"""Time a plan, as golang-mk2's plan benchmarks do: a T on a 14x24 ragged
stack, at each lookahead depth.

    uv run python bench_plan.py
"""

import time

from python_mk2.engine import Grid
from python_mk2.path import SpawnBox
from python_mk2.planner import Projection, plan_placements
from python_mk2.pieces import SPAWN_ROW


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


if __name__ == "__main__":
    run("no lookahead", [])
    run("lookahead 1", [0])
    run("lookahead 3", [0, 1, 2])
    run("lookahead 6", [0, 1, 2, 3, 4, 5])
    run("lookahead 6 projected", [0, 1, 2, 3, 4, 5],
        Projection(own_spawn_c=3, boxes=[SpawnBox(seat=1, c0=7, c1=10, imminent=True)]))
