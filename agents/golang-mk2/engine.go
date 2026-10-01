package main

import "math/bits"

// grid is a settled-cells board used only for planning: one cell state per
// square, no falling piece. 0 = empty, 1 = solid stack, 2 = adversarial
// garbage (filled for every feature; a row of nothing but garbage can never
// complete, a garbage row whose holes the stack has filled clears like any
// other), 3 = another player's falling piece (filled for every feature, and
// a row it crosses never completes), 4 = a teammate's claimed landing —
// where their piece is going to settle, by their claim on the blackboard or
// by its straight drop (coord.go buildProjection): stack for every feature,
// a row it fills completing like any other, since it will be stack by the
// time our piece lands (or we wait until it is). Beside the cells, every
// row's filled cells as a bit mask (bit c set when the cell in column c is
// anything but empty): what the placement checks and Dellacherie's
// features run on — a row at a time, where they used to run a cell at a
// time. It is a cheap value the planner clones per candidate.
type grid struct {
	h, w  int
	cells []int8   // row-major, w columns per row
	rows  []uint32 // per-row filled masks
	full  uint32   // the mask of a full row
}

// gridClaimed is a teammate's claimed landing, projected as settled.
const gridClaimed int8 = 4

func newGrid(h, w int) *grid {
	return &grid{h: h, w: w, cells: make([]int8, h*w), rows: make([]uint32, h), full: 1<<uint(w) - 1}
}

func (g *grid) at(r, c int) int8 { return g.cells[r*g.w+c] }
func (g *grid) set(r, c int, v int8) {
	g.cells[r*g.w+c] = v
	if v != 0 {
		g.rows[r] |= 1 << uint(c)
	} else {
		g.rows[r] &^= 1 << uint(c)
	}
}
func (g *grid) filled(r, c int) bool { return g.rows[r]>>uint(c)&1 == 1 }

func (g *grid) clone() *grid {
	c := &grid{h: g.h, w: g.w, cells: make([]int8, len(g.cells)), rows: make([]uint32, g.h), full: g.full}
	copy(c.cells, g.cells)
	copy(c.rows, g.rows)
	return c
}

// canPlace reports whether the four cells are all in bounds and empty.
func (g *grid) canPlace(cs [4]cell) bool {
	for _, c := range cs {
		if c.r < 0 || c.r >= g.h || c.c < 0 || c.c >= g.w {
			return false
		}
		if g.rows[c.r]>>uint(c.c)&1 == 1 {
			return false
		}
	}
	return true
}

// dropRow returns the lowest row the piece can occupy in column col at
// orientation orient, starting the search from row.
func (g *grid) dropRow(pt, orient, row, col int) int {
	for g.canPlace(pieceCells(pt, orient, row+1, col)) {
		row++
	}
	return row
}

// completedRows returns the indices of full rows that can actually clear: every
// column filled, at least one of them by the stack (a solid garbage row is
// permanent), and no foreign falling piece in the row.
func (g *grid) completedRows() []int {
	var out []int
	for r := 0; r < g.h; r++ {
		if g.rows[r] != g.full {
			continue
		}
		stack, blocked := false, false
		for c := 0; c < g.w; c++ {
			switch g.at(r, c) {
			case 1, gridClaimed:
				stack = true
			case 3:
				blocked = true
			}
		}
		if stack && !blocked {
			out = append(out, r)
		}
	}
	return out
}

// clearRows returns a new grid with the given rows removed and everything above
// each cleared row shifted down to fill the gap (standard gravity collapse).
func (g *grid) clearRows(rows []int) *grid {
	removed := make(map[int]bool, len(rows))
	for _, r := range rows {
		removed[r] = true
	}
	out := newGrid(g.h, g.w)
	dst := g.h - 1
	for r := g.h - 1; r >= 0; r-- {
		if removed[r] {
			continue
		}
		copy(out.cells[dst*g.w:(dst+1)*g.w], g.cells[r*g.w:(r+1)*g.w])
		out.rows[dst] = g.rows[r]
		dst--
	}
	return out
}

// ---- Dellacherie's features, a row at a time ---------------------------

// rowTransitions counts, over every row, the changes between filled and
// empty going across it, the walls counting as filled.
func (g *grid) rowTransitions() int {
	n := 0
	for _, m := range g.rows {
		a := m<<1 | 1                // the cells shifted up one bit, the left wall in bit 0
		b := m | 1<<uint(g.w)        // the cells, the right wall in bit w
		n += bits.OnesCount32(a ^ b) // a change wherever a neighbour differs
	}
	return n
}

// colTransitions counts, down every column, the changes between filled and
// empty, the space above the board counting as empty and the floor as
// filled.
func (g *grid) colTransitions() int {
	n := bits.OnesCount32(g.rows[0]) // above the board: empty
	for r := 1; r < g.h; r++ {
		n += bits.OnesCount32(g.rows[r-1] ^ g.rows[r])
	}
	return n + bits.OnesCount32(^g.rows[g.h-1]&g.full) // the floor: filled
}

// holes counts the empty cells with a filled cell somewhere above them.
func (g *grid) holes() int {
	n := 0
	var covered uint32
	for _, m := range g.rows {
		n += bits.OnesCount32(covered &^ m)
		covered |= m
	}
	return n
}

// wells is Dellacherie's cumulative well depth: 1+2+…+d for a well of depth
// d — every well cell (empty, walled on both sides) adds the empty cells
// from it down to the first filled one. One pass up the board: the run of
// empty cells below each column is kept as the pass climbs.
func (g *grid) wells() int {
	n := 0
	var run [32]int
	for r := g.h - 1; r >= 0; r-- {
		m := g.rows[r]
		walls := (m<<1 | 1) & (m>>1 | 1<<uint(g.w-1)) // both neighbours filled, the walls counting
		wellCells := ^m & walls & g.full
		for c := 0; c < g.w; c++ {
			if m>>uint(c)&1 == 1 {
				run[c] = 0
				continue
			}
			run[c]++
			if wellCells>>uint(c)&1 == 1 {
				n += run[c]
			}
		}
	}
	return n
}
