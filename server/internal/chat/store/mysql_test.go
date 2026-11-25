package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// artifactInsertErrDriver is a stub database/sql driver whose connections fail
// every Exec with a canned error selected by the DSN. It lets the MySQL store's
// error mapping be exercised without a live server: persistToolResult's
// async-query placeholder upgrade only triggers on ErrConflict, so duplicate
// artifact ids must not surface as raw driver errors.
type artifactInsertErrDriver struct{}

func (artifactInsertErrDriver) Open(mode string) (driver.Conn, error) {
	return artifactInsertErrConn{mode: mode}, nil
}

type artifactInsertErrConn struct{ mode string }

func (artifactInsertErrConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}

func (artifactInsertErrConn) Close() error { return nil }

func (artifactInsertErrConn) Begin() (driver.Tx, error) {
	return nil, errors.New("begin not supported")
}

func (c artifactInsertErrConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.mode == "duplicate" {
		return nil, errors.New("Error 1062 (23000): Duplicate entry 'art-async-1' for key 'artifacts.PRIMARY'")
	}
	return nil, errors.New("invalid connection")
}

func init() {
	sql.Register("artifact-insert-err", artifactInsertErrDriver{})
	sql.Register("approval-store-test", approvalStoreTestDriver{})
}

func TestMySQLInsertArtifactMapsDuplicateKeyToErrConflict(t *testing.T) {
	db, err := sql.Open("artifact-insert-err", "duplicate")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	s := NewMySQL(db)
	_, err = s.InsertArtifact(context.Background(), Artifact{
		ID:       "art-async-1",
		ThreadID: "thread-1",
		Type:     "data",
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate artifact insert err = %v, want ErrConflict", err)
	}
}

func TestMySQLInsertArtifactPassesThroughNonDuplicateErrors(t *testing.T) {
	db, err := sql.Open("artifact-insert-err", "transient")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()

	s := NewMySQL(db)
	_, err = s.InsertArtifact(context.Background(), Artifact{
		ID:       "art-async-1",
		ThreadID: "thread-1",
		Type:     "data",
	})
	if err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("non-duplicate insert err = %v, want the raw driver error", err)
	}
}

func TestIsDuplicateKeyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"mysql 1062", errors.New("Error 1062 (23000): Duplicate entry 'art-1' for key 'artifacts.PRIMARY'"), true},
		{"plain duplicate", errors.New("duplicate entry"), true},
		{"unrelated", errors.New("invalid connection"), false},
	}
	for _, tc := range cases {
		if got := isDuplicateKeyError(tc.err); got != tc.want {
			t.Fatalf("%s: isDuplicateKeyError(%v) = %v, want %v", tc.name, tc.err, got, tc.want)
		}
	}
}

