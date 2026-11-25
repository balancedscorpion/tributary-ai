package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/jsondb"
)

func seedLongHorizonJSONRun(t *testing.T, dir string) (*JSONStore, Thread, Run) {
	t.Helper()
	store, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := store.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := store.CreateRun(context.Background(), thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return store, thread, run
}

func testDurableJob(thread Thread, run Run, now time.Time) jobs.JobRecord {
	return jobs.JobRecord{
		ID:                   "job_durable_1",
		Provider:             "bigquery",
		ConnectionID:         "bq-connection",
		SQL:                  "SELECT 1",
		SQLPreview:           "SELECT 1",
		Status:               jobs.JobStatusReserved,
		MonitorHealth:        jobs.MonitorHealthOK,
		PolicyHealth:         jobs.PolicyHealthOK,
		MaterializationState: jobs.MaterializationNone,
		Owner: jobs.JobOwner{
			ThreadID: thread.ID,
			RunID:    run.ID,
			Handle:   "owner_handle",
		},
		IdempotencyKey:      "idem_durable_1",
		KeepOnRunCompletion: true,
		SubmittedAt:         now,
		LastAccess:          now,
	}
}

func TestJSONLongHorizonQueryJobCASLeaseWakeAndReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	record := testDurableJob(thread, run, now)

	granted, revision, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{
		MaxGlobal: 8, MaxPerThread: 3, MaxPerConnection: 2,
	})
	if err != nil || !granted || revision != 1 {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v, want true 1 nil", granted, revision, err)
	}
	duplicate := record
	duplicate.ID = "job_durable_duplicate"
	if _, _, err := store.ReserveQueryCapacity(ctx, duplicate, jobs.CapacityLimits{}); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("duplicate canonical idempotency identity err=%v, want ErrRevisionConflict", err)
	}
	record.Status = jobs.JobStatusSubmitted
	record.Handle = jobs.NativeHandle{Provider: "bigquery", ID: "native-job-1", Location: "EU"}
	record.WakeEvents = []jobs.WakeEvent{{At: now.Add(time.Minute), Kind: jobs.WakeSoftQueue, Detail: "diagnostic only"}}
	revision, err = store.UpsertQueryJobCAS(ctx, record, 1)
	if err != nil || revision != 2 {
		t.Fatalf("UpsertQueryJobCAS revision=%d err=%v, want 2 nil", revision, err)
	}
	if _, err := store.UpsertQueryJobCAS(ctx, record, 1); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("stale CAS err=%v, want ErrRevisionConflict", err)
	}
	acquired, err := store.AcquireMonitorLease(ctx, record.ID, "process-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("AcquireMonitorLease acquired=%v err=%v", acquired, err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen NewJSON: %v", err)
	}
	got, ok, err := reopened.GetQueryJob(ctx, record.ID)
	if err != nil || !ok {
		t.Fatalf("GetQueryJob after reopen ok=%v err=%v", ok, err)
	}
	if got.Revision != 2 || got.Status != jobs.JobStatusSubmitted || got.Handle.ID != "native-job-1" || len(got.WakeEvents) != 1 {
		t.Fatalf("reopened query job=%#v", got)
	}
	if acquired, err := reopened.AcquireMonitorLease(ctx, record.ID, "process-b", time.Minute); err != nil || acquired {
		t.Fatalf("second holder acquired live lease=%v err=%v, want false nil", acquired, err)
	}
	claimed, claimRevision, err := reopened.ClaimWakeEvents(ctx, record.ID, "continuation-a")
	if err != nil || len(claimed) != 1 || !claimed[0].Claimed || claimRevision != 3 {
		t.Fatalf("ClaimWakeEvents=%#v revision=%d err=%v", claimed, claimRevision, err)
	}
	claimedAgain, secondClaimRevision, err := reopened.ClaimWakeEvents(ctx, record.ID, "continuation-b")
	if err != nil || len(claimedAgain) != 0 || secondClaimRevision != 3 {
		t.Fatalf("second ClaimWakeEvents=%#v revision=%d err=%v, want empty at revision 3", claimedAgain, secondClaimRevision, err)
	}
	afterClaim, ok, err := reopened.GetQueryJob(ctx, record.ID)
	if err != nil || !ok || afterClaim.Revision != 3 {
		t.Fatalf("wake claim revision was not reported/persisted: job=%#v ok=%v err=%v", afterClaim, ok, err)
	}
	afterClaim.Status = jobs.JobStatusRunning
	if revision, err := reopened.UpsertQueryJobCAS(ctx, afterClaim, 3); err != nil || revision != 4 {
		t.Fatalf("lifecycle CAS after wake claim revision=%d err=%v, want 4 nil", revision, err)
	}
}

