package jobs

import (
	"context"
	"errors"
	"strings"
	"time"
)

// --- change notification (generation channel broadcast) ---

// notifyAll wakes every waiter observing registry state. Cheap enough to
// call on every transition.
func (r *Registry) notifyAll() {
	r.notifyMu.Lock()
	ch := r.notifyCh
	r.notifyCh = make(chan struct{})
	r.notifyMu.Unlock()
	close(ch)
}

// waitChan returns a channel closed on the next state change.
func (r *Registry) waitChan() <-chan struct{} {
	r.notifyMu.Lock()
	defer r.notifyMu.Unlock()
	return r.notifyCh
}

// afterTransition is called after any observable job mutation. Ordinary
// progress persists best-effort; a managed terminal verdict is stronger: it
// gets bounded synchronous retries and, if the store remains unavailable, a
// lease/CAS-aware repair loop. Capacity is released only after durability.
func (r *Registry) afterTransition(job *Job) {
	if r == nil || job == nil {
		return
	}
	job.mu.RLock()
	managed := job.managed
	terminal := isTerminalStatus(job.Status)
	job.mu.RUnlock()

	persisted := false
	if managed && terminal {
		persisted = r.persistTerminalWithRetry(job)
		job.mu.Lock()
		job.terminalPersistPending = !persisted && isTerminalStatus(job.Status) && !job.terminalPersistAbandoned
		pending := job.terminalPersistPending
		job.mu.Unlock()
		if !persisted && !pending {
			// A newer authoritative record won the CAS (or a driver takeover
			// changed native identity). There is no local verdict to repair.
			terminal = false
		}
	} else {
		persisted = r.persistJob(job)
	}
	r.notifyAll()
	// A terminal transition must be durable before it can free the
	// cross-process admission slot. In particular, a stale monitor that
	// loses a lifecycle CAS must not release capacity for work whose
	// authoritative record is still active.
	if managed && terminal && persisted {
		r.wakeEligibleWaiters()
		r.releaseStoreCapacity(job)
	} else if managed && terminal {
		r.scheduleTerminalPersistRepair(job)
	}
}

const terminalPersistAttempts = 4

// persistTerminalWithRetry absorbs short store outages while the current
// monitor lease/heartbeat is still live. The total attempt count is bounded;
// persistent outages hand off to scheduleTerminalPersistRepair instead of
// blocking the provider driver forever.
func (r *Registry) persistTerminalWithRetry(job *Job) bool {
	for attempt := 0; attempt < terminalPersistAttempts; attempt++ {
		if r.persistTerminalJob(job) {
			return true
		}
		if attempt+1 == terminalPersistAttempts {
			break
		}
		delay := time.Duration(1<<attempt) * 25 * time.Millisecond
		timer := time.NewTimer(delay)
		<-timer.C
	}
	r.logfSafe("jobs: terminal persistence for %s deferred to repair loop", job.ID)
	return false
}

