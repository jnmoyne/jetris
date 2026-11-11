"""The rising floor of a survival game (gameplays §2 "Survival", guide §4.4).

Garbage rows rise from the bottom of the crew's board on a clock of their
own, whatever the players do, and the game ends at the first top-out: the
time survived is the result. Six tiers set the pace, every one of them
quickening with the level (the board's lines, a raised row clearing like any
line once its holes are filled): the interval shrinks in a straight line from
the tier's start at level 1 to its end at level 15.

    too_easy    a row every 20 s → 8 s, one row at a time
    super_easy  every 10 s → 4 s, one row at a time
    very_easy   every 5 s → 2 s, one row at a time
    easy        every 2.5 s → 1.0 s, one row at a time
    normal      every 3.0 s → 1.5 s, one to four rows at a time (a seeded draw)
    hard        every 5.0 s → 1.0 s, four rows at a time

Everything about a raise is a function of the game's seed, so every engine on
the board agrees on it with nothing on the wire beyond the register's count
of raises: how many rows the k-th raise brings (survival_raise_rows) and
where every row's holes go (survival_holes).
"""

import time

from . import aio, jsx
from .rng import PCG
from .rules import MAX_GARBAGE_HOLES, MIN_SURVIVAL_HOLES, normalize_survival  # noqa: F401
from .wire import dumps, garbage_reg, log

# How many rows the floor raises through one hole column set before a new one
# is drawn: the well moves every four rows.
SURVIVAL_WELL_SPAN = 4

# The seed streams the survival draws run on, mixed into the game's seed.
SURVIVAL_ROWS_STREAM = 0x5375727620726F77
SURVIVAL_HOLE_STREAM = 0x53757276206F6C65

_MIN_LEVEL, _MAX_LEVEL = 1, 15
_NS = 1_000_000_000

# tier → (ns between raises at level 1, at level 15, min rows, max rows)
_PACES = {
    "too_easy": (20 * _NS, 8 * _NS, 1, 1),
    "super_easy": (10 * _NS, 4 * _NS, 1, 1),
    "very_easy": (5 * _NS, 2 * _NS, 1, 1),
    "easy": (2500 * _NS // 1000, 1 * _NS, 1, 1),
    "normal": (3 * _NS, 1500 * _NS // 1000, 1, 4),
    "hard": (5 * _NS, 1 * _NS, 4, 4),
}


def survival_interval_ns(tier, level):
    """The time between two raises at the given level, in whole nanoseconds:
    the tier's start at level 1, its end at level 15, a straight line
    between. A level outside the range reads as the nearer bound; zero for no
    rising floor."""
    pace = _PACES.get(tier)
    if pace is None:
        return 0
    start, end = pace[0], pace[1]
    level = min(max(level, _MIN_LEVEL), _MAX_LEVEL)
    return start - (start - end) * (level - _MIN_LEVEL) // (_MAX_LEVEL - _MIN_LEVEL)


def survival_interval(tier, level):
    """survival_interval_ns in seconds."""
    return survival_interval_ns(tier, level) / _NS


def survival_raise_rows(tier, seed, raise_no):
    """How many rows the raise_no-th raise (1-based) brings: the tier's fixed
    count, or a draw off the seed and the raise's number."""
    pace = _PACES.get(tier)
    if pace is None:
        return 0
    lo, hi = pace[2], pace[3]
    if hi == lo:
        return lo
    return lo + PCG(seed ^ SURVIVAL_ROWS_STREAM, raise_no).int_n(hi - lo + 1)


def survival_holes(width, holes, first_row, rows, random_holes, seed):
    """The hole columns of the rows a raise lands, top to bottom. Every
    raised row has an ordinal (first_row is the board's count of rows applied
    so far, the raise's rows the ordinals after it) and the rows of one well
    (SURVIVAL_WELL_SPAN consecutive ordinals) share one draw, so the holes
    line up into a well that moves every four rows whichever raises the rows
    came in; with random_holes set, every row draws its own. The count is
    floored at MIN_SURVIVAL_HOLES and capped like any garbage row's. None for
    no rows."""
    holes = min(max(holes, MIN_SURVIVAL_HOLES), MAX_GARBAGE_HOLES, width - 1)
    if rows <= 0 or holes <= 0:
        return None
    out = []
    for k in range(rows):
        n = first_row + k
        stream = n if random_holes else n // SURVIVAL_WELL_SPAN
        out.append(sorted(PCG(seed ^ SURVIVAL_HOLE_STREAM, stream).perm(width)[:holes]))
    return out


class SurvivalMixin:
    async def run_survival_clock(self):
        """The rising floor's clock, run beside the piece loop from the
        game's start until it ends (guide §4.4): a raise falls due
        survival_interval of the level after the last one landed. The raise
        is ONE write to the crew's garbage register (the next raise's rows
        added to the total owed, the count of raises advanced) under a
        per-subject CAS at the register's last-seen sequence (0 for a
        register never written). Every engine on the board keeps the same
        clock and fires within a round trip of the others; the CAS lets
        exactly one raise land per tick, and a lost race is NOT retried: the
        winner's echo (handle_board_msg's kick) is what every clock re-arms
        on, this one's included. The rows are then applied by whichever
        engine's gated shrink wins (apply_owed_garbage), like an attack's. A
        tick that finds the register moved since the clock was armed (a
        crewmate's raise folded a moment before it fired, its kick still
        queued) is a no-op, not a second raise on top of theirs. The level is
        re-read as the clock waits, so the floor quickens the moment the
        level does."""
        tier = self.survival
        subject = self.own_garbage_subject()
        anchor = time.monotonic()
        async with self.mu:
            armed_seq = self.garbage_seq
        while True:
            async with self.mu:
                level, seq, owed, raises, dead = (self.level(), self.garbage_seq, self.garbage_owed,
                                                  self.survival_raises, self.dead)
            if dead or self.is_ended():
                return
            wait = anchor + survival_interval(tier, level) - time.monotonic()
            if wait > 0:
                which = await aio.wait_any([self.ended, self.a.stop_ev, self.survival_kick], min(wait, 0.25))
                if which in (0, 1):
                    return
                if which == 2:
                    self.survival_kick.clear()
                    anchor = time.monotonic()
                    async with self.mu:
                        armed_seq = self.garbage_seq
                continue
            if seq != armed_seq:
                # A raise landed since the clock was armed: it re-arms from it.
                anchor, armed_seq = time.monotonic(), seq
                continue
            rows = survival_raise_rows(tier, self.meta_seed, raises + 1)
            if rows <= 0:
                return
            payload = dumps(garbage_reg(owed + rows, self.idx, raises + 1))
            try:
                _, conflict = await self.a.cas_request(subject, payload, {jsx.H_EXPECT_LAST: str(seq)})
                if not conflict:
                    log.info("survival: the floor rises %d row(s) (raise %d)", rows, raises + 1)
            except Exception as e:
                log.info("survival raise: %s", e)
            # Whether the raise was ours or a crewmate's beat it, its echo
            # re-arms the clock; a raise lost to the wire is tried again an
            # interval on.
            anchor = time.monotonic()
