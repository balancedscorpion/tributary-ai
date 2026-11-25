package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func idemRequest(key string) LaunchRequest {
	return LaunchRequest{
		Owner:          testOwner(),
		Provider:       "fake",
		SQL:            "select 1",
		IdempotencyKey: key,
	}
}

func TestLaunchIdempotentReplaySameProcess(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-A"})
	r.RegisterAdapter(adapter)

	job1, err := r.Launch(context.Background(), idemRequest("call-1"))
	if err != nil {
		t.Fatalf("launch 1: %v", err)
	}
	job2, err := r.Launch(context.Background(), idemRequest("call-1"))
	if err != nil {
		t.Fatalf("replayed launch: %v", err)
	}
	if job2.ID != job1.ID {
		t.Fatalf("replay created a new job: %s vs %s", job2.ID, job1.ID)
	}
	if job2.Owner.Handle != job1.Owner.Handle || job2.Owner.Handle == "" {
		t.Fatalf("replay must return the ORIGINAL owner token: %q vs %q", job2.Owner.Handle, job1.Owner.Handle)
	}
	if adapter.submitCount() != 1 {
		t.Fatalf("submit count=%d want exactly 1 across replays", adapter.submitCount())
	}
	// Exactly one durable record for the key.
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 1 {
		t.Fatalf("durable records=%d want 1", len(recs))
	}
	_ = r.RequestCancel(job1.ID, testOwner(), CancelReasonNoLongerNeeded)
}

func TestLaunchIdempotentReplayAcrossProcesses(t *testing.T) {
	store := NewMemoryJobStore()
	adapterA := runningAdapter()
	rA := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-A"})
	rA.RegisterAdapter(adapterA)
	job1, err := rA.Launch(context.Background(), idemRequest("call-x"))
	if err != nil {
		t.Fatalf("launch on A: %v", err)
	}

	// Fresh registry over the same durable store, its own adapter. The
	// replayed request carries NO owner handle: recovery must return the
	// original capability, never mint a new one.
	adapterB := &fakeAdapter{name: "fake", caps: ProviderCapabilities{CancelConfirmable: true}}
	rB := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-B"})
	rB.RegisterAdapter(adapterB)
	req := idemRequest("call-x")
	req.Owner = JobOwner{ThreadID: "thread-1", RunID: "run-1"} // no token
	job2, err := rB.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("replayed launch on B: %v", err)
	}
	if job2.ID != job1.ID || job2.Owner.Handle != job1.Owner.Handle {
		t.Fatalf("cross-process replay must return original job+owner: %s/%q vs %s/%q",
			job2.ID, job2.Owner.Handle, job1.ID, job1.Owner.Handle)
	}
	if job2.Status != JobStatusRunning && job2.Status != JobStatusSubmitted {
		t.Fatalf("recovered status=%s want the live state", job2.Status)
	}
	time.Sleep(30 * time.Millisecond)
	if adapterB.submitCount() != 0 {
		t.Fatalf("B must never submit on replay; submits=%d", adapterB.submitCount())
	}
	adapterB.mu.Lock()
	bPolls := adapterB.pollCalls
	adapterB.mu.Unlock()
	if bPolls != 0 {
		t.Fatalf("B must never start a duplicate monitor; polls=%d", bPolls)
	}
	if adapterA.submitCount() != 1 {
		t.Fatalf("total submit count=%d want exactly 1", adapterA.submitCount())
	}
	_ = rA.RequestCancel(job1.ID, testOwner(), CancelReasonNoLongerNeeded)
}

