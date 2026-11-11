"""Games against a live JetStream server (JETRIS_NATS_URL), for the
server-gated tests: a Game on its own private stream, with no consumers. A
test publishes its peers' cells itself and folds them as the board consumer
would (fold_peer_piece), which keeps it deterministic. Everything a ServerEnv
creates (streams, buckets, the connection) goes when it exits.
"""

import asyncio

import nats
from conftest import NATS_URL
from nats.js import api

from python_mk2 import aio, jsx
from python_mk2.agent import Agent, ConnChoice, rand_id
from python_mk2.blackboard import PHASE_PLANNED, Coordinator, board_name, claim_key
from python_mk2.consts import (MODE_COMPETITIVE, MODE_TEAMS, PUB_AUTO, PUB_SYNC, game_stream_name)
from python_mk2.game import Game
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_COL, SPAWN_ROW, WIDTH, piece_cells
from python_mk2.pipeline import resolve_publish
from python_mk2.planner import Placement
from python_mk2.rules import MIN_EXTRA_COLUMNS
from python_mk2.shared import shared_height, shared_width
from python_mk2.wire import WireCell, dumps, payload_bytes, player_summary


class ServerEnv:
    """One test's corner of the server: `async with ServerEnv() as env`."""

    async def __aenter__(self):
        self.nc = await nats.connect(NATS_URL)
        self.js = self.nc.jetstream()
        self.games, self.streams, self.buckets = [], [], []
        return self

    async def __aexit__(self, *exc):
        for g in self.games:
            g.mark_ended()
        tasks = [t for g in self.games for t in g.tasks]
        for g in self.games:
            await g.stop_consumers()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        for name in self.streams:
            try:
                await self.js.delete_stream(name)
            except Exception:
                pass
        for bucket in self.buckets:
            try:
                await self.js.delete_key_value(bucket)
            except Exception:
                pass
        await self.nc.close()

    def agent(self, name, pub=PUB_SYNC):
        a = Agent(ConnChoice(server=NATS_URL), "t", "hard", pub=pub)
        a.name = name
        a.nc, a.js = self.nc, self.js
        return a

    async def stream(self, atomic=True):
        """A private game stream with the game's own config; returns the
        game id."""
        gid = "test-" + rand_id(8)
        await self.js.add_stream(api.StreamConfig(
            name=game_stream_name(gid), subjects=[f"jetris.game.{gid}.>"], storage=api.StorageType.MEMORY,
            allow_atomic=atomic or None, allow_direct=True))
        self.streams.append(game_stream_name(gid))
        return gid

    async def shared_test_game(self, mode, seat, seats):
        """A Game on a shared board (the crew's, or team 0's) of `seats`
        seats at the default extra columns."""
        gid = await self.stream()
        g = Game(self.agent("me"), gid, seat)
        g.mode, g.player_count, g.seats_on_pf, g.extra = mode, seats, seats, MIN_EXTRA_COLUMNS
        if mode == MODE_TEAMS:
            g.player_count, g.team_count, g.team, g.team_slot = 2 * seats, 2, 0, seat
            g.team_scores, g.team_lines = [0, 0], [0, 0]
        g.w, g.h = shared_width(seats, MIN_EXTRA_COLUMNS), shared_height(seats, 0)
        g.spawn_c = g.spawn_column([])
        g.pub = resolve_publish(PUB_AUTO, True)
        self.games.append(g)
        return g

    def shared_test_peer(self, g0, seat):
        """A second Game on g0's board (another seat, its own Agent on the
        same connection) for tests of two agents on one stream and one
        blackboard."""
        g = Game(self.agent(f"peer{seat}"), g0.id, seat)
        g.mode, g.player_count, g.seats_on_pf, g.extra = g0.mode, g0.player_count, g0.seats_on_pf, g0.extra
        g.team, g.team_count = g0.team, g0.team_count
        if g.mode == MODE_TEAMS:
            g.team_slot = seat - g.team * g0.team_size()
            g.team_scores, g.team_lines = [0, 0], [0, 0]
        g.w, g.h = g0.w, g0.h
        g.spawn_c = g.spawn_column([])
        g.pub = PUB_SYNC
        self.games.append(g)
        return g

    async def pipeline_test_game(self, pub):
        """A competitive game with the agent's own stream config: atomic
        publish + direct get, the two features the pipeline rides on."""
        gid = await self.stream()
        g = Game(self.agent("me", pub=pub), gid, 0)
        g.mode, g.player_count, g.w, g.spawn_c = MODE_COMPETITIVE, 2, WIDTH, SPAWN_COL
        self.games.append(g)
        return g

    async def with_blackboard(self, *games):
        """Give the games a private blackboard bucket and their
        coordinators, and start their claim watches (they end with the
        env). Returns the bucket."""
        bucket = "JETRIS_BLACKBOARD_TEST_" + rand_id(6)
        self.buckets.append(bucket)
        for g in games:
            g.a.coordinate, g.a.bb_bucket = True, bucket
            g.a.set_blackboard(await g.a.ensure_blackboard())
            b = board_name(g.mode, g.team, g.individual, g.seats_on_pf)
            g.coord = Coordinator(board=b, key=claim_key(g.id, b, g.idx), watch=f"{g.id}.{b}.*")
            aio.spawn(g.tasks, g.claim_watch())
        return bucket

    async def leave_test_listing(self, g, invite_only):
        """Write the game's lobby listing into a private bucket: an
        in-progress game holding our seat and a crewmate's."""
        a = g.a
        a.bucket = "JETRIS_LOBBY_TEST_" + rand_id(6)
        self.buckets.append(a.bucket)
        a.kv = await a.ensure_lobby_bucket()
        listing = {
            "game_id": g.id, "mode": g.mode, "status": "in_progress", "player_count": 2, "invite_only": invite_only,
            "players": [player_summary("me", "me", seat=0, agent=True), player_summary("peer", "peer", seat=1)],
        }
        await a.kv.put("games." + g.id, dumps(listing))


