package nativeui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// End to end over a real server, against a real game: a competitive engine
// plays a countdown, a piece and a line clear onto its stream; the stream is
// copied into the replay stream the way the archiver copies it; and the replay
// screen loads that copy whole. What the player then holds must be the game
// itself — the countdown it opens on, a marker where the line went, and, at
// the end of the timeline, the very board the engine finished with.
func TestReplayLoadsRealGame(t *testing.T) {
	const gameID = "g-replay-load"
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Seed 5's first piece is a horizontal I (cols 3-6): one hard drop into a
	// row pre-filled everywhere else completes it.
	meta := config.GameMeta{GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusStarting, CreatorID: "p1", CreatedAt: time.Now()}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	// The board the game will start on: the bottom row filled everywhere but
	// the I piece's landing columns, so one hard drop completes it. Written
	// before the countdown, as the pre-game setup it stands in for.
	bottom := config.TotalRows - 1
	for c := 0; c < config.StandardWidth; c++ {
		if c >= 3 && c <= 6 {
			continue // the I piece's landing columns
		}
		payload, _ := game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}.Marshal()
		if _, err := natspkg.PublishCellsAtomicallyNoCAS(ctx, js, []natspkg.CellUpdate{{
			Subject: config.CompetitiveCellSubject(gameID, "p1", bottom, c), Payload: payload,
		}}); err != nil {
			t.Fatal(err)
		}
	}
	// The countdown, at a fifth of its real pace so the test is not five
	// seconds of waiting for it.
	const gap = 200 * time.Millisecond
	for _, n := range []int{3, 2, 1, 0} {
		payload, _ := json.Marshal(map[string]int{"seconds": n})
		if _, err := js.Publish(ctx, config.CountdownSubject(gameID), payload); err != nil {
			t.Fatal(err)
		}
		time.Sleep(gap)
	}
	// The game starts, and is played: one hard drop, one line.
	_, metaSeq, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		t.Fatal(err)
	}
	meta.Status, meta.StartedAt = config.GameStatusInProgress, time.Now()
	data, _ = json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, metaSeq); err != nil {
		t.Fatal(err)
	}
	e := engine.New(js, gameID, "p1", "", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "the first piece")
	e.HardDrop()
	waitUntil(t, 5*time.Second, func() bool {
		return e.Score() > 0 && len(game.CompletedRows(e.Playfield())) == 0
	}, "the line clear")
	time.Sleep(300 * time.Millisecond) // let the clear's writes settle before the copy
	e.Stop()
	final := e.Playfield()

	if err := natspkg.CopyGameToReplayStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// The load, exactly as the screen runs it.
	a := newTestApp()
	a.js = js
	rec := config.ArchiveRecord{GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2,
		StartedAt: meta.StartedAt, FinishedAt: time.Now(), WinningTeam: -1,
		Players: []config.PlayerResult{{PlayerID: "p1", Winner: true}, {PlayerID: "p2"}}}
	rv := newReplayView(rec)
	loadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rv.cancel = cancel
	go a.runReplayLoad(loadCtx, rv)
	waitUntil(t, 20*time.Second, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		if rv.err != "" {
			t.Fatalf("load failed: %s", rv.err)
		}
		return rv.tl != nil
	}, "the replay to load")

	tl := rv.tl
	if rv.total == 0 || rv.loaded != rv.total {
		t.Errorf("loaded %d of %d messages, want the marker's whole count", rv.loaded, rv.total)
	}
	if !rv.playing {
		t.Error("a loaded replay does not start playing")
	}

	// The countdown it opens on: four numbers, ≈gap apart, ending before the
	// game does.
	if len(tl.counts) != 4 {
		t.Fatalf("countdown = %+v, want 3-2-1-GO", tl.counts)
	}
	for i, want := range []int{3, 2, 1, 0} {
		if tl.counts[i].n != want {
			t.Errorf("countdown[%d] = %d, want %d", i, tl.counts[i].n, want)
		}
	}
	if got := tl.counts[3].off; got < 3*gap-50*time.Millisecond {
		t.Errorf("GO! at %v, want the recorded ≈%v after the first number", got, 3*gap)
	}
	if tl.startOff <= tl.counts[3].off || tl.startOff > tl.dur {
		t.Errorf("startOff %v is not between GO! (%v) and the end (%v)", tl.startOff, tl.counts[3].off, tl.dur)
	}
	if _, ok := tl.countdownAt(0); !ok {
		t.Error("the replay does not open on the countdown")
	}
	if _, ok := tl.countdownAt(tl.dur); ok {
		t.Error("the countdown is still showing at the end of the replay")
	}

	// The clear the game actually made, on the board that made it.
	if len(tl.marks) != 1 {
		t.Fatalf("clear markers = %+v, want the one line the hard drop cleared", tl.marks)
	}
	if m := tl.marks[0]; m.lines != 1 || m.color != 0 || m.off < tl.startOff || m.off > tl.dur {
		t.Errorf("marker = %+v, want one line on board 0 inside the game", m)
	}

	// And the boards themselves: seeked to the end, the replay holds the
	// locked cells the engine finished with, cell for cell.
	rv.seek(tl.dur)
	for r := 0; r < final.Height; r++ {
		for c := 0; c < final.Width; c++ {
			want := final.Rows[r].Cells[c]
			got := rv.boards[0].rows[r].Cells[c]
			if lockedEq(want, got) {
				continue
			}
			t.Fatalf("replayed board differs from the engine's at row %d col %d: %+v, want %+v", r, c, got, want)
		}
	}
	// Rewinding puts the opening board back: the row as it was pre-filled,
	// before the piece that completed it ever fell.
	rv.seek(0)
	for r := 0; r < final.Height; r++ {
		for c := 0; c < final.Width; c++ {
			want := r == bottom && (c < 3 || c > 6)
			if got := rv.boards[0].rows[r].Cells[c].Occupied; got != want {
				t.Fatalf("rewound to the start, row %d col %d occupied = %v, want %v", r, c, got, want)
			}
		}
	}
}

// lockedEq compares two cells by what a settled board is made of — a piece
// still falling when the recording ended is the engine's business, not the
// replay's.
func lockedEq(a, b game.Cell) bool {
	if a.Occupied && a.Active {
		return true
	}
	return a.Occupied == b.Occupied && a.PieceType == b.PieceType && a.Adversarial == b.Adversarial
}

// waitUntil polls cond until it holds or the deadline passes.
func waitUntil(t *testing.T, d time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
