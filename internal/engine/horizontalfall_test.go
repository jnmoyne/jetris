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

// TestCoopHorizontalIFallsWithoutSpuriousLock guards against the bug where a
// single-row piece (the horizontal I) triggered a spurious lock-in on each
// downward move: gravity clears the I's old row and sets its new row in separate
// messages, so the consumer briefly saw the player with no active cells and fired
// a lock-in — replacing the I with the next piece before any input. With seed 5
// the very first piece is a horizontal I; it must simply fall (anchor row grows)
// while staying piece 0, type I.
func TestCoopHorizontalIFallsWithoutSpuriousLock(t *testing.T) {
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
	gameID := "hfall-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 2,
		Seed: 5, Status: config.GameStatusInProgress, // seed 5 -> first piece is I
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

	// Wait for the first piece to spawn and confirm it is a horizontal I.
	var spawnRow int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p := a.Playfield().ActivePieceForPlayer(0); p != nil {
			if p.Type != game.PieceI {
				t.Fatalf("expected first piece to be I (seed 5), got %d", p.Type)
			}
			spawnRow = p.Row
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// No input. Let gravity act for a while.
	time.Sleep(1500 * time.Millisecond)

	// pieceIdx must still be 0 (no spurious lock-in promoted us to the next piece).
	if a.PieceIdx() != 0 || b.PieceIdx() != 0 {
		t.Fatalf("spurious lock-in: pieceIdx advanced with no input (p0=%d p1=%d)", a.PieceIdx(), b.PieceIdx())
	}
	// The I must still be active, still type I, and have fallen (gravity works).
	p := a.Playfield().ActivePieceForPlayer(0)
	if p == nil {
		t.Fatal("I piece disappeared without input")
	}
	if p.Type != game.PieceI {
		t.Fatalf("I changed type to %d without input", p.Type)
	}
	if p.Row <= spawnRow {
		t.Fatalf("I did not fall under gravity (spawnRow=%d nowRow=%d)", spawnRow, p.Row)
	}
}
