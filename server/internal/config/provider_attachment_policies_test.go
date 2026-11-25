package config

import (
	"errors"
	"testing"
)

func TestApplyProviderAttachmentPoliciesCASAppliesBatchAtomically(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	apiType := "chat_completions"
	baseURL := "https://example.test/v1"
	settings := DefaultSettings()
	settings.ProviderConfigs = []ProviderConfig{{
		ID: "connection-a", ProviderID: "compatible", Model: "saved-model",
		ApiType: &apiType, BaseURL: &baseURL,
	}}
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	target := store.Get().ProviderConfigs[0]
	target.Model = "selected-model"

	validated, created, err := store.ApplyProviderAttachmentPoliciesCAS(
		target.ID, target.Model, ProviderConfigFingerprint(target), ProviderPlanPolicyFingerprint(target),
		[]ProviderAttachmentPolicy{
			{Class: " office ", Action: " text ", APISurface: " CHAT_COMPLETIONS "},
			{Class: "archive", Action: "extract", APISurface: "chat_completions"},
		},
	)
	if err != nil {
		t.Fatalf("ApplyProviderAttachmentPoliciesCAS: %v", err)
	}
	if len(created) != 2 || len(validated.ProviderConfigs[0].AttachmentPolicies) != 2 {
		t.Fatalf("created/validated policies = %#v / %#v", created, validated.ProviderConfigs[0].AttachmentPolicies)
	}
	if got := store.Get().ProviderConfigs[0].AttachmentPolicies; len(got) != 2 {
		t.Fatalf("persisted policy count = %d, want 2", len(got))
	}

	// Replaying the same semantic batch is idempotent and returns the current
	// immutable settings snapshot without claiming ownership of existing rows.
	replayTarget := store.Get().ProviderConfigs[0]
	replayTarget.Model = target.Model
	validated, created, err = store.ApplyProviderAttachmentPoliciesCAS(
		replayTarget.ID, replayTarget.Model, ProviderConfigFingerprint(replayTarget), ProviderPlanPolicyFingerprint(replayTarget),
		[]ProviderAttachmentPolicy{{Class: "office", Action: "text", APISurface: "chat_completions"}},
	)
	if err != nil {
		t.Fatalf("idempotent ApplyProviderAttachmentPoliciesCAS: %v", err)
	}
	if len(created) != 0 || len(validated.ProviderConfigs[0].AttachmentPolicies) != 2 {
		t.Fatalf("idempotent created/validated policies = %#v / %#v", created, validated.ProviderConfigs[0].AttachmentPolicies)
	}
}

func TestApplyProviderAttachmentPoliciesCASRejectsStalePolicyAndInvalidBatchWithoutPartialWrite(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	apiType := "responses"
	baseURL := "https://example.test/v1"
	settings := DefaultSettings()
	settings.ProviderConfigs = []ProviderConfig{{
		ID: "connection-a", ProviderID: "openai", Model: "gpt-test",
		ApiType: &apiType, BaseURL: &baseURL,
		CapabilityControls: map[string]string{"hosted_web_search": "off"},
	}}
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	planned := store.Get().ProviderConfigs[0]
	plannedRouting := ProviderConfigFingerprint(planned)
	plannedPolicy := ProviderPlanPolicyFingerprint(planned)

	changed := store.Get()
	changed.ProviderConfigs[0].CapabilityControls["hosted_web_search"] = "on"
	if err := store.Save(changed); err != nil {
		t.Fatalf("Save policy mutation: %v", err)
	}
	_, _, err = store.ApplyProviderAttachmentPoliciesCAS(
		planned.ID, planned.Model, plannedRouting, plannedPolicy,
		[]ProviderAttachmentPolicy{{Class: "office", Action: "text", APISurface: "responses"}},
	)
	if !errors.Is(err, ErrProviderConfigChanged) {
		t.Fatalf("stale policy error = %v, want ErrProviderConfigChanged", err)
	}
	if got := store.Get().ProviderConfigs[0].AttachmentPolicies; len(got) != 0 {
		t.Fatalf("stale policy mutation partially persisted batch: %#v", got)
	}

	live := store.Get().ProviderConfigs[0]
	_, _, err = store.ApplyProviderAttachmentPoliciesCAS(
		live.ID, live.Model, ProviderConfigFingerprint(live), ProviderPlanPolicyFingerprint(live),
		[]ProviderAttachmentPolicy{
			{Class: "office", Action: "text", APISurface: "responses"},
			{Class: "archive", Action: "", APISurface: "responses"},
		},
	)
	if err == nil {
		t.Fatal("invalid second policy unexpectedly succeeded")
	}
	if got := store.Get().ProviderConfigs[0].AttachmentPolicies; len(got) != 0 {
		t.Fatalf("invalid batch partially persisted first policy: %#v", got)
	}

	deletedTarget := store.Get().ProviderConfigs[0]
	withoutTarget := store.Get()
	withoutTarget.ProviderConfigs = nil
	if err := store.Save(withoutTarget); err != nil {
		t.Fatalf("Save deleted connection: %v", err)
	}
	_, _, err = store.ApplyProviderAttachmentPoliciesCAS(
		deletedTarget.ID, deletedTarget.Model, ProviderConfigFingerprint(deletedTarget), ProviderPlanPolicyFingerprint(deletedTarget),
		[]ProviderAttachmentPolicy{{Class: "office", Action: "text", APISurface: "responses"}},
	)
	if !errors.Is(err, ErrProviderConfigChanged) {
		t.Fatalf("deleted target error = %v, want ErrProviderConfigChanged", err)
	}
}
