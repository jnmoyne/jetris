package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestResyncSharedMultiGet runs resyncShared against a live JetStream server
// (JETRIS_NATS_URL; skipped otherwise). A 2v2 teams board is 600 cells, so the
// fetch takes the multi-chunk path — two multi-subject direct gets bound to one
// stream sequence — and must come back as exactly the committed state: settled
// cells, our own piece, a teammate's piece, a vacated cell, the LAST sequence
// of a cell written twice, nothing from the other team's board, and none of
// the stale local state it replaces.
func TestResyncSharedMultiGet(t *testing.T) {
	url := os.Getenv("JETRIS_NATS_URL")
	if url == "" {
		t.Skip("set JETRIS_NATS_URL to a JetStream-enabled nats-server to run")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := "test-" + randID(8)
	s, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: gameStreamName(id), Subjects: []string{"jetris.game." + id + ".>"},
		AllowDirect: true, Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = js.DeleteStream(context.Background(), gameStreamName(id)) }()

	a := &Agent{nc: nc, js: js, name: "me"}
	g := newGame(a, id, 1)
	g.mode, g.team, g.playerCount, g.w, g.stream = modeTeams, 0, 4, 20, s
	if n := g.height() * g.w; n <= fetchChunk {
		t.Fatalf("board has %d cells; this test wants the multi-chunk path (> %d)", n, fetchChunk)
	}
	pub := func(c cell, payload any) uint64 {
		t.Helper()
		b := []byte("{}")
		if payload != nil {
			b, _ = json.Marshal(payload)
		}
		ack, err := js.Publish(ctx, g.cellSubject(c), b)
		if err != nil {
			t.Fatal(err)
		}
		return ack.Sequence
	}
	// A settled cell rewritten once: the second write must win.
	pub(cell{29, 0}, wireCell{O: true, T: 3})
	lockedSeq := pub(cell{29, 0}, wireCell{O: true, T: 5})
	// A vacated cell: written, then emptied.
	pub(cell{20, 7}, wireCell{O: true, T: 1})
	emptySeq := pub(cell{20, 7}, nil)
	// Our own piece (idx 1) at its spawn.
	mine := active{pt: 2, orient: 0, row: 2, col: 3}
	for _, c := range pieceCells(mine.pt, mine.orient, mine.row, mine.col) {
		pub(c, wireCell{A: true, T: mine.pt, R: mine.orient, Ar: mine.row, Ac: mine.col, Pi: 1})
	}
	// A teammate's piece (idx 0) mid-fall in the other section.
	theirs := active{pt: 4, orient: 1, row: 10, col: 13}
	for _, c := range pieceCells(theirs.pt, theirs.orient, theirs.row, theirs.col) {
		pub(c, wireCell{A: true, T: theirs.pt, R: theirs.orient, Ar: theirs.row, Ac: theirs.col, Pi: 0})
	}
	// The other team's board shares the stream but must not leak in.
	if _, err := js.Publish(ctx, fmt.Sprintf("jetris.game.%s.team.1.playfield.cell.29.0", id), []byte(`{"o":true,"t":1}`)); err != nil {
		t.Fatal(err)
	}

	// Stale local state the resync must replace wholesale.
	g.locked[cell{5, 5}] = wireCell{O: true, T: 1}
	g.seqs[cell{5, 5}] = 999
	g.othersAct[cell{6, 6}] = 3
	g.othersPiece[3] = active{pt: 1}

	g.resyncShared(ctx)

	if g.piece == nil || *g.piece != mine {
		t.Fatalf("own piece: got %+v, want %+v", g.piece, mine)
	}
	if got := g.othersPiece[0]; got != theirs {
		t.Errorf("teammate piece: got %+v, want %+v", got, theirs)
	}
	if len(g.othersPiece) != 1 || len(g.othersAct) != 4 {
		t.Errorf("others: %d piece(s) / %d cell(s), want 1 / 4", len(g.othersPiece), len(g.othersAct))
	}
	if got := g.locked[cell{29, 0}]; !got.O || got.T != 5 {
		t.Errorf("rewritten settled cell: got %+v, want the second write (T=5)", got)
	}
	if len(g.locked) != 1 {
		t.Errorf("locked cells: %v, want only {29 0}", g.locked)
	}
	if g.seqs[cell{29, 0}] != lockedSeq {
		t.Errorf("sequence of the rewritten cell: got %d, want %d", g.seqs[cell{29, 0}], lockedSeq)
	}
	if g.seqs[cell{20, 7}] != emptySeq {
		t.Errorf("sequence of the vacated cell: got %d, want %d", g.seqs[cell{20, 7}], emptySeq)
	}
	if _, stale := g.seqs[cell{5, 5}]; stale {
		t.Error("stale local sequence survived the resync")
	}
}
