package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	trinopkg "ai-analyst-local/internal/trino"
)

type fakeTrinoResponse struct {
	Status int
	Body   map[string]any
}

type trinoTestDrainMaterializer struct{}

func (trinoTestDrainMaterializer) AppendChunk(context.Context, string, jobs.JobOwner, jobs.ResultPage) error {
	return nil
}

func (trinoTestDrainMaterializer) Finalize(context.Context, string, jobs.JobOwner, *jobs.QueryResult, jobs.MaterializationSummary) error {
	return nil
}

func (trinoTestDrainMaterializer) CommitDrainObservation(_ context.Context, commit jobs.DrainCommit) (int64, error) {
	return commit.Seq, nil
}

type fakeTrinoHandler struct {
	mu sync.Mutex

	preflightOK      bool
	preflightMessage string

	queryResponses []fakeTrinoResponse

	catalogs         []any
	schemasByCatalog map[string][]any
	tablesByScope    map[string][]any
	schemaByFullName map[string][]any
}

func (h *fakeTrinoHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case strings.HasPrefix(r.URL.Path, "/api/trino/preflight"):
		w.Header().Set("Content-Type", "application/json")
		if h.preflightOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": firstNonEmpty(h.preflightMessage, "preflight failed"),
			},
		})
		return
	case strings.HasPrefix(r.URL.Path, "/api/trino/query"):
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if len(h.queryResponses) == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "FINISHED",
				"rows":   []any{},
			})
			return
		}
		resp := h.queryResponses[0]
		h.queryResponses = h.queryResponses[1:]
		status := resp.Status
		if status <= 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp.Body)
		return
	case strings.HasPrefix(r.URL.Path, "/api/trino/catalogs"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"catalogs": h.catalogs})
		return
	case strings.HasPrefix(r.URL.Path, "/api/trino/schemas"):
		w.Header().Set("Content-Type", "application/json")
		catalog := strings.TrimSpace(r.URL.Query().Get("catalog_name"))
		_ = json.NewEncoder(w).Encode(map[string]any{"schemas": h.schemasByCatalog[catalog]})
		return
	case strings.HasPrefix(r.URL.Path, "/api/trino/tables"):
		w.Header().Set("Content-Type", "application/json")
		catalog := strings.TrimSpace(r.URL.Query().Get("catalog_name"))
		schema := strings.TrimSpace(r.URL.Query().Get("schema_name"))
		key := catalog + "." + schema
		_ = json.NewEncoder(w).Encode(map[string]any{"tables": h.tablesByScope[key]})
		return
	case strings.HasPrefix(r.URL.Path, "/api/trino/table-schema"):
		w.Header().Set("Content-Type", "application/json")
		fullName := strings.TrimSpace(r.URL.Query().Get("full_name"))
		_ = json.NewEncoder(w).Encode(map[string]any{"schema": h.schemaByFullName[fullName]})
		return
	default:
		http.NotFound(w, r)
	}
}

