package nativeui

// Keyboard auto-shift — DAS/ARR, the competitive game's ← → ↓ handling, in
// place of the OS key repeat (which neither backend suppresses, whose rate no
// two platforms share, and which a player cannot tune):
//
//	press           one step, at once
//	held DAS ms     the ← → auto-repeat starts (↓ skips the charge, softDAS)
//	every ARR ms    one more shift
//	ARR = 0         the whole slide at once: the piece goes until the wall or
//	                another player's piece stops it, and stays glued there —
//	                a gap that opens (a teammate's piece moves off a shared
//	                board) is taken the frame it appears
//
// ↓ is the Guideline's soft drop and takes neither knob: it falls at SDF
// times the level's gravity (softARR), the third knob, and at maxSDF it is
// the same instant slide ARR 0 is — to the floor, resting there unlocked.
//
// All three are in the game menu's HANDLING section (handlingKnobs): DAS and
// ARR in milliseconds, 0..maxHandlingMs; SDF a multiple of gravity,
// minSDF..maxSDF. GUARD, the section's fourth slider, tunes nothing here: it
// is the accidental-drop guard's window (dropguard.go), in milliseconds on
// the same scale as the first two.
//
// autoShift is the machine: a pure struct in the boardGesture mold — fed key
// edges and the frame clock, emitting through a callback, no router and no
// engine, unit-tested as one (autoshift_test.go). The App runs ONE PER AXIS:
// a.shift for the ← → pair (mutually exclusive — the newest press has the
// piece) and a.soft for the lone ↓ soft drop, on its own knob and
// independent of it, so a piece slid and soft-dropped at once takes both.
// handleKeys (input.go) feeds them the frame's presses and releases;
// handleAutoShift below runs the frame's repeats.
//
// Space is deliberately not one of them: a hard drop is one drop per physical
// press, no DAS and no ARR (handleKeys drops the OS repeat's extra presses on
// the floor), or a held space would drop every piece that spawned under it.
//
// The machines' rules:
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
	// The soft drop factor: ↓ falls at sdf times the level's gravity, and at
	// maxSDF instantly (softARR). The prefs store owns the numbers.
	minSDF     = prefs.MinSDF
	maxSDF     = prefs.MaxSDF
	defaultSDF = prefs.DefaultSDF
	// The accidental-drop guard's window (dropguard.go): milliseconds on the
	// same 0..maxHandlingMs scale as DAS and ARR, 0 being off.
	defaultDropGuardMs = prefs.DefaultDropGuardMs
	// maxAutoShiftPerFrame caps a slow frame's ARR catch-up at ten steps — a
	// board's width of shifts, or as many rows of soft drop.
	maxAutoShiftPerFrame = 10
	// softDAS is the ↓ key's charge: none. A soft drop is a fall rate, not a
	// slide, so it wants no delay before the repeat — the first row goes on
	// the press and the next one softARR behind it, the DAS knob being the
	// shift's alone. (At maxSDF that makes a tap of ↓ the whole drop to the
	// floor, the piece landing without locking, exactly as ARR 0 slides a
	// shift into the wall.)
	softDAS = 0
)

// autoShift is one axis's DAS/ARR machine: the ← → pair (dir -1 left, +1
// right — the sign of a shift's column delta) or the lone ↓ soft drop (dir
// +1, one row down); dir 0 is no key held. UI goroutine only.
type autoShift struct {
	// negDown/posDown is the physical state of the axis's two keys, the
	// dir<0 one (←, which the soft drop's axis simply never has) and the
	// dir>0 one (→, ↓), from the Press/Release edges — what dedupes the OS
	// auto-repeat's synthetic presses.
	negDown, posDown bool
	// negAt/posAt is each key's press time — its DAS charge. It survives
	// the lock-to-spawn gap and dies only on release or reset.
	negAt, posAt time.Time
	// dir is the active direction: the most recent press still held.
	dir int
	// nextDue is when the active direction owes its next auto-shift.
	// Invariant on leaving step: never before now.
	nextDue time.Time
}

func (s *autoShift) down(dir int) bool {
	if dir < 0 {
		return s.negDown
	}
	return s.posDown
}

func (s *autoShift) pressedAt(dir int) time.Time {
	if dir < 0 {
		return s.negAt
	}
	return s.posAt
}

// press feeds a key-down edge. A press of a key already down is the OS
// auto-repeat: ignored. Otherwise the key becomes the active direction.
func (s *autoShift) press(dir int, now time.Time, das, arr time.Duration, emit func(int)) {
	if s.down(dir) {
		return
	}
	if dir < 0 {
		s.negDown, s.negAt = true, now
	} else {
		s.posDown, s.posAt = true, now
	}
	s.activate(dir, now, das, arr, emit)
}

