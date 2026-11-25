package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"ai-analyst-local/internal/browsercontrol"
	"ai-analyst-local/internal/chat/types"
)

const (
	BrowserControlToolStatus    = "cua_browser_status"
	BrowserControlToolNavigate  = "cua_browser_navigate"
	BrowserControlToolSnapshot  = "cua_browser_snapshot"
	BrowserControlToolClick     = "cua_browser_click"
	BrowserControlToolType      = "cua_browser_type"
	BrowserControlToolPress     = "cua_browser_press"
	BrowserControlToolWait      = "cua_browser_wait"
	BrowserControlToolBack      = "cua_browser_back"
	BrowserControlToolLogs      = "cua_browser_logs"
	BrowserControlToolClearLogs = "cua_browser_clear_logs"
)

var browserControlToolNames = []string{
	BrowserControlToolStatus,
	BrowserControlToolNavigate,
	BrowserControlToolSnapshot,
	BrowserControlToolClick,
	BrowserControlToolType,
	BrowserControlToolPress,
	BrowserControlToolWait,
	BrowserControlToolBack,
	BrowserControlToolLogs,
	BrowserControlToolClearLogs,
}

func BrowserControlToolNames() []string {
	return append([]string(nil), browserControlToolNames...)
}

func IsBrowserControlToolName(toolName string) bool {
	toolName = strings.TrimSpace(toolName)
	for _, candidate := range browserControlToolNames {
		if toolName == candidate {
			return true
		}
	}
	return false
}

func newBrowserControlStatusTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolStatus, "Browser Status", "Return the current local computer-use browser state, URL, and title.", map[string]any{})
}

func newBrowserControlNavigateTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolNavigate, "Navigate Browser", "Open a URL in the local computer-use browser. Use cua_browser_snapshot after navigation before clicking or typing.", objectSchema(map[string]any{
		"url": stringSchema("HTTP(S) URL to open. Localhost URLs are allowed."),
		"waitUntil": map[string]any{
			"type":        "string",
			"enum":        []string{"domcontentloaded", "load", "networkidle"},
			"description": "Optional Playwright load state. Defaults to domcontentloaded.",
		},
	}, []string{"url"}))
}

func newBrowserControlSnapshotTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolSnapshot, "Snapshot Browser", "Inspect visible page text and interactive elements in the local computer-use browser. Use returned 1-based element indexes for click/type actions.", objectSchema(map[string]any{
		"maxElements":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200},
		"maxTextChars": map[string]any{"type": "integer", "minimum": 200, "maximum": 30000},
	}, nil))
}

func newBrowserControlClickTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolClick, "Click Browser Element", "Click an element in the local computer-use browser by snapshot index, CSS selector, or visible text. Prefer a snapshot index from cua_browser_snapshot.", browserTargetSchema(nil))
}

func newBrowserControlTypeTool(manager *browsercontrol.Manager) Tool {
	properties := browserTargetProperties()
	properties["value"] = stringSchema("Text to enter into the target field.")
	properties["clear"] = map[string]any{
		"type":        "boolean",
		"description": "When true or omitted, replace the current field value. When false, append by typing.",
	}
	return browserControlBridgeTool(manager, BrowserControlToolType, "Type In Browser Element", "Enter text into a field in the local computer-use browser by snapshot index, CSS selector, or visible text.", objectSchema(properties, []string{"value"}))
}

func newBrowserControlPressTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolPress, "Press Browser Key", "Press a keyboard key in the local computer-use browser, for example Enter, Escape, Tab, ArrowDown, or Meta+L.", objectSchema(map[string]any{
		"key": stringSchema("Playwright keyboard key or shortcut, for example Enter, Escape, Tab, ArrowDown, Control+A, or Meta+L."),
	}, []string{"key"}))
}

func newBrowserControlWaitTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolWait, "Wait In Browser", "Wait briefly for page UI updates in the local computer-use browser.", objectSchema(map[string]any{
		"milliseconds": map[string]any{"type": "integer", "minimum": 100, "maximum": 30000},
	}, nil))
}

func newBrowserControlBackTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolBack, "Go Back In Browser", "Navigate back in the local computer-use browser history.", map[string]any{})
}

func newBrowserControlLogsTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolLogs, "Read Browser Logs", "Read recent computer-use browser console, page error, and network logs. Supports filtering by source, level, URL, text, method, resource type, status, age, and sequence. Network logs include URLs, methods, resource types, status, timing, failures, and response content type; request/response bodies and headers are not exposed.", objectSchema(map[string]any{
		"types":         browserLogStringArraySchema("Log sources to include. Defaults to all. Allowed values: console, pageerror, network, browser, all."),
		"levels":        browserLogStringArraySchema("Console/page-error levels to include, for example error, warning, log, info, debug."),
		"methods":       browserLogStringArraySchema("Network request methods to include, for example GET or POST."),
		"resourceTypes": browserLogStringArraySchema("Network resource types to include, for example document, xhr, fetch, script, stylesheet, image."),
		"statusCodes":   browserLogNumberArraySchema("HTTP status codes to include, for example 404 or 500."),
		"statusClass":   browserLogStringArraySchema("HTTP status classes to include, for example 2xx, 3xx, 4xx, or 5xx."),
		"statusMin":     map[string]any{"type": "integer", "minimum": 100, "maximum": 599, "description": "Minimum HTTP status to include."},
		"statusMax":     map[string]any{"type": "integer", "minimum": 100, "maximum": 599, "description": "Maximum HTTP status to include."},
		"urlContains":   stringSchema("Case-insensitive URL substring filter."),
		"urlRegex":      stringSchema("Case-insensitive regular expression matched against URL fields."),
		"textContains":  stringSchema("Case-insensitive substring filter across console text, page errors, URLs, statuses, and network failure text."),
		"textRegex":     stringSchema("Case-insensitive regular expression matched across console text, page errors, URLs, statuses, and network failure text."),
		"failedOnly":    map[string]any{"type": "boolean", "description": "When true, return network failures/HTTP 4xx+ and console/page errors only."},
		"sinceSequence": map[string]any{"type": "integer", "minimum": 0, "description": "Only return entries with sequence greater than this value. Use high_watermark_sequence from a previous call."},
		"sinceMs":       map[string]any{"type": "integer", "minimum": 1, "description": "Only return entries captured in the last N milliseconds."},
		"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Maximum entries to return. Defaults to 100."},
		"order":         map[string]any{"type": "string", "enum": []string{"asc", "desc"}, "description": "Return chronological ascending or descending entries. Defaults to asc."},
	}, nil))
}

func newBrowserControlClearLogsTool(manager *browsercontrol.Manager) Tool {
	return browserControlBridgeTool(manager, BrowserControlToolClearLogs, "Clear Browser Logs", "Clear captured computer-use browser console, page error, and network logs. Use before reproducing a UI issue when you want a focused log window.", objectSchema(map[string]any{
		"types": browserLogStringArraySchema("Optional log sources to clear. Defaults to all. Allowed values: console, pageerror, network, browser, all."),
	}, nil))
}

func browserTargetProperties() map[string]any {
	return map[string]any{
		"index": map[string]any{
			"type":        "integer",
			"minimum":     1,
			"description": "1-based interactive element index from cua_browser_snapshot.",
		},
		"selector": stringSchema("Optional CSS selector for the target element."),
		"text":     stringSchema("Optional visible text for the target element."),
	}
}

func browserTargetSchema(required []string) map[string]any {
	return objectSchema(browserTargetProperties(), required)
}

func browserLogStringArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       map[string]any{"type": "string"},
	}
}

func browserLogNumberArraySchema(description string) map[string]any {
	return map[string]any{
		"type":        "array",
		"description": description,
		"items":       map[string]any{"type": "integer"},
	}
}

