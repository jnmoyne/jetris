package nativeui

import (
	"reflect"
	"testing"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/op"

	"jetricks/internal/engine"
)

// TestBoardKeyFiltersIncludeFocusFilter guards the exact regression that broke
// the controls: without a key.FocusFilter the board tag never becomes a focus
// target, so key.FocusCmd can't focus it and the movement keys are never
// delivered.
func TestBoardKeyFiltersIncludeFocusFilter(t *testing.T) {
	var tag int
	fs := boardKeyFilters(&tag)

	hasFocus := false
	names := map[key.Name]bool{}
	for _, f := range fs {
		switch ff := f.(type) {
		case key.FocusFilter:
			if ff.Target == event.Tag(&tag) {
				hasFocus = true
			}
		case key.Filter:
			names[ff.Name] = true
		}
	}
	if !hasFocus {
		t.Fatal("boardKeyFilters missing key.FocusFilter{Target: tag}; focus will never stick and controls won't work")
	}
	for _, n := range []key.Name{
		key.NameLeftArrow, key.NameRightArrow, key.NameDownArrow,
		key.NameUpArrow, key.NameSpace, "Z", "X",
	} {
		if !names[n] {
			t.Errorf("boardKeyFilters missing key.Filter for %q", n)
		}
	}
}

// TestMoveForKey locks the control scheme: each key maps to the right engine
// action, and unmapped keys return false.
func TestMoveForKey(t *testing.T) {
	cases := []struct {
		name key.Name
		want func(*engine.Engine)
	}{
		{key.NameLeftArrow, (*engine.Engine).MoveLeft},
		{key.NameRightArrow, (*engine.Engine).MoveRight},
		{key.NameDownArrow, (*engine.Engine).MoveDown},
		{key.NameUpArrow, (*engine.Engine).RotateCW},
		{key.NameSpace, (*engine.Engine).HardDrop},
		{"Z", (*engine.Engine).RotateCCW},
		{"X", (*engine.Engine).RotateCW},
	}
	for _, c := range cases {
		got, ok := moveForKey(c.name)
		if !ok {
			t.Errorf("moveForKey(%q): ok=false, want true", c.name)
			continue
		}
		if reflect.ValueOf(got).Pointer() != reflect.ValueOf(c.want).Pointer() {
			t.Errorf("moveForKey(%q): mapped to the wrong action", c.name)
		}
	}
	if _, ok := moveForKey("Q"); ok {
		t.Error("moveForKey(\"Q\"): ok=true, want false for an unmapped key")
	}
}

// TestBoardKeysDeliverWhenFocused drives a real Gio input.Router (mirroring
// gioui.org/io/input TestDeferred) to prove that, with boardKeyFilters, a queued
// arrow-key press is actually delivered once the tag is focused. With the buggy
// filter set (no FocusFilter) the key is NOT delivered.
func TestBoardKeysDeliverWhenFocused(t *testing.T) {
	var r input.Router
	var tag int
	// register re-declares the filters each frame (as the real frame loop does)
	// and drains pending events.
	register := func() []event.Event { return drainEvents(&r, boardKeyFilters(&tag)...) }
	// frame commits ops that register the board tag as a key-input handler —
	// focus-gated key events only route to a tag present in the frame's ops
	// (mirrors event.Op(gtx.Ops, &a.boardTag) in layoutGame).
	frame := func() {
		ops := new(op.Ops)
		event.Op(ops, &tag)
		r.Frame(ops)
	}

	// Register filters and request focus, then commit a frame so the focus takes
	// effect (the focusability granted by key.FocusFilter is what lets this work).
	register()
	r.Source().Execute(key.FocusCmd{Tag: &tag})
	frame()
	if !containsFocus(register(), true) {
		t.Fatal("tag did not gain focus; key.FocusFilter is not making it focusable")
	}

	// Now focused: a queued arrow press is delivered to the focus-gated filter.
	r.Queue(key.Event{Name: key.NameLeftArrow, State: key.Press})
	got := register()
	for _, e := range got {
		if ke, ok := e.(key.Event); ok && ke.Name == key.NameLeftArrow && ke.State == key.Press {
			return // delivered — success
		}
	}
	t.Fatalf("LeftArrow press was not delivered while focused; got %#v", got)
}

func containsFocus(evs []event.Event, want bool) bool {
	for _, e := range evs {
		if fe, ok := e.(key.FocusEvent); ok && fe.Focus == want {
			return true
		}
	}
	return false
}

func drainEvents(r *input.Router, f ...event.Filter) []event.Event {
	var out []event.Event
	for {
		e, ok := r.Source().Event(f...)
		if !ok {
			break
		}
		out = append(out, e)
	}
	return out
}
