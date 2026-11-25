package tools

import "testing"

func TestLargeRowsArtifactPayloadCarriesToolRenderingMetadata(t *testing.T) {
	t.Parallel()

	payload, err := newLargeRowsArtifactPayload("artifact-large", "Rows", []map[string]any{{"region": "North"}}, "display_table", map[string]any{
		"row_count_total": 1,
		"preview_only":    true,
	})
	if err != nil {
		t.Fatalf("newLargeRowsArtifactPayload: %v", err)
	}
	if payload == nil {
		t.Fatal("payload=nil")
	}
	metadata := payload.Metadata
	if metadata["tool_name"] != "display_table" {
		t.Fatalf("tool_name=%#v, want display_table; metadata=%#v", metadata["tool_name"], metadata)
	}
	if _, ok := metadata["tool_args"].(map[string]any); !ok {
		t.Fatalf("tool_args=%T, want object; metadata=%#v", metadata["tool_args"], metadata)
	}
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object; metadata=%#v", metadata["result_metadata"], metadata)
	}
	if resultMetadata["tool_name"] != "display_table" ||
		resultMetadata["kind"] != "table_dataset" ||
		resultMetadata["native_viewer"] != "data" ||
		resultMetadata["renderer_strategy"] != "artifact_type" ||
		resultMetadata["renderer_key"] != "data" ||
		resultMetadata["artifact_id"] != "artifact-large" ||
		resultMetadata["row_count_total"] != 1 {
		t.Fatalf("result_metadata=%#v, want display_table data artifact metadata", resultMetadata)
	}
	presentation, ok := metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("tool_presentation=%T, want object; metadata=%#v", metadata["tool_presentation"], metadata)
	}
	if presentation["native_viewer"] != "data" ||
		presentation["renderer_strategy"] != "artifact_type" ||
		presentation["renderer_key"] != "data" {
		t.Fatalf("tool_presentation=%#v, want display_table presentation", presentation)
	}
}

func TestLargeRowsArtifactPayloadPreservesCallerRenderingMetadata(t *testing.T) {
	t.Parallel()

	payload, err := newLargeRowsArtifactPayload("artifact-custom", "Rows", []map[string]any{{"region": "North"}}, "display_table", map[string]any{
		"tool_args": map[string]any{"source": "fixture"},
		"result_metadata": map[string]any{
			"tool_name":     "display_table",
			"kind":          "custom_table",
			"renderer_key":  "custom_renderer",
			"native_viewer": "data",
			"tool_presentation": map[string]any{
				"renderer_key":  "custom_renderer",
				"native_viewer": "data",
			},
		},
		"tool_presentation": map[string]any{
			"renderer_key":  "caller_renderer",
			"native_viewer": "data",
		},
	})
	if err != nil {
		t.Fatalf("newLargeRowsArtifactPayload: %v", err)
	}
	metadata := payload.Metadata
	toolArgs, ok := metadata["tool_args"].(map[string]any)
	if !ok || toolArgs["source"] != "fixture" {
		t.Fatalf("tool_args=%#v, want caller-supplied args", metadata["tool_args"])
	}
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object", metadata["result_metadata"])
	}
	if resultMetadata["kind"] != "custom_table" || resultMetadata["renderer_key"] != "custom_renderer" {
		t.Fatalf("result_metadata=%#v, want caller-supplied result metadata", resultMetadata)
	}
	presentation, ok := metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("tool_presentation=%T, want object", metadata["tool_presentation"])
	}
	if presentation["renderer_key"] != "caller_renderer" {
		t.Fatalf("tool_presentation=%#v, want caller-supplied presentation", presentation)
	}
}
