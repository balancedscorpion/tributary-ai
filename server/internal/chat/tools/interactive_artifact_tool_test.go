package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
)

func TestCreateInteractiveArtifactToolBuildsSandboxedHTMLArtifact(t *testing.T) {
	tool := newCreateInteractiveArtifactTool()
	var progress []map[string]any
	out, err := tool.Execute(context.Background(), ExecContext{
		OnProgress: func(update map[string]any) {
			progress = append(progress, update)
		},
	}, json.RawMessage(`{
		"title": "Counter",
		"html": "<button id=\"b\">0</button><script>let n=0; b.onclick=()=>b.textContent=++n;</script>"
	}`))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !out.OK {
		t.Fatalf("out.OK=false, result=%#v", out.Result)
	}
	if out.ResultFormat != ResultFormatJSON {
		t.Fatalf("ResultFormat=%q, want %q", out.ResultFormat, ResultFormatJSON)
	}
	if out.Artifact == nil {
		t.Fatalf("Artifact is nil")
	}
	if out.Artifact.Type != "code" {
		t.Fatalf("Artifact.Type=%q, want code", out.Artifact.Type)
	}
	if out.Artifact.Title != "Counter" {
		t.Fatalf("Artifact.Title=%q, want Counter", out.Artifact.Title)
	}
	if out.Artifact.Language == nil || *out.Artifact.Language != "html" {
		t.Fatalf("Artifact.Language=%v, want html", out.Artifact.Language)
	}
	if got := strings.TrimSpace(anyToString(out.Artifact.Metadata["render_mode"])); got != "sandboxed_html" {
		t.Fatalf("render_mode=%q, want sandboxed_html", got)
	}
	if got := strings.TrimSpace(anyToString(out.Artifact.Metadata["network"])); got != "blocked" {
		t.Fatalf("network=%q, want blocked", got)
	}
	if out.Artifact.Metadata["inline_preview"] != true {
		t.Fatalf("inline_preview=%#v, want true", out.Artifact.Metadata["inline_preview"])
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result=%T, want map[string]any", out.Result)
	}
	if _, hasHTML := result["html"]; hasHTML {
		t.Fatalf("Result leaked full html payload: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(result["render_mode"])); got != "sandboxed_html" {
		t.Fatalf("result render_mode=%q, want sandboxed_html", got)
	}
	if result["inline_preview"] != true {
		t.Fatalf("result inline_preview=%#v, want true", result["inline_preview"])
	}
	requireInteractiveArtifactOutputContract(t, out, createInteractiveArtifactToolName, "interactive_html")
	toolArgs, ok := out.Artifact.Metadata["tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.tool_args=%T, want map[string]any", out.Artifact.Metadata["tool_args"])
	}
	if got := strings.TrimSpace(anyToString(toolArgs["title"])); got != "Counter" {
		t.Fatalf("tool_args.title=%q, want Counter", got)
	}
	if got := strings.TrimSpace(anyToString(toolArgs["html"])); got == "" {
		t.Fatalf("tool_args.html missing: %#v", toolArgs)
	}
	if len(progress) == 0 {
		t.Fatalf("expected progress updates")
	}
	if got := strings.TrimSpace(anyToString(progress[0]["phase"])); got != "validating_html" {
		t.Fatalf("first progress phase=%q, want validating_html", got)
	}
}

func TestCreateInteractiveArtifactToolRejectsOversizedHTML(t *testing.T) {
	tool := newCreateInteractiveArtifactTool()
	args, err := json.Marshal(map[string]string{
		"title": "Too large",
		"html":  strings.Repeat("x", maxInteractiveHTMLBytes+1),
	})
	if err != nil {
		t.Fatalf("Marshal args: %v", err)
	}
	out, err := tool.Execute(context.Background(), ExecContext{}, args)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if out.OK {
		t.Fatalf("out.OK=true, want validation failure")
	}
}

