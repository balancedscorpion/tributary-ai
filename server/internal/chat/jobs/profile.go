package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
)

const scanApprovalHeadroomPercent int64 = 5

var (
	// ErrProfileRejected wraps every typed pre-submit profile failure
	// (invalid / over_budget / approval_denied / timed_out / errored). The job is already
	// terminalized with Job.Profile + Job.ProfileOutcome populated; tools
	// format the rewrite/approval/alternative-plan response from those.
	ErrProfileRejected = errors.New("query profile rejected before submit")
	// ErrProfilePending: the durable profiling job exists but THIS call
	// may not drive it (idempotent replay found it profiling, or the
	// mandatory profile lease could not be confirmed). Wait/claim or let
	// the holder/recovery drive it; never profile or submit here.
	ErrProfilePending = errors.New("query is profiling under another holder or awaiting recovery")
	// ErrProfileFenceUnsupported: the durable store provides leases but
	// not FencedProfileStore, so profile mutations cannot be made
	// crash/lease-safe. Fail closed before any profile provider call.
	ErrProfileFenceUnsupported = errors.New("store provides monitor leases but no fenced profile mutations; refusing to profile")
)

// scanApprovalAuthorizationBytes converts a provider estimate into the
// execution cap shown to and approved by the user. Five percent is deliberately
// loose enough to absorb ordinary dry-run/submission drift while remaining a
// small, explicit cost allowance. A configured maxScan is always a hard
// ceiling. Saturation keeps untrusted int64 values from overflowing.
func scanApprovalAuthorizationBytes(estimated, maxScan int64) int64 {
	if estimated <= 0 {
		return estimated
	}
	headroom := estimated / (100 / scanApprovalHeadroomPercent)
	if estimated%(100/scanApprovalHeadroomPercent) != 0 {
		headroom++
	}
	authorized := int64(math.MaxInt64)
	if headroom <= math.MaxInt64-estimated {
		authorized = estimated + headroom
	}
	if maxScan > 0 && authorized > maxScan {
		return maxScan
	}
	return authorized
}

// profileSession is the lease-fenced write context for one profile phase.
type profileSession struct {
	r         *Registry
	job       *Job
	hasLease  bool
	fenced    FencedProfileStore // nil when the store has no lease semantics
	leaseLost atomic.Bool
}

// applyMutation performs one fenced narrow profile write — the SINGLE
// durable boundary for the mutation, including all terminal side effects.
// The in-memory job is updated ONLY from the store's returned result; no
// ordinary persist/CAS ever follows a fenced write. On an AMBIGUOUS
// failure (store error that may have committed) the durable record is
// re-read: if it shows the exact intended mutation, it is adopted as
// success rather than overwritten or duplicated.
func (s *profileSession) applyMutation(mut ProfileMutation) error {
	if s.leaseLost.Load() {
		return ErrLeaseLost
	}
	if s.fenced != nil {
		s.job.mu.RLock()
		expected := s.job.storeRevision
		s.job.mu.RUnlock()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		result, err := s.fenced.ApplyProfileMutation(ctx, s.job.ID, s.r.instanceID, expected, mut)
		cancel()
		if err == nil {
			s.applyLocal(mut, result)
			s.r.notifyAll()
			return nil
		}
		if errors.Is(err, ErrLeaseLost) {
			s.leaseLost.Store(true)
			return err
		}
		if !errors.Is(err, ErrRevisionConflict) {
			// Ambiguous: the write may have committed. Re-read and adopt
			// the exact intended mutation if it did.
			if s.adoptIfCommitted(mut) {
				s.r.notifyAll()
				return nil
			}
		}
		return err
	}
	// No lease semantics on the store (or no store): single-process rules
	// — one local apply plus at most one strict persist, same discipline.
	s.applyLocal(mut, ProfileMutationResult{At: s.r.nowFn(), CompletedAt: terminalStamp(mut, s.r.nowFn()), AppendedWakes: localWakes(s.job, mut, s.r.nowFn()), CapacityReleased: mutIsTerminal(mut)})
	if s.r.store != nil {
		if err := s.r.persistJobStrict(context.Background(), s.job); err != nil {
			return err
		}
	}
	s.r.notifyAll()
	return nil
}

