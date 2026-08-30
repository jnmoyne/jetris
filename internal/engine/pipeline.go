package engine

import (
	"context"
	"errors"
	"log"
	"time"

	"jetris/internal/game"
	natspkg "jetris/internal/nats"
)

// The step pipeline.
//
// A STEP is one move of the falling piece — a shift, a rotation, a soft drop,
// a gravity tick: the writes that move the piece's cells without changing what
// it is or what the stack is. Every step is one atomic batch (new cells, then
// the vacated ones). How the engine commits it is the player's PublishMode:
//
//   - PublishSync (the default, the game as it always was): the step's publish
//     blocks on the commit ack, its committed cells are written through into
//     the acked replica (e.playfield), and only then is the next queued move
//     taken. The engine never has two of a player's batches in flight; a
//     burst of input waits in the move queue, one round trip per move.
//
//   - PublishAsync: the step's commit is SENT and the next queued move is
//     taken at once — the batches pipeline, and the ack is handled when it
//     comes (awaitStep → resolveStep). The engine projects each step from the
//     OPTIMISTIC board, the acked replica with every in-flight step applied
//     on top (projectionBase), so a burst of moves goes out as fast as the
//     moves are made, and the round trip is paid once, not per move.
//
// The catch is CAS. JetStream's per-subject expectation is an EXACT
// sequence, and the sequences a batch is assigned are known only from its
// ack — so a pipelined step cannot carry an expectation about a cell an
// un-acked step already wrote (there is no "at least" expectation, and no
// "batch k committed" expectation either). Such cells go out with NO
// expectation (inflightCells); every other cell keeps its per-subject CAS.
// Which means the pipeline is optimistic in the true sense: when step k
// loses a race, the steps sent behind it were computed on the assumption
// that it had committed, and the ones whose CAS-guarded cells were not
// touched will commit anyway — stray cells, on a false premise. The recovery:
//
//   - resolveStep: the first failure flashes the outline of where the piece
//     wanted to be (the failed step's target) and marks the pipeline BROKEN,
//     remembering the piece as it was before that step (pipeRollback). Every
//     step behind it is poisoned: whether it commits or not, its effect is
//     undone — but its MOVES are not lost: the player moves of every
//     poisoned step are kept, in order (pipeReplay), to be played again.
//     Moves keep queueing meanwhile.
//   - repairPipeline (on runInput, once every in-flight step has resolved):
//     one authoritative batch puts the piece back at pipeRollback and vacates
//     every stray — every cell the episode's steps wrote is re-projected
//     (pipeTouched). Merge-retry on a shared board (never over another
//     player's mid-flight piece), NoCAS on an own board. Then the replay goes
//     back to the HEAD of the move queue, ahead of whatever queued since, and
//     goes out again as one batch — re-projected from the repaired board,
//     with its actual sequences: nothing computed on the lost step's
//     assumptions is ever sent again. A lost race costs the player exactly
//     that move, as a dropped move always did, plus a repair round trip. (A
//     gravity tick is not replayed: the timer keeps ticking.)
//
// Everything that is NOT a step — a hard drop, the lock, a hold, a garbage
// raise, a spawn — is a BARRIER: it settles the pipeline first
// (settlePipeline: wait for the acks, repair if broken), so it is computed
// from converged state with exact expectations. They all run on runInput,
// the engine's single gameplay-write goroutine; the ack goroutines only ever
// touch the pipeline bookkeeping under e.mu.
//
// On a shared board the optimism has one more edge: a poisoned step's
// no-expectation vacate can blank a cell another player's piece moved into
// (why the step ahead of it lost). Pieces are anchor-based — every cell
// carries its piece's anchor — so the owner's next step re-projects the full
// piece and heals the cell; until then it is missing from the board.
//
// PublishOptimistic is the same pipeline with the gap closed by GUESSING:
// instead of no expectation, a cell an un-acked step wrote carries the
// sequence that write is PREDICTED to get. The engine assumes the step ahead
// will not lose its CAS race, and that nothing else lands in the stream
// before it commits: a batch's N messages get N consecutive sequences
// following the stream's last, so the step's sequences are predicted from
// the engine's best knowledge of the stream's end — the highest sequence any
// of its consumers has delivered (streamSeqSeen, every consumer feeds it) or
// the previous in-flight step's predicted last message, whichever is later
// (inflightSeq, pipePredictedEnd). The predictions live here, in the
// pipeline's bookkeeping, never in the replica: the acked replica only ever
// holds ACTUAL sequences — the ack's write-through and the consumer's echoes
// overwrite whatever was guessed the moment they arrive, and a cell's
// expectation comes from the prediction only while its write is in flight.
// A wrong guess (another player's cells, an event, a meta update landing
// first) simply makes the next step's expectations stale: the server rejects
// it, and the loss runs through the same rollback and repair as any lost
// step — a misprediction is a lost race, visibly. Which is the trade: full
// CAS protection while pipelining, at the price of a repair whenever the
// stream did not do what the engine assumed; quiet games (a solo board, a
// slow opponent) predict right nearly always, busy shared streams do not.

