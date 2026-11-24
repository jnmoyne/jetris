package main

// Coordination on a shared board: what the agent does with its teammates'
// claims on the blackboard (blackboard.go) — plans on them, yields to the
// earlier ones, waits for the ones it depends on — and when a plan is no
// longer the thing to do (requestReplan). The blackboard's own plumbing is
// blackboard.go; the planner's projection is planner.go's.

import (
	"sort"
	"time"
)

// replanReason says why the piece in play is re-planned from where it
// stands (playPieces re-plans after a rethinkDelay instead of the full
// think pause).
type replanReason int

const (
	replanNone        replanReason = iota
	replanOverlap                  // an earlier claim took our spot (plan's linearization)
	replanDropRow                  // the plan's landing is no longer what the board gives: a teammate's lock, clear or collapse changed it
	replanDepTimeout               // the earlier claim we waited on never landed
	replanCollapse                 // a peer's line clear collapsed rows around our piece
	replanStale                    // no progress for three seconds (golang-mk1's resync rule)
	replanUnreachable              // no path to the plan, and nowhere better to wait (the stack moved)
	replanLockDropped              // the CAS lock was dropped after its merge-retry
	replanBlocked                  // a crewmate's piece has stood in the way past our patience: planned again among what the piece can walk to now
	replanHoles                    // the landing would cover more than the plan expected: the board is not the one it was planned on (spoils)
)

func (r replanReason) String() string {
	switch r {
	case replanOverlap:
		return "overlap"
	case replanDropRow:
		return "droprow"
	case replanDepTimeout:
		return "deptimeout"
	case replanCollapse:
		return "collapse"
	case replanStale:
		return "stale"
	case replanUnreachable:
		return "unreachable"
	case replanLockDropped:
		return "lockdropped"
	case replanHoles:
		return "holes"
	case replanBlocked:
		return "blocked"
	}
	return "none"
}

const (
	// depWaitMin and depWaitTimeout bound how long a drop is held for an
	// earlier claim to land before the piece is re-planned without it
	// (depTimeout: twice the claim's own estimate, between the two).
	depWaitMin     = 300 * time.Millisecond
	depWaitTimeout = 1500 * time.Millisecond
	// rethinkDelay is a re-plan's think pause: the piece is on the board
	// already and the difficulty's pause has been spent on it, so a re-plan
	// costs the plan alone.
	rethinkDelay = 0
	// lockDelay is the grace a piece resting on the stack AWAY from its
	// planned column gets on a shared board before it locks where it stands
	// (the GUI's LockDelay): a crewmate's piece across its path falls away
	// within it, and the walk resumes. golang-mk1 locked at once. It is the
	// most the grace is: at a fast level a crewmate's piece is gone sooner
	// than that (lockDelayNow).
	lockDelay = 500 * time.Millisecond
	// pauseRows is the most rows gravity may take a fresh piece down while
	// its player thinks (thinkPause): the pause is what the difficulty
	// says only while the level leaves it that long.
	pauseRows = 3
)

// lockDelayNow is the lock delay at the level being played: what a
// crewmate's piece needs to fall the whole board and be out of the way —
// lockDelay at most, four move steps at least. Level 13 leaves 437 ms of
// it, level 15 170. Caller holds mu.
func (g *Game) lockDelayNow() time.Duration {
	return min(lockDelay, max(time.Duration(g.height())*g.gravityNow(), 4*g.a.tn.moveDelay))
}

// capWait bounds a wait of the piece in play — on a claim it depends on, on
// a crewmate's piece across its way — by the air the piece has left: gravity
// goes on while it waits, and a wait that outlasts the fall ends with the
// piece on the stack under where it waited, most placements walled off.
// Asked every turn of the wait, it ends the wait with about half the air
// gone, while the piece can still walk somewhere else. A piece that stands
// on something (the stack: the lock delay's business; a crewmate's piece:
// it is going nowhere) waits as long as it did. Caller holds mu.
func (g *Game) capWait(d time.Duration) time.Duration {
	if g.piece == nil {
		return d
	}
	_, fell, _ := g.fallRows(*g.piece, g.height())
	if fell == 0 {
		return d
	}
	return min(d, max(time.Duration(fell)*g.gravityNow(), g.a.tn.moveDelay))
}

// fallenAt is the piece p as gravity will have it at the instant at: down
// the rows the clock owes by then (execute pays them as one batch before
// the walk), as far as the stack lets it. Where the walk STARTS — and at a
// fast level that is a long way under the spawn: 10 rows after a 115 ms
// think at level 14. The crewmates' pieces are not in its way here: they
// move. Caller holds mu.
func (g *Game) fallenAt(p active, at time.Time) active {
	if g.nextGravity.IsZero() {
		return p
	}
	rows := g.gravityDebt
	if step := g.gravityNow(); step > 0 && !at.Before(g.nextGravity) {
		rows += 1 + int(at.Sub(g.nextGravity)/step)
	}
	for ; rows > 0; rows-- {
		if !g.canPlace(pieceCells(p.pt, p.orient, p.row+1, p.col)) {
			break
		}
		p.row++
	}
	return p
}

