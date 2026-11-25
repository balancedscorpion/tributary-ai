package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultAttachmentPreviewBytes int64 = 32 << 10
	maxAttachmentPreviewBytes     int64 = 256 << 10
	defaultAttachmentSearchBytes  int64 = 1 << 20
	maxAttachmentSearchBytes      int64 = 8 << 20
)

func threadAttachmentOutput(toolName string, result map[string]any, artifact store.Artifact, kind string, extra map[string]any) Output {
	return JSONOKWithMetadata(result, threadAttachmentResultMetadata(toolName, artifact, kind, extra))
}

func threadAttachmentResultMetadata(toolName string, artifact store.Artifact, kind string, extra map[string]any) map[string]any {
	source := strings.TrimSpace(stringMapValue(artifact.Metadata, "source"))
	if source == "" {
		source = "attachment"
	}
	mimeType := strings.TrimSpace(stringMapValue(artifact.Metadata, "mimeType", "mime_type"))
	detectedMimeType := strings.TrimSpace(stringMapValue(artifact.Metadata, "detectedMimeType", "detected_mime_type"))
	meta := map[string]any{
		"provider":      "attachments",
		"product":       "attachments",
		"family":        "attachments",
		"kind":          strings.TrimSpace(kind),
		"tool_name":     strings.TrimSpace(toolName),
		"artifact_id":   artifact.ID,
		"file_name":     artifact.Title,
		"artifact_type": artifact.Type,
		"mime_type":     mimeType,
		"size_bytes":    artifact.ByteSize,
		"source":        source,
		"readonly":      true,
	}
	if detectedMimeType != "" {
		meta["detected_mime_type"] = detectedMimeType
	}
	if artifact.Language != nil && strings.TrimSpace(*artifact.Language) != "" {
		meta["language"] = strings.TrimSpace(*artifact.Language)
	}
	if attachmentIsText(artifact) {
		meta["is_text"] = true
	}
	if attachmentPreviewAvailable(artifact) {
		meta["preview_available"] = true
	}
	if attachmentPreviewTruncated(artifact) {
		meta["preview_truncated"] = true
	}
	if extraction := attachmentExtraction(artifact); extraction != nil {
		if extractionKind := strings.TrimSpace(stringMapValue(extraction, "kind")); extractionKind != "" {
			meta["extraction_kind"] = extractionKind
		}
		if pipeline := strings.TrimSpace(stringMapValue(extraction, "pipeline")); pipeline != "" {
			meta["extraction_pipeline"] = pipeline
		}
		if status := strings.TrimSpace(stringMapValue(extraction, "status")); status != "" {
			meta["extraction_status"] = status
		}
	}

	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_name"] = toolMeta.Name
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		if strings.TrimSpace(toolMeta.ConnectorID) != "" {
			meta["connector_id"] = toolMeta.ConnectorID
		}
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := threadAttachmentPresentationMetadata(toolMeta.Presentation)
		if family := strings.TrimSpace(fmt.Sprint(presentation["family"])); family != "" && family != "<nil>" {
			meta["family"] = family
		}
		if resultKind := strings.TrimSpace(fmt.Sprint(presentation["result_kind"])); resultKind != "" && resultKind != "<nil>" {
			meta["result_kind"] = resultKind
		}
		if rendererStrategy := strings.TrimSpace(fmt.Sprint(presentation["renderer_strategy"])); rendererStrategy != "" && rendererStrategy != "<nil>" {
			meta["renderer_strategy"] = rendererStrategy
		}
		if rendererKey := strings.TrimSpace(fmt.Sprint(presentation["renderer_key"])); rendererKey != "" && rendererKey != "<nil>" {
			meta["renderer_key"] = rendererKey
		}
		if nativeViewer := strings.TrimSpace(fmt.Sprint(presentation["native_viewer"])); nativeViewer != "" && nativeViewer != "<nil>" {
			meta["native_viewer"] = nativeViewer
		}
		if fileCapability := strings.TrimSpace(fmt.Sprint(presentation["file_capability"])); fileCapability != "" && fileCapability != "<nil>" {
			meta["file_capability"] = fileCapability
		}
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
		}
	}

	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		meta[key] = value
	}
	return meta
}

func threadAttachmentPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putThreadAttachmentMetadataValue(out, "family", p.Family)
	putThreadAttachmentMetadataValue(out, "result_kind", p.ResultKind)
	putThreadAttachmentMetadataValue(out, "renderer_strategy", p.RendererStrategy)
	putThreadAttachmentMetadataValue(out, "renderer_key", p.RendererKey)
	putThreadAttachmentMetadataValue(out, "native_viewer", p.NativeViewer)
	putThreadAttachmentMetadataValue(out, "artifact_types", cloneThreadAttachmentStringSlice(p.ArtifactTypes))
	putThreadAttachmentMetadataValue(out, "file_capability", p.FileCapability)
	putThreadAttachmentMetadataValue(out, "file_kinds", cloneThreadAttachmentStringSlice(p.FileKinds))
	putThreadAttachmentMetadataValue(out, "preview_modes", cloneThreadAttachmentStringSlice(p.PreviewModes))
	putThreadAttachmentMetadataValue(out, "source_refs", cloneThreadAttachmentStringSlice(p.SourceRefs))
	putThreadAttachmentMetadataValue(out, "produces_file_artifact", p.ProducesFileArtifact)
	putThreadAttachmentMetadataValue(out, "promotes_native_input", p.PromotesNativeInput)
	putThreadAttachmentMetadataValue(out, "native_links", cloneThreadAttachmentStringSlice(p.NativeLinks))
	return out
}

func putThreadAttachmentMetadataValue(out map[string]any, key string, value any) {
	key = strings.TrimSpace(key)
	if key == "" || !threadAttachmentMetadataHasValue(value) {
		return
	}
	out[key] = value
}

func threadAttachmentMetadataHasValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []string:
		return len(typed) > 0
	case bool:
		return typed
	default:
		return true
	}
}

func cloneThreadAttachmentStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func newInspectAttachmentTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "inspect_attachment",
				Description: "Inspect a thread-scoped uploaded file attachment. Returns metadata and a bounded text preview when available.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "The uploaded attachment artifact id.",
						},
						"max_preview_bytes": map[string]any{
							"type":        "integer",
							"description": "Optional preview byte cap (default 32768, max 262144).",
						},
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID      string `json:"artifact_id"`
				MaxPreviewBytes int64  `json:"max_preview_bytes,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifact, errOut, ok := requireThreadAttachment(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			maxPreviewBytes := clampPositiveInt64(in.MaxPreviewBytes, defaultAttachmentPreviewBytes, maxAttachmentPreviewBytes)
			preview := threadAttachmentPreview(artifact)
			if int64(len(preview)) > maxPreviewBytes {
				preview = preview[:maxPreviewBytes]
			}
			out := map[string]any{
				"artifact_id":        artifact.ID,
				"file_name":          artifact.Title,
				"type":               artifact.Type,
				"mime_type":          strings.TrimSpace(stringMapValue(artifact.Metadata, "mimeType", "mime_type")),
				"detected_mime_type": strings.TrimSpace(stringMapValue(artifact.Metadata, "detectedMimeType", "detected_mime_type")),
				"size_bytes":         artifact.ByteSize,
				"sha256":             strings.TrimSpace(stringMapValue(artifact.Metadata, "sha256")),
				"is_text":            attachmentIsText(artifact),
				"preview_available":  attachmentPreviewAvailable(artifact),
				"preview_truncated":  attachmentPreviewTruncated(artifact),
				"preview":            preview,
			}
			if note := strings.TrimSpace(stringMapValue(artifact.Metadata, "contentNote", "content_note")); note != "" {
				out["content_note"] = note
			}
			if artifact.Language != nil && strings.TrimSpace(*artifact.Language) != "" {
				out["language"] = strings.TrimSpace(*artifact.Language)
			}
			if extraction := attachmentExtraction(artifact); extraction != nil {
				out["extraction"] = compactAttachmentExtraction(extraction)
				if extractionPreview, _, sourcePath, err := readAttachmentExtractionPage(exec.CfgDir, extraction, 0, maxPreviewBytes); err == nil && strings.TrimSpace(extractionPreview) != "" {
					out["extraction_preview"] = extractionPreview
					out["extraction_preview_source"] = sourcePath
				}
			}
			return threadAttachmentOutput("inspect_attachment", out, artifact, "attachment_metadata", map[string]any{
				"max_preview_bytes": maxPreviewBytes,
				"preview_bytes":     len(preview),
			}), nil
		},
	}
}

func newReadAttachmentTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "read_attachment",
				Description: "Read a thread-scoped uploaded file attachment in paged text chunks.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "The uploaded attachment artifact id.",
						},
						"max_bytes": map[string]any{
							"type":        "integer",
							"description": "Maximum bytes to return for this page (default 65536, max 262144).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned by a previous read_attachment page.",
						},
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID string `json:"artifact_id"`
				MaxBytes   int    `json:"max_bytes,omitempty"`
				PageToken  string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifact, errOut, ok := requireThreadAttachment(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset < 0 {
				return JSONError("Invalid page_token"), nil
			}

			maxBytes := clampPositiveInt(in.MaxBytes, 64<<10, maxReadFileBytes)
			content := ""
			hasMore := false
			source := "attachment"
			sourcePath := ""
			var err error
			if attachmentIsText(artifact) {
				content, hasMore, err = readThreadAttachmentPage(exec.CfgDir, artifact, offset, int64(maxBytes))
			} else if extraction := attachmentExtraction(artifact); extraction != nil {
				content, hasMore, sourcePath, err = readAttachmentExtractionPage(exec.CfgDir, extraction, offset, int64(maxBytes))
				source = "extraction"
			} else {
				return JSONError("attachment is binary or not suitable for text reads; use inspect_attachment first"), nil
			}
			if err != nil {
				return JSONError(err.Error()), nil
			}

			out := map[string]any{
				"artifact_id":    artifact.ID,
				"file_name":      artifact.Title,
				"mime_type":      strings.TrimSpace(stringMapValue(artifact.Metadata, "mimeType", "mime_type")),
				"offset":         offset,
				"max_bytes":      maxBytes,
				"bytes_returned": len(content),
				"has_more":       hasMore,
				"content":        content,
				"source":         source,
			}
			if sourcePath != "" {
				out["source_path"] = sourcePath
			}
			if hasMore {
				nextOffset := offset + int64(len(content))
				out["next_page_token"] = encodePageOffsetToken(nextOffset)
			}
			readMeta := map[string]any{
				"offset":         offset,
				"max_bytes":      maxBytes,
				"bytes_returned": len(content),
				"has_more":       hasMore,
				"source":         source,
			}
			if sourcePath != "" {
				readMeta["source_path"] = sourcePath
			}
			return threadAttachmentOutput("read_attachment", out, artifact, "attachment_read", readMeta), nil
		},
	}
}

func newSearchAttachmentTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "search_attachment",
				Description: "Search text within a thread-scoped uploaded file attachment.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "The uploaded attachment artifact id.",
						},
						"query": map[string]any{
							"type":        "string",
							"description": "The text to search for.",
						},
						"case_sensitive": map[string]any{
							"type":        "boolean",
							"description": "Whether the search should be case sensitive. Default false.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum matches to return (default 20, max 100).",
						},
						"max_file_bytes": map[string]any{
							"type":        "integer",
							"description": "Maximum bytes to read while searching (default 1 MiB, max 8 MiB).",
						},
					},
					"required":             []string{"artifact_id", "query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID    string `json:"artifact_id"`
				Query         string `json:"query"`
				CaseSensitive bool   `json:"case_sensitive,omitempty"`
				Limit         int    `json:"limit,omitempty"`
				MaxFileBytes  int64  `json:"max_file_bytes,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return JSONError("query is required"), nil
			}
			artifact, errOut, ok := requireThreadAttachment(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			maxFileBytes := clampPositiveInt64(in.MaxFileBytes, defaultAttachmentSearchBytes, maxAttachmentSearchBytes)
			body := ""
			truncated := false
			source := "attachment"
			var err error
			if attachmentIsText(artifact) {
				body, truncated, err = readThreadAttachmentContent(exec.CfgDir, artifact, maxFileBytes)
			} else if extraction := attachmentExtraction(artifact); extraction != nil {
				body, truncated, err = readAttachmentExtractionSearchContent(exec.CfgDir, extraction, maxFileBytes)
				source = "extraction"
			} else {
				return JSONError("attachment is binary or not suitable for text search; use inspect_attachment first"), nil
			}
			if err != nil {
				return JSONError(err.Error()), nil
			}

			limit := clampPositiveInt(in.Limit, 20, 100)
			matches := make([]string, 0, limit)
			needle := query
			if !in.CaseSensitive {
				needle = strings.ToLower(query)
			}
			lines := strings.Split(body, "\n")
			for idx, line := range lines {
				haystack := line
				if !in.CaseSensitive {
					haystack = strings.ToLower(line)
				}
				if !strings.Contains(haystack, needle) {
					continue
				}
				matches = append(matches, fmt.Sprintf("Line %d: %s", idx+1, strings.TrimSpace(line)))
				if len(matches) >= limit {
					break
				}
			}

			out := map[string]any{
				"artifact_id":      artifact.ID,
				"file_name":        artifact.Title,
				"query":            query,
				"matches":          matches,
				"matches_count":    len(matches),
				"search_truncated": truncated,
				"source":           source,
			}
			return threadAttachmentOutput("search_attachment", out, artifact, "attachment_search", map[string]any{
				"query":            query,
				"case_sensitive":   in.CaseSensitive,
				"limit":            limit,
				"matches_count":    len(matches),
				"search_truncated": truncated,
				"max_file_bytes":   maxFileBytes,
				"source":           source,
			}), nil
		},
	}
}

