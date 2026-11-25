package jobs

import (
	"math/rand"
	"time"
)

const defaultProviderCallTimeout = 30 * time.Second

// SchedulerConfig controls monitor cadence. Cadence adapts from fast early
// polls toward MaxInterval, with jitter so many jobs never align. Transient
// monitor errors back off independently (cap MaxErrorBackoff) and never
// terminalize execution; MonitorHealth degrades instead.
type SchedulerConfig struct {
	BaseIntervals   []time.Duration // default 300ms, 800ms, 2s, 5s, 10s
	MaxInterval     time.Duration   // default 30s
	JitterFrac      float64         // default 0.2 (±20%)
	ErrorBackoff    []time.Duration // default 5s, 15s, 30s, 60s
	MaxErrorBackoff time.Duration   // default 2m
	DegradedAfter   int             // consecutive errors before MonitorHealthDegraded; default 3
	LostAfter       time.Duration   // continuous failure before MonitorHealthLost; default 60m
	// CancelUnconfirmedAfter bounds how long a cancellation may sit in
	// cancelling without provider confirmation before a durable pathology
	// wake is emitted (the job stays non-terminal and retries; default 2m).
	CancelUnconfirmedAfter time.Duration
	// ProviderCallTimeout bounds each Poll/Fetch/Adopt/Reconcile/Submit RPC.
	// It is additionally capped by the remaining hard execution budget for
	// ordinary execution calls. Default 30s.
	ProviderCallTimeout time.Duration
	// ClaimInterval is the recurring failover scan for durable active jobs
	// that no local goroutine is driving. Default 10s.
	ClaimInterval time.Duration
}

func (c SchedulerConfig) withDefaults() SchedulerConfig {
	if len(c.BaseIntervals) == 0 {
		c.BaseIntervals = []time.Duration{
			300 * time.Millisecond,
			800 * time.Millisecond,
			2 * time.Second,
			5 * time.Second,
			10 * time.Second,
		}
	}
	if c.MaxInterval <= 0 {
		c.MaxInterval = 30 * time.Second
	}
	if c.JitterFrac <= 0 {
		c.JitterFrac = 0.2
	}
	if len(c.ErrorBackoff) == 0 {
		c.ErrorBackoff = []time.Duration{5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	}
	if c.MaxErrorBackoff <= 0 {
		c.MaxErrorBackoff = 2 * time.Minute
	}
	if c.DegradedAfter <= 0 {
		c.DegradedAfter = 3
	}
	if c.LostAfter <= 0 {
		c.LostAfter = 60 * time.Minute
	}
	if c.CancelUnconfirmedAfter <= 0 {
		c.CancelUnconfirmedAfter = 2 * time.Minute
	}
	if c.ProviderCallTimeout <= 0 {
		c.ProviderCallTimeout = defaultProviderCallTimeout
	}
	if c.ClaimInterval <= 0 {
		c.ClaimInterval = 10 * time.Second
	}
	return c
}

// nextInterval returns the poll delay after pollCount successful polls,
// bounded by the provider's protocol keepalive requirement when present
// (e.g. Trino: an unfetched cursor expires server-side, so cadence may
// never exceed half the safe interval).
func (c SchedulerConfig) nextInterval(pollCount int, caps ProviderCapabilities, retryAfter ...time.Duration) time.Duration {
	var base time.Duration
	if pollCount < len(c.BaseIntervals) {
		base = c.BaseIntervals[pollCount]
	} else {
		base = c.MaxInterval
	}
	if base > c.MaxInterval {
		base = c.MaxInterval
	}
	return pacedInterval(base, caps, c.JitterFrac, firstDuration(retryAfter))
}

// errorInterval returns the delay after consecutiveErrs failed polls.
// Keepalive-bound providers cap ordinary exponential backoff, but an
// authoritative Retry-After remains a minimum: hammering a throttled
// endpoint is worse than the resulting (explicitly diagnosed) cursor risk.
func (c SchedulerConfig) errorInterval(consecutiveErrs int, caps ProviderCapabilities, retryAfter ...time.Duration) time.Duration {
	if consecutiveErrs <= 0 {
		consecutiveErrs = 1
	}
	var base time.Duration
	if consecutiveErrs-1 < len(c.ErrorBackoff) {
		base = c.ErrorBackoff[consecutiveErrs-1]
	} else {
		base = c.MaxErrorBackoff
	}
	if base > c.MaxErrorBackoff {
		base = c.MaxErrorBackoff
	}
	return pacedInterval(base, caps, c.JitterFrac, firstDuration(retryAfter))
}

func firstDuration(values []time.Duration) time.Duration {
	if len(values) == 0 || values[0] <= 0 {
		return 0
	}
	return values[0]
}

// pacedInterval applies exactly one combined pacing decision. Provider
// Retry-After is a minimum. Continuous-drain cursor safety caps ordinary
// cadence, but cannot override explicit provider backpressure.
func pacedInterval(base time.Duration, caps ProviderCapabilities, jitterFrac float64, retryAfter time.Duration) time.Duration {
	if retryAfter > base {
		base = retryAfter
	}
	if retryAfter > 0 {
		// Authoritative server backpressure outranks cursor keepalive advice.
		// Polling inside an explicit 429 window worsens throttling; callers emit
		// a cursor-risk diagnostic when this exceeds MaxSafePollInterval.
		result := jitter(base, jitterFrac)
		if result < retryAfter {
			result = retryAfter
		}
		return result
	}
	bound := time.Duration(0)
	if caps.MaxSafePollInterval > 0 {
		bound = caps.MaxSafePollInterval / 2
		if bound < time.Second {
			bound = caps.MaxSafePollInterval
		}
		if base > bound {
			base = bound
		}
	}
	result := jitter(base, jitterFrac)
	if bound > 0 && result > bound {
		result = bound
	}
	return result
}

func jitter(d time.Duration, frac float64) time.Duration {
	if d <= 0 || frac <= 0 {
		return d
	}
	delta := float64(d) * frac
	// Uniform in [d-delta, d+delta].
	j := time.Duration(float64(d) - delta + rand.Float64()*2*delta)
	if j <= 0 {
		return d
	}
	return j
}
