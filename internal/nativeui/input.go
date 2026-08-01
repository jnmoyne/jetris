package nativeui

import (
	"gioui.org/io/event"
	"gioui.org/io/key"

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
	}
}

// moveForKey maps a key name to the engine action it triggers. Pure function so
// the control scheme can be unit-tested. ok is false for unmapped keys.
//
// ← / → move, ↓ soft drop, ↑ / X rotate clockwise, Z rotate counter-clockwise,
// Space hard drop.
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
	}
	return nil, false
}

// handleKeys keeps the game board focused and dispatches movement keys directly
// to the engine. Called every frame on the game screen (ModePlayer only), which
// re-registers the filters Gio expects each frame.
func (a *App) handleKeys(gtx C, eng *engine.Engine) {
	tag := &a.boardTag
	if !gtx.Source.Focused(tag) {
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
