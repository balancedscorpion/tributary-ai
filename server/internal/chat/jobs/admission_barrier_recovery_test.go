package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func runningAdapter() *fakeAdapter {
	return &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
}

// --- admission ---

func TestAdmissionPerConnectionLimitQueuesThenAdmits(t *testing.T) {
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{
		Admission: &AdmissionConfig{MaxPerConnection: 2, MaxPerThread: 5, MaxGlobal: 8},
	})
	r.RegisterAdapter(adapter)

	launch := func(handle string) (*Job, error) {
		return r.Launch(context.Background(), LaunchRequest{
			Owner:        JobOwner{ThreadID: "thread-1", Handle: handle},
			Provider:     "fake",
			ConnectionID: "wh-1",
			SQL:          "select 1",
		})
	}
	j1, err := launch("tok_1")
	if err != nil {
		t.Fatalf("launch 1: %v", err)
	}
	if _, err := launch("tok_2"); err != nil {
		t.Fatalf("launch 2: %v", err)
	}

	// Third job on the same connection must queue, not fail.
	got := make(chan error, 1)
	go func() {
		_, err := launch("tok_3")
		got <- err
	}()
	waitFor(t, 2*time.Second, func() bool {
		r.admission.mu.Lock()
		defer r.admission.mu.Unlock()
		return r.admission.waiters.Len() == 1
	}, "third launch queued")

	// Freeing a slot admits the waiter.
	if err := r.RequestCancel(j1.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_1"}, CancelReasonSuperseded); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("queued launch: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("queued launch was not admitted after slot release")
	}
}

func TestAdmissionQueueBoundedAndTTL(t *testing.T) {
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{
		Admission: &AdmissionConfig{
			MaxPerConnection:   1,
			MaxQueuedGlobal:    1,
			MaxQueuedPerThread: 1,
			ReservationTTL:     30 * time.Millisecond,
		},
	})
	r.RegisterAdapter(adapter)
	launch := func(ctx context.Context, handle string) (*Job, error) {
		return r.Launch(ctx, LaunchRequest{
			Owner:        JobOwner{ThreadID: "thread-1", Handle: handle},
			Provider:     "fake",
			ConnectionID: "wh-1",
			SQL:          "select 1",
		})
	}
	if _, err := launch(context.Background(), "tok_1"); err != nil {
		t.Fatalf("launch 1: %v", err)
	}

	// Second launch queues, then times out at the reservation TTL.
	ttlErr := make(chan error, 1)
	go func() {
		_, err := launch(context.Background(), "tok_2")
		ttlErr <- err
	}()
	waitFor(t, 2*time.Second, func() bool {
		r.admission.mu.Lock()
		defer r.admission.mu.Unlock()
		return r.admission.waiters.Len() == 1
	}, "second launch queued")

	// Third launch finds the queue full.
	if _, err := launch(context.Background(), "tok_3"); !errors.Is(err, ErrAdmissionQueueFull) {
		t.Fatalf("err=%v want ErrAdmissionQueueFull", err)
	}

	select {
	case err := <-ttlErr:
		if !errors.Is(err, ErrAdmissionTimeout) {
			t.Fatalf("queued err=%v want ErrAdmissionTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("queued launch did not time out at reservation TTL")
	}
}

func TestAdmissionContextCancelWhileQueued(t *testing.T) {
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{
		Admission: &AdmissionConfig{MaxPerConnection: 1},
	})
	r.RegisterAdapter(adapter)
	if _, err := r.Launch(context.Background(), LaunchRequest{
		Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_1"}, Provider: "fake", ConnectionID: "wh-1", SQL: "select 1",
	}); err != nil {
		t.Fatalf("launch 1: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	got := make(chan error, 1)
	go func() {
		_, err := r.Launch(ctx, LaunchRequest{
			Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_2"}, Provider: "fake", ConnectionID: "wh-1", SQL: "select 1",
		})
		got <- err
	}()
	waitFor(t, 2*time.Second, func() bool {
		r.admission.mu.Lock()
		defer r.admission.mu.Unlock()
		return r.admission.waiters.Len() == 1
	}, "queued")
	cancel()
	select {
	case err := <-got:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("queued launch ignored context cancellation")
	}
}

func TestLegacyAndManagedLimitsAreIndependent(t *testing.T) {
	adapter := runningAdapter()
	r := newTestRegistry(t, RegistryOptions{
		MaxJobsPerThread: 1, MaxJobsGlobal: 1, // tight legacy limits
	})
	r.RegisterAdapter(adapter)

	// A managed job must not consume the legacy slot...
	if _, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"}); err != nil {
		t.Fatalf("launch: %v", err)
	}
	if _, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_legacy"},
		"bigquery", "select 2", "select 2", nil,
		func(ctx context.Context) (bool, *QueryResult, error) { <-ctx.Done(); return false, nil, ctx.Err() },
		nil,
	); err != nil {
		t.Fatalf("legacy register blocked by managed job: %v", err)
	}
	// ...and a second legacy job still hits the legacy limit.
	if _, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_legacy2"},
		"bigquery", "select 3", "select 3", nil,
		func(ctx context.Context) (bool, *QueryResult, error) { <-ctx.Done(); return false, nil, ctx.Err() },
		nil,
	); !errors.Is(err, ErrConcurrencyLimit) {
		t.Fatalf("err=%v want legacy ErrConcurrencyLimit", err)
	}
}

