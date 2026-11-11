"""The brain: Pierre Dellacherie's six-feature one-ply heuristic with the
"El-Tetris" weights. Features are computed on the board AFTER the simulated
lock and clear, except landing height and eroded cells, which are move
features by definition. A beam-pruned lookahead over the game's revealed
piece preview extends it.
"""

from dataclasses import dataclass, field

from .engine import GRID_CLAIMED, Geometry
from .pieces import SPAWN_ROW, piece_cells
from .rng import piece_at_bag

WEIGHT_LANDING_HEIGHT = -4.500158825
WEIGHT_ERODED_CELLS = 3.4181268
WEIGHT_ROW_TRANSITIONS = -3.2178882
WEIGHT_COL_TRANSITIONS = -9.348695
WEIGHT_HOLES = -7.899265
WEIGHT_WELLS = -3.3855972

# PLAN_BEAM is how many of the piece's own placements (the best by their
# one-ply evaluation) get the lookahead. Every placement would be 48 of them
# on a 14-column board, each searching 121 nodes of 48 placements: longer
# than a human takes, and longer than any teammate should have to wait for a
# claim.
PLAN_BEAM = 6
LOOKAHEAD_BEAM = 3
LOOKAHEAD_TOP_OUT_PENALTY = -1e4

# The placement penalties a coordinated board adds (Projection.penalty), in
# Dellacherie's units (a hole is 7.9).
DEP_PENALTY = 2.0    # per second an earlier claim we would wait on is expected to take
SOFT_PENALTY = 1.5   # per cell of the placement in, or on top of, a silent teammate's straight drop
TRAVEL_COST = 0.15   # per column between our spawn point and the placement
CROSS_COST = 4.0     # per imminent teammate spawn box the walk crosses


@dataclass
class Placement:
    """One candidate final resting position and the score it earns. wait_on,
    on a coordinated board, is the seats whose claimed landings hold the
    placement up: it must not lock before theirs have (coord.py)."""

    orient: int = 0
    col: int = 0
    drop_row: int = 0
    dest: tuple = ()
    lines: int = 0
    score: float = 0.0
    wait_on: list = field(default_factory=list)


@dataclass
class Projection:
    """What a coordinated board adds to the planner's grid
    (coord.build_projection): the teammates' claimed landings stamped into it
    as GRID_CLAIMED, by seat, with the time each is expected to take; our own
    spawn column, and the teammates' spawn boxes, for the costs a placement
    carries beyond its Dellacherie score (penalty)."""

    claimed: dict = field(default_factory=dict)   # a claimed landing's cell → the seat whose it is
    soft: dict = field(default_factory=dict)      # a silent teammate's straight drop → the seat: a nudge, not a wall
    dep_eta: dict = field(default_factory=dict)   # seat → how long their claim is expected to take to land, in ms
    pieces: dict = field(default_factory=dict)    # seat → the piece index their claim is for
    own_spawn_c: int = 0
    boxes: list = field(default_factory=list)

    def penalty(self, cand):
        """What a placement costs beyond its evaluation: waiting on a
        teammate's claim in proportion to how long it is expected to take;
        every cell of the placement in, or right on top of, a silent
        teammate's straight drop; the walk from our spawn point, a little per
        column; and every imminent teammate spawn box the walk crosses."""
        p = 0.0
        for seat in cand.wait_on:
            p += DEP_PENALTY * float(self.dep_eta.get(seat, 0)) / 1000
        soft = self.soft
        for r, c in cand.dest:
            if (r, c) in soft or (r + 1, c) in soft:
                p += SOFT_PENALTY
        p += TRAVEL_COST * abs(float(cand.col - self.own_spawn_c))
        lo, hi = min(cand.col, self.own_spawn_c), max(cand.col, self.own_spawn_c) + 3
        for b in self.boxes:
            if b.imminent and b.c0 <= hi and b.c1 >= lo:
                p += CROSS_COST
        return p


def support_seats(g, proj, cand):
    """The seats whose claimed landings a placement rests on or completes a
    row with: the claims it must let land first. Sorted."""
    seats = set()
    in_row = set()
    for r, c in cand.dest:
        in_row.add(r)
        if r + 1 < g.h and g.at(r + 1, c) == GRID_CLAIMED:
            seat = proj.claimed.get((r + 1, c))
            if seat is not None:
                seats.add(seat)
    # A row the lock completes: full once the placement's cells are in.
    dest = set(cand.dest)
    for r in in_row:
        if any((r, c) not in dest and not g.filled(r, c) for c in range(g.w)):
            continue
        for c in range(g.w):
            if g.at(r, c) == GRID_CLAIMED:
                seat = proj.claimed.get((r, c))
                if seat is not None:
                    seats.add(seat)
    return sorted(seats)


