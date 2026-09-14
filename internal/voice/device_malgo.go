//go:build !js && cgo

package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"

	"github.com/gen2brain/malgo"
)

// The desktop's audio is miniaudio, through malgo's cgo binding: one library
// that speaks CoreAudio on the Mac, WASAPI on Windows and PulseAudio, ALSA
// or JACK on Linux (each loaded with dlopen at run time, so the binary links
// against nothing). Both devices run at the wire's own format — 16 kHz mono
// S16 in 320-sample periods — and miniaudio converts to whatever the
// hardware does. Two separate devices rather than one duplex device: the
// speakers open with the game, the microphone only at the first unmute, so
// the platform's "in use" indicator (and on macOS the permission prompt)
// appears when the player asks for it; and a Bluetooth headset's microphone
// with the laptop's speakers, two clocks, is a pair miniaudio handles as two
// devices but not as one.
//
// The release build is a bare binary, not an .app bundle, so it carries no
// NSMicrophoneUsageDescription: launched from a terminal, macOS attributes
// the microphone prompt to the terminal application, which is where the
// permission then lives.

// The microphone is raw: miniaudio cancels no echo, and a player on
// speakers would send every voice they play back into the room. So this
// device does not claim to (no CancelsEcho), and the Session runs its own
// canceller on it (aec.go) — the job the browser's getUserMedia does for
// the web build.

// malgoBackends is the explicit backend list, in priority order. Naming
// them rather than passing nil leaves out the Null backend, so a Linux box
// with no sound library at all fails Open cleanly instead of playing into
// the void.
var malgoBackends = []malgo.Backend{
	malgo.BackendCoreaudio,
	malgo.BackendWasapi,
	malgo.BackendPulseaudio,
	malgo.BackendAlsa,
	malgo.BackendJack,
}

// malgoDevice is the Device behind NewDevice on the desktop.
type malgoDevice struct {
	mu        sync.Mutex // guards everything below but the accumulators
	io        DeviceIO
	ctx       *malgo.AllocatedContext
	play      *malgo.Device
	cap       *malgo.Device // initialised at the first SetCapture(true), kept across stops
	capturing bool

	// The accumulators belong to the audio threads: out is drained by the
	// playback callback, in filled by the capture callback, and nothing else
	// touches them. Each device's callbacks run one at a time on that
	// device's own thread, so no lock is needed — and none is taken, since
	// SetCapture holds mu across Start/Stop, which wait on the worker.
	out    [FrameSamples]int16
	outPos int // next sample of out to play; FrameSamples once out is spent
	in     [FrameSamples]int16
	inPos  int
}

// NewDevice is the desktop's audio, opened lazily by Open.
func NewDevice() Device {
	return &malgoDevice{outPos: FrameSamples}
}

// malgoConfig is the one configuration both devices use: S16 mono at the
// wire rate, a period of one frame so the callbacks come every 20 ms
// (Render still copes with any period, see onPlay). NoMMap keeps ALSA on
// its read/write path, where every plugin works.
func malgoConfig(kind malgo.DeviceType) malgo.DeviceConfig {
	cfg := malgo.DefaultDeviceConfig(kind)
	cfg.SampleRate = SampleRate
	cfg.PeriodSizeInFrames = FrameSamples
	cfg.Alsa.NoMMap = 1
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = 1
	cfg.Capture.Format = malgo.FormatS16
	cfg.Capture.Channels = 1
	return cfg
}

// Open initialises the audio context and starts the speakers. It prompts for
// nothing: playback needs no permission anywhere.
func (d *malgoDevice) Open(io DeviceIO) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx != nil {
		return errors.New("voice: device already open")
	}
	ctx, err := malgo.InitContext(malgoBackends, malgo.ContextConfig{}, nil)
	if err != nil {
		return fmt.Errorf("voice: no audio backend: %w", err)
	}
	// The callback reads d.io, and Start pulls the first period before it
	// returns, so io is in place before the device exists.
	d.io = io
	d.outPos = FrameSamples
	play, err := malgo.InitDevice(ctx.Context, malgoConfig(malgo.Playback), malgo.DeviceCallbacks{Data: d.onPlay})
	if err != nil {
		_ = ctx.Uninit()
		ctx.Free()
		return fmt.Errorf("voice: open speakers: %w", err)
	}
	if err := play.Start(); err != nil {
		play.Uninit()
		_ = ctx.Uninit()
		ctx.Free()
		return fmt.Errorf("voice: start speakers: %w", err)
	}
	d.ctx, d.play = ctx, play
	return nil
}

