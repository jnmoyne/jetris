package nativeui

// Touch gestures on the playfield — the control scheme of the phone versions
// of the game, for a finger on the board rather than a thumb on the pad:
//
//	swipe left / right  shift the piece, a column per cell of travel
//	tap the left half   rotate counter-clockwise
//	tap the right half  rotate clockwise
//	drag down           soft drop, a row per cell of travel (still steerable)
//	flick down          hard drop
//	swipe up            hold (games with the hold rule)
//
// The playfield and the empty room around it are the gesture surface
// (gestureSurface): a press that a pad button, the HOLD box, the chat panel
// or a HUD button took never reaches it, so nothing double-fires, and
// positions arrive in the surface's own coordinates — centered on the
// playfield, so the rotate tap splits at the playfield's center. Only touch
// presses gesture — a mouse click on the board is the "keys back to the
// board" click (handleGameFocus) and must not rotate. Every finger is its own
// gesture: a thumb resting on the board's edge blocks nothing, and a finger
// dragging the piece down while another taps a rotation does both. (The
// browser build never reuses a finger's pointer id on iOS — Gio's JS backend
// only forgets touch identifiers on a touchcancel — so nothing here relies on
// seeing the same id twice.)
//
// Every move is a NATS round trip (engine.dispatch: one publish in flight, the
// rest waiting in the engine's move queue — unbounded, nothing ever dropped),
// which shapes the recognizer: a drag steps the piece toward a target computed
// from the finger's whole travel and queues only a few moves ahead of the
// engine, catching up frame by frame as the queue drains, so the piece tracks
// where the finger is rather than replaying where it has been (a swipe that
// turns back cancels the steps not yet queued instead of queuing them and
// their undo); and while the finger moves fast and downward —
// a flick in progress — nothing steps at all, so the flick's release is a
// clean hard drop rather than a hard drop queued behind a burst of soft drops.
// A finger that stops after a fast move is no flick: once it has been still
// for gestureStill the rows it covered step after all.

import (
	"image"
	"math"
	"slices"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"jetris/internal/engine"
)

const (
	// gestureSlop is how far a finger may wander and still be tapping: well
	// past Gio's 3 dp scroll slop — a thumb on glass is not a cursor.
	gestureSlop = unit.Dp(12)
	// holdSwipe is the upward travel that holds the piece — a deliberate
	// swipe, not a wobble.
	holdSwipe = unit.Dp(48)
	// tapMaxDur is the longest press that still rotates on release; held
	// longer, a still finger does nothing (the spec's "tap, hold, and drag"
	// is a soft drop, not a rotate).
	tapMaxDur = 300 * time.Millisecond
	// flickVelocity (dp/ms) is the downward speed, over velocityWindow, that
	// makes a drag a flick, and flickMinTravel the downward travel a flick
	// must also cover — a hard drop is irreversible, so both must agree.
	flickVelocity  = 1.2
	flickMinTravel = unit.Dp(40)
	velocityWindow = 80 * time.Millisecond
	// flickWindow is how long after its press a gesture can still be a
	// flick: a flick is over in 30-100 ms. Past it a fast finger is a fast
	// DRAG — a soft drop swept top to bottom in under half a second — and
	// steps like any other; without the bound such a swipe was held back
	// for as long as it stayed fast, and only moved the piece once the
	// finger slowed, stopped or lifted.
	flickWindow = 200 * time.Millisecond
	// gestureStill is how long a finger must go without an event, by the
	// frame clock, before a fast drag counts as stopped: a still finger
	// sends nothing, so its speed would otherwise stay whatever it last was.
	gestureStill = 120 * time.Millisecond
	// gestureBufferCap is how many moves a drag lets queue ahead of the
	// engine before it stops stepping until the queue drains (the engine's
	// queue itself is unbounded): short enough that the piece follows the
	// finger's present position, not its past.
	gestureBufferCap = 6
	gestureSamples   = 8
	// maxFingers bounds the fingers tracked at once; past it the oldest is
	// forgotten (a finger whose release never came).
	maxFingers = 4
	// maxHeldMoves bounds the moves held for the next piece while the board
	// has none (handleGestures); a gesture past it in a long gap is dropped.
	maxHeldMoves = 12
	// playfieldLabel is the surface's semantic label (the tests find the
	// playfield on screen by it).
	playfieldLabel = "playfield"
)

