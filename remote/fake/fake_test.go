package fake

import (
	"context"
	"io"
	"math"
	"math/cmplx"
	"testing"
)

func TestToneFrequency(t *testing.T) {
	d := &Device{Tone: 120e3}
	d.SetSampleRate(2_400_000)
	r, err := d.Samples(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2*2400)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	var sum float64
	var prev complex128
	for i := 0; i < len(buf); i += 2 {
		v := complex(float64(buf[i])-127.5, float64(buf[i+1])-127.5)
		if i > 0 {
			sum += cmplx.Phase(v * cmplx.Conj(prev))
		}
		prev = v
	}
	f := sum / float64(len(buf)/2-1) / (2 * math.Pi) * 2_400_000
	if math.Abs(f-120e3) > 100 {
		t.Errorf("tone at %.0f Hz, want 120000±100", f)
	}
}
