package openai_compat

import (
	"bytes"
	"context"
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
	"ai-analyst-local/internal/chat/providers/stream"
	"ai-analyst-local/internal/chat/providers/toolnamemapper"
	"ai-analyst-local/internal/chat/providers/toolschema"
	"ai-analyst-local/internal/chat/providers/vision"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

type APIType string

const (
	APIChatCompletions APIType = "chat_completions"
	APIResponses       APIType = "responses"
)

type FileInputMode string

const (
	FileInputSidecarOnly     FileInputMode = "sidecar_only"
	FileInputNativePDFOnly   FileInputMode = "native_pdf_only"
	FileInputNativeSupported FileInputMode = "native_supported"
)

const defaultCompactionThresholdTokens = 200000

type Profile struct {
	ProviderID string
	ProfileID  *string
	APIType    APIType
	BaseURL    string

	QueryParams        map[string]string
	ExtraHeaders       map[string]string
	PayloadOverrides   map[string]any
	TimeoutMs          *int
	ReasoningEffort    *string
	ModelInPath        bool
	ModelInBody        bool
	EndpointSuffix     *string
	FileInputMode      FileInputMode
	IncludeStreamUsage *bool
	UsageSemantics     types.UsageSemantics
	WebSearch          *WebSearchConfig
	CodeExecution      *CodeExecutionConfig

	CompactionEnabled         bool
	CompactionThresholdTokens int
	MaxRequestBytes           int64
}

type WebSearchConfig struct {
	Enabled           bool
	ToolType          string
	SearchContextSize string
	AllowedDomains    []string
	BlockedDomains    []string
}

type CodeExecutionConfig struct {
	Enabled       bool
	ToolType      string
	ContainerMode string
	ContainerID   string
	MemoryLimit   string
}

type Provider struct {
	providerID string
	model      string
	profile    Profile

	httpc *http.Client
}

func New(providerID string, model string, profile Profile) *Provider {
	c := &http.Client{Timeout: 0}
	return &Provider{providerID: providerID, model: model, profile: profile, httpc: c}
}

func (p *Provider) ID() string    { return p.providerID }
func (p *Provider) Model() string { return p.model }

func (p *Provider) Capabilities() capabilities.ModelCapabilities {
	limits := capabilities.OpenAIModelTokenLimits(p.model)
	nativeCompaction := capabilities.NativeCompactionSupport{Mode: capabilities.NativeCompactionNone}
	supportsPreviousResponseID := false
	preservesOpaque := capabilities.OpaqueCompactionPreservation{}

	if p.profile.APIType == APIResponses {
		threshold := p.profile.CompactionThresholdTokens
		if threshold <= 0 {
			threshold = defaultCompactionThresholdTokens
		}
		nativeCompaction = capabilities.NativeCompactionSupport{
			Mode:          capabilities.NativeCompactionOpenAIContextManagement,
			Enabled:       p.profile.CompactionEnabled,
			TriggerTokens: threshold,
		}
		supportsPreviousResponseID = true
		preservesOpaque = capabilities.OpaqueCompactionPreservation{
			ProviderPayload: true,
			ProviderState:   true,
		}
	}

	return capabilities.Normalize(capabilities.ModelCapabilities{
		ProviderID:                   p.providerID,
		Model:                        p.model,
		MaxInputTokens:               limits.MaxInputTokens,
		MaxOutputTokens:              limits.MaxOutputTokens,
		NativeCompaction:             nativeCompaction,
		SupportsPreviousResponseID:   supportsPreviousResponseID,
		OpaqueCompactionPreservation: preservesOpaque,
	})
}

func normalizeBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimRight(trimmed, "/")
	return trimmed
}

func applyQueryParams(u string, params map[string]string) (string, error) {
	if len(params) == 0 {
		return u, nil
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	q := parsed.Query()
	for k, v := range params {
		if strings.TrimSpace(k) == "" {
			continue
		}
		q.Set(k, v)
	}
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

func buildEndpointURL(profile Profile, model string) (string, error) {
	base := normalizeBaseURL(profile.BaseURL)
	base, err := applyQueryParams(base, profile.QueryParams)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(parsed.Path, "/")

	endpointSegment := "/chat/completions"
	if profile.APIType == APIResponses {
		endpointSegment = "/responses"
	}

	if !strings.Contains(path, endpointSegment) {
		if profile.ModelInPath {
			path = path + "/" + url.PathEscape(model)
		}
		path = path + endpointSegment
	}
	if profile.EndpointSuffix != nil {
		suffix := strings.TrimSpace(*profile.EndpointSuffix)
		if suffix != "" {
			if strings.HasPrefix(suffix, "/") {
				path = path + suffix
			} else {
				path = path + "/" + suffix
			}
		}
	}

	parsed.Path = path
	return parsed.String(), nil
}

func coerceInt(v any) *int {
	switch t := v.(type) {
	case float64:
		i := int(t)
		if i < 0 {
			return nil
		}
		return &i
	case int:
		i := t
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

type chatCompletionsChunk struct {
	ID                *string `json:"id,omitempty"`
	Model             *string `json:"model,omitempty"`
	SystemFingerprint *string `json:"system_fingerprint,omitempty"`
	Choices           []struct {
		Delta *struct {
			Content   *string `json:"content,omitempty"`
			ToolCalls []struct {
				Index    *int    `json:"index,omitempty"`
				ID       *string `json:"id,omitempty"`
				Function *struct {
					Name      *string `json:"name,omitempty"`
					Arguments *string `json:"arguments,omitempty"`
				} `json:"function,omitempty"`
			} `json:"tool_calls,omitempty"`
		} `json:"delta,omitempty"`
		FinishReason *string `json:"finish_reason,omitempty"`
	} `json:"choices,omitempty"`
	Usage any `json:"usage,omitempty"`
}

type normalizedUsage struct {
	Input      *int
	Output     *int
	Total      *int
	Cached     *int
	CacheWrite *int
	Reasoning  *int
	Details    map[string]int
}

func (usage *normalizedUsage) merge(update normalizedUsage) {
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

func usageMapValue(obj map[string]any, key string) map[string]any {
	value, _ := obj[key].(map[string]any)
	return value
}

func addUsageDetail(details map[string]int, key string, value any) map[string]int {
	parsed := coerceInt(value)
	if parsed == nil {
		return details
	}
	if details == nil {
		details = map[string]int{}
	}
	details[key] = *parsed
	return details
}

func parseOpenAIUsage(raw any) normalizedUsage {
	usage, _ := raw.(map[string]any)
	if usage == nil {
		return normalizedUsage{}
	}

	result := normalizedUsage{
		Input:  coerceInt(usage["prompt_tokens"]),
		Output: coerceInt(usage["completion_tokens"]),
		Total:  coerceInt(usage["total_tokens"]),
	}
	if result.Input == nil {
		result.Input = coerceInt(usage["input_tokens"])
	}
	if result.Output == nil {
		result.Output = coerceInt(usage["output_tokens"])
	}
	result.Cached = coerceInt(usage["cached_tokens"])
	if result.Cached == nil {
		result.Cached = coerceInt(usage["cache_read_input_tokens"])
	}
	result.CacheWrite = coerceInt(usage["cache_creation_input_tokens"])
	if result.CacheWrite == nil {
		result.CacheWrite = coerceInt(usage["cache_write_input_tokens"])
	}
	result.Reasoning = coerceInt(usage["reasoning_tokens"])

	inputDetails := usageMapValue(usage, "prompt_tokens_details")
	if inputDetails == nil {
		inputDetails = usageMapValue(usage, "input_tokens_details")
	}
	if inputDetails != nil {
		if cached := coerceInt(inputDetails["cached_tokens"]); cached != nil {
			result.Cached = cached
		}
		result.Details = addUsageDetail(result.Details, "input_audio_tokens", inputDetails["audio_tokens"])
	}
	outputDetails := usageMapValue(usage, "completion_tokens_details")
	if outputDetails == nil {
		outputDetails = usageMapValue(usage, "output_tokens_details")
	}
	if outputDetails != nil {
		if reasoning := coerceInt(outputDetails["reasoning_tokens"]); reasoning != nil {
			result.Reasoning = reasoning
		}
		result.Details = addUsageDetail(result.Details, "output_audio_tokens", outputDetails["audio_tokens"])
		result.Details = addUsageDetail(result.Details, "accepted_prediction_tokens", outputDetails["accepted_prediction_tokens"])
		result.Details = addUsageDetail(result.Details, "rejected_prediction_tokens", outputDetails["rejected_prediction_tokens"])
	}
	return result
}

func (usage normalizedUsage) apply(meta types.ResponseMeta) types.ResponseMeta {
	meta.InputTokens = usage.Input
	meta.OutputTokens = usage.Output
	meta.TotalTokens = usage.Total
	meta.CachedInputTokens = usage.Cached
	meta.CacheWriteTokens = usage.CacheWrite
	meta.ReasoningTokens = usage.Reasoning
	meta.UsageDetails = usage.Details
	semantics := meta.UsageSemantics
	if semantics == "" {
		semantics = types.UsageSemanticsGeneric
	}
	return types.NormalizeResponseMetaUsageForSemantics(meta, semantics)
}

type responsesSSEPayload struct {
	Type        *string `json:"type,omitempty"`
	Delta       any     `json:"delta,omitempty"`
	OutputIndex *int    `json:"output_index,omitempty"`
	Item        any     `json:"item,omitempty"`
	Response    any     `json:"response,omitempty"`
	Error       any     `json:"error,omitempty"`
	ID          *string `json:"id,omitempty"`
}

type responsesProviderToolState struct {
	ID          string
	Name        string
	BlockType   string
	Args        map[string]any
	Status      string
	Action      map[string]any
	Results     []map[string]any
	Outputs     []any
	Files       []any
	ContainerID string
	Message     string
}

type streamRequestAttempt struct {
	Resp       *http.Response
	StatusCode int
	Message    string
	Err        error
	Transport  bool
	Dispatched bool
}

func (p *Provider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	out := make(chan types.StreamEvent, 64)

	profile := p.profile
	model := req.Model
	if strings.TrimSpace(model) == "" {
		model = p.model
	}

	apiType := string(profile.APIType)
	metaBase := types.ResponseMeta{ProviderID: p.providerID, Model: model, ProfileID: profile.ProfileID, APIType: &apiType, UsageSemantics: profile.UsageSemantics}

	urlStr, err := buildEndpointURL(profile, model)
	if err != nil {
		return nil, err
	}

	go func() {
		defer close(out)

		nameMapper := toolnamemapper.New(0)
		// Seed mapping from tool definitions first.
		for _, td := range req.Tools {
			_ = nameMapper.ProviderName(td.Function.Name)
		}

		headers := map[string]string{"Content-Type": "application/json", "Accept-Encoding": "identity"}
		for k, v := range profile.ExtraHeaders {
			if strings.TrimSpace(k) == "" {
				continue
			}
			headers[k] = v
		}

		timeoutMs := 0
		if profile.TimeoutMs != nil {
			timeoutMs = *profile.TimeoutMs
		}
		reqCtx := ctx
		var cancel context.CancelFunc
		if timeoutMs > 0 {
			reqCtx, cancel = context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
			defer cancel()
		}

		activeReq := req
		upstreamRequestCount := 0
		attempt := p.performStreamRequest(reqCtx, urlStr, headers, profile, model, activeReq, nameMapper)
		if attempt.Dispatched {
			upstreamRequestCount++
		}
		if shouldRetryChatCompletionsWithoutStreamUsage(profile, attempt) {
			profile = profileWithoutStreamUsage(profile)
			attempt = p.performStreamRequest(reqCtx, urlStr, headers, profile, model, activeReq, nameMapper)
			if attempt.Dispatched {
				upstreamRequestCount++
			}
		}
		if shouldRetryResponsesWithoutPreviousResponseID(profile, activeReq, attempt) {
			activeReq = requestWithoutPreviousResponseID(activeReq)
			attempt = p.performStreamRequest(reqCtx, urlStr, headers, profile, model, activeReq, nameMapper)
			if attempt.Dispatched {
				upstreamRequestCount++
			}
		}
		if shouldRetryResponsesWithoutNativeFileInput(profile, activeReq, attempt) {
			if activeReq.RuntimePlan == nil || activeReq.RuntimePlan.AllowReducedFileFallback {
				if activeReq.RuntimePlan != nil {
					feature := "documents"
					send(reqCtx, out, types.StreamEvent{Event: types.EventPlanUpdate, Data: types.PlanUpdateData{
						PlanID: activeReq.RuntimePlan.PlanID,
						Change: types.PlanUpdateChange{
							Kind: "fidelity_reduction", Feature: &feature,
							Text: "The provider rejected native file input, so the authorized extracted-text fallback was used.",
						},
					}})
				}
				fallbackProfile := profile
				fallbackProfile.FileInputMode = FileInputSidecarOnly
				attempt = p.performStreamRequest(reqCtx, urlStr, headers, fallbackProfile, model, requestWithFileAttachmentFallbackText(activeReq), nameMapper)
				if attempt.Dispatched {
					upstreamRequestCount++
				}
			} else {
				send(reqCtx, out, types.StreamEvent{Event: types.EventError, Data: types.ErrorData{
					Code:      "UNDECLARED_FIDELITY_REDUCTION",
					Message:   "The provider rejected native file input, and the finalized run plan did not authorize an extracted-text fallback.",
					Retryable: false,
					Meta:      &metaBase,
				}})
				return
			}
		}
		if upstreamRequestCount > 0 {
			metaBase.UpstreamRequestCount = &upstreamRequestCount
		}
		if attempt.Resp == nil {
			sendStreamRequestError(reqCtx, out, attempt, metaBase)
			return
		}
		resp := attempt.Resp

		if profile.APIType == APIResponses {
			previousResponseID := currentPreviousResponseID(activeReq)
			missingPreviousResponse := streamResponsesWithMissingPreviousResponseDetection(
				reqCtx,
				out,
				resp,
				model,
				metaBase,
				activeReq,
				nameMapper,
				previousResponseID,
			)
			_ = resp.Body.Close()
			if !missingPreviousResponse {
				return
			}

			activeReq = requestWithoutPreviousResponseID(activeReq)
			attempt = p.performStreamRequest(reqCtx, urlStr, headers, profile, model, activeReq, nameMapper)
			if attempt.Dispatched {
				upstreamRequestCount++
				metaBase.UpstreamRequestCount = &upstreamRequestCount
			}
			if attempt.Resp == nil {
				sendStreamRequestError(reqCtx, out, attempt, metaBase)
				return
			}
			resp = attempt.Resp
			defer resp.Body.Close()
			streamResponses(reqCtx, out, resp, model, metaBase, activeReq, nameMapper)
			return
		}
		defer resp.Body.Close()
		streamChatCompletions(reqCtx, out, resp, model, metaBase, activeReq, nameMapper)
	}()

	return out, nil
}

func (p *Provider) performStreamRequest(
	ctx context.Context,
	urlStr string,
	headers map[string]string,
	profile Profile,
	model string,
	req types.ChatRequest,
	mapper *toolnamemapper.Mapper,
) streamRequestAttempt {
	bodyBytes, err := buildBody(profile, model, req, mapper, urlStr)
	if err != nil {
		return streamRequestAttempt{Err: err}
	}

	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return streamRequestAttempt{Err: err}
	}
	for k, v := range headers {
		hreq.Header.Set(k, v)
	}

	if err := types.NotifyUsageDispatch(req, string(profile.APIType)); err != nil {
		return streamRequestAttempt{Err: err}
	}
	resp, err := p.httpc.Do(hreq)
	if err != nil {
		return streamRequestAttempt{Err: err, Transport: true, Dispatched: true}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		msg := strings.TrimSpace(string(b))
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return streamRequestAttempt{StatusCode: resp.StatusCode, Message: msg, Dispatched: true}
	}

	return streamRequestAttempt{Resp: resp, Dispatched: true}
}

func sendStreamRequestError(ctx context.Context, out chan<- types.StreamEvent, attempt streamRequestAttempt, metaBase types.ResponseMeta) {
	if attempt.StatusCode != 0 {
		code, retryable := requestguard.NormalizeProviderError(attempt.StatusCode, attempt.Message)
		send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: attempt.Message, Retryable: retryable, Meta: &metaBase}})
		return
	}
	msg := ""
	if attempt.Err != nil {
		msg = attempt.Err.Error()
	}
	if strings.TrimSpace(msg) == "" {
		msg = "Provider request failed."
	}
	code := requestguard.ErrorCodeFor(attempt.Err)
	retryable := false
	if attempt.Transport {
		code = "PROVIDER_ERROR"
		retryable = true
		if errors.Is(attempt.Err, context.Canceled) || errors.Is(attempt.Err, context.DeadlineExceeded) || errorsIsContext(attempt.Err) {
			code = "ABORTED"
			retryable = false
		}
	}
	send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: msg, Retryable: retryable, Meta: &metaBase}})
}

