package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	mysqldriver "github.com/go-sql-driver/mysql"
)

var leaseSQLRecorder struct {
	sync.Mutex
	query string
	args  []driver.NamedValue
}

type leaseSQLDriver struct{}
type leaseSQLConn struct{}

func (leaseSQLDriver) Open(string) (driver.Conn, error) { return leaseSQLConn{}, nil }
func (leaseSQLConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (leaseSQLConn) Close() error              { return nil }
func (leaseSQLConn) Begin() (driver.Tx, error) { return nil, errors.New("begin unsupported") }
func (leaseSQLConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	leaseSQLRecorder.Lock()
	leaseSQLRecorder.query = query
	leaseSQLRecorder.args = append([]driver.NamedValue(nil), args...)
	leaseSQLRecorder.Unlock()
	return driver.RowsAffected(1), nil
}

func init() { sql.Register("long-horizon-lease-sql", leaseSQLDriver{}) }

func TestMySQLQueryJobWriteContractMatchesColumnContract(t *testing.T) {
	now := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	remoteSubmittedAt := now.Add(time.Second)
	nextPollAt := now.Add(5 * time.Second)
	cancelStartedAt := now.Add(2 * time.Second)
	estimated := int64(4096)
	record := jobs.JobRecord{
		ID: "job_mysql_contract", Provider: "bigquery", ConnectionID: "bq-1", SQL: "SELECT 1",
		Status: jobs.JobStatusReserved, MonitorHealth: jobs.MonitorHealthOK, PolicyHealth: jobs.PolicyHealthOK,
		MaterializationState: jobs.MaterializationNone,
		Owner:                jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "owner-1"},
		IdempotencyKey:       "idem-1", KeepOnRunCompletion: true, SubmitDispatched: true,
		SubmittedAt: now, RemoteSubmittedAt: &remoteSubmittedAt, NextPollAt: &nextPollAt, NextPollAction: jobs.RetryActionConfirmCancel,
		CancelStartedAt: &cancelStartedAt, LastAccess: now,
		DrainRowsCommitted: 7, DrainBytesCommitted: 4096, DrainTruncated: true,
		Profile: &jobs.QueryProfile{Valid: true, EstimatedBytes: &estimated}, ProfileOutcome: jobs.ProfileOutcomeAccepted,
		ProfileHandle: jobs.NativeHandle{Provider: "bigquery", ID: "dry-run"}, ProfilePhase: "dry_run", ProfileDispatched: true,
	}
	values, err := queryJobWriteValues(record, 1)
	if err != nil {
		t.Fatalf("queryJobWriteValues: %v", err)
	}
	columns := strings.Split(queryJobSelectColumns, ",")
	if len(values) != len(columns) {
		t.Fatalf("query job values=%d columns=%d; insert/update order has drifted", len(values), len(columns))
	}
	if got := values[3]; got != "bq-1" {
		t.Fatalf("canonical connection_key=%#v, want bq-1", got)
	}
	columnIndex := map[string]int{}
	for index, column := range columns {
		columnIndex[strings.TrimSpace(column)] = index
	}
	if index := columnIndex["submit_dispatched"]; index == 0 {
		t.Fatal("submit_dispatched is missing from durable columns")
	} else if got, ok := values[index].(bool); !ok || !got {
		t.Fatalf("submit_dispatched value=%#v, want true at its durable column position", values[index])
	}
	for _, field := range []string{"query_profile", "profile_outcome", "profile_handle", "profile_phase", "profile_dispatched", "cancel_started_at", "remote_submitted_at", "next_poll_at", "next_poll_action", "drain_rows_committed", "drain_bytes_committed", "drain_truncated"} {
		index, ok := columnIndex[field]
		if !ok || values[index] == nil {
			t.Fatalf("%s value missing from write contract: columns=%#v values=%#v", field, columns, values)
		}
	}
}

