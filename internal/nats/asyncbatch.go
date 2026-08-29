package nats

import (
	"context"
	"fmt"
	"strconv"
	"time"

	natsclient "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nuid"
	"github.com/synadia-io/orbit.go/jetstreamext"
)

// BatchFuture is an atomic batch whose commit has been SENT but whose ack has
// not been waited for — the pipelined counterpart of PublishMoveAtomically's
// return value. Wait resolves it.
type BatchFuture struct {
	fut jetstream.PubAckFuture
}

// PublishMoveAtomicallyAsync stages updates as one atomic batch and sends its
// commit WITHOUT waiting for the ack, so the caller can publish its next batch
// right away and handle this one's outcome later (Wait). Everything goes out
// on the calling goroutine — the staged messages as plain core publishes
// carrying the batch headers, the commit through the JetStream context's
// async publisher (a reply inbox it multiplexes) — so the order of commits on
// the wire is the order of the calls: the server applies batch k before it
// looks at batch k+1.
//
// The expectations are the same per-message CAS options PublishMoveAtomically
// uses (see ExpectMode). A caller pipelining batches that touch the same
// cells must know that the sequences batch k is assigned are unknown until
// its ack: an expectation about a cell batch k wrote can only be ExpectNone
// in batch k+1 — the engine's pipeline is built around exactly that.
func PublishMoveAtomicallyAsync(js jetstream.JetStream, updates []CellUpdate) (*BatchFuture, error) {
	if len(updates) == 0 {
		return nil, fmt.Errorf("empty batch")
	}
	nc := js.Conn()
	id := nuid.Next()
	for i, u := range updates[:len(updates)-1] {
		if err := nc.PublishMsg(batchMsg(u, id, i+1, false)); err != nil {
			return nil, err
		}
	}
	fut, err := js.PublishMsgAsync(batchMsg(updates[len(updates)-1], id, len(updates), true))
	if err != nil {
		return nil, classifyPublishErr(err)
	}
	return &BatchFuture{fut: fut}, nil
}

// batchMsg builds message seq (1-based) of batch id from u: the cell payload,
// the update's expectation headers and the atomic-batch headers, the commit
// marker on the last one.
func batchMsg(u CellUpdate, id string, seq int, commit bool) *natsclient.Msg {
	h := natsclient.Header{}
	switch u.Expect {
	case ExpectNone:
	case ExpectForSubject:
		h.Set(jetstream.ExpectedLastSubjSeqSubjHeader, u.ExpectSubject)
		h.Set(jetstream.ExpectedLastSubjSeqHeader, strconv.FormatUint(u.ExpectLastSeq, 10))
	default:
		h.Set(jetstream.ExpectedLastSubjSeqHeader, strconv.FormatUint(u.ExpectLastSeq, 10))
	}
	h.Set(jetstreamext.BatchIDHeader, id)
	h.Set(jetstreamext.BatchSeqHeader, strconv.Itoa(seq))
	if commit {
		h.Set(jetstreamext.BatchCommitHeader, "1")
	}
	return &natsclient.Msg{Subject: u.Subject, Data: u.Payload, Header: h}
}

// Wait blocks until the commit is acknowledged and returns the ack's stream
// sequence — the LAST message's, the batch's N messages having consecutive
// sequences, exactly as PublishMoveAtomically reports it — or the classified
// error: ErrCASFailure for a lost expectation (the whole batch was rejected),
// ErrBatchTransient when no ack came within timeout (the batch was abandoned
// server-side, or the link went away under it), the context's error when it
// ended first.
func (f *BatchFuture) Wait(ctx context.Context, timeout time.Duration) (uint64, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case ack := <-f.fut.Ok():
		return ack.Sequence, nil
	case err := <-f.fut.Err():
		return 0, classifyPublishErr(err)
	case <-ctx.Done():
		return 0, ctx.Err()
	case <-t.C:
		return 0, fmt.Errorf("%w: no commit ack within %s", ErrBatchTransient, timeout)
	}
}
