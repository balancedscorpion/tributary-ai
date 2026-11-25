package snowflake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/config"
)

func TestHandleStatusReportsAuthMode(t *testing.T) {
	t.Parallel()

	h, _, sec := newAuthTestHandler(t)
	if err := sec.SetSnowflakeOAuthToken("tok-1"); err != nil {
		t.Fatalf("SetSnowflakeOAuthToken: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/snowflake/status", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if got := strings.TrimSpace(anyString(out["auth_mode"])); got != snowflakeAuthModeOAuthToken {
		t.Fatalf("auth_mode=%q, want %q", got, snowflakeAuthModeOAuthToken)
	}
}

func TestHandleTestUsesSelectOne(t *testing.T) {
	t.Parallel()

	var seenSQL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		seenSQL = strings.TrimSpace(anyString(body["statement"]))
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statementHandle": "h-test",
			"status":          "SUCCESS",
			"resultSetMetaData": map[string]any{
				"rowType": []map[string]any{{"name": "v", "type": "NUMBER"}},
			},
			"data": [][]any{{1}},
		})
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/snowflake/test", strings.NewReader(`{"account_identifier":"org-account"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	if seenSQL != "SELECT 1" {
		t.Fatalf("statement=%q, want SELECT 1", seenSQL)
	}

	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if ok, _ := out["ok"].(bool); !ok {
		t.Fatalf("ok=false, want true")
	}
	if got := int(numberToFloat(out["rows_visible"])); got != 1 {
		t.Fatalf("rows_visible=%d, want 1", got)
	}
}

func TestHandlePreflightContracts(t *testing.T) {
	t.Parallel()

	t.Run("ok", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "h-preflight-ok",
				"status":          "SUCCESS",
			})
		}))
		defer server.Close()

		h := newOAuthHandlerForServer(t, server)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/snowflake/preflight", strings.NewReader(`{"account_identifier":"org-account","sql":"SELECT 1"}`))
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if ok, _ := out["ok"].(bool); !ok {
			t.Fatalf("ok=false, want true")
		}
		if got := strings.TrimSpace(anyString(out["severity"])); got != "ok" {
			t.Fatalf("severity=%q, want ok", got)
		}
		if explain, _ := out["explainSucceeded"].(bool); !explain {
			t.Fatalf("explainSucceeded=false, want true")
		}
	})

	t.Run("warning", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "h-preflight-warn",
				"status":          "FAILED_WITH_ERROR",
				"message":         "Insufficient privileges to EXPLAIN",
			})
		}))
		defer server.Close()

		h := newOAuthHandlerForServer(t, server)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/snowflake/preflight", strings.NewReader(`{"account_identifier":"org-account","sql":"SELECT 1"}`))
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got := strings.TrimSpace(anyString(out["severity"])); got != "warning" {
			t.Fatalf("severity=%q, want warning", got)
		}
		if explain, _ := out["explainSucceeded"].(bool); explain {
			t.Fatalf("explainSucceeded=true, want false")
		}
		warnings, _ := out["warnings"].([]any)
		if len(warnings) == 0 {
			t.Fatalf("warnings empty, want warning message")
		}
	})

	t.Run("blocking read-only", func(t *testing.T) {
		t.Parallel()
		h, _, sec := newAuthTestHandler(t)
		if err := sec.SetSnowflakeOAuthToken("tok"); err != nil {
			t.Fatalf("SetSnowflakeOAuthToken: %v", err)
		}

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/snowflake/preflight", strings.NewReader(`{"sql":"DELETE FROM t"}`))
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400 body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got := strings.TrimSpace(anyString(out["errorCode"])); got != "read_only_required" {
			t.Fatalf("errorCode=%q, want read_only_required", got)
		}
	})

	t.Run("blocking put-get", func(t *testing.T) {
		t.Parallel()
		h, _, sec := newAuthTestHandler(t)
		if err := sec.SetSnowflakeOAuthToken("tok"); err != nil {
			t.Fatalf("SetSnowflakeOAuthToken: %v", err)
		}

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/snowflake/preflight", strings.NewReader(`{"sql":"PUT file://foo @mystage"}`))
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400 body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if got := strings.TrimSpace(anyString(out["errorCode"])); got != "unsupported_command" {
			t.Fatalf("errorCode=%q, want unsupported_command", got)
		}
		if msg := strings.ToLower(strings.TrimSpace(anyString(out["message"]))); !strings.Contains(msg, "put/get") {
			t.Fatalf("message=%q, want PUT/GET mention", anyString(out["message"]))
		}
	})
}

