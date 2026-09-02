package nativeui

import (
	"time"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"jetris/internal/engine"
)

// boardKeyFilters returns the event filters for the game board. The leading
// key.FocusFilter is REQUIRED: in Gio a tag only becomes a focusable key target
// when a key.FocusFilter is registered for it that frame (io/input/router.go:
// "case key.FocusFilter: f.focusable = true"). Without it, key.FocusCmd has
// nothing to focus and the Focus-conditional key.Filters below never match —
// which is exactly why the controls did nothing. The remaining filters capture
// the movement keys only while tag is focused.
func boardKeyFilters(tag event.Tag) []event.Filter {
	// Every game key tolerates Shift and Ctrl, because both are themselves
	// game keys now (hold and rotate CCW): a key pressed while one of them
	// is down arrives carrying its bit, and a filter that names no modifier
	// matches only an unmodified event (io/input/key.go: "e.Modifiers &^
	// (Required|Optional) != 0" is a miss). Without this, holding Shift
	// would silently kill every other control — and the presses of Shift
	// and Ctrl themselves, which carry their own bit, would never arrive at
	// all. ⌘ and Alt are deliberately left out: they are the platform's, and
	// a game key is not what ⌘W should mean.
	const mods = key.ModShift | key.ModCtrl
	fs := []event.Filter{key.FocusFilter{Target: tag}}
	for _, n := range []key.Name{
		key.NameLeftArrow, key.NameRightArrow, key.NameDownArrow, key.NameUpArrow,
		key.NameSpace,
		"Z", "X", "C",
		// WASD, the arrows' left-hand twins (see arrowForKey).
		"W", "A", "S", "D",
		// The Guideline's two modifier controls: Ctrl rotates counter-
		// clockwise (Z's twin) and Shift holds (C's). Gio delivers both as
		// keys of their own — press AND release, on every backend — so they
		// are filtered as keys of their own; Shift needs both edges, since
		// it holds on the release (handleKeys).
		key.NameCtrl, key.NameShift,
	} {
		fs = append(fs, key.Filter{Focus: tag, Name: n, Optional: mods})
	}
	return fs
}

// arrowForKey folds the WASD cluster onto the arrow keys — W↑ A← S↓ D→ — and
// returns every other key unchanged. The left hand's arrows: a player whose
// right hand is on Z/X/C/Space (or on a mouse) steers with the same fingers
// every other game puts there, and the two sets are one control, not two —
// everything downstream (the mapping table, the DAS/ARR machines, the legend)
// sees only the arrow name, so A and ← are the same key held.
//
// The letters reach the board only while it holds the keyboard; a player
// typing in the chat has the editor's focus, so "was" still types "was".
func arrowForKey(name key.Name) key.Name {
	switch name {
	case "W":
		return key.NameUpArrow
	case "A":
		return key.NameLeftArrow
	case "S":
		return key.NameDownArrow
	case "D":
		return key.NameRightArrow
	}
	return name
}

// moveForKey maps a key name to the engine action it triggers. Pure function so
// the control scheme can be unit-tested. ok is false for unmapped keys.
//
// The Guideline's scheme, whole: ← / → move, ↓ soft drop, ↑ / X rotate
// clockwise, Ctrl / Z rotate counter-clockwise, Space hard drop, Shift / C
// hold (a no-op in a game without the hold rule). Ctrl and Shift are keys
// here like any other — Gio delivers a modifier's own press as a key.Event
// named for it.
//
// The table is in arrow names alone: WASD is folded onto them upstream
// (arrowForKey), so A and ← are one entry here, not two.
//
// It is the scheme's table, and no longer its whole dispatch: handleKeys
// sends Z X C and Ctrl straight through it, but runs ← → ↓, Space and Shift
// itself (the DAS/ARR machines, the one-drop-per-press rule, and Shift's
// hold-on-release). Shift and Space stay in the table all the same: it is
// what the scheme IS, and the tests read it as such.
func moveForKey(name key.Name) (func(*engine.Engine), bool) {
	switch name {
	case key.NameLeftArrow:
		return (*engine.Engine).MoveLeft, true
	case key.NameRightArrow:
		return (*engine.Engine).MoveRight, true
	case key.NameDownArrow:
		return (*engine.Engine).MoveDown, true
	case key.NameUpArrow:
		return (*engine.Engine).RotateCW, true
	case key.NameSpace:
		return (*engine.Engine).HardDrop, true
	case "Z":
		return (*engine.Engine).RotateCCW, true
	case "X":
		return (*engine.Engine).RotateCW, true
	case "C", key.NameShift:
		return (*engine.Engine).Hold, true
	case key.NameCtrl:
		return (*engine.Engine).RotateCCW, true
	}
	return nil, false
}

