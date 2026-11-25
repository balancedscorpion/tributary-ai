package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	chatstore "ai-analyst-local/internal/chat/store"
	chatusage "ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
)

func TestProviderConnectionTestLegacyResponseRemainsUnextended(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "legacy-shape")
	providerServer := newProviderProbeTextServer(t, "legacy-request")
	defer providerServer.Close()

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"endpoint": providerServer.URL + "/chat/completions",
		"config":   compatibleProbeConfigMap("legacy-shape", providerServer.URL, "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	for _, forbidden := range []string{"fingerprint", "results", "tier"} {
		if _, ok := payload[forbidden]; ok {
			t.Fatalf("legacy response unexpectedly contains %q: %#v", forbidden, payload)
		}
	}
	for _, required := range []string{"ok", "message", "endpoint", "providerId", "model"} {
		if _, ok := payload[required]; !ok {
			t.Fatalf("legacy response missing %q: %#v", required, payload)
		}
	}
}

func TestProviderConnectionTestQuickReturnsTransportEvidence(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "quick-check")
	providerServer := newProviderProbeTextServer(t, "quick-request")
	defer providerServer.Close()

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":     "quick",
		"endpoint": providerServer.URL + "/chat/completions",
		"config":   compatibleProbeConfigMap("quick-check", providerServer.URL, "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	if payload["ok"] != true || strings.TrimSpace(stringField(payload, "fingerprint")) == "" {
		t.Fatalf("unexpected quick result: %#v", payload)
	}
	results := mapSliceField(t, payload, "results")
	if len(results) != 1 || results[0]["probe"] != "transport" || results[0]["verdict"] != "verified" {
		t.Fatalf("quick results = %#v", results)
	}
	if results[0]["requestId"] != "quick-request" {
		t.Fatalf("requestId = %#v, want quick-request", results[0]["requestId"])
	}
}

func TestProviderConnectionCapabilityReservedProbeIsHonestlySkipped(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":   "capability",
		"probes": []string{"structured_output"},
		"config": compatibleProbeConfigMap("reserved-skip", "http://127.0.0.1:1", "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	results := mapSliceField(t, payload, "results")
	if payload["ok"] != false || len(results) != 1 || results[0]["verdict"] != "skipped" || results[0]["reason"] != "probe_not_implemented" {
		t.Fatalf("reserved probe result = %#v", payload)
	}
}

func TestProviderConnectionCapabilityRejectsUnknownProbe(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":   "capability",
		"probes": []string{"telepathy"},
		"config": compatibleProbeConfigMap("unknown-probe", "http://127.0.0.1:1", "chat_completions"),
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "unknown capability probe") {
		t.Fatalf("status/body = %d %s, want unknown-probe 400", rec.Code, rec.Body.String())
	}
}

func TestProviderConnectionCapabilityWebSearchToggleSuppliesConsentButTargetMustSupportIt(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	cfg := compatibleProbeConfigMap("consent-probe", "http://127.0.0.1:1", "responses")
	cfg["capabilityControls"] = map[string]any{"hosted_web_search": "on"}
	cfg["webSearch"] = map[string]any{"enabled": true}
	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":                "capability",
		"probes":              []string{"hosted_web_search"},
		"consentAcknowledged": true,
		"config":              cfg,
	})
	if rec.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(rec.Body.String()), "not effective") {
		t.Fatalf("status/body = %d %s, want unsupported-target 400", rec.Code, rec.Body.String())
	}
}

