"""The lobby side of a game's lifecycle against a live JetStream server
(JETRIS_NATS_URL; skipped otherwise): hosting, joining, readying up, the
meta's transitions and walking away, on a private lobby bucket."""

import asyncio
from datetime import datetime, timedelta, timezone

import nats
import pytest
from conftest import NATS_URL, needs_server

from python_mk2 import jsx
from python_mk2.agent import Agent, ConnChoice, Hosting, rand_id
from python_mk2.consts import MODE_COOPERATIVE, MODE_TEAMS, game_stream_name
from python_mk2.wire import dumps, now_rfc, o_bool, o_int, o_str, players_of, to_obj


async def make_agent(bucket, stem="t"):
    a = Agent(ConnChoice(server=NATS_URL), stem, "hard")
    a.nc = await nats.connect(NATS_URL)
    a.js = a.nc.jetstream()
    a.bucket = bucket
    a.kv = await a.ensure_lobby_bucket()
    return a


async def listing_of(a, gid):
    return to_obj((await a.kv.get("games." + gid)).value)


@needs_server
async def test_host_join_ready_and_leave():
    bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    host = await make_agent(bucket, "host")
    peer = await make_agent(bucket, "peer")
    gid = "t-" + rand_id(8)
    try:
        got = await host.create_game(Hosting(game_name=gid, mode=MODE_COOPERATIVE, players=2, max_agents=2,
                                             line_goal=20, seed=42, seed_set=True, extra_cols=4))
        assert got == gid
        meta, seq = await host.fetch_meta(gid)
        assert seq == 1 and meta["seed"] == 42 and meta["status"] == "created" and meta["line_goal"] == 20
        assert meta["extra_columns"] == 4 and meta["player_count"] == 2 and meta["creator_id"] == host.name
        info = await host.stream(gid)
        assert info.config.allow_atomic and info.config.allow_direct
        g = await listing_of(host, gid)
        assert g["max_agents"] == 2 and g["players"] == [] and not o_bool(g, "invite_only")

        # The name is the claim: a second game of the same name is refused.
        with pytest.raises(RuntimeError, match="already exists"):
            await peer.create_game(Hosting(game_name=gid, mode=MODE_COOPERATIVE, players=2))

        # Lowest free seats, in order; joining twice is the same seat.
        assert await host.join_game(gid, False) == 0
        assert await peer.join_game(gid, False) == 1
        assert await peer.join_game(gid, False) == 1
        g = await listing_of(host, gid)
        assert [p["player_id"] for p in players_of(g)] == [host.name, peer.name]
        assert all(p["agent"] for p in players_of(g))
        roster = await jsx.get_last_msg(host.js, game_stream_name(gid), f"jetris.game.{gid}.roster.{peer.name}")
        assert to_obj(roster.data)["seat"] == 1
        presence = to_obj((await peer.kv.get("players." + peer.name)).value)
        assert presence["game_id"] == gid and presence["status"] == 1 and presence["agent"] is True

        # An open game starts on the first ready player: that toggle is
        # elected to run the countdown, the next one is not.
        assert await host.toggle_ready(gid) is True
        assert o_str(await listing_of(host, gid), "status") == "starting"
        assert await peer.toggle_ready(gid) is False

        # The peer walks away before the start: its seat and roster entry go.
        await peer.unjoin_game(gid)
        g = await listing_of(host, gid)
        assert [p["player_id"] for p in players_of(g)] == [host.name]
        with pytest.raises(jsx.NotFoundError):
            await jsx.get_last_msg(host.js, game_stream_name(gid), f"jetris.game.{gid}.roster.{peer.name}")

        # The meta's transitions: forward only, never out of a finished game.
        assert await host.transition_meta(gid, "in_progress")
        meta, _ = await host.fetch_meta(gid)
        assert meta["status"] == "in_progress" and "started_at" in meta and meta["seed"] == 42
        await host.set_listing_status(gid, "in_progress")
        assert o_str(await listing_of(host, gid), "status") == "in_progress"
        assert await host.transition_meta(gid, "finished")
        assert not await host.transition_meta(gid, "in_progress")
        assert await host.transition_meta(gid, "archived")
        # A game that is over takes nobody, whatever its listing still says.
        assert await peer.join_game(gid, False) == -1
    finally:
        for a in (host, peer):
            try:
                await a.js.delete_stream(game_stream_name(gid))
            except Exception:
                pass
        try:
            await host.js.delete_key_value(bucket)
        except Exception:
            pass
        await host.nc.close()
        await peer.nc.close()


