package rng

import (
	"slices"
	"testing"

	"jetris/internal/config"
	"jetris/internal/game"
)

func TestDeterministic(t *testing.T) {
	s1 := New(42)
	s2 := New(42)
	for i := uint64(0); i < 50; i++ {
		if s1.Piece(i) != s2.Piece(i) {
			t.Errorf("sequences diverge at index %d", i)
		}
	}
}

func TestSeekable(t *testing.T) {
	s := New(42)
	// Calling Piece(N) should always return the same value regardless of previous calls
	p1 := s.Piece(25)
	p2 := s.Piece(25)
	if p1 != p2 {
		t.Error("Piece(25) should be deterministic")
	}
}

func TestBagContainsAllPieces(t *testing.T) {
	s := New(12345)
	// Check first few bags
	for bag := uint64(0); bag < 5; bag++ {
		seen := make(map[game.PieceType]bool)
		for pos := uint64(0); pos < 7; pos++ {
			p := s.Piece(bag*7 + pos)
			seen[p] = true
		}
		if len(seen) != 7 {
			t.Errorf("bag %d has %d unique pieces, want 7", bag, len(seen))
		}
	}
}

func TestDifferentSeeds(t *testing.T) {
	s1 := New(1)
	s2 := New(2)
	same := 0
	for i := uint64(0); i < 14; i++ {
		if s1.Piece(i) == s2.Piece(i) {
			same++
		}
	}
	// With different seeds, it's extremely unlikely all 14 match
	if same == 14 {
		t.Error("different seeds should produce different sequences")
	}
}

// TestFullBagFixtures pins the standard 7-bag against the values every peer's
// port of it reproduces (agents/golang-mk1's TestPieceParity and
// example-python's selftest use the same two seeds): splitting the pieces
// added a set to the Sequence, and an ordinary game's sequence must not have
// moved a single index because of it.
func TestFullBagFixtures(t *testing.T) {
	fixtures := map[uint64][]game.PieceType{
		42:    {2, 3, 4, 1, 0, 5, 6, 6, 0, 5, 4, 2, 3, 1, 2, 1, 4, 3, 6, 5, 0},
		12345: {6, 3, 2, 1, 0, 4, 5, 0, 1, 5, 3, 2, 6, 4, 3, 6, 4, 2, 5, 1, 0},
	}
	for seed, want := range fixtures {
		s := New(seed)
		for i, w := range want {
			if got := s.Piece(uint64(i)); got != w {
				t.Fatalf("seed %d index %d: piece = %v, want %v", seed, i, got, w)
			}
		}
	}
}

// TestPieceSetsDealTheWholeBag is the split's contract (GameMeta.SplitPieces):
// however many seats it is dealt to, every one of the seven types goes to
// somebody and nobody is left empty-handed — so the team between them still
// has the whole bag.
func TestPieceSetsDealTheWholeBag(t *testing.T) {
	for seats := 1; seats <= 12; seats++ {
		for _, seed := range []uint64{1, 42, 12345, 999999} {
			sets := PieceSets(seed, seats)
			if len(sets) != seats {
				t.Fatalf("seed %d, %d seats: %d sets", seed, seats, len(sets))
			}
			seen := map[game.PieceType]bool{}
			for slot, set := range sets {
				if len(set) == 0 {
					t.Errorf("seed %d, %d seats: slot %d was dealt nothing", seed, seats, slot)
				}
				for i, pt := range set {
					if i > 0 && set[i-1] >= pt {
						t.Errorf("seed %d, %d seats: slot %d ration %v is not in piece order", seed, seats, slot, set)
					}
					seen[pt] = true
				}
			}
			if len(seen) != 7 {
				t.Errorf("seed %d, %d seats: the deal covers %d piece types, want all 7", seed, seats, len(seen))
			}
		}
	}
}

// TestPieceSetsEven checks the deal is as even as seven pieces allow: with two
// to seven seats the rations differ by at most one piece, so no seat is left
// playing a sliver of the game while another holds most of it.
func TestPieceSetsEven(t *testing.T) {
	for seats := 2; seats <= 7; seats++ {
		for _, seed := range []uint64{1, 42, 12345, 999999} {
			lo, hi := 7, 0
			for _, set := range PieceSets(seed, seats) {
				lo, hi = min(lo, len(set)), max(hi, len(set))
			}
			if hi-lo > 1 {
				t.Errorf("seed %d, %d seats: rations range %d..%d pieces", seed, seats, lo, hi)
			}
		}
	}
}

