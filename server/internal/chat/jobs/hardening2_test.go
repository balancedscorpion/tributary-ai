package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// casCommitter simulates the real store-backed committer: the drain commit
// is a CAS write of the job record (chunk recorded alongside), returning
// the new lifecycle revision. failWhen lets tests inject failures.
type casCommitter struct {
	store    *MemoryJobStore
	mu       sync.Mutex
	commits  []DrainCommit
	done     bool
	failWhen func(commit DrainCommit) error
}

func (c *casCommitter) AppendChunk(context.Context, string, JobOwner, ResultPage) error { return nil }

func (c *casCommitter) Finalize(context.Context, string, JobOwner, *QueryResult, MaterializationSummary) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.done = true
	return nil
}

func (c *casCommitter) CommitDrainObservation(ctx context.Context, commit DrainCommit) (int64, error) {
	c.mu.Lock()
	fail := c.failWhen
	c.mu.Unlock()
	if fail != nil {
		if err := fail(commit); err != nil {
			return 0, err
		}
	}
	newRev, err := c.store.UpsertQueryJobCAS(ctx, commit.Record, commit.Record.Revision)
	if err != nil {
		return 0, err
	}
	c.mu.Lock()
	c.commits = append(c.commits, commit)
	c.mu.Unlock()
	return newRev, nil
}

func (c *casCommitter) CommitDrainObservationFenced(ctx context.Context, commit DrainCommit, _ string) (int64, error) {
	return c.CommitDrainObservation(ctx, commit)
}

func (c *casCommitter) drainCommits() []DrainCommit {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]DrainCommit(nil), c.commits...)
}

func (c *casCommitter) setFailWhen(fn func(DrainCommit) error) {
	c.mu.Lock()
	c.failWhen = fn
	c.mu.Unlock()
}

// --- item 1: commit-before-advance, no repoll, pathology, recovery ---

func TestDrainCommitFailureNeverAdvancesOrRepolls(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	var failing atomic.Bool
	committer.setFailWhen(func(commit DrainCommit) error {
		if commit.Seq >= 2 && failing.Load() {
			return errors.New("sink unavailable")
		}
		return nil
	})
	failing.Store(true)

	h1 := drainHandle("q", "uri-1")
	h2 := drainHandle("q", "uri-2")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: h1}, nil // page-less: anchor commit seq 1
		},
		pollFn: func(call int, h NativeHandle) (StatusSnapshot, error) {
			if call == 1 {
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h2,
					DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 1}}, CursorAfter: &h2}},
				}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	// While the commit fails: pathology wake appears, and NOTHING advances
	// — DrainSeq stays at the anchor, the cursor stays at uri-1, and the
	// monitor never re-polls (a re-poll would consume the next page while
	// this one is uncommitted).
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
	}, "pathology wake while commit failing")

	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.DrainSeq != 1 {
		t.Fatalf("DrainSeq=%d want 1: seq must not advance before durable commit", snap.DrainSeq)
	}
	if snap.Handle.Extra["next_uri"] != "uri-1" {
		t.Fatalf("cursor=%q want uri-1: cursor must not advance before durable commit", snap.Handle.Extra["next_uri"])
	}
	adapter.mu.Lock()
	pollsDuringFailure := adapter.pollCalls
	adapter.mu.Unlock()
	if pollsDuringFailure != 1 {
		t.Fatalf("polls=%d want exactly 1: the same observation must be retried without re-polling", pollsDuringFailure)
	}
	if isTerminalStatus(snap.Status) {
		t.Fatalf("status=%s: commit failure must stay honest/non-terminal", snap.Status)
	}

	// Recovery: the held observation commits exactly once, then finishes.
	failing.Store(false)
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completion after commit recovery")
	commits := committer.drainCommits()
	seen := map[int64]int{}
	for _, c := range commits {
		seen[c.Seq]++
	}
	if seen[1] != 1 || seen[2] != 1 {
		t.Fatalf("commit seqs=%v want each of 1,2 exactly once (no loss, no duplicate)", seen)
	}
	final, _ := r.Snapshot(job.ID, testOwner())
	if final.Result == nil || len(final.Result.Rows) != 1 {
		t.Fatalf("held page rows lost: %+v", final.Result)
	}
}

