package main

// The path a piece takes to its planned column and orientation (execute):
// a search over the moves the game allows — a shift left or right, a row
// down, a quarter turn in place (the move set the planner's enumerate
// assumes: no kicks) — on the local board, where the stack and the other
// players' falling pieces are both obstacles (canMove) but only the stack
// is for good. The search is Dijkstra's over the states (row, col,
// orient); a move costs 1, and one that puts the piece in a teammate's
// spawn box while they are about to spawn costs pathBoxCost more, so the
// path keeps out of the box where it can — below it, most often. Only the
// diff from where the piece stands to where the path ends goes on the wire
// (moveBatch): the contract lets a batch carry a whole validated path
// (guide §4.3), so a walk of any length is one round trip, and the lock
// that follows can ride the same batch (execute). When the goal is out of
// reach — a crewmate's piece across the way — the search still says where
// to wait: the reached state nearest the goal outside every teammate's
// spawn box (waitingSpot), so a deferred spawn of theirs is never ours to
// cause. golang-mk1 walked the spawn row alone and waited wherever the
// obstacle stopped it, a teammate's spawn box included.

import (
	"container/heap"
	"math"
	"sort"
)

type pathMove uint8

const (
	moveLeft pathMove = iota
	moveRight
	moveDown
	moveCW
)

// pathBoxCost is what a move into a teammate's imminent spawn box costs on
// top of the move itself: two rows down and back across is cheaper than a
// step through the box.
const pathBoxCost = 6

// spawnBox is a teammate's spawn box: the columns their next piece appears
// in, on the spawn rows, and whether that spawn is imminent — they hold no
// piece on the board, or their claim says the one they held has locked.
type spawnBox struct {
	seat     int
	c0, c1   int
	imminent bool
}

// covers reports whether any of the cells is inside the box.
func (b spawnBox) covers(cs [4]cell) bool {
	for _, c := range cs {
		if c.r >= spawnRow && c.r <= spawnRow+1 && c.c >= b.c0 && c.c <= b.c1 {
			return true
		}
	}
	return false
}

// pathState is one position of the piece.
type pathState struct{ row, col, orient int }

// pathResult is what the search found: where the path ends (the goal, or
// the waiting spot), whether that is the goal, the moves that get there,
// how many of them were rows soft-dropped, and whether a crewmate's
// falling piece was what stood between the piece and any state it could
// not reach — and whose (blockers, the seats, sorted).
type pathResult struct {
	end                active
	reached            bool
	moves              []pathMove
	downs              int
	blockedByTransient bool
	blockers           []int
}

// pathNode is a heap entry of the search.
type pathNode struct {
	st   pathState
	cost float64
}

type pathHeap []pathNode

func (h pathHeap) Len() int              { return len(h) }
func (h pathHeap) Less(i, j int) bool    { return h[i].cost < h[j].cost }
func (h pathHeap) Swap(i, j int)         { h[i], h[j] = h[j], h[i] }
func (h *pathHeap) Push(x any)           { *h = append(*h, x.(pathNode)) }
func (h *pathHeap) Pop() any             { old := *h; n := len(old); x := old[n-1]; *h = old[:n-1]; return x }
func rotDist(from, to int) int           { return (to - from) & 3 }
func (s pathState) cells(pt int) [4]cell { return pieceCells(pt, s.orient, s.row, s.col) }

// search is one Dijkstra flood from where a piece stands: every state it
// reached, at what cost and by which move, and the seats whose falling
// pieces stopped it somewhere (blockers) — the crewmates in its way.
type search struct {
	pt       int
	start    pathState
	dist     map[pathState]float64
	prev     map[pathState]pathState
	via      map[pathState]pathMove
	closed   map[pathState]bool
	blockers map[int]bool
	found    *pathState
}

// flood searches from p over the moves the game allows, the stack an
// obstacle for good and `others` — cells held by other players' falling
// pieces, by seat: the live ones (g.othersAct), or a forecast of them
// (coord.go) — obstacles in passing. With a goal it stops at the first
// state that meets it (found); without one it reaches everything there is
// to reach. Call with g.mu held.
func (g *Game) flood(p active, boxes []spawnBox, others map[cell]int, goal func(pathState) bool) *search {
	start := pathState{p.row, p.col, p.orient}
	s := &search{pt: p.pt, start: start, dist: map[pathState]float64{start: 0}, prev: map[pathState]pathState{},
		via: map[pathState]pathMove{}, closed: map[pathState]bool{}, blockers: map[int]bool{}}
	h := &pathHeap{{start, 0}}
	for h.Len() > 0 {
		n := heap.Pop(h).(pathNode)
		if s.closed[n.st] || n.cost > s.dist[n.st] {
			continue
		}
		s.closed[n.st] = true
		if goal != nil && goal(n.st) {
			st := n.st
			s.found = &st
			break
		}
		for _, m := range []pathMove{moveLeft, moveRight, moveDown, moveCW} {
			next := n.st
			switch m {
			case moveLeft:
				next.col--
			case moveRight:
				next.col++
			case moveDown:
				next.row++
			case moveCW:
				next.orient = (next.orient + 1) & 3
			}
			cs := next.cells(p.pt)
			if !g.canPlace(cs) {
				continue
			}
			hit := false
			for _, c := range cs {
				if seat, ok := others[c]; ok {
					s.blockers[seat] = true // only a falling piece stands there
					hit = true
				}
			}
			if hit {
				continue
			}
			cost := n.cost + 1
			for _, b := range boxes {
				if b.imminent && b.covers(cs) {
					cost += pathBoxCost
					break
				}
			}
			if d, ok := s.dist[next]; ok && d <= cost {
				continue
			}
			s.dist[next] = cost
			s.prev[next] = n.st
			s.via[next] = m
			heap.Push(h, pathNode{next, cost})
		}
	}
	return s
}

