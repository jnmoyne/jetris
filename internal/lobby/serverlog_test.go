package lobby

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"jetris/internal/config"
)

// waitLog polls until pred holds of the lobby's server log entries.
func waitLog(t *testing.T, lb *Lobby, what string, pred func([]config.LogEntry) bool) []config.LogEntry {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := lb.LogEntries(); pred(got) {
			return got
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server log never showed %s; has %v", what, lb.LogEntries())
	return nil
}

// count is how many entries of one kind about one player the log holds.
func count(entries []config.LogEntry, kind, name string) int {
	n := 0
	for _, e := range entries {
		if e.Kind == kind && e.Name == name {
			n++
		}
	}
	return n
}

// TestServerLogJournal: a lobby journals its own connection, a game it
// creates, the game's start once its countdown has run, and its departure —
// and every lobby reads the journal back, in order.
func TestServerLogJournal(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	waitLog(t, lb, "Alice connected", func(got []config.LogEntry) bool {
		return count(got, config.LogKindConnected, "Alice") == 1
	})

	kv, err := js.KeyValue(ctx, lb.kv.Bucket())
	if err != nil {
		t.Fatal(err)
	}
	bob := New(js, kv, "player-2", "Bob")
	if err := bob.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bob.Stop)
	waitLog(t, lb, "Bob connected", func(got []config.LogEntry) bool {
		return count(got, config.LogKindConnected, "Bob") == 1
	})

	gameID, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{}})
	if err != nil {
		t.Fatal(err)
	}
	got := waitLog(t, bob, "the game created", func(got []config.LogEntry) bool {
		return count(got, config.LogKindGameCreated, "Alice") == 1
	})
	created := got[len(got)-1]
	if created.GameID != gameID || created.Mode != config.ModeCompetitive || created.PlayerCount != 2 {
		t.Fatalf("created entry = %+v", created)
	}
	if created.Text() != "Alice created a 2-player competitive game" {
		t.Fatalf("created text = %q", created.Text())
	}

	lb.StartGame(ctx, gameID)
	got = waitLog(t, bob, "the game started", func(got []config.LogEntry) bool {
		return count(got, config.LogKindGameStarted, "Alice") == 1
	})
	started := got[len(got)-1]
	if started.GameID != gameID || started.Text() != "Alice started a 2-player competitive game" {
		t.Fatalf("started entry = %+v", started)
	}

	// A clean quit journals one departure, however many delete paths run
	// (Leave, then the heartbeat's shutdown delete).
	bob.Leave(ctx)
	bob.Stop()
	time.Sleep(300 * time.Millisecond)
	got = waitLog(t, lb, "Bob disconnected", func(got []config.LogEntry) bool {
		return count(got, config.LogKindDisconnected, "Bob") >= 1
	})
	if n := count(got, config.LogKindDisconnected, "Bob"); n != 1 {
		t.Fatalf("Bob's departure journaled %d times: %v", n, got)
	}
	// Sequence numbers come from the stream and rise with it.
	for i := 1; i < len(got); i++ {
		if got[i].Seq <= got[i-1].Seq {
			t.Fatalf("entries out of order: %v", got)
		}
	}
}

// TestServerLogExpiredDeparture: a presence key the server expires (the
// client crashed) is journaled by the lobbies that saw it go, once between
// them, and the chat tells of the departure.
func TestServerLogExpiredDeparture(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()
	kv, err := js.KeyValue(ctx, lb.kv.Bucket())
	if err != nil {
		t.Fatal(err)
	}
	carol := New(js, kv, "player-3", "Carol")
	if err := carol.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(carol.Stop)

	// A client with no lobby of its own: presence written by hand.
	ghost, _ := json.Marshal(PlayerPresence{PlayerID: "player-9", Name: "Ghost", LastSeen: time.Now()})
	if _, err := kv.Put(ctx, config.LobbyPlayerKey("player-9"), ghost); err != nil {
		t.Fatal(err)
	}
	waitSystemLines(t, lb, 3) // Alice, Carol, Ghost joined
	// The server's expiry marker is a PURGE; a Purge by hand is the same to
	// the watcher.
	if err := kv.Purge(ctx, config.LobbyPlayerKey("player-9")); err != nil {
		t.Fatal(err)
	}
	got := waitSystemLines(t, lb, 4)
	if got[3] != "Ghost left the lobby" {
		t.Fatalf("departure notice = %q", got[3])
	}
	time.Sleep(500 * time.Millisecond) // both lobbies' journal writes land
	entries := waitLog(t, carol, "Ghost's departure", func(got []config.LogEntry) bool {
		return count(got, config.LogKindDisconnected, "Ghost") >= 1
	})
	if n := count(entries, config.LogKindDisconnected, "Ghost"); n != 1 {
		t.Fatalf("expiry journaled %d times by two lobbies: %v", n, entries)
	}
}

// TestPresenceGhostCleared: a presence key left under our name by a session
// that never deleted it is cleared at start, so the other lobbies see that
// session leave and this one arrive.
func TestPresenceGhostCleared(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()
	kv, err := js.KeyValue(ctx, lb.kv.Bucket())
	if err != nil {
		t.Fatal(err)
	}
	ghost, _ := json.Marshal(PlayerPresence{PlayerID: "player-2", Name: "Bob", LastSeen: time.Now()})
	if _, err := kv.Put(ctx, config.LobbyPlayerKey("player-2"), ghost); err != nil {
		t.Fatal(err)
	}
	waitSystemLines(t, lb, 2)

	bob := New(js, kv, "player-2", "Bob")
	if err := bob.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bob.Stop)
	got := waitSystemLines(t, lb, 4)
	if got[2] != "Bob left the lobby" || got[3] != "Bob joined the lobby" {
		t.Fatalf("notices = %v", got)
	}
	if got := waitSystemLines(t, bob, 1); got[0] != "You joined the lobby as Bob" || len(got) != 1 {
		t.Fatalf("Bob's notices = %v", got)
	}
}

// TestAgentsUnannounced: an agent's arrival and departure are told neither
// in the chat nor in the server log.
func TestAgentsUnannounced(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()
	waitSystemLines(t, lb, 1)
	kv, err := js.KeyValue(ctx, lb.kv.Bucket())
	if err != nil {
		t.Fatal(err)
	}
	bot := New(js, kv, "golang-mk1-1", "golang-mk1-1")
	bot.SetAgent(true)
	if err := bot.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(bot.Stop)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := lb.Players()["golang-mk1-1"]; ok {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	bot.Leave(ctx)
	time.Sleep(500 * time.Millisecond)
	if got := systemLines(lb); len(got) != 1 {
		t.Fatalf("the agent was announced: %v", got)
	}
	for _, e := range lb.LogEntries() {
		if e.Agent {
			t.Fatalf("the agent was journaled: %+v", e)
		}
	}
}
