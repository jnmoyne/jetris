package nativeui

import (
	"image"
	"reflect"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/unit"

	"jetris/internal/config"
	"jetris/internal/engine"
	"jetris/internal/lobby"
)

// gestureRig drives a boardGesture with synthetic touch events — a 300 px
// wide playfield of 30 px cells at 1 px/dp — and collects what it emits. The
// frame clock is base + the event's time, as a frame draining the event
// would see it; step(after) is a later frame. room starts high enough never
// to bind; the buffer tests set it.
type gestureRig struct {
	g     boardGesture
	m     unit.Metric
	base  time.Time
	room  int
	moves []engine.MoveType
}

func newGestureRig() *gestureRig {
	return &gestureRig{
		g:    boardGesture{fieldW: 300, cell: 30},
		m:    unit.Metric{PxPerDp: 1, PxPerSp: 1},
		base: time.Unix(1000, 0),
		room: 100,
	}
}

func (r *gestureRig) emit(m engine.MoveType) { r.moves = append(r.moves, m) }

func (r *gestureRig) feed(kind pointer.Kind, id pointer.ID, x, y float32, at time.Duration) {
	r.feedFrom(pointer.Touch, kind, id, x, y, at)
}

func (r *gestureRig) feedFrom(src pointer.Source, kind pointer.Kind, id pointer.ID, x, y float32, at time.Duration) {
	ev := pointer.Event{Kind: kind, Source: src, PointerID: id, Position: f32.Pt(x, y), Time: at}
	r.room = r.g.feed(ev, r.m, r.base.Add(at), r.room, r.emit)
}

// step is a frame at base+after with no events of its own.
func (r *gestureRig) step(after time.Duration, room int) {
	r.room = r.g.step(r.m, r.base.Add(after), room, r.emit)
}

func (r *gestureRig) want(t *testing.T, want ...engine.MoveType) {
	t.Helper()
	if len(want) == 0 {
		want = nil
	}
	if !reflect.DeepEqual(r.moves, want) {
		t.Fatalf("moves = %v, want %v", r.moves, want)
	}
}

func (r *gestureRig) wantFingers(t *testing.T, n int) {
	t.Helper()
	if len(r.g.fingers) != n {
		t.Fatalf("fingers tracked = %d, want %d", len(r.g.fingers), n)
	}
}

const ms = time.Millisecond

func TestGestureTapRotates(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 220, 300, 0)
	r.feed(pointer.Release, 1, 220, 300, 90*ms)
	r.want(t, engine.RotateCW)
	r.wantFingers(t, 0)

	r = newGestureRig()
	r.feed(pointer.Press, 1, 80, 300, 0)
	r.feed(pointer.Release, 1, 80, 300, 90*ms)
	r.want(t, engine.RotateCCW)

	// A thumb wobbles: a few px of drift inside the slop is still a tap.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 220, 300, 0)
	r.feed(pointer.Drag, 1, 226, 305, 50*ms)
	r.feed(pointer.Release, 1, 225, 306, 120*ms)
	r.want(t, engine.RotateCW)

	// At 2 px/dp the slop is 24 px: a 20 px wobble is still a tap.
	r = newGestureRig()
	r.m.PxPerDp = 2
	r.feed(pointer.Press, 1, 220, 300, 0)
	r.feed(pointer.Drag, 1, 240, 300, 50*ms)
	r.feed(pointer.Release, 1, 240, 300, 120*ms)
	r.want(t, engine.RotateCW)
}

func TestGestureLongPressDoesNothing(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 220, 300, 0)
	r.feed(pointer.Release, 1, 220, 300, 400*ms)
	r.want(t)
	r.wantFingers(t, 0)
}

func TestGestureSwipeShifts(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 100, 300, 0)
	r.feed(pointer.Drag, 1, 116, 300, 20*ms) // past the slop, half a cell: one column
	r.want(t, engine.MoveRight)
	r.feed(pointer.Drag, 1, 150, 300, 40*ms)
	r.feed(pointer.Drag, 1, 190, 300, 60*ms)
	r.want(t, engine.MoveRight, engine.MoveRight, engine.MoveRight)
	// Back to where it started within the same swipe: the columns come back.
	r.feed(pointer.Drag, 1, 100, 300, 120*ms)
	r.feed(pointer.Release, 1, 100, 300, 140*ms)
	r.want(t, engine.MoveRight, engine.MoveRight, engine.MoveRight, engine.MoveLeft, engine.MoveLeft, engine.MoveLeft)
}