// --- barrier / wake events ---

func TestWaitForJobsAndExactlyOnceClaim(t *testing.T) {
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
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := r.WaitForJobs(ctx, testOwner(), []string{job.ID})
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if res.Event == nil || res.Event.Kind != WakeTerminal {
		t.Fatalf("wait result=%+v want unclaimed terminal wake", res)
	}

	claimed, err := r.ClaimWakeEvents(job.ID, testOwner())
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim=%v err=%v want exactly one event", claimed, err)
	}
	// Second claim is empty: exactly-once.
	claimed, err = r.ClaimWakeEvents(job.ID, testOwner())
	if err != nil || len(claimed) != 0 {
		t.Fatalf("second claim=%v err=%v want none", claimed, err)
	}

	// With everything claimed and the job terminal, WaitForJobs returns the
	// all-terminal barrier result (Event nil) instead of blocking.
	res, err = r.WaitForJobs(ctx, testOwner(), []string{job.ID})
	if err != nil {
		t.Fatalf("wait 2: %v", err)
	}
	if res.Event != nil || res.Job == nil || res.Job.ID != job.ID {
		t.Fatalf("res=%+v want all-terminal barrier result", res)
	}

	// Owner mismatch is a hard error, not a hang.
	if _, err := r.WaitForJobs(ctx, JobOwner{ThreadID: "thread-1", Handle: "tok_wrong"}, []string{job.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err=%v want ErrNotFound for mismatched owner", err)
	}
}

// --- recovery / rehydration / reaper ---

