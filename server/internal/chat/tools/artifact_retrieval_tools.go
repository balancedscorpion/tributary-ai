package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultArtifactChunkBytes    = 64 << 10
	maxArtifactChunkBytes        = 256 << 10
	defaultArtifactSearchMatches = 20
	maxArtifactSearchMatches     = 100
	defaultArtifactSearchBytes   = 8 << 20
	maxArtifactSearchBytes       = 32 << 20
	defaultJSONArtifactLimit     = 50
	maxJSONArtifactLimit         = 500
	defaultJSONArtifactBytes     = 32 << 20
	maxJSONArtifactBytes         = 256 << 20
)

func artifactRetrievalResultMetadata(toolName, kind string, a store.Artifact, extra map[string]any) map[string]any {
	toolName = strings.TrimSpace(toolName)
	meta := map[string]any{
		"provider":           "artifact",
		"product":            "artifacts",
		"family":             "artifacts",
		"tool_name":          toolName,
		"kind":               strings.TrimSpace(kind),
		"readonly":           true,
		"artifact_id":        a.ID,
		"source_artifact_id": a.ID,
		"title":              a.Title,
		"artifact_title":     a.Title,
		"type":               a.Type,
		"artifact_type":      a.Type,
		"storage":            a.Storage,
		"byte_size":          a.ByteSize,
	}
	if strings.TrimSpace(a.ThreadID) != "" {
		meta["thread_id"] = a.ThreadID
	}
	if a.ContentPath != nil && strings.TrimSpace(*a.ContentPath) != "" {
		meta["content_path"] = strings.TrimSpace(*a.ContentPath)
	}
	if catalogMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_name"] = catalogMeta.Name
		meta["tool_set_id"] = catalogMeta.ToolSetID
		meta["tool_set_name"] = catalogMeta.ToolSetName
		meta["category"] = string(catalogMeta.Category)
		meta["origin"] = string(catalogMeta.Origin)
		if strings.TrimSpace(catalogMeta.ConnectorID) != "" {
			meta["connector_id"] = catalogMeta.ConnectorID
		}
		presentation := artifactRetrievalPresentationMetadata(catalogMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
		}
		if strings.TrimSpace(catalogMeta.Presentation.Family) != "" {
			meta["product"] = catalogMeta.Presentation.Family
			meta["family"] = catalogMeta.Presentation.Family
		}
		if strings.TrimSpace(catalogMeta.Presentation.ResultKind) != "" {
			meta["result_kind"] = catalogMeta.Presentation.ResultKind
		}
		if strings.TrimSpace(catalogMeta.Presentation.NativeViewer) != "" {
			meta["native_viewer"] = catalogMeta.Presentation.NativeViewer
		}
		if strings.TrimSpace(catalogMeta.Presentation.RendererStrategy) != "" {
			meta["renderer_strategy"] = catalogMeta.Presentation.RendererStrategy
		}
		if strings.TrimSpace(catalogMeta.Presentation.RendererKey) != "" {
			meta["renderer_key"] = catalogMeta.Presentation.RendererKey
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

func artifactRetrievalPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putArtifactRetrievalMetadataString(out, "family", p.Family)
	putArtifactRetrievalMetadataString(out, "result_kind", p.ResultKind)
	putArtifactRetrievalMetadataString(out, "renderer_strategy", p.RendererStrategy)
	putArtifactRetrievalMetadataString(out, "renderer_key", p.RendererKey)
	putArtifactRetrievalMetadataString(out, "native_viewer", p.NativeViewer)
	putArtifactRetrievalMetadataStrings(out, "artifact_types", p.ArtifactTypes)
	putArtifactRetrievalMetadataString(out, "file_capability", p.FileCapability)
	putArtifactRetrievalMetadataStrings(out, "file_kinds", p.FileKinds)
	putArtifactRetrievalMetadataStrings(out, "preview_modes", p.PreviewModes)
	putArtifactRetrievalMetadataStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putArtifactRetrievalMetadataStrings(out, "native_links", p.NativeLinks)
	return out
}

func putArtifactRetrievalMetadataString(out map[string]any, key, value string) {
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	out[key] = value
}

func putArtifactRetrievalMetadataStrings(out map[string]any, key string, values []string) {
	key = strings.TrimSpace(key)
	if key == "" || len(values) == 0 {
		return
	}
	copied := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		copied = append(copied, value)
	}
	if len(copied) > 0 {
		out[key] = copied
	}
}