func TestHandleQueryLifecycleAndRowLimit(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requestIDs := []string{}
	logs := []string{}
	pollCalls := 0
	partitionCalls := map[string]int{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if rid := strings.TrimSpace(r.URL.Query().Get("requestId")); rid != "" {
			requestIDs = append(requestIDs, rid)
		}
		mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "h-query",
				"status":          "RUNNING",
				"resultSetMetaData": map[string]any{
					"rowType":       []map[string]any{{"name": "v", "type": "NUMBER"}},
					"partitionInfo": []map[string]any{{}, {}, {}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/h-query" && r.URL.Query().Get("partition") == "":
			mu.Lock()
			pollCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "h-query",
				"status":          "SUCCESS",
				"resultSetMetaData": map[string]any{
					"rowType":       []map[string]any{{"name": "v", "type": "NUMBER"}},
					"partitionInfo": []map[string]any{{}, {}, {}},
				},
				"data": [][]any{{1}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/h-query" && r.URL.Query().Get("partition") != "":
			partition := r.URL.Query().Get("partition")
			mu.Lock()
			partitionCalls[partition]++
			mu.Unlock()
			switch partition {
			case "1":
				_ = json.NewEncoder(w).Encode(map[string]any{"statementHandle": "h-query", "status": "SUCCESS", "data": [][]any{{2}}})
			case "2":
				_ = json.NewEncoder(w).Encode(map[string]any{"statementHandle": "h-query", "status": "SUCCESS", "data": [][]any{{3}, {4}}})
			default:
				http.Error(w, "bad partition", http.StatusBadRequest)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)
	h.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, strings.TrimSpace(formatLogf(format, args...)))
	}
	cfg := h.Settings.Get()
	limit := 3
	cfg.DatabaseQueryRowLimit = &limit
	if cfg.DatabaseQueryPolicy == nil {
		policy := config.DefaultDatabaseQueryPolicy()
		cfg.DatabaseQueryPolicy = &policy
	}
	cfg.DatabaseQueryPolicy.MaxResultRows = limit
	if err := h.Settings.Save(cfg); err != nil {
		t.Fatalf("Settings.Save: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/snowflake/query", strings.NewReader(`{"account_identifier":"org-account","sql":"SELECT 1"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Rows) != 3 {
		t.Fatalf("rows len=%d, want 3", len(out.Rows))
	}

	mu.Lock()
	defer mu.Unlock()
	if pollCalls == 0 {
		t.Fatalf("expected poll to be attempted")
	}
	if partitionCalls["1"] == 0 || partitionCalls["2"] == 0 {
		t.Fatalf("expected partition fetches for 1 and 2, got %+v", partitionCalls)
	}
	if len(requestIDs) == 0 {
		t.Fatalf("expected requestId query params")
	}
	first := requestIDs[0]
	for i := range requestIDs {
		if requestIDs[i] != first {
			t.Fatalf("requestId mismatch across lifecycle: %v", requestIDs)
		}
	}
	if h.metrics.Counter("snowflake_poll_attempt_total") == 0 {
		t.Fatalf("snowflake_poll_attempt_total=0, want >0")
	}
	if h.metrics.Counter("snowflake_terminal_total") == 0 {
		t.Fatalf("snowflake_terminal_total=0, want >0")
	}
	if h.metrics.HistogramCount("snowflake_submit_latency_ms") == 0 {
		t.Fatalf("snowflake_submit_latency_ms histogram empty")
	}

	foundKeys := false
	for _, line := range logs {
		if strings.Contains(line, "provider=snowflake") && strings.Contains(line, "snowflake_query_handle=") && strings.Contains(line, "job_id=") && strings.Contains(line, "request_id=") && strings.Contains(line, "event=") && strings.Contains(line, "status=") {
			foundKeys = true
			break
		}
	}
	if !foundKeys {
		t.Fatalf("expected structured log keys in emitted lines: %v", logs)
	}
}

func TestHandleQueryPartitionExpiry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "h-expiry",
				"status":          "SUCCESS",
				"resultSetMetaData": map[string]any{
					"rowType":       []map[string]any{{"name": "v", "type": "NUMBER"}},
					"partitionInfo": []map[string]any{{}, {}},
				},
				"data": [][]any{{1}},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/h-expiry" && r.URL.Query().Get("partition") == "1":
			http.Error(w, `{"message":"expired"}`, http.StatusGone)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/snowflake/query", strings.NewReader(`{"account_identifier":"org-account","sql":"SELECT 1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("status=%d, want 410 body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var out struct {
		PartialRows []map[string]any `json:"partial_rows"`
		Error       struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.PartialRows) != 1 {
		t.Fatalf("partial_rows len=%d, want 1", len(out.PartialRows))
	}
	if !strings.Contains(strings.ToLower(out.Error.Message), "expired") {
		t.Fatalf("error message=%q, want expiry hint", out.Error.Message)
	}
}

func TestSubmitRetriesReuseStableRequestID(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	attempt := 0
	requestIDs := []string{}
	retryFlags := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/statements" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		attempt++
		requestIDs = append(requestIDs, strings.TrimSpace(r.URL.Query().Get("requestId")))
		retryFlags = append(retryFlags, strings.TrimSpace(r.URL.Query().Get("retry")))
		cur := attempt
		mu.Unlock()

		if cur == 1 {
			http.Error(w, `{"message":"temporary"}`, http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statementHandle": "h-retry",
			"status":          "SUCCESS",
			"data":            [][]any{{1}},
			"resultSetMetaData": map[string]any{
				"rowType": []map[string]any{{"name": "v"}},
			},
		})
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/snowflake/query", strings.NewReader(`{"account_identifier":"org-account","sql":"SELECT 1"}`))
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}

	mu.Lock()
	defer mu.Unlock()
	if attempt != 2 {
		t.Fatalf("submit attempts=%d, want 2", attempt)
	}
	if requestIDs[0] == "" {
		t.Fatalf("first requestId empty, want stable id")
	}
	if requestIDs[0] != requestIDs[1] {
		t.Fatalf("requestId mismatch across retries: %v", requestIDs)
	}
	if retryFlags[0] != "" || retryFlags[1] != "true" {
		t.Fatalf("retry flags=%v, want [empty true]", retryFlags)
	}
}

func TestSubmitDoesNotRetryWithoutRequestID(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	attempt := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/statements" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		attempt++
		mu.Unlock()
		http.Error(w, `{"message":"temporary"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)
	h.newRequestIDFn = func() string { return "" }

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/snowflake/query", strings.NewReader(`{"account_identifier":"org-account","sql":"SELECT 1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d, want 503 body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	mu.Lock()
	defer mu.Unlock()
	if attempt != 1 {
		t.Fatalf("submit attempts=%d, want 1 (no retry without request-id)", attempt)
	}
}

func TestMetadataEndpoints(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		sqlText := strings.TrimSpace(anyString(body["statement"]))

		switch sqlText {
		case "SHOW DATABASES":
			_ = json.NewEncoder(w).Encode(statementResponseWithRows([]string{"name"}, [][]any{{"DB1"}, {"DB2"}}))
		case `SHOW SCHEMAS IN DATABASE "DB1"`:
			_ = json.NewEncoder(w).Encode(statementResponseWithRows([]string{"name"}, [][]any{{"PUBLIC"}, {"ANALYTICS"}}))
		case `SHOW TABLES IN SCHEMA "DB1"."PUBLIC"`:
			_ = json.NewEncoder(w).Encode(statementResponseWithRows([]string{"name", "kind"}, [][]any{{"T1", "TABLE"}, {"V1", "VIEW"}}))
		case `DESCRIBE TABLE "DB1"."PUBLIC"."T1"`:
			_ = json.NewEncoder(w).Encode(statementResponseWithRows([]string{"name", "type", "comment"}, [][]any{{"ID", "NUMBER", "pk"}, {"NAME", "TEXT", ""}}))
		default:
			http.Error(w, "unexpected sql: "+sqlText, http.StatusBadRequest)
		}
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)

	t.Run("databases", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/snowflake/databases?account_identifier=org-account", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string][]map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(out["databases"]) != 2 {
			t.Fatalf("databases len=%d, want 2", len(out["databases"]))
		}
	})

	t.Run("schemas", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/snowflake/schemas?account_identifier=org-account&database_name=DB1", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string][]map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(out["schemas"]) != 2 {
			t.Fatalf("schemas len=%d, want 2", len(out["schemas"]))
		}
	})

	t.Run("tables", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/snowflake/tables?account_identifier=org-account&database_name=DB1&schema_name=PUBLIC", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string][]map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(out["tables"]) != 2 {
			t.Fatalf("tables len=%d, want 2", len(out["tables"]))
		}
	})

	t.Run("table schema", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/snowflake/table-schema?account_identifier=org-account&full_name=DB1.PUBLIC.T1", nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		schema, _ := out["schema"].([]any)
		if len(schema) != 2 {
			t.Fatalf("schema len=%d, want 2", len(schema))
		}
	})
}

