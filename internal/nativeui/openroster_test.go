package nativeui

import (
	"testing"

	"jetris/internal/config"
	"jetris/internal/lobby"
)

// The lobby row offers a running OPEN game's free seats to join — and only
// while a seat is free; a running invite game's seats are its invitees',
// and a game in its countdown, or over, takes nobody.
func TestCanJoinOpenInProgress(t *testing.T) {
	seat := func(id string, n int) lobby.PlayerSummary { return lobby.PlayerSummary{PlayerID: id, Seat: n} }
	for _, tc := range []struct {
		name              string
		g                 lobby.GameListing
		joinable, canJoin bool
	}{
		{"open, created, free seat", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, Status: config.GameStatusCreated, Players: []lobby.PlayerSummary{seat("a", 0)}}, true, true},
		{"open, running, free seat", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, Status: config.GameStatusInProgress, Players: []lobby.PlayerSummary{seat("a", 0)}}, true, true},
		{"open, running, full", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, Status: config.GameStatusInProgress, Players: []lobby.PlayerSummary{seat("a", 0), seat("b", 1)}}, true, false},
		{"open, running, a freed middle seat", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 3, Status: config.GameStatusInProgress, Players: []lobby.PlayerSummary{seat("a", 0), seat("c", 2)}}, true, true},
		{"open, countdown", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, Status: config.GameStatusStarting, Players: []lobby.PlayerSummary{seat("a", 0)}}, true, true},
		{"open, finished", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, Status: config.GameStatusFinished, Players: []lobby.PlayerSummary{seat("a", 0)}}, false, false},
		{"invite, running", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, InviteOnly: true, Status: config.GameStatusInProgress, Players: []lobby.PlayerSummary{seat("a", 0)}}, false, false},
		{"invite, created", lobby.GameListing{Mode: config.ModeCooperative, PlayerCount: 2, InviteOnly: true, Status: config.GameStatusCreated, Players: []lobby.PlayerSummary{seat("a", 0)}}, true, true},
		{"open teams, running, a team seat free", lobby.GameListing{Mode: config.ModeTeams, TeamCount: 2, TeamSize: 2, PlayerCount: 4, Status: config.GameStatusInProgress, Players: []lobby.PlayerSummary{seat("a", 0), seat("b", 2), seat("c", 3)}}, true, true},
	} {
		joinable, canJoin := joinGating(tc.g)
		if joinable != tc.joinable || canJoin != tc.canJoin {
			t.Errorf("%s: joinGating = %v/%v, want %v/%v", tc.name, joinable, canJoin, tc.joinable, tc.canJoin)
		}
	}
}

// The ready bar names what an open game still waits for once everyone
// present is ready, and lays out with the note in the narrowest window.
func TestReadyBarOpen(t *testing.T) {
	a := newTestApp()
	view := gameView{
		readyPlayer: []lobby.PlayerSummary{{PlayerID: "me", Ready: true}},
		readyNote:   "waiting for a player on Team B",
		myReady:     true,
	}
	for _, size := range [][2]int{{360, 640}, {1200, 800}} {
		if d := a.readyBar(testCtx(size[0], size[1]), view); d.Size.X == 0 || d.Size.Y == 0 {
			t.Fatalf("ready bar with a note laid out empty at %dx%d", size[0], size[1])
		}
	}
	view.readyNote = ""
	if d := a.readyBar(testCtx(800, 600), view); d.Size.X == 0 {
		t.Fatal("ready bar without a note laid out empty")
	}
}
