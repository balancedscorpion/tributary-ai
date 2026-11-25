package bedrock

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/requestguard"
	"ai-analyst-local/internal/chat/providers/toolnamemapper"
	"ai-analyst-local/internal/chat/providers/toolschema"
	"ai-analyst-local/internal/chat/providers/vision"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultRegion                = "us-east-1"
	defaultAnthropicVersion      = "bedrock-2023-05-31"
	defaultCompactionBeta        = "compact-2026-01-12"
	defaultCompactionEditType    = "compact_20260112"
	defaultCompactionInstruction = "Create a compact but complete handover summary. Include the latest user request, confirmed findings, key evidence (queries, jobs, artifacts, and message references), decisions made, and remaining next steps. Preserve critical context needed to continue accurately. Do not draft or continue the user-facing reply; output summary content only."
	defaultCompactionTrigger     = 170000
	minCompactionTriggerTokens   = 50000
	bedrockConverseMaxDocuments  = 5
	// Bedrock Converse document blocks are capped at 4.5 MB per document.
	bedrockConverseMaxDocumentBytes = (9 * 1024 * 1024) / 2
)

func MantleAnthropicBaseURL(region string) string {
	r := strings.TrimSpace(region)
	if r == "" {
		r = defaultRegion
	}
	return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/anthropic", r)
}

func PrepareMantleRequest(creds Credentials, region string) func(*http.Request, []byte) error {
	return func(req *http.Request, body []byte) error {
		return applyAuth(req, body, creds, region, "bedrock-mantle")
	}
}

