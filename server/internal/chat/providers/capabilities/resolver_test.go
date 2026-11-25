package capabilities

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestFeatureRegistryCoversV1Inventory(t *testing.T) {
	want := []FeatureKey{
		FeatureTextStreaming, FeatureTextNonStreaming, FeatureSystemInstructions,
		FeatureCustomTools, FeatureToolChoice, FeatureStrictToolSchemas, FeatureParallelTools, FeatureToolStatePreservation,
		FeatureImages, FeatureDocuments,
		FeatureHostedWebSearch, FeatureHostedCodeExecution, FeatureHostedComputerUse, FeatureProviderGeneratedFiles,
		FeatureReasoningRequestControl, FeatureThinkingDisplay, FeatureThinkingStatePreservation,
		FeatureStructuredOutput, FeatureCitations,
		FeatureNativeCompaction, FeatureSessionContinuation, FeaturePromptCaching, FeatureCacheReporting,
		FeatureTokenCounting, FeatureContextLimits, FeatureRichUsage,
		FeatureSafetyControls,
	}
	registry := FeatureRegistry()
	if len(registry) != len(want) {
		t.Fatalf("registry count = %d, want %d", len(registry), len(want))
	}
	seen := map[FeatureKey]bool{}
	for index, definition := range registry {
		if definition.Key != want[index] {
			t.Errorf("registry[%d] key = %q, want %q", index, definition.Key, want[index])
		}
		if seen[definition.Key] {
			t.Errorf("duplicate feature key %q", definition.Key)
		}
		seen[definition.Key] = true
		if definition.Meta.Label == "" || definition.Meta.Group == "" || definition.Meta.Order != index {
			t.Errorf("invalid metadata for %q: %#v", definition.Key, definition.Meta)
		}
	}
	for _, key := range []FeatureKey{FeatureHostedWebSearch, FeatureHostedCodeExecution, FeatureHostedComputerUse} {
		definition, _ := featureDefinition(key)
		if definition.Class != CapabilityClassExplicitConsent || !definition.Controllable || len(definition.RiskTags) == 0 {
			t.Errorf("explicit-consent feature %q incorrectly classified: %#v", key, definition)
		}
	}
}

func TestCuratedCatalogClassifiesEveryRegisteredFeature(t *testing.T) {
	entries := curatedCatalog()
	if len(entries) != 14 {
		t.Fatalf("catalog entries = %d, want 9 runtime surfaces plus 5 compatible presets", len(entries))
	}
	seenIDs := map[string]struct{}{}
	for _, entry := range entries {
		if _, ok := seenIDs[entry.ID]; ok {
			t.Errorf("duplicate catalog entry id %q", entry.ID)
		}
		seenIDs[entry.ID] = struct{}{}
		if entry.LastVerifiedAt != "2026-07-11" || len(entry.Sources) == 0 {
			t.Errorf("entry %q lacks dated sources: %#v", entry.ID, entry)
		}
		for _, definition := range FeatureRegistry() {
			feature, ok := entry.Features[definition.Key]
			if !ok {
				t.Errorf("entry %q missing feature %q", entry.ID, definition.Key)
				continue
			}
			switch feature.State {
			case StateSupported, StateUnsupported, StateConditional, StateUnknown:
			default:
				t.Errorf("entry %q feature %q has invalid state %q", entry.ID, definition.Key, feature.State)
			}
		}
	}
}

func TestResolverAzureOpenAIResponsesAllowsExplicitWebSearch(t *testing.T) {
	report := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "compatible", APISurface: "responses", EndpointFamily: "azure-openai",
		Model: "gpt-5.5", AdapterVersion: DefaultAdapterVersion,
	}, map[FeatureKey]ControlSetting{
		FeatureHostedWebSearch: ControlOn,
	}, map[FeatureKey]PermissionDecision{
		FeatureHostedWebSearch: {Granted: true},
	}, false)

	feature := report.Features[FeatureHostedWebSearch]
	if feature.State != StateConditional || feature.Effective != EffectiveOn ||
		!hasExplanationCode(feature.Conditions, "model_dependent") ||
		!hasEvidenceRef(feature.Evidence, "compatible.azure-openai-responses.repository.v1") {
		t.Fatalf("Azure OpenAI web search = %#v, want enabled model-dependent catalog support", feature)
	}
}

