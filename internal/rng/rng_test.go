package rng

import (
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
