package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/config"
)

func TestDefaultBQProjectIDUsesGoogleProjectIDWhenSet(t *testing.T) {
	dir := t.TempDir()
	s, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	settings := s.Get()
	settings.GoogleProjectID = strPtr("legacy-project")
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			LastProjectID: strPtr("fallback-project"),
		},
	}
	if err := s.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := defaultBQProjectID(bigquery.NewHandler(s, nil))
	if got != "legacy-project" {
		t.Fatalf("defaultBQProjectID = %q, want %q", got, "legacy-project")
	}
}

func TestDefaultBQProjectIDFallsBackToLastProjectID(t *testing.T) {
	dir := t.TempDir()
	s, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	settings := s.Get()
	settings.GoogleProjectID = nil
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			LastProjectID: strPtr("fallback-project"),
		},
	}
	if err := s.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := defaultBQProjectID(bigquery.NewHandler(s, nil))
	if got != "fallback-project" {
		t.Fatalf("defaultBQProjectID = %q, want %q", got, "fallback-project")
	}
}

func TestDefaultBQProjectIDUsesFallbackWhenGoogleProjectIDBlank(t *testing.T) {
	dir := t.TempDir()
	s, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	settings := s.Get()
	settings.GoogleProjectID = strPtr(" ")
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{
			LastProjectID: strPtr("fallback-project"),
		},
	}
	if err := s.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := defaultBQProjectID(bigquery.NewHandler(s, nil))
	if got != "fallback-project" {
		t.Fatalf("defaultBQProjectID = %q, want %q", got, "fallback-project")
	}
}

func TestDefaultBQProjectIDReturnsEmptyWhenNeitherConfigured(t *testing.T) {
	dir := t.TempDir()
	s, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	settings := s.Get()
	settings.GoogleProjectID = nil
	settings.DataSources = &config.DatasourceConnections{
		BigQuery: &config.DatasourceConnectionState{},
	}
	if err := s.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got := defaultBQProjectID(bigquery.NewHandler(s, nil))
	if got != "" {
		t.Fatalf("defaultBQProjectID = %q, want empty", got)
	}
}

func TestBigQueryScanBudgetsUseConnectionLevelsAndLegacyFallback(t *testing.T) {
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	handler := bigquery.NewHandler(settings, nil)
	// Defaults: 100 GiB automatic limit, NO approved ceiling. approved == 0
	// is load-bearing — collapsing it to the automatic limit turns every
	// oversized estimate into a hard over_budget rejection instead of an
	// approval prompt.
	automatic, approved := bigQueryScanBudgets(handler)
	if automatic != 100<<30 || approved != 0 {
		t.Fatalf("defaults=%d/%d want %d/0", automatic, approved, int64(100<<30))
	}

	next := settings.Get()
	autoValue := int64(250 << 30)
	approvedValue := int64(500 << 30)
	next.DataSources.BigQuery.AutomaticScanBytes = &autoValue
	next.DataSources.BigQuery.ApprovedScanBytes = &approvedValue
	if err := settings.Save(next); err != nil {
		t.Fatalf("Save: %v", err)
	}
	automatic, approved = bigQueryScanBudgets(handler)
	if automatic != autoValue || approved != approvedValue {
		t.Fatalf("configured=%d/%d want %d/%d", automatic, approved, autoValue, approvedValue)
	}

	next = settings.Get()
	autoValue = int64(50 << 30)
	next.DataSources.BigQuery.AutomaticScanBytes = &autoValue
	next.DataSources.BigQuery.ApprovedScanBytes = nil
	if err := settings.Save(next); err != nil {
		t.Fatalf("Save automatic-only: %v", err)
	}
	automatic, approved = bigQueryScanBudgets(handler)
	if automatic != autoValue || approved != 0 {
		t.Fatalf("automatic-only=%d/%d want %d/0 (no ceiling)", automatic, approved, autoValue)
	}

	// An explicit ceiling below the automatic limit clamps up to it (the
	// settings layer enforces the same invariant; this is defense in depth).
	next = settings.Get()
	lowApproved := int64(10 << 30)
	next.DataSources.BigQuery.ApprovedScanBytes = &lowApproved
	if err := settings.Save(next); err != nil {
		t.Fatalf("Save low-approved: %v", err)
	}
	automatic, approved = bigQueryScanBudgets(handler)
	if automatic != autoValue || approved != autoValue {
		t.Fatalf("low-approved=%d/%d want %d/%d", automatic, approved, autoValue, autoValue)
	}
}

