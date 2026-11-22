package dsp

import "math"

// NCO is a complex oscillator with continuous phase.
type NCO struct {
	phase float64 // cycles, kept in [0, 1)
	freq  float64 // cycles per sample
}

func NewNCO(freq float64) *NCO {
	return &NCO{freq: freq}
}

// SetFreq changes the frequency without a phase jump.
func (n *NCO) SetFreq(freq float64) {
	n.freq = freq
}

// Mix multiplies x in place by e^{j2π·freq·k}.
func (n *NCO) Mix(x []complex64) {
	for i, v := range x {
		s, c := math.Sincos(2 * math.Pi * n.phase)
		x[i] = v * complex(float32(c), float32(s))
		n.phase += n.freq
		n.phase -= math.Floor(n.phase)
	}
}