func TestResolverBedrockSurfacesAreSafeAndDocumentExact(t *testing.T) {
	resolver := fixedResolver()
	for _, surface := range []string{"invoke_stream", "converse_stream", "chat_completions", "responses"} {
		endpointFamily := "bedrock-runtime"
		if surface == "responses" {
			endpointFamily = "bedrock-mantle"
		}
		report := resolveForTest(t, resolver, TargetDescriptor{
			ProviderFamily: "bedrock", APISurface: surface, EndpointFamily: endpointFamily,
			Model: "anthropic.claude-opus-4-6-v1", AdapterVersion: DefaultAdapterVersion,
		}, map[FeatureKey]ControlSetting{
			FeatureHostedWebSearch: ControlOn, FeatureHostedCodeExecution: ControlOn,
		}, map[FeatureKey]PermissionDecision{
			FeatureHostedWebSearch: {Granted: true}, FeatureHostedCodeExecution: {Granted: true},
		}, false)
		for _, key := range []FeatureKey{FeatureHostedWebSearch, FeatureHostedCodeExecution} {
			feature := report.Features[key]
			if feature.State != StateUnsupported || feature.Effective != EffectiveUnavailable || !hasExplanationCode(feature.Reasons, "not_on_bedrock") {
				t.Errorf("surface %q feature %q = %#v, want unsupported/not_on_bedrock", surface, key, feature)
			}
		}
	}

	invoke := resolveForTest(t, resolver, TargetDescriptor{
		ProviderFamily: "bedrock", APISurface: "invoke_stream", EndpointFamily: "bedrock-runtime",
		Model: "anthropic.claude-opus-4-6-v1", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false).Features[FeatureDocuments]
	if invoke.State != StateConditional || !hasExplanationCode(invoke.Conditions, "requires_converse_switch") || !hasExplanationCode(invoke.Reasons, "requires_converse_switch") {
		t.Fatalf("Invoke documents = %#v, want explicit Converse switch condition", invoke)
	}

	converse := resolveForTest(t, resolver, TargetDescriptor{
		ProviderFamily: "bedrock", APISurface: "converse_stream", EndpointFamily: "bedrock-runtime",
		Model: "anthropic.claude-opus-4-6-v1", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false).Features[FeatureDocuments]
	if converse.State != StateConditional || converse.Mode == nil || *converse.Mode != ModeNative || converse.Limits == nil ||
		converse.Limits.MaxCount == nil || *converse.Limits.MaxCount != 5 ||
		converse.Limits.MaxBytes == nil || *converse.Limits.MaxBytes != 4718592 {
		t.Fatalf("Converse documents = %#v, want native max 5 / 4.5 MiB", converse)
	}
}

func TestResolverBedrockNormalizedEndpointsNeedNoCorrection(t *testing.T) {
	for _, test := range []struct {
		surface        string
		endpointFamily string
	}{
		{surface: "chat_completions", endpointFamily: "bedrock-runtime"},
		{surface: "responses", endpointFamily: "bedrock-mantle"},
	} {
		report := resolveForTest(t, fixedResolver(), TargetDescriptor{
			ProviderFamily: "bedrock", APISurface: test.surface, EndpointFamily: test.endpointFamily,
			Model: "openai.gpt-oss-120b-1:0", AdapterVersion: DefaultAdapterVersion,
		}, nil, nil, false)
		for key, feature := range report.Features {
			if hasExplanationCode(feature.Conditions, "requires_endpoint_correction") || hasExplanationCode(feature.Reasons, "requires_endpoint_correction") {
				t.Errorf("surface %q feature %q retained obsolete endpoint-correction guidance: %#v", test.surface, key, feature)
			}
		}
	}

	wrongSurfaceEndpoint := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "bedrock", APISurface: "responses", EndpointFamily: "bedrock-runtime",
		Model: "openai.gpt-oss-120b-1:0", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false)
	if feature := wrongSurfaceEndpoint.Features[FeatureTextStreaming]; feature.State != StateUnknown || !hasExplanationCode(feature.Reasons, "target_not_cataloged") {
		t.Fatalf("Responses on the Runtime endpoint = %#v, want uncataloged/unknown", feature)
	}
}

func TestResolverGenericCompatibleFailsClosedBeyondStreaming(t *testing.T) {
	for _, surface := range []string{"chat_completions", "responses"} {
		report := resolveForTest(t, fixedResolver(), TargetDescriptor{
			ProviderFamily: "compatible", APISurface: surface, EndpointFamily: "compatible-custom",
			Model: "vendor-model", AdapterVersion: DefaultAdapterVersion,
		}, nil, nil, false)
		if streaming := report.Features[FeatureTextStreaming]; streaming.State != StateSupported || streaming.Effective != EffectiveOn {
			t.Errorf("surface %q text streaming = %#v, want catalog-backed transport support", surface, streaming)
		}
		for _, key := range []FeatureKey{
			FeatureSystemInstructions, FeatureCustomTools, FeatureParallelTools,
			FeatureImages, FeatureDocuments, FeatureReasoningRequestControl, FeatureContextLimits,
		} {
			feature := report.Features[key]
			if feature.State != StateUnknown || feature.Effective != EffectiveUnavailable || !hasExplanationCode(feature.Reasons, "endpoint_model_unverified") {
				t.Errorf("surface %q feature %q = %#v, want endpoint/model-unverified", surface, key, feature)
			}
		}
		if structured := report.Features[FeatureStructuredOutput]; structured.State != StateUnsupported || !hasExplanationCode(structured.Reasons, "adapter_gap") {
			t.Errorf("surface %q structured output = %#v, want known adapter gap", surface, structured)
		}
	}
}

func TestResolverCompatiblePresetCatalogsUseStableEndpointFamilies(t *testing.T) {
	for _, test := range []struct {
		endpointFamily string
		catalogID      string
		images         SupportState
	}{
		{endpointFamily: "groq", catalogID: "compatible.groq-chat.repository.v1", images: StateUnknown},
		{endpointFamily: "together", catalogID: "compatible.together-chat.repository.v1", images: StateConditional},
		{endpointFamily: "fireworks", catalogID: "compatible.fireworks-chat.repository.v1", images: StateConditional},
		{endpointFamily: "openrouter", catalogID: "compatible.openrouter-chat.repository.v1", images: StateUnknown},
	} {
		report := resolveForTest(t, fixedResolver(), TargetDescriptor{
			ProviderFamily: "compatible", APISurface: "chat_completions", EndpointFamily: test.endpointFamily,
			Model: "preset-model", AdapterVersion: DefaultAdapterVersion,
		}, nil, nil, false)
		if system := report.Features[FeatureSystemInstructions]; system.State != StateSupported || !hasEvidenceRef(system.Evidence, test.catalogID) {
			t.Errorf("preset %q system instructions = %#v, want catalog-backed supported", test.endpointFamily, system)
		}
		if tools := report.Features[FeatureCustomTools]; tools.State != StateConditional || !hasExplanationCode(tools.Conditions, "model_dependent") || !hasEvidenceRef(tools.Evidence, test.catalogID) {
			t.Errorf("preset %q custom tools = %#v, want model-dependent catalog row", test.endpointFamily, tools)
		}
		if images := report.Features[FeatureImages]; images.State != test.images {
			t.Errorf("preset %q images state = %q, want %q", test.endpointFamily, images.State, test.images)
		}
	}
}

func TestResolverFirstPartyCatalogRequiresFirstPartyEndpoint(t *testing.T) {
	report := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "openai", APISurface: "responses", EndpointFamily: "custom:proxy.example",
		Model: "gpt-5", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false)
	for key, feature := range report.Features {
		if feature.State != StateUnknown || !hasExplanationCode(feature.Reasons, "target_not_cataloged") {
			t.Errorf("custom endpoint feature %q = %#v, want uncataloged/unknown", key, feature)
		}
	}
}

