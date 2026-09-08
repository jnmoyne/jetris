package game

import "testing"

// The Guideline scoring table (tetris.wiki/Scoring, "Recent guideline
// compatible games"), row by row, at level 1 and at level 3.
func TestClearPointsTable(t *testing.T) {
	cases := []struct {
		name  string
		clear Clear
		want  int // at level 1
	}{
		{"nothing", Clear{}, 0},
		{"single", Clear{Lines: 1}, 100},
		{"double", Clear{Lines: 2}, 300},
		{"triple", Clear{Lines: 3}, 500},
		{"jetris", Clear{Lines: 4}, 800},
		{"mini t-spin no lines", Clear{Spin: TSpinMini}, 100},
		{"t-spin no lines", Clear{Spin: TSpinFull}, 400},
		{"mini t-spin single", Clear{Lines: 1, Spin: TSpinMini}, 200},
		{"t-spin single", Clear{Lines: 1, Spin: TSpinFull}, 800},
		{"mini t-spin double", Clear{Lines: 2, Spin: TSpinMini}, 400},
		{"t-spin double", Clear{Lines: 2, Spin: TSpinFull}, 1200},
		{"t-spin triple", Clear{Lines: 3, Spin: TSpinFull}, 1600},
		{"a three-line mini is a t-spin triple", Clear{Lines: 3, Spin: TSpinMini}, 1600},
		// Back-to-Back: the action's points × 1.5, difficult clears only.
		{"b2b jetris", Clear{Lines: 4, BackToBack: true}, 1200},
		{"b2b t-spin single", Clear{Lines: 1, Spin: TSpinFull, BackToBack: true}, 1200},
		{"b2b t-spin double", Clear{Lines: 2, Spin: TSpinFull, BackToBack: true}, 1800},
		{"b2b t-spin triple", Clear{Lines: 3, Spin: TSpinFull, BackToBack: true}, 2400},
		{"b2b mini t-spin single", Clear{Lines: 1, Spin: TSpinMini, BackToBack: true}, 300},
		{"b2b mini t-spin double", Clear{Lines: 2, Spin: TSpinMini, BackToBack: true}, 600},
		{"a single is never b2b", Clear{Lines: 1, BackToBack: true}, 100},
		{"a t-spin with no lines is never b2b", Clear{Spin: TSpinFull, BackToBack: true}, 400},
		// Combo: 50 × combo count, from the second clear of a run.
		{"first clear of a run", Clear{Lines: 1, Combo: 0}, 100},
		{"second clear of a run", Clear{Lines: 1, Combo: 1}, 150},
		{"fifth clear of a run", Clear{Lines: 2, Combo: 4}, 500},
		{"combo needs lines", Clear{Spin: TSpinFull, Combo: 3}, 400},
		// Perfect clears add to the clear they came with.
		{"single perfect clear", Clear{Lines: 1, Perfect: true}, 900},
		{"double perfect clear", Clear{Lines: 2, Perfect: true}, 1500},
		{"triple perfect clear", Clear{Lines: 3, Perfect: true}, 2300},
		{"jetris perfect clear", Clear{Lines: 4, Perfect: true}, 2800},
		{"b2b jetris perfect clear", Clear{Lines: 4, BackToBack: true, Perfect: true}, 4400},
		{"t-spin double perfect clear", Clear{Lines: 2, Spin: TSpinFull, Perfect: true}, 2400},
		{"perfect needs lines", Clear{Perfect: true}, 0},
		{"everything at once", Clear{Lines: 4, BackToBack: true, Combo: 2, Perfect: true}, 4500},
	}
	for _, tc := range cases {
		if got := tc.clear.Points(1); got != tc.want {
			t.Errorf("%s: Points(1) = %d, want %d", tc.name, got, tc.want)
		}
		if got := tc.clear.Points(3); got != 3*tc.want {
			t.Errorf("%s: Points(3) = %d, want %d", tc.name, got, 3*tc.want)
		}
	}
	if got := (Clear{Lines: 1}).Points(0); got != 100 {
		t.Errorf("level 0 is played as level 1: got %d", got)
	}
}