// handleKeys dispatches movement keys directly to the engine while the board
// holds the keyboard. Called every frame on the game screen (ModePlayer only),
// which re-registers the filters Gio expects each frame. The board claims
// focus only when nothing of the game screen's has it: the chat editor keeps
// it for as long as the player has clicked into the chat (handleGameFocus is
// the switch between the two).
//
// ← → and ↓ go through the DAS/ARR machines (autoshift.go — one per axis):
// both their edges feed them — the press's immediate move comes back out
// through emit — and handleAutoShift runs the repeats right after. ↓ takes
// neither knob: no charge (softDAS) and its own rate (softARR, SDF times the
// level's gravity), so a soft drop starts repeating straight away. Space is
// the opposite: one hard drop per physical press, the OS repeat's extra
// presses ignored (dropHeld), or a held space would drop every piece that
// spawned under it — and refused outright for the moment after a piece has
// locked on its own (dropguard.go), so the press that was meant for it cannot
// take the piece behind it too. The remaining keys (rotate, hold) dispatch on
// the press as ever, the OS repeat included. The board's key.FocusEvent arrives in the
// same drain (the FocusFilter registers it): losing the keys — to the chat,
// the modal, a window blur — resets both machines and the held space, so a
// Release the board never saw cannot leave a repeat running.
//
// WASD is folded onto the arrows (arrowForKey) before any of that, so the
// left hand drives the very same machines: A and ← share the shift axis's
// charge, S and ↓ the soft drop's, and a key held on one set repeats exactly
// as it does on the other.
//
// Shift is the one key that acts on its RELEASE. A shifted Tab is still the
// board/chat switch, and holding the piece is not what a player reaching for
// the chat meant: so the press only arms the hold, the release spends it,
// and a Tab in between (handleGameFocus, which runs first in the frame and
// is where Tab is drained) disarms it unspent — as does losing the keys,
// whose release the board would never see. C holds on the press as ever.
func (a *App) handleKeys(gtx C, eng *engine.Engine) {
	tag := &a.boardTag
	if !gtx.Source.Focused(tag) && !gtx.Source.Focused(&a.gameChatEd) {
		gtx.Source.Execute(key.FocusCmd{Tag: tag})
	}
	shiftEmit, softEmit := a.shiftEmit(eng), a.softEmit(eng)
	das := time.Duration(a.dasMs) * time.Millisecond
	arr := time.Duration(a.arrMs) * time.Millisecond
	sarr := a.softARR(eng) // the soft drop's rate: SDF times gravity
	filters := boardKeyFilters(tag)
	for {
		ev, ok := gtx.Source.Event(filters...)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case key.FocusEvent:
			// Not on a pad press's one-frame flap (padFocused): the keys come
			// straight back, and the reset would kill the hold it began.
			if !e.Focus && !a.padFocused(gtx) {
				a.shift.reset()
				a.soft.reset()
				a.dropHeld = false
				a.holdArmed = false // its release will land elsewhere
			}
		case key.Event:
			// WASD first: from here down a press of A is a press of ←, so
			// the two sets share one DAS machine and one dispatch.
			name := arrowForKey(e.Name)
			switch name {
			case key.NameLeftArrow, key.NameRightArrow:
				dir := -1
				if name == key.NameRightArrow {
					dir = 1
				}
				if e.State == key.Press {
					a.shift.press(dir, gtx.Now, das, arr, shiftEmit)
				} else {
					a.shift.release(dir, gtx.Now, das, arr, shiftEmit)
				}
			case key.NameDownArrow:
				// The soft drop's own axis: +1 is one row down, no DAS
				// (softDAS), and its own rate — one row on the press, the
				// next one softARR behind it.
				if e.State == key.Press {
					a.soft.press(1, gtx.Now, softDAS, sarr, softEmit)
				} else {
					a.soft.release(1, gtx.Now, softDAS, sarr, softEmit)
				}
			case key.NameShift:
				// The hold on the RELEASE, and only while the press is still
				// armed: a Tab disarms it (handleGameFocus), so the shifted
				// Tab moves the keys to the chat and leaves the hold
				// unspent. Arming is idempotent — the browser repeats a held
				// modifier's keydown, macOS does not.
				if e.State == key.Press {
					a.holdArmed = true
					continue
				}
				if !a.holdArmed {
					continue
				}
				a.holdArmed = false
				eng.Hold()
			case key.NameSpace:
				// One drop per press, however long it is held: a Press with
				// no Release since the last one is the OS auto-repeat. The
				// press is spent either way — a drop the guard refuses
				// (dropguard.go) does not fire when the guard lifts under a
				// space still held down.
				if e.State != key.Press {
					a.dropHeld = false
					continue
				}
				if a.dropHeld {
					continue
				}
				a.dropHeld = true
				a.hardDrop(gtx, eng)
			default:
				if e.State != key.Press {
					continue
				}
				if move, ok := moveForKey(name); ok {
					move(eng)
				}
			}
		}
	}
}

