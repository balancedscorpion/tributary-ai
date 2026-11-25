package tools

import (
	"encoding/json"
	"strings"

	chattype "ai-analyst-local/internal/chat/types"
)

func largeOutputArtifactID() string {
	return chattype.NewUUID()
}

// NewLargeRowsArtifactPayload builds the canonical persisted JSON-rows artifact.
func NewLargeRowsArtifactPayload(artifactID string, title string, rows []map[string]any, generatedBy string, metadata map[string]any) (*ArtifactPayload, error) {
	return newLargeRowsArtifactPayload(artifactID, title, rows, generatedBy, metadata)
}

func newLargeRowsArtifactPayload(artifactID string, title string, rows []map[string]any, generatedBy string, metadata map[string]any) (*ArtifactPayload, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		artifactID = largeOutputArtifactID()
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Dataset"
	}
	fullRowsJSON, err := json.Marshal(rows)
	if err != nil {
		return nil, err
	}
	meta := cloneLargeOutputMetadata(metadata)
	putMetadataDefault(meta, "row_count_total", len(rows))
	putMetadataDefault(meta, "contains_full_content", true)
	putMetadataDefault(meta, "content_kind", "json_rows")
	putMetadataDefault(meta, "artifact_id", artifactID)
	putMetadataDefault(meta, "full_artifact_id", artifactID)
	putMetadataDefault(meta, "result_preview_only", true)
	if generatedBy = strings.TrimSpace(generatedBy); generatedBy != "" {
		putMetadataDefault(meta, "generated_by", generatedBy)
	}
	meta["retrieval"] = largeArtifactRetrievalHints(artifactID, "json_rows")
	attachLargeOutputToolMetadata(meta, generatedBy, artifactID)
	return &ArtifactPayload{
		ID:       artifactID,
		Type:     "data",
		Title:    title,
		Content:  string(fullRowsJSON),
		Metadata: meta,
	}, nil
}

func addLargeOutputPreviewContractMetadata(result map[string]any, resultMeta map[string]any, artifactID string, rowCountTotal int, rowsReturned int, contentKind string) map[string]any {
	return addRowsArtifactReferenceMetadata(result, resultMeta, artifactID, rowCountTotal, rowsReturned, contentKind, true)
}

func addRowsArtifactReferenceMetadata(result map[string]any, resultMeta map[string]any, artifactID string, rowCountTotal int, rowsReturned int, contentKind string, previewOnly bool) map[string]any {
	if result == nil {
		return resultMeta
	}
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" {
		return resultMeta
	}
	contentKind = strings.TrimSpace(contentKind)
	if contentKind == "" {
		contentKind = "json_rows"
	}
	result["artifact_id"] = artifactID
	result["full_artifact_id"] = artifactID
	result["row_count_total"] = rowCountTotal
	result["rows_preview_count"] = rowsReturned
	result["retrieval"] = largeArtifactRetrievalHints(artifactID, contentKind)
	if previewOnly {
		result["preview_only"] = true
	}

	if resultMeta == nil {
		resultMeta = map[string]any{}
	}
	resultMeta["artifact_id"] = artifactID
	resultMeta["full_artifact_id"] = artifactID
	resultMeta["row_count_total"] = rowCountTotal
	resultMeta["rows_preview_count"] = rowsReturned
	resultMeta["content_kind"] = contentKind
	resultMeta["retrieval"] = largeArtifactRetrievalHints(artifactID, contentKind)
	if previewOnly {
		resultMeta["preview_only"] = true
	}
	return resultMeta
}

