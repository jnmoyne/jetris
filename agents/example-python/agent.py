#!/usr/bin/env python3
"""A minimal Jetris-playing agent in Python.

This agent depends on NOTHING in the jetris repository: it implements the
wire protocol described in ../../jetris-agent-guide.md (with the game rules
from ../../jetris-gameplays.md) directly against NATS/JetStream, as a worked
example that the "any language, only NATS" contract is real.

Scope (deliberately minimal):
  - Plays COMPETITIVE mode only. It accepts invitations to competitive games
    (declining invitations to modes it cannot play); with --auto-join it also
    joins open competitive games that allow agents.
  - Strategy is a naive one-ply greedy: enumerate reachable placements
    (plain in-place rotations + sideways translations + hard drop), score
    each resulting board (lines, holes, height, bumpiness), take the best.
    It never uses SRS wall kicks: a kick-free rotation is the first SRS kick
    offset, so its move repertoire is a legal subset of the game's.
  - Fair visibility: decisions use only its own committed board. It never
    reads the meta seed beyond generating its OWN piece sequence (which the
    protocol requires every peer to do). Games may reveal a piece preview
    (meta next_count, 0-4) that an agent MAY plan with — this one keeps its
    strategy one-ply and simply ignores its allowance, which is always legal.

Everything it carries as a peer, per the guide:
  presence heartbeat - join via KV CAS - roster announcement - ready toggle -
  countdown election - its own engine (spawn, gravity, lock-in, line clears,
  garbage ledger CAS-adds + gated application, top-out) - per-player game_over
  events - CAS batch writes with write-through - CAS-failure flashes - finish +
  archive when it wins - clean teardown.

Usage:
  python agent.py --server nats://localhost:4222            # resident: waits for invitations
  python agent.py --auto-join                               # also join open agent-allowed games
  python agent.py --server nats://... --join <gameID>       # join one specific game
  python agent.py --once                                    # play a single game, then exit
  python agent.py --selftest                                # offline conformance checks
"""

import argparse
import asyncio
import json
import random
import re
import secrets
import signal
import sys
import time
from datetime import datetime, timezone

import nats
from nats.js import api as jsapi

# --------------------------------------------------------------------------
# Identity (jetris-agent-guide.md §2): <codename>-<instance>-<difficulty>.
# Bump CODENAME whenever this agent's play logic changes.
CODENAME = "example-py"
DIFFICULTY = "easy"  # one fixed strength; the label is part of the name

# Protocol constants (guide §4, gameplays §2/§7).
LOBBY_BUCKET = "JETRIS_LOBBY"
ARCHIVE_STREAM = "JETRIS_ARCHIVE"
ARCHIVE_SUBJECT = "jetris.archive"
CHAT_STREAM = "JETRIS_CHAT"
WIDTH = 10                    # competitive boards are always 10 wide
HEADROOM = 4                  # rows 0..3 are the hidden spawn headroom
GRAVITY_SECONDS = 1.0         # competitive gravity is fixed at level 0 (1000 ms, gameplays §7)
PRESENCE_SECONDS = 5.0        # presence heartbeat interval
INVITE_TTL = 120.0            # invitations are stale after 2 minutes
MODE_COMPETITIVE = 1          # GameMeta/GameListing "mode" (integer on the wire)

# Batch-publish headers (guide §4.3; NATS atomic batch publish).
H_BATCH_ID = "Nats-Batch-Id"
H_BATCH_SEQ = "Nats-Batch-Sequence"
H_BATCH_COMMIT = "Nats-Batch-Commit"
H_EXPECT_LAST = "Nats-Expected-Last-Subject-Sequence"
CAS_ERR_CODES = (10071, 10164)  # wrong last seq for subject (plain / batch-constant variant)

# Tetromino cell offsets [piece][orientation] -> 4 (rowOff, colOff) from the
# anchor; orientation 0 is the spawn orientation. Pieces: I O T S Z J L = 0..6.
PIECES = [
    [[(1, 0), (1, 1), (1, 2), (1, 3)], [(0, 2), (1, 2), (2, 2), (3, 2)],
     [(2, 0), (2, 1), (2, 2), (2, 3)], [(0, 1), (1, 1), (2, 1), (3, 1)]],  # I
    [[(0, 0), (0, 1), (1, 0), (1, 1)]] * 4,                                # O
    [[(0, 1), (1, 0), (1, 1), (1, 2)], [(0, 1), (1, 1), (1, 2), (2, 1)],
     [(1, 0), (1, 1), (1, 2), (2, 1)], [(0, 1), (1, 0), (1, 1), (2, 1)]],  # T
    [[(0, 1), (0, 2), (1, 0), (1, 1)], [(0, 1), (1, 1), (1, 2), (2, 2)],
     [(1, 1), (1, 2), (2, 0), (2, 1)], [(0, 0), (1, 0), (1, 1), (2, 1)]],  # S
    [[(0, 0), (0, 1), (1, 1), (1, 2)], [(0, 2), (1, 1), (1, 2), (2, 1)],
     [(1, 0), (1, 1), (2, 1), (2, 2)], [(0, 1), (1, 0), (1, 1), (2, 0)]],  # Z
    [[(0, 0), (1, 0), (1, 1), (1, 2)], [(0, 1), (0, 2), (1, 1), (2, 1)],
     [(1, 0), (1, 1), (1, 2), (2, 2)], [(0, 1), (1, 1), (2, 0), (2, 1)]],  # J
    [[(0, 2), (1, 0), (1, 1), (1, 2)], [(0, 1), (1, 1), (2, 1), (2, 2)],
     [(1, 0), (1, 1), (1, 2), (2, 0)], [(0, 0), (0, 1), (1, 1), (2, 1)]],  # L
]


def log(msg):
    print(f"{datetime.now().strftime('%H:%M:%S')} {msg}", flush=True)


def now_rfc3339():
    return datetime.now(timezone.utc).isoformat()


def parse_rfc3339(s):
    """Parse a Go RFC3339Nano timestamp (fractional part may be 9 digits)."""
    s = re.sub(r"(\.\d{6})\d+", r"\1", s)  # trim to microseconds for Python
    return datetime.fromisoformat(s)


# --------------------------------------------------------------------------
# The piece RNG: every peer derives the same deterministic 7-bag sequence from
# GameMeta.Seed. This reproduces Go's math/rand/v2 PCG + Fisher-Yates shuffle
# bit for bit (the sequence is seekable: bag k uses PCG(seed, k)).

M64 = (1 << 64) - 1