// Both pipelined modes bound their depth by COALESCING: with the in-flight
// limit's worth of batches out (InflightLimit), runInput leaves the moves
// in the queue — they keep piling up — and when a slot frees takes the run
// of steps at the queue's head as ONE batch (takeMoveGroup, attemptMoves):
// a burst of moves under a full pipeline goes out as a few multi-move
// batches instead of one batch per move, the round trip's worth of moves at
// a time, and a lost batch costs one rollback however many moves it
// carried. Sync mode coalesces the same way, without a limit to hit: the
// blocking round trip is its hold, and the moves that queued during it go
// out together when it returns. A hard drop and a hold are never grouped:
// they are barriers and stay batches of their own. BufferedBatches shows the
// queue split the way it will go out, for the MOVE BUFFER strip to draw the
// grouped moves as one.

// PublishMode is how the engine commits a step's batch: PublishSync waits for
// the ack before the next move, PublishAsync pipelines the batches with no
// expectation on the cells in flight, PublishOptimistic pipelines them with
// PREDICTED expectations (see the package comment above). The UI toggles it
// while playing (SetPublishMode).
type PublishMode int32

const (
	PublishSync PublishMode = iota
	PublishAsync
	PublishOptimistic
)

func (m PublishMode) String() string {
	switch m {
	case PublishAsync:
		return "async"
	case PublishOptimistic:
		return "optimistic"
	}
	return "sync"
}

// noteStreamSeq folds a delivered message's stream sequence into
// streamSeqSeen (a monotonic high-water mark). Called by tapMsg for every
// message every consumer delivers.
func (e *Engine) noteStreamSeq(seq uint64) {
	for {
		cur := e.streamSeqSeen.Load()
		if seq <= cur || e.streamSeqSeen.CompareAndSwap(cur, seq) {
			return
		}
	}
}

// stepAckTimeout bounds how long an async step waits for its commit ack
// before it is treated as lost (the batch abandoned server-side, the link
// gone): the pipeline then breaks and repairs like after any lost step.
const stepAckTimeout = 10 * time.Second

// The in-flight limit: how many batches a pipelined engine may have in
// flight. At the limit the queued moves wait, and the moves that piled up
// while they waited go out together as one batch. The UI's knob
// (SetInflightLimit) ranges over 1..MaxInflightLimit — its 0 is the sync
// mode, no pipeline at all: at 1 every burst goes out as one batch behind
// the one in flight, and at the top the pipeline all but never fills.
const (
	DefaultInflightLimit = 4
	MaxInflightLimit     = 11
)

// SetInflightLimit sets how many batches may be in flight before the queued
// moves wait and coalesce (clamped to 1..MaxInflightLimit).
func (e *Engine) SetInflightLimit(n int) {
	e.inflightLimit.Store(int32(min(max(n, 1), MaxInflightLimit)))
}

// InflightLimit is the current in-flight limit.
func (e *Engine) InflightLimit() int { return int(e.inflightLimit.Load()) }

// pipelined reports whether steps are being pipelined (async or optimistic).
func (e *Engine) pipelined() bool {
	m := e.PublishMode()
	return m == PublishAsync || m == PublishOptimistic
}

// pipelineFull reports whether the pipelined engine has its limit's worth of
// batches in flight — the queued moves wait (runInput) — and records the
// hold: the moves that pile up behind it go out together.
func (e *Engine) pipelineFull() bool {
	if !e.pipelined() {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.inflight) >= e.InflightLimit() {
		e.pipeHeld = true
		return true
	}
	return false
}

// coalescing reports whether the run of steps at the queue's head goes out
// as one batch. Pipelined: the pipeline is full, or was until a moment ago
// and the moves held behind it have not gone out yet. Sync: always — the
// only thing that ever piles moves up there is the round trip the last
// batch is blocking on, and whatever queued during it goes out together the
// moment it returns, one batch instead of one per move.
func (e *Engine) coalescing() bool {
	if !e.pipelined() {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.inflight) >= e.InflightLimit() || e.pipeHeld
}

