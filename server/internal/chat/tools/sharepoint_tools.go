package tools

import (
	"context"
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/sharepoint"
)

const (
	SharePointToolStatus          = "sharepoint_status"
	SharePointToolCheckConnection = "sharepoint_check_connection"
	SharePointToolSearch          = "sharepoint_search"
	SharePointToolGetMetadata     = "sharepoint_get_metadata"
	SharePointToolRead            = "sharepoint_read"
	SharePointToolSummarize       = "sharepoint_summarize"
	SharePointToolDownload        = "sharepoint_download"
)

var sharePointToolNames = []string{
	SharePointToolStatus,
	SharePointToolCheckConnection,
	SharePointToolSearch,
	SharePointToolGetMetadata,
	SharePointToolRead,
	SharePointToolSummarize,
	SharePointToolDownload,
}

func SharePointToolNames() []string {
	return append([]string(nil), sharePointToolNames...)
}

func IsSharePointToolName(toolName string) bool {
	name := strings.TrimSpace(toolName)
	for _, candidate := range sharePointToolNames {
		if name == candidate {
			return true
		}
	}
	return false
}

func newSharePointStatusTool(manager *sharepoint.Manager) Tool {
	return sharePointBridgeTool(manager, SharePointToolStatus, "SharePoint Browser Status", "Return the SharePoint browser-authenticated connection state.", map[string]any{})
}

func newSharePointCheckConnectionTool(manager *sharepoint.Manager) Tool {
	return sharePointBridgeTool(manager, SharePointToolCheckConnection, "Check SharePoint Connection", "Read-only check of the configured SharePoint site using the persistent browser SSO profile.", map[string]any{})
}

func newSharePointSearchTool(manager *sharepoint.Manager) Tool {
	return sharePointBridgeTool(manager, SharePointToolSearch, "Search SharePoint Files", "Search accessible SharePoint and OneDrive documents from the configured SharePoint tenant. Results include URLs, filenames, snippets, site URLs, and server-relative paths for follow-up read/download calls.", objectSchema(map[string]any{
		"query": stringSchema("Search text to find documents."),
		"row_limit": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"maximum":     50,
			"description": "Maximum search results to return. Defaults to 10.",
		},
		"file_extensions": map[string]any{
			"type":        "array",
			"description": "Optional file extensions to keep, for example [\"pdf\", \"docx\", \"xlsx\"].",
			"items":       map[string]any{"type": "string"},
		},
	}, []string{"query"}))
}

func newSharePointGetMetadataTool(manager *sharepoint.Manager) Tool {
	return sharePointBridgeTool(manager, SharePointToolGetMetadata, "Get SharePoint File Metadata", "Read SharePoint metadata for a file URL or server-relative URL. Use this before downloading when the search result needs verification.", sharePointFileTargetSchema(nil))
}

func newSharePointReadTool(manager *sharepoint.Manager) Tool {
	props := sharePointFileTargetProperties()
	props["max_chars"] = map[string]any{
		"type":        "integer",
		"minimum":     1000,
		"maximum":     100000,
		"description": "Maximum text preview characters for text-like files. Office, PDF, image, and binary files are saved for the app file parser and native model input.",
	}
	props["max_download_bytes"] = map[string]any{
		"type":        "integer",
		"minimum":     1,
		"maximum":     134217728,
		"description": "Maximum file bytes to save into the app-managed download cache. Defaults to 134217728.",
	}
	return sharePointBridgeTool(manager, SharePointToolRead, "Read SharePoint File", "Download a SharePoint file into the app-managed cache and promote it to the first-class file parser/native model file input path. Text-like files also return a bounded text preview.", objectSchema(props, nil))
}

func newSharePointSummarizeTool(manager *sharepoint.Manager) Tool {
	props := sharePointFileTargetProperties()
	props["focus"] = stringSchema("Optional summary focus. The model should use this after the file is promoted through native file input.")
	props["max_chars"] = map[string]any{
		"type":        "integer",
		"minimum":     1000,
		"maximum":     100000,
		"description": "Maximum text preview characters for text-like files.",
	}
	return sharePointBridgeTool(manager, SharePointToolSummarize, "Summarize SharePoint File", "Prepare a SharePoint file for summarization by downloading it into the app-managed cache and promoting it to the native file input path. The model summarizes from the promoted file content.", objectSchema(props, nil))
}

func newSharePointDownloadTool(manager *sharepoint.Manager) Tool {
	props := sharePointFileTargetProperties()
	props["filename"] = stringSchema("Optional local filename to use in the app-managed download cache.")
	props["max_download_bytes"] = map[string]any{
		"type":        "integer",
		"minimum":     1,
		"maximum":     134217728,
		"description": "Maximum file bytes to save into the app-managed download cache. Defaults to 134217728.",
	}
	return sharePointBridgeTool(manager, SharePointToolDownload, "Download SharePoint File", "Download a SharePoint file into the app-managed cache and return a local_path that can be parsed natively by the app and reused by other tools.", objectSchema(props, nil))
}

