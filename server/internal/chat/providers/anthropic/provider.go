package anthropic

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/requestguard"
	"ai-analyst-local/internal/chat/providers/stream"
	"ai-analyst-local/internal/chat/providers/toolnamemapper"
	"ai-analyst-local/internal/chat/providers/toolschema"
	"ai-analyst-local/internal/chat/providers/vision"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultAnthropicInputTokenLimit = 180000
	defaultAnthropicHardContext     = 200000
	defaultAnthropicCharsPerToken   = 3
	defaultCompactionBeta           = "compact-2026-01-12"
	defaultCompactionEditType       = "compact_20260112"
	defaultCompactionInstruction    = "Create a compact but complete handover summary. Include the latest user request, confirmed findings, key evidence (queries, jobs, artifacts, and message references), decisions made, and remaining next steps. Preserve critical context needed to continue accurately. Do not draft or continue the user-facing reply; output summary content only."
	defaultCompactionTriggerTokens  = 170000
	defaultFilesAPIBeta             = "files-api-2025-04-14"
	defaultSkillsBeta               = "skills-2025-10-02"
	minCompactionTriggerTokens      = 50000
)

var anthropicModelContextProfiles = []struct {
	prefix            string
	inputTokenLimit   int
	hardContextTokens int
}{
	{"claude-2", 100000, 100000},
	{"claude-3", 200000, 200000},
	{"claude-opus-4", 200000, 200000},
	{"claude-sonnet-4", 200000, 200000},
}

func ModelContextProfile(model string) (inputTokenLimit int, hardContextTokens int, charsPerToken int, ok bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return 0, 0, 0, false
	}
	if !strings.Contains(m, "claude-") {
		return 0, 0, 0, false
	}

	for _, p := range anthropicModelContextProfiles {
		if strings.Contains(m, p.prefix) {
			return p.inputTokenLimit, p.hardContextTokens, defaultAnthropicCharsPerToken, true
		}
	}
	return defaultAnthropicInputTokenLimit, defaultAnthropicHardContext, defaultAnthropicCharsPerToken, true
}

type Provider struct {
	providerID            string
	model                 string
	baseURL               string
	version               string
	compactionEnabled     bool
	compactionInstruction string
	compactionTrigger     int
	webSearch             *WebSearchConfig
	codeExecution         *CodeExecutionConfig
	headers               map[string]string
	prepareRequest        func(*http.Request, []byte) error
	disableStreaming      bool
	bedrockMessagesShape  bool
	maxRequestBytes       int64
	client                *http.Client
}

type WebSearchConfig struct {
	Enabled        bool
	ToolType       string
	MaxUses        *int
	AllowedDomains []string
	BlockedDomains []string
	AllowedCallers []string
}

type CodeExecutionSkill struct {
	Type    string
	SkillID string
	Version *string
}

type CodeExecutionConfig struct {
	Enabled       bool
	ToolType      string
	ContainerMode string
	ContainerID   string
	MemoryLimit   string
	Skills        []CodeExecutionSkill
}

type Options struct {
	ProviderID            string
	Model                 string
	BaseURL               string
	Version               string
	Headers               map[string]string
	CompactionEnabled     bool
	CompactionInstruction string
	CompactionTrigger     int
	WebSearch             *WebSearchConfig
	CodeExecution         *CodeExecutionConfig
	PrepareRequest        func(*http.Request, []byte) error
	DisableStreaming      bool
	BedrockMessagesShape  bool
	MaxRequestBytes       int64
}

func New(providerID, model, baseURL, version string, headers map[string]string, compactionEnabled bool, compactionInstruction string, compactionTrigger int) *Provider {
	return NewWithOptions(Options{
		ProviderID:            providerID,
		Model:                 model,
		BaseURL:               baseURL,
		Version:               version,
		Headers:               headers,
		CompactionEnabled:     compactionEnabled,
		CompactionInstruction: compactionInstruction,
		CompactionTrigger:     compactionTrigger,
	})
}

func NewWithOptions(opts Options) *Provider {
	b := normalizeMessagesBaseURL(opts.BaseURL, opts.BedrockMessagesShape)
	v := strings.TrimSpace(opts.Version)
	if v == "" {
		v = "2023-06-01"
	}
	compactionInstr := strings.TrimSpace(opts.CompactionInstruction)
	if compactionInstr == "" {
		compactionInstr = defaultCompactionInstruction
	}
	compactionTrigger := opts.CompactionTrigger
	if compactionTrigger < minCompactionTriggerTokens {
		compactionTrigger = defaultCompactionTriggerTokens
	}
	merged := map[string]string{}
	for k, val := range opts.Headers {
		merged[k] = val
	}
	merged["Content-Type"] = "application/json"
	if !opts.BedrockMessagesShape {
		merged["anthropic-version"] = v
	}
	if opts.BedrockMessagesShape && !opts.DisableStreaming && !hasHeaderKey(merged, "Accept") {
		merged["Accept"] = "text/event-stream"
	}
	merged["Accept-Encoding"] = "identity"

	return &Provider{
		providerID:            opts.ProviderID,
		model:                 opts.Model,
		baseURL:               b,
		version:               v,
		compactionEnabled:     opts.CompactionEnabled,
		compactionInstruction: compactionInstr,
		compactionTrigger:     compactionTrigger,
		webSearch:             cloneWebSearchConfig(opts.WebSearch),
		codeExecution:         cloneCodeExecutionConfig(opts.CodeExecution),
		headers:               merged,
		prepareRequest:        opts.PrepareRequest,
		disableStreaming:      opts.DisableStreaming,
		bedrockMessagesShape:  opts.BedrockMessagesShape,
		maxRequestBytes:       requestguard.NormalizeMaxBytes(opts.MaxRequestBytes),
		client:                &http.Client{Timeout: 0},
	}
}

func normalizeMessagesBaseURL(base string, bedrockMessagesShape bool) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return b
	}
	for _, suffix := range []string{"/v1/messages", "/v1"} {
		if strings.HasSuffix(strings.ToLower(b), suffix) {
			b = strings.TrimRight(b[:len(b)-len(suffix)], "/")
			break
		}
	}
	if !bedrockMessagesShape {
		return b
	}

	u, err := url.Parse(b)
	if err != nil || u.Host == "" {
		return b
	}
	if !strings.Contains(strings.ToLower(u.Host), "bedrock-mantle.") {
		return b
	}

	path := strings.TrimRight(u.Path, "/")
	lowerPath := strings.ToLower(path)
	switch {
	case path == "" || path == "/":
		u.Path = "/anthropic"
	case lowerPath == "/anthropic" || strings.HasSuffix(lowerPath, "/anthropic"):
		u.Path = path
	case strings.Contains(lowerPath, "/anthropic/"):
		u.Path = path
	default:
		u.Path = path + "/anthropic"
	}
	u.RawPath = ""
	return strings.TrimRight(u.String(), "/")
}

func hasHeaderKey(headers map[string]string, key string) bool {
	want := strings.TrimSpace(key)
	if want == "" {
		return false
	}
	for existing := range headers {
		if strings.EqualFold(strings.TrimSpace(existing), want) {
			return true
		}
	}
	return false
}

func (p *Provider) ID() string    { return p.providerID }
func (p *Provider) Model() string { return p.model }

func (p *Provider) Capabilities() capabilities.ModelCapabilities {
	limits := capabilities.AnthropicModelTokenLimits(p.model)
	return capabilities.Normalize(capabilities.ModelCapabilities{
		ProviderID:      p.providerID,
		Model:           p.model,
		MaxInputTokens:  limits.MaxInputTokens,
		MaxOutputTokens: limits.MaxOutputTokens,
		NativeCompaction: capabilities.NativeCompactionSupport{
			Mode:          capabilities.NativeCompactionAnthropicContextManagementEdits,
			Enabled:       p.compactionEnabled,
			TriggerTokens: p.compactionTrigger,
			Beta:          defaultCompactionBeta,
			EditType:      defaultCompactionEditType,
		},
		OpaqueCompactionPreservation: capabilities.OpaqueCompactionPreservation{
			ProviderPayload: true,
			ProviderState:   true,
		},
	})
}

type streamEnvelope struct {
	Type         *string `json:"type,omitempty"`
	Message      any     `json:"message,omitempty"`
	Index        *int    `json:"index,omitempty"`
	ContentBlock any     `json:"content_block,omitempty"`
	Delta        any     `json:"delta,omitempty"`
	Usage        any     `json:"usage,omitempty"`
	Error        any     `json:"error,omitempty"`
}

type choicesStreamChunk struct {
	Handled       bool
	Text          string
	StopReason    string
	RequestID     *string
	ResolvedModel *string
	InputTokens   *int
	OutputTokens  *int
	TotalTokens   *int
	Usage         anthropicUsage
}

func coerceInt(v any) *int {
	switch t := v.(type) {
	case float64:
		i := int(t)
		if i < 0 {
			return nil
		}
		return &i
	case json.Number:
		if n, err := t.Int64(); err == nil {
			i := int(n)
			if i < 0 {
				return nil
			}
			return &i
		}
	case int:
		i := t
		if i < 0 {
			return nil
		}
		return &i
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		if n, err := json.Number(strings.TrimSpace(t)).Int64(); err == nil {
			i := int(n)
			if i < 0 {
				return nil
			}
			return &i
		}
	}
	return nil
}

func sanitizeCompactionSummary(raw string) string {
	summary := strings.TrimSpace(raw)
	if summary == "" {
		return ""
	}

	lower := strings.ToLower(summary)
	markers := []string{
		"thread handover summary",
		"handover summary",
		"context summary",
		"conversation summary",
	}
	best := -1
	for _, marker := range markers {
		if idx := strings.Index(lower, marker); idx >= 0 && (best < 0 || idx < best) {
			best = idx
		}
	}
	if best > 0 && best <= 400 {
		start := 0
		if lineStart := strings.LastIndex(summary[:best], "\n"); lineStart >= 0 {
			start = lineStart + 1
		}
		summary = strings.TrimSpace(summary[start:])
	}
	return summary
}

func cloneWebSearchConfig(in *WebSearchConfig) *WebSearchConfig {
	if in == nil {
		return nil
	}
	out := *in
	if in.MaxUses != nil {
		v := *in.MaxUses
		out.MaxUses = &v
	}
	out.AllowedDomains = append([]string(nil), in.AllowedDomains...)
	out.BlockedDomains = append([]string(nil), in.BlockedDomains...)
	out.AllowedCallers = append([]string(nil), in.AllowedCallers...)
	return &out
}

func cloneCodeExecutionConfig(in *CodeExecutionConfig) *CodeExecutionConfig {
	if in == nil {
		return nil
	}
	out := *in
	out.ToolType = strings.TrimSpace(in.ToolType)
	out.ContainerMode = strings.TrimSpace(in.ContainerMode)
	out.ContainerID = strings.TrimSpace(in.ContainerID)
	out.MemoryLimit = strings.TrimSpace(in.MemoryLimit)
	if len(in.Skills) > 0 {
		out.Skills = make([]CodeExecutionSkill, 0, len(in.Skills))
		for _, skill := range in.Skills {
			cloned := skill
			cloned.Type = strings.TrimSpace(skill.Type)
			cloned.SkillID = strings.TrimSpace(skill.SkillID)
			if skill.Version != nil {
				v := strings.TrimSpace(*skill.Version)
				cloned.Version = &v
			}
			out.Skills = append(out.Skills, cloned)
		}
	}
	return &out
}

func deepCopyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func cloneRawJSON(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func orderedContentBlocks(blocks map[int]map[string]any) []any {
	if len(blocks) == 0 {
		return nil
	}
	indexes := make([]int, 0, len(blocks))
	for idx := range blocks {
		indexes = append(indexes, idx)
	}
	sort.Ints(indexes)
	out := make([]any, 0, len(indexes))
	for _, idx := range indexes {
		if block := deepCopyMap(blocks[idx]); len(block) > 0 {
			out = append(out, block)
		}
	}
	return out
}

func textFromContentBlocks(blocks []any) string {
	if len(blocks) == 0 {
		return ""
	}
	var b strings.Builder
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block == nil {
			continue
		}
		switch kind, _ := block["type"].(string); kind {
		case "text":
			if text, ok := block["text"].(string); ok && text != "" {
				b.WriteString(text)
			}
		case "":
			if text, ok := block["text"].(string); ok && text != "" {
				b.WriteString(text)
			}
		}
	}
	return b.String()
}

func contentBlocksFromAny(raw any) []any {
	switch typed := raw.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			switch block := item.(type) {
			case map[string]any:
				if copied := normalizeContentBlockKeys(deepCopyMap(block)); len(copied) > 0 {
					out = append(out, copied)
				}
			case string:
				if strings.TrimSpace(block) != "" {
					out = append(out, map[string]any{"type": "text", "text": block})
				}
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case string:
		if strings.TrimSpace(typed) == "" {
			return nil
		}
		return []any{map[string]any{"type": "text", "text": typed}}
	case map[string]any:
		if copied := normalizeContentBlockKeys(deepCopyMap(typed)); len(copied) > 0 {
			return []any{copied}
		}
		return nil
	default:
		return nil
	}
}

