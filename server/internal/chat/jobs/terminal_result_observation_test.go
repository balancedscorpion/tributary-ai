package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTerminalInlineResultContinuesAtAdvertisedCursor(t *testing.T) {
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle: NativeHandle{Provider: "fake", ID: "native-inline"},
				Initial: &StatusSnapshot{
					State: ProviderStateSucceeded,
					DrainedPages: []ResultPage{{
						Rows:          []map[string]any{{"n": 1}},
						Schema:        []map[string]any{{"name": "n", "type": "INT"}},
						NextPageToken: "native-page-2",
					}},
				},
			}, nil
		},
		fetchFn: func(call int, cursor FetchCursor) (ResultPage, error) {
			if call != 1 || cursor.PageToken != "native-page-2" {
				t.Fatalf("fetch call=%d cursor=%+v; want one continuation fetch from native-page-2", call, cursor)
			}
			return ResultPage{Rows: []map[string]any{{"n": 2}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	owner := testOwner()
	job, err := r.Launch(context.Background(), LaunchRequest{
		Owner: owner, Provider: "fake", SQL: "select 1 union all select 2",
		Budgets: Budgets{MaxResultRows: 100, MaxResultBytes: 1 << 20},
	})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusCompleted || job.Result == nil || len(job.Result.Rows) != 2 {
		t.Fatalf("terminal inline result=%+v", job)
	}
	if job.Result.Rows[0]["n"] != 1 || job.Result.Rows[1]["n"] != 2 {
		t.Fatalf("result rows=%+v", job.Result.Rows)
	}
	// Serving the durable in-memory result repeatedly must not touch the
	// provider's consumable cursor again.
	for i := 0; i < 2; i++ {
		result, resultErr := r.Results(job.ID, owner)
		if resultErr != nil || result == nil || len(result.Rows) != 2 {
			t.Fatalf("Results retry %d result=%+v err=%v", i, result, resultErr)
		}
	}
	adapter.mu.Lock()
	fetchCalls := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetchCalls != 1 {
		t.Fatalf("provider fetch calls=%d want 1", fetchCalls)
	}
}

func TestRecoveredInlineResultClearsStaleFetchErrorWithoutRefetch(t *testing.T) {
	pollStarted := make(chan struct{})
	releasePoll := make(chan struct{})
	materializer := &fakeMaterializer{}
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{Handle: NativeHandle{Provider: "fake", ID: "native-recovered"}}, nil
		},
		pollFn: func(call int, _ NativeHandle) (StatusSnapshot, error) {
			if call != 1 {
				t.Fatalf("poll call=%d want 1", call)
			}
			close(pollStarted)
			<-releasePoll
			return StatusSnapshot{
				State: ProviderStateSucceeded,
				DrainedPages: []ResultPage{{
					Rows: []map[string]any{{"n": 7}},
				}},
			}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore(), Materializer: materializer})
	r.RegisterAdapter(adapter)
	owner := testOwner()
	launched, err := r.Launch(context.Background(), LaunchRequest{Owner: owner, Provider: "fake", SQL: "select 7"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	select {
	case <-pollStarted:
	case <-time.After(time.Second):
		t.Fatal("monitor did not begin polling")
	}
	live := r.lookupJob(launched.ID, owner)
	if live == nil {
		t.Fatal("live job missing")
	}
	live.mu.Lock()
	if live.Metadata == nil {
		live.Metadata = map[string]any{}
	}
	live.Metadata["result_fetch_error"] = "old transient 400"
	live.Metadata["materialization_error"] = "old artifact write failure"
	live.Metadata["result_cursor"] = "stale-consumable-native-page"
	live.Metadata["result_recovery_required"] = true
	live.mu.Unlock()
	close(releasePoll)

	waitFor(t, 2*time.Second, func() bool {
		return snapshotStatus(r, launched.ID, owner) == JobStatusCompleted
	}, "inline recovery completed")
	snap, _ := r.Snapshot(launched.ID, owner)
	if _, stale := snap.Metadata["result_fetch_error"]; stale {
		t.Fatalf("stale result_fetch_error survived successful inline recovery: %+v", snap.Metadata)
	}
	if _, stale := snap.Metadata["materialization_error"]; stale {
		t.Fatalf("stale materialization_error survived successful Finalize: %+v", snap.Metadata)
	}
	if _, stale := snap.Metadata["result_cursor"]; stale {
		t.Fatalf("successful durable recovery still exposed a native result cursor: %+v", snap.Metadata)
	}
	if _, stale := snap.Metadata["result_recovery_required"]; stale {
		t.Fatalf("stale result recovery marker survived success: %+v", snap.Metadata)
	}
	if snap.Result == nil || len(snap.Result.Rows) != 1 || snap.Result.Rows[0]["n"] != 7 {
		t.Fatalf("recovered result=%+v", snap.Result)
	}
	adapter.mu.Lock()
	fetchCalls := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetchCalls != 0 {
		t.Fatalf("complete inline result caused %d provider fetches", fetchCalls)
	}
}

func TestSuccessfulDurableRestoreClearsStaleRestoreError(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now()
	if err := store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-fake-restored-error", Provider: "fake", Status: JobStatusCompleted,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "native-restored-error"},
		SubmittedAt: now.Add(-time.Minute), CompletedAt: &now, LastAccess: now,
		SubmitDispatched: true, MaterializationState: MaterializationComplete,
		Metadata: map[string]any{"result_restore_error": "old loader outage"},
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	materializer := &loaderMaterializer{loadResult: &QueryResult{Rows: []map[string]any{{"n": 13}}}}
	r := newTestRegistry(t, RegistryOptions{Store: store, Materializer: materializer})
	r.RegisterAdapter(&fakeAdapter{})
	snap, ok := r.Snapshot("qj-fake-restored-error", owner)
	if !ok || snap.Result == nil || len(snap.Result.Rows) != 1 {
		t.Fatalf("restored snapshot ok=%v snap=%+v", ok, snap)
	}
	if _, stale := snap.Metadata["result_restore_error"]; stale {
		t.Fatalf("stale result_restore_error survived successful durable load: %+v", snap.Metadata)
	}
}

func TestPermanentResultObservationLossNeverCompletesEmpty(t *testing.T) {
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle:  NativeHandle{Provider: "fake", ID: "native-closed"},
				Initial: &StatusSnapshot{State: ProviderStateSucceeded},
			}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{}, PermanentObservationLoss(errors.New("result cursor is CLOSED"))
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	owner := testOwner()
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: owner, Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusOrphaned || job.Result != nil {
		t.Fatalf("permanently unavailable result must be orphaned, not completed-empty: %+v", job)
	}
	hasRecovery := false
	for _, wake := range job.WakeEvents {
		if wake.Kind == WakeRecoveryRequired {
			hasRecovery = true
		}
	}
	if !hasRecovery {
		t.Fatalf("missing recovery-required wake: %+v", job.WakeEvents)
	}
	if _, resultErr := r.Results(job.ID, owner); !errors.Is(resultErr, ErrNotCompleted) {
		t.Fatalf("Results error=%v want ErrNotCompleted", resultErr)
	}
}

func TestTerminalResultFetchRetriesTransientFailureAndClearsError(t *testing.T) {
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle:  NativeHandle{Provider: "fake", ID: "native-transient"},
				Initial: &StatusSnapshot{State: ProviderStateSucceeded},
			}, nil
		},
		fetchFn: func(call int, cursor FetchCursor) (ResultPage, error) {
			if cursor.PageToken != "" {
				t.Fatalf("retry cursor=%q want first result page", cursor.PageToken)
			}
			if call < 3 {
				return ResultPage{}, errors.New("temporary result transport failure")
			}
			return ResultPage{Rows: []map[string]any{{"n": 11}}}, nil
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	owner := testOwner()
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: owner, Provider: "fake", SQL: "select 11"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusCompleted || job.Result == nil || len(job.Result.Rows) != 1 {
		t.Fatalf("transiently recovered job=%+v", job)
	}
	if _, stale := job.Metadata["result_fetch_error"]; stale {
		t.Fatalf("stale result fetch error after successful retry: %+v", job.Metadata)
	}
	adapter.mu.Lock()
	fetchCalls := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetchCalls != 3 {
		t.Fatalf("fetch calls=%d want 3", fetchCalls)
	}
}

