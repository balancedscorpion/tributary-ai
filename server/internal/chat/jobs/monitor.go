package jobs

import (
	"context"
	"errors"
	"strings"
	"time"
)

const providerCancelTimeout = 10 * time.Second

// runManagedMonitor drives one managed job to a terminal state. It is the
// only writer of managed execution transitions after submit. Monitoring
// errors degrade MonitorHealth and back off; they never terminalize
// execution (invariant I3). For RequiresContinuousDrain providers this
// loop is also the protocol keepalive and incremental materializer.
// ErrDrainSequenceConflict: the durable record's DrainSeq moved past what
// this process believes is next — another driver owns the stream. The
// local monitor must stop driving immediately.
var ErrDrainSequenceConflict = errors.New("drain sequence advanced by another driver")

// drainBacklog is an observed-but-not-yet-durable drain observation. While
// a backlog exists the monitor never re-polls (a re-poll would consume the
// next page while this one is uncommitted, losing it on crash) and no
// observable state — cursor, DrainSeq, assembler — advances.
type drainBacklog struct {
	pages  []ResultPage
	cursor *NativeHandle  // snapshot-level final cursor fallback
	snap   StatusSnapshot // deferred until every page is durably committed
}

func (r *Registry) runManagedMonitor(job *Job, leaseStore LeaseStore) {
	if job == nil || job.adapter == nil {
		return
	}
	if !r.confirmDriverLease(job, leaseStore) {
		r.releaseDriver(job, leaseStore)
		return
	}
	adapter := job.adapter
	caps := adapter.Capabilities()
	job.mu.Lock()
	if caps.RequiresContinuousDrain {
		prepareDrainAssemblerLocked(job)
	} else if job.assembler == nil {
		job.assembler = newResultAssembler(job.Budgets)
	}
	// Ownership handoff: launch populated the assembler with any initial
	// submit-response pages; from here only this goroutine touches it.
	assembler := job.assembler
	job.mu.Unlock()
	pollCount := 0
	var lastCancelIssue time.Time
	var lastCancelHandle NativeHandle

	// Renewal is independent from poll/backoff and tolerates transient store
	// errors until the last confirmed lease would expire. driverCtx is also
	// the parent of every provider call, so lease loss aborts in-flight I/O.
	driverCtx, stopHeartbeat := r.leaseHeartbeat(job.pollCtx, leaseStore, job.ID)
	defer r.releaseDriver(job, leaseStore)
	defer stopHeartbeat()
	var leaseDone <-chan struct{}
	if leaseStore != nil {
		leaseDone = driverCtx.Done()
	}

	leaseLost := func() bool {
		if leaseDone == nil {
			return false
		}
		select {
		case <-leaseDone:
			return true
		default:
			return false
		}
	}

	for {
		select {
		case <-driverCtx.Done():
			return
		default:
		}
		if leaseLost() {
			return
		}

		job.mu.RLock()
		status := job.Status
		handle := cloneHandle(job.Handle)
		wantCancel := job.cancelWanted
		monitorErrs := job.monitorErrs
		backlog := job.backlog
		nextPollAt := cloneTimePtr(job.NextPollAt)
		nextPollAction := job.NextPollAction
		job.mu.RUnlock()
		if isTerminalStatus(status) {
			return
		}

		// The hard budget is checked before EVERY provider call and again on
		// every error path. Poll/drain failures therefore cannot bypass it.
		if r.evaluateJobPolicy(job, caps, r.nowFn()) {
			continue
		}
		if r.handleUnconfirmedCancellation(job, r.nowFn()) {
			return
		}

		retryAfter := time.Duration(0)
		// Commit the exact staged page/cursor before cancellation or another
		// poll. In particular Trino DELETE targets nextUri, so discarding a
		// backlog would cancel a stale cursor and lose already-consumed rows.
		if backlog != nil {
			ok, stop := r.flushDrainBacklog(driverCtx, job, assembler, backlog)
			if stop {
				r.logfSafe("jobs: %s: %v; stopping local driver", job.ID, ErrDrainSequenceConflict)
				return
			}
			if !ok {
				r.emitDrainPathology(job, "durable drain commit failing; holding observation and retrying")
				if r.evaluateJobPolicy(job, caps, r.nowFn()) {
					continue
				}
				job.mu.RLock()
				emergency := job.hardExpired && job.cancelWanted
				job.mu.RUnlock()
				if emergency {
					terminal, delay := r.emergencyCancelDrainBacklog(driverCtx, job, adapter, backlog, caps)
					if terminal {
						return
					}
					if delay > 0 {
						r.sleepMonitorContext(job, driverCtx, delay)
					}
					continue
				}
				r.sleepMonitorContext(job, driverCtx, r.sched.errorInterval(1, caps))
				continue
			}
			job.mu.Lock()
			if job.backlog == backlog {
				job.backlog = nil
			}
			job.mu.Unlock()
			retryAfter = backlog.snap.RetryAfter
			if r.applySnapshot(job, backlog.snap, r.nowFn()) {
				r.finalizeFromSnapshotContext(driverCtx, job, backlog.snap, assembler, caps)
				return
			}
			// Refresh after the durable cursor advance before deciding which
			// endpoint to cancel or poll next.
			job.mu.RLock()
			status = job.Status
			handle = cloneHandle(job.Handle)
			wantCancel = job.cancelWanted
			nextPollAt = cloneTimePtr(job.NextPollAt)
			nextPollAction = job.NextPollAction
			job.mu.RUnlock()
		}

		// A durable Retry-After survives process/lease handoff. It never delays
		// cancellation, and a newly committed drain observation may tighten it.
		if !wantCancel && retryAfter > 0 {
			delay := r.sched.nextInterval(pollCount, caps, retryAfter)
			r.persistRetryDeadline(job, delay, RetryActionPoll, caps, retryAfter)
			r.sleepMonitorContext(job, driverCtx, delay)
			continue
		}
		honorDurableDelay := nextPollAt != nil && (!wantCancel ||
			(nextPollAction != "" && nextPollAction != RetryActionPoll))
		if honorDurableDelay {
			remaining := nextPollAt.Sub(r.nowFn())
			if remaining > 0 {
				r.sleepMonitorContext(job, driverCtx, remaining)
				continue
			}
			job.mu.Lock()
			if job.NextPollAt != nil && !job.NextPollAt.After(r.nowFn()) {
				job.NextPollAt = nil
				job.NextPollAction = ""
			}
			job.mu.Unlock()
		} else if wantCancel && nextPollAt != nil && (nextPollAction == "" || nextPollAction == RetryActionPoll) {
			// A newly requested cancellation bypasses an ordinary poll delay.
			job.mu.Lock()
			job.NextPollAt = nil
			job.NextPollAction = ""
			job.mu.Unlock()
		}

		now := r.nowFn()
		handleAdvanced := !lastCancelHandle.IsZero() && !handlesEqual(handle, lastCancelHandle)
		reissueDue := status == JobStatusCancelling && (lastCancelIssue.IsZero() ||
			handleAdvanced || now.Sub(lastCancelIssue) >= r.sched.CancelUnconfirmedAfter/2)
		if wantCancel && (status == JobStatusCancelRequested || reissueDue) {
			job.mu.Lock()
			startedCancel := job.CancelStartedAt == nil
			if startedCancel {
				t := now
				job.CancelStartedAt = &t
				job.cancellingAt = t
			}
			job.mu.Unlock()
			if startedCancel {
				r.afterTransition(job)
			}
			cctx, cancel := context.WithTimeout(driverCtx, providerCancelTimeout)
			err := adapter.Cancel(cctx, handle)
			cancel()
			if err == nil {
				lastCancelIssue = now
				lastCancelHandle = cloneHandle(handle)
				if status == JobStatusCancelRequested {
					job.mu.Lock()
					transitioned := markCancelAcceptedLocked(job, now)
					job.mu.Unlock()
					if transitioned {
						r.afterTransition(job)
						status = JobStatusCancelling
					}
				}
			} else {
				retryAfter = RetryAfterFromError(err)
				r.recordMonitorError(job, now, err)
				r.logfSafe("jobs: cancel request for %s failed (will retry): %v", job.ID, err)
				if retryAfter > 0 {
					delay := r.sched.errorInterval(monitorErrs+1, caps, retryAfter)
					r.persistRetryDeadline(job, delay, RetryActionCancel, caps, retryAfter)
					r.sleepMonitorContext(job, driverCtx, delay)
					continue
				}
			}
		}

		observed := false
		if status == JobStatusCancelling && caps.CancelConfirmable {
			cctx, cancel := context.WithTimeout(driverCtx, providerCancelTimeout)
			confirmed, snap, err := adapter.ConfirmCancel(cctx, handle)
			cancel()
			now = r.nowFn()
			if err != nil {
				if driverCtx.Err() != nil {
					return
				}
				if IsPermanentObservationLoss(err) {
					r.orphanCancelIndeterminate(job, err.Error())
					return
				}
				r.recordMonitorError(job, now, err)
				if r.evaluateJobPolicy(job, caps, now) {
					continue
				}
				hint := RetryAfterFromError(err)
				delay := r.sched.errorInterval(monitorErrs+1, caps, hint)
				if hint > 0 {
					r.persistRetryDeadline(job, delay, RetryActionConfirmCancel, caps, hint)
				}
				r.sleepMonitorContext(job, driverCtx, delay)
				continue
			}
			r.recordMonitorSuccess(job, now)
			retryAfter = snap.RetryAfter
			if snap.terminal() && snap.State != ProviderStateCancelled {
				r.finalizeFromSnapshotContext(driverCtx, job, snap, assembler, caps)
				return
			}
			if confirmed || snap.State == ProviderStateCancelled {
				r.finalizeCancelConfirmed(job, snap.Error)
				return
			}
			if snap.State != ProviderStateUnknown {
				if r.applySnapshot(job, snap, now) {
					r.finalizeFromSnapshotContext(driverCtx, job, snap, assembler, caps)
					return
				}
			}
			observed = true // confirmation already performed the provider poll
		}

		if !observed {
			// During cancellation, observation/confirmation remains allowed after
			// the hard deadline; ordinary polling is capped by the remaining budget.
			capHard := !(wantCancel || status == JobStatusCancelRequested || status == JobStatusCancelling)
			pctx, pcancel := r.providerCallContext(driverCtx, job, capHard)
			snap, err := adapter.Poll(pctx, handle)
			pcancel()
			now = r.nowFn()
			if err != nil {
				if driverCtx.Err() != nil {
					return
				}
				if IsPermanentObservationLoss(err) {
					if wantCancel || status == JobStatusCancelling || status == JobStatusCancelRequested {
						r.orphanCancelIndeterminate(job, err.Error())
					} else {
						r.orphanFromMonitor(job, err.Error())
					}
					return
				}
				r.recordMonitorError(job, now, err)
				if r.evaluateJobPolicy(job, caps, now) {
					continue
				}
				hint := RetryAfterFromError(err)
				delay := r.sched.errorInterval(monitorErrs+1, caps, hint)
				if hint > 0 {
					action := RetryActionPoll
					if wantCancel || status == JobStatusCancelRequested || status == JobStatusCancelling {
						action = RetryActionConfirmCancel
					}
					r.persistRetryDeadline(job, delay, action, caps, hint)
				}
				r.sleepMonitorContext(job, driverCtx, delay)
				continue
			}
			r.recordMonitorSuccess(job, now)
			retryAfter = snap.RetryAfter
			if caps.RequiresContinuousDrain && (snap.terminal() || len(snap.DrainedPages) > 0 || snap.UpdatedHandle != nil) {
				// Stage even after DELETE/cancel: the next iteration commits the
				// returned page and latest cursor before any further action.
				job.mu.Lock()
				job.backlog = &drainBacklog{pages: snap.DrainedPages, cursor: snap.UpdatedHandle, snap: snap}
				job.mu.Unlock()
				continue
			}
			if r.applySnapshot(job, snap, now) {
				r.finalizeFromSnapshotContext(driverCtx, job, snap, assembler, caps)
				return
			}
		}

		if r.evaluateJobPolicy(job, caps, r.nowFn()) {
			continue
		}
		if r.handleUnconfirmedCancellation(job, r.nowFn()) {
			return
		}
		pollCount++
		delay := r.sched.nextInterval(pollCount, caps, retryAfter)
		if retryAfter > 0 {
			action := RetryActionPoll
			if wantCancel || status == JobStatusCancelRequested || status == JobStatusCancelling {
				action = RetryActionConfirmCancel
			}
			r.persistRetryDeadline(job, delay, action, caps, retryAfter)
		}
		r.sleepMonitorContext(job, driverCtx, delay)
	}
}

