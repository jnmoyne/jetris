package engine

import (
	"context"

	"jetricks/internal/config"
	"jetricks/internal/game"
)

func (e *Engine) runMoves(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case move := <-e.moves:
			if e.mode != ModePlayer {
				continue
			}
			// Player input — drop+flash on CAS failure.
			_ = e.attemptMove(ctx, move, false)
		}
	}
}

// attemptMove runs a move. internal=true marks the move as engine-driven (e.g.
// gravity ticks): on CAS failure such moves use merge-retry in coop mode (so
// the piece keeps falling under contention) and never trigger the rainbow
// flash (the player didn't press a key). internal=false is for player input
// and uses the drop+flash path.
func (e *Engine) attemptMove(ctx context.Context, move MoveType, internal bool) error {
	e.mu.Lock()

	if e.gameMode == config.ModeCooperative {
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
			e.mu.Unlock()
			// Lock is authoritative — NoCAS so it can't be overridden by stale moves.
			e.publishProjectedRowsNoCAS(ctx, rows)
			return nil
		}
		e.mu.Unlock()
		return nil
	}

	affected := affectedRowsUnion(p, &newPiece)
	rows := e.playfield.ProjectMove(affected, &newPiece, e.playerIdx)
	e.mu.Unlock()
	// In competitive mode each player owns their row subjects, so this CAS
	// publish cannot race with another player. The flashOnFailure flag
	// distinguishes player input (flash on rare CAS conflicts caused by
	// stale local LastSeq) from internal gravity moves (no flash).
	e.publishProjectedRows(ctx, rows, !internal)
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
			e.mu.Unlock()
			e.publishProjectedRowsNoCAS(ctx, rows)
			return nil
		}
		e.mu.Unlock()
		return nil
	}

	affected := affectedRowsUnion(p, &newPiece)
	rows := e.playfield.ProjectMove(affected, &newPiece, e.playerIdx)
	e.mu.Unlock()

	if internal {
		// Gravity tick (engine-driven): the piece must keep falling even
		// when the other player is concurrently writing the same shared
		// rows. Use merge-retry — refetch+overlay+retry on CAS failure.
		// No flash either way: the player did not press a key.
		e.publishProjectedRowsWithMergeRetry(ctx, rows)
		return nil
	}
	// Player-initiated move: CAS conflict (typical in coop where two
	// players share the playfield) drops the move and surfaces a local
	// rainbow flash on the player's piece. We do not retry; the player
	// must retry the input themselves, and we do NOT publish anything to
	// the other players.
	e.publishProjectedRows(ctx, rows, true)
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
	e.mu.Unlock()

	// Hard drop landing is authoritative — NoCAS to prevent shrink/move
	// echoes from overwriting it.
	e.publishProjectedRowsNoCAS(ctx, rows)
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
	e.mu.Unlock()

	e.publishProjectedRowsNoCAS(ctx, rows)
	return nil
}
