package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/requestguard"
	"ai-analyst-local/internal/chat/providers/toolnamemapper"
	"ai-analyst-local/internal/chat/types"
)

func TestProvider_StreamToolCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1beta/models/test-model:streamGenerateContent" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("expected alt=sse, got %q", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-goog-request-id", "header-request-id")
		f, _ := w.(http.Flusher)

		// Sanity check body is JSON.
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, ok := body["contents"]; !ok {
			t.Errorf("expected contents in body, got %v", body)
		}

		chunk := `{"responseId":"gemini-response-id","modelVersion":"gemini-resolved-001","candidates":[{"content":{"parts":[{"functionCall":{"name":"my_tool","args":{"x":1}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3,"cachedContentTokenCount":1,"thoughtsTokenCount":1,"toolUsePromptTokenCount":2}}`
		_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	p := New("gemini", "test-model", srv.URL, map[string]string{})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := types.ChatRequest{
		Model:        "test-model",
		SystemPrompt: "",
		Messages:     []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hi")}},
		Tools: []types.ToolDefinition{
			{Type: "function", Function: types.ToolFunctionDefinition{Name: "my.tool", Parameters: map[string]any{"type": "object"}}},
		},
	}

	ch, err := p.StreamChat(ctx, req)
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var toolCalls *types.ToolCallsData
	for ev := range ch {
		switch ev.Event {
		case types.EventToolCalls:
			d, ok := ev.Data.(types.ToolCallsData)
			if !ok {
				t.Fatalf("tool_calls has unexpected data type: %T", ev.Data)
			}
			toolCalls = &d
		case types.EventError:
			t.Fatalf("unexpected error event: %#v", ev.Data)
		}
	}

	if toolCalls == nil {
		t.Fatalf("expected tool_calls event")
	}
	if len(toolCalls.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(toolCalls.ToolCalls))
	}
	if toolCalls.ToolCalls[0].Name != "my.tool" {
		t.Fatalf("expected canonical tool name my.tool, got %q", toolCalls.ToolCalls[0].Name)
	}
	if toolCalls.Meta.RequestID == nil || *toolCalls.Meta.RequestID != "gemini-response-id" {
		t.Fatalf("request id = %#v, want payload response id", toolCalls.Meta.RequestID)
	}
	if toolCalls.Meta.ResolvedModel == nil || *toolCalls.Meta.ResolvedModel != "gemini-resolved-001" || toolCalls.Meta.ProviderVersion == nil || *toolCalls.Meta.ProviderVersion != "gemini-resolved-001" {
		t.Fatalf("model/version metadata = %#v", toolCalls.Meta)
	}
	if toolCalls.Meta.CachedInputTokens == nil || *toolCalls.Meta.CachedInputTokens != 1 || toolCalls.Meta.ReasoningTokens == nil || *toolCalls.Meta.ReasoningTokens != 1 {
		t.Fatalf("extended usage = %#v", toolCalls.Meta)
	}
	if toolCalls.Meta.UsageDetails["tool_use_prompt_tokens"] != 2 || toolCalls.Meta.UsageQuality != types.UsageQualityProviderReported {
		t.Fatalf("usage details/quality = %#v", toolCalls.Meta)
	}
	if toolCalls.Meta.UpstreamRequestCount == nil || *toolCalls.Meta.UpstreamRequestCount != 1 {
		t.Fatalf("upstream request count = %#v", toolCalls.Meta.UpstreamRequestCount)
	}
	switch x := toolCalls.ToolCalls[0].Args["x"].(type) {
	case json.Number:
		if x.String() != "1" {
			t.Fatalf("unexpected tool args: %#v", toolCalls.ToolCalls[0].Args)
		}
	case float64:
		if x != 1 {
			t.Fatalf("unexpected tool args: %#v", toolCalls.ToolCalls[0].Args)
		}
	default:
		t.Fatalf("unexpected tool args type %T: %#v", toolCalls.ToolCalls[0].Args["x"], toolCalls.ToolCalls[0].Args)
	}
}

