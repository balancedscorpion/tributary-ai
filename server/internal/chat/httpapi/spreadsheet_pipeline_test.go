package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
)

const spreadsheetXLSXMIME = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

func TestHandlePostThreadAttachment_ExtractsXLSXSpreadsheetMetadata(t *testing.T) {
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

	h := &Handler{
		store:  st,
		cfgDir: cfgDir,
	}

	req := httptest.NewRequest(http.MethodPost, "/api/threads/"+thread.ID+"/attachments?name=revenue.xlsx", bytes.NewReader(makeTestXLSX(t)))
	req.SetPathValue("threadId", thread.ID)
	req.Header.Set("Content-Type", spreadsheetXLSXMIME)
	rec := httptest.NewRecorder()

	h.handlePostThreadAttachment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Artifact struct {
			ID       string         `json:"id"`
			Metadata map[string]any `json:"metadata"`
		} `json:"artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := strings.TrimSpace(firstStringMapValue(payload.Artifact.Metadata, "fileKind")); got != "spreadsheet" {
		t.Fatalf("fileKind=%q, want spreadsheet; metadata=%#v", got, payload.Artifact.Metadata)
	}
	extraction, ok := payload.Artifact.Metadata["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("missing extraction metadata: %#v", payload.Artifact.Metadata)
	}
	assertSpreadsheetExtractionMetadata(t, cfgDir, extraction, "xlsx")
}

func TestHandlePutProjectFile_ExtractsXLSXSpreadsheetMetadata(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	h := &Handler{
		store:        st,
		projects:     ps,
		cfgDir:       cfgDir,
		maxBodyBytes: 1 << 20,
	}

	project, err := st.CreateProject(ctx, "Spreadsheets", "projects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	putURL := "/api/projects/" + project.ID + "/files?path=" + url.QueryEscape("sheets/revenue.xlsx")
	putReq := httptest.NewRequest(http.MethodPut, putURL, bytes.NewReader(makeTestXLSX(t)))
	putReq.SetPathValue("projectId", project.ID)
	putReq.Header.Set("Content-Type", spreadsheetXLSXMIME)
	putRec := httptest.NewRecorder()

	h.handlePutProjectFile(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put file status = %d, body = %s", putRec.Code, putRec.Body.String())
	}
	var payload struct {
		Evaluation map[string]any `json:"evaluation"`
	}
	if err := json.Unmarshal(putRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got, _ := payload.Evaluation["fileKind"].(string); got != "spreadsheet" {
		t.Fatalf("fileKind=%q, want spreadsheet; evaluation=%#v", got, payload.Evaluation)
	}
	extraction, ok := payload.Evaluation["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("missing response extraction metadata: %#v", payload.Evaluation)
	}
	assertSpreadsheetExtractionMetadata(t, cfgDir, extraction, "xlsx")

	if _, err := h.refreshProjectFileIndex(ctx, project.ID); err != nil {
		t.Fatalf("refreshProjectFileIndex: %v", err)
	}
	sidecarPath := filepath.Join(cfgDir, "projects", project.ID, "file_evaluations.json")
	sidecarRaw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("ReadFile sidecar: %v", err)
	}
	var sidecar struct {
		Files []struct {
			Path       string         `json:"path"`
			Evaluation map[string]any `json:"evaluation"`
		} `json:"files"`
	}
	if err := json.Unmarshal(sidecarRaw, &sidecar); err != nil {
		t.Fatalf("decode sidecar: %v", err)
	}
	if len(sidecar.Files) != 1 || sidecar.Files[0].Path != "sheets/revenue.xlsx" {
		t.Fatalf("sidecar files=%#v, want sheets/revenue.xlsx", sidecar.Files)
	}
	sidecarExtraction, ok := sidecar.Files[0].Evaluation["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("sidecar missing extraction: %#v", sidecar.Files[0].Evaluation)
	}
	assertSpreadsheetExtractionMetadata(t, cfgDir, sidecarExtraction, "xlsx")
}

func assertSpreadsheetExtractionMetadata(t *testing.T, cfgDir string, extraction map[string]any, wantPipeline string) {
	t.Helper()

	if got, _ := extraction["pipeline"].(string); got != wantPipeline {
		t.Fatalf("pipeline=%q, want %q; extraction=%#v", got, wantPipeline, extraction)
	}
	if got, _ := extraction["status"].(string); got != "complete" {
		t.Fatalf("status=%q, want complete; extraction=%#v", got, extraction)
	}
	if got, _ := extraction["sheetCount"].(float64); got != 1 {
		t.Fatalf("sheetCount=%v, want 1; extraction=%#v", extraction["sheetCount"], extraction)
	}
	if got, _ := extraction["rowCount"].(float64); got != 3 {
		t.Fatalf("rowCount=%v, want 3; extraction=%#v", extraction["rowCount"], extraction)
	}
	if got, _ := extraction["columnCount"].(float64); got != 3 {
		t.Fatalf("columnCount=%v, want 3; extraction=%#v", extraction["columnCount"], extraction)
	}

	manifestPath, _ := extraction["manifestPath"].(string)
	dataPath, _ := extraction["dataPath"].(string)
	markdownPath, _ := extraction["markdownPath"].(string)
	if manifestPath == "" || dataPath == "" || markdownPath == "" {
		t.Fatalf("missing extraction paths: %#v", extraction)
	}
	for _, rel := range []string{manifestPath, dataPath, markdownPath} {
		if _, err := os.Stat(filepath.Join(cfgDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected extraction output %s: %v", rel, err)
		}
	}
}
