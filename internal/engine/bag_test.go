package engine

import (
	"testing"

	"jetris/internal/config"
	"jetris/internal/rng"
)

// The bag rule (GameMeta.Bag, gameplays §1b): how every seat's sequence is
// dealt — the standard 7-bag, the double bag, or no bag at all. The engine's
// half of it is reading the rule off the meta at Start and handing rng the
// same seed, set and kind every other peer does; these tests pin that
// against rng itself, split-pieces rations included.

// TestDoubleBagReachesEverySeat: a split 2v2 created with the double bag
// deals every seat a double bag of its own ration — each engine reports the
// rule, and its preview is rng.NewBag's sequence for that seat's ration.
func TestDoubleBagReachesEverySeat(t *testing.T) {
	a0, a1, b0 := startTeamsGame(t, true, config.BagDouble)
	for n, e := range []*Engine{a0, a1, b0} {
		if e.Bag() != config.BagDouble {
			t.Fatalf("engine %d: bag = %q, want the double bag", n, e.Bag())
		}
		if !e.SplitPieces() || len(e.PieceSet()) == 0 {
			t.Fatalf("engine %d: no ration in a split game", n)
		}
		want := rng.NewBag(42, e.PieceSet(), config.BagDouble)
		idx := e.PieceIdx()
		next := e.NextPieces()
		if len(next) != config.MaxNextCount {
			t.Fatalf("engine %d: preview of %d, want %d", n, len(next), config.MaxNextCount)
		}
		for i, pt := range next {
			if w := want.Piece(idx + 1 + uint64(i)); pt != w {
				t.Errorf("engine %d: next[%d] = %v, want %v (the double bag of %v)", n, i, pt, w, e.PieceSet())
			}
		}
	}
}

// TestNoBagReachesEverySeat: an unsplit teams game created with no bag draws
// every seat from the seven types independently — rng.NewBag over the full
// set off the raw seed, the same on both teams.
func TestNoBagReachesEverySeat(t *testing.T) {
	a0, a1, b0 := startTeamsGame(t, false, config.BagNone)
	want := rng.NewBag(42, nil, config.BagNone)
	for n, e := range []*Engine{a0, a1, b0} {
		if e.Bag() != config.BagNone {
			t.Fatalf("engine %d: bag = %q, want no bag", n, e.Bag())
		}
		if e.SplitPieces() {
			t.Fatalf("engine %d: split pieces in an unsplit game", n)
		}
		idx := e.PieceIdx()
		for i, pt := range e.NextPieces() {
			if w := want.Piece(idx + 1 + uint64(i)); pt != w {
				t.Errorf("engine %d: next[%d] = %v, want %v", n, i, pt, w)
			}
		}
	}
}

// TestOfflineBag: the transport-less engine (the How to play tour) deals its
// NEXT well by the rules' bag like a live one, and reads an unset bag as the
// 7-bag.
func TestOfflineBag(t *testing.T) {
	plain := Offline(OfflineGame{Mode: config.ModeCooperative, Seed: 42, Rules: config.GameRules{NextCount: config.MaxNextCount, Ghost: true}})
	if plain.Bag() != config.BagSingle {
		t.Fatalf("an unset bag reads as %q, want the 7-bag", plain.Bag())
	}
	double := Offline(OfflineGame{Mode: config.ModeCooperative, Seed: 42, Rules: config.GameRules{NextCount: config.MaxNextCount, Ghost: true, Bag: config.BagDouble}})
	if double.Bag() != config.BagDouble {
		t.Fatalf("bag = %q, want the double bag", double.Bag())
	}
	want := rng.NewBag(42, nil, config.BagDouble)
	for i, pt := range double.NextPieces() {
		if w := want.Piece(1 + uint64(i)); pt != w {
			t.Errorf("next[%d] = %v, want %v", i, pt, w)
		}
	}
}
