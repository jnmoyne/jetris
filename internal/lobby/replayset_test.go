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

// The lobby's replay set: HasReplay flips on when an archive record arrives
// for a game whose (promoted) replay stream exists — the arrival is what kicks
// the refresher — and games without a replay stream stay off.
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
	// The archive stream must exist before Start (as Bootstrap guarantees) or
	// the lobby's archive consumer — whose deliveries kick the replay
	// refresher — never comes up.
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
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

	// A finished replay is marked by its copy-complete marker on the shared
	// replay stream — publish one, then the records that announce both games.
	if err := natspkg.EnsureReplayStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	if _, err := js.Publish(ctx, config.ReplayMarkerSubject("with-replay"), []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"with-replay", "no-replay"} {
		data, _ := json.Marshal(config.ArchiveRecord{GameID: id, Mode: config.ModeCooperative})
		if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
			t.Fatal(err)
		}
	}

	deadline := time.Now().Add(5 * time.Second)
	for !lb.HasReplay("with-replay") {
		if time.Now().After(deadline) {
			t.Fatal("HasReplay(with-replay) never became true")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lb.HasReplay("no-replay") {
		t.Fatal("HasReplay must stay false for a game without a replay stream")
	}
}
