"""Coordination on a shared board: what the agent does with its teammates'
claims on the blackboard (blackboard.py): plans on them, yields to the
earlier ones, waits for the ones it depends on, and when a plan is no longer
the thing to do (request_replan). The blackboard's own plumbing is
blackboard.py; the planner's projection is planner.py's.
"""

from dataclasses import dataclass, field

from .blackboard import PHASE_LOCKED, PHASE_PLANNED
from .consts import MODE_TEAMS
from .engine import GRID_CLAIMED, Grid
from .path import Active, SpawnBox, find_path
from .pieces import SPAWN_ROW, piece_cells
from .planner import Projection

# Why the piece in play is re-planned from where it stands (play_pieces
# re-plans after a RETHINK_DELAY instead of the full think pause).
(REPLAN_NONE,
 REPLAN_OVERLAP,        # an earlier claim took our spot (plan's linearization)
 REPLAN_DROP_ROW,       # the plan's landing is no longer what the board gives: a teammate's lock, clear or collapse changed it
 REPLAN_DEP_TIMEOUT,    # the earlier claim we waited on never landed
 REPLAN_COLLAPSE,       # a peer's line clear collapsed rows around our piece
 REPLAN_STALE,          # no progress for three seconds
 REPLAN_UNREACHABLE,    # no path to the plan, and nowhere better to wait (the stack moved)
 REPLAN_LOCK_DROPPED,   # the CAS lock was dropped after its merge-retry
 REPLAN_BB_RESET,       # the blackboard watch was re-established on a recreated bucket
 ) = range(9)

_REPLAN_NAMES = ("none", "overlap", "droprow", "deptimeout", "collapse", "stale", "unreachable", "lockdropped",
                 "bbreset")


def replan_name(r):
    return _REPLAN_NAMES[r] if 0 <= r < len(_REPLAN_NAMES) else "none"


# DEP_WAIT_MIN and DEP_WAIT_TIMEOUT bound how long a drop is held for an
# earlier claim to land before the piece is re-planned without it
# (dep_timeout: twice the claim's own estimate, between the two).
DEP_WAIT_MIN = 0.3
DEP_WAIT_TIMEOUT = 1.5
# A re-plan's think pause: the piece is on the board already and the
# difficulty's pause has been spent on it, so a re-plan costs the plan alone.
RETHINK_DELAY = 0.0
# The grace a piece resting on the stack AWAY from its planned column gets on
# a shared board before it locks where it stands: a crewmate's piece across
# its path falls away within it, and the walk resumes.
LOCK_DELAY = 0.5

# What check_plan says of the plan on the live board.
(PLAN_OK,
 PLAN_WAIT_DEP,   # the landing is not there yet: an earlier claim we wait on will put it there
 PLAN_STALE,      # the landing is taken, or gone
 ) = range(3)

# classify's verdict on a placement against the claims made before ours.
(NO_CONFLICT,
 CONFLICT_DEPENDENCY,   # ordered after one or more earlier claims: keep it, wait for them
 CONFLICT_OVERLAP,      # an earlier claim's landing meets ours: yield
 ) = range(3)


@dataclass
class DepWait:
    """One claim the piece in play must let land first: the seat's, for the
    piece and at the revision it was made (a newer claim of theirs, the lock
    or a re-plan, ends the wait; so does every cell of the landing settling
    on the board)."""

    seat: int
    piece: int = 0
    rev: int = 0
    cells: list = field(default_factory=list)


def stamp_claimed(gr, cells):
    """Mark a landing on the grid as a teammate's."""
    for r, c in cells:
        if 0 <= r < gr.h and 0 <= c < gr.w:
            gr.set(r, c, GRID_CLAIMED)


def columns_meet(p, cols):
    """Whether any cell of the piece is in one of the columns."""
    return any(c in cols for _, c in p.cells())


