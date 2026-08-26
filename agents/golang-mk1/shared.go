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
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const (
	modeCooperative = 0
	modeTeams       = 2
)

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
			// (a clear shifting it down, a cascade lifting it): adopt.
			if g.piece == nil || g.piece.row != wc.Ar || g.piece.col != wc.Ac || g.piece.orient != wc.R {
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
// after a dropped write. A fetch that fails outright leaves the current state
// in place (and logs) rather than wiping the board to empty.
func (g *Game) resyncShared(ctx context.Context) {
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		log.Printf("resync: %v", err)
		return
	}
	g.locked = map[cell]wireCell{}
	g.othersAct = map[cell]int{}
	g.othersPiece = map[int]active{}
	g.seqs = map[cell]uint64{}
	g.piece = nil
	for at, m := range snap {
		g.seqs[at] = m.seq
		wc := m.wc
		switch {
		case wc.A && wc.Pi == g.idx:
			g.piece = &active{wc.T, wc.R, wc.Ar, wc.Ac}
		case wc.A:
			g.othersAct[at] = wc.Pi
			g.othersPiece[wc.Pi] = active{wc.T, wc.R, wc.Ar, wc.Ac}
		case wc.O:
			g.locked[at] = wc
		}
	}
}

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

// publishLineClear announces our clear with our CUMULATIVE totals (receivers
// fold deltas, so a retention-trimmed intermediate event is subsumed).
func (g *Game) publishLineClear(ctx context.Context, lines, scoreDelta int, rows []int) {
	ev := map[string]any{
		"kind": "line_clear", "player_id": g.a.name, "player_idx": g.idx,
		"lines_cleared": lines, "cleared_rows": rows, "score": scoreDelta,
		"team": g.team, "total_score": g.score, "total_lines": g.lines,
	}
	b, _ := json.Marshal(ev)
	_, _ = g.a.js.Publish(ctx, "jetris.game."+g.id+".events.line_clear."+g.a.name, b)
}

// foldLineClear folds another player's cumulative line-clear totals into the
// shared/team scoreboards.
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
	switch g.mode {
	case modeCooperative:
		g.sharedScore += ds
		g.totalLines += dl
	case modeTeams:
		if ev.Team >= 0 && ev.Team < 2 {
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
	return n > 0
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
// stay connected until one team is fully dead, then report whether OUR team
// prevailed. The winning side archives (CAS-deduplicated).
func (g *Game) waitForVerdict(ctx context.Context) bool {
	deadline := time.After(10 * time.Minute)
	for {
		g.mu.Lock()
		enemyDead := g.teamDead(1 - g.team)
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
			enemyDead = g.teamDead(1 - g.team)
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
