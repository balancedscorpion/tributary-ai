package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// --- P0-2: submission_unknown reconciliation ---

type reconcilingAdapter struct {
	*fakeAdapter
	rmu            sync.Mutex
	reconcileFn    func(call int, req ReconcileRequest) (ReconcileOutcome, error)
	reconcileCalls int
}

func (a *reconcilingAdapter) ReconcileSubmission(_ context.Context, req ReconcileRequest) (ReconcileOutcome, error) {
	a.rmu.Lock()
	a.reconcileCalls++
	call := a.reconcileCalls
	fn := a.reconcileFn
	a.rmu.Unlock()
	if fn != nil {
		return fn(call, req)
	}
	return ReconcileOutcome{Resolution: ReconcileUnknown}, nil
}

func ambiguousSubmit(int, SubmitRequest) (SubmitResult, error) {
	return SubmitResult{}, errors.New("i/o timeout mid-request")
}

func TestSubmissionUnknownAutoReconcilesToFound(t *testing.T) {
	adapter := &reconcilingAdapter{
		fakeAdapter: &fakeAdapter{
			submitFn: ambiguousSubmit,
			pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
				return StatusSnapshot{State: ProviderStateSucceeded}, nil
			},
			fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
		},
		reconcileFn: func(call int, req ReconcileRequest) (ReconcileOutcome, error) {
			if req.IdempotencyKey == "" {
				return ReconcileOutcome{}, errors.New("reconcile must receive the persisted idempotency key")
			}
			if call == 1 {
				return ReconcileOutcome{Resolution: ReconcileUnknown}, nil
			}
			return ReconcileOutcome{Resolution: ReconcileFound, Handle: NativeHandle{Provider: "fake", ID: "found-9"}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("err=%v want ErrSubmissionUnknown", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "auto-reconciled to found and completed")
	if adapter.submitCount() != 1 {
		t.Fatalf("reconciliation must never re-submit; submit calls=%d", adapter.submitCount())
	}
}

func TestSubmissionUnknownReconcilesToNotSubmitted(t *testing.T) {
	adapter := &reconcilingAdapter{
		fakeAdapter: &fakeAdapter{submitFn: ambiguousSubmit},
		reconcileFn: func(int, ReconcileRequest) (ReconcileOutcome, error) {
			return ReconcileOutcome{Resolution: ReconcileNotSubmitted}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, _ := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusFailed
	}, "verified not-submitted becomes a safe failure")
}

func TestSubmissionUnknownHardBudgetOrphansAndReleasesCapacity(t *testing.T) {
	adapter := &fakeAdapter{submitFn: ambiguousSubmit} // no reconciler capability
	r := newTestRegistry(t, RegistryOptions{
		Admission: &AdmissionConfig{MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{HardExecution: 30 * time.Millisecond},
	})
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("err=%v want ErrSubmissionUnknown", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusOrphaned
	}, "unreconcilable work orphaned at hard budget")

	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.PolicyHealth != PolicyHealthHardExceeded {
		t.Fatalf("policy health=%s want hard_exceeded (diagnostics preserved without a handle)", snap.PolicyHealth)
	}
	hasRecovery := false
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeRecoveryRequired {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("orphaned-by-budget must emit recovery_required, got %+v", snap.WakeEvents)
	}

	// The admission slot must be free again: a fresh launch on the same
	// connection admits immediately.
	ok := &fakeAdapter{name: "fake", pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
		return StatusSnapshot{State: ProviderStateSucceeded}, nil
	}, fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil }}
	r.RegisterAdapter(ok)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Launch(ctx, LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_next"}, Provider: "fake", SQL: "select 2"}); err != nil {
		t.Fatalf("capacity was not released after orphaning: %v", err)
	}
}

// --- P0-3: CAS revisions, leases, atomic claims ---

func TestRevisionedPersistIncrementsAndSurvivesInternalRaces(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{NativePagination: true},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if call < 3 {
				return StatusSnapshot{State: ProviderStateRunning}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	// The store converges to terminal right after the in-memory
	// transition; wait on the durable record itself.
	waitFor(t, 2*time.Second, func() bool {
		rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
		return ok && rec.Status == JobStatusCompleted
	}, "stored record completed")
	rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
	if !ok || rec.Revision < 3 {
		t.Fatalf("revision=%d want monotonically increased across transitions", rec.Revision)
	}
}

func TestCASConflictSemantics(t *testing.T) {
	store := NewMemoryJobStore()
	rec := JobRecord{ID: "qj-x", Provider: "fake", Status: JobStatusReserved, Owner: testOwner(), SubmittedAt: time.Now(), LastAccess: time.Now()}
	rev1, err := store.UpsertQueryJobCAS(context.Background(), rec, 0)
	if err != nil || rev1 != 1 {
		t.Fatalf("create: rev=%d err=%v", rev1, err)
	}
	if _, err := store.UpsertQueryJobCAS(context.Background(), rec, 0); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale create err=%v want conflict", err)
	}
	rev2, err := store.UpsertQueryJobCAS(context.Background(), rec, rev1)
	if err != nil || rev2 != 2 {
		t.Fatalf("update: rev=%d err=%v", rev2, err)
	}
	if _, err := store.UpsertQueryJobCAS(context.Background(), rec, rev1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale update err=%v want conflict", err)
	}
}