func TestLaunchIdempotentMismatchFailsClosed(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	if _, err := r.Launch(context.Background(), idemRequest("call-m")); err != nil {
		t.Fatalf("launch: %v", err)
	}
	baseline := adapter.submitCount()

	cases := []struct {
		name   string
		mutate func(*LaunchRequest)
	}{
		{"sql", func(q *LaunchRequest) { q.SQL = "select 2" }},
		{"owner run", func(q *LaunchRequest) { q.Owner.RunID = "run-OTHER" }},
		{"owner thread", func(q *LaunchRequest) { q.Owner.ThreadID = "thread-OTHER" }},
		{"replaces_job_id", func(q *LaunchRequest) { q.ReplacesJobID = "qj-old" }},
		{"owner token", func(q *LaunchRequest) { q.Owner.Handle = "tok_forged" }},
	}
	for _, tc := range cases {
		req := idemRequest("call-m")
		tc.mutate(&req)
		if _, err := r.Launch(context.Background(), req); !errors.Is(err, ErrIdempotencyMismatch) {
			t.Fatalf("%s mismatch: err=%v want ErrIdempotencyMismatch", tc.name, err)
		}
	}
	if adapter.submitCount() != baseline {
		t.Fatalf("mismatches must never submit: %d -> %d", baseline, adapter.submitCount())
	}
	// No extra durable records were created for this thread by mismatches.
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 1 {
		t.Fatalf("durable records=%d want 1 (fail closed writes nothing)", len(recs))
	}
}

func TestLaunchIdempotentRecoveryOfSubmissionUnknown(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{submitFn: ambiguousSubmit}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job1, err := r.Launch(context.Background(), idemRequest("call-u"))
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("launch 1: err=%v want ErrSubmissionUnknown", err)
	}
	submits := adapter.submitCount()

	job2, err := r.Launch(context.Background(), idemRequest("call-u"))
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("replay of unknown: err=%v want ErrSubmissionUnknown (reconcile, don't retry)", err)
	}
	if job2 == nil || job2.ID != job1.ID {
		t.Fatalf("replay must return the same unknown job: %+v vs %s", job2, job1.ID)
	}
	if adapter.submitCount() != submits {
		t.Fatalf("submission_unknown replay must NEVER replay the query: submits %d -> %d", submits, adapter.submitCount())
	}
}

// conflictReserveStore simulates the store-enforced idempotency uniqueness
// during ReserveQueryCapacity: the first reservation attempt for the
// guarded key persists the COMPETITOR (the other process's transaction
// winning at that instant) and returns jobs.ErrRevisionConflict, exactly
// as the JSON/MySQL stores now behave for a duplicate-key reservation.
type conflictReserveStore struct {
	*MemoryJobStore
	guardKey   string
	competitor JobRecord
	fired      atomic.Bool
}

func (s *conflictReserveStore) ReserveQueryCapacity(ctx context.Context, rec JobRecord, limits CapacityLimits) (bool, int64, error) {
	if rec.IdempotencyKey == s.guardKey && s.fired.CompareAndSwap(false, true) {
		if err := s.MemoryJobStore.UpsertQueryJob(ctx, s.competitor); err != nil {
			return false, 0, err
		}
		return false, 0, ErrRevisionConflict
	}
	return s.MemoryJobStore.ReserveQueryCapacity(ctx, rec, limits)
}

