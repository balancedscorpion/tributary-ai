package jobs

import (
	"context"
	"strings"
	"time"
)

// TerminalResultLoader reconstructs the immutable materialized result of a
// TERMINAL job from its durable chunks — never by re-executing SQL. The
// artifact-backed Materializer implements it (optional, type-asserted) so
// a restarted process can serve get_query_results/validation against jobs
// that finished under a previous process.
type TerminalResultLoader interface {
	LoadQueryResult(ctx context.Context, jobID string, owner JobOwner) (*QueryResult, MaterializationSummary, error)
}

// lookupJob returns the in-memory job, lazily restoring it from the
// durable store on a miss. Authorization is enforced BEFORE any state is
// reconstructed: a caller with the wrong owner token observes exactly what
// it would for a nonexistent job.
func (r *Registry) lookupJob(id string, owner JobOwner) *Job {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	r.mu.RLock()
	job := r.jobs[id]
	r.mu.RUnlock()
	if job != nil {
		return job
	}
	return r.ensureLoaded(id, owner)
}

// ensureLoaded restores a persisted record into memory (restart survival
// for check/get/validate/cancel/claim). It NEVER starts monitors, pollers,
// or submissions — driving ownership belongs exclusively to Rehydrate and
// the monitor lease. Terminal completed jobs additionally get their
// immutable result loaded through the TerminalResultLoader when available.
func (r *Registry) ensureLoaded(id string, owner JobOwner) *Job {
	if r == nil || r.store == nil {
		return nil
	}
	owner = normalizeOwner(owner)
	if owner.ThreadID == "" || owner.Handle == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	rec, found, err := r.store.GetQueryJob(ctx, id)
	cancel()
	if err != nil {
		r.logfSafe("jobs: lazy restore of %s failed: %v", id, err)
		return nil
	}
	if !found {
		return nil
	}
	// Owner-scoped restore: do not reconstruct state for callers that
	// could not observe the job anyway.
	if !ownerMatches(rec.Owner, owner) {
		return nil
	}

	job := r.jobFromRecord(rec, r.adapterFor(rec.Provider))
	if isTerminalStatus(rec.Status) {
		// Capacity for terminal jobs was released when they terminalized;
		// never re-release from a lazy restore.
		job.capacityReleased = true
		if rec.Status == JobStatusCompleted && job.Result == nil {
			if loader, ok := r.materializer.(TerminalResultLoader); ok {
				lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Second)
				result, summary, lErr := loader.LoadQueryResult(lctx, id, rec.Owner)
				lcancel()
				switch {
				case lErr != nil:
					if job.Metadata == nil {
						job.Metadata = map[string]any{}
					}
					job.Metadata["result_restore_error"] = lErr.Error()
				case result != nil:
					job.Result = result
					if job.Metadata != nil {
						delete(job.Metadata, "result_restore_error")
					}
					if job.Materialization == "" || job.Materialization == MaterializationNone {
						if summary.Truncated {
							job.Materialization = MaterializationTruncated
						} else {
							job.Materialization = MaterializationComplete
						}
					}
				}
			}
		}
	}

	r.mu.Lock()
	if existing := r.jobs[id]; existing != nil {
		r.mu.Unlock()
		job.pollStop()
		return existing
	}
	r.jobs[id] = job
	r.mu.Unlock()
	r.notifyAll()
	return job
}

