package remote

import "testing"

func BenchmarkChannel48k(b *testing.B) {
	c := chanConfig{rate: 48_000, bw: 38_400}
	p := newPipe(c, 2_400_000)
	raw := make([]byte, FrameBytes)
	b.SetBytes(FrameBytes)
	for b.Loop() {
		p.process(raw)
	}
}

func BenchmarkChannel240k(b *testing.B) {
	c := chanConfig{rate: 240_000, bw: 200_000}
	p := newPipe(c, 2_400_000)
	raw := make([]byte, FrameBytes)
	b.SetBytes(FrameBytes)
	for b.Loop() {
		p.process(raw)
	}
}
