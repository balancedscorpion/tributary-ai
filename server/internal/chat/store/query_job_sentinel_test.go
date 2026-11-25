package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/jsondb"
)

func managedSentinelTestJob(id, replaces string) jobs.JobRecord {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	return jobs.JobRecord{
		ID: id, Provider: "databricks", ConnectionID: "warehouse-1",
		Status: jobs.JobStatusRunning,
		Owner: jobs.JobOwner{
			ThreadID: "thread-1", RunID: "run-1", Handle: "owner-1",
		},
		ReplacesJobID: replaces,
		Metadata: map[string]any{
			jobs.QueryObligationManagedMetadataKey: true,
		},
		SubmittedAt: now, LastAccess: now,
	}
}

func TestQueryJobWritesRejectSentinelIdentities(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name   string
		mutate func(*jobs.JobRecord)
	}{
		{name: "nil replacement", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "nil" }},
		{name: "zero replacement", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "00000-00000" }},
		{name: "managed zero replacement", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "qj-00000-00000" }},
		{name: "sentinel primary", mutate: func(record *jobs.JobRecord) { record.ID = "nil" }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			record := managedSentinelTestJob("qj-dbx-current", "qj-dbx-previous")
			testCase.mutate(&record)
			if err := validateQueryJobRecord(record); err == nil {
				t.Fatal("validateQueryJobRecord succeeded, want identity error")
			}
		})
	}

	legacy := managedSentinelTestJob("job-legacy-generic", "job-legacy-parent")
	if err := validateQueryJobRecord(legacy); err != nil {
		t.Fatalf("marked legacy generic job identity should remain compatible: %v", err)
	}
}

func TestJSONLegacySentinelReplacementRemainsMonitorable(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []string{"nil", "00000-00000", "qj-0000_0000"} {
		t.Run(sentinel, func(t *testing.T) {
			repository, err := NewJSON(t.TempDir())
			if err != nil {
				t.Fatalf("NewJSON: %v", err)
			}
			record := managedSentinelTestJob("qj-dbx-running", sentinel)
			if err := repository.writeJSONQueryJobLocked(jsonQueryJobRecord{
				Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
				ConnectionKey: queryConnectionKey(record), Job: record,
			}); err != nil {
				t.Fatalf("write legacy row: %v", err)
			}

			stored, ok, err := repository.GetQueryJob(context.Background(), record.ID)
			if err != nil || !ok {
				t.Fatalf("GetQueryJob ok=%v err=%v", ok, err)
			}
			if stored.ReplacesJobID != "" {
				t.Fatalf("sanitized replaces_job_id=%q, want empty", stored.ReplacesJobID)
			}
			active, err := repository.ListActiveQueryJobs(context.Background())
			if err != nil || len(active) != 1 || active[0].ID != record.ID {
				t.Fatalf("ListActiveQueryJobs=%#v err=%v", active, err)
			}
			derived, err := DeriveQueryObligationID(context.Background(), repository, record.Owner.RunID, record.ID)
			want, wantErr := QueryObligationIDForRoot(record.Owner.RunID, record.ID)
			if err != nil || wantErr != nil || derived != want {
				t.Fatalf("derived=%q err=%v, want=%q wantErr=%v", derived, err, want, wantErr)
			}
		})
	}
}

func TestJSONQuarantinedPrimaryDoesNotBlockHealthyRecovery(t *testing.T) {
	t.Parallel()
	repository, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	bad := managedSentinelTestJob("qj-_malformed", "")
	good := managedSentinelTestJob("qj-dbx-healthy", "")
	for _, record := range []jobs.JobRecord{bad, good} {
		if err := repository.writeJSONQueryJobLocked(jsonQueryJobRecord{
			Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
			ConnectionKey: queryConnectionKey(record), Job: record,
		}); err != nil {
			t.Fatalf("write %s: %v", record.ID, err)
		}
	}
	active, err := repository.ListActiveQueryJobs(context.Background())
	if err != nil || len(active) != 2 {
		t.Fatalf("ListActiveQueryJobs=%#v err=%v, want repaired and healthy jobs", active, err)
	}
	repairedID := repairedManagedQueryJobID(bad)
	found := map[string]bool{}
	for _, record := range active {
		found[record.ID] = true
		if !jobs.IsValidManagedQueryJobID(record.ID) {
			t.Fatalf("active recovery exposed invalid identity %q", record.ID)
		}
	}
	if !found[repairedID] || !found[good.ID] {
		t.Fatalf("active identities=%#v, want repaired=%s healthy=%s", found, repairedID, good.ID)
	}
	if _, ok, err := repository.GetQueryJob(context.Background(), "nil"); err != nil || ok {
		t.Fatalf("sentinel GetQueryJob ok=%v err=%v, want absent", ok, err)
	}
}