func TestResolverComposesControlAndPermissionGates(t *testing.T) {
	target := TargetDescriptor{
		ProviderFamily: "anthropic", APISurface: "messages", EndpointFamily: "anthropic-api",
		Model: "claude-opus-4-6", AdapterVersion: DefaultAdapterVersion,
	}
	resolver := fixedResolver()

	blocked := resolveForTest(t, resolver, target,
		map[FeatureKey]ControlSetting{FeatureHostedCodeExecution: ControlOn},
		map[FeatureKey]PermissionDecision{FeatureHostedCodeExecution: {Granted: false, PolicyRef: "deny-code"}}, false,
	).Features[FeatureHostedCodeExecution]
	if blocked.Effective != EffectiveBlocked || blocked.Permission == nil || blocked.Permission.Granted || blocked.Permission.PolicyRef == nil || *blocked.Permission.PolicyRef != "deny-code" || !hasExplanationCode(blocked.Reasons, "permission_blocked") {
		t.Fatalf("blocked hosted code = %#v", blocked)
	}

	off := resolveForTest(t, resolver, target,
		map[FeatureKey]ControlSetting{FeatureHostedCodeExecution: ControlOff},
		map[FeatureKey]PermissionDecision{FeatureHostedCodeExecution: {Granted: false}}, false,
	).Features[FeatureHostedCodeExecution]
	if off.Effective != EffectiveOff || off.Control == nil || off.Control.Setting != ControlOff {
		t.Fatalf("off hosted code = %#v", off)
	}

	on := resolveForTest(t, resolver, target,
		map[FeatureKey]ControlSetting{FeatureHostedCodeExecution: ControlOn},
		map[FeatureKey]PermissionDecision{FeatureHostedCodeExecution: {Granted: true, PolicyRef: "allow-code"}}, false,
	).Features[FeatureHostedCodeExecution]
	if on.Effective != EffectiveOn {
		t.Fatalf("allowed hosted code effective = %q, want on", on.Effective)
	}
}

