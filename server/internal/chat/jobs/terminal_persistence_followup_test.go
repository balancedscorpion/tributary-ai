package jobs

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type terminalFailureStore struct {
	*MemoryJobStore
	failRemaining atomic.Int32
	failAll       atomic.Bool
	attempts      atomic.Int32
}

func (s *terminalFailureStore) UpsertQueryJobCAS(ctx context.Context, rec JobRecord, expected int64) (int64, error) {
	if isTerminalStatus(rec.Status) {
		s.attempts.Add(1)
		if s.failAll.Load() {
			return 0, errors.New("temporary terminal store outage")
		}
		for {
			remaining := s.failRemaining.Load()
			if remaining <= 0 {
				break
			}
			if s.failRemaining.CompareAndSwap(remaining, remaining-1) {
				return 0, errors.New("temporary terminal store outage")
			}
		}
	}
	return s.MemoryJobStore.UpsertQueryJobCAS(ctx, rec, expected)
}

func TestTerminalPersistenceRetriesBeforeReleasingCapacity(t *testing.T) {
	store := &terminalFailureStore{MemoryJobStore: NewMemoryJobStore()}
	store.failRemaining.Store(2)
	adapter := &fakeAdapter{pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
		return StatusSnapshot{State: ProviderStateSucceeded}, nil
	}}
	r := newTestRegistry(t, RegistryOptions{
		Store: store,
		Admission: &AdmissionConfig{
			MaxGlobal:        1,
			MaxPerThread:     1,
			MaxPerConnection: 1,
		},
	})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
		return ok && rec.Status == JobStatusCompleted
	}, "terminal record persisted after transient failures")
	if got := store.attempts.Load(); got < 3 {
		t.Fatalf("terminal attempts=%d want at least 3", got)
	}

	// The first job's slot is durable-terminal before another reservation is
	// admitted; this proves capacity was not released from memory alone.
	secondOwner := JobOwner{ThreadID: "thread-2", Handle: "tok_second"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Launch(ctx, LaunchRequest{Owner: secondOwner, Provider: "fake", SQL: "select 2"}); err != nil {
		t.Fatalf("capacity remained stranded after terminal retry: %v", err)
	}
}

func TestTerminalPersistenceRepairConvergesAfterLongerOutage(t *testing.T) {
	store := &terminalFailureStore{MemoryJobStore: NewMemoryJobStore()}
	store.failAll.Store(true)
	adapter := &fakeAdapter{pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
		return StatusSnapshot{State: ProviderStateSucceeded}, nil
	}}
	r := newTestRegistry(t, RegistryOptions{Store: store})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		snap, ok := r.Snapshot(job.ID, testOwner())
		return ok && snap.Status == JobStatusCompleted && store.attempts.Load() >= terminalPersistAttempts
	}, "bounded synchronous terminal attempts")
	rec, ok, getErr := store.GetQueryJob(context.Background(), job.ID)
	if getErr != nil || !ok || isTerminalStatus(rec.Status) {
		t.Fatalf("durable record should still be active during outage: ok=%v err=%v status=%s", ok, getErr, rec.Status)
	}

	store.failAll.Store(false)
	waitFor(t, 3*time.Second, func() bool {
		rec, ok, _ := store.GetQueryJob(context.Background(), job.ID)
		return ok && rec.Status == JobStatusCompleted
	}, "background terminal persistence repair")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.terminalPersistPending {
		t.Fatal("terminal persistence remained pending after durable repair")
	}
}

type memoryDrainMaterializer struct {
	store     *MemoryJobStore
	finalized atomic.Bool
}

func (m *memoryDrainMaterializer) AppendChunk(context.Context, string, JobOwner, ResultPage) error {
	return nil
}

func (m *memoryDrainMaterializer) Finalize(context.Context, string, JobOwner, *QueryResult, MaterializationSummary) error {
	m.finalized.Store(true)
	return nil
}

func (m *memoryDrainMaterializer) CommitDrainObservation(ctx context.Context, commit DrainCommit) (int64, error) {
	return m.store.UpsertQueryJobCAS(ctx, commit.Record, commit.Record.Revision)
}

func (m *memoryDrainMaterializer) CommitDrainObservationFenced(ctx context.Context, commit DrainCommit, _ string) (int64, error) {
	return m.CommitDrainObservation(ctx, commit)
}

