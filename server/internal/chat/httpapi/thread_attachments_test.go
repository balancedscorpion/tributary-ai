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
	"ai-analyst-local/internal/chat/types"
)

func TestExtractFileAttachments_ParsesArtifactID(t *testing.T) {
	t.Parallel()

	raw := []any{
		map[string]any{
			"artifactId":       "file-artifact-1",
			"fileName":         "notes.txt",
			"mimeType":         "text/plain",
			"detectedMimeType": "text/plain",
			"sizeBytes":        float64(11),
			"isText":           true,
			"previewAvailable": true,
			"previewTruncated": false,
			"previewText":      "hello preview",
			"contentNote":      "fallback note",
			"source":           "chat_upload",
		},
	}

	attachments, err := extractFileAttachments(raw)
	if err != nil {
		t.Fatalf("extractFileAttachments: %v", err)
	}
	if len(attachments) != 1 {
		t.Fatalf("len(attachments)=%d, want 1", len(attachments))
	}

	attachment := attachments[0]
	if attachment.ArtifactID != "file-artifact-1" {
		t.Fatalf("ArtifactID=%q, want file-artifact-1", attachment.ArtifactID)
	}
	if attachment.FileName != "notes.txt" {
		t.Fatalf("FileName=%q, want notes.txt", attachment.FileName)
	}
	if attachment.MimeType != "text/plain" {
		t.Fatalf("MimeType=%q, want text/plain", attachment.MimeType)
	}
	if attachment.DetectedMimeType != "text/plain" {
		t.Fatalf("DetectedMimeType=%q, want text/plain", attachment.DetectedMimeType)
	}
	if attachment.SizeBytes == nil || *attachment.SizeBytes != 11 {
		t.Fatalf("SizeBytes=%v, want 11", attachment.SizeBytes)
	}
	if attachment.IsText == nil || !*attachment.IsText {
		t.Fatalf("IsText=%v, want true", attachment.IsText)
	}
	if attachment.PreviewAvailable == nil || !*attachment.PreviewAvailable {
		t.Fatalf("PreviewAvailable=%v, want true", attachment.PreviewAvailable)
	}
	if attachment.PreviewTruncated == nil || *attachment.PreviewTruncated {
		t.Fatalf("PreviewTruncated=%v, want false", attachment.PreviewTruncated)
	}
	if attachment.PreviewText != "hello preview" {
		t.Fatalf("PreviewText=%q, want hello preview", attachment.PreviewText)
	}
	if attachment.ContentNote != "fallback note" {
		t.Fatalf("ContentNote=%q, want fallback note", attachment.ContentNote)
	}
	if attachment.Source != "chat_upload" {
		t.Fatalf("Source=%q, want chat_upload", attachment.Source)
	}
}

