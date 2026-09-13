//go:build linux

package gamepad

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"syscall"
	"unsafe"
)

// The Linux backend: the kernel's joystick device, /dev/input/js0 to js3,
// in pure Go (the same file serves amd64 and arm64). The device streams
// 8-byte records — {time u32; value s16; type u8; number u8} — one per
// button or axis change, and on open replays the current state of every
// control with the JS_EVENT_INIT bit set, so nothing has to be asked for.
// Reads are non-blocking: EAGAIN is nothing new, ENODEV the pad unplugged.
// The four nodes are looked at again every second for a pad plugged in
// later. A node that cannot be opened (the udev rules give the seated user
// the joystick nodes as a rule, but not on every distribution) is logged
// once and left alone.
//
// The numbering is the driver's. Axes are the same for the Xbox (xpad) and
// PlayStation (hid-playstation, hid-sony) drivers: 0/1 the left stick, 2
// and 5 the triggers — resting at −32767, not 0 — and 6/7 the D-pad as a
// hat. The buttons differ: joydev numbers keys in key-code order, and the
// kernel's BTN_X is BTN_NORTH while BTN_Y is BTN_WEST, so an Xbox pad reads
// 0 A, 1 B, 2 X, 3 Y, 4 LB, 5 RB, 6 Back, 7 Start, while a PlayStation pad
// reads 0 ✕, 1 ○, 2 △, 3 □, 4 L1, 5 R1, 6 L2, 7 R2, 8 Select, 9 Start —
// its West and North swapped against the Xbox table, and its triggers as
// buttons. The device's name (JSIOCGNAME) picks the table.

const (
	jsEventButton = 0x01
	jsEventAxis   = 0x02
	jsEventInit   = 0x80
	jsAxisMax     = 32767
	// jsiocgname is the JSIOCGNAME(len) ioctl: _IOC(_IOC_READ, 'j', 0x13, len).
	jsiocgnameLen = 128
	jsiocgname    = (2 << 30) | ('j' << 8) | 0x13 | (jsiocgnameLen << 16)
)

// jsXboxButtons and jsSonyButtons are Button by joydev number, per driver;
// a number past the table is no button.
var (
	jsXboxButtons = []Button{South, East, West, North, LB, RB, Back, Start, 0xff, LStick, RStick}
	jsSonyButtons = []Button{South, East, North, West, LB, RB, LT, RT, Back, Start, 0xff, LStick, RStick}
)

// jsSonyNames are the substrings a PlayStation pad's name carries.
var jsSonyNames = []string{"Sony", "DualSense", "DualShock", "PLAYSTATION", "Wireless Controller"}

type linuxSource struct {
	*poller
	mu      sync.Mutex
	dev     *jsDevice
	warned  map[string]bool
	lastTry int // ticks since the last scan for a pad
}

// jsDevice is one open joystick node and the state its records built.
type jsDevice struct {
	f       *os.File
	buttons []Button // by number
	raw     Raw
	buf     [8 * 64]byte
}

// Open is the platform's Source.
func Open() Source { return &linuxSource{poller: newPoller(), warned: map[string]bool{}} }

func (s *linuxSource) Start(wake func()) {
	s.setWake(wake)
	s.start(pollInterval, s.read)
}

// scanEvery is how many ticks apart the nodes are looked at for a pad: one
// second's worth.
const scanEvery = 250

func (s *linuxSource) read() (Raw, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dev == nil {
		s.lastTry++
		if s.lastTry < scanEvery && s.lastTry != 1 {
			return Raw{}, false
		}
		s.lastTry = 1
		s.dev = s.scan()
		if s.dev == nil {
			return Raw{}, false
		}
	}
	if err := s.dev.pump(); err != nil {
		s.dev.f.Close()
		s.dev = nil
		s.lastTry = 0
		return Raw{}, false
	}
	return s.dev.raw, true
}

// scan opens the first joystick node that will open.
func (s *linuxSource) scan() *jsDevice {
	for i := 0; i < 4; i++ {
		path := fmt.Sprintf("/dev/input/js%d", i)
		f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) && !s.warned[path] {
				s.warned[path] = true
				log.Printf("gamepad: %s: %v", path, err)
			}
			continue
		}
		name := jsName(f)
		d := &jsDevice{f: f, buttons: jsXboxButtons}
		for _, n := range jsSonyNames {
			if strings.Contains(name, n) {
				d.buttons = jsSonyButtons
				break
			}
		}
		log.Printf("gamepad: %s: %q", path, name)
		return d
	}
	return nil
}

// jsName is the device's name by JSIOCGNAME, "" when the ioctl fails.
func jsName(f *os.File) string {
	var buf [jsiocgnameLen]byte
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), uintptr(jsiocgname), uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return ""
	}
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return string(buf[:n])
}

// pump reads every record waiting and folds it into raw. The error is the
// pad gone (or a read failing for good).
func (d *jsDevice) pump() error {
	for {
		n, err := d.f.Read(d.buf[:])
		if err != nil {
			if errors.Is(err, syscall.EAGAIN) {
				return nil
			}
			return err
		}
		for i := 0; i+8 <= n; i += 8 {
			rec := d.buf[i : i+8]
			value := int16(binary.LittleEndian.Uint16(rec[4:6]))
			typ := rec[6] &^ jsEventInit
			num := int(rec[7])
			switch typ {
			case jsEventButton:
				if num >= len(d.buttons) || d.buttons[num] == 0xff {
					continue
				}
				bit := d.buttons[num].Bit()
				if value != 0 {
					d.raw.Buttons |= bit
				} else {
					d.raw.Buttons &^= bit
				}
			case jsEventAxis:
				v := float32(value) / jsAxisMax
				switch num {
				case 0:
					d.raw.LX = v
				case 1:
					d.raw.LY = v
				case 2:
					d.raw.LT = (v + 1) / 2 // rests at -1
				case 5:
					d.raw.RT = (v + 1) / 2
				case 6:
					d.raw.Buttons &^= DPadLeft.Bit() | DPadRight.Bit()
					if value < 0 {
						d.raw.Buttons |= DPadLeft.Bit()
					} else if value > 0 {
						d.raw.Buttons |= DPadRight.Bit()
					}
				case 7:
					d.raw.Buttons &^= DPadUp.Bit() | DPadDown.Bit()
					if value < 0 {
						d.raw.Buttons |= DPadUp.Bit()
					} else if value > 0 {
						d.raw.Buttons |= DPadDown.Bit()
					}
				}
			}
		}
	}
}

func (s *linuxSource) Stop() {
	s.poller.Stop()
	s.mu.Lock()
	if s.dev != nil {
		s.dev.f.Close()
		s.dev = nil
	}
	s.mu.Unlock()
}