class _PCG:
    def __init__(self, seed1, seed2):
        self.hi = seed1 & M64
        self.lo = seed2 & M64

    def uint64(self):
        mul_hi, mul_lo = 2549297995355413924, 4865540595714422341
        inc_hi, inc_lo = 6364136223846793005, 1442695040888963407
        prod = self.lo * mul_lo
        hi, lo = (prod >> 64) & M64, prod & M64
        hi = (hi + self.hi * mul_lo + self.lo * mul_hi) & M64
        s = lo + inc_lo
        lo = s & M64
        hi = (hi + inc_hi + (s >> 64)) & M64
        self.lo, self.hi = lo, hi
        out = hi
        out ^= out >> 32
        out = (out * 0xDA942042E4DD58B5) & M64
        out ^= out >> 48
        out = (out * (lo | 1)) & M64
        return out


def _uint64n(pcg, n):
    if n & (n - 1) == 0:
        return pcg.uint64() & (n - 1)
    prod = pcg.uint64() * n
    hi, lo = prod >> 64, prod & M64
    if lo < n:
        thresh = ((1 << 64) - n) % n
        while lo < thresh:
            prod = pcg.uint64() * n
            hi, lo = prod >> 64, prod & M64
    return hi


def piece_at(seed, index):
    """Piece type (0..6) at the given global sequence index."""
    bag, pos = index // 7, index % 7
    pcg = _PCG(seed, bag)
    pieces = [0, 1, 2, 3, 4, 5, 6]
    for i in range(6, 0, -1):
        j = _uint64n(pcg, i + 1)
        pieces[i], pieces[j] = pieces[j], pieces[i]
    return pieces[pos]


# --------------------------------------------------------------------------
# Board model. `locked` holds settled cells only (dict of (r,c) -> cell dict);
# the single active piece is tracked separately as (type, orient, row, col).
# `seqs` tracks the last known stream sequence per cell subject — the CAS
# expectation for the next write to that cell (0 = never written).

def piece_cells(pt, orient, row, col):
    return [(row + dr, col + dc) for dr, dc in PIECES[pt][orient % 4]]


def can_place(locked, height, cells):
    for r, c in cells:
        if r < 0 or r >= height or c < 0 or c >= WIDTH:
            return False
        if (r, c) in locked:
            return False
    return True


def drop_row(locked, height, pt, orient, row, col):
    while can_place(locked, height, piece_cells(pt, orient, row + 1, col)):
        row += 1
    return row


def completed_rows(locked, height):
    """Full rows that can clear: every cell settled and at least one of them a
    player's. A solid garbage row is permanent; a garbage row raised with holes
    clears like any other line once the holes are filled (gameplays §4)."""
    out = []
    for r in range(height):
        cells = [locked.get((r, c)) for c in range(WIDTH)]
        if all(c is not None for c in cells) and any(not c.get("g") for c in cells):
            out.append(r)
    return out


def evaluate(locked, height):
    """Classic greedy features: fewer holes, lower stack, flatter surface."""
    heights, holes = [], 0
    for c in range(WIDTH):
        top = None
        for r in range(height):
            if (r, c) in locked:
                top = r
                break
        heights.append(0 if top is None else height - top)
        if top is not None:
            holes += sum(1 for r in range(top + 1, height) if (r, c) not in locked)
    bump = sum(abs(heights[i] - heights[i + 1]) for i in range(WIDTH - 1))
    return -10.0 * holes - 1.0 * sum(heights) - 2.0 * bump


def plan_placement(locked, height, pt, spawn_row, spawn_col):
    """Enumerate (orientation, column) placements reachable with kick-free
    rotations at spawn followed by sideways translations, score the board each
    would leave behind, and return the best as (orient, col)."""
    orients = []
    o = 0
    while True:  # orientations reachable by successive in-place rotations
        if o not in orients and can_place(locked, height, piece_cells(pt, o, spawn_row, spawn_col)):
            orients.append(o)
        o += 1
        if o > 3 or not can_place(locked, height, piece_cells(pt, o % 4, spawn_row, spawn_col)):
            break
    best, best_score = None, None
    for o in orients:
        for direction in (-1, 1):
            col = spawn_col
            while can_place(locked, height, piece_cells(pt, o, spawn_row, col)):
                r = drop_row(locked, height, pt, o, spawn_row, col)
                after = dict(locked)
                for cell in piece_cells(pt, o, r, col):
                    after[cell] = {"o": True}
                lines = len(completed_rows(after, height))
                score = 200.0 * lines + evaluate(after, height)
                if best_score is None or score > best_score:
                    best, best_score = (o, col), score
                col += direction
                if direction == 1 and col == spawn_col:
                    break  # spawn_col was covered by the leftward sweep
    return best


# --------------------------------------------------------------------------
# The agent.

class CASFailure(Exception):
    pass


class GameOver(Exception):
    """Raised inside the play loop when this player tops out."""


