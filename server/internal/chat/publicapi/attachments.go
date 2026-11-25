package publicapi

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileingest"
	"ai-analyst-local/internal/chat/filepipeline"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	maxThreadAttachmentBytes               int64 = 128 << 20
	threadAttachmentPreviewBytes                 = 32 << 10
	threadExtractPreviewBytes                    = 12 << 10
	threadAttachmentExternalPreviewTimeout       = 8 * time.Second
)

func (h *Handler) handlePostAttachment(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeError(w, invalidRequest("missing threadId"))
		return
	}
	thread, err := h.store.GetThread(r.Context(), threadID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if !authInfoFromContext(r.Context()).canAccessThread(thread) {
		writeError(w, permissionDenied("API key is not allowed to access this thread"))
		return
	}
	fileName, err := fileingest.SafeFileName(firstNonEmptyString(
		r.URL.Query().Get("name"),
		r.URL.Query().Get("filename"),
		r.Header.Get("x-file-name"),
	))
	if err != nil {
		writeError(w, invalidRequest(err.Error()))
		return
	}
	if r.ContentLength > maxThreadAttachmentBytes {
		writeError(w, apiError(http.StatusRequestEntityTooLarge, "payload_too_large", "file too large"))
		return
	}
	artifactID := types.NewUUID()
	ext := strings.TrimSpace(path.Ext(fileName))
	relPath := filepath.ToSlash(filepath.Join("thread_uploads", strings.TrimSpace(threadID), artifactID+ext))
	targetPath := filepath.Join(h.cfgDir, filepath.FromSlash(relPath))
	ingested, err := fileingest.Ingest(fileingest.IngestRequest{
		Scope:        fileingest.ScopeThread,
		Source:       fileingest.SourceExternalAPI,
		FileName:     fileName,
		DeclaredMIME: r.Header.Get("Content-Type"),
		Reader:       r.Body,
		TargetPath:   targetPath,
		MaxBytes:     maxThreadAttachmentBytes,
		PreviewBytes: threadAttachmentPreviewBytes,
	})
	if err != nil {
		if errors.Is(err, fileingest.ErrTooLarge) {
			writeError(w, apiError(http.StatusRequestEntityTooLarge, "payload_too_large", "file too large"))
			return
		}
		h.writeStoreErr(w, err)
		return
	}

	evaluation := ingested.Evaluation
	metadata := evaluation.Metadata(fileingest.SourceExternalAPI)
	extractionRelPath := filepath.ToSlash(filepath.Join("thread_uploads", strings.TrimSpace(threadID), artifactID+".extract"))
	if extraction := filepipeline.ExtractFile(r.Context(), filepipeline.ExtractionRequest{
		SourcePath:             targetPath,
		OutputDir:              filepath.Join(h.cfgDir, filepath.FromSlash(extractionRelPath)),
		OutputRelPath:          extractionRelPath,
		FileName:               fileName,
		Evaluation:             evaluation,
		IncludeRawXML:          true,
		EnableExternalTools:    false,
		EnableRendering:        true,
		EnableOCR:              true,
		ExternalCommandTimeout: threadAttachmentExternalPreviewTimeout,
	}); extraction != nil {
		metadata["extraction"] = extraction
		h.hydrateExtractedAttachmentPreview(metadata, extraction)
	}
	toolName := types.ThreadFileAttachmentToolName
	okVal := true
	artifact := store.Artifact{
		ID:          artifactID,
		ThreadID:    threadID,
		Type:        "file",
		Title:       fileName,
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "file",
		ContentPath: &relPath,
		ByteSize:    ingested.ByteSize,
		Metadata:    metadata,
	}
	if evaluation.Language != "" {
		artifact.Language = &evaluation.Language
	}
	created, err := h.store.InsertArtifact(r.Context(), artifact)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	attachment := types.FileAttachment{
		ArtifactID:       created.ID,
		FileName:         fileName,
		MimeType:         evaluation.NormalizedMIME,
		DetectedMimeType: firstNonEmptyString(evaluation.SniffedMIME, evaluation.NormalizedMIME),
		NormalizedMIME:   evaluation.NormalizedMIME,
		DeclaredMIME:     evaluation.DeclaredMIME,
		ExtensionMIME:    evaluation.ExtensionMIME,
		SniffedMIME:      evaluation.SniffedMIME,
		FileKind:         string(evaluation.Kind),
		SHA256:           evaluation.SHA256Hex,
		PreviewText:      strings.TrimSpace(stringMapValue(created.Metadata, "previewText", "preview_text")),
		ContentNote:      strings.TrimSpace(stringMapValue(created.Metadata, "contentNote", "content_note")),
		Source:           string(fileingest.SourceExternalAPI),
		SizeBytes:        int64Ptr(ingested.ByteSize),
		IsText:           boolPtr(evaluation.IsText),
		PreviewAvailable: boolPtr(evaluation.PreviewAvailable || boolMapValue(created.Metadata["previewAvailable"])),
		PreviewTruncated: boolPtr(evaluation.PreviewTruncated || boolMapValue(created.Metadata["previewTruncated"])),
		Capabilities:     evaluation.CapabilitiesMap(),
	}
	if caps := boolMapFromAny(created.Metadata["capabilities"]); len(caps) > 0 {
		attachment.Capabilities = caps
	}
	content := strings.TrimSpace(stringMapValue(created.Metadata, "previewText", "preview_text"))
	if content == "" {
		content = strings.TrimSpace(stringMapValue(created.Metadata, "contentNote", "content_note"))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":     "attachment.create_response",
		"artifact":   publicArtifactDTO(created, content),
		"attachment": attachment,
	})
}