func shouldRetryResponsesWithoutPreviousResponseID(profile Profile, req types.ChatRequest, attempt streamRequestAttempt) bool {
	if profile.APIType != APIResponses || attempt.StatusCode == 0 {
		return false
	}
	previousResponseID := currentPreviousResponseID(req)
	if previousResponseID == "" {
		return false
	}
	return isPreviousResponseNotFoundError(attempt.Message, previousResponseID)
}

func includeStreamUsage(profile Profile) bool {
	if profile.IncludeStreamUsage != nil {
		return *profile.IncludeStreamUsage
	}
	return profile.UsageSemantics == types.UsageSemanticsOpenAI
}

func profileWithoutStreamUsage(profile Profile) Profile {
	disabled := false
	profile.IncludeStreamUsage = &disabled
	if len(profile.PayloadOverrides) == 0 {
		return profile
	}
	overrides := make(map[string]any, len(profile.PayloadOverrides))
	for key, value := range profile.PayloadOverrides {
		overrides[key] = value
	}
	if raw, ok := overrides["stream_options"].(map[string]any); ok {
		streamOptions := make(map[string]any, len(raw))
		for key, value := range raw {
			if key != "include_usage" {
				streamOptions[key] = value
			}
		}
		if len(streamOptions) == 0 {
			delete(overrides, "stream_options")
		} else {
			overrides["stream_options"] = streamOptions
		}
	}
	profile.PayloadOverrides = overrides
	return profile
}