func normalizeContentBlockKeys(block map[string]any) map[string]any {
	if len(block) == 0 {
		return block
	}
	if _, ok := block["type"]; !ok {
		if kind := firstNonEmptyString(valueByAnyKey(block, "Type")); kind != "" {
			block["type"] = strings.ToLower(kind)
		}
	}
	if _, ok := block["text"]; !ok {
		if text := firstStringPreserveWhitespace(valueByAnyKey(block, "Text")); text != "" {
			block["type"] = "text"
			block["text"] = text
		}
	}
	if _, ok := block["tool_use_id"]; !ok {
		if id := firstNonEmptyString(valueByAnyKey(block, "ToolUseId", "ToolUseID")); id != "" {
			block["tool_use_id"] = id
		}
	}
	if _, ok := block["content"]; !ok {
		if content := valueByAnyKey(block, "Content"); content != nil {
			block["content"] = content
		}
	}
	return block
}

func toolCallsFromAnthropicBlocks(blocks []any, mapper *toolnamemapper.Mapper) []types.ToolCall {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0)
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block == nil {
			continue
		}
		if kind, _ := block["type"].(string); kind != "tool_use" {
			continue
		}
		name, _ := block["name"].(string)
		id, _ := block["id"].(string)
		if strings.TrimSpace(id) == "" {
			id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		}
		args := map[string]any{}
		if input, ok := block["input"].(map[string]any); ok {
			args = input
		}
		argsText := "{}"
		if b, err := json.Marshal(args); err == nil {
			argsText = string(b)
		}
		out = append(out, types.ToolCall{
			ID:       id,
			Name:     mapper.CanonicalName(name),
			Args:     args,
			ArgsText: argsText,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func providerExecutedToolCallsFromBlocks(blocks []any) []types.ToolCall {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0)
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block == nil {
			continue
		}
		if kind, _ := block["type"].(string); kind != "server_tool_use" {
			continue
		}
		name := strings.TrimSpace(stringValue(block["name"]))
		if name == "" {
			name = "provider_tool"
		}
		id := strings.TrimSpace(stringValue(block["id"]))
		if id == "" {
			id = "srvtool_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		}
		args := map[string]any{}
		if input, ok := block["input"].(map[string]any); ok {
			args = deepCopyMap(input)
		}
		argsText := "{}"
		if b, err := json.Marshal(args); err == nil {
			argsText = string(b)
		}
		out = append(out, types.ToolCall{
			ID:               id,
			Name:             name,
			Args:             args,
			ArgsText:         argsText,
			ProviderExecuted: true,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func providerExecutedToolResultsFromBlocks(blocks []any, calls []types.ToolCall) []types.ChatMessage {
	if len(blocks) == 0 {
		return nil
	}
	toolNameByID := map[string]string{}
	argsByID := map[string]map[string]any{}
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		toolNameByID[id] = strings.TrimSpace(call.Name)
		if call.Args != nil {
			argsByID[id] = call.Args
		}
	}

	out := make([]types.ChatMessage, 0)
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block == nil {
			continue
		}
		kind := strings.TrimSpace(stringValue(block["type"]))
		if !strings.HasSuffix(kind, "_tool_result") {
			continue
		}
		toolCallID := strings.TrimSpace(stringValue(block["tool_use_id"]))
		if toolCallID == "" {
			toolCallID = strings.TrimSpace(stringValue(block["id"]))
		}
		if toolCallID == "" {
			continue
		}
		toolName := strings.TrimSpace(toolNameByID[toolCallID])
		if toolName == "" {
			toolName = strings.TrimSuffix(kind, "_tool_result")
		}

		args := argsByID[toolCallID]
		result := providerToolResultPayload(toolName, args, block)
		env := map[string]any{
			"artifact_id":     "provider_" + strings.ReplaceAll(types.NewUUID(), "-", ""),
			"tool_name":       toolName,
			"tool_call_id":    toolCallID,
			"ok":              !providerToolResultHasError(block),
			"result_format":   "json",
			"result":          result,
			"result_metadata": anthropicProviderHostedResultMetadata(toolName, kind),
		}
		if len(args) > 0 {
			env["tool_args"] = deepCopyMap(args)
		}
		contentBytes, err := json.Marshal(env)
		if err != nil {
			continue
		}
		content := string(contentBytes)
		out = append(out, types.ChatMessage{
			Role:       types.RoleTool,
			Content:    &content,
			ToolCallID: toolCallID,
			Name:       toolName,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func anthropicProviderHostedResultMetadata(toolName, blockType string) map[string]any {
	adapterFamily := "anthropic-messages"
	toolName = strings.TrimSpace(toolName)
	presentation := anthropicProviderHostedPresentationMap(chattools.ToolPresentationSpec{
		Family:           "provider-hosted",
		ResultKind:       "connector",
		RendererStrategy: "family",
		RendererKey:      "provider-hosted",
		NativeViewer:     "connector_inspector",
	})
	meta := map[string]any{
		"provider_tool":           true,
		"provider_hosted_tool_id": adapterFamily + "/" + toolName,
		"adapter_family":          adapterFamily,
		"block_type":              strings.TrimSpace(blockType),
		"tool_set_id":             "provider-hosted",
		"tool_set_name":           "Model provider capabilities",
		"origin":                  "provider",
		"permission_subject_kind": "provider_hosted_tool",
		"tool_presentation":       presentation,
		"presentation":            presentation,
		"family":                  "provider-hosted",
		"result_kind":             "connector",
		"renderer_strategy":       "family",
		"renderer_key":            "provider-hosted",
		"native_viewer":           "connector_inspector",
	}
	for _, capability := range chattools.ProviderHostedToolCapabilities() {
		if capability.AdapterFamily == adapterFamily && capability.Name == toolName {
			presentation = anthropicProviderHostedPresentationMap(capability.Presentation)
			meta["provider_hosted_tool_id"] = capability.ID
			meta["category"] = string(capability.Category)
			meta["readonly"] = capability.Category == chattools.ToolCategoryRead
			meta["tool_set_id"] = capability.ToolSetID
			meta["tool_set_name"] = capability.ToolSetName
			meta["origin"] = capability.Origin
			meta["settings_key"] = capability.SettingsKey
			meta["permission_subject_kind"] = capability.PermissionSubjectKind
			meta["tool_presentation"] = presentation
			meta["presentation"] = presentation
			meta["test"] = capability.Test
			meta["family"] = capability.Presentation.Family
			meta["result_kind"] = capability.Presentation.ResultKind
			meta["renderer_strategy"] = capability.Presentation.RendererStrategy
			meta["renderer_key"] = capability.Presentation.RendererKey
			meta["native_viewer"] = capability.Presentation.NativeViewer
			break
		}
	}
	return meta
}

func anthropicProviderHostedPresentationMap(p chattools.ToolPresentationSpec) map[string]any {
	return map[string]any{
		"family":            p.Family,
		"result_kind":       p.ResultKind,
		"renderer_strategy": p.RendererStrategy,
		"renderer_key":      p.RendererKey,
		"native_viewer":     p.NativeViewer,
	}
}

func providerToolResultPayload(toolName string, args map[string]any, block map[string]any) map[string]any {
	payload := map[string]any{}
	if query := strings.TrimSpace(stringValue(args["query"])); query != "" {
		payload["query"] = query
	}
	if content, ok := block["content"]; ok {
		payload["results"] = sanitizeProviderToolResultContent(content)
	}
	if code := strings.TrimSpace(stringValue(block["error_code"])); code != "" {
		payload["error_code"] = code
	}
	if message := strings.TrimSpace(stringValue(block["message"])); message != "" {
		payload["message"] = message
	}
	if len(payload) == 0 {
		payload["raw"] = sanitizeProviderToolResultContent(block)
	}
	return payload
}

func providerToolResultHasError(block map[string]any) bool {
	if strings.TrimSpace(stringValue(block["error_code"])) != "" {
		return true
	}
	if strings.TrimSpace(stringValue(block["error"])) != "" {
		return true
	}
	return false
}

func anthropicEmptyResponseMessage(stopReason string, requestID *string, blocks []any, debugSummary ...string) string {
	typesSeen := make([]string, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block == nil {
			continue
		}
		kind := strings.TrimSpace(stringValue(block["type"]))
		if kind == "" {
			kind = "unknown"
		}
		typesSeen = append(typesSeen, kind)
	}
	request := ""
	if requestID != nil && strings.TrimSpace(*requestID) != "" {
		request = ", request_id=" + strings.TrimSpace(*requestID)
	}
	reason := strings.TrimSpace(stopReason)
	if reason == "" {
		reason = "unknown"
	}
	blockSummary := "none"
	if len(typesSeen) > 0 {
		blockSummary = strings.Join(typesSeen, ",")
	}
	detail := ""
	if len(debugSummary) > 0 {
		debugParts := make([]string, 0, len(debugSummary))
		for _, summary := range debugSummary {
			if trimmed := strings.TrimSpace(summary); trimmed != "" {
				debugParts = append(debugParts, trimmed)
			}
		}
		if len(debugParts) > 0 {
			detail = ", " + strings.Join(debugParts, ", ")
		}
	}
	return fmt.Sprintf("Anthropic Messages returned no assistant text or visible provider tool output (stop_reason=%s%s, content_blocks=%d, block_types=%s%s)", reason, request, len(blocks), blockSummary, detail)
}

func anthropicRequestDebugSummary(body map[string]any, betas []string, bedrockMessagesShape bool) string {
	if len(body) == 0 {
		return "request_keys=none"
	}
	shape := "anthropic_messages"
	if bedrockMessagesShape {
		shape = "bedrock_messages"
	}
	parts := []string{
		"request_shape=" + shape,
		"request_keys=" + strings.Join(sortedKeys(body), "|"),
	}
	if streamValue, ok := body["stream"]; ok {
		parts = append(parts, fmt.Sprintf("request_stream=%v", streamValue))
	} else {
		parts = append(parts, "request_stream=omitted")
	}
	if len(betas) > 0 {
		parts = append(parts, "request_anthropic_beta="+strings.Join(betas, "|"))
	} else {
		parts = append(parts, "request_anthropic_beta=none")
	}
	if messages, ok := body["messages"].([]any); ok {
		parts = append(parts, fmt.Sprintf("request_messages=%d", len(messages)))
		if len(messages) > 0 {
			if msg := mapFromAny(messages[0]); len(msg) > 0 {
				parts = append(parts, "first_message_keys="+strings.Join(sortedKeys(msg), "|"))
				parts = append(parts, "first_message_content="+anthropicRequestContentShape(msg["content"]))
			}
		}
	}
	if tools, ok := body["tools"].([]any); ok {
		toolTypes := make([]string, 0, len(tools))
		for _, rawTool := range tools {
			tool := mapFromAny(rawTool)
			toolTypes = append(toolTypes, firstNonEmptyString(tool["type"], tool["name"]))
		}
		parts = append(parts, fmt.Sprintf("request_tools=%d:%s", len(tools), strings.Join(toolTypes, "|")))
	}
	if toolConfig := mapFromAny(body["toolConfig"]); len(toolConfig) > 0 {
		if tools, ok := toolConfig["tools"].([]any); ok {
			parts = append(parts, fmt.Sprintf("request_toolConfig_tools=%d:%s", len(tools), anthropicRequestToolConfigShapes(tools)))
		} else {
			parts = append(parts, "request_toolConfig_keys="+strings.Join(sortedKeys(toolConfig), "|"))
		}
	}
	if container, ok := body["container"]; ok {
		switch typed := container.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				parts = append(parts, "request_container=existing_id")
			}
		case map[string]any:
			parts = append(parts, "request_container_keys="+strings.Join(sortedKeys(typed), "|"))
		default:
			parts = append(parts, fmt.Sprintf("request_container_type=%T", container))
		}
	}
	return strings.Join(parts, ", ")
}

func anthropicRequestURLDebugSummary(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if host := strings.TrimSpace(u.Host); host != "" {
		parts = append(parts, "request_host="+host)
	}
	if path := strings.TrimSpace(u.EscapedPath()); path != "" {
		parts = append(parts, "request_path="+path)
	}
	return strings.Join(parts, ", ")
}

func responseContentTypeDebugSummary(contentType string) string {
	contentType = strings.TrimSpace(contentType)
	if contentType == "" {
		return "response_content_type=none"
	}
	return "response_content_type=" + strings.ReplaceAll(contentType, ",", ";")
}

func anthropicProviderErrorMessage(err error, debugSummary ...string) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	debugParts := make([]string, 0, len(debugSummary))
	for _, summary := range debugSummary {
		if trimmed := strings.TrimSpace(summary); trimmed != "" {
			debugParts = append(debugParts, trimmed)
		}
	}
	if len(debugParts) == 0 {
		return msg
	}
	return msg + " (" + strings.Join(debugParts, ", ") + ")"
}

func anthropicRequestContentShape(raw any) string {
	switch typed := raw.(type) {
	case string:
		if strings.TrimSpace(typed) == "" {
			return "empty_string"
		}
		return "string"
	case []any:
		if len(typed) == 0 {
			return "empty_array"
		}
		shapes := make([]string, 0, len(typed))
		for _, item := range typed {
			block := mapFromAny(item)
			if len(block) == 0 {
				shapes = append(shapes, fmt.Sprintf("%T", item))
				continue
			}
			switch {
			case block["text"] != nil:
				shapes = append(shapes, "text")
			case block["document"] != nil:
				shapes = append(shapes, "document")
			case block["image"] != nil:
				shapes = append(shapes, "image")
			default:
				shapes = append(shapes, strings.Join(sortedKeys(block), "+"))
			}
		}
		return strings.Join(shapes, "|")
	default:
		if raw == nil {
			return "nil"
		}
		return fmt.Sprintf("%T", raw)
	}
}

func anthropicRequestToolConfigShapes(tools []any) string {
	if len(tools) == 0 {
		return ""
	}
	shapes := make([]string, 0, len(tools))
	for _, rawTool := range tools {
		tool := mapFromAny(rawTool)
		if len(tool) == 0 {
			shapes = append(shapes, fmt.Sprintf("%T", rawTool))
			continue
		}
		shapes = append(shapes, strings.Join(sortedKeys(tool), "+"))
	}
	return strings.Join(shapes, "|")
}

func sanitizeProviderToolResultContent(raw any) any {
	switch typed := raw.(type) {
	case []any:
		out := make([]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, sanitizeProviderToolResultContent(item))
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, value := range typed {
			if strings.EqualFold(strings.TrimSpace(key), "encrypted_content") {
				continue
			}
			out[key] = sanitizeProviderToolResultContent(value)
		}
		return out
	default:
		return raw
	}
}

func stringValue(raw any) string {
	if s, ok := raw.(string); ok {
		return s
	}
	return ""
}

func toolCallsFromOpenAIMessage(raw any, mapper *toolnamemapper.Mapper) []types.ToolCall {
	msg, ok := raw.(map[string]any)
	if !ok || msg == nil {
		return nil
	}
	items, ok := msg["tool_calls"].([]any)
	if !ok || len(items) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0, len(items))
	for _, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok || item == nil {
			continue
		}
		id, _ := item["id"].(string)
		if strings.TrimSpace(id) == "" {
			id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		}
		fn, _ := item["function"].(map[string]any)
		name, _ := fn["name"].(string)
		argsText, _ := fn["arguments"].(string)
		if strings.TrimSpace(argsText) == "" {
			argsText = "{}"
		}
		args := map[string]any{}
		var parsed any
		if err := json.Unmarshal([]byte(argsText), &parsed); err == nil {
			if obj, ok := parsed.(map[string]any); ok {
				args = obj
			}
		}
		out = append(out, types.ToolCall{
			ID:       id,
			Name:     mapper.CanonicalName(name),
			Args:     args,
			ArgsText: argsText,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type anthropicUsage struct {
	Input      *int
	Output     *int
	Total      *int
	Cached     *int
	CacheWrite *int
	Reasoning  *int
	Details    map[string]int
}

func (usage *anthropicUsage) merge(update anthropicUsage) {
	if update.Input != nil {
		usage.Input = update.Input
	}
	if update.Output != nil {
		usage.Output = update.Output
	}
	if update.Total != nil {
		usage.Total = update.Total
	}
	if update.Cached != nil {
		usage.Cached = update.Cached
	}
	if update.CacheWrite != nil {
		usage.CacheWrite = update.CacheWrite
	}
	if update.Reasoning != nil {
		usage.Reasoning = update.Reasoning
	}
	if len(update.Details) > 0 {
		if usage.Details == nil {
			usage.Details = map[string]int{}
		}
		for key, value := range update.Details {
			usage.Details[key] = value
		}
	}
}

func usageDetail(details map[string]int, key string, raw any) map[string]int {
	value := coerceInt(raw)
	if value == nil {
		return details
	}
	if details == nil {
		details = map[string]int{}
	}
	details[key] = *value
	return details
}

func anthropicUsageFromAny(raw any) anthropicUsage {
	usage, ok := raw.(map[string]any)
	if !ok || usage == nil {
		return anthropicUsage{}
	}
	result := anthropicUsage{}
	result.Input = coerceInt(valueByAnyKey(usage, "input_tokens", "inputTokens", "InputTokens"))
	if result.Input == nil {
		result.Input = coerceInt(valueByAnyKey(usage, "prompt_tokens", "PromptTokens"))
	}
	result.Output = coerceInt(valueByAnyKey(usage, "output_tokens", "outputTokens", "OutputTokens"))
	if result.Output == nil {
		result.Output = coerceInt(valueByAnyKey(usage, "completion_tokens", "CompletionTokens"))
	}
	result.Total = coerceInt(valueByAnyKey(usage, "total_tokens", "totalTokens", "TotalTokens", "TotalTokenCount"))
	result.Cached = coerceInt(valueByAnyKey(usage, "cache_read_input_tokens", "cacheReadInputTokens", "CacheReadInputTokens"))
	result.CacheWrite = coerceInt(valueByAnyKey(usage, "cache_creation_input_tokens", "cacheCreationInputTokens", "cache_write_input_tokens", "cacheWriteInputTokens", "CacheWriteInputTokens"))
	result.Reasoning = coerceInt(valueByAnyKey(usage, "reasoning_tokens", "reasoningTokens", "thinking_tokens", "thinkingTokens"))
	if cacheCreation := mapFromAny(valueByAnyKey(usage, "cache_creation", "cacheCreation")); cacheCreation != nil {
		result.Details = usageDetail(result.Details, "cache_creation_ephemeral_5m_input_tokens", valueByAnyKey(cacheCreation, "ephemeral_5m_input_tokens", "ephemeral5mInputTokens"))
		result.Details = usageDetail(result.Details, "cache_creation_ephemeral_1h_input_tokens", valueByAnyKey(cacheCreation, "ephemeral_1h_input_tokens", "ephemeral1hInputTokens"))
	}
	return result
}

func usageFromAny(raw any) (inputTokens, outputTokens, totalTokens *int) {
	usage := anthropicUsageFromAny(raw)
	return usage.Input, usage.Output, usage.Total
}

type optionalTokenSum struct {
	value   int
	present int
}

func (sum *optionalTokenSum) add(value *int) {
	if value == nil {
		return
	}
	sum.value += *value
	sum.present++
}

func (sum optionalTokenSum) complete(attempts int) *int {
	if attempts <= 0 || sum.present != attempts {
		return nil
	}
	value := sum.value
	return &value
}

type anthropicUsageAccumulator struct {
	attempts   int
	input      optionalTokenSum
	output     optionalTokenSum
	total      optionalTokenSum
	cached     optionalTokenSum
	cacheWrite optionalTokenSum
	reasoning  optionalTokenSum
	details    map[string]*optionalTokenSum
}

func (acc anthropicUsageAccumulator) withAttempt(usage anthropicUsage) anthropicUsageAccumulator {
	out := acc
	if acc.details != nil {
		out.details = make(map[string]*optionalTokenSum, len(acc.details))
		for key, sum := range acc.details {
			if sum == nil {
				continue
			}
			copy := *sum
			out.details[key] = &copy
		}
	}
	out.add(usage)
	return out
}

func (acc *anthropicUsageAccumulator) add(usage anthropicUsage) {
	acc.attempts++
	acc.input.add(usage.Input)
	acc.output.add(usage.Output)
	acc.total.add(usage.Total)
	acc.cached.add(usage.Cached)
	acc.cacheWrite.add(usage.CacheWrite)
	acc.reasoning.add(usage.Reasoning)
	for key, value := range usage.Details {
		entry := acc.details[key]
		if entry == nil {
			if acc.details == nil {
				acc.details = map[string]*optionalTokenSum{}
			}
			entry = &optionalTokenSum{}
			acc.details[key] = entry
		}
		valueCopy := value
		entry.add(&valueCopy)
	}
}

func (acc anthropicUsageAccumulator) apply(meta types.ResponseMeta) types.ResponseMeta {
	meta.InputTokens = acc.input.complete(acc.attempts)
	meta.OutputTokens = acc.output.complete(acc.attempts)
	meta.TotalTokens = acc.total.complete(acc.attempts)
	meta.CachedInputTokens = acc.cached.complete(acc.attempts)
	meta.CacheWriteTokens = acc.cacheWrite.complete(acc.attempts)
	meta.ReasoningTokens = acc.reasoning.complete(acc.attempts)
	for key, sum := range acc.details {
		if value := sum.complete(acc.attempts); value != nil {
			if meta.UsageDetails == nil {
				meta.UsageDetails = map[string]int{}
			}
			meta.UsageDetails[key] = *value
		}
	}
	return types.NormalizeResponseMetaUsageForSemantics(meta, types.UsageSemanticsAnthropic)
}

func choicesStyleStreamChunk(payload map[string]any) choicesStreamChunk {
	if len(payload) == 0 {
		return choicesStreamChunk{}
	}
	choices, ok := arrayByAnyKey(payload, "choices", "Choices")
	if !ok || len(choices) == 0 {
		return choicesStreamChunk{}
	}
	chunk := choicesStreamChunk{Handled: true}
	if rid := firstNonEmptyString(valueByAnyKey(payload, "id", "ID", "Id", "request_id", "requestId", "RequestId", "RequestID")); rid != "" {
		chunk.RequestID = &rid
	}
	if returnedModel := firstNonEmptyString(valueByAnyKey(payload, "model", "Model", "model_version", "modelVersion")); returnedModel != "" {
		chunk.ResolvedModel = &returnedModel
	}
	chunk.Usage = anthropicUsageFromAny(valueByAnyKey(payload, "usage", "Usage"))
	chunk.InputTokens, chunk.OutputTokens, chunk.TotalTokens = chunk.Usage.Input, chunk.Usage.Output, chunk.Usage.Total

	choice := mapFromAny(choices[0])
	if len(choice) == 0 {
		return chunk
	}
	chunk.StopReason = firstNonEmptyString(valueByAnyKey(choice, "finish_reason", "finishReason", "FinishReason", "stop_reason", "stopReason", "StopReason"))
	if chunk.InputTokens == nil && chunk.OutputTokens == nil && chunk.TotalTokens == nil {
		chunk.Usage = anthropicUsageFromAny(valueByAnyKey(choice, "usage", "Usage"))
		chunk.InputTokens, chunk.OutputTokens, chunk.TotalTokens = chunk.Usage.Input, chunk.Usage.Output, chunk.Usage.Total
	}
	if delta := mapByAnyKey(choice, "delta", "Delta"); len(delta) > 0 {
		chunk.Text = choicesStyleContentText(valueByAnyKey(delta, "content", "Content"))
		if chunk.Text == "" {
			chunk.Text = firstStringPreserveWhitespace(valueByAnyKey(delta, "text", "Text", "output_text", "outputText", "OutputText"))
		}
		if chunk.StopReason == "" {
			chunk.StopReason = firstNonEmptyString(valueByAnyKey(delta, "finish_reason", "finishReason", "FinishReason", "stop_reason", "stopReason", "StopReason"))
		}
		return chunk
	}
	if message := mapByAnyKey(choice, "message", "Message"); len(message) > 0 {
		chunk.Text = choicesStyleContentText(valueByAnyKey(message, "content", "Content"))
		if chunk.Text == "" {
			chunk.Text = firstStringPreserveWhitespace(valueByAnyKey(message, "text", "Text", "output_text", "outputText", "OutputText"))
		}
		return chunk
	}
	chunk.Text = choicesStyleContentText(valueByAnyKey(choice, "content", "Content"))
	if chunk.Text == "" {
		chunk.Text = firstStringPreserveWhitespace(valueByAnyKey(choice, "text", "Text", "output_text", "outputText", "OutputText"))
	}
	return chunk
}

func choicesStyleContentText(raw any) string {
	if text, ok := raw.(string); ok {
		return text
	}
	if text := firstNonEmptyString(raw); text != "" {
		return text
	}
	if blocks := contentBlocksFromAny(raw); len(blocks) > 0 {
		return textFromContentBlocks(blocks)
	}
	return ""
}

func mapFromAny(raw any) map[string]any {
	obj, ok := raw.(map[string]any)
	if !ok || obj == nil {
		return nil
	}
	return obj
}

func valueByAnyKey(obj map[string]any, keys ...string) any {
	if len(obj) == 0 {
		return nil
	}
	for _, key := range keys {
		if value, ok := obj[key]; ok {
			return value
		}
	}
	for _, want := range keys {
		for key, value := range obj {
			if strings.EqualFold(strings.TrimSpace(key), strings.TrimSpace(want)) {
				return value
			}
		}
	}
	return nil
}

func mapByAnyKey(obj map[string]any, keys ...string) map[string]any {
	return mapFromAny(valueByAnyKey(obj, keys...))
}

func arrayByAnyKey(obj map[string]any, keys ...string) ([]any, bool) {
	value, ok := valueByAnyKey(obj, keys...).([]any)
	return value, ok
}

func firstNonEmptyString(values ...any) string {
	for _, raw := range values {
		if s, ok := raw.(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstStringPreserveWhitespace(values ...any) string {
	for _, raw := range values {
		if s, ok := raw.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func decodedAnthropicJSONPayload(raw []byte) map[string]any {
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil || payload == nil {
		return nil
	}
	if bodyText, ok := payload["body"].(string); ok && strings.TrimSpace(bodyText) != "" {
		var wrapped map[string]any
		wrappedDec := json.NewDecoder(strings.NewReader(bodyText))
		wrappedDec.UseNumber()
		if err := wrappedDec.Decode(&wrapped); err == nil && wrapped != nil {
			payload = wrapped
		}
	}
	return payload
}

func anthropicJSONResponseMetadata(raw []byte) (anthropicUsage, *string, *string) {
	payload := decodedAnthropicJSONPayload(raw)
	if payload == nil {
		return anthropicUsage{}, nil, nil
	}
	candidates := []map[string]any{payload}
	for _, key := range []string{"output", "result", "message"} {
		if nested := mapByAnyKey(payload, key); nested != nil {
			candidates = append(candidates, nested)
		}
	}
	if choices, ok := arrayByAnyKey(payload, "choices", "Choices"); ok && len(choices) > 0 {
		if choice := mapFromAny(choices[0]); choice != nil {
			candidates = append(candidates, choice)
		}
	}
	var usage anthropicUsage
	var resolvedModel, providerVersion *string
	for _, candidate := range candidates {
		if usage.Input == nil && usage.Output == nil && usage.Total == nil && usage.Cached == nil && usage.CacheWrite == nil && usage.Reasoning == nil {
			usage = anthropicUsageFromAny(valueByAnyKey(candidate, "usage", "Usage"))
		}
		if resolvedModel == nil {
			if model := firstNonEmptyString(valueByAnyKey(candidate, "model", "Model", "model_id", "modelId")); model != "" {
				resolvedModel = &model
			}
		}
		if providerVersion == nil {
			if version := firstNonEmptyString(valueByAnyKey(candidate, "version", "Version", "model_version", "modelVersion")); version != "" {
				providerVersion = &version
			}
		}
	}
	return usage, resolvedModel, providerVersion
}

func sortedKeys(obj map[string]any) []string {
	if len(obj) == 0 {
		return nil
	}
	keys := make([]string, 0, len(obj))
	for key := range obj {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func jsonResponseDebugSummary(payload map[string]any) string {
	if len(payload) == 0 {
		return "response_keys=none"
	}
	parts := []string{"response_keys=" + strings.Join(sortedKeys(payload), "|")}
	if kind := bedrockOutputUnionType(payload); kind != "" {
		parts = append(parts, "response_type="+debugSummaryValue(kind, 160))
	}
	if choices, ok := arrayByAnyKey(payload, "choices", "Choices"); ok {
		parts = append(parts, fmt.Sprintf("choices=%d", len(choices)))
		if len(choices) > 0 {
			if choice := mapFromAny(choices[0]); len(choice) > 0 {
				parts = append(parts, "choice_keys="+strings.Join(sortedKeys(choice), "|"))
				if message := mapByAnyKey(choice, "message", "Message"); len(message) > 0 {
					parts = append(parts, "choice_message_keys="+strings.Join(sortedKeys(message), "|"))
				}
			}
		}
	}
	if message := mapByAnyKey(payload, "message", "Message"); len(message) > 0 {
		parts = append(parts, "message_keys="+strings.Join(sortedKeys(message), "|"))
	}
	if output := mapByAnyKey(payload, "output", "Output"); len(output) > 0 {
		parts = append(parts, "output_keys="+strings.Join(sortedKeys(output), "|"))
		if kind := bedrockOutputUnionType(output); kind != "" {
			parts = append(parts, "output_type="+debugSummaryValue(kind, 160))
		}
		if msg := bedrockOutputUnionMessage(output); msg != "" {
			parts = append(parts, "output_message="+debugSummaryValue(msg, 240))
		}
		if message := mapByAnyKey(output, "message", "Message"); len(message) > 0 {
			parts = append(parts, "output_message_keys="+strings.Join(sortedKeys(message), "|"))
		}
	}
	if result := mapByAnyKey(payload, "result", "Result"); len(result) > 0 {
		parts = append(parts, "result_keys="+strings.Join(sortedKeys(result), "|"))
	}
	return strings.Join(parts, ", ")
}

func debugSummaryValue(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if value == "" {
		return ""
	}
	if limit > 0 && len(value) > limit {
		value = value[:limit] + "..."
	}
	return strings.NewReplacer(",", ";", "\n", " ", "\r", " ").Replace(value)
}

func bedrockOutputUnionType(obj map[string]any) string {
	return firstNonEmptyString(valueByAnyKey(obj, "__type", "type", "Type"))
}

func bedrockOutputUnionMessage(obj map[string]any) string {
	return firstNonEmptyString(
		valueByAnyKey(obj, "message", "Message"),
		valueByAnyKey(obj, "originalMessage", "OriginalMessage"),
		valueByAnyKey(obj, "error", "Error"),
	)
}

func bedrockOutputUnionError(obj map[string]any) error {
	kind := bedrockOutputUnionType(obj)
	if kind == "" {
		return nil
	}
	lower := strings.ToLower(kind)
	if !strings.Contains(lower, "exception") && !strings.Contains(lower, "error") && !strings.Contains(lower, "fault") {
		return nil
	}
	msg := bedrockOutputUnionMessage(obj)
	if msg == "" {
		msg = "Bedrock returned an error union without a message"
	}
	return fmt.Errorf("Bedrock Messages output error %s: %s", kind, msg)
}

func parseMessagePayload(message map[string]any, mapper *toolnamemapper.Mapper) (blocks []any, toolCalls []types.ToolCall, nextContainerID string) {
	if len(message) == 0 {
		return nil, nil, ""
	}
	if contentBlocks := contentBlocksFromAny(valueByAnyKey(message, "content", "Content")); len(contentBlocks) > 0 {
		blocks = contentBlocks
	}
	if len(blocks) == 0 {
		if text := firstStringPreserveWhitespace(valueByAnyKey(message, "content", "Content", "text", "Text", "output_text", "outputText", "OutputText")); strings.TrimSpace(text) != "" {
			blocks = []any{map[string]any{"type": "text", "text": text}}
		}
	}
	toolCalls = toolCallsFromOpenAIMessage(message, mapper)
	nextContainerID = extractContainerID(message)
	return blocks, toolCalls, nextContainerID
}

func readJSONResponse(
	body io.Reader,
	mapper *toolnamemapper.Mapper,
	currentContainerID string,
) (blocks []any, toolCalls []types.ToolCall, answer string, stopReason string, requestID *string, inputTokens, outputTokens, totalTokens *int, nextContainerID string, debugSummary string, err error) {
	var payload map[string]any
	dec := json.NewDecoder(body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, nil, "", "", nil, nil, nil, nil, currentContainerID, "", err
	}
	if bodyText, ok := payload["body"].(string); ok && strings.TrimSpace(bodyText) != "" {
		var wrapped map[string]any
		wrappedDec := json.NewDecoder(strings.NewReader(bodyText))
		wrappedDec.UseNumber()
		if err := wrappedDec.Decode(&wrapped); err == nil && len(wrapped) > 0 {
			payload = wrapped
		}
	}
	debugSummary = jsonResponseDebugSummary(payload)
	if currentContainerID != "" {
		nextContainerID = currentContainerID
	}
	if rid := firstNonEmptyString(valueByAnyKey(payload, "id", "ID", "Id", "request_id", "requestId", "RequestId", "RequestID")); rid != "" {
		requestID = &rid
	}
	if sr := firstNonEmptyString(valueByAnyKey(payload, "stop_reason", "stopReason", "StopReason")); sr != "" {
		stopReason = sr
	}
	inputTokens, outputTokens, totalTokens = usageFromAny(valueByAnyKey(payload, "usage", "Usage"))
	if containerID := extractContainerID(payload); containerID != "" {
		nextContainerID = containerID
	}

	if contentBlocks := contentBlocksFromAny(valueByAnyKey(payload, "content", "Content")); len(contentBlocks) > 0 {
		blocks = contentBlocks
		toolCalls = toolCallsFromAnthropicBlocks(blocks, mapper)
		answer = textFromContentBlocks(blocks)
		return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
	}

	if text := firstStringPreserveWhitespace(valueByAnyKey(payload, "output_text", "outputText", "OutputText", "text", "Text", "completion", "Completion")); strings.TrimSpace(text) != "" {
		blocks = []any{map[string]any{"type": "text", "text": text}}
		answer = text
		return blocks, nil, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
	}

	if message := mapByAnyKey(payload, "message", "Message"); len(message) > 0 {
		blocks, toolCalls, containerID := parseMessagePayload(message, mapper)
		if containerID != "" {
			nextContainerID = containerID
		}
		answer = textFromContentBlocks(blocks)
		return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
	}

	if output := mapByAnyKey(payload, "output", "Output"); len(output) > 0 {
		if err := bedrockOutputUnionError(output); err != nil {
			return nil, nil, "", stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, err
		}
		if stopReason == "" {
			stopReason = firstNonEmptyString(valueByAnyKey(output, "stop_reason", "stopReason", "StopReason"))
		}
		if inputTokens == nil && outputTokens == nil && totalTokens == nil {
			inputTokens, outputTokens, totalTokens = usageFromAny(valueByAnyKey(output, "usage", "Usage"))
		}
		if message := mapByAnyKey(output, "message", "Message"); len(message) > 0 {
			blocks, toolCalls, containerID := parseMessagePayload(message, mapper)
			if containerID != "" {
				nextContainerID = containerID
			}
			answer = textFromContentBlocks(blocks)
			return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
		}
		if contentBlocks := contentBlocksFromAny(valueByAnyKey(output, "content", "Content")); len(contentBlocks) > 0 {
			blocks = contentBlocks
			toolCalls = toolCallsFromAnthropicBlocks(blocks, mapper)
			answer = textFromContentBlocks(blocks)
			return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
		}
		if text := firstStringPreserveWhitespace(valueByAnyKey(output, "text", "Text", "output_text", "outputText", "OutputText")); strings.TrimSpace(text) != "" {
			blocks = []any{map[string]any{"type": "text", "text": text}}
			answer = text
			return blocks, nil, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
		}
	}

	if result := mapByAnyKey(payload, "result", "Result"); len(result) > 0 {
		if stopReason == "" {
			stopReason = firstNonEmptyString(valueByAnyKey(result, "stop_reason", "stopReason", "StopReason"))
		}
		if requestID == nil {
			if rid := firstNonEmptyString(valueByAnyKey(result, "id", "ID", "Id", "request_id", "requestId", "RequestId", "RequestID")); rid != "" {
				requestID = &rid
			}
		}
		if inputTokens == nil && outputTokens == nil && totalTokens == nil {
			inputTokens, outputTokens, totalTokens = usageFromAny(valueByAnyKey(result, "usage", "Usage"))
		}
		if contentBlocks := contentBlocksFromAny(valueByAnyKey(result, "content", "Content")); len(contentBlocks) > 0 {
			blocks = contentBlocks
			toolCalls = toolCallsFromAnthropicBlocks(blocks, mapper)
			answer = textFromContentBlocks(blocks)
			return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
		}
		if message := mapByAnyKey(result, "message", "Message"); len(message) > 0 {
			blocks, toolCalls, containerID := parseMessagePayload(message, mapper)
			if containerID != "" {
				nextContainerID = containerID
			}
			answer = textFromContentBlocks(blocks)
			return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
		}
		if resultChoices, ok := arrayByAnyKey(result, "choices", "Choices"); ok {
			payload["choices"] = resultChoices
		}
	}

	choices, ok := arrayByAnyKey(payload, "choices", "Choices")
	if !ok || len(choices) == 0 {
		return nil, nil, "", stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
	}
	choice, _ := choices[0].(map[string]any)
	if choice == nil {
		return nil, nil, "", stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
	}
	if stopReason == "" {
		stopReason = firstNonEmptyString(valueByAnyKey(choice, "finish_reason", "finishReason", "FinishReason", "stop_reason", "stopReason", "StopReason"))
	}
	message := mapByAnyKey(choice, "message", "Message")
	if message != nil {
		var containerID string
		blocks, toolCalls, containerID = parseMessagePayload(message, mapper)
		if containerID != "" {
			nextContainerID = containerID
		}
	}
	answer = textFromContentBlocks(blocks)
	return blocks, toolCalls, answer, stopReason, requestID, inputTokens, outputTokens, totalTokens, nextContainerID, debugSummary, nil
}

func extractContainerID(raw any) string {
	obj, ok := raw.(map[string]any)
	if !ok || obj == nil {
		return ""
	}
	if id, ok := obj["id"].(string); ok && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	if container, ok := obj["container"].(map[string]any); ok {
		if id, ok := container["id"].(string); ok && strings.TrimSpace(id) != "" {
			return strings.TrimSpace(id)
		}
	}
	return ""
}

func encodeAnthropicProviderPayload(providerID string, segments []map[string]any, state map[string]any) json.RawMessage {
	if len(segments) == 0 && len(state) == 0 {
		return nil
	}
	payload := map[string]any{
		"format":      "anthropic_messages_v1",
		"provider_id": strings.TrimSpace(providerID),
		"api_type":    "messages",
	}
	if len(segments) > 0 {
		payload["segments"] = segments
	}
	if len(state) > 0 {
		payload["state"] = state
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return b
}

func extractAnthropicProviderState(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Format string         `json:"format"`
		State  map[string]any `json:"state"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.Format) != "anthropic_messages_v1" || len(payload.State) == 0 {
		return nil
	}
	return payload.State
}

func decodeAnthropicProviderSegments(raw json.RawMessage, providerID string) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Format     string           `json:"format"`
		ProviderID string           `json:"provider_id"`
		APIType    string           `json:"api_type"`
		Segments   []map[string]any `json:"segments"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.Format) != "anthropic_messages_v1" {
		return nil
	}
	if strings.TrimSpace(payload.APIType) != "" && strings.TrimSpace(payload.APIType) != "messages" {
		return nil
	}
	if providerID != "" && strings.TrimSpace(payload.ProviderID) != "" && strings.TrimSpace(payload.ProviderID) != strings.TrimSpace(providerID) {
		return nil
	}
	if len(payload.Segments) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(payload.Segments))
	for _, segment := range payload.Segments {
		if copied := deepCopyMap(segment); len(copied) > 0 {
			out = append(out, copied)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func codeExecutionBeta(toolType string) string {
	raw := strings.TrimSpace(toolType)
	if !strings.HasPrefix(raw, "code_execution_") {
		return ""
	}
	suffix := strings.TrimPrefix(raw, "code_execution_")
	if len(suffix) == 8 {
		return "code-execution-" + suffix[:4] + "-" + suffix[4:6] + "-" + suffix[6:]
	}
	return strings.ReplaceAll(raw, "_", "-")
}

func codeExecutionToolType(codeExecution *CodeExecutionConfig) string {
	if codeExecution != nil && strings.TrimSpace(codeExecution.ToolType) != "" {
		return strings.TrimSpace(codeExecution.ToolType)
	}
	return "code_execution_20260120"
}

type anthropicNativeFileInputPreparation struct {
	NeedsFilesAPI      bool
	NeedsCodeExecution bool
}

func (p *Provider) prepareNativeFileInputs(ctx context.Context, req *types.ChatRequest) (anthropicNativeFileInputPreparation, error) {
	if req == nil || p == nil || p.bedrockMessagesShape {
		return anthropicNativeFileInputPreparation{}, nil
	}
	cache := map[string]string{}
	var prep anthropicNativeFileInputPreparation
	for msgIdx := range req.Messages {
		files := types.CloneFileAttachments(req.Messages[msgIdx].FileAttachments)
		if len(files) == 0 {
			continue
		}
		changed := false
		for fileIdx := range files {
			attachment := files[fileIdx]
			if !anthropicFileRequiresFilesAPI(attachment) {
				continue
			}
			if strings.TrimSpace(attachment.ProviderFileID) != "" {
				files[fileIdx].ProviderFileType = "container_upload"
				prep.NeedsFilesAPI = true
				prep.NeedsCodeExecution = true
				changed = true
				continue
			}
			cacheKey := anthropicFileUploadCacheKey(attachment)
			fileID := strings.TrimSpace(cache[cacheKey])
			if fileID == "" {
				uploaded, err := p.uploadFileForAnthropicMessages(ctx, attachment)
				if err != nil {
					return prep, err
				}
				fileID = strings.TrimSpace(uploaded)
				if fileID == "" {
					return prep, fmt.Errorf("anthropic file upload returned an empty file id for %s", anthropicAttachmentDisplayName(attachment))
				}
				cache[cacheKey] = fileID
			}
			files[fileIdx].ProviderFileID = fileID
			files[fileIdx].ProviderFileType = "container_upload"
			prep.NeedsFilesAPI = true
			prep.NeedsCodeExecution = true
			changed = true
		}
		if changed {
			req.Messages[msgIdx].FileAttachments = files
		}
	}
	return prep, nil
}

func requestNeedsAnthropicCodeExecution(req types.ChatRequest) bool {
	for _, message := range req.Messages {
		for _, attachment := range message.FileAttachments {
			if anthropicFileRequiresFilesAPI(attachment) {
				return true
			}
		}
	}
	return false
}

func anthropicFileRequiresFilesAPI(attachment types.FileAttachment) bool {
	if strings.TrimSpace(attachment.InlineDataBase64) == "" {
		return false
	}
	if strings.TrimSpace(attachment.ProviderFileID) != "" {
		return strings.TrimSpace(strings.ToLower(attachment.ProviderFileType)) == "container_upload"
	}
	if fileAttachmentIsPDFForAnthropic(attachment) || anthropicDirectImageAttachment(attachment) || anthropicDirectTextDocumentAttachment(attachment) {
		return false
	}
	return true
}

func anthropicFileUploadCacheKey(attachment types.FileAttachment) string {
	if sha := strings.TrimSpace(strings.ToLower(attachment.SHA256)); sha != "" {
		return "sha256:" + sha
	}
	return strings.Join([]string{
		strings.TrimSpace(attachment.ArtifactID),
		strings.TrimSpace(attachment.FileName),
		anthropicAttachmentMIME(attachment),
		strings.TrimSpace(attachment.InlineDataBase64),
	}, "\x00")
}

func (p *Provider) uploadFileForAnthropicMessages(ctx context.Context, attachment types.FileAttachment) (string, error) {
	raw, err := decodeAnthropicAttachmentBase64(attachment.InlineDataBase64)
	if err != nil {
		return "", fmt.Errorf("failed to decode %s for Anthropic file upload: %w", anthropicAttachmentDisplayName(attachment), err)
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("cannot upload empty file %s to Anthropic", anthropicAttachmentDisplayName(attachment))
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fileName := strings.TrimSpace(attachment.FileName)
	if fileName == "" {
		fileName = "attachment"
	}
	mimeType := anthropicAttachmentMIME(attachment)
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	partHeader := textproto.MIMEHeader{}
	partHeader.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeMultipartFilename(fileName)))
	partHeader.Set("Content-Type", mimeType)
	part, err := writer.CreatePart(partHeader)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(raw); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	urlStr := strings.TrimRight(p.baseURL, "/") + "/v1/files"
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	for k, v := range p.headers {
		if strings.EqualFold(strings.TrimSpace(k), "Content-Type") {
			continue
		}
		hreq.Header.Set(k, v)
	}
	hreq.Header.Set("Content-Type", writer.FormDataContentType())
	hreq.Header.Set("anthropic-beta", defaultFilesAPIBeta)
	if p.prepareRequest != nil {
		if err := p.prepareRequest(hreq, body.Bytes()); err != nil {
			return "", err
		}
	}
	resp, err := p.client.Do(hreq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		msg := strings.TrimSpace(string(text))
		if msg == "" {
			msg = resp.Status
		}
		return "", fmt.Errorf("Anthropic file upload failed for %s: %s", anthropicAttachmentDisplayName(attachment), msg)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256*1024)).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode Anthropic file upload response for %s: %w", anthropicAttachmentDisplayName(attachment), err)
	}
	return strings.TrimSpace(payload.ID), nil
}

func decodeAnthropicAttachmentBase64(raw string) ([]byte, error) {
	data := anthropicAttachmentBase64Payload(raw)
	if data == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(data)
}

func anthropicAttachmentBase64Payload(raw string) string {
	data := strings.TrimSpace(raw)
	if idx := strings.Index(data, ","); strings.HasPrefix(strings.ToLower(data), "data:") && idx >= 0 {
		data = strings.TrimSpace(data[idx+1:])
	}
	return data
}

func escapeMultipartFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "\\\\")
	name = strings.ReplaceAll(name, `"`, `\"`)
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	return name
}

func anthropicAttachmentDisplayName(attachment types.FileAttachment) string {
	if name := strings.TrimSpace(attachment.FileName); name != "" {
		return name
	}
	if id := strings.TrimSpace(attachment.ArtifactID); id != "" {
		return id
	}
	return "uploaded file"
}

func bedrockToolConfig(codeExecution *CodeExecutionConfig) map[string]any {
	if codeExecution == nil || !codeExecution.Enabled {
		return nil
	}
	return map[string]any{
		"tools": []any{
			map[string]any{
				"codeInterpreter": map[string]any{},
			},
		},
	}
}

func (p *Provider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	out := make(chan types.StreamEvent, 64)
	model := req.Model
	if strings.TrimSpace(model) == "" {
		model = p.model
	}
	metaBase := types.ResponseMeta{ProviderID: p.providerID, Model: model}
	apiSurface := "messages"
	if p.bedrockMessagesShape {
		apiSurface = "invoke_stream"
	}
	metaBase.APIType = &apiSurface

	go func() {
		defer close(out)

		nameMapper := toolnamemapper.New(0)
		for _, td := range req.Tools {
			_ = nameMapper.ProviderName(td.Function.Name)
		}

		urlStr := p.baseURL + "/v1/messages"
		if !p.bedrockMessagesShape && requestNeedsAnthropicCodeExecution(req) && (p.codeExecution == nil || !p.codeExecution.Enabled) {
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
				Code:      "HOSTED_CODE_EXECUTION_REQUIRED",
				Message:   "This file requires Anthropic hosted code execution, but that capability is not both enabled and permitted for this connection.",
				Retryable: false,
				Meta:      &metaBase,
			}})
			return
		}
		nativeFileInput, err := p.prepareNativeFileInputs(ctx, &req)
		if err != nil {
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_FILE_UPLOAD_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase}})
			return
		}
		requestMessages := messagesToAnthropicWithMode(req, nameMapper, p.bedrockMessagesShape)
		webSearchTools := p.webSearch
		codeExecutionTools := p.codeExecution
		if p.bedrockMessagesShape {
			webSearchTools = nil
			codeExecutionTools = nil
		}
		tools := toolsToAnthropic(req.Tools, nameMapper, webSearchTools, codeExecutionTools)
		currentContainerID := ""
		if rawID, ok := req.ProviderState["container_id"].(string); ok {
			currentContainerID = strings.TrimSpace(rawID)
		}
		accumulatedText := ""
		allSegments := make([]map[string]any, 0, 4)
		traceID := "trace_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		thinkingActive := false
		usageTotals := anthropicUsageAccumulator{}
		upstreamRequestCount := 0
		var latestRequestID, resolvedModel, providerVersion *string
		terminalMeta := func() types.ResponseMeta {
			meta := metaBase
			meta.RequestID = latestRequestID
			meta.ResolvedModel = resolvedModel
			meta.ProviderVersion = providerVersion
			if upstreamRequestCount > 0 {
				count := upstreamRequestCount
				meta.UpstreamRequestCount = &count
			}
			if currentContainerID != "" {
				meta.ProviderState = map[string]any{"container_id": currentContainerID}
			}
			return usageTotals.apply(meta)
		}
		notifyUsageSnapshot := func(current *anthropicUsage) {
			meta := terminalMeta()
			if current != nil {
				meta = usageTotals.withAttempt(*current).apply(meta)
			}
			_ = types.NotifyUsageSnapshot(req, meta)
		}

		for attempt := 0; attempt < 8; attempt++ {
			if ctx.Err() != nil {
				return
			}

			maxTokens := 8192
			body := map[string]any{
				"model":      model,
				"messages":   requestMessages,
				"max_tokens": maxTokens,
			}
			if !p.disableStreaming {
				body["stream"] = true
			}
			if strings.TrimSpace(req.SystemPrompt) != "" {
				body["system"] = strings.TrimSpace(req.SystemPrompt)
			}
			if p.compactionEnabled {
				body["context_management"] = map[string]any{
					"edits": []any{
						map[string]any{
							"type":         defaultCompactionEditType,
							"instructions": p.compactionInstruction,
							"trigger": map[string]any{
								"type":  "input_tokens",
								"value": p.compactionTrigger,
							},
						},
					},
				}
			}
			if len(tools) > 0 {
				body["tools"] = tools
			}
			if currentContainerID != "" {
				body["container"] = currentContainerID
			} else if !p.bedrockMessagesShape && codeExecutionTools != nil && codeExecutionTools.Enabled && len(codeExecutionTools.Skills) > 0 {
				skills := make([]any, 0, len(codeExecutionTools.Skills))
				for _, skill := range codeExecutionTools.Skills {
					entry := map[string]any{
						"type":     strings.TrimSpace(skill.Type),
						"skill_id": strings.TrimSpace(skill.SkillID),
					}
					if skill.Version != nil && strings.TrimSpace(*skill.Version) != "" {
						entry["version"] = strings.TrimSpace(*skill.Version)
					}
					skills = append(skills, entry)
				}
				if len(skills) > 0 {
					body["container"] = map[string]any{"skills": skills}
				}
			}

			betas := make([]string, 0, 4)
			if p.compactionEnabled {
				betas = append(betas, defaultCompactionBeta)
			}
			if nativeFileInput.NeedsFilesAPI {
				betas = append(betas, defaultFilesAPIBeta)
			}
			if !p.bedrockMessagesShape && codeExecutionTools != nil && codeExecutionTools.Enabled {
				if beta := codeExecutionBeta(codeExecutionToolType(codeExecutionTools)); beta != "" {
					betas = append(betas, beta)
				}
				if !p.bedrockMessagesShape && len(codeExecutionTools.Skills) > 0 {
					betas = append(betas, defaultSkillsBeta)
				}
			}
			requestDebugSummary := anthropicRequestDebugSummary(body, betas, p.bedrockMessagesShape)
			requestURLDebugSummary := anthropicRequestURLDebugSummary(urlStr)

			requestKind := "anthropic_messages"
			if p.bedrockMessagesShape {
				requestKind = "bedrock_messages"
			}
			b, err := requestguard.MarshalJSON(body, requestguard.Context{
				ProviderID:  p.providerID,
				Model:       model,
				RequestKind: requestKind,
				Endpoint:    urlStr,
				MaxBytes:    p.maxRequestBytes,
			})
			if err != nil {
				code := requestguard.ErrorCodeFor(err)
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: err.Error(), Retryable: false, Meta: &meta}})
				return
			}

			hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(b))
			if err != nil {
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: false, Meta: &meta}})
				return
			}
			for k, v := range p.headers {
				hreq.Header.Set(k, v)
			}
			if len(betas) > 0 {
				hreq.Header.Set("anthropic-beta", strings.Join(betas, ","))
			}
			if p.prepareRequest != nil {
				if err := p.prepareRequest(hreq, b); err != nil {
					meta := terminalMeta()
					send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "AUTH_ERROR", Message: err.Error(), Retryable: false, Meta: &meta}})
					return
				}
			}

			effectiveSurface := "messages"
			if p.bedrockMessagesShape {
				effectiveSurface = "invoke_stream"
			}
			if err := types.NotifyUsageDispatch(req, effectiveSurface); err != nil {
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "usage accounting is unavailable", Retryable: false, Meta: &meta}})
				return
			}
			upstreamRequestCount++
			resp, err := p.client.Do(hreq)
			if err != nil {
				code := "PROVIDER_ERROR"
				retryable := true
				if errorsIsContext(err) {
					code = "ABORTED"
					retryable = false
				}
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: err.Error(), Retryable: retryable, Meta: &meta}})
				return
			}

			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				text, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
				_ = resp.Body.Close()
				msg := strings.TrimSpace(string(text))
				if msg == "" {
					msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
				}
				code, retryable := requestguard.NormalizeProviderError(resp.StatusCode, msg)
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: msg, Retryable: retryable, Meta: &meta}})
				return
			}
			if resp.Body == nil {
				_ = resp.Body.Close()
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "Missing response body", Retryable: true, Meta: &meta}})
				return
			}
			for _, header := range []string{"request-id", "x-request-id", "anthropic-request-id", "x-amzn-requestid"} {
				if requestID := strings.TrimSpace(resp.Header.Get(header)); requestID != "" {
					latestRequestID = &requestID
					break
				}
			}
			for _, header := range []string{"anthropic-version", "x-anthropic-version"} {
				if version := strings.TrimSpace(resp.Header.Get(header)); version != "" {
					providerVersion = &version
					break
				}
			}

			contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
			responseDebugSummary := responseContentTypeDebugSummary(contentType)
			if !strings.Contains(contentType, "text/event-stream") {
				responseBody, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if readErr != nil {
					meta := terminalMeta()
					send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: anthropicProviderErrorMessage(readErr, responseDebugSummary, "", requestURLDebugSummary, requestDebugSummary), Retryable: true, Meta: &meta}})
					return
				}
				segmentBlocks, toolCalls, answer, jsonStopReason, jsonRequestID, jsonInputTokens, jsonOutputTokens, jsonTotalTokens, nextContainerID, jsonDebugSummary, err := readJSONResponse(bytes.NewReader(responseBody), nameMapper, currentContainerID)
				if err != nil {
					meta := terminalMeta()
					send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: anthropicProviderErrorMessage(err, responseDebugSummary, jsonDebugSummary, requestURLDebugSummary, requestDebugSummary), Retryable: true, Meta: &meta}})
					return
				}
				jsonUsage, jsonResolvedModel, jsonProviderVersion := anthropicJSONResponseMetadata(responseBody)
				jsonUsage.merge(anthropicUsage{Input: jsonInputTokens, Output: jsonOutputTokens, Total: jsonTotalTokens})
				usageTotals.add(jsonUsage)
				notifyUsageSnapshot(nil)
				if jsonRequestID != nil {
					latestRequestID = jsonRequestID
				}
				if jsonResolvedModel != nil {
					resolvedModel = jsonResolvedModel
				}
				if jsonProviderVersion != nil {
					providerVersion = jsonProviderVersion
				}
				if nextContainerID != "" {
					currentContainerID = nextContainerID
				}
				if len(segmentBlocks) > 0 {
					allSegments = append(allSegments, map[string]any{
						"role":    "assistant",
						"content": segmentBlocks,
					})
				}
				providerToolCalls := providerExecutedToolCallsFromBlocks(segmentBlocks)
				providerToolResults := providerExecutedToolResultsFromBlocks(segmentBlocks, providerToolCalls)

				meta := terminalMeta()
				providerPayload := encodeAnthropicProviderPayload(p.providerID, allSegments, meta.ProviderState)

				if jsonStopReason == "pause_turn" {
					if len(segmentBlocks) == 0 {
						send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "Anthropic pause_turn returned no assistant content", Retryable: true, Meta: &meta}})
						return
					}
					requestMessages = append(requestMessages, map[string]any{"role": "assistant", "content": segmentBlocks})
					accumulatedText += answer
					continue
				}
				if len(toolCalls) > 0 || jsonStopReason == "tool_use" {
					send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: toolCalls, Meta: meta, ProviderPayload: cloneRawJSON(providerPayload)}})
					return
				}

				if strings.TrimSpace(answer) == "" && len(segmentBlocks) > 0 {
					answer = textFromContentBlocks(segmentBlocks)
				}
				if strings.TrimSpace(answer) == "" && len(providerToolCalls) == 0 && len(providerToolResults) == 0 {
					send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_EMPTY_RESPONSE", Message: anthropicEmptyResponseMessage(jsonStopReason, jsonRequestID, segmentBlocks, responseDebugSummary, jsonDebugSummary, requestURLDebugSummary, requestDebugSummary), Retryable: true, Meta: &meta}})
					return
				}
				accumulatedText += answer
				send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: accumulatedText, Meta: meta, ProviderPayload: cloneRawJSON(providerPayload), ToolCalls: providerToolCalls, ToolResults: providerToolResults}})
				return
			}

			toolCalls := make([]types.ToolCall, 0)
			toolUseInputsByIndex := map[int]*struct{ id, name, inputJSON string }{}
			contentBlocksByIndex := map[int]map[string]any{}
			var compactionBlockIndex *int
			var compactionSummaryBuffer strings.Builder
			sawCompaction := false
			var stopReason *string
			requestID := latestRequestID
			var inputTokens, outputTokens, totalTokens *int
			attemptUsage := anthropicUsage{}
			messageStopSeen := false

			for msg := range stream.Parse(ctx, resp.Body) {
				if msg.Err != nil {
					attemptUsage.merge(anthropicUsage{Input: inputTokens, Output: outputTokens, Total: totalTokens})
					usageTotals.add(attemptUsage)
					notifyUsageSnapshot(nil)
					meta := terminalMeta()
					send(ctx, out, types.StreamEvent{
						Event: "error",
						Data: types.ErrorData{
							Code:      "PROVIDER_ERROR",
							Message:   "SSE parse error: " + msg.Err.Error(),
							Retryable: true,
							Meta:      &meta,
						},
					})
					_ = resp.Body.Close()
					return
				}
				raw := strings.TrimSpace(msg.Data)
				if raw == "" {
					continue
				}
				if raw == "[DONE]" {
					messageStopSeen = true
					goto REQUEST_DONE
				}
				var env streamEnvelope
				dec := json.NewDecoder(strings.NewReader(raw))
				dec.UseNumber()
				if err := dec.Decode(&env); err != nil {
					continue
				}
				var rawPayload map[string]any
				rawDec := json.NewDecoder(strings.NewReader(raw))
				rawDec.UseNumber()
				_ = rawDec.Decode(&rawPayload)

				typ := ""
				if msg.Event != "" {
					typ = msg.Event
				} else if env.Type != nil {
					typ = *env.Type
				}
				if typ == "" {
					if chunk := choicesStyleStreamChunk(rawPayload); chunk.Handled {
						if requestID == nil && chunk.RequestID != nil {
							requestID = chunk.RequestID
						}
						if chunk.InputTokens != nil {
							inputTokens = chunk.InputTokens
						}
						if chunk.OutputTokens != nil {
							outputTokens = chunk.OutputTokens
						}
						if chunk.TotalTokens != nil {
							totalTokens = chunk.TotalTokens
						}
						attemptUsage.merge(chunk.Usage)
						notifyUsageSnapshot(&attemptUsage)
						if chunk.ResolvedModel != nil {
							resolvedModel = chunk.ResolvedModel
						}
						if chunk.StopReason != "" {
							stop := chunk.StopReason
							stopReason = &stop
							messageStopSeen = true
						}
						if chunk.Text != "" {
							block := contentBlocksByIndex[0]
							if block == nil {
								block = map[string]any{"type": "text"}
								contentBlocksByIndex[0] = block
							}
							existing, _ := block["text"].(string)
							block["text"] = existing + chunk.Text
							accumulatedText += chunk.Text
							send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: chunk.Text}})
						}
						continue
					}
				}

				switch typ {
				case "message_start":
					if m, ok := env.Message.(map[string]any); ok {
						if id, ok := m["id"].(string); ok {
							id2 := id
							requestID = &id2
						}
						if returnedModel := firstNonEmptyString(valueByAnyKey(m, "model", "Model", "model_id", "modelId")); returnedModel != "" {
							resolvedModel = &returnedModel
						}
						if usage, ok := m["usage"].(map[string]any); ok {
							attemptUsage.merge(anthropicUsageFromAny(usage))
							notifyUsageSnapshot(&attemptUsage)
							inputTokens = attemptUsage.Input
							outputTokens = attemptUsage.Output
							totalTokens = attemptUsage.Total
						}
						if containerID := extractContainerID(m); containerID != "" {
							currentContainerID = containerID
						}
					}
				case "content_block_start":
					if env.Index == nil {
						continue
					}
					idx := *env.Index
					if block, ok := env.ContentBlock.(map[string]any); ok {
						copied := deepCopyMap(block)
						bt, _ := copied["type"].(string)
						if bt == "compaction" {
							sawCompaction = true
							trackedIndex := idx
							compactionBlockIndex = &trackedIndex
							compactionSummaryBuffer.Reset()
							send(ctx, out, types.StreamEvent{Event: types.EventCompactionStart, Data: map[string]any{}})
							continue
						}
						contentBlocksByIndex[idx] = copied
						if bt == "tool_use" || bt == "server_tool_use" {
							entry := &struct{ id, name, inputJSON string }{}
							if id, ok := copied["id"].(string); ok {
								entry.id = id
							}
							if nm, ok := copied["name"].(string); ok {
								entry.name = nm
							}
							toolUseInputsByIndex[idx] = entry
						}
					}
				case "content_block_delta":
					if env.Delta == nil || env.Index == nil {
						continue
					}
					delta, _ := env.Delta.(map[string]any)
					if delta == nil {
						continue
					}
					idx := *env.Index
					if dt, _ := delta["type"].(string); dt == "compaction_delta" {
						if content, ok := delta["content"].(string); ok && content != "" {
							compactionSummaryBuffer.WriteString(content)
						}
						continue
					}
					block := contentBlocksByIndex[idx]
					if block == nil {
						block = map[string]any{}
						contentBlocksByIndex[idx] = block
					}
					switch dt, _ := delta["type"].(string); dt {
					case "text_delta":
						if text, ok := delta["text"].(string); ok && text != "" {
							existing, _ := block["text"].(string)
							block["type"] = "text"
							block["text"] = existing + text
							accumulatedText += text
							send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: text}})
						}
					case "thinking_delta":
						if thinking, ok := delta["thinking"].(string); ok && thinking != "" {
							if !thinkingActive {
								thinkingActive = true
								send(ctx, out, types.StreamEvent{Event: "thinking_start", Data: types.ThinkingStartData{TraceID: traceID}})
							}
							existing, _ := block["thinking"].(string)
							block["type"] = "thinking"
							block["thinking"] = existing + thinking
							send(ctx, out, types.StreamEvent{Event: "thinking_delta", Data: types.ThinkingDeltaData{Content: thinking}})
						}
					case "signature_delta":
						if sig, ok := delta["signature"].(string); ok && sig != "" {
							block["signature"] = sig
						}
					case "input_json_delta":
						entry := toolUseInputsByIndex[idx]
						if entry != nil {
							if partial, ok := delta["partial_json"].(string); ok && partial != "" {
								entry.inputJSON += partial
							}
						}
					}
				case "content_block_stop":
					if env.Index == nil {
						continue
					}
					idx := *env.Index
					if compactionBlockIndex != nil && idx == *compactionBlockIndex {
						if summary := sanitizeCompactionSummary(compactionSummaryBuffer.String()); summary != "" {
							send(ctx, out, types.StreamEvent{Event: types.EventCompactionDelta, Data: types.CompactionDeltaData{Content: summary}})
						}
						send(ctx, out, types.StreamEvent{Event: types.EventCompactionEnd, Data: map[string]any{}})
						compactionSummaryBuffer.Reset()
						compactionBlockIndex = nil
						continue
					}
					entry := toolUseInputsByIndex[idx]
					if entry == nil {
						continue
					}
					block := contentBlocksByIndex[idx]
					if block == nil {
						block = map[string]any{}
						contentBlocksByIndex[idx] = block
					}
					if entry.id != "" {
						block["id"] = entry.id
					}
					if entry.name != "" {
						block["name"] = entry.name
					}
					argsText := entry.inputJSON
					if strings.TrimSpace(argsText) == "" {
						argsText = "{}"
					}
					args := map[string]any{}
					var parsed any
					if err := json.Unmarshal([]byte(argsText), &parsed); err == nil {
						if obj, ok := parsed.(map[string]any); ok {
							args = obj
							block["input"] = obj
						}
					}
					if blockType, _ := block["type"].(string); blockType == "tool_use" {
						id := strings.TrimSpace(entry.id)
						if id == "" {
							id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
							block["id"] = id
						}
						toolCalls = append(toolCalls, types.ToolCall{
							ID:       id,
							Name:     nameMapper.CanonicalName(entry.name),
							Args:     args,
							ArgsText: argsText,
						})
					}
				case "message_delta":
					if env.Delta != nil {
						if d, ok := env.Delta.(map[string]any); ok {
							if sr, ok := d["stop_reason"].(string); ok {
								stopReason = &sr
							}
						}
					}
					if env.Usage != nil {
						attemptUsage.merge(anthropicUsageFromAny(env.Usage))
						notifyUsageSnapshot(&attemptUsage)
						inputTokens = attemptUsage.Input
						outputTokens = attemptUsage.Output
						totalTokens = attemptUsage.Total
					}
				case "message_stop":
					messageStopSeen = true
					if compactionBlockIndex != nil {
						if summary := sanitizeCompactionSummary(compactionSummaryBuffer.String()); summary != "" {
							send(ctx, out, types.StreamEvent{Event: types.EventCompactionDelta, Data: types.CompactionDeltaData{Content: summary}})
						}
						send(ctx, out, types.StreamEvent{Event: types.EventCompactionEnd, Data: map[string]any{}})
						compactionSummaryBuffer.Reset()
						compactionBlockIndex = nil
					}
					goto REQUEST_DONE
				case "error":
					msgText := "Anthropic stream error"
					if env.Error != nil {
						if eobj, ok := env.Error.(map[string]any); ok {
							if m, ok := eobj["message"].(string); ok && strings.TrimSpace(m) != "" {
								msgText = m
							}
						}
					}
					code, retryable := requestguard.NormalizeStreamError(msgText)
					attemptUsage.merge(anthropicUsage{Input: inputTokens, Output: outputTokens, Total: totalTokens})
					usageTotals.add(attemptUsage)
					notifyUsageSnapshot(nil)
					meta := terminalMeta()
					send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: msgText, Retryable: retryable, Meta: &meta}})
					_ = resp.Body.Close()
					return
				}
			}
		REQUEST_DONE:
			_ = resp.Body.Close()
			attemptUsage.merge(anthropicUsage{Input: inputTokens, Output: outputTokens, Total: totalTokens})
			usageTotals.add(attemptUsage)
			notifyUsageSnapshot(nil)
			if requestID != nil {
				latestRequestID = requestID
			}
			if !messageStopSeen {
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
					Code: "STREAM_ENDED", Message: "Anthropic stream ended before message_stop", Retryable: true, Meta: &meta,
				}})
				return
			}

			segmentBlocks := orderedContentBlocks(contentBlocksByIndex)
			if len(segmentBlocks) > 0 {
				allSegments = append(allSegments, map[string]any{
					"role":    "assistant",
					"content": segmentBlocks,
				})
			}
			providerToolCalls := providerExecutedToolCallsFromBlocks(segmentBlocks)
			providerToolResults := providerExecutedToolResultsFromBlocks(segmentBlocks, providerToolCalls)

			meta := terminalMeta()
			providerPayload := encodeAnthropicProviderPayload(p.providerID, allSegments, meta.ProviderState)

			if stopReason != nil && *stopReason == "pause_turn" {
				if len(segmentBlocks) == 0 {
					send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "Anthropic pause_turn returned no assistant content", Retryable: true, Meta: &meta}})
					return
				}
				requestMessages = append(requestMessages, map[string]any{"role": "assistant", "content": segmentBlocks})
				continue
			}

			if thinkingActive {
				send(ctx, out, types.StreamEvent{Event: "thinking_end", Data: types.ThinkingEndData{Summary: nil}})
				thinkingActive = false
			}

			if len(toolCalls) > 0 || (stopReason != nil && *stopReason == "tool_use") {
				send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: toolCalls, Meta: meta, ProviderPayload: cloneRawJSON(providerPayload)}})
				return
			}

			finalAnswer := accumulatedText
			if strings.TrimSpace(finalAnswer) == "" {
				finalAnswer = textFromContentBlocks(segmentBlocks)
			}
			if strings.TrimSpace(finalAnswer) == "" && len(providerToolCalls) == 0 && len(providerToolResults) == 0 && !sawCompaction {
				reason := ""
				if stopReason != nil {
					reason = *stopReason
				}
				send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_EMPTY_RESPONSE", Message: anthropicEmptyResponseMessage(reason, requestID, segmentBlocks, responseDebugSummary, requestURLDebugSummary, requestDebugSummary), Retryable: true, Meta: &meta}})
				return
			}
			send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: finalAnswer, Meta: meta, ProviderPayload: cloneRawJSON(providerPayload), ToolCalls: providerToolCalls, ToolResults: providerToolResults}})
			return
		}

		meta := terminalMeta()
		send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "Anthropic pause_turn exceeded continuation limit", Retryable: true, Meta: &meta}})
	}()

	return out, nil
}

