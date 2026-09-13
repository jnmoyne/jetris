package engine

import (
	"context"
	"slices"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
)

// runInput is the engine's single gameplay-write goroutine: it processes player
// input, drives the gravity clock AND times the lock delay. Running all of it
// on one goroutine is deliberate — a player's own gravity step, a player move
// and the piece's lock can never publish to their cell subjects concurrently,
// so they can never lose the per-subject CAS race against each other (which
// would drop the step and flash the piece outline). This applies to both
// competitive and cooperative modes.
//
// Gravity is a clock, not a round trip: a row falls due every
// game.GravityInterval of the level (e.Level(): this player's own lines in
// competitive, the board's on a shared board — the same level in every
// mode), on a fixed schedule, and a wake-up that comes late — the goroutine
// was blocked in a sync round trip, a repair, a burst of input — owes every
// row the schedule passed meanwhile, at once. The rows are QUEUED
// (queueGravity), not published here: they take the move queue like the
// player's steps, so they wait behind the batch in flight, go out
// aggregated — the rows that came due during a round trip, and the player's
// steps around them, as ONE batch (takeMoveGroup, attemptMoves) — and are
// replayed by the repair if that batch is lost (pipeline.go): a row of
// gravity is retried until it lands. The optimistic display plays the
// queued rows at once (IntentPiece), so the piece falls at the level's speed
// whatever the wire does; at the top levels that is more than a row per
// frame. A row that cannot fall — the piece rests on the stack, or on
// another player's falling piece — is not queued at all: the first is the
// lock delay's business, the second waits for the next tick.
//
// A blocked downward step (gravity or soft drop) never locks the piece by
// itself: landing arms the lock delay (lockdelay.go) and the piece locks when
// that timer fires, unless a shift or rotation restarted it. Only a hard drop
// locks at once.
func (e *Engine) runInput(ctx context.Context) {
	interval := game.GravityInterval(e.Level()) // a rejoin starts at the level it had
	next := time.Now().Add(interval)            // when the next row falls due
	gravity := time.NewTimer(interval)
	defer gravity.Stop()
	// The housekeeping that used to ride the gravity tick keeps a cadence
	// of its own: at the top levels gravity is due 140 times a second.
	housekeeping := time.NewTicker(housekeepingInterval)
	defer housekeeping.Stop()
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
			// while it waited, the rows of gravity that came due among them,
			// go out together (pipeline.go). Unless a hard
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
			// Player input and gravity — drop+flash on CAS failure (a row of
			// gravity is replayed instead, pipeline.go).
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
		case <-e.levelChanged:
			// The level moved (a clear — this player's, or a teammate's on a
			// shared board): the new speed applies from now, not one old
			// interval later.
			if e.getMode() != ModePlayer {
				continue
			}
			interval = game.GravityInterval(e.Level())
			next = time.Now().Add(interval)
			if !gravity.Stop() {
				select {
				case <-gravity.C:
				default:
				}
			}
			gravity.Reset(interval)
		case now := <-gravity.C:
			if e.getMode() != ModePlayer {
				return // became a spectator: stop gravity (and this loop)
			}
			// The rows the schedule owes: this one, and one more per
			// interval the wake-up is late by. The schedule keeps its
			// phase — the next deadline is measured from the last, never
			// from now.
			owed := 1 + int(now.Sub(next)/interval)
			if owed < 1 {
				owed = 1
			}
			next = next.Add(time.Duration(owed) * interval)
			gravity.Reset(max(next.Sub(now), time.Millisecond))
			if e.queueGravity(owed) == 0 {
				// Nothing to fall: the piece rests, or waits on a
				// teammate's piece — keep the lock delay's view of it
				// current, as the tick always did.
				e.updateLockDelay(pieceSnapshot{})
			}
		case <-housekeeping.C:
			if e.getMode() != ModePlayer {
				return // became a spectator: stop (this loop, gravity with it)
			}
			// A spawn deferred because another player's active piece covered
			// the spawn cells is retried here, on the same single-write
			// goroutine (the consumer retries it on every board change too;
			// this is the backstop).
			e.retrySpawnIfPending(ctx)

			// A peer's piece left behind — by a player gone from the seat,
			// or one whose piece has not moved in a long while — is vacated
			// here, at the same cadence (roster.go).
			if e.gameStarted.Load() {
				e.vacateIdlePeers(ctx)
			}

			// Garbage backstop: if rows are still owed (a signal was consumed
			// by an attempt that lost its gate, or arrived before runInput
			// started), re-arm the application.
			if e.gameMode != config.ModeCooperative {
				e.mu.Lock()
				deficit := e.garbageOwed - e.txnApplied
				e.mu.Unlock()
				if deficit > 0 {
					e.signalGarbageApply()
				}
			}
		}
	}
}

