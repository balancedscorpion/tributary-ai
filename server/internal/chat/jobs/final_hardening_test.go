package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// loaderMaterializer extends fakeMaterializer with the TerminalResultLoader
// capability (reconstructing immutable results from durable chunks).
type loaderMaterializer struct {
	fakeMaterializer
	loadResult  *QueryResult
	loadErr     error
	loadCalls   atomic.Int32
	loadedOwner atomic.Value // JobOwner
}

func (m *loaderMaterializer) LoadQueryResult(_ context.Context, _ string, owner JobOwner) (*QueryResult, MaterializationSummary, error) {
	m.loadCalls.Add(1)
	m.loadedOwner.Store(owner)
	if m.loadErr != nil {
		return nil, MaterializationSummary{}, m.loadErr
	}
	rows := 0
	if m.loadResult != nil {
		rows = len(m.loadResult.Rows)
	}
	return m.loadResult, MaterializationSummary{Rows: rows}, nil
}

// --- item 2: owner-scoped lazy durable restore after restart ---

func TestLazyRestoreOfTerminalJobAfterRestart(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now()
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-lazy", Provider: "fake", Status: JobStatusCompleted,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n1"},
		SubmittedAt: now.Add(-time.Hour), LastAccess: now.Add(-time.Hour),
		CompletedAt: &now, SubmitDispatched: true, DrainSeq: 3,
		MaterializationState: MaterializationComplete,
		ValidationState:      ValidationUnvalidated,
		ArtifactID:           "art-lazy",
		WakeEvents:           []WakeEvent{{At: now, Kind: WakeTerminal, Detail: "completed"}},
	})
	mat := &loaderMaterializer{loadResult: &QueryResult{Rows: []map[string]any{{"n": 1}, {"n": 2}}}}
	// Fresh registry: simulates a restarted process that never Rehydrated
	// this record (Rehydrate only walks ACTIVE records).
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: mat})
	r.RegisterAdapter(&fakeAdapter{caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true}})

	// Wrong owner: no restore, indistinguishable from nonexistent.
	if _, ok := r.Snapshot("qj-fake-lazy", JobOwner{ThreadID: "thread-1", Handle: "tok_wrong"}); ok {
		t.Fatalf("lazy restore must not weaken authorization")
	}
	if _, ok := r.Snapshot("qj-fake-lazy", JobOwner{ThreadID: "thread-2", Handle: owner.Handle}); ok {
		t.Fatalf("lazy restore must be owner-scoped by thread too")
	}

	// Right owner: status, results, wakes, and validation all work.
	snap, ok := r.Snapshot("qj-fake-lazy", owner)
	if !ok || snap.Status != JobStatusCompleted {
		t.Fatalf("lazy snapshot failed: ok=%v snap=%+v", ok, snap)
	}
	if snap.ArtifactID != "art-lazy" || snap.DrainSeq != 3 {
		t.Fatalf("restored fields lost: %+v", snap)
	}
	res, err := r.Results("qj-fake-lazy", owner)
	if err != nil || res == nil || len(res.Rows) != 2 {
		t.Fatalf("results after restart: res=%+v err=%v (must come from durable chunks, no SQL rerun)", res, err)
	}
	if mat.loadCalls.Load() != 1 {
		t.Fatalf("loader calls=%d want 1 (loaded once at restore)", mat.loadCalls.Load())
	}
	claimed, err := r.ClaimWakeEvents("qj-fake-lazy", owner)
	if err != nil || len(claimed) != 1 || claimed[0].Kind != WakeTerminal {
		t.Fatalf("claim after restart: %v / %+v", err, claimed)
	}
	if again, _ := r.ClaimWakeEvents("qj-fake-lazy", owner); len(again) != 0 {
		t.Fatalf("claim must stay exactly-once after restart")
	}
	if err := r.SetValidation("qj-fake-lazy", owner, ValidationValidated, "post-restart validation"); err != nil {
		t.Fatalf("post-restart validation: %v", err)
	}
	rec, _, _ := store.GetQueryJob(context.Background(), "qj-fake-lazy")
	if rec.ValidationState != ValidationValidated {
		t.Fatalf("validation not durable after restart: %s", rec.ValidationState)
	}
}

