package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainAssemblerResumeUsesCumulativeBudgetAndDropsStaleBacklog(t *testing.T) {
	budgets := Budgets{MaxResultRows: 3, MaxResultBytes: 1_000}
	job := &Job{
		Budgets: budgets, DrainRowsCommitted: 2, DrainBytesCommitted: 10,
		assembler: newResultAssemblerFromCommitted(budgets, 1, 5, false),
		backlog:   &drainBacklog{pages: []ResultPage{{Rows: []map[string]any{{"stale": true}}}}},
	}
	job.mu.Lock()
	prepareDrainAssemblerLocked(job)
	assembler := job.assembler
	backlog := job.backlog
	job.mu.Unlock()
	if backlog != nil {
		t.Fatal("stale pre-takeover backlog was retained after durable accounting advanced")
	}

	page := ResultPage{Rows: []map[string]any{{"n": 1}, {"n": 2}}, Bytes: 20}
	bounded, more := assembler.plan(page)
	if more || len(bounded.Rows) != 1 || !bounded.Truncated {
		t.Fatalf("bounded=%+v more=%v; want one admitted row and explicit truncation", bounded, more)
	}
	assembler.commit(page, bounded)
	summary := assembler.summary()
	if summary.Rows != 3 || summary.Bytes != 20 || !summary.Truncated {
		t.Fatalf("cumulative summary=%+v want rows=3 bytes=20 truncated", summary)
	}
}

func TestDrainConflictRetryPreservesFreshLifecycleValidationAndWake(t *testing.T) {
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	h1 := drainHandle("query", "uri-1")
	h2 := drainHandle("query", "uri-2")
	rec := JobRecord{
		ID: "qj-drain-merge", Provider: "fake", Status: JobStatusRunning,
		Owner: testOwner(), Handle: h1, SubmittedAt: now, LastAccess: now,
		DrainSeq: 1, DrainRowsCommitted: 5, DrainBytesCommitted: 50,
		ValidationState: ValidationUnvalidated,
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stored, ok, err := store.GetQueryJob(context.Background(), rec.ID)
	if err != nil || !ok {
		t.Fatalf("get seed: ok=%v err=%v", ok, err)
	}

	committer := &casCommitter{store: store}
	injected := false
	committer.setFailWhen(func(DrainCommit) error {
		if injected {
			return nil
		}
		injected = true
		fresh, found, getErr := store.GetQueryJob(context.Background(), rec.ID)
		if getErr != nil || !found {
			return errors.New("sidecar seed record missing")
		}
		cancelAt := now.Add(time.Second)
		fresh.Status = JobStatusCancelRequested
		fresh.CancelReason = CancelReasonUserRequest
		fresh.CancelStartedAt = &cancelAt
		fresh.ValidationState = ValidationValidated
		fresh.ValidationDetail = "row count checked"
		fresh.WakeEvents = append(fresh.WakeEvents, WakeEvent{At: cancelAt, Kind: WakePathology, Detail: "sidecar wake"})
		_, putErr := store.UpsertQueryJobCAS(context.Background(), fresh, fresh.Revision)
		return putErr
	})
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	job := r.jobFromRecord(stored, &fakeAdapter{})
	bounded := ResultPage{Rows: []map[string]any{{"n": 6}}, Bytes: 10, Truncated: true, CursorAfter: &h2}
	if err := r.commitDrainObservation(context.Background(), job, bounded, &h2); err != nil {
		t.Fatalf("commit after sidecar conflict: %v", err)
	}

	got, ok, err := store.GetQueryJob(context.Background(), rec.ID)
	if err != nil || !ok {
		t.Fatalf("get committed record: ok=%v err=%v", ok, err)
	}
	if got.Status != JobStatusCancelRequested || got.CancelReason != CancelReasonUserRequest || got.CancelStartedAt == nil {
		t.Fatalf("fresh cancel mutation lost: %+v", got)
	}
	if got.ValidationState != ValidationValidated || got.ValidationDetail != "row count checked" {
		t.Fatalf("fresh validation mutation lost: %+v", got)
	}
	if len(got.WakeEvents) != 1 || got.WakeEvents[0].Detail != "sidecar wake" {
		t.Fatalf("fresh wake mutation lost: %+v", got.WakeEvents)
	}
	if got.DrainSeq != 2 || got.DrainRowsCommitted != 6 || got.DrainBytesCommitted != 60 || !got.DrainTruncated {
		t.Fatalf("cumulative drain accounting=%+v", got)
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Status != JobStatusCancelRequested || job.Validation != ValidationValidated ||
		job.DrainRowsCommitted != 6 || job.DrainBytesCommitted != 60 || !job.DrainTruncated {
		t.Fatalf("local job was not synchronized from successful merged commit: %+v", job)
	}
}

func TestDrainConflictDoesNotAppendOntoFreshTerminalRecord(t *testing.T) {
	store := NewMemoryJobStore()
	now := time.Now().UTC()
	h1 := drainHandle("query", "uri-1")
	h2 := drainHandle("query", "uri-2")
	rec := JobRecord{ID: "qj-drain-terminal", Provider: "fake", Status: JobStatusRunning,
		Owner: testOwner(), Handle: h1, SubmittedAt: now, LastAccess: now, DrainSeq: 1}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stored, _, _ := store.GetQueryJob(context.Background(), rec.ID)
	committer := &casCommitter{store: store}
	injected := false
	committer.setFailWhen(func(DrainCommit) error {
		if injected {
			return nil
		}
		injected = true
		fresh, _, _ := store.GetQueryJob(context.Background(), rec.ID)
		fresh.Status = JobStatusCompleted
		completed := now.Add(time.Second)
		fresh.CompletedAt = &completed
		_, err := store.UpsertQueryJobCAS(context.Background(), fresh, fresh.Revision)
		return err
	})
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: committer})
	job := r.jobFromRecord(stored, &fakeAdapter{})
	err := r.commitDrainObservation(context.Background(), job, ResultPage{Rows: []map[string]any{{"n": 2}}}, &h2)
	if !errors.Is(err, ErrDrainSequenceConflict) {
		t.Fatalf("err=%v want ErrDrainSequenceConflict", err)
	}
	got, _, _ := store.GetQueryJob(context.Background(), rec.ID)
	if got.Status != JobStatusCompleted || got.DrainSeq != 1 {
		t.Fatalf("terminal record was overwritten by drain retry: %+v", got)
	}
}