class Agent:
    def __init__(self, server, stem, join_id, once, auto_join):
        self.server = server
        self.name = f"{stem}-{secrets.token_hex(2)}-{DIFFICULTY}"
        if len(self.name) > 32:
            sys.exit(f"agent name {self.name!r} exceeds 32 characters")
        self.join_id = join_id
        self.once = once
        self.auto_join = auto_join
        self.nc = self.js = self.kv = None
        self.listings = {}      # gameID -> listing dict (from the KV watcher)
        self.invites = {}       # gameID -> invitation dict (one KV key per game)
        self.presence_status = 0
        self.current_game = ""
        self.stopping = False

    # ---- lobby plumbing --------------------------------------------------

    async def connect(self):
        self.nc = await nats.connect(self.server, name=self.name)
        self.js = self.nc.jetstream()
        try:
            self.kv = await self.js.key_value(LOBBY_BUCKET)
        except Exception:
            self.kv = await self.js.create_key_value(
                config=jsapi.KeyValueConfig(bucket=LOBBY_BUCKET, storage=jsapi.StorageType.FILE))
        try:  # the archive stream normally exists; ensure it like Bootstrap does
            await self.js.stream_info(ARCHIVE_STREAM)
        except Exception:
            await self.js.add_stream(
                config=jsapi.StreamConfig(name=ARCHIVE_STREAM, subjects=[ARCHIVE_SUBJECT],
                                          storage=jsapi.StorageType.FILE))

    async def publish_presence(self):
        p = {"player_id": self.name, "name": self.name, "status": self.presence_status,
             "agent": True, "last_seen": now_rfc3339()}
        if self.current_game:
            p["game_id"] = self.current_game
        await self.kv.put(f"players.{self.name}", json.dumps(p).encode())

    async def presence_loop(self):
        while not self.stopping:
            try:
                await self.publish_presence()
            except Exception as e:
                log(f"presence: {e}")
            await asyncio.sleep(PRESENCE_SECONDS)

    async def lobby_watch_loop(self):
        """Mirror the lobby KV into memory: game listings + our invitation."""
        watcher = await self.kv.watchall()
        async for entry in watcher:
            if entry is None:  # end-of-initial-data marker
                continue
            key, deleted = entry.key, entry.operation in ("DEL", "PURGE")
            if key.startswith("games."):
                gid = key[len("games."):]
                if deleted:
                    self.listings.pop(gid, None)
                else:
                    try:
                        self.listings[gid] = json.loads(entry.value)
                    except Exception:
                        pass
            elif key.startswith(f"invites.{self.name}."):
                gid = key[len(f"invites.{self.name}."):]
                if deleted:
                    self.invites.pop(gid, None)
                else:
                    try:
                        self.invites[gid] = json.loads(entry.value)
                    except Exception:
                        pass

    def fresh_invites(self):
        """Pending (fresh, not declined) invitations, oldest first. A player
        may hold one invitation per game, each under its own KV key."""
        out = []
        for gid, inv in self.invites.items():
            if inv.get("declined"):
                continue
            try:
                age = datetime.now(timezone.utc) - parse_rfc3339(inv["created_at"])
                if age.total_seconds() > INVITE_TTL:
                    continue
            except Exception:
                continue
            out.append((inv.get("created_at", ""), gid, inv))
        return [(gid, inv) for _, gid, inv in sorted(out)]

    async def consume_invite(self, game_id):
        """Accepting (joining) or dropping a stale invitation deletes its key —
        the deletion is what tells the inviter it was handled."""
        self.invites.pop(game_id, None)
        try:
            await self.kv.delete(f"invites.{self.name}.{game_id}")
        except Exception:
            pass

    async def decline_invite(self, game_id):
        """Declining REWRITES the key with declined=true (instead of deleting
        it) so the inviter sees the refusal until they dismiss it."""
        inv = self.invites.pop(game_id, None)
        if not inv:
            return
        inv["declined"] = True
        try:
            await self.kv.put(f"invites.{self.name}.{game_id}",
                              json.dumps(inv).encode())
        except Exception:
            pass

    def joinable(self, g):
        """An open competitive game with a free seat this agent may take."""
        agents = sum(1 for p in (g.get("players") or []) if p.get("agent"))
        return (g.get("mode") == MODE_COMPETITIVE
                and g.get("status") == "created"
                and not g.get("invite_only")
                and len(g.get("players") or []) < g.get("player_count", 0)
                and g.get("max_agents", 0) > agents)

    async def select_game(self):
        """Pick the next game: an explicit --join, a fresh invitation, or the
        first joinable open game. Returns (gameID, invited)."""
        if self.join_id:
            gid, self.join_id = self.join_id, None  # once
            return gid, False
        while not self.stopping:
            handled = False
            for gid, inv in self.fresh_invites():
                if inv.get("mode") == MODE_COMPETITIVE and gid in self.listings:
                    return gid, True
                if gid not in self.listings:  # game gone: drop the stale invite
                    await self.consume_invite(gid)
                else:
                    log(f"declining invitation from {inv.get('from_name')} (can't play that game)")
                    await self.decline_invite(gid)
                handled = True
            if handled:
                continue
            if self.auto_join:
                for gid, g in sorted(self.listings.items()):
                    if self.joinable(g):
                        return gid, False
            await asyncio.sleep(1.0)
        return None, False

    async def join_game(self, game_id, invited):
        """The guide §5.2 join: CAS-append ourselves to the KV listing, then
        publish our roster announcement to the game stream. Returns our
        player index, or None if the game can't be joined."""
        while True:
            try:
                entry = await self.kv.get(f"games.{game_id}")
            except Exception:
                return None
            g = json.loads(entry.value)
            players = g.get("players") or []
            for i, p in enumerate(players):
                if p["player_id"] == self.name:
                    return i  # already joined
            if g.get("invite_only") and not invited and g.get("creator_id") != self.name:
                return None
            if not invited:  # an invitation IS the permission (bypasses the policy)
                agents = sum(1 for p in players if p.get("agent"))
                if g.get("max_agents", 0) <= 0 or agents >= g["max_agents"]:
                    return None
            if len(players) >= g["player_count"]:
                return None
            summary = {"player_id": self.name, "name": self.name, "ready": False,
                       "team": 0, "team_slot": 0, "agent": True}
            players.append(summary)
            g["players"] = players
            full = len(players) >= g["player_count"]
            if full:
                g["status"] = "starting"
            try:
                await self.kv.update(f"games.{game_id}", json.dumps(g).encode(),
                                     last=entry.revision)
            except Exception:
                await asyncio.sleep(0.05)
                continue  # CAS conflict: retry from a fresh read
            await self.js.publish(f"jetris.game.{game_id}.roster.{self.name}",
                                  json.dumps(summary).encode())
            if full:
                await self.transition_meta(game_id, "starting")
            self.presence_status, self.current_game = 1, game_id
            await self.publish_presence()
            if invited:
                await self.consume_invite(game_id)
            return len(players) - 1

    async def toggle_ready(self, game_id):
        """CAS our ready flag; if our toggle completes the set (all seats
        filled, everyone ready) WE are elected to run the countdown."""
        while True:
            entry = await self.kv.get(f"games.{game_id}")
            g = json.loads(entry.value)
            if g.get("status") == "in_progress":
                return False
            players = g.get("players") or []
            for p in players:
                if p["player_id"] == self.name:
                    p["ready"] = True
            all_ready = all(p.get("ready") for p in players)
            try:
                await self.kv.update(f"games.{game_id}", json.dumps(g).encode(),
                                     last=entry.revision)
            except Exception:
                await asyncio.sleep(0.05)
                continue
            return all_ready and len(players) >= g.get("player_count", 0)

    async def fetch_meta(self, game_id):
        raw = await self.js.get_last_msg(f"JETRIS_GAME_{game_id}",
                                         f"jetris.game.{game_id}.meta", direct=True)
        return json.loads(raw.data), raw.seq

    async def transition_meta(self, game_id, status):
        """CAS the meta to a new status; never regress a completed game."""
        for _ in range(5):
            try:
                meta, seq = await self.fetch_meta(game_id)
            except Exception:
                return False
            if meta["status"] in ("finished", "archived", "cancelled") and \
               status not in ("archived",):
                return False
            if meta["status"] == status:
                return True
            meta["status"] = status
            if status == "in_progress":
                meta["started_at"] = now_rfc3339()
            if status == "finished":
                meta["finished_at"] = now_rfc3339()
            try:
                await self.js.publish(f"jetris.game.{game_id}.meta",
                                      json.dumps(meta).encode(),
                                      headers={H_EXPECT_LAST: str(seq)})
                return True
            except Exception:
                await asyncio.sleep(0.05)
        return False

    async def run_countdown(self, game_id):
        subject = f"jetris.game.{game_id}.countdown"
        for i in range(5, 0, -1):
            await self.js.publish(subject, json.dumps({"seconds": i}).encode())
            await asyncio.sleep(1.0)
        await self.js.publish(subject, json.dumps({"seconds": 0}).encode())
        await asyncio.sleep(0.7)
        await self.transition_meta(game_id, "in_progress")

    # ---- the wire: atomic CAS batches to our own cell subjects -----------

    async def publish_batch(self, game, cells, cas):
        """Publish cell updates as ONE atomic batch (guide §4.3). `cells` is a
        list of ((row, col), cell-dict-or-None); None vacates (publishes {}).
        Cells are ordered active → locked → empty so a relocating piece never
        transiently vanishes on observers' ordered consumers. With cas=True
        each message carries the per-subject expected-last-sequence; a mismatch
        drops the whole batch (CASFailure). On success the per-cell sequences
        advance by write-through: batch messages get consecutive stream
        sequences ending at the commit ack's."""
        def category(cell):
            if cell and cell.get("a"):
                return 0
            if cell and cell.get("o"):
                return 1
            return 2
        ordered = sorted(cells, key=lambda e: (category(e[1]), e[0][0], e[0][1]))
        n = len(ordered)
        if n == 1:  # a batch of one is just a plain (optionally CAS) publish
            (r, c), cell = ordered[0]
            payload = json.dumps({k: v for k, v in (cell or {}).items() if v}).encode()
            headers = {H_EXPECT_LAST: str(game.seqs.get((r, c), 0))} if cas else None
            resp = await self.nc.request(game.cell_subject(r, c), payload,
                                         timeout=5, headers=headers)
            body = json.loads(resp.data) if resp.data else {}
            if body.get("error"):
                if body["error"].get("err_code") in CAS_ERR_CODES:
                    raise CASFailure(body["error"].get("description", "cas"))
                raise RuntimeError(f"publish: {body['error']}")
            game.seqs[(r, c)] = body["seq"]
            return
        batch_id = secrets.token_hex(11)
        for i, ((r, c), cell) in enumerate(ordered):
            subject = game.cell_subject(r, c)
            payload = json.dumps({k: v for k, v in (cell or {}).items() if v}).encode()
            headers = {H_BATCH_ID: batch_id, H_BATCH_SEQ: str(i + 1)}
            if cas:
                headers[H_EXPECT_LAST] = str(game.seqs.get((r, c), 0))
            last = i == n - 1
            if last:
                headers[H_BATCH_COMMIT] = "1"
            if i == 0 or last:  # first (flow control) and commit are requests
                resp = await self.nc.request(subject, payload, timeout=5, headers=headers)
                body = json.loads(resp.data) if resp.data else {}
                if body.get("error"):
                    if body["error"].get("err_code") in CAS_ERR_CODES:
                        raise CASFailure(body["error"].get("description", "cas"))
                    raise RuntimeError(f"batch publish: {body['error']}")
                if last:
                    commit_seq = body["seq"]
            else:
                await self.nc.publish(subject, payload, headers=headers)
        for i, ((r, c), _cell) in enumerate(ordered):  # write-through
            game.seqs[(r, c)] = commit_seq - (n - 1 - i)

    async def publish_gated_batch(self, game, txn, cells):
        """Publish one BULK TRANSFORM (garbage application or line-clear
        collapse) as a single gated atomic batch: message 1 is the board's txn
        register carrying a per-subject CAS expectation — the exactly-once
        gate — and every cell after it is NoCAS (the transform overrides
        in-flight moves; a stale gate atomically rejects the WHOLE batch and
        nothing is stored). Cells keep the active → locked → empty order. On
        success the txn/cell sequences advance by write-through."""
        def category(cell):
            if cell and cell.get("a"):
                return 0
            if cell and cell.get("o"):
                return 1
            return 2
        ordered = sorted(cells, key=lambda e: (category(e[1]), e[0][0], e[0][1]))
        n = len(ordered) + 1  # + the txn register message
        batch_id = secrets.token_hex(11)
        txn_payload = json.dumps({k: v for k, v in txn.items() if v or k == "applied"}).encode()
        headers = {H_BATCH_ID: batch_id, H_BATCH_SEQ: "1",
                   H_EXPECT_LAST: str(game.txn_seq)}
        if n == 1:
            headers[H_BATCH_COMMIT] = "1"
        resp = await self.nc.request(game.txn_subject(), txn_payload, timeout=5, headers=headers)
        body = json.loads(resp.data) if resp.data else {}
        if body.get("error"):
            if body["error"].get("err_code") in CAS_ERR_CODES:
                raise CASFailure(body["error"].get("description", "cas"))
            raise RuntimeError(f"gated batch: {body['error']}")
        commit_seq = body.get("seq", 0)
        for i, ((r, c), cell) in enumerate(ordered):
            payload = json.dumps({k: v for k, v in (cell or {}).items() if v}).encode()
            headers = {H_BATCH_ID: batch_id, H_BATCH_SEQ: str(i + 2)}
            if i == len(ordered) - 1:
                headers[H_BATCH_COMMIT] = "1"
                resp = await self.nc.request(game.cell_subject(r, c), payload,
                                             timeout=5, headers=headers)
                body = json.loads(resp.data) if resp.data else {}
                if body.get("error"):
                    if body["error"].get("err_code") in CAS_ERR_CODES:
                        raise CASFailure(body["error"].get("description", "cas"))
                    raise RuntimeError(f"gated batch: {body['error']}")
                commit_seq = body["seq"]
            else:
                await self.nc.publish(game.cell_subject(r, c), payload, headers=headers)
        # Write-through: the txn is message 1 of n, cells follow.
        game.txn_seq = commit_seq - (n - 1)
        game.txn_applied = txn["applied"]
        for i, ((r, c), _cell) in enumerate(ordered):
            game.seqs[(r, c)] = commit_seq - (n - 1 - (i + 1))

    # ---- one game --------------------------------------------------------

    async def play(self, game_id, player_idx):
        game = Game(self, game_id, player_idx)
        try:
            return await game.run()
        finally:
            self.presence_status, self.current_game = 0, ""
            try:
                await self.publish_presence()
            except Exception:
                pass

    async def run(self):
        await self.connect()
        log(f"{self.name} connected to {self.server}")
        tasks = [asyncio.create_task(self.presence_loop()),
                 asyncio.create_task(self.lobby_watch_loop())]
        try:
            while not self.stopping:
                game_id, invited = await self.select_game()
                if game_id is None:
                    break
                idx = await self.join_game(game_id, invited)
                if idx is None:
                    if invited:  # unsatisfiable invitation: decline, don't loop
                        await self.decline_invite(game_id)
                    await asyncio.sleep(1.0)
                    continue
                log(f"joined game {game_id} as player {idx}")
                won = await self.play(game_id, idx)
                log(f"game {game_id} over: {'won' if won else 'lost'}")
                if self.once:
                    break
        finally:
            self.stopping = True
            for t in tasks:
                t.cancel()
            try:  # clean teardown: free the presence entry
                await self.kv.delete(f"players.{self.name}")
            except Exception:
                pass
            await self.nc.drain()


