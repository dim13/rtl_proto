package dsp

import "testing"

func BenchmarkNCOMix(b *testing.B) {
	x := make([]complex64, 8192)
	o := NewNCO(0.1)
	for b.Loop() {
		o.Mix(x)
	}
}

func BenchmarkDecimate240k(b *testing.B) {
	x := make([]complex64, 8192)
	d := NewDecimator(Lowpass(100e3/2.4e6, 140e3/2.4e6), 10)
	var out []complex64
	for b.Loop() {
		out = d.Process(out, x)
	}
}
