package nativeui

import (
	"context"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
)

// An UpdateRowsCleared — the player's own clear, or a teammate's on a shared
// board — strobes the cleared rows on the board in EVERY game mode, and never
// for a spectator.
func TestRowsClearedStrobesForPlayersInEveryMode(t *testing.T) {
	cases := []struct {
		name  string
		gmode config.GameMode
		mode  engine.Mode
		want  bool
	}{
		{"cooperative player", config.ModeCooperative, engine.ModePlayer, true},
		{"competitive player", config.ModeCompetitive, engine.ModePlayer, true},
		{"teams player", config.ModeTeams, engine.ModePlayer, true},
		{"cooperative spectator", config.ModeCooperative, engine.ModeSpectator, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp()
			a.mu.Lock()
			a.resetBoardFX()
			a.mu.Unlock()
			e := engine.New(nil, "g1", "alice", "", tc.gmode, tc.mode, 0, 0, 0)

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { a.pumpEngine(ctx, e); close(done) }()
			e.Updates <- engine.EngineUpdate{Kind: engine.UpdateRowsCleared, ChangedRows: []int{22, 23}}
			// An RTT update queued behind it tells us the strobe has been pumped.
			e.Updates <- engine.EngineUpdate{Kind: engine.UpdateRTT, RTT: time.Millisecond}
			deadline := time.Now().Add(2 * time.Second)
			for {
				a.mu.Lock()
				pumped := a.rtt == time.Millisecond
				a.mu.Unlock()
				if pumped {
					break
				}
				if time.Now().After(deadline) {
					cancel()
					t.Fatal("pumpEngine never processed the updates")
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			<-done

			a.mu.Lock()
			_, got22 := a.rowStrobes[22]
			_, got23 := a.rowStrobes[23]
			a.mu.Unlock()
			if got := got22 && got23; got != tc.want {
				t.Fatalf("rows 22/23 strobing = %v/%v, want both %v", got22, got23, tc.want)
			}
		})
	}
}