// isStep reports whether m moves the piece without changing what it is or
// what the stack is — the moves a batch can carry several of. A hard drop
// and a hold are barriers.
func isStep(m MoveType) bool {
	switch m {
	case MoveLeft, MoveRight, MoveDown, RotateCW, RotateCCW:
		return true
	}
	return false
}

// barrierQueued reports whether a hard drop or a hold is waiting in the
// move queue: the piece's fate is decided, and a full pipeline must not
// keep it waiting — the steps ahead of it go out at once (as one batch) and
// the barrier commits right behind them (runInput).
func (e *Engine) barrierQueued() bool {
	e.bufferedMu.Lock()
	defer e.bufferedMu.Unlock()
	for _, m := range e.bufferedMoves {
		if !isStep(m) {
			return true
		}
	}
	return false
}

// freezePiece marks the piece as being dropped or held (m the barrier being
// committed, or a step to unfreeze): IntentPiece shows it where it stands,
// whatever the queue holds behind the barrier, until the commit has taken
// it off the board (or a hold has swapped it).
func (e *Engine) freezePiece(m MoveType) {
	e.mu.Lock()
	e.pieceFrozen = m
	e.mu.Unlock()
}

// takeMoveGroup removes and returns the queue's next batch worth of moves —
// the run of steps at its head when coalescing, one move otherwise (a
// barrier is always alone) — and whether more are queued behind. Empty when
// the queue is. The moves leave the MOVE BUFFER strip as they are taken.
func (e *Engine) takeMoveGroup(coalesce bool) (moves []MoveType, more bool) {
	e.bufferedMu.Lock()
	n := 0
	if len(e.bufferedMoves) > 0 {
		n = 1
		if coalesce && isStep(e.bufferedMoves[0]) {
			for n < len(e.bufferedMoves) && isStep(e.bufferedMoves[n]) {
				n++
			}
		}
	}
	moves = append([]MoveType(nil), e.bufferedMoves[:n]...)
	e.bufferedMoves = e.bufferedMoves[n:]
	if more = len(e.bufferedMoves) > 0; !more {
		e.bufferedMoves = nil // let a drained burst's backing array go
	}
	e.bufferedMu.Unlock()
	if n > 0 {
		e.batchesTaken.Add(1)
		if coalesce {
			// The held moves went out (as one): the hold is over.
			e.mu.Lock()
			e.pipeHeld = false
			e.mu.Unlock()
		}
		e.emitUpdate(EngineUpdate{Kind: UpdateBufferedMoves})
	}
	return moves, more
}

// BatchesTaken counts the batches of player moves taken off the queue so
// far — the ordinal the next queued batch will have. The MOVE BUFFER strip
// colors its batches by ordinal, the way the NATS messages panel colors
// transactions, so a batch keeps its color as the queue drains.
func (e *Engine) BatchesTaken() int { return int(e.batchesTaken.Load()) }

// BufferedBatches is the move queue (BufferedMoves) split into the batches
// it will go out as, oldest first: runs of steps grouped while the engine
// is coalescing, every move on its own otherwise. The MOVE BUFFER strip
// draws a group's chips as one.
func (e *Engine) BufferedBatches() [][]MoveType {
	return splitBatches(e.BufferedMoves(), e.coalescing())
}

// splitBatches groups a move queue the way takeMoveGroup takes it.
func splitBatches(moves []MoveType, coalesce bool) [][]MoveType {
	var out [][]MoveType
	for i := 0; i < len(moves); {
		n := 1
		if coalesce && isStep(moves[i]) {
			for i+n < len(moves) && isStep(moves[i+n]) {
				n++
			}
		}
		out = append(out, moves[i:i+n])
		i += n
	}
	return out
}

// inflightStep is one step whose batch is in flight: sent, ack pending
// (async), or being awaited inline (sync — registered for the round trip so
// the UI's intent outline covers it). Guarded by e.mu, in e.inflight, oldest
// first.
type inflightStep struct {
	keys  []game.CellPos             // the batch's cells, in batch order
	cells map[game.CellPos]game.Cell // their committed content
	pre   game.Piece                 // the piece before the step: where a lost pipeline rolls back to
	piece game.Piece                 // where the piece wanted to be: the outline that flashes if the step is lost
	moves []MoveType                 // the player moves the step carries — replayed if a step ahead of it is lost (nil for a gravity tick)
	t0    time.Time                  // when the commit was sent (RTT)
	fut   *natspkg.BatchFuture       // the pending ack (nil for a sync step)
	hook  func()                     // test seam: runs before the step resolves
	// The outcome, once the ack is in (resolveStep): folded into the
	// pipeline in SEND order, so a step whose ack goroutine got to the lock
	// first waits here for the steps ahead of it.
	done bool
	seq  uint64
	err  error
}