func TestRunBigQueryQueryToolSchemaOmitsFullData(t *testing.T) {
	for _, tool := range []Tool{
		newRunBigQueryQueryTool(),
		newListBQDatasetsTool(),
		newListBQTablesTool(),
		newGetBQTableSchemaTool(),
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

	props := toolDefinitionProperties(t, newRunBigQueryQueryTool().Definition().Function.Parameters)
	limit, ok := props["limit"].(map[string]any)
	if !ok {
		t.Fatalf("run_bigquery_query.limit schema=%T, want object", props["limit"])
	}
	if limit["type"] != "integer" || limit["minimum"] != 1 || limit["maximum"] != maxQueryPageSize {
		t.Fatalf("run_bigquery_query.limit schema=%#v, want integer range 1..%d", limit, maxQueryPageSize)
	}
}

func TestBigQueryMetadataToolsUseFirstClassResultMetadata(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/bigquery/datasets":
			if r.URL.Query().Get("project_id") != "analytics-prod" {
				http.Error(w, "bad project", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"datasets": []map[string]any{{"id": "raw", "location": "EU"}},
			})
		case "/api/bigquery/tables":
			if r.URL.Query().Get("project_id") != "analytics-prod" || r.URL.Query().Get("dataset_id") != "raw" {
				http.Error(w, "bad table scope", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tables": []any{"events"},
			})
		case "/api/bigquery/schema":
			if r.URL.Query().Get("project_id") != "analytics-prod" || r.URL.Query().Get("dataset_id") != "raw" || r.URL.Query().Get("table_id") != "events" {
				http.Error(w, "bad schema scope", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": []map[string]any{{"name": "event_id", "type": "STRING"}},
			})
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	datasetsOut, err := newListBQDatasetsTool().Execute(ctx, ExecContext{BigQueryHTTP: handler}, mustJSON(t, map[string]any{
		"project_id": "analytics-prod",
	}))
	if err != nil {
		t.Fatalf("list_bq_datasets: %v", err)
	}
	if got := len(asMap(t, datasetsOut.Result)["datasets"].([]any)); got != 1 {
		t.Fatalf("datasets=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, datasetsOut, "list_bq_datasets", "bigquery", "dataset_list")

	tablesOut, err := newListBQTablesTool().Execute(ctx, ExecContext{BigQueryHTTP: handler}, mustJSON(t, map[string]any{
		"project_id": "analytics-prod",
		"dataset_id": "raw",
	}))
	if err != nil {
		t.Fatalf("list_bq_tables: %v", err)
	}
	if got := len(asMap(t, tablesOut.Result)["tables"].([]any)); got != 1 {
		t.Fatalf("tables=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, tablesOut, "list_bq_tables", "bigquery", "table_list")

	schemaOut, err := newGetBQTableSchemaTool().Execute(ctx, ExecContext{BigQueryHTTP: handler}, mustJSON(t, map[string]any{
		"project_id": "analytics-prod",
		"dataset_id": "raw",
		"table_id":   "events",
	}))
	if err != nil {
		t.Fatalf("get_bq_table_schema: %v", err)
	}
	if got := len(asMap(t, schemaOut.Result)["schema"].([]any)); got != 1 {
		t.Fatalf("schema=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, schemaOut, "get_bq_table_schema", "bigquery", "table_schema")
}

func TestRunBigQueryQueryPreflightFailure(t *testing.T) {
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tool := newRunBigQueryQueryTool()
	out, err := tool.Execute(context.Background(), ExecContext{
		BigQuery: bigquery.NewHandler(settings, nil),
	}, mustJSON(t, map[string]any{
		"project_id": "analytics-prod",
		"sql":        "delete from dataset.table where true",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.OK {
		t.Fatalf("expected preflight failure output")
	}
	result := asMap(t, out.Result)
	if result["preflight_failed"] != true {
		t.Fatalf("preflight_failed=%v, want true", result["preflight_failed"])
	}
	if result["rewrite_required"] != true {
		t.Fatalf("rewrite_required=%v, want true", result["rewrite_required"])
	}
}

func TestFormatBigQuerySuccessOutputStoresFullArtifactForPreview(t *testing.T) {
	rowsIn := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		rowsIn = append(rowsIn, map[string]any{"n": i})
	}

	out := formatBigQuerySuccessOutput(rowsIn, 10, 0, 10, false, 1000, false, map[string]any{
		"provider": "bigquery",
	})
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	result := asMap(t, out.Result)
	rows, _ := result["rows"].([]any)
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

func TestFormatBigQuerySuccessOutputPreservesProviderIdentityMetadata(t *testing.T) {
	out := formatBigQuerySuccessOutput(
		[]map[string]any{{"n": 1}},
		10,
		0,
		10,
		false,
		1000,
		false,
		map[string]any{
			"provider":         "bigquery",
			"project_id":       "analytics-prod",
			"job_id":           "job_123",
			"job_location":     "EU",
			"warehouse_job_id": "job_123",
			"provider_job_id":  "job_123",
			"provider_job_url": "https://console.cloud.google.com/bigquery?project=analytics-prod&j=bq%3AEU%3Ajob_123&page=queryresults",
			"schema":           []map[string]any{{"name": "n", "type": "INTEGER"}},
		},
	)
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	for _, key := range []string{"project_id", "job_id", "job_location", "warehouse_job_id", "provider_job_id", "provider_job_url", "schema"} {
		if _, ok := out.QueryMetadata[key]; !ok {
			t.Fatalf("QueryMetadata missing %s: %#v", key, out.QueryMetadata)
		}
		if _, ok := out.ResultMetadata[key]; !ok {
			t.Fatalf("ResultMetadata missing %s: %#v", key, out.ResultMetadata)
		}
	}
}

func TestBigQueryCompletedSubmitMetadataEnrichesFastAsyncComplete(t *testing.T) {
	meta := bigQueryCompletedSubmitMetadata(
		"select 1",
		"analytics-prod",
		&bigquery.SubmitResult{
			Completed:   true,
			JobID:       "job_123",
			JobLocation: "EU",
			ProjectID:   "ignored-project",
			Schema:      []map[string]any{{"name": "n", "type": "INTEGER"}},
			Metadata: map[string]any{
				"duration_ms": 12,
			},
		},
		"Count rows.",
	)
	want := map[string]any{
		"provider":           "bigquery",
		"sql":                "select 1",
		"sql_preview":        "select 1",
		"project_id":         "analytics-prod",
		"job_id":             "job_123",
		"job_location":       "EU",
		"warehouse_job_id":   "job_123",
		"warehouse_location": "EU",
		"provider_job_id":    "job_123",
		"description":        "Count rows.",
	}
	for key, wantValue := range want {
		if got := meta[key]; got != wantValue {
			t.Fatalf("%s=%#v want %#v in metadata %#v", key, got, wantValue, meta)
		}
	}
	if got := meta["provider_job_url"]; got == "" {
		t.Fatalf("provider_job_url missing from metadata %#v", meta)
	}
	if got, ok := meta["schema"].([]map[string]any); !ok || len(got) != 1 {
		t.Fatalf("schema=%#v want one schema field", meta["schema"])
	}
}

func strPtr(v string) *string {
	s := v
	return &s
}
