package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/rlm"
)

func TestRLMExecToolRejectsUnsupportedStarlarkPatterns(t *testing.T) {
	rt := rlm.NewRuntime(rlm.Options{})
	tool := newRLMExecTool(rt)
	ctx := context.Background()

	tests := []struct {
		name       string
		args       string
		errorMatch string
	}{
		{
			name:       "open blocked",
			args:       `{"code":"open(\"file.txt\")"}`,
			errorMatch: "open() is not supported in rlm_exec",
		},
		{
			name:       "max_results blocked",
			args:       `{"code":"search(query=\"x\", max_results=3)"}`,
			errorMatch: "search() does not accept max_results",
		},
		{
			name:       "f-string blocked",
			args:       `{"code":"x = f\"hello {1}\""}`,
			errorMatch: "f-strings are not valid Starlark syntax",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tool.Execute(ctx, ExecContext{RunID: "run-rlm-tool"}, []byte(tc.args))
			if err != nil {
				t.Fatalf("tool Execute returned unexpected error: %v", err)
			}
			if out.OK {
				t.Fatalf("expected tool failure for %q", tc.name)
			}
			msg := toolErrorMessage(t, out)
			if !strings.Contains(msg, tc.errorMatch) {
				t.Fatalf("error %q does not contain expected text %q", msg, tc.errorMatch)
			}
		})
	}
}

func TestRLMExecToolRejectsMissingRunContext(t *testing.T) {
	tool := newRLMExecTool(rlm.NewRuntime(rlm.Options{}))
	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{"code":"set_prompt(\"ok\")"}`))
	if err != nil {
		t.Fatalf("tool Execute returned unexpected error: %v", err)
	}
	if out.OK {
		t.Fatalf("expected tool failure when run context is missing")
	}
	if msg := toolErrorMessage(t, out); !strings.Contains(msg, "missing run context") {
		t.Fatalf("error %q does not contain expected text", msg)
	}
}

func TestRLMExecToolAcceptsSupportedCode(t *testing.T) {
	tool := newRLMExecTool(rlm.NewRuntime(rlm.Options{}))
	args := map[string]any{
		"code": "set_prompt(\"ok\")\nappend_prompt(\" + world\")",
		"note": "smoke",
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{RunID: "run-rlm-tool-success"}, rawArgs)
	if err != nil {
		t.Fatalf("tool Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected successful tool execution, got: %s", toolErrorMessage(t, out))
	}

	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	if doneRaw, ok := payload["done"]; !ok || doneRaw != false {
		t.Fatalf("unexpected done field: %#v", doneRaw)
	}
	if _, nested := payload["query_metadata"]; nested {
		t.Fatalf("result should not contain nested query_metadata: %#v", payload)
	}
	if out.ResultMetadata["provider"] != "thread-search" || out.ResultMetadata["product"] != "rlm" || out.ResultMetadata["kind"] != "execution" {
		t.Fatalf("ResultMetadata=%#v, want rlm execution metadata", out.ResultMetadata)
	}
	requireRLMCatalogMetadata(t, out.ResultMetadata)
}

func TestSanitizeRLMQueryRetryResultRemovesNestedQOBWithoutDroppingObservationHandles(t *testing.T) {
	final := "Use " + retrievalStaleQOB
	clean := sanitizeRLMQueryRetryResult(map[string]any{
		"stdout_prefix":  `{"query_obligation_id":"` + retrievalStaleQOB + `","replacement_job_id":"business-123","job_id":"qj-monitor"}`,
		"result_preview": `{"query_metadata":{"query_obligation_id":"` + retrievalStaleQOB + `","replaces_job_id":"qj-old","job_id":"qj-monitor"}}`,
		"evidence":       []rlm.Evidence{{ID: "ev-1", Note: "stale " + retrievalStaleQOB}},
		"final_answer":   &final,
		"nested": map[string]any{
			retrievalStaleQOB:                       "authority-in-key",
			"prefix" + retrievalStaleQOB + "suffix": "embedded-authority-in-key",
		},
	})
	encoded, err := json.Marshal(clean)
	if err != nil {
		t.Fatalf("Marshal(clean): %v", err)
	}
	text := string(encoded)
	if strings.Contains(text, retrievalStaleQOB) {
		t.Fatalf("RLM result retained stale qob: %s", text)
	}
	if !strings.Contains(text, "business-123") || !strings.Contains(text, "qj-monitor") {
		t.Fatalf("RLM result lost business/polling handles: %s", text)
	}
	if strings.Contains(text, "qj-old") || strings.Contains(text, "replaces_job_id") {
		t.Fatalf("RLM lifecycle envelope retained legacy retry target: %s", text)
	}
}

func TestRLMExecToolDryRunDoesNotRequireRuntimeOrRunContext(t *testing.T) {
	tool := newRLMExecTool(nil)
	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
		"code":"set_prompt(\"ok\")",
		"dry_run":true
	}`))
	if err != nil {
		t.Fatalf("tool Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected successful dry-run, got: %s", toolErrorMessage(t, out))
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	if payload["dry_run"] != true || payload["validated"] != true {
		t.Fatalf("unexpected dry-run payload: %#v", payload)
	}
	if _, nested := payload["result_metadata"]; nested {
		t.Fatalf("dry-run result should not contain nested result_metadata: %#v", payload)
	}
	if out.ResultMetadata["provider"] != "thread-search" || out.ResultMetadata["kind"] != "dry_run" || out.ResultMetadata["mode"] != "dry_run" {
		t.Fatalf("ResultMetadata=%#v, want rlm dry-run metadata", out.ResultMetadata)
	}
	requireRLMCatalogMetadata(t, out.ResultMetadata)
}

