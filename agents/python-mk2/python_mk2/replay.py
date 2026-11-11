"""Replay archive (guide §5 step 6).

After the archive record is published and before the game stream is deleted,
the ENTIRE game stream is copied into the ONE shared file-backed
JETRIS_REPLAY stream so the lobby can replay the game later. A replay is kept
while the game is in the KEEP SET: the top REPLAY_TOP_N of its bucket (one
bucket per (mode, with/without agents) pair) or the REPLAY_RECENT_N most
recently finished games overall. A finishing game is always the most recent,
so it always gets a replay; it outlives the next REPLAY_RECENT_N games only
by ranking. Each message is republished under
"jetris.replay.<gameID>.<original tail>" (the game ID right after the
prefix), so one filter replays a game and a purge with the same filter
deletes a displaced one. Republished copies get copy-time stream timestamps,
so every copy carries its ORIGINAL timestamp in the Jetris-Ts header (integer
nanoseconds): an original-speed replay paces itself from it. The copy ends
with a "jetris.replay.<gameID>.done" marker message; the marker's presence is
what makes a replay complete and listable.
"""

import asyncio
import functools
import json
import time
from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone

from nats.aio.msg import Msg
from nats.js import api

from . import jsx
from .consts import ARCHIVE_STREAM, ARCHIVE_SUBJECT, MODE_COOPERATIVE, MODE_TEAMS, game_stream_name
from .serverlog import create_or_update_stream
from .wire import dumps, log, o_bool, o_int, o_str, parse_rfc

REPLAY_TOP_N = 10
REPLAY_RECENT_N = 25
REPLAY_STREAM = "JETRIS_REPLAY"
REPLAY_SUBJECT_PREFIX = "jetris.replay."
REPLAY_TS_HEADER = "Jetris-Ts"
REPLAY_PIN_PREFIX = "pins."  # lobby KV keys of the pinned replays
REPLAY_COPY_CHUNK = 512      # async publishes in flight during a copy

_ZERO_TIME = datetime(1, 1, 1, tzinfo=timezone.utc)


def replay_filter(game_id):
    return REPLAY_SUBJECT_PREFIX + game_id + ".>"


def replay_marker_subject(game_id):
    return REPLAY_SUBJECT_PREFIX + game_id + ".done"


async def ensure_replay_stream(a):
    """Create the shared replay stream (direct gets power the marker
    lookups). Part of the agent's connect-time bootstrap."""
    await create_or_update_stream(a.js, api.StreamConfig(
        name=REPLAY_STREAM,
        subjects=[REPLAY_SUBJECT_PREFIX + ">"],
        storage=api.StorageType.FILE,
        allow_direct=True,
    ))


@dataclass
class ReplayRecord:
    """The slice of an archive record the replay ranking needs."""

    game_id: str = ""
    mode: int = 0
    total_score: int = 0
    team_scores: list = field(default_factory=list)
    started_at: datetime = _ZERO_TIME
    finished_at: datetime = _ZERO_TIME
    players: list = field(default_factory=list)  # (score, agent) per player

    @classmethod
    def from_json(cls, data):
        """Decode an archive record; None when it is not one (not an object,
        a time that does not parse)."""
        try:
            o = json.loads(data)
        except (ValueError, UnicodeDecodeError):
            return None
        if not isinstance(o, dict):
            return None
        times = []
        for key in ("started_at", "finished_at"):
            if o.get(key) is None:
                times.append(_ZERO_TIME)
                continue
            t = parse_rfc(o.get(key))
            if t is None:
                return None
            times.append(t)
        scores = o.get("team_scores")
        players = o.get("players")
        return cls(
            game_id=o_str(o, "game_id"), mode=o_int(o, "mode"), total_score=o_int(o, "total_score"),
            team_scores=[s for s in scores if isinstance(s, int)] if isinstance(scores, list) else [],
            started_at=times[0], finished_at=times[1],
            players=[(o_int(p, "score"), o_bool(p, "agent")) for p in players if isinstance(p, dict)]
            if isinstance(players, list) else [],
        )

    def headline_score(self):
        """The ranking score every lobby shows: the shared total for
        cooperative, the best team's total for teams, the best player's score
        otherwise."""
        if self.mode == MODE_COOPERATIVE:
            return self.total_score
        if self.mode == MODE_TEAMS and self.team_scores:
            return max(0, max(self.team_scores))
        return max([0] + [score for score, _ in self.players])

    def has_agents(self):
        return any(agent for _, agent in self.players)

    def duration(self):
        if self.started_at == _ZERO_TIME or self.finished_at == _ZERO_TIME:
            return timedelta(0)
        return max(self.finished_at - self.started_at, timedelta(0))

    def rank_before(self, o):
        """The shared "By score" total order: score desc, shorter game, newer
        finish, game ID: identical to every other archiver's cut so they all
        keep the same top N."""
        si, sj = self.headline_score(), o.headline_score()
        if si != sj:
            return si > sj
        di, dj = self.duration(), o.duration()
        if di != dj:
            return di < dj
        if self.finished_at != o.finished_at:
            return self.finished_at > o.finished_at
        return self.game_id < o.game_id

    def same_bucket(self, o):
        return self.mode == o.mode and self.has_agents() == o.has_agents()

    def recent_before(self, o):
        """The "most recent games" total order: newer finish first, game ID
        on ties: identical to every other archiver's so they all keep the
        same REPLAY_RECENT_N."""
        if self.finished_at != o.finished_at:
            return self.finished_at > o.finished_at
        return self.game_id < o.game_id


