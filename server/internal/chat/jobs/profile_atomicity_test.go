package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func wakeKinds(evs []WakeEvent) map[WakeKind]int {
	out := map[WakeKind]int{}
	for _, ev := range evs {
		out[ev.Kind]++
	}
	return out
}

// TestFencedTerminalWriteIsSingleAndComplete proves the terminal profile
// record is COMPLETE immediately after its one fenced transition — store
// CompletedAt, the exact wakes, the outcome — and that no later
// Upsert/CAS follows (revision is frozen the moment Launch returns).
func TestFencedTerminalWriteIsSingleAndComplete(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	adapter.profileResult = QueryProfile{Valid: false, Error: "syntax error"}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select bad"})
	if !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("err=%v want ErrProfileRejected", err)
	}

	rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
	if !ok {
		t.Fatalf("record missing")
	}
	if rec.Status != JobStatusFailed || rec.ProfileOutcome != ProfileOutcomeInvalid {
		t.Fatalf("terminal record: %s/%s want failed/invalid", rec.Status, rec.ProfileOutcome)
	}
	if rec.CompletedAt == nil {
		t.Fatalf("CompletedAt must be stamped IN the fenced transition")
	}
	kinds := wakeKinds(rec.WakeEvents)
	if kinds[WakeRecoveryRequired] != 1 || kinds[WakeTerminal] != 1 || len(rec.WakeEvents) != 2 {
		t.Fatalf("wakes must be exactly {recovery_required, terminal}: %+v", rec.WakeEvents)
	}
	frozenRev := rec.Revision

	// No trailing ordinary persist of any kind.
	time.Sleep(80 * time.Millisecond)
	recAfter, _, _ := store.GetQueryJob(context.Background(), job.ID)
	if recAfter.Revision != frozenRev {
		t.Fatalf("revision moved after the fenced terminal write: %d -> %d (a follow-up Upsert/CAS happened)", frozenRev, recAfter.Revision)
	}
	// Capacity was released atomically by the terminal write.
	r.RegisterAdapter(newProfilingAdapter(true))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Launch(ctx, LaunchRequest{Owner: JobOwner{ThreadID: "thread-1", Handle: "tok_n"}, Provider: "fake", SQL: "select 2"}); err != nil {
		t.Fatalf("capacity not released atomically: %v", err)
	}
	// Local state mirrors the store exactly.
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.CompletedAt == nil || !snap.CompletedAt.Equal(*rec.CompletedAt) {
		t.Fatalf("local CompletedAt must adopt the STORE timestamp: %v vs %v", snap.CompletedAt, rec.CompletedAt)
	}
}

func TestFencedCancellationWriteIsSingleAndComplete(t *testing.T) {
	store := NewMemoryJobStore()
	adapter := newProfilingAdapter(true)
	adapter.gate = make(chan struct{}) // blocks until cancel
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	done := make(chan struct {
		job *Job
		err error
	}, 1)
	go func() {
		job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
		done <- struct {
			job *Job
			err error
		}{job, err}
	}()
	var jobID string
	waitFor(t, 2*time.Second, func() bool {
		recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
		if len(recs) == 1 && recs[0].Status == JobStatusProfiling {
			jobID = recs[0].ID
			return true
		}
		return false
	}, "profiling")
	if err := r.RequestCancel(jobID, testOwner(), CancelReasonUserRequest); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	out := <-done
	if !errors.Is(out.err, context.Canceled) {
		t.Fatalf("err=%v want cancellation", out.err)
	}
	rec, _, _ := store.GetQueryJob(context.Background(), jobID)
	if rec.Status != JobStatusCancelled || rec.CancelOutcome != CancelOutcomeBeforeSubmit {
		t.Fatalf("cancel record: %s/%s want cancelled/before_submit", rec.Status, rec.CancelOutcome)
	}
	if rec.CompletedAt == nil {
		t.Fatalf("cancellation must stamp CompletedAt atomically")
	}
	kinds := wakeKinds(rec.WakeEvents)
	if kinds[WakeTerminal] != 1 || len(rec.WakeEvents) != 1 {
		t.Fatalf("cancel wakes must be exactly {terminal}: %+v", rec.WakeEvents)
	}
	frozenRev := rec.Revision
	time.Sleep(80 * time.Millisecond)
	recAfter, _, _ := store.GetQueryJob(context.Background(), jobID)
	if recAfter.Revision != frozenRev {
		t.Fatalf("revision moved after fenced cancellation: %d -> %d", frozenRev, recAfter.Revision)
	}
}

