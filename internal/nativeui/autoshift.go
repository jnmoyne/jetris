package nativeui

// Keyboard auto-shift — DAS/ARR, the competitive game's ← → handling, in
// place of the OS key repeat (which neither backend suppresses, whose rate no
// two platforms share, and which a player cannot tune):
//
//	press           one shift, at once
//	held DAS ms     the auto-repeat starts
//	every ARR ms    one more shift
//	ARR = 0         the whole slide at once: the piece goes until the wall or
//	                another player's piece stops it, and stays glued there —
//	                a gap that opens (a teammate's piece moves off a shared
//	                board) is taken the frame it appears
//
// Both knobs are in the game menu's HANDLING section (handlingKnobs), in
// milliseconds, 0..maxHandlingMs.
//
// autoShift is the machine: a pure struct in the boardGesture mold — fed key
// edges and the frame clock, emitting through a callback, no router and no
// engine, unit-tested as one (autoshift_test.go). handleKeys (input.go) feeds
// it the frame's presses and releases; handleAutoShift below runs the frame's
// repeats. Its rules:
//
//   - A press of a key already down is the OS auto-repeat leaking through
//     (macOS and the browser both resend key.Press with no Release between;
//     a genuine re-press always saw a Release first): ignored entirely, the
//     DAS clock unmoved.
//   - The most recent press is the active direction. Releasing it hands the
//     keys back to the other key if that one is still held — with its own
//     press time, so a key held past DAS resumes at full charge (one shift
//     at once, the repeat right behind it) rather than re-charging.
//   - Repeats owed but impossible — the wall, a full move buffer, the
//     lock-to-spawn gap, a drop being committed — are dropped, never banked:
//     after every step nextDue >= now, so no burst ever follows a stall.
//     The charge itself survives the gap: a held key past DAS snaps the NEXT
//     piece over the frame it spawns.
//   - Focus loss resets everything (a lost Release must never leave the
//     repeat running); handleKeys does it on the board's key.FocusEvent.
//
// Every emitted shift is a dispatch to the engine's move queue, and a burst
// coalesces into one published batch there (engine/pipeline.go takeMoveGroup),
// so an instant slide costs one or two NATS round trips, not one per cell.

import (
	"fmt"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"jetris/internal/engine"
	"jetris/internal/game"
	"jetris/internal/prefs"
)

const (
	// maxHandlingMs is both knobs' ceiling; defaultDASMs/defaultARRMs their
	// values until the player moves a slider (ARR 0 is the true-ARR slide).
	// The prefs store (the values' home across launches) owns the numbers.
	maxHandlingMs = prefs.MaxHandlingMs
	defaultDASMs  = prefs.DefaultDASMs
	defaultARRMs  = prefs.DefaultARRMs
	// maxAutoShiftPerFrame caps a slow frame's ARR catch-up at the board's
	// width — no run of repeats can mean more than that.
	maxAutoShiftPerFrame = 10
)

// autoShift is the ← → DAS/ARR machine. dir is -1 left, +1 right (the sign of
// a shift's column delta), 0 none. UI goroutine only.
type autoShift struct {
	// leftDown/rightDown is the physical key state, from the Press/Release
	// edges — what dedupes the OS auto-repeat's synthetic presses.
	leftDown, rightDown bool
	// leftAt/rightAt is each key's press time — its DAS charge. It survives
	// the lock-to-spawn gap and dies only on release or reset.
	leftAt, rightAt time.Time
	// dir is the active direction: the most recent press still held.
	dir int
	// nextDue is when the active direction owes its next auto-shift.
	// Invariant on leaving step: never before now.
	nextDue time.Time
}

func (s *autoShift) down(dir int) bool {
	if dir < 0 {
		return s.leftDown
	}
	return s.rightDown
}

func (s *autoShift) pressedAt(dir int) time.Time {
	if dir < 0 {
		return s.leftAt
	}
	return s.rightAt
}