func TestCreateInteractiveDataArtifactToolBindsDatasetWithoutReturningRows(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	rows := []map[string]any{
		{"region": "North", "revenue": 10},
		{"region": "South", "revenue": 20},
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("Marshal rows: %v", err)
	}
	content := string(b)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "source-data",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Revenue",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	var progress []map[string]any
	tool := newCreateInteractiveDataArtifactTool()
	out, err := tool.Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		CfgDir:   cfgDir,
		OnProgress: func(update map[string]any) {
			progress = append(progress, update)
		},
	}, mustJSONRaw(t, map[string]any{
		"title": "Interactive Revenue",
		"source": map[string]any{
			"artifact_id": "source-data",
		},
		"html": `<button onclick="sandboxData.getRows({limit: 2}).then(console.log)">Load</button>`,
	}))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !out.OK {
		t.Fatalf("out.OK=false, result=%#v", out.Result)
	}
	if out.Artifact == nil {
		t.Fatalf("Artifact is nil")
	}
	if got := strings.TrimSpace(anyToString(out.Artifact.Metadata["artifact_kind"])); got != "interactive_data_html" {
		t.Fatalf("artifact_kind=%q, want interactive_data_html", got)
	}
	if got := strings.TrimSpace(anyToString(out.Artifact.Metadata["data_source_artifact_id"])); got != "source-data" {
		t.Fatalf("data_source_artifact_id=%q, want source-data", got)
	}
	if out.Artifact.Metadata["inline_preview"] != true {
		t.Fatalf("inline_preview=%#v, want true", out.Artifact.Metadata["inline_preview"])
	}
	dataAccess, ok := out.Artifact.Metadata["data_access"].(map[string]any)
	if !ok {
		t.Fatalf("data_access metadata missing: %#v", out.Artifact.Metadata["data_access"])
	}
	if got := dataAccess["max_page_size"].(int); got != defaultInteractiveDataPageSize {
		t.Fatalf("data_access.max_page_size=%d, want %d", got, defaultInteractiveDataPageSize)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result=%T, want map[string]any", out.Result)
	}
	if _, hasRows := result["rows"]; hasRows {
		t.Fatalf("Result leaked dataset rows: %#v", result)
	}
	if _, hasHTML := result["html"]; hasHTML {
		t.Fatalf("Result leaked full html payload: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(result["data_source_artifact_id"])); got != "source-data" {
		t.Fatalf("result data_source_artifact_id=%q, want source-data", got)
	}
	if result["inline_preview"] != true {
		t.Fatalf("result inline_preview=%#v, want true", result["inline_preview"])
	}
	requireInteractiveArtifactOutputContract(t, out, createInteractiveDataArtifactToolName, "interactive_data_html")
	toolArgs, ok := out.Artifact.Metadata["tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.tool_args=%T, want map[string]any", out.Artifact.Metadata["tool_args"])
	}
	sourceArgs, ok := toolArgs["source"].(map[string]any)
	if !ok {
		t.Fatalf("tool_args.source=%T, want map[string]any", toolArgs["source"])
	}
	if got := strings.TrimSpace(anyToString(sourceArgs["artifact_id"])); got != "source-data" {
		t.Fatalf("tool_args.source.artifact_id=%q, want source-data", got)
	}
	resultMetadata := out.Artifact.Metadata["result_metadata"].(map[string]any)
	if got := strings.TrimSpace(anyToString(resultMetadata["data_source_artifact_id"])); got != "source-data" {
		t.Fatalf("result_metadata.data_source_artifact_id=%q, want source-data", got)
	}
	if got := int(resultMetadata["data_source_row_count"].(int)); got != len(rows) {
		t.Fatalf("result_metadata.data_source_row_count=%d, want %d", got, len(rows))
	}
	if len(progress) < 4 {
		t.Fatalf("progress updates=%d, want at least 4", len(progress))
	}
	if got := strings.TrimSpace(anyToString(progress[0]["phase"])); got != "loading_source_metadata" {
		t.Fatalf("first progress phase=%q, want loading_source_metadata", got)
	}
}

func requireInteractiveArtifactOutputContract(t *testing.T, out Output, toolName string, artifactKind string) {
	t.Helper()
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("Result=%T, want map[string]any", out.Result)
	}
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("Result should not contain nested result_metadata: %#v", result)
	}
	if out.ResultMetadata == nil {
		t.Fatalf("Output.ResultMetadata is nil")
	}
	requireInteractiveResultMetadata(t, out.ResultMetadata, toolName, artifactKind)
	if out.Artifact == nil {
		t.Fatalf("Artifact is nil")
	}
	metadata := out.Artifact.Metadata
	if metadata == nil {
		t.Fatalf("Artifact.Metadata is nil")
	}
	requireRenderableInteractiveArtifactMetadata(t, metadata, toolName, artifactKind)

	encoded, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("Marshal artifact metadata: %v", err)
	}
	var roundTripped map[string]any
	if err := json.Unmarshal(encoded, &roundTripped); err != nil {
		t.Fatalf("Unmarshal artifact metadata: %v", err)
	}
	requireRenderableInteractiveArtifactMetadata(t, roundTripped, toolName, artifactKind)
}

