package nativeui

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// The replayed pre-game countdown: the copied countdown messages drive the
// overlay number, each one restarting the pop animation, and the copied meta
// ends the count the moment its recorded status leaves the pre-game window —
// after which a late countdown message can no longer resurrect it.
func TestReplayCountdown(t *testing.T) {
	a := newTestApp()
	rec := config.ArchiveRecord{GameID: "g1", Mode: config.ModeCompetitive, PlayerCount: 2,
		Players: []config.PlayerResult{{PlayerID: "alice"}, {PlayerID: "bob"}}}
	rv := newReplayView(rec, false)

	cd := func(n int) []byte {
		b, _ := json.Marshal(map[string]int{"seconds": n})
		return b
	}
	meta := func(status config.GameStatus) []byte {
		b, _ := json.Marshal(config.GameMeta{GameID: "g1", Status: status})
		return b
	}
	cdSubj, metaSubj := replayCountdownSubject("g1"), replayMetaSubject("g1")

	if rv.countdown != -1 || replayCountdownVisible(rv.countdown, rv.started, rv.done) {
		t.Fatalf("a fresh replay shows a countdown: %d", rv.countdown)
	}
	// The pre-game metas the copy carries ahead of the count leave it alone.
	if a.applyReplayCountdown(rv, metaSubj, meta(config.GameStatusCreated)) || rv.started {
		t.Fatal("a created-status meta started the game")
	}
	// 5..0: every number repaints, and every number's pop starts from scratch.
	var last time.Time
	for _, n := range []int{5, 4, 3, 2, 1, 0} {
		if !a.applyReplayCountdown(rv, cdSubj, cd(n)) {
			t.Fatalf("countdown %d did not repaint", n)
		}
		if rv.countdown != n {
			t.Fatalf("countdown = %d, want %d", rv.countdown, n)
		}
		if !rv.countdownAt.After(last) && !last.IsZero() {
			t.Fatalf("countdown %d did not restart the pop animation", n)
		}
		if !replayCountdownVisible(rv.countdown, rv.started, rv.done) {
			t.Fatalf("countdown %d is not visible", n)
		}
		last = rv.countdownAt
	}
	// The in_progress meta ends the count — once, and for good.
	if !a.applyReplayCountdown(rv, metaSubj, meta(config.GameStatusInProgress)) || !rv.started {
		t.Fatal("the in_progress meta did not end the countdown")
	}
	if replayCountdownVisible(rv.countdown, rv.started, rv.done) {
		t.Fatal("the countdown outlived the game's start")
	}
	if a.applyReplayCountdown(rv, metaSubj, meta(config.GameStatusFinished)) {
		t.Fatal("a later meta repainted for nothing")
	}
	if a.applyReplayCountdown(rv, cdSubj, cd(0)) || rv.countdown != 0 {
		t.Fatal("a countdown message after the start was applied")
	}
	// Cell subjects are none of this function's business.
	if a.applyReplayCountdown(rv, config.ReplayCopySubject("g1", config.CompetitiveCellSubject("g1", "alice", 3, 4)), []byte("{}")) {
		t.Fatal("a cell message was taken for a countdown")
	}
}

// End to end over a real server: a game whose stream holds a countdown is
// copied into the replay stream and replayed, and the replay screen counts the
// numbers down over its boards at the rate they were recorded — then drops
// them when the recorded meta says the game started, and rebuilds the board
// from the cells that follow.
func TestReplaySessionReplaysCountdown(t *testing.T) {
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
	const gameID = "g-replay-countdown"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	meta := config.GameMeta{GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 1,
		ExtraColumns: config.MaxExtraColumns, Seed: 7, Status: config.GameStatusStarting,
		CreatorID: "alice", CreatedAt: time.Now()}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	// The count itself: 3-2-1-GO, a recorded gap apart — a fifth of the real
	// second so the paced replay of it stays a fifth of a real countdown.
	const gap = 200 * time.Millisecond
	for _, n := range []int{3, 2, 1, 0} {
		payload, _ := json.Marshal(map[string]int{"seconds": n})
		if _, err := js.Publish(ctx, config.CountdownSubject(gameID), payload); err != nil {
			t.Fatal(err)
		}
		time.Sleep(gap)
	}
	// GO! held, then the game starts and its first cell lands.
	meta.Status, meta.StartedAt = config.GameStatusInProgress, time.Now()
	data, _ = json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 1); err != nil {
		t.Fatal(err)
	}
	cell, _ := game.Cell{Occupied: true, PieceType: game.PieceT}.Marshal()
	if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, config.VisibleRowStart, 4), cell); err != nil {
		t.Fatal(err)
	}
	if err := natspkg.CopyGameToReplayStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	a := newTestApp()
	a.js = js
	rec := config.ArchiveRecord{GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 1,
		ExtraColumns: config.MaxExtraColumns, StartedAt: meta.StartedAt, FinishedAt: time.Now(),
		WinningTeam: -1, Players: []config.PlayerResult{{PlayerID: "alice"}}}
	rv := newReplayView(rec, false)
	sessCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rv.cancel = cancel
	go a.runReplaySession(sessCtx, rv)

	// Sample the overlay while the session runs: every number must show, in
	// order, no two of them in the same instant (the count is paced, not
	// fast-forwarded through).
	var seen []int
	var at []time.Time
	deadline := time.Now().Add(15 * time.Second)
	for {
		a.mu.Lock()
		n, started, done, errMsg := rv.countdown, rv.started, rv.done, rv.err
		a.mu.Unlock()
		if n >= 0 && !started && (len(seen) == 0 || seen[len(seen)-1] != n) {
			seen, at = append(seen, n), append(at, time.Now())
		}
		if errMsg != "" {
			t.Fatalf("replay session failed: %s", errMsg)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replay never completed (countdown seen: %v)", seen)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if want := []int{3, 2, 1, 0}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("countdown showed %v, want %v", seen, want)
	}
	for i := 1; i < len(at); i++ {
		if d := at[i].Sub(at[i-1]); d < gap/2 {
			t.Errorf("countdown %d came %v after %d, want the recorded ≈%v", seen[i], d, seen[i-1], gap)
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !rv.started || replayCountdownVisible(rv.countdown, rv.started, rv.done) {
		t.Error("the countdown outlived the replayed game's start")
	}
	if c := rv.boards[0].rows[config.VisibleRowStart].Cells[4]; !c.Occupied {
		t.Error("the cell replayed after the countdown never reached the board")
	}
}
