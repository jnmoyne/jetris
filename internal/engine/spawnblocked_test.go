package engine

import (
	"context"
	"testing"
	"time"

	"jetris/internal/game"
)

// TestDeferredSpawnVacatesStaleBlocker: a piece of seat 1's that nobody
// plays — the cells a stale collapse stranded, a crashed crewmate's piece —
// sits in a's spawn box while seat 1's live piece keeps moving elsewhere.
// a's next spawn is deferred behind it, as behind any crewmate's piece
// crossing the box; but these cells never move, and once they have blocked
// for the threshold a vacates exactly them and spawns (deferSpawnLocked).
// The live piece is untouched.
func TestDeferredSpawnVacatesStaleBlocker(t *testing.T) {
	a, js, gameID := setupEngineSeats(t, 2)
	a.spawnBlockedAfter = 500 * time.Millisecond
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	waitUntil(t, 3*time.Second, func() bool { return a.HasActivePiece() }, "a's first piece")

	// a's piece out of its spawn box, to the right, so its drop passes the
	// blocker by.
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

	// A T at a's spawn point covers a cell of every piece's spawn cells.
	ghost := game.Piece{Type: game.PieceT, Row: 2, Col: 3}
	publishSeatCells(t, js, gameID, ghost, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := game.Piece{Type: game.PieceT, Row: 12, Col: 14}
	publishSeatCells(t, js, gameID, live, 1)
	keepSeatPieceMoving(ctx, js, gameID, live, 1, 100*time.Millisecond)
	waitUntil(t, 3*time.Second, func() bool { return boardHasCells(a.Playfield(), ghost.Cells(), 1) }, "the blocker on a's board")

	idx0 := a.PieceIdx()
	a.HardDrop()
	waitUntil(t, 5*time.Second, func() bool { return a.PieceIdx() == idx0+1 && a.HasActivePiece() }, "the next piece, behind the stale blocker")
	if got := a.SpawnUnblocks(); got != 1 {
		t.Fatalf("SpawnUnblocks() = %d, want 1", got)
	}
	if p := a.Playfield().ActivePieceForPlayer(0); p == nil || p.Row != 2 || p.Col != 3 {
		t.Fatalf("the next piece spawned at %+v, want the seat's spawn point (2,3)", p)
	}

	cancel()
	time.Sleep(150 * time.Millisecond)
	on := seatCellsOnStream(t, js, gameID, a.Playfield(), 1)
	for _, c := range ghost.Cells() {
		if _, ok := on[game.CellPos{Row: c[0], Col: c[1]}]; ok {
			t.Fatalf("blocker cell (%d,%d) still on the stream", c[0], c[1])
		}
	}
	if anchors := anchorsOf(on); len(on) != 4 || len(anchors) != 1 {
		t.Fatalf("seat 1's live piece on the stream: %d cell(s) at %d anchor(s), want one whole piece", len(on), len(anchors))
	}
}
