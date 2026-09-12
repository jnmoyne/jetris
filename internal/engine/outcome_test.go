package engine

import (
	"context"
	"testing"

	"jetris/internal/config"
)

// A shared board whose seats are scored on their own: another player's
// line_clear folds the crew's LINES — the shared level and gravity — but
// never their points into the shown score, which stays this player's alone.
// Every sender's total is still tracked for the ranking.
func TestIndividualScoringKeepsOwnScore(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.individual = true
	ctx := context.Background()

	e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "other", Score: 20, LinesCleared: 10, TotalScore: 20, TotalLines: 10})
	if e.Score() != 0 {
		t.Fatalf("score = %d, want 0: another seat's points are not ours", e.Score())
	}
	if e.Level() != 2 {
		t.Fatalf("level = %d, want 2 after folding 10 shared lines", e.Level())
	}
	if got := e.PlayerScores()["other"]; got != 20 {
		t.Fatalf("PlayerScores()[other] = %d, want 20", got)
	}
	if got := e.PlayerLines()["other"]; got != 10 {
		t.Fatalf("PlayerLines()[other] = %d, want 10", got)
	}
	if !e.IndividualScoring() {
		t.Fatal("IndividualScoring() = false")
	}
	if _, _, decided := e.Winners(); decided {
		t.Fatal("the game is decided before anyone topped out")
	}
}

// The top-out on a shared board scored per seat ends the game for everyone
// and crowns the top scorer(s) — decided from the totals the ordered event
// stream carried, our own echo included, on every engine alike; a tie crowns
// everyone tied. The beaten see their loss, the winners their win.
func TestIndividualScoringTopOutRanks(t *testing.T) {
	ctx := context.Background()
	run := func(mine, theirs int) *Engine {
		e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
		e.individual = true
		e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "me", Score: mine, TotalScore: mine})
		e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "other", Score: theirs, TotalScore: theirs})
		// The other player tops out: their game_over carries their final total.
		e.handleGameEvent(ctx, GameEvent{Kind: EventGameOver, PlayerID: "other", Score: theirs, TotalScore: theirs})
		return e
	}

	lost := run(50, 80)
	winners, winTeam, decided := lost.Winners()
	if !decided || winTeam != -1 || !winners["other"] || winners["me"] || len(winners) != 1 {
		t.Fatalf("Winners() = %v, %d, %v; want other alone", winners, winTeam, decided)
	}
	if won, over := lost.GameOutcome(); !over || won {
		t.Fatalf("GameOutcome() = won %v over %v; want lost", won, over)
	}
	if lost.Mode() != ModeGameOver {
		t.Fatalf("mode = %v, want game over", lost.Mode())
	}

	won := run(100, 80)
	winners, _, decided = won.Winners()
	if !decided || !winners["me"] || len(winners) != 1 {
		t.Fatalf("Winners() = %v, %v; want me alone", winners, decided)
	}
	if w, over := won.GameOutcome(); !over || !w {
		t.Fatalf("GameOutcome() = won %v over %v; want won", w, over)
	}

	tie := run(80, 80)
	winners, _, _ = tie.Winners()
	if !winners["me"] || !winners["other"] {
		t.Fatalf("a tie crowns everyone tied, got %v", winners)
	}

	// A second decision is a no-op: the first verdict stands.
	if tie.decideOutcome(map[string]bool{"nobody": true}, 3, false) {
		t.Fatal("decideOutcome decided twice")
	}
	if winners, winTeam, _ := tie.Winners(); winners["nobody"] || winTeam != -1 {
		t.Fatalf("the verdict moved: %v, %d", winners, winTeam)
	}
}

// The ranking follows the roster once the lobby pushed one: a seated player
// who never scored is ranked (at zero), and a departed player's stale total
// is not.
func TestTopScorersFollowTheRoster(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	e.individual = true
	ctx := context.Background()
	e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "gone", Score: 500, TotalScore: 500})
	e.SetRoster([]Seat{{PlayerID: "me", Seat: 0}, {PlayerID: "quiet", Seat: 1}})
	if w := e.topScorers(); !w["me"] || !w["quiet"] || w["gone"] {
		t.Fatalf("topScorers() = %v, want the two seated players tied at zero", w)
	}
}