func TestRunTrinoQueryPreflightFailure(t *testing.T) {
	tool := newRunTrinoQueryTool()
	handler := &fakeTrinoHandler{
		preflightOK:      false,
		preflightMessage: "invalid query",
	}
	out, err := tool.Execute(context.Background(), ExecContext{
		TrinoHTTP: handler,
	}, mustJSONTrino(t, map[string]any{
		"sql": "select from",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.OK {
		t.Fatalf("expected preflight failure output")
	}
	result := mustMapTrino(t, out.Result)
	if result["preflight_failed"] != true {
		t.Fatalf("preflight_failed=%v, want true", result["preflight_failed"])
	}
	if result["rewrite_required"] != true {
		t.Fatalf("rewrite_required=%v, want true", result["rewrite_required"])
	}
}

func TestRunTrinoQueryToolSchemaOmitsFullData(t *testing.T) {
	for _, tool := range []Tool{
		newRunTrinoQueryTool(),
		newListTrinoCatalogsTool(),
		newListTrinoSchemasTool(),
		newListTrinoTablesTool(),
		newDescribeTrinoTableTool(),
	} {
		def := tool.Definition()
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
		props, ok := def.Function.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties missing from tool definition: %#v", def.Function.Name, def.Function.Parameters)
		}
		if _, exists := props["full_data"]; exists {
			t.Fatalf("%s should not advertise full_data in tool schema", def.Function.Name)
		}
	}

	props := toolDefinitionProperties(t, newRunTrinoQueryTool().Definition().Function.Parameters)
	requireIntegerRangeSchema(t, props["limit"], "run_trino_query.limit", 1, maxQueryPageSize)
	if _, exists := props["full_data"]; exists {
		t.Fatal("run_trino_query should not advertise full_data in tool schema")
	}
}

func TestFormatTrinoSuccessOutputStoresFullArtifactForPreview(t *testing.T) {
	rowsIn := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		rowsIn = append(rowsIn, map[string]any{"n": i})
	}

	out := formatTrinoSuccessOutput(rowsIn, 10, 0, 10, false, 1000, false, map[string]any{
		"provider": "trino",
	})
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	result := mustMapTrino(t, out.Result)
	rows := sliceAnyTrino(result["rows"])
	if len(rows) != 10 {
		t.Fatalf("preview rows=%d want=10", len(rows))
	}
	if result["preview_only"] != true {
		t.Fatalf("preview_only=%#v want=true", result["preview_only"])
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("result should not contain nested result_metadata: %#v", result)
	}
	if got := result["row_count_total"]; got != 100 {
		t.Fatalf("row_count_total=%#v want=100", got)
	}
	if out.Artifact == nil {
		t.Fatalf("expected full artifact payload")
	}
	var artifactRows []map[string]any
	if err := json.Unmarshal([]byte(out.Artifact.Content), &artifactRows); err != nil {
		t.Fatalf("unmarshal artifact content: %v", err)
	}
	if len(artifactRows) != 100 {
		t.Fatalf("artifact rows=%d want=100", len(artifactRows))
	}
	if out.Artifact.ID == "" {
		t.Fatalf("artifact ID should be populated")
	}
	if got := out.QueryMetadata["full_artifact_id"]; got != out.Artifact.ID {
		t.Fatalf("full_artifact_id=%#v want %q", got, out.Artifact.ID)
	}
	if got := out.ResultMetadata["full_artifact_id"]; got != out.Artifact.ID {
		t.Fatalf("ResultMetadata full_artifact_id=%#v want %q", got, out.Artifact.ID)
	}
	if got := out.ResultMetadata["content_kind"]; got != "json_rows" {
		t.Fatalf("ResultMetadata content_kind=%#v want json_rows", got)
	}
	if retrieval, ok := out.ResultMetadata["retrieval"].(map[string]any); !ok || retrieval["content_kind"] != "json_rows" {
		t.Fatalf("ResultMetadata retrieval=%#v want json_rows retrieval", out.ResultMetadata["retrieval"])
	}
}

func TestRunTrinoQueryAsyncLifecycle(t *testing.T) {
	tool := newRunTrinoQueryTool()
	handler := &fakeTrinoHandler{
		preflightOK: true,
		queryResponses: []fakeTrinoResponse{
			{
				Body: map[string]any{
					"status":   "RUNNING",
					"query_id": "tri-q-1",
					"next_uri": "https://trino.local/v1/statement/next/1",
				},
			},
			{
				Body: map[string]any{
					"status":   "RUNNING",
					"query_id": "tri-q-1",
					"next_uri": "https://trino.local/v1/statement/next/2",
				},
			},
			{
				Body: map[string]any{
					"status":   "FINISHED",
					"query_id": "tri-q-1",
					"rows": []any{
						map[string]any{"n": float64(1)},
						map[string]any{"n": float64(2)},
					},
					"schema": []any{
						map[string]any{"name": "n", "type": "bigint"},
					},
				},
			},
		},
	}
	reg := jobs.NewRegistry(jobs.RegistryOptions{
		Materializer: trinoTestDrainMaterializer{},
		Scheduler: &jobs.SchedulerConfig{
			BaseIntervals: []time.Duration{5 * time.Millisecond},
			MaxInterval:   10 * time.Millisecond,
			JitterFrac:    0.01,
		},
	})
	execCtx := ExecContext{
		TrinoHTTP:             handler,
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}
	out, err := tool.Execute(context.Background(), execCtx, mustJSONTrino(t, map[string]any{
		"sql": "select * from t",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected running response, got error: %#v", out.Result)
	}
	result := mustMapTrino(t, out.Result)
	if strings.TrimSpace(anyStringTrino(result["status"])) != "running" {
		t.Fatalf("status=%q, want running", anyStringTrino(result["status"]))
	}
	if strings.TrimSpace(anyStringTrino(out.QueryMetadata["provider"])) != "trino" {
		t.Fatalf("provider=%q, want trino", anyStringTrino(out.QueryMetadata["provider"]))
	}
	meta := requireDatasourceResultMetadata(t, out, "run_trino_query", "trino", "query_running")
	if meta["async_query"] != true || meta["status"] != "running" {
		t.Fatalf("ResultMetadata=%#v, want running async query metadata", meta)
	}
	jobID := strings.TrimSpace(anyStringTrino(result["job_id"]))
	ownerToken := strings.TrimSpace(anyStringTrino(result["owner_token"]))
	if jobID == "" || ownerToken == "" {
		t.Fatalf("missing async job references: job_id=%q owner_token=%q", jobID, ownerToken)
	}
	if meta["job_id"] != jobID || meta["owner_token"] != ownerToken {
		t.Fatalf("ResultMetadata job refs=%#v, want job_id=%q owner_token=%q", meta, jobID, ownerToken)
	}

	waitForJobStatusTrino(t, reg, jobID, "thread-1", ownerToken, jobs.JobStatusCompleted)
	getTool := newGetQueryResultsTool()
	getOut, err := getTool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSONTrino(t, map[string]any{
		"job_id":      jobID,
		"owner_token": ownerToken,
		"limit":       10,
	}))
	if err != nil {
		t.Fatalf("get_query_results: %v", err)
	}
	if !getOut.OK {
		t.Fatalf("expected get_query_results to succeed")
	}
	rows := sliceAnyTrino(mustMapTrino(t, getOut.Result)["rows"])
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2", len(rows))
	}
	if strings.TrimSpace(anyStringTrino(getOut.QueryMetadata["provider"])) != "trino" {
		t.Fatalf("metadata provider=%q, want trino", anyStringTrino(getOut.QueryMetadata["provider"]))
	}
}

func TestRunTrinoQueryAsyncLifecycleWithDirectTrinoHandler(t *testing.T) {
	var serverURL string
	var nextPollCount int
	var nextPollMu sync.Mutex
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/statement":
			sqlBytes, _ := io.ReadAll(r.Body)
			sqlText := strings.TrimSpace(string(sqlBytes))
			w.Header().Set("Content-Type", "application/json")
			if strings.HasPrefix(strings.ToUpper(sqlText), "EXPLAIN ") {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":      "tri-preflight",
					"columns": []map[string]any{{"name": "plan", "type": "varchar"}},
					"data":    [][]any{{"ok"}},
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "tri-q-real",
				"infoUri": serverURL + "/ui/tri-q-real",
				"columns": []map[string]any{{"name": "n", "type": "bigint"}},
				"data":    [][]any{{0}},
				"nextUri": serverURL + "/next1",
			})
			return
		case "/next1":
			nextPollMu.Lock()
			nextPollCount++
			call := nextPollCount
			nextPollMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if call < 12 {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id":      "tri-q-real",
					"infoUri": serverURL + "/ui/tri-q-real",
					"nextUri": serverURL + "/next1",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      "tri-q-real",
				"infoUri": serverURL + "/ui/tri-q-real",
				"columns": []map[string]any{{"name": "n", "type": "bigint"}},
				"data":    [][]any{{1}},
			})
			return
		case "/v1/query/tri-q-real":
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
			return
		default:
			http.NotFound(w, r)
			return
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	tool := newRunTrinoQueryTool()
	reg := jobs.NewRegistry(jobs.RegistryOptions{
		Materializer: trinoTestDrainMaterializer{},
		Scheduler: &jobs.SchedulerConfig{
			BaseIntervals: []time.Duration{5 * time.Millisecond},
			MaxInterval:   10 * time.Millisecond,
			JitterFrac:    0.01,
		},
	})
	execCtx := ExecContext{
		Trino:                 newDirectTrinoToolHandler(t, ts.URL),
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}
	out, err := tool.Execute(context.Background(), execCtx, mustJSONTrino(t, map[string]any{
		"sql": "select n from numbers",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected running response, got error: %#v", out.Result)
	}
	result := mustMapTrino(t, out.Result)
	if strings.TrimSpace(anyStringTrino(result["status"])) != "running" {
		t.Fatalf("status=%q, want running", anyStringTrino(result["status"]))
	}
	meta := requireDatasourceResultMetadata(t, out, "run_trino_query", "trino", "query_running")
	if meta["async_query"] != true || meta["status"] != "running" {
		t.Fatalf("ResultMetadata=%#v, want running async query metadata", meta)
	}
	jobID := strings.TrimSpace(anyStringTrino(result["job_id"]))
	ownerToken := strings.TrimSpace(anyStringTrino(result["owner_token"]))
	if jobID == "" || ownerToken == "" {
		t.Fatalf("missing async job references: job_id=%q owner_token=%q", jobID, ownerToken)
	}
	if meta["job_id"] != jobID || meta["owner_token"] != ownerToken {
		t.Fatalf("ResultMetadata job refs=%#v, want job_id=%q owner_token=%q", meta, jobID, ownerToken)
	}

	waitForJobStatusTrino(t, reg, jobID, "thread-1", ownerToken, jobs.JobStatusCompleted)
	getOut, err := newGetQueryResultsTool().Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSONTrino(t, map[string]any{
		"job_id":      jobID,
		"owner_token": ownerToken,
		"limit":       10,
	}))
	if err != nil {
		t.Fatalf("get_query_results: %v", err)
	}
	if !getOut.OK {
		t.Fatalf("expected get_query_results to succeed")
	}
	rows := sliceAnyTrino(mustMapTrino(t, getOut.Result)["rows"])
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2", len(rows))
	}
	row0 := mustMapTrino(t, rows[0])
	row1 := mustMapTrino(t, rows[1])
	if anyStringTrino(row0["n"]) != "0" || anyStringTrino(row1["n"]) != "1" {
		t.Fatalf("rows=%v, want n values [0,1]", rows)
	}
}

