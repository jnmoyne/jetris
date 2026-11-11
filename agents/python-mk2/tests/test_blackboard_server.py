"""Two agents on one stream and one blackboard, against a live JetStream
server."""

import asyncio
import time

import pytest
from conftest import needs_server
from server_helpers import ServerEnv, test_claim

from python_mk2 import jsx
from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.coord import REPLAN_BB_RESET
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW

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


@pytest.mark.parametrize("probe", [0.2, None], ids=["fast-probe", "default-probe"])
async def test_blackboard_watch_survives_bucket_deletion(probe, monkeypatch):
    """The bucket deleted underneath the watch (a shared server's purge):
    the watch comes back on a recreated bucket, the claims and the revision
    mark start over, the piece in play is re-planned, and new claims are
    seen again."""
    if probe is not None:
        monkeypatch.setattr(jsx.KvWatch, "PROBE_EVERY", probe)
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        h = env.shared_test_peer(g, 1)
        bucket = await env.with_blackboard(g, h)
        rev = await h.post_claim(test_claim(h, 8))
        ok, _ = await g.await_rev(rev, 5.0)
        assert ok, "the first claim never arrived"
        async with g.mu:
            g.piece = Active(2, 0, SPAWN_ROW, g.spawn_c)
        await env.js.delete_key_value(bucket)
        deadline = time.monotonic() + 60.0
        while True:
            async with g.mu:
                epoch, replan, n = g.bb_epoch, g.replan, len(g.claims)
            if epoch == 1:
                assert replan == REPLAN_BB_RESET and n == 0
                break
            assert time.monotonic() < deadline, "the watch never came back"
            await asyncio.sleep(0.25)
        # The bucket is back, and a claim posted on it reaches the watch again.
        attempt = 0
        while True:
            try:
                rev2 = await h.post_claim(test_claim(h, 5))
                break
            except Exception as e:
                attempt += 1
                assert attempt <= 20, f"posting after the recovery: {e}"
                await asyncio.sleep(0.25)
        ok, _ = await g.await_rev(rev2, 10.0)
        assert ok, f"the claim after the recovery never arrived (mark {g.bb_rev}, want {rev2})"
        async with g.mu:
            c = g.claims[1]
        assert c.col == 5 and c.rev == rev2
