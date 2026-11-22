package main

import (
	"context"
	"testing"
	"time"
)

// seedBoard writes the replica's board to the stream — the settled cells,
// our piece and the teammates' — as the board a transform starts from, and
// takes the cells' sequences from the write.
func seedBoard(t *testing.T, ctx context.Context, g *Game) {
	t.Helper()
	var cells []cellUpd
	for at, wc := range g.locked {
		cc := wc
		cells = append(cells, cellUpd{at: at, c: &cc})
	}
	for pi, p := range g.othersPiece {
		for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
			cells = append(cells, cellUpd{at: c, c: &wireCell{T: p.pt, A: true, R: p.orient, Ar: p.row, Ac: p.col, Pi: pi}})
		}
	}
	if g.piece != nil {
		for _, c := range pieceCells(g.piece.pt, g.piece.orient, g.piece.row, g.piece.col) {
			cells = append(cells, cellUpd{at: c, c: g.activePayload(*g.piece)})
		}
	}
	if err := g.publishBatch(ctx, cells, true); err != nil {
		t.Fatal(err)
	}
	g.reindexOthers()
}

// assertPieceOnStream checks the stream holds the piece whole, as owner's:
// each of its four cells an active cell of that seat, at that anchor.
func assertPieceOnStream(t *testing.T, snap map[cell]boardMsg, owner int, p active) {
	t.Helper()
	for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
		wc := snap[c].wc
		if !wc.A || wc.Pi != owner || wc.Ar != p.row || wc.Ac != p.col {
			t.Errorf("cell %v: want seat %d's piece at (%d,%d), the stream has %+v", c, owner, p.row, p.col, wc)
		}
	}
}

// A collapse carries the teammates' falling pieces down with the stack, and
// a piece that hung just over it shifts INTO cells the stack held: emptied
// by the settled cells' diff, taken by the piece. Written twice in the one
// batch, the cell's second write could never meet its expectation — the
// clear was refused every time it was tried, by every seat, and the rows
// stood full. One write a cell: the clear commits, the piece whole.
func TestCoopClearShiftsAPieceOntoTheStack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := pipelineTestGame(t, ctx, pubPessimistic)
	g.mode = modeCooperative
	g.mu.Lock()
	defer g.mu.Unlock()

	h := g.height()
	for r := h - 5; r < h; r++ { // five full rows…
		for c := 0; c < g.w; c++ {
			g.locked[cell{r, c}] = wireCell{O: true, T: 1}
		}
	}
	delete(g.locked, cell{h - 5, 0})       // …the top one short of a cell: four clear, one stays
	theirs := active{1 /*O*/, 0, h - 8, 6} // an O a row above the stack
	g.othersPiece[1] = theirs
	seedBoard(t, ctx, g)

	rows := g.completedRowsShared()
	if len(rows) != 4 {
		t.Fatalf("completed rows: %v, want the bottom four", rows)
	}
	if n := g.clearRowsCoop(ctx, rows); n != 4 {
		t.Fatalf("cleared %d row(s), want 4", n)
	}
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shifted := active{theirs.pt, theirs.orient, theirs.row + 4, theirs.col}
	assertPieceOnStream(t, snap, 1, shifted)
	own := cellSet(pieceCells(shifted.pt, shifted.orient, shifted.row, shifted.col))
	for at, m := range snap {
		switch {
		case m.wc.A && !own[at]:
			t.Errorf("stray active cell %v: %+v", at, m.wc)
		case m.wc.O && (at.r != h-1 || at.c == 0):
			t.Errorf("settled cell %v left by the clear: %+v", at, m.wc)
		}
	}
	for c := 1; c < g.w; c++ {
		if !snap[cell{h - 1, c}].wc.O {
			t.Errorf("cell %v: the row that stays is not there", cell{h - 1, c})
		}
	}
}

