package remote

import (
	"context"
	"io"
	"sync"
	"time"
)

// hub reads the device once and fans raw frames out to every
// subscriber's queue. The reader runs while there are subscribers.
type hub struct {
	dev Device

	mu     sync.Mutex
	subs   map[*queue]struct{}
	cancel context.CancelFunc
	done   chan struct{} // closed when the current reader has exited
}

func newHub(dev Device) *hub {
	return &hub{
		dev:  dev,
		subs: make(map[*queue]struct{}),
	}
}

// subscribe returns a queue of depth n that receives every frame from
// now on, and the func to leave. The first subscriber starts the reader.
func (h *hub) subscribe(n int) (*queue, func()) {
	q := newQueue(n)
	h.mu.Lock()
	// A previous reader may still be shutting down: two readers must
	// never share the device.
	for len(h.subs) == 0 && h.done != nil {
		done := h.done
		h.mu.Unlock()
		<-done
		h.mu.Lock()
		if h.done == done {
			h.done = nil
		}
	}
	if len(h.subs) == 0 {
		ctx, cancel := context.WithCancel(context.Background())
		h.cancel, h.done = cancel, make(chan struct{})
		go h.run(ctx, h.done)
	}
	h.subs[q] = struct{}{}
	h.mu.Unlock()
	return q, func() {
		h.unsubscribe(q)
	}
}

// unsubscribe removes q; the last one stops the reader and waits for it.
func (h *hub) unsubscribe(q *queue) {
	h.mu.Lock()
	delete(h.subs, q)
	if len(h.subs) > 0 || h.done == nil {
		h.mu.Unlock()
		return
	}
	h.cancel()
	done := h.done
	h.mu.Unlock()
	<-done
}

func (h *hub) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	r, err := h.dev.Samples(ctx)
	if err == nil {
		err = h.pump(r)
	}
	if ctx.Err() != nil {
		return // stopped by the last unsubscribe
	}
	// The device failed: every current subscriber learns it, and the
	// next subscriber starts a fresh reader.
	h.mu.Lock()
	for q := range h.subs {
		q.fail(err)
	}
	clear(h.subs)
	h.mu.Unlock()
}

func (h *hub) pump(r io.Reader) error {
	var first uint64
	for {
		raw := make([]byte, FrameBytes)
		if _, err := io.ReadFull(r, raw); err != nil {
			return err
		}
		f := frame{raw: raw, first: first, captured: time.Now()}
		h.mu.Lock()
		for q := range h.subs {
			q.put(f)
		}
		h.mu.Unlock()
		first += FrameBytes / 2
	}
}
