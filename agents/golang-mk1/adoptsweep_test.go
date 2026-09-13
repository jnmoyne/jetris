package main

import "testing"

// A crew game's seat 1 on a board of its own, its cells folded off the
// stream by hand.
func crewSeatGame() *Game {
	g := newGame(&Agent{name: "me"}, "g", 1)
	g.mode, g.w, g.h = modeCooperative, 14, 24
	return g
}

// foldPiece folds p's cells, or the given ones of them, as seat g.idx's
// active cells at fresh sequences.
func foldPiece(g *Game, seq *uint64, p active, cells ...cell) {
	if len(cells) == 0 {
		cs := pieceCells(p.pt, p.orient, p.row, p.col)
		cells = cs[:]
	}
	for _, c := range cells {
		*seq++
		g.foldCell(c, wireCell{T: p.pt, A: true, R: p.orient, Ar: p.row, Ac: p.col, Pi: g.idx}, *seq)
	}
}

// TestAdoptOwnPieceWholeOnly pins the adoption rule. A committed transform
// of a crewmate's (a clear shifting it down) moves our piece: we adopt the
// anchor it put our piece at — once the stream holds all four of its cells.
// Never from a part of a piece: a stale collapse of an older client's once
// copied three cells of our piece beside its live four (jetris-eu,
// 2026-09-12, game 0ca68b24); adopting the copy orphaned the piece, and its
// four cells sat on the board as a ghost for the rest of the game. Whatever
// an adoption leaves behind is a stray, for the next write to sweep. And
// nothing is adopted while our own batches are in flight.
func TestAdoptOwnPieceWholeOnly(t *testing.T) {
	g := crewSeatGame()
	var seq uint64
	a := active{pt: 2, orient: 0, row: 5, col: 4}
	foldPiece(g, &seq, a)
	if g.piece == nil || *g.piece != a {
		t.Fatalf("our piece off the stream: %+v, want %+v", g.piece, a)
	}

	// Three cells of another anchor: a part of a piece, not adopted — and
	// strays, all three.
	b := active{pt: 2, orient: 0, row: 8, col: 4}
	bc := pieceCells(b.pt, b.orient, b.row, b.col)
	foldPiece(g, &seq, b, bc[:3]...)
	if *g.piece != a {
		t.Fatalf("adopted a piece in part: %+v", g.piece)
	}
	if strays := g.strayOwnCells(); len(strays) != 3 {
		t.Fatalf("strays with a piece in part on the stream: %v, want its three cells", strays)
	}

	// The fourth: the piece is whole there, and adopted; the old anchor's
	// cells are the strays now.
	foldPiece(g, &seq, b, bc[3])
	if *g.piece != b {
		t.Fatalf("a whole piece at a new anchor not adopted: %+v, want %+v", g.piece, b)
	}
	strays := g.strayOwnCells()
	ac := pieceCells(a.pt, a.orient, a.row, a.col)
	if len(strays) != 4 {
		t.Fatalf("strays after the adoption: %v, want the old anchor's four cells %v", strays, ac)
	}
	for _, c := range ac {
		found := false
		for _, s := range strays {
			found = found || s == c
		}
		if !found {
			t.Fatalf("old cell %v is not a stray: %v", c, strays)
		}
	}

	// A batch of ours in flight: the echo is the stream catching up to our
	// projection, nothing is adopted off it.
	g.inflight = 1
	c := active{pt: 2, orient: 0, row: 11, col: 4}
	foldPiece(g, &seq, c)
	if *g.piece != b {
		t.Fatalf("adopted with a batch in flight: %+v", g.piece)
	}
}

// TestMoveBatchSweepsStrays: a write of the piece vacates the cells it
// leaves — the piece's old cells AND every stray of ours on the stream —
// in the one batch that moves the piece.
func TestMoveBatchSweepsStrays(t *testing.T) {
	g := crewSeatGame()
	var seq uint64
	a := active{pt: 2, orient: 0, row: 5, col: 4}
	b := active{pt: 2, orient: 0, row: 8, col: 4}
	foldPiece(g, &seq, a)
	foldPiece(g, &seq, b)
	if *g.piece != b {
		t.Fatalf("our piece: %+v, want %+v", g.piece, b)
	}

	to := b
	to.col--
	cells, old := g.moveBatch(to)
	vacated := map[cell]bool{}
	written := map[cell]bool{}
	for _, u := range cells {
		if u.c == nil {
			vacated[u.at] = true
		} else if u.c.A && u.c.Ar == to.row && u.c.Ac == to.col {
			written[u.at] = true
		}
	}
	for _, c := range pieceCells(to.pt, to.orient, to.row, to.col) {
		if !written[c] {
			t.Fatalf("the move does not write %v", c)
		}
	}
	newSet := cellSet(pieceCells(to.pt, to.orient, to.row, to.col))
	for _, c := range pieceCells(b.pt, b.orient, b.row, b.col) {
		if !newSet[c] && !vacated[c] {
			t.Fatalf("the move leaves the piece's old cell %v", c)
		}
	}
	for _, c := range pieceCells(a.pt, a.orient, a.row, a.col) {
		if !vacated[c] {
			t.Fatalf("the move leaves the stray %v", c)
		}
	}
	if len(old) != 8 {
		t.Fatalf("the cells the move leaves (what flashes if it is lost): %d, want the piece's four and the four strays", len(old))
	}
}
