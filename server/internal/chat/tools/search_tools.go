package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/messagepaging"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

var threadSearchToolNameSet = map[string]struct{}{
	"search_threads": {},
	"search_docs":    {},
	"get_doc":        {},
	"get_artifact":   {},
	"list_messages":  {},
	"rlm_exec":       {},
}

func IsThreadSearchToolName(toolName string) bool {
	_, ok := threadSearchToolNameSet[strings.TrimSpace(toolName)]
	return ok
}

func threadSearchOutput(toolName, kind string, result map[string]any, extra map[string]any) Output {
	return JSONOKWithMetadata(result, threadSearchResultMetadata(toolName, kind, extra))
}

func threadSearchResultMetadata(toolName, kind string, extra map[string]any) map[string]any {
	toolName = strings.TrimSpace(toolName)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "thread_search_result"
	}
	meta := map[string]any{
		"provider":  "thread-search",
		"product":   "thread-search",
		"family":    "thread-search",
		"tool_name": toolName,
		"kind":      kind,
		"readonly":  true,
	}
	catalogMeta, ok := threadSearchCatalogMetadata(toolName)
	if ok {
		meta["tool_set_id"] = catalogMeta.ToolSetID
		meta["tool_set_name"] = catalogMeta.ToolSetName
		meta["connector_id"] = catalogMeta.ConnectorID
		meta["category"] = string(catalogMeta.Category)
		meta["origin"] = string(catalogMeta.Origin)
		meta["readonly"] = catalogMeta.Category == ToolCategoryRead
		presentation := threadSearchPresentationMetadata(catalogMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
			if v := firstThreadSearchMetadataString(presentation, "family"); v != "" {
				meta["family"] = v
				meta["product"] = v
			}
			if v := firstThreadSearchMetadataString(presentation, "result_kind"); v != "" {
				meta["result_kind"] = v
			}
			if v := firstThreadSearchMetadataString(presentation, "native_viewer"); v != "" {
				meta["native_viewer"] = v
			}
			if v := firstThreadSearchMetadataString(presentation, "renderer_strategy"); v != "" {
				meta["renderer_strategy"] = v
			}
			if v := firstThreadSearchMetadataString(presentation, "renderer_key"); v != "" {
				meta["renderer_key"] = v
			}
			if v := firstThreadSearchMetadataString(presentation, "file_capability"); v != "" {
				meta["file_capability"] = v
			}
		}
		if toolName == "search_index" {
			meta["canonical_tool_name"] = catalogMeta.Name
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

func threadSearchCatalogMetadata(toolName string) (ToolMetadata, bool) {
	toolName = strings.TrimSpace(toolName)
	if meta, ok := ToolMetadataByName(toolName); ok {
		return meta, true
	}
	if toolName == "search_index" {
		return ToolMetadataByName("search_threads")
	}
	return ToolMetadata{}, false
}

func threadSearchPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putThreadSearchPresentationString(out, "family", p.Family)
	putThreadSearchPresentationString(out, "result_kind", p.ResultKind)
	putThreadSearchPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putThreadSearchPresentationString(out, "renderer_key", p.RendererKey)
	putThreadSearchPresentationString(out, "native_viewer", p.NativeViewer)
	putThreadSearchPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putThreadSearchPresentationString(out, "file_capability", p.FileCapability)
	putThreadSearchPresentationStrings(out, "file_kinds", p.FileKinds)
	putThreadSearchPresentationStrings(out, "preview_modes", p.PreviewModes)
	putThreadSearchPresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putThreadSearchPresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putThreadSearchPresentationString(out map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putThreadSearchPresentationStrings(out map[string]any, key string, values []string) {
	if len(values) == 0 {
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

func firstThreadSearchMetadataString(m map[string]any, key string) string {
	if len(m) == 0 {
		return ""
	}
	value, _ := m[strings.TrimSpace(key)].(string)
	return strings.TrimSpace(value)
}

func newSearchDocsTool(svc *search.Service) Tool {
	return newSearchIndexTool(svc, searchIndexToolConfig{
		Name:                "search_docs",
		Description:         "Legacy alias for search_threads. Search indexed conversation, turn, and artifact documents and return ranked metadata-only hits.",
		PrimaryQueryField:   "q",
		QueryDescription:    "Search query text.",
		IncludeFilesDefault: false,
		DefaultStatus:       "",
	})
}

func newSearchThreadsTool(svc *search.Service) Tool {
	return newSearchIndexTool(svc, searchIndexToolConfig{
		Name:                "search_threads",
		Description:         "Search all saved conversation history, thread summaries, generated artifacts, and file-attachment excerpts. When a project is attached, also returns matching current project files. This searches local app history/files, not the internet.",
		PrimaryQueryField:   "query",
		QueryDescription:    "Search query text. Use this for keywords, phrases, prior thread topics, artifact names, or file content.",
		IncludeFilesDefault: true,
		DefaultStatus:       "*",
	})
}

type searchIndexToolConfig struct {
	Name                string
	Description         string
	PrimaryQueryField   string
	QueryDescription    string
	IncludeFilesDefault bool
	DefaultStatus       string
}

func newSearchIndexTool(svc *search.Service, cfg searchIndexToolConfig) Tool {
	toolName := strings.TrimSpace(cfg.Name)
	if toolName == "" {
		toolName = "search_threads"
	}
	queryField := strings.TrimSpace(cfg.PrimaryQueryField)
	if queryField == "" {
		queryField = "query"
	}
	queryDescription := strings.TrimSpace(cfg.QueryDescription)
	if queryDescription == "" {
		queryDescription = "Search query text."
	}
	statusDescription := "Thread status filter: active, archived, or *."
	if strings.TrimSpace(cfg.DefaultStatus) == "*" {
		statusDescription = "Thread status filter: active, archived, or *. Defaults to * so all saved threads are searched."
	}
	params := map[string]any{
		queryField: map[string]any{
			"type":        "string",
			"description": queryDescription,
		},
		"limit": map[string]any{
			"type":        "integer",
			"description": "Maximum number of conversation/artifact results (default 12, max 50).",
		},
		"status": map[string]any{
			"type":        "string",
			"description": statusDescription,
		},
		"project_id": map[string]any{
			"type":        "string",
			"description": "Optional project filter for indexed conversation/artifact results.",
		},
		"thread_ids": map[string]any{
			"type":        "array",
			"description": "Optional list of thread IDs to scope conversation results.",
			"items": map[string]any{
				"type": "string",
			},
		},
		"artifact_ids": map[string]any{
			"type":        "array",
			"description": "Optional list of artifact IDs to scope conversation/artifact results.",
			"items": map[string]any{
				"type": "string",
			},
		},
		"message_ids": map[string]any{
			"type":        "array",
			"description": "Optional list of message IDs to scope conversation results.",
			"items": map[string]any{
				"type": "string",
			},
		},
		"before_ts": map[string]any{
			"type":        "string",
			"description": "Upper bound (RFC3339) on document update time.",
		},
		"after_ts": map[string]any{
			"type":        "string",
			"description": "Lower bound (RFC3339) on document update time.",
		},
		"order_by": map[string]any{
			"type":        "string",
			"description": "Deterministic ordering: score_desc, updated_at_desc, updated_at_asc.",
		},
		"min_score": map[string]any{
			"type":        "number",
			"description": "Minimum score threshold.",
		},
		"max_chars_per_result": map[string]any{
			"type":        "integer",
			"description": "Max characters per conversation/artifact snippet (default 320, max 500).",
		},
	}
	if queryField != "query" {
		params["query"] = map[string]any{
			"type":        "string",
			"description": "Alias for q.",
		}
	}
	if queryField != "q" {
		params["q"] = map[string]any{
			"type":        "string",
			"description": "Legacy alias for query.",
		}
		params["include_files"] = map[string]any{
			"type":        "boolean",
			"description": "Whether to also search files in the current attached project when available (default true for search_threads).",
		}
		params["file_limit"] = map[string]any{
			"type":        "integer",
			"description": "Maximum current-project file matches to return (default 20, max 100).",
		}
		params["max_file_bytes"] = map[string]any{
			"type":        "integer",
			"description": "Per-file read cap for current-project file search (default 32 KiB, max 256 KiB).",
		}
	}

	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        toolName,
				Description: strings.TrimSpace(cfg.Description),
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           params,
					"required":             []string{queryField},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				Query          string   `json:"q"`
				QueryAlias     string   `json:"query,omitempty"`
				Limit          int      `json:"limit,omitempty"`
				Status         string   `json:"status,omitempty"`
				ProjectID      string   `json:"project_id,omitempty"`
				ThreadIDs      []string `json:"thread_ids,omitempty"`
				ArtifactIDs    []string `json:"artifact_ids,omitempty"`
				MessageIDs     []string `json:"message_ids,omitempty"`
				BeforeTS       *string  `json:"before_ts,omitempty"`
				AfterTS        *string  `json:"after_ts,omitempty"`
				MinScore       float64  `json:"min_score,omitempty"`
				OrderBy        string   `json:"order_by,omitempty"`
				MaxCharsResult int      `json:"max_chars_per_result,omitempty"`
				IncludeFiles   *bool    `json:"include_files,omitempty"`
				FileLimit      int      `json:"file_limit,omitempty"`
				MaxFileBytes   int64    `json:"max_file_bytes,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				query = strings.TrimSpace(in.QueryAlias)
			}
			if query == "" {
				return JSONError(queryField + " is required"), nil
			}
			if in.MinScore < 0 {
				return JSONError("min_score must be >= 0"), nil
			}
			if in.MinScore > 1 {
				return JSONError("min_score must be <= 1"), nil
			}
			status := strings.ToLower(strings.TrimSpace(in.Status))
			if status == "" {
				status = strings.TrimSpace(strings.ToLower(cfg.DefaultStatus))
			}
			if status != "" && status != "active" && status != "archived" && status != "*" {
				return JSONError("status must be one of: active, archived, *"), nil
			}
			orderBy := normalizeSearchDocsOrderBy(strings.TrimSpace(in.OrderBy))
			if orderBy == "" && strings.TrimSpace(in.OrderBy) != "" {
				return JSONError("order_by must be one of: score_desc, updated_at_desc, updated_at_asc"), nil
			}
			maxChars := in.MaxCharsResult
			if maxChars < 0 {
				return JSONError("max_chars_per_result must be >= 0"), nil
			}
			if maxChars > 500 {
				return JSONError("max_chars_per_result must be <= 500"), nil
			}
			if maxChars == 0 {
				maxChars = 320
			}
			threadIDs := dedupeStringSlice(in.ThreadIDs)
			if len(threadIDs) > 100 {
				return JSONError("thread_ids length must be <= 100"), nil
			}
			artifactIDs := dedupeStringSlice(in.ArtifactIDs)
			if len(artifactIDs) > 100 {
				return JSONError("artifact_ids length must be <= 100"), nil
			}
			messageIDs := dedupeStringSlice(in.MessageIDs)
			if len(messageIDs) > 100 {
				return JSONError("message_ids length must be <= 100"), nil
			}
			beforeTS := parseTimeFromString(in.BeforeTS)
			if in.BeforeTS != nil && strings.TrimSpace(*in.BeforeTS) != "" && beforeTS == nil {
				return JSONError("before_ts must be RFC3339 timestamp"), nil
			}
			afterTS := parseTimeFromString(in.AfterTS)
			if in.AfterTS != nil && strings.TrimSpace(*in.AfterTS) != "" && afterTS == nil {
				return JSONError("after_ts must be RFC3339 timestamp"), nil
			}
			if beforeTS != nil && afterTS != nil && beforeTS.Before(*afterTS) {
				return JSONError("before_ts must be greater than or equal to after_ts"), nil
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 12
			}
			if limit > 50 {
				return JSONError("limit must be <= 50"), nil
			}
			if svc == nil {
				return JSONError("search service is not configured"), nil
			}
			intent, results, err := svc.Search(ctx, search.Query{
				Text:        query,
				Limit:       limit,
				Status:      status,
				ProjectID:   strings.TrimSpace(in.ProjectID),
				ThreadIDs:   threadIDs,
				ArtifactIDs: artifactIDs,
				MessageIDs:  messageIDs,
				BeforeTS:    beforeTS,
				AfterTS:     afterTS,
				MinScore:    in.MinScore,
				OrderBy:     orderBy,
				Intent:      "keyword",
			})
			if err != nil {
				return JSONError(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(results))
			for _, r := range results {
				currentRunProvenance := retrievedQueryRetryRunMatches(r.RunID, exec.RunID)
				stripLegacyTargets := false
				if sourceDoc, found := svc.GetDoc(r.DocID); found {
					stripLegacyTargets = retrievedSearchDocHasQueryLifecycle(sourceDoc.Text)
				}
				allowCurrentRunObligation := currentRunProvenance && stripLegacyTargets
				snippet := truncateRunes(sanitizeRetrievedQueryRetryText(r.Snippet, allowCurrentRunObligation, stripLegacyTargets), maxChars)
				row := map[string]any{
					"doc_id":     r.DocID,
					"doc_kind":   r.DocKind,
					"thread_id":  r.ThreadID,
					"title":      sanitizeRetrievedQueryRetryText(r.Title, allowCurrentRunObligation, stripLegacyTargets),
					"snippet":    snippet,
					"score":      r.Score,
					"updated_at": r.UpdatedAt,
				}
				if r.RunID != nil {
					row["run_id"] = *r.RunID
				}
				if r.MessageID != nil {
					row["message_id"] = *r.MessageID
				}
				if r.ArtifactID != nil {
					row["artifact_id"] = *r.ArtifactID
				}
				out = append(out, row)
			}
			result := map[string]any{
				"intent":  intent,
				"results": out,
				"count":   len(out),
			}
			includeFiles := shouldIncludeSearchThreadFiles(cfg.IncludeFilesDefault, in.IncludeFiles)
			fileSearchCount := 0
			fileSearchTruncated := false
			if includeFiles {
				fileSearch := runSearchThreadsFileSearch(ctx, exec, query, in.FileLimit, in.MaxFileBytes)
				result["file_search"] = fileSearch
				if count, ok := fileSearch["count"].(int); ok {
					fileSearchCount = count
				}
				if truncated, ok := fileSearch["truncated"].(bool); ok {
					fileSearchTruncated = truncated
				}
			}
			meta := map[string]any{
				"query":                query,
				"count":                len(out),
				"limit":                limit,
				"page_size":            limit,
				"status":               status,
				"order_by":             orderBy,
				"min_score":            in.MinScore,
				"max_chars_per_result": maxChars,
				"include_files":        includeFiles,
				"file_count":           fileSearchCount,
				"truncation":           len(out) >= limit || fileSearchTruncated,
				"limit_reached":        len(out) >= limit,
				"file_truncated":       fileSearchTruncated,
			}
			projectID := strings.TrimSpace(in.ProjectID)
			if projectID == "" {
				projectID = strings.TrimSpace(exec.ProjectID)
			}
			if projectID != "" {
				meta["project_id"] = projectID
			}
			if len(threadIDs) > 0 {
				meta["thread_ids"] = threadIDs
				meta["thread_filter_count"] = len(threadIDs)
			}
			if len(artifactIDs) > 0 {
				meta["artifact_ids"] = artifactIDs
				meta["artifact_filter_count"] = len(artifactIDs)
			}
			if len(messageIDs) > 0 {
				meta["message_ids"] = messageIDs
				meta["message_filter_count"] = len(messageIDs)
			}
			if in.BeforeTS != nil && strings.TrimSpace(*in.BeforeTS) != "" {
				meta["before_ts"] = strings.TrimSpace(*in.BeforeTS)
			}
			if in.AfterTS != nil && strings.TrimSpace(*in.AfterTS) != "" {
				meta["after_ts"] = strings.TrimSpace(*in.AfterTS)
			}
			return threadSearchOutput(toolName, "search_index", result, meta), nil
		},
	}
}

func shouldIncludeSearchThreadFiles(defaultValue bool, override *bool) bool {
	if override != nil {
		return *override
	}
	return defaultValue
}

func runSearchThreadsFileSearch(ctx context.Context, exec ExecContext, query string, limit int, maxFileBytes int64) map[string]any {
	projectID := strings.TrimSpace(exec.ProjectID)
	out := map[string]any{
		"included": true,
		"matches":  []map[string]any{},
		"count":    0,
	}
	if exec.Projects == nil || projectID == "" {
		out["available"] = false
		out["reason"] = "No current project files are attached to this thread."
		return out
	}

	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if maxFileBytes <= 0 {
		maxFileBytes = int64(defaultReadFileBytes)
	}
	if maxFileBytes > int64(maxReadFileBytes) {
		maxFileBytes = int64(maxReadFileBytes)
	}

	res, err := exec.Projects.SearchFiles(ctx, projectID, query, projects.SearchOptions{
		MaxMatches:   limit,
		MaxFileBytes: maxFileBytes,
	})
	if err != nil {
		out["available"] = false
		if errors.Is(err, fs.ErrNotExist) {
			out["reason"] = "No current project files are attached to this thread."
		} else {
			out["error"] = err.Error()
		}
		return out
	}

	matches := flattenProjectFileMatches(res.Matches, limit)
	out["available"] = true
	out["project_id"] = projectID
	out["matches"] = matches
	out["count"] = len(matches)
	out["truncated"] = res.Truncated
	out["max_file_bytes"] = maxFileBytes
	return out
}

func flattenProjectFileMatches(grouped map[string][]string, limit int) []map[string]any {
	if len(grouped) == 0 || limit <= 0 {
		return []map[string]any{}
	}
	paths := make([]string, 0, len(grouped))
	for p := range grouped {
		if strings.TrimSpace(p) == "" {
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]map[string]any, 0)
	for _, p := range paths {
		for _, rawLine := range grouped[p] {
			if limit > 0 && len(out) >= limit {
				return out
			}
			lineNo, text := parseProjectSearchLine(rawLine)
			row := map[string]any{
				"path":    p,
				"snippet": text,
			}
			if lineNo > 0 {
				row["line"] = lineNo
			}
			out = append(out, row)
		}
	}
	return out
}

func parseProjectSearchLine(raw string) (int, string) {
	line := strings.TrimSpace(raw)
	if line == "" {
		return 0, ""
	}
	const prefix = "Line "
	if !strings.HasPrefix(line, prefix) {
		return 0, line
	}
	rest := strings.TrimPrefix(line, prefix)
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return 0, line
	}
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return 0, line
	}
	return n, strings.TrimSpace(parts[1])
}

func newGetDocTool(svc *search.Service) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_doc",
				Description: "Fetch a specific indexed search document by doc_id.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"doc_id": map[string]any{
							"type":        "string",
							"description": "Document ID from search_threads or search_docs results.",
						},
						"max_chars": map[string]any{
							"type":        "integer",
							"description": "Max text characters to return (default 4000, max 20000).",
						},
					},
					"required":             []string{"doc_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(_ context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if svc == nil {
				return JSONError("search service is not configured"), nil
			}
			var in struct {
				DocID    string `json:"doc_id"`
				MaxChars int    `json:"max_chars,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			docID := strings.TrimSpace(in.DocID)
			if docID == "" {
				return JSONError("doc_id is required"), nil
			}
			maxChars := in.MaxChars
			if maxChars <= 0 {
				maxChars = 4000
			}
			if maxChars > 20000 {
				return JSONError("max_chars must be <= 20000"), nil
			}
			doc, ok := svc.GetDoc(docID)
			if !ok {
				return JSONError("doc not found"), nil
			}
			currentRunProvenance := retrievedQueryRetryRunMatches(doc.RunID, exec.RunID)
			stripLegacyTargets := retrievedSearchDocHasQueryLifecycle(doc.Text)
			allowCurrentRunObligation := currentRunProvenance && stripLegacyTargets
			text := sanitizeRetrievedQueryRetryText(doc.Text, allowCurrentRunObligation, stripLegacyTargets)
			out := map[string]any{
				"doc_id":      doc.ID,
				"doc_kind":    doc.DocKind,
				"thread_id":   doc.ThreadID,
				"title":       sanitizeRetrievedQueryRetryText(doc.Title, allowCurrentRunObligation, stripLegacyTargets),
				"text":        truncateRunes(text, maxChars),
				"status":      doc.Status,
				"updated_at":  doc.UpdatedAt.UnixMilli(),
				"created_at":  doc.CreatedAt.UnixMilli(),
				"schema_ver":  doc.SchemaVersion,
				"record_kind": string(doc.Kind),
			}
			if doc.RunID != nil {
				out["run_id"] = *doc.RunID
			}
			if doc.MessageID != nil {
				out["message_id"] = *doc.MessageID
			}
			if doc.ArtifactID != nil {
				out["artifact_id"] = *doc.ArtifactID
			}
			if doc.ProjectID != nil {
				out["project_id"] = *doc.ProjectID
			}
			if len(doc.Tags) > 0 {
				out["tags"] = doc.Tags
			}
			fullTextChars := len([]rune(strings.TrimSpace(text)))
			meta := map[string]any{
				"doc_id":              doc.ID,
				"doc_kind":            doc.DocKind,
				"thread_id":           doc.ThreadID,
				"max_chars":           maxChars,
				"text_chars_total":    fullTextChars,
				"text_chars_returned": len([]rune(out["text"].(string))),
				"truncation":          fullTextChars > maxChars,
			}
			if doc.RunID != nil {
				meta["run_id"] = *doc.RunID
			}
			if doc.MessageID != nil {
				meta["message_id"] = *doc.MessageID
			}
			if doc.ArtifactID != nil {
				meta["artifact_id"] = *doc.ArtifactID
			}
			if doc.ProjectID != nil {
				meta["project_id"] = *doc.ProjectID
			}
			return threadSearchOutput("get_doc", "search_document", out, meta), nil
		},
	}
}

func newGetArtifactTool(st store.Store, cfgDir string) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_artifact",
				Description: "Fetch artifact metadata and a byte-bounded content chunk by artifact_id. Use page_token or offset for paging large artifacts.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Artifact ID.",
						},
						"max_bytes": map[string]any{
							"type":        "integer",
							"description": "Max bytes to return for content (default 65536, max 262144).",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "Zero-based byte offset. Ignored when page_token is supplied.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from previous get_artifact or get_artifact_chunk call.",
						},
						"include_image_data": map[string]any{
							"type":        "boolean",
							"description": "When true, include raw data URI content for image artifacts. Defaults to false.",
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
			id := strings.TrimSpace(in.ArtifactID)
			if id == "" {
				return JSONError("artifact_id is required"), nil
			}
			maxBytes := in.MaxBytes
			if maxBytes <= 0 {
				maxBytes = 64 << 10
			}
			if maxBytes > 256<<10 {
				return JSONError("max_bytes must be <= 262144"), nil
			}
			offset, errOut, ok := artifactOffsetFromInputs(in.Offset, in.PageToken)
			if !ok {
				return errOut, nil
			}
			a, err := st.GetArtifact(ctx, id)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			// Artifacts are durable, cross-run objects and the artifact row has no
			// authoritative RunID. A source message reference is useful lineage but
			// cannot make retry authority portable, so artifact reads always redact it.
			currentRunProvenance := false
			stripLegacyTargets := a.ToolName != nil && isRetrievedQueryLifecycleToolName(*a.ToolName)
			imageContentOmitted := strings.EqualFold(strings.TrimSpace(a.Type), "image") && !in.IncludeImageData
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
				meta, _ := sanitizeRetrievedQueryRetryValue(threadSearchArtifactMetadata(a, out, offset, maxBytes), currentRunProvenance, stripLegacyTargets).(map[string]any)
				return threadSearchOutput("get_artifact", "artifact_chunk", out, meta), nil
			}
			chunk, readErr := readRetrievedArtifactContentChunk(cfgDir, a, offset, maxBytes)
			if readErr != nil {
				return JSONError(readErr.Error()), nil
			}
			// Artifact content may be a business dataset whose columns happen to
			// resemble lifecycle fields. Redact qob tokens globally, but only the
			// descriptor metadata gets structural retry-field removal.
			content := sanitizeRetrievedQueryRetryText(
				chunk.Content,
				currentRunProvenance,
				stripLegacyTargets && retrievedTextHasQueryLifecycleEnvelope(chunk.Content),
			)
			out["content"] = content
			out["content_size"] = len(content)
			out["bytes_returned"] = chunk.BytesReturned
			out["byte_size"] = chunk.ByteSize
			out["has_more"] = chunk.HasMore
			if chunk.HasMore {
				out["next_page_token"] = encodePageOffsetToken(chunk.Offset + int64(chunk.BytesReturned))
			}
			meta, _ := sanitizeRetrievedQueryRetryValue(threadSearchArtifactMetadata(a, out, offset, maxBytes), currentRunProvenance, stripLegacyTargets).(map[string]any)
			return threadSearchOutput("get_artifact", "artifact_chunk", out, meta), nil
		},
	}
}

