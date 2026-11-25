package openai_compat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/requestguard"
	"ai-analyst-local/internal/chat/providers/toolnamemapper"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

func TestProvider_ChatCompletions_ToolCallsAndNameMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		f, _ := w.(http.Flusher)

		// Optional sanity check: request is JSON and streaming.
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if stream, _ := body["stream"].(bool); !stream {
			t.Errorf("expected stream=true, got body=%v", body)
		}

		chunk1 := `{"id":"req1","choices":[{"delta":{"content":"Hello "}}]}`
		chunk2 := `{"choices":[{"delta":{"content":"world","tool_calls":[{"index":0,"id":"call_1","function":{"name":"my_tool","arguments":"{\"x\":1"}}]}}]}`
		chunk3 := `{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"}"}}]}}]}`

		_, _ = w.Write([]byte("data: " + chunk1 + "\n\n"))
		f.Flush()
		_, _ = w.Write([]byte("data: " + chunk2 + "\n\n"))
		f.Flush()
		_, _ = w.Write([]byte("data: " + chunk3 + "\n\n"))
		f.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	prof := Profile{
		ProviderID:   "compatible",
		APIType:      APIChatCompletions,
		BaseURL:      srv.URL,
		ExtraHeaders: map[string]string{},
		ModelInBody:  true,
		ModelInPath:  false,
	}
	p := New("compatible", "test-model", prof)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := types.ChatRequest{
		Model:        "test-model",
		SystemPrompt: "",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("hi")},
		},
		Tools: []types.ToolDefinition{
			{
				Type: "function",
				Function: types.ToolFunctionDefinition{
					Name:       "my.tool", // canonical name contains a disallowed char; should map to provider name "my_tool".
					Parameters: map[string]any{"type": "object", "properties": map[string]any{}},
				},
			},
		},
	}

	ch, err := p.StreamChat(ctx, req)
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) < 2 {
		t.Fatalf("expected >=2 events, got %d: %#v", len(events), events)
	}

	// Expect some text deltas.
	var gotText string
	for _, ev := range events {
		if ev.Event != types.EventTextDelta {
			continue
		}
		d, ok := ev.Data.(types.TextDeltaData)
		if !ok {
			t.Fatalf("text_delta has unexpected data type: %T", ev.Data)
		}
		gotText += d.Content
	}
	if gotText != "Hello world" {
		t.Fatalf("unexpected accumulated text: %q", gotText)
	}

	// Terminal should be tool_calls.
	last := events[len(events)-1]
	if last.Event != types.EventToolCalls {
		t.Fatalf("expected terminal tool_calls, got %q (%#v)", last.Event, last)
	}
	tc, ok := last.Data.(types.ToolCallsData)
	if !ok {
		t.Fatalf("tool_calls has unexpected data type: %T", last.Data)
	}
	if len(tc.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tc.ToolCalls))
	}
	call := tc.ToolCalls[0]
	if call.ID != "call_1" {
		t.Fatalf("unexpected tool call id: %q", call.ID)
	}
	if call.Name != "my.tool" {
		t.Fatalf("expected canonical name my.tool, got %q", call.Name)
	}
	if call.ArgsText != "{\"x\":1}" {
		t.Fatalf("unexpected argsText: %q", call.ArgsText)
	}
	if x, ok := call.Args["x"].(float64); !ok || x != 1 {
		t.Fatalf("unexpected args: %#v", call.Args)
	}
}

func TestProvider_ChatCompletionsUsageFallbackAndNormalization(t *testing.T) {
	var attempts atomic.Int32
	var requestBodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			return
		}
		requestBodies = append(requestBodies, body)
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unrecognized request argument supplied: stream_options.include_usage"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-request-id", "req-header")
		w.Header().Set("openai-version", "2026-06-01")
		_, _ = w.Write([]byte("data: " + `{"id":"chatcmpl_1","model":"resolved-model","system_fingerprint":"fp_1","choices":[{"delta":{"content":"ok"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: " + `{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":2,"audio_tokens":1},"completion_tokens_details":{"reasoning_tokens":1,"accepted_prediction_tokens":2}}}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	enabled := true
	p := New("compatible", "requested-model", Profile{
		ProviderID: "compatible", APIType: APIChatCompletions, BaseURL: srv.URL, ModelInBody: true,
		IncludeStreamUsage: &enabled, UsageSemantics: types.UsageSemanticsOpenAI,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model:    "requested-model",
		Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hello")}},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var final *types.FinalData
	for event := range ch {
		if event.Event == types.EventError {
			t.Fatalf("unexpected error: %#v", event.Data)
		}
		if event.Event == types.EventFinal {
			data := event.Data.(types.FinalData)
			final = &data
		}
	}
	if len(requestBodies) != 2 {
		t.Fatalf("requests = %d, want 2", len(requestBodies))
	}
	firstOptions, _ := requestBodies[0]["stream_options"].(map[string]any)
	if include, _ := firstOptions["include_usage"].(bool); !include {
		t.Fatalf("first request stream_options = %#v, want include_usage=true", requestBodies[0]["stream_options"])
	}
	if _, ok := requestBodies[1]["stream_options"]; ok {
		t.Fatalf("fallback request unexpectedly retained stream_options: %#v", requestBodies[1])
	}
	if final == nil {
		t.Fatal("expected final event")
	}
	meta := final.Meta
	if meta.InputTokens == nil || *meta.InputTokens != 4 || meta.OutputTokens == nil || *meta.OutputTokens != 3 || meta.TotalTokens == nil || *meta.TotalTokens != 7 {
		t.Fatalf("unexpected normalized usage: %#v", meta)
	}
	if meta.CachedInputTokens == nil || *meta.CachedInputTokens != 2 || meta.ReasoningTokens == nil || *meta.ReasoningTokens != 1 {
		t.Fatalf("unexpected usage details: %#v", meta)
	}
	if meta.UsageQuality != types.UsageQualityDerived || meta.DerivedTotalTokens == nil || *meta.DerivedTotalTokens != 7 {
		t.Fatalf("usage quality/derived total = %q/%v, want derived/7", meta.UsageQuality, meta.DerivedTotalTokens)
	}
	if meta.RequestID == nil || *meta.RequestID != "req-header" || meta.ResolvedModel == nil || *meta.ResolvedModel != "resolved-model" {
		t.Fatalf("unexpected response identity: %#v", meta)
	}
	if meta.ProviderVersion == nil || *meta.ProviderVersion != "2026-06-01" || meta.UpstreamRequestCount == nil || *meta.UpstreamRequestCount != 2 {
		t.Fatalf("unexpected version/request count: %#v", meta)
	}
}

func TestBuildBodyStreamUsageCannotBeEnabledByRawOverride(t *testing.T) {
	mapper := toolnamemapper.New(0)
	request := types.ChatRequest{Model: "generic-model", Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hello")}}}
	profile := Profile{
		ProviderID: "compatible", APIType: APIChatCompletions, ModelInBody: true,
		UsageSemantics:   types.UsageSemanticsGeneric,
		PayloadOverrides: map[string]any{"stream_options": map[string]any{"include_usage": true, "other": "kept"}},
	}
	body, err := buildBody(profile, request.Model, request, mapper)
	if err != nil {
		t.Fatalf("buildBody: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	streamOptions, _ := decoded["stream_options"].(map[string]any)
	if _, enabled := streamOptions["include_usage"]; enabled || streamOptions["other"] != "kept" {
		t.Fatalf("normalized stream_options=%#v", streamOptions)
	}
}

func TestProvider_ResponsesRequiresExplicitCompletedEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n"))
		_, _ = w.Write([]byte("event: response.in_progress\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.in_progress","response":{"usage":{"input_tokens":5,"output_tokens":1,"total_tokens":6}}}` + "\n\n"))
		// Clean transport EOF without response.completed is still incomplete.
	}))
	defer srv.Close()

	p := New("openai", "gpt-test", Profile{
		ProviderID: "openai", APIType: APIResponses, BaseURL: srv.URL, ModelInBody: true,
		UsageSemantics: types.UsageSemanticsOpenAI,
	})
	stream, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-test", Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hello")}},
	})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var sawFinal bool
	var terminal *types.ErrorData
	for event := range stream {
		if event.Event == types.EventFinal {
			sawFinal = true
		}
		if event.Event == types.EventError {
			data, _ := event.Data.(types.ErrorData)
			terminal = &data
		}
	}
	if sawFinal || terminal == nil || terminal.Meta == nil || !strings.Contains(terminal.Message, "response.completed") {
		t.Fatalf("truncated responses terminal final=%v error=%#v", sawFinal, terminal)
	}
}

func TestProvider_RequestTooLargeFailsBeforeDispatch(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		http.Error(w, "request should not be dispatched", http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := New("compatible", "test-model", Profile{
		ProviderID:      "compatible",
		APIType:         APIChatCompletions,
		BaseURL:         srv.URL,
		ExtraHeaders:    map[string]string{},
		ModelInBody:     true,
		ModelInPath:     false,
		MaxRequestBytes: 1,
	})

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
	if !strings.Contains(got.Message, "max_bytes=1") || !strings.Contains(got.Message, "/chat/completions") {
		t.Fatalf("error message missing guard context: %q", got.Message)
	}
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatalf("server saw %d requests, want 0", calls)
	}
}

func TestProvider_ContextWindowHTTPErrorIsNormalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130000 tokens."}}`))
	}))
	defer srv.Close()

	p := New("compatible", "test-model", Profile{
		ProviderID:   "compatible",
		APIType:      APIChatCompletions,
		BaseURL:      srv.URL,
		ExtraHeaders: map[string]string{},
		ModelInBody:  true,
	})

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
		t.Fatalf("expected context-window error")
	}
	if got.Code != requestguard.ErrorCodeContextOverflow {
		t.Fatalf("error code = %q, want %q; message=%q", got.Code, requestguard.ErrorCodeContextOverflow, got.Message)
	}
	if got.Retryable {
		t.Fatalf("retryable = true, want false")
	}
}

func TestProvider_Responses_Final(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi\",\"id\":\"rid-stream\"}\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"rid-final\",\"usage\":{\"input_tokens\":1,\"output_tokens\":2,\"total_tokens\":3},\"output_text\":\"Hi\"}}\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	prof := Profile{
		ProviderID:   "compatible",
		APIType:      APIResponses,
		BaseURL:      srv.URL,
		ExtraHeaders: map[string]string{},
		ModelInBody:  true,
		ModelInPath:  false,
	}
	p := New("compatible", "test-model", prof)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req := types.ChatRequest{
		Model:        "test-model",
		SystemPrompt: "You are helpful.",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("hello")},
		},
		Tools: nil,
	}

	ch, err := p.StreamChat(ctx, req)
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if len(events) < 2 {
		t.Fatalf("expected >=2 events, got %d: %#v", len(events), events)
	}

	if events[0].Event != types.EventTextDelta {
		t.Fatalf("expected first event text_delta, got %q", events[0].Event)
	}

	last := events[len(events)-1]
	if last.Event != types.EventFinal {
		t.Fatalf("expected terminal final, got %q (%#v)", last.Event, last)
	}
	fd, ok := last.Data.(types.FinalData)
	if !ok {
		t.Fatalf("final has unexpected data type: %T", last.Data)
	}
	if fd.Answer != "Hi" {
		t.Fatalf("unexpected final answer: %q", fd.Answer)
	}
	if fd.Meta.RequestID == nil || *fd.Meta.RequestID != "rid-stream" {
		// StreamChat sets requestID from the first payload.id it sees (matches frontend behavior).
		t.Fatalf("unexpected requestId: %#v", fd.Meta.RequestID)
	}
	if fd.Meta.InputTokens == nil || *fd.Meta.InputTokens != 1 {
		t.Fatalf("unexpected inputTokens: %#v", fd.Meta.InputTokens)
	}
	if fd.Meta.OutputTokens == nil || *fd.Meta.OutputTokens != 2 {
		t.Fatalf("unexpected outputTokens: %#v", fd.Meta.OutputTokens)
	}
	if fd.Meta.TotalTokens == nil || *fd.Meta.TotalTokens != 3 {
		t.Fatalf("unexpected totalTokens: %#v", fd.Meta.TotalTokens)
	}
	if fd.Meta.ProviderState == nil {
		t.Fatalf("expected provider state")
	}
	if got, _ := fd.Meta.ProviderState["previous_response_id"].(string); got != "rid-final" {
		t.Fatalf("unexpected provider state: %#v", fd.Meta.ProviderState)
	}
	if len(fd.ProviderPayload) == 0 {
		t.Fatalf("expected provider payload")
	}
}

