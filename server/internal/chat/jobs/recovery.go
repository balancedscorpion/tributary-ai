package jobs

import (
	"context"
	"errors"
	"strings"
	"time"
)

// RehydrateStats summarizes a boot-time recovery pass.
type RehydrateStats struct {
	Adopted     int // live handle re-attached, monitoring resumed
	Terminal    int // finished while we were away
	Orphaned    int // cannot be driven; recovery_required wake emitted
	Expired     int // stale reservations (never submitted)
	Skipped     int // already tracked in memory
	Deferred    int // another process holds the monitor lease
	Reconciling int // submission_unknown under a durable reconciliation watch
}

// Rehydrate restores persisted non-terminal jobs after a restart
// (invariant I7). Adoption is honest per capability: DetachedRepoll
// providers resume monitoring; drain providers resume only if the adapter
// confirms the persisted cursor is still live, otherwise the job is
// orphaned with a recovery_required wake — never a fake reattach, never a
// blind resubmit.
func (r *Registry) Rehydrate(ctx context.Context) (RehydrateStats, error) {
	var stats RehydrateStats
	if r == nil || r.store == nil {
		return stats, errors.New("job registry has no durable store configured")
	}
	r.rehydrateMu.Lock()
	defer r.rehydrateMu.Unlock()
	records, err := r.store.ListActiveQueryJobs(ctx)
	if err != nil {
		return stats, err
	}
	for _, listed := range records {
		if strings.TrimSpace(listed.ID) == "" || isTerminalStatus(listed.Status) {
			continue
		}
		// A crash may occur after the durable reservation but before the
		// launch path binds the attempt into the run's obligation ledger.
		// Repair that boundary before recovery performs any profile, submit,
		// reconciliation, adoption, or polling I/O. A transient ledger error
		// defers this record without changing or resubmitting it.
		if bindErr := r.bindQueryObligation(ctx, listed.ID); bindErr != nil {
			r.logfSafe("jobs: rehydrate %s deferred: query obligation bind failed: %v", listed.ID, bindErr)
			stats.Deferred++
			continue
		}
		r.mu.RLock()
		existing := r.jobs[listed.ID]
		r.mu.RUnlock()

		adapter := r.adapterFor(listed.Provider)
		job := existing
		if existing != nil {
			existing.mu.Lock()
			if !existing.driving && existing.profileCancel == nil && listed.Revision > existing.storeRevision {
				applyRecordLocked(existing, listed, r.nowFn())
			}
			if existing.adapter == nil {
				existing.adapter = adapter
			}
			driving := existing.driving
			profileActive := existing.profileCancel != nil
			status := existing.Status
			existing.mu.Unlock()
			if driving || profileActive || isTerminalStatus(status) {
				stats.Skipped++
				continue
			}
		} else {
			job = r.jobFromRecord(listed, adapter)
		}

		// Profiling owns a narrower status+revision+lease fence. Its recovery
		// routine re-reads on any conflict, so it is safe to dispatch before the
		// general driver claim (and avoids double-owning the same lease).
		job.mu.RLock()
		status := job.Status
		profileRec := recordFromJobLocked(job)
		job.mu.RUnlock()
		if status == JobStatusProfiling {
			// Interrupted profile (crash/restart mid-profile). NEVER
			// blindly re-run Profile. A lease held elsewhere defers with
			// zero mutation; otherwise ONE fenced atomic mutation carries
			// the transition + terminal metadata + wakes — no insert-then-
			// ordinary-persist of a profiling transition, ever.
			increment := r.rehydrateInterruptedProfile(ctx, profileRec, adapter, job)
			switch increment {
			case rehydrateProfileDeferred:
				stats.Deferred++
			case rehydrateProfileExpired:
				stats.Expired++
			case rehydrateProfileOrphaned:
				stats.Orphaned++
			}
			continue
		}

		// All remaining decisions are irreversible (expire/orphan/transition),
		// start provider I/O, or hand a provider driver off. Claim first, then
		// refresh from the store under that lease. A ListActive snapshot is never
		// authoritative enough for any of those actions.
		leaseStore, claimed := r.claimDriver(job)
		if !claimed {
			job.mu.RLock()
			terminal := isTerminalStatus(job.Status)
			job.mu.RUnlock()
			if existing == nil {
				job.pollStop()
			}
			if terminal {
				stats.Skipped++
			} else {
				stats.Deferred++
			}
			continue
		}

		driverCtx, stopHeartbeat := r.leaseHeartbeat(job.pollCtx, leaseStore, job.ID)
		handedToDriver := false
		finishDriver := func() {
			stopHeartbeat()
			if !handedToDriver {
				r.releaseDriver(job, leaseStore)
			}
		}
		handoffMonitor := func() {
			handedToDriver = true
			stopHeartbeat()
			go r.runManagedMonitor(job, leaseStore)
		}
		handoffSubmissionWatch := func() {
			handedToDriver = true
			stopHeartbeat()
			go r.runSubmissionWatch(job, leaseStore)
		}
		publish := func(changed bool) bool {
			if existing != nil {
				if changed {
					r.afterTransition(job)
				}
				return true
			}
			return r.insertRehydrated(job)
		}

		job.mu.Lock()
		if job.adapter == nil {
			job.adapter = r.adapterFor(job.Provider)
		}
		adapter = job.adapter
		status = job.Status
		handle := cloneHandle(job.Handle)
		submitDispatched := job.SubmitDispatched
		drainSeq := job.DrainSeq
		resultRecoveryRequired, _ := job.Metadata["result_recovery_required"].(bool)
		resultFetchError, _ := job.Metadata["result_fetch_error"].(string)
		job.mu.Unlock()
		drainProvider := adapter != nil && adapter.Capabilities().RequiresContinuousDrain
		now := r.nowFn()

		switch {
		case resultRecoveryRequired:
			// A prior driver consumed zero or more provider result pages but
			// could not complete materialization. Non-drain result append and
			// cursor persistence are not an atomic replay protocol, and Databricks
			// closes the statement after its final chunk. Re-fetching after a
			// process loss could therefore duplicate rows or hit CLOSED/400.
			// Fail closed into the existing replacement obligation instead of
			// pretending adoption can safely restart the consumable cursor.
			detail := "result retrieval was interrupted after provider completion; a linked recovery query is required"
			if strings.TrimSpace(resultFetchError) != "" {
				detail += ": " + strings.TrimSpace(resultFetchError)
			}
			job.mu.Lock()
			job.Error = detail
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, detail)
			job.mu.Unlock()
			if publish(true) {
				stats.Orphaned++
			} else {
				stats.Skipped++
			}
			finishDriver()
			continue

		case status == JobStatusReserved && !submitDispatched:
			// Fresh under the lease: no remote submit could have happened.
			job.mu.Lock()
			job.Error = "reservation did not survive restart (never submitted)"
			job.transitionLocked(JobStatusExpired, now)
			job.appendWakeLocked(now, WakeTerminal, job.Error)
			job.mu.Unlock()
			if publish(true) {
				stats.Expired++
			} else {
				stats.Skipped++
			}
			finishDriver()
			continue

		case drainProvider && drainSeq == 0:
			// Every legitimately-submitted drain record carries
			// DrainSeq >= 1 (the initial submit-response observation
			// commits atomically at launch). DrainSeq == 0 on any
			// dispatched/active drain record means an initial consumable
			// page may have been acknowledged but never durably committed
			// — resuming would silently lose those rows. Refuse.
			job.mu.Lock()
			job.Error = "initial drain observation was never durably committed; rows from the submit response may be lost"
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
			job.mu.Unlock()
			if publish(true) {
				stats.Orphaned++
			} else {
				stats.Skipped++
			}
			finishDriver()
			continue

		case status == JobStatusReserved && submitDispatched:
			// Crash inside the submit window: ambiguous, never expire.
			if adapter == nil {
				job.mu.Lock()
				job.Error = "submit was dispatched before restart but no adapter is available to reconcile it"
				job.transitionLocked(JobStatusOrphaned, now)
				job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
				job.mu.Unlock()
				if publish(true) {
					stats.Orphaned++
				} else {
					stats.Skipped++
				}
				finishDriver()
				continue
			}
			job.mu.Lock()
			job.transitionLocked(JobStatusSubmissionUnknown, now)
			job.appendWakeLocked(now, WakeSubmissionUnknown, "submit dispatched before restart; outcome unknown")
			job.mu.Unlock()
			if publish(true) {
				stats.Reconciling++
				handoffSubmissionWatch()
			} else {
				stats.Skipped++
				finishDriver()
			}
			continue

		case status == JobStatusSubmissionUnknown && adapter != nil:
			// Durable reconciliation watch: reconcile when the adapter can,
			// enforce the hard budget either way (P0-2). Never a blind
			// resubmit, never a permanent admission pin.
			if publish(false) {
				stats.Reconciling++
				handoffSubmissionWatch()
			} else {
				stats.Skipped++
				finishDriver()
			}
			continue

		case adapter == nil || handle.IsZero():
			job.mu.Lock()
			job.Error = "no adapter or native handle available after restart"
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
			job.mu.Unlock()
			if publish(true) {
				stats.Orphaned++
			} else {
				stats.Skipped++
			}
			finishDriver()
			continue

		case adapter.Capabilities().RequiresContinuousDrain && !r.hasSafeDrainCommitter():
			// Without atomic drain commits we may not drive this job:
			// best-effort would risk row duplication (P0-1).
			job.mu.Lock()
			job.Error = "continuous-drain job requires an atomic DrainCommitter materializer"
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
			job.mu.Unlock()
			if publish(true) {
				stats.Orphaned++
			} else {
				stats.Skipped++
			}
			finishDriver()
			continue
		}

		// Once the actual remote budget is exhausted, do not spend another
		// provider call on adoption. Hand the refreshed durable handle straight
		// to the leased monitor, whose first action is hard-budget cancellation.
		if remaining, bounded := r.hardRemaining(job, r.nowFn()); bounded && remaining <= 0 {
			if publish(false) {
				stats.Adopted++
				handoffMonitor()
			} else {
				stats.Skipped++
				finishDriver()
			}
			continue
		}

		adoptParent, stopAdoptParent := contextWithEither(driverCtx, ctx)
		adoptCtx, adoptCancel := r.providerCallContext(adoptParent, job, true)
		outcome, snap, adoptErr := adapter.Adopt(adoptCtx, handle)
		adoptCancel()
		stopAdoptParent()
		if driverCtx.Err() != nil || (ctx != nil && ctx.Err() != nil) {
			stats.Deferred++
			finishDriver()
			continue
		}
		switch {
		case adoptErr != nil && adapter.Capabilities().DetachedRepoll:
			// Transient adoption failure on a re-pollable provider:
			// resume monitoring degraded; the monitor loop retries and
			// never terminalizes on poll errors.
			job.mu.Lock()
			job.MonitorHealth = MonitorHealthDegraded
			job.mu.Unlock()
			if publish(true) {
				stats.Adopted++
				handoffMonitor()
			} else {
				stats.Skipped++
				finishDriver()
			}
		case adoptErr != nil:
			job.mu.Lock()
			job.Error = "adoption failed: " + adoptErr.Error()
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
			job.mu.Unlock()
			if publish(true) {
				stats.Orphaned++
			} else {
				stats.Skipped++
			}
			finishDriver()
		case outcome == AdoptOutcomeAdopted:
			job.mu.Lock()
			if job.Status == JobStatusSubmissionUnknown {
				job.transitionLocked(JobStatusSubmitted, now)
			}
			if snap.UpdatedHandle != nil && !snap.UpdatedHandle.IsZero() {
				job.Handle = cloneHandle(*snap.UpdatedHandle)
			}
			job.mu.Unlock()
			if publish(true) {
				stats.Adopted++
				handoffMonitor()
			} else {
				stats.Skipped++
				finishDriver()
			}
		case outcome == AdoptOutcomeTerminal:
			// The provider finished while we were away: fully finalize
			// — fetch/materialize results, run the terminal hooks —
			// exactly as a live monitor would, not just a state stamp.
			if !publish(false) {
				stats.Skipped++
				finishDriver()
				continue
			}
			job.mu.Lock()
			if job.assembler == nil {
				job.assembler = newResultAssembler(job.Budgets)
			}
			asm := job.assembler
			job.mu.Unlock()
			adoptCaps := adapter.Capabilities()
			if adoptCaps.RequiresContinuousDrain {
				backlog := &drainBacklog{pages: snap.DrainedPages, cursor: snap.UpdatedHandle, snap: snap}
				flushOK, stop := r.flushDrainBacklog(driverCtx, job, asm, backlog)
				if stop {
					r.logfSafe("jobs: rehydrate %s: %v; deferring to the owning driver", job.ID, ErrDrainSequenceConflict)
					stats.Deferred++
					finishDriver()
					break
				}
				if !flushOK {
					// Hold the observation and let a monitor retry the
					// commit; the terminal snapshot finalizes once the
					// backlog lands.
					job.mu.Lock()
					job.backlog = backlog
					job.mu.Unlock()
					r.emitDrainPathology(job, "durable drain commit failing during terminal adoption")
					stats.Adopted++
					handoffMonitor()
					break
				}
			}
			if handedToDriver {
				continue
			}
			r.finalizeFromSnapshotContext(driverCtx, job, snap, asm, adoptCaps)
			job.pollStop()
			stats.Terminal++
			finishDriver()
		default: // AdoptOutcomeOrphaned
			detail := "provider-side job is no longer reachable"
			if snap.Error != "" {
				detail = snap.Error
			}
			job.mu.Lock()
			job.Error = detail
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, detail)
			job.mu.Unlock()
			if publish(true) {
				stats.Orphaned++
			} else {
				stats.Skipped++
			}
			finishDriver()
		}
	}
	return stats, nil
}

