"""JetStream plumbing the protocol needs beyond the client's stock calls.

Raw publish requests carrying a CAS expectation or batch markers, the
multi-subject direct get, KV buckets with per-key TTLs, and a KV watch that
ends (instead of going quiet) when its bucket is deleted underneath it.
"""

import asyncio
import json

import nats.errors
import nats.js.errors
from nats.js import api

H_BATCH_ID = "Nats-Batch-Id"
H_BATCH_SEQ = "Nats-Batch-Sequence"
H_BATCH_COMMIT = "Nats-Batch-Commit"
H_EXPECT_LAST = "Nats-Expected-Last-Subject-Sequence"
H_TTL = "Nats-TTL"

# Wrong-expected-sequence rejections: the two err_codes the server uses for
# the plain and the batch cases.
CAS_ERR_CODES = (10071, 10164)

REQUEST_TIMEOUT = 5.0
NO_RESPONDERS_RETRIES = 2
NO_RESPONDERS_WAIT = 0.25

KV_PUT = "PUT"
KV_DEL = "DEL"
KV_PURGE = "PURGE"

NotFoundError = nats.js.errors.NotFoundError


class PublishError(Exception):
    """A publish the server answered with an error that is not a CAS loss."""


def parse_ack(data):
    """A JetStream publish ack (or error) off a reply: (seq, error dict)."""
    body = {}
    if data:
        try:
            body = json.loads(data)
        except ValueError:
            body = {}
    if not isinstance(body, dict):
        body = {}
    err = body.get("error")
    if isinstance(err, dict):
        return 0, err
    seq = body.get("seq")
    return (seq if isinstance(seq, int) else 0), None


def is_cas_conflict(err):
    return bool(err) and err.get("err_code") in CAS_ERR_CODES


async def cas_request(nc, subject, payload, headers=None, timeout=REQUEST_TIMEOUT):
    """Publish one message as a JetStream request (headers may carry a CAS
    expectation or batch markers) and interpret the ack: (seq, cas_conflict).
    Any other failure raises."""
    resp = await nc.request(subject, payload, timeout=timeout, headers=headers or None)
    seq, err = parse_ack(resp.data)
    if err:
        if is_cas_conflict(err):
            return 0, True
        raise PublishError(f"publish {subject}: {err.get('description', err)}")
    return seq, False


async def js_publish(nc, subject, payload, headers=None, ttl=None, timeout=REQUEST_TIMEOUT):
    """A plain JetStream publish, optionally with a per-message TTL in whole
    seconds. Returns the stream sequence; a rejection raises."""
    hdrs = dict(headers or {})
    if ttl is not None:
        hdrs[H_TTL] = f"{int(ttl)}s"
    attempt = 0
    while True:
        try:
            seq, conflict = await cas_request(nc, subject, payload, hdrs, timeout)
            break
        except nats.errors.NoRespondersError:
            # Nobody took it: the stream is between leaders, most likely.
            # Tried again, twice, a quarter of a second apart.
            attempt += 1
            if attempt > NO_RESPONDERS_RETRIES:
                raise
            await asyncio.sleep(NO_RESPONDERS_WAIT)
    if conflict:
        raise PublishError(f"publish {subject}: wrong last sequence")
    return seq


# ---- direct gets -------------------------------------------------------------


class RawMsg:
    __slots__ = ("subject", "seq", "data", "headers", "time")

    def __init__(self, subject, seq, data, headers, time=None):
        self.subject, self.seq, self.data, self.headers, self.time = subject, seq, data, headers, time


def _api_prefix(js):
    return js._prefix  # "$JS.API", or "$JS.<domain>.API"


async def get_last_msg(js, stream, subject):
    """The last message on a subject, by direct get. Raises NotFoundError when
    there is none (or no such stream)."""
    try:
        raw = await js.get_last_msg(stream, subject, direct=True)
    except nats.errors.NoRespondersError as e:
        raise NotFoundError() from e
    return RawMsg(raw.subject, raw.seq, raw.data or b"", raw.headers or {}, raw.time)


