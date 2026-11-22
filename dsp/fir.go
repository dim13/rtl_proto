package dsp

import "math"

// attenuation is the stopband attenuation Lowpass designs for, in dB,
// with margin over the required 60 dB.
const attenuation = 70

// Lowpass designs a Kaiser-windowed sinc lowpass with unity DC gain,
// flat to pass and attenuated beyond stop (cycles per sample, 0..0.5).
func Lowpass(pass, stop float64) []float32 {
	n := LowpassTaps(pass, stop)
	beta := 0.1102 * (attenuation - 8.7)
	cutoff := (pass + stop) / 2
	taps := make([]float64, n)
	var sum float64
	m := float64(n-1) / 2
	for i := range taps {
		t := float64(i) - m
		sinc := 2 * cutoff
		if t != 0 {
			sinc = math.Sin(2*math.Pi*cutoff*t) / (math.Pi * t)
		}
		r := t / m
		taps[i] = sinc * bessel0(beta*math.Sqrt(1-r*r)) / bessel0(beta)
		sum += taps[i]
	}
	out := make([]float32, n)
	for i, v := range taps {
		out[i] = float32(v / sum)
	}
	return out
}

// LowpassTaps is the tap count Lowpass would use, to check a design's
// cost before building it.
func LowpassTaps(pass, stop float64) int {
	n := int(math.Ceil((attenuation-8)/(2.285*2*math.Pi*(stop-pass)))) + 1
	return n | 1 // odd: symmetric around a center tap
}

// bessel0 is the modified Bessel function of the first kind, order 0.
func bessel0(x float64) float64 {
	sum, term := 1.0, 1.0
	for k := 1; k < 50; k++ {
		term *= (x / (2 * float64(k))) * (x / (2 * float64(k)))
		sum += term
		if term < 1e-12*sum {
			break
		}
	}
	return sum
}

// Decimator filters complex samples and keeps every factor-th output,
// computing only the kept ones. State carries across Process calls.
type Decimator struct {
	taps   []float32
	sym    bool // taps mirror around the center: fold, half the multiplies
	factor int
	hist   []complex64 // last len(taps)-1 inputs
	skip   int         // inputs to pass before the next output
	buf    []complex64
}

func NewDecimator(taps []float32, factor int) *Decimator {
	sym := len(taps)%2 == 1
	for k := range len(taps) / 2 {
		sym = sym && taps[k] == taps[len(taps)-1-k]
	}
	return &Decimator{
		taps:   taps,
		sym:    sym,
		factor: factor,
		hist:   make([]complex64, len(taps)-1),
	}
}

// Process appends the decimated output of src to dst[:0].
func (d *Decimator) Process(dst, src []complex64) []complex64 {
	dst = dst[:0]
	d.buf = append(append(d.buf[:0], d.hist...), src...)
	h := len(d.hist)
	i := h + d.skip
	n := len(d.taps)
	for ; i < len(d.buf); i += d.factor {
		var re, im float32
		if d.sym {
			w := d.buf[i-n+1 : i+1] // oldest first
			for k, t := range d.taps[:n/2] {
				a, b := w[k], w[n-1-k]
				re += t * (real(a) + real(b))
				im += t * (imag(a) + imag(b))
			}
			v := w[n/2]
			re += d.taps[n/2] * real(v)
			im += d.taps[n/2] * imag(v)
		} else {
			for k, t := range d.taps {
				v := d.buf[i-k]
				re += t * real(v)
				im += t * imag(v)
			}
		}
		dst = append(dst, complex(re, im))
	}
	d.skip = i - len(d.buf)
	copy(d.hist, d.buf[len(d.buf)-h:])
	return dst
}
