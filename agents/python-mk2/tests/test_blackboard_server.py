"""Two agents on one stream and one blackboard, against a live JetStream
server."""

import asyncio
import time

from conftest import needs_server
from server_helpers import ServerEnv, test_claim

from python_mk2 import jsx
from python_mk2.consts import MODE_COOPERATIVE

pytestmark = needs_server


async def test_claims_are_seen_in_revision_order():
    """Two agents' claims reach both in the bucket's revision order, each
    agent's own echo included; a claim's TTL takes it off both."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        h = env.shared_test_peer(g, 1)
        bucket = await env.with_blackboard(g, h)

        rev_a = await g.post_claim(test_claim(g, 3))
        rev_b = await h.post_claim(test_claim(h, 8))
        assert rev_b > rev_a, "revisions not ordered"
        for x in (g, h):
            ok, _ = await x.await_rev(rev_b, 5.0)
            assert ok, f"{x.a.name} never saw revision {rev_b} (mark {x.bb_rev})"
            async with x.mu:
                a, b = x.claims[0], x.claims[1]
            assert (a.rev, b.rev, a.col, b.col, a.agent, b.agent) == (rev_a, rev_b, 3, 8, "me", "peer1")
            assert a.seat == 0 and b.seat == 1 and a.eta_ms == 100 and a.phase == "planned"
        # A claim with a short TTL vanishes from both within a few seconds.
        await jsx.kv_put_ttl(env.nc, bucket, h.coord.key, test_claim(h, 9).to_bytes(), 1)
        deadline = time.monotonic() + 10.0
        while True:
            async with g.mu:
                still_g = 1 in g.claims
            async with h.mu:
                still_h = 1 in h.claims
            if not still_g and not still_h:
                break
            assert time.monotonic() < deadline, "the expired claim never left the board"
            await asyncio.sleep(0.2)
        # The other seat's claim (TTL not yet run out) is still there.
        async with g.mu:
            assert 0 in g.claims