// housekeepingInterval is the cadence of runInput's periodic chores — the
// deferred-spawn backstop, the idle-peer vacate, the garbage backstop —
// which used to ride the gravity tick.
const housekeepingInterval = time.Second

// queueGravity puts n rows of gravity (MoveGravity) into the move queue — as
// many of them as the piece can still fall, at most — and reports how many
// it queued. Nothing is queued while the piece's fate is decided (a hard
// drop or a hold queued, or being committed: rows behind it would fall on
// the NEXT piece), while the next piece is still on its way (AwaitingSpawn),
// or for the rows the piece cannot take: measured from the piece as it is
// headed (IntentPiece, the queued moves played out) against the optimistic
// board, a row into the stack is the lock delay's business and a row into
// another player's falling piece waits — the next tick asks again. So a row
// that queues can only be lost on the wire, where the repair replays it.
// Runs on runInput.
func (e *Engine) queueGravity(n int) int {
	if n <= 0 || e.AwaitingSpawn() || e.PieceCommitting() || e.barrierQueued() {
		return 0
	}
	p, ok := e.IntentPiece()
	if !ok {
		return 0
	}
	e.mu.Lock()
	base := e.projectionBase()
	shared := e.sharedBoard()
	room := 0
	for room < n {
		down := p
		down.Row += room + 1
		var fits bool
		if shared {
			fits = game.CanPlaceCoop(down, base, e.playerIdx)
		} else {
			fits = game.CanPlace(down, base)
		}
		if !fits {
			break
		}
		room++
	}
	e.mu.Unlock()
	if room == 0 {
		return 0
	}
	e.bufferedMu.Lock()
	for range room {
		e.bufferedMoves = append(e.bufferedMoves, MoveGravity)
	}
	e.bufferedMu.Unlock()
	e.signalMoves()
	e.emitUpdate(EngineUpdate{Kind: UpdateBufferedMoves})
	return room
}

// dropQueuedGravity forgets the rows of gravity still queued: they were owed
// to a piece that is gone — locked, or swapped out by a hold — and must not
// fall on the one that takes its place. Called where a piece enters play
// (spawnPiece) or is swapped (attemptHold); takes bufferedMu only, so it is
// safe under e.mu.
func (e *Engine) dropQueuedGravity() {
	e.bufferedMu.Lock()
	dropped := 0
	kept := e.bufferedMoves[:0]
	for _, m := range e.bufferedMoves {
		if m == MoveGravity {
			dropped++
			continue
		}
		kept = append(kept, m)
	}
	e.bufferedMoves = kept
	if len(kept) == 0 {
		e.bufferedMoves = nil
	}
	e.bufferedMu.Unlock()
	if dropped > 0 {
		e.emitUpdate(EngineUpdate{Kind: UpdateBufferedMoves})
	}
}