// The same collapse behind the board's gate (a team's board, a crew's whose
// floor rises): there the teammate's cells are guarded and the settled
// cells are not, and the cell written twice — the piece's, then the empty
// one the diff ordered last — left the piece on the wire short of the
// cells it had shifted into.
func TestGatedClearShiftsAPieceOntoTheStack(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := pipelineTestGame(t, ctx, pubPessimistic)
	g.mode, g.teamScores, g.teamLines = modeTeams, make([]int, 2), make([]int, 2)
	g.mu.Lock()
	defer g.mu.Unlock()

	h := g.height()
	for r := h - 5; r < h; r++ {
		for c := 0; c < g.w; c++ {
			g.locked[cell{r, c}] = wireCell{O: true, T: 1}
		}
	}
	delete(g.locked, cell{h - 5, 0})
	theirs := active{1 /*O*/, 0, h - 8, 6}
	g.othersPiece[1] = theirs
	seedBoard(t, ctx, g)

	g.clearRows(ctx, g.completedRowsShared(), 0)
	if g.lines != 4 {
		t.Fatalf("cleared %d row(s), want 4", g.lines)
	}
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	shifted := active{theirs.pt, theirs.orient, theirs.row + 4, theirs.col}
	assertPieceOnStream(t, snap, 1, shifted)
	own := cellSet(pieceCells(shifted.pt, shifted.orient, shifted.row, shifted.col))
	for at, m := range snap {
		if m.wc.A && !own[at] {
			t.Errorf("stray active cell %v: %+v", at, m.wc)
		}
	}
}

// A raise lifts every falling piece clear of the risen stack, and the lifts
// cascade: ours, resting on the floor, rises into the cells the teammate's
// piece right above it leaves. Those cells came twice in the batch — taken
// by our piece, vacated by theirs — and the empty one, ordered last, either
// failed its expectation (the raise refused, every time) or won (our piece
// on the wire short of its top row). One write a cell: both pieces whole.
func TestGarbageLiftsAPieceIntoAnothersCells(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := pipelineTestGame(t, ctx, pubPessimistic)
	g.mode, g.holes = modeTeams, 1
	g.mu.Lock()
	h := g.height()
	ours := active{1 /*O*/, 0, h - 2, 4}   // resting on the floor
	theirs := active{1 /*O*/, 0, h - 4, 4} // right above it
	g.piece = &ours
	g.othersPiece[1] = theirs
	seedBoard(t, ctx, g)
	g.garbageOwed = 1
	g.mu.Unlock()

	g.applyOwedGarbage(ctx)

	g.mu.Lock()
	defer g.mu.Unlock()
	if g.txnApplied != 1 {
		t.Fatalf("rows applied: %d, want 1 (the raise did not commit)", g.txnApplied)
	}
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertPieceOnStream(t, snap, 0, active{ours.pt, ours.orient, ours.row - 1, ours.col})
	assertPieceOnStream(t, snap, 1, active{theirs.pt, theirs.orient, theirs.row - 1, theirs.col})
	active := 0
	for _, m := range snap {
		if m.wc.A {
			active++
		}
	}
	if active != 8 {
		t.Errorf("%d active cell(s) on the stream, want the two pieces' 8", active)
	}
}

// The fold itself: one update a cell, in the order the cells first came; a
// write beats a vacate whichever came first, and a guard on either is kept.
func TestMergeCellsOneWriteACell(t *testing.T) {
	piece, stack := wireCell{T: 2, A: true, Pi: 1}, wireCell{O: true, T: 1}
	out := mergeCells([]cellUpd{
		{at: cell{3, 3}},                         // emptied by the settled cells' diff…
		{at: cell{5, 5}, c: &stack},              // (a cell written once is left alone)
		{at: cell{3, 3}, c: &piece, guard: true}, // …and taken by the shifted piece
		{at: cell{4, 4}, c: &piece},              // taken by one piece…
		{at: cell{4, 4}, guard: true},            // …and vacated by the one that left it
	})
	want := []cellUpd{
		{at: cell{3, 3}, c: &piece, guard: true},
		{at: cell{5, 5}, c: &stack},
		{at: cell{4, 4}, c: &piece, guard: true},
	}
	if len(out) != len(want) {
		t.Fatalf("%d update(s), want %d: %+v", len(out), len(want), out)
	}
	for i, w := range want {
		if out[i].at != w.at || out[i].c != w.c || out[i].guard != w.guard {
			t.Errorf("update %d: %+v, want %+v", i, out[i], w)
		}
	}
}
