"""Right of way over the wire, against a live JetStream server."""

import asyncio
import time

from conftest import needs_server
from server_helpers import ServerEnv, test_claim

from python_mk2.blackboard import PHASE_LOCKED
from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.difficulty import difficulty_tuning
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW, piece_cells
from python_mk2.planner import Placement

pytestmark = needs_server


async def test_a_wait_announced_hurries_the_teammate():
    """"You go, I wait", over the wire: a seat that announces it waits on a
    teammate's piece keeps its place in the right of way (on both agents'
    boards) and the teammate's think pause ends the moment the announcement
    reaches it; a pause begun while the announcement stands is a move step
    long. And the writes a seat makes without waiting for them reach the
    blackboard in the order it made them: the lock is the last word, not the
    wait announced before it."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        h = env.shared_test_peer(g, 1)
        await env.with_blackboard(g, h)
        g.a.tn = h.a.tn = difficulty_tuning("easy")  # a 1.5 s think pause

        # Seat 0 claims first, seat 1 after it.
        rev_g = await g.post_claim(test_claim(g, 3))
        mine = test_claim(h, 8)
        rev_h = await h.post_claim(mine)
        for x in (g, h):
            ok, _ = await x.await_rev(rev_h, 5.0)
            assert ok, f"{x.a.name} never saw revision {rev_h}"

        # Seat 0 starts thinking; seat 1 finds seat 0's piece in its way.
        async def think():
            start = time.monotonic()
            await g.pause(g.a.tn.piece_delay)
            return time.monotonic() - start

        thinking = asyncio.ensure_future(think())
        await asyncio.sleep(0.1)
        async with h.mu:
            h.piece = Active(2, 0, SPAWN_ROW, h.spawn_c)
            h.my_claim, h.my_rev, h.my_prio = mine, rev_h, rev_h
            h.announce_wait([0])
        took = await asyncio.wait_for(thinking, 5.0)
        assert took < 1.0, f"the think pause ran {took:.2f}s with a teammate waiting on the piece"
        async with g.mu:
            theirs, hurries = g.claims[1], g.st.hurries
        assert theirs.waits_on(0) and theirs.order() == rev_h and theirs.rev > rev_h
        assert theirs.order() > rev_g, "the waiting seat moved ahead of the claim made before it"
        assert hurries == 1
        # While the wait stands, the next pause is a move step.
        start = time.monotonic()
        await g.pause(g.a.tn.piece_delay)
        assert time.monotonic() - start < 1.0

        # The wait, then the lock, written without waiting: the lock is
        # what stays on the key.
        async with h.mu:
            h.announce_wait([0])
            locked = h.claim_for(h.piece, Placement(orient=0, col=8, dest=piece_cells(2, 0, 21, 8)), PHASE_LOCKED, 0)
            h.post_locked_async(locked)
            tail = h.post_tail
        await asyncio.wait_for(asyncio.wait([tail]), 5.0)
        deadline = time.monotonic() + 5.0
        while True:
            async with g.mu:
                c = g.claims.get(1)
            if c is not None and c.phase == PHASE_LOCKED:
                break
            assert time.monotonic() < deadline, f"the last word on seat 1's key is {c}, want the lock"
            await asyncio.sleep(0.02)
        await asyncio.sleep(0.2)  # nothing overtakes it afterwards
        async with g.mu:
            c = g.claims.get(1)
        assert c is not None and c.phase == PHASE_LOCKED, f"a wait announced before the lock landed after it: {c}"
