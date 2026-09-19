package engine

import (
	"context"
	"testing"

	"jetris/internal/config"
)

// TallyEvent counts a lock by every name its event carries: the clear by
// its size, the spin, the Back-to-Back, the longest combo, the perfect
// clear, the attack it sent — by the game's rule — and the sender's piece
// count, kept at the highest announced (a game_over's too).
func TestTallyEvent(t *testing.T) {
	var st config.PlayerStats
	events := []GameEvent{
		{Kind: EventLineClear, LinesCleared: 1, PieceCount: 3},
		{Kind: EventLineClear, LinesCleared: 2, Combo: 1, PieceCount: 4},
		{Kind: EventLineClear, LinesCleared: 4, Combo: 2, PieceCount: 5},                                  // a quad: starts the chain
		{Kind: EventLineClear, LinesCleared: 4, BackToBack: true, Combo: 3, Perfect: true, PieceCount: 6}, // Back-to-Back quad, perfect
		{Kind: EventLineClear, LinesCleared: 0, TSpin: 2, PieceCount: 7},                                  // a T-spin that cleared nothing
		{Kind: EventLineClear, LinesCleared: 2, TSpin: 2, BackToBack: true, PieceCount: 8},                // T-spin double, Back-to-Back
		{Kind: EventLineClear, LinesCleared: 1, TSpin: 1, PieceCount: 9},                                  // Mini T-spin single: breaks nothing, extends nothing
		{Kind: EventLineClear, LinesCleared: 3, TSpin: 3, PieceCount: 10},                                 // a 180 spin triple
		{Kind: EventGameOver, PieceCount: 12},
	}
	for _, ev := range events {
		TallyEvent(&st, ev, true, true)
	}
	want := config.PlayerStats{
		Singles: 2, Doubles: 2, Triples: 1, Quads: 2,
		TSpins: 3, MiniTSpins: 1, BackToBacks: 2, MaxCombo: 3, PerfectClears: 1,
		// The Guideline attack table: single 0, double 1, quad 4, B2B perfect
		// quad 4+2+10, T-spin double B2B 4+2, Mini single 0, 180 triple 6.
		Attack: 0 + 1 + 4 + 16 + 0 + 6 + 0 + 6,
		Pieces: 12,
	}
	if st != want {
		t.Errorf("tally = %+v\nwant    %+v", st, want)
	}

	// One row per line where the game attacks by lines, and no attack at
	// all on the crew's board.
	var plain, coop config.PlayerStats
	TallyEvent(&plain, GameEvent{Kind: EventLineClear, LinesCleared: 3}, true, false)
	TallyEvent(&coop, GameEvent{Kind: EventLineClear, LinesCleared: 3}, false, true)
	if plain.Attack != 3 || coop.Attack != 0 {
		t.Errorf("attack: plain-rule triple sent %d (want 3), crew's triple sent %d (want 0)", plain.Attack, coop.Attack)
	}
}

// An engine tallies every seat's events as it folds their totals — its own
// echo included, so its own tally is the one every other engine has — and a
// replay of an event already folded (the same totals again) counts nothing
// twice.
func TestPlayerStatsFold(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	ctx := context.Background()
	quad := GameEvent{Kind: EventLineClear, PlayerID: "them", LinesCleared: 4, Score: 800, TotalScore: 900, TotalLines: 5, PieceCount: 9}
	for _, ev := range []GameEvent{
		{Kind: EventLineClear, PlayerID: "them", LinesCleared: 1, Score: 100, TotalScore: 100, TotalLines: 1, PieceCount: 4},
		quad,
		quad, // the ordered stream read again: already folded
		{Kind: EventLineClear, PlayerID: "them", TSpin: 2, Score: 400, TotalScore: 1300, TotalLines: 5, PieceCount: 13},
		{Kind: EventLineClear, PlayerID: "me", LinesCleared: 2, Combo: 1, Score: 350, TotalScore: 350, TotalLines: 2, PieceCount: 6},
	} {
		e.handleGameEvent(ctx, ev)
	}
	them := e.PlayerStats()["them"]
	if want := (config.PlayerStats{Singles: 1, Quads: 1, TSpins: 1, Attack: 5, Pieces: 13}); them != want {
		t.Errorf("their tally = %+v, want %+v", them, want)
	}
	me := e.OwnStats()
	if me.Doubles != 1 || me.MaxCombo != 1 || me.Pieces != 6 {
		t.Errorf("own tally = %+v, want the echoed double (combo 1, 6 pieces)", me)
	}
	if _, ok := e.PlayerStats()["me"]; !ok {
		t.Error("the own echo left no row in PlayerStats")
	}
}
