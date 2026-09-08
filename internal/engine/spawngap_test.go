package engine

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// A beta player on a far server (Batch RTT 189 ms) playing at their own top
// speed: the piece is hard dropped and the next piece's inputs — a rotation,
// the drop — are pressed before that piece is on the board, because on that
// link the drop's commit and the spawn behind it are a round trip each.
// Those presses must land on the next piece, in order, not vanish.

// slowCompetitiveEngine is one competitive player's engine over a link with
// the given one-way delay, in the UI's default publishing mode (Optimistic
// async, one batch in flight).
func slowCompetitiveEngine(t *testing.T, gameID string, oneWay time.Duration) *Engine {
	e, _ := slowCompetitiveEngineJS(t, gameID, oneWay)
	return e
}

// slowCompetitiveEngineJS is slowCompetitiveEngine returning the direct
// (undelayed) JetStream handle too, for traffic beside the engine's.
func slowCompetitiveEngineJS(t *testing.T, gameID string, oneWay time.Duration) (*Engine, jetstream.JetStream) {
	t.Helper()
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
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "alice", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	slowNC, err := nats.Connect(testutil.StartLagProxy(t, url, oneWay))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(slowNC.Close)
	slowJS, err := jetstream.New(slowNC)
	if err != nil {
		t.Fatal(err)
	}
	e := New(slowJS, gameID, "alice", "bob", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	e.SetPublishMode(PublishOptimistic)
	e.SetInflightLimit(1)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	waitUntil(t, 10*time.Second, func() bool { return e.HasActivePiece() }, "the first piece")
	return e, js
}

const (
	farOneWay = 95 * time.Millisecond // a 190 ms round trip
	gapPieces = 6                     // drops in a row: the loss is a race, run it a few times
)

// waitForPiece waits for piece idx to be on the board.
func waitForPiece(t *testing.T, e *Engine, idx uint64) {
	t.Helper()
	waitUntil(t, 5*time.Second, func() bool { return e.PieceIdx() == idx && e.HasActivePiece() }, "the next piece")
}

// TestSpawnGapRotationLandsOnNextPiece: the rotation pressed for the next
// piece while the dropped one's lock is still round-tripping is the next
// piece's — it spawns rotated.
func TestSpawnGapRotationLandsOnNextPiece(t *testing.T) {
	e := slowCompetitiveEngine(t, "spawn-gap-rotate", farOneWay)
	for i := uint64(0); i < gapPieces; i++ {
		e.HardDrop()
		time.Sleep(farOneWay / 2) // inside the drop's round trip
		e.RotateCW()
		waitForPiece(t, e, i+1)
		time.Sleep(3 * farOneWay) // every chance for the queued move to be applied
		p := e.Playfield().ActivePieceForPlayer(0)
		if p == nil {
			t.Fatalf("piece %d: no piece after the spawn", i+1)
		}
		if p.Type != game.PieceO && p.Orientation != 1 {
			t.Fatalf("piece %d: the rotation pressed in the lock-to-spawn gap was lost (orientation %d, want 1)", i+1, p.Orientation)
		}
	}
}

// TestSpawnGapHardDropLandsOnNextPiece: the drop pressed for the next piece
// during the gap drops the next piece the moment it is there.
func TestSpawnGapHardDropLandsOnNextPiece(t *testing.T) {
	e := slowCompetitiveEngine(t, "spawn-gap-drop", farOneWay)
	for i := uint64(0); i < gapPieces; i++ {
		waitForPiece(t, e, 2*i)
		e.HardDrop()
		time.Sleep(farOneWay / 2)                   // inside the drop's round trip
		e.HardDrop()                                // the next piece's, pressed a round trip early
		deadline := time.Now().Add(2 * time.Second) // gravity alone (1 s a row at level 0) cannot lock a piece in this time
		for time.Now().Before(deadline) && e.PieceIdx() < 2*i+2 {
			time.Sleep(10 * time.Millisecond)
		}
		if e.PieceIdx() < 2*i+2 {
			t.Fatalf("drop pair %d: the hard drop pressed in the lock-to-spawn gap was lost: piece index %d, want %d", i+1, e.PieceIdx(), 2*i+2)
		}
	}
}

// TestSpawnGapEngineLockHeldForARoundTrip measures how long the engine's
// lock is held across the spawn's publish: the UI frame blocks on it.
func TestSpawnGapEngineLockHeldForARoundTrip(t *testing.T) {
	e := slowCompetitiveEngine(t, "spawn-gap-lock", farOneWay)
	e.HardDrop()
	var worst time.Duration
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		t0 := time.Now()
		e.HasActivePiece()
		if d := time.Since(t0); d > worst {
			worst = d
		}
		time.Sleep(time.Millisecond)
	}
	t.Logf("longest HasActivePiece call around the drop and the spawn: %v", worst)
	if worst > farOneWay {
		t.Fatalf("the engine lock was held for %v across the spawn's round trip: every UI frame in it stalls", worst)
	}
}

// TestFarServerDropBehindStepsOnBusyStream: the beta player's second clip —
// a piece moved twice and dropped, the drop drawn at once, then the piece
// snapping back into the headroom and landing again a round trip or two
// later. The steps ahead of a queued drop go out behind the batch in flight
// with GUESSED expectations, and on a far server the hard agent's writes to
// its own cells land in the game stream during the round trip, so the guess
// is wrong: the batch is rejected as a lost race and repaired. In a
// competitive game nobody else writes the player's cells, so the guess
// protects nothing: those cells go out with no expectation instead, and
// the drop lands once, without a flash.
func TestFarServerDropBehindStepsOnBusyStream(t *testing.T) {
	e, js := slowCompetitiveEngineJS(t, "far-busy-drop", farOneWay)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The opponent, busy: a write to its own board every 10 ms — a hard
	// agent's rate — on the undelayed link, so it lands in the stream
	// while the player's batches are still on the wire.
	go func() {
		cell, _ := game.Cell{Occupied: true, Active: true, PieceType: game.PieceL, PlayerIdx: 1}.Marshal()
		for i := 0; ctx.Err() == nil; i++ {
			_, _ = js.Publish(ctx, config.CompetitiveCellSubject("far-busy-drop", "bob", 10+i%5, i%10), cell)
			time.Sleep(10 * time.Millisecond)
		}
	}()

	var flashes atomic.Int32
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case u := <-e.Updates:
				if u.Kind == UpdateCASFlash {
					flashes.Add(1)
				}
			}
		}
	}()

	for i := uint64(0); i < gapPieces; i++ {
		waitForPiece(t, e, i)
		e.MoveLeft() // out at once
		e.MoveLeft() // queued behind it
		e.HardDrop() // the barrier: the queued left goes out behind the batch in flight
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && e.PieceIdx() < i+1 {
			time.Sleep(10 * time.Millisecond)
		}
		if e.PieceIdx() < i+1 {
			t.Fatalf("piece %d never dropped", i)
		}
	}
	if n := flashes.Load(); n != 0 {
		t.Fatalf("%d lost steps on the player's own board with nobody else writing it: the guessed expectations lost to the opponent's traffic", n)
	}
}
