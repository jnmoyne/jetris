package agent

import (
	"math/rand/v2"
	"testing"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/game"
)

// newBoard returns an empty competitive 2-player board (10 wide, 30 rows).
func newBoard() *game.Playfield {
	return game.NewPlayfieldWithHeight(config.StandardWidth, config.CompetitiveTotalRows(2))
}

// lockCell stamps a locked stack cell.
func lockCell(pf *game.Playfield, row, col int) {
	pf.Rows[row].Cells[col] = game.Cell{Occupied: true, PieceType: game.PieceO}
}

// garbageRow fills a full row with permanent adversarial cells, like a
// competitive shrink does.
func garbageRow(pf *game.Playfield, row int) {
	for c := 0; c < pf.Width; c++ {
		pf.Rows[row].Cells[c] = game.Cell{Occupied: true, PieceType: game.PieceO, Adversarial: true}
	}
}

func spawnOn(pf *game.Playfield, pt game.PieceType, playerIdx int) game.Piece {
	p := game.SpawnPiece(pt, pf.Width)
	pf.SetActivePieceForPlayer(p, playerIdx)
	return p
}

// enumerationCount is the classic distinct-placement count on an empty
// 10-wide board: pieces with 2 distinct orientations (I, S, Z) have 7+8=15 or
// 9+8=17, 4-orientation pieces (T, J, L) 9+8+9+8=34, and O has 9.
func TestEnumerationCounts(t *testing.T) {
	cases := []struct {
		pt   game.PieceType
		want int
	}{
		{game.PieceO, 9},  // 2x2 block: 9 columns
		{game.PieceI, 17}, // horizontal 7 + vertical 10
		{game.PieceS, 17}, // horizontal 8 + vertical 9
		{game.PieceZ, 17},
		{game.PieceT, 34}, // two 8-wide + two 9-wide orientations
		{game.PieceJ, 34},
		{game.PieceL, 34},
	}
	for _, tc := range cases {
		pf := newBoard()
		p := spawnOn(pf, tc.pt, 0)
		got := enumerate(pf, Rules{}, p)
		// Distinct final cell sets, to make the count independent of script paths.
		seen := map[string]bool{}
		for _, c := range got {
			seen[shapePosKey(c.dest)] = true
		}
		if len(seen) != tc.want {
			t.Errorf("piece %v: got %d distinct placements, want %d", tc.pt, len(seen), tc.want)
		}
		if len(got) != len(seen) {
			t.Errorf("piece %v: %d candidates but %d distinct placements (duplicates)", tc.pt, len(got), len(seen))
		}
	}
}

// shapePosKey identifies a placement by its final absolute cells.
func shapePosKey(p game.Piece) string {
	key := ""
	for _, c := range p.Cells() {
		key += string(rune('A'+c[0])) + string(rune('A'+c[1]))
	}
	return key
}

// Every candidate's script must replay successfully move by move with the
// engine's own rules and end exactly at the candidate's destination.
func TestEnumeratedScriptsReplay(t *testing.T) {
	pf := newBoard()
	// A jagged stack to force kicks/rejections into the mix.
	for c := 0; c < 5; c++ {
		lockCell(pf, pf.Height-1, c)
		lockCell(pf, pf.Height-2, c)
	}
	lockCell(pf, pf.Height-1, 7)
	p := spawnOn(pf, game.PieceJ, 0)

	for _, cand := range enumerate(pf, Rules{}, p) {
		q := p
		for _, mv := range cand.moves {
			switch mv {
			case engine.RotateCW, engine.RotateCCW:
				var ok bool
				q, ok = game.Rotate(q, mv == engine.RotateCW, pf)
				if !ok {
					t.Fatalf("script rotation rejected for dest %+v", cand.dest)
				}
			case engine.MoveLeft, engine.MoveRight:
				if mv == engine.MoveLeft {
					q.Col--
				} else {
					q.Col++
				}
				if !game.CanPlace(q, pf) {
					t.Fatalf("script slide rejected for dest %+v", cand.dest)
				}
			case engine.MoveHardDrop:
				q = game.HardDropDestination(q, pf)
			}
		}
		if q != cand.dest {
			t.Fatalf("script ends at %+v, want %+v", q, cand.dest)
		}
	}
}

// The heuristic must prefer completing a line over burying a hole.
func TestPrefersClearOverHole(t *testing.T) {
	pf := newBoard()
	bottom := pf.Height - 1
	// Bottom row full except column 9; column 9 open all the way down.
	for c := 0; c < 9; c++ {
		lockCell(pf, bottom, c)
	}
	p := spawnOn(pf, game.PieceI, 0)

	ranked := PlanPlacements(pf, Rules{}, p, DifficultyHard.Tuning())
	if len(ranked) == 0 {
		t.Fatal("no placements")
	}
	best := ranked[0]
	if best.Lines != 1 {
		t.Fatalf("best placement clears %d lines, want 1 (target %+v, score %v)", best.Lines, best.Target, best.Score)
	}
	// Vertical I in the open column is the only 1-line clear here.
	if best.Target.Orientation%2 != 1 {
		t.Errorf("expected a vertical I placement, got %+v", best.Target)
	}
}

