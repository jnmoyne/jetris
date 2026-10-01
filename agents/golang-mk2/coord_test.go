package main

import (
	"testing"
	"time"
)

// coordTestGame is a crew's board of two seats, coordinated, with no server.
func coordTestGame() *Game {
	g := pathTestGame()
	g.coord = &coordinator{board: "crew", key: "g.crew.0", watch: "g.crew.*"}
	return g
}

func fillRow(g *Game, r int, except ...int) {
	skip := map[int]bool{}
	for _, c := range except {
		skip[c] = true
	}
	for c := 0; c < g.w; c++ {
		if !skip[c] {
			g.locked[cell{r, c}] = wireCell{O: true, T: 1}
		}
	}
}

func planned(seat, piece, typ, rot, col int, rev uint64) claim {
	return claim{V: claimVersion, Seat: seat, Piece: piece, Type: typ, Rot: rot, Col: col, Phase: phasePlanned, ETAms: 150, rev: rev}
}

// The projection: a planned claim lands where its column and orientation
// drop on the board as it stands, a locked claim's cells count once the
// board lacks them, an older piece's claim is nothing, and a silent
// teammate's piece is dropped straight down with a second's penalty.
func TestBuildProjection(t *testing.T) {
	g := coordTestGame()
	fillRow(g, 23, 5, 6, 7, 8)
	// Seat 1 claims an I flat in the gap: its own cells say row 20 (a
	// collapse ago); the projection re-drops it to row 23.
	c := planned(1, 4, 0, 0, 5, 10)
	c.Cells = [4][2]int{{20, 5}, {20, 6}, {20, 7}, {20, 8}}
	g.claims[1] = c
	g.othersPiece[1] = active{0, 0, 2, 7}
	gr, proj := g.buildProjection()
	for col := 5; col <= 8; col++ {
		if gr.at(23, col) != gridClaimed || proj.claimed[cell{23, col}] != 1 {
			t.Errorf("gap cell (23,%d): grid %d, claimed by %d", col, gr.at(23, col), proj.claimed[cell{23, col}])
		}
	}
	if gr.at(20, 5) != 0 || proj.depETA[1] != 150 || proj.pieces[1] != 4 {
		t.Errorf("stale cells stamped (%d), eta %d, piece %d", gr.at(20, 5), proj.depETA[1], proj.pieces[1])
	}
	// The seat's board shows another type than the claim's: an old claim.
	g.othersPiece[1] = active{2, 0, 2, 7}
	gr, proj = g.buildProjection()
	if gr.at(23, 5) != 0 || len(proj.claimed) != 0 {
		t.Errorf("an older piece's claim was projected: %v", proj.claimed)
	}
	// …and with no claim for it, the T at (2,7) is a silent teammate's:
	// its straight drop is a soft nudge — on the map, not in the grid.
	delete(g.claims, 1)
	stack := g.toGridProjected()
	gr, proj = g.buildProjection()
	ghost := pieceCells(2, 0, stack.dropRow(2, 0, 2, 7), 7)
	for _, x := range ghost {
		if gr.at(x.r, x.c) != 0 || proj.soft[x] != 1 {
			t.Errorf("ghost cell %v: grid %d, soft for %d", x, gr.at(x.r, x.c), proj.soft[x])
		}
	}
	if len(proj.claimed) != 0 || len(proj.soft) != 4 {
		t.Errorf("ghost: claimed %v, soft %v", proj.claimed, proj.soft)
	}
	// A locked claim adds nothing — its cells reach the board off the game
	// stream — and marks the seat heard from: no ghost for its piece, and
	// nothing "rests on a claim" by resting where it locked (a claimed
	// stamp with no owner once read as seat 0's, and seat 0 waited on
	// itself).
	lc := planned(1, 4, 0, 0, 5, 11)
	lc.Phase = phaseLocked
	lc.Cells = [4][2]int{{23, 5}, {23, 6}, {23, 7}, {23, 8}}
	g.claims[1] = lc
	gr, proj = g.buildProjection()
	if gr.at(23, 5) != 0 || len(proj.claimed) != 0 || len(proj.depETA) != 0 || len(proj.soft) != 0 {
		t.Errorf("locked claim: grid %d, claimed %v, eta %v, soft %v", gr.at(23, 5), proj.claimed, proj.depETA, proj.soft)
	}
	delete(g.othersPiece, 1)
	for col := 5; col <= 8; col++ {
		g.locked[cell{23, col}] = wireCell{O: true, T: 0, Pi: 1}
	}
	gr, proj = g.buildProjection()
	onIt := placement{orient: 0, col: 5, dropRow: 21, dest: pieceCells(0, 0, 21, 5)}
	if seats := supportSeats(gr, proj, onIt); len(seats) != 0 {
		t.Errorf("resting on a landed lock waits on %v", seats)
	}
	if deps := g.mergeDeps(placement{waitOn: []int{0, 1}}, proj, nil); len(deps) != 1 || deps[0].seat != 1 {
		t.Errorf("deps %+v: our own seat must never be one", deps)
	}
	for col := 5; col <= 8; col++ {
		g.locked[cell{23, col}] = wireCell{O: true, T: 0, Pi: 1}
	}
	gr, _ = g.buildProjection()
	if gr.at(23, 5) != 1 {
		t.Errorf("landed locked claim re-stamped: %d", gr.at(23, 5))
	}
}

