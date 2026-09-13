package engine

import (
	"testing"
	"time"

	"jetris/internal/game"
)

// TestOwnStrayCellsSweptOnNextWrite: cells of a's own, far from its piece —
// a copy of the piece a stale collapse stranded — are no part of the piece
// (the anchor the most of a's cells carry) and go with a's next write of
// it, vacated in the same batch, so a's cells make one whole piece on the
// stream again.
func TestOwnStrayCellsSweptOnNextWrite(t *testing.T) {
	a, js, gameID := setupEngineSeats(t, 2)
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()
	waitUntil(t, 3*time.Second, func() bool { return a.HasActivePiece() }, "a's first piece")

	stray := game.Piece{Type: game.PieceT, Row: 14, Col: 7}
	strays := stray.Cells()[:3] // a piece in part, as a stale collapse copied one
	publishSeatCellsAt(t, js, gameID, stray, 0, strays)
	waitUntil(t, 3*time.Second, func() bool { return boardHasCells(a.Playfield(), strays, 0) }, "the strays on a's board")
	if p := a.Playfield().ActivePieceForPlayer(0); p == nil || p.Row != stray.Row-12 && p.Row > 5 {
		t.Fatalf("the strays were taken for the piece: %+v", p)
	}

	a.MoveLeft()
	waitUntil(t, 3*time.Second, func() bool { return boardHasNoneOf(a.Playfield(), strays) }, "the strays to be swept")
	on := seatCellsOnStream(t, js, gameID, a.Playfield(), 0)
	if anchors := anchorsOf(on); len(on) != 4 || len(anchors) != 1 {
		t.Fatalf("seat 0 on the stream after the move: %d cell(s) at %d anchor(s), want one whole piece", len(on), len(anchors))
	}
}
