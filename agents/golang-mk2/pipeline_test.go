package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// pipelineTestGame builds a competitive game against a live JetStream server
// (JETRIS_NATS_URL; the caller skips otherwise) with the agent's own stream
// config: atomic publish + direct get, the two features the pipeline rides on.
func pipelineTestGame(t *testing.T, ctx context.Context, pub int) *Game {
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

	a := &Agent{nc: nc, js: js, name: "me", pub: pub}
	g := newGame(a, id, 0)
	g.mode, g.playerCount, g.w, g.spawnC, g.stream = modeCompetitive, 2, width, spawnCol, s
	return g
}

// assertConverged checks the pipeline's invariant after a settle: the stream's
// committed truth and the local replica agree — every active cell on the
// stream belongs to the adopted piece (no strays), the settled cells match,
// and every local sequence is the cell's actual last stream sequence.
func assertConverged(t *testing.T, ctx context.Context, g *Game) {
	t.Helper()
	snap, err := g.fetchBoard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var own map[cell]bool
	if g.piece != nil {
		own = cellSet(pieceCells(g.piece.pt, g.piece.orient, g.piece.row, g.piece.col))
	}
	for at, m := range snap {
		switch {
		case m.wc.A:
			if !own[at] {
				t.Errorf("stray active cell %v on the stream (piece %+v)", at, g.piece)
			}
		case m.wc.O:
			if g.locked[at] != m.wc {
				t.Errorf("locked cell %v: local %+v, stream %+v", at, g.locked[at], m.wc)
			}
		}
		if g.seqs[at] != m.seq {
			t.Errorf("cell %v: local seq %d, stream seq %d", at, g.seqs[at], m.seq)
		}
	}
	for at := range g.locked {
		if !snap[at].wc.O {
			t.Errorf("local locked cell %v missing from the stream", at)
		}
	}
}

// TestPipelinedMoves drives a run of piece moves through the async and
// optimistic pipelines without awaiting the individual acks, settles, and
// checks the stream holds exactly the final position with the write-through
// sequences reconciled to the actual ones.
func TestPipelinedMoves(t *testing.T) {
	for _, pub := range []int{pubAsync, pubOptimistic} {
		name := "async"
		if pub == pubOptimistic {
			name = "optimistic"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			g := pipelineTestGame(t, ctx, pub)

			g.mu.Lock()
			defer g.mu.Unlock()
			p := active{pt: 2, orient: 0, row: 2, col: g.spawnC}
			var seed []cellUpd
			for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
				seed = append(seed, cellUpd{at: c, c: g.activePayload(p)})
			}
			if err := g.publishBatch(ctx, seed, true); err != nil {
				t.Fatal(err)
			}
			g.piece = &p

			// A whole walk, pipelined: rotate, three shifts, two gravity rows.
			steps := []active{
				{p.pt, 1, p.row, p.col},
				{p.pt, 1, p.row, p.col + 1},
				{p.pt, 1, p.row, p.col + 2},
				{p.pt, 1, p.row, p.col + 3},
				{p.pt, 1, p.row + 1, p.col + 3},
				{p.pt, 1, p.row + 2, p.col + 3},
			}
			for _, to := range steps {
				if !g.publishPieceMove(ctx, to) {
					t.Fatalf("pipelined move to %+v reported failure", to)
				}
			}
			if repaired := g.settlePipeline(ctx); repaired {
				t.Fatal("uncontended pipeline should settle without a repair")
			}
			want := steps[len(steps)-1]
			if g.piece == nil || *g.piece != want {
				t.Fatalf("piece: got %+v, want %+v", g.piece, want)
			}
			assertConverged(t, ctx, g)
		})
	}
}

// TestPipelineRepair loses a pipelined batch on purpose — a cell the next
// move's CAS covers is rewritten behind the agent's back — with more batches
// pipelined behind the loss, and checks the settle repairs the board back to
// one consistent piece: stream truth adopted, every stray vacated, sequences
// exact.
func TestPipelineRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	g := pipelineTestGame(t, ctx, pubAsync)

	g.mu.Lock()
	defer g.mu.Unlock()
	p := active{pt: 1, orient: 0, row: 2, col: g.spawnC} // O piece: 2x2, easy geometry
	var seed []cellUpd
	for _, c := range pieceCells(p.pt, p.orient, p.row, p.col) {
		seed = append(seed, cellUpd{at: c, c: g.activePayload(p)})
	}
	if err := g.publishBatch(ctx, seed, true); err != nil {
		t.Fatal(err)
	}
	g.piece = &p

	// Sabotage: the cell the first down-move writes into gets a value the
	// agent has never seen, so the move's expectation (0) loses its CAS.
	sab := cell{p.row + 2, p.col} // O occupies rows p.row..p.row+1
	if _, err := g.a.js.Publish(ctx, g.cellSubject(sab), []byte(`{"o":true,"t":3}`)); err != nil {
		t.Fatal(err)
	}

	// Three pipelined down-moves: the first is lost, the two behind it are
	// poisoned (computed assuming it committed). A move may already observe
	// the break — its false return means the settle+repair ran inside it.
	repaired := false
	for i := 1; i <= 3; i++ {
		to := active{p.pt, p.orient, p.row + i, p.col}
		if !g.publishPieceMove(ctx, to) {
			repaired = true
			break
		}
	}
	if g.settlePipeline(ctx) {
		repaired = true
	}
	if !repaired {
		t.Fatal("a lost pipelined batch must trigger a repair")
	}
	if g.pipeBroken {
		t.Fatal("pipeline still marked broken after settle")
	}
	if g.piece == nil {
		t.Fatal("repair lost the piece entirely")
	}
	assertConverged(t, ctx, g)
}