func TestMySQLApprovalRequestLifecyclePersistsJSONFields(t *testing.T) {
	ctx := context.Background()
	db, state := openApprovalStoreTestDB(t)
	st := NewMySQL(db)
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	expiresAt := createdAt.Add(10 * time.Minute)
	state.addRun(Run{ID: "run-approval-1", ThreadID: "thread-approval-1"})

	created, err := st.CreateApprovalRequest(ctx, ApprovalRequest{
		ID:         "apr_lifecycle",
		ThreadID:   "thread-approval-1",
		RunID:      "run-approval-1",
		ToolCallID: "call_1",
		Tool:       "github_create_pull_request",
		Category:   "write",
		Operation:  "create_pr",
		Subject: PermissionSubject{
			Kind:       "remote_resource",
			Connector:  "github",
			Repository: "openai/codex",
			RemoteKind: "pull_request",
			RemoteID:   "123",
			Operation:  "write",
		},
		Summary:   "Create a pull request",
		Preview:   map[string]any{"title": "Fix permissions", "changed_files": 2},
		Options:   []string{"thread", "once", "thread"},
		CreatedAt: createdAt,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("CreateApprovalRequest: %v", err)
	}
	if created.Status != "pending" {
		t.Fatalf("created.Status=%q, want pending", created.Status)
	}
	if _, err := st.CreateApprovalRequest(ctx, created); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate CreateApprovalRequest err=%v, want ErrConflict", err)
	}

	got, err := st.GetApprovalRequest(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest: %v", err)
	}
	if got.ToolCallID != "call_1" || got.Operation != "create_pr" {
		t.Fatalf("tool call/operation = %q/%q, want call_1/create_pr", got.ToolCallID, got.Operation)
	}
	if got.Subject.Kind != "remote_resource" || got.Subject.Connector != "github" || got.Subject.Repository != "openai/codex" || got.Subject.RemoteID != "123" {
		t.Fatalf("subject=%#v, want persisted remote resource subject", got.Subject)
	}
	if got.Preview["title"] != "Fix permissions" || got.Preview["changed_files"] != float64(2) {
		t.Fatalf("preview=%#v, want JSON round-tripped preview", got.Preview)
	}
	if strings.Join(got.Options, ",") != "thread,once" {
		t.Fatalf("options=%v, want deduped persisted options", got.Options)
	}

	listed, err := st.ListApprovalRequests(ctx, ApprovalFilter{
		ThreadID: "thread-approval-1",
		RunID:    "run-approval-1",
		Status:   "pending",
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("ListApprovalRequests: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed approvals=%#v, want only %s", listed, created.ID)
	}

	decidedAt := createdAt.Add(time.Minute)
	approved, err := st.DecideApprovalRequest(ctx, created.ID, ApprovalDecision{
		Action:    "APPROVE",
		Scope:     "THREAD",
		DecidedAt: decidedAt,
	}, "APPROVED")
	if err != nil {
		t.Fatalf("DecideApprovalRequest approve: %v", err)
	}
	if approved.Status != "approved" || approved.Decision == nil || approved.Decision.Action != "approve" || approved.Decision.Scope != "thread" {
		t.Fatalf("approved=%#v, want normalized approved thread decision", approved)
	}
	if !approved.Decision.DecidedAt.Equal(decidedAt.UTC()) {
		t.Fatalf("DecidedAt=%s, want %s", approved.Decision.DecidedAt, decidedAt.UTC())
	}

	again, err := st.DecideApprovalRequest(ctx, created.ID, ApprovalDecision{
		Action: "reject",
		Scope:  "once",
	}, "rejected")
	if err != nil {
		t.Fatalf("DecideApprovalRequest second decision: %v", err)
	}
	if again.Status != "approved" || again.Decision == nil || again.Decision.Action != "approve" {
		t.Fatalf("second decision changed approval=%#v, want original approved decision", again)
	}
}

func TestMySQLApprovalRequestBulkCancelAndExpireOnlyPendingMatches(t *testing.T) {
	ctx := context.Background()
	db, state := openApprovalStoreTestDB(t)
	st := NewMySQL(db)
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	state.addRun(Run{ID: "run-cancel", ThreadID: "thread-cancel"})
	state.addRun(Run{ID: "run-expire", ThreadID: "thread-expire"})

	cancelMe := createMySQLApprovalForTest(t, st, ApprovalRequest{
		ID:        "apr_cancel_me",
		ThreadID:  "thread-cancel",
		RunID:     "run-cancel",
		Tool:      "rlm_exec",
		Category:  "execute",
		Subject:   PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:   "Run command",
		CreatedAt: now.Add(-time.Minute),
		ExpiresAt: now.Add(time.Hour),
	})
	approved := createMySQLApprovalForTest(t, st, ApprovalRequest{
		ID:        "apr_already_approved",
		ThreadID:  "thread-cancel",
		RunID:     "run-cancel",
		Tool:      "rlm_exec",
		Category:  "execute",
		Subject:   PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:   "Already approved",
		CreatedAt: now.Add(-2 * time.Minute),
		ExpiresAt: now.Add(time.Hour),
	})
	if _, err := st.DecideApprovalRequest(ctx, approved.ID, ApprovalDecision{Action: "approve", Scope: "once", DecidedAt: now.Add(-time.Second)}, "approved"); err != nil {
		t.Fatalf("DecideApprovalRequest approved seed: %v", err)
	}
	stale := createMySQLApprovalForTest(t, st, ApprovalRequest{
		ID:        "apr_stale",
		ThreadID:  "thread-expire",
		RunID:     "run-expire",
		Tool:      "read_file",
		Category:  "read",
		Subject:   PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project"},
		Summary:   "Read file",
		CreatedAt: now.Add(-20 * time.Minute),
		ExpiresAt: now.Add(-time.Second),
	})
	future := createMySQLApprovalForTest(t, st, ApprovalRequest{
		ID:        "apr_future",
		ThreadID:  "thread-expire",
		RunID:     "run-expire",
		Tool:      "read_file",
		Category:  "read",
		Subject:   PermissionSubject{Kind: "local_location", LocalRoot: "/tmp/project/README.md"},
		Summary:   "Read future file",
		CreatedAt: now,
		ExpiresAt: now.Add(time.Hour),
	})

	if err := st.CancelRunApprovalRequests(ctx, "run-cancel"); err != nil {
		t.Fatalf("CancelRunApprovalRequests: %v", err)
	}
	gotCancel, err := st.GetApprovalRequest(ctx, cancelMe.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest canceled: %v", err)
	}
	if gotCancel.Status != "canceled" || gotCancel.Decision == nil || gotCancel.Decision.Action != "cancel" {
		t.Fatalf("canceled approval=%#v, want cancel decision", gotCancel)
	}
	gotApproved, err := st.GetApprovalRequest(ctx, approved.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest approved: %v", err)
	}
	if gotApproved.Status != "approved" || gotApproved.Decision == nil || gotApproved.Decision.Action != "approve" {
		t.Fatalf("approved approval=%#v, want unchanged approved decision", gotApproved)
	}

	if err := st.ExpireStaleApprovalRequests(ctx, now); err != nil {
		t.Fatalf("ExpireStaleApprovalRequests: %v", err)
	}
	gotStale, err := st.GetApprovalRequest(ctx, stale.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest stale: %v", err)
	}
	if gotStale.Status != "expired" || gotStale.Decision == nil || gotStale.Decision.Action != "expire" || !gotStale.Decision.DecidedAt.Equal(now) {
		t.Fatalf("stale approval=%#v, want expired decision at %s", gotStale, now)
	}
	gotFuture, err := st.GetApprovalRequest(ctx, future.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest future: %v", err)
	}
	if gotFuture.Status != "pending" {
		t.Fatalf("future status=%q, want pending", gotFuture.Status)
	}
}

func createMySQLApprovalForTest(t *testing.T, st *MySQLStore, approval ApprovalRequest) ApprovalRequest {
	t.Helper()
	created, err := st.CreateApprovalRequest(context.Background(), approval)
	if err != nil {
		t.Fatalf("CreateApprovalRequest(%s): %v", approval.ID, err)
	}
	return created
}

type approvalStoreTestDriver struct{}

var approvalStoreTestStates sync.Map

func (approvalStoreTestDriver) Open(name string) (driver.Conn, error) {
	state, ok := approvalStoreTestStates.Load(name)
	if !ok {
		return nil, fmt.Errorf("approval-store-test state %q not found", name)
	}
	return &approvalStoreTestConn{state: state.(*approvalStoreTestState)}, nil
}

type approvalStoreTestState struct {
	mu        sync.Mutex
	runs      map[string]Run
	approvals map[string]ApprovalRequest
}

func openApprovalStoreTestDB(t *testing.T) (*sql.DB, *approvalStoreTestState) {
	t.Helper()
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()) + fmt.Sprintf("_%d", time.Now().UnixNano())
	state := &approvalStoreTestState{
		runs:      map[string]Run{},
		approvals: map[string]ApprovalRequest{},
	}
	approvalStoreTestStates.Store(name, state)
	db, err := sql.Open("approval-store-test", name)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		approvalStoreTestStates.Delete(name)
	})
	return db, state
}