func TestExhaustedTransientResultFetchPersistsCursorAndRequiresRecovery(t *testing.T) {
	adapter := &fakeAdapter{
		submitFn: func(int, SubmitRequest) (SubmitResult, error) {
			return SubmitResult{
				Handle: NativeHandle{Provider: "fake", ID: "native-exhausted"},
				Initial: &StatusSnapshot{
					State: ProviderStateSucceeded,
					DrainedPages: []ResultPage{{
						Rows:          []map[string]any{{"n": 1}},
						NextPageToken: "native-page-2",
					}},
				},
			}, nil
		},
		fetchFn: func(int, FetchCursor) (ResultPage, error) {
			return ResultPage{}, errors.New("temporary result service failure")
		},
	}
	r := newTestRegistry(t, RegistryOptions{Store: NewMemoryJobStore()})
	r.RegisterAdapter(adapter)
	owner := testOwner()
	job, err := r.Launch(context.Background(), LaunchRequest{Owner: owner, Provider: "fake", SQL: "select 1"})
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	if job.Status != JobStatusOrphaned || job.Result != nil {
		t.Fatalf("exhausted result retrieval must require recovery: %+v", job)
	}
	if job.Metadata["result_cursor"] != "native-page-2" || job.Metadata["result_recovery_required"] != true {
		t.Fatalf("durable recovery cursor metadata=%+v", job.Metadata)
	}
	adapter.mu.Lock()
	fetchCalls := adapter.fetchCalls
	adapter.mu.Unlock()
	if fetchCalls != 3 {
		t.Fatalf("fetch calls=%d want bounded 3 attempts", fetchCalls)
	}
}

