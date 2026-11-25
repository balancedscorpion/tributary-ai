package optionc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

const (
	fileAttachmentContextPreviewChars      = 24000
	fileAttachmentContextTotalPreviewChars = 72000
	fileAttachmentNativeFileMaxBytes       = 50 << 20
	fileAttachmentNativeFileTotalBytes     = 50 << 20
	fileAttachmentBedrockNativeMaxBytes    = (9 * 1024 * 1024) / 2
)

type fileAttachmentTurnContextOptions struct {
	NativeInputAvailable      func(types.FileAttachment) bool
	NativeImageInputAvailable func(types.FileAttachment) bool
}

func referencedFileAttachmentArtifactIDs(attachments []types.FileAttachment) []string {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]string, 0, len(attachments))
	for _, attachment := range attachments {
		if artifactID := strings.TrimSpace(attachment.ArtifactID); artifactID != "" {
			out = append(out, artifactID)
		}
	}
	return dedupeStringSlice(out)
}

func fileAttachmentRefsForArtifactIDs(ids []string) []types.FileAttachment {
	ids = dedupeStringSlice(ids)
	if len(ids) == 0 {
		return nil
	}
	out := make([]types.FileAttachment, 0, len(ids))
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			out = append(out, types.FileAttachment{ArtifactID: id})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func fileAttachmentRefsForMessage(role types.Role, ids []string) []types.FileAttachment {
	switch role {
	case types.RoleUser, types.RoleTool:
		return fileAttachmentRefsForArtifactIDs(ids)
	default:
		return nil
	}
}

func fileAttachmentRefsForAttachments(attachments []types.FileAttachment) []types.FileAttachment {
	return fileAttachmentRefsForArtifactIDs(referencedFileAttachmentArtifactIDs(attachments))
}

func (m *Manager) hydrateHistoryFileAttachments(
	ctx context.Context,
	threadID string,
	history []types.ChatMessage,
) []types.ChatMessage {
	if len(history) == 0 {
		return history
	}
	out := append([]types.ChatMessage(nil), history...)
	for i := range out {
		if len(out[i].FileAttachments) == 0 {
			continue
		}
		attachments := m.hydrateReferencedFileAttachments(ctx, threadID, out[i].FileAttachments)
		out[i].FileAttachments = m.hydrateFileAttachmentInlineData(ctx, threadID, attachments)
	}
	return out
}

func (m *Manager) hydrateReferencedFileAttachments(
	ctx context.Context,
	threadID string,
	current []types.FileAttachment,
) []types.FileAttachment {
	base := types.CloneFileAttachments(current)
	if len(base) == 0 {
		return nil
	}

	out := make([]types.FileAttachment, 0, len(base))
	seen := make(map[string]struct{}, len(base))
	for _, attachment := range base {
		artifactID := strings.TrimSpace(attachment.ArtifactID)
		if artifactID == "" {
			continue
		}
		if _, exists := seen[artifactID]; exists {
			continue
		}

		artifact, err := m.store.GetArtifact(ctx, artifactID)
		if err != nil {
			continue
		}
		if !isThreadFileAttachmentArtifact(threadID, artifact) {
			continue
		}

		next := types.FileAttachment{
			ArtifactID: artifactID,
			FileName:   strings.TrimSpace(artifact.Title),
			MimeType:   strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "mimeType", "mime_type"))),
			DetectedMimeType: strings.TrimSpace(strings.ToLower(
				stringMapValue(artifact.Metadata, "detectedMimeType", "detected_mime_type"),
			)),
			NormalizedMIME: strings.TrimSpace(strings.ToLower(
				stringMapValue(artifact.Metadata, "normalizedMimeType", "normalized_mime_type"),
			)),
			DeclaredMIME: strings.TrimSpace(strings.ToLower(
				stringMapValue(artifact.Metadata, "declaredMimeType", "declared_mime_type"),
			)),
			ExtensionMIME: strings.TrimSpace(strings.ToLower(
				stringMapValue(artifact.Metadata, "extensionMimeType", "extension_mime_type"),
			)),
			SniffedMIME: strings.TrimSpace(strings.ToLower(
				stringMapValue(artifact.Metadata, "sniffedMimeType", "sniffed_mime_type"),
			)),
			FileKind:     strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "fileKind", "file_kind"))),
			SHA256:       strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "sha256"))),
			PreviewText:  strings.TrimSpace(stringMapValue(artifact.Metadata, "previewText", "preview_text")),
			ContentNote:  strings.TrimSpace(stringMapValue(artifact.Metadata, "contentNote", "content_note")),
			Source:       strings.TrimSpace(strings.ToLower(stringMapValue(artifact.Metadata, "source"))),
			Capabilities: boolMapMapValue(artifact.Metadata, "capabilities"),
		}
		if next.FileName == "" {
			next.FileName = strings.TrimSpace(attachment.FileName)
		}
		if next.FileName == "" {
			next.FileName = "Uploaded file"
		}
		if next.MimeType == "" {
			next.MimeType = strings.TrimSpace(strings.ToLower(attachment.MimeType))
		}
		if next.DetectedMimeType == "" {
			next.DetectedMimeType = strings.TrimSpace(strings.ToLower(attachment.DetectedMimeType))
		}
		if next.NormalizedMIME == "" {
			next.NormalizedMIME = strings.TrimSpace(strings.ToLower(attachment.NormalizedMIME))
		}
		if next.DeclaredMIME == "" {
			next.DeclaredMIME = strings.TrimSpace(strings.ToLower(attachment.DeclaredMIME))
		}
		if next.ExtensionMIME == "" {
			next.ExtensionMIME = strings.TrimSpace(strings.ToLower(attachment.ExtensionMIME))
		}
		if next.SniffedMIME == "" {
			next.SniffedMIME = strings.TrimSpace(strings.ToLower(attachment.SniffedMIME))
		}
		if next.FileKind == "" {
			next.FileKind = strings.TrimSpace(strings.ToLower(attachment.FileKind))
		}
		if next.SHA256 == "" {
			next.SHA256 = strings.TrimSpace(strings.ToLower(attachment.SHA256))
		}
		if len(next.Capabilities) == 0 && len(attachment.Capabilities) > 0 {
			next.Capabilities = make(map[string]bool, len(attachment.Capabilities))
			for key, value := range attachment.Capabilities {
				key = strings.TrimSpace(key)
				if key != "" {
					next.Capabilities[key] = value
				}
			}
		}
		if modelInput, ok := boolMapValue(artifact.Metadata, "model_file_input"); ok {
			if next.Capabilities == nil {
				next.Capabilities = map[string]bool{}
			}
			next.Capabilities["model_file_input"] = modelInput
		}
		if nativeInput, ok := boolMapValue(artifact.Metadata, "native_file_input"); ok {
			if next.Capabilities == nil {
				next.Capabilities = map[string]bool{}
			}
			next.Capabilities["native_file_input"] = nativeInput
		}
		if next.PreviewText == "" {
			next.PreviewText = strings.TrimSpace(attachment.PreviewText)
		}
		if next.ContentNote == "" {
			next.ContentNote = strings.TrimSpace(attachment.ContentNote)
		}
		if next.Source == "" {
			next.Source = types.ChatUploadAttachmentSource
		}
		if sizeBytes, ok := int64MapValue(artifact.Metadata, "sizeBytes", "size_bytes"); ok && sizeBytes > 0 {
			next.SizeBytes = &sizeBytes
		} else if attachment.SizeBytes != nil && *attachment.SizeBytes > 0 {
			v := *attachment.SizeBytes
			next.SizeBytes = &v
		}
		if isText, ok := boolMapValue(artifact.Metadata, "isText", "is_text"); ok {
			next.IsText = &isText
		} else if attachment.IsText != nil {
			v := *attachment.IsText
			next.IsText = &v
		}
		if previewAvailable, ok := boolMapValue(artifact.Metadata, "previewAvailable", "preview_available"); ok {
			next.PreviewAvailable = &previewAvailable
		} else if attachment.PreviewAvailable != nil {
			v := *attachment.PreviewAvailable
			next.PreviewAvailable = &v
		}
		if previewTruncated, ok := boolMapValue(artifact.Metadata, "previewTruncated", "preview_truncated"); ok {
			next.PreviewTruncated = &previewTruncated
		} else if attachment.PreviewTruncated != nil {
			v := *attachment.PreviewTruncated
			next.PreviewTruncated = &v
		}
		if extraction := fileAttachmentExtractionMap(artifact); extraction != nil {
			preview, truncated, sourcePath := m.readFileAttachmentExtractionPreview(extraction, fileAttachmentContextPreviewChars)
			if strings.TrimSpace(preview) != "" {
				if strings.TrimSpace(next.PreviewText) == "" || !boolPtrValue(next.PreviewAvailable, false) || fileAttachmentPreferExtractionPreview(next) || fileAttachmentExtractionPreviewIsOCR(sourcePath) {
					next.PreviewText = preview
					next.PreviewAvailable = boolPtr(true)
					next.PreviewTruncated = boolPtr(truncated)
				}
				if strings.TrimSpace(sourcePath) != "" && strings.TrimSpace(next.ContentNote) == "" {
					next.ContentNote = "Extracted text preview loaded from " + sourcePath + "."
				}
			}
		}

		out = append(out, next)
		seen[artifactID] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) hydrateFileAttachmentInlineData(
	ctx context.Context,
	threadID string,
	current []types.FileAttachment,
) []types.FileAttachment {
	base := types.CloneFileAttachments(current)
	if len(base) == 0 {
		return nil
	}

	out := make([]types.FileAttachment, 0, len(base))
	var inlineFileBytes int64
	for _, attachment := range base {
		artifactID := strings.TrimSpace(attachment.ArtifactID)
		if artifactID == "" {
			continue
		}
		artifact, err := m.store.GetArtifact(ctx, artifactID)
		if err != nil || !isThreadFileAttachmentArtifact(threadID, artifact) {
			out = append(out, attachment)
			continue
		}
		if artifact.ByteSize > fileAttachmentNativeFileMaxBytes || inlineFileBytes+artifact.ByteSize > fileAttachmentNativeFileTotalBytes {
			out = append(out, attachment)
			continue
		}
		raw := m.readArtifactBytes(artifact)
		if len(raw) == 0 {
			out = append(out, attachment)
			continue
		}
		rawBytes := int64(len(raw))
		if rawBytes > fileAttachmentNativeFileMaxBytes || inlineFileBytes+rawBytes > fileAttachmentNativeFileTotalBytes {
			out = append(out, attachment)
			continue
		}
		attachment.InlineDataBase64 = base64.StdEncoding.EncodeToString(raw)
		inlineFileBytes += rawBytes
		out = append(out, attachment)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *Manager) appendReferencedFileAttachments(
	ctx context.Context,
	threadID string,
	current []types.FileAttachment,
	artifactIDs []string,
) []types.FileAttachment {
	refs := fileAttachmentRefsForArtifactIDs(artifactIDs)
	if len(refs) == 0 {
		return types.CloneFileAttachments(current)
	}
	seen := make(map[string]struct{}, len(current))
	for _, attachment := range current {
		if id := strings.TrimSpace(attachment.ArtifactID); id != "" {
			seen[id] = struct{}{}
		}
	}
	filtered := make([]types.FileAttachment, 0, len(refs))
	for _, ref := range refs {
		id := strings.TrimSpace(ref.ArtifactID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, ref)
	}
	if len(filtered) == 0 {
		return types.CloneFileAttachments(current)
	}
	hydrated := m.hydrateReferencedFileAttachments(ctx, threadID, filtered)
	hydrated = m.hydrateFileAttachmentInlineData(ctx, threadID, hydrated)
	if len(hydrated) == 0 {
		return types.CloneFileAttachments(current)
	}
	out := types.CloneFileAttachments(current)
	out = append(out, hydrated...)
	return out
}

func buildFileAttachmentTurnContext(attachments []types.FileAttachment, options ...fileAttachmentTurnContextOptions) string {
	if len(attachments) == 0 {
		return ""
	}
	nativeInputAvailable := func(types.FileAttachment) bool { return false }
	nativeImageInputAvailable := func(types.FileAttachment) bool { return false }
	if len(options) > 0 && options[0].NativeInputAvailable != nil {
		nativeInputAvailable = options[0].NativeInputAvailable
	}
	if len(options) > 0 && options[0].NativeImageInputAvailable != nil {
		nativeImageInputAvailable = options[0].NativeImageInputAvailable
	}
	lines := []string{
		"Uploaded files for this user turn:",
		"- These files were uploaded in this thread and are thread-scoped to this conversation.",
		"- First identify likely file type from file name and MIME metadata before making claims about the contents.",
		"- When native_file_input=true, the original file bytes are attached directly to the provider as a file input; use that native file input rather than extracted preview text.",
		"- When native_image_input=true, the original image bytes are attached directly to the provider as an image input; use that native image input rather than extracted preview text.",
		"- When native_file_input=true or native_image_input=true, do not use workspace/project files, local attachment readers, or extracted previews as substitutes for the uploaded bytes.",
		"- Do not use Search Threads tools to inspect the current uploaded file contents; they search persisted snippets, not the provider-native uploaded bytes.",
		"- Extracted previews are included only when native file input is unavailable or skipped.",
		"- Only say you inspected the full file contents when native_file_input=true or the full provided preview is enough.",
		"- If native_file_input=false and native_image_input=false for a binary or oversized file, explain that the provider could not receive that file directly and avoid guessing.",
	}
	remainingPreviewChars := fileAttachmentContextTotalPreviewChars
	for i, attachment := range attachments {
		details := []string{
			fmt.Sprintf("[%d] artifact_id=%s", i+1, strings.TrimSpace(attachment.ArtifactID)),
			"file_name=" + strings.TrimSpace(attachment.FileName),
		}
		if mimeType := strings.TrimSpace(attachment.MimeType); mimeType != "" {
			details = append(details, "mime="+mimeType)
		}
		if detected := strings.TrimSpace(attachment.DetectedMimeType); detected != "" && !strings.EqualFold(detected, attachment.MimeType) {
			details = append(details, "detected_mime="+detected)
		}
		if attachment.SizeBytes != nil && *attachment.SizeBytes > 0 {
			details = append(details, fmt.Sprintf("size_bytes=%d", *attachment.SizeBytes))
		}
		if attachment.IsText != nil {
			details = append(details, fmt.Sprintf("is_text=%t", *attachment.IsText))
		}
		if attachment.PreviewAvailable != nil {
			details = append(details, fmt.Sprintf("preview_available=%t", *attachment.PreviewAvailable))
		}
		if attachment.PreviewTruncated != nil {
			details = append(details, fmt.Sprintf("preview_truncated=%t", *attachment.PreviewTruncated))
		}
		nativeInput := nativeInputAvailable(attachment)
		nativeImageInput := nativeImageInputAvailable(attachment)
		details = append(details, fmt.Sprintf("native_file_input=%t", nativeInput))
		if nativeImageInput {
			details = append(details, "native_image_input=true")
		}
		lines = append(lines, "- "+strings.Join(details, ", "))
		if nativeInput {
			lines = append(lines, "  Native file input: original file bytes are attached directly to the provider as a file input; extracted preview omitted from prompt. Use the native file input before any workspace or attachment-inspection tools.")
			if label, supplement := nativeFileAttachmentPreviewSupplement(attachment, nativeInput, nativeImageInput); supplement != "" {
				var appended bool
				lines, appended = appendFileAttachmentPreview(lines, label, supplement, attachment.PreviewTruncated, &remainingPreviewChars)
				if appended {
					lines = append(lines, "    [Supplemental extraction included because provider-native parsing can omit this file type's visible text/cell values.]")
				}
			}
			continue
		}
		if nativeImageInput {
			lines = append(lines, "  Native image input: original image bytes are attached directly to the provider as an image input; extracted preview omitted from prompt. Use the native image input before any workspace or attachment-inspection tools.")
			if label, supplement := nativeFileAttachmentPreviewSupplement(attachment, nativeInput, nativeImageInput); supplement != "" {
				var appended bool
				lines, appended = appendFileAttachmentPreview(lines, label, supplement, attachment.PreviewTruncated, &remainingPreviewChars)
				if appended {
					lines = append(lines, "    [Supplemental OCR included because provider-native image reading can miss embedded text.]")
				}
			}
			continue
		}
		previewText := strings.TrimSpace(attachment.PreviewText)
		if previewText != "" {
			var appended bool
			lines, appended = appendFileAttachmentPreview(lines, "  Provided preview:", previewText, attachment.PreviewTruncated, &remainingPreviewChars)
			if appended {
				continue
			}
		}
		if contentNote := strings.TrimSpace(attachment.ContentNote); contentNote != "" {
			lines = append(lines, "  Note: "+contentNote)
		}
	}
	return strings.Join(lines, "\n")
}

func appendFileAttachmentPreview(lines []string, label string, previewText string, previewTruncated *bool, remainingPreviewChars *int) ([]string, bool) {
	previewText = strings.TrimSpace(previewText)
	if previewText == "" || remainingPreviewChars == nil || *remainingPreviewChars <= 0 {
		return lines, false
	}
	limit := fileAttachmentContextPreviewChars
	if *remainingPreviewChars < limit {
		limit = *remainingPreviewChars
	}
	preview, truncated := truncateStringRunes(previewText, limit)
	if preview == "" {
		return lines, false
	}
	lines = append(lines, label)
	lines = append(lines, indentLines(preview, "    "))
	if truncated || (previewTruncated != nil && *previewTruncated) {
		lines = append(lines, "    [Preview truncated.]")
	}
	*remainingPreviewChars -= len([]rune(preview))
	return lines, true
}

func nativeFileAttachmentPreviewSupplement(attachment types.FileAttachment, nativeInput bool, nativeImageInput bool) (string, string) {
	if nativeImageInput {
		if text := fileAttachmentOCRSupplementText(attachment); text != "" {
			return "  OCR text supplement (native image input is still attached; use this only as fallback for visible text):", text
		}
		return "", ""
	}
	if nativeInput && fileAttachmentIsSpreadsheet(attachment) {
		if text := strings.TrimSpace(attachment.PreviewText); text != "" {
			return "  Extracted spreadsheet preview supplement (native file input is still attached; use this for sheet cell values when provider-native spreadsheet parsing omits them):", text
		}
	}
	if nativeInput && fileAttachmentIsKeynote(attachment) {
		if text := strings.TrimSpace(attachment.PreviewText); text != "" {
			return "  Extracted Keynote preview/OCR supplement (native .key file input is still attached; use this when provider-native Keynote parsing omits slide text):", text
		}
	}
	return "", ""
}

func fileAttachmentOCRSupplementText(attachment types.FileAttachment) string {
	if !fileAttachmentIsImage(attachment) {
		return ""
	}
	text := strings.TrimSpace(attachment.PreviewText)
	if text == "" {
		return ""
	}
	note := strings.ToLower(strings.TrimSpace(attachment.ContentNote))
	lowerText := strings.ToLower(text)
	if !strings.Contains(note, "ocr") && !strings.Contains(lowerText, "## ocr text") {
		return ""
	}
	if extracted := extractOCRTextSection(text); extracted != "" {
		return extracted
	}
	return text
}

func extractOCRTextSection(text string) string {
	lower := strings.ToLower(text)
	marker := "## ocr text"
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return strings.TrimSpace(text)
	}
	rest := strings.TrimSpace(text[idx+len(marker):])
	if next := strings.Index(strings.ToLower(rest), "\n## "); next >= 0 {
		rest = strings.TrimSpace(rest[:next])
	}
	return strings.TrimSpace(rest)
}

