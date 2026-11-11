"""The planner's board.

Grid is a settled-cells board used only for planning: one cell state per
square, no falling piece. 0 = empty, 1 = solid stack, 2 = adversarial garbage
(filled for every feature; a row of nothing but garbage can never complete, a
garbage row whose holes the stack has filled clears like any other), 3 =
another player's falling piece (filled for every feature, and a row it
crosses never completes), 4 = a teammate's claimed landing: where their piece
is going to settle, by their claim on the blackboard or by its straight drop
(coord.build_projection): stack for every feature, a row it fills completing
like any other, since it will be stack by the time our piece lands (or we
wait until it is).

The cells are kept as bit masks, a row at a time (bit c set for column c):
every filled cell, and the kinds a clear cares about. Python's integers have
no width, so a board may be as wide as the game makes it.

Packed is the same board as three whole-board integers, a row every w+1 bits
(one spare bit a row, always clear): what the planner's search runs on, where
a feature is a handful of integer operations instead of a loop over rows.
"""

from .pieces import PIECES, piece_cells

GRID_CLAIMED = 4


class Grid:
    __slots__ = ("h", "w", "rows", "stack", "foreign", "claimed", "full")

    def __init__(self, h, w):
        self.h, self.w = h, w
        self.rows = [0] * h      # every filled cell
        self.stack = [0] * h     # kinds 1 and 4: what lets a full row clear
        self.foreign = [0] * h   # kind 3: what keeps a full row from clearing
        self.claimed = [0] * h   # kind 4
        self.full = (1 << w) - 1

    def at(self, r, c):
        bit = 1 << c
        if not self.rows[r] & bit:
            return 0
        if self.claimed[r] & bit:
            return GRID_CLAIMED
        if self.stack[r] & bit:
            return 1
        if self.foreign[r] & bit:
            return 3
        return 2

    def set(self, r, c, v):
        bit = 1 << c
        nbit = ~bit
        self.stack[r] &= nbit
        self.foreign[r] &= nbit
        self.claimed[r] &= nbit
        if v == 0:
            self.rows[r] &= nbit
            return
        self.rows[r] |= bit
        if v == 1:
            self.stack[r] |= bit
        elif v == 3:
            self.foreign[r] |= bit
        elif v == GRID_CLAIMED:
            self.stack[r] |= bit
            self.claimed[r] |= bit

    def filled(self, r, c):
        return self.rows[r] >> c & 1 == 1

    def clone(self):
        g = Grid.__new__(Grid)
        g.h, g.w, g.full = self.h, self.w, self.full
        g.rows, g.stack = self.rows[:], self.stack[:]
        g.foreign, g.claimed = self.foreign[:], self.claimed[:]
        return g

    def can_place(self, cells):
        """Whether the four cells are all in bounds and empty."""
        h, w, rows = self.h, self.w, self.rows
        for r, c in cells:
            if r < 0 or r >= h or c < 0 or c >= w or rows[r] >> c & 1:
                return False
        return True

    def drop_row(self, pt, orient, row, col):
        """The lowest row the piece can occupy in column col at orientation
        orient, starting the search from row."""
        while self.can_place(piece_cells(pt, orient, row + 1, col)):
            row += 1
        return row

    def completed_rows(self):
        """The indices of full rows that can actually clear: every column
        filled, at least one of them by the stack (a solid garbage row is
        permanent), and no foreign falling piece in the row."""
        full = self.full
        return [r for r in range(self.h)
                if self.rows[r] == full and self.stack[r] and not self.foreign[r]]

    def clear_rows(self, rows):
        """A new grid with the given rows removed and everything above each
        cleared row shifted down to fill the gap (standard gravity collapse)."""
        removed = set(rows)
        keep = [r for r in range(self.h) if r not in removed]
        pad = [0] * (self.h - len(keep))
        out = Grid.__new__(Grid)
        out.h, out.w, out.full = self.h, self.w, self.full
        out.rows = pad + [self.rows[r] for r in keep]
        out.stack = pad + [self.stack[r] for r in keep]
        out.foreign = pad + [self.foreign[r] for r in keep]
        out.claimed = pad + [self.claimed[r] for r in keep]
        return out

    # ---- Dellacherie's features, a row at a time ---------------------------

    def row_transitions(self):
        """Over every row, the changes between filled and empty going across
        it, the walls counting as filled."""
        wall = 1 << self.w
        n = 0
        for m in self.rows:
            n += ((m << 1 | 1) ^ (m | wall)).bit_count()
        return n

    def col_transitions(self):
        """Down every column, the changes between filled and empty, the space
        above the board counting as empty and the floor as filled."""
        rows = self.rows
        n = rows[0].bit_count()
        for r in range(1, self.h):
            n += (rows[r - 1] ^ rows[r]).bit_count()
        return n + (~rows[self.h - 1] & self.full).bit_count()

    def holes(self):
        """The empty cells with a filled cell somewhere above them."""
        n = covered = 0
        for m in self.rows:
            n += (covered & ~m).bit_count()
            covered |= m
        return n

    def wells(self):
        """Dellacherie's cumulative well depth: 1+2+…+d for a well of depth d.
        Every well cell (empty, walled on both sides) adds the empty cells
        from it down to the first filled one. One pass up the board: the run
        of empty cells below each column is kept as the pass climbs."""
        w, full = self.w, self.full
        last = 1 << (w - 1)
        run = [0] * w
        n = 0
        for r in range(self.h - 1, -1, -1):
            m = self.rows[r]
            well_cells = ~m & (m << 1 | 1) & (m >> 1 | last) & full
            for c in range(w):
                if m >> c & 1:
                    run[c] = 0
                    continue
                run[c] += 1
                if well_cells >> c & 1:
                    n += run[c]
        return n


