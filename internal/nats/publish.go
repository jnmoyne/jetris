package nats

import (
	"context"
	"errors"
	"strings"

	natsclient "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"

	"jetricks/internal/config"
)

// CellUpdate represents a single cell's new state and the CAS expectation. The
// caller supplies the fully-built cell subject — this package is subject-agnostic
// and knows nothing about game modes or players.
type CellUpdate struct {
	Subject       string
	Payload       []byte
	ExpectLastSeq uint64
}

// ErrCASFailure indicates a CAS sequence expectation was not met.
var ErrCASFailure = errors.New("CAS sequence expectation not met")

// PublishMoveAtomically publishes a set of cell updates as an atomic batch with
// per-subject CAS expectations (Nats-Expected-Last-Subject-Sequence). Consumers
// never observe a torn intermediate state — either every cell is committed or
// none is. CAS is enforced per cell subject, so concurrent writes to other
// cells (e.g. another player's piece in cooperative mode) don't cause spurious
// rejections.
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

	batch, err := jetstreamext.NewBatchPublisher(js)
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
		err := batch.AddMsg(msg, jetstreamext.WithBatchExpectLastSequencePerSubject(u.ExpectLastSeq))
		if err != nil {
			_ = batch.Discard()
			if isCASError(err) {
				return 0, ErrCASFailure
			}
			return 0, err
		}
	}

	// Commit with the last update
	last := updates[len(updates)-1]
	commitMsg := &natsclient.Msg{
		Subject: last.Subject,
		Data:    last.Payload,
		Header:  natsclient.Header{},
	}
	ack, err := batch.CommitMsg(ctx, commitMsg, jetstreamext.WithBatchExpectLastSequencePerSubject(last.ExpectLastSeq))
	if err != nil {
		if isCASError(err) {
			return 0, ErrCASFailure
		}
		return 0, err
	}

	return ack.Sequence, nil
}

// PublishCellsAtomicallyNoCAS publishes a set of cell updates as an atomic
// batch without CAS expectations. Used for authoritative state changes (lock,
// hard-drop landing, line-clear, shrink) where the publisher's view is the
// new ground truth and partial writes must not be visible to consumers.
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

	batch, err := jetstreamext.NewBatchPublisher(js)
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
			return 0, err
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
		return 0, err
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
		if isCASError(err) {
			return ErrCASFailure
		}
		return err
	}
	return nil
}

func isCASError(err error) bool {
	if err == nil {
		return false
	}
	// Check for JetStream API error with wrong last sequence
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		return apiErr.ErrorCode == 10071 // wrong last msg seq for subject
	}
	return strings.Contains(err.Error(), "wrong last msg seq")
}
