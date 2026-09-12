package main

// The batch pipeline (guide §4.3): pipelined publishing of the piece's CAS
// move batches — the walk batches and the gravity ticks. The contract does not
// require awaiting one batch's commit ack before sending the next, so the
// agent's --publish flag picks how a move batch is committed:
//
//   - sync: the classic path (game.go publishBatch) — the publish blocks on
//     the commit ack, one round trip per batch.
//   - async (the default): the batch is SENT and the next move is made at
//     once; a cell an un-acked batch already wrote goes out with NO
//     expectation (its sequence is unknown until that ack), every other cell
//     with its exact per-subject CAS. On the agent's own competitive board it
//     is the only writer between barriers, so nothing can slip into the
//     unguarded window.
//   - optimistic: the same pipeline, with the gap closed by GUESSING — an
//     in-flight cell carries the sequence its write is PREDICTED to get (a
//     batch's N messages take N consecutive stream sequences after the
//     stream's end as the agent knows it, or the batch in flight ahead).
//     Full CAS protection while pipelining, at the price of a repair whenever
//     the stream did not do what the agent assumed (another player's traffic
//     landing first is a lost race, visibly).
//
// A lost pipelined batch (a CAS race, a wrong guess, a missing ack) marks the
// pipeline BROKEN: the batches sent behind it were computed on a false
// premise, so the agent stops, drains the in-flight acks, re-fetches its
// committed board (one multi-subject direct get), vacates whatever stray
// cells the poisoned batches left committed, and re-plans — the same
// drop-and-re-plan discipline a sync CAS loss always followed, at pipeline
// scale. Everything that is not a piece move — the spawn, the lock-in, the
// gated transforms — is a BARRIER: it settles the pipeline first
// (settlePipeline) so its expectations are exact.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// The publish disciplines (--publish).
const (
	pubSync = iota
	pubAsync
	pubOptimistic
)

func parsePublishMode(s string) (int, error) {
	switch s {
	case "sync":
		return pubSync, nil
	case "async":
		return pubAsync, nil
	case "optimistic":
		return pubOptimistic, nil
	}
	return 0, fmt.Errorf("unknown publish mode %q (want sync, async or optimistic)", s)
}

// maxInflightBatches bounds the pipeline's depth: at the limit the next move
// batch waits for a slot, like the human client's in-flight limit.
const maxInflightBatches = 4

// batchAckTimeout bounds how long a pipelined batch waits for its commit ack
// before it is treated as lost (abandoned server-side, or the link gone).
const batchAckTimeout = 10 * time.Second