func TestInitialCommitFailureCancelsConfirmedNeverFails(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	committer.setFailWhen(func(DrainCommit) error { return errors.New("sink down") })
	h1 := drainHandle("q", "uri-1")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true, CancelConfirmable: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: h1, Initial: &StatusSnapshot{
				State:        ProviderStateRunning,
				DrainedPages: []ResultPage{{Rows: []map[string]any{{"n": 0}}, CursorAfter: &h1}},
			}}, nil
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrInitialDurabilityFailure) {
		t.Fatalf("err=%v want ErrInitialDurabilityFailure", err)
	}
	if job.Status == JobStatusFailed {
		t.Fatalf("job must never be marked failed while the remote query may run")
	}
	if job.CancelReason != CancelReasonDurability {
		t.Fatalf("cancel reason=%s want durability_failure", job.CancelReason)
	}
	// The confirmed cancellation lifecycle completes it honestly.
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCancelled
	}, "provider-confirmed cancellation")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CancelOutcome != CancelOutcomeConfirmed {
		t.Fatalf("outcome=%s want confirmed", snap.CancelOutcome)
	}
	hasPathology := false
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakePathology {
			hasPathology = true
		}
	}
	if !hasPathology {
		t.Fatalf("initial durability failure must be a durable pathology diagnostic")
	}
}

// --- item 2: lease heartbeat ---

func TestLeaseLossStopsDrivingImmediately(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateRunning}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, LeaseTTL: 40 * time.Millisecond, InstanceID: "proc-a"})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		return adapter.pollCalls >= 2
	}, "monitor driving")

	// Steal the lease (release as the holder, acquire as an intruder).
	_ = store.ReleaseMonitorLease(context.Background(), job.ID, "proc-a")
	if got, _ := store.AcquireMonitorLease(context.Background(), job.ID, "intruder", time.Minute); !got {
		t.Fatalf("intruder acquire failed")
	}
	// The heartbeat detects the loss and the monitor stops polling.
	var plateau int
	waitFor(t, 2*time.Second, func() bool {
		adapter.mu.Lock()
		n := adapter.pollCalls
		adapter.mu.Unlock()
		if n == plateau {
			return true
		}
		plateau = n
		time.Sleep(60 * time.Millisecond) // > heartbeat interval
		return false
	}, "polling stopped after lease loss")
	before := plateau
	time.Sleep(120 * time.Millisecond)
	adapter.mu.Lock()
	after := adapter.pollCalls
	adapter.mu.Unlock()
	if after != before {
		t.Fatalf("monitor kept driving after lease loss: %d -> %d", before, after)
	}
	if got := snapshotStatus(r, job.ID, testOwner()); isTerminalStatus(got) {
		t.Fatalf("lease loss must not terminalize execution; status=%s", got)
	}
}

func TestLeaseHeldThroughLongErrorBackoff(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{}, errors.New("provider flapping")
		},
	}
	sched := fastScheduler()
	sched.ErrorBackoff = []time.Duration{300 * time.Millisecond} // backoff >> TTL
	sched.MaxErrorBackoff = 300 * time.Millisecond
	r := newTestRegistry(t, RegistryOptions{
		Store: store, LeaseTTL: 45 * time.Millisecond, InstanceID: "proc-a", Scheduler: sched,
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		adapter.mu.Lock()
		defer adapter.mu.Unlock()
		return adapter.pollCalls >= 1
	}, "first (failing) poll")
	// Mid-backoff (longer than the TTL), the independent heartbeat must
	// keep the lease alive: another process cannot take it.
	time.Sleep(150 * time.Millisecond)
	got, _ := store.AcquireMonitorLease(context.Background(), job.ID, "intruder", time.Minute)
	if got {
		t.Fatalf("lease expired during long error backoff; heartbeat must renew independently of the poll loop")
	}
}