func mutIsTerminal(mut ProfileMutation) bool {
	return mut.TransitionTo != nil && isTerminalStatus(*mut.TransitionTo)
}

func terminalStamp(mut ProfileMutation, now time.Time) *time.Time {
	if mutIsTerminal(mut) {
		t := now
		return &t
	}
	return nil
}

// localWakes computes the kind-deduped wake events for the leaseless path.
func localWakes(job *Job, mut ProfileMutation, now time.Time) []WakeEvent {
	job.mu.RLock()
	existing := map[WakeKind]bool{}
	for _, ev := range job.WakeEvents {
		existing[ev.Kind] = true
	}
	job.mu.RUnlock()
	var out []WakeEvent
	for _, spec := range mut.Wakes {
		if existing[spec.Kind] {
			continue
		}
		existing[spec.Kind] = true
		out = append(out, WakeEvent{At: now, Kind: spec.Kind, Detail: spec.Detail})
	}
	return out
}

// applyLocal mirrors the store's committed result into the in-memory job.
func (s *profileSession) applyLocal(mut ProfileMutation, result ProfileMutationResult) {
	s.job.mu.Lock()
	if mut.Dispatched != nil {
		s.job.ProfileDispatched = *mut.Dispatched
	}
	if mut.Handle != nil {
		s.job.ProfileHandle = cloneHandle(*mut.Handle)
	}
	if mut.Phase != nil {
		s.job.ProfilePhase = *mut.Phase
	}
	if mut.Profile != nil {
		s.job.Profile = cloneQueryProfile(mut.Profile)
	}
	if mut.Outcome != nil {
		s.job.ProfileOutcome = *mut.Outcome
	}
	if mut.Error != nil {
		s.job.Error = *mut.Error
	}
	if mut.CancelReason != nil {
		s.job.CancelReason = *mut.CancelReason
	}
	if mut.CancelOutcome != nil {
		s.job.CancelOutcome = *mut.CancelOutcome
	}
	if mut.TransitionTo != nil {
		s.job.transitionLocked(*mut.TransitionTo, result.At)
	}
	if result.CompletedAt != nil {
		t := *result.CompletedAt
		s.job.CompletedAt = &t
	}
	for _, ev := range result.AppendedWakes {
		s.job.WakeEvents = append(s.job.WakeEvents, ev)
		if s.job.emittedWakes == nil {
			s.job.emittedWakes = map[WakeKind]bool{}
		}
		s.job.emittedWakes[ev.Kind] = true
	}
	if result.CapacityReleased {
		s.job.capacityReleased = true
	}
	if result.NewRevision > s.job.storeRevision {
		s.job.storeRevision = result.NewRevision
	}
	s.job.LastAccess = result.At
	s.job.mu.Unlock()
}

// adoptIfCommitted re-reads the durable record after an ambiguous fenced
// write and, when the record already reflects the exact intended mutation,
// synchronizes local state to it (recognize-and-adopt, never overwrite).
func (s *profileSession) adoptIfCommitted(mut ProfileMutation) bool {
	if s.r.store == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	rec, found, err := s.r.store.GetQueryJob(ctx, s.job.ID)
	cancel()
	if err != nil || !found {
		return false
	}
	if !mutationCommitted(rec, mut) {
		return false
	}
	s.job.mu.Lock()
	applyRecordLocked(s.job, rec, s.r.nowFn())
	if isTerminalStatus(rec.Status) {
		s.job.capacityReleased = true
	}
	s.job.mu.Unlock()
	return true
}