// dropQueuedPlayerMoves lets go of the player's moves still queued — held
// for a next piece that is not coming for now (a spawn deferred past
// config.SpawnHoldRelease, deferSpawnLocked) — and keeps the rows of
// gravity, which dropQueuedGravity accounts for on its own. Reports how many
// moves went. Takes bufferedMu only, so it is safe under e.mu.
func (e *Engine) dropQueuedPlayerMoves() int {
	e.bufferedMu.Lock()
	dropped := 0
	kept := e.bufferedMoves[:0]
	for _, m := range e.bufferedMoves {
		if m != MoveGravity {
			dropped++
			continue
		}
		kept = append(kept, m)
	}
	e.bufferedMoves = kept
	if len(kept) == 0 {
		e.bufferedMoves = nil
	}
	e.bufferedMu.Unlock()
	if dropped > 0 {
		e.emitUpdate(EngineUpdate{Kind: UpdateBufferedMoves})
	}
	return dropped
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
		_ = e.attemptMove(ctx, moves[0], moves[0] == MoveGravity)
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
			e.noteStep(m, m == MoveGravity, kick) // the scoring's account of the piece: a rotation, a shift, a soft drop — or a row of gravity, which scores nothing
		}
	}

	if cur == *p {
		e.mu.Unlock()
		return // every step blocked: nothing to publish
	}
	affected := e.sweepRowsLocked(base, affectedRowsUnion(p, &cur))
	rows := base.ProjectMove(affected, &cur, e.playerIdx)
	cells := diffCells(base.Rows, rows)
	pre := *p
	e.mu.Unlock()
	e.publishStep(ctx, cells, pre, cur, moves)
}

// strayRowsLocked returns the rows of a shared board holding an active cell
// of this seat's outside the rows a write already re-projects (affected):
// cells left behind — a copy of the piece a crewmate's stale collapse
// stranded (publishCoopTransform) — which the write's projection then
// vacates in the same atomic batch as the piece (sweepRowsLocked). Only off
// a converged board, nothing in flight: a stray on a cell an un-acked step
// wrote would go out with no expectation (buildStepUpdates) and poison the
// step; the barriers — a hard drop, a hold, a lock — settle the pipeline
// first and sweep every time. Nothing on a private board, where nobody
// else writes the seat's cells. e.mu held.
func (e *Engine) strayRowsLocked(base *game.Playfield, affected []int) []int {
	if !e.sharedBoard() || len(e.inflight) > 0 {
		return nil
	}
	var out []int
	for _, r := range base.ActiveRowsForPlayer(e.playerIdx) {
		if !slices.Contains(affected, r) {
			out = append(out, r)
		}
	}
	return out
}

// sweepRowsLocked widens the rows a write re-projects to the rows holding a
// stray cell of this seat's (strayRowsLocked). e.mu held.
func (e *Engine) sweepRowsLocked(base *game.Playfield, affected []int) []int {
	return append(affected, e.strayRowsLocked(base, affected)...)
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
	case MoveDown, MoveGravity:
		newPiece = *p
		newPiece.Row++
		valid = game.CanPlace(newPiece, base)
	case RotateCW, RotateCCW, Rotate180:
		newPiece, kick, valid = game.RotateKick(*p, turnOf(move), base)
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
	e.publishStep(ctx, cells, pre, newPiece, stepMoves(move))
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
	case MoveDown, MoveGravity:
		newPiece = *p
		newPiece.Row++
		valid = game.CanPlaceCoop(newPiece, base, e.playerIdx)
	case RotateCW, RotateCCW, Rotate180:
		newPiece, kick, valid = game.RotateCoopKick(*p, turnOf(move), base, e.playerIdx)
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

	affected := e.sweepRowsLocked(base, affectedRowsUnion(p, &newPiece))

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
	e.publishStep(ctx, cells, pre, newPiece, stepMoves(move))
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

	affected := e.sweepRowsLocked(e.playfield, affectedRowsUnion(p, &dest))

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

// stepMoves is the move list a single-move step carries for the replay —
// the move itself, a row of gravity (MoveGravity) included: a lost row is
// played again by the repair (pipeline.go).
func stepMoves(m MoveType) []MoveType {
	return []MoveType{m}
}

// turnOf is the turn a rotation move makes: the quarter turn either way, or
// the half turn (Rotate180). Only meaningful for the three rotations.
func turnOf(m MoveType) game.Turn {
	switch m {
	case RotateCCW:
		return game.TurnCCW
	case Rotate180:
		return game.Turn180
	}
	return game.TurnCW
}
