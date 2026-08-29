//go:build js

package nativeui

import (
	"syscall/js"

	"gioui.org/app"
)

// Browser keyboard focus.
//
// Gio's browser backend takes its key events from a hidden <input> it places
// next to the canvas, and focuses that input only while a text editor has the
// focus: every focus change to a non-editor tag (the board, a button) closes
// the platform text input, which the backend answers by blurring the element
// (io/input/key.go Focus → TextInputClose; app/os_js.go ShowTextInput(false)
// → blur). Nothing focuses it again until the player clicks into an editor,
// so the moment the board takes the keys — game start, Escape out of the
// chat, or simply leaving the name field — the browser stops delivering
// keystrokes to the game, and only the on-screen pad still works. The
// desktop backends have no such step: keys arrive as long as the window is
// active. That is the behaviour restored here: while this page is the active
// document, the hidden input holds the browser's focus. Gio's own listeners
// on it then see every key, and its bookkeeping (Config.Focused from the
// focus/blur events, editor text through the input event) works as before.
//
// Touch-first devices are left alone: focusing the input there raises the
// on-screen keyboard.

// attachView receives the window's platform handles once the backend has
// built its elements, and wires the focus keeper to them.
func (a *App) attachView(e app.ViewEvent) {
	je, ok := e.(app.JSViewEvent)
	if !ok || !je.Valid() {
		return
	}
	if mediaMatches("(any-pointer: coarse)") {
		// A touch screen — a tablet's, a phone's, one beside a mouse too:
		// the control pad is laid out at thumb size from the first frame
		// (controls.go), rather than after the first tap reaches the game.
		a.touchUI = true
	}
	// The page sets window.jetrisTouchDebug when opened with ?touchdebug=1:
	// it then counts the raw DOM touch events itself and shows them beside
	// the counts reported here (touchDebugFrame), so a stall can be placed —
	// touches not reaching the page, not reaching the game, or reaching it
	// and doing nothing.
	a.touchDebug = js.Global().Get("jetrisTouchDebug").Truthy()
	keepKeyboardFocus(je.Element)
}

// frameBegin and frameEnd tell the page (window.jetrisInFrame) when a frame
// is in progress. A frame can park mid-way on an engine lock that is held
// across a NATS round trip — the consumer spawning the next piece after a
// lock does exactly that — and in the browser a parked frame hands the
// event loop back to the browser: an input event delivered then lands in
// Gio's router after this frame's handlers have drained, and Router.Frame
// discards it. The page's touch shim holds such events until frameEnd
// (web/index.html), so no touch made right after a drop is lost.
func (a *App) frameBegin() { js.Global().Set("jetrisInFrame", true) }
func (a *App) frameEnd()   { js.Global().Set("jetrisInFrame", false) }

// touchDebugFrame reports the game's side of the touch diagnostic to the
// page, once per frame while it is on: window.jetrisTouch = {presses,
// frames, queued} — touch presses that reached the game screen, frames laid
// out, and moves waiting in the engine's queue.
func (a *App) touchDebugFrame() {
	if !a.touchDebug {
		return
	}
	queued, watchdog := 0, int64(0)
	if eng := a.getEngine(); eng != nil {
		queued = len(eng.BufferedMoves())
		watchdog = eng.WatchdogSpawns()
	}
	js.Global().Set("jetrisTouch", map[string]any{
		"presses": a.touchPresses, "frames": a.frames, "queued": queued,
		// Moves of ours the server rejected (the rainbow flash) and pieces
		// the watchdog had to force: a delivered input that did nothing.
		"casDrops": a.casFlashes.Load(), "watchdogSpawns": watchdog,
		// The lock-to-spawn gap (handleGestures): the board without a piece,
		// the moves held for the next one.
		"spawnGapLast": a.spawnGapLast.Milliseconds(), "spawnGapMax": a.spawnGapMax.Milliseconds(),
		"held": len(a.heldMoves), "heldPeak": a.heldPeak,
	})
}

// mediaMatches evaluates a CSS media query against the page (false where the
// browser has no matchMedia).
func mediaMatches(query string) bool {
	win := js.Global().Get("window")
	if !win.Get("matchMedia").Truthy() {
		return false
	}
	mq := win.Call("matchMedia", query)
	return mq.Truthy() && mq.Get("matches").Bool()
}

// keepKeyboardFocus makes the hidden input inside Gio's container element
// (the #giowindow div, to which the backend appends its canvas and input)
// the page's standing focus target. The listeners live as long as the page.
func keepKeyboardFocus(cont js.Value) {
	doc := js.Global().Get("document")
	win := js.Global().Get("window")
	input := cont.Call("querySelector", "input")
	canvas := cont.Call("querySelector", "canvas")
	if !input.Truthy() || !canvas.Truthy() || mediaMatches("(pointer: coarse)") {
		return
	}
	focusOpts := map[string]any{"preventScroll": true}
	focus := js.FuncOf(func(this js.Value, args []js.Value) any {
		if doc.Call("hasFocus").Bool() && !doc.Get("activeElement").Equal(input) {
			input.Call("focus", focusOpts)
		}
		return nil
	})
	// A blur — Gio closing its text input, or a click that landed off the
	// canvas — is undone once it has settled, unless the page itself lost the
	// focus (another window or tab); the window's focus event brings the keys
	// back when the player returns.
	refocus := js.FuncOf(func(this js.Value, args []js.Value) any {
		win.Call("setTimeout", focus, 0)
		return nil
	})
	// Tab would walk the browser's focus out of the page (nothing else on it
	// can take it), and it is the in-game board/chat switch. Gio's own
	// keydown listener was added first and still sees the key.
	swallowTab := js.FuncOf(func(this js.Value, args []js.Value) any {
		if ev := args[0]; ev.Get("key").String() == "Tab" {
			ev.Call("preventDefault")
		}
		return nil
	})
	input.Call("addEventListener", "blur", refocus)
	input.Call("addEventListener", "keydown", swallowTab)
	canvas.Call("addEventListener", "mousedown", focus)
	win.Call("addEventListener", "focus", focus)
	focus.Invoke()
}