func shouldRetryChatCompletionsWithoutStreamUsage(profile Profile, attempt streamRequestAttempt) bool {
	if profile.APIType != APIChatCompletions || !includeStreamUsage(profile) || attempt.StatusCode == 0 {
		return false
	}
	if attempt.StatusCode != http.StatusBadRequest && attempt.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	message := strings.ToLower(attempt.Message)
	if !strings.Contains(message, "stream_options") && !strings.Contains(message, "include_usage") && !strings.Contains(message, "include usage") {
		return false
	}
	for _, marker := range []string{"unsupported", "unknown", "unrecognized", "not allowed", "not supported", "unexpected", "extra_forbidden", "invalid parameter", "invalid_request"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func shouldRetryResponsesWithoutNativeFileInput(profile Profile, req types.ChatRequest, attempt streamRequestAttempt) bool {
	if attempt.StatusCode == 0 {
		return false
	}
	if normalizeFileInputMode(profile.FileInputMode) == FileInputSidecarOnly {
		return false
	}
	if !responsesRequestHasNativeFileInput(req, profile.FileInputMode) {
		return false
	}
	return isResponsesNativeFileInputError(attempt.Message)
}

func responsesRequestHasNativeFileInput(req types.ChatRequest, fileInputMode FileInputMode) bool {
	if normalizeFileInputMode(fileInputMode) == FileInputSidecarOnly {
		return false
	}
	for _, msg := range req.Messages {
		if len(responsesInputFileContent(msg.FileAttachments, fileInputMode)) > 0 {
			return true
		}
	}
	return false
}

func isResponsesNativeFileInputError(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "empty file") || strings.Contains(msg, "file is empty") {
		return true
	}
	if strings.Contains(msg, "file_data") || strings.Contains(msg, "input_file") {
		return true
	}
	if strings.Contains(msg, "file") && (strings.Contains(msg, "unsupported") ||
		strings.Contains(msg, "not supported") ||
		strings.Contains(msg, "invalid") ||
		strings.Contains(msg, "parse") ||
		strings.Contains(msg, "unknown") ||
		strings.Contains(msg, "unrecognized") ||
		strings.Contains(msg, "content type") ||
		strings.Contains(msg, "content block") ||
		strings.Contains(msg, "content part")) {
		return true
	}
	return false
}

func currentPreviousResponseID(req types.ChatRequest) string {
	if raw, ok := req.ProviderState["previous_response_id"].(string); ok {
		return strings.TrimSpace(raw)
	}
	return ""
}

func requestWithoutPreviousResponseID(req types.ChatRequest) types.ChatRequest {
	if len(req.ProviderState) == 0 {
		return req
	}
	req.ProviderState = openAIDeepCopyMap(req.ProviderState)
	delete(req.ProviderState, "previous_response_id")
	if len(req.ProviderState) == 0 {
		req.ProviderState = nil
	}
	return req
}

func requestWithFileAttachmentFallbackText(req types.ChatRequest) types.ChatRequest {
	if len(req.Messages) == 0 {
		return req
	}
	req.Messages = append([]types.ChatMessage(nil), req.Messages...)
	for i := range req.Messages {
		sidecar := openAIFileAttachmentFallbackText(req.Messages[i].FileAttachments)
		if sidecar == "" {
			continue
		}
		content := ""
		if req.Messages[i].Content != nil {
			content = strings.TrimSpace(*req.Messages[i].Content)
		}
		if strings.Contains(content, sidecar) {
			continue
		}
		combined := strings.TrimSpace(content)
		if combined != "" {
			combined += "\n\n"
		}
		combined += sidecar
		req.Messages[i].Content = &combined
	}
	return req
}

func openAIFileAttachmentFallbackText(attachments []types.FileAttachment) string {
	files := types.CloneFileAttachments(attachments)
	if len(files) == 0 {
		return ""
	}
	lines := []string{
		"Uploaded file fallback content:",
		"- Native file input was unavailable or rejected by the provider for this retry.",
		"- Use the extracted preview below only as a fallback; use attachment tools if more detail is needed.",
	}
	remaining := 72000
	for i, attachment := range files {
		details := []string{
			fmt.Sprintf("[%d] artifact_id=%s", i+1, strings.TrimSpace(attachment.ArtifactID)),
			"file_name=" + strings.TrimSpace(attachment.FileName),
		}
		if mimeType := strings.TrimSpace(attachment.MimeType); mimeType != "" {
			details = append(details, "mime="+mimeType)
		}
		lines = append(lines, "- "+strings.Join(details, ", "))
		preview := strings.TrimSpace(attachment.PreviewText)
		if preview == "" {
			if note := strings.TrimSpace(attachment.ContentNote); note != "" {
				lines = append(lines, "  Note: "+note)
			}
			continue
		}
		if remaining <= 0 {
			lines = append(lines, "  [Preview omitted; fallback preview budget exhausted.]")
			continue
		}
		limit := 24000
		if remaining < limit {
			limit = remaining
		}
		truncated, didTruncate := truncateRunesOpenAICompat(preview, limit)
		lines = append(lines, "  Extracted preview:")
		lines = append(lines, indentLinesOpenAICompat(truncated, "    "))
		if didTruncate || (attachment.PreviewTruncated != nil && *attachment.PreviewTruncated) {
			lines = append(lines, "    [Preview truncated; use attachment tools for more.]")
		}
		remaining -= len([]rune(truncated))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func truncateRunesOpenAICompat(value string, limit int) (string, bool) {
	value = strings.TrimSpace(value)
	if limit <= 0 || value == "" {
		return "", value != ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return strings.TrimSpace(string(runes[:limit])), true
}

func indentLinesOpenAICompat(value string, prefix string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return prefix
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

func isPreviousResponseNotFoundError(message string, previousResponseID string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	phrases := strings.NewReplacer("_", " ", "-", " ").Replace(msg)
	if !strings.Contains(phrases, "not found") &&
		!strings.Contains(phrases, "does not exist") &&
		!strings.Contains(phrases, "could not be found") &&
		!strings.Contains(phrases, "no response found") {
		return false
	}
	if strings.Contains(phrases, "previous response id") || strings.Contains(phrases, "previous response") {
		return true
	}

	// Some Responses-compatible hosts return less specific errors after a
	// request is routed to a worker that does not have the referenced response.
	// Match those variants only when they identify the continuation response,
	// so an unrelated missing model, file, or tool does not trigger a replay.
	responseID := strings.ToLower(strings.TrimSpace(previousResponseID))
	if responseID != "" && strings.Contains(msg, responseID) {
		return true
	}
	return strings.Contains(phrases, "response id") ||
		strings.Contains(phrases, "response not found") ||
		strings.Contains(phrases, "no response found")
}

// streamResponsesWithMissingPreviousResponseDetection preserves normal token
// streaming while allowing one safe replay when a Responses-compatible host
// reports the missing continuation in its first SSE event. Once any event has
// been exposed to the caller, replay is disabled to avoid duplicate output or
// tool progress.
func streamResponsesWithMissingPreviousResponseDetection(
	ctx context.Context,
	out chan<- types.StreamEvent,
	resp *http.Response,
	model string,
	metaBase types.ResponseMeta,
	request types.ChatRequest,
	mapper *toolnamemapper.Mapper,
	previousResponseID string,
) bool {
	attemptEvents := make(chan types.StreamEvent, 64)
	go func() {
		defer close(attemptEvents)
		streamResponses(ctx, attemptEvents, resp, model, metaBase, request, mapper)
	}()

	first, ok := <-attemptEvents
	if !ok {
		return false
	}
	if isMissingPreviousResponseStreamEvent(first, previousResponseID) {
		return true
	}

	send(ctx, out, first)
	for event := range attemptEvents {
		send(ctx, out, event)
	}
	return false
}

func isMissingPreviousResponseStreamEvent(event types.StreamEvent, previousResponseID string) bool {
	if strings.TrimSpace(previousResponseID) == "" || event.Event != types.EventError {
		return false
	}
	switch data := event.Data.(type) {
	case types.ErrorData:
		return isPreviousResponseNotFoundError(data.Message, previousResponseID)
	case *types.ErrorData:
		return data != nil && isPreviousResponseNotFoundError(data.Message, previousResponseID)
	default:
		return false
	}
}

func send(ctx context.Context, ch chan<- types.StreamEvent, ev types.StreamEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

func buildBody(profile Profile, model string, req types.ChatRequest, mapper *toolnamemapper.Mapper, endpoint ...string) ([]byte, error) {
	requestEndpoint := ""
	if len(endpoint) > 0 {
		requestEndpoint = endpoint[0]
	}
	if profile.APIType == APIResponses {
		tools := toolsToResponses(req.Tools, mapper, profile.WebSearch, profile.CodeExecution)
		items, previousResponseID := responseInputItems(req, mapper, profile.ProviderID, profile.FileInputMode)
		body := map[string]any{
			"stream": true,
			"input":  "",
		}
		if len(items) > 0 {
			body["input"] = items
		}
		if previousResponseID != "" {
			body["previous_response_id"] = previousResponseID
		}
		if profile.ModelInBody {
			body["model"] = model
		}
		if strings.TrimSpace(req.SystemPrompt) != "" {
			body["instructions"] = strings.TrimSpace(req.SystemPrompt)
		}
		if profile.ReasoningEffort != nil {
			eff := strings.TrimSpace(*profile.ReasoningEffort)
			if eff != "" {
				body["reasoning"] = map[string]any{"effort": eff}
			}
		}
		if len(tools) > 0 {
			body["tools"] = tools
			body["tool_choice"] = "auto"
		}
		if contextManagement := responsesContextManagement(profile); len(contextManagement) > 0 {
			body["context_management"] = contextManagement
		}
		for k, v := range profile.PayloadOverrides {
			body[k] = v
		}
		return requestguard.MarshalJSON(body, requestguard.Context{
			ProviderID:  profile.ProviderID,
			Model:       model,
			RequestKind: string(APIResponses),
			Endpoint:    requestEndpoint,
			MaxBytes:    profile.MaxRequestBytes,
		})
	}

	messages := messagesToChatCompletionsWithFileInputMode(req, mapper, profile.FileInputMode)
	openaiTools := toolsToChatCompletions(req.Tools, mapper)
	body := map[string]any{
		"stream":   true,
		"messages": messages,
	}
	if profile.ModelInBody {
		body["model"] = model
	}
	if profile.ReasoningEffort != nil {
		eff := strings.TrimSpace(*profile.ReasoningEffort)
		if eff != "" {
			body["reasoning_effort"] = eff
		}
	}
	if len(openaiTools) > 0 {
		body["tools"] = openaiTools
		body["tool_choice"] = "auto"
	}
	for k, v := range profile.PayloadOverrides {
		body[k] = v
	}
	normalizeChatCompletionsStreamUsage(body, includeStreamUsage(profile))
	return requestguard.MarshalJSON(body, requestguard.Context{
		ProviderID:  profile.ProviderID,
		Model:       model,
		RequestKind: string(APIChatCompletions),
		Endpoint:    requestEndpoint,
		MaxBytes:    profile.MaxRequestBytes,
	})
}

func normalizeChatCompletionsStreamUsage(body map[string]any, enabled bool) {
	raw, _ := body["stream_options"].(map[string]any)
	streamOptions := make(map[string]any, len(raw)+1)
	for key, value := range raw {
		if key != "include_usage" {
			streamOptions[key] = value
		}
	}
	if enabled {
		streamOptions["include_usage"] = true
	}
	if len(streamOptions) == 0 {
		delete(body, "stream_options")
		return
	}
	body["stream_options"] = streamOptions
}

func toolsToChatCompletions(tools []types.ToolDefinition, mapper *toolnamemapper.Mapper) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        mapper.ProviderName(t.Function.Name),
				"description": t.Function.Description,
				"parameters":  toolschema.NormalizeFunctionParameters(t.Function.Parameters),
			},
		})
	}
	return out
}

func toolsToResponses(
	tools []types.ToolDefinition,
	mapper *toolnamemapper.Mapper,
	webSearch *WebSearchConfig,
	codeExecution *CodeExecutionConfig,
) []any {
	out := make([]any, 0, len(tools)+2)
	for _, t := range tools {
		out = append(out, map[string]any{
			"type":        "function",
			"name":        mapper.ProviderName(t.Function.Name),
			"description": t.Function.Description,
			"parameters":  toolschema.NormalizeFunctionParameters(t.Function.Parameters),
		})
	}
	if webSearch != nil && webSearch.Enabled {
		toolType := strings.TrimSpace(webSearch.ToolType)
		if toolType == "" {
			toolType = "web_search"
		}
		tool := map[string]any{
			"type": toolType,
		}
		if size := strings.TrimSpace(webSearch.SearchContextSize); size != "" {
			tool["search_context_size"] = size
		}
		if len(webSearch.AllowedDomains) > 0 || len(webSearch.BlockedDomains) > 0 {
			filters := map[string]any{}
			if len(webSearch.AllowedDomains) > 0 {
				filters["allowed_domains"] = append([]string(nil), webSearch.AllowedDomains...)
			}
			if len(webSearch.BlockedDomains) > 0 {
				filters["blocked_domains"] = append([]string(nil), webSearch.BlockedDomains...)
			}
			if len(filters) > 0 {
				tool["filters"] = filters
			}
		}
		out = append(out, tool)
	}
	if codeExecution != nil && codeExecution.Enabled {
		toolType := strings.TrimSpace(codeExecution.ToolType)
		if toolType == "" {
			toolType = "code_interpreter"
		}
		tool := map[string]any{
			"type": toolType,
		}
		if containerID := strings.TrimSpace(codeExecution.ContainerID); containerID != "" {
			tool["container"] = containerID
		} else {
			containerMode := strings.TrimSpace(codeExecution.ContainerMode)
			if containerMode == "" {
				containerMode = "auto"
			}
			container := map[string]any{"type": containerMode}
			if memoryLimit := strings.TrimSpace(codeExecution.MemoryLimit); memoryLimit != "" {
				container["memory_limit"] = memoryLimit
			}
			tool["container"] = container
		}
		out = append(out, tool)
	}
	return out
}

func responsesContextManagement(profile Profile) []any {
	if !profile.CompactionEnabled {
		return nil
	}
	threshold := profile.CompactionThresholdTokens
	if threshold <= 0 {
		threshold = defaultCompactionThresholdTokens
	}
	return []any{
		map[string]any{
			"type":              "compaction",
			"compact_threshold": threshold,
		},
	}
}

func responseInputItems(req types.ChatRequest, mapper *toolnamemapper.Mapper, providerID string, fileInputMode FileInputMode) ([]any, string) {
	validToolCallIDs := collectValidToolCallIDs(req.Messages)
	previousResponseID := ""
	if raw, ok := req.ProviderState["previous_response_id"].(string); ok {
		previousResponseID = strings.TrimSpace(raw)
	}
	if previousResponseID == "" {
		return messagesToResponsesWithValid(req.Messages, mapper, validToolCallIDs, fileInputMode), ""
	}
	anchorIndex := findResponsesAnchorIndex(req.Messages, previousResponseID, providerID)
	if anchorIndex < 0 {
		return messagesToResponsesWithValid(req.Messages, mapper, validToolCallIDs, fileInputMode), ""
	}
	return messagesToResponsesWithValid(req.Messages[anchorIndex+1:], mapper, validToolCallIDs, fileInputMode), previousResponseID
}

func collectValidToolCallIDs(messages []types.ChatMessage) map[string]struct{} {
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

func filterToolCallsByValidIDs(calls []types.ToolCall, valid map[string]struct{}) []types.ToolCall {
	if len(calls) == 0 || len(valid) == 0 {
		return nil
	}
	out := make([]types.ToolCall, 0, len(calls))
	for _, call := range calls {
		if call.ProviderExecuted {
			continue
		}
		if _, ok := valid[strings.TrimSpace(call.ID)]; !ok {
			continue
		}
		out = append(out, call)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func messagesToChatCompletions(req types.ChatRequest, mapper *toolnamemapper.Mapper) []any {
	return messagesToChatCompletionsWithFileInputMode(req, mapper, FileInputNativeSupported)
}

func messagesToChatCompletionsWithFileInputMode(req types.ChatRequest, mapper *toolnamemapper.Mapper, fileInputMode FileInputMode) []any {
	validToolCallIDs := collectValidToolCallIDs(req.Messages)
	out := make([]any, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		out = append(out, map[string]any{"role": "system", "content": strings.TrimSpace(req.SystemPrompt)})
	}
	for _, msg := range req.Messages {
		switch msg.Role {
		case types.RoleSystem:
			continue
		case types.RoleUser:
			content := ""
			if msg.Content != nil {
				content = types.SafeTruncate(*msg.Content)
			}
			images := chatCompletionsImageContent(msg.ImageAttachments, msg.FileAttachments, fileInputMode)
			files := chatCompletionsFileContent(msg.FileAttachments, fileInputMode)
			if len(images) == 0 && len(files) == 0 {
				out = append(out, map[string]any{"role": "user", "content": content})
				break
			}
			parts := make([]any, 0, len(images)+len(files)+1)
			if strings.TrimSpace(content) != "" {
				parts = append(parts, map[string]any{"type": "text", "text": content})
			}
			parts = append(parts, images...)
			parts = append(parts, files...)
			if len(parts) == 0 {
				parts = append(parts, map[string]any{"type": "text", "text": ""})
			}
			out = append(out, map[string]any{"role": "user", "content": parts})
		case types.RoleAssistant:
			content := ""
			if msg.Content != nil && strings.TrimSpace(*msg.Content) != "" {
				content = types.SafeTruncate(*msg.Content)
			}
			validToolCalls := filterToolCallsByValidIDs(msg.ToolCalls, validToolCallIDs)
			if content == "" && len(validToolCalls) == 0 {
				continue
			}
			payload := map[string]any{"role": "assistant", "content": content}
			if len(validToolCalls) > 0 {
				calls := make([]any, 0, len(validToolCalls))
				for _, call := range validToolCalls {
					argsText := call.ArgsText
					if strings.TrimSpace(argsText) == "" {
						b, _ := json.Marshal(call.Args)
						argsText = string(b)
					}
					if strings.TrimSpace(argsText) == "" {
						argsText = "{}"
					}
					calls = append(calls, map[string]any{
						"id":   call.ID,
						"type": "function",
						"function": map[string]any{
							"name":      mapper.ProviderName(call.Name),
							"arguments": argsText,
						},
					})
				}
				payload["tool_calls"] = calls
			}
			out = append(out, payload)
		case types.RoleTool:
			toolCallID := strings.TrimSpace(msg.ToolCallID)
			if toolCallID == "" {
				continue
			}
			if _, ok := validToolCallIDs[toolCallID]; !ok {
				continue
			}
			content := ""
			if msg.Content != nil {
				content = types.SafeTruncate(*msg.Content)
			}
			out = append(out, map[string]any{
				"role":         "tool",
				"tool_call_id": toolCallID,
				"content":      content,
			})
			if attachmentContent := chatCompletionsToolAttachmentContent(msg, fileInputMode); len(attachmentContent) > 0 {
				out = append(out, map[string]any{
					"role":    "user",
					"content": attachmentContent,
				})
			}
		}
	}
	return out
}

func messagesToResponsesWithValid(messages []types.ChatMessage, mapper *toolnamemapper.Mapper, validToolCallIDs map[string]struct{}, fileInputMode FileInputMode) []any {
	items := make([]any, 0, len(messages))
	for _, msg := range messages {
		if msg.Role == types.RoleSystem {
			continue
		}
		if msg.Role == types.RoleUser || msg.Role == types.RoleAssistant {
			role := "assistant"
			if msg.Role == types.RoleUser {
				role = "user"
			}

			if msg.Role == types.RoleUser {
				text := ""
				if msg.Content != nil && *msg.Content != "" {
					text = types.SafeTruncate(*msg.Content)
				}
				if content := responsesUserInputContent(msg, text, fileInputMode); len(content) > 0 {
					items = append(items, map[string]any{"role": role, "content": content})
				} else if strings.TrimSpace(text) != "" {
					items = append(items, map[string]any{"role": role, "content": text})
				}
			} else if msg.Content != nil && *msg.Content != "" {
				items = append(items, map[string]any{"role": role, "content": types.SafeTruncate(*msg.Content)})
			}
			if msg.Role == types.RoleAssistant && len(msg.ToolCalls) > 0 {
				for _, call := range filterToolCallsByValidIDs(msg.ToolCalls, validToolCallIDs) {
					argsText := call.ArgsText
					if strings.TrimSpace(argsText) == "" {
						b, _ := json.Marshal(call.Args)
						argsText = string(b)
					}
					items = append(items, map[string]any{
						"type":      "function_call",
						"call_id":   call.ID,
						"name":      mapper.ProviderName(call.Name),
						"arguments": argsText,
					})
				}
			}
			continue
		}
		if msg.Role == types.RoleTool {
			toolCallID := strings.TrimSpace(msg.ToolCallID)
			if toolCallID == "" {
				continue
			}
			if _, ok := validToolCallIDs[toolCallID]; !ok {
				continue
			}
			outText := ""
			if msg.Content != nil {
				outText = types.SafeTruncate(*msg.Content)
			}
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": toolCallID,
				"output":  outText,
			})
			if attachmentContent := responsesToolAttachmentInputContent(msg, fileInputMode); len(attachmentContent) > 0 {
				items = append(items, map[string]any{
					"role":    "user",
					"content": attachmentContent,
				})
			}
		}
	}
	return items
}

func responsesUserInputContent(msg types.ChatMessage, text string, fileInputMode FileInputMode) []any {
	images := responsesImageContent(msg.ImageAttachments, msg.FileAttachments, fileInputMode)
	files := responsesInputFileContent(msg.FileAttachments, fileInputMode)
	if len(images) == 0 && len(files) == 0 {
		return nil
	}
	content := make([]any, 0, len(images)+len(files)+1)
	if strings.TrimSpace(text) != "" {
		content = append(content, map[string]any{"type": "input_text", "text": text})
	}
	content = append(content, images...)
	content = append(content, files...)
	return content
}

func responsesImageContent(imageAttachments []types.ImageAttachment, fileAttachments []types.FileAttachment, fileInputMode FileInputMode) []any {
	images := vision.NormalizeAttachments(imageAttachments)
	fileImages := openAIInputImagePayloads(fileAttachments, fileInputMode)
	if len(images) == 0 && len(fileImages) == 0 {
		return nil
	}
	out := make([]any, 0, len(images)+len(fileImages))
	for _, img := range images {
		out = append(out, map[string]any{
			"type":      "input_image",
			"image_url": img.DataURI,
		})
	}
	for _, img := range fileImages {
		out = append(out, map[string]any{
			"type":      "input_image",
			"image_url": img.dataURI,
		})
	}
	return out
}

func chatCompletionsImageContent(imageAttachments []types.ImageAttachment, fileAttachments []types.FileAttachment, fileInputMode FileInputMode) []any {
	images := vision.NormalizeAttachments(imageAttachments)
	fileImages := openAIInputImagePayloads(fileAttachments, fileInputMode)
	if len(images) == 0 && len(fileImages) == 0 {
		return nil
	}
	out := make([]any, 0, len(images)+len(fileImages))
	for _, img := range images {
		out = append(out, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": img.DataURI,
			},
		})
	}
	for _, img := range fileImages {
		out = append(out, map[string]any{
			"type": "image_url",
			"image_url": map[string]any{
				"url": img.dataURI,
			},
		})
	}
	return out
}

