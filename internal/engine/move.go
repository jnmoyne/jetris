package engine

import (
	"context"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
)

// runInput is the engine's single gameplay-write goroutine: it processes player
// input, drives the gravity ticker AND times the lock delay. Running all of it
// on one goroutine is deliberate — a player's own gravity drop, a player move
// and the piece's lock can never publish to their cell subjects concurrently,
// so they can never lose the per-subject CAS race against each other (which
// would drop the step and flash the piece outline). This applies to both
// competitive and cooperative modes.
//
// A blocked downward step (gravity or soft drop) never locks the piece by
// itself: landing arms the lock delay (lockdelay.go) and the piece locks when
// that timer fires, unless a shift or rotation restarted it. Only a hard drop
// locks at once.
func (e *Engine) runInput(ctx context.Context) {
	level := 0
	timer := time.NewTimer(game.GravityInterval(level))
	defer timer.Stop()
	defer e.lockState.disarm()

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.moveReady:
			// The move leaves the queue the moment its processing (and batch
			// publish) starts — that is when its chip leaves the MOVE BUFFER
			// strip. One move per wake-up: the moves behind it re-arm the
			// wake-up, so the lock timer, echoes and gravity get their turn
			// between moves exactly as they did between channel receives.
			//
			// A pipelined engine with its in-flight limit's worth of batches in
			// flight holds the moves in the queue instead, and when a slot
			// frees (resolveStep re-arms the wake-up) takes the run of steps
			// at the queue's head as ONE batch — the moves that piled up
			// while it waited go out together (pipeline.go). Unless a hard
			// drop (or a hold) is waiting: the piece's fate is decided, so the
			// steps ahead of it go out now, as one batch, and the barrier
			// commits right behind them.
			if e.pipelineFull() && !e.barrierQueued() {
				continue
			}
			// The moves queued behind the player's own hard drop, while
			// its lock and the spawn behind it round-trip, are the NEXT
			// piece's — a rotation, a shift, the next drop, pressed a
			// round trip early by a player faster than the wire. They
			// wait in the queue for that piece (spawnPiece wakes the loop
			// once it is on the board) instead of running against a board
			// with no piece on it, where a move is a no-op and lost.
			if e.AwaitingSpawn() {
				continue
			}
			// A lost pipelined step is repaired BEFORE the queue is read: the
			// repair puts the moves pipelined behind the loss back at its head
			// (pipeline.go), and they go out ahead of what queued since.
			if e.getMode() == ModePlayer {
				e.settleIfBroken(ctx)
			}
			moves, more := e.takeMoveGroup(e.coalescing())
			if len(moves) == 0 {
				continue
			}
			if more {
				e.signalMoves()
			}
			if e.getMode() != ModePlayer {
				continue
			}
			// Player input — drop+flash on CAS failure.
			before := e.activePieceSnapshot()
			e.attemptMoves(ctx, moves)
			e.updateLockDelay(before)
		case <-e.pipeRepair:
			// The last in-flight step of a broken pipeline resolved: put the
			// piece back where the stream last agreed it was (pipeline.go).
			if e.getMode() != ModePlayer {
				continue
			}
			e.settlePipeline(ctx)
			e.updateLockDelay(pieceSnapshot{})
		case <-e.lockState.fire:
			// Lock delay expired: lock the piece where it rests (or let it
			// keep falling if the stack changed under it meanwhile).
			e.lockState.disarm()
			if e.getMode() != ModePlayer {
				continue
			}
			e.lockPieceIfGrounded(ctx)
			e.updateLockDelay(pieceSnapshot{})
		case <-e.cellUpdated:
			// An own-board echo: the stack may have changed under the piece
			// — the teammate's piece it was waiting on locked beneath it, a
			// clear collapsed the rows it stood on, a garbage raise met it —
			// so re-time the lock now rather than at the next gravity tick.
			if e.getMode() != ModePlayer {
				continue
			}
			e.updateLockDelay(pieceSnapshot{})
		case <-e.applyGarbage:
			if e.getMode() != ModePlayer {
				continue
			}
			// Apply owed garbage on THIS goroutine: the raise then never
			// races our own move/gravity publishes (they're serialized behind
			// it), and its NoCAS cells override whatever in-flight move a
			// remote writer had — the "raise overrides the move" rule.
			e.applyOwedGarbage(ctx)
		case <-timer.C:
			if e.getMode() != ModePlayer {
				return // became a spectator: stop gravity (and this loop)
			}
			// internal=true: engine-driven gravity tick. In coop this routes to
			// merge-retry on CAS conflict so the piece keeps falling under
			// contention; it flashes only if the tick is ultimately dropped.
			// Serialized with player input above (same goroutine), so it never
			// races our own moves.
			e.settleIfBroken(ctx)
			before := e.activePieceSnapshot()
			_ = e.attemptMove(ctx, MoveDown, true)
			e.updateLockDelay(before)

			// A spawn deferred because another player's active piece covered
			// the spawn cells is retried here, on the same single-write
			// goroutine and at the same cadence the blocker falls at.
			e.retrySpawnIfPending(ctx)

			// A peer's piece left behind — by a player gone from the seat,
			// or one whose piece has not moved in a long while — is vacated
			// here, at the same cadence (roster.go).
			if e.gameStarted.Load() {
				e.vacateIdlePeers(ctx)
			}

			// Garbage backstop: if rows are still owed (a signal was consumed
			// by an attempt that lost its gate, or arrived before runInput
			// started), re-arm the application at gravity cadence.
			if e.gameMode != config.ModeCooperative {
				e.mu.Lock()
				deficit := e.garbageOwed - e.txnApplied
				e.mu.Unlock()
				if deficit > 0 {
					e.signalGarbageApply()
				}
			}

			if e.sharedBoard() {
				if newLevel := game.Level(int(e.totalLines.Load())); newLevel != level {
					level = newLevel
				}
			}
			timer.Reset(game.GravityInterval(level))
		}
	}
}

