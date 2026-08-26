package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestLobbyWatchSurvivesBucketDeletion runs the lobby watcher against a live
// JetStream server (JETRIS_NATS_URL; skipped otherwise) and deletes the lobby
// bucket underneath it, as a shared server's purge does to a resident agent.
// The watcher must notice — its ordered consumer's heartbeats stop and nats.go
// closes the subscription, which takes ~10 s — recreate the bucket, drop the
// listing that vanished with the old one, and mirror new writes again, all
// through the KeyValue handle it already had. A private bucket name keeps the
// test off the server's real lobby.
func TestLobbyWatchSurvivesBucketDeletion(t *testing.T) {
	url := os.Getenv("JETRIS_NATS_URL")
	if url == "" {
		t.Skip("set JETRIS_NATS_URL to a JetStream-enabled nats-server to run")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	a := &Agent{nc: nc, js: js, name: "me", bucket: "JETRIS_LOBBY_TEST_" + randID(6),
		listings: map[string]obj{}, invites: map[string]obj{}, stopCh: make(chan struct{})}
	if a.kv, err = a.ensureLobbyBucket(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.DeleteKeyValue(context.Background(), a.bucket) }()
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		a.lobbyWatch(wctx)
	}()

	put := func(gid string) {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"id": gid, "created_at": nowRFC()})
		if _, err := a.kv.Put(ctx, "games."+gid, b); err != nil {
			t.Fatal(err)
		}
	}
	listed := func(gid string) bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		_, ok := a.listings[gid]
		return ok
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	put("g1")
	waitFor("g1 to be mirrored", func() bool { return listed("g1") })

	// Pull the bucket out from under the running watch.
	if err := js.DeleteKeyValue(ctx, a.bucket); err != nil {
		t.Fatal(err)
	}
	waitFor("the bucket to be recreated", func() bool {
		_, err := js.KeyValue(ctx, a.bucket)
		return err == nil
	})
	waitFor("the stale g1 listing to be dropped", func() bool { return !listed("g1") })
	// Our presence entry is republished into the new bucket without waiting
	// for the next presence tick.
	waitFor("presence to be republished", func() bool {
		_, err := a.kv.Get(ctx, "players.me")
		return err == nil
	})
	// And the watch is live again, through the same handle.
	put("g2")
	waitFor("g2 to be mirrored", func() bool { return listed("g2") })
	if listed("g1") {
		t.Fatal("g1 came back: the mirror kept state from the deleted bucket")
	}

	wcancel()
	select {
	case <-watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lobbyWatch did not return after its context was cancelled")
	}
}