func TestJSONRunContinuationCheckpointDependencyAndWakeSurviveReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 13, 0, 0, 0, time.UTC)
	job := testDurableJob(thread, run, now)
	if granted, _, err := store.ReserveQueryCapacity(ctx, job, jobs.CapacityLimits{}); err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v err=%v", granted, err)
	}
	dependency, err := store.UpsertRunQueryDependency(ctx, RunQueryDependency{
		RunID: run.ID, JobID: job.ID, RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
	})
	if err != nil {
		t.Fatalf("UpsertRunQueryDependency: %v", err)
	}
	history := json.RawMessage(`[{"role":"assistant","content":"waiting"}]`)
	executionSnapshot := json.RawMessage(`{"system_prompt":"system","dispatch_ordinal":2,"tools":["query_status"]}`)
	checkpoint, err := store.AppendContinuationCheckpoint(ctx, ContinuationCheckpoint{
		RunID: run.ID, ThreadID: thread.ID, ToolBatchSequence: 1,
		HistoryJSON: history, ProviderStateJSON: json.RawMessage(`{"provider":"anthropic"}`),
		ExecutionSnapshotJSON: executionSnapshot,
		DependencyIDs:         []string{job.ID, job.ID},
	})
	if err != nil {
		t.Fatalf("AppendContinuationCheckpoint: %v", err)
	}
	if checkpoint.Sequence != 1 || len(checkpoint.DependencyIDs) != 1 {
		t.Fatalf("checkpoint=%#v", checkpoint)
	}
	wakeAt := now.Add(-time.Second)
	waiting, swapped, err := store.TransitionRunCAS(ctx, RunStateMutation{
		RunID: run.ID, ExpectedRevision: 1, ExpectedStatuses: []string{RunStatusQueued},
		Status: RunStatusWaitingExternal, WakeAt: &wakeAt,
	})
	if err != nil || !swapped || waiting.Revision != 2 {
		t.Fatalf("TransitionRunCAS run=%#v swapped=%v err=%v", waiting, swapped, err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen NewJSON: %v", err)
	}
	latest, err := reopened.LatestContinuationCheckpoint(ctx, run.ID)
	if err != nil || latest == nil || latest.Sequence != 1 || !jsonSemanticallyEqual(latest.HistoryJSON, history) ||
		!jsonSemanticallyEqual(latest.ExecutionSnapshotJSON, executionSnapshot) {
		t.Fatalf("LatestContinuationCheckpoint=%#v err=%v", latest, err)
	}
	dependencies, err := reopened.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != dependency.JobID {
		t.Fatalf("ListRunQueryDependencies=%#v err=%v", dependencies, err)
	}
	recoverable, err := reopened.ListRecoverableRuns(ctx, now, 10)
	if err != nil || len(recoverable) != 1 || recoverable[0].ID != run.ID {
		t.Fatalf("ListRecoverableRuns=%#v err=%v", recoverable, err)
	}
	resuming, claimed, err := reopened.ClaimRunWake(ctx, run.ID, 2, "resume-a", now, now.Add(time.Minute))
	if err != nil || !claimed || resuming.Status != RunStatusResuming || resuming.ContinuationCount != 1 || resuming.Revision != 3 {
		t.Fatalf("ClaimRunWake run=%#v claimed=%v err=%v", resuming, claimed, err)
	}
	reclaimedAt := now.Add(2 * time.Minute)
	reclaimed, claimed, err := reopened.ClaimRunWake(ctx, run.ID, 3, "resume-b", reclaimedAt, reclaimedAt.Add(time.Minute))
	if err != nil || !claimed || reclaimed.Status != RunStatusResuming || reclaimed.ContinuationCount != 1 || reclaimed.Revision != 4 || reclaimed.ResumeLeaseHolder != "resume-b" {
		t.Fatalf("expired resuming lease reclaim run=%#v claimed=%v err=%v", reclaimed, claimed, err)
	}
	if err := reopened.DetachRunQueryDependencies(ctx, run.ID, now); err != nil {
		t.Fatalf("DetachRunQueryDependencies: %v", err)
	}
	dependencies, err = reopened.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || !dependencies[0].Detached {
		t.Fatalf("detached dependencies=%#v err=%v", dependencies, err)
	}
}

