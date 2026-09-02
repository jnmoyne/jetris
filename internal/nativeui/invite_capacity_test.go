package nativeui

import (
	"testing"

	"jetris/internal/config"
	"jetris/internal/lobby"
)

// The picker's capacity guard counts seats spoken for — roster members plus
// pending (not declined) invitations — so selections beyond the free seats
// are refused at click time.
func TestInviteSeatUsage(t *testing.T) {
	// Competitive: 1 joined + 2 pending + 1 declined = 3 seats spoken for.
	g := lobby.GameListing{
		PlayerCount: 4,
		Players:     []lobby.PlayerSummary{{PlayerID: "host"}},
	}
	invites := []lobby.Invitation{
		{InviteeID: "a"},
		{InviteeID: "b"},
		{InviteeID: "c", Declined: true}, // declined seats are free again
	}
	if n := inviteSeatUsage(g, invites, false, 0); n[0] != 3 {
		t.Errorf("competitive usage = %d, want 3 (1 joined + 2 pending)", n[0])
	}

	// Teams: usage is per team; the declined B invite frees that seat.
	g = lobby.GameListing{
		PlayerCount: 4,
		TeamSize:    2,
		Players: []lobby.PlayerSummary{
			{PlayerID: "host", Team: 0},
			{PlayerID: "ann", Team: 1},
		},
	}
	invites = []lobby.Invitation{
		{InviteeID: "a", Team: 0},
		{InviteeID: "b", Team: 1, Declined: true},
	}
	n := inviteSeatUsage(g, invites, true, 2)
	if n[0] != 2 || n[1] != 1 {
		t.Errorf("teams usage = %v, want [2 1]", n)
	}

	// Three teams: the tally is as wide as the game, and an invitation naming
	// a team the game does not have is counted against nobody.
	g = lobby.GameListing{
		Mode:        config.ModeTeams,
		PlayerCount: 6,
		TeamCount:   3,
		TeamSize:    2,
		Players: []lobby.PlayerSummary{
			{PlayerID: "host", Team: 0},
			{PlayerID: "ann", Team: 2},
		},
	}
	invites = []lobby.Invitation{
		{InviteeID: "a", Team: 1},
		{InviteeID: "b", Team: 2},
		{InviteeID: "c", Team: 5}, // not a team of this game
	}
	n = inviteSeatUsage(g, invites, true, g.Teams())
	if len(n) != 3 || n[0] != 1 || n[1] != 1 || n[2] != 2 {
		t.Errorf("three-team usage = %v, want [1 1 2]", n)
	}
}

// pickerRowStatus derives each row's live state: roster beats invitation, and
// the invitation's Declined flag distinguishes waiting from refused.
func TestPickerRowStatus(t *testing.T) {
	g := lobby.GameListing{
		Players: []lobby.PlayerSummary{
			{PlayerID: "joined"},
			{PlayerID: "ready", Ready: true},
		},
	}
	invites := []lobby.Invitation{
		{InviteeID: "pending"},
		{InviteeID: "declined", Declined: true},
	}
	want := map[string]inviteRowStatus{
		"joined":   rowJoined,
		"ready":    rowReady,
		"pending":  rowPending,
		"declined": rowDeclined,
		"nobody":   rowNone,
	}
	for id, st := range want {
		if got := pickerRowStatus(g, invites, id); got != st {
			t.Errorf("pickerRowStatus(%s) = %v, want %v", id, got, st)
		}
	}
}
