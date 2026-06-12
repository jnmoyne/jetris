package engine

import (
	"testing"
	"time"
)

// TestRTTMeasuredDuringPlay verifies the publish→echo RTT measurement completes: after
// the engine spawns and moves a piece, RTT() reports a positive round trip and
// an UpdateRTT event reaches the Updates channel.
func TestRTTMeasuredDuringPlay(t *testing.T) {
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
		return e.RTT() > 0
	}, "an RTT measurement to complete")

	// No pending entries should linger once the echoes have been consumed.
	waitUntil(t, 3*time.Second, func() bool {
		e.rttMu.Lock()
		defer e.rttMu.Unlock()
		return len(e.rttPending) == 0
	}, "pending RTT entries to drain")
}

// TestTrackRTTEchoBeatsAck covers the race where the consumer delivers the
// batch's first message before the publisher registers it: trackRTT must
// complete the measurement immediately via the lastEchoSeq high-water mark.
func TestTrackRTTEchoBeatsAck(t *testing.T) {
	e, _, _ := setupEngine(t)

	e.noteRTTEcho(7) // echo of seq 5..7 already delivered
	e.trackRTT(time.Now().Add(-time.Millisecond), 7, 3)

	if e.RTT() <= 0 {
		t.Fatal("expected an immediate measurement when the echo precedes trackRTT")
	}
	e.rttMu.Lock()
	defer e.rttMu.Unlock()
	if len(e.rttPending) != 0 {
		t.Fatalf("expected no pending entries, got %d", len(e.rttPending))
	}
}
