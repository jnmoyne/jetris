"""The blackboard (guide §1.4): where the agents of one team tell each other
what they are about to do.

A KV bucket, JETRIS_BLACKBOARD (a JetStream stream with last-value-per-key, a
watch, a per-key TTL and one global revision order, the same pattern the
lobby is built on) holds one key per seat of a playfield,
<gameID>.<board>.<seat>, whose value is that seat's CLAIM: the piece it
holds, where it means to put it, and whether it has. A claim is written
straight to the key's subject with a per-message TTL (CLAIM_TTL), so an agent
that dies takes its claims with it; and it is read by the seat's teammates
alone (the crew of a cooperative game, or one team of a teams game): an agent
never reads another board's keys. Nothing goes on the blackboard that its
writer may not see (guide §1): the piece in play and an intention about it,
and tentative placements for the pieces its OWN NEXT well shows; never a
teammate's sequence, a seed or a sequence number.

The bucket is one ordered stream, so its revisions order every claim the same
for everyone: an agent posts its claim, waits for its own echo on its watch
(await_rev), and has then seen every claim made before its: the
linearization coord.py's conflict rules run on.
"""

import asyncio
import json
import time
from dataclasses import dataclass, replace

from nats.js import api

from . import aio, jsx
from .consts import MODE_COOPERATIVE, MODE_TEAMS
from .wire import dumps, log, now_rfc

CLAIM_TTL = 10.0              # per-message TTL on every claim: a dead agent's claims evaporate
BLACKBOARD_MARKER_TTL = 30.0  # enables the per-key TTL, and is how long an expired key's marker lingers
ECHO_TIMEOUT = 2.0            # post-then-look: a claim whose echo never comes is played uncoordinated
CLAIM_VERSION = 1
PHASE_PLANNED = "planned"
PHASE_LOCKED = "locked"
DROP_HARD = "hard"
DROP_SOFT = "soft"


# The "locked" posts in flight, each on its own three seconds.
_POSTS = set()


@dataclass
class Claim:
    """One seat's intention for the piece it holds, as the blackboard carries
    it. Cells are (row, col) in the claimant's frame at claim time: a
    teammate re-derives a planned claim's landing from its column and
    orientation on its own board (build_projection), so a collapse between
    the two frames costs nothing."""

    v: int = 0
    agent: str = ""
    seat: int = 0
    piece: int = 0
    type: int = 0
    rot: int = 0
    col: int = 0
    cells: tuple = ((0, 0), (0, 0), (0, 0), (0, 0))
    lines: int = 0
    phase: str = ""
    eta_ms: int = 0
    spawn_col: int = 0
    t: str = ""
    # Optional, and absent from a claim of an agent that does not say them
    # (guide §1.4): where the piece stood when the claim was written (row,
    # col, rot); how it goes down once at its column (DROP_HARD, the default,
    # or DROP_SOFT); the seats whose pieces are in the writer's way, while it
    # waits for them where it is, falling with gravity; the writer's
    # tentative placements for the pieces its own NEXT well shows; and the
    # column it is keeping open for a four-row clear.
    at: tuple = None
    drop: str = ""
    wait_on: tuple = ()
    next: tuple = ()
    well: int = None
    rev: int = 0  # the KV revision the watcher delivered it at (not on the wire)
    prio: int = 0  # its place in the right of way: the revision this seat FIRST claimed this placement of this piece at (fold_claim_raw)
    seen_at: float = 0.0  # when the watcher delivered it, on our clock (monotonic): what its eta_ms counts from

    def to_bytes(self):
        o = {
            "v": self.v, "agent": self.agent, "seat": self.seat, "piece": self.piece, "type": self.type,
            "rot": self.rot, "col": self.col, "cells": [[r, c] for r, c in self.cells], "lines": self.lines,
            "phase": self.phase, "eta_ms": self.eta_ms, "spawn_col": self.spawn_col, "t": self.t}
        if self.at is not None:
            o["at"] = list(self.at)
        if self.drop:
            o["drop"] = self.drop
        if self.wait_on:
            o["wait_on"] = list(self.wait_on)
        if self.next:
            o["next"] = [{"piece": n.piece, "type": n.type, "rot": n.rot, "col": n.col} for n in self.next]
        if self.well is not None:
            o["well"] = self.well
        return dumps(o)

    def same_placement(self, o):
        """Whether two claims are for the same placement of the same piece:
        a re-post of one claim (a wait announced, a preview refreshed), which
        keeps its place in the right of way."""
        return (self.phase == PHASE_PLANNED and o.phase == PHASE_PLANNED and self.piece == o.piece and
                self.type == o.type and self.rot == o.rot and self.col == o.col)

    def order(self):
        """The claim's place in the right of way: the revision its placement
        was first claimed at, which is its own revision unless it was said
        again since."""
        return self.prio or self.rev

    def waits_on(self, seat):
        """Whether the claim says its writer waits on the seat."""
        return seat in self.wait_on


