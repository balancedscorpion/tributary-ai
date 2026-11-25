package optionc

import (
	"encoding/json"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestBindProviderPayloadTargetPreservesAdapterShape(t *testing.T) {
	raw := json.RawMessage(`{"format":"openai_responses_v1","provider_id":"openai","api_type":"responses","state":{"previous_response_id":"resp-a"},"future_field":{"kept":true}}`)
	bound := bindProviderPayloadTarget(raw, "openai-work", "gpt-5")
	if len(bound) == 0 {
		t.Fatal("bound payload is empty")
	}

	// This mirrors the adapters' typed decoders: the app-owned extra field is
	// ignored while all established provider fields remain decodable.
	var adapterShape struct {
		Format     string         `json:"format"`
		ProviderID string         `json:"provider_id"`
		APIType    string         `json:"api_type"`
		State      map[string]any `json:"state"`
	}
	if err := json.Unmarshal(bound, &adapterShape); err != nil {
		t.Fatalf("decode adapter shape: %v", err)
	}
	if adapterShape.Format != "openai_responses_v1" || adapterShape.ProviderID != "openai" || adapterShape.APIType != "responses" {
		t.Fatalf("provider fields changed: %#v", adapterShape)
	}
	if got, _ := adapterShape.State["previous_response_id"].(string); got != "resp-a" {
		t.Fatalf("state changed: %#v", adapterShape.State)
	}
	if !providerPayloadMatchesTarget(bound, "openai-work", "gpt-5") {
		t.Fatalf("target binding missing: %s", bound)
	}

	var complete map[string]json.RawMessage
	if err := json.Unmarshal(bound, &complete); err != nil {
		t.Fatal(err)
	}
	if _, ok := complete["future_field"]; !ok {
		t.Fatalf("unknown provider field was dropped: %s", bound)
	}
}

func TestExactTargetProviderStateIgnoresUnboundAndMismatchedPayloads(t *testing.T) {
	history := []types.ChatMessage{
		providerAssistantMessage(t, "portable answer A old", "openai", "resp-a-old", "conn-a", "model-a"),
		providerAssistantMessage(t, "portable legacy answer", "openai", "resp-legacy", "", ""),
		providerAssistantMessage(t, "portable answer A new", "openai", "resp-a-new", "conn-a", "model-a"),
		providerAssistantMessage(t, "portable answer B", "openai", "resp-b", "conn-b", "model-b"),
	}
	exactRun := store.Run{
		PlanID: "plan-a", ProviderID: "openai", ConnectionID: "conn-a", Model: "model-a",
	}

	filtered := providerHistoryForRun(history, exactRun, false)
	if filtered[0].ProviderPayload == nil || filtered[2].ProviderPayload == nil {
		t.Fatalf("matching payloads were removed: %#v", filtered)
	}
	if filtered[1].ProviderPayload != nil || filtered[3].ProviderPayload != nil {
		t.Fatalf("unbound or mismatched payload survived: %#v", filtered)
	}
	for idx, want := range []string{"portable answer A old", "portable legacy answer", "portable answer A new", "portable answer B"} {
		if filtered[idx].Content == nil || *filtered[idx].Content != want {
			t.Fatalf("portable content[%d] = %#v, want %q", idx, filtered[idx].Content, want)
		}
	}
	state := providerStateFromHistoryForRun(filtered, exactRun)
	if got, _ := state["previous_response_id"].(string); got != "resp-a-new" {
		t.Fatalf("exact state = %#v, want newest conn-a/model-a state", state)
	}

	// Legacy runs retain the prior provider-family behavior, including the
	// newest state even when target-binding fields happen to be present.
	legacyRun := store.Run{ProviderID: "openai", ConnectionID: "conn-a", Model: "model-a"}
	legacyState := providerStateFromHistoryForRun(history, legacyRun)
	if got, _ := legacyState["previous_response_id"].(string); got != "resp-b" {
		t.Fatalf("legacy state = %#v, want newest provider-family state", legacyState)
	}
}

func TestRecoveryBindsOnlyPayloadProducedAfterExactFiltering(t *testing.T) {
	run := store.Run{
		PlanID: "plan-a", ProviderID: "openai", ConnectionID: "conn-a", Model: "model-a",
	}
	legacy := providerAssistantMessage(t, "portable legacy", "openai", "resp-legacy", "", "")
	filtered := providerHistoryForRun([]types.ChatMessage{legacy}, run, false)
	if filtered[0].ProviderPayload != nil {
		t.Fatalf("legacy payload survived initial exact filtering: %s", filtered[0].ProviderPayload)
	}

	current := providerAssistantMessage(t, "current answer", "openai", "resp-current", "", "")
	loopHistory := append(filtered, current)
	recoveryHistory := providerHistoryForRun(loopHistory, run, true)
	if recoveryHistory[0].ProviderPayload != nil {
		t.Fatalf("scrubbed historical payload was rebound: %s", recoveryHistory[0].ProviderPayload)
	}
	if !providerPayloadMatchesTarget(recoveryHistory[1].ProviderPayload, "conn-a", "model-a") {
		t.Fatalf("current payload was not bound: %s", recoveryHistory[1].ProviderPayload)
	}
	state := providerStateFromHistoryForRun(recoveryHistory, run)
	if got, _ := state["previous_response_id"].(string); got != "resp-current" {
		t.Fatalf("recovery state = %#v", state)
	}
}

func providerAssistantMessage(t *testing.T, content, providerID, responseID, connectionID, model string) types.ChatMessage {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"format":      "openai_responses_v1",
		"provider_id": providerID,
		"api_type":    "responses",
		"state":       map[string]any{"previous_response_id": responseID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if connectionID != "" || model != "" {
		payload = bindProviderPayloadTarget(payload, connectionID, model)
	}
	return types.ChatMessage{Role: types.RoleAssistant, Content: ptr(content), ProviderPayload: payload}
}