type Credentials struct {
	APIKey          string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type Provider struct {
	providerID            string
	model                 string
	region                string
	baseURL               string
	anthropicVersion      string
	compactionEnabled     bool
	compactionInstruction string
	compactionTrigger     int
	headers               map[string]string
	creds                 Credentials
	maxRequestBytes       int64
	apiSurface            string
	client                *http.Client
}

func New(providerID, model, region, baseURL, anthropicVersion string, headers map[string]string, creds Credentials, compactionEnabled bool, compactionInstruction string, compactionTrigger int) *Provider {
	provider, _ := NewWithAPISurface(providerID, model, region, baseURL, anthropicVersion, headers, creds, compactionEnabled, compactionInstruction, compactionTrigger, "invoke_stream")
	return provider
}

func NewWithAPISurface(providerID, model, region, baseURL, anthropicVersion string, headers map[string]string, creds Credentials, compactionEnabled bool, compactionInstruction string, compactionTrigger int, apiSurface string) (*Provider, error) {
	apiSurface = strings.ToLower(strings.TrimSpace(apiSurface))
	switch apiSurface {
	case "", "messages", "bedrock_native", "invoke_stream":
		apiSurface = "invoke_stream"
	case "converse", "converse_stream":
		apiSurface = "converse_stream"
	default:
		return nil, fmt.Errorf("unsupported Bedrock native API surface %q", apiSurface)
	}
	r := strings.TrimSpace(region)
	if r == "" {
		r = defaultRegion
	}

	b := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if b == "" {
		b = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", r)
	}

	v := strings.TrimSpace(anthropicVersion)
	if v == "" {
		v = defaultAnthropicVersion
	}
	compactionInstr := strings.TrimSpace(compactionInstruction)
	if compactionInstr == "" {
		compactionInstr = defaultCompactionInstruction
	}
	if compactionTrigger < minCompactionTriggerTokens {
		compactionTrigger = defaultCompactionTrigger
	}

	merged := map[string]string{
		"Content-Type":    "application/json",
		"Accept":          "application/vnd.amazon.eventstream",
		"Accept-Encoding": "identity",
	}
	for k, val := range headers {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		merged[key] = val
	}

	return &Provider{
		providerID:            providerID,
		model:                 model,
		region:                r,
		baseURL:               b,
		anthropicVersion:      v,
		compactionEnabled:     compactionEnabled,
		compactionInstruction: compactionInstr,
		compactionTrigger:     compactionTrigger,
		headers:               merged,
		creds:                 creds,
		maxRequestBytes:       requestguard.DefaultMaxBytes,
		apiSurface:            apiSurface,
		client:                &http.Client{Timeout: 0},
	}, nil
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

type bedrockUsage struct {
	Input      *int
	Output     *int
	Total      *int
	Cached     *int
	CacheWrite *int
	Reasoning  *int
	Details    map[string]int
}

func (usage *bedrockUsage) merge(raw any) {
	values := mapFromAny(raw)
	if values == nil {
		return
	}
	if value := coerceInt(valueByAnyKey(values, "input_tokens", "inputTokens")); value != nil {
		usage.Input = value
	}
	if value := coerceInt(valueByAnyKey(values, "output_tokens", "outputTokens")); value != nil {
		usage.Output = value
	}
	if value := coerceInt(valueByAnyKey(values, "total_tokens", "totalTokens")); value != nil {
		usage.Total = value
	}
	if value := coerceInt(valueByAnyKey(values, "cache_read_input_tokens", "cacheReadInputTokens")); value != nil {
		usage.Cached = value
	}
	if value := coerceInt(valueByAnyKey(values, "cache_creation_input_tokens", "cacheCreationInputTokens", "cache_write_input_tokens", "cacheWriteInputTokens")); value != nil {
		usage.CacheWrite = value
	}
	if value := coerceInt(valueByAnyKey(values, "reasoning_tokens", "reasoningTokens", "thinking_tokens", "thinkingTokens")); value != nil {
		usage.Reasoning = value
	}
	if value := coerceInt(valueByAnyKey(values, "systemToolInputTokens", "system_tool_input_tokens")); value != nil {
		if usage.Details == nil {
			usage.Details = map[string]int{}
		}
		usage.Details["system_tool_input_tokens"] = *value
	}
}

func (usage bedrockUsage) apply(meta types.ResponseMeta) types.ResponseMeta {
	meta.InputTokens = usage.Input
	meta.OutputTokens = usage.Output
	meta.TotalTokens = usage.Total
	meta.CachedInputTokens = usage.Cached
	meta.CacheWriteTokens = usage.CacheWrite
	meta.ReasoningTokens = usage.Reasoning
	meta.UsageDetails = usage.Details
	semantics := types.UsageSemanticsAnthropic
	if meta.APIType != nil && strings.TrimSpace(*meta.APIType) == "converse_stream" {
		semantics = types.UsageSemanticsBedrockConverse
	}
	return types.NormalizeResponseMetaUsageForSemantics(meta, semantics)
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
	return nil
}

func stringByAnyKey(obj map[string]any, keys ...string) string {
	value := valueByAnyKey(obj, keys...)
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func stringPreserveWhitespaceByAnyKey(obj map[string]any, keys ...string) string {
	value := valueByAnyKey(obj, keys...)
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func converseContentBlockIndex(evt map[string]any) *int {
	return coerceInt(valueByAnyKey(evt, "contentBlockIndex", "content_block_index", "index"))
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

func shouldFallbackToInvoke(statusCode int, rawMessage string) bool {
	message := strings.ToLower(strings.TrimSpace(rawMessage))
	switch statusCode {
	case http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		// InvokeModel and InvokeModelWithResponseStream are separate IAM actions.
		// A credential can legitimately have the former without the latter.
		return true
	case http.StatusBadRequest:
		// Some models expose the Messages request shape through InvokeModel but do
		// not advertise response streaming. Retry only streaming-specific
		// validation failures so ordinary malformed requests are not duplicated.
		mentionsStreaming := strings.Contains(message, "stream") || strings.Contains(message, "invokemodelwithresponsestream")
		return mentionsStreaming && (strings.Contains(message, "not support") ||
			strings.Contains(message, "unsupported") ||
			strings.Contains(message, "not available") ||
			strings.Contains(message, "invalid operation"))
	default:
		return false
	}
}

func bedrockHTTPErrorMessage(body []byte, statusCode int) string {
	trimmed := strings.TrimSpace(string(body))
	if len(body) > 0 {
		var payload map[string]any
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&payload); err == nil {
			if message := stringByAnyKey(payload, "message", "Message", "originalMessage", "OriginalMessage"); message != "" {
				return message
			}
		}
	}
	if trimmed != "" {
		return trimmed
	}
	return fmt.Sprintf("HTTP %d", statusCode)
}

func applyBedrockResponseIdentity(meta *types.ResponseMeta, resp *http.Response) {
	if meta == nil || resp == nil {
		return
	}
	for _, header := range []string{"x-amzn-requestid", "x-request-id", "request-id"} {
		if requestID := strings.TrimSpace(resp.Header.Get(header)); requestID != "" {
			meta.RequestID = &requestID
			break
		}
	}
	for _, header := range []string{"x-amzn-bedrock-invoked-model-id", "x-amzn-bedrock-model-id"} {
		if returnedModel := strings.TrimSpace(resp.Header.Get(header)); returnedModel != "" {
			meta.ResolvedModel = &returnedModel
			break
		}
	}
}

func (p *Provider) streamInvokeFallback(
	ctx context.Context,
	out chan<- types.StreamEvent,
	req types.ChatRequest,
	model string,
	body []byte,
	metaBase types.ResponseMeta,
	nameMapper *toolnamemapper.Mapper,
	priorRequestCount int,
) {
	urlStr := fmt.Sprintf("%s/model/%s/invoke", p.baseURL, url.PathEscape(model))
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(body))
	if err != nil {
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase}})
		return
	}
	for key, value := range p.headers {
		hreq.Header.Set(key, value)
	}
	hreq.Header.Set("Accept", "application/json")
	hreq.Header.Del("X-Amzn-Bedrock-Accept")
	if err := applyAuth(hreq, body, p.creds, p.region, "bedrock"); err != nil {
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: "AUTH_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase}})
		return
	}
	if err := types.NotifyUsageDispatch(req, "invoke_stream"); err != nil {
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "usage accounting is unavailable", Retryable: false, Meta: &metaBase}})
		return
	}
	requestCount := priorRequestCount + 1
	metaBase.UpstreamRequestCount = &requestCount
	resp, err := p.client.Do(hreq)
	if err != nil {
		code := "PROVIDER_ERROR"
		retryable := true
		if errorsIsContext(err) {
			code = "ABORTED"
			retryable = false
		}
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: code, Message: err.Error(), Retryable: retryable, Meta: &metaBase}})
		return
	}
	if resp.Body == nil {
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "Missing InvokeModel response body", Retryable: true, Meta: &metaBase}})
		return
	}
	defer resp.Body.Close()
	applyBedrockResponseIdentity(&metaBase, resp)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		message := bedrockHTTPErrorMessage(raw, resp.StatusCode)
		code, retryable := requestguard.NormalizeProviderError(resp.StatusCode, message)
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: code, Message: message, Retryable: retryable, Meta: &metaBase}})
		return
	}
	var payload map[string]any
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "Invalid InvokeModel response: " + err.Error(), Retryable: true, Meta: &metaBase}})
		return
	}
	if requestID := stringByAnyKey(payload, "id", "request_id", "requestId"); requestID != "" {
		metaBase.RequestID = &requestID
	}
	if returnedModel := stringByAnyKey(payload, "model", "model_id", "modelId"); returnedModel != "" {
		metaBase.ResolvedModel = &returnedModel
	}
	usage := bedrockUsage{}
	usage.merge(valueByAnyKey(payload, "usage", "Usage"))
	meta := usage.apply(metaBase)
	_ = types.NotifyUsageSnapshot(req, meta)

	answer := ""
	toolCalls := make([]types.ToolCall, 0)
	thinkingActive := false
	blocks, _ := valueByAnyKey(payload, "content", "Content").([]any)
	for _, rawBlock := range blocks {
		block := mapFromAny(rawBlock)
		if block == nil {
			continue
		}
		switch stringByAnyKey(block, "type", "Type") {
		case "text":
			text := stringPreserveWhitespaceByAnyKey(block, "text", "Text")
			if text == "" {
				continue
			}
			answer += text
			send(ctx, out, types.StreamEvent{Event: types.EventTextDelta, Data: types.TextDeltaData{Content: text}})
		case "thinking":
			thinking := stringPreserveWhitespaceByAnyKey(block, "thinking", "text", "Text")
			if thinking == "" {
				continue
			}
			if !thinkingActive {
				thinkingActive = true
				traceID := "trace_" + strings.ReplaceAll(types.NewUUID(), "-", "")
				send(ctx, out, types.StreamEvent{Event: types.EventThinkingStart, Data: types.ThinkingStartData{TraceID: traceID}})
			}
			send(ctx, out, types.StreamEvent{Event: types.EventThinkingDelta, Data: types.ThinkingDeltaData{Content: thinking}})
		case "compaction":
			summary := sanitizeCompactionSummary(stringPreserveWhitespaceByAnyKey(block, "content", "summary", "text"))
			send(ctx, out, types.StreamEvent{Event: types.EventCompactionStart, Data: map[string]any{}})
			if summary != "" {
				send(ctx, out, types.StreamEvent{Event: types.EventCompactionDelta, Data: types.CompactionDeltaData{Content: summary}})
			}
			send(ctx, out, types.StreamEvent{Event: types.EventCompactionEnd, Data: map[string]any{}})
		case "tool_use":
			id := stringByAnyKey(block, "id")
			if id == "" {
				id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
			}
			providerName := stringByAnyKey(block, "name")
			args := mapFromAny(valueByAnyKey(block, "input", "Input"))
			if args == nil {
				args = map[string]any{}
			}
			argsJSON, _ := json.Marshal(args)
			toolCalls = append(toolCalls, types.ToolCall{
				ID: id, Name: nameMapper.CanonicalName(providerName), Args: args, ArgsText: string(argsJSON),
			})
		}
	}
	if answer == "" {
		if text := stringPreserveWhitespaceByAnyKey(payload, "completion", "text", "output_text"); text != "" {
			answer = text
			send(ctx, out, types.StreamEvent{Event: types.EventTextDelta, Data: types.TextDeltaData{Content: text}})
		}
	}
	if thinkingActive {
		send(ctx, out, types.StreamEvent{Event: types.EventThinkingEnd, Data: types.ThinkingEndData{Summary: nil}})
	}
	stopReason := stringByAnyKey(payload, "stop_reason", "stopReason")
	if len(toolCalls) > 0 || stopReason == "tool_use" {
		send(ctx, out, types.StreamEvent{Event: types.EventToolCalls, Data: types.ToolCallsData{ToolCalls: toolCalls, Meta: meta}})
		return
	}
	if strings.TrimSpace(answer) == "" {
		send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{Code: "PROVIDER_EMPTY_RESPONSE", Message: "Bedrock InvokeModel returned no assistant text or tool call", Retryable: true, Meta: &meta}})
		return
	}
	send(ctx, out, types.StreamEvent{Event: types.EventFinal, Data: types.FinalData{Answer: answer, Meta: meta}})
}

