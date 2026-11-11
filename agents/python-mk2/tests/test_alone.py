"""The pause-when-alone rule and the last-board-standing verdict (guide §5),
on a Game with no server: golang-mk2's TestPauseWhileAlone and the verdict
half of TestJoinableInProgress."""

import asyncio

from helpers import make_game, seat

from python_mk2 import consts


async def _alone_game(pause):
    g = make_game(mode=consts.MODE_COOPERATIVE)
    g.pause_alone = pause
    await g._on_roster([seat("me", 0)])
    return g


async def test_pause_while_alone():
    g = await _alone_game(False)
    assert await g.pause_while_alone(), "without the rule, an agent alone waited"

    g = await _alone_game(True)
    await g._on_roster([seat("me", 0), seat("x", 1)])
    assert await g.pause_while_alone(), "with company, the agent waited"

    g = await _alone_game(True)
    waiting = asyncio.ensure_future(g.pause_while_alone())
    await asyncio.sleep(0.4)
    assert not waiting.done(), "an agent alone did not wait"
    g.on_roster([seat("me", 0), seat("x", 1)])  # as the lobby watch pushes it
    assert await asyncio.wait_for(waiting, 3.0), "company arriving did not resume play"

    g = await _alone_game(True)
    waiting = asyncio.ensure_future(g.pause_while_alone())
    await asyncio.sleep(0.05)
    g.mark_ended()
    assert not await asyncio.wait_for(waiting, 3.0), "the game ending did not release the paused agent as a stop"

    g = await _alone_game(True)
    waiting = asyncio.ensure_future(g.pause_while_alone())
    await asyncio.sleep(0.05)
    g.a.stop()
    assert not await asyncio.wait_for(waiting, 3.0), "the agent stopping did not release the paused agent"


async def test_last_board_standing_wins():
    # A roster down to us alone after rivals played is a win.
    g = make_game(mode=consts.MODE_COMPETITIVE)
    await g._on_roster([seat("me", 0), seat("x", 1)])
    assert not await g.win_check(), "won with a rival standing"
    await g._on_roster([seat("me", 0)])
    assert await g.win_check(), "the last board standing after the rival left did not win"

    alone = make_game(mode=consts.MODE_COMPETITIVE)
    await alone._on_roster([seat("me", 0)])
    assert not await alone.win_check(), "a game nobody else ever joined was won"
