package main

import (
	"context"
	mrand "math/rand/v2"
	"testing"
	"time"
)

// wedgedT is a T piece standing in orientation 3 in a slot none of its other
// orientations fit in: the cell to its right is filled, which is in the way
// of orientations 0, 1 and 2 at that anchor alike.
func wedgedT(g *Game) active {
	p := active{2, 3, 10, 4}
	g.locked[cell{p.row + 1, p.col + 2}] = wireCell{O: true, T: 1}
	return p
}

// A piece planned again where it stands has turned since its spawn: its
// placements are counted from the orientation it is in. Counted from the
// spawn orientation, a piece wedged in another one has none at all.
func TestEnumerateFromTheOrientationThePieceStandsIn(t *testing.T) {
	g := newGame(&Agent{name: "me", stopCh: make(chan struct{})}, "g", 0)
	g.w, g.h = 10, 24
	p := wedgedT(g)
	gr := g.toGrid()
	if !gr.canPlace(pieceCells(p.pt, p.orient, p.row, p.col)) {
		t.Fatal("the piece does not fit where the test stands it")
	}
	for o := 0; o < 3; o++ {
		if gr.canPlace(pieceCells(p.pt, o, p.row, p.col)) {
			t.Fatalf("orientation %d fits: the piece is not wedged", o)
		}
	}
	if got := enumerate(gr, p.pt, p.row, p.col); len(got) != 0 {
		t.Fatalf("from the spawn orientation: %d placement(s), want none (the test's premise)", len(got))
	}
	got := enumerateFrom(nil, gr, p.pt, p.orient, p.row, p.col)
	if len(got) == 0 {
		t.Fatal("from the orientation the piece stands in: no placement at all")
	}
	here := false
	for _, pl := range got {
		if pl.orient != p.orient {
			t.Errorf("placement %+v: an orientation the piece cannot turn to", pl)
		}
		here = here || pl.col == p.col
	}
	if !here {
		t.Error("the column the piece stands in is not among its placements")
	}
	// At its spawn a piece stands in orientation 0, and the two agree.
	fresh := newGrid(24, 10)
	for pt := 0; pt < 7; pt++ {
		a, b := enumerate(fresh, pt, spawnRow, 3), enumerateFrom(nil, fresh, pt, 0, spawnRow, 3)
		if len(a) != len(b) {
			t.Fatalf("piece %d: %d placements from the spawn, %d from orientation 0", pt, len(a), len(b))
		}
		for i := range a {
			if a[i].orient != b[i].orient || a[i].col != b[i].col || a[i].dropRow != b[i].dropRow {
				t.Fatalf("piece %d placement %d: %+v against %+v", pt, i, a[i], b[i])
			}
		}
	}
}

// A re-plan of a wedged piece finds somewhere to put it. With nothing to
// choose from, plan reports a top-out, and playPieces ends the game on it —
// for the whole crew, on a cooperative board, however low the stack.
func TestReplanOfAWedgedPieceIsNotATopOut(t *testing.T) {
	g := newGame(&Agent{name: "me", stopCh: make(chan struct{})}, "g", 0)
	g.w, g.h = 10, 24
	p := wedgedT(g)
	pl, _, ok := g.plan(context.Background(), p, 0)
	if !ok {
		t.Fatal("no placement for a piece standing on a nearly empty board: read as a top-out")
	}
	if pl.orient != p.orient {
		t.Errorf("planned orientation %d, want %d (the only one the piece can be in there)", pl.orient, p.orient)
	}
}