func (p *Provider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	out := make(chan types.StreamEvent, 64)
	model := req.Model
	if strings.TrimSpace(model) == "" {
		model = p.model
	}
	metaBase := types.ResponseMeta{ProviderID: p.providerID, Model: model}
	messagesAPIType := "invoke_stream"
	metaBase.APIType = &messagesAPIType

	go func() {
		defer close(out)

		nameMapper := toolnamemapper.New(0)
		for _, td := range req.Tools {
			_ = nameMapper.ProviderName(td.Function.Name)
		}

		nativeDocumentsRequireConverse := hasBedrockConverseNativeDocuments(req)
		explicitConverse := p.apiSurface == "converse_stream"
		if explicitConverse || nativeDocumentsRequireConverse {
			if nativeDocumentsRequireConverse && !explicitConverse && req.RuntimePlan != nil && !req.RuntimePlan.AllowSurfaceSwitch {
				send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{
					Code:      "UNDECLARED_SURFACE_SWITCH",
					Message:   "This request requires the Bedrock Converse API, but the finalized run plan did not authorize an API-surface switch.",
					Retryable: false,
					Meta:      &metaBase,
				}})
				return
			}
			fidelityReductions := bedrockConverseFidelityReductions(req)
			if req.RuntimePlan != nil && len(fidelityReductions) > 0 && !req.RuntimePlan.AllowReducedFileFallback {
				attachment := fidelityReductions[0]
				label := firstNonEmptyString(attachment.FileName, attachment.ArtifactID, "an attachment")
				send(ctx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{
					Code:      "UNDECLARED_FIDELITY_REDUCTION",
					Message:   fmt.Sprintf("Bedrock Converse must use extracted text for %s, but the finalized run plan did not authorize a reduced-fidelity attachment fallback.", label),
					Retryable: false,
					Meta:      &metaBase,
				}})
				return
			}
			if nativeDocumentsRequireConverse && !explicitConverse && req.RuntimePlan != nil {
				feature := "documents"
				send(ctx, out, types.StreamEvent{Event: types.EventPlanUpdate, Data: types.PlanUpdateData{
					PlanID: req.RuntimePlan.PlanID,
					Change: types.PlanUpdateChange{
						Kind: "surface_switch", Feature: &feature,
						Text: "Bedrock switched from InvokeModel to Converse for native document input.",
					},
				}})
				for _, attachment := range fidelityReductions {
					attachmentID := strings.TrimSpace(attachment.ArtifactID)
					var attachmentIDPtr *string
					if attachmentID != "" {
						attachmentIDPtr = &attachmentID
					}
					label := firstNonEmptyString(attachment.FileName, attachmentID, "attachment")
					send(ctx, out, types.StreamEvent{Event: types.EventPlanUpdate, Data: types.PlanUpdateData{
						PlanID: req.RuntimePlan.PlanID,
						Change: types.PlanUpdateChange{
							Kind:         "fidelity_reduction",
							Feature:      &feature,
							AttachmentID: attachmentIDPtr,
							Text:         fmt.Sprintf("Bedrock Converse used extracted text instead of native document input for %s.", label),
						},
					}})
				}
			}
			converseMeta := metaBase
			converseAPIType := "converse_stream"
			converseMeta.APIType = &converseAPIType
			p.streamConverseChat(ctx, out, req, model, converseMeta, nameMapper)
			return
		}

		urlStr := fmt.Sprintf("%s/model/%s/invoke-with-response-stream", p.baseURL, url.PathEscape(model))
		tools := toolsToAnthropic(req.Tools, nameMapper)
		messages := messagesToAnthropic(req, nameMapper)

		body := map[string]any{
			"anthropic_version": p.anthropicVersion,
			"messages":          messages,
			"max_tokens":        8192,
		}
		if p.compactionEnabled {
			body["anthropic_beta"] = []string{defaultCompactionBeta}
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
		if strings.TrimSpace(req.SystemPrompt) != "" {
			body["system"] = strings.TrimSpace(req.SystemPrompt)
		}
		if len(tools) > 0 {
			body["tools"] = tools
		}

		b, err := requestguard.MarshalJSON(body, requestguard.Context{
			ProviderID:  p.providerID,
			Model:       model,
			RequestKind: "bedrock_invoke_with_response_stream",
			Endpoint:    urlStr,
			MaxBytes:    p.maxRequestBytes,
		})
		if err != nil {
			code := requestguard.ErrorCodeFor(err)
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: code, Message: err.Error(), Retryable: false, Meta: &metaBase},
			})
			return
		}

		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(b))
		if err != nil {
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase},
			})
			return
		}
		for k, v := range p.headers {
			hreq.Header.Set(k, v)
		}

		if err := applyAuth(hreq, b, p.creds, p.region, "bedrock"); err != nil {
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: "AUTH_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase},
			})
			return
		}

		if err := types.NotifyUsageDispatch(req, "invoke_stream"); err != nil {
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "usage accounting is unavailable", Retryable: false, Meta: &metaBase}})
			return
		}
		requestCount := 1
		metaBase.UpstreamRequestCount = &requestCount
		resp, err := p.client.Do(hreq)
		if err != nil {
			code := "PROVIDER_ERROR"
			retryable := true
			if errorsIsContext(err) {
				code = "ABORTED"
				retryable = false
			}
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: code, Message: err.Error(), Retryable: retryable, Meta: &metaBase},
			})
			return
		}
		if resp.Body == nil {
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: "PROVIDER_ERROR", Message: "Missing response body", Retryable: true, Meta: &metaBase},
			})
			return
		}
		defer resp.Body.Close()
		applyBedrockResponseIdentity(&metaBase, resp)

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			msg := bedrockHTTPErrorMessage(raw, resp.StatusCode)
			if shouldFallbackToInvoke(resp.StatusCode, msg) {
				_ = resp.Body.Close()
				p.streamInvokeFallback(ctx, out, req, model, b, metaBase, nameMapper, requestCount)
				return
			}
			code, retryable := requestguard.NormalizeProviderError(resp.StatusCode, msg)
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: code, Message: msg, Retryable: retryable, Meta: &metaBase},
			})
			return
		}

		accumulatedText := ""
		toolCalls := make([]types.ToolCall, 0)
		toolUseInputsByIndex := map[int]*struct{ id, name, inputJSON string }{}
		var compactionBlockIndex *int
		var compactionSummaryBuffer strings.Builder

		traceID := "trace_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		thinkingActive := false
		var stopReason *string
		messageStopSeen := false

		requestID := metaBase.RequestID
		usage := bedrockUsage{}
		terminalMeta := func() types.ResponseMeta {
			meta := metaBase
			meta.RequestID = requestID
			return usage.apply(meta)
		}

		for {
			payload, err := readEventStreamPayload(resp.Body)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{
					Event: "error",
					Data:  types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: true, Meta: &meta},
				})
				return
			}

			chunkData, bedrockErrKey, bedrockErrMsg := extractBedrockChunk(payload)
			if bedrockErrKey != "" {
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{
					Event: "error",
					Data: types.ErrorData{
						Code:      "PROVIDER_ERROR",
						Message:   bedrockErrMsg,
						Retryable: isRetryableBedrockError(bedrockErrKey),
						Meta:      &meta,
					},
				})
				return
			}
			if len(chunkData) == 0 {
				continue
			}

			raw := strings.TrimSpace(string(chunkData))
			if raw == "" {
				continue
			}

			var env streamEnvelope
			dec := json.NewDecoder(strings.NewReader(raw))
			dec.UseNumber()
			if err := dec.Decode(&env); err != nil {
				continue
			}

			typ := ""
			if env.Type != nil {
				typ = *env.Type
			}

			switch typ {
			case "message_start":
				if m, ok := env.Message.(map[string]any); ok {
					if id, ok := m["id"].(string); ok {
						id2 := id
						requestID = &id2
					}
					if rawUsage, ok := m["usage"]; ok {
						usage.merge(rawUsage)
						_ = types.NotifyUsageSnapshot(req, terminalMeta())
					}
				}
			case "content_block_start":
				if env.Index == nil {
					continue
				}
				idx := *env.Index
				if block, ok := env.ContentBlock.(map[string]any); ok {
					bt, _ := block["type"].(string)
					if bt == "tool_use" {
						entry := &struct{ id, name, inputJSON string }{}
						if id, ok := block["id"].(string); ok {
							entry.id = id
						}
						if nm, ok := block["name"].(string); ok {
							entry.name = nm
						}
						toolUseInputsByIndex[idx] = entry
					} else if bt == "compaction" {
						idx2 := idx
						compactionBlockIndex = &idx2
						compactionSummaryBuffer.Reset()
						send(ctx, out, types.StreamEvent{Event: types.EventCompactionStart, Data: map[string]any{}})
					}
				}
			case "content_block_delta":
				if env.Delta == nil {
					continue
				}
				delta, _ := env.Delta.(map[string]any)
				if delta == nil {
					continue
				}
				if dt, _ := delta["type"].(string); dt == "compaction_delta" {
					if content, ok := delta["content"].(string); ok && content != "" {
						compactionSummaryBuffer.WriteString(content)
					}
					continue
				}
				if dt, _ := delta["type"].(string); dt == "text_delta" {
					if text, ok := delta["text"].(string); ok && text != "" {
						accumulatedText += text
						send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: text}})
					}
					continue
				}
				if dt, _ := delta["type"].(string); dt == "thinking_delta" {
					if thinking, ok := delta["thinking"].(string); ok && thinking != "" {
						if !thinkingActive {
							thinkingActive = true
							send(ctx, out, types.StreamEvent{Event: "thinking_start", Data: types.ThinkingStartData{TraceID: traceID}})
						}
						send(ctx, out, types.StreamEvent{Event: "thinking_delta", Data: types.ThinkingDeltaData{Content: thinking}})
					}
					continue
				}
				if dt, _ := delta["type"].(string); dt == "input_json_delta" {
					if env.Index == nil {
						continue
					}
					idx := *env.Index
					entry := toolUseInputsByIndex[idx]
					if entry == nil {
						continue
					}
					if partial, ok := delta["partial_json"].(string); ok && partial != "" {
						entry.inputJSON += partial
					}
					continue
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
				providerName := entry.name
				canonical := nameMapper.CanonicalName(providerName)
				argsText := entry.inputJSON
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
				id := strings.TrimSpace(entry.id)
				if id == "" {
					id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
				}
				toolCalls = append(toolCalls, types.ToolCall{ID: id, Name: canonical, Args: args, ArgsText: argsText})
			case "message_delta":
				if env.Delta != nil {
					if d, ok := env.Delta.(map[string]any); ok {
						if sr, ok := d["stop_reason"].(string); ok {
							stopReason = &sr
						}
					}
				}
				if env.Usage != nil {
					usage.merge(env.Usage)
					_ = types.NotifyUsageSnapshot(req, terminalMeta())
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
				goto DONE
			case "error":
				msgText := "Bedrock stream error"
				if env.Error != nil {
					if eobj, ok := env.Error.(map[string]any); ok {
						if m, ok := eobj["message"].(string); ok && strings.TrimSpace(m) != "" {
							msgText = m
						}
					}
				}
				code, retryable := requestguard.NormalizeStreamError(msgText)
				meta := terminalMeta()
				send(ctx, out, types.StreamEvent{
					Event: "error",
					Data:  types.ErrorData{Code: code, Message: msgText, Retryable: retryable, Meta: &meta},
				})
				return
			}
		}
	DONE:
		if !messageStopSeen {
			meta := terminalMeta()
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
				Code: "STREAM_ENDED", Message: "Bedrock Invoke stream ended before message_stop", Retryable: true, Meta: &meta,
			}})
			return
		}
		if thinkingActive {
			send(ctx, out, types.StreamEvent{Event: "thinking_end", Data: types.ThinkingEndData{Summary: nil}})
		}

		meta := terminalMeta()

		if len(toolCalls) > 0 || (stopReason != nil && *stopReason == "tool_use") {
			send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: toolCalls, Meta: meta}})
			return
		}

		answer := accumulatedText
		send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: answer, Meta: meta}})
	}()

	return out, nil
}

