package engine

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// updateTap drains an engine's lossy Updates channel from the moment it is
// attached, so a test can ask what the engine announced without dropping any
// of it.
type updateTap struct {
	mu   sync.Mutex
	seen []EngineUpdate
}

func tapUpdates(ctx context.Context, e *Engine) *updateTap {
	tap := &updateTap{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-e.Updates:
				tap.mu.Lock()
				tap.seen = append(tap.seen, u)
				tap.mu.Unlock()
			}
		}
	}()
	return tap
}

func (tap *updateTap) count(kind UpdateKind) int {
	tap.mu.Lock()
	defer tap.mu.Unlock()
	n := 0
	for _, u := range tap.seen {
		if u.Kind == kind {
			n++
		}
	}
	return n
}

func publishLineClear(t *testing.T, js jetstream.JetStream, gameID string, ev GameEvent) {
	t.Helper()
	ev.Kind = EventLineClear
	data, _ := json.Marshal(ev)
	if _, err := js.Publish(context.Background(), config.EventKindSubject(gameID, string(EventLineClear), ev.PlayerID), data); err != nil {
		t.Fatal(err)
	}
}

// TestJoinReplaysHistoryWithoutAwardBanner: a player (re)joining a running
// shared-board game replays every line_clear the stream retains — that is how
// the shared score converges — but a clear from before the join is history,
// not news: it must not raise the award banner (nor strobe its rows) as if a
// teammate had just scored. A clear made AFTER the join still does.
func TestJoinReplaysHistoryWithoutAwardBanner(t *testing.T) {
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
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gameID := "join-award-history-game"
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

	// History: the teammate scored a double before we joined.
	publishLineClear(t, js, gameID, GameEvent{
		PlayerID: "p1", PlayerIdx: 1, LinesCleared: 2, ClearedRows: []int{27, 26},
		Score: 320, TotalScore: 320, TotalLines: 2,
	})

	a := New(js, gameID, "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	tap := tapUpdates(ctx, a)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	// The replayed clear folds into the shared score...
	waitUntil(t, 3*time.Second, func() bool { return a.Score() == 320 }, "the replayed clear to fold into the score")
	// ...and a beat later still has raised no banner and strobed no rows.
	time.Sleep(500 * time.Millisecond)
	if n := tap.count(UpdateAward); n != 0 {
		t.Fatalf("joining replayed a clear from before the join as a fresh award banner (%d UpdateAward)", n)
	}
	if n := tap.count(UpdateRowsCleared); n != 0 {
		t.Fatalf("joining strobed the rows of a clear from before the join (%d UpdateRowsCleared)", n)
	}

	// Live: the teammate scores again after we joined — that one is news.
	publishLineClear(t, js, gameID, GameEvent{
		PlayerID: "p1", PlayerIdx: 1, LinesCleared: 1, ClearedRows: []int{27},
		Score: 100, TotalScore: 420, TotalLines: 3,
	})
	waitUntil(t, 3*time.Second, func() bool { return tap.count(UpdateAward) == 1 }, "the live clear's award banner")
	if got := a.Score(); got != 420 {
		t.Fatalf("shared score after the live clear = %d, want 420", got)
	}
	if n := tap.count(UpdateRowsCleared); n != 1 {
		t.Fatalf("live clear strobed %d times, want 1", n)
	}
}