// thinkLagMax bounds what a plan is taken to cost beyond its pause.
const thinkLagMax = 200 * time.Millisecond

// noteThinkLag records what the last plan took beyond its think pause —
// the search, the claim's round trip, its echo — smoothed: the rows gravity
// takes meanwhile are part of where the walk starts (fallenAt). A pause cut
// short (a hurry) measures nothing. Caller holds mu.
func (g *Game) noteThinkLag(lag time.Duration) {
	if lag < 0 {
		return
	}
	lag = min(lag, thinkLagMax)
	if g.thinkLag == 0 {
		g.thinkLag = lag
		return
	}
	g.thinkLag = (3*g.thinkLag + lag) / 4
}

// planStatus is what checkPlan says of the plan on the live board.
type planStatus int

const (
	planOK      planStatus = iota
	planWaitDep            // the landing is not there yet: an earlier claim we wait on will put it there
	planStale              // the landing is taken, or gone
)

// requestReplan asks playPieces to re-plan the piece from where it stands,
// the first reason winning; every request is counted. Caller holds mu.
func (g *Game) requestReplan(r replanReason) {
	if g.replan == replanNone {
		g.replan = r
	}
	g.trace("re-plan requested: %s", r)
	g.st.noteReplan(r.String())
}

// checkPlan says whether the plan's landing still stands on the live board:
// stale when the stack has taken any of its cells, or when nothing holds
// them up any more (a collapse took the rows under them) — unless an
// earlier claim we wait on is what will hold them up, which is a wait.
// Caller holds mu.
func (g *Game) checkPlan(pl placement) planStatus {
	if !g.canPlace(pl.dest) {
		return planStale
	}
	h := g.height()
	for _, c := range pl.dest {
		below := cell{c.r + 1, c.c}
		if below.r >= h {
			return planOK
		}
		if _, ok := g.locked[below]; ok {
			return planOK
		}
	}
	if g.depsPending() {
		return planWaitDep
	}
	return planStale
}

// The placement penalties a coordinated board adds (projection.penalty), in
// Dellacherie's units (a hole is 7.9).
const (
	depPenalty  = 2.0  // per second an earlier claim we would wait on is expected to take
	softPenalty = 1.5  // per cell of the placement in, or on top of, a silent teammate's straight drop
	tentPenalty = 1.0  // per cell of the placement in a teammate's tentative landing for a NEXT piece, times its weight (1 for the first, half for the second)
	intentBonus = 1.0  // for the placement we announced for this piece while it was in our NEXT well: said, it is mostly kept
	travelCost  = 0.15 // per column between our spawn point and the placement — a projection without a route only
	crossCost   = 4.0  // per imminent teammate spawn box the walk crosses — a projection without a route only
)

// depWait is one claim the piece in play must let land first: the seat's,
// for the piece and at the revision it was made (a newer claim of theirs —
// the lock, or a re-plan — ends the wait; so does every cell of the landing
// settling on the board).
type depWait struct {
	seat, piece int
	rev         uint64
	cells       []cell
}

// orderedClaims is the teammates' claims on our board — every seat the
// listing still holds but ours — in the right of way: by the revision each
// placement was first claimed at (claim.prio), the order everyone sees.
// Caller holds mu.
func (g *Game) orderedClaims() []claim {
	var out []claim
	for seat, c := range g.claims {
		if seat == g.idx || c.Seat != seat || !g.seatPresentLocked(seat) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].order() != out[j].order() {
			return out[i].order() < out[j].order()
		}
		return out[i].rev < out[j].rev
	})
	return out
}

// toGridProjected is the settled board alone: no falling piece of anyone's
// on it. A teammate's piece is going where its claim says — or, silent,
// where its straight drop lands — not staying where it is, and the
// projection stamps that instead (buildProjection). golang-mk1 planned
// round the pieces where they stood, and so onto them: a placement that
// "landed" on a crossing piece, re-planned once it had gone. Caller holds
// mu.
func (g *Game) toGridProjected() *grid {
	gr := newGrid(g.height(), g.w)
	for c, v := range g.locked {
		if c.r < 0 || c.r >= gr.h || c.c < 0 || c.c >= gr.w {
			continue
		}
		if v.G {
			gr.set(c.r, c.c, 2)
		} else {
			gr.set(c.r, c.c, 1)
		}
	}
	return gr
}

