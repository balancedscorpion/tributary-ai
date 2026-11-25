package databricks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/secrets"
)

func newDatabricksLifecycleTestHandler(t *testing.T, fn http.HandlerFunc) (*Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fn)
	t.Cleanup(server.Close)
	secretStore, err := secrets.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := secretStore.Init("test-passphrase"); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := secretStore.Unlock("test-passphrase"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if err := secretStore.SetDatabricksPAT("test-token"); err != nil {
		t.Fatalf("SetDatabricksPAT: %v", err)
	}
	handler := NewHandler(nil, secretStore)
	handler.httpc = server.Client()
	return handler, server
}

func TestSubmitStatementIncludesContinueLimitsAndTags(t *testing.T) {
	var request statementRequest
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/sql/statements" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statement_id": "statement-1",
			"status":       map[string]any{"state": "PENDING"},
		})
	})

	snapshot, err := handler.SubmitStatement(context.Background(), StatementSubmitRequest{
		Host:          server.URL,
		WarehouseID:   "warehouse-1",
		SQL:           "SELECT 1",
		Catalog:       "main",
		Schema:        "analytics",
		WaitTimeout:   2 * time.Minute,
		OnWaitTimeout: "CONTINUE",
		Disposition:   "INLINE",
		Format:        "JSON_ARRAY",
		RowLimit:      2501,
		ByteLimit:     32 << 20,
		QueryTags: []QueryTag{
			{Key: "app_job", Value: "job-1"},
			{Key: "attempt", Value: "2"},
		},
	})
	if err != nil {
		t.Fatalf("SubmitStatement: %v", err)
	}
	if snapshot.Handle.StatementID != "statement-1" || snapshot.Handle.WarehouseID != "warehouse-1" || snapshot.State != "PENDING" || snapshot.Terminal {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if request.WaitTimeout != "50s" || request.OnWaitTimeout != "CONTINUE" || request.Disposition != "INLINE" || request.Format != "JSON_ARRAY" {
		t.Fatalf("execution controls = %#v", request)
	}
	if request.RowLimit != 2501 || request.ByteLimit != MaxInlineStatementByteLimit || request.Catalog != "main" || request.Schema != "analytics" {
		t.Fatalf("limits/scope = %#v", request)
	}
	if len(request.QueryTags) != 2 || request.QueryTags[0].Key != "app_job" {
		t.Fatalf("query tags = %#v", request.QueryTags)
	}
}

func TestNormalizeStatementByteLimitHonorsInlineProviderContract(t *testing.T) {
	tests := []struct {
		name        string
		disposition string
		input       int64
		want        int64
	}{
		{name: "unset inline", disposition: "INLINE", input: 0, want: MaxInlineStatementByteLimit},
		{name: "oversized inline", disposition: "inline", input: 32 << 20, want: MaxInlineStatementByteLimit},
		{name: "small inline", disposition: " INLINE ", input: 1 << 20, want: 1 << 20},
		{name: "external links unchanged", disposition: "EXTERNAL_LINKS", input: 32 << 20, want: 32 << 20},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeStatementByteLimit(tc.disposition, tc.input); got != tc.want {
				t.Fatalf("NormalizeStatementByteLimit(%q, %d)=%d want %d", tc.disposition, tc.input, got, tc.want)
			}
		})
	}
}

func TestSubmitStatementReturnsHTTP200FailedAsTerminalSnapshot(t *testing.T) {
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statement_id": "failed-1",
			"status": map[string]any{
				"state": "FAILED",
				"error": map[string]any{"error_code": "BAD_REQUEST", "message": "bad query", "sql_state": "42601"},
			},
		})
	})
	snapshot, err := handler.SubmitStatement(context.Background(), StatementSubmitRequest{Host: server.URL, WarehouseID: "wh", SQL: "SELECT broken"})
	if err != nil {
		t.Fatalf("SubmitStatement: %v", err)
	}
	if !snapshot.Terminal || snapshot.Succeeded || snapshot.Error == nil || snapshot.Error.ErrorCode != "BAD_REQUEST" || snapshot.Error.SQLState != "42601" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSubmitStatementDoesNotRetryAmbiguousFailure(t *testing.T) {
	var calls atomic.Int32
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error_code":"TEMPORARILY_UNAVAILABLE","message":"retry later"}`, http.StatusServiceUnavailable)
	})
	_, err := handler.SubmitStatement(context.Background(), StatementSubmitRequest{Host: server.URL, WarehouseID: "wh", SQL: "SELECT 1"})
	if err == nil {
		t.Fatal("SubmitStatement returned nil error")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1", calls.Load())
	}
	var apiErr *StatementAPIError
	if !errors.As(err, &apiErr) || !apiErr.Ambiguous || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("error = %#v", err)
	}
}