func TestProvider_Gemini3ContinuationPreservesPartsSignaturesIDsAndOrder(t *testing.T) {
	var requestNumber int32
	secondRequestBody := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		switch atomic.AddInt32(&requestNumber, 1) {
		case 1:
			chunks := []string{
				`{"candidates":[{"content":{"role":"model","parts":[{"text":"Preparing ","thoughtSignature":"text-signature"},{"functionCall":{"id":"provider-call-alpha","name":"first_tool","args":{"x":1}},"thoughtSignature":"signature-alpha"}]}}]}`,
				`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"provider-call-beta","name":"second_tool","args":{"y":2}}},{"text":"","thoughtSignature":"tail-signature","customMetadata":{"keep":true}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":5,"totalTokenCount":9}}`,
			}
			for _, chunk := range chunks {
				_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
				f.Flush()
			}
		case 2:
			secondRequestBody <- body
			chunk := `{"candidates":[{"content":{"role":"model","parts":[{"text":"Finished","thoughtSignature":"final-signature"}]},"finishReason":"STOP"}]}`
			_, _ = w.Write([]byte("data: " + chunk + "\n\n"))
			f.Flush()
		default:
			t.Errorf("unexpected request number %d", atomic.LoadInt32(&requestNumber))
			http.Error(w, "unexpected request", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	p := New("gemini", "gemini-3-test", srv.URL, map[string]string{})
	tools := []types.ToolDefinition{
		{Type: "function", Function: types.ToolFunctionDefinition{Name: "first_tool", Parameters: map[string]any{"type": "object"}}},
		{Type: "function", Function: types.ToolFunctionDefinition{Name: "second_tool", Parameters: map[string]any{"type": "object"}}},
	}
	initialUser := types.ChatMessage{Role: types.RoleUser, Content: ptr("run both tools")}

	firstStream, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model:    "gemini-3-test",
		Messages: []types.ChatMessage{initialUser},
		Tools:    tools,
	})
	if err != nil {
		t.Fatalf("first StreamChat: %v", err)
	}

	var firstToolData *types.ToolCallsData
	for ev := range firstStream {
		switch ev.Event {
		case types.EventToolCalls:
			data, ok := ev.Data.(types.ToolCallsData)
			if !ok {
				t.Fatalf("tool_calls data type = %T", ev.Data)
			}
			firstToolData = &data
		case types.EventError:
			t.Fatalf("first request error: %#v", ev.Data)
		}
	}
	if firstToolData == nil {
		t.Fatal("first request did not return tool calls")
	}
	if got := len(firstToolData.ToolCalls); got != 2 {
		t.Fatalf("tool call count = %d, want 2", got)
	}
	if got := firstToolData.ToolCalls[0].ID; got != "provider-call-alpha" {
		t.Fatalf("first tool ID = %q", got)
	}
	if got := firstToolData.ToolCalls[1].ID; got != "provider-call-beta" {
		t.Fatalf("second tool ID = %q", got)
	}
	if len(firstToolData.ProviderPayload) == 0 {
		t.Fatal("tool_calls event omitted provider payload")
	}

	assistant := types.ChatMessage{
		Role:            types.RoleAssistant,
		Content:         ptr("Preparing "),
		ToolCalls:       firstToolData.ToolCalls,
		ProviderPayload: firstToolData.ProviderPayload,
	}
	secondStream, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gemini-3-test",
		Messages: []types.ChatMessage{
			initialUser,
			assistant,
			{Role: types.RoleTool, ToolCallID: "provider-call-alpha", Name: "first_tool", Content: ptr(`{"value":"A"}`)},
			{Role: types.RoleTool, ToolCallID: "provider-call-beta", Name: "second_tool", Content: ptr(`{"value":"B"}`)},
		},
		Tools: tools,
	})
	if err != nil {
		t.Fatalf("continuation StreamChat: %v", err)
	}
	var finalData *types.FinalData
	for ev := range secondStream {
		switch ev.Event {
		case types.EventFinal:
			data, ok := ev.Data.(types.FinalData)
			if !ok {
				t.Fatalf("final data type = %T", ev.Data)
			}
			finalData = &data
		case types.EventError:
			t.Fatalf("continuation request error: %#v", ev.Data)
		}
	}
	if finalData == nil || finalData.Answer != "Finished" {
		t.Fatalf("final data = %#v", finalData)
	}
	if content, ok := decodeGeminiProviderContent(finalData.ProviderPayload, "gemini"); !ok {
		t.Fatal("final event omitted replayable provider content")
	} else {
		parts := content["parts"].([]any)
		if got := parts[0].(map[string]any)["thoughtSignature"]; got != "final-signature" {
			t.Fatalf("final thoughtSignature = %#v", got)
		}
	}

	var secondBody map[string]any
	select {
	case secondBody = <-secondRequestBody:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for continuation request body")
	}
	contents, ok := secondBody["contents"].([]any)
	if !ok || len(contents) != 3 {
		t.Fatalf("continuation contents = %#v, want user/model/user", secondBody["contents"])
	}

	modelContent := contents[1].(map[string]any)
	if got := modelContent["role"]; got != "model" {
		t.Fatalf("replayed assistant role = %#v", got)
	}
	parts := modelContent["parts"].([]any)
	if len(parts) != 4 {
		t.Fatalf("replayed model part count = %d, want 4: %#v", len(parts), parts)
	}
	if got := parts[0].(map[string]any)["thoughtSignature"]; got != "text-signature" {
		t.Fatalf("part 0 signature = %#v", got)
	}
	firstFunctionPart := parts[1].(map[string]any)
	if got := firstFunctionPart["thoughtSignature"]; got != "signature-alpha" {
		t.Fatalf("part 1 signature = %#v", got)
	}
	if got := firstFunctionPart["functionCall"].(map[string]any)["id"]; got != "provider-call-alpha" {
		t.Fatalf("part 1 function ID = %#v", got)
	}
	secondFunctionPart := parts[2].(map[string]any)
	if _, exists := secondFunctionPart["thoughtSignature"]; exists {
		t.Fatalf("part 2 unexpectedly gained a signature: %#v", secondFunctionPart)
	}
	if got := secondFunctionPart["functionCall"].(map[string]any)["id"]; got != "provider-call-beta" {
		t.Fatalf("part 2 function ID = %#v", got)
	}
	tailPart := parts[3].(map[string]any)
	if got := tailPart["thoughtSignature"]; got != "tail-signature" {
		t.Fatalf("part 3 signature = %#v", got)
	}
	if got := tailPart["text"]; got != "" {
		t.Fatalf("part 3 empty text was not preserved: %#v", got)
	}
	if got := tailPart["customMetadata"].(map[string]any)["keep"]; got != true {
		t.Fatalf("provider-native part metadata was not preserved: %#v", tailPart)
	}

	responseContent := contents[2].(map[string]any)
	if got := responseContent["role"]; got != "user" {
		t.Fatalf("functionResponse role = %#v, want user", got)
	}
	responseParts := responseContent["parts"].([]any)
	if len(responseParts) != 2 {
		t.Fatalf("functionResponse part count = %d, want 2", len(responseParts))
	}
	wantResponses := []map[string]any{
		{"name": "first_tool", "id": "provider-call-alpha", "response": map[string]any{"value": "A"}},
		{"name": "second_tool", "id": "provider-call-beta", "response": map[string]any{"value": "B"}},
	}
	for i, want := range wantResponses {
		got := responseParts[i].(map[string]any)["functionResponse"]
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("functionResponse %d = %#v, want %#v", i, got, want)
		}
	}
}

