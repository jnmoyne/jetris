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
	"gioui.org/widget"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/lobby"
	"jetris/internal/prefs"
	"jetris/internal/render"
)

// snapshotPNG renders one frame with the given layout func into the standard
// 1200×820 window and writes it to dir/name.png.
func snapshotPNG(t *testing.T, w *headless.Window, dir, name string, frame func(gtx C)) {
	t.Helper()
	snapshotPNGSized(t, w, dir, name, image.Pt(1200, 820), frame)
}

// snapshotPNGSized is snapshotPNG for a window of the given size (the
// headless window must have been created at that size).
func snapshotPNGSized(t *testing.T, w *headless.Window, dir, name string, size image.Point, frame func(gtx C)) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(size),
	}
	frame(gtx)
	if err := w.Frame(&ops); err != nil {
		t.Fatalf("frame %s: %v", name, err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
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
		{ts: at(0), subject: "jetris.game.g1.playfield.cell.3.4", payload: `{"active":true,"pieceType":"T","playerIdx":0}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: "jetris.game.g1.playfield.cell.4.3", payload: `{"active":true,"pieceType":"T","playerIdx":0}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(1), subject: "jetris.game.g1.playfield.cell.2.4", payload: `{}`, batch: "8f3a2c91d4", group: 1, batched: true},
		{ts: at(120), subject: "jetris.game.g1.meta", payload: `{"status":"in_progress","level":3,"score":1750}`, group: 2},
		{ts: at(240), subject: "jetris.game.g1.playfield.cell.4.4", payload: `{"occupied":true,"pieceType":"S","playerIdx":1}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(241), subject: "jetris.game.g1.playfield.cell.4.5", payload: `{"occupied":true,"pieceType":"S","playerIdx":1}`, batch: "b17e05aa62", group: 3, batched: true},
		{ts: at(241), subject: "jetris.game.g1.playfield.cell.3.5", payload: `{}`, batch: "b17e05aa62", group: 3, batched: true},
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
		a := NewWithPicker(config.Config{}, []string{"alpha", "beta", "demo"}, "beta", prefs.DefaultFavorites())
		a.th = newTestApp().th
		snapshotPNG(t, w, dir, "screen_login", func(gtx C) { a.layout(gtx) })
	})

	t.Run("lobby", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		// A favorite's full label — long enough to need the header's
		// single-line truncation at the default window.
		a.connLabel = "nats://172.105.76.148:4222 (Jetris (EU central))"
		a.chatLog = []lobby.ChatMessage{
			{Name: "alice", Text: "ready when you are"},
			{Name: "bob", Text: "one more round"},
		}
		snapshotPNG(t, w, dir, "screen_lobby", func(gtx C) { a.layout(gtx) })
	})

	// The same login and lobby screens on a display larger than the design
	// window (1920×1200): the display-adaptive scale (scale.go) stretches
	// them to fill it instead of leaving the 1280×820 layout floating.
	t.Run("large_display", func(t *testing.T) {
		big, err := headless.NewWindow(1920, 1200)
		if err != nil {
			t.Fatalf("headless window: %v", err)
		}
		defer big.Release()
		size := image.Pt(1920, 1200)

		login := NewWithPicker(config.Config{}, []string{"alpha", "beta", "demo"}, "beta", prefs.DefaultFavorites())
		login.th = newTestApp().th
		snapshotPNGSized(t, big, dir, "screen_login_large", size, func(gtx C) { login.layout(gtx) })

		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.connLabel = "nats://172.105.76.148:4222 (Jetris (EU central))"
		a.chatLog = []lobby.ChatMessage{{Name: "alice", Text: "ready when you are"}}
		snapshotPNGSized(t, big, dir, "screen_lobby_large", size, func(gtx C) { a.layout(gtx) })
	})

	// The lobby while hosting the embedded server (LAN mode): the header
	// names the server and the shareable-address line sits under it.
	t.Run("lobby_lan", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		a.usingEmbedded = true
		a.embAddr = "192.168.1.23:4222"
		a.connLabel = connectionLabel(config.Config{RunEmbedded: true, NATSURL: "nats://" + a.embAddr}, "nats://"+a.embAddr, "")
		snapshotPNG(t, w, dir, "screen_lobby_lan", func(gtx C) { a.layout(gtx) })
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

	// The teams spectator's two wells side by side on the game's dark ground,
	// each tinted and labeled in its team's color (boardFX.tint): Team A cyan,
	// Team B magenta — the pieces keep their own palette.
	t.Run("board_team_tint", func(t *testing.T) {
		a := newTestApp()
		snap := sampleBoard()
		snapshotPNG(t, w, dir, "screen_board_team_tint", func(gtx C) {
			fillRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, colBg)
			layout.Center.Layout(gtx, func(gtx C) D {
				var items []layout.FlexChild
				for team, label := range []string{"TEAM A", "TEAM B"} {
					team, label := team, label
					col := render.PlayerColorRGBA(team)
					items = append(items, layout.Rigid(func(gtx C) D {
						return layout.Inset{Right: unit.Dp(24)}.Layout(gtx, func(gtx C) D {
							return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
								layout.Rigid(a.body(label, col)),
								layout.Rigid(spacer(4)),
								layout.Rigid(a.boardWidget(snap, -1, 28, true, &boardFX{tint: col}, gtx.Now)),
							)
						})
					}))
				}
				return layout.Flex{}.Layout(gtx, items...)
			})
			scanlines(gtx)
		})
	})

	// A history row for a game with a replay archive: the Replay action next
	// to View board.
	t.Run("history_row_replay", func(t *testing.T) {
		a := newTestApp()
		rec := sampleReplayRecord()
		var viewBtn, replayBtn widget.Clickable
		snapshotPNG(t, w, dir, "screen_history_row_replay", func(gtx C) {
			layout.Center.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Max.X = 900
				return a.archiveHistoryRow(gtx, rec, &viewBtn, &replayBtn, false)
			})
			scanlines(gtx)
		})
	})

	// The same row for a game in its bucket's top 10: gold edge bar and
	// TOP 10 tag, over a plain (recent-only) row for contrast — on the
	// lobby's dark ground, where the marker has to read as a mark and not as
	// a selected row.
	t.Run("history_row_top10", func(t *testing.T) {
		a := newTestApp()
		rec := sampleReplayRecord()
		var viewBtn, replayBtn, viewBtn2, replayBtn2 widget.Clickable
		snapshotPNG(t, w, dir, "screen_history_row_top10", func(gtx C) {
			fillRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, colBg)
			layout.Center.Layout(gtx, func(gtx C) D {
				gtx.Constraints.Max.X = 900
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return a.archiveHistoryRow(gtx, rec, &viewBtn, &replayBtn, true) }),
					layout.Rigid(func(gtx C) D { return a.archiveHistoryRow(gtx, rec, &viewBtn2, &replayBtn2, false) }),
				)
			})
			scanlines(gtx)
		})
	})

	// The replay speed-choice dialog over the lobby.
	t.Run("replay_dialog", func(t *testing.T) {
		a := newTestApp()
		a.lobby = lobby.New(nil, nil, "tester", "tester")
		a.screen = screenLobby
		rec := sampleReplayRecord()
		a.replayChoice = &rec
		snapshotPNG(t, w, dir, "screen_replay_dialog", func(gtx C) { a.layout(gtx) })
	})

	// The replay screen mid-replay: two competitive boards being rebuilt from
	// the replay stream.
	t.Run("replay", func(t *testing.T) {
		a := newTestApp()
		rv := newReplayView(sampleReplayRecord(), false)
		src := sampleBoard()
		for r, row := range src.Rows {
			for c, cell := range row.Cells {
				br := r + rv.boards[0].visibleStart
				if br < rv.boards[0].height && c < rv.boards[0].width {
					rv.boards[0].rows[br].Cells[c] = cell
					if cell.Occupied && !cell.Adversarial {
						rv.boards[1].rows[br].Cells[c] = cell
					}
				}
			}
		}
		a.replayView = rv
		a.screen = screenReplay
		snapshotPNG(t, w, dir, "screen_replay", func(gtx C) { a.layout(gtx) })

		// The same replay paused: PAUSED status line, Resume in place of Pause.
		rv.gate.set(true, time.Now())
		snapshotPNG(t, w, dir, "screen_replay_paused", func(gtx C) { a.layout(gtx) })
	})
}

// sampleReplayRecord is a finished competitive game with a replay archive.
func sampleReplayRecord() config.ArchiveRecord {
	return config.ArchiveRecord{
		GameID:      "g-replay",
		Mode:        config.ModeCompetitive,
		PlayerCount: 2,
		StartedAt:   time.Date(2026, 7, 23, 14, 0, 0, 0, time.Local),
		FinishedAt:  time.Date(2026, 7, 23, 14, 6, 0, 0, time.Local),
		WinningTeam: -1,
		Players: []config.PlayerResult{
			{PlayerID: "alice", Score: 4200, Level: 4, Winner: true},
			{PlayerID: "bob", Score: 3100, Level: 3},
		},
	}
}