func (p *Provider) streamConverseChat(
	ctx context.Context,
	out chan<- types.StreamEvent,
	req types.ChatRequest,
	model string,
	metaBase types.ResponseMeta,
	nameMapper *toolnamemapper.Mapper,
) {
	urlStr := fmt.Sprintf("%s/model/%s/converse-stream", p.baseURL, url.PathEscape(model))
	messages := messagesToConverse(req, nameMapper)
	body := map[string]any{
		"messages": messages,
		"inferenceConfig": map[string]any{
			"maxTokens": 8192,
		},
	}
	if strings.TrimSpace(req.SystemPrompt) != "" {
		body["system"] = []any{map[string]any{"text": strings.TrimSpace(req.SystemPrompt)}}
	}
	if tools := toolsToConverse(req.Tools, nameMapper); len(tools) > 0 {
		body["toolConfig"] = map[string]any{"tools": tools}
	}

	b, err := requestguard.MarshalJSON(body, requestguard.Context{
		ProviderID:  p.providerID,
		Model:       model,
		RequestKind: "bedrock_converse_stream",
		Endpoint:    urlStr,
		MaxBytes:    p.maxRequestBytes,
	})
	if err != nil {
		code := requestguard.ErrorCodeFor(err)
		send(ctx, out, types.StreamEvent{
			Event: "error",
			Data:  types.ErrorData{Code: code, Message: err.Error(), Retryable: false, Meta: &metaBase},
		})
		return
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(b))
	if err != nil {
		send(ctx, out, types.StreamEvent{
			Event: "error",
			Data:  types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase},
		})
		return
	}
	for k, v := range p.headers {
		hreq.Header.Set(k, v)
	}

	if err := applyAuth(hreq, b, p.creds, p.region, "bedrock"); err != nil {
		send(ctx, out, types.StreamEvent{
			Event: "error",
			Data:  types.ErrorData{Code: "AUTH_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase},
		})
		return
	}

	if err := types.NotifyUsageDispatch(req, "converse_stream"); err != nil {
		send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "usage accounting is unavailable", Retryable: false, Meta: &metaBase}})
		return
	}
	requestCount := 1
	metaBase.UpstreamRequestCount = &requestCount
	resp, err := p.client.Do(hreq)
	if err != nil {
		code := "PROVIDER_ERROR"
		retryable := true
		if errorsIsContext(err) {
			code = "ABORTED"
			retryable = false
		}
		send(ctx, out, types.StreamEvent{
			Event: "error",
			Data:  types.ErrorData{Code: code, Message: err.Error(), Retryable: retryable, Meta: &metaBase},
		})
		return
	}
	defer resp.Body.Close()
	for _, header := range []string{"x-amzn-bedrock-invoked-model-id", "x-amzn-bedrock-model-id"} {
		if returnedModel := strings.TrimSpace(resp.Header.Get(header)); returnedModel != "" {
			metaBase.ResolvedModel = &returnedModel
			break
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		msg := strings.TrimSpace(string(text))
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		code, retryable := requestguard.NormalizeProviderError(resp.StatusCode, msg)
		send(ctx, out, types.StreamEvent{
			Event: "error",
			Data:  types.ErrorData{Code: code, Message: msg, Retryable: retryable, Meta: &metaBase},
		})
		return
	}

	if resp.Body == nil {
		send(ctx, out, types.StreamEvent{
			Event: "error",
			Data:  types.ErrorData{Code: "PROVIDER_ERROR", Message: "Missing response body", Retryable: true, Meta: &metaBase},
		})
		return
	}

	accumulatedText := ""
	toolCalls := make([]types.ToolCall, 0)
	toolUseInputsByIndex := map[int]*struct{ id, name, inputJSON string }{}
	traceID := "trace_" + strings.ReplaceAll(types.NewUUID(), "-", "")
	thinkingActive := false
	var stopReason *string
	messageStopSeen := false
	var requestID *string
	if rid := strings.TrimSpace(resp.Header.Get("x-amzn-requestid")); rid != "" {
		requestID = &rid
	}
	usage := bedrockUsage{}
	terminalMeta := func() types.ResponseMeta {
		meta := metaBase
		meta.RequestID = requestID
		return usage.apply(meta)
	}

	for {
		payload, err := readEventStreamPayload(resp.Body)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			meta := terminalMeta()
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data:  types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: true, Meta: &meta},
			})
			return
		}

		chunkData, bedrockErrKey, bedrockErrMsg := extractBedrockChunk(payload)
		if bedrockErrKey != "" {
			meta := terminalMeta()
			send(ctx, out, types.StreamEvent{
				Event: "error",
				Data: types.ErrorData{
					Code:      "PROVIDER_ERROR",
					Message:   bedrockErrMsg,
					Retryable: isRetryableBedrockError(bedrockErrKey),
					Meta:      &meta,
				},
			})
			return
		}
		if len(chunkData) == 0 {
			continue
		}

		raw := strings.TrimSpace(string(chunkData))
		if raw == "" {
			continue
		}
		var env map[string]any
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&env); err != nil {
			continue
		}

		if evt := mapFromAny(env["contentBlockStart"]); evt != nil {
			idx := converseContentBlockIndex(evt)
			if idx == nil {
				continue
			}
			start := mapFromAny(evt["start"])
			if start == nil {
				continue
			}
			if toolUse := mapFromAny(start["toolUse"]); toolUse != nil {
				entry := &struct{ id, name, inputJSON string }{
					id:   stringByAnyKey(toolUse, "toolUseId", "tool_use_id", "id"),
					name: stringByAnyKey(toolUse, "name"),
				}
				toolUseInputsByIndex[*idx] = entry
			}
			continue
		}

		if evt := mapFromAny(env["contentBlockDelta"]); evt != nil {
			idx := converseContentBlockIndex(evt)
			delta := mapFromAny(evt["delta"])
			if delta == nil {
				continue
			}
			if text := stringByAnyKey(delta, "text"); text != "" {
				accumulatedText += text
				send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: text}})
				continue
			}
			if reasoning := mapFromAny(delta["reasoningContent"]); reasoning != nil {
				if text := stringByAnyKey(reasoning, "text", "reasoningText"); text != "" {
					if !thinkingActive {
						thinkingActive = true
						send(ctx, out, types.StreamEvent{Event: "thinking_start", Data: types.ThinkingStartData{TraceID: traceID}})
					}
					send(ctx, out, types.StreamEvent{Event: "thinking_delta", Data: types.ThinkingDeltaData{Content: text}})
				}
				continue
			}
			if idx == nil {
				continue
			}
			if toolUse := mapFromAny(delta["toolUse"]); toolUse != nil {
				entry := toolUseInputsByIndex[*idx]
				if entry == nil {
					entry = &struct{ id, name, inputJSON string }{}
					toolUseInputsByIndex[*idx] = entry
				}
				if input := stringByAnyKey(toolUse, "input"); input != "" {
					entry.inputJSON += input
				}
			}
			continue
		}

		if evt := mapFromAny(env["contentBlockStop"]); evt != nil {
			idx := converseContentBlockIndex(evt)
			if idx == nil {
				continue
			}
			entry := toolUseInputsByIndex[*idx]
			if entry == nil {
				continue
			}
			providerName := entry.name
			canonical := nameMapper.CanonicalName(providerName)
			argsText := entry.inputJSON
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
			id := strings.TrimSpace(entry.id)
			if id == "" {
				id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
			}
			toolCalls = append(toolCalls, types.ToolCall{ID: id, Name: canonical, Args: args, ArgsText: argsText})
			continue
		}

		if evt := mapFromAny(env["messageStop"]); evt != nil {
			messageStopSeen = true
			if sr := stringByAnyKey(evt, "stopReason", "stop_reason"); sr != "" {
				stopReason = &sr
			}
			continue
		}

		if evt := mapFromAny(env["metadata"]); evt != nil {
			if rawUsage, ok := evt["usage"]; ok {
				usage.merge(rawUsage)
				_ = types.NotifyUsageSnapshot(req, terminalMeta())
			}
			continue
		}
	}
	if !messageStopSeen {
		meta := terminalMeta()
		send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
			Code: "STREAM_ENDED", Message: "Bedrock Converse stream ended before messageStop", Retryable: true, Meta: &meta,
		}})
		return
	}

	if thinkingActive {
		send(ctx, out, types.StreamEvent{Event: "thinking_end", Data: types.ThinkingEndData{Summary: nil}})
	}

	meta := terminalMeta()

	if len(toolCalls) > 0 || (stopReason != nil && strings.EqualFold(*stopReason, "tool_use")) {
		send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: toolCalls, Meta: meta}})
		return
	}

	send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: accumulatedText, Meta: meta}})
}

