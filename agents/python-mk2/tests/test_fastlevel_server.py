"""A level faster than the agent's turns, against a live JetStream server."""

import time

from conftest import needs_server
from server_helpers import ServerEnv, assert_converged, publish_cell

from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.coord import REPLAN_NONE
from python_mk2.difficulty import difficulty_tuning
from python_mk2.path import Active
from python_mk2.planner import Placement
from python_mk2.shared import LINES_PER_LEVEL, MAX_LEVEL
from python_mk2.wire import WireCell

pytestmark = needs_server


async def test_the_walk_follows_gravity_in_the_same_turn():
    """At a level whose rows come faster than the agent's turns do (level 12
    on: 28 ms a row against hard's 30 ms move step) every turn owes a fall.
    The walk follows that fall in the same turn; left to a turn that owes
    none, it never happens: the piece comes down its spawn columns onto the
    stack, and whatever stands between it and its column there (a wall,
    here) keeps it from its plan for good."""
    async with ServerEnv() as env:
        g = await env.shared_test_game(MODE_COOPERATIVE, 0, 2)
        g.a.tn = difficulty_tuning("hard")
        g.total_lines = (MAX_LEVEL - 1) * LINES_PER_LEVEL
        assert g.gravity_now() < g.a.tn.move_delay, "the test's premise: gravity faster than the move step"
        async with g.mu:
            spawn_t, placed, topped = await g.spawn()
            assert placed and not topped
            p = g.piece
            # A wall four rows high on the floor, a column clear of the
            # piece's widest, and the plan beyond it: an easy walk up where
            # the piece spawns, none along the floor.
            wall = p.col + 4
            wc = WireCell(o=True, t=1)
            for r in range(g.h - 4, g.h):
                g.fold_cell((r, wall), wc, await publish_cell(g, (r, wall), wc))
            to = Active(p.pt, p.orient, p.row, wall + 1)
            assert g.can_place(to.cells()), f"the plan's column {to.col} is off the {g.w}-column board"
            to = to._replace(row=g.drop_row_shared(to))
            plan = Placement(orient=to.orient, col=to.col, drop_row=to.row, dest=to.cells())
            g.next_gravity = time.monotonic()  # the first turn owes a row already

        await g.execute(plan, spawn_t)

        async with g.mu:
            assert g.replan == REPLAN_NONE, f"the piece was not driven to its plan: re-plan asked, piece {g.piece}"
            assert g.piece is None and g.piece_idx == 1, "not locked"
            for c in plan.dest:
                assert c in g.locked and g.locked[c].o, f"planned cell {c} not settled"
            assert g.st.stand_locks == 0
            await assert_converged(g)
