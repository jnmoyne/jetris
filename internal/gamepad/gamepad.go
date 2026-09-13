// Package gamepad reads game controllers — a gamepad's buttons, D-pad, left
// stick and triggers — on every build of the game, and hands the UI what it
// wants from them: button EDGES, a press or a release of one named button,
// drained once a frame. Gio has no idea a controller exists on any backend,
// so this is the game's own layer over the platform's:
//
//	browser   the Gamepad API (source_js.go), polled on requestAnimationFrame
//	macOS     Apple's GameController framework (source_darwin.go), by its
//	          value-changed handler on a queue of its own
//	Linux     the kernel's joystick device, /dev/input/js* (source_linux.go)
//	Windows   XInput (source_windows.go), pure Go so the arm64 build has it
//
// Each backend reads the hardware into a Raw — the digital buttons as a
// bitmask, the stick and the triggers as floats — and feeds every reading to
// one digitizer (digitizer.go), which is where the stick's dead zone and the
// triggers' threshold live, with hysteresis, so all four backends agree on
// what a press is. The digitizer's edges go into a queue the UI drains
// (Source.Drain); the backend wakes the window for them.
//
// The wake is a window.Invalidate from the backend's goroutine, and Gio
// drops an Invalidate made while a frame is running (app/window.go: it only
// fires while mayInvalidate is set, which the UI goroutine re-arms as it
// goes back to waiting for events). So a backend does not wake once, on
// the append: it wakes on every poll tick for as long as the queue holds
// something (queue.kick), and the retry after the frame has ended is the one
// that lands. A retry that is a no-op costs a mutex and a bool.
//
// What the buttons DO is the UI's business (nativeui/gamepad.go): here a
// button is a button, named by its place on the pad — South is A on an
// Xbox pad and ✕ on a PlayStation one — and the stick's four directions
// are buttons like the D-pad's.
package gamepad

import (
	"fmt"
	"sync"
	"time"
)

// Button is one control on the pad, named by its place: the D-pad's four
// arms, the four face buttons by compass point (South is A / ✕, East B /
// ○, West X / □, North Y / △), the shoulders and triggers, the two menu
// buttons, the stick clicks, and the left stick's four directions, which
// the digitizer makes into buttons of their own.
type Button uint8

const (
	DPadUp Button = iota
	DPadDown
	DPadLeft
	DPadRight
	South
	East
	West
	North
	LB
	RB
	LT
	RT
	Back
	Start
	LStick
	RStick
	StickUp
	StickDown
	StickLeft
	StickRight
	buttonCount
)

var buttonNames = [...]string{
	DPadUp: "dpad-up", DPadDown: "dpad-down", DPadLeft: "dpad-left", DPadRight: "dpad-right",
	South: "south", East: "east", West: "west", North: "north",
	LB: "lb", RB: "rb", LT: "lt", RT: "rt",
	Back: "back", Start: "start", LStick: "lstick", RStick: "rstick",
	StickUp: "stick-up", StickDown: "stick-down", StickLeft: "stick-left", StickRight: "stick-right",
}

func (b Button) String() string {
	if int(b) < len(buttonNames) {
		return buttonNames[b]
	}
	return fmt.Sprintf("button(%d)", uint8(b))
}

// Bit is b's bit in Raw.Buttons.
func (b Button) Bit() uint32 { return 1 << b }

// Event is one edge: b went down (Pressed) or came up. It carries no
// time: the UI dispatches it on the frame's clock, as it does a key.
type Event struct {
	Button  Button
	Pressed bool
}

// Raw is one reading of the pad, as a backend takes it from the platform:
// Buttons holds a Bit per digital button that is down (DPadUp through
// RStick; a backend whose triggers are digital sets LT/RT here and leaves
// the floats at 0), LX and LY the left stick (−1..1, LY −1 up and +1
// down — the browser's and the kernel's way up; the backends whose axis
// points the other way flip it), and LT/RT the triggers' travel (0..1)
// where the platform reports it.
type Raw struct {
	Buttons uint32
	LX, LY  float32
	LT, RT  float32
}

// Source is a controller as the UI sees it. Start begins reading the
// hardware and takes the function that wakes the window; Drain hands over
// the edges since the last Drain, in order; Pending reports edges waiting;
// Connected whether a pad is attached; Stop ends the reading and may be
// called more than once.
type Source interface {
	Start(wake func())
	Drain() []Event
	Pending() bool
	Connected() bool
	Stop()
}