func fileAttachmentIsImage(attachment types.FileAttachment) bool {
	if strings.TrimSpace(strings.ToLower(attachment.FileKind)) == "image" {
		return true
	}
	if strings.HasPrefix(fileAttachmentMIME(attachment), "image/") {
		return true
	}
	switch fileAttachmentExt(attachment.FileName) {
	case "png", "jpg", "jpeg", "webp", "gif":
		return true
	default:
		return false
	}
}

func fileAttachmentIsSpreadsheet(attachment types.FileAttachment) bool {
	if strings.TrimSpace(strings.ToLower(attachment.FileKind)) == "spreadsheet" {
		return true
	}
	mimeType := fileAttachmentMIME(attachment)
	if mimeType == "application/vnd.ms-excel" || strings.Contains(mimeType, "spreadsheet") {
		return true
	}
	switch fileAttachmentExt(attachment.FileName) {
	case "xls", "xlsx":
		return true
	default:
		return false
	}
}

func fileAttachmentIsKeynote(attachment types.FileAttachment) bool {
	if fileAttachmentExt(attachment.FileName) == "key" {
		return true
	}
	mimeType := fileAttachmentMIME(attachment)
	return mimeType == "application/vnd.apple.keynote" ||
		mimeType == "application/x-iwork-keynote-sffkey" ||
		strings.Contains(mimeType, "keynote")
}

