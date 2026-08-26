package game

// SRS wall kick data. Each entry is a list of (dRow, dCol) offsets to try, in
// order, keyed by (fromOrientation, toOrientation).
//
// The Guideline publishes the kicks as (x, y) with +y UP; this file stores
// them as playfield deltas — (dRow, dCol) = (-y, x), row 0 at the top — so a
// "(0, -2)" kick (down two) is {2, 0} here. TestSRSKickTables re-derives every
// entry from the published (x, y) table, so the convention can't drift.
//
// The 4th and 5th kicks are the ones that matter for the classic spins: the
// down-two kicks let a piece drop into a slot as it turns (the T-spin triple)
// and the up-two kicks let a piece standing on the floor rotate.

// kicksJLSTZ are the wall kick offsets for J, L, S, T, Z pieces.
var kicksJLSTZ = map[[2]int][][2]int{
	{0, 1}: {{0, 0}, {0, -1}, {-1, -1}, {2, 0}, {2, -1}},
	{1, 0}: {{0, 0}, {0, 1}, {1, 1}, {-2, 0}, {-2, 1}},
	{1, 2}: {{0, 0}, {0, 1}, {1, 1}, {-2, 0}, {-2, 1}},
	{2, 1}: {{0, 0}, {0, -1}, {-1, -1}, {2, 0}, {2, -1}},
	{2, 3}: {{0, 0}, {0, 1}, {-1, 1}, {2, 0}, {2, 1}},
	{3, 2}: {{0, 0}, {0, -1}, {1, -1}, {-2, 0}, {-2, -1}},
	{3, 0}: {{0, 0}, {0, -1}, {1, -1}, {-2, 0}, {-2, -1}},
	{0, 3}: {{0, 0}, {0, 1}, {-1, 1}, {2, 0}, {2, 1}},
}

// kicksI are the wall kick offsets for the I piece.
var kicksI = map[[2]int][][2]int{
	{0, 1}: {{0, 0}, {0, -2}, {0, 1}, {1, -2}, {-2, 1}},
	{1, 0}: {{0, 0}, {0, 2}, {0, -1}, {-1, 2}, {2, -1}},
	{1, 2}: {{0, 0}, {0, -1}, {0, 2}, {-2, -1}, {1, 2}},
	{2, 1}: {{0, 0}, {0, 1}, {0, -2}, {2, 1}, {-1, -2}},
	{2, 3}: {{0, 0}, {0, 2}, {0, -1}, {-1, 2}, {2, -1}},
	{3, 2}: {{0, 0}, {0, -2}, {0, 1}, {1, -2}, {-2, 1}},
	{3, 0}: {{0, 0}, {0, 1}, {0, -2}, {2, 1}, {-1, -2}},
	{0, 3}: {{0, 0}, {0, -1}, {0, 2}, {-2, -1}, {1, 2}},
}

// Rotate applies a CW or CCW rotation to the piece using SRS wall kicks.
// Returns the rotated piece and true on success, or the original piece and false.
func Rotate(p Piece, clockwise bool, pf *Playfield) (Piece, bool) {
	from := p.Orientation % 4
	var to int
	if clockwise {
		to = (from + 1) % 4
	} else {
		to = (from + 3) % 4
	}

	var kicks [][2]int
	key := [2]int{from, to}
	if p.Type == PieceI {
		kicks = kicksI[key]
	} else if p.Type == PieceO {
		// O piece doesn't rotate
		return p, false
	} else {
		kicks = kicksJLSTZ[key]
	}

	for _, kick := range kicks {
		candidate := Piece{
			Type:        p.Type,
			Orientation: to,
			Row:         p.Row + kick[0],
			Col:         p.Col + kick[1],
		}
		if CanPlace(candidate, pf) {
			return candidate, true
		}
	}

	return p, false
}

// RotateCoop is like Rotate but uses CanPlaceCoop for collision detection.
func RotateCoop(p Piece, clockwise bool, pf *Playfield, ownPlayerIdx int) (Piece, bool) {
	from := p.Orientation % 4
	var to int
	if clockwise {
		to = (from + 1) % 4
	} else {
		to = (from + 3) % 4
	}

	var kicks [][2]int
	key := [2]int{from, to}
	if p.Type == PieceI {
		kicks = kicksI[key]
	} else if p.Type == PieceO {
		return p, false
	} else {
		kicks = kicksJLSTZ[key]
	}

	for _, kick := range kicks {
		candidate := Piece{
			Type:        p.Type,
			Orientation: to,
			Row:         p.Row + kick[0],
			Col:         p.Col + kick[1],
		}
		if CanPlaceCoop(candidate, pf, ownPlayerIdx) {
			return candidate, true
		}
	}

	return p, false
}
