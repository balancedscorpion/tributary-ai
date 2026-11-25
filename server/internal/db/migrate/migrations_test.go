package migrate

import (
	"errors"
	"strings"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestLongHorizonQueryMigrationContainsDurabilityBoundaries(t *testing.T) {
	raw, err := EmbeddedMigrations.ReadFile("migrations/0017_long_horizon_query_orchestration.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	sqlText := strings.ToLower(string(raw))
	for _, required := range []string{
		"waiting_external", "resume_pending", "resuming", "waiting_user",
		"run_continuation_checkpoints", "query_jobs", "query_job_events",
		"query_result_chunks", "run_query_dependencies", "query_capacity_reservations",
		"native_handle", "monitor_health", "policy_health", "materialization_state",
		"cancellation_reason", "cancellation_outcome", "validation_state",
		"monitor_lease_holder", "observation_key", "cursor_after_json", "submit_dispatched",
		"connection_key", "query_capacity_gate", "execution_snapshot_json",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("0017 migration is missing %q", required)
		}
	}
	if statements := splitSQL(sqlText); len(statements) < 7 {
		t.Fatalf("0017 split into %d statements, want all long-horizon tables plus runs alter", len(statements))
	}
	for _, statement := range splitSQL(sqlText) {
		if !strings.HasPrefix(strings.TrimSpace(statement), "alter table runs add") {
			continue
		}
		additiveClauses := strings.Count(statement, "add column") + strings.Count(statement, "add index")
		if additiveClauses != 1 {
			t.Fatalf("replay-safe runs ALTER must contain one additive clause, got %d in %q", additiveClauses, statement)
		}
	}
}

func TestReplaySafeAdditiveDDLErrorIsNarrow(t *testing.T) {
	for _, number := range []uint16{1060, 1061} {
		if !isReplaySafeAdditiveDDLError(&mysqldriver.MySQLError{Number: number, Message: "already exists"}) {
			t.Fatalf("MySQL error %d should be replay-safe", number)
		}
	}
	for _, err := range []error{
		&mysqldriver.MySQLError{Number: 1062, Message: "duplicate data"},
		&mysqldriver.MySQLError{Number: 1213, Message: "deadlock"},
		errors.New("duplicate column name"),
	} {
		if isReplaySafeAdditiveDDLError(err) {
			t.Fatalf("error %v must remain fatal", err)
		}
	}
}

func TestQueryProfilingDurabilityMigrationIsAdditiveAndComplete(t *testing.T) {
	raw, err := EmbeddedMigrations.ReadFile("migrations/0018_query_profiling_durability.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	sqlText := strings.ToLower(string(raw))
	for _, required := range []string{
		"query_profile", "profile_outcome", "profile_handle", "profile_phase", "profile_dispatched",
		"cancel_started_at", "remote_submitted_at", "next_poll_at", "next_poll_action", "idx_query_jobs_next_poll",
		"drain_rows_committed", "drain_bytes_committed", "drain_truncated",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("0018 migration is missing %q", required)
		}
	}
	statements := splitSQL(sqlText)
	if len(statements) != 13 {
		t.Fatalf("0018 split into %d statements, want 13 isolated additive operations", len(statements))
	}
	for _, statement := range statements {
		normalized := strings.TrimSpace(statement)
		if !strings.Contains(normalized, "alter table query_jobs add") {
			t.Fatalf("0018 contains non-additive statement %q", statement)
		}
		if strings.Count(normalized, "add column")+strings.Count(normalized, "add index") != 1 {
			t.Fatalf("0018 statement is not isolated: %q", statement)
		}
	}
}

func TestQueryObligationMigrationIsAdditiveReplaySafeAndComplete(t *testing.T) {
	raw, err := EmbeddedMigrations.ReadFile("migrations/0019_query_obligations.sql")
	if err != nil {
		t.Fatalf("read embedded migration: %v", err)
	}
	sqlText := strings.ToLower(string(raw))
	for _, required := range []string{
		"query_obligation_id varchar(96)",
		"active_query_obligation_id varchar(96)",
		"character set utf8mb4 collate utf8mb4_bin",
		"generated always as",
		"detached=0 and required_for_completion=1",
		"stored",
		"add unique index uniq_run_query_active_obligation (run_id, active_query_obligation_id)",
		"add index idx_run_query_obligation_attempts (run_id, query_obligation_id, created_at, job_id)",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("0019 migration is missing %q", required)
		}
	}

	statements := splitSQL(sqlText)
	if len(statements) != 4 {
		t.Fatalf("0019 split into %d statements, want four isolated additive operations", len(statements))
	}
	for _, statement := range statements {
		normalized := strings.TrimSpace(statement)
		if !strings.Contains(normalized, "alter table run_query_dependencies add") {
			t.Fatalf("0019 contains non-additive statement %q", statement)
		}
		additiveClauses := strings.Count(normalized, "add column") +
			strings.Count(normalized, "add index") +
			strings.Count(normalized, "add unique index")
		if additiveClauses != 1 {
			t.Fatalf("0019 statement is not an isolated replay-safe operation: %q", statement)
		}
	}
}
