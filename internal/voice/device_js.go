//go:build js

package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sync"
	"syscall/js"
)

// The browser's audio is the Web Audio API: an AudioContext, and in it the
// two AudioWorklet processors of worklet_js.go, one filling a ring for the
// speakers and one chopping the microphone into frames. The context runs at
// its default rate rather than at the wire's 16 kHz: Safari refuses a
// 16 kHz context once a microphone at the hardware's rate is attached to
// it, so the context runs at whatever the hardware does (44.1 or 48 kHz)
// and the resampling to and from 16 kHz is done here, in Go.
//
// Everything below runs on the browser's one thread: wasm has no other, so
// Capture and Render are called from the event loop, in the port message
// handlers. A Gio frame parked on the engine's lock delays them by a
// frame; the play ring absorbs that (silence meanwhile), the capture side
// simply delivers its frames a little late.

const (
	jsRingFrames   = 16 // the play worklet's ring, in frames of frameLen samples
	jsTargetFrames = 4  // the fill Go keeps it at: 80 ms of headroom

	jsTapNotice        = "tap anywhere to enable audio"
	jsListenOnlyNotice = "listening only: the microphone needs https (or localhost)"
)

var (
	errJSUnsupported = errors.New("voice is not supported in this browser")
	errJSNeedsHTTPS  = errors.New("the microphone needs https (or localhost)")
)

// jsGestureEvents are the document events counted as a user gesture by
// every browser between them: pointerdown for a mouse, touchend for a
// finger (a touch's pointerdown activates nothing), keydown for a keyboard.
var jsGestureEvents = []string{"pointerdown", "touchend", "keydown"}

// jsDevice is the Device behind NewDevice in the browser.
type jsDevice struct {
	mu             sync.Mutex // guards the state; never held across a call into io
	io             DeviceIO
	ctx            js.Value
	rate, frameLen int

	ready       bool // the worklet module is loaded and the play node wired
	failed      bool // it never will be
	closed      bool
	wantCapture bool // SetCapture(true) arrived before ready
	opening     bool // a getUserMedia prompt is up
	capturing   bool

	play                      js.Value // the jetris-play node
	legacy                    js.Value // the ScriptProcessorNode of a page without an AudioWorklet (listen only)
	legacyQ                   []float32
	legacySize                int
	stream, source, cap, gain js.Value // the microphone chain, while open
	capFuncs                  []js.Func

	onGesture, onState js.Func
	gestureOn          bool // the one-shot document listeners are installed

	funcs []js.Func // every long-lived js.Func, released by Close

	// The playback path, touched only by onNeed.
	renderBuf [FrameSamples]int16
	renderF   [FrameSamples]float32
	up        *jsResampler // 16 kHz → the context rate
	outF      []float32
	outBytes  []byte

	// The capture path, touched only by onCap (reset while no cap node
	// exists).
	down     *jsResampler // the context rate → 16 kHz
	capBytes []byte
	capF     []float32
	capAcc   []float32 // 16 kHz samples not yet making a whole frame
	capFrame [FrameSamples]int16
}

// NewDevice is the browser's audio, opened lazily by Open.
func NewDevice() Device {
	return &jsDevice{}
}

// CancelsEcho: the microphone is opened with the browser's own echo
// cancellation (getUserMedia's echoCancellation), so the Session runs none.
func (d *jsDevice) CancelsEcho() bool { return true }

