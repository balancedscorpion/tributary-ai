package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrAdapterUnavailable     = errors.New("no provider adapter registered for this provider")
	ErrSubmissionUnknown      = errors.New("submission outcome unknown; reconcile before retrying")
	ErrCancelReasonRequired   = errors.New("cancel reason is required")
	ErrUseMaterializedResults = errors.New("results for this provider are materialized durably; read them from the artifact store")
	ErrNotReconcilable        = errors.New("job is not awaiting submission reconciliation")
	// ErrDrainCommitUnsupported: RequiresContinuousDrain providers may only
	// run when the configured Materializer implements DrainCommitter —
	// without atomic chunk+cursor+record commits a crash can replay a
	// consumed cursor and duplicate rows. No best-effort fallback exists.
	ErrDrainCommitUnsupported = errors.New("continuous-drain provider requires an atomic DrainCommitter materializer")
	// ErrInitialDurabilityFailure: the submit succeeded but its initial
	// observation could not be committed durably; the query is being
	// cancelled through the CONFIRMED lifecycle (it is never marked failed
	// while the remote query may still run).
	ErrInitialDurabilityFailure = errors.New("initial durable commit failed; cancelling the query through the confirmed lifecycle")
	// ErrInvalidValidationState guards the validation enum.
	ErrInvalidValidationState = errors.New("invalid validation state")
	// ErrIdempotencyMismatch: the caller-supplied idempotency key already
	// belongs to a durable job whose identity (provider, connection, SQL,
	// owner thread/run, ReplacesJobID, owner token) does not match this
	// request exactly. Fail closed: neither reuse nor resubmit.
	ErrIdempotencyMismatch = errors.New("idempotency key already used by a non-matching query")
	// ErrDriverLeaseUnavailable means no provider submit occurred: the
	// durable reservation could not be claimed for the submit->monitor
	// handoff, so callers must retain the job identity and let recovery decide.
	ErrDriverLeaseUnavailable = errors.New("query driver lease unavailable before submit")
	// ErrDriverHandoffPending means provider submission was dispatched or
	// confirmed, but the local process could not complete the durable monitor
	// handoff. The original job remains authoritative and recoverable; callers
	// must wait/poll it and must never launch a replacement from this error.
	ErrDriverHandoffPending = errors.New("query submitted; durable driver handoff pending")
	// ErrQueryObligationBinding means the durable job winner could not be
	// attached to its run-scoped completion obligation. Profile and Submit were
	// not called for a fresh winner; callers should wait/reconcile the existing
	// obligation rather than creating another replacement.
	ErrQueryObligationBinding = errors.New("query obligation binding failed before provider work")
)

// submitNotDeliveredError marks a submit failure where the request
// provably never reached the provider (connection refused, local
// validation, DNS). Adapters wrap such errors with SubmitNotDelivered so
// the registry knows a retry cannot double-execute. Any other submit error
// is treated as ambiguous (invariant I1: never blind-retry).
type submitNotDeliveredError struct{ err error }

func (e *submitNotDeliveredError) Error() string { return e.err.Error() }
func (e *submitNotDeliveredError) Unwrap() error { return e.err }

// SubmitNotDelivered wraps an error to assert the submit request was never
// delivered to the provider.
func SubmitNotDelivered(err error) error {
	if err == nil {
		return nil
	}
	return &submitNotDeliveredError{err: err}
}

func isSubmitNotDelivered(err error) bool {
	var nd *submitNotDeliveredError
	return errors.As(err, &nd)
}

// LaunchRequest starts a managed long-horizon query.
type LaunchRequest struct {
	Owner         JobOwner
	Provider      string
	Adapter       ProviderAdapter // optional override; default: registered adapter
	ConnectionID  string
	SQL           string
	SQLPreview    string
	Metadata      map[string]any
	Budgets       Budgets           // zero fields inherit policy defaults
	Session       map[string]string // provider session properties
	ReplacesJobID string            // linkage for replace/recovery submissions
	ScanApproval  ScanApprovalFunc  // optional one-time approval for profiled scan estimates
	// KeepOnRunCompletion defaults to true: managed jobs survive run
	// completion/stop (invariant I5). Set to disable for short-lived jobs
	// that should participate in the legacy sweep.
	KeepOnRunCompletion *bool
	IdempotencyKey      string // optional; minted when empty
}

// transitionLocked validates and applies a state change. Caller holds
// job.mu (write). Terminal transitions stamp CompletedAt.
func (j *Job) transitionLocked(to JobStatus, now time.Time) bool {
	if !canTransition(j.Status, to) {
		return false
	}
	j.Status = to
	j.LastAccess = now
	if isTerminalStatus(to) && j.CompletedAt == nil {
		j.CompletedAt = &now
	}
	return true
}

// nudgeLocked wakes the monitor loop without waiting for its next tick.
func (j *Job) nudgeLocked() {
	if j.wakeSignal == nil {
		return
	}
	select {
	case j.wakeSignal <- struct{}{}:
	default:
	}
}

func newIdempotencyKey() (string, error) {
	tok, err := GenerateOwnerToken()
	if err != nil {
		return "", err
	}
	return "idem_" + strings.TrimPrefix(tok, ownerTokenPrefix), nil
}

