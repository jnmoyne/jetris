package lobby

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// The lobby's replay set is driven by the copy-complete markers on the shared
// replay stream: HasReplay flips on the moment a game's marker lands — with
// or without its archive record having arrived — stays off for games without
// a marker, and flips back off when a later marker's reconciliation finds the
// game's replay purged (displaced).
func TestLobbyReplaySet(t *testing.T) {
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
	if err := natspkg.EnsureChatStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	// The archive and replay streams must exist before Start (as Bootstrap
	// guarantees) or the lobby's archive and marker consumers never come up.
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	if err := natspkg.EnsureReplayStream(ctx, js); err != nil {
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

	if lb.HasReplay("with-replay") {
		t.Fatal("HasReplay should start false")
	}

	waitReplay := func(id string, want bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for lb.HasReplay(id) != want {
			if time.Now().After(deadline) {
				t.Fatalf("HasReplay(%s) never became %v", id, want)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Records first (the archiver publishes the record before the copy), then
	// the marker for one of them: only that game gains a replay.
	for _, id := range []string{"with-replay", "no-replay"} {
		data, _ := json.Marshal(config.ArchiveRecord{GameID: id, Mode: config.ModeCooperative})
		if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := js.Publish(ctx, config.ReplayMarkerSubject("with-replay"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitReplay("with-replay", true)
	if lb.HasReplay("no-replay") {
		t.Fatal("HasReplay must stay false for a game without a replay")
	}

	// A marker with no record at all still counts (the record may be on its
	// way) — and a displaced replay, purged before the next archiver's marker,
	// drops out when that marker triggers the reconciliation.
	if err := natspkg.PurgeReplay(ctx, js, "with-replay"); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(ctx, config.ReplayMarkerSubject("later"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	waitReplay("later", true)
	waitReplay("with-replay", false)
}