// markCancelAcceptedLocked records provider acceptance without extending the
// durable grace window that began when cancellation was first requested.
// Caller holds job.mu.
func markCancelAcceptedLocked(job *Job, now time.Time) bool {
	if job == nil || !job.transitionLocked(JobStatusCancelling, now) {
		return false
	}
	if job.CancelStartedAt == nil {
		t := now
		job.CancelStartedAt = &t
	}
	if job.cancellingAt.IsZero() {
		job.cancellingAt = *job.CancelStartedAt
	}
	return true
}

// prepareDrainAssemblerLocked restores the cumulative budget baseline at a
// driver boundary. Caller holds job.mu.
func prepareDrainAssemblerLocked(job *Job) {
	if job == nil {
		return
	}
	if job.assembler == nil || !job.assembler.matchesCommitted(
		job.DrainRowsCommitted, job.DrainBytesCommitted, job.DrainTruncated) {
		// If this process retained an old assembler/backlog across lease loss
		// while another holder advanced the stream, both are stale together;
		// discarding them prevents replaying an old page as the next DrainSeq.
		job.assembler = newResultAssemblerFromCommitted(job.Budgets,
			job.DrainRowsCommitted, job.DrainBytesCommitted, job.DrainTruncated)
		job.backlog = nil
	}
}

