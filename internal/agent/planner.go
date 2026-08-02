package agent

import (
	"fmt"
	"math/rand/v2"
	"sort"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
)

// Placement is one candidate final resting position for the active piece.
type Placement struct {
	Target game.Piece        // final resting position (Row = drop destination at plan time)
	Moves  []engine.MoveType // validated script: rotations, then translations, then MoveHardDrop
	Lines  int               // lines the simulated lock clears
	Score  float64           // heuristic value, including lookahead
}

// candidate is an enumerated placement before scoring.
type candidate struct {
	dest  game.Piece
	moves []engine.MoveType
}

// PlanPlacements enumerates every placement of the active piece reachable with
// the executor's move vocabulary (rotate in place, slide, hard drop), scores
// each with the Dellacherie heuristic, and returns them best first. pf must be
// a race-free copy (e.g. engine.Playfield()) with the active piece still
// stamped on it; active is that piece as returned by ActivePieceForPlayer. r
// selects the board's collision variant — on shared (coop/teams) boards other
// players' mid-flight pieces block exactly as the engine's own move validation
// would.
//
// upcoming is the piece preview the agent may use: the visibility contract
// (see jetris-agent-guide.md) is that an agent decides only on what a human
// player can see, so callers must pass at most the pieces the game reveals
// (Engine.NextPieces, bounded by GameMeta.NextCount — nothing in a game
// created with no preview). The planner uses at most tn.Lookahead of them:
// each candidate's score gains the best achievable evaluation total of the
// lookahead pieces played out on the simulated board (beam-pruned).
func PlanPlacements(pf *game.Playfield, r Rules, active game.Piece, tn Tuning, upcoming ...game.PieceType) []Placement {
	if tn.Lookahead < len(upcoming) {
		upcoming = upcoming[:max(tn.Lookahead, 0)]
	}
	cands := enumerate(pf, r, active)
	out := make([]Placement, 0, len(cands))
	for _, cand := range cands {
		board, lines, eroded := simulateLock(pf, r.PlayerIdx, cand.dest)
		out = append(out, Placement{
			Target: cand.dest,
			Moves:  cand.moves,
			Lines:  lines,
			Score:  evaluateMove(board, cand.dest, lines, eroded) + lookaheadScore(board, r, upcoming),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

const (
	// lookaheadBeam bounds the search: at each lookahead level only this many
	// best placements are expanded further. With ~34 placements per piece and
	// up to config.MaxNextCount (4) levels, the full tree stays in the tens of
	// thousands of board evaluations — comfortably per-piece realtime.
	lookaheadBeam = 3
	// lookaheadTopOutPenalty scores a lookahead piece that cannot be placed at
	// all (spawn blocked on the simulated board — an imminent top-out).
	lookaheadTopOutPenalty = -1e4
)

// lookaheadScore returns the best evaluation total achievable by playing the
// upcoming pieces, in order, on board pf: max over the first piece's
// placements of eval(placement) + lookaheadScore(rest), expanding only the
// lookaheadBeam best-scoring placements at each level. pf is a simulation
// board (already cloned) and is not mutated.
func lookaheadScore(pf *game.Playfield, r Rules, upcoming []game.PieceType) float64 {
	if len(upcoming) == 0 {
		return 0
	}
	spawn := game.SpawnPiece(upcoming[0], config.StandardWidth)
	spawn.Col += r.SectionIdx * config.StandardWidth // own section on shared boards, same offset engine.spawnPiece applies
	// enumerate assumes a validly placed active piece, so the spawn itself
	// must be checked first — a covered spawn is the engine's top-out.
	if !r.canPlace(spawn, pf) {
		return lookaheadTopOutPenalty
	}
	cands := enumerate(pf, r, spawn)
	if len(cands) == 0 {
		return lookaheadTopOutPenalty
	}
	type expanded struct {
		board *game.Playfield
		score float64
	}
	exp := make([]expanded, 0, len(cands))
	for _, cand := range cands {
		board, lines, eroded := simulateLock(pf, r.PlayerIdx, cand.dest)
		exp = append(exp, expanded{board, evaluateMove(board, cand.dest, lines, eroded)})
	}
	sort.Slice(exp, func(i, j int) bool { return exp[i].score > exp[j].score })
	if len(upcoming) == 1 {
		return exp[0].score
	}
	if len(exp) > lookaheadBeam {
		exp = exp[:lookaheadBeam]
	}
	best := exp[0].score + lookaheadScore(exp[0].board, r, upcoming[1:])
	for _, e := range exp[1:] {
		if s := e.score + lookaheadScore(e.board, r, upcoming[1:]); s > best {
			best = s
		}
	}
	return best
}

// ChoosePlacement applies the blunder model to a ranked placement list: with
// probability tn.BlunderRate it picks uniformly among ranks 2..1+BlunderDepth
// instead of the best. Returns false only for an empty list.
func ChoosePlacement(ranked []Placement, tn Tuning, rnd *rand.Rand) (Placement, bool) {
	if len(ranked) == 0 {
		return Placement{}, false
	}
	if rnd != nil && tn.BlunderRate > 0 && len(ranked) > 1 && rnd.Float64() < tn.BlunderRate {
		depth := tn.BlunderDepth
		if depth > len(ranked)-1 {
			depth = len(ranked) - 1
		}
		if depth > 0 {
			return ranked[1+rnd.IntN(depth)], true
		}
	}
	return ranked[0], true
}

// rotationPlans are the four ways the executor reaches a target orientation:
// stay, one CW, one CCW, two CW. Listed shortest first so orientation-shape
// dedupe keeps the cheapest script.
var rotationPlans = [][]engine.MoveType{
	nil,
	{engine.RotateCW},
	{engine.RotateCCW},
	{engine.RotateCW, engine.RotateCW},
}

// enumerate returns every final resting position reachable by simulating the
// exact script the executor will run: SRS rotations (with wall kicks) at the
// piece's current position, then one-column slides gated by the board's
// collision rules, then a hard drop. Unreachable placements are skipped, so
// the executor is never handed an impossible target. Orientations whose cell
// shape duplicates an already-enumerated one (O all four, I/S/Z pairs) are
// pruned.
func enumerate(pf *game.Playfield, r Rules, active game.Piece) []candidate {
	var out []candidate
	shapes := make(map[string]bool, 4)

	for _, plan := range rotationPlans {
		p := active
		ok := true
		for _, mv := range plan {
			p, ok = r.rotate(p, mv == engine.RotateCW, pf)
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		key := shapeKey(p)
		if shapes[key] {
			continue
		}
		shapes[key] = true

		record := func(q game.Piece, slides int, slide engine.MoveType) {
			moves := make([]engine.MoveType, 0, len(plan)+slides+1)
			moves = append(moves, plan...)
			for i := 0; i < slides; i++ {
				moves = append(moves, slide)
			}
			moves = append(moves, engine.MoveHardDrop)
			out = append(out, candidate{dest: r.dropDest(q, pf), moves: moves})
		}

		record(p, 0, engine.MoveLeft)
		for q, n := p, 1; ; n++ {
			q.Col--
			if !r.canPlace(q, pf) {
				break
			}
			record(q, n, engine.MoveLeft)
		}
		for q, n := p, 1; ; n++ {
			q.Col++
			if !r.canPlace(q, pf) {
				break
			}
			record(q, n, engine.MoveRight)
		}
	}
	return out
}

// shapeKey returns a signature of the piece's cell layout normalized to its
// bounding box, so orientations that produce identical locked shapes (and thus
// identical drop results across the column sweep) collapse to one entry.
func shapeKey(p game.Piece) string {
	cells := p.Cells()
	minRow, minCol := cells[0][0], cells[0][1]
	for _, c := range cells[1:] {
		if c[0] < minRow {
			minRow = c[0]
		}
		if c[1] < minCol {
			minCol = c[1]
		}
	}
	norm := make([][2]int, len(cells))
	for i, c := range cells {
		norm[i] = [2]int{c[0] - minRow, c[1] - minCol}
	}
	sort.Slice(norm, func(i, j int) bool {
		if norm[i][0] != norm[j][0] {
			return norm[i][0] < norm[j][0]
		}
		return norm[i][1] < norm[j][1]
	})
	return fmt.Sprint(norm)
}

// simulateLock returns the board after playerIdx's active piece locks at dest:
// the old active cells are removed, dest is stamped as locked stack, and full
// rows are cleared (Row.IsFull keeps adversarial garbage rows out, mirroring
// the engine). Also returns the lines cleared and the eroded-cells feature
// (lines × piece cells in the cleared rows). pf is not mutated.
func simulateLock(pf *game.Playfield, playerIdx int, dest game.Piece) (*game.Playfield, int, int) {
	sim := pf.Clone()
	sim.ClearActiveCellsForPlayer(playerIdx)
	for _, c := range dest.Cells() {
		r, col := c[0], c[1]
		if r < 0 || r >= sim.Height || col < 0 || col >= sim.Width {
			continue
		}
		sim.Rows[r].Cells[col] = game.Cell{Occupied: true, PieceType: dest.Type, PlayerIdx: playerIdx}
	}

	destCellsInRow := make(map[int]int, 4)
	for _, c := range dest.Cells() {
		destCellsInRow[c[0]]++
	}
	var completed []int
	pieceCellsCleared := 0
	for r := range sim.Rows {
		if sim.Rows[r].IsFull() {
			completed = append(completed, r)
			pieceCellsCleared += destCellsInRow[r]
		}
	}
	if len(completed) > 0 {
		sim.Rows = sim.ProjectClearRows(completed, false)
	}
	return sim, len(completed), pieceCellsCleared * len(completed)
}
