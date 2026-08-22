package engine

import (
	"context"
	"testing"

	"jetris/internal/config"
)

// strobedRows drains e.Updates and returns the rows of the first
// UpdateRowsCleared found, or nil when none was emitted.
func strobedRows(e *Engine) []int {
	for {
		select {
		case u := <-e.Updates:
			if u.Kind == UpdateRowsCleared {
				return u.ChangedRows
			}
		default:
			return nil
		}
	}
}

func equalRows(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A teammate's line-clear event carries the cleared rows, and the receiving
// engine on the shared board raises the same UpdateRowsCleared the clearer's
// own lock did — so every crew member's board flashes the cleared lines, in
// cooperative and in teams (own team only).
func TestTeammateLineClearStrobesSharedBoard(t *testing.T) {
	rows := []int{22, 23}
	cases := []struct {
		name string
		mode config.GameMode
		ev   GameEvent
		want []int
	}{
		{"coop teammate", config.ModeCooperative,
			GameEvent{Kind: EventLineClear, PlayerID: "other", Score: 4, LinesCleared: 2, ClearedRows: rows, TotalScore: 4, TotalLines: 2},
			rows},
		{"coop peer omitting the rows", config.ModeCooperative,
			GameEvent{Kind: EventLineClear, PlayerID: "other", Score: 2, LinesCleared: 1, TotalScore: 2, TotalLines: 1},
			nil},
		{"teams own team", config.ModeTeams,
			GameEvent{Kind: EventLineClear, PlayerID: "mate", Team: 0, Score: 4, LinesCleared: 2, ClearedRows: rows, TotalScore: 4, TotalLines: 2},
			rows},
		{"teams opposing team", config.ModeTeams,
			GameEvent{Kind: EventLineClear, PlayerID: "them", Team: 1, Score: 4, LinesCleared: 2, ClearedRows: rows, TotalScore: 4, TotalLines: 2},
			nil},
		{"competitive opponent", config.ModeCompetitive,
			GameEvent{Kind: EventLineClear, PlayerID: "them", Score: 2, LinesCleared: 2, ClearedRows: rows, TotalScore: 2, TotalLines: 2},
			nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := New(nil, "g", "me", "", tc.mode, ModePlayer, 0, 0, 0)
			e.handleGameEvent(context.Background(), tc.ev)
			if got := strobedRows(e); !equalRows(got, tc.want) {
				t.Fatalf("UpdateRowsCleared rows = %v, want %v", got, tc.want)
			}
		})
	}
}
