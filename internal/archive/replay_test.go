package archive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

func setupJS(t *testing.T) jetstream.JetStream {
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
	if err := natspkg.EnsureArchiveStream(context.Background(), js); err != nil {
		t.Fatal(err)
	}
	return js
}

// publishGameStream creates a small finished game stream: a meta write and two
// cell writes cellGap apart (the gap is what the replay pacing test measures).
func publishGameStream(t *testing.T, js jetstream.JetStream, gameID string, cellGap time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(config.GameMeta{GameID: gameID, Status: config.GameStatusFinished})
	if _, err := js.Publish(ctx, config.MetaSubject(gameID), meta); err != nil {
		t.Fatal(err)
	}
	cell := []byte(`{"o":true,"t":1}`)
	if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, 5, 1), cell); err != nil {
		t.Fatal(err)
	}
	if cellGap > 0 {
		time.Sleep(cellGap)
	}
	if _, err := js.Publish(ctx, config.CoopCellSubject(gameID, 5, 2), cell); err != nil {
		t.Fatal(err)
	}
}

// testRecord builds a one-player archive record with the given headline score.
func testRecord(gameID string, mode config.GameMode, score int, agent bool) config.ArchiveRecord {
	now := time.Now()
	r := config.ArchiveRecord{
		GameID:      gameID,
		Mode:        mode,
		PlayerCount: 1,
		StartedAt:   now.Add(-time.Minute),
		FinishedAt:  now,
		WinningTeam: -1,
		Players:     []config.PlayerResult{{PlayerID: "p", Score: score, Agent: agent}},
	}
	if mode == config.ModeCooperative {
		r.TotalScore = score
	}
	return r
}

// archiveTestGame runs the game through the replay decision the way the real
// archiver does: build the stream, decide/copy, publish the record, delete the
// game stream.
func archiveTestGame(t *testing.T, js jetstream.JetStream, rec config.ArchiveRecord) {
	t.Helper()
	ctx := context.Background()
	publishGameStream(t, js, rec.GameID, 0)
	maybeArchiveReplay(ctx, js, rec)
	data, _ := json.Marshal(rec)
	if _, err := js.Publish(ctx, config.ArchiveSubject, data); err != nil {
		t.Fatal(err)
	}
	if err := natspkg.DeleteGameStream(ctx, js, rec.GameID); err != nil {
		t.Fatal(err)
	}
}

// hasReplay reports whether the game's copy-complete marker is present on the
// shared replay stream.
func hasReplay(t *testing.T, js jetstream.JetStream, gameID string) bool {
	t.Helper()
	_, err := natspkg.GetReplayMarker(context.Background(), js, gameID)
	if err == nil {
		return true
	}
	if errors.Is(err, jetstream.ErrMsgNotFound) || errors.Is(err, jetstream.ErrStreamNotFound) {
		return false
	}
	t.Fatal(err)
	return false
}