// Launch reserves, persists, submits, and begins monitoring a managed
// query. Ordering is load-bearing (invariant I1): the reserved record is
// durable before the remote submit; an ambiguous submit yields
// (job, ErrSubmissionUnknown) and is never blind-retried.
func (r *Registry) Launch(ctx context.Context, req LaunchRequest) (*Job, error) {
	if r == nil {
		return nil, errors.New("job registry is not configured")
	}
	owner := normalizeOwner(req.Owner)
	if err := validateOwnerForRegister(owner); err != nil {
		return nil, err
	}
	if managed, _ := req.Metadata[QueryObligationManagedMetadataKey].(bool); managed && owner.RunID == "" {
		return nil, errors.New("run_id is required for a managed query obligation")
	}
	adapter := req.Adapter
	provider := strings.TrimSpace(strings.ToLower(req.Provider))
	if adapter == nil {
		adapter = r.adapterFor(provider)
	}
	if adapter == nil {
		return nil, ErrAdapterUnavailable
	}
	if provider == "" {
		provider = strings.TrimSpace(strings.ToLower(adapter.Provider()))
	}
	caps := adapter.Capabilities()
	if caps.RequiresContinuousDrain {
		if _, ok := r.materializer.(DrainCommitter); !ok {
			return nil, ErrDrainCommitUnsupported
		}
		if _, leased := r.store.(LeaseStore); leased {
			if _, fenced := r.materializer.(FencedDrainCommitter); !fenced {
				return nil, fmt.Errorf("%w: leased continuous-drain jobs require FencedDrainCommitter", ErrDrainCommitUnsupported)
			}
		}
	}
	connKeyEarly := connectionKey(provider, req.ConnectionID)

	// Replay-safe recovery: a CALLER-SUPPLIED idempotency key may already
	// belong to a durable job from a prior Launch (same process or not).
	// Recover it — original job, original owner token — before reserving
	// anything, submitting anything, or minting a new owner.
	idem := strings.TrimSpace(req.IdempotencyKey)
	callerKey := idem != ""
	if callerKey {
		if job, err, handled := r.recoverIdempotent(ctx, req, owner, provider, connKeyEarly, idem); handled {
			return job, err
		}
	}

	// callerHandle is what the CALLER asserted (may be empty); the minted
	// token below is ours and must never participate in idempotent
	// identity matching.
	callerHandle := owner.Handle
	if owner.Handle == "" {
		token, err := GenerateOwnerToken()
		if err != nil {
			return nil, err
		}
		owner.Handle = token
	}
	id, err := newJobID(provider)
	if err != nil {
		return nil, err
	}
	if idem == "" {
		if idem, err = newIdempotencyKey(); err != nil {
			return nil, err
		}
	}
	keep := true
	if req.KeepOnRunCompletion != nil {
		keep = *req.KeepOnRunCompletion
	}
	budgets := req.Budgets.withDefaults(r.policy.DefaultBudgets)
	connKey := connectionKey(provider, req.ConnectionID)

	// Profiling is a first-class durable phase: when the adapter can
	// profile, the job is reserved directly in JobStatusProfiling so the
	// dry-run/EXPLAIN work is admission-controlled, idempotency-recoverable,
	// and cancellable — and the SAME slot carries through to submission.
	_, _, wantsProfile := adapterProfiler(adapter)
	initialStatus := JobStatusReserved
	if wantsProfile {
		initialStatus = JobStatusProfiling
		// Crash/lease safety gate: a durable store with lease semantics
		// MUST offer fenced profile mutations, or profiling cannot be made
		// safe. Fail closed before reserving anything or calling Profile.
		if r.store != nil {
			if _, hasLease := r.store.(LeaseStore); hasLease {
				if _, hasFence := r.store.(FencedProfileStore); !hasFence {
					return nil, ErrProfileFenceUnsupported
				}
			}
		}
	}

	buildJob := func(now time.Time) *Job {
		pollCtx, pollStop := context.WithCancel(context.Background())
		return &Job{
			ID:                  id,
			Provider:            provider,
			ConnectionID:        strings.TrimSpace(req.ConnectionID),
			SQL:                 strings.TrimSpace(req.SQL),
			SQLPreview:          strings.TrimSpace(req.SQLPreview),
			Status:              initialStatus,
			SubmittedAt:         now,
			LastAccess:          now,
			Metadata:            cloneMap(req.Metadata),
			Owner:               owner,
			IdempotencyKey:      idem,
			ReplacesJobID:       strings.TrimSpace(req.ReplacesJobID),
			KeepOnRunCompletion: keep,
			Budgets:             budgets,
			MonitorHealth:       MonitorHealthOK,
			PolicyHealth:        PolicyHealthOK,
			Materialization:     MaterializationNone,
			Validation:          ValidationUnvalidated,
			managed:             true,
			adapter:             adapter,
			wakeSignal:          make(chan struct{}, 1),
			pollCtx:             pollCtx,
			pollStop:            pollStop,
		}
	}

	// Admission: take a slot or queue (bounded) for one. With a
	// CapacityReserver the grant is transactional and cross-process, and
	// the granting write IS the reserve-before-submit persist (never
	// double-written via plain CAS). A woken waiter that loses the
	// capacity race re-queues at the FRONT so eligibility wakeups stay
	// fair; cross-process releases are picked up on a retry cadence.
	reserver, hasReserver := r.store.(CapacityReserver)
	limits := r.admission.cfg.capacityLimits()
	var job *Job
	requeueFront := false
	// One absolute admission deadline spans every cross-process retry. A new
	// timer per retry would reset the TTL forever under sustained contention.
	admissionDeadline := time.Now().Add(r.admission.cfg.ReservationTTL)
	for {
		now := r.nowFn()
		if hasReserver {
			if job == nil {
				job = buildJob(now)
			}
			job.mu.RLock()
			rec := recordFromJobLocked(job)
			job.mu.RUnlock()
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
			granted, newRev, rErr := reserver.ReserveQueryCapacity(rctx, rec, limits)
			rcancel()
			if rErr != nil {
				// The losing local candidate was never registered,
				// persisted, or submitted: discard it quietly (stop its
				// poll context, no terminalization) regardless of outcome.
				job.pollStop()
				if callerKey && errors.Is(rErr, ErrRevisionConflict) {
					// Duplicate-idempotency-key reservation race: the store
					// enforced uniqueness and a winner already exists.
					// Recover it — original job, original owner — leaving
					// the durable winner untouched.
					matchOwner := owner
					matchOwner.Handle = callerHandle // minted token never matches
					if winner, wErr, handled := r.recoverIdempotent(ctx, req, matchOwner, provider, connKeyEarly, idem); handled {
						return winner, wErr
					}
					// Conflict without a recoverable winner (no lookup
					// support, or the record vanished): fail closed.
					return nil, fmt.Errorf("idempotency reservation conflict but the winning job could not be recovered: %w", rErr)
				}
				// Non-idempotency conflicts and other reserver failures:
				// fail closed, nothing was submitted.
				return nil, fmt.Errorf("capacity reservation failed before submit: %w", rErr)
			}
			if granted {
				job.mu.Lock()
				job.storeRevision = newRev
				job.reservedViaStore = true
				job.mu.Unlock()
				r.mu.Lock()
				r.jobs[id] = job
				r.mu.Unlock()
				r.notifyAll()
				break
			}
		} else {
			r.mu.Lock()
			if r.hasManagedCapacityLocked(owner.ThreadID, connKey, wantsProfile) {
				job = buildJob(now)
				r.jobs[id] = job
				r.mu.Unlock()
				break
			}
			r.mu.Unlock()
		}
		waiter, el, qErr := r.admission.enqueue(owner.ThreadID, connKey, wantsProfile, requeueFront)
		if qErr != nil {
			if job != nil {
				job.pollStop()
			}
			return nil, qErr
		}
		remainingAdmission := time.Until(admissionDeadline)
		if remainingAdmission <= 0 {
			r.admission.remove(el)
			if job != nil {
				job.pollStop()
			}
			return nil, ErrAdmissionTimeout
		}
		ttl := time.NewTimer(remainingAdmission)
		var retryCh <-chan time.Time
		var retry *time.Timer
		if hasReserver {
			retry = time.NewTimer(r.admission.cfg.CrossProcessRetryInterval)
			retryCh = retry.C
		}
		stopTimers := func() {
			ttl.Stop()
			if retry != nil {
				retry.Stop()
			}
		}
		select {
		case <-ctx.Done():
			stopTimers()
			r.admission.remove(el)
			if job != nil {
				job.pollStop()
			}
			return nil, ctx.Err()
		case <-ttl.C:
			stopTimers()
			r.admission.remove(el)
			if job != nil {
				job.pollStop()
			}
			return nil, ErrAdmissionTimeout
		case <-waiter.ready:
			stopTimers()
			requeueFront = true
		case <-retryCh:
			stopTimers()
			r.admission.remove(el)
			requeueFront = true
		}
	}

	// Reserve-before-submit: with a store configured, the durable record
	// is mandatory before any remote call. When the CapacityReserver
	// granted, its transactional write already persisted the reservation.
	if !job.reservedViaStore {
		if err := r.persistJobStrict(ctx, job); err != nil {
			now := r.nowFn()
			job.mu.Lock()
			job.Error = "failed to persist reservation: " + err.Error()
			job.transitionLocked(JobStatusFailed, now)
			job.mu.Unlock()
			job.pollStop()
			r.afterTransition(job)
			return nil, fmt.Errorf("reserve-before-submit persistence failed: %w", err)
		}
	}
	r.notifyAll()

	// Reservation-race recovery: two launches can pass the pre-reserve
	// lookup simultaneously and both persist a reservation. Now that OUR
	// reservation is durable, re-look-up: if the store's deterministic
	// winner is a different job, abort ours (before any remote submit) and
	// recover the winner — submit count for the key stays exactly one.
	if callerKey && r.store != nil {
		matchOwner := owner
		matchOwner.Handle = callerHandle // only the caller's asserted token matches
		if winner, wErr, handled := r.adoptIdempotentWinner(ctx, job, req, matchOwner, provider, connKeyEarly, idem); handled {
			return winner, wErr
		}
	}

	// The reservation and idempotency race are now durable and this process
	// owns the authoritative winner. Bind that exact job to its run-scoped
	// obligation before profiling or provider submission. This closes the
	// crash/race window where a remotely submitted successor could otherwise
	// exist without replacing the predecessor in the completion ledger.
	if err := r.bindQueryObligation(ctx, job.ID); err != nil {
		detail := "failed to bind durable query obligation before provider work: " + err.Error()
		r.abortOwnPreSubmit(job, CancelReasonDurability, detail)
		return cloneJob(job), fmt.Errorf("%w: %v", ErrQueryObligationBinding, err)
	}

	subReq := SubmitRequest{
		SQL:            job.SQL,
		ConnectionID:   job.ConnectionID,
		IdempotencyKey: idem,
		Budgets:        budgets,
		Session:        req.Session,
		Metadata:       cloneMap(req.Metadata),
	}

	// Bounded pre-submit profiling on the SAME durable slot: acceptance
	// transitions profiling -> reserved with the profile persisted;
	// rejection terminalizes typed and Submit is never reached. This runs
	// BEFORE the dispatch marker — a crash during profiling must read as a
	// profiling record, never as an ambiguous submit.
	if wantsProfile {
		if pErr := r.runProfilePhase(ctx, job, adapter, caps, &subReq, req.ScanApproval); pErr != nil {
			return cloneJob(job), pErr
		}
	}

	// The submit boundary itself is leased. Without this, another process can
	// observe reserved+dispatched while the original HTTP call is still in
	// flight and issue reconciliation I/O concurrently. Profiling already
	// holds this lease; Acquire by the same holder simply confirms the handoff.
	leaseStore, claimed := r.claimDriver(job)
	if !claimed {
		return cloneJob(job), ErrDriverLeaseUnavailable
	}
	driverCtx, stopHeartbeat := r.leaseHeartbeat(job.pollCtx, leaseStore, job.ID)
	finishDriver := func() {
		stopHeartbeat()
		r.releaseDriver(job, leaseStore)
	}
	handoffMonitor := func() bool {
		stopHeartbeat()
		if !r.confirmDriverLease(job, leaseStore) {
			r.releaseDriver(job, leaseStore)
			return false
		}
		go r.runManagedMonitor(job, leaseStore)
		return true
	}
	handoffSubmissionWatch := func() bool {
		stopHeartbeat()
		if !r.confirmDriverLease(job, leaseStore) {
			r.releaseDriver(job, leaseStore)
			return false
		}
		go r.runSubmissionWatch(job, leaseStore)
		return true
	}

	// Dispatch marker: durable BEFORE the remote call, so a crash inside
	// the submit window rehydrates as ambiguous (possibly consumed initial
	// page), never as a safe expiry.
	job.mu.Lock()
	job.SubmitDispatched = true
	remoteSubmittedAt := r.nowFn()
	job.RemoteSubmittedAt = &remoteSubmittedAt
	job.assembler = newResultAssembler(budgets)
	job.mu.Unlock()
	if err := r.persistJobStrict(ctx, job); err != nil {
		now := r.nowFn()
		job.mu.Lock()
		job.Error = "failed to persist dispatch marker: " + err.Error()
		job.transitionLocked(JobStatusFailed, now)
		job.mu.Unlock()
		job.pollStop()
		r.afterTransition(job)
		finishDriver()
		return nil, fmt.Errorf("dispatch-marker persistence failed: %w", err)
	}

	submitCtx, submitCancel := contextWithEither(ctx, driverCtx)
	result, submitErr := r.submitWithRetries(submitCtx, job, adapter, caps, subReq)
	submitCancel()
	now := r.nowFn()
	if submitErr != nil {
		job.mu.Lock()
		job.Error = strings.TrimSpace(submitErr.Error())
		if isSubmitNotDelivered(submitErr) {
			// Provably never reached the provider: safe hard failure.
			job.transitionLocked(JobStatusFailed, now)
			job.mu.Unlock()
			job.pollStop()
			r.afterTransition(job)
			finishDriver()
			return cloneJob(job), fmt.Errorf("submit failed: %w", submitErr)
		}
		// Ambiguous: it may be running remotely. Never blind-retry.
		job.transitionLocked(JobStatusSubmissionUnknown, now)
		job.appendWakeLocked(now, WakeSubmissionUnknown, job.Error)
		job.mu.Unlock()
		r.afterTransition(job)
		// A durable watch keeps trying to reconcile (when the adapter can)
		// and enforces the hard budget so the admission slot is never
		// consumed forever.
		if !handoffSubmissionWatch() {
			return cloneJob(job), fmt.Errorf("%w: submission outcome requires recovery", ErrDriverHandoffPending)
		}
		return cloneJob(job), ErrSubmissionUnknown
	}

	job.mu.Lock()
	job.Handle = cloneHandle(result.Handle)
	job.transitionLocked(JobStatusSubmitted, now)
	if result.Initial != nil && !result.Initial.terminal() && result.Initial.RetryAfter > 0 {
		deadline := now.Add(r.sched.nextInterval(0, caps, result.Initial.RetryAfter))
		job.NextPollAt = &deadline
		job.NextPollAction = RetryActionPoll
	}
	job.mu.Unlock()

	if caps.RequiresContinuousDrain {
		// The submit response IS the first observation (Trino's initial
		// POST page). Commit it atomically — bounded page + post-page
		// cursor + DrainSeq + record — BEFORE the monitor starts. Every
		// durable drain record therefore has DrainSeq >= 1; rehydration
		// treats DrainSeq == 0 as "initial observation lost".
		if err := r.commitInitialDrain(driverCtx, job, result.Initial); err != nil {
			// The remote query may be live but its durable anchor failed:
			// a drain stream without one can never be resumed or deduped.
			// Never mark failed on an unconfirmed best-effort cancel —
			// drive the CONFIRMED cancellation lifecycle instead, with a
			// durable pathology diagnostic. The job stays honestly
			// non-terminal until the provider confirms.
			now = r.nowFn()
			job.mu.Lock()
			job.cancelWanted = true
			job.CancelReason = CancelReasonDurability
			if job.Metadata == nil {
				job.Metadata = map[string]any{}
			}
			job.Metadata["materialization_error"] = err.Error()
			job.appendWakeLocked(now, WakePathology, "initial durable commit failed: "+err.Error())
			job.transitionLocked(JobStatusCancelRequested, now)
			job.nudgeLocked()
			job.mu.Unlock()
			r.afterTransition(job)
			if !handoffMonitor() {
				return cloneJob(job), fmt.Errorf("%w: initial durability cancellation requires recovery", ErrDriverHandoffPending)
			}
			return cloneJob(job), fmt.Errorf("%w: %v", ErrInitialDurabilityFailure, err)
		}
		if result.Initial != nil && result.Initial.terminal() {
			r.finalizeFromSnapshotContext(driverCtx, job, *result.Initial, job.assembler, caps)
			job.pollStop()
			finishDriver()
			return cloneJob(job), nil
		}
		r.notifyAll()
		if !handoffMonitor() {
			return cloneJob(job), ErrDriverHandoffPending
		}
		return cloneJob(job), nil
	}

	r.afterTransition(job)
	if result.Initial != nil {
		// Inline first observation from a non-drain provider (e.g. a
		// wait_timeout fast path). A terminal observation is finalized as one
		// unit so its consumed inline page can hand off directly to any native
		// continuation without restarting Fetch at page zero.
		if result.Initial.terminal() {
			_ = r.applySnapshot(job, *result.Initial, r.nowFn())
			r.finalizeFromSnapshotContext(driverCtx, job, *result.Initial, job.assembler, caps)
			job.pollStop()
			finishDriver()
			return cloneJob(job), nil
		}
		for _, page := range result.Initial.DrainedPages {
			bounded, _ := job.assembler.accept(page)
			r.materializeChunk(driverCtx, job, bounded, true)
		}
		_ = r.applySnapshot(job, *result.Initial, r.nowFn())
	}
	if !handoffMonitor() {
		return cloneJob(job), ErrDriverHandoffPending
	}
	return cloneJob(job), nil
}

