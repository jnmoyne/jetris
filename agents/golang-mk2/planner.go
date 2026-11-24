package main

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
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
// waitOn, on a coordinated board, is the seats whose claimed landings hold
// the placement up — it must not lock before theirs have (coord.go).
type placement struct {
	orient, col, dropRow int
	dest                 [4]cell
	lines                int
	score                float64
	waitOn               []int
	// pv is the line the lookahead found best after this placement, one
	// placement per upcoming piece (principalVariation's); nil for a
	// placement outside the beam, which had no lookahead of its own.
	pv []placement
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
	// route is what the walk to each (orientation, column) costs, wait
	// for a crewmate's piece included (coord.go routeCosts); routeNow says
	// that what is not in it cannot be reached at all: it was made for a
	// blocked piece, on the board as it is, or for one gravity will have
	// taken down among the stack before it moves. nil: the flat costs below.
	route    map[[2]int]float64
	routeNow bool
	// tent is the cells of the teammates' tentative placements for their
	// NEXT pieces (claim.Next), each with its weight — a nudge away, never a
	// wall; intent is the placement (orientation, column) we announced for
	// the piece now in play, which gets intentBonus for being kept; well is
	// the column the crew keeps open for a four-row clear, while wellOn,
	// and wellHigh says the stack beside it stands high (coord.go wellFor).
	tent     map[cell]float64
	intent   *[2]int
	well     int
	wellOn   bool
	wellHigh bool
}

// wellSpec is the well the plan keeps, noWell when it keeps none.
func (proj *projection) wellSpec() wellSpec {
	if !proj.wellOn {
		return noWell
	}
	return wellSpec{col: proj.well, high: proj.wellHigh}
}

// penalty is what a placement costs beyond its evaluation, in Dellacherie's
// units (a hole is 7.9): waiting on a teammate's claim in proportion to how
// long it is expected to take; every cell of the placement in, or right on
// top of, a silent teammate's straight drop; and the walk there — what the
// path search says it costs, a crewmate's piece across the way priced by
// the wait for it (route); or, on a projection without a route, a little
// per column from our spawn point and every imminent teammate spawn box
// the walk crosses.
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
	for _, d := range cand.dest {
		p += tentPenalty * proj.tent[d]
	}
	if proj.intent != nil && *proj.intent == [2]int{cand.orient, cand.col} {
		p -= intentBonus
	}
	if proj.route != nil {
		// The walk as the path search finds it, and the wait when a
		// crewmate's piece is across it: the whole board is one piece's to
		// play on, and a far column costs what getting there costs.
		if c, ok := proj.route[[2]int{cand.orient, cand.col}]; ok {
			return p + c
		}
		if proj.routeNow {
			return p + routeBlocked
		}
		return p + depPenalty*silentLeave.Seconds()
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
	return planPlacementsFrom(g, pt, 0, sRow, sCol, spawnC, upcoming, proj)
}