@needs_server
async def test_teams_join_and_agent_policy():
    bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    agents = [await make_agent(bucket, f"a{i}") for i in range(3)]
    gid = "t-" + rand_id(8)
    try:
        await agents[0].create_game(Hosting(game_name=gid, mode=MODE_TEAMS, players=2, teams=2, max_agents=1))
        g = await listing_of(agents[0], gid)
        assert o_int(g, "player_count") == 4 and o_int(g, "team_size") == 2 and g["team_names"] == ["Green", "Blue"]
        # One agent seat per team: the first two agents take a team each, the
        # third finds every team's agent seat taken.
        seats = [await a.join_game(gid, False) for a in agents]
        assert seats == [0, 2, -1]
        ps = players_of(await listing_of(agents[0], gid))
        assert [(p["team"], p["team_slot"]) for p in ps] == [(0, 0), (1, 0)]
        # An invitation is the permission.
        agents[2].invite_team = 1
        assert await agents[2].join_game(gid, True) == 3
    finally:
        try:
            await agents[0].js.delete_stream(game_stream_name(gid))
            await agents[0].js.delete_key_value(bucket)
        except Exception:
            pass
        for a in agents:
            await a.nc.close()


@needs_server
async def test_run_selects_and_stops():
    """The whole lobby loop against the server's real resources, without a
    game: connect, presence, the lobby mirror, then a clean stop."""
    a = Agent(ConnChoice(server=NATS_URL), "t", "hard")
    a.bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    a.bb_bucket = "JETRIS_BLACKBOARD_TEST_" + rand_id(6)
    task = asyncio.create_task(a.run())
    try:
        for _ in range(100):
            if a.kv is not None and a.nc is not None and a.nc.is_connected:
                try:
                    await a.kv.get("players." + a.name)
                    break
                except Exception:
                    pass
            await asyncio.sleep(0.1)
        else:
            pytest.fail("presence never appeared")
        assert a.coordinate and a.blackboard() is not None
        nc = await nats.connect(NATS_URL)
        js = nc.jetstream()
        a.stop()
        await asyncio.wait_for(task, timeout=10.0)
        kv = await js.key_value(a.bucket)
        with pytest.raises(Exception):
            await kv.get("players." + a.name)  # the clean exit deleted it
        await js.delete_key_value(a.bucket)
        await js.delete_key_value(a.bb_bucket)
        await nc.close()
    finally:
        task.cancel()


@needs_server
async def test_invitations():
    """An invitation to a game the lobby lists is taken (and its key deleted
    on the join); one to a game that is gone is dropped; a declined one is
    rewritten for the inviter to see."""
    bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
    a = await make_agent(bucket, "inv")
    gid = "t-" + rand_id(8)
    watch = asyncio.create_task(a.lobby_watch())

    async def key_gone(key):
        try:
            await a.kv.get(key)
            return False
        except Exception:
            return True

    try:
        host = await make_agent(bucket, "host")
        await host.create_game(Hosting(game_name=gid, mode=MODE_TEAMS, players=1, teams=2, max_agents=1))
        g = await listing_of(host, gid)
        g["invite_only"] = True
        await host.kv.put("games." + gid, dumps(g))

        inv = {"game_id": gid, "invitee_id": a.name, "from_id": "x", "from_name": "x", "mode": MODE_TEAMS,
               "team": 1, "created_at": now_rfc()}
        await a.kv.put(f"invites.{a.name}.{gid}", dumps(inv))
        await a.kv.put(f"invites.{a.name}.gone", dumps({**inv, "game_id": "gone", "created_at": "2020-01-01T00:00:00Z"}))
        # Fresh, older than ours (so it is looked at first), its game gone.
        earlier = (datetime.now(timezone.utc) - timedelta(seconds=30)).isoformat().replace("+00:00", "Z")
        await a.kv.put(f"invites.{a.name}.ghost", dumps({**inv, "game_id": "ghost", "created_at": earlier}))

        for _ in range(100):  # the mirror has them all, and the game
            if len(a.invites) == 3 and gid in a.listings:
                break
            await asyncio.sleep(0.05)
        assert [g for g, _ in a.fresh_invites()] == ["ghost", gid], "the stale invitation is not fresh"
        got = await asyncio.wait_for(a.select_game(), timeout=10.0)
        assert got == (gid, True) and a.invite_team == 1
        assert await key_gone(f"invites.{a.name}.ghost"), "an invitation to a game that is gone was kept"
        # Uninvited, an invite game takes nobody but its creator; invited,
        # the team the invitation names.
        stranger = await make_agent(bucket, "stranger")
        assert await stranger.join_game(gid, False) == -1
        await stranger.nc.close()
        assert await a.join_game(gid, True) == 1
        assert await key_gone(f"invites.{a.name}.{gid}"), "the accepted invitation's key was kept"
        ps = players_of(await listing_of(a, gid))
        mine = [p for p in ps if p["player_id"] == a.name][0]
        assert (mine["team"], mine["team_slot"], mine["seat"]) == (1, 0, 1)

        # Declining rewrites the key.
        await a.kv.put(f"invites.{a.name}.other", dumps({**inv, "game_id": "other"}))
        for _ in range(50):
            if "other" in a.invites:
                break
            await asyncio.sleep(0.1)
        await a.decline_invite("other")
        assert to_obj((await a.kv.get(f"invites.{a.name}.other")).value)["declined"] is True
        await host.nc.close()
    finally:
        a.stop()
        await asyncio.gather(watch, return_exceptions=True)
        try:
            await a.js.delete_stream(game_stream_name(gid))
            await a.js.delete_key_value(bucket)
        except Exception:
            pass
        await a.nc.close()