func TestMySQLCapacityRetryClassificationIsNarrow(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		if !isMySQLTransactionRetryable(&mysqldriver.MySQLError{Number: number}) {
			t.Fatalf("MySQL error %d should retry capacity transaction", number)
		}
	}
	for _, err := range []error{
		&mysqldriver.MySQLError{Number: 1062},
		&mysqldriver.MySQLError{Number: 1045},
		errors.New("deadlock text without typed MySQL error"),
	} {
		if isMySQLTransactionRetryable(err) {
			t.Fatalf("error %v must not retry", err)
		}
	}
}

func TestMySQLCapacityGateAndDeadlockRetry(t *testing.T) {
	if normalized := strings.ToLower(queryCapacityGateLockSQL); !strings.Contains(normalized, "query_capacity_gate") || !strings.Contains(normalized, "for update") {
		t.Fatalf("capacity gate is not serialized: %s", queryCapacityGateLockSQL)
	}
	attempts := 0
	revision, err := retryMySQLCapacityTransaction(context.Background(), func() (int64, error) {
		attempts++
		if attempts < 3 {
			return 0, &mysqldriver.MySQLError{Number: 1213, Message: "deadlock"}
		}
		return 7, nil
	})
	if err != nil || revision != 7 || attempts != 3 {
		t.Fatalf("retry revision=%d attempts=%d err=%v, want 7/3/nil", revision, attempts, err)
	}
}

func TestQueryConnectionKeyIsCanonicalAndNeverEmpty(t *testing.T) {
	if got := queryConnectionKey(jobs.JobRecord{Provider: " BigQuery ", ConnectionID: " Warehouse-A "}); got != "warehouse-a" {
		t.Fatalf("explicit connection key=%q, want warehouse-a", got)
	}
	if got := queryConnectionKey(jobs.JobRecord{Provider: " BigQuery "}); got != "bigquery" {
		t.Fatalf("provider fallback connection key=%q, want bigquery", got)
	}
}

func TestMySQLMonitorLeasesUseDatabaseClock(t *testing.T) {
	db, err := sql.Open("long-horizon-lease-sql", "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	store := NewMySQL(db)
	ctx := context.Background()
	assertDatabaseClock := func(operation string) {
		t.Helper()
		leaseSQLRecorder.Lock()
		defer leaseSQLRecorder.Unlock()
		query := strings.ToLower(leaseSQLRecorder.query)
		if !strings.Contains(query, "utc_timestamp(3)") {
			t.Fatalf("%s query does not use database time: %s", operation, leaseSQLRecorder.query)
		}
		for _, arg := range leaseSQLRecorder.args {
			if _, isApplicationTime := arg.Value.(time.Time); isApplicationTime {
				t.Fatalf("%s passed application wall clock to lease SQL: %#v", operation, arg.Value)
			}
		}
	}
	if acquired, err := store.AcquireMonitorLease(ctx, "job-1", "holder", time.Minute); err != nil || !acquired {
		t.Fatalf("AcquireMonitorLease acquired=%v err=%v", acquired, err)
	}
	assertDatabaseClock("acquire")
	if renewed, err := store.RenewMonitorLease(ctx, "job-1", "holder", time.Minute); err != nil || !renewed {
		t.Fatalf("RenewMonitorLease renewed=%v err=%v", renewed, err)
	}
	assertDatabaseClock("renew")
	if err := store.ReleaseMonitorLease(ctx, "job-1", "holder"); err != nil {
		t.Fatalf("ReleaseMonitorLease: %v", err)
	}
	assertDatabaseClock("release")
	if renewed, err := store.RenewRunWakeLease(ctx, "run-1", "unique-claim-token", 2*time.Minute); err != nil || !renewed {
		t.Fatalf("RenewRunWakeLease renewed=%v err=%v", renewed, err)
	}
	assertDatabaseClock("run wake renewal")
	leaseSQLRecorder.Lock()
	runRenewalSQL := strings.ToLower(leaseSQLRecorder.query)
	leaseSQLRecorder.Unlock()
	if !strings.Contains(runRenewalSQL, "status in ('running','resuming')") ||
		!strings.Contains(runRenewalSQL, "resume_lease_expires_at>utc_timestamp(3)") {
		t.Fatalf("run wake renewal is not owner/status/unexpired fenced: %s", runRenewalSQL)
	}
}