// emergencyCancelDrainBacklog is the hard-budget escape hatch for a broken
// durable sink. Before the hard deadline we never cancel past an uncommitted
// page. At the deadline, continuing the expensive query indefinitely is worse:
// cancel the newest observed cursor while retaining the backlog in memory and
// terminalize honestly as recovery-required (never claim complete results).
func (r *Registry) emergencyCancelDrainBacklog(ctx context.Context, job *Job, adapter ProviderAdapter, backlog *drainBacklog, caps ProviderCapabilities) (bool, time.Duration) {
	job.mu.RLock()
	handle := cloneHandle(job.Handle)
	job.mu.RUnlock()
	if backlog.cursor != nil && !backlog.cursor.IsZero() {
		handle = cloneHandle(*backlog.cursor)
	}
	for i := len(backlog.pages) - 1; i >= 0; i-- {
		if backlog.pages[i].CursorAfter != nil && !backlog.pages[i].CursorAfter.IsZero() {
			handle = cloneHandle(*backlog.pages[i].CursorAfter)
			break
		}
	}
	cctx, cancel := context.WithTimeout(ctx, providerCancelTimeout)
	err := adapter.Cancel(cctx, handle)
	cancel()
	now := r.nowFn()
	if err != nil {
		r.recordMonitorError(job, now, err)
		if r.handleUnconfirmedCancellation(job, now) {
			return true, 0
		}
		hint := RetryAfterFromError(err)
		delay := r.sched.errorInterval(1, caps, hint)
		if hint > 0 {
			r.persistRetryDeadline(job, delay, RetryActionCancel, caps, hint)
		}
		return false, delay
	}
	job.mu.Lock()
	job.CancelOutcome = CancelOutcomeUnconfirmed
	job.Error = "hard execution budget reached while durable drain commit was unavailable; emergency cancellation issued against the latest observed cursor and result recovery is required"
	if job.Metadata == nil {
		job.Metadata = map[string]any{}
	}
	job.Metadata["needs_reconciliation"] = true
	job.Metadata["emergency_cancel_handle"] = handle
	job.Metadata["uncommitted_pages_at_cancel"] = len(backlog.pages)
	job.Metadata["remote_state_after_cancel"] = "indeterminate"
	job.transitionLocked(JobStatusOrphaned, now)
	job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
	job.appendWakeLocked(now, WakeTerminal, job.Error)
	job.mu.Unlock()
	r.afterTransition(job)
	return true, 0
}

// flushDrainBacklog commits the staged observation page by page. Each
// page's assembler/cursor/seq advance happens ONLY after its durable
// commit succeeds; committed pages are removed from the backlog so a
// partial flush never re-commits them. Returns ok=false to retry later,
// stop=true when another driver owns the stream.
func (r *Registry) flushDrainBacklog(ctx context.Context, job *Job, assembler *resultAssembler, backlog *drainBacklog) (ok bool, stop bool) {
	for len(backlog.pages) > 0 {
		page := backlog.pages[0]
		bounded, _ := assembler.plan(page)
		cursor := page.CursorAfter
		if cursor == nil {
			cursor = backlog.cursor
		}
		var terminal *StatusSnapshot
		finalCursorStillPending := backlog.cursor != nil && !backlog.cursor.IsZero() &&
			(cursor == nil || cursor.IsZero() || !handlesEqual(*cursor, *backlog.cursor))
		if backlog.snap.terminal() && len(backlog.pages) == 1 && !finalCursorStillPending {
			terminal = &backlog.snap
		}
		if err := r.commitDrainObservationState(ctx, job, bounded, cursor, terminal); err != nil {
			if errors.Is(err, ErrDrainSequenceConflict) || errors.Is(err, ErrLeaseLost) {
				return false, true
			}
			return false, false
		}
		assembler.commit(page, bounded)
		backlog.pages = backlog.pages[1:]
	}
	// Page-less cursor advance (or nothing to do): still atomic.
	if len(backlog.pages) == 0 && backlog.cursor != nil && !backlog.cursor.IsZero() {
		job.mu.RLock()
		current := job.Handle.Extra["next_uri"]
		incoming := backlog.cursor.Extra["next_uri"]
		sameID := job.Handle.ID == backlog.cursor.ID
		job.mu.RUnlock()
		if !(sameID && current == incoming) {
			var terminal *StatusSnapshot
			if backlog.snap.terminal() {
				terminal = &backlog.snap
			}
			if err := r.commitDrainObservationState(ctx, job, ResultPage{}, backlog.cursor, terminal); err != nil {
				if errors.Is(err, ErrDrainSequenceConflict) || errors.Is(err, ErrLeaseLost) {
					return false, true
				}
				return false, false
			}
		}
		backlog.cursor = nil
	}
	// A terminal continuous-drain observation can legitimately contain no
	// rows and no cursor refinement. It still needs one atomic drain commit:
	// otherwise the provider cursor has been consumed while the durable record
	// remains active and a restart may try to drive that expired cursor again.
	job.mu.RLock()
	terminalDurable := isTerminalStatus(job.Status)
	job.mu.RUnlock()
	if backlog.snap.terminal() && !terminalDurable {
		if err := r.commitDrainObservationState(ctx, job, ResultPage{}, nil, &backlog.snap); err != nil {
			if errors.Is(err, ErrDrainSequenceConflict) || errors.Is(err, ErrLeaseLost) {
				return false, true
			}
			return false, false
		}
	}
	return true, false
}

// emitDrainPathology records (once, durably) that drain commits are
// failing while the job honestly stays non-terminal.
func (r *Registry) emitDrainPathology(job *Job, detail string) {
	now := r.nowFn()
	job.mu.Lock()
	if job.Metadata == nil {
		job.Metadata = map[string]any{}
	}
	job.Metadata["materialization_error"] = detail
	woke := job.appendWakeLocked(now, WakePathology, detail)
	job.mu.Unlock()
	if woke {
		r.afterTransition(job)
	}
}

