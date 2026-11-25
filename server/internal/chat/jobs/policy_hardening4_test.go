package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHardBudgetEscapesPermanentlyBlockedDrainSink(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	committer.setFailWhen(func(commit DrainCommit) error {
		if commit.Seq >= 2 {
			return errors.New("durable result sink unavailable")
		}
		return nil
	})
	h1 := drainHandle("query", "uri-1")
	h2 := drainHandle("query", "uri-2")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: h1}, nil
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{
				State:         ProviderStateRunning,
				UpdatedHandle: &h2,
				DrainedPages: []ResultPage{{
					Rows:        []map[string]any{{"n": 1}},
					CursorAfter: &h2,
				}},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{
		Store:        store,
		Materializer: committer,
		Policy: &PolicyConfig{
			HardExecution:  45 * time.Millisecond,
			DefaultBudgets: Budgets{HardExecution: 45 * time.Millisecond},
		},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner: testOwner(), Provider: "fake", SQL: "select 1",
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusOrphaned
	}, "hard-budget emergency cancellation of blocked drain")

	adapter.mu.Lock()
	if len(adapter.cancelledHandles) == 0 {
		adapter.mu.Unlock()
		t.Fatal("blocked sink kept the remote query running indefinitely")
	}
	cancelHandle := cloneHandle(adapter.cancelledHandles[0])
	adapter.mu.Unlock()
	if got := cancelHandle.Extra["next_uri"]; got != "uri-2" {
		t.Fatalf("emergency cancel cursor=%q want newest observed uri-2", got)
	}
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelOutcome != CancelOutcomeUnconfirmed {
		t.Fatalf("cancel outcome=%s want honest unconfirmed", snap.CancelOutcome)
	}
	if snap.Metadata["needs_reconciliation"] != true {
		t.Fatalf("emergency escape must require reconciliation: %+v", snap.Metadata)
	}
	for _, commit := range committer.drainCommits() {
		if commit.Seq >= 2 {
			t.Fatalf("failed result page was falsely committed: %+v", commit)
		}
	}
}

func TestCancelRetryAfterSurvivesLeaseTakeover(t *testing.T) {
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	deadline := now.Add(100 * time.Millisecond)
	cancelStarted := now
	rec := JobRecord{
		ID: "qj-cancel-backpressure", Provider: "fake", Status: JobStatusCancelRequested,
		Owner: testOwner(), Handle: NativeHandle{Provider: "fake", ID: "remote"},
		SubmittedAt: now.Add(-time.Minute), RemoteSubmittedAt: &now, LastAccess: now,
		Budgets:         Budgets{HardExecution: time.Hour},
		CancelReason:    CancelReasonUserRequest,
		CancelStartedAt: &cancelStarted,
		NextPollAt:      &deadline,
		NextPollAction:  RetryActionCancel,
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true, CancelConfirmable: true},
		adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	sched := fastScheduler()
	sched.CancelUnconfirmedAfter = time.Second
	r := newTestRegistry(t, RegistryOptions{Store: store, Scheduler: sched, InstanceID: "takeover"})
	r.RegisterAdapter(adapter)
	started := time.Now()
	if _, err := r.Rehydrate(context.Background()); err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		return adapter.cancelCalls > 0
	}, "cancel after durable provider backpressure")
	if elapsed := time.Since(started); elapsed < 75*time.Millisecond {
		t.Fatalf("takeover hammered Cancel before Retry-After: %s", elapsed)
	}
}

func TestPerpetualCancelErrorsBecomeRecoveryRequiredAfterGrace(t *testing.T) {
	adapter := &fakeAdapter{
		caps:      ProviderCapabilities{CancelConfirmable: true},
		cancelErr: WithRetryAfter(errors.New("cancel endpoint unavailable"), time.Hour),
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	sched := fastScheduler()
	sched.CancelUnconfirmedAfter = 55 * time.Millisecond
	r := newTestRegistry(t, RegistryOptions{
		Scheduler: sched,
		Policy: &PolicyConfig{
			HardExecution:  25 * time.Millisecond,
			DefaultBudgets: Budgets{HardExecution: 25 * time.Millisecond},
		},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusExpired
	}, "unconfirmable cancellation grace expiry")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelOutcome != CancelOutcomeUnconfirmed {
		t.Fatalf("cancel outcome=%s want unconfirmed", snap.CancelOutcome)
	}
	foundRecovery := false
	for _, event := range snap.WakeEvents {
		if event.Kind == WakeRecoveryRequired {
			foundRecovery = true
		}
	}
	if !foundRecovery {
		t.Fatalf("missing recovery-required wake: %+v", snap.WakeEvents)
	}
}

func TestTerminalSubmissionReconciliationFetchesAndMaterializesResults(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &reconcilingAdapter{
		fakeAdapter: &fakeAdapter{
			submitFn: ambiguousSubmit,
			fetchFn: func(int, FetchCursor) (ResultPage, error) {
				return ResultPage{Rows: []map[string]any{{"answer": 42}}}, nil
			},
		},
		reconcileFn: func(int, ReconcileRequest) (ReconcileOutcome, error) {
			return ReconcileOutcome{
				Resolution: ReconcileFound,
				Handle:     NativeHandle{Provider: "fake", ID: "terminal-remote"},
				Snapshot:   &StatusSnapshot{State: ProviderStateSucceeded},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 42"})
	if !errors.Is(err, ErrSubmissionUnknown) {
		t.Fatalf("launch err=%v want ErrSubmissionUnknown", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "terminal reconciliation finalization")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Handle.ID != "terminal-remote" {
		t.Fatalf("reconciled handle was lost: %+v", snap.Handle)
	}
	if snap.Result == nil || len(snap.Result.Rows) != 1 || snap.Result.Rows[0]["answer"] != 42 {
		t.Fatalf("terminal reconciliation lost results: %+v", snap.Result)
	}
	adapter.mu.Lock()
	fetches := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetches == 0 {
		t.Fatal("terminal reconciliation bypassed normal result fetch")
	}
	record, ok, getErr := store.GetQueryJob(context.Background(), job.ID)
	if getErr != nil || !ok || record.Handle.ID != "terminal-remote" {
		t.Fatalf("reconciled handle not durable: ok=%v err=%v record=%+v", ok, getErr, record)
	}
}
