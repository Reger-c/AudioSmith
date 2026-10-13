package analysis

import (
	"math"
	"math/cmplx"
)

// fft is a radix-2 complex FFT of a fixed power-of-two size.
type fft struct {
	n   int
	tw  []complex128
	rev []int
}

func newFFT(n int) *fft {
	f := &fft{n: n, tw: make([]complex128, n/2), rev: make([]int, n)}
	for i := range f.tw {
		f.tw[i] = cmplx.Exp(complex(0, -2*math.Pi*float64(i)/float64(n)))
	}
	bits := 0
	for 1<<bits < n {
		bits++
	}
	for i := range n {
		r := 0
		for b := range bits {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		f.rev[i] = r
	}
	return f
}

// transform computes the forward DFT of x in place.
func (f *fft) transform(x []complex128) {
	for i, r := range f.rev {
		if i < r {
			x[i], x[r] = x[r], x[i]
		}
	}
	for size := 2; size <= f.n; size <<= 1 {
		half, step := size/2, f.n/size
		for start := 0; start < f.n; start += size {
			for k := range half {
				t := f.tw[k*step] * x[start+k+half]
				x[start+k+half] = x[start+k] - t
				x[start+k] += t
			}
		}
	}
}

// nextPow2 returns the smallest power of two ≥ n.
func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// hann returns a periodic Hann window of n points.
func hann(n int) []float64 {
	w := make([]float64, n)
	for i := range w {
		w[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
	}
	return w
}