func responsesInputFileContent(attachments []types.FileAttachment, fileInputMode FileInputMode) []any {
	files := openAIInputFilePayloads(attachments, fileInputMode)
	if len(files) == 0 {
		return nil
	}
	out := make([]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"type":      "input_file",
			"filename":  file.filename,
			"file_data": file.fileData,
		})
	}
	return out
}

func chatCompletionsFileContent(attachments []types.FileAttachment, fileInputMode FileInputMode) []any {
	files := openAIInputFilePayloads(attachments, fileInputMode)
	if len(files) == 0 {
		return nil
	}
	out := make([]any, 0, len(files))
	for _, file := range files {
		out = append(out, map[string]any{
			"type": "file",
			"file": map[string]any{
				"filename":  file.filename,
				"file_data": file.fileData,
			},
		})
	}
	return out
}

type openAIInputFilePayload struct {
	filename string
	fileData string
}

type openAIInputImagePayload struct {
	dataURI string
}

func openAIInputFilePayloads(attachments []types.FileAttachment, fileInputMode FileInputMode) []openAIInputFilePayload {
	if normalizeFileInputMode(fileInputMode) == FileInputSidecarOnly {
		return nil
	}
	files := types.CloneFileAttachments(attachments)
	if len(files) == 0 {
		return nil
	}
	out := make([]openAIInputFilePayload, 0, len(files))
	for _, file := range files {
		fileData := strings.TrimSpace(file.InlineDataBase64)
		if fileData == "" {
			continue
		}
		if openAIFileAttachmentImageMediaType(file) != "" {
			continue
		}
		if !openAIFileInputEligible(file, fileInputMode) {
			continue
		}
		mimeType := openAIFileInputMIME(file)
		fileName := strings.TrimSpace(file.FileName)
		if fileName == "" {
			fileName = responsesFallbackFileName(mimeType)
		}
		out = append(out, openAIInputFilePayload{
			filename: fileName,
			fileData: responsesInputFileData(fileData, mimeType, fileName),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func openAIInputImagePayloads(attachments []types.FileAttachment, fileInputMode FileInputMode) []openAIInputImagePayload {
	if normalizeFileInputMode(fileInputMode) == FileInputSidecarOnly {
		return nil
	}
	files := types.CloneFileAttachments(attachments)
	if len(files) == 0 {
		return nil
	}
	out := make([]openAIInputImagePayload, 0, len(files))
	for _, file := range files {
		fileData := strings.TrimSpace(file.InlineDataBase64)
		if fileData == "" {
			continue
		}
		mediaType := openAIFileAttachmentImageMediaType(file)
		if mediaType == "" {
			continue
		}
		out = append(out, openAIInputImagePayload{
			dataURI: responsesInputFileData(fileData, mediaType, strings.TrimSpace(file.FileName)),
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func responsesInputFileData(fileData string, mimeType string, fileName string) string {
	fileData = strings.TrimSpace(fileData)
	if fileData == "" || strings.HasPrefix(strings.ToLower(fileData), "data:") {
		return fileData
	}
	mimeType = responsesInputFileDataMIME(mimeType, fileName)
	return "data:" + mimeType + ";base64," + fileData
}

func responsesInputFileDataMIME(mimeType string, fileName string) string {
	if cleaned := strings.TrimSpace(strings.ToLower(mimeType)); cleaned != "" {
		return cleaned
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(fileName))), ".") {
	case "pdf":
		return "application/pdf"
	case "txt":
		return "text/plain"
	case "md", "markdown":
		return "text/markdown"
	case "html", "htm":
		return "text/html"
	case "xml":
		return "application/xml"
	case "json":
		return "application/json"
	case "jsonl", "ndjson":
		return "application/x-ndjson"
	case "csv":
		return "text/csv"
	case "tsv":
		return "text/tab-separated-values"
	case "iif":
		return "text/x-iif"
	case "rtf":
		return "application/rtf"
	case "odt":
		return "application/vnd.oasis.opendocument.text"
	case "doc":
		return "application/msword"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "ppt":
		return "application/vnd.ms-powerpoint"
	case "pptx":
		return "application/vnd.openxmlformats-officedocument.presentationml.presentation"
	case "key":
		return "application/vnd.apple.keynote"
	case "xls":
		return "application/vnd.ms-excel"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "yaml", "yml":
		return "application/yaml"
	case "toml":
		return "application/toml"
	case "js", "mjs":
		return "application/javascript"
	case "ts":
		return "application/typescript"
	case "css":
		return "text/css"
	case "sql":
		return "application/x-sql"
	case "eml":
		return "message/rfc822"
	case "ics", "ifb":
		return "text/calendar"
	default:
		if openAITextCodeExtension(fileName) {
			return "text/plain"
		}
		return "application/octet-stream"
	}
}

func normalizeFileInputMode(mode FileInputMode) FileInputMode {
	switch mode {
	case FileInputSidecarOnly, FileInputNativePDFOnly, FileInputNativeSupported:
		return mode
	default:
		return FileInputNativeSupported
	}
}

func openAIFileInputEligible(file types.FileAttachment, mode FileInputMode) bool {
	mode = normalizeFileInputMode(mode)
	if mode == FileInputSidecarOnly {
		return false
	}
	if openAIFileAttachmentImageMediaType(file) != "" {
		return false
	}
	if openAIFileAttachmentIsPDF(file) {
		return true
	}
	if mode == FileInputNativePDFOnly {
		return false
	}
	if mode == FileInputNativeSupported {
		return true
	}
	for _, mimeType := range openAIFileMIMECandidates(file) {
		if openAIFileMIMEEligible(mimeType) {
			return true
		}
	}
	if openAIFileExtensionEligible(file.FileName) {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(file.FileKind)) {
	case "text", "json", "html", "csv", "document", "spreadsheet", "presentation":
		return true
	}
	if file.Capabilities["canUseNativeDocument"] {
		return true
	}
	return false
}

func responsesFileInputEligible(mimeType string, fileName string, mode FileInputMode) bool {
	file := types.FileAttachment{
		ArtifactID:       "eligibility-check",
		FileName:         fileName,
		MimeType:         mimeType,
		InlineDataBase64: "AA==",
	}
	return openAIFileInputEligible(file, mode)
}

func openAIFileAttachmentIsPDF(file types.FileAttachment) bool {
	if strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(file.FileName))), ".") == "pdf" {
		return true
	}
	for _, mimeType := range openAIFileMIMECandidates(file) {
		if mimeType == "application/pdf" {
			return true
		}
	}
	return false
}

func openAIFileAttachmentImageMediaType(file types.FileAttachment) string {
	for _, mimeType := range openAIFileMIMECandidates(file) {
		switch mimeType {
		case "image/png":
			return "image/png"
		case "image/jpeg", "image/jpg":
			return "image/jpeg"
		case "image/gif":
			return "image/gif"
		case "image/webp":
			return "image/webp"
		}
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(file.FileName))), ".") {
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

func openAIFileMIMECandidates(file types.FileAttachment) []string {
	values := []string{
		file.MimeType,
		file.NormalizedMIME,
		file.DetectedMimeType,
		file.DeclaredMIME,
		file.ExtensionMIME,
		file.SniffedMIME,
	}
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func openAIFileInputMIME(file types.FileAttachment) string {
	generic := ""
	for _, mimeType := range openAIFileMIMECandidates(file) {
		if mimeType != "application/octet-stream" {
			return mimeType
		}
		generic = mimeType
	}
	if generic != "" && openAIFileExtensionEligible(file.FileName) {
		return ""
	}
	return strings.TrimSpace(strings.ToLower(file.MimeType))
}

func openAIFileMIMEEligible(mimeType string) bool {
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	if strings.HasPrefix(mimeType, "text/") {
		return true
	}
	switch mimeType {
	case "text/plain",
		"text/markdown",
		"text/html",
		"text/xml",
		"text/tsv",
		"text/x-iif",
		"application/xml",
		"application/json",
		"application/json5",
		"application/x-json5",
		"application/x-ndjson",
		"application/rtf",
		"text/rtf",
		"application/msword",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.oasis.opendocument.text",
		"application/vnd.apple.pages",
		"application/vnd.google-apps.document",
		"application/vnd.apple.iwork",
		"application/vnd.ms-powerpoint",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/vnd.apple.keynote",
		"application/vnd.google-apps.presentation",
		"application/vnd.ms-excel",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.google-apps.spreadsheet",
		"text/csv",
		"text/tab-separated-values",
		"application/csv",
		"application/x-iif",
		"application/javascript",
		"application/typescript",
		"application/x-sql",
		"application/x-rust",
		"application/x-powershell",
		"application/x-patch",
		"application/x-php",
		"application/x-httpd-php",
		"application/x-httpd-php-source",
		"application/x-bash",
		"application/x-awk",
		"application/x-protobuf",
		"application/x-terraform",
		"application/graphql",
		"application/x-graphql",
		"application/toml",
		"application/x-toml",
		"application/yaml",
		"application/x-yaml",
		"message/rfc822":
		return true
	}
	return false
}

func openAIFileExtensionEligible(fileName string) bool {
	ext := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(fileName))), ".")
	switch ext {
	case "pdf",
		"xla", "xlb", "xlc", "xlm", "xls", "xlsx", "xlt", "xlw",
		"csv", "tsv", "iif",
		"doc", "docx", "dot", "odt", "rtf",
		"key", "pot", "ppa", "pps", "ppt", "pptx", "pwz", "wiz":
		return true
	default:
		return openAITextCodeExtension(fileName)
	}
}

func openAITextCodeExtension(fileName string) bool {
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(fileName))), ".") {
	case "asm", "bat", "c", "cc", "conf", "cpp", "css", "cxx", "def", "dic", "eml", "h", "hh",
		"htm", "html", "ics", "ifb", "in", "js", "json", "jsonl", "ksh", "list", "log",
		"markdown", "md", "mht", "mhtml", "mime", "mjs", "ndjson", "nws", "pl", "py",
		"rst", "s", "sql", "srt", "text", "txt", "vcf", "vtt", "xml", "yaml", "yml",
		"toml", "ini", "cfg", "sh", "bash", "zsh", "java", "rb", "rs", "go", "jsx",
		"ts", "tsx", "scss", "sass", "less", "php", "cs", "kt", "kts", "swift", "lua",
		"r", "jl", "erl", "ex", "exs", "hs", "clj", "groovy", "dart", "awk", "proto",
		"graphql", "gql", "astro", "diff", "patch", "makefile", "dockerfile", "tf", "hcl":
		return true
	default:
		return false
	}
}

