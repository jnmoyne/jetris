package lobby

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"jetris/internal/config"
)

// waitListing polls one lobby's view of a game listing until pred holds.
func waitListing(t *testing.T, lb *Lobby, gameID, desc string, pred func(GameListing) bool) GameListing {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		g, ok := lb.Games()[gameID]
		if ok && pred(g) {
			return g
		}
		if time.Now().After(deadline) {
			t.Fatalf("game %s: waiting for %s, have %+v", gameID, desc, g)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// SetReady sets the exact ready state (idempotently), unlike ToggleReady —
// it's what clears readiness when a player goes back to the lobby.
func TestSetReady(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()

	gameID, err := a.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}

	if _, err := a.ToggleReady(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	ready := func(g GameListing) bool {
		for _, p := range g.Players {
			if p.PlayerID == a.PlayerID() {
				return p.Ready
			}
		}
		return false
	}
	waitListing(t, b, gameID, "A ready", ready)

	// Clearing is exact and idempotent.
	if err := a.SetReady(ctx, gameID, false); err != nil {
		t.Fatal(err)
	}
	if err := a.SetReady(ctx, gameID, false); err != nil {
		t.Fatal(err)
	}
	waitListing(t, b, gameID, "A not ready", func(g GameListing) bool { return !ready(g) })
}

// A player who joined and "went back to the lobby" keeps their roster seat and
// can join again — JoinGame returns the same position instead of a second seat.
func TestRejoinKeepsSeat(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()

	gameID, err := a.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	first, err := b.JoinGame(ctx, gameID, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Back to the lobby (screen change only — the seat is kept)…
	if err := b.LeaveGame(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	waitListing(t, a, gameID, "B still on the roster", func(g GameListing) bool {
		return len(g.Players) == 1 && g.Players[0].PlayerID == b.PlayerID()
	})
	// …and rejoining lands on the very same seat.
	again, err := b.JoinGame(ctx, gameID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Fatalf("rejoin position = %+v, want the original %+v", again, first)
	}
	g := waitListing(t, a, gameID, "roster still 1 seat", func(g GameListing) bool { return len(g.Players) == 1 })
	if len(g.Players) != 1 {
		t.Fatalf("roster grew on rejoin: %+v", g.Players)
	}
}

// Lobby actions publish transient core NATS events (game created/joined/left,
// invite sent/declined/retracted) that other peers can monitor in real time.
func TestLobbyEventsPublished(t *testing.T) {
	lbs := setupLobbies(t, 2)
	a, b := lbs[0], lbs[1]
	ctx := context.Background()

	// A raw core NATS listener on the events space (what an external monitor,
	// or another lobby's listener, sees).
	var mu sync.Mutex
	seen := map[string]LobbyEvent{}
	sub, err := a.GetJS().Conn().Subscribe(config.LobbyEventsFilter, func(msg *nats.Msg) {
		var ev LobbyEvent
		if json.Unmarshal(msg.Data, &ev) != nil {
			return
		}
		mu.Lock()
		seen[ev.Kind] = ev
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	gameID, err := a.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Invite(ctx, b.PlayerID(), gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitInvites(t, b, 1)
	if err := b.DeclineInvite(ctx, gameID); err != nil {
		t.Fatal(err)
	}
	if err := a.Uninvite(ctx, b.PlayerID(), gameID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.UnjoinGame(ctx, gameID); err != nil {
		t.Fatal(err)
	}

	want := []string{EventGameCreated, EventInviteSent, EventInviteDeclined,
		EventInviteRetracted, EventGameJoined, EventGameLeft}
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		missing := ""
		for _, k := range want {
			if _, ok := seen[k]; !ok {
				missing = k
				break
			}
		}
		mu.Unlock()
		if missing == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("event %q never seen (have %v)", missing, seen)
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if ev := seen[EventInviteSent]; ev.GameID != gameID || ev.PlayerID != a.PlayerID() || ev.TargetID != b.PlayerID() {
		t.Fatalf("invite.sent event = %+v", ev)
	}
	if ev := seen[EventGameJoined]; ev.GameID != gameID || ev.PlayerID != a.PlayerID() {
		t.Fatalf("game.joined event = %+v", ev)
	}
	if ev := seen[EventInviteDeclined]; ev.PlayerID != b.PlayerID() {
		t.Fatalf("invite.declined event = %+v", ev)
	}
}