// SetPublishMode switches how steps are committed from now on. Switching back
// to sync while steps are pipelined is safe: the next sync step settles the
// pipeline first.
func (e *Engine) SetPublishMode(m PublishMode) { e.publishMode.Store(int32(m)) }

// PublishMode reports how steps are currently committed.
func (e *Engine) PublishMode() PublishMode { return PublishMode(e.publishMode.Load()) }

// InflightSteps is how many step batches are in flight right now — sent and
// not yet acked (async), or the one being awaited (sync).
func (e *Engine) InflightSteps() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.inflight)
}

// PipelineBroken reports whether a lost step is being repaired: the piece is
// about to be put back where the stream last agreed it was.
func (e *Engine) PipelineBroken() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.pipeBroken
}

// projectionBase is the board a step is projected from: the acked replica
// with every in-flight step applied on top, in order — the OPTIMISTIC board.
// With nothing in flight it is e.playfield itself, so callers must not
// mutate the result. Call with e.mu held.
func (e *Engine) projectionBase() *game.Playfield {
	if len(e.inflight) == 0 {
		return e.playfield
	}
	pf := e.playfield.Clone()
	for _, s := range e.inflight {
		for _, k := range s.keys {
			pf.Rows[k.Row].Cells[k.Col] = s.cells[k]
		}
	}
	return pf
}

// publishStep commits one step: cells is the batch (the diff against the
// projection base), pre the piece before the step, target where it wants to
// be, moves the player moves it carries (nil for an engine-driven step — a
// gravity tick — which is never replayed). Sync mode is the game's classic
// path — the publish blocks on the ack, a lost CAS race drops the step and
// flashes — wrapped in an in-flight entry for the round trip so the UI's
// intent outline never snaps back mid-flight; async mode pipelines
// (publishStepAsync). Runs on runInput.
func (e *Engine) publishStep(ctx context.Context, cells map[game.CellPos]game.Cell, pre, target game.Piece, moves []MoveType) {
	if len(cells) == 0 {
		return
	}
	internal := moves == nil
	if m := e.PublishMode(); m == PublishAsync || m == PublishOptimistic {
		e.publishStepAsync(ctx, cells, pre, target, moves, m == PublishOptimistic)
		return
	}
	// A sync step behind pipelined ones (the mode was just switched): let
	// them land first, so its expectations are exact.
	e.settlePipeline(ctx)

	step := &inflightStep{keys: orderedCellKeys(cells), cells: cells, pre: pre, piece: target, moves: moves}
	e.mu.Lock()
	e.inflight = append(e.inflight, step)
	e.mu.Unlock()
	if internal && e.sharedBoard() {
		// A gravity tick on a shared board keeps falling under contention:
		// refetch + retry, flashing only if every retry is exhausted.
		e.publishProjectedCellsWithMergeRetry(ctx, cells, pre.Cells(), false)
	} else {
		e.publishProjectedCellsFlash(ctx, cells, pre.Cells(), target.Cells(), false)
	}
	e.mu.Lock()
	e.removeInflight(step)
	e.mu.Unlock()
	e.emitUpdate(EngineUpdate{Kind: UpdateStepAcked})
	e.signalPipe()
}