// recoverIdempotent resolves a caller-supplied idempotency key against the
// durable store BEFORE any reservation or submit. When a prior job exists
// and matches this request exactly, it is returned with its ORIGINAL owner
// capability — never resubmitted, never re-monitored (driving resumes only
// through Rehydrate/lease rules), never given a manufactured owner.
// handled=false means no prior job exists and Launch proceeds normally.
func (r *Registry) recoverIdempotent(ctx context.Context, req LaunchRequest, owner JobOwner, provider, connKey, idem string) (*Job, error, bool) {
	if r.store == nil {
		return nil, nil, false
	}
	lookup, ok := r.store.(IdempotencyLookupStore)
	if !ok {
		return nil, nil, false
	}
	lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	rec, found, err := lookup.GetQueryJobByIdempotency(lookupCtx, provider, connKey, idem)
	cancel()
	if err != nil {
		// Fail closed: proceeding could double-submit the warehouse query.
		return nil, fmt.Errorf("idempotency lookup failed (refusing to risk a duplicate submit): %w", err), true
	}
	if !found {
		return nil, nil, false
	}
	if mErr := idempotentMatch(rec, req, owner, provider, connKey); mErr != nil {
		return nil, mErr, true
	}
	return r.returnRecoveredJob(ctx, rec)
}

