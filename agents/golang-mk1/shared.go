package main

// Shared-board play: everything cooperative and teams mode add on top of the
// competitive machinery in game.go (jetris-gameplays.md §3/§5, guide §4).
// Cooperative: ONE board of playerCount×10, every player's falling piece on
// it, per-cell CAS with merge-retry, no garbage, game over on any top-out.
// Teams: one coop-style board per team plus the competitive garbage ledger
// between the boards, txn-gated transforms, and team-level outcomes.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	modeCooperative = 0
	modeTeams       = 2
)

// A shared board (cooperative, or one team's board) is `width` columns for
// its first seat and the game's `extra_columns` more for every seat after
// it — the creator's board-width setting, between minExtraColumns and
// maxExtraColumns (gameplays §2). The same step spaces the seats' spawn
// points, so seat i spawns at i×extra + spawnCol.
const (
	minExtraColumns = 4
	maxExtraColumns = width
)

// extraColumns clamps the meta's extra_columns to its legal range. Absent —
// zero, as every game created before the setting existed reads — means the
// historical board: a full `width` section per seat, i.e. maxExtraColumns.
func extraColumns(v int) int {
	if v <= 0 {
		return maxExtraColumns
	}
	return min(max(v, minExtraColumns), maxExtraColumns)
}

// sharedWidth is the width of a board shared by seats players.
func sharedWidth(seats, extra int) int {
	return width + max(seats-1, 0)*extra
}

// A shared board is `standardHeight` rows (headroom included) for its first
// seat and the game's `extra_rows` more for every seat after it — the
// creator's extra-rows setting, between minExtraRows and maxExtraRows. The
// headroom stays the top rows: the board grows downwards, so the spawn rows
// and the top-out rule are the same on every board.
const (
	standardHeight = headroom + 20
	minExtraRows   = 0
	maxExtraRows   = 10
)

// extraRows clamps the meta's extra_rows to its legal range. Absent — zero,
// as every game created before the setting existed reads — means exactly
// that: no extra rows, unlike the columns.
func extraRows(v int) int {
	return min(max(v, minExtraRows), maxExtraRows)
}

// sharedHeight is the height of a board shared by seats players.
func sharedHeight(seats, extra int) int {
	return standardHeight + max(seats-1, 0)*extraRows(extra)
}

// The number of teams a teams game is played between (meta team_count). Two —
// Team A vs Team B — is the default and what every meta written before the
// field reads as; team indices run 0..count-1 and are named by their letter.
const (
	defaultTeamCount = 2
	minTeamCount     = 2
	maxTeamCount     = 6
)

// normalizeTeamCount reads a meta's team_count: absent is the historical two,
// out of range is clamped.
func normalizeTeamCount(n int) int {
	if n <= 0 {
		return defaultTeamCount
	}
	return min(max(n, minTeamCount), maxTeamCount)
}

// teamLetter names a team index the way every screen shows it: A, B, C, …
func teamLetter(t int) string {
	if t < 0 || t >= 26 {
		return strconv.Itoa(t)
	}
	return string(rune('A' + t))
}

// teamNames names n teams after the piece colours in piece order — the
// GUI's default (meta team_names) when the creator renames none.
func teamNames(n int) []string {
	colors := []string{"Green", "Blue", "Amber", "Violet", "Teal", "Coral", "Navy"}
	out := make([]string, 0, n)
	for t := 0; t < n; t++ {
		out = append(out, colors[t%len(colors)])
	}
	return out
}

// gravityInterval is the guideline speed curve (gameplays §7): seconds per
// row = (0.8 − (L − 1) × 0.007)^(L − 1) with L = level + 1, to the
// millisecond, floored at one 60 Hz frame. Shared boards level up as lines
// accumulate; competitive stays at level 0 forever.
func gravityInterval(level int) time.Duration {
	if level < 0 {
		level = 0
	}
	l := float64(level)
	d := time.Duration(math.Round(math.Pow(0.8-l*0.007, l)*1000)) * time.Millisecond
	if d < time.Second/60 {
		return time.Second / 60
	}
	return d
}

