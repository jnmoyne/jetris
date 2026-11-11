"""Coordination on a shared board: the projection of the teammates' claims,
the conflict rules, the waits, the penalties and the spawn boxes."""

import time

from helpers import coord_test_game, fill_row, planned, seat, sweep_test_game

from python_mk2.blackboard import CLAIM_VERSION, PHASE_LOCKED, PHASE_PLANNED, Claim
from python_mk2.coord import (CONFLICT_DEPENDENCY, CONFLICT_OVERLAP, NO_CONFLICT, PLAN_OK, PLAN_STALE,
                              PLAN_WAIT_DEP, DepWait, columns_meet)
from python_mk2.engine import GRID_CLAIMED
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW, piece_cells
from python_mk2.planner import (CROSS_COST, DEP_PENALTY, SOFT_PENALTY, TRAVEL_COST, Placement, support_seats)
from python_mk2.wire import WireCell


async def test_build_projection():
    # The projection: a planned claim lands where its column and orientation
    # drop on the board as it stands, a locked claim's cells count once the
    # board lacks them, an older piece's claim is nothing, and a silent
    # teammate's piece is dropped straight down as a soft nudge.
    g = coord_test_game()
    fill_row(g, 23, 5, 6, 7, 8)
    # Seat 1 claims an I flat in the gap: its own cells say row 20 (a
    # collapse ago); the projection re-drops it to row 23.
    c = planned(1, 4, 0, 0, 5, 10)
    c.cells = ((20, 5), (20, 6), (20, 7), (20, 8))
    g.claims[1] = c
    g.others_piece[1] = Active(0, 0, 2, 7)
    gr, proj = g.build_projection()
    for col in range(5, 9):
        assert gr.at(23, col) == GRID_CLAIMED and proj.claimed.get((23, col)) == 1, col
    assert gr.at(20, 5) == 0 and proj.dep_eta.get(1) == 150 and proj.pieces.get(1) == 4
    # The seat's board shows another type than the claim's: an old claim.
    g.others_piece[1] = Active(2, 0, 2, 7)
    gr, proj = g.build_projection()
    assert gr.at(23, 5) == 0 and not proj.claimed
    # …and with no claim for it, the T at (2,7) is a silent teammate's: its
    # straight drop is a soft nudge, on the map, not in the grid.
    del g.claims[1]
    stack = g.to_grid_projected()
    gr, proj = g.build_projection()
    ghost = piece_cells(2, 0, stack.drop_row(2, 0, 2, 7), 7)
    for r, col in ghost:
        assert gr.at(r, col) == 0 and proj.soft.get((r, col)) == 1, (r, col)
    assert not proj.claimed and len(proj.soft) == 4
    # A locked claim adds nothing (its cells reach the board off the game
    # stream) and marks the seat heard from: no ghost for its piece, and
    # nothing "rests on a claim" by resting where it locked.
    lc = planned(1, 4, 0, 0, 5, 11)
    lc.phase = PHASE_LOCKED
    lc.cells = ((23, 5), (23, 6), (23, 7), (23, 8))
    g.claims[1] = lc
    gr, proj = g.build_projection()
    assert gr.at(23, 5) == 0 and not proj.claimed and not proj.dep_eta and not proj.soft
    del g.others_piece[1]
    for col in range(5, 9):
        g.locked[(23, col)] = WireCell(o=True, t=0, pi=1)
    gr, proj = g.build_projection()
    on_it = Placement(orient=0, col=5, drop_row=21, dest=piece_cells(0, 0, 21, 5))
    assert support_seats(gr, proj, on_it) == []
    deps = g.merge_deps(Placement(wait_on=[0, 1]), proj, [])
    assert len(deps) == 1 and deps[0].seat == 1, "our own seat must never be a dependency"
    gr, _ = g.build_projection()
    assert gr.at(23, 5) == 1, "a landed locked claim was re-stamped"