@dataclass(frozen=True)
class NextClaim:
    """A tentative placement for a piece the writer's NEXT well shows: its
    piece index, type, orientation and anchor column."""

    piece: int = 0
    type: int = 0
    rot: int = 0
    col: int = 0


def parse_claim(data):
    """Decode a claim; None for anything that is not one."""
    try:
        o = json.loads(data)
    except (ValueError, UnicodeDecodeError):
        return None
    if not isinstance(o, dict):
        return None

    def num(k):
        v = o.get(k)
        if v is None:  # absent, or null: the zero value
            return 0
        if isinstance(v, bool) or not isinstance(v, int):
            raise TypeError(k)
        return v

    def text(k):
        v = o.get(k)
        if v is None:
            return ""
        if not isinstance(v, str):
            raise TypeError(k)
        return v

    try:
        cells = [(0, 0)] * 4
        raw = o.get("cells") or []
        if not isinstance(raw, list):
            raise TypeError("cells")
        for i, rc in enumerate(raw[:4]):
            r, c = rc[0], rc[1]
            if isinstance(r, bool) or isinstance(c, bool) or not isinstance(r, int) or not isinstance(c, int):
                raise TypeError("cells")
            cells[i] = (r, c)
        def ints(v, what):
            if not isinstance(v, list):
                raise TypeError(what)
            for x in v:
                if isinstance(x, bool) or not isinstance(x, int):
                    raise TypeError(what)
            return tuple(v)

        at = o.get("at")
        if at is not None:
            at = (ints(at, "at") + (0, 0, 0))[:3]  # row, col, rot
        wait_on = o.get("wait_on")
        wait_on = () if wait_on is None else ints(wait_on, "wait_on")
        nxt = []
        raw = o.get("next")
        if raw is not None:
            if not isinstance(raw, list):
                raise TypeError("next")
            for n in raw:
                if not isinstance(n, dict):
                    raise TypeError("next")
                vals = []
                for k in ("piece", "type", "rot", "col"):
                    v = n.get(k)
                    if v is None:
                        v = 0
                    if isinstance(v, bool) or not isinstance(v, int):
                        raise TypeError("next")
                    vals.append(v)
                nxt.append(NextClaim(*vals))
        well = o.get("well")
        if well is not None and (isinstance(well, bool) or not isinstance(well, int)):
            raise TypeError("well")
        return Claim(v=num("v"), agent=text("agent"), seat=num("seat"), piece=num("piece"), type=num("type"),
                     rot=num("rot"), col=num("col"), cells=tuple(cells), lines=num("lines"), phase=text("phase"),
                     eta_ms=num("eta_ms"), spawn_col=num("spawn_col"), t=text("t"), at=at, drop=text("drop"),
                     wait_on=wait_on, next=tuple(nxt), well=well)
    except (TypeError, IndexError, KeyError):
        return None


@dataclass
class Coordinator:
    """This game's place on the blackboard: the board we play on, our key,
    and the filter the watch follows."""

    board: str
    key: str
    watch: str


def board_name(mode, team, individual, seats_on_pf):
    """The playfield a seat coordinates on: "crew" on the crew's board,
    "t<N>" on a team's; "" where there is no team to coordinate with: a
    competitive board, a crew scored per seat, a playfield of one."""
    if seats_on_pf <= 1:
        return ""
    if mode == MODE_COOPERATIVE:
        return "" if individual else "crew"
    if mode == MODE_TEAMS:
        return f"t{team}"
    return ""


def claim_key(game_id, board, seat):
    """A seat's key on the blackboard. A game ID with a dot in it (none
    exists: names are letters, digits, - and _, and UUIDs use dashes) would
    break the key's tokens, so it gets no key (no coordination) rather than a
    broken one."""
    if not game_id or not board or "." in game_id:
        return ""
    return f"{game_id}.{board}.{seat}"


