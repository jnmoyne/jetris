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
TENT_PENALTY = 1.0   # per cell of the placement in a teammate's tentative landing for a NEXT piece, times its weight (1 for the first, half for the second)
INTENT_BONUS = 1.0   # for the placement we announced for this piece while it was in our NEXT well: said, it is mostly kept
TRAVEL_COST = 0.15   # per column between our spawn point and the placement (a projection without a route only)
CROSS_COST = 4.0     # per imminent teammate spawn box the walk crosses (a projection without a route only)

# What a move of the walk to a placement costs the placement, in
# Dellacherie's units: a tie-break between equals (a placement at the far end
# of a wide board costs a twentieth of a hole). The walk itself is one batch
# however long it is; what a far placement really costs is the wait when a
# crewmate's piece is across the way, and that is priced by the wait
# (DEP_PENALTY a second), not by distance.
ROUTE_MOVE = 0.02
# What a placement no walk reaches costs in a re-plan made because the piece
# is blocked (coord.route_costs_now): it ranks below everything the piece can
# reach where it stands.
ROUTE_BLOCKED = 1e3
# How long a teammate's piece is expected to stay in the air when nothing
# says: a seat that does not speak, or one that waits. Seconds.
SILENT_LEAVE = 1.0

# What a cell put in the crew's well costs a placement that does not clear
# four rows with it, in Dellacherie's units: more than a hole (the well is
# what the four-row clear is made of).
WELL_SPOIL = 10.0
# What every empty cell of the well left UNDER such a placement costs it: a
# piece laid across the top of the well buries the rows banked beside it, and
# nothing but digging gets them back.
WELL_COVER = 25.0

# The plan that keeps no well: (column, the stack beside it stands high).
NO_WELL = (-1, False)


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
    # The lookahead's best line from the board this placement leaves, as
    # ((orient, col), …), one per upcoming piece: set on the placements the
    # lookahead searched (plan_finish), None on the rest (principal_variation
    # finds it then).
    pv: tuple = field(default=None, compare=False)


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
    # route is what the walk to each (orientation, column) costs, wait for a
    # crewmate's piece included (coord.route_costs); route_now says that
    # what is not in it cannot be reached at all: it was made for a blocked
    # piece, on the board as it is, or for one gravity will have taken down
    # among the stack before it moves. None: the flat costs below.
    route: dict = None
    route_now: bool = False
    # tent is the cells of the teammates' tentative placements for their NEXT
    # pieces (Claim.next), each with its weight: a nudge away, never a wall;
    # intent is the placement (orientation, column) we announced for the
    # piece now in play, which gets INTENT_BONUS for being kept; well is the
    # column the crew keeps open for a four-row clear, while well_on, and
    # well_high says the stack beside it stands high (coord.well_for).
    tent: dict = field(default_factory=dict)
    intent: tuple = None
    well: int = 0
    well_on: bool = False
    well_high: bool = False

    def well_spec(self):
        """The well the plan keeps, NO_WELL when it keeps none."""
        if not self.well_on:
            return NO_WELL
        return (self.well, self.well_high)

    def penalty(self, cand):
        """What a placement costs beyond its evaluation: waiting on a
        teammate's claim in proportion to how long it is expected to take;
        every cell of the placement in, or right on top of, a silent
        teammate's straight drop; and the walk there: what the path search
        says it costs, a crewmate's piece across the way priced by the wait
        for it (route); or, on a projection without a route, a little per
        column from our spawn point and every imminent teammate spawn box the
        walk crosses."""
        p = 0.0
        for seat in cand.wait_on:
            p += DEP_PENALTY * float(self.dep_eta.get(seat, 0)) / 1000
        soft = self.soft
        for r, c in cand.dest:
            if (r, c) in soft or (r + 1, c) in soft:
                p += SOFT_PENALTY
        tent = self.tent
        if tent:
            for d in cand.dest:
                p += TENT_PENALTY * tent.get(d, 0.0)
        if self.intent is not None and self.intent == (cand.orient, cand.col):
            p -= INTENT_BONUS
        if self.route is not None:
            # The walk as the path search finds it, and the wait when a
            # crewmate's piece is across it: the whole board is one piece's
            # to play on, and a far column costs what getting there costs.
            c = self.route.get((cand.orient, cand.col))
            if c is not None:
                return p + c
            if self.route_now:
                return p + ROUTE_BLOCKED
            return p + DEP_PENALTY * SILENT_LEAVE
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