// attemptMove runs a move. internal=true marks the move as engine-driven (e.g.
// gravity ticks): on CAS failure such moves use merge-retry in coop mode so the
// piece keeps falling under contention. internal=false is for player input and
// uses the drop path. Either way, a step that is ultimately dropped by CAS
// flashes the local player (see emitCASFlash); merge-retry flashes only after
// all retries are exhausted.
func (e *Engine) attemptMove(ctx context.Context, move MoveType, internal bool) error {
	if move == MoveHold {
		// The hold is a swap at the spawn point, not a step of the piece:
		// its own path, the same on every board (attemptHold locks e.mu itself).
		return e.attemptHold(ctx)
	}
	e.mu.Lock()

	if e.sharedBoard() {
		// Coop and teams share the board with other players' active pieces:
		// same collision rules, same merge-retry publish paths.
		return e.attemptMoveCoop(ctx, move, internal)
	}
	return e.attemptMoveStandard(ctx, move, internal)
}

// attemptMoves runs a group of player moves as ONE step: a single move is
// attemptMove; several — the steps that piled up while the pipeline was
// full (takeMoveGroup) — are played out one after another on the optimistic
// board, each by the engine's rules (a blocked step is a no-op, a kicked
// rotation kicks from where the previous step left the piece), and the
// piece's whole journey is published as one batch: the diff between where
// it stood and where the last step leaves it. A lost batch flashes that
// destination and rolls the piece back to where it stood, like any step.
// Groups never contain a hard drop or a hold (those are barriers of their
// own, takeMoveGroup keeps them apart).
func (e *Engine) attemptMoves(ctx context.Context, moves []MoveType) {
	if len(moves) == 1 {
		if !isStep(moves[0]) {
			// A hard drop or a hold: the piece is shown where it stands,
			// its ghost the landing, until the commit has taken it off the
			// board — the keys pressed meanwhile are the next piece's.
			e.freezePiece(moves[0])
			defer e.freezePiece(MoveDown)
		}
		_ = e.attemptMove(ctx, moves[0], false)
		return
	}
	e.mu.Lock()
	base := e.projectionBase()
	p := base.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return
	}
	cur := *p
	shared := e.sharedBoard()
	for _, m := range moves {
		if next, kick, ok, last := stepPieceKick(cur, m, base, shared, e.playerIdx); ok && !last {
			cur = next
			e.noteStep(m, false, kick) // the scoring's account of the piece: a rotation, a shift, a soft drop
		}
	}

	if cur == *p {
		e.mu.Unlock()
		return // every step blocked: nothing to publish
	}
	affected := affectedRowsUnion(p, &cur)
	rows := base.ProjectMove(affected, &cur, e.playerIdx)
	cells := diffCells(base.Rows, rows)
	pre := *p
	e.mu.Unlock()
	e.publishStep(ctx, cells, pre, cur, moves)
}