// level is the shared-progression level for gravity: cooperative counts every
// clear on the board, teams counts the OWN team's; competitive never leaves 0.
func (g *Game) level() int {
	switch g.mode {
	case modeCooperative:
		return min(g.totalLines/10, 19)
	case modeTeams:
		return min(g.teamLines[g.team]/10, 19)
	default:
		return 0
	}
}

func (g *Game) gravityNow() time.Duration { return gravityInterval(g.level()) }

// shared reports whether this game's board is shared with other pieces.
func (g *Game) shared() bool { return g.mode != modeCompetitive }

// sharedBlocked reports whether any of the cells is held by ANOTHER player's
// falling piece — the transient obstacle class that never locks or tops out.
func (g *Game) sharedBlocked(cs [4]cell) bool {
	for _, c := range cs {
		if _, ok := g.othersAct[c]; ok {
			return true
		}
	}
	return false
}

// canMove is the full collision rule for this board: bounds + locked cells
// (canPlace) + other players' active pieces on shared boards.
func (g *Game) canMove(cs [4]cell) bool {
	return g.canPlace(cs) && !g.sharedBlocked(cs)
}

// dropRowShared is dropRowLocked honoring other players' pieces: the hard-drop
// row (a piece resting on another ACTIVE piece does not lock there — gravity
// resumes when the obstacle falls away).
func (g *Game) dropRowShared(a active) int {
	row := a.row
	for g.canMove(pieceCells(a.pt, a.orient, row+1, a.col)) {
		row++
	}
	return row
}

// ---- the shared-board consumer -------------------------------------------

// boardFilter is the consumer filter covering this game's OWN board: cells
// plus, in teams, the board's garbage and txn registers.
func (g *Game) boardFilter() string {
	switch g.mode {
	case modeCooperative:
		return "jetris.game." + g.id + ".playfield.>"
	case modeTeams:
		return fmt.Sprintf("jetris.game.%s.team.%d.playfield.>", g.id, g.team)
	default:
		return ""
	}
}