func readEventStreamPayload(r io.Reader) ([]byte, error) {
	prelude := make([]byte, 12)
	if _, err := io.ReadFull(r, prelude); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}

	totalLen := binary.BigEndian.Uint32(prelude[0:4])
	headersLen := binary.BigEndian.Uint32(prelude[4:8])
	if totalLen < 16 {
		return nil, fmt.Errorf("invalid event stream frame length: %d", totalLen)
	}

	remainingLen := int(totalLen) - 12
	rest := make([]byte, remainingLen)
	if _, err := io.ReadFull(r, rest); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}

	payloadStart := int(headersLen)
	payloadEnd := remainingLen - 4 // message CRC
	if payloadStart < 0 || payloadStart > payloadEnd {
		return nil, errors.New("invalid event stream payload offsets")
	}
	out := make([]byte, payloadEnd-payloadStart)
	copy(out, rest[payloadStart:payloadEnd])
	return out, nil
}

func extractBedrockChunk(payload []byte) (chunk []byte, errKey string, errMessage string) {
	raw := bytes.TrimSpace(payload)
	if len(raw) == 0 {
		return nil, "", ""
	}

	var env map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil {
		// Tolerate providers that may already emit Anthropic JSON chunks directly.
		return raw, "", ""
	}

	if chunkRaw, ok := env["chunk"]; ok {
		var c struct {
			Bytes string `json:"bytes"`
		}
		if err := json.Unmarshal(chunkRaw, &c); err == nil {
			data, dErr := base64.StdEncoding.DecodeString(strings.TrimSpace(c.Bytes))
			if dErr == nil {
				return data, "", ""
			}
		}
	}

	if bytesRaw, ok := env["bytes"]; ok {
		var b64 string
		if err := json.Unmarshal(bytesRaw, &b64); err == nil {
			data, dErr := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
			if dErr == nil {
				return data, "", ""
			}
		}
	}

	for _, key := range []string{
		"modelStreamErrorException",
		"internalServerException",
		"modelTimeoutException",
		"throttlingException",
		"validationException",
		"serviceUnavailableException",
		"accessDeniedException",
		"resourceNotFoundException",
	} {
		if rawErr, ok := env[key]; ok {
			return nil, key, errorMessageFromRaw(rawErr, key)
		}
	}

	// Not a Bedrock envelope; assume this is already an Anthropic chunk.
	return raw, "", ""
}

