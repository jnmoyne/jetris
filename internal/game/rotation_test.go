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
	slotTop := pf.Height - 6 // the three-deep slot, clear of the floor
	lock(slotTop-2, 3)       // the overhang, above the T's left cell
	for r := slotTop; r <= slotTop+2; r++ {
		for c := 0; c < pf.Width; c++ {
			if c == 3 || (r == slotTop+1 && c == 4) {
				continue // the slot (col 3) and the notch its nub fills
			}
			lock(r, c)
		}
	}
	p := Piece{Type: PieceT, Orientation: 0, Row: slotTop - 2, Col: 3}
	if !CanPlace(p, pf) {
		t.Fatal("setup: the T must fit above the slot")
	}
	got, ok := Rotate(p, TurnCW, pf)
	want := Piece{Type: PieceT, Orientation: 1, Row: slotTop, Col: 2}
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
	got, ok := Rotate(p, TurnCW, pf)
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

// The half turn's SRS-X kicks in the Guideline's (x, y) form, +y UP, the
// in-place try not listed. rotation.go stores {0, 0} and then
// (dRow, dCol) = (-y, x), as for the SRS tables; this test re-derives every
// entry.
var srsx180JLSTZ = map[[2]int][][2]int{
	{0, 2}: {{1, 0}, {2, 0}, {1, -1}, {2, -1}, {-1, 0}, {-2, 0}, {-1, -1}, {-2, -1}, {0, 1}, {3, 0}, {-3, 0}},
	{1, 3}: {{0, -1}, {0, -2}, {-1, -1}, {-1, -2}, {0, 1}, {0, 2}, {-1, 1}, {-1, 2}, {1, 0}, {0, -3}, {0, 3}},
	{2, 0}: {{-1, 0}, {-2, 0}, {-1, 1}, {-2, 1}, {1, 0}, {2, 0}, {1, 1}, {2, 1}, {0, -1}, {-3, 0}, {3, 0}},
	{3, 1}: {{0, -1}, {0, -2}, {1, -1}, {1, -2}, {0, 1}, {0, 2}, {1, 1}, {1, 2}, {-1, 0}, {0, -3}, {0, 3}},
}

var srsx180I = map[[2]int][][2]int{
	{0, 2}: {{-1, 0}, {-2, 0}, {1, 0}, {2, 0}, {0, -1}},
	{1, 3}: {{0, -1}, {0, -2}, {0, 1}, {0, 2}, {-1, 0}},
	{2, 0}: {{1, 0}, {2, 0}, {-1, 0}, {-2, 0}, {0, 1}},
	{3, 1}: {{0, -1}, {0, -2}, {0, 1}, {0, 2}, {1, 0}},
}

func Test180KickTables(t *testing.T) {
	check := func(name string, got, want map[[2]int][][2]int) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s: %d transitions, want %d", name, len(got), len(want))
		}
		for key, kicks := range want {
			g := got[key]
			if len(g) != len(kicks)+1 || g[0] != [2]int{0, 0} {
				t.Fatalf("%s %d→%d: %d kicks, want the in-place try and then %d", name, key[0], key[1], len(g), len(kicks))
			}
			for i, xy := range kicks {
				w := [2]int{-xy[1], xy[0]}
				if g[i+1] != w {
					t.Errorf("%s %d→%d kick %d: got (dRow %d, dCol %d), want (dRow %d, dCol %d) for (x %d, y %d)",
						name, key[0], key[1], i+1, g[i+1][0], g[i+1][1], w[0], w[1], xy[0], xy[1])
				}
			}
		}
	}
	check("JLSTZ", kicks180JLSTZ, srsx180JLSTZ)
	check("I", kicks180I, srsx180I)
}