func TestClearDifficult(t *testing.T) {
	for _, tc := range []struct {
		clear Clear
		want  bool
	}{
		{Clear{Lines: 1}, false}, {Clear{Lines: 2}, false}, {Clear{Lines: 3}, false},
		{Clear{Lines: 4}, true},
		{Clear{Lines: 1, Spin: TSpinMini}, true}, {Clear{Lines: 1, Spin: TSpinFull}, true},
		{Clear{Lines: 3, Spin: TSpinFull}, true},
		{Clear{Spin: TSpinFull}, false}, {Clear{Spin: TSpinMini}, false}, {Clear{}, false},
	} {
		if got := tc.clear.Difficult(); got != tc.want {
			t.Errorf("%+v: Difficult = %v, want %v", tc.clear, got, tc.want)
		}
	}
}

// The Guideline garbage table (tetris.wiki/Garbage, "General Garbage System
// in Guideline Games"), and the legacy one-row-per-line rule beside it.
func TestClearAttackRows(t *testing.T) {
	cases := []struct {
		name  string
		clear Clear
		want  int
	}{
		{"nothing", Clear{}, 0},
		{"single", Clear{Lines: 1}, 0},
		{"double", Clear{Lines: 2}, 1},
		{"triple", Clear{Lines: 3}, 2},
		{"jetris", Clear{Lines: 4}, 4},
		{"mini t-spin single", Clear{Lines: 1, Spin: TSpinMini}, 0},
		{"mini t-spin double", Clear{Lines: 2, Spin: TSpinMini}, 1},
		{"t-spin single", Clear{Lines: 1, Spin: TSpinFull}, 2},
		{"t-spin double", Clear{Lines: 2, Spin: TSpinFull}, 4},
		{"t-spin triple", Clear{Lines: 3, Spin: TSpinFull}, 6},
		{"t-spin no lines", Clear{Spin: TSpinFull}, 0},
		// Back-to-Back bonus: +1 MTSS/MTSD/TSS, +2 TSD/Jetris, +3 TST.
		{"b2b mini t-spin single", Clear{Lines: 1, Spin: TSpinMini, BackToBack: true}, 1},
		{"b2b mini t-spin double", Clear{Lines: 2, Spin: TSpinMini, BackToBack: true}, 2},
		{"b2b t-spin single", Clear{Lines: 1, Spin: TSpinFull, BackToBack: true}, 3},
		{"b2b t-spin double", Clear{Lines: 2, Spin: TSpinFull, BackToBack: true}, 6},
		{"b2b t-spin triple", Clear{Lines: 3, Spin: TSpinFull, BackToBack: true}, 9},
		{"b2b jetris", Clear{Lines: 4, BackToBack: true}, 6},
		{"a double is never b2b", Clear{Lines: 2, BackToBack: true}, 1},
		// Perfect clear: 10 on top of the clear.
		{"single perfect clear", Clear{Lines: 1, Perfect: true}, 10},
		{"jetris perfect clear", Clear{Lines: 4, Perfect: true}, 14},
		{"b2b jetris perfect clear", Clear{Lines: 4, BackToBack: true, Perfect: true}, 16},
		{"combos send nothing extra", Clear{Lines: 2, Combo: 5}, 1},
	}
	for _, tc := range cases {
		if got := tc.clear.AttackRows(true); got != tc.want {
			t.Errorf("%s: AttackRows(guideline) = %d, want %d", tc.name, got, tc.want)
		}
		if got, want := tc.clear.AttackRows(false), max(tc.clear.Lines, 0); got != want {
			t.Errorf("%s: AttackRows(legacy) = %d, want %d (one row per line)", tc.name, got, want)
		}
	}
}