// gesturePhase is where a finger's gesture stands between press and release.
type gesturePhase uint8

const (
	gestureUndecided gesturePhase = iota // down, within the slop: a tap unless it moves
	gestureDrag                          // moved sideways or down: the piece follows the finger
	gestureUp                            // moved up: a hold once it reaches holdSwipe, a drag if it turns
	gestureDone                          // the gesture fired its one move (hold); nothing more until release
)

type gestureSample struct {
	pos f32.Point
	t   time.Duration
}

// boardGesture is the playfield's touch-gesture recognizer. It is fed the
// pointer events of the playfield's area (feed) and reports moves through a
// callback, so it is a pure state machine — no router, no engine — and
// unit-tests as one. UI goroutine only.
type boardGesture struct {
	// fieldW and cell are the playfield's width and cell size in px, set by
	// the frame's layout (setField) for the events of the next.
	fieldW, cell int
	// fingers are the fingers down on the playfield, oldest first.
	fingers []*fingerGesture
}

// fingerGesture is one finger's gesture, press to release.
type fingerGesture struct {
	id          pointer.ID
	phase       gesturePhase
	start, last f32.Point
	t0          time.Duration
	// horiz: the drag began sideways, so a column moves at half a cell of
	// travel (responsive) while a row still takes a whole one; a drag that
	// began downward needs a whole cell either way, so a soft drop's
	// sideways wobble never shifts the piece.
	horiz bool
	// stepsX (signed columns) and stepsY (rows) are the steps emitted so
	// far, matched against the targets the finger's travel sets.
	stepsX, stepsY int
	// samples is a ring of the latest positions and event times for the
	// velocity; seenAt is the frame time of the latest event (gestureStill).
	samples [gestureSamples]gestureSample
	n       int
	seenAt  time.Time
}

// setField records the playfield geometry the next frame's events are
// measured in. A change under a finger (the board re-planned — the first
// touch flipping touchUI, a resize) drops the gestures in flight rather
// than mixing two coordinate frames.
func (g *boardGesture) setField(w, cell int) {
	if w != g.fieldW || cell != g.cell {
		g.reset()
	}
	g.fieldW, g.cell = w, cell
}

// reset forgets every finger; the geometry stays.
func (g *boardGesture) reset() { g.fingers = nil }

// finger is the tracked finger of pointer id, nil if none.
func (g *boardGesture) finger(id pointer.ID) *fingerGesture {
	for _, f := range g.fingers {
		if f.id == id {
			return f
		}
	}
	return nil
}

func (g *boardGesture) forget(id pointer.ID) {
	g.fingers = slices.DeleteFunc(g.fingers, func(f *fingerGesture) bool { return f.id == id })
}

// feed advances the recognizer by one pointer event of the playfield's area,
// drained in the frame at now, emitting the moves it resolves to. room is
// how many steps the move buffer can take; the room left is returned. A
// release's own move (a rotate, the hard drop, the hold) is emitted
// regardless of room.
func (g *boardGesture) feed(ev pointer.Event, m unit.Metric, now time.Time, room int, emit func(engine.MoveType)) int {
	if ev.Kind == pointer.Cancel {
		// Whoever's: the system's (the JS backend's touchcancel names no
		// pointer), or the router's synthetic first one.
		g.reset()
		return room
	}
	if ev.Source != pointer.Touch {
		return room
	}
	switch ev.Kind {
	case pointer.Press:
		// A fresh finger — or a tracked one pressing again, which means
		// its release was lost: start it over either way.
		g.forget(ev.PointerID)
		if len(g.fingers) >= maxFingers {
			g.fingers = g.fingers[1:]
		}
		f := &fingerGesture{id: ev.PointerID, start: ev.Position, last: ev.Position, t0: ev.Time}
		f.sample(ev.Position, ev.Time, now)
		g.fingers = append(g.fingers, f)
	case pointer.Drag:
		if f := g.finger(ev.PointerID); f != nil {
			room = f.drag(g, ev, m, now, room, emit)
		}
	case pointer.Release:
		if f := g.finger(ev.PointerID); f != nil {
			room = f.release(g, ev, m, now, room, emit)
			g.forget(ev.PointerID)
		}
	}
	return room
}

