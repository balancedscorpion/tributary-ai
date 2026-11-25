package tools

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/pdfextract"
	"ai-analyst-local/internal/chat/store"
)

func TestFileSearchDefinitionDeclaresBoundedSchema(t *testing.T) {
	def := newFileSearchTool().Definition()
	if def.Type != "function" || def.Function.Name != "file_search" {
		t.Fatalf("definition=%#v, want file_search function", def)
	}
	params := def.Function.Parameters
	if got, _ := params["type"].(string); got != "object" {
		t.Fatalf("parameters.type=%#v, want object", params["type"])
	}
	additional, ok := params["additionalProperties"].(bool)
	if !ok || additional {
		t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T, want map", params["properties"])
	}
	for _, name := range []string{"query", "location_hint", "file_types", "max_results"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("properties missing %q: %#v", name, properties)
		}
	}
	if _, ok := properties["local_root"]; ok {
		t.Fatalf("properties expose local_root to the model: %#v", properties)
	}
	required := toolMetadataRequiredFields(params)
	for _, name := range []string{"query"} {
		if !containsString(required, name) {
			t.Fatalf("required=%v, want %s", required, name)
		}
	}
	if containsString(required, "local_root") {
		t.Fatalf("required=%v should not include local_root", required)
	}
}

func TestLocalListFolderDefinitionDeclaresBoundedSchema(t *testing.T) {
	def := newLocalListFolderTool().Definition()
	if def.Type != "function" || def.Function.Name != "local_list_folder" {
		t.Fatalf("definition=%#v, want local_list_folder function", def)
	}
	params := def.Function.Parameters
	if got, _ := params["type"].(string); got != "object" {
		t.Fatalf("parameters.type=%#v, want object", params["type"])
	}
	additional, ok := params["additionalProperties"].(bool)
	if !ok || additional {
		t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T, want map", params["properties"])
	}
	for _, name := range []string{"path", "recursive", "max_entries"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("properties missing %q: %#v", name, properties)
		}
	}
	if _, ok := properties["local_root"]; ok {
		t.Fatalf("properties expose local_root to the model: %#v", properties)
	}
	required := toolMetadataRequiredFields(params)
	if containsString(required, "local_root") {
		t.Fatalf("required=%v should not include local_root", required)
	}
}

func TestLocalReadFileDefinitionDeclaresBoundedSchema(t *testing.T) {
	def := newLocalReadFileTool().Definition()
	if def.Type != "function" || def.Function.Name != "local_read_file" {
		t.Fatalf("definition=%#v, want local_read_file function", def)
	}
	params := def.Function.Parameters
	if got, _ := params["type"].(string); got != "object" {
		t.Fatalf("parameters.type=%#v, want object", params["type"])
	}
	additional, ok := params["additionalProperties"].(bool)
	if !ok || additional {
		t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T, want map", params["properties"])
	}
	for _, name := range []string{"path", "max_bytes", "page_token"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("properties missing %q: %#v", name, properties)
		}
	}
	if _, ok := properties["local_root"]; ok {
		t.Fatalf("properties expose local_root to the model: %#v", properties)
	}
	required := toolMetadataRequiredFields(params)
	for _, name := range []string{"path"} {
		if !containsString(required, name) {
			t.Fatalf("required=%v, want %s", required, name)
		}
	}
	if containsString(required, "local_root") {
		t.Fatalf("required=%v should not include local_root", required)
	}
}

func TestLocalOpenFileDefinitionDeclaresViewerSchema(t *testing.T) {
	def := newLocalOpenFileTool().Definition()
	if def.Type != "function" || def.Function.Name != LocalOpenFileToolName {
		t.Fatalf("definition=%#v, want local_open_file function", def)
	}
	for _, want := range []string{"native file viewer", "snapshotting only that file", "view", "open", "render"} {
		if !strings.Contains(def.Function.Description, want) {
			t.Fatalf("description missing %q: %s", want, def.Function.Description)
		}
	}
	params := def.Function.Parameters
	if got, _ := params["type"].(string); got != "object" {
		t.Fatalf("parameters.type=%#v, want object", params["type"])
	}
	additional, ok := params["additionalProperties"].(bool)
	if !ok || additional {
		t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T, want map", params["properties"])
	}
	if _, ok := properties["path"]; !ok {
		t.Fatalf("properties missing path: %#v", properties)
	}
	if _, ok := properties["local_root"]; ok {
		t.Fatalf("properties expose local_root to the model: %#v", properties)
	}
	required := toolMetadataRequiredFields(params)
	if !containsString(required, "path") {
		t.Fatalf("required=%v, want path", required)
	}
	if containsString(required, "local_root") {
		t.Fatalf("required=%v should not include local_root", required)
	}
}