func (r *Registry) hasDrainCommitter() bool {
	_, ok := r.materializer.(DrainCommitter)
	return ok
}

func (r *Registry) hasSafeDrainCommitter() bool {
	if !r.hasDrainCommitter() {
		return false
	}
	if _, leased := r.store.(LeaseStore); leased {
		_, fenced := r.materializer.(FencedDrainCommitter)
		return fenced
	}
	return true
}

type rehydrateProfileOutcome int

const (
	rehydrateProfileDeferred rehydrateProfileOutcome = iota
	rehydrateProfileExpired
	rehydrateProfileOrphaned
)

// rehydrateInterruptedProfile terminalizes an interrupted profiling record
// through the fence: after a confirmed lease, ONE ApplyProfileMutation
// commits the transition, terminal metadata, and wakes atomically. On any
// fence conflict/failure it re-reads and adopts (or defers) — a profiling
// transition is never persisted through the ordinary CAS path. The job is
// only inserted into memory mirroring an already-durable state.
func (r *Registry) rehydrateInterruptedProfile(ctx context.Context, rec JobRecord, adapter ProviderAdapter, job *Job) rehydrateProfileOutcome {
	defer job.pollStop()

	ls, hasLease := r.store.(LeaseStore)
	fenced, hasFence := r.store.(FencedProfileStore)
	driverCtx := ctx
	stopHeartbeat := func() {}
	if hasLease {
		if !hasFence {
			r.logfSafe("jobs: rehydrate %s: lease store without fenced profile mutations; deferring", rec.ID)
			return rehydrateProfileDeferred
		}
		if !r.acquireDriverLease(job, ls, "profile recovery acquire") {
			return rehydrateProfileDeferred // another live process owns it
		}
		defer func() {
			rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = ls.ReleaseMonitorLease(rctx, rec.ID, r.instanceID)
			rcancel()
		}()

		// The list snapshot may predate a profile-handle publication or even the
		// accepted-profile transition. Re-read while holding the lease and adopt
		// that exact record before deciding whether/what to cancel.
		gctx, gcancel := context.WithTimeout(ctx, 2*time.Second)
		fresh, found, gErr := r.store.GetQueryJob(gctx, rec.ID)
		gcancel()
		if gErr != nil || !found {
			return rehydrateProfileDeferred
		}
		if !r.acquireDriverLease(job, ls, "profile recovery post-refresh confirmation") {
			return rehydrateProfileDeferred
		}
		job.mu.Lock()
		applyRecordLocked(job, fresh, r.nowFn())
		if isTerminalStatus(fresh.Status) {
			job.capacityReleased = true
		}
		job.mu.Unlock()
		rec = fresh
		if rec.Status != JobStatusProfiling {
			r.insertRehydratedNoPersist(job)
			if isTerminalStatus(rec.Status) {
				r.wakeEligibleWaiters()
			}
			switch rec.Status {
			case JobStatusExpired:
				return rehydrateProfileExpired
			case JobStatusOrphaned, JobStatusFailed, JobStatusCancelled:
				return rehydrateProfileOrphaned
			default:
				// Accepted/resumed work is now in the general lifecycle domain.
				// A later recurring pass claims it through the normal driver path.
				return rehydrateProfileDeferred
			}
		}

		driverCtx, stopHeartbeat = r.leaseHeartbeat(ctx, ls, rec.ID)
		defer stopHeartbeat()
	}

	var mut ProfileMutation
	var verdict rehydrateProfileOutcome
	if !rec.ProfileDispatched {
		// Provably no remote profile call was ever made.
		expired := JobStatusExpired
		errText := "profile was never dispatched before restart"
		mut = ProfileMutation{
			Error:        &errText,
			TransitionTo: &expired,
			Wakes:        []WakeSpec{{Kind: WakeTerminal, Detail: errText}},
		}
		verdict = rehydrateProfileExpired
	} else {
		// Remote profile work was (or may have been) started: cancel the
		// persisted handle when possible, then honest indeterminate.
		if !rec.ProfileHandle.IsZero() && adapter != nil {
			if pc, ok := adapter.(ProfileCanceller); ok {
				cctx, ccancel := context.WithTimeout(driverCtx, providerCancelTimeout)
				if cErr := pc.CancelProfile(cctx, rec.ProfileHandle); cErr != nil {
					r.logfSafe("jobs: rehydrate profile cancel for %s failed: %v", rec.ID, cErr)
				}
				ccancel()
			}
		}
		orphaned := JobStatusOrphaned
		indeterminate := ProfileOutcomeIndeterminate
		errText := "profile interrupted by restart; outcome indeterminate"
		mut = ProfileMutation{
			Outcome:      &indeterminate,
			Error:        &errText,
			TransitionTo: &orphaned,
			Wakes: []WakeSpec{
				{Kind: WakeRecoveryRequired, Detail: errText},
				{Kind: WakeTerminal, Detail: errText},
			},
		}
		verdict = rehydrateProfileOrphaned
	}

	session := &profileSession{r: r, job: job, hasLease: hasLease, fenced: fenced}
	if err := session.applyMutation(mut); err != nil {
		// Conflict or unconfirmed write: re-read and adopt whatever is
		// durable now; never overwrite, never insert a stale profiling
		// transition.
		gctx, gcancel := context.WithTimeout(ctx, 2*time.Second)
		fresh, found, gErr := r.store.GetQueryJob(gctx, rec.ID)
		gcancel()
		if gErr != nil || !found || fresh.Status == JobStatusProfiling {
			return rehydrateProfileDeferred
		}
		job.mu.Lock()
		applyRecordLocked(job, fresh, r.nowFn())
		job.capacityReleased = true
		job.mu.Unlock()
		r.insertRehydratedNoPersist(job)
		switch fresh.Status {
		case JobStatusExpired:
			return rehydrateProfileExpired
		default:
			return rehydrateProfileOrphaned
		}
	}
	// Fenced success: local state already mirrors the durable record.
	r.insertRehydratedNoPersist(job)
	r.wakeEligibleWaiters()
	return verdict
}