func responsesFallbackFileName(mimeType string) string {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "application/pdf":
		return "attachment.pdf"
	case "text/plain":
		return "attachment.txt"
	case "text/markdown":
		return "attachment.md"
	case "text/csv", "application/csv":
		return "attachment.csv"
	case "text/tab-separated-values":
		return "attachment.tsv"
	case "application/json":
		return "attachment.json"
	default:
		return "attachment"
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

func responsesToolAttachmentInputContent(msg types.ChatMessage, fileInputMode FileInputMode) []any {
	images := responsesImageContent(msg.ImageAttachments, msg.FileAttachments, fileInputMode)
	files := responsesInputFileContent(msg.FileAttachments, fileInputMode)
	if len(images) == 0 && len(files) == 0 {
		return nil
	}
	toolName := strings.TrimSpace(msg.Name)
	if toolName == "" {
		toolName = "tool"
	}
	content := make([]any, 0, len(images)+len(files)+1)
	content = append(content, map[string]any{
		"type": "input_text",
		"text": fmt.Sprintf("Attachment returned by %s.", toolName),
	})
	content = append(content, images...)
	content = append(content, files...)
	return content
}

func chatCompletionsToolAttachmentContent(msg types.ChatMessage, fileInputMode FileInputMode) []any {
	images := chatCompletionsImageContent(msg.ImageAttachments, msg.FileAttachments, fileInputMode)
	files := chatCompletionsFileContent(msg.FileAttachments, fileInputMode)
	if len(images) == 0 && len(files) == 0 {
		return nil
	}
	toolName := strings.TrimSpace(msg.Name)
	if toolName == "" {
		toolName = "tool"
	}
	content := make([]any, 0, len(images)+len(files)+1)
	content = append(content, map[string]any{
		"type": "text",
		"text": fmt.Sprintf("Attachment returned by %s.", toolName),
	})
	content = append(content, images...)
	content = append(content, files...)
	return content
}

func messagesToResponses(req types.ChatRequest, mapper *toolnamemapper.Mapper) []any {
	return messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputNativeSupported)
}

func findResponsesAnchorIndex(messages []types.ChatMessage, previousResponseID string, providerID string) int {
	target := strings.TrimSpace(previousResponseID)
	if target == "" {
		return -1
	}
	for idx := len(messages) - 1; idx >= 0; idx-- {
		msg := messages[idx]
		if msg.Role != types.RoleAssistant || len(msg.ProviderPayload) == 0 {
			continue
		}
		state := decodeResponsesProviderState(msg.ProviderPayload, providerID)
		if state == nil {
			continue
		}
		if raw, ok := state["previous_response_id"].(string); ok && strings.TrimSpace(raw) == target {
			return idx
		}
	}
	return -1
}