// press feeds a key-down edge. A press of a key already down is the OS
// auto-repeat: ignored. Otherwise the key becomes the active direction.
func (s *autoShift) press(dir int, now time.Time, das, arr time.Duration, emit func(int)) {
	if s.down(dir) {
		return
	}
	if dir < 0 {
		s.leftDown, s.leftAt = true, now
	} else {
		s.rightDown, s.rightAt = true, now
	}
	s.activate(dir, now, das, arr, emit)
}

// release feeds a key-up edge. Releasing the active direction hands the keys
// to the other one if it is still held; a release of a key not seen down
// (a focus regained mid-hold) is a stray and does nothing.
func (s *autoShift) release(dir int, now time.Time, das, arr time.Duration, emit func(int)) {
	if !s.down(dir) {
		return
	}
	if dir < 0 {
		s.leftDown = false
	} else {
		s.rightDown = false
	}
	if s.dir != dir {
		return
	}
	if other := -dir; s.down(other) {
		s.activate(other, now, das, arr, emit)
	} else {
		s.dir = 0
	}
}

// activate makes dir the active direction — a fresh press and a resume share
// it: one shift at once, the next due at the press's DAS expiry, or one ARR
// past now if the charge is already spent (a resume, or DAS 0).
func (s *autoShift) activate(dir int, now time.Time, das, arr time.Duration, emit func(int)) {
	s.dir = dir
	emit(dir)
	due := s.pressedAt(dir).Add(das)
	if !due.After(now) {
		due = now.Add(arr)
	}
	s.nextDue = due
}

// reset forgets everything: held keys, charges, the active direction.
func (s *autoShift) reset() { *s = autoShift{} }

// step runs one frame of the repeat. room is how many moves the buffer may
// take (the gestures' back-pressure); possible is how many columns the piece
// can still shift toward dir — negative when no shift can land right now (no
// piece on the board, a drop committing) — consulted only once due; emit
// dispatches one shift. Returns the room left, a wake time for a timed
// invalidate (zero when none), and whether the caller must animate every
// frame instead (glued at ARR 0, blocked at a wall, or watching for a spawn).
func (s *autoShift) step(now time.Time, arr time.Duration, room int,
	possible func() int, emit func(int)) (roomLeft int, wake time.Time, everyFrame bool) {
	if s.dir == 0 {
		return room, time.Time{}, false
	}
	if now.Before(s.nextDue) {
		return room, s.nextDue, false
	}
	p := possible()
	if p < 0 {
		// The lock-to-spawn gap, or a drop being committed: pause without
		// banking the wait — the spawn gets one immediate shift (ARR>0) or
		// the full slide (ARR 0), never the gap's worth of repeats.
		if s.nextDue.Before(now) {
			s.nextDue = now
		}
		return room, time.Time{}, true
	}
	var n int
	if arr <= 0 {
		n = p // the true-ARR slide: everything the board allows, right now
	} else {
		n = min(1+int(now.Sub(s.nextDue)/arr), maxAutoShiftPerFrame)
	}
	n = min(n, p, room)
	for range n {
		emit(s.dir)
	}
	if arr <= 0 {
		s.nextDue = now // permanently due: glued, re-measured every frame
		return room - n, time.Time{}, true
	}
	s.nextDue = s.nextDue.Add(time.Duration(n) * arr)
	if !s.nextDue.After(now) {
		// Repeats forfeited to the wall, a full buffer or the cap: dropped.
		s.nextDue = now
		return room - n, time.Time{}, true
	}
	return room - n, s.nextDue, false
}

// shiftEmit is the machine's dispatch: a shift now — or held for the next
// piece during the lock-to-spawn gap, exactly like a gesture's
// (handleGestures replays heldMoves the frame the piece appears).
func (a *App) shiftEmit(eng *engine.Engine) func(int) {
	noPiece := eng.Started() && !eng.HasActivePiece()
	return func(dir int) {
		m := engine.MoveLeft
		if dir > 0 {
			m = engine.MoveRight
		}
		if noPiece {
			if len(a.heldMoves) < maxHeldMoves {
				a.heldMoves = append(a.heldMoves, m)
			}
			return
		}
		moveFor(m)(eng)
	}
}