// A half turn in the open turns in place and a second one turns back; the O
// never turns; a T lying on the floor turns by the kick that lifts it a row
// ({-1, 0}, index 9 of its table); and a flat I with no row free under it
// cannot turn at all — 0→2 lays the bar a row lower and no kick of that row
// goes up — while a row free under it turns it in place.
func Test180Turns(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	p := Piece{Type: PieceT, Orientation: 0, Row: 10, Col: 4}
	got, kick, ok := RotateKick(p, Turn180, pf)
	if want := (Piece{Type: PieceT, Orientation: 2, Row: 10, Col: 4}); !ok || kick != 0 || got != want {
		t.Fatalf("T in the open: RotateKick = %+v, %d, %v; want %+v, 0, true", got, kick, ok, want)
	}
	if back, _, ok := RotateKick(got, Turn180, pf); !ok || back != p {
		t.Fatalf("a second half turn: %+v, %v; want the T back at %+v", back, ok, p)
	}
	if _, _, ok := RotateKick(Piece{Type: PieceO, Row: 10, Col: 4}, Turn180, pf); ok {
		t.Fatal("the O never rotates")
	}
	bottom := pf.Height - 1
	floorT := Piece{Type: PieceT, Orientation: 0, Row: bottom - 1, Col: 4} // its bar on the floor row
	got, kick, ok = RotateKick(floorT, Turn180, pf)
	if want := (Piece{Type: PieceT, Orientation: 2, Row: bottom - 2, Col: 4}); !ok || kick != 9 || got != want {
		t.Fatalf("T on the floor: RotateKick = %+v, %d, %v; want %+v, 9, true", got, kick, ok, want)
	}
	floorI := Piece{Type: PieceI, Orientation: 0, Row: bottom - 1, Col: 3} // cells on the floor row
	if !CanPlace(floorI, pf) {
		t.Fatal("setup: the I must lie on the floor")
	}
	if got, _, ok := RotateKick(floorI, Turn180, pf); ok || got != floorI {
		t.Fatalf("flat I on the floor: %+v, %v; want no turn", got, ok)
	}
	up := floorI
	up.Row--
	want := Piece{Type: PieceI, Orientation: 2, Row: up.Row, Col: 3}
	if got, kick, ok := RotateKick(up, Turn180, pf); !ok || kick != 0 || got != want {
		t.Fatalf("flat I a row up: %+v, %d, %v; want %+v turned in place", got, kick, ok, want)
	}
}

// twistBoard is a board drawn as the wiki draws a twist: 'G' the stack, '.'
// empty, and any other letter the falling piece — which is not put on the
// board; Test180Twists checks its case's piece against the letters.
func twistBoard(t *testing.T, rows ...string) *Playfield {
	t.Helper()
	pf := NewPlayfieldWithHeight(len(rows[0]), len(rows))
	for r, row := range rows {
		for c, ch := range row {
			if ch == 'G' {
				pf.Rows[r].Cells[c] = Cell{Occupied: true, PieceType: PieceJ}
			}
		}
	}
	return pf
}

