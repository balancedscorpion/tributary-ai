package jobs

import (
	"context"
	"sync"
	"testing"
	"time"
)

// staleActiveListStore lets a recovery scan observe an old list snapshot while
// the authoritative point-read already contains a newer lifecycle/cursor.
type staleActiveListStore struct {
	*MemoryJobStore
	listed []JobRecord
}

func (s *staleActiveListStore) ListActiveQueryJobs(context.Context) ([]JobRecord, error) {
	out := make([]JobRecord, 0, len(s.listed))
	for _, rec := range s.listed {
		out = append(out, cloneRecord(rec))
	}
	return out, nil
}

func TestRehydrateClassifiesAndAdoptsPostClaimRecord(t *testing.T) {
	base := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now().UTC()
	fresh := JobRecord{
		ID: "qj-fresh-after-claim", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "fresh-handle"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now, SubmitDispatched: true,
		Budgets: Budgets{HardExecution: time.Hour},
	}
	if err := base.UpsertQueryJob(context.Background(), fresh); err != nil {
		t.Fatalf("seed fresh record: %v", err)
	}
	store := &staleActiveListStore{MemoryJobStore: base, listed: []JobRecord{{
		ID: fresh.ID, Provider: "fake", Status: JobStatusReserved,
		Owner: owner, SubmittedAt: fresh.SubmittedAt, LastAccess: now,
		SubmitDispatched: true, // stale submit-window snapshot, no handle
	}}}

	var adopted NativeHandle
	adapter := &fakeAdapter{adoptFn: func(handle NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
		adopted = cloneHandle(handle)
		return AdoptOutcomeTerminal, StatusSnapshot{State: ProviderStateFailed, Error: "finished"}, nil
	}}
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "recovery"})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Terminal != 1 || stats.Reconciling != 0 {
		t.Fatalf("stats=%+v want terminal adoption from the post-claim record", stats)
	}
	if adopted.ID != "fresh-handle" {
		t.Fatalf("Adopt handle=%+v want refreshed fresh-handle", adopted)
	}
}

func TestRehydrateDefersAllIrreversibleClassificationsToLiveLeaseHolder(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now().UTC()
	records := []JobRecord{
		{
			ID: "qj-live-reservation", Provider: "fake", Status: JobStatusReserved,
			Owner: owner, SubmittedAt: now, LastAccess: now,
		},
		{
			ID: "qj-live-drain-submit", Provider: "fake", Status: JobStatusReserved,
			Owner: owner, SubmittedAt: now, LastAccess: now, SubmitDispatched: true,
			Handle: drainHandle("query", "next-1"), DrainSeq: 0,
		},
		{
			ID: "qj-live-missing-adapter", Provider: "missing", Status: JobStatusRunning,
			Owner: owner, SubmittedAt: now, LastAccess: now,
			Handle: NativeHandle{Provider: "missing", ID: "remote"},
		},
	}
	for _, rec := range records {
		if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
			t.Fatalf("seed %s: %v", rec.ID, err)
		}
		if acquired, err := store.AcquireMonitorLease(context.Background(), rec.ID, "live-launch", time.Minute); err != nil || !acquired {
			t.Fatalf("lease %s: acquired=%v err=%v", rec.ID, acquired, err)
		}
	}

	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: &fakeMaterializer{}, InstanceID: "recovery"})
	r.RegisterAdapter(&fakeAdapter{caps: ProviderCapabilities{RequiresContinuousDrain: true}})
	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Deferred != len(records) || stats.Expired != 0 || stats.Orphaned != 0 || stats.Reconciling != 0 {
		t.Fatalf("stats=%+v want every irreversible decision deferred", stats)
	}
	for _, want := range records {
		got, found, err := store.GetQueryJob(context.Background(), want.ID)
		if err != nil || !found {
			t.Fatalf("read %s: found=%v err=%v", want.ID, found, err)
		}
		if got.Status != want.Status || len(got.WakeEvents) != 0 {
			t.Fatalf("live-holder record %s mutated: status=%s wakes=%+v", want.ID, got.Status, got.WakeEvents)
		}
	}
}

