package engine

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/rng"
	"jetris/internal/testutil"
)

// startSplitCoopGame brings up an in-progress three-seat cooperative game
// with the piece split on and returns one engine per seat.
func startSplitCoopGame(t *testing.T) []*Engine {
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
	gameID := "split-coop-test-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeCooperative, PlayerCount: 3,
		NextCount: config.MaxNextCount, SplitPieces: true, ExtraColumns: 4,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "p0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	var engines []*Engine
	for i := 0; i < 3; i++ {
		e := New(js, gameID, "p"+string(rune('0'+i)), "", config.ModeCooperative, ModePlayer, i, 0, 0)
		if err := e.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(e.Stop)
		engines = append(engines, e)
	}
	return engines
}

// TestCoopSplitPiecesDealsTheWholeBag: the crew's shared board splits its
// pieces like a team's — every seat holds a ration of its own, the rations
// together are the seven types, and the deal is the same one every seat
// computes (rng.PieceSets off the seed, one ration per seat).
func TestCoopSplitPiecesDealsTheWholeBag(t *testing.T) {
	engines := startSplitCoopGame(t)
	want := rng.PieceSets(42, 3)
	seen := map[game.PieceType]bool{}
	for i, e := range engines {
		if !e.SplitPieces() {
			t.Fatalf("seat %d: the game splits its pieces but the engine does not", i)
		}
		if got := e.PieceSet(); !slices.Equal(got, want[i]) {
			t.Errorf("seat %d holds %v, want %v", i, got, want[i])
		}
		for _, pt := range e.PieceSet() {
			seen[pt] = true
		}
		// What the seat previews comes only from its ration.
		for _, pt := range e.NextPieces() {
			if !slices.Contains(e.PieceSet(), pt) {
				t.Errorf("seat %d previews %v, outside its ration %v", i, pt, e.PieceSet())
			}
		}
		// Every seat can look up any seat's ration — the HUD lists them all.
		if got := e.PieceSetForSlot(2); !slices.Equal(got, want[2]) {
			t.Errorf("seat %d reads seat 2's ration as %v, want %v", i, got, want[2])
		}
	}
	if len(seen) != 7 {
		t.Errorf("the crew holds %d piece types between them, want all 7", len(seen))
	}
}

// TestRedealAmongPresentSeats: an open game's deal follows the seats
// present. With seat 1 gone the two remaining seats hold the two-way deal
// — seat 2 the second ration, by its rank among the company — seat 1 holds
// nothing, and the sequence draws from the new ration at the same piece
// index; the same roster pushed twice re-deals nothing, and the full
// roster back restores the deal at creation.
func TestRedealAmongPresentSeats(t *testing.T) {
	engines := startSplitCoopGame(t)
	e := engines[2]
	full := rng.PieceSets(42, 3)
	two := rng.PieceSets(42, 2)

	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}, {PlayerID: "p2", Seat: 2}})
	if got := e.PieceSet(); !slices.Equal(got, two[1]) {
		t.Fatalf("after the re-deal seat 2 holds %v, want the second of the two-way deal %v", got, two[1])
	}
	if got := e.PieceSetForSlot(0); !slices.Equal(got, two[0]) {
		t.Errorf("seat 0's ration = %v, want %v", got, two[0])
	}
	if got := e.PieceSetForSlot(1); got != nil {
		t.Errorf("the absent seat 1 holds %v, want nothing", got)
	}
	for _, pt := range e.NextPieces() {
		if !slices.Contains(two[1], pt) {
			t.Errorf("seat 2 previews %v, outside its new ration %v", pt, two[1])
		}
	}
	before := e.seqNow()
	e.SetRoster([]Seat{{PlayerID: "p2", Seat: 2}, {PlayerID: "p0", Seat: 0}})
	if e.seqNow() != before {
		t.Error("the same roster, in another order, re-dealt the sequence")
	}
	e.SetRoster([]Seat{{PlayerID: "p0", Seat: 0}, {PlayerID: "p1", Seat: 1}, {PlayerID: "p2", Seat: 2}})
	if got := e.PieceSet(); !slices.Equal(got, full[2]) {
		t.Errorf("the full roster back: seat 2 holds %v, want the creation deal's %v", got, full[2])
	}
	// A game that does not split ignores the roster's comings and goings.
	plain := New(nil, "g", "me", "", config.ModeCooperative, ModePlayer, 0, 0, 0)
	plain.SetRoster([]Seat{{PlayerID: "me", Seat: 0}})
	if plain.SplitPieces() || plain.PieceSet() != nil {
		t.Error("a game without the split dealt rations")
	}
}