func TestProvider_Responses_FailurePreservesReportedUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: response.failed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.failed","response":{"id":"resp_failed","model":"resolved-model","usage":{"input_tokens":9,"output_tokens":2,"total_tokens":11,"input_tokens_details":{"cached_tokens":4}}},"error":{"message":"upstream stopped"}}` + "\n\n"))
	}))
	defer srv.Close()

	p := New("openai", "requested-model", Profile{
		ProviderID:  "openai",
		APIType:     APIResponses,
		BaseURL:     srv.URL,
		ModelInBody: true,
	})
	stream, err := p.StreamChat(context.Background(), types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	var terminal *types.ErrorData
	for event := range stream {
		if event.Event == types.EventError {
			data := event.Data.(types.ErrorData)
			terminal = &data
		}
	}
	if terminal == nil || terminal.Meta == nil {
		t.Fatal("missing terminal error metadata")
	}
	meta := terminal.Meta
	if meta.RequestID == nil || *meta.RequestID != "resp_failed" || meta.ResolvedModel == nil || *meta.ResolvedModel != "resolved-model" {
		t.Fatalf("identity metadata = %#v", meta)
	}
	if meta.InputTokens == nil || *meta.InputTokens != 9 || meta.OutputTokens == nil || *meta.OutputTokens != 2 || meta.TotalTokens == nil || *meta.TotalTokens != 11 {
		t.Fatalf("usage metadata = %#v", meta)
	}
	if meta.CachedInputTokens == nil || *meta.CachedInputTokens != 4 {
		t.Fatalf("cached usage metadata = %#v", meta)
	}
}