func fileAttachmentNativeInputAvailability(settings config.AppSettings, providerID string, profileID *string, attachments []types.FileAttachment) map[string]bool {
	out := make(map[string]bool, len(attachments))
	for _, attachment := range attachments {
		artifactID := strings.TrimSpace(attachment.ArtifactID)
		if artifactID == "" {
			continue
		}
		out[artifactID] = fileAttachmentNativeInputAvailableForRun(settings, providerID, profileID, attachment)
	}
	return out
}

func fileAttachmentNativeImageInputAvailability(settings config.AppSettings, providerID string, profileID *string, attachments []types.FileAttachment) map[string]bool {
	out := make(map[string]bool, len(attachments))
	for _, attachment := range attachments {
		artifactID := strings.TrimSpace(attachment.ArtifactID)
		if artifactID == "" {
			continue
		}
		out[artifactID] = fileAttachmentNativeImageInputAvailableForRun(settings, providerID, profileID, attachment)
	}
	return out
}

func fileAttachmentNativeInputAvailableForRun(settings config.AppSettings, providerID string, profileID *string, attachment types.FileAttachment) bool {
	if strings.TrimSpace(attachment.InlineDataBase64) == "" {
		return false
	}
	providerID = fileAttachmentEffectiveProviderID(settings, providerID)
	switch providerID {
	case "openai":
		return true
	case "compatible":
		return fileAttachmentOpenAICompatNativeInputAvailable(settings, providerID, profileID, attachment)
	case "anthropic":
		return fileAttachmentAnthropicNativeInputAvailable(attachment)
	case "bedrock":
		if fileAttachmentBedrockUsesOpenAICompat(settings) {
			return fileAttachmentOpenAICompatNativeInputAvailable(settings, providerID, profileID, attachment)
		}
		return fileAttachmentBedrockNativeInputAvailable(attachment)
	case "gemini":
		return fileAttachmentGeminiNativeInputAvailable(attachment)
	default:
		return false
	}
}

