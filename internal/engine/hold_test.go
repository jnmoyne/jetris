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
	"jetris/internal/rng"
	"jetris/internal/testutil"
)

// setupHoldEngine is a one-seat competitive engine on a fresh server whose
// meta carries the hold rule (or not).
func setupHoldEngine(t *testing.T, gameID string, hold bool) (*Engine, *rng.Sequence) {
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
		GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 1, NextCount: 2, Hold: hold,
		Seed: 5, Status: config.GameStatusInProgress,
		CreatorID: "p1", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	e := New(js, gameID, "p1", "", config.ModeCompetitive, ModePlayer, 0, 0, 0)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Stop)
	waitUntil(t, 3*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")
	return e, rng.New(meta.Seed)
}

func activeType(e *Engine) (game.PieceType, bool) {
	p := e.Playfield().ActivePieceForPlayer(0)
	if p == nil {
		return 0, false
	}
	return p.Type, true
}

// The Guideline hold: with the slot empty the falling piece goes in and the
// NEXT piece comes out at the spawn point (the queue advances); a second hold
// on the same piece is refused; once the piece locks and the next spawns from
// the queue, a hold swaps the slot's piece back in without touching the
// queue.
func TestHoldTakesNextPieceThenSwaps(t *testing.T) {
	e, seq := setupHoldEngine(t, "hold-swap", true)
	if !e.HoldEnabled() {
		t.Fatal("HoldEnabled() = false with the hold rule in the meta")
	}
	if _, has := e.HeldPiece(); has {
		t.Fatal("the hold slot should start empty")
	}
	first, second, third := seq.Piece(0), seq.Piece(1), seq.Piece(2)
	if got, _ := activeType(e); got != first {
		t.Fatalf("first piece = %v, want the sequence's %v", got, first)
	}
	if got := e.NextPieces(); len(got) != 2 || got[0] != second {
		t.Fatalf("NEXT = %v, want [%v %v]", got, second, third)
	}

	// Hold with an empty slot: the piece is stashed, the next one comes out,
	// the queue moves on.
	e.Hold()
	waitUntil(t, 3*time.Second, func() bool { pt, ok := activeType(e); return ok && pt == second }, "the next piece to come out of the hold")
	if held, has := e.HeldPiece(); !has || held != first {
		t.Fatalf("hold slot = (%v, %v), want the first piece %v", held, has, first)
	}
	if e.PieceIdx() != 1 {
		t.Fatalf("PieceIdx = %d after a hold from the queue, want 1", e.PieceIdx())
	}
	if got := e.NextPieces(); got[0] != third {
		t.Fatalf("NEXT after the hold = %v, want it to start with %v", got, third)
	}
	if !e.HoldUsed() {
		t.Fatal("HoldUsed() = false right after a hold")
	}
	p := e.Playfield().ActivePieceForPlayer(0)
	if spawn := game.SpawnPiece(second, config.StandardWidth); p.Row != spawn.Row || p.Col != spawn.Col || p.Orientation != 0 {
		t.Fatalf("swapped-in piece at %+v, want the spawn position %+v", *p, spawn)
	}
	if n := len(lockedCells(e.Playfield())); n != 0 {
		t.Fatalf("%d locked cells after a hold, want none — the outgoing piece must vanish, not lock", n)
	}

	// One hold per piece: a second hold is refused.
	e.Hold()
	e.MoveLeft() // a move after it, so its (dropped) processing is provably over
	waitUntil(t, 3*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0).Col < p.Col }, "the move after the refused hold")
	if got, _ := activeType(e); got != second {
		t.Fatalf("piece after a second hold = %v, want the unchanged %v", got, second)
	}
	if held, _ := e.HeldPiece(); held != first {
		t.Fatalf("hold slot after a refused hold = %v, want %v", held, first)
	}

	// Lock the piece: the next one spawns from the queue and the hold is
	// allowed again.
	e.HardDrop()
	waitUntil(t, 3*time.Second, func() bool { pt, ok := activeType(e); return ok && pt == third && e.PieceIdx() == 2 }, "the third piece to spawn after the drop")
	waitUntil(t, time.Second, func() bool { return !e.HoldUsed() }, "the hold allowance to renew on the spawn")

	// Hold with a full slot: a swap — the slot's piece comes out, this one
	// goes in, the queue stays where it is.
	e.Hold()
	waitUntil(t, 3*time.Second, func() bool { pt, ok := activeType(e); return ok && pt == first }, "the held piece to come out of the slot")
	if held, _ := e.HeldPiece(); held != third {
		t.Fatalf("hold slot after the swap = %v, want %v", held, third)
	}
	if e.PieceIdx() != 2 {
		t.Fatalf("PieceIdx = %d after a swap, want the unchanged 2", e.PieceIdx())
	}
	if got := e.NextPieces(); got[0] != seq.Piece(3) {
		t.Fatalf("NEXT after the swap = %v, want it to start with %v", got, seq.Piece(3))
	}
}

// Without the hold rule the key does nothing: no game created before the
// attribute — and no game whose creator left it off — grows a hold.
func TestHoldDisabledIsNoop(t *testing.T) {
	e, seq := setupHoldEngine(t, "hold-off", false)
	if e.HoldEnabled() {
		t.Fatal("HoldEnabled() = true without the rule")
	}
	before := *e.Playfield().ActivePieceForPlayer(0)
	e.Hold()
	e.MoveRight()
	waitUntil(t, 3*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0).Col > before.Col }, "the move after the ignored hold")
	if got, _ := activeType(e); got != seq.Piece(0) {
		t.Fatalf("piece = %v after an ignored hold, want %v", got, seq.Piece(0))
	}
	if _, has := e.HeldPiece(); has || e.PieceIdx() != 0 {
		t.Fatalf("ignored hold changed state: held=%v pieceIdx=%d", has, e.PieceIdx())
	}
}