def enumerate_placements(g, pt, s_row, s_col):
    """Each reachable resting position: the orientations reached by
    successive in-place rotations at spawn, each swept left and right across
    every column it can occupy, dropped to rest. (The reference form of the
    packed search's _expand: the two are pinned to each other by the tests.)"""
    orients = []
    seen = set()
    o = 0
    while True:
        if (o & 3) not in seen and g.can_place(piece_cells(pt, o & 3, s_row, s_col)):
            seen.add(o & 3)
            orients.append(o & 3)
        if o >= 3 or not g.can_place(piece_cells(pt, (o + 1) & 3, s_row, s_col)):
            break
        o += 1
    out = []

    def record(o, col):
        dr = g.drop_row(pt, o, s_row, col)
        out.append(Placement(orient=o, col=col, drop_row=dr, dest=piece_cells(pt, o, dr, col)))

    for o in orients:
        record(o, s_col)
        col = s_col - 1
        while g.can_place(piece_cells(pt, o, s_row, col)):
            record(o, col)
            col -= 1
        col = s_col + 1
        while g.can_place(piece_cells(pt, o, s_row, col)):
            record(o, col)
            col += 1
    return out


def simulate_lock(g, dest):
    """Stamp dest onto a clone, clear full rows, and return the resulting
    board, the number of lines cleared, and the eroded-cells feature (lines ×
    piece cells that were in the cleared rows)."""
    sim = g.clone()
    for r, c in dest:
        if 0 <= r < sim.h and 0 <= c < sim.w:
            sim.set(r, c, 1)
    rows = sim.completed_rows()
    if not rows:
        return sim, 0, 0
    in_row = set(rows)
    piece_in_cleared = sum(1 for r, _ in dest if r in in_row)
    return sim.clear_rows(rows), len(rows), piece_in_cleared * len(rows)


def landing_height(g, dest):
    total = 0.0
    for r, _ in dest:
        total += float(g.h - 1 - r)
    return total / float(len(dest))


def evaluate_board(g, dest, lines, eroded):
    """Score one placement: the Dellacherie feature combination."""
    return (WEIGHT_LANDING_HEIGHT * landing_height(g, dest) +
            WEIGHT_ERODED_CELLS * float(eroded) +
            WEIGHT_ROW_TRANSITIONS * float(g.row_transitions()) +
            WEIGHT_COL_TRANSITIONS * float(g.col_transitions()) +
            WEIGHT_HOLES * float(g.holes()) +
            WEIGHT_WELLS * float(g.wells()))


# ---- the packed search --------------------------------------------------------


def _can_spawn(geo, b, pt, orient, row, col):
    mask, cmin, cmax, max_ro = geo.shapes[pt][orient][:4]
    if col < cmin or col > cmax or row < 0 or row + max_ro >= geo.h:
        return False
    return not b & (mask << (row * geo.stride + col))