func send(ctx context.Context, ch chan<- types.StreamEvent, ev types.StreamEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

func toolsToAnthropic(
	tools []types.ToolDefinition,
	mapper *toolnamemapper.Mapper,
	webSearch *WebSearchConfig,
	codeExecution *CodeExecutionConfig,
) []any {
	out := make([]any, 0, len(tools)+2)
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":         mapper.ProviderName(t.Function.Name),
			"description":  t.Function.Description,
			"input_schema": toolschema.NormalizeFunctionParameters(t.Function.Parameters),
		})
	}
	includeCodeExecution := false
	codeExecutionType := codeExecutionToolType(codeExecution)
	if codeExecution != nil {
		includeCodeExecution = codeExecution.Enabled
	}
	if webSearch != nil && webSearch.Enabled {
		toolType := strings.TrimSpace(webSearch.ToolType)
		if toolType == "" {
			toolType = "web_search_20250305"
		}
		tool := map[string]any{
			"type": toolType,
			"name": "web_search",
		}
		if webSearch.MaxUses != nil && *webSearch.MaxUses > 0 {
			tool["max_uses"] = *webSearch.MaxUses
		}
		if len(webSearch.AllowedDomains) > 0 {
			tool["allowed_domains"] = append([]string(nil), webSearch.AllowedDomains...)
		}
		if len(webSearch.BlockedDomains) > 0 {
			tool["blocked_domains"] = append([]string(nil), webSearch.BlockedDomains...)
		}
		if len(webSearch.AllowedCallers) > 0 {
			tool["allowed_callers"] = append([]string(nil), webSearch.AllowedCallers...)
		} else if toolType != "web_search_20250305" && !includeCodeExecution {
			// Anthropic's newer search versions can default to programmatic
			// callers. A standalone Web Search toggle must remain directly
			// callable by the selected model without also enabling code execution.
			tool["allowed_callers"] = []string{"direct"}
		}
		out = append(out, tool)
	}
	if includeCodeExecution {
		out = append(out, map[string]any{
			"type": codeExecutionType,
			"name": "code_execution",
		})
	}
	return out
}

