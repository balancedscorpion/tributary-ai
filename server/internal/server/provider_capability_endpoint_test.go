package server

import (
	"net/http"
	"testing"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/config"
)

func TestProviderCapabilityResolveDraftReturnsExactBedrockSurface(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	handler := NewHandler(opts)
	rec := doJSON(t, handler, http.MethodPost, "/api/providers/capabilities/resolve", map[string]any{
		"config": map[string]any{
			"id": "draft-bedrock", "displayName": "Draft Bedrock", "providerId": "bedrock",
			"model": "anthropic.claude-opus-4-6-v1", "apiType": "messages", "apiSurface": "invoke_stream",
			"region": "eu-west-1",
			"capabilityControls": map[string]any{
				"hosted_web_search": "on", "hosted_code_execution": "on",
			},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	target := mustMap(t, payload["target"])
	if target["connectionId"] != nil || target["providerFamily"] != "bedrock" || target["apiSurface"] != "invoke_stream" ||
		target["endpointFamily"] != "bedrock-runtime" || target["model"] != "anthropic.claude-opus-4-6-v1" || target["region"] != "eu-west-1" ||
		target["adapterVersion"] != capabilities.DefaultAdapterVersion {
		t.Fatalf("target = %#v", target)
	}
	if payload["catalogVersion"] != capabilities.CatalogVersion || payload["fingerprint"] == "" || payload["generatedAt"] == "" {
		t.Fatalf("report metadata = %#v", payload)
	}
	features := mustMap(t, payload["features"])
	if len(features) != len(capabilities.FeatureRegistry()) {
		t.Fatalf("feature count = %d, want %d", len(features), len(capabilities.FeatureRegistry()))
	}
	for _, key := range []string{string(capabilities.FeatureHostedWebSearch), string(capabilities.FeatureHostedCodeExecution)} {
		feature := mustMap(t, features[key])
		if feature["state"] != "unsupported" || feature["effective"] != "unavailable" || !explanationsContainCode(feature["reasons"], "not_on_bedrock") {
			t.Errorf("feature %s = %#v", key, feature)
		}
	}
	documents := mustMap(t, features[string(capabilities.FeatureDocuments)])
	if documents["state"] != "conditional" || !explanationsContainCode(documents["conditions"], "requires_converse_switch") {
		t.Fatalf("documents = %#v", documents)
	}
}

func TestProviderConnectionCapabilitiesUsesSavedConfigAndModelOverride(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	settings := opts.Settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID: "anthropic-work", DisplayName: "Work Anthropic", ProviderID: "anthropic", Model: "claude-sonnet-4-6",
	}}
	activeID := "anthropic-work"
	settings.ActiveProviderConfigID = &activeID
	if err := opts.Settings.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	storedFingerprint := *opts.Settings.Get().ProviderConfigs[0].ConfigFingerprint
	handler := NewHandler(opts)
	rec := doJSON(t, handler, http.MethodGet, "/api/providers/connections/anthropic-work/capabilities?model=claude-opus-4-6", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	payload := decodeMap(t, rec.Body.Bytes())
	target := mustMap(t, payload["target"])
	if target["connectionId"] != "anthropic-work" || target["apiSurface"] != "messages" || target["model"] != "claude-opus-4-6" {
		t.Fatalf("target = %#v", target)
	}
	if payload["fingerprint"] == storedFingerprint {
		t.Fatal("model override reused the stored-model fingerprint")
	}
	if opts.Settings.Get().ProviderConfigs[0].Model != "claude-sonnet-4-6" {
		t.Fatal("read-only capability resolution mutated the saved model")
	}
}

func TestProviderCapabilityEndpointsRejectMissingTargets(t *testing.T) {
	opts, _ := newTestHandlerOpts(t)
	handler := NewHandler(opts)
	rec := doJSON(t, handler, http.MethodGet, "/api/providers/connections/missing/capabilities", nil)
	assertHTTPError(t, rec, http.StatusNotFound, "provider connection not found")

	rec = doJSON(t, handler, http.MethodPost, "/api/providers/capabilities/resolve", map[string]any{
		"config": map[string]any{"providerId": "anthropic", "apiSurface": "messages"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing model status = %d, want 400 (body=%s)", rec.Code, rec.Body.String())
	}
	if got := decodeMap(t, rec.Body.Bytes())["error"]; got != "model is required" {
		t.Fatalf("error = %#v, want model is required", got)
	}
}

func mustMap(t *testing.T, value any) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value = %#v, want JSON object", value)
	}
	return result
}

func explanationsContainCode(value any, code string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if ok && object["code"] == code {
			return true
		}
	}
	return false
}