func TestProviderConnectionCapabilityCustomToolUsesTwoDispatchesAndPersistsEvidence(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "saved-tool-check")

	var mu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		dispatch := len(requests)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		if dispatch == 1 {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"tool-request-1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_probe_1\",\"function\":{\"name\":\"capability_probe_echo\",\"arguments\":\"{\\\"value\\\":\\\"ok\\\"}\"}}]}}]}\n\n")
		} else {
			_, _ = fmt.Fprint(w, "data: {\"id\":\"tool-request-2\",\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n")
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	apiType := "chat_completions"
	baseURL := providerServer.URL
	authType := "api_key"
	authHeader := "Authorization"
	authScheme := "Bearer"
	modelInBody := true
	modelInPath := false
	settings := opts.Settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID: "saved-tool-check", DisplayName: "Saved tool check", ProviderID: "compatible", Model: "probe-model",
		ApiType: &apiType, BaseURL: &baseURL, AuthType: &authType, AuthHeader: &authHeader, AuthScheme: &authScheme,
		ModelInBody: &modelInBody, ModelInPath: &modelInPath,
	}}
	if err := opts.Settings.Save(settings); err != nil {
		t.Fatalf("save provider settings: %v", err)
	}
	evidenceStore, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("chatstore.NewJSON: %v", err)
	}
	opts.ChatStore = evidenceStore
	usagePrimary, err := chatusage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("usage.NewJSONRepository: %v", err)
	}
	usageRecorder, err := chatusage.NewRecorder(t.TempDir(), usagePrimary)
	if err != nil {
		t.Fatalf("usage.NewRecorder: %v", err)
	}
	opts.UsageRepository = usageRecorder

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":         "capability",
		"connectionId": "saved-tool-check",
		"probes":       []string{"custom_tools", "custom_tools"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	results := mapSliceField(t, payload, "results")
	if payload["ok"] != true || len(results) != 1 || results[0]["verdict"] != "verified" {
		t.Fatalf("custom tool result = %#v", payload)
	}
	if results[0]["requestId"] != "tool-request-2" {
		t.Fatalf("requestId = %#v, want second dispatch request id", results[0]["requestId"])
	}
	usageRows, err := usagePrimary.List(context.Background(), chatusage.Query{Operation: chatusage.OperationCapabilityProbe})
	if err != nil || len(usageRows) != 2 {
		t.Fatalf("capability probe usage rows=%d err=%v", len(usageRows), err)
	}
	for _, row := range usageRows {
		if row.ConnectionID == nil || *row.ConnectionID != "saved-tool-check" || row.UpstreamRequestCount != 1 {
			t.Fatalf("capability probe attribution=%#v", row)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("provider dispatches = %d, want 2", len(requests))
	}
	assertOnlyCapabilityProbeTool(t, requests[0])
	assertOnlyCapabilityProbeTool(t, requests[1])
	messages, ok := requests[1]["messages"].([]any)
	if !ok || len(messages) != 3 {
		t.Fatalf("second dispatch messages = %#v, want user/assistant/tool", requests[1]["messages"])
	}
	toolMessage, _ := messages[2].(map[string]any)
	if toolMessage["role"] != "tool" || toolMessage["tool_call_id"] != "call_probe_1" || !strings.Contains(fmt.Sprint(toolMessage["content"]), `"value":"ok"`) {
		t.Fatalf("tool result message = %#v", toolMessage)
	}

	evidence, err := evidenceStore.ListProviderCapabilityEvidence(context.Background(), chatstore.ProviderCapabilityEvidenceFilter{
		ConnectionID: "saved-tool-check", Model: "probe-model", Probe: "custom_tools",
	})
	if err != nil {
		t.Fatalf("ListProviderCapabilityEvidence: %v", err)
	}
	if len(evidence) != 1 || evidence[0].Verdict != "verified" || evidence[0].RequestID != "tool-request-2" || evidence[0].Fingerprint == "" {
		t.Fatalf("persisted evidence = %#v", evidence)
	}
}

func TestProviderConnectionCapabilitySystemInstructionsVerifiesExpectedMarker(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "system-instructions-check")

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		messages, ok := body["messages"].([]any)
		if !ok || len(messages) != 2 {
			t.Errorf("messages = %#v, want system and user messages", body["messages"])
		} else {
			systemMessage, _ := messages[0].(map[string]any)
			userMessage, _ := messages[1].(map[string]any)
			if systemMessage["role"] != "system" || !strings.Contains(fmt.Sprint(systemMessage["content"]), providerSystemInstructionProbeExpectedMarker) {
				t.Errorf("system message = %#v, want expected probe marker", systemMessage)
			}
			if userMessage["role"] != "user" || !strings.Contains(fmt.Sprint(userMessage["content"]), providerSystemInstructionProbeUserMarker) {
				t.Errorf("user message = %#v, want conflicting user marker", userMessage)
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"system-request-ok\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", providerSystemInstructionProbeExpectedMarker[:20])
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", providerSystemInstructionProbeExpectedMarker[20:])
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":   "capability",
		"probes": []string{"system_instructions"},
		"config": compatibleProbeConfigMap("system-instructions-check", providerServer.URL, "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	results := mapSliceField(t, payload, "results")
	if payload["ok"] != true || len(results) != 1 || results[0]["verdict"] != "verified" {
		t.Fatalf("system-instructions result = %#v", payload)
	}
	if results[0]["requestId"] != "system-request-ok" {
		t.Fatalf("requestId = %#v, want system-request-ok", results[0]["requestId"])
	}
	if duration, ok := results[0]["durationMs"].(float64); !ok || duration < 0 {
		t.Fatalf("durationMs = %#v, want non-negative number", results[0]["durationMs"])
	}
}

func TestProviderConnectionCapabilitySystemInstructionsFailsWhenSystemIsIgnored(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "system-instructions-ignored")

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"system-request-ignored\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", providerSystemInstructionProbeUserMarker)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":   "capability",
		"probes": []string{"system_instructions"},
		"config": compatibleProbeConfigMap("system-instructions-ignored", providerServer.URL, "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	results := mapSliceField(t, payload, "results")
	if payload["ok"] != false || len(results) != 1 || results[0]["verdict"] != "failed" || results[0]["reason"] != "system_instruction_not_observed" {
		t.Fatalf("ignored system-instructions result = %#v", payload)
	}
	if results[0]["requestId"] != "system-request-ignored" {
		t.Fatalf("requestId = %#v, want system-request-ignored", results[0]["requestId"])
	}
	if strings.Contains(fmt.Sprint(results[0]["message"]), providerSystemInstructionProbeUserMarker) {
		t.Fatalf("failure message leaked captured provider output: %#v", results[0]["message"])
	}
}

func TestProviderConnectionCapabilitySystemInstructionsFailsOnConflictingMarkers(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "system-instructions-conflicted")

	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		content := providerSystemInstructionProbeExpectedMarker + " " + providerSystemInstructionProbeUserMarker
		_, _ = fmt.Fprintf(w, "data: {\"id\":\"system-request-conflicted\",\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":   "capability",
		"probes": []string{"system_instructions"},
		"config": compatibleProbeConfigMap("system-instructions-conflicted", providerServer.URL, "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	results := mapSliceField(t, payload, "results")
	if payload["ok"] != false || len(results) != 1 || results[0]["verdict"] != "failed" || results[0]["reason"] != "system_instruction_not_observed" {
		t.Fatalf("conflicted system-instructions result = %#v", payload)
	}
	if strings.Contains(fmt.Sprint(results[0]["message"]), providerSystemInstructionProbeUserMarker) {
		t.Fatalf("failure message leaked captured provider output: %#v", results[0]["message"])
	}
}

func TestProviderConnectionCapabilityAttachmentsUseNativeFileAttachmentShapes(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	initializeCompatibleProviderSecret(t, opts, "attachment-check")

	var mu sync.Mutex
	requests := make([]map[string]any, 0, 2)
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode provider request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, body)
		requestID := fmt.Sprintf("attachment-request-%d", len(requests))
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"choices\":[{\"delta\":{\"content\":\"accepted\"}}]}\n\n", requestID)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer providerServer.Close()

	rec := doJSON(t, NewHandler(opts), http.MethodPost, "/api/providers/test", map[string]any{
		"tier":   "capability",
		"probes": []string{"images", "documents"},
		"config": compatibleProbeConfigMap("attachment-check", providerServer.URL, "chat_completions"),
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	results := mapSliceField(t, payload, "results")
	if payload["ok"] != true || len(results) != 2 || results[0]["verdict"] != "verified" || results[1]["verdict"] != "verified" {
		t.Fatalf("attachment probe result = %#v", payload)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("attachment provider dispatches = %d, want 2", len(requests))
	}
	imageTypes := firstUserContentPartTypes(t, requests[0])
	if !containsTestString(imageTypes, "image_url") {
		t.Fatalf("image probe content types = %#v, want image_url", imageTypes)
	}
	documentTypes := firstUserContentPartTypes(t, requests[1])
	if !containsTestString(documentTypes, "file") {
		t.Fatalf("document probe content types = %#v, want file", documentTypes)
	}
}

func initializeCompatibleProviderSecret(t *testing.T, opts HandlerOpts, connectionID string) {
	t.Helper()
	if err := opts.Secrets.Init("test-passphrase"); err != nil {
		t.Fatalf("secrets init: %v", err)
	}
	if err := opts.Secrets.Unlock("test-passphrase"); err != nil {
		t.Fatalf("secrets unlock: %v", err)
	}
	if err := opts.Secrets.SetCompatibleAPIKey(connectionID, "compatible-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}
}

func compatibleProbeConfigMap(id, baseURL, apiType string) map[string]any {
	return map[string]any{
		"id": id, "displayName": "Provider probe", "providerId": "compatible", "model": "probe-model",
		"apiType": apiType, "baseUrl": baseURL, "authType": "api_key", "authHeader": "Authorization", "authScheme": "Bearer",
		"modelInBody": true, "modelInPath": false,
	}
}

func newProviderProbeTextServer(t *testing.T, requestID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"id\":%q,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n", requestID)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
}

func mapSliceField(t *testing.T, payload map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("%s = %#v, want array", key, payload[key])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("%s item = %#v, want object", key, value)
		}
		out = append(out, item)
	}
	return out
}

func stringField(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return value
}

func assertOnlyCapabilityProbeTool(t *testing.T, request map[string]any) {
	t.Helper()
	tools, ok := request["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v, want exactly one local probe tool and no hosted tools", request["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	function, _ := tool["function"].(map[string]any)
	if function["name"] != "capability_probe_echo" {
		t.Fatalf("tool = %#v, want capability_probe_echo", tool)
	}
}

func firstUserContentPartTypes(t *testing.T, request map[string]any) []string {
	t.Helper()
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v, want user message", request["messages"])
	}
	message, _ := messages[0].(map[string]any)
	parts, ok := message["content"].([]any)
	if !ok {
		t.Fatalf("content = %#v, want multipart content", message["content"])
	}
	out := make([]string, 0, len(parts))
	for _, raw := range parts {
		part, _ := raw.(map[string]any)
		if kind, _ := part["type"].(string); kind != "" {
			out = append(out, kind)
		}
	}
	return out
}

func containsTestString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
