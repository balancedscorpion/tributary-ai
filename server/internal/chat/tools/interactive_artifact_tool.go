package tools

import (
	"context"
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/types"
)

const maxInteractiveHTMLBytes = 512 << 10
const defaultInteractiveDataPageSize = 100
const maxInteractiveDataPageSize = 500
const createInteractiveArtifactToolName = "create_interactive_artifact"
const createInteractiveDataArtifactToolName = "create_interactive_data_artifact"

func emitInteractiveProgress(exec ExecContext, phase string, percent int, fields map[string]any) {
	if exec.OnProgress == nil {
		return
	}
	progress := map[string]any{
		"phase": phase,
	}
	if percent >= 0 {
		progress["percent"] = percent
	}
	switch phase {
	case "loading_source_metadata":
		progress["label"] = "Loading source metadata"
	case "source_loaded":
		progress["label"] = "Source data ready"
	case "binding_data_source":
		progress["label"] = "Binding data source"
	case "validating_html":
		progress["label"] = "Validating HTML"
	case "preparing_artifact":
		progress["label"] = "Preparing artifact"
	case "artifact_ready":
		progress["label"] = "Artifact ready"
	case "validation_failed":
		progress["label"] = "Validation failed"
	}
	for key, value := range fields {
		progress[key] = value
	}
	exec.OnProgress(progress)
}

func newCreateInteractiveArtifactTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name: createInteractiveArtifactToolName,
				Description: "Create a self-contained interactive HTML artifact rendered in a sandboxed iframe. " +
					"Use this for custom interactive visuals, simulations, small tools, and one-off HTML/CSS/JavaScript experiences. " +
					"The HTML must be standalone and must not rely on network requests, external scripts, or external stylesheets.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{
							"type":        "string",
							"description": "Short title for the artifact.",
						},
						"html": map[string]any{
							"type":        "string",
							"description": "Complete self-contained HTML document or fragment. Inline CSS and JavaScript are allowed. External network resources are blocked by the sandbox.",
						},
					},
					"required":             []string{"title", "html"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				Title string `json:"title"`
				HTML  string `json:"html"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}

			emitInteractiveProgress(exec, "validating_html", 10, nil)
			title := strings.TrimSpace(in.Title)
			if title == "" {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{"message": "title is required"})
				return JSONError("title is required"), nil
			}
			html := strings.TrimSpace(in.HTML)
			if html == "" {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{"message": "html is required"})
				return JSONError("html is required"), nil
			}
			byteSize := len([]byte(html))
			if byteSize > maxInteractiveHTMLBytes {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{
					"message":    "html exceeds size limit",
					"html_bytes": byteSize,
				})
				return JSONError("html exceeds the 512 KiB limit for interactive artifacts"), nil
			}
			emitInteractiveProgress(exec, "preparing_artifact", 70, map[string]any{"html_bytes": byteSize})

			language := "html"
			resultMetadata := interactiveArtifactResultMetadata(createInteractiveArtifactToolName, "interactive_html", map[string]any{
				"byte_size": byteSize,
			})
			metadata := interactiveArtifactPayloadMetadata(map[string]any{
				"render_mode":    "sandboxed_html",
				"artifact_kind":  "interactive_html",
				"sandbox":        "allow-scripts",
				"network":        "blocked",
				"inline_preview": true,
				"byte_size":      byteSize,
			}, createInteractiveArtifactToolName, interactiveArtifactToolArgs(args), resultMetadata)
			emitInteractiveProgress(exec, "artifact_ready", 95, map[string]any{"html_bytes": byteSize})

			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				ResultMetadata: resultMetadata,
				Result: map[string]any{
					"title":          title,
					"artifact_type":  "interactive_html",
					"language":       language,
					"render_mode":    "sandboxed_html",
					"sandbox":        "allow-scripts",
					"network":        "blocked",
					"inline_preview": true,
					"byte_size":      byteSize,
				},
				Artifact: &ArtifactPayload{
					Type:     "code",
					Title:    title,
					Language: &language,
					Content:  html,
					Metadata: metadata,
				},
			}, nil
		},
	}
}

func newCreateInteractiveDataArtifactTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name: createInteractiveDataArtifactToolName,
				Description: "Create a self-contained interactive HTML artifact backed by a persisted dataset artifact. " +
					"Use this instead of embedding dataset rows into HTML. The HTML runs in a sandboxed iframe and can request focused row pages with window.sandboxData.getRows({ offset, limit, columns }). " +
					"The sandbox cannot make network requests or call app APIs directly; the parent app mediates data access.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"title": map[string]any{
							"type":        "string",
							"description": "Short title for the artifact.",
						},
						"source": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"artifact_id": map[string]any{
									"type":        "string",
									"description": "Dataset artifact ID to bind to this HTML artifact.",
								},
							},
							"required":             []string{"artifact_id"},
							"additionalProperties": false,
						},
						"html": map[string]any{
							"type":        "string",
							"description": "Complete self-contained HTML document or fragment. Use window.sandboxData.getMetadata() for dataset metadata and window.sandboxData.getRows({ offset, limit, columns }) for row pages.",
						},
						"max_page_size": map[string]any{
							"type":        "integer",
							"description": "Maximum rows per sandboxData.getRows call. Default 100, max 500.",
						},
					},
					"required":             []string{"title", "source", "html"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				Title  string `json:"title"`
				Source struct {
					ArtifactID string `json:"artifact_id"`
				} `json:"source"`
				HTML        string `json:"html"`
				MaxPageSize int    `json:"max_page_size,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}

			sourceArtifactID := strings.TrimSpace(in.Source.ArtifactID)
			emitInteractiveProgress(exec, "loading_source_metadata", 5, map[string]any{
				"source_artifact_id": sourceArtifactID,
			})
			artifact, rows, errOut, ok := requireDatasetArtifactRows(ctx, exec, sourceArtifactID)
			if !ok {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{
					"source_artifact_id": sourceArtifactID,
					"message":            "source dataset could not be loaded",
				})
				return errOut, nil
			}
			columns := datasetColumnNames(rows)
			emitInteractiveProgress(exec, "source_loaded", 35, map[string]any{
				"source_artifact_id": sourceArtifactID,
				"row_count":          len(rows),
				"column_count":       len(columns),
			})

			emitInteractiveProgress(exec, "validating_html", 50, nil)
			title := strings.TrimSpace(in.Title)
			if title == "" {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{"message": "title is required"})
				return JSONError("title is required"), nil
			}
			html := strings.TrimSpace(in.HTML)
			if html == "" {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{"message": "html is required"})
				return JSONError("html is required"), nil
			}
			byteSize := len([]byte(html))
			if byteSize > maxInteractiveHTMLBytes {
				emitInteractiveProgress(exec, "validation_failed", -1, map[string]any{
					"message":    "html exceeds size limit",
					"html_bytes": byteSize,
				})
				return JSONError("html exceeds the 512 KiB limit for interactive artifacts"), nil
			}
			maxPageSize := clampPositiveInt(in.MaxPageSize, defaultInteractiveDataPageSize, maxInteractiveDataPageSize)
			dataAccess := map[string]any{
				"mode":          "parent_bridge",
				"max_page_size": maxPageSize,
			}
			emitInteractiveProgress(exec, "binding_data_source", 75, map[string]any{
				"source_artifact_id": sourceArtifactID,
				"row_count":          len(rows),
				"column_count":       len(columns),
				"max_page_size":      maxPageSize,
			})

			language := "html"
			resultMetadata := interactiveArtifactResultMetadata(createInteractiveDataArtifactToolName, "interactive_data_html", map[string]any{
				"byte_size":               byteSize,
				"data_source_artifact_id": sourceArtifactID,
				"data_source_title":       artifact.Title,
				"data_source_row_count":   len(rows),
				"data_source_columns":     columns,
				"data_access":             dataAccess,
			})
			metadata := interactiveArtifactPayloadMetadata(map[string]any{
				"render_mode":             "sandboxed_html",
				"artifact_kind":           "interactive_data_html",
				"sandbox":                 "allow-scripts",
				"network":                 "blocked",
				"inline_preview":          true,
				"byte_size":               byteSize,
				"data_source_artifact_id": sourceArtifactID,
				"data_source_title":       artifact.Title,
				"data_source_row_count":   len(rows),
				"data_source_columns":     columns,
				"data_access":             dataAccess,
			}, createInteractiveDataArtifactToolName, interactiveArtifactToolArgs(args), resultMetadata)
			emitInteractiveProgress(exec, "artifact_ready", 95, map[string]any{
				"source_artifact_id": sourceArtifactID,
				"html_bytes":         byteSize,
				"row_count":          len(rows),
				"column_count":       len(columns),
			})

			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				ResultMetadata: resultMetadata,
				Result: map[string]any{
					"title":                   title,
					"artifact_type":           "interactive_data_html",
					"language":                language,
					"render_mode":             "sandboxed_html",
					"sandbox":                 "allow-scripts",
					"network":                 "blocked",
					"inline_preview":          true,
					"byte_size":               byteSize,
					"data_source_artifact_id": sourceArtifactID,
					"data_source_title":       artifact.Title,
					"data_source_row_count":   len(rows),
					"data_source_columns":     columns,
					"data_access":             dataAccess,
				},
				Artifact: &ArtifactPayload{
					Type:     "code",
					Title:    title,
					Language: &language,
					Content:  html,
					Metadata: metadata,
				},
			}, nil
		},
	}
}

