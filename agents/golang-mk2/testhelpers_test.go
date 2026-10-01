package main

import (
	"context"
	"os"
	"testing"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// sharedTestGame builds a Game on a shared board — the crew's
// (modeCooperative) or team 0's (modeTeams) — of `seats` seats at the
// default extra columns, against a live JetStream server (JETRIS_NATS_URL;
// the caller skips otherwise) with the game's own stream config, and no
// consumers: a test publishes its peers' cells itself and folds them as the
// board consumer would (foldPeerPiece), which keeps it deterministic.
func sharedTestGame(t *testing.T, ctx context.Context, mode, seat, seats int) *Game {
	t.Helper()
	url := os.Getenv("JETRIS_NATS_URL")
	if url == "" {
		t.Skip("set JETRIS_NATS_URL to a JetStream-enabled nats-server to run")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(nc.Close)
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatal(err)
	}
	id := "test-" + randID(8)
	s, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name: gameStreamName(id), Subjects: []string{"jetris.game." + id + ".>"},
		AllowAtomicPublish: true, AllowDirect: true, Storage: jetstream.MemoryStorage,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = js.DeleteStream(context.Background(), gameStreamName(id)) })

	a := &Agent{nc: nc, js: js, name: "me", pub: pubSync,
		listings: map[string]obj{}, invites: map[string]obj{}, streams: map[string]jetstream.Stream{}, stopCh: make(chan struct{})}
	g := newGame(a, id, seat)
	g.mode, g.playerCount, g.seatsOnPF, g.extra, g.stream = mode, seats, seats, minExtraColumns, s
	if mode == modeTeams {
		g.playerCount, g.teamCount, g.team, g.teamSlot = 2*seats, 2, 0, seat
		g.teamScores, g.teamLines = make([]int, 2), make([]int, 2)
	}
	g.w, g.h = sharedWidth(seats, minExtraColumns), sharedHeight(seats, 0)
	g.spawnC = g.spawnColumn(nil)
	g.pub = resolvePublish(pubAuto, true)
	return g
}

// publishCell writes one cell of the board straight to the stream — a
// peer's write, as far as the game under test is concerned — and returns
// its sequence. Nothing is folded: the caller decides what the game sees.
func publishCell(t *testing.T, ctx context.Context, g *Game, at cell, wc *wireCell) uint64 {
	t.Helper()
	ack, err := g.a.js.Publish(ctx, g.cellSubject(at), payloadBytes(wc))
	if err != nil {
		t.Fatal(err)
	}
	g.noteStreamSeq(ack.Sequence)
	return ack.Sequence
}

// foldPeerPiece publishes a peer's falling piece (seat pi) and folds every
// cell as the board consumer would deliver it; returns the cells' sequences.
// Caller holds g.mu.
func foldPeerPiece(t *testing.T, ctx context.Context, g *Game, pi int, p active) map[cell]uint64 {
	t.Helper()
	seqs := map[cell]uint64{}
	for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
		wc := &wireCell{T: p.pt, A: true, R: p.orient, Ar: p.row, Ac: p.col, Pi: pi}
		seq := publishCell(t, ctx, g, c, wc)
		g.foldCell(c, *wc, seq)
		seqs[c] = seq
	}
	return seqs
}

// streamCells is the board as the stream has it, for assertions.
func streamCells(t *testing.T, ctx context.Context, g *Game) map[cell]boardMsg {
	t.Helper()
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
