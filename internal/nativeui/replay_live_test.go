package nativeui

// Opt-in verification against a REAL recorded game, on a real server: loads
// the most recently archived game's replay the way the screen does and checks
// what it made of it. Skipped unless JETRIS_LIVE_SERVER is set:
//
//	JETRIS_LIVE_SERVER=nats://127.0.0.1:4333 go test ./internal/nativeui/ -run TestLiveReplay -v
//
// Play a game first — two agents will do:
//
//	agents/golang-mk1/golang-mk1 --server nats://127.0.0.1:4333 --name a --difficulty easy \
//	  --create --mode competitive --players 2 --max-agents 2
//	agents/golang-mk1/golang-mk1 --server nats://127.0.0.1:4333 --name b --difficulty easy --join <id>

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
)

func TestLiveReplayLoad(t *testing.T) {
	url := os.Getenv("JETRIS_LIVE_SERVER")
	if url == "" {
		t.Skip("set JETRIS_LIVE_SERVER to load a real archived game")
	}
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

	// The most recently archived game.
	st, err := js.Stream(ctx, config.ArchiveStream)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := st.GetLastMsgForSubject(ctx, config.ArchiveSubject)
	if err != nil {
		t.Fatal(err)
	}
	var rec config.ArchiveRecord
	if err := json.Unmarshal(msg.Data, &rec); err != nil {
		t.Fatal(err)
	}

	a := newTestApp()
	a.js = js
	rv := newReplayView(rec)
	loadCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	rv.cancel = cancel
	start := time.Now()
	go a.runReplayLoad(loadCtx, rv)
	waitUntil(t, time.Minute, func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		if rv.err != "" {
			t.Fatal(rv.err)
		}
		return rv.tl != nil
	}, "the replay to load")
	load, tl := time.Since(start), rv.tl

	t.Logf("%s %s, %d players, %v of play: %d messages loaded in %v — %d cells, %d countdown numbers, %d clears",
		rec.GameID, rec.Mode, rec.PlayerCount, tl.dur.Round(time.Second), rv.loaded, load.Round(time.Millisecond),
		len(tl.cells), len(tl.counts), len(tl.marks))

	// Scrubbing is the whole point of loading it all: seeking anywhere, in
	// either direction, must stay far inside a frame.
	worst := time.Duration(0)
	for i := 0; i <= 100; i++ {
		at := time.Duration(float64((i*37)%101) / 100 * float64(tl.dur))
		t0 := time.Now()
		rv.seek(at)
		if d := time.Since(t0); d > worst {
			worst = d
		}
	}
	t.Logf("worst of 101 scrubs across the game: %v", worst.Round(time.Microsecond))
	if worst > 8*time.Millisecond {
		t.Errorf("a scrub took %v — half a frame at 60Hz", worst)
	}

	// The clears the replay found must be the clears the game had. In
	// competitive a player's score IS their line count (engine.go: scoreDelta
	// = clearedLines), so the archived scoreboard is the ground truth for the
	// markers read off the boards.
	if rec.Mode != config.ModeCompetitive {
		return
	}
	ids := make([]string, 0, len(rec.Players))
	for _, p := range rec.Players {
		ids = append(ids, p.PlayerID)
	}
	sort.Strings(ids)
	lines := map[string]int{}
	for _, m := range tl.marks {
		if m.color >= 0 && m.color < len(ids) {
			lines[ids[m.color]] += m.lines
		}
	}
	for _, p := range rec.Players {
		if got, want := lines[p.PlayerID], p.Score; got != want {
			t.Errorf("%s: %d lines in the markers, %d on the scoreboard", p.PlayerID, got, want)
		}
	}
}