// The wiki's 180° twists (harddrop.com/wiki/List_of_twists, "180° Twists"),
// each the board as the wiki draws it before the turn — the letters the
// piece — with the piece's placement after the half turn and the kick that
// found it. All of them are SRS-X twists: the table is what makes them, and
// the T ones are full T-spins by the corner rule.
func Test180Twists(t *testing.T) {
	at := func(typ PieceType, o, row, col int) Piece {
		return Piece{Type: typ, Orientation: o, Row: row, Col: col}
	}
	cases := []struct {
		name    string
		before  []string
		p, want Piece
		kick    int
	}{
		{"T-spin single", []string{
			"..........",
			"..........",
			"GGGT.GGGGG",
			"GGTTT..GGG",
			"GGGGG.GGGG",
		}, at(PieceT, 0, 2, 2), at(PieceT, 2, 2, 4), 2},
		{"T-spin single, through the wall", []string{
			"..........",
			"..........",
			"GGT.GGGGGG",
			"GTTT...GGG",
			"GGGGG.GGGG",
		}, at(PieceT, 0, 2, 1), at(PieceT, 2, 2, 4), 10},
		{"T-spin double, from above", []string{
			"..........",
			"GGGGT.GGGG",
			"GGGTTTGGGG",
			"GGGGG...GG",
			"GGGGGG.GGG",
		}, at(PieceT, 0, 1, 3), at(PieceT, 2, 2, 5), 4},
		{"T-spin double, a row down", []string{
			"....T.....",
			"GGGTTGGGGG",
			"GGGGT.GGGG",
			"GGGG.GGGGG",
		}, at(PieceT, 3, 0, 3), at(PieceT, 1, 1, 3), 1},
		{"T-spin double, two rows down", []string{
			"....T.....",
			"...TTGGGGG",
			"GGGGTGGGGG",
			"GGG...GGGG",
			"GGGG.GGGGG",
		}, at(PieceT, 3, 0, 3), at(PieceT, 1, 2, 3), 2},
		{"T-spin triple", []string{
			"....T.....",
			"...TTGGGGG",
			"GGGGTGGGGG",
			"GGGG..GGGG",
			"GGGG.GGGGG",
		}, at(PieceT, 3, 0, 3), at(PieceT, 1, 2, 3), 2},
		{"T-spin triple, three rows down", []string{
			"..........",
			".....GGGGG",
			"....TGGGGG",
			"...TTGGGGG",
			"GGGGTGGGGG",
			"GGGG.GGGGG",
			"GGGG..GGGG",
			"GGGG.GGGGG",
		}, at(PieceT, 3, 2, 3), at(PieceT, 1, 5, 3), 10},
		{"I-spin single", []string{
			"..........",
			"GIGGGGGGGG",
			"GI.GGGGGGG",
			"GI.GGGGGGG",
			"GI.GGGGGGG",
			"GG.GGGGGGG",
		}, at(PieceI, 3, 1, 0), at(PieceI, 1, 2, 0), 1},
		{"I-spin double", []string{
			"..........",
			"GIGGGGGGGG",
			"GIGGGGGGGG",
			"GI.GGGGGGG",
			"GI.GGGGGGG",
			"GG.GGGGGGG",
			"GG.GGGGGGG",
		}, at(PieceI, 3, 1, 0), at(PieceI, 1, 3, 0), 2},
		{"S-spin single", []string{
			"..........",
			"......SS..",
			"GGGGGSS...",
			"GGGG..GGGG",
		}, at(PieceS, 0, 1, 5), at(PieceS, 2, 1, 4), 5},
		{"S-spin double, flat", []string{
			"..........",
			"GGGGG.SSGG",
			"GGGGGSSGGG",
			"GGGG..GGGG",
		}, at(PieceS, 0, 1, 5), at(PieceS, 2, 1, 4), 5},
		{"S-spin double, upright", []string{
			"..........",
			"..........",
			"...S......",
			"GGGSSGGGGG",
			"GGGGS.GGGG",
			"GGGGG.GGGG",
		}, at(PieceS, 3, 2, 3), at(PieceS, 1, 3, 3), 1},
		{"S-spin triple", []string{
			"..........",
			"...S......",
			"...SS.....",
			"GGGGSGGGGG",
			"GGGG..GGGG",
			"GGGGG.GGGG",
		}, at(PieceS, 3, 1, 3), at(PieceS, 1, 3, 3), 2},
		{"Z-spin single", []string{
			"..........",
			"..ZZ......",
			"...ZZGGGGG",
			"GGGG..GGGG",
		}, at(PieceZ, 0, 1, 2), at(PieceZ, 2, 1, 3), 1},
		{"Z-spin double, flat", []string{
			"..........",
			"GGZZ.GGGGG",
			"GGGZZGGGGG",
			"GGGG..GGGG",
		}, at(PieceZ, 0, 1, 2), at(PieceZ, 2, 1, 3), 1},
		{"Z-spin double, upright", []string{
			"..........",
			"..........",
			"......Z...",
			"GGGGGZZGGG",
			"GGGG.ZGGGG",
			"GGGG.GGGGG",
		}, at(PieceZ, 1, 2, 4), at(PieceZ, 3, 3, 4), 1},
		{"Z-spin triple", []string{
			"..........",
			"......Z...",
			".....ZZ...",
			"GGGGGZGGGG",
			"GGGG..GGGG",
			"GGGG.GGGGG",
		}, at(PieceZ, 1, 1, 4), at(PieceZ, 3, 3, 4), 2},
		{"L-spin double", []string{
			"..........",
			"..........",
			"..........",
			"GGGllGGGGG",
			"GGGGl..GGG",
			"GGGGlGGGGG",
		}, at(PieceL, 3, 3, 3), at(PieceL, 1, 2, 3), 5},
		{"J-spin double", []string{
			"..........",
			"..........",
			"..........",
			"GGGGGjjGGG",
			"GGG..jGGGG",
			"GGGGGjGGGG",
		}, at(PieceJ, 1, 3, 4), at(PieceJ, 3, 2, 4), 5},
	}
	for _, c := range cases {
		pf := twistBoard(t, c.before...)
		letters := map[[2]int]bool{}
		for r, row := range c.before {
			for col, ch := range row {
				if ch != '.' && ch != 'G' {
					letters[[2]int{r, col}] = true
				}
			}
		}
		for _, cell := range c.p.Cells() {
			if !letters[cell] {
				t.Fatalf("%s: the piece %+v is not where the diagram draws it (%v)", c.name, c.p, cell)
			}
		}
		if len(letters) != 4 || !CanPlace(c.p, pf) {
			t.Fatalf("%s: the diagram draws %d piece cells, or the piece does not fit them", c.name, len(letters))
		}
		got, kick, ok := RotateKick(c.p, Turn180, pf)
		if !ok || got != c.want || kick != c.kick {
			t.Errorf("%s: RotateKick(180) = %+v, kick %d, %v; want %+v, kick %d, true", c.name, got, kick, ok, c.want, c.kick)
			continue
		}
		if c.p.Type == PieceT {
			if spin := DetectTSpin(got, pf, SpinState{Rotated: true, Kick: kick, Half: true}); spin != TSpinFull {
				t.Errorf("%s: DetectTSpin = %v, want a full T-spin", c.name, spin)
			}
		}
	}
}