func TestLocalReadPDFPageDefinitionDeclaresBoundedSchema(t *testing.T) {
	def := newLocalReadPDFPageTool().Definition()
	if def.Type != "function" || def.Function.Name != "local_read_pdf_page" {
		t.Fatalf("definition=%#v, want local_read_pdf_page function", def)
	}
	for _, want := range []string{
		"exact page-number questions",
		"instead of whole-document PDF extraction",
		"1-based",
		"page_count",
		"method metadata",
	} {
		if !strings.Contains(def.Function.Description, want) {
			t.Fatalf("description missing %q: %s", want, def.Function.Description)
		}
	}
	params := def.Function.Parameters
	if got, _ := params["type"].(string); got != "object" {
		t.Fatalf("parameters.type=%#v, want object", params["type"])
	}
	additional, ok := params["additionalProperties"].(bool)
	if !ok || additional {
		t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
	}
	properties, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties=%T, want map", params["properties"])
	}
	for _, name := range []string{"path", "page_number", "dpi", "max_chars"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("properties missing %q: %#v", name, properties)
		}
	}
	if _, ok := properties["local_root"]; ok {
		t.Fatalf("properties expose local_root to the model: %#v", properties)
	}
	required := toolMetadataRequiredFields(params)
	for _, name := range []string{"path", "page_number"} {
		if !containsString(required, name) {
			t.Fatalf("required=%v, want %s", required, name)
		}
	}
	if containsString(required, "local_root") {
		t.Fatalf("required=%v should not include local_root", required)
	}
}

func TestLocalGrantToolDefinitionsDeclareBoundedSchemas(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tool       Tool
		required   []string
		properties []string
	}{
		{
			name:       "local_list_grant_folder",
			tool:       newLocalListGrantFolderTool(),
			required:   []string{"grant_id"},
			properties: []string{"grant_id", "relative_path", "recursive", "max_entries"},
		},
		{
			name:       "local_search_in_grant",
			tool:       newLocalSearchInGrantTool(),
			required:   []string{"grant_id", "query"},
			properties: []string{"grant_id", "query", "relative_path", "file_types", "max_results"},
		},
		{
			name:       "local_read_grant_file",
			tool:       newLocalReadGrantFileTool(),
			required:   []string{"grant_id", "file_ref"},
			properties: []string{"grant_id", "file_ref", "max_bytes", "page_token"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			def := tc.tool.Definition()
			if def.Type != "function" || def.Function.Name != tc.name {
				t.Fatalf("definition=%#v, want %s function", def, tc.name)
			}
			params := def.Function.Parameters
			if got, _ := params["type"].(string); got != "object" {
				t.Fatalf("parameters.type=%#v, want object", params["type"])
			}
			additional, ok := params["additionalProperties"].(bool)
			if !ok || additional {
				t.Fatalf("additionalProperties=%#v, want false", params["additionalProperties"])
			}
			properties, ok := params["properties"].(map[string]any)
			if !ok {
				t.Fatalf("properties=%T, want map", params["properties"])
			}
			for _, name := range tc.properties {
				if _, ok := properties[name]; !ok {
					t.Fatalf("properties missing %q: %#v", name, properties)
				}
			}
			if _, ok := properties["local_root"]; ok {
				t.Fatalf("properties expose local_root to the search agent: %#v", properties)
			}
			required := toolMetadataRequiredFields(params)
			for _, name := range tc.required {
				if !containsString(required, name) {
					t.Fatalf("required=%v, want %s", required, name)
				}
			}
			if containsString(required, "local_root") {
				t.Fatalf("required=%v should not include local_root", required)
			}
		})
	}
}

func TestLocalListFolderListsApprovedFolderSnapshotAndSkipsSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.md"), []byte("summary\n"), 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "deck.pdf"), []byte("%PDF-1.4\n"), 0o600); err != nil {
		t.Fatalf("write deck: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=value\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}

	out, err := newLocalListFolderTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root":  root,
		"grant_id":    "perm_local",
		"max_entries": 20,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["local_kind"] != "folder" || result["grant_id"] != "perm_local" || result["truncated"] != false {
		t.Fatalf("result metadata=%#v, want folder grant list", result)
	}
	entries, _ := result["entries"].([]map[string]any)
	paths := localMachineEntryPathsForTest(entries)
	for _, want := range []string{"deck.pdf", "reports", "summary.md"} {
		if !containsString(paths, want) {
			t.Fatalf("entry paths=%v, want %s", paths, want)
		}
	}
	if containsString(paths, ".env") || containsString(paths, ".git") {
		t.Fatalf("entry paths=%v should skip sensitive/.git entries", paths)
	}
	files, _ := result["files"].([]map[string]any)
	if len(files) != 2 {
		t.Fatalf("files=%#v, want two listed files", result["files"])
	}
	var sawPDF bool
	for _, file := range files {
		if file["file_ref"] == "deck.pdf" && file["file_kind"] == "pdf" && file["local_path"] != "" {
			sawPDF = true
		}
	}
	if !sawPDF {
		t.Fatalf("files=%#v, want PDF file metadata with local path", files)
	}
	presentation, ok := out.ResultMetadata["tool_presentation"].(map[string]any)
	if !ok || presentation["file_capability"] != "local_folder_list" || presentation["result_kind"] != "folder" {
		t.Fatalf("tool_presentation=%#v, want folder list presentation", out.ResultMetadata["tool_presentation"])
	}
}

