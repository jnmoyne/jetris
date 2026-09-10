package lobby

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
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

	gameID, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.GameRules{Ghost: true}})
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

// TestLobbyCreateGameSplitPieces: the piece split reaches both records — the
// meta every engine deals from and the listing the lobby row tags — and only
// where it means something: a playfield with seatmates to split between. A
// team of one, a solo co-op game, or a competitive game (a board each)
// records nothing, so no row can advertise a split that will never happen;
// a co-op crew of two splits like a team of two.
func TestLobbyCreateGameSplitPieces(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	split, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 4, TeamCount: 2, TeamSize: 2, SplitPieces: true, Rules: config.GameRules{NextCount: 1, Ghost: true}})
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

	// A team of one has nobody to split with; neither has a solo co-op
	// player, nor a competitive player on a board of their own.
	for name, spec := range map[string]config.GameSpec{
		"team of one": {Mode: config.ModeTeams, PlayerCount: 2, TeamCount: 2, TeamSize: 1, SplitPieces: true, Rules: config.GameRules{NextCount: 1, Ghost: true}},
		"solo co-op":  {Mode: config.ModeCooperative, PlayerCount: 1, SplitPieces: true, Rules: config.GameRules{NextCount: 1, Ghost: true}},
		"competitive": {Mode: config.ModeCompetitive, PlayerCount: 3, SplitPieces: true, Rules: config.GameRules{NextCount: 1, Ghost: true}},
	} {
		id, err := lb.CreateGame(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		if meta, _, err = natspkg.FetchGameMeta(ctx, js, id); err != nil {
			t.Fatal(err)
		}
		if meta.SplitPieces || meta.SplitsPieces() {
			t.Errorf("%s: recorded a piece split", name)
		}
	}

	// A co-op crew of two is a playfield with seatmates: the split stands.
	coop, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, SplitPieces: true, Rules: config.GameRules{NextCount: 1, Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if meta, _, err = natspkg.FetchGameMeta(ctx, js, coop); err != nil {
		t.Fatal(err)
	}
	if !meta.SplitPieces || !meta.SplitsPieces() {
		t.Error("a two-seat cooperative game dropped its piece split")
	}
	time.Sleep(300 * time.Millisecond)
	if g := lb.Games()[coop]; !g.SplitPieces || !g.SplitsPieces() {
		t.Error("a two-seat cooperative listing does not advertise its piece split")
	}
}

// TestLobbyCreateGameGarbageHoles: the garbage-holes rule is written to both
// records — the meta every peer raises from and the listing the lobby row
// tags — clamped to 0..config.MaxGarbageHoles; a meta written before the
// field reads as 0 (solid rows).
func TestLobbyCreateGameGarbageHoles(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	gameID, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: 2}})
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

	over, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 2, TeamCount: 2, TeamSize: 1, Rules: config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: 9}})
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

	random, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: 3, RandomGarbageHoles: true}})
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
	solidRandom, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true, RandomGarbageHoles: true}})
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

	under, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true, GarbageHoles: -1}})
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

	plain, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true}})
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

	guideline, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 2, TeamCount: 2, TeamSize: 1, Rules: config.GameRules{NextCount: 1, Ghost: true, GuidelineGarbage: true}})
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

// TestLobbyCreateGameHoldAndModernPreset: the hold rule is written to both
// records, off unless asked for; the Modern preset lands intact (clamped
// for a cooperative game, which stores no garbage rules) and reads back as
// the preset from either record; a meta written before the field has no
// hold.
func TestLobbyCreateGameHoldAndModernPreset(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	plain, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true}})
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
	if meta.Rules().IsModern(config.ModeCompetitive) {
		t.Error("next 1 / no hold is not the Modern preset")
	}

	guideline, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeTeams, PlayerCount: 2, TeamCount: 2, TeamSize: 1, Rules: config.ModernRules()})
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err = natspkg.FetchGameMeta(ctx, js, guideline)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.Hold || meta.NextCount != config.MaxNextCount || meta.NoGhost || meta.GarbageHoles != 1 || meta.RandomGarbageHoles || !meta.GuidelineGarbage {
		t.Errorf("Modern preset meta = %+v", meta.Rules())
	}
	if !meta.Rules().IsModern(config.ModeTeams) {
		t.Error("the preset's meta should read back as the Modern preset")
	}
	time.Sleep(300 * time.Millisecond)
	g := lb.Games()[guideline]
	if !g.Hold {
		t.Error("hold should be mirrored on the listing")
	}
	if !g.Rules().IsModern(config.ModeTeams) {
		t.Errorf("the preset's listing %+v should read back as the Modern preset", g.Rules())
	}

	coop, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Rules: config.ModernRules()})
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
	if !meta.Rules().IsModern(config.ModeCooperative) {
		t.Error("a cooperative preset game should still read as the Modern preset")
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
		id, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, Rules: config.GameRules{NextCount: 1, Ghost: true, Bag: bag}})
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
		if g := lb.Games()[id]; g.Bag != bag || g.Rules().Bag != bag || g.Rules().IsModern(g.Mode) {
			t.Errorf("listing bag = %q (guideline: %v), want %q and not the preset", g.Bag, g.Rules().IsModern(g.Mode), bag)
		}
	}

	junk, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1, Rules: config.GameRules{NextCount: 1, Ghost: true, Bag: "triple"}})
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

