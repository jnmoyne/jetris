//go:build js

package nativeui

import (
	"syscall/js"

	"gioui.org/app"
	"gioui.org/layout"
)

// Browser keyboard focus.
//
// Gio's browser backend takes its key events from a hidden text element it
// places next to the canvas (a <textarea> since Gio v0.10; an <input> before),
// and focuses that element only while a text editor has the focus: every
// focus change to a non-editor tag (the board, a button) closes the platform
// text input, which the backend answers by blurring the element
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
	a.deviceHint, a.deviceHinted = browserDevice(), true
	// The page sets window.jetrisTouchDebug when opened with ?touchdebug=1:
	// it then counts the raw DOM touch events itself and shows them beside
	// the counts reported here (touchDebugFrame), so a stall can be placed —
	// touches not reaching the page, not reaching the game, or reaching it
	// and doing nothing.
	a.touchDebug = js.Global().Get("jetrisTouchDebug").Truthy()
	a.bv.keyInput = je.Element.Call("querySelector", "textarea, input")
	keepKeyboardFocus(je.Element)
	// Closing the tab is the only way out of the browser build, and it
	// gives no DestroyEvent: leave the lobby on pagehide instead — the
	// presence delete and the departure journal go out on a goroutine while
	// the page is still unloading, best effort. Should they not make it,
	// the presence TTL reports the departure a few minutes on.
	if !a.bv.unloadHooked {
		a.bv.unloadHooked = true
		js.Global().Get("window").Call("addEventListener", "pagehide", js.FuncOf(func(js.Value, []js.Value) any {
			go a.teardown()
			return nil
		}))
	}
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
func (a *App) frameEnd(gtx layout.Context) {
	js.Global().Set("jetrisInFrame", false)
	a.syncEnterKey(gtx)
}

// syncEnterKey labels the phone keyboard's return key for the editor the
// frame just handed the focus to (enterKeyHint): Go on the login screen,
// Send on a chat line. It runs right after the window has focused the hidden
// text element and set its keyboard attributes for the editor's input hint
// (app/os_js.go ShowTextInput, then SetInputHint), in the same task, so the
// keyboard coming up reads the label along with the rest. Gio sets no
// enterkeyhint of its own, and a textarea's return key otherwise says
// "return" — for a field whose Enter is the Play button.
func (a *App) syncEnterKey(gtx layout.Context) {
	if !a.bv.keyInput.Truthy() {
		return
	}
	if hint := a.enterKeyHint(gtx); hint != a.bv.enterKey {
		a.bv.enterKey = hint
		a.bv.keyInput.Call("setAttribute", "enterkeyhint", hint)
	}
}

// browserView is the page's side of the window: the backend's hidden text
// element (the one the on-screen keyboard is up for, attachView) and the
// return-key label last set on it (syncEnterKey).
type browserView struct {
	keyInput     js.Value
	enterKey     string
	unloadHooked bool // the pagehide leave (attachView) is registered
}

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

// browserDevice reads the page for the machine it is on (formfactor.go):
// a fine pointer — a mouse, a trackpad — is a desktop whatever the window
// size; a coarse one is a phone or a tablet by the SCREEN's short side in CSS
// pixels, not the window's. The screen is the honest measure: a phone browser
// shrinks its viewport for the URL bar and the keyboard, and a tablet's
// split-screen half is still a tablet — its pad, its reach, its thumbs.
func browserDevice() deviceKind {
	if !mediaMatches("(any-pointer: coarse)") {
		return deviceDesktop
	}
	scr := js.Global().Get("screen")
	if !scr.Truthy() {
		return deviceTablet
	}
	w, h := scr.Get("width"), scr.Get("height")
	if !w.Truthy() || !h.Truthy() {
		return deviceTablet
	}
	if min(w.Int(), h.Int()) <= phoneShortSideDp {
		return devicePhone
	}
	return deviceTablet
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

// keepKeyboardFocus makes the hidden key-input element inside Gio's container
// element (the #giowindow div, to which the backend appends its canvas and
// that element) the page's standing focus target. The listeners live as long
// as the page.
func keepKeyboardFocus(cont js.Value) {
	doc := js.Global().Get("document")
	win := js.Global().Get("window")
	// The element by either tag: Gio v0.10 made it a <textarea> (v0.8 had an
	// <input>), and it is the only one of its kind in the container. Looking
	// for one tag alone is how the keys died at the v0.8 → v0.10 jump — the
	// query missed, the keeper stood down, and the board went deaf the moment
	// it took the focus.
	input := cont.Call("querySelector", "textarea, input")
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
