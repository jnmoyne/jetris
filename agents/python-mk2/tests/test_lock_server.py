"""The shared board's CAS lock against a live JetStream server."""

from conftest import needs_server
from server_helpers import ServerEnv, assert_converged, publish_cell, stream_cells

from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.path import Active
from python_mk2.wire import WireCell

pytestmark = needs_server


async def test_lock_retries_after_a_lost_cas():
    """A shared-board lock whose landing cell was rewritten behind our back
    loses its CAS once, refetches, and lands on the retry, converged."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        async with g.mu:
            _, placed, topped = await g.spawn()
            assert placed and not topped
            p = g.piece
            dest = Active(p.pt, p.orient, g.drop_row_shared(p), p.col)
            dest_cells = dest.cells()
            # A peer wrote, and emptied, a landing cell: its sequence moved,
            # our expectation for it is stale.
            await publish_cell(g, dest_cells[0], WireCell(o=True, t=1, pi=1))
            await publish_cell(g, dest_cells[0], None)
            assert await g.lock_piece(dest), "the lock was dropped"
            assert (g.st.cas_losses, g.st.lock_retries, g.st.locks_dropped) == (1, 1, 0)
            assert g.piece is None and g.piece_idx == 1
            for c in dest_cells:
                assert c in g.locked and g.locked[c].o, f"landing cell {c} not settled locally"
            await assert_converged(g)


async def test_lock_dropped_under_a_peers_piece():
    """A lock whose landing cell a crewmate's piece moved into is dropped:
    our piece stands where it was, active, and theirs is untouched."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        async with g.mu:
            _, placed, topped = await g.spawn()
            assert placed and not topped
            p = g.piece
            dest = Active(p.pt, p.orient, g.drop_row_shared(p), p.col)
            dest_cells = dest.cells()
            theirs = Active(4, 1, dest.row - 1, dest.col)
            await publish_cell(g, dest_cells[0],
                               WireCell(t=theirs.pt, a=True, r=theirs.orient, ar=theirs.row, ac=theirs.col, pi=1))
            assert not await g.lock_piece(dest), "locked over a crewmate's piece"
            assert g.piece == p, "our piece should stand where it was"
            assert g.st.locks_dropped == 1
            assert g.others_act.get(dest_cells[0]) == 1, "the peer's cell is not in our view"
            snap = await stream_cells(g)
            for c in p.cells():
                wc = snap[c][1]
                assert wc.a and wc.pi == 0, f"our cell {c} on the stream: {wc}"
            for at, (_, wc) in snap.items():
                assert not wc.o, f"a settled cell at {at}: {wc}"
