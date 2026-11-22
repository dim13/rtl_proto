package main

import "testing"

func TestNearestGain(t *testing.T) {
	for _, tc := range []struct {
		db   float64
		want int32
	}{
		{db: 29.7, want: 297},
		{db: 30, want: 297},
		{db: 32, want: 328},
		{db: 100, want: 496},
		{db: -5, want: 0},
	} {
		if got := nearestGain(tc.db); got != tc.want {
			t.Errorf("%v dB: got %d, want %d", tc.db, got, tc.want)
		}
	}
}
