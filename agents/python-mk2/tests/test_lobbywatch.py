"""The lobby watcher against a live JetStream server (JETRIS_NATS_URL;
skipped otherwise)."""

import asyncio
import time

import nats
import pytest
from conftest import NATS_URL, needs_server

from python_mk2 import jsx
from python_mk2.agent import Agent, ConnChoice, rand_id
from python_mk2.wire import dumps, now_rfc


async def wait_for(what, cond, timeout=10.0):
    deadline = time.monotonic() + timeout
    while True:
        ok = cond()
        if asyncio.iscoroutine(ok):
            ok = await ok
        if ok:
            return
        if time.monotonic() > deadline:
            pytest.fail(f"timed out waiting for {what}")
        await asyncio.sleep(0.1)


@needs_server
async def test_lobby_watch_mirrors_the_bucket():
    """Listings written to the bucket reach the mirror, deleted ones leave
    it, and the watcher ends with the agent. A private bucket name keeps the
    test off the server's real lobby."""
    nc = await nats.connect(NATS_URL)
    js = nc.jetstream()
    a = Agent(ConnChoice(server=NATS_URL), "t", "hard")
    a.name, a.nc, a.js = "me", nc, js
    a.bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    a.kv = await a.ensure_lobby_bucket()
    watch = asyncio.create_task(a.lobby_watch(await jsx.KvWatch.start(a.kv, ">")))

    async def put(gid):
        await a.kv.put("games." + gid, dumps({"id": gid, "created_at": now_rfc()}))

    try:
        await put("g1")
        await wait_for("g1 to be mirrored", lambda: "g1" in a.listings)
        await put("g2")
        await wait_for("g2 to be mirrored", lambda: "g2" in a.listings)
        await a.kv.delete("games.g1")
        await wait_for("the deleted g1 listing to be dropped", lambda: "g1" not in a.listings)
        assert "g2" in a.listings

        a.stop()
        await asyncio.wait_for(watch, timeout=5.0)
    finally:
        watch.cancel()
        try:
            await js.delete_key_value(a.bucket)
        except Exception:
            pass
        await nc.close()