// orphanFromMonitor terminalizes a job whose observation is definitively
// unrecoverable (typed PermanentObservationLoss from the adapter).
func (r *Registry) orphanFromMonitor(job *Job, detail string) {
	now := r.nowFn()
	job.mu.Lock()
	if strings.TrimSpace(detail) == "" {
		detail = "provider observation permanently lost"
	}
	job.Error = detail
	job.transitionLocked(JobStatusOrphaned, now)
	job.appendWakeLocked(now, WakeRecoveryRequired, detail)
	job.mu.Unlock()
	r.afterTransition(job)
}

// orphanCancelIndeterminate handles a forward-only cursor disappearing after
// cancellation was requested (notably Trino DELETE + subsequent 404/410).
// That is neither proof of clean cancellation nor a generic observation loss:
// preserve the handle, mark the cancellation unconfirmed, and force recovery.
func (r *Registry) orphanCancelIndeterminate(job *Job, providerDetail string) {
	now := r.nowFn()
	job.mu.Lock()
	job.CancelOutcome = CancelOutcomeUnconfirmed
	job.Error = "cancellation was requested but the provider terminal state is indeterminate; reconciliation required"
	if strings.TrimSpace(providerDetail) != "" {
		job.Error += ": " + strings.TrimSpace(providerDetail)
	}
	if job.Metadata == nil {
		job.Metadata = map[string]any{}
	}
	job.Metadata["needs_reconciliation"] = true
	job.Metadata["remote_state_after_cancel"] = "indeterminate"
	job.transitionLocked(JobStatusOrphaned, now)
	job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
	job.appendWakeLocked(now, WakeTerminal, job.Error)
	job.mu.Unlock()
	r.afterTransition(job)
}

// handleUnconfirmedCancellation records the pathology after its grace window.
// When the actual remote execution budget has also elapsed it releases the
// admission slot as expired, but preserves an honest unconfirmed outcome and
// a recovery-required wake instead of claiming a clean cancellation.
func (r *Registry) handleUnconfirmedCancellation(job *Job, now time.Time) bool {
	job.mu.Lock()
	if (job.Status != JobStatusCancelRequested && job.Status != JobStatusCancelling) || job.cancellingAt.IsZero() ||
		now.Sub(job.cancellingAt) < r.sched.CancelUnconfirmedAfter {
		job.mu.Unlock()
		return false
	}
	if job.CancelOutcome == CancelOutcomeNone {
		job.CancelOutcome = CancelOutcomeUnconfirmed
	}
	if job.Metadata == nil {
		job.Metadata = map[string]any{}
	}
	job.Metadata["cancel_unconfirmed_since"] = job.cancellingAt.UTC().Format(time.RFC3339)
	woke := job.appendWakeLocked(now, WakePathology,
		"cancellation requested but unconfirmed after "+now.Sub(job.cancellingAt).Round(time.Second).String())
	hard := job.Budgets.HardExecution
	start := remoteStartLocked(job)
	terminal := hard > 0 && !start.IsZero() && now.Sub(start) >= hard
	if terminal {
		job.Error = "cancellation unconfirmed past the hard execution budget; remote query state unknown — reconciliation required"
		job.PolicyHealth = PolicyHealthHardExceeded
		job.Metadata["needs_reconciliation"] = true
		job.Metadata["remote_state_at_expiry"] = "unconfirmed"
		job.transitionLocked(JobStatusExpired, now)
		job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
		job.appendWakeLocked(now, WakeTerminal, job.Error)
	}
	job.mu.Unlock()
	if woke || terminal {
		r.afterTransition(job)
	}
	return terminal
}

// commitDrainObservation performs one atomic drain commit: bounded chunk +
// post-page cursor + job record, deduped by a strictly increasing Seq.
// NOTHING in memory advances until the durable commit succeeds — the
// candidate seq/handle/record are computed without mutating the job, so a
// failed commit leaves the job exactly where it was and the same
// observation can be retried. External revision conflicts (sidecar writes)
// are reconciled by refreshing the stored revision and retrying the SAME
// observation, provided the durable DrainSeq still expects it.
func (r *Registry) commitDrainObservation(ctx context.Context, job *Job, bounded ResultPage, cursor *NativeHandle) error {
	return r.commitDrainObservationState(ctx, job, bounded, cursor, nil)
}

// commitDrainObservationState is the terminal-aware form used for the last
// observation of a forward-only stream. The final chunk/cursor and terminal
// lifecycle record commit in one transaction, so ordinary terminal-persist
// outages can never leave a durable active record pointing at a consumed
// cursor. Non-terminal callers use commitDrainObservation above.
func (r *Registry) commitDrainObservationState(ctx context.Context, job *Job, bounded ResultPage, cursor *NativeHandle, terminal *StatusSnapshot) error {
	committer, ok := r.materializer.(DrainCommitter)
	if !ok {
		return ErrDrainCommitUnsupported // unreachable: Launch/Rehydrate refuse
	}

	job.mu.Lock()
	seq := job.DrainSeq + 1
	handle := cloneHandle(job.Handle)
	if cursor != nil && !cursor.IsZero() {
		handle = cloneHandle(*cursor)
	}
	rec := recordFromJobLocked(job)
	if terminal != nil && terminal.terminal() {
		applyTerminalSnapshotToRecord(&rec, job, *terminal, r.nowFn())
	}
	job.mu.Unlock()

	commit := buildDrainCommit(rec, seq, bounded, handle)

	// Single-shot with an immediate (sleep-free) conflict-reconcile retry:
	// PACING IS THE CALLER'S JOB. The monitor's backlog loop retries on its
	// error cadence; sleeping here too would compound into a double
	// backoff that also ignores lease loss and cancel nudges.
	var err error
	rebuiltFromFresh := false
	for attempt := 0; attempt < 2; attempt++ {
		commitTimeout := 5 * time.Second
		if remaining, boundedByHard := r.hardRemaining(job, r.nowFn()); boundedByHard {
			if remaining <= 0 {
				return context.DeadlineExceeded
			}
			if remaining < commitTimeout {
				commitTimeout = remaining
			}
		}
		cctx, cancel := context.WithTimeout(ctx, commitTimeout)
		var newRev int64
		if fenced, ok := r.materializer.(FencedDrainCommitter); ok {
			newRev, err = fenced.CommitDrainObservationFenced(cctx, commit, r.instanceID)
		} else {
			newRev, err = committer.CommitDrainObservation(cctx, commit)
		}
		cancel()
		if err == nil {
			// Durable success: NOW advance observable state.
			job.mu.Lock()
			if (rebuiltFromFresh || terminal != nil) && job.storeRevision <= commit.Record.Revision {
				// The retry record was rebuilt from the authoritative store image,
				// including the sidecar mutation that caused the conflict. Mirror
				// it locally so the monitor's next ordinary persist cannot erase
				// validation/cancel/wake state that the drain commit preserved.
				committed := commit.Record
				committed.Revision = newRev
				applyRecordLocked(job, committed, r.nowFn())
			}
			job.DrainSeq = seq
			job.Handle = cloneHandle(handle)
			job.DrainRowsCommitted = commit.DrainRowsCommitted
			job.DrainBytesCommitted = commit.DrainBytesCommitted
			job.DrainTruncated = commit.DrainTruncated
			job.Materialization = commit.Record.MaterializationState
			if newRev > job.storeRevision {
				job.storeRevision = newRev
			}
			job.mu.Unlock()
			r.notifyAll()
			return nil
		}
		if !errors.Is(err, ErrRevisionConflict) || r.store == nil {
			return err
		}
		// A sidecar (wake claim, validation, another writer) bumped the
		// lifecycle revision. Reconcile: if the durable DrainSeq still
		// expects exactly this observation, refresh the revision and retry
		// the SAME commit once; if it moved past us, another driver owns
		// the stream.
		gctx, gcancel := context.WithTimeout(ctx, 2*time.Second)
		stored, found, gErr := r.store.GetQueryJob(gctx, rec.ID)
		gcancel()
		if gErr != nil || !found {
			return err
		}
		switch {
		case isTerminalStatus(stored.Status):
			// A concurrent terminal lifecycle won. Never append another page
			// or rewrite that terminal record as part of conflict recovery.
			return ErrDrainSequenceConflict
		case stored.DrainSeq == seq-1:
			// Retry the observation on the fresh whole-record image. Only the
			// drain-owned fields are patched; every concurrent lifecycle,
			// validation, metadata, and wake mutation remains byte-for-byte.
			if terminal != nil {
				mergeTerminalLifecycleRecord(&stored, commit.Record)
			}
			commit = buildDrainCommit(stored, seq, bounded, handle)
			rebuiltFromFresh = true
		case stored.DrainSeq >= seq:
			return ErrDrainSequenceConflict
		default:
			return err
		}
	}
	return err
}