class CoordMixin:
    def request_replan(self, r):
        """Ask play_pieces to re-plan the piece from where it stands, the
        first reason winning; every request is counted. Caller holds mu."""
        if self.replan == REPLAN_NONE:
            self.replan = r
        self.trace("re-plan requested: %s", replan_name(r))
        self.st.note_replan(replan_name(r))

    def check_plan(self, pl):
        """Whether the plan's landing still stands on the live board: stale
        when the stack has taken any of its cells, or when nothing holds them
        up any more (a collapse took the rows under them), unless an earlier
        claim we wait on is what will hold them up, which is a wait. Caller
        holds mu."""
        if not self.can_place(pl.dest):
            return PLAN_STALE
        h = self.height()
        locked = self.locked
        for r, c in pl.dest:
            if r + 1 >= h or (r + 1, c) in locked:
                return PLAN_OK
        if self.deps_pending():
            return PLAN_WAIT_DEP
        return PLAN_STALE

    def ordered_claims(self):
        """The teammates' claims on our board (every seat the listing still
        holds but ours) by revision, the order everyone sees. Caller holds
        mu."""
        out = [c for seat, c in self.claims.items()
               if seat != self.idx and c.seat == seat and self.seat_present_locked(seat)]
        out.sort(key=lambda c: c.rev)
        return out

    def to_grid_projected(self):
        """The settled board alone: no falling piece of anyone's on it. A
        teammate's piece is going where its claim says (or, silent, where its
        straight drop lands), not staying where it is, and the projection
        stamps that instead (build_projection). Caller holds mu."""
        gr = Grid(self.height(), self.w)
        for (r, c), v in self.locked.items():
            if 0 <= r < gr.h and 0 <= c < gr.w:
                gr.set(r, c, 2 if v.g else 1)
        return gr

    def project_claim(self, gr, c):
        """Where a claim's piece is going to settle on the grid as it stands:
        a planned claim re-dropped by its column and orientation from where
        the piece is (the claim's own cells may be a collapse old), a locked
        claim's cells verbatim. None for a claim the grid cannot take at
        all."""
        if c.phase == PHASE_LOCKED:
            return tuple(c.cells)
        if not 0 <= c.type < 7:
            return None
        frm = SPAWN_ROW
        op = self.others_piece.get(c.seat)
        if op is not None and op.pt == c.type:
            frm = op.row
        if not gr.can_place(piece_cells(c.type, c.rot, frm, c.col)):
            return None
        return piece_cells(c.type, c.rot, gr.drop_row(c.type, c.rot, frm, c.col), c.col)

    def build_projection(self):
        """The board the planner plans on when the board is coordinated: the
        settled cells, plus every teammate's landing as their claim says, in
        revision order, each dropped onto the board the earlier ones made. A
        locked claim adds nothing: its cells are on the board, or reach it
        off the game stream within a moment. A planned claim for a piece the
        seat no longer holds (their board shows another type) is an old one.
        A teammate with a piece on the board and no claim for it (a human, an
        uncoordinated agent, an agent that has not planned it yet) gets a
        nudge, not a wall: their piece's straight drop from where it is goes
        on the projection's soft map, and a placement in it or on top of it
        pays for that (penalty), while the grid stays as it is: nobody knows
        where they are going, and a wall there would keep the first planner
        of a game out of the one gap on the board because the other's piece
        hung above it, unplanned. Caller holds mu."""
        gr = self.to_grid_projected()
        proj = Projection(own_spawn_c=self.spawn_c, boxes=self.teammate_boxes())
        claimed = set()
        for c in self.ordered_claims():
            if c.phase == PHASE_LOCKED:
                # On the board, or a moment from it off the game stream, and
                # its cells in the lock's own frame, which a collapse may
                # have moved since: nothing to stamp. The seat is heard from,
                # though: no ghost for it.
                claimed.add(c.seat)
            elif c.phase == PHASE_PLANNED:
                op = self.others_piece.get(c.seat)
                if op is not None and op.pt != c.type:
                    continue
                if self.ignored_claims.get(c.seat) == c.rev:
                    continue  # waited on past its time: whatever holds it up, we plan without it
                cs = self.project_claim(gr, c)
                if cs is None:
                    continue
                stamp_claimed(gr, cs)
                for x in cs:
                    proj.claimed[x] = c.seat
                proj.dep_eta[c.seat] = c.eta_ms
                proj.pieces[c.seat] = c.piece
                claimed.add(c.seat)
        # Our own piece's cells stay ours: a claim whose landing passes
        # through where the piece stands right now (it has not landed, so
        # the grid has the cells empty) must not make its own position
        # unplaceable. The planner starts from there, and a start it cannot
        # place is "no placement at all", which reads as a top-out.
        if self.piece is not None:
            for r, c in self.piece.cells():
                if 0 <= r < gr.h and 0 <= c < gr.w and gr.at(r, c) == GRID_CLAIMED:
                    gr.set(r, c, 0)
                    proj.claimed.pop((r, c), None)
        for pi in sorted(pi for pi in self.others_piece if pi not in claimed):
            op = self.others_piece[pi]
            if not gr.can_place(op.cells()):
                continue
            for x in piece_cells(op.pt, op.orient, gr.drop_row(op.pt, op.orient, op.row, op.col), op.col):
                proj.soft[x] = pi
        return gr, proj

    def classify(self, p, cand, my_rev):
        """Check our placement (the piece p, planned at cand) against every
        claim made before ours (rev < my_rev), in revision order, each
        dropped onto the board the earlier ones made: an earlier landing that
        meets ours is an overlap, and we yield (taken is then every earlier
        landing's cells, for the next placement to keep clear of); one that
        changes where ours lands, or whose landing ours would change were we
        to land first, orders ours after it: a dependency to wait out
        (DepWait). Claims made after ours are theirs to yield to. A LOCKED
        claim is no claim at all here: its piece is on the board (its cells
        reach us off the game stream, and a landing of ours they have taken
        is check_plan's to find). Returns (kind, deps, taken). Caller holds
        mu."""
        acc = self.to_grid_projected()
        dest = set(cand.dest)
        taken = set()
        deps = []
        kind = NO_CONFLICT
        overlap = False
        mine_start = piece_cells(p.pt, cand.orient, p.row, cand.col)
        for c in self.ordered_claims():
            if c.phase == PHASE_LOCKED or c.rev >= my_rev:
                continue
            if self.ignored_claims.get(c.seat) == c.rev:
                continue
            their = self.project_claim(acc, c)
            if their is None:
                continue
            op = self.others_piece.get(c.seat)
            if op is not None and op.pt != c.type:
                continue
            for x in their:
                taken.add(x)
                if x in dest:
                    overlap = True
            if overlap:
                stamp_claimed(acc, their)
                continue  # the rest of the earlier landings, for taken
            if acc.can_place(mine_start):
                without = acc.drop_row(p.pt, cand.orient, p.row, cand.col)
                with_ = acc.clone()
                stamp_claimed(with_, their)
                with_row = without
                if with_.can_place(mine_start):
                    with_row = with_.drop_row(p.pt, cand.orient, p.row, cand.col)
                mine = acc.clone()
                for r, cc in cand.dest:
                    if 0 <= r < mine.h and 0 <= cc < mine.w:
                        mine.set(r, cc, 1)
                theirs_if_i_first = self.project_claim(mine, c)
                if with_row != without or theirs_if_i_first != their:
                    deps.append(DepWait(seat=c.seat, piece=c.piece, rev=c.rev, cells=list(their)))
                    kind = CONFLICT_DEPENDENCY
            stamp_claimed(acc, their)
        if overlap:
            return CONFLICT_OVERLAP, [], taken
        return kind, deps, taken

    def merge_deps(self, pl, proj, more):
        """The piece's waits: the seats whose claimed landings hold the
        placement up (the projection's wait_on), and what classify ordered it
        after: one wait per seat."""
        out = []
        seen = {self.idx}  # never on ourselves
        for seat in pl.wait_on:
            if seat in seen:
                continue
            d = DepWait(seat=seat, piece=proj.pieces.get(seat, 0))
            c = self.claims.get(seat)
            if c is not None:
                d.rev = c.rev
            d.cells = sorted(x for x, s in proj.claimed.items() if s == seat)
            out.append(d)
            seen.add(seat)
        for d in more:
            if d.seat not in seen:
                out.append(d)
                seen.add(d.seat)
        return out

    def reland_plan(self, p, pl):
        """Re-derive a plan's landing on the stack as it stands: the same
        column and orientation, dropped from where the piece is now. A
        crewmate's collapse moved the piece and the stack alike, but not by
        the same rows where a cleared row lay between the two; the plan keeps
        its intent, the landing follows the board. Caller holds mu."""
        from dataclasses import replace

        if not self.can_place(piece_cells(p.pt, pl.orient, p.row, pl.col)):
            return pl  # the checks that follow will call it stale
        dr = self.drop_row_locked(Active(p.pt, pl.orient, p.row, pl.col))
        return replace(pl, drop_row=dr, dest=piece_cells(p.pt, pl.orient, dr, pl.col))

    def dep_resolved(self, d):
        """Whether a wait is over: their lock, or no claim of theirs at all
        any more, or every cell of the landing settled, or a newer claim of
        theirs for ANOTHER landing (a re-plan: then check_plan says whether
        our landing still stands). A newer claim for the SAME landing keeps
        the wait, under the new revision: a re-plan of theirs that changed
        nothing must not void ours, or two pieces resting on each other's
        claims re-plan each other forever. Caller holds mu."""
        c = self.claims.get(d.seat)
        if c is None:
            return True
        if c.rev != d.rev:
            if c.phase != PHASE_PLANNED or c.piece != d.piece:
                return True
            their = self.project_claim(self.to_grid_projected(), c)
            if their is None or set(their) != set(d.cells) or len(their) != len(d.cells):
                return True
            d.rev = c.rev  # the same landing, claimed again: still coming
        locked = self.locked
        for x in d.cells:
            if x not in locked:
                return False
        return len(d.cells) > 0

    def deps_pending(self):
        """Whether the piece in play still waits on an earlier claim to land.
        Caller holds mu."""
        for d in self.deps:
            if not self.dep_resolved(d):
                return True
        return False

    def dep_timeout(self):
        """How long the piece waits on the claims it depends on: twice what
        the slowest of them said it would take, within DEP_WAIT_MIN and
        DEP_WAIT_TIMEOUT. Caller holds mu."""
        limit = DEP_WAIT_MIN
        for d in self.deps:
            if self.dep_resolved(d):
                continue
            c = self.claims.get(d.seat)
            if c is not None:
                limit = max(limit, 2 * c.eta_ms / 1000)
        return min(limit, DEP_WAIT_TIMEOUT)

    def await_deps(self, now):
        """Account a turn of waiting on the claims the piece depends on and
        report whether the wait has run out (dep_timeout): then the claims
        still pending are ignored by the re-plan that follows
        (ignored_claims). Whatever holds them up, the piece goes on without
        them, and its landing is theirs to work around. Caller holds mu."""
        if self.dep_since is None:
            self.dep_since = now
            self.st.dep_waits += 1
        if now - self.dep_since > self.dep_timeout():
            for d in self.deps:
                if not self.dep_resolved(d):
                    self.ignored_claims[d.seat] = d.rev
            self.end_dep_wait(now)
            return True
        return False

    def think_pause(self):
        """The difficulty's think pause for a fresh piece, jittered by up to
        a third on a coordinated board, so crewmates of one difficulty do not
        plan in lockstep: three agents planning at the same instant see none
        of each other's claims and all take the same spot, and the later two
        yield every time."""
        d = self.a.tn.piece_delay
        if self.coord is None or self.a.rng is None or d <= 0:
            return d
        return d * (0.67 + self.a.rng.random() * 0.66)

    def end_dep_wait(self, now):
        """Close the wait's account. Caller holds mu."""
        if self.dep_since is not None:
            self.st.dep_wait_ms += int((now - self.dep_since) * 1000)
            self.dep_since = None

    def dep_columns(self):
        """Every column the landings we still wait on need: the columns their
        pieces fall through, which ours must keep out of while it waits, or
        they can never land and the wait can never end. Caller holds mu."""
        cols = set()
        for d in self.deps:
            if self.dep_resolved(d):
                continue
            for _, c in d.cells:
                cols.add(c)
        return cols

    def dep_aside(self, p):
        """Where the piece waits for the claims it depends on: where it
        stands, if none of the columns their landings need is under it; else
        the nearest reachable spot, at its orientation, clear of those
        columns and of every imminent spawn box (step_aside). Returns (spot,
        moved): moved is False when it need not, or cannot, move. Caller
        holds mu."""
        avoid = self.dep_columns()
        if not columns_meet(p, avoid):
            return p, False
        return self.step_aside(p, avoid)

    def step_aside(self, p, avoid):
        """The nearest reachable spot (to the left or the right, whichever is
        nearer) at the piece's orientation whose cells are clear of the
        columns given and of every imminent teammate spawn box. Caller holds
        mu."""
        boxes = self.teammate_boxes()

        def clear(a):
            if columns_meet(a, avoid):
                return False
            cs = a.cells()
            return not any(b.imminent and b.covers(cs) for b in boxes)

        for d in range(1, self.w):
            for col in (p.col - d, p.col + d):
                res = find_path(self.can_move, self.can_place, p, col, p.orient, boxes)
                if res.reached and res.end != p and clear(res.end):
                    return res.end, True
        return p, False

    # ---- the path (path.py) --------------------------------------------------

    def find_path(self, p, pl, boxes):
        """The path from p to the plan's column and orientation on the live
        board. Caller holds mu."""
        return find_path(self.can_move, self.can_place, p, pl.col, pl.orient, boxes)

    def teammate_boxes(self):
        """Every other seat's spawn box on our playfield, among the seats
        present (spawn_column_for), imminent when that seat holds no piece on
        the board. Caller holds mu."""
        if not self.shared() or self.seats_on_pf <= 1:
            return []
        present = self.present_slots(self.roster)
        if not present:
            present = list(range(self.seats_on_pf))
        out = []
        seen = set()
        mine = self.my_slot()
        for slot in present:
            if slot == mine or slot in seen or slot < 0 or slot >= self.seats_on_pf:
                continue
            seen.add(slot)
            seat = self.team * self.team_size() + slot if self.mode == MODE_TEAMS else slot
            c0 = self.spawn_column_for(slot, present)
            holds = seat in self.others_piece
            out.append(SpawnBox(seat=seat, c0=c0, c1=c0 + 3, imminent=not holds or self.seat_about_to_spawn(seat)))
        return out