func largeArtifactRetrievalHints(artifactID string, contentKind string) map[string]any {
	artifactID = strings.TrimSpace(artifactID)
	contentKind = strings.TrimSpace(contentKind)
	if contentKind == "" {
		contentKind = "content"
	}
	tools := []map[string]any{
		{
			"tool":    "get_artifact_chunk",
			"purpose": "Page through raw artifact content without loading the full artifact into model context.",
			"args": map[string]any{
				"artifact_id": artifactID,
				"max_bytes":   defaultArtifactChunkBytes,
			},
		},
		{
			"tool":    "search_artifact_content",
			"purpose": "Find matching text inside the artifact before fetching focused chunks.",
			"args": map[string]any{
				"artifact_id": artifactID,
				"query":       "<text>",
				"limit":       defaultArtifactSearchMatches,
			},
		},
	}
	if contentKind == "json" || contentKind == "json_rows" {
		tools = append(tools,
			map[string]any{
				"tool":    "get_json_artifact",
				"purpose": "Read a paged JSON array or selected JSON path from the artifact.",
				"args": map[string]any{
					"artifact_id": artifactID,
					"limit":       defaultJSONArtifactLimit,
				},
			},
			map[string]any{
				"tool":    "inspect_dataset_artifact",
				"purpose": "Profile persisted row-oriented JSON datasets before reading many rows.",
				"args": map[string]any{
					"artifact_id": artifactID,
				},
			},
			map[string]any{
				"tool":    "get_dataset_rows",
				"purpose": "Read filtered, sorted, and projected pages from row-oriented JSON datasets.",
				"args": map[string]any{
					"artifact_id": artifactID,
					"limit":       defaultDatasetArtifactRowsLimit,
				},
			},
		)
	}
	return map[string]any{
		"artifact_id":  artifactID,
		"content_kind": contentKind,
		"tools":        tools,
	}
}

// LargeArtifactRetrievalHints returns model-facing retrieval instructions for large persisted artifacts.
func LargeArtifactRetrievalHints(artifactID string, contentKind string) map[string]any {
	return largeArtifactRetrievalHints(artifactID, contentKind)
}

func cloneLargeOutputMetadata(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func putMetadataDefault(meta map[string]any, key string, value any) {
	if meta == nil {
		return
	}
	if _, exists := meta[key]; exists {
		return
	}
	meta[key] = value
}

func attachLargeOutputToolMetadata(meta map[string]any, generatedBy string, artifactID string) {
	if meta == nil {
		return
	}
	toolName := strings.TrimSpace(generatedBy)
	if toolName == "" {
		return
	}
	putMetadataDefault(meta, "tool_name", toolName)
	if _, exists := meta["tool_args"]; !exists {
		meta["tool_args"] = map[string]any{}
	}

	resultMetadata := cloneOutputMetadataMap(mapStringAny(meta["result_metadata"]))
	if len(resultMetadata) == 0 {
		resultMetadata = toolCatalogResultMetadata(toolName, largeOutputResultMetadataKind(toolName, meta), largeOutputResultMetadataExtra(meta, artifactID))
		meta["result_metadata"] = cloneOutputMetadataMap(resultMetadata)
	}

	if _, exists := meta["tool_presentation"]; exists {
		return
	}
	presentation := cloneOutputMetadataMap(mapStringAny(resultMetadata["tool_presentation"]))
	if len(presentation) == 0 {
		if toolMeta, ok := ToolMetadataByName(toolName); ok {
			presentation = toolPresentationMetadata(toolMeta.Presentation)
		}
	}
	if len(presentation) > 0 {
		meta["tool_presentation"] = presentation
	}
}

func largeOutputResultMetadataKind(toolName string, meta map[string]any) string {
	switch strings.TrimSpace(toolName) {
	case "build_chart_artifact":
		return "chart_dataset"
	case "display_table":
		return "table_dataset"
	case "get_query_results":
		return "query_result"
	}
	if strings.HasPrefix(strings.TrimSpace(toolName), "run_") {
		return "query_result"
	}
	if contentKind := strings.TrimSpace(anyToString(meta["content_kind"])); contentKind != "" {
		return contentKind
	}
	return "data_artifact"
}

func largeOutputResultMetadataExtra(meta map[string]any, artifactID string) map[string]any {
	out := map[string]any{
		"artifact_id":      artifactID,
		"full_artifact_id": artifactID,
	}
	for _, key := range []string{
		"row_count",
		"row_count_total",
		"rows_returned",
		"rows_preview_count",
		"contains_full_content",
		"content_kind",
		"generated_by",
		"result_preview_only",
		"preview_only",
		"partial",
		"source",
		"source_artifact_id",
		"chart_engine",
		"provider",
		"status",
		"job_id",
		"provider_job_id",
		"provider_job_url",
		"warehouse_job_id",
		"statement_id",
		"query_id",
		"requested_limit",
		"effective_limit",
		"configured_limit",
		"limit_capped",
		"page_size",
		"offset",
		"has_more",
	} {
		if value, ok := meta[key]; ok {
			out[key] = value
		}
	}
	return out
}
