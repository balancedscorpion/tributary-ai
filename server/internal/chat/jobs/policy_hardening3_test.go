package jobs

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHardBudgetCannotBeBypassedByPerpetualPollErrors(t *testing.T) {
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{CancelConfirmable: true},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{}, WithRetryAfter(errors.New("provider temporarily unavailable"), time.Hour)
		},
	}
	r := newTestRegistry(t, RegistryOptions{
		Policy: &PolicyConfig{HardExecution: 30 * time.Millisecond, DefaultBudgets: Budgets{HardExecution: 30 * time.Millisecond}},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusExpired
	}, "hard-budget cancellation despite perpetual poll errors")
	adapter.mu.Lock()
	cancels := adapter.cancelCalls
	adapter.mu.Unlock()
	if cancels == 0 {
		t.Fatal("hard budget never issued provider cancellation")
	}
}

type contextBlockingAdapter struct {
	pollStarted chan struct{}
	once        sync.Once
	cancels     atomic.Int32
}

func (a *contextBlockingAdapter) Provider() string { return "blocking" }
func (a *contextBlockingAdapter) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{CancelConfirmable: true}
}
func (a *contextBlockingAdapter) Submit(context.Context, SubmitRequest) (SubmitResult, error) {
	return SubmitResult{Handle: NativeHandle{Provider: "blocking", ID: "remote"}}, nil
}
func (a *contextBlockingAdapter) Poll(ctx context.Context, _ NativeHandle) (StatusSnapshot, error) {
	a.once.Do(func() { close(a.pollStarted) })
	<-ctx.Done()
	return StatusSnapshot{}, ctx.Err()
}
func (*contextBlockingAdapter) Fetch(context.Context, NativeHandle, FetchCursor) (ResultPage, error) {
	return ResultPage{}, nil
}
func (a *contextBlockingAdapter) Cancel(context.Context, NativeHandle) error {
	a.cancels.Add(1)
	return nil
}
func (*contextBlockingAdapter) ConfirmCancel(context.Context, NativeHandle) (bool, StatusSnapshot, error) {
	return true, StatusSnapshot{State: ProviderStateCancelled}, nil
}
func (*contextBlockingAdapter) Adopt(context.Context, NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
	return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
}

func TestBlockedPollIsBoundedByHardBudget(t *testing.T) {
	adapter := &contextBlockingAdapter{pollStarted: make(chan struct{})}
	sched := fastScheduler()
	sched.ProviderCallTimeout = 5 * time.Second // hard remaining must tighten this
	r := newTestRegistry(t, RegistryOptions{
		Scheduler: sched,
		Policy:    &PolicyConfig{HardExecution: 35 * time.Millisecond, DefaultBudgets: Budgets{HardExecution: 35 * time.Millisecond}},
	})
	r.RegisterAdapter(adapter)
	started := time.Now()
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "blocking", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	select {
	case <-adapter.pollStarted:
	case <-time.After(time.Second):
		t.Fatal("poll never started")
	}
	waitFor(t, time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusExpired
	}, "blocked poll interrupted and hard-budget cancelled")
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked provider call pinned monitor for %s", elapsed)
	}
	if adapter.cancels.Load() == 0 {
		t.Fatal("provider cancellation was not issued")
	}
}

type delayedProfileAdapter struct {
	*fakeAdapter
	delay time.Duration
}

func (a *delayedProfileAdapter) Profile(ctx context.Context, _ SubmitRequest) (QueryProfile, error) {
	timer := time.NewTimer(a.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return QueryProfile{}, ctx.Err()
	case <-timer.C:
		return QueryProfile{Valid: true}, nil
	}
}

