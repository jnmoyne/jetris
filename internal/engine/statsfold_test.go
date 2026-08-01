package engine

import (
	"context"
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
	e.handleGameEvent(context.Background(), GameEvent{
		Kind: EventLineClear, PlayerID: "them", Team: 1, Score: 20, LinesCleared: 10,
	})

	if got, want := e.TeamScores(), [config.TeamCount]int{0, 20}; got != want {
		t.Fatalf("TeamScores = %v, want %v", got, want)
	}
	if got, want := e.TeamLevels(), [config.TeamCount]int{0, 1}; got != want {
		t.Fatalf("TeamLevels = %v, want %v", got, want)
	}
	if e.Score() != 0 || e.Level() != 0 {
		t.Fatalf("own score/level = %d/%d, want 0/0 (other team's clear)", e.Score(), e.Level())
	}
}

// TestCoopLineClearFoldsLinesAndLevel verifies that a cooperative line-clear
// event from another player folds the shared line total — so the receiver's
// level (HUD and archive) advances — in addition to the score delta.
func TestCoopLineClearFoldsLinesAndLevel(t *testing.T) {
	e := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)

	e.handleGameEvent(context.Background(), GameEvent{
		Kind: EventLineClear, PlayerID: "other", Score: 20, LinesCleared: 10,
	})

	if e.Score() != 20 {
		t.Fatalf("score = %d, want 20", e.Score())
	}
	if e.Level() != 1 {
		t.Fatalf("level = %d, want 1 after folding 10 shared lines", e.Level())
	}
	if e.AchievedLevel() != 1 {
		t.Fatalf("AchievedLevel = %d, want 1", e.AchievedLevel())
	}
}