// handleBoardMsg folds one delivered board message into local state. Own
// echoes no-op via the strictly-higher-sequence rule (write-through already
// recorded them); everything newer — teammates' moves and locks, another
// clearer's collapse, a garbage cascade — updates the settled map, the
// other-active map, and even OUR OWN piece (an external transform may
// legitimately shift or lift it; we adopt the committed position).
func (g *Game) handleBoardMsg(m jetstream.Msg) {
	subject := m.Subject()
	var seq uint64
	if md, _ := m.Metadata(); md != nil {
		seq = md.Sequence.Stream
	}
	g.noteStreamSeq(seq)
	if strings.HasSuffix(subject, ".playfield.garbage") {
		var reg garbageReg
		if len(m.Data()) > 0 {
			_ = json.Unmarshal(m.Data(), &reg)
		}
		g.mu.Lock()
		if reg.Total > g.garbageOwed {
			g.garbageOwed = reg.Total
		}
		g.garbageBy = reg.By
		need := g.garbageOwed > g.txnApplied && !g.dead
		g.mu.Unlock()
		if need && g.mode == modeTeams {
			// Any alive member may apply; the txn gate admits exactly one.
			go g.applyOwedGarbage(g.runCtx)
		}
		return
	}
	if strings.HasSuffix(subject, ".playfield.txn") {
		var t txnReg
		if len(m.Data()) > 0 {
			_ = json.Unmarshal(m.Data(), &t)
		}
		g.mu.Lock()
		if seq > g.txnSeq {
			g.txnSeq = seq
			if t.Applied > g.txnApplied {
				g.txnApplied = t.Applied
			}
			for _, idx := range t.Topped {
				if idx == g.idx {
					// A garbage cascade pushed our piece off the top: the
					// zero-active edge is an elimination, not a lock-in.
					g.dead = true
					g.piece = nil
				}
			}
			if t.Full {
				g.dead = true // locked rows past the top: the whole board is out
			}
		}
		g.mu.Unlock()
		return
	}
	r, c, ok := parseCellSubject(subject)
	if !ok {
		return
	}
	at := cell{r, c}
	var wc wireCell
	if len(m.Data()) > 0 {
		_ = json.Unmarshal(m.Data(), &wc)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if seq <= g.seqs[at] {
		return // our own write-through (or older) — already accounted
	}
	g.seqs[at] = seq
	switch {
	case wc.A:
		delete(g.locked, at)
		if wc.Pi == g.idx {
			// Our own piece, moved by someone else's committed transform
			// (a clear shifting it down, a cascade lifting it): adopt — but
			// not while our own batches are in flight: then the echo is just
			// the stream catching up to the projection, and adopting it would
			// snap the piece back mid-pipeline (a real external transform
			// rejects an in-flight batch, and the repair adopts its truth).
			if g.inflight == 0 && (g.piece == nil || g.piece.row != wc.Ar || g.piece.col != wc.Ac || g.piece.orient != wc.R) {
				g.piece = &active{wc.T, wc.R, wc.Ar, wc.Ac}
			}
			delete(g.othersAct, at)
		} else {
			g.othersAct[at] = wc.Pi
			g.othersPiece[wc.Pi] = active{wc.T, wc.R, wc.Ar, wc.Ac}
		}
	case wc.O:
		g.dropOwner(at)
		g.locked[at] = wc
	default:
		delete(g.locked, at)
		g.dropOwner(at)
	}
}

// dropOwner removes a cell from the other-actives map; when that was the
// owner's LAST active cell (their piece locked, vacated, or was squeezed
// out), the reconstructed piece goes with it. Caller holds mu.
func (g *Game) dropOwner(at cell) {
	pi, ok := g.othersAct[at]
	if !ok {
		return
	}
	delete(g.othersAct, at)
	for _, owner := range g.othersAct {
		if owner == pi {
			return
		}
	}
	delete(g.othersPiece, pi)
}

// parseCellSubject extracts row/col from a ...playfield.cell.<r>.<c> subject.
func parseCellSubject(subject string) (int, int, bool) {
	i := strings.Index(subject, ".playfield.cell.")
	if i < 0 {
		return 0, 0, false
	}
	rest := subject[i+len(".playfield.cell."):]
	dot := strings.IndexByte(rest, '.')
	if dot < 0 {
		return 0, 0, false
	}
	r, err1 := strconv.Atoi(rest[:dot])
	c, err2 := strconv.Atoi(rest[dot+1:])
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return r, c, true
}

// resyncShared rebuilds the whole shared board — settled cells, every other
// player's active cells, our own piece if the stream still has it, and every
// per-cell sequence — from one multi-get snapshot of the stream (fetchBoard)
// after a dropped write. It IS resync (foldSnapshot handles both board
// kinds); the name survives at the shared-board call sites.
func (g *Game) resyncShared(ctx context.Context) { g.resync(ctx) }

// ---- clears on shared boards ---------------------------------------------

// completedRowsShared: full-width, all settled, no garbage, and no ACTIVE cell
// anywhere in the row (a row a falling piece crosses cannot clear).
func (g *Game) completedRowsShared() []int {
	rows := g.completedRows()
	out := rows[:0]
	for _, r := range rows {
		blocked := false
		for c := 0; c < g.w; c++ {
			if _, ok := g.othersAct[cell{r, c}]; ok {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, r)
		}
	}
	return out
}

// clearProjection computes the post-clear board: settled cells collapse down,
// and every OTHER player's falling piece shifts down with the stack (its owner
// keeps dropping, gameplays §3 — the batch's active-first ordering keeps it
// continuously present). Returns the new settled map and each shifted piece
// keyed by owner.
func (g *Game) clearProjection(rows []int) (map[cell]wireCell, map[int]active) {
	removed := make(map[int]bool, len(rows))
	for _, r := range rows {
		removed[r] = true
	}
	newLocked := map[cell]wireCell{}
	shift := 0
	for r := g.height() - 1; r >= 0; r-- {
		if removed[r] {
			shift++
			continue
		}
		for c := 0; c < g.w; c++ {
			if cc, ok := g.locked[cell{r, c}]; ok {
				newLocked[cell{r + shift, c}] = cc
			}
		}
	}
	// Every other falling piece keeps dropping, shifted down with the stack:
	// by the number of cleared rows below its lowest cell (rows above it
	// collapse nothing under it; a cleared row can never cross a piece — a
	// row with an active cell is not complete).
	moved := map[int]active{}
	for pi, p := range g.othersPiece {
		bottom := p.row
		for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
			if c.r > bottom {
				bottom = c.r
			}
		}
		down := 0
		for _, r := range rows {
			if r > bottom {
				down++
			}
		}
		moved[pi] = active{p.pt, p.orient, p.row + down, p.col}
	}
	return newLocked, moved
}

