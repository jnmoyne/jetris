package nats

import (
	"context"
	"errors"
	"fmt"
	"strings"

	natsclient "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"

	"jetris/internal/config"
)

// ExpectMode selects which CAS expectation (if any) a batch message carries.
// A message can carry at most ONE expectation: about its own subject or about
// another subject (a "carrier" guard for state the batch doesn't rewrite).
type ExpectMode int

const (
	// ExpectOwnSubject asserts ExpectLastSeq against the message's own subject
	// (Nats-Expected-Last-Subject-Sequence). The zero value, so existing
	// callers that fill only Subject/Payload/ExpectLastSeq keep per-cell CAS.
	ExpectOwnSubject ExpectMode = iota
	// ExpectNone carries no expectation — an authoritative overwrite inside a
	// batch whose consistency is guarded by another (gated) message.
	ExpectNone
	// ExpectForSubject asserts ExpectLastSeq against ExpectSubject instead of
	// the message's own subject. The server rejects the whole batch if that
	// subject's last sequence moved — used to guard other players' piece cells
	// without rewriting them. Note the server rejects an expectation about a
	// subject the SAME batch already wrote earlier, so carriers must precede
	// any write to their asserted subject.
	ExpectForSubject
)

// CellUpdate represents a single cell's new state and the CAS expectation. The
// caller supplies the fully-built cell subject — this package is subject-agnostic
// and knows nothing about game modes or players.
type CellUpdate struct {
	Subject       string
	Payload       []byte
	ExpectLastSeq uint64
	Expect        ExpectMode
	ExpectSubject string // required when Expect == ExpectForSubject
}

// Sentinel errors for batch publish outcomes, matched with errors.Is.
var (
	// ErrCASFailure indicates a CAS sequence expectation was not met (the whole
	// atomic batch was rejected; nothing was stored).
	ErrCASFailure = errors.New("CAS sequence expectation not met")
	// ErrBatchTransient indicates the server temporarily refused the batch
	// (incomplete/abandoned batch state or too many inflight batches); the
	// operation can be recomputed and retried.
	ErrBatchTransient = errors.New("transient batch publish failure")
	// ErrBatchTooLarge indicates the batch exceeded the server's atomic batch
	// size limit — a programming error, batches must be pre-chunked.
	ErrBatchTooLarge = errors.New("atomic batch exceeds server limit")
)

// batchNoAckFirst disables the extra first-message ack round trip. Per-message
// acks carry no error information (all expectation checks happen at commit), so
// AckFirst only costs a full RTT per batch. Commit waits on the context.
var batchNoAckFirst = jetstreamext.BatchFlowControl{AckFirst: false}

// batchMsgOpts maps a CellUpdate's expectation mode to batch message options.
func batchMsgOpts(u CellUpdate) []jetstreamext.BatchMsgOpt {
	switch u.Expect {
	case ExpectNone:
		return nil
	case ExpectForSubject:
		return []jetstreamext.BatchMsgOpt{jetstreamext.WithBatchExpectLastSequenceForSubject(u.ExpectLastSeq, u.ExpectSubject)}
	default:
		return []jetstreamext.BatchMsgOpt{jetstreamext.WithBatchExpectLastSequencePerSubject(u.ExpectLastSeq)}
	}
}