func TestHandlePostThreadAttachment_StoresThreadScopedArtifact(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/api/threads/"+thread.ID+"/attachments?name=notes.txt", strings.NewReader("hello\nworld\n"))
	req.SetPathValue("threadId", thread.ID)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()

	h.handlePostThreadAttachment(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Artifact struct {
			ID       string         `json:"id"`
			Type     string         `json:"type"`
			Title    string         `json:"title"`
			Content  string         `json:"content"`
			Metadata map[string]any `json:"metadata"`
		} `json:"artifact"`
		Attachment types.FileAttachment `json:"attachment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Attachment.ArtifactID == "" {
		t.Fatalf("missing attachment artifact id: %s", rec.Body.String())
	}
	if payload.Artifact.Type != "file" {
		t.Fatalf("artifact type = %q, want file", payload.Artifact.Type)
	}
	if payload.Artifact.Title != "notes.txt" {
		t.Fatalf("artifact title = %q, want notes.txt", payload.Artifact.Title)
	}
	if !strings.Contains(payload.Artifact.Content, "hello") {
		t.Fatalf("artifact preview content = %q, want hello", payload.Artifact.Content)
	}
	if !strings.Contains(payload.Attachment.PreviewText, "hello") {
		t.Fatalf("attachment previewText = %q, want hello", payload.Attachment.PreviewText)
	}
	if payload.Attachment.FileKind != "text" {
		t.Fatalf("attachment fileKind = %q, want text", payload.Attachment.FileKind)
	}
	if payload.Attachment.NormalizedMIME != "text/plain" {
		t.Fatalf("attachment normalized MIME = %q, want text/plain", payload.Attachment.NormalizedMIME)
	}
	if payload.Attachment.SHA256 == "" {
		t.Fatalf("attachment sha256 missing")
	}
	if payload.Attachment.Capabilities == nil || !payload.Attachment.Capabilities["canPreviewText"] {
		t.Fatalf("attachment capabilities = %#v, want canPreviewText", payload.Attachment.Capabilities)
	}
	if payload.Attachment.ContentNote != "" {
		t.Fatalf("attachment contentNote = %q, want empty for text preview", payload.Attachment.ContentNote)
	}
	assertThreadAttachmentPresentationMetadata(t, payload.Artifact.Metadata)

	artifact, err := st.GetArtifact(ctx, payload.Attachment.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.ThreadID != thread.ID {
		t.Fatalf("artifact.ThreadID = %q, want %q", artifact.ThreadID, thread.ID)
	}
	if artifact.Type != "file" {
		t.Fatalf("artifact.Type = %q, want file", artifact.Type)
	}
	if artifact.ToolName == nil || *artifact.ToolName != types.ThreadFileAttachmentToolName {
		t.Fatalf("artifact.ToolName = %v, want %q", artifact.ToolName, types.ThreadFileAttachmentToolName)
	}
	if got := strings.TrimSpace(firstStringMapValue(artifact.Metadata, "source")); got != types.ChatUploadAttachmentSource {
		t.Fatalf("artifact source = %q, want %q", got, types.ChatUploadAttachmentSource)
	}
	assertThreadAttachmentPresentationMetadata(t, artifact.Metadata)
	if got := strings.TrimSpace(firstStringMapValue(artifact.Metadata, "previewText")); !strings.Contains(got, "hello") {
		t.Fatalf("artifact previewText = %q, want hello", got)
	}
	if got := strings.TrimSpace(firstStringMapValue(artifact.Metadata, "fileKind")); got != "text" {
		t.Fatalf("artifact fileKind = %q, want text", got)
	}
	if got := strings.TrimSpace(firstStringMapValue(artifact.Metadata, "normalizedMimeType")); got != "text/plain" {
		t.Fatalf("artifact normalizedMimeType = %q, want text/plain", got)
	}
	if artifact.ContentPath == nil || strings.TrimSpace(*artifact.ContentPath) == "" {
		t.Fatalf("artifact.ContentPath missing")
	}

	fullPath := filepath.Join(cfgDir, filepath.FromSlash(strings.TrimSpace(*artifact.ContentPath)))
	body, err := os.ReadFile(fullPath)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", fullPath, err)
	}
	if string(body) != "hello\nworld\n" {
		t.Fatalf("stored file body = %q, want exact uploaded content", string(body))
	}
}

func TestHandlePostThreadAttachment_ExtractsPPTXBundle(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/api/threads/"+thread.ID+"/attachments?name=pipeline.pptx", bytes.NewReader(makeTestPPTX(t)))
	req.SetPathValue("threadId", thread.ID)
	req.Header.Set("Content-Type", "application/vnd.openxmlformats-officedocument.presentationml.presentation")
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
		Attachment types.FileAttachment `json:"attachment"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Attachment.FileKind != "presentation" {
		t.Fatalf("fileKind=%q, want presentation", payload.Attachment.FileKind)
	}
	extraction, ok := payload.Artifact.Metadata["extraction"].(map[string]any)
	if !ok {
		t.Fatalf("missing extraction metadata: %#v", payload.Artifact.Metadata)
	}
	if got, _ := extraction["pipeline"].(string); got != "pptx" {
		t.Fatalf("pipeline=%q, want pptx; extraction=%#v", got, extraction)
	}
	if got, _ := extraction["status"].(string); got != "complete" {
		t.Fatalf("status=%q, want complete; extraction=%#v", got, extraction)
	}
	if got, _ := extraction["slideCount"].(float64); got != 1 {
		t.Fatalf("slideCount=%v, want 1; extraction=%#v", extraction["slideCount"], extraction)
	}
	assertThreadAttachmentPresentationMetadata(t, payload.Artifact.Metadata)
	manifestPath, _ := extraction["manifestPath"].(string)
	markdownPath, _ := extraction["markdownPath"].(string)
	if manifestPath == "" || markdownPath == "" {
		t.Fatalf("missing extraction paths: %#v", extraction)
	}
	for _, rel := range []string{manifestPath, markdownPath} {
		if _, err := os.Stat(filepath.Join(cfgDir, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("expected extraction output %s: %v", rel, err)
		}
	}
	markdown, err := os.ReadFile(filepath.Join(cfgDir, filepath.FromSlash(markdownPath)))
	if err != nil {
		t.Fatalf("ReadFile markdown: %v", err)
	}
	if !strings.Contains(string(markdown), "Pipeline Review") || !strings.Contains(string(markdown), "Speaker note from test fixture") {
		t.Fatalf("markdown missing extracted content:\n%s", string(markdown))
	}
}

func assertThreadAttachmentPresentationMetadata(t *testing.T, metadata map[string]any) {
	t.Helper()
	if metadata == nil {
		t.Fatalf("metadata is nil")
	}
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object: %#v", metadata["result_metadata"], metadata)
	}
	if resultMetadata["integration"] != "attachments" ||
		resultMetadata["family"] != "attachments" ||
		resultMetadata["kind"] != "file" ||
		resultMetadata["native_viewer"] != "file" ||
		resultMetadata["renderer_strategy"] != "artifact_type" ||
		resultMetadata["renderer_key"] != "file" ||
		resultMetadata["file_capability"] != "attachment_read" {
		t.Fatalf("result_metadata does not describe native attachment file rendering: %#v", resultMetadata)
	}
	if resultMetadata["source_tool_name"] != types.ThreadFileAttachmentToolName {
		t.Fatalf("result_metadata.source_tool_name=%#v, want %q", resultMetadata["source_tool_name"], types.ThreadFileAttachmentToolName)
	}
	if _, ok := resultMetadata["tool_presentation"]; !ok {
		t.Fatalf("result_metadata.tool_presentation missing: %#v", resultMetadata)
	}
	presentation, ok := metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("tool_presentation=%T, want object: %#v", metadata["tool_presentation"], metadata)
	}
	if presentation["family"] != "attachments" ||
		presentation["result_kind"] != "file" ||
		presentation["native_viewer"] != "file" ||
		presentation["file_capability"] != "attachment_read" {
		t.Fatalf("tool_presentation does not describe attachment file rendering: %#v", presentation)
	}
}

