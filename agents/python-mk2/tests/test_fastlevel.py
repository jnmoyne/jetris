"""A piece planned again where it stands: its placements are counted from the
orientation it is in, so a piece wedged in a turned one still has somewhere
to go: with none at all a plan reads as a top-out, and ends the game."""

import random
import time

import pytest
from helpers import coord_test_game, lock_column, make_game
from test_planner import random_grid
from test_routes import peer_piece

from python_mk2.blackboard import CLAIM_VERSION, PHASE_PLANNED, Claim
from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.coord import (BLOCK_PATIENCE_SILENT, DEP_WAIT_MIN, DEP_WAIT_TIMEOUT, LOCK_DELAY, PAUSE_ROWS,
                              DepWait)
from python_mk2.difficulty import difficulty_tuning
from python_mk2.engine import Geometry
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW, piece_cells
from python_mk2.planner import (NO_WELL, ROUTE_BLOCKED, Placement, Projection, _expand, enumerate_placements,
                                plan_placements)
from python_mk2.shared import LINES_PER_LEVEL, MAX_LEVEL, gravity_interval
from python_mk2.wire import WireCell


def wedged_t(g):
    """A T piece standing in orientation 3 in a slot none of its other
    orientations fit in: the cell to its right is filled, which is in the way
    of orientations 0, 1 and 2 at that anchor alike."""
    p = Active(2, 3, 10, 4)
    g.locked[(p.row + 1, p.col + 2)] = WireCell(o=True, t=1)
    return p


def test_enumerate_from_the_orientation_the_piece_stands_in():
    g = make_game(w=10, h=24)
    p = wedged_t(g)
    gr = g.to_grid()
    assert gr.can_place(p.cells()), "the piece does not fit where the test stands it"
    for o in range(3):
        assert not gr.can_place(Active(p.pt, o, p.row, p.col).cells()), f"orientation {o} fits: the piece is not wedged"
    assert enumerate_placements(gr, p.pt, p.row, p.col) == [], "the test's premise: none from the spawn orientation"
    got = enumerate_placements(gr, p.pt, p.row, p.col, p.orient)
    assert got, "from the orientation the piece stands in: no placement at all"
    assert all(pl.orient == p.orient for pl in got), "an orientation the piece cannot turn to"
    assert any(pl.col == p.col for pl in got), "the column the piece stands in is not among its placements"
    ranked = plan_placements(gr, p.pt, p.row, p.col, 3, [], None, p.orient)
    assert sorted((pl.orient, pl.col, pl.drop_row) for pl in ranked) == sorted(
        (pl.orient, pl.col, pl.drop_row) for pl in got)
    assert plan_placements(gr, p.pt, p.row, p.col, 3, [], None) == []


def test_packed_search_matches_the_reference_from_any_orientation():
    rnd = random.Random(7)
    checked = turned = 0
    for _ in range(600):
        g = random_grid(rnd, kinds=(1, 1, 1, 2, 3, 4))
        pt = rnd.randrange(7)
        s_row = rnd.choice([SPAWN_ROW, rnd.randrange(0, g.h - 4), rnd.randrange(0, g.h - 4)])
        s_col = rnd.randrange(-2, g.w)
        s_orient = rnd.randrange(4)
        ref = enumerate_placements(g, pt, s_row, s_col, s_orient)
        geo = Geometry.of(g.h, g.w)
        b, st, fo = geo.pack(g)
        got = _expand(geo, b, st, fo, pt, s_row, s_col, NO_WELL[0], NO_WELL[1], s_orient)
        assert [(c[1], c[2], c[3]) for c in got] == [(pl.orient, pl.col, pl.drop_row) for pl in ref]
        if g.can_place(Active(pt, s_orient, s_row, s_col).cells()):
            assert ref and ref[0].orient == s_orient and ref[0].col == s_col, "the piece's own spot comes first"
            turned += s_orient != 0
        checked += len(ref)
    assert checked > 3000 and turned > 50
    # At its spawn a piece stands in orientation 0: the default.
    g = random_grid(random.Random(3))
    for pt in range(7):
        assert enumerate_placements(g, pt, SPAWN_ROW, 3) == enumerate_placements(g, pt, SPAWN_ROW, 3, 0)


async def test_replan_of_a_wedged_piece_is_not_a_top_out():
    """With nothing to choose from, plan reports a top-out, and play_pieces
    ends the game on it: for the whole crew, on a cooperative board, however
    low the stack."""
    g = make_game(w=10, h=24)
    p = wedged_t(g)
    pl, _, ok = await g.plan(p, 0)
    assert ok, "no placement for a piece standing on a nearly empty board: read as a top-out"
    assert pl.orient == p.orient


# ---- gravity outruns the fixed time constants (level 12 on) ----------------


def at_level(g, level):
    """Set a cooperative crew's level."""
    g.mode = MODE_COOPERATIVE
    g.total_lines = (level - 1) * LINES_PER_LEVEL


def hard_coord_game():
    g = coord_test_game()
    g.a.tn = difficulty_tuning("hard")
    return g