type delayedClaimRefreshStore struct {
	*MemoryJobStore
	delay time.Duration
	once  sync.Once
}

func (s *delayedClaimRefreshStore) GetQueryJob(ctx context.Context, id string) (JobRecord, bool, error) {
	s.once.Do(func() { time.Sleep(s.delay) })
	return s.MemoryJobStore.GetQueryJob(ctx, id)
}

func TestClaimReconfirmsLeaseAfterSlowRefreshBeforeAdopt(t *testing.T) {
	base := NewMemoryJobStore()
	store := &delayedClaimRefreshStore{MemoryJobStore: base, delay: 45 * time.Millisecond}
	owner := testOwner()
	now := time.Now().UTC()
	rec := JobRecord{
		ID: "qj-slow-claim-refresh", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "remote"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		Budgets: Budgets{HardExecution: time.Hour},
	}
	if err := base.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}

	intruderAcquired := false
	adapter := &fakeAdapter{adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
		var err error
		intruderAcquired, err = store.AcquireMonitorLease(context.Background(), rec.ID, "intruder", time.Minute)
		if err != nil {
			t.Fatalf("intruder acquire: %v", err)
		}
		return AdoptOutcomeTerminal, StatusSnapshot{State: ProviderStateFailed, Error: "finished"}, nil
	}}
	r := newTestRegistry(t, RegistryOptions{
		Store: store, InstanceID: "recovery", LeaseTTL: 15 * time.Millisecond,
	})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if intruderAcquired {
		t.Fatal("provider Adopt ran after the refresh consumed the lease TTL")
	}
	if stats.Terminal != 1 {
		t.Fatalf("stats=%+v want terminal=1", stats)
	}
}

type captureProfileCancelAdapter struct {
	*fakeAdapter
	mu      sync.Mutex
	handles []NativeHandle
}

func (a *captureProfileCancelAdapter) CancelProfile(_ context.Context, handle NativeHandle) error {
	a.mu.Lock()
	a.handles = append(a.handles, cloneHandle(handle))
	a.mu.Unlock()
	return nil
}

func (a *captureProfileCancelAdapter) cancelCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.handles)
}

func TestInterruptedProfileAdoptsPostClaimStatusBeforeCleanup(t *testing.T) {
	base := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now().UTC()
	fresh := JobRecord{
		ID: "qj-profile-fresh-status", Provider: "fake", Status: JobStatusReserved,
		Owner: owner, SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		ProfileDispatched: true, ProfileOutcome: ProfileOutcomeAccepted,
		ProfileHandle: NativeHandle{Provider: "fake", ID: "fresh-profile"},
	}
	if err := base.UpsertQueryJob(context.Background(), fresh); err != nil {
		t.Fatalf("seed fresh record: %v", err)
	}
	store := &staleActiveListStore{MemoryJobStore: base, listed: []JobRecord{{
		ID: fresh.ID, Provider: "fake", Status: JobStatusProfiling,
		Owner: owner, SubmittedAt: fresh.SubmittedAt, LastAccess: now,
		ProfileDispatched: true,
		ProfileHandle:     NativeHandle{Provider: "fake", ID: "stale-profile"},
	}}}
	adapter := &captureProfileCancelAdapter{fakeAdapter: &fakeAdapter{name: "fake"}}
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "recovery"})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Deferred != 1 || stats.Orphaned != 0 || stats.Expired != 0 {
		t.Fatalf("stats=%+v want accepted profile adopted/deferred", stats)
	}
	if adapter.cancelCount() != 0 {
		t.Fatal("stale profiling snapshot triggered provider cleanup after durable acceptance")
	}
	stored, found, err := base.GetQueryJob(context.Background(), fresh.ID)
	if err != nil || !found || stored.Status != JobStatusReserved || stored.ProfileOutcome != ProfileOutcomeAccepted {
		t.Fatalf("accepted profile was mutated: found=%v err=%v record=%+v", found, err, stored)
	}
}