// classify: an earlier landing meeting ours is an overlap; one that holds
// ours up, or that ours would displace, is a dependency; a claim made
// after ours, a locked one, and an old one are none.
func TestClassify(t *testing.T) {
	g := coordTestGame()
	fillRow(g, 23, 5, 6, 7, 8)
	me := active{0, 0, spawnRow, g.spawnC}
	stack := g.toGridProjected()
	landing := func(orient, col int) placement {
		dr := stack.dropRow(0, orient, spawnRow, col)
		return placement{orient: orient, col: col, dropRow: dr, dest: pieceCells(0, orient, dr, col)}
	}
	gap := landing(0, 5)
	onTop := gap
	onTop.dropRow--
	onTop.dest = pieceCells(0, 0, onTop.dropRow, 5)
	aside := landing(0, 9)
	theirs := planned(1, 0, 0, 0, 5, 10) // seat 1's I, flat in the gap, claimed at revision 10
	g.claims[1] = theirs
	if kind, _, _ := g.classify(me, gap, 20); kind != conflictOverlap {
		t.Errorf("the same gap: %v, want an overlap", kind)
	}
	kind, deps, _ := g.classify(me, onTop, 20)
	if kind != conflictDependency || len(deps) != 1 || deps[0].seat != 1 || deps[0].rev != 10 || len(deps[0].cells) != 4 {
		t.Errorf("resting on their landing: %v %+v, want a dependency on seat 1", kind, deps)
	}
	if kind, deps, _ := g.classify(me, aside, 20); kind != noConflict || len(deps) != 0 {
		t.Errorf("beside their landing: %v %+v, want no conflict", kind, deps)
	}
	// Interference: our landing would change where theirs lands — a T
	// pointing down, resting on column 4 with an arm over column 5 above
	// the gap, would stop their flat I short of it.
	meT := active{2, 0, spawnRow, g.spawnC}
	dr := stack.dropRow(2, 2, spawnRow, 3)
	overhang := placement{orient: 2, col: 3, dropRow: dr, dest: pieceCells(2, 2, dr, 3)}
	if kind, deps, _ := g.classify(meT, overhang, 20); kind != conflictDependency || len(deps) != 1 {
		t.Errorf("displacing their landing: %v %+v (dest %v), want a dependency", kind, deps, overhang.dest)
	}
	// A claim made after ours yields to us; a locked one is a board fact;
	// an old one (their board shows another type) is nothing.
	if kind, _, _ := g.classify(me, gap, 5); kind != noConflict {
		t.Errorf("a later claim: %v, want no conflict", kind)
	}
	locked := theirs
	locked.Phase = phaseLocked
	locked.Cells = [4][2]int{{23, 5}, {23, 6}, {23, 7}, {23, 8}}
	g.claims[1] = locked
	if kind, deps, _ := g.classify(me, onTop, 20); kind != noConflict || len(deps) != 0 {
		t.Errorf("a locked claim: %v %+v, want no conflict", kind, deps)
	}
	g.claims[1] = theirs
	g.othersPiece[1] = active{2, 0, 2, 7}
	if kind, _, _ := g.classify(me, gap, 20); kind != noConflict {
		t.Errorf("an old claim: %v, want no conflict", kind)
	}
	// A LOCKED claim is no claim here, whatever its revision: its cells
	// reach the board off the game stream, and checkPlan finds a landing
	// they have taken.
	delete(g.othersPiece, 1)
	late := locked
	late.rev = 25
	g.claims[1] = late
	if kind, _, taken := g.classify(me, gap, 20); kind != noConflict || len(taken) != 0 {
		t.Errorf("a later lock: %v (taken %v), want no conflict", kind, taken)
	}
	// Our own key, echoed, is no teammate's claim.
	delete(g.othersPiece, 1)
	g.claims[0] = planned(0, 0, 0, 0, 5, 10)
	delete(g.claims, 1)
	if kind, _, _ := g.classify(me, gap, 20); kind != noConflict {
		t.Errorf("our own echo: %v, want no conflict", kind)
	}
}

