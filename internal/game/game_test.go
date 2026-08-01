package game

import (
	"testing"
	"time"

	"jetris/internal/config"
)

func TestPieceCells(t *testing.T) {
	// Test I piece orientation 0: horizontal line at row 1
	p := Piece{Type: PieceI, Orientation: 0, Row: 0, Col: 0}
	cells := p.Cells()
	if len(cells) != 4 {
		t.Fatalf("I piece should have 4 cells, got %d", len(cells))
	}
	expected := [][2]int{{1, 0}, {1, 1}, {1, 2}, {1, 3}}
	for i, c := range cells {
		if c != expected[i] {
			t.Errorf("I[0] cell %d: got %v, want %v", i, c, expected[i])
		}
	}

	// Test O piece - should be 2x2
	p = Piece{Type: PieceO, Orientation: 0, Row: 5, Col: 3}
	cells = p.Cells()
	if len(cells) != 4 {
		t.Fatalf("O piece should have 4 cells, got %d", len(cells))
	}

	// Every piece type and orientation should have exactly 4 cells
	for pt := PieceI; pt <= PieceL; pt++ {
		for orient := 0; orient < 4; orient++ {
			p := Piece{Type: pt, Orientation: orient, Row: 10, Col: 3}
			cells := p.Cells()
			if len(cells) != 4 {
				t.Errorf("Piece %d orient %d: got %d cells, want 4", pt, orient, len(cells))
			}
		}
	}
}

func TestCanPlace(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)

	// Piece in valid position
	p := Piece{Type: PieceT, Orientation: 0, Row: 10, Col: 4}
	if !CanPlace(p, pf) {
		t.Error("T piece should be placeable in empty field")
	}

	// Piece out of bounds (left)
	p = Piece{Type: PieceI, Orientation: 0, Row: 10, Col: -1}
	if CanPlace(p, pf) {
		t.Error("piece at col -1 should not be placeable")
	}

	// Piece out of bounds (right)
	p = Piece{Type: PieceI, Orientation: 0, Row: 10, Col: 8}
	if CanPlace(p, pf) {
		t.Error("I piece at col 8 orient 0 should overflow right")
	}

	// Piece overlapping locked cell
	pf.Rows[10].Cells[5] = Cell{Occupied: true, PieceType: PieceT}
	p = Piece{Type: PieceT, Orientation: 0, Row: 9, Col: 4}
	// T orient 0: cells at (9,5), (10,4), (10,5), (10,6) - (10,5) is occupied
	if CanPlace(p, pf) {
		t.Error("piece overlapping locked cell should not be placeable")
	}
}

func TestHardDropDestination(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	p := Piece{Type: PieceI, Orientation: 0, Row: 0, Col: 3}
	dest := HardDropDestination(p, pf)
	// I piece orient 0 occupies row+1, so max row is TotalRows-2 (cells at TotalRows-1)
	expectedRow := config.TotalRows - 2
	if dest.Row != expectedRow {
		t.Errorf("hard drop destination: got row %d, want %d", dest.Row, expectedRow)
	}

	// Add a locked row at bottom
	lastRow := config.TotalRows - 1
	for c := 0; c < 10; c++ {
		pf.Rows[lastRow].Cells[c] = Cell{Occupied: true, PieceType: PieceO}
	}
	dest = HardDropDestination(p, pf)
	if dest.Row != expectedRow-1 {
		t.Errorf("hard drop with locked bottom: got row %d, want %d", dest.Row, expectedRow-1)
	}
}

func TestCompletedRows(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	lastRow := config.TotalRows - 1
	// Fill last row completely
	for c := 0; c < 10; c++ {
		pf.Rows[lastRow].Cells[c] = Cell{Occupied: true, PieceType: PieceO}
	}
	rows := CompletedRows(pf)
	if len(rows) != 1 || rows[0] != lastRow {
		t.Errorf("completed rows: got %v, want [%d]", rows, lastRow)
	}

	// Row with active cell should not be complete
	pf.Rows[lastRow-1].Cells[0] = Cell{Active: true}
	for c := 1; c < 10; c++ {
		pf.Rows[lastRow-1].Cells[c] = Cell{Occupied: true, PieceType: PieceO}
	}
	rows = CompletedRows(pf)
	if len(rows) != 1 {
		t.Errorf("row with active cell should not be complete, got %v", rows)
	}
}

func TestLevel(t *testing.T) {
	if Level(0) != 0 {
		t.Error("Level(0) should be 0")
	}
	if Level(10) != 1 {
		t.Error("Level(10) should be 1")
	}
	if Level(200) != 19 {
		t.Error("Level(200) should be capped at 19")
	}
}

func TestGravityInterval(t *testing.T) {
	if GravityInterval(0) != 800*time.Millisecond {
		t.Errorf("GravityInterval(0) = %v", GravityInterval(0))
	}
	if GravityInterval(19) != 33*time.Millisecond {
		t.Errorf("GravityInterval(19) = %v", GravityInterval(19))
	}
	if GravityInterval(99) != 33*time.Millisecond {
		t.Error("levels beyond 19 should use 33ms")
	}
}