func TestTerminalDrainCommitPreventsExpiredCursorRedrive(t *testing.T) {
	store := &terminalFailureStore{MemoryJobStore: NewMemoryJobStore()}
	store.failAll.Store(true) // ordinary terminal lifecycle writes cannot land
	materializer := &memoryDrainMaterializer{store: store.MemoryJobStore}
	finalHandle := drainHandle("forward-only", "uri-consumed-terminal")
	var adopts atomic.Int32
	adapter := &fakeAdapter{
		caps: ProviderCapabilities{RequiresContinuousDrain: true, DetachedRepoll: true},
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle: drainHandle("forward-only", "uri-submit"),
				Initial: &StatusSnapshot{
					State:         ProviderStateSucceeded,
					UpdatedHandle: &finalHandle,
					DrainedPages: []ResultPage{{
						Rows:        []map[string]any{{"n": 1}},
						CursorAfter: &finalHandle,
					}},
				},
			}, nil
		},
		adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
			adopts.Add(1)
			return AdoptOutcomeOrphaned, StatusSnapshot{}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: materializer})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	rec, ok, getErr := store.GetQueryJob(context.Background(), job.ID)
	if getErr != nil || !ok || rec.Status != JobStatusCompleted || rec.DrainSeq != 1 {
		t.Fatalf("final cursor and terminal state were not atomic: ok=%v err=%v record=%+v", ok, getErr, rec)
	}
	if rec.Handle.Extra["next_uri"] != "uri-consumed-terminal" {
		t.Fatalf("durable terminal cursor=%q", rec.Handle.Extra["next_uri"])
	}

	restarted := newTestRegistry(t, RegistryOptions{Store: store, Materializer: materializer})
	restarted.RegisterAdapter(adapter)
	stats, err := restarted.Rehydrate(context.Background())
	if err != nil || stats.Adopted != 0 || adopts.Load() != 0 {
		t.Fatalf("terminal drain cursor was re-driven: stats=%+v adopts=%d err=%v", stats, adopts.Load(), err)
	}
}

func TestProfilingRejectsUnfencedValidationAndArtifactWrites(t *testing.T) {
	store := NewMemoryJobStore()
	now := time.Now()
	rec := JobRecord{
		ID: "qj-profile-sidecars", Provider: "fake", Status: JobStatusProfiling,
		Owner: testOwner(), SubmittedAt: now, LastAccess: now,
		ValidationState: ValidationUnvalidated,
	}
	if err := store.UpsertQueryJob(context.Background(), rec); err != nil {
		t.Fatalf("seed: %v", err)
	}
	stored, _, _ := store.GetQueryJob(context.Background(), rec.ID)
	r := newTestRegistry(t, RegistryOptions{Store: store})
	job := r.jobFromRecord(stored, &fakeAdapter{})
	r.mu.Lock()
	r.jobs[job.ID] = job
	r.mu.Unlock()

	if err := r.SetValidation(job.ID, testOwner(), ValidationValidated, "too early"); !errors.Is(err, ErrProfilePending) {
		t.Fatalf("SetValidation err=%v want ErrProfilePending", err)
	}
	if err := r.SetArtifactID(job.ID, testOwner(), "art-too-early"); !errors.Is(err, ErrProfilePending) {
		t.Fatalf("SetArtifactID err=%v want ErrProfilePending", err)
	}
	after, _, _ := store.GetQueryJob(context.Background(), job.ID)
	if after.Revision != stored.Revision || after.ValidationState != ValidationUnvalidated || after.ArtifactID != "" {
		t.Fatalf("unfenced profiling sidecar write reached store: before=%+v after=%+v", stored, after)
	}
}

func TestInlineTerminalPagesAreNotFetchedTwice(t *testing.T) {
	materializer := &fakeMaterializer{}
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle: NativeHandle{Provider: "fake", ID: "inline"},
				Initial: &StatusSnapshot{
					State:        ProviderStateSucceeded,
					DrainedPages: []ResultPage{{Rows: []map[string]any{{"n": 1}}}},
				},
			}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{Rows: []map[string]any{{"n": 1}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Materializer: materializer})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Result == nil || len(job.Result.Rows) != 1 {
		t.Fatalf("inline result duplicated or lost: %+v", job.Result)
	}
	adapter.mu.Lock()
	fetches := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetches != 0 {
		t.Fatalf("terminal inline result re-fetched %d times", fetches)
	}
	materializer.mu.Lock()
	chunks := len(materializer.chunks)
	materializer.mu.Unlock()
	if chunks != 1 {
		t.Fatalf("inline page materialized %d times", chunks)
	}
}

func TestAdapterTruncatedPageStopsNativeContinuation(t *testing.T) {
	adapter := &fakeAdapter{
		pollFn: func(int, NativeHandle) (StatusSnapshot, error) {
			return StatusSnapshot{State: ProviderStateSucceeded}, nil
		},
		fetchFn: func(call int, cursor FetchCursor) (ResultPage, error) {
			if call > 1 {
				return ResultPage{Rows: []map[string]any{{"n": 2}}}, nil
			}
			return ResultPage{
				Rows:          []map[string]any{{"n": 1}},
				NextPageToken: "provider-page-2",
				Truncated:     true,
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{})
	r.RegisterAdapter(adapter)
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: testOwner(), Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		return snapshotStatus(r, job.ID, testOwner()) == JobStatusCompleted
	}, "terminal result fetch")
	snap, _ := r.Snapshot(job.ID, testOwner())
	if snap.Result == nil || len(snap.Result.Rows) != 1 || snap.Result.Rows[0]["n"] != 1 {
		t.Fatalf("adapter-truncated page mishandled: %+v", snap.Result)
	}
	adapter.mu.Lock()
	fetches := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetches != 1 {
		t.Fatalf("adapter truncation advanced native continuation: fetches=%d", fetches)
	}
	if _, exposed := snap.Metadata["result_cursor"]; exposed {
		t.Fatalf("bounded result must not expose a consumable native continuation: %+v", snap.Metadata)
	}
}