func fileAttachmentNativeImageInputAvailableForRun(settings config.AppSettings, providerID string, profileID *string, attachment types.FileAttachment) bool {
	if strings.TrimSpace(attachment.InlineDataBase64) == "" || fileAttachmentNativeImageMediaType(attachment) == "" {
		return false
	}
	providerID = fileAttachmentEffectiveProviderID(settings, providerID)
	switch providerID {
	case "openai", "anthropic", "gemini":
		return true
	case "compatible":
		return fileAttachmentOpenAICompatFileInputMode(settings, providerID, profileID) != "sidecar_only"
	case "bedrock":
		if fileAttachmentBedrockUsesOpenAICompat(settings) {
			return fileAttachmentOpenAICompatFileInputMode(settings, providerID, profileID) != "sidecar_only"
		}
		return false
	default:
		return false
	}
}

func fileAttachmentEffectiveProviderID(settings config.AppSettings, fallback string) string {
	if cfg := fileAttachmentActiveProviderConfig(settings); cfg != nil {
		if providerID := strings.TrimSpace(cfg.ProviderID); providerID != "" {
			return providerID
		}
	}
	return strings.TrimSpace(fallback)
}

func fileAttachmentActiveProviderConfig(settings config.AppSettings) *config.ProviderConfig {
	if len(settings.ProviderConfigs) == 0 {
		return nil
	}
	if settings.ActiveProviderConfigID != nil {
		desired := strings.TrimSpace(*settings.ActiveProviderConfigID)
		if desired != "" {
			for i := range settings.ProviderConfigs {
				if strings.TrimSpace(settings.ProviderConfigs[i].ID) == desired {
					return &settings.ProviderConfigs[i]
				}
			}
		}
	}
	return &settings.ProviderConfigs[0]
}