func (h *Handler) hydrateExtractedAttachmentPreview(metadata map[string]any, extraction map[string]any) {
	if strings.TrimSpace(stringMapValue(metadata, "previewText", "preview_text")) != "" {
		return
	}
	textRel := strings.TrimSpace(stringMapValue(extraction, "textPath", "text_path", "markdownPath", "markdown_path"))
	if textRel == "" {
		return
	}
	textRel = filepath.ToSlash(strings.Trim(textRel, "/"))
	fullPath, ok := safeConfigRelativePath(h.cfgDir, textRel)
	if !ok {
		return
	}
	raw, err := os.ReadFile(fullPath)
	if err != nil || len(raw) == 0 {
		return
	}
	truncated := false
	if len(raw) > threadExtractPreviewBytes {
		raw = raw[:threadExtractPreviewBytes]
		truncated = true
	}
	preview := strings.TrimSpace(string(bytes.ToValidUTF8(raw, []byte("?"))))
	if preview == "" {
		return
	}
	metadata["previewText"] = preview
	metadata["previewAvailable"] = true
	metadata["previewTruncated"] = truncated || boolMapValue(metadata["previewTruncated"])
	delete(metadata, "contentNote")
	if caps, ok := metadata["capabilities"].(map[string]bool); ok {
		caps["canPreviewText"] = true
	}
}

func safeConfigRelativePath(cfgDir string, rel string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(strings.TrimSpace(rel)))
	if clean == "." || clean == "" || filepath.IsAbs(clean) || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || clean == ".." {
		return "", false
	}
	cfgAbs, err := filepath.Abs(cfgDir)
	if err != nil {
		return "", false
	}
	fullAbs, err := filepath.Abs(filepath.Join(cfgAbs, clean))
	if err != nil {
		return "", false
	}
	back, err := filepath.Rel(cfgAbs, fullAbs)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", false
	}
	return fullAbs, true
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func stringMapValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

func boolMapValue(raw any) bool {
	value, ok := raw.(bool)
	return ok && value
}

func boolMapFromAny(raw any) map[string]bool {
	switch v := raw.(type) {
	case map[string]bool:
		out := make(map[string]bool, len(v))
		for key, value := range v {
			out[key] = value
		}
		return out
	case map[string]any:
		out := make(map[string]bool, len(v))
		for key, value := range v {
			if b, ok := value.(bool); ok {
				out[key] = b
			}
		}
		return out
	default:
		return nil
	}
}

func int64Ptr(v int64) *int64 { return &v }

func boolPtr(v bool) *bool { return &v }
