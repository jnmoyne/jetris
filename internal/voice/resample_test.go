package voice

import (
	"math"
	"testing"
)

// resampleAll runs src through rs in blocks — the input block sizes and
// the output block sizes each cycling through their lists — and returns
// everything produced: a stream cut up arbitrarily on both sides.
func resampleAll(rs *Resampler, src []float32, inSizes, outSizes []int) []float32 {
	var got []float32
	var i, o int
	for len(src) > 0 {
		n := min(inSizes[i%len(inSizes)], len(src))
		i++
		block := src[:n]
		for len(block) > 0 {
			out := make([]float32, outSizes[o%len(outSizes)])
			o++
			c, p := rs.Process(block, out)
			got = append(got, out[:p]...)
			block = block[c:]
		}
		src = src[n:]
	}
	return got
}

// outputsFor is how many output samples n input samples yield: one for
// every position k·in/out short of the last input sample, which waits for
// the sample after it.
func outputsFor(n, inRate, outRate int) int {
	return ((n-1)*outRate + inRate - 1) / inRate
}

// crossings counts sign changes: twice the number of cycles of a sine.
func crossings(s []float32) int {
	var n int
	for i := 1; i < len(s); i++ {
		if (s[i] >= 0) != (s[i-1] >= 0) {
			n++
		}
	}
	return n
}

// A constant stays constant through any ratio, block boundaries included.
func TestResamplerDC(t *testing.T) {
	src := make([]float32, 4000)
	for i := range src {
		src[i] = 0.5
	}
	for _, rates := range [][2]int{{16000, 48000}, {48000, 16000}, {44100, 16000}, {16000, 44100}} {
		got := resampleAll(NewResampler(rates[0], rates[1]), src, []int{128, 7, 1, 300}, []int{100, 1, 320})
		if len(got) == 0 {
			t.Fatalf("%v: nothing produced", rates)
		}
		for i, v := range got {
			if v != 0.5 {
				t.Fatalf("%v: sample %d = %g, want 0.5", rates, i, v)
			}
		}
	}
}

// A ramp resamples to the same ramp — linear interpolation of a line is
// exact — so every output sample must land where the phase says, however
// the input and output are cut into blocks. This is the test of the phase
// carried across calls.
func TestResamplerPhaseAcrossBlocks(t *testing.T) {
	src := make([]float32, 16000)
	for i := range src {
		src[i] = float32(i) * 1e-3
	}
	for _, rates := range [][2]int{{16000, 48000}, {48000, 16000}, {44100, 16000}, {16000, 44100}} {
		step := float64(rates[0]) / float64(rates[1])
		got := resampleAll(NewResampler(rates[0], rates[1]), src, []int{1, 7, 128, 3, 50, 960}, []int{100, 1, 13, 320})
		for k, v := range got {
			want := float64(k) * step * 1e-3
			if math.Abs(float64(v)-want) > 1e-4 {
				t.Fatalf("%v: output %d = %g, want %g", rates, k, v, want)
			}
		}
		// Every output position short of the last input sample is produced,
		// and no other: the phase is exact.
		want := outputsFor(len(src), rates[0], rates[1])
		if len(got) != want {
			t.Fatalf("%v: produced %d samples, want %d", rates, len(got), want)
		}
	}
}

// A sine keeps its frequency across a non-integer ratio in both directions.
func TestResamplerSineFrequency(t *testing.T) {
	sec := make([]float32, SampleRate)
	for i := range sec {
		sec[i] = float32(math.Sin(2 * math.Pi * 1000 * float64(i) / SampleRate))
	}
	up := resampleAll(NewResampler(SampleRate, 44100), sec, []int{320}, []int{128})
	if n := crossings(up); n < 1995 || n > 2005 {
		t.Fatalf("1 kHz at 44100: %d crossings in a second, want ≈ 2000", n)
	}
	down := resampleAll(NewResampler(44100, SampleRate), up, []int{128}, []int{320})
	if n := crossings(down); n < 1993 || n > 2005 {
		t.Fatalf("1 kHz back at 16000: %d crossings, want ≈ 2000", n)
	}
	if len(down) < SampleRate-8 || len(down) > SampleRate {
		t.Fatalf("a second of 16 kHz came back as %d samples", len(down))
	}
}

