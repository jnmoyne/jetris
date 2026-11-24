package main

import (
	"context"
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