func TestTrinoMetadataTools(t *testing.T) {
	handler := &fakeTrinoHandler{
		preflightOK: true,
		catalogs: []any{
			map[string]any{"name": "hive"},
		},
		schemasByCatalog: map[string][]any{
			"hive": {
				map[string]any{"name": "default"},
			},
		},
		tablesByScope: map[string][]any{
			"hive.default": {
				map[string]any{"name": "events"},
			},
		},
		schemaByFullName: map[string][]any{
			"hive.default.events": {
				map[string]any{"name": "event_id", "type": "bigint"},
			},
		},
	}
	ctx := context.Background()

	catalogsOut, err := newListTrinoCatalogsTool().Execute(ctx, ExecContext{TrinoHTTP: handler}, mustJSONTrino(t, map[string]any{}))
	if err != nil {
		t.Fatalf("list_trino_catalogs: %v", err)
	}
	if !catalogsOut.OK {
		t.Fatalf("list_trino_catalogs should succeed")
	}
	if got := len(sliceAnyTrino(mustMapTrino(t, catalogsOut.Result)["catalogs"])); got != 1 {
		t.Fatalf("catalogs=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, catalogsOut, "list_trino_catalogs", "trino", "catalog_list")

	schemasOut, err := newListTrinoSchemasTool().Execute(ctx, ExecContext{TrinoHTTP: handler}, mustJSONTrino(t, map[string]any{
		"catalog_name": "hive",
	}))
	if err != nil {
		t.Fatalf("list_trino_schemas: %v", err)
	}
	if got := len(sliceAnyTrino(mustMapTrino(t, schemasOut.Result)["schemas"])); got != 1 {
		t.Fatalf("schemas=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, schemasOut, "list_trino_schemas", "trino", "schema_list")

	tablesOut, err := newListTrinoTablesTool().Execute(ctx, ExecContext{TrinoHTTP: handler}, mustJSONTrino(t, map[string]any{
		"catalog_name": "hive",
		"schema_name":  "default",
	}))
	if err != nil {
		t.Fatalf("list_trino_tables: %v", err)
	}
	if got := len(sliceAnyTrino(mustMapTrino(t, tablesOut.Result)["tables"])); got != 1 {
		t.Fatalf("tables=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, tablesOut, "list_trino_tables", "trino", "table_list")

	describeOut, err := newDescribeTrinoTableTool().Execute(ctx, ExecContext{TrinoHTTP: handler}, mustJSONTrino(t, map[string]any{
		"full_name": "hive.default.events",
	}))
	if err != nil {
		t.Fatalf("describe_trino_table: %v", err)
	}
	if got := len(sliceAnyTrino(mustMapTrino(t, describeOut.Result)["schema"])); got != 1 {
		t.Fatalf("schema=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, describeOut, "describe_trino_table", "trino", "table_schema")
}

func waitForJobStatusTrino(t *testing.T, reg *jobs.Registry, jobID, threadID, ownerToken string, want jobs.JobStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap, ok := reg.Snapshot(jobID, jobs.JobOwner{ThreadID: threadID, Handle: ownerToken})
		if ok && snap.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job status did not reach %s", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newDirectTrinoToolHandler(t *testing.T, host string) *trinopkg.Handler {
	t.Helper()
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	cfg := settings.Get()
	cfg.TrinoHost = testStrPtrTrino(host)
	cfg.TrinoUser = testStrPtrTrino("alice")
	if err := settings.Save(cfg); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	sec, err := secrets.NewStore(dir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("passphrase"); err != nil {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("passphrase"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := sec.SetTrinoNone(); err != nil {
		t.Fatalf("SetTrinoNone: %v", err)
	}
	return trinopkg.NewHandler(settings, sec)
}

func testStrPtrTrino(v string) *string {
	s := v
	return &s
}

func mustJSONTrino(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return json.RawMessage(b)
}

func mustMapTrino(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T want map[string]any", v)
	}
	return m
}

func sliceAnyTrino(v any) []any {
	items, _ := v.([]any)
	return items
}

func anyStringTrino(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