// mutationCommitted recognizes the exact intended mutation in a record.
func mutationCommitted(rec JobRecord, mut ProfileMutation) bool {
	if mut.TransitionTo != nil && rec.Status != *mut.TransitionTo {
		return false
	}
	if mut.TransitionTo == nil && rec.Status != JobStatusProfiling {
		return false
	}
	if mut.Outcome != nil && rec.ProfileOutcome != *mut.Outcome {
		return false
	}
	if mut.Profile != nil && !reflect.DeepEqual(rec.Profile, mut.Profile) {
		return false
	}
	if mut.Error != nil && rec.Error != *mut.Error {
		return false
	}
	if mut.CancelReason != nil && rec.CancelReason != *mut.CancelReason {
		return false
	}
	if mut.CancelOutcome != nil && rec.CancelOutcome != *mut.CancelOutcome {
		return false
	}
	if mut.Dispatched != nil && rec.ProfileDispatched != *mut.Dispatched {
		return false
	}
	// Query ID alone is insufficient for forward-only protocols. Trino keeps
	// the same ID while next_uri and protocol state advance; recognizing an
	// old cursor as a committed publication could make crash cleanup DELETE
	// the wrong page. Match the complete durable handle.
	if mut.Handle != nil && !handlesEqual(rec.ProfileHandle, *mut.Handle) {
		return false
	}
	if mut.Phase != nil && rec.ProfilePhase != *mut.Phase {
		return false
	}
	if mutIsTerminal(mut) && rec.CompletedAt == nil {
		return false
	}
	for _, spec := range mut.Wakes {
		present := false
		for _, ev := range rec.WakeEvents {
			if ev.Kind == spec.Kind {
				present = true
				break
			}
		}
		if !present {
			return false
		}
	}
	return true
}

// PublishProfileHandle / PublishProfilePhase implement ProfileObserver.
func (s *profileSession) PublishProfileHandle(h NativeHandle) error {
	clone := cloneHandle(h)
	return s.applyMutation(ProfileMutation{Handle: &clone})
}

func (s *profileSession) PublishProfilePhase(phase string) error {
	trimmed := strings.TrimSpace(phase)
	return s.applyMutation(ProfileMutation{Phase: &trimmed})
}

// adapterProfiler resolves the profiling capability (observable preferred).
func adapterProfiler(adapter ProviderAdapter) (ObservableQueryProfiler, QueryProfiler, bool) {
	if op, ok := adapter.(ObservableQueryProfiler); ok {
		return op, nil, true
	}
	if p, ok := adapter.(QueryProfiler); ok {
		return nil, p, true
	}
	return nil, nil, false
}