// release feeds a key-up edge. Releasing the active direction hands the keys
// to the other one if it is still held (the soft drop's axis has no other
// key, so it simply stops); a release of a key not seen down (a focus
// regained mid-hold) is a stray and does nothing.
func (s *autoShift) release(dir int, now time.Time, das, arr time.Duration, emit func(int)) {
	if !s.down(dir) {
		return
	}
	if dir < 0 {
		s.negDown = false
	} else {
		s.posDown = false
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
// take (the gestures' back-pressure); possible is how many steps the piece
// can still take toward dir — columns for a shift, rows for a soft drop, and
// negative when none can land right now (no piece on the board, a drop
// committing) — consulted only once due; emit dispatches one step. Returns
// the room left, a wake time for a timed invalidate (zero when none), and
// whether the caller must animate every
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
	// Not in the gap behind the player's own hard drop: the engine holds
	// the moves made there for the next piece itself, in the order they
	// were made (Engine.AwaitingSpawn), so a shift dispatched now keeps its
	// place among the rotations dispatched around it.
	noPiece := eng.Started() && !eng.HasActivePiece() && !eng.AwaitingSpawn()
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

// softInterval is the soft drop's repeat interval: the level's gravity
// divided by the SDF knob — the Guideline's "soft drop is N times the fall
// speed", so ↓ stays ahead of gravity at every level instead of running at
// some wall-clock rate the speed curve overtakes. maxSDF is 0, the machine's
// instant slide (to the floor, resting there unlocked); anything faster than
// a millisecond a row is that in all but name, and the floor is also what
// keeps a fast level's division from rounding to 0 and meaning instant by
// accident. Pure, so the knob's whole range is testable without an engine.
func softInterval(sdf, level int) time.Duration {
	if sdf >= maxSDF {
		return 0
	}
	return max(game.GravityInterval(level)/time.Duration(max(sdf, minSDF)), time.Millisecond)
}

// softARR is the soft drop's interval right now: the knob at the engine's
// level. eng may be nil (a frame with no engine): level 1 stands in.
func (a *App) softARR(eng *engine.Engine) time.Duration {
	level := game.MinLevel
	if eng != nil {
		level = eng.Level()
	}
	return softInterval(a.sdf, level)
}

// softEmit is the soft-drop machine's dispatch: one row down, now. A step
// owed while the board has no piece is dropped rather than held for the next
// one (unlike shiftEmit's): a soft drop banked across the spawn would push
// the new piece down before the player had seen it, and the repeat takes it
// the frame it appears anyway.
func (a *App) softEmit(eng *engine.Engine) func(int) {
	noPiece := eng.Started() && !eng.HasActivePiece()
	return func(int) {
		if noPiece {
			return
		}
		moveFor(engine.MoveDown)(eng)
	}
}

// handlePadShift feeds the on-screen pad's ← → ↓ press-and-hold edges to the
// DAS/ARR machines: on a touch device the pad's arms ARE the arrow keys, so
// they move on the press (not the click's release — handlePadClicks leaves
// these three arms to this function) and, held, auto-repeat on the same
// HANDLING tuning as the keys, each arm on its own key's terms (the ↓ arm at
// softDAS, repeating on softARR). Pressed() is state, not an event, so edges are detected
// against the last frame's reading — except a tap whose press and release
// both land inside one frame, which the state poll can never see: its click
// (drained here, which is also what brings the button's state up to date)
// is fed as a press-and-release pair. Called before handleAutoShift, which
// runs the repeats an edge fed here starts.
func (a *App) handlePadShift(gtx C, eng *engine.Engine, active bool) {
	active = active && eng != nil
	shiftEmit, softEmit := func(int) {}, func(int) {}
	if eng != nil {
		shiftEmit, softEmit = a.shiftEmit(eng), a.softEmit(eng)
	}
	das := time.Duration(a.dasMs) * time.Millisecond
	arr := time.Duration(a.arrMs) * time.Millisecond
	sarr := a.softARR(eng)
	pads := [...]struct {
		btn      *widget.Clickable
		was      *bool
		s        *autoShift
		dir      int
		das, arr time.Duration
		emit     func(int)
	}{
		{&a.padLeft, &a.padLeftWas, &a.shift, -1, das, arr, shiftEmit},
		{&a.padRight, &a.padRightWas, &a.shift, +1, das, arr, shiftEmit},
		{&a.padDown, &a.padDownWas, &a.soft, +1, softDAS, sarr, softEmit},
	}
	for _, p := range pads {
		clicked := false
		for p.btn.Clicked(gtx) {
			clicked = true
		}
		down := active && p.btn.Pressed()
		switch {
		case down && !*p.was:
			p.s.press(p.dir, gtx.Now, p.das, p.arr, p.emit)
		case !down && *p.was:
			p.s.release(p.dir, gtx.Now, p.das, p.arr, p.emit)
		case clicked && !*p.was && active:
			// The whole tap inside one frame: press and release together.
			p.s.press(p.dir, gtx.Now, p.das, p.arr, p.emit)
			p.s.release(p.dir, gtx.Now, p.das, p.arr, p.emit)
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
		&a.padCCW, &a.pad180, &a.padCW, &a.padDrop, &a.padHold, &a.holdBoxBtn,
	} {
		if gtx.Source.Focused(b) {
			return true
		}
	}
	return false
}

// autoShiftPossible is how many steps of (dRow, dCol) the piece can still
// take right now — the columns a shift has left, the rows a soft drop has:
// measured from IntentPiece (queued and in-flight moves already played out —
// what makes the per-frame re-measure idempotent) against the board,
// CanPlaceCoop so a teammate's active piece blocks like a wall on a shared
// board (on a private one there is none and it is CanPlace). Negative when
// no step can land: no piece (the lock-to-spawn gap) or a drop committing.
func (a *App) autoShiftPossible(eng *engine.Engine, dRow, dCol int) int {
	if eng.PieceCommitting() {
		return -1
	}
	p, ok := eng.IntentPiece()
	if !ok {
		return -1
	}
	pf, idx := eng.Playfield(), eng.PlayerIdx()
	limit := pf.Width
	if dRow != 0 {
		limit = pf.Height
	}
	n := 0
	for n < limit {
		p.Row += dRow
		p.Col += dCol
		if !game.CanPlaceCoop(p, pf, idx) {
			break
		}
		n++
	}
	return n
}

// handleAutoShift runs the frame's auto-repeats — what a held ←, → or ↓ owes
// by the frame clock — back-pressured like the gestures so a slide never
// queues past the engine. Called right after handleKeys (which feeds the
// machines the frame's presses and releases); active under the same gate, so
// a repeat behaves under the leave modal exactly like a held OS-repeat key
// did, and any other screen state resets both machines (and forgets a held
// space, whose Release the board will not be there to see).
//
// The shift goes first and the soft drop takes the room it leaves: the two
// axes share one move buffer, and a slide that is already under way should
// not lose its cells to the drop riding along with it.
func (a *App) handleAutoShift(gtx C, eng *engine.Engine, active bool) {
	if !active {
		a.shift.reset()
		a.soft.reset()
		a.dropHeld = false
		a.holdArmed = false
		return
	}
	if a.shift.dir == 0 && a.soft.dir == 0 {
		return
	}
	room := max(0, gestureBufferCap-eng.QueuedPlayerMoves())
	var wake time.Time
	everyFrame := false
	// step runs one machine, at its own repeat rate and on the room left
	// over, folding its wake-up into the frame's: the earliest of the two,
	// since either may be owed first.
	step := func(s *autoShift, arr time.Duration, dRow, dCol int, emit func(int)) {
		possible := func() int { return a.autoShiftPossible(eng, dRow*s.dir, dCol*s.dir) }
		var w time.Time
		var every bool
		room, w, every = s.step(gtx.Now, arr, room, possible, emit)
		everyFrame = everyFrame || every
		if !w.IsZero() && (wake.IsZero() || w.Before(wake)) {
			wake = w
		}
	}
	step(&a.shift, time.Duration(a.arrMs)*time.Millisecond, 0, 1, func(dir int) {
		if dir < 0 {
			eng.MoveLeft()
		} else {
			eng.MoveRight()
		}
	})
	step(&a.soft, a.softARR(eng), 1, 0, func(int) { eng.MoveDown() })
	switch {
	case everyFrame:
		animate(gtx) // glued, blocked or waiting for the spawn: look again next frame
	case !wake.IsZero():
		gtx.Execute(op.InvalidateCmd{At: wake}) // the DAS expiry or the next ARR tick, exactly
	}
}

// clampHandlingMs clamps a knob value to its range.
func clampHandlingMs(v int) int { return min(max(v, 0), maxHandlingMs) }

// knobRange maps a HANDLING slider's 0..1 position to its knob's integer
// value and back, snapping to the knob's detents so the thumb comes to rest
// exactly where the value is.
type knobRange struct{ lo, hi, step int }

var (
	msRange  = knobRange{0, maxHandlingMs, 5}
	sdfRange = knobRange{minSDF, maxSDF, 1}
)

func (r knobRange) value(pos float32) int {
	return r.lo + int(pos*float32(r.hi-r.lo)/float32(r.step)+0.5)*r.step
}

func (r knobRange) pos(v int) float32 {
	return float32(min(max(v, r.lo), r.hi)-r.lo) / float32(r.hi-r.lo)
}

// SetHandling sets the four knobs — DAS, ARR and the drop guard in ms
// (0..maxHandlingMs), SDF a multiple of gravity (minSDF..maxSDF) — and
// mirrors them into the menu sliders: the loaded preferences' way in
// (cmd/jetris/main.go), before Run.
func (a *App) SetHandling(dasMs, arrMs, sdf, dropGuardMs int) {
	a.dasMs, a.arrMs = clampHandlingMs(dasMs), clampHandlingMs(arrMs)
	a.sdf = min(max(sdf, minSDF), maxSDF)
	a.dropGuardMs = clampHandlingMs(dropGuardMs)
	a.dasFloat.Value = msRange.pos(a.dasMs)
	a.arrFloat.Value = msRange.pos(a.arrMs)
	a.sdfFloat.Value = sdfRange.pos(a.sdf)
	a.dropGuardFloat.Value = msRange.pos(a.dropGuardMs)
}

// persistHandling saves the knobs. A failure is silent: the game screen has
// no error line, and the in-memory values still drive this session.
func (a *App) persistHandling() {
	if a.handlingSave == nil {
		return
	}
	_ = a.handlingSave(prefs.Handling{
		DASMs:       a.dasMs,
		ARRMs:       a.arrMs,
		SDF:         a.sdf,
		DropGuardMs: a.dropGuardMs,
	})
}

// handlingKnobs is the menu's HANDLING section: a slider per knob — DAS and
// ARR, the shift's, in ms, SDF, the soft drop's, as a multiple of gravity
// (MAX being instant), and GUARD, the hard drop's, in ms again: how long a
// piece that locked on its own keeps the drop unavailable (dropguard.go), OFF
// at 0. Each snaps to its own detents and drives dasMs/arrMs/sdf/dropGuardMs
// directly — the machines read those every frame, so a change applies to the
// very next press or tick. widget.Float is drag-only (no Clickable), so
// tuning never takes the keys from the board.
func (a *App) handlingKnobs(gtx C) D {
	row := func(label string, f *widget.Float, val *int, r knobRange, text func(int) string) layout.Widget {
		return func(gtx C) D {
			if f.Update(gtx) {
				*val = r.value(f.Value)
				f.Value = r.pos(*val) // the detent, thumb included
				a.handlingDirty = true
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D {
					// The longest label's width, so the four sliders start on
					// the same column (the pixel face is monospace: one em a
					// character, GUARD the widest at five).
					gtx.Constraints.Min.X = gtx.Dp(50)
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
					return a.pixelLabelFit(gtx, unit.Sp(10), text(*val), colMuted)
				}),
			)
		}
	}
	// A finished drag (every thumb at rest) persists the set once.
	if a.handlingDirty && !a.dasFloat.Dragging() && !a.arrFloat.Dragging() &&
		!a.sdfFloat.Dragging() && !a.dropGuardFloat.Dragging() {
		a.handlingDirty = false
		a.persistHandling()
	}
	// The readouts are the same width in the monospace pixel face, so the
	// three sliders start and end on the same columns.
	ms := func(v int) string { return fmt.Sprintf("%3d ms", v) }
	factor := func(v int) string {
		if v >= maxSDF {
			return "   MAX" // instant: straight to the floor, resting there
		}
		return fmt.Sprintf("%5dx", v)
	}
	guard := func(v int) string {
		if v <= 0 {
			return "   OFF" // no guard: every press is the player's, as it always was
		}
		return ms(v)
	}
	// Each row is a part of its own to the tour (tutorial.go), the header
	// going with the first.
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(a.tutMarked(tutHUDDAS, a.header("HANDLING"))),
		layout.Rigid(a.tutMarked(tutHUDDAS, row("DAS", &a.dasFloat, &a.dasMs, msRange, ms))),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.tutMarked(tutHUDARR, row("ARR", &a.arrFloat, &a.arrMs, msRange, ms))),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.tutMarked(tutHUDSDF, row("SDF", &a.sdfFloat, &a.sdf, sdfRange, factor))),
		layout.Rigid(spacer(4)),
		layout.Rigid(a.tutMarked(tutHUDGuard, row("GUARD", &a.dropGuardFloat, &a.dropGuardMs, msRange, guard))),
	)
}