func browserControlBridgeTool(manager *browsercontrol.Manager, name, title, description string, parameters map[string]any) Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        name,
				Description: description,
				Parameters:  browserControlParameters(name, parameters),
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var input map[string]any
			if len(strings.TrimSpace(string(args))) == 0 {
				input = map[string]any{}
			} else if err := json.Unmarshal(args, &input); err != nil {
				return JSONError(err.Error()), nil
			}
			dryRun := boolMapValueSingle(input, "dry_run")
			delete(input, "dry_run")
			if dryRun && browserControlSupportsDryRun(name) {
				result, metadata, err := browserControlDryRunResult(name, input)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				emitBrowserControlProgress(exec, name, "dry_run", "Validated browser command without running it", 100, browserControlProgressFields(name, input))
				return JSONOKWithMetadata(result, metadata), nil
			}
			if manager == nil {
				return JSONError("browser control integration is not configured"), nil
			}
			emitBrowserControlProgress(exec, name, "browser_starting", "Starting computer-use browser", 8, nil)
			emitBrowserControlProgress(exec, name, browserControlProgressPhase(name), browserControlProgressLabel(name, input), 42, browserControlProgressFields(name, input))
			result, err := manager.CallTool(ctx, name, input)
			if err != nil {
				emitBrowserControlProgress(exec, name, "validation_failed", err.Error(), -1, nil)
				return JSONError(err.Error()), nil
			}
			completeMeta := map[string]any{}
			if url, ok := result["url"].(string); ok && strings.TrimSpace(url) != "" {
				completeMeta["url"] = strings.TrimSpace(url)
			}
			emitBrowserControlProgress(exec, name, "browser_ready", browserControlProgressCompleteLabel(name), 100, completeMeta)
			return JSONOKWithMetadata(result, browserControlResultMetadata(name, browserControlResultKind(name), "live", result)), nil
		},
	}
}

func browserControlParameters(toolName string, parameters map[string]any) map[string]any {
	parameters = normalizeObjectSchema(parameters)
	parameters["additionalProperties"] = false
	if !browserControlSupportsDryRun(toolName) {
		return parameters
	}
	out := map[string]any{}
	for key, value := range parameters {
		out[key] = value
	}
	if out["type"] != "object" {
		out["type"] = "object"
	}
	properties, _ := out["properties"].(map[string]any)
	if properties == nil {
		properties = map[string]any{}
	} else {
		copied := map[string]any{}
		for key, value := range properties {
			copied[key] = value
		}
		properties = copied
	}
	properties["dry_run"] = map[string]any{
		"type":        "boolean",
		"description": "Validate and preview this browser action without changing browser state.",
	}
	out["properties"] = properties
	return out
}

func browserControlSupportsDryRun(toolName string) bool {
	switch toolName {
	case BrowserControlToolNavigate,
		BrowserControlToolClick,
		BrowserControlToolType,
		BrowserControlToolPress,
		BrowserControlToolWait,
		BrowserControlToolBack,
		BrowserControlToolClearLogs:
		return true
	default:
		return false
	}
}

func browserControlDryRunResult(toolName string, args map[string]any) (map[string]any, map[string]any, error) {
	if err := validateBrowserControlDryRunArgs(toolName, args); err != nil {
		return nil, nil, err
	}
	result := map[string]any{
		"ok":            true,
		"dry_run":       true,
		"tool_name":     toolName,
		"would_execute": toolName,
		"message":       "Dry run only. Browser state was not changed.",
	}
	if fields := browserControlProgressFields(toolName, args); len(fields) > 0 {
		result["preview"] = fields
	}
	metadata := browserControlResultMetadata(toolName, "browser_dry_run", "dry_run", result)
	return result, metadata, nil
}

