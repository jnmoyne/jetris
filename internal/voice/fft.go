package voice

import (
	"math"
	"math/bits"
)

// fft is a radix-2 complex FFT of one power-of-two size, with its
// bit-reversal permutation and twiddles worked out once: the echo
// canceller's transforms (aec.go), a couple of thousand of 128 points a
// second. Not safe for concurrent use: buf is its own scratch.
type fft struct {
	n   int
	rev []int
	tw  []complex128 // e^(-2πik/n), k < n/2
	buf []complex128
}

func newFFT(n int) *fft {
	if n < 2 || n&(n-1) != 0 {
		panic("voice: fft size must be a power of two")
	}
	f := &fft{n: n, rev: make([]int, n), tw: make([]complex128, n/2), buf: make([]complex128, n)}
	shift := bits.UintSize - bits.TrailingZeros(uint(n))
	for i := range f.rev {
		f.rev[i] = int(bits.Reverse(uint(i)) >> shift)
	}
	for k := range f.tw {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(n))
		f.tw[k] = complex(c, s)
	}
	return f
}

// transform is the FFT in place: forward unscaled, inverse scaled by 1/n,
// so that one undoes the other.
func (f *fft) transform(x []complex128, inverse bool) {
	n := f.n
	for i, j := range f.rev {
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for k := 0; k < half; k++ {
				w := f.tw[k*step]
				if inverse {
					w = complex(real(w), -imag(w))
				}
				a, b := x[start+k], x[start+k+half]*w
				x[start+k], x[start+k+half] = a+b, a-b
			}
		}
	}
	if inverse {
		s := complex(1/float64(n), 0)
		for i := range x {
			x[i] *= s
		}
	}
}

// forwardReal is the spectrum of n real samples: its n/2+1 bins, the rest
// being their mirror image.
func (f *fft) forwardReal(in []float64, out []complex128) {
	for i, v := range in {
		f.buf[i] = complex(v, 0)
	}
	f.transform(f.buf, false)
	copy(out, f.buf[:f.n/2+1])
}

// inverseReal is the n real samples of a spectrum given by its n/2+1 bins.
func (f *fft) inverseReal(in []complex128, out []float64) {
	n := f.n
	copy(f.buf, in[:n/2+1])
	for k := 1; k < n/2; k++ {
		f.buf[n-k] = complex(real(in[k]), -imag(in[k]))
	}
	f.transform(f.buf, true)
	for i := range out {
		out[i] = real(f.buf[i])
	}
}