// --- item 3: terminal adoption fully finalizes ---

func TestRehydrateTerminalAdoptionMaterializesResults(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now()
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-fin", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "n1"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now, SubmitDispatched: true,
	})
	mat := &fakeMaterializer{}
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true},
		adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			return AdoptOutcomeTerminal, StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{Rows: []map[string]any{{"n": 1}, {"n": 2}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: mat})
	r.RegisterAdapter(adapter)
	stats, err := r.Rehydrate(context.Background())
	if err != nil || stats.Terminal != 1 {
		t.Fatalf("stats=%+v err=%v want terminal=1", stats, err)
	}
	snap, ok := r.Snapshot("qj-fake-fin", owner)
	if !ok || snap.Status != JobStatusCompleted {
		t.Fatalf("status=%v want completed", snap)
	}
	if snap.Result == nil || len(snap.Result.Rows) != 2 {
		t.Fatalf("terminal adoption must fetch/materialize results, got %+v", snap.Result)
	}
	if snap.Materialization != MaterializationComplete {
		t.Fatalf("materialization=%s want complete", snap.Materialization)
	}
	if !mat.finalized() {
		t.Fatalf("materializer Finalize must run for adopted-terminal jobs")
	}
}

func TestRehydrateTerminalAdoptionDrainCommitsFinalPage(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	owner := testOwner()
	now := time.Now()
	rec := JobRecord{
		ID: "qj-fake-find", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: drainHandle("q", "uri-5"),
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		SubmitDispatched: true, DrainSeq: 5,
	}
	_ = store.UpsertQueryJob(context.Background(), rec)
	hEnd := drainHandle("q", "uri-6")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			return AdoptOutcomeTerminal, StatusSnapshot{
				State:         ProviderStateSucceeded,
				UpdatedHandle: &hEnd,
				DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 9}}, CursorAfter: &hEnd}},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	r.RegisterAdapter(adapter)
	stats, err := r.Rehydrate(context.Background())
	if err != nil || stats.Terminal != 1 {
		t.Fatalf("stats=%+v err=%v want terminal=1", stats, err)
	}
	commits := committer.drainCommits()
	if len(commits) != 1 || commits[0].Seq != 6 {
		t.Fatalf("adopt-terminal drain page must commit at the continuing seq: %+v", commits)
	}
	snap, _ := r.Snapshot("qj-fake-find", owner)
	if snap.Status != JobStatusCompleted || snap.Result == nil || len(snap.Result.Rows) != 1 {
		t.Fatalf("adopt-terminal drain result lost: status=%s result=%+v", snap.Status, snap.Result)
	}
}

// --- item 4: typed permanent observation loss ---

func TestPermanentObservationLossOrphansInsteadOfRetrying(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{}, PermanentObservationLoss(errors.New("410 gone: query purged"))
		},
	}
	r := newTestRegistry(t, RegistryOptions{Materializer: &fakeMaterializer{}})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusOrphaned
	}, "orphaned on permanent loss")
	snap, _ := r.Snapshot(job.ID, testOwner())
	hasRecovery := false
	for _, ev := range snap.WakeEvents {
		if ev.Kind == WakeRecoveryRequired {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("permanent loss must emit recovery_required")
	}
	adapter.mu.Lock()
	polls := adapter.pollCalls
	adapter.mu.Unlock()
	if polls > 2 {
		t.Fatalf("polls=%d: permanent loss must not be retried like a transient error", polls)
	}
}

// --- items 5: cross-process capacity ---

