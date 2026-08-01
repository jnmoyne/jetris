package lobby

import (
	"context"
	"errors"
	"testing"
	"time"

	"jetris/internal/config"
)

// waitInvites polls until the player's pending-invite count reaches want,
// returning the invitations (oldest first).
func waitInvites(t *testing.T, lb *Lobby, want int) []Invitation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		invs := lb.MyInvites()
		if len(invs) == want {
			return invs
		}
		if time.Now().After(deadline) {
			t.Fatalf("MyInvites() = %d invitations, want %d: %+v", len(invs), want, invs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitSent polls the INVITER's view of the invitations to one game until pred
// is satisfied.
func waitSent(t *testing.T, lb *Lobby, gameID string, desc string, pred func([]Invitation) bool) []Invitation {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		invs := lb.SentInvites(gameID)
		if pred(invs) {
			return invs
		}
		if time.Now().After(deadline) {
			t.Fatalf("SentInvites(%s): waiting for %s, have %+v", gameID, desc, invs)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The invitation lifecycle: invite-only games reject the uninvited (humans
// AND agents), admit the creator, deliver invitations through the KV watcher,
// admit invitees (an invited agent bypasses the agent-seat policy), and
// consume the invitation on join.
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
	// provenance, and B may then join; joining consumes the invitation — on
	// B's side AND in A's sent-invites view.
	if err := a.Invite(ctx, b.PlayerID(), gameID, 0); err != nil {
		t.Fatal(err)
	}
	inv := waitInvites(t, b, 1)[0]
	if inv.GameID != gameID || inv.FromID != a.PlayerID() || inv.Mode != config.ModeCompetitive {
		t.Fatalf("invitation = %+v", inv)
	}
	waitSent(t, a, gameID, "the pending invite", func(invs []Invitation) bool { return len(invs) == 1 })
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("invited join: %v", err)
	}
	waitInvites(t, b, 0)
	waitSent(t, a, gameID, "invite consumed by join", func(invs []Invitation) bool { return len(invs) == 0 })

	// Invite the agent: admitted despite maxAgents == 0.
	if err := a.Invite(ctx, c.PlayerID(), gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitInvites(t, c, 1)
	if _, err := c.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatalf("invited agent join: %v", err)
	}
}

// Declining keeps the invitation visible to the INVITER (marked declined) but
// removes it from the invitee's pending set and does not grant access; the
// inviter then dismisses the declined marker via Uninvite.
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
	inv := waitInvites(t, b, 1)[0]
	if inv.Team != 1 || inv.Mode != config.ModeTeams {
		t.Fatalf("invitation = %+v, want teams/team 1", inv)
	}

	if err := b.DeclineInvite(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	waitInvites(t, b, 0)
	if _, err := b.JoinGame(ctx, gameID, 1); !errors.Is(err, ErrNotInvited) {
		t.Fatalf("join after decline: err = %v, want ErrNotInvited", err)
	}

	// The inviter sees the refusal…
	invs := waitSent(t, a, gameID, "the declined marker", func(invs []Invitation) bool {
		return len(invs) == 1 && invs[0].Declined
	})
	if invs[0].InviteeID != b.PlayerID() {
		t.Fatalf("declined invitee = %q, want %q", invs[0].InviteeID, b.PlayerID())
	}
	// …and dismisses it.
	if err := a.Uninvite(ctx, b.PlayerID(), gameID); err != nil {
		t.Fatal(err)
	}
	waitSent(t, a, gameID, "the marker dismissed", func(invs []Invitation) bool { return len(invs) == 0 })
}

// A player can hold invitations to several games at once — one key per game —
// and answering one leaves the others pending.
func TestInviteMultiple(t *testing.T) {
	lbs := setupLobbies(t, 3)
	a, b, c := lbs[0], lbs[1], lbs[2]
	ctx := context.Background()

	g1, err := a.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := b.CreateGame(ctx, config.ModeCooperative, 2, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}

	if err := a.Invite(ctx, c.PlayerID(), g1, 0); err != nil {
		t.Fatal(err)
	}
	if err := b.Invite(ctx, c.PlayerID(), g2, 0); err != nil {
		t.Fatal(err)
	}
	invs := waitInvites(t, c, 2)
	// Oldest first: g1 was sent before g2.
	if invs[0].GameID != g1 || invs[1].GameID != g2 {
		t.Fatalf("invitations out of order: %+v", invs)
	}
	if c.InviteTo(g1) == nil || c.InviteTo(g2) == nil {
		t.Fatal("InviteTo should find both pending invitations")
	}

	// Declining g1 leaves g2 pending.
	if err := c.DeclineInvite(ctx, g1); err != nil {
		t.Fatal(err)
	}
	invs = waitInvites(t, c, 1)
	if invs[0].GameID != g2 {
		t.Fatalf("remaining invitation = %+v, want game %s", invs[0], g2)
	}
}

// Retracting a pending invitation (Uninvite) removes it from the invitee's
// pending set — their pop-up disappears.
func TestInviteRetract(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()

	gameID, err := a.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Invite(ctx, b.PlayerID(), gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitInvites(t, b, 1)

	if err := a.Uninvite(ctx, b.PlayerID(), gameID); err != nil {
		t.Fatal(err)
	}
	waitInvites(t, b, 0)
	if _, err := b.JoinGame(ctx, gameID, 0); !errors.Is(err, ErrNotInvited) {
		t.Fatalf("join after retract: err = %v, want ErrNotInvited", err)
	}
}
