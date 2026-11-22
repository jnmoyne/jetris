"""Time a plan, as golang-mk2's plan benchmarks do: a T on a 14x24 ragged
stack, at each lookahead depth; and the route costs a coordinated plan adds.

    uv run python bench_plan.py
"""

import asyncio
import time

from python_mk2.blackboard import Coordinator
from python_mk2.consts import MODE_COOPERATIVE
from python_mk2.difficulty import difficulty_tuning
from python_mk2.engine import Grid
from python_mk2.game import Game
from python_mk2.path import Active, SpawnBox
from python_mk2.pieces import SPAWN_ROW
from python_mk2.planner import Projection, plan_placements
from python_mk2.rules import MIN_EXTRA_COLUMNS
from python_mk2.shared import shared_height, shared_width
from python_mk2.wire import WireCell


def bench_grid():
    """A 14-wide crew's board with a ragged stack, the kind the planner sees
    mid-game."""
    g = Grid(24, 14)
    for c, hc in enumerate([3, 4, 4, 2, 5, 3, 3, 6, 4, 2, 3, 5, 4, 3]):
        for r in range(24 - hc, 24):
            g.set(r, c, 1)
    return g


def run(name, upcoming, proj=None, budget=2.0):
    g = bench_grid()
    n, t0 = 0, time.perf_counter()
    while True:
        ranked = plan_placements(g, 2, SPAWN_ROW, 3, 3, upcoming, proj)
        n += 1
        dt = time.perf_counter() - t0
        if dt > budget or n >= 500:
            break
    print(f"{name:<22} {dt / n * 1000:8.2f} ms/plan   ({len(ranked)} placements)")


class _Agent:
    """What a Game reads of its agent to price a route."""

    name, pub, trace, soft_drop, well = "me", 0, False, False, "auto"
    tn = difficulty_tuning("hard")


def run_routes(budget=2.0):
    """The path-search floods a coordinated plan runs to price its
    placements' walks (coord.route_costs), as golang-mk2's BenchmarkRouteCosts
    has them: a four-seat board, three teammates' pieces in the air, a stack
    a few rows high."""

    async def make():  # a Game holds asyncio primitives: it is built inside a loop
        g = Game(_Agent(), "g", 0)
        g.mode, g.player_count, g.seats_on_pf, g.extra = MODE_COOPERATIVE, 4, 4, MIN_EXTRA_COLUMNS
        g.w, g.h = shared_width(4, MIN_EXTRA_COLUMNS), shared_height(4, 0)
        g.coord = Coordinator(board="crew", key="g.crew.0", watch="g.crew.*")
        return g

    g = asyncio.run(make())
    for r in range(g.h - 6, g.h):
        for c in range(g.w):
            if c != r % g.w:
                g.locked[(r, c)] = WireCell(o=True, t=1)
    for seat in range(1, 4):
        theirs = Active(2, 0, SPAWN_ROW, 3 + 4 * seat)
        g.others_piece[seat] = theirs
        for c in theirs.cells():
            g.others_act[c] = seat
    p = Active(0, 0, SPAWN_ROW, 3)
    now = time.monotonic()
    n, t0 = 0, time.perf_counter()
    while True:
        costs = g.route_costs(p, now, 0.1)
        n += 1
        dt = time.perf_counter() - t0
        if dt > budget or n >= 500:
            break
    print(f"{'route costs, 4 seats':<22} {dt / n * 1000:8.2f} ms        ({len(costs)} columns and turns priced)")


if __name__ == "__main__":
    run("no lookahead", [])
    run("lookahead 1", [0])
    run("lookahead 3", [0, 1, 2])
    run("lookahead 6", [0, 1, 2, 3, 4, 5])
    run("lookahead 6 projected", [0, 1, 2, 3, 4, 5],
        Projection(own_spawn_c=3, boxes=[SpawnBox(seat=1, c0=7, c1=10, imminent=True)]))
    run_routes()
