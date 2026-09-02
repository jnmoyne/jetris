package nativeui

// The accidental-drop guard — the HANDLING section's fourth knob (GUARD, in
// autoshift.go's handlingKnobs), in milliseconds, 0 (off) to maxHandlingMs.
//
// A piece that locks on its own gives no warning. The player has already
// begun the press that was meant for it when the lock delay runs out, and by
// the time the space bar (or the pad's ⤓, or a flick) arrives the piece is
// gone and the next one has spawned under it — dropped where nobody aimed it,
// at the top of the stack, one piece of the plan lost. The faster the level,
// the more often it happens.
//
// The guard makes the hard drop unavailable for that moment and no longer:
//
//   - from the lock the player did not make until the next piece is on the
//     board — the lock-to-spawn gap, a NATS round trip, where a drop is a
//     no-op or lands on the piece that is about to appear (the touch gestures
//     have always refused a hard drop in that gap: gesture.go); then
//   - the knob's own window past the spawn, the few frames a press already in
//     flight takes to arrive.
//
// A piece the player DID commit — their own hard drop — guards nothing: the
// gap behind it belongs to a deliberate stream of presses, and a drop pressed
// during it is the next piece's, as before. Only the lock nobody asked for is
// guarded.
//
// The soft drop, the shifts and the rotations are untouched: they are all
// undoable, and only the hard drop spends the piece where it stands.
//
// dropGuard is the machine, a pure struct in the autoShift mold (autoshift.go)
// — fed the board's state each frame, told about the drops the player makes,
// asked whether a drop right now is refused — so the whole thing is unit
// tested without an engine (dropguard_test.go). UI goroutine only.

import (
	"time"

	"jetris/internal/engine"
)

// dropGuard watches one board's pieces go by.
//
// The piece INDEX is what it watches, not the piece on the board: the lock
// and the spawn that follows it happen in one critical section on the
// engine's consumer goroutine (engine/consumer.go handleLockIn), so a frame
// may well never see the board without a piece — but Engine.PieceIdx, bumped
// there, has moved on for good and any frame after it can tell.
type dropGuard struct {
	seen  bool      // idx has been read: the first frame arms nothing
	idx   uint64    // the piece the board was on (Engine.PieceIdx)
	spent bool      // that piece is the player's own to commit: they hard dropped it
	armed bool      // a lock the player did not make: waiting for the next piece
	until time.Time // the window past that piece's arrival
}

// observe feeds the frame: idx is the board's piece index, hasPiece whether
// one of the player's is on it, d the knob (0: the guard is off).
func (g *dropGuard) observe(idx uint64, hasPiece bool, now time.Time, d time.Duration) {
	if !g.seen {
		g.seen, g.idx = true, idx
		return
	}
	if idx != g.idx {
		// The piece moved on. Either the player dropped it (spent) or it
		// locked on its own — and only the second is guarded. This is also
		// the whole of it when the gap below was never visible to a frame.
		g.armed = g.armed || (!g.spent && d > 0)
		g.idx, g.spent = idx, false
	}
	if !hasPiece && !g.spent && d > 0 {
		// The lock-to-spawn gap of a lock the player did not make: the piece
		// is off the board and its lock-in has not come back yet.
		g.armed = true
	}
	if g.armed && hasPiece {
		// The next piece is here: the guard becomes the knob's window.
		g.armed, g.until = false, now.Add(d)
	}
}

// blocked reports whether a hard drop must be refused now: the knob's window
// after a lock the player did not make, and the gap before it.
func (g *dropGuard) blocked(now time.Time, d time.Duration) bool {
	if d <= 0 {
		return false // off: the drop is the player's at every instant
	}
	return g.armed || now.Before(g.until)
}

// spend marks the piece as the player's own to commit: the lock it is about
// to make is a hard drop of theirs, not the accident the guard is for.
func (g *dropGuard) spend() { g.spent = true }

// reset forgets everything: the game screen left, the player eliminated, the
// game over — nothing carries into the next one.
func (g *dropGuard) reset() { *g = dropGuard{} }

// dropGuardDur is the knob as a duration.
func (a *App) dropGuardDur() time.Duration {
	return time.Duration(a.dropGuardMs) * time.Millisecond
}

// observeDropGuard feeds the guard the frame's board. Called FIRST among the
// frame's input handlers (layoutGame), before any of them can dispatch a hard
// drop, and under the same gate as the keyboard's auto-repeat: any other
// state resets it, so a guard armed by the last lock of a finished game
// cannot refuse the first drop of the next one.
func (a *App) observeDropGuard(gtx C, eng *engine.Engine, active bool) {
	if !active || eng == nil {
		a.dropGuard.reset()
		return
	}
	// "No piece" only counts on a live engine — Started's qualifier, as
	// everywhere else the lock-to-spawn gap is read (gesture.go): an engine
	// that has not started has no gap, only an empty board.
	noPiece := eng.Started() && !eng.HasActivePiece()
	a.dropGuard.observe(eng.PieceIdx(), !noPiece, gtx.Now, a.dropGuardDur())
}

// hardDrop is the player's hard drop, from wherever it came — the space bar
// (input.go), the pad's ⤓ (controls.go), a flick on the board (gesture.go):
// the one door the guard sits at. A refused drop is dropped on the floor, not
// held for the next piece — the press was meant for a piece that is already
// down.
func (a *App) hardDrop(gtx C, eng *engine.Engine) {
	if a.dropGuard.blocked(gtx.Now, a.dropGuardDur()) {
		return
	}
	a.dropGuard.spend()
	eng.HardDrop()
}