func requireThreadAttachment(ctx context.Context, exec ExecContext, artifactID string) (store.Artifact, Output, bool) {
	if exec.Store == nil {
		return store.Artifact{}, JSONError("store is not configured"), false
	}
	threadID := strings.TrimSpace(exec.ThreadID)
	if threadID == "" {
		return store.Artifact{}, JSONError("thread id is required"), false
	}
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return store.Artifact{}, JSONError("artifact_id is required"), false
	}
	artifact, err := exec.Store.GetArtifact(ctx, id)
	if err != nil {
		return store.Artifact{}, JSONError(err.Error()), false
	}
	if !strings.EqualFold(strings.TrimSpace(artifact.ThreadID), threadID) {
		return store.Artifact{}, JSONError("attachment is not available in this thread"), false
	}
	if !fileartifact.IsFileAttachmentArtifact(artifact) {
		return store.Artifact{}, JSONError("artifact is not a thread file attachment"), false
	}
	return artifact, Output{}, true
}

func attachmentIsText(artifact store.Artifact) bool {
	if v, ok := boolMapValue(artifact.Metadata, "isText", "is_text"); ok {
		return v
	}
	return false
}

func attachmentPreviewAvailable(artifact store.Artifact) bool {
	if v, ok := boolMapValue(artifact.Metadata, "previewAvailable", "preview_available"); ok {
		return v
	}
	return strings.TrimSpace(threadAttachmentPreview(artifact)) != ""
}

func attachmentPreviewTruncated(artifact store.Artifact) bool {
	if v, ok := boolMapValue(artifact.Metadata, "previewTruncated", "preview_truncated"); ok {
		return v
	}
	return false
}