// insertRehydratedNoPersist inserts a job whose durable state is ALREADY
// committed (fenced mutation or adopted read): map insert + notify only,
// deliberately without the ordinary persist of insertRehydrated.
func (r *Registry) insertRehydratedNoPersist(job *Job) {
	r.mu.Lock()
	if existing, exists := r.jobs[job.ID]; exists {
		r.mu.Unlock()
		if existing == job {
			r.notifyAll()
			return
		}
		job.pollStop()
		return
	}
	r.jobs[job.ID] = job
	r.mu.Unlock()
	r.notifyAll()
}

// jobFromRecord rebuilds an in-memory job from its durable record.
func (r *Registry) jobFromRecord(rec JobRecord, adapter ProviderAdapter) *Job {
	pollCtx, pollStop := context.WithCancel(context.Background())
	job := &Job{
		ID:                  rec.ID,
		Provider:            rec.Provider,
		ConnectionID:        rec.ConnectionID,
		SQL:                 rec.SQL,
		SQLPreview:          rec.SQLPreview,
		Status:              rec.Status,
		SubmittedAt:         rec.SubmittedAt,
		RemoteSubmittedAt:   cloneTimePtr(rec.RemoteSubmittedAt),
		NextPollAt:          cloneTimePtr(rec.NextPollAt),
		NextPollAction:      rec.NextPollAction,
		LastAccess:          r.nowFn(),
		Error:               rec.Error,
		Metadata:            cloneMap(rec.Metadata),
		Owner:               rec.Owner,
		Handle:              cloneHandle(rec.Handle),
		IdempotencyKey:      rec.IdempotencyKey,
		ReplacesJobID:       rec.ReplacesJobID,
		KeepOnRunCompletion: rec.KeepOnRunCompletion,
		Budgets:             rec.Budgets.withDefaults(r.policy.DefaultBudgets),
		MonitorHealth:       rec.MonitorHealth,
		PolicyHealth:        rec.PolicyHealth,
		Materialization:     rec.MaterializationState,
		CancelReason:        rec.CancelReason,
		CancelOutcome:       rec.CancelOutcome,
		CancelStartedAt:     cloneTimePtr(rec.CancelStartedAt),
		ArtifactID:          rec.ArtifactID,
		DrainSeq:            rec.DrainSeq,
		DrainRowsCommitted:  rec.DrainRowsCommitted,
		DrainBytesCommitted: rec.DrainBytesCommitted,
		DrainTruncated:      rec.DrainTruncated,
		SubmitDispatched:    rec.SubmitDispatched,
		Validation:          rec.ValidationState,
		ValidationDetail:    rec.ValidationDetail,
		Profile:             cloneQueryProfile(rec.Profile),
		ProfileOutcome:      rec.ProfileOutcome,
		ProfileHandle:       cloneHandle(rec.ProfileHandle),
		ProfilePhase:        rec.ProfilePhase,
		ProfileDispatched:   rec.ProfileDispatched,
		managed:             true,
		adapter:             adapter,
		storeRevision:       rec.Revision,
		wakeSignal:          make(chan struct{}, 1),
		pollCtx:             pollCtx,
		pollStop:            pollStop,
	}
	if job.MonitorHealth == "" {
		job.MonitorHealth = MonitorHealthOK
	}
	if job.Validation == "" {
		job.Validation = ValidationUnvalidated
	}
	// Rehydrated jobs re-enter cross-process capacity accounting; their
	// terminal transition must release it exactly once.
	if _, ok := r.store.(CapacityReserver); ok {
		job.reservedViaStore = true
	}
	if rec.StartedAt != nil {
		t := *rec.StartedAt
		job.StartedAt = &t
	}
	if rec.CompletedAt != nil {
		t := *rec.CompletedAt
		job.CompletedAt = &t
	}
	if len(rec.WakeEvents) > 0 {
		job.WakeEvents = append([]WakeEvent(nil), rec.WakeEvents...)
		job.emittedWakes = map[WakeKind]bool{}
		for _, ev := range rec.WakeEvents {
			job.emittedWakes[ev.Kind] = true
		}
	}
	// Restore cancellation intent so the monitor resumes the confirmed
	// cancel lifecycle after restart. cancellingAt restarts here: the
	// pathology clock measures unconfirmed time under OUR watch.
	if rec.Status == JobStatusCancelRequested || rec.Status == JobStatusCancelling {
		job.cancelWanted = true
	}
	if rec.Status == JobStatusCancelRequested || rec.Status == JobStatusCancelling {
		if rec.CancelStartedAt != nil {
			job.cancellingAt = *rec.CancelStartedAt
		} else {
			job.cancellingAt = r.nowFn()
			job.CancelStartedAt = cloneTimePtr(&job.cancellingAt)
		}
	}
	if rec.CancelReason == CancelReasonBudget {
		job.hardExpired = true
	}
	return job
}