func TestLaunchReserveConflictRecoversStoreWinner(t *testing.T) {
	owner := JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_orig"}
	mkCompetitor := func(status JobStatus) JobRecord {
		return JobRecord{
			ID: "qj-fake-rcwin", Provider: "fake", Status: status,
			Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n-rc"},
			SQL:              "select 1",
			IdempotencyKey:   "call-rc",
			SubmittedAt:      time.Now().Add(-time.Second),
			LastAccess:       time.Now(),
			SubmitDispatched: status != JobStatusReserved,
		}
	}

	// Live competitor: recovery returns the original job/owner plainly.
	store := &conflictReserveStore{
		MemoryJobStore: NewMemoryJobStore(),
		guardKey:       "call-rc",
		competitor:     mkCompetitor(JobStatusRunning),
	}
	adapter := runningAdapter()
	// Fresh registry over the shared store: the competitor exists only
	// durably, never in this process's memory.
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-fresh"})
	r.RegisterAdapter(adapter)
	req := idemRequest("call-rc")
	req.Owner = JobOwner{ThreadID: "thread-1", RunID: "run-1"} // no token
	job, err := r.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("reserve-conflict launch: %v", err)
	}
	if job.ID != "qj-fake-rcwin" || job.Owner.Handle != "tok_orig" {
		t.Fatalf("must recover the store winner with its original owner: %s/%q", job.ID, job.Owner.Handle)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("conflict recovery must never submit; submits=%d", adapter.submitCount())
	}
	time.Sleep(30 * time.Millisecond)
	adapter.mu.Lock()
	polls := adapter.pollCalls
	adapter.mu.Unlock()
	if polls != 0 {
		t.Fatalf("conflict recovery must never start a monitor; polls=%d", polls)
	}
	// Exactly one durable record: the winner. The losing candidate was
	// never persisted or terminalized.
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 1 || recs[0].ID != "qj-fake-rcwin" || recs[0].Status != JobStatusRunning {
		t.Fatalf("store must hold only the untouched winner: %+v", recs)
	}

	// submission_unknown competitor: recovery maps to the sentinel.
	store2 := &conflictReserveStore{
		MemoryJobStore: NewMemoryJobStore(),
		guardKey:       "call-rc",
		competitor:     mkCompetitor(JobStatusSubmissionUnknown),
	}
	r2 := newTestRegistry(t, RegistryOptions{Store: store2})
	r2.RegisterAdapter(&fakeAdapter{name: "fake"})
	job2, err := r2.Launch(context.Background(), req)
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("unknown-state winner: err=%v want ErrSubmissionUnknown", err)
	}
	if job2 == nil || job2.ID != "qj-fake-rcwin" {
		t.Fatalf("unknown-state winner job=%+v", job2)
	}

	// Mismatched competitor: fail closed, winner untouched, nothing new.
	bad := mkCompetitor(JobStatusRunning)
	bad.SQL = "select 999"
	store3 := &conflictReserveStore{MemoryJobStore: NewMemoryJobStore(), guardKey: "call-rc", competitor: bad}
	adapter3 := runningAdapter()
	r3 := newTestRegistry(t, RegistryOptions{Store: store3})
	r3.RegisterAdapter(adapter3)
	if _, err := r3.Launch(context.Background(), req); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Fatalf("mismatched winner: err=%v want ErrIdempotencyMismatch", err)
	}
	if adapter3.submitCount() != 0 {
		t.Fatalf("mismatch after conflict must never submit")
	}
	recs3, _ := store3.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs3) != 1 || recs3[0].SQL != "select 999" {
		t.Fatalf("mismatched winner must remain untouched and alone: %+v", recs3)
	}
}

