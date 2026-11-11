"""The rising floor: its pace, the rows a raise brings and where their holes
go, pinned against the game's own values."""

from python_mk2.survival import (MIN_SURVIVAL_HOLES, normalize_survival, survival_holes, survival_interval,
                                 survival_interval_ns, survival_raise_rows)


def test_survival_curve_parity():
    ms = 1_000_000
    cases = [
        ("too_easy", 1, 20000), ("too_easy", 8, 14000), ("too_easy", 15, 8000),
        ("super_easy", 1, 10000), ("super_easy", 8, 7000), ("super_easy", 15, 4000),
        ("very_easy", 1, 5000), ("very_easy", 8, 3500), ("very_easy", 15, 2000),
        ("easy", 1, 2500), ("easy", 8, 1750), ("easy", 15, 1000),
        ("normal", 1, 3000), ("normal", 8, 2250), ("normal", 15, 1500),
        ("hard", 1, 5000), ("hard", 8, 3000), ("hard", 15, 1000),
        ("easy", 0, 2500), ("easy", 99, 1000), ("", 5, 0), ("lunatic", 5, 0),
    ]
    for tier, level, want in cases:
        assert survival_interval_ns(tier, level) == want * ms, (tier, level)
        assert survival_interval(tier, level) == want / 1000, (tier, level)
    assert normalize_survival("hard") == "hard" and normalize_survival("too_easy") == "too_easy"
    assert normalize_survival("Hard") == "" and normalize_survival("very easy") == "" and normalize_survival("") == ""


def test_survival_rows_parity():
    # Easy and below one, Hard four, Normal the seeded draw: the twenty
    # raises of seed 42.
    want = [4, 4, 3, 4, 4, 3, 4, 1, 1, 2, 2, 1, 1, 3, 1, 1, 3, 2, 2, 4]
    for i, n in enumerate(want):
        assert survival_raise_rows("normal", 42, i + 1) == n, i + 1
        for tier in ("too_easy", "super_easy", "very_easy", "easy"):
            assert survival_raise_rows(tier, 42, i + 1) == 1
        assert survival_raise_rows("hard", 42, i + 1) == 4
    assert survival_raise_rows("", 42, 1) == 0


def test_survival_holes_parity():
    # The seeded well (rows 0-3 share a column, the next four another) and
    # the random draw, from the same seed and ordinals.
    well = []
    for h in survival_holes(10, 1, 0, 12, False, 42):
        assert len(h) == 1
        well.append(h[0])
    assert well == [7, 7, 7, 7, 3, 3, 3, 3, 1, 1, 1, 1]
    assert survival_holes(14, 2, 5, 6, True, 42) == [[7, 8], [8, 11], [1, 2], [3, 5], [1, 12], [5, 12]]
    # Chunked as the raises would come, the same draws.
    chunked = []
    for first, rows in ((0, 3), (3, 1), (4, 4), (8, 4)):
        chunked += survival_holes(10, 1, first, rows, False, 42)
    assert chunked == survival_holes(10, 1, 0, 12, False, 42)
    assert len(survival_holes(10, 0, 0, 1, False, 42)[0]) == MIN_SURVIVAL_HOLES
    assert len(survival_holes(3, 4, 0, 1, False, 42)[0]) == 2
    assert survival_holes(10, 1, 0, 0, False, 42) is None
