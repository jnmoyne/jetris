package voice

import "math"

// The echo canceller: what the browser's getUserMedia does for its page and
// the desktop's microphone does not. Without it a player on speakers sends
// the room back to itself — every voice the speakers play is picked up by
// the microphone, opens the gate and goes out again, a round trip later.
//
// It runs on the Session's encode loop, one microphone frame at a time,
// paired with the frame of far-end audio the speakers were handed at that
// moment (Session.refFrame): the echo in the microphone is that audio,
// delayed by the two devices' buffers and the room, and shaped by the
// speaker, the room and the microphone. Two stages:
//
//   - A linear adaptive filter learns that echo path and subtracts its
//     prediction: a multidelay block frequency-domain filter, the path cut
//     into aecParts partitions of aecBlock samples (400 ms in all — the
//     devices' latency and the room's tail), each adapted per frequency bin
//     and normalised by the far end's power in that bin. It learns in two
//     copies: a background filter that always adapts and a foreground one
//     that makes the output, taking the background's weights only once they
//     do better — so a burst of both ends talking at once, which throws an
//     adapting filter off, never reaches the output. The learning rate
//     follows how much echo is left: a leakage estimate, the share of the
//     echo the filter still misses, read from how the error's spectrum
//     moves with the prediction's (speech at the near end does not move
//     with it), sets the step bin by bin, so the filter learns fast while it
//     is wrong and hardly at all while the near end talks.
//   - A residual suppressor then turns the block down while what is left
//     is mostly echo: the predicted echo times the leakage (a tenth at the
//     least) or, until the filter has adapted, the far end's loudest recent
//     block times the lowest ratio of what is left to it seen lately.
//     Near-end speech louder than that estimate passes; echo alone is taken
//     down by up to 40 dB. And a prediction that would leave a block louder
//     than the microphone had it is not taken off at all.
//
// Both signals are pre-emphasised (a first difference) before the filter,
// which evens out speech's spectrum and speeds the learning, and the output
// is de-emphasised back. With a silent far end — nobody talking, "Play
// voice" off — every prediction is zero and the output is the microphone,
// sample for sample.

const (
	aecBlock   = 64                                       // the filter's block: 4 ms, five to a frame
	aecFFTSize = 2 * aecBlock                             // overlap-save: this block and the last
	aecBins    = aecFFTSize/2 + 1                         // the bins of a real signal's spectrum
	aecTailMs  = 400                                      // the echo path the filter spans
	aecParts   = aecTailMs * SampleRate / 1000 / aecBlock // its partitions
	aecPreemph = 0.9                                      // the pre-emphasis coefficient

	// A block's energies are sums of squares over aecBlock samples of the
	// pre-emphasised signals. aecFloor is the least worth comparing (an rms
	// of 2); aecActive the far end's average that counts as talking (rms
	// 20); aecAdaptMin the far-end block below which the filter learns
	// nothing (rms 10). aecReg regularises the per-bin normalisation.
	aecFloor    = aecBlock * 4
	aecActive   = aecBlock * 400
	aecAdaptMin = aecBlock * 100
	aecReg      = aecFFTSize * 100

	// The learning: the fixed rate's ceiling until the filter has adapted
	// (aecAdaptedSum of it spent), the leakage estimate's floor, and the
	// rates its statistics move at.
	aecInitRate   = 0.5
	aecAdaptedSum = aecParts
	aecMinLeak    = 0.005
	aecLeakBase   = 2.0 * aecBlock / SampleRate
	aecLeakMax    = 0.5 * aecBlock / SampleRate
	aecSpecSmooth = float64(aecBlock) / SampleRate

	// The two paths: the background takes over the output once it leaves
	// 30% less, and is thrown back to the foreground once it leaves eight
	// times more. aecBadBlocks of a foreground louder than its input (four
	// times, 200 ms of it) reset the whole canceller.
	aecTakeOver  = 0.7
	aecThrowBack = 8
	aecBadBlocks = 50

	// The residual suppressor: its over-subtraction; the least it takes to
	// be left of the prediction (a tenth: the leakage is an average, and a
	// block's residue — a syllable's onset above all — can stand well above
	// it); its deepest cut (-40 dB); and the lower envelope's rates (down
	// within tens of milliseconds, up over seconds).
	aecOversub  = 2
	aecResFloor = 0.1
	aecGainMin  = 0.01
	aecErlDown  = 0.05
	aecErlUp    = 0.002
	aecErlTrack = 0.1
)