func TestClearName(t *testing.T) {
	for _, tc := range []struct {
		clear Clear
		want  string
	}{
		{Clear{}, ""},
		{Clear{Lines: 1}, "SINGLE"},
		{Clear{Lines: 4}, "JETRIS"},
		{Clear{Spin: TSpinFull}, "T-SPIN"},
		{Clear{Spin: TSpinMini}, "MINI T-SPIN"},
		{Clear{Lines: 2, Spin: TSpinFull}, "T-SPIN DOUBLE"},
		{Clear{Lines: 1, Spin: TSpinMini}, "MINI T-SPIN SINGLE"},
		{Clear{Lines: 3, Spin: TSpinMini}, "T-SPIN TRIPLE"},
	} {
		if got := tc.clear.Name(); got != tc.want {
			t.Errorf("%+v: Name = %q, want %q", tc.clear, got, tc.want)
		}
	}
}

func TestDropPoints(t *testing.T) {
	if got := DropPoints(3, 7); got != 17 {
		t.Errorf("DropPoints(3, 7) = %d, want 17 (1 per soft cell, 2 per hard cell)", got)
	}
	if got := DropPoints(-1, -1); got != 0 {
		t.Errorf("DropPoints(-1, -1) = %d, want 0", got)
	}
}

func TestIsPerfectClear(t *testing.T) {
	pf := NewPlayfieldWithHeight(4, 3)
	if !IsPerfectClear(pf.Rows) {
		t.Fatal("an empty board is a perfect clear")
	}
	pf.Rows[2].Cells[1] = Cell{Occupied: true, Active: true, PlayerIdx: 1}
	if !IsPerfectClear(pf.Rows) {
		t.Fatal("another player's falling piece does not spoil a perfect clear")
	}
	pf.Rows[2].Cells[0] = Cell{Occupied: true, Adversarial: true}
	if IsPerfectClear(pf.Rows) {
		t.Fatal("a garbage cell is a locked cell: not a perfect clear")
	}
	pf.Rows[2].Cells[0] = Cell{Occupied: true, PieceType: PieceI}
	if IsPerfectClear(pf.Rows) {
		t.Fatal("a locked cell: not a perfect clear")
	}
}

// tBoard is a 6-wide, 5-high board drawn as rows of '.' (empty), '#' (locked)
// and 'a' (another player's active cell).
func tBoard(t *testing.T, rows ...string) *Playfield {
	t.Helper()
	pf := NewPlayfieldWithHeight(len(rows[0]), len(rows))
	for r, row := range rows {
		for c, ch := range row {
			switch ch {
			case '#':
				pf.Rows[r].Cells[c] = Cell{Occupied: true, PieceType: PieceJ}
			case 'a':
				pf.Rows[r].Cells[c] = Cell{Occupied: true, Active: true, PlayerIdx: 7}
			}
		}
	}
	return pf
}