// adoptIdempotentWinner is the post-reservation race check: with OUR
// reservation durable, the store's deterministic winner for the key is
// authoritative. If it is not us, our reservation is aborted (nothing was
// submitted) and the winner is recovered instead.
func (r *Registry) adoptIdempotentWinner(ctx context.Context, ours *Job, req LaunchRequest, owner JobOwner, provider, connKey, idem string) (*Job, error, bool) {
	lookup, ok := r.store.(IdempotencyLookupStore)
	if !ok {
		return nil, nil, false
	}
	lookupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	rec, found, err := lookup.GetQueryJobByIdempotency(lookupCtx, provider, connKey, idem)
	cancel()
	if err != nil {
		// Fail closed pre-submit: abort ours rather than risk a duplicate.
		r.abortOwnPreSubmit(ours, CancelReasonSuperseded, "idempotency race check failed: "+err.Error())
		return nil, fmt.Errorf("idempotency race check failed (reservation aborted, nothing submitted): %w", err), true
	}
	if !found || rec.ID == ours.ID {
		return nil, nil, false // we are the winner (or the only entrant)
	}
	if mErr := idempotentMatch(rec, req, owner, provider, connKey); mErr != nil {
		r.abortOwnPreSubmit(ours, CancelReasonSuperseded, "idempotency winner mismatch")
		return nil, mErr, true
	}
	r.abortOwnPreSubmit(ours, CancelReasonSuperseded, "lost idempotency reservation race to "+rec.ID)
	return r.returnRecoveredJob(ctx, rec)
}

