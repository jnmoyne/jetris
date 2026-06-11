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

// Clone returns a deep copy of the playfield. The Rows and LastSeq slices are
// copied so the result can be read without holding the lock that guards the
// original — used by the engine's accessors to hand a race-free snapshot to the
// UI and tests while the consumer/publish goroutines keep mutating the live one.
func (pf *Playfield) Clone() *Playfield {
	lastSeq := make([]uint64, len(pf.LastSeq))
	copy(lastSeq, pf.LastSeq)
	return &Playfield{
		Width:   pf.Width,
		Height:  pf.Height,
		Rows:    CloneRows(pf.Rows),
		LastSeq: lastSeq,
	}
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

// Apply updates the playfield from a decoded row message and is the single
// reconciliation point for both the consumer echo and the engine's own publish
// write-through. A message is applied only if its sequence is HIGHER than the
// row's current LastSeq; a message with the same-or-lower sequence is skipped.
//
// This makes the two sources converge correctly: when the engine commits a
// write it write-throughs the committed row here with the sequence inferred from
// the commit ack; the same row is later echoed back by the consumer with the
// SAME sequence, which is skipped (same-or-lower) — a harmless no-op. Only a
// strictly higher sequence (e.g. the other player's write in cooperative mode,
// or a NoCAS line-clear/shrink we did not originate) updates in-memory state.
func (pf *Playfield) Apply(rowIndex int, row Row, seq uint64) {
	if rowIndex < 0 || rowIndex >= pf.Height {
		return
	}
	if seq > 0 && seq <= pf.LastSeq[rowIndex] {
		return // same-or-lower sequence: already have it, skip
	}
	pf.Rows[rowIndex] = row
	pf.LastSeq[rowIndex] = seq
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

// ProjectShrink returns the full new set of rows after a competitive shrink:
// the locked stack shifts up by rowsToAdd and rowsToAdd permanent adversarial
// rows tagged with causerIdx are added at the bottom.
//
// The falling piece belonging to ownPlayerIdx holds its on-screen position
// while the stack rises beneath it ("dropped into place"). It is pushed up only
// when the risen stack or garbage would overlap it, and only by the minimum
// number of rows needed to clear the conflict (0..rowsToAdd). If no amount of
// lift keeps the piece on the board (the only way to avoid the overlap is to
// push a cell above the top), the second return value is true to signal that
// the player has been squeezed out and tops out.
//
// rowsToAdd is a sufficient search bound for the lift: the piece was placeable
// at its old anchor, and the new locked cells are exactly the old ones shifted
// up by rowsToAdd, so the piece at anchor-rowsToAdd has identical geometry
// relative to the stack and always clears it — the only remaining failure at
// that lift is a cell crossing row 0, which is the top-out case.
func (pf *Playfield) ProjectShrink(rowsToAdd, causerIdx, ownPlayerIdx int) ([]Row, bool) {
	// Capture the falling piece before the shift; it is re-placed below.
	piece := pf.ActivePieceForPlayer(ownPlayerIdx)

	out := make([]Row, pf.Height)
	// Shift the stack up by rowsToAdd, stripping active cells (the piece is
	// re-stamped at its resolved position, not carried along by the shift).
	for i := 0; i < pf.Height-rowsToAdd; i++ {
		cells := make([]Cell, pf.Width)
		for j, c := range pf.Rows[i+rowsToAdd].Cells {
			if !c.Active {
				cells[j] = c
			}
		}
		out[i] = Row{Cells: cells}
	}
	// Permanent adversarial garbage fills the bottom rows.
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

	if piece == nil {
		return out, false
	}

	// Keep the piece where it is (k=0) unless the risen stack/garbage overlaps
	// it; then lift it the minimum needed to rest on top of the new stack.
	tmp := &Playfield{Width: pf.Width, Height: pf.Height, Rows: out}
	for k := 0; k <= rowsToAdd; k++ {
		cand := *piece
		cand.Row = piece.Row - k
		if CanPlace(cand, tmp) {
			tmp.SetActivePieceForPlayer(cand, ownPlayerIdx)
			return out, false
		}
	}
	// No lift keeps the piece on the board: it is pushed off the top → top-out.
	// Leave the doomed piece unstamped so the board shows the risen stack only.
	return out, true
}
