"""Per-difficulty tuning."""

from dataclasses import dataclass

# The game's maximum piece-preview depth (jetris-gameplays.md); "hard" plans
# with the full revealed preview, whatever a given game exposes.
MAX_NEXT_COUNT = 6


@dataclass(frozen=True)
class Tuning:
    """The per-difficulty knobs. piece_delay is the "think" pause after a new
    piece appears; move_delay paces the walks (a whole path to the planned
    column goes out as one batch); the blunder knobs model weaker play;
    lookahead caps how many revealed preview pieces the planner uses (further
    bounded by what the game actually reveals: a no-preview game yields none
    at any level). Delays are in seconds."""

    piece_delay: float
    move_delay: float
    blunder_rate: float = 0.0
    blunder_depth: int = 0
    lookahead: int = 0


def difficulty_tuning(d):
    """The standard knobs for "easy"/"medium"/"hard"."""
    if d == "easy":
        return Tuning(1.5, 0.3, 0.30, 4, 0)
    if d == "medium":
        return Tuning(0.6, 0.15, 0.10, 2, 1)
    return Tuning(0.1, 0.03, 0.0, 0, MAX_NEXT_COUNT)  # hard


def valid_difficulty(s):
    """Normalize and validate a difficulty label."""
    d = s.lower()
    if d in ("easy", "medium", "hard"):
        return d
    raise ValueError(f'unknown difficulty "{s}" (want easy, medium or hard)')