# ---- the packed board --------------------------------------------------------


class Geometry:
    """Everything about an h×w board the packed search needs, computed once:
    the masks, and every piece's shape as a mask with its extents."""

    _cache = {}

    def __init__(self, h, w):
        self.h, self.w = h, w
        s = self.stride = w + 1
        self.full = (1 << w) - 1
        self.ones = sum(1 << (r * s) for r in range(h))          # column 0 of every row
        self.pads = self.ones << w                                # the spare bit of every row
        self.last = self.ones << (w - 1)                          # the last column of every row
        self.cells = self.full * self.ones                        # every cell of the board
        self.floor = self.full << (h * s)                         # the row below the board, filled
        self.low = [(1 << (r * s)) - 1 for r in range(h + 2)]     # every row above r
        self.row_mask = [self.full << (r * s) for r in range(h)]
        self.col_mask = [self.ones << c for c in range(w)]
        self.smears = []
        k = 1
        while k < h:
            self.smears.append(k * s)
            k *= 2
        # shapes[pt][orient] = (mask at anchor (0,0), min col, max col,
        # max row offset, sum of row offsets, ((col off, top off, bottom off), …),
        # the four (row off, col off), the distinct row offsets ascending)
        self.shapes = []
        for pt in range(7):
            per = []
            for o in range(4):
                offs = PIECES[pt][o]
                mask = 0
                cols = {}
                for ro, co in offs:
                    mask |= 1 << (ro * s + co)
                    top, bottom = cols.get(co, (ro, ro))
                    cols[co] = (min(top, ro), max(bottom, ro))
                per.append((
                    mask,
                    -min(co for _, co in offs),
                    w - 1 - max(co for _, co in offs),
                    max(ro for ro, _ in offs),
                    sum(ro for ro, _ in offs),
                    tuple((co, tb[0], tb[1]) for co, tb in sorted(cols.items())),
                    offs,
                    tuple(sorted({ro for ro, _ in offs})),
                ))
            self.shapes.append(per)

    @classmethod
    def of(cls, h, w):
        g = cls._cache.get((h, w))
        if g is None:
            g = cls._cache[(h, w)] = cls(h, w)
        return g

    def pack(self, grid):
        """A Grid as (filled, stack, foreign) whole-board integers."""
        s = self.stride
        b = st = f = 0
        for r in range(self.h):
            sh = r * s
            b |= grid.rows[r] << sh
            st |= grid.stack[r] << sh
            f |= grid.foreign[r] << sh
        return b, st, f

    def features(self, a):
        """Dellacherie's four board features of the packed board a: row
        transitions, column transitions, holes, wells (Grid's, each in a few
        whole-board operations)."""
        s = self.stride
        left = (a << 1) | self.ones                      # the left neighbour filled, the wall counting
        rt = (left ^ (a | self.pads)).bit_count()
        ct = ((a << s) ^ (a | self.floor)).bit_count()
        empty = self.cells ^ a
        cov = a
        for k in self.smears:
            cov |= cov << k
        holes = ((cov << s) & empty).bit_count()
        cur = left & ((a >> 1) | self.last) & empty      # the well cells
        wells = cur.bit_count()
        below = empty
        while cur:
            below >>= s
            cur &= below
            wells += cur.bit_count()
        return rt, ct, holes, wells

    def clear(self, a, rows):
        """a with the given rows (ascending) removed and the rows above each
        shifted down."""
        s, low = self.stride, self.low
        for r in rows:
            a = (a & ~low[r + 1]) | ((a & low[r]) << s)
        return a
