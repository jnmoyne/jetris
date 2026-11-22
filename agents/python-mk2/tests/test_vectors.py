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
from python_mk2.planner import NO_WELL, Projection, lookahead, plan_placements, principal_variation, simulate_lock

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
        if p["route"] is not None:
            # A projection as plan() makes it: the walk's costs, the
            # teammates' tentative landings, our own intent, the well.
            proj.route = {(int(o), int(c)): cost for o, c, cost in p["route"]}
            proj.route_now = p["route_now"]
            proj.tent = {(int(r), int(c)): weight for r, c, weight in p["tent"] or []}
            proj.intent = tuple(p["intent"]) if p["intent"] is not None else None
            proj.well, proj.well_on, proj.well_high = p["well"], p["well_on"], p["well_high"]
    return g, proj


def test_vector_count():
    assert len(VECTORS) == 160
    assert sum(1 for v in VECTORS if v["proj"] is not None) == 40
    assert sum(1 for v in VECTORS if len(v["upcoming"] or []) == 6) > 5
    routed = [v for v in VECTORS if v["proj"] is not None and v["proj"]["route"] is not None]
    assert len(routed) == 20 and sum(1 for v in routed if v["proj"]["well_on"]) > 10
    assert sum(1 for v in routed if v["upcoming"]) > 10 and sum(1 for v in VECTORS if v["pv"]) > 100


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
    # The lookahead's best line from the best placement (what a claim says
    # for the pieces to come), and what it scores.
    if v["pv"] is not None:
        best = next(p for p in got if (p.orient, p.col, p.drop_row) == (want[0]["orient"], want[0]["col"],
                                                                        want[0]["drop_row"]))
        ws = proj.well_spec() if proj is not None else NO_WELL
        after, _, _ = simulate_lock(g, best.dest)
        upcoming = v["upcoming"]
        assert lookahead(after, v["spawn_c"], upcoming, ws) == pytest.approx(v["pv_score"], abs=EPS)
        line = principal_variation(after, v["spawn_c"], upcoming, ws)
        assert [list(x) for x in line] == v["pv"]
        if best.pv is not None:  # in the search's beam: the line it kept is that one
            assert list(best.pv) == line
