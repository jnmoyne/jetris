package engine

import (
	"testing"
	"time"

	"jetris/internal/config"
)

// TestStartedAndHasActivePiece: a constructed engine is neither started nor
// has a piece; a started one has its piece once the first spawn lands, and
// none in the gap after a hard drop until the next spawn.
func TestStartedAndHasActivePiece(t *testing.T) {
	idle := New(nil, "g", "p0", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	if idle.Started() || idle.HasActivePiece() {
		t.Fatalf("constructed engine: Started %v, HasActivePiece %v; want false, false", idle.Started(), idle.HasActivePiece())
	}

	e, _, _ := setupEngine(t)
	defer e.Stop()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}
	if !e.Started() {
		t.Fatal("Started() false after Start")
	}
	waitUntil(t, 5*time.Second, e.HasActivePiece, "the first piece to spawn")
	e.HardDrop()
	// The piece locks and the next one spawns a round trip later; either
	// state is legitimate at any instant, but the next spawn must arrive.
	waitUntil(t, 5*time.Second, func() bool { return !e.HasActivePiece() || len(e.BufferedMoves()) == 0 }, "the drop to be taken")
	waitUntil(t, 5*time.Second, e.HasActivePiece, "the next piece to spawn")
}
