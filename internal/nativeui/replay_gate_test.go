package nativeui

import (
	"context"
	"testing"
	"time"
)

// The pause gate: set flips the state and wakes waiters through the changed
// channel, a resume banks the paused span for take (once), and repeated
// sets of the same state are no-ops.
func TestReplayGate(t *testing.T) {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	g := newReplayGate()
	if paused, _ := g.state(); paused {
		t.Fatal("a new gate is not paused")
	}
	_, changed := g.state()
	g.set(true, t0)
	select {
	case <-changed:
	default:
		t.Fatal("pausing must close the changed channel")
	}
	if paused, _ := g.state(); !paused {
		t.Fatal("state should read paused")
	}
	g.set(true, t0.Add(time.Second)) // no-op: does not move pausedAt
	g.set(false, t0.Add(3*time.Second))
	if d := g.take(); d != 3*time.Second {
		t.Fatalf("paused span = %v, want 3s", d)
	}
	if d := g.take(); d != 0 {
		t.Fatalf("a second take must be empty, got %v", d)
	}
	g.set(false, t0.Add(time.Hour)) // no-op: nothing banked
	if d := g.take(); d != 0 {
		t.Fatalf("resuming a running gate must bank nothing, got %v", d)
	}
}

// replayWait holds a message while the gate is paused — in fast mode too —
// returns once resumed, and gives up when the context ends.
func TestReplayWaitHonorsPause(t *testing.T) {
	rv := newReplayView(sampleReplayRecord(), true)
	pacer := replayPacer{}
	rv.gate.set(true, time.Now())

	released := make(chan bool, 1)
	go func() { released <- replayWait(context.Background(), rv, &pacer, time.Time{}) }()
	select {
	case ok := <-released:
		t.Fatalf("replayWait returned %v while paused", ok)
	case <-time.After(150 * time.Millisecond):
	}
	rv.gate.set(false, time.Now())
	select {
	case ok := <-released:
		if !ok {
			t.Fatal("replayWait should report true after a resume")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replayWait never returned after the resume")
	}

	// Cancelling the context releases a paused wait with false.
	rv.gate.set(true, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	go func() { released <- replayWait(ctx, rv, &pacer, time.Time{}) }()
	cancel()
	select {
	case ok := <-released:
		if ok {
			t.Fatal("replayWait should report false when the context ends")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replayWait never returned after cancellation")
	}
}
