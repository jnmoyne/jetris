package nativeui

import (
	"testing"

	"jetris/internal/config"
	"jetris/internal/lobby"
)

// TestReleaseSeatOnLeave pins the "Back to Lobby" presence decision. The KV
// listing's status never advances past created/starting during a game (the
// meta carries the live status), so the engine-reported meta status must be
// able to force the release on its own — that was the bug that left players
// stuck "in game" (and un-invitable) after a finished game.
func TestReleaseSeatOnLeave(t *testing.T) {
	const me = "me"
	seated := lobby.GameListing{
		GameID:  "g1",
		Status:  config.GameStatusStarting,
		Players: []lobby.PlayerSummary{{PlayerID: me}, {PlayerID: "other"}},
	}

	cases := []struct {
		name       string
		g          lobby.GameListing
		found      bool
		metaStatus config.GameStatus
		want       bool
	}{
		{
			// The reported bug: game just finished, archiver hasn't deleted
			// the listing yet — the meta status alone must release the seat.
			name: "meta finished overrides live-looking listing",
			g:    seated, found: true, metaStatus: config.GameStatusFinished, want: true,
		},
		{
			name: "meta cancelled overrides live-looking listing",
			g:    seated, found: true, metaStatus: config.GameStatusCancelled, want: true,
		},
		{
			name: "listing already deleted",
			g:    lobby.GameListing{}, found: false, metaStatus: config.GameStatusInProgress, want: true,
		},
		{
			name: "listing stamped dead",
			g:    lobby.GameListing{GameID: "g1", Status: config.GameStatusArchived}, found: true,
			metaStatus: config.GameStatusInProgress, want: true,
		},
		{
			name: "not on the roster",
			g: lobby.GameListing{
				GameID:  "g1",
				Status:  config.GameStatusStarting,
				Players: []lobby.PlayerSummary{{PlayerID: "other"}},
			},
			found: true, metaStatus: config.GameStatusInProgress, want: true,
		},
		{
			// Seat keeping: leaving a live game mid-play (or after our own
			// elimination while others play on) keeps the in-game marker so
			// the lobby row shows Rejoin and we stay un-invitable.
			name: "live game keeps the seat",
			g:    seated, found: true, metaStatus: config.GameStatusInProgress, want: false,
		},
		{
			name: "pre-start leave keeps the seat",
			g:    seated, found: true, metaStatus: config.GameStatusStarting, want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseSeatOnLeave(tc.g, tc.found, tc.metaStatus, me); got != tc.want {
				t.Errorf("releaseSeatOnLeave() = %v, want %v", got, tc.want)
			}
		})
	}
}