def _ordered(recs, before):
    return sorted(recs, key=functools.cmp_to_key(lambda a, b: -1 if before(a, b) else (1 if before(b, a) else 0)))


def replay_keep_set(recs, pinned):
    """The retention policy as a pure function of the archive records (one
    per game ID) and the pinned games: every bucket's top REPLAY_TOP_N by
    rank_before, plus the REPLAY_RECENT_N most recent finishes overall by
    recent_before, plus every pinned game (a `pins.<gameID>` key in the lobby
    KV, see list_pinned_replays). Returns (keep, top): top is which of the
    kept games rank (the rest are kept for recency or by a pin)."""
    buckets = {}
    for r in recs:
        buckets.setdefault((r.mode, r.has_agents()), []).append(r)
    top = set()
    for b in buckets.values():
        for r in _ordered(b, ReplayRecord.rank_before)[:REPLAY_TOP_N]:
            top.add(r.game_id)
    keep = set(top)
    for r in _ordered(recs, ReplayRecord.recent_before)[:REPLAY_RECENT_N]:
        keep.add(r.game_id)
    keep.update(pinned or ())
    return keep, top


async def list_pinned_replays(a):
    """The pinned games, straight off the lobby KV: every `pins.<gameID>` key
    (guide §5 step 6). A pin keeps a game's replay for good, whatever its
    rank or age, until the key is deleted."""
    pinned = set()
    watcher = await a.kv.watch(REPLAY_PIN_PREFIX + ">", ignore_deletes=True, meta_only=True)
    try:
        while True:
            e = await watcher.updates(timeout=5.0)
            if e is None:  # the current keys are all in
                break
            pin = e.key.removeprefix(REPLAY_PIN_PREFIX)
            if pin != e.key and pin:
                pinned.add(pin)
    finally:
        try:
            await watcher.stop()
        except Exception:
            pass
    return pinned


async def maybe_archive_replay(a, rec_json):
    """Apply the retention policy for the record this agent JUST published
    (rec_json). Order matters for the lobby, which tracks replays by their
    markers: the replays this game displaces are purged FIRST, then the
    game's own stream is copied and its marker published LAST, so a marker
    arrival means every change of this archive is visible to one listing.
    Must run before the game stream is deleted. Best-effort: a failed copy is
    purged again and the game simply has no replay."""
    rec = ReplayRecord.from_json(rec_json)
    if rec is None or not rec.game_id:
        return
    try:
        prior = await fetch_archive_records(a)
    except Exception as e:
        log.info("replay %s: can't read archive records: %s", rec.game_id, e)
        return
    known, records = set(), []
    for r in prior:
        if r.game_id != rec.game_id and r.game_id not in known:
            known.add(r.game_id)
            records.append(r)
    known.add(rec.game_id)
    records.append(rec)  # ours, whether or not the drain caught it yet
    # A pin bucket that cannot be read keeps every listed replay rather than
    # risk purging a pinned one: the purge is the irreversible step.
    try:
        pinned = await list_pinned_replays(a)
    except Exception as e:
        log.info("replay %s: can't read the pinned replays, purging nothing: %s", rec.game_id, e)
        pinned = set()
        try:
            pinned = set(await list_replay_game_ids(a))
        except Exception:
            pass
    keep, top = replay_keep_set(records, pinned)
    if rec.game_id not in keep:
        return  # impossible while REPLAY_RECENT_N > 0, but the policy rules

    # Displacement first: every listed replay whose record we can see and
    # which the keep set no longer covers loses it.
    try:
        ids = await list_replay_game_ids(a)
    except Exception as e:
        log.info("replay %s: displacement check: %s", rec.game_id, e)
    else:
        for gid in ids:
            if gid in known and gid not in keep:
                log.info("replay %s: out of the keep set (top %d per bucket, last %d games, or pinned), purging its replay",
                         gid, REPLAY_TOP_N, REPLAY_RECENT_N)
                try:
                    await purge_replay(a, gid)
                except Exception:
                    pass

    try:
        await copy_game_to_replay_stream(a, rec.game_id)
    except Exception as e:
        log.info("replay %s: %s", rec.game_id, e or type(e).__name__)
        try:
            await purge_replay(a, rec.game_id)
        except Exception:
            pass
        return
    why = "recent"
    if rec.game_id in pinned:
        why = "pinned"
    elif rec.game_id in top:
        why = "bucket top"
    log.info("replay %s: archived (%s)", rec.game_id, why)


