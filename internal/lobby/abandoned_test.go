package lobby

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
)

// TestAbandonedRules exercises the per-game abandonment rules by injecting a
// future "now" into isAbandoned: an unstarted invite game is abandoned 15
// minutes after creation, a started one after one minute without stream
// activity, and a started game whose stream is already gone immediately.
func TestAbandonedRules(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, InviteOnly: true, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the KV watcher deliver the listing
	g, ok := lb.Games()[gameID]
	if !ok {
		t.Fatal("game not in listing")
	}

	now := time.Now()
	if lb.isAbandoned(ctx, g, now) {
		t.Error("fresh created game flagged as abandoned")
	}
	if !lb.isAbandoned(ctx, g, now.Add(config.AbandonedUnstartedTimeout+time.Second)) {
		t.Error("created game older than the unstarted timeout not flagged")
	}

	// Started game: the create-time meta publish is the stream's last activity.
	g.Status = config.GameStatusInProgress
	if lb.isAbandoned(ctx, g, now) {
		t.Error("started game with recent stream activity flagged as abandoned")
	}
	if !lb.isAbandoned(ctx, g, now.Add(config.AbandonedIdleTimeout+time.Second)) {
		t.Error("started game idle past the timeout not flagged")
	}

	// Started game whose stream was deleted can never make progress.
	if err := natspkg.DeleteGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	if !lb.isAbandoned(ctx, g, now) {
		t.Error("started game with deleted stream not flagged")
	}

	// End to end: with the in_progress listing in KV and the stream gone, a
	// checker pass must land the game in the AbandonedGames snapshot.
	data, _ := json.Marshal(g)
	if _, err := lb.kv.Put(ctx, config.LobbyGameKey(gameID), data); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	lb.checkAbandoned(ctx)
	if !lb.AbandonedGames()[gameID] {
		t.Error("checker pass did not flag the abandoned game")
	}
}

// TestOpenGameAbandonedAfterTwoWeeks: an open game is a standing invitation —
// neither the 15-minute unstarted rule nor the one-minute silence rule
// touches it. It is abandoned only once nothing has happened to it for two
// weeks: no change to its listing (a join, a leave, a ready) and no move on
// its stream. A started open game whose stream is gone is still abandoned at
// once.
func TestOpenGameAbandonedAfterTwoWeeks(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond) // let the KV watcher deliver the listing
	g, ok := lb.Games()[gameID]
	if !ok {
		t.Fatal("game not in listing")
	}
	if g.InviteOnly {
		t.Fatal("setup: the game must be open")
	}

	now := time.Now()
	week, twoWeeks := 7*24*time.Hour, config.AbandonedOpenTimeout+time.Second
	if lb.isAbandoned(ctx, g, now.Add(config.AbandonedUnstartedTimeout+time.Second)) {
		t.Error("open game flagged by the unstarted timeout")
	}
	if lb.isAbandoned(ctx, g, now.Add(week)) {
		t.Error("open game a week old flagged")
	}
	if !lb.isAbandoned(ctx, g, now.Add(twoWeeks)) {
		t.Error("open game untouched for two weeks not flagged")
	}
	// A change to the listing — someone joining, say — is activity: the
	// clock restarts from the listing's last revision.
	lb.mu.Lock()
	lb.touched[gameID] = now.Add(week)
	lb.mu.Unlock()
	if lb.isAbandoned(ctx, g, now.Add(twoWeeks)) {
		t.Error("open game whose listing changed a week ago flagged")
	}
	if !lb.isAbandoned(ctx, g, now.Add(week+twoWeeks)) {
		t.Error("open game untouched for two weeks after its last change not flagged")
	}

	// Started: a silent stream is not desertion in an open game.
	g.Status = config.GameStatusInProgress
	if lb.isAbandoned(ctx, g, now.Add(config.AbandonedIdleTimeout+time.Second)) {
		t.Error("started open game flagged by the idle timeout")
	}
	if lb.isAbandoned(ctx, g, now.Add(week)) {
		t.Error("started open game quiet for a week flagged")
	}
	if !lb.isAbandoned(ctx, g, now.Add(week+twoWeeks)) {
		t.Error("started open game quiet for two weeks not flagged")
	}
	if err := natspkg.DeleteGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	if !lb.isAbandoned(ctx, g, now) {
		t.Error("started open game with a deleted stream not flagged")
	}
}

// TestDeleteGame verifies the full teardown of an abandoned game: the game
// stream and KV listing are removed and the game's chat messages are purged
// from the shared chat stream without touching the lobby chat.
func TestDeleteGame(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if err := lb.SendGameChat(ctx, gameID, "anyone there?", false); err != nil {
		t.Fatal(err)
	}
	if err := lb.SendChat(ctx, "lobby chat survives"); err != nil {
		t.Fatal(err)
	}

	if err := lb.DeleteGame(ctx, gameID); err != nil {
		t.Fatal(err)
	}

	if _, err := js.Stream(ctx, config.GameStream(gameID)); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Errorf("game stream still exists (err=%v)", err)
	}
	if _, err := lb.kv.Get(ctx, config.LobbyGameKey(gameID)); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("KV listing still exists (err=%v)", err)
	}
	chat, err := js.Stream(ctx, config.ChatStream)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chat.GetLastMsgForSubject(ctx, config.GameChatSubject(gameID)); err == nil {
		t.Error("game chat messages not purged")
	}
	if _, err := chat.GetLastMsgForSubject(ctx, config.LobbyChatSubject); err != nil {
		t.Errorf("lobby chat should have survived the purge: %v", err)
	}

	// Deleting a game whose stream is already gone must still succeed (the
	// checker flags such games precisely so they can be cleaned up).
	if err := lb.DeleteGame(ctx, gameID); err != nil {
		t.Errorf("re-delete of a torn-down game: %v", err)
	}
}
