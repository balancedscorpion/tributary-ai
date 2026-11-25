package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// obligationBindingStore decorates the reference store with the durable
// obligation boundary used by production stores. Keeping the hook here makes
// the tests assert ordering at the interface boundary instead of depending on
// a chat/store implementation.
type obligationBindingStore struct {
	*MemoryJobStore

	mu       sync.Mutex
	bindFn   func(context.Context, string) error
	boundIDs []string
}

func (s *obligationBindingStore) BindQueryObligation(ctx context.Context, jobID string) error {
	s.mu.Lock()
	s.boundIDs = append(s.boundIDs, jobID)
	fn := s.bindFn
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, jobID)
	}
	return nil
}

func (s *obligationBindingStore) bindings() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.boundIDs...)
}

type binderEventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *binderEventLog) add(event string) {
	l.mu.Lock()
	l.events = append(l.events, event)
	l.mu.Unlock()
}

func (l *binderEventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func requireEventPrefix(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("events=%v want prefix %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events=%v want prefix %v", got, want)
		}
	}
}

func TestQueryObligationBinderRunsAfterDurableWinnerBeforeProfileAndSubmit(t *testing.T) {
	events := &binderEventLog{}
	store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
	store.bindFn = func(ctx context.Context, jobID string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("binder received cancelled context: %w", err)
		}
		rec, found, err := store.GetQueryJob(ctx, jobID)
		if err != nil || !found {
			return fmt.Errorf("binder ran before reservation was durable: found=%v err=%v", found, err)
		}
		if rec.Status != JobStatusProfiling {
			return fmt.Errorf("durable status at bind=%s want profiling", rec.Status)
		}
		winner, found, err := store.GetQueryJobByIdempotency(ctx, "fake", connectionKey("fake", ""), rec.IdempotencyKey)
		if err != nil || !found || winner.ID != jobID {
			return fmt.Errorf("binder job was not durable idempotency winner: winner=%s found=%v err=%v", winner.ID, found, err)
		}
		events.add("bind")
		return nil
	}

	adapter := newProfilingAdapter(true)
	adapter.onProfileStart = func() { events.add("profile") }
	adapter.submitFn = func(int, SubmitRequest) (SubmitResult, error) {
		events.add("submit")
		return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "native-bound"}}, nil
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner: testOwner(), Provider: "fake", SQL: "select 1", IdempotencyKey: "binder-order",
		Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job == nil {
		t.Fatal("launch returned nil job")
	}
	requireEventPrefix(t, events.snapshot(), "bind", "profile", "submit")
	if got := store.bindings(); len(got) != 1 || got[0] != job.ID {
		t.Fatalf("bindings=%v want exactly durable winner %s", got, job.ID)
	}
}

func TestQueryObligationBinderFailureCancelsBeforeProviderWork(t *testing.T) {
	bindErr := errors.New("obligation ledger unavailable")
	store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
	store.bindFn = func(context.Context, string) error { return bindErr }
	adapter := newProfilingAdapter(true)
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner: testOwner(), Provider: "fake", SQL: "select 1",
		ReplacesJobID: "qj-fake-predecessor",
		Metadata: map[string]any{
			QueryObligationManagedMetadataKey: true,
			"query_obligation_id":             "qob-11111111111111111111111111111111",
		},
	})
	if !errors.Is(err, ErrQueryObligationBinding) {
		t.Fatalf("launch err=%v want ErrQueryObligationBinding", err)
	}
	if job == nil {
		t.Fatal("binding failure must return the durable failed attempt")
	}
	if job.Status != JobStatusCancelled || job.CancelOutcome != CancelOutcomeBeforeSubmit || job.CancelReason != CancelReasonDurability {
		t.Fatalf("job=%+v want durability cancellation before_submit", job)
	}
	rec, found, getErr := store.GetQueryJob(context.Background(), job.ID)
	if getErr != nil || !found {
		t.Fatalf("durable cancelled record: found=%v err=%v", found, getErr)
	}
	if rec.Status != JobStatusCancelled || rec.CancelOutcome != CancelOutcomeBeforeSubmit || rec.SubmitDispatched || rec.ProfileDispatched {
		t.Fatalf("durable record=%+v want cancelled before any dispatch", rec)
	}
	if adapter.profileCount() != 0 || adapter.submitCount() != 0 {
		t.Fatalf("provider work occurred after bind failure: profile=%d submit=%d", adapter.profileCount(), adapter.submitCount())
	}
	adapter.mu.Lock()
	polls, cancels := adapter.pollCalls, adapter.fakeAdapter.cancelCalls
	adapter.mu.Unlock()
	if polls != 0 || cancels != 0 {
		t.Fatalf("provider work occurred after bind failure: poll=%d cancel=%d", polls, cancels)
	}
}