func messagesToAnthropic(req types.ChatRequest, mapper *toolnamemapper.Mapper) []any {
	return messagesToAnthropicWithMode(req, mapper, false)
}

func messagesToAnthropicWithMode(req types.ChatRequest, mapper *toolnamemapper.Mapper, bedrockMessagesShape bool) []any {
	out := make([]any, 0, len(req.Messages))
	pendingToolResults := make([]any, 0)
	validToolUseIDs := collectValidToolUseIDs(req.Messages)

	flushToolResults := func() {
		if len(pendingToolResults) == 0 {
			return
		}
		out = append(out, map[string]any{"role": "user", "content": append([]any(nil), pendingToolResults...)})
		pendingToolResults = pendingToolResults[:0]
	}

	for _, msg := range req.Messages {
		if msg.Role == types.RoleSystem {
			continue
		}
		if msg.Role == types.RoleTool {
			toolCallID := strings.TrimSpace(msg.ToolCallID)
			if toolCallID == "" {
				continue
			}
			if _, ok := validToolUseIDs[toolCallID]; !ok {
				// Skip orphan tool_result blocks. Anthropic requires a matching preceding tool_use.
				continue
			}
			content := ""
			if msg.Content != nil {
				content = types.SafeTruncate(*msg.Content)
			}
			toolResultBlocks := []any{map[string]any{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     content,
			}}
			if attachments := toolAttachmentContentToAnthropic(msg, bedrockMessagesShape); len(attachments) > 0 {
				toolResultBlocks = append(toolResultBlocks, attachments...)
			}
			pendingToolResults = append(pendingToolResults, toolResultBlocks...)
			continue
		}

		flushToolResults()

		if msg.Role == types.RoleUser {
			content := userContentToAnthropicWithMode(msg, bedrockMessagesShape)
			switch typed := content.(type) {
			case nil:
				out = append(out, map[string]any{"role": "user", "content": ""})
			case string:
				out = append(out, map[string]any{"role": "user", "content": typed})
			case []any:
				out = append(out, map[string]any{"role": "user", "content": typed})
			default:
				out = append(out, map[string]any{"role": "user", "content": typed})
			}
			continue
		}
		if msg.Role == types.RoleAssistant {
			if segments := assistantSegmentsFromPayload(msg, validToolUseIDs); len(segments) > 0 {
				out = append(out, segments...)
				continue
			}
			blocks := make([]any, 0)
			if msg.Content != nil && strings.TrimSpace(*msg.Content) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": types.SafeTruncate(*msg.Content)})
			}
			for _, call := range msg.ToolCalls {
				if call.ProviderExecuted {
					continue
				}
				if _, ok := validToolUseIDs[strings.TrimSpace(call.ID)]; !ok {
					continue
				}
				blocks = append(blocks, map[string]any{
					"type":  "tool_use",
					"id":    call.ID,
					"name":  mapper.ProviderName(call.Name),
					"input": call.Args,
				})
			}
			// Skip assistant turns that became empty after filtering invalid tool_use IDs.
			// Emitting an empty text block is rejected by Anthropic.
			if len(blocks) == 0 {
				continue
			}
			out = append(out, map[string]any{"role": "assistant", "content": blocks})
			continue
		}
	}

	flushToolResults()
	return out
}

