"""The batch pipeline against a live JetStream server."""

import pytest
from conftest import needs_server
from server_helpers import ServerEnv, assert_converged

from python_mk2 import jsx
from python_mk2.consts import PUB_ASYNC, PUB_OPTIMISTIC
from python_mk2.path import Active
from python_mk2.wire import CellUpd

pytestmark = needs_server


@pytest.mark.parametrize("pub", [PUB_ASYNC, PUB_OPTIMISTIC], ids=["async", "optimistic"])
async def test_pipelined_moves(pub):
    """A run of piece moves through the async and optimistic pipelines
    without awaiting the individual acks, settled: the stream holds exactly
    the final position with the write-through sequences reconciled to the
    actual ones."""
    async with ServerEnv() as env:
        g = await env.pipeline_test_game(pub)
        async with g.mu:
            p = Active(2, 0, 2, g.spawn_c)
            await g.publish_batch([CellUpd(c, g.active_payload(p)) for c in p.cells()], True)
            g.piece = p

            # A whole walk, pipelined: rotate, three shifts, two gravity rows.
            steps = [
                Active(p.pt, 1, p.row, p.col),
                Active(p.pt, 1, p.row, p.col + 1),
                Active(p.pt, 1, p.row, p.col + 2),
                Active(p.pt, 1, p.row, p.col + 3),
                Active(p.pt, 1, p.row + 1, p.col + 3),
                Active(p.pt, 1, p.row + 2, p.col + 3),
            ]
            for to in steps:
                assert await g.publish_piece_move(to), f"pipelined move to {to} reported failure"
            assert not await g.settle_pipeline(), "uncontended pipeline should settle without a repair"
            assert g.piece == steps[-1]
            assert g.inflight == 0 and not g.inflight_cells and not g.inflight_seq
            await assert_converged(g)


async def test_pipeline_repair():
    """A pipelined batch lost on purpose (a cell the next move's CAS covers
    is rewritten behind the agent's back) with more batches pipelined behind
    the loss: the settle repairs the board back to one consistent piece,
    stream truth adopted, every stray vacated, sequences exact."""
    async with ServerEnv() as env:
        g = await env.pipeline_test_game(PUB_ASYNC)
        async with g.mu:
            p = Active(1, 0, 2, g.spawn_c)  # O piece: 2x2, easy geometry
            await g.publish_batch([CellUpd(c, g.active_payload(p)) for c in p.cells()], True)
            g.piece = p

            # Sabotage: the cell the first down-move writes into gets a value
            # the agent has never seen, so the move's expectation (0) loses
            # its CAS.
            sab = (p.row + 2, p.col)  # O occupies rows p.row..p.row+1
            await jsx.js_publish(env.nc, g.cell_subject(sab), b'{"o":true,"t":3}')

            # Three pipelined down-moves: the first is lost, the two behind
            # it are poisoned (computed assuming it committed). A move may
            # already observe the break: its False return means the
            # settle+repair ran inside it.
            repaired = False
            for i in range(1, 4):
                if not await g.publish_piece_move(Active(p.pt, p.orient, p.row + i, p.col)):
                    repaired = True
                    break
            if await g.settle_pipeline():
                repaired = True
            assert repaired, "a lost pipelined batch must trigger a repair"
            assert not g.pipe_broken, "pipeline still marked broken after settle"
            assert g.piece is not None, "repair lost the piece entirely"
            assert g.st.cas_losses == 1, "the loss itself flashes, the poisoned tail does not"
            await assert_converged(g)