class Game:
    """One competitive game: this class IS the engine for our own board."""

    def __init__(self, agent, game_id, player_idx):
        self.a = agent
        self.id = game_id
        self.idx = player_idx
        self.locked = {}          # (r,c) -> settled cell dict
        self.seqs = {}            # (r,c) -> last stream seq (CAS expectation)
        self.piece = None         # [type, orient, row, col] while one is falling
        self.piece_idx = 0
        self.score = 0
        self.total_lines = 0
        self.eliminated = set()   # player IDs seen in game_over events
        self.lock = asyncio.Lock()  # serializes board mutations + publishes
        self.started = asyncio.Event()
        self.ended = asyncio.Event()  # set when meta goes finished/archived or stream dies
        self.dead = False         # we topped out
        self.meta = None
        self.roster = []
        # Garbage protocol registers (guide §4.4): rows OWED to this board
        # (attackers CAS-add our garbage register) vs rows APPLIED (recorded in
        # our txn register, whose per-subject CAS gates every bulk transform).
        self.garbage_owed = 0
        self.garbage_by = 0
        self.txn_applied = 0
        self.txn_seq = 0          # txn register's last stream seq — the gate expectation

    def cell_subject(self, r, c):
        return f"jetris.game.{self.id}.player.{self.a.name}.playfield.cell.{r}.{c}"

    def garbage_subject(self, player=None):
        return f"jetris.game.{self.id}.player.{player or self.a.name}.playfield.garbage"

    def txn_subject(self):
        return f"jetris.game.{self.id}.player.{self.a.name}.playfield.txn"

    # ---- consumers -------------------------------------------------------

    async def meta_loop(self):
        try:
            sub = await self.a.js.subscribe(f"jetris.game.{self.id}.meta",
                                            stream=f"JETRIS_GAME_{self.id}",
                                            ordered_consumer=True)
            async for msg in sub.messages:
                meta = json.loads(msg.data)
                self.meta = meta
                if meta["status"] == "in_progress":
                    self.started.set()
                if meta["status"] in ("finished", "archived", "cancelled"):
                    self.ended.set()
        except Exception:
            self.ended.set()  # stream deleted → the game is gone

    async def events_loop(self):
        """Events live on per-kind, per-player subjects (events.<kind>.<pid>)
        so per-subject retention can never trim one player's event with
        another's. Garbage is NOT an event — it arrives via the garbage
        register (garbage_loop below)."""
        try:
            sub = await self.a.js.subscribe(f"jetris.game.{self.id}.events.>",
                                            stream=f"JETRIS_GAME_{self.id}",
                                            ordered_consumer=True)
            async for msg in sub.messages:
                ev = json.loads(msg.data)
                kind, pid = ev.get("kind"), ev.get("player_id")
                if kind == "game_over":
                    self.eliminated.add(pid)
                    if pid != self.a.name:
                        self.record_result(ev)
        except Exception:
            self.ended.set()

    async def garbage_loop(self):
        """Watch our garbage register — the cumulative rows OWED to this
        board, CAS-added by attackers — and apply the deficit. The register
        is a monotonic total, so a trimmed intermediate value or a missed
        message is subsumed by the next one."""
        try:
            sub = await self.a.js.subscribe(self.garbage_subject(),
                                            stream=f"JETRIS_GAME_{self.id}",
                                            ordered_consumer=True)
            async for msg in sub.messages:
                reg = json.loads(msg.data) if msg.data else {}
                self.garbage_owed = max(self.garbage_owed, reg.get("total", 0))
                self.garbage_by = reg.get("by", 0)
                if self.garbage_owed > self.txn_applied and not self.dead:
                    await self.apply_owed_garbage()
        except Exception:
            pass

    async def refresh_registers(self):
        """Reload both registers from the stream (after a lost gate race or a
        board resync)."""
        for subject, apply in ((self.txn_subject(), "txn"), (self.garbage_subject(), "owed")):
            try:
                raw = await self.a.js.get_last_msg(f"JETRIS_GAME_{self.id}", subject, direct=True)
                body = json.loads(raw.data) if raw.data else {}
                if apply == "txn":
                    self.txn_seq = raw.seq
                    self.txn_applied = body.get("applied", 0)
                else:
                    self.garbage_owed = max(self.garbage_owed, body.get("total", 0))
                    self.garbage_by = body.get("by", self.garbage_by)
            except Exception:
                if apply == "txn":
                    self.txn_seq, self.txn_applied = 0, 0

    def record_result(self, ev):
        for p in self.roster:
            if p["player_id"] == ev["player_id"]:
                p["_score"] = ev.get("score", 0)
                p["_level"] = ev.get("level", 0)
                p["_pieces"] = ev.get("piece_count", 0)

    # ---- our engine ------------------------------------------------------

    @property
    def height(self):
        return 28 + self.meta["player_count"]  # 4 headroom + 24 + P visible

    def active_cells(self):
        return piece_cells(*self.piece) if self.piece else []

    def active_cell_payload(self, pt, orient, row, col):
        return {"t": pt, "a": True, "r": orient, "ar": row, "ac": col}

    async def publish_piece_move(self, new):
        """CAS-batch the diff between the current active cells and `new`
        (type, orient, row, col). On a dropped write: flash + resync."""
        old = set(self.active_cells())
        cells = [((r, c), self.active_cell_payload(*new)) for r, c in piece_cells(*new)]
        cells += [((r, c), None) for r, c in old - set(piece_cells(*new))]
        try:
            await self.a.publish_batch(self, cells, cas=True)
        except CASFailure:
            await self.flash(old)
            await self.resync()
            return False
        self.piece = list(new)
        return True

    async def flash(self, cells):
        """Broadcast a CAS-failure flash so spectators see the dropped write
        (core NATS, never the stream — guide §4.3)."""
        payload = {"pi": self.idx, "c": [[r, c] for r, c in cells]}
        await self.a.nc.publish(f"jetris.flash.{self.id}.{self.a.name}",
                                json.dumps(payload).encode())

    async def resync(self):
        """After a dropped CAS write, refetch our own board from the stream to
        recover the true cell contents and sequences."""
        self.locked, self.seqs, self.piece = {}, {}, None
        for r in range(self.height):
            for c in range(WIDTH):
                try:
                    raw = await self.a.js.get_last_msg(f"JETRIS_GAME_{self.id}",
                                                       self.cell_subject(r, c), direct=True)
                except Exception:
                    continue
                self.seqs[(r, c)] = raw.seq
                cell = json.loads(raw.data) if raw.data else {}
                if cell.get("a"):
                    self.piece = [cell.get("t", 0), cell.get("r", 0),
                                  cell.get("ar", 0), cell.get("ac", 0)]
                elif cell.get("o"):
                    self.locked[(r, c)] = cell

    async def spawn(self):
        pt = piece_at(self.meta["seed"], self.piece_idx)
        row, col = 2, (WIDTH - 4) // 2
        cells = piece_cells(pt, 0, row, col)
        if not can_place(self.locked, self.height, cells):
            raise GameOver()  # spawn blocked by settled cells: top-out
        new = (pt, 0, row, col)
        payload = [((r, c), self.active_cell_payload(*new)) for r, c in cells]
        try:
            await self.a.publish_batch(self, payload, cas=True)
            self.piece = list(new)
        except CASFailure:
            await self.flash(cells)
            await self.resync()
        return time.monotonic()

    async def lock_piece(self, dest):
        """Authoritative NoCAS: settle the piece at `dest`, then run line
        clears, the shrink event, the piece-index bump, all per the guide."""
        pt = self.piece[0]
        old = set(self.active_cells())
        dest_cells = piece_cells(pt, dest[1], dest[2], dest[3])
        cells = [((r, c), self.locked_payload(pt)) for r, c in dest_cells]
        cells += [((r, c), None) for r, c in old - set(dest_cells)]
        await self.a.publish_batch(self, cells, cas=False)
        for rc in dest_cells:
            self.locked[rc] = self.locked_payload(pt)
        self.piece = None

        done = completed_rows(self.locked, self.height)
        if done:
            await self.clear_rows(done)
        self.piece_idx += 1
        await self.bump_meta_piece_idx()

    def locked_payload(self, pt):
        cell = {"o": True, "t": pt}
        if self.idx:
            cell["pi"] = self.idx
        return cell

    async def clear_rows(self, rows):
        """Collapse completed rows as a GATED transform (guide §4.4): the txn
        register gates the batch, so a garbage application racing this clear
        can never be clobbered by a stale collapse — the loser recomputes from
        fresh state. Then CAS-add the attack to every surviving opponent's
        garbage register."""
        cleared = 0
        for attempt in range(3):
            if attempt > 0:
                # Lost the gate (our own garbage application landed between
                # detection and publish): converge and re-detect.
                await self.refresh_registers()
                await self.resync()
                rows = completed_rows(self.locked, self.height)
                if not rows:
                    return
            new = {}
            removed = set(rows)
            shift = 0
            for r in range(self.height - 1, -1, -1):
                if r in removed:
                    shift += 1
                    continue
                for c in range(WIDTH):
                    if (r, c) in self.locked:
                        new[(r + shift, c)] = self.locked[(r, c)]
            diff = []
            for r in range(self.height):
                for c in range(WIDTH):
                    if new.get((r, c)) != self.locked.get((r, c)):
                        diff.append(((r, c), new.get((r, c))))
            txn = {"applied": self.txn_applied, "op": "clear", "by": self.idx}
            try:
                await self.a.publish_gated_batch(self, txn, diff)
            except CASFailure:
                continue
            self.locked = new
            cleared = len(rows)
            break
        if not cleared:
            return
        self.score += cleared
        self.total_lines += cleared
        log(f"cleared {cleared} line(s), score {self.score}")
        rows = self.attack_rows(cleared)
        if rows:
            await self.bump_victim_ledgers(rows)

    def attack_rows(self, lines):
        """The garbage a clear owes: one row per line, or — when the meta's
        guideline_garbage is set — the Guideline table: a single sends
        nothing, a double 1 row, a triple 2, a Tetris 4."""
        if not (self.meta or {}).get("guideline_garbage"):
            return lines
        return {1: 0, 2: 1, 3: 2}.get(lines, 4 if lines >= 4 else 0)

    async def bump_victim_ledgers(self, lines):
        """The attack: CAS-add `lines` to every surviving opponent's garbage
        register. The register is a cumulative total, so simultaneous
        attackers' adds serialize on the per-subject expectation and converge
        to the exact sum — an attack can never be lost."""
        for p in self.roster:
            pid = p["player_id"]
            if pid == self.a.name or pid in self.eliminated:
                continue
            subject = self.garbage_subject(pid)
            for _ in range(20):
                total, seq = 0, 0
                try:
                    raw = await self.a.js.get_last_msg(f"JETRIS_GAME_{self.id}",
                                                       subject, direct=True)
                    body = json.loads(raw.data) if raw.data else {}
                    total, seq = body.get("total", 0), raw.seq
                except Exception:
                    pass  # never written: expect 0
                payload = json.dumps({"total": total + lines, "by": self.idx}).encode()
                try:
                    resp = await self.a.nc.request(subject, payload, timeout=5,
                                                   headers={H_EXPECT_LAST: str(seq)})
                    body = json.loads(resp.data) if resp.data else {}
                    if not body.get("error"):
                        break
                    if body["error"].get("err_code") not in CAS_ERR_CODES:
                        log(f"ledger bump {pid}: {body['error']}")
                        break
                    # Lost the add race to another attacker: refresh, re-add.
                except Exception as exc:
                    log(f"ledger bump {pid}: {exc}")
                    break

    async def bump_meta_piece_idx(self):
        """Best-effort: mirror our piece cursor into the meta (informational)."""
        try:
            meta, seq = await self.a.fetch_meta(self.id)
            meta["piece_idx"] = self.piece_idx
            await self.a.js.publish(f"jetris.game.{self.id}.meta",
                                    json.dumps(meta).encode(),
                                    headers={H_EXPECT_LAST: str(seq)})
        except Exception:
            pass  # racing writers are fine; this field is advisory

    async def apply_owed_garbage(self):
        """Apply the outstanding deficit (owed − applied) as ONE gated cascade
        transform: shift the stack up, fill the bottom with adversarial rows
        (punched with the game's garbage_holes — the same columns on every row
        of the raise), keep the falling piece at its position unless the
        risen stack overlaps it — then lift it the MINIMUM amount that clears
        the conflict, and top out if it's pushed off the board. Locked cells
        shoved past row 0 mean the whole board is full: also a top-out. The
        txn register (message 1 of the batch, per-subject CAS) makes the
        application exactly-once across duplicate signals and replays."""
        async with self.lock:
            for attempt in range(3):
                n = self.garbage_owed - self.txn_applied
                if n <= 0 or self.dead:
                    return
                causer_idx = self.garbage_by
                garbage = {"o": True, "t": 1, "g": True}
                if causer_idx:
                    garbage["pi"] = causer_idx
                # The game's garbage_holes (meta; absent = 0 = solid rows):
                # empty cells in every raised row — one random column set for
                # the whole raise, or one per row when random_garbage_holes is
                # set — always leaving adversarial cells in the row.
                meta = self.meta or {}
                hole_count = min(max(int(meta.get("garbage_holes", 0)), 0), 4, WIDTH - 1)
                per_row = bool(meta.get("random_garbage_holes"))
                holes = set(random.sample(range(WIDTH), hole_count))
                board_full = any(r < n for r, _c in self.locked)
                new = {}
                for (r, c), cell in self.locked.items():
                    if r - n >= 0:
                        new[(r - n, c)] = cell
                for i, r in enumerate(range(self.height - n, self.height)):
                    if per_row and i > 0:
                        holes = set(random.sample(range(WIDTH), hole_count))
                    for c in range(WIDTH):
                        if c not in holes:
                            new[(r, c)] = garbage
                old_wire = {rc: self.locked.get(rc) for rc in set(self.locked) | set(new)}
                new_piece, squeezed = self.piece, False
                if self.piece:
                    squeezed = True
                    k = 0
                    while True:
                        cand = [self.piece[0], self.piece[1], self.piece[2] - k, self.piece[3]]
                        cand_cells = piece_cells(*cand)
                        if min(r for r, _c in cand_cells) < 0:
                            break  # off the top: squeezed out
                        if can_place(new, self.height, cand_cells):
                            new_piece, squeezed = cand, False
                            break
                        k += 1
                diff = [(rc, cell) for rc, cell in
                        ((rc, new.get(rc)) for rc in old_wire) if cell != old_wire[rc]]
                if self.piece:
                    old_active = set(self.active_cells())
                    if squeezed:
                        diff += [((r, c), None) for r, c in old_active if (r, c) not in new]
                    elif new_piece != self.piece:
                        new_cells = set(piece_cells(*new_piece))
                        diff += [((r, c), self.active_cell_payload(*new_piece)) for r, c in new_cells]
                        diff += [((r, c), None) for r, c in old_active - new_cells
                                 if (r, c) not in new]
                txn = {"applied": self.garbage_owed, "op": "shrink", "by": self.idx}
                if squeezed:
                    txn["topped"] = [self.idx]
                if board_full:
                    txn["full"] = True
                try:
                    await self.a.publish_gated_batch(self, txn, diff)
                except CASFailure:
                    # A replayed signal lost the gate: converge and re-check.
                    await self.refresh_registers()
                    await self.resync()
                    continue
                self.locked = new
                if squeezed:
                    self.piece = None
                elif self.piece:
                    self.piece = new_piece
                log(f"garbage: +{n} row(s) from player {causer_idx}")
                if squeezed or board_full:
                    self.dead = True  # the play loop raises GameOver on its next step
                return

    # ---- outcome ---------------------------------------------------------

    async def publish_game_over(self):
        # Per-player subject: our one game_over can never be trimmed by
        # another player's events.
        ev = {"kind": "game_over", "player_id": self.a.name, "score": self.score,
              "level": min(self.total_lines // 10, 19), "piece_count": self.piece_idx,
              "team": 0}
        await self.a.js.publish(
            f"jetris.game.{self.id}.events.game_over.{self.a.name}",
            json.dumps(ev).encode())

    async def archive(self):
        """We triggered the finish: transition finished→archived (CAS elects
        one archiver), publish the ArchiveRecord, delete the game's resources.
        Guide §5.5: don't disconnect until this is done."""
        await asyncio.sleep(5.0)  # let every peer see the final events
        try:
            meta, seq = await self.a.fetch_meta(self.id)
        except Exception:
            return
        if meta["status"] != "finished":
            return
        meta["status"] = "archived"
        try:
            await self.a.js.publish(f"jetris.game.{self.id}.meta",
                                    json.dumps(meta).encode(),
                                    headers={H_EXPECT_LAST: str(seq)})
        except Exception:
            return  # someone else won the archive CAS
        record = {
            "game_id": self.id, "mode": MODE_COMPETITIVE,
            "player_count": meta["player_count"],
            "players": [self.player_result(p) for p in self.roster],
            "started_at": meta.get("started_at", meta["created_at"]),
            "finished_at": meta.get("finished_at", now_rfc3339()),
            "winning_team": -1,
            "boards": await self.board_pictures(),
        }
        await self.a.js.publish(ARCHIVE_SUBJECT, json.dumps(record).encode())
        try:
            await self.a.js.delete_stream(f"JETRIS_GAME_{self.id}")
        except Exception:
            pass
        try:
            await self.a.kv.delete(f"games.{self.id}")
        except Exception:
            pass
        try:
            await self.a.js.purge_stream(CHAT_STREAM,
                                         subject=f"jetris.chat.{self.id}")
        except Exception:
            pass
        log(f"archived game {self.id}")

    def player_result(self, p):
        pid = p["player_id"]
        if pid == self.a.name:
            score, level, pieces = self.score, min(self.total_lines // 10, 19), self.piece_idx
        else:
            score, level, pieces = p.get("_score", 0), p.get("_level", 0), p.get("_pieces", 0)
        out = {"player_id": pid, "score": score, "piece_count": pieces}
        if level:
            out["level"] = level
        if pid not in self.eliminated:
            out["winner"] = True
        if p.get("agent"):
            out["agent"] = True
        return out

    async def board_pictures(self):
        """Sparse per-player snapshots of the visible region (rows 4..end),
        fetched from the stream BEFORE it is deleted; row 0 = first visible."""
        pics = []
        ids = sorted(p["player_id"] for p in self.roster)
        for i, pid in enumerate(ids):
            cells = []
            fetches = [(r, c, self.a.js.get_last_msg(
                f"JETRIS_GAME_{self.id}",
                f"jetris.game.{self.id}.player.{pid}.playfield.cell.{r}.{c}",
                direct=True))
                for r in range(HEADROOM, self.height) for c in range(WIDTH)]
            for r, c, fut in fetches:
                try:
                    raw = await fut
                except Exception:
                    continue
                if raw.data and raw.data != b"{}":
                    cells.append({"r": r - HEADROOM, "c": c,
                                  "d": json.loads(raw.data)})
            pics.append({"label": pid, "idx": i, "w": WIDTH,
                         "h": self.height - HEADROOM, "cells": cells})
        return pics

    # ---- the play loop ---------------------------------------------------

    async def run(self):
        meta, _ = await self.a.fetch_meta(self.id)
        self.meta = meta
        self.piece_idx = meta.get("piece_idx", 0)
        listing = self.a.listings.get(self.id) or {}
        self.roster = [dict(p) for p in (listing.get("players") or [])]
        tasks = [asyncio.create_task(self.meta_loop()),
                 asyncio.create_task(self.events_loop()),
                 asyncio.create_task(self.garbage_loop())]
        try:
            if await self.a.toggle_ready(self.id):
                log("all ready — running the countdown")
                await self.run_countdown_and_refresh_roster()
            await self.started.wait()
            # roster may have filled after our snapshot; refresh from the listing
            listing = self.a.listings.get(self.id) or listing
            if listing.get("players"):
                self.roster = [dict(p) for p in listing["players"]]
            log("game started")
            won = await self.play_pieces()
            if won:
                await self.transition_finished_and_archive()
            else:
                await self.wait_for_end()
            return won
        finally:
            for t in tasks:
                t.cancel()

    async def run_countdown_and_refresh_roster(self):
        await self.a.run_countdown(self.id)

    async def play_pieces(self):
        try:
            while not self.ended.is_set():
                if self.dead:
                    raise GameOver()
                async with self.lock:
                    spawn_t = await self.spawn()
                await asyncio.sleep(0.15)  # think pause, keeps play watchable
                if self.piece is None:
                    continue  # spawn was dropped by CAS; resynced, try again
                plan = plan_placement(self.locked, self.height, self.piece[0],
                                      self.piece[2], self.piece[3])
                if plan is None:
                    raise GameOver()
                await self.execute(plan, spawn_t)
                if self.win_check():
                    return True
            return not self.dead and self.win_check()
        except GameOver:
            self.dead = True
            await self.publish_game_over()
            log(f"topped out (score {self.score})")
            if len(self.eliminated | {self.a.name}) >= self.meta["player_count"]:
                await self.transition_finished_and_archive()  # simultaneous draw
            return False

    def win_check(self):
        others = {p["player_id"] for p in self.roster} - {self.a.name}
        return bool(others) and others <= self.eliminated and not self.dead

    async def execute(self, plan, spawn_t):
        """Drive the piece to (orient, col) one step at a time, honoring the
        gravity cadence, then hard-drop and settle it."""
        target_o, target_c = plan
        next_gravity = spawn_t + GRAVITY_SECONDS
        while True:
            if self.dead:
                raise GameOver()
            if self.ended.is_set():
                return
            async with self.lock:
                if self.piece is None:
                    return  # a shrink squeezed the piece away
                pt, o, row, col = self.piece
                if time.monotonic() >= next_gravity:
                    # our own gravity tick: down one, or lock in place
                    if can_place(self.locked, self.height,
                                 piece_cells(pt, o, row + 1, col)):
                        await self.publish_piece_move((pt, o, row + 1, col))
                        next_gravity += GRAVITY_SECONDS
                        continue
                    await self.lock_piece((pt, o, row, col))
                    return
                if o != target_o:
                    step = (pt, (o + 1) % 4, row, col)
                elif col != target_c:
                    step = (pt, o, row, col + (1 if target_c > col else -1))
                else:
                    dest_r = drop_row(self.locked, self.height, pt, o, row, col)
                    await self.lock_piece((pt, o, dest_r, col))
                    return
                if not can_place(self.locked, self.height, piece_cells(*step)):
                    # blocked (board changed under us): drop where we are
                    dest_r = drop_row(self.locked, self.height, pt, o, row, col)
                    await self.lock_piece((pt, o, dest_r, col))
                    return
                await self.publish_piece_move(step)
            await asyncio.sleep(0.06)  # pace the moves; don't spam the stream

    async def transition_finished_and_archive(self):
        if await self.a.transition_meta(self.id, "finished"):
            await self.archive()
        self.ended.set()

    async def wait_for_end(self):
        """We lost: linger until the winner archives (meta terminal or the
        stream disappears), so our seat and consumers wind down cleanly."""
        try:
            await asyncio.wait_for(self.ended.wait(), timeout=120)
        except asyncio.TimeoutError:
            pass


# --------------------------------------------------------------------------

def selftest():
    """Offline conformance checks: RNG parity with the Go implementation and
    basic geometry sanity. The expected sequences below were generated by the
    repo's Go rng package (internal/rng) for the given seeds."""
    expected = {
        42: [2, 3, 4, 1, 0, 5, 6, 6, 0, 5, 4, 2, 3, 1, 2, 1, 4, 3, 6, 5, 0],
        12345: [6, 3, 2, 1, 0, 4, 5, 0, 1, 5, 3, 2, 6, 4, 3, 6, 4, 2, 5, 1, 0],
    }
    for seed, seq in expected.items():
        got = [piece_at(seed, i) for i in range(len(seq))]
        assert got == seq, f"RNG mismatch for seed {seed}: {got} != {seq}"
        bag = sorted(got[:7])
        assert bag == [0, 1, 2, 3, 4, 5, 6], f"first bag not a permutation: {bag}"
    assert piece_cells(0, 0, 2, 3) == [(3, 3), (3, 4), (3, 5), (3, 6)]  # I spawn
    assert len({tuple(sorted(piece_cells(t, o, 0, 0))) for t in range(7)
                for o in range(4)}) > 7
    print("selftest OK")


async def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--server", default="nats://localhost:4222")
    ap.add_argument("--name", default=CODENAME,
                    help="agent codename (version stem of the player name)")
    ap.add_argument("--join", default=None, metavar="GAMEID",
                    help="join this specific game instead of scanning the lobby")
    ap.add_argument("--auto-join", action="store_true",
                    help="also join open agent-allowed games (default: invited games only)")
    ap.add_argument("--once", action="store_true", help="play one game, then exit")
    ap.add_argument("--selftest", action="store_true")
    args = ap.parse_args()
    if args.selftest:
        selftest()
        return
    agent = Agent(args.server, args.name, args.join, args.once, args.auto_join)
    loop = asyncio.get_running_loop()
    for sig in (signal.SIGINT, signal.SIGTERM):
        loop.add_signal_handler(sig, lambda: setattr(agent, "stopping", True))
    await agent.run()


if __name__ == "__main__":
    asyncio.run(main())