def enumerate_placements(g, pt, s_row, s_col, s_orient=0):
    """Each reachable resting position: the orientations reached by
    successive in-place rotations from the one the piece stands in (s_orient:
    0 at its spawn; a piece planned again where it stands has turned since),
    each swept left and right across every column it can occupy, dropped to
    rest. (The reference form of the packed search's _expand: the two are
    pinned to each other by the tests.)"""
    orients = []
    seen = set()
    i = 0
    while True:
        o = (s_orient + i) & 3
        if o not in seen and g.can_place(piece_cells(pt, o, s_row, s_col)):
            seen.add(o)
            orients.append(o)
        if i >= 3 or not g.can_place(piece_cells(pt, (o + 1) & 3, s_row, s_col)):
            break
        i += 1
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


def evaluate_well(g, dest, lines, eroded, ws):
    """evaluate_board for a crew keeping a column open (ws: the column and
    whether the stack beside it stands high; NO_WELL: it is evaluate_board
    exactly): the features are taken with the well's column counted as filled
    (the board is played as if it were a column narrower, so rows full but
    for the well read as full, and the well itself as neither a well nor a
    hole) and a row banked beside it as the empty row a clear will make of
    it. A placement that puts cells in the well without clearing four rows
    pays WELL_SPOIL a cell (unless the stack is high and the cells sit on the
    well's floor: an I piece cashing the rows there are) and WELL_COVER for
    every empty cell of the well it leaves under it. The four-row clear needs
    no reward of its own: eroded cells pay it 16. (The reference form of the
    packed search's: the two are pinned to each other by the tests.)"""
    well, high = ws
    if well < 0 or well >= g.w:
        return evaluate_board(g, dest, lines, eroded)
    saved = g.rows
    bit = 1 << well
    g.rows = [m | bit for m in saved]
    score = evaluate_board(g, dest, lines, eroded)
    # A row full but for the well has no transitions at all, where the empty
    # row a clear leaves in its place has two (the walls count as filled):
    # left at that, every row banked beside the well is worth two transitions
    # to keep, and the evaluator hoards rows it could clear (it would rather
    # lay an I flat than send it down a well seven rows deep). A banked row
    # is counted as the empty row it is about to be.
    banked = sum(1 for m in g.rows if m == g.full)
    score += WEIGHT_ROW_TRANSITIONS * float(2 * banked)
    g.rows = saved
    if lines >= 4:
        return score
    inside = sum(1 for _, c in dest if c == well)
    if inside == 0:
        return score
    # What is empty in the well with something over it is buried: the well
    # was open before this piece (coord.well_for), so this piece is what
    # buried it: laid across the top, or hung into it from the side.
    under, covered = 0, False
    for r in range(g.h):
        if g.filled(r, well):
            covered = True
        elif covered:
            under += 1
    if not (high and under == 0):
        score -= WELL_SPOIL * float(inside)
    return score - WELL_COVER * float(under)


# ---- the packed search --------------------------------------------------------


def _can_spawn(geo, b, pt, orient, row, col):
    mask, cmin, cmax, max_ro = geo.shapes[pt][orient][:4]
    if col < cmin or col > cmax or row < 0 or row + max_ro >= geo.h:
        return False
    return not b & (mask << (row * geo.stride + col))


