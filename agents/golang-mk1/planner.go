package main

import (
	"math/rand/v2"
	"sort"
)

// The brain: Pierre Dellacherie's six-feature one-ply heuristic with the
// "El-Tetris" weights — the strong evaluator inherited from the retired
// in-repo mk1 agent, grafted onto this self-contained engine. Features
// are computed on the board AFTER the simulated lock and clear, except landing
// height and eroded cells, which are move features by definition. Optional
// beam-pruned lookahead over the game's revealed piece preview extends it.
const (
	weightLandingHeight  = -4.500158825
	weightErodedCells    = 3.4181268
	weightRowTransitions = -3.2178882
	weightColTransitions = -9.348695
	weightHoles          = -7.899265
	weightWells          = -3.3855972
)

// Spawn position every peer uses for a new piece: two rows into the headroom,
// horizontally centered (WIDTH-4)/2.
const (
	spawnRow = 2
	spawnCol = (width - 4) / 2
)

// placement is one candidate final resting position and the score it earns.
type placement struct {
	orient, col, dropRow int
	dest                 [4]cell
	lines                int
	score                float64
}

// planPlacements enumerates every placement reachable with kick-free rotations
// at the piece's current position (sRow, sCol) plus sideways slides plus a hard
// drop (a legal subset of the game's moves), scores each with Dellacherie + the
// lookahead over `upcoming` (which spawn fresh at spawnC — the caller's own
// section spawn on shared boards), and returns them best first.
func planPlacements(g *grid, pt, sRow, sCol, spawnC int, upcoming []int) []placement {
	cands := enumerate(g, pt, sRow, sCol)
	out := make([]placement, 0, len(cands))
	for _, cand := range cands {
		after, lines, eroded := simulateLock(g, cand.dest)
		cand.lines = lines
		cand.score = evaluateBoard(after, cand.dest, lines, eroded) + lookahead(after, spawnC, upcoming)
		out = append(out, cand)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// enumerate returns each reachable resting position: the orientations reached
// by successive in-place rotations at spawn, each swept left and right across
// every column it can occupy, dropped to rest.
func enumerate(g *grid, pt, sRow, sCol int) []placement {
	// Orientations reachable by repeated in-place rotation from spawn.
	var orients []int
	seen := map[int]bool{}
	for o := 0; ; o++ {
		if !seen[o&3] && g.canPlace(pieceCells(pt, o&3, sRow, sCol)) {
			seen[o&3] = true
			orients = append(orients, o&3)
		}
		if o >= 3 || !g.canPlace(pieceCells(pt, (o+1)&3, sRow, sCol)) {
			break
		}
	}
	var out []placement
	record := func(o, col int) {
		dr := g.dropRow(pt, o, sRow, col)
		out = append(out, placement{orient: o, col: col, dropRow: dr, dest: pieceCells(pt, o, dr, col)})
	}
	for _, o := range orients {
		record(o, sCol)
		for col := sCol - 1; g.canPlace(pieceCells(pt, o, sRow, col)); col-- {
			record(o, col)
		}
		for col := sCol + 1; g.canPlace(pieceCells(pt, o, sRow, col)); col++ {
			record(o, col)
		}
	}
	return out
}

// simulateLock stamps dest onto a clone, clears full rows, and returns the
// resulting board, the number of lines cleared, and the eroded-cells feature
// (lines × piece cells that were in the cleared rows).
func simulateLock(g *grid, dest [4]cell) (*grid, int, int) {
	sim := g.clone()
	for _, c := range dest {
		if c.r >= 0 && c.r < sim.h && c.c >= 0 && c.c < sim.w {
			sim.set(c.r, c.c, 1)
		}
	}
	rows := sim.completedRows()
	if len(rows) == 0 {
		return sim, 0, 0
	}
	inRow := map[int]bool{}
	for _, r := range rows {
		inRow[r] = true
	}
	pieceInCleared := 0
	for _, c := range dest {
		if inRow[c.r] {
			pieceInCleared++
		}
	}
	return sim.clearRows(rows), len(rows), pieceInCleared * len(rows)
}

// evaluateBoard scores one placement: the Dellacherie feature combination.
func evaluateBoard(g *grid, dest [4]cell, lines, eroded int) float64 {
	return weightLandingHeight*landingHeight(g, dest) +
		weightErodedCells*float64(eroded) +
		weightRowTransitions*float64(rowTransitions(g)) +
		weightColTransitions*float64(colTransitions(g)) +
		weightHoles*float64(holes(g)) +
		weightWells*float64(wells(g))
}

func landingHeight(g *grid, dest [4]cell) float64 {
	sum := 0.0
	for _, c := range dest {
		sum += float64(g.h - 1 - c.r)
	}
	return sum / float64(len(dest))
}

func rowTransitions(g *grid) int {
	n := 0
	for r := 0; r < g.h; r++ {
		prev := true // left wall
		for c := 0; c < g.w; c++ {
			cur := g.filled(r, c)
			if cur != prev {
				n++
			}
			prev = cur
		}
		if !prev { // right wall
			n++
		}
	}
	return n
}

func colTransitions(g *grid) int {
	n := 0
	for c := 0; c < g.w; c++ {
		prev := false // above the board
		for r := 0; r < g.h; r++ {
			cur := g.filled(r, c)
			if cur != prev {
				n++
			}
			prev = cur
		}
		if !prev { // floor
			n++
		}
	}
	return n
}

func holes(g *grid) int {
	n := 0
	for c := 0; c < g.w; c++ {
		covered := false
		for r := 0; r < g.h; r++ {
			if g.filled(r, c) {
				covered = true
			} else if covered {
				n++
			}
		}
	}
	return n
}

// wells is Dellacherie's cumulative well depth: 1+2+…+d for a well of depth d.
func wells(g *grid) int {
	n := 0
	for c := 0; c < g.w; c++ {
		for r := 0; r < g.h; r++ {
			if g.filled(r, c) {
				continue
			}
			leftFilled := c == 0 || g.filled(r, c-1)
			rightFilled := c == g.w-1 || g.filled(r, c+1)
			if !leftFilled || !rightFilled {
				continue
			}
			for r2 := r; r2 < g.h && !g.filled(r2, c); r2++ {
				n++
			}
		}
	}
	return n
}

const (
	lookaheadBeam          = 3
	lookaheadTopOutPenalty = -1e4
)

// lookahead returns the best evaluation total achievable by playing `upcoming`
// (the revealed preview pieces, in order) on board g: at each level only the
// lookaheadBeam best placements are expanded further. A piece that cannot spawn
// is an imminent top-out and scores the penalty.
func lookahead(g *grid, spawnC int, upcoming []int) float64 {
	if len(upcoming) == 0 {
		return 0
	}
	pt := upcoming[0]
	if !g.canPlace(pieceCells(pt, 0, spawnRow, spawnC)) {
		return lookaheadTopOutPenalty
	}
	cands := enumerate(g, pt, spawnRow, spawnC)
	if len(cands) == 0 {
		return lookaheadTopOutPenalty
	}
	type exp struct {
		board *grid
		score float64
	}
	xs := make([]exp, 0, len(cands))
	for _, cand := range cands {
		after, lines, eroded := simulateLock(g, cand.dest)
		xs = append(xs, exp{after, evaluateBoard(after, cand.dest, lines, eroded)})
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i].score > xs[j].score })
	if len(upcoming) == 1 {
		return xs[0].score
	}
	if len(xs) > lookaheadBeam {
		xs = xs[:lookaheadBeam]
	}
	best := xs[0].score + lookahead(xs[0].board, spawnC, upcoming[1:])
	for _, e := range xs[1:] {
		if s := e.score + lookahead(e.board, spawnC, upcoming[1:]); s > best {
			best = s
		}
	}
	return best
}

// choose applies the blunder model to a ranked list: with probability
// tn.BlunderRate it picks uniformly among ranks 2..1+BlunderDepth instead of the
// best. Returns false only for an empty list.
func choose(ranked []placement, tn tuning, rnd *rand.Rand) (placement, bool) {
	if len(ranked) == 0 {
		return placement{}, false
	}
	if rnd != nil && tn.blunderRate > 0 && len(ranked) > 1 && rnd.Float64() < tn.blunderRate {
		depth := tn.blunderDepth
		if depth > len(ranked)-1 {
			depth = len(ranked) - 1
		}
		if depth > 0 {
			return ranked[1+rnd.IntN(depth)], true
		}
	}
	return ranked[0], true
}
