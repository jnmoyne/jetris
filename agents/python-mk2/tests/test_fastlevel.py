"""A piece planned again where it stands: its placements are counted from the
orientation it is in, so a piece wedged in a turned one still has somewhere
to go: with none at all a plan reads as a top-out, and ends the game."""

import random

from helpers import make_game
from test_planner import random_grid

from python_mk2.engine import Geometry
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW
from python_mk2.planner import NO_WELL, _expand, enumerate_placements, plan_placements
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
