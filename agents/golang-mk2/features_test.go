package main

import (
	"math/rand/v2"
	"testing"
)

// The reference features, a cell at a time — golang-mk1's — which the
// grid's row-at-a-time ones must agree with on every board.

func rowTransitionsSlow(g *grid) int {
	n := 0
	for r := 0; r < g.h; r++ {
		prev := true
		for c := 0; c < g.w; c++ {
			cur := g.at(r, c) != 0
			if cur != prev {
				n++
			}
			prev = cur
		}
		if !prev {
			n++
		}
	}
	return n
}

func colTransitionsSlow(g *grid) int {
	n := 0
	for c := 0; c < g.w; c++ {
		prev := false
		for r := 0; r < g.h; r++ {
			cur := g.at(r, c) != 0
			if cur != prev {
				n++
			}
			prev = cur
		}
		if !prev {
			n++
		}
	}
	return n
}

func holesSlow(g *grid) int {
	n := 0
	for c := 0; c < g.w; c++ {
		covered := false
		for r := 0; r < g.h; r++ {
			if g.at(r, c) != 0 {
				covered = true
			} else if covered {
				n++
			}
		}
	}
	return n
}

func wellsSlow(g *grid) int {
	n := 0
	f := func(r, c int) bool { return g.at(r, c) != 0 }
	for c := 0; c < g.w; c++ {
		for r := 0; r < g.h; r++ {
			if f(r, c) {
				continue
			}
			if (c == 0 || f(r, c-1)) && (c == g.w-1 || f(r, c+1)) {
				for r2 := r; r2 < g.h && !f(r2, c); r2++ {
					n++
				}
			}
		}
	}
	return n
}

func randomGrid(rnd *rand.Rand, h, w int) *grid {
	g := newGrid(h, w)
	density := rnd.Float64()
	for r := 0; r < h; r++ {
		for c := 0; c < w; c++ {
			if rnd.Float64() < density*float64(r)/float64(h) {
				g.set(r, c, int8(1+rnd.IntN(4)))
			}
		}
	}
	return g
}

func TestFeaturesAgreeWithTheCellByCellOnes(t *testing.T) {
	rnd := rand.New(rand.NewPCG(7, 11))
	for i := 0; i < 2000; i++ {
		w := 10 + rnd.IntN(maxGridWidth-9) // 10..63 columns: every board the grid holds
		h := 24 + rnd.IntN(11)
		g := randomGrid(rnd, h, w)
		if a, b := g.rowTransitions(), rowTransitionsSlow(g); a != b {
			t.Fatalf("rowTransitions %d != %d on %dx%d", a, b, h, w)
		}
		if a, b := g.colTransitions(), colTransitionsSlow(g); a != b {
			t.Fatalf("colTransitions %d != %d on %dx%d", a, b, h, w)
		}
		if a, b := g.holes(), holesSlow(g); a != b {
			t.Fatalf("holes %d != %d on %dx%d", a, b, h, w)
		}
		if a, b := g.wells(), wellsSlow(g); a != b {
			t.Fatalf("wells %d != %d on %dx%d", a, b, h, w)
		}
		// The masks agree with the cells, through set, clone and clearRows.
		c := g.clone()
		if rows := c.completedRows(); len(rows) > 0 {
			c = c.clearRows(rows)
		}
		for r := 0; r < c.h; r++ {
			for col := 0; col < c.w; col++ {
				if c.filled(r, col) != (c.at(r, col) != 0) {
					t.Fatalf("mask and cells disagree at (%d,%d)", r, col)
				}
			}
		}
	}
}

// A crew of eight's board (38 columns at four extra a seat) is planned on
// like any other, and so is the widest the grid holds: the row masks are
// 64 bits wide (they were 32, and such a board was a panic at the first
// plan).
func TestPlanningOnAWideBoard(t *testing.T) {
	for _, w := range []int{38, maxGridWidth} {
		g := newGrid(20+headroom, w)
		for c := 0; c < w-1; c++ { // a row full but for the last column, and a step on it
			g.set(g.h-1, c, 1)
			if c%5 == 0 {
				g.set(g.h-2, c, 1)
			}
		}
		for pt := range pieces {
			for _, proj := range []*projection{nil, {well: w - 1, wellOn: true}} {
				ranked := planPlacements(g, pt, spawnRow, w/2, 3, []int{0, 1}, proj)
				if len(ranked) == 0 {
					t.Fatalf("no placement for piece %d on a board of %d columns", pt, w)
				}
				for _, d := range ranked[0].dest {
					if d.c < 0 || d.c >= w {
						t.Fatalf("piece %d on a board of %d columns lands at column %d", pt, w, d.c)
					}
				}
			}
		}
	}
}
