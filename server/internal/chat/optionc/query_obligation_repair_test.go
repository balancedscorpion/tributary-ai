package optionc

import (
	"context"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
)

func TestQueryDependenciesRepairHalfCommittedReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	oldJob := jobs.JobRecord{
		ID: "qj-old", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-old"},
		ValidationState: jobs.ValidationRejected,
		SubmittedAt:     now, LastAccess: now, CompletedAt: &now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, oldJob, 0); err != nil {
		t.Fatalf("persist old job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: oldJob.ID, RequiredForCompletion: true, ValidationState: store.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("seed old dependency: %v", err)
	}
	// The successor launched durably (its record carries the replacement link)
	// but the process crashed before persistRunQueryDependency committed the
	// dependency swap: the half-committed state under repair.
	successor := jobs.JobRecord{
		ID: "qj-new", Provider: "bigquery", Status: jobs.JobStatusRunning,
		Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-new"},
		ReplacesJobID: oldJob.ID, ValidationState: jobs.ValidationUnvalidated,
		SubmittedAt: now, LastAccess: now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, successor, 0); err != nil {
		t.Fatalf("persist successor job: %v", err)
	}

	manager := &Manager{longHorizon: repository}
	refs := manager.queryDependenciesForRun(thread.ID, run.ID)
	if len(refs) != 1 || refs[0].ID != successor.ID {
		t.Fatalf("refs=%#v, want the repaired swap to leave only the successor obligation", refs)
	}

	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	wantObligation, obligationErr := store.QueryObligationIDForRoot(run.ID, oldJob.ID)
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}
	for _, dependency := range dependencies {
		if dependency.ObligationID != wantObligation {
			t.Fatalf("dependency %s obligation=%q, want %q", dependency.JobID, dependency.ObligationID, wantObligation)
		}
		switch dependency.JobID {
		case oldJob.ID:
			if !dependency.Detached || dependency.ReplacementJobID != successor.ID {
				t.Fatalf("old dependency after repair=%#v", dependency)
			}
		case successor.ID:
			if dependency.Detached || !dependency.RequiredForCompletion {
				t.Fatalf("successor dependency after repair=%#v", dependency)
			}
		}
	}

	// Idempotent: a second barrier evaluation neither errors nor double-counts.
	refs = manager.queryDependenciesForRun(thread.ID, run.ID)
	if len(refs) != 1 || refs[0].ID != successor.ID {
		t.Fatalf("second pass refs=%#v", refs)
	}
}

func TestQueryDependenciesRepairLeavesLiveOldAttemptAlone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	liveOld := jobs.JobRecord{
		ID: "qj-live", Provider: "bigquery", Status: jobs.JobStatusRunning,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-live"},
		ValidationState: jobs.ValidationUnvalidated,
		SubmittedAt:     now, LastAccess: now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, liveOld, 0); err != nil {
		t.Fatalf("persist live job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: liveOld.ID, RequiredForCompletion: true, ValidationState: store.QueryValidationUnvalidated,
	}); err != nil {
		t.Fatalf("seed live dependency: %v", err)
	}
	rogue := jobs.JobRecord{
		ID: "qj-rogue", Provider: "bigquery", Status: jobs.JobStatusRunning,
		Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-rogue"},
		ReplacesJobID: liveOld.ID, ValidationState: jobs.ValidationUnvalidated,
		SubmittedAt: now, LastAccess: now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, rogue, 0); err != nil {
		t.Fatalf("persist rogue successor: %v", err)
	}

	manager := &Manager{longHorizon: repository}
	refs := manager.queryDependenciesForRun(thread.ID, run.ID)
	// The live attempt is not replacement-eligible: the store swap refuses,
	// so BOTH jobs stay outstanding (fail closed) and the live row is intact.
	if len(refs) != 2 {
		t.Fatalf("refs=%#v, want fail-closed double-count while the old attempt is live", refs)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if dependencies[0].JobID != liveOld.ID || dependencies[0].Detached {
		t.Fatalf("live dependency mutated by repair: %#v", dependencies[0])
	}
}

func TestQueryDependenciesRepairDoesNotChooseAmongMultipleSuccessors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	old := jobs.JobRecord{
		ID: "qj-ambiguous-old", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-ambiguous-old"},
		ValidationState: jobs.ValidationRejected, SubmittedAt: now, LastAccess: now, CompletedAt: &now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, old, 0); err != nil {
		t.Fatalf("persist old job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: old.ID, RequiredForCompletion: true, ValidationState: store.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("seed old dependency: %v", err)
	}
	for _, id := range []string{"qj-ambiguous-a", "qj-ambiguous-b"} {
		record := jobs.JobRecord{
			ID: id, Provider: "bigquery", Status: jobs.JobStatusRunning,
			Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-" + id},
			ReplacesJobID: old.ID, ValidationState: jobs.ValidationUnvalidated,
			SubmittedAt: now, LastAccess: now, SubmitDispatched: true,
			Handle: jobs.NativeHandle{Provider: "bigquery", ID: "native-" + id},
		}
		if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
			t.Fatalf("persist successor %s: %v", id, err)
		}
	}

	manager := &Manager{longHorizon: repository}
	refs := manager.queryDependenciesForRun(thread.ID, run.ID)
	if len(refs) != 3 {
		t.Fatalf("refs=%#v, want predecessor and both ambiguous successors kept fail-closed", refs)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if dependencies[0].JobID != old.ID || dependencies[0].Detached || dependencies[0].ReplacementJobID != "" {
		t.Fatalf("ambiguous repair chose a winner or detached predecessor: %#v", dependencies[0])
	}
}

func TestExtractAsyncQueryJobIDIgnoresNativeProviderIDs(t *testing.T) {
	t.Parallel()
	nativeOnly := tools.Output{OK: true, ResultFormat: tools.ResultFormatJSON,
		QueryMetadata: map[string]any{"statement_id": "native-stmt-1"},
		Result: map[string]any{
			"statement_id": "native-stmt-1",
			"statementId":  "native-stmt-1",
			"job":          map[string]any{"statement_id": "native-stmt-1"},
		},
	}
	if id := extractAsyncQueryJobID(nativeOnly); id != "" {
		t.Fatalf("extractAsyncQueryJobID=%q, native provider ids must never register a dependency", id)
	}
	managed := tools.Output{OK: true, ResultFormat: tools.ResultFormatJSON,
		QueryMetadata: map[string]any{"job_id": "qj-app-1", "statement_id": "native-stmt-1"},
	}
	if id := extractAsyncQueryJobID(managed); id != "qj-app-1" {
		t.Fatalf("extractAsyncQueryJobID=%q, want app-managed job id", id)
	}
}