func TestExecutionBudgetStartsAtActualRemoteDispatch(t *testing.T) {
	base := &fakeAdapter{pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
		return StatusSnapshot{State: ProviderStateRunning}, nil
	}}
	adapter := &delayedProfileAdapter{fakeAdapter: base, delay: 70 * time.Millisecond}
	r := newTestRegistry(t, RegistryOptions{
		Profile: &ProfilePolicy{Timeout: time.Second},
		Policy:  &PolicyConfig{HardExecution: 35 * time.Millisecond, DefaultBudgets: Budgets{HardExecution: 35 * time.Millisecond}},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.RemoteSubmittedAt == nil {
		t.Fatal("actual remote dispatch timestamp was not stamped")
	}
	if gap := job.RemoteSubmittedAt.Sub(job.SubmittedAt); gap < 60*time.Millisecond {
		t.Fatalf("remote timestamp did not exclude profiling time: gap=%s", gap)
	}
	if isTerminalStatus(job.Status) || job.CancelReason == CancelReasonBudget {
		t.Fatalf("profiling time incorrectly consumed execution budget: %+v", job)
	}
	_ = r.RequestCancel(job.ID, testOwner(), CancelReasonNoLongerNeeded)
}

type denyCapacityStore struct{ *MemoryJobStore }

func (s *denyCapacityStore) ReserveQueryCapacity(context.Context, JobRecord, CapacityLimits) (bool, int64, error) {
	return false, 0, nil
}

func TestAdmissionTTLIsAbsoluteAcrossCrossProcessRetries(t *testing.T) {
	store := &denyCapacityStore{MemoryJobStore: NewMemoryJobStore()}
	r := newTestRegistry(t, RegistryOptions{
		Store: store,
		Admission: &AdmissionConfig{
			ReservationTTL:            55 * time.Millisecond,
			CrossProcessRetryInterval: 8 * time.Millisecond,
		},
	})
	r.RegisterAdapter(&fakeAdapter{})
	started := time.Now()
	_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if !errors.Is(err, ErrAdmissionTimeout) {
		t.Fatalf("err=%v want ErrAdmissionTimeout", err)
	}
	elapsed := time.Since(started)
	if elapsed < 45*time.Millisecond || elapsed > 250*time.Millisecond {
		t.Fatalf("absolute admission TTL not honored: %s", elapsed)
	}
}

func TestSchedulerHonorsRetryAfterAndDrainSafetyBound(t *testing.T) {
	cfg := SchedulerConfig{BaseIntervals: []time.Duration{time.Second}, MaxInterval: time.Second, JitterFrac: 0.01}.withDefaults()
	if got := cfg.errorInterval(1, ProviderCapabilities{}, 4*time.Second); got < 4*time.Second {
		t.Fatalf("retry-after was undercut: %s", got)
	}
	caps := ProviderCapabilities{MaxSafePollInterval: 2 * time.Second}
	if got := cfg.errorInterval(1, caps, 4*time.Second); got < 4*time.Second {
		t.Fatalf("authoritative Retry-After was truncated to unsafe hammering cadence: %s", got)
	}
}

func TestSubmitRetryAfterIsDurableAndDelaysFirstPoll(t *testing.T) {
	store := NewMemoryJobStore()
	pollAt := make(chan time.Time, 1)
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle:  NativeHandle{Provider: "fake", ID: "remote"},
				Initial: &StatusSnapshot{State: ProviderStatePending, RetryAfter: 80 * time.Millisecond},
			}, nil
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			select {
			case pollAt <- time.Now():
			default:
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	started := time.Now()
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	record, ok, err := store.GetQueryJob(context.Background(), job.ID)
	if err != nil || !ok || record.NextPollAt == nil {
		t.Fatalf("Retry-After deadline not durable: ok=%v err=%v record=%+v", ok, err, record)
	}
	select {
	case observed := <-pollAt:
		if elapsed := observed.Sub(started); elapsed < 70*time.Millisecond {
			t.Fatalf("first poll ignored Retry-After: %s", elapsed)
		}
	case <-time.After(time.Second):
		t.Fatal("first poll never ran")
	}
}

type adoptCountingAdapter struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	count   atomic.Int32
}

