package game

import (
	"sort"

	"jetris/internal/config"
)

// TotalRows is kept for backward compatibility but NewPlayfieldWithHeight should be preferred.
const TotalRows = config.TotalRows

// Playfield is the in-memory representation of the game board. LastSeq tracks
// the stream sequence of the last message applied to each cell, flat row-major
// (index = row*Width + col — see seqIdx).
type Playfield struct {
	Width   int
	Height  int // total rows (headroom + visible)
	Rows    []Row
	LastSeq []uint64
}

// seqIdx returns the flat LastSeq index of cell (row, col).
func (pf *Playfield) seqIdx(row, col int) int {
	return row*pf.Width + col
}

// CellLastSeq returns the stream sequence of the last message applied to cell
// (row, col) — the per-subject CAS expectation for that cell.
func (pf *Playfield) CellLastSeq(row, col int) uint64 {
	return pf.LastSeq[pf.seqIdx(row, col)]
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
		LastSeq: make([]uint64, width*height),
	}
	for i := range pf.Rows {
		pf.Rows[i] = NewRow(width)
	}
	return pf
}

// Apply updates the playfield from a decoded cell message and is the single
// reconciliation point for both the consumer echo and the engine's own publish
// write-through. A message is applied only if its sequence is HIGHER than the
// cell's current LastSeq; a message with the same-or-lower sequence is skipped.
//
// This makes the two sources converge correctly: when the engine commits a
// write it write-throughs the committed cell here with the sequence inferred
// from the commit ack; the same cell is later echoed back by the consumer with
// the SAME sequence, which is skipped (same-or-lower) — a harmless no-op. Only
// a strictly higher sequence (e.g. the other player's write in cooperative
// mode, or a NoCAS line-clear/shrink we did not originate) updates in-memory
// state.
func (pf *Playfield) Apply(row, col int, cell Cell, seq uint64) {
	if row < 0 || row >= pf.Height || col < 0 || col >= pf.Width {
		return
	}
	idx := pf.seqIdx(row, col)
	if seq > 0 && seq <= pf.LastSeq[idx] {
		return // same-or-lower sequence: already have it, skip
	}
	pf.Rows[row].Cells[col] = cell
	pf.LastSeq[idx] = seq
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
// If shiftAnchors is true, active cells in the returned rows have AnchorRow
// incremented by the number of cleared rows BELOW that cell — exactly how far
// the collapse moves it — so on shared boards other players' pieces keep an
// anchor that agrees with their cells. The shift is per cell, NOT a blanket
// len(completed): a piece trapped below (or between) the cleared rows moves
// less than the rows above them, and over-shifting its anchor hands its owner
// a phantom origin — the owner then moves/vacates the wrong cells and its
// real piece freezes on every client's board. Per-cell is per-piece safe: a
// full row can never contain an active cell and no tetromino has a vertical
// gap, so all cells of one piece sit on the same side of every cleared row
// and receive the same shift.
func (pf *Playfield) ProjectClearRows(completed []int, shiftAnchors bool) []Row {
	cleared := make(map[int]bool, len(completed))
	for _, r := range completed {
		cleared[r] = true
	}
	var newRows []Row
	for i := 0; i < pf.Height; i++ {
		if cleared[i] {
			continue
		}
		cells := make([]Cell, pf.Width)
		copy(cells, pf.Rows[i].Cells)
		if shiftAnchors {
			below := 0
			for _, r := range completed {
				if r > i {
					below++
				}
			}
			if below > 0 {
				for j := range cells {
					if cells[j].Active {
						cells[j].AnchorRow += below
					}
				}
			}
		}
		newRows = append(newRows, Row{Cells: cells})
	}
	for len(newRows) < pf.Height {
		newRows = append([]Row{NewRow(pf.Width)}, newRows...)
	}
	return newRows
}

// ProjectShrinkCascade returns the full new set of rows after a garbage raise
// on any board (competitive or shared): the locked stack shifts up by
// rowsToAdd and rowsToAdd permanent adversarial rows tagged with causerIdx are
// added at the bottom.
//
// Every falling piece on the board — whoever owns it — holds its on-screen
// position while the stack rises beneath it ("dropped into place"). A piece is
// pushed up only when the risen stack, the new garbage, or another falling
// piece would overlap it, and only by the minimum number of rows needed to
// clear the conflict. Lifts cascade: pieces are resolved bottom-most first, and
// a piece already lifted is an obstacle for the pieces above it, so a rising
// stack can push a whole column of stacked pieces upward. A piece is never
// merged into the risen stack.
//
// topped lists the players whose pieces could not be kept on the board (the
// only conflict-free lift would push a cell above row 0); their pieces are left
// unstamped so the board shows the risen stack only, and the engine eliminates
// them. boardFull is true when the shift pushed a LOCKED cell past the top of
// the board (the stack itself no longer fits): the engine eliminates the
// board's owner (competitive) or every remaining player on it (teams).
// Competitive boards are simply the one-piece case of the same transform.
func (pf *Playfield) ProjectShrinkCascade(rowsToAdd, causerIdx int) ([]Row, []int, bool) {
	if rowsToAdd <= 0 {
		return CloneRows(pf.Rows), nil, false
	}

	// The stack no longer fits if any LOCKED cell sits in the rows the shift
	// pushes past the top. Active cells don't count — pieces get lifted (or
	// topped) individually below.
	boardFull := false
	for i := 0; i < rowsToAdd && i < pf.Height; i++ {
		for _, c := range pf.Rows[i].Cells {
			if c.Occupied && !c.Active {
				boardFull = true
				break
			}
		}
		if boardFull {
			break
		}
	}

	// Capture every falling piece before the shift; each is re-placed below.
	type fallingPiece struct {
		playerIdx int
		piece     Piece
		lowestRow int
	}
	var pieces []fallingPiece
	seen := make(map[int]bool)
	for _, row := range pf.Rows {
		for _, c := range row.Cells {
			if c.Active && !seen[c.PlayerIdx] {
				seen[c.PlayerIdx] = true
				p := Piece{Type: c.PieceType, Orientation: c.Orientation, Row: c.AnchorRow, Col: c.AnchorCol}
				lowest := 0
				for _, cell := range p.Cells() {
					if cell[0] > lowest {
						lowest = cell[0]
					}
				}
				pieces = append(pieces, fallingPiece{playerIdx: c.PlayerIdx, piece: p, lowestRow: lowest})
			}
		}
	}
	// Bottom-most piece first (ties broken by playerIdx for determinism):
	// pieces resolved earlier become obstacles for the pieces above them,
	// which is what makes the lifts cascade upward.
	sort.Slice(pieces, func(i, j int) bool {
		if pieces[i].lowestRow != pieces[j].lowestRow {
			return pieces[i].lowestRow > pieces[j].lowestRow
		}
		return pieces[i].playerIdx < pieces[j].playerIdx
	})

	out := make([]Row, pf.Height)
	// Shift the locked stack up by rowsToAdd, stripping ALL active cells (each
	// piece is re-stamped at its resolved position, not carried by the shift).
	for i := 0; i+rowsToAdd < pf.Height; i++ {
		cells := make([]Cell, pf.Width)
		for j, c := range pf.Rows[i+rowsToAdd].Cells {
			if !c.Active {
				cells[j] = c
			}
		}
		out[i] = Row{Cells: cells}
	}
	// Permanent adversarial garbage fills the bottom rows.
	garbageStart := pf.Height - rowsToAdd
	if garbageStart < 0 {
		garbageStart = 0
	}
	for i := garbageStart; i < pf.Height; i++ {
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

	// Re-place each piece with the minimum lift that clears the risen stack,
	// the garbage, and every piece already resolved below it. The lift search
	// ends when the candidate's top cell would leave the board: that piece is
	// squeezed off the top and its owner tops out.
	var topped []int
	tmp := &Playfield{Width: pf.Width, Height: pf.Height, Rows: out}
	for _, fp := range pieces {
		placed := false
		for k := 0; ; k++ {
			cand := fp.piece
			cand.Row = fp.piece.Row - k
			minRow := pf.Height
			for _, cell := range cand.Cells() {
				if cell[0] < minRow {
					minRow = cell[0]
				}
			}
			if minRow < 0 {
				break // off the top: no lift keeps the piece on the board
			}
			if CanPlaceCoop(cand, tmp, fp.playerIdx) {
				tmp.SetActivePieceForPlayer(cand, fp.playerIdx)
				placed = true
				break
			}
		}
		if !placed {
			topped = append(topped, fp.playerIdx)
		}
	}
	return out, topped, boardFull
}



// AdversarialRowCount returns the number of garbage rows at the bottom of the
// board: contiguous bottom rows containing at least one adversarial cell.
// Garbage rows are permanent (never cleared) and bottom-anchored, so this
// count is monotonically non-decreasing over a game's lifetime — the engine
// uses it as the idempotency guard when several teammates race to apply the
// same shrink to their shared board.
//
// "At least one" rather than "all" because a garbage row can transiently hold
// a teammate's overlaid active piece and can permanently keep the empty holes
// that piece leaves behind (the documented shared-board skip artifact); a
// piece covers at most 4 of the row's 10-per-teammate cells, so a garbage row
// always retains adversarial cells.
func (pf *Playfield) AdversarialRowCount() int {
	count := 0
	for i := pf.Height - 1; i >= 0; i-- {
		any := false
		for _, c := range pf.Rows[i].Cells {
			if c.Adversarial {
				any = true
				break
			}
		}
		if !any {
			break
		}
		count++
	}
	return count
}
