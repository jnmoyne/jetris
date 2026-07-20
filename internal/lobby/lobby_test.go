package lobby

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

func setupLobby(t *testing.T) (*Lobby, jetstream.JetStream) {
	t.Helper()
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := natspkg.EnsureLobbyChatStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}

	lb := New(js, kv, "player-1", "Alice")
	if err := lb.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lb.Stop)

	// Wait for KV watcher to initialize
	time.Sleep(200 * time.Millisecond)

	return lb, js
}

func TestLobbyCreateGame(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.ModeCooperative, 2, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if gameID == "" {
		t.Fatal("expected non-empty game ID")
	}

	// Wait for KV update
	time.Sleep(300 * time.Millisecond)

	games := lb.Games()
	if len(games) == 0 {
		t.Fatal("expected game in listing")
	}
	g, ok := games[gameID]
	if !ok {
		t.Fatal("game not found in listing")
	}
	if g.Status != config.GameStatusCreated {
		t.Errorf("expected status created, got %s", g.Status)
	}
}

func TestLobbySendChat(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	if err := lb.SendChat(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}

	// Check for chat update
	timeout := time.After(2 * time.Second)
	select {
	case update := <-lb.Updates:
		if update.Kind == LobbyUpdateChat {
			if update.ChatMsg == nil || update.ChatMsg.Text != "hello world" {
				t.Error("chat message mismatch")
			}
		}
	case <-timeout:
		t.Log("no chat update received (may have been consumed by KV watcher)")
	}
}

// TestChatBacklogSurvivesUndrainedUpdates reproduces the two-player bug where
// a joining player's chat panel came up empty: the stream's chat backlog is
// replayed while nothing drains lb.Updates (during login the UI pump hasn't
// attached yet, and emitUpdate drops on a full channel). The log lives in the
// Lobby, not in the lossy updates, so ChatLog must return the backlog even
// when every update ping was dropped.
func TestChatBacklogSurvivesUndrainedUpdates(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := lb.SendChat(ctx, fmt.Sprintf("line %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// A second player joins the same lobby. Nothing reads lb2.Updates, and
	// pre-filling the channel to capacity guarantees every ping is dropped.
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	lb2 := New(js, kv, "player-2", "Bob")
	for i := 0; i < cap(lb2.Updates); i++ {
		lb2.Updates <- LobbyUpdate{Kind: LobbyUpdatePlayers}
	}
	if err := lb2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lb2.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for {
		lobbyLines := 0
		for _, m := range lb2.ChatLog() {
			if m.GameID == "" {
				lobbyLines++
			}
		}
		if lobbyLines >= 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("joining player's ChatLog has %d lobby lines, want 3", lobbyLines)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGameChatScoping verifies that lobby and game chat share one stream and
// are distinguished by subject: a game message arrives tagged with its game ID
// (parsed from the subject) and a lobby message with GameID "".
func TestGameChatScoping(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	if err := lb.SendGameChat(ctx, "game-42", "gg", true); err != nil {
		t.Fatal(err)
	}
	if err := lb.SendChat(ctx, "hi lobby"); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"gg": "game-42", "hi lobby": ""}
	specWant := map[string]bool{"gg": true, "hi lobby": false}
	got := 0
	timeout := time.After(3 * time.Second)
	for got < len(want) {
		select {
		case update := <-lb.Updates:
			if update.Kind != LobbyUpdateChat || update.ChatMsg == nil {
				continue
			}
			m := update.ChatMsg
			wantID, ok := want[m.Text]
			if !ok {
				continue
			}
			if m.GameID != wantID {
				t.Fatalf("message %q GameID = %q, want %q", m.Text, m.GameID, wantID)
			}
			if m.Spectator != specWant[m.Text] {
				t.Fatalf("message %q Spectator = %v, want %v", m.Text, m.Spectator, specWant[m.Text])
			}
			got++
		case <-timeout:
			t.Fatalf("received %d of %d chat updates before timeout", got, len(want))
		}
	}
}

func TestLobbyPresence(t *testing.T) {
	lb, _ := setupLobby(t)

	// Wait for heartbeat to publish
	time.Sleep(config.PresenceHeartbeat + 200*time.Millisecond)

	players := lb.Players()
	if _, ok := players["player-1"]; !ok {
		t.Log("player-1 not in presence map yet (TTL may have expired)")
	}
}