def _stream_seq_and_ts(msg):
    """A consumed message's stream sequence and its ORIGINAL timestamp in
    whole nanoseconds, off the delivery's reply subject."""
    tokens = Msg.Metadata._get_metadata_fields(msg.reply)
    if len(tokens) == 9:  # $JS.ACK.<stream>.<consumer>.<delivered>.<sseq>.<cseq>.<ts>.<pending>
        return int(tokens[5]), int(tokens[7])
    return int(tokens[Msg.Ack.StreamSeq]), int(tokens[Msg.Ack.Timestamp])


async def copy_game_to_replay_stream(a, game_id):
    """Republish every game-stream message under the game's replay prefix:
    payload verbatim, the original stream timestamp in the Jetris-Ts header,
    the original headers dropped (their CAS expectations reference the dying
    game stream). Async-published in chunks with every ack checked; the
    marker goes last, synchronously, so its presence proves the whole copy
    landed."""
    await ensure_replay_stream(a)
    stream = game_stream_name(game_id)
    last_seq = (await a.js.stream_info(stream)).state.last_seq
    if not last_seq:
        raise RuntimeError("empty game stream")
    prefix = "jetris.game." + game_id + "."
    deadline = time.monotonic() + 60.0

    def remaining():
        left = deadline - time.monotonic()
        if left <= 0:
            raise TimeoutError("the copy ran out of time")
        return left

    pending = []

    async def flush():
        if not pending:
            return
        done, not_done = await asyncio.wait(pending, timeout=remaining())
        if not_done:
            raise TimeoutError("the copy ran out of time waiting for its acks")
        for f in done:
            f.result()  # raises the publish's error, if it had one
        pending.clear()

    sub = await a.js.subscribe(prefix + ">", stream=stream, ordered_consumer=True)
    copied = 0
    try:
        done = False
        while not done:
            msg = await sub.next_msg(timeout=remaining())
            try:
                sseq, ts = _stream_seq_and_ts(msg)
            except Exception:
                continue
            done = sseq >= last_seq
            if not msg.subject.startswith(prefix):
                continue
            tail = msg.subject[len(prefix):]
            if not tail:
                continue
            pending.append(await a.js.publish_async(
                REPLAY_SUBJECT_PREFIX + game_id + "." + tail, msg.data or b"",
                headers={REPLAY_TS_HEADER: str(ts)}))
            copied += 1
            if len(pending) >= REPLAY_COPY_CHUNK:
                await flush()
        await flush()
    finally:
        for f in pending:
            f.cancel()
        try:
            await sub.unsubscribe()
        except Exception:
            pass
    await jsx.js_publish(a.nc, replay_marker_subject(game_id), dumps({"game_id": game_id, "msgs": copied}))


async def purge_replay(a, game_id):
    """Remove one game's replay (copied messages and marker) from the shared
    replay stream."""
    await a.js.purge_stream(REPLAY_STREAM, subject=replay_filter(game_id))


async def list_replay_game_ids(a):
    """The game IDs whose copy-complete marker is present (a subjects-filtered
    stream info: one marker subject per game)."""
    info = await a.js.stream_info(REPLAY_STREAM, subjects_filter=REPLAY_SUBJECT_PREFIX + "*.done")
    ids = []
    for subject in (info.state.subjects or {}):
        tail = subject.removeprefix(REPLAY_SUBJECT_PREFIX)
        if tail.endswith(".done"):
            gid = tail[:-len(".done")]
            if gid and "." not in gid:
                ids.append(gid)
    return ids


async def fetch_archive_records(a):
    """Drain the archive stream (append-only: complete once the last sequence
    seen at call time has been delivered)."""
    last_seq = (await a.js.stream_info(ARCHIVE_STREAM)).state.last_seq
    if not last_seq:
        return []
    out = []
    deadline = time.monotonic() + 10.0  # a partial read beats no decision
    sub = await a.js.subscribe(ARCHIVE_SUBJECT, stream=ARCHIVE_STREAM, ordered_consumer=True)
    try:
        while True:
            left = deadline - time.monotonic()
            if left <= 0:
                break
            try:
                msg = await sub.next_msg(timeout=left)
            except Exception:
                break
            rec = ReplayRecord.from_json(msg.data)
            if rec is not None and rec.game_id:
                out.append(rec)
            try:
                sseq, _ = _stream_seq_and_ts(msg)
            except Exception:
                continue
            if sseq >= last_seq:
                break
    finally:
        try:
            await sub.unsubscribe()
        except Exception:
            pass
    return out
