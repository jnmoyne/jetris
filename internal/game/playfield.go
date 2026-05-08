package game

import "jetricks/internal/config"

// TotalRows is kept for backward compatibility but NewPlayfieldWithHeight should be preferred.
const TotalRows = config.TotalRows

// Playfield is the in-memory representation of the game board.
type Playfield struct {
	Width   int
	Height  int // total rows (headroom + visible)
	Rows    []Row
	LastSeq []uint64
}

// NewPlayfield creates an empty playfield with the default TotalRows height.
func NewPlayfield(width int) *Playfield {
	return NewPlayfieldWithHeight(width, TotalRows)
}

// NewPlayfieldWithHeight creates an empty playfield with a specific height.
func NewPlayfieldWithHeight(width, height int) *Playfield {
	pf := &Playfield{
		Width:   width,
		Height:  height,
		Rows:    make([]Row, height),
		LastSeq: make([]uint64, height),
	}
	for i := range pf.Rows {
		pf.Rows[i] = NewRow(width)
	}
	return pf
}

// Apply updates the playfield from a decoded row message.
// Stale messages (with a sequence <= the current LastSeq for that row)
// are ignored. This prevents old consumer messages from overwriting
// state that was already updated by a NoCAS publish (e.g. line clears).
func (pf *Playfield) Apply(rowIndex int, row Row, seq uint64) {
	if rowIndex < 0 || rowIndex >= pf.Height {
		return
	}
	if seq > 0 && seq <= pf.LastSeq[rowIndex] {
		return // stale message, skip
	}
	pf.Rows[rowIndex] = row
	pf.LastSeq[rowIndex] = seq
}

// ActivePiece scans all rows for cells with Active==true and reconstructs
// the Piece from the first active cell found.
func (pf *Playfield) ActivePiece() *Piece {
	for _, row := range pf.Rows {
		for _, c := range row.Cells {
			if c.Active {
				return &Piece{
					Type:        c.PieceType,
					Orientation: c.Orientation,
					Row:         c.AnchorRow,
					Col:         c.AnchorCol,
				}
			}
		}
	}
	return nil
}

// Snapshot returns a copy of LastSeq for CAS expectations.
func (pf *Playfield) Snapshot() []uint64 {
	out := make([]uint64, len(pf.LastSeq))
	copy(out, pf.LastSeq)
	return out
}

// SetActivePiece places a piece's active cells on the playfield.
// Does not modify locked cells. Clears any existing active cells first.
func (pf *Playfield) SetActivePiece(p Piece) {
	pf.ClearActiveCells()
	for _, cell := range p.Cells() {
		r, c := cell[0], cell[1]
		if r >= 0 && r < pf.Height && c >= 0 && c < pf.Width {
			pf.Rows[r].Cells[c].Active = true
			pf.Rows[r].Cells[c].PieceType = p.Type
			pf.Rows[r].Cells[c].Orientation = p.Orientation
			pf.Rows[r].Cells[c].AnchorRow = p.Row
			pf.Rows[r].Cells[c].AnchorCol = p.Col
		}
	}
}

// ClearActiveCells removes all active piece cells from the playfield.
func (pf *Playfield) ClearActiveCells() {
	for i := range pf.Rows {
		for j := range pf.Rows[i].Cells {
			if pf.Rows[i].Cells[j].Active {
				pf.Rows[i].Cells[j] = Cell{}
			}
		}
	}
}

// LockActivePiece converts all active cells to occupied (locked) cells.
func (pf *Playfield) LockActivePiece() {
	for i := range pf.Rows {
		for j := range pf.Rows[i].Cells {
			c := &pf.Rows[i].Cells[j]
			if c.Active {
				c.Active = false
				c.Occupied = true
				// Keep PieceType for coloring, clear active-specific fields
				c.Orientation = 0
				c.AnchorRow = 0
				c.AnchorCol = 0
			}
		}
	}
}

// ActivePieceForPlayer returns the active piece belonging to the given playerIdx.
// Used in cooperative mode where two players' pieces coexist on the same playfield.
func (pf *Playfield) ActivePieceForPlayer(playerIdx int) *Piece {
	for _, row := range pf.Rows {
		for _, c := range row.Cells {
			if c.Active && c.PlayerIdx == playerIdx {
				return &Piece{
					Type:        c.PieceType,
					Orientation: c.Orientation,
					Row:         c.AnchorRow,
					Col:         c.AnchorCol,
				}
			}
		}
	}
	return nil
}

// SetActivePieceForPlayer clears only active cells with matching PlayerIdx
// before writing the new piece's active cells tagged with playerIdx.
func (pf *Playfield) SetActivePieceForPlayer(p Piece, playerIdx int) {
	pf.ClearActiveCellsForPlayer(playerIdx)
	for _, cell := range p.Cells() {
		r, c := cell[0], cell[1]
		if r >= 0 && r < pf.Height && c >= 0 && c < pf.Width {
			pf.Rows[r].Cells[c].Active = true
			pf.Rows[r].Cells[c].PieceType = p.Type
			pf.Rows[r].Cells[c].Orientation = p.Orientation
			pf.Rows[r].Cells[c].AnchorRow = p.Row
			pf.Rows[r].Cells[c].AnchorCol = p.Col
			pf.Rows[r].Cells[c].PlayerIdx = playerIdx
		}
	}
}

// ClearActiveCellsForPlayer removes active cells belonging to the given playerIdx only.
func (pf *Playfield) ClearActiveCellsForPlayer(playerIdx int) {
	for i := range pf.Rows {
		for j := range pf.Rows[i].Cells {
			if pf.Rows[i].Cells[j].Active && pf.Rows[i].Cells[j].PlayerIdx == playerIdx {
				pf.Rows[i].Cells[j] = Cell{}
			}
		}
	}
}

// LockActivePieceForPlayer converts active cells with matching PlayerIdx to locked.
func (pf *Playfield) LockActivePieceForPlayer(playerIdx int) {
	for i := range pf.Rows {
		for j := range pf.Rows[i].Cells {
			c := &pf.Rows[i].Cells[j]
			if c.Active && c.PlayerIdx == playerIdx {
				c.Active = false
				c.Occupied = true
				c.Orientation = 0
				c.AnchorRow = 0
				c.AnchorCol = 0
				// PlayerIdx is intentionally preserved so locked cells
				// retain the player's color for outline rendering.
			}
		}
	}
}

// RowsWithActiveCellsForPlayer returns row indices containing active cells for the given playerIdx.
func (pf *Playfield) RowsWithActiveCellsForPlayer(playerIdx int) []int {
	var rows []int
	for i := range pf.Rows {
		for _, c := range pf.Rows[i].Cells {
			if c.Active && c.PlayerIdx == playerIdx {
				rows = append(rows, i)
				break
			}
		}
	}
	return rows
}

// RowsWithActiveCells returns the indices of rows containing active piece cells.
func (pf *Playfield) RowsWithActiveCells() []int {
	var rows []int
	for i := range pf.Rows {
		for _, c := range pf.Rows[i].Cells {
			if c.Active {
				rows = append(rows, i)
				break
			}
		}
	}
	return rows
}
