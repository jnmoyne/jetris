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
			_ = e.attemptMove(ctx, move)
		}
	}
}

func (e *Engine) attemptMove(ctx context.Context, move MoveType) error {
	e.mu.Lock()

	if e.gameMode == config.ModeCooperative {
		return e.attemptMoveCoop(ctx, move)
	}
	return e.attemptMoveStandard(ctx, move)
}

func (e *Engine) attemptMoveStandard(ctx context.Context, move MoveType) error {
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
			e.playfield.LockActivePieceForPlayer(e.playerIdx)
			affectedRows := getAffectedRows(*p)
			// Lock is authoritative — use NoCAS to prevent concurrent shrink from overwriting
			e.publishPlayfieldRowsNoCAS(ctx, affectedRows)
			e.mu.Unlock()
			return nil
		}
		e.mu.Unlock()
		return nil
	}

	oldCells := p.Cells()
	newCells := newPiece.Cells()
	affectedMap := make(map[int]bool)
	for _, c := range oldCells {
		affectedMap[c[0]] = true
	}
	for _, c := range newCells {
		affectedMap[c[0]] = true
	}

	e.playfield.ClearActiveCellsForPlayer(e.playerIdx)
	e.playfield.SetActivePieceForPlayer(newPiece, e.playerIdx)
	e.mu.Unlock()

	affected := make([]int, 0, len(affectedMap))
	for r := range affectedMap {
		affected = append(affected, r)
	}
	e.publishPlayfieldRows(ctx, affected)

	return nil
}

func (e *Engine) attemptMoveCoop(ctx context.Context, move MoveType) error {
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
		// For rotation in coop, we need to check with CanPlaceCoop
		newPiece, valid = game.RotateCoop(*p, true, e.playfield, e.playerIdx)
	case RotateCCW:
		newPiece, valid = game.RotateCoop(*p, false, e.playfield, e.playerIdx)
	}

	if !valid {
		if move == MoveDown {
			// Check WHY it can't move down:
			// - If blocked by locked cells or out-of-bounds → lock the piece
			// - If blocked only by the other player's active piece → don't lock,
			//   gravity will try again next tick (the obstacle is temporary)
			downPiece := *p
			downPiece.Row++
			if game.CanPlace(downPiece, e.playfield) {
				// CanPlace passes (ignores all active cells) but CanPlaceCoop
				// fails → blocked by other player's active piece only.
				// Don't lock, just wait.
				e.mu.Unlock()
				return nil
			}
			// Blocked by locked cells or bounds → lock
			e.playfield.LockActivePieceForPlayer(e.playerIdx)
			affectedRows := getAffectedRows(*p)
			// Lock is authoritative — use NoCAS
			e.publishPlayfieldRowsNoCAS(ctx, affectedRows)
			e.mu.Unlock()
			return nil
		}
		e.mu.Unlock()
		return nil
	}

	oldCells := p.Cells()
	newCells := newPiece.Cells()
	affectedMap := make(map[int]bool)
	for _, c := range oldCells {
		affectedMap[c[0]] = true
	}
	for _, c := range newCells {
		affectedMap[c[0]] = true
	}

	e.playfield.ClearActiveCellsForPlayer(e.playerIdx)
	e.playfield.SetActivePieceForPlayer(newPiece, e.playerIdx)
	e.mu.Unlock()

	affected := make([]int, 0, len(affectedMap))
	for r := range affectedMap {
		affected = append(affected, r)
	}
	// Moves don't retry on CAS failure — the move is dropped
	e.publishPlayfieldRowsRetry(ctx, affected, false)

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

	oldCells := p.Cells()
	destCells := dest.Cells()
	affectedMap := make(map[int]bool)
	for _, c := range oldCells {
		affectedMap[c[0]] = true
	}
	for _, c := range destCells {
		affectedMap[c[0]] = true
	}

	e.playfield.ClearActiveCellsForPlayer(e.playerIdx)
	e.playfield.SetActivePieceForPlayer(dest, e.playerIdx)
	e.playfield.LockActivePieceForPlayer(e.playerIdx)

	affected := make([]int, 0, len(affectedMap))
	for r := range affectedMap {
		affected = append(affected, r)
	}
	// Lock is authoritative — use NoCAS to prevent shrink events from overwriting it
	e.publishPlayfieldRowsNoCAS(ctx, affected)
	e.mu.Unlock()

	return nil
}

func (e *Engine) publishHardDropCoop(ctx context.Context) error {
	e.mu.Lock()
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return nil
	}

	// Drop to lowest valid position (stops at other player's active piece too)
	dest := game.HardDropDestinationCoop(*p, e.playfield, e.playerIdx)

	// Check if the piece landed on the other player's active piece or on
	// locked cells / bounds. If the position one row below the destination
	// would be valid ignoring active cells (CanPlace), then the obstacle is
	// the other player's active piece → don't lock, let gravity take over.
	below := dest
	below.Row++
	landedOnActivePiece := game.CanPlace(below, e.playfield)

	oldCells := p.Cells()
	destCells := dest.Cells()
	affectedMap := make(map[int]bool)
	for _, c := range oldCells {
		affectedMap[c[0]] = true
	}
	for _, c := range destCells {
		affectedMap[c[0]] = true
	}

	e.playfield.ClearActiveCellsForPlayer(e.playerIdx)
	e.playfield.SetActivePieceForPlayer(dest, e.playerIdx)
	if !landedOnActivePiece {
		// Landed on locked cells or bounds → lock immediately
		e.playfield.LockActivePieceForPlayer(e.playerIdx)
	}

	affected := make([]int, 0, len(affectedMap))
	for r := range affectedMap {
		affected = append(affected, r)
	}
	// Use NoCAS — hard drop result is authoritative
	e.publishPlayfieldRowsNoCAS(ctx, affected)
	e.mu.Unlock()

	return nil
}

func getAffectedRows(p game.Piece) []int {
	rowMap := make(map[int]bool)
	for _, c := range p.Cells() {
		rowMap[c[0]] = true
	}
	rows := make([]int, 0, len(rowMap))
	for r := range rowMap {
		rows = append(rows, r)
	}
	return rows
}