async def get_last_msgs_for(nc, js, stream, subjects, up_to_seq=0, batch=0, timeout=REQUEST_TIMEOUT):
    """The last message of every subject given (wildcards allowed), in one
    batched direct get: one request, the replies streamed to an inbox and
    closed by an end-of-batch status. Returns the messages in stream order."""
    req = {"multi_last": list(subjects)}
    if batch:
        req["batch"] = batch
    if up_to_seq:
        req["up_to_seq"] = up_to_seq
    inbox = nc.new_inbox()
    sub = await nc.subscribe(inbox)
    out = []
    try:
        await nc.publish(f"{_api_prefix(js)}.DIRECT.GET.{stream}", json.dumps(req).encode(), reply=inbox)
        while True:
            msg = await sub.next_msg(timeout=timeout)
            hdrs = msg.headers or {}
            status = hdrs.get("Status")
            if status:
                if status == "204":  # end of batch
                    break
                if status == "404":
                    break  # nothing on any of the subjects
                raise PublishError(f"direct get {stream}: status {status} {hdrs.get('Description', '')}")
            seq = hdrs.get("Nats-Sequence")
            out.append(RawMsg(hdrs.get("Nats-Subject", ""), int(seq) if seq else 0, msg.data or b"", hdrs))
    except nats.errors.NoRespondersError as e:
        raise NotFoundError() from e
    finally:
        try:
            await sub.unsubscribe()
        except Exception:
            pass
    return out


# ---- KV buckets ---------------------------------------------------------------


async def ensure_kv(js, bucket, storage, limit_marker_ttl):
    """Create the bucket, or bind to it and converge its stream to the config
    (idempotent): LimitMarkerTTL is what lets a key carry a per-message TTL
    and makes an expiry emit a watchable marker. A server too old for per-key
    TTLs raises KeyValueLimitMarkerTTLNotSupportedError."""
    cfg = api.KeyValueConfig(bucket=bucket, storage=storage, limit_marker_ttl=limit_marker_ttl, direct=True)
    try:
        return await js.create_key_value(cfg)
    except nats.js.errors.BadRequestError:
        pass  # the stream is there under another config: converge it below
    stream = f"KV_{bucket}"
    si = await js.stream_info(stream)
    sc = si.config
    if not sc.allow_msg_ttl or not sc.subject_delete_marker_ttl or not sc.allow_direct:
        sc.allow_msg_ttl = True
        sc.allow_direct = True
        sc.subject_delete_marker_ttl = limit_marker_ttl
        await js.update_stream(sc)
    return await js.key_value(bucket)


async def kv_put_ttl(nc, bucket, key, value, ttl):
    """A plain KV put carrying a per-message TTL, published straight to the
    key's subject. Returns the revision."""
    return await js_publish(nc, f"$KV.{bucket}.{key}", value, ttl=ttl)


class KvWatch:
    """A watch on a bucket's keys that knows when to give up: updates() yields
    every delivery (None once the initial values are in), and ends when the
    watch is stopped or the bucket's stream is gone or was recreated
    underneath it, which a liveness probe checks every few seconds."""

    PROBE_EVERY = 5.0

    def __init__(self, js, kv, bucket, watcher):
        self._js, self._kv, self._bucket, self._w = js, kv, bucket, watcher
        self._created = None
        self._stopped = False
        self._probe = asyncio.create_task(self._probe_loop())

    @classmethod
    async def start(cls, js, kv, bucket, keys):
        watcher = await kv.watch(keys)
        return cls(js, kv, bucket, watcher)

    async def _probe_loop(self):
        stream = f"KV_{self._bucket}"
        try:
            while not self._stopped:
                try:
                    si = await self._js.stream_info(stream)
                    created = si.created
                    if self._created is None:
                        self._created = created
                    elif created != self._created:
                        break  # deleted and recreated: its revisions started over
                except NotFoundError:
                    break
                except Exception:
                    pass  # a blip (a reconnect in progress): probe again
                await asyncio.sleep(self.PROBE_EVERY)
        except asyncio.CancelledError:
            return
        if not self._stopped:
            await self._end()

    async def _end(self):
        self._stopped = True
        try:
            await self._w.stop()
        except Exception:
            # The subscription is already gone: wake the iterator ourselves.
            try:
                self._w._updates.put_nowait(self._w.STOP_ITER)
            except Exception:
                pass

    async def stop(self):
        if self._stopped:
            return
        self._probe.cancel()
        await self._end()

    async def updates(self):
        """Yield (key, value, revision, op) per delivery, None once the
        initial values are in. op is KV_PUT, KV_DEL or KV_PURGE (a TTL expiry
        arrives as a purge)."""
        async for e in self._w:
            if e is None:
                yield None
                continue
            yield (e.key, e.value or b"", e.revision, e.operation or KV_PUT)