// handlePadShift feeds the on-screen pad's ← → press-and-hold edges to the
// DAS/ARR machine: on a touch device the pad's arms ARE the arrow keys, so
// they shift on the press (not the click's release — handlePadClicks leaves
// these two arms to this function) and, held, auto-repeat with the same
// HANDLING tuning. Pressed() is state, not an event, so edges are detected
// against the last frame's reading — except a tap whose press and release
// both land inside one frame, which the state poll can never see: its click
// (drained here, which is also what brings the button's state up to date)
// is fed as a press-and-release pair. Called before handleAutoShift, which
// runs the repeats an edge fed here starts.
func (a *App) handlePadShift(gtx C, eng *engine.Engine, active bool) {
	active = active && eng != nil
	emit := func(int) {}
	if eng != nil {
		emit = a.shiftEmit(eng)
	}
	das := time.Duration(a.dasMs) * time.Millisecond
	arr := time.Duration(a.arrMs) * time.Millisecond
	pads := [...]struct {
		btn *widget.Clickable
		was *bool
		dir int
	}{
		{&a.padLeft, &a.padLeftWas, -1},
		{&a.padRight, &a.padRightWas, +1},
	}
	for _, p := range pads {
		clicked := false
		for p.btn.Clicked(gtx) {
			clicked = true
		}
		down := active && p.btn.Pressed()
		switch {
		case down && !*p.was:
			a.shift.press(p.dir, gtx.Now, das, arr, emit)
		case !down && *p.was:
			a.shift.release(p.dir, gtx.Now, das, arr, emit)
		case clicked && !*p.was && active:
			// The whole tap inside one frame: press and release together.
			a.shift.press(p.dir, gtx.Now, das, arr, emit)
			a.shift.release(p.dir, gtx.Now, das, arr, emit)
		}
		*p.was = down
	}
}

// padFocused reports a pad Clickable (or the HOLD box) holding the keys: the
// one-frame flap of a pad press — a Clickable takes the keys on its press
// (Gio keyboard navigation) and handleKeys hands them back the next frame —
// which must not read as the board losing the keyboard: the machine would
// reset the very hold the press began (and a pad tap mid-keyboard-hold would
// kill the keyboard's repeat).
func (a *App) padFocused(gtx C) bool {
	for _, b := range []*widget.Clickable{
		&a.padUp, &a.padLeft, &a.padDown, &a.padRight,
		&a.padCCW, &a.padCW, &a.padDrop, &a.padHold, &a.holdBoxBtn,
	} {
		if gtx.Source.Focused(b) {
			return true
		}
	}
	return false
}

// autoShiftPossible is how many columns the piece can shift toward dir right
// now: measured from IntentPiece (queued and in-flight moves already played
// out — what makes the per-frame re-measure idempotent) against the board,
// CanPlaceCoop so a teammate's active piece blocks like a wall on a shared
// board (on a private one there is none and it is CanPlace). Negative when
// no shift can land: no piece (the lock-to-spawn gap) or a drop committing.
func (a *App) autoShiftPossible(eng *engine.Engine, dir int) int {
	if eng.PieceCommitting() {
		return -1
	}
	p, ok := eng.IntentPiece()
	if !ok {
		return -1
	}
	pf, idx := eng.Playfield(), eng.PlayerIdx()
	n := 0
	for n < pf.Width {
		p.Col += dir
		if !game.CanPlaceCoop(p, pf, idx) {
			break
		}
		n++
	}
	return n
}