func TestRehydrateInterruptedConsumableResultRequiresReplacementWithoutProviderReplay(t *testing.T) {
	store := NewMemoryJobStore()
	owner := testOwner()
	now := time.Now().UTC()
	if err := store.UpsertQueryJob(context.Background(), JobRecord{
		ID: "qj-interrupted-result", Provider: "fake", Status: JobStatusRunning,
		Owner: owner, Handle: NativeHandle{Provider: "fake", ID: "native-interrupted-result"},
		SubmittedAt: now.Add(-time.Minute), LastAccess: now, SubmitDispatched: true,
		Budgets: Budgets{HardExecution: time.Hour},
		Metadata: map[string]any{
			"result_recovery_required": true,
			"result_cursor":            "native-page-2",
			"result_fetch_error":       "transport failed after chunk one",
		},
	}); err != nil {
		t.Fatalf("seed interrupted result: %v", err)
	}
	adoptCalls := 0
	adapter := &fakeAdapter{adoptFn: func(NativeHandle) (AdoptOutcome, StatusSnapshot, error) {
		adoptCalls++
		return AdoptOutcomeTerminal, StatusSnapshot{State: ProviderStateSucceeded}, nil
	}}
	r := newTestRegistry(t, RegistryOptions{Store: store, InstanceID: "result-recovery"})
	r.RegisterAdapter(adapter)

	stats, err := r.Rehydrate(context.Background())
	if err != nil {
		t.Fatalf("rehydrate: %v", err)
	}
	if stats.Orphaned != 1 || adoptCalls != 0 {
		t.Fatalf("stats=%+v adoptCalls=%d, want recovery without provider replay", stats, adoptCalls)
	}
	record, found, err := store.GetQueryJob(context.Background(), "qj-interrupted-result")
	if err != nil || !found || record.Status != JobStatusOrphaned {
		t.Fatalf("recovered record=%+v found=%v err=%v", record, found, err)
	}
	if len(record.WakeEvents) == 0 || record.WakeEvents[len(record.WakeEvents)-1].Kind != WakeRecoveryRequired {
		t.Fatalf("missing recovery-required wake: %+v", record.WakeEvents)
	}
}
