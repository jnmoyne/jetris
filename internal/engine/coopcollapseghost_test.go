package engine

import (
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"jetris/internal/game"
)

// TestCoopCollapseNeverCopiesPeerPiece reproduces the jetris-eu incident of
// 2026-09-12 (game 0ca68b24): a's lock completes a row, and between a's
// projection of the collapse — with b's piece where a last saw it — and its
// publish, b moves twice. a's batch loses its CAS race. Merged cell by cell
// from the stale projection, it used to skip the cells b's piece holds now
// but still write the projection's shifted copy of b's piece into the cells
// it had moved off: seat 1 with cells at two anchors on the stream. b's
// engine adopted the copy, the real cells stayed behind as a ghost nobody
// vacated, and the ghost held a's spawn box for the rest of the game. The
// collapse is now projected again off a server snapshot
// (publishCoopTransform): b's piece, shifted with the stack, stays one whole
// piece, and b plays on from where the collapse put it.
func TestCoopCollapseNeverCopiesPeerPiece(t *testing.T) {
	const gameID = "crew-collapse-ghost"
	js, es := startCrewGame(t, gameID, 2, 2, 4)
	a, b := es[0], es[1]
	pf := a.Playfield()
	bottom := pf.Height - 1

	// b's piece (seed 42: a T at the seat's spawn point, col 7) down into
	// the visible rows — above the row a will clear, out of a's drop path
	// (cols 3-5) — so the collapse shifts it.
	waitUntil(t, 5*time.Second, func() bool {
		p := b.Playfield().ActivePieceForPlayer(1)
		if p == nil {
			return false
		}
		if p.Row >= 10 {
			return true
		}
		b.MoveDown()
		return false
	}, "b's piece to descend into the visible rows")
	idx0 := b.PieceIdx()

	// The bottom row filled but for a's T's footprint: a's hard drop
	// completes it.
	cells := make([]game.Cell, pf.Width)
	for c := range cells {
		if c < 3 || c > 5 {
			cells[c] = game.Cell{Occupied: true, PieceType: game.PieceL, PlayerIdx: 0}
		}
	}
	publishCoopRowCells(t, js, gameID, bottom, cells)
	waitUntil(t, 3*time.Second, func() bool {
		n := 0
		for _, c := range a.Playfield().Rows[bottom].Cells {
			if c.Occupied {
				n++
			}
		}
		return n == pf.Width-3
	}, "the pre-filled bottom row to apply")

	// The race: b moves twice while a's collapse is projected but not yet
	// published. The hook runs on a's consumer goroutine, off a's lock.
	var hookFailed atomic.Value
	a.testHookBeforeCoopClearPublish = func() {
		bp := func() *game.Piece { return b.Playfield().ActivePieceForPlayer(1) }
		before := bp()
		if before == nil {
			hookFailed.Store("b had no piece when a's collapse was projected")
			return
		}
		b.MoveDown()
		if !pollUntil(2*time.Second, func() bool { p := bp(); return p != nil && p.Row > before.Row }) {
			hookFailed.Store("b's piece did not move down")
			return
		}
		mid := bp()
		b.MoveLeft()
		if !pollUntil(2*time.Second, func() bool { p := bp(); return p != nil && p.Col < mid.Col }) {
			hookFailed.Store("b's piece did not move left")
		}
	}

	a.HardDrop()
	waitUntil(t, 5*time.Second, func() bool {
		return a.Score() > 0 && len(game.CompletedRows(a.Playfield())) == 0
	}, "a's line clear to register")
	if msg := hookFailed.Load(); msg != nil {
		t.Fatal(msg)
	}
	time.Sleep(300 * time.Millisecond) // the collapse's echo, on b

	// THE ASSERTION: on the stream, seat 1's cells make exactly one piece,
	// and it is the piece b's engine holds.
	on := seatCellsOnStream(t, js, gameID, pf, 1)
	anchors := anchorsOf(on)
	if len(on) != 4 || len(anchors) != 1 {
		t.Fatalf("seat 1 on the stream after a's collapse: %d cell(s) at %d anchor(s): %v", len(on), len(anchors), describeAnchors(anchors))
	}
	if got := b.PieceIdx(); got != idx0 {
		t.Fatalf("b respawned on a's collapse: pieceIdx %d -> %d", idx0, got)
	}
	bp := b.Playfield().ActivePieceForPlayer(1)
	for anchor := range anchors {
		if bp == nil || *bp != anchor {
			t.Fatalf("b's engine holds %+v, the stream %+v", bp, anchor)
		}
	}

	// And b plays on from there: a move moves that piece, whole.
	col := bp.Col
	b.MoveLeft()
	waitUntil(t, 3*time.Second, func() bool {
		p := b.Playfield().ActivePieceForPlayer(1)
		return p != nil && p.Col == col-1
	}, "b's piece to move left after the collapse")
	on = seatCellsOnStream(t, js, gameID, pf, 1)
	anchors = anchorsOf(on)
	if len(on) != 4 || len(anchors) != 1 {
		t.Fatalf("seat 1 on the stream after b's move: %d cell(s) at %d anchor(s): %v", len(on), len(anchors), describeAnchors(anchors))
	}
}

func describeAnchors(anchors map[game.Piece]int) string {
	s := ""
	for p, n := range anchors {
		s += fmt.Sprintf(" (%d,%d)x%d", p.Row, p.Col, n)
	}
	return s
}

// pollUntil is waitUntil off the test goroutine: it reports the outcome
// instead of failing the test.
func pollUntil(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
