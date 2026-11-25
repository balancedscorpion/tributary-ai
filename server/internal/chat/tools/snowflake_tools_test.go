package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
)

type fakeSnowflakeResponse struct {
	Status int
	Body   map[string]any
}

type fakeSnowflakeHandler struct {
	mu sync.Mutex

	preflightOK      bool
	preflightMessage string

	queryResponses []fakeSnowflakeResponse

	databases       []any
	schemasByDB     map[string][]any
	tablesByScope   map[string][]any
	schemaByFullRef map[string][]any
}

func (h *fakeSnowflakeHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	switch {
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/preflight"):
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
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/query"):
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodDelete {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		if len(h.queryResponses) == 0 {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status": "SUCCEEDED",
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
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/databases"):
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"databases": h.databases})
		return
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/schemas"):
		w.Header().Set("Content-Type", "application/json")
		database := strings.TrimSpace(r.URL.Query().Get("database_name"))
		_ = json.NewEncoder(w).Encode(map[string]any{"schemas": h.schemasByDB[database]})
		return
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/tables"):
		w.Header().Set("Content-Type", "application/json")
		database := strings.TrimSpace(r.URL.Query().Get("database_name"))
		schema := strings.TrimSpace(r.URL.Query().Get("schema_name"))
		key := database + "." + schema
		_ = json.NewEncoder(w).Encode(map[string]any{"tables": h.tablesByScope[key]})
		return
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/table-schema"):
		w.Header().Set("Content-Type", "application/json")
		fullName := strings.TrimSpace(r.URL.Query().Get("full_name"))
		_ = json.NewEncoder(w).Encode(map[string]any{"schema": h.schemaByFullRef[fullName]})
		return
	default:
		http.NotFound(w, r)
	}
}