func userContentToAnthropic(msg types.ChatMessage) any {
	return userContentToAnthropicWithMode(msg, false)
}

func userContentToAnthropicWithMode(msg types.ChatMessage, bedrockMessagesShape bool) any {
	if bedrockMessagesShape {
		return userContentToBedrockMessages(msg)
	}

	text := ""
	if msg.Content != nil {
		text = types.SafeTruncate(*msg.Content)
	}

	images := vision.NormalizeAttachments(msg.ImageAttachments)
	files := types.CloneFileAttachments(msg.FileAttachments)
	blocks := make([]any, 0, len(images)+len(files)+1)
	for _, attachment := range files {
		if block := anthropicDocumentBlockForAttachment(attachment); block != nil {
			blocks = append(blocks, block)
		}
	}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MIMEType,
				"data":       img.Base64Data,
			},
		})
	}
	switch {
	case len(blocks) == 0:
		return text
	case len(blocks) == 1:
		if onlyText, ok := blocks[0].(map[string]any); ok && onlyText != nil {
			if kind, _ := onlyText["type"].(string); kind == "text" {
				if value, _ := onlyText["text"].(string); value != "" {
					return value
				}
			}
		}
	}
	return blocks
}

func toolAttachmentContentToAnthropic(msg types.ChatMessage, bedrockMessagesShape bool) []any {
	if bedrockMessagesShape {
		return toolAttachmentContentToBedrockMessages(msg)
	}

	images := vision.NormalizeAttachments(msg.ImageAttachments)
	files := types.CloneFileAttachments(msg.FileAttachments)
	blocks := make([]any, 0, len(images)+len(files)+1)
	for _, attachment := range files {
		if block := anthropicDocumentBlockForAttachment(attachment); block != nil {
			blocks = append(blocks, block)
		}
	}
	for _, img := range images {
		blocks = append(blocks, map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": img.MIMEType,
				"data":       img.Base64Data,
			},
		})
	}
	if len(blocks) == 0 {
		return nil
	}
	return append([]any{map[string]any{"type": "text", "text": toolAttachmentLabel(msg)}}, blocks...)
}

