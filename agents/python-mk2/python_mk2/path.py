"""The path a piece takes to its planned column and orientation (execute).

A search over the moves the game allows (a shift left or right, a row down, a
quarter turn in place: the move set the planner's enumeration assumes, no
kicks) on the local board, where the stack and the other players' falling
pieces are both obstacles (can_move) but only the stack is for good. The
search is Dijkstra's over the states (row, col, orient); a move costs 1, and
one that puts the piece in a teammate's spawn box while they are about to
spawn costs PATH_BOX_COST more, so the path keeps out of the box where it can
(below it, most often). Only the diff from where the piece stands to where
the path ends goes on the wire: the contract lets a batch carry a whole
validated path (guide §4.3), so a walk of any length is one round trip, and
the lock that follows can ride the same batch (execute). When the goal is out
of reach (a crewmate's piece across the way) the search still says where to
wait: the reached state nearest the goal outside every teammate's spawn box
(waiting_spot), so a deferred spawn of theirs is never ours to cause.

The search is a flood (Search): with a goal it stops at the first state that
meets it; without one it reaches everything there is to reach, which is what
prices the walk to every placement at once (coord.route_costs). The other
players' pieces are given to it as cells by seat (the live ones, or a
forecast of them), so it can say whose piece stood in the way.
"""

from dataclasses import dataclass, field
from typing import NamedTuple

from .pieces import SPAWN_ROW, piece_cells

MOVE_LEFT, MOVE_RIGHT, MOVE_DOWN, MOVE_CW = range(4)

# What a move into a teammate's imminent spawn box costs on top of the move
# itself: two rows down and back across is cheaper than a step through the
# box.
PATH_BOX_COST = 6


class Active(NamedTuple):
    """A falling piece: its type, orientation and anchor."""

    pt: int
    orient: int
    row: int
    col: int

    def cells(self):
        return piece_cells(self.pt, self.orient, self.row, self.col)


@dataclass
class SpawnBox:
    """A teammate's spawn box: the columns their next piece appears in, on
    the spawn rows, and whether that spawn is imminent (they hold no piece on
    the board, or their claim says the one they held has locked)."""

    seat: int = 0
    c0: int = 0
    c1: int = 0
    imminent: bool = False

    def covers(self, cells):
        """Whether any of the cells is inside the box."""
        for r, c in cells:
            if SPAWN_ROW <= r <= SPAWN_ROW + 1 and self.c0 <= c <= self.c1:
                return True
        return False


@dataclass
class PathResult:
    """What the search found: where the path ends (the goal, or the waiting
    spot), whether that is the goal, the moves that get there, how many of
    them were rows soft-dropped, and whether a crewmate's falling piece was
    what stood between the piece and any state it could not reach, and whose
    (blockers, the seats, sorted)."""

    end: Active
    reached: bool = False
    moves: list = field(default_factory=list)
    downs: int = 0
    blocked_by_transient: bool = False
    blockers: list = field(default_factory=list)


# The search's queue: a binary heap ordered by cost alone, sifted exactly as
# golang-mk2's is, so that among paths of equal cost the two agents take the
# same one.


def _push(h, x):
    h.append(x)
    j = len(h) - 1
    while j > 0:
        i = (j - 1) // 2
        if not h[j][0] < h[i][0]:
            break
        h[i], h[j] = h[j], h[i]
        j = i


def _pop(h):
    n = len(h) - 1
    h[0], h[n] = h[n], h[0]
    i = 0
    while True:
        j = 2 * i + 1
        if j >= n:
            break
        if j + 1 < n and h[j + 1][0] < h[j][0]:
            j += 1
        if not h[j][0] < h[i][0]:
            break
        h[i], h[j] = h[j], h[i]
        i = j
    return h.pop()


def rot_dist(frm, to):
    return (to - frm) & 3