func TestRehydrateAdoptsResumesAndCompletes(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	rec := JobRecord{
		ID: "qj-fake-adopt1", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "native-9"},
		SubmittedAt: time.Now().Add(-time.Minute), LastAccess: time.Now(),
		KeepOnRunCompletion: true,
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true},
		adoptFn: func(h NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			if h.ID != "native-9" {
				return AdoptOutcomeOrphaned, StatusSnapshot{}, nil
			}
			return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
		},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if call < 2 {
				return StatusSnapshot{State: ProviderStateRunning}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{Rows: []map[string]any{{"n": 1}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Adopted != 1 {
		t.Fatalf("stats=%+v want 1 adopted", stats)
	}
	waitFor(t, 2*time.Second, func() bool {
		rec, ok, _ := store.GetQueryJob(context.Background(), "qj-fake-adopt1")
		return ok && rec.Status == JobStatusCompleted
	}, "adopted job completes durably")
}

func TestRehydrateOutcomes(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now()
	seed := func(id string, status JobStatus, handle NativeHandle, provider string) {
		_ = store.UpsertQueryJob(context.Background(), JobRecord{
			ID: id, Provider: provider, Status: status, Owner: owner,
			Handle: handle, SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		})
	}
	seed("qj-fake-term", JobStatusRunning, NativeHandle{Provider: "fake", ID: "done"}, "fake")
	seed("qj-fake-orph", JobStatusRunning, NativeHandle{Provider: "fake", ID: "gone"}, "fake")
	seed("qj-fake-resv", JobStatusReserved, NativeHandle{}, "fake")
	seed("qj-none-1", JobStatusRunning, NativeHandle{Provider: "missing", ID: "x"}, "missing-provider")

	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true},
		adoptFn: func(h NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			switch h.ID {
			case "done":
				return AdoptOutcomeTerminal, StatusSnapshot{State: ProviderStateSucceeded}, nil
			default:
				return AdoptOutcomeOrphaned, StatusSnapshot{Error: "query expired server-side"}, nil
			}
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Terminal != 1 || stats.Orphaned != 2 || stats.Expired != 1 {
		t.Fatalf("stats=%+v want terminal=1 orphaned=2 expired=1", stats)
	}
	if got := snapshotStatus(r, "qj-fake-term", owner); got != JobStatusCompleted {
		t.Fatalf("terminal adoption status=%s want completed", got)
	}
	orph, _ := r.Snapshot("qj-fake-orph", owner)
	if orph == nil || orph.Status != JobStatusOrphaned {
		t.Fatalf("orphan status=%v want orphaned", orph)
	}
	hasRecovery := false
	for _, ev := range orph.WakeEvents {
		if ev.Kind == WakeRecoveryRequired && !ev.Claimed {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("orphaned job must carry recovery_required wake; got %+v", orph.WakeEvents)
	}
	if got := snapshotStatus(r, "qj-fake-resv", owner); got != JobStatusExpired {
		t.Fatalf("stale reservation status=%s want expired", got)
	}
	// Rehydrate is idempotent: a second pass skips everything in memory.
	stats2, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate 2: %v", err)
	}
	if stats2.Adopted+stats2.Terminal+stats2.Orphaned+stats2.Expired != 0 {
		t.Fatalf("second rehydrate must be a no-op; stats=%+v", stats2)
	}
}

func TestReaperRemovesTerminalJobsFromMemory(t *testing.T) {
	r := newTestRegistry(t, RegistryOptions{RetentionTTL: time.Millisecond})
	job, err := r.Register(
		JobOwner{ThreadID: "thread-1", Handle: "tok_reap"},
		"bigquery", "select 1", "select 1", nil,
		func(ctx context.Context) (bool, *QueryResult, error) {
			return true, &QueryResult{Metadata: map[string]any{"terminal_status": "completed"}}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	owner := JobOwner{ThreadID: "thread-1", Handle: "tok_reap"}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, owner) == JobStatusCompleted
	}, "completed")
	time.Sleep(5 * time.Millisecond)
	r.reapOnce()
	if _, ok := r.Snapshot(job.ID, owner); ok {
		t.Fatalf("terminal job should have been reaped from memory")
	}
}

func TestManagedTransitionsAreValidatedForwardOnly(t *testing.T) {
	cases := []struct {
		from, to JobStatus
		want     bool
	}{
		{JobStatusReserved, JobStatusSubmitted, true},
		{JobStatusReserved, JobStatusRunning, false},
		{JobStatusSubmitted, JobStatusCancelRequested, true},
		{JobStatusCancelRequested, JobStatusCompleted, true}, // completion wins
		{JobStatusCancelling, JobStatusCancelled, true},
		{JobStatusCompleted, JobStatusRunning, false}, // terminal is final
		{JobStatusCancelled, JobStatusCancelRequested, false},
		{JobStatusSubmissionUnknown, JobStatusSubmitted, true},
		{JobStatusSubmissionUnknown, JobStatusOrphaned, true},
		{JobStatusRunning, JobStatusExpired, true},
		{JobStatusExpired, JobStatusCompleted, false},
	}
	for _, tc := range cases {
		if got := canTransition(tc.from, tc.to); got != tc.want {
			t.Fatalf("canTransition(%s -> %s)=%v want %v", tc.from, tc.to, got, tc.want)
		}
	}
}