// clearRowsCoop collapses completed rows on the cooperative board with the
// shared-board CAS merge-retry: every changed cell carries its per-subject
// expectation, so a teammate's simultaneous move atomically rejects the whole
// batch; we refetch, recompute (the rows may no longer be complete), and
// retry a bounded number of times.
func (g *Game) clearRowsCoop(ctx context.Context, rows []int) int {
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			g.resyncShared(ctx)
			rows = g.completedRowsShared()
			if len(rows) == 0 {
				return 0
			}
		}
		newLocked, moved := g.clearProjection(rows)
		diff := g.diffCells(newLocked)
		for pi, np := range moved {
			op := g.othersPiece[pi]
			oldCells := cellSet(pieceCells(op.pt, op.orient, op.row, op.col))
			newCells := pieceCells(np.pt, np.orient, np.row, np.col)
			newSet := cellSet(newCells)
			pl := wireCell{T: np.pt, A: true, R: np.orient, Ar: np.row, Ac: np.col, Pi: pi}
			for c := range newSet {
				cc := pl
				diff = append(diff, cellUpd{at: c, c: &cc})
			}
			for c := range oldCells {
				if !newSet[c] {
					if _, isLocked := newLocked[c]; !isLocked {
						diff = append(diff, cellUpd{at: c})
					}
				}
			}
		}
		if err := g.publishBatch(ctx, diff, true); err != nil {
			if errors.Is(err, errCAS) {
				continue
			}
			log.Printf("coop clear: %v", err)
			return 0
		}
		g.locked = newLocked
		for pi, np := range moved {
			p := np
			g.othersPiece[pi] = p
		}
		g.reindexOthers()
		return len(rows)
	}
	return 0
}

// reindexOthers rebuilds the cell→owner map from the per-owner pieces.
func (g *Game) reindexOthers() {
	g.othersAct = map[cell]int{}
	for pi, p := range g.othersPiece {
		for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
			g.othersAct[c] = pi
		}
	}
}

// ---- events ---------------------------------------------------------------

// publishLineClear announces a lock that scored — a clear, or on a shared
// board a drop's points alone (lines_cleared 0) — with our CUMULATIVE totals
// (receivers fold deltas, so a retention-trimmed intermediate event is
// subsumed) and the Guideline's names for the clear, as the GUI does.
func (g *Game) publishLineClear(ctx context.Context, c clearInfo, pts int, rows []int) {
	ev := map[string]any{
		"kind": "line_clear", "player_id": g.a.name, "player_idx": g.idx,
		"lines_cleared": c.lines, "cleared_rows": rows, "score": pts,
		"team": g.team, "total_score": g.score, "total_lines": g.lines,
	}
	if c.spin != spinNone {
		ev["t_spin"] = int(c.spin)
	}
	if c.backToBack {
		ev["back_to_back"] = true
	}
	if c.combo > 0 {
		ev["combo"] = c.combo
	}
	if c.perfect {
		ev["perfect"] = true
	}
	b, _ := json.Marshal(ev)
	_, _ = g.a.js.Publish(ctx, "jetris.game."+g.id+".events.line_clear."+g.a.name, b)
}

