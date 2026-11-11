"""A pipelined batch's commit reply, folded without a server: an ack writes
the sequences through, anything else (a lost CAS, a status reply nobody
answered behind, silence) breaks the pipeline for the repair."""

from types import SimpleNamespace

import nats.errors
from helpers import make_game

from python_mk2 import consts


class FakeAck:
    def __init__(self, data=b"", headers=None, silent=False):
        self.msg, self.silent = SimpleNamespace(data=data, headers=headers), silent

    async def next_msg(self, timeout=None):
        if self.silent:
            raise nats.errors.TimeoutError
        return self.msg

    async def unsubscribe(self):
        pass


async def _resolve(ack):
    g = make_game(mode=consts.MODE_COMPETITIVE)
    keys = [(5, 3), (5, 4)]
    g.inflight = 1
    g.inflight_cells = {k: 1 for k in keys}
    g.inflight_seq = {k: 99 for k in keys}
    await g.await_batch(ack, keys, keys)
    assert g.inflight == 0 and not g.inflight_cells and not g.inflight_seq
    return g, keys


async def test_an_ack_writes_the_sequences_through():
    g, keys = await _resolve(FakeAck(b'{"stream":"S","seq":42}'))
    assert not g.pipe_broken
    assert [g.seqs[k] for k in keys] == [41, 42]  # consecutive, ending at the commit's
    assert g.stream_seq_seen == 42 and g.st.cas_losses == 0


async def test_a_lost_cas_breaks_the_pipeline_and_flashes():
    g, _ = await _resolve(FakeAck(b'{"error":{"code":400,"err_code":10071,"description":"wrong last sequence"}}'))
    assert g.pipe_broken and g.st.cas_losses == 1 and not g.seqs


async def test_a_status_reply_is_not_an_ack():
    # Nobody took the commit (the stream gone, or between leaders): the
    # server answers 503 with an empty body, which must not read as success.
    g, _ = await _resolve(FakeAck(b"", {"Status": "503"}))
    assert g.pipe_broken and not g.seqs and g.st.cas_losses == 0


async def test_silence_breaks_the_pipeline():
    g, _ = await _resolve(FakeAck(silent=True))
    assert g.pipe_broken and not g.seqs
