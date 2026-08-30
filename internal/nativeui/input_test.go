package nativeui

import (
	"image"
	"reflect"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
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
		key.NameUpArrow, key.NameSpace, "Z", "X", "C",
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
		{"C", (*engine.Engine).Hold},
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

// gameFrame lays the current screen out through router r and commits the
// frame the way the window loop does, so the focus commands, key filters and
// pointer areas registered during layout take effect for the next one.
func gameFrame(a *App, r *input.Router) {
	ops := new(op.Ops)
	gtx := layout.Context{
		Ops:         ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(1200, 820)),
		Source:      r.Source(),
	}
	a.layout(gtx)
	r.Frame(ops)
}

// press queues a primary mouse click (press + release) at window position x,y.
func press(r *input.Router, x, y float32) {
	r.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(x, y)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(x, y)},
	)
}

// Window positions used by the focus tests: the chat panel is the strip at
// the bottom of the 1200×820 window (its pointer area includes the panel's
// own inset, so a point right at the bottom edge is inside the panel yet over
// no widget — it tests the panel area itself, not the editor's own click
// handling), and the board area's top-left corner, beside the HUD column, is
// empty space: pure paint, so a press there falls through to the screen-wide
// board area. (The middle of the window is no longer safe: the control pad
// flanks the playfield there, and a pad button — a Clickable — takes the keys
// itself for the frame of its press, handleKeys handing them back on the
// next.)
const (
	// The bar's chat button, at the top right of the game screen; and a point
	// on the board that is clear of it AND clear of the chat panel when that
	// is open — the panel takes the bottom drawerFrac of the body, so
	// this sits in the strip left above it.
	chatBtnPressX, chatBtnPressY = 1200 - 6 - (gameBarH-14)/2, gameBarH / 2
	boardPressX, boardPressY     = 250, gameBarH + 40
	// Inside the chat strip, on widgets that consume the press themselves:
	// the editor and the Send button (the press still reaches the strip's
	// area, which encloses them). The strip spans the window's foot.
	editorPressX, editorPressY = 600, 788
	sendPressX, sendPressY     = 1147, 787
)

// TestTouchPressSizesPad checks the runtime half of the touch detection: the
// first press from a touch screen anywhere on the game screen (here, on the
// playfield's empty surroundings) switches the control pad to its thumb-sized
// metrics for good; mouse presses never do.
func TestTouchPressSizesPad(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	a.gameStatus = string(config.GameStatusInProgress)
	var r input.Router
	gameFrame(a, &r)
	press(&r, boardPressX, boardPressY)
	gameFrame(a, &r)
	if a.touchUI {
		t.Fatal("a mouse press switched the pad to touch size")
	}
	r.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Touch, PointerID: 1, Position: f32.Pt(boardPressX, boardPressY)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Touch, PointerID: 1, Position: f32.Pt(boardPressX, boardPressY)},
	)
	gameFrame(a, &r)
	if !a.touchUI {
		t.Fatal("a touch press did not switch the pad to touch size")
	}
	press(&r, boardPressX, boardPressY)
	gameFrame(a, &r)
	if !a.touchUI {
		t.Fatal("a later mouse press switched the pad back")
	}
}

