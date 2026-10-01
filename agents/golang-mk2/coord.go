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
	replanBBReset                  // the blackboard watch was re-established on a recreated bucket
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
	case replanBBReset:
		return "bbreset"
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
	// within it, and the walk resumes. golang-mk1 locked at once.
	lockDelay = 500 * time.Millisecond
)

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
	travelCost  = 0.15 // per column between our spawn point and the placement
	crossCost   = 4.0  // per imminent teammate spawn box the walk crosses
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
// listing still holds but ours — by revision, the order everyone sees.
// Caller holds mu.
func (g *Game) orderedClaims() []claim {
	var out []claim
	for seat, c := range g.claims {
		if seat == g.idx || c.Seat != seat || !g.seatPresentLocked(seat) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rev < out[j].rev })
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
		ownSpawnC: g.spawnC, boxes: g.teammateBoxes()}
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
			if rev, ok := g.ignoredClaims[c.Seat]; ok && rev == c.rev {
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
// every claim made before ours (rev < myRev), in revision order, each
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
func (g *Game) classify(p active, cand placement, myRev uint64) (kind conflictKind, deps []depWait, taken map[cell]bool) {
	acc := g.toGridProjected()
	dest := cellSet(cand.dest)
	taken = map[cell]bool{}
	overlap := false
	for _, c := range g.orderedClaims() {
		if c.Phase == phaseLocked || c.rev >= myRev {
			continue
		}
		if rev, ok := g.ignoredClaims[c.Seat]; ok && rev == c.rev {
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
// depWaitTimeout. Caller holds mu.
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
	return min(limit, depWaitTimeout)
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
				g.ignoredClaims[g.deps[i].seat] = g.deps[i].rev
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
// two yield every time.
func (g *Game) thinkPause() time.Duration {
	d := g.a.tn.pieceDelay
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
