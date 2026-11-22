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

// TestLobbyWatchMirrorsTheBucket runs the lobby watcher against a live
// JetStream server (JETRIS_NATS_URL; skipped otherwise): listings written to
// the bucket reach the mirror, deleted ones leave it, and the watcher ends
// with its context. A private bucket name keeps the test off the server's
// real lobby.
func TestLobbyWatchMirrorsTheBucket(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	a := &Agent{nc: nc, js: js, name: "me", bucket: "JETRIS_LOBBY_TEST_" + randID(6),
		listings: map[string]obj{}, invites: map[string]obj{}, stopCh: make(chan struct{})}
	if a.kv, err = a.ensureLobbyBucket(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.DeleteKeyValue(context.Background(), a.bucket) }()
	wctx, wcancel := context.WithCancel(ctx)
	defer wcancel()
	w, err := a.kv.WatchAll(wctx)
	if err != nil {
		t.Fatal(err)
	}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		a.lobbyWatch(wctx, w)
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
		deadline := time.Now().Add(10 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	put("g1")
	waitFor("g1 to be mirrored", func() bool { return listed("g1") })

	put("g2")
	waitFor("g2 to be mirrored", func() bool { return listed("g2") })
	if err := a.kv.Delete(ctx, "games.g1"); err != nil {
		t.Fatal(err)
	}
	waitFor("the deleted g1 listing to be dropped", func() bool { return !listed("g1") })
	if !listed("g2") {
		t.Fatal("g2 left the mirror with g1")
	}

	wcancel()
	select {
	case <-watchDone:
	case <-time.After(5 * time.Second):
		t.Fatal("lobbyWatch did not return after its context was cancelled")
	}
}
