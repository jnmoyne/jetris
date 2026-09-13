//go:build windows

package gamepad

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows backend: XInput, pure Go through the system DLL (xinput1_4
// on Windows 8 and later, xinput9_1_0 — every Windows since Vista — as the
// fallback), so the arm64 build, which has no cgo, has it too. XInput
// speaks to Xbox-class pads: an Xbox controller of any generation, and
// whatever else presents itself as one (Steam Input, DS4Windows). A
// PlayStation pad on its own is not one of them.
//
// XInputGetState fills {packet u32; buttons u16; lt, rt u8; lx, ly, rx, ry
// s16}: the buttons a bitmask, the triggers 0..255, the sticks −32768..
// 32767 with +Y up. The four slots are asked in turn and the first
// connected one drives; a slot that is not connected is asked again only
// once a second, as Microsoft advises (an empty slot is slow to answer).

const (
	xiDPadUp        = 0x0001
	xiDPadDown      = 0x0002
	xiDPadLeft      = 0x0004
	xiDPadRight     = 0x0008
	xiStart         = 0x0010
	xiBack          = 0x0020
	xiLeftThumb     = 0x0040
	xiRightThumb    = 0x0080
	xiLeftShoulder  = 0x0100
	xiRightShoulder = 0x0200
	xiA             = 0x1000
	xiB             = 0x2000
	xiX             = 0x4000
	xiY             = 0x8000

	errDeviceNotConnected = 1167
)

// xiButtons pairs each XInput bit with its Button.
var xiButtons = []struct {
	bit uint16
	b   Button
}{
	{xiDPadUp, DPadUp}, {xiDPadDown, DPadDown}, {xiDPadLeft, DPadLeft}, {xiDPadRight, DPadRight},
	{xiA, South}, {xiB, East}, {xiX, West}, {xiY, North},
	{xiLeftShoulder, LB}, {xiRightShoulder, RB},
	{xiBack, Back}, {xiStart, Start}, {xiLeftThumb, LStick}, {xiRightThumb, RStick},
}

type xinputState struct {
	packet  uint32
	buttons uint16
	lt, rt  uint8
	lx, ly  int16
	rx, ry  int16
}

type windowsSource struct {
	*poller
	getState *windows.LazyProc
	slot     int // the slot driving, -1 for none
	retry    int // ticks until the empty slots are asked again
	last     Raw
}

// Open is the platform's Source.
func Open() Source {
	s := &windowsSource{poller: newPoller(), slot: -1}
	for _, name := range []string{"xinput1_4.dll", "xinput9_1_0.dll"} {
		dll := windows.NewLazySystemDLL(name)
		proc := dll.NewProc("XInputGetState")
		if proc.Find() == nil {
			s.getState = proc
			break
		}
	}
	return s
}

func (s *windowsSource) Start(wake func()) {
	s.setWake(wake)
	if s.getState == nil {
		return // no XInput on this Windows: the pad is never connected
	}
	s.start(pollInterval, s.read)
}

// scanEvery is how many ticks apart the empty slots are asked: a second.
const scanEvery = 250

func (s *windowsSource) read() (Raw, bool) {
	var st xinputState
	if s.slot >= 0 {
		if ok := s.query(s.slot, &st); ok {
			return s.decode(st), true
		}
		s.slot, s.retry = -1, scanEvery
		return Raw{}, false
	}
	if s.retry > 0 {
		s.retry--
		return Raw{}, false
	}
	s.retry = scanEvery
	for i := 0; i < 4; i++ {
		if s.query(i, &st) {
			s.slot = i
			return s.decode(st), true
		}
	}
	return Raw{}, false
}

// query asks one slot; false is no pad there.
func (s *windowsSource) query(slot int, st *xinputState) bool {
	r, _, _ := s.getState.Call(uintptr(slot), uintptr(unsafe.Pointer(st)))
	return r == 0
}

func (s *windowsSource) decode(st xinputState) Raw {
	var r Raw
	for _, x := range xiButtons {
		if st.buttons&x.bit != 0 {
			r.Buttons |= x.b.Bit()
		}
	}
	r.LX = float32(st.lx) / 32767
	r.LY = -float32(st.ly) / 32767 // XInput's +Y is up; ours is down
	r.LT = float32(st.lt) / 255
	r.RT = float32(st.rt) / 255
	return r
}

func (s *windowsSource) Stop() { s.poller.Stop() }