func TestLocalListFolderRecursiveHonorsSubpathLimitAndTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports", "nested"), 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	for _, name := range []string{"a.txt", "b.txt", "nested/c.txt"} {
		if err := os.WriteFile(filepath.Join(root, "reports", filepath.FromSlash(name)), []byte("ok\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	out, err := newLocalListFolderTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root":  root,
		"path":        "reports",
		"recursive":   true,
		"max_entries": 2,
	}))
	if err != nil {
		t.Fatalf("Execute recursive: %v", err)
	}
	if !out.OK {
		t.Fatalf("recursive output not OK: %#v", out)
	}
	result := out.Result.(map[string]any)
	entries, _ := result["entries"].([]map[string]any)
	if len(entries) != 2 || result["truncated"] != true {
		t.Fatalf("entries=%#v truncated=%#v, want limit and truncation", entries, result["truncated"])
	}

	out, err = newLocalListFolderTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "../outside",
	}))
	if err != nil {
		t.Fatalf("Execute traversal: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "invalid path traversal") {
		t.Fatalf("traversal output=%#v, want traversal rejection", out)
	}
}

func TestFileSearchSearchesLocalRootWithoutProjectAndSkipsSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("alpha\nneedle in content\n"), 0o600); err != nil {
		t.Fatalf("write report: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "needle-name.txt"), []byte("ordinary text\n"), 0o600); err != nil {
		t.Fatalf("write name match: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=needle\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	out, err := newFileSearchTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"query":      "needle",
		"limit":      20,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	nameMatches, _ := result["name_matches"].([]string)
	if !containsString(nameMatches, "needle-name.txt") {
		t.Fatalf("name_matches=%v, want needle-name.txt", nameMatches)
	}
	contentMatches, _ := result["content_matches"].(map[string][]string)
	if len(contentMatches["report.txt"]) == 0 {
		t.Fatalf("content_matches=%#v, want report.txt match", contentMatches)
	}
	if _, ok := contentMatches[".env"]; ok {
		t.Fatalf("content_matches=%#v, should skip sensitive .env", contentMatches)
	}
	files, _ := result["files"].([]map[string]any)
	if len(files) != 2 {
		t.Fatalf("files=%#v, want two unique file refs", result["files"])
	}
	fileRefs := []string{}
	for _, file := range files {
		fileRefs = append(fileRefs, strings.TrimSpace(fmt.Sprint(file["file_ref"])))
		if file["display_path"] == "" || file["file_kind"] == "" {
			t.Fatalf("file entry=%#v, want display_path and file_kind", file)
		}
	}
	if !containsString(fileRefs, "needle-name.txt") || !containsString(fileRefs, "report.txt") {
		t.Fatalf("file refs=%v, want search/readable refs for both matches", fileRefs)
	}
	if got := out.ResultMetadata["tool_name"]; got != "file_search" {
		t.Fatalf("tool_name=%#v, want file_search; metadata=%#v", got, out.ResultMetadata)
	}
	presentation, ok := out.ResultMetadata["tool_presentation"].(map[string]any)
	if !ok || presentation["file_capability"] != "local_file_search" {
		t.Fatalf("tool_presentation=%#v, want local_file_search", out.ResultMetadata["tool_presentation"])
	}
}

func TestFileSearchDeadlineReturnsPartialObservation(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forecast.txt"), []byte("quarterly forecast"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	out, err := newFileSearchTool().Execute(ctx, ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root, "local_kind": "folder", "query": "forecast", "portable_search": true,
	}))
	if err != nil || !out.OK {
		t.Fatalf("deadline search output=%+v err=%v", out, err)
	}
	result, _ := out.Result.(map[string]any)
	if result["timed_out"] != true || result["truncated"] != true {
		t.Fatalf("deadline result=%#v, want bounded partial observation", result)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(strings.ToLower(string(raw)), "context deadline exceeded") {
		t.Fatalf("deadline leaked through tool output: %s", raw)
	}
}

func TestLocalMachineSearchBasesPrioritizesUserLocationsForFilesystemRoot(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("home directory unavailable")
	}
	home = filepath.Clean(home)
	root := filepath.VolumeName(home) + string(filepath.Separator)
	bases := localMachineSearchBases(root)
	if len(bases) < 2 {
		t.Fatalf("bases=%v, want user locations then filesystem root", bases)
	}
	if !containsString(bases, home) || filepath.Clean(bases[len(bases)-1]) != filepath.Clean(root) {
		t.Fatalf("bases=%v, want home included and root last", bases)
	}
}