// A full adversarial garbage row must never be counted as clearable.
func TestGarbageRowNeverClears(t *testing.T) {
	pf := newBoard()
	bottom := pf.Height - 1
	garbageRow(pf, bottom)
	// The row above the garbage: full except column 9.
	for c := 0; c < 9; c++ {
		lockCell(pf, bottom-1, c)
	}
	p := spawnOn(pf, game.PieceI, 0)

	for _, cand := range enumerate(pf, Rules{}, p) {
		sim, lines, _ := simulateLock(pf, 0, cand.dest)
		if lines > 1 {
			t.Fatalf("placement %+v clears %d lines; garbage row must not clear", cand.dest, lines)
		}
		if sim.AdversarialRowCount() != 1 {
			t.Fatalf("placement %+v: garbage row disappeared from the simulated board", cand.dest)
		}
	}

	// And the vertical-I clear of the row ABOVE the garbage still works.
	ranked := PlanPlacements(pf, Rules{}, p, DifficultyHard.Tuning())
	if ranked[0].Lines != 1 {
		t.Fatalf("best placement clears %d lines, want 1", ranked[0].Lines)
	}
}

// The blunder model must respect its rate and depth.
func TestChoosePlacementBlunders(t *testing.T) {
	ranked := []Placement{
		{Score: 3}, {Score: 2}, {Score: 1}, {Score: 0},
	}
	rnd := rand.New(rand.NewPCG(1, 2))

	// Rate 0: always the best.
	for i := 0; i < 100; i++ {
		pl, ok := ChoosePlacement(ranked, Tuning{}, rnd)
		if !ok || pl.Score != 3 {
			t.Fatal("rate 0 must always pick the top placement")
		}
	}

	// Rate 1, depth 2: never the best, only ranks 2-3.
	counts := map[float64]int{}
	for i := 0; i < 1000; i++ {
		pl, _ := ChoosePlacement(ranked, Tuning{BlunderRate: 1, BlunderDepth: 2}, rnd)
		counts[pl.Score]++
	}
	if counts[3] != 0 || counts[0] != 0 {
		t.Fatalf("depth-2 blunders picked out-of-range ranks: %v", counts)
	}
	if counts[2] == 0 || counts[1] == 0 {
		t.Fatalf("depth-2 blunders not spread over ranks 2-3: %v", counts)
	}

	// Empty list.
	if _, ok := ChoosePlacement(nil, Tuning{}, rnd); ok {
		t.Error("empty list must return ok=false")
	}
}

// On a shared (coop/teams) board a teammate's mid-flight piece is an
// obstacle: no enumerated placement may overlap it, drops rest on top of it,
// and every destination must satisfy the coop collision rules.
func TestEnumerateSharedBoardAvoidsTeammate(t *testing.T) {
	// A 2-player cooperative board: 20 wide.
	pf := game.NewPlayfieldWithHeight(2*config.StandardWidth, config.HeadroomRows+config.VisibleRows)
	rules := Rules{Shared: true, PlayerIdx: 0, SectionIdx: 0}

	// Our piece spawns in section 0; the teammate's active piece sits low in
	// the middle of the board, straddling the section boundary.
	our := game.SpawnPiece(game.PieceT, config.StandardWidth)
	pf.SetActivePieceForPlayer(our, 0)
	mate := game.Piece{Type: game.PieceO, Orientation: 0, Row: pf.Height - 2, Col: 9}
	pf.SetActivePieceForPlayer(mate, 1)

	mateCells := map[[2]int]bool{}
	for _, c := range mate.Cells() {
		mateCells[[2]int{c[0], c[1]}] = true
	}

	cands := enumerate(pf, rules, our)
	if len(cands) == 0 {
		t.Fatal("no placements on the shared board")
	}
	sawRestOnMate := false
	for _, cand := range cands {
		if !game.CanPlaceCoop(cand.dest, pf, 0) {
			t.Fatalf("dest %+v violates coop collision", cand.dest)
		}
		for _, c := range cand.dest.Cells() {
			if mateCells[[2]int{c[0], c[1]}] {
				t.Fatalf("dest %+v overlaps the teammate's active piece", cand.dest)
			}
			// A drop in the teammate's columns must rest ON the piece, i.e.
			// some cell directly above one of its cells.
			if c[0] == mate.Row-1 && (c[1] == 9 || c[1] == 10) {
				sawRestOnMate = true
			}
		}
	}
	if !sawRestOnMate {
		t.Error("expected at least one placement resting on top of the teammate's piece")
	}

	// Without shared rules the same board lets pieces fall THROUGH the
	// teammate's piece — proving the rules variant is what protects it.
	overlap := false
	for _, cand := range enumerate(pf, Rules{}, our) {
		for _, c := range cand.dest.Cells() {
			if mateCells[[2]int{c[0], c[1]}] {
				overlap = true
			}
		}
	}
	if !overlap {
		t.Error("competitive rules unexpectedly avoided the teammate's cells (test setup wrong?)")
	}
}
