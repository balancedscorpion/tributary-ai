package trino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitAsyncReturnsOnePageAndCapturesProtocolStateAndStats(t *testing.T) {
	var requests atomic.Int32
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/statement" {
			t.Fatalf("request = %s %s, want POST /v1/statement", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if got := string(body); got != "SELECT count(*) FROM events" {
			t.Fatalf("body = %q", got)
		}
		assertHeader(t, r, "X-Trino-Source", "durable-source")
		assertHeader(t, r, "X-Trino-Trace-Token", "job-123")
		assertHeader(t, r, "X-Trino-Client-Tags", "ai-analyst,long-horizon,risk-high")
		assertHeaderContainsAssignments(t, r, "X-Trino-Session", map[string]string{
			"join_distribution_type":        "BROADCAST",
			"query_max_execution_time":      "20m",
			"query_max_run_time":            "30m",
			"query_max_scan_physical_bytes": "1073741824B",
		})

		w.Header().Set("X-Trino-Set-Catalog", "iceberg")
		w.Header().Set("X-Trino-Set-Schema", "analytics")
		w.Header().Set("X-Trino-Set-Path", "system.runtime")
		w.Header().Add("X-Trino-Set-Session", "retry_policy=TASK,query_priority=high%20priority")
		w.Header().Set("X-Trino-Set-Role", "iceberg=ROLE%7Banalyst%7D")
		w.Header().Set("X-Trino-Set-Original-Roles", "iceberg=ROLE%7Breader%7D")
		w.Header().Set("X-Trino-Added-Prepare", "latest=SELECT%201")
		w.Header().Set("X-Trino-Started-Transaction-Id", "txn-1")
		w.Header().Set("X-Trino-Set-Authorization-User", "delegated-user")
		writeJSONTest(w, map[string]any{
			"id":               "20260712_120000_00001_abcd",
			"infoUri":          serverURL + "/ui/query.html?q=1",
			"nextUri":          serverURL + "/v1/statement/executing/1",
			"partialCancelUri": serverURL + "/v1/stage/leaf/1",
			"columns": []map[string]any{{
				"name": "count", "type": "bigint",
				"typeSignature": map[string]any{"rawType": "bigint", "arguments": []any{}},
			}},
			"data": [][]any{{17}},
			"warnings": []map[string]any{{
				"warningCode": map[string]any{"code": 7, "name": "STALE_STATS"},
				"message":     "statistics are stale",
			}},
			"stats": comprehensiveStatsFixture(),
		})
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, ts.URL)
	page, err := h.SubmitAsync(context.Background(), AsyncQueryRequest{
		SQL:        " SELECT count(*) FROM events ",
		Source:     "durable-source",
		TraceToken: "job-123",
		ClientTags: []string{"risk-high", "ai-analyst", "long-horizon", "ai-analyst"},
		SessionProperties: map[string]string{
			"query_max_execution_time":      "20m",
			"query_max_run_time":            "30m",
			"query_max_scan_physical_bytes": "1073741824B",
		},
		ProtocolState: ProtocolState{
			SessionProperties: map[string]string{"join_distribution_type": "BROADCAST"},
		},
	})
	if err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want exactly one POST and no adaptive polling", got)
	}
	if page.Completed {
		t.Fatalf("Completed = true with nextUri")
	}
	if page.QueryID != "20260712_120000_00001_abcd" {
		t.Fatalf("QueryID = %q", page.QueryID)
	}
	if page.RequestURI != ts.URL+"/v1/statement" {
		t.Fatalf("RequestURI = %q", page.RequestURI)
	}
	if page.NextURI != ts.URL+"/v1/statement/executing/1" {
		t.Fatalf("NextURI = %q", page.NextURI)
	}
	if page.PartialCancelURI != ts.URL+"/v1/stage/leaf/1" {
		t.Fatalf("PartialCancelURI = %q", page.PartialCancelURI)
	}
	if len(page.Rows) != 1 || page.Rows[0]["count"] != float64(17) {
		t.Fatalf("Rows = %#v", page.Rows)
	}
	if len(page.Columns) != 1 || len(page.Columns[0].TypeSignature) == 0 {
		t.Fatalf("Columns = %#v", page.Columns)
	}
	if len(page.Warnings) != 1 || page.Warnings[0].WarningCode.Name != "STALE_STATS" {
		t.Fatalf("Warnings = %#v", page.Warnings)
	}
	assertComprehensiveStats(t, page.Stats)
	if !strings.Contains(string(page.Stats.Raw), `"futureMetric":99`) {
		t.Fatalf("raw stats did not preserve unknown field: %s", page.Stats.Raw)
	}

	wantState := ProtocolState{
		Catalog: "iceberg",
		Schema:  "analytics",
		Path:    "system.runtime",
		SessionProperties: map[string]string{
			"join_distribution_type": "BROADCAST",
			"retry_policy":           "TASK",
			"query_priority":         "high priority",
		},
		Roles:              map[string]string{"iceberg": "ROLE{analyst}"},
		OriginalRoles:      map[string]string{"iceberg": "ROLE{reader}"},
		PreparedStatements: map[string]string{"latest": "SELECT 1"},
		TransactionID:      "txn-1",
		AuthorizationUser:  "delegated-user",
		OriginalUser:       "alice",
	}
	if !reflect.DeepEqual(page.ProtocolState, wantState) {
		t.Fatalf("ProtocolState = %#v, want %#v", page.ProtocolState, wantState)
	}
	if page.ProtocolStateDelta.StartedTransactionID != "txn-1" {
		t.Fatalf("ProtocolStateDelta = %#v", page.ProtocolStateDelta)
	}
}

