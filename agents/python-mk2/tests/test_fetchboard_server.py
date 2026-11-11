"""The board's multi-subject fetch against a live JetStream server."""

from conftest import needs_server
from server_helpers import ServerEnv

from python_mk2 import jsx
from python_mk2.consts import MODE_TEAMS
from python_mk2.game import FETCH_CHUNK, Game
from python_mk2.path import Active
from python_mk2.shared import shared_height
from python_mk2.wire import WireCell, payload_bytes

pytestmark = needs_server


async def test_resync_shared_multi_get():
    """A 2v2 teams board is 600 cells, so the fetch takes the multi-chunk
    path (two multi-subject direct gets bound to one stream sequence) and
    must come back as exactly the committed state: settled cells, our own
    piece, a teammate's piece, a vacated cell, the LAST sequence of a cell
    written twice, nothing from the other team's board, and none of the
    stale local state it replaces."""
    async with ServerEnv() as env:
        gid = await env.stream(atomic=False)
        g = Game(env.agent("me"), gid, 1)
        env.games.append(g)
        g.mode, g.team, g.player_count, g.w = MODE_TEAMS, 0, 4, 20
        g.h = shared_height(2, 6)  # two seats with six extra rows each: 30 × 20 = 600 cells, past one chunk
        assert g.height() * g.w > FETCH_CHUNK, "this test wants the multi-chunk path"

        async def pub(at, wc):
            return await jsx.js_publish(env.nc, g.cell_subject(at), payload_bytes(wc))

        # A settled cell rewritten once: the second write must win.
        await pub((29, 0), WireCell(o=True, t=3))
        locked_seq = await pub((29, 0), WireCell(o=True, t=5))
        # A vacated cell: written, then emptied.
        await pub((20, 7), WireCell(o=True, t=1))
        empty_seq = await pub((20, 7), None)
        # Our own piece (idx 1) at its spawn.
        mine = Active(2, 0, 2, 3)
        for c in mine.cells():
            await pub(c, WireCell(a=True, t=mine.pt, r=mine.orient, ar=mine.row, ac=mine.col, pi=1))
        # A teammate's piece (idx 0) mid-fall in the other section.
        theirs = Active(4, 1, 10, 13)
        for c in theirs.cells():
            await pub(c, WireCell(a=True, t=theirs.pt, r=theirs.orient, ar=theirs.row, ac=theirs.col, pi=0))
        # The other team's board shares the stream but must not leak in.
        await jsx.js_publish(env.nc, f"jetris.game.{gid}.team.1.playfield.cell.29.0", b'{"o":true,"t":1}')

        # Stale local state the resync must replace wholesale.
        g.locked[(5, 5)] = WireCell(o=True, t=1)
        g.seqs[(5, 5)] = 999
        g.others_act[(6, 6)] = 3
        g.others_piece[3] = Active(1, 0, 0, 0)

        async with g.mu:
            await g.resync_shared()

        assert g.piece == mine
        assert g.others_piece.get(0) == theirs
        assert len(g.others_piece) == 1 and len(g.others_act) == 4
        assert g.locked.get((29, 0)) == WireCell(o=True, t=5), "the second write of a rewritten cell wins"
        assert list(g.locked) == [(29, 0)]
        assert g.seqs[(29, 0)] == locked_seq
        assert g.seqs[(20, 7)] == empty_seq
        assert (5, 5) not in g.seqs, "stale local sequence survived the resync"
