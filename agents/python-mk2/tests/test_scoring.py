"""The scoring tables, the level, the speed curve and gravity's fall."""

from helpers import make_game

from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.path import Active
from python_mk2.scoring import SPIN_FULL, SPIN_MINI, ClearInfo, drop_points
from python_mk2.shared import MAX_LEVEL, MIN_LEVEL, gravity_interval, level_of
from python_mk2.wire import WireCell


def test_clear_info_points_and_attack():
    # (name, clear, points at level 1, rows under the Modern garbage table)
    cases = [
        ("nothing", ClearInfo(), 0, 0),
        ("single", ClearInfo(lines=1), 100, 0),
        ("double", ClearInfo(lines=2), 300, 1),
        ("triple", ClearInfo(lines=3), 500, 2),
        ("quad", ClearInfo(lines=4), 800, 4),
        ("b2b quad", ClearInfo(lines=4, back_to_back=True), 1200, 6),
        ("a single is never b2b", ClearInfo(lines=1, back_to_back=True), 100, 0),
        ("combo 3 single", ClearInfo(lines=1, combo=3), 250, 0),
        ("quad perfect clear", ClearInfo(lines=4, perfect=True), 2800, 14),
        ("b2b quad perfect clear", ClearInfo(lines=4, back_to_back=True, perfect=True), 4400, 16),
        ("single perfect clear", ClearInfo(lines=1, perfect=True), 900, 10),
        ("t-spin no lines", ClearInfo(spin=SPIN_FULL), 400, 0),
        ("mini t-spin single", ClearInfo(lines=1, spin=SPIN_MINI), 200, 0),
        ("t-spin double", ClearInfo(lines=2, spin=SPIN_FULL), 1200, 4),
        ("b2b t-spin double", ClearInfo(lines=2, spin=SPIN_FULL, back_to_back=True), 1800, 6),
        ("b2b t-spin triple", ClearInfo(lines=3, spin=SPIN_FULL, back_to_back=True), 2400, 9),
        ("b2b mini t-spin double", ClearInfo(lines=2, spin=SPIN_MINI, back_to_back=True), 600, 2),
    ]
    for name, c, points, rows in cases:
        assert c.points(1) == points, name
        assert c.points(3) == 3 * points, name
        assert c.attack_rows(True) == rows, name
        assert c.attack_rows(False) == max(c.lines, 0), name
    assert drop_points(3, 7) == 17
    assert ClearInfo(lines=4, back_to_back=True, combo=2).name() == "quad, back-to-back, combo 2"


async def test_score_level():
    # The level a clear is scored at is the level gravity falls at: the
    # seat's own lines' in competitive, the shared progression's on a shared
    # board; 1 at the start, one more every ten lines.
    g = make_game(mode=MODE_COMPETITIVE)
    assert g.score_level() == 1 and g.level() == 1
    g.lines = 23
    assert g.score_level() == 3
    assert g.level() == 3
    coop = make_game(mode=MODE_COOPERATIVE, total_lines=45)
    assert coop.score_level() == 5 and coop.level() == 5
    teams = make_game(mode=MODE_TEAMS, team=1, team_lines=[80, 30])
    assert teams.level() == 4  # the OWN team's 30 lines
    assert level_of(200) == MAX_LEVEL


def test_gravity_curve():
    # (0.8 − (L − 1) × 0.007)^(L − 1) seconds at level L, levels 1 to 15.
    want_micros = [1000000, 793000, 617796, 472729, 355197, 262004, 189677, 134735, 93882, 64152, 42976, 28218,
                   18153, 11439, 7059]
    for i, us in enumerate(want_micros):
        assert round(gravity_interval(MIN_LEVEL + i) * 1e6) == us, MIN_LEVEL + i
    assert gravity_interval(0) == gravity_interval(MIN_LEVEL)
    assert gravity_interval(99) == gravity_interval(MAX_LEVEL)


async def test_fall_rows():
    # The rows gravity owes taken as one move: as far as the board allows,
    # stopping at the stack (the piece will lock) or at another player's
    # falling piece (transient: it waits).
    g = make_game(w=10, h=24)
    o = Active(1, 0, 4, 4)  # an O piece: rows 4-5, columns 4-5
    bottom = max(r for r, _ in o.cells())

    to, fell, transient = g.fall_rows(o, 3)
    assert fell == 3 and not transient and to.row == o.row + 3
    # The stack two rows down: one row falls, and the piece is on the stack,
    # not transient.
    for c in range(g.w):
        g.locked[(bottom + 2, c)] = WireCell(o=True, t=1)
    to, fell, transient = g.fall_rows(o, 3)
    assert fell == 1 and not transient and to.row == o.row + 1
    # A teammate's falling piece two rows down instead: one row, then wait.
    g.locked = {}
    for c in range(g.w):
        g.others_act[(bottom + 2, c)] = 1
    to, fell, transient = g.fall_rows(o, 3)
    assert fell == 1 and transient and to.row == o.row + 1
    # Owed nothing: nothing moves, and nothing is reported blocked.
    to, fell, transient = g.fall_rows(o, 0)
    assert fell == 0 and not transient and to == o
