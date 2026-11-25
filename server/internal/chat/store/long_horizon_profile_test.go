package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

func profileTestJob(thread Thread, run Run, id, idempotencyKey string, now time.Time) jobs.JobRecord {
	record := testDurableJob(thread, run, now)
	record.ID = id
	record.IdempotencyKey = idempotencyKey
	record.Status = jobs.JobStatusProfiling
	return record
}

func TestJSONQueryJobProfilingFieldsRoundTripAndRemainActive(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 12, 30, 0, 123000000, time.UTC)
	remoteSubmittedAt := now.Add(3 * time.Second)
	nextPollAt := now.Add(11 * time.Second)
	cancelStartedAt := now.Add(7 * time.Second)
	estimatedBytes := int64(8192)
	record := profileTestJob(thread, run, "job-profile-roundtrip", "idem-profile-roundtrip", now)
	record.RemoteSubmittedAt = &remoteSubmittedAt
	record.NextPollAt = &nextPollAt
	record.NextPollAction = jobs.RetryActionCancel
	record.CancelStartedAt = &cancelStartedAt
	record.DrainRowsCommitted = 7
	record.DrainBytesCommitted = 4096
	record.DrainTruncated = true
	record.Profile = &jobs.QueryProfile{
		Valid: true, EstimatedBytes: &estimatedBytes, EstimateIsExact: true,
		PlanSummary: "partition-pruned scan", Warnings: []string{"estimate only"},
		ProviderDetails: map[string]any{"statement_kind": "EXPLAIN"},
	}
	record.ProfileOutcome = jobs.ProfileOutcomeAccepted
	record.ProfileHandle = jobs.NativeHandle{Provider: "databricks", ID: "profile-statement", Extra: map[string]string{"warehouse": "wh-1"}}
	record.ProfilePhase = "explain_running"
	record.ProfileDispatched = true

	granted, revision, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{
		MaxGlobal: 8, MaxPerThread: 3, MaxPerConnection: 2,
		MaxProfilingGlobal: 4, MaxProfilingPerThread: 2, MaxProfilingPerConnection: 2,
	})
	if err != nil || !granted || revision != 1 {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok, err := reopened.GetQueryJob(ctx, record.ID)
	if err != nil || !ok {
		t.Fatalf("GetQueryJob ok=%v err=%v", ok, err)
	}
	if got.Status != jobs.JobStatusProfiling || got.Profile == nil || got.Profile.EstimatedBytes == nil ||
		*got.Profile.EstimatedBytes != estimatedBytes || got.Profile.PlanSummary != record.Profile.PlanSummary ||
		got.ProfileOutcome != record.ProfileOutcome || got.ProfileHandle.ID != record.ProfileHandle.ID ||
		got.ProfileHandle.Extra["warehouse"] != "wh-1" || got.ProfilePhase != record.ProfilePhase || !got.ProfileDispatched ||
		got.RemoteSubmittedAt == nil || !got.RemoteSubmittedAt.Equal(remoteSubmittedAt) || got.NextPollAt == nil || !got.NextPollAt.Equal(nextPollAt) ||
		got.NextPollAction != record.NextPollAction || got.CancelStartedAt == nil || !got.CancelStartedAt.Equal(cancelStartedAt) ||
		got.DrainRowsCommitted != record.DrainRowsCommitted || got.DrainBytesCommitted != record.DrainBytesCommitted || !got.DrainTruncated {
		t.Fatalf("round-tripped profiling job=%#v", got)
	}
	active, err := reopened.ListActiveQueryJobs(ctx)
	if err != nil || len(active) != 1 || active[0].ID != record.ID || active[0].Status != jobs.JobStatusProfiling {
		t.Fatalf("active profiling jobs=%#v err=%v", active, err)
	}
}

