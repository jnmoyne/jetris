from helpers import make_game, seat

from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.pieces import SPAWN_COL


async def test_spawn_column_follows_seats_present():
    # The spawn point follows the seats present: ranked in slot order, one
    # extra-columns step apart, the group centred on the board; alone in the
    # middle, a full house at slot x extra + SPAWN_COL. Refreshed by every
    # roster push.
    # Seat 2 of a three-seat crew on an 18-wide board (extra 4).
    g = make_game(mode=MODE_COOPERATIVE, idx=2, seats_on_pf=3, extra=4)
    for what, roster, want in [
        ("alone", [seat("me", 2)], 7),
        ("two of three, the higher seat", [seat("x", 0), seat("me", 2)], 9),
        ("a full house", [seat("x", 0), seat("y", 1), seat("me", 2)], 11),
        ("a roster that lacks us", [seat("x", 0)], 9),
        ("no roster", [], 11),
    ]:
        await g._on_roster(roster)
        assert g.spawn_c == want, what
    assert g.spawn_column([]) == 11  # no roster at all: the section's own

    # Slot 2 of team B, three a side: only the team's own slots count.
    tm = make_game(mode=MODE_TEAMS, idx=5, team=1, team_slot=2, seats_on_pf=3, extra=4)
    await tm._on_roster([seat("me", 5, team=1, team_slot=2), seat("a0", 0, team=0)])
    assert tm.spawn_c == 7  # alone on the team board: the middle
    await tm._on_roster([seat("me", 5, team=1, team_slot=2), seat("b0", 3, team=1, team_slot=0),
                         seat("a0", 0, team=0)])
    assert tm.spawn_c == 9  # two of the team's three

    # A competitive board is private and standard.
    c = make_game(mode=MODE_COMPETITIVE, idx=1)
    await c._on_roster([seat("me", 1)])
    assert c.spawn_c == SPAWN_COL


async def test_on_roster_is_folded_from_the_lobby_watch():
    # on_roster is what the lobby watch calls: the fold runs as a task that
    # waits its turn on mu.
    import asyncio

    g = make_game(mode=MODE_COOPERATIVE, idx=2, seats_on_pf=3, extra=4)
    g.on_roster([seat("x", 0), seat("me", 2)])
    for _ in range(5):
        await asyncio.sleep(0)
    assert g.spawn_c == 9 and g.had_rivals
