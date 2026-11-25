package capabilityruntime

import (
	"context"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/config"
)

func TestResolveMergesOnlyCurrentFingerprintProbeEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	apiType := "chat_completions"
	baseURL := "https://example.test/v1"
	cfg := config.ProviderConfig{
		ID: "compatible-one", ProviderID: "compatible", Model: "example-model",
		ApiType: &apiType, BaseURL: &baseURL,
	}
	fingerprint := config.ProviderConfigFingerprint(cfg)
	checkedAt := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	if _, err := ledger.UpsertProviderCapabilityEvidence(ctx, store.ProviderCapabilityEvidence{
		ConnectionID: cfg.ID, Model: cfg.Model, Fingerprint: fingerprint,
		Probe: string(capabilities.FeatureCustomTools), Verdict: "verified",
		Message: "Two-dispatch tool loop completed.", VerifiedAt: checkedAt,
	}); err != nil {
		t.Fatalf("UpsertProviderCapabilityEvidence: %v", err)
	}

	service := NewService(ledger, nil)
	service.Now = func() time.Time { return checkedAt.Add(24 * time.Hour) }
	report, err := service.Resolve(ctx, ResolveInput{Config: cfg, ConnectionID: &cfg.ID})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	feature := report.Features[capabilities.FeatureCustomTools]
	if feature.State != capabilities.StateSupported || feature.Effective != capabilities.EffectiveOn {
		t.Fatalf("current evidence resolution = state %q effective %q", feature.State, feature.Effective)
	}
	if !hasProbeEvidence(feature, false, "") {
		t.Fatalf("current probe evidence missing: %#v", feature.Evidence)
	}

	changed := cfg
	changedBaseURL := "https://changed.example.test/v1"
	changed.BaseURL = &changedBaseURL
	staleReport, err := service.Resolve(ctx, ResolveInput{Config: changed, ConnectionID: &cfg.ID})
	if err != nil {
		t.Fatalf("Resolve changed config: %v", err)
	}
	staleFeature := staleReport.Features[capabilities.FeatureCustomTools]
	if staleFeature.State != capabilities.StateUnknown {
		t.Fatalf("fingerprint-mismatched probe changed support to %q, want unknown", staleFeature.State)
	}
	if !hasProbeEvidence(staleFeature, true, "fingerprint") {
		t.Fatalf("stale probe evidence missing: %#v", staleFeature.Evidence)
	}
}

func TestResolveWebSearchConnectionProvidesItsOwnConsentGrant(t *testing.T) {
	t.Parallel()
	apiType := "responses"
	baseURL := "https://api.openai.com/v1"
	enabled := true
	cfg := config.ProviderConfig{
		ID: "openai-one", ProviderID: "openai", Model: "gpt-5",
		ApiType: &apiType, BaseURL: &baseURL,
		CapabilityControls: map[string]string{string(capabilities.FeatureHostedWebSearch): "on"},
		WebSearch:          &config.ProviderWebSearchConfig{Enabled: &enabled},
	}
	report, err := NewService(nil, nil).Resolve(context.Background(), ResolveInput{Config: cfg, ConnectionID: &cfg.ID})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	feature := report.Features[capabilities.FeatureHostedWebSearch]
	if feature.Control == nil || feature.Control.Setting != capabilities.ControlOn {
		t.Fatalf("control = %#v, want on", feature.Control)
	}
	if feature.Permission == nil || !feature.Permission.Required || !feature.Permission.Granted {
		t.Fatalf("permission = %#v, want the enabled connection to grant consent", feature.Permission)
	}
	if feature.Effective != capabilities.EffectiveOn {
		t.Fatalf("effective = %q, want on", feature.Effective)
	}
}

func TestResolveKeepsWebSearchOnDespiteCachedUnsupportedProbe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	ledger, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	apiType := "responses"
	baseURL := "https://example.services.ai.azure.com/openai/v1"
	enabled := true
	cfg := config.ProviderConfig{
		ID: "azure-one", ProviderID: "compatible", Model: "gpt-5.5",
		ApiType: &apiType, BaseURL: &baseURL,
		CapabilityControls: map[string]string{string(capabilities.FeatureHostedWebSearch): "on"},
		WebSearch:          &config.ProviderWebSearchConfig{Enabled: &enabled},
	}
	fingerprint := config.ProviderConfigFingerprint(cfg)
	if _, err := ledger.UpsertProviderCapabilityEvidence(ctx, store.ProviderCapabilityEvidence{
		ConnectionID: cfg.ID, Model: cfg.Model, Fingerprint: fingerprint,
		Probe: string(capabilities.FeatureHostedWebSearch), Verdict: "unsupported",
		Message: "Old build cached a failed check.", VerifiedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("UpsertProviderCapabilityEvidence: %v", err)
	}

	report, err := NewService(ledger, nil).Resolve(ctx, ResolveInput{Config: cfg, ConnectionID: &cfg.ID})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	feature := report.Features[capabilities.FeatureHostedWebSearch]
	if feature.State == capabilities.StateUnsupported || feature.Effective != capabilities.EffectiveOn {
		t.Fatalf("cached probe suppressed Web Search: state=%q effective=%q", feature.State, feature.Effective)
	}
}

func TestEndpointFamilyDistinguishesBedrockMantle(t *testing.T) {
	t.Parallel()
	apiType := "responses"
	baseURL := "https://bedrock-mantle.eu-west-2.api.aws/v1"
	cfg := config.ProviderConfig{ProviderID: "bedrock", ApiType: &apiType, BaseURL: &baseURL}
	if got := EndpointFamily(cfg); got != "bedrock-mantle" {
		t.Fatalf("EndpointFamily = %q, want bedrock-mantle", got)
	}
}

func TestEndpointFamilyRecognizesAzureOpenAI(t *testing.T) {
	t.Parallel()
	apiType := "responses"
	for _, baseURL := range []string{
		"https://example.openai.azure.com/openai/v1",
		"https://example.services.ai.azure.com/openai/v1",
		"https://example.cognitiveservices.azure.com/openai/v1",
	} {
		cfg := config.ProviderConfig{ProviderID: "compatible", ApiType: &apiType, BaseURL: &baseURL}
		if got := EndpointFamily(cfg); got != "azure-openai" {
			t.Errorf("EndpointFamily(%q) = %q, want azure-openai", baseURL, got)
		}
	}
}

func hasProbeEvidence(feature capabilities.FeatureResolution, stale bool, staleReason string) bool {
	for _, evidence := range feature.Evidence {
		if evidence.Kind != "probe" || evidence.Stale != stale {
			continue
		}
		if staleReason == "" && evidence.StaleReason == nil {
			return true
		}
		if evidence.StaleReason != nil && *evidence.StaleReason == staleReason {
			return true
		}
	}
	return false
}
