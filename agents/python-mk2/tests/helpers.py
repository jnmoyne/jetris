"""Games without a server, for the pure tests: a Game around a stub agent,
set up the way golang-mk2's test helpers set theirs up."""

import asyncio
import random

from python_mk2 import consts
from python_mk2.blackboard import CLAIM_VERSION, PHASE_PLANNED, Claim, Coordinator
from python_mk2.difficulty import difficulty_tuning
from python_mk2.game import Game
from python_mk2.rules import MIN_EXTRA_COLUMNS
from python_mk2.shared import shared_height, shared_width
from python_mk2.wire import WireCell, dumps, parse_event, player_summary


class StubAgent:
    """What a Game reads of its agent, with nothing behind it."""

    def __init__(self, name="me"):
        self.name = name
        self.pub = consts.PUB_AUTO
        self.tn = difficulty_tuning("hard")
        self.rng = random.Random(1)
        self.trace = False
        self.soft_drop = False
        self.coordinate = True
        self.wait = 600.0
        self.stop_ev = asyncio.Event()
        self.tasks = set()
        self.listings = {}
        self.nc = self.js = self.kv = None
        self.bb_bucket = "JETRIS_BLACKBOARD"
        self.bb = None

    def stop(self):
        self.stop_ev.set()

    def stopping(self):
        return self.stop_ev.is_set()

    def done(self):
        return self.stop_ev.is_set()

    def blackboard(self):
        return self.bb

    def set_blackboard(self, kv):
        self.bb = kv


def make_game(name="me", idx=0, **attrs):
    """A Game with no server behind it. The mode defaults to cooperative
    (the zero value of the Go struct the tests this mirrors build); every
    keyword sets the attribute of that name."""
    g = Game(StubAgent(name), "g", idx)
    g.mode = consts.MODE_COOPERATIVE
    for k, v in attrs.items():
        setattr(g, k, v)
    return g


def sweep_test_game(seats):
    """A crew's board of `seats` seats with no server behind it."""
    g = make_game()
    g.player_count, g.seats_on_pf, g.extra = seats, seats, MIN_EXTRA_COLUMNS
    g.w, g.h = shared_width(seats, MIN_EXTRA_COLUMNS), shared_height(seats, 0)
    g.spawn_c = g.spawn_column([])
    return g


def path_test_game():
    """A crew's board of two seats (14 columns) with no server."""
    g = sweep_test_game(2)
    g.roster = [seat("me", 0), seat("peer", 1)]
    g.spawn_c = g.spawn_column(g.present_slots(g.roster))
    return g


def coord_test_game():
    """A crew's board of two seats, coordinated, with no server."""
    g = path_test_game()
    g.coord = Coordinator(board="crew", key="g.crew.0", watch="g.crew.*")
    return g


def seat(player_id, seat_no=0, team=0, team_slot=0):
    """A roster entry."""
    return player_summary(player_id, "", seat=seat_no, team=team, team_slot=team_slot)


def event(**fields):
    """A game event as the consumers hand it on: every field present."""
    return parse_event(dumps(fields))


def fill_row(g, r, *except_cols):
    for c in range(g.w):
        if c not in except_cols:
            g.locked[(r, c)] = WireCell(o=True, t=1)


def lock_column(g, col, from_row):
    for r in range(from_row, g.height()):
        g.locked[(r, col)] = WireCell(o=True, t=1)


def planned(seat_no, piece, typ, rot, col, rev):
    return Claim(v=CLAIM_VERSION, seat=seat_no, piece=piece, type=typ, rot=rot, col=col, phase=PHASE_PLANNED,
                 eta_ms=150, rev=rev)
