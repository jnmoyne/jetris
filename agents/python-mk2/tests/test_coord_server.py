"""Two coordinating agents planning on one board, against a live JetStream
server."""

import asyncio
import time

from conftest import needs_server
from server_helpers import ServerEnv, publish_cell

from python_mk2.blackboard import PHASE_LOCKED
from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.coord import DepWait
from python_mk2.difficulty import difficulty_tuning
from python_mk2.path import Active
from python_mk2.rng import BAG_SINGLE, piece_at_bag
from python_mk2.wire import WireCell

pytestmark = needs_server


def seed_with_first_piece(pt):
    """A seed whose first piece is the given type."""
    seed = 1
    while piece_at_bag(seed, [], BAG_SINGLE, 0) != pt:
        seed += 1
    return seed


async def test_second_planner_plans_around_the_firsts_claim():
    """Two coordinating agents with one obvious spot (a four-wide gap in the
    bottom row and an I each): the first to plan claims the gap, the second
    plans on that claim and goes elsewhere; the first's lock ends what the
    second waited on."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        h = env.shared_test_peer(g, 1)
        await env.with_blackboard(g, h)
        seed = seed_with_first_piece(0)
        g.meta_seed = h.meta_seed = seed
        g.a.tn = h.a.tn = difficulty_tuning("hard")
        async with g.mu, h.mu:
            # The bottom row, full but for columns 5-8, on both boards and
            # the stream.
            wc = WireCell(o=True, t=1)
            for c in range(g.w):
                if 5 <= c <= 8:
                    continue
                seq = await publish_cell(g, (23, c), wc)
                g.fold_cell((23, c), wc, seq)
                h.fold_cell((23, c), wc, seq)
            for x in (g, h):
                _, placed, topped = await x.spawn()
                assert placed and not topped and x.piece.pt == 0, f"{x.a.name}: spawn"
            # Each sees the other's spawn as the board consumer would deliver it.
            for me, other in ((g, h), (h, g)):
                for c in other.piece.cells():
                    me.fold_cell(c, other.active_payload(other.piece), other.seqs[c])
            gp, hp = g.piece, h.piece

        first, pending, ok = await g.plan(gp, 0)
        assert ok and (first.orient, first.col, first.drop_row) == (0, 5, 22), "the first planner did not take the gap"
        assert pending is not None, "the first's claim was not posted"
        first = await g.confirm_plan(gp, first, pending)
        second, pending2, ok = await h.plan(hp, 0)
        assert ok, "the second planner found nothing"
        second = await h.confirm_plan(hp, second, pending2)
        gap = set(first.dest)
        assert not any(c in gap for c in second.dest), f"the second planner took the gap too: {second}"
        async with h.mu:
            assert 0 in h.claims and h.claims[0].col == 5 and h.st.claims_yielded == 0
            # Whatever the second chose, make it wait on the first, then let
            # the first lock: the lock's claim ends the wait.
            h.deps = [DepWait(seat=0, piece=0, rev=h.claims[0].rev, cells=list(first.dest))]
            assert h.deps_pending(), "no wait pending"
        async with g.mu:
            assert await g.lock_piece(Active(gp.pt, first.orient, first.drop_row, first.col)), \
                "the first's lock was dropped"
        deadline = time.monotonic() + 5.0
        while True:
            async with h.mu:
                waiting = h.deps_pending()
            if not waiting:
                break
            assert time.monotonic() < deadline, "the first's lock never ended the second's wait"
            await asyncio.sleep(0.05)
        async with h.mu:
            c = h.claims[0]
        assert c.phase == PHASE_LOCKED and c.cells[0] == (23, 5)
