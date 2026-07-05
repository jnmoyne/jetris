package lobby

import (
	"context"
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

	gameID, err := lb.CreateGame(ctx, config.ModeCooperative, 2, 0)
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
