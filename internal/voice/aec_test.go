package voice

import (
	"math"
	"math/rand"
	"testing"
)

// The canceller's tests run a far end through a simulated room: an echo
// path of a delay and a decaying tail, the microphone picking it up with
// whatever the near end says and a little hiss.

// talk is n samples of speech-like noise at an rms of rms: low-passed, in
// syllables of 100 to 300 ms with gaps of 50 to 200 ms between them.
func talk(rng *rand.Rand, n int, rms float64) []float64 {
	out := make([]float64, n)
	var lp1, lp2, env float64
	on, left := false, 0
	for i := range out {
		if left == 0 {
			if on = !on; on {
				left = 1600 + rng.Intn(3200)
			} else {
				left = 800 + rng.Intn(2400)
			}
		}
		left--
		target := 0.0
		if on {
			target = 1
		}
		env += 0.005 * (target - env)
		lp1 = 0.7*lp1 + rng.NormFloat64()
		lp2 = 0.5*lp2 + lp1
		out[i] = lp2 * env
	}
	var sum float64
	for _, v := range out {
		sum += v * v
	}
	k := rms / math.Sqrt(sum/float64(n))
	for i := range out {
		out[i] *= k
	}
	return out
}

// room is an echo path: delay samples of nothing, then taps decaying over
// their length, their energy the path's gain.
type room struct {
	delay int
	taps  []float64
}

func newRoom(rng *rand.Rand, delay, length int, gainDb float64) room {
	taps := make([]float64, length)
	var sum float64
	for i := range taps {
		taps[i] = rng.NormFloat64() * math.Exp(-float64(i)/float64(length/5))
		sum += taps[i] * taps[i]
	}
	k := math.Pow(10, gainDb/20) / math.Sqrt(sum)
	for i := range taps {
		taps[i] *= k
	}
	return room{delay, taps}
}

// echo is the path's output at sample i of x.
func (r room) echo(x []float64, i int) float64 {
	var s float64
	for j, h := range r.taps {
		k := i - r.delay - j
		if k < 0 {
			break
		}
		s += h * x[k]
	}
	return s
}

// aecRun is a canceller's run over far and near, the path changing to
// rooms[1] at switchAt (0: never): the echo it was given, sample by sample,
// and what it made of the microphone.
type aecRun struct {
	echo, near, out []float64
}

func runCanceller(c *echoCanceller, far, near []float64, rooms []room, switchAt int) aecRun {
	rng := rand.New(rand.NewSource(99))
	n := len(far) / FrameSamples * FrameSamples
	run := aecRun{echo: make([]float64, n), near: near[:n], out: make([]float64, n)}
	mic := make([]int16, FrameSamples)
	ref := make([]int16, FrameSamples)
	for f := 0; f < n; f += FrameSamples {
		for i := 0; i < FrameSamples; i++ {
			r := rooms[0]
			if switchAt > 0 && f+i >= switchAt {
				r = rooms[1]
			}
			run.echo[f+i] = r.echo(far, f+i)
			mic[i] = clamp16(run.echo[f+i] + near[f+i] + 3*rng.NormFloat64())
			ref[i] = clamp16(far[f+i])
		}
		c.Process(mic, ref, mic)
		for i, v := range mic {
			run.out[f+i] = float64(v)
		}
	}
	return run
}

// erleDb is the echo return loss enhancement over [from, to) seconds: the
// echo's energy over the output's.
func (r aecRun) erleDb(from, to float64) float64 {
	a, b := int(from*SampleRate), int(to*SampleRate)
	var e, o float64
	for i := a; i < b; i++ {
		e += r.echo[i] * r.echo[i]
		o += r.out[i] * r.out[i]
	}
	return 10 * math.Log10(e/max(o, 1))
}

// kept is how much of the near end the output keeps over [from, to)
// seconds: the output projected on it, 1 for all of it.
func (r aecRun) kept(from, to float64) float64 {
	a, b := int(from*SampleRate), int(to*SampleRate)
	var on, nn float64
	for i := a; i < b; i++ {
		on += r.out[i] * r.near[i]
		nn += r.near[i] * r.near[i]
	}
	return on / nn
}

func silence(n int) []float64 { return make([]float64, n) }

// A far end talking into a laptop's room: the echo comes back 75 ms late
// through 100 ms of tail at -6 dB, and within a few seconds it is gone —
// the filter alone takes most of it, the suppressor the rest.
func TestEchoCancellerRemovesTheEcho(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	far := talk(rng, 10*SampleRate, 3000)
	path := newRoom(rng, 1200, 1600, -6)

	lin := newEchoCanceller()
	lin.linearOnly = true
	r := runCanceller(lin, far, silence(len(far)), []room{path}, 0)
	for s := 0; s < 10; s++ {
		t.Logf("filter alone, second %d: ERLE %.1f dB", s, r.erleDb(float64(s), float64(s+1)))
	}
	if got := r.erleDb(4, 10); got < 18 {
		t.Errorf("the filter alone takes %.1f dB of echo once converged, want at least 18", got)
	}

	r = runCanceller(newEchoCanceller(), far, silence(len(far)), []room{path}, 0)
	for s := 0; s < 10; s++ {
		t.Logf("with the suppressor, second %d: ERLE %.1f dB", s, r.erleDb(float64(s), float64(s+1)))
	}
	if got := r.erleDb(1, 3); got < 15 {
		t.Errorf("the canceller takes %.1f dB of echo in its first seconds, want at least 15", got)
	}
	if got := r.erleDb(4, 10); got < 30 {
		t.Errorf("the canceller takes %.1f dB of echo once converged, want at least 30", got)
	}
}

