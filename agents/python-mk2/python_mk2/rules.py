"""Game-shape rules the lobby and the game both read (gameplays §2).

Pure helpers: the clamps a meta's settings go through and the defaults a
missing field reads as. No NATS, no game state.
"""

from .pieces import WIDTH

# A shared board (cooperative, or one team's board) is WIDTH columns for its
# first seat and the game's `extra_columns` more for every seat after it: the
# creator's board-width setting, between MIN_EXTRA_COLUMNS and
# MAX_EXTRA_COLUMNS. The same step spaces the seats' spawn points, so seat i
# spawns at i x extra + SPAWN_COL.
MIN_EXTRA_COLUMNS = 4
MAX_EXTRA_COLUMNS = WIDTH


def extra_columns(v):
    """Clamp the meta's extra_columns to its legal range. Absent (zero, as
    every game created before the setting existed reads) means the historical
    board: a full WIDTH section per seat, i.e. MAX_EXTRA_COLUMNS."""
    if v <= 0:
        return MAX_EXTRA_COLUMNS
    return min(max(v, MIN_EXTRA_COLUMNS), MAX_EXTRA_COLUMNS)


# A shared board is the standard height (headroom included) for its first
# seat and the game's `extra_rows` more for every seat after it, between
# MIN_EXTRA_ROWS and MAX_EXTRA_ROWS. The headroom stays the top rows: the
# board grows downwards.
MIN_EXTRA_ROWS = 0
MAX_EXTRA_ROWS = 10


def extra_rows(v):
    """Clamp the meta's extra_rows to its legal range. Absent means exactly
    that: no extra rows, unlike the columns."""
    return min(max(v, MIN_EXTRA_ROWS), MAX_EXTRA_ROWS)


# The number of teams a teams game is played between (meta team_count). Two
# is the default and what every meta written before the field reads as; team
# indices run 0..count-1.
DEFAULT_TEAM_COUNT = 2
MIN_TEAM_COUNT = 2
MAX_TEAM_COUNT = 6


def normalize_team_count(n):
    """A meta's team_count: absent is the historical two, out of range is
    clamped."""
    if n <= 0:
        return DEFAULT_TEAM_COUNT
    return min(max(n, MIN_TEAM_COUNT), MAX_TEAM_COUNT)


def team_names(n):
    """Name n teams after the piece colours in piece order: the default
    (meta team_names) when the creator renames none."""
    colors = ["Green", "Blue", "Amber", "Violet", "Teal", "Coral", "Navy"]
    return [colors[t % len(colors)] for t in range(n)]


# The cap on holes per garbage row (meta garbage_holes).
MAX_GARBAGE_HOLES = 4

# The floor of a survival game's garbage_holes: a row the floor raises must
# be clearable, or the board would only ever fill.
MIN_SURVIVAL_HOLES = 1

SURVIVAL_TIERS = ("too_easy", "super_easy", "very_easy", "easy", "normal", "hard")


def normalize_survival(s):
    """A recorded survival tier: the six tiers as themselves, anything else
    (absent, an unknown word) as no rising floor."""
    return s if s in SURVIVAL_TIERS else ""