func TestMessagesToGemini_DoesNotEchoLocallyGeneratedLegacyCallID(t *testing.T) {
	mapper := toolnamemapper.New(64)
	payload := encodeGeminiProviderPayload("gemini", "model", []any{
		map[string]any{"functionCall": map[string]any{"name": "legacy_tool", "args": map[string]any{}}},
	})
	contents := messagesToGeminiForProvider(types.ChatRequest{Messages: []types.ChatMessage{
		{Role: types.RoleAssistant, ProviderPayload: payload, ToolCalls: []types.ToolCall{{ID: "call_local_fallback", Name: "legacy_tool"}}},
		{Role: types.RoleTool, ToolCallID: "call_local_fallback", Name: "legacy_tool", Content: ptr(`{"ok":true}`)},
	}}, mapper, "gemini")

	if len(contents) != 2 {
		t.Fatalf("contents count = %d, want 2", len(contents))
	}
	responseContent := contents[1].(map[string]any)
	functionResponse := responseContent["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if _, exists := functionResponse["id"]; exists {
		t.Fatalf("locally generated fallback ID leaked into Gemini request: %#v", functionResponse)
	}
}

func TestProvider_RequestTooLargeFailsBeforeDispatch(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "request should not be dispatched", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New("gemini", "test-model", srv.URL, map[string]string{})
	p.maxRequestBytes = 1

	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model:    "test-model",
		Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hello")}},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var got *types.ErrorData
	for ev := range ch {
		if ev.Event != types.EventError {
			continue
		}
		d, ok := ev.Data.(types.ErrorData)
		if !ok {
			t.Fatalf("error event has unexpected data type: %T", ev.Data)
		}
		got = &d
	}

	if got == nil {
		t.Fatalf("expected request size error")
	}
	if got.Code != requestguard.ErrorCodeRequestTooLarge {
		t.Fatalf("error code = %q, want %q; message=%q", got.Code, requestguard.ErrorCodeRequestTooLarge, got.Message)
	}
	if !strings.Contains(got.Message, "max_bytes=1") || !strings.Contains(got.Message, "/v1beta/models/test-model:streamGenerateContent") {
		t.Fatalf("error message missing guard context: %q", got.Message)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("server saw %d requests, want 0", calls)
	}
}

