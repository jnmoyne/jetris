package engine

import (
	"testing"

	"jetris/internal/config"
	"jetris/internal/game"
)

// TestOffline: an Offline engine answers every accessor a screen calls as a
// live one-seat game would — the visible region below the headroom, the
// NEXT well off its sequence, the hold slot, the piece where the board has
// it — with nothing started and nothing in flight.
func TestOffline(t *testing.T) {
	pf := game.NewPlayfieldWithHeight(config.StandardWidth, config.TotalRows)
	pf.Rows[config.TotalRows-1].Cells[0] = game.Cell{Occupied: true, PieceType: game.PieceJ}
	want := game.Piece{Type: game.PieceT, Row: config.VisibleRowStart + 2, Col: 3}
	pf.SetActivePieceForPlayer(want, 0)
	held := game.PieceI
	e := Offline(OfflineGame{
		GameID: "demo", PlayerID: "p1", Mode: config.ModeCooperative,
		Board: pf, Rules: config.GuidelineRules(), Seed: 7, Held: &held,
	})
	if e.Started() {
		t.Error("an offline engine reports itself started")
	}
	snap := e.Snapshot()
	if snap.VisibleStart != config.VisibleRowStart || snap.Height != config.TotalRows || snap.Width != config.StandardWidth {
		t.Errorf("snapshot %dx%d from row %d, want %dx%d from %d", snap.Width, snap.Height, snap.VisibleStart, config.StandardWidth, config.TotalRows, config.VisibleRowStart)
	}
	if !snap.Rows[config.TotalRows-1].Cells[0].Occupied {
		t.Error("the board handed in is not the one shown")
	}
	if echo := e.EchoSnapshot(); echo.VisibleStart != snap.VisibleStart || len(echo.Rows) != len(snap.Rows) {
		t.Error("the echo snapshot is not the board")
	}
	rules := config.GuidelineRules()
	if n := e.NextPieces(); len(n) != rules.NextCount {
		t.Errorf("NEXT well shows %d pieces, want %d", len(n), rules.NextCount)
	}
	if !e.HoldEnabled() || !e.ShowGhost() {
		t.Errorf("hold=%v ghost=%v, want both on under the Guideline preset", e.HoldEnabled(), e.ShowGhost())
	}
	if h, ok := e.HeldPiece(); !ok || h != game.PieceI {
		t.Errorf("hold slot %v/%v, want the I", h, ok)
	}
	if e.Mode() != ModePlayer || e.GameMode() != config.ModeCooperative || e.PlayerIdx() != 0 {
		t.Errorf("mode %v / %v / seat %d", e.Mode(), e.GameMode(), e.PlayerIdx())
	}
	if p, ok := e.IntentPiece(); !ok || p != want {
		t.Errorf("intent %+v/%v, want %+v", p, ok, want)
	}
	if p, ok := e.AckedPiece(); !ok || p != want {
		t.Errorf("acked %+v/%v, want %+v", p, ok, want)
	}
	if _, ok := e.PendingDrop(); ok {
		t.Error("a drop is pending on a board nothing moves on")
	}
	if len(e.BufferedBatches()) != 0 || e.BatchesTaken() != 0 || e.TeamCount() != 0 {
		t.Error("an offline engine carries pipeline state")
	}
	if !e.solo() {
		t.Error("a one-seat offline co-op game is not solo")
	}
	e.Stop() // never started: nothing to stop, and no panic
}