func browserControlResultMetadata(toolName, kind, mode string, result map[string]any) map[string]any {
	toolName = strings.TrimSpace(toolName)
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "browser_result"
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "live"
	}
	meta := map[string]any{
		"provider":  "browser_control",
		"product":   "browser",
		"family":    "browser",
		"kind":      kind,
		"mode":      mode,
		"tool_name": toolName,
		"readonly":  true,
	}
	if mode == "dry_run" {
		meta["would_execute"] = toolName
	}
	if url := firstStringMapValue(result, "url"); url != "" {
		meta["url"] = url
		meta["browser_url"] = url
	}
	if title := firstStringMapValue(result, "title"); title != "" {
		meta["title"] = title
	}
	if preview, ok := result["preview"].(map[string]any); ok && len(preview) > 0 {
		meta["preview"] = preview
	}
	if toolMeta, ok := ToolMetadataByName(toolName); ok {
		meta["tool_name"] = toolMeta.Name
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := browserControlPresentationMetadata(toolMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
			if v := firstStringMapValue(presentation, "family"); v != "" {
				meta["family"] = v
			}
			if v := firstStringMapValue(presentation, "result_kind"); v != "" {
				meta["result_kind"] = v
			}
			if v := firstStringMapValue(presentation, "native_viewer"); v != "" {
				meta["native_viewer"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_strategy"); v != "" {
				meta["renderer_strategy"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_key"); v != "" {
				meta["renderer_key"] = v
			}
			if v := firstStringMapValue(presentation, "file_capability"); v != "" {
				meta["file_capability"] = v
			}
		}
	}
	return meta
}

func browserControlResultKind(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case BrowserControlToolStatus:
		return "browser_status"
	case BrowserControlToolSnapshot:
		return "browser_snapshot"
	case BrowserControlToolLogs:
		return "browser_logs"
	case BrowserControlToolClearLogs:
		return "browser_logs_clear"
	default:
		return "browser_action"
	}
}

func browserControlPresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putBrowserControlPresentationString(out, "family", p.Family)
	putBrowserControlPresentationString(out, "result_kind", p.ResultKind)
	putBrowserControlPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putBrowserControlPresentationString(out, "renderer_key", p.RendererKey)
	putBrowserControlPresentationString(out, "native_viewer", p.NativeViewer)
	putBrowserControlPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putBrowserControlPresentationString(out, "file_capability", p.FileCapability)
	putBrowserControlPresentationStrings(out, "file_kinds", p.FileKinds)
	putBrowserControlPresentationStrings(out, "preview_modes", p.PreviewModes)
	putBrowserControlPresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putBrowserControlPresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func putBrowserControlPresentationString(out map[string]any, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	out[key] = value
}

