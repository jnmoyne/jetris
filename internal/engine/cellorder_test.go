package engine

import (
	"testing"

	"jetricks/internal/game"
)

// TestOrderedCellKeys verifies the category publish order — active cells
// first, locked cells second, empty (vacate) cells last — with a
// deterministic ascending (row, col) tie-break within each category. This
// order is what prevents a relocating piece from transiently having zero
// active cells (spurious lock-in) and guarantees a lock's landing cells are
// applied before lock-in fires.
func TestOrderedCellKeys(t *testing.T) {
	active := game.Cell{Active: true, PieceType: game.PieceI, PlayerIdx: 0}
	locked := game.Cell{Occupied: true, PieceType: game.PieceL}
	empty := game.Cell{}

	// A horizontal-I downward relocate: new active cells on row 6, vacated old
	// positions on row 5, plus a locked cell thrown in.
	cells := map[game.CellPos]game.Cell{
		{Row: 5, Col: 3}: empty,
		{Row: 6, Col: 4}: active,
		{Row: 5, Col: 4}: empty,
		{Row: 7, Col: 0}: locked,
		{Row: 6, Col: 3}: active,
		{Row: 5, Col: 6}: empty,
		{Row: 6, Col: 6}: active,
		{Row: 6, Col: 5}: active,
		{Row: 5, Col: 5}: empty,
	}

	got := orderedCellKeys(cells)
	want := []game.CellPos{
		{Row: 6, Col: 3}, {Row: 6, Col: 4}, {Row: 6, Col: 5}, {Row: 6, Col: 6}, // active
		{Row: 7, Col: 0},                                                       // locked
		{Row: 5, Col: 3}, {Row: 5, Col: 4}, {Row: 5, Col: 5}, {Row: 5, Col: 6}, // vacates
	}
	if len(got) != len(want) {
		t.Fatalf("got %d keys, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("key %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDiffCells(t *testing.T) {
	cur := []game.Row{game.NewRow(4), game.NewRow(4), game.NewRow(4)}
	cur[1].Cells[1] = game.Cell{Active: true, PieceType: game.PieceT, PlayerIdx: 0}
	cur[1].Cells[2] = game.Cell{Occupied: true, PieceType: game.PieceL}

	// Projection: the active cell moves from (1,1) to (2,1); the locked cell
	// and everything else is unchanged.
	proj1 := cur[1].Clone()
	proj1.Cells[1] = game.Cell{}
	proj2 := cur[2].Clone()
	proj2.Cells[1] = game.Cell{Active: true, PieceType: game.PieceT, PlayerIdx: 0}
	projected := map[int]game.Row{1: proj1, 2: proj2}

	got := diffCells(cur, projected)
	if len(got) != 2 {
		t.Fatalf("got %d changed cells, want 2: %+v", len(got), got)
	}
	if c, ok := got[game.CellPos{Row: 1, Col: 1}]; !ok || c != (game.Cell{}) {
		t.Errorf("expected vacate at (1,1), got %+v (present=%v)", c, ok)
	}
	if c, ok := got[game.CellPos{Row: 2, Col: 1}]; !ok || !c.Active {
		t.Errorf("expected active cell at (2,1), got %+v (present=%v)", c, ok)
	}
}

func TestChangedCells(t *testing.T) {
	cur := []game.Row{game.NewRow(3), game.NewRow(3), game.NewRow(3)}
	cur[2].Cells[0] = game.Cell{Occupied: true, PieceType: game.PieceJ}

	projected := game.CloneRows(cur)
	projected[2].Cells[0] = game.Cell{}                                       // cleared
	projected[2].Cells[2] = game.Cell{Occupied: true, PieceType: game.PieceS} // shifted in

	// Row 0 excluded by the range; rows 1-2 diffed.
	got := changedCells(cur, projected, 1, 3)
	if len(got) != 2 {
		t.Fatalf("got %d changed cells, want 2: %+v", len(got), got)
	}
	if _, ok := got[game.CellPos{Row: 2, Col: 0}]; !ok {
		t.Error("expected change at (2,0)")
	}
	if _, ok := got[game.CellPos{Row: 2, Col: 2}]; !ok {
		t.Error("expected change at (2,2)")
	}
}