func (s *approvalStoreTestState) addRun(run Run) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if run.Status == "" {
		run.Status = "queued"
	}
	if run.ProviderID == "" {
		run.ProviderID = "compatible"
	}
	if run.Model == "" {
		run.Model = "model"
	}
	if run.SystemPrompt == "" {
		run.SystemPrompt = "system"
	}
	if run.MaxIterations == 0 {
		run.MaxIterations = 4
	}
	if run.Purpose == "" {
		run.Purpose = "analysis"
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = run.CreatedAt
	}
	s.runs[run.ID] = run
}

type approvalStoreTestConn struct {
	state *approvalStoreTestState
}

func (c *approvalStoreTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}

func (c *approvalStoreTestConn) Close() error { return nil }

func (c *approvalStoreTestConn) Begin() (driver.Tx, error) {
	return approvalStoreTestTx{}, nil
}

func (c *approvalStoreTestConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return approvalStoreTestTx{}, nil
}

func (c *approvalStoreTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.state.query(query, args)
}

func (c *approvalStoreTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.state.exec(query, args)
}

type approvalStoreTestTx struct{}

func (approvalStoreTestTx) Commit() error   { return nil }
func (approvalStoreTestTx) Rollback() error { return nil }

func (s *approvalStoreTestState) query(query string, args []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(q, "from runs where id=?"):
		run, ok := s.runs[namedString(args, 0)]
		if !ok {
			return approvalRows(approvalRunColumns(), nil), nil
		}
		return approvalRows(approvalRunColumns(), [][]driver.Value{approvalRunValues(run)}), nil
	case strings.Contains(q, "from approval_requests"):
		approvals := s.matchApprovalsLocked(q, args)
		rows := make([][]driver.Value, 0, len(approvals))
		for _, approval := range approvals {
			values, err := approvalRequestValues(approval)
			if err != nil {
				return nil, err
			}
			rows = append(rows, values)
		}
		return approvalRows(approvalRequestColumns(), rows), nil
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
}

