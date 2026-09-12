package main

import (
	"testing"
	"time"
)

// The Guideline tables, row by row — the same numbers the GUI's
// internal/game/scoring_test.go pins, so the two never drift apart.
func TestClearInfoPointsAndAttack(t *testing.T) {
	cases := []struct {
		name         string
		c            clearInfo
		points, rows int // at level 1; guideline garbage
	}{
		{"nothing", clearInfo{}, 0, 0},
		{"single", clearInfo{lines: 1}, 100, 0},
		{"double", clearInfo{lines: 2}, 300, 1},
		{"triple", clearInfo{lines: 3}, 500, 2},
		{"quad", clearInfo{lines: 4}, 800, 4},
		{"b2b jetris", clearInfo{lines: 4, backToBack: true}, 1200, 6},
		{"a single is never b2b", clearInfo{lines: 1, backToBack: true}, 100, 0},
		{"combo 3 single", clearInfo{lines: 1, combo: 3}, 250, 0},
		{"jetris perfect clear", clearInfo{lines: 4, perfect: true}, 2800, 14},
		{"b2b jetris perfect clear", clearInfo{lines: 4, backToBack: true, perfect: true}, 4400, 16},
		{"single perfect clear", clearInfo{lines: 1, perfect: true}, 900, 10},
		{"t-spin no lines", clearInfo{spin: spinFull}, 400, 0},
		{"mini t-spin single", clearInfo{lines: 1, spin: spinMini}, 200, 0},
		{"t-spin double", clearInfo{lines: 2, spin: spinFull}, 1200, 4},
		{"b2b t-spin double", clearInfo{lines: 2, spin: spinFull, backToBack: true}, 1800, 6},
		{"b2b t-spin triple", clearInfo{lines: 3, spin: spinFull, backToBack: true}, 2400, 9},
		{"b2b mini t-spin double", clearInfo{lines: 2, spin: spinMini, backToBack: true}, 600, 2},
	}
	for _, tc := range cases {
		if got := tc.c.points(1); got != tc.points {
			t.Errorf("%s: points(1) = %d, want %d", tc.name, got, tc.points)
		}
		if got := tc.c.points(3); got != 3*tc.points {
			t.Errorf("%s: points(3) = %d, want %d", tc.name, got, 3*tc.points)
		}
		if got := tc.c.attackRows(true); got != tc.rows {
			t.Errorf("%s: attackRows(guideline) = %d, want %d", tc.name, got, tc.rows)
		}
		if got := tc.c.attackRows(false); got != max(tc.c.lines, 0) {
			t.Errorf("%s: attackRows(legacy) = %d, want one row per line", tc.name, got)
		}
	}
	if got := dropPoints(3, 7); got != 17 {
		t.Errorf("dropPoints(3, 7) = %d, want 17", got)
	}
	if got := (clearInfo{lines: 4, backToBack: true, combo: 2}).name(); got != "quad, back-to-back, combo 2" {
		t.Errorf("name = %q", got)
	}
}

// scoreLevel is the level a clear is scored at — the level gravity falls
// at (level): the seat's own lines' in competitive, the shared
// progression's on a shared board; 1 at the start, one more every ten lines.
func TestScoreLevel(t *testing.T) {
	g := &Game{mode: modeCompetitive}
	if g.scoreLevel() != 1 || g.level() != 1 {
		t.Errorf("levels = %d/%d, want 1/1 with no lines", g.scoreLevel(), g.level())
	}
	g.lines = 23
	if g.scoreLevel() != 3 {
		t.Errorf("scoreLevel = %d, want 3 at 23 own lines in competitive", g.scoreLevel())
	}
	if g.level() != 3 {
		t.Errorf("gravity level = %d, want 3: competitive gravity follows the seat's own lines", g.level())
	}
	coop := &Game{mode: modeCooperative, totalLines: 45}
	if coop.scoreLevel() != 5 || coop.level() != 5 {
		t.Errorf("coop levels = %d/%d, want 5/5 at 45 shared lines", coop.scoreLevel(), coop.level())
	}
	teams := &Game{mode: modeTeams, team: 1, teamLines: []int{80, 30}}
	if teams.level() != 4 {
		t.Errorf("teams level = %d, want 4: the OWN team's 30 lines", teams.level())
	}
	if levelOf(200) != maxLevel {
		t.Errorf("levelOf(200) = %d, want the cap %d", levelOf(200), maxLevel)
	}
}

// gravityInterval is the Tetris Worlds curve exactly (gameplays §7), the
// same numbers the GUI's engine plays by: (0.8 − (L − 1) × 0.007)^(L − 1)
// seconds at level L, levels 1 to 15, the top two faster than a frame.
func TestGravityCurve(t *testing.T) {
	wantMicros := []int64{1000000, 793000, 617796, 472729, 355197, 262004, 189677, 134735, 93882, 64152, 42976, 28218, 18153, 11439, 7059}
	for i, us := range wantMicros {
		if got := gravityInterval(minLevel + i).Round(time.Microsecond); got != time.Duration(us)*time.Microsecond {
			t.Errorf("gravityInterval(%d) = %v, want %dµs", minLevel+i, got, us)
		}
	}
	if gravityInterval(0) != gravityInterval(minLevel) || gravityInterval(99) != gravityInterval(maxLevel) {
		t.Error("a level outside the range should read as the nearer bound")
	}
}

// fallRows is the rows gravity owes taken as one move: as far as the board
// allows, stopping at the stack (the piece will lock) or at another
// player's falling piece (transient: it waits).
func TestFallRows(t *testing.T) {
	g := newGame(&Agent{name: "me", stopCh: make(chan struct{})}, "g", 0)
	g.w, g.h = 10, 24
	o := active{1, 0, 4, 4} // an O piece: rows 4-5, columns 4-5 (pieceCells)
	cs := pieceCells(o.pt, o.orient, o.row, o.col)
	top := cs[0].r
	for _, c := range cs {
		top = min(top, c.r)
	}
	bottom := top
	for _, c := range cs {
		bottom = max(bottom, c.r)
	}

	to, fell, transient := g.fallRows(o, 3)
	if fell != 3 || transient || to.row != o.row+3 {
		t.Fatalf("free board: fell %d rows to %v (transient %v), want 3", fell, to, transient)
	}
	// The stack two rows down: one row falls, and the piece is on the
	// stack — not transient.
	for c := 0; c < g.w; c++ {
		g.locked[cell{bottom + 2, c}] = wireCell{O: true, T: 1}
	}
	to, fell, transient = g.fallRows(o, 3)
	if fell != 1 || transient || to.row != o.row+1 {
		t.Fatalf("stack two rows down: fell %d rows to %v (transient %v), want 1 onto the stack", fell, to, transient)
	}
	// A teammate's falling piece two rows down instead: one row, then wait.
	g.locked = map[cell]wireCell{}
	for c := 0; c < g.w; c++ {
		g.othersAct[cell{bottom + 2, c}] = 1
	}
	to, fell, transient = g.fallRows(o, 3)
	if fell != 1 || !transient || to.row != o.row+1 {
		t.Fatalf("teammate two rows down: fell %d rows to %v (transient %v), want 1 and transient", fell, to, transient)
	}
	// Owed nothing: nothing moves, and nothing is reported blocked.
	if to, fell, transient = g.fallRows(o, 0); fell != 0 || transient || to != o {
		t.Fatalf("owed 0: fell %d (transient %v) to %v", fell, transient, to)
	}
}
