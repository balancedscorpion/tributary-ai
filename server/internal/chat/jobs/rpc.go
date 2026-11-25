package jobs

import (
	"context"
	"time"
)

// remoteStartLocked returns the durable provider-dispatch timestamp. Legacy
// records created before RemoteSubmittedAt use SubmittedAt conservatively.
// Caller holds job.mu for read or write.
func remoteStartLocked(job *Job) time.Time {
	if job != nil && job.RemoteSubmittedAt != nil && !job.RemoteSubmittedAt.IsZero() {
		return *job.RemoteSubmittedAt
	}
	if job != nil {
		return job.SubmittedAt
	}
	return time.Time{}
}

func remoteStart(job *Job) time.Time {
	if job == nil {
		return time.Time{}
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	return remoteStartLocked(job)
}

// hardRemaining reports how much provider execution budget remains. The
// boolean is false when no hard execution budget is configured.
func (r *Registry) hardRemaining(job *Job, now time.Time) (time.Duration, bool) {
	if job == nil {
		return 0, false
	}
	job.mu.RLock()
	hard := job.Budgets.HardExecution
	start := remoteStartLocked(job)
	job.mu.RUnlock()
	if hard <= 0 || start.IsZero() {
		return 0, false
	}
	return hard - now.Sub(start), true
}

// providerCallContext bounds every ordinary provider RPC. When capHard is
// true, the call cannot run beyond the job's remaining hard execution
// budget. Cancellation and confirmation calls deliberately pass false: they
// must still be allowed to stop/reconcile work after the budget is reached.
func (r *Registry) providerCallContext(parent context.Context, job *Job, capHard bool) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	timeout := r.sched.ProviderCallTimeout
	if timeout <= 0 {
		timeout = defaultProviderCallTimeout
	}
	if capHard {
		if remaining, ok := r.hardRemaining(job, r.nowFn()); ok {
			if remaining <= 0 {
				ctx, cancel := context.WithCancel(parent)
				cancel()
				return ctx, func() {}
			}
			if remaining < timeout {
				timeout = remaining
			}
		}
	}
	return context.WithTimeout(parent, timeout)
}

func handlesEqual(a, b NativeHandle) bool {
	if a.Provider != b.Provider || a.ID != b.ID || a.Location != b.Location || len(a.Extra) != len(b.Extra) {
		return false
	}
	for key, value := range a.Extra {
		if b.Extra[key] != value {
			return false
		}
	}
	return true
}

// contextWithEither is cancelled when either input is done. The returned
// cancel also detaches the AfterFunc so a completed call retains no watcher.
func contextWithEither(primary, other context.Context) (context.Context, context.CancelFunc) {
	if primary == nil {
		primary = context.Background()
	}
	ctx, cancel := context.WithCancel(primary)
	if other == nil {
		return ctx, cancel
	}
	stop := context.AfterFunc(other, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

// persistRetryDeadline stores only authoritative provider backpressure (not
// ordinary cadence). This is intentionally durable: a lease takeover or app
// restart must not collapse a Retry-After window into an immediate poll.
func (r *Registry) persistRetryDeadline(job *Job, delay time.Duration, action RetryAction, caps ProviderCapabilities, retryAfter time.Duration) {
	if job == nil || delay <= 0 {
		return
	}
	deadline := r.nowFn().Add(delay)
	job.mu.Lock()
	if action != "" && action != job.NextPollAction {
		job.NextPollAt = &deadline
		job.NextPollAction = action
	} else if job.NextPollAt == nil || deadline.After(*job.NextPollAt) {
		job.NextPollAt = &deadline
		if action != "" {
			job.NextPollAction = action
		}
	}
	if caps.MaxSafePollInterval > 0 && retryAfter > caps.MaxSafePollInterval {
		if job.Metadata == nil {
			job.Metadata = map[string]any{}
		}
		job.Metadata["cursor_at_risk_due_to_backpressure"] = true
		job.Metadata["provider_retry_after_ms"] = retryAfter.Milliseconds()
		job.Metadata["provider_max_safe_poll_interval_ms"] = caps.MaxSafePollInterval.Milliseconds()
		job.appendWakeLocked(r.nowFn(), WakePathology,
			"provider Retry-After exceeds the cursor keepalive interval; honoring backpressure and marking the cursor at risk")
	}
	job.mu.Unlock()
	r.afterTransition(job)
}
