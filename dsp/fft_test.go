package dsp

import (
	"math"
	"math/cmplx"
	"math/rand/v2"
	"testing"
)

func dft(x []complex64) []complex128 {
	n := len(x)
	out := make([]complex128, n)
	for k := range n {
		var s complex128
		for t, v := range x {
			s += complex128(v) * cmplx.Exp(complex(0, -2*math.Pi*float64(k*t)/float64(n)))
		}
		out[k] = s
	}
	return out
}

func TestFFTMatchesDFT(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, n := range []int{8, 64, 1024} {
		x := make([]complex64, n)
		for i := range x {
			x[i] = complex(r.Float32()*2-1, r.Float32()*2-1)
		}
		want := dft(x)
		FFT(x)
		for k := range n {
			got := complex128(x[k])
			if d := cmplx.Abs(got - want[k]); d > 1e-3*math.Max(1, cmplx.Abs(want[k])) {
				t.Fatalf("n %d bin %d: got %v, want %v", n, k, got, want[k])
			}
		}
	}
}

func TestFFTTone(t *testing.T) {
	const n, k = 64, 5
	x := make([]complex64, n)
	for i := range x {
		x[i] = complex64(cmplx.Exp(complex(0, 2*math.Pi*k*float64(i)/n)))
	}
	FFT(x)
	for i, v := range x {
		m := cmplx.Abs(complex128(v))
		if i == k && math.Abs(m-n) > 1e-3 || i != k && m > 1e-3 {
			t.Errorf("bin %d: magnitude %v", i, m)
		}
	}
}

func TestHannSymmetric(t *testing.T) {
	w := Hann(8)
	if w[0] != 0 || math.Abs(float64(w[4])-1) > 1e-6 {
		t.Errorf("w[0] %v, w[4] %v", w[0], w[4])
	}
	for i := 1; i < 8; i++ {
		if math.Abs(float64(w[i]-w[8-i])) > 1e-6 {
			t.Errorf("w[%d] %v != w[%d] %v", i, w[i], 8-i, w[8-i])
		}
	}
}

func TestShift(t *testing.T) {
	x := []complex64{0, 1, 2, 3}
	Shift(x)
	for i, want := range []complex64{2, 3, 0, 1} {
		if x[i] != want {
			t.Fatalf("got %v", x)
		}
	}
}

func BenchmarkFFT2048(b *testing.B) {
	x := make([]complex64, 2048)
	for b.Loop() {
		FFT(x)
	}
}
