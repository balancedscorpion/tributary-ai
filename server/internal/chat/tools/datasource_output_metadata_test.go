package tools

import (
	"reflect"
	"testing"
)

func requireDatasourceResultMetadata(t *testing.T, out Output, toolName, provider, kind string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != provider || meta["product"] != provider || meta["tool_name"] != toolName || meta["kind"] != kind {
		t.Fatalf("%s ResultMetadata=%#v, want provider=%s kind=%s", toolName, meta, provider, kind)
	}
	requireDatasourcePresentationMetadata(t, toolName, meta)
	return meta
}

func requireDatasourcePresentationMetadata(t *testing.T, toolName string, meta map[string]any) {
	t.Helper()
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("%s is missing tool metadata", toolName)
	}
	if meta["result_kind"] != toolMeta.Presentation.ResultKind ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("%s presentation metadata=%#v, want %#v", toolName, meta, toolMeta.Presentation)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("%s ResultMetadata missing tool_presentation: %#v", toolName, meta)
	}
	if presentation["result_kind"] != toolMeta.Presentation.ResultKind ||
		presentation["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		presentation["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		presentation["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("%s nested presentation=%#v, want %#v", toolName, presentation, toolMeta.Presentation)
	}
	if !reflect.DeepEqual(presentation["artifact_types"], toolMeta.Presentation.ArtifactTypes) {
		t.Fatalf("%s artifact_types=%#v, want %#v", toolName, presentation["artifact_types"], toolMeta.Presentation.ArtifactTypes)
	}
}

func TestDatasourceToolResultMetadataUsesCatalogPresentation(t *testing.T) {
	cases := []struct {
		toolName string
		provider string
		kind     string
	}{
		{"run_bigquery_query", "bigquery", "query_running"},
		{"list_bq_datasets", "bigquery", "dataset_list"},
		{"list_bq_tables", "bigquery", "table_list"},
		{"get_bq_table_schema", "bigquery", "table_schema"},
		{"run_databricks_query", "databricks", "query_running"},
		{"list_databricks_catalogs", "databricks", "catalog_list"},
		{"list_databricks_schemas", "databricks", "schema_list"},
		{"list_databricks_tables", "databricks", "table_list"},
		{"describe_databricks_table", "databricks", "table_schema"},
		{"run_trino_query", "trino", "query_running"},
		{"list_trino_catalogs", "trino", "catalog_list"},
		{"list_trino_schemas", "trino", "schema_list"},
		{"list_trino_tables", "trino", "table_list"},
		{"describe_trino_table", "trino", "table_schema"},
		{"run_snowflake_query", "snowflake", "query_running"},
		{"list_snowflake_databases", "snowflake", "database_list"},
		{"list_snowflake_schemas", "snowflake", "schema_list"},
		{"list_snowflake_tables", "snowflake", "table_list"},
		{"describe_snowflake_table", "snowflake", "table_schema"},
	}
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			meta := datasourceToolResultMetadata(tc.toolName, tc.provider, tc.kind, map[string]any{"count": 1})
			if meta["provider"] != tc.provider || meta["product"] != tc.provider || meta["tool_name"] != tc.toolName || meta["kind"] != tc.kind {
				t.Fatalf("metadata=%#v, want provider=%s kind=%s", meta, tc.provider, tc.kind)
			}
			requireDatasourcePresentationMetadata(t, tc.toolName, meta)
		})
	}
}

func TestDatasourceCompletedQueryResultMetadataUsesCatalogPresentation(t *testing.T) {
	t.Parallel()

	meta := datasourceCompletedQueryResultMetadata("run_bigquery_query", "bigquery", map[string]any{
		"status":             "completed",
		"rows_returned":      25,
		"rows_preview_count": 25,
		"page_size":          25,
		"offset":             25,
		"has_more":           true,
		"next_page_token":    "50",
	})
	if meta["provider"] != "bigquery" ||
		meta["tool_name"] != "run_bigquery_query" ||
		meta["kind"] != "query_result" ||
		meta["status"] != "completed" ||
		meta["rows_returned"] != 25 ||
		meta["offset"] != 25 ||
		meta["has_more"] != true {
		t.Fatalf("metadata=%#v, want completed BigQuery page metadata", meta)
	}
	if meta["native_viewer"] != "query" ||
		meta["renderer_strategy"] != "artifact_type" ||
		meta["renderer_key"] != "query" ||
		meta["tool_set_id"] != "bigquery" ||
		meta["category"] != "read" ||
		meta["origin"] != "connected" {
		t.Fatalf("renderer/catalog metadata=%#v, want native query presentation", meta)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("metadata missing tool_presentation: %#v", meta)
	}
}