// idempotentMatch enforces the exact-identity contract for reuse. Any
// mismatch fails closed with a precise, field-naming error.
func idempotentMatch(rec JobRecord, req LaunchRequest, owner JobOwner, provider, connKey string) error {
	fail := func(field string) error {
		return fmt.Errorf("%w: %s differs from existing job %s", ErrIdempotencyMismatch, field, rec.ID)
	}
	if strings.TrimSpace(strings.ToLower(rec.Provider)) != provider {
		return fail("provider")
	}
	if connectionKey(rec.Provider, rec.ConnectionID) != connKey {
		return fail("connection")
	}
	if strings.TrimSpace(rec.SQL) != strings.TrimSpace(req.SQL) {
		return fail("sql")
	}
	if strings.TrimSpace(rec.Owner.ThreadID) != strings.TrimSpace(owner.ThreadID) {
		return fail("owner thread")
	}
	if strings.TrimSpace(rec.Owner.RunID) != strings.TrimSpace(owner.RunID) {
		return fail("owner run")
	}
	if strings.TrimSpace(rec.ReplacesJobID) != strings.TrimSpace(req.ReplacesJobID) {
		return fail("replaces_job_id")
	}
	// A caller-supplied owner token must be the original; recovery never
	// manufactures or swaps owners.
	if owner.Handle != "" && owner.Handle != rec.Owner.Handle {
		return fail("owner token")
	}
	return nil
}

// returnRecoveredJob materializes the durable winner in memory (lazy load
// under its ORIGINAL owner — authorized because idempotentMatch proved the
// caller's claim) and maps not-yet-established states to the
// ErrSubmissionUnknown sentinel so callers reconcile instead of retrying.
func (r *Registry) returnRecoveredJob(ctx context.Context, rec JobRecord) (*Job, error, bool) {
	job := r.lookupJob(rec.ID, rec.Owner)
	if job == nil {
		return nil, fmt.Errorf("idempotent recovery: durable job %s exists but could not be loaded", rec.ID), true
	}
	snap := cloneJob(job)
	if err := r.bindQueryObligation(ctx, rec.ID); err != nil {
		// A recovered winner may already be running remotely. Never terminalize
		// or resubmit it merely because the ledger repair is temporarily
		// unavailable; return the same durable identity with a retryable error.
		return snap, fmt.Errorf("idempotent recovery could not bind query obligation for %s: %w", rec.ID, err), true
	}
	switch {
	case snap.Status == JobStatusProfiling:
		// Still profiling (here or elsewhere): wait/claim; never re-profile.
		return snap, ErrProfilePending, true
	case snap.ProfileOutcome != ProfileOutcomeNone && snap.ProfileOutcome != ProfileOutcomeAccepted:
		// Profile-failed / interrupted replays get the same typed error a
		// fresh rejection produced, with Job.Profile/ProfileOutcome intact.
		return snap, fmt.Errorf("%w (%s): %s", ErrProfileRejected, snap.ProfileOutcome, snap.Error), true
	case snap.Status == JobStatusSubmissionUnknown, snap.Status == JobStatusReserved:
		return snap, ErrSubmissionUnknown, true
	default:
		return snap, nil, true
	}
}

// abortOwnReservation retracts a never-submitted reservation: terminal
// cancelled with outcome before_submit, capacity released, no wake spam.
func (r *Registry) abortOwnPreSubmit(job *Job, reason CancelReason, detail string) {
	now := r.nowFn()
	job.mu.Lock()
	if !isTerminalStatus(job.Status) &&
		(job.Status == JobStatusReserved || job.Status == JobStatusProfiling) &&
		!job.SubmitDispatched && !job.ProfileDispatched {
		job.CancelReason = reason
		job.CancelOutcome = CancelOutcomeBeforeSubmit
		job.Error = detail
		job.transitionLocked(JobStatusCancelled, now)
	}
	stop := job.pollStop
	job.mu.Unlock()
	if stop != nil {
		stop()
	}
	r.afterTransition(job)
}

func (r *Registry) bindQueryObligation(ctx context.Context, jobID string) error {
	if r == nil || r.store == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	record, found, err := r.store.GetQueryJob(ctx, strings.TrimSpace(jobID))
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	if !RequiresQueryObligationBinding(record) {
		return nil
	}
	// Runs are intentionally deletable while keep-on-completion query jobs
	// remain active. Their MySQL run_id is cleared by ON DELETE SET NULL and
	// their now-retired completion ledger no longer needs binding. Fresh marked
	// launches cannot enter this state because Launch validates run_id above.
	if strings.TrimSpace(record.Owner.RunID) == "" {
		return nil
	}
	binder, ok := r.store.(QueryObligationBinder)
	if !ok {
		return errors.New("durable store does not implement query obligation binding")
	}
	bindCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return binder.BindQueryObligation(bindCtx, strings.TrimSpace(jobID))
}

// persistJobStrict is the mandatory (error-returning) persist used on the
// launch path, CAS-aware so revision bookkeeping stays coherent.
func (r *Registry) persistJobStrict(ctx context.Context, job *Job) error {
	if r.store == nil {
		return nil
	}
	job.persistMu.Lock()
	defer job.persistMu.Unlock()
	job.mu.Lock()
	rec := recordFromJobLocked(job)
	expected := job.storeRevision
	job.mu.Unlock()
	persistCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if cas, ok := r.store.(RevisionedStore); ok {
		newRev, err := cas.UpsertQueryJobCAS(persistCtx, rec, expected)
		if err != nil {
			return err
		}
		job.mu.Lock()
		if newRev > job.storeRevision {
			job.storeRevision = newRev
		}
		job.mu.Unlock()
		return nil
	}
	return r.store.UpsertQueryJob(persistCtx, rec)
}

// commitInitialDrain routes the submit response's embedded observation
// through the atomic drain-commit path. Even a page-less response commits
// once (empty page + cursor) so DrainSeq >= 1 becomes an invariant of
// every durable drain record.
func (r *Registry) commitInitialDrain(ctx context.Context, job *Job, initial *StatusSnapshot) error {
	var pages []ResultPage
	var finalCursor *NativeHandle
	if initial != nil {
		pages = initial.DrainedPages
		finalCursor = initial.UpdatedHandle
	}
	if len(pages) == 0 {
		return r.commitDrainObservationWithRetryState(ctx, job, ResultPage{}, finalCursor, initial, 3)
	}
	for index, page := range pages {
		// Commit-before-advance: the assembler only moves after the
		// page's durable commit succeeds.
		bounded, _ := job.assembler.plan(page)
		cursor := page.CursorAfter
		if cursor == nil {
			cursor = finalCursor
		}
		var terminal *StatusSnapshot
		if initial != nil && initial.terminal() && index == len(pages)-1 {
			terminal = initial
		}
		if err := r.commitDrainObservationWithRetryState(ctx, job, bounded, cursor, terminal, 3); err != nil {
			return err
		}
		job.assembler.commit(page, bounded)
	}
	return nil
}

