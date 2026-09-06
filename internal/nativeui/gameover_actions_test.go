package nativeui

// The game-over box's Pin, Share and Back to Lobby (share.go's
// gameOverActions): the replay screen's three actions, at the moment the
// game ends.

import (
	"strings"
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	"jetris/internal/testutil"
)

// finishedGameApp is a game screen over a finished game, as a player or a
// spectator, with a lobby standing in for the pin watcher.
func finishedGameApp(m engine.Mode, gmode config.GameMode, status config.GameStatus) *App {
	a := newTestApp()
	a.lobby = lobby.New(nil, nil, "alice", "alice")
	a.eng = engine.New(nil, "g1", "alice", "bob", gmode, m, 0, 0, 0)
	a.screenEng = a.eng
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(status)
	a.gameOver = true
	a.connName, a.connURL = "My LAN", "ws://192.168.1.20:4223"
	return a
}

// The finished game's box lays out with the three buttons, for a player and
// a spectator in every mode; the share modal opens over it for the game
// just played, and its OK closes it. A game the local player is merely out
// of, still running for the others, offers Back alone (nothing to share
// yet), and no modal state leaks past the screen: the flag is dropped the
// frame the lobby is drawn.
func TestGameOverActions(t *testing.T) {
	for _, m := range []engine.Mode{engine.ModePlayer, engine.ModeSpectator} {
		for _, gmode := range []config.GameMode{config.ModeCooperative, config.ModeCompetitive, config.ModeTeams} {
			a := finishedGameApp(m, gmode, config.GameStatusFinished)
			renderOnce(t, a)
			a.openShareFor(a.eng.GameID())
			if !a.shareOpen || !strings.Contains(a.shareLink, "replay=g1") {
				t.Fatalf("%v %v: share modal open %v, link %q; want the game's link", m, gmode, a.shareOpen, a.shareLink)
			}
			renderOnce(t, a) // the modal over the game screen
			if a.screen != screenGame {
				t.Fatalf("%v %v: the modal left the game screen (%v)", m, gmode, a.screen)
			}
			a.screen = screenLobby
			renderOnce(t, a)
			if a.shareOpen {
				t.Fatalf("%v %v: the share modal outlived the game screen", m, gmode)
			}
		}
	}
	// Out, but the game plays on: the box is drawn with Back alone.
	a := finishedGameApp(engine.ModePlayer, config.ModeCompetitive, config.GameStatusInProgress)
	renderOnce(t, a)
	if view := a.snapshotGame(time.Now()); view.finished {
		t.Fatal("an in-progress game reads finished")
	}
}

// Pinning from the game-over box: the toggle writes the lobby's pin, the
// screen follows the lobby's watcher (pinned, then unpinned), and nothing
// is reported under the buttons.
func TestGameOverPinToggles(t *testing.T) {
	url, _ := testutil.StartServer(t)
	a := linkApp(t, config.Config{NATSURL: url, PlayerName: "alice"})
	renderOnce(t, a)
	waitFor(t, "the lobby", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.screen == screenLobby || a.loginErr != ""
	})
	lb := a.getLobby()
	if lb == nil {
		t.Fatalf("no lobby (err %q)", a.loginErr)
	}
	const gameID = "g-over"
	a.togglePin(gameID, a.setGameOverNote)
	waitFor(t, "the pin", func() bool { return lb.IsPinned(gameID) })
	a.togglePin(gameID, a.setGameOverNote)
	waitFor(t, "the unpin", func() bool { return !lb.IsPinned(gameID) })
	a.mu.Lock()
	note := a.gameOverNote
	a.mu.Unlock()
	if note != "" {
		t.Fatalf("note under the buttons = %q, want none", note)
	}
	// No lobby (the tour's game, a torn-down one): the toggle is a no-op.
	a.mu.Lock()
	a.lobby = nil
	a.mu.Unlock()
	a.togglePin(gameID, a.setGameOverNote)
}
