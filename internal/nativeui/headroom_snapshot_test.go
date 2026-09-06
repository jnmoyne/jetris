package nativeui

// Opt-in visual verification of the hidden rows behind smoked glass: the
// same board with and without its headroom shown, at the player's cell size
// and at an opponent thumbnail's. Skipped unless FW_SNAPSHOT_DIR is set
// (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestHeadroomSnapshot

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestHeadroomSnapshot(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render the hidden-rows snapshot")
	}
	size := image.Pt(900, 720)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()
	snap := headroomBoard()
	now := time.Now()
	// The ghost of the spawned I, down on the right-hand stack.
	fx := &boardFX{ghost: map[[2]int]game.PieceType{}, frame: colFocus}
	for c := 3; c < 7; c++ {
		fx.ghost[[2]int{snap.Height - 5, c}] = game.PieceI
	}
	snapshotPNGSized(t, w, dir, "headroom_smoked_glass", size, func(gtx C) {
		paint.Fill(gtx.Ops, colBg)
		layout.UniformInset(20).Layout(gtx, func(gtx C) D {
			return layout.Flex{Alignment: layout.End}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return drawBoard(gtx, snap, 0, 26, true, fx, now, true) }),
				layout.Rigid(hSpacer(24)),
				layout.Rigid(func(gtx C) D { return drawBoard(gtx, snap, 0, 26, true, fx, now, false) }),
				layout.Rigid(hSpacer(24)),
				layout.Rigid(func(gtx C) D { return drawBoard(gtx, snap, -1, 10, false, nil, now, true) }),
				layout.Rigid(hSpacer(24)),
				layout.Rigid(func(gtx C) D { return drawBoard(gtx, snap, -1, 10, false, nil, now, false) }),
			)
		})
	})
}

// TestHeadroomScreenSnapshot renders the player's whole game screen — a
// competitive game on an embedded server whose meta shows the hidden rows —
// at the default desktop window, so the board plan can be seen fitting the
// taller board with its wells and pad beside it. Skipped unless
// FW_SNAPSHOT_DIR is set (needs a GPU).
func TestHeadroomScreenSnapshot(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render the hidden-rows screen snapshot")
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
	gameID := "headroom-screen-shot"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2, NextCount: 3, Hold: true, ShowHeadroom: true, Seed: 7,
		Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	size := image.Pt(1280, 820)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()
	a := newTestApp()
	e := engine.New(js, gameID, "alice", "bob", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	a.eng = e
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}, {PlayerID: "bob", Name: "bob"}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	time.Sleep(300 * time.Millisecond) // the first piece spawns, in the headroom
	snapshotPNGSized(t, w, dir, "headroom_screen", size, func(gtx C) { a.layout(gtx) })
}
