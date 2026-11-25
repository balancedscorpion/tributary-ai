package httpapi

import (
	"bytes"
	"encoding/base64"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
)

// handleGetArtifactRaw streams the original bytes of an uploaded file,
// composer image attachment, or generated-image artifact. Any other artifact
// kind is treated as not found so this endpoint never exposes internal
// tool/analysis artifacts.
func (h *Handler) handleGetArtifactRaw(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}
	a, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}

	switch {
	case fileartifact.IsFileAttachmentArtifact(a):
		h.serveFileAttachmentRaw(w, r, a)
	case isRawImageArtifact(a):
		h.serveImageAttachmentRaw(w, r, a)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

// handleGetArtifactExtracted streams whitelisted visual preview files produced
// by the upload extraction pipeline. It intentionally serves only paths recorded
// in artifact metadata, so extracted XML/Markdown/text outputs are not exposed
// as generic downloadable files.
func (h *Handler) handleGetArtifactExtracted(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing artifactId"})
		return
	}
	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	if requested == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "missing path"})
		return
	}
	cleanRequested, err := cleanRelPath(requested)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid path"})
		return
	}

	a, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeErr(w, err)
		return
	}
	if !fileartifact.IsFileAttachmentArtifact(a) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	extraction, ok := a.Metadata["extraction"].(map[string]any)
	if !ok || !extractedPreviewPathAllowed(extraction, cleanRequested) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	full, ok := safeConfigRelativePath(h.cfgDir, cleanRequested)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	defer f.Close()

	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(cleanRequested)))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	setRawArtifactHeaders(w, mimeType, path.Base(cleanRequested))
	http.ServeContent(w, r, path.Base(cleanRequested), time.Time{}, f)
}

func extractedPreviewPathAllowed(extraction map[string]any, requested string) bool {
	allowed := map[string]struct{}{}
	addVisual := func(value string) {
		clean, err := cleanRelPath(value)
		if err == nil && extractedPreviewPathIsVisual(clean) {
			allowed[clean] = struct{}{}
		}
	}
	addSheetPreview := func(value string) {
		clean, err := cleanRelPath(value)
		if err == nil && extractedPreviewPathIsSpreadsheetPreview(clean) {
			allowed[clean] = struct{}{}
		}
	}
	addVisual(firstStringMapValue(extraction, "previewImagePath", "preview_image_path"))
	addSheetPreview(firstStringMapValue(extraction, "workbookPath", "workbook_path"))
	for _, value := range artifactPreviewStringSliceFromAny(extraction["renderedSlides"]) {
		addVisual(value)
	}
	for _, value := range artifactPreviewStringSliceFromAny(extraction["sheetOutputs"]) {
		addSheetPreview(value)
	}
	for _, value := range artifactPreviewStringSliceFromAny(extraction["sheetPreviewOutputs"]) {
		addSheetPreview(value)
	}
	root := strings.Trim(strings.TrimSpace(firstStringMapValue(extraction, "rootPath", "root_path")), "/")
	if outputs, ok := extraction["outputs"].(map[string]any); ok {
		if workbookPath := firstStringMapValue(outputs, "workbookPath", "workbook_path"); workbookPath != "" {
			if root != "" {
				addSheetPreview(path.Join(root, workbookPath))
			} else {
				addSheetPreview(workbookPath)
			}
		}
		for _, value := range artifactPreviewStringSliceFromAny(outputs["renderedSlides"]) {
			if root != "" {
				addVisual(path.Join(root, value))
			} else {
				addVisual(value)
			}
		}
		for _, value := range artifactPreviewStringSliceFromAny(outputs["sheetOutputs"]) {
			if root != "" {
				addSheetPreview(path.Join(root, value))
			} else {
				addSheetPreview(value)
			}
		}
		for _, value := range artifactPreviewStringSliceFromAny(outputs["sheetPreviewOutputs"]) {
			if root != "" {
				addSheetPreview(path.Join(root, value))
			} else {
				addSheetPreview(value)
			}
		}
	}
	_, ok := allowed[requested]
	return ok
}

func extractedPreviewPathIsVisual(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func extractedPreviewPathIsSpreadsheetPreview(rel string) bool {
	clean := strings.ToLower(filepath.ToSlash(strings.TrimSpace(rel)))
	if path.Base(clean) == "workbook.json" {
		return true
	}
	if !strings.Contains(clean, "/worksheets/") {
		return false
	}
	switch path.Base(clean) {
	case "preview.md", "preview.json":
		return true
	default:
		return false
	}
}

func artifactPreviewStringSliceFromAny(raw any) []string {
	switch v := raw.(type) {
	case []string:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if clean := strings.TrimSpace(item); clean != "" {
				out = append(out, clean)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if clean := strings.TrimSpace(s); clean != "" {
					out = append(out, clean)
				}
			}
		}
		return out
	default:
		return nil
	}
}

func isRawImageArtifact(a store.Artifact) bool {
	if !strings.EqualFold(strings.TrimSpace(a.Type), "image") {
		return false
	}
	if a.ToolName != nil {
		switch strings.ToLower(strings.TrimSpace(*a.ToolName)) {
		case "image_attachment":
			return true
		}
	}
	return isGeneratedImageArtifact(a)
}

