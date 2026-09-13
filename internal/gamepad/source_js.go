//go:build js

package gamepad

import (
	"log"
	"syscall/js"
)

// The browser backend: the Gamepad API. navigator.getGamepads() is a
// snapshot the page asks for, not events it is sent, so the pad is read on
// every animation frame (requestAnimationFrame — the browser's own frame
// clock, which it pauses in a hidden tab) and the poller's tick makes the
// edges. The page is told of a pad only after a button is pressed on it
// while the page has the focus (every browser's rule, a privacy measure),
// and the API needs a secure context — https, or localhost.
//
// The "standard" mapping is assumed: buttons 0..3 the face buttons by
// compass point (0 South, 1 East, 2 West, 3 North), 4/5 the shoulders, 6/7
// the triggers — already digitized by the browser, so their .pressed is
// used — 8/9 Back/Start, 10/11 the stick clicks, 12..15 the D-pad up, down,
// left, right; axes 0/1 the left stick, −1 up. A pad the browser reports
// under another mapping is read as if it were standard, once logged.

// jsButtons is Button by standard-mapping index.
var jsButtons = [...]Button{
	0: South, 1: East, 2: West, 3: North,
	4: LB, 5: RB, 6: LT, 7: RT,
	8: Back, 9: Start, 10: LStick, 11: RStick,
	12: DPadUp, 13: DPadDown, 14: DPadLeft, 15: DPadRight,
}

type jsSource struct {
	*poller
	frame    js.Func
	stopped  bool
	warned   bool
	nav      js.Value
	rafToken js.Value
}

// Open is the platform's Source.
func Open() Source {
	return &jsSource{poller: newPoller(), nav: js.Global().Get("navigator")}
}

func (s *jsSource) Start(wake func()) {
	s.setWake(wake)
	if !s.nav.Truthy() || !s.nav.Get("getGamepads").Truthy() {
		return // no Gamepad API: the pad is never connected
	}
	win := js.Global().Get("window")
	s.frame = js.FuncOf(func(this js.Value, args []js.Value) any {
		if s.stopped {
			return nil
		}
		s.tick(s.read())
		s.rafToken = win.Call("requestAnimationFrame", s.frame)
		return nil
	})
	s.rafToken = win.Call("requestAnimationFrame", s.frame)
}

// read is the first pad the browser lists, or none.
func (s *jsSource) read() (Raw, bool) {
	pads := s.nav.Call("getGamepads")
	if !pads.Truthy() {
		return Raw{}, false
	}
	n := pads.Length()
	for i := 0; i < n; i++ {
		gp := pads.Index(i)
		if !gp.Truthy() || !gp.Get("connected").Bool() {
			continue
		}
		if m := gp.Get("mapping").String(); m != "standard" && !s.warned {
			s.warned = true
			log.Printf("gamepad: %q reports the %q mapping; reading it as standard", gp.Get("id").String(), m)
		}
		var r Raw
		btns := gp.Get("buttons")
		for j := 0; j < min(btns.Length(), len(jsButtons)); j++ {
			if btns.Index(j).Get("pressed").Bool() {
				r.Buttons |= jsButtons[j].Bit()
			}
		}
		if axes := gp.Get("axes"); axes.Length() >= 2 {
			r.LX = float32(axes.Index(0).Float())
			r.LY = float32(axes.Index(1).Float())
		}
		return r, true
	}
	return Raw{}, false
}

func (s *jsSource) Stop() {
	if s.stopped {
		return
	}
	s.stopped = true
	if s.rafToken.Truthy() {
		js.Global().Get("window").Call("cancelAnimationFrame", s.rafToken)
	}
	if !s.frame.IsUndefined() {
		s.frame.Release()
	}
	s.poller.Stop()
}