func TestJSONApplyProfileMutationTripleFenceAndAtomicTerminalEffects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	now := time.Now().UTC()
	record := profileTestJob(thread, run, "job-profile-fenced", "idem-profile-fenced", now)
	granted, revision, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{
		MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1,
		MaxProfilingGlobal: 1, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 1,
	})
	if err != nil || !granted {
		t.Fatalf("reserve granted=%v revision=%d err=%v", granted, revision, err)
	}
	if acquired, err := store.AcquireMonitorLease(ctx, record.ID, "holder-a", time.Minute); err != nil || !acquired {
		t.Fatalf("AcquireMonitorLease acquired=%v err=%v", acquired, err)
	}
	phase := "dry_run"
	if _, err := store.ApplyProfileMutation(ctx, record.ID, "holder-a", revision+1, jobs.ProfileMutation{Phase: &phase}); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("stale revision err=%v, want ErrRevisionConflict", err)
	}
	if _, err := store.ApplyProfileMutation(ctx, record.ID, "holder-b", revision, jobs.ProfileMutation{Phase: &phase}); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("wrong holder err=%v, want ErrLeaseLost", err)
	}

	store.longHorizonMu.Lock()
	wrapper, readErr := store.readJSONQueryJobLocked(record.ID, false)
	if readErr == nil {
		wrapper.MonitorLease.ExpiresAt = nowUTC().Add(-time.Second)
		readErr = store.writeJSONQueryJobLocked(wrapper)
	}
	store.longHorizonMu.Unlock()
	if readErr != nil {
		t.Fatalf("expire lease fixture: %v", readErr)
	}
	if _, err := store.ApplyProfileMutation(ctx, record.ID, "holder-a", revision, jobs.ProfileMutation{Phase: &phase}); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("expired lease err=%v, want ErrLeaseLost", err)
	}
	if acquired, err := store.AcquireMonitorLease(ctx, record.ID, "holder-a", time.Minute); err != nil || !acquired {
		t.Fatalf("reacquire lease acquired=%v err=%v", acquired, err)
	}

	dispatched := true
	handle := jobs.NativeHandle{Provider: "bigquery", ID: "dry-run-1", Location: "EU"}
	first, err := store.ApplyProfileMutation(ctx, record.ID, "holder-a", revision, jobs.ProfileMutation{
		Dispatched: &dispatched, Handle: &handle, Phase: &phase,
	})
	if err != nil || first.NewRevision != revision+1 || first.CapacityReleased || first.CompletedAt != nil {
		t.Fatalf("first mutation=%#v err=%v", first, err)
	}

	estimated := int64(1 << 20)
	profile := jobs.QueryProfile{Valid: false, Error: "unknown column", EstimatedBytes: &estimated}
	outcome := jobs.ProfileOutcomeInvalid
	detail := "unknown column"
	failed := jobs.JobStatusFailed
	terminal, err := store.ApplyProfileMutation(ctx, record.ID, "holder-a", first.NewRevision, jobs.ProfileMutation{
		Profile: &profile, Outcome: &outcome, Error: &detail, TransitionTo: &failed,
		Wakes: []jobs.WakeSpec{
			{Kind: jobs.WakeTerminal, Detail: detail},
			{Kind: jobs.WakeTerminal, Detail: "duplicate kind must be ignored"},
		},
	})
	if err != nil || terminal.NewRevision != first.NewRevision+1 || terminal.CompletedAt == nil ||
		!terminal.CapacityReleased || len(terminal.AppendedWakes) != 1 || terminal.AppendedWakes[0].Kind != jobs.WakeTerminal {
		t.Fatalf("terminal mutation=%#v err=%v", terminal, err)
	}
	got, ok, err := store.GetQueryJob(ctx, record.ID)
	if err != nil || !ok || got.Status != jobs.JobStatusFailed || got.Profile == nil || got.ProfileOutcome != outcome ||
		got.Error != detail || got.CompletedAt == nil || got.Revision != terminal.NewRevision || len(got.WakeEvents) != 1 {
		t.Fatalf("terminal job=%#v ok=%v err=%v", got, ok, err)
	}
	capacity, found, err := store.readJSONCapacityRecordLocked(record.ID)
	if err != nil || !found || capacity.State != "released" || capacity.ReleasedAt == nil {
		t.Fatalf("terminal capacity=%#v found=%v err=%v", capacity, found, err)
	}
	active, err := store.ListActiveQueryJobs(ctx)
	if err != nil || len(active) != 0 {
		t.Fatalf("active after terminal=%#v err=%v", active, err)
	}
	if _, err := store.ApplyProfileMutation(ctx, record.ID, "holder-a", terminal.NewRevision, jobs.ProfileMutation{Phase: &phase}); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("terminal replay err=%v, want ErrRevisionConflict", err)
	}
}