func (s *approvalStoreTestState) exec(query string, args []driver.NamedValue) (driver.Result, error) {
	q := strings.ToLower(query)
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case strings.Contains(q, "insert into approval_requests"):
		approval, err := approvalRequestFromInsertArgs(args)
		if err != nil {
			return nil, err
		}
		if _, exists := s.approvals[approval.ID]; exists {
			return nil, errors.New("Error 1062 (23000): Duplicate entry for key 'approval_requests.PRIMARY'")
		}
		s.approvals[approval.ID] = approval
		return approvalStoreTestResult(1), nil
	case strings.Contains(q, "where run_id=? and status='pending'"):
		decision, err := approvalDecisionFromArg(args, 0)
		if err != nil {
			return nil, err
		}
		updatedAt := namedTime(args, 1)
		runID := namedString(args, 2)
		var changed int64
		for id, approval := range s.approvals {
			if approval.RunID == runID && approval.Status == "pending" {
				approval.Status = "canceled"
				approval.Decision = &decision
				approval.UpdatedAt = updatedAt
				s.approvals[id] = approval
				changed++
			}
		}
		return approvalStoreTestResult(changed), nil
	case strings.Contains(q, "where status='pending' and expires_at<=?"):
		decision, err := approvalDecisionFromArg(args, 0)
		if err != nil {
			return nil, err
		}
		updatedAt := namedTime(args, 1)
		cutoff := namedTime(args, 2)
		var changed int64
		for id, approval := range s.approvals {
			if approval.Status == "pending" && !approval.ExpiresAt.After(cutoff) {
				approval.Status = "expired"
				approval.Decision = &decision
				approval.UpdatedAt = updatedAt
				s.approvals[id] = approval
				changed++
			}
		}
		return approvalStoreTestResult(changed), nil
	case strings.Contains(q, "update approval_requests set status=?"):
		status := namedString(args, 0)
		decision, err := approvalDecisionFromArg(args, 1)
		if err != nil {
			return nil, err
		}
		updatedAt := namedTime(args, 2)
		id := namedString(args, 3)
		approval, ok := s.approvals[id]
		if !ok {
			return approvalStoreTestResult(0), nil
		}
		approval.Status = status
		approval.Decision = &decision
		approval.UpdatedAt = updatedAt
		s.approvals[id] = approval
		return approvalStoreTestResult(1), nil
	default:
		return nil, fmt.Errorf("unexpected exec: %s", query)
	}
}

