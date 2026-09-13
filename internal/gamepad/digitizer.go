package gamepad

// digitizer turns a backend's readings into button edges: the digital
// buttons by their bits, the left stick's four directions and the two
// triggers by thresholds with hysteresis — a stick pushed past stickOn is
// down until it comes back inside stickOff, so a hand resting near the
// threshold does not stutter presses. One per pad; the backend feeds it
// every reading and pushes what comes out.
type digitizer struct {
	down [buttonCount]bool
}

const (
	// stickOn/stickOff are the left stick's thresholds, as a fraction of
	// full travel: on past stickOn, off again inside stickOff.
	stickOn  = 0.5
	stickOff = 0.35
	// triggerOn/triggerOff are the analog triggers'.
	triggerOn  = 0.5
	triggerOff = 0.3
)

// update takes one reading and returns the edges it makes, in Button order.
func (d *digitizer) update(r Raw) []Event {
	var evs []Event
	set := func(b Button, on bool) {
		if d.down[b] == on {
			return
		}
		d.down[b] = on
		evs = append(evs, Event{Button: b, Pressed: on})
	}
	// analog is a threshold with hysteresis: v past on turns the button on,
	// v back inside off turns it off, in between it keeps its state.
	analog := func(b Button, v, on, off float32) {
		switch {
		case v >= on:
			set(b, true)
		case v <= off:
			set(b, false)
		}
	}
	for b := DPadUp; b <= RStick; b++ {
		on := r.Buttons&b.Bit() != 0
		switch b {
		case LT:
			// A digital trigger is its bit; an analog one its travel.
			if on {
				set(b, true)
			} else {
				analog(b, r.LT, triggerOn, triggerOff)
			}
		case RT:
			if on {
				set(b, true)
			} else {
				analog(b, r.RT, triggerOn, triggerOff)
			}
		default:
			set(b, on)
		}
	}
	analog(StickUp, -r.LY, stickOn, stickOff)
	analog(StickDown, r.LY, stickOn, stickOff)
	analog(StickLeft, -r.LX, stickOn, stickOff)
	analog(StickRight, r.LX, stickOn, stickOff)
	return evs
}

// releaseAll is the edges that let go of every button held — what a pad
// coming unplugged owes the UI, so no machine is left running on a release
// that will never come.
func (d *digitizer) releaseAll() []Event {
	var evs []Event
	for b := Button(0); b < buttonCount; b++ {
		if d.down[b] {
			d.down[b] = false
			evs = append(evs, Event{Button: b, Pressed: false})
		}
	}
	return evs
}