// onPlay is the playback callback. The device asks for frameCount samples —
// one period, 320, unless the backend has other ideas — and gets them out
// of the out accumulator, which Render refills a whole frame at a time, so
// any period size is served exactly. Little-endian S16, two bytes a sample;
// nothing here allocates, logs or locks.
func (d *malgoDevice) onPlay(out, _ []byte, frameCount uint32) {
	for i := 0; i+1 < len(out); i += 2 {
		if d.outPos == FrameSamples {
			for j := range d.out {
				d.out[j] = 0
			}
			if d.io.Render != nil {
				d.io.Render(d.out[:])
			}
			d.outPos = 0
		}
		binary.LittleEndian.PutUint16(out[i:], uint16(d.out[d.outPos]))
		d.outPos++
	}
}

// onCapture is the microphone callback: samples decoded into the in
// accumulator, handed over as each frame completes.
func (d *malgoDevice) onCapture(_, in []byte, frameCount uint32) {
	for i := 0; i+1 < len(in); i += 2 {
		d.in[d.inPos] = int16(binary.LittleEndian.Uint16(in[i:]))
		d.inPos++
		if d.inPos == FrameSamples {
			if d.io.Capture != nil {
				d.io.Capture(d.in[:])
			}
			d.inPos = 0
		}
	}
}

// SetCapture opens (initialising it the first time) and starts, or stops,
// the microphone. Idempotent, callable from any goroutine; the outcome is
// reported through CaptureState after the lock is released, so the report
// may call back into the device. Opening runs on a goroutine of its own:
// the Session asks from the mic button's frame on the UI goroutine, and
// initialising a capture device is tens of milliseconds the frame should
// not wait on (the browser device has the opposite need — the click's own
// moment — which is why the choice is the device's). Stopping is done here
// and now: it is quick, and a mute must not be outrun by an open still on
// its way. Never from inside a callback.
func (d *malgoDevice) SetCapture(on bool) {
	if on {
		go d.applyCapture(true)
		return
	}
	d.applyCapture(false)
}

func (d *malgoDevice) applyCapture(on bool) {
	d.mu.Lock()
	err := d.setCapture(on)
	io, open := d.io, d.capturing
	d.mu.Unlock()
	if io.CaptureState != nil {
		io.CaptureState(open, err)
	}
}

// setCapture is SetCapture under the lock; d.capturing is the truth after
// it returns.
func (d *malgoDevice) setCapture(on bool) error {
	if !on {
		if d.capturing {
			// Stop only: the device stays initialised so the next unmute
			// is instant. Stop waits for the worker, hence never from a
			// callback.
			_ = d.cap.Stop()
		}
		d.capturing = false
		return nil
	}
	if d.capturing {
		return nil
	}
	if d.ctx == nil {
		return errors.New("the audio device is not open")
	}
	if d.cap == nil {
		dev, err := malgo.InitDevice(d.ctx.Context, malgoConfig(malgo.Capture), malgo.DeviceCallbacks{Data: d.onCapture})
		if err != nil {
			return micError(err)
		}
		d.cap = dev
	}
	if err := d.cap.Start(); err != nil {
		// A device that will not start (unplugged since it was initialised)
		// is thrown away, so the next attempt starts from a fresh init.
		d.cap.Uninit()
		d.cap = nil
		return micError(err)
	}
	d.capturing = true
	return nil
}

// micError is what the player reads when the microphone will not open.
func micError(err error) error {
	switch {
	case errors.Is(err, malgo.ErrNoDevice):
		return errors.New("no microphone found")
	case errors.Is(err, malgo.ErrAccessDenied):
		return errors.New("microphone access denied")
	}
	return fmt.Errorf("could not open the microphone: %v", err)
}

// Close stops and releases the microphone, the speakers and then the
// context, in that order (a context must outlive its devices). Uninit stops
// a running device itself and, like Stop, waits for its worker — which is
// why Close belongs to Session.Stop and never to a callback. Idempotent.
func (d *malgoDevice) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cap != nil {
		d.cap.Uninit()
		d.cap = nil
	}
	d.capturing = false
	if d.play != nil {
		d.play.Uninit()
		d.play = nil
	}
	if d.ctx != nil {
		_ = d.ctx.Uninit()
		d.ctx.Free()
		d.ctx = nil
	}
}