func (r *Registry) insertRehydrated(job *Job) bool {
	r.mu.Lock()
	if _, exists := r.jobs[job.ID]; exists {
		r.mu.Unlock()
		job.pollStop()
		return false
	}
	r.jobs[job.ID] = job
	r.mu.Unlock()
	r.afterTransition(job)
	return true
}

func (r *Registry) orphanRehydrated(job *Job, detail string) {
	now := r.nowFn()
	job.mu.Lock()
	job.Error = detail
	job.transitionLocked(JobStatusOrphaned, now)
	job.appendWakeLocked(now, WakeRecoveryRequired, detail)
	job.mu.Unlock()
	job.pollStop()
	r.insertRehydrated(job)
}

// StartReaper begins periodic garbage collection: terminal jobs leave
// memory after the retention window (durable records remain the source of
// truth), and reservations that never submitted expire after twice the
// reservation TTL. This fixes the historical unbounded-map leak.
func (r *Registry) StartReaper(ctx context.Context, interval time.Duration) {
	if r == nil {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		claimTicker := time.NewTicker(r.sched.ClaimInterval)
		defer claimTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				r.admission.releaseAll()
				return
			case <-ticker.C:
				r.reapOnce()
			case <-claimTicker.C:
				scanTimeout := 2 * r.sched.ProviderCallTimeout
				if scanTimeout < 30*time.Second {
					scanTimeout = 30 * time.Second
				}
				scanCtx, scanCancel := context.WithTimeout(ctx, scanTimeout)
				_, err := r.Rehydrate(scanCtx)
				scanCancel()
				if err != nil && ctx.Err() == nil {
					r.logfSafe("jobs: recurring durable claimant scan failed: %v", err)
				}
			}
		}
	}()
}

