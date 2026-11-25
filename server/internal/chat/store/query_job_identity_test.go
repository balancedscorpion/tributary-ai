package store

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

func durableIdentityTestJob(t *testing.T) (*JSONStore, jobs.JobRecord) {
	t.Helper()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	record := testDurableJob(thread, run, time.Date(2026, 7, 13, 9, 0, 0, 0, time.UTC))
	record.Owner.UserToken = "user-token"
	record.ReplacesJobID = "job-predecessor"
	granted, revision, err := store.ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	record.Revision = revision
	return store, record
}

func TestJSONQueryJobWholeRecordWritesRejectImmutableIdentityChanges(t *testing.T) {
	t.Parallel()
	mutations := []struct {
		name   string
		mutate func(*jobs.JobRecord)
	}{
		{name: "provider", mutate: func(record *jobs.JobRecord) { record.Provider = "databricks" }},
		{name: "connection", mutate: func(record *jobs.JobRecord) { record.ConnectionID = "other-connection" }},
		{name: "sql", mutate: func(record *jobs.JobRecord) { record.SQL = "SELECT 2" }},
		{name: "sql-preview", mutate: func(record *jobs.JobRecord) { record.SQLPreview = "SELECT ?" }},
		{name: "thread", mutate: func(record *jobs.JobRecord) { record.Owner.ThreadID = "other-thread" }},
		{name: "run", mutate: func(record *jobs.JobRecord) { record.Owner.RunID = "other-run" }},
		{name: "user-token", mutate: func(record *jobs.JobRecord) { record.Owner.UserToken = "other-user-token" }},
		{name: "owner-handle", mutate: func(record *jobs.JobRecord) { record.Owner.Handle = "other-owner-handle" }},
		{name: "idempotency", mutate: func(record *jobs.JobRecord) { record.IdempotencyKey = "other-idempotency-key" }},
		{name: "replacement-lineage", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "other-predecessor" }},
	}
	for _, method := range []string{"cas", "upsert"} {
		for _, mutation := range mutations {
			t.Run(method+"/"+mutation.name, func(t *testing.T) {
				store, record := durableIdentityTestJob(t)
				candidate := record
				candidate.Status = jobs.JobStatusSubmitted
				mutation.mutate(&candidate)
				var err error
				if method == "cas" {
					_, err = store.UpsertQueryJobCAS(context.Background(), candidate, record.Revision)
				} else {
					err = store.UpsertQueryJob(context.Background(), candidate)
				}
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("immutable %s write err=%v, want ErrConflict", mutation.name, err)
				}
				after, found, getErr := store.GetQueryJob(context.Background(), record.ID)
				if getErr != nil || !found {
					t.Fatalf("GetQueryJob found=%v err=%v", found, getErr)
				}
				if after.Revision != record.Revision || after.Status != record.Status {
					t.Fatalf("rejected identity write changed lifecycle: before=%#v after=%#v", record, after)
				}
				probe := after
				if err := freezeQueryJobIdentity(record, &probe); err != nil {
					t.Fatalf("rejected identity write changed durable identity: %v", err)
				}
			})
		}
	}
}

func TestJSONQueryJobLifecyclePreservesRunIDClearedByRunDeletion(t *testing.T) {
	t.Parallel()
	store, thread, _ := seedLongHorizonJSONRun(t, t.TempDir())
	record := testDurableJob(thread, Run{}, time.Date(2026, 7, 13, 9, 15, 0, 0, time.UTC))
	record.Owner.RunID = ""
	granted, revision, err := store.ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	record.Revision = revision
	record.Status = jobs.JobStatusSubmitted
	record.Owner.RunID = "deleted-run-still-in-memory"
	newRevision, err := store.UpsertQueryJobCAS(context.Background(), record, revision)
	if err != nil || newRevision != revision+1 {
		t.Fatalf("lifecycle update revision=%d err=%v", newRevision, err)
	}
	after, found, err := store.GetQueryJob(context.Background(), record.ID)
	if err != nil || !found || after.Owner.RunID != "" || after.Status != jobs.JobStatusSubmitted {
		t.Fatalf("cleared durable run was reattached: found=%v record=%#v err=%v", found, after, err)
	}
}