// foldLineClear folds another player's cumulative totals — a line_clear
// event's, or the ones its game_over carries — into the shared/team
// scoreboards, as deltas against the last totals seen from that sender.
func (g *Game) foldLineClear(ev event) {
	if ev.PlayerID == g.a.name {
		return // our own echo: already folded at publish time
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	last := g.senderTotals[ev.PlayerID]
	ds, dl := ev.TotalScore-last[0], ev.TotalLines-last[1]
	if ds < 0 || dl < 0 {
		return
	}
	g.senderTotals[ev.PlayerID] = [2]int{ev.TotalScore, ev.TotalLines}
	if g.senderTeams == nil {
		g.senderTeams = map[string]int{}
	}
	g.senderTeams[ev.PlayerID] = ev.Team
	switch g.mode {
	case modeCooperative:
		// A board scored per seat folds the crew's lines (the shared level)
		// but never their points.
		if !g.individual {
			g.sharedScore += ds
		}
		g.totalLines += dl
	case modeTeams:
		if ev.Team >= 0 && ev.Team < len(g.teamScores) {
			g.teamScores[ev.Team] += ds
			g.teamLines[ev.Team] += dl
		}
	}
}

// ---- outcomes -------------------------------------------------------------

// teamDead reports whether every roster member of team t is eliminated.
// Caller holds mu.
func (g *Game) teamDead(t int) bool {
	n := 0
	for _, p := range g.roster {
		if p.Team != t {
			continue
		}
		n++
		if !g.eliminated[p.PlayerID] {
			return false
		}
	}
	// Every member out — or, in an open game, every member gone: a team
	// nobody holds a seat on any more is out (the game started with one).
	return n > 0 || g.hadRivals
}

// othersDead reports whether every team but our own is fully out — the moment
// we have won. With two teams that is simply the other one; with more it is
// the last-team-standing rule the whole game plays by. Caller holds mu.
func (g *Game) othersDead() bool {
	for t := 0; t < g.teams(); t++ {
		if t != g.team && !g.teamDead(t) {
			return false
		}
	}
	return true
}

// winningTeam is the game's verdict: the one team still standing, or -1 while
// more than one is (and on a draw, every team out together). Caller holds mu.
func (g *Game) winningTeam() int {
	alive := -1
	for t := 0; t < g.teams(); t++ {
		if g.teamDead(t) {
			continue
		}
		if alive >= 0 {
			return -1 // more than one team left: undecided
		}
		alive = t
	}
	return alive
}

// nextGarbageTarget picks the opposing team our next attack lands on: the
// other team in a duel, and past that the rotation's next, so consecutive
// raises spread over the opponents instead of every one of them taking the
// full raise. Same rule as the GUI engine's (internal/engine/ledger.go).
// Caller holds mu (the whole lock/clear/attack path runs under it).
func (g *Game) nextGarbageTarget() int {
	n := g.teams()
	t := (g.team + 1 + g.attackRotor%max(n-1, 1)) % n
	g.attackRotor++
	return t
}

// vacateOwnPiece removes our dead piece from the team board as a txn-gated
// transform (op "vacate"), so a racing garbage application can never
// resurrect it from a stale snapshot. Caller holds mu.
//
// It retries persistently: on a busy board the gate keeps losing to teammate
// moves and other transforms, and a dead piece that is never vacated becomes
// a ghost obstacle every falling piece hovers on forever (nobody ever locks
// against another player's "active" cells). Each retry resyncs, so a cascade
// that already vacated us ends the loop with piece == nil.
func (g *Game) vacateOwnPiece(ctx context.Context) {
	g.settlePipeline(ctx) // barrier: converge before the gated vacate
	const maxAttempts = 10
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Refresh from converged state: the winner's transform may have
			// moved our cells (or already vacated us — resync then clears
			// g.piece and we are done).
			g.resyncShared(ctx)
			g.refreshRegisters(ctx)
		}
		if g.piece == nil {
			return
		}
		var diff []cellUpd
		for _, c := range g.activeCells() {
			diff = append(diff, cellUpd{at: c})
		}
		txn := txnReg{Applied: g.txnApplied, Op: "vacate", By: g.idx}
		err := g.publishGatedBatch(ctx, txn, diff)
		if err == nil {
			g.piece = nil
			return
		}
		if !errors.Is(err, errCAS) {
			log.Printf("vacate: %v", err)
			g.piece = nil
			return
		}
	}
	log.Printf("vacate: gave up after %d attempts — dead piece may linger until the next board transform", maxAttempts)
	g.piece = nil
}