func decodeResponsesProviderState(raw json.RawMessage, providerID string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var payload struct {
		Format     string         `json:"format"`
		ProviderID string         `json:"provider_id"`
		APIType    string         `json:"api_type"`
		State      map[string]any `json:"state"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	if strings.TrimSpace(payload.Format) != "openai_responses_v1" {
		return nil
	}
	if strings.TrimSpace(payload.APIType) != "" && strings.TrimSpace(payload.APIType) != string(APIResponses) {
		return nil
	}
	if providerID != "" && strings.TrimSpace(payload.ProviderID) != "" && strings.TrimSpace(payload.ProviderID) != strings.TrimSpace(providerID) {
		return nil
	}
	if len(payload.State) == 0 {
		return nil
	}
	b, err := json.Marshal(payload.State)
	if err != nil {
		return payload.State
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func encodeResponsesProviderPayload(providerID string, state map[string]any) json.RawMessage {
	if len(state) == 0 {
		return nil
	}
	payload := map[string]any{
		"format":      "openai_responses_v1",
		"provider_id": strings.TrimSpace(providerID),
		"api_type":    string(APIResponses),
		"state":       state,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return b
}

func cloneRawJSON(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func responsesProviderStateFromCompletedResponse(respObj map[string]any, requestID *string) map[string]any {
	state := map[string]any{}
	if rid, ok := respObj["id"].(string); ok && strings.TrimSpace(rid) != "" {
		state["previous_response_id"] = strings.TrimSpace(rid)
	} else if requestID != nil && strings.TrimSpace(*requestID) != "" {
		state["previous_response_id"] = strings.TrimSpace(*requestID)
	}
	if output, ok := respObj["output"].([]any); ok {
		for _, raw := range output {
			obj, _ := raw.(map[string]any)
			if obj == nil {
				continue
			}
			if typ, _ := obj["type"].(string); typ != "code_interpreter_call" {
				continue
			}
			if containerID, ok := obj["container_id"].(string); ok && strings.TrimSpace(containerID) != "" {
				state["container_id"] = strings.TrimSpace(containerID)
				break
			}
			if container, ok := obj["container"].(map[string]any); ok {
				if containerID, ok := container["id"].(string); ok && strings.TrimSpace(containerID) != "" {
					state["container_id"] = strings.TrimSpace(containerID)
					break
				}
			}
		}
	}
	if len(state) == 0 {
		return nil
	}
	return state
}

func streamChatCompletions(ctx context.Context, out chan<- types.StreamEvent, resp *http.Response, model string, metaBase types.ResponseMeta, request types.ChatRequest, mapper *toolnamemapper.Mapper) {
	accumulatedText := ""
	toolCallsByIndex := map[int]*struct{ id, name, args string }{}

	var requestID *string
	if rid := strings.TrimSpace(resp.Header.Get("x-request-id")); rid != "" {
		requestID = &rid
	} else if rid := strings.TrimSpace(resp.Header.Get("request-id")); rid != "" {
		requestID = &rid
	}
	var resolvedModel *string
	var providerVersion *string
	for _, header := range []string{"openai-version", "x-ms-api-version"} {
		if version := strings.TrimSpace(resp.Header.Get(header)); version != "" {
			providerVersion = &version
			break
		}
	}
	usage := normalizedUsage{}
	terminalSeen := false
	terminalMeta := func() types.ResponseMeta {
		meta := metaBase
		meta.RequestID = requestID
		meta.Model = model
		meta.ResolvedModel = resolvedModel
		meta.ProviderVersion = providerVersion
		return usage.apply(meta)
	}
	for msg := range stream.Parse(ctx, resp.Body) {
		if msg.Err != nil {
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
			return
		}
		dataStr := strings.TrimSpace(msg.Data)
		if dataStr == "" {
			continue
		}
		if dataStr == "[DONE]" {
			terminalSeen = true
			break
		}
		var chunk chatCompletionsChunk
		dec := json.NewDecoder(strings.NewReader(dataStr))
		dec.UseNumber()
		if err := dec.Decode(&chunk); err != nil {
			continue
		}
		if requestID == nil && chunk.ID != nil {
			requestID = chunk.ID
		}
		if chunk.Model != nil && strings.TrimSpace(*chunk.Model) != "" {
			resolved := strings.TrimSpace(*chunk.Model)
			resolvedModel = &resolved
		}
		if providerVersion == nil && chunk.SystemFingerprint != nil && strings.TrimSpace(*chunk.SystemFingerprint) != "" {
			version := strings.TrimSpace(*chunk.SystemFingerprint)
			providerVersion = &version
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta != nil {
			delta := chunk.Choices[0].Delta
			if delta.Content != nil && *delta.Content != "" {
				accumulatedText += *delta.Content
				send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: *delta.Content}})
			}
			for offset, tc := range delta.ToolCalls {
				idx := offset
				if tc.Index != nil {
					idx = *tc.Index
				}
				entry := toolCallsByIndex[idx]
				if entry == nil {
					entry = &struct{ id, name, args string }{}
					toolCallsByIndex[idx] = entry
				}
				if tc.ID != nil {
					entry.id = *tc.ID
				}
				if tc.Function != nil {
					if tc.Function.Name != nil {
						entry.name = *tc.Function.Name
					}
					if tc.Function.Arguments != nil {
						entry.args += *tc.Function.Arguments
					}
				}
			}
		}
		for _, choice := range chunk.Choices {
			if choice.FinishReason != nil && strings.TrimSpace(*choice.FinishReason) != "" {
				terminalSeen = true
			}
		}
		if chunk.Usage != nil {
			usage.merge(parseOpenAIUsage(chunk.Usage))
			_ = types.NotifyUsageSnapshot(request, terminalMeta())
		}
	}

	meta := terminalMeta()
	if !terminalSeen {
		send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
			Code: "STREAM_ENDED", Message: "Chat Completions stream ended before a terminal marker", Retryable: true, Meta: &meta,
		}})
		return
	}

	if len(toolCallsByIndex) > 0 {
		calls := toolCallsFromIndex(toolCallsByIndex, mapper)
		send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: calls, Meta: meta}})
		return
	}
	send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: accumulatedText, Meta: meta}})
}

func streamResponses(ctx context.Context, out chan<- types.StreamEvent, resp *http.Response, model string, metaBase types.ResponseMeta, request types.ChatRequest, mapper *toolnamemapper.Mapper) {
	accumulatedText := ""
	toolCallsByIndex := map[int]*struct{ id, name, args string }{}
	providerToolsByID := map[string]*responsesProviderToolState{}
	providerToolOrder := make([]string, 0)
	compactionIndexes := map[int]struct{}{}
	compactionActive := false
	compactionSeen := false
	var requestID *string
	for _, header := range []string{"x-request-id", "request-id"} {
		if rid := strings.TrimSpace(resp.Header.Get(header)); rid != "" {
			requestID = &rid
			break
		}
	}
	var resolvedModel *string
	var providerVersion *string
	for _, header := range []string{"openai-version", "x-ms-api-version"} {
		if version := strings.TrimSpace(resp.Header.Get(header)); version != "" {
			providerVersion = &version
			break
		}
	}
	usage := normalizedUsage{}
	finalSent := false
	var providerState map[string]any
	terminalMeta := func() types.ResponseMeta {
		meta := metaBase
		meta.RequestID = requestID
		meta.Model = model
		meta.ResolvedModel = resolvedModel
		meta.ProviderVersion = providerVersion
		meta.ProviderState = providerState
		return usage.apply(meta)
	}
	mergeResponseIdentityAndUsage := func(respObj map[string]any) {
		if respObj == nil {
			return
		}
		if rid, ok := respObj["id"].(string); ok && requestID == nil {
			rid = strings.TrimSpace(rid)
			if rid != "" {
				requestID = &rid
			}
		}
		if returnedModel, ok := respObj["model"].(string); ok && strings.TrimSpace(returnedModel) != "" {
			resolved := strings.TrimSpace(returnedModel)
			resolvedModel = &resolved
		}
		if fingerprint, ok := respObj["system_fingerprint"].(string); providerVersion == nil && ok && strings.TrimSpace(fingerprint) != "" {
			version := strings.TrimSpace(fingerprint)
			providerVersion = &version
		}
		if rawUsage, ok := respObj["usage"]; ok {
			usage.merge(parseOpenAIUsage(rawUsage))
			_ = types.NotifyUsageSnapshot(request, terminalMeta())
		}
	}

	startCompaction := func() {
		if compactionActive {
			return
		}
		compactionActive = true
		compactionSeen = true
		send(ctx, out, types.StreamEvent{Event: types.EventCompactionStart, Data: map[string]any{}})
	}
	endCompaction := func() {
		if !compactionActive {
			return
		}
		compactionActive = false
		send(ctx, out, types.StreamEvent{Event: types.EventCompactionEnd, Data: map[string]any{}})
	}
	emitProviderToolProgress := func(state *responsesProviderToolState) {
		progress := responsesProviderToolProgressData(state)
		if strings.TrimSpace(progress.ToolCallID) == "" || strings.TrimSpace(progress.ToolName) == "" {
			return
		}
		send(ctx, out, types.StreamEvent{Event: types.EventProviderToolProgress, Data: progress})
	}

	for msg := range stream.Parse(ctx, resp.Body) {
		if msg.Err != nil {
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
			return
		}
		dataStr := strings.TrimSpace(msg.Data)
		if dataStr == "" {
			continue
		}
		if dataStr == "[DONE]" {
			break
		}

		var payload responsesSSEPayload
		dec := json.NewDecoder(strings.NewReader(dataStr))
		dec.UseNumber()
		if err := dec.Decode(&payload); err != nil {
			continue
		}
		var rawPayload map[string]any
		rawDec := json.NewDecoder(strings.NewReader(dataStr))
		rawDec.UseNumber()
		_ = rawDec.Decode(&rawPayload)

		curType := ""
		if msg.Event != "" {
			curType = msg.Event
		} else if payload.Type != nil {
			curType = *payload.Type
		}
		if requestID == nil && payload.ID != nil {
			requestID = payload.ID
		}

		switch curType {
		case "response.output_text.delta", "response.text.delta":
			if s, ok := payload.Delta.(string); ok && s != "" {
				accumulatedText += s
				send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: s}})
			}
		case "response.output_item.added":
			item, _ := payload.Item.(map[string]any)
			if item != nil {
				if isResponsesCompactionItem(item) {
					idx := 0
					if payload.OutputIndex != nil {
						idx = *payload.OutputIndex
					}
					compactionIndexes[idx] = struct{}{}
					startCompaction()
				} else if typ, _ := item["type"].(string); typ == "web_search_call" {
					emitProviderToolProgress(recordResponsesWebSearchItem(providerToolsByID, &providerToolOrder, item))
				} else if typ == "code_interpreter_call" {
					recordResponsesCodeInterpreterItem(providerToolsByID, &providerToolOrder, item)
				} else if typ == "function_call" {
					idx := 0
					if payload.OutputIndex != nil {
						idx = *payload.OutputIndex
					}
					entry := toolCallsByIndex[idx]
					if entry == nil {
						entry = &struct{ id, name, args string }{}
						toolCallsByIndex[idx] = entry
					}
					if callID, ok := item["call_id"].(string); ok {
						entry.id = callID
					}
					if nm, ok := item["name"].(string); ok {
						entry.name = nm
					}
				}
			}
		case "response.output_item.done", "response.output_item.completed":
			idx := 0
			if payload.OutputIndex != nil {
				idx = *payload.OutputIndex
			}
			item, _ := payload.Item.(map[string]any)
			if isResponsesCompactionItem(item) {
				compactionIndexes[idx] = struct{}{}
				endCompaction()
			} else if _, ok := compactionIndexes[idx]; ok {
				endCompaction()
			} else if typ, _ := item["type"].(string); typ == "code_interpreter_call" {
				recordResponsesCodeInterpreterItem(providerToolsByID, &providerToolOrder, item)
			} else if typ == "web_search_call" {
				emitProviderToolProgress(recordResponsesWebSearchItem(providerToolsByID, &providerToolOrder, item))
			}
		case "response.compaction.delta", "response.context_management.delta", "response.context_management.compaction.delta":
			startCompaction()
		case "response.compaction.started", "response.compaction.in_progress", "response.context_management.compaction.started", "response.context_management.compaction.in_progress":
			startCompaction()
		case "response.compaction.completed", "response.compaction.done", "response.context_management.compaction.completed", "response.context_management.compaction.done":
			endCompaction()
		case "response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed":
			item, _ := payload.Item.(map[string]any)
			if item == nil && len(rawPayload) > 0 {
				item = openAIDeepCopyMap(rawPayload)
				item["type"] = "web_search_call"
			}
			if item != nil {
				emitProviderToolProgress(recordResponsesWebSearchItem(providerToolsByID, &providerToolOrder, item))
			}
		case "response.code_interpreter_call.in_progress", "response.code_interpreter_call.interpreting", "response.code_interpreter_call.completed":
			item, _ := payload.Item.(map[string]any)
			if item == nil && len(rawPayload) > 0 {
				item = openAIDeepCopyMap(rawPayload)
				item["type"] = "code_interpreter_call"
			}
			if item != nil {
				recordResponsesCodeInterpreterItem(providerToolsByID, &providerToolOrder, item)
			}
		case "response.function_call_arguments.delta":
			idx := 0
			if payload.OutputIndex != nil {
				idx = *payload.OutputIndex
			}
			entry := toolCallsByIndex[idx]
			if entry != nil {
				if s, ok := payload.Delta.(string); ok {
					entry.args += s
				}
			}
		case "response.completed":
			respObj, _ := payload.Response.(map[string]any)
			if respObj != nil {
				mergeResponseIdentityAndUsage(respObj)
				if outText, ok := respObj["output_text"].(string); ok && outText != "" {
					accumulatedText = outText
				}

				var outputItems []any
				if output, ok := respObj["output"].([]any); ok {
					outputItems = output
					for _, it := range outputItems {
						obj, _ := it.(map[string]any)
						if obj == nil {
							continue
						}
						if typ, _ := obj["type"].(string); typ == "web_search_call" {
							emitProviderToolProgress(recordResponsesWebSearchItem(providerToolsByID, &providerToolOrder, obj))
						} else if typ == "code_interpreter_call" {
							recordResponsesCodeInterpreterItem(providerToolsByID, &providerToolOrder, obj)
						} else if isResponsesCompactionItem(obj) && !compactionSeen {
							startCompaction()
						}
					}
					if annotations := responsesWebSearchAnnotationsFromOutput(outputItems); len(annotations) > 0 {
						appendResponsesWebSearchResults(providerToolsByID, providerToolOrder, annotations)
						for _, id := range providerToolOrder {
							state := providerToolsByID[id]
							if state != nil && state.Name == "web_search" {
								emitProviderToolProgress(state)
							}
						}
					}
				}

				if len(toolCallsByIndex) == 0 {
					if len(outputItems) > 0 {
						for idx, it := range outputItems {
							obj, _ := it.(map[string]any)
							if obj == nil {
								continue
							}
							if typ, _ := obj["type"].(string); typ != "function_call" {
								continue
							}
							entry := &struct{ id, name, args string }{}
							if callID, ok := obj["call_id"].(string); ok {
								entry.id = callID
							}
							if nm, ok := obj["name"].(string); ok {
								entry.name = nm
							}
							if args, ok := obj["arguments"].(string); ok {
								entry.args = args
							}
							toolCallsByIndex[idx] = entry
						}
					}
				}
				providerState = responsesProviderStateFromCompletedResponse(respObj, requestID)
			}
			endCompaction()

			meta := terminalMeta()
			providerPayload := encodeResponsesProviderPayload(meta.ProviderID, providerState)

			if len(toolCallsByIndex) > 0 {
				calls := toolCallsFromIndex(toolCallsByIndex, mapper)
				send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: calls, Meta: meta, ProviderPayload: cloneRawJSON(providerPayload)}})
			} else {
				providerToolCalls, providerToolResults := responsesProviderToolsFinal(providerToolsByID, providerToolOrder)
				send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: accumulatedText, Meta: meta, ProviderPayload: cloneRawJSON(providerPayload), ToolCalls: providerToolCalls, ToolResults: providerToolResults}})
			}
			finalSent = true
			return
		case "response.error", "response.failed", "response.incomplete":
			respObj, _ := payload.Response.(map[string]any)
			mergeResponseIdentityAndUsage(respObj)
			msgText := "Responses API error"
			if errObj, ok := payload.Error.(map[string]any); ok {
				if m, ok := errObj["message"].(string); ok && strings.TrimSpace(m) != "" {
					msgText = m
				}
			}
			code, retryable := requestguard.NormalizeStreamError(msgText)
			meta := terminalMeta()
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: msgText, Retryable: retryable, Meta: &meta}})
			return
		}
	}

	if !finalSent {
		endCompaction()
		meta := terminalMeta()
		send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
			Code: "STREAM_ENDED", Message: "Responses stream ended before response.completed", Retryable: true, Meta: &meta,
		}})
	}
}

func isResponsesCompactionItem(item map[string]any) bool {
	if len(item) == 0 {
		return false
	}
	typ := strings.ToLower(strings.TrimSpace(openAIStringValue(item["type"])))
	if strings.Contains(typ, "compaction") || strings.Contains(typ, "compact") {
		return true
	}
	role := strings.ToLower(strings.TrimSpace(openAIStringValue(item["role"])))
	return strings.Contains(role, "compaction") || strings.Contains(role, "compact")
}

func recordResponsesWebSearchItem(tools map[string]*responsesProviderToolState, order *[]string, item map[string]any) *responsesProviderToolState {
	if len(item) == 0 {
		return nil
	}
	if typ := strings.TrimSpace(openAIStringValue(item["type"])); typ != "" && typ != "web_search_call" {
		return nil
	}
	query := responsesWebSearchQuery(item)
	results := responsesWebSearchResultsFromItem(item)
	id := firstOpenAIString(item["id"], item["call_id"], item["tool_call_id"])
	if id == "" {
		id = responsesProviderToolSyntheticID(tools, "web_search", "web_search", query, results)
	}
	state := tools[id]
	if state == nil {
		state = &responsesProviderToolState{
			ID:        id,
			Name:      "web_search",
			BlockType: "web_search_call",
			Args:      map[string]any{},
		}
		tools[id] = state
		*order = append(*order, id)
	}
	if status := firstOpenAIString(item["status"], item["state"]); status != "" {
		state.Status = status
	}
	if action := openAIMapValue(item["action"]); len(action) > 0 {
		state.Action = openAIDeepCopyMap(action)
	}
	if query != "" {
		state.Args["query"] = query
	}
	if message := firstOpenAIString(item["message"], item["summary"], item["error"]); message != "" {
		state.Message = message
	}
	appendUniqueResponsesWebSearchResults(state, results)
	return state
}

func responsesProviderToolSyntheticID(tools map[string]*responsesProviderToolState, toolName string, prefix string, query string, results []map[string]any) string {
	toolName = strings.TrimSpace(toolName)
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "provider_tool"
	}
	if query = strings.TrimSpace(query); query != "" {
		for id, state := range tools {
			if state == nil || state.Name != toolName {
				continue
			}
			if strings.TrimSpace(openAIStringValue(state.Args["query"])) == query {
				return id
			}
		}
		return prefix + "_" + responsesProviderToolSlug(query)
	}
	for _, result := range results {
		key := responsesWebSearchResultKey(result)
		if key == "" {
			continue
		}
		for id, state := range tools {
			if state == nil || state.Name != toolName {
				continue
			}
			for _, existing := range state.Results {
				if responsesWebSearchResultKey(existing) == key {
					return id
				}
			}
		}
		return prefix + "_" + responsesProviderToolSlug(key)
	}
	for id, state := range tools {
		if state == nil || state.Name != toolName {
			continue
		}
		if len(state.Args) == 0 && len(state.Action) == 0 && len(state.Results) == 0 {
			return id
		}
	}
	return prefix + "_" + strings.ReplaceAll(types.NewUUID(), "-", "")
}

func responsesProviderToolSlug(value string) string {
	cleaned := strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range cleaned {
		isWord := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isWord {
			b.WriteRune(r)
			lastUnderscore = false
		} else if !lastUnderscore && b.Len() > 0 {
			b.WriteByte('_')
			lastUnderscore = true
		}
		if b.Len() >= 44 {
			break
		}
	}
	slug := strings.Trim(b.String(), "_")
	if slug == "" {
		slug = "item"
	}
	return slug + "_" + responsesProviderToolHash(cleaned)
}

func responsesProviderToolHash(value string) string {
	var h uint32 = 2166136261
	for _, b := range []byte(value) {
		h ^= uint32(b)
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

func responsesProviderToolProgressData(state *responsesProviderToolState) types.ProviderToolProgressData {
	if state == nil {
		return types.ProviderToolProgressData{}
	}
	status := strings.TrimSpace(state.Status)
	statusKey := strings.ToLower(status)
	phase := "polling"
	label := "Running provider tool"
	message := strings.TrimSpace(state.Message)
	if state.Name == "web_search" {
		label = "Searching the web"
		message = "Searching the web"
		if statusKey == "completed" || statusKey == "complete" || statusKey == "done" {
			phase = "complete"
			label = "Web search complete"
			if len(state.Results) == 1 {
				message = "Found 1 result"
			} else if len(state.Results) > 1 {
				message = fmt.Sprintf("Found %d results", len(state.Results))
			}
		} else if statusKey == "in_progress" || statusKey == "queued" || statusKey == "pending" {
			phase = "submitted"
		}
	}
	query := strings.TrimSpace(openAIStringValue(state.Args["query"]))
	previewCount := len(state.Results)
	if previewCount > 3 {
		previewCount = 3
	}
	preview := make([]map[string]any, 0, previewCount)
	for _, result := range state.Results[:previewCount] {
		preview = append(preview, openAIDeepCopyMap(result))
	}
	return types.ProviderToolProgressData{
		ToolCallID:       state.ID,
		ToolName:         state.Name,
		Provider:         "provider-hosted",
		Integration:      "openai-responses",
		AdapterFamily:    "openai-responses",
		Phase:            phase,
		Label:            label,
		Message:          message,
		Status:           status,
		State:            status,
		Query:            query,
		ResultsCount:     len(state.Results),
		ResultPreview:    preview,
		ProviderExecuted: true,
	}
}

func recordResponsesCodeInterpreterItem(tools map[string]*responsesProviderToolState, order *[]string, item map[string]any) {
	if len(item) == 0 {
		return
	}
	if typ := strings.TrimSpace(openAIStringValue(item["type"])); typ != "" && typ != "code_interpreter_call" {
		return
	}
	id := firstOpenAIString(item["id"], item["call_id"], item["tool_call_id"])
	if id == "" {
		id = "code_interpreter_" + strings.ReplaceAll(types.NewUUID(), "-", "")
	}
	state := tools[id]
	if state == nil {
		state = &responsesProviderToolState{
			ID:        id,
			Name:      "code_interpreter",
			BlockType: "code_interpreter_call",
			Args:      map[string]any{},
		}
		tools[id] = state
		*order = append(*order, id)
	}
	if status := firstOpenAIString(item["status"], item["state"]); status != "" {
		state.Status = status
	}
	if code := firstOpenAIString(item["code"], item["input"]); code != "" {
		state.Args["code"] = code
	}
	if containerID := firstOpenAIString(item["container_id"], item["containerId"]); containerID != "" {
		state.ContainerID = containerID
	} else if container := openAIMapValue(item["container"]); len(container) > 0 {
		if containerID := firstOpenAIString(container["id"], container["container_id"], container["containerId"]); containerID != "" {
			state.ContainerID = containerID
		}
	}
	if message := firstOpenAIString(item["message"], item["summary"], item["error"]); message != "" {
		state.Message = message
	}
	for _, key := range []string{"outputs", "results", "content"} {
		state.Outputs = appendUniqueOpenAIValues(state.Outputs, item[key])
	}
	for _, key := range []string{"files", "output_files", "generated_files"} {
		state.Files = appendUniqueOpenAIValues(state.Files, item[key])
	}
}

func responsesWebSearchQuery(item map[string]any) string {
	if query := firstOpenAIString(item["query"], item["search_query"], item["searchQuery"]); query != "" {
		return query
	}
	action := openAIMapValue(item["action"])
	if len(action) == 0 {
		return ""
	}
	if query := firstOpenAIString(action["query"], action["search_query"], action["searchQuery"]); query != "" {
		return query
	}
	for _, key := range []string{"queries", "search_terms", "searchTerms"} {
		if values, ok := action[key].([]any); ok {
			for _, raw := range values {
				if query := strings.TrimSpace(openAIStringValue(raw)); query != "" {
					return query
				}
			}
		}
	}
	return ""
}

func responsesWebSearchResultsFromItem(item map[string]any) []map[string]any {
	if len(item) == 0 {
		return nil
	}
	results := make([]map[string]any, 0)
	for _, key := range []string{"results", "search_results", "searchResults"} {
		results = append(results, normalizeResponsesWebSearchResults(item[key])...)
	}
	action := openAIMapValue(item["action"])
	if len(action) > 0 {
		for _, key := range []string{"results", "search_results", "searchResults", "sources"} {
			results = append(results, normalizeResponsesWebSearchResults(action[key])...)
		}
	}
	if len(results) == 0 {
		results = append(results, normalizeResponsesWebSearchResults(item["content"])...)
	}
	return results
}

func normalizeResponsesWebSearchResults(raw any) []map[string]any {
	switch typed := raw.(type) {
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			out = append(out, normalizeResponsesWebSearchResults(item)...)
		}
		return out
	case map[string]any:
		if nested := normalizeResponsesWebSearchResults(typed["results"]); len(nested) > 0 {
			return nested
		}
		result := map[string]any{}
		for _, key := range []string{"title", "url", "snippet", "text", "source", "published_at", "publishedAt"} {
			if value, ok := typed[key]; ok {
				result[key] = value
			}
		}
		if len(result) == 0 && (firstOpenAIString(typed["title"], typed["url"]) != "" || len(typed) <= 12) {
			result = openAIDeepCopyMap(typed)
		}
		if len(result) == 0 {
			return nil
		}
		return []map[string]any{result}
	default:
		return nil
	}
}

func responsesWebSearchAnnotationsFromOutput(output []any) []map[string]any {
	if len(output) == 0 {
		return nil
	}
	results := make([]map[string]any, 0)
	for _, rawItem := range output {
		item := openAIMapValue(rawItem)
		if len(item) == 0 {
			continue
		}
		results = append(results, responsesWebSearchResultsFromAnnotations(item["annotations"])...)
		content, _ := item["content"].([]any)
		for _, rawContent := range content {
			contentItem := openAIMapValue(rawContent)
			if len(contentItem) == 0 {
				continue
			}
			results = append(results, responsesWebSearchResultsFromAnnotations(contentItem["annotations"])...)
		}
	}
	return results
}

func responsesWebSearchResultsFromAnnotations(raw any) []map[string]any {
	annotations, ok := raw.([]any)
	if !ok || len(annotations) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(annotations))
	for _, rawAnnotation := range annotations {
		annotation := openAIMapValue(rawAnnotation)
		if len(annotation) == 0 {
			continue
		}
		url := firstOpenAIString(annotation["url"], annotation["uri"])
		title := firstOpenAIString(annotation["title"])
		if url == "" && title == "" {
			continue
		}
		result := map[string]any{}
		if title != "" {
			result["title"] = title
		}
		if url != "" {
			result["url"] = url
		}
		if snippet := firstOpenAIString(annotation["snippet"], annotation["text"]); snippet != "" {
			result["snippet"] = snippet
		}
		if kind := firstOpenAIString(annotation["type"]); kind != "" {
			result["type"] = kind
		}
		out = append(out, result)
	}
	return out
}

func appendResponsesWebSearchResults(tools map[string]*responsesProviderToolState, order []string, results []map[string]any) {
	if len(results) == 0 {
		return
	}
	for _, id := range order {
		state := tools[id]
		if state == nil || len(state.Results) > 0 {
			continue
		}
		appendUniqueResponsesWebSearchResults(state, results)
	}
}

func appendUniqueResponsesWebSearchResults(state *responsesProviderToolState, results []map[string]any) {
	if state == nil || len(results) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(state.Results)+len(results))
	for _, existing := range state.Results {
		seen[responsesWebSearchResultKey(existing)] = struct{}{}
	}
	for _, result := range results {
		if len(result) == 0 {
			continue
		}
		key := responsesWebSearchResultKey(result)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		state.Results = append(state.Results, openAIDeepCopyMap(result))
	}
}

func appendUniqueOpenAIValues(existing []any, raw any) []any {
	values := normalizeOpenAIValueList(raw)
	if len(values) == 0 {
		return existing
	}
	seen := make(map[string]struct{}, len(existing)+len(values))
	for _, value := range existing {
		seen[openAIValueKey(value)] = struct{}{}
	}
	for _, value := range values {
		key := openAIValueKey(value)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		existing = append(existing, openAIDeepCopyValue(value))
	}
	return existing
}

func normalizeOpenAIValueList(raw any) []any {
	switch typed := raw.(type) {
	case nil:
		return nil
	case []any:
		out := make([]any, 0, len(typed))
		for _, value := range typed {
			out = append(out, normalizeOpenAIValueList(value)...)
		}
		return out
	default:
		return []any{typed}
	}
}

func openAIValueKey(value any) string {
	if s := strings.TrimSpace(openAIStringValue(value)); s != "" {
		return s
	}
	if b, err := json.Marshal(value); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%T:%v", value, value)
}

func responsesWebSearchResultKey(result map[string]any) string {
	return strings.TrimSpace(firstOpenAIString(result["url"], result["title"], result["snippet"]))
}

func openAIProviderHostedResultMetadata(toolName, blockType string) map[string]any {
	adapterFamily := "openai-responses"
	toolName = strings.TrimSpace(toolName)
	presentation := openAIProviderHostedPresentationMap(chattools.ToolPresentationSpec{
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
			presentation = openAIProviderHostedPresentationMap(capability.Presentation)
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

func openAIProviderHostedPresentationMap(p chattools.ToolPresentationSpec) map[string]any {
	return map[string]any{
		"family":            p.Family,
		"result_kind":       p.ResultKind,
		"renderer_strategy": p.RendererStrategy,
		"renderer_key":      p.RendererKey,
		"native_viewer":     p.NativeViewer,
	}
}

func responsesProviderToolsFinal(tools map[string]*responsesProviderToolState, order []string) ([]types.ToolCall, []types.ChatMessage) {
	if len(tools) == 0 || len(order) == 0 {
		return nil, nil
	}
	calls := make([]types.ToolCall, 0, len(order))
	results := make([]types.ChatMessage, 0, len(order))
	for _, id := range order {
		state := tools[id]
		if state == nil {
			continue
		}
		args := openAIDeepCopyMap(state.Args)
		if len(args) == 0 {
			args = map[string]any{}
		}
		argsText := "{}"
		if b, err := json.Marshal(args); err == nil {
			argsText = string(b)
		}
		calls = append(calls, types.ToolCall{
			ID:               state.ID,
			Name:             state.Name,
			Args:             args,
			ArgsText:         argsText,
			ProviderExecuted: true,
		})

		payload := map[string]any{}
		if query := strings.TrimSpace(openAIStringValue(args["query"])); query != "" {
			payload["query"] = query
		}
		if state.Status != "" {
			payload["status"] = state.Status
		}
		if len(state.Action) > 0 {
			payload["action"] = openAIDeepCopyMap(state.Action)
		}
		if len(state.Results) > 0 {
			resultItems := make([]any, 0, len(state.Results))
			for _, result := range state.Results {
				resultItems = append(resultItems, openAIDeepCopyMap(result))
			}
			payload["results"] = resultItems
		}
		if state.Message != "" {
			payload["message"] = state.Message
		}
		if state.ContainerID != "" {
			payload["container_id"] = state.ContainerID
		}
		if len(state.Outputs) > 0 {
			payload["outputs"] = openAIDeepCopyValue(state.Outputs)
		}
		if len(state.Files) > 0 {
			payload["files"] = openAIDeepCopyValue(state.Files)
		}
		if len(payload) == 0 {
			payload["status"] = "completed"
		}
		env := map[string]any{
			"artifact_id":     "provider_" + strings.ReplaceAll(types.NewUUID(), "-", ""),
			"tool_name":       state.Name,
			"tool_call_id":    state.ID,
			"ok":              true,
			"result_format":   "json",
			"result":          payload,
			"result_metadata": openAIProviderHostedResultMetadata(state.Name, state.BlockType),
		}
		if len(args) > 0 {
			env["tool_args"] = openAIDeepCopyMap(args)
		}
		contentBytes, err := json.Marshal(env)
		if err != nil {
			continue
		}
		content := string(contentBytes)
		results = append(results, types.ChatMessage{
			Role:       types.RoleTool,
			Content:    &content,
			ToolCallID: state.ID,
			Name:       state.Name,
		})
	}
	if len(calls) == 0 {
		return nil, nil
	}
	return calls, results
}

func firstOpenAIString(values ...any) string {
	for _, value := range values {
		if text := strings.TrimSpace(openAIStringValue(value)); text != "" {
			return text
		}
	}
	return ""
}

func openAIStringValue(raw any) string {
	switch typed := raw.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		return ""
	}
}

func openAIMapValue(raw any) map[string]any {
	obj, ok := raw.(map[string]any)
	if !ok || obj == nil {
		return nil
	}
	return obj
}

func openAIDeepCopyMap(in map[string]any) map[string]any {
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

func openAIDeepCopyValue(in any) any {
	if in == nil {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return in
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return in
	}
	return out
}

func toolCallsFromIndex(byIndex map[int]*struct{ id, name, args string }, mapper *toolnamemapper.Mapper) []types.ToolCall {
	idxs := make([]int, 0, len(byIndex))
	for k := range byIndex {
		idxs = append(idxs, k)
	}
	sort.Ints(idxs)
	out := make([]types.ToolCall, 0, len(idxs))
	for _, idx := range idxs {
		raw := byIndex[idx]
		if raw == nil {
			continue
		}
		id := strings.TrimSpace(raw.id)
		if id == "" {
			id = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
		}
		provName := raw.name
		canonical := mapper.CanonicalName(provName)
		argsText := raw.args
		parseText := argsText
		if strings.TrimSpace(parseText) == "" {
			parseText = "{}"
		}
		args := map[string]any{}
		var parsed any
		if err := json.Unmarshal([]byte(parseText), &parsed); err == nil {
			if obj, ok := parsed.(map[string]any); ok {
				args = obj
			}
		}
		out = append(out, types.ToolCall{ID: id, Name: canonical, Args: args, ArgsText: argsText})
	}
	return out
}

func errorsIsContext(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "deadline")
}