// projectClaim is where a claim's piece is going to settle on the grid as
// it stands: a planned claim re-dropped by its column and orientation from
// where the piece is (the claim's own cells may be a collapse old), a
// locked claim's cells verbatim. ok is false for a claim the grid cannot
// take at all.
func (g *Game) projectClaim(gr *grid, c claim) ([4]cell, bool) {
	if c.Phase == phaseLocked {
		var cs [4]cell
		for i, rc := range c.Cells {
			cs[i] = cell{rc[0], rc[1]}
		}
		return cs, true
	}
	from := spawnRow
	if op, ok := g.othersPiece[c.Seat]; ok && op.pt == c.Type {
		from = op.row
	}
	if !gr.canPlace(pieceCells(c.Type, c.Rot, from, c.Col)) {
		return [4]cell{}, false
	}
	return pieceCells(c.Type, c.Rot, gr.dropRow(c.Type, c.Rot, from, c.Col), c.Col), true
}

// stampClaimed marks a landing on the grid as a teammate's.
func stampClaimed(gr *grid, cs [4]cell) {
	for _, c := range cs {
		if c.r >= 0 && c.r < gr.h && c.c >= 0 && c.c < gr.w {
			gr.set(c.r, c.c, gridClaimed)
		}
	}
}

// buildProjection is the board the planner plans on when the board is
// coordinated: the settled cells, plus every teammate's landing as their
// claim says — in revision order, each dropped onto the board the earlier
// ones made. A locked claim adds nothing: its cells are on the board, or
// reach it off the game stream within a moment. A planned claim for a
// piece the seat no longer holds
// (their board shows another type) is an old one. A teammate with a piece
// on the board and no claim for it — a human, a golang-mk1, an agent that
// has not planned it yet — gets a nudge, not a wall: their piece's straight
// drop from where it is goes on the projection's soft map, and a placement
// in it or on top of it pays for that (penalty), while the grid stays as
// it is — nobody knows where they are going, and a wall there once kept
// the first planner of a game out of the one gap on the board because the
// other's piece hung above it, unplanned. Caller holds mu.
func (g *Game) buildProjection() (*grid, *projection) {
	gr := g.toGridProjected()
	proj := &projection{claimed: map[cell]int{}, soft: map[cell]int{}, depETA: map[int]int{}, pieces: map[int]int{},
		tent: map[cell]float64{}, ownSpawnC: g.spawnC, boxes: g.teammateBoxes()}
	claimed := map[int]bool{}
	for _, c := range g.orderedClaims() {
		switch c.Phase {
		case phaseLocked:
			// On the board, or a moment from it off the game stream — and
			// its cells in the lock's own frame, which a collapse may have
			// moved since: nothing to stamp. The seat is heard from,
			// though: no ghost for it.
			claimed[c.Seat] = true
		case phasePlanned:
			if op, ok := g.othersPiece[c.Seat]; ok && op.pt != c.Type {
				continue
			}
			if prio, ok := g.ignoredClaims[c.Seat]; ok && prio == c.order() {
				continue // waited on past its time: whatever holds it up, we plan without it
			}
			cs, ok := g.projectClaim(gr, c)
			if !ok {
				continue
			}
			stampClaimed(gr, cs)
			for _, x := range cs {
				proj.claimed[x] = c.Seat
			}
			proj.depETA[c.Seat] = c.ETAms
			proj.pieces[c.Seat] = c.Piece
			claimed[c.Seat] = true
		}
	}
	proj.well, proj.wellOn, proj.wellHigh = g.wellFor(gr)
	if g.myIntent != nil && g.myIntent.Piece == g.pieceIdx {
		proj.intent = &[2]int{g.myIntent.Rot, g.myIntent.Col}
	}
	g.stampTentative(gr, proj)
	// Our own piece's cells stay ours: a claim whose landing passes through
	// where the piece stands right now (it has not landed, so the grid has
	// the cells empty) must not make its own position unplaceable — the
	// planner starts from there, and a start it cannot place is "no
	// placement at all", which once read as a top-out four pieces into a
	// three-agent game.
	if g.piece != nil {
		for _, c := range pieceCells(g.piece.pt, g.piece.orient, g.piece.row, g.piece.col) {
			if c.r >= 0 && c.r < gr.h && c.c >= 0 && c.c < gr.w && gr.at(c.r, c.c) == gridClaimed {
				gr.set(c.r, c.c, 0)
				delete(proj.claimed, c)
			}
		}
	}
	seats := make([]int, 0, len(g.othersPiece))
	for pi := range g.othersPiece {
		if !claimed[pi] {
			seats = append(seats, pi)
		}
	}
	sort.Ints(seats)
	for _, pi := range seats {
		op := g.othersPiece[pi]
		if !gr.canPlace(pieceCells(op.pt, op.orient, op.row, op.col)) {
			continue
		}
		for _, x := range pieceCells(op.pt, op.orient, gr.dropRow(op.pt, op.orient, op.row, op.col), op.col) {
			proj.soft[x] = pi
		}
	}
	return gr, proj
}

// conflictKind is classify's verdict on a placement against the claims
// made before ours.
type conflictKind int

const (
	noConflict         conflictKind = iota
	conflictDependency              // ordered after one or more earlier claims: keep it, wait for them
	conflictOverlap                 // an earlier claim's landing meets ours: yield
)

