package nats

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	natsclient "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/synadia-io/orbit.go/jetstreamext"

	"jetris/internal/config"
	"jetris/internal/testutil"
)

// TestPublishMoveAtomicallyAsync: an async batch commits as one atomic batch
// — consecutive sequences, the batch headers on every message, the commit
// ack's sequence the last message's — and a lost expectation rejects it whole,
// classified like the sync publish's.
func TestPublishMoveAtomicallyAsync(t *testing.T) {
	url, _ := testutil.StartServer(t)
	nc, err := natsclient.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	gameID := "async-batch-game"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}
	subj := func(col int) string { return config.CoopCellSubject(gameID, 5, col) }
	updates := []CellUpdate{
		{Subject: subj(0), Payload: []byte("a")},
		{Subject: subj(1), Payload: []byte("b")},
		{Subject: subj(2), Payload: []byte("c")},
	}
	fut, err := PublishMoveAtomicallyAsync(js, updates)
	if err != nil {
		t.Fatal(err)
	}
	seq, err := fut.Wait(ctx, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := js.Stream(ctx, config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	var batchID string
	for i, u := range updates {
		m, err := stream.GetLastMsgForSubject(ctx, u.Subject)
		if err != nil {
			t.Fatal(err)
		}
		if want := seq - uint64(len(updates)-1-i); m.Sequence != want {
			t.Fatalf("%s: seq %d, want %d (consecutive, the commit's last)", u.Subject, m.Sequence, want)
		}
		id := m.Header.Get(jetstreamext.BatchIDHeader)
		if id == "" || (batchID != "" && id != batchID) {
			t.Fatalf("%s: batch id %q, want one shared id", u.Subject, id)
		}
		batchID = id
		if got := m.Header.Get(jetstreamext.BatchSeqHeader); got != fmt.Sprint(i+1) {
			t.Fatalf("%s: batch seq %q, want %d", u.Subject, got, i+1)
		}
	}

	// A stale expectation on one cell rejects the whole batch: nothing stored.
	stale := []CellUpdate{
		{Subject: subj(3), Payload: []byte("d")},
		{Subject: subj(0), Payload: []byte("x"), ExpectLastSeq: seq + 10}, // not col 0's sequence
	}
	fut, err = PublishMoveAtomicallyAsync(js, stale)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fut.Wait(ctx, 5*time.Second); !errors.Is(err, ErrCASFailure) {
		t.Fatalf("stale batch: err = %v, want ErrCASFailure", err)
	}
	if _, err := stream.GetLastMsgForSubject(ctx, subj(3)); err == nil {
		t.Fatal("a rejected batch stored a message")
	}

	// ExpectNone on an already-written cell: no expectation, it commits.
	free := []CellUpdate{
		{Subject: subj(0), Payload: []byte("y"), Expect: ExpectNone},
		{Subject: subj(1), Payload: []byte("z"), ExpectLastSeq: seq - 1},
	}
	fut, err = PublishMoveAtomicallyAsync(js, free)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fut.Wait(ctx, 5*time.Second); err != nil {
		t.Fatalf("free batch: %v", err)
	}
}
