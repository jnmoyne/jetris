package voice

import (
	"errors"
	"math"
	"sync"
)

// DeviceIO is what a Device drives. Capture and Render are called on the
// device's own audio thread (the browser's event loop in the wasm build):
// they must never block, allocate per call, or take a lock anything slow
// could hold — the Session keeps them to a channel send and a mix.
type DeviceIO struct {
	// Capture hands over one FrameSamples-sample 16 kHz mono frame from the
	// microphone. The slice is the device's own and is reused: copy it.
	Capture func(pcm []int16)
	// Render asks for one FrameSamples-sample frame to play; out arrives
	// zeroed and unfilled samples stay silent.
	Render func(out []int16)
	// CaptureState reports the outcome of every SetCapture: the microphone
	// is open (or closed) with a nil error, or it could not be opened — a
	// denied permission, no microphone — with the reason. Asynchronous where
	// the platform is (the browser's permission prompt), so it may arrive on
	// any goroutine, after SetCapture has returned.
	CaptureState func(open bool, err error)
	// Notice is a one-line status from the device that is not a failure of a
	// SetCapture — the browser's "tap anywhere to enable audio" while its
	// audio context waits for a gesture — cleared with "".
	Notice func(msg string)
}

// Device is the platform's audio: the speakers from Open, the microphone
// from SetCapture(true). Open asks for no permission and prompts for nothing;
// the microphone prompt, where the platform has one, is SetCapture's.
type Device interface {
	Open(io DeviceIO) error
	// SetCapture opens or closes the microphone; the outcome comes back
	// through DeviceIO.CaptureState. Closing stops the platform's "in use"
	// indicator; opening again is meant to be quick.
	SetCapture(on bool)
	Close()
}

// ErrUnavailable is Open's answer in a build with no audio at all — the
// desktop built without cgo (device_none.go).
var ErrUnavailable = errors.New("voice is not available in this build")

// FakeDevice is a Device driven by a test: Feed is the microphone, Pull the
// speakers. Exported so the UI package's tests can run a Session without
// audio hardware.
type FakeDevice struct {
	// OpenErr fails Open; CaptureErr fails every SetCapture(true).
	OpenErr, CaptureErr error

	mu        sync.Mutex
	io        DeviceIO
	open      bool
	capturing bool
}

func (f *FakeDevice) Open(io DeviceIO) error {
	if f.OpenErr != nil {
		return f.OpenErr
	}
	f.mu.Lock()
	f.io, f.open = io, true
	f.mu.Unlock()
	return nil
}

func (f *FakeDevice) SetCapture(on bool) {
	f.mu.Lock()
	io := f.io
	err := f.CaptureErr
	if on && err != nil {
		on = false
	}
	f.capturing = on
	f.mu.Unlock()
	if io.CaptureState != nil {
		io.CaptureState(on, err)
	}
}

func (f *FakeDevice) Close() {
	f.mu.Lock()
	f.open, f.capturing = false, false
	f.mu.Unlock()
}

// Capturing reports whether the microphone is open.
func (f *FakeDevice) Capturing() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.capturing
}

// Feed delivers one frame as the microphone would — only while it is open.
func (f *FakeDevice) Feed(pcm []int16) {
	f.mu.Lock()
	io, on := f.io, f.capturing
	f.mu.Unlock()
	if on && io.Capture != nil {
		io.Capture(pcm)
	}
}

// Pull asks for one frame as the speakers would; silence when nothing is
// open.
func (f *FakeDevice) Pull() []int16 {
	out := make([]int16, FrameSamples)
	f.mu.Lock()
	io, open := f.io, f.open
	f.mu.Unlock()
	if open && io.Render != nil {
		io.Render(out)
	}
	return out
}

// SineFrames is n frames of a sine at hz and dbfs (peak), phase carried from
// one frame to the next — a test's voice.
func SineFrames(hz, dbfs float64, n int) [][]int16 {
	amp := 32767 * math.Pow(10, dbfs/20)
	frames := make([][]int16, n)
	for f := range frames {
		frames[f] = make([]int16, FrameSamples)
		for i := range frames[f] {
			t := float64(f*FrameSamples+i) / SampleRate
			frames[f][i] = int16(amp * math.Sin(2*math.Pi*hz*t))
		}
	}
	return frames
}

// Silence is n frames of nothing.
func Silence(n int) [][]int16 {
	frames := make([][]int16, n)
	for f := range frames {
		frames[f] = make([]int16, FrameSamples)
	}
	return frames
}
