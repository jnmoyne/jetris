"""The Modern scoring and garbage tables (https://tetris.wiki/Scoring,
https://tetris.wiki/Garbage): the same rules every engine on the board plays
by. Every mode scores by them; the garbage table applies to games created
with the guideline_garbage rule.
"""

from dataclasses import dataclass

# How a T came to rest: not a T-spin, a Mini, or a full T-spin. This agent
# never makes one (it rotates, shifts and drops, so a rotation is never its
# last move) but a clear carries the field so the table reads like every
# other engine's (the line_clear event's t_spin).
SPIN_NONE, SPIN_MINI, SPIN_FULL = 0, 1, 2


@dataclass
class ClearInfo:
    """One lock's line clear (or the lack of one) with everything it is
    valued by."""

    lines: int = 0
    spin: int = SPIN_NONE
    back_to_back: bool = False  # a difficult clear right after another difficult clear
    combo: int = 0              # consecutive line-clearing locks before this one
    perfect: bool = False       # the clear left no settled cell on the board (garbage included)

    def difficult(self):
        """A quad, or any T-spin that cleared lines: the clears that start and
        extend a Back-to-Back chain. Only a plain single, double or triple
        breaks the chain; a lock that clears nothing leaves it alone."""
        return self.lines == 4 or (self.spin != SPIN_NONE and self.lines > 0)

    def n(self):
        return min(max(self.lines, 0), 4)

    def spin_kind(self):
        """The T-spin the tables know: a three-line T-spin is always a full
        T-spin triple, and a T cannot clear four lines."""
        if self.spin == SPIN_MINI and self.lines >= 3:
            return SPIN_FULL
        if self.spin != SPIN_NONE and self.lines >= 4:
            return SPIN_NONE
        return self.spin

    def points(self, level):
        """What the clear scores at level (1-based, the level BEFORE the
        clear): Single 100, Double 300, Triple 500, quad 800; Mini T-Spin
        100/200/400 for none/1/2 lines; T-Spin 400/800/1200/1600 for
        none/1/2/3; one and a half times for a Back-to-Back difficult clear,
        plus 50 per combo count, plus the perfect-clear bonus (800/1200/1800/
        2000 for 1-4 lines, 3200 for a Back-to-Back quad), all multiplied by
        the level. Drop points are not part of it (drop_points)."""
        level = max(level, 1)
        n = self.n()
        kind = self.spin_kind()
        if kind == SPIN_FULL:
            pts = (400, 800, 1200, 1600)[n]
        elif kind == SPIN_MINI:
            pts = (100, 200, 400)[n]
        else:
            pts = (0, 100, 300, 500, 800)[n]
        if self.back_to_back and self.difficult():
            pts = pts * 3 // 2
        if n > 0 and self.combo > 0:
            pts += 50 * self.combo
        if self.perfect and n > 0:
            if n == 4 and self.back_to_back:
                pts += 3200
            else:
                pts += (0, 800, 1200, 1800, 2000)[n]
        return pts * level

    def attack_rows(self, guideline):
        """The garbage the clear owes the opponents: one row per cleared line,
        or under the guideline_garbage rule the Modern table: a single sends
        nothing, a double 1, a triple 2, a quad 4; a Mini T-Spin single
        nothing and a Mini double 1; a T-Spin single 2, double 4, triple 6; a
        Back-to-Back difficult clear adds 1 (Mini single/double, T-Spin
        single), 2 (T-Spin double, quad) or 3 (T-Spin triple); a perfect clear
        adds 10."""
        if self.lines <= 0:
            return 0
        if not guideline:
            return self.lines
        n = self.n()
        kind = self.spin_kind()
        b2b = 0
        if kind == SPIN_FULL:
            rows, b2b = (0, 2, 4, 6)[n], (0, 1, 2, 3)[n]
        elif kind == SPIN_MINI:
            rows, b2b = (0, 0, 1)[n], 1
        else:
            rows = (0, 0, 1, 2, 4)[n]
            if n == 4:
                b2b = 2
        if self.back_to_back and self.difficult():
            rows += b2b
        if self.perfect:
            rows += 10
        return rows

    def name(self):
        """The clear's name, for the log."""
        n = self.n()
        name = ("nothing", "single", "double", "triple", "quad")[n]
        kind = self.spin_kind()
        if kind == SPIN_FULL:
            name = "t-spin " + name
        elif kind == SPIN_MINI:
            name = "mini t-spin " + name
        if self.back_to_back:
            name += ", back-to-back"
        if self.combo > 0:
            name += f", combo {self.combo}"
        if self.perfect:
            name += ", perfect clear"
        return name


def drop_points(soft_cells, hard_cells):
    """What the piece earned on its way down: one point per cell soft-dropped,
    two per cell hard-dropped, never multiplied by the level. This agent
    hard-drops (or, with --soft-drop, soft-drops) or lets gravity carry the
    piece, which earns nothing."""
    return max(soft_cells, 0) + 2 * max(hard_cells, 0)


_STAT_FIELDS = ("singles", "doubles", "triples", "quads", "t_spins", "mini_t_spins",
                "back_to_backs", "max_combo", "perfect_clears", "attack", "played_ms")


@dataclass
class PlayerStats:
    """A seat's tally of what its locks did over the game (the archive
    record's per-player `stats`): one count per name a line_clear carries,
    folded from every seat's events (our own at publish time), the garbage
    rows its clears sent by the game's attack rule, and how long it played."""

    singles: int = 0
    doubles: int = 0
    triples: int = 0
    quads: int = 0
    t_spins: int = 0         # full T-spins and 180 spins that scored
    mini_t_spins: int = 0    # Mini T-spins that scored
    back_to_backs: int = 0
    max_combo: int = 0
    perfect_clears: int = 0
    attack: int = 0          # garbage rows sent (competitive and teams)
    played_ms: int = 0       # from the start to the seat's game_over, or to the finish

    def tally(self, c, spin, attacks, guideline):
        """Count one lock: the clear by its size, the spin that made it, the
        Back-to-Back, the longest combo, the perfect clear, and, where the
        game attacks (competitive and teams), the rows it sent by the game's
        rule. spin is the event's t_spin, kept apart from the tables' reading
        of it: a Mini that cleared three lines scored as a full T-spin but
        was a Mini."""
        if c.lines >= 4:
            self.quads += 1
        elif c.lines == 3:
            self.triples += 1
        elif c.lines == 2:
            self.doubles += 1
        elif c.lines == 1:
            self.singles += 1
        if spin == 1:
            self.mini_t_spins += 1
        elif spin in (2, 3):
            self.t_spins += 1
        if c.back_to_back and c.difficult():
            self.back_to_backs += 1
        if c.lines > 0:
            self.max_combo = max(self.max_combo, c.combo)
            if c.perfect:
                self.perfect_clears += 1
            if attacks:
                self.attack += c.attack_rows(guideline)

    def is_zero(self):
        return not any(getattr(self, f) for f in _STAT_FIELDS)

    def to_json(self):
        """The record's JSON: zero counts dropped."""
        return {f: getattr(self, f) for f in _STAT_FIELDS if getattr(self, f)}