// runProfilePhase executes the bounded pre-submit profile on a job that is
// durably reserved in JobStatusProfiling.
//
// Lease discipline (when the store has leases):
//   - acquisition is MANDATORY: unconfirmed acquisition means zero profile
//     calls, zero submits, and an ErrProfilePending yield;
//   - an independent TTL/3 heartbeat holds the lease for the whole
//     profile; renewal failure cancels the profile context and this
//     process yields WITHOUT writing any verdict (a stale holder never
//     terminalizes);
//   - every durable profile write is holder+lease+revision fenced;
//   - the lease is released only on local terminal verdicts; acceptance
//     carries the same holder straight into submission/monitor startup.
func (r *Registry) runProfilePhase(
	ctx context.Context,
	job *Job,
	adapter ProviderAdapter,
	caps ProviderCapabilities,
	subReq *SubmitRequest,
	scanApproval ScanApprovalFunc,
) error {
	session := &profileSession{r: r, job: job}
	ls, hasLease := r.store.(LeaseStore)
	if hasLease {
		fenced, hasFence := r.store.(FencedProfileStore)
		if !hasFence {
			// Guarded again in Launch pre-reserve; defense in depth.
			return ErrProfileFenceUnsupported
		}
		session.hasLease = true
		session.fenced = fenced
		actx, acancel := context.WithTimeout(context.Background(), 2*time.Second)
		acquired, err := ls.AcquireMonitorLease(actx, job.ID, r.instanceID, r.leaseTTL)
		acancel()
		if err != nil || !acquired {
			// MANDATORY: no confirmed lease, no profile, no submit. The
			// durable profiling job stays for its holder or recovery.
			return fmt.Errorf("%w: profile lease not acquired (acquired=%v err=%v)", ErrProfilePending, acquired, err)
		}
	}

	bound := r.profile.effectiveProfileTimeout(caps)
	phaseCtx, phaseCancel := context.WithCancel(ctx)
	profileCtx, profileTimeoutCancel := context.WithTimeout(phaseCtx, bound)
	job.mu.Lock()
	job.profileCancel = phaseCancel
	job.mu.Unlock()
	defer func() {
		profileTimeoutCancel()
		phaseCancel()
		job.mu.Lock()
		job.profileCancel = nil
		job.mu.Unlock()
	}()

	// Independent lease heartbeat for the whole profile (a profile may
	// legitimately outlive the lease TTL).
	releaseOnExit := false
	if hasLease {
		releaseOnExit = true
		hbCtx, hbCancel := context.WithCancel(context.Background())
		defer hbCancel()
		interval := r.leaseTTL / 3
		if interval < 5*time.Millisecond {
			interval = 5 * time.Millisecond
		}
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-hbCtx.Done():
					return
				case <-ticker.C:
				}
				rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
				renewed, err := ls.RenewMonitorLease(rctx, job.ID, r.instanceID, r.leaseTTL)
				rcancel()
				if err != nil || !renewed {
					r.logfSafe("jobs: profile lease for %s lost (renewed=%v err=%v); yielding", job.ID, renewed, err)
					session.leaseLost.Store(true)
					phaseCancel()
					return
				}
			}
		}()
	}
	releaseLease := func() {
		if !releaseOnExit || session.leaseLost.Load() {
			return // release only if still the holder
		}
		rctx, rcancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = ls.ReleaseMonitorLease(rctx, job.ID, r.instanceID)
		rcancel()
	}

	// Profile dispatch marker: strictly durable immediately before the
	// profiler's remote-call boundary. The EXECUTION marker
	// (SubmitDispatched) remains untouched by profile work.
	dispatched := true
	if err := session.applyMutation(ProfileMutation{Dispatched: &dispatched}); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			return fmt.Errorf("%w: before profile dispatch", ErrLeaseLost)
		}
		// Nothing remote happened; terminalize safely under our lease.
		defer releaseLease()
		return r.terminalizeProfile(session, adapter, ProfileOutcomeErrored, "profile dispatch marker could not be persisted: "+err.Error(), nil)
	}

	var profile QueryProfile
	var perr error
	if op, p, ok := adapterProfiler(adapter); ok {
		if op != nil {
			profile, perr = op.ProfileWithObserver(profileCtx, *subReq, session)
		} else {
			profile, perr = p.Profile(profileCtx, *subReq)
		}
	} else {
		defer releaseLease()
		return nil // caller guards; nothing to profile
	}
	profileTimeoutCancel()

	if session.leaseLost.Load() {
		// Stale holder: zero verdict writes, zero submit; the new holder
		// or rehydration owns this job's fate now.
		return fmt.Errorf("%w: during profile execution", ErrLeaseLost)
	}

	job.mu.RLock()
	cancelWanted := job.cancelWanted
	maxScan := job.Budgets.MaxScanBytes
	automaticScan := job.Budgets.AutomaticScanBytes
	job.mu.RUnlock()
	if automaticScan <= 0 || (maxScan > 0 && automaticScan > maxScan) {
		automaticScan = maxScan
	}

	outcome := ProfileOutcomeAccepted
	detail := ""
	switch {
	case perr != nil && errors.Is(perr, context.DeadlineExceeded):
		outcome = ProfileOutcomeTimedOut
		detail = fmt.Sprintf("profile exceeded its %s bound", bound)
	case perr != nil && errors.Is(perr, context.Canceled) && cancelWanted:
		outcome = ProfileOutcomeNone // cancellation is not a profile verdict
		detail = "cancelled during profiling"
	case perr != nil:
		outcome = ProfileOutcomeErrored
		detail = strings.TrimSpace(perr.Error())
	case !profile.Valid:
		outcome = ProfileOutcomeInvalid
		detail = strings.TrimSpace(profile.Error)
		if detail == "" {
			detail = "provider reported the query invalid"
		}
	case maxScan > 0 && profile.EstimatedBytes != nil && *profile.EstimatedBytes > maxScan:
		outcome = ProfileOutcomeOverBudget
		detail = fmt.Sprintf("estimated scan of %d bytes exceeds MaxScanBytes %d", *profile.EstimatedBytes, maxScan)
	case cancelWanted:
		outcome = ProfileOutcomeNone
		detail = "cancelled during profiling"
	}

	authorizedScan := maxScan
	if automaticScan > 0 {
		authorizedScan = automaticScan
	}
	// Approval gate: any accepted estimate above the automatic limit needs a
	// one-time user approval. maxScan == 0 means no configured ceiling, so
	// the approval band is unbounded; estimates above a positive maxScan were
	// already rejected as over_budget and never reach here.
	if outcome == ProfileOutcomeAccepted &&
		automaticScan > 0 &&
		profile.EstimatedBytes != nil &&
		*profile.EstimatedBytes > automaticScan {
		approvalAuthorization := scanApprovalAuthorizationBytes(*profile.EstimatedBytes, maxScan)
		if scanApproval == nil {
			outcome = ProfileOutcomeApprovalDenied
			detail = fmt.Sprintf(
				"estimated scan of %d bytes exceeds automatic limit %d and approval is unavailable",
				*profile.EstimatedBytes,
				automaticScan,
			)
		} else {
			decision, approvalErr := scanApproval(phaseCtx, ScanApprovalRequest{
				JobID:               job.ID,
				Provider:            job.Provider,
				ConnectionID:        job.ConnectionID,
				SQLPreview:          job.SQLPreview,
				EstimatedBytes:      *profile.EstimatedBytes,
				AutomaticScanBytes:  automaticScan,
				ApprovedScanBytes:   maxScan,
				AuthorizedScanBytes: approvalAuthorization,
			})
			job.mu.RLock()
			cancelWanted = job.cancelWanted
			job.mu.RUnlock()
			switch {
			case cancelWanted:
				outcome = ProfileOutcomeNone
				detail = "cancelled while awaiting scan budget approval"
			case approvalErr != nil:
				outcome = ProfileOutcomeApprovalDenied
				detail = "scan budget approval failed: " + strings.TrimSpace(approvalErr.Error())
			case !decision.Approved:
				outcome = ProfileOutcomeApprovalDenied
				detail = "scan budget approval was not granted"
			default:
				authorizedScan = approvalAuthorization
				profile.ProviderDetails = cloneMap(profile.ProviderDetails)
				if profile.ProviderDetails == nil {
					profile.ProviderDetails = map[string]any{}
				}
				profile.ProviderDetails["scan_budget_approval_id"] = strings.TrimSpace(decision.ApprovalID)
				profile.ProviderDetails["scan_budget_approved"] = true
				profile.ProviderDetails["scan_budget_headroom_bytes"] = authorizedScan - *profile.EstimatedBytes
				profile.ProviderDetails["scan_budget_headroom_percent"] = scanApprovalHeadroomPercent
			}
		}
	}

	if outcome == ProfileOutcomeAccepted {
		subReq.Budgets.MaxScanBytes = authorizedScan
		profile.ProviderDetails = cloneMap(profile.ProviderDetails)
		if profile.ProviderDetails == nil {
			profile.ProviderDetails = map[string]any{}
		}
		profile.ProviderDetails["automatic_scan_bytes"] = automaticScan
		profile.ProviderDetails["approved_scan_bytes"] = maxScan
		profile.ProviderDetails["authorized_scan_bytes"] = authorizedScan
		accepted := ProfileOutcomeAccepted
		reserved := JobStatusReserved
		if err := session.applyMutation(ProfileMutation{
			Profile:      &profile,
			Outcome:      &accepted,
			TransitionTo: &reserved,
		}); err != nil {
			if errors.Is(err, ErrLeaseLost) {
				return fmt.Errorf("%w: at profile acceptance", ErrLeaseLost)
			}
			defer releaseLease()
			return r.terminalizeProfile(session, adapter, ProfileOutcomeErrored, "accepted profile could not be persisted: "+err.Error(), &profile)
		}
		// Acceptance: do NOT release — the same holder carries into
		// submission and monitor startup.
		r.notifyAll()
		return nil
	}

	if outcome == ProfileOutcomeNone {
		// Explicit cancellation during profiling: ONE fenced atomic write
		// carrying Cancelled + before_submit + store CompletedAt +
		// WakeTerminal. No follow-up persist of any kind.
		defer releaseLease()
		r.cancelProfileHandleBestEffort(job, adapter)
		cancelledStatus := JobStatusCancelled
		errText := detail
		beforeSubmit := CancelOutcomeBeforeSubmit
		job.mu.RLock()
		reason := job.CancelReason
		job.mu.RUnlock()
		if err := session.applyMutation(ProfileMutation{
			Error:         &errText,
			TransitionTo:  &cancelledStatus,
			CancelReason:  &reason,
			CancelOutcome: &beforeSubmit,
			Wakes:         []WakeSpec{{Kind: WakeTerminal, Detail: detail}},
		}); err != nil {
			// Unconfirmed terminal write: never claim terminal locally.
			// Yield; durable reconciliation (rehydrate/holder) decides.
			if errors.Is(err, ErrLeaseLost) {
				return fmt.Errorf("%w: at profile cancellation", ErrLeaseLost)
			}
			return fmt.Errorf("%w: profile cancellation verdict unconfirmed: %v", ErrProfilePending, err)
		}
		job.pollStop()
		r.afterFencedTerminal(job)
		return fmt.Errorf("profiling cancelled before submit: %w", context.Canceled)
	}

	var learned *QueryProfile
	if perr == nil {
		learned = &profile // invalid/over_budget still carry useful details
	}
	defer releaseLease()
	return r.terminalizeProfile(session, adapter, outcome, detail, learned)
}

