package main

import "testing"

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
		{"jetris", clearInfo{lines: 4}, 800, 4},
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
	if got := (clearInfo{lines: 4, backToBack: true, combo: 2}).name(); got != "jetris, back-to-back, combo 2" {
		t.Errorf("name = %q", got)
	}
}

// scoreLevel: the Guideline's 1-based level a clear is scored at — in
// competitive the seat's own lines' (a competitive seat's gravity level
// stays 0), on a shared board the shared progression's.
func TestScoreLevel(t *testing.T) {
	g := &Game{mode: modeCompetitive}
	if g.scoreLevel() != 1 {
		t.Errorf("scoreLevel = %d, want 1 with no lines", g.scoreLevel())
	}
	g.lines = 23
	if g.scoreLevel() != 3 {
		t.Errorf("scoreLevel = %d, want 3 at 23 own lines in competitive", g.scoreLevel())
	}
	if g.level() != 0 {
		t.Errorf("gravity level = %d, want 0 in competitive regardless", g.level())
	}
	coop := &Game{mode: modeCooperative, totalLines: 45}
	if coop.scoreLevel() != 5 {
		t.Errorf("coop scoreLevel = %d, want 5 at 45 shared lines", coop.scoreLevel())
	}
}
