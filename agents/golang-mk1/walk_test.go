package main

import "testing"

// TestWalkWholePath: the walk to a plan is the whole path — the rotations,
// then the shifts — when every step is free, so it goes out as one batch;
// it stops before a step the stack blocks, and at the very first step it
// reports the stack (not a transient piece) so the caller locks.
func TestWalkWholePath(t *testing.T) {
	g := newGame(&Agent{name: "me"}, "walk", 0)
	g.mode, g.w = modeCooperative, 10
	p := active{pt: 2, orient: 0, row: 2, col: 4} // a T at its spawn

	// Free board: the plan is reached in one walk.
	plan := placement{orient: 1, col: 7}
	to, transient := g.walk(p, plan)
	if to.orient != 1 || to.col != 7 || to.row != p.row || transient {
		t.Fatalf("free walk = %+v (transient %v), want orient 1 col 7 on row %d", to, transient, p.row)
	}

	// A locked column in the way: the walk stops just before it, and the
	// blocker is the stack.
	for r := 0; r < g.height(); r++ {
		g.locked[cell{r: r, c: 7}] = g.lockedPayload(0)
	}
	to, transient = g.walk(p, plan)
	if to.col >= 7 || to.orient != 1 || transient {
		t.Fatalf("walk into a locked column = %+v (transient %v), want it to stop short of column 7", to, transient)
	}
	// Nothing reachable at all: the walk returns the start, stack-blocked.
	for r := 0; r < g.height(); r++ {
		g.locked[cell{r: r, c: 5}] = g.lockedPayload(0)
		g.locked[cell{r: r, c: 3}] = g.lockedPayload(0)
	}
	blocked := placement{orient: 0, col: 7}
	if to, transient := g.walk(p, blocked); to != p || transient {
		t.Fatalf("walled-in walk = %+v (transient %v), want the start, stack-blocked", to, transient)
	}
}