func TestProvider_Responses_WebSearchCallSurfacesAsProviderTool(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"searching","action":{"type":"search","query":"AAPL current price"}}}` + "\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"AAPL is trading.","id":"resp_stream"}` + "\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_final","output_text":"AAPL is trading.","output":[{"type":"web_search_call","id":"ws_1","status":"completed","action":{"type":"search","query":"AAPL current price"}},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"AAPL is trading.","annotations":[{"type":"url_citation","title":"Apple quote","url":"https://example.com/aapl"}]}]}]}}` + "\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	prof := Profile{
		ProviderID:   "compatible",
		APIType:      APIResponses,
		BaseURL:      srv.URL,
		ExtraHeaders: map[string]string{},
		ModelInBody:  true,
		WebSearch: &WebSearchConfig{
			Enabled:  true,
			ToolType: "web_search",
		},
	}
	p := New("compatible", "test-model", prof)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := p.StreamChat(ctx, types.ChatRequest{
		Model:    "test-model",
		Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("search for AAPL")}},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var final *types.FinalData
	progressEvents := make([]types.ProviderToolProgressData, 0)
	for ev := range ch {
		switch ev.Event {
		case types.EventProviderToolProgress:
			d, ok := ev.Data.(types.ProviderToolProgressData)
			if !ok {
				t.Fatalf("provider tool progress has unexpected data type: %T", ev.Data)
			}
			progressEvents = append(progressEvents, d)
		case types.EventFinal:
			d, ok := ev.Data.(types.FinalData)
			if !ok {
				t.Fatalf("final has unexpected data type: %T", ev.Data)
			}
			final = &d
		case types.EventToolCalls:
			t.Fatalf("provider web search should be included on final, not sent for local execution: %#v", ev.Data)
		case types.EventError:
			t.Fatalf("unexpected error event: %#v", ev.Data)
		}
	}

	tools, ok := captured["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("expected web search tool in request, got %#v", captured["tools"])
	}
	if got, _ := tools[0].(map[string]any)["type"].(string); got != "web_search" {
		t.Fatalf("unexpected web search request tool: %#v", tools[0])
	}
	if final == nil {
		t.Fatalf("expected final event")
	}
	if final.Answer != "AAPL is trading." {
		t.Fatalf("unexpected final answer: %q", final.Answer)
	}
	if len(progressEvents) < 2 {
		t.Fatalf("expected provider web search progress events, got %#v", progressEvents)
	}
	firstProgress := progressEvents[0]
	if firstProgress.ToolCallID != "ws_1" ||
		firstProgress.ToolName != "web_search" ||
		firstProgress.Provider != "provider-hosted" ||
		firstProgress.Integration != "openai-responses" ||
		firstProgress.Query != "AAPL current price" ||
		firstProgress.Phase != "polling" ||
		firstProgress.Status != "searching" ||
		!firstProgress.ProviderExecuted {
		t.Fatalf("unexpected first provider progress event: %#v", firstProgress)
	}
	lastProgress := progressEvents[len(progressEvents)-1]
	if lastProgress.ToolCallID != "ws_1" ||
		lastProgress.Phase != "complete" ||
		lastProgress.Status != "completed" ||
		lastProgress.ResultsCount != 1 ||
		len(lastProgress.ResultPreview) != 1 {
		t.Fatalf("unexpected completed provider progress event: %#v", lastProgress)
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("expected provider tool call, got %#v", final.ToolCalls)
	}
	call := final.ToolCalls[0]
	if call.ID != "ws_1" || call.Name != "web_search" || !call.ProviderExecuted {
		t.Fatalf("unexpected provider tool call: %#v", call)
	}
	if got, _ := call.Args["query"].(string); got != "AAPL current price" {
		t.Fatalf("unexpected web search query args: %#v", call.Args)
	}
	if len(final.ToolResults) != 1 || final.ToolResults[0].Content == nil {
		t.Fatalf("expected provider tool result, got %#v", final.ToolResults)
	}
	content := *final.ToolResults[0].Content
	if !strings.Contains(content, "AAPL current price") || !strings.Contains(content, "Apple quote") || !strings.Contains(content, "https://example.com/aapl") {
		t.Fatalf("provider tool result missing query/results: %s", content)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		t.Fatalf("provider tool result is not JSON: %v", err)
	}
	if args, ok := env["tool_args"].(map[string]any); !ok || args["query"] != "AAPL current price" {
		t.Fatalf("provider tool result tool_args=%#v, want query args", env["tool_args"])
	}
	meta, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("provider tool result metadata missing: %#v", env["result_metadata"])
	}
	if meta["provider_hosted_tool_id"] != "openai-responses/web_search" ||
		meta["adapter_family"] != "openai-responses" ||
		meta["tool_set_id"] != "provider-hosted" ||
		meta["tool_set_name"] != "Model provider capabilities" ||
		meta["origin"] != "provider" ||
		meta["permission_subject_kind"] != "provider_hosted_tool" ||
		meta["settings_key"] != "webSearch" ||
		meta["readonly"] != true ||
		meta["renderer_strategy"] != "family" ||
		meta["renderer_key"] != "provider-hosted" ||
		meta["native_viewer"] != "connector_inspector" {
		t.Fatalf("provider tool result metadata=%#v, want hosted catalog metadata", meta)
	}
	if presentation, ok := meta["tool_presentation"].(map[string]any); !ok || presentation["family"] != "provider-hosted" {
		t.Fatalf("provider tool result presentation=%#v, want provider-hosted presentation", meta["tool_presentation"])
	}
	if _, ok := meta["test"].(map[string]any); !ok {
		t.Fatalf("provider tool result test metadata=%#v, want settings test spec", meta["test"])
	}
}

func TestProvider_Responses_WebSearchCallCoalescesIDLessFragments(t *testing.T) {
	tools := map[string]*responsesProviderToolState{}
	order := make([]string, 0)

	first := recordResponsesWebSearchItem(tools, &order, map[string]any{
		"type":   "web_search_call",
		"status": "searching",
		"action": map[string]any{
			"type":  "search",
			"query": "Bombyliidae Southern Africa page 27",
		},
	})
	second := recordResponsesWebSearchItem(tools, &order, map[string]any{
		"type":   "web_search_call",
		"status": "completed",
		"action": map[string]any{
			"type":  "search",
			"query": "Bombyliidae Southern Africa page 27",
			"results": []any{
				map[string]any{
					"title": "BHL page",
					"url":   "https://example.com/page/27",
				},
			},
		},
	})

	if first == nil || second == nil {
		t.Fatalf("expected recorded web search states, got first=%#v second=%#v", first, second)
	}
	if first.ID != second.ID {
		t.Fatalf("expected ID-less fragments for the same query to coalesce, got %q and %q", first.ID, second.ID)
	}
	if len(order) != 1 || len(tools) != 1 {
		t.Fatalf("expected one coalesced provider tool state, order=%#v tools=%#v", order, tools)
	}
	if len(second.Results) != 1 {
		t.Fatalf("expected coalesced result on provider tool state, got %#v", second.Results)
	}
	calls, results := responsesProviderToolsFinal(tools, order)
	if len(calls) != 1 || len(results) != 1 {
		t.Fatalf("expected one final provider tool call/result, calls=%#v results=%#v", calls, results)
	}
	if got, _ := calls[0].Args["query"].(string); got != "Bombyliidae Southern Africa page 27" {
		t.Fatalf("unexpected coalesced query args: %#v", calls[0].Args)
	}
	if results[0].Content == nil || !strings.Contains(*results[0].Content, "https://example.com/page/27") {
		t.Fatalf("expected coalesced result content to include result URL, got %#v", results[0].Content)
	}
}

func TestProvider_Responses_CodeInterpreterCallSurfacesAsProviderTool(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"code_interpreter_call","id":"ci_1","status":"in_progress","code":"print('hello')","container_id":"ctr_1"}}` + "\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_final","output_text":"Done.","output":[{"type":"code_interpreter_call","id":"ci_1","status":"completed","code":"print('hello')","container_id":"ctr_1","outputs":[{"type":"logs","logs":"hello\n"}],"files":[{"id":"file_1","name":"plot.png"}]},{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"Done."}]}]}}` + "\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	prof := Profile{
		ProviderID:   "compatible",
		APIType:      APIResponses,
		BaseURL:      srv.URL,
		ExtraHeaders: map[string]string{},
		ModelInBody:  true,
		CodeExecution: &CodeExecutionConfig{
			Enabled:  true,
			ToolType: "code_interpreter",
		},
	}
	p := New("compatible", "test-model", prof)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := p.StreamChat(ctx, types.ChatRequest{
		Model:    "test-model",
		Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("run code")}},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var final *types.FinalData
	for ev := range ch {
		if ev.Event != types.EventFinal {
			continue
		}
		d, ok := ev.Data.(types.FinalData)
		if !ok {
			t.Fatalf("final has unexpected data type: %T", ev.Data)
		}
		final = &d
	}
	if final == nil {
		t.Fatalf("expected final event")
	}
	if len(final.ToolCalls) != 1 {
		t.Fatalf("expected provider code tool call, got %#v", final.ToolCalls)
	}
	call := final.ToolCalls[0]
	if call.ID != "ci_1" || call.Name != "code_interpreter" || !call.ProviderExecuted {
		t.Fatalf("unexpected provider tool call: %#v", call)
	}
	if got, _ := call.Args["code"].(string); got != "print('hello')" {
		t.Fatalf("unexpected code args: %#v", call.Args)
	}
	if len(final.ToolResults) != 1 || final.ToolResults[0].Content == nil {
		t.Fatalf("expected provider code tool result, got %#v", final.ToolResults)
	}
	content := *final.ToolResults[0].Content
	if !strings.Contains(content, "hello") || !strings.Contains(content, "plot.png") || !strings.Contains(content, "ctr_1") {
		t.Fatalf("provider code tool result missing output/files/container: %s", content)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(content), &env); err != nil {
		t.Fatalf("provider code tool result is not JSON: %v", err)
	}
	if env["tool_name"] != "code_interpreter" {
		t.Fatalf("provider code tool result tool_name=%#v", env["tool_name"])
	}
	meta, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("provider code tool result metadata missing: %#v", env["result_metadata"])
	}
	if meta["provider_hosted_tool_id"] != "openai-responses/code_interpreter" ||
		meta["adapter_family"] != "openai-responses" ||
		meta["settings_key"] != "codeExecution" ||
		meta["category"] != "execute" ||
		meta["readonly"] != false ||
		meta["permission_subject_kind"] != "provider_hosted_tool" ||
		meta["native_viewer"] != "connector_inspector" {
		t.Fatalf("provider code tool result metadata=%#v, want hosted code catalog metadata", meta)
	}
}

func TestBuildBody_ResponsesUsesPreviousResponseIDAndDeltaInput(t *testing.T) {
	mapper := toolnamemapper.New(64)
	profile := Profile{
		ProviderID:  "compatible",
		APIType:     APIResponses,
		BaseURL:     "https://example.com/v1",
		ModelInBody: true,
	}
	providerPayload := encodeResponsesProviderPayload("compatible", map[string]any{
		"previous_response_id": "resp_prev_1",
	})
	bodyBytes, err := buildBody(profile, "test-model", types.ChatRequest{
		Model:        "test-model",
		SystemPrompt: "Be precise.",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("start")},
			{
				Role:            types.RoleAssistant,
				ProviderPayload: providerPayload,
				ToolCalls: []types.ToolCall{
					{ID: "call_1", Name: "my.tool", Args: map[string]any{"x": 1}},
				},
			},
			{Role: types.RoleTool, ToolCallID: "call_1", Content: ptr(`{"ok":true}`)},
		},
		ProviderState: map[string]any{"previous_response_id": "resp_prev_1"},
	}, mapper)
	if err != nil {
		t.Fatalf("buildBody error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if got, _ := body["previous_response_id"].(string); got != "resp_prev_1" {
		t.Fatalf("unexpected previous_response_id: %#v", body["previous_response_id"])
	}
	input, ok := body["input"].([]any)
	if !ok {
		t.Fatalf("expected array input, got %T", body["input"])
	}
	if len(input) != 1 {
		t.Fatalf("expected only delta input item, got %#v", input)
	}
	item, ok := input[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected input item type: %T", input[0])
	}
	if got, _ := item["type"].(string); got != "function_call_output" {
		t.Fatalf("unexpected delta item type: %#v", item)
	}
	if got, _ := item["call_id"].(string); got != "call_1" {
		t.Fatalf("unexpected call_id: %#v", item)
	}
}

func TestProvider_ResponsesRetriesWithFullHistoryWhenPreviousResponseMissing(t *testing.T) {
	providerPayload := encodeResponsesProviderPayload("openai", map[string]any{
		"previous_response_id": "resp_missing",
	})
	var attempts atomic.Int32
	var bodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		bodies = append(bodies, body)

		if attempts.Add(1) == 1 {
			if got, _ := body["previous_response_id"].(string); got != "resp_missing" {
				t.Errorf("first request previous_response_id = %#v", body["previous_response_id"])
			}
			input, _ := body["input"].([]any)
			if len(input) != 1 {
				t.Errorf("first request input length = %d, want delta-only input", len(input))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Response with id resp_missing not found."}}`))
			return
		}

		if _, ok := body["previous_response_id"]; ok {
			t.Errorf("fallback request unexpectedly kept previous_response_id: %#v", body["previous_response_id"])
		}
		input, _ := body["input"].([]any)
		if len(input) != 3 {
			t.Errorf("fallback request input length = %d, want full history", len(input))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_new","output_text":"fallback ok","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}` + "\n\n"))
	}))
	defer srv.Close()

	p := New("openai", "gpt-5.5", Profile{
		ProviderID:  "openai",
		APIType:     APIResponses,
		BaseURL:     srv.URL,
		ModelInBody: true,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-5.5",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("start")},
			{Role: types.RoleAssistant, Content: ptr("previous answer"), ProviderPayload: providerPayload},
			{Role: types.RoleUser, Content: ptr("continue")},
		},
		ProviderState: map[string]any{
			"previous_response_id": "resp_missing",
		},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if len(bodies) != 2 {
		t.Fatalf("captured request bodies = %d, want 2", len(bodies))
	}
	if len(events) == 0 {
		t.Fatalf("expected final event")
	}
	last := events[len(events)-1]
	if last.Event != types.EventFinal {
		t.Fatalf("terminal event = %q, want final (%#v)", last.Event, last)
	}
	final, ok := last.Data.(types.FinalData)
	if !ok {
		t.Fatalf("final data type = %T", last.Data)
	}
	if final.Answer != "fallback ok" {
		t.Fatalf("answer = %q, want fallback ok", final.Answer)
	}
	if got, _ := final.Meta.ProviderState["previous_response_id"].(string); got != "resp_new" {
		t.Fatalf("provider state previous_response_id = %#v", final.Meta.ProviderState)
	}
}

func TestProvider_ResponsesRetriesWithFullHistoryWhenStreamReportsMissingResponse(t *testing.T) {
	providerPayload := encodeResponsesProviderPayload("openai", map[string]any{
		"previous_response_id": "resp_missing",
	})
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		if attempts.Add(1) == 1 {
			if got, _ := body["previous_response_id"].(string); got != "resp_missing" {
				t.Errorf("first request previous_response_id = %#v", body["previous_response_id"])
			}
			_, _ = w.Write([]byte("event: response.error\n"))
			_, _ = w.Write([]byte(`data: {"type":"response.error","error":{"message":"No response found with id 'resp_missing'."}}` + "\n\n"))
			return
		}

		if _, ok := body["previous_response_id"]; ok {
			t.Errorf("fallback request unexpectedly kept previous_response_id: %#v", body["previous_response_id"])
		}
		input, _ := body["input"].([]any)
		if len(input) != 3 {
			t.Errorf("fallback request input length = %d, want full history", len(input))
		}
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_new","output_text":"fallback ok","usage":{"input_tokens":12,"output_tokens":3,"total_tokens":15}}}` + "\n\n"))
	}))
	defer srv.Close()

	p := New("openai", "gpt-5.5", Profile{
		ProviderID:  "openai",
		APIType:     APIResponses,
		BaseURL:     srv.URL,
		ModelInBody: true,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-5.5",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("start")},
			{Role: types.RoleAssistant, Content: ptr("previous answer"), ProviderPayload: providerPayload},
			{Role: types.RoleUser, Content: ptr("continue")},
		},
		ProviderState: map[string]any{
			"previous_response_id": "resp_missing",
		},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for event := range ch {
		events = append(events, event)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if len(events) != 1 || events[0].Event != types.EventFinal {
		t.Fatalf("events = %#v, want one final event", events)
	}
	final, ok := events[0].Data.(types.FinalData)
	if !ok {
		t.Fatalf("final data type = %T", events[0].Data)
	}
	if final.Answer != "fallback ok" {
		t.Fatalf("answer = %q, want fallback ok", final.Answer)
	}
	if final.Meta.UpstreamRequestCount == nil || *final.Meta.UpstreamRequestCount != 2 {
		t.Fatalf("upstream request count = %#v, want 2", final.Meta.UpstreamRequestCount)
	}
}

func TestIsPreviousResponseNotFoundError(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    bool
	}{
		{name: "canonical param", message: `{"error":{"message":"not found","param":"previous_response_id"}}`, want: true},
		{name: "response id", message: `Response with id 'resp_missing' not found.`, want: true},
		{name: "response does not exist", message: `The response resp_missing does not exist.`, want: true},
		{name: "generic response missing", message: `Response not found.`, want: true},
		{name: "structured response missing code", message: `{"error":{"message":"Not found","code":"response_not_found"}}`, want: true},
		{name: "different response", message: `Response with id 'resp_other' not found.`, want: false},
		{name: "model missing", message: `Model gpt-missing not found for the Responses API.`, want: false},
		{name: "file missing", message: `File file-missing not found.`, want: false},
		{name: "unrelated error", message: `Bad request.`, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isPreviousResponseNotFoundError(test.message, "resp_missing"); got != test.want {
				t.Fatalf("isPreviousResponseNotFoundError() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProvider_ResponsesRetriesSidecarOnlyWhenNativeFileFallbackIsAuthorized(t *testing.T) {
	var attempts atomic.Int32
	var bodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		bodies = append(bodies, body)

		if attempts.Add(1) == 1 {
			if !responsesTestBodyContainsInputFile(t, body) {
				t.Errorf("first request did not include native input_file: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Empty file.","type":"invalid_request_error"}}`))
			return
		}

		if responsesTestBodyContainsInputFile(t, body) {
			t.Errorf("fallback request still included native input_file: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_sidecar","output_text":"This PDF is a generative AI report summary."}}` + "\n\n"))
	}))
	defer srv.Close()

	p := New("compatible", "gpt-5.5-2", Profile{
		ProviderID:    "compatible",
		APIType:       APIResponses,
		BaseURL:       srv.URL,
		ModelInBody:   true,
		FileInputMode: FileInputNativeSupported,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-5.5-2",
		RuntimePlan: &types.RuntimePlanPolicy{
			PlanID:                   "plan-native-file-fallback",
			AllowReducedFileFallback: true,
		},
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("What's in this file?"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
						PreviewText:      "This PDF is a generative AI report.",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if len(bodies) != 2 {
		t.Fatalf("captured request bodies = %d, want 2", len(bodies))
	}
	secondBody, _ := json.Marshal(bodies[1])
	if !strings.Contains(string(secondBody), "Uploaded file fallback content") ||
		!strings.Contains(string(secondBody), "This PDF is a generative AI report.") {
		t.Fatalf("fallback request did not include generated sidecar preview: %s", secondBody)
	}
	if len(events) == 0 {
		t.Fatalf("expected final event")
	}
	if events[0].Event != types.EventPlanUpdate {
		t.Fatalf("first event = %q, want plan_update before fallback transport (%#v)", events[0].Event, events)
	}
	planUpdate, ok := events[0].Data.(types.PlanUpdateData)
	if !ok {
		t.Fatalf("plan_update data type = %T", events[0].Data)
	}
	if planUpdate.PlanID != "plan-native-file-fallback" {
		t.Fatalf("plan_update plan id = %q", planUpdate.PlanID)
	}
	if planUpdate.Change.Kind != "fidelity_reduction" {
		t.Fatalf("plan_update change kind = %q", planUpdate.Change.Kind)
	}
	if planUpdate.Change.Feature == nil || *planUpdate.Change.Feature != "documents" {
		t.Fatalf("plan_update feature = %#v", planUpdate.Change.Feature)
	}
	last := events[len(events)-1]
	if last.Event != types.EventFinal {
		t.Fatalf("terminal event = %q, want final (%#v)", last.Event, last)
	}
	final, ok := last.Data.(types.FinalData)
	if !ok {
		t.Fatalf("final data type = %T", last.Data)
	}
	if final.Answer != "This PDF is a generative AI report summary." {
		t.Fatalf("answer = %q, want sidecar fallback answer", final.Answer)
	}
}

func TestProvider_ResponsesRejectsUnplannedNativeFileFallback(t *testing.T) {
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		if !responsesTestBodyContainsInputFile(t, body) {
			t.Errorf("native request did not include input_file: %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Empty file.","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	p := New("compatible", "gpt-5.5-2", Profile{
		ProviderID:    "compatible",
		APIType:       APIResponses,
		BaseURL:       srv.URL,
		ModelInBody:   true,
		FileInputMode: FileInputNativeSupported,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-5.5-2",
		RuntimePlan: &types.RuntimePlanPolicy{
			PlanID:                   "plan-native-file-no-fallback",
			AllowReducedFileFallback: false,
		},
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("What's in this file?"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
						PreviewText:      "This PDF is a generative AI report.",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if got := attempts.Load(); got != 1 {
		t.Fatalf("transport attempts = %d, want only the rejected native request", got)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one authorization error", events)
	}
	if events[0].Event != types.EventError {
		t.Fatalf("event = %q, want error", events[0].Event)
	}
	errData, ok := events[0].Data.(types.ErrorData)
	if !ok {
		t.Fatalf("error data type = %T", events[0].Data)
	}
	if errData.Code != "UNDECLARED_FIDELITY_REDUCTION" {
		t.Fatalf("error code = %q, want UNDECLARED_FIDELITY_REDUCTION", errData.Code)
	}
}

func TestProvider_ChatCompletionsRetriesSidecarOnlyWhenNativeFileRejected(t *testing.T) {
	var attempts atomic.Int32
	var bodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		bodies = append(bodies, body)

		if attempts.Add(1) == 1 {
			if !chatCompletionsTestBodyContainsFile(body) {
				t.Errorf("first request did not include native file block: %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown content type file.","type":"invalid_request_error"}}`))
			return
		}

		if chatCompletionsTestBodyContainsFile(body) {
			t.Errorf("fallback request still included native file block: %#v", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write([]byte(`data: {"choices":[{"delta":{"content":"sidecar ok"}}]}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	p := New("compatible", "gpt-5.5-2", Profile{
		ProviderID:    "compatible",
		APIType:       APIChatCompletions,
		BaseURL:       srv.URL,
		ModelInBody:   true,
		FileInputMode: FileInputNativePDFOnly,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-5.5-2",
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("What's in this file?"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
						PreviewText:      "This PDF is a generative AI report.",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}

	if got := attempts.Load(); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	if len(bodies) != 2 {
		t.Fatalf("captured request bodies = %d, want 2", len(bodies))
	}
	secondBody, _ := json.Marshal(bodies[1])
	if !strings.Contains(string(secondBody), "Uploaded file fallback content") ||
		!strings.Contains(string(secondBody), "This PDF is a generative AI report.") {
		t.Fatalf("fallback request did not include generated sidecar preview: %s", secondBody)
	}
	if len(events) == 0 {
		t.Fatalf("expected final event")
	}
	last := events[len(events)-1]
	if last.Event != types.EventFinal {
		t.Fatalf("terminal event = %q, want final (%#v)", last.Event, last)
	}
	final, ok := last.Data.(types.FinalData)
	if !ok {
		t.Fatalf("final data type = %T", last.Data)
	}
	if final.Answer != "sidecar ok" {
		t.Fatalf("answer = %q, want sidecar fallback answer", final.Answer)
	}
}

func TestProvider_ResponsesDoesNotRetryUnrelatedHTTPError(t *testing.T) {
	providerPayload := encodeResponsesProviderPayload("openai", map[string]any{
		"previous_response_id": "resp_prev",
	})
	var attempts atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer srv.Close()

	p := New("openai", "gpt-5.5", Profile{
		ProviderID:  "openai",
		APIType:     APIResponses,
		BaseURL:     srv.URL,
		ModelInBody: true,
	})
	ch, err := p.StreamChat(context.Background(), types.ChatRequest{
		Model: "gpt-5.5",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("start")},
			{Role: types.RoleAssistant, Content: ptr("previous answer"), ProviderPayload: providerPayload},
			{Role: types.RoleUser, Content: ptr("continue")},
		},
		ProviderState: map[string]any{
			"previous_response_id": "resp_prev",
		},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts = %d, want 1", got)
	}
	if len(events) != 1 || events[0].Event != types.EventError {
		t.Fatalf("events = %#v, want single error event", events)
	}
}

func TestBuildBody_ResponsesIncludesCompactionContextManagement(t *testing.T) {
	mapper := toolnamemapper.New(64)
	profile := Profile{
		ProviderID:                "openai",
		APIType:                   APIResponses,
		BaseURL:                   "https://api.openai.com/v1",
		ModelInBody:               true,
		CompactionEnabled:         true,
		CompactionThresholdTokens: 200000,
	}

	bodyBytes, err := buildBody(profile, "gpt-5.5", types.ChatRequest{
		Model: "gpt-5.5",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("continue")},
		},
	}, mapper)
	if err != nil {
		t.Fatalf("buildBody error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	contextManagement, ok := body["context_management"].([]any)
	if !ok || len(contextManagement) != 1 {
		t.Fatalf("expected one context_management compaction entry, got %#v", body["context_management"])
	}
	entry, ok := contextManagement[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected context_management entry type: %T", contextManagement[0])
	}
	if got, _ := entry["type"].(string); got != "compaction" {
		t.Fatalf("unexpected compaction entry type: %#v", entry)
	}
	if got, _ := entry["compact_threshold"].(float64); got != 200000 {
		t.Fatalf("unexpected compact_threshold: %#v", entry["compact_threshold"])
	}
}

func TestProviderCapabilities_ResponsesExposeContextManagementAndPreviousResponseID(t *testing.T) {
	p := New("openai", "gpt-5.5", Profile{
		ProviderID:                "openai",
		APIType:                   APIResponses,
		BaseURL:                   "https://api.openai.com/v1",
		ModelInBody:               true,
		CompactionEnabled:         true,
		CompactionThresholdTokens: 200000,
	})

	caps := p.Capabilities()
	if caps.NativeCompaction.Mode != capabilities.NativeCompactionOpenAIContextManagement {
		t.Fatalf("native compaction mode = %q", caps.NativeCompaction.Mode)
	}
	if !caps.NativeCompaction.Enabled {
		t.Fatalf("expected native compaction to be enabled")
	}
	if caps.NativeCompaction.TriggerTokens != 200000 {
		t.Fatalf("trigger tokens = %d, want 200000", caps.NativeCompaction.TriggerTokens)
	}
	if !caps.SupportsPreviousResponseID {
		t.Fatalf("expected previous_response_id support")
	}
	if !caps.OpaqueCompactionPreservation.ProviderPayload || !caps.OpaqueCompactionPreservation.ProviderState {
		t.Fatalf("expected opaque compaction preservation flags, got %#v", caps.OpaqueCompactionPreservation)
	}
	if caps.MaxInputTokens <= 0 || caps.MaxOutputTokens <= 0 || caps.InputSafetyReserveTokens <= 0 || caps.OutputSafetyReserveTokens <= 0 {
		t.Fatalf("expected token limits and safety reserves, got %#v", caps)
	}
}

func TestProvider_ResponsesStreamsCompactionEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: response.output_item.added\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"compaction","id":"cmp_1"}}` + "\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.output_item.done\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"compaction","id":"cmp_1"}}` + "\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.output_text.delta\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","delta":"Done","id":"rid-stream"}` + "\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: response.completed\n"))
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"rid-final","output_text":"Done"}}` + "\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	p := New("openai", "gpt-5.5", Profile{
		ProviderID:  "openai",
		APIType:     APIResponses,
		BaseURL:     srv.URL,
		ModelInBody: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ch, err := p.StreamChat(ctx, types.ChatRequest{
		Model:    "gpt-5.5",
		Messages: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("start")}},
	})
	if err != nil {
		t.Fatalf("StreamChat error: %v", err)
	}

	var events []types.StreamEvent
	for ev := range ch {
		events = append(events, ev)
	}
	if len(events) < 4 {
		t.Fatalf("expected compaction, text, and final events, got %#v", events)
	}
	if events[0].Event != types.EventCompactionStart {
		t.Fatalf("expected first event %s, got %s", types.EventCompactionStart, events[0].Event)
	}
	if events[1].Event != types.EventCompactionEnd {
		t.Fatalf("expected second event %s, got %s", types.EventCompactionEnd, events[1].Event)
	}
	if events[2].Event != types.EventTextDelta {
		t.Fatalf("expected third event %s, got %s", types.EventTextDelta, events[2].Event)
	}
	if last := events[len(events)-1]; last.Event != types.EventFinal {
		t.Fatalf("expected final event, got %#v", last)
	}
}

func TestBuildBody_ResponsesIncludesBuiltInTools(t *testing.T) {
	mapper := toolnamemapper.New(64)
	profile := Profile{
		ProviderID:  "compatible",
		APIType:     APIResponses,
		BaseURL:     "https://example.com/v1",
		ModelInBody: true,
		WebSearch: &WebSearchConfig{
			Enabled:           true,
			ToolType:          "web_search",
			SearchContextSize: "medium",
			AllowedDomains:    []string{"example.com"},
			BlockedDomains:    []string{"blocked.example"},
		},
		CodeExecution: &CodeExecutionConfig{
			Enabled:       true,
			ToolType:      "code_interpreter",
			ContainerMode: "auto",
			MemoryLimit:   "4g",
		},
	}

	bodyBytes, err := buildBody(profile, "test-model", types.ChatRequest{
		Model:        "test-model",
		SystemPrompt: "Be precise.",
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("start")},
		},
	}, mapper)
	if err != nil {
		t.Fatalf("buildBody error: %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	tools, ok := body["tools"].([]any)
	if !ok {
		t.Fatalf("expected tools array, got %T", body["tools"])
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 built-in tools, got %#v", tools)
	}

	webTool, ok := tools[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected web tool type: %T", tools[0])
	}
	webCapability := providerHostedCapabilityForTest(t, "openai-responses", "web_search")
	if got, _ := webTool["type"].(string); got != webCapability.ProviderToolNames[0] {
		t.Fatalf("unexpected web search tool type: %#v", webTool)
	}
	if got, _ := webTool["search_context_size"].(string); got != "medium" {
		t.Fatalf("unexpected search context size: %#v", webTool)
	}
	filters, ok := webTool["filters"].(map[string]any)
	if !ok {
		t.Fatalf("expected filters on web tool: %#v", webTool)
	}
	if got, _ := filters["allowed_domains"].([]any); len(got) != 1 {
		t.Fatalf("unexpected allowed domains: %#v", filters)
	}
	if got, _ := filters["blocked_domains"].([]any); len(got) != 1 {
		t.Fatalf("unexpected blocked domains: %#v", filters)
	}

	codeTool, ok := tools[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected code tool type: %T", tools[1])
	}
	codeCapability := providerHostedCapabilityForTest(t, "openai-responses", "code_interpreter")
	if got, _ := codeTool["type"].(string); got != codeCapability.ProviderToolNames[0] {
		t.Fatalf("unexpected code tool type: %#v", codeTool)
	}
	container, ok := codeTool["container"].(map[string]any)
	if !ok {
		t.Fatalf("expected code execution container: %#v", codeTool)
	}
	if got, _ := container["type"].(string); got != "auto" {
		t.Fatalf("unexpected container type: %#v", container)
	}
	if got, _ := container["memory_limit"].(string); got != "4g" {
		t.Fatalf("unexpected memory limit: %#v", container)
	}
}

func providerHostedCapabilityForTest(t *testing.T, adapterFamily string, name string) chattools.ProviderHostedToolCapability {
	t.Helper()
	for _, cap := range chattools.ProviderHostedToolCapabilities() {
		if cap.AdapterFamily == adapterFamily && cap.Name == name {
			return cap
		}
	}
	t.Fatalf("missing provider-hosted capability %s/%s", adapterFamily, name)
	return chattools.ProviderHostedToolCapability{}
}

func TestMessagesToResponses_SkipsOrphanToolReferences(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("question")},
			{Role: types.RoleTool, ToolCallID: "call_orphan", Content: ptr("ignored")},
			{
				Role:    types.RoleAssistant,
				Content: ptr("Checking"),
				ToolCalls: []types.ToolCall{
					{ID: "call_1", Name: "my.tool", Args: map[string]any{"x": 1}},
					{ID: "call_2", Name: "other.tool", Args: map[string]any{"y": 2}},
				},
			},
			{Role: types.RoleTool, ToolCallID: "call_1", Content: ptr(`{"ok":true}`)},
			{Role: types.RoleAssistant, Content: ptr("Done")},
		},
	}

	items := messagesToResponses(req, mapper)
	if len(items) != 5 {
		t.Fatalf("items len=%d, want 5: %#v", len(items), items)
	}

	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, "call_orphan") {
		t.Fatalf("serialized responses history kept orphan tool output: %s", serialized)
	}
	if strings.Contains(serialized, "call_2") {
		t.Fatalf("serialized responses history kept unmatched tool call: %s", serialized)
	}
	if !strings.Contains(serialized, "call_1") {
		t.Fatalf("serialized responses history dropped valid tool call: %s", serialized)
	}
}