// mergeTerminalLifecycleRecord overlays only the terminal verdict fields on
// a freshly-read record. Sidecar axes (validation/artifact/profile), provider
// identity, and concurrent metadata stay authoritative from fresh.
func mergeTerminalLifecycleRecord(fresh *JobRecord, terminal JobRecord) {
	if fresh == nil || !isTerminalStatus(terminal.Status) {
		return
	}
	fresh.Status = terminal.Status
	fresh.CompletedAt = cloneTimePtr(terminal.CompletedAt)
	fresh.LastAccess = terminal.LastAccess
	fresh.Error = terminal.Error
	fresh.CancelReason = terminal.CancelReason
	fresh.CancelOutcome = terminal.CancelOutcome
	fresh.CancelStartedAt = cloneTimePtr(terminal.CancelStartedAt)
	fresh.MonitorHealth = terminal.MonitorHealth
	fresh.PolicyHealth = terminal.PolicyHealth
	fresh.MaterializationState = terminal.MaterializationState
	fresh.NextPollAt = nil
	fresh.NextPollAction = ""
	existing := map[WakeKind]bool{}
	for _, event := range fresh.WakeEvents {
		existing[event.Kind] = true
	}
	for _, event := range terminal.WakeEvents {
		if !existing[event.Kind] {
			fresh.WakeEvents = append(fresh.WakeEvents, event)
			existing[event.Kind] = true
		}
	}
}

// applyTerminalSnapshotToRecord mirrors applyTerminalStateLocked without
// mutating the live job before the atomic drain transaction succeeds. Caller
// holds job.mu, allowing cancellation-race intent to be captured honestly.
func applyTerminalSnapshotToRecord(rec *JobRecord, job *Job, snap StatusSnapshot, now time.Time) {
	if rec == nil || job == nil || !snap.terminal() {
		return
	}
	detail := ""
	switch snap.State {
	case ProviderStateSucceeded:
		rec.Status = JobStatusCompleted
		if job.cancelWanted {
			rec.CancelOutcome = CancelOutcomeSupersededByCompletion
		}
		detail = "completed"
	case ProviderStateFailed:
		rec.Status = JobStatusFailed
		if job.cancelWanted {
			rec.CancelOutcome = CancelOutcomeSupersededByFailure
		}
		rec.Error = strings.TrimSpace(snap.Error)
		if rec.Error == "" {
			rec.Error = "query failed"
		}
		detail = rec.Error
	case ProviderStateCancelled:
		rec.CancelOutcome = CancelOutcomeConfirmed
		if job.hardExpired {
			rec.Status = JobStatusExpired
			detail = "hard execution budget exceeded"
		} else {
			rec.Status = JobStatusCancelled
			if strings.TrimSpace(rec.Error) == "" {
				rec.Error = "query cancelled"
			}
			detail = "cancelled"
		}
	}
	rec.LastAccess = now
	rec.CompletedAt = cloneTimePtr(&now)
	rec.NextPollAt = nil
	rec.NextPollAction = ""
	for _, event := range rec.WakeEvents {
		if event.Kind == WakeTerminal {
			return
		}
	}
	rec.WakeEvents = append(rec.WakeEvents, WakeEvent{At: now, Kind: WakeTerminal, Detail: detail})
}

func buildDrainCommit(base JobRecord, seq int64, bounded ResultPage, handle NativeHandle) DrainCommit {
	base.DrainSeq = seq
	base.Handle = cloneHandle(handle)
	base.DrainRowsCommitted += int64(len(bounded.Rows))
	base.DrainBytesCommitted += bounded.Bytes
	base.DrainTruncated = base.DrainTruncated || bounded.Truncated
	if base.MaterializationState == "" || base.MaterializationState == MaterializationNone {
		base.MaterializationState = MaterializationPartial
	}
	return DrainCommit{
		JobID: base.ID, Owner: base.Owner, Seq: seq, Page: bounded,
		Handle: cloneHandle(handle), DrainRowsCommitted: base.DrainRowsCommitted,
		DrainBytesCommitted: base.DrainBytesCommitted, DrainTruncated: base.DrainTruncated,
		Record: base,
	}
}

// commitDrainObservationWithRetry adds a short bounded retry for the
// LAUNCH path only, where no monitor loop exists yet to own pacing.
func (r *Registry) commitDrainObservationWithRetry(ctx context.Context, job *Job, bounded ResultPage, cursor *NativeHandle, attempts int) error {
	return r.commitDrainObservationWithRetryState(ctx, job, bounded, cursor, nil, attempts)
}

