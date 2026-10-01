package main

import (
	"context"
	"testing"
	"time"
)

// seedWithFirstPiece finds a seed whose first piece is the given type.
func seedWithFirstPiece(pt int) uint64 {
	for seed := uint64(1); ; seed++ {
		if pieceAtBag(seed, nil, bagSingle, 0) == pt {
			return seed
		}
	}
}

// Two coordinating agents with one obvious spot — a four-wide gap in the
// bottom row and an I each: the first to plan claims the gap, the second
// plans on that claim and goes elsewhere; the first's lock ends what the
// second waited on.
func TestSecondPlannerPlansAroundTheFirstsClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	h := sharedTestPeer(t, ctx, g, 1)
	withBlackboard(t, ctx, g, h)
	seed := seedWithFirstPiece(0)
	g.metaSeed, h.metaSeed = seed, seed
	g.a.tn, h.a.tn = difficultyTuning("hard"), difficultyTuning("hard")
	// The bottom row, full but for columns 5-8, on both boards and the stream.
	for _, x := range []*Game{g, h} {
		x.mu.Lock()
	}
	for c := 0; c < g.w; c++ {
		if c >= 5 && c <= 8 {
			continue
		}
		wc := &wireCell{O: true, T: 1}
		seq := publishCell(t, ctx, g, cell{23, c}, wc)
		g.foldCell(cell{23, c}, *wc, seq)
		h.foldCell(cell{23, c}, *wc, seq)
	}
	for _, x := range []*Game{g, h} {
		if _, placed, topped := x.spawn(ctx); !placed || topped || x.piece.pt != 0 {
			t.Fatalf("%s: spawn placed %v topped %v piece %+v", x.a.name, placed, topped, x.piece)
		}
	}
	// Each sees the other's spawn as the board consumer would deliver it.
	for _, pair := range [][2]*Game{{g, h}, {h, g}} {
		me, other := pair[0], pair[1]
		for _, c := range pieceCells(other.piece.pt, other.piece.orient, other.piece.row, other.piece.col) {
			me.foldCell(c, *other.activePayload(*other.piece), other.seqs[c])
		}
	}
	gp, hp := *g.piece, *h.piece
	g.mu.Unlock()
	h.mu.Unlock()

	first, pending, ok := g.plan(ctx, gp, 0)
	if !ok || first.orient != 0 || first.col != 5 || first.dropRow != 22 {
		t.Fatalf("the first planner did not take the gap: %+v", first)
	}
	first = g.confirmPlan(ctx, gp, first, pending)
	second, pending2, ok := h.plan(ctx, hp, 0)
	if !ok {
		t.Fatal("the second planner found nothing")
	}
	second = h.confirmPlan(ctx, hp, second, pending2)
	gap := cellSet(first.dest)
	for _, c := range second.dest {
		if gap[c] {
			t.Fatalf("the second planner took the gap too: %+v", second)
		}
	}
	h.mu.Lock()
	if _, ok := h.claims[0]; !ok || h.claims[0].Col != 5 || h.st.claimsYielded != 0 {
		t.Errorf("the second's view of the first's claim: %+v (yielded %d)", h.claims[0], h.st.claimsYielded)
	}
	// Whatever the second chose, make it wait on the first, then let the
	// first lock: the lock's claim ends the wait.
	h.deps = []depWait{{seat: 0, piece: 0, rev: h.claims[0].rev, cells: first.dest[:]}}
	if !h.depsPending() {
		t.Fatal("no wait pending")
	}
	h.mu.Unlock()
	g.mu.Lock()
	if !g.lockPiece(ctx, active{gp.pt, first.orient, first.dropRow, first.col}) {
		t.Fatal("the first's lock was dropped")
	}
	g.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		pending := h.depsPending()
		h.mu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first's lock never ended the second's wait")
		}
		time.Sleep(50 * time.Millisecond)
	}
	h.mu.Lock()
	c := h.claims[0]
	h.mu.Unlock()
	if c.Phase != phaseLocked || c.Cells[0] != [2]int{23, 5} {
		t.Errorf("the locked claim: %+v", c)
	}
}
