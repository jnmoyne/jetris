package main

// Tetromino geometry. pieces[type][orientation] is the four (rowOff, colOff)
// cell offsets from the piece's anchor; orientation 0 is the spawn shape.
// Types: I O T S Z J L = 0..6. These offsets are the game's canonical shapes
// (jetris-gameplays.md §2) and match every other peer's engine — the whole
// point of the deterministic protocol is that everyone computes the same cells.
var pieces = [7][4][4][2]int{
	{ // I
		{{1, 0}, {1, 1}, {1, 2}, {1, 3}}, {{0, 2}, {1, 2}, {2, 2}, {3, 2}},
		{{2, 0}, {2, 1}, {2, 2}, {2, 3}}, {{0, 1}, {1, 1}, {2, 1}, {3, 1}},
	},
	{ // O (all four orientations identical)
		{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, {{0, 0}, {0, 1}, {1, 0}, {1, 1}},
		{{0, 0}, {0, 1}, {1, 0}, {1, 1}}, {{0, 0}, {0, 1}, {1, 0}, {1, 1}},
	},
	{ // T
		{{0, 1}, {1, 0}, {1, 1}, {1, 2}}, {{0, 1}, {1, 1}, {1, 2}, {2, 1}},
		{{1, 0}, {1, 1}, {1, 2}, {2, 1}}, {{0, 1}, {1, 0}, {1, 1}, {2, 1}},
	},
	{ // S
		{{0, 1}, {0, 2}, {1, 0}, {1, 1}}, {{0, 1}, {1, 1}, {1, 2}, {2, 2}},
		{{1, 1}, {1, 2}, {2, 0}, {2, 1}}, {{0, 0}, {1, 0}, {1, 1}, {2, 1}},
	},
	{ // Z
		{{0, 0}, {0, 1}, {1, 1}, {1, 2}}, {{0, 2}, {1, 1}, {1, 2}, {2, 1}},
		{{1, 0}, {1, 1}, {2, 1}, {2, 2}}, {{0, 1}, {1, 0}, {1, 1}, {2, 0}},
	},
	{ // J
		{{0, 0}, {1, 0}, {1, 1}, {1, 2}}, {{0, 1}, {0, 2}, {1, 1}, {2, 1}},
		{{1, 0}, {1, 1}, {1, 2}, {2, 2}}, {{0, 1}, {1, 1}, {2, 0}, {2, 1}},
	},
	{ // L
		{{0, 2}, {1, 0}, {1, 1}, {1, 2}}, {{0, 1}, {1, 1}, {2, 1}, {2, 2}},
		{{1, 0}, {1, 1}, {1, 2}, {2, 0}}, {{0, 0}, {0, 1}, {1, 1}, {2, 1}},
	},
}

// width is fixed: competitive boards are always ten cells wide.
const width = 10

// cell is one board coordinate.
type cell struct{ r, c int }

// pieceCells returns the four absolute cells of piece pt at orientation orient
// anchored at (row, col).
func pieceCells(pt, orient, row, col int) [4]cell {
	off := pieces[pt][orient&3]
	return [4]cell{
		{row + off[0][0], col + off[0][1]},
		{row + off[1][0], col + off[1][1]},
		{row + off[2][0], col + off[2][1]},
		{row + off[3][0], col + off[3][1]},
	}
}