func TestLocalReadFileReadsLocalRootWithoutProject(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "summary.txt"), []byte("alpha\nneedle in content\n"), 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	out, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "reports/summary.txt",
		"max_bytes":  5,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["path"] != "reports/summary.txt" || result["content"] != "alpha" || result["has_more"] != true {
		t.Fatalf("result=%#v, want bounded local file content", result)
	}
	if result["file_ref"] != "reports/summary.txt" || result["display_path"] != "reports/summary.txt" || result["file_kind"] != "document" {
		t.Fatalf("result=%#v, want stable file ref and file metadata", result)
	}
	if got := out.ResultMetadata["tool_name"]; got != "local_read_file" {
		t.Fatalf("tool_name=%#v, want local_read_file; metadata=%#v", got, out.ResultMetadata)
	}
	if out.ResultMetadata["path"] != "reports/summary.txt" || out.ResultMetadata["file_ref"] != "reports/summary.txt" || out.ResultMetadata["local_path"] == "" {
		t.Fatalf("metadata=%#v, want local path and file ref details", out.ResultMetadata)
	}
	presentation, ok := out.ResultMetadata["tool_presentation"].(map[string]any)
	if !ok || presentation["file_capability"] != "local_file_read" || presentation["promotes_native_input"] != true {
		t.Fatalf("tool_presentation=%#v, want local_file_read native input", out.ResultMetadata["tool_presentation"])
	}
}

func TestLocalOpenFileSnapshotsPDFAsNativeFileArtifact(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	sourcePath := filepath.Join(root, "reports", "forecast.pdf")
	writeLocalMachinePDFTestFile(t, sourcePath, "Native Viewer Alpha")

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	out, err := newLocalOpenFileTool().Execute(ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		RunID:    "run-local-open-test",
		CfgDir:   cfgDir,
	}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "reports/forecast.pdf",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	artifactID := strings.TrimSpace(fmt.Sprint(result["attachment_artifact_id"]))
	if artifactID == "" || artifactID != strings.TrimSpace(fmt.Sprint(result["file_artifact_id"])) {
		t.Fatalf("result artifact ids missing or inconsistent: %#v", result)
	}
	if result["native_file_input"] != true || result["model_file_input"] != true {
		t.Fatalf("result=%#v, want native/model file flags", result)
	}
	if result["mime_type"] != "application/pdf" || result["file_kind"] != "pdf" {
		t.Fatalf("result=%#v, want PDF metadata", result)
	}
	if got := out.ResultMetadata["attachment_artifact_id"]; got != artifactID {
		t.Fatalf("result metadata attachment_artifact_id=%#v, want %q", got, artifactID)
	}
	if out.ResultMetadata["native_file_input"] != true || out.ResultMetadata["model_file_input"] != true {
		t.Fatalf("result metadata=%#v, want native/model file flags", out.ResultMetadata)
	}
	presentation, ok := out.ResultMetadata["tool_presentation"].(map[string]any)
	if !ok ||
		presentation["native_viewer"] != "file" ||
		presentation["produces_file_artifact"] != true ||
		presentation["promotes_native_input"] != true {
		t.Fatalf("tool_presentation=%#v, want file artifact producer", out.ResultMetadata["tool_presentation"])
	}

	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.Type != "file" || artifact.Storage != "file" || artifact.ContentPath == nil {
		t.Fatalf("artifact=%#v, want stored file artifact with content path", artifact)
	}
	if artifact.Metadata["external_tool_file"] != true ||
		artifact.Metadata["native_file_input"] != true ||
		artifact.Metadata["model_file_input"] != true {
		t.Fatalf("artifact metadata=%#v, want native external file metadata", artifact.Metadata)
	}
	raw, err := os.ReadFile(filepath.Join(cfgDir, filepath.FromSlash(*artifact.ContentPath)))
	if err != nil {
		t.Fatalf("read stored raw file: %v", err)
	}
	if !bytes.HasPrefix(raw, []byte("%PDF-1.4")) {
		t.Fatalf("stored raw prefix=%q, want PDF bytes", string(raw[:min(len(raw), 16)]))
	}
}

func TestLocalReadFileExtractsPDFText(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	writeLocalMachinePDFTestFile(t, filepath.Join(root, "reports", "forecast.pdf"), "Local PDF Grant Alpha")

	out, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "reports/forecast.pdf",
		"max_bytes":  2048,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["file_kind"] != "pdf" || result["source"] != "pdf_extraction" || result["binary"] != false {
		t.Fatalf("result=%#v, want PDF extraction metadata", result)
	}
	if content := fmt.Sprint(result["content"]); !strings.Contains(content, "Local PDF Grant Alpha") {
		t.Fatalf("content=%q, want extracted PDF text", content)
	}
	if omitted, _ := result["content_omitted"].(bool); omitted {
		t.Fatalf("result=%#v, did not expect extracted PDF content to be omitted", result)
	}
	extraction, ok := result["extraction"].(map[string]any)
	if !ok || extraction["kind"] != "pdf" {
		t.Fatalf("extraction=%#v, want PDF extraction metadata", result["extraction"])
	}
	if out.ResultMetadata["source"] != "pdf_extraction" || out.ResultMetadata["file_kind"] != "pdf" {
		t.Fatalf("metadata=%#v, want PDF extraction metadata", out.ResultMetadata)
	}
}

