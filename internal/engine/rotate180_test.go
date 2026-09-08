package engine

import (
	"testing"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
)

// TestRotate180: the half turn is a move of its own — dispatched by
// Engine.Rotate180, played by the look-ahead and the step pipeline as a
// step (batched with the other steps, no barrier), placed by the SRS-X
// kicks, and remembered by the scoring as a rotation that was a half turn.
func TestRotate180(t *testing.T) {
	e := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	p := game.Piece{Type: game.PieceT, Row: 2, Col: 4}
	e.playfield.SetActivePieceForPlayer(p, 0)

	e.Rotate180()
	if got := e.BufferedMoves(); len(got) != 1 || got[0] != Rotate180 {
		t.Fatalf("BufferedMoves = %v, want [Rotate180]", got)
	}
	want := p
	want.Orientation = 2
	if got, ok := e.IntentPiece(); !ok || got != want {
		t.Fatalf("IntentPiece = %+v, %v; want the T turned in place, %+v", got, ok, want)
	}
	if !isStep(Rotate180) {
		t.Fatal("a half turn is a step, not a barrier")
	}

	// On the floor the turn needs the kick that lifts the T a row — the
	// 180 table's tenth entry, reported as such.
	floor := game.Piece{Type: game.PieceT, Row: e.playfield.Height - 2, Col: 4}
	next, kick, ok, last := stepPieceKick(floor, Rotate180, e.playfield, true, 0)
	if !ok || last || kick != 9 || next.Orientation != 2 || next.Row != floor.Row-1 {
		t.Fatalf("stepPieceKick(floor T, 180) = %+v, kick %d, %v, %v; want it turned a row up by kick 9", next, kick, ok, last)
	}

	e.mu.Lock()
	e.noteStep(Rotate180, false, kick)
	spin := e.spin
	e.noteStep(MoveLeft, false, 0)
	after := e.spin
	e.mu.Unlock()
	if spin != (game.SpinState{Rotated: true, Kick: 9, Half: true}) {
		t.Fatalf("spin after a half turn = %+v, want rotated, kick 9, half", spin)
	}
	if after.Rotated {
		t.Fatalf("spin after a shift = %+v, want the rotation forgotten", after)
	}
}

// TestEngineRotate180Publishes: the half turn goes down the real publish
// path — a shared board's CAS pipeline against a live server — and comes
// back in the acked board turned, its new orientation on the wire.
func TestEngineRotate180Publishes(t *testing.T) {
	e, _, _ := setupEngineSeats(t, 2)
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()

	// The O never turns: drop until the piece is any other.
	var p game.Piece
	for i := 0; i < 8; i++ {
		waitUntil(t, 10*time.Second, func() bool { return e.HasActivePiece() }, "a piece")
		p = *e.Playfield().ActivePieceForPlayer(0)
		if p.Type != game.PieceO {
			break
		}
		idx := e.PieceIdx()
		e.HardDrop()
		waitUntil(t, 5*time.Second, func() bool { return e.PieceIdx() > idx && e.HasActivePiece() }, "the next piece")
	}
	if p.Type == game.PieceO {
		t.Fatal("eight O pieces in a row")
	}

	e.Rotate180()
	want := (p.Orientation + 2) % 4
	waitUntil(t, 5*time.Second, func() bool {
		q := snapshotPiece(e.Snapshot(), 0)
		// Gravity may have stepped it down a row meanwhile; in the open the
		// turn is in place, so the column holds.
		return q != nil && q.Type == p.Type && q.Orientation == want && q.Col == p.Col && q.Row >= p.Row
	}, "the acked board to show the half turn")
}
