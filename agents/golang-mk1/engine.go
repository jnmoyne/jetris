package main

// grid is a settled-cells board used only for planning: one cell state per
// square, no falling piece. 0 = empty, 1 = solid stack, 2 = permanent
// adversarial garbage (counts as filled for every feature but can never
// complete a row). It is a cheap value the planner clones per candidate.
type grid struct {
	h     int
	cells []int8 // row-major, width columns per row
}

func newGrid(h int) *grid { return &grid{h: h, cells: make([]int8, h*width)} }

func (g *grid) at(r, c int) int8     { return g.cells[r*width+c] }
func (g *grid) set(r, c int, v int8) { g.cells[r*width+c] = v }
func (g *grid) filled(r, c int) bool { return g.cells[r*width+c] != 0 }

func (g *grid) clone() *grid {
	c := &grid{h: g.h, cells: make([]int8, len(g.cells))}
	copy(c.cells, g.cells)
	return c
}

// canPlace reports whether the four cells are all in bounds and empty.
func (g *grid) canPlace(cs [4]cell) bool {
	for _, c := range cs {
		if c.r < 0 || c.r >= g.h || c.c < 0 || c.c >= width {
			return false
		}
		if g.filled(c.r, c.c) {
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
// column solid and no garbage cell (garbage rows are permanent).
func (g *grid) completedRows() []int {
	var out []int
	for r := 0; r < g.h; r++ {
		full, garbage := true, false
		for c := 0; c < width; c++ {
			switch g.at(r, c) {
			case 0:
				full = false
			case 2:
				garbage = true
			}
			if !full {
				break
			}
		}
		if full && !garbage {
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
	out := newGrid(g.h)
	dst := g.h - 1
	for r := g.h - 1; r >= 0; r-- {
		if removed[r] {
			continue
		}
		for c := 0; c < width; c++ {
			out.set(dst, c, g.at(r, c))
		}
		dst--
	}
	return out
}