// The plan on the live board: stale once the stack has taken a landing
// cell or nothing holds the landing up (a collapse), a wait while an
// earlier claim will.
func TestCheckPlanAfterACollapse(t *testing.T) {
	g := coordTestGame()
	fillRow(g, 23)
	pl := placement{orient: 0, col: 5, dropRow: 21, dest: pieceCells(0, 0, 21, 5)} // an I flat on the bottom row
	if st := g.checkPlan(pl); st != planOK {
		t.Fatalf("resting on the stack: %v, want ok", st)
	}
	for c := 0; c < g.w; c++ {
		delete(g.locked, cell{22, c})
		delete(g.locked, cell{23, c})
	}
	if st := g.checkPlan(pl); st != planStale {
		t.Errorf("the rows under it gone: %v, want stale", st)
	}
	g.deps = []depWait{{seat: 1, piece: 3, rev: 10, cells: cellsOf(pieceCells(0, 0, 22, 5))}}
	g.claims[1] = planned(1, 3, 0, 0, 5, 10)
	if st := g.checkPlan(pl); st != planWaitDep {
		t.Errorf("an earlier claim will hold it up: %v, want a wait", st)
	}
	g.locked[cell{22, 5}] = wireCell{O: true, T: 1}
	if st := g.checkPlan(pl); st != planStale {
		t.Errorf("a landing cell taken: %v, want stale", st)
	}
}