// planPlacementsFrom is planPlacements for a piece standing in the
// orientation sOrient: a piece planned again where it stands (a re-plan) has
// turned since its spawn, and the orientations it can reach are the ones a
// rotation in place takes it to from THERE.
func planPlacementsFrom(g *grid, pt, sOrient, sRow, sCol, spawnC int, upcoming []int, proj *projection) []placement {
	cands := enumerateFrom(nil, g, pt, sOrient, sRow, sCol)
	out := make([]placement, 0, len(cands))
	afters := make([]*grid, 0, len(cands))
	// Every placement by its one-ply evaluation first…
	well := noWell
	if proj != nil {
		well = proj.wellSpec()
	}
	for _, cand := range cands {
		after, lines, eroded := simulateLock(g, cand.dest)
		cand.lines = lines
		cand.score = evaluateWell(after, cand.dest, lines, eroded, well)
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
		// The beam's lookaheads are independent searches, each from its
		// own board: they run side by side, one goroutine each, which is
		// what keeps a plan at the full preview under a think pause on a
		// wide board (python-mk2 runs them on worker processes for the
		// same reason). Each keeps its line, for the claim's next pieces.
		beam := order[:min(planBeam, len(order))]
		las := make([]float64, len(beam))
		var wg sync.WaitGroup
		for k, i := range beam {
			wg.Add(1)
			go func() {
				defer wg.Done()
				las[k], out[i].pv = lookaheadLine(afters[i], spawnC, upcoming, well, true)
			}()
		}
		wg.Wait()
		floor := math.Inf(1)
		for k, i := range beam {
			out[i].score += las[k]
			floor = min(floor, las[k])
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
	return enumerateInto(nil, g, pt, sRow, sCol)
}

// enumerateInto is enumerate appending to out, for a search that reuses one
// buffer for every board it expands.
func enumerateInto(out []placement, g *grid, pt, sRow, sCol int) []placement {
	return enumerateFrom(out, g, pt, 0, sRow, sCol)
}

// enumerateFrom is enumerateInto for a piece standing in the orientation
// sOrient (0 at its spawn).
func enumerateFrom(out []placement, g *grid, pt, sOrient, sRow, sCol int) []placement {
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
	if out == nil {
		out = make([]placement, 0, n*g.w)
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
// scores — a plan at the full preview scores some three hundred thousand of
// them. Returns the board to evaluate: scratch itself, good until its next
// use, or a new one when the lock clears rows.
func simulateOn(scratch, g *grid, dest [4]cell) (*grid, int, int) {
	copy(scratch.cells, g.cells)
	copy(scratch.rows, g.rows)
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

// wellSpec is the well a plan keeps: its column (-1: none), and whether
// the stack beside it stands high — then an I piece takes whatever rows the
// well gives it, instead of waiting for four.
type wellSpec struct {
	col  int
	high bool
}

// noWell is the plan that keeps no well.
var noWell = wellSpec{col: -1}

const (
	// wellSpoil is what a cell put in the crew's well costs a placement
	// that does not clear four rows with it, in Dellacherie's units: more
	// than a hole — the well is what the four-row clear is made of.
	wellSpoil = 10.0
	// wellCover is what every empty cell of the well left UNDER such a
	// placement costs it: a piece laid across the top of the well buries
	// the rows banked beside it, and nothing but digging gets them back.
	wellCover = 25.0
)

// evaluateWell is evaluateBoard for a crew keeping a column open (ws;
// noWell: it is evaluateBoard exactly): the features are taken with the
// well's column counted as filled — the board is played as if it were a
// column narrower, so rows full but for the well read as full, and the
// well itself as neither a well nor a hole — and a row banked beside it as
// the empty row a clear will make of it. A placement that puts cells in
// the well without clearing four rows pays wellSpoil a cell — unless the
// stack is high and the cells sit on the well's floor: an I piece cashing
// the rows there are — and wellCover for every empty cell of the well it
// leaves under it. The four-row clear needs no reward of its own: eroded
// cells pay it 16.
func evaluateWell(g *grid, dest [4]cell, lines, eroded int, ws wellSpec) float64 {
	well := ws.col
	if well < 0 || well >= g.w {
		return evaluateBoard(g, dest, lines, eroded)
	}
	var buf [64]uint64
	saved := append(buf[:0], g.rows...)
	bit := uint64(1) << uint(well)
	for r := range g.rows {
		g.rows[r] |= bit
	}
	score := evaluateBoard(g, dest, lines, eroded)
	// A row full but for the well has no transitions at all, where the
	// empty row a clear leaves in its place has two (the walls count as
	// filled): left at that, every row banked beside the well is worth two
	// transitions to keep, and the evaluator hoards rows it could clear —
	// it would rather lay an I flat than send it down a well seven rows
	// deep. A banked row is counted as the empty row it is about to be.
	banked := 0
	for _, m := range g.rows {
		if m == g.full {
			banked++
		}
	}
	score += weightRowTransitions * float64(2*banked)
	copy(g.rows, saved)
	if lines >= 4 {
		return score
	}
	in := 0
	for _, c := range dest {
		if c.c == well {
			in++
		}
	}
	if in == 0 {
		return score
	}
	// What is empty in the well with something over it is buried: the well
	// was open before this piece (wellFor), so this piece is what buried
	// it — laid across the top, or hung into it from the side.
	under, covered := 0, false
	for r := 0; r < g.h; r++ {
		if g.filled(r, well) {
			covered = true
		} else if covered {
			under++
		}
	}
	if !(ws.high && under == 0) {
		score -= wellSpoil * float64(in)
	}
	return score - wellCover*float64(under)
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
	return lookaheadWell(g, spawnC, upcoming, noWell)
}

// lookaheadWell is lookahead for a crew keeping a well (ws; noWell: none):
// every ply is evaluated as the plan's own placements are
// (evaluateWell).
func lookaheadWell(g *grid, spawnC int, upcoming []int, ws wellSpec) float64 {
	score, _ := lookaheadLine(g, spawnC, upcoming, ws, false)
	return score
}

// principalVariation is the line the lookahead found best from board g:
// one placement per upcoming piece, in order — where the planner means to
// put the pieces its NEXT well shows, as things stand. Shorter than
// `upcoming` when a piece down the line cannot spawn.
func principalVariation(g *grid, spawnC int, upcoming []int, ws wellSpec) []placement {
	_, line := lookaheadLine(g, spawnC, upcoming, ws, true)
	return line
}

// lookaheadLine is the search behind lookahead and principalVariation: the
// best total, and — when asked (keep) — the placements that make it.
func lookaheadLine(g *grid, spawnC int, upcoming []int, ws wellSpec, keep bool) (float64, []placement) {
	s := lookaheadSearch{scratch: newGrid(g.h, g.w), spawnC: spawnC, ws: ws, keep: keep}
	return s.from(g, upcoming)
}

// lookaheadSearch is what one lookahead works with, from its first board to
// its last: the board it scores every placement on (simulateOn) and the
// buffers it lists and ranks a board's placements in, used again for every
// board it expands — a search at the full preview expands 364 of them, with
// a placement per orientation and column each, and a board and two lists
// allocated for each one is what a plan's time used to go on. Only the
// lookaheadBeam placements a level goes on from get a board of their own.
type lookaheadSearch struct {
	scratch *grid
	cands   []placement
	ranked  []scoredPlacement
	spawnC  int
	ws      wellSpec
	keep    bool
}

type scoredPlacement struct {
	score float64
	pl    placement
}

func (s *lookaheadSearch) from(g *grid, upcoming []int) (float64, []placement) {
	if len(upcoming) == 0 {
		return 0, nil
	}
	pt := upcoming[0]
	if !g.canPlace(pieceCells(pt, 0, spawnRow, s.spawnC)) {
		return lookaheadTopOutPenalty, nil
	}
	if s.cands == nil {
		s.cands = make([]placement, 0, 4*g.w)
		s.ranked = make([]scoredPlacement, 0, 4*g.w)
	}
	s.cands = enumerateInto(s.cands[:0], g, pt, spawnRow, s.spawnC)
	if len(s.cands) == 0 {
		return lookaheadTopOutPenalty, nil
	}
	xs := s.ranked[:0]
	for _, cand := range s.cands {
		after, lines, eroded := simulateOn(s.scratch, g, cand.dest)
		cand.lines = lines
		xs = append(xs, scoredPlacement{evaluateWell(after, cand.dest, lines, eroded, s.ws), cand})
	}
	s.ranked = xs
	slices.SortStableFunc(xs, func(a, b scoredPlacement) int { return cmp.Compare(b.score, a.score) })
	if len(upcoming) == 1 {
		if s.keep {
			return xs[0].score, []placement{xs[0].pl}
		}
		return xs[0].score, nil
	}
	// The levels below use the buffers again: what this one goes on from
	// is copied out of them first.
	var beam [lookaheadBeam]scoredPlacement
	n := copy(beam[:], xs)
	best, bestLine := 0.0, []placement(nil)
	for i, e := range beam[:n] {
		board, _, _ := simulateLock(g, e.pl.dest)
		rest, line := s.from(board, upcoming[1:])
		if t := e.score + rest; i == 0 || t > best {
			best = t
			if s.keep {
				bestLine = append([]placement{e.pl}, line...)
			}
		}
	}
	return best, bestLine
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
