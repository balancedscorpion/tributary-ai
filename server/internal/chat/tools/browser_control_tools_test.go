package tools

import (
	"context"
	"os"
	"strings"
	"testing"

	"ai-analyst-local/internal/browsercontrol"
)

func TestBrowserControlLogToolsAreExposedWhenEnabled(t *testing.T) {
	registry := NewRegistry(&RegistryOptions{
		Browser: browsercontrol.NewManager(t.TempDir(), nil),
	})

	disabled := registry.BrowserControlToolDefinitions(false)
	if len(disabled) != 0 {
		t.Fatalf("disabled browser-control definitions = %d, want 0", len(disabled))
	}

	defs := registry.BrowserControlToolDefinitions(true)
	names := map[string]bool{}
	for _, def := range defs {
		names[def.Function.Name] = true
	}
	for _, want := range []string{BrowserControlToolLogs, BrowserControlToolClearLogs} {
		if !names[want] {
			t.Fatalf("browser-control definitions missing %s", want)
		}
		if !IsBrowserControlToolName(want) {
			t.Fatalf("IsBrowserControlToolName(%q) = false, want true", want)
		}
	}
}

func TestBrowserControlLogsToolSchemaIncludesFilteringFields(t *testing.T) {
	def := newBrowserControlLogsTool(nil).Definition()
	if def.Function.Name != BrowserControlToolLogs {
		t.Fatalf("tool name = %q, want %q", def.Function.Name, BrowserControlToolLogs)
	}

	properties, ok := def.Function.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties has type %T, want map[string]any", def.Function.Parameters["properties"])
	}
	for _, key := range []string{
		"types",
		"levels",
		"methods",
		"resourceTypes",
		"statusCodes",
		"statusClass",
		"statusMin",
		"statusMax",
		"urlContains",
		"urlRegex",
		"textContains",
		"textRegex",
		"failedOnly",
		"sinceSequence",
		"sinceMs",
		"limit",
		"order",
	} {
		if _, ok := properties[key]; !ok {
			t.Fatalf("logs schema missing %s", key)
		}
	}
	if _, ok := properties["includeHeaders"]; ok {
		t.Fatal("logs schema should not expose request or response headers")
	}
	if _, ok := properties["includeBody"]; ok {
		t.Fatal("logs schema should not expose request or response bodies")
	}
}

func TestBrowserControlToolSchemasAreClosed(t *testing.T) {
	for _, tool := range []Tool{
		newBrowserControlStatusTool(nil),
		newBrowserControlNavigateTool(nil),
		newBrowserControlSnapshotTool(nil),
		newBrowserControlClickTool(nil),
		newBrowserControlTypeTool(nil),
		newBrowserControlPressTool(nil),
		newBrowserControlWaitTool(nil),
		newBrowserControlBackTool(nil),
		newBrowserControlLogsTool(nil),
		newBrowserControlClearLogsTool(nil),
	} {
		def := tool.Definition()
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
	}
}

func TestBrowserControlExecuteToolsExposeDryRunOnlyWhereSafe(t *testing.T) {
	executeTools := []Tool{
		newBrowserControlNavigateTool(nil),
		newBrowserControlClickTool(nil),
		newBrowserControlTypeTool(nil),
		newBrowserControlPressTool(nil),
		newBrowserControlWaitTool(nil),
		newBrowserControlBackTool(nil),
		newBrowserControlClearLogsTool(nil),
	}
	for _, tool := range executeTools {
		def := tool.Definition()
		properties, ok := def.Function.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s properties has type %T, want map[string]any", def.Function.Name, def.Function.Parameters["properties"])
		}
		if _, ok := properties["dry_run"]; !ok {
			t.Fatalf("%s schema missing dry_run", def.Function.Name)
		}
	}

	readTools := []Tool{
		newBrowserControlStatusTool(nil),
		newBrowserControlSnapshotTool(nil),
		newBrowserControlLogsTool(nil),
	}
	for _, tool := range readTools {
		def := tool.Definition()
		properties, _ := def.Function.Parameters["properties"].(map[string]any)
		if _, ok := properties["dry_run"]; ok {
			t.Fatalf("%s read schema should not expose dry_run", def.Function.Name)
		}
	}
}

func TestBrowserControlDryRunsUseFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("browser_control_tools.go")
	if err != nil {
		t.Fatalf("read browser_control_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("browser control tools should set Output.ResultMetadata directly instead of nesting result_metadata in result maps")
	}
}

func TestBrowserControlDryRunsDoNotRequireManager(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		tool     Tool
		args     map[string]any
	}{
		{
			name:     "navigate",
			toolName: BrowserControlToolNavigate,
			tool:     newBrowserControlNavigateTool(nil),
			args:     map[string]any{"url": "http://127.0.0.1:5173/"},
		},
		{
			name:     "click",
			toolName: BrowserControlToolClick,
			tool:     newBrowserControlClickTool(nil),
			args:     map[string]any{"index": 1},
		},
		{
			name:     "type",
			toolName: BrowserControlToolType,
			tool:     newBrowserControlTypeTool(nil),
			args:     map[string]any{"selector": "#email", "value": "person@example.com"},
		},
		{
			name:     "press",
			toolName: BrowserControlToolPress,
			tool:     newBrowserControlPressTool(nil),
			args:     map[string]any{"key": "Enter"},
		},
		{
			name:     "wait",
			toolName: BrowserControlToolWait,
			tool:     newBrowserControlWaitTool(nil),
			args:     map[string]any{"milliseconds": 250},
		},
		{
			name:     "back",
			toolName: BrowserControlToolBack,
			tool:     newBrowserControlBackTool(nil),
			args:     map[string]any{},
		},
		{
			name:     "clear_logs",
			toolName: BrowserControlToolClearLogs,
			tool:     newBrowserControlClearLogsTool(nil),
			args:     map[string]any{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"dry_run": true}
			for key, value := range tc.args {
				args[key] = value
			}
			var progress []map[string]any
			out, err := tc.tool.Execute(context.Background(), ExecContext{
				OnProgress: func(event map[string]any) {
					progress = append(progress, event)
				},
			}, mustJSON(t, args))
			if err != nil {
				t.Fatalf("execute dry-run: %v", err)
			}
			if !out.OK {
				t.Fatalf("expected OK dry-run output")
			}
			result := asMap(t, out.Result)
			if result["dry_run"] != true {
				t.Fatalf("dry_run=%#v want=true", result["dry_run"])
			}
			if result["would_execute"] != tc.toolName {
				t.Fatalf("would_execute=%#v want=%s", result["would_execute"], tc.toolName)
			}
			if _, ok := result["result_metadata"]; ok {
				t.Fatalf("dry-run result contains nested result_metadata: %#v", result)
			}
			if out.ResultMetadata["provider"] != "browser_control" || out.ResultMetadata["product"] != "browser" || out.ResultMetadata["mode"] != "dry_run" || out.ResultMetadata["tool_name"] != tc.toolName {
				t.Fatalf("ResultMetadata=%#v, want browser dry-run metadata for %s", out.ResultMetadata, tc.toolName)
			}
			requireBrowserControlResultMetadata(t, out, tc.toolName, "browser_dry_run", "dry_run")
			if len(progress) != 1 || progress[0]["phase"] != "dry_run" {
				t.Fatalf("progress=%#v want one dry_run event", progress)
			}
		})
	}
}

