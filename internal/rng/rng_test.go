package rng

import (
	"slices"
	"testing"

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