// TestGameFocusFollowsClicks drives the real game screen through a Gio
// input.Router: once the game is playable the board owns the keys; a press
// inside the chat panel hands them to the chat editor, a press anywhere else
// hands them back, and so does Escape while typing; Tab switches either
// way. Before the start there is no contest (the chat editor is the only key
// consumer), so no press moves the keys to the board.
func TestGameFocusFollowsClicks(t *testing.T) {
	a := newTestApp()
	a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	a.gamePlayers = []lobby.PlayerSummary{
		{PlayerID: "alice", Name: "alice", Ready: true},
		{PlayerID: "bob", Name: "bob"},
	}
	a.readyPlayers = a.gamePlayers
	a.screen = screenGame
	var r input.Router

	// Pre-start: the board never takes the keys.
	gameFrame(a, &r)
	press(&r, boardPressX, boardPressY)
	gameFrame(a, &r)
	if r.Source().Focused(&a.boardTag) {
		t.Fatal("board took the keys before the game started")
	}

	// Start: the keys jump to the board.
	a.gameStatus = string(config.GameStatusInProgress)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys when the game became playable")
	}

	// This window has room, so the chat strip is up from the start and the
	// bar's button is a hide/show for it — never a focus change: the keys
	// stay with the board until a click or a Tab moves them.
	if !a.chatVisible() {
		t.Fatal("a 1200x820 window does not show the chat strip by default")
	}
	toggleChat := func() {
		press(&r, chatBtnPressX, chatBtnPressY)
		gameFrame(a, &r)
		gameFrame(a, &r)
	}
	toggleChat()
	if a.chatVisible() {
		t.Fatal("the bar's chat button did not hide the strip")
	}
	toggleChat()
	if !a.chatVisible() {
		t.Fatal("the bar's chat button did not show the strip again")
	}
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("showing and hiding the chat strip moved the keys off the board")
	}

	press(&r, editorPressX, editorPressY)
	gameFrame(a, &r)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("a click in the chat strip did not hand it the keys")
	}
	if r.Source().Focused(&a.boardTag) {
		t.Fatal("board kept the keys while the chat panel was open")
	}
	// A press on the board takes the keys back, and leaves the strip up: the
	// bar shows and hides it, nothing else does.
	press(&r, boardPressX, boardPressY)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("a press on the board did not hand the keys back to it")
	}
	if !a.chatVisible() {
		t.Fatal("a press on the board hid the chat strip")
	}

	press(&r, editorPressX, editorPressY)
	gameFrame(a, &r)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("a second click in the chat strip did not hand it the keys")
	}
	r.Queue(key.Event{Name: key.NameEscape, State: key.Press})
	gameFrame(a, &r)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("Escape in the chat did not hand the keys back to the board")
	}
	if !a.chatVisible() {
		t.Fatal("Escape hid the chat strip; it only moves the keys")
	}

	// Tab toggles — a shifted Tab too. The window delivers Tab keys wrapped
	// as an input.SystemEvent (so that, unclaimed, they drive Gio's generic
	// focus traversal); the switch's explicit filters claim them.
	tab := input.SystemEvent{Event: key.Event{Name: key.NameTab, State: key.Press}}
	shiftTab := input.SystemEvent{Event: key.Event{Name: key.NameTab, Modifiers: key.ModShift, State: key.Press}}
	r.Queue(tab)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("Tab on the board did not hand the keys to the chat editor")
	}
	r.Queue(tab)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("Tab in the chat did not hand the keys back to the board")
	}
	r.Queue(shiftTab)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.gameChatEd) {
		t.Fatal("Shift-Tab on the board did not hand the keys to the chat editor")
	}
	r.Queue(shiftTab)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("Shift-Tab in the chat did not hand the keys back to the board")
	}

	// Presses on the panel's own widgets count as presses in the panel. The
	// Send button is a Clickable, which takes the keys itself at the end of
	// the press frame (its focus command is deferred behind the editor's
	// event processing); the switch hands them on to the editor at the top
	// of the next frame — one the pending focus events make Gio schedule at
	// once — so give each press that second frame.
	for _, pt := range []struct {
		name string
		x, y float32
	}{{"editor", editorPressX, editorPressY}, {"Send button", sendPressX, sendPressY}} {
		// From the board each time, then press the widget.
		press(&r, boardPressX, boardPressY)
		gameFrame(a, &r)
		gameFrame(a, &r)
		press(&r, pt.x, pt.y)
		gameFrame(a, &r)
		gameFrame(a, &r)
		if !r.Source().Focused(&a.gameChatEd) {
			t.Fatalf("a press on the %s did not leave the keys with the chat editor", pt.name)
		}
		gameFrame(a, &r)
		if !r.Source().Focused(&a.gameChatEd) {
			t.Fatalf("the chat editor did not keep the keys after a press on the %s", pt.name)
		}
	}
	// Back to the board for the fresh-screen check below.
	press(&r, boardPressX, boardPressY)
	gameFrame(a, &r)
	gameFrame(a, &r)

	// A new game screen (new engine) observes the start edge afresh: the
	// keys go to the board again even though the chat held them last.
	a.eng = engine.New(nil, "g2", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	gameFrame(a, &r)
	if !r.Source().Focused(&a.boardTag) {
		t.Fatal("board did not take the keys on a fresh game screen")
	}
}