func attachmentExtraction(artifact store.Artifact) map[string]any {
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

func compactAttachmentExtraction(extraction map[string]any) map[string]any {
	keys := []string{
		"kind", "pipeline", "version", "status", "rootPath", "manifestPath", "workbookPath", "graphPath",
		"dataPath", "rowsPath", "schemaPath", "markdownPath", "sheetCount", "rowCount", "dataRowCount",
		"columnCount", "tableCount", "formulaCount", "namedRangeCount", "pivotCount", "chartCount",
		"commentCount", "mergedRangeCount", "mediaCount", "hasHiddenSheets", "hasHiddenRowsOrColumns",
		"hasExternalLinks", "hasMacrosOrOle", "cachedValuePolicy", "delimiter", "hasHeader",
		"raggedRowCount", "formulaRiskCellCount", "previewTruncated", "warnings", "capabilities",
		"documentPath", "textPath", "paragraphsPath", "tablesPath", "paragraphCount", "wordCount",
		"characterCount", "headerFooterCount", "relationshipCount", "hasExternalTargets",
		"contentPath", "chunksPath", "structurePath", "lineCount", "chunkCount", "validJson",
		"jsonRootType", "htmlTitle",
		"imagePath", "width", "height", "pixelCount", "animated", "frameCount", "hasAlpha",
		"nativeVisionEligible", "nativeVisionBlockedReason", "ocrStatus", "ocrTextPath",
		"archivePath", "entriesPath", "entryCount", "fileCount", "directoryCount", "symlinkCount",
		"unsafeEntryCount", "totalUncompressedBytes", "totalCompressedBytes", "maxDepth",
		"pagesPath", "pageCount", "encrypted", "linearized", "textExtractionQuality",
		"nativePdfEligible", "title", "author",
	}
	out := map[string]any{}
	for _, key := range keys {
		if value, ok := extraction[key]; ok {
			out[key] = value
		}
	}
	return out
}

func threadAttachmentPreview(artifact store.Artifact) string {
	if preview := strings.TrimSpace(stringMapValue(artifact.Metadata, "previewText", "preview_text")); preview != "" {
		return preview
	}
	return strings.TrimSpace(stringMapValue(artifact.Metadata, "contentNote", "content_note"))
}

func readAttachmentExtractionPage(cfgDir string, extraction map[string]any, offset int64, maxBytes int64) (string, bool, string, error) {
	if offset < 0 {
		return "", false, "", fmt.Errorf("invalid page offset")
	}
	var rel string
	for _, candidate := range []string{
		stringMapValue(extraction, "markdownPath", "markdown_path"),
		stringMapValue(extraction, "textPath", "text_path"),
		stringMapValue(extraction, "contentPath", "content_path"),
		stringMapValue(extraction, "dataPath", "data_path"),
		stringMapValue(extraction, "rowsPath", "rows_path"),
		stringMapValue(extraction, "schemaPath", "schema_path"),
		stringMapValue(extraction, "ocrTextPath", "ocr_text_path"),
		stringMapValue(extraction, "documentPath", "document_path"),
		stringMapValue(extraction, "manifestPath", "manifest_path"),
	} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			rel = candidate
			break
		}
	}
	if rel == "" {
		status := strings.TrimSpace(stringMapValue(extraction, "status"))
		if status == "" {
			status = "unknown"
		}
		return "", false, "", fmt.Errorf("attachment extraction has no readable output path; status=%s", status)
	}
	content, hasMore, err := readRelFilePage(cfgDir, rel, offset, maxBytes)
	return content, hasMore, filepath.ToSlash(rel), err
}