func TestGestureDragSoftDrops(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 150, 130, 300*ms) // a slow finger: 0.1 dp/ms
	r.feed(pointer.Drag, 1, 150, 160, 600*ms)
	r.want(t, engine.MoveDown, engine.MoveDown)
	// Rising does nothing; coming back down resumes past the depth reached.
	r.feed(pointer.Drag, 1, 150, 120, 900*ms)
	r.feed(pointer.Drag, 1, 150, 165, 1200*ms)
	r.want(t, engine.MoveDown, engine.MoveDown)
	r.feed(pointer.Drag, 1, 150, 195, 1500*ms)
	r.feed(pointer.Release, 1, 150, 195, 1600*ms)
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown)
}

func TestGestureAxisDrift(t *testing.T) {
	// A sideways swipe drooping most of a cell does not soft-drop.
	r := newGestureRig()
	r.feed(pointer.Press, 1, 100, 100, 0)
	r.feed(pointer.Drag, 1, 160, 127, 50*ms)
	r.feed(pointer.Release, 1, 160, 127, 80*ms)
	r.want(t, engine.MoveRight, engine.MoveRight)

	// A soft drop wobbling most of a cell sideways does not shift…
	r = newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 177, 190, 300*ms)
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown)
	// …but a deliberate whole cell sideways does.
	r.feed(pointer.Drag, 1, 186, 190, 400*ms)
	r.feed(pointer.Release, 1, 186, 190, 450*ms)
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown, engine.MoveRight)
}

func TestGestureFlickHardDrops(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 150, 140, 10*ms)
	r.feed(pointer.Drag, 1, 150, 200, 25*ms)
	r.feed(pointer.Drag, 1, 152, 240, 40*ms)
	r.want(t) // nothing steps during the flick
	r.feed(pointer.Release, 1, 152, 240, 45*ms)
	r.want(t, engine.MoveHardDrop)

	// Fast, then a stop before lifting: no flick — the rows are owed.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 150, 200, 20*ms)
	r.want(t)
	r.feed(pointer.Release, 1, 150, 200, 300*ms)
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown)

	// Fast but short of flickMinTravel: a soft drop of its one row, no hard drop.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 150, 130, 10*ms)
	r.feed(pointer.Release, 1, 150, 130, 12*ms)
	r.want(t, engine.MoveDown)
}

// TestGestureFastFingerThatStopsResumes: a finger that moves fast and then
// holds still sends nothing more, so its last speed would keep it a flick in
// progress forever; after gestureStill of silence the frames step the rows
// it covered, without waiting for the release.
func TestGestureFastFingerThatStopsResumes(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 150, 200, 20*ms)
	r.want(t)
	r.step(50*ms, 100) // 30 ms of silence: still a flick in progress
	r.want(t)
	if !r.g.pending() {
		t.Fatal("rows owed, pending() = false")
	}
	r.step(200*ms, 100) // 180 ms of silence: stopped
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown)
	if r.g.pending() {
		t.Fatal("caught up, pending() = true")
	}
	r.feed(pointer.Release, 1, 150, 200, 300*ms) // no flick, nothing left to flush
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown)
}

func TestGestureSwipeUpHolds(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 150, 300, 0)
	r.feed(pointer.Drag, 1, 150, 280, 20*ms) // heading up, short of holdSwipe
	r.want(t)
	r.feed(pointer.Drag, 1, 150, 235, 40*ms)
	r.want(t, engine.MoveHold)
	r.feed(pointer.Drag, 1, 150, 200, 60*ms) // no repeat
	r.feed(pointer.Release, 1, 150, 200, 80*ms)
	r.want(t, engine.MoveHold)

	r = newGestureRig()
	r.feed(pointer.Press, 1, 150, 300, 0)
	r.feed(pointer.Drag, 1, 150, 270, 20*ms)
	r.feed(pointer.Release, 1, 150, 270, 40*ms)
	r.want(t)
}

