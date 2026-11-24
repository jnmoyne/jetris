package main

import (
	"math/rand/v2"
	"sort"
	"sync"
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
	return planPlacementsFrom(g, pt, 0, sRow, sCol, spawnC, upcoming)
}

// planPlacementsFrom is planPlacements for a piece standing in the
// orientation sOrient: a piece planned again where it stands (resumed after
// a repair or a resync, spawn) has turned since its spawn, and the
// orientations it can reach are the ones a rotation in place takes it to
// from THERE.
func planPlacementsFrom(g *grid, pt, sOrient, sRow, sCol, spawnC int, upcoming []int) []placement {
	cands := enumerateFrom(g, pt, sOrient, sRow, sCol)
	out := make([]placement, 0, len(cands))
	afters := make([]*grid, 0, len(cands))
	for _, cand := range cands {
		after, lines, eroded := simulateLock(g, cand.dest)
		cand.lines = lines
		cand.score = evaluateBoard(after, cand.dest, lines, eroded)
		out = append(out, cand)
		afters = append(afters, after)
	}
	// Every placement's lookahead is an independent search from its own
	// board: they run side by side, one goroutine each, so a plan at the
	// full preview fits the think pause instead of adding a third of a
	// second to every piece. The answers are the sequential search's.
	if len(upcoming) > 0 {
		las := make([]float64, len(out))
		var wg sync.WaitGroup
		for i := range out {
			wg.Add(1)
			go func() {
				defer wg.Done()
				las[i] = lookahead(afters[i], spawnC, upcoming)
			}()
		}
		wg.Wait()
		for i := range out {
			out[i].score += las[i]
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

// enumerate returns each reachable resting position: the orientations reached
// by successive in-place rotations at spawn, each swept left and right across
// every column it can occupy, dropped to rest.
func enumerate(g *grid, pt, sRow, sCol int) []placement {
	return enumerateFrom(g, pt, 0, sRow, sCol)
}

// enumerateInto is enumerate appending to out, for a search that reuses one
// buffer for every board it expands.
func enumerateInto(out []placement, g *grid, pt, sRow, sCol int) []placement {
	return enumerateFromInto(out, g, pt, 0, sRow, sCol)
}

// enumerateFrom is enumerate for a piece standing in the orientation sOrient
// (0 at its spawn).
func enumerateFrom(g *grid, pt, sOrient, sRow, sCol int) []placement {
	return enumerateFromInto(nil, g, pt, sOrient, sRow, sCol)
}

// enumerateFromInto is enumerateFrom appending to out.
func enumerateFromInto(out []placement, g *grid, pt, sOrient, sRow, sCol int) []placement {
	// Orientations reachable by repeated in-place rotation from where the
	// piece stands.
	var orients [4]int
	var seen [4]bool
	n := 0
	for i := 0; ; i++ {
		o := (sOrient + i) & 3
		if !seen[o] && g.canPlace(pieceCells(pt, o, sRow, sCol)) {
			seen[o] = true
			orients[n] = o
			n++
		}
		if i >= 3 || !g.canPlace(pieceCells(pt, (o+1)&3, sRow, sCol)) {
			break
		}
	}
	record := func(o, col int) {
		dr := g.dropRow(pt, o, sRow, col)
		out = append(out, placement{orient: o, col: col, dropRow: dr, dest: pieceCells(pt, o, dr, col)})
	}
	for _, o := range orients[:n] {
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

// simulateOn is simulateLock for a board that is only looked at, never
// kept: g's cells are copied over scratch (a grid of g's size) and dest
// stamped there, so the search allocates no board for a placement it only
// scores. Returns the board to evaluate: scratch itself, good until its
// next use, or a new one when the lock clears rows.
func simulateOn(scratch, g *grid, dest [4]cell) (*grid, int, int) {
	copy(scratch.cells, g.cells)
	for _, c := range dest {
		if c.r >= 0 && c.r < scratch.h && c.c >= 0 && c.c < scratch.w {
			scratch.set(c.r, c.c, 1)
		}
	}
	rows := scratch.completedRows()
	if len(rows) == 0 {
		return scratch, 0, 0
	}
	pieceInCleared := 0
	for _, c := range dest {
		for _, r := range rows {
			if c.r == r {
				pieceInCleared++
			}
		}
	}
	return scratch.clearRows(rows), len(rows), pieceInCleared * len(rows)
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
	s := lookaheadSearch{scratch: newGrid(g.h, g.w), spawnC: spawnC}
	return s.from(g, upcoming)
}

// lookaheadSearch is what one lookahead works with, from its first board to
// its last: the board it scores every placement on (simulateOn) and the
// buffers it lists and ranks a board's placements in, used again for every
// board it expands. A board and two lists allocated for every one of them
// is what a plan's time used to go on; only the lookaheadBeam placements a
// level goes on from get a board of their own.
type lookaheadSearch struct {
	scratch *grid
	cands   []placement
	ranked  []scoredPlacement
	spawnC  int
}

type scoredPlacement struct {
	score float64
	dest  [4]cell
}

func (s *lookaheadSearch) from(g *grid, upcoming []int) float64 {
	if len(upcoming) == 0 {
		return 0
	}
	pt := upcoming[0]
	if !g.canPlace(pieceCells(pt, 0, spawnRow, s.spawnC)) {
		return lookaheadTopOutPenalty
	}
	s.cands = enumerateInto(s.cands[:0], g, pt, spawnRow, s.spawnC)
	if len(s.cands) == 0 {
		return lookaheadTopOutPenalty
	}
	xs := s.ranked[:0]
	for _, cand := range s.cands {
		after, lines, eroded := simulateOn(s.scratch, g, cand.dest)
		xs = append(xs, scoredPlacement{evaluateBoard(after, cand.dest, lines, eroded), cand.dest})
	}
	s.ranked = xs
	// sort.Slice, as the search always ranked: on equal scores it picks
	// the same beam the allocating search did.
	sort.Slice(xs, func(i, j int) bool { return xs[i].score > xs[j].score })
	if len(upcoming) == 1 {
		return xs[0].score
	}
	// The levels below use the buffers again: what this one goes on from
	// is copied out of them first.
	var beam [lookaheadBeam]scoredPlacement
	n := copy(beam[:], xs)
	best := 0.0
	for i, e := range beam[:n] {
		board, _, _ := simulateLock(g, e.dest)
		if t := e.score + s.from(board, upcoming[1:]); i == 0 || t > best {
			best = t
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

// revealedPieces is the planner's entire lookahead: the upcoming pieces the
// GAME reveals — the next `nextCount` of this seat's sequence, exactly the
// tiles a human sees in the NEXT well — further trimmed to the difficulty's
// `lookahead`. This is the fair-visibility contract's preview rule
// (jetris-agent-guide.md §1): the horizon is the game's setting, read from its
// meta, and nothing on the agent's side (difficulty, flag, default) may reach
// past it. A game with no preview — next_count 0, or a meta written before the
// field existed — reveals nothing, so the planner plays one piece at a time
// like everyone else. The seed is consulted for these indices only. `ration`
// is this seat's piece set in a split-pieces teams game (nil elsewhere): the
// preview is of the seat's OWN sequence, so it reveals only the types this
// seat holds. `bag` is the game's bag rule (meta bag), the way that sequence
// is dealt.
func revealedPieces(seed uint64, ration []int, bag string, pieceIdx, nextCount, lookahead int) []int {
	var upcoming []int
	for i := 1; i <= min(nextCount, lookahead); i++ {
		upcoming = append(upcoming, pieceAtBag(seed, ration, bag, pieceIdx+i))
	}
	return upcoming
}