func TestGetStatementMapsStatesAndClosedResultUnavailable(t *testing.T) {
	tests := []struct {
		id              string
		state           string
		terminal        bool
		succeeded       bool
		cancelled       bool
		closed          bool
		resultAvailable bool
	}{
		{id: "pending", state: "PENDING"},
		{id: "running", state: "RUNNING"},
		{id: "success", state: "SUCCEEDED", terminal: true, succeeded: true, resultAvailable: true},
		{id: "failed", state: "FAILED", terminal: true},
		{id: "cancel", state: "CANCELED", terminal: true, cancelled: true},
		{id: "closed", state: "CLOSED", terminal: true, succeeded: true, closed: true},
	}
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/2.0/sql/statements/")
		var selected *struct {
			id              string
			state           string
			terminal        bool
			succeeded       bool
			cancelled       bool
			closed          bool
			resultAvailable bool
		}
		for i := range tests {
			if tests[i].id == id {
				selected = &tests[i]
				break
			}
		}
		if selected == nil {
			http.NotFound(w, r)
			return
		}
		response := map[string]any{
			"statement_id": id,
			"status":       map[string]any{"state": selected.state},
		}
		if selected.state == "FAILED" {
			response["status"] = map[string]any{"state": "FAILED", "error": map[string]any{"message": "failed"}}
		}
		if selected.state == "SUCCEEDED" {
			response["manifest"] = map[string]any{
				"format": "JSON_ARRAY", "total_chunk_count": 1, "total_row_count": 1, "total_byte_count": 8, "truncated": false,
				"schema": map[string]any{"columns": []map[string]any{{"name": "x", "type_name": "INT", "position": 0}}},
			}
			response["result"] = map[string]any{"chunk_index": 0, "row_offset": 0, "row_count": 1, "byte_count": 8, "data_array": [][]any{{1}}}
		}
		_ = json.NewEncoder(w).Encode(response)
	})

	for _, test := range tests {
		t.Run(test.id, func(t *testing.T) {
			snapshot, err := handler.GetStatement(context.Background(), StatementHandle{Host: server.URL, StatementID: test.id})
			if err != nil {
				t.Fatalf("GetStatement: %v", err)
			}
			if snapshot.Terminal != test.terminal || snapshot.Succeeded != test.succeeded || snapshot.Cancelled != test.cancelled || snapshot.Closed != test.closed || snapshot.ResultAvailable != test.resultAvailable {
				t.Fatalf("snapshot = %#v", snapshot)
			}
			if test.id == "success" {
				if snapshot.Manifest.TotalRowCount != 1 || len(snapshot.Manifest.Columns) != 1 || len(snapshot.FirstChunk.DataArray) != 1 {
					t.Fatalf("manifest/chunk = %#v %#v", snapshot.Manifest, snapshot.FirstChunk)
				}
			}
		})
	}
}

func TestMaterializeStatementResultFollowsNativeChunksExactlyOnce(t *testing.T) {
	var statementGets atomic.Int32
	var chunkGets atomic.Int32
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/2.0/sql/statements/s1":
			statementGets.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statement_id": "s1",
				"status":       map[string]any{"state": "SUCCEEDED"},
				"manifest": map[string]any{
					"format": "JSON_ARRAY", "total_chunk_count": 2, "total_row_count": 3, "total_byte_count": 30,
					"schema": map[string]any{"columns": []map[string]any{{"name": "id", "type_name": "INT"}, {"name": "name", "type_name": "STRING"}}},
					"chunks": []map[string]any{{"chunk_index": 0, "row_offset": 0, "row_count": 2}, {"chunk_index": 1, "row_offset": 2, "row_count": 1}},
				},
				"result": map[string]any{"chunk_index": 0, "row_offset": 0, "row_count": 2, "data_array": [][]any{{1, "a"}, {2, "b"}}, "next_chunk_internal_link": "/api/2.0/sql/statements/s1/result/chunks/1"},
			})
		case "/api/2.0/sql/statements/s1/result/chunks/1":
			chunkGets.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"chunk_index": 1, "row_offset": 2, "row_count": 1, "data_array": [][]any{{3, "c"}}})
		default:
			http.NotFound(w, r)
		}
	})
	snapshot, err := handler.GetStatement(context.Background(), StatementHandle{Host: server.URL, StatementID: "s1"})
	if err != nil {
		t.Fatalf("GetStatement: %v", err)
	}
	materialized, err := handler.MaterializeStatementResult(context.Background(), MaterializeStatementRequest{
		Handle:    snapshot.Handle,
		Snapshot:  &snapshot,
		MaxRows:   2500,
		MaxBytes:  32 << 20,
		MaxChunks: 10,
	})
	if err != nil {
		t.Fatalf("MaterializeStatementResult: %v", err)
	}
	if statementGets.Load() != 1 || chunkGets.Load() != 1 {
		t.Fatalf("statement GETs=%d chunk GETs=%d", statementGets.Load(), chunkGets.Load())
	}
	if len(materialized.Rows) != 3 || materialized.Rows[2]["id"] != float64(3) || materialized.Rows[2]["name"] != "c" {
		t.Fatalf("rows = %#v", materialized.Rows)
	}
	if materialized.ChunksRead != 2 || materialized.LocallyTruncated || !materialized.ResultAvailable {
		t.Fatalf("materialized = %#v", materialized)
	}
}

