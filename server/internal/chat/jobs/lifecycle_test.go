package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// --- shared test fixtures ---

func fastScheduler() *SchedulerConfig {
	return &SchedulerConfig{
		BaseIntervals:          []time.Duration{time.Millisecond},
		MaxInterval:            2 * time.Millisecond,
		JitterFrac:             0.01,
		ErrorBackoff:           []time.Duration{time.Millisecond},
		MaxErrorBackoff:        2 * time.Millisecond,
		DegradedAfter:          2,
		LostAfter:              time.Hour,
		CancelUnconfirmedAfter: 40 * time.Millisecond,
	}
}

func newTestRegistry(t *testing.T, opts RegistryOptions) *Registry {
	t.Helper()
	if opts.Scheduler == nil {
		opts.Scheduler = fastScheduler()
	}
	return NewRegistry(opts)
}

type fakeAdapter struct {
	mu               sync.Mutex
	name             string
	caps             ProviderCapabilities
	submitFn         func(call int, req SubmitRequest) (SubmitResult, error)
	submitCalls      int
	pollFn           func(call int, h NativeHandle) (StatusSnapshot, error)
	pollCalls        int
	polledHandles    []NativeHandle
	fetchFn          func(call int, cursor FetchCursor) (ResultPage, error)
	fetchCalls       int
	cancelErr        error
	cancelCalls      int
	cancelledHandles []NativeHandle
	confirmFn        func(call int) (bool, StatusSnapshot, error)
	confirmCalls     int
	adoptFn          func(h NativeHandle) (AdoptOutcome, StatusSnapshot, error)
}

func (f *fakeAdapter) Provider() string {
	if f.name == "" {
		return "fake"
	}
	return f.name
}

func (f *fakeAdapter) Capabilities() ProviderCapabilities { return f.caps }

func (f *fakeAdapter) Submit(_ context.Context, req SubmitRequest) (SubmitResult, error) {
	f.mu.Lock()
	f.submitCalls++
	call := f.submitCalls
	fn := f.submitFn
	f.mu.Unlock()
	if fn != nil {
		return fn(call, req)
	}
	return SubmitResult{Handle: NativeHandle{Provider: f.Provider(), ID: "native-1"}}, nil
}

func (f *fakeAdapter) Poll(_ context.Context, h NativeHandle) (StatusSnapshot, error) {
	f.mu.Lock()
	f.pollCalls++
	call := f.pollCalls
	fn := f.pollFn
	f.polledHandles = append(f.polledHandles, cloneHandle(h))
	f.mu.Unlock()
	if fn != nil {
		return fn(call, h)
	}
	return StatusSnapshot{State: ProviderStateSucceeded}, nil
}

func (f *fakeAdapter) firstPolledHandle() (NativeHandle, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.polledHandles) == 0 {
		return NativeHandle{}, false
	}
	return f.polledHandles[0], true
}

func (f *fakeAdapter) Fetch(_ context.Context, _ NativeHandle, cursor FetchCursor) (ResultPage, error) {
	f.mu.Lock()
	f.fetchCalls++
	call := f.fetchCalls
	fn := f.fetchFn
	f.mu.Unlock()
	if fn != nil {
		return fn(call, cursor)
	}
	return ResultPage{}, nil
}

func (f *fakeAdapter) Cancel(_ context.Context, h NativeHandle) error {
	f.mu.Lock()
	f.cancelCalls++
	f.cancelledHandles = append(f.cancelledHandles, cloneHandle(h))
	err := f.cancelErr
	f.mu.Unlock()
	return err
}

func (f *fakeAdapter) ConfirmCancel(_ context.Context, _ NativeHandle) (bool, StatusSnapshot, error) {
	f.mu.Lock()
	f.confirmCalls++
	call := f.confirmCalls
	fn := f.confirmFn
	f.mu.Unlock()
	if fn != nil {
		return fn(call)
	}
	return true, StatusSnapshot{State: ProviderStateCancelled}, nil
}

func (f *fakeAdapter) Adopt(_ context.Context, h NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
	if f.adoptFn != nil {
		return f.adoptFn(h)
	}
	return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
}

func (f *fakeAdapter) submitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.submitCalls
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for: %s", msg)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func testOwner() JobOwner {
	return JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_test"}
}

func snapshotStatus(r *Registry, id string, owner JobOwner) JobStatus {
	snap, ok := r.Snapshot(id, owner)
	if !ok {
		return JobStatusUnknown
	}
	return snap.Status
}

// --- Launch lifecycle ---