func (r *Registry) reapOnce() {
	now := r.nowFn()
	staleReserved := make([]*Job, 0, 4)

	r.mu.Lock()
	for id, job := range r.jobs {
		if job == nil {
			delete(r.jobs, id)
			continue
		}
		job.mu.RLock()
		terminal := isTerminalStatus(job.Status)
		terminalPersistPending := job.terminalPersistPending
		last := job.LastAccess
		reservedStale := job.Status == JobStatusReserved &&
			now.Sub(job.SubmittedAt) > 2*r.admission.cfg.ReservationTTL
		stop := job.pollStop
		job.mu.RUnlock()

		if terminal && !terminalPersistPending && now.Sub(last) > r.retention {
			if stop != nil {
				stop()
			}
			delete(r.jobs, id)
			continue
		}
		if reservedStale {
			staleReserved = append(staleReserved, job)
		}
	}
	r.mu.Unlock()

	for _, job := range staleReserved {
		job.mu.Lock()
		if job.Status == JobStatusReserved {
			job.Error = "reservation expired before submission"
			job.transitionLocked(JobStatusExpired, now)
			job.appendWakeLocked(now, WakeTerminal, job.Error)
		}
		stop := job.pollStop
		job.mu.Unlock()
		if stop != nil {
			stop()
		}
		r.afterTransition(job)
	}
}