func TestProviderCapabilities_ReportsNoNativeCompaction(t *testing.T) {
	p := New("gemini", "gemini-2.5-pro", "https://generativelanguage.googleapis.com", map[string]string{})

	caps := p.Capabilities()
	if caps.NativeCompaction.Mode != capabilities.NativeCompactionNone {
		t.Fatalf("native compaction mode = %q", caps.NativeCompaction.Mode)
	}
	if caps.NativeCompaction.Enabled {
		t.Fatalf("gemini should not report native compaction enabled")
	}
	if caps.SupportsPreviousResponseID {
		t.Fatalf("gemini should not report previous_response_id support")
	}
	if caps.OpaqueCompactionPreservation.ProviderPayload || caps.OpaqueCompactionPreservation.ProviderState {
		t.Fatalf("gemini should not report opaque compaction preservation, got %#v", caps.OpaqueCompactionPreservation)
	}
	if caps.MaxInputTokens <= 0 || caps.MaxOutputTokens <= 0 || caps.InputSafetyReserveTokens <= 0 || caps.OutputSafetyReserveTokens <= 0 {
		t.Fatalf("expected token limits and safety reserves, got %#v", caps)
	}
}

func ptr(s string) *string { return &s }

func TestMessagesToGemini_EncodesInlineImages(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("analyze"),
				ImageAttachments: []types.ImageAttachment{
					{
						FileName: "mockup.png",
						MimeType: "image/png",
						DataURI:  "data:image/png;base64,AAAA",
					},
				},
			},
		},
	}

	out := messagesToGemini(req, mapper)
	if len(out) != 1 {
		t.Fatalf("expected 1 user content, got %d", len(out))
	}
	msg, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected content type: %T", out[0])
	}
	parts, ok := msg["parts"].([]any)
	if !ok {
		t.Fatalf("expected parts array, got %T", msg["parts"])
	}
	if len(parts) != 2 {
		t.Fatalf("expected text + image parts, got %d", len(parts))
	}
	imagePart, ok := parts[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image part type: %T", parts[1])
	}
	inlineData, ok := imagePart["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("expected inlineData payload, got %#v", imagePart)
	}
	if mime, _ := inlineData["mimeType"].(string); mime != "image/png" {
		t.Fatalf("unexpected mimeType: %q", mime)
	}
	if data, _ := inlineData["data"].(string); data != "AAAA" {
		t.Fatalf("unexpected base64 data: %q", data)
	}
}

