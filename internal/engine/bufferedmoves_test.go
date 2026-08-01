package engine

import (
	"testing"

	"jetris/internal/config"
)

// TestBufferedMovesMirrorsDispatchQueue: dispatched moves appear in
// BufferedMoves() oldest first while they sit in the e.moves buffer, leave it
// the moment runInput would dequeue them (popBufferedMove), and dispatches
// dropped by a full channel are never mirrored.
func TestBufferedMovesMirrorsDispatchQueue(t *testing.T) {
	// No Start(): nothing consumes e.moves, so dispatched moves stay queued.
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

	// Oldest entry leaves when its processing starts.
	e.popBufferedMove()
	got = e.BufferedMoves()
	if len(got) != 3 || got[0] != MoveLeft || got[2] != MoveHardDrop {
		t.Fatalf("after pop: BufferedMoves = %v, want [MoveLeft RotateCW MoveHardDrop]", got)
	}

	// Fill the channel: it still holds the 4 original moves (popBufferedMove
	// only adjusts the mirror; nothing consumed the channel here), so of 6
	// more dispatches only 4 fit (cap 8) and 2 are dropped — dropped inputs
	// must not be mirrored. Mirror: 3 + 4 = 7.
	for i := 0; i < 6; i++ {
		e.MoveDown()
	}
	if got := e.BufferedMoves(); len(got) != 7 {
		t.Fatalf("after overflow: BufferedMoves len = %d, want 7 (dropped input mirrored?)", len(got))
	}

	// Spectators never queue input.
	e.setMode(ModeSpectator)
	e.MoveLeft()
	if got := e.BufferedMoves(); len(got) != 7 {
		t.Fatalf("spectator dispatch was mirrored: len = %d, want 7", len(got))
	}
}
