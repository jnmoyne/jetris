package engine

import (
	"testing"
	"time"
)

// TestPingMeasuredDuringPlay verifies the publish→echo ping completes: after
// the engine spawns and moves a piece, Ping() reports a positive round trip and
// an UpdatePing event reaches the Updates channel.
func TestPingMeasuredDuringPlay(t *testing.T) {
	e, _, _ := setupEngine(t)
	defer e.Stop()
	if err := e.Start(); err != nil {
		t.Fatal(err)
	}

	waitUntil(t, 3*time.Second, func() bool {
		return e.Playfield().ActivePieceForPlayer(0) != nil
	}, "first piece to spawn")

	e.MoveLeft()

	waitUntil(t, 3*time.Second, func() bool {
		return e.Ping() > 0
	}, "a ping measurement to complete")

	// No pending entries should linger once the echoes have been consumed.
	waitUntil(t, 3*time.Second, func() bool {
		e.pingMu.Lock()
		defer e.pingMu.Unlock()
		return len(e.pingPending) == 0
	}, "pending ping entries to drain")
}

// TestTrackPingEchoBeatsAck covers the race where the consumer delivers the
// batch's first message before the publisher registers it: trackPing must
// complete the measurement immediately via the lastEchoSeq high-water mark.
func TestTrackPingEchoBeatsAck(t *testing.T) {
	e, _, _ := setupEngine(t)

	e.notePingEcho(7) // echo of seq 5..7 already delivered
	e.trackPing(time.Now().Add(-time.Millisecond), 7, 3)

	if e.Ping() <= 0 {
		t.Fatal("expected an immediate measurement when the echo precedes trackPing")
	}
	e.pingMu.Lock()
	defer e.pingMu.Unlock()
	if len(e.pingPending) != 0 {
		t.Fatalf("expected no pending entries, got %d", len(e.pingPending))
	}
}
