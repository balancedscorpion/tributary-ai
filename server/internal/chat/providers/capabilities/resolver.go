package capabilities

import (
	"errors"
	"strings"
	"time"
)

type Resolver struct {
	now func() time.Time
}

func NewResolver() *Resolver {
	return &Resolver{now: time.Now}
}

func (r *Resolver) Resolve(request ResolveRequest) (CapabilityReport, error) {
	target, err := normalizeTarget(request.Target)
	if err != nil {
		return CapabilityReport{}, err
	}
	if r == nil {
		r = NewResolver()
	}
	now := time.Now
	if r.now != nil {
		now = r.now
	}

	entry, cataloged := findCatalogEntry(target)
	if cataloged {
		target.ModelPattern = entry.ModelPattern
	}
	generatedAt := now().UTC()
	report := CapabilityReport{
		Target:         target,
		Fingerprint:    strings.TrimSpace(request.Fingerprint),
		CatalogVersion: CatalogVersion,
		GeneratedAt:    generatedAt.Format(time.RFC3339Nano),
		Features:       make(map[FeatureKey]FeatureResolution, len(featureRegistry)),
	}

	for _, definition := range FeatureRegistry() {
		feature := uncatalogedFeature()
		var evidence []CapabilityEvidence
		if cataloged {
			feature = cloneCatalogFeature(entry.Features[definition.Key])
			evidence = catalogEvidence(entry, generatedAt)
		}
		feature = applyModelRules(target, definition.Key, feature)
		if request.OverridePresent && !isCoreFeature(definition.Key) {
			feature = catalogFeature{
				State: StateUnknown,
				Reasons: []CapabilityExplanation{{
					Code: "override_present",
					Text: "Payload overrides or query parameters are present, so non-core capabilities cannot be verified.",
				}},
			}
		}
		feature = applyDynamicLimits(target, definition.Key, feature)
		report.Features[definition.Key] = resolveFeature(definition, feature, evidence, request)
	}
	return report, nil
}

func normalizeTarget(target TargetDescriptor) (TargetDescriptor, error) {
	target.ProviderFamily = strings.ToLower(strings.TrimSpace(target.ProviderFamily))
	target.APISurface = strings.ToLower(strings.TrimSpace(target.APISurface))
	target.EndpointFamily = strings.ToLower(strings.TrimSpace(target.EndpointFamily))
	target.Model = strings.TrimSpace(target.Model)
	target.ModelPattern = ""
	target.AdapterVersion = strings.TrimSpace(target.AdapterVersion)
	if target.AdapterVersion == "" {
		target.AdapterVersion = DefaultAdapterVersion
	}
	if target.ConnectionID != nil {
		value := strings.TrimSpace(*target.ConnectionID)
		if value == "" {
			target.ConnectionID = nil
		} else {
			target.ConnectionID = &value
		}
	}
	if target.Region != nil {
		value := strings.ToLower(strings.TrimSpace(*target.Region))
		if value == "" {
			target.Region = nil
		} else {
			target.Region = &value
		}
	}
	if target.ProviderFamily == "" {
		return TargetDescriptor{}, errors.New("provider family is required")
	}
	if target.APISurface == "" {
		return TargetDescriptor{}, errors.New("api surface is required")
	}
	if target.EndpointFamily == "" {
		return TargetDescriptor{}, errors.New("endpoint family is required")
	}
	if target.Model == "" {
		return TargetDescriptor{}, errors.New("model is required")
	}
	if !validSurfaceForFamily(target.ProviderFamily, target.APISurface) {
		return TargetDescriptor{}, errors.New("api surface is not valid for provider family")
	}
	return target, nil
}

func validSurfaceForFamily(family, surface string) bool {
	switch family {
	case "openai":
		return surface == "responses"
	case "anthropic":
		return surface == "messages"
	case "gemini":
		return surface == "generate_content"
	case "bedrock":
		return surface == "invoke_stream" || surface == "converse_stream" || surface == "chat_completions" || surface == "responses"
	case "compatible":
		return surface == "chat_completions" || surface == "responses"
	default:
		return false
	}
}

func uncatalogedFeature() catalogFeature {
	return catalogFeature{
		State: StateUnknown,
		Reasons: []CapabilityExplanation{{
			Code: "target_not_cataloged",
			Text: "No curated evidence matches this provider target and adapter version.",
		}},
	}
}

func applyModelRules(target TargetDescriptor, key FeatureKey, feature catalogFeature) catalogFeature {
	model := strings.ToLower(target.Model)
	if target.ProviderFamily == "openai" && strings.HasPrefix(model, "gpt-4o") && key == FeatureHostedWebSearch {
		return catalogFeature{
			State: StateUnknown,
			Reasons: []CapabilityExplanation{{
				Code: "model_not_verified",
				Text: "The product's gpt-4o web-search default is not verified against the current supported-model documentation.",
			}},
		}
	}
	return feature
}

