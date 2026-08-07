package engine

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// This file implements the GATED BULK TRANSFORM: the single publish path for
// every whole-board state change on a competitive or team board — garbage
// application ("shrink"), line-clear collapse, and the teams elimination
// vacate.
//
// A transform is one atomic batch:
//
//	[ txn register (per-subject CAS — the gate) | changed cells (NoCAS) ]
//
// The txn register's CAS expectation makes the transform exactly-once: when
// two appliers race (teammates applying the same garbage, or a clear racing a
// shrink on the same board), the server atomically rejects the LOSER'S ENTIRE
// batch — nothing of it is stored — and the loser recomputes from a freshly
// fetched consistent snapshot. The cells themselves carry no expectations: a
// raise overrides a victim's in-flight move by design (the move's own
// per-subject CAS then fails against the transform's committed cells, and the
// move is dropped and flashed — the existing discipline).
//
// On a TEAM board the batch additionally guards teammate concurrency:
//
//   - every write to a cell of another player's snapshot piece, and every
//     write to a headroom row (where a teammate's fresh spawn can land after
//     the snapshot), carries per-subject CAS at the snapshot sequence;
//   - every foreign piece the transform leaves completely untouched
//     contributes one expectation "carrier" — a batch message asserting that
//     piece's first cell is still at its snapshot sequence (the batch never
//     writes that cell, so the assertion is legal at any batch position).
//
// Any teammate move/lock/spawn that committed between the snapshot and the
// batch therefore rejects the whole batch, and the recompute sees the
// teammate's new reality — a stale projection can never bury or duplicate a
// mid-flight piece.
type gatedProjection func(pf *game.Playfield, owed GarbageRegister, txn TxnRegister) (rows []game.Row, newTxn TxnRegister, ok bool)

// gatedUpdate pairs a batch message with the cell it writes (isTxn for the
// register message, which has no cell) so write-through can track sequences
// through batch splits.
type gatedUpdate struct {
	u     natspkg.CellUpdate
	pos   game.CellPos
	isTxn bool
}

const (
	// gatedBatchLimit mirrors the server's default max atomic batch size. A
	// gated batch above it is split into a gate head plus NoCAS tail chunks
	// (see publishGatedItems) — only reachable on the largest team boards.
	gatedBatchLimit = 1000
	// gatedTransformMaxAttempts bounds gate-rejection recomputes. Rejections
	// need another writer's transform to have landed in the race window, so
	// contention is self-limiting; the bound only guards pathology.
	gatedTransformMaxAttempts = 6
)

// txnSubject returns the own board's txn register subject.
func (e *Engine) txnSubject() string {
	if e.gameMode == config.ModeTeams {
		return config.TeamTxnSubject(e.gameID, e.teamIdx)
	}
	return config.CompetitiveTxnSubject(e.gameID, e.playerID)
}