// SetValidation updates the orthogonal result-validation axis. It is the
// ONLY way validation state moves — retrieval never auto-validates. When
// the store supports ValidationStore the update is a revision-returning
// sidecar write, so it cannot invisibly invalidate lifecycle/drain CAS.
func (r *Registry) SetValidation(id string, owner JobOwner, state ValidationState, detail string) error {
	if r == nil {
		return ErrNotFound
	}
	if !validValidationState(state) {
		return ErrInvalidValidationState
	}
	job := r.lookupJob(id, normalizeOwner(owner))
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()
	if !ownerMatches(job.Owner, normalizeOwner(owner)) {
		job.mu.Unlock()
		return ErrOwnershipMismatch
	}
	if !job.managed {
		job.mu.Unlock()
		return ErrNotFound
	}
	if job.Status == JobStatusProfiling {
		// Like RequestCancel, never issue an unfenced whole-record/sidecar CAS
		// while a profile session owns the record's revision fence. Validation
		// is not part of the profile verdict, so defer it until that phase ends.
		job.mu.Unlock()
		return ErrProfilePending
	}
	job.Validation = state
	job.ValidationDetail = strings.TrimSpace(detail)
	job.LastAccess = r.nowFn()
	job.mu.Unlock()

	if vs, ok := r.store.(ValidationStore); ok {
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			job.mu.RLock()
			expected := job.storeRevision
			job.mu.RUnlock()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			newRev, err := vs.UpdateQueryJobValidation(ctx, id, state, strings.TrimSpace(detail), expected)
			cancel()
			if err == nil {
				job.mu.Lock()
				if newRev > job.storeRevision {
					job.storeRevision = newRev
				}
				job.mu.Unlock()
				r.notifyAll()
				return nil
			}
			lastErr = err
			if !errors.Is(err, ErrRevisionConflict) {
				break
			}
			// Sidecar race: refresh the revision and retry once.
			gctx, gcancel := context.WithTimeout(context.Background(), 2*time.Second)
			stored, found, gErr := r.store.GetQueryJob(gctx, id)
			gcancel()
			if gErr != nil || !found {
				break
			}
			job.mu.Lock()
			job.storeRevision = stored.Revision
			job.mu.Unlock()
		}
		r.notifyAll()
		return lastErr
	}
	r.afterTransition(job)
	return nil
}

// startMonitor begins monitoring under the store lease when available.
// Returns false when another live process holds the job's lease — the job
// stays in memory but this process does not drive it (P0: two processes
// must never monitor the same job).
func (r *Registry) startMonitor(job *Job) bool {
	ls, claimed := r.claimDriver(job)
	if !claimed {
		return false
	}
	go r.runManagedMonitor(job, ls)
	return true
}

// startSubmissionWatch marks the job locally driven before spawning the
// reconciliation watch, so cross-process barrier refresh never merges over
// a state this process is actively producing.
func (r *Registry) startSubmissionWatch(job *Job) bool {
	ls, claimed := r.claimDriver(job)
	if !claimed {
		return false
	}
	go r.runSubmissionWatch(job, ls)
	return true
}

