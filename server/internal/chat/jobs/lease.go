package jobs

import (
	"context"
	"time"
)

// claimDriver serializes local drivers and, when supported, acquires the
// cross-process monitor lease before any provider I/O. A same-holder lease
// acquisition is not enough by itself: without the local driving guard two
// goroutines in one process could both poll a forward-only cursor.
func (r *Registry) claimDriver(job *Job) (LeaseStore, bool) {
	if job == nil {
		return nil, false
	}
	job.mu.Lock()
	if job.driving || isTerminalStatus(job.Status) {
		job.mu.Unlock()
		return nil, false
	}
	if job.pollCtx == nil || job.pollCtx.Err() != nil {
		job.pollCtx, job.pollStop = context.WithCancel(context.Background())
	}
	job.driving = true
	job.mu.Unlock()

	ls, hasLease := r.store.(LeaseStore)
	if !hasLease {
		if !r.refreshClaimedJob(job) {
			job.mu.Lock()
			job.driving = false
			job.mu.Unlock()
			return nil, false
		}
		return nil, true
	}
	if !r.acquireDriverLease(job, ls, "acquire") {
		job.mu.Lock()
		job.driving = false
		job.mu.Unlock()
		return nil, false
	}
	if !r.refreshClaimedJob(job) {
		r.releaseDriver(job, ls)
		return nil, false
	}
	// The refresh is deliberately after the first acquisition, but it can take
	// long enough for a short lease to expire. Confirm once more before the
	// caller is allowed to perform provider I/O.
	if !r.acquireDriverLease(job, ls, "post-refresh confirmation") {
		r.releaseDriver(job, ls)
		return nil, false
	}
	return ls, true
}

func (r *Registry) confirmDriverLease(job *Job, ls LeaseStore) bool {
	if ls == nil {
		return true
	}
	if !r.acquireDriverLease(job, ls, "handoff") {
		return false
	}
	if !r.refreshClaimedJob(job) {
		return false
	}
	return r.acquireDriverLease(job, ls, "post-refresh handoff confirmation")
}

func (r *Registry) acquireDriverLease(job *Job, ls LeaseStore, phase string) bool {
	if job == nil || ls == nil {
		return ls == nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	acquired, err := ls.AcquireMonitorLease(ctx, job.ID, r.instanceID, r.leaseTTL)
	cancel()
	if err != nil || !acquired {
		r.logfSafe("jobs: driver lease %s for %s failed (acquired=%v err=%v)", phase, job.ID, acquired, err)
		return false
	}
	return true
}

// refreshClaimedJob closes the list/read -> lease-acquire race. A takeover
// must drive the record current AFTER acquiring the lease, otherwise a Trino
// cursor advanced by the prior holder can be polled from a stale nextUri.
func (r *Registry) refreshClaimedJob(job *Job) bool {
	if r.store == nil || job == nil {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	rec, found, err := r.store.GetQueryJob(ctx, job.ID)
	cancel()
	if err != nil || !found {
		r.logfSafe("jobs: refresh after lease claim for %s failed (found=%v err=%v)", job.ID, found, err)
		return false
	}
	job.mu.Lock()
	if rec.Revision > job.storeRevision {
		applyRecordLocked(job, rec, r.nowFn())
	}
	terminal := isTerminalStatus(job.Status)
	job.mu.Unlock()
	return !terminal
}

func (r *Registry) releaseDriver(job *Job, ls LeaseStore) {
	if job == nil {
		return
	}
	// Keep the local guard set until the same-holder lease release completes.
	// Otherwise a recurring claimant can reacquire under our instance id and
	// have this old release delete the new driver's lease.
	if ls != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := ls.ReleaseMonitorLease(ctx, job.ID, r.instanceID); err != nil {
			r.logfSafe("jobs: release monitor lease for %s failed: %v", job.ID, err)
		}
		cancel()
	}
	job.mu.Lock()
	job.driving = false
	terminal := isTerminalStatus(job.Status)
	stop := job.pollStop
	job.mu.Unlock()
	if terminal && stop != nil {
		stop()
	}
}

// releaseHeldLease is used by launch paths that terminalize before a monitor
// goroutine is created (for example an inline terminal submit response after
// accepted profiling). It is safe when no lease exists or was never acquired.
func (r *Registry) releaseHeldLease(job *Job) {
	ls, ok := r.store.(LeaseStore)
	if !ok || job == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	if err := ls.ReleaseMonitorLease(ctx, job.ID, r.instanceID); err != nil {
		r.logfSafe("jobs: release launch lease for %s failed: %v", job.ID, err)
	}
	cancel()
}

// leaseHeartbeat creates the provider-driver context for an already acquired
// lease. A definitive Renew=false stops immediately. A transient store error
// is retried while the last confirmed lease can still be valid; the driver is
// cancelled no later than local expiry. Thus a brief store outage does not
// abandon a healthy query, while stale holders never keep issuing I/O.
func (r *Registry) leaseHeartbeat(parent context.Context, ls LeaseStore, jobID string) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	if ls == nil {
		return parent, func() {}
	}
	leaseCtx, leaseCancel := context.WithCancel(parent)
	stopped := make(chan struct{})
	interval := r.leaseTTL / 3
	if interval < 5*time.Millisecond {
		interval = 5 * time.Millisecond
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		lastConfirmed := time.Now()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
			}
			remaining := lastConfirmed.Add(r.leaseTTL).Sub(time.Now())
			if remaining <= 0 {
				r.logfSafe("jobs: monitor lease for %s reached local expiry before renewal", jobID)
				leaseCancel()
				return
			}
			renewTimeout := 2 * time.Second
			if remaining < renewTimeout {
				renewTimeout = remaining
			}
			rctx, rcancel := context.WithTimeout(leaseCtx, renewTimeout)
			renewed, err := ls.RenewMonitorLease(rctx, jobID, r.instanceID, r.leaseTTL)
			rcancel()
			switch {
			case err == nil && renewed:
				lastConfirmed = time.Now()
			case err == nil && !renewed:
				r.logfSafe("jobs: monitor lease for %s was taken or expired; stopping local driver", jobID)
				leaseCancel()
				return
			case time.Since(lastConfirmed) >= r.leaseTTL:
				r.logfSafe("jobs: monitor lease for %s could not be renewed before expiry: %v", jobID, err)
				leaseCancel()
				return
			default:
				r.logfSafe("jobs: transient monitor lease renewal error for %s (retrying before expiry): %v", jobID, err)
			}
		}
	}()
	return leaseCtx, func() {
		leaseCancel()
		<-stopped
	}
}
