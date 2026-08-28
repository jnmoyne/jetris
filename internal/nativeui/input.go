package nativeui

import (
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
	return []event.Filter{
		key.FocusFilter{Target: tag},
		key.Filter{Focus: tag, Name: key.NameLeftArrow},
		key.Filter{Focus: tag, Name: key.NameRightArrow},
		key.Filter{Focus: tag, Name: key.NameDownArrow},
		key.Filter{Focus: tag, Name: key.NameUpArrow},
		key.Filter{Focus: tag, Name: key.NameSpace},
		key.Filter{Focus: tag, Name: "Z"},
		key.Filter{Focus: tag, Name: "X"},
		key.Filter{Focus: tag, Name: "C"},
	}
}

// moveForKey maps a key name to the engine action it triggers. Pure function so
// the control scheme can be unit-tested. ok is false for unmapped keys.
//
// ← / → move, ↓ soft drop, ↑ / X rotate clockwise, Z rotate counter-clockwise,
// Space hard drop, C hold (the Guideline's key; a no-op in a game without the
// hold rule — Shift, the Guideline's other hold key, is not mapped: it is a
// modifier, and a shifted Tab is still the board/chat focus switch).
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
	case "C":
		return (*engine.Engine).Hold, true
	}
	return nil, false
}

// handleKeys dispatches movement keys directly to the engine while the board
// holds the keyboard. Called every frame on the game screen (ModePlayer only),
// which re-registers the filters Gio expects each frame. The board claims
// focus only when nothing of the game screen's has it: the chat editor keeps
// it for as long as the player has clicked into the chat (handleGameFocus is
// the switch between the two).
func (a *App) handleKeys(gtx C, eng *engine.Engine) {
	tag := &a.boardTag
	if !gtx.Source.Focused(tag) && !gtx.Source.Focused(&a.gameChatEd) {
		gtx.Source.Execute(key.FocusCmd{Tag: tag})
	}
	filters := boardKeyFilters(tag)
	for {
		ev, ok := gtx.Source.Event(filters...)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		if move, ok := moveForKey(ke.Name); ok {
			move(eng)
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
				target = sw.to
			}
		}
	}
	if playing && !a.playingSeen {
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
