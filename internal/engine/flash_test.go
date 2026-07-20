package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	natspkg "jetricks/internal/nats"
	"jetricks/internal/testutil"
)

// A player's CAS-failure flash is broadcast over core NATS and reaches a
// SPECTATOR engine as an UpdateCASFlash carrying the flashing player's index —
// while the player's own flash never touches the game stream.
func TestFlashReachesSpectator(t *testing.T) {
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
	gameID := "flash-test-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2, Seed: 42,
		Status: config.GameStatusInProgress, CreatorID: "p0",
		CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	// A spectator engine — it subscribes to the flash subject.
	spec := New(js, gameID, "watcher", "", config.ModeCompetitive, ModeSpectator, 0, 0, 0)
	if err := spec.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(spec.Stop)
	time.Sleep(200 * time.Millisecond) // let the subscription establish

	// Drain the spectator's Updates channel, recording flashes.
	got := make(chan EngineUpdate, 16)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-spec.Updates:
				if u.Kind == UpdateCASFlash {
					got <- u
				}
			}
		}
	}()

	// A player (index 1) publishes a flash — the same core message the engine
	// emits on a dropped CAS write.
	fm := FlashMessage{PlayerIdx: 1, Cells: [][2]int{{5, 3}, {5, 4}}}
	fd, _ := json.Marshal(fm)
	if err := nc.Publish(config.FlashSubject(gameID, "rival"), fd); err != nil {
		t.Fatal(err)
	}

	select {
	case u := <-got:
		if u.FlashPlayerIdx != 1 || len(u.FlashCells) != 2 {
			t.Fatalf("spectator flash = idx %d, %d cells; want idx 1, 2 cells", u.FlashPlayerIdx, len(u.FlashCells))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("spectator never received the broadcast flash")
	}

	// The flash must NOT have been captured by the game stream (it's core-only).
	if s, err := js.Stream(ctx, config.GameStream(gameID)); err == nil {
		if _, err := s.GetLastMsgForSubject(ctx, config.FlashSubject(gameID, "rival")); err == nil {
			t.Fatal("flash was persisted in the game stream — must be core NATS only")
		}
	}
}

// A PLAYER engine does not subscribe to flashes: it sees only its own
// (emitted locally), never another player's broadcast.
func TestPlayerIgnoresOthersFlash(t *testing.T) {
	e, js, gameID := setupEngine(t)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	time.Sleep(200 * time.Millisecond)

	got := make(chan struct{}, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-e.Updates:
				if u.Kind == UpdateCASFlash && u.FlashPlayerIdx == 9 {
					got <- struct{}{}
				}
			}
		}
	}()

	// Another player's flash on the wire.
	fm := FlashMessage{PlayerIdx: 9, Cells: [][2]int{{1, 1}}}
	fd, _ := json.Marshal(fm)
	_ = js.Conn().Publish(config.FlashSubject(gameID, "someone"), fd)

	select {
	case <-got:
		t.Fatal("player received another player's flash (should only see its own)")
	case <-time.After(700 * time.Millisecond):
		// expected: no foreign flash
	}
}
