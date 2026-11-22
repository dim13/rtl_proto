// Package fake provides a remote.Device for tests. Its sample stream
// is a byte counter: byte k of the stream is byte(k), continuing across
// readers.
package fake

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/dim13/r82xx/tuner"
)

var (
	ErrRate = errors.New("fake: sample rate rejected")
	ErrUSB  = errors.New("fake: usb failure")
)

const block = 16384

// State is what the device was told to do.
type State struct {
	Freq     float64
	Rate     uint32
	PPM      float64
	Manual   bool
	Gain     int
	AGC      bool
	BiasTee  bool
	Calls    []string // method names in call order
	Released int      // sample readers that saw their context end
}

// Device is a fake RTL-SDR. Set the exported fields before use.
type Device struct {
	Type      tuner.Type
	V4        bool
	Pace      time.Duration // sleep per 16384 bytes produced
	FailRate  bool          // SetSampleRate fails
	FailAfter int           // first reader fails after this many blocks, 0 never

	mu     sync.Mutex
	st     State
	k      uint64 // stream position
	failed bool
}

type reader struct {
	d        *Device
	ctx      context.Context
	fail     bool
	n        int // bytes produced by this reader
	released bool
}

// Snapshot returns a copy of the recorded state.
func (d *Device) Snapshot() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.st
	s.Calls = slices.Clone(s.Calls)
	return s
}

func (d *Device) record(name string, fn func(*State)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Calls = append(d.st.Calls, name)
	if fn != nil {
		fn(&d.st)
	}
}

func (d *Device) SetFrequency(hz float64) error {
	d.record("SetFrequency", func(s *State) { s.Freq = hz })
	return nil
}

func (d *Device) SetSampleRate(hz uint32) (float64, error) {
	if d.FailRate {
		d.record("SetSampleRate", nil)
		return 0, ErrRate
	}
	d.record("SetSampleRate", func(s *State) { s.Rate = hz })
	return float64(hz) + 0.5, nil
}

func (d *Device) SetBandwidth(hz, sampleRateHz float64) error {
	d.record("SetBandwidth", nil)
	return nil
}

func (d *Device) SetPPM(ppm float64) error {
	d.record("SetPPM", func(s *State) { s.PPM = ppm })
	return nil
}

func (d *Device) SetGainAuto() error {
	d.record("SetGainAuto", func(s *State) { s.Manual = false })
	return nil
}

func (d *Device) SetGainManual() error {
	d.record("SetGainManual", func(s *State) { s.Manual = true })
	return nil
}

func (d *Device) SetGain(tenthsDB int) error {
	d.record("SetGain", func(s *State) { s.Gain, s.Manual = tenthsDB, true })
	return nil
}

func (d *Device) SetAGC(on bool) error {
	d.record("SetAGC", func(s *State) { s.AGC = on })
	return nil
}

func (d *Device) SetBiasTee(on bool) error {
	d.record("SetBiasTee", func(s *State) { s.BiasTee = on })
	return nil
}

func (d *Device) Chip() tuner.Type {
	return d.Type
}

func (d *Device) BlogV4() bool {
	return d.V4
}

func (d *Device) Strings() (string, string, error) {
	return "Fake", "Fake RTL", nil
}

func (d *Device) Samples(ctx context.Context) (io.Reader, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	fail := d.FailAfter > 0 && !d.failed
	d.failed = d.failed || fail
	return &reader{d: d, ctx: ctx, fail: fail}, nil
}

// Read is not safe for concurrent use, like gousb's ReadStream.
func (r *reader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		if !r.released {
			r.released = true
			r.d.record("Release", func(s *State) { s.Released++ })
		}
		return 0, err
	}
	if r.fail && r.n >= r.d.FailAfter*block {
		return 0, ErrUSB
	}
	if left := block - r.n%block; len(p) > left {
		p = p[:left]
	}
	r.d.mu.Lock()
	for i := range p {
		p[i] = byte(r.d.k)
		r.d.k++
	}
	r.d.mu.Unlock()
	r.n += len(p)
	if r.n%block == 0 && r.d.Pace > 0 {
		select {
		case <-time.After(r.d.Pace):
		case <-r.ctx.Done():
		}
	}
	return len(p), nil
}