// classify checks our placement — the piece p, planned at cand — against
// every claim made before ours (prio < myPrio), in that order, each
// dropped onto the board the earlier ones made: an earlier landing that
// meets ours is an overlap, and we yield (taken is then every earlier
// landing's cells, for the next placement to keep clear of); one that
// changes where ours lands, or whose landing ours would change were we to
// land first, orders ours after it — a dependency to wait out (depWait).
// Claims made after ours are theirs to yield to. A LOCKED claim is no
// claim at all here: its piece is on the board — its cells reach us off
// the game stream, and a landing of ours they have taken is checkPlan's to
// find (a re-plan, on the board as it is; the claim's own cells are in the
// frame of the lock, a collapse old within a moment). Caller holds mu.
func (g *Game) classify(p active, cand placement, myPrio uint64) (kind conflictKind, deps []depWait, taken map[cell]bool) {
	acc := g.toGridProjected()
	dest := cellSet(cand.dest)
	taken = map[cell]bool{}
	overlap := false
	for _, c := range g.orderedClaims() {
		if c.Phase == phaseLocked || c.order() >= myPrio {
			continue
		}
		if prio, ok := g.ignoredClaims[c.Seat]; ok && prio == c.order() {
			continue
		}
		their, ok := g.projectClaim(acc, c)
		if !ok {
			continue
		}
		if op, ok := g.othersPiece[c.Seat]; ok && op.pt != c.Type {
			continue
		}
		for _, x := range their {
			taken[x] = true
			if dest[x] {
				overlap = true
			}
		}
		if overlap {
			stampClaimed(acc, their)
			continue // the rest of the earlier landings, for taken
		}
		if acc.canPlace(pieceCells(p.pt, cand.orient, p.row, cand.col)) {
			without := acc.dropRow(p.pt, cand.orient, p.row, cand.col)
			with := acc.clone()
			stampClaimed(with, their)
			withRow := without
			if with.canPlace(pieceCells(p.pt, cand.orient, p.row, cand.col)) {
				withRow = with.dropRow(p.pt, cand.orient, p.row, cand.col)
			}
			mine := acc.clone()
			for _, x := range cand.dest {
				if x.r >= 0 && x.r < mine.h && x.c >= 0 && x.c < mine.w {
					mine.set(x.r, x.c, 1)
				}
			}
			theirsIfIFirst, _ := g.projectClaim(mine, c)
			if withRow != without || theirsIfIFirst != their {
				deps = append(deps, depWait{seat: c.Seat, piece: c.Piece, rev: c.rev, cells: their[:]})
				kind = conflictDependency
			}
		}
		stampClaimed(acc, their)
	}
	if overlap {
		return conflictOverlap, nil, taken
	}
	return kind, deps, taken
}

// mergeDeps is the piece's waits: the seats whose claimed landings hold the
// placement up (the projection's waitOn), and what classify ordered it
// after — one wait per seat.
func (g *Game) mergeDeps(pl placement, proj *projection, more []depWait) []depWait {
	var out []depWait
	seen := map[int]bool{g.idx: true} // never on ourselves
	for _, seat := range pl.waitOn {
		if seen[seat] {
			continue
		}
		d := depWait{seat: seat, piece: proj.pieces[seat]}
		if c, ok := g.claims[seat]; ok {
			d.rev = c.rev
		}
		for x, s := range proj.claimed {
			if s == seat {
				d.cells = append(d.cells, x)
			}
		}
		out = append(out, d)
		seen[seat] = true
	}
	for _, d := range more {
		if !seen[d.seat] {
			out = append(out, d)
			seen[d.seat] = true
		}
	}
	return out
}

// relandPlan re-derives a plan's landing on the stack as it stands: the
// same column and orientation, dropped from where the piece is now. A
// crewmate's collapse moved the piece and the stack alike, but not by the
// same rows where a cleared row lay between the two; the plan keeps its
// intent, the landing follows the board. Caller holds mu.
func (g *Game) relandPlan(p active, pl placement) placement {
	if !g.canPlace(pieceCells(p.pt, pl.orient, p.row, pl.col)) {
		return pl // the checks that follow will call it stale
	}
	pl.dropRow = g.dropRowLocked(active{p.pt, pl.orient, p.row, pl.col})
	pl.dest = pieceCells(p.pt, pl.orient, pl.dropRow, pl.col)
	return pl
}

// depResolved reports whether a wait is over: their lock, or no claim of
// theirs at all any more, or every cell of the landing settled — or a
// newer claim of theirs for ANOTHER landing (a re-plan: then checkPlan
// says whether our landing still stands). A newer claim for the SAME
// landing keeps the wait, under the new revision: a re-plan of theirs that
// changed nothing must not void ours, or two pieces resting on each
// other's claims re-plan each other forever. Caller holds mu.
func (g *Game) depResolved(d *depWait) bool {
	c, ok := g.claims[d.seat]
	if !ok {
		return true
	}
	if c.rev != d.rev {
		if c.Phase != phasePlanned || c.Piece != d.piece {
			return true
		}
		their, ok := g.projectClaim(g.toGridProjected(), c)
		if !ok || !sameCells(their[:], d.cells) {
			return true
		}
		d.rev = c.rev // the same landing, claimed again: still coming
	}
	for _, x := range d.cells {
		if _, ok := g.locked[x]; !ok {
			return false
		}
	}
	return len(d.cells) > 0
}

