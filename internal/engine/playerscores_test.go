package engine

import (
	"context"
	"testing"

	"jetris/internal/config"
)

// updateKinds drains e.Updates and counts what came out by kind.
func updateKinds(e *Engine) map[UpdateKind]int {
	kinds := map[UpdateKind]int{}
	for {
		select {
		case u := <-e.Updates:
			kinds[u.Kind]++
		default:
			return kinds
		}
	}
}

// Every engine — a competitive player's, whose own score no rival's event
// touches, and a spectator's, which has no score at all — folds every
// sender's cumulative totals into the per-player scoreboard: its own echo, a
// rival's clears, the rival's game_over with their last points. A rival's
// news raises UpdatePlayerScores; a stale replay of an older total neither
// moves the board nor raises anything; the own echo raises nothing (the
// screen reads that row live); and the engine's own score stays its own.
func TestPlayerScoresFoldEverySender(t *testing.T) {
	for _, mode := range []Mode{ModePlayer, ModeSpectator} {
		e := New(nil, "g", "me", "", config.ModeCompetitive, mode, 0, 0, 0)
		ctx := context.Background()
		updateKinds(e)
		e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "me", Score: 300, LinesCleared: 1, TotalScore: 300, TotalLines: 1})
		if k := updateKinds(e); k[UpdatePlayerScores] != 0 {
			t.Errorf("%v: our own echo raised UpdatePlayerScores", mode)
		}
		e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "them", Score: 500, LinesCleared: 2, TotalScore: 500, TotalLines: 2})
		if k := updateKinds(e); k[UpdatePlayerScores] != 1 {
			t.Errorf("%v: a rival's clear raised UpdatePlayerScores %d times, want once", mode, k[UpdatePlayerScores])
		}
		e.handleGameEvent(ctx, GameEvent{Kind: EventLineClear, PlayerID: "them", Score: 100, LinesCleared: 1, TotalScore: 100, TotalLines: 1})
		if k := updateKinds(e); k[UpdatePlayerScores] != 0 {
			t.Errorf("%v: a stale replay of an older total raised UpdatePlayerScores", mode)
		}
		// The rival's game_over folds like a clear — handleGameEvent's first
		// move on one, before the elimination it announces.
		e.foldTotals(GameEvent{Kind: EventGameOver, PlayerID: "them", Score: 700, Level: 1, TotalScore: 700, TotalLines: 3})
		if k := updateKinds(e); k[UpdatePlayerScores] != 1 {
			t.Errorf("%v: a rival's last points raised UpdatePlayerScores %d times, want once", mode, k[UpdatePlayerScores])
		}
		scores, lines := e.PlayerScores(), e.PlayerLines()
		if scores["me"] != 300 || scores["them"] != 700 {
			t.Errorf("%v: PlayerScores = %v, want me 300, them 700", mode, scores)
		}
		if lines["me"] != 1 || lines["them"] != 3 {
			t.Errorf("%v: PlayerLines = %v, want me 1, them 3", mode, lines)
		}
		if e.Score() != 0 || e.OwnScore() != 0 {
			t.Errorf("%v: a rival's totals moved this engine's own score to %d/%d", mode, e.Score(), e.OwnScore())
		}
	}
}