func readAttachmentExtractionSearchContent(cfgDir string, extraction map[string]any, maxBytes int64) (string, bool, error) {
	if maxBytes <= 0 {
		maxBytes = defaultAttachmentSearchBytes
	}
	rels, err := extractionSearchPaths(cfgDir, extraction)
	if err != nil {
		return "", false, err
	}
	if len(rels) == 0 {
		return "", false, fmt.Errorf("attachment extraction has no searchable text outputs")
	}
	var b strings.Builder
	truncated := false
	for _, rel := range rels {
		if int64(b.Len()) > maxBytes {
			truncated = true
			break
		}
		header := "\n\n--- " + rel + " ---\n"
		remaining := maxBytes + 1 - int64(b.Len()+len(header))
		if remaining <= 0 {
			truncated = true
			break
		}
		content, hasMore, err := readRelFilePage(cfgDir, rel, 0, remaining)
		if err != nil {
			continue
		}
		b.WriteString(header)
		b.WriteString(content)
		if hasMore {
			truncated = true
			break
		}
	}
	out := b.String()
	if int64(len(out)) > maxBytes {
		out = out[:maxBytes]
		truncated = true
	}
	return out, truncated, nil
}

func extractionSearchPaths(cfgDir string, extraction map[string]any) ([]string, error) {
	root := strings.TrimSpace(stringMapValue(extraction, "rootPath", "root_path"))
	if root == "" {
		return compactStringList(
			stringMapValue(extraction, "markdownPath", "markdown_path"),
			stringMapValue(extraction, "dataPath", "data_path"),
			stringMapValue(extraction, "rowsPath", "rows_path"),
			stringMapValue(extraction, "schemaPath", "schema_path"),
			stringMapValue(extraction, "manifestPath", "manifest_path"),
		), nil
	}
	cleanRoot, err := cleanRelPath(root)
	if err != nil {
		return nil, err
	}
	fullRoot := filepath.Join(cfgDir, filepath.FromSlash(cleanRoot))
	var rels []string
	err = filepath.WalkDir(fullRoot, func(fullPath string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d == nil {
			return nil
		}
		if d.IsDir() {
			if strings.EqualFold(d.Name(), "raw") {
				return filepath.SkipDir
			}
			return nil
		}
		if !isSearchableExtractionPath(fullPath) {
			return nil
		}
		rel, err := filepath.Rel(cfgDir, fullPath)
		if err != nil {
			return nil
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	return rels, nil
}

func isSearchableExtractionPath(fullPath string) bool {
	switch strings.ToLower(filepath.Ext(fullPath)) {
	case ".md", ".json", ".jsonl", ".txt":
		return true
	default:
		return false
	}
}

func readRelFilePage(cfgDir string, rel string, offset int64, maxBytes int64) (string, bool, error) {
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", false, err
	}
	full := filepath.Join(cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", false, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	hasMore := int64(len(b)) > maxBytes
	if hasMore {
		b = b[:maxBytes]
	}
	return string(b), hasMore, nil
}

func compactStringList(values ...string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(filepath.ToSlash(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func readThreadAttachmentPage(cfgDir string, artifact store.Artifact, offset int64, maxBytes int64) (string, bool, error) {
	if artifact.ContentPath == nil {
		return "", false, fmt.Errorf("attachment content path is missing")
	}
	rel := strings.TrimSpace(*artifact.ContentPath)
	if rel == "" {
		return "", false, fmt.Errorf("attachment content path is missing")
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", false, err
	}
	full := filepath.Join(cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return "", false, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	hasMore := int64(len(b)) > maxBytes
	if hasMore {
		b = b[:maxBytes]
	}
	return string(b), hasMore, nil
}

func readThreadAttachmentContent(cfgDir string, artifact store.Artifact, maxBytes int64) (string, bool, error) {
	if artifact.ContentPath == nil {
		return "", false, fmt.Errorf("attachment content path is missing")
	}
	rel := strings.TrimSpace(*artifact.ContentPath)
	if rel == "" {
		return "", false, fmt.Errorf("attachment content path is missing")
	}
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", false, err
	}
	full := filepath.Join(cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", false, err
	}
	truncated := int64(len(b)) > maxBytes
	if truncated {
		b = b[:maxBytes]
	}
	return string(b), truncated, nil
}

func stringMapValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		if value, ok := raw.(string); ok {
			return value
		}
	}
	return ""
}

func boolMapValue(m map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		value, ok := raw.(bool)
		if ok {
			return value, true
		}
	}
	return false, false
}