// runSubmissionWatch drives a submission_unknown job: it retries the
// adapter's SubmissionReconciler (when implemented) on a backoff cadence
// and, regardless of reconciler availability, enforces the hard execution
// budget so unresolved work is eventually classified orphaned with a
// recovery_required wake (releasing its admission slot) — never silently
// pinned, never blind-resubmitted, and never a generic failure.
func (r *Registry) runSubmissionWatch(job *Job, ls LeaseStore) {
	if !r.confirmDriverLease(job, ls) {
		r.releaseDriver(job, ls)
		return
	}
	driverCtx, stopHeartbeat := r.leaseHeartbeat(job.pollCtx, ls, job.ID)
	handedToMonitor := false
	defer func() {
		stopHeartbeat()
		if !handedToMonitor {
			r.releaseDriver(job, ls)
		}
	}()
	adapter := job.adapter
	reconciler, hasReconciler := adapter.(SubmissionReconciler)
	caps := adapter.Capabilities()
	attempt := 0
	handoffMonitor := func() {
		handedToMonitor = true
		stopHeartbeat()
		r.runManagedMonitor(job, ls)
	}
	for {
		retryAfter := time.Duration(0)
		select {
		case <-driverCtx.Done():
			return
		default:
		}
		job.mu.RLock()
		status := job.Status
		submittedAt := remoteStartLocked(job)
		budgets := job.Budgets
		owner := job.Owner
		idem := job.IdempotencyKey
		sql := job.SQL
		connID := job.ConnectionID
		meta := cloneMap(job.Metadata)
		nextPollAt := cloneTimePtr(job.NextPollAt)
		nextAction := job.NextPollAction
		job.mu.RUnlock()

		if status != JobStatusSubmissionUnknown {
			if !isTerminalStatus(status) {
				job.mu.RLock()
				canMonitor := !job.Handle.IsZero()
				job.mu.RUnlock()
				if canMonitor {
					handoffMonitor()
				}
			}
			return // reconciled (externally or by us) or already terminal
		}
		if nextPollAt != nil && (nextAction == "" || nextAction == RetryActionReconcile) {
			if remaining := nextPollAt.Sub(r.nowFn()); remaining > 0 {
				r.sleepMonitorContext(job, driverCtx, remaining)
				continue
			}
			job.mu.Lock()
			if job.NextPollAt != nil && !job.NextPollAt.After(r.nowFn()) {
				job.NextPollAt = nil
				job.NextPollAction = ""
			}
			job.mu.Unlock()
		}
		now := r.nowFn()
		if budgets.HardExecution > 0 && !submittedAt.IsZero() && now.Sub(submittedAt) >= budgets.HardExecution {
			// Hard-budget diagnostics apply even with no handle: the work
			// is unreconcilable within budget → orphaned + recovery.
			job.mu.Lock()
			job.PolicyHealth = PolicyHealthHardExceeded
			job.Error = "submission unresolved past hard execution budget"
			job.transitionLocked(JobStatusOrphaned, now)
			job.appendWakeLocked(now, WakeRecoveryRequired, job.Error)
			stop := job.pollStop
			job.mu.Unlock()
			if stop != nil {
				stop()
			}
			r.afterTransition(job)
			return
		}

		if hasReconciler {
			rpcCtx, rpcCancel := r.providerCallContext(driverCtx, job, true)
			outcome, err := reconciler.ReconcileSubmission(rpcCtx, ReconcileRequest{
				JobID:          job.ID,
				IdempotencyKey: idem,
				SQL:            sql,
				ConnectionID:   connID,
				SubmittedAt:    submittedAt,
				Budgets:        budgets, // effective (defaulted) — what Submit saw
				Metadata:       meta,
			})
			rpcCancel()
			if err == nil {
				retryAfter = outcome.RetryAfter
				if outcome.Snapshot != nil && outcome.Snapshot.RetryAfter > retryAfter {
					retryAfter = outcome.Snapshot.RetryAfter
				}
				switch outcome.Resolution {
				case ReconcileFound:
					handle := cloneHandle(outcome.Handle)
					if handle.IsZero() && outcome.Snapshot != nil && outcome.Snapshot.UpdatedHandle != nil {
						handle = cloneHandle(*outcome.Snapshot.UpdatedHandle)
					}
					if handle.IsZero() {
						// "Found" without a resumable identity is not actionable.
						// Keep reconciling rather than falsely terminalizing or
						// risking a second submit.
						r.logfSafe("jobs: reconcile-found for %s omitted a durable handle", job.ID)
						break
					}

					// We already own the monitor lease. Persist the recovered
					// handle first, then consume the supplied snapshot through
					// the normal observation/finalization path. In particular, a
					// terminal success must fetch/materialize its results; merely
					// applying terminal state here would lose them forever.
					job.mu.Lock()
					if job.Status != JobStatusSubmissionUnknown {
						job.mu.Unlock()
						return
					}
					job.Handle = handle
					job.transitionLocked(JobStatusSubmitted, r.nowFn())
					if retryAfter > 0 {
						deadline := r.nowFn().Add(r.sched.errorInterval(attempt+1, caps, retryAfter))
						job.NextPollAt = &deadline
						job.NextPollAction = RetryActionPoll
					}
					job.mu.Unlock()
					r.afterTransition(job)
					if driverCtx.Err() != nil {
						return
					}

					if outcome.Snapshot != nil {
						if caps.RequiresContinuousDrain {
							if cErr := r.commitInitialDrain(driverCtx, job, outcome.Snapshot); cErr != nil {
								now := r.nowFn()
								job.mu.Lock()
								job.cancelWanted = true
								job.CancelReason = CancelReasonDurability
								if job.CancelStartedAt == nil {
									t := now
									job.CancelStartedAt = &t
									job.cancellingAt = t
								}
								if job.Metadata == nil {
									job.Metadata = map[string]any{}
								}
								job.Metadata["materialization_error"] = cErr.Error()
								job.transitionLocked(JobStatusCancelRequested, now)
								job.appendWakeLocked(now, WakePathology, "reconciled initial durable commit failed: "+cErr.Error())
								job.mu.Unlock()
								r.afterTransition(job)
								handoffMonitor()
								return
							}
						}
						if r.applySnapshot(job, *outcome.Snapshot, r.nowFn()) {
							r.finalizeFromSnapshotContext(driverCtx, job, *outcome.Snapshot, job.assembler, caps)
							return
						}
					}
					// Keep the already-acquired lease and local driving guard;
					// there must be no unleased gap between reconciliation I/O and Poll.
					handoffMonitor()
					return
				case ReconcileNotSubmitted:
					// Verifiably never landed: safe, distinct hard failure.
					if rErr := r.ReconcileSubmission(job.ID, owner, NativeHandle{}, nil); rErr != nil {
						r.logfSafe("jobs: reconcile-not-submitted apply for %s failed: %v", job.ID, rErr)
					}
					return
				}
				// ReconcileUnknown: keep trying under the hard budget.
			} else {
				retryAfter = RetryAfterFromError(err)
				r.logfSafe("jobs: submission reconcile for %s errored: %v", job.ID, err)
			}
		}
		attempt++
		delay := r.sched.errorInterval(attempt, caps, retryAfter)
		if retryAfter > 0 {
			r.persistRetryDeadline(job, delay, RetryActionReconcile, caps, retryAfter)
		}
		r.sleepMonitorContext(job, driverCtx, delay)
	}
}

// submitWithRetries retries not-delivered failures for every provider
// (the request never left) and ambiguous failures only when the provider
// supports idempotent submits keyed by IdempotencyKey.
func (r *Registry) submitWithRetries(ctx context.Context, job *Job, adapter ProviderAdapter, caps ProviderCapabilities, req SubmitRequest) (SubmitResult, error) {
	const maxAttempts = 3
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := time.Duration(attempt) * 500 * time.Millisecond
			if hinted := RetryAfterFromError(lastErr); hinted > delay {
				delay = hinted
			}
			hardEndsWait := false
			if remaining, bounded := r.hardRemaining(job, r.nowFn()); bounded {
				if remaining <= 0 {
					return SubmitResult{}, lastErr
				}
				if delay >= remaining {
					delay = remaining
					hardEndsWait = true
				}
			}
			select {
			case <-ctx.Done():
				return SubmitResult{}, lastErr
			case <-time.After(delay):
			}
			if hardEndsWait {
				return SubmitResult{}, lastErr
			}
		}
		rpcCtx, rpcCancel := r.providerCallContext(ctx, job, true)
		result, err := adapter.Submit(rpcCtx, req)
		rpcCancel()
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		if isSubmitNotDelivered(err) {
			continue // never left this process: always safe to retry
		}
		if !caps.IdempotentSubmit {
			break // ambiguous with no idempotency key: stop immediately
		}
	}
	return SubmitResult{}, lastErr
}

