package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// leaveTestListing writes the game's lobby listing into a private bucket:
// an in-progress game holding our seat and a crewmate's.
func leaveTestListing(t *testing.T, ctx context.Context, g *Game, inviteOnly bool) {
	t.Helper()
	a := g.a
	a.bucket = "JETRIS_LOBBY_TEST_" + randID(6)
	var err error
	if a.kv, err = a.ensureLobbyBucket(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.js.DeleteKeyValue(context.Background(), a.bucket) })
	listing := map[string]any{
		"game_id": g.id, "mode": g.mode, "status": "in_progress", "player_count": 2, "invite_only": inviteOnly,
		"players": []playerSummary{{PlayerID: "me", Name: "me", Seat: 0, Agent: true}, {PlayerID: "peer", Name: "peer", Seat: 1}},
	}
	b, _ := json.Marshal(listing)
	if _, err := a.kv.Put(ctx, "games."+g.id, b); err != nil {
		t.Fatal(err)
	}
}

func listingPlayers(t *testing.T, ctx context.Context, g *Game) []playerSummary {
	t.Helper()
	entry, err := g.a.kv.Get(ctx, "games."+g.id)
	if err != nil {
		t.Fatal(err)
	}
	return toObj(entry.Value()).players()
}

// Stopped mid-game in an OPEN game, the agent vacates its own piece before
// freeing its seat: the board keeps no cell of ours, the listing no seat.
func TestLeaveRunningVacatesPieceAndFreesSeat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	leaveTestListing(t, ctx, g, false)
	g.mu.Lock()
	if _, placed, topped := g.spawn(ctx); !placed || topped {
		t.Fatalf("spawn: placed %v topped %v", placed, topped)
	}
	g.mu.Unlock()
	g.a.stop()
	g.leaveRunning()
	for at, m := range streamCells(t, ctx, g) {
		if m.wc.A && m.wc.Pi == 0 {
			t.Errorf("a cell of ours left on the board at %v", at)
		}
	}
	if g.piece != nil {
		t.Error("the piece is still ours locally")
	}
	players := listingPlayers(t, ctx, g)
	if len(players) != 1 || players[0].PlayerID != "peer" {
		t.Errorf("listing after the leave: %+v, want the peer alone", players)
	}
}

// An invite game's seat is kept for the player to rejoin: the piece is
// vacated, the seat stays.
func TestLeaveRunningKeepsAnInviteSeat(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	leaveTestListing(t, ctx, g, true)
	g.mu.Lock()
	if _, placed, topped := g.spawn(ctx); !placed || topped {
		t.Fatalf("spawn: placed %v topped %v", placed, topped)
	}
	g.mu.Unlock()
	g.a.stop()
	g.leaveRunning()
	for at, m := range streamCells(t, ctx, g) {
		if m.wc.A && m.wc.Pi == 0 {
			t.Errorf("a cell of ours left on the board at %v", at)
		}
	}
	if players := listingPlayers(t, ctx, g); len(players) != 2 {
		t.Errorf("listing after the leave: %+v, want both seats kept", players)
	}
}
