package engine

import (
	"context"
	"time"

	"jetris/internal/config"
	"jetris/internal/game"
)

// Guideline lock delay. A piece that lands on locked cells or the floor does
// not lock on the spot: it locks config.LockDelay after landing, and every
// successful shift or rotation made while it rests restarts that timer — up
// to config.LockDelayMoveResets times per piece, the allowance renewed each
// time the piece falls to a new lowest row. A piece that leaves the stack (a
// shift over a hole, a kick upward) stops the timer, which restarts fresh when
// it lands again. Only a hard drop locks a piece at once.
//
// Everything here runs on runInput's goroutine — the one that moves the piece
// and drives gravity — so the lock can never race the engine's own publishes;
// e.mu is taken only to read the playfield. "Grounded" is the test the
// immediate lock used to make: the piece cannot move down and the obstacle is
// locked cells or the bottom edge (game.CanPlace, which ignores every active
// piece — a piece resting on another player's still-falling piece is waiting,
// not landing, and the delay does not start until that piece locks).

// lockDelayState is the timing of the current piece's lock (runInput only).
type lockDelayState struct {
	timer    *time.Timer
	fire     <-chan time.Time // nil while no lock is pending; runInput selects on it
	tracking bool             // a piece is being timed (false between pieces)
	gen      uint64           // Engine.spawnGen of the piece the counters describe (a spawn or a hold swap starts a new one)
	lowest   int              // lowest playfield row any cell of the piece has reached
	resets   int              // timer restarts spent since lowest last advanced
}

func (s *lockDelayState) arm(d time.Duration) {
	s.disarm()
	s.timer = time.NewTimer(d)
	s.fire = s.timer.C
}

func (s *lockDelayState) disarm() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.fire = nil
}

// pieceSnapshot is the active piece before one of the engine's own moves, for
// updateLockDelay to tell a successful shift or rotation (which restarts the
// timer) from a step that was blocked or lost its CAS race (which does not).
// The zero snapshot means "no move was attempted".
type pieceSnapshot struct {
	p  game.Piece
	ok bool
}

func (e *Engine) activePieceSnapshot() pieceSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.projectionBase().ActivePieceForPlayer(e.playerIdx); p != nil {
		return pieceSnapshot{p: *p, ok: true}
	}
	return pieceSnapshot{}
}

// lowestRow returns the playfield row of the piece's lowest cell.
func lowestRow(p game.Piece) int {
	low := -1
	for _, c := range p.Cells() {
		if c[0] > low {
			low = c[0]
		}
	}
	return low
}

// updateLockDelay re-times the lock after anything that may have moved the
// piece or changed the stack under it: a player move, a gravity tick, an
// own-board echo. before is the piece as it was before the engine's own move,
// if one was attempted.
func (e *Engine) updateLockDelay(before pieceSnapshot) {
	s := &e.lockState
	e.mu.Lock()
	// The piece where it is headed: with steps pipelined (pipeline.go) the
	// acked replica lags the moves, and the delay times the piece the player
	// is actually steering.
	base := e.projectionBase()
	p := base.ActivePieceForPlayer(e.playerIdx)
	var grounded bool
	if p != nil {
		down := *p
		down.Row++
		grounded = !game.CanPlace(down, base)
	}
	gen := e.spawnGen.Load() // bumped under e.mu by every spawn and hold swap
	e.mu.Unlock()

	if p == nil {
		s.disarm()
		s.tracking = false
		return
	}
	low := lowestRow(*p)
	if !s.tracking || s.gen != gen {
		// A new piece (spawned, swapped in from the hold slot, or one the
		// watchdog respawned): a fresh allowance.
		s.tracking, s.gen, s.lowest, s.resets = true, gen, low, 0
	} else if low > s.lowest {
		s.lowest, s.resets = low, 0
	}
	if !grounded {
		s.disarm() // airborne: the delay restarts when it lands again
		return
	}
	if s.fire == nil {
		s.arm(e.lockDelay) // just landed
		return
	}
	if before.ok && before.p != *p && s.resets < config.LockDelayMoveResets {
		s.resets++
		s.arm(e.lockDelay)
	}
}

// lockPieceIfGrounded is the lock timer's expiry: lock the piece where it
// rests — unless the stack changed under it meanwhile (a clear collapsed the
// rows it stood on) and it can fall again, in which case gravity carries on.
// The publish is the same in-place lock the blocked gravity step used to make.
func (e *Engine) lockPieceIfGrounded(ctx context.Context) {
	// A barrier: the lock is written where the piece has been acked to be,
	// after every pipelined step landed (pipeline.go). A repair's replay
	// goes first: the lock re-times behind it (runInput calls
	// updateLockDelay right after).
	if e.settlePipeline(ctx) {
		return
	}
	e.mu.Lock()
	p := e.playfield.ActivePieceForPlayer(e.playerIdx)
	if p == nil {
		e.mu.Unlock()
		return
	}
	down := *p
	down.Row++
	if game.CanPlace(down, e.playfield) {
		e.mu.Unlock()
		return
	}
	e.armLockAward(*p) // the lock's worth: its T-spin and drop points (award.go)
	affected := affectedRowsUnion(p, nil)
	rows := e.playfield.ProjectLock(affected, e.playerIdx)
	for r, row := range e.playfield.ProjectMove(e.strayRowsLocked(e.playfield, affected), nil, e.playerIdx) {
		rows[r] = row // a cell of ours left elsewhere goes with the lock's batch, vacated
	}

	cells := diffCells(e.playfield.Rows, rows)
	flashCells := p.Cells()
	shared := e.sharedBoard()
	e.mu.Unlock()

	if shared {
		// Coop shares cell subjects: CAS+merge-retry so this lock can't
		// clobber another player's mid-flight piece with our stale view.
		e.publishProjectedCellsWithMergeRetry(ctx, cells, flashCells, false)
		e.noteLockSent(false)
		return
	}
	// In-place lock: all messages convert active cells to locked in place, so
	// lock-in fires at the batch's last message with every locked cell
	// already applied (see orderedCellKeys).
	e.publishProjectedCellsNoCAS(ctx, cells, false)
	e.noteLockSent(false)
}
