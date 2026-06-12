package game

import "testing"

// buildTeamBoard returns a 20-wide, 12-row board with a locked L-cell stack on
// the bottom row and two active pieces: player 0's T near the bottom-left and
// player 1's T higher up on the right half.
func buildTeamBoard() *Playfield {
	pf := NewPlayfieldWithHeight(20, 12)
	bottom := pf.Height - 1
	for c := 0; c < pf.Width; c++ {
		if c%3 != 0 { // leave holes so the row is not complete
			pf.Rows[bottom].Cells[c] = Cell{Occupied: true, PieceType: PieceL, PlayerIdx: 0}
		}
	}
	pf.SetActivePieceForPlayer(Piece{Type: PieceT, Row: 8, Col: 2}, 0)
	pf.SetActivePieceForPlayer(Piece{Type: PieceT, Row: 4, Col: 12}, 1)
	return pf
}

func activeCellsForPlayer(pf *Playfield, rows []Row, playerIdx int) map[[2]int]bool {
	out := map[[2]int]bool{}
	for r := range rows {
		for c, cell := range rows[r].Cells {
			if cell.Active && cell.PlayerIdx == playerIdx {
				out[[2]int{r, c}] = true
			}
		}
	}
	return out
}

func TestProjectShrinkSharedHoldsAllPiecesInPlace(t *testing.T) {
	pf := buildTeamBoard()
	rowsToAdd := 2

	beforeP0 := activeCellsForPlayer(pf, pf.Rows, 0)
	beforeP1 := activeCellsForPlayer(pf, pf.Rows, 1)

	out := pf.ProjectShrinkShared(rowsToAdd, 1)

	// Every active cell stays at its exact pre-shrink position — no piece is
	// lifted or carried by the shift, regardless of owner.
	afterP0 := activeCellsForPlayer(pf, out, 0)
	afterP1 := activeCellsForPlayer(pf, out, 1)
	if len(afterP0) != len(beforeP0) {
		t.Fatalf("player 0 active cell count changed: %d -> %d", len(beforeP0), len(afterP0))
	}
	for pos := range beforeP0 {
		if !afterP0[pos] {
			t.Errorf("player 0 active cell %v moved during shrink", pos)
		}
	}
	for pos := range beforeP1 {
		if !afterP1[pos] {
			t.Errorf("player 1 active cell %v moved during shrink", pos)
		}
	}

	// The locked stack shifted up by rowsToAdd: the old bottom row's pattern is
	// now rowsToAdd higher (except where an active overlay covers it).
	oldBottom := pf.Height - 1
	newRow := oldBottom - rowsToAdd
	for c := 0; c < pf.Width; c++ {
		want := pf.Rows[oldBottom].Cells[c]
		got := out[newRow].Cells[c]
		if got.Active {
			continue // an overlaid active cell may cover a shifted cell
		}
		if got.Occupied != want.Occupied {
			t.Errorf("col %d: shifted cell occupied=%v, want %v", c, got.Occupied, want.Occupied)
		}
	}

	// The bottom rowsToAdd rows are adversarial garbage tagged with the causer,
	// except where an active piece was overlaid in place.
	for r := pf.Height - rowsToAdd; r < pf.Height; r++ {
		for c, cell := range out[r].Cells {
			if cell.Active {
				continue
			}
			if !cell.Adversarial || !cell.Occupied || cell.PlayerIdx != 1 {
				t.Errorf("garbage row %d col %d: got %+v, want adversarial causer-1 cell", r, c, cell)
			}
		}
	}
}

func TestAdversarialRowCount(t *testing.T) {
	pf := NewPlayfieldWithHeight(20, 12)
	if got := pf.AdversarialRowCount(); got != 0 {
		t.Fatalf("empty board: AdversarialRowCount = %d, want 0", got)
	}

	// Two garbage rows at the bottom.
	for r := pf.Height - 2; r < pf.Height; r++ {
		for c := range pf.Rows[r].Cells {
			pf.Rows[r].Cells[c] = Cell{Occupied: true, Adversarial: true, PlayerIdx: 1}
		}
	}
	if got := pf.AdversarialRowCount(); got != 2 {
		t.Fatalf("AdversarialRowCount = %d, want 2", got)
	}

	// A garbage row keeps counting even when a crushed piece left holes or
	// locked cells in it (the documented shared-board skip artifact): only the
	// presence of at least one adversarial cell matters.
	pf.Rows[pf.Height-1].Cells[3] = Cell{}
	pf.Rows[pf.Height-1].Cells[4] = Cell{Occupied: true, PieceType: PieceT, PlayerIdx: 0}
	if got := pf.AdversarialRowCount(); got != 2 {
		t.Fatalf("AdversarialRowCount with holes = %d, want 2", got)
	}

	// A locked (non-adversarial) row above the garbage block ends the count.
	for c := range pf.Rows[pf.Height-3].Cells {
		pf.Rows[pf.Height-3].Cells[c] = Cell{Occupied: true, PieceType: PieceL}
	}
	if got := pf.AdversarialRowCount(); got != 2 {
		t.Fatalf("AdversarialRowCount with stack above = %d, want 2", got)
	}
}