// affectedRowsUnion returns the union of row indices touched by oldPiece and
// newPiece (either may be nil).
func affectedRowsUnion(oldPiece, newPiece *game.Piece) []int {
	seen := make(map[int]bool, 8)
	if oldPiece != nil {
		for _, c := range oldPiece.Cells() {
			seen[c[0]] = true
		}
	}
	if newPiece != nil {
		for _, c := range newPiece.Cells() {
			seen[c[0]] = true
		}
	}
	out := make([]int, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	return out
}

func (e *Engine) attemptMoveStandard(ctx context.Context, move MoveType, internal bool) error {
	// The step is projected from the optimistic board — the acked replica
	// with every pipelined step applied (pipeline.go); with nothing in
	// flight that is the replica itself.
	base := e.projectionBase()
	p := base.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return nil
	}

	if move == MoveHardDrop {
		e.mu.Unlock()
		return e.publishHardDrop(ctx)
	}

	var newPiece game.Piece
	var valid bool
	var kick int // the SRS kick a rotation used (the scoring's T-spin judgement)

	switch move {
	case MoveLeft:
		newPiece = *p
		newPiece.Col--
		valid = game.CanPlace(newPiece, base)
	case MoveRight:
		newPiece = *p
		newPiece.Col++
		valid = game.CanPlace(newPiece, base)
	case MoveDown:
		newPiece = *p
		newPiece.Row++
		valid = game.CanPlace(newPiece, base)
	case RotateCW:
		newPiece, kick, valid = game.RotateKick(*p, true, base)
	case RotateCCW:
		newPiece, kick, valid = game.RotateKick(*p, false, base)
	}

	if !valid {
		// A blocked step is a no-op. A blocked MoveDown means the piece rests
		// on the stack: locking it is the lock delay's job (lockdelay.go),
		// which runInput re-times right after this move returns.
		e.mu.Unlock()
		return nil
	}
	e.noteStep(move, internal, kick)

	affected := affectedRowsUnion(p, &newPiece)

	rows := base.ProjectMove(affected, &newPiece, e.playerIdx)
	cells := diffCells(base.Rows, rows)
	pre := *p // the piece where it stands: what flashes if the step is dropped by CAS, what a lost pipeline rolls back to
	e.mu.Unlock()
	// In competitive mode each player owns their cell subjects, so this CAS
	// publish cannot race with another player in practice; if it ever does,
	// the dropped step flashes regardless of whether it was a player input or
	// an internal gravity tick. orderedCellKeys writes the new (active) cells
	// before the vacated ones, so the piece never transiently vanishes
	// mid-relocate (single-row horizontal I included). publishStep commits it
	// the way the player's PublishMode says (pipeline.go).
	e.publishStep(ctx, cells, pre, newPiece, playerMoves(move, internal))
	return nil
}

