package tools

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/sharepoint"
)

func TestSharePointToolNamesIncludeFileCapabilitySet(t *testing.T) {
	names := SharePointToolNames()
	for _, want := range []string{
		SharePointToolStatus,
		SharePointToolCheckConnection,
		SharePointToolSearch,
		SharePointToolGetMetadata,
		SharePointToolRead,
		SharePointToolSummarize,
		SharePointToolDownload,
	} {
		if !sharePointStringSliceContains(names, want) {
			t.Fatalf("SharePointToolNames missing %q: %v", want, names)
		}
		if !IsSharePointToolName(want) {
			t.Fatalf("IsSharePointToolName(%q)=false, want true", want)
		}
	}
}

func TestSharePointToolDefinitionsAreGated(t *testing.T) {
	r := NewRegistry(&RegistryOptions{
		SharePoint: sharepoint.NewManager(t.TempDir(), nil),
	})
	if defs := r.SharePointToolDefinitions(false); len(defs) != 0 {
		t.Fatalf("SharePointToolDefinitions(false) len=%d, want 0", len(defs))
	}
	defs := r.SharePointToolDefinitions(true)
	if len(defs) != len(SharePointToolNames()) {
		t.Fatalf("SharePointToolDefinitions(true) len=%d, want %d", len(defs), len(SharePointToolNames()))
	}
	got := map[string]bool{}
	for _, def := range defs {
		got[def.Function.Name] = true
	}
	for _, want := range SharePointToolNames() {
		if !got[want] {
			t.Fatalf("SharePointToolDefinitions(true) missing %q: %v", want, got)
		}
	}
}

func TestSharePointToolSchemasAreClosed(t *testing.T) {
	manager := sharepoint.NewManager(t.TempDir(), nil)
	for _, tool := range []Tool{
		newSharePointStatusTool(manager),
		newSharePointCheckConnectionTool(manager),
		newSharePointSearchTool(manager),
		newSharePointGetMetadataTool(manager),
		newSharePointReadTool(manager),
		newSharePointSummarizeTool(manager),
		newSharePointDownloadTool(manager),
	} {
		def := tool.Definition()
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
	}
}

func TestSharePointReadToolSchemaRoutesFilesToNativeParser(t *testing.T) {
	def := newSharePointReadTool(sharepoint.NewManager(t.TempDir(), nil)).Definition()
	if def.Function.Name != SharePointToolRead {
		t.Fatalf("tool name=%q, want %q", def.Function.Name, SharePointToolRead)
	}
	desc := strings.ToLower(def.Function.Description)
	if !strings.Contains(desc, "first-class file parser") || !strings.Contains(desc, "native model file input") {
		t.Fatalf("description should describe native parser promotion: %s", def.Function.Description)
	}
	props, _ := def.Function.Parameters["properties"].(map[string]any)
	for _, key := range []string{"item_url", "server_relative_url", "site_url", "max_chars", "max_download_bytes"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("sharepoint_read schema missing %q: %#v", key, props)
		}
	}
}

