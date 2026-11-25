package config

import (
	"sync"
	"testing"
)

func TestDeriveProviderAPISurface(t *testing.T) {
	stringPtr := func(value string) *string { return &value }
	tests := []struct {
		name string
		cfg  ProviderConfig
		want string
	}{
		{"OpenAI", ProviderConfig{ProviderID: "openai"}, "responses"},
		{"Anthropic", ProviderConfig{ProviderID: "anthropic"}, "messages"},
		{"Gemini", ProviderConfig{ProviderID: "gemini"}, "generate_content"},
		{"Bedrock legacy messages", ProviderConfig{ProviderID: "bedrock", ApiType: stringPtr("messages")}, "invoke_stream"},
		{"Bedrock explicit Converse", ProviderConfig{ProviderID: "bedrock", ApiType: stringPtr("messages"), APISurface: stringPtr("converse_stream")}, "converse_stream"},
		{"Bedrock chat", ProviderConfig{ProviderID: "bedrock", ApiType: stringPtr("chat_completions")}, "chat_completions"},
		{"Bedrock responses", ProviderConfig{ProviderID: "bedrock", ApiType: stringPtr("responses")}, "responses"},
		{"Compatible chat default", ProviderConfig{ProviderID: "compatible"}, "chat_completions"},
		{"Compatible responses", ProviderConfig{ProviderID: "compatible", ApiType: stringPtr("responses")}, "responses"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DeriveProviderAPISurface(test.cfg); got != test.want {
				t.Fatalf("DeriveProviderAPISurface() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProviderConfigFingerprintIsStableAndEvidenceScoped(t *testing.T) {
	baseURL := " https://example.test/v1/ "
	region := " US-EAST-1 "
	auto := "auto"
	cfg := ProviderConfig{
		ID: "connection-a", DisplayName: "First label", ProviderID: "compatible", Model: "model-a",
		ApiType: stringPointer("responses"), BaseURL: &baseURL, Region: &region,
		ExtraHeaders: []HeaderKV{
			{Key: "X-Z", Value: "value-z"},
			{Key: "Authorization", Value: "secret-one", IsSecret: true},
			{Key: "X-A", Value: "value-a"},
		},
		QueryParams:        map[string]string{"b": " 2 ", "a": "1"},
		PayloadOverrides:   map[string]any{"nested": map[string]any{"z": float64(2), "a": true}},
		CapabilityControls: map[string]string{"native_compaction": auto},
		AttachmentPolicies: []ProviderAttachmentPolicy{{ID: "p1", Class: "office", Action: "text", APISurface: "responses", CreatedAt: 1}},
	}
	first := ProviderConfigFingerprint(cfg)

	reordered := cfg
	reordered.ID = "connection-b"
	reordered.DisplayName = "Renamed"
	reordered.ExtraHeaders = []HeaderKV{
		{Key: "X-A", Value: "value-a"},
		{Key: "Authorization", Value: "different-secret", IsSecret: true},
		{Key: "X-Z", Value: "value-z"},
	}
	reordered.QueryParams = map[string]string{"a": "1", "b": "2"}
	reordered.PayloadOverrides = map[string]any{"nested": map[string]any{"a": true, "z": float64(2)}}
	reordered.CapabilityControls = map[string]string{"native_compaction": "off"}
	reordered.AttachmentPolicies = nil
	if got := ProviderConfigFingerprint(reordered); got != first {
		t.Fatalf("metadata/preferences/map order changed fingerprint:\nfirst=%s\nsecond=%s", first, got)
	}

	changedModel := cfg
	changedModel.Model = "model-b"
	if got := ProviderConfigFingerprint(changedModel); got == first {
		t.Fatal("model change did not change fingerprint")
	}
	changedOverride := cfg
	changedOverride.PayloadOverrides = map[string]any{"nested": map[string]any{"z": float64(3), "a": true}}
	if got := ProviderConfigFingerprint(changedOverride); got == first {
		t.Fatal("payload override change did not change fingerprint")
	}
	changedRegion := cfg
	changedRegion.Region = stringPointer("eu-west-1")
	if got := ProviderConfigFingerprint(changedRegion); got == first {
		t.Fatal("region change did not change fingerprint")
	}
}

func TestSettingsSaveStampsCapabilityMetadataAndPreservesFlags(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.ProviderConfigs = []ProviderConfig{{
		ID: "anthropic-work", DisplayName: "Anthropic Work", ProviderID: "anthropic", Model: "claude-opus-4-6",
		CapabilityControls: map[string]string{"hosted_web_search": " ON ", "bad": "sometimes", " future_feature ": "auto"},
	}}
	settings.FeatureFlags.CapabilityUX = CapabilityUXFeatureFlags{
		Report: true, EditorV2: true, Controls: true, Preflight: true, ComposerTarget: true,
	}
	activeID := "anthropic-work"
	settings.ActiveProviderConfigID = &activeID
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := store.Get()
	if len(got.ProviderConfigs) != 1 {
		t.Fatalf("provider configs = %#v", got.ProviderConfigs)
	}
	cfg := got.ProviderConfigs[0]
	if cfg.APISurface == nil || *cfg.APISurface != "messages" {
		t.Fatalf("apiSurface = %#v, want messages", cfg.APISurface)
	}
	if cfg.ConfigFingerprint == nil || *cfg.ConfigFingerprint != ProviderConfigFingerprint(cfg) {
		t.Fatalf("configFingerprint = %#v, want stable computed value", cfg.ConfigFingerprint)
	}
	if cfg.CapabilityControls["hosted_web_search"] != "on" || cfg.CapabilityControls["future_feature"] != "auto" {
		t.Fatalf("capability controls = %#v", cfg.CapabilityControls)
	}
	if _, ok := cfg.CapabilityControls["bad"]; ok {
		t.Fatalf("invalid capability control survived: %#v", cfg.CapabilityControls)
	}
	if !got.FeatureFlags.CapabilityUX.Report || !got.FeatureFlags.CapabilityUX.EditorV2 || !got.FeatureFlags.CapabilityUX.Controls || !got.FeatureFlags.CapabilityUX.Preflight || !got.FeatureFlags.CapabilityUX.ComposerTarget {
		t.Fatalf("capability UX flags were not preserved: %#v", got.FeatureFlags.CapabilityUX)
	}
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	reloaded := store.Get()
	if reloaded.ProviderConfigs[0].ConfigFingerprint == nil || *reloaded.ProviderConfigs[0].ConfigFingerprint != *cfg.ConfigFingerprint || !reloaded.FeatureFlags.CapabilityUX.Preflight {
		t.Fatalf("disk round trip lost capability metadata: %#v", reloaded)
	}
}

func TestProviderConfigWebSearchReconcilesAcrossLegacyAndNewConfigs(t *testing.T) {
	enabled := true
	disabled := false
	responses := "responses"
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.ProviderConfigs = []ProviderConfig{
		{
			ID: "openai-legacy", DisplayName: "OpenAI Legacy", ProviderID: "openai", Model: "gpt-legacy",
		},
		{
			ID: "openai-new", DisplayName: "OpenAI New", ProviderID: "openai", Model: "gpt-new",
			CapabilityControls: map[string]string{},
		},
		{
			ID: "anthropic-legacy", DisplayName: "Anthropic Legacy", ProviderID: "anthropic", Model: "claude-legacy",
		},
		{
			ID: "anthropic-new", DisplayName: "Anthropic New", ProviderID: "anthropic", Model: "claude-new",
			CapabilityControls: map[string]string{" native_compaction ": " AUTO "},
		},
		{
			ID: "azure-responses", DisplayName: "Azure Responses", ProviderID: "compatible", Model: "gpt-5.5",
			ApiType:            &responses,
			CapabilityControls: map[string]string{"hosted_web_search": "off"},
			WebSearch:          &ProviderWebSearchConfig{Enabled: &disabled},
		},
		{
			ID: "bedrock-explicit", DisplayName: "Bedrock", ProviderID: "bedrock", Model: "anthropic.claude-test-v1",
			ApiType: stringPointer("messages"),
			CapabilityControls: map[string]string{
				"hosted_web_search":     "on",
				"hosted_code_execution": "on",
			},
			WebSearch:     &ProviderWebSearchConfig{Enabled: &enabled},
			CodeExecution: &ProviderCodeExecutionConfig{Enabled: &enabled},
		},
	}

	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	assertNormalized := func(stage string, got AppSettings) {
		t.Helper()
		byID := make(map[string]ProviderConfig, len(got.ProviderConfigs))
		for _, cfg := range got.ProviderConfigs {
			byID[cfg.ID] = cfg
		}

		// One legacy On value means the app-level Web Search connection is On.
		// Every supported target, including a previously-Off Azure Responses
		// target, must therefore receive the same explicit runtime state.
		for _, id := range []string{"openai-legacy", "openai-new", "anthropic-legacy", "anthropic-new", "azure-responses"} {
			cfg := byID[id]
			if cfg.WebSearch == nil || cfg.WebSearch.Enabled == nil || !*cfg.WebSearch.Enabled {
				t.Errorf("%s: %s WebSearch = %#v, want globally enabled mirror", stage, id, cfg.WebSearch)
			}
			if got := cfg.CapabilityControls["hosted_web_search"]; got != "on" {
				t.Errorf("%s: %s hosted_web_search control = %q, want on", stage, id, got)
			}
		}

		for _, id := range []string{"openai-legacy", "anthropic-legacy", "openai-new", "anthropic-new"} {
			cfg := byID[id]
			if cfg.CodeExecution != nil {
				t.Errorf("%s: %s CodeExecution = %#v, want Web Search migration not to enable code execution", stage, id, cfg.CodeExecution)
			}
		}
		if got := byID["anthropic-new"].CapabilityControls["native_compaction"]; got != "auto" {
			t.Errorf("%s: anthropic-new native_compaction control = %q, want auto", stage, got)
		}

		bedrock := byID["bedrock-explicit"]
		if bedrock.WebSearch != nil || bedrock.CodeExecution != nil {
			t.Errorf("%s: Bedrock hosted mirrors were not stripped: web=%#v code=%#v", stage, bedrock.WebSearch, bedrock.CodeExecution)
		}

		// App-level fields remain the compatibility mirror for older clients.
		if got.OpenAI == nil || got.OpenAI.WebSearch == nil || got.OpenAI.WebSearch.Enabled == nil || !*got.OpenAI.WebSearch.Enabled {
			t.Errorf("%s: app-level OpenAI legacy mirror lost its enabled default: %#v", stage, got.OpenAI)
		}
		if got.Anthropic == nil || got.Anthropic.WebSearch == nil || got.Anthropic.WebSearch.Enabled == nil || !*got.Anthropic.WebSearch.Enabled {
			t.Errorf("%s: app-level Anthropic legacy mirror lost its enabled default: %#v", stage, got.Anthropic)
		}
		if got.OpenAI.CodeExecution != nil || got.Anthropic.CodeExecution != nil {
			t.Errorf("%s: app-level code execution should remain Off: openai=%#v anthropic=%#v", stage, got.OpenAI.CodeExecution, got.Anthropic.CodeExecution)
		}
	}

	assertNormalized("after save", store.Get())
	if err := store.Load(); err != nil {
		t.Fatalf("Load after first save: %v", err)
	}
	assertNormalized("after first disk round trip", store.Get())
	if err := store.Save(store.Get()); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if err := store.Load(); err != nil {
		t.Fatalf("Load after second save: %v", err)
	}
	assertNormalized("after second disk round trip", store.Get())
}

func TestUpsertProviderAttachmentPolicyIsConcurrentAndIdempotent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.ProviderConfigs = []ProviderConfig{{
		ID: "bedrock-work", DisplayName: "Bedrock Work", ProviderID: "bedrock", Model: "anthropic.claude-opus-4-6-v1",
		ApiType: stringPointer("messages"), APISurface: stringPointer("converse_stream"),
	}}
	activeID := "bedrock-work"
	settings.ActiveProviderConfigID = &activeID
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	const workers = 12
	results := make(chan ProviderAttachmentPolicy, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			policy, err := store.UpsertProviderAttachmentPolicy("bedrock-work", ProviderAttachmentPolicy{
				Class: " BEDROCK_DOCUMENTS ", Action: " CONVERSE ",
			})
			if err != nil {
				errors <- err
				return
			}
			results <- policy
		}()
	}
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Errorf("UpsertProviderAttachmentPolicy: %v", err)
	}
	var first ProviderAttachmentPolicy
	for result := range results {
		if first.ID == "" {
			first = result
			continue
		}
		if result != first {
			t.Errorf("idempotent result = %#v, want %#v", result, first)
		}
	}
	if first.ID == "" || first.CreatedAt <= 0 || first.APISurface != "converse_stream" {
		t.Fatalf("created policy = %#v", first)
	}
	got := store.Get().ProviderConfigs[0].AttachmentPolicies
	if len(got) != 1 || got[0] != first {
		t.Fatalf("stored policies = %#v, want exactly %#v", got, first)
	}
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if reloaded := store.Get().ProviderConfigs[0].AttachmentPolicies; len(reloaded) != 1 || reloaded[0] != first {
		t.Fatalf("reloaded policies = %#v", reloaded)
	}
}

func TestDeleteProviderAttachmentPolicyIsPreciseAndIdempotent(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	settings := store.Get()
	settings.ProviderConfigs = []ProviderConfig{
		{ID: "connection-a", DisplayName: "Connection A", ProviderID: "compatible", Model: "model-a", ApiType: stringPointer("chat_completions")},
		{ID: "connection-b", DisplayName: "Connection B", ProviderID: "compatible", Model: "model-b", ApiType: stringPointer("chat_completions")},
	}
	activeID := "connection-a"
	settings.ActiveProviderConfigID = &activeID
	if err := store.Save(settings); err != nil {
		t.Fatalf("Save: %v", err)
	}

	createdPolicy, created, err := store.UpsertProviderAttachmentPolicyTracked("connection-a", ProviderAttachmentPolicy{
		Class: "office", Action: "text", APISurface: "chat_completions",
	})
	if err != nil {
		t.Fatalf("UpsertProviderAttachmentPolicyTracked create: %v", err)
	}
	if !created || createdPolicy.ID == "" {
		t.Fatalf("first tracked upsert = (%#v, created=%v), want a newly created policy", createdPolicy, created)
	}
	matchedPolicy, created, err := store.UpsertProviderAttachmentPolicyTracked("connection-a", ProviderAttachmentPolicy{
		Class: " OFFICE ", Action: " TEXT ", APISurface: " CHAT_COMPLETIONS ",
	})
	if err != nil {
		t.Fatalf("UpsertProviderAttachmentPolicyTracked existing: %v", err)
	}
	if created || matchedPolicy != createdPolicy {
		t.Fatalf("second tracked upsert = (%#v, created=%v), want existing %#v", matchedPolicy, created, createdPolicy)
	}

	deleted, err := store.DeleteProviderAttachmentPolicy("connection-b", createdPolicy.ID)
	if err != nil {
		t.Fatalf("DeleteProviderAttachmentPolicy wrong connection: %v", err)
	}
	if deleted {
		t.Fatal("DeleteProviderAttachmentPolicy removed a policy through the wrong connection")
	}
	if got := store.Get().ProviderConfigs[0].AttachmentPolicies; len(got) != 1 || got[0] != createdPolicy {
		t.Fatalf("wrong-connection delete changed source policy: %#v", got)
	}

	deleted, err = store.DeleteProviderAttachmentPolicy("connection-a", createdPolicy.ID)
	if err != nil {
		t.Fatalf("DeleteProviderAttachmentPolicy: %v", err)
	}
	if !deleted {
		t.Fatal("DeleteProviderAttachmentPolicy deleted=false, want true")
	}
	deleted, err = store.DeleteProviderAttachmentPolicy("connection-a", createdPolicy.ID)
	if err != nil {
		t.Fatalf("DeleteProviderAttachmentPolicy retry: %v", err)
	}
	if deleted {
		t.Fatal("DeleteProviderAttachmentPolicy retry deleted=true, want idempotent no-op")
	}
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := store.Get().ProviderConfigs[0].AttachmentPolicies; len(got) != 0 {
		t.Fatalf("deleted policy returned after reload: %#v", got)
	}
}

func stringPointer(value string) *string { return &value }