func TestListQueryHistoryParsesFiltersTagsAndMetrics(t *testing.T) {
	var request queryHistoryRequest
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/2.0/sql/history/queries" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"has_next_page":   true,
			"next_page_token": "next-token",
			"res": []map[string]any{{
				"query_id": "q1", "query_text": "SELECT 1", "status": "RUNNING", "warehouse_id": "wh", "query_start_time_ms": 100,
				"query_tags": map[string]any{"app_job": "job-1", "nonce": "n-1"},
				"metrics": map[string]any{
					"execution_time_ms": 40, "task_total_time_ms": 50, "projected_remaining_wallclock_time_ms": 60,
					"remaining_task_count": 7, "work_to_be_done": 8, "rows_read_count": 9, "read_bytes": 10,
					"read_remote_bytes": 11, "spill_to_disk_bytes": 12, "runnable_tasks": 13,
					"task_time_over_time_range": []map[string]any{{"start_time_ms": 100, "task_total_time_ms": 50}},
				},
			}},
		})
	})
	page, err := handler.ListQueryHistory(context.Background(), server.URL, QueryHistoryFilter{
		Statuses:       []string{"RUNNING"},
		WarehouseIDs:   []string{"wh"},
		StatementIDs:   []string{"q1"},
		StartTimeMs:    90,
		EndTimeMs:      200,
		IncludeMetrics: true,
		MaxResults:     50,
	})
	if err != nil {
		t.Fatalf("ListQueryHistory: %v", err)
	}
	if !request.IncludeMetrics || request.MaxResults != 50 || request.FilterBy.StartRange == nil || request.FilterBy.StartRange.StartTimeMs != 90 || request.FilterBy.StatementIDs[0] != "q1" {
		t.Fatalf("request = %#v", request)
	}
	if len(page.Entries) != 1 || !page.HasNextPage || page.NextPageToken != "next-token" {
		t.Fatalf("page = %#v", page)
	}
	entry := page.Entries[0]
	if entry.QueryTags["nonce"] != "n-1" || entry.Metrics.ProjectedRemainingWallclockTimeMs != 60 || entry.Metrics.ReadRemoteBytes != 11 || entry.Metrics.SpillToDiskBytes != 12 || len(entry.Metrics.TaskTimeOverTimeRange) == 0 {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestFindStatementCandidatesReturnsAllExactMatches(t *testing.T) {
	sql := "/* dbstream nonce=n-1 */ SELECT 1"
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"res": []map[string]any{
				{"query_id": "later", "query_text": sql, "warehouse_id": "wh", "query_start_time_ms": 200, "query_tags": []map[string]any{{"key": "nonce", "value": "n-1"}}},
				{"query_id": "wrong-sql", "query_text": "SELECT 2", "warehouse_id": "wh", "query_start_time_ms": 150, "query_tags": map[string]any{"nonce": "n-1"}},
				{"query_id": "wrong-warehouse", "query_text": sql, "warehouse_id": "other", "query_start_time_ms": 150, "query_tags": map[string]any{"nonce": "n-1"}},
				{"query_id": "outside-window", "query_text": sql, "warehouse_id": "wh", "query_start_time_ms": 400, "query_tags": map[string]any{"nonce": "n-1"}},
				{"query_id": "earlier", "query_text": sql, "warehouse_id": "wh", "query_start_time_ms": 100, "query_tags": map[string]any{"nonce": "n-1"}},
			},
		})
	})
	candidates, err := handler.FindStatementCandidates(context.Background(), StatementReconcileRequest{
		Host:               server.URL,
		WarehouseID:        "wh",
		SubmittedSQLSHA256: StatementSQLSHA256(sql),
		RequiredTags:       map[string]string{"nonce": "n-1"},
		StartTimeMs:        50,
		EndTimeMs:          300,
		MaxPages:           1,
	})
	if err != nil {
		t.Fatalf("FindStatementCandidates: %v", err)
	}
	if len(candidates) != 2 || candidates[0].StatementID != "earlier" || candidates[1].StatementID != "later" {
		t.Fatalf("candidates = %#v", candidates)
	}
}

