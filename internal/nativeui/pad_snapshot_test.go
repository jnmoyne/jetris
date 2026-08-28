package nativeui

// Opt-in visual verification of the control pad's placement and sizes:
// renders the player's game screen — a competitive game consuming a real
// stream on an embedded server, so the playfield has its live row count and
// the NEXT/HOLD wells beside it — at a tablet's landscape and portrait
// viewports with the touch pad, and at the default desktop window with the
// mouse pad, via a headless GPU window, and writes PNGs for inspection.
// Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestPadSnapshots

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

func TestPadSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render control pad snapshots")
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
		name  string
		size  image.Point
		touch bool
	}{
		{"pad_touch_landscape", image.Pt(1180, 740), true}, // an 11" tablet, the browser's toolbar over the page
		{"pad_touch_portrait", image.Pt(820, 1100), true},
		{"pad_mouse_desktop", image.Pt(1280, 820), false},
	}
	for i, c := range cases {
		gameID := fmt.Sprintf("pad-shots-%d", i)
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
		a.touchUI = c.touch
		e := engine.New(js, gameID, "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		a.eng = e
		a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		time.Sleep(300 * time.Millisecond) // the first piece spawns and the preview fills
		snapshotPNGSized(t, w, dir, c.name, c.size, func(gtx C) { a.layout(gtx) })
		w.Release()
		e.Stop()
	}
}