func TestLocalReadPDFPageUsesEmbeddedTextBeforeOCR(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	pdfPath := filepath.Join(root, "reports", "forecast.pdf")
	writeLocalMachinePDFTestFile(t, pdfPath, "Local PDF Grant Alpha")
	if _, _, _, err := localExtractPDFPageText(context.Background(), pdfPath, 1); err != nil {
		if _, pageTextErr := pdfextract.ExtractPageText(pdfPath, 1); pageTextErr != nil {
			t.Skipf("embedded PDF page text extraction unavailable: pdftotext=%v pypdf=%v", err, pageTextErr)
		}
	}

	out, err := newLocalReadPDFPageTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root":  root,
		"path":        "reports/forecast.pdf",
		"page_number": 1,
		"dpi":         180,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	method := fmt.Sprint(result["method"])
	if result["file_kind"] != "pdf" || result["page_number"] != 1 || method == "" || strings.Contains(method, "pdftoppm") || strings.Contains(method, "tesseract") {
		t.Fatalf("result=%#v, want embedded PDF page text metadata before OCR", result)
	}
	if text := fmt.Sprint(result["text"]); !strings.Contains(text, "Local PDF Grant Alpha") {
		t.Fatalf("page text=%q, want fixture text", text)
	}
	if got := out.ResultMetadata["tool_name"]; got != "local_read_pdf_page" {
		t.Fatalf("tool_name=%#v, want local_read_pdf_page; metadata=%#v", got, out.ResultMetadata)
	}
	presentation, ok := out.ResultMetadata["tool_presentation"].(map[string]any)
	if !ok || presentation["file_capability"] != "local_file_read" || presentation["promotes_native_input"] != true {
		t.Fatalf("tool_presentation=%#v, want local_file_read native input", out.ResultMetadata["tool_presentation"])
	}
}

func TestLocalReadPDFPageFallsBackToEmbeddedTextWhenRenderToolsUnavailable(t *testing.T) {
	original := localToolExecutable
	localToolExecutable = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { localToolExecutable = original })

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	pdfPath := filepath.Join(root, "reports", "forecast.pdf")
	writeLocalMachinePDFTestFile(t, pdfPath, "Local PDF Grant Fallback Alpha")
	if _, err := pdfextract.ExtractPageText(pdfPath, 1); err != nil {
		t.Skipf("pypdf embedded PDF page text fallback unavailable: %v", err)
	}

	out, err := newLocalReadPDFPageTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root":  root,
		"path":        "reports/forecast.pdf",
		"page_number": 1,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["file_kind"] != "pdf" || result["page_number"] != 1 {
		t.Fatalf("result=%#v, want PDF page metadata", result)
	}
	if method := fmt.Sprint(result["method"]); method == "" || strings.Contains(method, "pdftoppm") || strings.Contains(method, "tesseract") || strings.Contains(method, "fallback_after_ocr_unavailable") {
		t.Fatalf("method=%q, want embedded text fallback method", method)
	}
	if text := fmt.Sprint(result["text"]); !strings.Contains(text, "Local PDF Grant Fallback Alpha") {
		t.Fatalf("fallback text=%q, want fixture text", text)
	}
	if warnings := fmt.Sprint(result["warnings"]); strings.Contains(warnings, "pdftoppm") || strings.Contains(warnings, "OCR failed") {
		t.Fatalf("warnings=%#v, did not expect render-tool warning when embedded text succeeds", result["warnings"])
	}
}

func TestLocalReadFileExtractsXLSXSpreadsheetText(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "revenue.xlsx"), localMachineTestXLSX(t), 0o600); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}

	out, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "revenue.xlsx",
		"max_bytes":  4096,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["file_kind"] != "spreadsheet" || result["source"] != "file_extraction" || result["binary"] != false {
		t.Fatalf("result=%#v, want spreadsheet extraction metadata", result)
	}
	content := fmt.Sprint(result["content"])
	if !strings.Contains(content, "Revenue") || !strings.Contains(content, "North") || !strings.Contains(content, "South") {
		t.Fatalf("content=%q, want extracted spreadsheet text", content)
	}
	extraction, ok := result["extraction"].(map[string]any)
	if !ok || extraction["kind"] != "spreadsheet" || extraction["pipeline"] != "xlsx" {
		t.Fatalf("extraction=%#v, want XLSX extraction metadata", result["extraction"])
	}
}

