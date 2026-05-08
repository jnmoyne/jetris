package game

// CanPlace returns true if all cells of p are within bounds and not
// occupied by a locked cell in pf.
func CanPlace(p Piece, pf *Playfield) bool {
	for _, cell := range p.Cells() {
		r, c := cell[0], cell[1]
		if r < 0 || r >= pf.Height || c < 0 || c >= pf.Width {
			return false
		}
		if pf.Rows[r].Cells[c].Occupied && !pf.Rows[r].Cells[c].Active {
			return false
		}
	}
	return true
}

// CanPlaceCoop returns true if the piece can be placed without conflicting
// with locked cells or the OTHER player's active cells. The moving player's
// own active cells (matching ownPlayerIdx) are excluded from collision.
func CanPlaceCoop(p Piece, pf *Playfield, ownPlayerIdx int) bool {
	for _, cell := range p.Cells() {
		r, c := cell[0], cell[1]
		if r < 0 || r >= pf.Height || c < 0 || c >= pf.Width {
			return false
		}
		cc := pf.Rows[r].Cells[c]
		if cc.Occupied && !cc.Active {
			return false // locked cell
		}
		if cc.Active && cc.PlayerIdx != ownPlayerIdx {
			return false // other player's active piece
		}
	}
	return true
}

// HardDropDestinationCoop is like HardDropDestination but uses CanPlaceCoop.
func HardDropDestinationCoop(p Piece, pf *Playfield, ownPlayerIdx int) Piece {
	dest := p
	for {
		next := dest
		next.Row++
		if !CanPlaceCoop(next, pf, ownPlayerIdx) {
			return dest
		}
		dest = next
	}
}

// WouldCollide returns true if moving p by (dRow, dCol) would result
// in an invalid placement.
func WouldCollide(p Piece, pf *Playfield, dRow, dCol int) bool {
	moved := p
	moved.Row += dRow
	moved.Col += dCol
	return !CanPlace(moved, pf)
}

// HardDropDestination returns the lowest valid position for p by
// moving it down until collision.
func HardDropDestination(p Piece, pf *Playfield) Piece {
	dest := p
	for {
		next := dest
		next.Row++
		if !CanPlace(next, pf) {
			return dest
		}
		dest = next
	}
}
