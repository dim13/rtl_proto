package dsp

import (
	"math"
	"math/cmplx"
	"math/rand/v2"
	"testing"
)

func tone(n int, freq float64) []complex64 {
	x := make([]complex64, n)
	for i := range x {
		x[i] = complex64(cmplx.Exp(complex(0, 2*math.Pi*freq*float64(i))))
	}
	return x
}

func power(x []complex64) float64 {
	var p float64
	for _, v := range x {
		p += float64(real(v)*real(v) + imag(v)*imag(v))
	}
	return p / float64(len(x))
}

func TestNCOShift(t *testing.T) {
	x := tone(1000, 0.1)
	NewNCO(-0.1).Mix(x)
	for i, v := range x {
		if cmplx.Abs(complex128(v)-1) > 1e-3 {
			t.Fatalf("[%d]: %v, want ~1", i, v)
		}
	}
}

func TestNCOPhaseContinuous(t *testing.T) {
	one := make([]complex64, 1000)
	for i := range one {
		one[i] = 1
	}
	chunked := append([]complex64(nil), one...)
	NewNCO(0.013).Mix(one)
	n := NewNCO(0.013)
	for i := 0; i < 1000; i += 100 {
		n.Mix(chunked[i : i+100])
	}
	for i := range one {
		if cmplx.Abs(complex128(one[i]-chunked[i])) > 1e-5 {
			t.Fatalf("[%d]: %v vs %v", i, one[i], chunked[i])
		}
	}

	x := make([]complex64, 200)
	for i := range x {
		x[i] = 1
	}
	n = NewNCO(0.01)
	n.Mix(x[:100])
	n.SetFreq(0.05)
	n.Mix(x[100:])
	step := cmplx.Phase(complex128(x[100]) * cmplx.Conj(complex128(x[99])))
	if d := math.Abs(step - 2*math.Pi*0.05); d > 1e-4 && math.Abs(step-2*math.Pi*0.01) > 1e-4 {
		t.Errorf("phase step at switch %v, want 2π·0.01 or 2π·0.05 (no jump)", step)
	}
}

// response returns |H(f)| of taps at frequency f (cycles per sample).
func response(taps []float32, f float64) float64 {
	var s complex128
	for k, h := range taps {
		s += complex(float64(h), 0) * cmplx.Exp(complex(0, -2*math.Pi*f*float64(k)))
	}
	return cmplx.Abs(s)
}

func TestLowpassResponse(t *testing.T) {
	taps := Lowpass(0.1, 0.15)
	if g := response(taps, 0); math.Abs(g-1) > 1e-3 {
		t.Errorf("DC gain %v", g)
	}
	for i := range 512 {
		f := 0.5 * float64(i) / 512
		db := 20 * math.Log10(response(taps, f))
		switch {
		case f <= 0.1 && math.Abs(db) > 0.5:
			t.Fatalf("passband %.3f: %.2f dB", f, db)
		case f >= 0.15 && db > -60:
			t.Fatalf("stopband %.3f: %.2f dB", f, db)
		}
	}
}

func TestDecimatorChunked(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 4))
	x := make([]complex64, 5000)
	for i := range x {
		x[i] = complex(r.Float32()*2-1, r.Float32()*2-1)
	}
	taps := Lowpass(0.05, 0.1)
	want := NewDecimator(taps, 7).Process(nil, x)
	d := NewDecimator(taps, 7)
	var got []complex64
	var buf []complex64
	for _, c := range [][2]int{{0, 1}, {1, 14}, {14, 1013}, {1013, 5000}} {
		buf = d.Process(buf, x[c[0]:c[1]])
		got = append(got, buf...)
	}
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if cmplx.Abs(complex128(got[i]-want[i])) > 1e-5 {
			t.Fatalf("[%d]: %v vs %v", i, got[i], want[i])
		}
	}
}

func TestDecimatorRejectsAlias(t *testing.T) {
	taps := Lowpass(0.04, 0.06)
	out := NewDecimator(taps, 10).Process(nil, tone(20000, 0.3))
	if db := 10 * math.Log10(power(out[len(taps):])); db > -60 {
		t.Errorf("alias at 0.3: %.1f dB, want ≤ -60", db)
	}
	out = NewDecimator(taps, 10).Process(nil, tone(20000, 0.01))
	if db := 10 * math.Log10(power(out[len(taps):])); math.Abs(db) > 0.5 {
		t.Errorf("passband at 0.01: %.2f dB, want 0±0.5", db)
	}
}
