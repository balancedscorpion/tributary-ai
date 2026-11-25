package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
)

func TestRunDatabricksQueryToolSchemaOmitsFullData(t *testing.T) {
	for _, tool := range []Tool{
		newRunDatabricksQueryTool(),
		newListDatabricksCatalogsTool(),
		newListDatabricksSchemasTool(),
		newListDatabricksTablesTool(),
		newDescribeDatabricksTableTool(),
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

	props := toolDefinitionProperties(t, newRunDatabricksQueryTool().Definition().Function.Parameters)
	requireIntegerRangeSchema(t, props["limit"], "run_databricks_query.limit", 1, maxQueryPageSize)
	if _, exists := props["full_data"]; exists {
		t.Fatal("run_databricks_query should not advertise full_data in tool schema")
	}
	for _, tool := range []Tool{
		newListDatabricksCatalogsTool(),
		newListDatabricksSchemasTool(),
		newListDatabricksTablesTool(),
	} {
		metadataProps := toolDefinitionProperties(t, tool.Definition().Function.Parameters)
		if _, ok := metadataProps["page_token"]; !ok {
			t.Fatalf("%s should advertise page_token", tool.Definition().Function.Name)
		}
	}
}

func TestDatabricksMetadataToolsUseFirstClassResultMetadata(t *testing.T) {
	const pageToken = "opaque+/= page"
	const nextPageToken = "next+/= page"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/databricks/table-schema" && r.URL.Query().Get("page_token") != pageToken {
			http.Error(w, "bad page token", http.StatusBadRequest)
			return
		}
		switch r.URL.Path {
		case "/api/databricks/catalogs":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"catalogs":        []map[string]any{{"name": "hive"}},
				"next_page_token": nextPageToken,
			})
		case "/api/databricks/schemas":
			if r.URL.Query().Get("catalog_name") != "hive" {
				http.Error(w, "bad catalog", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemas":         []map[string]any{{"name": "default"}},
				"next_page_token": nextPageToken,
			})
		case "/api/databricks/tables":
			if r.URL.Query().Get("catalog_name") != "hive" || r.URL.Query().Get("schema_name") != "default" {
				http.Error(w, "bad table scope", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"tables":          []map[string]any{{"name": "events"}},
				"next_page_token": nextPageToken,
			})
		case "/api/databricks/table-schema":
			if r.URL.Query().Get("full_name") != "hive.default.events" {
				http.Error(w, "bad full name", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schema": []map[string]any{{"name": "event_id", "type": "string"}},
			})
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()

	catalogsOut, err := newListDatabricksCatalogsTool().Execute(ctx, ExecContext{DatabricksHTTP: handler}, mustJSON(t, map[string]any{
		"page_token": encodeDatabricksMetadataPageToken("catalogs", "", "", "", pageToken),
	}))
	if err != nil {
		t.Fatalf("list_databricks_catalogs: %v", err)
	}
	if got := len(asMap(t, catalogsOut.Result)["catalogs"].([]any)); got != 1 {
		t.Fatalf("catalogs=%d, want 1", got)
	}
	requireDatabricksPaginationResult(t, catalogsOut, "catalogs", "", "", nextPageToken)
	requireDatasourceResultMetadata(t, catalogsOut, "list_databricks_catalogs", "databricks", "catalog_list")

	schemasOut, err := newListDatabricksSchemasTool().Execute(ctx, ExecContext{DatabricksHTTP: handler}, mustJSON(t, map[string]any{
		"catalog_name": "hive",
		"page_token":   encodeDatabricksMetadataPageToken("schemas", "hive", "", "", pageToken),
	}))
	if err != nil {
		t.Fatalf("list_databricks_schemas: %v", err)
	}
	if got := len(asMap(t, schemasOut.Result)["schemas"].([]any)); got != 1 {
		t.Fatalf("schemas=%d, want 1", got)
	}
	requireDatabricksPaginationResult(t, schemasOut, "schemas", "hive", "", nextPageToken)
	requireDatasourceResultMetadata(t, schemasOut, "list_databricks_schemas", "databricks", "schema_list")

	tablesOut, err := newListDatabricksTablesTool().Execute(ctx, ExecContext{DatabricksHTTP: handler}, mustJSON(t, map[string]any{
		"catalog_name": "hive",
		"schema_name":  "default",
		"page_token":   encodeDatabricksMetadataPageToken("tables", "hive", "default", "", pageToken),
	}))
	if err != nil {
		t.Fatalf("list_databricks_tables: %v", err)
	}
	if got := len(asMap(t, tablesOut.Result)["tables"].([]any)); got != 1 {
		t.Fatalf("tables=%d, want 1", got)
	}
	requireDatabricksPaginationResult(t, tablesOut, "tables", "hive", "default", nextPageToken)
	requireDatasourceResultMetadata(t, tablesOut, "list_databricks_tables", "databricks", "table_list")

	describeOut, err := newDescribeDatabricksTableTool().Execute(ctx, ExecContext{DatabricksHTTP: handler}, mustJSON(t, map[string]any{
		"full_name": "hive.default.events",
	}))
	if err != nil {
		t.Fatalf("describe_databricks_table: %v", err)
	}
	if got := len(asMap(t, describeOut.Result)["schema"].([]any)); got != 1 {
		t.Fatalf("schema=%d, want 1", got)
	}
	requireDatasourceResultMetadata(t, describeOut, "describe_databricks_table", "databricks", "table_schema")
}

