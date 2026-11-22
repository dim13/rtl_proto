package remote

import (
	"context"
	"time"
)

// frame is one FrameBytes chunk of raw u8 I/Q.
type frame struct {
	raw      []byte
	first    uint64 // complex-sample index from stream start
	captured time.Time
}

// queue is a bounded FIFO that drops the oldest frame when full, so
// what gets sent is always the freshest.
type queue struct {
	ch chan frame
}

func newQueue(n int) *queue {
	return &queue{ch: make(chan frame, n)}
}

// put is called by a single producer.
func (q *queue) put(f frame) (dropped bool) {
	for {
		select {
		case q.ch <- f:
			return dropped
		default:
		}
		select {
		case <-q.ch:
			dropped = true
		default:
		}
	}
}

func (q *queue) get(ctx context.Context) (frame, error) {
	select {
	case f := <-q.ch:
		return f, nil
	case <-ctx.Done():
		return frame{}, ctx.Err()
	}
}

func (q *queue) len() int {
	return len(q.ch)
}