func TestMessagesToResponses_EncodesInlineImages(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("describe this image"),
				ImageAttachments: []types.ImageAttachment{
					{
						FileName: "diagram.png",
						MimeType: "image/png",
						DataURI:  "data:image/png;base64,AAAA",
					},
				},
			},
		},
	}

	items := messagesToResponses(req, mapper)
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1: %#v", len(items), items)
	}
	msg, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", items[0])
	}
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected array content for multimodal user message, got %T", msg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("content len=%d, want 2: %#v", len(content), content)
	}
	imageBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image block type: %T", content[1])
	}
	if typ, _ := imageBlock["type"].(string); typ != "input_image" {
		t.Fatalf("unexpected image block type: %q", typ)
	}
	if url, _ := imageBlock["image_url"].(string); url != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected image url: %q", url)
	}
}

func TestMessagesToResponses_EncodesUploadedImageFileAsNativeImage(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("describe this upload"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-png",
						FileName:         "diagram.png",
						MimeType:         "image/png",
						InlineDataBase64: "iVBORw0KGgo=",
					},
				},
			},
		},
	}

	items := messagesToResponses(req, mapper)
	content := responsesTestContentBlocks(t, items)
	if got := responsesTestInputFilenames(content); len(got) != 0 {
		t.Fatalf("uploaded PNG was also encoded as input_file: %v; content=%#v", got, content)
	}
	if got := responsesTestInputImageURLs(content); strings.Join(got, ",") != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("input_image URLs=%v, want uploaded PNG data URI; content=%#v", got, content)
	}
}