// A wait ends on a newer claim of the seat's (their lock or re-plan), on
// no claim at all, on the landing settling — a silent teammate's once
// their piece has left the spot it was dropped from.
func TestDepResolved(t *testing.T) {
	g := coordTestGame()
	fillRow(g, 23, 5, 6, 7, 8)
	d := depWait{seat: 1, piece: 3, rev: 10, cells: cellsOf(pieceCells(0, 0, 22, 5))}
	g.claims[1] = planned(1, 3, 0, 0, 5, 10)
	if g.depResolved(&d) {
		t.Error("resolved while their claim stands")
	}
	// A newer claim of theirs for the SAME landing keeps the wait, under
	// the new revision; one for another landing ends it.
	g.claims[1] = planned(1, 3, 0, 0, 5, 12)
	if g.depResolved(&d) || d.rev != 12 {
		t.Errorf("a re-plan to the same landing ended the wait (rev now %d)", d.rev)
	}
	g.claims[1] = planned(1, 3, 0, 0, 9, 14)
	if !g.depResolved(&d) {
		t.Error("not resolved by a newer claim of theirs for another landing")
	}
	g.claims[1] = planned(1, 3, 0, 0, 5, 14)
	d.rev = 14
	for _, x := range d.cells {
		g.locked[x] = wireCell{O: true, T: 0, Pi: 1}
	}
	if !g.depResolved(&d) {
		t.Error("not resolved by the landing settling")
	}
	delete(g.claims, 1)
	if !g.depResolved(&depWait{seat: 1, piece: 3, rev: 10}) {
		t.Error("not resolved by their claim's absence")
	}
	g.deps = []depWait{{seat: 1, piece: 3, rev: 10}}
	if g.depsPending() {
		t.Error("deps pending with their claim gone")
	}
	// The wait's account: it times out after twice the claim's estimate
	// (within depWaitMin and depWaitTimeout), and the claim waited on is
	// then ignored by the re-plan.
	g.claims[1] = claim{V: claimVersion, Seat: 1, Piece: 3, Type: 0, Rot: 0, Col: 5, Phase: phasePlanned, ETAms: 400, rev: 20}
	g.deps = []depWait{{seat: 1, piece: 3, rev: 20, cells: cellsOf(pieceCells(0, 0, 22, 5))}}
	for _, x := range g.deps[0].cells {
		delete(g.locked, x)
	}
	if got := g.depTimeout(); got != 800*time.Millisecond {
		t.Errorf("timeout %v, want twice the 400 ms estimate", got)
	}
	now := time.Now()
	if g.awaitDeps(now) || g.st.depWaits != 1 {
		t.Errorf("first turn: timed out, or not counted (%d)", g.st.depWaits)
	}
	if g.awaitDeps(now.Add(400 * time.Millisecond)) {
		t.Error("timed out early")
	}
	if !g.awaitDeps(now.Add(801*time.Millisecond)) || g.st.depWaitMs < 800 {
		t.Errorf("did not time out, or the account is short (%d ms)", g.st.depWaitMs)
	}
	if g.ignoredClaims[1] != 20 {
		t.Errorf("the claim waited on is not ignored: %v", g.ignoredClaims)
	}
	if _, proj := g.buildProjection(); len(proj.claimed) != 0 {
		t.Errorf("an ignored claim was projected: %v", proj.claimed)
	}
}