// publishStepAsync sends the step's batch and returns without waiting for
// the ack: the step is registered in flight FIRST (so the projection base
// and the UI's intent outline include it from the moment it goes out), its
// expectations are built against the acked replica — none for a cell another
// in-flight step wrote, or with predict the sequence that write is predicted
// to get — its own sequences are predicted for the steps behind it, and
// awaitStep picks up the outcome.
func (e *Engine) publishStepAsync(ctx context.Context, cells map[game.CellPos]game.Cell, pre, target game.Piece, moves []MoveType, predict bool) {
	keys := orderedCellKeys(cells)
	step := &inflightStep{keys: keys, cells: cells, pre: pre, piece: target, moves: moves, hook: e.testHookBeforeStepResolve}
	e.mu.Lock()
	updates, err := e.buildStepUpdates(keys, cells, predict)
	if err != nil {
		e.mu.Unlock()
		log.Printf("build step batch: %v", err)
		return
	}
	e.inflight = append(e.inflight, step)
	// The prediction: this batch follows the stream's end as the engine
	// knows it — or the batch in flight ahead of it — with N consecutive
	// sequences, message i getting the (i+1)-th.
	base := max(e.streamSeqSeen.Load(), e.pipePredictedEnd)
	for i, k := range keys {
		e.inflightCells[k]++
		e.pipeTouched[k] = true
		e.inflightSeq[k] = base + uint64(i) + 1
	}
	e.pipePredictedEnd = base + uint64(len(keys))
	e.mu.Unlock()

	if hook := e.testHookBeforeStepSend; hook != nil {
		hook(updates)
	}
	step.t0 = time.Now()
	fut, err := natspkg.PublishMoveAtomicallyAsync(e.js, updates)
	if err != nil {
		// Never left the client (the link is gone): nothing is in flight
		// for it, the step is simply lost — flash it like any dropped step.
		log.Printf("send step batch: %v", err)
		e.mu.Lock()
		e.removeInflight(step)
		for _, k := range keys {
			e.releaseInflightCell(k)
		}
		e.mu.Unlock()
		e.emitCASFlash(pre.Cells(), target.Cells())
		e.signalPipe()
		return
	}
	step.fut = fut
	go e.awaitStep(ctx, step)
}

// buildStepUpdates is buildBatchUpdates for a pipelined step: a cell some
// in-flight step already wrote gets NO expectation — its sequence is not
// known until that step's ack — or, with predict (the optimistic mode), the
// sequence that write is predicted to get (inflightSeq); every other cell
// its per-subject CAS from the acked replica. Call with e.mu held.
func (e *Engine) buildStepUpdates(keys []game.CellPos, cells map[game.CellPos]game.Cell, predict bool) ([]natspkg.CellUpdate, error) {
	updates := make([]natspkg.CellUpdate, 0, len(keys))
	for _, k := range keys {
		data, err := cells[k].Marshal()
		if err != nil {
			return nil, err
		}
		u := natspkg.CellUpdate{Subject: e.cellSubject(k.Row, k.Col), Payload: data}
		switch {
		case e.inflightCells[k] == 0:
			u.ExpectLastSeq = e.playfield.CellLastSeq(k.Row, k.Col)
		case predict:
			u.ExpectLastSeq = e.inflightSeq[k]
		default:
			u.Expect = natspkg.ExpectNone
		}
		updates = append(updates, u)
	}
	return updates, nil
}

// awaitStep waits for an async step's commit ack and resolves it. A stopped
// engine (ctx done) just lets go.
func (e *Engine) awaitStep(ctx context.Context, s *inflightStep) {
	seq, err := s.fut.Wait(ctx, stepAckTimeout)
	if ctx.Err() != nil {
		return
	}
	if s.hook != nil {
		s.hook()
	}
	e.resolveStep(s, seq, err)
}

