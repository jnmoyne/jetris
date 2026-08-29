package engine

import (
	"testing"
	"time"

	"jetris/internal/config"
)

// TestBufferedMovesQueueIsUnbounded: dispatched moves queue in BufferedMoves()
// oldest first, leave it the moment runInput takes them (takeBufferedMove),
// and the queue has no depth limit — a burst far past the eight chips the
// strip can show is kept whole, nothing dropped.
func TestBufferedMovesQueueIsUnbounded(t *testing.T) {
	// No Start(): nothing takes from the queue, so dispatched moves stay.
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)

	e.MoveLeft()
	e.MoveLeft()
	e.RotateCW()
	e.HardDrop()

	want := []MoveType{MoveLeft, MoveLeft, RotateCW, MoveHardDrop}
	got := e.BufferedMoves()
	if len(got) != len(want) {
		t.Fatalf("BufferedMoves len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("BufferedMoves[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	select {
	case <-e.moveReady:
	default:
		t.Fatal("dispatch did not wake runInput")
	}

	// The oldest entry leaves when its processing starts; the rest follow.
	m, ok, more := e.takeBufferedMove()
	if !ok || m != MoveLeft || !more {
		t.Fatalf("takeBufferedMove = %v, %v, %v; want MoveLeft, true, true", m, ok, more)
	}
	if got := e.BufferedMoves(); len(got) != 3 || got[0] != MoveLeft || got[2] != MoveHardDrop {
		t.Fatalf("after take: BufferedMoves = %v, want [MoveLeft RotateCW MoveHardDrop]", got)
	}

	// A burst well past what the strip shows (and past the old channel's
	// depth of 8) queues whole.
	for i := 0; i < 40; i++ {
		e.MoveDown()
	}
	if got := e.BufferedMoves(); len(got) != 43 {
		t.Fatalf("after a burst: BufferedMoves len = %d, want 43 (a move was dropped)", len(got))
	}
	for i := 0; i < 43; i++ {
		if _, ok, _ := e.takeBufferedMove(); !ok {
			t.Fatalf("take %d: queue ran dry early", i)
		}
	}
	if m, ok, more := e.takeBufferedMove(); ok || more {
		t.Fatalf("take from an empty queue = %v, %v, %v; want zero, false, false", m, ok, more)
	}

	// Spectators never queue input.
	e.setMode(ModeSpectator)
	e.MoveLeft()
	if got := e.BufferedMoves(); len(got) != 0 {
		t.Fatalf("spectator dispatch was queued: %v", got)
	}
}

// TestBufferedMovesBurstAllProcessed: a running engine works through a burst
// of moves longer than any fixed buffer, in order, none dropped — the queue
// drains to empty and the piece ends up where every move says it should.
func TestBufferedMovesBurstAllProcessed(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, 5*time.Second, func() bool { return e.Playfield().ActivePieceForPlayer(0) != nil }, "first piece to spawn")

	// Eleven to the left: past the wall the extra ones are no-ops, but each
	// is taken and processed in turn. Then one to the right. A queue that
	// dropped past a depth of 8 would leave the piece on the wall.
	for i := 0; i < 11; i++ {
		e.MoveLeft()
	}
	e.MoveRight()
	waitUntil(t, 5*time.Second, func() bool { return len(e.BufferedMoves()) == 0 }, "the burst to drain")
	leftmost := func() int {
		p := e.Playfield().ActivePieceForPlayer(0)
		if p == nil {
			return -1
		}
		col := -1
		for _, c := range p.Cells() {
			if col < 0 || c[1] < col {
				col = c[1]
			}
		}
		return col
	}
	// The queue empties as the last move's publish starts; its write-through
	// lands a round trip later.
	waitUntil(t, 5*time.Second, func() bool { return leftmost() == 1 }, "the piece one column off the left wall")
}