func (r *Registry) commitDrainObservationWithRetryState(ctx context.Context, job *Job, bounded ResultPage, cursor *NativeHandle, terminal *StatusSnapshot, attempts int) error {
	var err error
	for i := 0; i < attempts; i++ {
		if err = r.commitDrainObservationState(ctx, job, bounded, cursor, terminal); err == nil {
			return nil
		}
		if errors.Is(err, ErrDrainSequenceConflict) {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(r.boundedDrainRetryDelay(job, time.Duration(i+1)*100*time.Millisecond)):
		}
	}
	return err
}

func (r *Registry) boundedDrainRetryDelay(job *Job, requested time.Duration) time.Duration {
	if remaining, bounded := r.hardRemaining(job, r.nowFn()); bounded {
		if remaining <= 0 {
			return 0
		}
		if requested > remaining {
			return remaining
		}
	}
	return requested
}

// recordMonitorError degrades MonitorHealth; never touches execution state.
func (r *Registry) recordMonitorError(job *Job, now time.Time, err error) {
	job.mu.Lock()
	job.monitorErrs++
	if job.monitorErrs == 1 {
		job.monitorErrAt = now
	}
	prev := job.MonitorHealth
	if job.monitorErrs >= r.sched.DegradedAfter {
		job.MonitorHealth = MonitorHealthDegraded
	}
	wake := false
	if !job.monitorErrAt.IsZero() && now.Sub(job.monitorErrAt) >= r.sched.LostAfter {
		job.MonitorHealth = MonitorHealthLost
		wake = job.appendWakeLocked(now, WakeMonitorLost, strings.TrimSpace(err.Error()))
	}
	changed := prev != job.MonitorHealth
	job.mu.Unlock()
	if changed || wake {
		r.afterTransition(job)
	}
}

func (r *Registry) recordMonitorSuccess(job *Job, now time.Time) {
	job.mu.Lock()
	changed := job.MonitorHealth != MonitorHealthOK || job.monitorErrs != 0
	job.monitorErrs = 0
	job.monitorErrAt = time.Time{}
	job.MonitorHealth = MonitorHealthOK
	job.LastAccess = now
	job.mu.Unlock()
	if changed {
		r.afterTransition(job)
	}
}

// applySnapshot updates handle, telemetry, and non-terminal state from an
// observation. Returns true when the snapshot is terminal (the caller then
// finalizes). Telemetry is pass-through only.
func (r *Registry) applySnapshot(job *Job, snap StatusSnapshot, now time.Time) bool {
	adapterCaps := job.adapter.Capabilities()
	signals := signalsFromSnapshot(adapterCaps, snap)

	job.mu.Lock()
	if snap.UpdatedHandle != nil && !snap.UpdatedHandle.IsZero() && !adapterCaps.RequiresContinuousDrain {
		// Non-drain handle refinement. Drain cursors NEVER advance here:
		// they move only inside a successful atomic drain commit.
		job.Handle = cloneHandle(*snap.UpdatedHandle)
	}
	if job.Metadata == nil {
		job.Metadata = map[string]any{}
	}
	job.Metadata["provider_state"] = string(snap.State)
	if snap.RawState != "" {
		job.Metadata["provider_state_raw"] = snap.RawState
	}
	if snap.BytesProcessed != nil {
		job.Metadata["bytes_processed"] = *snap.BytesProcessed
	}
	if snap.RowsProcessed != nil {
		job.Metadata["rows_processed"] = *snap.RowsProcessed
	}
	if snap.SplitsDone != nil {
		job.Metadata["splits_done"] = *snap.SplitsDone
	}
	if snap.SplitsTotal != nil {
		job.Metadata["splits_total"] = *snap.SplitsTotal
	}
	if snap.NativeProgressPercent != nil {
		job.Metadata["native_progress_percent"] = *snap.NativeProgressPercent
	}
	for k, v := range snap.Telemetry {
		if strings.TrimSpace(k) != "" {
			job.Metadata[k] = v
		}
	}
	// Stall evidence requires actual comparable observations: a snapshot
	// with no extractable counters is not evidence of progress OR of a
	// stall (P1-5). The baseline starts at the first real observation.
	if len(signals) > 0 {
		switch {
		case len(job.lastSignals) == 0:
			job.lastSignals = signals
			job.lastProgressAt = now
			job.noProgressObs = 0
		case progressAdvanced(job.lastSignals, signals):
			job.lastSignals = signals
			job.lastProgressAt = now
			job.noProgressObs = 0
		default:
			job.noProgressObs++
		}
	}
	if snap.State == ProviderStateRunning && job.StartedAt == nil {
		t := now
		job.StartedAt = &t
	}
	if snap.State == ProviderStateRunning && job.Status == JobStatusSubmitted {
		job.transitionLocked(JobStatusRunning, now)
	}
	job.LastAccess = now
	terminal := snap.terminal()
	job.mu.Unlock()
	if !terminal {
		r.afterTransition(job)
	}
	return terminal
}

// applyTerminalStateLocked applies a terminal provider snapshot. Caller
// holds job.mu (write). Handles cancel-race outcomes and hard-budget
// expiry mapping.
func applyTerminalStateLocked(job *Job, snap StatusSnapshot, now time.Time) {
	switch snap.State {
	case ProviderStateSucceeded:
		if job.cancelWanted {
			job.CancelOutcome = CancelOutcomeSupersededByCompletion
		}
		job.transitionLocked(JobStatusCompleted, now)
		job.appendWakeLocked(now, WakeTerminal, "completed")
	case ProviderStateFailed:
		if job.cancelWanted {
			job.CancelOutcome = CancelOutcomeSupersededByFailure
		}
		if strings.TrimSpace(snap.Error) != "" {
			job.Error = strings.TrimSpace(snap.Error)
		} else if strings.TrimSpace(job.Error) == "" {
			job.Error = "query failed"
		}
		job.transitionLocked(JobStatusFailed, now)
		job.appendWakeLocked(now, WakeTerminal, job.Error)
	case ProviderStateCancelled:
		job.CancelOutcome = CancelOutcomeConfirmed
		if job.hardExpired {
			job.transitionLocked(JobStatusExpired, now)
			job.appendWakeLocked(now, WakeTerminal, "hard execution budget exceeded")
		} else {
			if strings.TrimSpace(job.Error) == "" {
				job.Error = "query cancelled"
			}
			job.transitionLocked(JobStatusCancelled, now)
			job.appendWakeLocked(now, WakeTerminal, "cancelled")
		}
	}
}

// finalizeFromSnapshot completes a terminal observation: fetches results
// (non-drain providers), materializes, and applies the terminal state.
func (r *Registry) finalizeFromSnapshot(job *Job, snap StatusSnapshot, assembler *resultAssembler, caps ProviderCapabilities) {
	r.finalizeFromSnapshotContext(job.pollCtx, job, snap, assembler, caps)
}