def _expand(geo, b, st, fo, pt, s_row, s_col):
    """Every placement of piece pt from (s_row, s_col) on the packed board
    (b, st, fo), in enumerate_placements' order, each locked, cleared and
    evaluated: a list of (score, orient, col, drop row, lines, board after,
    piece mask, cleared rows)."""
    h, w, s = geo.h, geo.w, geo.stride
    shapes = geo.shapes[pt]
    base = s_row * s

    def placeable(o):
        mask, cmin, cmax, max_ro = shapes[o][:4]
        return cmin <= s_col <= cmax and s_row + max_ro < h and not b & (mask << (base + s_col))

    orients = []
    o = 0
    while True:
        if (o & 3) not in orients and placeable(o & 3):
            orients.append(o & 3)
        if o >= 3 or not placeable((o + 1) & 3):
            break
        o += 1
    if not orients:
        return []

    # The first filled row at or below the start row, a column at a time:
    # what a straight drop comes to rest on.
    first = [h] * w
    under = b & ~geo.low[s_row]
    col_mask = geo.col_mask
    for c in range(w):
        x = under & col_mask[c]
        if x:
            first[c] = ((x & -x).bit_length() - 1) // s

    ones, pads, floor, cells_mask, last, smears, low = (
        geo.ones, geo.pads, geo.floor, geo.cells, geo.last, geo.smears, geo.low)
    row_mask = geo.row_mask
    lh_base = 4 * (h - 1)
    # Rows already full before the piece lands: the ones that cannot clear
    # (solid garbage, a foreign piece across them) stay, the ones that can
    # (a teammate's claimed landing filled them) clear with any lock.
    full_before = (b + ones) & pads
    pre = []
    if full_before:
        pre = [r for r in range(h)
               if full_before >> (r * s + w) & 1 and st & row_mask[r] and not fo & row_mask[r]]
    out = []
    for o in orients:
        mask, cmin, cmax, max_ro, sum_ro, cols, offs, row_offs = shapes[o]
        at_start = mask << base
        order = [s_col]
        col = s_col - 1
        while col >= cmin and not b & (at_start << col if col >= 0 else at_start >> -col):
            order.append(col)
            col -= 1
        col = s_col + 1
        while col <= cmax and not b & (at_start << col if col >= 0 else at_start >> -col):
            order.append(col)
            col += 1
        for col in order:
            # The drop: every column of the piece rests on its column's first
            # filled row; the shortest fall is the piece's.
            k = h
            for co, top, bottom in cols:
                f = first[col + co]
                if f < s_row + top:
                    k = -1  # a filled cell beside the piece, above its cell in this column: walk it down
                    break
                d = f - (s_row + bottom) - 1
                if d < k:
                    k = d
            if k < 0:
                k = 0
                sh = base + col
                while s_row + k + 1 + max_ro < h:
                    m = mask << (sh + (k + 1) * s) if sh >= 0 else (mask << ((k + 1) * s)) >> -sh
                    if b & m:
                        break
                    k += 1
            dr = s_row + k
            sh = dr * s + col
            p = mask << sh if sh >= 0 else mask >> -sh
            a = b | p
            lines = eroded = 0
            rows = ()
            new_full = ((a + ones) & pads) ^ full_before
            if new_full or pre:
                rows = list(pre)
                if new_full:
                    for ro in row_offs:
                        r = dr + ro
                        if new_full >> (r * s + w) & 1 and not fo & row_mask[r]:
                            rows.append(r)
                    rows.sort()
                if rows:
                    lines = len(rows)
                    in_cleared = 0
                    for ro, _ in offs:
                        if dr + ro in rows:
                            in_cleared += 1
                    eroded = in_cleared * lines
                    for r in rows:
                        a = (a & ~low[r + 1]) | ((a & low[r]) << s)
            # Dellacherie's board features (Geometry.features, inlined: this
            # is the search's innermost loop).
            left = (a << 1) | ones
            rt = (left ^ (a | pads)).bit_count()
            ct = ((a << s) ^ (a | floor)).bit_count()
            empty = cells_mask ^ a
            cov = a
            for sm in smears:
                cov |= cov << sm
            holes = ((cov << s) & empty).bit_count()
            cur = left & ((a >> 1) | last) & empty
            wells = cur.bit_count()
            below = empty
            while cur:
                below >>= s
                cur &= below
                wells += cur.bit_count()
            score = (WEIGHT_LANDING_HEIGHT * ((lh_base - 4 * dr - sum_ro) / 4.0) +
                     WEIGHT_ERODED_CELLS * float(eroded) +
                     WEIGHT_ROW_TRANSITIONS * float(rt) +
                     WEIGHT_COL_TRANSITIONS * float(ct) +
                     WEIGHT_HOLES * float(holes) +
                     WEIGHT_WELLS * float(wells))
            out.append((score, o, col, dr, lines, a, p, rows))
    return out


def _after(geo, st, fo, cand):
    """The stack and foreign masks of the board a candidate leaves."""
    stp = st | cand[6]
    rows = cand[7]
    if rows:
        return geo.clear(stp, rows), geo.clear(fo, rows)
    return stp, fo


def _lookahead(geo, b, st, fo, spawn_c, upcoming, i):
    """The best evaluation total achievable by playing upcoming[i:] (the
    revealed preview pieces, in order) on the packed board: at each level
    only the LOOKAHEAD_BEAM best placements are expanded further. A piece
    that cannot spawn is an imminent top-out and scores the penalty."""
    if i >= len(upcoming):
        return 0.0
    pt = upcoming[i]
    if not _can_spawn(geo, b, pt, 0, SPAWN_ROW, spawn_c):
        return LOOKAHEAD_TOP_OUT_PENALTY
    cands = _expand(geo, b, st, fo, pt, SPAWN_ROW, spawn_c)
    if not cands:
        return LOOKAHEAD_TOP_OUT_PENALTY
    if i == len(upcoming) - 1:
        return max(c[0] for c in cands)
    cands.sort(key=_neg_score)
    best = None
    for cand in cands[:LOOKAHEAD_BEAM]:
        nst, nfo = _after(geo, st, fo, cand)
        total = cand[0] + _lookahead(geo, cand[5], nst, nfo, spawn_c, upcoming, i + 1)
        if best is None or total > best:
            best = total
    return best


def _neg_score(c):
    return -c[0]


