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
	"gioui.org/io/input"
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
//
// The frame is given a live (if eventless) input source. A zero Source has
// been a DISABLED one since Gio v0.10 moved Enabled() onto it — `s.r != nil
// && !s.disabled`, where v0.8's Context.Enabled() only asked about the
// context's own flag — and every material widget paints its disabled look
// when the context is disabled: without this the buttons in these
// screenshots come out washed grey, which is not what anyone running the app
// sees.
func snapshotPNGSized(t *testing.T, w *headless.Window, dir, name string, size image.Point, frame func(gtx C)) {
	t.Helper()
	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(size),
		Source:      new(input.Router).Source(),
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
		a.connName, a.connURL = "Jetris (EU central)", "nats://172.105.76.148:4222"
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
		a.connName, a.connURL = "Jetris (EU central)", "nats://172.105.76.148:4222"
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
		a.connName, a.connURL = connectionParts(config.Config{RunEmbedded: true, NATSURL: "nats://" + a.embAddr}, "nats://"+a.embAddr, "")
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

	t.Run("game_focus", func(t *testing.T) {
		// Mid-game keyboard focus, driven through a real input.Router the
		// way the window loop does it: the playfield's frame lights up white
		// while the keys drive the piece; after a click into the chat panel
		// the white ring moves to the chat and the editor's hint flips.
		a := newTestApp()
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = []lobby.PlayerSummary{
			{PlayerID: "alice", Name: "alice", Ready: true},
			{PlayerID: "bob", Name: "bob"},
		}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		a.gameStatus = string(config.GameStatusInProgress)
		a.chatLog = []lobby.ChatMessage{{Name: "bob", Text: "good luck", GameID: "g1"}}
		var r input.Router
		frame := func(gtx C) {
			gtx.Source = r.Source()
			a.layout(gtx)
			r.Frame(gtx.Ops)
		}
		gameFrame(a, &r) // the game is playable: the keys go to the board
		snapshotPNG(t, w, dir, "screen_game_keys_board", frame)
		press(&r, chatBtnPressX, chatBtnPressY)
		gameFrame(a, &r)
		gameFrame(a, &r) // the bar's chat button opens the panel and takes the keys
		snapshotPNG(t, w, dir, "screen_game_keys_chat", frame)
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
		fillReplayBoards(rv)
		a.replayView = rv
		a.screen = screenReplay
		snapshotPNG(t, w, dir, "screen_replay", func(gtx C) { a.layout(gtx) })

		// The same replay paused: PAUSED status line, Resume in place of Pause.
		rv.gate.set(true, time.Now())
		snapshotPNG(t, w, dir, "screen_replay_paused", func(gtx C) { a.layout(gtx) })
	})

	// The replay's ending, three seconds into the winner show: the WINNER
	// banner floated up out of the winning well under the trophy and its
	// rank caption, the winner's name gold in bold italic on the board and
	// in the summary line (scores now shown), the verdict on the status line,
	// the beaten board OUT behind a wash — with the trophy graded by the
	// game's rank in its bucket: legendary (#1), epic (top 3, here a teams
	// game), rare (top 10, a co-op run), and the plain bronze cup below that.
	t.Run("spectate_done", func(t *testing.T) { snapshotSpectateDone(t, w, dir) })

	t.Run("game_won", func(t *testing.T) {
		// A winning player's screen a few seconds after the win: the crown
		// settled over the board on top of the fireworks, under the
		// scanlines, beside the game-over box.
		now := time.Date(2026, 7, 23, 14, 6, 3, 300_000_000, time.Local)
		cases := []struct {
			name     string
			gmode    config.GameMode
			team     int
			rank, of int
		}{{"competitive_legendary", config.ModeCompetitive, 0, 1, 12}, {"teams_epic", config.ModeTeams, 1, 2, 12}}
		for _, tc := range cases {
			a := newTestApp()
			a.eng = engine.New(nil, "g1", "alice", "bob", tc.gmode, engine.ModePlayer, 0, tc.team, 0)
			a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Team: 1}, {PlayerID: "bob", Name: "bob", Agent: true}}
			a.screen = screenGame
			a.gameOver, a.won, a.score, a.level = true, true, 4200, 4
			a.teamScores, a.teamLevels = [config.TeamCount]int{3100, 4200}, [config.TeamCount]int{3, 4}
			a.fireworks = newFireworksShow(now.Add(-2500 * time.Millisecond))
			a.decidedAt, a.liveRank, a.liveOf, a.liveRankFinal = now.Add(-3300*time.Millisecond), tc.rank, tc.of, true
			snapshotPNG(t, w, dir, "screen_game_won_"+tc.name, func(gtx C) {
				gtx.Now = now
				a.layout(gtx)
			})
		}
	})

	t.Run("replay_done", func(t *testing.T) {
		cases := []struct {
			name     string
			rec      config.ArchiveRecord
			rank, of int
		}{
			{"legendary", sampleReplayRecord(), 1, 12},
			{"plain", sampleReplayRecord(), 14, 40},
			{"teams_epic", sampleTeamsReplayRecord(), 2, 12},
			{"coop_rare", sampleCoopReplayRecord(), 7, 12},
		}
		for _, tc := range cases {
			a := newTestApp()
			rv := newReplayView(tc.rec, false)
			fillReplayBoards(rv)
			rv.rank, rv.of = tc.rank, tc.of
			rv.done, rv.doneAt = true, time.Date(2026, 7, 23, 14, 6, 0, 0, time.Local)
			a.replayView = rv
			a.screen = screenReplay
			snapshotPNG(t, w, dir, "screen_replay_done_"+tc.name, func(gtx C) {
				gtx.Now = rv.doneAt.Add(3300 * time.Millisecond)
				a.layout(gtx)
			})
		}
	})
}