func requireRLMCatalogMetadata(t *testing.T, meta map[string]any) {
	t.Helper()
	toolMeta, ok := ToolMetadataByName("rlm_exec")
	if !ok {
		t.Fatal("missing rlm_exec metadata")
	}
	if meta["tool_name"] != "rlm_exec" ||
		meta["tool_set_id"] != toolMeta.ToolSetID ||
		meta["category"] != string(toolMeta.Category) ||
		meta["origin"] != string(toolMeta.Origin) ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("ResultMetadata=%#v, want rlm catalog metadata %#v", meta, toolMeta.Presentation)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", meta)
	}
}

func TestRLMExecToolDryRunDoesNotMutateWorkspace(t *testing.T) {
	rt := rlm.NewRuntime(rlm.Options{})
	tool := newRLMExecTool(rt)
	out, err := tool.Execute(context.Background(), ExecContext{RunID: "run-rlm-dry-run"}, json.RawMessage(`{
		"code":"set_prompt(\"changed\")",
		"dry_run":true
	}`))
	if err != nil {
		t.Fatalf("dry-run Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected successful dry-run, got: %s", toolErrorMessage(t, out))
	}

	out, err = tool.Execute(context.Background(), ExecContext{RunID: "run-rlm-dry-run"}, json.RawMessage(`{
		"code":"print(prompt_len())"
	}`))
	if err != nil {
		t.Fatalf("execute after dry-run returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected successful execution after dry-run, got: %s", toolErrorMessage(t, out))
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	if stdout, _ := payload["stdout_prefix"].(string); strings.TrimSpace(stdout) != "0" {
		t.Fatalf("stdout_prefix=%q, want prompt length 0 after dry-run", stdout)
	}
}

func TestRLMExecToolDryRunValidatesSyntax(t *testing.T) {
	tool := newRLMExecTool(nil)
	out, err := tool.Execute(context.Background(), ExecContext{}, json.RawMessage(`{
		"code":"x =",
		"dry_run":true
	}`))
	if err != nil {
		t.Fatalf("dry-run Execute returned unexpected error: %v", err)
	}
	if out.OK {
		t.Fatalf("expected syntax validation failure")
	}
	if msg := toolErrorMessage(t, out); !strings.Contains(msg, "starlark syntax error") {
		t.Fatalf("error %q does not contain syntax validation text", msg)
	}
}

func TestRLMExecToolDefinitionDocumentsStarlarkRules(t *testing.T) {
	tool := newRLMExecTool(rlm.NewRuntime(rlm.Options{}))
	def := tool.Definition()
	desc := def.Function.Description
	if !strings.Contains(desc, "open()") || !strings.Contains(desc, "max_results=") {
		t.Fatalf("tool description missing hardening guidance: %q", desc)
	}
	properties := toolDefinitionProperties(t, def.Function.Parameters)
	if _, ok := properties["dry_run"]; !ok {
		t.Fatalf("rlm_exec schema missing dry_run")
	}
}
