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

// TestPresenceChatNotices: our own connection is told in the lobby chat as
// a system line, as is another player arriving or departing; heartbeats are
// not, and neither is the backlog of who was already here.
func TestPresenceChatNotices(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	got := waitSystemLines(t, lb, 1)
	if got[0] != "You joined the lobby as Alice" {
		t.Fatalf("own connection notice = %q", got[0])
	}
	// Alice's own heartbeat is not another connection.
	lb.publishPresence(ctx)
	time.Sleep(200 * time.Millisecond)
	if got := systemLines(lb); len(got) != 1 {
		t.Fatalf("own heartbeat produced a notice: %v", got)
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

	got = waitSystemLines(t, lb, 2)
	if got[1] != "Bob joined the lobby" {
		t.Fatalf("arrival notice = %q", got[1])
	}
	// A heartbeat re-put of a known key is not another arrival.
	bob.publishPresence(ctx)
	time.Sleep(200 * time.Millisecond)
	if got := systemLines(lb); len(got) != 2 {
		t.Fatalf("heartbeat produced a notice: %v", got)
	}

	bob.Leave(ctx)
	got = waitSystemLines(t, lb, 3)
	if got[2] != "Bob left the lobby" {
		t.Fatalf("departure notice = %q", got[2])
	}
	// Bob hears of his own connection but not of Alice, who was already
	// here when he arrived, nor of his own leaving.
	if got := systemLines(bob); len(got) != 1 || got[0] != "You joined the lobby as Bob" {
		t.Fatalf("Bob's notices = %v", got)
	}
}