func TestLocalReadFileExtractsHTMLText(t *testing.T) {
	root := t.TempDir()
	raw := `<html><head><title>Quarterly Note</title><script>ignore()</script></head><body><h1>Revenue</h1><p>North grew.</p></body></html>`
	if err := os.WriteFile(filepath.Join(root, "note.html"), []byte(raw), 0o600); err != nil {
		t.Fatalf("write html: %v", err)
	}

	out, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "note.html",
		"max_bytes":  4096,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["file_kind"] != "html" || result["source"] != "file_extraction" || result["binary"] != false {
		t.Fatalf("result=%#v, want HTML extraction metadata", result)
	}
	content := fmt.Sprint(result["content"])
	if !strings.Contains(content, "Revenue") || !strings.Contains(content, "North grew.") || strings.Contains(content, "ignore()") {
		t.Fatalf("content=%q, want extracted HTML body text without script", content)
	}
	extraction, ok := result["extraction"].(map[string]any)
	if !ok || extraction["kind"] != "text" || extraction["pipeline"] != "html" || extraction["htmlTitle"] != "Quarterly Note" {
		t.Fatalf("extraction=%#v, want HTML extraction metadata", result["extraction"])
	}
}

func TestFileSearchSearchesSingleFileGrant(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "forecast.txt")
	siblingPath := filepath.Join(root, "sibling.txt")
	if err := os.WriteFile(filePath, []byte("needle in selected file\n"), 0o600); err != nil {
		t.Fatalf("write selected file: %v", err)
	}
	if err := os.WriteFile(siblingPath, []byte("needle in sibling file\n"), 0o600); err != nil {
		t.Fatalf("write sibling file: %v", err)
	}

	out, err := newFileSearchTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": filePath,
		"local_kind": "file",
		"query":      "needle",
		"limit":      20,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["local_kind"] != "file" {
		t.Fatalf("result local_kind=%#v, want file", result["local_kind"])
	}
	files, _ := result["files"].([]map[string]any)
	if len(files) != 1 || files[0]["file_ref"] != "forecast.txt" {
		t.Fatalf("files=%#v, want only selected file ref", result["files"])
	}
	contentMatches, _ := result["content_matches"].(map[string][]string)
	if len(contentMatches["forecast.txt"]) == 0 {
		t.Fatalf("content_matches=%#v, want selected file match", contentMatches)
	}
	if _, ok := contentMatches["sibling.txt"]; ok {
		t.Fatalf("content_matches=%#v, should not inspect sibling", contentMatches)
	}
}

func TestLocalReadFileReadsSingleFileGrant(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "forecast.txt")
	if err := os.WriteFile(filePath, []byte("alpha\nneedle\n"), 0o600); err != nil {
		t.Fatalf("write selected file: %v", err)
	}

	out, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": filePath,
		"local_kind": "file",
		"path":       "forecast.txt",
		"max_bytes":  5,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if result["local_kind"] != "file" || result["path"] != "forecast.txt" || result["content"] != "alpha" {
		t.Fatalf("result=%#v, want exact file grant content", result)
	}

	out, err = newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": filePath,
		"local_kind": "file",
		"path":       "sibling.txt",
	}))
	if err != nil {
		t.Fatalf("Execute sibling: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "escapes local file grant") {
		t.Fatalf("sibling output=%#v, want exact file grant rejection", out)
	}
}