func TestPollAsyncPropagatesStateAdvancesCursorAndUsesOnlyGET(t *testing.T) {
	var methodsMu sync.Mutex
	methods := make([]string, 0, 1)
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methodsMu.Lock()
		methods = append(methods, r.Method)
		methodsMu.Unlock()
		if r.Method != http.MethodGet || r.URL.Path != "/next/1" {
			t.Fatalf("request = %s %s, want GET /next/1", r.Method, r.URL.Path)
		}
		assertHeader(t, r, "X-Trino-Catalog", "iceberg")
		assertHeader(t, r, "X-Trino-Schema", "analytics")
		assertHeader(t, r, "X-Trino-Path", "system.runtime")
		assertHeader(t, r, "X-Trino-Trace-Token", "job-poll")
		assertHeader(t, r, "X-Trino-Transaction-Id", "txn-old")
		assertHeader(t, r, "X-Trino-User", "old-user")
		assertHeader(t, r, "X-Trino-Original-User", "alice")
		assertHeader(t, r, "X-Trino-Client-Capabilities", "PATH,SESSION_AUTHORIZATION")
		assertHeaderContainsAssignments(t, r, "X-Trino-Session", map[string]string{
			"clear_me": "old",
			"keep_me":  "yes",
		})
		assertHeaderContainsAssignments(t, r, "X-Trino-Role", map[string]string{
			"iceberg": "ROLE{analyst}",
		})
		assertHeaderContainsAssignments(t, r, "X-Trino-Original-Roles", map[string]string{
			"iceberg": "ROLE{reader}",
		})
		assertHeaderContainsAssignments(t, r, "X-Trino-Prepared-Statement", map[string]string{
			"old": "SELECT 1",
		})

		w.Header().Set("X-Trino-Clear-Session", "clear_me")
		w.Header().Set("X-Trino-Deallocated-Prepare", "old")
		w.Header().Set("X-Trino-Clear-Transaction-Id", "true")
		w.Header().Set("X-Trino-Reset-Authorization-User", "true")
		writeJSONTest(w, map[string]any{
			"id":      "q-poll",
			"nextUri": serverURL + "/next/2",
			"data":    [][]any{{"advanced"}},
			"stats": map[string]any{
				"state":              "RUNNING",
				"progressPercentage": 42.5,
				"totalSplits":        10,
				"completedSplits":    4,
			},
		})
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, ts.URL)
	page, err := h.PollAsync(context.Background(), AsyncPollRequest{
		QueryID:    "q-poll",
		NextURI:    ts.URL + "/next/1",
		TraceToken: "job-poll",
		Columns:    []StatementColumn{{Name: "status", Type: "varchar"}},
		ProtocolState: ProtocolState{
			Catalog:            "iceberg",
			Schema:             "analytics",
			Path:               "system.runtime",
			SessionProperties:  map[string]string{"clear_me": "old", "keep_me": "yes"},
			Roles:              map[string]string{"iceberg": "ROLE{analyst}"},
			OriginalRoles:      map[string]string{"iceberg": "ROLE{reader}"},
			PreparedStatements: map[string]string{"old": "SELECT 1"},
			TransactionID:      "txn-old",
			AuthorizationUser:  "old-user",
		},
	})
	if err != nil {
		t.Fatalf("PollAsync: %v", err)
	}
	if page.Completed {
		t.Fatalf("Completed = true with nextUri")
	}
	if page.RequestURI != ts.URL+"/next/1" || page.NextURI != ts.URL+"/next/2" {
		t.Fatalf("cursor = %q -> %q", page.RequestURI, page.NextURI)
	}
	if len(page.Rows) != 1 || page.Rows[0]["status"] != "advanced" {
		t.Fatalf("Rows = %#v", page.Rows)
	}
	if page.Stats.ProgressPercentage == nil || *page.Stats.ProgressPercentage != 42.5 {
		t.Fatalf("ProgressPercentage = %#v", page.Stats.ProgressPercentage)
	}
	if _, exists := page.ProtocolState.SessionProperties["clear_me"]; exists {
		t.Fatalf("clear_me remains in state: %#v", page.ProtocolState)
	}
	if page.ProtocolState.SessionProperties["keep_me"] != "yes" ||
		page.ProtocolState.TransactionID != "" ||
		page.ProtocolState.AuthorizationUser != "" ||
		len(page.ProtocolState.PreparedStatements) != 0 {
		t.Fatalf("post-poll state = %#v", page.ProtocolState)
	}
	if !page.ProtocolStateDelta.ClearTransactionID || !page.ProtocolStateDelta.ResetAuthorizationUser {
		t.Fatalf("delta = %#v", page.ProtocolStateDelta)
	}

	methodsMu.Lock()
	gotMethods := append([]string(nil), methods...)
	methodsMu.Unlock()
	if !reflect.DeepEqual(gotMethods, []string{http.MethodGet}) {
		t.Fatalf("methods = %v; polling must be exactly one GET and no HEAD", gotMethods)
	}
}