func (*adoptCountingAdapter) Provider() string { return "adopt" }
func (*adoptCountingAdapter) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{DetachedRepoll: true, CancelConfirmable: true}
}
func (*adoptCountingAdapter) Submit(context.Context, SubmitRequest) (SubmitResult, error) {
	return SubmitResult{}, errors.New("unused")
}
func (*adoptCountingAdapter) Poll(context.Context, NativeHandle) (StatusSnapshot, error) {
	return StatusSnapshot{State: ProviderStateRunning}, nil
}
func (*adoptCountingAdapter) Fetch(context.Context, NativeHandle, FetchCursor) (ResultPage, error) {
	return ResultPage{}, nil
}
func (*adoptCountingAdapter) Cancel(context.Context, NativeHandle) error { return nil }
func (*adoptCountingAdapter) ConfirmCancel(context.Context, NativeHandle) (bool, StatusSnapshot, error) {
	return true, StatusSnapshot{State: ProviderStateCancelled}, nil
}
func (a *adoptCountingAdapter) Adopt(context.Context, NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
	a.count.Add(1)
	a.once.Do(func() { close(a.started) })
	<-a.release
	return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
}

func TestRehydrateAcquiresLeaseBeforeProviderAdopt(t *testing.T) {
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	record := JobRecord{
		ID: "qj-adopt-one", Provider: "adopt", Status: JobStatusRunning,
		Owner: testOwner(), Handle: NativeHandle{Provider: "adopt", ID: "remote"},
		SubmittedAt: now.Add(-time.Minute), RemoteSubmittedAt: &now, LastAccess: now,
		Budgets: Budgets{HardExecution: time.Hour},
	}
	if err := store.UpsertQueryJob(context.Background(), record); err != nil {
		t.Fatalf("seed: %v", err)
	}
	adapter := &adoptCountingAdapter{started: make(chan struct{}), release: make(chan struct{})}
	r1 := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "process-a", LeaseTTL: time.Second})
	r2 := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "process-b", LeaseTTL: time.Second})
	r1.RegisterAdapter(adapter)
	r2.RegisterAdapter(adapter)
	done := make(chan error, 1)
	go func() {
		_, err := r1.Rehydrate(context.Background())
		done <- err
	}()
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("first adoption did not start")
	}
	stats, err := r2.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("second rehydrate: %v", err)
	}
	if stats.Deferred != 1 {
		t.Fatalf("second registry should defer to lease holder: %+v", stats)
	}
	close(adapter.release)
	if err := <-done; err != nil {
		t.Fatalf("first rehydrate: %v", err)
	}
	if got := adapter.count.Load(); got != 1 {
		t.Fatalf("provider Adopt calls=%d want exactly 1", got)
	}
	_ = r1.RequestCancel(record.ID, testOwner(), CancelReasonNoLongerNeeded)
}

type transientRenewStore struct {
	*MemoryJobStore
	calls atomic.Int32
}

func (s *transientRenewStore) RenewMonitorLease(ctx context.Context, jobID, holder string, ttl time.Duration) (bool, error) {
	if s.calls.Add(1) == 1 {
		return false, errors.New("transient store outage")
	}
	return s.MemoryJobStore.RenewMonitorLease(ctx, jobID, holder, ttl)
}

func TestTransientLeaseRenewalErrorDoesNotAbandonQuery(t *testing.T) {
	store := &transientRenewStore{MemoryJobStore: NewMemoryJobStore()}
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if call < 6 {
				return StatusSnapshot{State: ProviderStateRunning}, nil
			}
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	sched := fastScheduler()
	sched.BaseIntervals = []time.Duration{15 * time.Millisecond}
	sched.MaxInterval = 15 * time.Millisecond
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "process-a", LeaseTTL: 45 * time.Millisecond, Scheduler: sched})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "completion after transient lease renewal error")
	if got := store.calls.Load(); got < 2 {
		t.Fatalf("lease renewal was not retried: calls=%d", got)
	}
}