func TestJSONQueryJobLifecycleFillsBlankRunIDOnceWhenRunExists(t *testing.T) {
	t.Parallel()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	record := testDurableJob(thread, Run{}, time.Date(2026, 7, 13, 9, 20, 0, 0, time.UTC))
	record.Owner.RunID = ""
	granted, revision, err := store.ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	record.Revision = revision
	record.Status = jobs.JobStatusSubmitted
	record.Owner.RunID = run.ID
	if _, err := store.UpsertQueryJobCAS(context.Background(), record, revision); err != nil {
		t.Fatalf("attach existing run: %v", err)
	}
	after, found, err := store.GetQueryJob(context.Background(), record.ID)
	if err != nil || !found || after.Owner.RunID != run.ID {
		t.Fatalf("blank durable run was not attached once: found=%v record=%#v err=%v", found, after, err)
	}
}

func TestJSONManagedQueryJobNeverReattachesBlankRunToExistingRun(t *testing.T) {
	t.Parallel()
	store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
	record := testDurableJob(thread, Run{}, time.Date(2026, 7, 13, 9, 22, 0, 0, time.UTC))
	record.Owner.RunID = ""
	record.Metadata = map[string]any{jobs.QueryObligationManagedMetadataKey: true}
	granted, revision, err := store.ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{})
	if err != nil || !granted {
		t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
	}
	record.Revision = revision
	record.Status = jobs.JobStatusSubmitted
	record.Owner.RunID = run.ID
	if _, err := store.UpsertQueryJobCAS(context.Background(), record, revision); err != nil {
		t.Fatalf("managed lifecycle update: %v", err)
	}
	after, found, err := store.GetQueryJob(context.Background(), record.ID)
	if err != nil || !found || after.Owner.RunID != "" || !jobs.RequiresQueryObligationBinding(after) {
		t.Fatalf("managed job was reattached to a new obligation scope: found=%v record=%#v err=%v", found, after, err)
	}
}

func TestJSONQueryJobLifecyclePreservesManagedRecoveryMarker(t *testing.T) {
	t.Parallel()
	store, record := durableIdentityTestJob(t)
	record.Metadata = map[string]any{jobs.QueryObligationManagedMetadataKey: true, "old": "value"}
	if _, err := store.UpsertQueryJobCAS(context.Background(), record, record.Revision); err != nil {
		t.Fatalf("persist managed marker: %v", err)
	}
	current, found, err := store.GetQueryJob(context.Background(), record.ID)
	if err != nil || !found {
		t.Fatalf("GetQueryJob found=%v err=%v", found, err)
	}
	current.Status = jobs.JobStatusSubmitted
	current.Metadata = map[string]any{"fresh": "telemetry"} // stale writer omitted marker
	if _, err := store.UpsertQueryJobCAS(context.Background(), current, current.Revision); err != nil {
		t.Fatalf("lifecycle update: %v", err)
	}
	after, found, err := store.GetQueryJob(context.Background(), record.ID)
	if err != nil || !found || !jobs.RequiresQueryObligationBinding(after) || after.Metadata["fresh"] != "telemetry" {
		t.Fatalf("managed marker was not preserved: found=%v record=%#v err=%v", found, after, err)
	}
}

func mysqlIdentityTestRecord(now time.Time) jobs.JobRecord {
	return jobs.JobRecord{
		ID: "job-identity", Provider: "databricks", ConnectionID: "warehouse-1",
		SQL: "SELECT 1", SQLPreview: "SELECT 1", Status: jobs.JobStatusReserved,
		Owner:          jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", UserToken: "user-token", Handle: "owner-handle"},
		IdempotencyKey: "idempotency-1", ReplacesJobID: "job-predecessor",
		SubmittedAt: now.Add(-time.Minute), LastAccess: now, Revision: 7,
	}
}

func TestMySQLQueryJobWholeRecordWritesRejectRunAndLineageChangesAtValidRevision(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 9, 30, 0, 0, time.UTC)
	for _, method := range []string{"cas", "upsert"} {
		for _, mutation := range []struct {
			name   string
			mutate func(*jobs.JobRecord)
		}{
			{name: "run", mutate: func(record *jobs.JobRecord) { record.Owner.RunID = "run-other" }},
			{name: "replacement-lineage", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "job-other-predecessor" }},
		} {
			t.Run(method+"/"+mutation.name, func(t *testing.T) {
				current := mysqlIdentityTestRecord(now)
				candidate := current
				candidate.Status = jobs.JobStatusSubmitted
				mutation.mutate(&candidate)
				db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
					kind: "query", contains: "from query_jobs where id=? for update",
					columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)},
				})
				store := NewMySQL(db)
				var err error
				if method == "cas" {
					_, err = store.UpsertQueryJobCAS(context.Background(), candidate, current.Revision)
				} else {
					err = store.UpsertQueryJob(context.Background(), candidate)
				}
				if !errors.Is(err, ErrConflict) {
					t.Fatalf("immutable %s write err=%v, want ErrConflict", mutation.name, err)
				}
				state.mu.Lock()
				committed, rolledBack := state.committed, state.rolledBack
				state.mu.Unlock()
				if committed || !rolledBack {
					t.Fatalf("rejected identity write committed=%v rolledBack=%v", committed, rolledBack)
				}
				assertLongHorizonScriptDone(t, state)
			})
		}
	}
}