async def test_classify():
    # An earlier landing meeting ours is an overlap; one that holds ours up,
    # or that ours would displace, is a dependency; a claim made after ours,
    # a locked one, and an old one are none.
    g = coord_test_game()
    fill_row(g, 23, 5, 6, 7, 8)
    me = Active(0, 0, SPAWN_ROW, g.spawn_c)
    stack = g.to_grid_projected()

    def landing(orient, col):
        dr = stack.drop_row(0, orient, SPAWN_ROW, col)
        return Placement(orient=orient, col=col, drop_row=dr, dest=piece_cells(0, orient, dr, col))

    gap = landing(0, 5)
    on_top = Placement(orient=0, col=5, drop_row=gap.drop_row - 1, dest=piece_cells(0, 0, gap.drop_row - 1, 5))
    aside = landing(0, 9)
    theirs = planned(1, 0, 0, 0, 5, 10)  # seat 1's I, flat in the gap, claimed at revision 10
    g.claims[1] = theirs
    kind, _, _ = g.classify(me, gap, 20)
    assert kind == CONFLICT_OVERLAP
    kind, deps, _ = g.classify(me, on_top, 20)
    assert kind == CONFLICT_DEPENDENCY and len(deps) == 1
    assert deps[0].seat == 1 and deps[0].rev == 10 and len(deps[0].cells) == 4
    kind, deps, _ = g.classify(me, aside, 20)
    assert kind == NO_CONFLICT and not deps
    # Interference: our landing would change where theirs lands. A T
    # pointing down, resting on column 4 with an arm over column 5 above the
    # gap, would stop their flat I short of it.
    me_t = Active(2, 0, SPAWN_ROW, g.spawn_c)
    dr = stack.drop_row(2, 2, SPAWN_ROW, 3)
    overhang = Placement(orient=2, col=3, drop_row=dr, dest=piece_cells(2, 2, dr, 3))
    kind, deps, _ = g.classify(me_t, overhang, 20)
    assert kind == CONFLICT_DEPENDENCY and len(deps) == 1, overhang.dest
    # A claim made after ours yields to us; a locked one is a board fact; an
    # old one (their board shows another type) is nothing.
    kind, _, _ = g.classify(me, gap, 5)
    assert kind == NO_CONFLICT
    locked = planned(1, 0, 0, 0, 5, 10)
    locked.phase = PHASE_LOCKED
    locked.cells = ((23, 5), (23, 6), (23, 7), (23, 8))
    g.claims[1] = locked
    kind, deps, _ = g.classify(me, on_top, 20)
    assert kind == NO_CONFLICT and not deps
    g.claims[1] = theirs
    g.others_piece[1] = Active(2, 0, 2, 7)
    kind, _, _ = g.classify(me, gap, 20)
    assert kind == NO_CONFLICT
    # A LOCKED claim is no claim here, whatever its revision: its cells
    # reach the board off the game stream, and check_plan finds a landing
    # they have taken.
    del g.others_piece[1]
    late = planned(1, 0, 0, 0, 5, 25)
    late.phase = PHASE_LOCKED
    late.cells = locked.cells
    g.claims[1] = late
    kind, _, taken = g.classify(me, gap, 20)
    assert kind == NO_CONFLICT and not taken
    # Our own key, echoed, is no teammate's claim.
    g.claims[0] = planned(0, 0, 0, 0, 5, 10)
    del g.claims[1]
    kind, _, _ = g.classify(me, gap, 20)
    assert kind == NO_CONFLICT