// Open creates the context, starts loading the worklet module (asynchronous:
// SetCapture requests wait for it) and sets up the resume dance. Nothing
// here prompts.
func (d *jsDevice) Open(io DeviceIO) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.New("voice: device closed")
	}
	if d.ctx.Truthy() {
		d.mu.Unlock()
		return errors.New("voice: device already open")
	}
	d.io = io

	ctor := js.Global().Get("AudioContext")
	if !ctor.Truthy() {
		ctor = js.Global().Get("webkitAudioContext")
	}
	if !ctor.Truthy() {
		d.mu.Unlock()
		return errors.New("this browser has no Web Audio")
	}
	// The default rate, on purpose: see the top of the file.
	var ctx js.Value
	if err := jsTry(func() { ctx = ctor.New() }); err != nil {
		d.mu.Unlock()
		return fmt.Errorf("audio context: %v", err)
	}
	worklet := ctx.Get("audioWorklet")
	rateV := ctx.Get("sampleRate")
	if rateV.Type() != js.TypeNumber || rateV.Int() <= 0 {
		_ = jsTry(func() { ctx.Call("close") })
		d.mu.Unlock()
		return errors.New("this browser has no Web Audio")
	}
	d.ctx = ctx
	d.rate = rateV.Int()
	d.frameLen = int(math.Round(float64(FrameSamples) * float64(d.rate) / SampleRate))
	d.up = newJSResampler(SampleRate, d.rate)
	d.down = newJSResampler(d.rate, SampleRate)
	d.outF = make([]float32, 0, (jsRingFrames+1)*d.frameLen)
	d.capAcc = make([]float32, 0, 2*FrameSamples)
	if !worklet.Truthy() {
		// No AudioWorklet: this page is not a secure context (plain http
		// on a LAN address — audioWorklet, like getUserMedia, is only
		// there over https or localhost). The room can still be heard
		// through a ScriptProcessorNode, deprecated but everywhere and
		// ungated; the microphone cannot be asked for, and the status line
		// says so (jsListenOnlyNotice).
		return d.openLegacy(ctx, io)
	}

	// Resume: ask now — a page that has seen a click already runs — and
	// failing that, ask again from inside the next real gesture on the
	// document. Gio's canvas handlers do not stop propagation, so the
	// capture-phase listeners see every tap on the game.
	d.onState = js.FuncOf(d.stateChanged)
	d.onGesture = js.FuncOf(d.gesture)
	d.funcs = append(d.funcs, d.onState, d.onGesture)
	_ = jsTry(func() { ctx.Call("addEventListener", "statechange", d.onState) })
	_ = jsTry(func() { ctx.Call("resume") })
	tap := ctx.Get("state").String() != "running"
	if tap {
		d.armGesture()
	}

	// The module, from a blob URL revoked once it has loaded. The promise
	// handlers are one-shot and release themselves.
	urls := js.Global().Get("URL")
	var url js.Value
	var promise js.Value
	err := jsTry(func() {
		blob := js.Global().Get("Blob").New([]any{jsWorkletSource}, map[string]any{"type": "text/javascript"})
		url = urls.Call("createObjectURL", blob)
		promise = worklet.Call("addModule", url)
	})
	if err != nil {
		if url.Truthy() {
			_ = jsTry(func() { urls.Call("revokeObjectURL", url) })
		}
		_ = jsTry(func() { ctx.Call("close") })
		d.ctx = js.Undefined()
		d.disarmGesture()
		d.mu.Unlock()
		return fmt.Errorf("audio worklet: %v", err)
	}
	var onLoaded, onFailed js.Func
	onLoaded = js.FuncOf(func(js.Value, []js.Value) any {
		defer onLoaded.Release()
		_ = jsTry(func() { urls.Call("revokeObjectURL", url) })
		d.moduleLoaded()
		return nil
	})
	onFailed = js.FuncOf(func(js.Value, []js.Value) any {
		defer onFailed.Release()
		_ = jsTry(func() { urls.Call("revokeObjectURL", url) })
		d.moduleFailed()
		return nil
	})
	_ = jsTry(func() { promise.Call("then", onLoaded, onFailed) })
	d.mu.Unlock()

	if tap && io.Notice != nil {
		io.Notice(jsTapNotice)
	}
	return nil
}

// moduleLoaded wires the play node and lets a waiting SetCapture through.
func (d *jsDevice) moduleLoaded() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	var play js.Value
	err := jsTry(func() {
		play = js.Global().Get("AudioWorkletNode").New(d.ctx, "jetris-play", map[string]any{
			"numberOfInputs":     0,
			"numberOfOutputs":    1,
			"outputChannelCount": []any{1},
			"processorOptions": map[string]any{
				"capacity": jsRingFrames * d.frameLen,
				"target":   jsTargetFrames * d.frameLen,
			},
		})
		play.Call("connect", d.ctx.Get("destination"))
	})
	if err != nil {
		d.mu.Unlock()
		d.moduleFailed()
		return
	}
	onNeed := js.FuncOf(d.onNeed)
	d.funcs = append(d.funcs, onNeed)
	play.Get("port").Set("onmessage", onNeed)
	d.play = play
	d.ready = true
	want := d.wantCapture
	d.wantCapture = false
	d.mu.Unlock()
	if want {
		d.SetCapture(true)
	}
}

