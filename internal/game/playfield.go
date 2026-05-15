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

// Projection helpers below build new Row values without mutating pf. They are
// used by the engine to compute the row payloads that should be published to
// NATS; the in-memory playfield is only updated when the consumer echoes the
// published rows back via Apply.

func projectClearActiveCellsForPlayer(src Row, playerIdx int) Row {
	cells := make([]Cell, len(src.Cells))
	copy(cells, src.Cells)
	for j := range cells {
		if cells[j].Active && cells[j].PlayerIdx == playerIdx {
			cells[j] = Cell{}
		}
	}
	return Row{Cells: cells}
}

func projectLockActiveCellsForPlayer(src Row, playerIdx int) Row {
	cells := make([]Cell, len(src.Cells))
	copy(cells, src.Cells)
	for j := range cells {
		c := &cells[j]
		if c.Active && c.PlayerIdx == playerIdx {
			c.Active = false
			c.Occupied = true
			c.Orientation = 0
			c.AnchorRow = 0
			c.AnchorCol = 0
			// PlayerIdx preserved so locked cells retain player color.
		}
	}
	return Row{Cells: cells}
}

func projectPlaceActivePieceCells(rows map[int]Row, p Piece, playerIdx int, width int) {
	for _, c := range p.Cells() {
		r, col := c[0], c[1]
		row, ok := rows[r]
		if !ok || col < 0 || col >= width {
			continue
		}
		row.Cells[col].Active = true
		row.Cells[col].PieceType = p.Type
		row.Cells[col].Orientation = p.Orientation
		row.Cells[col].AnchorRow = p.Row
		row.Cells[col].AnchorCol = p.Col
		row.Cells[col].PlayerIdx = playerIdx
		rows[r] = row
	}
}

// ProjectMove returns rowIdx -> Row showing the result of clearing playerIdx's
// active cells from each affected row and (if newPiece != nil) placing the new
// piece's active cells on top. The playfield is not mutated.
func (pf *Playfield) ProjectMove(affectedRows []int, newPiece *Piece, playerIdx int) map[int]Row {
	out := make(map[int]Row, len(affectedRows))
	for _, r := range affectedRows {
		if r < 0 || r >= pf.Height {
			continue
		}
		out[r] = projectClearActiveCellsForPlayer(pf.Rows[r], playerIdx)
	}
	if newPiece != nil {
		projectPlaceActivePieceCells(out, *newPiece, playerIdx, pf.Width)
	}
	return out
}

// ProjectLock returns rowIdx -> Row with the player's active cells converted
// to locked cells in each affected row. The playfield is not mutated.
func (pf *Playfield) ProjectLock(affectedRows []int, playerIdx int) map[int]Row {
	out := make(map[int]Row, len(affectedRows))
	for _, r := range affectedRows {
		if r < 0 || r >= pf.Height {
			continue
		}
		out[r] = projectLockActiveCellsForPlayer(pf.Rows[r], playerIdx)
	}
	return out
}

// ProjectHardDrop returns rowIdx -> Row showing the result of clearing
// playerIdx's old active cells from `affectedRows` and placing `dest` either as
// locked cells (lockOnLand=true) or as active cells (lockOnLand=false).
func (pf *Playfield) ProjectHardDrop(affectedRows []int, dest Piece, playerIdx int, lockOnLand bool) map[int]Row {
	out := make(map[int]Row, len(affectedRows))
	for _, r := range affectedRows {
		if r < 0 || r >= pf.Height {
			continue
		}
		out[r] = projectClearActiveCellsForPlayer(pf.Rows[r], playerIdx)
	}
	for _, c := range dest.Cells() {
		r, col := c[0], c[1]
		row, ok := out[r]
		if !ok || col < 0 || col >= pf.Width {
			continue
		}
		if lockOnLand {
			row.Cells[col] = Cell{
				Occupied:  true,
				PieceType: dest.Type,
				PlayerIdx: playerIdx,
			}
		} else {
			row.Cells[col] = Cell{
				Active:      true,
				PieceType:   dest.Type,
				Orientation: dest.Orientation,
				AnchorRow:   dest.Row,
				AnchorCol:   dest.Col,
				PlayerIdx:   playerIdx,
			}
		}
		out[r] = row
	}
	return out
}

// ProjectClearRows returns the full set of rows after removing `completed`
// rows and shifting non-cleared rows down (empty rows prepended to top).
// If shiftAnchors is true, all active cells in the returned rows have
// AnchorRow incremented by len(completed) (used in cooperative mode where
// other players' active pieces need anchor adjustment after the shift).
func (pf *Playfield) ProjectClearRows(completed []int, shiftAnchors bool) []Row {
	cleared := make(map[int]bool, len(completed))
	for _, r := range completed {
		cleared[r] = true
	}
	var newRows []Row
	for i := 0; i < pf.Height; i++ {
		if !cleared[i] {
			cells := make([]Cell, pf.Width)
			copy(cells, pf.Rows[i].Cells)
			newRows = append(newRows, Row{Cells: cells})
		}
	}
	for len(newRows) < pf.Height {
		newRows = append([]Row{NewRow(pf.Width)}, newRows...)
	}
	if shiftAnchors {
		for i := range newRows {
			for j := range newRows[i].Cells {
				if newRows[i].Cells[j].Active {
					newRows[i].Cells[j].AnchorRow += len(completed)
				}
			}
		}
	}
	return newRows
}

// ProjectShrink returns the full new set of rows after shifting the playfield
// up by rowsToAdd (existing pieces move up) and adding rowsToAdd permanent
// adversarial rows tagged with causerIdx at the bottom. AnchorRow values in
// remaining active cells are decremented by rowsToAdd.
func (pf *Playfield) ProjectShrink(rowsToAdd int, causerIdx int) []Row {
	out := make([]Row, pf.Height)
	for i := 0; i < pf.Height-rowsToAdd; i++ {
		cells := make([]Cell, pf.Width)
		copy(cells, pf.Rows[i+rowsToAdd].Cells)
		out[i] = Row{Cells: cells}
	}
	for i := pf.Height - rowsToAdd; i < pf.Height; i++ {
		cells := make([]Cell, pf.Width)
		for c := range cells {
			cells[c] = Cell{
				Occupied:    true,
				PieceType:   PieceO,
				Adversarial: true,
				PlayerIdx:   causerIdx,
			}
		}
		out[i] = Row{Cells: cells}
	}
	for i := range out {
		for j := range out[i].Cells {
			c := &out[i].Cells[j]
			if c.Active {
				c.AnchorRow -= rowsToAdd
			}
		}
	}
	return out
}
