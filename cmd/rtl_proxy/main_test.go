package main

import "testing"

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