func TestLocalGrantToolsUseInjectedLocalRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "summary.txt"), []byte("alpha\nneedle in content\n"), 0o600); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	searchOut, err := newLocalSearchInGrantTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"grant_id":      "perm_local",
		"local_root":    root,
		"query":         "needle",
		"relative_path": "reports",
	}))
	if err != nil {
		t.Fatalf("search Execute: %v", err)
	}
	if !searchOut.OK {
		t.Fatalf("search output not OK: %#v", searchOut)
	}
	searchResult, ok := searchOut.Result.(map[string]any)
	if !ok {
		t.Fatalf("search result=%T, want map", searchOut.Result)
	}
	if searchResult["grant_id"] != "perm_local" {
		t.Fatalf("search grant_id=%#v, want perm_local", searchResult["grant_id"])
	}
	searchFiles, _ := searchResult["files"].([]map[string]any)
	if len(searchFiles) != 1 || searchFiles[0]["grant_id"] != "perm_local" || searchFiles[0]["file_ref"] != "reports/summary.txt" {
		t.Fatalf("search files=%#v, want grant-scoped file ref", searchResult["files"])
	}
	if searchOut.ResultMetadata["tool_name"] != "local_search_in_grant" || searchOut.ResultMetadata["grant_id"] != "perm_local" {
		t.Fatalf("search metadata=%#v, want internal grant metadata", searchOut.ResultMetadata)
	}

	listOut, err := newLocalListGrantFolderTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"grant_id":      "perm_local",
		"local_root":    root,
		"relative_path": "reports",
	}))
	if err != nil {
		t.Fatalf("list Execute: %v", err)
	}
	if !listOut.OK {
		t.Fatalf("list output not OK: %#v", listOut)
	}
	listResult, ok := listOut.Result.(map[string]any)
	if !ok {
		t.Fatalf("list result=%T, want map", listOut.Result)
	}
	listFiles, _ := listResult["files"].([]map[string]any)
	if listResult["grant_id"] != "perm_local" || len(listFiles) != 1 || listFiles[0]["grant_id"] != "perm_local" || listFiles[0]["file_ref"] != "reports/summary.txt" {
		t.Fatalf("list result=%#v, want grant-scoped file listing", listResult)
	}
	if listOut.ResultMetadata["tool_name"] != "local_list_grant_folder" || listOut.ResultMetadata["grant_id"] != "perm_local" {
		t.Fatalf("list metadata=%#v, want internal grant metadata", listOut.ResultMetadata)
	}

	readOut, err := newLocalReadGrantFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"grant_id":   "perm_local",
		"local_root": root,
		"file_ref":   "reports/summary.txt",
		"max_bytes":  5,
	}))
	if err != nil {
		t.Fatalf("read Execute: %v", err)
	}
	if !readOut.OK {
		t.Fatalf("read output not OK: %#v", readOut)
	}
	readResult, ok := readOut.Result.(map[string]any)
	if !ok {
		t.Fatalf("read result=%T, want map", readOut.Result)
	}
	if readResult["grant_id"] != "perm_local" || readResult["file_ref"] != "reports/summary.txt" {
		t.Fatalf("read result=%#v, want grant and file refs", readResult)
	}
	if readOut.ResultMetadata["tool_name"] != "local_read_grant_file" || readOut.ResultMetadata["grant_id"] != "perm_local" || readOut.ResultMetadata["file_ref"] != "reports/summary.txt" {
		t.Fatalf("read metadata=%#v, want internal grant metadata", readOut.ResultMetadata)
	}
}

func TestFileSearchRejectsUnknownArgumentsAndTraversal(t *testing.T) {
	root := t.TempDir()
	tool := newFileSearchTool()

	out, err := tool.Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"query":      "needle",
		"unexpected": "value",
	}))
	if err != nil {
		t.Fatalf("Execute unknown argument: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "Invalid JSON arguments") {
		t.Fatalf("unknown argument output=%#v, want invalid JSON error", out)
	}

	out, err = tool.Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "../outside",
		"query":      "needle",
	}))
	if err != nil {
		t.Fatalf("Execute traversal: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "invalid path traversal") {
		t.Fatalf("traversal output=%#v, want traversal rejection", out)
	}
}

func TestLocalReadFileRejectsUnknownArgumentsTraversalAndSensitiveFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("SECRET=value\n"), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
	tool := newLocalReadFileTool()

	out, err := tool.Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       ".env",
	}))
	if err != nil {
		t.Fatalf("Execute sensitive file: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "excluded by local permission rules") {
		t.Fatalf("sensitive output=%#v, want exclusion", out)
	}

	out, err = tool.Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "../outside.txt",
	}))
	if err != nil {
		t.Fatalf("Execute traversal: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "invalid path traversal") {
		t.Fatalf("traversal output=%#v, want traversal rejection", out)
	}

	out, err = tool.Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       ".env",
		"unexpected": "value",
	}))
	if err != nil {
		t.Fatalf("Execute unknown argument: %v", err)
	}
	if out.OK || !strings.Contains(outputResultStringForLocalMachineTest(out), "Invalid JSON arguments") {
		t.Fatalf("unknown argument output=%#v, want invalid JSON error", out)
	}
}

func TestFileSearchHonorsLimitAndReportsTruncation(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"needle-a.txt", "needle-b.txt", "needle-c.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("plain text\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	out, err := newFileSearchTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"query":      "needle",
		"limit":      2,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("output not OK: %#v", out)
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("result=%T, want map", out.Result)
	}
	if got, _ := result["matches_count"].(int); got != 2 {
		t.Fatalf("matches_count=%#v, want 2", result["matches_count"])
	}
	if got, _ := result["truncated"].(bool); !got {
		t.Fatalf("truncated=%#v, want true", result["truncated"])
	}
}

func TestLocalReadFilePaginatesWithPageToken(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte("abcdefghijkl"), 0o600); err != nil {
		t.Fatalf("write long: %v", err)
	}

	first, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "long.txt",
		"max_bytes":  4,
	}))
	if err != nil {
		t.Fatalf("Execute first page: %v", err)
	}
	firstResult := first.Result.(map[string]any)
	token, _ := firstResult["next_page_token"].(string)
	if token == "" || firstResult["content"] != "abcd" {
		t.Fatalf("first result=%#v, want next page token", firstResult)
	}
	second, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "long.txt",
		"max_bytes":  4,
		"page_token": token,
	}))
	if err != nil {
		t.Fatalf("Execute second page: %v", err)
	}
	secondResult := second.Result.(map[string]any)
	if secondResult["content"] != "efgh" || secondResult["offset"] == 0 {
		t.Fatalf("second result=%#v, want offset page", secondResult)
	}
}