func TestJSONDrainJournalRecoversChunkCursorEventAndRevisionOnReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 14, 0, 0, 0, time.UTC)
	job := testDurableJob(thread, run, now)
	job.ID = "job_trino_recovery"
	job.Provider = "trino"
	job.ConnectionID = "trino-connection"
	job.Status = jobs.JobStatusRunning
	job.Handle = jobs.NativeHandle{Provider: "trino", ID: "query-1", Extra: map[string]string{"next_uri": "https://trino/1"}}
	granted, revision, err := store.ReserveQueryCapacity(ctx, job, jobs.CapacityLimits{})
	if err != nil || !granted || revision != 1 {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	validationRevision, err := store.UpdateQueryJobValidation(ctx, job.ID, jobs.ValidationValidating, "profile accepted", revision)
	if err != nil || validationRevision != revision+1 {
		t.Fatalf("UpdateQueryJobValidation revision=%d err=%v, want %d", validationRevision, err, revision+1)
	}
	job.Revision = validationRevision
	job.ValidationState = jobs.ValidationValidating
	job.ValidationDetail = "profile accepted"
	job.DrainSeq = 1
	nextHandle := jobs.NativeHandle{Provider: "trino", ID: "query-1", Extra: map[string]string{"next_uri": "https://trino/2"}}
	page := jobs.ResultPage{
		Rows: []map[string]any{{"value": 1}}, Bytes: 12, CursorAfter: &nextHandle,
	}
	job.DrainRowsCommitted = int64(len(page.Rows))
	job.DrainBytesCommitted = page.Bytes
	job.DrainTruncated = page.Truncated
	commit := jobs.DrainCommit{
		JobID: job.ID, Owner: job.Owner, Seq: 1, Page: page, Handle: nextHandle,
		DrainRowsCommitted: job.DrainRowsCommitted, DrainBytesCommitted: job.DrainBytesCommitted,
		DrainTruncated: job.DrainTruncated, Record: job,
	}
	chunk := QueryResultChunk{
		JobID: job.ID, Sequence: 1, Page: page, CursorAfter: nextHandle,
		RowCount: 1, ByteCount: 12, CreatedAt: now,
	}
	payload, _ := json.Marshal(map[string]any{"drain_sequence": 1})
	event := QueryJobEvent{
		JobID: job.ID, Type: "drain_observation", ObservationKey: "drain:1", Payload: payload, CreatedAt: now,
	}
	transaction := jsonQueryTransactionRecord{
		Kind: queryTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: drainTransactionID(job.ID, 1), Commit: commit, Chunk: chunk, Event: event, PreparedAt: now,
	}
	transactionPath, err := queryTransactionPath(dir, transaction.ID)
	if err != nil {
		t.Fatalf("queryTransactionPath: %v", err)
	}
	if err := jsondb.WriteJSONFile(transactionPath, transaction); err != nil {
		t.Fatalf("write prepared drain journal: %v", err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen with prepared drain journal: %v", err)
	}
	got, ok, err := reopened.GetQueryJob(ctx, job.ID)
	if err != nil || !ok {
		t.Fatalf("GetQueryJob after journal recovery ok=%v err=%v", ok, err)
	}
	if got.Revision != 3 || got.DrainSeq != 1 || got.DrainRowsCommitted != 1 || got.DrainBytesCommitted != 12 ||
		got.DrainTruncated || got.Handle.Extra["next_uri"] != "https://trino/2" {
		t.Fatalf("recovered job=%#v", got)
	}
	chunks, err := reopened.ListQueryResultChunks(ctx, job.ID, 0, 10)
	if err != nil || len(chunks) != 1 || chunks[0].RowCount != 1 {
		t.Fatalf("recovered chunks=%#v err=%v", chunks, err)
	}
	events, err := reopened.ListQueryJobEvents(ctx, job.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].ObservationKey != "drain:1" {
		t.Fatalf("recovered events=%#v err=%v", events, err)
	}
	if _, err := os.Stat(transactionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drain journal still exists after recovery: %v", err)
	}
}

func TestJSONLiveCASRecoversPeerDrainJournalBeforeMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	first, thread, run := seedLongHorizonJSONRun(t, dir)
	second, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("open peer JSON store: %v", err)
	}
	now := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	job := testDurableJob(thread, run, now)
	job.ID = "job_trino_live_recovery"
	job.Provider = "trino"
	job.ConnectionID = "trino-connection"
	job.Status = jobs.JobStatusRunning
	job.Handle = jobs.NativeHandle{Provider: "trino", ID: "query-live", Extra: map[string]string{"next_uri": "https://trino/1"}}
	granted, revision, err := first.ReserveQueryCapacity(ctx, job, jobs.CapacityLimits{})
	if err != nil || !granted || revision != 1 {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}

	job.Revision = revision
	job.DrainSeq = 1
	job.DrainRowsCommitted = 1
	job.DrainBytesCommitted = 12
	nextHandle := jobs.NativeHandle{Provider: "trino", ID: "query-live", Extra: map[string]string{"next_uri": "https://trino/2"}}
	page := jobs.ResultPage{Rows: []map[string]any{{"value": 1}}, Bytes: 12, CursorAfter: &nextHandle}
	commit := jobs.DrainCommit{
		JobID: job.ID, Owner: job.Owner, Seq: 1, Page: page, Handle: nextHandle,
		DrainRowsCommitted: 1, DrainBytesCommitted: 12, Record: job,
	}
	payload, _ := json.Marshal(map[string]any{"drain_sequence": 1})
	transaction := jsonQueryTransactionRecord{
		Kind: queryTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: drainTransactionID(job.ID, 1), Commit: commit,
		Chunk: QueryResultChunk{
			JobID: job.ID, Sequence: 1, Page: page, CursorAfter: nextHandle,
			RowCount: 1, ByteCount: 12, CreatedAt: now,
		},
		Event: QueryJobEvent{
			JobID: job.ID, Type: "drain_observation", ObservationKey: "drain:1",
			Payload: payload, CreatedAt: now,
		},
		PreparedAt: now,
	}
	transactionPath, err := queryTransactionPath(dir, transaction.ID)
	if err != nil {
		t.Fatalf("queryTransactionPath: %v", err)
	}
	// Simulate process A stopping immediately after the journal's durable
	// rename, before it can apply the chunk/cursor/event or unlink the intent.
	first.longHorizonMu.Lock()
	err = jsondb.WriteJSONFile(transactionPath, transaction)
	first.longHorizonMu.Unlock()
	if err != nil {
		t.Fatalf("write peer drain journal: %v", err)
	}

	staleCancellation := job
	staleCancellation.DrainSeq = 0
	staleCancellation.DrainRowsCommitted = 0
	staleCancellation.DrainBytesCommitted = 0
	staleCancellation.Status = jobs.JobStatusCancelRequested
	if _, err := second.UpsertQueryJobCAS(ctx, staleCancellation, revision); !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("stale cancellation CAS err=%v, want ErrRevisionConflict after recovery", err)
	}
	durable, ok, err := second.GetQueryJob(ctx, job.ID)
	if err != nil || !ok || durable.Revision != revision+1 || durable.DrainSeq != 1 ||
		durable.DrainRowsCommitted != 1 || durable.DrainBytesCommitted != 12 || durable.Handle.Extra["next_uri"] != "https://trino/2" {
		t.Fatalf("recovered peer drain job=%#v ok=%v err=%v", durable, ok, err)
	}
	if _, err := os.Stat(transactionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("peer drain journal still exists after live recovery: %v", err)
	}

	// The caller can rebase its cancellation onto the recovered revision; the
	// store is not left permanently revision-bricked by the crashed peer.
	durable.Status = jobs.JobStatusCancelRequested
	newRevision, err := second.UpsertQueryJobCAS(ctx, durable, durable.Revision)
	if err != nil || newRevision != revision+2 {
		t.Fatalf("rebased cancellation revision=%d err=%v", newRevision, err)
	}
	final, ok, err := first.GetQueryJob(ctx, job.ID)
	if err != nil || !ok || final.Status != jobs.JobStatusCancelRequested || final.Revision != revision+2 ||
		final.DrainSeq != 1 || final.Handle.Extra["next_uri"] != "https://trino/2" {
		t.Fatalf("final recovered/cancelled job=%#v ok=%v err=%v", final, ok, err)
	}
}

