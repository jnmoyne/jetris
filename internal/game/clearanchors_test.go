package game

import (
	"sort"
	"testing"
)

// activeCellsFor collects the [row,col] positions of playerIdx's active cells.
func activeCellsFor(rows []Row, playerIdx int) [][2]int {
	var out [][2]int
	for r := range rows {
		for c := range rows[r].Cells {
			cc := rows[r].Cells[c]
			if cc.Active && cc.PlayerIdx == playerIdx {
				out = append(out, [2]int{r, c})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i][0] != out[j][0] {
			return out[i][0] < out[j][0]
		}
		return out[i][1] < out[j][1]
	})
	return out
}

// anchorAgreesWithCells verifies the invariant the piece's owner depends on:
// reconstructing the piece from any active cell's anchor must land exactly on
// the actual active cells. A diverged anchor hands the owner a phantom origin
// — it then moves/vacates the wrong cells and its real piece freezes on every
// client (the teams-mode frozen-agent bug).
func anchorAgreesWithCells(t *testing.T, rows []Row, playerIdx int) {
	t.Helper()
	cells := activeCellsFor(rows, playerIdx)
	if len(cells) == 0 {
		t.Fatal("no active cells found for player")
	}
	at := cells[0]
	cc := rows[at[0]].Cells[at[1]]
	rebuilt := Piece{Type: cc.PieceType, Orientation: cc.Orientation, Row: cc.AnchorRow, Col: cc.AnchorCol}
	want := rebuilt.Cells()
	sort.Slice(want, func(i, j int) bool {
		if want[i][0] != want[j][0] {
			return want[i][0] < want[j][0]
		}
		return want[i][1] < want[j][1]
	})
	if len(want) != len(cells) {
		t.Fatalf("anchor-derived piece has %d cells, board has %d active cells", len(want), len(cells))
	}
	for i := range want {
		if want[i] != cells[i] {
			t.Fatalf("anchor-derived cells %v disagree with board active cells %v", want, cells)
		}
	}
}

// TestProjectClearRowsAnchors pins the per-piece anchor rule for shared-board
// clears: each active piece's anchor shifts by the number of cleared rows
// below it — NOT a blanket len(completed). The old blanket bump over-shifted
// any piece trapped below (or between) the cleared rows, desynchronizing its
// anchor from its cells.
func TestProjectClearRowsAnchors(t *testing.T) {
	const owner = 1
	newBoard := func() *Playfield { return NewPlayfieldWithHeight(10, 12) }
	fillRow := func(pf *Playfield, r int) {
		for c := 0; c < pf.Width; c++ {
			pf.Rows[r].Cells[c] = Cell{Occupied: true, PieceType: PieceT}
		}
	}

	t.Run("piece above the clear shifts down with the stack", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 2, Col: 4}, owner) // cells rows 2-3
		fillRow(pf, 8)

		out := pf.ProjectClearRows([]int{8}, true)

		if got := cascadeAnchorFor(out, owner); got != 3 {
			t.Errorf("anchor should shift down by 1: got %d, want 3", got)
		}
		want := [][2]int{{3, 4}, {3, 5}, {4, 4}, {4, 5}}
		if got := activeCellsFor(out, owner); len(got) != 4 || got[0] != want[0] || got[3] != want[3] {
			t.Errorf("cells should shift down by 1: got %v, want %v", got, want)
		}
		anchorAgreesWithCells(t, out, owner)
	})

	t.Run("trapped piece below the clear keeps cells and anchor", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 9, Col: 4}, owner) // cells rows 9-10
		fillRow(pf, 8)                                                         // full row ABOVE the piece

		out := pf.ProjectClearRows([]int{8}, true)

		if got := cascadeAnchorFor(out, owner); got != 9 {
			t.Errorf("trapped piece's anchor must not move: got %d, want 9", got)
		}
		want := [][2]int{{9, 4}, {9, 5}, {10, 4}, {10, 5}}
		if got := activeCellsFor(out, owner); len(got) != 4 || got[0] != want[0] || got[3] != want[3] {
			t.Errorf("trapped piece's cells must not move: got %v, want %v", got, want)
		}
		anchorAgreesWithCells(t, out, owner)
	})

	t.Run("piece between cleared rows shifts by the rows below only", func(t *testing.T) {
		pf := newBoard()
		pf.SetActivePieceForPlayer(Piece{Type: PieceO, Row: 8, Col: 4}, owner) // cells rows 8-9
		fillRow(pf, 6)
		fillRow(pf, 11)

		out := pf.ProjectClearRows([]int{6, 11}, true)

		if got := cascadeAnchorFor(out, owner); got != 9 {
			t.Errorf("anchor should shift by 1 (one cleared row below): got %d, want 9", got)
		}
		anchorAgreesWithCells(t, out, owner)
	})
}
