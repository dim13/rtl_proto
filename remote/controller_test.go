package remote

import (
	"testing"
	"time"
)

func TestController(t *testing.T) {
	type step struct {
		waiting int
		at      time.Duration
		want    int
	}
	for _, tc := range []struct {
		name     string
		min, max int
		start    []step // drive to a starting point, not checked
		steps    []step
	}{
		{
			name: "step down",
			min:  2, max: 8,
			steps: []step{{2, 0, 7}, {3, 0, 6}, {2, 0, 5}, {2, 0, 4}, {2, 0, 3}, {2, 0, 2}, {5, 0, 2}},
		},
		{
			name: "step up after hold",
			min:  2, max: 8,
			start: []step{{2, 0, 0}, {2, 0, 0}, {2, 0, 0}, {2, 0, 0}, {2, 0, 0}, {2, 0, 0}},
			steps: []step{
				{0, 0, 2},
				{0, 1900 * time.Millisecond, 2},
				{0, 2000 * time.Millisecond, 3},
				{0, 3900 * time.Millisecond, 3},
				{0, 4000 * time.Millisecond, 4},
			},
		},
		{
			name: "one waiting resets hold",
			min:  2, max: 8,
			start: []step{{2, 0, 0}, {2, 0, 0}, {2, 0, 0}, {2, 0, 0}, {2, 0, 0}, {2, 0, 0}},
			steps: []step{
				{0, 0, 2},
				{1, time.Second, 2},
				{0, 2500 * time.Millisecond, 2},
				{0, 4400 * time.Millisecond, 2},
				{0, 4500 * time.Millisecond, 3},
			},
		},
		{
			name: "ceiling",
			min:  2, max: 8,
			steps: []step{{0, 0, 8}, {0, 10 * time.Second, 8}, {0, 20 * time.Second, 8}},
		},
		{
			name: "lossless",
			min:  8, max: 8,
			steps: []step{{4, 0, 8}, {4, 0, 8}, {0, 10 * time.Second, 8}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t0 := time.Unix(1000, 0)
			c := newController(tc.min, tc.max, 2*time.Second)
			for _, s := range tc.start {
				c.update(s.waiting, t0.Add(-time.Hour))
			}
			for i, s := range tc.steps {
				if got := c.update(s.waiting, t0.Add(s.at)); got != s.want {
					t.Errorf("step %d (waiting %d at %v): got %d, want %d", i, s.waiting, s.at, got, s.want)
				}
			}
		})
	}
}
