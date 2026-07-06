package nativeui

// Opt-in visual verification for the 8-bit look and feel: renders the login,
// lobby, and game screens plus a populated sample board via a headless GPU
// window and writes PNGs for inspection. Skipped unless FW_SNAPSHOT_DIR is set
// (needs a GPU):
//
//	FW_SNAPSHOT_DIR=/tmp go test ./internal/nativeui/ -run TestScreenSnapshots

import (
	"image"
	"image/png"
	"os"
	"testing"

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

	t.Run("board", func(t *testing.T) {
		a := newTestApp()
		snap := sampleBoard()
		snapshotPNG(t, w, dir, "screen_board", func(gtx C) {
			layout.Center.Layout(gtx, a.boardWidget(snap, 0, 32, true, nil, gtx.Now))
			scanlines(gtx)
		})
	})
}