func TestLaunchHappyPathReserveSubmitRunComplete(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{NativePagination: true, CancelConfirmable: true, DetachedRepoll: true},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if call == 1 {
				return StatusSnapshot{State: ProviderStateRunning}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(_ int, _ FetchCursor) (ResultPage, error) {
			return ResultPage{
				Rows:   []map[string]any{{"n": 1}, {"n": 2}},
				Schema: []map[string]any{{"name": "n"}},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusSubmitted {
		t.Fatalf("status after launch=%s want submitted", job.Status)
	}
	if job.Handle.ID != "native-1" {
		t.Fatalf("handle=%+v want native-1", job.Handle)
	}
	if !job.KeepOnRunCompletion {
		t.Fatalf("KeepOnRunCompletion should default true")
	}
	if job.IdempotencyKey == "" {
		t.Fatalf("idempotency key must be minted before submit")
	}

	owner := testOwner()
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, owner) == JobStatusCompleted
	}, "job completed")

	snap, _ := r.Snapshot(job.ID, owner)
	if snap.Result == nil || len(snap.Result.Rows) != 2 {
		t.Fatalf("result=%+v want 2 rows", snap.Result)
	}
	if snap.Materialization != MaterializationComplete {
		t.Fatalf("materialization=%s want complete", snap.Materialization)
	}
	if snap.StartedAt == nil {
		t.Fatalf("StartedAt should be set on first running observation")
	}
	foundTerminalWake := false
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeTerminal && !ev.Claimed {
			foundTerminalWake = true
		}
	}
	if !foundTerminalWake {
		t.Fatalf("expected unclaimed terminal wake event, got %+v", snap.WakeEvents)
	}

	rec, ok, err := store.GetQueryJob(context.Background(), job.ID)
	if err != nil || !ok {
		t.Fatalf("stored record missing: ok=%v err=%v", ok, err)
	}
	if rec.Status != JobStatusCompleted {
		t.Fatalf("stored status=%s want completed", rec.Status)
	}
	if rec.Handle.ID != "native-1" {
		t.Fatalf("stored handle=%+v want native-1", rec.Handle)
	}
}

type failingStore struct{ *MemoryJobStore }

func (s *failingStore) UpsertQueryJob(_ context.Context, _ JobRecord) error {
	return errors.New("disk full")
}

// Also fail the CAS path: the embedded MemoryJobStore implements
// RevisionedStore, and persistJobStrict prefers CAS when available.
func (s *failingStore) UpsertQueryJobCAS(_ context.Context, _ JobRecord, _ int64) (int64, error) {
	return 0, errors.New("disk full")
}

func TestLaunchReservePersistenceFailureFailsBeforeSubmit(t *testing.T) {
	adapter := &fakeAdapter{}
	r := newTestRegistry(t, RegistryOptions{Store: &failingStore{NewMemoryJobStore()}})
	r.RegisterAdapter(adapter)

	_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err == nil {
		t.Fatalf("expected reserve persistence failure")
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("submit must not run when the reservation cannot be persisted (invariant I1); calls=%d", adapter.submitCount())
	}
}