// echoCanceller is one microphone's canceller, run by one goroutine (the
// Session's encode loop).
type echoCanceller struct {
	f *fft

	// The far end: its spectra over the last aecParts blocks, a ring whose
	// newest row is head; per bin, the power summed over the ring (the
	// filter's normalisation); each row's block energy and their sum; and
	// the last block, the first half of the next transform.
	xs     []complex128
	head   int
	xpow   [aecBins]float64
	xen    [aecParts]float64
	xenSum float64
	xlast  [aecBlock]float64

	w, wf []complex128 // the background and foreground filters, aecParts rows of aecBins

	dMem, xMem, oMem float64 // the pre-emphasis and de-emphasis memories

	adapted    bool
	sumAdapt   float64
	saturated  int
	pey, pyy   float64
	eh, yh     [aecBins]float64
	leak       float64
	rot        int
	see, sff   float64 // smoothed block energies: the background's error, the foreground's
	sdd        float64 // and the microphone's
	bad        int
	erl        float64 // the lower envelope of what is left over the far end's level
	eeS        float64 // the output's smoothed energy, for erl
	gain       float64
	use        float64 // how much of the prediction the last block took off
	dirty      bool    // anything learned or heard since the last reset
	linearOnly bool    // the tests' look at the filter alone, the suppressor off

	tmp            [aecFFTSize]float64
	ys, es, yspec  [aecBins]complex128
	step           [aecBins]float64
	yb, yf, eb, ef [aecBlock]float64
}

func newEchoCanceller() *echoCanceller {
	c := &echoCanceller{
		f:  newFFT(aecFFTSize),
		xs: make([]complex128, aecParts*aecBins),
		w:  make([]complex128, aecParts*aecBins),
		wf: make([]complex128, aecParts*aecBins),
	}
	c.dirty = true
	c.reset()
	return c
}

// reset forgets everything: the filters, the far end's history and every
// statistic — a microphone opened afresh, or a pairing with the speakers
// made anew. Cheap when there is nothing to forget.
func (c *echoCanceller) reset() {
	if !c.dirty {
		return
	}
	clear(c.xs)
	clear(c.w)
	clear(c.wf)
	f, lin := c.f, c.linearOnly
	xs, w, wf := c.xs, c.w, c.wf
	*c = echoCanceller{f: f, xs: xs, w: w, wf: wf, linearOnly: lin}
	c.pey, c.pyy, c.leak = 1, 1, 1
	c.erl, c.gain, c.use = 1, 1, 1
}

// Process cancels the echo out of one microphone frame. mic and ref are
// FrameSamples each, ref the far end's samples paired with this frame; out
// takes the result and may be mic itself.
func (c *echoCanceller) Process(mic, ref, out []int16) {
	for b := 0; b+aecBlock <= len(mic); b += aecBlock {
		c.block(mic[b:b+aecBlock], ref[b:b+aecBlock], out[b:b+aecBlock])
	}
}

func abs2(v complex128) float64 { return real(v)*real(v) + imag(v)*imag(v) }

// row is the far end's spectrum m blocks ago.
func (c *echoCanceller) row(m int) []complex128 {
	r := (c.head - m + aecParts) % aecParts
	return c.xs[r*aecBins : (r+1)*aecBins]
}

// predict is a filter's echo prediction for the current block, in time.
func (c *echoCanceller) predict(w []complex128, y *[aecBlock]float64) {
	clear(c.yspec[:])
	for m := 0; m < aecParts; m++ {
		wr, xr := w[m*aecBins:(m+1)*aecBins], c.row(m)
		for k := range c.yspec {
			c.yspec[k] += wr[k] * xr[k]
		}
	}
	c.f.inverseReal(c.yspec[:], c.tmp[:])
	copy(y[:], c.tmp[aecBlock:])
}

