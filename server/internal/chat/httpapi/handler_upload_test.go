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

const (
	exampleUploadFileName = "Example Large Upload.csv"
	// Size observed in the original failing example.
	exampleUploadSizeBytes int64 = 33337178
)

func TestHandlePutProjectFile_Regression_ExampleLargeUpload(t *testing.T) {
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

	// Create a project through the same handler path used by the UI flow.
	createReq := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"name":"aaaa"}`))
	createRec := httptest.NewRecorder()
	h.handleCreateProject(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create project status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Project struct {
			ID string `json:"id"`
		} `json:"project"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	projectID := strings.TrimSpace(created.Project.ID)
	if projectID == "" {
		t.Fatalf("missing project id in create response: %s", createRec.Body.String())
	}

	examplePath, fileSize := prepareExampleUploadFile(t)
	if fileSize <= 8<<20 {
		t.Fatalf("fixture must exceed old 8 MiB backend limit, got %d", fileSize)
	}

	f, err := os.Open(examplePath)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	putURL := "/api/projects/" + projectID + "/files?path=" + url.QueryEscape(exampleUploadFileName)
	putReq := httptest.NewRequest(http.MethodPut, putURL, f)
	putReq.SetPathValue("projectId", projectID)
	putReq.ContentLength = fileSize
	putRec := httptest.NewRecorder()
	h.handlePutProjectFile(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put file status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	finalizeReq := httptest.NewRequest(http.MethodPost, "/api/projects/"+projectID+"/finalize", nil)
	finalizeReq.SetPathValue("projectId", projectID)
	finalizeRec := httptest.NewRecorder()
	h.handleFinalizeProject(finalizeRec, finalizeReq)
	if finalizeRec.Code != http.StatusOK {
		t.Fatalf("finalize status = %d, body = %s", finalizeRec.Code, finalizeRec.Body.String())
	}

	var finalized struct {
		Project struct {
			FileCount  int   `json:"file_count"`
			TotalBytes int64 `json:"total_bytes"`
		} `json:"project"`
	}
	if err := json.Unmarshal(finalizeRec.Body.Bytes(), &finalized); err != nil {
		t.Fatalf("decode finalize response: %v", err)
	}
	if finalized.Project.FileCount != 1 {
		t.Fatalf("file_count = %d, want 1", finalized.Project.FileCount)
	}
	if finalized.Project.TotalBytes != fileSize {
		t.Fatalf("total_bytes = %d, want %d", finalized.Project.TotalBytes, fileSize)
	}

	storedPath := filepath.Join(cfgDir, "projects", projectID, "files", exampleUploadFileName)
	storedInfo, err := os.Stat(storedPath)
	if err != nil {
		t.Fatalf("stat stored file: %v", err)
	}
	if storedInfo.Size() != fileSize {
		t.Fatalf("stored file size = %d, want %d", storedInfo.Size(), fileSize)
	}

	// Metadata persisted through UpsertProjectFile should match finalize totals.
	project, err := st.GetProject(ctx, projectID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if project.FileCount != 1 || project.TotalBytes != fileSize {
		t.Fatalf("project summary mismatch: file_count=%d total_bytes=%d", project.FileCount, project.TotalBytes)
	}
}

func TestHandlePutProjectFile_ReturnsEvaluationMetadata(t *testing.T) {
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

	project, err := st.CreateProject(ctx, "Files", "projects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	body := "%PDF-1.7\n% project upload\n"
	putURL := "/api/projects/" + project.ID + "/files?path=" + url.QueryEscape("report.bin")
	putReq := httptest.NewRequest(http.MethodPut, putURL, strings.NewReader(body))
	putReq.SetPathValue("projectId", project.ID)
	putReq.Header.Set("Content-Type", "text/plain")
	putRec := httptest.NewRecorder()

	h.handlePutProjectFile(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put file status = %d, body = %s", putRec.Code, putRec.Body.String())
	}

	var payload struct {
		OK         bool           `json:"ok"`
		Evaluation map[string]any `json:"evaluation"`
	}
	if err := json.Unmarshal(putRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.OK {
		t.Fatalf("ok=false, body=%s", putRec.Body.String())
	}
	if got, _ := payload.Evaluation["fileKind"].(string); got != "pdf" {
		t.Fatalf("fileKind=%q, want pdf; evaluation=%#v", got, payload.Evaluation)
	}
	if got, _ := payload.Evaluation["normalizedMimeType"].(string); got != "application/pdf" {
		t.Fatalf("normalizedMimeType=%q, want application/pdf; evaluation=%#v", got, payload.Evaluation)
	}
	if got, _ := payload.Evaluation["declaredMimeType"].(string); got != "text/plain" {
		t.Fatalf("declaredMimeType=%q, want text/plain; evaluation=%#v", got, payload.Evaluation)
	}
	if got, _ := payload.Evaluation["sha256"].(string); strings.TrimSpace(got) == "" {
		t.Fatalf("sha256 missing; evaluation=%#v", payload.Evaluation)
	}

	storedPath := filepath.Join(cfgDir, "projects", project.ID, "files", "report.bin")
	stored, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", storedPath, err)
	}
	if string(stored) != body {
		t.Fatalf("stored file body = %q, want exact upload", string(stored))
	}

	if _, err := h.refreshProjectFileIndex(ctx, project.ID); err != nil {
		t.Fatalf("refreshProjectFileIndex: %v", err)
	}
	sidecarPath := filepath.Join(cfgDir, "projects", project.ID, "file_evaluations.json")
	sidecarRaw, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", sidecarPath, err)
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
	if len(sidecar.Files) != 1 || sidecar.Files[0].Path != "report.bin" {
		t.Fatalf("sidecar files=%#v, want report.bin", sidecar.Files)
	}
	if got, _ := sidecar.Files[0].Evaluation["fileKind"].(string); got != "pdf" {
		t.Fatalf("sidecar fileKind=%q, want pdf; evaluation=%#v", got, sidecar.Files[0].Evaluation)
	}
}

func TestHandlePutProjectFile_ExtractsPPTXBundle(t *testing.T) {
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

	project, err := st.CreateProject(ctx, "Decks", "projects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	putURL := "/api/projects/" + project.ID + "/files?path=" + url.QueryEscape("decks/pipeline.pptx")
	putReq := httptest.NewRequest(http.MethodPut, putURL, bytes.NewReader(makeTestPPTX(t)))
	putReq.SetPathValue("projectId", project.ID)
	putReq.Header.Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
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
	if got, _ := payload.Evaluation["fileKind"].(string); got != "presentation" {
		t.Fatalf("fileKind=%q, want presentation; evaluation=%#v", got, payload.Evaluation)
	}
	extraction, ok := payload.Evaluation["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("missing extraction metadata: %#v", payload.Evaluation)
	}
	if got, _ := extraction["status"].(string); got != "complete" {
		t.Fatalf("extraction status=%q, want complete; extraction=%#v", got, extraction)
	}
	manifestPath, _ := extraction["manifestPath"].(string)
	if manifestPath == "" {
		t.Fatalf("missing manifestPath: %#v", extraction)
	}
	if _, err := os.Stat(filepath.Join(cfgDir, filepath.FromSlash(manifestPath))); err != nil {
		t.Fatalf("expected project extraction manifest %s: %v", manifestPath, err)
	}

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
	if len(sidecar.Files) != 1 || sidecar.Files[0].Path != "decks/pipeline.pptx" {
		t.Fatalf("sidecar files=%#v, want decks/pipeline.pptx", sidecar.Files)
	}
	sidecarExtraction, ok := sidecar.Files[0].Evaluation["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("sidecar missing extraction: %#v", sidecar.Files[0].Evaluation)
	}
	if got, _ := sidecarExtraction["pipeline"].(string); got != "pptx" {
		t.Fatalf("sidecar extraction pipeline=%q, want pptx; extraction=%#v", got, sidecarExtraction)
	}
}

func prepareExampleUploadFile(t *testing.T) (string, int64) {
	t.Helper()

	dst := filepath.Join(t.TempDir(), exampleUploadFileName)
	if err := generateSizedCSV(dst, exampleUploadSizeBytes); err != nil {
		t.Fatalf("generate upload fixture: %v", err)
	}
	fi, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat generated fixture: %v", err)
	}
	return dst, fi.Size()
}

func generateSizedCSV(path string, size int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	line := []byte("date,region,price\n2025-04-01,United Kingdom,286000\n")
	var written int64
	for written < size {
		chunk := line
		remaining := size - written
		if int64(len(chunk)) > remaining {
			chunk = chunk[:remaining]
		}
		n, err := f.Write(chunk)
		if err != nil {
			return err
		}
		written += int64(n)
	}
	return f.Sync()
}