func TestSubmitNotDeliveredFailsSafelyAndRetries(t *testing.T) {
	adapter := &fakeAdapter{
		submitFn: func(call int, _ SubmitRequest) (SubmitResult, error) {
			return SubmitResult{}, SubmitNotDelivered(errors.New("connection refused"))
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err == nil {
		t.Fatalf("expected submit failure")
	}
	if errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("not-delivered must be a hard failure, not submission_unknown")
	}
	if job == nil || job.Status != JobStatusFailed {
		t.Fatalf("job=%+v want failed", job)
	}
	// Not-delivered errors never left the process, so retrying is safe for
	// every provider.
	if adapter.submitCount() != 3 {
		t.Fatalf("submit attempts=%d want 3", adapter.submitCount())
	}
}

func TestAmbiguousSubmitBecomesSubmissionUnknownWithoutRetry(t *testing.T) {
	adapter := &fakeAdapter{
		submitFn: func(call int, _ SubmitRequest) (SubmitResult, error) {
			return SubmitResult{}, errors.New("i/o timeout mid-request")
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("err=%v want ErrSubmissionUnknown", err)
	}
	if job.Status != JobStatusSubmissionUnknown {
		t.Fatalf("status=%s want submission_unknown", job.Status)
	}
	if adapter.submitCount() != 1 {
		t.Fatalf("ambiguous submit without idempotency must never retry; calls=%d", adapter.submitCount())
	}
	hasWake := false
	for _, ev := range job.WakeEvents {
		if ev.Kind == WakeSubmissionUnknown {
			hasWake = true
		}
	}
	if !hasWake {
		t.Fatalf("expected submission_unknown wake event")
	}
}

func TestAmbiguousSubmitRetriesWithIdempotencyKey(t *testing.T) {
	var seenKeys []string
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{IdempotentSubmit: true},
		submitFn: func(call int, req SubmitRequest) (SubmitResult, error) {
			seenKeys = append(seenKeys, req.IdempotencyKey)
			if call == 1 {
				return SubmitResult{}, errors.New("i/o timeout mid-request")
			}
			return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "native-2"}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusSubmitted {
		t.Fatalf("status=%s want submitted", job.Status)
	}
	if len(seenKeys) != 2 || seenKeys[0] == "" || seenKeys[0] != seenKeys[1] {
		t.Fatalf("idempotent retry must reuse the same key; keys=%v", seenKeys)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "job completed after idempotent retry")
}

func TestReconcileSubmissionToSubmittedAndToFailed(t *testing.T) {
	mkUnknown := func(r *Registry) *Job {
		adapter := &fakeAdapter{
			submitFn: func(int, SubmitRequest) (SubmitResult, error) {
				return SubmitResult{}, errors.New("ambiguous")
			},
			pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
				return StatusSnapshot{State: ProviderStateSucceeded}, nil
			},
			fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{}, nil },
		}
		r.RegisterAdapter(adapter)
		job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
		if !errors.Is(err, ErrSubmissionUnknown) {
			t.Fatalf("setup: err=%v", err)
		}
		return job
	}

	r1 := newTestRegistry(t, RegistryOptions{})
	jobA := mkUnknown(r1)
	if err := r1.ReconcileSubmission(jobA.ID, testOwner(), NativeHandle{Provider: "fake", ID: "found-1"}, nil); err != nil {
		t.Fatalf("reconcile to submitted: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r1, jobA.ID, testOwner()) == JobStatusCompleted
	}, "reconciled job completes via monitor")

	r2 := newTestRegistry(t, RegistryOptions{})
	jobB := mkUnknown(r2)
	if err := r2.ReconcileSubmission(jobB.ID, testOwner(), NativeHandle{}, nil); err != nil {
		t.Fatalf("reconcile to failed: %v", err)
	}
	if got := snapshotStatus(r2, jobB.ID, testOwner()); got != JobStatusFailed {
		t.Fatalf("status=%s want failed", got)
	}

	// Reconcile on a non-unknown job is rejected.
	if err := r2.ReconcileSubmission(jobB.ID, testOwner(), NativeHandle{}, nil); !errors.Is(err, ErrNotReconcilable) {
		t.Fatalf("err=%v want ErrNotReconcilable", err)
	}
}

func TestConfirmedCancelLifecycle(t *testing.T) {
	confirmed := make(chan struct{})
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
		confirmFn: func(call int) (bool, StatusSnapshot, error) {
			if call == 1 {
				return false, StatusSnapshot{State: ProviderStateRunning}, nil
			}
			select {
			case <-confirmed:
			default:
				close(confirmed)
			}
			return true, StatusSnapshot{State: ProviderStateCancelled}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	// The job must pass through cancel_requested/cancelling and only reach
	// cancelled once the provider confirmed (invariant I2).
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCancelled
	}, "confirmed cancellation")

	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelReason != CancelReasonUserRequest {
		t.Fatalf("cancel reason=%s want user_request", snap.CancelReason)
	}
	if snap.CancelOutcome != CancelOutcomeConfirmed {
		t.Fatalf("cancel outcome=%s want confirmed", snap.CancelOutcome)
	}
	// Idempotent re-cancel.
	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("re-cancel: %v", err)
	}
}

func TestCancelReasonRequired(t *testing.T) {
	r := newTestRegistry(t, RegistryOptions{})
	if err := r.RequestCancel("qj-x", testOwner(), ""); !errors.Is(err, ErrCancelReasonRequired) {
		t.Fatalf("err=%v want ErrCancelReasonRequired", err)
	}
}

func TestCompletionWinsOverCancellation(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true, NativePagination: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
		confirmFn: func(int) (bool, StatusSnapshot, error) {
			// The provider finished before the cancel landed.
			return false, StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{Rows: []map[string]any{{"n": 1}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completion wins")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelOutcome != CancelOutcomeSupersededByCompletion {
		t.Fatalf("outcome=%s want superseded_by_completion", snap.CancelOutcome)
	}
	if snap.Result == nil || len(snap.Result.Rows) != 1 {
		t.Fatalf("completed-won job must keep its results; got %+v", snap.Result)
	}
}

func TestFetchPagePolicies(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{NativePagination: true},
		fetchFn: func(call int, cursor FetchCursor) (ResultPage, error) {
			return ResultPage{Rows: []map[string]any{{"page": cursor.PageToken}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completed")

	if _, err := r.FetchPage(context.Background(), job.ID, testOwner(), FetchCursor{PageToken: "p2"}); err != nil {
		t.Fatalf("fetch page: %v", err)
	}
	if _, err := r.FetchPage(context.Background(), job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_wrong"}, FetchCursor{}); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("err=%v want ownership mismatch", err)
	}
}