async def test_check_plan_after_a_collapse():
    # The plan on the live board: stale once the stack has taken a landing
    # cell or nothing holds the landing up (a collapse), a wait while an
    # earlier claim will.
    g = coord_test_game()
    fill_row(g, 23)
    pl = Placement(orient=0, col=5, drop_row=21, dest=piece_cells(0, 0, 21, 5))  # an I flat on the bottom row
    assert g.check_plan(pl) == PLAN_OK
    for c in range(g.w):
        g.locked.pop((22, c), None)
        g.locked.pop((23, c), None)
    assert g.check_plan(pl) == PLAN_STALE
    g.deps = [DepWait(seat=1, piece=3, rev=10, cells=list(piece_cells(0, 0, 22, 5)))]
    g.claims[1] = planned(1, 3, 0, 0, 5, 10)
    assert g.check_plan(pl) == PLAN_WAIT_DEP
    g.locked[(22, 5)] = WireCell(o=True, t=1)
    assert g.check_plan(pl) == PLAN_STALE


async def test_dep_resolved():
    # A wait ends on a newer claim of the seat's (their lock or re-plan), on
    # no claim at all, on the landing settling.
    g = coord_test_game()
    fill_row(g, 23, 5, 6, 7, 8)
    d = DepWait(seat=1, piece=3, rev=10, cells=list(piece_cells(0, 0, 22, 5)))
    g.claims[1] = planned(1, 3, 0, 0, 5, 10)
    assert not g.dep_resolved(d), "resolved while their claim stands"
    # A newer claim of theirs for the SAME landing keeps the wait, under the
    # new revision; one for another landing ends it.
    g.claims[1] = planned(1, 3, 0, 0, 5, 12)
    assert not g.dep_resolved(d) and d.rev == 12
    g.claims[1] = planned(1, 3, 0, 0, 9, 14)
    assert g.dep_resolved(d), "not resolved by a newer claim of theirs for another landing"
    g.claims[1] = planned(1, 3, 0, 0, 5, 14)
    d.rev = 14
    for x in d.cells:
        g.locked[x] = WireCell(o=True, t=0, pi=1)
    assert g.dep_resolved(d), "not resolved by the landing settling"
    del g.claims[1]
    assert g.dep_resolved(DepWait(seat=1, piece=3, rev=10)), "not resolved by their claim's absence"
    g.deps = [DepWait(seat=1, piece=3, rev=10)]
    assert not g.deps_pending()
    # The wait's account: it times out after twice the claim's estimate
    # (within DEP_WAIT_MIN and DEP_WAIT_TIMEOUT), and the claim waited on is
    # then ignored by the re-plan.
    g.claims[1] = Claim(v=CLAIM_VERSION, seat=1, piece=3, type=0, rot=0, col=5, phase=PHASE_PLANNED, eta_ms=400,
                        rev=20)
    g.deps = [DepWait(seat=1, piece=3, rev=20, cells=list(piece_cells(0, 0, 22, 5)))]
    for x in g.deps[0].cells:
        g.locked.pop(x, None)
    assert abs(g.dep_timeout() - 0.8) < 1e-9  # twice the 400 ms estimate
    now = time.monotonic()
    assert not g.await_deps(now) and g.st.dep_waits == 1
    assert not g.await_deps(now + 0.4), "timed out early"
    assert g.await_deps(now + 0.801) and g.st.dep_wait_ms >= 800
    assert g.ignored_claims.get(1) == 20
    _, proj = g.build_projection()
    assert not proj.claimed, "an ignored claim was projected"