// moduleFailed is a browser without a working AudioWorklet: the notice
// stays, and a waiting SetCapture learns the same.
func (d *jsDevice) moduleFailed() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.failed = true
	want := d.wantCapture
	d.wantCapture = false
	io := d.io
	d.mu.Unlock()
	if io.Notice != nil {
		io.Notice(errJSUnsupported.Error())
	}
	if want {
		d.report(false, errJSUnsupported)
	}
}

// stateChanged follows the context: running clears the tap notice and the
// listeners; suspended (or Safari's "interrupted", after a phone call)
// puts them back.
func (d *jsDevice) stateChanged(js.Value, []js.Value) any {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	state := d.ctx.Get("state").String()
	notice := ""
	switch state {
	case "running":
		d.disarmGesture()
		if d.legacy.Truthy() {
			notice = jsListenOnlyNotice // still no microphone on this page
		}
	case "closed":
	default:
		d.armGesture()
		notice = jsTapNotice
	}
	io := d.io
	d.mu.Unlock()
	if state != "closed" && io.Notice != nil {
		io.Notice(notice)
	}
	return nil
}

// gesture is the document's tap: the resume Safari wants from inside a real
// DOM gesture. All three listeners come off (one fired itself off) and go
// back on if the state stays anything but running.
func (d *jsDevice) gesture(js.Value, []js.Value) any {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.disarmGesture()
	ctx := d.ctx
	d.mu.Unlock()
	_ = jsTry(func() { ctx.Call("resume") })
	return nil
}

// armGesture (under mu) installs the one-shot document listeners.
func (d *jsDevice) armGesture() {
	if d.gestureOn {
		return
	}
	doc := js.Global().Get("document")
	if !doc.Truthy() {
		return
	}
	for _, name := range jsGestureEvents {
		_ = jsTry(func() {
			doc.Call("addEventListener", name, d.onGesture, map[string]any{"once": true, "capture": true})
		})
	}
	d.gestureOn = true
}

// disarmGesture (under mu) removes them; harmless for one already gone.
func (d *jsDevice) disarmGesture() {
	if !d.gestureOn {
		return
	}
	doc := js.Global().Get("document")
	if doc.Truthy() {
		for _, name := range jsGestureEvents {
			_ = jsTry(func() {
				doc.Call("removeEventListener", name, d.onGesture, map[string]any{"capture": true})
			})
		}
	}
	d.gestureOn = false
}

// onNeed answers the play worklet: as many frames as bring its ring back to
// target, rendered, resampled to the context rate and shipped as one
// Float32Array — its bytes copied in bulk, never a sample at a time — with
// the buffer transferred. Always an answer, even an empty one, since the
// worklet asks again only once it has one.
func (d *jsDevice) onNeed(_ js.Value, args []js.Value) any {
	if len(args) == 0 {
		return nil
	}
	msg := args[0].Get("data")
	if msg.Type() != js.TypeObject || msg.Get("t").String() != "need" {
		return nil
	}
	d.mu.Lock()
	play, closed := d.play, d.closed
	d.mu.Unlock()
	if closed || !play.Truthy() {
		return nil
	}
	avail := 0
	if a := msg.Get("avail"); a.Type() == js.TypeNumber {
		avail = a.Int()
	}
	frames := (jsTargetFrames*d.frameLen - avail + d.frameLen - 1) / d.frameLen
	if frames < 1 {
		frames = 1
	}
	if frames > jsRingFrames {
		frames = jsRingFrames
	}
	out := d.renderFrames(frames, d.outF[:0])
	d.outF = out
	b := d.floatBytes(out)
	_ = jsTry(func() {
		arr := js.Global().Get("Float32Array").New(len(out))
		buf := arr.Get("buffer")
		js.CopyBytesToJS(js.Global().Get("Uint8Array").New(buf), b)
		play.Get("port").Call("postMessage", map[string]any{"t": "pcm", "pcm": arr}, []any{buf})
	})
	return nil
}

