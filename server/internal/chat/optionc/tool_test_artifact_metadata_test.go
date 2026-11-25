package optionc

import (
	"encoding/json"
	"testing"

	"ai-analyst-local/internal/chat/tools"
)

func TestToolTestResultMarshalJSONNormalizesArtifactWithoutPayloadMetadata(t *testing.T) {
	result := ToolTestResult{
		ToolName: "custom tool",
		Output: tools.Output{
			OK:           true,
			ResultFormat: tools.ResultFormatText,
			Result:       "ok",
			Artifact: &tools.ArtifactPayload{
				Content: "artifact body",
			},
		},
	}

	artifact := decodeToolTestArtifactJSON(t, result)
	if artifact.ID != "tool-test-custom-tool-artifact" {
		t.Fatalf("artifact id=%q, want fallback tool-test id", artifact.ID)
	}
	if artifact.Type != "code" {
		t.Fatalf("artifact type=%q, want code fallback", artifact.Type)
	}
	if artifact.Title != "custom tool" {
		t.Fatalf("artifact title=%q, want tool name fallback", artifact.Title)
	}
	if artifact.Content != "artifact body" {
		t.Fatalf("artifact content=%q, want payload content", artifact.Content)
	}
	metadata := artifact.Metadata
	if metadata["tool_test"] != true ||
		metadata["tool_name"] != "custom tool" ||
		metadata["ok"] != true ||
		metadata["placeholder"] != false ||
		metadata["result_format"] != string(tools.ResultFormatText) ||
		metadata["raw_result"] != "ok" {
		t.Fatalf("artifact metadata missing normalized fields: %#v", metadata)
	}
	if args, ok := metadata["tool_args"].(map[string]any); !ok || len(args) != 0 {
		t.Fatalf("tool_args=%#v, want empty object", metadata["tool_args"])
	}
}

func TestToolTestResultMarshalJSONMergesArtifactAndOutputResultMetadata(t *testing.T) {
	toolMeta, ok := tools.ToolMetadataByName("build_chart_artifact")
	if !ok {
		t.Fatal("missing build_chart_artifact metadata")
	}
	result := ToolTestResult{
		ToolName:     "build_chart_artifact",
		Presentation: toolMeta.Presentation,
		Args: map[string]any{
			"source": map[string]any{"artifact_id": "source-1"},
		},
		Output: tools.Output{
			OK:           true,
			ResultFormat: tools.ResultFormatJSON,
			Result: map[string]any{
				"preview_only": true,
			},
			QueryMetadata: map[string]any{
				"query_id": "query-1",
			},
			ResultMetadata: map[string]any{
				"kind":              "chart",
				"renderer_strategy": "artifact_type",
				"renderer_key":      "chart",
				"native_viewer":     "chart",
				"tool_presentation": map[string]any{
					"renderer_key":  "chart",
					"native_viewer": "chart",
				},
			},
			Artifact: &tools.ArtifactPayload{
				Type:    "data",
				Title:   "Full Chart Dataset",
				Content: `[{"x":1,"y":2}]`,
				Metadata: map[string]any{
					"full_artifact_id": "chart-full-id",
					"result_metadata": map[string]any{
						"payload_marker":  "payload-specific",
						"row_count_total": 1,
					},
				},
			},
		},
	}

	artifact := decodeToolTestArtifactJSON(t, result)
	if artifact.ID != "chart-full-id" {
		t.Fatalf("artifact id=%q, want id from payload metadata", artifact.ID)
	}
	if artifact.Type != "data" {
		t.Fatalf("artifact type=%q, want data", artifact.Type)
	}
	metadata := artifact.Metadata
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object: %#v", metadata["result_metadata"], metadata)
	}
	if resultMetadata["payload_marker"] != "payload-specific" ||
		resultMetadata["row_count_total"] != float64(1) {
		t.Fatalf("result_metadata lost payload fields: %#v", resultMetadata)
	}
	if resultMetadata["kind"] != "chart" ||
		resultMetadata["renderer_strategy"] != "artifact_type" ||
		resultMetadata["renderer_key"] != "chart" ||
		resultMetadata["native_viewer"] != "chart" {
		t.Fatalf("result_metadata missing output rendering fields: %#v", resultMetadata)
	}
	if _, ok := resultMetadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("result_metadata missing tool_presentation: %#v", resultMetadata)
	}
	queryMetadata, ok := metadata["query_metadata"].(map[string]any)
	if !ok || queryMetadata["query_id"] != "query-1" {
		t.Fatalf("query_metadata=%#v, want query id", metadata["query_metadata"])
	}
	if args, ok := metadata["tool_args"].(map[string]any); !ok {
		t.Fatalf("tool_args=%T, want object", metadata["tool_args"])
	} else if source, ok := args["source"].(map[string]any); !ok || source["artifact_id"] != "source-1" {
		t.Fatalf("tool_args.source=%#v, want source artifact id", args["source"])
	}
	if presentation, ok := metadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("tool_presentation=%T, want object", metadata["tool_presentation"])
	} else if presentation["renderer_key"] != "chart" || presentation["native_viewer"] != "chart" {
		t.Fatalf("tool_presentation=%#v, want chart renderer", presentation)
	}
}

func decodeToolTestArtifactJSON(t *testing.T, result ToolTestResult) toolTestArtifactJSON {
	t.Helper()
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Marshal ToolTestResult: %v", err)
	}
	var decoded struct {
		Output struct {
			Artifact *toolTestArtifactJSON `json:"artifact"`
		} `json:"output"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal ToolTestResult JSON: %v", err)
	}
	if decoded.Output.Artifact == nil {
		t.Fatalf("output.artifact missing from JSON: %s", string(raw))
	}
	return *decoded.Output.Artifact
}