// step emits, for every finger, the moves that bring the piece up to its
// targets — at most room of them in all — and returns the room left. The
// frame calls it after draining the events so a drag that outran the
// buffer, or stopped after a fast move, catches up.
func (g *boardGesture) step(m unit.Metric, now time.Time, room int, emit func(engine.MoveType)) int {
	for _, f := range g.fingers {
		room = f.step(g, m, now, room, emit)
	}
	return room
}

// pending reports steps a finger has asked for that have not been emitted
// yet (held back by the buffer, or by a flick in progress).
func (g *boardGesture) pending() bool {
	for _, f := range g.fingers {
		if f.pending(g) {
			return true
		}
	}
	return false
}

func (f *fingerGesture) sample(pos f32.Point, t time.Duration, now time.Time) {
	f.samples[f.n%gestureSamples] = gestureSample{pos: pos, t: t}
	f.n++
	f.seenAt = now
}

// velocity is the finger's recent speed in dp/ms: the newest sample against
// the oldest within velocityWindow of it — or, for a finger too slow to have
// two samples that close, the one just before it.
func (f *fingerGesture) velocity(m unit.Metric) f32.Point {
	if f.n < 2 || m.PxPerDp <= 0 {
		return f32.Point{}
	}
	newest := f.samples[(f.n-1)%gestureSamples]
	oldest := f.samples[(f.n-2)%gestureSamples]
	for i := 3; i <= min(f.n, gestureSamples); i++ {
		s := f.samples[(f.n-i)%gestureSamples]
		if newest.t-s.t > velocityWindow {
			break
		}
		oldest = s
	}
	dt := float32(newest.t-oldest.t) / float32(time.Millisecond)
	if dt <= 0 {
		return f32.Point{}
	}
	d := newest.pos.Sub(oldest.pos)
	return f32.Pt(d.X/dt/m.PxPerDp, d.Y/dt/m.PxPerDp)
}

// fast reports a finger moving fast and mostly downward: a flick in
// progress, during which nothing steps. A finger that has sent nothing for
// gestureStill has stopped, whatever its last speed.
func (f *fingerGesture) fast(m unit.Metric, now time.Time) bool {
	if now.Sub(f.seenAt) > gestureStill {
		return false
	}
	if f.n > 0 && f.samples[(f.n-1)%gestureSamples].t-f.t0 > flickWindow {
		return false // too long on the glass to be a flick: a drag, whatever its speed
	}
	v := f.velocity(m)
	return v.Y >= flickVelocity && v.Y > absf(v.X)
}

// flick reports a release that hard-drops: fast, and far enough down.
func (f *fingerGesture) flick(m unit.Metric, now time.Time) bool {
	return f.fast(m, now) && f.last.Y-f.start.Y >= float32(m.Dp(flickMinTravel))
}

// targets are the columns (signed) and rows the finger's travel from the
// press asks for. Rows never count upward travel: rising does nothing, and
// coming back down resumes past the depth already reached.
func (f *fingerGesture) targets(g *boardGesture) (tx, ty int) {
	d := f.last.Sub(f.start)
	c := float64(g.cell)
	if f.horiz {
		tx = int(math.Round(float64(d.X) / c))
	} else {
		tx = int(math.Trunc(float64(d.X) / c))
	}
	ty = int(math.Trunc(float64(max(0, d.Y)) / c))
	return tx, ty
}

// step is flush unless the finger is flicking.
func (f *fingerGesture) step(g *boardGesture, m unit.Metric, now time.Time, room int, emit func(engine.MoveType)) int {
	if f.fast(m, now) {
		return room
	}
	return f.flush(g, room, emit)
}