// renderFrames appends n frames from the session's Render, resampled to the
// context's rate, to out.
func (d *jsDevice) renderFrames(n int, out []float32) []float32 {
	for i := 0; i < n; i++ {
		for j := range d.renderBuf {
			d.renderBuf[j] = 0
		}
		if d.io.Render != nil {
			d.io.Render(d.renderBuf[:])
		}
		jsInt16ToFloat32(d.renderF[:], d.renderBuf[:])
		out = d.up.Process(d.renderF[:], out)
	}
	return out
}

// floatBytes is out as little-endian float32 bytes, in a reused buffer —
// what CopyBytesToJS takes.
func (d *jsDevice) floatBytes(out []float32) []byte {
	n := len(out)
	if cap(d.outBytes) < 4*n {
		d.outBytes = make([]byte, 4*n)
	}
	b := d.outBytes[:4*n]
	for i, v := range out {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(v))
	}
	return b
}

// openLegacy is Open's ending on a page without an AudioWorklet: a
// ScriptProcessorNode of the smallest power-of-two size holding one frame
// (1024 samples at 44.1 and 48 kHz), fed from a queue the session's Render
// tops up on every callback, connected to the destination — it runs only
// while connected. On the main thread, which is fine for listening. Called
// with mu held; releases it.
func (d *jsDevice) openLegacy(ctx js.Value, io DeviceIO) error {
	size := 256
	for size < d.frameLen {
		size *= 2
	}
	var node js.Value
	err := jsTry(func() {
		node = ctx.Call("createScriptProcessor", size, 1, 1)
		node.Set("onaudioprocess", js.Undefined())
	})
	if err != nil || !node.Truthy() {
		_ = jsTry(func() { ctx.Call("close") })
		d.ctx = js.Undefined()
		d.mu.Unlock()
		return errors.New("this browser has neither an AudioWorklet nor a ScriptProcessorNode")
	}
	onProcess := js.FuncOf(d.onProcess)
	d.funcs = append(d.funcs, onProcess)
	node.Set("onaudioprocess", onProcess)
	_ = jsTry(func() { node.Call("connect", ctx.Get("destination")) })
	d.legacy, d.legacySize = node, size
	d.legacyQ = make([]float32, 0, size+2*d.frameLen)
	d.ready = true
	d.wantCapture = false

	// The same resume dance as the worklet path: the context runs only
	// after a gesture.
	d.onState = js.FuncOf(d.stateChanged)
	d.onGesture = js.FuncOf(d.gesture)
	d.funcs = append(d.funcs, d.onState, d.onGesture)
	_ = jsTry(func() { ctx.Call("addEventListener", "statechange", d.onState) })
	_ = jsTry(func() { ctx.Call("resume") })
	tap := ctx.Get("state").String() != "running"
	if tap {
		d.armGesture()
	}
	d.mu.Unlock()
	if io.Notice != nil {
		if tap {
			io.Notice(jsTapNotice)
		} else {
			io.Notice(jsListenOnlyNotice)
		}
	}
	return nil
}

// onProcess is the ScriptProcessorNode's callback: the queue topped up to
// the node's size from the session, then that many samples copied into the
// output buffer in one go.
func (d *jsDevice) onProcess(_ js.Value, args []js.Value) any {
	if len(args) == 0 {
		return nil
	}
	d.mu.Lock()
	closed, size := d.closed, d.legacySize
	d.mu.Unlock()
	if closed || size == 0 {
		return nil
	}
	for len(d.legacyQ) < size {
		d.legacyQ = d.renderFrames(1, d.legacyQ)
	}
	b := d.floatBytes(d.legacyQ[:size])
	_ = jsTry(func() {
		ch := args[0].Get("outputBuffer").Call("getChannelData", 0)
		js.CopyBytesToJS(js.Global().Get("Uint8Array").New(ch.Get("buffer"), ch.Get("byteOffset"), 4*size), b)
	})
	d.legacyQ = append(d.legacyQ[:0], d.legacyQ[size:]...)
	return nil
}

