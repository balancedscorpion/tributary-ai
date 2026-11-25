package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/requestguard"
	"ai-analyst-local/internal/chat/providers/stream"
	"ai-analyst-local/internal/chat/providers/toolnamemapper"
	"ai-analyst-local/internal/chat/providers/vision"
	"ai-analyst-local/internal/chat/types"
)

type Provider struct {
	providerID      string
	model           string
	baseURL         string
	headers         map[string]string
	maxRequestBytes int64
	client          *http.Client
}

func New(providerID, model, baseURL string, headers map[string]string) *Provider {
	b := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	merged := map[string]string{"Content-Type": "application/json", "Accept-Encoding": "identity"}
	for k, v := range headers {
		merged[k] = v
	}
	return &Provider{providerID: providerID, model: model, baseURL: b, headers: merged, maxRequestBytes: requestguard.DefaultMaxBytes, client: &http.Client{Timeout: 0}}
}

func (p *Provider) ID() string    { return p.providerID }
func (p *Provider) Model() string { return p.model }

func (p *Provider) Capabilities() capabilities.ModelCapabilities {
	limits := capabilities.GeminiModelTokenLimits(p.model)
	return capabilities.Normalize(capabilities.ModelCapabilities{
		ProviderID:      p.providerID,
		Model:           p.model,
		MaxInputTokens:  limits.MaxInputTokens,
		MaxOutputTokens: limits.MaxOutputTokens,
		NativeCompaction: capabilities.NativeCompactionSupport{
			Mode: capabilities.NativeCompactionNone,
		},
	})
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

func applyGeminiUsage(meta types.ResponseMeta, raw any) types.ResponseMeta {
	usage, _ := raw.(map[string]any)
	if usage != nil {
		meta.InputTokens = coerceInt(usage["promptTokenCount"])
		meta.OutputTokens = coerceInt(usage["candidatesTokenCount"])
		meta.TotalTokens = coerceInt(usage["totalTokenCount"])
		meta.CachedInputTokens = coerceInt(usage["cachedContentTokenCount"])
		meta.ReasoningTokens = coerceInt(usage["thoughtsTokenCount"])
		if toolTokens := coerceInt(usage["toolUsePromptTokenCount"]); toolTokens != nil {
			meta.UsageDetails = map[string]int{"tool_use_prompt_tokens": *toolTokens}
		}
	}
	return types.NormalizeResponseMetaUsageForSemantics(meta, types.UsageSemanticsGemini)
}

func (p *Provider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	out := make(chan types.StreamEvent, 64)
	model := req.Model
	if strings.TrimSpace(model) == "" {
		model = p.model
	}
	metaBase := types.ResponseMeta{ProviderID: p.providerID, Model: model}
	apiSurface := "generate_content"
	metaBase.APIType = &apiSurface

	go func() {
		defer close(out)

		nameMapper := toolnamemapper.New(0)
		for _, td := range req.Tools {
			_ = nameMapper.ProviderName(td.Function.Name)
		}

		urlStr := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse", p.baseURL, url.PathEscape(model))

		tools := toolsToGemini(req.Tools, nameMapper)
		contents := messagesToGeminiForProvider(req, nameMapper, p.providerID)

		body := map[string]any{"contents": contents}
		if strings.TrimSpace(req.SystemPrompt) != "" {
			body["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": strings.TrimSpace(req.SystemPrompt)}}}
		}
		if len(tools) > 0 {
			body["tools"] = []any{map[string]any{"functionDeclarations": tools}}
			body["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "AUTO"}}
		}

		b, err := requestguard.MarshalJSON(body, requestguard.Context{
			ProviderID:  p.providerID,
			Model:       model,
			RequestKind: "gemini_stream_generate_content",
			Endpoint:    urlStr,
			MaxBytes:    p.maxRequestBytes,
		})
		if err != nil {
			code := requestguard.ErrorCodeFor(err)
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: err.Error(), Retryable: false, Meta: &metaBase}})
			return
		}

		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(b))
		if err != nil {
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: err.Error(), Retryable: false, Meta: &metaBase}})
			return
		}
		for k, v := range p.headers {
			hreq.Header.Set(k, v)
		}

		if err := types.NotifyUsageDispatch(req, "generate_content"); err != nil {
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: "PROVIDER_ERROR", Message: "usage accounting is unavailable", Retryable: false, Meta: &metaBase}})
			return
		}
		resp, err := p.client.Do(hreq)
		requestCount := 1
		metaBase.UpstreamRequestCount = &requestCount
		if err != nil {
			code := "PROVIDER_ERROR"
			retryable := true
			if errorsIsContext(err) {
				code = "ABORTED"
				retryable = false
			}
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: err.Error(), Retryable: retryable, Meta: &metaBase}})
			return
		}
		defer resp.Body.Close()
		for _, header := range []string{"x-goog-request-id", "x-request-id", "request-id"} {
			if requestID := strings.TrimSpace(resp.Header.Get(header)); requestID != "" {
				metaBase.RequestID = &requestID
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
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{Code: code, Message: msg, Retryable: retryable, Meta: &metaBase}})
			return
		}

		accumulatedText := ""
		toolCalls := make([]types.ToolCall, 0)
		providerParts := make([]any, 0)
		providerRole := "model"
		var usageMetadata map[string]any
		var resolvedModel, providerVersion *string
		requestID := metaBase.RequestID
		terminalSeen := false
		terminalMeta := func() types.ResponseMeta {
			meta := metaBase
			meta.RequestID = requestID
			meta.ResolvedModel = resolvedModel
			meta.ProviderVersion = providerVersion
			return applyGeminiUsage(meta, usageMetadata)
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
			raw := strings.TrimSpace(msg.Data)
			if raw == "" {
				continue
			}
			if raw == "[DONE]" {
				terminalSeen = true
				break
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err != nil {
				continue
			}
			if usage, ok := payload["usageMetadata"].(map[string]any); ok {
				if usageMetadata == nil {
					usageMetadata = map[string]any{}
				}
				for key, value := range usage {
					usageMetadata[key] = value
				}
				_ = types.NotifyUsageSnapshot(req, terminalMeta())
			}
			if responseID, ok := payload["responseId"].(string); ok && strings.TrimSpace(responseID) != "" {
				id := strings.TrimSpace(responseID)
				requestID = &id
			}
			if modelVersion, ok := payload["modelVersion"].(string); ok && strings.TrimSpace(modelVersion) != "" {
				version := strings.TrimSpace(modelVersion)
				resolvedModel = &version
				providerVersion = &version
			}

			cands, _ := payload["candidates"].([]any)
			if len(cands) == 0 {
				continue
			}
			cand, _ := cands[0].(map[string]any)
			if finishReason, ok := cand["finishReason"].(string); ok && strings.TrimSpace(finishReason) != "" {
				terminalSeen = true
			}
			content, _ := cand["content"].(map[string]any)
			if role, ok := content["role"].(string); ok && strings.TrimSpace(role) != "" {
				providerRole = role
			}
			parts, _ := content["parts"].([]any)
			for _, part := range parts {
				pobj, _ := part.(map[string]any)
				if pobj == nil {
					continue
				}
				// Gemini thought signatures are positional metadata on the original Part.
				// Keep every provider-native Part intact and in response order so a
				// subsequent tool-loop request can replay the model Content verbatim.
				providerParts = append(providerParts, pobj)
				if text, ok := pobj["text"].(string); ok && text != "" {
					accumulatedText += text
					send(ctx, out, types.StreamEvent{Event: "text_delta", Data: types.TextDeltaData{Content: text}})
				}
				if fc, ok := pobj["functionCall"].(map[string]any); ok {
					name, _ := fc["name"].(string)
					if strings.TrimSpace(name) == "" {
						continue
					}
					provName := name
					canonical := nameMapper.CanonicalName(provName)
					args := map[string]any{}
					if aobj, ok := fc["args"].(map[string]any); ok {
						args = aobj
					} else if astr, ok := fc["args"].(string); ok {
						var parsed any
						if err := json.Unmarshal([]byte(astr), &parsed); err == nil {
							if o, ok := parsed.(map[string]any); ok {
								args = o
							}
						}
					}
					callID, _ := fc["id"].(string)
					if strings.TrimSpace(callID) == "" {
						// Older Gemini responses may omit an ID. The engine still needs a
						// local correlation key, but this fallback is never sent back to
						// Gemini as though it were a provider-issued ID.
						callID = "call_" + strings.ReplaceAll(types.NewUUID(), "-", "")
					}
					toolCalls = append(toolCalls, types.ToolCall{ID: callID, Name: canonical, Args: args})
				}
			}
		}

		meta := terminalMeta()
		if !terminalSeen {
			send(ctx, out, types.StreamEvent{Event: "error", Data: types.ErrorData{
				Code: "STREAM_ENDED", Message: "Gemini stream ended before finishReason", Retryable: true, Meta: &meta,
			}})
			return
		}
		providerPayload := encodeGeminiProviderPayload(p.providerID, providerRole, providerParts)

		if len(toolCalls) > 0 {
			send(ctx, out, types.StreamEvent{Event: "tool_calls", Data: types.ToolCallsData{ToolCalls: toolCalls, Meta: meta, ProviderPayload: providerPayload}})
			return
		}

		answer := accumulatedText
		send(ctx, out, types.StreamEvent{Event: "final", Data: types.FinalData{Answer: answer, Meta: meta, ProviderPayload: providerPayload}})
	}()

	return out, nil
}

