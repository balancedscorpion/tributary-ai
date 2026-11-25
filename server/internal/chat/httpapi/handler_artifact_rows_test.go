package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
)

func TestHandleGetArtifactHydratesSandboxedHTMLMetadata(t *testing.T) {
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

	html := `<button id="b">Open</button><script>b.onclick = () => b.textContent = "ok";</script>`
	language := "html"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-html-1",
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "Interactive HTML",
		Language:    &language,
		Storage:     "mysql",
		ContentText: &html,
		ByteSize:    int64(len(html)),
		Metadata: map[string]any{
			"render_mode":             "sandboxed_html",
			"artifact_kind":           "interactive_data_html",
			"sandbox":                 "allow-scripts",
			"network":                 "blocked",
			"data_source_artifact_id": "source-data",
			"data_source_row_count":   2,
			"data_source_columns":     []any{"region", "revenue"},
			"data_access":             map[string]any{"mode": "parent_bridge", "max_page_size": 100},
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest("GET", "/api/artifacts/artifact-html-1", nil)
	req.SetPathValue("artifactId", "artifact-html-1")
	rec := httptest.NewRecorder()

	h.handleGetArtifact(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	artifact, ok := payload["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("artifact payload missing: %#v", payload["artifact"])
	}
	if got := strings.TrimSpace(artifact["content"].(string)); got != html {
		t.Fatalf("artifact content=%q, want hydrated html", got)
	}
	if artifact["inline_preview"] != true {
		t.Fatalf("artifact inline_preview=%#v, want true", artifact["inline_preview"])
	}
	if got := strings.TrimSpace(artifact["content_storage"].(string)); got != "mysql" {
		t.Fatalf("content_storage=%q, want mysql", got)
	}
	if got := int(artifact["byte_size"].(float64)); got != len(html) {
		t.Fatalf("byte_size=%d, want %d", got, len(html))
	}
	metadata, ok := artifact["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("metadata missing: %#v", artifact["metadata"])
	}
	if metadata["inline_preview"] != true {
		t.Fatalf("metadata.inline_preview=%#v, want true", metadata["inline_preview"])
	}
	if metadata["data_source_artifact_id"] != "source-data" {
		t.Fatalf("metadata data_source_artifact_id=%#v, want source-data", metadata["data_source_artifact_id"])
	}
	if got := int(metadata["data_source_row_count"].(float64)); got != 2 {
		t.Fatalf("metadata data_source_row_count=%d, want 2", got)
	}
	dataAccess, ok := metadata["data_access"].(map[string]any)
	if !ok {
		t.Fatalf("metadata data_access missing: %#v", metadata["data_access"])
	}
	if got := strings.TrimSpace(dataAccess["mode"].(string)); got != "parent_bridge" {
		t.Fatalf("metadata data_access.mode=%q, want parent_bridge", got)
	}
}

func TestHandleGetArtifactRowsPagingAndColumns(t *testing.T) {
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

	rows := make([]map[string]any, 0, 10)
	for i := 1; i <= 10; i++ {
		rows = append(rows, map[string]any{
			"a": i,
			"b": i * 10,
			"c": "row",
		})
	}
	contentBytes, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(contentBytes)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-rows-1",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest("GET", "/api/artifacts/artifact-rows-1/rows?offset=2&limit=3&columns=a,c", nil)
	req.SetPathValue("artifactId", "artifact-rows-1")
	rec := httptest.NewRecorder()

	h.handleGetArtifactRows(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	if got := int(payload["row_count_total"].(float64)); got != 10 {
		t.Fatalf("row_count_total=%d, want 10", got)
	}
	if got := int(payload["rows_returned"].(float64)); got != 3 {
		t.Fatalf("rows_returned=%d, want 3", got)
	}
	if payload["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", payload["has_more"])
	}

	rowsOut, ok := payload["rows"].([]any)
	if !ok || len(rowsOut) != 3 {
		t.Fatalf("rows=%#v, want length 3", payload["rows"])
	}
	first, ok := rowsOut[0].(map[string]any)
	if !ok {
		t.Fatalf("rows[0]=%#v, want object", rowsOut[0])
	}
	if _, ok := first["b"]; ok {
		t.Fatalf("unexpected projected column b in row: %#v", first)
	}
	if got := int(first["a"].(float64)); got != 3 {
		t.Fatalf("first projected row a=%d, want 3", got)
	}
}

func TestHandleGetArtifactRowsHasMoreAfterByteClamp(t *testing.T) {
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

	rows := make([]map[string]any, 0, 5)
	payload := strings.Repeat("x", 600000)
	for i := 0; i < 5; i++ {
		rows = append(rows, map[string]any{"index": i, "payload": payload})
	}
	contentBytes, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(contentBytes)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-rows-clamped",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Large Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest("GET", "/api/artifacts/artifact-rows-clamped/rows?limit=5", nil)
	req.SetPathValue("artifactId", "artifact-rows-clamped")
	rec := httptest.NewRecorder()

	h.handleGetArtifactRows(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status=%d, want 200 body=%s", rec.Code, rec.Body.String())
	}

	var payloadOut map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payloadOut); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	rowsReturned := int(payloadOut["rows_returned"].(float64))
	if rowsReturned <= 0 || rowsReturned >= 5 {
		t.Fatalf("rows_returned=%d, want byte-clamped partial page", rowsReturned)
	}
	if payloadOut["has_more"] != true {
		t.Fatalf("has_more=%#v, want true after byte clamp", payloadOut["has_more"])
	}
	if got := int(payloadOut["next_offset"].(float64)); got != rowsReturned {
		t.Fatalf("next_offset=%d, want %d", got, rowsReturned)
	}
}

func TestHandleGetArtifactRowsRejectsInvalidOffset(t *testing.T) {
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
	content := `[{"a":1}]`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-rows-2",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	h := &Handler{store: st, cfgDir: cfgDir}
	req := httptest.NewRequest("GET", "/api/artifacts/artifact-rows-2/rows?offset=-1", nil)
	req.SetPathValue("artifactId", "artifact-rows-2")
	rec := httptest.NewRecorder()

	h.handleGetArtifactRows(rec, req)
	if rec.Code != 400 {
		t.Fatalf("status=%d, want 400 body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "offset") {
		t.Fatalf("expected offset validation error, body=%s", rec.Body.String())
	}

	reqBad := httptest.NewRequest("GET", "/api/artifacts/artifact-rows-2/rows?offset=3oops", nil)
	reqBad.SetPathValue("artifactId", "artifact-rows-2")
	recBad := httptest.NewRecorder()

	h.handleGetArtifactRows(recBad, reqBad)
	if recBad.Code != 400 {
		t.Fatalf("status=%d for malformed offset, want 400 body=%s", recBad.Code, recBad.Body.String())
	}
	if !strings.Contains(recBad.Body.String(), "offset") {
		t.Fatalf("expected offset validation error for malformed offset, body=%s", recBad.Body.String())
	}
}