func (s *approvalStoreTestState) matchApprovalsLocked(query string, args []driver.NamedValue) []ApprovalRequest {
	if strings.Contains(query, "where id=?") {
		approval, ok := s.approvals[namedString(args, 0)]
		if !ok {
			return nil
		}
		return []ApprovalRequest{approval}
	}
	arg := 0
	threadID := ""
	runID := ""
	status := ""
	if strings.Contains(query, "thread_id=?") {
		threadID = namedString(args, arg)
		arg++
	}
	if strings.Contains(query, "run_id=?") {
		runID = namedString(args, arg)
		arg++
	}
	if strings.Contains(query, "status=?") {
		status = namedString(args, arg)
		arg++
	}
	limit := int(namedInt64(args, len(args)-1))
	if limit <= 0 {
		limit = 200
	}
	out := []ApprovalRequest{}
	for _, approval := range s.approvals {
		if threadID != "" && approval.ThreadID != threadID {
			continue
		}
		if runID != "" && approval.RunID != runID {
			continue
		}
		if status != "" && approval.Status != status {
			continue
		}
		out = append(out, approval)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func approvalRequestFromInsertArgs(args []driver.NamedValue) (ApprovalRequest, error) {
	var subject PermissionSubject
	if err := json.Unmarshal([]byte(namedString(args, 7)), &subject); err != nil {
		return ApprovalRequest{}, err
	}
	approval := ApprovalRequest{
		ID:         namedString(args, 0),
		ThreadID:   namedString(args, 1),
		RunID:      namedString(args, 2),
		ToolCallID: namedString(args, 3),
		Tool:       namedString(args, 4),
		Category:   namedString(args, 5),
		Operation:  namedString(args, 6),
		Subject:    subject,
		Summary:    namedString(args, 8),
		Preview:    namedMap(args, 9),
		Options:    namedStringSlice(args, 10),
		Status:     namedString(args, 11),
		CreatedAt:  namedTime(args, 12),
		ExpiresAt:  namedTime(args, 13),
		UpdatedAt:  namedTime(args, 14),
	}
	return prepareApprovalRequest(approval)
}

func approvalDecisionFromArg(args []driver.NamedValue, idx int) (ApprovalDecision, error) {
	var decision ApprovalDecision
	if err := json.Unmarshal([]byte(namedString(args, idx)), &decision); err != nil {
		return ApprovalDecision{}, err
	}
	return decision, nil
}

func approvalRequestValues(approval ApprovalRequest) ([]driver.Value, error) {
	subject, err := json.Marshal(approval.Subject)
	if err != nil {
		return nil, err
	}
	preview, err := json.Marshal(approval.Preview)
	if err != nil {
		return nil, err
	}
	options, err := json.Marshal(approval.Options)
	if err != nil {
		return nil, err
	}
	var decision any
	if approval.Decision != nil {
		b, err := json.Marshal(approval.Decision)
		if err != nil {
			return nil, err
		}
		decision = string(b)
	}
	return []driver.Value{
		approval.ID,
		approval.ThreadID,
		approval.RunID,
		emptyStringAsNil(approval.ToolCallID),
		approval.Tool,
		approval.Category,
		emptyStringAsNil(approval.Operation),
		string(subject),
		approval.Summary,
		emptyJSONAsNil(approval.Preview, string(preview)),
		emptyStringSliceAsNil(approval.Options, string(options)),
		approval.Status,
		decision,
		approval.CreatedAt,
		approval.ExpiresAt,
		approval.UpdatedAt,
	}, nil
}

func approvalRequestColumns() []string {
	return []string{"id", "thread_id", "run_id", "tool_call_id", "tool", "category", "operation", "subject", "summary", "preview", "options_json", "status", "decision", "created_at", "expires_at", "updated_at"}
}

func approvalRunColumns() []string {
	return []string{"id", "thread_id", "status", "revision", "provider_id", "connection_id", "plan_id", "model", "profile_id", "system_prompt", "max_iterations", "continuation_count", "max_auto_continuations", "last_checkpoint_sequence", "wake_at", "resume_lease_holder", "resume_lease_expires_at", "request_id", "input_tokens", "output_tokens", "total_tokens", "error_code", "error_message", "purpose", "context_tokens_before", "context_tokens_after", "context_summary_buckets", "context_summary_rounds", "context_artifact_refs_loaded", "context_need_retrieval", "context_need_retrieval_tooling", "context_hard_capped", "context_overflow_compacted", "context_plan", "created_at", "started_at", "ended_at", "updated_at"}
}

func approvalRunValues(run Run) []driver.Value {
	return []driver.Value{
		run.ID,
		run.ThreadID,
		run.Status,
		run.Revision,
		run.ProviderID,
		emptyStringAsNil(run.ConnectionID),
		emptyStringAsNil(run.PlanID),
		run.Model,
		optionalDriverString(run.ProfileID),
		run.SystemPrompt,
		int64(run.MaxIterations),
		int64(run.ContinuationCount),
		int64(run.MaxAutoContinuations),
		run.LastCheckpointSequence,
		nil,
		emptyStringAsNil(run.ResumeLeaseHolder),
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		run.Purpose,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		run.CreatedAt,
		nil,
		nil,
		run.UpdatedAt,
	}
}

func optionalDriverString(value *string) driver.Value {
	if value == nil {
		return nil
	}
	return *value
}

type approvalStoreTestRows struct {
	columns []string
	rows    [][]driver.Value
	next    int
}

func approvalRows(columns []string, rows [][]driver.Value) *approvalStoreTestRows {
	return &approvalStoreTestRows{columns: columns, rows: rows}
}

func (r *approvalStoreTestRows) Columns() []string { return r.columns }

func (r *approvalStoreTestRows) Close() error { return nil }

func (r *approvalStoreTestRows) Next(dest []driver.Value) error {
	if r.next >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.next])
	r.next++
	return nil
}