// At a level whose rows come faster than the agent's turns do (level 12 on:
// 28 ms a row against hard's 30 ms move step) every turn owes a fall. The
// walk follows that fall in the same turn; left to a turn that owes none, it
// never happens: the piece comes down its spawn columns onto the stack, and
// whatever stands between it and its column there — a wall, here — keeps it
// from its plan for good.
func TestTheWalkFollowsGravityInTheSameTurn(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	g.a.tn = difficultyTuning("hard")
	g.totalLines = (maxLevel - 1) * linesPerLevel
	if step := g.gravityNow(); step >= g.a.tn.moveDelay {
		t.Fatalf("gravity %s a row is no faster than the %s move step: the test's premise", step, g.a.tn.moveDelay)
	}
	g.mu.Lock()
	spawnT, placed, topped := g.spawn(ctx)
	if !placed || topped {
		g.mu.Unlock()
		t.Fatalf("spawn: placed %v topped %v", placed, topped)
	}
	p := *g.piece
	// A wall four rows high on the floor, a column clear of the piece's
	// widest, and the plan beyond it: an easy walk up where the piece
	// spawns, none along the floor.
	wall := p.col + 4
	for r := g.h - 4; r < g.h; r++ {
		at := cell{r, wall}
		wc := wireCell{O: true, T: 1}
		g.foldCell(at, wc, publishCell(t, ctx, g, at, &wc))
	}
	to := active{p.pt, p.orient, p.row, wall + 1}
	if !g.canPlace(pieceCells(to.pt, to.orient, to.row, to.col)) {
		g.mu.Unlock()
		t.Fatalf("the plan's column %d is off the %d-column board", to.col, g.w)
	}
	to.row = g.dropRowShared(to)
	plan := placement{orient: to.orient, col: to.col, dropRow: to.row, dest: pieceCells(to.pt, to.orient, to.row, to.col)}
	g.nextGravity = time.Now() // the first turn owes a row already
	g.mu.Unlock()

	g.execute(ctx, plan, spawnT)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.replan != replanNone {
		t.Fatalf("the piece was not driven to its plan: re-plan %q asked, piece %+v", g.replan, g.piece)
	}
	if g.piece != nil || g.pieceIdx != 1 {
		t.Fatalf("after execute: piece %+v, index %d — not locked", g.piece, g.pieceIdx)
	}
	for _, c := range plan.dest {
		if !g.locked[c].O {
			t.Errorf("planned cell %v not settled", c)
		}
	}
	if g.st.standLocks != 0 {
		t.Errorf("%d lock(s) where the piece stood, away from its plan", g.st.standLocks)
	}
	assertConverged(t, ctx, g)
}

// atLevel sets a cooperative crew's level.
func atLevel(g *Game, level int) {
	g.mode = modeCooperative
	g.totalLines = (level - 1) * linesPerLevel
}

// Gravity does not wait for the think pause: the pause is the difficulty's
// while the level leaves it that long, and no longer than a few rows take
// once it does not.
func TestThinkPauseFollowsGravity(t *testing.T) {
	g := coordTestGame()
	g.a.tn = difficultyTuning("hard")
	atLevel(g, 5)
	if got := g.thinkPause(); got != g.a.tn.pieceDelay {
		t.Errorf("level 5: pause %v, want the difficulty's %v", got, g.a.tn.pieceDelay)
	}
	atLevel(g, maxLevel)
	want := pauseRows * gravityInterval(maxLevel)
	if got := g.thinkPause(); got != want || got >= g.a.tn.pieceDelay/2 {
		t.Errorf("level %d: pause %v, want %v (%d rows of gravity)", maxLevel, got, want, pauseRows)
	}
	g.a.rng = mrand.New(mrand.NewPCG(1, 2))
	for i := 0; i < 50; i++ {
		if got := g.thinkPause(); got < want*2/3 || got > want*4/3 {
			t.Fatalf("level %d, jittered: pause %v outside a third of %v", maxLevel, got, want)
		}
	}
}

// The walk starts where gravity will have taken the piece by the time it
// moves, and the plan is priced from there: a column the stack walls off
// from that depth is out of reach, however open the spawn row is.
func TestThePlanIsPricedFromWhereThePieceWillBe(t *testing.T) {
	g := coordTestGame()
	g.a.tn = difficultyTuning("hard")
	atLevel(g, maxLevel)
	now := time.Now()
	p := active{1 /*O*/, 0, spawnRow, g.spawnC}
	wall := g.spawnC + 4
	lockColumn(g, wall, 8) // a wall from row 8 down, a column clear of the piece
	far := [2]int{0, wall + 1}

	if got := g.fallenAt(p, now); got != p {
		t.Errorf("no gravity clock yet: the piece at %+v, want it where it stands", got)
	}
	g.nextGravity = now
	step := g.gravityNow()
	from := g.fallenAt(p, now.Add(9*step+step/2))
	if from.row != p.row+10 || from.col != p.col || from.orient != p.orient {
		t.Fatalf("ten rows owed: the piece at %+v, want ten rows under %+v", from, p)
	}
	g.gravityDebt = 2
	if got := g.fallenAt(p, now.Add(-time.Second)); got.row != p.row+2 {
		t.Errorf("two rows of debt, no deadline passed: the piece at row %d, want %d", got.row, p.row+2)
	}
	g.gravityDebt = 0
	floor := g.fallenAt(p, now.Add(time.Minute))
	if g.canPlace(pieceCells(floor.pt, floor.orient, floor.row+1, floor.col)) || !g.canPlace(pieceCells(floor.pt, floor.orient, floor.row, floor.col)) {
		t.Errorf("a minute of gravity: the piece at row %d, not on the floor", floor.row)
	}

	if _, ok := g.routeCosts(p, now, 0)[far]; !ok {
		t.Fatal("from the spawn row the column beyond the wall is not reached: the test's premise")
	}
	costs := g.routeCosts(from, now, 0)
	if _, ok := costs[far]; ok {
		t.Error("from under the wall's top the column beyond it is priced as a walk")
	}
	if _, ok := costs[[2]int{0, g.spawnC - 2}]; !ok {
		t.Error("from under the wall's top a column on the piece's own side is not reached")
	}
	proj := &projection{route: costs, routeNow: from != p}
	if got := proj.penalty(placement{orient: far[0], col: far[1]}); got < routeBlocked {
		t.Errorf("the placement beyond the wall costs %.1f, want at least %.0f: it is out of reach", got, routeBlocked)
	}
}

