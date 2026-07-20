package lobby

import (
	"context"
	"errors"
	"testing"
	"time"

	"jetricks/internal/config"
)

func waitInvite(t *testing.T, lb *Lobby, want bool) *Invitation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		inv := lb.MyInvite()
		if (inv != nil) == want {
			return inv
		}
		if time.Now().After(deadline) {
			t.Fatalf("MyInvite() presence = %v, want %v", inv != nil, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The invitation lifecycle: invite-only games reject the uninvited (humans
// AND agents), admit the creator, deliver invitations through the KV watcher,
// admit invitees (an invited agent bypasses the agent-seat policy), and
// consume the invitation on join or decline.
func TestInviteFlow(t *testing.T) {
	lbs := setupLobbies(t, 3)
	a, b, c := lbs[0], lbs[1], lbs[2]
	c.SetAgent(true)
	ctx := context.Background()

	// Invite-only game with agents disallowed by policy (maxAgents 0): the
	// invitation itself must be what admits the invited agent.
	gameID, err := a.CreateGame(ctx, config.ModeCompetitive, 3, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}

	// Uninvited: both humans and agents bounce.
	if _, err := b.JoinGame(ctx, gameID, 0); !errors.Is(err, ErrNotInvited) {
		t.Fatalf("uninvited human join: err = %v, want ErrNotInvited", err)
	}
	if _, err := c.JoinGame(ctx, gameID, 0); !errors.Is(err, ErrNotInvited) {
		t.Fatalf("uninvited agent join: err = %v, want ErrNotInvited", err)
	}

	// The creator always may.
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("creator join: %v", err)
	}

	// Invite B: the invitation reaches B via the KV watcher with the right
	// provenance, and B may then join; joining consumes the invitation.
	if err := a.Invite(ctx, b.PlayerID(), gameID, 0); err != nil {
		t.Fatal(err)
	}
	inv := waitInvite(t, b, true)
	if inv.GameID != gameID || inv.FromID != a.PlayerID() || inv.Mode != config.ModeCompetitive {
		t.Fatalf("invitation = %+v", inv)
	}
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("invited join: %v", err)
	}
	waitInvite(t, b, false)

	// Invite the agent: admitted despite maxAgents == 0.
	if err := a.Invite(ctx, c.PlayerID(), gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitInvite(t, c, true)
	if _, err := c.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("invited agent join: %v", err)
	}
}

// Declining consumes the invitation without granting access.
func TestInviteDecline(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()

	gameID, err := a.CreateGame(ctx, config.ModeTeams, 2, 1, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Invite(ctx, b.PlayerID(), gameID, 1); err != nil {
		t.Fatal(err)
	}
	inv := waitInvite(t, b, true)
	if inv.Team != 1 || inv.Mode != config.ModeTeams {
		t.Fatalf("invitation = %+v, want teams/team 1", inv)
	}

	got, err := b.RespondInvite(ctx, false)
	if err != nil || got.GameID != gameID {
		t.Fatalf("RespondInvite = %+v, %v", got, err)
	}
	waitInvite(t, b, false)
	if _, err := b.JoinGame(ctx, gameID, 1); !errors.Is(err, ErrNotInvited) {
		t.Fatalf("join after decline: err = %v, want ErrNotInvited", err)
	}
}
