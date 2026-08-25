package main

import "testing"

// TestLookaheadNeverExceedsGamePreview pins the fair-visibility contract's
// preview rule (jetris-agent-guide.md §1): the planner's lookahead is the
// pieces the GAME reveals — its meta's next_count, the same ones a human sees
// in the NEXT well — and nothing on the agent's side can reach past it. Every
// difficulty is checked against every preview size, plus a meta without the
// field (0) and an out-of-range one, so a future "smarter" tuning cannot
// quietly plan on pieces its human opponents cannot see.
func TestLookaheadNeverExceedsGamePreview(t *testing.T) {
	const seed, pieceIdx = uint64(42), 7
	for _, d := range []string{"easy", "medium", "hard"} {
		tn := difficultyTuning(d)
		if tn.lookahead > maxNextCount {
			t.Errorf("%s asks for a %d-piece lookahead; no game reveals more than %d", d, tn.lookahead, maxNextCount)
		}
		for nextCount := 0; nextCount <= maxNextCount; nextCount++ {
			got := revealedPieces(seed, pieceIdx, nextCount, tn.lookahead)
			if want := min(nextCount, tn.lookahead); len(got) != want {
				t.Errorf("%s in a next_count %d game plans on %d upcoming pieces, want %d", d, nextCount, len(got), want)
			}
			// What it does see is the seat's own sequence, in play order.
			for i, pt := range got {
				if exp := pieceAt(seed, pieceIdx+1+i); pt != exp {
					t.Errorf("%s next_count %d: upcoming[%d] = %d, want sequence piece %d (%d)", d, nextCount, i, pt, pieceIdx+1+i, exp)
				}
			}
		}
	}
	// No preview means no lookahead at ANY difficulty — and a meta without the
	// field unmarshals to 0, never the create wizard's default of 1.
	if got := revealedPieces(seed, pieceIdx, 0, maxNextCount); len(got) != 0 {
		t.Errorf("next_count 0 (or absent) with the deepest lookahead: planned on %d upcoming pieces, want none", len(got))
	}
	// A foreign host writing nonsense can't open the horizon past what the
	// difficulty is allowed to use, and a negative count reveals nothing.
	if got := revealedPieces(seed, pieceIdx, 99, maxNextCount); len(got) != maxNextCount {
		t.Errorf("next_count 99: planned on %d upcoming pieces, want %d", len(got), maxNextCount)
	}
	if got := revealedPieces(seed, pieceIdx, -1, maxNextCount); len(got) != 0 {
		t.Errorf("next_count -1: planned on %d upcoming pieces, want none", len(got))
	}
}