func (r *Registry) finalizeFromSnapshotContext(ctx context.Context, job *Job, snap StatusSnapshot, assembler *resultAssembler, caps ProviderCapabilities) {
	r.finalizeFromSnapshotContextWithFetch(ctx, job, snap, assembler, caps, true)
}

// finalizeFromSnapshotContextWithFetch first accepts result pages consumed by
// the terminal observation, then continues at their native continuation. This
// is critical for providers such as Databricks where re-reading the statement
// can close an already-consumed result and make a second page-zero fetch fail.
func (r *Registry) finalizeFromSnapshotContextWithFetch(ctx context.Context, job *Job, snap StatusSnapshot, assembler *resultAssembler, caps ProviderCapabilities, fetchResults bool) {
	if fetchResults && snap.State == ProviderStateSucceeded && !caps.RequiresContinuousDrain {
		if snap.UpdatedHandle != nil && !snap.UpdatedHandle.IsZero() {
			job.mu.Lock()
			job.Handle = cloneHandle(*snap.UpdatedHandle)
			job.mu.Unlock()
		}
		cursor, shouldFetch := r.acceptObservedResultPages(ctx, job, assembler, snap.DrainedPages)
		if shouldFetch {
			if err := r.fetchResultsIntoAssembler(ctx, job, assembler, caps, cursor); err != nil {
				detail := "provider completed but result retrieval failed after bounded retries; linked recovery is required and the last cursor was retained for diagnosis: " + err.Error()
				if IsPermanentObservationLoss(err) {
					detail = "provider completed but its result can no longer be observed: " + err.Error()
				}
				r.orphanResultFetch(job, detail)
				return
			}
		}
	}

	now := r.nowFn()
	job.mu.Lock()
	applyTerminalStateLocked(job, snap, now)
	finalStatus := job.Status
	if finalStatus == JobStatusCompleted {
		job.Result = assembler.result()
		if r.materializer != nil {
			job.Materialization = MaterializationPartial // until Finalize succeeds
		} else {
			job.Materialization = assembler.state()
		}
	}
	owner := job.Owner
	id := job.ID
	job.mu.Unlock()

	if r.materializer != nil {
		var result *QueryResult
		if finalStatus == JobStatusCompleted {
			result = assembler.result()
		}
		mctx, mcancel := context.WithTimeout(ctx, r.sched.ProviderCallTimeout)
		mErr := r.materializer.Finalize(mctx, id, owner, result, assembler.summary())
		mcancel()
		job.mu.Lock()
		if mErr != nil {
			job.Materialization = MaterializationPartial
			if job.Metadata == nil {
				job.Metadata = map[string]any{}
			}
			job.Metadata["materialization_error"] = mErr.Error()
		} else {
			if job.Metadata != nil {
				delete(job.Metadata, "materialization_error")
			}
			if finalStatus == JobStatusCompleted {
				if assembler.committedTruncated {
					job.Materialization = MaterializationTruncated
				} else {
					job.Materialization = MaterializationComplete
				}
			}
		}
		job.mu.Unlock()
	}
	r.afterTransition(job)
	if finalStatus == JobStatusCompleted && caps.RequiresContinuousDrain {
		// The live assembler intentionally retains only rows observed by this
		// process after a restart. Once the completed record is durable, rebuild
		// the immutable result from every committed chunk so immediate callers
		// see the same full result that a later lazy restore would return.
		if loader, ok := r.materializer.(TerminalResultLoader); ok {
			lctx, lcancel := context.WithTimeout(ctx, r.sched.ProviderCallTimeout)
			loaded, summary, lErr := loader.LoadQueryResult(lctx, id, owner)
			lcancel()
			job.mu.Lock()
			if lErr == nil && loaded != nil {
				job.Result = loaded
				if job.Metadata != nil {
					delete(job.Metadata, "result_restore_error")
				}
				if summary.Truncated {
					job.Materialization = MaterializationTruncated
				} else {
					job.Materialization = MaterializationComplete
				}
			} else if lErr != nil {
				if job.Metadata == nil {
					job.Metadata = map[string]any{}
				}
				job.Metadata["result_restore_error"] = lErr.Error()
			}
			job.mu.Unlock()
		}
	}
}

// finalizeCancelConfirmed terminalizes a provider-confirmed cancellation.
func (r *Registry) finalizeCancelConfirmed(job *Job, detail string) {
	now := r.nowFn()
	job.mu.Lock()
	applyTerminalStateLocked(job, StatusSnapshot{State: ProviderStateCancelled, Error: detail}, now)
	job.mu.Unlock()
	r.afterTransition(job)
}

// acceptObservedResultPages appends pages already consumed by the terminal
// status request and returns the exact provider continuation to use. When the
// inline page is complete, shouldFetch is false and no provider call occurs.
func (r *Registry) acceptObservedResultPages(ctx context.Context, job *Job, assembler *resultAssembler, pages []ResultPage) (FetchCursor, bool) {
	job.mu.RLock()
	maxRows := job.Budgets.MaxResultRows
	maxBytes := job.Budgets.MaxResultBytes
	job.mu.RUnlock()

	cursor := FetchCursor{MaxRows: maxRows, MaxBytes: maxBytes}
	if len(pages) == 0 {
		return cursor, true
	}
	for _, page := range pages {
		bounded, more := assembler.accept(page)
		r.materializeChunk(ctx, job, bounded, false)
		next := strings.TrimSpace(page.NextPageToken)
		if !more {
			r.clearResultFetchError(job)
			return cursor, false
		}
		if next == "" || next == cursor.PageToken {
			r.clearResultFetchError(job)
			return cursor, false
		}
		cursor.PageToken = next
	}
	return cursor, strings.TrimSpace(cursor.PageToken) != ""
}

