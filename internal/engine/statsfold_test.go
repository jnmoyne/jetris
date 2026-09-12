package engine

import (
	"context"
	"slices"
	"testing"

	"jetris/internal/config"
)

// TestTeamStatsFoldOnAllEngines verifies that a teams-mode line-clear event
// updates the per-team scoreboard AND per-team level on an engine that is NOT
// on the clearing team (the same path spectators and opposing players take),
// while leaving the engine's own score/level untouched.
func TestTeamStatsFoldOnAllEngines(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeTeams, ModePlayer, 0, 0, 0)

	// The other team clears 10 lines in total → their level ticks to 1.
	// Events carry the sender's cumulative totals; receivers fold deltas.
	e.handleGameEvent(context.Background(), GameEvent{
		Kind: EventLineClear, PlayerID: "them", Team: 1,
		Score: 20, LinesCleared: 10, TotalScore: 20, TotalLines: 10,
	})

	if got, want := e.TeamScores(), []int{0, 20}; !slices.Equal(got, want) {
		t.Fatalf("TeamScores = %v, want %v", got, want)
	}
	if got, want := e.TeamLevels(), []int{1, 2}; !slices.Equal(got, want) {
		t.Fatalf("TeamLevels = %v, want %v", got, want)
	}
	if e.Score() != 0 || e.Level() != 1 {
		t.Fatalf("own score/level = %d/%d, want 0/1 (other team's clear)", e.Score(), e.Level())
	}
}

// TestTeamStatsFoldPastTwoTeams verifies the per-team scoreboard is as wide as
// the game: a three-way game folds team C's clears into a third column, and a
// stray event naming a team the game does not have is ignored rather than
// running off the end of the board.
func TestTeamStatsFoldPastTwoTeams(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeTeams, ModePlayer, 0, 0, 0)
	e.teamCount = 3

	e.handleGameEvent(context.Background(), GameEvent{
		Kind: EventLineClear, PlayerID: "them", Team: 2,
		Score: 40, LinesCleared: 10, TotalScore: 40, TotalLines: 10,
	})
	e.handleGameEvent(context.Background(), GameEvent{
		Kind: EventLineClear, PlayerID: "stray", Team: 5, // not a team of this game
		Score: 900, LinesCleared: 90, TotalScore: 900, TotalLines: 90,
	})

	if got, want := e.TeamScores(), []int{0, 0, 40}; !slices.Equal(got, want) {
		t.Fatalf("TeamScores = %v, want %v", got, want)
	}
	if got, want := e.TeamLevels(), []int{1, 1, 2}; !slices.Equal(got, want) {
		t.Fatalf("TeamLevels = %v, want %v", got, want)
	}
}

// TestCoopLineClearFoldsLinesAndLevel verifies that a cooperative line-clear
// event from another player folds the shared line total — so the receiver's
// level (HUD and archive) advances — in addition to the score delta.
func TestCoopLineClearFoldsLinesAndLevel(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)

	e.handleGameEvent(context.Background(), GameEvent{
		Kind: EventLineClear, PlayerID: "other",
		Score: 20, LinesCleared: 10, TotalScore: 20, TotalLines: 10,
	})

	if e.Score() != 20 {
		t.Fatalf("score = %d, want 20", e.Score())
	}
	if e.Level() != 2 {
		t.Fatalf("level = %d, want 2 after folding 10 shared lines", e.Level())
	}
	if e.AchievedLevel() != 2 {
		t.Fatalf("AchievedLevel = %d, want 2", e.AchievedLevel())
	}
}
