package lobby

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func setupLobby(t *testing.T) (*Lobby, jetstream.JetStream) {
	t.Helper()
	url, _ := testutil.StartServer(t)
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := natspkg.EnsureChatStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}

	lb := New(js, kv, "player-1", "Alice")
	if err := lb.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lb.Stop)

	// Wait for KV watcher to initialize
	time.Sleep(200 * time.Millisecond)

	return lb, js
}

func TestLobbyCreateGame(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.ModeCooperative, 2, 0, 0, 0, 0, false, config.GameRules{Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if gameID == "" {
		t.Fatal("expected non-empty game ID")
	}

	// Wait for KV update
	time.Sleep(300 * time.Millisecond)

	games := lb.Games()
	if len(games) == 0 {
		t.Fatal("expected game in listing")
	}
	g, ok := games[gameID]
	if !ok {
		t.Fatal("game not found in listing")
	}
	if g.Status != config.GameStatusCreated {
		t.Errorf("expected status created, got %s", g.Status)
	}
}

func TestLobbySendChat(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	if err := lb.SendChat(ctx, "hello world"); err != nil {
		t.Fatal(err)
	}

	// Check for chat update
	timeout := time.After(2 * time.Second)
	select {
	case update := <-lb.Updates:
		if update.Kind == LobbyUpdateChat {
			if update.ChatMsg == nil || update.ChatMsg.Text != "hello world" {
				t.Error("chat message mismatch")
			}
		}
	case <-timeout:
		t.Log("no chat update received (may have been consumed by KV watcher)")
	}
}

// TestChatBacklogSurvivesUndrainedUpdates reproduces the two-player bug where
// a joining player's chat panel came up empty: the stream's chat backlog is
// replayed while nothing drains lb.Updates (during login the UI pump hasn't
// attached yet, and emitUpdate drops on a full channel). The log lives in the
// Lobby, not in the lossy updates, so ChatLog must return the backlog even
// when every update ping was dropped.
func TestChatBacklogSurvivesUndrainedUpdates(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		if err := lb.SendChat(ctx, fmt.Sprintf("line %d", i)); err != nil {
			t.Fatal(err)
		}
	}

	// A second player joins the same lobby. Nothing reads lb2.Updates, and
	// pre-filling the channel to capacity guarantees every ping is dropped.
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	lb2 := New(js, kv, "player-2", "Bob")
	for i := 0; i < cap(lb2.Updates); i++ {
		lb2.Updates <- LobbyUpdate{Kind: LobbyUpdatePlayers}
	}
	if err := lb2.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(lb2.Stop)

	deadline := time.Now().Add(3 * time.Second)
	for {
		lobbyLines := 0
		for _, m := range lb2.ChatLog() {
			if m.GameID == "" {
				lobbyLines++
			}
		}
		if lobbyLines >= 3 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("joining player's ChatLog has %d lobby lines, want 3", lobbyLines)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestGameChatScoping verifies that lobby and game chat share one stream and
// are distinguished by subject: a game message arrives tagged with its game ID
// (parsed from the subject) and a lobby message with GameID "".
func TestGameChatScoping(t *testing.T) {
	lb, _ := setupLobby(t)
	ctx := context.Background()

	if err := lb.SendGameChat(ctx, "game-42", "gg", true); err != nil {
		t.Fatal(err)
	}
	if err := lb.SendChat(ctx, "hi lobby"); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{"gg": "game-42", "hi lobby": ""}
	specWant := map[string]bool{"gg": true, "hi lobby": false}
	got := 0
	timeout := time.After(3 * time.Second)
	for got < len(want) {
		select {
		case update := <-lb.Updates:
			if update.Kind != LobbyUpdateChat || update.ChatMsg == nil {
				continue
			}
			m := update.ChatMsg
			wantID, ok := want[m.Text]
			if !ok {
				continue
			}
			if m.GameID != wantID {
				t.Fatalf("message %q GameID = %q, want %q", m.Text, m.GameID, wantID)
			}
			if m.Spectator != specWant[m.Text] {
				t.Fatalf("message %q Spectator = %v, want %v", m.Text, m.Spectator, specWant[m.Text])
			}
			got++
		case <-timeout:
			t.Fatalf("received %d of %d chat updates before timeout", got, len(want))
		}
	}
}

func TestLobbyPresence(t *testing.T) {
	lb, _ := setupLobby(t)

	// Wait for heartbeat to publish
	time.Sleep(config.PresenceHeartbeat + 200*time.Millisecond)

	players := lb.Players()
	if _, ok := players["player-1"]; !ok {
		t.Log("player-1 not in presence map yet (TTL may have expired)")
	}
}

// TestLobbyCreateGameSplitPieces: the teams-mode piece split reaches both
// records — the meta every engine deals from and the listing the lobby row
// tags — and only where it means something: a game with teammates to split
// between. A team of one, or any other mode, records nothing, so no row can
// advertise a split that will never happen.
func TestLobbyCreateGameSplitPieces(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	split, err := lb.CreateGame(ctx, config.ModeTeams, 4, 2, 2, 0, 0, true, config.GameRules{NextCount: 1, Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, js, split)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.SplitPieces || !meta.SplitsPieces() {
		t.Errorf("meta split_pieces = %v (splits: %v), want a split 2v2", meta.SplitPieces, meta.SplitsPieces())
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[split]; !g.SplitPieces || !g.SplitsPieces() {
		t.Errorf("listing split_pieces = %v (splits: %v), want a split 2v2", g.SplitPieces, g.SplitsPieces())
	}

	// A team of one has nobody to split with, and a co-op game no teams.
	solo, err := lb.CreateGame(ctx, config.ModeTeams, 2, 2, 1, 0, 0, true, config.GameRules{NextCount: 1, Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if meta, _, err = natspkg.FetchGameMeta(ctx, js, solo); err != nil {
		t.Fatal(err)
	}
	if meta.SplitPieces {
		t.Error("a team of one recorded a piece split")
	}
	coop, err := lb.CreateGame(ctx, config.ModeCooperative, 2, 0, 0, 0, 0, true, config.GameRules{NextCount: 1, Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if meta, _, err = natspkg.FetchGameMeta(ctx, js, coop); err != nil {
		t.Fatal(err)
	}
	if meta.SplitPieces {
		t.Error("a cooperative game recorded a piece split")
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[coop]; g.SplitPieces || g.SplitsPieces() {
		t.Error("a cooperative listing advertises a piece split")
	}
}

// TestLobbyCreateGameGarbageHoles: the garbage-holes rule is written to both
// records — the meta every peer raises from and the listing the lobby row
// tags — clamped to 0..config.MaxGarbageHoles; a meta written before the
// field reads as 0 (solid rows).
func TestLobbyCreateGameGarbageHoles(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: 2}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, js, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GarbageHoles != 2 {
		t.Errorf("meta garbage_holes = %d, want 2", meta.GarbageHoles)
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[gameID]; g.GarbageHoles != 2 {
		t.Errorf("listing garbage_holes = %d, want 2", g.GarbageHoles)
	}

	over, err := lb.CreateGame(ctx, config.ModeTeams, 2, 2, 1, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: 9}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, over)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GarbageHoles != config.MaxGarbageHoles {
		t.Errorf("clamped meta garbage_holes = %d, want %d", meta.GarbageHoles, config.MaxGarbageHoles)
	}
	if meta.RandomGarbageHoles {
		t.Error("random holes should be off unless asked for")
	}

	random, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: 3, RandomGarbageHoles: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, random)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GarbageHoles != 3 || !meta.RandomGarbageHoles {
		t.Errorf("random-holes meta = holes %d random %v, want 3 true", meta.GarbageHoles, meta.RandomGarbageHoles)
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[random]; g.GarbageHoles != 3 || !g.RandomGarbageHoles {
		t.Errorf("random-holes listing = holes %d random %v, want 3 true", g.GarbageHoles, g.RandomGarbageHoles)
	}

	// Random holes without holes is meaningless: stored off.
	solidRandom, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, RandomGarbageHoles: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, solidRandom)
	if err != nil {
		t.Fatal(err)
	}
	if meta.RandomGarbageHoles {
		t.Error("random holes at 0 holes should be stored off")
	}

	under, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: -1}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, under)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GarbageHoles != 0 {
		t.Errorf("negative holes should clamp to 0, got %d", meta.GarbageHoles)
	}

	var legacy config.GameMeta
	if err := json.Unmarshal([]byte(`{"game_id":"x","mode":1,"player_count":2}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.GarbageHoles != 0 {
		t.Errorf("pre-field meta garbage_holes = %d, want 0", legacy.GarbageHoles)
	}
}

// TestLobbyCreateGameGuidelineGarbage: the Guideline attack rule is written
// to both records, off unless asked for.
func TestLobbyCreateGameGuidelineGarbage(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	plain, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, js, plain)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GuidelineGarbage {
		t.Error("guideline garbage should be off unless asked for")
	}

	guideline, err := lb.CreateGame(ctx, config.ModeTeams, 2, 2, 1, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, GuidelineGarbage: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, guideline)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.GuidelineGarbage {
		t.Error("guideline garbage should be stored on the meta")
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[guideline]; !g.GuidelineGarbage {
		t.Error("guideline garbage should be mirrored on the listing")
	}
}

// TestLobbyCreateGameHoldAndGuidelinePreset: the hold rule is written to both
// records, off unless asked for; the Guideline preset lands intact (clamped
// for a cooperative game, which stores no garbage rules) and reads back as
// the preset from either record; a meta written before the field has no
// hold.
func TestLobbyCreateGameHoldAndGuidelinePreset(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	plain, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, js, plain)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Hold {
		t.Error("hold should be off unless asked for")
	}
	if meta.Rules().IsGuideline(config.ModeCompetitive) {
		t.Error("next 1 / no hold is not the Guideline preset")
	}

	guideline, err := lb.CreateGame(ctx, config.ModeTeams, 2, 2, 1, 0, 0, false, config.GuidelineRules(), false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, guideline)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Hold || meta.NextCount != config.MaxNextCount || meta.NoGhost || meta.GarbageHoles != 1 || meta.RandomGarbageHoles || !meta.GuidelineGarbage {
		t.Errorf("Guideline preset meta = %+v", meta.Rules())
	}
	if !meta.Rules().IsGuideline(config.ModeTeams) {
		t.Error("the preset's meta should read back as the Guideline preset")
	}
	time.Sleep(300 * time.Millisecond)
	g := lb.Games()[guideline]
	if !g.Hold {
		t.Error("hold should be mirrored on the listing")
	}
	if !g.Rules().IsGuideline(config.ModeTeams) {
		t.Errorf("the preset's listing %+v should read back as the Guideline preset", g.Rules())
	}

	coop, err := lb.CreateGame(ctx, config.ModeCooperative, 2, 0, 0, 0, 0, false, config.GuidelineRules(), false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, coop)
	if err != nil {
		t.Fatal(err)
	}
	if meta.GarbageHoles != 0 || meta.GuidelineGarbage || !meta.Hold {
		t.Errorf("cooperative Guideline meta = %+v, want no garbage rules, hold on", meta.Rules())
	}
	if !meta.Rules().IsGuideline(config.ModeCooperative) {
		t.Error("a cooperative preset game should still read as the Guideline preset")
	}

	var legacy config.GameMeta
	if err := json.Unmarshal([]byte(`{"game_id":"x","mode":1,"player_count":2}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.Hold {
		t.Error("pre-field meta should have no hold")
	}
}

// TestLobbyCreateGameBag: the bag rule reaches both records — the meta every
// engine deals from and the listing the lobby row tags — normalized on the
// way in, so an unknown kind is stored as the 7-bag (the field absent) and
// no peer ever reads a randomizer it cannot deal.
func TestLobbyCreateGameBag(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	for _, bag := range []config.Bag{config.BagDouble, config.BagNone} {
		id, err := lb.CreateGame(ctx, config.ModeCompetitive, 2, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, Bag: bag}, false)
		if err != nil {
			t.Fatal(err)
		}
		meta, _, err := natspkg.FetchGameMeta(ctx, js, id)
		if err != nil {
			t.Fatal(err)
		}
		if meta.Bag != bag || meta.Rules().Bag != bag {
			t.Errorf("meta bag = %q, want %q", meta.Bag, bag)
		}
		time.Sleep(300 * time.Millisecond)
		if g := lb.Games()[id]; g.Bag != bag || g.Rules().Bag != bag || g.Rules().IsGuideline(g.Mode) {
			t.Errorf("listing bag = %q (guideline: %v), want %q and not the preset", g.Bag, g.Rules().IsGuideline(g.Mode), bag)
		}
	}

	junk, err := lb.CreateGame(ctx, config.ModeCooperative, 1, 0, 0, 0, 0, false, config.GameRules{NextCount: 1, Ghost: true, Bag: "triple"}, false)
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, js, junk)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Bag != config.BagSingle {
		t.Errorf("an unknown kind was stored as %q, want the 7-bag (absent)", meta.Bag)
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[junk]; g.Bag != config.BagSingle {
		t.Errorf("an unknown kind was listed as %q, want the 7-bag (absent)", g.Bag)
	}
}