func applyDynamicLimits(target TargetDescriptor, key FeatureKey, feature catalogFeature) catalogFeature {
	if key != FeatureContextLimits {
		return feature
	}
	var limits TokenLimits
	switch target.ProviderFamily {
	case "openai":
		limits = OpenAIModelTokenLimits(target.Model)
	case "anthropic", "bedrock":
		limits = AnthropicModelTokenLimits(target.Model)
	case "gemini":
		limits = GeminiModelTokenLimits(target.Model)
	default:
		return feature
	}
	if feature.Limits == nil {
		feature.Limits = &CapabilityLimits{}
	}
	feature.Limits.ContextTokens = intPtr(limits.MaxInputTokens)
	feature.Limits.MaxOutputTokens = intPtr(limits.MaxOutputTokens)
	return feature
}

func resolveFeature(definition FeatureDefinition, feature catalogFeature, evidence []CapabilityEvidence, request ResolveRequest) FeatureResolution {
	setting := request.Controls[definition.Key]
	control := (*CapabilityControl)(nil)
	if definition.Controllable {
		if definition.Class == CapabilityClassExplicitConsent {
			if setting != ControlOn {
				setting = ControlOff
			}
		} else if setting != ControlOn && setting != ControlOff {
			setting = ControlAuto
		}
		control = &CapabilityControl{Setting: setting, Controllable: true}
	}

	permission := (*CapabilityPermission)(nil)
	permissionDecision := request.Permissions[definition.Key]
	if definition.Class == CapabilityClassExplicitConsent {
		permission = &CapabilityPermission{Required: true, Granted: permissionDecision.Granted}
		if value := strings.TrimSpace(permissionDecision.PolicyRef); value != "" {
			permission.PolicyRef = &value
		}
	}

	effective := EffectiveUnavailable
	if feature.State == StateSupported || feature.State == StateConditional {
		if control != nil && control.Setting == ControlOff {
			effective = EffectiveOff
		} else if permission != nil && !permission.Granted {
			effective = EffectiveBlocked
			feature.Reasons = append(feature.Reasons, CapabilityExplanation{
				Code: "permission_blocked",
				Text: "The provider capability is configured on, but the permission policy does not allow it.",
			})
		} else {
			effective = EffectiveOn
		}
	}

	mode := feature.Mode
	if feature.State == StateUnknown || feature.State == StateUnsupported {
		mode = nil
	}
	return FeatureResolution{
		Meta:       definition.Meta,
		Class:      definition.Class,
		RiskTags:   append([]RiskTag{}, definition.RiskTags...),
		State:      feature.State,
		Mode:       mode,
		Effective:  effective,
		Control:    control,
		Permission: permission,
		Conditions: append([]CapabilityExplanation{}, feature.Conditions...),
		Reasons:    append([]CapabilityExplanation{}, feature.Reasons...),
		Limits:     cloneCapabilityLimits(feature.Limits),
		Detail:     cloneDetail(feature.Detail),
		Evidence:   append([]CapabilityEvidence{}, evidence...),
	}
}

func catalogEvidence(entry catalogEntry, generatedAt time.Time) []CapabilityEvidence {
	verifiedAt := entry.LastVerifiedAt
	entryID := entry.ID
	stale := false
	var staleReason *string
	if verified, err := time.Parse("2006-01-02", entry.LastVerifiedAt); err == nil && generatedAt.Sub(verified) > 30*24*time.Hour {
		stale = true
		reason := "age"
		staleReason = &reason
	}
	out := []CapabilityEvidence{{Kind: "catalog", VerifiedAt: &verifiedAt, Ref: &entryID, Stale: stale, StaleReason: staleReason}}
	for _, source := range entry.Sources {
		source := source
		out = append(out, CapabilityEvidence{Kind: "docs", VerifiedAt: &verifiedAt, Ref: &source, Stale: stale, StaleReason: staleReason})
	}
	return out
}

func cloneCatalogFeature(feature catalogFeature) catalogFeature {
	feature.Conditions = append([]CapabilityExplanation(nil), feature.Conditions...)
	feature.Reasons = append([]CapabilityExplanation(nil), feature.Reasons...)
	feature.Limits = cloneCapabilityLimits(feature.Limits)
	feature.Detail = cloneDetail(feature.Detail)
	if feature.Mode != nil {
		mode := *feature.Mode
		feature.Mode = &mode
	}
	return feature
}

func cloneCapabilityLimits(limits *CapabilityLimits) *CapabilityLimits {
	if limits == nil {
		return nil
	}
	out := *limits
	out.MIMETypes = append([]string(nil), limits.MIMETypes...)
	if limits.MaxBytes != nil {
		value := *limits.MaxBytes
		out.MaxBytes = &value
	}
	if limits.MaxCount != nil {
		value := *limits.MaxCount
		out.MaxCount = &value
	}
	if limits.RequestBudgetBytes != nil {
		value := *limits.RequestBudgetBytes
		out.RequestBudgetBytes = &value
	}
	if limits.ContextTokens != nil {
		value := *limits.ContextTokens
		out.ContextTokens = &value
	}
	if limits.MaxOutputTokens != nil {
		value := *limits.MaxOutputTokens
		out.MaxOutputTokens = &value
	}
	return &out
}

func cloneDetail(detail map[string]any) map[string]any {
	if detail == nil {
		return nil
	}
	out := make(map[string]any, len(detail))
	for key, value := range detail {
		out[key] = value
	}
	return out
}
