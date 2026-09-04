package engine

import (
	"context"
	"log"
	"sort"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// The solo journal.
//
// A one-seat cooperative game — a crew of one, played for the high score —
// has exactly one writer: this engine. Nothing can lose a CAS race on its
// board, so nothing is guarded: no batch carries an expectation, and no
// sequence is tracked to build one. The game is played on the LOCAL board
// (e.playfield), at once — a step, a hard drop, the lock, the clear it
// completes and the next spawn all happen the moment they are made, on
// runInput — and every write goes out behind it as one atomic batch with no
// expectation, sent and not waited for. The stream is a JOURNAL of the game
// played here, in the order it was played (every batch leaves under e.mu, on
// the one connection), and the consumers — the player's own, a spectator's,
// a replay — read the journal back.
//
// Two replicas trail the local board by a round trip, for the display
// positions that show where the acks and the echo have the piece: the acked
// replica (e.ackedField), written through at every ack and kept by the
// consumer's echo exactly as a shared board's replica is, and the echo
// replica (e.echoField). On a shared board the acked replica IS the board
// the game is played on; here they part, and the consumer's echo drives
// nothing — the lock-in it detects on a shared board is detected on the
// local board instead, as the lock is journaled (publishLocal).
//
// The LAB switch keeps its meaning without its guard: the pessimistic mode
// lets one batch out at a time — the next waits for the ack, the moves made
// meanwhile go out together behind it — and the optimistic mode lets every
// batch out the moment it is made, with no in-flight limit at all (the knob
// is not consulted: a deeper pipeline costs nothing that could be lost).
//
// A batch that does not commit is not a lost race — there is none to lose —
// but the link's failure: a send error, or no ack within stepAckTimeout. The
// local board is the truth and is never rolled back; the loss is noted
// (soloLost) and, once nothing is in flight, every cell where the acked
// replica lags the local board is journaled again as one batch
// (resyncLocal). The stream converges on the game, never the other way.

// solo reports whether this engine is the only writer to its game: a
// one-seat cooperative game that it plays. Known from Start, where the seat
// count comes from the meta — a constructed, transport-less engine is never
// solo — and false for a spectator of a solo game, which reads the journal
// like any board.
func (e *Engine) solo() bool {
	return e.gameMode == config.ModeCooperative && e.playerCount == 1 && e.initialMode == ModePlayer
}

// publishLocal commits cells the solo way: to the local board at once, then
// to the stream as one atomic batch with no expectation, sent and not waited
// for — registered in flight so the ack writes it through into the acked
// replica (resolveStep) and the HUD can count it. locked reports whether the
// caller holds e.mu. The batch leaves UNDER e.mu, so the wire carries the
// journal in the order the local board was written; the send never blocks
// on the server. The lock-in — had a piece, has none — is detected here on
// the local board, the edge the consumer detects on a shared board's echo,
// so the lock, the clear it completes and the next spawn journal one behind
// the other. Every board this path serves is the standard width, well
// under the server's batch limit even for a whole-board resync.
func (e *Engine) publishLocal(ctx context.Context, cells map[game.CellPos]game.Cell, locked bool) {
	if len(cells) == 0 {
		return
	}
	keys := orderedCellKeys(cells)
	updates := make([]natspkg.CellUpdate, 0, len(keys))
	for _, k := range keys {
		data, err := cells[k].Marshal()
		if err != nil {
			log.Printf("engine %s: marshal cell (%d,%d): %v", e.playerID, k.Row, k.Col, err)
			return
		}
		updates = append(updates, natspkg.CellUpdate{Subject: e.cellSubject(k.Row, k.Col), Payload: data, Expect: natspkg.ExpectNone})
	}
	if !locked {
		e.mu.Lock()
	}
	// The local board first. It keeps no sequences: an unsequenced write
	// (seq 0) is one Apply takes unconditionally.
	rows := make(map[int]bool, 4)
	for _, k := range keys {
		e.playfield.Apply(k.Row, k.Col, cells[k], 0)
		rows[k.Row] = true
	}
	step := &inflightStep{keys: keys, cells: cells, hook: e.testHookBeforeStepResolve}
	e.inflight = append(e.inflight, step)
	if hook := e.testHookBeforeStepSend; hook != nil {
		hook(updates)
	}
	step.t0 = time.Now()
	fut, err := natspkg.PublishMoveAtomicallyAsync(e.js, updates)
	if err != nil {
		// Never left the client: the link is gone. The local board stands;
		// the stream catches up once the link is back (resyncLocal).
		log.Printf("engine %s: journal batch: %v", e.playerID, err)
		e.removeInflight(step)
		e.soloLost = true
	} else {
		step.fut = fut
		go e.awaitStep(ctx, step)
	}
	// hadActivePiece is cleared BEFORE the lock-in runs: the clear and the
	// spawn it publishes come back through here, and neither is an edge.
	hasActive := e.playfield.ActivePieceForPlayer(e.playerIdx) != nil
	if e.hadActivePiece && !hasActive && e.getMode() == ModePlayer {
		e.hadActivePiece = false
		e.handleLockIn(ctx)
		hasActive = e.playfield.ActivePieceForPlayer(e.playerIdx) != nil
	}
	e.hadActivePiece = hasActive
	if !locked {
		e.mu.Unlock()
	}
	changed := make([]int, 0, len(rows))
	for r := range rows {
		changed = append(changed, r)
	}
	sort.Ints(changed)
	e.emitUpdate(EngineUpdate{Kind: UpdatePlayfield, ChangedRows: changed})
}

// settleLocal is the solo engine's settlePipeline: in the pessimistic mode
// it waits until nothing is in flight — one batch at a time, the next
// behind the last one's ack — and in either mode, once nothing is, it
// journals the local board again if a batch was lost (resyncLocal). Never
// a repair, never a replay: the local board was right all along. Runs on
// runInput.
func (e *Engine) settleLocal(ctx context.Context) {
	for {
		e.mu.Lock()
		idle, lost := len(e.inflight) == 0, e.soloLost
		e.mu.Unlock()
		if idle {
			if lost {
				e.resyncLocal(ctx)
			}
			return
		}
		if e.PublishMode() != PublishSync {
			return // the optimistic journal never waits
		}
		select {
		case <-e.pipeChanged:
		case <-ctx.Done():
			return
		}
	}
}

// resyncLocal journals the local board again after a lost batch: every cell
// where the acked replica lags the local board goes out as one batch, the
// truth restated. Call with nothing in flight — the diff is exactly what
// the stream is missing then — on runInput.
func (e *Engine) resyncLocal(ctx context.Context) {
	e.mu.Lock()
	if !e.soloLost || len(e.inflight) > 0 {
		e.mu.Unlock()
		return
	}
	e.soloLost = false
	cells := changedCells(e.ackedField.Rows, e.playfield.Rows, 0, e.playfield.Height)
	if len(cells) > 0 {
		e.publishLocal(ctx, cells, true)
	}
	e.mu.Unlock()
	log.Printf("engine %s: local board journaled again after a lost batch: %d cells", e.playerID, len(cells))
}

// publishEvent publishes a game event on the game stream. A solo engine
// never waits for it — the event leaves on the same connection, behind the
// cells it announces — while every other engine publishes it synchronously,
// as it always did.
func (e *Engine) publishEvent(ctx context.Context, subject string, data []byte) {
	if e.solo() {
		if _, err := e.js.PublishAsync(subject, data); err != nil {
			log.Printf("engine %s: publish event: %v", e.playerID, err)
		}
		return
	}
	_, _ = e.js.Publish(ctx, subject, data)
}
