package usage

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type usageCaptureDriver struct{}
type usageCaptureState struct {
	mu      sync.Mutex
	queries []string
	args    [][]driver.NamedValue
	err     error
	queryFn func(string, []driver.NamedValue) (driver.Rows, error)
}
type usageCaptureConn struct{ state *usageCaptureState }

var (
	usageCaptureSequence atomic.Int64
	usageCaptureStates   sync.Map
)

func init() { sql.Register("usage-repository-capture", usageCaptureDriver{}) }
func (usageCaptureDriver) Open(name string) (driver.Conn, error) {
	state, ok := usageCaptureStates.Load(name)
	if !ok {
		return nil, fmt.Errorf("unknown usage capture state %q", name)
	}
	return &usageCaptureConn{state: state.(*usageCaptureState)}, nil
}
func (c *usageCaptureConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}
func (c *usageCaptureConn) Close() error { return nil }
func (c *usageCaptureConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not supported")
}
func (c *usageCaptureConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)
	c.state.args = append(c.state.args, append([]driver.NamedValue(nil), args...))
	if c.state.err != nil {
		return nil, c.state.err
	}
	return driver.RowsAffected(1), nil
}
func (c *usageCaptureConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.queries = append(c.state.queries, query)
	copied := append([]driver.NamedValue(nil), args...)
	c.state.args = append(c.state.args, copied)
	if c.state.err != nil {
		return nil, c.state.err
	}
	if c.state.queryFn != nil {
		return c.state.queryFn(query, copied)
	}
	return &usageCaptureRows{}, nil
}

type usageCaptureRows struct {
	columns []string
	values  [][]driver.Value
	index   int
}

func (r *usageCaptureRows) Columns() []string { return r.columns }
func (r *usageCaptureRows) Close() error      { return nil }
func (r *usageCaptureRows) Next(dest []driver.Value) error {
	if r.index >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.index])
	r.index++
	return nil
}

func TestMySQLRepositoryStartWritesCanonicalInvocation(t *testing.T) {
	db, state := openUsageCaptureDB(t)
	repo := NewMySQLRepository(db)
	startedAt := time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC)
	repo.now = func() time.Time { return startedAt }
	ctx := WithAttribution(context.Background(), Attribution{RunID: "run-1", ThreadID: "thread-1", PlanID: "plan-1", Operation: OperationChat,
		ProviderConfigID: "connection-1", ConfigFingerprint: "sha256:abc", APIType: "responses"})
	created, err := repo.Start(ctx, Invocation{ID: "mysql-invocation-1", ProviderID: "openai", RequestedModel: "gpt-5", SanitizedDetails: map[string]int64{"prompt_audio_tokens": 12}})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created.Status != StatusRunning || !created.StartedAt.Equal(startedAt) || stringValue(created.ConnectionID) != "connection-1" {
		t.Fatalf("created = %#v", created)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.queries) != 1 || !strings.Contains(state.queries[0], "INSERT INTO model_invocations") || !strings.Contains(state.queries[0], "connection_id") {
		t.Fatalf("queries = %#v", state.queries)
	}
	if strings.Contains(state.queries[0], "provider_config_id") || strings.Contains(state.queries[0], "error_message") {
		t.Fatalf("legacy column leaked: %s", state.queries[0])
	}
	if len(state.args) != 1 || len(state.args[0]) != 19 {
		t.Fatalf("args = %#v, want one 19-argument insert", state.args)
	}
}

func TestMySQLRepositoryStartMapsUnverifiableDuplicateToConflict(t *testing.T) {
	db, state := openUsageCaptureDB(t)
	state.err = errors.New("Error 1062 (23000): Duplicate entry 'inv-1' for key 'PRIMARY'")
	_, err := NewMySQLRepository(db).Start(context.Background(), Invocation{ID: "inv-1", ProviderID: "openai", RequestedModel: "gpt-5"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Start duplicate error = %v, want ErrConflict", err)
	}
}

func TestMySQLRepositoryGetScansCanonicalNullableUsage(t *testing.T) {
	db, state := openUsageCaptureDB(t)
	started := time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC)
	ended := started.Add(time.Minute)
	state.queryFn = func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "FROM model_invocations WHERE id=?") {
			t.Fatalf("unexpected query: %s", query)
		}
		return invocationRows([]driver.Value{"mysql-get-1", "run-1", "thread-1", nil, "plan-1", string(OperationChat),
			"openai", "connection-1", nil, "sha256:abc", "responses", "responses", "gpt-5", "gpt-5-2026-06-01",
			string(StatusCompleted), started, ended, ended, int64(2), int64(100), int64(20), int64(121), int64(120), int64(40), nil, int64(5),
			uint64(0), string(QualityProviderReported), string(TokenSemanticsRawCountersV1), int64(2), "req-1", []byte(`{"audio_tokens":3}`), nil}), nil
	}
	got, err := NewMySQLRepository(db).Get(context.Background(), "mysql-get-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ProviderTotalTokens == nil || *got.ProviderTotalTokens != 121 || got.DerivedTotalTokens == nil || *got.DerivedTotalTokens != 120 || got.CacheWriteTokens != nil {
		t.Fatalf("nullable counters = %#v", got)
	}
	if got.SanitizedDetails["audio_tokens"] != 3 || got.DisplayModel() != "gpt-5-2026-06-01" {
		t.Fatalf("details/model = %#v", got)
	}
}