// sameCells reports whether two landings are the same cells.
func sameCells(a, b []cell) bool {
	if len(a) != len(b) {
		return false
	}
	set := map[cell]bool{}
	for _, c := range a {
		set[c] = true
	}
	for _, c := range b {
		if !set[c] {
			return false
		}
	}
	return true
}

// depsPending reports whether the piece in play still waits on an earlier
// claim to land. Caller holds mu.
func (g *Game) depsPending() bool {
	for i := range g.deps {
		if !g.depResolved(&g.deps[i]) {
			return true
		}
	}
	return false
}

// depTimeout is how long the piece waits on the claims it depends on: twice
// what the slowest of them said it would take, within depWaitMin and
// depWaitTimeout — and no longer than the piece has air for (capWait).
// Caller holds mu.
func (g *Game) depTimeout() time.Duration {
	limit := depWaitMin
	for i := range g.deps {
		if g.depResolved(&g.deps[i]) {
			continue
		}
		if c, ok := g.claims[g.deps[i].seat]; ok {
			limit = max(limit, 2*time.Duration(c.ETAms)*time.Millisecond)
		}
	}
	return g.capWait(min(limit, depWaitTimeout))
}

// awaitDeps accounts a turn of waiting on the claims the piece depends on
// and reports whether the wait has run out (depTimeout): then the claims
// still pending are ignored by the re-plan that follows (ignoredClaims) —
// whatever holds them up, the piece goes on without them, and its landing
// is theirs to work around. Caller holds mu.
func (g *Game) awaitDeps(now time.Time) (timedOut bool) {
	if g.depSince.IsZero() {
		g.depSince = now
		g.st.depWaits++
	}
	if now.Sub(g.depSince) > g.depTimeout() {
		if g.ignoredClaims == nil {
			g.ignoredClaims = map[int]uint64{}
		}
		for i := range g.deps {
			if !g.depResolved(&g.deps[i]) {
				if c, ok := g.claims[g.deps[i].seat]; ok {
					g.ignoredClaims[g.deps[i].seat] = c.order()
				}
			}
		}
		g.endDepWait(now)
		return true
	}
	return false
}

// thinkPause is the difficulty's think pause for a fresh piece — jittered
// by up to a third on a coordinated board, so crewmates of one difficulty
// do not plan in lockstep: three agents planning at the same instant see
// none of each other's claims and all take the same spot, and the later
// two yield every time. Gravity does not wait for the thought: the pause is
// no longer than pauseRows rows of the level being played take — all of
// hard's 100 ms up to level 11, 55 ms at level 13, 21 ms at level 15 —
// else the piece is on the stack under its spawn before its first move.
// Takes mu.
func (g *Game) thinkPause() time.Duration {
	g.mu.Lock()
	step := g.gravityNow()
	g.mu.Unlock()
	d := min(g.a.tn.pieceDelay, pauseRows*step)
	if g.coord == nil || g.a.rng == nil || d <= 0 {
		return d
	}
	return time.Duration(float64(d) * (0.67 + g.a.rng.Float64()*0.66))
}

// endDepWait closes the wait's account. Caller holds mu.
func (g *Game) endDepWait(now time.Time) {
	if !g.depSince.IsZero() {
		g.st.depWaitMs += now.Sub(g.depSince).Milliseconds()
		g.depSince = time.Time{}
	}
}

// depColumns is every column the landings we still wait on need — the
// columns their pieces fall through, which ours must keep out of while it
// waits, or they can never land and the wait can never end. Caller holds
// mu.
func (g *Game) depColumns() map[int]bool {
	cols := map[int]bool{}
	for i := range g.deps {
		if g.depResolved(&g.deps[i]) {
			continue
		}
		for _, c := range g.deps[i].cells {
			cols[c.c] = true
		}
	}
	return cols
}

// depAside is where the piece waits for the claims it depends on: where it
// stands, if none of the columns their landings need is under it; else the
// nearest reachable spot, at its orientation, clear of those columns and of
// every imminent spawn box (stepAside). moved is false when it need not, or
// cannot, move. Caller holds mu.
func (g *Game) depAside(p active) (active, bool) {
	avoid := g.depColumns()
	if !columnsMeet(p, avoid) {
		return p, false
	}
	return g.stepAside(p, avoid)
}

// columnsMeet reports whether any cell of the piece is in one of the columns.
func columnsMeet(p active, cols map[int]bool) bool {
	for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
		if cols[c.c] {
			return true
		}
	}
	return false
}

