package jobs

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type capacityReleaseCountingStore struct {
	*MemoryJobStore
	releases atomic.Int32
}

func (s *capacityReleaseCountingStore) ReleaseQueryCapacity(context.Context, string) error {
	s.releases.Add(1)
	return nil
}

func seedConflictJob(t *testing.T, store *capacityReleaseCountingStore, id string) (JobRecord, *Registry, *Job) {
	t.Helper()
	now := time.Now().UTC()
	rec := JobRecord{
		ID: id, Provider: "fake", Status: JobStatusRunning,
		Owner: testOwner(), Handle: NativeHandle{Provider: "fake", ID: "old-handle"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
		Budgets: Budgets{HardExecution: time.Hour},
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stored, found, err := store.GetQueryJob(context.Background(), id)
	if err != nil || !found {
		t.Fatalf("read seed: found=%v err=%v", found, err)
	}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	adapter := &fakeAdapter{}
	r.RegisterAdapter(adapter)
	job := r.jobFromRecord(stored, adapter)
	r.mu.Lock()
	r.jobs[id] = job
	r.mu.Unlock()
	return stored, r, job
}

func TestNonDriverCASConflictAdoptsDurableTerminalAndSidecars(t *testing.T) {
	store := &capacityReleaseCountingStore{MemoryJobStore: NewMemoryJobStore()}
	stored, r, job := seedConflictJob(t, store, "qj-cas-durable-terminal")

	// This local status-only copy reaches a different stale terminal verdict.
	localAt := time.Now().UTC()
	job.mu.Lock()
	job.Error = "stale local failure"
	job.transitionLocked(JobStatusFailed, localAt)
	job.Validation = ValidationUnvalidated
	job.Metadata = map[string]any{"source": "stale-local"}
	job.WakeEvents = []WakeEvent{{At: localAt, Kind: WakeTerminal, Detail: "stale", Claimed: false}}
	job.mu.Unlock()

	// Another process wins with the authoritative terminal state plus sidecars.
	durableAt := localAt.Add(time.Second)
	fresh := cloneRecord(stored)
	fresh.Status = JobStatusCompleted
	fresh.CompletedAt = &durableAt
	fresh.Handle = NativeHandle{Provider: "fake", ID: "fresh-handle"}
	fresh.Error = ""
	fresh.ValidationState = ValidationValidated
	fresh.ValidationDetail = "durable validation"
	fresh.Metadata = map[string]any{"source": "durable"}
	fresh.WakeEvents = []WakeEvent{{At: durableAt, Kind: WakeTerminal, Detail: "completed", Claimed: true}}
	newRev, err := store.UpsertQueryJobCAS(context.Background(), fresh, stored.Revision)
	if err != nil {
		t.Fatalf("external terminal CAS: %v", err)
	}

	r.afterTransition(job)

	got, found, err := store.GetQueryJob(context.Background(), stored.ID)
	if err != nil || !found {
		t.Fatalf("read durable: found=%v err=%v", found, err)
	}
	if got.Revision != newRev || got.Status != JobStatusCompleted || got.Handle.ID != "fresh-handle" {
		t.Fatalf("stale non-driver overwrote terminal lifecycle: %+v", got)
	}
	if got.ValidationState != ValidationValidated || got.ValidationDetail != "durable validation" ||
		len(got.WakeEvents) != 1 || !got.WakeEvents[0].Claimed || got.Metadata["source"] != "durable" {
		t.Fatalf("durable sidecars were lost: %+v", got)
	}

	job.mu.RLock()
	localStatus := job.Status
	localHandle := cloneHandle(job.Handle)
	localValidation := job.Validation
	localWakeClaimed := len(job.WakeEvents) == 1 && job.WakeEvents[0].Claimed
	capacityReleased := job.capacityReleased
	job.mu.RUnlock()
	if localStatus != JobStatusCompleted || localHandle.ID != "fresh-handle" ||
		localValidation != ValidationValidated || !localWakeClaimed || !capacityReleased {
		t.Fatalf("local copy did not adopt durable terminal state: status=%s handle=%+v validation=%s claimed=%v capacityReleased=%v",
			localStatus, localHandle, localValidation, localWakeClaimed, capacityReleased)
	}
	if got := store.releases.Load(); got != 0 {
		t.Fatalf("stale writer released capacity %d times", got)
	}
	select {
	case <-job.pollCtx.Done():
	default:
		t.Fatal("durable terminal adoption did not stop the stale local poll context")
	}
}

func TestDrivenCASConflictYieldsWithoutOverwriteOrCapacityRelease(t *testing.T) {
	store := &capacityReleaseCountingStore{MemoryJobStore: NewMemoryJobStore()}
	stored, r, job := seedConflictJob(t, store, "qj-cas-driven-yield")

	localAt := time.Now().UTC()
	job.mu.Lock()
	job.driving = true
	job.transitionLocked(JobStatusFailed, localAt)
	job.Error = "stale driver terminal"
	job.mu.Unlock()

	fresh := cloneRecord(stored)
	fresh.Handle = NativeHandle{Provider: "fake", ID: "fresh-active-handle"}
	fresh.ValidationState = ValidationValidated
	fresh.ValidationDetail = "external sidecar"
	fresh.WakeEvents = []WakeEvent{{At: localAt, Kind: WakePathology, Detail: "external", Claimed: true}}
	newRev, err := store.UpsertQueryJobCAS(context.Background(), fresh, stored.Revision)
	if err != nil {
		t.Fatalf("external active CAS: %v", err)
	}

	r.afterTransition(job)

	got, found, err := store.GetQueryJob(context.Background(), stored.ID)
	if err != nil || !found {
		t.Fatalf("read durable: found=%v err=%v", found, err)
	}
	if got.Revision != newRev || got.Status != JobStatusRunning || got.Handle.ID != "fresh-active-handle" ||
		got.ValidationState != ValidationValidated || len(got.WakeEvents) != 1 || !got.WakeEvents[0].Claimed {
		t.Fatalf("stale driver overwrote newer active record: %+v", got)
	}
	if got := store.releases.Load(); got != 0 {
		t.Fatalf("stale driver released capacity %d times", got)
	}
	job.mu.RLock()
	capacityReleased := job.capacityReleased
	job.mu.RUnlock()
	if capacityReleased {
		t.Fatal("active durable record was marked capacity-released locally")
	}
	select {
	case <-job.pollCtx.Done():
	default:
		t.Fatal("stale driven writer did not yield its poll context")
	}
}

func TestNonDriverCASConflictAdoptsNewerActiveRecordWithoutRetry(t *testing.T) {
	store := &capacityReleaseCountingStore{MemoryJobStore: NewMemoryJobStore()}
	stored, r, job := seedConflictJob(t, store, "qj-cas-active-adopt")

	job.mu.Lock()
	job.Metadata = map[string]any{"source": "stale-local"}
	job.mu.Unlock()
	fresh := cloneRecord(stored)
	fresh.Handle = NativeHandle{Provider: "fake", ID: "fresh-active-handle"}
	fresh.ValidationState = ValidationValidated
	fresh.ValidationDetail = "external"
	fresh.Metadata = map[string]any{"source": "durable"}
	fresh.WakeEvents = []WakeEvent{{At: time.Now().UTC(), Kind: WakePathology, Detail: "external", Claimed: true}}
	newRev, err := store.UpsertQueryJobCAS(context.Background(), fresh, stored.Revision)
	if err != nil {
		t.Fatalf("external active CAS: %v", err)
	}

	if persisted := r.persistJob(job); persisted {
		t.Fatal("active conflict adoption must not claim the stale local mutation persisted")
	}
	got, found, err := store.GetQueryJob(context.Background(), stored.ID)
	if err != nil || !found {
		t.Fatalf("read durable: found=%v err=%v", found, err)
	}
	if got.Revision != newRev || got.Handle.ID != "fresh-active-handle" || got.Metadata["source"] != "durable" ||
		got.ValidationState != ValidationValidated || len(got.WakeEvents) != 1 || !got.WakeEvents[0].Claimed {
		t.Fatalf("newer active record was overwritten: %+v", got)
	}
	job.mu.RLock()
	localHandle := cloneHandle(job.Handle)
	localSource := job.Metadata["source"]
	localValidation := job.Validation
	localWakeClaimed := len(job.WakeEvents) == 1 && job.WakeEvents[0].Claimed
	job.mu.RUnlock()
	if localHandle.ID != "fresh-active-handle" || localSource != "durable" ||
		localValidation != ValidationValidated || !localWakeClaimed {
		t.Fatalf("local active copy did not adopt durable state: handle=%+v source=%v validation=%s claimed=%v",
			localHandle, localSource, localValidation, localWakeClaimed)
	}
}

func TestNonDriverCancelConflictRebasesOntoFreshActiveRecord(t *testing.T) {
	store := &capacityReleaseCountingStore{MemoryJobStore: NewMemoryJobStore()}
	stored, r, job := seedConflictJob(t, store, "qj-cas-cancel-rebase")
	cancelAt := time.Now().UTC()
	job.mu.Lock()
	job.Status = JobStatusCancelRequested
	job.CancelReason = CancelReasonUserRequest
	job.CancelStartedAt = &cancelAt
	job.cancelWanted = true
	job.cancellingAt = cancelAt
	job.mu.Unlock()

	wakeAt := cancelAt.Add(-time.Second)
	fresh := cloneRecord(stored)
	fresh.Handle = NativeHandle{Provider: "fake", ID: "fresh-active-handle"}
	fresh.ValidationState = ValidationValidated
	fresh.ValidationDetail = "external"
	fresh.Metadata = map[string]any{"source": "durable"}
	fresh.WakeEvents = []WakeEvent{{At: wakeAt, Kind: WakePathology, Detail: "external", Claimed: true}}
	if _, err := store.UpsertQueryJobCAS(context.Background(), fresh, stored.Revision); err != nil {
		t.Fatalf("external active CAS: %v", err)
	}

	if persisted := r.persistJob(job); !persisted {
		t.Fatal("safe cancellation rebase was not persisted")
	}
	got, found, err := store.GetQueryJob(context.Background(), stored.ID)
	if err != nil || !found {
		t.Fatalf("read durable: found=%v err=%v", found, err)
	}
	if got.Status != JobStatusCancelRequested || got.Handle.ID != "fresh-active-handle" ||
		got.CancelReason != CancelReasonUserRequest || got.CancelStartedAt == nil || !got.CancelStartedAt.Equal(cancelAt) {
		t.Fatalf("cancel intent did not rebase onto fresh handle: %+v", got)
	}
	if got.ValidationState != ValidationValidated || got.ValidationDetail != "external" ||
		got.Metadata["source"] != "durable" || len(got.WakeEvents) != 1 || !got.WakeEvents[0].Claimed {
		t.Fatalf("fresh sidecars were lost during cancel rebase: %+v", got)
	}
}

func TestDrivenCancelConflictRebasesWithoutYieldingMonitor(t *testing.T) {
	store := &capacityReleaseCountingStore{MemoryJobStore: NewMemoryJobStore()}
	stored, r, job := seedConflictJob(t, store, "qj-cas-driven-cancel-rebase")
	cancelAt := time.Now().UTC()
	job.mu.Lock()
	job.driving = true
	job.Status = JobStatusCancelRequested
	job.CancelReason = CancelReasonUserRequest
	job.CancelStartedAt = &cancelAt
	job.cancelWanted = true
	job.cancellingAt = cancelAt
	job.mu.Unlock()

	fresh := cloneRecord(stored)
	fresh.Handle = NativeHandle{Provider: "fake", ID: "fresh-cancel-handle"}
	fresh.ValidationState = ValidationValidated
	fresh.ValidationDetail = "external"
	fresh.WakeEvents = []WakeEvent{{At: cancelAt.Add(-time.Second), Kind: WakePathology, Detail: "external", Claimed: true}}
	if _, err := store.UpsertQueryJobCAS(context.Background(), fresh, stored.Revision); err != nil {
		t.Fatalf("external active CAS: %v", err)
	}

	if persisted := r.persistJob(job); !persisted {
		t.Fatal("driven cancellation rebase was not persisted")
	}
	got, found, err := store.GetQueryJob(context.Background(), stored.ID)
	if err != nil || !found {
		t.Fatalf("read durable: found=%v err=%v", found, err)
	}
	if got.Status != JobStatusCancelRequested || got.Handle.ID != "fresh-cancel-handle" ||
		got.CancelReason != CancelReasonUserRequest || got.ValidationState != ValidationValidated ||
		len(got.WakeEvents) != 1 || !got.WakeEvents[0].Claimed {
		t.Fatalf("driven cancel did not preserve fresh durable state: %+v", got)
	}
	select {
	case <-job.pollCtx.Done():
		t.Fatal("safe driven cancellation rebase incorrectly yielded the monitor")
	default:
	}
}
