package lobby

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"jetris/internal/config"
)

// fetchPresence reads the player's presence entry straight from the KV.
func fetchPresence(t *testing.T, lb *Lobby, playerID string) (PlayerPresence, bool) {
	t.Helper()
	entry, err := lb.kv.Get(context.Background(), config.LobbyPlayerKey(playerID))
	if err != nil {
		return PlayerPresence{}, false
	}
	var p PlayerPresence
	if err := json.Unmarshal(entry.Value(), &p); err != nil {
		t.Fatalf("unmarshal presence: %v", err)
	}
	return p, true
}

// waitForPresenceStatus polls the KV until the player's presence reaches the
// wanted status or the deadline passes.
func waitForPresenceStatus(t *testing.T, lb *Lobby, playerID string, want PresenceStatus) PlayerPresence {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last PlayerPresence
	for time.Now().Before(deadline) {
		if p, ok := fetchPresence(t, lb, playerID); ok {
			last = p
			if p.Status == want {
				return p
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("presence never reached status %d, last: %+v", want, last)
	return last
}

// TestPresenceHealsWhenGameDeleted reproduces the stuck "in game" bug: a
// player whose game is archived by ANOTHER client (which deletes the KV
// listing) has no LeaveGame call of their own — before the self-heal in
// handleGameUpdate their heartbeat would re-publish the stale in-game status
// forever, leaving them un-invitable in every other player's lobby.
func TestPresenceHealsWhenGameDeleted(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, config.GameRules{Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lb.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitForPresenceStatus(t, lb, lb.PlayerID(), StatusInGame)

	// Simulate the archiver on another client: it deletes the game's KV
	// listing (archive.ArchiveAndCleanup) without touching OUR presence.
	if err := lb.kv.Delete(ctx, config.LobbyGameKey(gameID)); err != nil {
		t.Fatal(err)
	}

	p := waitForPresenceStatus(t, lb, lb.PlayerID(), StatusInLobby)
	if p.GameID != "" {
		t.Errorf("expected empty game ID after heal, got %q", p.GameID)
	}
}

// TestPresenceHealsWhenGameStampedDead covers the other death shape: the
// listing is not deleted but overwritten with a terminal status (the startup
// cleanup pass writes archived listings that way).
func TestPresenceHealsWhenGameStampedDead(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, config.GameRules{Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lb.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitForPresenceStatus(t, lb, lb.PlayerID(), StatusInGame)

	listing := GameListing{GameID: gameID, Status: config.GameStatusArchived}
	data, _ := json.Marshal(listing)
	if _, err := lb.kv.Put(ctx, config.LobbyGameKey(gameID), data); err != nil {
		t.Fatal(err)
	}

	waitForPresenceStatus(t, lb, lb.PlayerID(), StatusInLobby)
}

// TestPresenceUntouchedWhenOtherGameDies makes sure the self-heal only fires
// for OUR current game: some unrelated game being archived must not flip an
// in-game player back to "in lobby".
func TestPresenceUntouchedWhenOtherGameDies(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	otherID, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, config.GameRules{Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	gameID, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, config.GameRules{Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lb.JoinGame(ctx, gameID, 0); err != nil {
		t.Fatal(err)
	}
	waitForPresenceStatus(t, lb, lb.PlayerID(), StatusInGame)

	if err := lb.kv.Delete(ctx, config.LobbyGameKey(otherID)); err != nil {
		t.Fatal(err)
	}

	// Give the watcher time to deliver the unrelated delete, then confirm the
	// presence still says in-game for our game.
	time.Sleep(500 * time.Millisecond)
	p, ok := fetchPresence(t, lb, lb.PlayerID())
	if !ok {
		t.Fatal("presence entry missing")
	}
	if p.Status != StatusInGame || p.GameID != gameID {
		t.Errorf("presence disturbed by unrelated game death: %+v", p)
	}
}
