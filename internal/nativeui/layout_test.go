package nativeui

import (
	"image"
	"testing"

	"gioui.org/font/gofont"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/lobby"
)

// newTestApp builds an App wired for headless layout: nil NATS handles (only
// used by background goroutines, never by the layout code) and a real theme.
func newTestApp() *App {
	a := New(nil, nil)
	a.th = material.NewTheme()
	a.th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	return a
}

// renderOnce builds a manual frame context (zero input.Source, which Gio treats
// as disabled — safe) and lays out the current screen. A panic fails the test.
func renderOnce(t *testing.T, a *App) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1200, 820)),
	}
	d := a.layout(gtx)
	if d.Size.X == 0 || d.Size.Y == 0 {
		// Screens fill the window; a zero size means nothing was laid out.
		t.Fatalf("layout produced zero dimensions: %+v", d.Size)
	}
}

// TestScreensLayoutWithoutPanic exercises every screen's layout code path
// (including the busy lobby and game screens, which can't be reached via the
// live launch smoke test) to catch nil derefs, bad indexing, or op misuse.
func TestScreensLayoutWithoutPanic(t *testing.T) {
	players := []lobby.PlayerSummary{
		{PlayerID: "alice", Name: "alice", Ready: true},
		{PlayerID: "bob", Name: "bob"},
	}

	t.Run("login", func(t *testing.T) {
		a := newTestApp()
		renderOnce(t, a)
	})

	t.Run("login-collision", func(t *testing.T) {
		a := newTestApp()
		a.loginCollision = true
		renderOnce(t, a)
	})

	t.Run("lobby", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.chatLog = []lobby.ChatMessage{{Name: "alice", Text: "hi"}}
		renderOnce(t, a)
	})

	t.Run("game-coop-player", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0)
		a.gamePlayers = players
		a.readyPlayers = players
		a.screen = screenGame
		renderOnce(t, a)
	})

	t.Run("game-competitive-player", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0)
		a.gamePlayers = players
		a.readyPlayers = players
		a.screen = screenGame
		a.countdown = 3
		renderOnce(t, a)
	})

	t.Run("game-spectator-competitive", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "spec", "", config.ModeCompetitive, engine.ModeSpectator, 0)
		a.gamePlayers = players
		a.screen = screenGame
		renderOnce(t, a)
	})

	t.Run("game-over", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0)
		a.gamePlayers = players
		a.screen = screenGame
		a.gameOver = true
		a.won = true
		renderOnce(t, a)
	})
}
