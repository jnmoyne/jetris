"""Routes, right of way, the pieces to come and the well: what a claim says
beyond the landing, and what the planner and the walk make of it."""

import asyncio
import json
import time

import pytest
from helpers import coord_test_game, fill_row, lock_column, planned

from python_mk2 import jsx
from python_mk2.blackboard import DROP_SOFT, PHASE_PLANNED, NextClaim, parse_claim
from python_mk2.coord import BLOCK_PATIENCE_SILENT, DEP_WAIT_TIMEOUT, MAX_HOLE_REPLANS, well_ready
from python_mk2.engine import Grid
from python_mk2.path import Active, waiting_spot
from python_mk2.pieces import SPAWN_ROW, piece_cells
from python_mk2.planner import (DEP_PENALTY, INTENT_BONUS, ROUTE_BLOCKED, SILENT_LEAVE, TENT_PENALTY, WELL_COVER,
                                Placement, Projection, evaluate_well, plan_placements, simulate_lock)
from python_mk2.wire import WireCell


def peer_piece(g, seat, p):
    """Put a teammate's falling piece on the board."""
    for c in p.cells():
        g.others_act[c] = seat
    g.others_piece[seat] = p


def fold(g, c, rev):
    g.fold_claim_raw("g.crew.1", c.to_bytes(), rev, jsx.KV_PUT)


async def test_claim_keeps_its_place_when_said_again():
    # A claim said again for the same placement of the same piece (a wait
    # announced, a preview refreshed) keeps its place in the right of way; a
    # claim for another landing takes a new one. And a claim that says it
    # waits on us ends our think pause.
    g = coord_test_game()
    c = planned(1, 4, 2, 1, 9, 0)
    fold(g, c, 10)
    assert g.claims[1].order() == 10
    hurry = g.hurry = asyncio.Event()
    c.wait_on = (0,)
    fold(g, c, 15)
    got = g.claims[1]
    assert got.order() == 10 and got.rev == 15 and got.waits_on(0)
    assert hurry.is_set(), "a claim waiting on our piece did not end the think pause"
    assert g.hurry is None, "the hurry event was left armed"
    assert g.waited_on(time.monotonic()), "a teammate waits on us, and it says not"
    c.col, c.wait_on = 5, ()
    fold(g, c, 20)
    assert g.claims[1].order() == 20
    assert not g.waited_on(time.monotonic()), "nobody waits on us any more"
    # A piece type there is not is no claim at all.
    c.type = 9
    fold(g, c, 25)
    assert 1 not in g.claims


def test_claim_optional_fields():
    # The optional fields are optional: a claim without them reads as a
    # hard-dropping piece that waits on nobody, and one with them
    # round-trips.
    c = parse_claim(b'{"v":1,"seat":1,"piece":3,"type":2,"rot":1,"col":5,"phase":"planned"}')
    assert c is not None
    assert c.at is None and c.drop == "" and c.wait_on == () and c.next == () and c.well is None
    assert all(k not in json.loads(c.to_bytes()) for k in ("at", "drop", "wait_on", "next", "well"))
    c.wait_on, c.next, c.well, c.drop, c.at = (0,), (NextClaim(piece=4, type=0, rot=1, col=11),), 13, DROP_SOFT, (2, 5, 1)
    back = parse_claim(c.to_bytes())
    assert back == c
    assert back.waits_on(0) and back.next[0] == c.next[0] and back.well == 13 and back.drop == DROP_SOFT
    # Fields of the wrong shape make it no claim, as the base fields do.
    for bad in (b'{"v":1,"wait_on":"0"}', b'{"v":1,"next":[3]}', b'{"v":1,"well":"13"}', b'{"v":1,"at":[1,"2",3]}',
                b'{"v":1,"next":[{"piece":true}]}'):
        assert parse_claim(bad) is None, bad


async def test_forecast():
    # The forecast: a piece whose claim says it lands before we move is
    # gone; one that lands later, soft-drops, waits or says nothing is in the
    # air: where it is, and the rows gravity takes it through until we move.
    g = coord_test_game()
    now = time.monotonic()
    theirs = Active(2, 0, SPAWN_ROW, 7)
    peer_piece(g, 1, theirs)
    c = planned(1, 4, 2, 0, 9, 10)
    c.eta_ms, c.seen_at = 100, now
    g.claims[1] = c
    assert g.forecast(now, now + 0.2) == []
    fc = g.forecast(now, now + 0.05)
    assert len(fc) == 1 and fc[0].seat == 1 and len(fc[0].cells) == 4
    assert fc[0].leave_at == pytest.approx(now + 0.1)
    from dataclasses import replace

    for cc in (replace(c, drop=DROP_SOFT), replace(c, wait_on=(0,))):
        g.claims[1] = cc
        fc = g.forecast(now, now + 0.2)
        assert len(fc) == 1 and fc[0].leave_at == pytest.approx(now + SILENT_LEAVE), fc
    del g.claims[1]  # a seat that says nothing
    fc = g.forecast(now, now + 2 * g.gravity_now() + 0.001)
    assert len(fc) == 1 and len(fc[0].cells) == 12, "a silent piece over two gravity steps: its four cells at three rows"