async def test_penalty_and_support_seats():
    # The placement penalties: a wait in proportion to the claim's time, the
    # walk per column, every imminent spawn box crossed; and the seats a
    # placement rests on or completes a row with.
    g = coord_test_game()
    fill_row(g, 23, 5, 6, 7, 8)
    g.claims[1] = planned(1, 4, 0, 0, 5, 10)
    g.others_piece[1] = Active(0, 0, 2, 7)
    gr, proj = g.build_projection()
    on_top = Placement(orient=0, col=5, drop_row=21, dest=piece_cells(0, 0, 21, 5))
    assert support_seats(gr, proj, on_top) == [1]
    completing = Placement(orient=1, col=-1, drop_row=20, dest=((20, 0), (21, 0), (22, 0), (23, 0)))
    g.locked.pop((23, 0), None)
    gr, proj = g.build_projection()
    assert support_seats(gr, proj, completing) == [1]
    aside = Placement(orient=0, col=9, drop_row=21, dest=piece_cells(0, 0, 21, 9))
    assert support_seats(gr, proj, aside) == []
    on_top.wait_on = [1]
    want = DEP_PENALTY * 0.15 + TRAVEL_COST * abs(5 - g.spawn_c)
    assert abs(proj.penalty(on_top) - want) < 1e-9
    # Crossing seat 1's spawn box while their spawn is imminent.
    del g.others_piece[1]
    del g.claims[1]
    _, proj = g.build_projection()
    far = Placement(orient=0, col=10, drop_row=21, dest=piece_cells(0, 0, 21, 10))
    assert proj.penalty(far) >= CROSS_COST
    # A silent teammate's straight drop: a placement in it pays per cell.
    g.others_piece[1] = Active(0, 0, 2, 7)
    _, proj = g.build_projection()
    under = Placement(orient=0, col=7, drop_row=21, dest=piece_cells(0, 0, 21, 7))
    assert proj.penalty(under) >= 4 * SOFT_PENALTY


async def test_teammate_boxes():
    # The teammates' spawn boxes among the seats present, on a crew's board
    # of two and of three, at the default step (shoulder to shoulder).
    g = coord_test_game()
    boxes = g.teammate_boxes()
    assert len(boxes) == 1
    b = boxes[0]
    assert b.seat == 1 and b.c0 == g.spawn_column_for(1, [0, 1]) and b.c1 == b.c0 + 3 and b.imminent
    mine = g.spawn_c
    assert b.c0 != mine and not (b.c0 < mine + 4 and b.c1 >= mine), "the box overlaps our own"
    g = sweep_test_game(3)
    g.roster = [seat("me", 0), seat("p1", 1), seat("p2", 2)]
    g.spawn_c = g.spawn_column(g.present_slots(g.roster))
    g.others_piece[2] = Active(2, 0, 2, 11)
    boxes = g.teammate_boxes()
    assert len(boxes) == 2 and boxes[0].seat == 1 and boxes[1].seat == 2
    assert boxes[0].imminent and not boxes[1].imminent
    assert boxes[0].c0 == 7 and boxes[1].c0 == 11  # three seats at step 4


async def test_dep_aside_and_own_cells_stay_clear():
    # A piece waiting on a claim keeps out of the columns that claim's
    # landing falls through: it steps aside to the nearest clear spot, and
    # stays put when it is clear already. The projection never stamps a
    # claim over the piece's own cells.
    g = coord_test_game()
    fill_row(g, 23, 5, 6, 7, 8)
    g.claims[1] = planned(1, 4, 0, 0, 5, 10)
    g.others_piece[1] = Active(0, 0, 2, 7)
    _, proj = g.build_projection()
    # We hold an I flat over the gap: our landing rests on theirs.
    g.piece = Active(0, 0, SPAWN_ROW, 5)
    g.deps = [DepWait(seat=1, piece=4, rev=10, cells=[x for x, s in proj.claimed.items() if s == 1])]
    cols = g.dep_columns()
    assert len(cols) == 4 and 5 in cols and 8 in cols
    aside, moved = g.dep_aside(g.piece)
    assert moved and not columns_meet(aside, g.dep_columns())
    assert aside.col in (1, 9), "the nearest clear spot"
    g.piece = Active(0, 0, SPAWN_ROW, 0)
    _, moved = g.dep_aside(g.piece)
    assert not moved, "moved a piece already clear of the columns"
    # Our own cells: a claim landing where we stand leaves the grid empty there.
    g.piece = Active(0, 0, 22, 5)  # sitting in the gap ourselves
    gr, proj = g.build_projection()
    for r, c in piece_cells(0, 0, 22, 5):
        assert gr.at(r, c) == 0, (r, c)
        assert (r, c) not in proj.claimed, (r, c)