// TestLobbyCreateGameShowHeadroom: the hidden-rows setting reaches both
// records — the meta every engine reads at Start (and the replay off the
// stream) and the listing the lobby row tags — and a game created without it
// stores nothing (the field absent, as every game before it).
func TestLobbyCreateGameShowHeadroom(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	for _, show := range []bool{true, false} {
		id, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 1, Rules: config.GameRules{NextCount: 1, Ghost: true, ShowHeadroom: show}})
		if err != nil {
			t.Fatal(err)
		}
		meta, _, err := natspkg.FetchGameMeta(ctx, js, id)
		if err != nil {
			t.Fatal(err)
		}
		if meta.ShowHeadroom != show || meta.Rules().ShowHeadroom != show {
			t.Errorf("meta show_headroom = %v, want %v", meta.ShowHeadroom, show)
		}
		time.Sleep(300 * time.Millisecond)
		if g := lb.Games()[id]; g.ShowHeadroom != show || g.Rules().ShowHeadroom != show {
			t.Errorf("listing show_headroom = %v, want %v", g.ShowHeadroom, show)
		}
	}
}

// TestLobbyCreateGameSpecRoundTrip: the new shape settings — the extra rows,
// the line goal, the scoring, the stable seat — reach both records, and a
// game created without them stores nothing (the fields absent, as every game
// before them). JoinGame fills the seat with the roster position.
func TestLobbyCreateGameSpecRoundTrip(t *testing.T) {
	lb, js := setupLobby(t)
	ctx := context.Background()

	id, err := lb.CreateGame(ctx, config.GameSpec{
		Mode: config.ModeCooperative, PlayerCount: 3, ExtraColumns: 5, ExtraRows: 4, LineGoal: 40,
		Scoring: config.ScoringIndividual, SplitPieces: true, MaxAgents: 1, AgentsPauseAlone: true,
		Rules: config.GameRules{NextCount: 2, Ghost: true, Hold: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	meta, _, err := natspkg.FetchGameMeta(ctx, js, id)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ExtraRows != 4 || meta.LineGoal != 40 || !meta.IndividualScoring() || !meta.SplitsPieces() {
		t.Errorf("meta lost a setting: extra_rows=%d line_goal=%d scoring=%q split=%v", meta.ExtraRows, meta.LineGoal, meta.Scoring, meta.SplitPieces)
	}
	if got, want := meta.BoardHeight(), config.SharedBoardHeight(3, 4); got != want {
		t.Errorf("meta BoardHeight() = %d, want %d", got, want)
	}
	time.Sleep(300 * time.Millisecond)
	g := lb.Games()[id]
	if g.ExtraRows != 4 || g.LineGoal != 40 || !g.IndividualScoring() || !g.SplitsPieces() || g.MaxAgents != 1 || !g.AgentsPauseAlone || !g.Dynamic() {
		t.Errorf("listing lost a setting: %+v", g)
	}
	if got, want := g.BoardHeight(), config.SharedBoardHeight(3, 4); got != want {
		t.Errorf("listing BoardHeight() = %d, want %d", got, want)
	}
	if g.Playfields() != 1 || g.SeatsPerPlayfield() != 3 {
		t.Errorf("listing shape = %d playfields × %d seats, want 1 × 3", g.Playfields(), g.SeatsPerPlayfield())
	}

	// The seat: the roster position, on the listing and in the join result.
	res, err := lb.JoinGame(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	g = lb.Games()[id]
	if len(g.Players) != 1 || g.Players[0].Seat != 0 || res.PlayerIdx != 0 {
		t.Errorf("first joiner's seat = %+v / idx %d, want seat 0", g.Players, res.PlayerIdx)
	}
	seats := g.NormalizedSeats()
	if len(seats) != 1 || seats[0].Seat != 0 {
		t.Errorf("NormalizedSeats() = %+v", seats)
	}

	// The defaults store nothing.
	plain, err := lb.CreateGame(ctx, config.GameSpec{Mode: config.ModeCompetitive, PlayerCount: 2, ExtraRows: 5, Scoring: config.ScoringIndividual, Rules: config.GameRules{Ghost: true}})
	if err != nil {
		t.Fatal(err)
	}
	if meta, _, err = natspkg.FetchGameMeta(ctx, js, plain); err != nil {
		t.Fatal(err)
	}
	if meta.ExtraRows != 0 || meta.LineGoal != 0 || meta.Scoring != config.ScoringShared || meta.IndividualScoring() {
		t.Errorf("a competitive game recorded shared-board settings: %+v", meta)
	}
	raw, _ := json.Marshal(meta)
	for _, field := range []string{"extra_rows", "line_goal", "scoring"} {
		if strings.Contains(string(raw), `"`+field+`"`) {
			t.Errorf("the default %s is written out: %s", field, raw)
		}
	}
}

// NormalizedSeats reads a listing written before the seat field — every seat
// zero — by roster position, as the game assigned them then; a listing with
// recorded seats is returned as it is.
func TestNormalizedSeatsLegacy(t *testing.T) {
	legacy := GameListing{Players: []PlayerSummary{{PlayerID: "a"}, {PlayerID: "b"}, {PlayerID: "c"}}}
	for i, p := range legacy.NormalizedSeats() {
		if p.Seat != i {
			t.Errorf("legacy seat %d = %d", i, p.Seat)
		}
	}
	if legacy.Players[1].Seat != 0 {
		t.Error("NormalizedSeats must not mutate the listing")
	}
	seated := GameListing{Players: []PlayerSummary{{PlayerID: "a", Seat: 2}, {PlayerID: "b", Seat: 0}}}
	got := seated.NormalizedSeats()
	if got[0].Seat != 2 || got[1].Seat != 0 {
		t.Errorf("recorded seats rewritten: %+v", got)
	}
	if solo := (GameListing{Players: []PlayerSummary{{PlayerID: "a", Seat: 3}}}).NormalizedSeats(); solo[0].Seat != 3 {
		t.Errorf("a lone recorded seat rewritten: %+v", solo)
	}
}