// noteStreamSeq folds an observed stream sequence into the agent's high-water
// mark of the stream's end — what the optimistic mode predicts from.
func (g *Game) noteStreamSeq(seq uint64) {
	for {
		cur := g.streamSeqSeen.Load()
		if seq <= cur || g.streamSeqSeen.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// publishBatchAsync sends one CAS move batch without waiting for its commit
// ack and reports whether it went out. A cell an un-acked batch already wrote
// carries no expectation (async) or the sequence that write is predicted to
// get (optimistic); every other cell its exact per-subject CAS from g.seqs
// (which holds only acked/echoed truth — predictions live in inflightSeq).
// With the limit's worth of batches in flight it waits for a slot. Call with
// g.mu held; false means the pipeline is broken and the caller must settle.
func (g *Game) publishBatchAsync(ctx context.Context, cells []cellUpd, flashCells []cell) bool {
	orderCells(cells)
	n := len(cells)
	if n == 0 {
		return true
	}
	for g.inflight >= maxInflightBatches && !g.pipeBroken {
		g.pipeCond.Wait()
	}
	if g.pipeBroken || g.isEnded() {
		return false
	}
	predict := g.a.pub == pubOptimistic
	base := max(g.streamSeqSeen.Load(), g.pipePredictedEnd)
	batchID := randID(22)
	keys := make([]cell, n)
	var fut jetstream.PubAckFuture
	for i, u := range cells {
		keys[i] = u.at
		h := nats.Header{}
		if n > 1 {
			h.Set(hBatchID, batchID)
			h.Set(hBatchSeq, fmt.Sprint(i+1))
		}
		switch {
		case g.inflightCells[u.at] > 0:
			if predict {
				h.Set(hExpectLast, fmt.Sprint(g.inflightSeq[u.at]))
			}
		default:
			h.Set(hExpectLast, fmt.Sprint(g.seqs[u.at]))
		}
		msg := &nats.Msg{Subject: g.cellSubject(u.at), Data: payloadBytes(u.c), Header: h}
		var err error
		if i == n-1 {
			if n > 1 {
				h.Set(hBatchCommit, "1")
			}
			fut, err = g.a.js.PublishMsgAsync(msg)
		} else {
			err = g.a.nc.PublishMsg(msg)
		}
		if err != nil {
			// Never left the client: the batch is lost, the pipeline broken
			// (messages before the commit may already be staged server-side).
			log.Printf("pipelined publish: %v", err)
			g.pipeBroken = true
			g.pipeCond.Broadcast()
			return false
		}
	}
	for i, k := range keys {
		g.inflightCells[k]++
		g.inflightSeq[k] = base + uint64(i) + 1
	}
	g.pipePredictedEnd = base + uint64(n)
	g.inflight++
	go g.awaitBatch(ctx, fut, keys, flashCells)
	return true
}

// awaitBatch waits for a pipelined batch's commit ack and folds the outcome
// in: on success the cells' ACTUAL sequences write through (the batch's N
// messages take consecutive sequences ending at the ack's); on a lost CAS
// race, a wrong guess, or a missing ack the pipeline breaks and the flash
// goes out, exactly as a sync loss would flash.
func (g *Game) awaitBatch(ctx context.Context, fut jetstream.PubAckFuture, keys []cell, flashCells []cell) {
	var seq uint64
	var err error
	t := time.NewTimer(batchAckTimeout)
	defer t.Stop()
	select {
	case ack := <-fut.Ok():
		seq = ack.Sequence
	case err = <-fut.Err():
	case <-t.C:
		err = fmt.Errorf("no commit ack within %s", batchAckTimeout)
	case <-ctx.Done():
		err = ctx.Err()
	}
	var flash bool
	g.mu.Lock()
	g.inflight--
	for _, k := range keys {
		if g.inflightCells[k]--; g.inflightCells[k] <= 0 {
			delete(g.inflightCells, k)
			delete(g.inflightSeq, k)
		}
	}
	if err == nil {
		g.noteStreamSeq(seq)
		n := len(keys)
		for i, k := range keys {
			if s := seq - uint64(n-1-i); s > g.seqs[k] {
				g.seqs[k] = s
			}
		}
	} else {
		if !g.pipeBroken {
			g.pipeBroken = true
			flash = isCASConflictErr(err) // the loss itself; later resolutions are the poisoned tail
		}
		if !isCASConflictErr(err) && !errors.Is(err, context.Canceled) {
			log.Printf("pipelined batch: %v", err)
		}
	}
	if g.inflight == 0 {
		// Nothing in flight: the next batch predicts from the stream's end as
		// observed, not from a guess that is now history.
		g.pipePredictedEnd = 0
	}
	g.pipeCond.Broadcast()
	g.mu.Unlock()
	if flash {
		g.flash(flashCells)
	}
}

// isCASConflictErr reports whether a pipelined publish error is a lost
// per-subject expectation (the same 10071/10164 codes pubAck.isCASConflict
// matches on the sync path).
func isCASConflictErr(err error) bool {
	var apiErr *jetstream.APIError
	if errors.As(err, &apiErr) {
		return int(apiErr.ErrorCode) == 10071 || int(apiErr.ErrorCode) == 10164
	}
	return err != nil && strings.Contains(err.Error(), "wrong last")
}

// settlePipeline is the barrier: it returns once no batch is in flight and,
// if one was lost meanwhile, the board has been repaired — so the caller's
// write (a spawn, a lock-in, a gated transform) is computed from converged
// state with exact expectations. Reports whether a repair ran (the caller
// should re-plan). Call with g.mu held.
func (g *Game) settlePipeline(ctx context.Context) (repaired bool) {
	for g.inflight > 0 {
		g.pipeCond.Wait()
	}
	if !g.pipeBroken {
		return false
	}
	g.pipeBroken = false
	g.pipePredictedEnd = 0
	g.repairPipeline(ctx)
	return true
}

// settleForLock settles the pipeline before an authoritative lock-in and
// reports whether the caller must re-plan instead of locking: a lost batch
// was repaired, or the piece is no longer where the lock decision left it (a
// committed transform moved it while the acks drained). Call with g.mu held.
func (g *Game) settleForLock(ctx context.Context, p active) bool {
	if g.settlePipeline(ctx) {
		return true
	}
	return g.piece == nil || *g.piece != p
}

// repairPipeline recovers from a lost pipelined batch: refetch the committed
// board (adopting the stream's truth wholesale, our own piece included) and
// vacate every stray own-piece cell the batches poisoned behind the loss left
// committed — cells carrying our active piece at an anchor the adopted piece
// no longer stands at. CAS on the snapshot's sequences; a conflict refetches
// and tries again (bounded). Call with g.mu held and nothing in flight.
func (g *Game) repairPipeline(ctx context.Context) {
	for attempt := 0; attempt < 3; attempt++ {
		snap, err := g.fetchBoard(ctx)
		if err != nil {
			log.Printf("repair: %v", err)
			return
		}
		before := -1
		if g.piece != nil {
			before = g.piece.row
		}
		g.foldSnapshot(snap)
		if g.piece != nil && before > g.piece.row {
			// The stream's truth has the piece higher than the lost
			// batches had taken it: the rows of gravity among them are
			// owed again, at once (execute).
			g.gravityDebt += before - g.piece.row
		}
		var own map[cell]bool
		if g.piece != nil {
			own = cellSet(pieceCells(g.piece.pt, g.piece.orient, g.piece.row, g.piece.col))
		}
		var strays []cellUpd
		for at, m := range snap {
			if m.wc.A && m.wc.Pi == g.idx && !own[at] {
				strays = append(strays, cellUpd{at: at})
			}
		}
		if len(strays) == 0 {
			return
		}
		if err := g.publishBatch(ctx, strays, true); err != nil {
			if errors.Is(err, errCAS) {
				continue
			}
			log.Printf("repair: %v", err)
			return
		}
		log.Printf("pipeline repaired: %d stray cell(s) vacated", len(strays))
		return
	}
	log.Print("repair: gave up after 3 attempts — the next resync converges")
}