func isGeneratedImageArtifact(a store.Artifact) bool {
	if !strings.EqualFold(strings.TrimSpace(a.Type), "image") {
		return false
	}
	if a.ToolName != nil && strings.EqualFold(strings.TrimSpace(*a.ToolName), "generate_image") {
		return true
	}
	if metadataTool, ok := a.Metadata["tool_name"].(string); ok && strings.EqualFold(strings.TrimSpace(metadataTool), "generate_image") {
		return true
	}
	if metadataTool, ok := a.Metadata["toolName"].(string); ok && strings.EqualFold(strings.TrimSpace(metadataTool), "generate_image") {
		return true
	}
	if generated, ok := a.Metadata["generated"].(bool); ok && generated {
		return true
	}
	source, _ := a.Metadata["source"].(string)
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "generated_image", "image_generation":
		return true
	default:
		return false
	}
}

// serveFileAttachmentRaw streams the stored bytes for a thread file
// attachment from its cfgDir-relative ContentPath.
func (h *Handler) serveFileAttachmentRaw(w http.ResponseWriter, r *http.Request, a store.Artifact) {
	if a.ContentPath == nil || strings.TrimSpace(*a.ContentPath) == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	full, ok := safeConfigRelativePath(h.cfgDir, *a.ContentPath)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	f, err := os.Open(full)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}
	defer f.Close()

	setRawArtifactHeaders(w, artifactRawMIME(a.Metadata), a.Title)
	http.ServeContent(w, r, a.Title, time.Time{}, f)
}

// serveImageAttachmentRaw streams decoded bytes for a composer attachment or
// generated image. Image content is stored as a data URI, either inline in
// ContentText or at a cfgDir-relative ContentPath; either may also hold raw
// bytes for robustness.
func (h *Handler) serveImageAttachmentRaw(w http.ResponseWriter, r *http.Request, a store.Artifact) {
	var content []byte
	switch {
	case a.ContentText != nil && strings.TrimSpace(*a.ContentText) != "":
		content = []byte(*a.ContentText)
	case a.ContentPath != nil && strings.TrimSpace(*a.ContentPath) != "":
		full, ok := safeConfigRelativePath(h.cfgDir, *a.ContentPath)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		content = raw
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
		return
	}

	mimeType := artifactRawMIME(a.Metadata)
	if decoded, dataMIME, ok := decodeDataURI(content); ok {
		content = decoded
		if strings.TrimSpace(dataMIME) != "" {
			mimeType = dataMIME
		}
	}

	setRawArtifactHeaders(w, mimeType, a.Title)
	http.ServeContent(w, r, a.Title, time.Time{}, bytes.NewReader(content))
}

// decodeDataURI decodes a "data:" URI, returning the raw payload bytes and the
// declared MIME type. It reports false when the input is not a data URI.
func decodeDataURI(content []byte) ([]byte, string, bool) {
	const prefix = "data:"
	if !bytes.HasPrefix(content, []byte(prefix)) {
		return nil, "", false
	}
	comma := bytes.IndexByte(content, ',')
	if comma < 0 {
		return nil, "", false
	}
	header := string(content[len(prefix):comma])
	payload := content[comma+1:]

	mimeType := header
	base64Encoded := false
	if idx := strings.IndexByte(header, ';'); idx >= 0 {
		mimeType = header[:idx]
		for _, param := range strings.Split(header[idx+1:], ";") {
			if strings.EqualFold(strings.TrimSpace(param), "base64") {
				base64Encoded = true
			}
		}
	}
	mimeType = strings.TrimSpace(mimeType)

	if base64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(payload)))
		if err != nil {
			return nil, "", false
		}
		return decoded, mimeType, true
	}
	if unescaped, err := url.QueryUnescape(string(payload)); err == nil {
		return []byte(unescaped), mimeType, true
	}
	return payload, mimeType, true
}

// artifactRawMIME resolves a content type from the artifact metadata, falling
// back to application/octet-stream. It shares the file-audit/native-viewer
// alias chain, including connector-native keys nested in result_metadata
// (Slack `mimetype`, HTTP-ish `content_type`, ...), so /raw headers agree
// with how the viewer routes the same artifact.
func artifactRawMIME(metadata map[string]any) string {
	mime := fileAuditMimeType(metadata, plainMap(metadata["result_metadata"]))
	if mime == "" {
		return "application/octet-stream"
	}
	return mime
}

func setRawArtifactHeaders(w http.ResponseWriter, mimeType, filename string) {
	if strings.TrimSpace(mimeType) == "" {
		mimeType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Disposition", contentDispositionInline(filename))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=3600")
}

// contentDispositionInline builds an RFC 6266-safe inline Content-Disposition
// header, providing both an ASCII-quoted filename and a UTF-8 filename*.
func contentDispositionInline(filename string) string {
	filename = strings.TrimSpace(filename)
	if filename == "" {
		return "inline"
	}
	var ascii strings.Builder
	nonASCII := false
	for _, r := range filename {
		switch {
		case r == '"' || r == '\\':
			ascii.WriteByte('_')
		case r < 0x20 || r == 0x7f:
			ascii.WriteByte('_')
		case r > 0x7f:
			nonASCII = true
			ascii.WriteByte('_')
		default:
			ascii.WriteRune(r)
		}
	}
	disposition := "inline; filename=\"" + ascii.String() + "\""
	if nonASCII {
		disposition += "; filename*=UTF-8''" + url.PathEscape(filename)
	}
	return disposition
}