func fileAttachmentBedrockUsesOpenAICompat(settings config.AppSettings) bool {
	cfg := fileAttachmentActiveProviderConfig(settings)
	if cfg == nil || strings.TrimSpace(cfg.ProviderID) != "bedrock" {
		return false
	}
	apiType := ""
	if cfg.ApiType != nil {
		apiType = strings.TrimSpace(*cfg.ApiType)
	}
	return apiType != "" && apiType != "messages"
}

func fileAttachmentOpenAICompatNativeInputAvailable(settings config.AppSettings, providerID string, profileID *string, attachment types.FileAttachment) bool {
	mode := fileAttachmentOpenAICompatFileInputMode(settings, providerID, profileID)
	if mode == "sidecar_only" {
		return false
	}
	if fileAttachmentNativeImageMediaType(attachment) != "" {
		return false
	}
	if mode == "native_pdf_only" && !fileAttachmentIsPDF(attachment) {
		return false
	}
	return true
}

func fileAttachmentOpenAICompatFileInputMode(settings config.AppSettings, providerID string, profileID *string) string {
	rawMode := ""
	if cfg := fileAttachmentActiveProviderConfig(settings); cfg != nil && strings.TrimSpace(cfg.ProviderID) == strings.TrimSpace(providerID) {
		if cfg.FileInputMode != nil {
			rawMode = strings.TrimSpace(*cfg.FileInputMode)
		}
	} else if providerID == "compatible" {
		profile := fileAttachmentCompatibleProfile(settings, profileID)
		rawMode = strings.TrimSpace(profile.FileInputMode)
	}

	switch rawMode {
	case "sidecar_only", "native_pdf_only", "native_supported":
		return rawMode
	default:
		return "native_supported"
	}
}