func TestMySQLRepositorySnapshotPersistsSequenceAndLowerBoundMask(t *testing.T) {
	db, state := openUsageCaptureDB(t)
	started := time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC)
	state.queryFn = func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		if !strings.Contains(query, "WHERE id=?") {
			return &usageCaptureRows{columns: invocationTestColumns()}, nil
		}
		return invocationRows(runningInvocationValues("mysql-snapshot", started, int64(0))), nil
	}
	input := int64(11)
	got, err := NewMySQLRepository(db).Snapshot(context.Background(), "mysql-snapshot", UsageSnapshot{Sequence: 1, ObservedAt: started.Add(time.Second), InputTokens: &input, LowerBoundMask: CounterInput, UsageQuality: QualityLegacyPartial, UpstreamRequestCount: 1})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got.SnapshotSequence != 1 || !got.LowerBoundMask.Has(CounterInput) {
		t.Fatalf("snapshot = %#v", got)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.queries) != 2 || !strings.Contains(state.queries[1], "snapshot_sequence=?") || !strings.Contains(state.queries[1], "lower_bound_mask=?") {
		t.Fatalf("queries = %#v", state.queries)
	}
}

func TestMySQLRepositoryFinalizePersistsEffectiveSurface(t *testing.T) {
	db, state := openUsageCaptureDB(t)
	started := time.Date(2026, 7, 10, 12, 30, 0, 0, time.UTC)
	state.queryFn = func(query string, _ []driver.NamedValue) (driver.Rows, error) {
		return invocationRows(runningBedrockValues("mysql-finalize-api", started)), nil
	}
	effectiveAPI := "converse_stream"
	finalized, err := NewMySQLRepository(db).Finalize(context.Background(), "mysql-finalize-api", Finalization{Status: StatusCompleted, EndedAt: started.Add(time.Minute), UsageQuality: QualityMissing, APIType: &effectiveAPI})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if finalized.EffectiveAPISurface == nil || *finalized.EffectiveAPISurface != effectiveAPI {
		t.Fatalf("effective surface = %#v", finalized.EffectiveAPISurface)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.queries) != 2 || !strings.Contains(state.queries[1], "effective_api_surface=?") {
		t.Fatalf("queries = %#v", state.queries)
	}
}

func TestMySQLRepositoryListBuildsBoundedIndexedRangeQuery(t *testing.T) {
	db, state := openUsageCaptureDB(t)
	state.queryFn = func(_ string, _ []driver.NamedValue) (driver.Rows, error) {
		return &usageCaptureRows{columns: invocationTestColumns()}, nil
	}
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	listed, err := NewMySQLRepository(db).List(context.Background(), Query{From: from, To: to, RangeField: RangeByEndedAt, ProviderID: "openai", ResolvedModel: "gpt-5", Limit: 25})
	if err != nil || len(listed) != 0 {
		t.Fatalf("List = %#v, %v", listed, err)
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	query := state.queries[0]
	for _, fragment := range []string{"ended_at >= ?", "ended_at < ?", "provider_id = ?", "COALESCE(NULLIF(resolved_model, ''), requested_model) = ?", "ORDER BY ended_at ASC, id ASC LIMIT ?"} {
		if !strings.Contains(query, fragment) {
			t.Errorf("query missing %q: %s", fragment, query)
		}
	}
	if len(state.args[0]) != 5 {
		t.Fatalf("query args = %#v, want 5", state.args[0])
	}
}

func invocationRows(values []driver.Value) driver.Rows {
	return &usageCaptureRows{columns: invocationTestColumns(), values: [][]driver.Value{values}}
}
func runningInvocationValues(id string, started time.Time, sequence int64) []driver.Value {
	return []driver.Value{id, nil, nil, nil, nil, string(OperationChat), "openai", "connection-1", nil, nil, "responses", nil, "gpt-5", nil, string(StatusRunning), started, nil, started, sequence, nil, nil, nil, nil, nil, nil, nil, uint64(0), string(QualityMissing), string(TokenSemanticsRawCountersV1), int64(0), nil, nil, nil}
}
func runningBedrockValues(id string, started time.Time) []driver.Value {
	values := runningInvocationValues(id, started, 0)
	values[6] = "bedrock"
	values[10] = "invoke_stream"
	values[12] = "claude"
	return values
}

func invocationTestColumns() []string {
	return []string{"id", "run_id", "thread_id", "job_id", "plan_id", "operation", "provider_id", "connection_id", "profile_id", "config_fingerprint", "planned_api_surface", "effective_api_surface", "requested_model", "resolved_model", "status", "started_at", "ended_at", "updated_at", "snapshot_sequence", "input_tokens", "output_tokens", "provider_total_tokens", "derived_total_tokens", "cached_input_tokens", "cache_write_tokens", "reasoning_tokens", "lower_bound_mask", "usage_quality", "token_semantics_profile", "upstream_request_count", "provider_request_id", "sanitized_details", "error_code"}
}

func openUsageCaptureDB(t *testing.T) (*sql.DB, *usageCaptureState) {
	t.Helper()
	name := fmt.Sprintf("capture-%d", usageCaptureSequence.Add(1))
	state := &usageCaptureState{}
	usageCaptureStates.Store(name, state)
	db, err := sql.Open("usage-repository-capture", name)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close(); usageCaptureStates.Delete(name) })
	return db, state
}
