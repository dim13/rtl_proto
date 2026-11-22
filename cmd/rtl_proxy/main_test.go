package main

import (
	"net"
	"testing"
)

func TestRemoteAddr(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{in: "nuc", want: "nuc:7373"},
		{in: "nuc:1234", want: "nuc:1234"},
		{in: "10.0.0.5", want: "10.0.0.5:7373"},
		{in: "::1", want: "[::1]:7373"},
		{in: "[::1]:9000", want: "[::1]:9000"},
	} {
		if got := remoteAddr(tc.in); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// "localhost" may resolve to ::1 first; clients must reach the proxy on
// either loopback address.
func TestListenLocalhost(t *testing.T) {
	ls, err := listen("localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	_, port, _ := net.SplitHostPort(ls[0].Addr().String())
	for _, host := range []string{"127.0.0.1", "::1"} {
		c, err := net.Dial("tcp", net.JoinHostPort(host, port))
		if err != nil {
			t.Errorf("%s: %v", host, err)
			continue
		}
		c.Close()
	}
}
