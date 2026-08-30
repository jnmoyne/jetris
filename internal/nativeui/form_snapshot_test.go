package nativeui

// Opt-in visual verification of the responsive game screen (formfactor.go,
// compact.go): the compact screen on a phone in both orientations and on a
// tablet held portrait, its two panels open, the on-screen pad switched back
// on, and — for comparison — the full screen on a tablet held landscape and
// with both its panels folded away. Renders a real competitive game
// consuming a real stream on an embedded server, through a headless GPU
// window, and writes PNGs for inspection. Skipped unless FW_SNAPSHOT_DIR is
// set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestFormSnapshots

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestFormSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render responsive-layout snapshots")
	}
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
	cases := []struct {
		name   string
		size   image.Point
		device deviceKind
		tweak  func(*App)
	}{
		{name: "form_phone_portrait", size: image.Pt(390, 844), device: devicePhone},
		{name: "form_phone_portrait_pad", size: image.Pt(390, 844), device: devicePhone,
			tweak: func(a *App) { a.padPref = 1 }},
		{name: "form_phone_portrait_hud", size: image.Pt(390, 844), device: devicePhone,
			tweak: func(a *App) { a.hudDrawer = true }},
		{name: "form_phone_portrait_chat", size: image.Pt(390, 844), device: devicePhone,
			tweak: func(a *App) { a.chatPref = 1 }},
		{name: "form_phone_portrait_opps", size: image.Pt(390, 844), device: devicePhone,
			tweak: func(a *App) { a.oppPref = 1 }},
		{name: "form_phone_portrait_opps_pad", size: image.Pt(390, 844), device: devicePhone,
			tweak: func(a *App) { a.oppPref, a.padPref = 1, 1 }},
		{name: "form_phone_landscape", size: image.Pt(844, 390), device: devicePhone},
		{name: "form_tablet_portrait", size: image.Pt(820, 1180), device: deviceTablet},
		{name: "form_tablet_landscape", size: image.Pt(1180, 740), device: deviceTablet},
		{name: "form_tablet_landscape_hud", size: image.Pt(1180, 740), device: deviceTablet,
			tweak: func(a *App) { a.hudDrawer = true }},
		{name: "form_desktop", size: image.Pt(1280, 820), device: deviceDesktop},
	}
	for i, c := range cases {
		gameID := fmt.Sprintf("form-shots-%d", i)
		if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
			t.Fatal(err)
		}
		meta := config.GameMeta{
			GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2, NextCount: 3, Hold: true, Seed: 7,
			Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
		}
		data, _ := json.Marshal(meta)
		if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
			t.Fatal(err)
		}
		w, err := headless.NewWindow(c.size.X, c.size.Y)
		if err != nil {
			t.Fatalf("headless window: %v", err)
		}
		a := newTestApp()
		a.touchUI = c.device != deviceDesktop
		a.deviceHint, a.deviceHinted = c.device, true
		e := engine.New(js, gameID, "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		a.eng = e
		a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		a.connName, a.connURL = "Jetris EU central", "wss://eu-central.jetris.johnnyxmas.com:4223"
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.chatLog = []lobby.ChatMessage{
			{GameID: gameID, Name: "bob", Text: "gl hf"},
			{GameID: "", Name: "carol", Text: "who's winning?"},
		}
		// The panels shut themselves on a game screen's first frame (a new
		// engine — handleFormClicks); this IS that first frame, so the tweak
		// opens one against an already-established screen.
		a.drawerEng = e
		if c.tweak != nil {
			c.tweak(a)
		}
		time.Sleep(300 * time.Millisecond) // the first piece spawns and the preview fills
		snapshotPNGSized(t, w, dir, c.name, c.size, func(gtx C) { a.layout(gtx) })
		w.Release()
		e.Stop()
	}
}