func TestResolverOverridesForceEveryNonCoreFeatureUnknown(t *testing.T) {
	report := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "openai", APISurface: "responses", EndpointFamily: "openai-api",
		Model: "gpt-5", AdapterVersion: DefaultAdapterVersion,
	}, map[FeatureKey]ControlSetting{FeatureHostedWebSearch: ControlOn},
		map[FeatureKey]PermissionDecision{FeatureHostedWebSearch: {Granted: true}}, true)
	for _, definition := range FeatureRegistry() {
		feature := report.Features[definition.Key]
		if definition.Meta.Group == "core" {
			if feature.State == StateUnknown {
				t.Errorf("core feature %q was incorrectly invalidated", definition.Key)
			}
			continue
		}
		if feature.State != StateUnknown || feature.Effective != EffectiveUnavailable || !hasExplanationCode(feature.Reasons, "override_present") {
			t.Errorf("override feature %q = %#v", definition.Key, feature)
		}
	}
}

func TestResolverAdapterVersionMismatchReturnsHonestUnknown(t *testing.T) {
	report := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "openai", APISurface: "responses", EndpointFamily: "openai-api",
		Model: "gpt-5", AdapterVersion: "future-adapter/2",
	}, nil, nil, false)
	if report.Target.ModelPattern != "" {
		t.Fatalf("model pattern = %q, want empty for unmatched target", report.Target.ModelPattern)
	}
	for key, feature := range report.Features {
		if feature.State != StateUnknown || !hasExplanationCode(feature.Reasons, "target_not_cataloged") || len(feature.Evidence) != 0 {
			t.Errorf("feature %q = %#v, want uncataloged unknown", key, feature)
		}
	}
}

