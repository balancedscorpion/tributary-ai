package arxiv

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestLimiterEnforcesSpacing(t *testing.T) {
	clock := newFakeClock()
	var slept []time.Duration
	sleep := func(d time.Duration) {
		slept = append(slept, d)
		clock.advance(d)
	}
	lim := NewLimiter(3*time.Second, clock.now, sleep)
	ctx := context.Background()

	if err := lim.Wait(ctx); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	if len(slept) != 0 {
		t.Fatalf("first Wait slept %v, want no sleep", slept)
	}

	if err := lim.Wait(ctx); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if len(slept) != 1 || slept[0] != 3*time.Second {
		t.Fatalf("second Wait slept %v, want [3s]", slept)
	}

	clock.advance(1 * time.Second)
	if err := lim.Wait(ctx); err != nil {
		t.Fatalf("third Wait: %v", err)
	}
	if len(slept) != 2 || slept[1] != 2*time.Second {
		t.Fatalf("third Wait slept %v, want second sleep of 2s", slept)
	}

	clock.advance(5 * time.Second)
	if err := lim.Wait(ctx); err != nil {
		t.Fatalf("fourth Wait: %v", err)
	}
	if len(slept) != 2 {
		t.Fatalf("fourth Wait slept %v, want no additional sleep after 5s gap", slept)
	}
}

func TestLimiterWaitCancellation(t *testing.T) {
	clock := newFakeClock()
	block := make(chan struct{})
	sleep := func(time.Duration) { <-block }
	defer close(block)
	lim := NewLimiter(3*time.Second, clock.now, sleep)

	if err := lim.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- lim.Wait(ctx) }()
	cancel()
	select {
	case err := <-errCh:
		if err != context.Canceled {
			t.Fatalf("cancelled Wait = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("cancelled Wait did not return")
	}
}

func TestLimiterCancelledWaitDoesNotConsumeSlot(t *testing.T) {
	clock := newFakeClock()
	var slept []time.Duration
	sleep := func(d time.Duration) {
		slept = append(slept, d)
		clock.advance(d)
	}
	lim := NewLimiter(3*time.Second, clock.now, sleep)

	if err := lim.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lim.Wait(cancelled); err != context.Canceled {
		t.Fatalf("Wait with cancelled ctx = %v, want context.Canceled", err)
	}

	clock.advance(3 * time.Second)
	if err := lim.Wait(context.Background()); err != nil {
		t.Fatalf("third Wait: %v", err)
	}
	if len(slept) != 0 {
		t.Fatalf("slept %v, want none: cancelled Wait must not move the last-start stamp", slept)
	}
}
