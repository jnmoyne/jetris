"""The planner's packed search against its reference form, and the features
against a cell-at-a-time reading of their definitions."""

import random

from python_mk2.engine import GRID_CLAIMED, Geometry, Grid
from python_mk2.pieces import SPAWN_COL, SPAWN_ROW, WIDTH, piece_cells
from python_mk2.planner import (LOOKAHEAD_BEAM, LOOKAHEAD_TOP_OUT_PENALTY, _expand, enumerate_placements,
                                evaluate_board, lookahead, plan_placements, revealed_pieces, simulate_lock)


def row_transitions_slow(g):
    n = 0
    for r in range(g.h):
        prev = True
        for c in range(g.w):
            cur = g.at(r, c) != 0
            if cur != prev:
                n += 1
            prev = cur
        if not prev:
            n += 1
    return n


def col_transitions_slow(g):
    n = 0
    for c in range(g.w):
        prev = False
        for r in range(g.h):
            cur = g.at(r, c) != 0
            if cur != prev:
                n += 1
            prev = cur
        if not prev:
            n += 1
    return n


def holes_slow(g):
    n = 0
    for c in range(g.w):
        covered = False
        for r in range(g.h):
            if g.at(r, c) != 0:
                covered = True
            elif covered:
                n += 1
    return n


def wells_slow(g):
    n = 0
    for c in range(g.w):
        for r in range(g.h):
            if g.at(r, c) != 0:
                continue
            left = c == 0 or g.at(r, c - 1) != 0
            right = c == g.w - 1 or g.at(r, c + 1) != 0
            if not (left and right):
                continue
            k = r
            while k < g.h and g.at(k, c) == 0:
                n += 1
                k += 1
    return n


def random_grid(rnd, h=None, w=None, kinds=(1,)):
    h = h or rnd.choice([24, 24, 28, 34])
    w = w or rnd.choice([10, 14, 18, 22, 40])
    g = Grid(h, w)
    top = rnd.randrange(6, h)
    density = rnd.uniform(0.3, 0.95)
    for r in range(top, h):
        for c in range(w):
            if rnd.random() < density:
                g.set(r, c, rnd.choice(kinds))
    return g


def test_features_match_the_cell_at_a_time_reading():
    rnd = random.Random(1)
    for _ in range(600):
        g = random_grid(rnd, kinds=(1, 1, 2, 3, 4))
        geo = Geometry.of(g.h, g.w)
        b, _, _ = geo.pack(g)
        want = (row_transitions_slow(g), col_transitions_slow(g), holes_slow(g), wells_slow(g))
        assert (g.row_transitions(), g.col_transitions(), g.holes(), g.wells()) == want
        assert geo.features(b) == want


def test_grid_kinds_and_clears():
    g = Grid(6, 4)
    for c in range(4):
        g.set(5, c, 2)          # solid garbage: full, never clears
        g.set(4, c, 1)          # stack: clears
        g.set(3, c, 1)
        g.set(2, c, GRID_CLAIMED if c else 2)   # claimed landing filling a garbage row: clears
    g.set(3, 0, 3)              # a foreign piece across row 3: it stays
    assert g.completed_rows() == [2, 4]
    assert [g.at(2, c) for c in range(4)] == [2, 4, 4, 4]
    out = g.clear_rows([2, 4])
    assert out.rows[:3] == [0, 0, 0]
    assert [out.at(4, c) for c in range(4)] == [3, 1, 1, 1]
    assert [out.at(5, c) for c in range(4)] == [2, 2, 2, 2]
    g.set(3, 0, 0)
    assert g.at(3, 0) == 0 and not g.filled(3, 0)


def test_packed_search_matches_the_reference():
    rnd = random.Random(2)
    checked = 0
    for _ in range(400):
        g = random_grid(rnd, kinds=(1, 1, 1, 2, 3, 4))
        # Headroom: keep the spawn rows mostly clear, sometimes not.
        pt = rnd.randrange(7)
        s_row = rnd.choice([SPAWN_ROW, SPAWN_ROW, rnd.randrange(0, g.h - 4)])
        s_col = rnd.randrange(-2, g.w)
        ref = enumerate_placements(g, pt, s_row, s_col)
        geo = Geometry.of(g.h, g.w)
        b, st, fo = geo.pack(g)
        got = _expand(geo, b, st, fo, pt, s_row, s_col)
        assert [(c[1], c[2], c[3]) for c in got] == [(p.orient, p.col, p.drop_row) for p in ref]
        for cand, pl in zip(got, ref):
            after, lines, eroded = simulate_lock(g, pl.dest)
            assert cand[4] == lines
            assert cand[0] == evaluate_board(after, pl.dest, lines, eroded)
            assert cand[5] == geo.pack(after)[0]
            checked += 1
    assert checked > 3000


def lookahead_reference(g, spawn_c, upcoming):
    if not upcoming:
        return 0.0
    pt = upcoming[0]
    if not g.can_place(piece_cells(pt, 0, SPAWN_ROW, spawn_c)):
        return LOOKAHEAD_TOP_OUT_PENALTY
    cands = enumerate_placements(g, pt, SPAWN_ROW, spawn_c)
    if not cands:
        return LOOKAHEAD_TOP_OUT_PENALTY
    xs = []
    for cand in cands:
        after, lines, eroded = simulate_lock(g, cand.dest)
        xs.append((evaluate_board(after, cand.dest, lines, eroded), after))
    xs.sort(key=lambda x: -x[0])
    if len(upcoming) == 1:
        return xs[0][0]
    return max(score + lookahead_reference(board, spawn_c, upcoming[1:]) for score, board in xs[:LOOKAHEAD_BEAM])


def test_lookahead_matches_the_reference():
    rnd = random.Random(3)
    for _ in range(40):
        g = random_grid(rnd, h=24, w=rnd.choice([10, 14]), kinds=(1, 1, 1, 2))
        for r in range(8):
            for c in range(g.w):
                g.set(r, c, 0)
        upcoming = [rnd.randrange(7) for _ in range(rnd.randrange(1, 4))]
        spawn_c = (g.w - 4) // 2
        assert lookahead(g, spawn_c, upcoming) == lookahead_reference(g, spawn_c, upcoming)


def test_planner_takes_the_line_it_is_handed():
    # A board one cell short of a full bottom row: dropping an I flat into the
    # gap must clear it, which the planner should prefer.
    g = Grid(24, WIDTH)
    for c in range(WIDTH):
        if c < 3 or c > 6:
            g.set(23, c, 1)
    ranked = plan_placements(g, 0, SPAWN_ROW, SPAWN_COL, SPAWN_COL, [], None)
    assert ranked and ranked[0].lines == 1
    assert ranked[0].dest == ((23, 3), (23, 4), (23, 5), (23, 6))


def test_no_placement_when_the_start_is_taken():
    g = Grid(24, WIDTH)
    for c in range(WIDTH):
        g.set(3, c, 1)
    assert plan_placements(g, 0, SPAWN_ROW, SPAWN_COL, SPAWN_COL, [], None) == []


def test_preview_is_capped_by_the_game_and_the_difficulty():
    # The fair-visibility rule: never past next_count, whatever the difficulty.
    assert revealed_pieces(42, [], "", 0, 0, 6) == []
    assert revealed_pieces(42, [], "", 0, 3, 6) == [3, 4, 1]
    assert revealed_pieces(42, [], "", 0, 6, 1) == [3]
    assert revealed_pieces(42, [], "", 5, 6, 6) == [6, 6, 0, 5, 4, 2]
    assert revealed_pieces(42, [0, 1, 4], "", 0, 2, 6) == [4, 1]
