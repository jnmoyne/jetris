package game

// PieceType identifies one of the seven standard Tetris pieces.
type PieceType int

const (
	PieceI PieceType = iota
	PieceO
	PieceT
	PieceS
	PieceZ
	PieceJ
	PieceL
)

// Piece represents a tetromino at a specific position and orientation.
type Piece struct {
	Type        PieceType
	Orientation int // 0-3, clockwise rotations from spawn orientation
	Row         int // anchor row (top-left of bounding box, 0 = top of headroom)
	Col         int // anchor column
}

// cellOffsets defines the cell positions for each piece type at each orientation.
// Each entry is relative to the anchor (top-left of bounding box).
// [pieceType][orientation] -> list of (row, col) offsets
var cellOffsets = [7][4][][2]int{
	// PieceI
	{
		{{1, 0}, {1, 1}, {1, 2}, {1, 3}}, // 0: horizontal in row 1
		{{0, 2}, {1, 2}, {2, 2}, {3, 2}}, // 1: vertical in col 2
		{{2, 0}, {2, 1}, {2, 2}, {2, 3}}, // 2: horizontal in row 2
		{{0, 1}, {1, 1}, {2, 1}, {3, 1}}, // 3: vertical in col 1
	},
	// PieceO
	{
		{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, // all orientations identical
		{{0, 0}, {0, 1}, {1, 0}, {1, 1}},
		{{0, 0}, {0, 1}, {1, 0}, {1, 1}},
		{{0, 0}, {0, 1}, {1, 0}, {1, 1}},
	},
	// PieceT
	{
		{{0, 1}, {1, 0}, {1, 1}, {1, 2}}, // 0: T pointing up
		{{0, 1}, {1, 1}, {1, 2}, {2, 1}}, // 1: T pointing right
		{{1, 0}, {1, 1}, {1, 2}, {2, 1}}, // 2: T pointing down
		{{0, 1}, {1, 0}, {1, 1}, {2, 1}}, // 3: T pointing left
	},
	// PieceS
	{
		{{0, 1}, {0, 2}, {1, 0}, {1, 1}}, // 0
		{{0, 1}, {1, 1}, {1, 2}, {2, 2}}, // 1
		{{1, 1}, {1, 2}, {2, 0}, {2, 1}}, // 2
		{{0, 0}, {1, 0}, {1, 1}, {2, 1}}, // 3
	},
	// PieceZ
	{
		{{0, 0}, {0, 1}, {1, 1}, {1, 2}}, // 0
		{{0, 2}, {1, 1}, {1, 2}, {2, 1}}, // 1
		{{1, 0}, {1, 1}, {2, 1}, {2, 2}}, // 2
		{{0, 1}, {1, 0}, {1, 1}, {2, 0}}, // 3
	},
	// PieceJ
	{
		{{0, 0}, {1, 0}, {1, 1}, {1, 2}}, // 0: J spawn
		{{0, 1}, {0, 2}, {1, 1}, {2, 1}}, // 1
		{{1, 0}, {1, 1}, {1, 2}, {2, 2}}, // 2
		{{0, 1}, {1, 1}, {2, 0}, {2, 1}}, // 3
	},
	// PieceL
	{
		{{0, 2}, {1, 0}, {1, 1}, {1, 2}}, // 0: L spawn
		{{0, 1}, {1, 1}, {2, 1}, {2, 2}}, // 1
		{{1, 0}, {1, 1}, {1, 2}, {2, 0}}, // 2
		{{0, 0}, {0, 1}, {1, 1}, {2, 1}}, // 3
	},
}

// Cells returns the (row, col) pairs occupied by this piece in the playfield.
func (p Piece) Cells() [][2]int {
	offsets := cellOffsets[p.Type][p.Orientation%4]
	cells := make([][2]int, len(offsets))
	for i, off := range offsets {
		cells[i] = [2]int{p.Row + off[0], p.Col + off[1]}
	}
	return cells
}

// SpawnPiece creates a new piece at the standard spawn position.
// The piece spawns centered at the top of the visible area.
//
// All piece types use the same anchor row so their lowest cell sits at the same
// playfield row (row 3, just inside the headroom). Every spawn orientation places
// its occupied cells at bounding-box offset row 1 — including the horizontal I —
// so a single anchor row keeps every piece's lowest cell aligned. This matters
// because a piece only becomes visible once it falls past the headroom: if the I
// spawned one row higher (as it used to), it became visible one gravity tick later
// than every other piece, so a player hard-dropping each piece on sight would
// drop the I before it appeared and "never see" it.
func SpawnPiece(pt PieceType, width int) Piece {
	col := (width - 4) / 2 // center in field
	if col < 0 {
		col = 0
	}
	return Piece{
		Type:        pt,
		Orientation: 0,
		Row:         2, // lowest cell lands at row 3 for every piece type
		Col:         col,
	}
}
