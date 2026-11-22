package main

import (
	"io"

	"github.com/dim13/r82xx"
)

// device adds what remote.Device needs beyond *r82xx.Device.
type device struct {
	*r82xx.Device
	u *usb
}

func (d device) Strings() (string, string, error) {
	return d.u.Strings()
}

func (d device) Samples() (io.ReadCloser, error) {
	if err := d.ResetBuffer(); err != nil {
		return nil, err
	}
	return d.u.stream()
}
