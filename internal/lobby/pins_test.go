package lobby

import (
	"context"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// A pin is a lobby KV entry, so every lobby's watcher sees it — the pinning
// player's own included (the screen follows the watcher, not the click) —
// and sees the unpin the same way, which a stream purge could never tell a
// consumer. The pin records who set it.
func TestLobbyPins(t *testing.T) {
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
	for _, ensure := range []func(context.Context, jetstream.JetStream) error{
		natspkg.EnsureChatStream, natspkg.EnsureArchiveStream, natspkg.EnsureReplayStream,
	} {
		if err := ensure(ctx, js); err != nil {
			t.Fatal(err)
		}
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	// A pin set before either lobby starts is part of the initial load.
	if err := natspkg.PinReplay(ctx, kv, "old-pin", "carol"); err != nil {
		t.Fatal(err)
	}

	alice := New(js, kv, "alice", "Alice")
	bob := New(js, kv, "bob", "Bob")
	for _, lb := range []*Lobby{alice, bob} {
		if err := lb.Start(ctx); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(lb.Stop)
		if err := lb.WaitForInitialLoad(ctx); err != nil {
			t.Fatal(err)
		}
	}
	waitPinned := func(lb *Lobby, id string, want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for lb.IsPinned(id) != want {
			if time.Now().After(deadline) {
				t.Fatalf("%s: IsPinned(%s) never became %v", lb.name, id, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitPinned(alice, "old-pin", true)
	waitPinned(bob, "old-pin", true)
	if alice.IsPinned("never") {
		t.Fatal("an unpinned game reads pinned")
	}

	if err := alice.PinReplay(ctx, "game-1"); err != nil {
		t.Fatal(err)
	}
	waitPinned(alice, "game-1", true)
	waitPinned(bob, "game-1", true)
	if pin, ok := bob.Pin("game-1"); !ok || pin.PinnedBy != "Alice" || pin.GameID != "game-1" || pin.PinnedAt.IsZero() {
		t.Fatalf("pin record = %+v (%v), want Alice's, timestamped", pin, ok)
	}

	if err := bob.UnpinReplay(ctx, "game-1"); err != nil {
		t.Fatal(err)
	}
	waitPinned(alice, "game-1", false)
	waitPinned(bob, "game-1", false)
	if _, ok := alice.Pin("game-1"); ok {
		t.Fatal("an unpinned game still has a pin record")
	}
	// The key is what the archivers read.
	if pins, err := natspkg.ListPinnedReplays(ctx, kv); err != nil || len(pins) != 1 || !pins["old-pin"] {
		t.Fatalf("KV pin set = %v (err %v), want just old-pin", pins, err)
	}
	if key := config.LobbyPinKey("x"); config.GameIDFromPinKey(key) != "x" || config.GameIDFromPinKey("games.x") != "" {
		t.Fatal("pin key round trip")
	}
}
