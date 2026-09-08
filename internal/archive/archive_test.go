package archive

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// ArchiveAndCleanup against a real server: the history record goes out
// within moments of the finish — with the rival's final score recovered from
// its game_over event (the drain completes on the first delivery that reports
// nothing pending) — while the game stream and listing survive until the
// streamDeleteGrace has passed, and the replay marker follows the record.
func TestArchiveAndCleanupRecordFirstThenGrace(t *testing.T) {
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
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}

	// A finished two-player competitive game: meta, the rival's game_over on
	// its per-kind subject, and a couple of cells for the board picture.
	const gameID = "archive-flow"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	meta, _ := json.Marshal(config.GameMeta{GameID: gameID, Mode: config.ModeCompetitive, PlayerCount: 2,
		Status: config.GameStatusFinished, StartedAt: now.Add(-time.Minute), FinishedAt: now})
	if _, err := js.Publish(ctx, config.MetaSubject(gameID), meta); err != nil {
		t.Fatal(err)
	}
	ev, _ := json.Marshal(engine.GameEvent{Kind: engine.EventGameOver, PlayerID: "rival", Score: 321, Level: 3, PieceCount: 42})
	if _, err := js.Publish(ctx, config.EventKindSubject(gameID, string(engine.EventGameOver), "rival"), ev); err != nil {
		t.Fatal(err)
	}
	cell := []byte(`{"o":true,"t":1}`)
	if _, err := js.Publish(ctx, config.CompetitiveCellSubject(gameID, "rival", config.VisibleRowStart+5, 1), cell); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, config.LobbyGameKey(gameID), []byte(`{"game_id":"archive-flow"}`)); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(js, gameID, "me", "rival", config.ModeCompetitive, engine.ModePlayer, 0, 0, 0)

	// Timestamp the record's arrival the way a lobby would see it.
	recordAt := make(chan time.Time, 1)
	var record config.ArchiveRecord
	sub, err := nc.Subscribe(config.ArchiveSubject, func(m *nats.Msg) {
		_ = json.Unmarshal(m.Data, &record)
		select {
		case recordAt <- time.Now():
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	start := time.Now()
	done := make(chan struct{})
	go func() {
		ArchiveAndCleanup(ctx, js, kv, eng, nil, []lobby.PlayerSummary{{PlayerID: "me"}, {PlayerID: "rival"}})
		close(done)
	}()

	var at time.Time
	select {
	case at = <-recordAt:
	case <-time.After(4 * time.Second):
		t.Fatal("no archive record within 4s of the finish")
	}
	if d := at.Sub(start); d > 2*time.Second {
		t.Errorf("record took %v — it must not wait for the grace period", d)
	}
	// The record carries the rival's drained result. (Verdicts come from the
	// archiving engine's live elimination record, which this never-started
	// engine never built — so only the drained figures are asserted here.)
	scores := map[string]config.PlayerResult{}
	for _, p := range record.Players {
		scores[p.PlayerID] = p
	}
	if r := scores["rival"]; r.Score != 321 || r.Level != 3 || r.PieceCount != 42 {
		t.Errorf("rival result = %+v, want the drained game_over (321/3/42)", r)
	}
	if _, ok := scores["me"]; !ok {
		t.Error("the archiver itself must be in the record")
	}
	if len(record.Boards) != 2 {
		t.Errorf("record has %d boards, want 2 (one per competitive player)", len(record.Boards))
	}
	// The record says how tall the boards were — a competitive board is the
	// standard one — and carries the game's length and scoring (absent here).
	if record.BoardRows != config.StandardHeight || record.BoardHeight() != config.StandardHeight {
		t.Errorf("record board_rows = %d, want %d", record.BoardRows, config.StandardHeight)
	}
	if record.ExtraRows != 0 || record.LineGoal != 0 || record.Scoring != config.ScoringShared {
		t.Errorf("record carries settings the meta never had: %+v", record)
	}
	// At record time the game is still fully there for every other peer.
	if _, err := js.Stream(ctx, config.GameStream(gameID)); err != nil {
		t.Errorf("game stream already gone when the record was published: %v", err)
	}
	if _, err := kv.Get(ctx, config.LobbyGameKey(gameID)); err != nil {
		t.Errorf("lobby listing already gone when the record was published: %v", err)
	}

	// The replay copy follows the record (every finished game is "recent").
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, _, err := natspkg.GetReplayMarker(ctx, js, gameID); err == nil {
			break
		} else if !errors.Is(err, jetstream.ErrMsgNotFound) && !errors.Is(err, jetstream.ErrStreamNotFound) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("replay marker never appeared")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Teardown only after the grace: the stream and listing vanish no sooner
	// than streamDeleteGrace after the finish.
	select {
	case <-done:
	case <-time.After(streamDeleteGrace + 5*time.Second):
		t.Fatal("ArchiveAndCleanup never returned")
	}
	if d := time.Since(start); d < streamDeleteGrace {
		t.Errorf("cleanup finished after %v, before the %v grace", d, streamDeleteGrace)
	}
	if _, err := js.Stream(ctx, config.GameStream(gameID)); !errors.Is(err, jetstream.ErrStreamNotFound) {
		t.Errorf("game stream should be deleted after the grace, got %v", err)
	}
	if _, err := kv.Get(ctx, config.LobbyGameKey(gameID)); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("lobby listing should be deleted after the grace, got %v", err)
	}
}

// A board scored per seat archives its verdict: the top scorer wins, every
// seat carries its own totals — a survivor's from the line_clear events the
// archiving engine folded — and the record carries the scoring and no
// shared total, so it ranks in its own bucket.
func TestArchiveIndividualWinner(t *testing.T) {
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
	if err := natspkg.EnsureArchiveStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	kv, err := natspkg.EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}

	// A two-seat board scored per seat, in progress: the rival announced
	// 500 points, then topped out with them; we never announced a thing.
	const gameID = "archive-individual"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	meta := config.GameSpec{Mode: config.ModeCooperative, PlayerCount: 2, Scoring: config.ScoringIndividual, Rules: config.GameRules{Ghost: true}}.
		Normalized().Meta(gameID, "me", 7, now.Add(-time.Minute))
	meta.Status, meta.StartedAt = config.GameStatusInProgress, now.Add(-time.Minute)
	metaData, _ := json.Marshal(meta)
	if _, err := js.Publish(ctx, config.MetaSubject(gameID), metaData); err != nil {
		t.Fatal(err)
	}
	clear, _ := json.Marshal(engine.GameEvent{Kind: engine.EventLineClear, PlayerID: "rival", Score: 500, LinesCleared: 2, TotalScore: 500, TotalLines: 2})
	if _, err := js.Publish(ctx, config.EventKindSubject(gameID, string(engine.EventLineClear), "rival"), clear); err != nil {
		t.Fatal(err)
	}
	over, _ := json.Marshal(engine.GameEvent{Kind: engine.EventGameOver, PlayerID: "rival", Score: 500, Level: 0, PieceCount: 9, TotalScore: 500, TotalLines: 2})
	if _, err := js.Publish(ctx, config.EventKindSubject(gameID, string(engine.EventGameOver), "rival"), over); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Put(ctx, config.LobbyGameKey(gameID), []byte(`{"game_id":"archive-individual"}`)); err != nil {
		t.Fatal(err)
	}

	// Our engine lives through it: it folds the rival's events and decides
	// the verdict from them.
	eng := engine.New(js, gameID, "me", "", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	if err := eng.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Stop)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, decided := eng.Winners(); decided {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the engine never decided the game from the rival's game over")
		}
		time.Sleep(20 * time.Millisecond)
	}

	var record config.ArchiveRecord
	got := make(chan struct{}, 1)
	sub, err := nc.Subscribe(config.ArchiveSubject, func(m *nats.Msg) {
		_ = json.Unmarshal(m.Data, &record)
		select {
		case got <- struct{}{}:
		default:
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Unsubscribe() }()
	// The finish the topper would have made.
	meta.Status, meta.FinishedAt = config.GameStatusFinished, now
	metaData, _ = json.Marshal(meta)
	if _, err := js.Publish(ctx, config.MetaSubject(gameID), metaData); err != nil {
		t.Fatal(err)
	}
	go ArchiveAndCleanup(ctx, js, kv, eng, nil, []lobby.PlayerSummary{{PlayerID: "me"}, {PlayerID: "rival"}})
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no archive record within 5s")
	}

	if record.Scoring != config.ScoringIndividual || !record.IndividualScoring() || record.TotalScore != 0 {
		t.Errorf("record scoring = %q, total %d; want individual and no shared total", record.Scoring, record.TotalScore)
	}
	results := map[string]config.PlayerResult{}
	for _, p := range record.Players {
		results[p.PlayerID] = p
	}
	if r := results["rival"]; !r.Winner || r.Score != 500 || r.Lines != 2 {
		t.Errorf("rival = %+v, want the winner with 500 points and 2 lines", r)
	}
	if r := results["me"]; r.Winner || r.Score != 0 {
		t.Errorf("me = %+v, want beaten at 0", r)
	}
	if record.HeadlineScore() != 500 {
		t.Errorf("headline = %d, want the best seat's 500", record.HeadlineScore())
	}
}