class Search:
    """One Dijkstra flood from where a piece stands: every state it reached,
    at what cost and by which move, and the seats whose falling pieces
    stopped it somewhere (blockers): the crewmates in its way."""

    __slots__ = ("pt", "start", "dist", "prev", "via", "closed", "blockers", "found")

    def __init__(self, pt, start):
        self.pt, self.start = pt, start
        self.dist = {start: 0.0}
        self.prev = {}
        self.via = {}
        self.closed = set()
        self.blockers = set()
        self.found = None

    def reach(self):
        """The cost of the cheapest way to every (orientation, column) the
        flood reached, at whatever row: what a placement's walk costs."""
        out = {}
        dist = self.dist
        for st in self.closed:
            k = (st[2], st[1])
            d = dist[st]
            if k not in out or d < out[k]:
                out[k] = d
        return out

    def blocker_seats(self):
        """The seats whose pieces stopped the flood, sorted."""
        return sorted(self.blockers)

    def result(self, end, reached):
        """The path to end (a state the flood reached), as moves."""
        moves = []
        downs = 0
        st = end
        while st != self.start:
            m = self.via[st]
            moves.append(m)
            if m == MOVE_DOWN:
                downs += 1
            st = self.prev[st]
        moves.reverse()
        return PathResult(end=Active(self.pt, end[2], end[0], end[1]), reached=reached, moves=moves, downs=downs,
                          blocked_by_transient=bool(self.blockers), blockers=self.blocker_seats())


def flood(can_place, p, boxes, others, goal=None):
    """Search from p over the moves the game allows, the stack an obstacle
    for good (can_place(cells) is true where only a falling piece, not the
    stack or the walls, could be in the way) and `others` (cells held by
    other players' falling pieces, by seat: the live ones, or a forecast of
    them, coord.py) obstacles in passing. With a goal, goal(state) on (row,
    col, orient), it stops at the first state that meets it (found); without
    one it reaches everything there is to reach."""
    pt = p.pt
    start = (p.row, p.col, p.orient)
    live = [b for b in boxes if b.imminent]
    s = Search(pt, start)
    dist, prev, via, closed, blockers = s.dist, s.prev, s.via, s.closed, s.blockers
    heap = [(0.0, start)]
    while heap:
        cost, st = _pop(heap)
        if st in closed or cost > dist[st]:
            continue
        closed.add(st)
        if goal is not None and goal(st):
            s.found = st
            break
        row, col, orient = st
        for m, nxt in ((MOVE_LEFT, (row, col - 1, orient)), (MOVE_RIGHT, (row, col + 1, orient)),
                       (MOVE_DOWN, (row + 1, col, orient)), (MOVE_CW, (row, col, (orient + 1) & 3))):
            cs = piece_cells(pt, nxt[2], nxt[0], nxt[1])
            if not can_place(cs):
                continue
            hit = False
            for c in cs:
                seat = others.get(c)
                if seat is not None:
                    blockers.add(seat)  # only a falling piece stands there
                    hit = True
            if hit:
                continue
            ncost = cost + 1
            for b in live:
                if b.covers(cs):
                    ncost += PATH_BOX_COST
                    break
            d = dist.get(nxt)
            if d is not None and d <= ncost:
                continue
            dist[nxt] = ncost
            prev[nxt] = st
            via[nxt] = m
            _push(heap, (ncost, nxt))
    return s


def find_path(can_place, others, p, goal_col, goal_orient, boxes, keep=None):
    """Search from p to the plan's orientation and column, at whatever row
    the search reaches them: gravity and the hard drop are the caller's, and
    the planner's placements are straight drops from above. keep is cells a
    wait must stay out of where it can (the drop shafts of the claims with
    right of way over ours, coord.keep_out): a path may cross them, a waiting
    spot is chosen outside them."""
    s = flood(can_place, p, boxes, others, lambda st: st[1] == goal_col and st[2] == goal_orient)
    if s.found is not None:
        return s.result(s.found, True)
    spot = waiting_spot(s.closed, s.dist, s.start, goal_col, goal_orient, boxes, keep, p.pt)
    return s.result(s.start if spot is None else spot, False)


def waiting_spot(closed, dist, start, goal_col, goal_orient, boxes, keep, pt):
    """Where to wait when the goal is out of reach: among the states the
    search reached, the one nearest the goal (columns and quarter turns to
    go) outside every imminent teammate spawn box when any such state exists,
    and outside the cells to keep out of (keep) when any of those does, the
    cheaper path breaking ties (and, between equals, the state's own order:
    row, column, orientation). None when the start is the best."""
    keep = keep or ()
    best = None
    best_key = None
    for s in sorted(closed):
        cs = piece_cells(pt, s[2], s[0], s[1])
        # Trouble ranks a spot: in a spawn box is worse than in a shaft, and
        # either is worse than neither.
        trouble = 2 if any(b.imminent and b.covers(cs) for b in boxes) else 0
        if keep and any(c in keep for c in cs):
            trouble += 1
        key = (trouble, abs(s[1] - goal_col) + rot_dist(s[2], goal_orient), dist[s])
        if best_key is None or key < best_key:
            best, best_key = s, key
    if best is None or best == start:
        return None
    return best