// publishGatedTransform runs one bulk transform to completion: project →
// gated atomic batch → on gate rejection, recompute from a fresh server-side
// snapshot and try again. project is called once per attempt — against the
// live replica on the first, against the fetched snapshot on retries — and
// returns ok=false when the transform has nothing left to do (deficit closed,
// rows already cleared by someone else), which ends the transform as a no-op.
//
// locked reports whether the caller already holds e.mu (handleLockIn runs
// under the consumer's lock). Returns true when a batch committed; the
// caller reads the outcome from whatever its last project call captured.
func (e *Engine) publishGatedTransform(ctx context.Context, op string, locked bool, project gatedProjection) bool {
	for attempt := 0; attempt < gatedTransformMaxAttempts; attempt++ {
		if attempt > 0 {
			// Let the winning transform's echo settle before recomputing.
			select {
			case <-time.After(time.Duration(attempt) * 2 * time.Millisecond):
			case <-ctx.Done():
				return false
			}
		}

		var (
			snap   *game.Playfield
			owed   GarbageRegister
			txn    TxnRegister
			txnSeq uint64
		)
		if attempt == 0 {
			if !locked {
				e.mu.Lock()
			}
			snap = e.playfield.Clone()
			owed = GarbageRegister{Total: e.garbageOwed, By: e.garbageOwedBy}
			txn = TxnRegister{Applied: e.txnApplied}
			txnSeq = e.txnSeq
			if !locked {
				e.mu.Unlock()
			}
		} else {
			var err error
			snap, owed, txn, txnSeq, err = e.fetchBoardSnapshot(ctx)
			if err != nil {
				log.Printf("gated %s: refetch: %v", op, err)
				return false
			}
		}

		rows, newTxn, ok := project(snap, owed, txn)
		if !ok {
			return false
		}
		newTxn.Op = op
		newTxn.By = e.playerIdx

		changed := changedCells(snap.Rows, rows, 0, snap.Height)
		if len(changed) == 0 && newTxn.Applied == txn.Applied {
			return false // nothing to write and nothing to record
		}
		items, err := e.buildGatedItems(snap, changed, newTxn, txnSeq)
		if err != nil {
			log.Printf("gated %s: build batch: %v", op, err)
			return false
		}

		if hook := e.testHookBeforeGatedCommit; hook != nil {
			hook(op)
		}

		committed, retry := e.publishGatedItems(ctx, op, items, changed, newTxn, locked)
		if committed {
			return true
		}
		if !retry {
			return false
		}
	}
	log.Printf("gated %s: gave up after %d attempts", op, gatedTransformMaxAttempts)
	return false
}

// fetchBoardSnapshot fetches a consistent server-side snapshot of the own
// board — full cell contents with their sequences plus both registers — for a
// gate-rejection recompute. It builds a detached playfield, so no lock is
// needed (board dimensions are immutable after Start).
func (e *Engine) fetchBoardSnapshot(ctx context.Context) (*game.Playfield, GarbageRegister, TxnRegister, uint64, error) {
	msgs, err := natspkg.FetchPlayfieldState(ctx, e.js, e.gameID, e.snapshotSubjects())
	if err != nil {
		return nil, GarbageRegister{}, TxnRegister{}, 0, err
	}
	pf := game.NewPlayfieldWithHeight(e.playfield.Width, e.playfield.Height)
	var owed GarbageRegister
	var txn TxnRegister
	var txnSeq uint64
	for _, m := range msgs {
		if m.Row < 0 {
			switch {
			case config.IsGarbageSubject(m.Subject):
				_ = json.Unmarshal(m.Payload, &owed)
			case config.IsTxnSubject(m.Subject):
				_ = json.Unmarshal(m.Payload, &txn)
				txnSeq = m.Seq
			}
			continue
		}
		c, uErr := game.UnmarshalCell(m.Payload)
		if uErr != nil {
			return nil, GarbageRegister{}, TxnRegister{}, 0, uErr
		}
		pf.Apply(m.Row, m.Col, c, m.Seq)
	}
	return pf, owed, txn, txnSeq, nil
}