func TestJSONProfilingNestedCapacityUsesJobStatusAndFreesOnlyNestedSlot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	now := time.Now().UTC()
	limits := jobs.CapacityLimits{
		MaxGlobal: 2, MaxPerThread: 2, MaxPerConnection: 2,
		MaxProfilingGlobal: 1, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 1,
	}
	first := profileTestJob(thread, run, "job-profile-first", "idem-profile-first", now)
	second := profileTestJob(thread, run, "job-profile-second", "idem-profile-second", now.Add(time.Millisecond))
	granted, revision, err := store.ReserveQueryCapacity(ctx, first, limits)
	if err != nil || !granted {
		t.Fatalf("reserve first granted=%v revision=%d err=%v", granted, revision, err)
	}
	if granted, _, err := store.ReserveQueryCapacity(ctx, second, limits); err != nil || granted {
		t.Fatalf("nested profiling limit granted=%v err=%v, want false nil", granted, err)
	}
	if acquired, err := store.AcquireMonitorLease(ctx, first.ID, "holder", time.Minute); err != nil || !acquired {
		t.Fatalf("acquire first lease=%v err=%v", acquired, err)
	}
	accepted := jobs.ProfileOutcomeAccepted
	reserved := jobs.JobStatusReserved
	mutation, err := store.ApplyProfileMutation(ctx, first.ID, "holder", revision, jobs.ProfileMutation{Outcome: &accepted, TransitionTo: &reserved})
	if err != nil || mutation.CapacityReleased {
		t.Fatalf("profiling->reserved mutation=%#v err=%v", mutation, err)
	}
	if granted, _, err := store.ReserveQueryCapacity(ctx, second, limits); err != nil || !granted {
		t.Fatalf("second profile after nested release granted=%v err=%v", granted, err)
	}
}

func TestJSONCapacityIgnoresStaleReservationSidecar(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	now := time.Now().UTC()
	first := testDurableJob(thread, run, now)
	first.ID, first.IdempotencyKey = "job-terminal-sidecar", "idem-terminal-sidecar"
	granted, revision, err := store.ReserveQueryCapacity(ctx, first, jobs.CapacityLimits{MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1})
	if err != nil || !granted {
		t.Fatalf("reserve first granted=%v err=%v", granted, err)
	}
	first.Status = jobs.JobStatusFailed
	if _, err := store.UpsertQueryJobCAS(ctx, first, revision); err != nil {
		t.Fatalf("terminalize first: %v", err)
	}
	store.longHorizonMu.Lock()
	capacity, found, readErr := store.readJSONCapacityRecordLocked(first.ID)
	if readErr == nil && found {
		capacity.State = "active"
		capacity.ReleasedAt = nil
		readErr = store.writeJSONCapacityRecordLocked(capacity)
	}
	store.longHorizonMu.Unlock()
	if readErr != nil || !found {
		t.Fatalf("seed stale capacity found=%v err=%v", found, readErr)
	}
	second := testDurableJob(thread, run, now.Add(time.Second))
	second.ID, second.IdempotencyKey = "job-after-stale-sidecar", "idem-after-stale-sidecar"
	if granted, _, err := store.ReserveQueryCapacity(ctx, second, jobs.CapacityLimits{MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1}); err != nil || !granted {
		t.Fatalf("authoritative status capacity granted=%v err=%v", granted, err)
	}
}

func TestJSONProfilingCapacitySerializesAcrossStoreInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	firstStore, thread, run := seedLongHorizonJSONRun(t, dir)
	secondStore, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("second NewJSON: %v", err)
	}
	now := time.Now().UTC()
	first := profileTestJob(thread, run, "job-profile-race-a", "idem-profile-race-a", now)
	second := profileTestJob(thread, run, "job-profile-race-b", "idem-profile-race-b", now)
	limits := jobs.CapacityLimits{
		MaxGlobal: 10, MaxPerThread: 10, MaxPerConnection: 10,
		MaxProfilingGlobal: 1, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 1,
	}
	start := make(chan struct{})
	type result struct {
		granted bool
		err     error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	reserve := func(store *JSONStore, record jobs.JobRecord) {
		defer wg.Done()
		<-start
		granted, _, err := store.ReserveQueryCapacity(ctx, record, limits)
		results <- result{granted: granted, err: err}
	}
	wg.Add(2)
	go reserve(firstStore, first)
	go reserve(secondStore, second)
	close(start)
	wg.Wait()
	close(results)
	grantedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent reserve: %v", result.err)
		}
		if result.granted {
			grantedCount++
		}
	}
	if grantedCount != 1 {
		t.Fatalf("profiling grants=%d, want exactly one", grantedCount)
	}
	active, err := firstStore.ListActiveQueryJobs(ctx)
	if err != nil || len(active) != 1 || active[0].Status != jobs.JobStatusProfiling {
		t.Fatalf("active profiling jobs=%#v err=%v", active, err)
	}
}
