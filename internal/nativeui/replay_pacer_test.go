package nativeui

import (
	"testing"
	"time"
)

// The original-speed replay pacer: pre-game messages fast-forward, the first
// paced message anchors recorded time to the wall clock, and every later
// message is due on the absolute schedule anchor + (recorded − first) — so a
// slow apply never compounds into drift, and a message already past due waits
// zero.
func TestReplayPacer(t *testing.T) {
	rec0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC) // recorded game start
	wall := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC) // replay wall clock
	p := replayPacer{thresh: rec0.Add(-replayStartGuard)}

	// Pre-game traffic (roster joins minutes earlier) fast-forwards.
	if d := p.delay(rec0.Add(-time.Minute), wall); d != 0 {
		t.Fatalf("pre-game delay = %v, want 0", d)
	}
	// A message with no recorded time (missing header) applies immediately —
	// and must not anchor the schedule.
	if d := p.delay(time.Time{}, wall); d != 0 {
		t.Fatalf("headerless delay = %v, want 0", d)
	}
	// First paced message: applies now, anchors the schedule.
	if d := p.delay(rec0, wall); d != 0 {
		t.Fatalf("first paced delay = %v, want 0", d)
	}
	// Recorded 800ms after the first, asked 300ms of wall time later → 500ms.
	if d := p.delay(rec0.Add(800*time.Millisecond), wall.Add(300*time.Millisecond)); d != 500*time.Millisecond {
		t.Fatalf("paced delay = %v, want 500ms", d)
	}
	// Already past due (a slow apply ate the gap): no wait, no accumulation.
	if d := p.delay(rec0.Add(time.Second), wall.Add(2*time.Second)); d != 0 {
		t.Fatalf("past-due delay = %v, want 0", d)
	}
	// Absolute schedule: the NEXT message is timed from the anchor, not from
	// the delayed predecessor — recorded +3s is due at wall +3s exactly.
	if d := p.delay(rec0.Add(3*time.Second), wall.Add(2500*time.Millisecond)); d != 500*time.Millisecond {
		t.Fatalf("anchored delay = %v, want 500ms", d)
	}
	// A pause shifts the anchor by the paused span: after 2s paused, the
	// message recorded at +3s is due at wall +5s — 2.5s from wall +2.5s.
	p.shift(2 * time.Second)
	if d := p.delay(rec0.Add(3*time.Second), wall.Add(2500*time.Millisecond)); d != 2500*time.Millisecond {
		t.Fatalf("shifted delay = %v, want 2.5s", d)
	}
	// The countdown opens the paced window where it was recorded: its first
	// number falls before the guarded cutoff, so without open it would
	// fast-forward — with it, the number anchors the schedule and the one a
	// second behind it is due a second later.
	c := replayPacer{thresh: rec0.Add(-replayStartGuard)}
	cd0 := rec0.Add(-5700 * time.Millisecond) // "5", the whole count ahead of the guard
	if d := c.delay(cd0, wall); d != 0 || !c.base.IsZero() {
		t.Fatalf("pre-open countdown delay = %v, anchored = %v", d, !c.base.IsZero())
	}
	c.open(cd0)
	if d := c.delay(cd0, wall); d != 0 || !c.base.Equal(cd0) {
		t.Fatalf("opened countdown delay = %v, anchor = %v, want 0 anchored at the number", d, c.base)
	}
	if d := c.delay(cd0.Add(time.Second), wall); d != time.Second {
		t.Fatalf("next number delay = %v, want 1s", d)
	}
	// open only ever widens the window: a later ts leaves the cutoff alone.
	c.open(rec0.Add(time.Minute))
	if !c.thresh.Equal(cd0) {
		t.Fatalf("open moved the cutoff forward to %v", c.thresh)
	}

	// An unanchored schedule has nothing to shift: the first paced message
	// still applies at once and anchors to the moment it is applied.
	q := replayPacer{thresh: rec0.Add(-replayStartGuard)}
	q.shift(time.Minute)
	if d := q.delay(rec0, wall); d != 0 || !q.wall0.Equal(wall) {
		t.Fatalf("shift before anchoring changed the first delay (%v) or anchor (%v)", d, q.wall0)
	}
}
