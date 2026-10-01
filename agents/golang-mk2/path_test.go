package main

import (
	"testing"
)

// pathTestGame is a crew's board of two seats (14 columns) with no server.
func pathTestGame() *Game {
	g := sweepTestGame(2)
	g.roster = []playerSummary{{PlayerID: "me", Seat: 0}, {PlayerID: "peer", Seat: 1}}
	g.spawnC = g.spawnColumn(g.presentSlots(g.roster))
	return g
}

func lockColumn(g *Game, col, fromRow int) {
	for r := fromRow; r < g.height(); r++ {
		g.locked[cell{r, col}] = wireCell{O: true, T: 1}
	}
}

func hasMove(moves []pathMove, m pathMove) bool {
	for _, x := range moves {
		if x == m {
			return true
		}
	}
	return false
}

// replayPath is every state the moves take the piece through, the start
// left out.
func replayPath(p active, moves []pathMove) []pathState {
	s := pathState{p.row, p.col, p.orient}
	var out []pathState
	for _, m := range moves {
		switch m {
		case moveLeft:
			s.col--
		case moveRight:
			s.col++
		case moveDown:
			s.row++
		case moveCW:
			s.orient = (s.orient + 1) & 3
		}
		out = append(out, s)
	}
	return out
}

// On a free board the path is the rotations and the shifts, no more, and
// it ends at the goal on the row it started.
func TestPathReachesTheGoalOnAFreeBoard(t *testing.T) {
	g := pathTestGame()
	p := active{2 /*T*/, 0, spawnRow, g.spawnC}
	res := g.findPath(p, placement{orient: 1, col: g.spawnC + 3}, nil)
	if !res.reached || res.end != (active{2, 1, spawnRow, g.spawnC + 3}) {
		t.Fatalf("free board: reached %v, end %+v", res.reached, res.end)
	}
	if len(res.moves) != 4 || res.downs != 0 || hasMove(res.moves, moveDown) {
		t.Errorf("moves %v (downs %d), want one turn and three shifts", res.moves, res.downs)
	}
}

// A goal the stack walls off is out of reach for good: not transient, and
// the waiting spot is the reachable state nearest it.
func TestPathStoppedByTheStack(t *testing.T) {
	g := pathTestGame()
	lockColumn(g, 8, 0)
	p := active{1 /*O*/, 0, spawnRow, g.spawnC}
	res := g.findPath(p, placement{orient: 0, col: 11}, nil)
	if res.reached || res.blockedByTransient {
		t.Fatalf("walled goal: reached %v transient %v", res.reached, res.blockedByTransient)
	}
	if res.end.col != 6 {
		t.Errorf("waiting spot at col %d, want 6 (cells 6-7, the wall at 8)", res.end.col)
	}
	// Boxed in on every side: nowhere to go at all.
	g = pathTestGame()
	lockColumn(g, g.spawnC-1, 0)
	lockColumn(g, g.spawnC+2, 0)
	for c := g.spawnC; c < g.spawnC+2; c++ {
		g.locked[cell{spawnRow + 2, c}] = wireCell{O: true, T: 1}
	}
	res = g.findPath(p, placement{orient: 0, col: 11}, nil)
	if res.reached || res.end != p || len(res.moves) != 0 || res.blockedByTransient {
		t.Errorf("boxed in: %+v", res)
	}
}

// A crewmate's falling piece across the way, with no corridor round it, is
// a transient block: the path waits as near the goal as it can get.
func TestPathBlockedByATeammatesPiece(t *testing.T) {
	g := pathTestGame()
	theirs := active{0 /*I*/, 1, 0, 6} // vertical I in column 7 (rows 0-3)…
	for _, c := range pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col) {
		g.othersAct[c] = 1
	}
	g.othersPiece[1] = theirs
	wall := pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col)[0].c
	lockColumn(g, wall, 4) // …on a stack filling the column below it
	p := active{1 /*O*/, 0, spawnRow, g.spawnC}
	res := g.findPath(p, placement{orient: 0, col: 11}, nil)
	if res.reached || !res.blockedByTransient {
		t.Fatalf("blocked by a piece: reached %v transient %v", res.reached, res.blockedByTransient)
	}
	if res.end.col != wall-2 {
		t.Errorf("waiting spot at col %d, want %d (right up against the piece)", res.end.col, wall-2)
	}
}

// A teammate about to spawn: the path ducks under their spawn box rather
// than crossing it.
func TestPathKeepsOutOfAnImminentSpawnBox(t *testing.T) {
	g := pathTestGame()
	boxes := g.teammateBoxes()
	if len(boxes) != 1 || !boxes[0].imminent || boxes[0].seat != 1 {
		t.Fatalf("teammate boxes: %+v", boxes)
	}
	p := active{0 /*I*/, 0, spawnRow, g.spawnC}
	res := g.findPath(p, placement{orient: 0, col: 10}, boxes)
	if !res.reached {
		t.Fatalf("goal not reached: %+v", res)
	}
	if !hasMove(res.moves, moveDown) || res.downs == 0 {
		t.Errorf("path did not duck under the box: %+v", res)
	}
	for _, s := range replayPath(p, res.moves) {
		if boxes[0].covers(s.cells(p.pt)) {
			t.Errorf("path crossed the box at %+v: %+v", s, res)
		}
	}
	// With the teammate's piece on the board the box is not imminent, and
	// the straight walk is the path.
	g.othersPiece[1] = active{2, 0, 10, 8}
	res = g.findPath(p, placement{orient: 0, col: 10}, g.teammateBoxes())
	if !res.reached || res.downs != 0 {
		t.Errorf("box not imminent, yet the path went down: %+v", res)
	}
}

// Waiting for a crewmate's piece happens outside a teammate's imminent
// spawn box, even where that is further from the goal.
func TestWaitingSpotStaysOutOfTheSpawnBox(t *testing.T) {
	g := pathTestGame()
	theirs := active{0 /*I*/, 1, 0, 8} // vertical I in column 9, rows 0-3
	for _, c := range pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col) {
		g.othersAct[c] = 2
	}
	g.othersPiece[2] = theirs
	wall := pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col)[0].c
	lockColumn(g, wall, 4)
	boxes := []spawnBox{{seat: 1, c0: 5, c1: 8, imminent: true}}
	p := active{1 /*O*/, 0, spawnRow, 3}
	res := g.findPath(p, placement{orient: 0, col: 11}, boxes)
	if res.reached || !res.blockedByTransient {
		t.Fatalf("blocked by a piece: %+v", res)
	}
	if boxes[0].covers(pieceCells(p.pt, res.end.orient, res.end.row, res.end.col)) {
		t.Errorf("waiting spot %+v is inside the teammate's spawn box", res.end)
	}
	if res.end.col != wall-2 || res.end.row < spawnRow+2 {
		t.Errorf("waiting spot %+v, want the column against the piece, below the box", res.end)
	}
}