// block cancels one aecBlock of a frame.
func (c *echoCanceller) block(mic, ref, out []int16) {
	var d, x [aecBlock]float64
	var sxx float64
	for i := range mic {
		m, r := float64(mic[i]), float64(ref[i])
		if mic[i] >= 32000 || mic[i] <= -32000 {
			c.saturated = 2 // a clipped microphone is no echo path: this block and the next teach nothing
		}
		d[i], c.dMem = m-aecPreemph*c.dMem, m
		x[i], c.xMem = r-aecPreemph*c.xMem, r
		sxx += x[i] * x[i]
	}
	if sxx > 0 {
		c.dirty = true
	}

	// The far end's newest spectrum joins the ring in place of the oldest.
	c.head = (c.head + 1) % aecParts
	row := c.row(0)
	for k := range row {
		c.xpow[k] -= abs2(row[k])
	}
	copy(c.tmp[:aecBlock], c.xlast[:])
	copy(c.tmp[aecBlock:], x[:])
	c.xlast = x
	c.f.forwardReal(c.tmp[:], row)
	for k := range row {
		c.xpow[k] = max(c.xpow[k]+abs2(row[k]), 0)
	}
	c.xenSum = max(c.xenSum+sxx-c.xen[c.head], 0)
	c.xen[c.head] = sxx
	if c.head == 0 {
		c.resum()
	}
	xe := c.xenSum / aecParts
	active := xe > aecActive

	// Both filters' predictions, and what each leaves.
	c.predict(c.w, &c.yb)
	c.predict(c.wf, &c.yf)
	var sdd, see, sff, syy, sey float64
	for i := range d {
		c.eb[i] = d[i] - c.yb[i]
		c.ef[i] = d[i] - c.yf[i]
		sdd += d[i] * d[i]
		see += c.eb[i] * c.eb[i]
		sff += c.ef[i] * c.ef[i]
		syy += c.yb[i] * c.yb[i]
		sey += c.eb[i] * c.yb[i]
	}
	if math.IsNaN(see+sff) || math.IsInf(see+sff, 0) {
		c.dirty = true
		c.reset()
		copy(out, mic)
		return
	}

	// The two paths: the output is the foreground's, unless the background
	// has just proved the better — then it is the background's, and the
	// background becomes the foreground.
	c.see += 0.2 * (see - c.see)
	c.sff += 0.2 * (sff - c.sff)
	c.sdd += 0.2 * (sdd - c.sdd)
	e, y := &c.ef, &c.yf
	threwBack := false
	if active {
		switch {
		case c.see < aecTakeOver*c.sff && c.sff > aecFloor:
			copy(c.wf, c.w)
			c.sff = c.see
			e, y = &c.eb, &c.yb
		case c.see > aecThrowBack*c.sff+aecFloor:
			copy(c.w, c.wf)
			c.see = c.sff
			threwBack = true
		}
		if c.sff > 4*c.sdd+aecFloor {
			c.bad++
		} else {
			c.bad = 0
		}
		if c.bad > aecBadBlocks {
			c.reset()
			copy(out, mic)
			return
		}
	}
	if c.saturated > 0 {
		c.saturated--
	} else if sxx > aecAdaptMin && !threwBack {
		c.adapt(see, syy, sey, sxx)
	}

	// A prediction that leaves the block louder than the microphone had it
	// is not taken off at all: an echo the filter makes up — its long tail's
	// learning noise, sounding on in a pause after the far end's syllable
	// has died away — is worse than the echo it would cancel. The switch is
	// eased across the block, so the output never jumps.
	var ee, ey float64
	for i := range e {
		ee += e[i] * e[i]
		ey += y[i] * y[i]
	}
	use := 1.0
	if ee > sdd {
		use = 0
	}
	if use != c.use || use == 0 {
		ee = 0
		for i := range e {
			w := c.use + (use-c.use)*float64(i+1)/aecBlock
			e[i] = d[i] - w*y[i]
			ee += e[i] * e[i]
		}
	}
	c.use = use

	// The residual suppressor, then the de-emphasis, the gain eased in
	// across the block.
	rf := max(c.leak, aecResFloor)
	if use < 1 {
		rf = 1 // a prediction not taken off leaves all the echo it stood for
	}
	res := rf * ey
	if !c.adapted {
		// Measured against the far end's loudest block in the filter's
		// span, which the echo of a syllable's onset follows at once, where
		// the span's mean would still be counting the silence before it.
		var xm float64
		for _, v := range c.xen {
			xm = max(xm, v)
		}
		c.eeS += aecErlTrack * (ee - c.eeS)
		if active {
			r := c.eeS / (xm + aecFloor)
			if r < c.erl {
				c.erl += aecErlDown * (r - c.erl)
			} else {
				c.erl += aecErlUp * (r - c.erl)
			}
		}
		res = max(res, 2*c.erl*xm) // twice: an envelope along the dips sits well under the ratio's run
	}
	g := 1.0
	if res > 0 && !c.linearOnly {
		g = max(aecGainMin, 1-aecOversub*res/(ee+aecFloor))
	}
	// Down at once — an echo's onset must not slip out while the gain comes
	// down, and the block before it was quiet — and back up gently, eased
	// across the block.
	from := c.gain
	switch {
	case g < c.gain:
		from = g
	case g > c.gain:
		if g = c.gain + 0.2*(g-c.gain); g > 0.999 {
			g = 1
		}
	}
	for i := range e {
		o := e[i] + aecPreemph*c.oMem
		c.oMem = o
		out[i] = clamp16(o * (from + (g-from)*float64(i+1)/aecBlock))
	}
	c.gain = g
}