// TestGestureUpTurnsIntoDrag: a finger that starts upward but turns sideways
// or comes back down is a drag after all, not a dead gesture.
func TestGestureUpTurnsIntoDrag(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 150, 300, 0)
	r.feed(pointer.Drag, 1, 150, 280, 20*ms) // up
	r.feed(pointer.Drag, 1, 190, 290, 40*ms) // now mostly sideways: a column
	r.feed(pointer.Release, 1, 190, 290, 60*ms)
	r.want(t, engine.MoveRight)

	r = newGestureRig()
	r.feed(pointer.Press, 1, 150, 300, 0)
	r.feed(pointer.Drag, 1, 150, 280, 20*ms)  // up
	r.feed(pointer.Drag, 1, 150, 340, 300*ms) // back down past the press: a row
	r.feed(pointer.Release, 1, 150, 340, 320*ms)
	r.want(t, engine.MoveDown)
}

// TestGestureFingersAreIndependent: a mouse never gestures; each finger is
// its own gesture — a resting finger blocks nothing, a second finger's tap
// lands while the first is down; a cancel ends them all.
func TestGestureFingersAreIndependent(t *testing.T) {
	r := newGestureRig()
	r.feedFrom(pointer.Mouse, pointer.Press, 1, 220, 300, 0)
	r.feedFrom(pointer.Mouse, pointer.Drag, 1, 150, 300, 20*ms)
	r.feedFrom(pointer.Mouse, pointer.Release, 1, 150, 300, 40*ms)
	r.want(t)
	r.wantFingers(t, 0)

	// Two taps, the second finger down and up while the first is down.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 220, 300, 0)
	r.feed(pointer.Press, 2, 80, 300, 10*ms)
	r.wantFingers(t, 2)
	r.feed(pointer.Release, 2, 80, 300, 50*ms)
	r.want(t, engine.RotateCCW)
	r.feed(pointer.Release, 1, 220, 300, 100*ms)
	r.want(t, engine.RotateCCW, engine.RotateCW)
	r.wantFingers(t, 0)

	// A thumb resting on the board's edge (down for seconds, never moving)
	// does not block another finger's swipe.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 5, 300, 0)
	r.feed(pointer.Press, 2, 100, 200, 500*ms)
	r.feed(pointer.Drag, 2, 150, 200, 520*ms)
	r.feed(pointer.Release, 2, 150, 200, 540*ms)
	r.want(t, engine.MoveRight, engine.MoveRight)
	r.feed(pointer.Release, 1, 5, 300, 3000*ms) // a long press: nothing
	r.want(t, engine.MoveRight, engine.MoveRight)

	// A drag with one finger and a rotate tap with another both land.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	r.feed(pointer.Drag, 1, 150, 130, 300*ms)
	r.feed(pointer.Press, 2, 250, 300, 310*ms)
	r.feed(pointer.Release, 2, 250, 300, 380*ms)
	r.feed(pointer.Drag, 1, 150, 160, 600*ms)
	r.feed(pointer.Release, 1, 150, 160, 700*ms)
	r.want(t, engine.MoveDown, engine.RotateCW, engine.MoveDown)

	// A cancel — whoever's, even the router's synthetic mouse-sourced one
	// naming no pointer — ends every gesture; the fingers' releases are moot.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 100, 300, 0)
	r.feed(pointer.Drag, 1, 150, 300, 20*ms)
	r.want(t, engine.MoveRight, engine.MoveRight)
	r.feedFrom(pointer.Mouse, pointer.Cancel, 0, 0, 0, 30*ms)
	r.wantFingers(t, 0)
	r.feed(pointer.Release, 1, 200, 300, 60*ms)
	r.want(t, engine.MoveRight, engine.MoveRight)

	// A finger pressing again under its old id (its release was lost)
	// starts over; and past maxFingers the oldest is forgotten.
	r = newGestureRig()
	r.feed(pointer.Press, 1, 100, 300, 0)
	r.feed(pointer.Drag, 1, 150, 300, 20*ms)
	r.feed(pointer.Press, 1, 220, 300, 1000*ms)
	r.feed(pointer.Release, 1, 220, 300, 1050*ms)
	r.want(t, engine.MoveRight, engine.MoveRight, engine.RotateCW)
	for id := pointer.ID(10); id < 10+maxFingers+1; id++ {
		r.feed(pointer.Press, id, 80, 300, 2000*ms)
	}
	r.wantFingers(t, maxFingers)
	if r.g.finger(10) != nil {
		t.Fatal("the oldest finger was not forgotten past maxFingers")
	}
}