func anthropicDocumentBlockForAttachment(attachment types.FileAttachment) map[string]any {
	if providerFileID := strings.TrimSpace(attachment.ProviderFileID); providerFileID != "" {
		switch strings.TrimSpace(strings.ToLower(attachment.ProviderFileType)) {
		case "container_upload":
			return map[string]any{"type": "container_upload", "file_id": providerFileID}
		case "image":
			return map[string]any{
				"type": "image",
				"source": map[string]any{
					"type":    "file",
					"file_id": providerFileID,
				},
			}
		case "document", "file":
			block := map[string]any{
				"type": "document",
				"source": map[string]any{
					"type":    "file",
					"file_id": providerFileID,
				},
			}
			if title := strings.TrimSpace(attachment.FileName); title != "" {
				block["title"] = title
			}
			return block
		}
	}

	if data := anthropicAttachmentBase64Payload(attachment.InlineDataBase64); data != "" && anthropicDirectImageAttachment(attachment) {
		return map[string]any{
			"type": "image",
			"source": map[string]any{
				"type":       "base64",
				"media_type": anthropicImageMediaType(attachment),
				"data":       data,
			},
		}
	}

	if !anthropicNativeDocumentCandidate(attachment) {
		return nil
	}

	data := anthropicAttachmentBase64Payload(attachment.InlineDataBase64)
	mimeType := anthropicAttachmentMIME(attachment)
	format := bedrockDocumentFormat(mimeType, attachment.FileName)
	if data != "" && format == "pdf" {
		block := map[string]any{
			"type": "document",
			"source": map[string]any{
				"type":       "base64",
				"media_type": "application/pdf",
				"data":       data,
			},
		}
		if title := strings.TrimSpace(attachment.FileName); title != "" {
			block["title"] = title
		}
		return block
	}

	if text := anthropicAttachmentText(attachment); text != "" {
		source := map[string]any{
			"type": "content",
			"content": []any{
				map[string]any{"type": "text", "text": text},
			},
		}
		if anthropicDirectTextDocumentAttachment(attachment) && strings.TrimSpace(attachment.InlineDataBase64) != "" {
			source = map[string]any{
				"type":       "text",
				"media_type": "text/plain",
				"data":       text,
			}
		}
		block := map[string]any{
			"type":   "document",
			"source": source,
		}
		if title := strings.TrimSpace(attachment.FileName); title != "" {
			block["title"] = title
		}
		return block
	}

	return nil
}