func sharePointFileTargetSchema(extra map[string]any) map[string]any {
	props := sharePointFileTargetProperties()
	for key, value := range extra {
		props[key] = value
	}
	return objectSchema(props, nil)
}

func sharePointFileTargetProperties() map[string]any {
	return map[string]any{
		"item_url":            stringSchema("Absolute SharePoint file URL from search results or the browser address bar."),
		"server_relative_url": stringSchema("Server-relative SharePoint file path, for example /sites/team/Shared Documents/file.docx."),
		"site_url":            stringSchema("Optional SharePoint site/web URL. Use the siteUrl/SPWebUrl from search results when present."),
	}
}

func sharePointBridgeTool(manager *sharepoint.Manager, name, title, description string, parameters map[string]any) Tool {
	parameters = normalizeObjectSchema(parameters)
	parameters["additionalProperties"] = false
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        name,
				Description: description,
				Parameters:  parameters,
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if manager == nil {
				return JSONError("SharePoint integration is not configured"), nil
			}
			var input map[string]any
			if len(strings.TrimSpace(string(args))) == 0 {
				input = map[string]any{}
			} else if err := json.Unmarshal(args, &input); err != nil {
				return JSONError(err.Error()), nil
			}
			emitSharePointProgress(exec, name, "browser_starting", "Starting SharePoint browser session", 8, nil)
			emitSharePointProgress(exec, name, sharePointProgressPhase(name), sharePointProgressLabel(name), 42, nil)
			result, err := manager.CallTool(ctx, name, input)
			if err != nil {
				emitSharePointProgress(exec, name, "validation_failed", err.Error(), -1, nil)
				return JSONError(err.Error()), nil
			}
			completeMeta := map[string]any{}
			if url, ok := result["url"].(string); ok && strings.TrimSpace(url) != "" {
				completeMeta["url"] = strings.TrimSpace(url)
			}
			emitSharePointProgress(exec, name, "browser_ready", sharePointProgressCompleteLabel(name), 100, completeMeta)
			return sharePointOutput(name, result), nil
		},
	}
}