// waitForVerdict is the teams endgame for an eliminated (or winning) member:
// stay connected until every team but one is fully dead, then report whether
// OUR team is the one left. The winning side archives (CAS-deduplicated).
func (g *Game) waitForVerdict(ctx context.Context) bool {
	deadline := time.After(10 * time.Minute)
	for {
		g.mu.Lock()
		enemyDead := g.othersDead()
		ownDead := g.teamDead(g.team)
		g.mu.Unlock()
		if enemyDead {
			g.transitionFinishedAndArchive(ctx)
			return true
		}
		if ownDead {
			g.waitForEnd(ctx)
			return false
		}
		select {
		case <-g.ended:
			g.mu.Lock()
			enemyDead = g.othersDead()
			g.mu.Unlock()
			return enemyDead
		case <-ctx.Done():
			return false
		case <-g.a.stopCh:
			return false
		case <-deadline:
			return false
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// ---- the deal --------------------------------------------------------------

// mySlot is this seat's slot on its playfield: the seat itself on the crew's
// board, the slot within the team on a team's.
func (g *Game) mySlot() int {
	if g.mode == modeTeams {
		return g.teamSlot
	}
	return g.idx
}

// presentSlots is the slots seated on our playfield per a listing's roster:
// every seat on the crew's board, our team's slots on a team's.
func (g *Game) presentSlots(roster []playerSummary) []int {
	var slots []int
	for _, p := range roster {
		switch g.mode {
		case modeCooperative:
			slots = append(slots, p.Seat)
		case modeTeams:
			if p.Team == g.team {
				slots = append(slots, p.TeamSlot)
			}
		}
	}
	sort.Ints(slots)
	return slots
}

// redeal follows the GUI's engine (outcome.go redealLocked): in a game that
// splits its pieces, the seven types are dealt out between the slots
// PRESENT on our playfield — the seats an open game has right now — ranked
// in slot order, and this seat draws from its rank's ration. Every slot
// present (or no roster at all) is the deal at creation. Only our own
// sequence depends on it, so nobody has to agree; the piece index stands.
// Returns whether the ration changed. mu held.
func (g *Game) redealLocked(present []int) bool {
	if !g.split || g.seatsOnPF <= 1 {
		return false
	}
	var slots []int
	seen := map[int]bool{}
	for _, s := range present {
		if s >= 0 && s < g.seatsOnPF && !seen[s] {
			seen[s] = true
			slots = append(slots, s)
		}
	}
	sort.Ints(slots)
	if len(slots) == 0 {
		slots = slots[:0]
		for i := 0; i < g.seatsOnPF; i++ {
			slots = append(slots, i)
		}
		g.presentPF = nil
	} else if slices.Equal(slots, g.presentPF) {
		return false
	} else {
		g.presentPF = slots
	}
	rank := slices.Index(slots, g.mySlot())
	if rank < 0 {
		rank = 0 // not in the deal (a seat we are leaving): the first ration
	}
	g.ration = pieceSetFor(g.metaSeed, len(slots), rank)
	return true
}

// ---- the line goal ---------------------------------------------------------

// playfieldLinesLocked is the lines the playfield ev's sender plays on has
// cleared, as the stream told us so far — our own included: every seat's on
// the crew's board, our team's seats' on a team's board, the sender's own on
// a competitive board (the GUI's goal.go, mirrored). mu held.
func (g *Game) playfieldLinesLocked(ev event) int {
	switch g.mode {
	case modeCooperative:
		n := g.lines
		for _, t := range g.senderTotals {
			n += t[1]
		}
		return n
	case modeTeams:
		n := 0
		if ev.Team == g.team {
			n = g.lines
		}
		for id, t := range g.senderTotals {
			if g.senderTeams[id] == ev.Team {
				n += t[1]
			}
		}
		return n
	default:
		if ev.PlayerID == g.a.name {
			return g.lines
		}
		return g.senderTotals[ev.PlayerID][1]
	}
}

// checkLineGoal runs on every line_clear consumed (our own echo too): when
// the sender's playfield has reached the goal the game is decided — the
// crew wins together, a board scored per seat by its top scorer(s), a team
// by its members, a competitive board by its player — and the play loop
// ends; run finishes the meta (guide §5).
func (g *Game) checkLineGoal(ev event) {
	if g.lineGoal <= 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.goalDecided || g.playfieldLinesLocked(ev) < g.lineGoal {
		return
	}
	g.goalDecided = true
	switch g.mode {
	case modeCooperative:
		g.goalWon = !g.individual || g.individualWinnerLocked()
	case modeTeams:
		g.goalWon = ev.Team == g.team
	default:
		g.goalWon = ev.PlayerID == g.a.name
	}
	log.Printf("line goal reached by %s's playfield — %s", ev.PlayerID, map[bool]string{true: "we win", false: "we lose"}[g.goalWon])
	g.markEnded()
}
