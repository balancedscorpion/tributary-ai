package arxiv

import (
	"context"
	"sync"
	"time"
)

// DefaultMinInterval is the politeness interval required between request
// starts against the arXiv API (arXiv asks for no more than one request every
// three seconds).
const DefaultMinInterval = 3 * time.Second

// Limiter serializes callers and enforces a minimum interval between request
// starts. The clock and sleep functions are injectable for tests.
type Limiter struct {
	mu      sync.Mutex
	min     time.Duration
	now     func() time.Time
	sleep   func(time.Duration)
	last    time.Time
	hasLast bool
}

// NewLimiter returns a Limiter enforcing at least min between request starts.
// A nil now defaults to time.Now and a nil sleep defaults to time.Sleep.
func NewLimiter(min time.Duration, now func() time.Time, sleep func(time.Duration)) *Limiter {
	if now == nil {
		now = time.Now
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	if min < 0 {
		min = 0
	}
	return &Limiter{min: min, now: now, sleep: sleep}
}

// Wait blocks until the caller may start a request. Callers are serialized;
// each successful return reserves a request start slot at least min after the
// previous one. It returns the context error if ctx is cancelled while
// waiting (without consuming a slot).
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if l.hasLast && l.min > 0 {
		wait := l.min - l.now().Sub(l.last)
		if wait > 0 {
			done := make(chan struct{})
			go func() {
				l.sleep(wait)
				close(done)
			}()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	l.last = l.now()
	l.hasLast = true
	return nil
}
