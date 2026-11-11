"""Leaving a running game cleanly, against a live JetStream server."""

import pytest
from conftest import needs_server
from server_helpers import ServerEnv, stream_cells

from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.wire import players_of, to_obj

pytestmark = needs_server


async def listing_players(g):
    entry = await g.a.kv.get("games." + g.id)
    return players_of(to_obj(entry.value))


@pytest.mark.parametrize("invite_only", [False, True], ids=["open-frees-seat", "invite-keeps-seat"])
async def test_leave_running(invite_only):
    """Stopped mid-game in an OPEN game, the agent vacates its own piece
    before freeing its seat: the board keeps no cell of ours, the listing no
    seat. An invite game's seat is kept for the player to rejoin: the piece
    is vacated, the seat stays."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        await env.leave_test_listing(g, invite_only)
        async with g.mu:
            _, placed, topped = await g.spawn()
            assert placed and not topped
            assert g.piece is not None
        g.a.stop()
        await g.leave_running()
        snap = await stream_cells(g)
        assert snap, "the spawn's cells are on the stream, vacated"
        for at, (_, wc) in snap.items():
            assert not (wc.a and wc.pi == 0), f"a cell of ours left on the board at {at}"
        assert g.piece is None, "the piece is still ours locally"
        players = await listing_players(g)
        if invite_only:
            assert len(players) == 2, "an invite game keeps both seats"
        else:
            assert [p["player_id"] for p in players] == ["peer"], "the listing keeps the peer alone"