// buildGatedItems assembles the transform batch: the txn register first, then
// the changed cells in orderedCellKeys order (active → locked → empty, so a
// consumer never sees a piece with zero active cells mid-batch), with the
// teammate guards described at the top of the file on team boards.
func (e *Engine) buildGatedItems(snap *game.Playfield, cells map[game.CellPos]game.Cell, newTxn TxnRegister, txnSeq uint64) ([]gatedUpdate, error) {
	txnPayload, err := json.Marshal(newTxn)
	if err != nil {
		return nil, err
	}
	items := make([]gatedUpdate, 0, len(cells)+1)
	items = append(items, gatedUpdate{
		u: natspkg.CellUpdate{
			Subject:       e.txnSubject(),
			Payload:       txnPayload,
			ExpectLastSeq: txnSeq, // ExpectOwnSubject: the gate
		},
		isTxn: true,
	})

	// Teammate guards (team boards only): collect the other players' pieces
	// from the snapshot, split them into "moved by this transform" (any of
	// their cells is rewritten → CAS those writes) and "held untouched" (no
	// cell rewritten → one expectation carrier per piece).
	teams := e.gameMode == config.ModeTeams
	guardedCells := make(map[game.CellPos]bool)
	type heldGuard struct {
		pos game.CellPos
		seq uint64
	}
	var carriers []heldGuard
	if teams {
		byPlayer := make(map[int][]game.CellPos)
		for r := range snap.Rows {
			for c := range snap.Rows[r].Cells {
				cc := snap.Rows[r].Cells[c]
				if cc.Active && cc.PlayerIdx != e.playerIdx {
					byPlayer[cc.PlayerIdx] = append(byPlayer[cc.PlayerIdx], game.CellPos{Row: r, Col: c})
				}
			}
		}
		for _, pieceCells := range byPlayer {
			moved := false
			for _, pos := range pieceCells {
				if _, inDiff := cells[pos]; inDiff {
					moved = true
					break
				}
			}
			if moved {
				for _, pos := range pieceCells {
					guardedCells[pos] = true
				}
			} else {
				pos := pieceCells[0]
				carriers = append(carriers, heldGuard{pos: pos, seq: snap.CellLastSeq(pos.Row, pos.Col)})
			}
		}
	}

	carrierIdx := 0
	for _, k := range orderedCellKeys(cells) {
		data, mErr := cells[k].Marshal()
		if mErr != nil {
			return nil, mErr
		}
		u := natspkg.CellUpdate{
			Subject: e.cellSubject(k.Row, k.Col),
			Payload: data,
			Expect:  natspkg.ExpectNone,
		}
		switch {
		case teams && (guardedCells[k] || k.Row < e.visibleRowStart):
			// Rewriting a moved foreign piece's snapshot cell, or a headroom
			// cell a teammate spawn could have claimed: guard the write.
			u.Expect = natspkg.ExpectOwnSubject
			u.ExpectLastSeq = snap.CellLastSeq(k.Row, k.Col)
		case carrierIdx < len(carriers):
			// Attach a held-piece carrier to this plain write (the batch never
			// writes the asserted subject, so position doesn't matter).
			c := carriers[carrierIdx]
			carrierIdx++
			u.Expect = natspkg.ExpectForSubject
			u.ExpectSubject = e.cellSubject(c.pos.Row, c.pos.Col)
			u.ExpectLastSeq = c.seq
		}
		items = append(items, gatedUpdate{u: u, pos: k})
	}

	// Degenerate case: more held-piece carriers than plain writes to ride on
	// (a tiny diff on a crowded board). Fall back to re-writing one snapshot
	// cell of each remaining held piece with its unchanged content under
	// per-subject CAS — an equivalent guard. Idempotent content, so consumers
	// see no change.
	for ; carrierIdx < len(carriers); carrierIdx++ {
		c := carriers[carrierIdx]
		content := snap.Rows[c.pos.Row].Cells[c.pos.Col]
		data, mErr := content.Marshal()
		if mErr != nil {
			return nil, mErr
		}
		items = append(items, gatedUpdate{
			u: natspkg.CellUpdate{
				Subject:       e.cellSubject(c.pos.Row, c.pos.Col),
				Payload:       data,
				ExpectLastSeq: c.seq, // ExpectOwnSubject
			},
			pos: c.pos,
		})
	}
	return items, nil
}