// flush emits the moves that bring the piece up to the finger's targets, at
// most room of them, whatever the finger's speed: the release of a drag that
// was no flick (too short, or slowed to a stop) owes the finger its travel.
func (f *fingerGesture) flush(g *boardGesture, room int, emit func(engine.MoveType)) int {
	if f.phase != gestureDrag || g.cell <= 0 {
		return room
	}
	tx, ty := f.targets(g)
	for room > 0 && f.stepsX < tx {
		emit(engine.MoveRight)
		f.stepsX++
		room--
	}
	for room > 0 && f.stepsX > tx {
		emit(engine.MoveLeft)
		f.stepsX--
		room--
	}
	for room > 0 && f.stepsY < ty {
		emit(engine.MoveDown)
		f.stepsY++
		room--
	}
	return room
}

func (f *fingerGesture) pending(g *boardGesture) bool {
	if f.phase != gestureDrag || g.cell <= 0 {
		return false
	}
	tx, ty := f.targets(g)
	return f.stepsX != tx || f.stepsY < ty
}

func (f *fingerGesture) drag(g *boardGesture, ev pointer.Event, m unit.Metric, now time.Time, room int, emit func(engine.MoveType)) int {
	f.last = ev.Position
	f.sample(ev.Position, ev.Time, now)
	d := ev.Position.Sub(f.start)
	if f.phase == gestureUndecided {
		if slop := float32(m.Dp(gestureSlop)); d.X*d.X+d.Y*d.Y <= slop*slop {
			return room
		}
		if -d.Y > absf(d.X) {
			f.phase = gestureUp
		} else {
			f.phase = gestureDrag
			f.horiz = absf(d.X) >= absf(d.Y)
		}
	}
	switch f.phase {
	case gestureUp:
		if -d.Y >= float32(m.Dp(holdSwipe)) {
			emit(engine.MoveHold)
			f.phase = gestureDone
		} else if -d.Y <= absf(d.X) {
			// No longer heading up (turned sideways, or came back down):
			// a drag after all, measured from the press like any other.
			f.phase = gestureDrag
			f.horiz = absf(d.X) >= absf(d.Y)
			room = f.step(g, m, now, room, emit)
		}
	case gestureDrag:
		room = f.step(g, m, now, room, emit)
	}
	return room
}

func (f *fingerGesture) release(g *boardGesture, ev pointer.Event, m unit.Metric, now time.Time, room int, emit func(engine.MoveType)) int {
	f.last = ev.Position
	f.sample(ev.Position, ev.Time, now)
	switch f.phase {
	case gestureUndecided:
		d := ev.Position.Sub(f.start)
		slop := float32(m.Dp(gestureSlop))
		if ev.Time-f.t0 <= tapMaxDur && d.X*d.X+d.Y*d.Y <= slop*slop {
			if f.start.X < float32(g.fieldW)/2 {
				emit(engine.RotateCCW)
			} else {
				emit(engine.RotateCW)
			}
		}
	case gestureDrag:
		if f.flick(m, now) {
			// The steps a flick held back are moot: the piece is going
			// all the way down.
			emit(engine.MoveHardDrop)
		} else {
			room = f.flush(g, room, emit)
		}
	}
	return room
}