// A ramp of int16 samples up to 48 kHz and back down is itself, within the
// rounding of the two conversions.
func TestResamplerRoundTrip(t *testing.T) {
	src := make([]int16, 10*FrameSamples)
	for i := range src {
		src[i] = int16(20*i - 32000)
	}
	f := make([]float32, len(src))
	Int16ToFloat32(f, src)
	up := resampleAll(NewResampler(SampleRate, 48000), f, []int{FrameSamples}, []int{3 * FrameSamples})
	down := resampleAll(NewResampler(48000, SampleRate), up, []int{3 * FrameSamples}, []int{FrameSamples})
	if len(down) < len(src)-2 {
		t.Fatalf("round trip of %d samples produced %d", len(src), len(down))
	}
	back := make([]int16, len(down))
	Float32ToInt16(back, down)
	for i, s := range back {
		if d := int(s) - int(src[i]); d < -2 || d > 2 {
			t.Fatalf("sample %d came back as %d, was %d", i, s, src[i])
		}
	}
}

// Over many calls the sample counts stay in the ratio of the rates, both
// ways between 44100 and 16000: nothing is dropped or doubled at the seams.
func TestResamplerCountsAtNonIntegerRatio(t *testing.T) {
	for _, rates := range [][2]int{{44100, 16000}, {16000, 44100}} {
		rs := NewResampler(rates[0], rates[1])
		var consumed, produced int
		in, out := make([]float32, 128), make([]float32, 128)
		for consumed < 10*rates[0] {
			c, p := rs.Process(in, out)
			consumed += c
			produced += p
			if c == 0 && p == 0 {
				t.Fatalf("%v: no progress", rates)
			}
			copy(in, in[c:])
			in = in[:len(in)-c]
			if len(in) == 0 {
				in = make([]float32, 128)
			}
		}
		if want := outputsFor(consumed, rates[0], rates[1]); produced != want {
			t.Fatalf("%v: %d in, %d out, want %d", rates, consumed, produced, want)
		}
	}
}

// The conversions: full scale maps to full scale, anything beyond clamps,
// and a value survives a trip through int16 to within its rounding.
func TestSampleConversions(t *testing.T) {
	src := []float32{0, 1, -1, 2, -2, 0.5, -0.5, 1e-5}
	got := make([]int16, len(src))
	Float32ToInt16(got, src)
	want := []int16{0, 32767, -32767, 32767, -32767, 16384, -16384, 0}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%g -> %d, want %d", src[i], got[i], want[i])
		}
	}
	f := make([]float32, 3)
	Int16ToFloat32(f, []int16{-32768, 0, 16384})
	if f[0] != -1 || f[1] != 0 || f[2] != 0.5 {
		t.Fatalf("int16 -> %v", f)
	}
}

// The assembler turns a 48 kHz sine delivered in worklet-sized blocks into
// the 16 kHz frames SineFrames would have made of it.
func TestFrameAssembler(t *testing.T) {
	const rate, hz = 48000, 440
	amp := float32(math.Pow(10, -20.0/20))
	want := SineFrames(hz, -20, 50)
	a := NewFrameAssembler(rate)
	var got [][]int16
	block := make([]float32, 128)
	for n := 0; n < rate; n += len(block) {
		for i := range block {
			block[i] = amp * float32(math.Sin(2*math.Pi*hz*float64(n+i)/rate))
		}
		a.Push(block, func(pcm []int16) {
			got = append(got, append([]int16(nil), pcm...))
		})
	}
	if len(got) != len(want) {
		t.Fatalf("a second at 48 kHz made %d frames, want %d", len(got), len(want))
	}
	for f := range want {
		for i := range want[f] {
			if d := int(got[f][i]) - int(want[f][i]); d < -3 || d > 3 {
				t.Fatalf("frame %d sample %d = %d, want %d", f, i, got[f][i], want[f][i])
			}
		}
	}
}

// The player pulls frames as it needs them, each one zeroed first, and
// what it plays at the context's rate is the sine those frames carried.
func TestFramePlayer(t *testing.T) {
	frames := SineFrames(1000, -20, 60)
	var asked int
	p := NewFramePlayer(44100)
	var out []float32
	block := make([]float32, 128)
	for len(out) < 44100 {
		p.Pull(block, func(pcm []int16) {
			for i, s := range pcm {
				if s != 0 {
					t.Fatalf("frame %d arrived with sample %d = %d, want zeroed", asked, i, s)
				}
			}
			if asked < len(frames) {
				copy(pcm, frames[asked])
			}
			asked++
		})
		out = append(out, block...)
	}
	// 44100 samples at 2.75625 per 16 kHz sample is 16000 of them: 50
	// frames, plus the one the interpolation looks ahead into.
	if asked < 50 || asked > 52 {
		t.Fatalf("a second of playback asked for %d frames, want 50 or 51", asked)
	}
	if n := crossings(out); n < 1995 || n > 2005 {
		t.Fatalf("1 kHz played at 44100: %d crossings in a second, want ≈ 2000", n)
	}
}
