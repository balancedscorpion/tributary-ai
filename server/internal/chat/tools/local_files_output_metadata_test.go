package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
)

func requireLocalFilesResultMetadata(t *testing.T, out Output, toolName, kind string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != "local-files" || meta["product"] != "local-files" || meta["family"] != "local-files" {
		t.Fatalf("%s ResultMetadata=%#v, want local-files provider/product/family", toolName, meta)
	}
	if meta["tool_name"] != toolName || meta["kind"] != kind || meta["tool_set_id"] != "local-files" {
		t.Fatalf("%s ResultMetadata=%#v, want tool=%s kind=%s tool_set_id=local-files", toolName, meta, toolName, kind)
	}
	if meta["readonly"] != true || meta["category"] != "read" || meta["origin"] != "local" {
		t.Fatalf("%s ResultMetadata=%#v, want local read metadata", toolName, meta)
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
	return meta
}

func TestLocalFileToolsUseFirstClassResultMetadata(t *testing.T) {
	ps, err := projects.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "local-files-project"
	if err := ps.PutFile(projectID, "README.md", []byte("needle\nsecond line\n")); err != nil {
		t.Fatalf("PutFile README: %v", err)
	}
	if err := ps.PutFile(projectID, "src/app.txt", []byte("needle in source\n")); err != nil {
		t.Fatalf("PutFile app: %v", err)
	}
	execCtx := ExecContext{ProjectID: projectID, Projects: ps}

	listOut := executeToolForOutput(t, newListFilesTool(), execCtx, map[string]any{})
	listMeta := requireLocalFilesResultMetadata(t, listOut, "list_files", "file_list")
	if listMeta["total_count"] == nil {
		t.Fatalf("list_files ResultMetadata missing total_count: %#v", listMeta)
	}

	readOut := executeToolForOutput(t, newReadFileTool(), execCtx, map[string]any{"path": "README.md"})
	readMeta := requireLocalFilesResultMetadata(t, readOut, "read_file", "file")
	if readMeta["path"] != "README.md" || readMeta["bytes_returned"] == nil {
		t.Fatalf("read_file ResultMetadata=%#v, want path and bytes_returned", readMeta)
	}
	readResult := readOut.Result.(map[string]any)
	if !strings.Contains(readResult["content"].(string), "needle") {
		t.Fatalf("read_file content=%q, want needle", readResult["content"])
	}

	localRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(localRoot, "local.txt"), []byte("local needle\n"), 0o600); err != nil {
		t.Fatalf("WriteFile local.txt: %v", err)
	}
	localReadOut := executeToolForOutput(t, newLocalReadFileTool(), ExecContext{}, map[string]any{
		"local_root": localRoot,
		"path":       "local.txt",
	})
	localReadMeta := requireLocalFilesResultMetadata(t, localReadOut, "local_read_file", "file")
	if localReadMeta["path"] != "local.txt" || localReadMeta["local_path"] == "" || localReadMeta["bytes_returned"] == nil {
		t.Fatalf("local_read_file ResultMetadata=%#v, want path, local_path, and bytes_returned", localReadMeta)
	}

	searchOut := executeToolForOutput(t, newSearchFilesTool(), execCtx, map[string]any{"query": "needle"})
	searchMeta := requireLocalFilesResultMetadata(t, searchOut, "search_files", "file_search")
	if searchMeta["query"] != "needle" || searchMeta["matches"] == nil {
		t.Fatalf("search_files ResultMetadata=%#v, want query and matches", searchMeta)
	}

	emptySearchOut := executeToolForOutput(t, newSearchFilesTool(), execCtx, map[string]any{"query": "absent"})
	emptyMeta := requireLocalFilesResultMetadata(t, emptySearchOut, "search_files", "file_search")
	if emptyMeta["empty_state"] != true || emptyMeta["matches"] != 0 {
		t.Fatalf("empty search ResultMetadata=%#v, want empty_state", emptyMeta)
	}
}

func executeToolForOutput(t *testing.T, tool Tool, execCtx ExecContext, args map[string]any) Output {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(context.Background(), execCtx, raw)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("tool returned error: %#v", out.Result)
	}
	return out
}
