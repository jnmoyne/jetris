"""The lobby watcher against a live JetStream server (JETRIS_NATS_URL;
skipped otherwise)."""

import asyncio
import time

import nats
import pytest
from conftest import NATS_URL, needs_server

from python_mk2.agent import Agent, ConnChoice, rand_id
from python_mk2.wire import dumps, now_rfc


async def wait_for(what, cond, timeout=45.0):
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
async def test_lobby_watch_survives_bucket_deletion():
    """Delete the lobby bucket underneath a running watch, as a shared
    server's purge does to a resident agent. The watcher must notice,
    recreate the bucket, drop the listing that vanished with the old one, and
    mirror new writes again, all through the KeyValue handle it already had.
    A private bucket name keeps the test off the server's real lobby."""
    nc = await nats.connect(NATS_URL)
    js = nc.jetstream()
    a = Agent(ConnChoice(server=NATS_URL), "t", "hard")
    a.name, a.nc, a.js = "me", nc, js
    a.bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    a.kv = await a.ensure_lobby_bucket()
    watch = asyncio.create_task(a.lobby_watch())

    async def put(gid):
        await a.kv.put("games." + gid, dumps({"id": gid, "created_at": now_rfc()}))

    async def bucket_exists():
        try:
            await js.key_value(a.bucket)
            return True
        except Exception:
            return False

    async def presence_there():
        try:
            await a.kv.get("players.me")
            return True
        except Exception:
            return False

    try:
        await put("g1")
        await wait_for("g1 to be mirrored", lambda: "g1" in a.listings)

        # Pull the bucket out from under the running watch.
        await js.delete_key_value(a.bucket)
        await wait_for("the bucket to be recreated", bucket_exists)
        await wait_for("the stale g1 listing to be dropped", lambda: "g1" not in a.listings)
        # Our presence entry is republished into the new bucket without
        # waiting for the next presence tick.
        await wait_for("presence to be republished", presence_there)
        # And the watch is live again, through the same handle.
        await put("g2")
        await wait_for("g2 to be mirrored", lambda: "g2" in a.listings)
        assert "g1" not in a.listings, "g1 came back: the mirror kept state from the deleted bucket"

        a.stop()
        await asyncio.wait_for(watch, timeout=5.0)
    finally:
        watch.cancel()
        try:
            await js.delete_key_value(a.bucket)
        except Exception:
            pass
        await nc.close()