func TestGestureRoomBoundsSteps(t *testing.T) {
	r := newGestureRig()
	r.room = 1
	r.feed(pointer.Press, 1, 100, 300, 0)
	r.feed(pointer.Drag, 1, 190, 300, 20*ms) // three columns asked, one slot free
	r.want(t, engine.MoveRight)
	if !r.g.pending() {
		t.Fatal("two columns owed, pending() = false")
	}
	// The next frame, with room again, catches up.
	r.step(40*ms, 2)
	r.want(t, engine.MoveRight, engine.MoveRight, engine.MoveRight)
	if r.g.pending() || r.room != 0 {
		t.Fatalf("caught up: pending() = %v, room = %d", r.g.pending(), r.room)
	}
	// Further travel with no room waits; the release flushes what it can.
	r.feed(pointer.Drag, 1, 280, 300, 40*ms)
	r.want(t, engine.MoveRight, engine.MoveRight, engine.MoveRight)
	r.room = 1
	r.feed(pointer.Release, 1, 280, 300, 60*ms)
	r.want(t, engine.MoveRight, engine.MoveRight, engine.MoveRight, engine.MoveRight)
	if r.g.pending() {
		t.Fatal("pending() after release")
	}
}

func TestGestureFieldChangeResets(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 100, 300, 0)
	r.feed(pointer.Drag, 1, 150, 300, 20*ms)
	r.want(t, engine.MoveRight, engine.MoveRight)
	r.g.setField(200, 20) // the board re-planned under the finger
	r.wantFingers(t, 0)
	r.feed(pointer.Drag, 1, 190, 300, 40*ms)
	r.feed(pointer.Release, 1, 190, 300, 60*ms)
	r.want(t, engine.MoveRight, engine.MoveRight)
	// The same geometry again is no change.
	r.feed(pointer.Press, 1, 100, 300, 100*ms)
	r.g.setField(200, 20)
	r.wantFingers(t, 1)
}

// TestMoveForDispatches checks the move → engine-method table against a
// transport-less engine: dispatched moves queue in its buffer in order.
func TestMoveForDispatches(t *testing.T) {
	eng := engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
	all := []engine.MoveType{engine.MoveLeft, engine.MoveRight, engine.MoveDown, engine.RotateCW, engine.RotateCCW, engine.MoveHardDrop, engine.MoveHold}
	for _, m := range all {
		moveFor(m)(eng)
	}
	if got := eng.BufferedMoves(); !reflect.DeepEqual(got, all) {
		t.Fatalf("buffered = %v, want %v", got, all)
	}
}

// touch queues one touch event of finger id at window position (x, y),
// stamped at. Moves are queued as pointer.Move, as the backends send them:
// the router rewrites a pressed pointer's Move into a Drag.
func touch(r *input.Router, kind pointer.Kind, id pointer.ID, x, y float32, at time.Duration) {
	r.Queue(pointer.Event{Kind: kind, Source: pointer.Touch, PointerID: id, Position: f32.Pt(x, y), Time: at})
}

// semanticBounds is the window-px rectangle of the widget carrying the
// semantic label in the router's tree of the last frame.
func semanticBounds(r *input.Router, label string) (image.Rectangle, bool) {
	var find func([]input.SemanticNode) (image.Rectangle, bool)
	find = func(nodes []input.SemanticNode) (image.Rectangle, bool) {
		for _, n := range nodes {
			if n.Desc.Label == label {
				return n.Desc.Bounds, true
			}
			if b, ok := find(n.Children); ok {
				return b, true
			}
		}
		return image.Rectangle{}, false
	}
	return find(r.AppendSemantics(nil))
}

// nearestButton is the window-px rectangle of the semantic button (every
// pad button is one: a material Clickable) whose center is nearest pt.
func nearestButton(r *input.Router, pt image.Point) (image.Rectangle, bool) {
	var best image.Rectangle
	found, bestD := false, 0
	var walk func([]input.SemanticNode)
	walk = func(nodes []input.SemanticNode) {
		for _, n := range nodes {
			if n.Desc.Class == semantic.Button {
				d := n.Desc.Bounds.Min.Add(n.Desc.Bounds.Size().Div(2)).Sub(pt)
				if dd := d.X*d.X + d.Y*d.Y; !found || dd < bestD {
					best, found, bestD = n.Desc.Bounds, true, dd
				}
			}
			walk(n.Children)
		}
	}
	walk(r.AppendSemantics(nil))
	return best, found
}

