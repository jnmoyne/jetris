package main

import (
	"testing"
	"time"
)

// sweepTestGame is a crew's board of `seats` seats with no server behind it.
func sweepTestGame(seats int) *Game {
	g := newGame(&Agent{name: "me"}, "g", 0)
	g.mode, g.playerCount, g.seatsOnPF, g.extra = modeCooperative, seats, seats, minExtraColumns
	g.w, g.h = sharedWidth(seats, minExtraColumns), sharedHeight(seats, 0)
	g.spawnC = g.spawnColumn(nil)
	return g
}

// A foreign cell's idle clock restarts when its owner rewrites it (a new
// sequence) and only then; a cell that turns settled, empty or ours is
// forgotten.
func TestPeerCellClockRestartsOnlyOnANewSequence(t *testing.T) {
	g := sweepTestGame(2)
	at := cell{10, 8}
	wc := wireCell{T: 4, A: true, R: 1, Ar: 10, Ac: 8, Pi: 1}
	g.foldCell(at, wc, 10)
	pc, ok := g.peerCells[at]
	if !ok || pc.seq != 10 || pc.pi != 1 || pc.p != (active{4, 1, 10, 8}) {
		t.Fatalf("clock after delivery: %+v (present %v)", pc, ok)
	}
	t0 := pc.at
	g.notePeerCell(at, wc, 10, t0.Add(time.Second))
	if g.peerCells[at].at != t0 {
		t.Error("the same sequence again restarted the clock")
	}
	g.notePeerCell(at, wc, 11, t0.Add(2*time.Second))
	if pc := g.peerCells[at]; pc.seq != 11 || pc.at != t0.Add(2*time.Second) {
		t.Errorf("a new sequence did not restart the clock: %+v", pc)
	}
	g.foldCell(at, wireCell{O: true, T: 4, Pi: 1}, 12)
	if _, ok := g.peerCells[at]; ok {
		t.Error("a settled cell kept its clock")
	}
	g.foldCell(at, wc, 13)
	g.foldCell(at, wireCell{T: 2, A: true, Ar: 10, Ac: 8}, 14) // ours (seat 0)
	if _, ok := g.peerCells[at]; ok {
		t.Error("a cell of ours kept a peer clock")
	}
	g.foldCell(at, wc, 15)
	g.foldCell(at, wireCell{}, 16)
	if _, ok := g.peerCells[at]; ok {
		t.Error("an emptied cell kept its clock")
	}
}

// Stale groups are per piece, not per seat: a seat's stray copy runs out of
// clock while its live piece, at another anchor, does not; a seat the
// listing no longer holds is stale at once, whatever its clocks say.
func TestStaleGroupsByPieceAndSeat(t *testing.T) {
	g := sweepTestGame(2)
	now := time.Now()
	stray := active{4, 1, 10, 8}
	live := active{2, 0, 2, 7}
	seq := uint64(1)
	for _, p := range []active{stray, live} {
		for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
			g.foldCell(c, wireCell{T: p.pt, A: true, R: p.orient, Ar: p.row, Ac: p.col, Pi: 1}, seq)
			seq++
		}
	}
	for at, pc := range g.peerCells {
		if pc.p == stray {
			pc.at = now.Add(-idlePieceVacateAfter)
		} else {
			pc.at = now.Add(-time.Second)
		}
		g.peerCells[at] = pc
	}
	st := g.staleGroups(now)
	if len(st) != 1 || st[0].key != (pieceKey{1, stray}) || len(st[0].cells) != 4 {
		t.Fatalf("stale groups: %+v, want the stray alone", st)
	}
	// The seat gone from the roster: both of its pieces are stale.
	g.onRoster([]playerSummary{{PlayerID: "me", Seat: 0}})
	if st := g.staleGroups(now); len(st) != 2 {
		t.Errorf("seatless: %d stale group(s), want 2", len(st))
	}
	// No roster at all: every seat is present.
	g.roster = nil
	if st := g.staleGroups(now); len(st) != 1 {
		t.Errorf("no roster: %d stale group(s), want 1", len(st))
	}
}

// The deferred spawn's account follows its blockers: the same cells at the
// same sequences accumulate, a rewrite of any of them (the piece moved)
// starts over, and a box with nothing foreign on it has no account.
func TestSpawnBlockedAccountFollowsTheBlockers(t *testing.T) {
	g := sweepTestGame(2)
	cs := g.spawnCells()
	blocker := active{g.pieceAt(0), 0, spawnRow, g.spawnC}
	wc := func(c cell) wireCell {
		return wireCell{T: blocker.pt, A: true, R: blocker.orient, Ar: blocker.row, Ac: blocker.col, Pi: 1}
	}
	for i, c := range cs {
		g.foldCell(c, wc(c), uint64(i+1))
	}
	t0 := time.Now()
	if g.noteSpawnBlocked(nil, cs, t0) {
		t.Fatal("vacated at once")
	}
	if g.spawnBlock.since != t0 || len(g.spawnBlock.cells) != 4 {
		t.Fatalf("fresh account: %+v", g.spawnBlock)
	}
	if g.noteSpawnBlocked(nil, cs, t0.Add(time.Second)) || g.spawnBlock.since != t0 {
		t.Errorf("the same blockers a second later restarted the account: %+v", g.spawnBlock)
	}
	g.foldCell(cs[0], wc(cs[0]), 9) // the piece was rewritten: a fresh account
	t2 := t0.Add(2 * time.Second)
	if g.noteSpawnBlocked(nil, cs, t2) || g.spawnBlock.since != t2 {
		t.Errorf("a rewritten blocker kept the old account: %+v", g.spawnBlock)
	}
	for i, c := range cs {
		g.foldCell(c, wireCell{}, uint64(20+i))
	}
	if g.noteSpawnBlocked(nil, cs, t2) || len(g.spawnBlock.cells) != 0 {
		t.Errorf("an empty box kept an account: %+v", g.spawnBlock)
	}
}