type artifactContentChunk struct {
	Content       string
	Offset        int64
	BytesReturned int
	ByteSize      int64
	HasMore       bool
}

func newGetArtifactChunkTool(st store.Store, cfgDir string) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_artifact_chunk",
				Description: "Fetch a byte-bounded chunk of artifact content by artifact_id. Use page_token or offset to page large artifacts instead of loading full content.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Artifact ID.",
						},
						"max_bytes": map[string]any{
							"type":        "integer",
							"description": "Max bytes to return for this chunk (default 65536, max 262144).",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "Zero-based byte offset. Ignored when page_token is supplied.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from the previous chunk.",
						},
						"include_image_data": map[string]any{
							"type":        "boolean",
							"description": "When true, allow raw image data URI chunks. Defaults to false.",
						},
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			if st == nil {
				return JSONError("store is not configured"), nil
			}
			var in struct {
				ArtifactID       string `json:"artifact_id"`
				MaxBytes         int64  `json:"max_bytes,omitempty"`
				Offset           int64  `json:"offset,omitempty"`
				PageToken        string `json:"page_token,omitempty"`
				IncludeImageData bool   `json:"include_image_data,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifactID := strings.TrimSpace(in.ArtifactID)
			if artifactID == "" {
				return JSONError("artifact_id is required"), nil
			}
			offset, errOut, ok := artifactOffsetFromInputs(in.Offset, in.PageToken)
			if !ok {
				return errOut, nil
			}
			maxBytes := clampPositiveInt64(in.MaxBytes, defaultArtifactChunkBytes, maxArtifactChunkBytes)
			a, err := st.GetArtifact(ctx, artifactID)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			imageContentOmitted := artifactIsImage(a) && !in.IncludeImageData
			out := sanitizedRetrievedArtifactDescriptor(a)
			out["offset"] = offset
			out["max_bytes"] = maxBytes
			if imageContentOmitted {
				out["content"] = ""
				out["content_size"] = 0
				out["bytes_returned"] = 0
				out["has_more"] = false
				out["image_content_omitted"] = true
				out["content_note"] = "Image data URI omitted by default. Set include_image_data=true only when raw bytes are required."
				return JSONOKWithMetadata(out, sanitizedArtifactRetrievalResultMetadata("get_artifact_chunk", "artifact_chunk", a, map[string]any{
					"offset":                offset,
					"max_bytes":             maxBytes,
					"count":                 0,
					"content_size":          0,
					"bytes_returned":        0,
					"has_more":              false,
					"truncation":            false,
					"image_content_omitted": true,
				})), nil
			}

			chunk, readErr := readRetrievedArtifactContentChunk(cfgDir, a, offset, maxBytes)
			if readErr != nil {
				return JSONError(readErr.Error()), nil
			}
			content := chunk.Content
			if offset == 0 && !chunk.HasMore && a.ToolName != nil && isRetrievedQueryLifecycleToolName(*a.ToolName) && retrievedTextHasQueryLifecycleEnvelope(content) {
				content = sanitizeRetrievedQueryRetryText(content, false, true)
			}
			out["content"] = content
			out["content_size"] = len(content)
			out["bytes_returned"] = chunk.BytesReturned
			out["byte_size"] = chunk.ByteSize
			out["has_more"] = chunk.HasMore
			if chunk.HasMore {
				out["next_page_token"] = encodePageOffsetToken(chunk.Offset + int64(chunk.BytesReturned))
			}
			return JSONOKWithMetadata(out, sanitizedArtifactRetrievalResultMetadata("get_artifact_chunk", "artifact_chunk", a, map[string]any{
				"offset":         offset,
				"max_bytes":      maxBytes,
				"count":          chunk.BytesReturned,
				"content_size":   len(content),
				"bytes_returned": chunk.BytesReturned,
				"has_more":       chunk.HasMore,
				"truncation":     chunk.HasMore,
				"byte_size":      chunk.ByteSize,
			})), nil
		},
	}
}

func newSearchArtifactContentTool(st store.Store, cfgDir string) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "search_artifact_content",
				Description: "Search text inside a persisted artifact and return compact line snippets. Use this before fetching focused chunks from large artifacts.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Artifact ID.",
						},
						"query": map[string]any{
							"type":        "string",
							"description": "Text to search for.",
						},
						"case_sensitive": map[string]any{
							"type":        "boolean",
							"description": "Whether matching is case sensitive. Default false.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum matching lines to return (default 20, max 100).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from a previous search_artifact_content call.",
						},
						"max_bytes": map[string]any{
							"type":        "integer",
							"description": "Maximum artifact bytes to scan from the beginning (default 8 MiB, max 32 MiB).",
						},
					},
					"required":             []string{"artifact_id", "query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			if st == nil {
				return JSONError("store is not configured"), nil
			}
			var in struct {
				ArtifactID    string `json:"artifact_id"`
				Query         string `json:"query"`
				CaseSensitive bool   `json:"case_sensitive,omitempty"`
				Limit         int    `json:"limit,omitempty"`
				PageToken     string `json:"page_token,omitempty"`
				MaxBytes      int64  `json:"max_bytes,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifactID := strings.TrimSpace(in.ArtifactID)
			query := strings.TrimSpace(in.Query)
			if artifactID == "" {
				return JSONError("artifact_id is required"), nil
			}
			if query == "" {
				return JSONError("query is required"), nil
			}
			startMatch, ok := parseSearchPageToken(in.PageToken)
			if !ok {
				return JSONError("Invalid page_token"), nil
			}
			limit := clampPositiveInt(in.Limit, defaultArtifactSearchMatches, maxArtifactSearchMatches)
			maxBytes := clampPositiveInt64(in.MaxBytes, defaultArtifactSearchBytes, maxArtifactSearchBytes)
			a, err := st.GetArtifact(ctx, artifactID)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if artifactIsImage(a) {
				return JSONError("image artifact content is not searchable as text"), nil
			}
			contentChunk, readErr := readRetrievedArtifactContentChunk(cfgDir, a, 0, maxBytes)
			if readErr != nil {
				return JSONError(readErr.Error()), nil
			}
			content := contentChunk.Content
			truncated := contentChunk.HasMore
			byteSize := contentChunk.ByteSize
			matches, totalScanned, nextToken := searchArtifactContentLines(content, query, in.CaseSensitive, int(startMatch), limit)
			out := sanitizedRetrievedArtifactDescriptor(a)
			out["query"] = query
			out["matches"] = matches
			out["count"] = len(matches)
			out["total_matches_scanned"] = totalScanned
			out["offset"] = startMatch
			out["page_size"] = limit
			out["has_more"] = nextToken != "" || truncated
			out["max_bytes_searched"] = maxBytes
			out["bytes_scanned"] = minInt64(int64(len(content)), maxBytes)
			out["byte_size"] = byteSize
			out["content_truncated"] = truncated
			if nextToken != "" {
				out["next_page_token"] = nextToken
			}
			if truncated {
				out["note"] = "Search scanned only the configured byte prefix. Increase max_bytes or use get_artifact_chunk for later content."
			}
			return JSONOKWithMetadata(out, sanitizedArtifactRetrievalResultMetadata("search_artifact_content", "artifact_search", a, map[string]any{
				"query":                 query,
				"offset":                startMatch,
				"page_size":             limit,
				"count":                 len(matches),
				"total_matches_scanned": totalScanned,
				"has_more":              nextToken != "" || truncated,
				"truncation":            nextToken != "" || truncated,
				"content_truncated":     truncated,
				"max_bytes_searched":    maxBytes,
				"bytes_scanned":         minInt64(int64(len(content)), maxBytes),
				"byte_size":             byteSize,
			})), nil
		},
	}
}

