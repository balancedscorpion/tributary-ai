package tools

import "testing"

func requireChartCapabilityResultMetadata(t *testing.T, out Output, toolName, engine string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != engine || meta["product"] != engine || meta["tool_name"] != toolName || meta["kind"] != "chart_capabilities" {
		t.Fatalf("%s ResultMetadata=%#v, want %s chart capabilities metadata", toolName, meta, engine)
	}
	if meta["tool_set_id"] != "artifacts" || meta["category"] != "read" || meta["origin"] != "builtin" || meta["readonly"] != true {
		t.Fatalf("%s ResultMetadata=%#v, want builtin artifact read metadata", toolName, meta)
	}
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) missing", toolName)
	}
	if meta["result_kind"] != toolMeta.Presentation.ResultKind ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("%s renderer metadata=%#v, want presentation %#v", toolName, meta, toolMeta.Presentation)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("%s ResultMetadata missing tool_presentation: %#v", toolName, meta)
	}
	if meta["capability_version"] == "" || meta["chart_spec_version"] == "" || meta["schema_version"] == "" {
		t.Fatalf("%s ResultMetadata missing snapshot version fields: %#v", toolName, meta)
	}
	return meta
}