func TestRehydrateBindsBeforeProviderIOAndDefersOnBindFailure(t *testing.T) {
	t.Run("bind before adopt and poll", func(t *testing.T) {
		events := &binderEventLog{}
		store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
		owner := testOwner()
		rec := JobRecord{
			ID: "qj-fake-rehydrate-bound", Provider: "fake", Status: JobStatusRunning,
			Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "native-rehydrate"},
			SubmittedAt: time.Now().Add(-time.Minute), LastAccess: time.Now(), SubmitDispatched: true,
			Budgets:  Budgets{HardExecution: time.Hour},
			Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
		}
		if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
			t.Fatalf("seed: %v", err)
		}
		store.bindFn = func(ctx context.Context, jobID string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if jobID != rec.ID {
				return fmt.Errorf("bound %s want %s", jobID, rec.ID)
			}
			events.add("bind")
			return nil
		}
		adapter := &fakeAdapter{
			caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true},
			adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
				events.add("adopt")
				return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
			},
			pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
				events.add("poll")
				return StatusSnapshot{State: ProviderStateSucceeded}, nil
			},
		}
		r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "binder-recovery"})
		r.RegisterAdapter(adapter)

		stats, err := r.Rehydrate(context.Background())
		if err != nil {
			t.Fatalf("rehydrate: %v", err)
		}
		if stats.Adopted != 1 {
			t.Fatalf("stats=%+v want one adopted job", stats)
		}
		waitFor(t, 2*time.Second, func() bool {
			return len(events.snapshot()) >= 3
		}, "rehydrated job polled")
		requireEventPrefix(t, events.snapshot(), "bind", "adopt", "poll")
	})

	t.Run("bind failure defers with zero provider IO", func(t *testing.T) {
		store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
		store.bindFn = func(context.Context, string) error { return errors.New("ledger temporarily unavailable") }
		events := &binderEventLog{}
		rec := JobRecord{
			ID: "qj-fake-rehydrate-deferred", Provider: "fake", Status: JobStatusRunning,
			Owner: testOwner(), Handle: NativeHandle{Provider: "fake", ID: "native-deferred"},
			SubmittedAt: time.Now().Add(-time.Minute), LastAccess: time.Now(), SubmitDispatched: true,
			Budgets:  Budgets{HardExecution: time.Hour},
			Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
		}
		if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
			t.Fatalf("seed: %v", err)
		}
		adapter := newProfilingAdapter(true)
		adapter.caps.DetachedRepoll = true
		adapter.adoptFn = func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			events.add("adopt")
			return AdoptOutcomeOrphaned, StatusSnapshot{}, nil
		}
		adapter.pollFn = func(int, NativeHandle) (StatusSnapshot, error) {
			events.add("poll")
			return StatusSnapshot{}, nil
		}
		r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "binder-recovery-deferred"})
		r.RegisterAdapter(adapter)

		stats, err := r.Rehydrate(context.Background())
		if err != nil {
			t.Fatalf("rehydrate: %v", err)
		}
		if stats.Deferred != 1 || stats.Adopted != 0 || stats.Terminal != 0 || stats.Orphaned != 0 {
			t.Fatalf("stats=%+v want exactly one deferred job", stats)
		}
		if got := store.bindings(); len(got) != 1 || got[0] != rec.ID {
			t.Fatalf("bindings=%v want %s", got, rec.ID)
		}
		stored, found, getErr := store.GetQueryJob(context.Background(), rec.ID)
		if getErr != nil || !found || stored.Status != JobStatusRunning {
			t.Fatalf("deferred record changed: found=%v status=%s err=%v", found, stored.Status, getErr)
		}
		if got := events.snapshot(); len(got) != 0 || adapter.profileCount() != 0 || adapter.cancelCalls.Load() != 0 {
			t.Fatalf("provider I/O after binding failure: events=%v profile=%d profile_cancel=%d", got, adapter.profileCount(), adapter.cancelCalls.Load())
		}
	})
}

func TestManagedReplacementFailsClosedWhenStoreLacksObligationBinder(t *testing.T) {
	store := NewMemoryJobStore() // deliberately does not implement QueryObligationBinder
	adapter := &fakeAdapter{}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner: testOwner(), Provider: "fake", SQL: "select replacement",
		ReplacesJobID: "qj-fake-predecessor",
		Metadata: map[string]any{
			QueryObligationManagedMetadataKey: true,
			"query_obligation_id":             "qob-22222222222222222222222222222222",
		},
	})
	if !errors.Is(err, ErrQueryObligationBinding) {
		t.Fatalf("launch err=%v want ErrQueryObligationBinding", err)
	}
	if job == nil || job.Status != JobStatusCancelled || job.CancelOutcome != CancelOutcomeBeforeSubmit {
		t.Fatalf("job=%+v want cancelled before_submit", job)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("replacement submitted through store without binder: submits=%d", adapter.submitCount())
	}
}