async def test_route_costs_price_the_wait_not_the_distance():
    # A placement's walk is priced by the path, and a crewmate's piece
    # across the way by the wait for it to go: nothing when its claim says
    # it will have gone by the time we move.
    g = coord_test_game()
    now = time.monotonic()
    theirs = Active(0, 1, 0, 6)  # vertical I in column 8 (rows 0-3)…
    peer_piece(g, 1, theirs)
    wall = theirs.cells()[0][1]
    lock_column(g, wall, 4)  # …on a stack filling the column below it
    p = Active(1, 0, SPAWN_ROW, g.spawn_c)
    near, far = (0, g.spawn_c - 2), (0, 11)

    c = planned(1, 4, 0, 1, 6, 10)
    c.eta_ms, c.seen_at = 400, now
    g.claims[1] = c
    costs = g.route_costs(p, now, 0.1)
    want_wait = DEP_PENALTY * 0.3  # they leave 300 ms after we mean to move
    assert want_wait <= costs[far] <= want_wait + 0.5, "behind their piece: the wait plus a walk"
    assert 0 < costs[near] <= 0.2, "two columns away on a free board: a tie-break"

    c.eta_ms = 50  # gone before we move: the rows it stood in are the way through
    g.claims[1] = c
    costs = g.route_costs(p, now, 0.1)
    assert far in costs and costs[far] <= 0.5, "their piece gone by the time we move: a walk's cost"

    now2 = g.route_costs_now(p)
    assert far not in now2, "route_costs_now priced a placement behind a piece that is still there"
    proj = Projection(route=now2, route_now=True)
    assert proj.penalty(Placement(orient=far[0], col=far[1])) >= ROUTE_BLOCKED
    # Planned ahead, what the route does not reach costs a silent piece's wait.
    assert Projection(route={}).penalty(Placement(orient=0, col=3)) == DEP_PENALTY * SILENT_LEAVE


async def test_keep_out_of_earlier_shafts():
    # The cells a wait keeps out of are the drop shafts of the claims with
    # the right of way over ours (not of the ones made after it) and a
    # waiting spot is chosen outside them.
    g = coord_test_game()
    g.my_claim, g.my_prio = planned(0, 2, 1, 0, 2, 20), 20
    peer_piece(g, 1, Active(0, 0, SPAWN_ROW, 7))
    g.claims[1] = planned(1, 4, 0, 1, 8, 10)  # vertical I down column 10, claimed before ours
    keep = g.keep_out()
    assert (5, 10) in keep and (0, 10) in keep and (5, 9) not in keep and (g.height() - 1, 10) not in keep
    g.claims[1] = planned(1, 4, 0, 1, 8, 30)  # claimed after ours: theirs to keep clear of us
    assert not g.keep_out(), "a later claim's shaft is kept out of"

    closed = {(2, 3, 0), (2, 9, 0), (2, 7, 0)}
    dist = {(2, 3, 0): 0.0, (2, 9, 0): 6.0, (2, 7, 0): 4.0}
    keep_cells = {(2, 10), (3, 10)}  # an O at column 9 stands in it
    spot = waiting_spot(closed, dist, (2, 3, 0), 11, 0, [], keep_cells, 1)
    assert spot is not None and spot[1] == 7, "the nearest spot outside the shaft"
    spot = waiting_spot(closed, dist, (2, 3, 0), 11, 0, [], None, 1)
    assert spot[1] == 9, "with nothing to keep out of"


async def test_path_names_the_seats_in_the_way():
    # A blocked path says whose piece blocks it, and a wait is chosen
    # outside an earlier claim's shaft where it can be.
    g = coord_test_game()
    theirs = Active(0, 1, 0, 6)
    peer_piece(g, 1, theirs)
    lock_column(g, theirs.cells()[0][1], 4)
    p = Active(1, 0, SPAWN_ROW, g.spawn_c)
    res = g.find_path(p, Placement(orient=0, col=11), [])
    assert not res.reached and res.blocked_by_transient and res.blockers == [1]
    keep = {(r, c) for r in range(g.height()) for c in (6, 7)}  # the columns right up against the piece
    res = g.find_path(p, Placement(orient=0, col=11), [], keep)
    assert not res.reached and not any(c in keep for c in res.end.cells())


