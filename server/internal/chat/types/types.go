package types

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

const (
	toolResultTextLimitForPlanner   = 2048
	toolResultArrayLimitForPlanner  = 6
	toolResultMapKeyLimitForPlanner = 24
)

// Role matches the frontend provider layer and OpenAI-style message roles.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type ToolCall struct {
	ID   string         `json:"id"`
	Name string         `json:"name"` // canonical tool name
	Args map[string]any `json:"args"`

	// Optional raw JSON string for arguments (useful while streaming partial deltas).
	// When present, Args should represent the best-effort parsed form.
	ArgsText string `json:"argsText,omitempty"`

	// ProviderExecuted marks built-in provider/server tools such as Anthropic web_search.
	// They are displayed like tool calls, but must not be replayed as client-executed tool_use blocks.
	ProviderExecuted bool `json:"provider_executed,omitempty"`
}

type ImageAttachment struct {
	ArtifactID string `json:"artifactId,omitempty"`
	FileName   string `json:"fileName,omitempty"`
	MimeType   string `json:"mimeType,omitempty"`
	SizeBytes  *int64 `json:"sizeBytes,omitempty"`
	DataURI    string `json:"dataUri,omitempty"`
	Width      *int   `json:"width,omitempty"`
	Height     *int   `json:"height,omitempty"`
	Source     string `json:"source,omitempty"`
}

const (
	ThreadFileAttachmentToolName = "thread_file_attachment"
	ChatUploadAttachmentSource   = "chat_upload"
)

type FileAttachment struct {
	ArtifactID       string          `json:"artifactId,omitempty"`
	FileName         string          `json:"fileName,omitempty"`
	MimeType         string          `json:"mimeType,omitempty"`
	DetectedMimeType string          `json:"detectedMimeType,omitempty"`
	NormalizedMIME   string          `json:"normalizedMimeType,omitempty"`
	DeclaredMIME     string          `json:"declaredMimeType,omitempty"`
	ExtensionMIME    string          `json:"extensionMimeType,omitempty"`
	SniffedMIME      string          `json:"sniffedMimeType,omitempty"`
	FileKind         string          `json:"fileKind,omitempty"`
	SHA256           string          `json:"sha256,omitempty"`
	InlineDataBase64 string          `json:"inlineDataBase64,omitempty"`
	ProviderFileID   string          `json:"providerFileId,omitempty"`
	ProviderFileType string          `json:"providerFileType,omitempty"`
	PreviewText      string          `json:"previewText,omitempty"`
	ContentNote      string          `json:"contentNote,omitempty"`
	SizeBytes        *int64          `json:"sizeBytes,omitempty"`
	IsText           *bool           `json:"isText,omitempty"`
	PreviewAvailable *bool           `json:"previewAvailable,omitempty"`
	PreviewTruncated *bool           `json:"previewTruncated,omitempty"`
	Source           string          `json:"source,omitempty"`
	Capabilities     map[string]bool `json:"capabilities,omitempty"`
}

