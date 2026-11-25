package httpapi

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
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

const (
	maxThreadAttachmentBytes               int64 = 128 << 20
	threadAttachmentPreviewBytes                 = 32 << 10
	threadExtractPreviewBytes                    = 12 << 10
	threadAttachmentExternalPreviewTimeout       = 8 * time.Second
)

func (h *Handler) handlePostThreadAttachment(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing threadId"})
		return
	}
	if _, err := h.store.GetThread(r.Context(), threadID); err != nil {
		h.writeErr(w, err)
		return
	}

	fileName, err := fileingest.SafeFileName(firstNonEmptyString(
		r.URL.Query().Get("name"),
		r.URL.Query().Get("filename"),
		r.Header.Get("x-file-name"),
	))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	if r.ContentLength > maxThreadAttachmentBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
			"error":     "file too large",
			"max_bytes": maxThreadAttachmentBytes,
		})
		return
	}

	artifactID := types.NewUUID()
	ext := strings.TrimSpace(path.Ext(fileName))
	relPath := filepath.ToSlash(filepath.Join("thread_uploads", strings.TrimSpace(threadID), artifactID+ext))
	targetPath := filepath.Join(h.cfgDir, filepath.FromSlash(relPath))
	ingested, err := fileingest.Ingest(fileingest.IngestRequest{
		Scope:        fileingest.ScopeThread,
		Source:       fileingest.Source(types.ChatUploadAttachmentSource),
		FileName:     fileName,
		DeclaredMIME: r.Header.Get("Content-Type"),
		Reader:       r.Body,
		TargetPath:   targetPath,
		MaxBytes:     maxThreadAttachmentBytes,
		PreviewBytes: threadAttachmentPreviewBytes,
	})
	if err != nil {
		if errors.Is(err, fileingest.ErrTooLarge) {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{
				"error":     "file too large",
				"max_bytes": maxThreadAttachmentBytes,
			})
			return
		}
		h.writeErr(w, err)
		return
	}

	evaluation := ingested.Evaluation
	metadata := evaluation.Metadata(fileingest.Source(types.ChatUploadAttachmentSource))
	attachThreadFilePresentationMetadata(metadata, fileName, relPath)
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
		h.writeErr(w, err)
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
		Capabilities:     evaluation.CapabilitiesMap(),
		Source:           types.ChatUploadAttachmentSource,
	}
	attachment.PreviewText = strings.TrimSpace(firstStringMapValue(created.Metadata, "previewText", "preview_text"))
	attachment.ContentNote = strings.TrimSpace(firstStringMapValue(created.Metadata, "contentNote", "content_note"))
	attachment.SizeBytes = int64Ptr(ingested.ByteSize)
	attachment.IsText = boolValuePtr(evaluation.IsText)
	attachment.PreviewAvailable = boolValuePtr(evaluation.PreviewAvailable || boolMapValue(created.Metadata["previewAvailable"]))
	attachment.PreviewTruncated = boolValuePtr(evaluation.PreviewTruncated || boolMapValue(created.Metadata["previewTruncated"]))
	if caps := boolMapFromAny(created.Metadata["capabilities"]); len(caps) > 0 {
		attachment.Capabilities = caps
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"artifact":   fileAttachmentArtifactDTO(created),
		"attachment": attachment,
	})
}

func attachThreadFilePresentationMetadata(metadata map[string]any, fileName string, relPath string) {
	if metadata == nil {
		return
	}
	meta, ok := tools.ToolMetadataByName("read_attachment")
	if !ok {
		return
	}
	presentation := meta.Presentation
	resultMetadata := map[string]any{
		"integration":       meta.ToolSetID,
		"product":           presentation.Family,
		"family":            presentation.Family,
		"kind":              presentation.ResultKind,
		"native_viewer":     presentation.NativeViewer,
		"renderer_strategy": presentation.RendererStrategy,
		"renderer_key":      presentation.RendererKey,
		"file_capability":   presentation.FileCapability,
		"tool_presentation": presentation,
		"source_tool_name":  types.ThreadFileAttachmentToolName,
		"file_name":         strings.TrimSpace(fileName),
		"content_path":      strings.TrimSpace(relPath),
	}
	metadata["result_metadata"] = resultMetadata
	metadata["tool_presentation"] = presentation
}