// PublishMoveAtomically publishes a set of cell updates as an atomic batch,
// each message carrying the CAS expectation selected by its ExpectMode
// (per-subject CAS by default). Consumers never observe a torn intermediate
// state — either every cell is committed or none is: every expectation is
// checked at commit time and a single failure rejects the whole batch, which
// surfaces here as ErrCASFailure.
//
// Callers must keep a batch within the server's atomic-batch limit (default
// max_batch_size is 1000 messages); the engine chunks larger writes.
//
// On success it returns the commit ack's stream sequence — the sequence assigned
// to the LAST message in the batch. The batch's messages get consecutive stream
// sequences, so the caller can infer every cell's assigned sequence from this and
// the batch order (message i of N → commitSeq-(N-1-i)) and advance its own
// per-subject sequence tracking without waiting for the consumer echo.
func PublishMoveAtomically(
	ctx context.Context,
	js jetstream.JetStream,
	updates []CellUpdate,
) (uint64, error) {
	if len(updates) == 0 {
		return 0, nil
	}

	batch, err := jetstreamext.NewBatchPublisher(js, batchNoAckFirst)
	if err != nil {
		return 0, err
	}

	// Add all cells except the last as batch messages
	for i := 0; i < len(updates)-1; i++ {
		u := updates[i]
		msg := &natsclient.Msg{
			Subject: u.Subject,
			Data:    u.Payload,
			Header:  natsclient.Header{},
		}
		if err := batch.AddMsg(msg, batchMsgOpts(u)...); err != nil {
			_ = batch.Discard()
			return 0, classifyPublishErr(err)
		}
	}

	// Commit with the last update
	last := updates[len(updates)-1]
	commitMsg := &natsclient.Msg{
		Subject: last.Subject,
		Data:    last.Payload,
		Header:  natsclient.Header{},
	}
	ack, err := batch.CommitMsg(ctx, commitMsg, batchMsgOpts(last)...)
	if err != nil {
		return 0, classifyPublishErr(err)
	}

	return ack.Sequence, nil
}

// PublishCellsAtomicallyNoCAS publishes a set of cell updates as an atomic
// batch without CAS expectations. Used for authoritative state changes (lock,
// hard-drop landing, and the NoCAS tail of an oversized gated transform) where
// the publisher's view is the new ground truth and partial writes must not be
// visible to consumers.
//
// Like PublishMoveAtomically it is subject to the server's atomic-batch limit
// (default 1000 messages) and returns the commit ack's stream sequence (the
// last message's sequence) so the caller can advance its own per-subject
// sequence tracking from the inferred consecutive sequences.
func PublishCellsAtomicallyNoCAS(
	ctx context.Context,
	js jetstream.JetStream,
	updates []CellUpdate,
) (uint64, error) {
	if len(updates) == 0 {
		return 0, nil
	}

	batch, err := jetstreamext.NewBatchPublisher(js, batchNoAckFirst)
	if err != nil {
		return 0, err
	}

	for i := 0; i < len(updates)-1; i++ {
		u := updates[i]
		msg := &natsclient.Msg{
			Subject: u.Subject,
			Data:    u.Payload,
			Header:  natsclient.Header{},
		}
		if err := batch.AddMsg(msg); err != nil {
			_ = batch.Discard()
			return 0, classifyPublishErr(err)
		}
	}

	last := updates[len(updates)-1]
	commitMsg := &natsclient.Msg{
		Subject: last.Subject,
		Data:    last.Payload,
		Header:  natsclient.Header{},
	}
	ack, err := batch.CommitMsg(ctx, commitMsg)
	if err != nil {
		return 0, classifyPublishErr(err)
	}
	return ack.Sequence, nil
}

// PublishMeta publishes a game metadata update with a CAS expectation.
func PublishMeta(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	payload []byte,
	expectLastSeq uint64,
) error {
	_, err := js.Publish(ctx, config.MetaSubject(gameID), payload,
		jetstream.WithExpectLastSequencePerSubject(expectLastSeq))
	if err != nil {
		return classifyPublishErr(err)
	}
	return nil
}

// classifyPublishErr maps JetStream publish errors onto the package's sentinel
// errors so callers can branch with errors.Is:
//
//   - 10071 (wrong last sequence for subject) and 10164 (the batch/in-process
//     variant of the same check) → ErrCASFailure: an expectation lost the race,
//     the batch was atomically rejected, recompute from converged state.
//   - 10176 (batch incomplete/abandoned), 10210/10211 (too many inflight
//     batches) → ErrBatchTransient: server-side pressure, retry.
//   - 10199 (batch too large) → ErrBatchTooLarge: a bug — batches are chunked
//     client-side and must never exceed the server limit.
//
// Anything else is returned unchanged.
func classifyPublishErr(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode {
		case 10071, 10164:
			return ErrCASFailure
		case 10176, 10210, 10211:
			return fmt.Errorf("%w: %s", ErrBatchTransient, apiErr.Description)
		case 10199:
			return fmt.Errorf("%w: %s", ErrBatchTooLarge, apiErr.Description)
		}
		return err
	}
	if strings.Contains(err.Error(), "wrong last msg seq") ||
		strings.Contains(err.Error(), "wrong last sequence") {
		return ErrCASFailure
	}
	return err
}