func TestMonitorLeasePreventsSecondDriver(t *testing.T) {
	store := NewMemoryJobStore()
	// Direct lease semantics.
	ok1, _ := store.AcquireMonitorLease(context.Background(), "qj-l", "proc-a", time.Minute)
	ok2, _ := store.AcquireMonitorLease(context.Background(), "qj-l", "proc-b", time.Minute)
	if !ok1 || ok2 {
		t.Fatalf("lease acquire: a=%v b=%v want true,false", ok1, ok2)
	}
	renewed, _ := store.RenewMonitorLease(context.Background(), "qj-l", "proc-b", time.Minute)
	if renewed {
		t.Fatalf("non-holder renew must fail")
	}

	// Registry level: a job whose lease is held elsewhere is not driven.
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	rec := JobRecord{
		ID: "qj-fake-lease", Provider: "fake", Status: JobStatusRunning,
		Owner: testOwner(), Handle: NativeHandle{Provider: "fake", ID: "n1"},
		SubmittedAt: time.Now(), LastAccess: time.Now(),
	}
	_ = store.UpsertQueryJob(context.Background(), rec)
	if got, _ := store.AcquireMonitorLease(context.Background(), "qj-fake-lease", "other-process", time.Minute); !got {
		t.Fatalf("setup lease failed")
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Deferred != 1 || stats.Adopted != 0 {
		t.Fatalf("stats=%+v want Deferred=1 (lease held elsewhere)", stats)
	}
	time.Sleep(20 * time.Millisecond)
	if adapter.pollCalls != 0 {
		t.Fatalf("deferred job must not be polled by this process; polls=%d", adapter.pollCalls)
	}
}

func TestWakeClaimStoreIsExactlyOnceAcrossClaimants(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{
		caps:    ProviderCapabilities{NativePagination: true},
		fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completed")

	// Another process claims first, directly against the store.
	stolen, _, err := store.ClaimWakeEvents(context.Background(), job.ID, "other-process")
	if err != nil || len(stolen) == 0 {
		t.Fatalf("external claim: %v / %d", err, len(stolen))
	}
	// Our claim must observe the store's verdict: nothing left.
	claimed, err := r.ClaimWakeEvents(job.ID, testOwner())
	if err != nil || len(claimed) != 0 {
		t.Fatalf("local claim=%d err=%v want 0 (store is the exactly-once point)", len(claimed), err)
	}
	// Memory synchronized: a barrier wait now returns all-terminal, not
	// the stale event.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	res, err := r.WaitForJobRefs(ctx, []JobRef{{ID: job.ID, Owner: testOwner()}}, WaitOptions{})
	if err != nil || res.Event != nil {
		t.Fatalf("res=%+v err=%v want all-terminal with no stale event", res, err)
	}
}

// --- P0-4: multi-ref wait ---

func TestWaitForJobRefsAcrossDifferentOwnerTokens(t *testing.T) {
	fast := &fakeAdapter{
		name:    "fastp",
		caps:    ProviderCapabilities{NativePagination: true},
		fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
	}
	slow := &fakeAdapter{
		name: "slowp",
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(fast)
	r.RegisterAdapter(slow)

	ownerA := JobOwner{ThreadID: "thread-1", Handle: "tok_a"}
	ownerB := JobOwner{ThreadID: "thread-1", Handle: "tok_b"}
	jobSlow, err := r.Launch(context.Background(), LaunchRequest{Owner: ownerA, Provider: "slowp", SQL: "select slow"})
	if err != nil {
		t.Fatalf("launch slow: %v", err)
	}
	jobFast, err := r.Launch(context.Background(), LaunchRequest{Owner: ownerB, Provider: "fastp", SQL: "select fast"})
	if err != nil {
		t.Fatalf("launch fast: %v", err)
	}

	refs := []JobRef{{ID: jobSlow.ID, Owner: ownerA}, {ID: jobFast.ID, Owner: ownerB}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := r.WaitForJobRefs(ctx, refs, WaitOptions{Claim: true})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Job.ID != jobFast.ID || res.Event == nil || res.Event.Kind != WakeTerminal {
		t.Fatalf("res=%+v want fast job terminal wake", res)
	}
	if len(res.Events) == 0 || !allClaimedInStore(t, r, jobFast.ID) {
		t.Fatalf("claim-on-wait must atomically claim the returned events")
	}
	// Wrong token in a ref fails fast rather than hanging.
	bad := []JobRef{{ID: jobSlow.ID, Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_wrong"}}}
	if _, err := r.WaitForJobRefs(ctx, bad, WaitOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound", err)
	}
	_ = r.RequestCancel(jobSlow.ID, ownerA, CancelReasonNoLongerNeeded)
}

func allClaimedInStore(t *testing.T, r *Registry, jobID string) bool {
	t.Helper()
	rec, ok, err := r.store.GetQueryJob(context.Background(), jobID)
	if err != nil || !ok {
		return false
	}
	for _, ev := range rec.WakeEvents {
		if !ev.Claimed {
			return false
		}
	}
	return len(rec.WakeEvents) > 0
}

// --- P1-5: no false stall without comparable observations ---

func TestNoFalseStallWithDeclaredButUnextractedSignals(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{ProgressSignals: []ProgressSignal{SignalStageInfo}},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			// Stage-only provider: running, but no extractable counters.
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{
		Policy: &PolicyConfig{StallWindow: 2 * time.Millisecond, StallWindows: 2},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	time.Sleep(80 * time.Millisecond) // many windows worth of polling
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.PolicyHealth == PolicyHealthSuspectedStalled {
		t.Fatalf("stall fired with zero comparable observations (P1-5 regression)")
	}
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeSuspectedStalled {
			t.Fatalf("stall wake emitted without evidence: %+v", ev)
		}
	}
	_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
}

// --- P1-7: admission fairness ---

func TestAdmissionWakesEligibleWaiterBehindBlockedOne(t *testing.T) {
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{
		Admission: &AdmissionConfig{MaxGlobal: 2, MaxPerThread: 1, MaxPerConnection: 8},
	})
	r.RegisterAdapter(adapter)
	launch := func(thread, handle string) (*Job, error) {
		return r.Launch(context.Background(), LaunchRequest{
			Owner: JobOwner{ThreadID: thread, Handle: handle}, Provider: "fake", ConnectionID: "wh-1", SQL: "select 1",
		})
	}
	jobA, err := launch("thread-A", "tok_a")
	if err != nil {
		t.Fatalf("launch A: %v", err)
	}
	jobB, err := launch("thread-B", "tok_b")
	if err != nil {
		t.Fatalf("launch B: %v", err)
	}

	// Waiter X (thread-B) is FIFO-first but blocked by its per-thread cap.
	// Waiter Y (thread-C) behind it only needs a global slot.
	xDone := make(chan error, 1)
	go func() { _, err := launch("thread-B", "tok_x"); xDone <- err }()
	waitFor(t, 2*time.Second, func() bool {
		r.admission.mu.Lock()
		defer r.admission.mu.Unlock()
		return r.admission.waiters.Len() == 1
	}, "waiter X queued")
	yDone := make(chan error, 1)
	go func() { _, err := launch("thread-C", "tok_y"); yDone <- err }()
	waitFor(t, 2*time.Second, func() bool {
		r.admission.mu.Lock()
		defer r.admission.mu.Unlock()
		return r.admission.waiters.Len() == 2
	}, "waiter Y queued behind X")

	// Free a global slot from thread-A: only Y is eligible.
	if err := r.RequestCancel(jobA.ID, JobOwner{ThreadID: "thread-A", Handle: "tok_a"}, CancelReasonSuperseded); err != nil {
		t.Fatalf("cancel A: %v", err)
	}
	select {
	case err := <-yDone:
		if err != nil {
			t.Fatalf("eligible waiter Y: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("deadlock: eligible waiter Y was never woken behind blocked waiter X (P1-7)")
	}
	select {
	case err := <-xDone:
		t.Fatalf("blocked waiter X must still be queued, returned %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Freeing thread-B's slot admits X.
	if err := r.RequestCancel(jobB.ID, JobOwner{ThreadID: "thread-B", Handle: "tok_b"}, CancelReasonSuperseded); err != nil {
		t.Fatalf("cancel B: %v", err)
	}
	select {
	case err := <-xDone:
		if err != nil {
			t.Fatalf("waiter X after thread slot freed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("waiter X never admitted after its thread slot freed")
	}
}

// --- P1-8: rehydration round-trip fidelity ---

func TestJobRecordRoundTripFidelity(t *testing.T) {
	r := newTestRegistry(t, RegistryOptions{})
	adapter := &fakeAdapter{caps: ProviderCapabilities{DetachedRepoll: true}}
	started := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	rec := JobRecord{
		ID: "qj-fake-rt", Provider: "fake", ConnectionID: "wh-9",
		SQL: "select 1", SQLPreview: "select 1",
		Status:               JobStatusCancelling,
		MonitorHealth:        MonitorHealthDegraded,
		PolicyHealth:         PolicyHealthSoftExecution,
		MaterializationState: MaterializationPartial,
		Owner:                testOwner(),
		Handle:               NativeHandle{Provider: "fake", ID: "n-rt", Extra: map[string]string{"next_uri": "u9"}},
		IdempotencyKey:       "idem_rt",
		ReplacesJobID:        "qj-old",
		KeepOnRunCompletion:  true,
		Budgets:              Budgets{HardExecution: time.Hour},
		SubmittedAt:          started,
		StartedAt:            &started,
		LastAccess:           time.Now(),
		CancelReason:         CancelReasonUserRequest,
		CancelOutcome:        CancelOutcomeUnconfirmed,
		Error:                "prior error",
		ArtifactID:           "art-abc123",
		Metadata:             map[string]any{"k": "v"},
		WakeEvents: []WakeEvent{
			{At: started, Kind: WakeSoftExecution, Detail: "d", Claimed: true},
			{At: started.Add(time.Minute), Kind: WakePathology, Detail: "p", Claimed: false},
		},
		Revision: 7,
		DrainSeq: 42,
	}
	job := r.jobFromRecord(rec, adapter)
	if job.MonitorHealth != MonitorHealthDegraded {
		t.Fatalf("MonitorHealth dropped: %s", job.MonitorHealth)
	}
	if job.ArtifactID != "art-abc123" || job.DrainSeq != 42 || job.storeRevision != 7 {
		t.Fatalf("artifact/drainseq/revision dropped: %q %d %d", job.ArtifactID, job.DrainSeq, job.storeRevision)
	}
	if job.CancelReason != CancelReasonUserRequest || job.CancelOutcome != CancelOutcomeUnconfirmed {
		t.Fatalf("cancel state dropped: %s/%s", job.CancelReason, job.CancelOutcome)
	}
	if !job.cancelWanted || job.cancellingAt.IsZero() {
		t.Fatalf("cancellation runtime intent not restored")
	}
	if len(job.WakeEvents) != 2 || !job.WakeEvents[0].Claimed || job.WakeEvents[1].Claimed {
		t.Fatalf("wake events/claim flags dropped: %+v", job.WakeEvents)
	}
	if !job.emittedWakes[WakePathology] || !job.emittedWakes[WakeSoftExecution] {
		t.Fatalf("wake dedup state not seeded from record")
	}

	job.mu.RLock()
	back := recordFromJobLocked(job)
	job.mu.RUnlock()
	if back.ArtifactID != rec.ArtifactID || back.DrainSeq != rec.DrainSeq || back.Revision != rec.Revision {
		t.Fatalf("record round-trip lost fields: %+v", back)
	}
	if back.MonitorHealth != MonitorHealthDegraded || back.CancelOutcome != CancelOutcomeUnconfirmed {
		t.Fatalf("record round-trip lost health/cancel: %+v", back)
	}
	if len(back.WakeEvents) != 2 {
		t.Fatalf("record round-trip lost wakes: %+v", back.WakeEvents)
	}
}

// --- P1-9: unconfirmable cancellation pathology ---

func TestUnconfirmableCancellationEmitsPathologyAndKeepsRetrying(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
		confirmFn: func(int) (bool, StatusSnapshot, error) {
			return false, StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		if !ok {
			return false
		}
		for _, ev := range snap.WakeEvents {
			if ev.Kind == WakePathology {
				return true
			}
		}
		return false
	}, "pathology wake for unconfirmable cancellation")

	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Status != JobStatusCancelling {
		t.Fatalf("status=%s want cancelling (honest non-terminal)", snap.Status)
	}
	if snap.CancelOutcome != CancelOutcomeUnconfirmed {
		t.Fatalf("outcome=%s want unconfirmed diagnostic", snap.CancelOutcome)
	}
	// Durable: the pathology is in the persisted record too.
	rec, ok, _ := r.store.GetQueryJob(context.Background(), job.ID)
	foundDurable := false
	for _, ev := range rec.WakeEvents {
		if ev.Kind == WakePathology {
			foundDurable = true
		}
	}
	if !ok || !foundDurable {
		t.Fatalf("pathology wake must be durable; rec=%+v", rec.WakeEvents)
	}
	// Cancel keeps being re-issued safely.
	waitFor(t, 2*time.Second, func() bool {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		return adapter.cancelCalls >= 2
	}, "cancel re-issued while unconfirmed")
}