// The spectator's live ending, composed as the game screen lays it out —
// the legend on the left, the boards strip center, the result box on the
// right — three seconds into the show, for a competitive game whose winner
// is the bucket's best (the legendary cup holding the I piece) and a teams
// game ranked #2 (epic, the T piece). A spectator engine without a stream
// has no opponent boards to show, so the strip is composed here from the
// sample board with the same crown / knockout wraps spectatorBoards uses.
func snapshotSpectateDone(t *testing.T, w *headless.Window, dir string) {
	now := time.Date(2026, 7, 23, 14, 6, 3, 300_000_000, time.Local)
	at := now.Add(-3300 * time.Millisecond)
	cases := []struct {
		name  string
		gmode config.GameMode
		oc    liveOutcome
	}{
		{"competitive", config.ModeCompetitive, liveOutcome{decided: true, winners: map[string]bool{"alice": true}, winTeam: -1,
			scores: map[string]int{"alice": 4200, "bob": 3100}, banner: "WINNER", verdict: "ALICE WINS!", at: at, rank: 1, of: 12}},
		{"teams", config.ModeTeams, liveOutcome{decided: true, winners: map[string]bool{"carol": true, "dave": true}, winTeam: 1,
			banner: "WINNERS", verdict: "TEAM B WINS!", at: at, rank: 2, of: 12}},
	}
	for _, tc := range cases {
		a := newTestApp()
		eng := engine.New(nil, "g1", "spec", "", tc.gmode, engine.ModeSpectator, 0, 0, 0)
		roster := []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice"}, {PlayerID: "bob", Name: "bob", Agent: true}}
		if tc.gmode == config.ModeTeams {
			roster = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Team: 0}, {PlayerID: "bob", Name: "bob", Team: 0},
				{PlayerID: "carol", Name: "carol", Team: 1, Agent: true}, {PlayerID: "dave", Name: "dave", Team: 1}}
		}
		a.eng, a.gamePlayers, a.screen = eng, roster, screenGame
		a.teamScores, a.teamLevels = [config.TeamCount]int{3100, 4200}, [config.TeamCount]int{3, 4}
		snapshotPNG(t, w, dir, "screen_spectate_done_"+tc.name, func(gtx C) {
			gtx.Now = now
			view := a.snapshotGame(now)
			view.outcome = tc.oc
			fillRect(gtx.Ops, image.Rectangle{Max: gtx.Constraints.Max}, colBg)
			layout.UniformInset(unit.Dp(20)).Layout(gtx, func(gtx C) D {
				return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return a.legend(gtx, eng, view, tc.gmode) }),
					layout.Rigid(hSpacer(24)),
					layout.Flexed(1, func(gtx C) D {
						return layout.Center.Layout(gtx, func(gtx C) D {
							cell := gtx.Dp(20)
							var items []layout.FlexChild
							type well struct {
								label string
								col   colorN
								won   bool
								idx   int
							}
							wells := []well{{"alice", render.PlayerColorRGBA(0), true, 0}, {"bob [agent]", render.PlayerColorRGBA(1), false, 1}}
							if tc.gmode == config.ModeTeams {
								wells = []well{{"TEAM A", render.PlayerColorRGBA(0), false, -1}, {"TEAM B", render.PlayerColorRGBA(1), true, -1}}
							}
							for _, wl := range wells {
								wl := wl
								items = append(items, layout.Rigid(func(gtx C) D {
									return layout.Inset{Right: unit.Dp(16)}.Layout(gtx, func(gtx C) D {
										return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
											layout.Rigid(a.boardLabel(wl.label, wl.col, wl.won)),
											layout.Rigid(spacer(4)),
											layout.Rigid(func(gtx C) D {
												fx := &boardFX{}
												if wl.idx < 0 {
													fx.tint = wl.col
												}
												board := a.boardWidget(sampleBoard(), wl.idx, cell, true, fx, gtx.Now)
												if wl.won {
													return a.crownBoard(tc.oc.fx(), gtx.Now)(board, cell)(gtx)
												}
												return a.knockoutBoard(board, cell)(gtx)
											}),
										)
									})
								}))
							}
							return layout.Flex{}.Layout(gtx, items...)
						})
					}),
					layout.Rigid(func(gtx C) D { return a.spectatorResultBox(gtx, view, tc.oc, tc.gmode) }),
				)
			})
			scanlines(gtx)
		})
	}
}

