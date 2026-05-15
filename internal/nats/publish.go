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

// RowUpdate represents a single row's new state and the CAS expectation.
type RowUpdate struct {
	Row           int
	PlayerID      string
	Payload       []byte
	ExpectLastSeq uint64
}

// ErrCASFailure indicates a CAS sequence expectation was not met.
var ErrCASFailure = errors.New("CAS sequence expectation not met")

// PublishMoveAtomically publishes a set of row updates as an atomic batch with
// per-subject CAS expectations (Nats-Expected-Last-Subject-Sequence). Consumers
// never observe a torn intermediate state — either every row is committed or
// none is. CAS is enforced per row subject, so concurrent writes to other rows
// (e.g. another player's playfield in cooperative mode) don't cause spurious
// rejections.
func PublishMoveAtomically(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	updates []RowUpdate,
) error {
	if len(updates) == 0 {
		return nil
	}

	batch, err := jetstreamext.NewBatchPublisher(js)
	if err != nil {
		return err
	}

	// Add all rows except the last as batch messages
	for i := 0; i < len(updates)-1; i++ {
		u := updates[i]
		subject := config.RowSubject(gameID, u.PlayerID, u.Row)
		msg := &natsclient.Msg{
			Subject: subject,
			Data:    u.Payload,
			Header:  natsclient.Header{},
		}
		err := batch.AddMsg(msg, jetstreamext.WithBatchExpectLastSequencePerSubject(u.ExpectLastSeq))
		if err != nil {
			_ = batch.Discard()
			if isCASError(err) {
				return ErrCASFailure
			}
			return err
		}
	}

	// Commit with the last update
	last := updates[len(updates)-1]
	subject := config.RowSubject(gameID, last.PlayerID, last.Row)
	commitMsg := &natsclient.Msg{
		Subject: subject,
		Data:    last.Payload,
		Header:  natsclient.Header{},
	}
	_, err = batch.CommitMsg(ctx, commitMsg, jetstreamext.WithBatchExpectLastSequencePerSubject(last.ExpectLastSeq))
	if err != nil {
		if isCASError(err) {
			return ErrCASFailure
		}
		return err
	}

	return nil
}

// PublishRowsAtomicallyNoCAS publishes a set of row updates as an atomic batch
// without CAS expectations. Used for authoritative state changes (lock,
// hard-drop landing, line-clear, shrink) where the publisher's view is the
// new ground truth and partial writes must not be visible to consumers.
func PublishRowsAtomicallyNoCAS(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	updates []RowUpdate,
) error {
	if len(updates) == 0 {
		return nil
	}

	batch, err := jetstreamext.NewBatchPublisher(js)
	if err != nil {
		return err
	}

	for i := 0; i < len(updates)-1; i++ {
		u := updates[i]
		subject := config.RowSubject(gameID, u.PlayerID, u.Row)
		msg := &natsclient.Msg{
			Subject: subject,
			Data:    u.Payload,
			Header:  natsclient.Header{},
		}
		if err := batch.AddMsg(msg); err != nil {
			_ = batch.Discard()
			return err
		}
	}

	last := updates[len(updates)-1]
	subject := config.RowSubject(gameID, last.PlayerID, last.Row)
	commitMsg := &natsclient.Msg{
		Subject: subject,
		Data:    last.Payload,
		Header:  natsclient.Header{},
	}
	_, err = batch.CommitMsg(ctx, commitMsg)
	return err
}

// PublishSingleRow publishes a single row update with CAS.
func PublishSingleRow(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	update RowUpdate,
) error {
	subject := config.RowSubject(gameID, update.PlayerID, update.Row)
	_, err := js.Publish(ctx, subject, update.Payload,
		jetstream.WithExpectLastSequencePerSubject(update.ExpectLastSeq))
	if err != nil {
		if isCASError(err) {
			return ErrCASFailure
		}
		return err
	}
	return nil
}

// PublishSingleRowNoCAS publishes a single row without CAS checks.
// Used for line-clear publishes where the cleared state is authoritative.
func PublishSingleRowNoCAS(
	ctx context.Context,
	js jetstream.JetStream,
	gameID string,
	update RowUpdate,
) (uint64, error) {
	subject := config.RowSubject(gameID, update.PlayerID, update.Row)
	ack, err := js.Publish(ctx, subject, update.Payload)
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