func TestHandleGetThread_IncludesReferencedFileAttachmentArtifacts(t *testing.T) {
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

	toolName := types.ThreadFileAttachmentToolName
	okVal := true
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ID:       "artifact-file-1",
		ThreadID: thread.ID,
		Type:     "file",
		Title:    "notes.txt",
		ToolName: &toolName,
		OK:       &okVal,
		Storage:  "file",
		Metadata: map[string]any{
			"source":      types.ChatUploadAttachmentSource,
			"previewText": "hello from preview",
			"mimeType":    "text/plain",
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	content := "read this"
	if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role:                  types.RoleUser,
		Content:               &content,
		ReferencedArtifactIDs: []string{artifact.ID},
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	h := &Handler{
		store:  st,
		cfgDir: cfgDir,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/threads/"+thread.ID, nil)
	req.SetPathValue("threadId", thread.ID)
	rec := httptest.NewRecorder()

	h.handleGetThread(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var payload struct {
		Artifacts []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Content string `json:"content"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Artifacts) != 1 {
		t.Fatalf("len(artifacts)=%d, want 1: %s", len(payload.Artifacts), rec.Body.String())
	}
	if payload.Artifacts[0].ID != artifact.ID {
		t.Fatalf("artifact id=%q, want %q", payload.Artifacts[0].ID, artifact.ID)
	}
	if payload.Artifacts[0].Type != "file" {
		t.Fatalf("artifact type=%q, want file", payload.Artifacts[0].Type)
	}
	if payload.Artifacts[0].Content != "hello from preview" {
		t.Fatalf("artifact content=%q, want preview", payload.Artifacts[0].Content)
	}
}
