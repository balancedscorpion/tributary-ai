package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/threadtransfer"
	"ai-analyst-local/internal/chat/types"
)

func TestHandleExportThreadReturnsBundleAttachment(t *testing.T) {
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
	title := "Shared Planning Thread"
	if err := st.UpdateThreadTitleSummary(ctx, thread.ID, &title, nil); err != nil {
		t.Fatalf("UpdateThreadTitleSummary: %v", err)
	}

	content := "Visible message"
	if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role:    types.RoleUser,
		Content: &content,
	}); err != nil {
		t.Fatalf("AppendMessage(visible): %v", err)
	}
	hidden := "hidden note"
	if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role:     types.RoleAssistant,
		Content:  &hidden,
		UIHidden: true,
	}); err != nil {
		t.Fatalf("AppendMessage(hidden): %v", err)
	}
	insertThreadAttachmentArtifact(t, ctx, st, cfgDir, thread.ID, "share.txt", "hello export\n")

	h := &Handler{
		store:  st,
		cfgDir: cfgDir,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID+"/export", nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleExportThread(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Header().Get("Content-Type")); got != threadtransfer.BundleMimeType {
		t.Fatalf("Content-Type=%q, want %q", got, threadtransfer.BundleMimeType)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, ".ai-thread.json") {
		t.Fatalf("Content-Disposition=%q, want .ai-thread.json attachment", got)
	}

	bundle, err := threadtransfer.ParseBundle(rec.Body.Bytes())
	if err != nil {
		t.Fatalf("ParseBundle: %v", err)
	}
	if bundle.Thread.Title != title {
		t.Fatalf("bundle.Thread.Title=%q, want %q", bundle.Thread.Title, title)
	}
	if bundle.Summary.MessageCount != 1 || len(bundle.Messages) != 1 {
		t.Fatalf("bundle messages=%d summary=%d, want 1 visible message", len(bundle.Messages), bundle.Summary.MessageCount)
	}
	if bundle.Summary.ArtifactCount != 1 || len(bundle.Artifacts) != 1 {
		t.Fatalf("bundle artifacts=%d summary=%d, want 1 visible artifact", len(bundle.Artifacts), bundle.Summary.ArtifactCount)
	}
	if strings.Contains(rec.Body.String(), hidden) {
		t.Fatalf("export leaked hidden message content: %s", rec.Body.String())
	}
}

func TestHandleImportThreadCreatesNewThreadFromBundle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	sourceThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread(source): %v", err)
	}
	title := "Import Me"
	if err := st.UpdateThreadTitleSummary(ctx, sourceThread.ID, &title, nil); err != nil {
		t.Fatalf("UpdateThreadTitleSummary: %v", err)
	}
	content := "Thread body"
	if _, err := st.AppendMessage(ctx, sourceThread.ID, nil, store.Message{
		Role:    types.RoleUser,
		Content: &content,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	insertThreadAttachmentArtifact(t, ctx, st, cfgDir, sourceThread.ID, "context.txt", "portable\nbundle\n")

	h := &Handler{
		store:  st,
		cfgDir: cfgDir,
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/api/threads/"+sourceThread.ID+"/export", nil)
	exportReq.SetPathValue("threadId", sourceThread.ID)
	exportRec := httptest.NewRecorder()
	h.handleExportThread(exportRec, exportReq)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", exportRec.Code, exportRec.Body.String())
	}

	importReq := httptest.NewRequest(http.MethodPost, "/api/threads/import", bytes.NewReader(exportRec.Body.Bytes()))
	importReq.Header.Set("Content-Type", threadtransfer.BundleMimeType)
	importRec := httptest.NewRecorder()
	h.handleImportThread(importRec, importReq)
	if importRec.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", importRec.Code, importRec.Body.String())
	}

	var payload struct {
		Thread struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"thread"`
		MessageCount  int `json:"message_count"`
		ArtifactCount int `json:"artifact_count"`
	}
	if err := json.Unmarshal(importRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal(import response): %v", err)
	}
	if payload.Thread.ID == "" || payload.Thread.ID == sourceThread.ID {
		t.Fatalf("imported thread id=%q, want new thread id", payload.Thread.ID)
	}
	if payload.Thread.Title != title {
		t.Fatalf("imported title=%q, want %q", payload.Thread.Title, title)
	}
	if payload.MessageCount != 1 || payload.ArtifactCount != 1 {
		t.Fatalf("counts=%+v, want 1 message and 1 artifact", payload)
	}

	importedMessages, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: payload.Thread.ID,
		Limit:    10,
		OrderBy:  "seq_asc",
	})
	if err != nil {
		t.Fatalf("ListMessages(imported): %v", err)
	}
	if len(importedMessages) != 1 || strings.TrimSpace(stringPtrValue(importedMessages[0].Content)) != content {
		t.Fatalf("importedMessages=%+v, want one visible imported message", importedMessages)
	}

	importedArtifacts, err := st.ListArtifacts(ctx, payload.Thread.ID, 10)
	if err != nil {
		t.Fatalf("ListArtifacts(imported): %v", err)
	}
	if len(importedArtifacts) != 1 || importedArtifacts[0].Title != "context.txt" {
		t.Fatalf("importedArtifacts=%+v, want imported attachment", importedArtifacts)
	}
}

func stringPtrValue(in *string) string {
	if in == nil {
		return ""
	}
	return *in
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
