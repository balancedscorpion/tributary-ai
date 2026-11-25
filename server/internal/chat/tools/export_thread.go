package tools

import (
	"context"
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/threadtransfer"
	"ai-analyst-local/internal/chat/types"
)

func newExportThreadTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "export_thread",
				Description: "Export the current thread as a portable .ai-thread.json bundle artifact for download or sharing.",
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Store == nil {
				return JSONError("store is not configured"), nil
			}
			threadID := strings.TrimSpace(exec.ThreadID)
			if threadID == "" {
				return JSONError("thread id is required"), nil
			}
			if len(strings.TrimSpace(string(args))) > 0 && strings.TrimSpace(string(args)) != "{}" {
				var noop map[string]any
				if err := decodeArgs(args, &noop); err != nil {
					return JSONError("Invalid JSON arguments."), nil
				}
			}

			fileName, payload, summary, err := threadtransfer.ExportThreadFile(ctx, exec.Store, exec.CfgDir, threadID)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			result := map[string]any{
				"thread_id":      threadID,
				"file_name":      fileName,
				"message_count":  summary.MessageCount,
				"artifact_count": summary.ArtifactCount,
				"mime_type":      threadtransfer.BundleMimeType,
			}
			resultMetadata := toolCatalogResultMetadata("export_thread", "file", map[string]any{
				"export_kind":        "thread_bundle",
				"download_name":      fileName,
				"download_available": true,
				"mime_type":          threadtransfer.BundleMimeType,
				"thread_id":          threadID,
				"message_count":      summary.MessageCount,
				"artifact_count":     summary.ArtifactCount,
				"bundle_type":        threadtransfer.BundleType,
				"schema_version":     threadtransfer.BundleSchemaVersion,
			})
			artifactMetadata := map[string]any{
				"export_kind":        "thread_bundle",
				"download_name":      fileName,
				"download_available": true,
				"mimeType":           threadtransfer.BundleMimeType,
				"content_encoding":   "utf8",
				"thread_id":          threadID,
				"message_count":      summary.MessageCount,
				"artifact_count":     summary.ArtifactCount,
				"bundle_type":        threadtransfer.BundleType,
				"schema_version":     threadtransfer.BundleSchemaVersion,
				"tool_name":          "export_thread",
				"tool_args":          map[string]any{},
				"result_metadata":    cloneOutputMetadataMap(resultMetadata),
			}
			if presentation, ok := resultMetadata["tool_presentation"].(map[string]any); ok && len(presentation) > 0 {
				artifactMetadata["tool_presentation"] = cloneOutputMetadataMap(presentation)
			}
			language := "json"
			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				Result:         result,
				ResultMetadata: resultMetadata,
				Artifact: &ArtifactPayload{
					Type:     "file",
					Title:    fileName,
					Language: &language,
					Content:  string(payload),
					Metadata: artifactMetadata,
				},
			}, nil
		},
	}
}
