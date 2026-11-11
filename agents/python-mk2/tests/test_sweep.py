"""The shared board's housekeeping: the foreign cells' idle clocks, the
pieces a playing peer may vacate, and the deferred spawn's account."""

import time

from helpers import seat, sweep_test_game

from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW
from python_mk2.sweep import IDLE_PIECE_VACATE_AFTER
from python_mk2.wire import WireCell


async def test_peer_cell_clock_restarts_only_on_a_new_sequence():
    # A foreign cell's idle clock restarts when its owner rewrites it (a new
    # sequence) and only then; a cell that turns settled, empty or ours is
    # forgotten.
    g = sweep_test_game(2)
    at = (10, 8)
    wc = WireCell(t=4, a=True, r=1, ar=10, ac=8, pi=1)
    g.fold_cell(at, wc, 10)
    pc = g.peer_cells.get(at)
    assert pc is not None and pc.seq == 10 and pc.pi == 1 and pc.p == Active(4, 1, 10, 8)
    t0 = pc.at
    g.note_peer_cell(at, wc, 10, t0 + 1.0)
    assert g.peer_cells[at].at == t0, "the same sequence again restarted the clock"
    g.note_peer_cell(at, wc, 11, t0 + 2.0)
    pc = g.peer_cells[at]
    assert pc.seq == 11 and pc.at == t0 + 2.0
    g.fold_cell(at, WireCell(o=True, t=4, pi=1), 12)
    assert at not in g.peer_cells, "a settled cell kept its clock"
    g.fold_cell(at, wc, 13)
    g.fold_cell(at, WireCell(t=2, a=True, ar=10, ac=8), 14)  # ours (seat 0)
    assert at not in g.peer_cells, "a cell of ours kept a peer clock"
    g.fold_cell(at, wc, 15)
    g.fold_cell(at, WireCell(), 16)
    assert at not in g.peer_cells, "an emptied cell kept its clock"


async def test_stale_groups_by_piece_and_seat():
    # Stale groups are per piece, not per seat: a seat's stray copy runs out
    # of clock while its live piece, at another anchor, does not; a seat the
    # listing no longer holds is stale at once, whatever its clocks say.
    g = sweep_test_game(2)
    now = time.monotonic()
    stray = Active(4, 1, 10, 8)
    live = Active(2, 0, 2, 7)
    seq = 1
    for p in (stray, live):
        for c in p.cells():
            g.fold_cell(c, WireCell(t=p.pt, a=True, r=p.orient, ar=p.row, ac=p.col, pi=1), seq)
            seq += 1
    for pc in g.peer_cells.values():
        pc.at = now - IDLE_PIECE_VACATE_AFTER if pc.p == stray else now - 1.0
    st = g.stale_groups(now)
    assert len(st) == 1 and st[0].key == (1, stray) and len(st[0].cells) == 4
    # The seat gone from the roster: both of its pieces are stale.
    await g._on_roster([seat("me", 0)])
    assert len(g.stale_groups(now)) == 2
    # No roster at all: every seat is present.
    g.roster = []
    assert len(g.stale_groups(now)) == 1


async def test_spawn_blocked_account_follows_the_blockers():
    # The deferred spawn's account follows its blockers: the same cells at
    # the same sequences accumulate, a rewrite of any of them (the piece
    # moved) starts over, and a box with nothing foreign on it has no
    # account.
    g = sweep_test_game(2)
    cs = g.spawn_cells()
    blocker = Active(g.piece_at(0), 0, SPAWN_ROW, g.spawn_c)
    wc = WireCell(t=blocker.pt, a=True, r=blocker.orient, ar=blocker.row, ac=blocker.col, pi=1)
    for i, c in enumerate(cs):
        g.fold_cell(c, wc, i + 1)
    t0 = time.monotonic()
    assert not await g.note_spawn_blocked(cs, t0), "vacated at once"
    assert g.spawn_block.since == t0 and len(g.spawn_block.cells) == 4
    assert not await g.note_spawn_blocked(cs, t0 + 1.0) and g.spawn_block.since == t0
    g.fold_cell(cs[0], wc, 9)  # the piece was rewritten: a fresh account
    t2 = t0 + 2.0
    assert not await g.note_spawn_blocked(cs, t2) and g.spawn_block.since == t2
    for i, c in enumerate(cs):
        g.fold_cell(c, WireCell(), 20 + i)
    assert not await g.note_spawn_blocked(cs, t2) and len(g.spawn_block.cells) == 0