func fileAttachmentCompatibleProfile(settings config.AppSettings, profileID *string) config.CompatibleProfile {
	want := ""
	if profileID != nil {
		want = strings.TrimSpace(*profileID)
	}
	if want == "" && settings.Provider.ProfileID != nil {
		want = strings.TrimSpace(*settings.Provider.ProfileID)
	}
	for _, profile := range settings.CompatibleProfiles {
		if want != "" && strings.TrimSpace(profile.ID) == want {
			return profile
		}
	}
	if len(settings.CompatibleProfiles) > 0 {
		return settings.CompatibleProfiles[0]
	}
	return config.CompatibleProfile{}
}

func fileAttachmentAnthropicNativeInputAvailable(attachment types.FileAttachment) bool {
	return true
}

func fileAttachmentBedrockNativeInputAvailable(attachment types.FileAttachment) bool {
	if fileAttachmentBase64DecodedLength(attachment.InlineDataBase64) > fileAttachmentBedrockNativeMaxBytes {
		return false
	}
	switch fileAttachmentExt(attachment.FileName) {
	case "pdf", "txt", "md", "html", "csv", "doc", "docx", "xls", "xlsx":
		return true
	}
	switch fileAttachmentMIME(attachment) {
	case "application/pdf",
		"text/plain",
		"text/markdown",
		"text/html",
		"text/csv",
		"application/csv",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return true
	default:
		return false
	}
}

func fileAttachmentGeminiNativeInputAvailable(attachment types.FileAttachment) bool {
	return fileAttachmentNativeInputCandidate(attachment)
}

func fileAttachmentIsPDF(attachment types.FileAttachment) bool {
	if fileAttachmentExt(attachment.FileName) == "pdf" {
		return true
	}
	return fileAttachmentMIME(attachment) == "application/pdf"
}

func fileAttachmentTextLike(attachment types.FileAttachment) bool {
	if attachment.IsText != nil && *attachment.IsText {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(attachment.FileKind)) {
	case "text", "csv", "json", "html":
		return true
	}
	mimeType := fileAttachmentMIME(attachment)
	if strings.HasPrefix(mimeType, "text/") || strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml") {
		return true
	}
	switch mimeType {
	case "application/json",
		"application/javascript",
		"application/x-ndjson",
		"application/x-sh",
		"application/x-yaml",
		"application/xml",
		"application/rtf",
		"application/toml",
		"application/yaml",
		"image/svg+xml":
		return true
	default:
		return false
	}
}

func fileAttachmentMIME(attachment types.FileAttachment) string {
	return strings.TrimSpace(strings.ToLower(firstNonEmptyString(
		attachment.NormalizedMIME,
		attachment.MimeType,
		attachment.DetectedMimeType,
		attachment.DeclaredMIME,
		attachment.ExtensionMIME,
		attachment.SniffedMIME,
	)))
}

func fileAttachmentExt(fileName string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(filepath.Ext(strings.TrimSpace(fileName)))), ".")
}

func fileAttachmentNativeImageMediaType(attachment types.FileAttachment) string {
	switch fileAttachmentMIME(attachment) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	}
	switch fileAttachmentExt(attachment.FileName) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

func fileAttachmentBase64DecodedLength(data string) int64 {
	clean := strings.TrimSpace(data)
	if clean == "" {
		return 0
	}
	padding := 0
	for i := len(clean) - 1; i >= 0 && clean[i] == '='; i-- {
		padding++
	}
	return int64((len(clean)*3)/4 - padding)
}

func (m *Manager) readFileAttachmentExtractionPreview(extraction map[string]any, maxBytes int) (string, bool, string) {
	if len(extraction) == 0 || strings.TrimSpace(m.cfgDir) == "" {
		return "", false, ""
	}
	if strings.EqualFold(strings.TrimSpace(stringMapValue(extraction, "kind")), "spreadsheet") {
		return m.readSpreadsheetAttachmentExtractionPreview(extraction, maxBytes)
	}
	for _, rel := range fileAttachmentExtractionPreviewPaths(extraction) {
		content, truncated, ok := m.readRelTextPrefix(rel, maxBytes)
		if ok && strings.TrimSpace(content) != "" {
			return content, truncated, filepath.ToSlash(strings.TrimSpace(rel))
		}
	}
	return "", false, ""
}