// The placement penalties: a wait in proportion to the claim's time, the
// walk per column, every imminent spawn box crossed; and the seats a
// placement rests on or completes a row with.
func TestPenaltyAndSupportSeats(t *testing.T) {
	g := coordTestGame()
	fillRow(g, 23, 5, 6, 7, 8)
	g.claims[1] = planned(1, 4, 0, 0, 5, 10)
	g.othersPiece[1] = active{0, 0, 2, 7}
	gr, proj := g.buildProjection()
	onTop := placement{orient: 0, col: 5, dropRow: 21, dest: pieceCells(0, 0, 21, 5)}
	if seats := supportSeats(gr, proj, onTop); len(seats) != 1 || seats[0] != 1 {
		t.Errorf("resting on seat 1's landing: %v", seats)
	}
	completing := placement{orient: 1, col: -1, dropRow: 20, dest: [4]cell{{20, 0}, {21, 0}, {22, 0}, {23, 0}}}
	delete(g.locked, cell{23, 0})
	gr, proj = g.buildProjection()
	if seats := supportSeats(gr, proj, completing); len(seats) != 1 || seats[0] != 1 {
		t.Errorf("completing a row with seat 1's landing: %v", seats)
	}
	aside := placement{orient: 0, col: 9, dropRow: 21, dest: pieceCells(0, 0, 21, 9)}
	if seats := supportSeats(gr, proj, aside); len(seats) != 0 {
		t.Errorf("beside it: %v, want none", seats)
	}
	onTop.waitOn = []int{1}
	want := depPenalty*0.15 + travelCost*float64(abs(5-g.spawnC))
	if p := proj.penalty(onTop); p < want-1e-9 || p > want+1e-9 {
		t.Errorf("penalty %.3f, want %.3f (wait %.3f + travel)", p, want, depPenalty*0.15)
	}
	// Crossing seat 1's spawn box while their spawn is imminent.
	delete(g.othersPiece, 1)
	delete(g.claims, 1)
	_, proj = g.buildProjection()
	far := placement{orient: 0, col: 10, dropRow: 21, dest: pieceCells(0, 0, 21, 10)}
	if p := proj.penalty(far); p < crossCost {
		t.Errorf("penalty %.3f crossing an imminent box, want at least %.1f", p, crossCost)
	}
	// A silent teammate's straight drop: a placement in it pays per cell.
	g.othersPiece[1] = active{0, 0, 2, 7}
	_, proj = g.buildProjection()
	under := placement{orient: 0, col: 7, dropRow: 21, dest: pieceCells(0, 0, 21, 7)}
	if p := proj.penalty(under); p < 4*softPenalty {
		t.Errorf("penalty %.3f in a silent teammate's drop, want at least %.1f", p, 4*softPenalty)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// The teammates' spawn boxes among the seats present, on a crew's board of
// two and of three, at the default step (shoulder to shoulder).
func TestTeammateBoxes(t *testing.T) {
	g := coordTestGame()
	boxes := g.teammateBoxes()
	if len(boxes) != 1 || boxes[0].seat != 1 || boxes[0].c0 != g.spawnColumnFor(1, []int{0, 1}) || boxes[0].c1 != boxes[0].c0+3 || !boxes[0].imminent {
		t.Errorf("two seats: %+v", boxes)
	}
	if mine := g.spawnC; boxes[0].c0 == mine || (boxes[0].c0 < mine+4 && boxes[0].c1 >= mine) {
		t.Errorf("box %+v overlaps our own (%d-%d)", boxes[0], mine, mine+3)
	}
	g = sweepTestGame(3)
	g.roster = []playerSummary{{PlayerID: "me", Seat: 0}, {PlayerID: "p1", Seat: 1}, {PlayerID: "p2", Seat: 2}}
	g.spawnC = g.spawnColumn(g.presentSlots(g.roster))
	g.othersPiece[2] = active{2, 0, 2, 11}
	boxes = g.teammateBoxes()
	if len(boxes) != 2 || boxes[0].seat != 1 || boxes[1].seat != 2 || !boxes[0].imminent || boxes[1].imminent {
		t.Errorf("three seats: %+v", boxes)
	}
	if boxes[0].c0 != 7 || boxes[1].c0 != 11 {
		t.Errorf("three seats at step 4: boxes at %d and %d, want 7 and 11", boxes[0].c0, boxes[1].c0)
	}
}

// cellsOf is a piece's cells as a slice.
func cellsOf(cs [4]cell) []cell { return cs[:] }

// A piece waiting on a claim keeps out of the columns that claim's landing
// falls through: it steps aside to the nearest clear spot, and stays put
// when it is clear already. The projection never stamps a claim over the
// piece's own cells.
func TestDepAsideAndOwnCellsStayClear(t *testing.T) {
	g := coordTestGame()
	fillRow(g, 23, 5, 6, 7, 8)
	g.claims[1] = planned(1, 4, 0, 0, 5, 10)
	g.othersPiece[1] = active{0, 0, 2, 7}
	_, proj := g.buildProjection()
	// We hold an I flat over the gap: our landing rests on theirs.
	g.piece = &active{0, 0, spawnRow, 5}
	g.deps = []depWait{{seat: 1, piece: 4, rev: 10}}
	for x, s := range proj.claimed {
		if s == 1 {
			g.deps[0].cells = append(g.deps[0].cells, x)
		}
	}
	if cols := g.depColumns(); len(cols) != 4 || !cols[5] || !cols[8] {
		t.Fatalf("dependency columns: %v", cols)
	}
	aside, moved := g.depAside(*g.piece)
	if !moved || columnsMeet(aside, g.depColumns()) {
		t.Errorf("step aside: %+v (moved %v), still in the way", aside, moved)
	}
	if aside.col != 1 && aside.col != 9 {
		t.Errorf("step aside to col %d, want the nearest clear spot (1 or 9)", aside.col)
	}
	g.piece = &active{0, 0, spawnRow, 0}
	if _, moved := g.depAside(*g.piece); moved {
		t.Error("moved a piece already clear of the columns")
	}
	// Our own cells: a claim landing where we stand leaves the grid empty there.
	g.piece = &active{0, 0, 22, 5} // sitting in the gap ourselves
	gr, proj := g.buildProjection()
	for _, c := range pieceCells(0, 0, 22, 5) {
		if gr.at(c.r, c.c) != 0 {
			t.Errorf("our cell %v stamped as %d", c, gr.at(c.r, c.c))
		}
		if _, ok := proj.claimed[c]; ok {
			t.Errorf("our cell %v claimed", c)
		}
	}
}