func send(ctx context.Context, ch chan<- types.StreamEvent, ev types.StreamEvent) {
	select {
	case ch <- ev:
	case <-ctx.Done():
	}
}

func toolsToGemini(tools []types.ToolDefinition, mapper *toolnamemapper.Mapper) []any {
	out := make([]any, 0, len(tools))
	for _, t := range tools {
		out = append(out, map[string]any{
			"name":        mapper.ProviderName(t.Function.Name),
			"description": t.Function.Description,
			"parameters":  t.Function.Parameters,
		})
	}
	return out
}

func parseToolResponse(content *string) map[string]any {
	raw := ""
	if content != nil {
		raw = strings.TrimSpace(*content)
	}
	if raw == "" {
		return map[string]any{}
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return map[string]any{"result": raw}
	}
	if obj, ok := parsed.(map[string]any); ok {
		return obj
	}
	return map[string]any{"result": parsed}
}

func messagesToGemini(req types.ChatRequest, mapper *toolnamemapper.Mapper) []any {
	return messagesToGeminiForProvider(req, mapper, "")
}

func messagesToGeminiForProvider(req types.ChatRequest, mapper *toolnamemapper.Mapper, providerID string) []any {
	contents := make([]any, 0, len(req.Messages))
	providerFunctionCallIDs := make(map[string]struct{})
	for msgIndex := 0; msgIndex < len(req.Messages); {
		msg := req.Messages[msgIndex]
		if msg.Role == types.RoleSystem {
			msgIndex++
			continue
		}
		if msg.Role == types.RoleUser {
			text := ""
			if msg.Content != nil {
				text = types.SafeTruncate(*msg.Content)
			}
			fileParts := geminiFileParts(msg.FileAttachments)
			parts := make([]any, 0, 1+len(fileParts)+len(msg.ImageAttachments))
			parts = append(parts, fileParts...)
			if text != "" {
				parts = append(parts, map[string]any{"text": text})
			}
			images := vision.NormalizeAttachments(msg.ImageAttachments)
			for _, img := range images {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": img.MIMEType,
						"data":     img.Base64Data,
					},
				})
			}
			if len(parts) == 0 {
				msgIndex++
				continue
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
			msgIndex++
			continue
		}
		if msg.Role == types.RoleAssistant {
			if content, ok := decodeGeminiProviderContent(msg.ProviderPayload, providerID); ok {
				contents = append(contents, content)
				for id := range geminiFunctionCallIDs(content) {
					providerFunctionCallIDs[id] = struct{}{}
				}
				msgIndex++
				continue
			}
			parts := make([]any, 0)
			if msg.Content != nil {
				text := types.SafeTruncate(*msg.Content)
				if text != "" {
					parts = append(parts, map[string]any{"text": text})
				}
			}
			for _, call := range msg.ToolCalls {
				parts = append(parts, map[string]any{"functionCall": map[string]any{"name": mapper.ProviderName(call.Name), "args": call.Args}})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "model", "parts": parts})
			}
			msgIndex++
			continue
		}
		if msg.Role == types.RoleTool {
			// Gemini requires function responses under role=user. Keep consecutive
			// tool results in one Content so parallel calls remain ordered as all
			// functionCall Parts followed by all matching functionResponse Parts.
			parts := make([]any, 0)
			for msgIndex < len(req.Messages) && req.Messages[msgIndex].Role == types.RoleTool {
				toolMsg := req.Messages[msgIndex]
				msgIndex++
				if strings.TrimSpace(toolMsg.Name) == "" {
					continue
				}
				functionResponse := map[string]any{
					"name":     mapper.ProviderName(toolMsg.Name),
					"response": parseToolResponse(toolMsg.Content),
				}
				if _, ok := providerFunctionCallIDs[toolMsg.ToolCallID]; ok {
					// Echo only IDs observed in provider-native functionCall Parts. This
					// preserves Gemini 3 IDs exactly and avoids leaking locally generated
					// legacy correlation IDs into the wire protocol.
					functionResponse["id"] = toolMsg.ToolCallID
				}
				parts = append(parts, map[string]any{"functionResponse": functionResponse})
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{"role": "user", "parts": parts})
			}
			continue
		}
		msgIndex++
	}
	return contents
}

