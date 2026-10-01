package main

import (
	"math"
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
// waitOn, on a coordinated board, is the seats whose claimed landings hold
// the placement up — it must not lock before theirs have (coord.go).
type placement struct {
	orient, col, dropRow int
	dest                 [4]cell
	lines                int
	score                float64
	waitOn               []int
}

// projection is what a coordinated board adds to the planner's grid
// (coord.go buildProjection): the teammates' claimed landings stamped into
// it as gridClaimed, by seat, with the time each is expected to take; our
// own spawn column, and the teammates' spawn boxes — for the costs a
// placement carries beyond its Dellacherie score (penalty).
type projection struct {
	claimed   map[cell]int // a claimed landing's cell → the seat whose it is
	soft      map[cell]int // a silent teammate's straight drop → the seat: a nudge, not a wall
	depETA    map[int]int  // seat → how long their claim is expected to take to land, in ms
	pieces    map[int]int  // seat → the piece index their claim is for
	ownSpawnC int
	boxes     []spawnBox
}

// penalty is what a placement costs beyond its evaluation, in Dellacherie's
// units (a hole is 7.9): waiting on a teammate's claim in proportion to how
// long it is expected to take; every cell of the placement in, or right on
// top of, a silent teammate's straight drop; the walk from our spawn point,
// a little per column; and every imminent teammate spawn box the walk
// crosses.
func (proj *projection) penalty(cand placement) float64 {
	p := 0.0
	for _, seat := range cand.waitOn {
		p += depPenalty * float64(proj.depETA[seat]) / 1000
	}
	for _, d := range cand.dest {
		if _, ok := proj.soft[d]; ok {
			p += softPenalty
		} else if _, ok := proj.soft[cell{d.r + 1, d.c}]; ok {
			p += softPenalty
		}
	}
	p += travelCost * math.Abs(float64(cand.col-proj.ownSpawnC))
	lo, hi := min(cand.col, proj.ownSpawnC), max(cand.col, proj.ownSpawnC)+3
	for _, b := range proj.boxes {
		if b.imminent && b.c0 <= hi && b.c1 >= lo {
			p += crossCost
		}
	}
	return p
}

// supportSeats is the seats whose claimed landings a placement rests on or
// completes a row with — the claims it must let land first. Sorted.
func supportSeats(g *grid, proj *projection, cand placement) []int {
	seats := map[int]bool{}
	inRow := map[int]bool{}
	for _, d := range cand.dest {
		inRow[d.r] = true
		if below := (cell{d.r + 1, d.c}); below.r < g.h && g.at(below.r, below.c) == gridClaimed {
			if seat, ok := proj.claimed[below]; ok {
				seats[seat] = true
			}
		}
	}
	// A row the lock completes: full once the placement's cells are in.
	destSet := cellSet(cand.dest)
	for r := range inRow {
		full := true
		for c := 0; c < g.w && full; c++ {
			full = destSet[cell{r, c}] || g.filled(r, c)
		}
		if !full {
			continue
		}
		for c := 0; c < g.w; c++ {
			if g.at(r, c) == gridClaimed {
				if seat, ok := proj.claimed[cell{r, c}]; ok {
					seats[seat] = true
				}
			}
		}
	}
	if len(seats) == 0 {
		return nil
	}
	out := make([]int, 0, len(seats))
	for s := range seats {
		out = append(out, s)
	}
	sort.Ints(out)
	return out
}

// planPlacements enumerates every placement reachable with kick-free rotations
// at the piece's current position (sRow, sCol) plus sideways slides plus a hard
// drop (a legal subset of the game's moves), scores each with Dellacherie + the
// lookahead over `upcoming` (which spawn fresh at spawnC — the caller's own
// section spawn on shared boards) — less, on a coordinated board (proj), the
// placement's penalty — and returns them best first. A nil proj is
// golang-mk1's planner exactly.
func planPlacements(g *grid, pt, sRow, sCol, spawnC int, upcoming []int, proj *projection) []placement {
	cands := enumerate(g, pt, sRow, sCol)
	out := make([]placement, 0, len(cands))
	afters := make([]*grid, 0, len(cands))
	// Every placement by its one-ply evaluation first…
	for _, cand := range cands {
		after, lines, eroded := simulateLock(g, cand.dest)
		cand.lines = lines
		cand.score = evaluateBoard(after, cand.dest, lines, eroded)
		if proj != nil {
			cand.waitOn = supportSeats(g, proj, cand)
		}
		out = append(out, cand)
		afters = append(afters, after)
	}
	order := make([]int, len(out))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool { return out[order[i]].score > out[order[j]].score })
	// …then the lookahead over the revealed preview for the planBeam best
	// of them alone: a placement outside the beam keeps its one-ply score
	// and ranks below every placement in it (the lookahead's sum is what
	// scores are made of), in its one-ply order.
	if len(upcoming) > 0 {
		floor := math.Inf(1)
		for k, i := range order {
			if k >= planBeam {
				break
			}
			la := lookahead(afters[i], spawnC, upcoming)
			out[i].score += la
			floor = min(floor, la)
		}
		for k, i := range order {
			if k >= planBeam {
				out[i].score += floor - 1
			}
		}
	}
	if proj != nil {
		for i := range out {
			out[i].score -= proj.penalty(out[i])
		}
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

// evaluateBoard scores one placement: the Dellacherie feature combination
// (the features are the grid's, engine.go, a row at a time).
func evaluateBoard(g *grid, dest [4]cell, lines, eroded int) float64 {
	return weightLandingHeight*landingHeight(g, dest) +
		weightErodedCells*float64(eroded) +
		weightRowTransitions*float64(g.rowTransitions()) +
		weightColTransitions*float64(g.colTransitions()) +
		weightHoles*float64(g.holes()) +
		weightWells*float64(g.wells())
}

func landingHeight(g *grid, dest [4]cell) float64 {
	sum := 0.0
	for _, c := range dest {
		sum += float64(g.h - 1 - c.r)
	}
	return sum / float64(len(dest))
}

const (
	// planBeam is how many of the piece's own placements — the best by their
	// one-ply evaluation — get the lookahead below. Every placement used to:
	// 48 of them on a 14-column board, each searching 121 nodes of 48
	// placements — 1.7 s a plan, longer than a human takes, and longer than
	// any teammate should have to wait for a claim.
	planBeam               = 6
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
