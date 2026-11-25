package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

type longHorizonScriptStep struct {
	kind     string
	contains string
	columns  []string
	rows     [][]driver.Value
	affected int64
	err      error
	check    func(string, []driver.NamedValue) error
}

type longHorizonScriptState struct {
	mu         sync.Mutex
	steps      []longHorizonScriptStep
	committed  bool
	commitErr  error
	rolledBack bool
}

var longHorizonScriptStates sync.Map

type longHorizonScriptDriver struct{}
type longHorizonScriptConn struct{ state *longHorizonScriptState }
type longHorizonScriptTx struct{ state *longHorizonScriptState }

func (longHorizonScriptDriver) Open(name string) (driver.Conn, error) {
	value, ok := longHorizonScriptStates.Load(name)
	if !ok {
		return nil, fmt.Errorf("unknown long-horizon script %q", name)
	}
	return &longHorizonScriptConn{state: value.(*longHorizonScriptState)}, nil
}

func (c *longHorizonScriptConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *longHorizonScriptConn) Close() error { return nil }
func (c *longHorizonScriptConn) Begin() (driver.Tx, error) {
	return &longHorizonScriptTx{state: c.state}, nil
}

func (c *longHorizonScriptConn) next(kind, query string, args []driver.NamedValue) (longHorizonScriptStep, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if len(c.state.steps) == 0 {
		return longHorizonScriptStep{}, fmt.Errorf("unexpected %s: %s args=%#v", kind, query, args)
	}
	step := c.state.steps[0]
	c.state.steps = c.state.steps[1:]
	if step.kind != kind || !strings.Contains(strings.ToLower(query), strings.ToLower(step.contains)) {
		return longHorizonScriptStep{}, fmt.Errorf("got %s %q, want %s containing %q", kind, query, step.kind, step.contains)
	}
	if step.check != nil {
		if err := step.check(query, args); err != nil {
			return longHorizonScriptStep{}, err
		}
	}
	return step, nil
}

func (c *longHorizonScriptConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	step, err := c.next("query", query, args)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return approvalRows(step.columns, step.rows), nil
}

func (c *longHorizonScriptConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	step, err := c.next("exec", query, args)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return approvalStoreTestResult(step.affected), nil
}

func (tx *longHorizonScriptTx) Commit() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.committed = true
	return tx.state.commitErr
}

func (tx *longHorizonScriptTx) Rollback() error {
	tx.state.mu.Lock()
	defer tx.state.mu.Unlock()
	tx.state.rolledBack = true
	return nil
}

func init() { sql.Register("long-horizon-script", longHorizonScriptDriver{}) }

func openLongHorizonScriptDB(t *testing.T, steps ...longHorizonScriptStep) (*sql.DB, *longHorizonScriptState) {
	t.Helper()
	name := strings.ReplaceAll(t.Name(), "/", "-") + fmt.Sprintf("-%d", time.Now().UnixNano())
	state := &longHorizonScriptState{steps: append([]longHorizonScriptStep(nil), steps...)}
	longHorizonScriptStates.Store(name, state)
	t.Cleanup(func() { longHorizonScriptStates.Delete(name) })
	db, err := sql.Open("long-horizon-script", name)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	return db, state
}

func assertLongHorizonScriptDone(t *testing.T, state *longHorizonScriptState) {
	t.Helper()
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.steps) != 0 {
		t.Fatalf("%d scripted SQL steps were not consumed: %#v", len(state.steps), state.steps)
	}
}

func testMySQLRunRow(run Run) []driver.Value {
	wakeAt, leaseExpiry := driver.Value(nil), driver.Value(nil)
	if run.WakeAt != nil {
		wakeAt = run.WakeAt.UTC()
	}
	if run.ResumeLeaseExpiresAt != nil {
		leaseExpiry = run.ResumeLeaseExpiresAt.UTC()
	}
	leaseHolder := driver.Value(nil)
	if run.ResumeLeaseHolder != "" {
		leaseHolder = run.ResumeLeaseHolder
	}
	return []driver.Value{
		run.ID, run.ThreadID, run.Status, run.Revision, run.ProviderID, nil, nil, run.Model, nil, run.SystemPrompt, int64(run.MaxIterations),
		int64(run.ContinuationCount), int64(run.MaxAutoContinuations), run.LastCheckpointSequence, wakeAt, leaseHolder, leaseExpiry,
		nil, nil, nil, nil, nil, nil, "analysis",
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		run.CreatedAt.UTC(), nil, nil, run.UpdatedAt.UTC(),
	}
}

func testMySQLJobRow(record jobs.JobRecord) []driver.Value {
	connectionKey := queryConnectionKey(record)
	jsonValue := func(value any) driver.Value {
		if value == nil {
			return nil
		}
		raw, _ := json.Marshal(value)
		if string(raw) == "{}" || string(raw) == "null" {
			return nil
		}
		return raw
	}
	timeValue := func(value *time.Time) driver.Value {
		if value == nil {
			return nil
		}
		return value.UTC()
	}
	return []driver.Value{
		record.ID, record.Provider, record.ConnectionID, connectionKey, record.Owner.ThreadID, record.Owner.RunID,
		record.Owner.UserToken, record.Owner.Handle, record.SQL, record.SQLPreview, string(record.Status), string(record.MonitorHealth),
		string(record.PolicyHealth), string(record.MaterializationState), string(record.ValidationState), record.ValidationDetail,
		string(record.CancelReason), string(record.CancelOutcome), timeValue(record.CancelStartedAt), jsonValue(record.Handle), record.IdempotencyKey, record.ReplacesJobID,
		record.KeepOnRunCompletion, record.SubmitDispatched, jsonValue(record.Profile), string(record.ProfileOutcome), jsonValue(record.ProfileHandle),
		record.ProfilePhase, record.ProfileDispatched, jsonValue(record.Budgets), nil, jsonValue(record.Metadata), record.ArtifactID, record.Error,
		record.SubmittedAt.UTC(), timeValue(record.RemoteSubmittedAt), timeValue(record.NextPollAt), string(record.NextPollAction), timeValue(record.StartedAt),
		timeValue(record.CompletedAt), record.LastAccess.UTC(), record.Revision, record.DrainSeq,
		record.DrainRowsCommitted, record.DrainBytesCommitted, record.DrainTruncated,
	}
}

func testMySQLReplacementJobLockStep(id string, status jobs.JobStatus, threadID, runID, replacesJobID string, now time.Time) longHorizonScriptStep {
	record := jobs.JobRecord{
		ID: id, Provider: "databricks", ConnectionID: "warehouse-1",
		Status: status, MonitorHealth: jobs.MonitorHealthOK, PolicyHealth: jobs.PolicyHealthOK,
		MaterializationState: jobs.MaterializationNone, ValidationState: jobs.ValidationUnvalidated,
		Owner: jobs.JobOwner{ThreadID: threadID, RunID: runID}, ReplacesJobID: replacesJobID,
		SubmittedAt: now.Add(-time.Hour), LastAccess: now,
	}
	row := testMySQLJobRow(record)
	return longHorizonScriptStep{
		kind: "query", contains: "from query_jobs where id=? for update",
		columns: makeColumns(len(row)), rows: [][]driver.Value{row},
	}
}

func testMySQLFencedDrainCommit(now time.Time) jobs.DrainCommit {
	owner := jobs.JobOwner{ThreadID: "thread-drain", RunID: "run-drain", Handle: "owner-drain"}
	oldHandle := jobs.NativeHandle{Provider: "trino", ID: "native-drain", Extra: map[string]string{"next_uri": "one"}}
	nextHandle := jobs.NativeHandle{Provider: "trino", ID: "native-drain", Extra: map[string]string{"next_uri": "two"}}
	record := jobs.JobRecord{
		ID: "job-drain", Provider: "trino", ConnectionID: "trino-1", Owner: owner,
		Status: jobs.JobStatusRunning, MonitorHealth: jobs.MonitorHealthOK, PolicyHealth: jobs.PolicyHealthOK,
		MaterializationState: jobs.MaterializationPartial, ValidationState: jobs.ValidationValidated,
		Handle: oldHandle, SubmittedAt: now.Add(-time.Minute), LastAccess: now, Revision: 7, DrainSeq: 1,
		DrainRowsCommitted: 1, DrainBytesCommitted: 11, DrainTruncated: true,
	}
	page := jobs.ResultPage{Rows: []map[string]any{{"value": 1}}, Bytes: 11, Truncated: true, CursorAfter: &nextHandle}
	return jobs.DrainCommit{
		JobID: record.ID, Owner: owner, Seq: 1, Page: page, Handle: nextHandle,
		DrainRowsCommitted: 1, DrainBytesCommitted: 11, DrainTruncated: true, Record: record,
	}
}