func TestLazyRestoreNonTerminalNeverStartsDriving(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now()
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-lazyrun", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n1"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now, SubmitDispatched: true, DrainSeq: 1,
	})
	adapter := &fakeAdapter{caps: ProviderCapabilities{DetachedRepoll: true, CancelConfirmable: true}}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: &fakeMaterializer{}})
	r.RegisterAdapter(adapter)

	snap, ok := r.Snapshot("qj-fake-lazyrun", owner)
	if !ok || snap.Status != JobStatusRunning {
		t.Fatalf("lazy non-terminal snapshot: ok=%v snap=%+v", ok, snap)
	}
	time.Sleep(30 * time.Millisecond)
	adapter.mu.Lock()
	polls := adapter.pollCalls
	adapter.mu.Unlock()
	if polls != 0 {
		t.Fatalf("lazy restore must NEVER start monitors/pollers; polls=%d", polls)
	}
	// Mutating ops on the lazily-loaded job still work (cross-process
	// cancel is legitimate; the CAS/lease layers arbitrate).
	if err := r.RequestCancel("qj-fake-lazyrun", owner, CancelReasonUserRequest); err != nil {
		t.Fatalf("cancel on lazily-loaded job: %v", err)
	}
	if got := snapshotStatus(r, "qj-fake-lazyrun", owner); got != JobStatusCancelRequested {
		t.Fatalf("status=%s want cancel_requested", got)
	}
}

// --- item 4: unconfirmable cancellation cannot hold capacity forever ---

func TestUnconfirmedCancelExpiresAtHardBudget(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
		confirmFn: func(int) (bool, StatusSnapshot, error) {
			return false, StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	sched := fastScheduler()
	sched.CancelUnconfirmedAfter = 20 * time.Millisecond
	store := NewMemoryJobStore()
	r := newTestRegistry(t, RegistryOptions{
		Store:     store,
		Scheduler: sched,
		Admission: &AdmissionConfig{MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1, CrossProcessRetryInterval: 20 * time.Millisecond},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{HardExecution: 80 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusExpired
	}, "expired at hard budget despite unconfirmable cancel")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelOutcome != CancelOutcomeUnconfirmed {
		t.Fatalf("outcome=%s must stay UNCONFIRMED (honest) even when budget-terminalized", snap.CancelOutcome)
	}
	if snap.Handle.ID == "" {
		t.Fatalf("native handle must remain for later reconciliation")
	}
	// The durable snapshot must scream "needs attention", never read as a
	// cleanly cancelled query.
	if snap.PolicyHealth != PolicyHealthHardExceeded {
		t.Fatalf("policy health=%s want hard_exceeded on unconfirmed expiry", snap.PolicyHealth)
	}
	if snap.Metadata["needs_reconciliation"] != true || snap.Metadata["remote_state_at_expiry"] != "unconfirmed" {
		t.Fatalf("needs-reconciliation markers missing: %+v", snap.Metadata)
	}
	hasRecoveryWake := false
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeRecoveryRequired {
			hasRecoveryWake = true
		}
	}
	if !hasRecoveryWake {
		t.Fatalf("unconfirmed expiry must carry recovery_required alongside terminal")
	}
	rec, _, _ := store.GetQueryJob(context.Background(), job.ID)
	if rec.Metadata["needs_reconciliation"] != true {
		t.Fatalf("needs_reconciliation must be durable: %+v", rec.Metadata)
	}
	// Capacity is released: a fresh launch admits without waiting.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ok2 := &fakeAdapter{name: "fake", fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil }}
	r.RegisterAdapter(ok2)
	if _, err := r.Launch(ctx, LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_next"}, Provider: "fake", SQL: "select 2"}); err != nil {
		t.Fatalf("capacity not released after unconfirmed-cancel expiry: %v", err)
	}
}

// --- item 1: commit retry pacing is a single (caller-owned) backoff ---

func TestDrainCommitRetryPacingIsSingleBackoff(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	var attempts atomic.Int32
	committer.setFailWhen(func(commit DrainCommit) error {
		if commit.Seq >= 2 {
			attempts.Add(1)
			return errors.New("sink down")
		}
		return nil
	})
	h2 := drainHandle("q", "uri-2")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: drainHandle("q", "uri-1")}, nil
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{
				State:         ProviderStateRunning,
				UpdatedHandle: &h2,
				DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 1}}, CursorAfter: &h2}},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	r.RegisterAdapter(adapter)
	if _, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	// With single-shot commits paced only by the monitor's errorInterval
	// (1ms here), retries accumulate quickly. The old nested-sleep double
	// backoff managed only ~3-6 attempts in this window.
	time.Sleep(400 * time.Millisecond)
	if got := attempts.Load(); got < 8 {
		t.Fatalf("commit retry attempts=%d in 400ms; double backoff suspected (want >= 8)", got)
	}
}