def lookahead(g, spawn_c, upcoming):
    """lookahead on a Grid (the packed search underneath)."""
    geo = Geometry.of(g.h, g.w)
    b, st, fo = geo.pack(g)
    return _lookahead(geo, b, st, fo, spawn_c, list(upcoming), 0)


def plan_roots(g, pt, s_row, s_col, spawn_c, upcoming, proj=None):
    """The first half of a plan: every placement reachable with kick-free
    rotations at the piece's current position (s_row, s_col) plus sideways
    slides plus a hard drop (a legal subset of the game's moves), each with
    its one-ply Dellacherie score. Returns (placements, the indices of the
    PLAN_BEAM best of them, one lookahead job per such placement): a job is
    the arguments of lookahead_job, the search over `upcoming` (which spawn
    fresh at spawn_c: the caller's own section spawn on shared boards) from
    the board that placement leaves. No jobs without a preview."""
    geo = Geometry.of(g.h, g.w)
    b, st, fo = geo.pack(g)
    cands = _expand(geo, b, st, fo, pt, s_row, s_col)
    out = []
    for score, o, col, dr, lines, _, _, _ in cands:
        pl = Placement(orient=o, col=col, drop_row=dr, dest=piece_cells(pt, o, dr, col), lines=lines, score=score)
        if proj is not None:
            pl.wait_on = support_seats(g, proj, pl)
        out.append(pl)
    if not upcoming:
        return out, [], []
    upcoming = tuple(upcoming)
    order = sorted(range(len(out)), key=lambda i: -out[i].score)
    jobs = []
    for i in order[:PLAN_BEAM]:
        nst, nfo = _after(geo, st, fo, cands[i])
        jobs.append((g.h, g.w, cands[i][5], nst, nfo, spawn_c, upcoming))
    return out, order, jobs


def lookahead_job(h, w, b, st, fo, spawn_c, upcoming):
    """One of plan_roots' jobs: the lookahead's total from a packed board. A
    plain function of plain integers, so it can run in another process."""
    return _lookahead(Geometry.of(h, w), b, st, fo, spawn_c, upcoming, 0)


def plan_finish(out, order, totals, proj=None):
    """The second half of a plan: the lookahead's totals (one per job, in
    order) added to the beam's placements, the coordinated board's penalties
    taken off, and the placements returned best first. A placement outside
    the beam keeps its one-ply score and ranks below every placement in it
    (the lookahead's sum is what scores are made of), in its one-ply order."""
    if totals:
        floor = float("inf")
        for i, la in zip(order, totals):
            out[i].score += la
            floor = min(floor, la)
        for i in order[len(totals):]:
            out[i].score += floor - 1
    if proj is not None:
        for pl in out:
            pl.score -= proj.penalty(pl)
    out.sort(key=lambda pl: -pl.score)
    return out


def plan_placements(g, pt, s_row, s_col, spawn_c, upcoming, proj=None):
    """A whole plan, here and now: every placement of the piece scored with
    Dellacherie + the lookahead over `upcoming`, less, on a coordinated board
    (proj), the placement's penalty; best first."""
    out, order, jobs = plan_roots(g, pt, s_row, s_col, spawn_c, upcoming, proj)
    return plan_finish(out, order, [lookahead_job(*job) for job in jobs], proj)


def choose(ranked, tn, rnd):
    """Apply the blunder model to a ranked list: with probability
    tn.blunder_rate pick uniformly among ranks 2..1+blunder_depth instead of
    the best. None only for an empty list."""
    if not ranked:
        return None
    if rnd is not None and tn.blunder_rate > 0 and len(ranked) > 1 and rnd.random() < tn.blunder_rate:
        depth = min(tn.blunder_depth, len(ranked) - 1)
        if depth > 0:
            return ranked[1 + rnd.randrange(depth)]
    return ranked[0]


def revealed_pieces(seed, ration, bag, piece_idx, next_count, lookahead_n):
    """The planner's entire lookahead: the upcoming pieces the GAME reveals
    (the next `next_count` of this seat's sequence, exactly the tiles a human
    sees in the NEXT well) further trimmed to the difficulty's lookahead.
    This is the fair-visibility contract's preview rule (jetris-agent-guide.md
    §1): the horizon is the game's setting, read from its meta, and nothing
    on the agent's side (difficulty, flag, default) may reach past it. A game
    with no preview reveals nothing, so the planner plays one piece at a time
    like everyone else. The seed is consulted for these indices only. `ration`
    is this seat's piece set in a split-pieces game (empty elsewhere): the
    preview is of the seat's OWN sequence. `bag` is the game's bag rule."""
    return [piece_at_bag(seed, ration, bag, piece_idx + i)
            for i in range(1, min(next_count, lookahead_n) + 1)]
