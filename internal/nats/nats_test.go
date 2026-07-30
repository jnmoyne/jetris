package nats

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetricks/internal/config"
	"jetricks/internal/game"
	"jetricks/internal/testutil"
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
	return js
}

func TestEnsureGameStream(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-game-1"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	// Verify stream exists
	s, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	info := s.CachedInfo()
	if !info.Config.AllowAtomicPublish {
		t.Error("AllowAtomicPublish should be true")
	}
	if !info.Config.AllowDirect {
		t.Error("AllowDirect should be true")
	}
}

func TestEnsureChatStream(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	if err := EnsureChatStream(ctx, js); err != nil {
		t.Fatal(err)
	}
	s, err := js.Stream(ctx, config.ChatStream)
	if err != nil {
		t.Fatal(err)
	}
	info := s.CachedInfo()
	if info.Config.MaxAge != config.ChatMaxAge {
		t.Errorf("MaxAge = %v, want %v", info.Config.MaxAge, config.ChatMaxAge)
	}
}

func TestEnsureLobbyKV(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	kv, err := EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	// Put and verify expiry
	_, err = kv.Put(ctx, "test-key", []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	entry, err := kv.Get(ctx, "test-key")
	if err != nil {
		t.Fatal(err)
	}
	if string(entry.Value()) != "value" {
		t.Error("KV value mismatch")
	}
}

// TestLobbyPresenceTTL verifies the lobby bucket has per-key TTL enabled and
// that a presence-style write with a per-message TTL actually self-expires
// (server-side removal), while a plain Put keeps its value.
func TestLobbyPresenceTTL(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	kv, err := EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}

	// Per-key TTL / delete markers must be enabled on the bucket.
	st, err := kv.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.LimitMarkerTTL() != config.PresenceTTL {
		t.Errorf("LimitMarkerTTL = %v, want %v", st.LimitMarkerTTL(), config.PresenceTTL)
	}

	// A plain (game/invite-style) key has no TTL and must persist.
	if _, err := kv.Put(ctx, "games.persist", []byte("{}")); err != nil {
		t.Fatal(err)
	}

	// Write a presence key straight to its KV subject with a short TTL (the
	// same mechanism PutLobbyPresence uses, just a testable duration) and
	// confirm it expires while the plain key survives.
	subj := lobbyKVSubject("players.ttltest")
	if _, err := js.Publish(ctx, subj, []byte("{}"), jetstream.WithMsgTTL(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Get(ctx, "players.ttltest"); err != nil {
		t.Fatalf("presence key should exist right after write: %v", err)
	}

	time.Sleep(2 * time.Second) // TTL (1s) + margin for the expiry sweep
	if _, err := kv.Get(ctx, "players.ttltest"); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Errorf("presence key should have expired, got err=%v", err)
	}
	if _, err := kv.Get(ctx, "games.persist"); err != nil {
		t.Errorf("plain key should NOT expire: %v", err)
	}
}

// TestPutLobbyPresence writes a presence value with the production TTL and
// confirms it reads back through the KV layer as a normal value.
func TestPutLobbyPresence(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	kv, err := EnsureLobbyKV(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	if err := PutLobbyPresence(ctx, js, "players.alice", []byte(`{"name":"alice"}`)); err != nil {
		t.Fatal(err)
	}
	entry, err := kv.Get(ctx, "players.alice")
	if err != nil {
		t.Fatal(err)
	}
	if string(entry.Value()) != `{"name":"alice"}` {
		t.Errorf("value mismatch: %q", entry.Value())
	}
}

func TestPublishMetaCAS(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-meta-cas"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	meta := config.GameMeta{GameID: gameID, Status: config.GameStatusCreated}
	data, _ := json.Marshal(meta)

	// First publish with seq 0
	if err := PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	// Second publish with wrong seq should fail
	if err := PublishMeta(ctx, js, gameID, data, 0); !errors.Is(err, ErrCASFailure) {
		t.Errorf("expected ErrCASFailure, got %v", err)
	}
}

func TestFetchGameMeta(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-fetch-meta"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	meta := config.GameMeta{GameID: gameID, Status: config.GameStatusCreated, Seed: 12345}
	data, _ := json.Marshal(meta)
	if err := PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}

	got, seq, err := FetchGameMeta(ctx, js, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if got.GameID != gameID || got.Seed != 12345 {
		t.Errorf("meta mismatch: %+v", got)
	}
	if seq == 0 {
		t.Error("expected non-zero sequence")
	}
}

func TestPublishAndFetchCells(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-cells"
	playerID := "test-player"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Publish a few cells individually
	for i := 0; i < 3; i++ {
		data, _ := game.Cell{Occupied: true, PieceType: game.PieceT}.Marshal()
		_, err := PublishCellsAtomicallyNoCAS(ctx, js, []CellUpdate{{
			Subject: config.CompetitiveCellSubject(gameID, playerID, i, 4),
			Payload: data,
		}})
		if err != nil {
			t.Fatalf("cell %d: %v", i, err)
		}
	}

	// Fetch playfield state — include a never-written subject, which should
	// simply be absent from the result.
	subjects := []string{
		config.CompetitiveCellSubject(gameID, playerID, 0, 4),
		config.CompetitiveCellSubject(gameID, playerID, 1, 4),
		config.CompetitiveCellSubject(gameID, playerID, 2, 4),
		config.CompetitiveCellSubject(gameID, playerID, 3, 4),
	}
	cells, err := FetchPlayfieldState(ctx, js, gameID, subjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != 3 {
		t.Fatalf("expected 3 cells, got %d", len(cells))
	}
	for _, c := range cells {
		if c.Seq == 0 {
			t.Error("expected non-zero sequence")
		}
		if c.Col != 4 {
			t.Errorf("expected col 4, got %d", c.Col)
		}
	}
}

func TestFetchPlayfieldStateChunked(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-chunked"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// A 20x30 board = 600 subjects, above fetchChunkSize, so the fetch is
	// split into bounded chunks. Write one cell in each chunk's range.
	const width, height = 20, 30
	occupied := map[[2]int]bool{{0, 0}: true, {15, 5}: true, {29, 19}: true}
	for pos := range occupied {
		data, _ := game.Cell{Occupied: true, PieceType: game.PieceL}.Marshal()
		if _, err := PublishCellsAtomicallyNoCAS(ctx, js, []CellUpdate{{
			Subject: config.CoopCellSubject(gameID, pos[0], pos[1]),
			Payload: data,
		}}); err != nil {
			t.Fatal(err)
		}
	}

	subjects := make([]string, 0, width*height)
	for r := 0; r < height; r++ {
		for c := 0; c < width; c++ {
			subjects = append(subjects, config.CoopCellSubject(gameID, r, c))
		}
	}
	cells, err := FetchPlayfieldState(ctx, js, gameID, subjects)
	if err != nil {
		t.Fatal(err)
	}
	if len(cells) != len(occupied) {
		t.Fatalf("expected %d cells, got %d", len(occupied), len(cells))
	}
	for _, c := range cells {
		if !occupied[[2]int{c.Row, c.Col}] {
			t.Errorf("unexpected cell (%d,%d)", c.Row, c.Col)
		}
	}
}

func TestParseCellFromSubject(t *testing.T) {
	row, col := ParseCellFromSubject(config.CoopCellSubject("g1", 12, 7))
	if row != 12 || col != 7 {
		t.Errorf("coop: got (%d,%d), want (12,7)", row, col)
	}
	row, col = ParseCellFromSubject(config.CompetitiveCellSubject("g1", "p1", 3, 0))
	if row != 3 || col != 0 {
		t.Errorf("competitive: got (%d,%d), want (3,0)", row, col)
	}
	if row, col = ParseCellFromSubject("jetricks.game.g1.meta"); row != -1 || col != -1 {
		t.Errorf("non-cell subject: got (%d,%d), want (-1,-1)", row, col)
	}
}

func TestOrderedConsumer(t *testing.T) {
	js := setupJS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	gameID := "test-consumer"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Start consumer
	ch, consCancel, err := NewOrderedConsumer(ctx, js, OrderedConsumerConfig{
		Stream:        config.GameStream(gameID),
		FilterSubject: config.GameSubjectFilter(gameID),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer consCancel()

	// Publish a message
	data, _ := game.Cell{Occupied: true, PieceType: game.PieceT}.Marshal()
	_, err = js.Publish(ctx, config.CompetitiveCellSubject(gameID, "test-player", 0, 0), data)
	if err != nil {
		t.Fatal(err)
	}

	// Read from consumer
	select {
	case msg := <-ch:
		if msg == nil {
			t.Fatal("received nil message")
		}
		if msg.Subject() != config.CompetitiveCellSubject(gameID, "test-player", 0, 0) {
			t.Errorf("unexpected subject: %s", msg.Subject())
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for message")
	}
}

func TestSealGameStream(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-seal"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Publish one message first
	_, err := js.Publish(ctx, config.MetaSubject(gameID), []byte("{}"))
	if err != nil {
		t.Fatal(err)
	}

	if err := SealGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	// Further publishes should fail
	_, err = js.Publish(ctx, config.MetaSubject(gameID), []byte("{}"))
	if err == nil {
		t.Error("expected publish to sealed stream to fail")
	}
}

func TestListGameStreams(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()

	// Create a couple game streams
	EnsureGameStream(ctx, js, "game-a")
	EnsureGameStream(ctx, js, "game-b")
	// Also create the lobby chat stream (should not appear)
	EnsureChatStream(ctx, js)

	names, err := ListGameStreams(ctx, js)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("expected 2 game streams, got %d: %v", len(names), names)
	}
}