// A wait goes on while gravity does. It ends while the piece still has air
// to walk in — a claim's estimate or the right of way is no use to a piece
// on the stack — and is what it always was where the level leaves the time,
// or the piece stands on something.
func TestWaitsEndWhileThePieceHasAir(t *testing.T) {
	g := coordTestGame()
	g.a.tn = difficultyTuning("hard")
	now := time.Now()
	g.myPrio = 20
	p := active{1 /*O*/, 0, spawnRow, g.spawnC}
	g.piece = &p
	peerPiece(g, 1, active{0 /*I*/, 0, spawnRow, 7})
	g.claims[1] = claim{V: claimVersion, Seat: 1, Piece: 3, Type: 0, Rot: 0, Col: 9, Phase: phasePlanned, ETAms: 400, rev: 30}
	g.deps = []depWait{{seat: 1, piece: 3, rev: 30, cells: cellsOf(pieceCells(0, 0, 22, 9))}}

	atLevel(g, 5)
	if got := g.depTimeout(); got != 800*time.Millisecond {
		t.Errorf("level 5: dependency wait %v, want twice the 400 ms estimate", got)
	}
	if got := g.blockPatience([]int{1}, now); got != depWaitTimeout {
		t.Errorf("level 5, a later claim: patience %v, want %v", got, depWaitTimeout)
	}

	atLevel(g, 14)
	_, fell, _ := g.fallRows(p, g.height())
	air := time.Duration(fell) * g.gravityNow()
	if fell < 10 || air >= depWaitMin {
		t.Fatalf("level 14: %d rows of air, %v: not shorter than the shortest wait (the test's premise)", fell, air)
	}
	if got := g.depTimeout(); got != air {
		t.Errorf("level 14: dependency wait %v, want the %v of air", got, air)
	}
	if got := g.blockPatience([]int{1}, now); got != air {
		t.Errorf("level 14, a later claim: patience %v, want the %v of air", got, air)
	}
	delete(g.claims, 1)
	if got := g.blockPatience([]int{1}, now); got != air {
		t.Errorf("level 14, a silent piece: patience %v, want the %v of air", got, air)
	}

	// Standing on the floor, the piece is not sinking: the wait is the claim's.
	g.nextGravity = now
	rest := g.fallenAt(p, now.Add(time.Minute))
	g.piece = &rest
	if got := g.blockPatience([]int{1}, now); got != blockPatienceSilent {
		t.Errorf("level 14, at rest: patience %v, want %v", got, blockPatienceSilent)
	}
}

// The lock delay is the time a crewmate's piece needs to fall out of the
// way: half a second at most, and less at a level that drops it sooner.
func TestLockDelayFollowsGravity(t *testing.T) {
	g := coordTestGame()
	g.a.tn = difficultyTuning("hard")
	atLevel(g, 5)
	if got := g.lockDelayNow(); got != lockDelay {
		t.Errorf("level 5: lock delay %v, want %v", got, lockDelay)
	}
	atLevel(g, 13)
	d13 := g.lockDelayNow()
	atLevel(g, maxLevel)
	d15 := g.lockDelayNow()
	if want := time.Duration(g.height()) * gravityInterval(maxLevel); d15 != want {
		t.Errorf("level %d: lock delay %v, want the %v of a fall down the board", maxLevel, d15, want)
	}
	if !(4*g.a.tn.moveDelay <= d15 && d15 < d13 && d13 < lockDelay) {
		t.Errorf("lock delay %v at level 13, %v at level %d: want them falling from %v, never under four move steps", d13, d15, maxLevel, lockDelay)
	}
}