// persistTerminalJob converges one already-observed terminal verdict. On a
// CAS conflict it rebases only terminal-owned fields onto the fresh active
// record, preserving validation/artifact/profile sidecars and wake claims.
// A fresh terminal record is authoritative and counts as success (including
// ambiguous writes where the store committed but the response was lost).
func (r *Registry) persistTerminalJob(job *Job) bool {
	if r.store == nil {
		return true
	}
	job.persistMu.Lock()
	defer job.persistMu.Unlock()

	job.mu.RLock()
	terminal := recordFromJobLocked(job)
	job.mu.RUnlock()
	if terminal.ID == "" || !isTerminalStatus(terminal.Status) {
		return false
	}

	loadFresh := func() (JobRecord, bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		fresh, found, err := r.store.GetQueryJob(ctx, terminal.ID)
		cancel()
		if err != nil || !found {
			return JobRecord{}, false
		}
		return fresh, true
	}
	adoptTerminal := func(fresh JobRecord, authoritativeOther bool) bool {
		if !isTerminalStatus(fresh.Status) {
			return false
		}
		job.mu.Lock()
		applyRecordLocked(job, fresh, r.nowFn())
		job.CompletedAt = cloneTimePtr(fresh.CompletedAt)
		job.terminalPersistPending = false
		if authoritativeOther {
			// The process that committed the authoritative terminal record owns
			// its capacity-release side effect. Preserve the existing exactly-
			// once behavior for stale local copies.
			job.capacityReleased = true
		}
		stop := job.pollStop
		job.mu.Unlock()
		if authoritativeOther && stop != nil {
			stop()
		}
		return true
	}

	cas, hasCAS := r.store.(RevisionedStore)
	if !hasCAS {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := r.store.UpsertQueryJob(ctx, terminal)
		cancel()
		if err == nil {
			job.mu.Lock()
			job.terminalPersistPending = false
			job.mu.Unlock()
			return true
		}
		if fresh, ok := loadFresh(); ok && adoptTerminal(fresh, false) {
			return true
		}
		r.logfSafe("jobs: terminal persist %s failed: %v", terminal.ID, err)
		return false
	}

	candidate := terminal
	expected := terminal.Revision
	for attempt := 0; attempt < 2; attempt++ {
		candidate.Revision = expected
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		newRev, err := cas.UpsertQueryJobCAS(ctx, candidate, expected)
		cancel()
		if err == nil {
			candidate.Revision = newRev
			job.mu.Lock()
			applyRecordLocked(job, candidate, r.nowFn())
			job.CompletedAt = cloneTimePtr(candidate.CompletedAt)
			job.terminalPersistPending = false
			job.mu.Unlock()
			return true
		}
		fresh, ok := loadFresh()
		if ok && adoptTerminal(fresh, errors.Is(err, ErrRevisionConflict)) {
			return true
		}
		if !ok || !errors.Is(err, ErrRevisionConflict) {
			r.logfSafe("jobs: terminal persist %s failed: %v", terminal.ID, err)
			return false
		}
		merged, mergeOK := terminalRecordOnFresh(fresh, terminal)
		if !mergeOK {
			job.mu.Lock()
			if job.driving {
				job.terminalPersistAbandoned = true
				stop := job.pollStop
				job.mu.Unlock()
				if stop != nil {
					stop()
				}
			} else {
				applyRecordLocked(job, fresh, r.nowFn())
				job.CompletedAt = cloneTimePtr(fresh.CompletedAt)
				job.mu.Unlock()
			}
			r.logfSafe("jobs: terminal persist %s refused unsafe rebase from durable status %s", terminal.ID, fresh.Status)
			return false
		}
		candidate = merged
		expected = fresh.Revision
	}
	return false
}

// terminalRecordOnFresh returns a narrow terminal rebase. A different native
// job identity, a profiling record, an illegal lifecycle edge, or divergent
// drain sequence is never guessed through.
func terminalRecordOnFresh(fresh, terminal JobRecord) (JobRecord, bool) {
	if fresh.ID != terminal.ID || !isTerminalStatus(terminal.Status) ||
		fresh.Status == JobStatusProfiling || !canTransition(fresh.Status, terminal.Status) {
		return JobRecord{}, false
	}
	if !fresh.Handle.IsZero() && !terminal.Handle.IsZero() && fresh.Handle.ID != terminal.Handle.ID {
		return JobRecord{}, false
	}
	if fresh.DrainSeq != terminal.DrainSeq {
		// Drain sequence advances only with its result chunk. A whole-record
		// terminal repair must never manufacture or discard such an advance.
		return JobRecord{}, false
	}
	merged := cloneRecord(fresh)
	mergeTerminalLifecycleRecord(&merged, terminal)
	if !terminal.Handle.IsZero() {
		merged.Handle = cloneHandle(terminal.Handle)
	}
	merged.DrainRowsCommitted = terminal.DrainRowsCommitted
	merged.DrainBytesCommitted = terminal.DrainBytesCommitted
	merged.DrainTruncated = terminal.DrainTruncated
	if merged.Metadata == nil {
		merged.Metadata = map[string]any{}
	}
	for key, value := range terminal.Metadata {
		if _, exists := merged.Metadata[key]; !exists {
			merged.Metadata[key] = value
		}
	}
	merged.Revision = fresh.Revision
	return merged, true
}