func TestRecurringClaimantTakesOverExpiredLease(t *testing.T) {
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	record := JobRecord{
		ID: "qj-failover", Provider: "fake", Status: JobStatusRunning,
		Owner: testOwner(), Handle: NativeHandle{Provider: "fake", ID: "remote"},
		SubmittedAt: now.Add(-time.Minute), RemoteSubmittedAt: &now, LastAccess: now,
		Budgets: Budgets{HardExecution: time.Hour},
	}
	if err := store.UpsertQueryJob(context.Background(), record); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if acquired, err := store.AcquireMonitorLease(context.Background(), record.ID, "dead-process", 45*time.Millisecond); err != nil || !acquired {
		t.Fatalf("seed dead lease: acquired=%v err=%v", acquired, err)
	}
	var adopts atomic.Int32
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true},
		adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			adopts.Add(1)
			return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	sched := fastScheduler()
	sched.ClaimInterval = 10 * time.Millisecond
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "live-process", LeaseTTL: 45 * time.Millisecond, Scheduler: sched})
	r.RegisterAdapter(adapter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.StartReaper(ctx, time.Hour)
	waitFor(t, 2*time.Second, func() bool {
		return adopts.Load() > 0
	}, "recurring claimant provider adoption after lease expiry")
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, record.ID, testOwner()) == JobStatusCompleted
	}, "recurring claimant completion after takeover")
	if got := adopts.Load(); got != 1 {
		t.Fatalf("adopt calls=%d want exactly one takeover", got)
	}
}

func TestCancellationCommitsDrainBacklogAndUsesLatestCursor(t *testing.T) {
	store := NewMemoryJobStore()
	committer := &casCommitter{store: store}
	var failSecond atomic.Bool
	failSecond.Store(true)
	committer.setFailWhen(func(commit DrainCommit) error {
		if commit.Seq == 2 && failSecond.Load() {
			return errors.New("durable sink temporarily unavailable")
		}
		return nil
	})
	h1 := drainHandle("query", "uri-1")
	h2 := drainHandle("query", "uri-2")
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true, CancelConfirmable: false},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: h1}, nil
		},
		pollFn: func(call int, handle NativeHandle) (StatusSnapshot, error) {
			if call == 1 {
				return StatusSnapshot{
					State:         ProviderStateRunning,
					UpdatedHandle: &h2,
					DrainedPages: []ResultPage{{
						Rows:        []map[string]any{{"n": 1}},
						CursorAfter: &h2,
					}},
				}, nil
			}
			return StatusSnapshot{State: ProviderStateCancelled}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		if !ok {
			return false
		}
		for _, event := range snap.WakeEvents {
			if event.Kind == WakePathology {
				return true
			}
		}
		return false
	}, "drain backlog held on durable commit failure")
	if err := r.RequestCancel(job.ID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("request cancel: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	adapter.mu.Lock()
	cancelsWhileBlocked := adapter.cancelCalls
	adapter.mu.Unlock()
	if cancelsWhileBlocked != 0 {
		t.Fatalf("cancel used stale cursor before backlog commit: calls=%d", cancelsWhileBlocked)
	}
	failSecond.Store(false)
	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCancelled
	}, "post-cancel terminal page drained")
	adapter.mu.Lock()
	if len(adapter.cancelledHandles) == 0 {
		adapter.mu.Unlock()
		t.Fatal("provider cancel was never issued")
	}
	cancelHandle := cloneHandle(adapter.cancelledHandles[0])
	adapter.mu.Unlock()
	if got := cancelHandle.Extra["next_uri"]; got != "uri-2" {
		t.Fatalf("cancel cursor=%q want latest durable uri-2", got)
	}
	snap, _ := r.Snapshot(job.ID, testOwner())
	commits := committer.drainCommits()
	foundRow := false
	for _, commit := range commits {
		if commit.Seq == 2 && len(commit.Page.Rows) == 1 {
			foundRow = true
		}
	}
	if snap.DrainSeq != 3 || !foundRow {
		t.Fatalf("backlog was lost across cancellation: seq=%d commits=%+v", snap.DrainSeq, commits)
	}
}
