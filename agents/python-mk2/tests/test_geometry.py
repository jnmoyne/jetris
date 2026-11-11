from helpers import make_game

from python_mk2.rules import extra_rows
from python_mk2.shared import STANDARD_HEIGHT, shared_height


async def test_shared_height():
    # The shared board's height rule: the standard 24 rows (4 of headroom
    # over 20 visible) for one seat and the game's extra_rows more for every
    # seat after it, clamped; a Game that never read a meta answers the
    # standard board.
    for seats, extra, want in [(1, 0, 24), (1, 10, 24), (2, 0, 24), (2, 5, 29), (3, 5, 34), (4, 4, 36),
                               (2, 10, 34), (2, -1, 24), (2, 99, 34)]:
        assert shared_height(seats, extra) == want, (seats, extra)
    for v, want in {0: 0, -1: 0, 5: 5, 10: 10, 11: 10}.items():
        assert extra_rows(v) == want, v
    g = make_game()
    assert g.height() == STANDARD_HEIGHT == 24
    g.h = 34
    assert g.height() == 34
