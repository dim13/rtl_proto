package remote

import (
	"context"
	"testing"
	"time"

	"github.com/dim13/rtl_proto/remote/fake"
)

func getFrame(t *testing.T, q *queue) frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f, err := q.get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestHubFanOut(t *testing.T) {
	h := newHub(&fake.Device{Pace: time.Millisecond})
	a, unsubA := h.subscribe(16)
	defer unsubA()
	b, unsubB := h.subscribe(16)
	defer unsubB()
	// b may have joined a frame later; align on its first frame.
	fb := getFrame(t, b)
	for {
		if fa := getFrame(t, a); fa.first == fb.first {
			break
		}
	}
	for range 5 {
		fa, fb := getFrame(t, a), getFrame(t, b)
		if fa.first != fb.first {
			t.Fatalf("a %d, b %d", fa.first, fb.first)
		}
	}
}

// A consumer that never reads must not stall the others.
func TestHubSlowConsumer(t *testing.T) {
	h := newHub(&fake.Device{Pace: time.Millisecond})
	a, unsubA := h.subscribe(4)
	defer unsubA()
	b, unsubB := h.subscribe(4)
	defer unsubB()
	deadline := time.Now().Add(time.Second)
	for range 50 {
		getFrame(t, b)
	}
	if time.Now().After(deadline) {
		t.Error("50 frames took over 1s")
	}
	if a.len() > 4 {
		t.Errorf("slow queue holds %d frames, want ≤ 4", a.len())
	}
}

func TestHubStopsReader(t *testing.T) {
	dev := &fake.Device{Pace: time.Millisecond}
	h := newHub(dev)
	q, unsub := h.subscribe(4)
	getFrame(t, q)
	unsub()
	if got := dev.Snapshot().Released; got != 1 {
		t.Fatalf("released after last unsubscribe: got %d, want 1", got)
	}
	q, unsub = h.subscribe(4)
	getFrame(t, q)
	unsub()
	if got := dev.Snapshot().Released; got != 2 {
		t.Errorf("released after second round: got %d, want 2", got)
	}
}

func TestHubReaderError(t *testing.T) {
	h := newHub(&fake.Device{FailAfter: 3, Pace: time.Millisecond})
	q, unsub := h.subscribe(16)
	defer unsub()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for range 4 {
		if _, err := q.get(ctx); err != nil {
			if ctx.Err() != nil {
				t.Fatal("timed out instead of reader error")
			}
			return
		}
	}
	t.Error("no error after 4 gets")
}