// stepAside is the nearest reachable spot — to the left or the right,
// whichever is nearer — at the piece's orientation whose cells are clear of
// the columns given and of every imminent teammate spawn box. Caller holds
// mu.
func (g *Game) stepAside(p active, avoid map[int]bool) (active, bool) {
	boxes := g.teammateBoxes()
	clear := func(a active) bool {
		if columnsMeet(a, avoid) {
			return false
		}
		cs := pieceCells(a.pt, a.orient, a.row, a.col)
		for _, b := range boxes {
			if b.imminent && b.covers(cs) {
				return false
			}
		}
		return true
	}
	for d := 1; d < g.w; d++ {
		for _, col := range []int{p.col - d, p.col + d} {
			res := g.findPath(p, placement{orient: p.orient, col: col}, boxes)
			if res.reached && res.end != p && clear(res.end) {
				return res.end, true
			}
		}
	}
	return p, false
}

// ---- routes: where the teammates' pieces will be, and what a walk costs ----

const (
	// routeMove is what a move of the walk to a placement costs the
	// placement, in Dellacherie's units: a tie-break between equals — a
	// placement at the far end of a wide board costs a twentieth of a hole.
	// The walk itself is one batch however long it is; what a far placement
	// really costs is the wait when a crewmate's piece is across the way,
	// and that is priced by the wait (depPenalty a second), not by distance.
	routeMove = 0.02
	// routeBlocked is what a placement no walk reaches costs in a re-plan
	// made because the piece is blocked (routeCostsNow): it ranks below
	// everything the piece can reach where it stands.
	routeBlocked = 1e3
	// silentLeave is how long a teammate's piece is expected to stay in the
	// air when nothing says: a seat that does not speak, or one that waits.
	silentLeave = time.Second
)

// forecastPiece is a teammate's falling piece as it is expected to stand
// when ours moves: the cells it holds until then — where it is, and the
// rows gravity takes it through meanwhile — and when it is expected to be
// gone from them (landed, by its claim; a guess, without one).
type forecastPiece struct {
	seat    int
	cells   []cell
	leaveAt time.Time
}

// forecast is the teammates' pieces expected to be in the air at execAt —
// the "where is everyone going" the claims are there to answer. A piece
// whose claim says it is planned, hard-dropped, not waiting on anyone, and
// due to land by execAt is GONE: it will have left, and its landing is the
// projection's business. Every other piece — one that soft-drops, one that
// waits, one of a seat that says nothing — is where it is, plus the rows
// gravity carries it down until execAt, and is expected to leave when its
// claim says, or after silentLeave. Sorted by when they leave. Caller
// holds mu.
func (g *Game) forecast(now, execAt time.Time) []forecastPiece {
	if !g.shared() {
		return nil
	}
	seats := make([]int, 0, len(g.othersPiece))
	for pi := range g.othersPiece {
		seats = append(seats, pi)
	}
	sort.Ints(seats)
	fall := 0
	if step := g.gravityNow(); step > 0 && execAt.After(now) {
		fall = int(execAt.Sub(now) / step)
	}
	var out []forecastPiece
	for _, pi := range seats {
		op := g.othersPiece[pi]
		leave := now.Add(silentLeave)
		if c, ok := g.claims[pi]; ok && c.Seat == pi {
			switch {
			case c.Phase == phasePlanned && c.Type == op.pt && c.Drop != dropSoft && len(c.WaitOn) == 0:
				leave = c.seenAt.Add(time.Duration(c.ETAms) * time.Millisecond)
				if !leave.After(execAt) {
					continue // it will have landed by then
				}
			case c.Phase == phaseLocked || c.Type != op.pt:
				// A seat that speaks, holding a piece it has not claimed
				// yet: it will plan it, think and go as we do.
				leave = now.Add(g.a.tn.pieceDelay + g.a.tn.moveDelay + 50*time.Millisecond)
			}
		}
		fp := forecastPiece{seat: pi, leaveAt: leave}
		for d := 0; d <= fall; d++ {
			cs := pieceCells(op.pt, op.orient, op.row+d, op.col)
			if d > 0 && !g.canPlace(cs) {
				break
			}
			fp.cells = append(fp.cells, cs[:]...)
		}
		out = append(out, fp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].leaveAt.Before(out[j].leaveAt) })
	return out
}

