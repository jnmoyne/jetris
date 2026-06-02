package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/game"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

// TestCoopConcurrentPlayNoPieceCorruption guards against the coop bug where one
// player's authoritative write (lock / hard-drop / line-clear) — which copies the
// whole shared row — clobbered the OTHER player's mid-flight piece from a stale
// local snapshot, producing ghosts (>4 active cells) and mixed-type pieces (e.g.
// a J with a stray resurrected I cell). Two players play concurrently under
// contention; a player's active piece must never PERSISTENTLY have >4 cells or
// mixed types (transient mid-multi-row-update states are allowed and re-checked).
func TestCoopConcurrentPlayNoPieceCorruption(t *testing.T) {
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
	gameID := "coop-concurrency-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	a := New(js, gameID, "p0", "", config.ModeCooperative, ModePlayer, 0)
	b := New(js, gameID, "p1", "", config.ModeCooperative, ModePlayer, 1)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	defer b.Stop()

	// Desynced input maximizes shared-row races between one player's mid-flight
	// piece and the other's authoritative writes.
	stop := make(chan struct{})
	drive := func(e *Engine, left bool) {
		for {
			select {
			case <-stop:
				return
			default:
				if left {
					e.MoveLeft()
				} else {
					e.MoveRight()
				}
				e.MoveDown()
				time.Sleep(40 * time.Millisecond)
			}
		}
	}
	go drive(a, true)
	go drive(b, false)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				a.HardDrop()
				time.Sleep(210 * time.Millisecond)
				b.HardDrop()
				time.Sleep(210 * time.Millisecond)
			}
		}
	}()
	defer close(stop)

	// anomaly reports a player's active-cell count and whether the cells have
	// mixed piece types (read from the shared board via engine a).
	anomaly := func(pi int) (int, bool) {
		n := 0
		types := map[game.PieceType]int{}
		pf := a.Playfield()
		for r := range pf.Rows {
			for _, c := range pf.Rows[r].Cells {
				if c.Active && c.PlayerIdx == pi {
					n++
					types[c.PieceType]++
				}
			}
		}
		return n, len(types) > 1
	}

	deadline := time.Now().Add(9 * time.Second)
	for time.Now().Before(deadline) {
		for pi := 0; pi < 2; pi++ {
			n, mix := anomaly(pi)
			if n > 4 || mix {
				// Re-check after a beat — a real corruption persists; a transient
				// mid-update state resolves on the next tick.
				time.Sleep(300 * time.Millisecond)
				if n2, mix2 := anomaly(pi); n2 > 4 || mix2 {
					t.Fatalf("persistent corruption for player %d: activeCells=%d mixedTypes=%v", pi, n2, mix2)
				}
			}
		}
		if a.Mode() != ModePlayer && b.Mode() != ModePlayer {
			break // both topped out — enough contention exercised
		}
		time.Sleep(20 * time.Millisecond)
	}
}