def test_think_pause_follows_gravity():
    # Gravity does not wait for the think pause: the pause is the
    # difficulty's while the level leaves it that long, and no longer than a
    # few rows take once it does not.
    g = hard_coord_game()
    g.a.rng = None
    at_level(g, 5)
    assert g.think_pause() == g.a.tn.piece_delay
    at_level(g, MAX_LEVEL)
    want = PAUSE_ROWS * gravity_interval(MAX_LEVEL)
    assert g.think_pause() == want and want < g.a.tn.piece_delay / 2
    g.a.rng = random.Random(1)
    for _ in range(50):
        assert want * 2 / 3 <= g.think_pause() <= want * 4 / 3, "jittered by a third of the capped pause"


def test_the_plan_is_priced_from_where_the_piece_will_be():
    # The walk starts where gravity will have taken the piece by the time it
    # moves, and the plan is priced from there: a column the stack walls off
    # from that depth is out of reach, however open the spawn row is.
    g = hard_coord_game()
    at_level(g, MAX_LEVEL)
    now = time.monotonic()
    p = Active(1, 0, SPAWN_ROW, g.spawn_c)  # an O
    wall = g.spawn_c + 4
    lock_column(g, wall, 8)  # a wall from row 8 down, a column clear of the piece
    far = (0, wall + 1)

    assert g.fallen_at(p, now) == p, "no gravity clock yet: the piece is where it stands"
    g.next_gravity = now
    step = g.gravity_now()
    start = g.fallen_at(p, now + 9.5 * step)
    assert start == Active(p.pt, p.orient, p.row + 10, p.col), "ten rows owed"
    g.gravity_debt = 2
    assert g.fallen_at(p, now - 1).row == p.row + 2, "two rows of debt, no deadline passed"
    g.gravity_debt = 0
    floor = g.fallen_at(p, now + 60)
    assert g.can_place(floor.cells()) and not g.can_place(Active(floor.pt, floor.orient, floor.row + 1, floor.col).cells())

    assert far in g.route_costs(p, now, 0), "from the spawn row the column beyond the wall is reached (the premise)"
    costs = g.route_costs(start, now, 0)
    assert far not in costs, "from under the wall's top the column beyond it is priced as a walk"
    assert (0, g.spawn_c - 2) in costs, "a column on the piece's own side is still reached"
    proj = Projection(route=costs, route_now=start != p)
    assert proj.penalty(Placement(orient=far[0], col=far[1])) >= ROUTE_BLOCKED


def test_waits_end_while_the_piece_has_air():
    # A wait goes on while gravity does. It ends while the piece still has
    # air to walk in (a claim's estimate or the right of way is no use to a
    # piece on the stack) and is what it always was where the level leaves
    # the time, or the piece stands on something.
    g = hard_coord_game()
    now = time.monotonic()
    g.my_prio = 20
    p = Active(1, 0, SPAWN_ROW, g.spawn_c)
    g.piece = p
    peer_piece(g, 1, Active(0, 0, SPAWN_ROW, 7))
    g.claims[1] = Claim(v=CLAIM_VERSION, seat=1, piece=3, type=0, rot=0, col=9, phase=PHASE_PLANNED, eta_ms=400, rev=30)
    g.deps = [DepWait(seat=1, piece=3, rev=30, cells=list(piece_cells(0, 0, 22, 9)))]

    at_level(g, 5)
    assert g.dep_timeout() == pytest.approx(0.8), "level 5: twice the 400 ms estimate"
    assert g.block_patience([1], now) == DEP_WAIT_TIMEOUT, "level 5, a later claim: theirs to move"

    at_level(g, 14)
    _, fell, _ = g.fall_rows(p, g.height())
    air = fell * g.gravity_now()
    assert fell >= 10 and air < DEP_WAIT_MIN, "the air is shorter than the shortest wait (the premise)"
    assert g.dep_timeout() == pytest.approx(air)
    assert g.block_patience([1], now) == pytest.approx(air), "a later claim"
    del g.claims[1]
    assert g.block_patience([1], now) == pytest.approx(air), "a silent piece"

    # Standing on the floor, the piece is not sinking: the wait is the claim's.
    g.next_gravity = now
    g.piece = g.fallen_at(p, now + 60)
    assert g.block_patience([1], now) == BLOCK_PATIENCE_SILENT


def test_lock_delay_follows_gravity():
    # The lock delay is the time a crewmate's piece needs to fall out of the
    # way: half a second at most, and less at a level that drops it sooner.
    g = hard_coord_game()
    at_level(g, 5)
    assert g.lock_delay_now() == LOCK_DELAY
    at_level(g, 13)
    d13 = g.lock_delay_now()
    at_level(g, MAX_LEVEL)
    d15 = g.lock_delay_now()
    assert d15 == pytest.approx(g.height() * gravity_interval(MAX_LEVEL))
    assert 4 * g.a.tn.move_delay <= d15 < d13 < LOCK_DELAY
