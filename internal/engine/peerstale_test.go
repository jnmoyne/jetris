package engine

import (
	"context"
	"testing"
	"time"

	"jetris/internal/game"
)

// TestStaleGhostVacatedWhileLivePieceMoves: seat 1 has cells at two
// anchors on the crew's board — its live piece, rewritten every 300 ms,
// and a copy of a piece nobody plays. The idle clock runs per cell: the
// copy, unwritten for the threshold, is vacated while the live piece —
// which kept the seat's single clock restarting forever under the old
// rule — stays. Left standing (its player gone), the live piece goes a
// threshold later, as ever.
func TestStaleGhostVacatedWhileLivePieceMoves(t *testing.T) {
	a, js, gameID := setupEngineSeats(t, 2)
	a.idleVacateAfter = 400 * time.Millisecond
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	waitUntil(t, 3*time.Second, func() bool { return a.HasActivePiece() }, "a's first piece")

	ghost := game.Piece{Type: game.PieceT, Row: 19, Col: 10}
	publishSeatCells(t, js, gameID, ghost, 1)
	live := game.Piece{Type: game.PieceT, Row: 8, Col: 14}
	publishSeatCells(t, js, gameID, live, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepSeatPieceMoving(ctx, js, gameID, live, 1, 300*time.Millisecond)

	waitUntil(t, 5*time.Second, func() bool { return boardHasNoneOf(a.Playfield(), ghost.Cells()) }, "the ghost to be vacated")
	if a.Playfield().ActivePieceForPlayer(1) == nil {
		t.Fatal("seat 1's live piece went with the ghost")
	}
	on := seatCellsOnStream(t, js, gameID, a.Playfield(), 1)
	for _, c := range ghost.Cells() {
		if _, ok := on[game.CellPos{Row: c[0], Col: c[1]}]; ok {
			t.Fatalf("ghost cell (%d,%d) still on the stream", c[0], c[1])
		}
	}
	whole := false
	for _, n := range anchorsOf(on) {
		whole = whole || n == 4
	}
	if !whole {
		t.Fatalf("seat 1's live piece on the stream: %v, want a whole piece", anchorsOf(on))
	}

	// The live piece left standing goes a threshold later.
	cancel()
	waitUntil(t, 5*time.Second, func() bool { return a.Playfield().ActivePieceForPlayer(1) == nil }, "the abandoned live piece to be vacated")
	if a.Playfield().ActivePieceForPlayer(0) == nil {
		t.Fatal("a's own piece went with it")
	}
}