def _expand(geo, b, st, fo, pt, s_row, s_col, well=-1, high=False, s_orient=0):
    """Every placement of piece pt from (s_row, s_col), standing in the
    orientation s_orient, on the packed board (b, st, fo), in
    enumerate_placements' order, each locked, cleared and evaluated (as
    evaluate_well does, for a crew keeping the column `well` open; -1: none):
    a list of (score, orient, col, drop row, lines, board after, piece mask,
    cleared rows)."""
    h, w, s = geo.h, geo.w, geo.stride
    if well < 0 or well >= w:
        well = -1
    well_mask = geo.col_mask[well] if well >= 0 else 0
    shapes = geo.shapes[pt]
    base = s_row * s

    def placeable(o):
        mask, cmin, cmax, max_ro = shapes[o][:4]
        return cmin <= s_col <= cmax and s_row + max_ro < h and not b & (mask << (base + s_col))

    orients = []
    i = 0
    while True:
        o = (s_orient + i) & 3
        if o not in orients and placeable(o):
            orients.append(o)
        if i >= 3 or not placeable((o + 1) & 3):
            break
        i += 1
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
            # is the search's innermost loop), the crew's well counted as
            # filled (evaluate_well).
            am = a | well_mask
            left = (am << 1) | ones
            rt = (left ^ (am | pads)).bit_count()
            ct = ((am << s) ^ (am | floor)).bit_count()
            empty = cells_mask ^ am
            cov = am
            for sm in smears:
                cov |= cov << sm
            holes = ((cov << s) & empty).bit_count()
            cur = left & ((am >> 1) | last) & empty
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
            if well >= 0:
                # A row banked beside the well counts as the empty row a
                # clear will make of it; a piece in the well that does not
                # clear four rows pays for the cells it puts there, and for
                # every empty cell of the well it leaves under it.
                banked = ((am + ones) & pads).bit_count()
                score += WEIGHT_ROW_TRANSITIONS * float(2 * banked)
                if lines < 4:
                    inside = 0
                    for _, co in offs:
                        if col + co == well:
                            inside += 1
                    if inside:
                        under, covered = 0, False
                        x = a >> well
                        for _ in range(h):
                            if x & 1:
                                covered = True
                            elif covered:
                                under += 1
                            x >>= s
                        if not (high and under == 0):
                            score -= WELL_SPOIL * float(inside)
                        score = score - WELL_COVER * float(under)
            out.append((score, o, col, dr, lines, a, p, rows))
    return out


def _after(geo, st, fo, cand):
    """The stack and foreign masks of the board a candidate leaves."""
    stp = st | cand[6]
    rows = cand[7]
    if rows:
        return geo.clear(stp, rows), geo.clear(fo, rows)
    return stp, fo


def _lookahead(geo, b, st, fo, spawn_c, upcoming, i, well=-1, high=False, keep=False):
    """The best evaluation total achievable by playing upcoming[i:] (the
    revealed preview pieces, in order) on the packed board, every ply
    evaluated as the plan's own placements are (the crew's well, if any): at
    each level only the LOOKAHEAD_BEAM best placements are expanded further.
    A piece that cannot spawn is an imminent top-out and scores the penalty.
    Returns (total, line): line, when asked (keep), is the placements that
    make the total, as [(orient, col), …], shorter than the pieces left when
    one down the line cannot spawn; None otherwise."""
    if i >= len(upcoming):
        return 0.0, None
    pt = upcoming[i]
    if not _can_spawn(geo, b, pt, 0, SPAWN_ROW, spawn_c):
        return LOOKAHEAD_TOP_OUT_PENALTY, None
    cands = _expand(geo, b, st, fo, pt, SPAWN_ROW, spawn_c, well, high)
    if not cands:
        return LOOKAHEAD_TOP_OUT_PENALTY, None
    if i == len(upcoming) - 1:
        if keep:
            c = max(cands, key=_score)  # the first of the best, as a stable sort would have it
            return c[0], [(c[1], c[2])]
        return max(c[0] for c in cands), None
    cands.sort(key=_neg_score)
    best = None
    best_line = None
    for cand in cands[:LOOKAHEAD_BEAM]:
        nst, nfo = _after(geo, st, fo, cand)
        rest, line = _lookahead(geo, cand[5], nst, nfo, spawn_c, upcoming, i + 1, well, high, keep)
        total = cand[0] + rest
        if best is None or total > best:
            best = total
            if keep:
                best_line = [(cand[1], cand[2])] + (line or [])
    return best, best_line