func newListMessagesTool(st store.Store) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_messages",
				Description: "List one deterministic page of messages in a thread. For continuation, copy next_page_token unchanged into page_token; do not construct or combine sequence bounds for ordinary paging.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"thread_id": map[string]any{
							"type":        "string",
							"description": "Thread ID.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Opaque next_page_token returned by the previous list_messages call. When supplied, pass only thread_id and page_token; the token preserves ordering, filters, limits, and the exclusive sequence boundary.",
						},
						"after_seq": map[string]any{
							"type":        "integer",
							"minimum":     0,
							"description": "Optional exclusive lower bound for an explicit sequence window. If before_seq is also supplied, after_seq must be numerically smaller. Do not use for ordinary pagination; use page_token.",
						},
						"before_seq": map[string]any{
							"type":        "integer",
							"minimum":     0,
							"description": "Optional exclusive upper bound for an explicit sequence window. If after_seq is also supplied, before_seq must be numerically greater. Do not use for ordinary pagination; use page_token.",
						},
						"before_ts": map[string]any{
							"type":        "string",
							"description": "Upper bound (RFC3339) for created time.",
						},
						"after_ts": map[string]any{
							"type":        "string",
							"description": "Lower bound (RFC3339) for created time.",
						},
						"message_ids": map[string]any{
							"type":        "array",
							"description": "Filter to listed message IDs.",
							"items": map[string]any{
								"type": "string",
							},
						},
						"artifact_ids": map[string]any{
							"type":        "array",
							"description": "Filter to messages that reference listed artifacts.",
							"items": map[string]any{
								"type": "string",
							},
						},
						"order_by": map[string]any{
							"type":        "string",
							"description": "Deterministic ordering: seq_desc (default), seq_asc.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Max messages to return (default 20, max 100).",
						},
						"max_chars_per_result": map[string]any{
							"type":        "integer",
							"description": "Max chars per returned content field.",
						},
						"include_hidden": map[string]any{
							"type":        "boolean",
							"description": "Whether to include UI-hidden messages.",
						},
					},
					"required":             []string{"thread_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ThreadID      string   `json:"thread_id"`
				PageToken     string   `json:"page_token,omitempty"`
				AfterSeq      *int64   `json:"after_seq,omitempty"`
				BeforeSeq     *int64   `json:"before_seq,omitempty"`
				BeforeTS      *string  `json:"before_ts,omitempty"`
				AfterTS       *string  `json:"after_ts,omitempty"`
				MessageIDs    []string `json:"message_ids,omitempty"`
				ArtifactIDs   []string `json:"artifact_ids,omitempty"`
				OrderBy       string   `json:"order_by,omitempty"`
				MaxChars      int      `json:"max_chars_per_result,omitempty"`
				Limit         int      `json:"limit,omitempty"`
				IncludeHidden bool     `json:"include_hidden,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}

			threadID := strings.TrimSpace(in.ThreadID)
			if threadID == "" {
				return JSONError("thread_id is required"), nil
			}

			pageToken := strings.TrimSpace(in.PageToken)
			var cursor messagepaging.Cursor
			var beforeTS, afterTS *time.Time
			if pageToken != "" {
				if conflicts := listMessagesPageTokenConflicts(args); len(conflicts) > 0 {
					return JSONError("page_token cannot be combined with " + strings.Join(conflicts, ", ") + "; copy next_page_token with only thread_id and page_token"), nil
				}
				decoded, tokenErr := messagepaging.Decode(pageToken)
				if tokenErr != nil {
					return JSONError(tokenErr.Error()), nil
				}
				if strings.TrimSpace(decoded.ThreadID) != threadID {
					return JSONError("page_token belongs to a different thread"), nil
				}
				cursor = decoded
				beforeTS = parseTimeFromString(stringPtrIfNotEmpty(cursor.BeforeTS))
				afterTS = parseTimeFromString(stringPtrIfNotEmpty(cursor.AfterTS))
			} else {
				orderBy := normalizeListMessagesOrderBy(strings.TrimSpace(in.OrderBy))
				if orderBy == "" && strings.TrimSpace(in.OrderBy) != "" {
					return JSONError("order_by must be one of: seq_desc, seq_asc"), nil
				}

				beforeTS = parseTimeFromString(in.BeforeTS)
				if in.BeforeTS != nil && strings.TrimSpace(*in.BeforeTS) != "" && beforeTS == nil {
					return JSONError("before_ts must be RFC3339 timestamp"), nil
				}
				afterTS = parseTimeFromString(in.AfterTS)
				if in.AfterTS != nil && strings.TrimSpace(*in.AfterTS) != "" && afterTS == nil {
					return JSONError("after_ts must be RFC3339 timestamp"), nil
				}
				if beforeTS != nil && afterTS != nil && beforeTS.Before(*afterTS) {
					return JSONError("before_ts must be greater than or equal to after_ts"), nil
				}

				if in.MaxChars == 0 {
					in.MaxChars = 3000
				} else if in.MaxChars < 0 {
					return JSONError("max_chars_per_result must be >= 0"), nil
				} else if in.MaxChars > 3000 {
					return JSONError("max_chars_per_result must be <= 3000"), nil
				}

				if in.AfterSeq != nil && in.BeforeSeq != nil && *in.BeforeSeq <= *in.AfterSeq {
					return JSONError(fmt.Sprintf("before_seq must be greater than after_seq (before_seq=%d, after_seq=%d); use next_page_token for pagination", *in.BeforeSeq, *in.AfterSeq)), nil
				}
				if in.Limit <= 0 {
					in.Limit = 20
				}
				if in.Limit > 100 {
					return JSONError("limit must be <= 100"), nil
				}
				if in.AfterSeq != nil && *in.AfterSeq < 0 {
					return JSONError("after_seq must be >= 0"), nil
				}
				if in.BeforeSeq != nil && *in.BeforeSeq < 0 {
					return JSONError("before_seq must be >= 0"), nil
				}

				messageIDs := dedupeStringSlice(in.MessageIDs)
				if len(messageIDs) > 100 {
					return JSONError("message_ids length must be <= 100"), nil
				}
				artifactIDs := dedupeStringSlice(in.ArtifactIDs)
				if len(artifactIDs) > 100 {
					return JSONError("artifact_ids length must be <= 100"), nil
				}
				sort.Strings(messageIDs)
				sort.Strings(artifactIDs)
				cursor = messagepaging.Cursor{
					ThreadID:      threadID,
					OrderBy:       orderBy,
					BeforeSeq:     in.BeforeSeq,
					AfterSeq:      in.AfterSeq,
					MessageIDs:    messageIDs,
					ArtifactIDs:   artifactIDs,
					Limit:         in.Limit,
					MaxChars:      in.MaxChars,
					IncludeHidden: in.IncludeHidden,
				}
				if beforeTS != nil {
					cursor.BeforeTS = beforeTS.UTC().Format(time.RFC3339Nano)
				}
				if afterTS != nil {
					cursor.AfterTS = afterTS.UTC().Format(time.RFC3339Nano)
				}
			}

			limit := cursor.Limit
			if st == nil {
				return JSONError("store is not configured"), nil
			}
			msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
				ThreadID:      threadID,
				AfterSeq:      cursor.AfterSeq,
				BeforeSeq:     cursor.BeforeSeq,
				BeforeTS:      beforeTS,
				AfterTS:       afterTS,
				MessageIDs:    cursor.MessageIDs,
				ArtifactIDs:   cursor.ArtifactIDs,
				OrderBy:       cursor.OrderBy,
				Limit:         limit + 1,
				IncludeHidden: cursor.IncludeHidden,
			})
			if err != nil {
				return JSONError(err.Error()), nil
			}
			hasMore := len(msgs) > limit
			if hasMore {
				msgs = msgs[:limit]
			}
			out := make([]any, 0, len(msgs))
			for _, m := range msgs {
				if m.UIHidden && !cursor.IncludeHidden {
					continue
				}
				maxChars := cursor.MaxChars
				if maxChars <= 0 {
					maxChars = 3000
				}
				row := map[string]any{
					"id":         m.ID,
					"message_id": m.ID,
					"seq":        float64(m.Seq),
					"thread_id":  m.ThreadID,
					"role":       string(m.Role),
					"ui_hidden":  m.UIHidden,
					"created_at": float64(m.CreatedAt.UnixMilli()),
					"updated_at": float64(m.UpdatedAt.UnixMilli()),
				}
				currentRunProvenance := retrievedQueryRetryRunMatches(m.RunID, exec.RunID)
				stripLegacyTargets := m.Name != nil && isRetrievedQueryLifecycleToolName(*m.Name)
				if !stripLegacyTargets && m.Content != nil {
					stripLegacyTargets = retrievedTextHasQueryLifecycleEnvelope(*m.Content)
				}
				allowCurrentRunObligation := currentRunProvenance && stripLegacyTargets
				if m.RunID != nil {
					row["run_id"] = *m.RunID
				}
				if m.Content != nil {
					row["content"] = truncateRunes(sanitizeRetrievedQueryRetryText(*m.Content, allowCurrentRunObligation, stripLegacyTargets), maxChars)
				} else {
					row["content"] = ""
				}
				if m.ToolCallID != nil {
					row["tool_call_id"] = *m.ToolCallID
				}
				if m.Name != nil {
					row["name"] = *m.Name
				}
				if len(m.ReferencedArtifactIDs) > 0 {
					row["artifact_ids"] = append([]string(nil), m.ReferencedArtifactIDs...)
				}
				if m.Thinking != nil {
					row["thinking"] = truncateRunes(sanitizeRetrievedQueryRetryText(*m.Thinking, allowCurrentRunObligation, stripLegacyTargets), 2000)
				}
				out = append(out, row)
			}
			result := map[string]any{
				"messages":  out,
				"count":     len(out),
				"page_size": limit,
				"order_by":  cursor.OrderBy,
				"has_more":  hasMore,
			}
			meta := map[string]any{
				"thread_id":            threadID,
				"count":                len(out),
				"limit":                limit,
				"page_size":            limit,
				"order_by":             cursor.OrderBy,
				"max_chars_per_result": cursor.MaxChars,
				"include_hidden":       cursor.IncludeHidden,
				"has_more":             hasMore,
				"truncation":           hasMore,
				"limit_reached":        len(out) >= limit,
				"page_token_used":      pageToken != "",
			}
			if cursor.AfterSeq != nil {
				meta["after_seq"] = *cursor.AfterSeq
			}
			if cursor.BeforeSeq != nil {
				meta["before_seq"] = *cursor.BeforeSeq
			}
			if cursor.BeforeTS != "" {
				meta["before_ts"] = cursor.BeforeTS
			}
			if cursor.AfterTS != "" {
				meta["after_ts"] = cursor.AfterTS
			}
			if len(cursor.MessageIDs) > 0 {
				meta["message_ids"] = cursor.MessageIDs
				meta["message_filter_count"] = len(cursor.MessageIDs)
			}
			if len(cursor.ArtifactIDs) > 0 {
				meta["artifact_ids"] = cursor.ArtifactIDs
				meta["artifact_filter_count"] = len(cursor.ArtifactIDs)
			}
			if hasMore && len(msgs) > 0 {
				nextToken, tokenErr := messagepaging.Encode(messagepaging.Advance(cursor, msgs[len(msgs)-1].Seq))
				if tokenErr != nil {
					return JSONError("Failed to create the next message page token: " + tokenErr.Error()), nil
				}
				result["next_page_token"] = nextToken
				meta["next_page_token"] = nextToken
			}
			return threadSearchOutput("list_messages", "thread_messages", result, meta), nil
		},
	}
}

func threadSearchArtifactMetadata(a store.Artifact, result map[string]any, offset int64, maxBytes int64) map[string]any {
	meta := map[string]any{
		"artifact_id":   a.ID,
		"thread_id":     a.ThreadID,
		"title":         a.Title,
		"type":          a.Type,
		"artifact_type": a.Type,
		"storage":       a.Storage,
		"offset":        offset,
		"max_bytes":     maxBytes,
		"byte_size":     a.ByteSize,
	}
	if a.MessageID != nil && strings.TrimSpace(*a.MessageID) != "" {
		meta["message_id"] = *a.MessageID
	}
	if a.ContentPath != nil && strings.TrimSpace(*a.ContentPath) != "" {
		meta["content_path"] = strings.TrimSpace(*a.ContentPath)
	}
	for _, key := range []string{
		"content_size",
		"bytes_returned",
		"byte_size",
		"has_more",
		"next_page_token",
		"image_content_omitted",
	} {
		if value, ok := result[key]; ok {
			meta[key] = value
		}
	}
	hasMore, _ := result["has_more"].(bool)
	meta["truncation"] = hasMore
	if imageOmitted, _ := result["image_content_omitted"].(bool); imageOmitted {
		meta["truncation"] = false
	}
	return meta
}

const retrievedQueryRetryIdentityOmitted = "[query retry identity omitted]"

var (
	retrievedQueryObligationIDPattern = regexp.MustCompile(`qob-[0-9A-Fa-f]{32}`)
	retrievedReplacementLabelPattern  = regexp.MustCompile(`(?i)\b(?:replaces[_ -]?job[_ -]?id|replacement[_ -]?(?:target[_ -]?)?job[_ -]?id|attempted[_ -]?(?:replaces|replacement|retry)[_ -]?(?:target|job[_ -]?id)?|retry[_ -]?target|target[_ -]?job[_ -]?id)\b\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;}]+)`)
	// Historical ownership errors used prose rather than a key/value delimiter,
	// for example: "replacement target [qj-old] is not owned by the current
	// run". The ownership suffix keeps this matcher narrow so ordinary business
	// text containing a bracketed replacement target is not redacted.
	retrievedReplacementOwnershipErrorPattern = regexp.MustCompile(`(?i)\breplacement[_ -]?target\s+(?:\[[^\]\r\n]{1,256}\]|qj-[A-Za-z0-9][A-Za-z0-9_-]{0,127}|nil|null|<nil>|0{4,}(?:-0{4,})*)\s+is\s+not\s+owned\s+by\s+(?:the\s+)?current\s+run\b`)
)

// retrievedQueryRetryRunMatches is deliberately fail-closed. Retrieval tools
// can expose messages and artifacts from any saved thread, while a query
// obligation is authority scoped to exactly one run. A nil/missing source run
// or a missing execution run therefore never makes an obligation portable.
func retrievedQueryRetryRunMatches(sourceRunID *string, executionRunID string) bool {
	if sourceRunID == nil {
		return false
	}
	source := strings.TrimSpace(*sourceRunID)
	execution := strings.TrimSpace(executionRunID)
	return source != "" && execution != "" && source == execution
}

func isRetrievedQueryLifecycleToolName(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	if IsAsyncQueryRunTool(toolName) {
		return true
	}
	switch strings.ToLower(toolName) {
	case "check_query_status", "get_query_results", "validate_query_result", "cancel_query":
		return true
	default:
		return false
	}
}

func retrievedSearchDocHasQueryLifecycle(text string) bool {
	lower := strings.ToLower(text)
	for _, toolName := range AsyncQueryRunToolNames() {
		if strings.Contains(lower, strings.ToLower(toolName)) {
			return true
		}
	}
	for _, toolName := range []string{"check_query_status", "get_query_results", "validate_query_result", "cancel_query"} {
		if strings.Contains(lower, toolName) {
			return true
		}
	}
	return false
}

func retrievedTextHasQueryLifecycleEnvelope(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	if !json.Valid([]byte(trimmed)) {
		return false
	}
	var decoded any
	if json.Unmarshal([]byte(trimmed), &decoded) != nil {
		return false
	}
	var visit func(any) bool
	visit = func(value any) bool {
		switch typed := value.(type) {
		case map[string]any:
			for key, nested := range typed {
				switch normalizeRetrievedQueryRetryKey(key) {
				case "querymetadata", "retrymetadata", "queryobligationmanaged", "replacementwaitrequired",
					"replacementpreflightfailed", "replacementpreflightretryrequired", "monitorrecoverypending":
					return true
				}
				if visit(nested) {
					return true
				}
			}
		case []any:
			for _, nested := range typed {
				if visit(nested) {
					return true
				}
			}
		}
		return false
	}
	return visit(decoded)
}

func sanitizeRetrievedQueryRetryText(raw string, allowCurrentRunObligation bool, stripLegacyRetryTargets bool) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed != "" && json.Valid([]byte(trimmed)) {
		decoder := json.NewDecoder(strings.NewReader(trimmed))
		decoder.UseNumber()
		var decoded any
		if decoder.Decode(&decoded) == nil {
			decoded = sanitizeRetrievedQueryRetryValue(decoded, allowCurrentRunObligation, stripLegacyRetryTargets)
			if encoded, err := json.Marshal(decoded); err == nil {
				return string(encoded)
			}
		}
	}

	// Plain text and arbitrary string values never establish which qob a field
	// represents. Same-run preservation is limited to a canonical structured
	// query_obligation_id key handled by sanitizeRetrievedQueryRetryValue.
	clean := retrievedQueryObligationIDPattern.ReplaceAllString(raw, retrievedQueryRetryIdentityOmitted)
	// Preserve ordinary observation handles such as job_id: the model still
	// needs those to poll and fetch results. Only a handle explicitly labelled
	// as a replacement/retry target is authority-bearing legacy state.
	if stripLegacyRetryTargets {
		clean = retrievedReplacementOwnershipErrorPattern.ReplaceAllString(clean, retrievedQueryRetryIdentityOmitted)
		clean = retrievedReplacementLabelPattern.ReplaceAllString(clean, retrievedQueryRetryIdentityOmitted)
	}
	return clean
}

// readRetrievedArtifactContentChunk uses a one-token overlap and a
// length-preserving qob mask before selecting the requested byte range. A qob
// split across two adjacent pages therefore cannot be reconstructed by
// concatenating the responses, while paging offsets remain tied to the source
// artifact rather than to variable-length redaction text.
func readRetrievedArtifactContentChunk(cfgDir string, artifact store.Artifact, offset, maxBytes int64) (artifactContentChunk, error) {
	const overlap = int64(len(store.QueryObligationIDPrefix) + 32 - 1)
	start := offset - overlap
	if start < 0 {
		start = 0
	}
	leftOverlap := offset - start
	readBytes := maxBytes + leftOverlap + overlap
	if readBytes < maxBytes {
		return artifactContentChunk{}, errors.New("artifact retrieval range overflow")
	}
	expanded, err := readArtifactContentChunk(cfgDir, artifact, start, readBytes)
	if err != nil {
		return artifactContentChunk{}, err
	}
	redacted := retrievedQueryObligationIDPattern.ReplaceAllStringFunc(expanded.Content, func(candidate string) string {
		return strings.Repeat("*", len(candidate))
	})
	data := []byte(redacted)
	begin := int(leftOverlap)
	if begin > len(data) {
		begin = len(data)
	}
	end := begin + int(maxBytes)
	if end < begin || end > len(data) {
		end = len(data)
	}
	selected := data[begin:end]
	bytesReturned := len(selected)
	return artifactContentChunk{
		Content:       string(selected),
		Offset:        offset,
		BytesReturned: bytesReturned,
		ByteSize:      expanded.ByteSize,
		HasMore:       offset+int64(bytesReturned) < expanded.ByteSize,
	}, nil
}

func sanitizeRetrievedQueryRetryValue(value any, allowCurrentRunObligation bool, stripLegacyRetryTargets bool) any {
	switch typed := value.(type) {
	case map[string]any:
		clean := make(map[string]any, len(typed))
		for key, nested := range typed {
			// Dropping a stale authority-bearing key is collision-safe; renaming
			// two distinct keys to one redaction marker could silently overwrite
			// an unrelated value in the retrieved object.
			if sanitizedKey := sanitizeRetrievedQueryRetryText(key, allowCurrentRunObligation, false); sanitizedKey != key {
				continue
			}
			normalized := normalizeRetrievedQueryRetryKey(key)
			if normalized == "queryobligationid" {
				candidate, ok := nested.(string)
				if key == "query_obligation_id" && allowCurrentRunObligation && ok && store.ValidateQueryObligationID(candidate) == nil {
					clean[key] = strings.TrimSpace(candidate)
				}
				continue
			}
			if stripLegacyRetryTargets && isRetrievedQueryRetryIdentityKey(normalized) {
				continue
			}
			clean[key] = sanitizeRetrievedQueryRetryValue(nested, allowCurrentRunObligation, stripLegacyRetryTargets)
		}
		return clean
	case []any:
		clean := make([]any, len(typed))
		for index, nested := range typed {
			clean[index] = sanitizeRetrievedQueryRetryValue(nested, allowCurrentRunObligation, stripLegacyRetryTargets)
		}
		return clean
	case []map[string]any:
		clean := make([]map[string]any, len(typed))
		for index, nested := range typed {
			item, _ := sanitizeRetrievedQueryRetryValue(nested, allowCurrentRunObligation, stripLegacyRetryTargets).(map[string]any)
			clean[index] = item
		}
		return clean
	case []string:
		clean := make([]string, len(typed))
		for index, nested := range typed {
			clean[index] = sanitizeRetrievedQueryRetryText(nested, allowCurrentRunObligation, stripLegacyRetryTargets)
		}
		return clean
	case string:
		return sanitizeRetrievedQueryRetryText(typed, allowCurrentRunObligation, stripLegacyRetryTargets)
	case json.RawMessage:
		return json.RawMessage(sanitizeRetrievedQueryRetryText(string(typed), allowCurrentRunObligation, stripLegacyRetryTargets))
	default:
		return value
	}
}

func normalizeRetrievedQueryRetryKey(key string) string {
	var normalized strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(key)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			normalized.WriteRune(char)
		}
	}
	return normalized.String()
}

func isRetrievedQueryRetryIdentityKey(normalized string) bool {
	switch normalized {
	case "queryobligationmanaged", "replacesjobid", "replacementjobid", "replacementtargetjobid",
		"attemptedretrytarget", "attemptedreplacesjobid", "attemptedreplacementjobid",
		"candidatejobids", "replacementcandidatejobids", "resolvedreplacementjobid",
		"replacementtarget", "retrytarget", "targetjobid":
		return true
	default:
		return false
	}
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= max {
		return string(runes)
	}
	return string(runes[:max])
}

func normalizeSearchDocsOrderBy(orderBy string) string {
	switch strings.TrimSpace(strings.ToLower(orderBy)) {
	case "", "score_desc", "updated_at_desc", "updated_at_asc":
		if strings.TrimSpace(orderBy) == "" {
			return "score_desc"
		}
		return strings.TrimSpace(strings.ToLower(orderBy))
	default:
		return ""
	}
}

func normalizeListMessagesOrderBy(orderBy string) string {
	switch strings.TrimSpace(strings.ToLower(orderBy)) {
	case "", "seq_desc", "seq_asc":
		if strings.TrimSpace(orderBy) == "" {
			return "seq_desc"
		}
		return strings.TrimSpace(strings.ToLower(orderBy))
	default:
		return ""
	}
}

func dedupeStringSlice(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func parseTimeFromString(v *string) *time.Time {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return &parsed
	}
	parsed, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return &parsed
	}
	return nil
}

func stringPtrIfNotEmpty(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func listMessagesPageTokenConflicts(args json.RawMessage) []string {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(args, &raw); err != nil {
		return nil
	}
	fields := []string{
		"after_seq",
		"before_seq",
		"before_ts",
		"after_ts",
		"message_ids",
		"artifact_ids",
		"order_by",
		"limit",
		"max_chars_per_result",
		"include_hidden",
	}
	conflicts := make([]string, 0, len(fields))
	for _, field := range fields {
		if _, exists := raw[field]; exists {
			conflicts = append(conflicts, field)
		}
	}
	return conflicts
}

func readRelativeArtifactFile(cfgDir string, rel string, maxBytes int64) (string, error) {
	clean, err := cleanRelPath(rel)
	if err != nil {
		return "", err
	}
	full := filepath.Join(cfgDir, filepath.FromSlash(clean))
	f, err := os.Open(full)
	if err != nil {
		return "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(b)) > maxBytes {
		b = b[:maxBytes]
	}
	return string(b), nil
}

func cleanRelPath(p string) (string, error) {
	raw := strings.TrimSpace(p)
	raw = strings.ReplaceAll(raw, "\\", "/")
	raw = strings.TrimPrefix(raw, "./")
	raw = strings.TrimPrefix(raw, "/")
	clean := path.Clean(raw)
	if clean == "." || clean == "" {
		return "", errors.New("invalid path")
	}
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", errors.New("path traversal is not allowed")
	}
	if strings.ContainsRune(clean, '\x00') {
		return "", errors.New("invalid path")
	}
	return clean, nil
}