// resolveStep records an async step's outcome and folds every resolved step
// at the head of the pipeline, in SEND order (the acks arrive in that order
// on the one connection, but their goroutines race for the lock). Committed:
// the step's cells are written through into the acked replica with the
// sequences inferred from the ack (like every sync commit). Lost: the
// pipeline breaks — the first loss of an episode flashes the step's target
// outline and fixes the rollback point at the piece before it; later losses
// (poisoned steps whose own CAS cells were touched too) just resolve. Once
// the last in-flight step has resolved, a broken pipeline asks runInput for
// the repair.
func (e *Engine) resolveStep(s *inflightStep, seq uint64, err error) {
	var flashPre, flashTarget [][2]int
	var landed []*inflightStep
	e.mu.Lock()
	s.done, s.seq, s.err = true, seq, err
	for len(e.inflight) > 0 && e.inflight[0].done {
		x := e.inflight[0]
		e.inflight = e.inflight[1:]
		// Behind a lost step: whatever this one did is undone by the repair,
		// and its moves play again from where the repair leaves the piece.
		poisoned := e.pipeBroken
		if poisoned {
			e.pipeReplay = append(e.pipeReplay, x.moves...)
		}
		for _, k := range x.keys {
			e.releaseInflightCell(k)
		}
		if x.err == nil {
			// Committed — a poisoned one too: its cells are the stream's
			// truth until the repair rewrites them.
			n := len(x.keys)
			for i, k := range x.keys {
				e.playfield.Apply(k.Row, k.Col, x.cells[k], x.seq-uint64(n-1-i))
			}
			landed = append(landed, x)
			continue
		}
		if !poisoned {
			// The loss itself: its move is the one the player pays for.
			e.pipeBroken = true
			e.pipeRollback = x.pre
			flashPre, flashTarget = x.pre.Cells(), x.piece.Cells()
		}
		if !errors.Is(x.err, natspkg.ErrCASFailure) {
			log.Printf("engine %s: step commit: %v", e.playerID, x.err)
		}
	}
	idle, broken := len(e.inflight) == 0, e.pipeBroken
	if idle {
		// Nothing in flight: the next step predicts from the stream's end as
		// delivered, not from a guess that is now history (or never was).
		// A hold with nothing behind it is over too — one with moves still
		// waiting is not: they go out together even if every ack came in
		// before runInput got to them.
		e.pipePredictedEnd = 0
		e.bufferedMu.Lock()
		if len(e.bufferedMoves) == 0 {
			e.pipeHeld = false
		}
		e.bufferedMu.Unlock()
	}
	if idle && !broken {
		e.pipeTouched = make(map[game.CellPos]bool) // every step landed: nothing to repair
	}
	e.mu.Unlock()

	for _, x := range landed {
		e.trackRTT(x.t0, x.seq, len(x.keys))
	}
	if flashTarget != nil {
		e.emitCASFlash(flashPre, flashTarget)
	}
	if len(landed) > 0 {
		e.emitUpdate(EngineUpdate{Kind: UpdateStepAcked})
	}
	if idle && broken {
		select {
		case e.pipeRepair <- struct{}{}:
		default:
		}
	}
	e.signalPipe()
	// A slot freed: the moves that waited on a full pipeline may go out now.
	e.signalMoves()
}

// removeInflight drops s from the in-flight list. Call with e.mu held.
func (e *Engine) removeInflight(s *inflightStep) {
	for i, x := range e.inflight {
		if x == s {
			e.inflight = append(e.inflight[:i:i], e.inflight[i+1:]...)
			return
		}
	}
}

// releaseInflightCell counts one in-flight write to k down; the last one
// out takes the cell's predicted sequence with it — from then on the cell's
// expectation is the replica's actual sequence. Call with e.mu held.
func (e *Engine) releaseInflightCell(k game.CellPos) {
	if e.inflightCells[k]--; e.inflightCells[k] <= 0 {
		delete(e.inflightCells, k)
		delete(e.inflightSeq, k)
	}
}

// signalPipe wakes a settlePipeline waiting on the pipeline to change.
func (e *Engine) signalPipe() {
	select {
	case e.pipeChanged <- struct{}{}:
	default:
	}
}

// settlePipeline is the barrier: it returns once no step is in flight and,
// if a step was lost meanwhile, the repair has been published — so the
// caller's write (a hard drop, the lock, a hold, a garbage raise) is computed
// from converged state with exact expectations. Reports whether a repair
// was needed: the moves pipelined behind the loss are then back in the
// queue, and a write that belongs AFTER them (the lock, a barrier) must
// yield to them. Runs on runInput only.
func (e *Engine) settlePipeline(ctx context.Context) (repaired bool) {
	for {
		e.mu.Lock()
		idle, broken := len(e.inflight) == 0, e.pipeBroken
		e.mu.Unlock()
		if idle {
			if broken {
				e.repairPipeline(ctx)
			}
			return broken
		}
		select {
		case <-e.pipeChanged:
		case <-ctx.Done():
			return false
		}
	}
}

// settleBarrier is settlePipeline for a barrier move taken off the queue —
// a hard drop, a hold. If the pipeline had to be repaired, the moves
// replayed behind the loss are this piece's and belong BEFORE the barrier:
// it goes back into the queue right behind them (requeueAfterReplay) and
// settleBarrier reports deferred — the caller returns without committing,
// and the barrier is taken again once the replay has gone out.
func (e *Engine) settleBarrier(ctx context.Context, m MoveType) (deferred bool) {
	if !e.settlePipeline(ctx) {
		return false
	}
	e.requeueAfterReplay(m)
	return true
}

// requeueAfterReplay puts a barrier back into the move queue right behind
// the moves the last repair replayed (ahead of whatever queued since), and
// wakes runInput.
func (e *Engine) requeueAfterReplay(m MoveType) {
	e.mu.Lock()
	n := e.lastReplayLen
	e.mu.Unlock()
	e.bufferedMu.Lock()
	n = min(n, len(e.bufferedMoves))
	q := make([]MoveType, 0, len(e.bufferedMoves)+1)
	q = append(q, e.bufferedMoves[:n]...)
	q = append(q, m)
	q = append(q, e.bufferedMoves[n:]...)
	e.bufferedMoves = q
	e.bufferedMu.Unlock()
	e.emitUpdate(EngineUpdate{Kind: UpdateBufferedMoves})
	e.signalMoves()
}