func errorMessageFromRaw(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	var obj struct {
		Message         string `json:"message"`
		OriginalMessage string `json:"originalMessage"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fallback
	}
	if strings.TrimSpace(obj.Message) != "" {
		return obj.Message
	}
	if strings.TrimSpace(obj.OriginalMessage) != "" {
		return obj.OriginalMessage
	}
	return fallback
}

func isRetryableBedrockError(errKey string) bool {
	switch errKey {
	case "internalServerException", "modelTimeoutException", "throttlingException", "serviceUnavailableException", "modelStreamErrorException":
		return true
	default:
		return false
	}
}

func applyAuth(req *http.Request, body []byte, creds Credentials, region string, service string) error {
	if apiKey := strings.TrimSpace(creds.APIKey); apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Del("x-amz-date")
		req.Header.Del("x-amz-content-sha256")
		req.Header.Del("x-amz-security-token")
		return nil
	}
	return signSigV4(req, body, creds, region, service)
}

func signSigV4(req *http.Request, body []byte, creds Credentials, region string, service string) error {
	accessKeyID := strings.TrimSpace(creds.AccessKeyID)
	secretAccessKey := strings.TrimSpace(creds.SecretAccessKey)
	if accessKeyID == "" || secretAccessKey == "" {
		return errors.New("bedrock authentication is not configured")
	}

	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	shortDate := now.Format("20060102")

	payloadHash := sha256Hex(body)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if tok := strings.TrimSpace(creds.SessionToken); tok != "" {
		req.Header.Set("x-amz-security-token", tok)
	}

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalQuery := canonicalQueryString(req.URL.Query())
	canonicalHeaders, signedHeaders := canonicalHeadersAndSignedHeaders(req)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQuery,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := shortDate + "/" + strings.TrimSpace(region) + "/" + strings.TrimSpace(service) + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(secretAccessKey, shortDate, strings.TrimSpace(region), strings.TrimSpace(service))
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	auth := fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKeyID,
		scope,
		signedHeaders,
		signature,
	)
	req.Header.Set("Authorization", auth)
	return nil
}

func canonicalQueryString(values url.Values) string {
	if len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		vals := append([]string(nil), values[k]...)
		sort.Strings(vals)
		if len(vals) == 0 {
			parts = append(parts, awsPercentEncode(k)+"=")
			continue
		}
		for _, v := range vals {
			parts = append(parts, awsPercentEncode(k)+"="+awsPercentEncode(v))
		}
	}
	return strings.Join(parts, "&")
}

func awsPercentEncode(s string) string {
	enc := url.QueryEscape(s)
	enc = strings.ReplaceAll(enc, "+", "%20")
	enc = strings.ReplaceAll(enc, "*", "%2A")
	enc = strings.ReplaceAll(enc, "%7E", "~")
	return enc
}

func canonicalHeadersAndSignedHeaders(req *http.Request) (string, string) {
	normalized := map[string]string{
		"host": strings.ToLower(req.URL.Host),
	}
	for k, vals := range req.Header {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" || lk == "authorization" {
			continue
		}
		if len(vals) == 0 {
			normalized[lk] = ""
			continue
		}
		cleanVals := make([]string, 0, len(vals))
		for _, v := range vals {
			cleanVals = append(cleanVals, collapseSpaces(v))
		}
		normalized[lk] = strings.Join(cleanVals, ",")
	}

	keys := make([]string, 0, len(normalized))
	for k := range normalized {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(normalized[k])
		b.WriteByte('\n')
	}
	return b.String(), strings.Join(keys, ";")
}

func collapseSpaces(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func deriveSigningKey(secretAccessKey string, date string, region string, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretAccessKey), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	_, _ = h.Write([]byte(data))
	return h.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func send(ctx context.Context, ch chan<- types.StreamEvent, ev types.StreamEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

func toolsToAnthropic(tools []types.ToolDefinition, mapper *toolnamemapper.Mapper) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":         mapper.ProviderName(t.Function.Name),
			"description":  t.Function.Description,
			"input_schema": toolschema.NormalizeFunctionParameters(t.Function.Parameters),
		})
	}
	return out
}

func toolsToConverse(tools []types.ToolDefinition, mapper *toolnamemapper.Mapper) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"toolSpec": map[string]any{
				"name":        mapper.ProviderName(t.Function.Name),
				"description": t.Function.Description,
				"inputSchema": map[string]any{
					"json": toolschema.NormalizeFunctionParameters(t.Function.Parameters),
				},
			},
		})
	}
	return out
}

func hasBedrockConverseNativeDocuments(req types.ChatRequest) bool {
	validToolUseIDs := collectValidToolUseIDs(req.Messages)
	for _, msg := range req.Messages {
		switch msg.Role {
		case types.RoleUser:
		case types.RoleTool:
			if _, ok := validToolUseIDs[strings.TrimSpace(msg.ToolCallID)]; !ok {
				continue
			}
		default:
			continue
		}
		for _, attachment := range msg.FileAttachments {
			if strings.TrimSpace(attachment.InlineDataBase64) != "" &&
				bedrockConverseDocumentWithinLimits(attachment) &&
				bedrockDocumentFormatForAttachment(attachment) != "" {
				return true
			}
			if bedrockConverseFallbackText(attachment) != "" {
				return true
			}
		}
	}
	return false
}

func messagesToConverse(req types.ChatRequest, mapper *toolnamemapper.Mapper) []any {
	out := make([]any, 0, len(req.Messages))
	pendingToolResults := make([]any, 0)
	documentCount := 0
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
				continue
			}
			content := ""
			if msg.Content != nil {
				content = types.SafeTruncate(*msg.Content)
			}
			toolResultBlocks := []any{map[string]any{
				"toolResult": map[string]any{
					"toolUseId": toolCallID,
					"content":   []any{map[string]any{"text": content}},
				},
			}}
			attachments, updatedDocumentCount := toolAttachmentContentToConverse(msg, documentCount)
			documentCount = updatedDocumentCount
			if len(attachments) > 0 {
				toolResultBlocks = append(toolResultBlocks, attachments...)
			}
			pendingToolResults = append(pendingToolResults, toolResultBlocks...)
			continue
		}

		flushToolResults()

		if msg.Role == types.RoleUser {
			content, updatedDocumentCount := userContentToConverseWithDocumentCount(msg, documentCount)
			documentCount = updatedDocumentCount
			switch typed := content.(type) {
			case nil:
				out = append(out, map[string]any{"role": "user", "content": []any{map[string]any{"text": ""}}})
			case string:
				out = append(out, map[string]any{"role": "user", "content": []any{map[string]any{"text": typed}}})
			case []any:
				out = append(out, map[string]any{"role": "user", "content": typed})
			default:
				out = append(out, map[string]any{"role": "user", "content": typed})
			}
			continue
		}
		if msg.Role == types.RoleAssistant {
			blocks := make([]any, 0)
			if msg.Content != nil && strings.TrimSpace(*msg.Content) != "" {
				blocks = append(blocks, map[string]any{"text": types.SafeTruncate(*msg.Content)})
			}
			for _, call := range msg.ToolCalls {
				if call.ProviderExecuted {
					continue
				}
				if _, ok := validToolUseIDs[strings.TrimSpace(call.ID)]; !ok {
					continue
				}
				input := call.Args
				if input == nil {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"toolUse": map[string]any{
						"toolUseId": call.ID,
						"name":      mapper.ProviderName(call.Name),
						"input":     input,
					},
				})
			}
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

func userContentToConverse(msg types.ChatMessage) any {
	content, _ := userContentToConverseWithDocumentCount(msg, 0)
	return content
}

func userContentToConverseWithDocumentCount(msg types.ChatMessage, documentCount int) (any, int) {
	text := ""
	if msg.Content != nil {
		text = types.SafeTruncate(*msg.Content)
	}

	images := vision.NormalizeAttachments(msg.ImageAttachments)
	files := types.CloneFileAttachments(msg.FileAttachments)
	blocks := make([]any, 0, len(images)+len(files)+1)

	converted := bedrockConverseAttachmentContent(files, documentCount)

	if strings.TrimSpace(text) != "" {
		blocks = append(blocks, map[string]any{"text": text})
	} else if len(converted.blocks) > 0 {
		blocks = append(blocks, map[string]any{"text": "Please analyze the attached document."})
	}
	blocks = append(blocks, converted.blocks...)

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
		return text, converted.documentCount
	}
	return blocks, converted.documentCount
}

func toolAttachmentContentToConverse(msg types.ChatMessage, documentCount int) ([]any, int) {
	images := vision.NormalizeAttachments(msg.ImageAttachments)
	files := types.CloneFileAttachments(msg.FileAttachments)
	blocks := make([]any, 0, len(images)+len(files)+1)

	converted := bedrockConverseAttachmentContent(files, documentCount)
	blocks = append(blocks, converted.blocks...)

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
		return nil, converted.documentCount
	}
	return append([]any{map[string]any{"text": toolAttachmentLabel(msg)}}, blocks...), converted.documentCount
}

func toolAttachmentLabel(msg types.ChatMessage) string {
	toolName := strings.TrimSpace(msg.Name)
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf("Attachment returned by %s.", toolName)
}

type bedrockConverseAttachmentConversion struct {
	blocks        []any
	reductions    []types.FileAttachment
	documentCount int
}

// bedrockConverseAttachmentContent is the single authority for native document
// and extracted-text attachment conversion. documentCount is shared across
// messages because Bedrock permits at most five document blocks in a request.
func bedrockConverseAttachmentContent(files []types.FileAttachment, documentCount int) bedrockConverseAttachmentConversion {
	result := bedrockConverseAttachmentConversion{documentCount: documentCount}
	if result.documentCount < 0 {
		result.documentCount = 0
	}

	for _, attachment := range files {
		data := strings.TrimSpace(attachment.InlineDataBase64)
		format := bedrockDocumentFormatForAttachment(attachment)
		native := data != "" && format != "" && bedrockConverseDocumentWithinLimits(attachment)
		if native && result.documentCount < bedrockConverseMaxDocuments {
			result.documentCount++
			document := map[string]any{
				"format": format,
				"name":   bedrockDocumentName(result.documentCount),
				"source": map[string]any{"bytes": data},
			}
			if format == "pdf" {
				// Bedrock Converse requires citations for Claude's full visual PDF understanding.
				document["citations"] = map[string]any{"enabled": true}
			}
			result.blocks = append(result.blocks, map[string]any{"document": document})
			continue
		}

		fallbackText := bedrockConverseFallbackText(attachment)
		if fallbackText == "" {
			continue
		}
		result.reductions = append(result.reductions, attachment)
		if result.documentCount < bedrockConverseMaxDocuments {
			result.documentCount++
			result.blocks = append(result.blocks, map[string]any{
				"document": map[string]any{
					"format": "txt",
					"name":   bedrockDocumentName(result.documentCount),
					"source": map[string]any{"text": fallbackText},
				},
			})
			continue
		}

		// Preserve extractable overflow without creating a sixth Bedrock document
		// block. An ordinary text content block is not subject to the document cap.
		result.blocks = append(result.blocks, map[string]any{"text": fallbackText})
	}
	return result
}

func bedrockConverseFidelityReductions(req types.ChatRequest) []types.FileAttachment {
	validToolUseIDs := collectValidToolUseIDs(req.Messages)
	reductions := make([]types.FileAttachment, 0)
	seen := make(map[string]struct{})
	documentCount := 0

	appendReductions := func(items []types.FileAttachment) {
		for _, attachment := range items {
			key := strings.TrimSpace(attachment.ArtifactID)
			if key == "" {
				key = strings.Join([]string{
					strings.TrimSpace(attachment.FileName),
					strings.TrimSpace(attachment.SHA256),
					bedrockAttachmentMIME(attachment),
				}, "|")
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			reductions = append(reductions, attachment)
		}
	}

	for _, msg := range req.Messages {
		if msg.Role == types.RoleTool {
			if _, ok := validToolUseIDs[strings.TrimSpace(msg.ToolCallID)]; !ok {
				continue
			}
			converted := bedrockConverseAttachmentContent(types.CloneFileAttachments(msg.FileAttachments), documentCount)
			documentCount = converted.documentCount
			appendReductions(converted.reductions)
			continue
		}
		if msg.Role != types.RoleUser {
			continue
		}
		converted := bedrockConverseAttachmentContent(types.CloneFileAttachments(msg.FileAttachments), documentCount)
		documentCount = converted.documentCount
		appendReductions(converted.reductions)
	}
	return reductions
}

func bedrockConverseFallbackText(attachment types.FileAttachment) string {
	// PreviewText is already extracted content and remains useful even for a
	// natively supported document that overflows Bedrock's five-document cap.
	if preview := strings.TrimSpace(attachment.PreviewText); preview != "" {
		return bedrockAttachmentTextLabel(attachment, types.SafeTruncate(preview))
	}
	if !bedrockConverseFallbackCandidate(attachment) {
		return ""
	}
	data := strings.TrimSpace(attachment.InlineDataBase64)
	if data != "" && bedrockTextLikeAttachment(attachment) {
		if decoded, err := base64.StdEncoding.DecodeString(data); err == nil {
			if text := strings.TrimSpace(string(decoded)); text != "" {
				return bedrockAttachmentTextLabel(attachment, types.SafeTruncate(text))
			}
		}
	}
	return ""
}

func bedrockAttachmentTextLabel(attachment types.FileAttachment, text string) string {
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

func bedrockConverseFallbackCandidate(attachment types.FileAttachment) bool {
	if attachment.Capabilities != nil && attachment.Capabilities["canUseNativeDocument"] {
		return true
	}
	if attachment.IsText != nil && *attachment.IsText {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(attachment.FileKind)) {
	case "text", "csv", "json", "html", "document", "spreadsheet", "presentation":
		return true
	}
	mimeType := bedrockAttachmentMIME(attachment)
	if bedrockTextLikeMIME(mimeType) || bedrockOfficeLikeMIME(mimeType) {
		return true
	}
	switch fileExtLower(attachment.FileName) {
	case "txt", "md", "markdown", "csv", "tsv", "tab", "json", "jsonl", "ndjson", "html", "htm", "xml", "rtf", "doc", "docx", "ppt", "pptx", "xls", "xlsx",
		"yaml", "yml", "toml", "ini", "cfg", "conf", "log", "sql", "py", "go", "js", "jsx", "ts", "tsx", "rb", "rs", "java", "css", "scss", "sh", "bash", "zsh", "c", "h", "cpp", "hpp", "svg":
		return true
	default:
		return false
	}
}

func bedrockTextLikeAttachment(attachment types.FileAttachment) bool {
	if attachment.IsText != nil && *attachment.IsText {
		return true
	}
	return bedrockTextLikeMIME(bedrockAttachmentMIME(attachment))
}

func bedrockAttachmentMIME(attachment types.FileAttachment) string {
	return firstNonEmptyString(
		attachment.NormalizedMIME,
		attachment.MimeType,
		attachment.DetectedMimeType,
		attachment.DeclaredMIME,
		attachment.ExtensionMIME,
		attachment.SniffedMIME,
	)
}

func bedrockDocumentName(index int) string {
	if index <= 0 {
		return "Document"
	}
	return fmt.Sprintf("Document %d", index)
}

func bedrockDocumentFormatForAttachment(attachment types.FileAttachment) string {
	return bedrockDocumentFormat(bedrockAttachmentMIME(attachment), attachment.FileName)
}

func bedrockConverseDocumentWithinLimits(attachment types.FileAttachment) bool {
	if attachment.SizeBytes != nil && *attachment.SizeBytes > bedrockConverseMaxDocumentBytes {
		return false
	}
	data := strings.TrimSpace(attachment.InlineDataBase64)
	if data == "" {
		return false
	}
	return base64DecodedLength(data) <= bedrockConverseMaxDocumentBytes
}

func base64DecodedLength(data string) int64 {
	clean := strings.TrimSpace(data)
	if clean == "" {
		return 0
	}
	padding := 0
	for i := len(clean) - 1; i >= 0 && clean[i] == '='; i-- {
		padding++
	}
	return int64((len(clean)*3)/4 - padding)
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
	switch fileExtLower(fileName) {
	case "pdf", "txt", "md", "html", "csv", "doc", "docx", "xls", "xlsx":
		return fileExtLower(fileName)
	default:
		return ""
	}
}

func bedrockTextLikeMIME(mimeType string) bool {
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

func bedrockOfficeLikeMIME(mimeType string) bool {
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

func bedrockImageFormat(mimeType string) string {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}

func fileExtLower(name string) string {
	clean := strings.TrimSpace(name)
	if clean == "" {
		return ""
	}
	dot := strings.LastIndex(clean, ".")
	if dot < 0 || dot == len(clean)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(clean[dot+1:]))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(strings.ToLower(value))
		}
	}
	return ""
}

func messagesToAnthropic(req types.ChatRequest, mapper *toolnamemapper.Mapper) []any {
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
				continue
			}
			content := ""
			if msg.Content != nil {
				content = types.SafeTruncate(*msg.Content)
			}
			pendingToolResults = append(pendingToolResults, map[string]any{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     content,
			})
			continue
		}

		flushToolResults()

		if msg.Role == types.RoleUser {
			text := ""
			if msg.Content != nil {
				text = types.SafeTruncate(*msg.Content)
			}
			images := vision.NormalizeAttachments(msg.ImageAttachments)
			if len(images) == 0 {
				out = append(out, map[string]any{"role": "user", "content": text})
				continue
			}
			blocks := make([]any, 0, len(images)+1)
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
			out = append(out, map[string]any{"role": "user", "content": blocks})
			continue
		}
		if msg.Role == types.RoleAssistant {
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
			// Emitting an empty text block is rejected by Anthropic-compatible APIs.
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
