package optionc

import (
	"testing"

	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
)

func TestUsageAttributionForRunUsesCanonicalTarget(t *testing.T) {
	profileID := "legacy-profile"
	modernRun := store.Run{
		ID: "run-modern", ProviderID: "openai", ConnectionID: "connection-modern", PlanID: "plan-modern", Model: "gpt-modern",
	}
	snapshot := providers.DispatchSnapshot{
		ConnectionID: "connection-modern", ProviderID: "openai", Model: "gpt-modern",
		ConfigFingerprint: "provider-capability-v1:abc", PlannedAPISurface: "responses",
	}
	modern := usageAttributionForRun(modernRun, "thread-modern", snapshot)
	if modern.ConnectionID != modernRun.ConnectionID || modern.PlanID != modernRun.PlanID ||
		modern.ConfigFingerprint != snapshot.ConfigFingerprint || modern.PlannedAPISurface != "responses" {
		t.Fatalf("modern attribution=%#v", modern)
	}

	legacyRun := store.Run{ID: "run-legacy", ProviderID: "compatible", Model: "legacy-model", ProfileID: &profileID}
	legacy := usageAttributionForRun(legacyRun, "thread-legacy", providers.DispatchSnapshot{ConnectionID: "legacy-default"})
	if legacy.ConnectionID != "" || legacy.ProfileID != profileID {
		t.Fatalf("legacy attribution=%#v", legacy)
	}
}