func TestFileSearchRejectsSymlinkEscapeWhenSearchingSubpath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked-outside")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	out, err := newFileSearchTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "linked-outside",
		"query":      "needle",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("output OK for symlink escape: %#v", out)
	}
}

func TestCleanLocalMachineRelRejectsAbsoluteAndWindowsQualifiedPaths(t *testing.T) {
	for _, value := range []string{
		"/etc/passwd",
		`\Windows\System32`,
		`C:\Windows\System32`,
		`c:Windows\System32`,
		`\\server\share\report.pdf`,
	} {
		t.Run(value, func(t *testing.T) {
			if got, err := cleanLocalMachineRel(value, false); err == nil {
				t.Fatalf("cleanLocalMachineRel(%q)=%q, want rejection", value, got)
			}
		})
	}
	if got, err := cleanLocalMachineRel(`reports\quarterly\forecast.pdf`, false); err != nil || got != "reports/quarterly/forecast.pdf" {
		t.Fatalf("relative Windows path got=%q err=%v", got, err)
	}
}

func TestLocalReadFileRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra privileges on Windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle\n"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "linked-secret.txt")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	out, err := newLocalReadFileTool().Execute(context.Background(), ExecContext{}, mustJSONForLocalMachineToolTest(map[string]any{
		"local_root": root,
		"path":       "linked-secret.txt",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("output OK for symlink escape: %#v", out)
	}
}

func outputResultStringForLocalMachineTest(out Output) string {
	if s, ok := out.Result.(string); ok {
		return s
	}
	b, _ := json.Marshal(out.Result)
	return string(b)
}

func localMachineEntryPathsForTest(entries []map[string]any) []string {
	paths := []string{}
	for _, entry := range entries {
		if path, ok := entry["path"].(string); ok {
			paths = append(paths, path)
		}
	}
	return paths
}

func mustJSONForLocalMachineToolTest(value any) json.RawMessage {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return json.RawMessage(b)
}

func writeLocalMachinePDFTestFile(t *testing.T, path string, text string) {
	t.Helper()
	if strings.ContainsAny(text, "()\\") {
		t.Fatalf("test PDF literal must not contain characters requiring PDF escaping: %q", text)
	}
	stream := `BT
/F1 36 Tf
72 680 Td
(` + text + `) Tj
ET
`
	content := fmt.Sprintf(`%%PDF-1.4
1 0 obj
<< /Type /Catalog /Pages 2 0 R >>
endobj
2 0 obj
<< /Type /Pages /Kids [3 0 R] /Count 1 >>
endobj
3 0 obj
<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>
endobj
4 0 obj
<< /Length %d >>
stream
%s
endstream
endobj
5 0 obj
<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>
endobj
trailer
<< /Root 1 0 R >>
%%EOF
`, len(stream), stream)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write PDF fixture: %v", err)
	}
}

func localMachineTestXLSX(t *testing.T) []byte {
	t.Helper()
	entries := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="xml" ContentType="application/xml"/>
  <Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
  <Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
  <Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>
</Types>`,
		"_rels/.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`,
		"xl/workbook.xml": `<?xml version="1.0" encoding="UTF-8"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
  <sheets>
    <sheet name="Revenue" sheetId="1" r:id="rId1"/>
  </sheets>
</workbook>`,
		"xl/_rels/workbook.xml.rels": `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>
</Relationships>`,
		"xl/worksheets/sheet1.xml": `<?xml version="1.0" encoding="UTF-8"?>
<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
  <sheetData>
    <row r="1">
      <c r="A1" t="s"><v>0</v></c>
      <c r="B1" t="s"><v>1</v></c>
    </row>
    <row r="2">
      <c r="A2" t="s"><v>2</v></c>
      <c r="B2"><v>10</v></c>
    </row>
    <row r="3">
      <c r="A3" t="s"><v>3</v></c>
      <c r="B3"><v>8</v></c>
    </row>
  </sheetData>
</worksheet>`,
		"xl/sharedStrings.xml": `<?xml version="1.0" encoding="UTF-8"?>
<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="4" uniqueCount="4">
  <si><t>region</t></si>
  <si><t>q1</t></si>
  <si><t>North</t></si>
  <si><t>South</t></si>
</sst>`,
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create xlsx entry %s: %v", name, err)
		}
		if _, err := w.Write([]byte(entries[name])); err != nil {
			t.Fatalf("write xlsx entry %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close xlsx: %v", err)
	}
	return buf.Bytes()
}