// conflictTerminalStore forces a revision conflict on the first terminal
// mutation, committing a CONCURRENT WINNER (different verdict) first.
type conflictTerminalStore struct {
	*MemoryJobStore
	fired atomic.Bool
}

func (s *conflictTerminalStore) ApplyProfileMutation(ctx context.Context, jobID, holder string, expectedRevision int64, mut ProfileMutation) (ProfileMutationResult, error) {
	if mutIsTerminal(mut) && s.fired.CompareAndSwap(false, true) {
		// The concurrent winner terminalizes as orphaned/indeterminate.
		rec, ok, _ := s.MemoryJobStore.GetQueryJob(ctx, jobID)
		if ok {
			now := time.Now()
			rec.Status = JobStatusOrphaned
			rec.ProfileOutcome = ProfileOutcomeIndeterminate
			rec.Error = "terminalized by concurrent winner"
			rec.CompletedAt = &now
			rec.WakeEvents = append(rec.WakeEvents, WakeEvent{At: now, Kind: WakeRecoveryRequired, Detail: "winner"})
			_ = s.MemoryJobStore.UpsertQueryJob(ctx, rec) // bumps revision
		}
		return ProfileMutationResult{}, ErrRevisionConflict
	}
	return s.MemoryJobStore.ApplyProfileMutation(ctx, jobID, holder, expectedRevision, mut)
}

func TestFencedConflictNeverOverwritesConcurrentWinner(t *testing.T) {
	store := &conflictTerminalStore{MemoryJobStore: NewMemoryJobStore()}
	adapter := newProfilingAdapter(true)
	adapter.profileResult = QueryProfile{Valid: false, Error: "bad"}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	_, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select bad"})
	// Our verdict was NOT confirmed: we must yield, not claim terminal.
	if !errors.Is(err, ErrProfilePending) {
		t.Fatalf("err=%v want ErrProfilePending (unconfirmed verdict yields)", err)
	}
	if adapter.submitCount() != 0 {
		t.Fatalf("yield must mean zero submit")
	}
	// The concurrent winner's verdict survives untouched.
	recs, _ := store.ListQueryJobsByThread(context.Background(), "thread-1")
	if len(recs) != 1 {
		t.Fatalf("records: %+v", recs)
	}
	rec := recs[0]
	if rec.Status != JobStatusOrphaned || rec.ProfileOutcome != ProfileOutcomeIndeterminate || rec.Error != "terminalized by concurrent winner" {
		t.Fatalf("concurrent winner overwritten: %s/%s/%q", rec.Status, rec.ProfileOutcome, rec.Error)
	}
}

// ambiguousTerminalStore commits the mutation but reports a transport
// error, exercising recognize-and-adopt.
type ambiguousTerminalStore struct {
	*MemoryJobStore
	fired atomic.Bool
}

func (s *ambiguousTerminalStore) ApplyProfileMutation(ctx context.Context, jobID, holder string, expectedRevision int64, mut ProfileMutation) (ProfileMutationResult, error) {
	if mutIsTerminal(mut) && s.fired.CompareAndSwap(false, true) {
		if _, err := s.MemoryJobStore.ApplyProfileMutation(ctx, jobID, holder, expectedRevision, mut); err != nil {
			return ProfileMutationResult{}, err
		}
		return ProfileMutationResult{}, errors.New("transport timeout (write may have committed)")
	}
	return s.MemoryJobStore.ApplyProfileMutation(ctx, jobID, holder, expectedRevision, mut)
}