func newOAuthHandlerForServer(t *testing.T, server *httptest.Server) *Handler {
	t.Helper()
	h, _, sec := newAuthTestHandler(t)
	if err := sec.SetSnowflakeOAuthToken("tok"); err != nil {
		t.Fatalf("SetSnowflakeOAuthToken: %v", err)
	}
	h.httpc = rewriteClientToServer(t, server)
	// Keep the per-exchange timeout comfortably above race-detector overhead;
	// query execution duration is governed separately by the provider request.
	h.upstreamRequestTimeout = 2 * time.Second
	return h
}

func rewriteClientToServer(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(server.URL): %v", err)
	}
	base := http.DefaultTransport
	return &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			clone := req.Clone(req.Context())
			clone.URL.Scheme = target.Scheme
			clone.URL.Host = target.Host
			clone.Host = target.Host
			return base.RoundTrip(clone)
		}),
	}
}

func statementResponseWithRows(cols []string, rows [][]any) map[string]any {
	rowType := make([]map[string]any, 0, len(cols))
	for _, col := range cols {
		rowType = append(rowType, map[string]any{"name": col, "type": "TEXT"})
	}
	return map[string]any{
		"statementHandle": "h-meta",
		"status":          "SUCCESS",
		"resultSetMetaData": map[string]any{
			"rowType": rowType,
		},
		"data": rows,
	}
}

func numberToFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		f, _ := strconv.ParseFloat(strings.TrimSpace(anyString(v)), 64)
		return f
	}
}

func formatLogf(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