// TestPieceSetsDeterministic pins the deal as the wire contract it is: every
// peer computing it off the game's seed must get the same hands, or two
// clients would disagree about what a seat is even allowed to spawn.
func TestPieceSetsDeterministic(t *testing.T) {
	fixtures := map[uint64]map[int][][]game.PieceType{
		42: {
			2: {{0, 1, 4}, {2, 3, 5, 6}},
			3: {{0, 4, 5}, {2, 3}, {1, 6}},
			7: {{5}, {0}, {6}, {1}, {4}, {3}, {2}},
		},
		12345: {
			2: {{0, 1, 2, 5}, {3, 4, 6}},
			3: {{1, 2}, {0, 5, 6}, {3, 4}},
			7: {{6}, {2}, {1}, {0}, {3}, {5}, {4}},
		},
	}
	for seed, bySeats := range fixtures {
		for seats, want := range bySeats {
			got := PieceSets(seed, seats)
			if len(got) != len(want) {
				t.Fatalf("seed %d, %d seats: %d sets, want %d", seed, seats, len(got), len(want))
			}
			for slot := range want {
				if !slices.Equal(got[slot], want[slot]) {
					t.Errorf("seed %d, %d seats: slot %d = %v, want %v", seed, seats, slot, got[slot], want[slot])
				}
			}
		}
	}
}

// TestSetSequenceStaysInItsRation: a seat that holds three types spawns those
// three and nothing else, and every bag-length run of its sequence is a
// permutation of the ration — the same fairness the 7-bag gives a whole game,
// scaled to the hand.
func TestSetSequenceStaysInItsRation(t *testing.T) {
	const seed = uint64(42)
	for _, set := range PieceSets(seed, 3) {
		s := NewSet(seed, set)
		n := uint64(len(set))
		for bag := uint64(0); bag < 6; bag++ {
			seen := map[game.PieceType]bool{}
			for pos := uint64(0); pos < n; pos++ {
				p := s.Piece(bag*n + pos)
				if !slices.Contains(set, p) {
					t.Fatalf("ration %v spawned %v", set, p)
				}
				seen[p] = true
			}
			if len(seen) != len(set) {
				t.Errorf("ration %v, bag %d: %d distinct pieces, want %d", set, bag, len(seen), len(set))
			}
		}
	}
}

// TestSetSequenceIndependentOfSlot: two seats holding different rations draw
// different orders (the ration salts the stream), while the same ration draws
// the same order wherever it is held — on both teams' slot N, in a
// spectator's engine, in an agent's port.
func TestSetSequenceIndependentOfSlot(t *testing.T) {
	const seed = uint64(7)
	a := NewSet(seed, []game.PieceType{game.PieceI, game.PieceT})
	b := NewSet(seed, []game.PieceType{game.PieceT, game.PieceI}) // the same hand, dealt in another order
	for i := uint64(0); i < 20; i++ {
		if a.Piece(i) != b.Piece(i) {
			t.Fatalf("the same ration drew different sequences at index %d", i)
		}
	}
	c := NewSet(seed, []game.PieceType{game.PieceS, game.PieceZ})
	same := 0
	for i := uint64(0); i < 20; i++ {
		if a.Piece(i) == c.Piece(i) {
			same++
		}
	}
	if same == 20 {
		t.Error("different rations should not draw the same sequence")
	}
}

// TestPieceSetFor covers the edges the engine leans on: a team of one holds
// the whole bag, and a slot outside the deal (a roster that outgrew its meta)
// falls back to it rather than to nothing.
func TestPieceSetFor(t *testing.T) {
	if got := PieceSetFor(42, 1, 0); len(got) != 7 {
		t.Errorf("a team of one holds %v, want all seven", got)
	}
	if got := PieceSetFor(42, 3, 9); len(got) != 7 {
		t.Errorf("slot past the deal holds %v, want all seven", got)
	}
}

// The bag rule (config.Bag, gameplays §1b): the same seed dealt three ways.

