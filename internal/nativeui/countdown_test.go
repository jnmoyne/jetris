package nativeui

import (
	"testing"

	"jetricks/internal/config"
	"jetricks/internal/engine"
)

// The countdown overlay must show only BEFORE the game starts — and never
// resurrect at game end, when the status moves past in_progress to finished
// while the last countdown value is still 0 (the spectator "stale GO!" bug).
func TestCountdownVisible(t *testing.T) {
	v := func(status string, countdown int, gameOver bool) gameView {
		return gameView{status: status, countdown: countdown, gameOver: gameOver}
	}
	cases := []struct {
		name string
		view gameView
		mode engine.Mode
		want bool
	}{
		{"player pre-start", v(string(config.GameStatusStarting), 3, false), engine.ModePlayer, true},
		{"spectator pre-start", v(string(config.GameStatusStarting), 0, false), engine.ModeSpectator, true},
		{"no countdown yet", v(string(config.GameStatusStarting), -1, false), engine.ModeSpectator, false},
		{"in progress", v(string(config.GameStatusInProgress), 0, false), engine.ModeSpectator, false},
		{"finished (stale GO!)", v(string(config.GameStatusFinished), 0, false), engine.ModeSpectator, false},
		{"archived", v(string(config.GameStatusArchived), 0, false), engine.ModeSpectator, false},
		{"local game over", v(string(config.GameStatusStarting), 0, true), engine.ModePlayer, false},
		{"finished local player", v(string(config.GameStatusStarting), 0, false), engine.ModeGameOver, false},
	}
	for _, tc := range cases {
		if got := countdownVisible(tc.view, tc.mode); got != tc.want {
			t.Errorf("%s: countdownVisible = %v, want %v", tc.name, got, tc.want)
		}
	}
}