func fileAttachmentExtractionPreviewPaths(extraction map[string]any) []string {
	kind := strings.TrimSpace(strings.ToLower(stringMapValue(extraction, "kind")))
	paths := make([]string, 0, 12)
	if kind == "image" {
		paths = append(paths, stringMapValue(extraction, "ocrTextPath", "ocr_text_path"))
	}
	if kind == "spreadsheet" {
		paths = append(paths, stringMapValue(extraction, "markdownPath", "markdown_path"))
		paths = append(paths, fileAttachmentSpreadsheetPreviewMarkdownPaths(fileAttachmentStringSliceMapValue(extraction, "sheetOutputs", "sheet_outputs"))...)
		if outputs, ok := fileAttachmentMapValue(extraction, "outputs"); ok {
			rootPath := stringMapValue(extraction, "rootPath", "root_path")
			for _, nested := range fileAttachmentSpreadsheetPreviewMarkdownPaths(fileAttachmentStringSliceMapValue(outputs, "sheetOutputs", "sheet_outputs")) {
				paths = append(paths, fileAttachmentJoinExtractionRoot(rootPath, nested))
			}
		}
		return compactFileAttachmentStrings(paths...)
	}
	paths = append(paths,
		stringMapValue(extraction, "markdownPath", "markdown_path"),
		stringMapValue(extraction, "textPath", "text_path"),
		stringMapValue(extraction, "documentPath", "document_path"),
		stringMapValue(extraction, "contentPath", "content_path"),
		stringMapValue(extraction, "ocrTextPath", "ocr_text_path"),
		stringMapValue(extraction, "dataPath", "data_path"),
		stringMapValue(extraction, "rowsPath", "rows_path"),
		stringMapValue(extraction, "schemaPath", "schema_path"),
		stringMapValue(extraction, "manifestPath", "manifest_path"),
	)
	return compactFileAttachmentStrings(paths...)
}

func (m *Manager) readSpreadsheetAttachmentExtractionPreview(extraction map[string]any, maxBytes int) (string, bool, string) {
	if maxBytes <= 0 {
		return "", false, ""
	}
	paths := compactFileAttachmentStrings(append(
		[]string{stringMapValue(extraction, "markdownPath", "markdown_path")},
		m.spreadsheetWorkbookOrderedPreviewPaths(extraction)...,
	)...)
	paths = compactFileAttachmentStrings(append(paths, fileAttachmentExtractionPreviewPaths(extraction)...)...)
	if len(paths) == 0 {
		return "", false, ""
	}

	var b strings.Builder
	sources := make([]string, 0, len(paths))
	truncated := false
	for _, rel := range paths {
		remaining := maxBytes - b.Len()
		if remaining <= 0 {
			truncated = true
			break
		}
		content, cut, ok := m.readRelTextPrefix(rel, remaining)
		content = strings.TrimSpace(content)
		if !ok || content == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n\n---\n\n")
		}
		b.WriteString(content)
		sources = append(sources, filepath.ToSlash(strings.TrimSpace(rel)))
		if cut {
			truncated = true
			break
		}
	}
	if strings.TrimSpace(b.String()) == "" {
		return "", false, ""
	}
	return b.String(), truncated, strings.Join(sources, ", ")
}

func (m *Manager) spreadsheetWorkbookOrderedPreviewPaths(extraction map[string]any) []string {
	rootPath := stringMapValue(extraction, "rootPath", "root_path")
	workbookPath := firstNonEmptyString(
		stringMapValue(extraction, "workbookPath", "workbook_path"),
		stringMapValue(extraction, "dataPath", "data_path"),
	)
	if workbookPath == "" {
		return nil
	}
	raw, _, ok := m.readRelTextPrefix(workbookPath, 1<<20)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil
	}
	var workbook struct {
		Sheets []struct {
			State           string `json:"state"`
			PreviewPath     string `json:"previewPath"`
			PreviewPathAlt  string `json:"preview_path"`
			PreviewMarkdown string `json:"previewMarkdownPath"`
		} `json:"sheets"`
	}
	if err := json.Unmarshal([]byte(raw), &workbook); err != nil || len(workbook.Sheets) == 0 {
		return nil
	}
	visible := make([]string, 0, len(workbook.Sheets))
	hidden := make([]string, 0, len(workbook.Sheets))
	for _, sheet := range workbook.Sheets {
		previewPath := firstNonEmptyString(sheet.PreviewPath, sheet.PreviewPathAlt, sheet.PreviewMarkdown)
		if strings.TrimSpace(previewPath) == "" {
			continue
		}
		joined := fileAttachmentJoinExtractionRoot(rootPath, previewPath)
		state := strings.TrimSpace(strings.ToLower(sheet.State))
		if state == "" || state == "visible" {
			visible = append(visible, joined)
		} else {
			hidden = append(hidden, joined)
		}
	}
	return compactFileAttachmentStrings(append(visible, hidden...)...)
}

func fileAttachmentSpreadsheetPreviewMarkdownPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := strings.TrimSpace(filepath.ToSlash(path))
		if clean == "" {
			continue
		}
		lower := strings.ToLower(clean)
		if strings.HasSuffix(lower, ".md") {
			out = append(out, clean)
		}
	}
	return out
}

func (m *Manager) readRelTextPrefix(rel string, maxBytes int) (string, bool, bool) {
	if maxBytes <= 0 {
		return "", false, false
	}
	clean, err := cleanFileAttachmentRelPath(rel)
	if err != nil {
		return "", false, false
	}
	f, err := os.Open(filepath.Join(m.cfgDir, clean))
	if err != nil {
		return "", false, false
	}
	defer f.Close()

	raw, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)+1))
	if err != nil || len(raw) == 0 {
		return "", false, false
	}
	truncated := len(raw) > maxBytes
	if truncated {
		raw = raw[:maxBytes]
	}
	return strings.TrimSpace(strings.ToValidUTF8(string(raw), "")), truncated, true
}

func cleanFileAttachmentRelPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.Contains(trimmed, "\x00") {
		return "", fmt.Errorf("invalid relative path")
	}
	clean := filepath.Clean(filepath.FromSlash(trimmed))
	if clean == "" || clean == "." || clean == ".." || filepath.IsAbs(clean) {
		return "", fmt.Errorf("invalid relative path")
	}
	if strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("invalid relative path")
	}
	return clean, nil
}

