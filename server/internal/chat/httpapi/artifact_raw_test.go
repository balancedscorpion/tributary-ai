package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

func TestHandleGetArtifactRaw_ServesFileAttachmentBytes(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	const body = "hello\nworld\n"
	up := httptest.NewRequest(http.MethodPost, "/api/threads/"+thread.ID+"/attachments?name=notes.txt", strings.NewReader(body))
	up.SetPathValue("threadId", thread.ID)
	up.Header.Set("Content-Type", "text/plain")
	upRec := httptest.NewRecorder()
	h.handlePostThreadAttachment(upRec, up)
	if upRec.Code != http.StatusOK {
		t.Fatalf("upload status = %d, body = %s", upRec.Code, upRec.Body.String())
	}
	var uploaded struct {
		Attachment types.FileAttachment `json:"attachment"`
	}
	if err := json.Unmarshal(upRec.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	artifactID := uploaded.Attachment.ArtifactID
	if artifactID == "" {
		t.Fatalf("missing artifact id: %s", upRec.Body.String())
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID+"/raw", nil)
	req.SetPathValue("artifactId", artifactID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "private, max-age=3600" {
		t.Fatalf("Cache-Control = %q, want private, max-age=3600", got)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="notes.txt"`) {
		t.Fatalf("Content-Disposition = %q, want inline notes.txt", cd)
	}
}

func TestHandleGetArtifactRaw_ServesLocalOpenFileSnapshot(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()
	localRoot := t.TempDir()
	const body = "%PDF-1.4\n% local open snapshot\n"
	if err := os.MkdirAll(filepath.Join(localRoot, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir reports: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localRoot, "reports", "forecast.pdf"), []byte(body), 0o600); err != nil {
		t.Fatalf("write local pdf: %v", err)
	}

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	reg := chattools.NewRegistry(&chattools.RegistryOptions{Store: st, CfgDir: cfgDir})
	tool, ok := reg.Get(chattools.LocalOpenFileToolName)
	if !ok {
		t.Fatal("local_open_file tool missing from registry")
	}
	args, _ := json.Marshal(map[string]any{
		"local_root": localRoot,
		"path":       "reports/forecast.pdf",
	})
	out, err := tool.Execute(ctx, chattools.ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		RunID:    "run-local-open-raw-test",
		CfgDir:   cfgDir,
	}, args)
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
	if artifactID == "" {
		t.Fatalf("missing attachment artifact id: %#v", result)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifactID+"/raw", nil)
	req.SetPathValue("artifactId", artifactID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="forecast.pdf"`) {
		t.Fatalf("Content-Disposition = %q, want inline forecast.pdf", cd)
	}
}

func TestHandleGetArtifactRaw_ConnectorFileUsesNestedResultMetadataMIME(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	const body = "%PDF-1.4 connector bytes"
	rel := filepath.ToSlash(filepath.Join("connector_files", thread.ID, "quarterly-close-scan"))
	full := filepath.Join(cfgDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	toolName := "slack_download_file"
	contentPath := rel
	art, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "connector-file-1",
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "Quarterly Close Scan",
		ToolName:    &toolName,
		ContentPath: &contentPath,
		Metadata: map[string]any{
			"source": "external_tool",
			"result_metadata": map[string]any{
				"mimetype": "application/pdf",
				"filetype": "pdf",
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+art.ID+"/raw", nil)
	req.SetPathValue("artifactId", art.ID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != body {
		t.Fatalf("body = %q, want %q", rec.Body.String(), body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/pdf" {
		t.Fatalf("Content-Type = %q, want application/pdf", ct)
	}

	// Backend-normalized top-level keys keep priority over nested
	// connector metadata.
	if got := artifactRawMIME(map[string]any{
		"normalizedMimeType": "text/csv",
		"result_metadata":    map[string]any{"mimetype": "application/pdf"},
	}); got != "text/csv" {
		t.Fatalf("artifactRawMIME top-level priority = %q, want text/csv", got)
	}
}

func TestHandleGetArtifactExtracted_ServesWhitelistedRenderedSlide(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	rel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "artifact-deck.extract", "rendered", "slide-001.png"))
	full := filepath.Join(cfgDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const slideBody = "rendered slide bytes"
	if err := os.WriteFile(full, []byte(slideBody), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	toolName := types.ThreadFileAttachmentToolName
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-deck",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "deck.pptx",
		ToolName: &toolName,
		Metadata: map[string]any{
			"source":   types.ChatUploadAttachmentSource,
			"mimeType": "application/vnd.openxmlformats-officedocument.presentationml.presentation",
			"extraction": map[string]any{
				"kind":             "presentation",
				"pipeline":         "pptx",
				"renderedSlides":   []any{rel},
				"previewImagePath": rel,
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/extracted?path="+url.QueryEscape(rel), nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactExtracted(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != slideBody {
		t.Fatalf("body = %q, want %q", rec.Body.String(), slideBody)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/extracted?path="+url.QueryEscape(filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "artifact-deck.extract", "deck.md"))), nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec = httptest.NewRecorder()
	h.handleGetArtifactExtracted(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unlisted extracted path status = %d, want 404", rec.Code)
	}
}

func TestHandleGetArtifactExtracted_ServesWhitelistedSpreadsheetPreview(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	rootRel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "artifact-sheet.extract"))
	previewRel := filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "preview.md"))
	previewJSONRel := filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "preview.json"))
	workbookRel := filepath.ToSlash(filepath.Join(rootRel, "workbook.json"))
	const previewBody = "| A | B |\n| --- | --- |\n| XLSX_SENTINEL | 42 |\n"
	const previewJSONBody = `{"version":1,"sheet":{"id":"sheet1","name":"Sheet1"},"rows":[]}` + "\n"
	const workbookBody = `{"version":1,"fileName":"model.xlsx","sheets":[]}` + "\n"
	for rel, body := range map[string]string{
		previewRel:     previewBody,
		previewJSONRel: previewJSONBody,
		workbookRel:    workbookBody,
	} {
		full := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	toolName := types.ThreadFileAttachmentToolName
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-sheet",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "model.xlsx",
		ToolName: &toolName,
		Metadata: map[string]any{
			"source":   types.ChatUploadAttachmentSource,
			"mimeType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"extraction": map[string]any{
				"kind":     "spreadsheet",
				"pipeline": "xlsx",
				"rootPath": rootRel,
				"outputs": map[string]any{
					"workbookPath":        "workbook.json",
					"sheetOutputs":        []any{"worksheets/sheet1/preview.md"},
					"sheetPreviewOutputs": []any{"worksheets/sheet1/preview.json"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/extracted?path="+url.QueryEscape(previewRel), nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactExtracted(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != previewBody {
		t.Fatalf("body = %q, want %q", rec.Body.String(), previewBody)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/extracted?path="+url.QueryEscape(previewJSONRel), nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec = httptest.NewRecorder()
	h.handleGetArtifactExtracted(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview json status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != previewJSONBody {
		t.Fatalf("preview json body = %q, want %q", rec.Body.String(), previewJSONBody)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/extracted?path="+url.QueryEscape(workbookRel), nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec = httptest.NewRecorder()
	h.handleGetArtifactExtracted(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("workbook json status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Body.String() != workbookBody {
		t.Fatalf("workbook json body = %q, want %q", rec.Body.String(), workbookBody)
	}

	unlistedRel := filepath.ToSlash(filepath.Join(rootRel, "workbook.md"))
	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/extracted?path="+url.QueryEscape(unlistedRel), nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec = httptest.NewRecorder()
	h.handleGetArtifactExtracted(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unlisted extracted path status = %d, want 404", rec.Code)
	}
}

func TestHandleGetSpreadsheetCells_PagesKnownCellSidecar(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	rootRel := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "artifact-sheet.extract"))
	workbookRel := filepath.ToSlash(filepath.Join(rootRel, "workbook.json"))
	cellsRel := filepath.ToSlash(filepath.Join(rootRel, "worksheets", "sheet1", "cells-0001.jsonl"))
	workbookBody := `{"version":1,"fileName":"model.xlsx","sheets":[{"id":"sheet1","name":"Revenue","index":1,"rowCount":5,"columnCount":3,"cellsPath":"worksheets/sheet1/cells-0001.jsonl"}]}` + "\n"
	cellsBody := strings.Join([]string{
		`{"sheet_id":"sheet1","address":"A1","row":1,"col":1,"kind":"string","display":"Region"}`,
		`{"sheet_id":"sheet1","address":"A2","row":2,"col":1,"kind":"string","display":"North"}`,
		`{"sheet_id":"sheet1","address":"B2","row":2,"col":2,"kind":"number","display":"10","number_format":"0.00","style":{"bold":true}}`,
		`{"sheet_id":"sheet1","address":"A3","row":3,"col":1,"kind":"string","display":"South"}`,
		`{"sheet_id":"sheet1","address":"C3","row":3,"col":3,"kind":"number","display":"12"}`,
		`{"sheet_id":"sheet1","address":"A4","row":4,"col":1,"kind":"string","display":"West"}`,
	}, "\n") + "\n"
	for rel, body := range map[string]string{
		workbookRel: workbookBody,
		cellsRel:    cellsBody,
	} {
		full := filepath.Join(cfgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}

	toolName := types.ThreadFileAttachmentToolName
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-sheet",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "model.xlsx",
		ToolName: &toolName,
		Metadata: map[string]any{
			"source":   types.ChatUploadAttachmentSource,
			"mimeType": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			"extraction": map[string]any{
				"kind":         "spreadsheet",
				"pipeline":     "xlsx",
				"rootPath":     rootRel,
				"workbookPath": workbookRel,
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/spreadsheet/cells?sheet_id=sheet1&row_start=2&row_count=2&col_start=1&col_count=2", nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec := httptest.NewRecorder()
	h.handleGetSpreadsheetCells(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		ArtifactID       string `json:"artifact_id"`
		SheetID          string `json:"sheet_id"`
		RowsReturned     int    `json:"rows_returned"`
		NextRowStart     int    `json:"next_row_start"`
		HasMoreRows      bool   `json:"has_more_rows"`
		HasMoreColumns   bool   `json:"has_more_columns"`
		TotalRowCount    int    `json:"total_row_count"`
		TotalColumnCount int    `json:"total_column_count"`
		Columns          []struct {
			Index int    `json:"index"`
			Name  string `json:"name"`
		} `json:"columns"`
		Rows []struct {
			Index int `json:"index"`
			Cells []struct {
				Address      string         `json:"address"`
				Row          int            `json:"row"`
				Col          int            `json:"col"`
				Display      string         `json:"display"`
				NumberFormat string         `json:"numberFormat"`
				Style        map[string]any `json:"style"`
			} `json:"cells"`
		} `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ArtifactID != artifact.ID || payload.SheetID != "sheet1" {
		t.Fatalf("ids mismatch: %#v", payload)
	}
	if payload.RowsReturned != 2 || payload.NextRowStart != 4 || !payload.HasMoreRows || !payload.HasMoreColumns {
		t.Fatalf("paging mismatch: %#v", payload)
	}
	if payload.TotalRowCount != 5 || payload.TotalColumnCount != 3 {
		t.Fatalf("totals mismatch: %#v", payload)
	}
	if len(payload.Columns) != 2 || payload.Columns[0].Name != "A" || payload.Columns[1].Name != "B" {
		t.Fatalf("columns mismatch: %#v", payload.Columns)
	}
	if len(payload.Rows) != 2 || payload.Rows[0].Index != 2 || payload.Rows[1].Index != 3 {
		t.Fatalf("rows mismatch: %#v", payload.Rows)
	}
	if len(payload.Rows[0].Cells) != 2 || payload.Rows[0].Cells[0].Display != "North" || payload.Rows[0].Cells[1].NumberFormat != "0.00" {
		t.Fatalf("row 2 cells mismatch: %#v", payload.Rows[0].Cells)
	}
	if bold, _ := payload.Rows[0].Cells[1].Style["bold"].(bool); !bold {
		t.Fatalf("style mismatch: %#v", payload.Rows[0].Cells[1].Style)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/artifacts/"+artifact.ID+"/spreadsheet/cells?sheet_id=missing", nil)
	req.SetPathValue("artifactId", artifact.ID)
	rec = httptest.NewRecorder()
	h.handleGetSpreadsheetCells(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing sheet status = %d, want 404", rec.Code)
	}
}

func TestHandleGetArtifactRaw_ServesImageAttachmentBytes(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	// 1x1 transparent PNG.
	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	dataURI := "data:image/png;base64," + pngBase64
	contentText := dataURI
	toolName := "image_attachment"
	okVal := true
	art, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "image-artifact-1",
		ThreadID:    thread.ID,
		Type:        "image",
		Title:       "shot.png",
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &contentText,
		Metadata:    map[string]any{"mimeType": "image/png"},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+art.ID+"/raw", nil)
	req.SetPathValue("artifactId", art.ID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Fatalf("decoded body does not start with PNG magic bytes")
	}
}

func TestHandleGetArtifactRaw_ServesGeneratedImageBytes(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	const pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="
	contentText := "data:image/png;base64," + pngBase64
	toolName := "generate_image"
	okVal := true
	art, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "generated-image-artifact-1",
		ThreadID:    thread.ID,
		Type:        "image",
		Title:       "terrarium-city.png",
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &contentText,
		Metadata: map[string]any{
			"source":    "generated_image",
			"generated": true,
			"mimeType":  "image/png",
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+art.ID+"/raw", nil)
	req.SetPathValue("artifactId", art.ID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Fatalf("Content-Type = %q, want image/png", ct)
	}
	if !strings.HasPrefix(rec.Body.String(), "\x89PNG") {
		t.Fatalf("decoded body does not start with PNG magic bytes")
	}

	// Normal artifact hydration carries metadata but deliberately omits the
	// large data URI; image consumers use the authenticated /raw endpoint.
	jsonReq := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+art.ID, nil)
	jsonReq.SetPathValue("artifactId", art.ID)
	jsonRec := httptest.NewRecorder()
	h.handleGetArtifact(jsonRec, jsonReq)
	if jsonRec.Code != http.StatusOK {
		t.Fatalf("artifact status = %d, body = %s", jsonRec.Code, jsonRec.Body.String())
	}
	var payload struct {
		Artifact map[string]any `json:"artifact"`
	}
	if err := json.Unmarshal(jsonRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode artifact response: %v", err)
	}
	if payload.Artifact["content"] != "" {
		t.Fatalf("generated image JSON content = %#v, want omitted bytes", payload.Artifact["content"])
	}
}

func TestHandleGetArtifactRaw_NonAttachmentReturns404(t *testing.T) {
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
	h := &Handler{store: st, cfgDir: cfgDir}

	toolName := "run_sql"
	okVal := true
	content := "col_a,col_b\n1,2\n"
	art, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "table-artifact-1",
		ThreadID:    thread.ID,
		Type:        "table",
		Title:       "results",
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &content,
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/"+art.ID+"/raw", nil)
	req.SetPathValue("artifactId", art.ID)
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleGetArtifactRaw_MissingArtifactReturns404(t *testing.T) {
	t.Parallel()

	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	h := &Handler{store: st, cfgDir: cfgDir}

	req := httptest.NewRequest(http.MethodGet, "/api/artifacts/does-not-exist/raw", nil)
	req.SetPathValue("artifactId", "does-not-exist")
	rec := httptest.NewRecorder()
	h.handleGetArtifactRaw(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body = %s", rec.Code, rec.Body.String())
	}
}