func TestJSONPrimaryIdentityRepairPreservesDurableQueryState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, thread, run := seedLongHorizonJSONRun(t, cfgDir)
	now := time.Date(2026, 7, 13, 12, 30, 0, 0, time.UTC)
	oldID := "00000-00000"
	corrupt := managedSentinelTestJob(oldID, "")
	corrupt.Owner.ThreadID = thread.ID
	corrupt.Owner.RunID = run.ID
	corrupt.SubmittedAt = now
	corrupt.LastAccess = now
	if err := repository.writeJSONQueryJobLocked(jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(corrupt), Job: corrupt,
	}); err != nil {
		t.Fatal(err)
	}
	child := managedSentinelTestJob("qj-dbx-child", oldID)
	child.Owner.ThreadID = thread.ID
	child.Owner.RunID = run.ID
	child.Status = jobs.JobStatusCompleted
	child.SubmittedAt = now.Add(time.Second)
	child.LastAccess = child.SubmittedAt
	if err := repository.writeJSONQueryJobLocked(jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(child), Job: child,
	}); err != nil {
		t.Fatal(err)
	}
	obligationID, err := QueryObligationIDForRoot(run.ID, oldID)
	if err != nil {
		t.Fatal(err)
	}
	dependencyPath, err := runQueryDependencyPath(cfgDir, run.ID, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondb.WriteJSONFile(dependencyPath, jsonRunQueryDependencyRecord{
		Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
		RunQueryDependency: RunQueryDependency{
			RunID: run.ID, JobID: oldID, RequiredForCompletion: true,
			ValidationState: QueryValidationUnvalidated, ObligationID: obligationID,
			CreatedAt: now, UpdatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	eventPath, _ := queryJobEventPath(cfgDir, oldID, 1)
	if err := jsondb.WriteJSONFile(eventPath, jsonQueryJobEventRecord{
		Kind: queryJobEventRecordKind, SchemaVersion: longHorizonSchemaVersion,
		QueryJobEvent: QueryJobEvent{
			JobID: oldID, Sequence: 1, Type: "wake", WakeKind: jobs.WakePathology,
			ObservationKey: "identity-repair-event", CreatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	chunkPath, _ := queryResultChunkPath(cfgDir, oldID, 1)
	if err := jsondb.WriteJSONFile(chunkPath, jsonQueryResultChunkRecord{
		Kind: queryResultChunkRecordKind, SchemaVersion: longHorizonSchemaVersion,
		QueryResultChunk: QueryResultChunk{
			JobID: oldID, Sequence: 1, Page: jobs.ResultPage{Rows: []map[string]any{{"value": 1}}},
			RowCount: 1, CreatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	capacityPath, _ := queryCapacityPath(cfgDir, oldID)
	if err := jsondb.WriteJSONFile(capacityPath, jsonQueryCapacityRecord{
		Kind: queryCapacityRecordKind, SchemaVersion: longHorizonSchemaVersion,
		JobID: oldID, ThreadID: thread.ID, ConnectionKey: queryConnectionKey(corrupt),
		State: "active", ReservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	checkpointPath, _ := continuationCheckpointPath(cfgDir, run.ID, 1)
	if err := jsondb.WriteJSONFile(checkpointPath, jsonContinuationCheckpointRecord{
		Kind: continuationCheckpointRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ContinuationCheckpoint: ContinuationCheckpoint{
			ID: "checkpoint-identity-repair", RunID: run.ID, ThreadID: thread.ID, Sequence: 1,
			HistoryJSON: json.RawMessage(`[]`), DependencyIDs: []string{oldID}, CreatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}

	active, err := repository.ListActiveQueryJobs(ctx)
	if err != nil || len(active) != 1 {
		t.Fatalf("ListActiveQueryJobs=%#v err=%v", active, err)
	}
	repairedID := repairedManagedQueryJobID(corrupt)
	if active[0].ID != repairedID || len(active[0].WakeEvents) != 1 {
		t.Fatalf("repaired active job=%#v, want id=%s and preserved wake", active[0], repairedID)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != repairedID ||
		dependencies[0].ObligationID != obligationID {
		t.Fatalf("repaired dependencies=%#v err=%v", dependencies, err)
	}
	if err := repository.BindQueryObligation(ctx, repairedID); err != nil {
		t.Fatalf("stable obligation did not survive rebind: %v", err)
	}
	chunks, err := repository.ListQueryResultChunks(ctx, repairedID, 0, 10)
	if err != nil || len(chunks) != 1 || chunks[0].JobID != repairedID {
		t.Fatalf("repaired chunks=%#v err=%v", chunks, err)
	}
	repairedChild, found, err := repository.GetQueryJob(ctx, child.ID)
	if err != nil || !found || repairedChild.ReplacesJobID != repairedID {
		t.Fatalf("repaired child lineage found=%v child=%#v err=%v", found, repairedChild, err)
	}
	checkpoint, err := repository.LatestContinuationCheckpoint(ctx, run.ID)
	if err != nil || checkpoint == nil || len(checkpoint.DependencyIDs) != 1 || checkpoint.DependencyIDs[0] != repairedID {
		t.Fatalf("repaired checkpoint=%#v err=%v", checkpoint, err)
	}
	newCapacityPath, _ := queryCapacityPath(cfgDir, repairedID)
	var capacity jsonQueryCapacityRecord
	if err := readJSONLoose(newCapacityPath, &capacity); err != nil || capacity.JobID != repairedID || capacity.State != "active" {
		t.Fatalf("repaired capacity=%#v err=%v", capacity, err)
	}
	oldJobPath, _ := jsondb.QueryJobPath(cfgDir, oldID)
	if _, err := os.Stat(oldJobPath); !os.IsNotExist(err) {
		t.Fatalf("old corrupt query job still exists: err=%v", err)
	}
}

func TestJSONLiveLeasedPrimaryIdentityDefersRepairWithoutLosingCapacityOrJournal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()
	_, thread, run := seedLongHorizonJSONRun(t, cfgDir)
	now := nowUTC()
	oldID := "00000-00000"
	corrupt := managedSentinelTestJob(oldID, "")
	corrupt.Owner.ThreadID = thread.ID
	corrupt.Owner.RunID = run.ID
	corrupt.IdempotencyKey = "live-corrupt-idempotency"
	corrupt.Provider = "trino"
	corrupt.ConnectionID = "trino-connection"
	corrupt.Handle = jobs.NativeHandle{Provider: "trino", ID: "query-live", Extra: map[string]string{"next_uri": "https://trino/1"}}
	corrupt.Revision = 2
	corrupt.SubmittedAt = now
	corrupt.LastAccess = now
	jobPath, err := jsondb.QueryJobPath(cfgDir, oldID)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondb.WriteJSONFile(jobPath, jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(corrupt), Job: corrupt,
		MonitorLease: &jsonMonitorLease{Holder: "live-monitor", ExpiresAt: now.Add(time.Hour)}, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	capacityPath, _ := queryCapacityPath(cfgDir, oldID)
	if err := jsondb.WriteJSONFile(capacityPath, jsonQueryCapacityRecord{
		Kind: queryCapacityRecordKind, SchemaVersion: longHorizonSchemaVersion,
		JobID: oldID, ThreadID: thread.ID, ConnectionKey: queryConnectionKey(corrupt),
		State: "active", ReservedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	nextHandle := jobs.NativeHandle{Provider: "trino", ID: "query-live", Extra: map[string]string{"next_uri": "https://trino/2"}}
	page := jobs.ResultPage{Rows: []map[string]any{{"value": 1}}, Bytes: 12, CursorAfter: &nextHandle}
	committed := corrupt
	committed.DrainSeq = 1
	committed.DrainRowsCommitted = 1
	committed.DrainBytesCommitted = 12
	transaction := jsonQueryTransactionRecord{
		Kind: queryTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: drainTransactionID(oldID, 1),
		Commit: jobs.DrainCommit{
			JobID: oldID, Owner: corrupt.Owner, Seq: 1, Page: page, Handle: nextHandle,
			DrainRowsCommitted: 1, DrainBytesCommitted: 12, Record: committed,
		},
		Chunk: QueryResultChunk{
			JobID: oldID, Sequence: 1, Page: page, CursorAfter: nextHandle,
			RowCount: 1, ByteCount: 12, CreatedAt: now,
		},
		Event: QueryJobEvent{
			JobID: oldID, Type: "drain_observation", ObservationKey: "drain:1", CreatedAt: now,
		},
		PreparedAt: now,
	}
	transactionPath, err := queryTransactionPath(cfgDir, transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := jsondb.WriteJSONFile(transactionPath, transaction); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("reopen while corrupt identity is live-leased: %v", err)
	}
	if _, ok, err := reopened.GetQueryJobByIdempotency(ctx, corrupt.Provider, corrupt.ConnectionID, corrupt.IdempotencyKey); ok || !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("live corrupt idempotency lookup ok=%v err=%v, want ErrLeaseLost", ok, err)
	}
	newJob := managedSentinelTestJob("qj-dbx-capacity-probe", "")
	newJob.Owner.ThreadID = thread.ID
	newJob.Owner.RunID = run.ID
	newJob.IdempotencyKey = "unrelated-capacity-probe"
	newJob.Provider = corrupt.Provider
	newJob.ConnectionID = corrupt.ConnectionID
	granted, _, err := reopened.ReserveQueryCapacity(ctx, newJob, jobs.CapacityLimits{MaxGlobal: 1})
	if err != nil || granted {
		t.Fatalf("capacity probe granted=%v err=%v, want denied by live corrupt job", granted, err)
	}
	var preservedCapacity jsonQueryCapacityRecord
	if err := readJSONLoose(capacityPath, &preservedCapacity); err != nil || preservedCapacity.State != "active" {
		t.Fatalf("live corrupt capacity=%#v err=%v, want preserved active reservation", preservedCapacity, err)
	}
	if _, err := os.Stat(transactionPath); err != nil {
		t.Fatalf("live corrupt drain journal was not deferred: %v", err)
	}

	var expired jsonQueryJobRecord
	if err := readJSONLoose(jobPath, &expired); err != nil {
		t.Fatal(err)
	}
	expired.MonitorLease.ExpiresAt = now.Add(-time.Minute)
	if err := jsondb.WriteJSONFile(jobPath, expired); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("reopen after corrupt identity lease expiry: %v", err)
	}
	repairedID := repairedManagedQueryJobID(corrupt)
	got, ok, err := recovered.GetQueryJob(ctx, repairedID)
	if err != nil || !ok || got.Revision != 3 || got.DrainSeq != 1 || got.DrainRowsCommitted != 1 ||
		got.DrainBytesCommitted != 12 || got.Handle.Extra["next_uri"] != "https://trino/2" {
		t.Fatalf("repaired/recovered job=%#v ok=%v err=%v", got, ok, err)
	}
	chunks, err := recovered.ListQueryResultChunks(ctx, repairedID, 0, 10)
	if err != nil || len(chunks) != 1 || chunks[0].JobID != repairedID || chunks[0].RowCount != 1 {
		t.Fatalf("repaired/recovered chunks=%#v err=%v", chunks, err)
	}
	if _, err := os.Stat(transactionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("recovered drain journal still exists: %v", err)
	}
	newCapacityPath, _ := queryCapacityPath(cfgDir, repairedID)
	var repairedCapacity jsonQueryCapacityRecord
	if err := readJSONLoose(newCapacityPath, &repairedCapacity); err != nil || repairedCapacity.JobID != repairedID || repairedCapacity.State != "active" {
		t.Fatalf("repaired capacity=%#v err=%v", repairedCapacity, err)
	}
	if _, err := os.Stat(jobPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy corrupt job still exists after lease expiry: %v", err)
	}
}

func TestJSONJournalDeferralMatchesOnlyStructuredIdentityFields(t *testing.T) {
	t.Parallel()
	now := nowUTC()
	path := filepath.Join(t.TempDir(), "journal.json")
	transaction := jsonQueryTransactionRecord{
		Kind: queryTransactionRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ID: "unrelated-nil-business-value",
		Commit: jobs.DrainCommit{
			JobID: "qj-unrelated", Seq: 1,
			Page:   jobs.ResultPage{Rows: []map[string]any{{"business_value": "nil"}}},
			Record: managedSentinelTestJob("qj-unrelated", ""),
		},
		Chunk:      QueryResultChunk{JobID: "qj-unrelated", Sequence: 1},
		Event:      QueryJobEvent{JobID: "qj-unrelated", ObservationKey: "unrelated", CreatedAt: now},
		PreparedAt: now,
	}
	if err := jsondb.WriteJSONFile(path, transaction); err != nil {
		t.Fatal(err)
	}
	live := map[string]jsonQueryJobRecord{"nil": {Job: managedSentinelTestJob("nil", "")}}
	deferred, err := queryTransactionReferencesLiveQuarantinedJob(path, live)
	if err != nil || deferred {
		t.Fatalf("unrelated business value deferred=%v err=%v", deferred, err)
	}
	transaction.Event.JobID = "  nil  "
	if err := jsondb.WriteJSONFile(path, transaction); err != nil {
		t.Fatal(err)
	}
	deferred, err = queryTransactionReferencesLiveQuarantinedJob(path, live)
	if err != nil || !deferred {
		t.Fatalf("trimmed identity field deferred=%v err=%v, want true", deferred, err)
	}
}

func TestJSONPrimaryIdentityRepairKeepsObligationStableForNextReplacement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, thread, run := seedLongHorizonJSONRun(t, cfgDir)
	now := nowUTC()
	oldID := "00000-00000"
	root := managedSentinelTestJob(oldID, "")
	root.Owner.ThreadID = thread.ID
	root.Owner.RunID = run.ID
	root.Status = jobs.JobStatusFailed
	root.ValidationState = jobs.ValidationRejected
	root.CompletedAt = &now
	root.SubmittedAt = now
	root.LastAccess = now
	if err := repository.writeJSONQueryJobLocked(jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(root), Job: root,
	}); err != nil {
		t.Fatal(err)
	}
	wantObligation, err := QueryObligationIDForRoot(run.ID, oldID)
	if err != nil {
		t.Fatal(err)
	}
	dependencyPath, _ := runQueryDependencyPath(cfgDir, run.ID, oldID)
	if err := jsondb.WriteJSONFile(dependencyPath, jsonRunQueryDependencyRecord{
		Kind: runQueryDependencyRecordKind, SchemaVersion: longHorizonSchemaVersion,
		RunQueryDependency: RunQueryDependency{
			RunID: run.ID, JobID: oldID, RequiredForCompletion: true,
			ValidationState: QueryValidationRejected, ObligationID: wantObligation,
			CreatedAt: now, UpdatedAt: now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ListQueryJobsByThread(ctx, thread.ID); err != nil {
		t.Fatalf("repair root identity: %v", err)
	}
	repairedID := repairedManagedQueryJobID(root)
	if err := repository.BindQueryObligation(ctx, repairedID); err != nil {
		t.Fatalf("rebind repaired root: %v", err)
	}
	successor := managedSentinelTestJob("qj-dbx-after-primary-repair", repairedID)
	successor.Owner.ThreadID = thread.ID
	successor.Owner.RunID = run.ID
	successor.SubmittedAt = now.Add(time.Second)
	successor.LastAccess = successor.SubmittedAt
	if _, err := repository.UpsertQueryJobCAS(ctx, successor, 0); err != nil {
		t.Fatalf("persist successor: %v", err)
	}
	if err := repository.BindQueryObligation(ctx, successor.ID); err != nil {
		t.Fatalf("bind successor after primary repair: %v", err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	for _, dependency := range dependencies {
		if dependency.ObligationID != wantObligation {
			t.Fatalf("dependency %s obligation=%s, want stable %s", dependency.JobID, dependency.ObligationID, wantObligation)
		}
		if dependency.JobID == repairedID && (!dependency.Detached || dependency.ReplacementJobID != successor.ID) {
			t.Fatalf("repaired predecessor lineage=%#v", dependency)
		}
		if dependency.JobID == successor.ID && dependency.Detached {
			t.Fatalf("successor unexpectedly detached: %#v", dependency)
		}
	}
}

func TestMySQLLegacySentinelReplacementRemainsMonitorable(t *testing.T) {
	t.Parallel()
	record := managedSentinelTestJob("qj-dbx-running", "00000-00000")
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=?",
			columns: makeColumns(len(testMySQLJobRow(record))), rows: [][]driver.Value{testMySQLJobRow(record)},
		},
		longHorizonScriptStep{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
	)
	stored, ok, err := NewMySQL(db).GetQueryJob(context.Background(), record.ID)
	if err != nil || !ok {
		t.Fatalf("GetQueryJob ok=%v err=%v", ok, err)
	}
	if stored.ReplacesJobID != "" {
		t.Fatalf("sanitized replaces_job_id=%q, want empty", stored.ReplacesJobID)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLQuarantinedPrimaryDoesNotBlockHealthyRecovery(t *testing.T) {
	t.Parallel()
	bad := managedSentinelTestJob("qj-_malformed", "")
	good := managedSentinelTestJob("qj-dbx-healthy", "")
	repaired := bad
	repaired.ID = repairedManagedQueryJobID(bad)
	now := time.Date(2026, 7, 13, 12, 5, 0, 0, time.UTC)
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "select id from query_jobs", columns: []string{"id"},
			rows: [][]driver.Value{{bad.ID}, {good.ID}},
		},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(bad))), rows: [][]driver.Value{testMySQLJobRow(bad)},
		},
		longHorizonScriptStep{
			kind: "query", contains: "select monitor_lease_holder", columns: makeColumns(5),
			rows: [][]driver.Value{{nil, nil, now.Add(-time.Hour), now, now}},
		},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(repaired))),
		},
		longHorizonScriptStep{kind: "exec", contains: "insert into query_jobs", affected: 1},
		longHorizonScriptStep{kind: "exec", contains: "update query_jobs set created_at", affected: 1},
		longHorizonScriptStep{kind: "exec", contains: "update query_job_events set job_id", affected: 0},
		longHorizonScriptStep{kind: "exec", contains: "update query_result_chunks set job_id", affected: 0},
		longHorizonScriptStep{kind: "exec", contains: "update run_query_dependencies set job_id", affected: 0},
		longHorizonScriptStep{kind: "exec", contains: "update query_capacity_reservations set job_id", affected: 1},
		longHorizonScriptStep{kind: "exec", contains: "update query_jobs set replaces_job_id", affected: 0},
		longHorizonScriptStep{kind: "exec", contains: "update run_query_dependencies set replacement_job_id", affected: 0},
		longHorizonScriptStep{kind: "query", contains: "from run_continuation_checkpoints for update", columns: []string{"id", "dependency_ids"}},
		longHorizonScriptStep{kind: "exec", contains: "delete from query_jobs", affected: 1},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=?",
			columns: makeColumns(len(testMySQLJobRow(repaired))), rows: [][]driver.Value{testMySQLJobRow(repaired)},
		},
		longHorizonScriptStep{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=?",
			columns: makeColumns(len(testMySQLJobRow(good))), rows: [][]driver.Value{testMySQLJobRow(good)},
		},
		longHorizonScriptStep{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
	)
	active, err := NewMySQL(db).ListActiveQueryJobs(context.Background())
	if err != nil || len(active) != 2 || active[0].ID != repaired.ID || active[1].ID != good.ID {
		t.Fatalf("ListActiveQueryJobs=%#v err=%v, want repaired=%s and healthy=%s", active, err, repaired.ID, good.ID)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLListingToleratesConcurrentPrimaryIdentityRepair(t *testing.T) {
	t.Parallel()
	badID := "qj-_malformed"
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "select id from query_jobs", columns: []string{"id"},
			rows: [][]driver.Value{{badID}},
		},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(managedSentinelTestJob(badID, "")))),
		},
	)
	active, err := NewMySQL(db).ListActiveQueryJobs(context.Background())
	if err != nil || len(active) != 0 {
		t.Fatalf("concurrent repair listing=%#v err=%v, want a retryable empty snapshot", active, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func mysqlIdempotencyIdentityRepairSteps(record, repaired jobs.JobRecord, databaseNow time.Time) []longHorizonScriptStep {
	return []longHorizonScriptStep{
		{
			kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?",
			columns: makeColumns(len(testMySQLJobRow(record))), rows: [][]driver.Value{testMySQLJobRow(record)},
		},
		{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(record))), rows: [][]driver.Value{testMySQLJobRow(record)},
		},
		{
			kind: "query", contains: "select monitor_lease_holder", columns: makeColumns(5),
			rows: [][]driver.Value{{nil, nil, databaseNow.Add(-time.Hour), databaseNow, databaseNow}},
		},
		{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(repaired))),
		},
		{kind: "exec", contains: "update query_jobs set idempotency_key=null", affected: 1},
		{kind: "exec", contains: "insert into query_jobs", affected: 1},
		{kind: "exec", contains: "update query_jobs set created_at", affected: 1},
		{kind: "exec", contains: "update query_job_events set job_id", affected: 0},
		{kind: "exec", contains: "update query_result_chunks set job_id", affected: 0},
		{kind: "exec", contains: "update run_query_dependencies set job_id", affected: 0},
		{kind: "exec", contains: "update query_capacity_reservations set job_id", affected: 0},
		{kind: "exec", contains: "update query_jobs set replaces_job_id", affected: 0},
		{kind: "exec", contains: "update run_query_dependencies set replacement_job_id", affected: 0},
		{kind: "query", contains: "from run_continuation_checkpoints for update", columns: []string{"id", "dependency_ids"}},
		{kind: "exec", contains: "delete from query_jobs", affected: 1},
		{
			kind: "query", contains: "from query_jobs where id=?",
			columns: makeColumns(len(testMySQLJobRow(repaired))), rows: [][]driver.Value{testMySQLJobRow(repaired)},
		},
		{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
	}
}

func TestMySQLIdempotencyLookupRepairsQuarantinedPrimary(t *testing.T) {
	t.Parallel()
	for _, corruptID := range []string{"nil", "00000-00000", "qj-_malformed", "QJ-noncanonical"} {
		t.Run(corruptID, func(t *testing.T) {
			t.Parallel()
			databaseNow := time.Date(2026, 7, 13, 12, 5, 0, 0, time.UTC)
			record := managedSentinelTestJob(corruptID, "")
			record.IdempotencyKey = "stable-call"
			repaired := record
			repaired.ID = repairedManagedQueryJobID(record)
			db, state := openLongHorizonScriptDB(t, mysqlIdempotencyIdentityRepairSteps(record, repaired, databaseNow)...)

			stored, found, err := NewMySQL(db).GetQueryJobByIdempotency(
				context.Background(), record.Provider, queryConnectionKey(record), record.IdempotencyKey,
			)
			if err != nil || !found || stored.ID != repaired.ID || stored.IdempotencyKey != record.IdempotencyKey {
				t.Fatalf("corrupt id %q lookup=%#v found=%v err=%v, want repaired %q", corruptID, stored, found, err, repaired.ID)
			}
			state.mu.Lock()
			committed, rolledBack := state.committed, state.rolledBack
			state.mu.Unlock()
			if !committed || rolledBack {
				t.Fatalf("corrupt id %q repair committed=%v rolledBack=%v", corruptID, committed, rolledBack)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}

func TestMySQLIdempotencyLookupDoesNotHideQuarantinedLiveOwner(t *testing.T) {
	t.Parallel()
	databaseNow := time.Date(2026, 7, 13, 12, 5, 0, 0, time.UTC)
	record := managedSentinelTestJob("qj-_malformed", "")
	record.IdempotencyKey = "stable-call"
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?",
			columns: makeColumns(len(testMySQLJobRow(record))), rows: [][]driver.Value{testMySQLJobRow(record)},
		},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(record))), rows: [][]driver.Value{testMySQLJobRow(record)},
		},
		longHorizonScriptStep{
			kind: "query", contains: "select monitor_lease_holder", columns: makeColumns(5),
			rows: [][]driver.Value{{"live-monitor", databaseNow.Add(time.Minute), databaseNow.Add(-time.Hour), databaseNow, databaseNow}},
		},
	)

	stored, found, err := NewMySQL(db).GetQueryJobByIdempotency(
		context.Background(), record.Provider, queryConnectionKey(record), record.IdempotencyKey,
	)
	if !errors.Is(err, jobs.ErrLeaseLost) || found || stored.ID != "" {
		t.Fatalf("live quarantined lookup=%#v found=%v err=%v, want ErrLeaseLost and no false absence", stored, found, err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if committed || !rolledBack {
		t.Fatalf("live quarantined repair committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLIdempotencyLookupConvergesAfterConcurrentIdentityRepair(t *testing.T) {
	t.Parallel()
	record := managedSentinelTestJob("00000-00000", "")
	record.IdempotencyKey = "stable-call"
	repaired := record
	repaired.ID = repairedManagedQueryJobID(record)
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?",
			columns: makeColumns(len(testMySQLJobRow(record))), rows: [][]driver.Value{testMySQLJobRow(record)},
		},
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(record))),
		},
		longHorizonScriptStep{
			kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?",
			columns: makeColumns(len(testMySQLJobRow(repaired))), rows: [][]driver.Value{testMySQLJobRow(repaired)},
		},
		longHorizonScriptStep{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
	)

	stored, found, err := NewMySQL(db).GetQueryJobByIdempotency(
		context.Background(), record.Provider, queryConnectionKey(record), record.IdempotencyKey,
	)
	if err != nil || !found || stored.ID != repaired.ID {
		t.Fatalf("concurrent repair lookup=%#v found=%v err=%v, want repaired winner %q", stored, found, err, repaired.ID)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestDeriveQueryObligationTreatsLegacySentinelParentAsAbsent(t *testing.T) {
	t.Parallel()
	record := managedSentinelTestJob("qj-dbx-running", "nil")
	reader := queryJobReaderFunc(func(context.Context, string) (jobs.JobRecord, bool, error) {
		return record, true, nil
	})
	derived, err := DeriveQueryObligationID(context.Background(), reader, record.Owner.RunID, record.ID)
	want, _ := QueryObligationIDForRoot(record.Owner.RunID, record.ID)
	if err != nil || derived != want {
		t.Fatalf("derived=%q err=%v, want %q", derived, err, want)
	}
	if _, err := DeriveQueryObligationID(context.Background(), reader, record.Owner.RunID, "00000-00000"); !errors.Is(err, ErrConflict) {
		t.Fatalf("sentinel primary derivation err=%v, want ErrConflict", err)
	}
}

func TestRunQueryDependencyWritesRejectSentinelIdentities(t *testing.T) {
	t.Parallel()
	base := RunQueryDependency{RunID: "run-1", JobID: "qj-dbx-current"}
	for _, testCase := range []struct {
		name   string
		mutate func(*RunQueryDependency)
	}{
		{name: "nil primary", mutate: func(dependency *RunQueryDependency) { dependency.JobID = "nil" }},
		{name: "zero primary", mutate: func(dependency *RunQueryDependency) { dependency.JobID = "00000-00000" }},
		{name: "malformed managed primary", mutate: func(dependency *RunQueryDependency) { dependency.JobID = "qj-_bad" }},
		{name: "nil replacement", mutate: func(dependency *RunQueryDependency) { dependency.ReplacementJobID = "nil" }},
		{name: "zero replacement", mutate: func(dependency *RunQueryDependency) { dependency.ReplacementJobID = "qj-0000_0000" }},
		{name: "malformed managed replacement", mutate: func(dependency *RunQueryDependency) { dependency.ReplacementJobID = "qj-_bad" }},
		{name: "self replacement", mutate: func(dependency *RunQueryDependency) { dependency.ReplacementJobID = dependency.JobID }},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			dependency := base
			testCase.mutate(&dependency)
			if _, err := normalizeRunQueryDependency(dependency); err == nil {
				t.Fatal("normalizeRunQueryDependency succeeded, want identity error")
			}
		})
	}
}

func TestJSONLegacySentinelDependencyReplacementIsCleared(t *testing.T) {
	t.Parallel()
	cfgDir := t.TempDir()
	repository, thread, run := seedLongHorizonJSONRun(t, cfgDir)
	job := managedSentinelTestJob("qj-dbx-dependency", "")
	job.Owner.ThreadID = thread.ID
	job.Owner.RunID = run.ID
	if err := repository.writeJSONQueryJobLocked(jsonQueryJobRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		ConnectionKey: queryConnectionKey(job), Job: job,
	}); err != nil {
		t.Fatal(err)
	}
	path, err := runQueryDependencyPath(cfgDir, run.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	record := jsonRunQueryDependencyRecord{
		Kind: queryJobRecordKind, SchemaVersion: longHorizonSchemaVersion,
		RunQueryDependency: RunQueryDependency{
			RunID: run.ID, JobID: job.ID, RequiredForCompletion: true,
			ValidationState: QueryValidationUnvalidated, ReplacementJobID: "00000-00000",
		},
	}
	record.Kind = runQueryDependencyRecordKind
	if err := jsondb.WriteJSONFile(path, record); err != nil {
		t.Fatal(err)
	}
	dependencies, err := repository.ListRunQueryDependencies(context.Background(), run.ID)
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("ListRunQueryDependencies=%#v err=%v", dependencies, err)
	}
	if dependencies[0].ReplacementJobID != "" {
		t.Fatalf("sanitized replacement_job_id=%q, want empty", dependencies[0].ReplacementJobID)
	}
	var healed jsonRunQueryDependencyRecord
	if err := readJSONLoose(path, &healed); err != nil {
		t.Fatal(err)
	}
	if healed.ReplacementJobID != "" {
		t.Fatalf("durable replacement_job_id=%q, want healed empty value", healed.ReplacementJobID)
	}
}

func TestMySQLLegacySentinelDependencyReplacementIsCleared(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 13, 0, 0, 0, time.UTC)
	db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
		kind: "query", contains: "select dependency identity", columns: makeColumns(9),
		rows: [][]driver.Value{{"run-1", "qj-dbx-current", true, false, QueryValidationUnvalidated, nil, "nil", now, now}},
	})
	dependency, err := scanRunQueryDependency(db.QueryRowContext(context.Background(), "SELECT dependency identity"))
	if err != nil {
		t.Fatal(err)
	}
	if dependency.ReplacementJobID != "" {
		t.Fatalf("sanitized replacement_job_id=%q, want empty", dependency.ReplacementJobID)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestDetachedSentinelDependencySuccessorIsQuarantined(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []string{"nil", "00000-00000", "qj-0000_0000"} {
		dependency := RunQueryDependency{
			RunID: "run-1", JobID: "qj-dbx-old", RequiredForCompletion: true,
			Detached: true, ReplacementJobID: sentinel,
		}
		if err := sanitizeLegacyRunQueryDependencyIdentity(&dependency); !errors.Is(err, ErrQuarantinedQueryJobIdentity) {
			t.Fatalf("detached sentinel %q err=%v, want quarantine", sentinel, err)
		}
	}
}

type queryJobReaderFunc func(context.Context, string) (jobs.JobRecord, bool, error)

func (fn queryJobReaderFunc) GetQueryJob(ctx context.Context, id string) (jobs.JobRecord, bool, error) {
	return fn(ctx, id)
}
