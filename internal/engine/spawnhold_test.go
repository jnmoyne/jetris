package engine

import (
	"context"
	"testing"
	"time"

	"jetris/internal/game"
)

// TestDeferredSpawnReleasesHeldMoves: the moves pressed behind the player's
// own hard drop are the next piece's, held for it (awaitSpawn) — for the
// round trip the lock and the spawn take. A spawn deferred behind another
// seat's piece for longer is not that round trip: the hold ends, the held
// moves are let go with a flash on the spawn cells, and the moves pressed
// after that are no-ops at once, not a queue that grows by the tap at a
// board with no piece on it (the incident's MOVE BUFFER 182).
func TestDeferredSpawnReleasesHeldMoves(t *testing.T) {
	a, js, gameID := setupEngineSeats(t, 2)
	a.spawnBlockedAfter = time.Hour // the blocker stays: this is about the moves held behind the drop
	a.spawnHoldRelease = 300 * time.Millisecond
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	flashes := collectFlashes(ctx, a)
	waitUntil(t, 3*time.Second, func() bool { return a.HasActivePiece() }, "a's first piece")
	waitUntil(t, 5*time.Second, func() bool {
		p := a.Playfield().ActivePieceForPlayer(0)
		if p == nil {
			return false
		}
		if p.Col >= 7 {
			return true
		}
		a.MoveRight()
		return false
	}, "a's piece to move right")

	ghost := game.Piece{Type: game.PieceT, Row: 2, Col: 3}
	publishSeatCells(t, js, gameID, ghost, 1)
	live := game.Piece{Type: game.PieceT, Row: 12, Col: 14}
	publishSeatCells(t, js, gameID, live, 1)
	keepSeatPieceMoving(ctx, js, gameID, live, 1, 100*time.Millisecond) // board changes: the deferral's retries
	waitUntil(t, 3*time.Second, func() bool { return boardHasCells(a.Playfield(), ghost.Cells(), 1) }, "the blocker on a's board")

	a.HardDrop()
	waitUntil(t, 3*time.Second, func() bool { return a.AwaitingSpawn() }, "the hold behind the drop")
	for i := 0; i < 50; i++ {
		a.RotateCW() // the next piece's turns, pressed at a board with no piece on it
	}
	waitUntil(t, 3*time.Second, func() bool { return !a.AwaitingSpawn() && len(a.BufferedMoves()) == 0 }, "the held moves to be let go")
	if a.HasActivePiece() {
		t.Fatal("a piece spawned over the blocker")
	}
	waitUntil(t, 2*time.Second, func() bool {
		for _, u := range flashes() {
			if len(u.FlashCells) == 4 && u.FlashCells[0][0] <= 3 {
				return true // the spawn cells, in the headroom
			}
		}
		return false
	}, "the flash on the spawn cells")

	// Later taps do not pile up: consumed at once against the empty board.
	for i := 0; i < 20; i++ {
		a.RotateCW()
	}
	time.Sleep(200 * time.Millisecond)
	if n := len(a.BufferedMoves()); n > 1 {
		t.Fatalf("%d moves queued at a board with no piece, want none", n)
	}

	// The blocker gone, the piece comes, and nothing waits for it.
	vacateCellsDirect(t, js, gameID, ghost.Cells())
	waitUntil(t, 5*time.Second, func() bool { return a.HasActivePiece() }, "the next piece once the blocker is gone")
	if n := len(a.BufferedMoves()); n != 0 {
		t.Fatalf("%d moves queued behind the spawn, want none", n)
	}
}
