"""The agent: one connected peer.

Lobby plumbing (presence, the lobby mirror, invitations, joining and hosting
games) plus the game loop it runs when it joins one. It plays all three
modes (cooperative, competitive and teams) as an ordinary peer over the wire
protocol (jetris-agent-guide.md).
"""

import asyncio
import random
import secrets
import time
import uuid
from dataclasses import dataclass
from datetime import datetime, timezone

import nats
import nats.errors
import nats.js.errors
from nats.js import api

from . import jsx, natsctx, replay, serverlog
from .consts import (ARCHIVE_STREAM, ARCHIVE_SUBJECT, BLACKBOARD_BUCKET, INVITE_TTL, LOBBY_BUCKET,
                     MODE_COMPETITIVE, MODE_COOPERATIVE, MODE_TEAMS, PRESENCE_EVERY, PRESENCE_TTL,
                     game_stream_name, meta_subject)
from .difficulty import MAX_NEXT_COUNT, difficulty_tuning
from .rng import BAG_SINGLE, bag_label, normalize_bag
from .rules import (MAX_GARBAGE_HOLES, MIN_SURVIVAL_HOLES, extra_columns, extra_rows,
                    normalize_survival, normalize_team_count, team_names)
from .wire import dumps, log, now_rfc, o_bool, o_int, o_str, parse_rfc, player_summary, players_of, to_obj
from .workers import PlanPool

BLACKBOARD_MARKER_TTL = 30.0  # LimitMarkerTTL: enables the per-key TTL, and is how long an expired key's marker lingers

WATCH_RETRY_MAX = 30.0  # ceiling for the doubling pause between failed re-watches
WATCH_SETTLED = 30.0    # a watch up this long before ending: the next retry starts afresh

# Caps a game's name, in characters. A name is the game's ID, so it lands in
# the game's stream name, in every subject it writes and in its lobby KV key.
MAX_GAME_NAME_LEN = 24

_ERR_STREAM_NAME_IN_USE = 10058


@dataclass
class ConnChoice:
    """How to reach the NATS server, with the same precedence as the game and
    the nats CLI: an explicit --server URL (with optional --user /
    --password) wins over --context, and an empty context name means the
    currently selected one."""

    server: str = ""    # NATS URL; "" = connect via context instead
    context: str = ""   # NATS context name; "" = the selected context
    user: str = ""      # used with server
    password: str = ""  # used with server


@dataclass
class Hosting:
    """The --create configuration: what kind of game to host, how big, and
    how agent-friendly. None on an Agent means "never host"."""

    game_name: str = ""        # what to call the game: the name IS its ID, so its stream is JETRIS_GAME_<name> and lobbies list it by name ("" = a generated UUID). Cut to what a stream name, a subject and a KV key all take by game_name()
    mode: int = MODE_COMPETITIVE
    players: int = 2           # seat count (per TEAM in teams mode; min 2, teams min 1)
    teams: int = 2             # teams mode: how many teams play each other (clamped 2..6; the total seat count is teams x players)
    extra_cols: int = 4        # shared boards: columns every seat beyond the first adds to the standard 10 (clamped 4..10)
    max_agents: int = 0        # agent seats, this agent included: on each team of a teams game, in the whole game elsewhere (<=0 = all seats)
    next: int = MAX_NEXT_COUNT  # revealed upcoming pieces (clamped 0..MAX_NEXT_COUNT)
    holes: int = 0             # holes per garbage row (clamped 0..4; 0 = solid, permanent rows)
    random: bool = False       # every garbage row draws its own hole columns (off = one draw per raise)
    guideline: bool = False    # attacks follow the Modern table (0/1/2/4 rows for 1/2/3/4 lines)
    hold: bool = False         # the hold queue (this agent never holds; humans in the game may)
    split: bool = False        # shared playfields with company: deal the seven piece types out between the seats of a playfield, each playing only its ration (meta split_pieces)
    bag: str = ""              # the piece randomizer (meta bag): "" the 7-bag, "double" the double bag, "none" no bag
    extra_rows: int = 0        # shared boards: rows every seat beyond the first adds below the standard 20 (clamped 0..10; meta extra_rows)
    line_goal: int = 0         # the game's length in lines (meta line_goal; 0 = until top out)
    single: bool = False       # cooperative: score every seat on its own, the top score wins (meta scoring "individual")
    survival: str = ""         # cooperative: the rising floor's tier (meta survival); "" = no floor. No line goal with it, at least one hole per row
    pause_alone: bool = False  # open games: an agent left as the only player waits for company instead of playing on (listing agents_pause_alone)
    seed: int = 0              # the game's piece seed when seed_set (--seed): the same seed deals every crew the same pieces, which is what makes benchmark runs comparable; otherwise the clock's
    seed_set: bool = False
    host_only: bool = False    # --host-only: create the game and stay in the lobby until it is over, without taking a seat (a benchmark's neutral host)


