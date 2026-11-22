// Package remote implements the rtlproto.v1 Receiver service on top of
// an RTL-SDR device.
package remote

import (
	"context"
	"io"

	"github.com/dim13/r82xx/tuner"
)

// FrameBytes is the raw size of one frame: 8192 complex u8 samples.
const FrameBytes = 16384

// Device is what the Receiver service needs from the hardware.
// *r82xx.Device provides all but Strings and Samples.
type Device interface {
	SetFrequency(hz float64) error
	SetSampleRate(hz uint32) (achievedHz float64, err error)
	SetBandwidth(hz, sampleRateHz float64) error
	SetPPM(ppm float64) error
	SetGainAuto() error
	SetGainManual() error
	SetGain(tenthsDB int) error
	SetAGC(on bool) error
	SetBiasTee(on bool) error
	Chip() tuner.Type
	BlogV4() bool
	Strings() (manufacturer, product string, err error)
	// Samples flushes the device FIFO and returns a continuous,
	// ordered u8 I/Q stream. The stream ends and releases the device
	// when ctx ends. Only one goroutine may read it.
	Samples(ctx context.Context) (io.Reader, error)
}