// TestPlayfieldGesturesDriveThePiece drives the real game screen through a
// Gio router: touch gestures on the playfield — found on screen by its
// semantic label — queue the engine moves the recognizer resolves them to
// (the transport-less engine never drains its buffer, so BufferedMoves is
// the exact list); a mouse press on it queues nothing; a touch tap on a pad
// button queues that button's one move and nothing else; nothing queues
// before the start.
func TestPlayfieldGesturesDriveThePiece(t *testing.T) {
	type game struct {
		a     *App
		r     *input.Router
		field image.Rectangle
		cell  float32
	}
	newGame := func(t *testing.T, status config.GameStatus) game {
		t.Helper()
		a := newTestApp()
		// Thumb-sized from the first frame, as the browser build on a
		// tablet is (the media query); otherwise the first touch press
		// would re-plan the board under the finger.
		a.touchUI = true
		a.eng = engine.New(nil, "g1", "alice", "bob", config.ModeCooperative, engine.ModePlayer, 0, 0, 0)
		a.gamePlayers = []lobby.PlayerSummary{{PlayerID: "alice", Name: "alice", Ready: true}}
		a.readyPlayers = a.gamePlayers
		a.screen = screenGame
		a.gameStatus = string(status)
		r := new(input.Router)
		gameFrame(a, r)
		field, ok := semanticBounds(r, playfieldLabel)
		if !ok {
			t.Fatal("no playfield in the semantic tree")
		}
		if a.gest.cell <= 0 || a.gest.fieldW != field.Dx() {
			t.Fatalf("recorded geometry: cell %d, width %d; playfield on screen %v", a.gest.cell, a.gest.fieldW, field)
		}
		return game{a: a, r: r, field: field, cell: float32(a.gest.cell)}
	}
	center := func(g game) (float32, float32) {
		return float32(g.field.Min.X + g.field.Dx()/2), float32(g.field.Min.Y + g.field.Dy()/2)
	}
	want := func(t *testing.T, g game, want ...engine.MoveType) {
		t.Helper()
		if len(want) == 0 {
			want = nil
		}
		if got := g.a.eng.BufferedMoves(); !reflect.DeepEqual(got, want) {
			t.Fatalf("buffered = %v, want %v", got, want)
		}
	}

	t.Run("tap rotates", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		cx, cy := center(g)
		touch(g.r, pointer.Press, 1, cx+g.cell, cy, 0)
		touch(g.r, pointer.Release, 1, cx+g.cell, cy, 80*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.RotateCW)

		g = newGame(t, config.GameStatusInProgress)
		cx, cy = center(g)
		touch(g.r, pointer.Press, 1, cx-g.cell, cy, 0)
		touch(g.r, pointer.Release, 1, cx-g.cell, cy, 80*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.RotateCCW)
	})
	t.Run("swipe shifts", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		cx, cy := center(g)
		x0 := cx - 1.5*g.cell
		touch(g.r, pointer.Press, 1, x0, cy, 0)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, x0+1.2*g.cell, cy, 20*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.MoveRight)
		touch(g.r, pointer.Move, 1, x0+3*g.cell, cy, 40*ms)
		touch(g.r, pointer.Release, 1, x0+3*g.cell, cy, 60*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.MoveRight, engine.MoveRight, engine.MoveRight)
	})
	t.Run("drag soft-drops", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		cx, cy := center(g)
		y0 := cy - 2*g.cell
		touch(g.r, pointer.Press, 1, cx, y0, 0)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, cx, y0+g.cell, 300*ms)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, cx, y0+2*g.cell, 600*ms)
		touch(g.r, pointer.Release, 1, cx, y0+2*g.cell, 700*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.MoveDown, engine.MoveDown)
	})
	t.Run("flick hard-drops", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		cx, cy := center(g)
		y0 := cy - 2*g.cell
		touch(g.r, pointer.Press, 1, cx, y0, 0)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, cx, y0+2*g.cell, 16*ms)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, cx, y0+4*g.cell, 32*ms)
		touch(g.r, pointer.Release, 1, cx, y0+4*g.cell, 40*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.MoveHardDrop)
	})
	t.Run("swipe up holds", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		cx, cy := center(g)
		y0 := cy + g.cell
		touch(g.r, pointer.Press, 1, cx, y0, 0)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, cx, y0-60, 30*ms)
		touch(g.r, pointer.Release, 1, cx, y0-60, 60*ms)
		gameFrame(g.a, g.r)
		want(t, g, engine.MoveHold)
	})
	t.Run("mouse press is no gesture", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		cx, cy := center(g)
		press(g.r, cx+g.cell, cy)
		gameFrame(g.a, g.r)
		want(t, g)
		if !g.r.Source().Focused(&g.a.boardTag) {
			t.Fatal("the press did not leave the keys with the board")
		}
	})
	// A pad button sits OVER the gesture surface, which runs the width of the
	// board column (gestureSurface): the button is laid out after it, so the
	// press is the button's alone — one move, not the button's move and a
	// rotate from the tap underneath it. That is the whole reason the two are
	// allowed to overlap, so it is checked on a button that really does.
	t.Run("pad button fires once", func(t *testing.T) {
		g := newGame(t, config.GameStatusInProgress)
		btn, ok := nearestButton(g.r, image.Pt(g.field.Min.X, g.field.Min.Y+g.field.Dy()/2))
		if !ok {
			t.Fatal("no pad button in the semantic tree")
		}
		if !btn.Overlaps(g.field) {
			t.Fatalf("the nearest button %v does not overlap the surface %v — the overlap this checks is gone", btn, g.field)
		}
		bx, by := float32(btn.Min.X+btn.Dx()/2), float32(btn.Min.Y+btn.Dy()/2)
		touch(g.r, pointer.Press, 1, bx, by, 0)
		touch(g.r, pointer.Release, 1, bx, by, 60*ms)
		gameFrame(g.a, g.r)
		gameFrame(g.a, g.r)
		if got := g.a.eng.BufferedMoves(); len(got) != 1 {
			t.Fatalf("buffered = %v, want exactly the button's one move", got)
		}
	})
	t.Run("nothing before the start", func(t *testing.T) {
		g := newGame(t, config.GameStatusCreated)
		cx, cy := center(g)
		x0 := cx - 1.5*g.cell
		touch(g.r, pointer.Press, 1, x0, cy, 0)
		gameFrame(g.a, g.r)
		touch(g.r, pointer.Move, 1, x0+3*g.cell, cy, 40*ms)
		touch(g.r, pointer.Release, 1, x0+3*g.cell, cy, 60*ms)
		gameFrame(g.a, g.r)
		want(t, g)
	})
}