func TestFencedAmbiguousCommitIsAdoptedNotDuplicated(t *testing.T) {
	store := &ambiguousTerminalStore{MemoryJobStore: NewMemoryJobStore()}
	adapter := newProfilingAdapter(true)
	adapter.profileResult = QueryProfile{Valid: false, Error: "bad"}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select bad"})
	// The committed write was recognized and adopted: the caller gets the
	// real typed verdict, not a spurious pending.
	if !errors.Is(err, ErrProfileRejected) {
		t.Fatalf("err=%v want ErrProfileRejected via adoption", err)
	}
	rec, _, _ := store.GetQueryJob(context.Background(), job.ID)
	kinds := wakeKinds(rec.WakeEvents)
	if kinds[WakeRecoveryRequired] != 1 || kinds[WakeTerminal] != 1 || len(rec.WakeEvents) != 2 {
		t.Fatalf("adoption must not duplicate wakes: %+v", rec.WakeEvents)
	}
	if rec.Status != JobStatusFailed || rec.CompletedAt == nil {
		t.Fatalf("adopted record: %s completed=%v", rec.Status, rec.CompletedAt)
	}
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Status != JobStatusFailed || snap.CompletedAt == nil {
		t.Fatalf("local state must mirror the adopted record: %s", snap.Status)
	}
}

// TestRehydrateProfilingFenceIsAtomic proves rehydration terminalizes an
// interrupted profile through ONE fenced write carrying everything, with
// no follow-up persist, and adopts on conflict instead of overwriting.
func TestRehydrateProfilingFenceIsAtomic(t *testing.T) {
	owner := testOwner()
	now := time.Now()
	store := NewMemoryJobStore()
	_ = store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-rfa", Provider: "fake", Status: JobStatusProfiling,
		Owner: owner, SQL: "select 1",
		ProfileDispatched: true, ProfileHandle: NativeHandle{Provider: "fake", ID: "ph"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
	})
	adapter := newProfilingAdapter(true)
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "proc-R"})
	r.RegisterAdapter(adapter)
	stats, err := r.Rehydrate(context.Background())
	if err != nil || stats.Orphaned != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	rec, _, _ := store.GetQueryJob(context.Background(), "qj-fake-rfa")
	if rec.Status != JobStatusOrphaned || rec.ProfileOutcome != ProfileOutcomeIndeterminate || rec.CompletedAt == nil {
		t.Fatalf("rehydrate terminal record incomplete: %s/%s completed=%v", rec.Status, rec.ProfileOutcome, rec.CompletedAt)
	}
	kinds := wakeKinds(rec.WakeEvents)
	if kinds[WakeRecoveryRequired] != 1 || kinds[WakeTerminal] != 1 {
		t.Fatalf("rehydrate wakes: %+v", rec.WakeEvents)
	}
	if adapter.cancelCalls.Load() != 1 {
		t.Fatalf("handle cancel calls=%d want 1", adapter.cancelCalls.Load())
	}
	frozenRev := rec.Revision
	time.Sleep(80 * time.Millisecond)
	recAfter, _, _ := store.GetQueryJob(context.Background(), "qj-fake-rfa")
	if recAfter.Revision != frozenRev {
		t.Fatalf("revision moved after rehydrate fence: %d -> %d (insert-then-persist regression)", frozenRev, recAfter.Revision)
	}

	// Conflict variant: the fence conflicts because a concurrent winner
	// already expired the record; rehydrate must adopt, not overwrite.
	store2 := &conflictTerminalStore{MemoryJobStore: NewMemoryJobStore()}
	_ = store2.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-rfc", Provider: "fake", Status: JobStatusProfiling,
		Owner: owner, SQL: "select 1", ProfileDispatched: true,
		SubmittedAt: now.Add(-time.Minute), LastAccess: now,
	})
	r2 := newTestRegistry(t, RegistryOptions{Store: store2, InstanceID: "proc-R2"})
	r2.RegisterAdapter(newProfilingAdapter(true))
	stats2, err := r2.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate 2: %v", err)
	}
	if stats2.Orphaned != 1 { // adopted the winner's orphaned verdict
		t.Fatalf("stats2=%+v want adopted orphaned=1", stats2)
	}
	rec2, _, _ := store2.GetQueryJob(context.Background(), "qj-fake-rfc")
	if rec2.Error != "terminalized by concurrent winner" {
		t.Fatalf("rehydrate overwrote the concurrent winner: %q", rec2.Error)
	}
	snap, ok := r2.Snapshot("qj-fake-rfc", owner)
	if !ok || snap.Status != JobStatusOrphaned {
		t.Fatalf("adopted local state: ok=%v %+v", ok, snap)
	}
}