func (h *Handler) hydrateExtractedAttachmentPreview(metadata map[string]any, extraction map[string]any) {
	if strings.TrimSpace(firstStringMapValue(metadata, "previewText", "preview_text")) != "" {
		return
	}
	textRel := strings.TrimSpace(firstStringMapValue(extraction, "textPath", "text_path", "markdownPath", "markdown_path"))
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

func extractFileAttachments(v any) ([]types.FileAttachment, error) {
	arr, ok := v.([]any)
	if !ok {
		return nil, nil
	}
	out := make([]types.FileAttachment, 0, len(arr))
	for _, item := range arr {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		artifactID := strings.TrimSpace(firstStringMapValue(obj, "artifactId", "artifact_id"))
		if artifactID == "" {
			continue
		}
		fileName := strings.TrimSpace(firstStringMapValue(obj, "fileName", "file_name"))
		mimeType := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "mimeType", "mime_type")))
		detectedMimeType := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "detectedMimeType", "detected_mime_type")))
		normalizedMIME := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "normalizedMimeType", "normalized_mime_type")))
		declaredMIME := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "declaredMimeType", "declared_mime_type")))
		extensionMIME := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "extensionMimeType", "extension_mime_type")))
		sniffedMIME := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "sniffedMimeType", "sniffed_mime_type")))
		fileKind := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "fileKind", "file_kind")))
		sha256Hex := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "sha256")))
		previewText := strings.TrimSpace(firstStringMapValue(obj, "previewText", "preview_text"))
		contentNote := strings.TrimSpace(firstStringMapValue(obj, "contentNote", "content_note"))
		source := strings.ToLower(strings.TrimSpace(firstStringMapValue(obj, "source")))
		capabilities := extractBoolMapValue(obj, "capabilities")
		sizeBytes, sizeOK := extractOptionalInt64MapValue(obj, "sizeBytes", "size_bytes")
		isText, isTextOK := extractOptionalBoolMapValue(obj, "isText", "is_text")
		previewAvailable, previewAvailableOK := extractOptionalBoolMapValue(obj, "previewAvailable", "preview_available")
		previewTruncated, previewTruncatedOK := extractOptionalBoolMapValue(obj, "previewTruncated", "preview_truncated")

		attachment := types.FileAttachment{
			ArtifactID:       artifactID,
			FileName:         fileName,
			MimeType:         mimeType,
			DetectedMimeType: detectedMimeType,
			NormalizedMIME:   normalizedMIME,
			DeclaredMIME:     declaredMIME,
			ExtensionMIME:    extensionMIME,
			SniffedMIME:      sniffedMIME,
			FileKind:         fileKind,
			SHA256:           sha256Hex,
			PreviewText:      previewText,
			ContentNote:      contentNote,
			Source:           source,
			Capabilities:     capabilities,
		}
		if sizeOK {
			attachment.SizeBytes = &sizeBytes
		}
		if isTextOK {
			attachment.IsText = &isText
		}
		if previewAvailableOK {
			attachment.PreviewAvailable = &previewAvailable
		}
		if previewTruncatedOK {
			attachment.PreviewTruncated = &previewTruncated
		}
		out = append(out, attachment)
	}
	return types.CloneFileAttachments(out), nil
}

func fileAttachmentArtifactDTO(a store.Artifact) map[string]any {
	content := strings.TrimSpace(firstStringMapValue(a.Metadata, "previewText", "preview_text"))
	if content == "" {
		content = strings.TrimSpace(firstStringMapValue(a.Metadata, "contentNote", "content_note"))
	}
	dto := map[string]any{
		"id":       a.ID,
		"type":     a.Type,
		"title":    a.Title,
		"content":  content,
		"metadata": a.Metadata,
	}
	if a.Language != nil && strings.TrimSpace(*a.Language) != "" {
		dto["language"] = strings.TrimSpace(*a.Language)
	}
	return dto
}

func extractOptionalBoolMapValue(m map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case bool:
			return v, true
		}
	}
	return false, false
}

func boolMapValue(raw any) bool {
	value, ok := raw.(bool)
	return ok && value
}

func extractBoolMapValue(m map[string]any, keys ...string) map[string]bool {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		obj, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		out := make(map[string]bool, len(obj))
		for rawKey, rawValue := range obj {
			cleanKey := strings.TrimSpace(rawKey)
			if cleanKey == "" {
				continue
			}
			if value, ok := rawValue.(bool); ok {
				out[cleanKey] = value
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

func boolValuePtr(v bool) *bool {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
