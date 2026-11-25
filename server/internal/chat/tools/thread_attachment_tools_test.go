package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestInspectAttachmentToolEnforcesThreadScope(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	otherThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread(other): %v", err)
	}

	artifact := insertThreadAttachmentArtifact(t, ctx, st, cfgDir, thread.ID, "notes.txt", "alpha\nbeta\ngamma\n")

	tool := newInspectAttachmentTool()
	args := json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s"}`, artifact.ID))

	out, err := tool.Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		CfgDir:   cfgDir,
	}, args)
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	if got := strings.TrimSpace(payload["artifact_id"].(string)); got != artifact.ID {
		t.Fatalf("artifact_id = %q, want %q", got, artifact.ID)
	}
	if got := strings.TrimSpace(payload["preview"].(string)); !strings.Contains(got, "alpha") {
		t.Fatalf("preview = %q, want alpha", got)
	}

	out, err = tool.Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: otherThread.ID,
		CfgDir:   cfgDir,
	}, args)
	if err != nil {
		t.Fatalf("tool.Execute(other thread): %v", err)
	}
	if got := toolErrorMessage(t, out); !strings.Contains(got, "not available in this thread") {
		t.Fatalf("error = %q, want thread isolation message", got)
	}
}

func TestReadAttachmentToolPagesTextContent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	artifact := insertThreadAttachmentArtifact(t, ctx, st, cfgDir, thread.ID, "page.txt", "abcdefghij")
	tool := newReadAttachmentTool()

	out, err := tool.Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		CfgDir:   cfgDir,
	}, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","max_bytes":4}`, artifact.ID)))
	if err != nil {
		t.Fatalf("tool.Execute(first page): %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	if got := payload["content"].(string); got != "abcd" {
		t.Fatalf("first page content = %q, want abcd", got)
	}
	if hasMore, _ := payload["has_more"].(bool); !hasMore {
		t.Fatalf("has_more = false, want true")
	}
	nextToken, _ := payload["next_page_token"].(string)
	if strings.TrimSpace(nextToken) == "" {
		t.Fatalf("missing next_page_token")
	}

	out, err = tool.Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		CfgDir:   cfgDir,
	}, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","page_token":"%s","max_bytes":4}`, artifact.ID, nextToken)))
	if err != nil {
		t.Fatalf("tool.Execute(second page): %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}
	payload, ok = out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected second page payload: %#v", out.Result)
	}
	if got := payload["content"].(string); got != "efgh" {
		t.Fatalf("second page content = %q, want efgh", got)
	}
}