async def test_block_patience():
    # Who waits and for how long is the right of way's: a piece nobody
    # claimed gets a short patience, an earlier claim twice what it says it
    # needs, and against later claims we hold the plan: they are the ones to
    # move.
    g = coord_test_game()
    now = time.monotonic()
    g.my_prio = 20
    peer_piece(g, 1, Active(2, 0, SPAWN_ROW, 7))
    assert g.block_patience([1], now) == BLOCK_PATIENCE_SILENT
    c = planned(1, 4, 2, 0, 9, 10)
    c.eta_ms, c.seen_at = 400, now
    g.claims[1] = c
    assert g.block_patience([1], now) == pytest.approx(0.8)
    c.rev = 30
    assert g.block_patience([1], now) == DEP_WAIT_TIMEOUT, "a later claim: theirs to move"
    assert g.owners_under(Active(1, 0, 0, 7)) == [1]


def well_board(ready):
    """A 22-column board with `ready` rows full but for the last column."""
    g = Grid(24, 22)
    for r in range(24 - ready, 24):
        for c in range(21):
            g.set(r, c, 1)
    return g


def touches(pl, col):
    return any(c == col for _, c in pl.dest)


def test_well_placements():
    # Keeping the well: an I piece goes down it once four rows are banked
    # and stays out of it before, unless the stack stands high, when it
    # takes the rows there are; and no other piece is laid in or across it.
    on = Projection(well=21, well_on=True)
    high = Projection(well=21, well_on=True, well_high=True)

    best = plan_placements(well_board(5), 0, SPAWN_ROW, 3, 3, [], on)[0]
    assert best.lines == 4 and touches(best, 21), "five rows banked: the I goes down the well for 4"
    best = plan_placements(well_board(2), 0, SPAWN_ROW, 3, 3, [], on)[0]
    assert best.lines == 0 and not touches(best, 21), "two rows banked: the I is kept out of the well"
    best = plan_placements(well_board(2), 0, SPAWN_ROW, 3, 3, [], high)[0]
    assert best.lines == 2 and touches(best, 21), "two rows banked, the stack high: the well for 2"
    for pt in range(1, 7):
        for proj in (on, high):
            best = plan_placements(well_board(5), pt, SPAWN_ROW, 3, 3, [], proj)[0]
            assert not touches(best, 21), f"piece {pt} (high {proj.well_high}) was laid in or across the well"
    # Played plainly, the same board has its deep well capped by the first
    # piece that fits across it: what keeping the well is there to stop.
    g = well_board(5)
    covering = ((18, 18), (18, 19), (18, 20), (18, 21))
    after, lines, eroded = simulate_lock(g, covering)
    flat = ((18, 0), (18, 1), (18, 2), (18, 3))
    after_flat, _, _ = simulate_lock(g, flat)
    ws = (21, False)
    assert (evaluate_well(after, covering, lines, eroded, ws)
            < evaluate_well(after_flat, flat, 0, 0, ws) - 5 * WELL_COVER), \
        "an I laid across the top of a five-deep well is not priced for the five cells it buries"


def test_lookahead_takes_a_ready_well():
    # With the preview looked at, a ready well is still taken: the rows
    # banked beside it are not worth more kept than cleared.
    upcoming = [1, 2, 1, 1, 2, 3]  # no I to come: this one, or none
    for ready in (4, 6, 10):
        for high in (False, True):
            proj = Projection(well=21, well_on=True, well_high=high)
            best = plan_placements(well_board(ready), 0, SPAWN_ROW, 3, 3, upcoming, proj)[0]
            assert best.lines == 4, (ready, high, best.orient, best.col, best.lines)
            # The search kept the line it found best from there: one
            # placement per piece of the preview.
            assert best.pv is not None and len(best.pv) == len(upcoming)