func TestMessagesToResponses_EncodesInlinePDFFile(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("summarize this file"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-1",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
					},
				},
			},
		},
	}

	items := messagesToResponses(req, mapper)
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1: %#v", len(items), items)
	}
	msg, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", items[0])
	}
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected array content for file user message, got %T", msg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("content len=%d, want 2: %#v", len(content), content)
	}
	fileBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected file block type: %T", content[1])
	}
	if typ, _ := fileBlock["type"].(string); typ != "input_file" {
		t.Fatalf("unexpected file block type: %q", typ)
	}
	if filename, _ := fileBlock["filename"].(string); filename != "brief.pdf" {
		t.Fatalf("unexpected filename: %q", filename)
	}
	if fileData, _ := fileBlock["file_data"].(string); fileData != "data:application/pdf;base64,JVBERi0xLjQ=" {
		t.Fatalf("unexpected file data: %q", fileData)
	}
}

func TestMessagesToResponses_EncodesNativeFileDataURIs(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("analyze these files"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
					},
					{
						ArtifactID:       "artifact-xlsx",
						FileName:         "model.xlsx",
						InlineDataBase64: "UEsDBAo=",
					},
					{
						ArtifactID:       "artifact-pptx",
						FileName:         "deck.pptx",
						MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
						InlineDataBase64: "UEsDBAo=",
					},
					{
						ArtifactID:       "artifact-key",
						FileName:         "deck.key",
						InlineDataBase64: "S0VZ",
					},
					{
						ArtifactID:       "artifact-prefixed",
						FileName:         "notes.txt",
						MimeType:         "text/plain",
						InlineDataBase64: "data:text/plain;base64,aGVsbG8=",
					},
				},
			},
		},
	}

	items := messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputNativeSupported)
	content := responsesTestContentBlocks(t, items)
	got := responsesTestInputFileData(content)
	want := []string{
		"data:application/pdf;base64,JVBERi0xLjQ=",
		"data:application/vnd.openxmlformats-officedocument.spreadsheetml.sheet;base64,UEsDBAo=",
		"data:application/vnd.openxmlformats-officedocument.presentationml.presentation;base64,UEsDBAo=",
		"data:application/vnd.apple.keynote;base64,S0VZ",
		"data:text/plain;base64,aGVsbG8=",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("file_data values=%#v, want %#v", got, want)
	}
}