func TestDetectTSpin(t *testing.T) {
	rotated := SpinState{Rotated: true}
	// A T-slot: the T points down into it (orientation 2, anchor row 1 col
	// 1), its two bottom corners the slot's floor, one top corner the
	// overhang — the T-spin double.
	slot := tBoard(t,
		"......",
		".#....",
		"......",
		".#.#..",
		"######",
	)
	down := Piece{Type: PieceT, Orientation: 2, Row: 1, Col: 1}
	if got := DetectTSpin(down, slot, rotated); got != TSpinFull {
		t.Errorf("T pointing down into a slot, rotated: %v, want full", got)
	}
	if got := DetectTSpin(down, slot, SpinState{}); got != TSpinNone {
		t.Errorf("the same T shifted into place: %v, want none", got)
	}
	// Two corners only: no T-spin.
	open := tBoard(t,
		"......",
		"......",
		"......",
		".#.#..",
		"######",
	)
	if got := DetectTSpin(down, open, rotated); got != TSpinNone {
		t.Errorf("two corners: %v, want none", got)
	}
	// Another player's falling piece is not a corner.
	transient := tBoard(t,
		"......",
		".a....",
		"......",
		".#.#..",
		"######",
	)
	if got := DetectTSpin(down, transient, rotated); got != TSpinNone {
		t.Errorf("a falling piece as the third corner: %v, want none", got)
	}
	// The Mini: the T points up (orientation 0), its flat side on the floor
	// (both bottom corners filled) under an overhang covering ONE top corner
	// — a front corner is open.
	mini := tBoard(t,
		"......",
		"......",
		".#....",
		"......",
		"######",
	)
	up := Piece{Type: PieceT, Orientation: 0, Row: 2, Col: 1}
	if got := DetectTSpin(up, mini, rotated); got != TSpinMini {
		t.Errorf("T pointing up under a one-corner overhang: %v, want mini", got)
	}
	// …unless it got there by the last kick (the T-spin triple's).
	if got := DetectTSpin(up, mini, SpinState{Rotated: true, Kick: LastKick}); got != TSpinFull {
		t.Errorf("the same Mini via the last kick: %v, want full", got)
	}
	// A half turn is a rotation for the corner rule…
	if got := DetectTSpin(down, slot, SpinState{Rotated: true, Half: true}); got != TSpinFull {
		t.Errorf("T half-turned into the slot: %v, want full", got)
	}
	// …but its table has no triple kick: the Mini stays a Mini whatever the
	// kick's index.
	if got := DetectTSpin(up, mini, SpinState{Rotated: true, Kick: LastKick, Half: true}); got != TSpinMini {
		t.Errorf("the Mini via a half turn's kick %d: %v, want mini", LastKick, got)
	}
	// A T pointing left (orientation 3): its front corners are the left
	// pair.
	left := Piece{Type: PieceT, Orientation: 3, Row: 2, Col: 0}
	walled := tBoard(t,

		"......",
		"......",
		"#.....",
		"......",
		"#.#...",
	)
	if got := DetectTSpin(left, walled, rotated); got != TSpinFull {
		t.Errorf("T pointing left with both left corners filled: %v, want full", got)
	}
	// The floor as a corner: a T pointing down whose box hangs below the
	// board's last row.
	floor := tBoard(t,
		"......",
		"......",
		"......",
		"#.#...",
		"......",
	)
	// anchor row 3: box rows 3..5, row 5 is below the floor → both bottom
	// corners "filled"; (3,0) and (3,2) locked → four corners, front pair
	// (bottom) filled → full.
	if got := DetectTSpin(Piece{Type: PieceT, Orientation: 2, Row: 3, Col: 0}, floor, rotated); got != TSpinFull {
		t.Errorf("the floor as the front corners: %v, want full", got)
	}
	// Not a T: never a T-spin.
	if got := DetectTSpin(Piece{Type: PieceL, Orientation: 2, Row: 1, Col: 1}, slot, rotated); got != TSpinNone {
		t.Errorf("an L: %v, want none", got)
	}
}

func TestRotateKickReportsTheKick(t *testing.T) {
	pf := NewPlayfieldWithHeight(10, 6)
	p := Piece{Type: PieceT, Orientation: 0, Row: 1, Col: 3}
	if _, kick, ok := RotateKick(p, TurnCW, pf); !ok || kick != 0 {
		t.Fatalf("free rotation: kick %d ok %v, want kick 0 ok true", kick, ok)
	}
	// Against the left wall a vertical T rotating CCW (1 → 0) needs kick 1
	// (0, +1) — the in-place rotation would put its bar cell at col -1.
	wall := Piece{Type: PieceT, Orientation: 1, Row: 1, Col: -1}
	if _, kick, ok := RotateKick(wall, TurnCCW, pf); !ok || kick != 1 {
		t.Fatalf("wall kick: kick %d ok %v, want kick 1 ok true", kick, ok)
	}
	if _, _, ok := RotateKick(Piece{Type: PieceO, Row: 1, Col: 1}, TurnCW, pf); ok {
		t.Fatal("the O never rotates")
	}
}