// routeCosts is what the walk to every (orientation, column) costs the
// piece p — where it will stand when it moves (plan: fallenAt) — planned
// now and moved after the pause: one flood over the board
// with the forecast pieces on it prices everything the piece will reach at
// once (routeMove a move); what it will not reach until a crewmate's piece
// has gone is priced by the flood without that piece — the earliest-leaving
// first, a layer at a time — plus the wait for it to go (depPenalty a
// second). The planner adds the cost to the placement at that column
// (projection.penalty). Caller holds mu.
func (g *Game) routeCosts(p active, now time.Time, pause time.Duration) map[[2]int]float64 {
	execAt := now.Add(pause)
	fc := g.forecast(now, execAt)
	boxes := g.teammateBoxes()
	out := map[[2]int]float64{}
	for layer := 0; layer <= len(fc); layer++ {
		others := map[cell]int{}
		for _, fp := range fc[layer:] {
			for _, c := range fp.cells {
				others[c] = fp.seat
			}
		}
		wait := 0.0
		if layer > 0 {
			wait = max(fc[layer-1].leaveAt.Sub(execAt).Seconds(), 0)
		}
		s := g.flood(p, boxes, others, nil)
		for k, d := range s.reach() {
			if _, ok := out[k]; !ok {
				out[k] = routeMove*d + depPenalty*wait
			}
		}
		if len(s.blockers) == 0 {
			break // nothing of anyone's stood in the way: the rest is the stack's
		}
	}
	return out
}

// routeCostsNow is routeCosts for a piece that is blocked and planned
// again (replanBlocked): the board as it is this instant, every teammate's
// piece a wall — a placement the piece cannot walk to right now is no
// answer to being blocked, and ranks below every one it can (routeBlocked,
// by penalty's default). Caller holds mu.
func (g *Game) routeCostsNow(p active) map[[2]int]float64 {
	out := map[[2]int]float64{}
	for k, d := range g.flood(p, g.teammateBoxes(), g.othersAct, nil).reach() {
		out[k] = routeMove * d
	}
	return out
}

// keepOut is the cells our piece must not STOP in: the drop shafts — the
// landing's columns, from the top of the board down to the landing — of
// every planned claim with the right of way over ours. Their piece comes
// down there; ours may cross a shaft in a batch, and a wait is chosen
// outside them (waitingSpot). Generalises depColumns, which keeps out of
// the shafts of the claims our own landing depends on. Caller holds mu.
func (g *Game) keepOut() map[cell]bool {
	if g.coord == nil || g.myClaim == nil || g.myPrio == 0 {
		return nil
	}
	var keep map[cell]bool
	gr := g.toGridProjected()
	for _, c := range g.orderedClaims() {
		if c.Phase != phasePlanned || c.order() >= g.myPrio {
			continue
		}
		if op, ok := g.othersPiece[c.Seat]; !ok || op.pt != c.Type {
			continue
		}
		their, ok := g.projectClaim(gr, c)
		if !ok {
			continue
		}
		stampClaimed(gr, their)
		if keep == nil {
			keep = map[cell]bool{}
		}
		for _, x := range their {
			for r := 0; r < x.r; r++ {
				keep[cell{r, x.c}] = true
			}
		}
	}
	return keep
}

// ---- blocked: who waits, who goes, and for how long ------------------------

const (
	// blockPatienceSilent is how long a piece waits on a piece nobody has
	// claimed — a human's, a golang-mk1's — before it plans again among
	// what it can reach.
	blockPatienceSilent = 600 * time.Millisecond
	// maxBlockedPlans bounds the re-plans a piece gets for being blocked.
	maxBlockedPlans = 4
)

// blockPatience is how long the piece in play waits for the seats' pieces
// to get out of its way before it plans again among the placements it can
// walk to right now (replanBlocked). The right of way decides: against
// pieces whose claims were all made after ours, we hold our plan — they
// are the ones to step aside or plan again — for as long as a claim is
// waited on at all (depWaitTimeout). Against a claim made before ours we
// give it twice what it says it still needs, and a piece nobody has
// claimed gets blockPatienceSilent; within depWaitMin and depWaitTimeout.
// Two pieces in each other's way are thus never both the one that waits:
// the claims are in one order, and the later one moves. Whoever waits, it
// is for no longer than the piece has air for (capWait): the right of way
// is no use to a piece that has sunk to the stack. Caller holds mu.
func (g *Game) blockPatience(seats []int, now time.Time) time.Duration {
	pat := depWaitMin
	ours := g.myPrio != 0
	for _, seat := range seats {
		c, ok := g.claims[seat]
		op, holds := g.othersPiece[seat]
		if !ok || c.Phase != phasePlanned || !holds || op.pt != c.Type {
			ours = false
			pat = max(pat, blockPatienceSilent)
			continue
		}
		if c.order() < g.myPrio {
			ours = false
		}
		left := c.seenAt.Add(time.Duration(c.ETAms) * time.Millisecond).Sub(now)
		pat = max(pat, 2*left)
	}
	if ours && len(seats) > 0 {
		return g.capWait(depWaitTimeout)
	}
	return g.capWait(min(pat, depWaitTimeout))
}

// ownersUnder is the seats whose falling pieces the piece a would come to
// rest on, were it dropped where it stands — who a blocked drop waits on.
// Sorted. Caller holds mu.
func (g *Game) ownersUnder(a active) []int {
	dr := g.dropRowShared(a)
	seen := map[int]bool{}
	var out []int
	for _, c := range pieceCells(a.pt, a.orient, dr+1, a.col) {
		if seat, ok := g.othersAct[c]; ok && !seen[seat] {
			seen[seat] = true
			out = append(out, seat)
		}
	}
	sort.Ints(out)
	return out
}