func toolAttachmentContentToBedrockMessages(msg types.ChatMessage) []any {
	images := vision.NormalizeAttachments(msg.ImageAttachments)
	files := types.CloneFileAttachments(msg.FileAttachments)
	blocks := make([]any, 0, len(images)+len(files)+1)
	docCount := 0
	for _, attachment := range files {
		data := strings.TrimSpace(attachment.InlineDataBase64)
		mimeType := strings.TrimSpace(strings.ToLower(attachment.MimeType))
		if mimeType == "" {
			mimeType = strings.TrimSpace(strings.ToLower(attachment.DetectedMimeType))
		}
		format := bedrockDocumentFormat(mimeType, attachment.FileName)
		if data != "" && format != "" {
			docCount++
			blocks = append(blocks, map[string]any{
				"document": map[string]any{
					"format": format,
					"name":   bedrockDocumentName(docCount),
					"source": map[string]any{
						"bytes": data,
					},
				},
			})
			continue
		}
		if preview := strings.TrimSpace(attachment.PreviewText); preview != "" {
			docCount++
			blocks = append(blocks, map[string]any{
				"document": map[string]any{
					"format": "txt",
					"name":   bedrockDocumentName(docCount),
					"source": map[string]any{
						"text": toolAttachmentTextLabel(attachment, types.SafeTruncate(preview)),
					},
				},
			})
		}
	}
	for _, img := range images {
		format := bedrockImageFormat(img.MIMEType)
		if format == "" {
			continue
		}
		blocks = append(blocks, map[string]any{
			"image": map[string]any{
				"format": format,
				"source": map[string]any{
					"bytes": img.Base64Data,
				},
			},
		})
	}
	if len(blocks) == 0 {
		return nil
	}
	return append([]any{map[string]any{"text": toolAttachmentLabel(msg)}}, blocks...)
}