async def test_well_for():
    # The well is kept when the agent wants one, every teammate's claim
    # names the same column and nothing sits over it; a high stack is said,
    # not a reason to stop.
    g = coord_test_game()
    g.a.well = "auto"
    gr = g.to_grid_projected()
    assert not g.well_for(gr)[1], "the well is on before the teammate has said anything"
    w = g.well_column()
    c = planned(1, 4, 2, 0, 9, 10)
    c.well = w
    g.claims[1] = c
    assert g.well_for(gr) == (g.w - 1, True, False)
    c.well = 0
    assert not g.well_for(gr)[1], "the well is on though the teammate names another column"
    c.well = w
    g.survival = "slow"
    assert not g.well_wanted(), "--well auto keeps a well in a survival game"
    g.a.well = "on"
    assert g.well_wanted(), "--well on keeps no well in a survival game"
    g.survival, g.a.well = "", "off"
    assert not g.well_wanted(), "--well off keeps a well"
    g.a.well = "auto"
    for r in range(g.height() - 12, g.height()):
        fill_row(g, r, w)
    _, on, high = g.well_for(g.to_grid_projected())
    assert on and high, "twelve rows banked: the well kept and the stack called high"
    g.locked[(g.height() - 13, w)] = WireCell(o=True, t=1)  # a cell over the well
    assert not g.well_for(g.to_grid_projected())[1], "the well is on with a cell sitting over it"
    del g.locked[(g.height() - 13, w)]
    assert well_ready(g.to_grid_projected(), w) == 12
    cl = g.claim_for(Active(2, 0, SPAWN_ROW, g.spawn_c), Placement(dest=piece_cells(2, 0, 21, 3)), PHASE_PLANNED, 0)
    assert cl.well == w, "our claim does not name the well"
    assert json.loads(cl.to_bytes())["well"] == w


async def test_stamp_tentative():
    # A teammate's tentative placements for its NEXT pieces are a nudge on
    # the projection (the first weighted 1, the second a half, dropped on
    # the first), never a wall; nonsense is ignored.
    g = coord_test_game()
    c = planned(1, 4, 2, 0, 9, 10)
    c.next = (NextClaim(piece=5, type=1, rot=0, col=5), NextClaim(piece=6, type=1, rot=0, col=5),
              NextClaim(piece=7, type=1, rot=0, col=0))
    g.claims[1] = c
    gr, proj = g.build_projection()
    h = g.height()
    assert proj.tent[(h - 1, 5)] == 1 and proj.tent[(h - 2, 6)] == 1, proj.tent
    assert proj.tent[(h - 3, 5)] == 0.5 and proj.tent[(h - 4, 6)] == 0.5, proj.tent
    assert len(proj.tent) == 8, "the 8 cells of the first two pieces"
    assert not gr.filled(h - 1, 5), "a tentative placement is a wall on the grid"
    on_it = Placement(dest=((h - 1, 5), (h - 1, 6), (h - 2, 5), (h - 2, 6)))
    clear = Placement(dest=((h - 1, 0), (h - 1, 1), (h - 2, 0), (h - 2, 1)))
    assert proj.penalty(on_it) - proj.penalty(clear) == pytest.approx(4 * TENT_PENALTY)
    c.next = (NextClaim(piece=5, type=9, rot=0, col=5), NextClaim(piece=4, type=1, rot=0, col=5))
    _, proj = g.build_projection()
    assert not proj.tent, "a piece type that does not exist, a piece already played"
    # What we said for the piece now in play is worth keeping.
    g.piece_idx = 3
    g.my_intent = NextClaim(piece=3, type=2, rot=1, col=4)
    _, proj = g.build_projection()
    assert proj.intent == (1, 4)
    kept = Placement(orient=1, col=4, dest=clear.dest)
    other = Placement(orient=2, col=4, dest=clear.dest)
    assert proj.penalty(other) - proj.penalty(kept) == pytest.approx(INTENT_BONUS)


async def test_spoils():
    # A landing that would cover more than its plan expected is not locked:
    # it is planned again, a bounded number of times.
    g = coord_test_game()
    h = g.height()
    fill_row(g, h - 1, 5, 6)  # a two-cell gap on the floor
    bridge = Active(0, 0, h - 3, 4)
    assert all(r == h - 2 for r, _ in bridge.cells()), "test geometry: the I lies on the row above the gap"
    assert g.spoils(bridge), "an I laid over a two-cell gap, with none planned: not a spoiled landing"
    assert g.hole_replans == 1
    g.plan_holes = 2
    assert not g.spoils(bridge), "the plan expected the two holes: spoiled all the same"
    g.plan_holes, g.hole_replans = 0, MAX_HOLE_REPLANS
    assert not g.spoils(bridge), "past the bound, the landing is taken as it is"
    g.hole_replans = 0
    assert not g.spoils(Active(1, 0, h - 2, 5)), "an O dropped into the gap spoils nothing"
    # What a plan expects of its landing is taken on the board it was made on.
    g.note_plan_holes(g.to_grid_projected(), Placement(dest=bridge.cells()))
    assert g.plan_holes == 2