// refreshJobFromStore merges externally persisted changes into a job this
// process is NOT driving (cross-process barrier visibility). It is a
// read-only reconciliation: it never persists, never starts a monitor or
// submission watch, and never re-executes SQL. Locally driven jobs are
// never touched — their goroutine's state is authoritative and its own
// transitions wake local waiters. Returns true when anything changed.
func (r *Registry) refreshJobFromStore(job *Job, owner JobOwner) bool {
	if r == nil || r.store == nil || job == nil {
		return false
	}
	job.mu.RLock()
	id := job.ID
	driving := job.driving
	localRev := job.storeRevision
	job.mu.RUnlock()
	if driving {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	stored, found, err := r.store.GetQueryJob(ctx, id)
	cancel()
	if err != nil || !found {
		return false
	}
	// Strict owner scope, re-checked against the durable record.
	if !ownerMatches(stored.Owner, normalizeOwner(owner)) {
		return false
	}
	if stored.Revision <= localRev {
		return false
	}

	job.mu.Lock()
	if job.driving || stored.Revision <= job.storeRevision {
		job.mu.Unlock()
		return false
	}
	applyRecordLocked(job, stored, r.nowFn())
	becameTerminal := isTerminalStatus(job.Status)
	needLoad := job.Status == JobStatusCompleted && job.Result == nil
	stop := job.pollStop
	job.mu.Unlock()

	if becameTerminal {
		// Capacity for externally terminalized jobs was released by the
		// driving process; never re-release here.
		job.mu.Lock()
		job.capacityReleased = true
		job.mu.Unlock()
		if stop != nil {
			stop()
		}
	}
	if needLoad {
		if loader, ok := r.materializer.(TerminalResultLoader); ok {
			lctx, lcancel := context.WithTimeout(context.Background(), 5*time.Second)
			result, _, lErr := loader.LoadQueryResult(lctx, id, stored.Owner)
			lcancel()
			if lErr == nil && result != nil {
				job.mu.Lock()
				job.Result = result
				job.mu.Unlock()
			}
		}
	}
	r.notifyAll()
	return true
}

// applyRecordLocked overwrites a non-driven job's observable state from
// its durable record. Caller holds job.mu (write). Runtime driver fields
// (assembler, backlog, poll machinery) are untouched — there is no local
// driver by contract.
func applyRecordLocked(job *Job, rec JobRecord, now time.Time) {
	job.Status = rec.Status
	job.Error = rec.Error
	job.Handle = cloneHandle(rec.Handle)
	job.DrainSeq = rec.DrainSeq
	job.DrainRowsCommitted = rec.DrainRowsCommitted
	job.DrainBytesCommitted = rec.DrainBytesCommitted
	job.DrainTruncated = rec.DrainTruncated
	job.SubmitDispatched = rec.SubmitDispatched
	job.RemoteSubmittedAt = cloneTimePtr(rec.RemoteSubmittedAt)
	job.NextPollAt = cloneTimePtr(rec.NextPollAt)
	job.NextPollAction = rec.NextPollAction
	job.MonitorHealth = rec.MonitorHealth
	job.PolicyHealth = rec.PolicyHealth
	job.Materialization = rec.MaterializationState
	job.CancelReason = rec.CancelReason
	job.CancelOutcome = rec.CancelOutcome
	job.CancelStartedAt = cloneTimePtr(rec.CancelStartedAt)
	job.Validation = rec.ValidationState
	job.ValidationDetail = rec.ValidationDetail
	job.Profile = cloneQueryProfile(rec.Profile)
	job.ProfileOutcome = rec.ProfileOutcome
	job.ProfileHandle = cloneHandle(rec.ProfileHandle)
	job.ProfilePhase = rec.ProfilePhase
	job.ProfileDispatched = rec.ProfileDispatched
	job.ArtifactID = rec.ArtifactID
	job.Metadata = cloneMap(rec.Metadata)
	job.WakeEvents = append([]WakeEvent(nil), rec.WakeEvents...)
	job.emittedWakes = map[WakeKind]bool{}
	for _, ev := range rec.WakeEvents {
		job.emittedWakes[ev.Kind] = true
	}
	if rec.StartedAt != nil {
		t := *rec.StartedAt
		job.StartedAt = &t
	}
	if rec.CompletedAt != nil {
		t := *rec.CompletedAt
		job.CompletedAt = &t
	}
	job.storeRevision = rec.Revision
	if rec.Status == JobStatusCancelRequested || rec.Status == JobStatusCancelling {
		job.cancelWanted = true
		if rec.CancelStartedAt != nil {
			job.cancellingAt = *rec.CancelStartedAt
		} else {
			job.cancellingAt = now
		}
	}
	job.hardExpired = rec.CancelReason == CancelReasonBudget
	job.LastAccess = now
	if job.MonitorHealth == "" {
		job.MonitorHealth = MonitorHealthOK
	}
	if job.Validation == "" {
		job.Validation = ValidationUnvalidated
	}
}