func TestAttachmentToolsAcceptPromotedExternalToolFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	otherThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread(other): %v", err)
	}

	artifact := insertExternalToolFileArtifact(t, ctx, st, cfgDir, thread.ID, "slack_download_file", "report.csv", "region,revenue\nNorth,10\n")
	exec := ExecContext{Store: st, ThreadID: thread.ID, CfgDir: cfgDir}

	inspectOut, err := newInspectAttachmentTool().Execute(ctx, exec, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s"}`, artifact.ID)))
	if err != nil {
		t.Fatalf("inspect Execute: %v", err)
	}
	if !inspectOut.OK {
		t.Fatalf("inspect output not OK: %#v", inspectOut.Result)
	}
	inspectPayload := inspectOut.Result.(map[string]any)
	if got, _ := inspectPayload["preview"].(string); !strings.Contains(got, "North") {
		t.Fatalf("inspect preview=%q, want promoted file content", got)
	}

	readOut, err := newReadAttachmentTool().Execute(ctx, exec, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","max_bytes":64}`, artifact.ID)))
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !readOut.OK {
		t.Fatalf("read output not OK: %#v", readOut.Result)
	}
	readPayload := readOut.Result.(map[string]any)
	if got, _ := readPayload["content"].(string); !strings.Contains(got, "region,revenue") {
		t.Fatalf("read content=%q, want promoted file content", got)
	}

	searchOut, err := newSearchAttachmentTool().Execute(ctx, exec, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","query":"North"}`, artifact.ID)))
	if err != nil {
		t.Fatalf("search Execute: %v", err)
	}
	if !searchOut.OK {
		t.Fatalf("search output not OK: %#v", searchOut.Result)
	}
	searchPayload := searchOut.Result.(map[string]any)
	if got, _ := searchPayload["matches_count"].(int); got == 0 {
		t.Fatalf("matches_count=%v, want match", searchPayload["matches_count"])
	}

	otherOut, err := newInspectAttachmentTool().Execute(ctx, ExecContext{Store: st, ThreadID: otherThread.ID, CfgDir: cfgDir}, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s"}`, artifact.ID)))
	if err != nil {
		t.Fatalf("inspect other Execute: %v", err)
	}
	if got := toolErrorMessage(t, otherOut); !strings.Contains(got, "not available in this thread") {
		t.Fatalf("other thread error=%q, want scope error", got)
	}
}

func TestAttachmentToolsUseSpreadsheetExtractionForBinaryFile(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	artifact := insertSpreadsheetExtractionArtifact(t, ctx, st, cfgDir, thread.ID)
	exec := ExecContext{Store: st, ThreadID: thread.ID, CfgDir: cfgDir}

	inspectOut, err := newInspectAttachmentTool().Execute(ctx, exec, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s"}`, artifact.ID)))
	if err != nil {
		t.Fatalf("inspect Execute: %v", err)
	}
	if !inspectOut.OK {
		t.Fatalf("inspect output not OK: %#v", inspectOut.Result)
	}
	inspectPayload := inspectOut.Result.(map[string]any)
	extraction, ok := inspectPayload["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("inspect missing extraction summary: %#v", inspectPayload)
	}
	if got, _ := extraction["pipeline"].(string); got != "xlsx" {
		t.Fatalf("extraction pipeline=%q, want xlsx", got)
	}
	if got, _ := inspectPayload["extraction_preview"].(string); !strings.Contains(got, "Revenue") {
		t.Fatalf("extraction preview=%q, want Revenue", got)
	}

	readOut, err := newReadAttachmentTool().Execute(ctx, exec, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","max_bytes":12}`, artifact.ID)))
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !readOut.OK {
		t.Fatalf("read output not OK: %#v", readOut.Result)
	}
	readPayload := readOut.Result.(map[string]any)
	if got, _ := readPayload["source"].(string); got != "extraction" {
		t.Fatalf("read source=%q, want extraction", got)
	}
	if got, _ := readPayload["content"].(string); !strings.Contains(got, "# Revenue") {
		t.Fatalf("read content=%q, want extraction markdown", got)
	}

	searchOut, err := newSearchAttachmentTool().Execute(ctx, exec, json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","query":"North"}`, artifact.ID)))
	if err != nil {
		t.Fatalf("search Execute: %v", err)
	}
	if !searchOut.OK {
		t.Fatalf("search output not OK: %#v", searchOut.Result)
	}
	searchPayload := searchOut.Result.(map[string]any)
	if got, _ := searchPayload["source"].(string); got != "extraction" {
		t.Fatalf("search source=%q, want extraction", got)
	}
	if got, _ := searchPayload["matches_count"].(int); got == 0 {
		t.Fatalf("matches_count=%v, want at least one match; payload=%#v", searchPayload["matches_count"], searchPayload)
	}
}

func TestThreadAttachmentToolsUseFirstClassResultMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	artifact := insertThreadAttachmentArtifact(t, ctx, st, cfgDir, thread.ID, "metadata.txt", "alpha\nbeta\ngamma\n")
	exec := ExecContext{Store: st, ThreadID: thread.ID, CfgDir: cfgDir}

	tests := []struct {
		name     string
		tool     Tool
		args     json.RawMessage
		kind     string
		validate func(t *testing.T, meta map[string]any)
	}{
		{
			name: "inspect_attachment",
			tool: newInspectAttachmentTool(),
			args: json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","max_preview_bytes":5}`, artifact.ID)),
			kind: "attachment_metadata",
			validate: func(t *testing.T, meta map[string]any) {
				t.Helper()
				requireThreadAttachmentMetadataValue(t, meta, "artifact_id", artifact.ID)
				requireThreadAttachmentMetadataValue(t, meta, "file_name", "metadata.txt")
				requireThreadAttachmentMetadataValue(t, meta, "mime_type", "text/plain")
				requireThreadAttachmentMetadataValue(t, meta, "source", types.ChatUploadAttachmentSource)
				requireThreadAttachmentMetadataValue(t, meta, "max_preview_bytes", int64(5))
				requireThreadAttachmentMetadataValue(t, meta, "preview_bytes", 5)
			},
		},
		{
			name: "read_attachment",
			tool: newReadAttachmentTool(),
			args: json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","max_bytes":4}`, artifact.ID)),
			kind: "attachment_read",
			validate: func(t *testing.T, meta map[string]any) {
				t.Helper()
				requireThreadAttachmentMetadataValue(t, meta, "artifact_id", artifact.ID)
				requireThreadAttachmentMetadataValue(t, meta, "file_name", "metadata.txt")
				requireThreadAttachmentMetadataValue(t, meta, "offset", int64(0))
				requireThreadAttachmentMetadataValue(t, meta, "max_bytes", 4)
				requireThreadAttachmentMetadataValue(t, meta, "bytes_returned", 4)
				requireThreadAttachmentMetadataValue(t, meta, "has_more", true)
				requireThreadAttachmentMetadataValue(t, meta, "source", "attachment")
			},
		},
		{
			name: "search_attachment",
			tool: newSearchAttachmentTool(),
			args: json.RawMessage(fmt.Sprintf(`{"artifact_id":"%s","query":"beta","limit":2}`, artifact.ID)),
			kind: "attachment_search",
			validate: func(t *testing.T, meta map[string]any) {
				t.Helper()
				requireThreadAttachmentMetadataValue(t, meta, "artifact_id", artifact.ID)
				requireThreadAttachmentMetadataValue(t, meta, "file_name", "metadata.txt")
				requireThreadAttachmentMetadataValue(t, meta, "query", "beta")
				requireThreadAttachmentMetadataValue(t, meta, "limit", 2)
				requireThreadAttachmentMetadataValue(t, meta, "matches_count", 1)
				requireThreadAttachmentMetadataValue(t, meta, "search_truncated", false)
				requireThreadAttachmentMetadataValue(t, meta, "source", "attachment")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := tt.tool.Execute(ctx, exec, tt.args)
			if err != nil {
				t.Fatalf("%s Execute: %v", tt.name, err)
			}
			if !out.OK {
				t.Fatalf("%s output not OK: %#v", tt.name, out.Result)
			}
			meta := requireThreadAttachmentResultMetadata(t, out, tt.name, tt.kind)
			tt.validate(t, meta)
		})
	}
}

func requireThreadAttachmentResultMetadata(t *testing.T, out Output, toolName string, kind string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
		}
	}
	meta := out.ResultMetadata
	if meta == nil {
		t.Fatalf("%s ResultMetadata is nil", toolName)
	}
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) not found", toolName)
	}
	requireThreadAttachmentMetadataValue(t, meta, "provider", "attachments")
	requireThreadAttachmentMetadataValue(t, meta, "product", "attachments")
	requireThreadAttachmentMetadataValue(t, meta, "family", toolMeta.Presentation.Family)
	requireThreadAttachmentMetadataValue(t, meta, "tool_name", toolMeta.Name)
	requireThreadAttachmentMetadataValue(t, meta, "tool_set_id", toolMeta.ToolSetID)
	requireThreadAttachmentMetadataValue(t, meta, "tool_set_name", toolMeta.ToolSetName)
	requireThreadAttachmentMetadataValue(t, meta, "category", string(toolMeta.Category))
	requireThreadAttachmentMetadataValue(t, meta, "origin", string(toolMeta.Origin))
	requireThreadAttachmentMetadataValue(t, meta, "kind", kind)
	requireThreadAttachmentMetadataValue(t, meta, "result_kind", toolMeta.Presentation.ResultKind)
	requireThreadAttachmentMetadataValue(t, meta, "native_viewer", toolMeta.Presentation.NativeViewer)
	requireThreadAttachmentMetadataValue(t, meta, "renderer_strategy", toolMeta.Presentation.RendererStrategy)
	requireThreadAttachmentMetadataValue(t, meta, "renderer_key", toolMeta.Presentation.RendererKey)
	requireThreadAttachmentMetadataValue(t, meta, "readonly", true)
	if _, exists := meta["connector_id"]; exists {
		t.Fatalf("%s attached tool metadata should not expose connector_id: %#v", toolName, meta)
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("%s ResultMetadata.tool_presentation=%T, want map[string]any; metadata=%#v", toolName, meta["tool_presentation"], meta)
	}
	requireThreadAttachmentMetadataValue(t, presentation, "family", toolMeta.Presentation.Family)
	requireThreadAttachmentMetadataValue(t, presentation, "result_kind", toolMeta.Presentation.ResultKind)
	requireThreadAttachmentMetadataValue(t, presentation, "native_viewer", toolMeta.Presentation.NativeViewer)
	requireThreadAttachmentMetadataValue(t, presentation, "renderer_strategy", toolMeta.Presentation.RendererStrategy)
	requireThreadAttachmentMetadataValue(t, presentation, "renderer_key", toolMeta.Presentation.RendererKey)
	requireThreadAttachmentMetadataValue(t, presentation, "file_capability", toolMeta.Presentation.FileCapability)
	return meta
}

func requireThreadAttachmentMetadataValue(t *testing.T, meta map[string]any, key string, want any) {
	t.Helper()
	got, ok := meta[key]
	if !ok {
		t.Fatalf("metadata missing %s; metadata=%#v", key, meta)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("metadata[%s]=%#v, want %#v; metadata=%#v", key, got, want, meta)
	}
}

func insertThreadAttachmentArtifact(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	cfgDir string,
	threadID string,
	fileName string,
	content string,
) store.Artifact {
	t.Helper()

	relPath := filepath.ToSlash(filepath.Join("thread_uploads", threadID, fileName))
	fullPath := filepath.Join(cfgDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	toolName := types.ThreadFileAttachmentToolName
	okValue := true
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    threadID,
		Type:        "file",
		Title:       fileName,
		ToolName:    &toolName,
		OK:          &okValue,
		Storage:     "file",
		ContentPath: &relPath,
		ByteSize:    int64(len(content)),
		Metadata: map[string]any{
			"mimeType":         "text/plain",
			"detectedMimeType": "text/plain",
			"isText":           true,
			"previewAvailable": true,
			"previewTruncated": false,
			"previewText":      content,
			"source":           types.ChatUploadAttachmentSource,
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	return artifact
}

func insertExternalToolFileArtifact(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	cfgDir string,
	threadID string,
	toolName string,
	fileName string,
	content string,
) store.Artifact {
	t.Helper()

	relPath := filepath.ToSlash(filepath.Join("tool_files", threadID, fileName))
	fullPath := filepath.Join(cfgDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
		t.Fatalf("MkdirAll external: %v", err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile external: %v", err)
	}

	okValue := true
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    threadID,
		Type:        "file",
		Title:       fileName,
		ToolName:    &toolName,
		OK:          &okValue,
		Storage:     "file",
		ContentPath: &relPath,
		ByteSize:    int64(len(content)),
		Metadata: map[string]any{
			"mimeType":         "text/csv",
			"detectedMimeType": "text/plain",
			"isText":           true,
			"previewAvailable": true,
			"previewTruncated": false,
			"previewText":      content,
			"source":           "external_tool",
			"externalToolFile": true,
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact external: %v", err)
	}
	return artifact
}

func insertSpreadsheetExtractionArtifact(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	cfgDir string,
	threadID string,
) store.Artifact {
	t.Helper()

	rawRelPath := filepath.ToSlash(filepath.Join("thread_uploads", threadID, "revenue.xlsx"))
	rawFullPath := filepath.Join(cfgDir, filepath.FromSlash(rawRelPath))
	if err := os.MkdirAll(filepath.Dir(rawFullPath), 0o700); err != nil {
		t.Fatalf("MkdirAll raw: %v", err)
	}
	if err := os.WriteFile(rawFullPath, []byte("binary workbook"), 0o600); err != nil {
		t.Fatalf("WriteFile raw: %v", err)
	}

	rootRel := filepath.ToSlash(filepath.Join("thread_uploads", threadID, "revenue.extract"))
	manifestRel := filepath.ToSlash(filepath.Join(rootRel, "manifest.json"))
	workbookRel := filepath.ToSlash(filepath.Join(rootRel, "workbook.json"))
	markdownRel := filepath.ToSlash(filepath.Join(rootRel, "index", "workbook.md"))
	cellsRel := filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "cells-0001.jsonl"))
	for rel, body := range map[string]string{
		manifestRel: `{"pipeline":"xlsx","status":"complete","sheetCount":1,"rowCount":3,"columnCount":3}`,
		workbookRel: `{"sheets":[{"name":"Revenue","rowCount":3,"columnCount":3}]}`,
		markdownRel: "# Revenue\n\n- Rows: 3\n- Columns: 3\n",
		cellsRel:    `{"address":"A2","display":"North"}` + "\n",
	} {
		full := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("MkdirAll %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatalf("WriteFile %s: %v", rel, err)
		}
	}

	toolName := types.ThreadFileAttachmentToolName
	okValue := true
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    threadID,
		Type:        "file",
		Title:       "revenue.xlsx",
		ToolName:    &toolName,
		OK:          &okValue,
		Storage:     "file",
		ContentPath: &rawRelPath,
		ByteSize:    int64(len("binary workbook")),
		Metadata: map[string]any{
			"mimeType":         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"detectedMimeType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"isText":           false,
			"previewAvailable": false,
			"previewTruncated": false,
			"contentNote":      "Binary or unsupported text preview omitted. File type was evaluated before further processing.",
			"source":           types.ChatUploadAttachmentSource,
			"extraction": map[string]any{
				"kind":         "spreadsheet",
				"pipeline":     "xlsx",
				"version":      1,
				"status":       "complete",
				"rootPath":     rootRel,
				"manifestPath": manifestRel,
				"workbookPath": workbookRel,
				"dataPath":     workbookRel,
				"markdownPath": markdownRel,
				"sheetCount":   1,
				"rowCount":     3,
				"columnCount":  3,
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	return artifact
}
