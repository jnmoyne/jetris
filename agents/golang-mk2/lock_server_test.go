package main

import (
	"context"
	"testing"
	"time"
)

// A shared-board lock whose landing cell was rewritten behind our back loses
// its CAS once, refetches, and lands on the retry — converged.
func TestLockRetriesAfterALostCAS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, placed, topped := g.spawn(ctx); !placed || topped {
		t.Fatalf("spawn: placed %v topped %v", placed, topped)
	}
	p := *g.piece
	dest := active{p.pt, p.orient, g.dropRowShared(p), p.col}
	destCells := pieceCells(dest.pt, dest.orient, dest.row, dest.col)
	// A peer wrote — and emptied — a landing cell: its sequence moved, our
	// expectation for it is stale.
	publishCell(t, ctx, g, destCells[0], &wireCell{O: true, T: 1, Pi: 1})
	publishCell(t, ctx, g, destCells[0], nil)
	if !g.lockPiece(ctx, dest) {
		t.Fatal("the lock was dropped")
	}
	if g.st.casLosses.Load() != 1 || g.st.lockRetries != 1 || g.st.locksDropped != 0 {
		t.Errorf("losses %d retries %d dropped %d, want 1/1/0", g.st.casLosses.Load(), g.st.lockRetries, g.st.locksDropped)
	}
	if g.piece != nil || g.pieceIdx != 1 {
		t.Errorf("after the lock: piece %+v, index %d", g.piece, g.pieceIdx)
	}
	for _, c := range destCells {
		if !g.locked[c].O {
			t.Errorf("landing cell %v not settled locally", c)
		}
	}
	assertConverged(t, ctx, g)
}

// A lock whose landing cell a crewmate's piece moved into is dropped: our
// piece stands where it was, active, and theirs is untouched.
func TestLockDroppedUnderAPeersPiece(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, placed, topped := g.spawn(ctx); !placed || topped {
		t.Fatalf("spawn: placed %v topped %v", placed, topped)
	}
	p := *g.piece
	dest := active{p.pt, p.orient, g.dropRowShared(p), p.col}
	destCells := pieceCells(dest.pt, dest.orient, dest.row, dest.col)
	theirs := active{4, 1, dest.row - 1, dest.col}
	publishCell(t, ctx, g, destCells[0], &wireCell{T: theirs.pt, A: true, R: theirs.orient, Ar: theirs.row, Ac: theirs.col, Pi: 1})
	if g.lockPiece(ctx, dest) {
		t.Fatal("locked over a crewmate's piece")
	}
	if g.piece == nil || *g.piece != p {
		t.Errorf("our piece: %+v, want it standing at %+v", g.piece, p)
	}
	if g.st.locksDropped != 1 {
		t.Errorf("locks dropped %d, want 1", g.st.locksDropped)
	}
	if pi, ok := g.othersAct[destCells[0]]; !ok || pi != 1 {
		t.Errorf("the peer's cell is not in our view: %v / %v", ok, pi)
	}
	snap := streamCells(t, ctx, g)
	for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
		if m := snap[c]; !m.wc.A || m.wc.Pi != 0 {
			t.Errorf("our cell %v on the stream: %+v", c, m.wc)
		}
	}
	for at, m := range snap {
		if m.wc.O {
			t.Errorf("a settled cell at %v: %+v", at, m.wc)
		}
	}
}
