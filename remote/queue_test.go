package remote

import (
	"context"
	"errors"
	"testing"
)

func TestQueueDropsOldest(t *testing.T) {
	q := newQueue(4)
	for i := range 6 {
		dropped := q.put(frame{first: uint64(i)})
		if want := i >= 4; dropped != want {
			t.Errorf("put %d: dropped %v, want %v", i, dropped, want)
		}
	}
	if got, want := q.len(), 4; got != want {
		t.Errorf("len: got %d, want %d", got, want)
	}
	for _, want := range []uint64{2, 3, 4, 5} {
		f, err := q.get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if f.first != want {
			t.Errorf("got first %d, want %d", f.first, want)
		}
	}
	if got := q.len(); got != 0 {
		t.Errorf("len after drain: got %d, want 0", got)
	}
}

func TestQueueGetCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newQueue(4).get(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}