// TestGestureFastLongDragSteps: a finger that keeps moving fast and downward
// past flickWindow is a drag, not a flick in progress — it steps like any
// other drag while it moves, and its release is no hard drop.
func TestGestureFastLongDragSteps(t *testing.T) {
	r := newGestureRig()
	r.feed(pointer.Press, 1, 150, 100, 0)
	// 1.2 px/ms downward, every 50 ms: a flick candidate for the window…
	r.feed(pointer.Drag, 1, 150, 160, 50*ms)
	r.feed(pointer.Drag, 1, 150, 220, 100*ms)
	r.feed(pointer.Drag, 1, 150, 280, 150*ms)
	r.want(t) // …held back so far
	// …and a fast drag past it: the rows come, at once, and keep coming.
	r.feed(pointer.Drag, 1, 150, 340, 250*ms)
	r.want(t, engine.MoveDown, engine.MoveDown, engine.MoveDown, engine.MoveDown, engine.MoveDown, engine.MoveDown, engine.MoveDown, engine.MoveDown)
	r.feed(pointer.Drag, 1, 150, 400, 300*ms)
	if n := len(r.moves); n != 10 {
		t.Fatalf("after 300 px of drag: %d moves, want 10 rows", n)
	}
	r.feed(pointer.Release, 1, 150, 430, 320*ms) // lifted while still fast
	if n := len(r.moves); n != 11 || r.moves[10] != engine.MoveDown {
		t.Fatalf("release of a long fast drag: %v, want a last soft-drop row and no hard drop", r.moves[8:])
	}
}