func TestCrossProcessCapacityReservation(t *testing.T) {
	store := NewMemoryJobStore()
	mkAdapter := func() *fakeAdapter { return runningAdapter() }
	admission := &AdmissionConfig{MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1, CrossProcessRetryInterval: 20 * time.Millisecond}
	r1 := newTestRegistry(t, RegistryOptions{Store: store, Admission: admission, InstanceID: "proc-1"})
	a1 := mkAdapter()
	r1.RegisterAdapter(a1)
	r2 := newTestRegistry(t, RegistryOptions{Store: store, Admission: admission, InstanceID: "proc-2"})
	r2.RegisterAdapter(mkAdapter())

	owner1 := JobOwner{ThreadID: "thread-1", Handle: "tok_1"}
	job1, err := r1.Launch(context.Background(), LaunchRequest{Owner: owner1, Provider: "fake", ConnectionID: "wh-1", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch r1: %v", err)
	}

	// r2 has zero LOCAL jobs, but the store-side transactional count must
	// deny it: no remote submit may happen before capacity succeeds.
	got := make(chan error, 1)
	go func() {
		_, err := r2.Launch(context.Background(), LaunchRequest{
			Owner: JobOwner{ThreadID: "thread-2", Handle: "tok_2"}, Provider: "fake", ConnectionID: "wh-2", SQL: "select 2",
		})
		got <- err
	}()
	time.Sleep(80 * time.Millisecond)
	r2.mu.RLock()
	r2adapter := r2.adapters["fake"]
	r2.mu.RUnlock()
	fa := r2adapter.(*fakeAdapter)
	if fa.submitCount() != 0 {
		t.Fatalf("r2 submitted before cross-process capacity was granted")
	}
	select {
	case err := <-got:
		t.Fatalf("r2 launch returned early: %v", err)
	default:
	}

	// Terminal release in r1 frees the slot; r2's retry cadence picks it up.
	if err := r1.RequestCancel(job1.ID, owner1, CancelReasonSuperseded); err != nil {
		t.Fatalf("cancel r1: %v", err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("r2 launch after release: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("cross-process release never admitted the waiting launch")
	}
}

// --- item 6: validation seam ---

func TestValidationLifecycleSeam(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := &fakeAdapter{
		caps:    ProviderCapabilities{NativePagination: true},
		fetchFn: func(int, FetchCursor) (ResultPage, error) { return ResultPage{Rows: []map[string]any{{"n": 1}}}, nil },
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Validation != ValidationUnvalidated {
		t.Fatalf("new jobs must start unvalidated, got %s", job.Validation)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completed")

	// Retrieval must not auto-validate.
	if _, err := r.FetchPage(context.Background(), job.ID, testOwner(), FetchCursor{}); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Validation != ValidationUnvalidated {
		t.Fatalf("retrieval auto-validated: %s", snap.Validation)
	}

	if err := r.SetValidation(job.ID, testOwner(), "bogus", ""); !errors.Is(err, ErrInvalidValidationState) {
		t.Fatalf("err=%v want ErrInvalidValidationState", err)
	}
	if err := r.SetValidation(job.ID, JobOwner{ThreadID: "thread-1", Handle: "tok_wrong"}, ValidationValidated, ""); !errors.Is(err, ErrOwnershipMismatch) {
		t.Fatalf("err=%v want ownership mismatch", err)
	}
	if err := r.SetValidation(job.ID, testOwner(), ValidationValidating, "checks running"); err != nil {
		t.Fatalf("set validating: %v", err)
	}
	if err := r.SetValidation(job.ID, testOwner(), ValidationValidated, "row counts match"); err != nil {
		t.Fatalf("set validated: %v", err)
	}
	snap, _ = r.Snapshot(job.ID, testOwner())
	if snap.Validation != ValidationValidated || snap.ValidationDetail != "row counts match" {
		t.Fatalf("validation=%s/%q want validated", snap.Validation, snap.ValidationDetail)
	}
	rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
	if !ok || rec.ValidationState != ValidationValidated {
		t.Fatalf("validation not durable: %+v", rec.ValidationState)
	}
	// The sidecar write must not desynchronize lifecycle CAS: a subsequent
	// claim + persist cycle works without conflict.
	if _, err := r.ClaimWakeEvents(job.ID, testOwner()); err != nil {
		t.Fatalf("claim after validation: %v", err)
	}
}

// --- items 7+8: revision-domain safety for drain commits ---

func TestClaimedWakeDoesNotBreakNextDrainCommit(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	h := func(n string) NativeHandle { return drainHandle("q", n) }
	h2, h3 := h("uri-2"), h("uri-3")
	var release, dataSent atomic.Bool
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true, QueuedStateVisible: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: h("uri-1")}, nil
		},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if !release.Load() {
				return StatusSnapshot{State: ProviderStateRunning}, nil // no pages while waiting
			}
			if dataSent.CompareAndSwap(false, true) {
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h2,
					DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 1}}, CursorAfter: &h2}},
				}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded, UpdatedHandle: &h3}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{
		Store:        store,
		Materializer: committer,
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    testOwner(),
		Provider: "fake",
		SQL:      "select 1",
		Budgets:  Budgets{SoftExecution: time.Nanosecond}, // immediate soft wake
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}

	// Wait for the soft wake, claim it through the registry (bumps the
	// store revision via the sidecar), then release the data pages: the
	// next drain commit must succeed with no conflict and no loss.
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		if !ok {
			return false
		}
		for _, ev := range snap.WakeEvents {
			if !ev.Claimed && (ev.Kind == WakeSoftExecution || ev.Kind == WakeSoftQueue) {
				return true
			}
		}
		return false
	}, "soft wake emitted")
	claimed, err := r.ClaimWakeEvents(job.ID, testOwner())
	if err != nil || len(claimed) == 0 {
		t.Fatalf("claim: %v / %d", err, len(claimed))
	}

	release.Store(true)
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "drain completes after claimed wake")
	commits := committer.drainCommits()
	rowsCommitted := 0
	prevSeq := int64(0)
	for _, c := range commits {
		if c.Seq != prevSeq+1 {
			t.Fatalf("non-contiguous commit seqs: %+v", commits)
		}
		prevSeq = c.Seq
		rowsCommitted += len(c.Page.Rows)
	}
	if rowsCommitted != 1 {
		t.Fatalf("rows committed=%d want exactly 1 (no loss, no duplicate)", rowsCommitted)
	}
}

