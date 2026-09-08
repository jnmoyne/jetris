package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// TestRejoinRestoresOwnTotals: a player's ID is their name, so a player who
// leaves a shared-board game and comes back is the same sender to everyone
// else — whose engines fold the DELTA between the cumulative totals they last
// saw from that sender and the ones the next event carries. A fresh engine
// must therefore pick its own cumulative totals back up from the history it
// joins, or its next clear is announced with totals below what the crew last
// saw and dropped as stale everywhere, and its own earlier points are gone
// from the shared score it shows. Seed 5 deals a horizontal I first, so one
// hard drop into a prepared bottom row is the rejoined player's next clear.
func TestRejoinRestoresOwnTotals(t *testing.T) {
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
	gameID := "rejoin-own-totals-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	// The history: before p0 left, p0 had cleared one line for 200 and the
	// teammate p1 a double for 320 — a crew total of 520 over 3 lines.
	publishLineClear(t, js, gameID, GameEvent{PlayerID: "p0", LinesCleared: 1, ClearedRows: []int{27}, Score: 200, TotalScore: 200, TotalLines: 1})
	publishLineClear(t, js, gameID, GameEvent{PlayerID: "p1", PlayerIdx: 1, LinesCleared: 2, ClearedRows: []int{27, 26}, Score: 320, TotalScore: 320, TotalLines: 2})

	// The teammate is still playing; p0 rejoins.
	b := New(js, gameID, "p1", "", config.ModeCooperative, ModePlayer, 1, 0, 0)
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	a := New(js, gameID, "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	// Both engines show the crew's 520 over 3 lines, and each knows its own
	// share again.
	waitUntil(t, 3*time.Second, func() bool { return a.Score() == 520 && b.Score() == 520 }, "the crew total on both engines")
	if got := a.OwnLines(); got != 1 {
		t.Fatalf("rejoined player's own lines = %d, want 1", got)
	}
	if got, want := a.Level(), game.Level(3); got != want {
		t.Fatalf("rejoined player's level = %d, want %d (3 crew lines)", got, want)
	}

	// p0's next clear: the first piece (a horizontal I at cols 3-6) hard
	// dropped into a bottom row prepared with exactly that gap.
	waitUntil(t, 3*time.Second, func() bool { return a.Playfield().ActivePieceForPlayer(0) != nil }, "p0's first piece")
	pf := a.Playfield()
	bottom := pf.Height - 1
	cells := make([]game.Cell, pf.Width)
	gap := map[int]bool{3: true, 4: true, 5: true, 6: true}
	for c := range cells {
		if !gap[c] {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 1}
		}
	}
	publishCoopRowCells(t, js, gameID, bottom, cells)
	waitUntil(t, 3*time.Second, func() bool {
		n := 0
		for _, c := range a.Playfield().Rows[bottom].Cells {
			if c.Occupied {
				n++
			}
		}
		return n == pf.Width-len(gap)
	}, "the prepared bottom row to apply")
	a.HardDrop()
	waitUntil(t, 3*time.Second, func() bool { return a.OwnLines() == 2 }, "p0's clear to register")

	// The clear's event carries p0's CUMULATIVE totals, history included...
	stream, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	last, err := stream.GetLastMsgForSubject(ctx, config.EventKindSubject(gameID, string(EventLineClear), "p0"))
	if err != nil {
		t.Fatal(err)
	}
	var ev GameEvent
	if err := json.Unmarshal(last.Data, &ev); err != nil {
		t.Fatal(err)
	}
	if ev.LinesCleared != 1 || ev.TotalLines != 2 || ev.TotalScore != 200+ev.Score {
		t.Fatalf("rejoined player's clear announced lines %d total %d, score %d total %d; want a single, 2 lines in all, and 200 + the single", ev.LinesCleared, ev.TotalLines, ev.Score, ev.TotalScore)
	}
	// ...so the teammate folds it as news instead of dropping it as stale.
	waitUntil(t, 3*time.Second, func() bool { return b.Score() == 520+ev.Score }, "the teammate to fold the rejoined player's clear")
	if got := a.Score(); got != 520+ev.Score {
		t.Fatalf("rejoined player's shared score = %d, want %d", got, 520+ev.Score)
	}
}

// TestRejoinRestoresOwnTotalsTeams: on a team board the rejoined player's
// share goes back onto their team's scoreboard as well as the shared score
// they show, while the other team's history folds off the replay as usual.
func TestRejoinRestoresOwnTotalsTeams(t *testing.T) {
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
	gameID := "rejoin-own-totals-teams-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeTeams, PlayerCount: 4, TeamSize: 2,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	// Team A's p0 had 200 over one line, team B's p2 300 over two.
	publishLineClear(t, js, gameID, GameEvent{PlayerID: "p0", Team: 0, LinesCleared: 1, Score: 200, TotalScore: 200, TotalLines: 1})
	publishLineClear(t, js, gameID, GameEvent{PlayerID: "p2", PlayerIdx: 2, Team: 1, LinesCleared: 2, Score: 300, TotalScore: 300, TotalLines: 2})

	a := New(js, gameID, "p0", "", config.ModeTeams, ModePlayer, 0, 0, 0)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	waitUntil(t, 3*time.Second, func() bool {
		ts := a.TeamScores()
		return a.Score() == 200 && ts[0] == 200 && ts[1] == 300
	}, "the rejoined player's team share and the other team's history")
	if got := a.OwnLines(); got != 1 {
		t.Fatalf("rejoined player's own lines = %d, want 1", got)
	}
}