// reach is the cost of the cheapest way to every (orientation, column) the
// flood reached, at whatever row — what a placement's walk costs.
func (s *search) reach() map[[2]int]float64 {
	out := map[[2]int]float64{}
	for st := range s.closed {
		k := [2]int{st.orient, st.col}
		if d, ok := out[k]; !ok || s.dist[st] < d {
			out[k] = s.dist[st]
		}
	}
	return out
}

// blockerSeats is the seats whose pieces stopped the flood, sorted.
func (s *search) blockerSeats() []int {
	out := make([]int, 0, len(s.blockers))
	for seat := range s.blockers {
		out = append(out, seat)
	}
	sort.Ints(out)
	return out
}

// result is the path to end (a state the flood reached), as moves.
func (s *search) result(end pathState, reached bool) pathResult {
	var moves []pathMove
	downs := 0
	for st := end; st != s.start; st = s.prev[st] {
		moves = append(moves, s.via[st])
		if s.via[st] == moveDown {
			downs++
		}
	}
	for i, j := 0, len(moves)-1; i < j; i, j = i+1, j-1 {
		moves[i], moves[j] = moves[j], moves[i]
	}
	return pathResult{end: active{s.pt, end.orient, end.row, end.col}, reached: reached, moves: moves, downs: downs,
		blockedByTransient: len(s.blockers) > 0, blockers: s.blockerSeats()}
}

// findPath searches from p to the plan's orientation and column — at
// whatever row the search reaches them: gravity and the hard drop are the
// caller's, and the planner's placements are straight drops from above.
// Call with g.mu held.
func (g *Game) findPath(p active, pl placement, boxes []spawnBox) pathResult {
	return g.findPathKeep(p, pl, boxes, nil)
}

// findPathKeep is findPath with cells a wait must keep out of where it can
// (keep: the drop shafts of the claims with right of way over ours,
// coord.go keepOut) — a path may cross them, a waiting spot is chosen
// outside them. Call with g.mu held.
func (g *Game) findPathKeep(p active, pl placement, boxes []spawnBox, keep map[cell]bool) pathResult {
	s := g.flood(p, boxes, g.othersAct, func(st pathState) bool { return st.col == pl.col && st.orient == pl.orient })
	if s.found != nil {
		return s.result(*s.found, true)
	}
	end := s.start
	if spot, ok := waitingSpot(s.closed, s.dist, s.start, pl, boxes, keep, p.pt); ok {
		end = spot
	}
	return s.result(end, false)
}

// waitingSpot picks where to wait when the goal is out of reach: among the
// states the search reached, the one nearest the goal — columns and quarter
// turns to go — outside every imminent teammate spawn box when any such
// state exists, and outside the cells to keep out of (keep) when any of
// those does, the cheaper path breaking ties. Nothing to move to when the
// start is the best.
func waitingSpot(closed map[pathState]bool, dist map[pathState]float64, start pathState, pl placement, boxes []spawnBox, keep map[cell]bool, pt int) (pathState, bool) {
	// trouble ranks a spot: in a spawn box is worse than in a shaft, and
	// either is worse than neither.
	trouble := func(s pathState) int {
		cs := s.cells(pt)
		n := 0
		for _, b := range boxes {
			if b.imminent && b.covers(cs) {
				n += 2
				break
			}
		}
		for _, c := range cs {
			if keep[c] {
				n++
				break
			}
		}
		return n
	}
	score := func(s pathState) float64 {
		return math.Abs(float64(s.col-pl.col)) + float64(rotDist(s.orient, pl.orient))
	}
	var best pathState
	bestTrouble, bestScore, bestCost, have := 0, math.Inf(1), math.Inf(1), false
	for s := range closed {
		tr, sc, c := trouble(s), score(s), dist[s]
		switch {
		case !have, tr < bestTrouble,
			tr == bestTrouble && (sc < bestScore || (sc == bestScore && (c < bestCost || (c == bestCost && lessState(s, best))))):
			best, bestTrouble, bestScore, bestCost, have = s, tr, sc, c, true
		}
	}
	if !have || best == start {
		return start, false
	}
	return best, true
}

// lessState orders two states, so that a tie between waiting spots is
// broken the same way every time (map order is not an order).
func lessState(a, b pathState) bool {
	if a.row != b.row {
		return a.row < b.row
	}
	if a.col != b.col {
		return a.col < b.col
	}
	return a.orient < b.orient
}

// teammateBoxes is every other seat's spawn box on our playfield, among the
// seats present (spawnColumnFor), imminent when that seat holds no piece on
// the board. Call with g.mu held.
func (g *Game) teammateBoxes() []spawnBox {
	if !g.shared() || g.seatsOnPF <= 1 {
		return nil
	}
	present := g.presentSlots(g.roster)
	if len(present) == 0 {
		for s := 0; s < g.seatsOnPF; s++ {
			present = append(present, s)
		}
	}
	var out []spawnBox
	seen := map[int]bool{}
	for _, slot := range present {
		if slot == g.mySlot() || seen[slot] || slot < 0 || slot >= g.seatsOnPF {
			continue
		}
		seen[slot] = true
		seat := slot
		if g.mode == modeTeams {
			seat = g.team*g.teamSize() + slot
		}
		c0 := g.spawnColumnFor(slot, present)
		_, holds := g.othersPiece[seat]
		out = append(out, spawnBox{seat: seat, c0: c0, c1: c0 + 3, imminent: !holds || g.seatAboutToSpawn(seat)})
	}
	return out
}