func TestCellMarshalRoundTrip(t *testing.T) {
	c := Cell{Active: true, PieceType: PieceI, Orientation: 1, AnchorRow: 5, AnchorCol: 7}
	data, err := c.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := UnmarshalCell(data)
	if err != nil {
		t.Fatal(err)
	}
	if c2 != c {
		t.Errorf("round trip mismatch: got %+v, want %+v", c2, c)
	}

	// An empty cell — the vacate payload — encodes as "{}" and decodes back to
	// the zero Cell.
	data, err = Cell{}.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{}" {
		t.Errorf("empty cell encodes as %q, want {}", data)
	}
	c2, err = UnmarshalCell(data)
	if err != nil {
		t.Fatal(err)
	}
	if c2 != (Cell{}) {
		t.Errorf("empty cell round trip: got %+v", c2)
	}
}

func TestPlayfieldApply(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	pf.Apply(5, 0, Cell{Occupied: true, PieceType: PieceI}, 42)
	if pf.CellLastSeq(5, 0) != 42 {
		t.Errorf("CellLastSeq(5,0) = %d, want 42", pf.CellLastSeq(5, 0))
	}
	if !pf.Rows[5].Cells[0].Occupied {
		t.Error("row 5 cell 0 should be occupied after Apply")
	}
	// Same-or-lower sequence is skipped.
	pf.Apply(5, 0, Cell{}, 42)
	if !pf.Rows[5].Cells[0].Occupied {
		t.Error("same-sequence apply should be a no-op")
	}
	// Higher sequence wins.
	pf.Apply(5, 0, Cell{}, 43)
	if pf.Rows[5].Cells[0].Occupied {
		t.Error("higher-sequence apply should overwrite")
	}
}

func TestRotateSRS(t *testing.T) {
	pf := NewPlayfield(config.StandardWidth)
	// T piece at center, rotate CW
	p := Piece{Type: PieceT, Orientation: 0, Row: 10, Col: 4}
	rotated, ok := Rotate(p, true, pf)
	if !ok {
		t.Fatal("T piece should rotate CW in open field")
	}
	if rotated.Orientation != 1 {
		t.Errorf("expected orientation 1, got %d", rotated.Orientation)
	}

	// O piece should not rotate
	p = Piece{Type: PieceO, Orientation: 0, Row: 10, Col: 4}
	_, ok = Rotate(p, true, pf)
	if ok {
		t.Error("O piece should not rotate")
	}

	// Test wall kick: I piece against left wall
	p = Piece{Type: PieceI, Orientation: 0, Row: 10, Col: 0}
	rotated, ok = Rotate(p, true, pf)
	if !ok {
		t.Fatal("I piece against left wall should wall-kick")
	}
}

func TestProjectShrink(t *testing.T) {
	const own = 0
	const causer = 1

	// activeAnchor returns the anchor row of own's active piece, or -1 if none.
	activeAnchor := func(rows []Row) int {
		for _, r := range rows {
			for _, c := range r.Cells {
				if c.Active && c.PlayerIdx == own {
					return c.AnchorRow
				}
			}
		}
		return -1
	}

	// Stays put: piece hovers well above the stack, so a 1-row rise leaves a
	// gap and the piece keeps its row (dropped into place).
	t.Run("stays put when no conflict", func(t *testing.T) {
		pf := NewPlayfield(config.StandardWidth)
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 10, Col: 4}, own)
		pf.Rows[20].Cells[0] = Cell{Occupied: true, PieceType: PieceT} // low stack, far below

		out, topOut := pf.ProjectShrink(1, causer, own)

		if topOut {
			t.Fatal("expected no top-out when the piece has room above the stack")
		}
		if got := activeAnchor(out); got != 10 {
			t.Errorf("piece should hold its row: got anchor %d, want 10", got)
		}
		if !out[19].Cells[0].Occupied || out[19].Cells[0].Adversarial {
			t.Error("locked stack should have shifted up by 1 (row 20 -> 19)")
		}
		bottom := config.TotalRows - 1
		for c := 0; c < config.StandardWidth; c++ {
			if !out[bottom].Cells[c].Adversarial {
				t.Errorf("bottom row cell %d should be adversarial garbage", c)
			}
		}
	})

	// Pushed up minimally: a floor sits directly under the piece, so a 1-row
	// rise overlaps it and the piece is lifted by exactly 1 to rest on top.
	t.Run("pushed up minimally on conflict", func(t *testing.T) {
		pf := NewPlayfield(config.StandardWidth)
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 10, Col: 4}, own)
		pf.Rows[12].Cells[4] = Cell{Occupied: true, PieceType: PieceT}
		pf.Rows[12].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topOut := pf.ProjectShrink(1, causer, own)

		if topOut {
			t.Fatal("expected no top-out: piece can rest one row higher")
		}
		if got := activeAnchor(out); got != 9 {
			t.Errorf("piece should be pushed up by exactly 1: got anchor %d, want 9", got)
		}
	})

	// Tops out: piece is pinned against the ceiling with a floor right under
	// it, so the only conflict-free lift would push a cell above row 0.
	t.Run("tops out when pushed off the top", func(t *testing.T) {
		pf := NewPlayfield(config.StandardWidth)
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 0, Col: 4}, own)
		pf.Rows[2].Cells[4] = Cell{Occupied: true, PieceType: PieceT}
		pf.Rows[2].Cells[5] = Cell{Occupied: true, PieceType: PieceT}

		out, topOut := pf.ProjectShrink(1, causer, own)

		if !topOut {
			t.Fatal("expected top-out when the piece is squeezed off the top")
		}
		if got := activeAnchor(out); got != -1 {
			t.Errorf("doomed piece should not be stamped: found active anchor %d", got)
		}
	})
}