def _neg_score(c):
    return -c[0]


def _score(c):
    return c[0]


def lookahead(g, spawn_c, upcoming, ws=NO_WELL):
    """lookahead on a Grid (the packed search underneath), for a crew
    keeping a well (ws; NO_WELL: none)."""
    geo = Geometry.of(g.h, g.w)
    b, st, fo = geo.pack(g)
    return _lookahead(geo, b, st, fo, spawn_c, list(upcoming), 0, ws[0], ws[1])[0]


def principal_variation(g, spawn_c, upcoming, ws=NO_WELL):
    """The line the lookahead found best from board g: one (orient, col) per
    upcoming piece, in order: where the planner means to put the pieces its
    NEXT well shows, as things stand. Shorter than `upcoming` when a piece
    down the line cannot spawn."""
    geo = Geometry.of(g.h, g.w)
    b, st, fo = geo.pack(g)
    return _lookahead(geo, b, st, fo, spawn_c, list(upcoming), 0, ws[0], ws[1], True)[1] or []


def plan_roots(g, pt, s_row, s_col, spawn_c, upcoming, proj=None, s_orient=0):
    """The first half of a plan: every placement reachable with kick-free
    rotations at the piece's current position (s_row, s_col) and orientation
    (s_orient: a piece planned again where it stands has turned since its
    spawn) plus sideways slides plus a hard drop (a legal subset of the
    game's moves), each with its one-ply Dellacherie score. Returns (placements, the indices of the
    PLAN_BEAM best of them, one lookahead job per such placement): a job is
    the arguments of lookahead_job, the search over `upcoming` (which spawn
    fresh at spawn_c: the caller's own section spawn on shared boards) from
    the board that placement leaves. No jobs without a preview."""
    geo = Geometry.of(g.h, g.w)
    b, st, fo = geo.pack(g)
    well, high = proj.well_spec() if proj is not None else NO_WELL
    cands = _expand(geo, b, st, fo, pt, s_row, s_col, well, high, s_orient)
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
        jobs.append((g.h, g.w, cands[i][5], nst, nfo, spawn_c, upcoming, well, high))
    return out, order, jobs


def lookahead_job(h, w, b, st, fo, spawn_c, upcoming, well=-1, high=False):
    """One of plan_roots' jobs: the lookahead's total from a packed board,
    and the line that makes it (principal_variation's, for the claim's
    `next`). A plain function of plain integers, so it can run in another
    process."""
    return _lookahead(Geometry.of(h, w), b, st, fo, spawn_c, upcoming, 0, well, high, True)


def plan_finish(out, order, totals, proj=None):
    """The second half of a plan: the lookahead's totals (one per job, in
    order; a job's own answer, total and line, will do, and the line is then
    kept on the placement) added to the beam's placements, the coordinated
    board's penalties taken off, and the placements returned best first. A
    placement outside the beam keeps its one-ply score and ranks below every
    placement in it (the lookahead's sum is what scores are made of), in its
    one-ply order."""
    if totals:
        floor = float("inf")
        for i, la in zip(order, totals):
            if isinstance(la, tuple):  # a job's (total, line)
                la, line = la
                out[i].pv = tuple(line or ())
            out[i].score += la
            floor = min(floor, la)
        for i in order[len(totals):]:
            out[i].score += floor - 1
    if proj is not None:
        for pl in out:
            pl.score -= proj.penalty(pl)
    out.sort(key=lambda pl: -pl.score)
    return out


def plan_placements(g, pt, s_row, s_col, spawn_c, upcoming, proj=None, s_orient=0):
    """A whole plan, here and now: every placement of the piece (standing in
    the orientation s_orient) scored with Dellacherie + the lookahead over
    `upcoming`, less, on a coordinated board (proj), the placement's penalty;
    best first."""
    out, order, jobs = plan_roots(g, pt, s_row, s_col, spawn_c, upcoming, proj, s_orient)
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
