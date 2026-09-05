package engine

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
	"jetris/internal/testutil"
)

// The teams-mode piece split (GameMeta.SplitPieces, gameplays §5): the seven
// types are dealt out between the teammates and every seat draws only from
// its own ration, so the team has the whole bag only between them. These
// tests cover the engine's half of it — the deal each seat reads out of the
// meta, and the sequence that deal restricts.

// startSplitTeamsGame brings up an in-progress 2v2 teams game with the split
// on and returns an engine per seat: team A slots 0 and 1, then team B slot 0
// (the mirror of A's slot 0, which must hold the same ration).
func startSplitTeamsGame(t *testing.T, split bool) (a0, a1, b0 *Engine) {
	t.Helper()
	return startTeamsGame(t, split, config.BagSingle)
}

// startTeamsGame is startSplitTeamsGame with the game's bag rule chosen too
// (bag_test.go deals the seats with the double bag and with no bag).
func startTeamsGame(t *testing.T, split bool, bag config.Bag) (a0, a1, b0 *Engine) {
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
	gameID := "split-pieces-test-game"
	if err := natspkg.EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	meta := config.GameMeta{
		GameID: gameID, Mode: config.ModeTeams, PlayerCount: 4, TeamSize: 2,
		NextCount: config.MaxNextCount, SplitPieces: split, Bag: bag,
		Seed: 42, Status: config.GameStatusInProgress,
		CreatorID: "a0", CreatedAt: time.Now(), StartedAt: time.Now(),
	}
	data, _ := json.Marshal(meta)
	if err := natspkg.PublishMeta(ctx, js, gameID, data, 0); err != nil {
		t.Fatal(err)
	}
	for i, e := range []struct {
		id            string
		idx, tm, slot int
	}{{"a0", 0, 0, 0}, {"a1", 1, 0, 1}, {"b0", 2, 1, 0}} {
		eng := New(js, gameID, e.id, "", config.ModeTeams, ModePlayer, e.idx, e.tm, e.slot)
		if err := eng.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(eng.Stop)
		switch i {
		case 0:
			a0 = eng
		case 1:
			a1 = eng
		default:
			b0 = eng
		}
	}
	waitUntil(t, 5*time.Second, func() bool {
		return a0.Playfield().ActivePieceForPlayer(0) != nil &&
			a1.Playfield().ActivePieceForPlayer(1) != nil &&
			b0.Playfield().ActivePieceForPlayer(2) != nil
	}, "every seat's first piece to spawn")
	return a0, a1, b0
}

// TestSplitPiecesDealsTheWholeBag: each teammate holds a ration of its own,
// the two rations together are the seven pieces, and the other team's
// matching slot holds exactly the same hand — the deal follows the game's
// seed, so neither team is handed the easier half.
func TestSplitPiecesDealsTheWholeBag(t *testing.T) {
	a0, a1, b0 := startSplitTeamsGame(t, true)

	for _, e := range []*Engine{a0, a1, b0} {
		if !e.SplitPieces() {
			t.Fatalf("%s: the game splits its pieces but the engine does not", e.PlayerID())
		}
		if len(e.PieceSet()) == 0 {
			t.Fatalf("%s was dealt no pieces", e.PlayerID())
		}
	}
	seen := map[game.PieceType]bool{}
	for _, pt := range append(append([]game.PieceType{}, a0.PieceSet()...), a1.PieceSet()...) {
		seen[pt] = true
	}
	if len(seen) != 7 {
		t.Errorf("team A holds %d piece types between them (%v + %v), want all 7",
			len(seen), a0.PieceSet(), a1.PieceSet())
	}
	if got, want := b0.PieceSet(), a0.PieceSet(); len(got) != len(want) {
		t.Fatalf("team B slot 0 holds %v, team A slot 0 holds %v", got, want)
	} else {
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("team B slot 0 holds %v, team A slot 0 holds %v", got, want)
			}
		}
	}
	// Every seat can look up any slot's ration — the HUD lists them all.
	if got := a0.PieceSetForSlot(1); len(got) != len(a1.PieceSet()) {
		t.Errorf("slot 1's ration read from a0 = %v, want %v", got, a1.PieceSet())
	}
	if got := a0.PieceSetForSlot(2); got != nil {
		t.Errorf("a slot past the team = %v, want nothing", got)
	}
}

// TestSplitPiecesSequenceStaysInRation: what a seat actually spawns — and
// what its NEXT well reveals — comes only from its own ration. The piece a
// teammate holds is a piece this seat can never put on the board.
func TestSplitPiecesSequenceStaysInRation(t *testing.T) {
	a0, a1, _ := startSplitTeamsGame(t, true)

	for idx, e := range map[int]*Engine{0: a0, 1: a1} {
		ration := e.PieceSet()
		in := func(pt game.PieceType) bool {
			for _, r := range ration {
				if r == pt {
					return true
				}
			}
			return false
		}
		p := e.Playfield().ActivePieceForPlayer(idx)
		if p == nil {
			t.Fatalf("%s has no piece", e.PlayerID())
		}
		if !in(p.Type) {
			t.Errorf("%s spawned %v, not in its ration %v", e.PlayerID(), p.Type, ration)
		}
		next := e.NextPieces()
		if len(next) != config.MaxNextCount {
			t.Fatalf("%s previews %d pieces, want %d", e.PlayerID(), len(next), config.MaxNextCount)
		}
		for i, pt := range next {
			if !in(pt) {
				t.Errorf("%s previews %v at %d, not in its ration %v", e.PlayerID(), pt, i, ration)
			}
		}
	}
}

// TestNoSplitKeepsTheFullBag: without the setting a teams game is what it
// always was — every seat running the same full 7-bag, no ration anywhere.
func TestNoSplitKeepsTheFullBag(t *testing.T) {
	a0, a1, b0 := startSplitTeamsGame(t, false)
	for _, e := range []*Engine{a0, a1, b0} {
		if e.SplitPieces() {
			t.Errorf("%s split its pieces in a game that does not", e.PlayerID())
		}
		if got := e.PieceSet(); got != nil {
			t.Errorf("%s holds a ration (%v) in a game that does not split", e.PlayerID(), got)
		}
	}
	// The same sequence for everybody, as ever.
	if a := a0.NextPieces(); len(a) > 0 {
		for i, pt := range a {
			if b := a1.NextPieces(); pt != b[i] {
				t.Errorf("teammates preview different pieces at %d: %v vs %v", i, pt, b[i])
			}
		}
	}
}