func requireDatabricksPaginationResult(t *testing.T, out Output, resource, catalog, schema, wantProviderToken string) {
	t.Helper()
	result := asMap(t, out.Result)
	if hasMore, _ := result["has_more"].(bool); !hasMore {
		t.Fatalf("has_more=%#v, want true", result["has_more"])
	}
	got, _ := result["next_page_token"].(string)
	providerToken, err := decodeDatabricksMetadataPageToken(got, resource, catalog, schema, "")
	if err != nil || providerToken != wantProviderToken {
		t.Fatalf("next_page_token=%q decoded=%q err=%v, want provider token %q", got, providerToken, err, wantProviderToken)
	}
	if _, duplicated := out.ResultMetadata["next_page_token"]; duplicated {
		t.Fatalf("next_page_token must have one canonical location in result, got metadata=%#v", out.ResultMetadata)
	}
}

func TestDatabricksMetadataPageTokensRejectWrongListingBeforeProviderCall(t *testing.T) {
	var calls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(map[string]any{"tables": []any{}})
	})
	tool := newListDatabricksTablesTool()
	validForDefault := encodeDatabricksMetadataPageToken("tables", "hive", "default", "", "provider-next")

	for _, token := range []string{validForDefault, "provider-next"} {
		out, err := tool.Execute(context.Background(), ExecContext{DatabricksHTTP: handler}, mustJSON(t, map[string]any{
			"catalog_name": "hive",
			"schema_name":  "other",
			"page_token":   token,
		}))
		if err != nil || out.OK {
			t.Fatalf("mismatched token output=%+v err=%v", out, err)
		}
	}
	if calls != 0 {
		t.Fatalf("mismatched metadata tokens reached provider %d times", calls)
	}
}

func TestDatabricksMetadataPageTokenIntegrityAndWorkspaceBinding(t *testing.T) {
	const (
		workspace = "workspace-a"
		cursor    = "opaque+/= provider cursor"
	)
	token := encodeDatabricksMetadataPageToken("tables", "hive", "default", workspace, cursor)
	decoded, err := decodeDatabricksMetadataPageToken(token, "tables", "hive", "default", workspace)
	if err != nil || decoded != cursor {
		t.Fatalf("valid token decoded=%q err=%v, want %q", decoded, err, cursor)
	}

	if _, err := decodeDatabricksMetadataPageToken(token, "tables", "hive", "default", "workspace-b"); databricksMetadataTokenErrorReason(err) != "workspace_mismatch" {
		t.Fatalf("workspace mismatch err=%v reason=%q", err, databricksMetadataTokenErrorReason(err))
	}

	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, databricksMetadataPageTokenPrefix))
	if err != nil {
		t.Fatalf("decode wrapper: %v", err)
	}
	var envelope databricksMetadataPageTokenEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	envelope.Cursor += "corrupted"
	mutatedPayload, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode mutated envelope: %v", err)
	}
	mutated := databricksMetadataPageTokenPrefix + base64.RawURLEncoding.EncodeToString(mutatedPayload)
	if _, err := decodeDatabricksMetadataPageToken(mutated, "tables", "hive", "default", workspace); databricksMetadataTokenErrorReason(err) != "integrity_check_failed" {
		t.Fatalf("mutated token err=%v reason=%q", err, databricksMetadataTokenErrorReason(err))
	}
}

func TestDatabricksMetadataPageTokenProviderRejectionRequiresRestart(t *testing.T) {
	var calls int
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Input byte array has wrong 4-byte ending unit",
		})
	})
	out, err := newListDatabricksTablesTool().Execute(context.Background(), ExecContext{DatabricksHTTP: handler}, mustJSON(t, map[string]any{
		"catalog_name": "hive",
		"schema_name":  "default",
		"page_token":   encodeDatabricksMetadataPageToken("tables", "hive", "default", "", "provider-next"),
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out.OK {
		t.Fatalf("provider rejection unexpectedly succeeded: %#v", out.Result)
	}
	result := asMap(t, out.Result)
	if result["status"] != "restart_required" || result["restart_required"] != true || result["page_token_invalid"] != true {
		t.Fatalf("provider rejection missing structured restart contract: %#v", result)
	}
	if result["reason"] != "provider_rejected" {
		t.Fatalf("reason=%#v, want provider_rejected", result["reason"])
	}
	if strings.Contains(strings.ToLower(anyToString(result["error"])), "input byte array") {
		t.Fatalf("raw upstream decoder error leaked to tool result: %#v", result)
	}
	if calls != 1 {
		t.Fatalf("provider calls=%d, want 1", calls)
	}
}

func databricksMetadataTokenErrorReason(err error) string {
	var tokenErr *databricksMetadataPageTokenError
	if errors.As(err, &tokenErr) {
		return tokenErr.reason
	}
	return ""
}

func TestRunDatabricksQueryPreflightFailure(t *testing.T) {
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	tool := newRunDatabricksQueryTool()
	out, err := tool.Execute(context.Background(), ExecContext{
		Databricks: databricks.NewHandler(settings, nil),
	}, mustJSON(t, map[string]any{
		"sql": "delete from sales.orders where true",
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

func TestFormatDatabricksSuccessOutputStoresFullArtifactForPreview(t *testing.T) {
	rowsIn := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		rowsIn = append(rowsIn, map[string]any{"n": i})
	}

	out := formatDatabricksSuccessOutput(rowsIn, 10, 0, 10, false, 1000, false, map[string]any{
		"provider": "databricks",
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