// TestReconcileRequestCarriesEffectiveBudgets proves a custom effective
// HardExecution survives into every reconciliation path — both a live
// ambiguous submit and a post-restart rehydration — so adapters (e.g.
// Snowflake's statement-timeout matching) reconcile against exactly what
// Submit configured, never the provider default. No resubmit occurs.
func TestReconcileRequestCarriesEffectiveBudgets(t *testing.T) {
	const customHard = 123 * time.Minute
	defaults := PolicyConfig{}.withDefaults()

	capture := func(got *ReconcileRequest, resolve func(int) ReconcileOutcome) *reconcilingAdapter {
		return &reconcilingAdapter{
			fakeAdapter: &fakeAdapter{
				submitFn: ambiguousSubmit,
				pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
					return StatusSnapshot{State: ProviderStateSucceeded}, nil
				},
				fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
			},
			reconcileFn: func(call int, req ReconcileRequest) (ReconcileOutcome, error) {
				*got = req
				return resolve(call), nil
			},
		}
	}

	// Path 1: live ambiguous submit.
	var liveReq ReconcileRequest
	adapter := capture(&liveReq, func(int) ReconcileOutcome {
		return ReconcileOutcome{Resolution: ReconcileFound, Handle: NativeHandle{Provider: "fake", ID: "found-b"}}
	})
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{HardExecution: customHard},
	})
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("launch: err=%v want ErrSubmissionUnknown", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "reconciled and completed")
	if liveReq.Budgets.HardExecution != customHard {
		t.Fatalf("live reconcile HardExecution=%s want custom %s", liveReq.Budgets.HardExecution, customHard)
	}
	if liveReq.Budgets.SoftExecution != defaults.DefaultBudgets.SoftExecution || liveReq.Budgets.MaxResultRows != defaults.DefaultBudgets.MaxResultRows {
		t.Fatalf("live reconcile must carry the EFFECTIVE (defaulted) budgets: %+v", liveReq.Budgets)
	}

	// Path 2: rehydration of a persisted submission_unknown record.
	store := NewMemoryJobStore()
	owner := testOwner()
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-budg", Provider: "fake", Status: JobStatusSubmissionUnknown,
		Owner: owner, SQL: "select 1", IdempotencyKey: "idem-budg",
		SubmittedAt: time.Now().Add(-time.Minute), LastAccess: time.Now(),
		SubmitDispatched: true,
		Budgets:          Budgets{HardExecution: customHard},
	})
	var rehydratedReq ReconcileRequest
	adapter2 := capture(&rehydratedReq, func(int) ReconcileOutcome {
		return ReconcileOutcome{Resolution: ReconcileFound, Handle: NativeHandle{Provider: "fake", ID: "found-c"}}
	})
	r2 := newTestRegistry(t, RegistryOptions{Store: store})
	r2.RegisterAdapter(adapter2)
	stats, err := r2.Rehydrate(context.Background())
	if err != nil || stats.Reconciling != 1 {
		t.Fatalf("rehydrate: stats=%+v err=%v", stats, err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r2, "qj-fake-budg", owner) == JobStatusCompleted
	}, "rehydrated reconcile completed")
	if rehydratedReq.Budgets.HardExecution != customHard {
		t.Fatalf("rehydrated reconcile HardExecution=%s want custom %s (provider default would be wrong)", rehydratedReq.Budgets.HardExecution, customHard)
	}
	if rehydratedReq.Budgets.SoftExecution != defaults.DefaultBudgets.SoftExecution {
		t.Fatalf("rehydrated reconcile budgets not effective/defaulted: %+v", rehydratedReq.Budgets)
	}
	if rehydratedReq.IdempotencyKey != "idem-budg" || rehydratedReq.SQL != "select 1" {
		t.Fatalf("identity semantics changed: %+v", rehydratedReq)
	}
	if adapter2.submitCount() != 0 {
		t.Fatalf("rehydrated reconciliation must never resubmit; submits=%d", adapter2.submitCount())
	}
}

// raceStore simulates the reservation race window: the FIRST idempotency
// lookup misses (as if the competitor's reservation was not yet visible),
// every later lookup delegates to the real store.
type raceStore struct {
	*MemoryJobStore
	calls atomic.Int32
}

func (s *raceStore) GetQueryJobByIdempotency(ctx context.Context, provider, connKey, key string) (JobRecord, bool, error) {
	if s.calls.Add(1) == 1 {
		return JobRecord{}, false, nil
	}
	return s.MemoryJobStore.GetQueryJobByIdempotency(ctx, provider, connKey, key)
}

func TestLaunchIdempotentReservationRaceAdoptsWinner(t *testing.T) {
	store := &raceStore{MemoryJobStore: NewMemoryJobStore()}
	owner := JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_orig"}
	// The competitor (older, therefore the deterministic winner) is already
	// durable — created by "another process" moments before our launch.
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-winner", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n-win"},
		SQL:              "select 1",
		IdempotencyKey:   "call-r",
		SubmittedAt:      time.Now().Add(-time.Minute),
		LastAccess:       time.Now(),
		SubmitDispatched: true,
	})
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	req := idemRequest("call-r")
	req.Owner = JobOwner{ThreadID: "thread-1", RunID: "run-1"} // no token
	job, err := r.Launch(context.Background(), req)
	if err != nil {
		t.Fatalf("race launch: %v", err)
	}
	if job.ID != "qj-fake-winner" || job.Owner.Handle != "tok_orig" {
		t.Fatalf("race must adopt the deterministic winner with its original owner: %s/%q", job.ID, job.Owner.Handle)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("the losing reservation must never submit; submits=%d", adapter.submitCount())
	}
	// Frozen reserve-transaction ordering: the store detects the
	// different-ID canonical winner BEFORE any capacity verdict, so the
	// losing candidate is never persisted at all — the winner is the only
	// durable record.
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 1 || recs[0].ID != "qj-fake-winner" {
		t.Fatalf("store must hold only the untouched winner: %+v", recs)
	}
}