func fileAttachmentExtractionMap(artifact store.Artifact) map[string]any {
	if len(artifact.Metadata) == 0 {
		return nil
	}
	raw, ok := artifact.Metadata["extraction"]
	if !ok {
		return nil
	}
	extraction, ok := raw.(map[string]any)
	if !ok || len(extraction) == 0 {
		return nil
	}
	return extraction
}

func fileAttachmentNativeInputCandidate(attachment types.FileAttachment) bool {
	mimeType := strings.ToLower(firstNonEmptyString(
		attachment.NormalizedMIME,
		attachment.MimeType,
		attachment.DetectedMimeType,
		attachment.DeclaredMIME,
		attachment.ExtensionMIME,
		attachment.SniffedMIME,
	))
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.FileName))), ".")
	if mimeType == "application/pdf" || ext == "pdf" {
		return true
	}
	if attachment.IsText != nil && *attachment.IsText {
		return true
	}
	switch mimeType {
	case "text/plain",
		"text/markdown",
		"text/html",
		"text/xml",
		"application/xml",
		"application/json",
		"application/javascript",
		"application/x-ndjson",
		"application/x-sh",
		"application/x-yaml",
		"application/rtf",
		"application/toml",
		"application/yaml",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.apple.keynote",
		"application/x-iwork-keynote-sffkey",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"image/svg+xml",
		"text/csv",
		"text/tab-separated-values",
		"application/csv":
		return true
	}
	if strings.HasPrefix(mimeType, "text/") || strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml") {
		return true
	}
	switch ext {
	case "txt", "md", "markdown", "json", "jsonl", "ndjson", "html", "htm", "xml", "rtf", "doc", "docx", "ppt", "pptx", "key", "csv", "tsv", "tab", "xls", "xlsx",
		"yaml", "yml", "toml", "ini", "cfg", "conf", "log", "sql", "py", "go", "js", "jsx", "ts", "tsx", "rb", "rs", "java", "css", "scss", "sh", "bash", "zsh", "c", "h", "cpp", "hpp", "svg":
		return true
	default:
		return false
	}
}

func fileAttachmentPreferExtractionPreview(attachment types.FileAttachment) bool {
	kind := strings.ToLower(strings.TrimSpace(attachment.FileKind))
	switch kind {
	case "spreadsheet", "presentation", "document", "pdf", "csv", "html", "json":
		return true
	}
	mimeType := strings.ToLower(firstNonEmptyString(
		attachment.NormalizedMIME,
		attachment.MimeType,
		attachment.DetectedMimeType,
		attachment.DeclaredMIME,
		attachment.ExtensionMIME,
		attachment.SniffedMIME,
	))
	switch {
	case mimeType == "application/pdf",
		mimeType == "application/json",
		mimeType == "text/html",
		mimeType == "application/xhtml+xml",
		mimeType == "application/rtf",
		mimeType == "application/msword",
		mimeType == "application/vnd.ms-excel",
		mimeType == "application/vnd.ms-powerpoint",
		strings.Contains(mimeType, "wordprocessingml"),
		strings.Contains(mimeType, "spreadsheet"),
		strings.Contains(mimeType, "presentation"):
		return true
	default:
		return false
	}
}

func fileAttachmentExtractionPreviewIsOCR(sourcePath string) bool {
	clean := strings.TrimSpace(strings.ToLower(filepath.ToSlash(sourcePath)))
	return clean != "" && (strings.HasSuffix(clean, "/ocr.txt") || clean == "ocr.txt" || strings.Contains(clean, "ocr"))
}

func boolPtr(v bool) *bool {
	return &v
}

func boolPtrValue(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func compactFileAttachmentStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		clean := strings.TrimSpace(value)
		if clean == "" {
			continue
		}
		key := filepath.ToSlash(clean)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func fileAttachmentStringSliceMapValue(m map[string]any, keys ...string) []string {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case []string:
			return append([]string(nil), v...)
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func fileAttachmentMapValue(m map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		switch v := raw.(type) {
		case map[string]any:
			return v, true
		}
	}
	return nil, false
}

func fileAttachmentJoinExtractionRoot(rootPath string, relPath string) string {
	rootPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(rootPath)), "/")
	relPath = strings.Trim(strings.TrimSpace(filepath.ToSlash(relPath)), "/")
	switch {
	case relPath == "":
		return ""
	case rootPath == "":
		return relPath
	default:
		return rootPath + "/" + relPath
	}
}

func truncateStringRunes(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	if limit <= 0 || value == "" {
		return "", value != ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return strings.TrimSpace(string(runes[:limit])), true
}

func indentLines(value string, prefix string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return prefix
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func appendHiddenContext(base string, extra string) string {
	base = strings.TrimSpace(base)
	extra = strings.TrimSpace(extra)
	switch {
	case base == "":
		return extra
	case extra == "":
		return base
	default:
		return base + "\n\n" + extra
	}
}

func isThreadFileAttachmentArtifact(threadID string, artifact store.Artifact) bool {
	return fileartifact.IsThreadFileArtifact(threadID, artifact)
}

func boolMapValue(m map[string]any, keys ...string) (bool, bool) {
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

func boolMapMapValue(m map[string]any, keys ...string) map[string]bool {
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