// The replay archiver keeps exactly the bucket's top config.ReplayTopN games:
// a finishing game below the cut gets no replay, one inside the cut gets a
// full copy and evicts the replay of the game it displaces — and buckets are
// scoped per (mode, with/without agents), so a low score in a fresh bucket
// still qualifies.
func TestReplayTopNAndDisplacement(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()

	// Fill the coop/human bucket with ReplayTopN games, scores 100..1000.
	for i := 1; i <= config.ReplayTopN; i++ {
		rec := testRecord(fmt.Sprintf("coop-%02d", i), config.ModeCooperative, i*100, false)
		archiveTestGame(t, js, rec)
		if !hasReplay(t, js, rec.GameID) {
			t.Fatalf("game %s should have a replay (bucket not full yet)", rec.GameID)
		}
	}

	// A game below the cut: no replay, and every existing replay survives.
	archiveTestGame(t, js, testRecord("coop-low", config.ModeCooperative, 50, false))
	if hasReplay(t, js, "coop-low") {
		t.Fatal("a game below the top-N cut must not get a replay")
	}
	if !hasReplay(t, js, "coop-01") {
		t.Fatal("an unqualified game must not displace anything")
	}

	// A game inside the cut: gets a replay and evicts the now-#11 (score 100).
	archiveTestGame(t, js, testRecord("coop-mid", config.ModeCooperative, 550, false))
	if !hasReplay(t, js, "coop-mid") {
		t.Fatal("a game inside the top-N cut should get a replay")
	}
	if hasReplay(t, js, "coop-01") {
		t.Fatal("the displaced game's replay should have been deleted")
	}
	if !hasReplay(t, js, "coop-02") {
		t.Fatal("games still inside the top N must keep their replays")
	}

	// Fresh buckets: a different mode, and the same mode with agents, both
	// qualify regardless of the full coop/human bucket.
	archiveTestGame(t, js, testRecord("comp-1", config.ModeCompetitive, 10, false))
	if !hasReplay(t, js, "comp-1") {
		t.Fatal("a different mode is a different bucket and should qualify")
	}
	archiveTestGame(t, js, testRecord("coop-agent", config.ModeCooperative, 10, true))
	if !hasReplay(t, js, "coop-agent") {
		t.Fatal("an agent game is a different bucket and should qualify")
	}

	// The listing names exactly the games whose copy-complete marker exists.
	ids, err := natspkg.ListReplayGameIDs(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	want := config.ReplayTopN + 2 // coop bucket still holds N, plus comp-1 and coop-agent
	if len(ids) != want {
		t.Fatalf("listed %d replays (%v), want %d", len(ids), ids, want)
	}
}

// A replay is the game's whole stream copied into the shared replay stream
// under its "jetris.replay.<id>." prefix: subjects remapped, payloads
// verbatim, and — the property original-speed playback rests on — every
// copy carrying its ORIGINAL stream timestamp in the config.ReplayTsHeader
// header. Purging the prefix removes the whole replay, marker included.
func TestReplayCopyPreservesContentAndTiming(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	const gameID = "timing-game"
	const gap = 500 * time.Millisecond

	publishGameStream(t, js, gameID, gap)
	origin, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	originMsgs := origin.CachedInfo().State.Msgs

	rec := testRecord(gameID, config.ModeCooperative, 42, false)
	maybeArchiveReplay(ctx, js, rec)
	if err := natspkg.DeleteGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Drain the game's replay prefix and check the copy against the origin.
	ch, cancel, err := natspkg.NewOrderedConsumer(ctx, js, natspkg.OrderedConsumerConfig{
		Stream:        config.ReplayStream,
		FilterSubject: config.ReplayFilter(gameID),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	var copies []jetstream.Msg
	var stamps []time.Time
	sawMarker := false
	for !sawMarker {
		select {
		case msg := <-ch:
			if msg.Subject() == config.ReplayMarkerSubject(gameID) {
				sawMarker = true
				continue
			}
			copies = append(copies, msg)
			ns, err := strconv.ParseInt(msg.Headers().Get(config.ReplayTsHeader), 10, 64)
			if err != nil {
				t.Fatalf("copy %s has no %s header: %v", msg.Subject(), config.ReplayTsHeader, err)
			}
			stamps = append(stamps, time.Unix(0, ns))
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out draining the replay (got %d msgs, marker %v)", len(copies), sawMarker)
		}
	}
	if uint64(len(copies)) != originMsgs {
		t.Errorf("replay holds %d copies, origin had %d msgs", len(copies), originMsgs)
	}
	// The two cell writes were recorded `gap` apart; their preserved original
	// timestamps (the last two copies) must keep that spacing.
	if n := len(stamps); n >= 2 {
		if d := stamps[n-1].Sub(stamps[n-2]); d < gap-100*time.Millisecond {
			t.Errorf("preserved timestamps %v apart, want ≈%v", d, gap)
		}
	}
	// Subjects are remapped under the game's replay prefix, tails intact.
	wantLast := config.ReplayCopySubject(gameID, config.CoopCellSubject(gameID, 5, 2))
	if got := copies[len(copies)-1].Subject(); got != wantLast {
		t.Errorf("last copy subject = %s, want %s", got, wantLast)
	}

	// Purge-by-prefix removes the whole replay, marker included.
	if err := natspkg.PurgeReplay(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	if hasReplay(t, js, gameID) {
		t.Fatal("purging the game's prefix should remove its marker")
	}
	if ids, err := natspkg.ListReplayGameIDs(ctx, js); err != nil || len(ids) != 0 {
		t.Fatalf("after purge, listing = %v (err %v), want empty", ids, err)
	}
}