// fetchResultsIntoAssembler pulls result pages through the provider-native
// cursor up to the job's row/byte ceilings. Transient failures receive three
// context-aware attempts and persist the exact cursor before each wait. Any
// remaining error is returned so finalization enters recovery rather than
// exposing a misleading empty completed result.
func (r *Registry) fetchResultsIntoAssembler(ctx context.Context, job *Job, assembler *resultAssembler, caps ProviderCapabilities, cursor FetchCursor) error {
	job.mu.RLock()
	handle := cloneHandle(job.Handle)
	job.mu.RUnlock()

	for {
		var page ResultPage
		var err error
		for attempt := 1; attempt <= 3; attempt++ {
			fctx, fcancel := r.providerCallContext(ctx, job, false)
			page, err = job.adapter.Fetch(fctx, handle, cursor)
			fcancel()
			if err == nil {
				break
			}
			r.logfSafe("jobs: result fetch for %s failed (attempt %d/3): %v", job.ID, attempt, err)
			r.recordResultFetchFailure(job, cursor.PageToken, err)
			if IsPermanentObservationLoss(err) || attempt == 3 {
				return err
			}
			delay := r.sched.errorInterval(attempt, caps, RetryAfterFromError(err))
			if !waitResultFetchRetry(ctx, job, delay) {
				return err
			}
		}
		// Only the bounded portion is durably appended; clipping is
		// explicit via Truncated (P1-6).
		bounded, more := assembler.accept(page)
		r.materializeChunk(ctx, job, bounded, false)
		if page.NextPageToken == "" || page.NextPageToken == cursor.PageToken {
			r.clearResultFetchError(job)
			return nil
		}
		if !more {
			// Ceilings reached. The bounded durable result is authoritative;
			// exposing a consumable provider cursor here would bypass those
			// budgets and make retries non-idempotent.
			r.clearResultFetchError(job)
			return nil
		}
		cursor.PageToken = page.NextPageToken
	}
}

func (r *Registry) recordResultFetchFailure(job *Job, cursor string, err error) {
	job.mu.Lock()
	if job.Metadata == nil {
		job.Metadata = map[string]any{}
	}
	job.Metadata["result_fetch_error"] = err.Error()
	job.Metadata["result_cursor"] = cursor
	job.Metadata["result_recovery_required"] = true
	job.mu.Unlock()
	// Persist the exact continuation before waiting or retrying. If this process
	// is lost, recovery recognizes the interrupted consumable-result state and
	// chooses a linked replacement instead of replaying page zero.
	r.afterTransition(job)
}

func waitResultFetchRetry(ctx context.Context, job *Job, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-job.pollCtx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (r *Registry) orphanResultFetch(job *Job, detail string) {
	now := r.nowFn()
	job.mu.Lock()
	if strings.TrimSpace(detail) == "" {
		detail = "provider result retrieval requires recovery"
	}
	job.Error = detail
	job.transitionLocked(JobStatusOrphaned, now)
	job.appendWakeLocked(now, WakeRecoveryRequired, detail)
	job.mu.Unlock()
	r.afterTransition(job)
}

func (r *Registry) clearResultFetchError(job *Job) {
	job.mu.Lock()
	if job.Metadata != nil {
		delete(job.Metadata, "result_fetch_error")
		delete(job.Metadata, "result_recovery_required")
		delete(job.Metadata, "result_cursor")
	}
	job.mu.Unlock()
}

func (r *Registry) materializeChunk(ctx context.Context, job *Job, page ResultPage, capHard bool) {
	if r.materializer == nil {
		return
	}
	job.mu.RLock()
	id := job.ID
	owner := job.Owner
	job.mu.RUnlock()
	mctx, mcancel := r.providerCallContext(ctx, job, capHard)
	err := r.materializer.AppendChunk(mctx, id, owner, page)
	mcancel()
	if err != nil {
		r.logfSafe("jobs: materialize chunk for %s failed: %v", id, err)
		job.mu.Lock()
		if job.Metadata == nil {
			job.Metadata = map[string]any{}
		}
		job.Metadata["materialization_error"] = err.Error()
		if job.Materialization == MaterializationNone {
			job.Materialization = MaterializationPartial
		}
		job.mu.Unlock()
		return
	}
	job.mu.Lock()
	if job.Materialization == MaterializationNone {
		job.Materialization = MaterializationPartial
	}
	job.mu.Unlock()
}

// evaluateJobPolicy runs the budget/stall evaluation, updates PolicyHealth,
// emits wake events, and initiates the automatic hard-budget cancellation.
// Returns true when a hard cancel was initiated this tick.
func (r *Registry) evaluateJobPolicy(job *Job, caps ProviderCapabilities, now time.Time) bool {
	job.mu.Lock()
	state := ProviderStateUnknown
	if s, ok := job.Metadata["provider_state"].(string); ok {
		state = ProviderState(s)
	}
	in := policyInput{
		budgets:      job.Budgets,
		submittedAt:  remoteStartLocked(job),
		startedAt:    job.StartedAt,
		queued:       state == ProviderStateQueued || state == ProviderStatePending,
		lastProgress: job.lastProgressAt,
		// Declared capabilities alone are not evidence: stall requires a
		// real comparable baseline (P1-5).
		hasSignals:    len(job.lastSignals) > 0,
		observations:  job.noProgressObs,
		providerState: state,
	}
	verdict := evaluatePolicy(r.policy, in, now)
	changed := job.PolicyHealth != verdict.health
	job.PolicyHealth = verdict.health

	woke := false
	if verdict.wake != "" && verdict.wake != WakeTerminal {
		woke = job.appendWakeLocked(now, verdict.wake, verdict.wakeDetail)
	}

	hardCancel := false
	if verdict.hardCancel && !job.cancelWanted {
		job.cancelWanted = true
		job.hardExpired = true
		job.CancelReason = CancelReasonBudget
		if job.CancelStartedAt == nil {
			t := now
			job.CancelStartedAt = &t
			job.cancellingAt = t
		}
		if job.Status == JobStatusSubmitted || job.Status == JobStatusRunning {
			job.transitionLocked(JobStatusCancelRequested, now)
		}
		job.nudgeLocked()
		hardCancel = true
	}
	job.mu.Unlock()

	if changed || woke || hardCancel {
		r.afterTransition(job)
	}
	return hardCancel
}

// sleepMonitor waits for the next tick, a nudge (cancel request), lease
// loss (done), or shutdown. A nil done channel never fires.
func (r *Registry) sleepMonitor(job *Job, d time.Duration, done <-chan struct{}) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-job.pollCtx.Done():
	case <-done:
	case <-job.wakeSignal:
	case <-timer.C:
	}
}

func (r *Registry) sleepMonitorContext(job *Job, ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	job.mu.RLock()
	status := job.Status
	cancelStarted := cloneTimePtr(job.CancelStartedAt)
	job.mu.RUnlock()
	if remaining, bounded := r.hardRemaining(job, r.nowFn()); bounded {
		if remaining > 0 {
			if d > remaining {
				d = remaining
			}
		} else {
			// At/after the hard deadline only an actual cancellation lifecycle
			// may wait, and never beyond its durable unconfirmed grace.
			if (status != JobStatusCancelRequested && status != JobStatusCancelling) || cancelStarted == nil {
				return
			}
			graceRemaining := r.sched.CancelUnconfirmedAfter - r.nowFn().Sub(*cancelStarted)
			if graceRemaining <= 0 {
				return
			}
			if d > graceRemaining {
				d = graceRemaining
			}
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-job.pollCtx.Done():
	case <-job.wakeSignal:
	case <-timer.C:
	}
}