// PendingDrop reports a hard drop the player has made that has not landed
// yet — queued behind the steps ahead of it, or being committed — and where
// the piece is going to land: the hard-drop destination of the piece as it
// stands with those steps applied (IntentPiece). False while a lost step is
// being repaired (the drop yields to the replay and is recomputed from the
// repaired board). Position 3 of the display draws the piece as dropped
// there at once.
func (e *Engine) PendingDrop() (game.Piece, bool) {
	e.bufferedMu.Lock()
	queued := false
	for _, m := range e.bufferedMoves {
		if !isStep(m) {
			queued = m == MoveHardDrop
			break
		}
	}
	e.bufferedMu.Unlock()
	e.mu.Lock()
	committing, broken := e.pieceFrozen == MoveHardDrop, e.pipeBroken
	e.mu.Unlock()
	if broken || (!queued && !committing) {
		return game.Piece{}, false
	}
	p, ok := e.IntentPiece()
	if !ok {
		return game.Piece{}, false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	base := e.projectionBase()
	if e.sharedBoard() {
		return game.HardDropDestinationCoop(p, base, e.playerIdx), true
	}
	return game.HardDropDestination(p, base), true
}

// settleIfBroken settles the pipeline only if a step was lost: the moves
// queued behind the loss wait for the repair, then resume from the restored
// piece. Runs on runInput.
func (e *Engine) settleIfBroken(ctx context.Context) {
	e.mu.Lock()
	broken := e.pipeBroken
	e.mu.Unlock()
	if broken {
		e.settlePipeline(ctx)
	}
}

// repairPipeline publishes the rollback of a broken pipeline (see the file
// comment): the piece back at pipeRollback, every stray the episode's steps
// left vacated — one batch, merge-retry on a shared board, NoCAS on an own
// one. Call with no step in flight, on runInput.
func (e *Engine) repairPipeline(ctx context.Context) {
	e.mu.Lock()
	if !e.pipeBroken || len(e.inflight) > 0 {
		e.mu.Unlock()
		return
	}
	p := e.pipeRollback
	touched := e.pipeTouched
	replay := e.pipeReplay
	e.pipeBroken, e.pipeRollback, e.pipeTouched, e.pipeReplay = false, game.Piece{}, make(map[game.CellPos]bool), nil
	e.inflightSeq, e.pipePredictedEnd = make(map[game.CellPos]uint64), 0 // every guess of the episode is void
	if e.getMode() != ModePlayer {
		e.mu.Unlock()
		return
	}
	rows := make(map[int]bool, 8)
	for k := range touched {
		rows[k.Row] = true
	}
	for _, c := range p.Cells() {
		rows[c[0]] = true
	}
	affected := make([]int, 0, len(rows))
	for r := range rows {
		affected = append(affected, r)
	}
	projected := e.playfield.ProjectMove(affected, &p, e.playerIdx)
	cells := diffCells(e.playfield.Rows, projected)
	shared := e.sharedBoard()
	e.mu.Unlock()

	if len(cells) > 0 {
		if shared {
			e.publishProjectedCellsWithMergeRetry(ctx, cells, nil, false)
		} else {
			e.publishProjectedCellsNoCAS(ctx, cells, false)
		}
		log.Printf("engine %s: pipeline repaired: piece back at (%d,%d), %d cells", e.playerID, p.Row, p.Col, len(cells))
	}
	e.mu.Lock()
	e.lastReplayLen = len(replay)
	e.mu.Unlock()
	if len(replay) > 0 {
		// The moves pipelined behind the loss go back to the head of the
		// queue — ahead of whatever the player queued since — and out again
		// as one batch, projected from the board as just repaired.
		e.bufferedMu.Lock()
		e.bufferedMoves = append(append(make([]MoveType, 0, len(replay)+len(e.bufferedMoves)), replay...), e.bufferedMoves...)
		e.bufferedMu.Unlock()
		e.mu.Lock()
		e.pipeHeld = true
		e.mu.Unlock()
		e.emitUpdate(EngineUpdate{Kind: UpdateBufferedMoves})
		e.signalMoves()
	}
	e.emitUpdate(EngineUpdate{Kind: UpdateStepAcked})
}

// EchoSnapshot is the board as the CONSUMER has delivered it so far — the
// stream's echo only, none of the engine's own write-through — for a UI that
// shows the player nothing before it has round-tripped. Before Start it is
// the same board as Snapshot.
func (e *Engine) EchoSnapshot() BoardSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	pf := e.echoField
	if pf == nil {
		pf = e.playfield
	}
	return BoardSnapshot{
		Width:        pf.Width,
		Height:       pf.Height,
		VisibleStart: e.visibleRowStart,
		Rows:         game.CloneRows(pf.Rows),
	}
}

