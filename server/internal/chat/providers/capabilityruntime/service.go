package capabilityruntime

import (
	"context"
	"net/url"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/store"
	chattools "ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/permissions"
)

const EvidenceFreshnessWindow = 30 * 24 * time.Hour

// Service composes the static capability catalog with connection preferences,
// permission decisions, and durable probe evidence. It is shared by settings
// reports and run preflight so those two paths cannot disagree about a target.
type Service struct {
	Evidence    store.Store
	Permissions *permissions.Service
	Now         func() time.Time
}

type ResolveInput struct {
	Config       config.ProviderConfig
	ConnectionID *string
	ThreadID     string
}

func NewService(evidence store.Store, permissionService *permissions.Service) *Service {
	return &Service{
		Evidence:    evidence,
		Permissions: permissionService,
		Now:         func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Resolve(ctx context.Context, input ResolveInput) (capabilities.CapabilityReport, error) {
	cfg := input.Config
	target := TargetForConfig(cfg, input.ConnectionID)
	permissionDecisions, err := resolvePermissions(ctx, target, strings.TrimSpace(input.ThreadID), s.permissionService())
	if err != nil {
		return capabilities.CapabilityReport{}, err
	}
	report, err := capabilities.NewResolver().Resolve(capabilities.ResolveRequest{
		Target:          target,
		Fingerprint:     config.ProviderConfigFingerprint(cfg),
		Controls:        ControlsForConfig(cfg),
		Permissions:     permissionDecisions,
		OverridePresent: len(cfg.PayloadOverrides) > 0 || len(cfg.QueryParams) > 0,
	})
	if err != nil {
		return capabilities.CapabilityReport{}, err
	}
	if s == nil || s.Evidence == nil || input.ConnectionID == nil || strings.TrimSpace(*input.ConnectionID) == "" {
		return report, nil
	}
	evidence, err := s.Evidence.ListProviderCapabilityEvidence(ctx, store.ProviderCapabilityEvidenceFilter{
		ConnectionID: strings.TrimSpace(*input.ConnectionID),
		Model:        strings.TrimSpace(cfg.Model),
		Limit:        1000,
	})
	if err != nil {
		return capabilities.CapabilityReport{}, err
	}
	mergeProbeEvidence(&report, evidence, s.now())
	return report, nil
}

func (s *Service) permissionService() *permissions.Service {
	if s == nil {
		return nil
	}
	return s.Permissions
}

func (s *Service) now() time.Time {
	if s != nil && s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func TargetForConfig(cfg config.ProviderConfig, connectionID *string) capabilities.TargetDescriptor {
	return capabilities.TargetDescriptor{
		ConnectionID:   normalizedConnectionID(connectionID),
		ProviderFamily: strings.ToLower(strings.TrimSpace(cfg.ProviderID)),
		APISurface:     config.DeriveProviderAPISurface(cfg),
		EndpointFamily: EndpointFamily(cfg),
		Model:          strings.TrimSpace(cfg.Model),
		Region:         normalizedStringPtr(cfg.Region),
		AdapterVersion: capabilities.DefaultAdapterVersion,
	}
}

func ControlsForConfig(cfg config.ProviderConfig) map[capabilities.FeatureKey]capabilities.ControlSetting {
	out := make(map[capabilities.FeatureKey]capabilities.ControlSetting, len(cfg.CapabilityControls)+3)
	for rawKey, rawValue := range cfg.CapabilityControls {
		key := capabilities.FeatureKey(strings.TrimSpace(rawKey))
		value := capabilities.ControlSetting(strings.ToLower(strings.TrimSpace(rawValue)))
		if key == "" || (value != capabilities.ControlAuto && value != capabilities.ControlOn && value != capabilities.ControlOff) {
			continue
		}
		out[key] = value
	}
	if _, ok := out[capabilities.FeatureHostedWebSearch]; !ok {
		out[capabilities.FeatureHostedWebSearch] = enabledControl(cfg.WebSearch != nil && cfg.WebSearch.Enabled != nil && *cfg.WebSearch.Enabled)
	}
	if _, ok := out[capabilities.FeatureHostedCodeExecution]; !ok {
		out[capabilities.FeatureHostedCodeExecution] = enabledControl(cfg.CodeExecution != nil && cfg.CodeExecution.Enabled != nil && *cfg.CodeExecution.Enabled)
	}
	if _, ok := out[capabilities.FeatureNativeCompaction]; !ok && cfg.CompactionEnabled != nil {
		out[capabilities.FeatureNativeCompaction] = enabledControl(*cfg.CompactionEnabled)
	}
	return out
}

func enabledControl(enabled bool) capabilities.ControlSetting {
	if enabled {
		return capabilities.ControlOn
	}
	return capabilities.ControlOff
}

func EndpointFamily(cfg config.ProviderConfig) string {
	surface := config.DeriveProviderAPISurface(cfg)
	base := strings.TrimSpace(stringPtrValue(cfg.BaseURL))
	if parsed, err := url.Parse(base); err == nil && base != "" {
		host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
		switch {
		case host == "api.openai.com":
			return "openai-api"
		case strings.HasSuffix(host, ".openai.azure.com") ||
			strings.HasSuffix(host, ".services.ai.azure.com") ||
			strings.HasSuffix(host, ".cognitiveservices.azure.com"):
			return "azure-openai"
		case host == "api.anthropic.com":
			return "anthropic-api"
		case host == "generativelanguage.googleapis.com":
			return "gemini-developer"
		case strings.Contains(host, "bedrock-mantle."):
			return "bedrock-mantle"
		case strings.Contains(host, "bedrock-runtime."):
			return "bedrock-runtime"
		case host == "api.groq.com":
			return "groq"
		case host == "api.together.xyz":
			return "together"
		case strings.Contains(host, "fireworks.ai"):
			return "fireworks"
		case host == "openrouter.ai":
			return "openrouter"
		case host != "":
			return "custom:" + host
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ProviderID)) {
	case "openai":
		return "openai-api"
	case "anthropic":
		return "anthropic-api"
	case "gemini":
		return "gemini-developer"
	case "bedrock":
		if surface == "responses" {
			return "bedrock-mantle"
		}
		return "bedrock-runtime"
	default:
		return "compatible-custom"
	}
}

func resolvePermissions(
	ctx context.Context,
	target capabilities.TargetDescriptor,
	threadID string,
	service *permissions.Service,
) (map[capabilities.FeatureKey]capabilities.PermissionDecision, error) {
	out := map[capabilities.FeatureKey]capabilities.PermissionDecision{}
	adapterFamily := ""
	switch {
	case target.ProviderFamily == "anthropic" && target.APISurface == "messages":
		adapterFamily = "anthropic-messages"
	case (target.ProviderFamily == "openai" || target.ProviderFamily == "compatible") && target.APISurface == "responses":
		adapterFamily = "openai-responses"
	}
	if adapterFamily == "" {
		return out, nil
	}
	// Enabling the Web Search connection is the explicit consent action. Keep
	// that grant local to the connection state instead of consulting legacy
	// provider-hosted permission rules that older app builds may have cached.
	out[capabilities.FeatureHostedWebSearch] = capabilities.PermissionDecision{Granted: true}
	if service == nil {
		return out, nil
	}
	for _, capability := range chattools.ProviderHostedToolCapabilities() {
		if capability.AdapterFamily != adapterFamily {
			continue
		}
		var key capabilities.FeatureKey
		switch capability.SettingsKey {
		case "webSearch":
			key = capabilities.FeatureHostedWebSearch
		case "codeExecution":
			key = capabilities.FeatureHostedCodeExecution
		default:
			continue
		}
		if key == capabilities.FeatureHostedWebSearch {
			continue
		}
		decision, err := evaluatePermission(ctx, service, threadID, capability)
		if err != nil {
			return nil, err
		}
		out[key] = decision
	}
	return out, nil
}

func evaluatePermission(
	ctx context.Context,
	service *permissions.Service,
	threadID string,
	capability chattools.ProviderHostedToolCapability,
) (capabilities.PermissionDecision, error) {
	operationSubject := store.PermissionSubject{
		Kind:      capability.PermissionSubjectKind,
		Tool:      capability.ID,
		Connector: capability.AdapterFamily,
		ToolSetID: capability.ToolSetID,
		Operation: capability.SettingsKey,
	}
	candidates := []store.PermissionSubject{
		operationSubject,
		{Kind: "tool_set", ToolSetID: capability.ToolSetID, Connector: capability.AdapterFamily},
		{Kind: "connector", Connector: capability.AdapterFamily},
	}
	var operationDefault permissions.EvaluationResult
	for index, subject := range candidates {
		result, err := service.Evaluate(ctx, permissions.EvaluationInput{
			ThreadID: strings.TrimSpace(threadID),
			Tool:     capability.ID,
			Category: string(capability.Category),
			Subject:  subject,
		})
		if err != nil {
			return capabilities.PermissionDecision{}, err
		}
		if index == 0 {
			operationDefault = result
		}
		if result.Rule != nil {
			return capabilities.PermissionDecision{
				Granted:   result.Decision == permissions.DecisionAllow,
				PolicyRef: result.Rule.ID,
			}, nil
		}
	}
	return capabilities.PermissionDecision{Granted: operationDefault.Decision == permissions.DecisionAllow}, nil
}

func mergeProbeEvidence(report *capabilities.CapabilityReport, entries []store.ProviderCapabilityEvidence, now time.Time) {
	if report == nil || len(entries) == 0 {
		return
	}
	hasCurrentEvidence := map[string]bool{}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Fingerprint) != strings.TrimSpace(report.Fingerprint) {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(entry.Verdict)) {
		case "verified", "unsupported":
			hasCurrentEvidence[strings.TrimSpace(entry.Probe)] = true
		}
	}
	seen := map[string]struct{}{}
	for _, entry := range entries {
		probe := strings.TrimSpace(entry.Probe)
		if probe == "" || probe == "transport" {
			continue
		}
		// The ledger is newest-first. Only the newest record for a fingerprint
		// and probe participates in resolution; older records remain auditable.
		identity := entry.Fingerprint + "\x00" + probe
		if _, ok := seen[identity]; ok {
			continue
		}
		seen[identity] = struct{}{}
		key := capabilities.FeatureKey(probe)
		feature, ok := report.Features[key]
		if !ok {
			continue
		}
		verifiedAt := entry.VerifiedAt.UTC().Format(time.RFC3339Nano)
		ref := probe
		stale := strings.TrimSpace(entry.Fingerprint) != strings.TrimSpace(report.Fingerprint)
		var staleReason *string
		if stale {
			reason := "fingerprint"
			staleReason = &reason
		} else if now.Sub(entry.VerifiedAt) > EvidenceFreshnessWindow {
			stale = true
			reason := "age"
			staleReason = &reason
		}
		feature.Evidence = append(feature.Evidence, capabilities.CapabilityEvidence{
			Kind: "probe", VerifiedAt: &verifiedAt, Ref: &ref, Stale: stale, StaleReason: staleReason,
		})
		// Hosted Web Search is provider-native and the connection toggle is
		// authoritative. Keep probe history visible, but never let a stale or
		// failed check cache suppress the tool from a supported target.
		if key == capabilities.FeatureHostedWebSearch {
			report.Features[key] = feature
			continue
		}
		currentFingerprint := strings.TrimSpace(entry.Fingerprint) == strings.TrimSpace(report.Fingerprint)
		if currentFingerprint {
			switch strings.ToLower(strings.TrimSpace(entry.Verdict)) {
			case "verified":
				feature.State = capabilities.StateSupported
				if feature.Mode == nil {
					mode := capabilities.ModeNative
					feature.Mode = &mode
				}
				feature.Reasons = nil
				feature.Effective = effectiveFromGates(feature)
			case "unsupported":
				feature.State = capabilities.StateUnsupported
				feature.Mode = nil
				feature.Effective = capabilities.EffectiveUnavailable
				feature.Conditions = nil
				feature.Reasons = []capabilities.CapabilityExplanation{{
					Code: "probe_unsupported", Text: evidenceMessage(entry, "The capability check reported this feature as unsupported."),
				}}
			}
		} else if !hasCurrentEvidence[probe] && (feature.State == capabilities.StateUnknown || feature.State == capabilities.StateConditional) {
			// A probe-derived conditional/available result cannot survive an
			// endpoint/auth/surface fingerprint change. Keep the stale evidence
			// visible, but fail closed until this exact target is checked again.
			feature.State = capabilities.StateUnknown
			feature.Mode = nil
			feature.Effective = capabilities.EffectiveUnavailable
			feature.Conditions = nil
			feature.Reasons = []capabilities.CapabilityExplanation{{
				Code: "probe_fingerprint_changed",
				Text: "The provider configuration changed since this capability was checked. Re-check this target before relying on it.",
			}}
		}
		report.Features[key] = feature
	}
}

func effectiveFromGates(feature capabilities.FeatureResolution) capabilities.EffectiveState {
	if feature.Control != nil && feature.Control.Setting == capabilities.ControlOff {
		return capabilities.EffectiveOff
	}
	if feature.Permission != nil && feature.Permission.Required && !feature.Permission.Granted {
		return capabilities.EffectiveBlocked
	}
	return capabilities.EffectiveOn
}

func evidenceMessage(entry store.ProviderCapabilityEvidence, fallback string) string {
	if message := strings.TrimSpace(entry.Message); message != "" {
		return message
	}
	return fallback
}

func normalizedConnectionID(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func normalizedStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}

func stringPtrValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