// publishGatedItems publishes an assembled transform. Batches above the
// server limit are split into a gate head (the txn register, every
// expectation-carrying message, and as many leading plain cells as fit) and
// NoCAS tail chunks — winning the gate excludes concurrent bulk transforms,
// and racing moves are still caught by their own per-subject CAS, so the tail
// stays consistent. Returns (committed, retry): retry is true on a CAS-class
// or transient rejection, false on a hard error.
func (e *Engine) publishGatedItems(ctx context.Context, op string, items []gatedUpdate, cells map[game.CellPos]game.Cell, newTxn TxnRegister, locked bool) (bool, bool) {
	head, tail := splitGatedItems(items)
	updates := make([]natspkg.CellUpdate, len(head))
	for i, it := range head {
		updates[i] = it.u
	}
	t0 := time.Now()
	seq, err := natspkg.PublishMoveAtomically(ctx, e.js, updates)
	if err != nil {
		if errors.Is(err, natspkg.ErrCASFailure) || errors.Is(err, natspkg.ErrBatchTransient) {
			return false, true
		}
		log.Printf("gated %s: publish: %v", op, err)
		return false, false
	}
	e.trackRTT(t0, seq, len(head))
	e.applyGatedItems(head, cells, newTxn, seq, locked)

	// NoCAS tail chunks (oversized boards only — the gate above has already
	// excluded every concurrent bulk transform).
	for start := 0; start < len(tail); start += gatedBatchLimit {
		chunk := tail[start:min(start+gatedBatchLimit, len(tail))]
		chunkUpdates := make([]natspkg.CellUpdate, len(chunk))
		for i, it := range chunk {
			chunkUpdates[i] = it.u
		}
		t0 := time.Now()
		chunkSeq, chunkErr := natspkg.PublishCellsAtomicallyNoCAS(ctx, e.js, chunkUpdates)
		if chunkErr != nil {
			log.Printf("gated %s: tail chunk: %v", op, chunkErr)
			return true, false // the gate batch committed; the transform stands partially — the next transform recomputes from converged state
		}
		e.trackRTT(t0, chunkSeq, len(chunk))
		e.applyGatedItems(chunk, cells, newTxn, chunkSeq, locked)
	}
	return true, false
}

// splitGatedItems returns the transform's gate head and NoCAS tail. Batches
// within the server limit come back whole. Oversized batches keep the txn
// register and EVERY expectation-carrying message in the head (a guard in a
// NoCAS tail would be no guard at all), fill the rest of the head with the
// leading plain cells, and spill the remaining plain cells to the tail —
// relative order preserved on both sides.
func splitGatedItems(items []gatedUpdate) (head, tail []gatedUpdate) {
	if len(items) <= gatedBatchLimit {
		return items, nil
	}
	log.Printf("gated batch of %d messages exceeds the %d-message limit; splitting into gate head + NoCAS tail", len(items), gatedBatchLimit)
	mustKeep := 0
	for _, it := range items {
		if it.isTxn || it.u.Expect != natspkg.ExpectNone {
			mustKeep++
		}
	}
	plainBudget := gatedBatchLimit - mustKeep
	head = make([]gatedUpdate, 0, gatedBatchLimit)
	tail = make([]gatedUpdate, 0, len(items)-gatedBatchLimit)
	for _, it := range items {
		if it.isTxn || it.u.Expect != natspkg.ExpectNone {
			head = append(head, it)
			continue
		}
		if plainBudget > 0 {
			plainBudget--
			head = append(head, it)
		} else {
			tail = append(tail, it)
		}
	}
	return head, tail
}

// applyGatedItems write-throughs one committed gated publish into e.playfield
// (content + the inferred consecutive sequences) and advances the txn mirror
// when the batch carried the register — the same optimistic write-through the
// move paths use, so the later consumer echo of these sequences is a no-op.
func (e *Engine) applyGatedItems(items []gatedUpdate, cells map[game.CellPos]game.Cell, newTxn TxnRegister, commitSeq uint64, locked bool) {
	if commitSeq == 0 || len(items) == 0 {
		return
	}
	if !locked {
		e.mu.Lock()
		defer e.mu.Unlock()
	}
	n := len(items)
	for i, it := range items {
		seq := commitSeq - uint64(n-1-i)
		if it.isTxn {
			if seq > e.txnSeq {
				e.txnSeq = seq
				e.txnApplied = newTxn.Applied
			}
			continue
		}
		if c, ok := cells[it.pos]; ok {
			e.playfield.Apply(it.pos.Row, it.pos.Col, c, seq)
		} else {
			// A carrier-fallback rewrite: same content, higher sequence.
			e.playfield.Apply(it.pos.Row, it.pos.Col, e.playfield.Rows[it.pos.Row].Cells[it.pos.Col], seq)
		}
	}
}
