package engine

import (
	"context"
	"time"

	"jetricks/internal/game"
)

// runInput is the engine's single gameplay-write goroutine: it processes player
// input AND drives the gravity ticker. Running both on one goroutine is
// deliberate — a player's own gravity drop and a player move can never publish
// to their cell subjects concurrently, so they can never lose the per-subject CAS
// race against each other (which would drop the step and flash the piece
// outline). This applies to both competitive and cooperative modes.
func (e *Engine) runInput(ctx context.Context) {
	level := 0
	timer := time.NewTimer(game.GravityInterval(level))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case move := <-e.moves:
			// The move leaves the buffer the moment its processing (and batch
			// publish) starts — keep the under-board buffered line in sync.
			e.popBufferedMove()
			if e.getMode() != ModePlayer {
				continue
			}
			// Player input — drop+flash on CAS failure.
			_ = e.attemptMove(ctx, move, false)
		case <-timer.C:
			if e.getMode() != ModePlayer {
				return // became a spectator: stop gravity (and this loop)
			}
			// internal=true: engine-driven gravity tick. In coop this routes to
			// merge-retry on CAS conflict so the piece keeps falling under
			// contention; it flashes only if the tick is ultimately dropped.
			// Serialized with player input above (same goroutine), so it never
			// races our own moves.
			_ = e.attemptMove(ctx, MoveDown, true)

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
	e.mu.Lock()

	if e.sharedBoard() {
		// Coop and teams share the board with other players' active pieces:
		// same collision rules, same merge-retry publish paths.
		return e.attemptMoveCoop(ctx, move, internal)
	}
	return e.attemptMoveStandard(ctx, move, internal)
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
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
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

	switch move {
	case MoveLeft:
		newPiece = *p
		newPiece.Col--
		valid = game.CanPlace(newPiece, e.playfield)
	case MoveRight:
		newPiece = *p
		newPiece.Col++
		valid = game.CanPlace(newPiece, e.playfield)
	case MoveDown:
		newPiece = *p
		newPiece.Row++
		valid = game.CanPlace(newPiece, e.playfield)
	case RotateCW:
		newPiece, valid = game.Rotate(*p, true, e.playfield)
	case RotateCCW:
		newPiece, valid = game.Rotate(*p, false, e.playfield)
	}

	if !valid {
		if move == MoveDown {
			affected := affectedRowsUnion(p, nil)
			rows := e.playfield.ProjectLock(affected, e.playerIdx)
			cells := diffCells(e.playfield.Rows, rows)
			e.mu.Unlock()
			// In-place lock: all messages convert active cells to locked in
			// place, so lock-in fires at the batch's last message with every
			// locked cell already applied (see orderedCellKeys).
			e.publishProjectedCellsNoCAS(ctx, cells, false)
			return nil
		}
		e.mu.Unlock()
		return nil
	}

	affected := affectedRowsUnion(p, &newPiece)
	rows := e.playfield.ProjectMove(affected, &newPiece, e.playerIdx)
	cells := diffCells(e.playfield.Rows, rows)
	// Cells to flash if the step is dropped by CAS (the piece stays put, so
	// flash its current position). Computed under e.mu before unlocking.
	flashCells := p.Cells()
	e.mu.Unlock()
	// In competitive mode each player owns their cell subjects, so this CAS
	// publish cannot race with another player in practice; if it ever does,
	// the dropped step flashes regardless of whether it was a player input or
	// an internal gravity tick. orderedCellKeys writes the new (active) cells
	// before the vacated ones, so the piece never transiently vanishes
	// mid-relocate (single-row horizontal I included).
	e.publishProjectedCells(ctx, cells, flashCells, false)
	return nil
}

func (e *Engine) attemptMoveCoop(ctx context.Context, move MoveType, internal bool) error {
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
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

	switch move {
	case MoveLeft:
		newPiece = *p
		newPiece.Col--
		valid = game.CanPlaceCoop(newPiece, e.playfield, e.playerIdx)
	case MoveRight:
		newPiece = *p
		newPiece.Col++
		valid = game.CanPlaceCoop(newPiece, e.playfield, e.playerIdx)
	case MoveDown:
		newPiece = *p
		newPiece.Row++
		valid = game.CanPlaceCoop(newPiece, e.playfield, e.playerIdx)
	case RotateCW:
		newPiece, valid = game.RotateCoop(*p, true, e.playfield, e.playerIdx)
	case RotateCCW:
		newPiece, valid = game.RotateCoop(*p, false, e.playfield, e.playerIdx)
	}

	if !valid {
		if move == MoveDown {
			// Distinguish: blocked by locked/bounds (lock now) vs. blocked
			// only by other player's active piece (wait for next gravity tick).
			downPiece := *p
			downPiece.Row++
			if game.CanPlace(downPiece, e.playfield) {
				e.mu.Unlock()
				return nil
			}
			affected := affectedRowsUnion(p, nil)
			rows := e.playfield.ProjectLock(affected, e.playerIdx)
			cells := diffCells(e.playfield.Rows, rows)
			flashCells := p.Cells()
			e.mu.Unlock()
			// Coop shares cell subjects: use CAS+merge-retry so this lock can't
			// clobber the other player's mid-flight piece with our stale view.
			e.publishProjectedCellsWithMergeRetry(ctx, cells, flashCells, false)
			return nil
		}
		e.mu.Unlock()
		return nil
	}

	affected := affectedRowsUnion(p, &newPiece)
	rows := e.playfield.ProjectMove(affected, &newPiece, e.playerIdx)
	cells := diffCells(e.playfield.Rows, rows)
	// Cells to flash if the step is dropped by CAS. Computed under e.mu.
	flashCells := p.Cells()
	e.mu.Unlock()

	if internal {
		// Gravity tick (engine-driven): the piece must keep falling even
		// when the other player is concurrently writing the same shared
		// cells. Use merge-retry — refetch+retry on CAS failure. If
		// every retry is exhausted the tick is dropped and flashes, same as
		// any other lost CAS step. orderedCellKeys writes the new active
		// cells before the vacated ones, so a single-row (horizontal I)
		// piece never transiently vanishes mid-move and triggers a spurious
		// lock-in.
		e.publishProjectedCellsWithMergeRetry(ctx, cells, flashCells, false)
		return nil
	}
	// Player-initiated move: CAS conflict (possible in coop when both
	// players touch the same cell) drops the move and surfaces a local
	// rainbow flash on the player's piece. We do not retry; the player
	// must retry the input themselves, and we do NOT publish anything to
	// the other players.
	e.publishProjectedCells(ctx, cells, flashCells, false)
	return nil
}

func (e *Engine) publishHardDrop(ctx context.Context) error {
	e.mu.Lock()
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return nil
	}

	dest := game.HardDropDestination(*p, e.playfield)
	affected := affectedRowsUnion(p, &dest)
	rows := e.playfield.ProjectHardDrop(affected, dest, e.playerIdx, true)
	cells := diffCells(e.playfield.Rows, rows)
	e.mu.Unlock()

	// Hard drop landing is authoritative — NoCAS to prevent shrink/move
	// echoes from overwriting it. orderedCellKeys applies the landing cells
	// before the vacated ones, ensuring a line completed by the drop is
	// detected at this lock (not one piece later). See publishProjectedCellsNoCAS.
	e.publishProjectedCellsNoCAS(ctx, cells, false)
	return nil
}

func (e *Engine) publishHardDropCoop(ctx context.Context) error {
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
	return nil
}