type approvalStoreTestResult int64

func (r approvalStoreTestResult) LastInsertId() (int64, error) { return 0, nil }
func (r approvalStoreTestResult) RowsAffected() (int64, error) { return int64(r), nil }

func namedString(args []driver.NamedValue, idx int) string {
	if idx < 0 || idx >= len(args) || args[idx].Value == nil {
		return ""
	}
	switch v := args[idx].Value.(type) {
	case string:
		return v
	case []byte:
		return string(v)
	default:
		return fmt.Sprint(v)
	}
}

func namedTime(args []driver.NamedValue, idx int) time.Time {
	if idx < 0 || idx >= len(args) || args[idx].Value == nil {
		return time.Time{}
	}
	switch v := args[idx].Value.(type) {
	case time.Time:
		return v.UTC()
	case string:
		t, _ := time.Parse(time.RFC3339Nano, v)
		return t.UTC()
	default:
		return time.Time{}
	}
}

func namedInt64(args []driver.NamedValue, idx int) int64 {
	if idx < 0 || idx >= len(args) || args[idx].Value == nil {
		return 0
	}
	switch v := args[idx].Value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	default:
		return 0
	}
}

func namedMap(args []driver.NamedValue, idx int) map[string]any {
	raw := namedString(args, idx)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func namedStringSlice(args []driver.NamedValue, idx int) []string {
	raw := namedString(args, idx)
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func emptyStringAsNil(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func emptyJSONAsNil(value map[string]any, encoded string) any {
	if len(value) == 0 {
		return nil
	}
	return encoded
}

func emptyStringSliceAsNil(value []string, encoded string) any {
	if len(value) == 0 {
		return nil
	}
	return encoded
}
