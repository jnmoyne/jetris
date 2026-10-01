package main

import (
	"context"
	"testing"
)

// TestSpawnResumesAdoptedPiece pins the rule that spawn NEVER creates a new
// piece while we already own a live one. A piece can be handed back to us
// between cycles — adopted off the wire when a committed transform (garbage
// lift, clear shift) moved it, or re-adopted by a resync — and the old
// behavior of spawning over it orphaned its cells on the stream as a frozen
// ghost no client ever vacates (the teams-mode frozen-agent bug). The caller
// must instead re-plan the piece we already hold.
func TestSpawnResumesAdoptedPiece(t *testing.T) {
	adopted := active{pt: 1, orient: 0, row: 5, col: 4}
	p := adopted
	g := &Game{piece: &p}

	spawnT, placed, topped := g.spawn(context.Background())

	if !placed || topped {
		t.Fatalf("spawn with an adopted piece: placed=%v topped=%v, want placed=true topped=false", placed, topped)
	}
	if spawnT.IsZero() {
		t.Error("spawn should return a usable spawn time for the resumed piece")
	}
	if g.piece == nil || *g.piece != adopted {
		t.Errorf("spawn must not replace the adopted piece: got %+v, want %+v", g.piece, adopted)
	}
}
