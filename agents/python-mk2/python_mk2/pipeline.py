"""The batch pipeline (guide §4.3): pipelined publishing of the piece's CAS
move batches, the walk batches and the gravity ticks.

The contract does not require awaiting one batch's commit ack before sending
the next, so the agent's --publish flag picks how a move batch is committed:

  - sync: the classic path (Game.publish_batch): the publish blocks on the
    commit ack, one round trip per batch.
  - async: the batch is SENT and the next move is made at once; a cell an
    un-acked batch already wrote goes out with NO expectation (its sequence
    is unknown until that ack), every other cell with its exact per-subject
    CAS. On the agent's own competitive board it is the only writer between
    barriers, so nothing can slip into the unguarded window, which is why
    `auto`, the default, picks it there and never on a shared board, where a
    teammate contending for that cell within one round trip would be
    overwritten: `auto` plays those sync (resolve_publish).
  - optimistic: the same pipeline, with the gap closed by GUESSING: an
    in-flight cell carries the sequence its write is PREDICTED to get (a
    batch's N messages take N consecutive stream sequences after the
    stream's end as the agent knows it, or the batch in flight ahead). Full
    CAS protection while pipelining, at the price of a repair whenever the
    stream did not do what the agent assumed.

A lost pipelined batch (a CAS race, a wrong guess, a missing ack) marks the
pipeline BROKEN: the batches sent behind it were computed on a false premise,
so the agent stops, drains the in-flight acks, re-fetches its committed board
(one multi-subject direct get), vacates whatever stray cells the poisoned
batches left committed, and re-plans. Everything that is not a piece move
(the spawn, the lock-in, the gated transforms) is a BARRIER: it settles the
pipeline first (settle_pipeline) so its expectations are exact.
"""

import asyncio

from . import aio, jsx
from .consts import PUB_ASYNC, PUB_AUTO, PUB_OPTIMISTIC, PUB_SYNC
from .wire import CASError, CellUpd, log, order_cells, payload_bytes, rand_id

# The pipeline's depth: at the limit the next move batch waits for a slot.
MAX_INFLIGHT_BATCHES = 4

# How long a pipelined batch waits for its commit ack before it is treated as
# lost (abandoned server-side, or the link gone).
BATCH_ACK_TIMEOUT = 10.0


def resolve_publish(pub, shared):
    """The discipline a game is played with: an explicit choice as it is,
    auto as sync on a shared board (every cell there may have another writer,
    so every write of ours carries its exact expectation) and async on a
    private one."""
    if pub != PUB_AUTO:
        return pub
    return PUB_SYNC if shared else PUB_ASYNC


def parse_publish_mode(s):
    modes = {"sync": PUB_SYNC, "async": PUB_ASYNC, "optimistic": PUB_OPTIMISTIC, "auto": PUB_AUTO}
    if s not in modes:
        raise ValueError(f'unknown publish mode "{s}" (want auto, sync, async or optimistic)')
    return modes[s]


