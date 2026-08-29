package nativeui

import (
	"errors"
	"testing"
	"time"
)

// TestLinkStateReachesTheGameView: a lost connection shows in the game view
// as the time the link has been down, for as long as it stays down; a
// reconnect clears it; both leave a line in the NATS-messages panel.
func TestLinkStateReachesTheGameView(t *testing.T) {
	a := newTestApp()
	a.msgShow = true
	now := time.Now()
	if v := a.snapshotGame(now); v.linkDown != 0 {
		t.Fatalf("fresh app: linkDown = %v, want 0", v.linkDown)
	}
	a.linkLost(errors.New("websocket ws://x closed"))
	a.mu.Lock()
	a.linkDownAt = now.Add(-2300 * time.Millisecond) // as if 2.3 s ago
	a.mu.Unlock()
	if v := a.snapshotGame(now); v.linkDown != 2300*time.Millisecond {
		t.Fatalf("down: linkDown = %v, want 2.3s", v.linkDown)
	}
	a.linkLost(errors.New("again")) // a second drop while down keeps the first clock
	if v := a.snapshotGame(now); v.linkDown != 2300*time.Millisecond {
		t.Fatalf("second drop: linkDown = %v, want 2.3s", v.linkDown)
	}
	a.linkBack("ws://x")
	if v := a.snapshotGame(now); v.linkDown != 0 {
		t.Fatalf("back: linkDown = %v, want 0", v.linkDown)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.msgLog); n != 3 || a.msgLog[0].subject != "link" || a.msgLog[2].payload != "reconnected to ws://x after 2.3s" {
		t.Fatalf("panel log = %+v, want three link lines ending in the reconnect", a.msgLog)
	}
}

func TestFormatLinkDown(t *testing.T) {
	cases := map[time.Duration]string{
		time.Millisecond:        "0.0s",
		800 * time.Millisecond:  "0.8s",
		2300 * time.Millisecond: "2.3s",
		12 * time.Second:        "12s",
		65 * time.Second:        "1m05s",
	}
	for d, want := range cases {
		if got := formatLinkDown(d); got != want {
			t.Errorf("formatLinkDown(%v) = %q, want %q", d, got, want)
		}
	}
	if linkDownFor(time.Time{}, time.Now()) != 0 {
		t.Error("a zero since is a healthy link")
	}
	now := time.Now()
	if d := linkDownFor(now, now); d != time.Millisecond {
		t.Errorf("a link down for 0 ns reads as %v, want 1ms (never 0)", d)
	}
}
