package voice

import (
	"math"
	"math/cmplx"
	"math/rand"
	"testing"
)

// The FFT agrees with the DFT written out, and its inverse undoes it.
func TestFFTMatchesTheDFT(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{2, 8, 128} {
		f := newFFT(n)
		x := make([]complex128, n)
		for i := range x {
			x[i] = complex(rng.NormFloat64(), rng.NormFloat64())
		}
		got := append([]complex128(nil), x...)
		f.transform(got, false)
		for k := 0; k < n; k++ {
			var want complex128
			for i, v := range x {
				want += v * cmplx.Exp(complex(0, -2*math.Pi*float64(i*k)/float64(n)))
			}
			if cmplx.Abs(got[k]-want) > 1e-9*float64(n) {
				t.Fatalf("n=%d bin %d = %v, want %v", n, k, got[k], want)
			}
		}
		f.transform(got, true)
		for i := range x {
			if cmplx.Abs(got[i]-x[i]) > 1e-12*float64(n) {
				t.Fatalf("n=%d: inverse(forward(x))[%d] = %v, want %v", n, i, got[i], x[i])
			}
		}
		// The real pair: n real samples there and back through n/2+1 bins.
		r := make([]float64, n)
		for i := range r {
			r[i] = rng.NormFloat64()
		}
		spec := make([]complex128, n/2+1)
		back := make([]float64, n)
		f.forwardReal(r, spec)
		f.inverseReal(spec, back)
		for i := range r {
			if math.Abs(back[i]-r[i]) > 1e-12*float64(n) {
				t.Fatalf("n=%d: real round trip [%d] = %v, want %v", n, i, back[i], r[i])
			}
		}
	}
}