func TestAsyncPageCompletionAndQueryErrorSemantics(t *testing.T) {
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/statement":
			writeJSONTest(w, map[string]any{"id": "q-complete", "stats": map[string]any{"state": "FINISHED"}})
		case "/query-error":
			writeJSONTest(w, map[string]any{
				"id": "q-error",
				"error": map[string]any{
					"message":   "scan limit exceeded",
					"errorCode": 123,
					"errorName": "EXCEEDED_SCAN_LIMIT",
					"errorType": "USER_ERROR",
				},
				"stats": map[string]any{"state": "FAILED"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, serverURL)
	complete, err := h.SubmitAsync(context.Background(), AsyncQueryRequest{SQL: "SELECT 1"})
	if err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	if !complete.Completed || complete.Error != nil || complete.NextURI != "" {
		t.Fatalf("completed page = %#v", complete)
	}

	failed, err := h.PollAsync(context.Background(), AsyncPollRequest{
		QueryID: "q-error", NextURI: serverURL + "/query-error",
	})
	if err != nil {
		t.Fatalf("PollAsync returned monitoring error for query failure: %v", err)
	}
	if failed.Completed {
		t.Fatalf("query error page must not be normal completion")
	}
	if failed.Error == nil || failed.Error.ErrorName != "EXCEEDED_SCAN_LIMIT" || failed.Stats.State != "FAILED" {
		t.Fatalf("failed page = %#v", failed)
	}
}

func TestCancelAsyncDeletesCurrentNextURINeverPartialCancelURI(t *testing.T) {
	var requestedPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("method = %s, want DELETE", r.Method)
		}
		requestedPath = r.URL.Path
		if requestedPath == "/leaf-stage" {
			t.Fatalf("partialCancelUri must never be used for full cancellation")
		}
		if requestedPath != "/current-next" {
			t.Fatalf("path = %q, want /current-next", requestedPath)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	h := newQueryTestHandler(t, ts.URL)
	outcome, err := h.CancelAsync(context.Background(), AsyncPollRequest{
		QueryID: "q-cancel",
		NextURI: ts.URL + "/current-next",
	})
	if err != nil {
		t.Fatalf("CancelAsync: %v", err)
	}
	if requestedPath != "/current-next" || !outcome.Accepted || outcome.Confirmed || outcome.HTTPStatus != http.StatusNoContent {
		t.Fatalf("outcome = %#v, path=%q", outcome, requestedPath)
	}

	if _, err := h.CancelAsync(context.Background(), AsyncPollRequest{QueryID: "q-cancel"}); err == nil {
		t.Fatalf("CancelAsync without nextUri succeeded")
	}
}

func TestLegacyWaitInterruptionDoesNotCancelProviderQuery(t *testing.T) {
	var serverURL string
	var deletes atomic.Int32
	submitted := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			writeJSONTest(w, map[string]any{
				"id":      "q-long-running",
				"nextUri": serverURL + "/next/long-running",
				"stats":   map[string]any{"state": "RUNNING"},
			})
			close(submitted)
		case http.MethodGet:
			<-r.Context().Done()
		case http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL
	h := newQueryTestHandler(t, ts.URL)
	cfg, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-submitted
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	_, err = h.executeSQL(ctx, cfg, "SELECT count(*) FROM huge_table", 10)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeSQL error=%v, want context cancellation", err)
	}
	if got := deletes.Load(); got != 0 {
		t.Fatalf("provider DELETE calls=%d, want 0 without explicit cancellation", got)
	}
}

func TestExplainQueryRunsValidateIOAndDistributedWithoutAnalyze(t *testing.T) {
	var mu sync.Mutex
	statements := make([]string, 0, 3)
	methods := make([]string, 0, 4)
	observations := make([]string, 0, 4)
	var cancels atomic.Int32
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		methods = append(methods, r.Method)
		mu.Unlock()
		if r.Method == http.MethodHead {
			t.Fatalf("EXPLAIN lifecycle invented a HEAD request")
		}
		if r.Method == http.MethodDelete {
			cancels.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/distributed/next" {
				t.Fatalf("GET path = %q", r.URL.Path)
			}
			assertHeaderContainsAssignments(t, r, "X-Trino-Session", map[string]string{"retry_policy": "TASK"})
			writeJSONTest(w, map[string]any{
				"id":    "explain-distributed",
				"data":  [][]any{{`{"plan":"distributed"}`}},
				"stats": map[string]any{"state": "FINISHED"},
			})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/statement" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		statement := string(body)
		if strings.Contains(strings.ToUpper(statement), "EXPLAIN ANALYZE") {
			t.Fatalf("preflight executed EXPLAIN ANALYZE: %q", statement)
		}
		mu.Lock()
		statements = append(statements, statement)
		index := len(statements)
		mu.Unlock()
		switch index {
		case 1:
			w.Header().Set("X-Trino-Set-Session", "retry_policy=TASK")
			writeJSONTest(w, map[string]any{
				"id":      "explain-validate",
				"columns": []map[string]any{{"name": "valid", "type": "boolean"}},
				"data":    [][]any{{true}},
				"stats":   map[string]any{"state": "FINISHED"},
			})
		case 2:
			assertHeaderContainsAssignments(t, r, "X-Trino-Session", map[string]string{"retry_policy": "TASK"})
			writeJSONTest(w, map[string]any{
				"id":      "explain-io",
				"columns": []map[string]any{{"name": "Query Plan", "type": "varchar"}},
				"data":    [][]any{{`{"inputTableColumnInfos":[]}`}},
				"stats":   map[string]any{"state": "FINISHED"},
			})
		case 3:
			assertHeaderContainsAssignments(t, r, "X-Trino-Session", map[string]string{"retry_policy": "TASK"})
			writeJSONTest(w, map[string]any{
				"id":      "explain-distributed",
				"columns": []map[string]any{{"name": "Query Plan", "type": "varchar"}},
				"nextUri": serverURL + "/distributed/next",
				"stats":   map[string]any{"state": "PLANNING"},
			})
		default:
			t.Fatalf("unexpected statement %d: %q", index, statement)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, serverURL)
	profile, err := h.ExplainQuery(context.Background(), ExplainRequest{
		SQL:        "SELECT * FROM events",
		TraceToken: "job-explain",
		ClientTags: []string{"ai-analyst", "preflight"},
		OnObservation: func(observation ExplainObservation) error {
			observations = append(observations, fmt.Sprintf("%s:%s:%s:%t",
				observation.Phase, observation.Page.QueryID, observation.Page.NextURI, observation.Page.Completed))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ExplainQuery: %v", err)
	}
	wantStatements := []string{
		"EXPLAIN (TYPE VALIDATE) SELECT * FROM events",
		"EXPLAIN (TYPE IO, FORMAT JSON) SELECT * FROM events",
		"EXPLAIN (TYPE DISTRIBUTED, FORMAT JSON) SELECT * FROM events",
	}
	mu.Lock()
	gotStatements := append([]string(nil), statements...)
	gotMethods := append([]string(nil), methods...)
	mu.Unlock()
	if !reflect.DeepEqual(gotStatements, wantStatements) {
		t.Fatalf("statements = %#v, want %#v", gotStatements, wantStatements)
	}
	if !reflect.DeepEqual(gotMethods, []string{http.MethodPost, http.MethodPost, http.MethodPost, http.MethodGet}) {
		t.Fatalf("methods = %v", gotMethods)
	}
	if !profile.Validated || profile.Validate.QueryID != "explain-validate" {
		t.Fatalf("validate profile = %#v", profile.Validate)
	}
	if !json.Valid(profile.IO.JSON) || !json.Valid(profile.Distributed.JSON) {
		t.Fatalf("plans are not JSON: io=%s distributed=%s", profile.IO.JSON, profile.Distributed.JSON)
	}
	if profile.ProtocolState.SessionProperties["retry_policy"] != "TASK" {
		t.Fatalf("protocol state = %#v", profile.ProtocolState)
	}
	if profile.EstimateQuality != "connector_dependent" || len(profile.Warnings) == 0 {
		t.Fatalf("profile quality/warnings = %#v", profile)
	}
	wantObservations := []string{
		"explain_validate:explain-validate::true",
		"explain_io:explain-io::true",
		"explain_distributed:explain-distributed:" + serverURL + "/distributed/next:false",
		"explain_distributed:explain-distributed::true",
	}
	if !reflect.DeepEqual(observations, wantObservations) {
		t.Fatalf("observations=%#v, want %#v", observations, wantObservations)
	}
	if cancels.Load() != 0 {
		t.Fatalf("normal terminal profile issued %d cancellations", cancels.Load())
	}
}

func TestExplainQueryObservationFailureCancelsCurrentPhase(t *testing.T) {
	sentinel := errors.New("durable observation rejected")
	var statementCount atomic.Int32
	var polls atomic.Int32
	var cancels atomic.Int32
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/statement":
			switch statementCount.Add(1) {
			case 1:
				writeJSONTest(w, map[string]any{"id": "validate", "stats": map[string]any{"state": "FINISHED"}})
			case 2:
				writeJSONTest(w, map[string]any{"id": "io", "stats": map[string]any{"state": "FINISHED"}})
			case 3:
				writeJSONTest(w, map[string]any{
					"id":      "distributed-observer-failed",
					"nextUri": serverURL + "/cursor/current",
					"stats":   map[string]any{"state": "PLANNING"},
				})
			default:
				t.Fatalf("unexpected EXPLAIN submit")
			}
		case r.Method == http.MethodGet:
			polls.Add(1)
			http.NotFound(w, r)
		case r.Method == http.MethodDelete && r.URL.Path == "/cursor/current":
			cancels.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL
	h := newQueryTestHandler(t, serverURL)

	_, err := h.ExplainQuery(context.Background(), ExplainRequest{
		SQL: "SELECT 1",
		OnObservation: func(observation ExplainObservation) error {
			if observation.Phase == "explain_distributed" {
				if observation.Page.QueryID != "distributed-observer-failed" || observation.Page.NextURI != serverURL+"/cursor/current" {
					t.Fatalf("observation=%+v", observation)
				}
				return sentinel
			}
			return nil
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ExplainQuery error=%v, want observer error", err)
	}
	if polls.Load() != 0 || cancels.Load() != 1 {
		t.Fatalf("polls/cancels=%d/%d, want 0/1", polls.Load(), cancels.Load())
	}
}

func TestExplainQueryCancellationUsesOnlyCurrentNextURIAndBoundedContext(t *testing.T) {
	var statementCount atomic.Int32
	var cancelPathMu sync.Mutex
	var cancelPaths []string
	pollStarted := make(chan struct{})
	cancelStarted := make(chan struct{})
	cancelDone := make(chan struct{})
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/statement":
			switch statementCount.Add(1) {
			case 1:
				writeJSONTest(w, map[string]any{"id": "validate", "data": [][]any{{true}}, "stats": map[string]any{"state": "FINISHED"}})
			case 2:
				writeJSONTest(w, map[string]any{"id": "io", "data": [][]any{{`{"inputTableColumnInfos":[]}`}}, "stats": map[string]any{"state": "FINISHED"}})
			case 3:
				writeJSONTest(w, map[string]any{
					"id":      "distributed",
					"nextUri": serverURL + "/cursor/one",
					"stats":   map[string]any{"state": "PLANNING"},
				})
			default:
				t.Fatalf("unexpected EXPLAIN submit")
			}
		case r.Method == http.MethodGet && r.URL.Path == "/cursor/one":
			writeJSONTest(w, map[string]any{
				"id":      "distributed",
				"nextUri": serverURL + "/cursor/two",
				"stats":   map[string]any{"state": "RUNNING"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/cursor/two":
			close(pollStarted)
			<-r.Context().Done()
		case r.Method == http.MethodDelete:
			cancelPathMu.Lock()
			cancelPaths = append(cancelPaths, r.URL.Path)
			cancelPathMu.Unlock()
			close(cancelStarted)
			<-r.Context().Done()
			close(cancelDone)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL
	h := newQueryTestHandler(t, serverURL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.ExplainQuery(ctx, ExplainRequest{
			SQL:            "SELECT 1",
			CleanupTimeout: 200 * time.Millisecond,
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
	cancelPathMu.Lock()
	gotPaths := append([]string(nil), cancelPaths...)
	cancelPathMu.Unlock()
	if !reflect.DeepEqual(gotPaths, []string{"/cursor/two"}) {
		t.Fatalf("cancel paths=%v, want only current /cursor/two", gotPaths)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("cleanup exceeded bound: %s", time.Since(started))
	}
}

func TestPollAsyncReturnsTransientStatusAndRetryAfterWithoutHiddenRetry(t *testing.T) {
	for _, status := range []int{
		http.StatusTooManyRequests,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var attempts atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Retry-After", "3")
				http.Error(w, "coordinator temporarily unavailable", status)
			}))
			defer ts.Close()

			h := newQueryTestHandler(t, ts.URL)
			_, err := h.PollAsync(context.Background(), AsyncPollRequest{QueryID: "q-retry", NextURI: ts.URL + "/same-cursor"})
			if err == nil {
				t.Fatal("PollAsync unexpectedly succeeded")
			}
			var upstream UpstreamError
			if !errors.As(err, &upstream) || upstream.StatusCode != status ||
				!upstream.Retryable || upstream.RetryAfter != 3*time.Second {
				t.Fatalf("error = %#v, want retryable %d with 3s Retry-After", err, status)
			}
			if got := attempts.Load(); got != 1 {
				t.Fatalf("GET attempts = %d, want 1 so the registry can durably pace the retry", got)
			}
		})
	}
}

func TestSubmitAsyncDoesNotRetryAmbiguousPOST(t *testing.T) {
	var attempts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		http.Error(w, "coordinator unavailable", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	h := newQueryTestHandler(t, ts.URL)
	_, err := h.SubmitAsync(context.Background(), AsyncQueryRequest{SQL: "SELECT 1"})
	if err == nil {
		t.Fatalf("SubmitAsync succeeded")
	}
	var upstream UpstreamError
	if !errors.As(err, &upstream) || upstream.StatusCode != http.StatusServiceUnavailable || !upstream.Retryable {
		t.Fatalf("error = %#v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("POST attempts = %d, want 1 to avoid duplicate query", got)
	}
}

func TestLifecycleHeadersRejectInjection(t *testing.T) {
	h := newQueryTestHandler(t, "http://trino.local")
	_, err := h.SubmitAsync(context.Background(), AsyncQueryRequest{
		SQL:        "SELECT 1",
		TraceToken: "job\r\nX-Evil: true",
	})
	if err == nil || !strings.Contains(err.Error(), "newline") {
		t.Fatalf("error = %v, want header validation failure", err)
	}
}

func TestPollAndCancelRejectCrossOriginNextURIWithoutSendingCredentials(t *testing.T) {
	var received atomic.Int32
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer evil.Close()

	h := newQueryTestHandler(t, "http://trino.local")
	if _, err := h.PollAsync(context.Background(), AsyncPollRequest{NextURI: evil.URL + "/steal"}); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("PollAsync error = %v, want origin rejection", err)
	}
	if _, err := h.CancelAsync(context.Background(), AsyncPollRequest{NextURI: evil.URL + "/steal"}); err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("CancelAsync error = %v, want origin rejection", err)
	}
	if got := received.Load(); got != 0 {
		t.Fatalf("cross-origin server received %d requests", got)
	}
}

func comprehensiveStatsFixture() map[string]any {
	return map[string]any{
		"state":                     "RUNNING",
		"queued":                    false,
		"scheduled":                 true,
		"progressPercentage":        25.5,
		"runningPercentage":         75.25,
		"nodes":                     3,
		"totalSplits":               100,
		"queuedSplits":              10,
		"runningSplits":             15,
		"completedSplits":           75,
		"planningTimeMillis":        11,
		"analysisTimeMillis":        12,
		"cpuTimeMillis":             13,
		"wallTimeMillis":            14,
		"queuedTimeMillis":          15,
		"elapsedTimeMillis":         16,
		"finishingTimeMillis":       17,
		"physicalInputTimeMillis":   18,
		"processedRows":             19,
		"processedBytes":            20,
		"physicalInputRows":         21,
		"physicalInputBytes":        22,
		"physicalWrittenRows":       23,
		"physicalWrittenBytes":      24,
		"internalNetworkInputRows":  25,
		"internalNetworkInputBytes": 26,
		"peakMemoryBytes":           27,
		"spilledBytes":              28,
		"rootStage":                 map[string]any{"stageId": "0", "state": "RUNNING"},
		"futureMetric":              99,
	}
}

func assertComprehensiveStats(t *testing.T, stats QueryStats) {
	t.Helper()
	if stats.State != "RUNNING" || stats.Queued || !stats.Scheduled ||
		stats.ProgressPercentage == nil || *stats.ProgressPercentage != 25.5 ||
		stats.RunningPercentage == nil || *stats.RunningPercentage != 75.25 ||
		stats.Nodes != 3 || stats.TotalSplits != 100 || stats.QueuedSplits != 10 ||
		stats.RunningSplits != 15 || stats.CompletedSplits != 75 ||
		stats.PlanningTimeMillis != 11 || stats.AnalysisTimeMillis != 12 ||
		stats.CPUTimeMillis != 13 || stats.WallTimeMillis != 14 ||
		stats.QueuedTimeMillis != 15 || stats.ElapsedTimeMillis != 16 ||
		stats.FinishingTimeMillis != 17 || stats.PhysicalInputTimeMillis != 18 ||
		stats.ProcessedRows != 19 || stats.ProcessedBytes != 20 ||
		stats.PhysicalInputRows != 21 || stats.PhysicalInputBytes != 22 ||
		stats.PhysicalWrittenRows != 23 || stats.PhysicalWrittenBytes != 24 ||
		stats.InternalNetworkInputRows != 25 || stats.InternalNetworkInputBytes != 26 ||
		stats.PeakMemoryBytes != 27 || stats.SpilledBytes != 28 || len(stats.RootStage) == 0 {
		t.Fatalf("stats not comprehensively decoded: %#v", stats)
	}
}

func assertHeader(t *testing.T, r *http.Request, name, want string) {
	t.Helper()
	if got := r.Header.Get(name); got != want {
		t.Fatalf("%s = %q, want %q", name, got, want)
	}
}

func assertHeaderContainsAssignments(t *testing.T, r *http.Request, name string, want map[string]string) {
	t.Helper()
	got := parseHeaderAssignments(r.Header.Values(name))
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("%s[%q] = %q, want %q; full header=%q", name, key, got[key], value, r.Header.Get(name))
		}
	}
}