func TestMessagesToResponses_EncodesAllNativeSupportedFiles(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("analyze these files"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-cpp",
						FileName:         "main.cpp",
						InlineDataBase64: "aW50IG1haW4oKSB7fQo=",
						FileKind:         "text",
						Capabilities:     map[string]bool{"canUseNativeDocument": true},
					},
					{
						ArtifactID:       "artifact-mjs",
						FileName:         "index.mjs",
						MimeType:         "application/octet-stream",
						InlineDataBase64: "Y29uc29sZS5sb2coMSk=",
						FileKind:         "text",
						Capabilities:     map[string]bool{"canUseNativeDocument": true},
					},
					{
						ArtifactID:       "artifact-yaml",
						FileName:         "config.yaml",
						NormalizedMIME:   "application/yaml",
						InlineDataBase64: "bmFtZTogZGVtbwo=",
						FileKind:         "text",
						Capabilities:     map[string]bool{"canUseNativeDocument": true},
					},
					{
						ArtifactID:       "artifact-zip",
						FileName:         "archive.zip",
						MimeType:         "application/zip",
						InlineDataBase64: "UEsDBAo=",
						FileKind:         "archive",
						Capabilities:     map[string]bool{"canUseNativeDocument": false},
					},
				},
			},
		},
	}

	items := messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputNativeSupported)
	content := responsesTestContentBlocks(t, items)
	if got := responsesTestInputFilenames(content); strings.Join(got, ",") != "main.cpp,index.mjs,config.yaml,archive.zip" {
		t.Fatalf("native_supported filenames=%v, want all inline files; content=%#v", got, content)
	}
	gotData := responsesTestInputFileData(content)
	wantData := []string{
		"data:text/plain;base64,aW50IG1haW4oKSB7fQo=",
		"data:application/javascript;base64,Y29uc29sZS5sb2coMSk=",
		"data:application/yaml;base64,bmFtZTogZGVtbwo=",
		"data:application/zip;base64,UEsDBAo=",
	}
	if strings.Join(gotData, "\n") != strings.Join(wantData, "\n") {
		t.Fatalf("file_data values=%#v, want %#v", gotData, wantData)
	}
}

