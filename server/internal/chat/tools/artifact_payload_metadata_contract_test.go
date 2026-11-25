package tools

import (
	"strings"
	"testing"
)

func TestLargeRowsArtifactPayloadCarriesDatasourceQueryPresentation(t *testing.T) {
	t.Parallel()

	for _, toolName := range []string{
		"run_bigquery_query",
		"run_databricks_query",
		"run_trino_query",
		"run_snowflake_query",
		"get_query_results",
	} {
		toolName := toolName
		t.Run(toolName, func(t *testing.T) {
			t.Parallel()

			toolMeta, ok := ToolMetadataByName(toolName)
			if !ok {
				t.Fatalf("missing metadata for %s", toolName)
			}
			artifactID := "artifact-" + strings.ReplaceAll(toolName, "_", "-")
			payload, err := newLargeRowsArtifactPayload(artifactID, "Query Results", []map[string]any{{"n": 1}}, toolName, map[string]any{
				"provider":        DatasourceForToolName(toolName),
				"row_count_total": 1,
			})
			if err != nil {
				t.Fatalf("newLargeRowsArtifactPayload: %v", err)
			}
			if payload == nil {
				t.Fatal("payload=nil")
			}
			metadata := payload.Metadata
			if metadata["tool_name"] != toolName {
				t.Fatalf("tool_name=%#v, want %s; metadata=%#v", metadata["tool_name"], toolName, metadata)
			}
			resultMetadata, ok := metadata["result_metadata"].(map[string]any)
			if !ok {
				t.Fatalf("result_metadata=%T, want object; metadata=%#v", metadata["result_metadata"], metadata)
			}
			if resultMetadata["artifact_id"] != artifactID ||
				resultMetadata["full_artifact_id"] != artifactID ||
				resultMetadata["row_count_total"] != 1 ||
				resultMetadata["content_kind"] != "json_rows" {
				t.Fatalf("result_metadata=%#v, want artifact row metadata", resultMetadata)
			}
			if resultMetadata["tool_name"] != toolName ||
				resultMetadata["native_viewer"] != toolMeta.Presentation.NativeViewer ||
				resultMetadata["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
				resultMetadata["renderer_key"] != toolMeta.Presentation.RendererKey {
				t.Fatalf("result_metadata=%#v, want %s query renderer metadata", resultMetadata, toolName)
			}
			if _, ok := resultMetadata["tool_presentation"].(map[string]any); !ok {
				t.Fatalf("result_metadata missing tool_presentation: %#v", resultMetadata)
			}
			presentation, ok := metadata["tool_presentation"].(map[string]any)
			if !ok {
				t.Fatalf("tool_presentation=%T, want object; metadata=%#v", metadata["tool_presentation"], metadata)
			}
			if presentation["native_viewer"] != toolMeta.Presentation.NativeViewer ||
				presentation["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
				presentation["renderer_key"] != toolMeta.Presentation.RendererKey {
				t.Fatalf("tool_presentation=%#v, want %s presentation", presentation, toolName)
			}
		})
	}
}
