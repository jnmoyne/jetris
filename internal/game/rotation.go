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

// The half turn's kicks are SRS-X's — the 180 kicks the wiki's 180° twists
// are drawn for (harddrop.com/wiki/List_of_twists, "180° Twists": all of
// them are for SRS-X). Stored like the SRS ones, as (dRow, dCol) playfield
// deltas; Test180KickTables re-derives every entry from the (x, y) form and
// Test180Twists plays the wiki's twists through them.
//
// They are far more generous than the quarter turns': the piece may land up
// to three columns or rows away, which is what twists it into the wiki's
// slots — and lets it pass through a one-cell wall (the wiki's
// "teleporting"). One consequence: a flat I with no free row under it
// cannot turn, since 0→2 puts the bar a row lower and every kick of that
// row is sideways or down.

// kicks180JLSTZ are the half turn's kicks for J, L, S, T, Z pieces.
var kicks180JLSTZ = map[[2]int][][2]int{
	{0, 2}: {{0, 0}, {0, 1}, {0, 2}, {1, 1}, {1, 2}, {0, -1}, {0, -2}, {1, -1}, {1, -2}, {-1, 0}, {0, 3}, {0, -3}},
	{1, 3}: {{0, 0}, {1, 0}, {2, 0}, {1, -1}, {2, -1}, {-1, 0}, {-2, 0}, {-1, -1}, {-2, -1}, {0, 1}, {3, 0}, {-3, 0}},
	{2, 0}: {{0, 0}, {0, -1}, {0, -2}, {-1, -1}, {-1, -2}, {0, 1}, {0, 2}, {-1, 1}, {-1, 2}, {1, 0}, {0, -3}, {0, 3}},
	{3, 1}: {{0, 0}, {1, 0}, {2, 0}, {1, 1}, {2, 1}, {-1, 0}, {-2, 0}, {-1, 1}, {-2, 1}, {0, -1}, {3, 0}, {-3, 0}},
}

// kicks180I are the half turn's kicks for the I piece.
var kicks180I = map[[2]int][][2]int{
	{0, 2}: {{0, 0}, {0, -1}, {0, -2}, {0, 1}, {0, 2}, {1, 0}},
	{1, 3}: {{0, 0}, {1, 0}, {2, 0}, {-1, 0}, {-2, 0}, {0, -1}},
	{2, 0}: {{0, 0}, {0, 1}, {0, 2}, {0, -1}, {0, -2}, {-1, 0}},
	{3, 1}: {{0, 0}, {1, 0}, {2, 0}, {-1, 0}, {-2, 0}, {0, 1}},
}

// Turn is a rotation of the falling piece: a quarter turn either way, on the
// SRS kicks, or a half turn, on the SRS-X ones.
type Turn int

const (
	TurnCW  Turn = iota // a quarter turn clockwise
	TurnCCW             // a quarter turn counter-clockwise
	Turn180             // a half turn, on its own kick tables (kicks180JLSTZ, kicks180I)
)

// to is the orientation the turn reaches from from.
func (t Turn) to(from int) int {
	switch t {
	case TurnCCW:
		return (from + 3) % 4
	case Turn180:
		return (from + 2) % 4
	}
	return (from + 1) % 4
}

// Rotate applies the turn to the piece using its wall kicks — SRS for a
// quarter turn, SRS-X for the half turn. Returns the rotated piece and true
// on success, or the original piece and false.
func Rotate(p Piece, t Turn, pf *Playfield) (Piece, bool) {
	q, _, ok := RotateKick(p, t, pf)
	return q, ok
}

// RotateKick is Rotate reporting which kick placed the piece: the index into
// the turn's kick table of the offset that fit (0 = the rotation in place; a
// quarter turn's table ends at LastKick, the half turn's runs longer) — what
// tells a T-spin triple from a Mini T-spin (DetectTSpin).
func RotateKick(p Piece, t Turn, pf *Playfield) (Piece, int, bool) {
	return rotateWith(p, t, func(q Piece) bool { return CanPlace(q, pf) })
}

// RotateCoop is like Rotate but uses CanPlaceCoop for collision detection.
func RotateCoop(p Piece, t Turn, pf *Playfield, ownPlayerIdx int) (Piece, bool) {
	q, _, ok := RotateCoopKick(p, t, pf, ownPlayerIdx)
	return q, ok
}

// RotateCoopKick is RotateKick for a shared board (CanPlaceCoop).
func RotateCoopKick(p Piece, t Turn, pf *Playfield, ownPlayerIdx int) (Piece, int, bool) {
	return rotateWith(p, t, func(q Piece) bool { return CanPlaceCoop(q, pf, ownPlayerIdx) })
}

// rotateWith tries the turn's kicks in table order and returns the first
// placement canPlace accepts, with the kick's index; the O never rotates,
// and a turn no kick can place fails with the piece unmoved.
func rotateWith(p Piece, t Turn, canPlace func(Piece) bool) (Piece, int, bool) {
	from := p.Orientation % 4
	to := t.to(from)

	var kicks [][2]int
	key := [2]int{from, to}
	switch {
	case p.Type == PieceO:
		return p, 0, false // O piece doesn't rotate
	case p.Type == PieceI && t == Turn180:
		kicks = kicks180I[key]
	case p.Type == PieceI:
		kicks = kicksI[key]
	case t == Turn180:
		kicks = kicks180JLSTZ[key]
	default:
		kicks = kicksJLSTZ[key]
	}

	for i, kick := range kicks {
		candidate := Piece{
			Type:        p.Type,
			Orientation: to,
			Row:         p.Row + kick[0],
			Col:         p.Col + kick[1],
		}
		if canPlace(candidate) {
			return candidate, i, true
		}
	}

	return p, 0, false
}