// ---- the pieces to come, and the well ---------------------------------------

// maxNextClaims is how many of its NEXT pieces a claim says a placement
// for (claim.Next), and how many of a teammate's a reader takes into
// account: past the second, a tentative placement is a guess about a board
// nobody has seen.
const maxNextClaims = 2

// stampTentative puts the teammates' tentative placements for their NEXT
// pieces (claim.Next) on the projection's tent map — each dropped on the
// board the firm claims made, a seat's second on its first — weighted 1
// for the piece up next and half for the one after. A nudge (tentPenalty),
// never a wall: nothing yields to a tentative placement, and nothing waits
// for one. A seat whose claim is `locked` holds the first of them in its
// hands by now: this is where its fresh piece is most likely going, before
// it has said so. Caller holds mu.
func (g *Game) stampTentative(gr *grid, proj *projection) {
	var scratch *grid
	for _, c := range g.orderedClaims() {
		for i, n := range c.Next {
			if i >= maxNextClaims {
				break
			}
			if n.Type < 0 || n.Type > 6 || n.Rot < 0 || n.Rot > 3 || n.Piece <= c.Piece {
				break
			}
			if scratch == nil {
				scratch = gr.clone()
			}
			if !scratch.canPlace(pieceCells(n.Type, n.Rot, spawnRow, n.Col)) {
				break
			}
			weight := 1 / float64(i+1)
			for _, x := range pieceCells(n.Type, n.Rot, scratch.dropRow(n.Type, n.Rot, spawnRow, n.Col), n.Col) {
				if x.r < 0 || x.r >= scratch.h || x.c < 0 || x.c >= scratch.w {
					continue
				}
				proj.tent[x] = max(proj.tent[x], weight)
				scratch.set(x.r, x.c, gridClaimed)
			}
		}
	}
}

// wellWanted reports whether this agent keeps a well on this board: a
// coordinated one, by its --well setting — "on", or "auto" anywhere but a
// survival game, whose rising floor leaves no room to bank rows.
func (g *Game) wellWanted() bool {
	if g.coord == nil || g.a == nil || g.w < 8 {
		return false
	}
	switch g.a.well {
	case "on":
		return true
	case "auto":
		return g.survival == ""
	}
	return false
}

// wellColumn is the column the crew keeps open: the board's last. Every
// seat derives the same one from the board's width, and says it in its
// claims (claim.Well), so a crew knows it agrees.
func (g *Game) wellColumn() int { return g.w - 1 }

// wellFor says whether the plan made on gr keeps the well, where it is,
// and whether the stack beside it stands high: kept when this agent wants
// one (wellWanted); every teammate on the board has a claim up naming the
// same column — a crew with a human, a golang-mk1 or an agent that keeps
// no well plays as it always did; and the column is open to the sky (with
// something over it there is no well to keep, and the board is played
// plainly until it is dug out). High is a stack past half the playing
// rows: the well is kept all the same — played plainly, a deep well is the
// first thing Dellacherie's features cap — but an I piece then takes what
// rows there are (evaluateWell). Caller holds mu.
func (g *Game) wellFor(gr *grid) (col int, on, high bool) {
	if !g.wellWanted() {
		return 0, false, false
	}
	w := g.wellColumn()
	if w < 0 || w >= gr.w {
		return w, false, false
	}
	boxes := g.teammateBoxes()
	if len(boxes) == 0 {
		return w, false, false
	}
	for _, b := range boxes {
		c, ok := g.claims[b.seat]
		if !ok || c.Well == nil || *c.Well != w {
			return w, false, false
		}
	}
	return w, wellOpen(gr, w), wellHigh(gr, w)
}

// wellOpen reports whether nothing sits over an empty cell of the column.
func wellOpen(gr *grid, w int) bool {
	covered := false
	for r := 0; r < gr.h; r++ {
		if gr.filled(r, w) {
			covered = true
		} else if covered {
			return false
		}
	}
	return true
}

// wellHigh reports whether the stack beside the column stands higher than
// half the playing rows.
func wellHigh(gr *grid, w int) bool {
	bit := uint64(1) << uint(w)
	for r := 0; r < gr.h; r++ {
		if gr.rows[r]&^bit != 0 {
			return gr.h-r > (gr.h-headroom)/2
		}
	}
	return false
}

// wellReady is how many rows stand full but for the well's column, from
// the well's floor up: what an I piece dropped down it would clear.
func wellReady(gr *grid, well int) int {
	if well < 0 || well >= gr.w {
		return 0
	}
	bit := uint64(1) << uint(well)
	n := 0
	for r := gr.h - 1; r >= 0; r-- {
		if gr.rows[r]&bit != 0 {
			continue // the well's floor, or below it
		}
		if gr.rows[r]|bit != gr.full {
			break
		}
		n++
	}
	return n
}