// Both ends talking at once: the near end comes through, and the filter,
// learning through it, is still there afterwards.
func TestEchoCancellerKeepsTheNearEnd(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	n := 12 * SampleRate
	far := talk(rng, n, 3000)
	near := silence(n)
	copy(near[5*SampleRate:9*SampleRate], talk(rng, 4*SampleRate, 1500))
	r := runCanceller(newEchoCanceller(), far, near, []room{newRoom(rng, 1200, 1600, -6)}, 0)
	for s := 0; s < 12; s++ {
		t.Logf("second %d: ERLE %.1f dB, near kept %.2f", s, r.erleDb(float64(s), float64(s+1)), r.kept(float64(s), float64(s+1)))
	}
	if got := r.kept(5, 9); got < 0.7 {
		t.Errorf("the near end came through at %.2f of its level while both talked, want at least 0.7", got)
	}
	if got := r.erleDb(10, 12); got < 25 {
		t.Errorf("after both talked the canceller takes %.1f dB of echo, want at least 25", got)
	}
}

// With headphones there is no echo to cancel: the far end talks, the near
// end talks, and the near end comes through untouched.
func TestEchoCancellerLeavesNoEchoAlone(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	n := 6 * SampleRate
	far := talk(rng, n, 3000)
	near := talk(rng, n, 1500)
	r := runCanceller(newEchoCanceller(), far, near, []room{{delay: 0, taps: []float64{0}}}, 0)
	if got := r.kept(1, 6); got < 0.9 {
		t.Errorf("with no echo the near end came through at %.2f, want at least 0.9", got)
	}
}

// The laptop moved: a new path, and the canceller follows.
func TestEchoCancellerFollowsAMove(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	n := 12 * SampleRate
	far := talk(rng, n, 3000)
	rooms := []room{newRoom(rng, 1200, 1600, -6), newRoom(rng, 2000, 1600, -8)}
	r := runCanceller(newEchoCanceller(), far, silence(n), rooms, 6*SampleRate)
	for s := 0; s < 12; s++ {
		t.Logf("second %d: ERLE %.1f dB", s, r.erleDb(float64(s), float64(s+1)))
	}
	if got := r.erleDb(9, 12); got < 25 {
		t.Errorf("three seconds after the move the canceller takes %.1f dB of echo, want at least 25", got)
	}
}

// Headphones taken off mid-game: the canceller, which has had nothing to
// cancel, finds the new echo within a couple of seconds.
func TestEchoCancellerFindsANewEcho(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	n := 12 * SampleRate
	far := talk(rng, n, 3000)
	rooms := []room{{taps: []float64{0}}, newRoom(rng, 1200, 1600, -6)}
	r := runCanceller(newEchoCanceller(), far, silence(n), rooms, 4*SampleRate)
	for s := 4; s < 12; s++ {
		t.Logf("second %d: ERLE %.1f dB", s, r.erleDb(float64(s), float64(s+1)))
	}
	if got := r.erleDb(7, 10); got < 15 {
		t.Errorf("three seconds after the echo appeared the canceller takes %.1f dB of it, want at least 15", got)
	}
}

// A silent far end changes nothing: the output is the microphone, sample
// for sample, even after the filter has learned a path.
func TestEchoCancellerPassesTheMicrophoneThrough(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	c := newEchoCanceller()
	far := talk(rng, 4*SampleRate, 3000)
	runCanceller(c, far, silence(len(far)), []room{newRoom(rng, 1200, 1600, -6)}, 0)
	ref := make([]int16, FrameSamples)
	for f := 0; f < 50; f++ { // a second of silence: the echo tail runs out
		c.Process(make([]int16, FrameSamples), ref, make([]int16, FrameSamples))
	}
	for f := 0; f < 50; f++ {
		mic := make([]int16, FrameSamples)
		for i := range mic {
			mic[i] = int16(rng.Intn(20001) - 10000)
		}
		out := make([]int16, FrameSamples)
		c.Process(mic, ref, out)
		for i := range mic {
			if out[i] != mic[i] {
				t.Fatalf("frame %d sample %d: %d out for %d in, with a silent far end", f, i, out[i], mic[i])
			}
		}
	}
}

func BenchmarkEchoCanceller(b *testing.B) {
	rng := rand.New(rand.NewSource(6))
	c := newEchoCanceller()
	mic := make([]int16, FrameSamples)
	ref := make([]int16, FrameSamples)
	for i := range mic {
		mic[i], ref[i] = int16(rng.Intn(4001)-2000), int16(rng.Intn(4001)-2000)
	}
	out := make([]int16, FrameSamples)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Process(mic, ref, out)
	}
}
