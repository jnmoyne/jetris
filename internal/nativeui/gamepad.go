package nativeui

import (
	"time"

	"jetris/internal/engine"
	"jetris/internal/gamepad"
	"jetris/internal/prefs"
)

// The game controller: the third way to move a piece, beside the keyboard
// (input.go) and the touch pad and gestures (controls.go, gesture.go). The
// platform layer (internal/gamepad) reads the pad and hands over button
// edges; what each button DOES is fixed here, in gamepadMap — the console
// convention rather than the keyboard's: the D-pad and the left stick move
// and soft-drop, D-pad up (and △ / Y) hard-drops, the face buttons turn the
// piece and the shoulders hold it. The scheme is not the player's to change
// — no dialog, no file — and the legend's GAMEPAD section says what it is,
// from the first press of a pad on.
//
// Every edge is turned into the move it makes and then dispatched exactly
// as handleKeys dispatches a key: ← → into the shift machine, ↓ into the
// soft drop's, both on the frame's clock and the HANDLING knobs, so DAS and
// ARR are the pad's too; the hard drop through the guard (dropguard.go),
// one per press; hold and the rotations on the press. What the keyboard
// does not need and the pad does is gamepadHeld: the D-pad's left and the
// stick's left are two buttons for one move, and a hand that lets one go
// while the other is still pushed has not released the move. The count
// per move is that: the machine hears the first press and the last release.
//
// The pad is read on the keyboard's gate (game.go) and never the chat's:
// it cannot type, so it keeps driving the piece while the chat has the
// keys. It is drained every frame regardless, presses made while the game
// is not playable dying here — as the touch pad's clicks do — and the
// reset on leaving play (handleAutoShift) forgets what it held.

// gamepadMap is the scheme: the move each button makes. A button not here
// (Back, Start, the stick clicks) makes none.
var gamepadMap = map[gamepad.Button]prefs.KeyAction{
	gamepad.DPadLeft:   prefs.KeyMoveLeft,
	gamepad.StickLeft:  prefs.KeyMoveLeft,
	gamepad.DPadRight:  prefs.KeyMoveRight,
	gamepad.StickRight: prefs.KeyMoveRight,
	gamepad.DPadDown:   prefs.KeySoftDrop,
	gamepad.StickDown:  prefs.KeySoftDrop,
	gamepad.DPadUp:     prefs.KeyHardDrop,
	gamepad.North:      prefs.KeyHardDrop,
	gamepad.East:       prefs.KeyRotateCW,
	gamepad.South:      prefs.KeyRotateCCW,
	gamepad.West:       prefs.KeyRotate180,
	gamepad.LB:         prefs.KeyHold,
	gamepad.RB:         prefs.KeyHold,
	gamepad.LT:         prefs.KeyHold,
	gamepad.RT:         prefs.KeyHold,
}

// gamepadRows is the legend's GAMEPAD section: the scheme, a line per move,
// the face buttons named both ways (Xbox letter, PlayStation glyph). hold
// adds the hold line, as the KEYS section's does.
func gamepadRows(hold bool) []controlsRow {
	rows := []controlsRow{
		{key: "DPAD ← → · STICK", move: "move · hold slides"},
		{key: "DPAD ↓ · STICK ↓", move: "soft drop · hold falls"},
		{key: "B ○", move: "rotate CW"},
		{key: "A ✕", move: "rotate CCW"},
		{key: "X □", move: "rotate 180"},
		{key: "DPAD ↑ · Y △", move: "hard drop"},
	}
	if hold {
		rows = append(rows, controlsRow{key: "LB RB LT RT", move: "hold"})
	}
	return rows
}

// gamepadReset forgets what the pad held: the reset on leaving play.
func (a *App) gamepadReset() {
	clear(a.gamepadHeld)
	a.gamepadDropHeld = false
}

// handleGamepad drains the pad's edges and, while active, dispatches the
// moves they make — see the file's doc. Called right after handleKeys,
// under its gate, and before handleAutoShift, which runs the repeats a
// press here starts.
func (a *App) handleGamepad(gtx C, eng *engine.Engine, active bool) {
	src := a.gamepad
	if src == nil {
		return
	}
	evs := src.Drain()
	if !a.gamepadSeen && (len(evs) > 0 || src.Connected()) {
		a.gamepadSeen = true
	}
	if !active || eng == nil {
		// Spent, not answered — and nothing is held into the game.
		if len(evs) > 0 {
			a.gamepadReset()
		}
		return
	}
	if a.gamepadHeld == nil {
		a.gamepadHeld = map[prefs.KeyAction]int{}
	}
	shiftEmit, softEmit := a.shiftEmit(eng), a.softEmit(eng)
	das := time.Duration(a.dasMs) * time.Millisecond
	arr := time.Duration(a.arrMs) * time.Millisecond
	sarr := a.softARR(eng)
	for _, e := range evs {
		act, ok := gamepadMap[e.Button]
		if !ok {
			continue
		}
		// One press and one release per move, however many of its buttons
		// are pushed: the first press and the last release are the edges.
		if e.Pressed {
			a.gamepadHeld[act]++
			if a.gamepadHeld[act] > 1 {
				continue
			}
		} else {
			if a.gamepadHeld[act] == 0 {
				continue // a release of a button never seen pressed: a stray
			}
			a.gamepadHeld[act]--
			if a.gamepadHeld[act] > 0 {
				continue
			}
		}
		switch act {
		case prefs.KeyMoveLeft, prefs.KeyMoveRight:
			dir := -1
			if act == prefs.KeyMoveRight {
				dir = 1
			}
			if e.Pressed {
				a.shift.press(dir, gtx.Now, das, arr, shiftEmit)
			} else {
				a.shift.release(dir, gtx.Now, das, arr, shiftEmit)
			}
		case prefs.KeySoftDrop:
			if e.Pressed {
				a.soft.press(1, gtx.Now, softDAS, sarr, softEmit)
			} else {
				a.soft.release(1, gtx.Now, softDAS, sarr, softEmit)
			}
		case prefs.KeyHardDrop:
			// One drop per press, through the guard, however long the
			// button is held.
			if !e.Pressed {
				a.gamepadDropHeld = false
				continue
			}
			if a.gamepadDropHeld {
				continue
			}
			a.gamepadDropHeld = true
			a.hardDrop(gtx, eng)
		default:
			if !e.Pressed {
				continue
			}
			if move, ok := moveForAction(act); ok {
				move(eng)
			}
		}
	}
	if src.Pending() {
		// Edges landed while this frame ran (the backend's wake may have
		// fallen inside it): look again next frame.
		animate(gtx)
	}
}