// fillReplayBoards seeds a replay view's boards from the sample board: the
// first board gets it whole, every other board its non-garbage cells.
func fillReplayBoards(rv *replayView) {
	src := sampleBoard()
	for r, row := range src.Rows {
		for c, cell := range row.Cells {
			for i, b := range rv.boards {
				br := r + b.visibleStart
				if br >= b.height || c >= b.width {
					continue
				}
				if i == 0 || (cell.Occupied && !cell.Adversarial) {
					b.rows[br].Cells[c] = cell
				}
			}
		}
	}
}

// sampleTeamsReplayRecord is a finished teams game: Team B (carol, an agent,
// and dave) beat Team A.
func sampleTeamsReplayRecord() config.ArchiveRecord {
	return config.ArchiveRecord{
		GameID:      "g-replay-teams",
		Mode:        config.ModeTeams,
		PlayerCount: 4,
		TeamSize:    2,
		StartedAt:   time.Date(2026, 7, 23, 14, 0, 0, 0, time.Local),
		FinishedAt:  time.Date(2026, 7, 23, 14, 6, 0, 0, time.Local),
		WinningTeam: 1,
		TeamScores:  []int{3100, 4200},
		TeamLevels:  []int{3, 4},
		Players: []config.PlayerResult{
			{PlayerID: "alice", Score: 1600, Level: 3, Team: 0},
			{PlayerID: "bob", Score: 1500, Level: 3, Team: 0},
			{PlayerID: "carol", Score: 2200, Level: 4, Team: 1, Agent: true, Winner: true},
			{PlayerID: "dave", Score: 2000, Level: 4, Team: 1, Winner: true},
		},
	}
}

// sampleCoopReplayRecord is a finished two-seat cooperative run.
func sampleCoopReplayRecord() config.ArchiveRecord {
	return config.ArchiveRecord{
		GameID:      "g-replay-coop",
		Mode:        config.ModeCooperative,
		PlayerCount: 2,
		StartedAt:   time.Date(2026, 7, 23, 14, 0, 0, 0, time.Local),
		FinishedAt:  time.Date(2026, 7, 23, 14, 6, 0, 0, time.Local),
		WinningTeam: -1,
		TotalScore:  5200,
		FinalLevel:  5,
		Players: []config.PlayerResult{
			{PlayerID: "alice", Score: 2700, Level: 5},
			{PlayerID: "bob", Score: 2500, Level: 5},
		},
	}
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
