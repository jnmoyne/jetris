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
never a preview, a seed or a sequence number.

The bucket is one ordered stream, so its revisions order every claim the same
for everyone: an agent posts its claim, waits for its own echo on its watch
(await_rev), and has then seen every claim made before its: the
linearization coord.py's conflict rules run on.
"""

import asyncio
import json
from dataclasses import dataclass

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
    rev: int = 0  # the KV revision the watcher delivered it at (not on the wire)

    def to_bytes(self):
        return dumps({
            "v": self.v, "agent": self.agent, "seat": self.seat, "piece": self.piece, "type": self.type,
            "rot": self.rot, "col": self.col, "cells": [[r, c] for r, c in self.cells], "lines": self.lines,
            "phase": self.phase, "eta_ms": self.eta_ms, "spawn_col": self.spawn_col, "t": self.t})


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
        return Claim(v=num("v"), agent=text("agent"), seat=num("seat"), piece=num("piece"), type=num("type"),
                     rot=num("rot"), col=num("col"), cells=tuple(cells), lines=num("lines"), phase=text("phase"),
                     eta_ms=num("eta_ms"), spawn_col=num("spawn_col"), t=text("t"))
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
        return Claim(v=CLAIM_VERSION, agent=self.a.name, seat=self.idx, piece=self.piece_idx, type=p.pt,
                     rot=pl.orient, col=pl.col, cells=tuple(pl.dest), lines=pl.lines, phase=phase,
                     eta_ms=int(eta * 1000 + 1e-6), spawn_col=self.spawn_c, t=now_rfc())

    async def post_claim(self, c):
        """Write the claim to our key (a plain KV put carrying the
        per-message TTL, published straight to the key's subject) and return
        its revision. A round trip: NEVER call with mu held."""
        if self.coord is None:
            raise RuntimeError("no blackboard")
        return await jsx.kv_put_ttl(self.a.nc, self.a.bb_bucket, self.coord.key, c.to_bytes(), CLAIM_TTL)

    def post_locked_async(self, c):
        """Announce that the piece has locked where it did, without waiting
        for the ack: teammates stop projecting the claim and, until the
        lock's cells reach them off the game stream, project the cells it
        names."""
        if self.coord is None:
            return

        async def post():
            try:
                await asyncio.wait_for(self.post_claim(c), 3.0)
            except Exception as e:
                self.bb_log_once(e)

        aio.spawn(_POSTS, post())  # not the game's tasks: the last lock's post outlives the game

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
        game, folding every claim under mu (fold_claim_raw). A watch that
        ends (the bucket deleted underneath us, as a shared server's purge
        does) is re-established, the bucket recreated if need be, and since a
        new bucket's revisions start over, the claims and the revision mark
        are dropped and the piece in play re-planned (REPLAN_BB_RESET)."""
        from .coord import REPLAN_BB_RESET

        if self.coord is None:
            return
        a = self.a
        backoff = 0.5
        established = False
        while not a.done() and not self.is_ended():
            kv = a.blackboard()
            if kv is None:
                try:
                    kv = await a.ensure_blackboard()
                except Exception as e:
                    self.bb_log_once(e)
                    if not await self.wait(backoff):
                        return
                    continue
                a.set_blackboard(kv)
            try:
                w = await jsx.KvWatch.start(a.js, kv, a.bb_bucket, self.coord.watch)
            except Exception:
                if not a.done():
                    # The bucket is gone (or the server is): recreate it on
                    # the way back in, through a fresh handle.
                    try:
                        a.set_blackboard(await a.ensure_blackboard())
                    except Exception:
                        a.set_blackboard(None)
                if not await self.wait(backoff):
                    return
                continue
            async with self.mu:
                if established:
                    self.bb_epoch += 1
                    self.bb_rev, self.my_rev = 0, 0
                    self.claims = {}
                    if self.piece is not None:
                        self.request_replan(REPLAN_BB_RESET)
                    log.info("blackboard watch re-established (epoch %d)", self.bb_epoch)
                established = True
            try:
                async for e in w.updates():
                    if e is None:
                        continue  # the initial values are in
                    key, value, rev, op = e
                    async with self.mu:
                        self.fold_claim_raw(key, value, rev, op)
            finally:
                await w.stop()
            if a.done() or self.is_ended():
                return
            # The watch ended on its own: the bucket was deleted underneath
            # it. Make sure it is there, then watch again.
            try:
                a.set_blackboard(await a.ensure_blackboard())
            except Exception:
                pass
            if not await self.wait(backoff):
                return

    def fold_claim_raw(self, key, value, rev, op):
        """Fold one delivery of the watch into the claims: a put is the
        seat's claim (a version we do not speak is none), a delete or a purge
        (a TTL expiry arrives as one) is the seat's claim gone. Every
        delivery moves the revision mark and wakes await_rev. Caller holds
        mu."""
        try:
            seat = int(key[key.rfind(".") + 1:])
        except ValueError:
            seat = None
        if seat is not None:
            c = parse_claim(value) if op == jsx.KV_PUT else None
            if c is not None and c.v == CLAIM_VERSION:
                c.rev = rev
                self.claims[seat] = c
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
