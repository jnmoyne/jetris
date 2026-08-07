package nats

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nats-io/nats.go/jetstream"

	"jetris/internal/config"
	"jetris/internal/game"
)

// lastSeqFor returns the last message sequence for a subject, or 0 if the
// subject has never been written.
func lastSeqFor(t *testing.T, js jetstream.JetStream, gameID, subject string) uint64 {
	t.Helper()
	s, err := js.Stream(context.Background(), config.GameStream(gameID))
	if err != nil {
		t.Fatal(err)
	}
	msg, err := s.GetLastMsgForSubject(context.Background(), subject)
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return 0
		}
		t.Fatal(err)
	}
	return msg.Sequence
}

// TestGatedBatchMixedExpectations covers the gated-transform batch shape: one
// register message with per-subject CAS (the gate) plus NoCAS cell overwrites.
// A stale gate must reject the WHOLE batch atomically (nothing stored), and a
// committed batch must assign contiguous sequences so the write-through math
// (message i of N -> commitSeq-(N-1-i)) holds.
func TestGatedBatchMixedExpectations(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-gated-batch"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	txn := "jetris.game." + gameID + ".player.p1.playfield.txn"
	cellA := config.CompetitiveCellSubject(gameID, "p1", 5, 0)
	cellB := config.CompetitiveCellSubject(gameID, "p1", 5, 1)
	cellC := config.CompetitiveCellSubject(gameID, "p1", 5, 2)

	// Seed the gate register (expect 0: never written).
	seedSeq, err := PublishMoveAtomically(ctx, js, []CellUpdate{
		{Subject: txn, Payload: []byte(`{"applied":0}`), ExpectLastSeq: 0},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Gated batch: txn CAS + two NoCAS cells. Must commit.
	occupied, _ := game.Cell{Occupied: true, PieceType: game.PieceT}.Marshal()
	commitSeq, err := PublishMoveAtomically(ctx, js, []CellUpdate{
		{Subject: txn, Payload: []byte(`{"applied":1}`), ExpectLastSeq: seedSeq},
		{Subject: cellA, Payload: occupied, Expect: ExpectNone},
		{Subject: cellB, Payload: occupied, Expect: ExpectNone},
	})
	if err != nil {
		t.Fatalf("gated batch should commit: %v", err)
	}

	// Contiguous sequences: message i of N sits at commitSeq-(N-1-i).
	wants := map[string]uint64{txn: commitSeq - 2, cellA: commitSeq - 1, cellB: commitSeq}
	for subj, want := range wants {
		if got := lastSeqFor(t, js, gameID, subj); got != want {
			t.Errorf("subject %s: seq %d, want %d", subj, got, want)
		}
	}

	// Stale gate: reuse the seed sequence after the register moved. The whole
	// batch — including the write to the never-written cellC — must be
	// rejected atomically.
	_, err = PublishMoveAtomically(ctx, js, []CellUpdate{
		{Subject: txn, Payload: []byte(`{"applied":2}`), ExpectLastSeq: seedSeq},
		{Subject: cellC, Payload: occupied, Expect: ExpectNone},
	})
	if !errors.Is(err, ErrCASFailure) {
		t.Fatalf("stale gate should classify as ErrCASFailure, got %v", err)
	}
	if got := lastSeqFor(t, js, gameID, cellC); got != 0 {
		t.Errorf("rejected batch stored cellC at seq %d; nothing must be stored", got)
	}
	if got := lastSeqFor(t, js, gameID, txn); got != commitSeq-2 {
		t.Errorf("rejected batch moved the register to %d", got)
	}
}

// TestForSubjectExpectationGuardsForeignCell covers the teams stale-piece
// guard: a batch message carrying an expectation about ANOTHER subject (a
// teammate's piece cell the batch does not rewrite) must reject the whole
// batch when that subject moved.
func TestForSubjectExpectationGuardsForeignCell(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-forsubject"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	foreign := config.CompetitiveCellSubject(gameID, "p2", 8, 3) // teammate's piece cell
	cellA := config.CompetitiveCellSubject(gameID, "p1", 5, 0)
	cellB := config.CompetitiveCellSubject(gameID, "p1", 5, 1)

	occupied, _ := game.Cell{Occupied: true, PieceType: game.PieceL}.Marshal()
	if _, err := PublishCellsAtomicallyNoCAS(ctx, js, []CellUpdate{{Subject: foreign, Payload: occupied}}); err != nil {
		t.Fatal(err)
	}
	foreignSeq := lastSeqFor(t, js, gameID, foreign)

	// Carrier with the current foreign seq: commits.
	if _, err := PublishMoveAtomically(ctx, js, []CellUpdate{
		{Subject: cellA, Payload: occupied, Expect: ExpectForSubject, ExpectSubject: foreign, ExpectLastSeq: foreignSeq},
		{Subject: cellB, Payload: occupied, Expect: ExpectNone},
	}); err != nil {
		t.Fatalf("carrier with fresh foreign seq should commit: %v", err)
	}
	seqA := lastSeqFor(t, js, gameID, cellA)

	// The teammate "moves": foreign subject advances.
	if _, err := PublishCellsAtomicallyNoCAS(ctx, js, []CellUpdate{{Subject: foreign, Payload: occupied}}); err != nil {
		t.Fatal(err)
	}

	// Stale carrier: whole batch rejected, cellA untouched.
	_, err := PublishMoveAtomically(ctx, js, []CellUpdate{
		{Subject: cellA, Payload: []byte(`{}`), Expect: ExpectForSubject, ExpectSubject: foreign, ExpectLastSeq: foreignSeq},
		{Subject: cellB, Payload: []byte(`{}`), Expect: ExpectNone},
	})
	if !errors.Is(err, ErrCASFailure) {
		t.Fatalf("stale foreign expectation should classify as ErrCASFailure, got %v", err)
	}
	if got := lastSeqFor(t, js, gameID, cellA); got != seqA {
		t.Errorf("rejected batch changed cellA: seq %d, want %d", got, seqA)
	}
}

// TestExpectationAfterInBatchWriteRejected pins the server-side ordering rule:
// an expectation about a subject the SAME batch already wrote is rejected
// (error 10164, classified as a CAS failure). Gated-transform batches must
// therefore order expectation carriers before any write to their subject.
func TestExpectationAfterInBatchWriteRejected(t *testing.T) {
	js := setupJS(t)
	ctx := context.Background()
	gameID := "test-inbatch-order"
	if err := EnsureGameStream(ctx, js, gameID); err != nil {
		t.Fatal(err)
	}

	s := config.CompetitiveCellSubject(gameID, "p1", 4, 4)
	other := config.CompetitiveCellSubject(gameID, "p1", 4, 5)
	occupied, _ := game.Cell{Occupied: true}.Marshal()

	_, err := PublishMoveAtomically(ctx, js, []CellUpdate{
		{Subject: s, Payload: occupied, Expect: ExpectNone},
		{Subject: other, Payload: occupied, Expect: ExpectForSubject, ExpectSubject: s, ExpectLastSeq: 0},
	})
	if !errors.Is(err, ErrCASFailure) {
		t.Fatalf("expectation after in-batch write should be a CAS-class rejection, got %v", err)
	}
	if got := lastSeqFor(t, js, gameID, s); got != 0 {
		t.Errorf("rejected batch stored subject %s at seq %d", s, got)
	}
}

func TestClassifyPublishErr(t *testing.T) {
	cases := []struct {
		name string
		in   error
		want error
	}{
		{"wrong last subject seq", &jetstream.APIError{ErrorCode: 10071}, ErrCASFailure},
		{"wrong last seq constant", &jetstream.APIError{ErrorCode: 10164}, ErrCASFailure},
		{"incomplete batch", &jetstream.APIError{ErrorCode: 10176}, ErrBatchTransient},
		{"atomic too many inflight", &jetstream.APIError{ErrorCode: 10210}, ErrBatchTransient},
		{"batch too many inflight", &jetstream.APIError{ErrorCode: 10211}, ErrBatchTransient},
		{"batch too large", &jetstream.APIError{ErrorCode: 10199}, ErrBatchTooLarge},
		{"string fallback", fmt.Errorf("nats: wrong last msg seq: 42"), ErrCASFailure},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPublishErr(tc.in); !errors.Is(got, tc.want) {
				t.Errorf("classifyPublishErr(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	other := &jetstream.APIError{ErrorCode: 10058}
	if got := classifyPublishErr(other); !errors.As(got, new(*jetstream.APIError)) {
		t.Errorf("unrelated API error should pass through, got %v", got)
	}
	if got := classifyPublishErr(nil); got != nil {
		t.Errorf("nil should stay nil, got %v", got)
	}
}