func newGetJSONArtifactTool(st store.Store, cfgDir string) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_json_artifact",
				Description: "Parse a JSON artifact and return a compact selected value or a paged array slice. Use path for simple dot-separated object keys or array indexes.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Artifact ID.",
						},
						"path": map[string]any{
							"type":        "string",
							"description": "Optional simple dot path to select before paging, for example rows or data.items. Array indexes are numeric segments.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Array items to return (default 50, max 500).",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "Zero-based array offset. Ignored when page_token is supplied.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned by a previous get_json_artifact call.",
						},
						"columns": map[string]any{
							"type":        "array",
							"description": "Optional projection when selected array items are objects.",
							"items":       map[string]any{"type": "string"},
						},
						"max_bytes": map[string]any{
							"type":        "integer",
							"description": "Maximum artifact bytes to parse (default 32 MiB, max 256 MiB).",
						},
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, _ ExecContext, args json.RawMessage) (Output, error) {
			if st == nil {
				return JSONError("store is not configured"), nil
			}
			var in struct {
				ArtifactID string   `json:"artifact_id"`
				Path       string   `json:"path,omitempty"`
				Limit      int      `json:"limit,omitempty"`
				Offset     int64    `json:"offset,omitempty"`
				PageToken  string   `json:"page_token,omitempty"`
				Columns    []string `json:"columns,omitempty"`
				MaxBytes   int64    `json:"max_bytes,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifactID := strings.TrimSpace(in.ArtifactID)
			if artifactID == "" {
				return JSONError("artifact_id is required"), nil
			}
			offset, errOut, ok := artifactOffsetFromInputs(in.Offset, in.PageToken)
			if !ok {
				return errOut, nil
			}
			limit := clampPositiveInt(in.Limit, defaultJSONArtifactLimit, maxJSONArtifactLimit)
			maxBytes := clampPositiveInt64(in.MaxBytes, defaultJSONArtifactBytes, maxJSONArtifactBytes)
			a, err := st.GetArtifact(ctx, artifactID)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if artifactIsImage(a) {
				return JSONError("image artifact content is not JSON-readable text"), nil
			}
			content, truncated, byteSize, readErr := readArtifactContentPrefix(cfgDir, a, maxBytes)
			if readErr != nil {
				return JSONError(readErr.Error()), nil
			}
			var parsed any
			if err := json.Unmarshal([]byte(content), &parsed); err != nil {
				if truncated {
					return JSONError("artifact JSON exceeds max_bytes before a complete JSON value could be parsed"), nil
				}
				return JSONError("artifact content is not valid JSON"), nil
			}
			stripLegacyTargets := a.ToolName != nil && isRetrievedQueryLifecycleToolName(*a.ToolName) && retrievedTextHasQueryLifecycleEnvelope(content)
			parsed = sanitizeRetrievedQueryRetryValue(parsed, false, stripLegacyTargets)
			selected, err := selectSimpleJSONPath(parsed, in.Path)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			out := sanitizedRetrievedArtifactDescriptor(a)
			out["path"] = strings.TrimSpace(in.Path)
			out["max_bytes_parsed"] = maxBytes
			out["byte_size"] = byteSize
			out["content_truncated"] = truncated
			if err := appendJSONSelectionResult(out, selected, offset, limit, normalizeDatasetColumns(in.Columns)); err != nil {
				return JSONError(err.Error()), nil
			}
			return JSONOKWithMetadata(out, sanitizedArtifactRetrievalResultMetadata("get_json_artifact", "json_artifact", a, jsonArtifactRetrievalMetadata(out, strings.TrimSpace(in.Path), offset, limit, maxBytes, truncated))), nil
		},
	}
}

func jsonArtifactRetrievalMetadata(result map[string]any, path string, offset int64, limit int, maxBytes int64, truncated bool) map[string]any {
	extra := map[string]any{
		"path":              path,
		"offset":            offset,
		"page_size":         limit,
		"max_bytes_parsed":  maxBytes,
		"content_truncated": truncated,
		"truncation":        truncated,
	}
	if value, ok := result["byte_size"]; ok {
		extra["byte_size"] = value
	}
	if value, ok := result["kind"]; ok {
		extra["selection_kind"] = value
	}
	if value, ok := result["has_more"]; ok {
		extra["has_more"] = value
		if hasMore, ok := value.(bool); ok && hasMore {
			extra["truncation"] = true
		}
	}
	if value, ok := result["items_returned"]; ok {
		extra["count"] = value
	} else if value, ok := result["key_count"]; ok {
		extra["count"] = value
	} else {
		extra["count"] = 1
	}
	if value, ok := result["total_count"]; ok {
		extra["total_count"] = value
	}
	if value, ok := result["columns"]; ok {
		extra["columns"] = value
	}
	return extra
}

func artifactOffsetFromInputs(offset int64, pageToken string) (int64, Output, bool) {
	if tokenOffset, tokenOK := parsePageOffsetToken(pageToken); tokenOK {
		return tokenOffset, Output{}, true
	}
	if strings.TrimSpace(pageToken) != "" {
		return 0, JSONError("Invalid page_token"), false
	}
	if offset < 0 {
		return 0, JSONError("offset must be >= 0"), false
	}
	return offset, Output{}, true
}

func artifactDescriptorMap(a store.Artifact) map[string]any {
	out := map[string]any{
		"id":        a.ID,
		"thread_id": a.ThreadID,
		"type":      a.Type,
		"title":     a.Title,
		"storage":   a.Storage,
		"byte_size": a.ByteSize,
		"metadata":  a.Metadata,
	}
	if a.MessageID != nil {
		out["message_id"] = *a.MessageID
	}
	if a.ToolName != nil {
		out["tool_name"] = *a.ToolName
	}
	if a.ToolCallID != nil {
		out["tool_call_id"] = *a.ToolCallID
	}
	if a.OK != nil {
		out["ok"] = *a.OK
	}
	if a.Language != nil {
		out["language"] = *a.Language
	}
	return out
}

func sanitizedRetrievedArtifactDescriptor(a store.Artifact) map[string]any {
	stripLegacyTargets := a.ToolName != nil && isRetrievedQueryLifecycleToolName(*a.ToolName)
	descriptor := artifactDescriptorMap(a)
	if encoded, err := json.Marshal(descriptor); err == nil {
		var generic map[string]any
		if json.Unmarshal(encoded, &generic) == nil {
			descriptor = generic
		}
	}
	clean, _ := sanitizeRetrievedQueryRetryValue(descriptor, false, stripLegacyTargets).(map[string]any)
	if clean == nil {
		return map[string]any{}
	}
	return clean
}

func sanitizedArtifactRetrievalResultMetadata(toolName, kind string, a store.Artifact, extra map[string]any) map[string]any {
	clean, _ := sanitizeRetrievedQueryRetryValue(artifactRetrievalResultMetadata(toolName, kind, a, extra), false, false).(map[string]any)
	if clean == nil {
		return map[string]any{}
	}
	return clean
}

func readArtifactContentChunk(cfgDir string, a store.Artifact, offset int64, maxBytes int64) (artifactContentChunk, error) {
	if offset < 0 {
		return artifactContentChunk{}, errors.New("offset must be >= 0")
	}
	if maxBytes <= 0 {
		maxBytes = defaultArtifactChunkBytes
	}
	switch strings.TrimSpace(strings.ToLower(a.Storage)) {
	case "file":
		if a.ContentPath == nil || strings.TrimSpace(*a.ContentPath) == "" {
			return artifactContentChunk{Offset: offset, ByteSize: a.ByteSize}, nil
		}
		clean, err := cleanRelPath(strings.TrimSpace(*a.ContentPath))
		if err != nil {
			return artifactContentChunk{}, err
		}
		full := filepath.Join(cfgDir, filepath.FromSlash(clean))
		f, err := os.Open(full)
		if err != nil {
			return artifactContentChunk{}, err
		}
		defer f.Close()
		size := a.ByteSize
		if st, statErr := f.Stat(); statErr == nil {
			size = st.Size()
		}
		if offset > size {
			return artifactContentChunk{}, errors.New("offset out of range")
		}
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return artifactContentChunk{}, err
		}
		b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
		if err != nil {
			return artifactContentChunk{}, err
		}
		hasMore := int64(len(b)) > maxBytes
		if hasMore {
			b = b[:maxBytes]
		}
		if !hasMore && size > 0 && offset+int64(len(b)) < size {
			hasMore = true
		}
		return artifactContentChunk{
			Content:       string(b),
			Offset:        offset,
			BytesReturned: len(b),
			ByteSize:      size,
			HasMore:       hasMore,
		}, nil
	default:
		content := ""
		if a.ContentText != nil {
			content = *a.ContentText
		}
		data := []byte(content)
		size := int64(len(data))
		if offset > size {
			return artifactContentChunk{}, errors.New("offset out of range")
		}
		end := offset + maxBytes
		if end < offset || end > size {
			end = size
		}
		selected := data[offset:end]
		return artifactContentChunk{
			Content:       string(selected),
			Offset:        offset,
			BytesReturned: len(selected),
			ByteSize:      size,
			HasMore:       end < size,
		}, nil
	}
}

func readArtifactContentPrefix(cfgDir string, a store.Artifact, maxBytes int64) (string, bool, int64, error) {
	chunk, err := readArtifactContentChunk(cfgDir, a, 0, maxBytes)
	if err != nil {
		return "", false, 0, err
	}
	return chunk.Content, chunk.HasMore, chunk.ByteSize, nil
}

func artifactIsImage(a store.Artifact) bool {
	return strings.EqualFold(strings.TrimSpace(a.Type), "image")
}

func parseSearchPageToken(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	return parsePageOffsetToken(raw)
}

func searchArtifactContentLines(content string, query string, caseSensitive bool, startMatch int, limit int) ([]map[string]any, int, string) {
	if startMatch < 0 {
		startMatch = 0
	}
	if limit <= 0 {
		limit = defaultArtifactSearchMatches
	}
	haystackQuery := query
	if !caseSensitive {
		haystackQuery = strings.ToLower(query)
	}
	lines := strings.SplitAfter(content, "\n")
	matches := make([]map[string]any, 0, limit)
	total := 0
	byteOffset := 0
	for lineIndex, rawLine := range lines {
		line := strings.TrimRight(rawLine, "\r\n")
		haystackLine := line
		if !caseSensitive {
			haystackLine = strings.ToLower(line)
		}
		if strings.Contains(haystackLine, haystackQuery) {
			total++
			if total > startMatch && len(matches) < limit {
				matches = append(matches, map[string]any{
					"line":        lineIndex + 1,
					"byte_offset": byteOffset,
					"snippet":     truncateRunes(line, 500),
				})
			}
		}
		byteOffset += len(rawLine)
	}
	nextToken := ""
	if total > startMatch+len(matches) {
		nextToken = encodePageOffsetToken(int64(startMatch + len(matches)))
	}
	return matches, total, nextToken
}

func selectSimpleJSONPath(root any, rawPath string) (any, error) {
	rawPath = strings.TrimSpace(rawPath)
	if rawPath == "" {
		return root, nil
	}
	current := root
	for _, rawPart := range strings.Split(rawPath, ".") {
		part := strings.TrimSpace(rawPart)
		if part == "" {
			return nil, fmt.Errorf("invalid path %q", rawPath)
		}
		switch v := current.(type) {
		case map[string]any:
			next, ok := v[part]
			if !ok {
				return nil, fmt.Errorf("path segment %q not found", part)
			}
			current = next
		case []any:
			idx, err := strconv.Atoi(part)
			if err != nil || idx < 0 || idx >= len(v) {
				return nil, fmt.Errorf("array path segment %q out of range", part)
			}
			current = v[idx]
		default:
			return nil, fmt.Errorf("path segment %q cannot be selected from %T", part, current)
		}
	}
	return current, nil
}

func appendJSONSelectionResult(out map[string]any, selected any, offset int64, limit int, columns []string) error {
	switch v := selected.(type) {
	case []any:
		total := len(v)
		if offset > int64(total) {
			return errors.New("offset out of range")
		}
		start := int(offset)
		end := start + limit
		if end > total {
			end = total
		}
		items := projectJSONItems(v[start:end], columns)
		out["kind"] = "array"
		out["items"] = items
		out["total_count"] = total
		out["items_returned"] = len(items)
		out["offset"] = offset
		out["page_size"] = limit
		out["has_more"] = end < total
		if len(columns) > 0 {
			out["columns"] = columns
		}
		if end < total {
			out["next_page_token"] = encodePageOffsetToken(int64(end))
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out["kind"] = "object"
		out["keys"] = keys
		out["key_count"] = len(keys)
		if encoded, err := json.Marshal(v); err == nil && len(encoded) <= defaultArtifactChunkBytes {
			out["value"] = v
		} else {
			out["note"] = "Object value omitted because it is too large. Use path to select a specific key."
		}
	default:
		out["kind"] = "scalar"
		out["value"] = selected
	}
	return nil
}

func projectJSONItems(items []any, columns []string) []any {
	if len(columns) == 0 {
		return items
	}
	out := make([]any, 0, len(items))
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			out = append(out, item)
			continue
		}
		projected := map[string]any{}
		for _, col := range columns {
			if value, exists := row[col]; exists {
				projected[col] = value
			}
		}
		out = append(out, projected)
	}
	return out
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