func TestResolverGemini3ReflectsRepairedContinuationAndImagePath(t *testing.T) {
	report := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "gemini", APISurface: "generate_content", EndpointFamily: "gemini-developer",
		Model: "gemini-3.5-flash", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false)
	for _, key := range []FeatureKey{FeatureCustomTools, FeatureToolStatePreservation, FeatureThinkingStatePreservation} {
		feature := report.Features[key]
		if feature.State != StateSupported || feature.Effective != EffectiveOn {
			t.Errorf("Gemini 3 feature %q = %#v, want supported/on after signed-part replay repair", key, feature)
		}
	}
	parallel := report.Features[FeatureParallelTools]
	if parallel.State != StateConditional || !hasExplanationCode(parallel.Conditions, "serial_execution") {
		t.Errorf("Gemini parallel tools = %#v", parallel)
	}
	images := report.Features[FeatureImages]
	if images.State != StateConditional || images.Limits == nil || len(images.Limits.MIMETypes) != 6 || !hasExplanationCode(images.Conditions, "gif_unverified") {
		t.Errorf("Gemini images = %#v", images)
	}
}

func TestResolverEvidenceAgeIsStaleWithoutChangingSupport(t *testing.T) {
	resolver := &Resolver{now: func() time.Time { return time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC) }}
	report := resolveForTest(t, resolver, TargetDescriptor{
		ProviderFamily: "openai", APISurface: "responses", EndpointFamily: "openai-api",
		Model: "gpt-5", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false)
	feature := report.Features[FeatureTextStreaming]
	if feature.State != StateSupported || len(feature.Evidence) < 2 {
		t.Fatalf("text streaming = %#v", feature)
	}
	for _, evidence := range feature.Evidence {
		if !evidence.Stale || evidence.StaleReason == nil || *evidence.StaleReason != "age" {
			t.Errorf("evidence = %#v, want age-stale", evidence)
		}
	}
}

func TestCapabilityReportJSONUsesOpenFeatureMapAndNullContract(t *testing.T) {
	report := resolveForTest(t, fixedResolver(), TargetDescriptor{
		ProviderFamily: "anthropic", APISurface: "messages", EndpointFamily: "anthropic-api",
		Model: "claude-opus-4-6", AdapterVersion: DefaultAdapterVersion,
	}, nil, nil, false)
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(raw)
	for _, needle := range []string{`"connectionId":null`, `"region":null`, `"features":{`, `"riskTags":[]`, `"conditions":[]`, `"limits":null`, `"detail":null`} {
		if !strings.Contains(text, needle) {
			t.Errorf("JSON missing %s: %s", needle, text)
		}
	}
}

func fixedResolver() *Resolver {
	return &Resolver{now: func() time.Time { return time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC) }}
}

func resolveForTest(t *testing.T, resolver *Resolver, target TargetDescriptor, controls map[FeatureKey]ControlSetting, permissions map[FeatureKey]PermissionDecision, override bool) CapabilityReport {
	t.Helper()
	report, err := resolver.Resolve(ResolveRequest{
		Target: target, Fingerprint: "test-fingerprint", Controls: controls,
		Permissions: permissions, OverridePresent: override,
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(report.Features) != len(FeatureRegistry()) {
		t.Fatalf("features = %d, want %d", len(report.Features), len(FeatureRegistry()))
	}
	return report
}

func hasExplanationCode(values []CapabilityExplanation, code string) bool {
	for _, value := range values {
		if value.Code == code {
			return true
		}
	}
	return false
}

func hasEvidenceRef(values []CapabilityEvidence, ref string) bool {
	for _, value := range values {
		if value.Ref != nil && *value.Ref == ref {
			return true
		}
	}
	return false
}
