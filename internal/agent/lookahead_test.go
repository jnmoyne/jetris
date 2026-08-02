package agent

import (
	"testing"

	"jetris/internal/game"
)

// With Lookahead 0 the preview must be ignored entirely: passing upcoming
// pieces yields exactly the same ranking and scores as passing none. This is
// the knob half of the visibility contract — the other half (callers hand the
// planner at most the game's revealed preview) lives at the call site.
func TestLookaheadZeroIgnoresUpcoming(t *testing.T) {
	pf := newBoard()
	bottom := pf.Height - 1
	for c := 0; c < 9; c++ {
		lockCell(pf, bottom, c)
	}
	p := spawnOn(pf, game.PieceT, 0)

	tn := Tuning{} // Lookahead 0
	without := PlanPlacements(pf, Rules{}, p, tn)
	with := PlanPlacements(pf, Rules{}, p, tn, game.PieceI, game.PieceO)

	if len(with) != len(without) {
		t.Fatalf("placement count changed: %d vs %d", len(with), len(without))
	}
	for i := range with {
		if with[i].Target != without[i].Target || with[i].Score != without[i].Score {
			t.Fatalf("rank %d differs with ignored upcoming: %+v vs %+v", i, with[i], without[i])
		}
	}
}

// With lookahead, the planner must reserve a line the next piece can finish:
// bottom row open only at columns 0-3, active O, revealed next piece I. The
// horizontal I fills the gap exactly, so the O must stay out of it.
func TestLookaheadReservesClearForNextPiece(t *testing.T) {
	pf := newBoard()
	bottom := pf.Height - 1
	for c := 4; c < pf.Width; c++ {
		lockCell(pf, bottom, c)
	}
	p := spawnOn(pf, game.PieceO, 0)

	tn := DifficultyHard.Tuning() // Lookahead = full preview
	ranked := PlanPlacements(pf, Rules{}, p, tn, game.PieceI)
	if len(ranked) == 0 {
		t.Fatal("no placements")
	}
	for _, c := range ranked[0].Target.Cells() {
		if c[0] == bottom && c[1] < 4 {
			t.Fatalf("best placement %+v fills the gap the revealed I would clear", ranked[0].Target)
		}
	}
}

// A lookahead piece that cannot be placed at all (spawn blocked) must score
// the top-out penalty, steering plans away from placements that kill us next.
func TestLookaheadSpawnBlockedPenalty(t *testing.T) {
	pf := newBoard()
	// Wall off the spawn rows completely.
	for r := 0; r < 6; r++ {
		for c := 0; c < pf.Width; c++ {
			lockCell(pf, r, c)
		}
	}
	if got := lookaheadScore(pf, Rules{}, []game.PieceType{game.PieceI}); got != lookaheadTopOutPenalty {
		t.Fatalf("lookaheadScore = %v, want top-out penalty %v", got, lookaheadTopOutPenalty)
	}
}
