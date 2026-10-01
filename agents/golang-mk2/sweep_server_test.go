package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// A crewmate's piece idle for idlePieceVacateAfter is vacated — one CAS
// batch at its cells' last-seen sequences — and gone from the board and
// from our view.
func TestSweepVacatesIdlePeerPiece(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	g.mu.Lock()
	defer g.mu.Unlock()
	theirs := active{4, 1, 10, 8}
	foldPeerPiece(t, ctx, g, 1, theirs)
	if st := g.staleGroups(time.Now()); len(st) != 0 {
		t.Fatalf("a piece just delivered is stale: %+v", st)
	}
	for at, pc := range g.peerCells {
		pc.at = pc.at.Add(-idlePieceVacateAfter)
		g.peerCells[at] = pc
	}
	st := g.staleGroups(time.Now())
	if len(st) != 1 {
		t.Fatalf("stale groups: %d, want 1", len(st))
	}
	if !g.vacateForeignCells(ctx, st[0].cells) {
		t.Fatal("the vacate did not land")
	}
	snap := streamCells(t, ctx, g)
	for _, c := range pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col) {
		if m := snap[c]; m.wc.A || m.wc.O {
			t.Errorf("cell %v still on the stream: %+v", c, m.wc)
		}
	}
	if len(g.othersAct) != 0 || len(g.othersPiece) != 0 || len(g.peerCells) != 0 {
		t.Errorf("our view kept the piece: %v / %v / %v", g.othersAct, g.othersPiece, g.peerCells)
	}
}

// A piece whose owner moved it since we grouped it fails the vacate's CAS:
// nothing is vacated, and the loss resyncs our view to the stream's.
func TestSweepSkipsAPieceThatMoved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	g.mu.Lock()
	defer g.mu.Unlock()
	theirs := active{4, 1, 10, 8}
	foldPeerPiece(t, ctx, g, 1, theirs)
	for at, pc := range g.peerCells {
		pc.at = pc.at.Add(-idlePieceVacateAfter)
		g.peerCells[at] = pc
	}
	group := g.staleGroups(time.Now())[0]
	cs := pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col)
	moved := active{theirs.pt, theirs.orient, theirs.row + 1, theirs.col}
	newSeq := publishCell(t, ctx, g, cs[0], &wireCell{T: moved.pt, A: true, R: moved.orient, Ar: moved.row, Ac: moved.col, Pi: 1})
	if g.vacateForeignCells(ctx, group.cells) {
		t.Fatal("vacated a piece that moved")
	}
	if pc := g.peerCells[cs[0]]; pc.seq != newSeq || pc.p != moved {
		t.Errorf("the loss did not resync the moved cell: %+v", pc)
	}
	snap := streamCells(t, ctx, g)
	for _, c := range cs[1:] {
		if !snap[c].wc.A {
			t.Errorf("cell %v was vacated", c)
		}
	}
}

// A piece parked on our spawn box, unmoved at the same sequences for
// spawnBlockedVacateAfter, is vacated whole, and the spawn goes through.
func TestSpawnBlockerVacatedAfterThreeSeconds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeCooperative, 0, 2)
	g.mu.Lock()
	defer g.mu.Unlock()
	cs := g.spawnCells()
	blocker := active{g.pieceAt(g.pieceIdx), 0, spawnRow, g.spawnC}
	foldPeerPiece(t, ctx, g, 1, blocker)
	if _, placed, topped := g.spawn(ctx); placed || topped {
		t.Fatalf("spawn over a foreign piece: placed %v topped %v, want deferred", placed, topped)
	}
	t0 := time.Now()
	if g.noteSpawnBlocked(ctx, cs, t0) {
		t.Fatal("vacated at once")
	}
	if g.noteSpawnBlocked(ctx, cs, t0.Add(spawnBlockedVacateAfter-time.Millisecond)) {
		t.Fatal("vacated before the threshold")
	}
	if !g.noteSpawnBlocked(ctx, cs, t0.Add(spawnBlockedVacateAfter)) {
		t.Fatal("the stale blocker was not vacated")
	}
	if g.sharedBlocked(cs) || g.st.spawnUnblocks != 1 {
		t.Errorf("box still blocked locally (%v), unblocks %d", g.sharedBlocked(cs), g.st.spawnUnblocks)
	}
	snap := streamCells(t, ctx, g)
	for _, c := range cs {
		if m := snap[c]; m.wc.A || m.wc.O {
			t.Errorf("cell %v still held on the stream: %+v", c, m.wc)
		}
	}
	if _, placed, topped := g.spawn(ctx); !placed || topped {
		t.Fatalf("spawn after the vacate: placed %v topped %v", placed, topped)
	}
	assertConverged(t, ctx, g)
}

// On a team's board the vacate takes the txn gate, so a racing transform
// can never resurrect the piece from a stale snapshot.
func TestTeamVacateIsGated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := sharedTestGame(t, ctx, modeTeams, 0, 2)
	g.mu.Lock()
	defer g.mu.Unlock()
	theirs := active{4, 1, 10, 8}
	foldPeerPiece(t, ctx, g, 1, theirs)
	for at, pc := range g.peerCells {
		pc.at = pc.at.Add(-idlePieceVacateAfter)
		g.peerCells[at] = pc
	}
	st := g.staleGroups(time.Now())
	if len(st) != 1 || !g.vacateForeignCells(ctx, st[0].cells) {
		t.Fatalf("gated vacate did not land (%d group(s))", len(st))
	}
	raw, err := g.stream.GetLastMsgForSubject(ctx, g.txnSubject())
	if err != nil {
		t.Fatalf("no txn register after the vacate: %v", err)
	}
	var reg txnReg
	_ = json.Unmarshal(raw.Data, &reg)
	if reg.Op != "vacate" || reg.By != 0 || g.txnSeq != raw.Sequence {
		t.Errorf("txn register: %+v at %d (local %d)", reg, raw.Sequence, g.txnSeq)
	}
	snap := streamCells(t, ctx, g)
	for _, c := range pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col) {
		if m := snap[c]; m.wc.A || m.wc.O {
			t.Errorf("cell %v still on the stream: %+v", c, m.wc)
		}
	}
}