// onCap takes the capture worklet's frameLen samples, copied out in bulk,
// resamples them to 16 kHz and hands over every whole 320-sample frame,
// carrying the remainder to the next message.
func (d *jsDevice) onCap(_ js.Value, args []js.Value) any {
	if len(args) == 0 {
		return nil
	}
	msg := args[0].Get("data")
	if msg.Type() != js.TypeObject || msg.Get("t").String() != "cap" {
		return nil
	}
	pcm := msg.Get("pcm")
	if !pcm.Truthy() {
		return nil
	}
	d.mu.Lock()
	on := d.capturing && !d.closed
	d.mu.Unlock()
	if !on {
		return nil
	}
	nBytes := pcm.Get("byteLength").Int()
	if cap(d.capBytes) < nBytes {
		d.capBytes = make([]byte, nBytes)
	}
	b := d.capBytes[:nBytes]
	if err := jsTry(func() {
		u8 := js.Global().Get("Uint8Array").New(pcm.Get("buffer"), pcm.Get("byteOffset"), nBytes)
		js.CopyBytesToGo(b, u8)
	}); err != nil {
		return nil
	}
	n := nBytes / 4
	if cap(d.capF) < n {
		d.capF = make([]float32, n)
	}
	f := d.capF[:n]
	for i := range f {
		f[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	d.capAcc = d.down.Process(f, d.capAcc)
	for len(d.capAcc) >= FrameSamples {
		jsFloat32ToInt16(d.capFrame[:], d.capAcc[:FrameSamples])
		if d.io.Capture != nil {
			d.io.Capture(d.capFrame[:])
		}
		d.capAcc = d.capAcc[:copy(d.capAcc, d.capAcc[FrameSamples:])]
	}
	return nil
}

// SetCapture opens or closes the microphone. Opening asks getUserMedia
// right here, synchronously, on the caller's goroutine: the unmute click's
// frame runs inside the browser's transient-activation window, and the
// prompt must be asked for from within it. The answer comes back on the
// event loop, through CaptureState. Idempotent; a request that arrives
// before the module is ready waits for it.
func (d *jsDevice) SetCapture(on bool) {
	if !on {
		d.stopCapture()
		d.report(false, nil)
		return
	}
	d.mu.Lock()
	switch {
	case d.closed:
		d.mu.Unlock()
		d.report(false, errors.New("the audio device is closed"))
		return
	case d.capturing || d.opening:
		d.mu.Unlock()
		return
	case d.failed:
		d.mu.Unlock()
		d.report(false, errJSUnsupported)
		return
	case d.legacy.Truthy():
		// A page without an AudioWorklet is a page without a microphone
		// (the same secure-context rule), whatever isSecureContext says.
		d.mu.Unlock()
		d.report(false, errJSNeedsHTTPS)
		return
	case !jsSecure():
		// No mediaDevices at all is what an http page gets: say so rather
		// than prompt for something that cannot be granted.
		d.mu.Unlock()
		d.report(false, errJSNeedsHTTPS)
		return
	case !d.ready:
		d.wantCapture = true
		d.mu.Unlock()
		return
	}
	d.opening = true
	ctx := d.ctx
	d.mu.Unlock()

	constraints := map[string]any{"audio": map[string]any{
		"echoCancellation": true,
		"noiseSuppression": true,
		"autoGainControl":  true,
		"channelCount":     1,
	}}
	var promise js.Value
	err := jsTry(func() {
		promise = js.Global().Get("navigator").Get("mediaDevices").Call("getUserMedia", constraints)
	})
	// A gesture-driven moment: the best one to resume a context Safari has
	// kept suspended.
	_ = jsTry(func() { ctx.Call("resume") })
	if err == nil {
		var onOK, onErr js.Func
		onOK = js.FuncOf(func(_ js.Value, args []js.Value) any {
			defer onOK.Release()
			defer onErr.Release()
			d.streamOpened(args)
			return nil
		})
		onErr = js.FuncOf(func(_ js.Value, args []js.Value) any {
			defer onOK.Release()
			defer onErr.Release()
			d.streamRefused(args)
			return nil
		})
		err = jsTry(func() { promise.Call("then", onOK, onErr) })
		if err != nil {
			onOK.Release()
			onErr.Release()
		}
	}
	if err != nil {
		d.mu.Lock()
		d.opening = false
		d.mu.Unlock()
		d.report(false, fmt.Errorf("could not open the microphone: %v", err))
	}
}

// streamOpened is the granted microphone: source → jetris-cap → a silent
// GainNode → destination (a worklet node that does not reach the
// destination is never processed), the port handler installed, and the
// report. A stream that arrives after the request was withdrawn — muted
// again, or the game left, while the prompt was up — is stopped at once so
// the browser's "in use" light goes out.
func (d *jsDevice) streamOpened(args []js.Value) {
	var stream js.Value
	if len(args) > 0 {
		stream = args[0]
	}
	d.mu.Lock()
	if d.closed || !d.opening || !stream.Truthy() {
		d.opening = false
		d.mu.Unlock()
		jsStopTracks(stream)
		return
	}
	var source, cap, gain js.Value
	err := jsTry(func() {
		source = d.ctx.Call("createMediaStreamSource", stream)
		cap = js.Global().Get("AudioWorkletNode").New(d.ctx, "jetris-cap", map[string]any{
			"numberOfInputs":     1,
			"numberOfOutputs":    1,
			"outputChannelCount": []any{1},
			"processorOptions":   map[string]any{"frameLen": d.frameLen},
		})
		gain = d.ctx.Call("createGain")
		gain.Get("gain").Set("value", 0)
		source.Call("connect", cap)
		cap.Call("connect", gain)
		gain.Call("connect", d.ctx.Get("destination"))
	})
	if err != nil {
		d.opening = false
		d.mu.Unlock()
		jsStopTracks(stream)
		jsDisconnect(source, cap, gain)
		d.report(false, fmt.Errorf("could not open the microphone: %v", err))
		return
	}
	onCap := js.FuncOf(d.onCap)
	cap.Get("port").Set("onmessage", onCap)
	d.stream, d.source, d.cap, d.gain = stream, source, cap, gain
	d.capFuncs = []js.Func{onCap}
	d.down = newJSResampler(d.rate, SampleRate)
	d.capAcc = d.capAcc[:0]
	d.opening, d.capturing = false, true
	ctx := d.ctx
	d.mu.Unlock()
	_ = jsTry(func() { ctx.Call("resume") })
	d.report(true, nil)
}

// streamRefused is the prompt's no, or a microphone that could not be had.
func (d *jsDevice) streamRefused(args []js.Value) {
	d.mu.Lock()
	withdrawn := d.closed || !d.opening
	d.opening = false
	d.mu.Unlock()
	if withdrawn {
		return
	}
	var e js.Value
	if len(args) > 0 {
		e = args[0]
	}
	d.report(false, jsMediaError(e))
}

// jsMediaError turns getUserMedia's DOMException into the line the player
// reads.
func jsMediaError(e js.Value) error {
	name, msg := "", ""
	if e.Type() == js.TypeObject {
		if v := e.Get("name"); v.Type() == js.TypeString {
			name = v.String()
		}
		if v := e.Get("message"); v.Type() == js.TypeString {
			msg = v.String()
		}
	}
	switch name {
	case "NotAllowedError", "PermissionDeniedError":
		return errors.New("microphone permission denied")
	case "NotFoundError", "DevicesNotFoundError":
		return errors.New("no microphone found")
	case "NotReadableError", "TrackStartError":
		return errors.New("the microphone is in use by another app")
	case "SecurityError":
		return errJSNeedsHTTPS
	}
	switch {
	case msg != "":
		return errors.New(msg)
	case name != "":
		return errors.New(name)
	}
	return errors.New("could not open the microphone")
}

// stopCapture closes the microphone: every track stopped (the "in use"
// light), the chain disconnected, its handler released, a pending prompt's
// answer disowned.
func (d *jsDevice) stopCapture() {
	d.mu.Lock()
	d.opening, d.wantCapture, d.capturing = false, false, false
	stream, source, cap, gain := d.stream, d.source, d.cap, d.gain
	fns := d.capFuncs
	d.stream, d.source, d.cap, d.gain = js.Undefined(), js.Undefined(), js.Undefined(), js.Undefined()
	d.capFuncs = nil
	d.mu.Unlock()

	jsStopTracks(stream)
	if cap.Truthy() {
		_ = jsTry(func() {
			port := cap.Get("port")
			port.Set("onmessage", js.Null())
			port.Call("close")
		})
	}
	jsDisconnect(source, cap, gain)
	for _, f := range fns {
		f.Release()
	}
}

// Close is the microphone off, the play node cut, the context closed, the
// document listeners removed and every long-lived js.Func released.
// Idempotent.
func (d *jsDevice) Close() {
	d.stopCapture()
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	d.ready = false
	d.disarmGesture()
	ctx, play, legacy, onState := d.ctx, d.play, d.legacy, d.onState
	funcs := d.funcs
	d.funcs = nil
	d.play = js.Undefined()
	d.legacy = js.Undefined()
	d.mu.Unlock()

	if legacy.Truthy() {
		_ = jsTry(func() {
			legacy.Set("onaudioprocess", js.Null())
			legacy.Call("disconnect")
		})
	}
	if play.Truthy() {
		_ = jsTry(func() {
			port := play.Get("port")
			port.Set("onmessage", js.Null())
			port.Call("close")
			play.Call("disconnect")
		})
	}
	if ctx.Truthy() {
		if onState.Truthy() {
			_ = jsTry(func() { ctx.Call("removeEventListener", "statechange", onState) })
		}
		_ = jsTry(func() { ctx.Call("close") })
	}
	for _, f := range funcs {
		f.Release()
	}
}

// report is CaptureState, when there is one.
func (d *jsDevice) report(open bool, err error) {
	if d.io.CaptureState != nil {
		d.io.CaptureState(open, err)
	}
}

// jsSecure is whether getUserMedia can be asked at all: a secure context
// (https, or localhost) with mediaDevices present.
func jsSecure() bool {
	win := js.Global()
	if v := win.Get("isSecureContext"); v.Type() != js.TypeBoolean || !v.Bool() {
		return false
	}
	nav := win.Get("navigator")
	if !nav.Truthy() {
		return false
	}
	md := nav.Get("mediaDevices")
	return md.Truthy() && md.Get("getUserMedia").Truthy()
}

// jsStopTracks stops every track of a MediaStream.
func jsStopTracks(stream js.Value) {
	if !stream.Truthy() {
		return
	}
	_ = jsTry(func() {
		tracks := stream.Call("getTracks")
		for i := 0; i < tracks.Length(); i++ {
			tracks.Index(i).Call("stop")
		}
	})
}

// jsDisconnect disconnects whichever of the nodes exist.
func jsDisconnect(nodes ...js.Value) {
	for _, n := range nodes {
		if n.Truthy() {
			_ = jsTry(func() { n.Call("disconnect") })
		}
	}
}

// jsTry runs f, converting a thrown JavaScript exception into an error.
func jsTry(f func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(js.Error); ok {
				err = errors.New(e.Get("message").String())
				return
			}
			err = fmt.Errorf("%v", r)
		}
	}()
	f()
	return nil
}