func TestMessagesToResponses_FileInputModes(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("analyze these files"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
					},
					{
						ArtifactID:       "artifact-xlsx",
						FileName:         "model.xlsx",
						MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
						InlineDataBase64: "UEsDBAo=",
					},
					{
						ArtifactID:       "artifact-pptx",
						FileName:         "deck.pptx",
						MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
						InlineDataBase64: "UEsDBAo=",
					},
					{
						ArtifactID:       "artifact-key",
						FileName:         "deck.key",
						MimeType:         "application/vnd.apple.keynote",
						InlineDataBase64: "S0VZ",
					},
				},
			},
		},
	}

	items := messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputSidecarOnly)
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1: %#v", len(items), items)
	}
	msg, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", items[0])
	}
	if got, _ := msg["content"].(string); got != "analyze these files" {
		t.Fatalf("sidecar-only content=%#v, want plain text", msg["content"])
	}

	items = messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputNativePDFOnly)
	content := responsesTestContentBlocks(t, items)
	if got := responsesTestInputFilenames(content); strings.Join(got, ",") != "brief.pdf" {
		t.Fatalf("native_pdf_only filenames=%v, want [brief.pdf]; content=%#v", got, content)
	}

	items = messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputNativeSupported)
	content = responsesTestContentBlocks(t, items)
	if got := responsesTestInputFilenames(content); strings.Join(got, ",") != "brief.pdf,model.xlsx,deck.pptx,deck.key" {
		t.Fatalf("native_supported filenames=%v, want [brief.pdf model.xlsx deck.pptx deck.key]; content=%#v", got, content)
	}
}

func TestMessagesToResponses_AddsToolImageAsNativeInput(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("read the Jira attachment")},
			{
				Role: types.RoleAssistant,
				ToolCalls: []types.ToolCall{
					{ID: "call_1", Name: "jira_read_attachment", Args: map[string]any{"issueKey": "ABC-1"}},
				},
			},
			{
				Role:       types.RoleTool,
				ToolCallID: "call_1",
				Name:       "jira_read_attachment",
				Content:    ptr(`{"ok":true,"native_image_input":true}`),
				ImageAttachments: []types.ImageAttachment{
					{
						FileName: "screenshot.png",
						MimeType: "image/png",
						DataURI:  "data:image/png;base64,BBBB",
					},
				},
			},
		},
	}

	items := messagesToResponses(req, mapper)
	if len(items) != 4 {
		t.Fatalf("items len=%d, want 4: %#v", len(items), items)
	}
	output, ok := items[2].(map[string]any)
	if !ok {
		t.Fatalf("unexpected function output type: %T", items[2])
	}
	if typ, _ := output["type"].(string); typ != "function_call_output" {
		t.Fatalf("unexpected item type: %q", typ)
	}
	imageMsg, ok := items[3].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image message type: %T", items[3])
	}
	if role, _ := imageMsg["role"].(string); role != "user" {
		t.Fatalf("image message role=%q, want user", role)
	}
	content, ok := imageMsg["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("unexpected image content: %#v", imageMsg["content"])
	}
	imageBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image block type: %T", content[1])
	}
	if typ, _ := imageBlock["type"].(string); typ != "input_image" {
		t.Fatalf("unexpected image block type: %q", typ)
	}
	if url, _ := imageBlock["image_url"].(string); url != "data:image/png;base64,BBBB" {
		t.Fatalf("unexpected image url: %q", url)
	}
}

func TestMessagesToResponses_AddsToolFileAsNativeInput(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("read the Jira workbook")},
			{
				Role: types.RoleAssistant,
				ToolCalls: []types.ToolCall{
					{ID: "call_1", Name: "jira_read_attachment", Args: map[string]any{"issueKey": "ABC-1"}},
				},
			},
			{
				Role:       types.RoleTool,
				ToolCallID: "call_1",
				Name:       "jira_read_attachment",
				Content:    ptr(`{"ok":true,"attachment_artifact_id":"artifact_xlsx"}`),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact_xlsx",
						FileName:         "revenue.xlsx",
						MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
						InlineDataBase64: "UEsDBAo=",
					},
				},
			},
		},
	}

	items := messagesToResponses(req, mapper)
	if len(items) != 4 {
		t.Fatalf("items len=%d, want 4: %#v", len(items), items)
	}
	fileMsg, ok := items[3].(map[string]any)
	if !ok {
		t.Fatalf("unexpected file message type: %T", items[3])
	}
	if role, _ := fileMsg["role"].(string); role != "user" {
		t.Fatalf("file message role=%q, want user", role)
	}
	content, ok := fileMsg["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("unexpected file content: %#v", fileMsg["content"])
	}
	fileBlock, ok := content[1].(map[string]any)
	if !ok || fileBlock["type"] != "input_file" {
		t.Fatalf("second content block=%#v, want input_file", content[1])
	}
	if got, _ := fileBlock["filename"].(string); got != "revenue.xlsx" {
		t.Fatalf("filename=%q, want revenue.xlsx", got)
	}
}

func TestMessagesToResponses_AddsArxivReadPaperPDFAsNativeInput(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("read the paper before recommending it")},
			{
				Role: types.RoleAssistant,
				ToolCalls: []types.ToolCall{
					{ID: "call_arxiv_read", Name: "arxiv_read_paper", Args: map[string]any{"paper": "2508.08077", "start_page": 1}},
				},
			},
			{
				Role:       types.RoleTool,
				ToolCallID: "call_arxiv_read",
				Name:       "arxiv_read_paper",
				Content:    ptr(`{"ok":true,"arxiv_id":"2508.08077","pages":[{"page_number":1,"text":"abstract"}]}`),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact_arxiv_pdf",
						FileName:         "Agentic Search for Scientific Discovery.pdf",
						MimeType:         "application/pdf",
						FileKind:         "pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
						Capabilities:     map[string]bool{"model_file_input": true, "native_file_input": true},
					},
				},
			},
		},
	}

	items := messagesToResponsesWithValid(req.Messages, mapper, collectValidToolCallIDs(req.Messages), FileInputNativeSupported)
	if len(items) != 4 {
		t.Fatalf("items len=%d, want 4: %#v", len(items), items)
	}
	fileMsg, ok := items[3].(map[string]any)
	if !ok {
		t.Fatalf("unexpected file message type: %T", items[3])
	}
	content, ok := fileMsg["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("unexpected file content: %#v", fileMsg["content"])
	}
	fileBlock, ok := content[1].(map[string]any)
	if !ok || fileBlock["type"] != "input_file" {
		t.Fatalf("second content block=%#v, want input_file", content[1])
	}
	if got, _ := fileBlock["filename"].(string); got != "Agentic Search for Scientific Discovery.pdf" {
		t.Fatalf("filename=%q, want arXiv paper filename", got)
	}
	if got, _ := fileBlock["file_data"].(string); got != "data:application/pdf;base64,JVBERi0xLjQ=" {
		t.Fatalf("file_data=%q, want PDF data URI", got)
	}
}