func testMySQLFencedDrainWriteSteps(commit jobs.DrainCommit, databaseNow, leaseExpires time.Time, finalAffected int64) []longHorizonScriptStep {
	current := commit.Record
	current.DrainSeq = 0
	current.DrainRowsCommitted = 0
	current.DrainBytesCommitted = 0
	current.DrainTruncated = false
	return []longHorizonScriptStep{
		{kind: "query", contains: "from query_jobs where id=? for update", columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)}},
		{kind: "query", contains: "monitor_lease_holder, monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(3), rows: [][]driver.Value{{"holder-drain", leaseExpires, databaseNow}}},
		{kind: "exec", contains: "insert into query_result_chunks", affected: 1},
		{kind: "query", contains: "coalesce(max(sequence)", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		{kind: "exec", contains: "insert into query_job_events", affected: 1},
		{kind: "exec", contains: "insert into query_capacity_reservations", affected: 1},
		{kind: "exec", contains: "update query_jobs set", affected: finalAffected, check: func(query string, args []driver.NamedValue) error {
			normalized := strings.Join(strings.Fields(strings.ToLower(query)), " ")
			for _, required := range []string{
				"where id=? and revision=? and drain_seq=?", "monitor_lease_holder=?", "monitor_lease_expires_at>utc_timestamp(3)",
				"execution_state=?", "drain_rows_committed=?", "drain_bytes_committed=?", "drain_truncated=?",
			} {
				if !strings.Contains(normalized, required) {
					return fmt.Errorf("fenced drain update missing %q: %s", required, query)
				}
			}
			if len(args) < 5 || args[len(args)-5].Value != commit.JobID || args[len(args)-4].Value != commit.Record.Revision ||
				args[len(args)-3].Value != int64(0) || args[len(args)-2].Value != string(jobs.JobStatusRunning) ||
				args[len(args)-1].Value != "holder-drain" {
				return fmt.Errorf("fenced drain args=%#v", args)
			}
			return nil
		}},
	}
}