func TestJSONRecoveryDiscardsOnlyEntirelyUnappliedObsoleteDrainJournal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 15, 30, 0, 0, time.UTC)
	job := testDurableJob(thread, run, now)
	job.ID = "job_trino_obsolete_drain"
	job.Provider = "trino"
	job.ConnectionID = "trino-connection"
	job.Status = jobs.JobStatusRunning
	job.Handle = jobs.NativeHandle{Provider: "trino", ID: "query-obsolete", Extra: map[string]string{"next_uri": "https://trino/1"}}
	granted, revision, err := store.ReserveQueryCapacity(ctx, job, jobs.CapacityLimits{})
	if err != nil || !granted || revision != 1 {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}

	job.Revision = revision
	job.DrainSeq = 1
	job.DrainRowsCommitted = 1
	job.DrainBytesCommitted = 8
	nextHandle := jobs.NativeHandle{Provider: "trino", ID: "query-obsolete", Extra: map[string]string{"next_uri": "https://trino/2"}}
	page := jobs.ResultPage{Rows: []map[string]any{{"value": 1}}, Bytes: 8, CursorAfter: &nextHandle}
	payload, _ := json.Marshal(map[string]any{"drain_sequence": 1})
	transaction := jsonQueryTransactionRecord{
		Kind: queryTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: drainTransactionID(job.ID, 1),
		Commit: jobs.DrainCommit{
			JobID: job.ID, Owner: job.Owner, Seq: 1, Page: page, Handle: nextHandle,
			DrainRowsCommitted: 1, DrainBytesCommitted: 8, Record: job,
		},
		Chunk: QueryResultChunk{
			JobID: job.ID, Sequence: 1, Page: page, CursorAfter: nextHandle,
			RowCount: 1, ByteCount: 8, CreatedAt: now,
		},
		Event: QueryJobEvent{
			JobID: job.ID, Type: "drain_observation", ObservationKey: "drain:1",
			Payload: payload, CreatedAt: now,
		},
		PreparedAt: now,
	}
	transactionPath, err := queryTransactionPath(dir, transaction.ID)
	if err != nil {
		t.Fatalf("queryTransactionPath: %v", err)
	}

	// Reproduce a legacy writer that advanced the lifecycle without first
	// recovering the already-durable drain intent. Because no drain effect was
	// applied, recovery can safely retain the newer record and unlink the stale
	// intent instead of bricking every subsequent store open.
	store.longHorizonMu.Lock()
	err = jsondb.WriteJSONFile(transactionPath, transaction)
	current, readErr := store.readJSONQueryJobLocked(job.ID, false)
	if err == nil {
		err = readErr
	}
	if err == nil {
		current.Job.Status = jobs.JobStatusCancelRequested
		current.Job.Revision++
		current.UpdatedAt = now.Add(time.Second)
		err = store.writeJSONQueryJobLocked(current)
	}
	store.longHorizonMu.Unlock()
	if err != nil {
		t.Fatalf("seed obsolete drain journal: %v", err)
	}
	if err := store.recoverQueryTransactions(); err != nil {
		t.Fatalf("recover obsolete unapplied drain journal: %v", err)
	}
	durable, ok, err := store.GetQueryJob(ctx, job.ID)
	if err != nil || !ok || durable.Revision != revision+1 || durable.Status != jobs.JobStatusCancelRequested ||
		durable.DrainSeq != 0 || durable.Handle.Extra["next_uri"] != "https://trino/1" {
		t.Fatalf("durable job after obsolete intent=%#v ok=%v err=%v", durable, ok, err)
	}
	chunks, err := store.ListQueryResultChunks(ctx, job.ID, 0, 10)
	if err != nil || len(chunks) != 0 {
		t.Fatalf("obsolete intent chunks=%#v err=%v", chunks, err)
	}
	if _, err := os.Stat(transactionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("obsolete drain journal still exists: %v", err)
	}

	// The same lifecycle shape is not safe to discard once any drain effect
	// exists. Keep the journal and surface the inconsistency for repair rather
	// than hiding an orphaned chunk behind the older durable cursor.
	store.longHorizonMu.Lock()
	err = jsondb.WriteJSONFile(transactionPath, transaction)
	if err == nil {
		err = store.appendJSONQueryResultChunkLocked(transaction.Chunk)
	}
	store.longHorizonMu.Unlock()
	if err != nil {
		t.Fatalf("seed partially applied obsolete drain: %v", err)
	}
	if err := store.recoverQueryTransactions(); !errors.Is(err, ErrConflict) {
		t.Fatalf("recover partially applied obsolete drain err=%v, want ErrConflict", err)
	}
	if _, err := os.Stat(transactionPath); err != nil {
		t.Fatalf("unsafe drain journal was discarded: %v", err)
	}
}

