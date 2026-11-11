"""The JetStream plumbing against a live server (JETRIS_NATS_URL)."""

import asyncio
import secrets

import nats
import pytest
from nats.js import api

from python_mk2 import jsx

from conftest import NATS_URL, needs_server

pytestmark = needs_server


@pytest.fixture
async def nc():
    conn = await nats.connect(NATS_URL)
    yield conn
    await conn.close()


async def game_stream(js):
    name = "JETRIS_GAME_test-" + secrets.token_hex(4)
    await js.add_stream(api.StreamConfig(
        name=name, subjects=[f"jetris.game.{name[12:]}.>"], storage=api.StorageType.MEMORY,
        allow_atomic=True, allow_direct=True))
    return name, f"jetris.game.{name[12:]}"


async def test_cas_request_and_conflict(nc):
    js = nc.jetstream()
    stream, base = await game_stream(js)
    try:
        subj = base + ".playfield.cell.1.1"
        seq, conflict = await jsx.cas_request(nc, subj, b"{}", {jsx.H_EXPECT_LAST: "0"})
        assert seq == 1 and not conflict
        seq2, conflict = await jsx.cas_request(nc, subj, b"{}", {jsx.H_EXPECT_LAST: "0"})
        assert conflict and seq2 == 0
        seq3, conflict = await jsx.cas_request(nc, subj, b'{"o":true}', {jsx.H_EXPECT_LAST: "1"})
        assert seq3 == 2 and not conflict
        raw = await jsx.get_last_msg(js, stream, subj)
        assert raw.seq == 2 and raw.data == b'{"o":true}'
        with pytest.raises(jsx.NotFoundError):
            await jsx.get_last_msg(js, stream, base + ".playfield.cell.9.9")
    finally:
        await js.delete_stream(stream)


async def test_atomic_batch_is_all_or_nothing(nc):
    js = nc.jetstream()
    stream, base = await game_stream(js)
    try:
        cells = [f"{base}.playfield.cell.0.{c}" for c in range(4)]

        async def batch(expect):
            bid = secrets.token_hex(11)
            for i, subj in enumerate(cells):
                h = {jsx.H_BATCH_ID: bid, jsx.H_BATCH_SEQ: str(i + 1), jsx.H_EXPECT_LAST: str(expect[i])}
                if i == len(cells) - 1:
                    h[jsx.H_BATCH_COMMIT] = "1"
                if i in (0, len(cells) - 1):
                    seq, conflict = await jsx.cas_request(nc, subj, b'{"a":true}', h)
                    if conflict:
                        return 0, True
                else:
                    await nc.publish(subj, b'{"a":true}', headers=h)
            return seq, False

        seq, conflict = await batch([0, 0, 0, 0])
        assert not conflict and seq == 4  # four consecutive sequences ending at the commit's
        # One stale expectation rejects the whole batch: nothing is stored.
        seq, conflict = await batch([1, 2, 3, 99])
        assert conflict
        info = await js.stream_info(stream)
        assert info.state.last_seq == 4
        seq, conflict = await batch([1, 2, 3, 4])
        assert not conflict and seq == 8
    finally:
        await js.delete_stream(stream)


async def test_multi_get_reads_a_600_cell_board(nc):
    js = nc.jetstream()
    stream, base = await game_stream(js)
    try:
        subjects = [f"{base}.playfield.cell.{r}.{c}" for r in range(30) for c in range(20)]
        for i, subj in enumerate(subjects):
            if i % 3 == 0:
                await nc.publish(subj, b'{"o":true,"t":2}')
        await nc.flush()
        await asyncio.sleep(0.1)
        info = await js.stream_info(stream)
        got = []
        for start in range(0, len(subjects), 512):
            got += await jsx.get_last_msgs_for(nc, js, stream, subjects[start:start + 512],
                                               up_to_seq=info.state.last_seq)
        assert len(got) == 200
        assert {m.subject for m in got} == {s for i, s in enumerate(subjects) if i % 3 == 0}
        assert all(m.seq > 0 and m.data == b'{"o":true,"t":2}' for m in got)
        # Nothing written under the subjects asked for: an empty answer, not an error.
        assert await jsx.get_last_msgs_for(nc, js, stream, [f"{base}.playfield.cell.99.{c}" for c in range(5)]) == []
    finally:
        await js.delete_stream(stream)


async def test_kv_ttl_put_expires_and_the_watch_sees_it(nc):
    js = nc.jetstream()
    bucket = "JETRIS_BLACKBOARD_TEST_" + secrets.token_hex(4)
    kv = await jsx.ensure_kv(js, bucket, api.StorageType.MEMORY, 30.0)
    try:
        # Idempotent: a second ensure binds to the same bucket.
        await jsx.ensure_kv(js, bucket, api.StorageType.MEMORY, 30.0)
        w = await jsx.KvWatch.start(js, kv, bucket, "g1.crew.*")
        seen = []

        async def follow():
            async for e in w.updates():
                seen.append(e)

        task = asyncio.ensure_future(follow())
        r1 = await jsx.kv_put_ttl(nc, bucket, "g1.crew.0", b'{"v":1}', 1)
        r2 = await jsx.kv_put_ttl(nc, bucket, "g1.crew.1", b'{"v":1}', 30)
        await jsx.kv_put_ttl(nc, bucket, "g2.crew.0", b'{"v":1}', 30)  # another board's key: not ours to see
        assert r2 == r1 + 1
        for _ in range(60):
            if any(e is not None and e[0] == "g1.crew.0" and e[3] != jsx.KV_PUT for e in seen):
                break
            await asyncio.sleep(0.1)
        await w.stop()
        await task
        assert seen[0] is None  # the initial values (none) are in
        puts = [e for e in seen if e is not None and e[3] == jsx.KV_PUT]
        assert [(e[0], e[2]) for e in puts] == [("g1.crew.0", r1), ("g1.crew.1", r2)]
        gone = [e for e in seen if e is not None and e[3] != jsx.KV_PUT]
        assert gone and gone[0][0] == "g1.crew.0" and gone[0][2] > r2
    finally:
        await js.delete_key_value(bucket)


async def test_watch_ends_when_its_bucket_is_deleted(nc, monkeypatch):
    monkeypatch.setattr(jsx.KvWatch, "PROBE_EVERY", 0.2)
    js = nc.jetstream()
    bucket = "JETRIS_BLACKBOARD_TEST_" + secrets.token_hex(4)
    kv = await jsx.ensure_kv(js, bucket, api.StorageType.MEMORY, 30.0)
    w = await jsx.KvWatch.start(js, kv, bucket, ">")

    async def drain():
        return [e async for e in w.updates()]

    task = asyncio.ensure_future(drain())
    await asyncio.sleep(0.3)
    await js.delete_key_value(bucket)
    assert await asyncio.wait_for(task, 5.0) == [None]