// jsResampler converts a stream between two rates by linear interpolation,
// carrying its phase and the last input sample from one call to the next,
// so a stream fed in any chunking comes out as one continuous signal. Good
// enough for speech that IMA ADPCM is about to quantise to four bits.
type jsResampler struct {
	step float64 // input samples per output sample
	pos  float64 // the next output's position, in input samples from in[0]
	last float32 // the previous call's final sample, at position −1
}

func newJSResampler(inRate, outRate int) *jsResampler {
	return &jsResampler{step: float64(inRate) / float64(outRate)}
}

// Process consumes all of in, appending every output sample that falls
// within it to out, and returns out. An output between the previous call's
// last sample and in[0] is interpolated across the boundary.
func (r *jsResampler) Process(in, out []float32) []float32 {
	n := len(in)
	if n == 0 {
		return out
	}
	for r.pos < float64(n-1) {
		i := int(math.Floor(r.pos))
		f := float32(r.pos - float64(i))
		a := r.last
		if i >= 0 {
			a = in[i]
		}
		b := in[i+1]
		out = append(out, a+(b-a)*f)
		r.pos += r.step
	}
	r.pos -= float64(n)
	r.last = in[n-1]
	return out
}

// jsInt16ToFloat32 is the wire's samples as Web Audio's, −1..1.
func jsInt16ToFloat32(dst []float32, src []int16) {
	for i := range dst {
		dst[i] = float32(src[i]) / 32768
	}
}

// jsFloat32ToInt16 is the reverse, clipped and rounded.
func jsFloat32ToInt16(dst []int16, src []float32) {
	for i := range dst {
		v := src[i] * 32767
		switch {
		case v >= 32767:
			dst[i] = 32767
		case v <= -32768:
			dst[i] = -32768
		case v >= 0:
			dst[i] = int16(v + 0.5)
		default:
			dst[i] = int16(v - 0.5)
		}
	}
}
