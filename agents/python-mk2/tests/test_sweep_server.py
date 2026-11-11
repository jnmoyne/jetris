"""The shared board's housekeeping against a live JetStream server."""

import time

from conftest import needs_server
from server_helpers import ServerEnv, assert_converged, fold_peer_piece, publish_cell, stream_cells

from python_mk2 import jsx
from python_mk2.consts import MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW
from python_mk2.sweep import IDLE_PIECE_VACATE_AFTER, SPAWN_BLOCKED_VACATE_AFTER
from python_mk2.wire import WireCell, o_int, o_str, to_obj

pytestmark = needs_server


def age_clocks(g):
    for pc in g.peer_cells.values():
        pc.at -= IDLE_PIECE_VACATE_AFTER


async def test_sweep_vacates_idle_peer_piece():
    """A crewmate's piece idle for IDLE_PIECE_VACATE_AFTER is vacated (one
    CAS batch at its cells' last-seen sequences) and gone from the board and
    from our view."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        async with g.mu:
            theirs = Active(4, 1, 10, 8)
            await fold_peer_piece(g, 1, theirs)
            assert g.stale_groups(time.monotonic()) == [], "a piece just delivered is stale"
            age_clocks(g)
            st = g.stale_groups(time.monotonic())
            assert len(st) == 1
            assert await g.vacate_foreign_cells(st[0].cells), "the vacate did not land"
            snap = await stream_cells(g)
            for c in theirs.cells():
                wc = snap[c][1]
                assert not wc.a and not wc.o, f"cell {c} still on the stream: {wc}"
            assert not g.others_act and not g.others_piece and not g.peer_cells, "our view kept the piece"


async def test_sweep_skips_a_piece_that_moved():
    """A piece whose owner moved it since we grouped it fails the vacate's
    CAS: nothing is vacated, and the loss resyncs our view to the stream's."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        async with g.mu:
            theirs = Active(4, 1, 10, 8)
            await fold_peer_piece(g, 1, theirs)
            age_clocks(g)
            group = g.stale_groups(time.monotonic())[0]
            cs = theirs.cells()
            moved = Active(theirs.pt, theirs.orient, theirs.row + 1, theirs.col)
            new_seq = await publish_cell(
                g, cs[0], WireCell(t=moved.pt, a=True, r=moved.orient, ar=moved.row, ac=moved.col, pi=1))
            assert not await g.vacate_foreign_cells(group.cells), "vacated a piece that moved"
            pc = g.peer_cells[cs[0]]
            assert pc.seq == new_seq and pc.p == moved, f"the loss did not resync the moved cell: {pc}"
            snap = await stream_cells(g)
            for c in cs[1:]:
                assert snap[c][1].a, f"cell {c} was vacated"


async def test_spawn_blocker_vacated_after_three_seconds():
    """A piece parked on our spawn box, unmoved at the same sequences for
    SPAWN_BLOCKED_VACATE_AFTER, is vacated whole, and the spawn goes
    through."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        async with g.mu:
            cs = g.spawn_cells()
            blocker = Active(g.piece_at(g.piece_idx), 0, SPAWN_ROW, g.spawn_c)
            await fold_peer_piece(g, 1, blocker)
            _, placed, topped = await g.spawn()
            assert not placed and not topped, "spawn over a foreign piece: want deferred"
            t0 = time.monotonic()
            assert not await g.note_spawn_blocked(cs, t0), "vacated at once"
            assert not await g.note_spawn_blocked(cs, t0 + SPAWN_BLOCKED_VACATE_AFTER - 0.001), \
                "vacated before the threshold"
            assert await g.note_spawn_blocked(cs, t0 + SPAWN_BLOCKED_VACATE_AFTER), \
                "the stale blocker was not vacated"
            assert not g.shared_blocked(cs) and g.st.spawn_unblocks == 1
            snap = await stream_cells(g)
            for c in cs:
                wc = snap[c][1]
                assert not wc.a and not wc.o, f"cell {c} still held on the stream: {wc}"
            _, placed, topped = await g.spawn()
            assert placed and not topped, "spawn after the vacate"
            await assert_converged(g)


async def test_team_vacate_is_gated():
    """On a team's board the vacate takes the txn gate, so a racing
    transform can never resurrect the piece from a stale snapshot."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_TEAMS, 0, 2)
        async with g.mu:
            theirs = Active(4, 1, 10, 8)
            await fold_peer_piece(g, 1, theirs)
            age_clocks(g)
            st = g.stale_groups(time.monotonic())
            assert len(st) == 1 and await g.vacate_foreign_cells(st[0].cells), "gated vacate did not land"
            raw = await jsx.get_last_msg(env.js, g.stream, g.txn_subject())
            reg = to_obj(raw.data)
            assert o_str(reg, "op") == "vacate" and o_int(reg, "by") == 0 and g.txn_seq == raw.seq
            snap = await stream_cells(g)
            for c in theirs.cells():
                wc = snap[c][1]
                assert not wc.a and not wc.o, f"cell {c} still on the stream: {wc}"
            assert not g.others_act and not g.others_piece and not g.peer_cells