// IntentPiece is where the player's piece is headed: its position on the
// optimistic board (every in-flight step applied) with the moves still
// queued behind them played out on top, each as the engine will play it —
// a blocked shift is a no-op; a hard drop FREEZES the piece where it stands
// (it is going to be dropped from there, its ghost marks the landing) and
// ends the look-ahead, the moves after it being the next piece's; a hold
// ends it too. The freeze holds while the drop (or hold) is being committed
// (pieceFrozen), until the piece has locked and is gone. While a lost step
// is being repaired it is the rollback point — the piece is going back where
// the stream last agreed it was, and the moves queued meanwhile play on from
// there once it is back. False with no piece on the board. The UI's
// pre-rendered move.
func (e *Engine) IntentPiece() (game.Piece, bool) {
	moves := e.BufferedMoves()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pipeBroken {
		return e.pipeRollback, true
	}
	base := e.projectionBase()
	p := base.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		return game.Piece{}, false
	}
	cur := *p
	if !isStep(e.pieceFrozen) {
		return cur, true // a drop or a hold is being committed from here
	}
	shared := e.sharedBoard()
	for _, m := range moves {
		next, ok, last := stepPiece(cur, m, base, shared, e.playerIdx)
		if ok {
			cur = next
		}
		if last {
			break
		}
	}
	return cur, true
}

// AckedPiece is the player's piece as the ACKS have it: its position on the
// acked replica — or, while a lost step is being repaired, the rollback
// point the repair puts it back at (the replica may hold a poisoned step's
// strays meanwhile). False with no piece on the board. The UI's "acks
// outlined" display marks it and moves it only as commits ack.
func (e *Engine) AckedPiece() (game.Piece, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pipeBroken {
		return e.pipeRollback, true
	}
	if p := e.playfield.ActivePieceForPlayer(e.playerIdx); p != nil {
		return *p, true
	}
	return game.Piece{}, false
}

// stepPiece plays one move on p against pf with the engine's rules: the
// piece after it, whether it moved, and whether it ends the look-ahead (a
// hard drop lands the piece, a hold swaps it out).
func stepPiece(p game.Piece, m MoveType, pf *game.Playfield, shared bool, playerIdx int) (next game.Piece, ok, last bool) {
	canPlace := func(q game.Piece) bool {
		if shared {
			return game.CanPlaceCoop(q, pf, playerIdx)
		}
		return game.CanPlace(q, pf)
	}
	rotate := func(q game.Piece, cw bool) (game.Piece, bool) {
		if shared {
			return game.RotateCoop(q, cw, pf, playerIdx)
		}
		return game.Rotate(q, cw, pf)
	}
	switch m {
	case MoveLeft:
		next = p
		next.Col--
		return next, canPlace(next), false
	case MoveRight:
		next = p
		next.Col++
		return next, canPlace(next), false
	case MoveDown:
		next = p
		next.Row++
		return next, canPlace(next), false
	case RotateCW, RotateCCW:
		next, ok = rotate(p, m == RotateCW)
		return next, ok, false
	case MoveHardDrop:
		// The piece is going to be dropped from HERE: it stays put for the
		// look-ahead (its ghost marks the landing) and nothing behind the
		// drop moves it — those moves are the next piece's.
		return p, false, true
	case MoveHold:
		return p, false, true
	}
	return p, false, false
}

// SeedActivePiece puts p on the board as this player's falling piece, on the
// local replicas only — nothing is published. A seam for the UI's layout
// tests and previews, which run transport-less engines (New with a nil
// JetStream, never started) and need a piece to draw and steer.
func (e *Engine) SeedActivePiece(p game.Piece) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.playfield.SetActivePieceForPlayer(p, e.playerIdx)
	if e.echoField != nil {
		e.echoField.SetActivePieceForPlayer(p, e.playerIdx)
	}
}