func TestMySQLSuspendRunRollsBackCheckpointWhenRunUpdateFails(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	wakeAt := dbNow.Add(time.Minute)
	mutation := SuspendRunMutation{
		Checkpoint: ContinuationCheckpoint{
			ID: "checkpoint-mysql-atomic", RunID: "run-mysql", ThreadID: "thread-mysql",
			HistoryJSON:           jsonRaw(`[{"role":"assistant","content":"waiting"}]`),
			ExecutionSnapshotJSON: jsonRaw(`{"system_prompt":"system","tools":["query_status"]}`),
			CreatedAt:             dbNow,
		},
		ExpectedRevision: 4, ExpectedStatuses: []string{RunStatusRunning},
		ContinuationCount: 1, MaxAutoContinuations: 8, WakeAt: wakeAt,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3) from runs", columns: makeColumns(10), rows: [][]driver.Value{{"thread-mysql", int64(4), RunStatusRunning, int64(1), int64(8), int64(0), nil, nil, nil, dbNow}}},
		longHorizonScriptStep{kind: "query", contains: "from run_continuation_checkpoints where id", columns: makeColumns(10)},
		longHorizonScriptStep{kind: "query", contains: "coalesce(max(sequence)", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "exec", contains: "insert into run_continuation_checkpoints", affected: 1, check: func(query string, args []driver.NamedValue) error {
			if !strings.Contains(strings.ToLower(query), "execution_snapshot_json") || len(args) != 10 ||
				fmt.Sprint(args[7].Value) != string(mutation.Checkpoint.ExecutionSnapshotJSON) {
				return fmt.Errorf("execution snapshot was not bound to checkpoint insert: query=%s args=%#v", query, args)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "exec", contains: "update runs set status", err: errors.New("run update failed")},
	)
	_, _, changed, err := NewMySQL(db).SuspendRun(context.Background(), mutation)
	if err == nil || changed {
		t.Fatalf("SuspendRun changed=%v err=%v, want rollback error", changed, err)
	}
	state.mu.Lock()
	rolledBack, committed := state.rolledBack, state.committed
	state.mu.Unlock()
	if !rolledBack || committed {
		t.Fatalf("transaction rolledBack=%v committed=%v", rolledBack, committed)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLSuspendRunFixedIDReplayIsIdempotent(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	wakeAt := now.Add(time.Minute)
	checkpoint := ContinuationCheckpoint{
		ID: "checkpoint-mysql-replay", RunID: "run-mysql", ThreadID: "thread-mysql", Sequence: 2,
		HistoryJSON:           jsonRaw(`[{"role":"assistant","content":"waiting"}]`),
		ExecutionSnapshotJSON: jsonRaw(`{"dispatch_ordinal":2}`), DependencyIDs: []string{"job-1"}, CreatedAt: now,
	}
	mutation := SuspendRunMutation{
		Checkpoint: checkpoint, ExpectedRevision: 6, ExpectedStatuses: []string{RunStatusRunning},
		ContinuationCount: 2, MaxAutoContinuations: 8, WakeAt: wakeAt,
	}
	dependencies := []byte(`["job-1"]`)
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3) from runs", columns: makeColumns(10), rows: [][]driver.Value{{"thread-mysql", int64(7), RunStatusWaitingExternal, int64(2), int64(8), int64(2), wakeAt, nil, nil, now}}},
		longHorizonScriptStep{kind: "query", contains: "from run_continuation_checkpoints where id", columns: makeColumns(10), rows: [][]driver.Value{{checkpoint.ID, checkpoint.RunID, checkpoint.ThreadID, int64(2), int64(0), []byte(checkpoint.HistoryJSON), nil, []byte(checkpoint.ExecutionSnapshotJSON), dependencies, now}}},
	)
	got, run, changed, err := NewMySQL(db).SuspendRun(context.Background(), mutation)
	if err != nil || changed || got.ID != checkpoint.ID || run.Revision != 7 {
		t.Fatalf("idempotent SuspendRun changed=%v checkpoint=%#v run=%#v err=%v", changed, got, run, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLSuspendRunRequiresResumeClaimTokenForResumingWorker(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	mutation := SuspendRunMutation{
		Checkpoint: ContinuationCheckpoint{
			ID: "checkpoint-unfenced-resume", RunID: "run-resuming", ThreadID: "thread-1",
			HistoryJSON: jsonRaw(`[{"role":"assistant","content":"waiting"}]`), CreatedAt: now,
		},
		ExpectedRevision: 5, ExpectedStatuses: []string{RunStatusResuming},
		ContinuationCount: 2, MaxAutoContinuations: 8, WakeAt: now.Add(time.Minute),
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3) from runs", columns: makeColumns(10), rows: [][]driver.Value{{"thread-1", int64(5), RunStatusResuming, int64(2), int64(8), int64(1), nil, "claim-token", now.Add(time.Minute), now}}},
		longHorizonScriptStep{kind: "query", contains: "from run_continuation_checkpoints where id", columns: makeColumns(10)},
	)
	_, _, changed, err := NewMySQL(db).SuspendRun(context.Background(), mutation)
	if err != nil || changed {
		t.Fatalf("unfenced resuming SuspendRun changed=%v err=%v", changed, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLTransitionRunCASFencesResumeTokenAndDBExpiry(t *testing.T) {
	t.Parallel()
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select revision, status from runs", columns: makeColumns(2), rows: [][]driver.Value{{int64(9), RunStatusResuming}}},
		longHorizonScriptStep{kind: "exec", contains: "resume_lease_expires_at>utc_timestamp(3)", affected: 0, check: func(query string, args []driver.NamedValue) error {
			if !strings.Contains(strings.ToLower(query), "resume_lease_holder=?") || len(args) == 0 || args[len(args)-1].Value != "unique-token" {
				return fmt.Errorf("transition is not claim-token fenced: query=%s args=%#v", query, args)
			}
			return nil
		}},
	)
	_, changed, err := NewMySQL(db).TransitionRunCAS(context.Background(), RunStateMutation{
		RunID: "run-1", ExpectedRevision: 9, ExpectedStatuses: []string{RunStatusResuming},
		ExpectedResumeLeaseHolder: "unique-token", Status: RunStatusWaitingUser, ClearResumeLease: true,
	})
	if err != nil || changed {
		t.Fatalf("expired/mismatched fenced transition changed=%v err=%v", changed, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReleaseRunWakeRejectsExpiredOrFormerHolder(t *testing.T) {
	t.Parallel()
	db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
		kind: "exec", contains: "update runs set status", affected: 0,
		check: func(query string, args []driver.NamedValue) error {
			normalized := strings.ToLower(query)
			if !strings.Contains(normalized, "status in ('running','resuming')") ||
				!strings.Contains(normalized, "resume_lease_holder=?") ||
				!strings.Contains(normalized, "resume_lease_expires_at>utc_timestamp(3)") {
				return fmt.Errorf("release is not status/token/expiry fenced: %s", query)
			}
			if len(args) != 4 || args[3].Value != "former-token" {
				return fmt.Errorf("release claimant args=%#v", args)
			}
			return nil
		},
	})
	wakeAt := time.Now().UTC().Add(time.Minute)
	err := NewMySQL(db).ReleaseRunWake(context.Background(), "run-1", "former-token", RunStatusResumePending, &wakeAt)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("ReleaseRunWake err=%v, want ErrConflict", err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLClaimRunWakeUsesDatabaseClockAndExpiredStealPreservesCount(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	leaseExpired := dbNow.Add(-time.Second)
	callerNow := dbNow.Add(24 * time.Hour)
	leaseUntil := callerNow.Add(2 * time.Minute)
	updated := Run{
		ID: "run-expired", ThreadID: "thread-1", Status: RunStatusResuming, Revision: 10,
		ProviderID: "anthropic", Model: "claude", SystemPrompt: "system", MaxIterations: 20,
		ContinuationCount: 4, MaxAutoContinuations: 8, LastCheckpointSequence: 3,
		ResumeLeaseHolder: "new-token", ResumeLeaseExpiresAt: timePointer(dbNow.Add(2 * time.Minute)),
		CreatedAt: dbNow.Add(-time.Hour), UpdatedAt: dbNow,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3)", columns: makeColumns(8), rows: [][]driver.Value{{int64(9), RunStatusResuming, int64(4), int64(8), nil, "old-token", leaseExpired, dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "resume_lease_expires_at=timestampadd", affected: 1, check: assertNoApplicationTimeArgs},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", columns: makeColumns(38), rows: [][]driver.Value{testMySQLRunRow(updated)}},
	)
	claimed, ok, err := NewMySQL(db).ClaimRunWake(context.Background(), updated.ID, 9, "new-token", callerNow, leaseUntil)
	if err != nil || !ok || claimed.ContinuationCount != 4 || claimed.Revision != 10 {
		t.Fatalf("expired steal claimed=%v run=%#v err=%v", ok, claimed, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLClaimRunWakeStartsAndReclaimsInitialExecutionWithoutIncrementingCount(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	leaseUntil := dbNow.Add(time.Minute)
	running := Run{
		ID: "run-initial", ThreadID: "thread-1", Status: RunStatusRunning, Revision: 2,
		ProviderID: "anthropic", Model: "claude", SystemPrompt: "system", MaxIterations: 20,
		ContinuationCount: 0, MaxAutoContinuations: 8, ResumeLeaseHolder: "initial-token",
		ResumeLeaseExpiresAt: &leaseUntil, CreatedAt: dbNow.Add(-time.Minute), UpdatedAt: dbNow,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3)", columns: makeColumns(8), rows: [][]driver.Value{{int64(1), RunStatusQueued, int64(0), int64(8), nil, nil, nil, dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "resume_lease_holder is null", affected: 1, check: assertNoApplicationTimeArgs},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", columns: makeColumns(38), rows: [][]driver.Value{testMySQLRunRow(running)}},
	)
	claimed, ok, err := NewMySQL(db).ClaimRunWake(context.Background(), running.ID, 1, "initial-token", dbNow.Add(24*time.Hour), dbNow.Add(24*time.Hour+time.Minute))
	if err != nil || !ok || claimed.Status != RunStatusRunning || claimed.ContinuationCount != 0 || claimed.Revision != 2 {
		t.Fatalf("initial claim ok=%v run=%#v err=%v", ok, claimed, err)
	}
	assertLongHorizonScriptDone(t, state)

	expiredAt := dbNow.Add(-time.Second)
	reclaimed := running
	reclaimed.Revision = 3
	reclaimed.ResumeLeaseHolder = "recovery-token"
	reclaimed.ResumeLeaseExpiresAt = &leaseUntil
	db, state = openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3)", columns: makeColumns(8), rows: [][]driver.Value{{int64(2), RunStatusRunning, int64(0), int64(8), nil, "initial-token", expiredAt, dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "resume_lease_expires_at=timestampadd", affected: 1, check: assertNoApplicationTimeArgs},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", columns: makeColumns(38), rows: [][]driver.Value{testMySQLRunRow(reclaimed)}},
	)
	claimed, ok, err = NewMySQL(db).ClaimRunWake(context.Background(), reclaimed.ID, 2, "recovery-token", dbNow.Add(24*time.Hour), dbNow.Add(24*time.Hour+time.Minute))
	if err != nil || !ok || claimed.Status != RunStatusRunning || claimed.ContinuationCount != 0 || claimed.Revision != 3 {
		t.Fatalf("initial reclaim ok=%v run=%#v err=%v", ok, claimed, err)
	}
	assertLongHorizonScriptDone(t, state)

	legacy := running
	legacy.ID = "run-legacy-running"
	legacy.Revision = 6
	legacy.ResumeLeaseHolder = "legacy-recovery"
	legacy.ResumeLeaseExpiresAt = &leaseUntil
	db, state = openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3)", columns: makeColumns(8), rows: [][]driver.Value{{int64(5), RunStatusRunning, int64(0), int64(8), nil, nil, nil, dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "updated_at<=timestampadd", affected: 1, check: assertNoApplicationTimeArgs},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", columns: makeColumns(38), rows: [][]driver.Value{testMySQLRunRow(legacy)}},
	)
	claimed, ok, err = NewMySQL(db).ClaimRunWake(context.Background(), legacy.ID, 5, "legacy-recovery", dbNow.Add(24*time.Hour), dbNow.Add(24*time.Hour+time.Minute))
	if err != nil || !ok || claimed.Status != RunStatusRunning || claimed.ContinuationCount != 0 || claimed.Revision != 6 {
		t.Fatalf("legacy initial reclaim ok=%v run=%#v err=%v", ok, claimed, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLClaimRunWakeReconcilesAmbiguousCommitAndTransientPostRead(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	leaseUntil := dbNow.Add(time.Minute)
	running := Run{
		ID: "run-ambiguous-commit", ThreadID: "thread-1", Status: RunStatusRunning, Revision: 2,
		ProviderID: "anthropic", Model: "claude", SystemPrompt: "system", MaxIterations: 20,
		ContinuationCount: 0, MaxAutoContinuations: 8, ResumeLeaseHolder: "initial-token",
		ResumeLeaseExpiresAt: &leaseUntil, CreatedAt: dbNow.Add(-time.Minute), UpdatedAt: dbNow,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3)", columns: makeColumns(8), rows: [][]driver.Value{{int64(1), RunStatusQueued, int64(0), int64(8), nil, nil, nil, dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "resume_lease_holder is null", affected: 1, check: assertNoApplicationTimeArgs},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", err: errors.New("transient post-commit read failure")},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", columns: makeColumns(38), rows: [][]driver.Value{testMySQLRunRow(running)}},
	)
	state.mu.Lock()
	state.commitErr = errors.New("ambiguous commit acknowledgement")
	state.mu.Unlock()
	claimed, ok, err := NewMySQL(db).ClaimRunWake(context.Background(), running.ID, 1, "initial-token", dbNow, leaseUntil)
	if err != nil || !ok || claimed.Status != RunStatusRunning || claimed.ResumeLeaseHolder != "initial-token" || claimed.Revision != 2 {
		t.Fatalf("ambiguous commit claim ok=%v run=%#v err=%v", ok, claimed, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLClaimRunWakeLegacyContinuationLimitFallsBackToEight(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	wakeAt := dbNow.Add(-time.Second)
	leaseExpiry := dbNow.Add(time.Minute)
	updated := Run{
		ID: "run-legacy-limit", ThreadID: "thread-1", Status: RunStatusResuming, Revision: 3,
		ProviderID: "anthropic", Model: "claude", SystemPrompt: "system", MaxIterations: 20,
		ContinuationCount: 4, MaxAutoContinuations: 0, LastCheckpointSequence: 1,
		ResumeLeaseHolder: "legacy-token", ResumeLeaseExpiresAt: &leaseExpiry,
		CreatedAt: dbNow.Add(-time.Hour), UpdatedAt: dbNow,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "utc_timestamp(3)", columns: makeColumns(8), rows: [][]driver.Value{{int64(2), RunStatusWaitingExternal, int64(3), int64(0), wakeAt, nil, nil, dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "continuation_count=continuation_count+1", affected: 1, check: assertNoApplicationTimeArgs},
		longHorizonScriptStep{kind: "query", contains: "from runs where id", columns: makeColumns(38), rows: [][]driver.Value{testMySQLRunRow(updated)}},
	)
	claimed, ok, err := NewMySQL(db).ClaimRunWake(context.Background(), updated.ID, 2, "legacy-token", dbNow.Add(24*time.Hour), dbNow.Add(24*time.Hour+time.Minute))
	if err != nil || !ok || claimed.Status != RunStatusResuming || claimed.ContinuationCount != 4 {
		t.Fatalf("legacy fallback claim ok=%v run=%#v err=%v", ok, claimed, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLListRecoverableRunsUsesDatabaseClock(t *testing.T) {
	t.Parallel()
	db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
		kind: "query", contains: "from runs where", columns: []string{"id"},
		check: func(query string, args []driver.NamedValue) error {
			if strings.Count(strings.ToLower(query), "utc_timestamp(3)") < 2 {
				return fmt.Errorf("recoverable query does not use DB clock: %s", query)
			}
			if !strings.Contains(strings.ToLower(query), "status in ('running','resuming')") {
				return fmt.Errorf("recoverable query omits expired initial execution leases: %s", query)
			}
			if len(args) != 3 || args[0].Value != -legacyRunningRecoveryAge.Microseconds() ||
				args[1].Value != -queuedRunRecoveryAge.Microseconds() {
				return fmt.Errorf("recoverable query passed application time args: %#v", args)
			}
			return nil
		},
	})
	got, err := NewMySQL(db).ListRecoverableRuns(context.Background(), time.Now().Add(365*24*time.Hour), 25)
	if err != nil || len(got) != 0 {
		t.Fatalf("ListRecoverableRuns=%#v err=%v", got, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyRollsBackSuccessorWhenDetachFails(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	oldRow := []driver.Value{"run-1", "job-old", true, false, QueryValidationUnvalidated, nil, nil, now.Add(-time.Hour), now.Add(-time.Hour)}
	wantObligation, _ := QueryObligationIDForRoot("run-1", "job-old")
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		testMySQLReplacementJobLockStep("job-old", jobs.JobStatusFailed, "thread-1", "run-1", "", now),
		testMySQLReplacementJobLockStep("job-new", jobs.JobStatusRunning, "thread-1", "run-1", "job-old", now),
		longHorizonScriptStep{kind: "exec", contains: "update run_query_dependencies set detached=1", err: errors.New("detach failed"), check: func(_ string, args []driver.NamedValue) error {
			if len(args) < 5 || fmt.Sprint(args[3].Value) != "run-1" || fmt.Sprint(args[4].Value) != "job-old" {
				return fmt.Errorf("detach did not target only explicit old dependency: %#v", args)
			}
			if fmt.Sprint(args[1].Value) != wantObligation {
				return fmt.Errorf("detach did not backfill the derived obligation id: %#v", args)
			}
			return nil
		}},
	)
	_, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-1", JobID: "job-new", RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
		CreatedAt: now, UpdatedAt: now,
	})
	if err == nil {
		t.Fatal("ReplaceRunQueryDependency succeeded despite detach failure")
	}
	state.mu.Lock()
	rolledBack, committed := state.rolledBack, state.committed
	state.mu.Unlock()
	if !rolledBack || committed {
		t.Fatalf("replacement transaction rolledBack=%v committed=%v", rolledBack, committed)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyRejectsMissingAncestorWithoutMutation(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	oldRow := []driver.Value{
		"run-1", "job-old", true, false, QueryValidationRejected,
		nil, nil, now.Add(-time.Hour), now,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		testMySQLReplacementJobLockStep("job-old", jobs.JobStatusFailed, "thread-1", "run-1", "job-missing-ancestor", now),
		testMySQLReplacementJobLockStep("job-new", jobs.JobStatusRunning, "thread-1", "run-1", "job-old", now),
		longHorizonScriptStep{kind: "query", contains: "from query_jobs where id=? for update", columns: makeColumns(len(testMySQLJobRow(jobs.JobRecord{})))},
	)

	_, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-1", JobID: "job-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("missing-ancestor replacement err=%v, want ErrConflict", err)
	}
	state.mu.Lock()
	rolledBack, committed := state.rolledBack, state.committed
	state.mu.Unlock()
	if !rolledBack || committed {
		t.Fatalf("missing-ancestor transaction rolledBack=%v committed=%v, want rollback with no mutation", rolledBack, committed)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyTreatsLegacySentinelParentAsAbsent(t *testing.T) {
	t.Parallel()
	for _, sentinel := range []string{"nil", "null", "00000-00000", "qj-0000_0000"} {
		t.Run(sentinel, func(t *testing.T) {
			t.Parallel()
			now := time.Now().UTC()
			oldRow := []driver.Value{
				"run-1", "qj-old", true, false, QueryValidationRejected,
				nil, sentinel, now.Add(-time.Hour), now,
			}
			wantObligation, _ := QueryObligationIDForRoot("run-1", "qj-old")
			db, state := openLongHorizonScriptDB(t,
				longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
				longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
				longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
				testMySQLReplacementJobLockStep("qj-old", jobs.JobStatusFailed, "thread-1", "run-1", sentinel, now),
				testMySQLReplacementJobLockStep("qj-new", jobs.JobStatusRunning, "thread-1", "run-1", "qj-old", now),
				longHorizonScriptStep{kind: "exec", contains: "update run_query_dependencies set detached=1", affected: 1},
				longHorizonScriptStep{kind: "exec", contains: "insert into run_query_dependencies", affected: 1},
			)
			created, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "qj-old", RunQueryDependency{
				RunID: "run-1", JobID: "qj-new", RequiredForCompletion: true,
				ValidationState: QueryValidationUnvalidated, CreatedAt: now, UpdatedAt: now,
			})
			if err != nil || created.JobID != "qj-new" || created.ObligationID != wantObligation {
				t.Fatalf("sentinel %q replacement=%#v err=%v, want root obligation %q", sentinel, created, err, wantObligation)
			}
			state.mu.Lock()
			committed, rolledBack := state.committed, state.rolledBack
			state.mu.Unlock()
			if !committed || rolledBack {
				t.Fatalf("sentinel %q replacement committed=%v rolledBack=%v", sentinel, committed, rolledBack)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}

func TestMySQLReplaceRunQueryDependencyQuarantinesMalformedManagedAncestor(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	oldRow := []driver.Value{
		"run-1", "qj-old", true, false, QueryValidationRejected,
		nil, nil, now.Add(-time.Hour), now,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		testMySQLReplacementJobLockStep("qj-old", jobs.JobStatusFailed, "thread-1", "run-1", "qj-ancestor", now),
		testMySQLReplacementJobLockStep("qj-new", jobs.JobStatusRunning, "thread-1", "run-1", "qj-old", now),
		testMySQLReplacementJobLockStep("qj-ancestor", jobs.JobStatusFailed, "thread-1", "run-1", "qj-_malformed", now),
	)

	_, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "qj-old", RunQueryDependency{
		RunID: "run-1", JobID: "qj-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrQuarantinedQueryJobIdentity) {
		t.Fatalf("malformed managed ancestor err=%v, want ErrQuarantinedQueryJobIdentity", err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if committed || !rolledBack {
		t.Fatalf("malformed managed ancestor committed=%v rolledBack=%v, want no mutation", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyDoesNotMaskActiveObligationConflict(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	oldRow := []driver.Value{"run-1", "job-old", true, false, QueryValidationRejected, nil, nil, now.Add(-time.Hour), now}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		testMySQLReplacementJobLockStep("job-old", jobs.JobStatusFailed, "thread-1", "run-1", "", now),
		testMySQLReplacementJobLockStep("job-new", jobs.JobStatusRunning, "thread-1", "run-1", "job-old", now),
		longHorizonScriptStep{kind: "exec", contains: "update run_query_dependencies set detached=1", affected: 1},
		longHorizonScriptStep{kind: "exec", contains: "insert into run_query_dependencies", err: errors.New("duplicate active query obligation"), check: func(query string, _ []driver.NamedValue) error {
			if strings.Contains(strings.ToLower(query), "on duplicate key") {
				return errors.New("active-obligation conflicts must not update an unrelated winner")
			}
			return nil
		}},
	)
	_, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-1", JobID: "job-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, CreatedAt: now, UpdatedAt: now,
	})
	if err == nil {
		t.Fatal("active obligation uniqueness conflict was masked")
	}
	state.mu.Lock()
	rolledBack, committed := state.rolledBack, state.committed
	state.mu.Unlock()
	if !rolledBack || committed {
		t.Fatalf("conflicting replacement rolledBack=%v committed=%v", rolledBack, committed)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLUpsertRunQueryDependencyAcceptsEquivalentConcurrentWinner(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	obligationID, _ := QueryObligationIDForRoot("run-1", "job-root")
	equivalent := []driver.Value{
		"run-1", "job-root", true, false, QueryValidationUnvalidated,
		obligationID, nil, now, now,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		longHorizonScriptStep{kind: "exec", contains: "insert into run_query_dependencies", err: errors.New("Error 1062: Duplicate entry for PRIMARY")},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{equivalent}},
	)
	got, err := NewMySQL(db).UpsertRunQueryDependency(context.Background(), RunQueryDependency{
		RunID: "run-1", JobID: "job-root", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, ObligationID: obligationID,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || got.JobID != "job-root" || got.ObligationID != obligationID || got.Detached {
		t.Fatalf("equivalent concurrent winner=%#v err=%v", got, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLUpsertRunQueryDependencyRejectsDifferentActiveWinner(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	obligationID, _ := QueryObligationIDForRoot("run-1", "job-root")
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		longHorizonScriptStep{kind: "exec", contains: "insert into run_query_dependencies", err: errors.New("Error 1062: Duplicate entry for uniq_run_query_active_obligation")},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
	)
	_, err := NewMySQL(db).UpsertRunQueryDependency(context.Background(), RunQueryDependency{
		RunID: "run-1", JobID: "job-root", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, ObligationID: obligationID,
		CreatedAt: now, UpdatedAt: now,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("different active winner err=%v, want ErrConflict", err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyIdempotentReplayAndWrongRun(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	obligationID, _ := QueryObligationIDForRoot("run-1", "job-old")
	oldRow := []driver.Value{"run-1", "job-old", true, true, QueryValidationUnvalidated, obligationID, "job-new", now.Add(-time.Hour), now}
	replacementRow := []driver.Value{"run-1", "job-new", true, false, QueryValidationUnvalidated, obligationID, nil, now, now}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{replacementRow}},
		testMySQLReplacementJobLockStep("job-old", jobs.JobStatusFailed, "thread-1", "run-1", "", now),
		testMySQLReplacementJobLockStep("job-new", jobs.JobStatusRunning, "thread-1", "run-1", "job-old", now),
	)
	replayed, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-1", JobID: "job-new", RequiredForCompletion: true, ValidationState: QueryValidationUnvalidated,
	})
	if err != nil || replayed.JobID != "job-new" || replayed.Detached || !replayed.RequiredForCompletion {
		t.Fatalf("idempotent replacement=%#v err=%v", replayed, err)
	}
	assertLongHorizonScriptDone(t, state)
	if _, err := NewMySQL(nil).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-other", JobID: "job-new", RequiredForCompletion: true,
	}); err == nil {
		t.Fatal("wrong-run replacement unexpectedly succeeded")
	}
}

func legacyDetachedReplayMySQLSteps(now time.Time, oldObligation, successorObligation driver.Value) []longHorizonScriptStep {
	oldRow := []driver.Value{
		"run-1", "job-old", true, true, QueryValidationRejected,
		oldObligation, "job-new", now.Add(-time.Hour), now,
	}
	successorRow := []driver.Value{
		"run-1", "job-new", true, false, QueryValidationUnvalidated,
		successorObligation, nil, now, now,
	}
	return []longHorizonScriptStep{
		{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{successorRow}},
		testMySQLReplacementJobLockStep("job-old", jobs.JobStatusFailed, "thread-1", "run-1", "", now),
		testMySQLReplacementJobLockStep("job-new", jobs.JobStatusRunning, "thread-1", "run-1", "job-old", now),
	}
}

func checkLegacyObligationHydrationArgs(wantObligation, wantJobID string) func(string, []driver.NamedValue) error {
	return func(_ string, args []driver.NamedValue) error {
		if len(args) < 4 || fmt.Sprint(args[0].Value) != wantObligation || fmt.Sprint(args[2].Value) != "run-1" || fmt.Sprint(args[3].Value) != wantJobID {
			return fmt.Errorf("unexpected legacy obligation hydration args: %#v", args)
		}
		return nil
	}
}

func TestMySQLReplaceRunQueryDependencyHydratesLegacyDetachedReplay(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	wantObligation, _ := QueryObligationIDForRoot("run-1", "job-old")
	for _, test := range []struct {
		name                string
		oldObligation       driver.Value
		successorObligation driver.Value
		updateOld           bool
		updateSuccessor     bool
	}{
		{name: "both null", updateOld: true, updateSuccessor: true},
		{name: "predecessor null", successorObligation: wantObligation, updateOld: true},
		{name: "successor null", oldObligation: wantObligation, updateSuccessor: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			steps := legacyDetachedReplayMySQLSteps(now, test.oldObligation, test.successorObligation)
			if test.updateOld {
				steps = append(steps, longHorizonScriptStep{
					kind: "exec", contains: "detached=1 and replacement_job_id=?", affected: 1,
					check: checkLegacyObligationHydrationArgs(wantObligation, "job-old"),
				})
			}
			if test.updateSuccessor {
				steps = append(steps, longHorizonScriptStep{
					kind: "exec", contains: "required_for_completion=1 and detached=0", affected: 1,
					check: checkLegacyObligationHydrationArgs(wantObligation, "job-new"),
				})
			}
			db, state := openLongHorizonScriptDB(t, steps...)
			replayed, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
				RunID: "run-1", JobID: "job-new", RequiredForCompletion: true,
				ValidationState: QueryValidationUnvalidated,
			})
			if err != nil || replayed.JobID != "job-new" || replayed.ObligationID != wantObligation || replayed.Detached || !replayed.RequiredForCompletion {
				t.Fatalf("legacy detached replay=%#v err=%v, want hydrated active successor", replayed, err)
			}
			state.mu.Lock()
			committed, rolledBack := state.committed, state.rolledBack
			state.mu.Unlock()
			if !committed || rolledBack {
				t.Fatalf("legacy hydration committed=%v rolledBack=%v, want one committed transaction", committed, rolledBack)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}

func TestMySQLReplaceRunQueryDependencyRollsBackPartialLegacyHydration(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	wantObligation, _ := QueryObligationIDForRoot("run-1", "job-old")
	steps := legacyDetachedReplayMySQLSteps(now, nil, nil)
	steps = append(steps,
		longHorizonScriptStep{
			kind: "exec", contains: "detached=1 and replacement_job_id=?", affected: 1,
			check: checkLegacyObligationHydrationArgs(wantObligation, "job-old"),
		},
		longHorizonScriptStep{
			kind: "exec", contains: "required_for_completion=1 and detached=0",
			err:   errors.New("Error 1062: Duplicate entry for uniq_run_query_active_obligation"),
			check: checkLegacyObligationHydrationArgs(wantObligation, "job-new"),
		},
	)
	db, state := openLongHorizonScriptDB(t, steps...)
	_, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-1", JobID: "job-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("partial hydration err=%v, want ErrConflict", err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if committed || !rolledBack {
		t.Fatalf("partial hydration committed=%v rolledBack=%v, want full rollback", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyRejectsMismatchedLegacyObligations(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	wantObligation, _ := QueryObligationIDForRoot("run-1", "job-old")
	otherObligation, _ := QueryObligationIDForRoot("run-1", "job-other-root")
	db, state := openLongHorizonScriptDB(t, legacyDetachedReplayMySQLSteps(now, wantObligation, otherObligation)...)
	_, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-1", "job-old", RunQueryDependency{
		RunID: "run-1", JobID: "job-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched obligation replay err=%v, want ErrConflict", err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if committed || !rolledBack {
		t.Fatalf("mismatched replay committed=%v rolledBack=%v, want no mutation", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLReplaceRunQueryDependencyAcceptsAttachedSameThreadForeignLineage(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	oldRow := []driver.Value{"run-current", "job-old", true, false, QueryValidationRejected, nil, nil, now.Add(-time.Hour), now}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select thread_id from runs", columns: makeColumns(1), rows: [][]driver.Value{{"thread-1"}}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9), rows: [][]driver.Value{oldRow}},
		longHorizonScriptStep{kind: "query", contains: "from run_query_dependencies", columns: makeColumns(9)},
		testMySQLReplacementJobLockStep("job-old", jobs.JobStatusFailed, "thread-1", "run-old", "", now),
		testMySQLReplacementJobLockStep("job-new", jobs.JobStatusRunning, "thread-1", "run-foreign-successor", "job-old", now),
		longHorizonScriptStep{kind: "exec", contains: "update run_query_dependencies set detached=1", affected: 1, check: func(_ string, args []driver.NamedValue) error {
			if len(args) < 5 || fmt.Sprint(args[3].Value) != "run-current" || fmt.Sprint(args[4].Value) != "job-old" {
				return fmt.Errorf("detach crossed the current-run dependency boundary: %#v", args)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "exec", contains: "insert into run_query_dependencies", affected: 1},
	)
	replacement, err := NewMySQL(db).ReplaceRunQueryDependency(context.Background(), "run-current", "job-old", RunQueryDependency{
		RunID: "run-current", JobID: "job-new", RequiredForCompletion: true,
		ValidationState: QueryValidationUnvalidated, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil || replacement.JobID != "job-new" || replacement.Detached || !replacement.RequiredForCompletion {
		t.Fatalf("foreign-run replacement=%#v err=%v", replacement, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLQueryJobIdempotencyLookupUsesCanonicalUniqueIdentity(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	remoteSubmittedAt := now.Add(time.Second)
	nextPollAt := now.Add(4 * time.Second)
	cancelStartedAt := now.Add(2 * time.Second)
	estimated := int64(2048)
	record := jobs.JobRecord{
		ID: "job-original", Provider: "bigquery", ConnectionID: "warehouse-a", Status: jobs.JobStatusSubmitted,
		MonitorHealth: jobs.MonitorHealthOK, PolicyHealth: jobs.PolicyHealthOK, MaterializationState: jobs.MaterializationNone,
		ValidationState: jobs.ValidationUnvalidated, Owner: jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1"},
		IdempotencyKey: "stable-call", SubmittedAt: now, RemoteSubmittedAt: &remoteSubmittedAt, NextPollAt: &nextPollAt, NextPollAction: jobs.RetryActionReconcile,
		CancelStartedAt:    &cancelStartedAt,
		DrainRowsCommitted: 5, DrainBytesCommitted: 2048, DrainTruncated: true,
		LastAccess: now, Revision: 4,
		Profile:        &jobs.QueryProfile{Valid: true, EstimatedBytes: &estimated, PlanSummary: "partition scan"},
		ProfileOutcome: jobs.ProfileOutcomeAccepted, ProfileHandle: jobs.NativeHandle{Provider: "bigquery", ID: "dry-run-1"},
		ProfilePhase: "complete", ProfileDispatched: true,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?", columns: makeColumns(len(strings.Split(queryJobSelectColumns, ","))), rows: [][]driver.Value{testMySQLJobRow(record)}, check: func(query string, args []driver.NamedValue) error {
			if len(args) != 3 || args[0].Value != "bigquery" || args[1].Value != "warehouse-a" || args[2].Value != "stable-call" {
				return fmt.Errorf("non-canonical lookup args: %#v", args)
			}
			if !strings.Contains(strings.ToLower(query), "order by submitted_at, id limit 1") {
				return fmt.Errorf("lookup has no deterministic winner order: %s", query)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
	)
	got, ok, err := NewMySQL(db).GetQueryJobByIdempotency(context.Background(), " BIGQUERY ", " Warehouse-A ", "stable-call")
	if err != nil || !ok || got.ID != record.ID || got.Revision != record.Revision || got.Profile == nil ||
		got.Profile.EstimatedBytes == nil || *got.Profile.EstimatedBytes != estimated || got.ProfileOutcome != record.ProfileOutcome ||
		got.ProfileHandle.ID != record.ProfileHandle.ID || got.ProfilePhase != record.ProfilePhase || !got.ProfileDispatched ||
		got.RemoteSubmittedAt == nil || !got.RemoteSubmittedAt.Equal(remoteSubmittedAt) || got.NextPollAt == nil || !got.NextPollAt.Equal(nextPollAt) ||
		got.NextPollAction != record.NextPollAction || got.CancelStartedAt == nil || !got.CancelStartedAt.Equal(cancelStartedAt) ||
		got.DrainRowsCommitted != record.DrainRowsCommitted || got.DrainBytesCommitted != record.DrainBytesCommitted || !got.DrainTruncated {
		t.Fatalf("GetQueryJobByIdempotency=%#v ok=%v err=%v", got, ok, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLListActiveQueryJobsIncludesProfiling(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "job-active-profile", Provider: "bigquery", ConnectionID: "warehouse-a", Status: jobs.JobStatusProfiling,
		MonitorHealth: jobs.MonitorHealthOK, PolicyHealth: jobs.PolicyHealthOK, MaterializationState: jobs.MaterializationNone,
		ValidationState: jobs.ValidationUnvalidated, Owner: jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1"},
		SubmittedAt: now, LastAccess: now, Revision: 1,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "select id from query_jobs where execution_state in", columns: []string{"id"}, rows: [][]driver.Value{{record.ID}}, check: func(query string, _ []driver.NamedValue) error {
			if !strings.Contains(strings.ToLower(query), "'profiling'") {
				return fmt.Errorf("active listing omits profiling: %s", query)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "query", contains: "from query_jobs where id=?", columns: makeColumns(len(strings.Split(queryJobSelectColumns, ","))), rows: [][]driver.Value{testMySQLJobRow(record)}},
		longHorizonScriptStep{kind: "query", contains: "from query_job_events", columns: makeColumns(4)},
	)
	active, err := NewMySQL(db).ListActiveQueryJobs(context.Background())
	if err != nil || len(active) != 1 || active[0].ID != record.ID || active[0].Status != jobs.JobStatusProfiling {
		t.Fatalf("active=%#v err=%v", active, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLApplyProfileMutationTripleFencedAtomicTerminalCommit(t *testing.T) {
	t.Parallel()
	dbNow := time.Date(2026, 7, 12, 16, 0, 0, 0, time.UTC)
	leaseExpires := dbNow.Add(time.Minute)
	dispatched := true
	handle := jobs.NativeHandle{Provider: "bigquery", ID: "profile-native", Location: "EU"}
	phase := "dry_run_complete"
	estimated := int64(8192)
	profile := jobs.QueryProfile{Valid: false, Error: "unknown column", EstimatedBytes: &estimated}
	outcome := jobs.ProfileOutcomeInvalid
	errorDetail := "unknown column"
	failed := jobs.JobStatusFailed
	cancelReason := jobs.CancelReasonWrongQuery
	cancelOutcome := jobs.CancelOutcomeBeforeSubmit
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(5), rows: [][]driver.Value{{int64(7), string(jobs.JobStatusProfiling), "holder-a", leaseExpires, dbNow}}},
		longHorizonScriptStep{kind: "query", contains: "select wake_kind from query_job_events", columns: []string{"wake_kind"}, rows: [][]driver.Value{{string(jobs.WakeSoftQueue)}}},
		longHorizonScriptStep{kind: "query", contains: "coalesce(max(sequence)", columns: makeColumns(1), rows: [][]driver.Value{{int64(2)}}},
		longHorizonScriptStep{kind: "exec", contains: "insert into query_job_events", affected: 1},
		longHorizonScriptStep{kind: "exec", contains: "update query_capacity_reservations", affected: 1, check: func(query string, _ []driver.NamedValue) error {
			if !strings.Contains(strings.ToLower(query), "utc_timestamp(3)") {
				return fmt.Errorf("capacity release does not use DB clock: %s", query)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "exec", contains: "update query_jobs set", affected: 1, check: func(query string, args []driver.NamedValue) error {
			normalized := strings.ToLower(query)
			for _, required := range []string{
				"revision=?", "execution_state='profiling'", "monitor_lease_holder=?",
				"monitor_lease_expires_at>utc_timestamp(3)", "query_profile=?", "profile_outcome=?",
				"profile_handle=?", "profile_phase=?", "profile_dispatched=?", "completed_at=?",
			} {
				if !strings.Contains(normalized, required) {
					return fmt.Errorf("profile update missing %q: %s", required, query)
				}
			}
			if len(args) < 3 || args[len(args)-3].Value != "job-profile" || args[len(args)-2].Value != int64(7) || args[len(args)-1].Value != "holder-a" {
				return fmt.Errorf("profile fence args=%#v", args)
			}
			return nil
		}},
	)
	result, err := NewMySQL(db).ApplyProfileMutation(context.Background(), "job-profile", "holder-a", 7, jobs.ProfileMutation{
		Dispatched: &dispatched, Handle: &handle, Phase: &phase, Profile: &profile, Outcome: &outcome, Error: &errorDetail,
		TransitionTo: &failed, CancelReason: &cancelReason, CancelOutcome: &cancelOutcome,
		Wakes: []jobs.WakeSpec{
			{Kind: jobs.WakeSoftQueue, Detail: "already exists"},
			{Kind: jobs.WakeTerminal, Detail: errorDetail},
			{Kind: jobs.WakeTerminal, Detail: "duplicate kind"},
		},
	})
	if err != nil || result.NewRevision != 8 || !result.At.Equal(dbNow) || result.CompletedAt == nil ||
		!result.CompletedAt.Equal(dbNow) || !result.CapacityReleased || len(result.AppendedWakes) != 1 ||
		result.AppendedWakes[0].Kind != jobs.WakeTerminal {
		t.Fatalf("ApplyProfileMutation=%#v err=%v", result, err)
	}
	state.mu.Lock()
	committed := state.committed
	state.mu.Unlock()
	if !committed {
		t.Fatal("profile transaction did not commit")
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLCommitDrainObservationFencedUsesFinalLeaseAndRevisionGuard(t *testing.T) {
	t.Parallel()
	databaseNow := time.Date(2026, 7, 12, 16, 30, 0, 0, time.UTC)
	commit := testMySQLFencedDrainCommit(databaseNow)
	db, state := openLongHorizonScriptDB(t, testMySQLFencedDrainWriteSteps(commit, databaseNow, databaseNow.Add(time.Minute), 1)...)
	newRevision, err := NewMySQL(db).CommitDrainObservationFenced(context.Background(), commit, "holder-drain")
	if err != nil || newRevision != commit.Record.Revision+1 {
		t.Fatalf("CommitDrainObservationFenced revision=%d err=%v", newRevision, err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if !committed || rolledBack {
		t.Fatalf("fenced drain committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLCommitDrainObservationFencedRejectsInitialFencesBeforeWrites(t *testing.T) {
	t.Parallel()
	databaseNow := time.Date(2026, 7, 12, 16, 45, 0, 0, time.UTC)
	base := testMySQLFencedDrainCommit(databaseNow)
	tests := []struct {
		name         string
		commit       jobs.DrainCommit
		currentRev   int64
		currentDrain int64
		holder       string
		expires      time.Time
		want         error
	}{
		{name: "revision", commit: base, currentRev: base.Record.Revision + 1, holder: "holder-drain", expires: databaseNow.Add(time.Minute), want: jobs.ErrRevisionConflict},
		{name: "drain-sequence", commit: func() jobs.DrainCommit {
			value := base
			value.Seq = 2
			value.Record.DrainSeq = 2
			return value
		}(), currentRev: base.Record.Revision, holder: "holder-drain", expires: databaseNow.Add(time.Minute), want: jobs.ErrRevisionConflict},
		{name: "holder", commit: base, currentRev: base.Record.Revision, holder: "former-holder", expires: databaseNow.Add(time.Minute), want: jobs.ErrLeaseLost},
		{name: "expired", commit: base, currentRev: base.Record.Revision, holder: "holder-drain", expires: databaseNow.Add(-time.Millisecond), want: jobs.ErrLeaseLost},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := tc.commit.Record
			current.Revision = tc.currentRev
			current.DrainSeq = tc.currentDrain
			current.Status = jobs.JobStatusRunning
			current.DrainRowsCommitted = 0
			current.DrainBytesCommitted = 0
			current.DrainTruncated = false
			db, state := openLongHorizonScriptDB(t,
				longHorizonScriptStep{kind: "query", contains: "from query_jobs where id=? for update", columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)}},
				longHorizonScriptStep{kind: "query", contains: "monitor_lease_holder, monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(3), rows: [][]driver.Value{{tc.holder, tc.expires, databaseNow}}},
			)
			_, err := NewMySQL(db).CommitDrainObservationFenced(context.Background(), tc.commit, "holder-drain")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			state.mu.Lock()
			committed, rolledBack := state.committed, state.rolledBack
			state.mu.Unlock()
			if committed || !rolledBack {
				t.Fatalf("rejected fence committed=%v rolledBack=%v", committed, rolledBack)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}

func TestMySQLCommitDrainObservationFencedRollsBackPreparedEffectsWhenFinalLeaseGuardFails(t *testing.T) {
	t.Parallel()
	databaseNow := time.Date(2026, 7, 12, 17, 0, 0, 0, time.UTC)
	commit := testMySQLFencedDrainCommit(databaseNow)
	db, state := openLongHorizonScriptDB(t, testMySQLFencedDrainWriteSteps(commit, databaseNow, databaseNow.Add(time.Minute), 0)...)
	_, err := NewMySQL(db).CommitDrainObservationFenced(context.Background(), commit, "holder-drain")
	if !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("err=%v, want ErrLeaseLost", err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if committed || !rolledBack {
		t.Fatalf("failed final fence committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLCommitDrainObservationRejectsNonMonotonicAccountingAndTerminalAppend(t *testing.T) {
	t.Parallel()
	databaseNow := time.Date(2026, 7, 12, 17, 15, 0, 0, time.UTC)
	base := testMySQLFencedDrainCommit(databaseNow)
	badAccounting := base
	badAccounting.DrainRowsCommitted = 2
	badAccounting.Record.DrainRowsCommitted = 2
	tests := []struct {
		name   string
		commit jobs.DrainCommit
		status jobs.JobStatus
		want   error
	}{
		{name: "accounting", commit: badAccounting, status: jobs.JobStatusRunning, want: ErrConflict},
		{name: "terminal", commit: base, status: jobs.JobStatusCompleted, want: jobs.ErrRevisionConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			current := base.Record
			current.DrainSeq = 0
			current.Status = tc.status
			current.DrainRowsCommitted = 0
			current.DrainBytesCommitted = 0
			current.DrainTruncated = false
			db, state := openLongHorizonScriptDB(t,
				longHorizonScriptStep{kind: "query", contains: "from query_jobs where id=? for update", columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)}},
				longHorizonScriptStep{kind: "query", contains: "monitor_lease_holder, monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(3), rows: [][]driver.Value{{"holder-drain", databaseNow.Add(time.Minute), databaseNow}}},
			)
			_, err := NewMySQL(db).CommitDrainObservationFenced(context.Background(), tc.commit, "holder-drain")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			state.mu.Lock()
			committed, rolledBack := state.committed, state.rolledBack
			state.mu.Unlock()
			if committed || !rolledBack {
				t.Fatalf("rejected commit committed=%v rolledBack=%v", committed, rolledBack)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}

func TestMySQLCommitDrainObservationTerminalReplayRemainsIdempotent(t *testing.T) {
	t.Parallel()
	databaseNow := time.Date(2026, 7, 12, 17, 30, 0, 0, time.UTC)
	commit := testMySQLFencedDrainCommit(databaseNow)
	pageJSON, _ := json.Marshal(commit.Page)
	cursorJSON, _ := json.Marshal(commit.Handle)
	current := commit.Record
	current.Revision = 8
	current.Status = jobs.JobStatusCompleted
	current.Handle = commit.Handle
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{
			kind: "query", contains: "from query_jobs where id=? for update", columns: makeColumns(len(testMySQLJobRow(current))), rows: [][]driver.Value{testMySQLJobRow(current)},
		},
		longHorizonScriptStep{kind: "query", contains: "monitor_lease_holder, monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(3), rows: [][]driver.Value{{"holder-drain", databaseNow.Add(time.Minute), databaseNow}}},
		longHorizonScriptStep{
			kind: "query", contains: "from query_result_chunks where job_id=? and sequence=?", columns: makeColumns(9),
			rows: [][]driver.Value{{commit.JobID, commit.Seq, pageJSON, cursorJSON, nil, int64(len(commit.Page.Rows)), commit.Page.Bytes, commit.Page.Truncated, databaseNow}},
		},
	)
	revision, err := NewMySQL(db).CommitDrainObservationFenced(context.Background(), commit, "holder-drain")
	if err != nil || revision != 8 {
		t.Fatalf("terminal replay revision=%d err=%v", revision, err)
	}
	state.mu.Lock()
	committed, rolledBack := state.committed, state.rolledBack
	state.mu.Unlock()
	if !committed || rolledBack {
		t.Fatalf("terminal replay committed=%v rolledBack=%v", committed, rolledBack)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLApplyProfileMutationRejectsRevisionStatusAndLeaseFences(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	tests := []struct {
		name       string
		revision   int64
		status     jobs.JobStatus
		holder     driver.Value
		leaseUntil driver.Value
		want       error
	}{
		{name: "revision", revision: 8, status: jobs.JobStatusProfiling, holder: "holder", leaseUntil: dbNow.Add(time.Minute), want: jobs.ErrRevisionConflict},
		{name: "status", revision: 7, status: jobs.JobStatusReserved, holder: "holder", leaseUntil: dbNow.Add(time.Minute), want: jobs.ErrRevisionConflict},
		{name: "holder", revision: 7, status: jobs.JobStatusProfiling, holder: "former", leaseUntil: dbNow.Add(time.Minute), want: jobs.ErrLeaseLost},
		{name: "expired", revision: 7, status: jobs.JobStatusProfiling, holder: "holder", leaseUntil: dbNow.Add(-time.Second), want: jobs.ErrLeaseLost},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			db, state := openLongHorizonScriptDB(t, longHorizonScriptStep{
				kind: "query", contains: "monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(5),
				rows: [][]driver.Value{{tc.revision, string(tc.status), tc.holder, tc.leaseUntil, dbNow}},
			})
			phase := "profile"
			_, err := NewMySQL(db).ApplyProfileMutation(context.Background(), "job", "holder", 7, jobs.ProfileMutation{Phase: &phase})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err=%v, want %v", err, tc.want)
			}
			assertLongHorizonScriptDone(t, state)
		})
	}
}

func TestMySQLApplyProfileMutationFinalUpdateRetainsExpiryFence(t *testing.T) {
	t.Parallel()
	dbNow := time.Now().UTC()
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "monitor_lease_expires_at, utc_timestamp(3)", columns: makeColumns(5), rows: [][]driver.Value{{int64(7), string(jobs.JobStatusProfiling), "holder", dbNow.Add(time.Minute), dbNow}}},
		longHorizonScriptStep{kind: "exec", contains: "monitor_lease_expires_at>utc_timestamp(3)", affected: 0},
	)
	phase := "profile"
	_, err := NewMySQL(db).ApplyProfileMutation(context.Background(), "job", "holder", 7, jobs.ProfileMutation{Phase: &phase})
	if !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("err=%v, want ErrLeaseLost", err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLCapacityResolvesIdempotencyBeforeAuthoritativeCounts(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "job-replay", Provider: "bigquery", ConnectionID: "warehouse", Status: jobs.JobStatusProfiling,
		Owner: jobs.JobOwner{ThreadID: "thread", RunID: "run"}, IdempotencyKey: "stable-call",
		SubmittedAt: now, LastAccess: now,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "from query_capacity_gate", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "query", contains: "where id=? for update", columns: makeColumns(1), rows: [][]driver.Value{{int64(0)}}},
		longHorizonScriptStep{kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?", columns: []string{"id"}, rows: [][]driver.Value{{"job-original"}}},
	)
	granted, _, err := NewMySQL(db).ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{
		MaxGlobal: 1, MaxPerThread: 1, MaxPerConnection: 1,
		MaxProfilingGlobal: 1, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 1,
	})
	if granted || !errors.Is(err, jobs.ErrRevisionConflict) {
		t.Fatalf("granted=%v err=%v, want idempotency conflict before capacity", granted, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func TestMySQLCapacityUsesAuthoritativeProfilingJobCounts(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "job-profile-new", Provider: "bigquery", ConnectionID: "warehouse", Status: jobs.JobStatusProfiling,
		Owner: jobs.JobOwner{ThreadID: "thread", RunID: "run"}, IdempotencyKey: "fresh-call",
		SubmittedAt: now, LastAccess: now,
	}
	db, state := openLongHorizonScriptDB(t,
		longHorizonScriptStep{kind: "query", contains: "from query_capacity_gate", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "query", contains: "where id=? for update", columns: makeColumns(1), rows: [][]driver.Value{{int64(0)}}},
		longHorizonScriptStep{kind: "query", contains: "where provider=? and connection_key=? and idempotency_key=?", columns: []string{"id"}},
		longHorizonScriptStep{kind: "query", contains: "execution_state in ('reserved','profiling'", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}, check: func(query string, _ []driver.NamedValue) error {
			if strings.Contains(strings.ToLower(query), "query_capacity_reservations") {
				return fmt.Errorf("capacity count still trusts reservation sidecars: %s", query)
			}
			return nil
		}},
		longHorizonScriptStep{kind: "query", contains: "and thread_id=?", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "query", contains: "and connection_key=?", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "query", contains: "execution_state='profiling'", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "query", contains: "execution_state='profiling' and thread_id=?", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
		longHorizonScriptStep{kind: "query", contains: "execution_state='profiling' and connection_key=?", columns: makeColumns(1), rows: [][]driver.Value{{int64(1)}}},
	)
	granted, _, err := NewMySQL(db).ReserveQueryCapacity(context.Background(), record, jobs.CapacityLimits{
		MaxGlobal: 10, MaxPerThread: 10, MaxPerConnection: 10,
		MaxProfilingGlobal: 1, MaxProfilingPerThread: 1, MaxProfilingPerConnection: 1,
	})
	if err != nil || granted {
		t.Fatalf("granted=%v err=%v, want nested profiling rejection", granted, err)
	}
	assertLongHorizonScriptDone(t, state)
}

func makeColumns(count int) []string {
	columns := make([]string, count)
	for i := range columns {
		columns[i] = fmt.Sprintf("c%d", i)
	}
	return columns
}

func jsonRaw(value string) []byte { return []byte(value) }

func timePointer(value time.Time) *time.Time { return &value }

func assertNoApplicationTimeArgs(_ string, args []driver.NamedValue) error {
	for _, arg := range args {
		if _, ok := arg.Value.(time.Time); ok {
			return fmt.Errorf("SQL passed application wall clock: %#v", args)
		}
	}
	return nil
}