class PipelineMixin:
    def note_stream_seq(self, seq):
        """Fold an observed stream sequence into the agent's high-water mark
        of the stream's end: what the optimistic mode predicts from."""
        if seq > self.stream_seq_seen:
            self.stream_seq_seen = seq

    async def publish_batch_async(self, cells, flash_cells):
        """Send one CAS move batch without waiting for its commit ack and
        report whether it went out. A cell an un-acked batch already wrote
        carries no expectation (async) or the sequence that write is
        predicted to get (optimistic); every other cell its exact per-subject
        CAS from self.seqs (which holds only acked/echoed truth: predictions
        live in inflight_seq). With the limit's worth of batches in flight it
        waits for a slot. Call with mu held; False means the pipeline is
        broken and the caller must settle."""
        order_cells(cells)
        n = len(cells)
        if n == 0:
            return True
        while self.inflight >= MAX_INFLIGHT_BATCHES and not self.pipe_broken:
            await self.pipe_cond.wait()
        if self.pipe_broken or self.is_ended():
            return False
        predict = self.pub == PUB_OPTIMISTIC
        base = max(self.stream_seq_seen, self.pipe_predicted_end)
        batch_id = rand_id(22)
        nc = self.a.nc
        keys = [u.at for u in cells]
        inbox = nc.new_inbox()
        try:
            ack_sub = await nc.subscribe(inbox, max_msgs=1)
            for i, u in enumerate(cells):
                h = {}
                if n > 1:
                    h[jsx.H_BATCH_ID] = batch_id
                    h[jsx.H_BATCH_SEQ] = str(i + 1)
                if self.inflight_cells.get(u.at, 0) > 0:
                    if predict:
                        h[jsx.H_EXPECT_LAST] = str(self.inflight_seq.get(u.at, 0))
                else:
                    h[jsx.H_EXPECT_LAST] = str(self.seqs.get(u.at, 0))
                if i == n - 1:
                    if n > 1:
                        h[jsx.H_BATCH_COMMIT] = "1"
                    await nc.publish(self.cell_subject(u.at), payload_bytes(u.c), reply=inbox, headers=h)
                else:
                    await nc.publish(self.cell_subject(u.at), payload_bytes(u.c), headers=h or None)
        except Exception as e:
            # Never left the client: the batch is lost, the pipeline broken
            # (messages before the commit may already be staged server-side).
            log.info("pipelined publish: %s", e)
            self.pipe_broken = True
            self.pipe_cond.notify_all()
            return False
        for i, k in enumerate(keys):
            self.inflight_cells[k] = self.inflight_cells.get(k, 0) + 1
            self.inflight_seq[k] = base + i + 1
        self.pipe_predicted_end = base + n
        self.inflight += 1
        self.note_own_writes(cells)  # the optimistic projection; a lost batch resyncs
        aio.spawn(self.tasks, self.await_batch(ack_sub, keys, flash_cells))
        return True

    async def await_batch(self, ack_sub, keys, flash_cells):
        """Wait for a pipelined batch's commit ack and fold the outcome in:
        on success the cells' ACTUAL sequences write through (the batch's N
        messages take consecutive sequences ending at the ack's); on a lost
        CAS race, a wrong guess, or a missing ack the pipeline breaks and the
        flash goes out, exactly as a sync loss would flash."""
        seq, lost, cas, why = 0, False, False, None
        cancelled = False
        try:
            resp = await ack_sub.next_msg(timeout=BATCH_ACK_TIMEOUT)
            seq, err = jsx.parse_ack(resp.data)
            status = (resp.headers or {}).get("Status")
            if err:
                lost, cas = True, jsx.is_cas_conflict(err)
                if not cas:
                    why = err.get("description", err)
            elif status or seq <= 0:
                # Not an ack at all: a status reply (503, nobody took the
                # commit: the stream is gone or between leaders) or a body
                # without a sequence. The batch is lost.
                lost, why = True, f"no commit ack (status {status or 'none'})"
        except asyncio.CancelledError:
            lost, cancelled = True, True
        except Exception:
            lost, why = True, f"no commit ack within {BATCH_ACK_TIMEOUT:g}s"
        finally:
            try:
                await ack_sub.unsubscribe()
            except Exception:
                pass
        flash = False
        async with self.mu:
            self.inflight -= 1
            for k in keys:
                left = self.inflight_cells.get(k, 0) - 1
                if left <= 0:
                    self.inflight_cells.pop(k, None)
                    self.inflight_seq.pop(k, None)
                else:
                    self.inflight_cells[k] = left
            if not lost:
                self.note_stream_seq(seq)
                n = len(keys)
                for i, k in enumerate(keys):
                    s = seq - (n - 1 - i)
                    if s > self.seqs.get(k, 0):
                        self.seqs[k] = s
            else:
                if not self.pipe_broken:
                    self.pipe_broken = True
                    flash = cas  # the loss itself; later resolutions are the poisoned tail
                if why is not None:
                    log.info("pipelined batch: %s", why)
            if self.inflight == 0:
                # Nothing in flight: the next batch predicts from the
                # stream's end as observed, not from a guess that is now
                # history.
                self.pipe_predicted_end = 0
            self.pipe_cond.notify_all()
        if flash and not cancelled:
            await self.flash(flash_cells)
        if cancelled:
            raise asyncio.CancelledError()

    async def settle_pipeline(self):
        """The barrier: it returns once no batch is in flight and, if one was
        lost meanwhile, the board has been repaired, so the caller's write (a
        spawn, a lock-in, a gated transform) is computed from converged state
        with exact expectations. Reports whether a repair ran (the caller
        should re-plan). Call with mu held."""
        while self.inflight > 0:
            await self.pipe_cond.wait()
        if not self.pipe_broken:
            return False
        self.pipe_broken = False
        self.pipe_predicted_end = 0
        await self.repair_pipeline()
        return True

    async def settle_for_lock(self, p):
        """Settle the pipeline before an authoritative lock-in and report
        whether the caller must re-plan instead of locking: a lost batch was
        repaired, or the piece is no longer where the lock decision left it
        (a committed transform moved it while the acks drained). Call with mu
        held."""
        if await self.settle_pipeline():
            return True
        return self.piece is None or self.piece != p

    async def repair_pipeline(self):
        """Recover from a lost pipelined batch: refetch the committed board
        (adopting the stream's truth wholesale, our own piece included) and
        vacate every stray own-piece cell the batches poisoned behind the
        loss left committed: cells carrying our active piece at an anchor the
        adopted piece no longer stands at. CAS on the snapshot's sequences; a
        conflict refetches and tries again (bounded). Call with mu held and
        nothing in flight."""
        for _ in range(3):
            try:
                snap = await self.fetch_board()
            except Exception as e:
                log.info("repair: %s", e)
                return
            before = self.piece.row if self.piece is not None else -1
            self.fold_snapshot(snap)
            if self.piece is not None and before > self.piece.row:
                # The stream's truth has the piece higher than the lost
                # batches had taken it: the rows of gravity among them are
                # owed again, at once (execute).
                self.gravity_debt += before - self.piece.row
            own = set(self.piece.cells()) if self.piece is not None else ()
            strays = [CellUpd(at) for at, (_, wc) in snap.items() if wc.a and wc.pi == self.idx and at not in own]
            if not strays:
                return
            try:
                await self.publish_batch(strays, True)
            except CASError:
                continue
            except Exception as e:
                log.info("repair: %s", e)
                return
            log.info("pipeline repaired: %d stray cell(s) vacated", len(strays))
            return
        log.info("repair: gave up after 3 attempts, the next resync converges")
