package game

import "testing"

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