// handleGameFocus is the in-game keyboard switch between the playfield and the
// chat. Players chat while playing: the keys go to whichever of the two was
// clicked last. The whole game screen is the board's pointer area and the chat
// panel is the chat's — both registered with pointerArea, i.e. as ANCESTORS of
// their widgets, so a press anywhere inside reaches them (after the widget
// that took it, if any). A press that reached the chat panel hands the keys to
// the chat editor; any other press hands them back to the board — as does
// Escape while typing, and the moment the game becomes playable (start,
// rejoin) so the piece answers at once even if the player was mid-sentence
// during the countdown. Tab switches either way without the mouse — a
// shifted Tab too, so an old habit never falls through. (Gio's window wraps
// Tab/Shift-Tab as an input.SystemEvent and cycles the focus through EVERY
// focusable widget when no handler claims it; the explicit filters here
// claim it, so mid-game it only ever toggles between the board and the chat.)
//
// playing is "the keys drive the piece" (a seated player, game in progress,
// not eliminated); outside it there is no contest — the chat editor is the
// only key consumer — but the presses are drained every frame regardless so
// none fire stale once the game starts.
//
// Call it FIRST in the frame, before any widget has processed its events.
// Two Gio router rules make the order matter: a command issued after some
// handler finished its event processing is deferred to the end of the frame
// (so a later widget's own focus command would win, and ours would land a
// frame late), and a command issued while a press is still undrained makes
// the router replay that press without its merged release — the pointer then
// stays "pressed" and later presses reach only the handler it was on, so the
// chat panel would stop seeing clicks (pre-start, the attract button issues
// an invalidate command every frame, which is exactly that case).
func (a *App) handleGameFocus(gtx C, eng *engine.Engine, playing bool) {
	if a.playingSeenEng != eng {
		// A fresh game screen (join, rejoin, spectate): observe from scratch
		// so the first playable frame is a start edge.
		a.playingSeenEng, a.playingSeen = eng, false
	}
	var target event.Tag
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{Target: &a.boardTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Press {
			target = &a.boardTag
			if pe.Source == pointer.Touch {
				// A finger on the game screen: from here on the control
				// pad is laid out at thumb size (controls.go).
				a.touchUI = true
				a.touchPresses++ // the touch diagnostic's count (view_js.go)
			}
		}
	}
	for {
		ev, ok := gtx.Source.Event(pointer.Filter{Target: &a.chatTag, Kinds: pointer.Press})
		if !ok {
			break
		}
		// A press in the chat panel reaches the board area too (it encloses
		// the panel), in the same frame: the chat wins.
		if pe, ok := ev.(pointer.Event); ok && pe.Kind == pointer.Press {
			target = &a.gameChatEd
		}
	}
	for {
		ev, ok := gtx.Source.Event(key.Filter{Focus: &a.gameChatEd, Name: key.NameEscape})
		if !ok {
			break
		}
		if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
			target = &a.boardTag
		}
	}
	// Tab (shifted or not): from the board to the chat and from the chat to
	// the board.
	for _, sw := range []struct{ from, to event.Tag }{
		{&a.boardTag, &a.gameChatEd},
		{&a.gameChatEd, &a.boardTag},
	} {
		for {
			ev, ok := gtx.Source.Event(key.Filter{Focus: sw.from, Name: key.NameTab, Optional: key.ModShift})
			if !ok {
				break
			}
			if ke, ok := ev.(key.Event); ok && ke.State == key.Press {
				// A shifted Tab spends no hold: Shift holds on its release
				// (handleKeys), and this is the Tab that disarms it — here
				// rather than in handleKeys because this drain is the one
				// Tab reaches, and it runs first in the frame. Disarmed even
				// when the Tab moves nothing below, so the combo means the
				// same thing whether or not the chat is on screen.
				a.holdArmed = false
				// Tab moves the keys and nothing else. Into a chat that is
				// not on screen it moves nothing: the strip is shown and
				// hidden from the bar (gamescreen.go), never by typing.
				if sw.to == &a.gameChatEd && !a.chatVisible() {
					continue
				}
				target = sw.to
			}
		}
	}
	if playing && !a.playingSeen {
		target = &a.boardTag
	}
	// A chat that has just been hidden cannot keep the keys: they go back to
	// the board. (The other way round is not automatic — showing the strip
	// only puts it on screen, and the player takes the keys when they mean
	// to type.) Only when nothing louder happened this frame, so a press
	// that landed somewhere specific still wins.
	if target == nil && !a.chatVisible() && gtx.Source.Focused(&a.gameChatEd) {
		target = &a.boardTag
	}
	a.playingSeen = playing
	act := playing
	if target == nil && gtx.Source.Focused(&a.gameChatBtn) {
		// Send is a Clickable, and a Clickable takes the keys on a mouse
		// press (Gio keyboard navigation); it is part of the chat, so pass
		// them straight on to the editor — playing or not.
		target, act = &a.gameChatEd, true
	}
	if target == nil || !act || gtx.Source.Focused(target) {
		return
	}
	gtx.Source.Execute(key.FocusCmd{Tag: target})
	animate(gtx) // repaint with the outline on its new owner
}

// pointerArea lays out w and registers tag as a pointer-input area exactly
// covering it, as an ancestor of w's own input handlers: Gio delivers a press
// to the foremost handler under the pointer and then on up through the areas
// enclosing it, so tag sees every press inside w — on a widget that consumed
// it and on empty space alike. (w is laid out first, into a macro, which is
// what makes its size known before the area is pushed around it.)
func pointerArea(gtx C, tag event.Tag, w layout.Widget) D {
	macro := op.Record(gtx.Ops)
	dims := w(gtx)
	call := macro.Stop()
	defer clip.Rect{Max: dims.Size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, tag)
	call.Add(gtx.Ops)
	return dims
}