func TestExplainQueryRejectsUntrustedMode(t *testing.T) {
	handler := NewHandler(nil, nil)
	_, err := handler.ExplainQuery(context.Background(), ExplainQueryRequest{
		StatementSubmitRequest: StatementSubmitRequest{Host: "https://example.gcp.databricks.com", WarehouseID: "wh", SQL: "SELECT 1"},
		Mode:                   "FORMATTED; DROP TABLE important",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported EXPLAIN mode") {
		t.Fatalf("error = %v", err)
	}
}

func TestExplainQueryUsesTrackedStatementExecution(t *testing.T) {
	var submitted string
	var waitTimeout string
	var onWaitTimeout string
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		var request statementRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		submitted = request.Statement
		waitTimeout = request.WaitTimeout
		onWaitTimeout = request.OnWaitTimeout
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statement_id": "explain-1",
			"status":       map[string]any{"state": "SUCCEEDED"},
			"manifest":     map[string]any{"schema": map[string]any{"columns": []map[string]any{{"name": "plan", "type_name": "STRING"}}}},
			"result":       map[string]any{"data_array": [][]any{{"Scan table"}, {"Filter x = 1"}}},
		})
	})
	result, err := handler.ExplainQuery(context.Background(), ExplainQueryRequest{
		StatementSubmitRequest: StatementSubmitRequest{Host: server.URL, WarehouseID: "wh", SQL: "SELECT 1"},
		Mode:                   "FORMATTED",
	})
	if err != nil {
		t.Fatalf("ExplainQuery: %v", err)
	}
	if submitted != "EXPLAIN FORMATTED\nSELECT 1" || result.Statement.Handle.StatementID != "explain-1" || !strings.Contains(result.PlanText, "Filter x = 1") {
		t.Fatalf("submitted=%q result=%#v", submitted, result)
	}
	if waitTimeout != "50s" || onWaitTimeout != "CANCEL" {
		t.Fatalf("profile wait controls=%q/%q, want 50s/CANCEL", waitTimeout, onWaitTimeout)
	}
}

