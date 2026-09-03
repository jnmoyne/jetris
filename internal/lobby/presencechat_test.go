package lobby

import (
	"context"
	"testing"
	"time"
)

// systemLines is the lobby's chat log reduced to its system notices.
func systemLines(lb *Lobby) []string {
	var out []string
	for _, m := range lb.ChatLog() {
		if m.System {
			out = append(out, m.Text)
		}
	}
	return out
}

// waitSystemLines polls until the lobby's chat log holds n system notices.
func waitSystemLines(t *testing.T, lb *Lobby, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := systemLines(lb); len(got) >= n {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("chat log never reached %d system lines, has %v", n, systemLines(lb))
	return nil
}

// TestPresenceChatNotices: another player arriving or departing is told in
// the lobby chat as a system line; the backlog at start (Alice's own key)
// and Alice's own heartbeats are not.
func TestPresenceChatNotices(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	if got := systemLines(lb); len(got) != 0 {
		t.Fatalf("system lines before anyone else arrived: %v", got)
	}

	kv, err := js.KeyValue(ctx, lb.kv.Bucket())
	if err != nil {
		t.Fatal(err)
	}
	bob := New(js, kv, "player-2", "Bob")
	if err := bob.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bob.Stop)

	got := waitSystemLines(t, lb, 1)
	if got[0] != "Bob joined the lobby" {
		t.Fatalf("first notice = %q", got[0])
	}
	// A heartbeat re-put of a known key is not another arrival.
	bob.publishPresence(ctx)
	time.Sleep(200 * time.Millisecond)
	if got := systemLines(lb); len(got) != 1 {
		t.Fatalf("heartbeat produced a notice: %v", got)
	}

	bob.Leave(ctx)
	got = waitSystemLines(t, lb, 2)
	if got[1] != "Bob left the lobby" {
		t.Fatalf("second notice = %q", got[1])
	}
	// Bob never hears of his own coming and going, only of Alice's staying.
	if got := systemLines(bob); len(got) != 0 {
		t.Fatalf("Bob's log has notices about himself: %v", got)
	}
}