func absf(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// moveFor maps a move to the engine method that dispatches it (the pad's and
// the keys' tables spell these out per button and key).
func moveFor(m engine.MoveType) func(*engine.Engine) {
	switch m {
	case engine.MoveLeft:
		return (*engine.Engine).MoveLeft
	case engine.MoveRight:
		return (*engine.Engine).MoveRight
	case engine.MoveDown:
		return (*engine.Engine).MoveDown
	case engine.RotateCW:
		return (*engine.Engine).RotateCW
	case engine.RotateCCW:
		return (*engine.Engine).RotateCCW
	case engine.MoveHardDrop:
		return (*engine.Engine).HardDrop
	case engine.MoveHold:
		return (*engine.Engine).Hold
	}
	return func(*engine.Engine) {}
}

// handleGestures drains the playfield area's touch events every frame on the
// game screen and dispatches the moves they resolve to while the game is
// actually being played (active — the pad's gate; a gesture made before the
// start or after elimination dies here). A drag that outran the move buffer,
// or stopped after a fast move, keeps stepping frame by frame.
//
// Call it after handleGameFocus (which must come first in the frame — see
// its doc) and after the pad's Clickables have drained: every event bound
// for the area is drained here before the frame issues any command, and the
// filter includes the drags and the release, so a press on the playfield is
// never left undrained for a command to replay.
func (a *App) handleGestures(gtx C, eng *engine.Engine, active bool) {
	g := &a.gest
	room := 0
	if active {
		room = max(0, gestureBufferCap-len(eng.BufferedMoves()))
	}
	// Between a lock and the next spawn — a NATS round trip — the board has
	// no piece and a move dispatched now is a no-op. The moves a gesture
	// resolves to in that gap are held (heldMoves) and fly the moment the
	// piece is there, so a swipe or a tap begun right after a drop lands on
	// the new piece where the finger already is. A hard drop is not held: a
	// flick meant for the piece that just locked must not drop the next one.
	noPiece := active && eng.Started() && !eng.HasActivePiece()
	// The lock-to-spawn gap as the frames see it, for the touch diagnostic
	// (view_js.go): how long the board went without a piece.
	switch {
	case noPiece && a.pieceGapStart.IsZero():
		a.pieceGapStart = gtx.Now
	case !noPiece && !a.pieceGapStart.IsZero():
		a.spawnGapLast = gtx.Now.Sub(a.pieceGapStart)
		a.spawnGapMax = max(a.spawnGapMax, a.spawnGapLast)
		a.pieceGapStart = time.Time{}
	}
	if active && !noPiece && len(a.heldMoves) > 0 {
		for _, m := range a.heldMoves {
			moveFor(m)(eng)
		}
		a.heldMoves = a.heldMoves[:0]
	}
	emit := func(m engine.MoveType) {
		switch {
		case !active:
		case noPiece && m == engine.MoveHardDrop:
		case noPiece:
			if len(a.heldMoves) < maxHeldMoves {
				a.heldMoves = append(a.heldMoves, m)
				a.heldPeak = max(a.heldPeak, len(a.heldMoves))
			}
		default:
			moveFor(m)(eng)
		}
	}
	filter := pointer.Filter{Target: &a.fieldTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel}
	for {
		ev, ok := gtx.Source.Event(filter)
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok {
			room = g.feed(pe, gtx.Metric, gtx.Now, room, emit)
		}
	}
	if !active {
		g.reset()
		a.heldMoves = a.heldMoves[:0]
		return
	}
	g.step(gtx.Metric, gtx.Now, room, emit) // catch up as the buffer drains, or once a fast finger has stopped
	if g.pending() || len(a.heldMoves) > 0 {
		animate(gtx) // steps held back, or moves waiting for the piece: try again next frame
	}
}

// gestureSurface registers the touch-gesture surface over rectangle r of the
// current transform: the fieldTag pointer area, the playfield's semantic
// label (what the tests find it on screen by), and the geometry the
// recognizer measures the next frame's events in.
//
// r is the playfield PLUS the empty room around it — the board column's
// margins, the gap before the wells or the pad, the strip's row under it: a
// thumb on any of it drives the piece, which on a phone roughly doubles the
// surface a swipe has to work with (game.go computes r). It is kept centered
// on the playfield, because a tap's rotation direction splits the surface
// down the middle (fingerGesture.release) and that middle must be the
// playfield's. Nothing else is registered inside r; the wells, the pad's
// buttons and the panels are all outside it — and, being laid out after,
// would win the press anyway.
//
// Registered outside the garbage-impact shake, so a swipe in flight never
// judders with the well, and outside the countdown overlay, which draws over
// the playfield without widening it.
func (a *App) gestureSurface(gtx C, r image.Rectangle, cell int) {
	// Pushed and popped here, not deferred to the caller: the surface is an
	// EMPTY area, and everything the caller places after it must be outside
	// it — over it in the hit test, so a pad button's tap is only ever the
	// button's, and unclipped by it.
	off := op.Offset(r.Min).Push(gtx.Ops)
	cl := clip.Rect{Max: r.Size()}.Push(gtx.Ops)
	event.Op(gtx.Ops, &a.fieldTag)
	semantic.LabelOp(playfieldLabel).Add(gtx.Ops)
	cl.Pop()
	off.Pop()
	a.gest.setField(r.Dx(), cell)
}