func TestExplainQueryPollsToTerminalWithoutCancelling(t *testing.T) {
	var polls atomic.Int32
	var cancels atomic.Int32
	var observations []string
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statement_id": "explain-polled",
				"status":       map[string]any{"state": "PENDING"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/sql/statements/explain-polled":
			if polls.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"statement_id": "explain-polled",
					"status":       map[string]any{"state": "RUNNING"},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statement_id": "explain-polled",
				"status":       map[string]any{"state": "SUCCEEDED"},
				"manifest":     map[string]any{"schema": map[string]any{"columns": []map[string]any{{"name": "plan", "type_name": "STRING"}}}},
				"result":       map[string]any{"data_array": [][]any{{"final plan"}}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements/explain-polled/cancel":
			cancels.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})

	result, err := handler.ExplainQuery(context.Background(), ExplainQueryRequest{
		StatementSubmitRequest: StatementSubmitRequest{Host: server.URL, WarehouseID: "wh", SQL: "SELECT 1"},
		Mode:                   "COST",
		PollInterval:           time.Millisecond,
		OnObservation: func(observation ExplainObservation) error {
			observations = append(observations, observation.Phase+":"+observation.Statement.State)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ExplainQuery: %v", err)
	}
	if !result.Statement.Terminal || !result.Statement.Succeeded || result.PlanText != "final plan" {
		t.Fatalf("result = %#v", result)
	}
	if polls.Load() != 2 || cancels.Load() != 0 {
		t.Fatalf("polls/cancels=%d/%d, want 2/0", polls.Load(), cancels.Load())
	}
	if got := strings.Join(observations, ","); got != "explain_cost:PENDING,explain_cost:RUNNING,explain_cost:SUCCEEDED" {
		t.Fatalf("observations=%q", got)
	}
}

func TestExplainQueryObservationFailureCancelsActiveStatement(t *testing.T) {
	sentinel := errors.New("durable observation rejected")
	var polls atomic.Int32
	var cancels atomic.Int32
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statement_id": "explain-observer-failed",
				"status":       map[string]any{"state": "PENDING"},
			})
		case r.Method == http.MethodGet:
			polls.Add(1)
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements/explain-observer-failed/cancel":
			cancels.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := handler.ExplainQuery(context.Background(), ExplainQueryRequest{
		StatementSubmitRequest: StatementSubmitRequest{Host: server.URL, WarehouseID: "wh", SQL: "SELECT 1"},
		Mode:                   "COST",
		OnObservation: func(observation ExplainObservation) error {
			if observation.Phase != "explain_cost" || observation.Statement.Handle.StatementID != "explain-observer-failed" {
				t.Fatalf("observation=%+v", observation)
			}
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ExplainQuery error=%v, want observer error", err)
	}
	if polls.Load() != 0 || cancels.Load() != 1 {
		t.Fatalf("polls/cancels=%d/%d, want 0/1", polls.Load(), cancels.Load())
	}
}

func TestExplainQueryCancellationUsesFreshBoundedCleanupContext(t *testing.T) {
	var cancels atomic.Int32
	pollStarted := make(chan struct{})
	cancelStarted := make(chan struct{})
	cancelDone := make(chan struct{})
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statement_id": "explain-timeout",
				"status":       map[string]any{"state": "PENDING"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/2.0/sql/statements/explain-timeout":
			close(pollStarted)
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/api/2.0/sql/statements/explain-timeout/cancel":
			cancels.Add(1)
			close(cancelStarted)
			<-r.Context().Done()
			close(cancelDone)
		default:
			http.NotFound(w, r)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := handler.ExplainQuery(ctx, ExplainQueryRequest{
			StatementSubmitRequest: StatementSubmitRequest{Host: server.URL, WarehouseID: "wh", SQL: "SELECT 1"},
			Mode:                   "COST",
			PollInterval:           time.Millisecond,
			CleanupTimeout:         200 * time.Millisecond,
		})
		done <- err
	}()
	select {
	case <-pollStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("profile poll did not start")
	}
	started := time.Now()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ExplainQuery error=%v, want context cancellation", err)
	}
	select {
	case <-cancelStarted:
	default:
		t.Fatal("cleanup cancellation was not attempted with a fresh context")
	}
	select {
	case <-cancelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup request did not stop at its bounded deadline")
	}
	if cancels.Load() != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("cancels=%d elapsed=%s", cancels.Load(), time.Since(started))
	}
}

func TestRequestCancelStatementReturnsUnconfirmedReceipt(t *testing.T) {
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/2.0/sql/statements/s1/cancel" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	})
	receipt, err := handler.RequestCancelStatement(context.Background(), StatementHandle{Host: server.URL, WarehouseID: "wh", StatementID: "s1"})
	if err != nil {
		t.Fatalf("RequestCancelStatement: %v", err)
	}
	if !receipt.Accepted || receipt.Handle.StatementID != "s1" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestGetStatementProfileKeepsBasicSnapshotWhenHistoryDenied(t *testing.T) {
	handler, server := newDatabricksLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/2.0/sql/statements/s1" {
			_ = json.NewEncoder(w).Encode(map[string]any{"statement_id": "s1", "status": map[string]any{"state": "RUNNING"}})
			return
		}
		if r.URL.Path == "/api/2.0/sql/history/queries" {
			http.Error(w, `{"error_code":"PERMISSION_DENIED","message":"denied"}`, http.StatusForbidden)
			return
		}
		http.NotFound(w, r)
	})
	profile, err := handler.GetStatementProfile(context.Background(), StatementHandle{Host: server.URL, WarehouseID: "wh", StatementID: "s1"})
	if StatementHTTPStatus(err) != http.StatusForbidden {
		t.Fatalf("error = %v status=%d", err, StatementHTTPStatus(err))
	}
	if profile.Statement.State != "RUNNING" || profile.Statement.Terminal {
		t.Fatalf("profile = %#v", profile)
	}
}
