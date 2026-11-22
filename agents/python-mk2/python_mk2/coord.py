"""Coordination on a shared board: what the agent does with its teammates'
claims on the blackboard (blackboard.py): plans on them, yields to the
earlier ones, waits for the ones it depends on, and when a plan is no longer
the thing to do (request_replan). The blackboard's own plumbing is
blackboard.py; the planner's projection is planner.py's.
"""

from dataclasses import dataclass, field

from .blackboard import DROP_SOFT, PHASE_LOCKED, PHASE_PLANNED
from .consts import HEADROOM, MODE_TEAMS
from .engine import GRID_CLAIMED, Grid
from .path import Active, SpawnBox, find_path, flood
from .pieces import SPAWN_ROW, piece_cells
from .planner import DEP_PENALTY, ROUTE_MOVE, SILENT_LEAVE, Projection

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
 REPLAN_BLOCKED,        # a crewmate's piece has stood in the way past our patience: planned again among what the piece can walk to now
 REPLAN_HOLES,          # the landing would cover more than the plan expected: the board is not the one it was planned on (spoils)
 ) = range(10)

_REPLAN_NAMES = ("none", "overlap", "droprow", "deptimeout", "collapse", "stale", "unreachable", "lockdropped",
                 "blocked", "holes")


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

# How long a piece waits on a piece nobody has claimed (a human's, a
# golang-mk1's) before it plans again among what it can reach.
BLOCK_PATIENCE_SILENT = 0.6
# The re-plans a piece gets for being blocked, at most.
MAX_BLOCKED_PLANS = 4
# The re-plans a piece gets because its landing would cover more than its
# plan expected (spoils): past it, the landing is taken as it is.
MAX_HOLE_REPLANS = 3
# How many of its NEXT pieces a claim says a placement for (Claim.next), and
# how many of a teammate's a reader takes into account: past the second, a
# tentative placement is a guess about a board nobody has seen.
MAX_NEXT_CLAIMS = 2

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


@dataclass
class ForecastPiece:
    """A teammate's falling piece as it is expected to stand when ours moves:
    the cells it holds until then (where it is, and the rows gravity takes it
    through meanwhile) and when it is expected to be gone from them (landed,
    by its claim; a guess, without one)."""

    seat: int
    cells: list
    leave_at: float


def well_open(gr, w):
    """Whether nothing sits over an empty cell of the column."""
    covered = False
    for r in range(gr.h):
        if gr.filled(r, w):
            covered = True
        elif covered:
            return False
    return True


def well_high(gr, w):
    """Whether the stack beside the column stands higher than half the
    playing rows."""
    bit = 1 << w
    for r in range(gr.h):
        if gr.rows[r] & ~bit:
            return gr.h - r > (gr.h - HEADROOM) // 2
    return False


