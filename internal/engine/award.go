package engine

import "jetris/internal/game"

// The Guideline scoring's account of the falling piece (game.Clear,
// game.DetectTSpin). Every field here is guarded by e.mu.
//
// lockAward is what the lock this engine published is worth beyond its
// lines: the spin the piece made where it came to rest — judged from how
// it got there (Engine.spin) — and the drop points it earned on the way
// down. Armed by the publish that locks the piece (a hard drop, the lock
// delay's expiry) and consumed once by handleLockIn when the lock echoes
// back; a lock-in with nothing armed is a plain lock.
type lockAward struct {
	spin       game.TSpin
	dropPoints int
}

// noteStep records a successful step of the falling piece: a rotation is
// remembered with the kick that placed it (what a T-spin is judged by) — a
// half turn as such, its kicks being no quarter turn's — any shift or
// downward step forgets it, and a player's soft drop — a MoveDown that is
// not a gravity tick — earns the piece a cell of drop points. Call with e.mu
// held.
func (e *Engine) noteStep(move MoveType, internal bool, kick int) {
	switch move {
	case RotateCW, RotateCCW:
		e.spin = game.SpinState{Rotated: true, Kick: kick}
	case Rotate180:
		e.spin = game.SpinState{Rotated: true, Kick: kick, Half: true}
	case MoveDown:
		e.spin.Rotated = false
		if !internal {
			e.softDropCells++
		}
	case MoveLeft, MoveRight:
		e.spin.Rotated = false
	}
}

// noteHardDrop values the piece dropping from where it stands to dest. A
// drop of zero cells is not a move — a T rotated into its slot and
// hard-dropped in place is still a T-spin — while any real fall forgets the
// rotation. locks says the drop locks the piece (on a shared board a drop
// that lands on another player's falling piece does not); then the lock's
// award is armed: the T-spin dest makes, and the piece's drop points — two
// per cell of the fall, one per cell soft-dropped before it. Call with e.mu
// held.
func (e *Engine) noteHardDrop(from, dest game.Piece, locks bool) {
	fell := dest.Row - from.Row
	if fell > 0 {
		e.spin.Rotated = false
	}
	if locks {
		e.lockAward = lockAward{
			spin:       game.DetectSpin(dest, e.playfield, e.spin),
			dropPoints: game.DropPoints(e.softDropCells, fell),
		}
	}
}

// armLockAward values the piece locking where it stands — the lock delay's
// expiry. Call with e.mu held.
func (e *Engine) armLockAward(p game.Piece) {
	e.lockAward = lockAward{
		spin:       game.DetectSpin(p, e.playfield, e.spin),
		dropPoints: game.DropPoints(e.softDropCells, 0),
	}
}

// accountLock is the Guideline's account of a lock that cleared lines rows
// and left the board as after (game.Clear): a clear counts towards the
// combo — the run of consecutive clearing locks — and either extends or
// breaks the Back-to-Back chain: a Jetris or a T-spin clear extends it, a
// plain single, double or triple breaks it. A lock that clears nothing ends
// the combo and leaves the chain alone; a T-spin that cleared nothing still
// scores. The points are the clear's at level (the 1-based level BEFORE the
// clear) plus the piece's drop points, unmultiplied. Call with e.mu held.
func (e *Engine) accountLock(lines int, award lockAward, after []game.Row, level int) (game.Clear, int) {
	var clear game.Clear
	if lines > 0 {
		e.comboRun++
		clear = game.Clear{Lines: lines, Spin: award.spin, Combo: e.comboRun - 1, Perfect: game.IsPerfectClear(after)}
		clear.BackToBack = e.b2bChain && clear.Difficult()
		e.b2bChain = clear.Difficult()
	} else {
		e.comboRun = 0
		clear = game.Clear{Spin: award.spin}
	}
	return clear, clear.Points(level) + award.dropPoints
}

// resetPieceScoring opens the account of a new falling piece — a spawn, or a

// swap out of the hold slot. Call with e.mu held.
func (e *Engine) resetPieceScoring() {
	e.spin = game.SpinState{}
	e.softDropCells = 0
	e.lockAward = lockAward{}
}

// awardFromEvent is the clear a teammate's line-clear event describes, for
// the banner.
func awardFromEvent(ev GameEvent) game.Clear {
	return game.Clear{
		Lines:      ev.LinesCleared,
		Spin:       game.TSpin(ev.TSpin),
		BackToBack: ev.BackToBack,
		Combo:      ev.Combo,
		Perfect:    ev.Perfect,
	}
}