func CloneImageAttachments(in []ImageAttachment) []ImageAttachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]ImageAttachment, 0, len(in))
	for _, item := range in {
		dataURI := strings.TrimSpace(item.DataURI)
		if dataURI == "" {
			continue
		}
		clone := item
		clone.ArtifactID = strings.TrimSpace(item.ArtifactID)
		clone.DataURI = dataURI
		clone.FileName = strings.TrimSpace(item.FileName)
		clone.MimeType = strings.TrimSpace(strings.ToLower(item.MimeType))
		clone.Source = strings.TrimSpace(strings.ToLower(item.Source))
		out = append(out, clone)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func CloneFileAttachments(in []FileAttachment) []FileAttachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]FileAttachment, 0, len(in))
	for _, item := range in {
		artifactID := strings.TrimSpace(item.ArtifactID)
		if artifactID == "" {
			continue
		}
		clone := item
		clone.ArtifactID = artifactID
		clone.FileName = strings.TrimSpace(item.FileName)
		clone.MimeType = strings.TrimSpace(strings.ToLower(item.MimeType))
		clone.DetectedMimeType = strings.TrimSpace(strings.ToLower(item.DetectedMimeType))
		clone.NormalizedMIME = strings.TrimSpace(strings.ToLower(item.NormalizedMIME))
		clone.DeclaredMIME = strings.TrimSpace(strings.ToLower(item.DeclaredMIME))
		clone.ExtensionMIME = strings.TrimSpace(strings.ToLower(item.ExtensionMIME))
		clone.SniffedMIME = strings.TrimSpace(strings.ToLower(item.SniffedMIME))
		clone.FileKind = strings.TrimSpace(strings.ToLower(item.FileKind))
		clone.SHA256 = strings.TrimSpace(strings.ToLower(item.SHA256))
		clone.InlineDataBase64 = strings.TrimSpace(item.InlineDataBase64)
		clone.ProviderFileID = strings.TrimSpace(item.ProviderFileID)
		clone.ProviderFileType = strings.TrimSpace(strings.ToLower(item.ProviderFileType))
		clone.PreviewText = strings.TrimSpace(item.PreviewText)
		clone.ContentNote = strings.TrimSpace(item.ContentNote)
		clone.Source = strings.TrimSpace(strings.ToLower(item.Source))
		if len(item.Capabilities) > 0 {
			clone.Capabilities = make(map[string]bool, len(item.Capabilities))
			for key, value := range item.Capabilities {
				key = strings.TrimSpace(key)
				if key != "" {
					clone.Capabilities[key] = value
				}
			}
		}
		out = append(out, clone)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type ChatMessage struct {
	ID  string `json:"id,omitempty"`
	Seq *int64 `json:"seq,omitempty"`

	Role    Role    `json:"role"`
	Content *string `json:"content"` // nil allowed

	// Optional provider-native replay payload for preserving exact request/response state
	// across stateless providers such as Anthropic Messages and OpenAI Responses.
	ProviderPayload json.RawMessage `json:"provider_payload,omitempty"`

	ToolCalls []ToolCall `json:"tool_calls,omitempty"`

	ImageAttachments []ImageAttachment `json:"image_attachments,omitempty"`
	FileAttachments  []FileAttachment  `json:"file_attachments,omitempty"`

	// Tool result fields (for RoleTool messages).
	ToolCallID string `json:"tool_call_id,omitempty"`
	Name       string `json:"name,omitempty"` // tool name for tool result messages
}

type ToolFunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolDefinition struct {
	Type     string                 `json:"type"` // "function"
	Function ToolFunctionDefinition `json:"function"`
}

type ToolResultEnvelope struct {
	ArtifactID       string         `json:"artifact_id"`
	ToolName         string         `json:"tool_name,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	OK               bool           `json:"ok"`
	ResultFormat     string         `json:"result_format"`
	Result           any            `json:"result,omitempty"`
	QueryMetadata    map[string]any `json:"query_metadata,omitempty"`
	ToolArgs         map[string]any `json:"tool_args,omitempty"`
	ResultMetadata   map[string]any `json:"result_metadata,omitempty"`
	ToolPresentation map[string]any `json:"tool_presentation,omitempty"`
	PlotSpec         map[string]any `json:"plot_spec,omitempty"`
	ChartBudget      map[string]any `json:"chart_budget,omitempty"`
	ChartEngine      string         `json:"chart_engine,omitempty"`
	ChartSpecVersion string         `json:"chart_spec_version,omitempty"`
	ChartModule      string         `json:"chart_module,omitempty"`
	ChartModules     []string       `json:"chart_modules,omitempty"`
	PreviewOnly      bool           `json:"preview_only,omitempty"`
	FullArtifactID   string         `json:"full_artifact_id,omitempty"`
	RowCountTotal    int            `json:"row_count_total,omitempty"`
	DownsampleRatio  float64        `json:"downsample_ratio,omitempty"`
}

func ParseToolResultEnvelope(raw string) (ToolResultEnvelope, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ToolResultEnvelope{}, false
	}

	var env ToolResultEnvelope
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return ToolResultEnvelope{}, false
	}
	if strings.TrimSpace(env.ArtifactID) == "" {
		return ToolResultEnvelope{}, false
	}
	if strings.TrimSpace(env.ResultFormat) == "" {
		env.ResultFormat = "text"
	}
	return env, true
}

func CompactToolResultPayload(raw string) (string, bool) {
	env, ok := ParseToolResultEnvelope(raw)
	if !ok {
		return strings.TrimSpace(raw), false
	}

	result := env.Result
	if env.ResultFormat == "text" {
		result = compactToolResultText(env.Result)
	} else if m, ok := result.(map[string]any); ok {
		if preview, ok := m["result_preview"]; ok {
			result = compactToolResultPayloadValue(preview)
		} else {
			result = compactToolResultPayloadValue(result)
		}
	} else {
		result = compactToolResultPayloadValue(result)
	}

	compact := ToolResultEnvelope{
		ArtifactID:       strings.TrimSpace(env.ArtifactID),
		ToolName:         strings.TrimSpace(env.ToolName),
		ToolCallID:       strings.TrimSpace(env.ToolCallID),
		OK:               env.OK,
		ResultFormat:     strings.TrimSpace(env.ResultFormat),
		QueryMetadata:    env.QueryMetadata,
		ResultMetadata:   env.ResultMetadata,
		ToolPresentation: env.ToolPresentation,
		PlotSpec:         env.PlotSpec,
		ChartBudget:      env.ChartBudget,
		ChartEngine:      strings.TrimSpace(env.ChartEngine),
		ChartSpecVersion: strings.TrimSpace(env.ChartSpecVersion),
		ChartModule:      strings.TrimSpace(env.ChartModule),
		ChartModules:     append([]string(nil), env.ChartModules...),
		PreviewOnly:      env.PreviewOnly,
		FullArtifactID:   strings.TrimSpace(env.FullArtifactID),
		RowCountTotal:    env.RowCountTotal,
		DownsampleRatio:  env.DownsampleRatio,
		Result:           result,
	}
	b, err := json.Marshal(compact)
	if err != nil {
		return strings.TrimSpace(raw), false
	}
	return strings.TrimSpace(string(b)), true
}

func compactToolResultPayloadValue(value any) any {
	switch v := value.(type) {
	case []string:
		arr := make([]any, 0, len(v))
		for _, item := range v {
			arr = append(arr, item)
		}
		return compactToolResultArrayValue(arr)
	case map[string]any:
		out := make(map[string]any, min(toolResultMapKeyLimitForPlanner, len(v)))
		keysLeft := toolResultMapKeyLimitForPlanner

		addArrayPreview := func(key string, raw any) {
			if keysLeft <= 0 {
				return
			}
			out[key] = compactToolResultArrayValue(raw)
			out[key+"_total"] = countAnySlice(raw)
			out[key+"_returned"] = min(countAnySlice(raw), toolResultArrayLimitForPlanner)
			keysLeft--
		}

		if keysLeft > 0 {
			if _, ok := v["rows"]; ok {
				addArrayPreview("rows", v["rows"])
			}
		}
		if keysLeft > 0 {
			if _, ok := v["results"]; ok {
				addArrayPreview("results", v["results"])
			}
		}
		if keysLeft > 0 {
			if _, ok := v["evidence"]; ok {
				addArrayPreview("evidence", v["evidence"])
			}
		}
		if keysLeft > 0 {
			if _, ok := v["messages"]; ok {
				addArrayPreview("messages", v["messages"])
			}
		}

		for key, raw := range v {
			switch key {
			case "rows", "results", "evidence", "messages":
				continue
			}
			if keysLeft <= 0 {
				out["...truncated"] = true
				break
			}
			out[key] = compactToolResultPayloadValue(raw)
			keysLeft--
		}
		return out
	case []any:
		return compactToolResultArrayValue(v)
	case string:
		return compactToolResultText(v)
	default:
		if v == nil {
			return nil
		}
		b, err := json.Marshal(v)
		if err != nil {
			return compactToolResultText(fmt.Sprintf("%v", v))
		}
		return compactToolResultText(string(b))
	}
}

func compactToolResultArrayValue(value any) any {
	arr, ok := value.([]any)
	if !ok {
		return compactToolResultPayloadValue(value)
	}
	limit := min(len(arr), toolResultArrayLimitForPlanner)
	out := make([]any, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, compactToolResultPayloadValue(arr[i]))
	}
	if len(arr) > limit {
		out = append(out, map[string]any{
			"...truncated": true,
			"total":        len(arr),
			"returned":     limit,
		})
	}
	return out
}

func compactToolResultText(raw any) string {
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return truncateForToolPreview(v, toolResultTextLimitForPlanner)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return truncateForToolPreview(fmt.Sprintf("%v", v), toolResultTextLimitForPlanner)
		}
		return truncateForToolPreview(string(b), toolResultTextLimitForPlanner)
	}
}

func truncateForToolPreview(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "... [truncated]"
}

func countAnySlice(raw any) int {
	if arr, ok := raw.([]string); ok {
		return len(arr)
	}
	if arr, ok := raw.([]any); ok {
		return len(arr)
	}
	if raw == nil {
		return 0
	}
	rv := reflect.ValueOf(raw)
	if rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array {
		return rv.Len()
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func parseBoolValue(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case int:
		return b != 0
	case int8:
		return b != 0
	case int16:
		return b != 0
	case int32:
		return b != 0
	case int64:
		return b != 0
	case uint:
		return b != 0
	case uint8:
		return b != 0
	case uint16:
		return b != 0
	case uint32:
		return b != 0
	case uint64:
		return b != 0
	case float32:
		return b != 0
	case float64:
		return b != 0
	case string:
		return strings.EqualFold(strings.TrimSpace(b), "true")
	}
	return false
}

// Provider identifiers are stringly-typed in the server layer to match settings.json.

type CompatibleAPIType string

const (
	CompatibleAPIChatCompletions CompatibleAPIType = "chat_completions"
	CompatibleAPIResponses       CompatibleAPIType = "responses"
)

type ProviderAuth struct {
	Type   string  `json:"type"`             // "none" | "api_key"
	Header string  `json:"header"`           // e.g. Authorization, x-api-key
	Scheme *string `json:"scheme,omitempty"` // e.g. Bearer, nil for raw
}

type HeaderKV struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Value    string `json:"value"`
	IsSecret bool   `json:"isSecret,omitempty"`
}

type CompatibleProfile struct {
	ID          string            `json:"id"`
	DisplayName string            `json:"displayName"`
	APIType     CompatibleAPIType `json:"apiType"` // "chat_completions" | "responses"
	BaseURL     string            `json:"baseUrl"` // typically ends with /v1

	Auth   ProviderAuth `json:"auth"`
	APIKey *string      `json:"apiKey,omitempty"`

	ExtraHeaders    []HeaderKV        `json:"extraHeaders"`
	QueryParams     map[string]string `json:"queryParams,omitempty"`
	Overrides       map[string]any    `json:"payloadOverrides,omitempty"`
	TimeoutMs       *int              `json:"timeoutMs,omitempty"`
	ReasoningEffort *string           `json:"reasoningEffort,omitempty"`
	WebSearch       map[string]any    `json:"webSearch,omitempty"`
	CodeExecution   map[string]any    `json:"codeExecution,omitempty"`

	ModelInPath   *bool   `json:"modelInPath,omitempty"`
	ModelInBody   *bool   `json:"modelInBody,omitempty"`
	EndpointSufx  *string `json:"endpointSuffix,omitempty"`
	FileInputMode *string `json:"fileInputMode,omitempty"`
	// IncludeStreamUsage controls whether Chat Completions requests ask the
	// upstream endpoint to include usage in the stream. Nil lets the adapter use
	// its safe default (request usage, then retry without it on explicit
	// unsupported-parameter errors).
	IncludeStreamUsage *bool `json:"includeStreamUsage,omitempty"`
}

type ChatRequest struct {
	Model        string           `json:"model"`
	SystemPrompt string           `json:"systemPrompt"`
	Messages     []ChatMessage    `json:"messages"`
	Tools        []ToolDefinition `json:"tools"`

	Reasoning        map[string]any     `json:"reasoning,omitempty"`
	ReasoningEnabled *bool              `json:"reasoningEnabled,omitempty"`
	ProviderState    map[string]any     `json:"providerState,omitempty"`
	RuntimePlan      *RuntimePlanPolicy `json:"-"`
	UsageObserver    UsageObserver      `json:"-"`
}

// RuntimePlanPolicy is an internal, non-serialized authorization derived from
// the finalized preflight plan. Adapters use it to refuse silent API-surface or
// fidelity changes that were not declared before dispatch.
type RuntimePlanPolicy struct {
	PlanID                   string
	AllowSurfaceSwitch       bool
	AllowReducedFileFallback bool
}

type UsageQuality string

const (
	UsageQualityProviderReported UsageQuality = "provider_reported"
	UsageQualityDerived          UsageQuality = "derived"
	UsageQualityMissing          UsageQuality = "missing"
)

// UsageSemantics describes whether cache/reasoning counters are subsets of,
// or disjoint from, provider input/output counters. Generic compatible
// endpoints deliberately opt out of derivation unless configured explicitly.
type UsageSemantics string

const (
	UsageSemanticsGeneric         UsageSemantics = "generic"
	UsageSemanticsOpenAI          UsageSemantics = "openai"
	UsageSemanticsAnthropic       UsageSemantics = "anthropic"
	UsageSemanticsBedrockConverse UsageSemantics = "bedrock_converse"
	UsageSemanticsGemini          UsageSemantics = "gemini"
)

type UsageTelemetryKind string

const (
	UsageTelemetryDispatch UsageTelemetryKind = "dispatch"
	UsageTelemetrySnapshot UsageTelemetryKind = "snapshot"
)

// UsageTelemetry is internal-only and must never be serialized into provider
// payloads. Pointer counters preserve missing versus explicit zero.
type UsageTelemetry struct {
	Kind                 UsageTelemetryKind
	EffectiveAPISurface  string
	InputTokens          *int
	OutputTokens         *int
	ProviderTotalTokens  *int
	DerivedTotalTokens   *int
	CachedInputTokens    *int
	CacheWriteTokens     *int
	ReasoningTokens      *int
	UsageQuality         UsageQuality
	UsageSemantics       UsageSemantics
	UpstreamRequestCount int
	RequestID            *string
	ResolvedModel        *string
	SanitizedDetails     map[string]int
}

type UsageObserver func(UsageTelemetry) error

func NotifyUsageDispatch(req ChatRequest, effectiveAPISurface string) error {
	if req.UsageObserver == nil {
		return nil
	}
	return req.UsageObserver(UsageTelemetry{Kind: UsageTelemetryDispatch, EffectiveAPISurface: strings.TrimSpace(effectiveAPISurface), UpstreamRequestCount: 1})
}

func NotifyUsageSnapshot(req ChatRequest, meta ResponseMeta) error {
	if req.UsageObserver == nil {
		return nil
	}
	return req.UsageObserver(UsageTelemetry{
		Kind: UsageTelemetrySnapshot, EffectiveAPISurface: trimmedUsageString(meta.APIType),
		InputTokens: cloneUsageInt(meta.InputTokens), OutputTokens: cloneUsageInt(meta.OutputTokens),
		ProviderTotalTokens: cloneUsageInt(meta.ProviderTotalTokens), DerivedTotalTokens: cloneUsageInt(meta.DerivedTotalTokens),
		CachedInputTokens: cloneUsageInt(meta.CachedInputTokens), CacheWriteTokens: cloneUsageInt(meta.CacheWriteTokens),
		ReasoningTokens: cloneUsageInt(meta.ReasoningTokens), UsageQuality: meta.UsageQuality, UsageSemantics: meta.UsageSemantics,
		RequestID: cloneUsageString(meta.RequestID), ResolvedModel: cloneUsageString(meta.ResolvedModel),
		SanitizedDetails: cloneUsageDetails(meta.UsageDetails),
	})
}

type ResponseMeta struct {
	ProviderID string `json:"providerId"`
	Model      string `json:"model"`

	RequestID            *string        `json:"requestId,omitempty"`
	InputTokens          *int           `json:"inputTokens,omitempty"`
	OutputTokens         *int           `json:"outputTokens,omitempty"`
	TotalTokens          *int           `json:"totalTokens,omitempty"`
	ProviderTotalTokens  *int           `json:"providerTotalTokens,omitempty"`
	DerivedTotalTokens   *int           `json:"derivedTotalTokens,omitempty"`
	CachedInputTokens    *int           `json:"cachedInputTokens,omitempty"`
	CacheWriteTokens     *int           `json:"cacheWriteTokens,omitempty"`
	ReasoningTokens      *int           `json:"reasoningTokens,omitempty"`
	UsageQuality         UsageQuality   `json:"usageQuality,omitempty"`
	UsageSemantics       UsageSemantics `json:"usageSemantics,omitempty"`
	UsageDetails         map[string]int `json:"usageDetails,omitempty"`
	ResolvedModel        *string        `json:"resolvedModel,omitempty"`
	ProviderVersion      *string        `json:"providerVersion,omitempty"`
	APIType              *string        `json:"apiType,omitempty"`
	UpstreamRequestCount *int           `json:"upstreamRequestCount,omitempty"`
	ProfileID            *string        `json:"profileId,omitempty"`
	ProviderState        map[string]any `json:"providerState,omitempty"`
}

// NormalizeResponseMetaUsage preserves raw provider counters without deriving
// a generic input+output total. Generic compatible endpoints have no declared
// cache/reasoning relationship, so derivation would be unsafe.
func NormalizeResponseMetaUsage(meta ResponseMeta) ResponseMeta {
	if meta.ProviderTotalTokens == nil && meta.DerivedTotalTokens == nil && meta.TotalTokens != nil {
		if meta.UsageQuality == UsageQualityDerived {
			meta.DerivedTotalTokens = cloneUsageInt(meta.TotalTokens)
		} else {
			meta.ProviderTotalTokens = cloneUsageInt(meta.TotalTokens)
		}
	}
	if meta.TotalTokens == nil {
		meta.TotalTokens = firstUsageInt(meta.ProviderTotalTokens, meta.DerivedTotalTokens)
	}
	if meta.UsageQuality != "" {
		return meta
	}
	if meta.InputTokens != nil || meta.OutputTokens != nil || meta.ProviderTotalTokens != nil || meta.DerivedTotalTokens != nil ||
		meta.CachedInputTokens != nil || meta.CacheWriteTokens != nil || meta.ReasoningTokens != nil || len(meta.UsageDetails) > 0 {
		meta.UsageQuality = UsageQualityProviderReported
	} else {
		meta.UsageQuality = UsageQualityMissing
	}
	return meta
}

func NormalizeResponseMetaUsageForSemantics(meta ResponseMeta, semantics UsageSemantics) ResponseMeta {
	meta.UsageSemantics = semantics
	if meta.ProviderTotalTokens == nil && meta.DerivedTotalTokens == nil && meta.TotalTokens != nil {
		if meta.UsageQuality == UsageQualityDerived {
			meta.DerivedTotalTokens = cloneUsageInt(meta.TotalTokens)
		} else {
			meta.ProviderTotalTokens = cloneUsageInt(meta.TotalTokens)
		}
	}
	if meta.DerivedTotalTokens == nil {
		var derived *int
		switch semantics {
		case UsageSemanticsOpenAI:
			derived = sumKnownUsage(meta.InputTokens, meta.OutputTokens)
		case UsageSemanticsAnthropic, UsageSemanticsBedrockConverse:
			// Cache read/write tokens are disjoint from Anthropic/Bedrock input;
			// reasoning/thinking remains a subset of output.
			derived = sumKnownUsage(meta.InputTokens, meta.CachedInputTokens, meta.CacheWriteTokens, meta.OutputTokens)
		case UsageSemanticsGemini:
			// Cached content is a subset of prompt tokens; thoughts are disjoint
			// from candidate output tokens.
			derived = sumKnownUsage(meta.InputTokens, meta.OutputTokens, meta.ReasoningTokens)
		case UsageSemanticsGeneric:
			// Intentionally no derivation.
		}
		meta.DerivedTotalTokens = derived
	}
	meta.TotalTokens = firstUsageInt(meta.ProviderTotalTokens, meta.DerivedTotalTokens)
	if meta.ProviderTotalTokens != nil {
		meta.UsageQuality = UsageQualityProviderReported
	} else if meta.DerivedTotalTokens != nil {
		meta.UsageQuality = UsageQualityDerived
	}
	return NormalizeResponseMetaUsage(meta)
}

func sumKnownUsage(values ...*int) *int {
	total := 0
	for _, value := range values {
		if value == nil || *value < 0 {
			return nil
		}
		total += *value
	}
	return &total
}

func firstUsageInt(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return cloneUsageInt(value)
		}
	}
	return nil
}

func cloneUsageInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUsageString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := strings.TrimSpace(*value)
	if copy == "" {
		return nil
	}
	return &copy
}

func trimmedUsageString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func cloneUsageDetails(input map[string]int) map[string]int {
	if len(input) == 0 {
		return nil
	}
	out := make(map[string]int, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

const (
	EventThinkingStart        = "thinking_start"
	EventThinkingDelta        = "thinking_delta"
	EventThinkingEnd          = "thinking_end"
	EventCompactionStart      = "compaction_start"
	EventCompactionDelta      = "compaction_delta"
	EventCompactionEnd        = "compaction_end"
	EventTextDelta            = "text_delta"
	EventProviderToolProgress = "provider_tool_progress"
	EventPlanUpdate           = "plan_update"
	EventToolCalls            = "tool_calls"
	EventFinal                = "final"
	EventError                = "error"
)

type PlanUpdateChange struct {
	Kind         string  `json:"kind"`
	Feature      *string `json:"feature"`
	AttachmentID *string `json:"attachmentId"`
	Text         string  `json:"text"`
}

type PlanUpdateData struct {
	PlanID string           `json:"planId"`
	Change PlanUpdateChange `json:"change"`
}

type ThinkingStartData struct {
	TraceID string `json:"traceId"`
}

type ThinkingDeltaData struct {
	Content string `json:"content"`
}

type CompactionDeltaData struct {
	Content string `json:"content"`
}

type ThinkingEndData struct {
	Summary *string `json:"summary,omitempty"`
}

type TextDeltaData struct {
	Content string `json:"content"`
}

type ToolCallsData struct {
	ToolCalls       []ToolCall      `json:"toolCalls"`
	Meta            ResponseMeta    `json:"meta"`
	ProviderPayload json.RawMessage `json:"providerPayload,omitempty"`
}

type ProviderToolProgressData struct {
	ToolCallID       string           `json:"tool_call_id"`
	ToolName         string           `json:"tool_name"`
	Provider         string           `json:"provider,omitempty"`
	Integration      string           `json:"integration,omitempty"`
	AdapterFamily    string           `json:"adapter_family,omitempty"`
	Phase            string           `json:"phase,omitempty"`
	Label            string           `json:"label,omitempty"`
	Message          string           `json:"message,omitempty"`
	Status           string           `json:"status,omitempty"`
	State            string           `json:"state,omitempty"`
	Query            string           `json:"query,omitempty"`
	ResultsCount     int              `json:"results_count,omitempty"`
	ResultPreview    []map[string]any `json:"result_preview,omitempty"`
	ProviderExecuted bool             `json:"provider_executed"`
	Timestamp        int64            `json:"timestamp,omitempty"`
}

type FinalData struct {
	Answer          string          `json:"answer"`
	Meta            ResponseMeta    `json:"meta"`
	ProviderPayload json.RawMessage `json:"providerPayload,omitempty"`
	ToolCalls       []ToolCall      `json:"toolCalls,omitempty"`
	ToolResults     []ChatMessage   `json:"toolResults,omitempty"`
}

type ErrorData struct {
	Code      string        `json:"code"`
	Message   string        `json:"message"`
	Retryable bool          `json:"retryable"`
	Meta      *ResponseMeta `json:"meta,omitempty"`
}

type StreamEvent struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

type Provider interface {
	ID() string
	Model() string
	StreamChat(ctx context.Context, request ChatRequest) (<-chan StreamEvent, error)
}