func TestJSONLongHorizonCASSerializesAcrossStoreInstances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	first, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC)
	record := testDurableJob(thread, run, now)
	if granted, revision, err := first.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{}); err != nil || !granted || revision != 1 {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	second, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("second NewJSON: %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	update := func(store *JSONStore, status jobs.JobStatus) {
		candidate := record
		candidate.Status = status
		<-start
		_, err := store.UpsertQueryJobCAS(ctx, candidate, 1)
		results <- err
	}
	go update(first, jobs.JobStatusSubmitted)
	go update(second, jobs.JobStatusRunning)
	close(start)

	successes, conflicts := 0, 0
	for i := 0; i < 2; i++ {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, jobs.ErrRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent CAS err=%v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("cross-instance CAS successes=%d conflicts=%d, want 1/1", successes, conflicts)
	}
}

func TestJSONPreparedWakeClaimJournalRollsBackPartialMutationOnReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 19, 0, 0, 0, time.UTC)
	record := testDurableJob(thread, run, now)
	granted, revision, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v err=%v", granted, err)
	}
	record.WakeEvents = []jobs.WakeEvent{{At: now, Kind: jobs.WakeSoftExecution, Detail: "inspect"}}
	record.Status = jobs.JobStatusSubmitted
	if revision, err = store.UpsertQueryJobCAS(ctx, record, revision); err != nil {
		t.Fatalf("UpsertQueryJobCAS: %v", err)
	}

	store.longHorizonMu.Lock()
	beforeJob, err := store.readJSONQueryJobLocked(record.ID, false)
	if err != nil {
		store.longHorizonMu.Unlock()
		t.Fatalf("read job: %v", err)
	}
	events, err := store.listJSONQueryJobEventsLocked(record.ID, 0, 0)
	if err != nil || len(events) != 1 {
		store.longHorizonMu.Unlock()
		t.Fatalf("wake events=%#v err=%v", events, err)
	}
	beforeEvent := jsonQueryJobEventRecord{Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: events[0]}
	afterEvent := beforeEvent
	afterEvent.ClaimedBy = "claimant"
	afterEvent.ClaimedAt = &now
	afterJob := beforeJob
	afterJob.Job.Revision++
	afterJob.Job.LastAccess = now
	afterJob.UpdatedAt = now
	transaction := jsonQueryMutationTransactionRecord{
		Kind: queryMutationTransactionKind, SchemaVersion: longHorizonSchemaVersion,
		ID: record.ID + "-wake-partial", Operation: "wake_claim", State: "prepared",
		BeforeJob: beforeJob, AfterJob: afterJob,
		BeforeEvents: []jsonQueryJobEventRecord{beforeEvent}, AfterEvents: []jsonQueryJobEventRecord{afterEvent}, PreparedAt: now,
	}
	transactionPath, err := queryTransactionPath(dir, transaction.ID)
	if err == nil {
		err = jsondb.WriteJSONFile(transactionPath, transaction)
	}
	if err == nil {
		err = store.writeJSONQueryJobEventLocked(afterEvent.QueryJobEvent)
	}
	if err == nil {
		err = store.writeJSONQueryJobLocked(afterJob)
	}
	store.longHorizonMu.Unlock()
	if err != nil {
		t.Fatalf("seed partial wake claim: %v", err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok, err := reopened.GetQueryJob(ctx, record.ID)
	if err != nil || !ok || got.Revision != revision || len(got.WakeEvents) != 1 || got.WakeEvents[0].Claimed {
		t.Fatalf("rolled-back wake job=%#v ok=%v err=%v", got, ok, err)
	}
}

func TestJSONCommittedValidationJournalCompletesOnReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	store, thread, run := seedLongHorizonJSONRun(t, dir)
	now := time.Date(2026, 7, 12, 20, 0, 0, 0, time.UTC)
	record := testDurableJob(thread, run, now)
	granted, revision, err := store.ReserveQueryCapacity(ctx, record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v err=%v", granted, err)
	}
	store.longHorizonMu.Lock()
	beforeJob, err := store.readJSONQueryJobLocked(record.ID, false)
	if err != nil {
		store.longHorizonMu.Unlock()
		t.Fatalf("read job: %v", err)
	}
	afterJob := beforeJob
	afterJob.Job.Revision++
	afterJob.Job.ValidationState = jobs.ValidationValidated
	afterJob.Job.ValidationDetail = "row totals reconciled"
	afterJob.ValidationState = string(jobs.ValidationValidated)
	afterJob.ValidationDetail = "row totals reconciled"
	afterJob.UpdatedAt = now
	payload, _ := json.Marshal(map[string]any{"state": jobs.ValidationValidated, "lifecycle_revision": revision + 1})
	event := QueryJobEvent{
		JobID: record.ID, Sequence: 1, Type: "validation", ObservationKey: validationObservationKey(record.ID, string(jobs.ValidationValidated), "row totals reconciled", revision+1),
		Payload: payload, CreatedAt: now,
	}
	transaction := jsonQueryMutationTransactionRecord{
		Kind: queryMutationTransactionKind, SchemaVersion: longHorizonSchemaVersion,
		ID: record.ID + "-validation-committed", Operation: "validation", State: "committed",
		BeforeJob: beforeJob, AfterJob: afterJob,
		AfterEvents:          []jsonQueryJobEventRecord{{Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion, QueryJobEvent: event}},
		DeleteEventSequences: []int64{1}, PreparedAt: now,
	}
	transactionPath, err := queryTransactionPath(dir, transaction.ID)
	if err == nil {
		err = jsondb.WriteJSONFile(transactionPath, transaction)
	}
	store.longHorizonMu.Unlock()
	if err != nil {
		t.Fatalf("write committed validation journal: %v", err)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	got, ok, err := reopened.GetQueryJob(ctx, record.ID)
	if err != nil || !ok || got.Revision != revision+1 || got.ValidationState != jobs.ValidationValidated {
		t.Fatalf("completed validation job=%#v ok=%v err=%v", got, ok, err)
	}
	events, err := reopened.ListQueryJobEvents(ctx, record.ID, 0, 10)
	if err != nil || len(events) != 1 || events[0].Type != "validation" {
		t.Fatalf("validation events=%#v err=%v", events, err)
	}
}