async def ensure_blackboard(js, bucket):
    """Create or update the blackboard bucket: memory storage (nothing on it
    outlives a piece), the marker TTL so every claim can carry its own.
    Idempotent, so it serves both connect and the watch's recovery of a
    bucket deleted underneath a running agent. A server too old for per-key
    TTLs raises: no blackboard."""
    return await jsx.ensure_kv(js, bucket, api.StorageType.MEMORY, BLACKBOARD_MARKER_TTL)


class BlackboardMixin:
    def coordinating(self):
        """Whether this game is played with the blackboard: a team to
        coordinate with, and a bucket to do it on."""
        return self.coord is not None

    def bb_log_once(self, err):
        if not self.bb_logged:
            self.bb_logged = True
            log.info("blackboard: %s (repeats not logged)", err)

    def claim_for(self, p, pl, phase, eta):
        """Our claim for the piece p at the placement pl, in the phase given,
        expected to land eta seconds from now."""
        c = Claim(v=CLAIM_VERSION, agent=self.a.name, seat=self.idx, piece=self.piece_idx, type=p.pt,
                  rot=pl.orient, col=pl.col, cells=tuple(pl.dest), lines=pl.lines, phase=phase,
                  eta_ms=int(eta * 1000 + 1e-6), spawn_col=self.spawn_c, t=now_rfc())
        if self.well_wanted():
            c.well = self.well_column()
        if phase == PHASE_PLANNED:
            c.at = (p.row, p.col, p.orient)
            c.drop = DROP_SOFT if self.a.soft_drop else DROP_HARD
        return c

    async def post_claim(self, c):
        """Write the claim to our key (a plain KV put carrying the
        per-message TTL, published straight to the key's subject) and return
        its revision; after every write still on its way (post_async): this
        claim is the newer word, and must be the last one on the key. A round
        trip: NEVER call with mu held."""
        if self.coord is None:
            raise RuntimeError("no blackboard")
        tail = self.post_tail
        if tail is not None and not tail.done():
            await asyncio.wait([tail])
        return await self.publish_claim(c)

    async def publish_claim(self, c):
        """The write itself (post_claim, post_async)."""
        return await jsx.kv_put_ttl(self.a.nc, self.a.bb_bucket, self.coord.key, c.to_bytes(), CLAIM_TTL)

    def post_locked_async(self, c):
        """Announce that the piece has locked where it did, without waiting
        for the ack: teammates stop projecting the claim and, until the
        lock's cells reach them off the game stream, project the cells it
        names. Caller holds mu."""
        self.post_async(c)

    def post_async(self, c):
        """Write a claim without waiting for its ack (the lock's
        announcement, a wait's) IN THE ORDER the calls were made: each write
        goes out once the one before it has (post_tail), and the next claim
        made with post_claim waits for them all. Out of order, a wait
        announced for a piece could land on the blackboard after that piece's
        lock, and stand there as a planned claim for a piece that is stack.
        Caller holds mu."""
        if self.coord is None:
            return
        prev = self.post_tail

        async def post():
            if prev is not None and not prev.done():
                await asyncio.wait([prev])
            try:
                await asyncio.wait_for(self.publish_claim(c), 3.0)
            except Exception as e:
                self.bb_log_once(e)

        # Not the game's tasks: the last lock's post outlives the game.
        self.post_tail = aio.spawn(_POSTS, post())

    def announce_wait(self, seats):
        """Say on the blackboard that the piece in play waits on the seats'
        pieces, where it is, falling with gravity (Claim.wait_on): the claim
        as it stands, said again (it keeps its place in the right of way), so
        that the seats named know someone is held up by them (pause: their
        think pause ends now) and everyone knows this piece is staying in its
        columns. Caller holds mu."""
        if self.coord is None or self.my_claim is None or not seats or self.piece is None:
            return
        p = self.piece
        self.post_async(replace(self.my_claim, wait_on=tuple(seats), at=(p.row, p.col, p.orient), t=now_rfc()))

    def waited_on(self, now):
        """Whether a teammate's claim says, as of the last couple of seconds,
        that it waits on our piece. Caller holds mu."""
        for seat, c in self.claims.items():
            if (seat != self.idx and c.phase == PHASE_PLANNED and c.waits_on(self.idx) and
                    now - c.seen_at < 2.0 and self.seat_present_locked(seat)):
                return True
        return False

    async def pause(self, d):
        """The think pause of a piece on a coordinated board: the
        difficulty's, unless a teammate says it waits on our piece. Then the
        piece goes as soon as its claim is confirmed (a move step), and the
        pause is cut short the moment such a claim arrives (fold_claim_raw
        sets hurry). "You go, I wait": the one in the way is the one that
        hurries. Returns False when the game or the agent stops."""
        move_delay = self.a.tn.move_delay
        if self.coord is None or d <= move_delay:
            return await self.wait(d)
        async with self.mu:
            if self.waited_on(time.monotonic()):
                self.st.hurries += 1
                hurry = None
            else:
                hurry = self.hurry = asyncio.Event()
        if hurry is None:
            return await self.wait(move_delay)
        which = await aio.wait_any([hurry, self.ended, self.a.stop_ev], d)
        async with self.mu:
            if self.hurry is hurry:
                self.hurry = None
            if which == 0:
                self.st.hurries += 1
        return which in (0, -1)

    async def await_rev(self, rev, timeout):
        """Wait until our watch has delivered the blackboard's revision rev
        (our own claim's echo, and with it every claim made before it) or the
        timeout, the game or the agent ends it. Returns (ok, timed_out).
        NEVER call with mu held: the watcher folds under it."""
        loop = asyncio.get_running_loop()
        deadline = loop.time() + timeout
        while True:
            async with self.mu:
                ok = self.bb_rev >= rev
                notify = self.bb_notify
            if ok:
                return True, False
            left = deadline - loop.time()
            if left <= 0:
                return False, True
            which = await aio.wait_any([notify, self.ended, self.a.stop_ev], left)
            if which in (1, 2):
                return False, False

    async def claim_watch(self):
        """Follow our board's keys on the blackboard for the life of the
        game, folding every claim under mu (fold_claim_raw)."""
        kv = self.a.blackboard()
        if self.coord is None or kv is None:
            return
        try:
            w = await jsx.KvWatch.start(kv, self.coord.watch)
        except Exception as e:
            self.bb_log_once(e)
            return
        try:
            async for e in w.updates():
                if e is None:
                    continue  # the initial values are in
                key, value, rev, op = e
                async with self.mu:
                    self.fold_claim_raw(key, value, rev, op)
        finally:
            await w.stop()

    def fold_claim_raw(self, key, value, rev, op):
        """Fold one delivery of the watch into the claims: a put is the
        seat's claim (a version we do not speak, or a piece type there is
        not, is none), a delete or a purge (a TTL expiry arrives as one) is
        the seat's claim gone. A claim takes its place in the right of way
        (prio) from the revision it was first made at: said again for the
        same placement of the same piece, it keeps it. Every delivery moves
        the revision mark and wakes await_rev. Caller holds mu."""
        try:
            seat = int(key[key.rfind(".") + 1:])
        except ValueError:
            seat = None
        if seat is not None:
            c = parse_claim(value) if op == jsx.KV_PUT else None
            if c is not None and c.v == CLAIM_VERSION and 0 <= c.type < 7:
                c.rev, c.prio, c.seen_at = rev, rev, time.monotonic()
                old = self.claims.get(seat)
                if old is not None and old.same_placement(c):
                    c.prio = old.order()  # the same claim, said again: its place in the order is kept
                self.claims[seat] = c
                if seat != self.idx and c.phase == PHASE_PLANNED and c.waits_on(self.idx) and self.hurry is not None:
                    self.hurry.set()  # a teammate waits on our piece: the think pause ends now (pause)
                    self.hurry = None
            else:
                self.claims.pop(seat, None)
        if rev > self.bb_rev:
            self.bb_rev = rev
        self.bb_notify.set()
        self.bb_notify = asyncio.Event()

    async def drop_claim(self):
        """Purge our key on the way out of a game (the TTL would take it
        within CLAIM_TTL anyway; the purge's own marker expires after
        BLACKBOARD_MARKER_TTL)."""
        if self.coord is None:
            return
        kv = self.a.blackboard()
        if kv is not None:
            try:
                await kv.purge(self.coord.key, msg_ttl=BLACKBOARD_MARKER_TTL)
            except Exception:
                pass

    def seat_about_to_spawn(self, seat):
        """Whether a teammate's claim says their piece has locked: their next
        spawn is imminent, so their spawn box is best kept clear
        (teammate_boxes)."""
        c = self.claims.get(seat)
        return c is not None and c.phase == PHASE_LOCKED