// RequestCancel starts the confirmed-cancellation lifecycle for a managed
// job (legacy jobs are delegated to the legacy Cancel). The job remains
// cancel_requested/cancelling until the provider confirms; completion may
// win (invariant I2).
func (r *Registry) RequestCancel(id string, owner JobOwner, reason CancelReason) error {
	if r == nil {
		return ErrNotFound
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	if id == "" || owner.ThreadID == "" || owner.Handle == "" {
		return ErrNotFound
	}
	if strings.TrimSpace(string(reason)) == "" {
		return ErrCancelReasonRequired
	}
	job := r.lookupJob(id, owner)
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()
	if !ownerMatches(job.Owner, owner) {
		job.mu.Unlock()
		return ErrOwnershipMismatch
	}
	if !job.managed {
		job.mu.Unlock()
		return r.Cancel(id, owner)
	}
	if isTerminalStatus(job.Status) {
		job.LastAccess = r.nowFn()
		job.mu.Unlock()
		return nil // idempotent
	}
	now := r.nowFn()
	switch job.Status {
	case JobStatusReserved:
		// Nothing was submitted: cancel is immediate and safe.
		job.CancelReason = reason
		job.CancelOutcome = CancelOutcomeBeforeSubmit
		job.transitionLocked(JobStatusCancelled, now)
		job.appendWakeLocked(now, WakeTerminal, "cancelled before submit")
		stop := job.pollStop
		job.mu.Unlock()
		if stop != nil {
			stop()
		}
		r.afterTransition(job)
		return nil
	case JobStatusProfiling:
		// Runtime intent ONLY: the profile phase persists the reason and
		// outcome atomically inside its single fenced cancellation verdict.
		// An ordinary persist here would be an unfenced write into the
		// profiling domain and could race/invalidate the fence.
		job.cancelWanted = true
		job.CancelReason = reason
		if job.profileCancel != nil {
			job.profileCancel()
		}
		job.LastAccess = now
		job.mu.Unlock()
		r.notifyAll()
		return nil
	case JobStatusSubmitted, JobStatusRunning:
		job.cancelWanted = true
		job.CancelReason = reason
		if job.CancelStartedAt == nil {
			t := now
			job.CancelStartedAt = &t
			job.cancellingAt = t
		}
		job.transitionLocked(JobStatusCancelRequested, now)
		job.nudgeLocked()
		job.mu.Unlock()
		r.afterTransition(job)
		return nil
	case JobStatusSubmissionUnknown:
		// Remember intent; reconciliation applies it.
		job.cancelWanted = true
		job.CancelReason = reason
		if job.CancelStartedAt == nil {
			t := now
			job.CancelStartedAt = &t
		}
		job.LastAccess = now
		job.mu.Unlock()
		r.afterTransition(job)
		return nil
	default: // cancel_requested / cancelling
		job.LastAccess = now
		job.mu.Unlock()
		return nil // idempotent
	}
}

// ReconcileSubmission resolves a submission_unknown job: with a live
// handle it becomes submitted and monitored; with a terminal snapshot it
// finalizes; with neither the caller is asserting the submit never landed
// and it fails safely. A resubmit is never performed here — that is a new
// linked Launch with ReplacesJobID.
func (r *Registry) ReconcileSubmission(id string, owner JobOwner, handle NativeHandle, snap *StatusSnapshot) error {
	if r == nil {
		return ErrNotFound
	}
	id = strings.TrimSpace(id)
	owner = normalizeOwner(owner)
	job := r.lookupJob(id, owner)
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()
	if !ownerMatches(job.Owner, owner) {
		job.mu.Unlock()
		return ErrOwnershipMismatch
	}
	if !job.managed || job.Status != JobStatusSubmissionUnknown {
		job.mu.Unlock()
		return ErrNotReconcilable
	}
	now := r.nowFn()
	switch {
	case !handle.IsZero():
		job.Handle = cloneHandle(handle)
		job.transitionLocked(JobStatusSubmitted, now)
		if snap != nil && snap.RetryAfter > 0 {
			caps := ProviderCapabilities{}
			if job.adapter != nil {
				caps = job.adapter.Capabilities()
			}
			deadline := now.Add(r.sched.nextInterval(0, caps, snap.RetryAfter))
			job.NextPollAt = &deadline
			job.NextPollAction = RetryActionPoll
		}
		wasDriving := job.driving
		job.nudgeLocked()
		job.mu.Unlock()
		r.afterTransition(job)
		if !wasDriving {
			r.startMonitor(job)
		}
		return nil
	case snap != nil && snap.terminal():
		if snap.State == ProviderStateSucceeded {
			// A successful remote statement without a resumable identity
			// cannot be fetched or durably materialized. Preserve the
			// unknown-submission record for reconciliation rather than
			// falsely claiming a result-less completion.
			job.mu.Unlock()
			return ErrNotReconcilable
		}
		// Failure/cancellation carry no result to retrieve, so a terminal
		// assertion without a handle remains safe.
		applyTerminalStateLocked(job, *snap, now)
		stop := job.pollStop
		job.mu.Unlock()
		if stop != nil {
			stop()
		}
		r.afterTransition(job)
		return nil
	default:
		job.Error = "submission confirmed not delivered"
		job.transitionLocked(JobStatusFailed, now)
		job.appendWakeLocked(now, WakeTerminal, job.Error)
		stop := job.pollStop
		job.mu.Unlock()
		if stop != nil {
			stop()
		}
		r.afterTransition(job)
		return nil
	}
}

// MarkOrphaned records that a job can no longer be driven (dead cursor,
// missing adapter, revoked credentials) and emits a recovery_required wake
// so continuation can launch a linked replacement. Never a generic retry.
func (r *Registry) MarkOrphaned(id string, owner JobOwner, detail string) error {
	if r == nil {
		return ErrNotFound
	}
	job := r.lookupJob(id, normalizeOwner(owner))
	if job == nil {
		return ErrNotFound
	}
	job.mu.Lock()
	if !ownerMatches(job.Owner, normalizeOwner(owner)) {
		job.mu.Unlock()
		return ErrOwnershipMismatch
	}
	if isTerminalStatus(job.Status) {
		job.mu.Unlock()
		return nil
	}
	now := r.nowFn()
	if strings.TrimSpace(detail) == "" {
		detail = "job can no longer be driven"
	}
	job.Error = detail
	job.transitionLocked(JobStatusOrphaned, now)
	job.appendWakeLocked(now, WakeRecoveryRequired, detail)
	stop := job.pollStop
	job.mu.Unlock()
	if stop != nil {
		stop()
	}
	r.afterTransition(job)
	return nil
}

// FetchPage serves post-completion result pages through the provider's
// native continuation (invariant I6: never re-executes SQL). Providers
// that require continuous drain have their results materialized durably
// instead and callers are redirected there.
func (r *Registry) FetchPage(ctx context.Context, id string, owner JobOwner, cursor FetchCursor) (ResultPage, error) {
	if r == nil {
		return ResultPage{}, ErrNotFound
	}
	job := r.lookupJob(id, normalizeOwner(owner))
	if job == nil {
		return ResultPage{}, ErrNotFound
	}
	job.mu.RLock()
	ownerOK := ownerMatches(job.Owner, normalizeOwner(owner))
	managed := job.managed
	status := job.Status
	handle := cloneHandle(job.Handle)
	adapter := job.adapter
	job.mu.RUnlock()
	if !ownerOK {
		return ResultPage{}, ErrOwnershipMismatch
	}
	if !managed || adapter == nil {
		return ResultPage{}, ErrNotFound
	}
	if status != JobStatusCompleted {
		return ResultPage{}, ErrNotCompleted
	}
	if adapter.Capabilities().RequiresContinuousDrain {
		return ResultPage{}, ErrUseMaterializedResults
	}
	fctx, fcancel := r.providerCallContext(ctx, job, false)
	defer fcancel()
	return adapter.Fetch(fctx, handle, cursor)
}
