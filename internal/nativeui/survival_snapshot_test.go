package nativeui

// Opt-in visual verification of a survival game's screen: a one-seat Easy
// game on an embedded server, rendered once its first raise has landed (the
// SURVIVAL and SURVIVED stats, the raised row) and, on a second game whose
// stack is already at the top, once the raise has ended it (the game-over
// box with the time survived). Skipped unless FW_SNAPSHOT_DIR is set (needs
// a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestSurvivalSnapshots

import (
	"context"
	"encoding/json"
	"image"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestSurvivalSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render survival snapshots")
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
	size := image.Pt(1280, 820)
	for _, c := range []struct {
		name  string
		full  bool // the stack already at the top: the first raise ends the game
		until func(e *engine.Engine) bool
	}{
		{"survival_hud", false, func(e *engine.Engine) bool {
			return e.SurvivalRaises() >= 1 && e.Playfield().AdversarialRowCount() >= 1
		}},
		{"survival_gameover", true, func(e *engine.Engine) bool { _, over := e.GameOutcome(); return over }},
	} {
		gameID := "survival-shot-" + c.name
		if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
			t.Fatal(err)
		}
		meta := config.GameMeta{
			GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 1, NextCount: 3, Hold: true, Seed: 7,
			Survival: config.SurvivalEasy, GarbageHoles: 1,
			Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now().Add(-83 * time.Second),
		}
		data, _ := json.Marshal(meta)
		if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
			t.Fatal(err)
		}
		if c.full {
			cell, _ := game.Cell{Occupied: true, PieceType: game.PieceL}.Marshal()
			if _, err := natspkg.PublishCellsAtomicallyNoCAS(ctx, js, []natspkg.CellUpdate{{Subject: config.CoopCellSubject(gameID, 0, 0), Payload: cell}}); err != nil {
				t.Fatal(err)
			}
		}
		w, err := headless.NewWindow(size.X, size.Y)
		if err != nil {
			t.Fatalf("headless window: %v", err)
		}
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "alice", "alice")
		e := engine.New(js, gameID, "alice", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		a.eng = e
		a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		ectx, cancel := context.WithCancel(ctx)
		go a.pumpEngine(ectx, e)
		deadline := time.Now().Add(10 * time.Second)
		for !c.until(e) && time.Now().Before(deadline) {
			time.Sleep(50 * time.Millisecond)
		}
		if !c.until(e) {
			t.Fatalf("%s: the game never got there", c.name)
		}
		time.Sleep(300 * time.Millisecond) // the update pump folds the game over
		snapshotPNGSized(t, w, dir, c.name, size, func(gtx C) { a.layout(gtx) })
		cancel()
		w.Release()
		e.Stop()
	}
}
