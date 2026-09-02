package nativeui

// Opt-in visual verification of the teams-mode piece split
// (config.GameMeta.SplitPieces): the create wizard's step 1 offering the
// split, and a player's game screen in a split 2v2, where the HUD legend
// writes every seat's ration under its name in the pieces' own colors.
// Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestSplitPiecesSnapshots

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
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func TestSplitPiecesSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render split-pieces snapshots")
	}
	size := image.Pt(1280, 820)
	w, err := headless.NewWindow(size.X, size.Y)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	// Step 1 of the create wizard for a 2v2, the split box checked: the seat
	// count, the board-width slider and the split under them.
	t.Run("wizard", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.createWizStep = wizStepMode
		a.modeEnum.Value = "teams"
		a.countEd.SetText("2")
		a.splitPiecesCb.Value = true
		snapshotPNGSized(t, w, dir, "wizard_split_pieces", size, func(gtx C) { a.layout(gtx) })
	})

	// A player's screen in a split 2v2, consuming a real stream so the board
	// has its live geometry and the NEXT well its (rationed) preview.
	t.Run("game", func(t *testing.T) {
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
		const gameID = "split-shots"
		if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
			t.Fatal(err)
		}
		meta := config.GameMeta{
			GameID: gameID, Mode: config.ModeTeams, PlayerCount: 4, TeamSize: 2,
			ExtraColumns: config.DefaultExtraColumns, NextCount: 3, SplitPieces: true, Seed: 42,
			Status: config.GameStatusInProgress, CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
		}
		data, _ := json.Marshal(meta)
		if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
			t.Fatal(err)
		}
		a := newTestApp()
		e := engine.New(js, gameID, "alice", "", config.ModeTeams, engine.ModePlayer, 0, 0, 0)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		a.eng = e
		a.gamePlayers = []lobby.PlayerSummary{
			{PlayerID: "alice", Name: "alice", Team: 0, TeamSlot: 0, Ready: true},
			{PlayerID: "bob", Name: "bob", Team: 0, TeamSlot: 1, Ready: true},
			{PlayerID: "carol", Name: "carol", Team: 1, TeamSlot: 0, Ready: true},
			{PlayerID: "dave", Name: "dave", Team: 1, TeamSlot: 1, Ready: true},
		}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		time.Sleep(300 * time.Millisecond) // the first piece spawns and the preview fills
		snapshotPNGSized(t, w, dir, "game_split_pieces", size, func(gtx C) { a.layout(gtx) })
	})
}