func interactiveArtifactToolArgs(args json.RawMessage) map[string]any {
	if len(args) == 0 {
		return nil
	}
	var decoded map[string]any
	if err := json.Unmarshal(args, &decoded); err != nil {
		return nil
	}
	return decoded
}

func interactiveArtifactResultMetadata(toolName string, artifactKind string, fields map[string]any) map[string]any {
	presentation := interactiveArtifactToolPresentation(toolName)
	resultMetadata := map[string]any{
		"integration":       "artifact",
		"product":           "artifact",
		"kind":              artifactKind,
		"widget":            "interactive_html",
		"render_mode":       "sandboxed_html",
		"artifact_kind":     artifactKind,
		"content_kind":      "html",
		"inline_preview":    true,
		"native_viewer":     firstStringMapValue(presentation, "native_viewer"),
		"renderer_strategy": firstStringMapValue(presentation, "renderer_strategy"),
		"renderer_key":      firstStringMapValue(presentation, "renderer_key"),
		"result_kind":       firstStringMapValue(presentation, "result_kind"),
		"file_capability":   firstStringMapValue(presentation, "file_capability"),
		"tool_presentation": presentation,
	}
	if artifactTypes, ok := presentation["artifact_types"]; ok {
		resultMetadata["artifact_types"] = artifactTypes
	}
	for key, value := range fields {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		resultMetadata[key] = value
	}
	return resultMetadata
}

func interactiveArtifactPayloadMetadata(base map[string]any, toolName string, toolArgs map[string]any, resultMetadata map[string]any) map[string]any {
	metadata := map[string]any{}
	for key, value := range base {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		metadata[key] = value
	}
	metadata["tool_name"] = toolName
	if len(toolArgs) > 0 {
		metadata["tool_args"] = toolArgs
	}
	presentation := interactiveArtifactToolPresentation(toolName)
	metadata["tool_presentation"] = presentation
	if len(resultMetadata) > 0 {
		metadata["result_metadata"] = cloneOutputMetadataMap(resultMetadata)
	}
	return metadata
}

func interactiveArtifactToolPresentation(toolName string) map[string]any {
	meta, ok := ToolMetadataByName(toolName)
	if !ok {
		return map[string]any{
			"family":            "artifacts",
			"result_kind":       "interactive",
			"renderer_strategy": "artifact_type",
			"renderer_key":      "interactive_html",
			"native_viewer":     "interactive_html",
			"artifact_types":    []string{"code"},
			"file_capability":   "artifact_create",
		}
	}
	p := meta.Presentation
	out := map[string]any{}
	putInteractivePresentationString(out, "family", p.Family)
	putInteractivePresentationString(out, "result_kind", p.ResultKind)
	putInteractivePresentationString(out, "renderer_strategy", p.RendererStrategy)
	putInteractivePresentationString(out, "renderer_key", p.RendererKey)
	putInteractivePresentationString(out, "native_viewer", p.NativeViewer)
	putInteractivePresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putInteractivePresentationString(out, "file_capability", p.FileCapability)
	putInteractivePresentationStrings(out, "file_kinds", p.FileKinds)
	putInteractivePresentationStrings(out, "preview_modes", p.PreviewModes)
	putInteractivePresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putInteractivePresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putInteractivePresentationString(out map[string]any, key string, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putInteractivePresentationStrings(out map[string]any, key string, values []string) {
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
	if len(copied) == 0 {
		return
	}
	out[key] = copied
}
