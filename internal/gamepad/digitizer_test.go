package gamepad

import (
	"reflect"
	"testing"
)

func edges(evs []Event) []Event {
	if len(evs) == 0 {
		return nil
	}
	return evs
}

// TestDigitizerButtons: a bit set is a press, cleared a release, unchanged
// nothing; two buttons changing in one reading give two edges in Button order.
func TestDigitizerButtons(t *testing.T) {
	var d digitizer
	got := d.update(Raw{Buttons: South.Bit() | DPadLeft.Bit()})
	want := []Event{{DPadLeft, true}, {South, true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("press edges = %v, want %v", got, want)
	}
	if got := edges(d.update(Raw{Buttons: South.Bit() | DPadLeft.Bit()})); got != nil {
		t.Fatalf("a repeat of the same reading made edges: %v", got)
	}
	got = d.update(Raw{Buttons: South.Bit()})
	want = []Event{{DPadLeft, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("release edges = %v, want %v", got, want)
	}
}

// TestDigitizerStickHysteresis: the stick is a press past stickOn, holds
// inside the band, and releases only back inside stickOff.
func TestDigitizerStickHysteresis(t *testing.T) {
	var d digitizer
	if got := edges(d.update(Raw{LX: -0.4})); got != nil {
		t.Fatalf("under the threshold pressed: %v", got)
	}
	if got, want := d.update(Raw{LX: -0.6}), []Event{{StickLeft, true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("past the threshold = %v, want %v", got, want)
	}
	if got := edges(d.update(Raw{LX: -0.4})); got != nil {
		t.Fatalf("inside the band released: %v", got)
	}
	if got, want := d.update(Raw{LX: -0.2}), []Event{{StickLeft, false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("back inside off = %v, want %v", got, want)
	}
	// A swing straight across: left releases and right presses in one reading.
	d.update(Raw{LX: -1})
	got, want := d.update(Raw{LX: 1}), []Event{{StickLeft, false}, {StickRight, true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("swing = %v, want %v", got, want)
	}
	// Up is negative LY, down positive.
	var e digitizer
	if got, want := e.update(Raw{LY: -1}), []Event{{StickUp, true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("up = %v, want %v", got, want)
	}
	if got, want := e.update(Raw{LY: 1}), []Event{{StickUp, false}, {StickDown, true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("down = %v, want %v", got, want)
	}
}

// TestDigitizerTriggers: an analog trigger presses at triggerOn and releases
// at triggerOff; a digital one is its bit.
func TestDigitizerTriggers(t *testing.T) {
	var d digitizer
	if got := edges(d.update(Raw{RT: 0.4})); got != nil {
		t.Fatalf("light pull pressed: %v", got)
	}
	if got, want := d.update(Raw{RT: 0.7}), []Event{{RT, true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pull = %v, want %v", got, want)
	}
	if got := edges(d.update(Raw{RT: 0.4})); got != nil {
		t.Fatalf("eased inside the band released: %v", got)
	}
	if got, want := d.update(Raw{RT: 0.1}), []Event{{RT, false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("let go = %v, want %v", got, want)
	}
	var e digitizer
	if got, want := e.update(Raw{Buttons: LT.Bit()}), []Event{{LT, true}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("digital pull = %v, want %v", got, want)
	}
	if got, want := e.update(Raw{}), []Event{{LT, false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("digital let go = %v, want %v", got, want)
	}
}

// TestDigitizerDPadAndStick: the D-pad and the stick are separate buttons —
// both left at once are two presses, and letting one go is one release.
func TestDigitizerDPadAndStick(t *testing.T) {
	var d digitizer
	got, want := d.update(Raw{Buttons: DPadLeft.Bit(), LX: -1}), []Event{{DPadLeft, true}, {StickLeft, true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("both = %v, want %v", got, want)
	}
	if got, want := d.update(Raw{LX: -1}), []Event{{DPadLeft, false}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("dpad up = %v, want %v", got, want)
	}
}

// TestDigitizerReleaseAll: an unplug lets go of everything held, once.
func TestDigitizerReleaseAll(t *testing.T) {
	var d digitizer
	d.update(Raw{Buttons: East.Bit() | RB.Bit(), LX: 1})
	got, want := d.releaseAll(), []Event{{East, false}, {RB, false}, {StickRight, false}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("releaseAll = %v, want %v", got, want)
	}
	if got := edges(d.releaseAll()); got != nil {
		t.Fatalf("a second releaseAll released again: %v", got)
	}
}

// TestQueue: push wakes once per batch, Drain hands the edges over in order
// and empties the queue, kick wakes only while something waits.
func TestQueue(t *testing.T) {
	var q queue
	wakes := 0
	q.setWake(func() { wakes++ })
	q.push(nil)
	if wakes != 0 {
		t.Fatalf("an empty push woke")
	}
	q.push([]Event{{South, true}})
	q.push([]Event{{South, false}})
	if wakes != 2 || !q.Pending() {
		t.Fatalf("wakes=%d pending=%v after two pushes", wakes, q.Pending())
	}
	q.kick()
	if wakes != 3 {
		t.Fatalf("kick with edges waiting did not wake")
	}
	got, want := q.Drain(), []Event{{South, true}, {South, false}}
	if !reflect.DeepEqual(got, want) || q.Pending() || q.Drain() != nil {
		t.Fatalf("Drain = %v (want %v), pending after = %v", got, want, q.Pending())
	}
	q.kick()
	if wakes != 3 {
		t.Fatalf("kick with nothing waiting woke")
	}
}
