package nativeui

// Opt-in visual verification for the 8-bit look and feel: renders the login,
// lobby, game (plain and with the NATS message strip up) and archive screens
// plus a populated sample board via a headless GPU window and writes PNGs for
// inspection. Skipped unless FW_SNAPSHOT_DIR is set (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestScreenSnapshots

import (
	"image"
	"image/png"
	"os"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetricks/internal/config"
	"jetricks/internal/engine"
	"jetricks/internal/game"
	"jetricks/internal/lobby"
)

// snapshotPNG renders one frame with the given layout func and writes it to
// dir/name.png.
func snapshotPNG(t *testing.T, w *headless.Window, dir, name string, frame func(gtx C)) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1200, 820)),
	}
	frame(gtx)
	if err := w.Frame(&ops); err != nil {
		t.Fatalf("frame %s: %v", name, err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 1200, 820))
	if err := w.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", name, err)
	}
	f, err := os.Create(dir + "/" + name + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// sampleBoard builds a small hand-authored snapshot exercising every cell
// look: empty grid, locked stacks from several players, an active piece, and
// an adversarial row.
func sampleBoard() engine.BoardSnapshot {
	const w, h = 10, 16
	rows := make([]game.Row, h)
	for r := range rows {
		rows[r] = game.Row{Cells: make([]game.Cell, w)}
	}
	lock := func(r, c int, pt game.PieceType, pi int) {
		rows[r].Cells[c] = game.Cell{Occupied: true, PieceType: pt, PlayerIdx: pi}
	}
	active := func(r, c int, pt game.PieceType, pi int) {
		rows[r].Cells[c] = game.Cell{Active: true, PieceType: pt, PlayerIdx: pi}
	}
	// A falling T piece (own, player 0).
	active(3, 4, game.PieceT, 0)
	active(4, 3, game.PieceT, 0)
	active(4, 4, game.PieceT, 0)
	active(4, 5, game.PieceT, 0)
	// Locked stacks from a few players.
	for c := 0; c < 4; c++ {
		lock(13, c, game.PieceJ, 1)
	}
	for c := 2; c < 7; c++ {
		lock(14, c, game.PieceS, 0)
	}
	lock(12, 0, game.PieceL, 1)
	lock(12, 6, game.PieceI, 2)
	lock(13, 6, game.PieceI, 2)
	lock(13, 8, game.PieceO, 0)
	lock(14, 8, game.PieceO, 0)
	// Adversarial garbage row at the bottom.
	for c := 0; c < w; c++ {
		rows[15].Cells[c] = game.Cell{Occupied: true, Adversarial: true, PlayerIdx: 3}
	}
	return engine.BoardSnapshot{Width: w, Height: h, VisibleStart: 0, Rows: rows}
}

// sampleStreamMsgs is a hand-authored message log for the "Show NATS messages"
// strip: two multi-cell moves (each one atomic batch, so each is one tinted
// transaction block) with an untinted single-message meta publish between them.
func sampleStreamMsgs() []streamMsg {
	base := time.Date(2026, 1, 1, 20, 15, 4, 0, time.UTC)
	at := func(ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }
	return []streamMsg{
		{ts: at(0), subject: "jetricks.game.g1.playfield.cell.3.4", payload: `{"active":true,"pieceType":"T","playerIdx":0}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: "jetricks.game.g1.playfield.cell.4.3", payload: `{"active":true,"pieceType":"T","playerIdx":0}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: "jetricks.game.g1.playfield.cell.2.4", payload: `{}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(120), subject: "jetricks.game.g1.meta", payload: `{"status":"in_progress","level":3,"score":1750}`, group: 2},
		{ts: at(240), subject: "jetricks.game.g1.playfield.cell.4.4", payload: `{"occupied":true,"pieceType":"S","playerIdx":1}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(241), subject: "jetricks.game.g1.playfield.cell.4.5", payload: `{"occupied":true,"pieceType":"S","playerIdx":1}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(241), subject: "jetricks.game.g1.playfield.cell.3.5", payload: `{}`, batch: "b17e05aa62", group: 3, batched: true},
	}
}

func TestScreenSnapshots(t *testing.T) {
	dir := os.Getenv("FW_SNAPSHOT_DIR")
	if dir == "" {
		t.Skip("set FW_SNAPSHOT_DIR to render screen snapshots")
	}
	w, err := headless.NewWindow(1200, 820)
	if err != nil {
		t.Fatalf("headless window: %v", err)
	}
	defer w.Release()

	t.Run("login", func(t *testing.T) {
		a := NewWithPicker(config.Config{}, []string{"alpha", "beta", "demo"}, "beta")
		a.th = newTestApp().th
		snapshotPNG(t, w, dir, "screen_login", func(gtx C) { a.layout(gtx) })
	})

	t.Run("lobby", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.chatLog = []lobby.ChatMessage{
			{Name: "alice", Text: "ready when you are"},
			{Name: "bob", Text: "one more round"},
		}
		snapshotPNG(t, w, dir, "screen_lobby", func(gtx C) { a.layout(gtx) })
	})

	t.Run("game", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = []lobby.PlayerSummary{
			{PlayerID: "alice", Name: "alice", Ready: true},
			{PlayerID: "bob", Name: "bob"},
		}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		snapshotPNG(t, w, dir, "screen_game", func(gtx C) { a.layout(gtx) })
	})

	t.Run("game_natsmsgs", func(t *testing.T) {
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
		a.screen = screenGame
		a.showMsgs.Value = true
		a.msgLog = sampleStreamMsgs()
		snapshotPNG(t, w, dir, "screen_game_natsmsgs", func(gtx C) { a.layout(gtx) })
	})

	t.Run("archive", func(t *testing.T) {
		a := newTestApp()
		a.openArchive(config.ArchiveRecord{
			GameID:      "g-done",
			Mode:        config.ModeCompetitive,
			PlayerCount: 2,
			StartedAt:   time.Date(2026, 7, 23, 14, 0, 0, 0, time.Local),
			FinishedAt:  time.Date(2026, 7, 23, 14, 6, 0, 0, time.Local),
			WinningTeam: -1,
			Players: []config.PlayerResult{
				{PlayerID: "alice", Score: 4200, Level: 4, Winner: true},
				{PlayerID: "bob", Score: 3100, Level: 3},
			},
			Chat: []config.ChatLine{
				{Name: "alice", Text: "good luck!", Timestamp: time.Date(2026, 7, 23, 14, 0, 10, 0, time.Local)},
				{Name: "bob", Text: "you too", Timestamp: time.Date(2026, 7, 23, 14, 0, 14, 0, time.Local)},
				{Name: "carol", Text: "go alice", Spectator: true},
			},
		})
		snapshotPNG(t, w, dir, "screen_archive", func(gtx C) { a.layout(gtx) })
	})

	t.Run("board", func(t *testing.T) {
		a := newTestApp()
		snap := sampleBoard()
		snapshotPNG(t, w, dir, "screen_board", func(gtx C) {
			layout.Center.Layout(gtx, a.boardWidget(snap, 0, 32, true, nil, gtx.Now))
			scanlines(gtx)
		})
	})
}
