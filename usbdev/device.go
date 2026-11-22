// Package usbdev opens an RTL-SDR over USB as a remote.Device.
package usbdev

import (
	"context"
	"io"

	"github.com/dim13/r82xx"
)

// Device is an opened dongle: *r82xx.Device plus what remote.Device
// needs on top.
type Device struct {
	*r82xx.Device
	u *usb
}

// Open opens the first RTL-SDR dongle.
func Open() (*Device, error) {
	u, err := openUSB()
	if err != nil {
		return nil, err
	}
	dev, err := r82xx.Open(u)
	if err != nil {
		u.Close()
		return nil, err
	}
	return &Device{Device: dev, u: u}, nil
}

// Close powers the tuner down and releases USB.
func (d *Device) Close() error {
	err := d.Device.Close()
	if uerr := d.u.Close(); err == nil {
		err = uerr
	}
	return err
}

func (d *Device) Strings() (string, string, error) {
	return d.u.Strings()
}

func (d *Device) Samples(ctx context.Context) (io.Reader, error) {
	if err := d.ResetBuffer(); err != nil {
		return nil, err
	}
	return d.u.stream(ctx)
}