// queue is the edges waiting for the UI, shared by every backend: pushed
// from the backend's goroutine (or the browser's event loop), drained on
// the UI goroutine.
type queue struct {
	mu        sync.Mutex
	evs       []Event
	connected bool
	wake      func()
}

func (q *queue) setWake(wake func()) {
	q.mu.Lock()
	q.wake = wake
	q.mu.Unlock()
}

// push appends evs and wakes the window. Nothing is appended for an empty
// batch and nothing woken.
func (q *queue) push(evs []Event) {
	if len(evs) == 0 {
		return
	}
	q.mu.Lock()
	q.evs = append(q.evs, evs...)
	wake := q.wake
	q.mu.Unlock()
	if wake != nil {
		wake()
	}
}

// kick wakes the window once more if edges are still waiting: the
// backend's every-tick retry for an Invalidate that fell inside a frame
// (see the package doc).
func (q *queue) kick() {
	q.mu.Lock()
	pending := len(q.evs) > 0
	wake := q.wake
	q.mu.Unlock()
	if pending && wake != nil {
		wake()
	}
}

// Drain hands over the edges since the last Drain, in order.
func (q *queue) Drain() []Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.evs) == 0 {
		return nil
	}
	evs := q.evs
	q.evs = nil
	return evs
}

// Pending reports whether edges are waiting.
func (q *queue) Pending() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.evs) > 0
}

// Connected reports whether a pad is attached.
func (q *queue) Connected() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.connected
}

func (q *queue) setConnected(on bool) {
	q.mu.Lock()
	q.connected = on
	q.mu.Unlock()
}

// FakeSource is a Source a test drives: Queue is the pad's buttons,
// SetConnected its cable. Exported so the UI package's tests can play a
// game without a controller.
type FakeSource struct {
	queue
	started, stopped bool
}

func (f *FakeSource) Start(wake func()) { f.setWake(wake); f.started = true }
func (f *FakeSource) Stop()             { f.stopped = true }

// Queue delivers edges as the pad would, waking the window if Start gave
// it the means.
func (f *FakeSource) Queue(evs ...Event) { f.push(evs) }

// SetConnected attaches or detaches the fake pad.
func (f *FakeSource) SetConnected(on bool) { f.setConnected(on) }

// Press and Release are Queue for one button.
func (f *FakeSource) Press(b Button)   { f.Queue(Event{Button: b, Pressed: true}) }
func (f *FakeSource) Release(b Button) { f.Queue(Event{Button: b, Pressed: false}) }

// poller is the shape of every backend that reads the pad by asking: a
// queue, a digitizer, and a tick that takes one reading — or none, the pad
// gone — and pushes the edges it makes. run ticks on a goroutine of its
// own at the given interval until Stop (the desktop backends); the browser
// ticks it from requestAnimationFrame instead (source_js.go).
type poller struct {
	queue
	d       digitizer
	stop    chan struct{}
	stopped sync.Once
	done    chan struct{}
	running bool // start was called: Stop has a run to wait for
}

func newPoller() *poller {
	return &poller{stop: make(chan struct{}), done: make(chan struct{})}
}

// tick takes one reading. ok false is no pad: the first such tick after a
// pad lets go of everything it held, so no machine runs on a release that
// will never come. Every tick kicks the queue (see the package doc).
func (p *poller) tick(r Raw, ok bool) {
	if ok {
		if !p.Connected() {
			p.setConnected(true)
		}
		p.push(p.d.update(r))
	} else if p.Connected() {
		p.setConnected(false)
		p.push(p.d.releaseAll())
	}
	p.kick()
}

// start runs the poll on a goroutine of its own: read every interval until
// Stop. read reports the pad's reading and whether there is a pad.
func (p *poller) start(interval time.Duration, read func() (Raw, bool)) {
	p.running = true
	go p.run(interval, read)
}

func (p *poller) run(interval time.Duration, read func() (Raw, bool)) {
	defer close(p.done)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.tick(read())
		}
	}
}

// Stop ends the poll, once, and waits for it — or returns at once for a
// backend that never started one.
func (p *poller) Stop() {
	p.stopped.Do(func() { close(p.stop) })
	if p.running {
		<-p.done
	}
}

// pollInterval is how often the desktop backends ask: 250 times a second,
// well under a frame, so a press is never more than 4 ms late.
const pollInterval = 4 * time.Millisecond