func (e *Engine) attemptMoveCoop(ctx context.Context, move MoveType, internal bool) error {
	base := e.projectionBase() // the optimistic board, see attemptMoveStandard
	p := base.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return nil
	}

	if move == MoveHardDrop {
		e.mu.Unlock()
		return e.publishHardDropCoop(ctx)
	}

	var newPiece game.Piece
	var valid bool
	var kick int // the SRS kick a rotation used (the scoring's T-spin judgement)

	switch move {
	case MoveLeft:
		newPiece = *p
		newPiece.Col--
		valid = game.CanPlaceCoop(newPiece, base, e.playerIdx)
	case MoveRight:
		newPiece = *p
		newPiece.Col++
		valid = game.CanPlaceCoop(newPiece, base, e.playerIdx)
	case MoveDown:
		newPiece = *p
		newPiece.Row++
		valid = game.CanPlaceCoop(newPiece, base, e.playerIdx)
	case RotateCW:
		newPiece, kick, valid = game.RotateCoopKick(*p, true, base, e.playerIdx)
	case RotateCCW:
		newPiece, kick, valid = game.RotateCoopKick(*p, false, base, e.playerIdx)
	}

	if !valid {
		// A blocked step is a no-op. A blocked MoveDown is either the piece
		// resting on the stack — locking it is the lock delay's job
		// (lockdelay.go), re-timed by runInput right after this move — or
		// the piece waiting on another player's active piece below it, an
		// obstacle that will itself fall: gravity simply tries again next
		// tick (updateLockDelay makes the same distinction and does not
		// start the delay for a piece that is only waiting).
		e.mu.Unlock()
		return nil
	}
	e.noteStep(move, internal, kick)

	affected := affectedRowsUnion(p, &newPiece)

	rows := base.ProjectMove(affected, &newPiece, e.playerIdx)
	cells := diffCells(base.Rows, rows)
	pre := *p // what flashes if the step is dropped by CAS, what a lost pipeline rolls back to
	e.mu.Unlock()

	// A gravity tick (engine-driven) must keep falling even when the other
	// player is concurrently writing the same shared cells: in sync mode
	// publishStep gives it merge-retry — refetch+retry on CAS failure,
	// flashing only if every retry is exhausted. A player-initiated move
	// that loses its CAS race (possible in coop when both players touch the
	// same cell) is dropped and flashed; we do not retry — the player retries
	// the input themselves — and we do NOT publish anything to the other
	// players. A pipelined step (async mode) is dropped and rolled back
	// either way (pipeline.go). orderedCellKeys writes the new active cells
	// before the vacated ones, so a single-row (horizontal I) piece never
	// transiently vanishes mid-move and triggers a spurious lock-in.
	e.publishStep(ctx, cells, pre, newPiece, playerMoves(move, internal))
	return nil
}

func (e *Engine) publishHardDrop(ctx context.Context) error {
	// A barrier: the drop lands the piece from where it has been acked to
	// be, after every pipelined step landed — and after the moves a repair
	// replayed, if a step was lost: the drop yields to them (pipeline.go).
	if e.settleBarrier(ctx, MoveHardDrop) {
		return nil
	}
	e.mu.Lock()
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return nil
	}

	dest := game.HardDropDestination(*p, e.playfield)
	e.noteHardDrop(*p, dest, true) // the lock's worth: its T-spin and drop points (award.go)
	affected := affectedRowsUnion(p, &dest)
	rows := e.playfield.ProjectHardDrop(affected, dest, e.playerIdx, true)

	cells := diffCells(e.playfield.Rows, rows)
	e.mu.Unlock()

	// Hard drop landing is authoritative — NoCAS to prevent shrink/move
	// echoes from overwriting it. orderedCellKeys applies the landing cells
	// before the vacated ones, ensuring a line completed by the drop is
	// detected at this lock (not one piece later). See publishProjectedCellsNoCAS.
	e.publishProjectedCellsNoCAS(ctx, cells, false)
	e.noteLockSent(true)
	return nil
}

func (e *Engine) publishHardDropCoop(ctx context.Context) error {
	if e.settleBarrier(ctx, MoveHardDrop) { // a barrier, see publishHardDrop
		return nil
	}
	e.mu.Lock()
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return nil
	}

	dest := game.HardDropDestinationCoop(*p, e.playfield, e.playerIdx)

	// If the cell below the destination is valid ignoring active cells
	// (CanPlace), then dest is touching the OTHER player's active piece, not
	// locked cells/bounds — don't lock, gravity will retry.
	below := dest
	below.Row++
	landedOnActivePiece := game.CanPlace(below, e.playfield)
	e.noteHardDrop(*p, dest, !landedOnActivePiece) // the lock's worth: its T-spin and drop points (award.go)

	affected := affectedRowsUnion(p, &dest)

	rows := e.playfield.ProjectHardDrop(affected, dest, e.playerIdx, !landedOnActivePiece)
	cells := diffCells(e.playfield.Rows, rows)
	flashCells := p.Cells()
	e.mu.Unlock()

	// Coop shares cell subjects: CAS+merge-retry so this drop can't clobber the
	// other player's mid-flight piece with our stale view. orderedCellKeys
	// applies the landing cells before the vacated ones so a line completed by
	// the drop is detected at this lock, not one piece later.
	e.publishProjectedCellsWithMergeRetry(ctx, cells, flashCells, false)
	e.noteLockSent(true)
	return nil
}

// playerMoves is the move list a step carries for the replay: the player's
// move, or nil for an engine-driven step (a gravity tick), which is never
// played again — the timer keeps ticking.
func playerMoves(m MoveType, internal bool) []MoveType {
	if internal {
		return nil
	}
	return []MoveType{m}
}