// TestBagFixtures pins the double bag and no bag against the values every
// port reproduces (golang-mk1's TestBagParity and --selftest,
// example-python's selftest), as TestFullBagFixtures pins the 7-bag — over
// the seven types and over a split-pieces ration — and pins NewBag's
// standard bag to New's sequence, so the rule's existence moved no ordinary
// game's pieces.
func TestBagFixtures(t *testing.T) {
	full := map[config.Bag]map[uint64][]game.PieceType{
		config.BagDouble: {
			42:    {6, 5, 2, 3, 0, 3, 2, 4, 1, 4, 0, 1, 6, 5, 4, 5, 4, 1, 3, 1, 3, 6, 0, 2, 5, 6, 0, 2},
			12345: {3, 5, 4, 0, 3, 0, 6, 1, 6, 1, 5, 2, 2, 4, 4, 4, 1, 1, 0, 2, 0, 3, 5, 6, 6, 5, 3, 2},
		},
		config.BagNone: {
			42:    {6, 1, 0, 4, 1, 2, 2, 5, 4, 6, 6, 5, 0, 0, 5, 0, 3, 0, 0, 0, 0},
			12345: {5, 4, 0, 5, 5, 0, 2, 1, 4, 4, 1, 6, 2, 1, 5, 2, 1, 6, 5, 5, 5},
		},
	}
	for bag, bySeed := range full {
		for seed, want := range bySeed {
			s := NewBag(seed, nil, bag)
			if s.Bag() != bag {
				t.Fatalf("NewBag(%d, nil, %q).Bag() = %q", seed, bag, s.Bag())
			}
			for i, w := range want {
				if got := s.Piece(uint64(i)); got != w {
					t.Fatalf("bag %q seed %d index %d: piece = %v, want %v", bag, seed, i, got, w)
				}
			}
		}
	}
	// A ration under each kind: seed 42 dealt to two seats ({I O Z}, {T S J L}).
	sets := PieceSets(42, 2)
	rations := map[config.Bag][][]game.PieceType{
		config.BagDouble: {{1, 0, 4, 1, 0, 4, 0, 4, 4, 1, 1, 0}, {6, 6, 5, 3, 3, 5, 2, 2, 6, 3, 5, 2}},
		config.BagNone:   {{1, 0, 0, 4, 1, 0, 1, 4, 1, 0, 4, 4}, {2, 5, 6, 2, 6, 2, 2, 2, 5, 3, 6, 3}},
	}
	for bag, bySlot := range rations {
		for slot, want := range bySlot {
			s := NewBag(42, sets[slot], bag)
			for i, w := range want {
				if got := s.Piece(uint64(i)); got != w {
					t.Errorf("bag %q ration %v index %d: piece = %v, want %v", bag, sets[slot], i, got, w)
				}
			}
		}
	}
	// The standard bag through NewBag is New / NewSet unchanged.
	for _, seed := range []uint64{42, 12345} {
		single, plain := NewBag(seed, nil, config.BagSingle), New(seed)
		set, ration := NewBag(seed, sets[1], config.BagSingle), NewSet(seed, sets[1])
		for i := uint64(0); i < 50; i++ {
			if single.Piece(i) != plain.Piece(i) {
				t.Fatalf("seed %d index %d: NewBag's standard bag differs from New", seed, i)
			}
			if set.Piece(i) != ration.Piece(i) {
				t.Fatalf("seed %d index %d: NewBag's standard bag over a ration differs from NewSet", seed, i)
			}
		}
	}
}

// TestDoubleBagDealsEachTypeTwice: every fourteen pieces of a double bag are
// two of each of the seven types — and every 2k of a ration's double bag two
// of each of its k types.
func TestDoubleBagDealsEachTypeTwice(t *testing.T) {
	for _, seed := range []uint64{1, 42, 12345} {
		for _, set := range append([][]game.PieceType{nil}, PieceSets(seed, 3)...) {
			s := NewBag(seed, set, config.BagDouble)
			n := uint64(len(s.Set()))
			for bag := uint64(0); bag < 6; bag++ {
				count := map[game.PieceType]int{}
				for pos := uint64(0); pos < 2*n; pos++ {
					count[s.Piece(bag*2*n+pos)]++
				}
				for _, pt := range s.Set() {
					if count[pt] != 2 {
						t.Errorf("seed %d set %v bag %d: piece %v dealt %d times, want 2", seed, s.Set(), bag, pt, count[pt])
					}
				}
				if len(count) != len(s.Set()) {
					t.Errorf("seed %d set %v bag %d: dealt %v outside the set", seed, s.Set(), bag, count)
				}
			}
		}
	}
}

// TestNoBagDrawsFromTheSet: no bag stays inside its set — the seven types,
// or the ration — and, unlike a bag, is free to repeat: over a long enough
// run every type turns up and some type twice in a row, while the draws stay
// deterministic and seekable like every other sequence's.
func TestNoBagDrawsFromTheSet(t *testing.T) {
	for _, seed := range []uint64{1, 42, 12345} {
		for _, set := range append([][]game.PieceType{nil}, PieceSets(seed, 2)...) {
			s := NewBag(seed, set, config.BagNone)
			seen := map[game.PieceType]bool{}
			repeat := false
			var prev game.PieceType
			for i := uint64(0); i < 200; i++ {
				p := s.Piece(i)
				if !slices.Contains(s.Set(), p) {
					t.Fatalf("seed %d set %v: drew %v", seed, s.Set(), p)
				}
				if i > 0 && p == prev {
					repeat = true
				}
				seen[p], prev = true, p
			}
			if len(seen) != len(s.Set()) {
				t.Errorf("seed %d set %v: 200 draws covered only %v", seed, s.Set(), seen)
			}
			if !repeat {
				t.Errorf("seed %d set %v: 200 draws never repeated a type — that is a bag, not chance", seed, s.Set())
			}
		}
	}
	s := NewBag(42, nil, config.BagNone)
	if a, b := s.Piece(25), s.Piece(25); a != b {
		t.Errorf("Piece(25) = %v then %v; no bag must be seekable too", a, b)
	}
}

// TestUnknownBagIsTheSevenBag: a kind this build does not know — a meta from
// a newer one — is dealt as the standard 7-bag, as config.Bag.Normalized
// reads it, rather than as nothing at all.
func TestUnknownBagIsTheSevenBag(t *testing.T) {
	s := NewBag(42, nil, config.Bag("triple"))
	if s.Bag() != config.BagSingle {
		t.Fatalf("Bag() = %q, want the 7-bag", s.Bag())
	}
	plain := New(42)
	for i := uint64(0); i < 21; i++ {
		if s.Piece(i) != plain.Piece(i) {
			t.Fatalf("index %d: an unknown kind is not dealt as the 7-bag", i)
		}
	}
}