func TestMessagesToGemini_EncodesNativeFileParts(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("summarize"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
					},
				},
			},
		},
	}

	out := messagesToGemini(req, mapper)
	if len(out) != 1 {
		t.Fatalf("expected 1 user content, got %d", len(out))
	}
	msg, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected content type: %T", out[0])
	}
	parts, ok := msg["parts"].([]any)
	if !ok {
		t.Fatalf("expected parts array, got %T", msg["parts"])
	}
	if len(parts) != 2 {
		t.Fatalf("expected file + text parts, got %d: %#v", len(parts), parts)
	}
	filePart, ok := parts[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected file part type: %T", parts[0])
	}
	inlineData, ok := filePart["inlineData"].(map[string]any)
	if !ok {
		t.Fatalf("expected inlineData payload, got %#v", filePart)
	}
	if mime, _ := inlineData["mimeType"].(string); mime != "application/pdf" {
		t.Fatalf("unexpected mimeType: %q", mime)
	}
	if data, _ := inlineData["data"].(string); data != "JVBERi0xLjQ=" {
		t.Fatalf("unexpected base64 data: %q", data)
	}
}

func TestMessagesToGemini_EncodesUploadedImagesFromFileAttachmentPath(t *testing.T) {
	tests := []struct {
		name       string
		fileName   string
		mimeType   string
		inlineData string
		wantMIME   string
		wantData   string
	}{
		{name: "png data URI", fileName: "chart.png", mimeType: "image/png", inlineData: "data:image/png;base64,AAAA", wantMIME: "image/png", wantData: "AAAA"},
		{name: "jpeg alias", fileName: "photo.jpg", mimeType: "image/jpg", inlineData: "BBBB", wantMIME: "image/jpeg", wantData: "BBBB"},
		{name: "webp", fileName: "screen.webp", mimeType: "image/webp", inlineData: "CCCC", wantMIME: "image/webp", wantData: "CCCC"},
		{name: "repository gif upload", fileName: "animation.gif", mimeType: "image/gif", inlineData: "DDDD", wantMIME: "image/gif", wantData: "DDDD"},
		{name: "heic", fileName: "camera.heic", mimeType: "image/heic", inlineData: "EEEE", wantMIME: "image/heic", wantData: "EEEE"},
		{name: "heif by extension", fileName: "camera.heif", mimeType: "application/octet-stream", inlineData: "FFFF", wantMIME: "image/heif", wantData: "FFFF"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := toolnamemapper.New(64)
			out := messagesToGemini(types.ChatRequest{Messages: []types.ChatMessage{{
				Role:    types.RoleUser,
				Content: ptr("inspect upload"),
				FileAttachments: []types.FileAttachment{{
					ArtifactID:       "artifact-image",
					FileName:         tt.fileName,
					MimeType:         tt.mimeType,
					InlineDataBase64: tt.inlineData,
				}},
			}}}, mapper)

			if len(out) != 1 {
				t.Fatalf("content count = %d, want 1", len(out))
			}
			parts := out[0].(map[string]any)["parts"].([]any)
			if len(parts) != 2 {
				t.Fatalf("parts count = %d, want uploaded image + text: %#v", len(parts), parts)
			}
			inlineData := parts[0].(map[string]any)["inlineData"].(map[string]any)
			if got := inlineData["mimeType"]; got != tt.wantMIME {
				t.Fatalf("mimeType = %#v, want %q", got, tt.wantMIME)
			}
			if got := inlineData["data"]; got != tt.wantData {
				t.Fatalf("data = %#v, want %q", got, tt.wantData)
			}
		})
	}
}
