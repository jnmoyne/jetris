package engine

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

// startHeadroomEngine is a one-seat cooperative engine on a fresh server,
// started on a meta that shows its hidden rows or not.
func startHeadroomEngine(t *testing.T, gameID string, show bool) *Engine {
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
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 1, NextCount: 1, ShowHeadroom: show,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p1", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	e := New(js, gameID, "p1", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	return e
}

// TestShowHeadroomReachesEngine: a live engine reads the hidden-rows setting
// off the meta at Start — and it changes nothing about the region it plays
// by: the visible rows start under the headroom either way.
func TestShowHeadroomReachesEngine(t *testing.T) {
	e := startHeadroomEngine(t, "headroom-shown", true)
	if !e.ShowHeadroom() {
		t.Error("the engine does not show the headroom its meta asks for")
	}
	if e.VisibleRowStart() != config.VisibleRowStart {
		t.Errorf("visible rows start at %d with the headroom shown, want %d", e.VisibleRowStart(), config.VisibleRowStart)
	}
	plain := startHeadroomEngine(t, "headroom-hidden", false)
	if plain.ShowHeadroom() {
		t.Error("a meta without the field shows the headroom")
	}
}

// TestOfflineShowHeadroom: the transport-less engine (the How to play tour)
// reads the setting off its rules like a live one.
func TestOfflineShowHeadroom(t *testing.T) {
	shown := Offline(OfflineGame{Mode: config.ModeCooperative, Seed: 42, Rules: config.GameRules{NextCount: 1, Ghost: true, ShowHeadroom: true}})
	if !shown.ShowHeadroom() {
		t.Error("the offline engine ignores the hidden-rows rule")
	}
	if Offline(OfflineGame{Mode: config.ModeCooperative, Seed: 42, Rules: config.GameRules{NextCount: 1, Ghost: true}}).ShowHeadroom() {
		t.Error("the offline engine shows the headroom without the rule")
	}
}