func TestExternalRevisionBumpReconcilesWithoutLoss(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	h2 := drainHandle("q", "uri-2")
	var emitted atomic.Bool
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: drainHandle("q", "uri-1")}, nil
		},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if !emitted.Load() {
				emitted.Store(true)
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h2,
					DrainedPages:  []ResultPage{{Rows: []map[string]any{{"n": 1}}, CursorAfter: &h2}},
				}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	// Inject an EXTERNAL sidecar bump right before the page commit: the
	// committer sees a stale expected revision on the first attempt.
	bumped := false
	committer.setFailWhen(func(commit DrainCommit) error {
		if commit.Seq == 2 && !bumped {
			bumped = true
			// Simulate another process's sidecar write the registry never
			// saw (revision bump without notification).
			if stored, ok, _ := store.GetQueryJob(context.Background(), commit.JobID); ok {
				_, _ = store.UpdateQueryJobValidation(context.Background(), commit.JobID, ValidationValidating, "external", stored.Revision)
			}
		}
		return nil
	})
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completed despite external revision bump")
	commits := committer.drainCommits()
	seen := map[int64]int{}
	rows := 0
	for _, c := range commits {
		seen[c.Seq]++
		rows += len(c.Page.Rows)
	}
	if seen[2] != 1 || rows != 1 {
		t.Fatalf("reconcile must retry the SAME observation exactly once: seqs=%v rows=%d", seen, rows)
	}
	rec, _, _ := store.GetQueryJob(context.Background(), job.ID)
	if rec.DrainSeq < 2 {
		t.Fatalf("durable DrainSeq=%d want >=2", rec.DrainSeq)
	}
}
