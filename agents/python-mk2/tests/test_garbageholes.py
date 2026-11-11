from helpers import make_game

from python_mk2.consts import MODE_COMPETITIVE
from python_mk2.engine import Grid
from python_mk2.wire import WireCell, garbage_payload


async def test_completed_rows_garbage_holes():
    # The garbage-row rule on both boards the agent keeps (the planner's
    # grid and the live settled map): a solid garbage row never completes, a
    # garbage row whose holes the stack filled clears like any other, and a
    # row a foreign falling piece crosses never does.
    gr = Grid(4, 5)
    for c in range(5):
        gr.set(3, c, 2)  # solid garbage: permanent
        gr.set(2, c, 2)  # garbage whose hole (col 1) the stack filled
        gr.set(1, c, 1)  # plain full row
        gr.set(0, c, 1)  # full but crossed by a foreign falling piece
    gr.set(2, 1, 1)
    gr.set(0, 4, 3)
    assert gr.completed_rows() == [1, 2]

    g = make_game(w=5, mode=MODE_COMPETITIVE)
    h = g.height()
    gp = garbage_payload(1)
    for c in range(5):
        g.locked[(h - 1, c)] = gp
        g.locked[(h - 2, c)] = gp
        g.locked[(h - 3, c)] = gp
    g.locked[(h - 2, 3)] = WireCell(o=True, t=0)  # hole filled by the stack
    del g.locked[(h - 3, 0)]                      # hole still open
    assert g.completed_rows() == [h - 2]


async def test_garbage_hole_columns():
    # The raise's hole draw honours the game's count, stays in range, and
    # never empties a whole row.
    g = make_game(w=10, holes=3)
    for i in range(100):
        holes = g.garbage_hole_columns()
        assert len(holes) == 3, i
        assert all(0 <= c < g.w for c in holes), i
    g.holes = 0
    assert len(g.garbage_hole_columns()) == 0
    narrow = make_game(w=3, holes=4)
    assert len(narrow.garbage_hole_columns()) == 2  # a row keeps garbage


async def test_random_holes_draw_per_row():
    # A random-holes game draws each garbage row's columns independently; by
    # default the rows of one raise share one draw.
    def raise_rows(g, n):
        rows, holes = [], None
        for _ in range(n):
            if holes is None or g.random_holes:
                holes = g.garbage_hole_columns()
            rows.append(sorted(holes))
        return rows

    shared = make_game(w=10, holes=1)
    for _ in range(30):
        rows = raise_rows(shared, 3)
        assert rows[0] == rows[1] == rows[2], "a clean raise should share one draw"
    rnd = make_game(w=10, holes=1, random_holes=True)
    differed = False
    for _ in range(40):
        rows = raise_rows(rnd, 2)
        if rows[0] != rows[1]:
            differed = True
            break
    assert differed, "random raise: 40 two-row raises never drew different columns"


async def test_attack_rows():
    # One row per line by default; the Modern table (0/1/2/4) in a
    # guideline_garbage game.
    plain, table = make_game(), make_game(guideline=True)
    want = {0: 0, 1: 0, 2: 1, 3: 2, 4: 4}
    for lines in range(5):
        assert plain.attack_rows(lines) == lines
        assert table.attack_rows(lines) == want[lines]