func encodeGeminiProviderPayload(providerID, role string, parts []any) json.RawMessage {
	if len(parts) == 0 {
		return nil
	}
	role = strings.TrimSpace(role)
	if role == "" {
		role = "model"
	}
	payload := map[string]any{
		"format":      "gemini_generate_content_v1",
		"provider_id": strings.TrimSpace(providerID),
		"api_type":    "generate_content",
		"content": map[string]any{
			"role":  role,
			"parts": parts,
		},
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return nil
	}
	return b
}

func decodeGeminiProviderContent(raw json.RawMessage, providerID string) (map[string]any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var payload struct {
		Format     string         `json:"format"`
		ProviderID string         `json:"provider_id"`
		APIType    string         `json:"api_type"`
		Content    map[string]any `json:"content"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false
	}
	if strings.TrimSpace(payload.Format) != "gemini_generate_content_v1" {
		return nil, false
	}
	if apiType := strings.TrimSpace(payload.APIType); apiType != "" && apiType != "generate_content" {
		return nil, false
	}
	if providerID != "" && strings.TrimSpace(payload.ProviderID) != "" && strings.TrimSpace(payload.ProviderID) != strings.TrimSpace(providerID) {
		return nil, false
	}
	role, _ := payload.Content["role"].(string)
	parts, _ := payload.Content["parts"].([]any)
	if strings.TrimSpace(role) != "model" || len(parts) == 0 {
		return nil, false
	}
	for _, part := range parts {
		if _, ok := part.(map[string]any); !ok {
			return nil, false
		}
	}
	return map[string]any{"role": role, "parts": parts}, true
}

func geminiFunctionCallIDs(content map[string]any) map[string]struct{} {
	ids := make(map[string]struct{})
	parts, _ := content["parts"].([]any)
	for _, rawPart := range parts {
		part, _ := rawPart.(map[string]any)
		functionCall, _ := part["functionCall"].(map[string]any)
		id, _ := functionCall["id"].(string)
		if strings.TrimSpace(id) != "" {
			ids[id] = struct{}{}
		}
	}
	return ids
}

func geminiFileParts(attachments []types.FileAttachment) []any {
	files := types.CloneFileAttachments(attachments)
	if len(files) == 0 {
		return nil
	}
	out := make([]any, 0, len(files))
	for _, file := range files {
		data := geminiAttachmentBase64(file.InlineDataBase64)
		if data == "" || !geminiNativeFileCandidate(file) {
			continue
		}
		mimeType := geminiAttachmentMIME(file)
		if imageMIME := geminiImageMIME(mimeType); imageMIME != "" {
			mimeType = imageMIME
		}
		out = append(out, map[string]any{
			"inlineData": map[string]any{
				"mimeType": mimeType,
				"data":     data,
			},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func geminiAttachmentBase64(data string) string {
	data = strings.TrimSpace(data)
	if data == "" {
		return ""
	}
	if !strings.HasPrefix(strings.ToLower(data), "data:") {
		return data
	}
	if comma := strings.Index(data, ","); comma >= 0 && comma+1 < len(data) {
		return strings.TrimSpace(data[comma+1:])
	}
	return ""
}

func geminiNativeFileCandidate(file types.FileAttachment) bool {
	mimeType := geminiAttachmentMIME(file)
	if mimeType == "application/pdf" || geminiImageMIME(mimeType) != "" {
		return true
	}
	if file.Capabilities != nil && file.Capabilities["canUseNativeDocument"] {
		return true
	}
	if file.IsText != nil && *file.IsText {
		return true
	}
	switch strings.TrimSpace(strings.ToLower(file.FileKind)) {
	case "text", "csv", "json", "html", "document":
		return true
	}
	if geminiTextLikeMIME(geminiAttachmentMIME(file)) {
		return true
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(file.FileName))), ".") {
	case "pdf", "txt", "md", "markdown", "csv", "tsv", "tab", "json", "jsonl", "ndjson", "html", "htm", "xml",
		"yaml", "yml", "toml", "ini", "cfg", "conf", "log", "sql", "py", "go", "js", "jsx", "ts", "tsx", "rb", "rs", "java", "css", "scss", "sh", "bash", "zsh", "c", "h", "cpp", "hpp", "svg":
		return true
	default:
		return false
	}
}

func geminiAttachmentMIME(file types.FileAttachment) string {
	mimeType := firstNonEmptyString(
		file.NormalizedMIME,
		file.MimeType,
		file.DetectedMimeType,
		file.DeclaredMIME,
		file.ExtensionMIME,
		file.SniffedMIME,
	)
	if cleaned := strings.TrimSpace(strings.ToLower(mimeType)); cleaned != "" && cleaned != "application/octet-stream" {
		return cleaned
	}
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(fileExt(file.FileName))), ".") {
	case "pdf":
		return "application/pdf"
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	case "heic":
		return "image/heic"
	case "heif":
		return "image/heif"
	case "txt", "log", "ini", "cfg", "conf":
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
	case "tsv", "tab":
		return "text/tab-separated-values"
	case "yaml", "yml":
		return "application/yaml"
	case "toml":
		return "application/toml"
	case "js", "jsx", "mjs":
		return "application/javascript"
	case "ts", "tsx":
		return "application/typescript"
	case "css", "scss":
		return "text/css"
	case "sql":
		return "application/x-sql"
	case "svg":
		return "image/svg+xml"
	default:
		return "text/plain"
	}
}

func geminiImageMIME(mimeType string) string {
	switch strings.TrimSpace(strings.ToLower(mimeType)) {
	case "image/png":
		return "image/png"
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/webp":
		return "image/webp"
	case "image/gif":
		// The repository's upload manager currently classifies GIF as a native
		// Gemini image. Forward it here rather than silently dropping the bytes
		// after that manager-level decision has suppressed the preview fallback.
		return "image/gif"
	case "image/heic":
		return "image/heic"
	case "image/heif":
		return "image/heif"
	default:
		return ""
	}
}

func geminiTextLikeMIME(mimeType string) bool {
	mimeType = strings.TrimSpace(strings.ToLower(mimeType))
	if strings.HasPrefix(mimeType, "text/") || strings.HasSuffix(mimeType, "+json") || strings.HasSuffix(mimeType, "+xml") {
		return true
	}
	switch mimeType {
	case "application/json",
		"application/javascript",
		"application/typescript",
		"application/x-ndjson",
		"application/x-sh",
		"application/x-yaml",
		"application/xml",
		"application/toml",
		"application/yaml",
		"application/x-sql":
		return true
	default:
		return false
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func fileExt(name string) string {
	return strings.TrimSpace(strings.ToLower(filepath.Ext(strings.TrimSpace(name))))
}

func errorsIsContext(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "context canceled") || strings.Contains(msg, "deadline")
}