func TestRunSnowflakeQueryPreflightFailure(t *testing.T) {
	tool := newRunSnowflakeQueryTool()
	handler := &fakeSnowflakeHandler{
		preflightOK:      false,
		preflightMessage: "invalid sql",
	}
	out, err := tool.Execute(context.Background(), ExecContext{
		SnowflakeHTTP: handler,
	}, mustJSONSnowflake(t, map[string]any{
		"sql": "select from",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.OK {
		t.Fatalf("expected preflight failure output")
	}
	result := mustMapSnowflake(t, out.Result)
	if result["preflight_failed"] != true {
		t.Fatalf("preflight_failed=%v, want true", result["preflight_failed"])
	}
	if result["rewrite_required"] != true {
		t.Fatalf("rewrite_required=%v, want true", result["rewrite_required"])
	}
}

func TestRunSnowflakeQueryToolSchemaOmitsFullData(t *testing.T) {
	for _, tool := range []Tool{
		newRunSnowflakeQueryTool(),
		newListSnowflakeDatabasesTool(),
		newListSnowflakeSchemasTool(),
		newListSnowflakeTablesTool(),
		newDescribeSnowflakeTableTool(),
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

	props := toolDefinitionProperties(t, newRunSnowflakeQueryTool().Definition().Function.Parameters)
	requireIntegerRangeSchema(t, props["limit"], "run_snowflake_query.limit", 1, maxQueryPageSize)
	if _, exists := props["full_data"]; exists {
		t.Fatal("run_snowflake_query should not advertise full_data in tool schema")
	}
}

func TestFormatSnowflakeSuccessOutputStoresFullArtifactForPreview(t *testing.T) {
	rowsIn := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		rowsIn = append(rowsIn, map[string]any{"n": i})
	}

	out := formatSnowflakeSuccessOutput(rowsIn, 10, 0, 10, false, 1000, false, map[string]any{
		"provider": "snowflake",
	})
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	result := mustMapSnowflake(t, out.Result)
	rows := sliceAnySnowflake(result["rows"])
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

func TestRunSnowflakeQueryAsyncLifecycle(t *testing.T) {
	tool := newRunSnowflakeQueryTool()
	handler := &fakeSnowflakeHandler{
		preflightOK: true,
		queryResponses: []fakeSnowflakeResponse{
			{
				Body: map[string]any{
					"status":   "RUNNING",
					"query_id": "sf-q-1",
					"next_uri": "https://snowflake.local/next/1",
				},
			},
			{
				Body: map[string]any{
					"status":   "RUNNING",
					"query_id": "sf-q-1",
					"next_uri": "https://snowflake.local/next/2",
				},
			},
			{
				Body: map[string]any{
					"status":   "SUCCEEDED",
					"query_id": "sf-q-1",
					"rows": []any{
						map[string]any{"n": float64(1)},
						map[string]any{"n": float64(2)},
					},
					"schema": []any{
						map[string]any{"name": "n", "type": "number"},
					},
				},
			},
		},
	}
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	execCtx := ExecContext{
		SnowflakeHTTP:         handler,
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}
	out, err := tool.Execute(context.Background(), execCtx, mustJSONSnowflake(t, map[string]any{
		"sql": "select * from t",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected running response, got error: %#v", out.Result)
	}
	result := mustMapSnowflake(t, out.Result)
	if strings.TrimSpace(anyStringSnowflake(result["status"])) != "running" {
		t.Fatalf("status=%q, want running", anyStringSnowflake(result["status"]))
	}
	if strings.TrimSpace(anyStringSnowflake(out.QueryMetadata["provider"])) != "snowflake" {
		t.Fatalf("provider=%q, want snowflake", anyStringSnowflake(out.QueryMetadata["provider"]))
	}
	meta := requireDatasourceResultMetadata(t, out, "run_snowflake_query", "snowflake", "query_running")
	if meta["async_query"] != true || meta["status"] != "running" {
		t.Fatalf("ResultMetadata=%#v, want running async query metadata", meta)
	}

	jobID := strings.TrimSpace(anyStringSnowflake(result["job_id"]))
	ownerToken := strings.TrimSpace(anyStringSnowflake(result["owner_token"]))
	if jobID == "" || ownerToken == "" {
		t.Fatalf("missing async job references: job_id=%q owner_token=%q", jobID, ownerToken)
	}
	if meta["job_id"] != jobID || meta["owner_token"] != ownerToken {
		t.Fatalf("ResultMetadata job refs=%#v, want job_id=%q owner_token=%q", meta, jobID, ownerToken)
	}
	if !strings.HasPrefix(jobID, "qj-sf-") {
		t.Fatalf("job_id=%q, want qj-sf-* prefix", jobID)
	}

	waitForJobStatusSnowflake(t, reg, jobID, "thread-1", ownerToken, jobs.JobStatusCompleted)
	getTool := newGetQueryResultsTool()
	getOut, err := getTool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSONSnowflake(t, map[string]any{
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
	rows := sliceAnySnowflake(mustMapSnowflake(t, getOut.Result)["rows"])
	if len(rows) != 2 {
		t.Fatalf("rows=%d, want 2", len(rows))
	}
	if strings.TrimSpace(anyStringSnowflake(getOut.QueryMetadata["provider"])) != "snowflake" {
		t.Fatalf("metadata provider=%q, want snowflake", anyStringSnowflake(getOut.QueryMetadata["provider"]))
	}
}

func TestRunSnowflakeQuerySyncSetsProviderMetadata(t *testing.T) {
	tool := newRunSnowflakeQueryTool()
	handler := &fakeSnowflakeHandler{
		preflightOK: true,
		queryResponses: []fakeSnowflakeResponse{
			{
				Body: map[string]any{
					"status":   "SUCCEEDED",
					"query_id": "sf-sync-1",
					"rows": []any{
						map[string]any{"id": float64(1)},
					},
				},
			},
		},
	}
	out, err := tool.Execute(context.Background(), ExecContext{
		SnowflakeHTTP:         handler,
		DatabaseQueryRowLimit: 1000,
	}, mustJSONSnowflake(t, map[string]any{
		"sql": "select 1 as id",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got %#v", out.Result)
	}
	if got := strings.TrimSpace(anyStringSnowflake(out.QueryMetadata["provider"])); got != "snowflake" {
		t.Fatalf("provider=%q, want snowflake", got)
	}
}

func TestRunSnowflakeQueryAsyncWhenInitialResponseCompleted(t *testing.T) {
	tool := newRunSnowflakeQueryTool()
	handler := &fakeSnowflakeHandler{
		preflightOK: true,
		queryResponses: []fakeSnowflakeResponse{
			{
				Body: map[string]any{
					"rows": []any{
						map[string]any{"id": float64(7)},
					},
					"query_metadata": map[string]any{
						"provider":               "snowflake",
						"snowflake_query_handle": "sf-done-1",
						"status":                 "completed",
					},
				},
			},
		},
	}
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	execCtx := ExecContext{
		SnowflakeHTTP:         handler,
		Jobs:                  reg,
		ThreadID:              "thread-2",
		RunID:                 "run-2",
		DatabaseQueryRowLimit: 1000,
	}
	out, err := tool.Execute(context.Background(), execCtx, mustJSONSnowflake(t, map[string]any{
		"sql": "select 7 as id",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected running response, got %#v", out.Result)
	}
	result := mustMapSnowflake(t, out.Result)
	if strings.TrimSpace(anyStringSnowflake(result["status"])) != "running" {
		t.Fatalf("status=%q, want running", anyStringSnowflake(result["status"]))
	}
	meta := requireDatasourceResultMetadata(t, out, "run_snowflake_query", "snowflake", "query_running")
	if meta["async_query"] != true || meta["status"] != "running" {
		t.Fatalf("ResultMetadata=%#v, want running async query metadata", meta)
	}
	jobID := strings.TrimSpace(anyStringSnowflake(result["job_id"]))
	ownerToken := strings.TrimSpace(anyStringSnowflake(result["owner_token"]))
	if jobID == "" || ownerToken == "" {
		t.Fatalf("missing async references: job_id=%q owner_token=%q", jobID, ownerToken)
	}
	if meta["job_id"] != jobID || meta["owner_token"] != ownerToken {
		t.Fatalf("ResultMetadata job refs=%#v, want job_id=%q owner_token=%q", meta, jobID, ownerToken)
	}

	waitForJobStatusSnowflake(t, reg, jobID, "thread-2", ownerToken, jobs.JobStatusCompleted)
	getTool := newGetQueryResultsTool()
	getOut, err := getTool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-2",
		RunID:                 "run-2",
		DatabaseQueryRowLimit: 1000,
	}, mustJSONSnowflake(t, map[string]any{
		"job_id":      jobID,
		"owner_token": ownerToken,
		"limit":       10,
	}))
	if err != nil {
		t.Fatalf("get_query_results: %v", err)
	}
	if !getOut.OK {
		t.Fatalf("expected get_query_results success")
	}
	rows := sliceAnySnowflake(mustMapSnowflake(t, getOut.Result)["rows"])
	if len(rows) != 1 {
		t.Fatalf("rows=%d, want 1", len(rows))
	}
}

func TestSnowflakeMetadataTools(t *testing.T) {
	handler := &fakeSnowflakeHandler{
		preflightOK: true,
		databases: []any{
			map[string]any{"name": "analytics"},
		},
		schemasByDB: map[string][]any{
			"analytics": {
				map[string]any{"name": "public"},
			},
		},
		tablesByScope: map[string][]any{
			"analytics.public": {
				map[string]any{"name": "events"},
			},
		},
		schemaByFullRef: map[string][]any{
			"analytics.public.events": {
				map[string]any{"name": "event_id", "type": "number"},
			},
		},
	}
	ctx := context.Background()

	databasesOut, err := newListSnowflakeDatabasesTool().Execute(ctx, ExecContext{SnowflakeHTTP: handler}, mustJSONSnowflake(t, map[string]any{}))
	if err != nil {
		t.Fatalf("list_snowflake_databases: %v", err)
	}
	if !databasesOut.OK {
		t.Fatalf("list_snowflake_databases should succeed")
	}
	if got := len(sliceAnySnowflake(mustMapSnowflake(t, databasesOut.Result)["databases"])); got != 1 {
		t.Fatalf("databases=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, databasesOut, "list_snowflake_databases", "snowflake", "database_list")

	schemasOut, err := newListSnowflakeSchemasTool().Execute(ctx, ExecContext{SnowflakeHTTP: handler}, mustJSONSnowflake(t, map[string]any{
		"database_name": "analytics",
	}))
	if err != nil {
		t.Fatalf("list_snowflake_schemas: %v", err)
	}
	if got := len(sliceAnySnowflake(mustMapSnowflake(t, schemasOut.Result)["schemas"])); got != 1 {
		t.Fatalf("schemas=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, schemasOut, "list_snowflake_schemas", "snowflake", "schema_list")

	tablesOut, err := newListSnowflakeTablesTool().Execute(ctx, ExecContext{SnowflakeHTTP: handler}, mustJSONSnowflake(t, map[string]any{
		"database_name": "analytics",
		"schema_name":   "public",
	}))
	if err != nil {
		t.Fatalf("list_snowflake_tables: %v", err)
	}
	if got := len(sliceAnySnowflake(mustMapSnowflake(t, tablesOut.Result)["tables"])); got != 1 {
		t.Fatalf("tables=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, tablesOut, "list_snowflake_tables", "snowflake", "table_list")

	describeOut, err := newDescribeSnowflakeTableTool().Execute(ctx, ExecContext{SnowflakeHTTP: handler}, mustJSONSnowflake(t, map[string]any{
		"full_name": "analytics.public.events",
	}))
	if err != nil {
		t.Fatalf("describe_snowflake_table: %v", err)
	}
	if got := len(sliceAnySnowflake(mustMapSnowflake(t, describeOut.Result)["schema"])); got != 1 {
		t.Fatalf("schema=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, describeOut, "describe_snowflake_table", "snowflake", "table_schema")
}

func TestSnowflakeToolsRegisteredInRegistry(t *testing.T) {
	reg := NewRegistry(nil)
	defs := reg.DatasourceToolDefinitions("snowflake")
	if len(defs) == 0 {
		t.Fatalf("expected snowflake datasource tool definitions")
	}
	names := map[string]struct{}{}
	for _, def := range defs {
		names[strings.TrimSpace(def.Function.Name)] = struct{}{}
	}
	for _, want := range []string{
		"run_snowflake_query",
		"list_snowflake_databases",
		"list_snowflake_schemas",
		"list_snowflake_tables",
		"describe_snowflake_table",
		"check_query_status",
		"get_query_results",
		"cancel_query",
	} {
		if _, ok := names[want]; !ok {
			t.Fatalf("missing %s from datasource defs", want)
		}
	}
}

func waitForJobStatusSnowflake(t *testing.T, reg *jobs.Registry, jobID, threadID, ownerToken string, want jobs.JobStatus) {
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

func mustJSONSnowflake(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return json.RawMessage(b)
}

func mustMapSnowflake(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T want map[string]any", v)
	}
	return m
}

func sliceAnySnowflake(v any) []any {
	items, _ := v.([]any)
	return items
}

func anyStringSnowflake(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}