type contextBlockingMaterializer struct{ started chan struct{} }

func (m *contextBlockingMaterializer) AppendChunk(ctx context.Context, _ string, _ JobOwner, _ ResultPage) error {
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return ctx.Err()
}

func (*contextBlockingMaterializer) Finalize(context.Context, string, JobOwner, *QueryResult, MaterializationSummary) error {
	return nil
}

func TestBlockedAppendChunkIsBoundedByRemainingHardBudget(t *testing.T) {
	materializer := &contextBlockingMaterializer{started: make(chan struct{}, 1)}
	sched := fastScheduler()
	sched.ProviderCallTimeout = 5 * time.Second
	r := newTestRegistry(t, RegistryOptions{Scheduler: sched, Materializer: materializer})
	now := time.Now()
	job := &Job{ID: "qj-blocked-append", Owner: testOwner(), SubmittedAt: now,
		RemoteSubmittedAt: &now, Budgets: Budgets{HardExecution: 35 * time.Millisecond}}
	started := time.Now()
	r.materializeChunk(context.Background(), job, ResultPage{Rows: []map[string]any{{"n": 1}}}, true)
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("blocked append ignored hard budget: %s", elapsed)
	}
	select {
	case <-materializer.started:
	default:
		t.Fatal("AppendChunk was not called")
	}
	job.mu.RLock()
	defer job.mu.RUnlock()
	if job.Metadata["materialization_error"] == nil {
		t.Fatalf("bounded append error was not recorded: %+v", job.Metadata)
	}
}

func TestCancelAcceptanceKeepsOriginalDurableGraceStart(t *testing.T) {
	now := time.Now().UTC()
	started := now.Add(-time.Minute)
	job := &Job{Status: JobStatusCancelRequested, CancelStartedAt: &started, cancellingAt: started}
	job.mu.Lock()
	transitioned := markCancelAcceptedLocked(job, now)
	job.mu.Unlock()
	if !transitioned || job.Status != JobStatusCancelling {
		t.Fatalf("cancel was not transitioned: %+v", job)
	}
	if job.CancelStartedAt == nil || !job.CancelStartedAt.Equal(started) || !job.cancellingAt.Equal(started) {
		t.Fatalf("cancel grace was extended: durable=%v runtime=%v want=%v", job.CancelStartedAt, job.cancellingAt, started)
	}
}