async def publish_cell(g, at, wc):
    """Write one cell of the board straight to the stream (a peer's write,
    as far as the game under test is concerned) and return its sequence.
    Nothing is folded: the caller decides what the game sees."""
    seq = await jsx.js_publish(g.a.nc, g.cell_subject(at), payload_bytes(wc))
    g.note_stream_seq(seq)
    return seq


async def fold_peer_piece(g, pi, p):
    """Publish a peer's falling piece (seat pi) and fold every cell as the
    board consumer would deliver it; returns the cells' sequences. Caller
    holds g.mu."""
    seqs = {}
    wc = WireCell(t=p.pt, a=True, r=p.orient, ar=p.row, ac=p.col, pi=pi)
    for c in p.cells():
        seq = await publish_cell(g, c, wc)
        g.fold_cell(c, wc, seq)
        seqs[c] = seq
    return seqs


async def stream_cells(g):
    """The board as the stream has it, for assertions: (r, c) → (seq,
    WireCell)."""
    return await g.fetch_board()


async def assert_converged(g):
    """The pipeline's invariant after a settle: the stream's committed truth
    and the local replica agree. Every active cell on the stream belongs to
    the adopted piece (no strays), the settled cells match, and every local
    sequence is the cell's actual last stream sequence."""
    snap = await g.fetch_board()
    own = set(g.piece.cells()) if g.piece is not None else set()
    for at, (seq, wc) in snap.items():
        if wc.a:
            assert at in own, f"stray active cell {at} on the stream (piece {g.piece})"
        elif wc.o:
            assert g.locked.get(at) == wc, f"locked cell {at}: local {g.locked.get(at)}, stream {wc}"
        assert g.seqs.get(at, 0) == seq, f"cell {at}: local seq {g.seqs.get(at, 0)}, stream seq {seq}"
    for at in g.locked:
        assert at in snap and snap[at][1].o, f"local locked cell {at} missing from the stream"


def test_claim(g, col):
    p = Active(2, 0, SPAWN_ROW, g.spawn_c)
    return g.claim_for(p, Placement(orient=0, col=col, drop_row=21, dest=piece_cells(2, 0, 21, col)),
                       PHASE_PLANNED, 0.1)


test_claim.__test__ = False  # a helper, not a test