func requireInteractiveResultMetadata(t *testing.T, metadata map[string]any, toolName string, artifactKind string) {
	t.Helper()
	if got := strings.TrimSpace(anyToString(metadata["kind"])); got != artifactKind {
		t.Fatalf("result metadata kind=%q, want %q; metadata=%#v", got, artifactKind, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["render_mode"])); got != "sandboxed_html" {
		t.Fatalf("result metadata render_mode=%q, want sandboxed_html; metadata=%#v", got, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["artifact_kind"])); got != artifactKind {
		t.Fatalf("result metadata artifact_kind=%q, want %q; metadata=%#v", got, artifactKind, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["native_viewer"])); got != "interactive_html" {
		t.Fatalf("result metadata native_viewer=%q, want interactive_html; metadata=%#v", got, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["renderer_strategy"])); got != "artifact_type" {
		t.Fatalf("result metadata renderer_strategy=%q, want artifact_type; metadata=%#v", got, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["renderer_key"])); got != "interactive_html" {
		t.Fatalf("result metadata renderer_key=%q, want interactive_html; metadata=%#v", got, metadata)
	}
	presentation, ok := metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("result metadata.tool_presentation=%T, want map[string]any; metadata=%#v", metadata["tool_presentation"], metadata)
	}
	requireInteractiveToolPresentation(t, presentation, toolName)
}

func requireRenderableInteractiveArtifactMetadata(t *testing.T, metadata map[string]any, toolName string, artifactKind string) {
	t.Helper()
	if got := strings.TrimSpace(anyToString(metadata["render_mode"])); got != "sandboxed_html" {
		t.Fatalf("artifact metadata render_mode=%q, want sandboxed_html; metadata=%#v", got, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["sandbox"])); got != "allow-scripts" {
		t.Fatalf("artifact metadata sandbox=%q, want allow-scripts; metadata=%#v", got, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["network"])); got != "blocked" {
		t.Fatalf("artifact metadata network=%q, want blocked; metadata=%#v", got, metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["artifact_kind"])); got != artifactKind {
		t.Fatalf("artifact metadata artifact_kind=%q, want %q; metadata=%#v", got, artifactKind, metadata)
	}
	if metadata["inline_preview"] != true {
		t.Fatalf("artifact metadata inline_preview=%#v, want true; metadata=%#v", metadata["inline_preview"], metadata)
	}
	if got := strings.TrimSpace(anyToString(metadata["tool_name"])); got != toolName {
		t.Fatalf("artifact metadata tool_name=%q, want %q; metadata=%#v", got, toolName, metadata)
	}
	if _, ok := metadata["tool_args"].(map[string]any); !ok {
		t.Fatalf("artifact metadata.tool_args=%T, want map[string]any; metadata=%#v", metadata["tool_args"], metadata)
	}
	presentation, ok := metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.tool_presentation=%T, want map[string]any; metadata=%#v", metadata["tool_presentation"], metadata)
	}
	requireInteractiveToolPresentation(t, presentation, toolName)
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.result_metadata=%T, want map[string]any; metadata=%#v", metadata["result_metadata"], metadata)
	}
	requireInteractiveResultMetadata(t, resultMetadata, toolName, artifactKind)
}

func requireInteractiveToolPresentation(t *testing.T, presentation map[string]any, toolName string) {
	t.Helper()
	if got := strings.TrimSpace(anyToString(presentation["family"])); got != "artifacts" {
		t.Fatalf("tool_presentation.family=%q, want artifacts; presentation=%#v", got, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["result_kind"])); got != "interactive" {
		t.Fatalf("tool_presentation.result_kind=%q, want interactive; presentation=%#v", got, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["renderer_strategy"])); got != "artifact_type" {
		t.Fatalf("tool_presentation.renderer_strategy=%q, want artifact_type; presentation=%#v", got, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["renderer_key"])); got != "interactive_html" {
		t.Fatalf("tool_presentation.renderer_key=%q, want interactive_html; presentation=%#v", got, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["native_viewer"])); got != "interactive_html" {
		t.Fatalf("tool_presentation.native_viewer=%q, want interactive_html; presentation=%#v", got, presentation)
	}
	if got := strings.TrimSpace(anyToString(presentation["file_capability"])); got != "artifact_create" {
		t.Fatalf("tool_presentation.file_capability=%q, want artifact_create; presentation=%#v", got, presentation)
	}
	artifactTypes, ok := presentation["artifact_types"].([]any)
	if ok {
		for _, artifactType := range artifactTypes {
			if strings.TrimSpace(anyToString(artifactType)) == "code" {
				return
			}
		}
	}
	if artifactTypes, ok := presentation["artifact_types"].([]string); ok {
		for _, artifactType := range artifactTypes {
			if strings.TrimSpace(artifactType) == "code" {
				return
			}
		}
	}
	t.Fatalf("tool_presentation.artifact_types=%#v, want code", presentation["artifact_types"])
}