func TestMySQLQueryJobLifecyclePreservesRunIDClearedByRunDeletion(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 9, 45, 0, 0, time.UTC)
	current := mysqlIdentityTestRecord(now)
	current.Owner.RunID = ""
	current.Metadata = map[string]any{jobs.QueryObligationManagedMetadataKey: true}
	candidate := current
	candidate.Owner.RunID = "deleted-run-still-in-memory"
	candidate.Status = jobs.JobStatusSubmitted
	candidate.Metadata = map[string]any{"fresh": "telemetry"}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)},
		},
		longHorizonScriptStep{kind: "exec", contains: "update query_jobs set", affected: 1, check: func(_ string, args []driver.NamedValue) error {
			// queryJobUpdateSQL begins provider, connection_id, connection_key,
			// thread_id, run_id. The stale deleted run must be frozen to NULL.
			if len(args) < 5 || args[4].Value != nil {
				return fmt.Errorf("run_id update argument=%#v, want nil", args)
			}
			if len(args) <= 30 {
				return fmt.Errorf("metadata update argument missing: %#v", args)
			}
			raw, ok := args[30].Value.(string)
			if !ok {
				return fmt.Errorf("metadata update argument=%#v, want JSON string", args[30].Value)
			}
			var metadata map[string]any
			if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
				return fmt.Errorf("decode metadata update: %w", err)
			}
			if managed, _ := metadata[jobs.QueryObligationManagedMetadataKey].(bool); !managed || metadata["fresh"] != "telemetry" {
				return fmt.Errorf("managed recovery marker was not preserved: %#v", metadata)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "exec", contains: "insert into query_capacity_reservations", affected: 1},
	)
	revision, err := NewMySQL(db).UpsertQueryJobCAS(context.Background(), candidate, current.Revision)
	if err != nil || revision != current.Revision+1 {
		t.Fatalf("lifecycle update revision=%d err=%v", revision, err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if !committed || rolledBack {
		t.Fatalf("lifecycle update committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLQueryJobLifecycleFillsBlankRunIDOnceWhenRunExists(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 9, 50, 0, 0, time.UTC)
	current := mysqlIdentityTestRecord(now)
	current.Owner.RunID = ""
	candidate := current
	candidate.Owner.RunID = "run-1"
	candidate.Status = jobs.JobStatusSubmitted
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)},
		},
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs where id=?", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "exec", contains: "update query_jobs set", affected: 1, check: func(_ string, args []driver.NamedValue) error {
			if len(args) < 5 || args[4].Value != "run-1" {
				return fmt.Errorf("run_id update argument=%#v, want run-1", args)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "exec", contains: "insert into query_capacity_reservations", affected: 1},
	)
	if _, err := NewMySQL(db).UpsertQueryJobCAS(context.Background(), candidate, current.Revision); err != nil {
		t.Fatalf("attach existing run: %v", err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if !committed || rolledBack {
		t.Fatalf("run attachment committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLManagedQueryJobNeverReattachesBlankRunToExistingRun(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 9, 52, 0, 0, time.UTC)
	current := mysqlIdentityTestRecord(now)
	current.Owner.RunID = ""
	current.Metadata = map[string]any{jobs.QueryObligationManagedMetadataKey: true}
	candidate := current
	candidate.Owner.RunID = "run-1" // an existing later run in the same thread
	candidate.Status = jobs.JobStatusSubmitted
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update",
			columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)},
		},
		// No runs lookup is expected: the managed marker makes the durable blank
		// an unconditional tombstone even when run-1 exists.
		longHorizonScriptStep{kind: "exec", contains: "update query_jobs set", affected: 1, check: func(_ string, args []driver.NamedValue) error {
			if len(args) < 5 || args[4].Value != nil {
				return fmt.Errorf("managed run_id update argument=%#v, want nil", args)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "exec", contains: "insert into query_capacity_reservations", affected: 1},
	)
	if _, err := NewMySQL(db).UpsertQueryJobCAS(context.Background(), candidate, current.Revision); err != nil {
		t.Fatalf("managed lifecycle update: %v", err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if !committed || rolledBack {
		t.Fatalf("managed lifecycle update committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestJSONFencedDrainCommitRejectsImmutableRunAndLineageChanges(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 9, 55, 0, 0, time.UTC)
	for _, mutation := range []struct {
		name   string
		mutate func(*jobs.JobRecord)
	}{
		{name: "run", mutate: func(record *jobs.JobRecord) { record.Owner.RunID = "run-other" }},
		{name: "replacement-lineage", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "job-other-predecessor" }},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			store, thread, run := seedLongHorizonJSONRun(t, t.TempDir())
			current := testDurableJob(thread, run, now)
			current.Provider = "trino"
			current.ConnectionID = "trino-1"
			current.Status = jobs.JobStatusRunning
			current.ReplacesJobID = "job-predecessor"
			current.Handle = jobs.NativeHandle{Provider: "trino", ID: "native-1", Extra: map[string]string{"next_uri": "one"}}
			granted, revision, err := store.ReserveQueryCapacity(context.Background(), current, jobs.CapacityLimits{})
			if err != nil || !granted {
				t.Fatalf("ReserveQueryCapacity granted=%v revision=%d err=%v", granted, revision, err)
			}
			current.Revision = revision
			if acquired, err := store.AcquireMonitorLease(context.Background(), current.ID, "holder", time.Minute); err != nil || !acquired {
				t.Fatalf("AcquireMonitorLease acquired=%v err=%v", acquired, err)
			}
			next := jobs.NativeHandle{Provider: "trino", ID: "native-1", Extra: map[string]string{"next_uri": "two"}}
			proposed := current
			proposed.Handle = next
			proposed.DrainSeq = 1
			mutation.mutate(&proposed)
			commit := jobs.DrainCommit{JobID: current.ID, Owner: current.Owner, Seq: 1, Handle: next, Record: proposed}
			if _, err := store.CommitDrainObservationFenced(context.Background(), commit, "holder"); !errors.Is(err, ErrConflict) {
				t.Fatalf("fenced drain err=%v, want ErrConflict", err)
			}
			chunks, err := store.ListQueryResultChunks(context.Background(), current.ID, 0, 10)
			if err != nil || len(chunks) != 0 {
				t.Fatalf("rejected drain chunks=%#v err=%v", chunks, err)
			}
			after, found, err := store.GetQueryJob(context.Background(), current.ID)
			if err != nil || !found || after.Revision != revision || after.DrainSeq != 0 {
				t.Fatalf("rejected drain changed job: found=%v record=%#v err=%v", found, after, err)
			}
		})
	}
}

func TestFencedDrainCommitRejectsImmutableRunAndLineageChanges(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	for _, mutation := range []struct {
		name   string
		mutate func(*jobs.JobRecord)
	}{
		{name: "run", mutate: func(record *jobs.JobRecord) { record.Owner.RunID = "run-other" }},
		{name: "replacement-lineage", mutate: func(record *jobs.JobRecord) { record.ReplacesJobID = "job-other-predecessor" }},
	} {
		t.Run("mysql/"+mutation.name, func(t *testing.T) {
			current := mysqlIdentityTestRecord(now)
			current.Provider = "trino"
			current.ConnectionID = "trino-1"
			current.Status = jobs.JobStatusRunning
			current.Handle = jobs.NativeHandle{Provider: "trino", ID: "native-1"}
			proposed := current
			proposed.DrainSeq = 1
			mutation.mutate(&proposed)
			commit := jobs.DrainCommit{
				JobID: current.ID, Owner: current.Owner, Seq: 1, Record: proposed,
				Handle: jobs.NativeHandle{Provider: "trino", ID: "native-1", Extra: map[string]string{"next_uri": "two"}},
			}
			db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
				kind: "query", contains: "from query_jobs where id=? for update",
				columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)},
			})
			if _, err := NewMySQL(db).CommitDrainObservationFenced(context.Background(), commit, "holder"); !errors.Is(err, ErrConflict) {
				t.Fatalf("fenced drain err=%v, want ErrConflict", err)
			}
			state.mu.Lock()
			committed, rolledBack := state.committed, state.rolledBack
			state.mu.Unlock()
			if committed || !rolledBack {
				t.Fatalf("rejected drain committed=%v rolledBack=%v", committed, rolledBack)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}
