"""The replay copy against a live JetStream server (JETRIS_NATS_URL; skipped
otherwise). It writes to the server's real JETRIS_ARCHIVE and JETRIS_REPLAY
streams, so point it at a private server."""

import asyncio

import nats
from conftest import NATS_URL, needs_server
from nats.js import api

from python_mk2 import jsx, replay
from python_mk2.agent import Agent, ConnChoice, rand_id
from python_mk2.consts import ARCHIVE_SUBJECT, MODE_COOPERATIVE, game_stream_name
from python_mk2.wire import dumps, now_rfc, to_obj


@needs_server
async def test_copy_marker_pins_and_purge():
    a = Agent(ConnChoice(server=NATS_URL), "t", "hard")
    a.nc = await nats.connect(NATS_URL)
    a.js = a.nc.jetstream()
    a.bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    a.kv = await a.ensure_lobby_bucket()
    gid = "t-" + rand_id(8)
    stream = game_stream_name(gid)
    try:
        try:
            await a.js.stream_info("JETRIS_ARCHIVE")
        except jsx.NotFoundError:
            await a.js.add_stream(api.StreamConfig(name="JETRIS_ARCHIVE", subjects=[ARCHIVE_SUBJECT],
                                                   storage=api.StorageType.FILE))
        await a.js.add_stream(api.StreamConfig(name=stream, subjects=[f"jetris.game.{gid}.>"], allow_atomic=True,
                                               allow_direct=True, storage=api.StorageType.MEMORY))
        n = 700  # more than one copy chunk
        await jsx.js_publish(a.nc, f"jetris.game.{gid}.meta", dumps({"game_id": gid}), {jsx.H_EXPECT_LAST: "0"})
        for i in range(n - 1):
            await a.nc.publish(f"jetris.game.{gid}.playfield.cell.{i % 24}.{i % 10}", dumps({"o": True, "t": i % 7}))
        await a.nc.flush()
        for _ in range(100):  # the stream stores what the server has received a moment later
            if (await a.js.stream_info(stream)).state.messages == n:
                break
            await asyncio.sleep(0.02)
        assert (await a.js.stream_info(stream)).state.messages == n

        rec = dumps({"version": 4, "game_id": gid, "mode": MODE_COOPERATIVE, "total_score": 1234,
                     "started_at": now_rfc(), "finished_at": now_rfc(), "players": [{"score": 1234, "agent": True}]})
        await jsx.js_publish(a.nc, ARCHIVE_SUBJECT, rec)
        assert gid in {r.game_id for r in await replay.fetch_archive_records(a)}

        await a.maybe_archive_replay(rec)
        assert gid in await replay.list_replay_game_ids(a)
        info = await a.js.stream_info("JETRIS_REPLAY", subjects_filter=replay.replay_filter(gid))
        assert sum(info.state.subjects.values()) == n + 1  # every message, and the marker
        marker = await jsx.get_last_msg(a.js, "JETRIS_REPLAY", replay.replay_marker_subject(gid))
        assert to_obj(marker.data) == {"game_id": gid, "msgs": n}
        meta = await jsx.get_last_msg(a.js, "JETRIS_REPLAY", f"jetris.replay.{gid}.meta")
        assert to_obj(meta.data) == {"game_id": gid}
        ts = int(meta.headers["Jetris-Ts"])
        assert ts > 1_700_000_000 * 10**9 and jsx.H_EXPECT_LAST not in meta.headers

        # Pins are read off the lobby bucket.
        assert await replay.list_pinned_replays(a) == set()
        await a.kv.put("pins." + gid, b"{}")
        await a.kv.put("games.other", b"{}")
        assert await replay.list_pinned_replays(a) == {gid}

        await replay.purge_replay(a, gid)
        assert gid not in await replay.list_replay_game_ids(a)
    finally:
        for name in (stream,):
            try:
                await a.js.delete_stream(name)
            except Exception:
                pass
        try:
            await replay.purge_replay(a, gid)
            await a.js.delete_key_value(a.bucket)
        except Exception:
            pass
        await a.nc.close()