// adapt moves the background filter a step along the echo path, the step
// bin by bin as large as the share of the error that is still echo.
func (c *echoCanceller) adapt(see, syy, sey, sxx float64) {
	clear(c.tmp[:aecBlock])
	copy(c.tmp[aecBlock:], c.eb[:])
	c.f.forwardReal(c.tmp[:], c.es[:])
	clear(c.tmp[:aecBlock])
	copy(c.tmp[aecBlock:], c.yb[:])
	c.f.forwardReal(c.tmp[:], c.ys[:])

	// The leakage: how the error's power moves with the prediction's, over
	// how much the prediction's moves at all. The statistics move faster
	// while the prediction outweighs the error.
	var pey, pyy float64
	for k := range c.es {
		rf, yf := abs2(c.es[k]), abs2(c.ys[k])
		c.eh[k] += aecSpecSmooth * (rf - c.eh[k])
		c.yh[k] += aecSpecSmooth * (yf - c.yh[k])
		pey += (rf - c.eh[k]) * (yf - c.yh[k])
		pyy += (yf - c.yh[k]) * (yf - c.yh[k])
	}
	alpha := min(aecLeakBase*syy/(see+1), aecLeakMax)
	c.pey += alpha * (pey - c.pey)
	c.pyy += alpha * (pyy - c.pyy)
	c.pyy = max(c.pyy, 1)
	c.pey = min(max(c.pey, aecMinLeak*c.pyy), c.pyy)
	c.leak = c.pey / c.pyy

	if !c.adapted {
		// Until the leakage means something: a fixed rate, smaller the more
		// the error outweighs the far end (someone talking at this end).
		mu := min(aecInitRate*sxx, aecInitRate*see) / (see + 1)
		if c.sumAdapt += mu; c.sumAdapt > aecAdaptedSum {
			c.adapted = true
		}
		for k := range c.step {
			c.step[k] = 2 * mu / (c.xpow[k] + aecReg)
		}
	} else {
		rer := min(max((1e-4*sxx+3*c.leak*syy)/(see+1), sey*sey/(1+see*syy)), 0.5)
		for k := range c.step {
			e := abs2(c.es[k]) + 1
			r := min(c.leak*abs2(c.ys[k]), 0.5*e)
			r = 0.7*r + 0.3*rer*e
			c.step[k] = 2 * (r / e) / (c.xpow[k] + aecReg)
		}
	}
	for m := 0; m < aecParts; m++ {
		wr, xr := c.w[m*aecBins:(m+1)*aecBins], c.row(m)
		for k := range wr {
			x := xr[k]
			wr[k] += complex(c.step[k], 0) * complex(real(x), -imag(x)) * c.es[k]
		}
	}
	// The step's circular wrap trimmed off each partition's filter: the
	// newest partition every block, the others one a block in turn.
	c.constrain(0)
	c.constrain(1 + c.rot)
	c.rot = (c.rot + 1) % (aecParts - 1)
}

// constrain cuts partition m's filter back to aecBlock taps.
func (c *echoCanceller) constrain(m int) {
	wr := c.w[m*aecBins : (m+1)*aecBins]
	c.f.inverseReal(wr, c.tmp[:])
	clear(c.tmp[aecBlock:])
	c.f.forwardReal(c.tmp[:], wr)
}

// resum recounts the running sums over the ring, so their rounding never
// drifts.
func (c *echoCanceller) resum() {
	clear(c.xpow[:])
	for m := 0; m < aecParts; m++ {
		for k, v := range c.row(m) {
			c.xpow[k] += abs2(v)
		}
	}
	c.xenSum = 0
	for _, v := range c.xen {
		c.xenSum += v
	}
}

// clamp16 is a sample rounded back to 16 bits.
func clamp16(v float64) int16 {
	switch {
	case v >= 32767:
		return 32767
	case v <= -32768:
		return -32768
	}
	return int16(math.Round(v))
}
