package voice

// The browser's AudioContext runs at whatever rate the hardware does —
// 48000 on most machines, 44100 on some — and hands the worklet float32
// samples, while everything else here is 16 kHz int16 frames. This file is
// the bridge: a linear resampler that carries its phase from one call to
// the next, so that a non-integer ratio like 44100/16000 neither drifts
// nor clicks at block boundaries, and the two conversions between float
// and int16. Linear interpolation is crude next to a windowed sinc, but
// for speech at these rates it is inaudible over the codec, and it costs
// nothing.

// Resampler converts a stream of float32 samples from one rate to another,
// in one direction; a device that needs both keeps two. Between calls it
// remembers the last input sample it consumed and where the next output
// sample falls relative to the input still to come, so that a stream cut
// into blocks of any size resamples exactly as it would in one piece.
//
// The phase is kept as a whole number of input samples and a fraction over
// outRate, in integers, rather than as a float: every output sample then
// falls exactly where it should — a 48000 to 16000 conversion takes every
// third sample and interpolates nothing — and the phase cannot drift over
// an hour of audio.
type Resampler struct {
	inRate, outRate int
	invOut          float32 // 1 / outRate, to turn num into a fraction
	// idx and num place the next output sample at idx + num/outRate input
	// samples from the first sample of the next Process's input; idx can be
	// as low as -1, the position of prev.
	idx, num int
	prev     float32 // the last input sample consumed, the one at position -1
}

// NewResampler resamples from inRate to outRate, in hertz.
func NewResampler(inRate, outRate int) *Resampler {
	return &Resampler{inRate: inRate, outRate: outRate, invOut: 1 / float32(outRate)}
}

// Process resamples in into out until one of them runs out and reports how
// much of each it used: consumed is the number of input samples that will
// never be needed again (the caller passes the rest again next time), and
// produced the output samples written. It allocates nothing.
//
// An output sample at position p interpolates between the input samples at
// floor(p) and floor(p)+1, so the last input sample cannot be used until
// the next call brings the one after it; it is kept as prev and the phase
// counted back from it.
func (r *Resampler) Process(in, out []float32) (consumed, produced int) {
	idx, num := r.idx, r.num
	for produced < len(out) && idx+1 < len(in) {
		a := r.prev
		if idx >= 0 {
			a = in[idx]
		}
		b := in[idx+1]
		out[produced] = a + (b-a)*float32(num)*r.invOut
		produced++
		num += r.inRate
		idx += num / r.outRate
		num %= r.outRate
	}
	consumed = min(len(in), idx+1)
	if consumed > 0 {
		r.prev = in[consumed-1]
	}
	r.idx, r.num = idx-consumed, num
	return consumed, produced
}

// Float32ToInt16 converts samples in [-1, 1] to int16, clamping anything
// outside and rounding to the nearest sample.
func Float32ToInt16(dst []int16, src []float32) {
	for i, v := range src[:min(len(dst), len(src))] {
		s := min(max(v, -1), 1) * 32767
		if s >= 0 {
			s += 0.5
		} else {
			s -= 0.5
		}
		dst[i] = int16(s)
	}
}

// Int16ToFloat32 converts samples to [-1, 1).
func Int16ToFloat32(dst []float32, src []int16) {
	for i, s := range src[:min(len(dst), len(src))] {
		dst[i] = float32(s) / 32768
	}
}

// FrameAssembler is the capture side of the bridge: it takes the blocks of
// float32 samples a browser's worklet produces, at the context's rate and
// in whatever size it likes, and hands out FrameSamples-sample int16 frames
// at SampleRate as they complete.
type FrameAssembler struct {
	rs    Resampler
	buf   [FrameSamples]float32 // the frame being assembled
	fill  int
	frame [FrameSamples]int16
}

// NewFrameAssembler assembles frames from samples at inRate.
func NewFrameAssembler(inRate int) *FrameAssembler {
	return &FrameAssembler{rs: *NewResampler(inRate, SampleRate)}
}

// Push takes a block in and calls emit for every frame it completes; the
// frame is the assembler's own and is reused, so emit copies it.
func (a *FrameAssembler) Push(in []float32, emit func(pcm []int16)) {
	for {
		c, p := a.rs.Process(in, a.buf[a.fill:])
		in, a.fill = in[c:], a.fill+p
		if a.fill == FrameSamples {
			Float32ToInt16(a.frame[:], a.buf[:])
			emit(a.frame[:])
			a.fill = 0
		}
		if len(in) == 0 {
			return
		}
	}
}

// FramePlayer is the playback side: it fills the blocks of float32 samples
// a browser's worklet plays, at the context's rate, from the
// FrameSamples-sample int16 frames it asks for as it needs them.
type FramePlayer struct {
	rs    Resampler
	frame [FrameSamples]int16
	buf   [FrameSamples]float32 // the current frame, as floats
	pos   int                   // how much of buf the resampler has consumed
}

// NewFramePlayer plays frames out at outRate.
func NewFramePlayer(outRate int) *FramePlayer {
	return &FramePlayer{rs: *NewResampler(SampleRate, outRate), pos: FrameSamples}
}

// Pull fills out, calling need for each new frame it runs into; the frame
// arrives zeroed, as DeviceIO.Render promises, and what need leaves unfilled
// plays as silence.
func (p *FramePlayer) Pull(out []float32, need func(pcm []int16)) {
	for len(out) > 0 {
		if p.pos == FrameSamples {
			clear(p.frame[:])
			need(p.frame[:])
			Int16ToFloat32(p.buf[:], p.frame[:])
			p.pos = 0
		}
		c, n := p.rs.Process(p.buf[p.pos:], out)
		p.pos += c
		out = out[n:]
	}
}
