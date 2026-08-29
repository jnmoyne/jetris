package nativeui

// Opt-in visual verification of the lab toggles (lab.go): the game HUD with
// its LAB section on a live game (a real engine on an embedded server, async
// publishing on, display position 3), and the pre-rendered move outline on a
// transport-less board with queued moves (display position 2). Writes PNGs
// for inspection; skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestLabSnapshots

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestLabSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render lab snapshots")
	}
	size := image.Pt(1280, 820)

	// The live game: the HUD's LAB section, async on, position 3.
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
	gameID := "lab-shots"
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
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	a := newTestApp()
	e := engine.New(js, gameID, "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	a.eng = e
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	a.labEnum.Value = labAsync
	time.Sleep(300 * time.Millisecond) // the first piece spawns
	e.MoveLeft()
	e.MoveLeft()
	e.MoveDown()
	time.Sleep(200 * time.Millisecond) // the burst lands
	snapshotPNGSized(t, w, dir, "lab_hud_async_ack", size, func(gtx C) { a.layout(gtx) })
	w.Release()
	e.Stop()

	// The outline: a transport-less board with a piece and queued moves the
	// engine never takes, display position 2.
	w, err = headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	b := newTestApp()
	b.eng = engine.New(nil, "lab-outline", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	b.eng.SeedActivePiece(game.Piece{Type: game.PieceT, Row: 8, Col: 4})
	b.eng.MoveLeft()
	b.eng.MoveLeft()
	b.eng.MoveDown()
	b.eng.RotateCW()
	b.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	b.readyPlayers = b.gamePlayers
	b.screen = screenGame
	b.gameStatus = string(config.GameStatusInProgress)
	// Optimistic async on the same board: the piece drawn where it is headed,
	// its ghost under it, the acked position outlined.
	b.labEnum.Value = labAsync
	snapshotPNGSized(t, w, dir, "lab_piece_at_once", size, func(gtx C) { b.layout(gtx) })
	w.Release()

	// And with a hard drop queued: the piece painted as already dropped.
	w, err = headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	b.eng.HardDrop()
	snapshotPNGSized(t, w, dir, "lab_pending_drop", size, func(gtx C) { b.layout(gtx) })
	w.Release()
}

// TestStripSnapshot renders the MOVE BUFFER strip alone with a coalesced
// queue — two multi-move batches around a hard drop, four batches in flight
// — for a look at the fused plates. Same opt-in as TestLabSnapshots.
func TestStripSnapshot(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render the strip snapshot")
	}
	size := image.Pt(560, 120)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	a := newTestApp()
	batches := [][]engine.MoveType{
		{engine.MoveLeft, engine.MoveLeft, engine.RotateCW},
		{engine.MoveHardDrop},
		{engine.MoveDown, engine.MoveRight},
		{engine.MoveDown},
	}
	snapshotPNGSized(t, w, dir, "lab_strip_batches", size, func(gtx C) {
		fillRect(gtx.Ops, image.Rect(0, 0, size.X, size.Y), colBg)
		layout.Center.Layout(gtx, func(gtx C) D { return a.bufferedMovesStrip(gtx, batches, 4, 2) })
	})
	w.Release()
}