func TestRecoveredIdempotencyWinnerBindsWithLiveCallerContext(t *testing.T) {
	store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
	owner := testOwner()
	rec := JobRecord{
		ID: "qj-fake-idempotent-bound", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "native-idempotent"},
		SQL: "select 1", IdempotencyKey: "binder-idempotent",
		SubmittedAt: time.Now().Add(-time.Minute), LastAccess: time.Now(), SubmitDispatched: true,
		Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store.bindFn = func(ctx context.Context, jobID string) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("recovered winner binder context was already cancelled: %w", err)
		}
		if jobID != rec.ID {
			return fmt.Errorf("bound %s want %s", jobID, rec.ID)
		}
		return nil
	}
	adapter := &fakeAdapter{}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner: owner, Provider: "fake", SQL: rec.SQL, IdempotencyKey: rec.IdempotencyKey,
		Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
	})
	if err != nil {
		t.Fatalf("recover winner: %v", err)
	}
	if job == nil || job.ID != rec.ID {
		t.Fatalf("job=%+v want recovered %s", job, rec.ID)
	}
	if got := store.bindings(); len(got) != 1 || got[0] != rec.ID {
		t.Fatalf("bindings=%v want recovered winner %s", got, rec.ID)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("recovered winner was resubmitted: submits=%d", adapter.submitCount())
	}
}

func TestGenericRunlessLaunchDoesNotEnterQueryObligationProtocol(t *testing.T) {
	store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
	store.bindFn = func(context.Context, string) error {
		return errors.New("generic launch must not call obligation binder")
	}
	adapter := &fakeAdapter{}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    JobOwner{ThreadID: "thread-generic", Handle: "tok_generic"},
		Provider: "fake", SQL: "select 1",
	})
	if err != nil {
		t.Fatalf("generic runless launch: %v", err)
	}
	if job == nil {
		t.Fatal("generic runless launch returned nil job")
	}
	if got := store.bindings(); len(got) != 0 {
		t.Fatalf("generic launch bindings=%v want none", got)
	}
	if adapter.submitCount() != 1 {
		t.Fatalf("generic launch submits=%d want 1", adapter.submitCount())
	}
}

func TestFreshManagedQueryObligationRequiresRun(t *testing.T) {
	store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
	adapter := &fakeAdapter{}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)

	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner:    JobOwner{ThreadID: "thread-managed", Handle: "tok_managed"},
		Provider: "fake", SQL: "select 1",
		Metadata: map[string]any{QueryObligationManagedMetadataKey: true},
	})
	if err == nil || !strings.Contains(err.Error(), "run_id is required") {
		t.Fatalf("launch err=%v want missing run_id failure", err)
	}
	if job != nil {
		t.Fatalf("job=%+v want no reservation", job)
	}
	if adapter.submitCount() != 0 || len(store.bindings()) != 0 {
		t.Fatalf("provider/binder work occurred: submits=%d bindings=%v", adapter.submitCount(), store.bindings())
	}
	records, listErr := store.ListActiveQueryJobs(context.Background())
	if listErr != nil || len(records) != 0 {
		t.Fatalf("durable reservations=%v err=%v want none", records, listErr)
	}
}

func TestRehydrateManagedJobAfterRunDeletionSkipsRetiredObligation(t *testing.T) {
	store := &obligationBindingStore{MemoryJobStore: NewMemoryJobStore()}
	rec := JobRecord{
		ID: "qj-fake-run-deleted", Provider: "fake", Status: JobStatusRunning,
		Owner:       JobOwner{ThreadID: "thread-deleted-run", Handle: "tok_deleted_run"},
		Handle:      NativeHandle{Provider: "fake", ID: "native-run-deleted"},
		Metadata:    map[string]any{QueryObligationManagedMetadataKey: true},
		SubmittedAt: time.Now().Add(-time.Minute), LastAccess: time.Now(), SubmitDispatched: true,
		Budgets: Budgets{HardExecution: time.Hour},
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	events := &binderEventLog{}
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{DetachedRepoll: true, NativePagination: true},
		adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			events.add("adopt")
			return AdoptOutcomeAdopted, StatusSnapshot{State: ProviderStateRunning}, nil
		},
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			events.add("poll")
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "run-deleted-recovery"})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Adopted != 1 || stats.Deferred != 0 {
		t.Fatalf("stats=%+v want adopted without obligation deferral", stats)
	}
	waitFor(t, 2*time.Second, func() bool { return len(events.snapshot()) >= 2 }, "runless recovered job polled")
	if got := store.bindings(); len(got) != 0 {
		t.Fatalf("retired run obligation bindings=%v want none", got)
	}
}
