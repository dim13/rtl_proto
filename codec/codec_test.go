package codec

import (
	"bytes"
	"errors"
	"math"
	"math/rand/v2"
	"testing"
)

func randomFrame(n int) []byte {
	r := rand.New(rand.NewPCG(1, 2))
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(r.UintN(256))
	}
	return b
}

func counterFrame(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

func roundTrip(t *testing.T, in []byte, bits int) ([]byte, float32) {
	t.Helper()
	data, step, err := Encode(nil, in, bits)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(in))
	if err := Decode(out, data, bits, step); err != nil {
		t.Fatal(err)
	}
	return out, step
}

func TestRoundTrip8Exact(t *testing.T) {
	in := randomFrame(16384)
	out, step := roundTrip(t, in, 8)
	if step != 0 {
		t.Errorf("step: got %v, want 0", step)
	}
	if !bytes.Equal(out, in) {
		t.Error("8-bit round trip not exact")
	}
}

func TestRoundTripErrorBound(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   []byte
	}{
		{name: "random", in: randomFrame(16384)},
		{name: "counter", in: counterFrame(16384)},
	} {
		for bits := 1; bits <= 7; bits++ {
			out, step := roundTrip(t, tc.in, bits)
			limit := float64(step)/2 + 0.5
			for i := range tc.in {
				if d := math.Abs(float64(out[i]) - float64(tc.in[i])); d > limit {
					t.Errorf("%s bits %d [%d]: got %d, want %d±%.2f", tc.name, bits, i, out[i], tc.in[i], limit)
					break
				}
			}
		}
	}
}

func TestQuietLossless(t *testing.T) {
	in := make([]byte, 1024)
	for i := range in {
		in[i] = byte(124 + i%8)
	}
	out, step := roundTrip(t, in, 3)
	if step != 1 {
		t.Errorf("step: got %v, want 1", step)
	}
	if !bytes.Equal(out, in) {
		t.Error("quiet frame not lossless")
	}
}

func TestConstantFrame(t *testing.T) {
	for _, v := range []byte{0, 127, 128, 255} {
		in := bytes.Repeat([]byte{v}, 256)
		for bits := 1; bits <= 7; bits++ {
			out, step := roundTrip(t, in, bits)
			if s := float64(step); math.IsNaN(s) || math.IsInf(s, 0) {
				t.Fatalf("value %d bits %d: step %v", v, bits, step)
			}
			limit := float64(step)/2 + 0.5
			for i := range in {
				if d := math.Abs(float64(out[i]) - float64(v)); d > limit {
					t.Errorf("value %d bits %d: got %d", v, bits, out[i])
					break
				}
			}
		}
	}
}

func TestPackedLength(t *testing.T) {
	for bits := 1; bits <= 8; bits++ {
		for _, n := range []int{2, 6, 1000, 16384} {
			data, _, err := Encode(nil, counterFrame(n), bits)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := len(data), (n*bits+7)/8; got != want {
				t.Errorf("bits %d n %d: got %d, want %d", bits, n, got, want)
			}
		}
	}
}

func TestOddPackingTail(t *testing.T) {
	in := []byte{127, 128, 126, 129, 125, 130}
	out, _ := roundTrip(t, in, 3)
	if !bytes.Equal(out, in) {
		t.Errorf("got %v, want %v", out, in)
	}
}

func TestBitsRange(t *testing.T) {
	for _, bits := range []int{0, 9} {
		if _, _, err := Encode(nil, []byte{1, 2}, bits); !errors.Is(err, ErrBits) {
			t.Errorf("Encode bits %d: got %v, want ErrBits", bits, err)
		}
		if err := Decode(make([]byte, 2), []byte{1, 2}, bits, 1); !errors.Is(err, ErrBits) {
			t.Errorf("Decode bits %d: got %v, want ErrBits", bits, err)
		}
	}
}

func BenchmarkEncode4(b *testing.B) {
	in := randomFrame(16384)
	var dst []byte
	b.SetBytes(int64(len(in)))
	for b.Loop() {
		dst, _, _ = Encode(dst, in, 4)
	}
}

func BenchmarkDecode4(b *testing.B) {
	in := randomFrame(16384)
	data, step, _ := Encode(nil, in, 4)
	out := make([]byte, len(in))
	b.SetBytes(int64(len(in)))
	for b.Loop() {
		Decode(out, data, 4, step)
	}
}

// Frames arrive from the network; a short payload must not panic.
func TestDecodeShort(t *testing.T) {
	for _, bits := range []int{3, 8} {
		if err := Decode(make([]byte, 16), make([]byte, 2), bits, 1); !errors.Is(err, ErrShort) {
			t.Errorf("bits %d: got %v, want ErrShort", bits, err)
		}
	}
}