def rand_id(n):
    """n random hex chars (batch ids)."""
    return secrets.token_hex((n + 1) // 2)[:n]


def uuid_v4():
    """A random RFC-4122 v4 UUID string: the game-id format every other
    client uses."""
    return str(uuid.uuid4())


def game_name(s):
    """Cut what the user typed to a name a game can carry, and so to the
    game's ID, since a named game's ID IS its name: ASCII letters, digits and
    the underscore survive, every other run becomes one dash, the ends are
    trimmed and the whole is cut to MAX_GAME_NAME_LEN. It is what a NATS
    stream name, a subject token and a KV key all accept, those being what a
    game ID has to be. Nothing usable in it comes back empty, and the game
    keeps a UUID."""
    name = []
    sep = False
    for r in s:
        usable = r == "_" or "a" <= r <= "z" or "A" <= r <= "Z" or "0" <= r <= "9"
        if not usable:
            sep = True
            continue
        if sep and name:
            if len(name) + 2 > MAX_GAME_NAME_LEN:
                break
            name.append("-")
        if len(name) >= MAX_GAME_NAME_LEN:
            break
        sep = False
        name.append(r)
    return "".join(name)


# ---- the lobby's rules (pure) ---------------------------------------------


def agents_among(players):
    """Count the roster seats agents hold."""
    return sum(1 for p in players if p["agent"])


def pick_team(g, want, policy):
    """The team an agent should join: the invited team when the invitation
    names one (want >= 0), else the least-populated team with room, over
    however many teams the game is played between (listing team_count; absent
    is the historical two). With policy set, a team also needs a free AGENT
    seat: the listing's max_agents caps the agents of each team (guide §2);
    an invitation is the permission, so an invited join passes False.
    Returns -1 when no team has a seat for us."""
    team_count = normalize_team_count(o_int(g, "team_count"))
    team_size = o_int(g, "team_size")
    if team_size <= 0:
        team_size = o_int(g, "player_count") // team_count
    counts, agents = [0] * team_count, [0] * team_count
    for p in players_of(g):
        if 0 <= p["team"] < team_count:
            counts[p["team"]] += 1
            if p["agent"]:
                agents[p["team"]] += 1

    def room(t):
        return counts[t] < team_size and (not policy or agents[t] < o_int(g, "max_agents"))

    if 0 <= want < team_count:
        return want if room(want) else -1
    best, best_count = -1, team_size
    for t in range(team_count):
        if room(t) and counts[t] < best_count:
            best, best_count = t, counts[t]
    return best


def joinable(g):
    """An open game (any mode) with a free seat this agent may take under the
    game's agent policy (guide §2): max_agents caps the agents of the whole
    game, or, in teams, of EACH team, so a free seat on at least one team
    whose agent seats are not all taken."""
    players = players_of(g)
    # An open game takes joiners before it starts and while it runs (its
    # seats are anyone's at any time); the countdown is the one moment it
    # does not (guide §5). An invite game takes nobody uninvited.
    if o_str(g, "status") not in ("created", "in_progress"):
        return False
    if o_bool(g, "invite_only") or len(players) >= o_int(g, "player_count"):
        return False
    if o_int(g, "mode") == MODE_TEAMS:
        return pick_team(g, -1, True) >= 0
    return o_int(g, "max_agents") > agents_among(players)


def free_seat(g, team):
    """The lowest free seat of a listing (and, in teams, the lowest free slot
    of the given team, the seat being team x size + slot) as every lobby
    assigns them (guide §5.2): a seat a departed player freed is the next one
    taken, and nobody else's seat moves. Returns (seat, slot, ok); ok is
    False when full."""
    taken = {p["seat"] for p in players_of(g)}
    if o_int(g, "mode") == MODE_TEAMS:
        size = o_int(g, "team_size")
        if size <= 0:
            size = o_int(g, "player_count") // normalize_team_count(o_int(g, "team_count"))
        size = max(size, 1)
        for slot in range(size):
            seat = team * size + slot
            if seat not in taken:
                return seat, slot, True
        return 0, 0, False
    for seat in range(o_int(g, "player_count")):
        if seat not in taken:
            return seat, 0, True
    return 0, 0, False


def ready_on(players, on):
    """Count the ready players among those on a playfield (on: the team's
    members, a competitive board's one seat, everyone on the crew's)."""
    return sum(1 for p in players if p["ready"] and on(p))


def ready_to_start(g):
    """The start rule (guide §5.3): an invite game's table is ready when
    every seat is filled and everyone is ready; an open game's as soon as
    every playfield has a ready player (the crew's one board, every team's,
    every competitive board) whoever else is seated and not ready."""
    players = players_of(g)
    if not players:
        return False
    if o_bool(g, "invite_only"):
        if any(not p["ready"] for p in players):
            return False
        return len(players) >= o_int(g, "player_count")
    mode = o_int(g, "mode")
    if mode == MODE_TEAMS:
        for t in range(normalize_team_count(o_int(g, "team_count"))):
            if ready_on(players, lambda p, t=t: p["team"] == t) == 0:
                return False
        return True
    if mode == MODE_COMPETITIVE:
        for seat in range(o_int(g, "player_count")):
            if ready_on(players, lambda p, seat=seat: p["seat"] == seat) == 0:
                return False
        return True
    return ready_on(players, lambda p: True) > 0


def _bool(v):
    return "true" if v else "false"


def _url_str(url):
    """A connected server's URL without whatever credentials it carried."""
    if url is None:
        return "?"
    host = url.hostname or "?"
    return f"{url.scheme}://{host}:{url.port}" if url.port else f"{url.scheme}://{host}"


# ---- the agent ----------------------------------------------------------------


class Agent:
    """One connected peer: lobby plumbing plus the game loop it runs when it
    joins."""

    def __init__(self, conn, stem, difficulty, join_id="", once=False, auto_join=False, host=None,
                 wait=600.0, pub=0):
        """Build an agent with a fresh instance id and per-difficulty tuning.
        Raises ValueError for a name the protocol does not take."""
        name = f"{stem}-{secrets.token_hex(2)}-{difficulty}"
        if len(name) > 32:
            raise ValueError(f'agent name "{name}" exceeds 32 characters')
        self.conn = conn
        self.name = name
        self.difficulty = difficulty
        self.tn = difficulty_tuning(difficulty)
        self.pub = pub             # publish discipline for move batches: consts.PUB_*
        self.join_id = join_id
        self.invite_team = -1      # teams: the team the current invitation names (-1 = none)
        self.host = host           # not None: create one game first, then play it
        self.wait = wait           # max wait (seconds) for a joined game to fill and start before un-joining
        self.once = once
        self.auto_join = auto_join
        self.coordinate = True     # shared boards: coordinate with teammate agents through the blackboard; off, the agent plays every board on its own
        self.trace = False         # --trace: log every piece's lifecycle with microsecond stamps
        self.soft_drop = False     # --soft-drop: never hard-drop

        self.nc = None
        self.js = None
        self.bucket = LOBBY_BUCKET          # the lobby KV bucket, or a private one under test
        self.kv = None
        self.bb_bucket = BLACKBOARD_BUCKET  # the blackboard bucket, or a private one under test
        self.bb = None                      # the blackboard handle, None while there is none

        self.listings = {}  # gameID -> listing
        self.invites = {}   # gameID -> our invitation

        self.presence_status = 0
        self.current_game = ""
        self.game = None    # the game being played, for the lobby watch's roster pushes (None between games)

        self.rng = random.Random()
        self.stop_ev = asyncio.Event()
        self.conn_err = None  # set when the connection closed for good
        self._tasks = set()
        self.plan_pool = PlanPool()  # the planner's lookahead workers (--plan-workers), started on first use

    def stop(self):
        self.stop_ev.set()

    def stopping(self):
        return self.stop_ev.is_set()

    def done(self):
        """Whether this connection's work is over: the agent stopped, or the
        connection closed (or draining) for good."""
        return self.stopping() or self.nc is None or self.nc.is_closed or self.nc.is_draining

    async def sleep(self, seconds):
        """A stop-aware sleep: False when the agent stopped before or during
        it, True when the time ran out."""
        if self.stop_ev.is_set():
            return False
        if seconds <= 0:
            return True
        try:
            await asyncio.wait_for(self.stop_ev.wait(), timeout=seconds)
        except asyncio.TimeoutError:
            return True
        return False

    def _spawn(self, coro):
        task = asyncio.create_task(coro)
        self._tasks.add(task)
        task.add_done_callback(self._tasks.discard)
        return task

    async def stream(self, game_id):
        """A game's stream info (NotFoundError when the stream is gone)."""
        return await self.js.stream_info(game_stream_name(game_id))

    # ---- connection & lobby ------------------------------------------------

    async def connect(self):
        # A vanished server is the client's job: it reconnects in the
        # background (2 s apart, forever) while every task here just waits on
        # its subscription. Giving up would CLOSE the connection, which
        # closes every subscription, and a resident agent with a dead
        # connection is useless.
        async def on_disconnected():
            if not self.stopping():
                log.info("nats: disconnected; reconnecting in the background")

        async def on_reconnected():
            log.info("nats: reconnected to %s", _url_str(self.nc.connected_url))

        async def on_error(err):
            # While the client is reconnecting every failed attempt, and
            # every consumer that noticed, raises an error: the disconnect
            # was logged once and the client rebuilds what it needs once it
            # is back, so they are noise until then.
            if self.stopping() or self.nc is None or not self.nc.is_connected:
                return
            log.info("nats: %s", err or type(err).__name__)

        async def on_closed():
            # With unlimited reconnects the client only closes on a fatal
            # error (an authorization failure on reconnect, say): there is
            # nothing left to wait for, so stop and let run report it.
            if self.stopping():
                return
            self.conn_err = (self.nc.last_error if self.nc else None) or nats.errors.ConnectionClosedError()
            self.stop()

        opts = dict(name=self.name, error_cb=on_error, disconnected_cb=on_disconnected,
                    reconnected_cb=on_reconnected, closed_cb=on_closed)

        async def dial(**kwargs):
            # The first connection fails fast (a server that is not there is
            # the user's to hear about); once made, it is kept for good.
            nc = await nats.connect(max_reconnect_attempts=1, reconnect_time_wait=0, **kwargs)
            nc.options["max_reconnect_attempts"] = -1
            nc.options["reconnect_time_wait"] = 2
            return nc

        js_domain = ""
        if self.conn.server:
            if self.conn.user:
                opts["user"], opts["password"] = self.conn.user, self.conn.password
            servers = [u.strip() for u in self.conn.server.split(",") if u.strip()]
            self.nc = await dial(servers=servers, **opts)
        else:
            # NATS-CLI-compatible contexts (credentials, TLS, JS domain
            # included); an empty name is the currently selected context.
            try:
                settings = natsctx.resolve(self.conn.context)
                opts.update(settings.connect_kwargs)
                js_domain = settings.js_domain
                self.nc = await dial(**opts)
            except Exception as e:
                raise ConnectionError(f'connect via NATS context "{self.conn.context}": '
                                      f"{e or type(e).__name__} (pass --server or --context)") from e
        self.js = self.nc.jetstream(domain=js_domain) if js_domain else self.nc.jetstream()
        self.kv = await self.ensure_lobby_bucket()
        try:
            await self.js.stream_info(ARCHIVE_STREAM)
        except jsx.NotFoundError:
            await self.js.add_stream(api.StreamConfig(
                name=ARCHIVE_STREAM, subjects=[ARCHIVE_SUBJECT], storage=api.StorageType.FILE))
        await self.ensure_log_stream()
        await self.ensure_replay_stream()
        if self.coordinate:
            # The blackboard: a server that cannot give claims their TTL
            # leaves the agent playing uncoordinated.
            if not self.bb_bucket:
                self.bb_bucket = BLACKBOARD_BUCKET
            try:
                self.set_blackboard(await self.ensure_blackboard())
            except Exception as e:
                log.info("blackboard %s: %s: playing uncoordinated", self.bb_bucket, e or type(e).__name__)
                self.coordinate = False

    async def ensure_lobby_bucket(self):
        """Create or update the lobby bucket exactly as the game does:
        LimitMarkerTTL enables the per-message-TTL presence writes AND makes
        expiries emit watchable delete markers, so every lobby learns a
        vanished player is gone without polling. It also converges a plain
        bucket some earlier client may have left behind. Idempotent, so it
        serves both connect and the lobby watcher's recovery of a bucket
        that was deleted underneath a running agent."""
        return await jsx.ensure_kv(self.js, self.bucket, api.StorageType.FILE, PRESENCE_TTL)

    async def ensure_blackboard(self):
        """Create or update the blackboard bucket: memory storage (nothing on
        it outlives a piece), LimitMarkerTTL so every claim can carry its
        TTL. Idempotent, so it serves both connect and the watch's recovery
        of a bucket deleted underneath a running agent. A server too old for
        per-key TTLs raises: no blackboard."""
        return await jsx.ensure_kv(self.js, self.bb_bucket, api.StorageType.MEMORY, BLACKBOARD_MARKER_TTL)

    def blackboard(self):
        """The bucket handle, None while there is none."""
        return self.bb

    def set_blackboard(self, kv):
        self.bb = kv

    async def ensure_log_stream(self):
        await serverlog.ensure_log_stream(self)

    async def journal(self, kind, game_id, mode, players):
        await serverlog.journal(self, kind, game_id, mode, players)

    async def ensure_replay_stream(self):
        await replay.ensure_replay_stream(self)

    async def maybe_archive_replay(self, rec_json):
        await replay.maybe_archive_replay(self, rec_json)

    async def publish_presence(self):
        p = {"player_id": self.name, "name": self.name, "status": self.presence_status,
             "agent": True, "last_seen": now_rfc()}
        if self.current_game:
            p["game_id"] = self.current_game
        # A presence write is a plain KV put carrying a per-message TTL,
        # exactly like the game's: if this agent dies without its clean exit,
        # the entry self-deletes after PRESENCE_TTL instead of haunting the
        # lobby. Published straight to the key's subject.
        await jsx.kv_put_ttl(self.nc, self.bucket, "players." + self.name, dumps(p), PRESENCE_TTL)

    def set_presence(self, status, game):
        self.presence_status, self.current_game = status, game

    async def _presence(self):
        """publish_presence, a failure swallowed (the next tick retries)."""
        try:
            await self.publish_presence()
        except Exception:
            pass

    async def presence_loop(self):
        while True:
            # While the client is reconnecting a JetStream publish could only
            # sit out its ack timeout: skip the tick. The first one after the
            # reconnect restores the entry, which outlives any short outage.
            if self.nc.is_connected:
                try:
                    await self.publish_presence()
                except Exception as e:
                    log.info("presence: %s", e or type(e).__name__)
            if not await self.sleep(PRESENCE_EVERY):
                return

    async def lobby_watch(self):
        """Mirror the lobby KV into memory: game listings and our
        invitations.

        A server outage never reaches here: the watcher's subscription
        survives the reconnect and its ordered consumer resumes where it
        stopped. What ends a watch is its stop at shutdown, or the bucket
        itself being deleted, as a shared server can do to a running agent.
        A finished watch is either the end, or something to re-establish
        after a pause, recreating a vanished bucket first, as at connect.
        Never spin on it: the pause doubles while re-watching keeps failing,
        and a failure that repeats is logged once."""
        delay, last = 1.0, ""
        while True:
            started = time.monotonic()
            msg = "lobby watch ended; re-watching"
            try:
                await self.watch_lobby(last != "")
            except asyncio.CancelledError:
                raise
            except Exception as e:
                msg = f"lobby watch: {e or type(e).__name__}"
            if self.done():
                return
            if time.monotonic() - started > WATCH_SETTLED:
                delay, last = 1.0, ""  # it had been up: start afresh
            if msg != last:
                last = msg
                log.info("%s (repeats not logged)", msg)
            if not await self.sleep(delay):
                return
            delay = min(2 * delay, WATCH_RETRY_MAX)

    async def watch_lobby(self, report):
        """Run one lobby watcher until it ends: returns once a watch that was
        established has closed underneath it, raises the error that kept one
        from being established. The watch opens with the bucket's current
        state and then a None marker; the mirror is rebuilt aside and swapped
        in whole at the marker, so a re-watch neither shows an empty lobby
        meanwhile nor keeps the entries that vanished with the old bucket or
        during the gap. With report set, the re-established watch is
        logged."""
        try:
            w = await jsx.KvWatch.start(self.js, self.kv, self.bucket, ">")
        except Exception:
            if not await self.recreate_lobby_bucket():
                raise
            w = await jsx.KvWatch.start(self.js, self.kv, self.bucket, ">")

        async def stop_with_agent():
            await self.stop_ev.wait()
            await w.stop()

        stopper = asyncio.create_task(stop_with_agent())
        invite_prefix = "invites." + self.name + "."
        listings, invites = {}, {}
        try:
            async for e in w.updates():
                if e is None:  # end-of-initial-data marker
                    self.listings, self.invites = listings, invites
                    if report:
                        log.info("lobby watch re-established: %d games, %d invitations", len(listings), len(invites))
                    continue
                key, value, _rev, op = e
                deleted = op in (jsx.KV_DEL, jsx.KV_PURGE)
                # Private maps until the marker, the shared mirror after it.
                if key.startswith("games.") and len(key) > 6:
                    gid = key[6:]
                    if deleted:
                        listings.pop(gid, None)
                    else:
                        listings[gid] = to_obj(value)
                        # The game being played: its roster follows the
                        # listing (an open game's seats come and go), for
                        # the deal and the verdict.
                        g = self.game
                        if g is not None and g.id == gid:
                            g.on_roster(players_of(listings[gid]))
                elif key.startswith(invite_prefix) and len(key) > len(invite_prefix):
                    gid = key[len(invite_prefix):]
                    if deleted:
                        invites.pop(gid, None)
                    else:
                        invites[gid] = to_obj(value)
        finally:
            stopper.cancel()
            await w.stop()

    async def recreate_lobby_bucket(self):
        """Tell whether the lobby bucket is what a failed watch is missing
        and, if so, recreate it as at connect and report True. Our presence
        entry went with the old bucket, so it is republished at once rather
        than on the next tick. The KeyValue handle is left alone: it holds
        nothing but names, so it works against the new bucket (same
        configuration) exactly as against the old, and the other tasks using
        it need no coordination."""
        try:
            await self.js.key_value(self.bucket)
            return False
        except nats.js.errors.BucketNotFoundError:
            pass
        except Exception:
            return False
        try:
            await self.ensure_lobby_bucket()
        except Exception as e:
            raise RuntimeError(f"lobby bucket {self.bucket} is gone and recreating it failed: "
                               f"{e or type(e).__name__}") from e
        log.info("lobby bucket %s was deleted underneath us; recreated it", self.bucket)
        await self._presence()
        return True

    def fresh_invites(self):
        """Pending (not declined, not stale) invitations, oldest first: one
        KV key per game. Returns (gameID, invitation) pairs."""
        out = []
        now = datetime.now(timezone.utc)
        for gid, o in self.invites.items():
            if o_bool(o, "declined"):
                continue
            created = o_str(o, "created_at")
            t = parse_rfc(created)
            if t is None or (now - t).total_seconds() > INVITE_TTL:
                continue
            out.append((created, gid, o))
        out.sort(key=lambda v: v[0])
        return [(gid, o) for _, gid, o in out]

    async def consume_invite(self, game_id):
        """Delete an invitation key (accept or drop-stale): the deletion is
        what tells the inviter it was handled."""
        self.invites.pop(game_id, None)
        try:
            await self.kv.delete("invites." + self.name + "." + game_id)
        except Exception:
            pass

    async def decline_invite(self, game_id):
        """Rewrite the key with declined=true so the inviter sees the refusal
        until they dismiss it."""
        o = self.invites.pop(game_id, None)
        if o is None:
            return
        o["declined"] = True
        try:
            await self.kv.put("invites." + self.name + "." + game_id, dumps(o))
        except Exception:
            pass

    async def select_game(self):
        """Pick the next game: an explicit --join, a --create host, a fresh
        invitation, or the first joinable open game. Returns (gameID,
        invited), or ("", False) when stopping."""
        if self.join_id:
            gid, self.join_id = self.join_id, ""
            return gid, False
        if self.host is not None:
            host, self.host = self.host, None  # host once; afterwards fall back to resident selection
            try:
                gid = await self.create_game(host)
            except Exception as e:
                log.info("create game: %s", e or type(e).__name__)
                return "", False
            if not host.host_only:
                return gid, False
            # A neutral host: the game is up for others to play; we keep the
            # lobby until it is over, then go on as a resident (or leave,
            # with --once).
            await self.host_only_wait(gid)
            if self.once or self.stopping():
                return "", False
        while not self.stopping():
            handled = False
            for gid, o in self.fresh_invites():
                if gid in self.listings:
                    self.invite_team = o_int(o, "team") if "team" in o else -1
                    return gid, True
                await self.consume_invite(gid)  # game gone: drop the stale invite
                handled = True
            if handled:
                continue
            if self.auto_join:
                listings = dict(self.listings)
                # Oldest first (id as the tiebreak), so waiting games fill in
                # creation order and every scanning agent converges on the
                # same one.
                for gid in sorted(listings, key=lambda i: (o_str(listings[i], "created_at"), i)):
                    if joinable(listings[gid]):
                        return gid, False
            await self.sleep(1.0)
        return "", False

    async def join_game(self, game_id, invited):
        """CAS-append us to the KV listing and announce our roster seat
        (guide §5.2). Returns our player index, or -1 if the game can't be
        joined."""
        key = "games." + game_id
        while True:
            try:
                entry = await self.kv.get(key)
            except Exception:
                return -1
            g = to_obj(entry.value)
            players = players_of(g)
            for p in players:
                if p["player_id"] == self.name:
                    return p["seat"]  # already joined: our seat
            if o_bool(g, "invite_only") and not invited and o_str(g, "creator_id") != self.name:
                return -1
            # A game that is over takes nobody; an invite game's roster is
            # frozen once it starts; an open game's seats are anyone's while
            # it runs (not during the countdown).
            status = o_str(g, "status")
            if status in ("created", "starting"):
                pass
            elif status == "in_progress":
                if o_bool(g, "invite_only"):
                    return -1
                # The listing says in_progress until the archiver deletes
                # it: the meta is what says an open game is over.
                try:
                    meta, _ = await self.fetch_meta(game_id)
                except Exception:
                    pass
                else:
                    if o_str(meta, "status") in ("finished", "archived", "cancelled"):
                        return -1
            else:
                return -1
            # The agent policy (guide §2), checked here inside the CAS loop
            # so agents racing for the last agent seat never over-fill it:
            # agents may not join at all at max_agents 0, and past that the
            # cap is on the agents of each TEAM in teams mode (pick_team), of
            # the whole game elsewhere. An invitation IS the permission
            # (bypasses it).
            if not invited and o_int(g, "max_agents") <= 0:
                return -1
            if len(players) >= o_int(g, "player_count"):
                return -1
            team = 0
            teams_mode = o_int(g, "mode") == MODE_TEAMS
            if teams_mode:
                team = pick_team(g, self.invite_team if invited else -1, not invited)
                if team < 0:
                    return -1  # the invited (or every) team is full, or its agent seats are
            elif not invited and agents_among(players) >= o_int(g, "max_agents"):
                return -1  # the game's agent seats are taken
            # The seat: the lowest free one (free_seat), stable, so a cell's
            # player index names one seat for the whole game.
            seat, slot, ok = free_seat(g, team)
            if not ok:
                return -1
            summary = player_summary(self.name, self.name, seat=seat, agent=True)
            if teams_mode:
                summary["team"], summary["team_slot"] = team, slot
            players.append(summary)
            g["players"] = players
            # An invite game full -> starting; an open game starts on
            # readiness (toggle_ready elects the countdown) whatever seats
            # stay free.
            full = len(players) >= o_int(g, "player_count") and o_bool(g, "invite_only") and status == "created"
            if full:
                g["status"] = "starting"
            try:
                await self.kv.update(key, dumps(g), last=entry.revision)
            except Exception:
                if self.stopping():
                    return -1
                await asyncio.sleep(0.05)
                continue  # CAS conflict: retry from a fresh read
            try:
                await jsx.js_publish(self.nc, "jetris.game." + game_id + ".roster." + self.name, dumps(summary))
            except Exception as e:
                log.info("roster publish: %s", e or type(e).__name__)
            if full:
                await self.transition_meta(game_id, "starting")
            self.set_presence(1, game_id)
            await self._presence()
            if invited:
                await self.consume_invite(game_id)
            return seat

    async def create_game(self, h):
        """Host a new game: the creator's side of the lifecycle (guide §5),
        writing what every lobby's create row writes: the per-game stream
        (atomic batches enabled), the initial meta (CAS "subject must be
        empty", so a colliding id fails loudly), the lobby KV listing others
        discover, and the transient game.created event. The caller then
        joins its own game like any other player. Returns the new game id;
        raises when the game cannot be created."""
        players, team_size, team_count = h.players, 0, 0
        if h.mode == MODE_TEAMS:
            # The count is players PER TEAM, and the game seats one such team
            # per team the creator asked for.
            players = max(players, 1)
            team_count = normalize_team_count(h.teams)
            team_size = players
            players = team_count * team_size
        else:
            # A competitive game needs an opponent (the last player standing
            # wins) while a cooperative game can be played alone, for the
            # high score, on the standard 10-column board.
            players = max(players, 2 if h.mode == MODE_COMPETITIVE else 1)
        # The agent policy: how many seats agents may take, on each team of a
        # teams game, in the whole game elsewhere (guide §2). Agent-hosted
        # games are agent-friendly by default: every seat.
        policy_seats = team_size if h.mode == MODE_TEAMS else players
        max_agents = h.max_agents
        if max_agents <= 0 or max_agents > policy_seats:
            max_agents = policy_seats
        # A shared board's width setting; competitive boards are always the
        # standard 10 columns, so its meta records none.
        extra = extra_columns(h.extra_cols) if h.mode != MODE_COMPETITIVE else 0
        next_count = min(max(h.next, 0), MAX_NEXT_COUNT)
        holes = min(max(h.holes, 0), MAX_GARBAGE_HOLES)
        # The rising floor is a single playfield's game (gameplays §2
        # Survival): its rows must be clearable, nothing attacks anybody,
        # and there is no line goal: it runs until the floor wins.
        survival = normalize_survival(h.survival) if h.mode == MODE_COOPERATIVE else ""
        guideline = h.guideline
        if survival:
            holes = max(holes, MIN_SURVIVAL_HOLES)
            guideline = False
        random_holes = h.random and holes > 0

        # A named game IS its name: the name is the game's ID, so the stream
        # below is JETRIS_GAME_<name> and every lobby lists the game by name.
        # The claim on it is the meta publish further down (expected sequence
        # 0 lands only on a stream with no meta on it) since creating a
        # stream is idempotent for an identical config and two games of one
        # name have exactly that.
        game_id = h.game_name or uuid_v4()
        # The stream config every game runs on (guide §4.1): full game
        # history retained in memory (no per-subject cap: ordered consumers
        # then never skip a trimmed write, and spectators replay the game
        # from the start), with atomic publishes for the CAS batches and
        # direct gets for last-per-subject reads.
        try:
            await self.js.add_stream(api.StreamConfig(
                name=game_stream_name(game_id),
                subjects=["jetris.game." + game_id + ".>"],
                allow_atomic=True,
                allow_direct=True,
                storage=api.StorageType.MEMORY,
                retention=api.RetentionPolicy.LIMITS,
            ))
        except Exception as e:
            if h.game_name and getattr(e, "err_code", None) == _ERR_STREAM_NAME_IN_USE:
                raise RuntimeError(f"a game called {game_id} already exists") from e
            raise RuntimeError(f"create stream: {e or type(e).__name__}") from e

        seats_on_pf = players
        if team_size > 0:
            seats_on_pf = team_size
        elif h.mode == MODE_COMPETITIVE:
            seats_on_pf = 1
        split = h.split and seats_on_pf > 1  # only a playfield with company has pieces to split
        bag = normalize_bag(h.bag)
        rows = extra_rows(h.extra_rows) if h.mode != MODE_COMPETITIVE else 0
        goal = 0 if survival else max(h.line_goal, 0)
        single = h.single and h.mode == MODE_COOPERATIVE and players > 1
        seed = h.seed if h.seed_set else time.time_ns()

        def shape(o):
            """The settings the meta and the listing both carry, absent when
            they are the default (as every other client omits them)."""
            if extra > 0:
                o["extra_columns"] = extra
            if o is listing:
                o["max_agents"] = max_agents
                if h.pause_alone:
                    o["agents_pause_alone"] = True
            o["next_count"] = next_count
            if holes > 0:
                o["garbage_holes"] = holes
            if random_holes:
                o["random_garbage_holes"] = True
            if guideline:
                o["guideline_garbage"] = True
            if h.hold:
                o["hold"] = True
            if split:
                o["split_pieces"] = True
            if bag != BAG_SINGLE:
                o["bag"] = bag
            if rows > 0:
                o["extra_rows"] = rows
            if goal > 0:
                o["line_goal"] = goal
            if single:
                o["scoring"] = "individual"
            if survival:
                o["survival"] = survival

        listing = {"game_id": game_id, "mode": h.mode, "status": "created", "player_count": players}
        meta = {"game_id": game_id, "mode": h.mode, "player_count": players}
        for o in (meta, listing):
            if team_size > 0:
                o["team_count"] = team_count
                o["team_size"] = team_size
                o["team_names"] = team_names(team_count)  # the piece colours, the default names (guide §4.2)
            shape(o)
        meta["seed"] = seed
        meta["status"] = "created"
        meta["creator_id"] = self.name
        meta["created_at"] = now_rfc()
        meta["piece_idx"] = 0
        try:
            _, conflict = await self.meta_publish(game_id, dumps(meta), 0)
        except Exception as e:
            raise RuntimeError(f"publish meta: {e or type(e).__name__}") from e
        if conflict:
            if h.game_name:
                raise RuntimeError(f"a game called {game_id} already exists")
            raise RuntimeError(f"game id {game_id} already has a meta")

        listing["creator_id"] = self.name
        listing["players"] = []  # no seats taken yet: everyone joins, the creator included
        listing["created_at"] = now_rfc()
        try:
            await self.kv.put("games." + game_id, dumps(listing))
        except Exception as e:
            raise RuntimeError(f"write listing: {e or type(e).__name__}") from e

        # Transient courtesy event (core NATS, no stream): lobbies
        # beep/refresh on it but discover the game from the KV either way.
        try:
            await self.nc.publish("jetris.lobby.event.game.created", dumps(
                {"kind": "game.created", "game_id": game_id, "player_id": self.name, "time": now_rfc()}))
        except Exception:
            pass

        teams_shape = ""
        policy = f"max {max_agents} agents"
        if team_count > 0:
            teams_shape = f" in {team_count} teams of {team_size}"
            policy += " per team"
        if h.pause_alone:
            policy += ", pausing when alone"
        mode_name = {MODE_COOPERATIVE: "cooperative", MODE_COMPETITIVE: "competitive", MODE_TEAMS: "teams"}[h.mode]
        log.info("created %s game %s for %d players%s (%s, next %d, garbage holes %d, random %s, "
                 "modern garbage %s, hold %s, %s, seed %d): waiting for opponents",
                 mode_name, game_id, players, teams_shape, policy, next_count, holes, _bool(random_holes),
                 _bool(h.guideline), _bool(h.hold), bag_label(h.bag), seed)
        return game_id

    async def host_only_wait(self, game_id):
        """--host-only's stay in the lobby: the game we created is others'
        to play, and we wait until its listing is gone (archived by whoever
        finished it, or dropped by whoever left it) noting its start on the
        way, so a benchmark's driver can wait on this process for the game's
        end. The listing reaches the lobby mirror a moment after the create,
        so a listing not seen yet is waited for, briefly."""
        log.info("hosting %s without a seat: waiting for it to be played out", game_id)
        seen, started = False, False
        first_look = time.monotonic()
        while not self.stopping():
            listing = self.listings.get(game_id)
            if listing is not None:
                seen = True
                if not started and o_str(listing, "status") == "in_progress":
                    started = True
                    log.info("game %s started with %d player(s)", game_id, len(players_of(listing)))
            elif seen or time.monotonic() - first_look > 10.0:
                log.info("game %s is over (its listing is gone)", game_id)
                return
            if not await self.sleep(0.5):
                return

    async def unjoin_game(self, game_id):
        """Walk away from a game that never started (guide §5.6): CAS-remove
        our seat from the listing (reverting a full "starting" roster to
        "created" so the freed seat is joinable again), then purge our roster
        announcement so late joiners don't discover a ghost opponent.
        Pre-start only for an invite game: the meta is the authoritative
        started check, and once play begins its roster is frozen."""
        key = "games." + game_id
        while True:
            try:
                entry = await self.kv.get(key)
            except Exception:
                break
            g = to_obj(entry.value)
            is_open = not o_bool(g, "invite_only")
            status = o_str(g, "status")
            if status not in ("created", "starting") and not (is_open and status == "in_progress"):
                return
            if not is_open:
                try:
                    meta, _ = await self.fetch_meta(game_id)
                except Exception:
                    pass
                else:
                    if o_str(meta, "status") not in ("created", "starting"):
                        return
            players = players_of(g)
            kept = [p for p in players if p["player_id"] != self.name]
            if len(kept) == len(players):
                break  # not on the roster; nothing to remove
            g["players"] = kept
            if status == "starting" and not ready_to_start(g):
                g["status"] = "created"
            try:
                await self.kv.update(key, dumps(g), last=entry.revision)
            except Exception:
                if self.nc.is_closed:
                    break
                await asyncio.sleep(0.05)
                continue  # CAS conflict: retry from a fresh read
            try:
                await self.js.purge_stream(game_stream_name(game_id),
                                           subject="jetris.game." + game_id + ".roster." + self.name)
            except Exception:
                pass
            try:
                await self.nc.publish("jetris.lobby.event.game.left", dumps(
                    {"kind": "game.left", "game_id": game_id, "player_id": self.name, "time": now_rfc()}))
            except Exception:
                pass
            break
        self.set_presence(0, "")
        await self._presence()

    async def toggle_ready(self, game_id):
        """CAS-set our ready flag; returns True if our toggle completed the
        set (the start rule met by this very write): then WE run the
        countdown."""
        key = "games." + game_id
        while True:
            try:
                entry = await self.kv.get(key)
            except Exception:
                return False
            g = to_obj(entry.value)
            if o_str(g, "status") == "in_progress":
                return False
            players = players_of(g)
            for p in players:
                if p["player_id"] == self.name:
                    p["ready"] = True
            g["players"] = players
            # The start rule (ready_to_start, guide §5.3). An invite game's
            # table is ready when full and ready: the toggle that completes
            # it runs the countdown. An open game's is ready as soon as every
            # playfield has a ready player: the one write that moves the
            # listing from created to starting is elected to run the
            # countdown.
            ready = ready_to_start(g)
            is_open = not o_bool(g, "invite_only")
            elected = ready and not is_open
            if ready and is_open and o_str(g, "status") == "created":
                g["status"] = "starting"
                elected = True
            try:
                await self.kv.update(key, dumps(g), last=entry.revision)
            except Exception:
                if self.stopping():
                    return False
                await asyncio.sleep(0.05)
                continue
            return elected

    async def fetch_meta(self, game_id):
        """The game's meta and the sequence it was read at. Raises (a
        NotFoundError when there is no meta, or no stream)."""
        raw = await jsx.get_last_msg(self.js, game_stream_name(game_id), meta_subject(game_id))
        return to_obj(raw.data), raw.seq

    async def transition_meta(self, game_id, status):
        """CAS-advance the meta to a new status; never regresses a completed
        game."""
        for _ in range(5):
            try:
                meta, seq = await self.fetch_meta(game_id)
            except Exception as e:
                # A blip: the next attempt may see the stream again.
                log.info("transition %s to %s: fetch meta: %s", game_id, status, e or type(e).__name__)
                await asyncio.sleep(0.25)
                continue
            cur = o_str(meta, "status")
            if cur in ("finished", "archived", "cancelled") and status != "archived":
                return False
            if cur == status:
                return True
            meta["status"] = status
            if status == "in_progress":
                meta["started_at"] = now_rfc()
            if status == "finished":
                meta["finished_at"] = now_rfc()
            try:
                _, conflict = await self.meta_publish(game_id, dumps(meta), seq)
            except Exception:
                conflict = True
            if not conflict:
                return True
            await asyncio.sleep(0.05)
        return False

    async def meta_publish(self, game_id, payload, expect_last):
        """Publish the meta with an expected-last-subject-sequence CAS:
        (seq, conflict)."""
        return await self.cas_request(meta_subject(game_id), payload, {jsx.H_EXPECT_LAST: str(expect_last)})

    async def cas_request(self, subject, payload, headers=None):
        """Publish one message as a JetStream request (headers may carry a
        CAS expectation or batch markers) and interpret the ack: (seq,
        cas_conflict). Any other failure raises."""
        return await jsx.cas_request(self.nc, subject, payload, headers)

    async def run_countdown(self, game_id):
        """Publish the 5 -> 0 countdown, then transition the meta to
        in_progress."""
        subject = "jetris.game." + game_id + ".countdown"

        async def tick(seconds):
            try:
                await jsx.js_publish(self.nc, subject, dumps({"seconds": seconds}))
            except Exception:
                pass

        for i in range(5, 0, -1):
            await tick(i)
            if not await self.sleep(1.0):
                return
        await tick(0)
        await asyncio.sleep(0.7)
        if await self.transition_meta(game_id, "in_progress"):
            # The countdown ran out and the game is on: journal it, with the
            # game's shape from its meta, and mirror the status onto the
            # listing, where the lobby rows, the join rules and every agent
            # read that the game is on (an open game's free seats are
            # joinable from here).
            try:
                meta, _ = await self.fetch_meta(game_id)
            except Exception:
                pass
            else:
                await self.journal(serverlog.LOG_GAME_STARTED, game_id, o_int(meta, "mode"), o_int(meta, "player_count"))
            await self.set_listing_status(game_id, "in_progress")

    async def run(self):
        """The agent's main loop: connect, keep presence + lobby mirror
        alive, and play games as they are selected. Raises when the
        connection could not be made, or closed for good."""
        try:
            await self.connect()
        except BaseException:
            if self.nc is not None and not self.nc.is_closed:
                self.stop()
                try:
                    await self.nc.close()
                except Exception:
                    pass
            raise
        log.info("%s connected to %s", self.name, _url_str(self.nc.connected_url))
        await self._presence()
        self._spawn(self.presence_loop())
        self._spawn(self.lobby_watch())
        try:
            while not self.stopping():
                game_id, invited = await self.select_game()
                if not game_id:
                    break
                idx = await self.join_game(game_id, invited)
                if idx < 0:
                    if invited:
                        await self.decline_invite(game_id)
                    await self.sleep(1.0)
                    continue
                log.info("joined game %s as player %d", game_id, idx)
                won = await self.play_game(game_id, idx)
                log.info("game %s over: %s", game_id, "won" if won else "lost")
                if self.once:
                    break
        finally:
            try:
                await asyncio.wait_for(self.kv.delete("players." + self.name), timeout=3.0)
            except Exception:
                pass
            self.stop()  # our own close: the closed handler must not mistake it for a loss
            self.plan_pool.close()
            for task in list(self._tasks):
                task.cancel()
            await asyncio.gather(*self._tasks, return_exceptions=True)
            try:
                await self.nc.drain()
            except Exception:
                pass
        if self.conn_err is not None:
            raise ConnectionError(f"NATS connection closed: {self.conn_err or type(self.conn_err).__name__}")

    async def play_game(self, game_id, idx):
        """Run one game and restore lobby presence afterward."""
        from .game import Game  # here, not at the top: game.py imports this module

        g = Game(self, game_id, idx)
        self.game = g
        try:
            won = await g.run()
        finally:
            self.game = None
        self.set_presence(0, "")
        await self._presence()
        return won

    async def set_listing_status(self, game_id, status):
        """Mirror a status the meta reached onto the lobby listing (a CAS
        loop; a listing already there, or further along, is left alone)."""
        key = "games." + game_id
        for _ in range(5):
            try:
                entry = await self.kv.get(key)
            except Exception:
                return
            g = to_obj(entry.value)
            if o_str(g, "status") not in ("created", "starting"):
                return  # there already, or past it
            g["status"] = status
            try:
                await self.kv.update(key, dumps(g), last=entry.revision)
                return
            except Exception:
                await asyncio.sleep(0.05)