// afterFencedTerminal handles the local aftermath of a fenced terminal
// mutation: the store already committed status, CompletedAt, wakes, and
// the capacity release atomically, so this only notifies waiters and
// re-evaluates local admission — NO ordinary persist, NO CAS.
func (r *Registry) afterFencedTerminal(job *Job) {
	r.notifyAll()
	r.wakeEligibleWaiters()
	_ = job // local state was already mirrored from the mutation result
}

// terminalizeProfile records the typed failure through ONE fenced atomic
// write — Failed + profile/outcome/error + store CompletedAt +
// WakeRecoveryRequired + WakeTerminal + capacity release — after a
// best-effort cancel of any published profile handle. Never submits;
// never writes from a stale holder; never claims an unconfirmed terminal.
func (r *Registry) terminalizeProfile(session *profileSession, adapter ProviderAdapter, outcome ProfileOutcome, detail string, learned *QueryProfile) error {
	if session.leaseLost.Load() {
		return fmt.Errorf("%w: refusing terminal profile verdict from stale holder", ErrLeaseLost)
	}
	job := session.job
	r.cancelProfileHandleBestEffort(job, adapter)
	failedStatus := JobStatusFailed
	errText := detail
	mut := ProfileMutation{
		Outcome:      &outcome,
		Error:        &errText,
		TransitionTo: &failedStatus,
		Wakes: []WakeSpec{
			{Kind: WakeRecoveryRequired, Detail: "profile " + string(outcome) + ": " + detail},
			{Kind: WakeTerminal, Detail: detail},
		},
	}
	if learned != nil {
		mut.Profile = learned
	}
	if err := session.applyMutation(mut); err != nil {
		// Unconfirmed terminal write: never claim terminal locally, no
		// unfenced fallback. Yield pending/recovery; the durable record
		// (still profiling unless the write actually committed, in which
		// case adoption already succeeded) is reconciled by its holder or
		// rehydration. Zero Submit either way.
		if errors.Is(err, ErrLeaseLost) {
			return fmt.Errorf("%w: at profile verdict", ErrLeaseLost)
		}
		return fmt.Errorf("%w: profile verdict (%s) unconfirmed: %v", ErrProfilePending, outcome, err)
	}
	job.pollStop()
	r.afterFencedTerminal(job)
	return fmt.Errorf("%w (%s): %s", ErrProfileRejected, outcome, detail)
}

func (r *Registry) cancelProfileHandleBestEffort(job *Job, adapter ProviderAdapter) {
	job.mu.RLock()
	handle := cloneHandle(job.ProfileHandle)
	job.mu.RUnlock()
	if handle.IsZero() || adapter == nil {
		return
	}
	pc, ok := adapter.(ProfileCanceller)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), providerCancelTimeout)
	defer cancel()
	if err := pc.CancelProfile(ctx, handle); err != nil {
		r.logfSafe("jobs: profile cancel for %s failed: %v", job.ID, err)
	}
}
