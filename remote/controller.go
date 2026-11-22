package remote

import "time"

// controller picks the bits per component from queue occupancy: down
// one bit at once when frames pile up, up one bit only after the queue
// stayed empty for hold. Reacting fast keeps latency low.
type controller struct {
	min, max, bits int
	hold           time.Duration
	emptySince     time.Time
}

func newController(minBits, maxBits int, hold time.Duration) *controller {
	return &controller{
		min:  minBits,
		max:  maxBits,
		bits: maxBits,
		hold: hold,
	}
}

// update takes the number of frames still waiting after one was taken.
func (c *controller) update(waiting int, now time.Time) int {
	switch {
	case waiting > 1:
		c.bits = max(c.bits-1, c.min)
		c.emptySince = time.Time{}
	case waiting == 1:
		c.emptySince = time.Time{}
	case c.emptySince.IsZero():
		c.emptySince = now
	case now.Sub(c.emptySince) >= c.hold && c.bits < c.max:
		c.bits++
		c.emptySince = now
	}
	return c.bits
}