func (r *Registry) scheduleTerminalPersistRepair(job *Job) {
	if r.store == nil || job == nil {
		return
	}
	job.mu.Lock()
	if job.terminalRepairing || !job.terminalPersistPending || !isTerminalStatus(job.Status) {
		job.mu.Unlock()
		return
	}
	job.terminalRepairing = true
	job.mu.Unlock()
	go r.repairTerminalPersistence(job)
}

// repairTerminalPersistence retries with capped exponential backoff. It waits
// for the local provider driver to release, then acquires the durable monitor
// lease before rebasing, so it cannot race a takeover that is actively
// observing the provider. No provider endpoint is called from this loop.
func (r *Registry) repairTerminalPersistence(job *Job) {
	delay := 250 * time.Millisecond
	defer func() {
		job.mu.Lock()
		job.terminalRepairing = false
		job.mu.Unlock()
	}()
	for {
		job.mu.RLock()
		pending := job.terminalPersistPending
		terminal := isTerminalStatus(job.Status)
		driving := job.driving
		id := job.ID
		job.mu.RUnlock()
		if !pending || !terminal {
			return
		}
		if !driving {
			var ls LeaseStore
			acquired := true
			if store, ok := r.store.(LeaseStore); ok {
				ls = store
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				var err error
				acquired, err = ls.AcquireMonitorLease(ctx, id, r.instanceID, r.leaseTTL)
				cancel()
				if err != nil {
					acquired = false
					r.logfSafe("jobs: terminal repair lease for %s failed: %v", id, err)
				}
			}
			if acquired {
				persisted := r.persistTerminalJob(job)
				if ls != nil {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					_ = ls.ReleaseMonitorLease(ctx, id, r.instanceID)
					cancel()
				}
				if persisted {
					r.notifyAll()
					r.wakeEligibleWaiters()
					r.releaseStoreCapacity(job)
					return
				}
			}
		}
		timer := time.NewTimer(delay)
		<-timer.C
		if delay < 30*time.Second {
			delay *= 2
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}

// releaseStoreCapacity issues the (idempotent, once-per-job) cross-process
// capacity release after the terminal record persisted.
func (r *Registry) releaseStoreCapacity(job *Job) {
	reserver, ok := r.store.(CapacityReserver)
	if !ok {
		return
	}
	job.mu.Lock()
	eligible := job.reservedViaStore && !job.capacityReleased
	if eligible {
		job.capacityReleased = true
	}
	id := job.ID
	job.mu.Unlock()
	if !eligible {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := reserver.ReleaseQueryCapacity(ctx, id); err != nil {
		r.logfSafe("jobs: capacity release for %s failed: %v", id, err)
	}
}

// persistJob writes through to the durable store, best-effort: failures
// are logged, never block or alter lifecycle. When the store implements
// RevisionedStore the write is a CAS on the job's tracked revision; an
// internal-race conflict is retried once against the refreshed stored
// revision. Reserve-before-submit is the one place persistence is
// mandatory; Launch enforces that inline.
func (r *Registry) persistJob(job *Job) bool {
	if r.store == nil {
		return true
	}
	job.persistMu.Lock()
	defer job.persistMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	cas, hasCAS := r.store.(RevisionedStore)
	for attempt := 0; attempt < 2; attempt++ {
		job.mu.Lock()
		rec := recordFromJobLocked(job)
		expected := job.storeRevision
		job.mu.Unlock()
		if rec.ID == "" {
			return false
		}
		if !hasCAS {
			rec.Revision = expected
			if err := r.store.UpsertQueryJob(ctx, rec); err != nil {
				r.logfSafe("jobs: persist %s failed: %v", rec.ID, err)
				return false
			}
			return true
		}
		rec.Revision = expected
		newRev, err := cas.UpsertQueryJobCAS(ctx, rec, expected)
		if err == nil {
			job.mu.Lock()
			if newRev > job.storeRevision {
				job.storeRevision = newRev
			}
			job.mu.Unlock()
			return true
		}
		if !errors.Is(err, ErrRevisionConflict) {
			r.logfSafe("jobs: persist %s failed: %v", rec.ID, err)
			return false
		}
		// Conflict: refresh the stored revision and retry once. This
		// resolves internal write races; a conflict against another
		// process is prevented up-stack by the monitor lease.
		stored, ok, getErr := r.store.GetQueryJob(ctx, rec.ID)
		if getErr != nil || !ok {
			r.logfSafe("jobs: persist %s revision refresh failed: ok=%v err=%v", rec.ID, ok, getErr)
			return false
		}
		job.mu.Lock()
		// Cancellation is the one safe lifecycle intent to rebase for a
		// local writer: it only moves an executing job toward less remote work.
		// Build it on the complete fresh record so a concurrent handle advance,
		// validation result, or wake claim is preserved. This must run before
		// the driven-holder yield: RequestCancel intentionally mutates a job
		// while its monitor owns the driver lease. Never downgrade an already-
		// cancelling or terminal durable state.
		localCancel := rec.Status == JobStatusCancelRequested || rec.Status == JobStatusCancelling
		if localCancel && (stored.Status == JobStatusSubmitted || stored.Status == JobStatusRunning) {
			merged := cloneRecord(stored)
			merged.Status = JobStatusCancelRequested
			merged.CancelReason = rec.CancelReason
			merged.CancelStartedAt = earliestTimePtr(stored.CancelStartedAt, rec.CancelStartedAt)
			if merged.CancelStartedAt == nil {
				t := r.nowFn()
				merged.CancelStartedAt = &t
			}
			applyRecordLocked(job, merged, r.nowFn())
			job.StartedAt = cloneTimePtr(merged.StartedAt)
			job.CompletedAt = nil
			job.cancelWanted = true
			job.cancellingAt = *merged.CancelStartedAt
			job.mu.Unlock()
			continue
		}

		// A local driver must never retry any other stale whole-record image
		// over a newer durable lifecycle. Yield the driver and let the recurring
		// claimant reacquire, refresh every field, and continue from the
		// authoritative record. Sidecar mutations (wake claims/validation)
		// are therefore preserved, and a stale terminal observation cannot
		// free capacity it did not durably commit.
		if job.driving {
			stop := job.pollStop
			durableTerminal := isTerminalStatus(stored.Status)
			if durableTerminal {
				job.capacityReleased = true
			}
			job.mu.Unlock()
			if stop != nil {
				stop()
			}
			r.logfSafe("jobs: persist %s yielded local driver after revision conflict", rec.ID)
			return durableTerminal
		}
		// A non-driver can be a lazy/status-only copy owned by another
		// process. Retrying its whole-record image at the fresh revision would
		// resurrect terminal work and erase authoritative handles, validation,
		// wake claims, or cancellation state. Adopt the complete durable image
		// instead. Important user intents have dedicated atomic sidecar APIs in
		// capable stores; an untyped whole-record conflict is never permission
		// to guess a merge.
		durableTerminal := isTerminalStatus(stored.Status)
		applyRecordLocked(job, stored, r.nowFn())
		// applyRecordLocked intentionally leaves absent timestamps and some
		// runtime intent untouched for ordinary monotonic refreshes. Conflict
		// adoption is a full replacement, so clear those stale values too.
		job.StartedAt = cloneTimePtr(stored.StartedAt)
		job.CompletedAt = cloneTimePtr(stored.CompletedAt)
		job.cancelWanted = stored.Status == JobStatusCancelRequested || stored.Status == JobStatusCancelling
		job.cancellingAt = time.Time{}
		if job.cancelWanted && stored.CancelStartedAt != nil {
			job.cancellingAt = *stored.CancelStartedAt
		}
		if durableTerminal {
			// Terminal persistence is already the capacity release point for
			// durable stores. Never let this stale local writer issue a second
			// release on the strength of a transition it did not commit.
			job.capacityReleased = true
		}
		stop := job.pollStop
		job.mu.Unlock()
		if durableTerminal && stop != nil {
			stop()
		}
		r.logfSafe("jobs: persist %s adopted newer durable record after revision conflict", rec.ID)
		return durableTerminal
	}
	r.logfSafe("jobs: persist gave up after revision conflicts")
	return false
}

func earliestTimePtr(left, right *time.Time) *time.Time {
	switch {
	case left == nil:
		return cloneTimePtr(right)
	case right == nil:
		return cloneTimePtr(left)
	case left.Before(*right):
		return cloneTimePtr(left)
	default:
		return cloneTimePtr(right)
	}
}

// recordFromJobLocked builds the serializable record. Caller holds job.mu
// (read or write).
func recordFromJobLocked(job *Job) JobRecord {
	rec := JobRecord{
		ID:                   job.ID,
		Provider:             job.Provider,
		ConnectionID:         job.ConnectionID,
		SQL:                  job.SQL,
		SQLPreview:           job.SQLPreview,
		Status:               job.Status,
		MonitorHealth:        job.MonitorHealth,
		PolicyHealth:         job.PolicyHealth,
		MaterializationState: job.Materialization,
		Owner:                job.Owner,
		Handle:               cloneHandle(job.Handle),
		IdempotencyKey:       job.IdempotencyKey,
		ReplacesJobID:        job.ReplacesJobID,
		KeepOnRunCompletion:  job.KeepOnRunCompletion,
		Budgets:              job.Budgets,
		SubmittedAt:          job.SubmittedAt,
		RemoteSubmittedAt:    cloneTimePtr(job.RemoteSubmittedAt),
		NextPollAt:           cloneTimePtr(job.NextPollAt),
		NextPollAction:       job.NextPollAction,
		LastAccess:           job.LastAccess,
		CancelReason:         job.CancelReason,
		CancelOutcome:        job.CancelOutcome,
		CancelStartedAt:      cloneTimePtr(job.CancelStartedAt),
		Error:                job.Error,
		ArtifactID:           job.ArtifactID,
		Metadata:             cloneMap(job.Metadata),
		Revision:             job.storeRevision,
		DrainSeq:             job.DrainSeq,
		DrainRowsCommitted:   job.DrainRowsCommitted,
		DrainBytesCommitted:  job.DrainBytesCommitted,
		DrainTruncated:       job.DrainTruncated,
		SubmitDispatched:     job.SubmitDispatched,
		ValidationState:      job.Validation,
		ValidationDetail:     job.ValidationDetail,
		Profile:              cloneQueryProfile(job.Profile),
		ProfileOutcome:       job.ProfileOutcome,
		ProfileHandle:        cloneHandle(job.ProfileHandle),
		ProfilePhase:         job.ProfilePhase,
		ProfileDispatched:    job.ProfileDispatched,
	}
	if job.StartedAt != nil {
		t := *job.StartedAt
		rec.StartedAt = &t
	}
	if job.CompletedAt != nil {
		t := *job.CompletedAt
		rec.CompletedAt = &t
	}
	if len(job.WakeEvents) > 0 {
		rec.WakeEvents = append([]WakeEvent(nil), job.WakeEvents...)
	}
	return rec
}

// appendWakeLocked appends a wake event once per kind (policy kinds are
// emitted at most once per job; a state that resolves and re-occurs is
// carried by the health fields, not repeated events). Caller holds job.mu
// (write). Returns whether an event was appended.
func (j *Job) appendWakeLocked(at time.Time, kind WakeKind, detail string) bool {
	if j.emittedWakes == nil {
		j.emittedWakes = map[WakeKind]bool{}
	}
	if j.emittedWakes[kind] {
		return false
	}
	j.emittedWakes[kind] = true
	j.WakeEvents = append(j.WakeEvents, WakeEvent{At: at, Kind: kind, Detail: detail})
	return true
}

// --- completion barrier ---

// WaitResult is what a barrier wait observed. Event is nil when the wait
// returned because every requested job is terminal with no unclaimed
// events left. Events carries the full atomically-claimed batch when the
// wait ran with Claim.
type WaitResult struct {
	Job    *Job
	Event  *WakeEvent
	Events []WakeEvent
}

// WaitForJobs is the single-owner convenience form of WaitForJobRefs: all
// ids share one owner token. It never claims events (exactly-once
// consumption stays anchored on ClaimWakeEvents).
func (r *Registry) WaitForJobs(ctx context.Context, owner JobOwner, ids []string) (*WaitResult, error) {
	refs := make([]JobRef, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, JobRef{ID: id, Owner: owner})
	}
	return r.WaitForJobRefs(ctx, refs, WaitOptions{})
}

// ClaimWakeEvents returns all previously-unclaimed wake events for a job
// and atomically marks them claimed. When the store implements
// WakeClaimStore the claim happens THERE first — that is the cross-process
// exactly-once point — and the in-memory flags are synchronized to its
// verdict, so two processes can never both consume the same event.
func (r *Registry) ClaimWakeEvents(id string, owner JobOwner) ([]WakeEvent, error) {
	if r == nil {
		return nil, ErrNotFound
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	if id == "" || owner.ThreadID == "" || owner.Handle == "" {
		return nil, ErrNotFound
	}
	job := r.lookupJob(id, owner)
	if job == nil {
		return nil, ErrNotFound
	}
	job.mu.Lock()
	if !ownerMatches(job.Owner, owner) {
		job.mu.Unlock()
		return nil, ErrOwnershipMismatch
	}

	revClaim, hasRevClaim := r.store.(RevisionedWakeClaimStore)
	legacyClaim, hasLegacyClaim := r.store.(WakeClaimStore)
	if hasRevClaim || hasLegacyClaim {
		job.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		var claimed []WakeEvent
		var newRev int64
		var err error
		if hasRevClaim {
			claimed, newRev, err = revClaim.ClaimWakeEvents(ctx, id, r.instanceID)
		} else {
			claimed, err = legacyClaim.ClaimWakeEvents(ctx, id, r.instanceID)
		}
		cancel()
		if err != nil {
			return nil, err
		}
		if !hasRevClaim && len(claimed) > 0 {
			// Legacy claim stores bump the lifecycle revision without
			// reporting it; self-heal by re-reading the record so the next
			// drain commit / CAS persist cannot conflict against our own
			// sidecar write.
			gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
			if stored, found, gErr := r.store.GetQueryJob(gctx, id); gErr == nil && found {
				newRev = stored.Revision
			}
			gcancel()
		}
		// Synchronize memory to the store's atomic verdict — including the
		// NEW LIFECYCLE REVISION and the case where ANOTHER claimant won:
		// events claimed in the stored record must read claimed here too,
		// or waits would rescan them.
		job.mu.Lock()
		if newRev > job.storeRevision {
			job.storeRevision = newRev
		}
		for i := range job.WakeEvents {
			for _, ev := range claimed {
				if job.WakeEvents[i].Kind == ev.Kind && job.WakeEvents[i].At.Equal(ev.At) {
					job.WakeEvents[i].Claimed = true
				}
			}
		}
		remaining := 0
		for i := range job.WakeEvents {
			if !job.WakeEvents[i].Claimed {
				remaining++
			}
		}
		job.mu.Unlock()

		if remaining > 0 {
			// Memory can be ahead of the store (an event was just emitted
			// and its persist has not landed yet). An event absent from
			// the stored record cannot have been observed by any other
			// process, so claiming it locally is still exactly-once; an
			// event present-and-claimed in the store was taken by someone
			// else and only syncs the flag.
			gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
			stored, ok, getErr := r.store.GetQueryJob(gctx, id)
			gcancel()
			if getErr == nil {
				var localClaims []WakeEvent
				job.mu.Lock()
				for i := range job.WakeEvents {
					if job.WakeEvents[i].Claimed {
						continue
					}
					inStore := false
					storeClaimed := false
					if ok {
						for _, sev := range stored.WakeEvents {
							if sev.Kind == job.WakeEvents[i].Kind && sev.At.Equal(job.WakeEvents[i].At) {
								inStore = true
								storeClaimed = sev.Claimed
								break
							}
						}
					}
					switch {
					case !inStore:
						job.WakeEvents[i].Claimed = true
						localClaims = append(localClaims, job.WakeEvents[i])
					case storeClaimed:
						job.WakeEvents[i].Claimed = true
					}
				}
				job.mu.Unlock()
				if len(localClaims) > 0 {
					r.persistJob(job)
					claimed = append(claimed, localClaims...)
				}
			}
		}
		job.mu.Lock()
		job.LastAccess = r.nowFn()
		job.mu.Unlock()
		if len(claimed) > 0 {
			r.notifyAll()
		}
		return claimed, nil
	}

	claimed := make([]WakeEvent, 0, len(job.WakeEvents))
	for i := range job.WakeEvents {
		if !job.WakeEvents[i].Claimed {
			job.WakeEvents[i].Claimed = true
			claimed = append(claimed, job.WakeEvents[i])
		}
	}
	job.LastAccess = r.nowFn()
	job.mu.Unlock()
	if len(claimed) > 0 {
		r.afterTransition(job)
	}
	return claimed, nil
}

// JobRef pairs a job id with the owner credentials for that specific job.
// One run frequently owns several jobs under different owner tokens; the
// multi-ref APIs verify each ref independently.
type JobRef struct {
	ID    string
	Owner JobOwner
}

// WaitOptions controls WaitForJobRefs. Claim makes the wait consume the
// returned events atomically before returning (exactly-once even across
// processes when the store supports WakeClaimStore): if another claimant
// wins the race the wait resumes instead of returning stale events.
type WaitOptions struct {
	Claim bool
}

// WaitForJobRefs blocks until any referenced job has an unclaimed wake
// event, or every referenced job is terminal, or ctx ends. With
// opts.Claim, the returned events are atomically claimed for this caller.
// Unknown ids or owner mismatches fail fast with ErrNotFound — a barrier
// must never hang on a ref it can't observe.
//
// CROSS-PROCESS VISIBILITY: local notifications only cover transitions
// made by THIS process. When a durable store is configured, the wait also
// refreshes each non-locally-driven ref from the store on a bounded
// adaptive cadence (reset on local activity, doubling toward a cap while
// quiet), so a waiter in one process observes wakes and terminal states
// persisted by another. Refresh is read-only reconciliation — it never
// starts a monitor and never resubmits SQL — and claims stay exactly-once
// through the claim store.
func (r *Registry) WaitForJobRefs(ctx context.Context, refs []JobRef, opts WaitOptions) (*WaitResult, error) {
	if r == nil {
		return nil, ErrNotFound
	}
	cleaned := make([]JobRef, 0, len(refs))
	for _, ref := range refs {
		ref.ID = strings.TrimSpace(ref.ID)
		ref.Owner = normalizeOwner(ref.Owner)
		if ref.ID != "" {
			cleaned = append(cleaned, ref)
		}
	}
	if len(cleaned) == 0 {
		return nil, ErrNotFound
	}
	refreshMin, refreshMax := r.barrierRefreshBounds()
	refreshIn := refreshMin
	for {
		wait := r.waitChan() // arm before scanning to avoid lost wakeups
		allTerminal := true
		var firstTerminal *Job
		for _, ref := range cleaned {
			snap, ok := r.Snapshot(ref.ID, ref.Owner)
			if !ok {
				return nil, ErrNotFound
			}
			hasUnclaimed := false
			for i := range snap.WakeEvents {
				if !snap.WakeEvents[i].Claimed {
					hasUnclaimed = true
					break
				}
			}
			if hasUnclaimed {
				if !opts.Claim {
					for i := range snap.WakeEvents {
						if !snap.WakeEvents[i].Claimed {
							ev := snap.WakeEvents[i]
							return &WaitResult{Job: snap, Event: &ev, Events: []WakeEvent{ev}}, nil
						}
					}
				}
				claimed, err := r.ClaimWakeEvents(ref.ID, ref.Owner)
				if err != nil {
					return nil, err
				}
				if len(claimed) > 0 {
					fresh, _ := r.Snapshot(ref.ID, ref.Owner)
					if fresh == nil {
						fresh = snap
					}
					ev := claimed[0]
					return &WaitResult{Job: fresh, Event: &ev, Events: claimed}, nil
				}
				// Another claimant won the race: fall through and keep
				// waiting rather than returning stale events.
			}
			if isTerminalStatus(snap.Status) {
				if firstTerminal == nil {
					firstTerminal = snap
				}
			} else {
				allTerminal = false
			}
		}
		if allTerminal {
			return &WaitResult{Job: firstTerminal}, nil
		}
		if r.store == nil {
			// Local-only deployments: local notifications are complete.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-wait:
			}
			continue
		}
		timer := time.NewTimer(jitter(refreshIn, r.sched.JitterFrac))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-wait:
			timer.Stop()
			refreshIn = refreshMin // local activity: fast cadence again
		case <-timer.C:
			for _, ref := range cleaned {
				if job := r.lookupJob(ref.ID, ref.Owner); job != nil {
					r.refreshJobFromStore(job, ref.Owner)
				}
			}
			refreshIn *= 2
			if refreshIn > refreshMax {
				refreshIn = refreshMax
			}
		}
	}
}

// barrierRefreshBounds derives the adaptive durable-refresh cadence from
// the monitor cadence: fast enough to observe cross-process completion
// promptly, bounded so an idle waiter costs a couple of store reads per
// second at most (and far fewer once backed off).
func (r *Registry) barrierRefreshBounds() (time.Duration, time.Duration) {
	min := r.sched.MaxInterval / 4
	if min < 10*time.Millisecond {
		min = 10 * time.Millisecond
	}
	if min > 250*time.Millisecond {
		min = 250 * time.Millisecond
	}
	max := r.sched.MaxInterval
	if max < 100*time.Millisecond {
		max = 100 * time.Millisecond
	}
	if max > 2*time.Second {
		max = 2 * time.Second
	}
	if max < min {
		max = min
	}
	return min, max
}

// ListActiveByThread returns snapshots of all active jobs owned by a
// thread (any handle). Used by the orchestration slice to tag runs with
// pending query work at completion.
func (r *Registry) ListActiveByThread(threadID string) []*Job {
	if r == nil {
		return nil
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return nil
	}
	r.mu.RLock()
	jobs := make([]*Job, 0, 8)
	for _, job := range r.jobs {
		if job == nil {
			continue
		}
		job.mu.RLock()
		if !isTerminalStatus(job.Status) && strings.TrimSpace(job.Owner.ThreadID) == threadID {
			jobs = append(jobs, cloneJobLocked(job))
		}
		job.mu.RUnlock()
	}
	r.mu.RUnlock()
	return jobs
}