def well_ready(gr, well):
    """How many rows stand full but for the well's column, from the well's
    floor up: what an I piece dropped down it would clear."""
    if well < 0 or well >= gr.w:
        return 0
    bit = 1 << well
    n = 0
    for r in range(gr.h - 1, -1, -1):
        if gr.rows[r] & bit:
            continue  # the well's floor, or below it
        if gr.rows[r] | bit != gr.full:
            break
        n += 1
    return n


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
        holds but ours) in the right of way: by the revision each placement
        was first claimed at (Claim.prio), the order everyone sees. Caller
        holds mu."""
        out = [c for seat, c in self.claims.items()
               if seat != self.idx and c.seat == seat and self.seat_present_locked(seat)]
        out.sort(key=lambda c: (c.order(), c.rev))
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
                if self.ignored_claims.get(c.seat) == c.order():
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
        proj.well, proj.well_on, proj.well_high = self.well_for(gr)
        if self.my_intent is not None and self.my_intent.piece == self.piece_idx:
            proj.intent = (self.my_intent.rot, self.my_intent.col)
        self.stamp_tentative(gr, proj)
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

    def classify(self, p, cand, my_prio):
        """Check our placement (the piece p, planned at cand) against every
        claim made before ours (prio < my_prio), in that order, each
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
            if c.phase == PHASE_LOCKED or c.order() >= my_prio:
                continue
            if self.ignored_claims.get(c.seat) == c.order():
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
                    c = self.claims.get(d.seat)
                    if c is not None:
                        self.ignored_claims[d.seat] = c.order()
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
                res = find_path(self.can_place, self.others_act, p, col, p.orient, boxes)
                if res.reached and res.end != p and clear(res.end):
                    return res.end, True
        return p, False

    # ---- the path (path.py) --------------------------------------------------

    def find_path(self, p, pl, boxes, keep=None):
        """The path from p to the plan's column and orientation on the live
        board, a wait chosen outside the cells to keep out of (keep: the drop
        shafts of the claims with right of way over ours, keep_out). Caller
        holds mu."""
        return find_path(self.can_place, self.others_act, p, pl.col, pl.orient, boxes, keep)

    # ---- routes: where the teammates' pieces will be, and what a walk costs ----

    def forecast(self, now, exec_at):
        """The teammates' pieces expected to be in the air at exec_at: the
        "where is everyone going" the claims are there to answer. A piece
        whose claim says it is planned, hard-dropped, not waiting on anyone,
        and due to land by exec_at is GONE: it will have left, and its
        landing is the projection's business. Every other piece (one that
        soft-drops, one that waits, one of a seat that says nothing) is where
        it is, plus the rows gravity carries it down until exec_at, and is
        expected to leave when its claim says, or after SILENT_LEAVE. Sorted
        by when they leave. Caller holds mu."""
        if not self.shared():
            return []
        fall = 0
        step = self.gravity_now()
        if step > 0 and exec_at > now:
            fall = int((exec_at - now) / step)
        tn = self.a.tn
        out = []
        for pi in sorted(self.others_piece):
            op = self.others_piece[pi]
            leave = now + SILENT_LEAVE
            c = self.claims.get(pi)
            if c is not None and c.seat == pi:
                if c.phase == PHASE_PLANNED and c.type == op.pt and c.drop != DROP_SOFT and not c.wait_on:
                    leave = c.seen_at + c.eta_ms / 1000
                    if leave <= exec_at:
                        continue  # it will have landed by then
                elif c.phase == PHASE_LOCKED or c.type != op.pt:
                    # A seat that speaks, holding a piece it has not claimed
                    # yet: it will plan it, think and go as we do.
                    leave = now + tn.piece_delay + tn.move_delay + 0.05
            cells = []
            for d in range(fall + 1):
                cs = piece_cells(op.pt, op.orient, op.row + d, op.col)
                if d > 0 and not self.can_place(cs):
                    break
                cells.extend(cs)
            out.append(ForecastPiece(seat=pi, cells=cells, leave_at=leave))
        out.sort(key=lambda fp: fp.leave_at)
        return out

    def route_costs(self, p, now, pause):
        """What the walk to every (orientation, column) costs the piece p,
        planned now and moved after the pause: one flood over the board with
        the forecast pieces on it prices everything the piece will reach at
        once (ROUTE_MOVE a move); what it will not reach until a crewmate's
        piece has gone is priced by the flood without that piece (the
        earliest-leaving first, a layer at a time) plus the wait for it to go
        (DEP_PENALTY a second). The planner adds the cost to the placement at
        that column (Projection.penalty). Caller holds mu."""
        exec_at = now + pause
        fc = self.forecast(now, exec_at)
        boxes = self.teammate_boxes()
        out = {}
        for layer in range(len(fc) + 1):
            others = {}
            for fp in fc[layer:]:
                for c in fp.cells:
                    others[c] = fp.seat
            wait = 0.0
            if layer > 0:
                wait = max(fc[layer - 1].leave_at - exec_at, 0.0)
            s = flood(self.can_place, p, boxes, others)
            for k, d in s.reach().items():
                if k not in out:
                    out[k] = ROUTE_MOVE * d + DEP_PENALTY * wait
            if not s.blockers:
                break  # nothing of anyone's stood in the way: the rest is the stack's
        return out

    def route_costs_now(self, p):
        """route_costs for a piece that is blocked and planned again
        (REPLAN_BLOCKED): the board as it is this instant, every teammate's
        piece a wall. A placement the piece cannot walk to right now is no
        answer to being blocked, and ranks below every one it can
        (ROUTE_BLOCKED, by penalty's default). Caller holds mu."""
        s = flood(self.can_place, p, self.teammate_boxes(), self.others_act)
        return {k: ROUTE_MOVE * d for k, d in s.reach().items()}

    def keep_out(self):
        """The cells our piece must not STOP in: the drop shafts (the
        landing's columns, from the top of the board down to the landing) of
        every planned claim with the right of way over ours. Their piece
        comes down there; ours may cross a shaft in a batch, and a wait is
        chosen outside them (waiting_spot). Generalises dep_columns, which
        keeps out of the shafts of the claims our own landing depends on.
        None when there is nothing to keep out of. Caller holds mu."""
        if self.coord is None or self.my_claim is None or self.my_prio == 0:
            return None
        keep = None
        gr = self.to_grid_projected()
        for c in self.ordered_claims():
            if c.phase != PHASE_PLANNED or c.order() >= self.my_prio:
                continue
            op = self.others_piece.get(c.seat)
            if op is None or op.pt != c.type:
                continue
            their = self.project_claim(gr, c)
            if their is None:
                continue
            stamp_claimed(gr, their)
            if keep is None:
                keep = set()
            for xr, xc in their:
                for r in range(xr):
                    keep.add((r, xc))
        return keep

    # ---- blocked: who waits, who goes, and for how long ----------------------

    def block_patience(self, seats, now):
        """How long the piece in play waits for the seats' pieces to get out
        of its way before it plans again among the placements it can walk to
        right now (REPLAN_BLOCKED). The right of way decides: against pieces
        whose claims were all made after ours, we hold our plan (they are the
        ones to step aside or plan again) for as long as a claim is waited on
        at all (DEP_WAIT_TIMEOUT). Against a claim made before ours we give
        it twice what it says it still needs, and a piece nobody has claimed
        gets BLOCK_PATIENCE_SILENT; within DEP_WAIT_MIN and DEP_WAIT_TIMEOUT.
        Two pieces in each other's way are thus never both the one that
        waits: the claims are in one order, and the later one moves. Caller
        holds mu."""
        pat = DEP_WAIT_MIN
        ours = self.my_prio != 0
        for seat in seats:
            c = self.claims.get(seat)
            op = self.others_piece.get(seat)
            if c is None or c.phase != PHASE_PLANNED or op is None or op.pt != c.type:
                ours = False
                pat = max(pat, BLOCK_PATIENCE_SILENT)
                continue
            if c.order() < self.my_prio:
                ours = False
            left = c.seen_at + c.eta_ms / 1000 - now
            pat = max(pat, 2 * left)
        if ours and seats:
            return DEP_WAIT_TIMEOUT
        return min(pat, DEP_WAIT_TIMEOUT)

    def owners_under(self, a):
        """The seats whose falling pieces the piece a would come to rest on,
        were it dropped where it stands: who a blocked drop waits on. Sorted.
        Caller holds mu."""
        dr = self.drop_row_shared(a)
        others = self.others_act
        return sorted({others[c] for c in piece_cells(a.pt, a.orient, dr + 1, a.col) if c in others})

    # ---- the pieces to come, and the well -------------------------------------

    def stamp_tentative(self, gr, proj):
        """Put the teammates' tentative placements for their NEXT pieces
        (Claim.next) on the projection's tent map: each dropped on the board
        the firm claims made, a seat's second on its first, weighted 1 for
        the piece up next and half for the one after. A nudge (TENT_PENALTY),
        never a wall: nothing yields to a tentative placement, and nothing
        waits for one. A seat whose claim is `locked` holds the first of them
        in its hands by now: this is where its fresh piece is most likely
        going, before it has said so. Caller holds mu."""
        scratch = None
        tent = proj.tent
        for c in self.ordered_claims():
            for i, n in enumerate(c.next[:MAX_NEXT_CLAIMS]):
                if not 0 <= n.type <= 6 or not 0 <= n.rot <= 3 or n.piece <= c.piece:
                    break
                if scratch is None:
                    scratch = gr.clone()
                if not scratch.can_place(piece_cells(n.type, n.rot, SPAWN_ROW, n.col)):
                    break
                weight = 1 / float(i + 1)
                dr = scratch.drop_row(n.type, n.rot, SPAWN_ROW, n.col)
                for x in piece_cells(n.type, n.rot, dr, n.col):
                    r, col = x
                    if r < 0 or r >= scratch.h or col < 0 or col >= scratch.w:
                        continue
                    tent[x] = max(tent.get(x, 0.0), weight)
                    scratch.set(r, col, GRID_CLAIMED)

    def well_wanted(self):
        """Whether this agent keeps a well on this board: a coordinated one,
        by its --well setting: "on", or "auto" anywhere but a survival game,
        whose rising floor leaves no room to bank rows."""
        if self.coord is None or self.a is None or self.w < 8:
            return False
        well = getattr(self.a, "well", "auto")
        if well == "on":
            return True
        if well == "auto":
            return not self.survival
        return False

    def well_column(self):
        """The column the crew keeps open: the board's last. Every seat
        derives the same one from the board's width, and says it in its
        claims (Claim.well), so a crew knows it agrees."""
        return self.w - 1

    def well_for(self, gr):
        """Whether the plan made on gr keeps the well, where it is, and
        whether the stack beside it stands high: kept when this agent wants
        one (well_wanted); every teammate on the board has a claim up naming
        the same column (a crew with a human, a golang-mk1 or an agent that
        keeps no well plays as it always did); and the column is open to the
        sky (with something over it there is no well to keep, and the board
        is played plainly until it is dug out). High is a stack past half the
        playing rows: the well is kept all the same (played plainly, a deep
        well is the first thing Dellacherie's features cap) but an I piece
        then takes what rows there are (evaluate_well). Returns (column, on,
        high). Caller holds mu."""
        if not self.well_wanted():
            return 0, False, False
        w = self.well_column()
        if w < 0 or w >= gr.w:
            return w, False, False
        boxes = self.teammate_boxes()
        if not boxes:
            return w, False, False
        for b in boxes:
            c = self.claims.get(b.seat)
            if c is None or c.well is None or c.well != w:
                return w, False, False
        return w, well_open(gr, w), well_high(gr, w)

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
