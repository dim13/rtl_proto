// Package dsp holds the signal processing shared by rtl_remote and
// rtl_gui: FFT, window, oscillator and decimating filter. Samples are
// complex64, with u8 input mapped to (x-127.5)/127.5.
package dsp

import (
	"math"
	"math/bits"
	"sync"
)

var twiddles sync.Map // n -> []complex64

// FFT transforms x in place. len(x) must be a power of two.
func FFT(x []complex64) {
	n := len(x)
	if n == 0 || n&(n-1) != 0 {
		panic("dsp: FFT size not a power of two")
	}
	shift := 64 - bits.TrailingZeros(uint(n))
	for i := range n {
		if j := int(bits.Reverse64(uint64(i)) >> shift); j > i {
			x[i], x[j] = x[j], x[i]
		}
	}
	w := twiddle(n)
	for size := 2; size <= n; size <<= 1 {
		half, step := size/2, n/size
		for start := 0; start < n; start += size {
			for k := range half {
				t := w[k*step] * x[start+k+half]
				x[start+k+half] = x[start+k] - t
				x[start+k] += t
			}
		}
	}
}

// twiddle returns e^{-2πik/n} for k < n/2, computed in float64.
func twiddle(n int) []complex64 {
	if w, ok := twiddles.Load(n); ok {
		return w.([]complex64)
	}
	w := make([]complex64, n/2)
	for k := range w {
		s, c := math.Sincos(-2 * math.Pi * float64(k) / float64(n))
		w[k] = complex(float32(c), float32(s))
	}
	twiddles.Store(n, w)
	return w
}

// Hann returns a periodic Hann window of n points.
func Hann(n int) []float32 {
	w := make([]float32, n)
	for i := range w {
		w[i] = float32(0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n)))
	}
	return w
}

// Shift swaps the halves of x so DC moves to index len(x)/2.
func Shift(x []complex64) {
	h := len(x) / 2
	for i := range h {
		x[i], x[i+h] = x[i+h], x[i]
	}
}