// handleAutoShift runs the frame's auto-repeats — what a held ← or → owes by
// the frame clock — back-pressured like the gestures so a slide never queues
// past the engine. Called right after handleKeys (which feeds the machine the
// frame's presses and releases); active under the same gate, so the repeat
// behaves under the leave modal exactly like a held OS-repeat key did, and
// any other screen state resets the machine.
func (a *App) handleAutoShift(gtx C, eng *engine.Engine, active bool) {
	if !active {
		a.shift.reset()
		return
	}
	if a.shift.dir == 0 {
		return
	}
	room := max(0, gestureBufferCap-len(eng.BufferedMoves()))
	arr := time.Duration(a.arrMs) * time.Millisecond
	possible := func() int { return a.autoShiftPossible(eng, a.shift.dir) }
	emit := func(dir int) {
		if dir < 0 {
			eng.MoveLeft()
		} else {
			eng.MoveRight()
		}
	}
	_, wake, every := a.shift.step(gtx.Now, arr, room, possible, emit)
	switch {
	case every:
		animate(gtx) // glued, blocked or waiting for the spawn: look again next frame
	case !wake.IsZero():
		gtx.Execute(op.InvalidateCmd{At: wake}) // the DAS expiry or the next ARR tick, exactly
	}
}

// clampHandlingMs clamps a knob value to its range.
func clampHandlingMs(v int) int { return min(max(v, 0), maxHandlingMs) }

// SetHandling sets the DAS/ARR knobs (ms, clamped to 0..maxHandlingMs) and
// mirrors them into the menu sliders — the loaded preferences' way in
// (cmd/jetris/main.go), before Run.
func (a *App) SetHandling(dasMs, arrMs int) {
	a.dasMs, a.arrMs = clampHandlingMs(dasMs), clampHandlingMs(arrMs)
	a.dasFloat.Value = float32(a.dasMs) / maxHandlingMs
	a.arrFloat.Value = float32(a.arrMs) / maxHandlingMs
}

// persistHandling saves the knobs. A failure is silent: the game screen has
// no error line, and the in-memory values still drive this session.
func (a *App) persistHandling() {
	if a.handlingSave == nil {
		return
	}
	_ = a.handlingSave(prefs.Handling{DASMs: a.dasMs, ARRMs: a.arrMs})
}

// handlingKnobs is the menu's HANDLING section: a slider per knob, DAS and
// ARR, with a live ms readout. The sliders snap to 5 ms detents and drive
// dasMs/arrMs directly — the machine reads those every frame, so a change
// applies to the very next press or tick. widget.Float is drag-only (no
// Clickable), so tuning never takes the keys from the board.
func (a *App) handlingKnobs(gtx C) D {
	row := func(label string, f *widget.Float, val *int) layout.Widget {
		return func(gtx C) D {
			if f.Update(gtx) {
				*val = int(f.Value*maxHandlingMs/5+0.5) * 5
				f.Value = float32(*val) / maxHandlingMs // the detent, thumb included
				a.handlingDirty = true
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Dp(30) // DAS over ARR, sliders aligned
					return a.pixelLabelFit(gtx, unit.Sp(10), label, colFg)
				}),
				layout.Flexed(1, func(gtx C) D {
					gtx.Constraints.Max.Y = gtx.Dp(20) // a row, not a touch target
					s := material.Slider(a.th, f)
					s.Color = colAccent
					return s.Layout(gtx)
				}),
				layout.Rigid(hSpacer(6)),
				layout.Rigid(func(gtx C) D {
					return a.pixelLabelFit(gtx, unit.Sp(10), fmt.Sprintf("%3d ms", *val), colMuted)
				}),
			)
		}
	}
	// A finished drag (both thumbs at rest) persists the pair once.
	if a.handlingDirty && !a.dasFloat.Dragging() && !a.arrFloat.Dragging() {
		a.handlingDirty = false
		a.persistHandling()
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.header("HANDLING")),
		layout.Rigid(row("DAS", &a.dasFloat, &a.dasMs)),
		layout.Rigid(spacer(4)),
		layout.Rigid(row("ARR", &a.arrFloat, &a.arrMs)),
	)
}
