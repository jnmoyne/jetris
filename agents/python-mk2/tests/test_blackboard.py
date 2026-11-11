"""The blackboard's claims: their wire format, who shares a board, the keys,
and what the watch's deliveries do to the claims held."""

import json

from helpers import sweep_test_game

from python_mk2 import jsx
from python_mk2.blackboard import CLAIM_VERSION, PHASE_PLANNED, Claim, board_name, claim_key, parse_claim
from python_mk2.consts import MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS
from python_mk2.path import Active
from python_mk2.pieces import SPAWN_ROW, piece_cells
from python_mk2.planner import Placement


async def test_claim_json():
    # The claim's wire format: the field names the guide documents,
    # round-tripped.
    g = sweep_test_game(2)
    g.piece_idx = 17
    p = Active(2, 0, SPAWN_ROW, g.spawn_c)
    pl = Placement(orient=1, col=5, drop_row=21, dest=piece_cells(2, 1, 21, 5), lines=1)
    c = g.claim_for(p, pl, PHASE_PLANNED, 0.120)
    b = c.to_bytes()
    fields = json.loads(b)
    for k in ("v", "agent", "seat", "piece", "type", "rot", "col", "cells", "lines", "phase", "eta_ms", "spawn_col",
              "t"):
        assert k in fields, f"claim JSON lacks {k!r}: {b}"
    assert len(fields) == 13
    assert parse_claim(b) == c
    assert (c.v == CLAIM_VERSION and c.seat == 0 and c.piece == 17 and c.type == 2 and c.rot == 1 and c.col == 5
            and c.eta_ms == 120 and c.spawn_col == g.spawn_c and c.lines == 1)
    assert c.agent == "me" and c.phase == PHASE_PLANNED
    assert fields["cells"] == [[r, col] for r, col in pl.dest]
    assert c.cells == ((21, 6), (22, 6), (22, 7), (23, 6))


def test_board_name():
    # Who coordinates with whom: the crew of a board scored together, one
    # team of a teams game; nobody on a competitive board, a board scored
    # per seat, or a playfield of one.
    cases = [
        (MODE_COOPERATIVE, 0, False, 2, "crew"),
        (MODE_COOPERATIVE, 0, True, 2, ""),
        (MODE_COOPERATIVE, 0, False, 1, ""),
        (MODE_TEAMS, 1, False, 2, "t1"),
        (MODE_TEAMS, 0, False, 1, ""),
        (MODE_COMPETITIVE, 0, False, 1, ""),
        (MODE_COMPETITIVE, 0, False, 4, ""),
    ]
    for mode, team, individual, seats, want in cases:
        assert board_name(mode, team, individual, seats) == want, (mode, team, individual, seats)


def test_claim_key():
    assert (claim_key("550e8400-e29b-41d4-a716-446655440000", "crew", 1)
            == "550e8400-e29b-41d4-a716-446655440000.crew.1")
    assert claim_key("friday-night", "t2", 5) == "friday-night.t2.5"
    assert claim_key("a.b", "crew", 0) == ""
    assert claim_key("x", "", 0) == ""


async def test_fold_claim_raw():
    # The watch's deliveries: a put is the seat's claim, a delete or a purge
    # (a TTL expiry) its end, a version we do not speak nothing; every
    # delivery moves the revision mark and wakes whoever waits on it.
    g = sweep_test_game(2)
    c = Claim(v=CLAIM_VERSION, seat=1, piece=3, type=4, rot=1, col=8, phase=PHASE_PLANNED)
    b = c.to_bytes()
    notify = g.bb_notify
    g.fold_claim_raw("g.crew.1", b, 7, jsx.KV_PUT)
    assert notify.is_set(), "a delivery did not wake the waiters"
    assert not g.bb_notify.is_set(), "the next wait has nothing to wait on"
    got = g.claims[1]
    assert got.rev == 7 and got.col == 8 and g.bb_rev == 7
    g.fold_claim_raw("g.crew.1", b"", 9, jsx.KV_DEL)
    assert 1 not in g.claims and g.bb_rev == 9
    g.fold_claim_raw("g.crew.1", b, 10, jsx.KV_PUT)
    g.fold_claim_raw("g.crew.1", b"", 11, jsx.KV_PURGE)
    assert 1 not in g.claims, "a purge (a TTL expiry) kept the claim"
    c2 = Claim(v=2, seat=1, piece=3, type=4, rot=1, col=8, phase=PHASE_PLANNED)
    g.fold_claim_raw("g.crew.1", c2.to_bytes(), 12, jsx.KV_PUT)
    assert 1 not in g.claims, "a claim of a version we do not speak was folded"
    g.fold_claim_raw("g.crew.1", b, 5, jsx.KV_PUT)
    assert g.bb_rev == 12, "the revision mark went backwards"


def test_parse_claim_reads_what_a_peer_may_write():
    # A field absent or null reads as its zero value; a field of the wrong
    # type, or anything that is not a JSON object, is no claim at all.
    c = parse_claim(b'{"v":1,"seat":1,"type":4,"col":8,"phase":"planned","agent":null,"lines":null,"cells":null}')
    assert c is not None and c.v == 1 and c.seat == 1 and c.col == 8 and c.agent == "" and c.lines == 0
    assert c.cells == ((0, 0), (0, 0), (0, 0), (0, 0)) and c.rev == 0
    assert parse_claim(b'{"v":1,"seat":"1"}') is None
    assert parse_claim(b'{"v":1.5}') is None
    assert parse_claim(b'{"v":true}') is None
    assert parse_claim(b'[1,2,3]') is None
    assert parse_claim(b'not json') is None
    assert parse_claim(b'') is None
