package game

import (
	"testing"

	"jetris/internal/config"
)

// The SRS kick tables as the Guideline publishes them: (x, y) with +y UP.
// rotation.go stores (dRow, dCol) = (-y, x); this test re-derives every entry.
var guidelineKicksJLSTZ = map[[2]int][][2]int{
	{0, 1}: {{0, 0}, {-1, 0}, {-1, 1}, {0, -2}, {-1, -2}},
	{1, 0}: {{0, 0}, {1, 0}, {1, -1}, {0, 2}, {1, 2}},
	{1, 2}: {{0, 0}, {1, 0}, {1, -1}, {0, 2}, {1, 2}},
	{2, 1}: {{0, 0}, {-1, 0}, {-1, 1}, {0, -2}, {-1, -2}},
	{2, 3}: {{0, 0}, {1, 0}, {1, 1}, {0, -2}, {1, -2}},
	{3, 2}: {{0, 0}, {-1, 0}, {-1, -1}, {0, 2}, {-1, 2}},
	{3, 0}: {{0, 0}, {-1, 0}, {-1, -1}, {0, 2}, {-1, 2}},
	{0, 3}: {{0, 0}, {1, 0}, {1, 1}, {0, -2}, {1, -2}},
}

var guidelineKicksI = map[[2]int][][2]int{
	{0, 1}: {{0, 0}, {-2, 0}, {1, 0}, {-2, -1}, {1, 2}},
	{1, 0}: {{0, 0}, {2, 0}, {-1, 0}, {2, 1}, {-1, -2}},
	{1, 2}: {{0, 0}, {-1, 0}, {2, 0}, {-1, 2}, {2, -1}},
	{2, 1}: {{0, 0}, {1, 0}, {-2, 0}, {1, -2}, {-2, 1}},
	{2, 3}: {{0, 0}, {2, 0}, {-1, 0}, {2, 1}, {-1, -2}},
	{3, 2}: {{0, 0}, {-2, 0}, {1, 0}, {-2, -1}, {1, 2}},
	{3, 0}: {{0, 0}, {1, 0}, {-2, 0}, {1, -2}, {-2, 1}},
	{0, 3}: {{0, 0}, {-1, 0}, {2, 0}, {-1, 2}, {2, -1}},
}

func TestSRSKickTables(t *testing.T) {
	check := func(name string, got, want map[[2]int][][2]int) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d transitions, want %d", name, len(got), len(want))
		}
		for key, kicks := range want {
			g := got[key]
			if len(g) != len(kicks) {
				t.Fatalf("%s %d→%d: %d kicks, want %d", name, key[0], key[1], len(g), len(kicks))
			}
			for i, xy := range kicks {
				w := [2]int{-xy[1], xy[0]}
				if g[i] != w {
					t.Errorf("%s %d→%d kick %d: got (dRow %d, dCol %d), want (dRow %d, dCol %d) for Guideline (x %d, y %d)",
						name, key[0], key[1], i+1, g[i][0], g[i][1], w[0], w[1], xy[0], xy[1])
				}
			}
		}
	}
	check("JLSTZ", kicksJLSTZ, guidelineKicksJLSTZ)
	check("I", kicksI, guidelineKicksI)
}

// A T-spin triple: the T lies flat under an overhang, one column off a
// three-deep slot, and turning it clockwise drops it into the slot — only the
// 5th kick, (-1, -2) = left one and down two, fits.
func TestTSpinTripleKick(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	lock := func(r, c int) { pf.Rows[r].Cells[c] = Cell{Occupied: true, PieceType: PieceL} }
	lock(20, 3) // the overhang, above the T's left cell
	for r := 22; r <= 24; r++ {
		for c := 0; c < pf.Width; c++ {
			if c == 3 || (r == 23 && c == 4) {
				continue // the slot (col 3, rows 22-24) and the notch its nub fills
			}
			lock(r, c)
		}
	}
	p := Piece{Type: PieceT, Orientation: 0, Row: 20, Col: 3}
	if !CanPlace(p, pf) {
		t.Fatal("setup: the T must fit above the slot")
	}
	got, ok := Rotate(p, true, pf)
	want := Piece{Type: PieceT, Orientation: 1, Row: 22, Col: 2}
	if !ok || got != want {
		t.Fatalf("T-spin triple: Rotate = %+v, %v; want %+v, true", got, ok, want)
	}
}

// An I lying flat on the floor stands up: the 5th kick, (+1, +2) = right one
// and up two, is the only one that lifts it clear of the bottom edge.
func TestIFloorKick(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	bottom := pf.Height - 1
	p := Piece{Type: PieceI, Orientation: 0, Row: bottom - 1, Col: 3} // cells on the floor row
	if !CanPlace(p, pf) {
		t.Fatal("setup: the I must lie on the floor")
	}
	got, ok := Rotate(p, true, pf)
	want := Piece{Type: PieceI, Orientation: 1, Row: bottom - 3, Col: 4}
	if !ok || got != want {
		t.Fatalf("I floor kick: Rotate = %+v, %v; want %+v, true", got, ok, want)
	}
	for _, c := range got.Cells() {
		if c[0] > bottom {
			t.Fatalf("kicked I has a cell below the floor: %v", c)
		}
	}
}