func sharePointOutput(toolName string, result map[string]any) Output {
	out := cloneStringAnyMap(result)
	if out == nil {
		out = map[string]any{}
	}
	var bridgeMetadata map[string]any
	if raw, ok := out["result_metadata"].(map[string]any); ok {
		bridgeMetadata = cloneStringAnyMap(raw)
	}
	delete(out, "result_metadata")

	metadata := cloneStringAnyMap(bridgeMetadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	applySharePointMetadataDefaults(metadata, toolName)
	return JSONOKWithMetadata(out, metadata)
}

func applySharePointMetadataDefaults(metadata map[string]any, toolName string) {
	if metadata == nil {
		return
	}
	toolName = strings.TrimSpace(toolName)
	setSharePointMetadataValue(metadata, "provider", "sharepoint")
	setSharePointMetadataValue(metadata, "integration", "sharepoint")
	setSharePointMetadataValue(metadata, "product", "sharepoint")
	setSharePointMetadataValue(metadata, "tool_name", toolName)

	meta, ok := ToolMetadataByName(toolName)
	if !ok {
		setSharePointMetadataValue(metadata, "family", "sharepoint")
		setSharePointMetadataValue(metadata, "kind", "connector")
		setSharePointMetadataValue(metadata, "result_kind", "connector")
		setSharePointMetadataValue(metadata, "native_viewer", "connector_inspector")
		setSharePointMetadataValue(metadata, "renderer_strategy", "family")
		setSharePointMetadataValue(metadata, "renderer_key", "sharepoint")
		putSharePointToolPresentationDefaults(metadata, map[string]any{
			"family":            "sharepoint",
			"result_kind":       "connector",
			"renderer_strategy": "family",
			"renderer_key":      "sharepoint",
			"native_viewer":     "connector_inspector",
		})
		return
	}

	presentation := sharePointToolPresentationMap(meta.Presentation)
	setSharePointMetadataValue(metadata, "tool_set_id", meta.ToolSetID)
	setSharePointMetadataValue(metadata, "tool_set_name", meta.ToolSetName)
	setSharePointMetadataValue(metadata, "connector_id", meta.ConnectorID)
	setSharePointMetadataValue(metadata, "category", string(meta.Category))
	setSharePointMetadataValue(metadata, "origin", string(meta.Origin))
	setSharePointMetadataValue(metadata, "readonly", meta.Category == ToolCategoryRead)
	setSharePointMetadataValue(metadata, "family", meta.Presentation.Family)
	setSharePointMetadataValue(metadata, "kind", meta.Presentation.ResultKind)
	setSharePointMetadataValue(metadata, "result_kind", meta.Presentation.ResultKind)
	setSharePointMetadataValue(metadata, "native_viewer", meta.Presentation.NativeViewer)
	setSharePointMetadataValue(metadata, "renderer_strategy", meta.Presentation.RendererStrategy)
	setSharePointMetadataValue(metadata, "renderer_key", meta.Presentation.RendererKey)
	setSharePointMetadataValue(metadata, "file_capability", meta.Presentation.FileCapability)
	setSharePointMetadataValue(metadata, "artifact_types", cloneSharePointStringSlice(meta.Presentation.ArtifactTypes))
	setSharePointMetadataValue(metadata, "file_kinds", cloneSharePointStringSlice(meta.Presentation.FileKinds))
	setSharePointMetadataValue(metadata, "preview_modes", cloneSharePointStringSlice(meta.Presentation.PreviewModes))
	setSharePointMetadataValue(metadata, "source_refs", cloneSharePointStringSlice(meta.Presentation.SourceRefs))
	setSharePointMetadataValue(metadata, "produces_file_artifact", meta.Presentation.ProducesFileArtifact)
	setSharePointMetadataValue(metadata, "promotes_native_input", meta.Presentation.PromotesNativeInput)
	setSharePointMetadataValue(metadata, "native_links", cloneSharePointStringSlice(meta.Presentation.NativeLinks))
	putSharePointToolPresentationDefaults(metadata, presentation)
}

func sharePointToolPresentationMap(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putSharePointMetadataDefault(out, "family", p.Family)
	putSharePointMetadataDefault(out, "result_kind", p.ResultKind)
	putSharePointMetadataDefault(out, "renderer_strategy", p.RendererStrategy)
	putSharePointMetadataDefault(out, "renderer_key", p.RendererKey)
	putSharePointMetadataDefault(out, "native_viewer", p.NativeViewer)
	putSharePointMetadataDefault(out, "artifact_types", cloneSharePointStringSlice(p.ArtifactTypes))
	putSharePointMetadataDefault(out, "file_capability", p.FileCapability)
	putSharePointMetadataDefault(out, "file_kinds", cloneSharePointStringSlice(p.FileKinds))
	putSharePointMetadataDefault(out, "preview_modes", cloneSharePointStringSlice(p.PreviewModes))
	putSharePointMetadataDefault(out, "source_refs", cloneSharePointStringSlice(p.SourceRefs))
	putSharePointMetadataDefault(out, "produces_file_artifact", p.ProducesFileArtifact)
	putSharePointMetadataDefault(out, "promotes_native_input", p.PromotesNativeInput)
	putSharePointMetadataDefault(out, "native_links", cloneSharePointStringSlice(p.NativeLinks))
	return out
}

func putSharePointToolPresentationDefaults(metadata map[string]any, defaults map[string]any) {
	if len(defaults) == 0 {
		return
	}
	existing, _ := metadata["tool_presentation"].(map[string]any)
	presentation := cloneStringAnyMap(existing)
	if presentation == nil {
		presentation = map[string]any{}
	}
	for key, value := range defaults {
		setSharePointMetadataValue(presentation, key, value)
	}
	if len(presentation) > 0 {
		metadata["tool_presentation"] = presentation
	}
}

func setSharePointMetadataValue(metadata map[string]any, key string, value any) {
	key = strings.TrimSpace(key)
	if key == "" || !sharePointMetadataHasValue(value) {
		return
	}
	metadata[key] = value
}

func putSharePointMetadataDefault(metadata map[string]any, key string, value any) {
	key = strings.TrimSpace(key)
	if key == "" || !sharePointMetadataHasValue(value) || sharePointMetadataHasValue(metadata[key]) {
		return
	}
	metadata[key] = value
}

func sharePointMetadataHasValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []string:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func cloneSharePointStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func emitSharePointProgress(exec ExecContext, toolName, phase, label string, percent int, fields map[string]any) {
	if exec.OnProgress == nil {
		return
	}
	progress := map[string]any{
		"provider": "sharepoint",
		"product":  "sharepoint",
		"tool":     toolName,
		"phase":    phase,
		"label":    label,
	}
	if percent >= 0 {
		progress["percent"] = percent
	}
	for key, value := range fields {
		progress[key] = value
	}
	exec.OnProgress(progress)
}

func sharePointProgressPhase(toolName string) string {
	switch toolName {
	case SharePointToolSearch:
		return "searching"
	case SharePointToolRead, SharePointToolSummarize, SharePointToolDownload:
		return "downloading_file"
	case SharePointToolGetMetadata:
		return "reading_metadata"
	default:
		return "checking_connection"
	}
}

func sharePointProgressLabel(toolName string) string {
	switch toolName {
	case SharePointToolSearch:
		return "Searching SharePoint files"
	case SharePointToolRead:
		return "Reading SharePoint file"
	case SharePointToolSummarize:
		return "Preparing SharePoint file summary"
	case SharePointToolDownload:
		return "Downloading SharePoint file"
	case SharePointToolGetMetadata:
		return "Reading SharePoint file metadata"
	default:
		return "Checking SharePoint connection"
	}
}

func sharePointProgressCompleteLabel(toolName string) string {
	switch toolName {
	case SharePointToolSearch:
		return "SharePoint search complete"
	case SharePointToolRead:
		return "SharePoint file ready for native parsing"
	case SharePointToolSummarize:
		return "SharePoint file ready for summary"
	case SharePointToolDownload:
		return "SharePoint download complete"
	case SharePointToolGetMetadata:
		return "SharePoint metadata loaded"
	default:
		return "SharePoint browser command complete"
	}
}