func toolAttachmentLabel(msg types.ChatMessage) string {
	toolName := strings.TrimSpace(msg.Name)
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf("Attachment returned by %s.", toolName)
}

func toolAttachmentTextLabel(attachment types.FileAttachment, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	name := strings.TrimSpace(attachment.FileName)
	if name == "" {
		return text
	}
	return fmt.Sprintf("Extracted text from %s:\n\n%s", name, text)
}

func anthropicAttachmentText(attachment types.FileAttachment) string {
	data := strings.TrimSpace(attachment.InlineDataBase64)
	if data != "" && anthropicTextLikeAttachment(attachment) {
		if decoded, err := decodeAnthropicAttachmentBase64(data); err == nil {
			if text := strings.TrimSpace(string(decoded)); text != "" {
				return types.SafeTruncate(text)
			}
		}
	}
	if preview := strings.TrimSpace(attachment.PreviewText); preview != "" {
		return types.SafeTruncate(preview)
	}
	return ""
}

func fileAttachmentIsPDFForAnthropic(attachment types.FileAttachment) bool {
	if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(attachment.FileName))), ".") == "pdf" {
		return true
	}
	return anthropicAttachmentMIME(attachment) == "application/pdf"
}

func anthropicDirectTextDocumentAttachment(attachment types.FileAttachment) bool {
	mimeType := anthropicAttachmentMIME(attachment)
	if mimeType == "text/plain" {
		return true
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(attachment.FileName))), ".") {
	case "txt":
		return mimeType == "" || mimeType == "application/octet-stream" || strings.HasPrefix(mimeType, "text/")
	default:
		return false
	}
}

func anthropicDirectImageAttachment(attachment types.FileAttachment) bool {
	return anthropicImageMediaType(attachment) != ""
}

func anthropicImageMediaType(attachment types.FileAttachment) string {
	switch strings.TrimSpace(strings.ToLower(anthropicAttachmentMIME(attachment))) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/gif":
		return "image/gif"
	case "image/webp":
		return "image/webp"
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(attachment.FileName))), ".") {
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

func anthropicNativeDocumentCandidate(attachment types.FileAttachment) bool {
	if attachment.Capabilities != nil && attachment.Capabilities["canUseNativeDocument"] {
		return true
	}
	if attachment.IsText != nil && *attachment.IsText {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(attachment.FileKind)) {
	case "pdf", "text", "csv", "json", "html", "document", "spreadsheet", "presentation":
		return true
	}
	mimeType := anthropicAttachmentMIME(attachment)
	if mimeType == "application/pdf" || anthropicTextLikeMIME(mimeType) || anthropicOfficeLikeMIME(mimeType) {
		return true
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(attachment.FileName))), ".") {
	case "pdf", "txt", "md", "markdown", "csv", "tsv", "tab", "json", "jsonl", "ndjson", "html", "htm", "xml", "rtf", "doc", "docx", "ppt", "pptx", "xls", "xlsx",
		"yaml", "yml", "toml", "ini", "cfg", "conf", "log", "sql", "py", "go", "js", "jsx", "ts", "tsx", "rb", "rs", "java", "css", "scss", "sh", "bash", "zsh", "c", "h", "cpp", "hpp", "svg":
		return true
	default:
		return false
	}
}

func anthropicTextLikeAttachment(attachment types.FileAttachment) bool {
	if attachment.IsText != nil && *attachment.IsText {
		return true
	}
	return anthropicTextLikeMIME(anthropicAttachmentMIME(attachment))
}

func anthropicAttachmentMIME(attachment types.FileAttachment) string {
	return strings.TrimSpace(strings.ToLower(firstNonEmptyString(
		attachment.NormalizedMIME,
		attachment.MimeType,
		attachment.DetectedMimeType,
		attachment.DeclaredMIME,
		attachment.ExtensionMIME,
		attachment.SniffedMIME,
	)))
}

func anthropicTextLikeMIME(mimeType string) bool {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "text/plain",
		"text/markdown",
		"text/html",
		"text/xml",
		"text/csv",
		"text/tab-separated-values",
		"application/csv",
		"application/json",
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
		mimeType = strings.TrimSpace(strings.ToLower(mimeType))
		return strings.HasPrefix(mimeType, "text/") || strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml")
	}
}

func anthropicOfficeLikeMIME(mimeType string) bool {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return true
	default:
		return false
	}
}

func userContentToBedrockMessages(msg types.ChatMessage) any {
	text := ""
	if msg.Content != nil {
		text = types.SafeTruncate(*msg.Content)
	}

	images := vision.NormalizeAttachments(msg.ImageAttachments)
	files := types.CloneFileAttachments(msg.FileAttachments)
	blocks := make([]any, 0, len(images)+len(files)+1)
	docCount := 0
	for _, attachment := range files {
		data := strings.TrimSpace(attachment.InlineDataBase64)
		if data == "" {
			continue
		}
		mimeType := strings.TrimSpace(strings.ToLower(attachment.MimeType))
		if mimeType == "" {
			mimeType = strings.TrimSpace(strings.ToLower(attachment.DetectedMimeType))
		}
		format := bedrockDocumentFormat(mimeType, attachment.FileName)
		if format == "" {
			continue
		}
		docCount++
		blocks = append(blocks, map[string]any{
			"document": map[string]any{
				"format": format,
				"name":   bedrockDocumentName(docCount),
				"source": map[string]any{
					"bytes": data,
				},
			},
		})
	}
	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"text": text})
	} else if docCount > 0 {
		blocks = append(blocks, map[string]any{"text": "Please analyze the attached document."})
	}
	for _, img := range images {
		format := bedrockImageFormat(img.MIMEType)
		if format == "" {
			continue
		}
		blocks = append(blocks, map[string]any{
			"image": map[string]any{
				"format": format,
				"source": map[string]any{
					"bytes": img.Base64Data,
				},
			},
		})
	}
	if len(blocks) == 0 {
		return text
	}
	return blocks
}

func bedrockDocumentName(index int) string {
	if index <= 0 {
		return "Document"
	}
	return fmt.Sprintf("Document %d", index)
}

func bedrockDocumentFormat(mimeType, fileName string) string {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "application/pdf":
		return "pdf"
	case "text/plain":
		return "txt"
	case "text/markdown":
		return "md"
	case "text/html":
		return "html"
	case "text/csv", "application/csv":
		return "csv"
	case "application/msword":
		return "doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return "docx"
	case "application/vnd.ms-excel":
		return "xls"
	case "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet":
		return "xlsx"
	}
	ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(fileName))), ".")
	switch ext {
	case "pdf", "txt", "md", "html", "csv", "doc", "docx", "xls", "xlsx":
		return ext
	default:
		return ""
	}
}

func bedrockImageFormat(mimeType string) string {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	default:
		return ""
	}
}

func fileExt(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return ""
	}
	idx := strings.LastIndex(trimmed, ".")
	if idx < 0 || idx == len(trimmed)-1 {
		return ""
	}
	return trimmed[idx:]
}

func assistantSegmentsFromPayload(msg types.ChatMessage, validToolUseIDs map[string]struct{}) []any {
	segments := decodeAnthropicProviderSegments(msg.ProviderPayload, "")
	if len(segments) == 0 {
		return nil
	}
	out := make([]any, 0, len(segments))
	for _, segment := range segments {
		role, _ := segment["role"].(string)
		if strings.TrimSpace(role) != "assistant" {
			continue
		}
		content, _ := segment["content"].([]any)
		if len(content) == 0 {
			continue
		}
		filtered := filterAnthropicAssistantBlocks(msg, content, validToolUseIDs)
		if len(filtered) == 0 {
			continue
		}
		out = append(out, map[string]any{
			"role":    "assistant",
			"content": filtered,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterAnthropicAssistantBlocks(
	msg types.ChatMessage,
	blocks []any,
	validToolUseIDs map[string]struct{},
) []any {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]any, 0, len(blocks))
	requiresToolValidation := len(msg.ToolCalls) > 0
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block == nil {
			continue
		}
		kind, _ := block["type"].(string)
		if kind == "tool_use" && requiresToolValidation {
			id, _ := block["id"].(string)
			if _, ok := validToolUseIDs[strings.TrimSpace(id)]; !ok {
				continue
			}
		}
		out = append(out, deepCopyMap(block))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func collectValidToolUseIDs(messages []types.ChatMessage) map[string]struct{} {
	valid := make(map[string]struct{})

	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != types.RoleAssistant || len(msg.ToolCalls) == 0 {
			continue
		}

		expected := make(map[string]struct{}, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			if call.ProviderExecuted {
				continue
			}
			id := strings.TrimSpace(call.ID)
			if id == "" {
				continue
			}
			expected[id] = struct{}{}
		}
		if len(expected) == 0 {
			continue
		}

		found := make(map[string]struct{}, len(expected))
		for j := i + 1; j < len(messages); j++ {
			next := messages[j]
			if next.Role != types.RoleTool {
				break
			}
			id := strings.TrimSpace(next.ToolCallID)
			if id == "" {
				continue
			}
			if _, ok := expected[id]; ok {
				found[id] = struct{}{}
			}
		}

		for id := range found {
			valid[id] = struct{}{}
		}
	}

	return valid
}

func errorsIsContext(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "deadline")
}

// Ensure deterministic tool call ordering if needed.
func sortToolCalls(calls []types.ToolCall) {
	sort.SliceStable(calls, func(i, j int) bool {
		return calls[i].ID < calls[j].ID
	})
}
