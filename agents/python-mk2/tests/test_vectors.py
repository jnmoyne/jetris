"""The planner against golang-mk2's own answers on seeded random boards
(tests/data/planner_vectors.json, written by golang-mk2's TestWriteVectors):
the same placements, the same lines, the same scores.

Scores are compared to within rounding (the Go compiler may fuse a multiply
and an add where Python never does), and the ranking only where the scores
tell two placements apart.
"""

import json
from pathlib import Path

import pytest

from python_mk2.engine import Geometry, Grid
from python_mk2.path import SpawnBox
from python_mk2.planner import Projection, plan_placements

VECTORS = json.loads((Path(__file__).parent / "data" / "planner_vectors.json").read_text())["plans"]
EPS = 1e-6


def build(v):
    g = Grid(v["h"], v["w"])
    for r, c, kind in v["cells"] or []:
        g.set(r, c, kind)
    proj = None
    if v["proj"] is not None:
        p = v["proj"]
        proj = Projection(
            claimed={(r, c): seat for r, c, seat in p["claimed"] or []},
            soft={(r, c): seat for r, c, seat in p["soft"] or []},
            dep_eta={int(k): eta for k, eta in (p["dep_eta"] or {}).items()},
            own_spawn_c=p["own_spawn_c"],
            boxes=[SpawnBox(seat=s, c0=c0, c1=c1, imminent=bool(im)) for s, c0, c1, im in p["boxes"] or []])
    return g, proj


def test_vector_count():
    assert len(VECTORS) == 160
    assert sum(1 for v in VECTORS if v["proj"] is not None) == 40
    assert sum(1 for v in VECTORS if len(v["upcoming"] or []) == 6) > 5


@pytest.mark.parametrize("i", range(len(VECTORS)))
def test_plan_matches_golang_mk2(i):
    v = VECTORS[i]
    g, proj = build(v)
    assert [g.row_transitions(), g.col_transitions(), g.holes(), g.wells()] == v["features"]
    assert list(Geometry.of(g.h, g.w).features(Geometry.of(g.h, g.w).pack(g)[0])) == v["features"]
    got = plan_placements(g, v["pt"], v["row"], v["col"], v["spawn_c"], v["upcoming"] or [], proj)
    want = v["ranked"] or []
    assert len(got) == len(want)
    # The same placements with the same lines, waits and scores…
    key = lambda o, c, d: (o, c, d)  # noqa: E731
    mine = sorted((key(p.orient, p.col, p.drop_row), p.lines, p.wait_on, p.score) for p in got)
    theirs = sorted((key(w["orient"], w["col"], w["drop_row"]), w["lines"], w["wait_on"] or [], w["score"])
                    for w in want)
    for m, t in zip(mine, theirs):
        assert m[:3] == t[:3]
        assert m[3] == pytest.approx(t[3], abs=EPS), (m, t)
    # …and ranked the same wherever the scores tell them apart.
    for p, w in zip(got, want):
        assert p.score == pytest.approx(w["score"], abs=EPS)
    if len(want) > 1 and want[0]["score"] - want[1]["score"] > EPS:
        assert (got[0].orient, got[0].col, got[0].drop_row) == (want[0]["orient"], want[0]["col"], want[0]["drop_row"])