func TestBrowserControlLiveResultMetadataUsesCatalogPresentation(t *testing.T) {
	cases := []struct {
		toolName string
		kind     string
		result   map[string]any
	}{
		{BrowserControlToolStatus, "browser_status", map[string]any{"url": "http://127.0.0.1:5173/", "title": "App"}},
		{BrowserControlToolSnapshot, "browser_snapshot", map[string]any{"url": "http://127.0.0.1:5173/", "title": "App", "elements": []any{}}},
		{BrowserControlToolLogs, "browser_logs", map[string]any{"entries": []any{}, "high_watermark_sequence": 1}},
		{BrowserControlToolNavigate, "browser_action", map[string]any{"url": "http://127.0.0.1:5173/", "ok": true}},
		{BrowserControlToolClearLogs, "browser_logs_clear", map[string]any{"cleared": true}},
	}
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			out := JSONOKWithMetadata(tc.result, browserControlResultMetadata(tc.toolName, tc.kind, "live", tc.result))
			meta := requireBrowserControlResultMetadata(t, out, tc.toolName, tc.kind, "live")
			if url, ok := tc.result["url"].(string); ok && meta["browser_url"] != url {
				t.Fatalf("browser_url=%#v, want %q; metadata=%#v", meta["browser_url"], url, meta)
			}
		})
	}
}

func requireBrowserControlResultMetadata(t *testing.T, out Output, toolName, kind, mode string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != "browser_control" || meta["product"] != "browser" || meta["tool_name"] != toolName || meta["kind"] != kind || meta["mode"] != mode {
		t.Fatalf("%s ResultMetadata=%#v, want browser metadata kind=%s mode=%s", toolName, meta, kind, mode)
	}
	if meta["tool_set_id"] != "browser" || meta["tool_set_name"] != "In-app browser" || meta["connector_id"] != "browser" || meta["origin"] != "connected" {
		t.Fatalf("%s provenance metadata=%#v, want connected browser tool set", toolName, meta)
	}
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) missing", toolName)
	}
	if meta["category"] != string(toolMeta.Category) || meta["readonly"] != (toolMeta.Category == ToolCategoryRead) {
		t.Fatalf("%s category metadata=%#v, want category %s", toolName, meta, toolMeta.Category)
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
	return meta
}

func TestBrowserControlDryRunValidatesRequiredArguments(t *testing.T) {
	tool := newBrowserControlNavigateTool(nil)
	out, err := tool.Execute(context.Background(), ExecContext{}, mustJSON(t, map[string]any{
		"dry_run": true,
	}))
	if err != nil {
		t.Fatalf("execute dry-run: %v", err)
	}
	if out.OK {
		t.Fatalf("expected validation error for missing URL")
	}
	result := asMap(t, out.Result)
	if result["error"] == "" {
		t.Fatalf("result missing error: %#v", result)
	}
}

func TestBrowserControlDryRunValidatesTargetArguments(t *testing.T) {
	cases := []struct {
		name string
		tool Tool
		args map[string]any
	}{
		{
			name: "click",
			tool: newBrowserControlClickTool(nil),
			args: map[string]any{"dry_run": true},
		},
		{
			name: "type",
			tool: newBrowserControlTypeTool(nil),
			args: map[string]any{"value": "hello", "dry_run": true},
		},
		{
			name: "press",
			tool: newBrowserControlPressTool(nil),
			args: map[string]any{"dry_run": true},
		},
		{
			name: "wait too short",
			tool: newBrowserControlWaitTool(nil),
			args: map[string]any{"milliseconds": 50, "dry_run": true},
		},
		{
			name: "wait too long",
			tool: newBrowserControlWaitTool(nil),
			args: map[string]any{"milliseconds": 30001, "dry_run": true},
		},
		{
			name: "wait non numeric",
			tool: newBrowserControlWaitTool(nil),
			args: map[string]any{"milliseconds": "soon", "dry_run": true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tc.tool.Execute(context.Background(), ExecContext{}, mustJSON(t, tc.args))
			if err != nil {
				t.Fatalf("execute dry-run: %v", err)
			}
			if out.OK {
				t.Fatalf("expected validation error for missing target")
			}
			result := asMap(t, out.Result)
			if result["error"] == "" {
				t.Fatalf("result missing error: %#v", result)
			}
		})
	}
}