func TestMessagesToChatCompletions_SkipsOrphanToolReferences(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("question")},
			{Role: types.RoleTool, ToolCallID: "call_orphan", Content: ptr("ignored")},
			{
				Role:    types.RoleAssistant,
				Content: ptr("Checking"),
				ToolCalls: []types.ToolCall{
					{ID: "call_1", Name: "my.tool", Args: map[string]any{"x": 1}},
					{ID: "call_2", Name: "other.tool", Args: map[string]any{"y": 2}},
				},
			},
			{Role: types.RoleTool, ToolCallID: "call_1", Content: ptr(`{"ok":true}`)},
			{Role: types.RoleAssistant, Content: ptr("Done")},
		},
	}

	items := messagesToChatCompletions(req, mapper)
	if len(items) != 4 {
		t.Fatalf("items len=%d, want 4: %#v", len(items), items)
	}

	raw, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	serialized := string(raw)
	if strings.Contains(serialized, "call_orphan") {
		t.Fatalf("serialized chat history kept orphan tool output: %s", serialized)
	}
	if strings.Contains(serialized, "call_2") {
		t.Fatalf("serialized chat history kept unmatched tool call: %s", serialized)
	}
	if !strings.Contains(serialized, "call_1") {
		t.Fatalf("serialized chat history dropped valid tool call: %s", serialized)
	}
}

func TestMessagesToChatCompletions_EncodesNativeFileBlocks(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("summarize this file"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-pdf",
						FileName:         "brief.pdf",
						MimeType:         "application/pdf",
						InlineDataBase64: "JVBERi0xLjQ=",
					},
					{
						ArtifactID:       "artifact-pptx",
						FileName:         "deck.pptx",
						MimeType:         "application/vnd.openxmlformats-officedocument.presentationml.presentation",
						InlineDataBase64: "UEsDBAo=",
					},
				},
			},
		},
	}

	items := messagesToChatCompletionsWithFileInputMode(req, mapper, FileInputNativeSupported)
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1: %#v", len(items), items)
	}
	msg, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", items[0])
	}
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected array content for file user message, got %T", msg["content"])
	}
	if len(content) != 3 {
		t.Fatalf("content len=%d, want 3: %#v", len(content), content)
	}
	fileBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected file block type: %T", content[1])
	}
	if typ, _ := fileBlock["type"].(string); typ != "file" {
		t.Fatalf("unexpected file block type: %q", typ)
	}
	filePayload, ok := fileBlock["file"].(map[string]any)
	if !ok {
		t.Fatalf("missing file payload: %#v", fileBlock)
	}
	if filename, _ := filePayload["filename"].(string); filename != "brief.pdf" {
		t.Fatalf("unexpected filename: %q", filename)
	}
	if fileData, _ := filePayload["file_data"].(string); fileData != "data:application/pdf;base64,JVBERi0xLjQ=" {
		t.Fatalf("unexpected file data: %q", fileData)
	}
	presentationBlock, ok := content[2].(map[string]any)
	if !ok || presentationBlock["type"] != "file" {
		t.Fatalf("unexpected presentation block: %#v", content[2])
	}
	presentationPayload, ok := presentationBlock["file"].(map[string]any)
	if !ok {
		t.Fatalf("missing presentation file payload: %#v", presentationBlock)
	}
	if filename, _ := presentationPayload["filename"].(string); filename != "deck.pptx" {
		t.Fatalf("unexpected presentation filename: %q", filename)
	}
	if fileData, _ := presentationPayload["file_data"].(string); fileData != "data:application/vnd.openxmlformats-officedocument.presentationml.presentation;base64,UEsDBAo=" {
		t.Fatalf("unexpected presentation file data: %q", fileData)
	}
}

func ptr(s string) *string { return &s }

func responsesTestContentBlocks(t *testing.T, items []any) []any {
	t.Helper()
	if len(items) != 1 {
		t.Fatalf("items len=%d, want 1: %#v", len(items), items)
	}
	msg, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", items[0])
	}
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected content blocks, got %T: %#v", msg["content"], msg["content"])
	}
	return content
}

func responsesTestInputFilenames(content []any) []string {
	out := make([]string, 0)
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := block["type"].(string); typ != "input_file" {
			continue
		}
		if filename, _ := block["filename"].(string); filename != "" {
			out = append(out, filename)
		}
	}
	return out
}

func responsesTestInputFileData(content []any) []string {
	out := make([]string, 0)
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := block["type"].(string); typ != "input_file" {
			continue
		}
		if fileData, _ := block["file_data"].(string); fileData != "" {
			out = append(out, fileData)
		}
	}
	return out
}

func responsesTestInputImageURLs(content []any) []string {
	out := make([]string, 0)
	for _, raw := range content {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if typ, _ := block["type"].(string); typ != "input_image" {
			continue
		}
		if imageURL, _ := block["image_url"].(string); imageURL != "" {
			out = append(out, imageURL)
		}
	}
	return out
}

func responsesTestBodyContainsInputFile(t *testing.T, body map[string]any) bool {
	t.Helper()
	input, ok := body["input"].([]any)
	if !ok {
		return false
	}
	for _, rawItem := range input {
		item, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		content, ok := item["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := block["type"].(string); typ == "input_file" {
				return true
			}
		}
	}
	return false
}

func chatCompletionsTestBodyContainsFile(body map[string]any) bool {
	messages, ok := body["messages"].([]any)
	if !ok {
		return false
	}
	for _, rawMessage := range messages {
		message, ok := rawMessage.(map[string]any)
		if !ok {
			continue
		}
		content, ok := message["content"].([]any)
		if !ok {
			continue
		}
		for _, rawBlock := range content {
			block, ok := rawBlock.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := block["type"].(string); typ == "file" {
				return true
			}
		}
	}
	return false
}

func TestMessagesToChatCompletions_EncodesInlineImages(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("describe this image"),
				ImageAttachments: []types.ImageAttachment{
					{
						FileName: "diagram.png",
						MimeType: "image/png",
						DataURI:  "data:image/png;base64,AAAA",
					},
				},
			},
		},
	}

	out := messagesToChatCompletions(req, mapper)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	msg, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", out[0])
	}

	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected array content for multimodal user message, got %T", msg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks (text + image), got %d", len(content))
	}

	imageBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image block type: %T", content[1])
	}
	if typ, _ := imageBlock["type"].(string); typ != "image_url" {
		t.Fatalf("unexpected image block type: %q", typ)
	}
	imageURL, ok := imageBlock["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("missing image_url payload: %#v", imageBlock)
	}
	if url, _ := imageURL["url"].(string); url != "data:image/png;base64,AAAA" {
		t.Fatalf("unexpected image url: %q", url)
	}
}

func TestMessagesToChatCompletions_EncodesUploadedImageFileAsNativeImage(t *testing.T) {
	mapper := toolnamemapper.New(64)
	req := types.ChatRequest{
		Messages: []types.ChatMessage{
			{
				Role:    types.RoleUser,
				Content: ptr("describe this upload"),
				FileAttachments: []types.FileAttachment{
					{
						ArtifactID:       "artifact-png",
						FileName:         "diagram.png",
						MimeType:         "image/png",
						InlineDataBase64: "iVBORw0KGgo=",
					},
				},
			},
		},
	}

	out := messagesToChatCompletions(req, mapper)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	msg, ok := out[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected message type: %T", out[0])
	}
	content, ok := msg["content"].([]any)
	if !ok {
		t.Fatalf("expected array content for uploaded image user message, got %T", msg["content"])
	}
	if len(content) != 2 {
		t.Fatalf("expected 2 content blocks (text + image), got %d: %#v", len(content), content)
	}
	for _, rawBlock := range content {
		if block, _ := rawBlock.(map[string]any); block["type"] == "file" {
			t.Fatalf("uploaded PNG was also encoded as file block: %#v", content)
		}
	}
	imageBlock, ok := content[1].(map[string]any)
	if !ok {
		t.Fatalf("unexpected image block type: %T", content[1])
	}
	if typ, _ := imageBlock["type"].(string); typ != "image_url" {
		t.Fatalf("unexpected image block type: %q", typ)
	}
	imageURL, ok := imageBlock["image_url"].(map[string]any)
	if !ok {
		t.Fatalf("missing image_url payload: %#v", imageBlock)
	}
	if url, _ := imageURL["url"].(string); url != "data:image/png;base64,iVBORw0KGgo=" {
		t.Fatalf("unexpected image url: %q", url)
	}
}