func TestSharePointOutputPromotesBridgeMetadataWithPresentationDefaults(t *testing.T) {
	tests := []struct {
		tool             string
		wantKind         string
		wantNativeViewer string
		wantRendererMode string
		wantRendererKey  string
	}{
		{
			tool:             SharePointToolStatus,
			wantKind:         "connector",
			wantNativeViewer: "connector_inspector",
			wantRendererMode: "family",
			wantRendererKey:  "sharepoint",
		},
		{
			tool:             SharePointToolCheckConnection,
			wantKind:         "connector",
			wantNativeViewer: "connector_inspector",
			wantRendererMode: "family",
			wantRendererKey:  "sharepoint",
		},
		{
			tool:             SharePointToolSearch,
			wantKind:         "connector",
			wantNativeViewer: "connector_inspector",
			wantRendererMode: "family",
			wantRendererKey:  "sharepoint",
		},
		{
			tool:             SharePointToolGetMetadata,
			wantKind:         "connector",
			wantNativeViewer: "connector_inspector",
			wantRendererMode: "family",
			wantRendererKey:  "sharepoint",
		},
		{
			tool:             SharePointToolRead,
			wantKind:         "file",
			wantNativeViewer: "file",
			wantRendererMode: "artifact_type",
			wantRendererKey:  "file",
		},
		{
			tool:             SharePointToolSummarize,
			wantKind:         "file",
			wantNativeViewer: "file",
			wantRendererMode: "artifact_type",
			wantRendererKey:  "file",
		},
		{
			tool:             SharePointToolDownload,
			wantKind:         "file",
			wantNativeViewer: "file",
			wantRendererMode: "artifact_type",
			wantRendererKey:  "file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			bridgeResult := map[string]any{
				"ok":   true,
				"name": "Planning.docx",
				"result_metadata": map[string]any{
					"provider":          "legacy_provider",
					"product":           "legacy_product",
					"family":            "legacy_family",
					"kind":              "legacy_kind",
					"native_viewer":     "legacy_viewer",
					"renderer_strategy": "legacy_strategy",
					"renderer_key":      "legacy_key",
					"widget":            "bridge_widget",
					"bridge_only":       "preserved",
					"tool_presentation": map[string]any{
						"bridge_presentation": "preserved",
						"family":              "legacy_family",
						"native_viewer":       "legacy_viewer",
					},
				},
			}

			out := sharePointOutput(tt.tool, bridgeResult)
			if !out.OK {
				t.Fatalf("sharePointOutput OK=false")
			}
			result, ok := out.Result.(map[string]any)
			if !ok {
				t.Fatalf("result=%T, want map[string]any", out.Result)
			}
			if _, ok := result["result_metadata"]; ok {
				t.Fatalf("result still has nested result_metadata: %#v", result)
			}
			if _, ok := bridgeResult["result_metadata"]; !ok {
				t.Fatalf("sharePointOutput mutated the bridge result instead of cloning it")
			}

			meta := out.ResultMetadata
			requireSharePointMetadataString(t, meta, "provider", "sharepoint")
			requireSharePointMetadataString(t, meta, "integration", "sharepoint")
			requireSharePointMetadataString(t, meta, "product", "sharepoint")
			requireSharePointMetadataString(t, meta, "family", "sharepoint")
			requireSharePointMetadataString(t, meta, "kind", tt.wantKind)
			requireSharePointMetadataString(t, meta, "result_kind", tt.wantKind)
			requireSharePointMetadataString(t, meta, "tool_name", tt.tool)
			requireSharePointMetadataString(t, meta, "tool_set_id", "sharepoint")
			requireSharePointMetadataString(t, meta, "connector_id", "sharepoint")
			requireSharePointMetadataString(t, meta, "native_viewer", tt.wantNativeViewer)
			requireSharePointMetadataString(t, meta, "renderer_strategy", tt.wantRendererMode)
			requireSharePointMetadataString(t, meta, "renderer_key", tt.wantRendererKey)
			requireSharePointMetadataString(t, meta, "widget", "bridge_widget")
			requireSharePointMetadataString(t, meta, "bridge_only", "preserved")

			presentation, ok := meta["tool_presentation"].(map[string]any)
			if !ok {
				t.Fatalf("tool_presentation=%T, want map[string]any; metadata=%#v", meta["tool_presentation"], meta)
			}
			requireSharePointMetadataString(t, presentation, "bridge_presentation", "preserved")
			requireSharePointMetadataString(t, presentation, "family", "sharepoint")
			requireSharePointMetadataString(t, presentation, "result_kind", tt.wantKind)
			requireSharePointMetadataString(t, presentation, "native_viewer", tt.wantNativeViewer)
			requireSharePointMetadataString(t, presentation, "renderer_strategy", tt.wantRendererMode)
			requireSharePointMetadataString(t, presentation, "renderer_key", tt.wantRendererKey)
		})
	}
}

func sharePointStringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func requireSharePointMetadataString(t *testing.T, metadata map[string]any, key string, want string) {
	t.Helper()
	got, _ := metadata[key].(string)
	if strings.TrimSpace(got) != want {
		t.Fatalf("metadata[%q]=%#v, want %q; metadata=%#v", key, metadata[key], want, metadata)
	}
}