func putBrowserControlPresentationStrings(out map[string]any, key string, values []string) {
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

func validateBrowserControlDryRunArgs(toolName string, args map[string]any) error {
	textArg := func(key string) string {
		if value, ok := args[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	hasTarget := func() bool {
		if _, ok := args["index"]; ok {
			return true
		}
		return textArg("selector") != "" || textArg("text") != ""
	}
	switch toolName {
	case BrowserControlToolNavigate:
		url := textArg("url")
		if url == "" {
			return fmt.Errorf("url is required")
		}
		if !strings.HasPrefix(strings.ToLower(url), "http://") && !strings.HasPrefix(strings.ToLower(url), "https://") {
			return fmt.Errorf("url must start with http:// or https://")
		}
	case BrowserControlToolClick:
		if !hasTarget() {
			return fmt.Errorf("index, selector, or text is required")
		}
	case BrowserControlToolType:
		if _, ok := args["value"]; !ok {
			return fmt.Errorf("value is required")
		}
		if !hasTarget() {
			return fmt.Errorf("index, selector, or text is required")
		}
	case BrowserControlToolPress:
		if textArg("key") == "" {
			return fmt.Errorf("key is required")
		}
	case BrowserControlToolWait:
		if raw, ok := args["milliseconds"]; ok {
			ms, ok := numericMapValue(raw)
			if !ok || ms < 100 || ms > 30000 {
				return fmt.Errorf("milliseconds must be between 100 and 30000")
			}
		}
	}
	return nil
}

func boolMapValueSingle(args map[string]any, key string) bool {
	value, ok := args[key]
	if !ok {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return strings.EqualFold(strings.TrimSpace(v), "true")
	default:
		return false
	}
}

func numericMapValue(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	default:
		return 0, false
	}
}

func emitBrowserControlProgress(exec ExecContext, toolName, phase, label string, percent int, fields map[string]any) {
	if exec.OnProgress == nil {
		return
	}
	progress := map[string]any{
		"tool_name": toolName,
		"provider":  "browser_control",
		"product":   "browser",
		"phase":     phase,
		"label":     label,
	}
	if percent >= 0 {
		progress["percent"] = percent
	}
	for key, value := range fields {
		progress[key] = value
	}
	exec.OnProgress(progress)
}

func browserControlProgressPhase(toolName string) string {
	switch toolName {
	case BrowserControlToolSnapshot, BrowserControlToolStatus:
		return "browser_reading"
	case BrowserControlToolLogs:
		return "browser_reading_logs"
	case BrowserControlToolClearLogs:
		return "browser_clearing_logs"
	default:
		return "browser_navigating"
	}
}

func browserControlProgressLabel(toolName string, args map[string]any) string {
	textArg := func(key string) string {
		if value, ok := args[key].(string); ok {
			return strings.TrimSpace(value)
		}
		return ""
	}
	switch toolName {
	case BrowserControlToolStatus:
		return "Inspecting browser state"
	case BrowserControlToolNavigate:
		if url := textArg("url"); url != "" {
			return "Opening " + url
		}
		return "Opening browser URL"
	case BrowserControlToolSnapshot:
		return "Reading page text and controls"
	case BrowserControlToolClick:
		return "Clicking page element"
	case BrowserControlToolType:
		return "Typing into page element"
	case BrowserControlToolPress:
		if key := textArg("key"); key != "" {
			return "Pressing " + key
		}
		return "Pressing browser key"
	case BrowserControlToolWait:
		return "Waiting for page updates"
	case BrowserControlToolBack:
		return "Going back"
	case BrowserControlToolLogs:
		return "Reading browser console and network logs"
	case BrowserControlToolClearLogs:
		return "Clearing browser logs"
	default:
		return "Running browser command"
	}
}

func browserControlProgressFields(toolName string, args map[string]any) map[string]any {
	fields := map[string]any{}
	if toolName == BrowserControlToolNavigate {
		if url, ok := args["url"].(string); ok && strings.TrimSpace(url) != "" {
			fields["url"] = strings.TrimSpace(url)
		}
	}
	if index, ok := args["index"]; ok {
		fields["target"] = fmt.Sprintf("element %v", index)
	} else if selector, ok := args["selector"].(string); ok && strings.TrimSpace(selector) != "" {
		fields["target"] = strings.TrimSpace(selector)
	} else if text, ok := args["text"].(string); ok && strings.TrimSpace(text) != "" {
		fields["target"] = strings.TrimSpace(text)
	}
	return fields
}

func browserControlProgressCompleteLabel(toolName string) string {
	switch toolName {
	case BrowserControlToolSnapshot:
		return "Browser snapshot ready"
	case BrowserControlToolStatus:
		return "Browser state ready"
	case BrowserControlToolNavigate:
		return "Browser navigation complete"
	case BrowserControlToolClick:
		return "Browser click complete"
	case BrowserControlToolType:
		return "Browser typing complete"
	case BrowserControlToolPress:
		return "Browser key press complete"
	case BrowserControlToolWait:
		return "Browser wait complete"
	case BrowserControlToolBack:
		return "Browser back navigation complete"
	case BrowserControlToolLogs:
		return "Browser logs ready"
	case BrowserControlToolClearLogs:
		return "Browser logs cleared"
	default:
		return "Browser command complete"
	}
}
