package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"ai-analyst-local/internal/chat/providers/anthropic"
	"ai-analyst-local/internal/chat/providers/bedrock"
	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/capabilityruntime"
	"ai-analyst-local/internal/chat/providers/gemini"
	"ai-analyst-local/internal/chat/providers/openai_compat"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

const defaultBedrockCompactionInstruction = "Create a compact but complete handover summary. Include the latest user request, confirmed findings, key evidence (queries, jobs, artifacts, and message references), decisions made, and remaining next steps. Preserve critical context needed to continue accurately. Do not draft or continue the user-facing reply; output summary content only."
const defaultCompactionTriggerTokens = 170000
const defaultOpenAICompactionThresholdTokens = 200000
const minCompactionTriggerTokens = 50000

type Factory struct {
	Settings *config.Store
	Secrets  *secrets.Store
	Usage    usage.Repository
}

type HostedToolPolicy struct {
	Disabled map[string]bool
}

// DispatchSnapshot is an immutable, in-memory copy of the exact non-secret
// settings that passed run-plan fingerprint validation. Secrets deliberately
// remain live in the secret store and are resolved only when an attempt is
// dispatched. The snapshot is never serialized with the run.
type DispatchSnapshot struct {
	ConnectionID      string
	LegacyProfileID   string
	ProviderID        string
	Model             string
	ConfigFingerprint string
	PlannedAPISurface string

	settings       config.AppSettings
	providerConfig *config.ProviderConfig
	legacyProfile  *config.CompatibleProfile
}

type dispatchSnapshotContextKey struct{}

func WithDispatchSnapshot(ctx context.Context, snapshot DispatchSnapshot) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, dispatchSnapshotContextKey{}, snapshot)
}

func DispatchSnapshotFromContext(ctx context.Context) (DispatchSnapshot, bool) {
	if ctx == nil {
		return DispatchSnapshot{}, false
	}
	snapshot, ok := ctx.Value(dispatchSnapshotContextKey{}).(DispatchSnapshot)
	return snapshot, ok
}

func (p HostedToolPolicy) Disables(capabilityID string) bool {
	return p.Disabled[strings.TrimSpace(capabilityID)]
}

func NewFactory(settings *config.Store, secretsStore *secrets.Store, usageRepositories ...usage.Repository) *Factory {
	var usageRepository usage.Repository
	if len(usageRepositories) > 0 {
		usageRepository = usageRepositories[0]
	}
	return &Factory{Settings: settings, Secrets: secretsStore, Usage: usageRepository}
}

func (f *Factory) CreateProvider(modelOverride string) (types.Provider, error) {
	return f.CreateProviderWithHostedToolPolicy(modelOverride, HostedToolPolicy{})
}

func (f *Factory) CreateProviderWithHostedToolPolicy(modelOverride string, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	if f.Settings == nil || f.Secrets == nil {
		return nil, errors.New("missing settings or secrets")
	}
	st := f.Settings.Get()
	activeCfg := activeProviderConfig(st)
	provider, err := f.createProviderFromSettings(st, activeCfg, modelOverride, hostedPolicy)
	if err != nil {
		return nil, err
	}
	connectionID, profileID, surface := providerIdentity(st, activeCfg)
	fingerprint := ""
	if activeCfg != nil {
		target := *activeCfg
		if model := strings.TrimSpace(modelOverride); model != "" {
			target.Model = model
		}
		fingerprint = config.ProviderConfigFingerprint(target)
	}
	return f.trackProvider(provider, connectionID, profileID, fingerprint, surface, surface), nil
}

// CreateProviderForContext honors a finalized run snapshot carried through
// nested tool contexts; background work without one keeps the active-provider
// behavior. A nested model override cannot escape the finalized target.
func (f *Factory) CreateProviderForContext(ctx context.Context, modelOverride string, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	if snapshot, ok := DispatchSnapshotFromContext(ctx); ok {
		if requested := strings.TrimSpace(modelOverride); requested != "" && requested != strings.TrimSpace(snapshot.Model) {
			return nil, errors.New("nested model override is not authorized by the finalized run target")
		}
		return f.CreateProviderFromSnapshot(snapshot, hostedPolicy)
	}
	return f.CreateProviderWithHostedToolPolicy(modelOverride, hostedPolicy)
}

// CreateProviderForConnection creates a provider for an explicit saved
// connection without changing the global active connection.
func (f *Factory) CreateProviderForConnection(connectionID, modelOverride string) (types.Provider, error) {
	return f.CreateProviderForConnectionWithHostedToolPolicy(connectionID, modelOverride, HostedToolPolicy{})
}

// CreateProviderForConnectionWithHostedToolPolicy is the per-thread/per-run
// target seam. It resolves a saved ProviderConfig by ID, applies an optional
// model override to a local copy, and composes capability support with the
// runtime hosted-tool permission policy before constructing the adapter.
func (f *Factory) CreateProviderForConnectionWithHostedToolPolicy(connectionID, modelOverride string, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	if f.Settings == nil || f.Secrets == nil {
		return nil, errors.New("missing settings or secrets")
	}
	st := f.Settings.Get()
	snapshot, err := f.SnapshotConnection(st, connectionID, modelOverride, "", "")
	if err != nil {
		return nil, err
	}
	return f.CreateProviderFromSnapshot(snapshot, hostedPolicy)
}

// SnapshotConnection validates and clones the exact configured target used by
// preflight. The caller supplies the settings snapshot read for fingerprint
// validation so later settings mutations cannot redirect queued/tool-loop calls.
func (f *Factory) SnapshotConnection(st config.AppSettings, connectionID, model, expectedFingerprint, plannedSurface string) (DispatchSnapshot, error) {
	connectionID = strings.TrimSpace(connectionID)
	model = strings.TrimSpace(model)
	if connectionID == "" || connectionID == "legacy-default" {
		return f.snapshotLegacy(st, model, expectedFingerprint, plannedSurface)
	}
	cfg := providerConfigByID(st, connectionID)
	if cfg == nil {
		return DispatchSnapshot{}, fmt.Errorf("unknown provider connection id: %s", connectionID)
	}
	clonedSettings := st.Clone()
	clonedConfig := providerConfigByID(clonedSettings, connectionID)
	if clonedConfig == nil {
		return DispatchSnapshot{}, fmt.Errorf("provider connection %q could not be snapshotted", connectionID)
	}
	if model == "" {
		model = strings.TrimSpace(clonedConfig.Model)
	}
	if model == "" {
		return DispatchSnapshot{}, errors.New("provider model is required")
	}
	fingerprintConfig := *clonedConfig
	fingerprintConfig.Model = model
	fingerprint := config.ProviderConfigFingerprint(fingerprintConfig)
	if expected := strings.TrimSpace(expectedFingerprint); expected != "" && fingerprint != expected {
		return DispatchSnapshot{}, fmt.Errorf("provider connection %q changed after preflight", connectionID)
	}
	surface := config.DeriveProviderAPISurface(fingerprintConfig)
	if strings.TrimSpace(plannedSurface) == "" {
		plannedSurface = surface
	}
	return DispatchSnapshot{
		ConnectionID: connectionID, ProviderID: strings.TrimSpace(clonedConfig.ProviderID), Model: model,
		ConfigFingerprint: fingerprint, PlannedAPISurface: strings.TrimSpace(plannedSurface),
		settings: clonedSettings, providerConfig: clonedConfig,
	}, nil

}

func (f *Factory) snapshotLegacy(st config.AppSettings, model, expectedFingerprint, plannedSurface string) (DispatchSnapshot, error) {
	clonedSettings := st.Clone()
	activeCfg := activeProviderConfig(clonedSettings)
	if activeCfg != nil {
		snapshot, err := f.SnapshotConnection(clonedSettings, strings.TrimSpace(activeCfg.ID), model, expectedFingerprint, plannedSurface)
		if err != nil {
			return DispatchSnapshot{}, err
		}
		// Runs created before mandatory preflight have no persisted exact
		// connection identity. Keep that explicit instead of retroactively
		// claiming the active connection was transactionally finalized.
		snapshot.ConnectionID = "legacy-default"
		return snapshot, nil
	}
	providerID := strings.TrimSpace(clonedSettings.Provider.ProviderID)
	if model == "" {
		model = strings.TrimSpace(clonedSettings.Provider.Model)
	}
	if providerID == "" || model == "" {
		return DispatchSnapshot{}, errors.New("legacy provider target is incomplete")
	}
	profileID := ""
	var profile *config.CompatibleProfile
	if strings.EqualFold(providerID, "compatible") {
		resolved, id, err := compatibleProfile(clonedSettings)
		if err != nil {
			return DispatchSnapshot{}, err
		}
		profileID = id
		profile = &resolved
	}
	return DispatchSnapshot{
		ConnectionID: "legacy-default", LegacyProfileID: profileID, ProviderID: providerID, Model: model,
		ConfigFingerprint: strings.TrimSpace(expectedFingerprint), PlannedAPISurface: strings.TrimSpace(plannedSurface),
		settings: clonedSettings, legacyProfile: profile,
	}, nil
}

// SnapshotLegacyProfile preserves the exact legacy compatible profile selected
// by older callers without confusing it with a modern connection identity.
func (f *Factory) SnapshotLegacyProfile(st config.AppSettings, profileID, model, plannedSurface string) (DispatchSnapshot, error) {
	profileID = strings.TrimSpace(profileID)
	model = strings.TrimSpace(model)
	if profileID == "" || model == "" {
		return DispatchSnapshot{}, errors.New("legacy profile id and model are required")
	}
	clonedSettings := st.Clone()
	var selected *config.CompatibleProfile
	for i := range clonedSettings.CompatibleProfiles {
		if strings.TrimSpace(clonedSettings.CompatibleProfiles[i].ID) == profileID {
			profile := clonedSettings.CompatibleProfiles[i]
			selected = &profile
			break
		}
	}
	if selected == nil {
		return DispatchSnapshot{}, fmt.Errorf("unknown compatible profileId: %s", profileID)
	}
	clonedSettings.Provider = config.ProviderSelection{ProviderID: "compatible", Model: model, ProfileID: &profileID}
	if strings.TrimSpace(plannedSurface) == "" {
		plannedSurface = strings.TrimSpace(selected.ApiType)
		if plannedSurface == "" {
			plannedSurface = "chat_completions"
		}
	}
	return DispatchSnapshot{
		ConnectionID: "legacy-default", LegacyProfileID: profileID, ProviderID: "compatible", Model: model,
		PlannedAPISurface: plannedSurface, settings: clonedSettings, legacyProfile: selected,
	}, nil
}

func (f *Factory) CreateProviderFromSnapshot(snapshot DispatchSnapshot, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	if f.Secrets == nil {
		return nil, errors.New("missing secrets")
	}
	st := snapshot.settings.Clone()
	var activeCfg *config.ProviderConfig
	if snapshot.providerConfig != nil {
		cfg := *snapshot.providerConfig
		activeCfg = &cfg
	}
	if snapshot.legacyProfile != nil {
		profile := *snapshot.legacyProfile
		st.CompatibleProfiles = []config.CompatibleProfile{profile}
		st.Provider.ProviderID = "compatible"
		st.Provider.ProfileID = &profile.ID
	}
	provider, err := f.createProviderFromSettings(st, activeCfg, snapshot.Model, hostedPolicy)
	if err != nil {
		return nil, err
	}
	effectiveSurface := snapshot.PlannedAPISurface
	if activeCfg != nil {
		effectiveSurface = config.DeriveProviderAPISurface(*activeCfg)
	}
	connectionID := snapshot.ConnectionID
	if strings.TrimSpace(snapshot.LegacyProfileID) != "" {
		// Legacy profiles predate saved connections and are a mutually exclusive
		// attribution identity in the usage schema. "legacy-default" remains a
		// dispatch sentinel only; it must never be persisted beside profile_id.
		connectionID = ""
	}
	return f.trackProvider(provider, connectionID, snapshot.LegacyProfileID, snapshot.ConfigFingerprint, snapshot.PlannedAPISurface, effectiveSurface), nil
}

func (f *Factory) createProviderFromSettings(st config.AppSettings, activeCfg *config.ProviderConfig, modelOverride string, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	if activeCfg != nil {
		cfg := providerConfigWithCapabilityControls(*activeCfg)
		activeCfg = &cfg
	}
	providerID := strings.TrimSpace(st.Provider.ProviderID)
	if activeCfg != nil && strings.TrimSpace(activeCfg.ProviderID) != "" {
		providerID = strings.TrimSpace(activeCfg.ProviderID)
	}
	model := strings.TrimSpace(modelOverride)
	if model == "" {
		if activeCfg != nil && strings.TrimSpace(activeCfg.Model) != "" {
			model = strings.TrimSpace(activeCfg.Model)
		} else {
			model = strings.TrimSpace(st.Provider.Model)
		}
	}
	if model == "" {
		return nil, errors.New("missing model")
	}
	if activeCfg != nil {
		hostedPolicy = capabilityAwareHostedToolPolicy(*activeCfg, model, hostedPolicy)
	}

	switch providerID {
	case "openai":
		base := "https://api.openai.com/v1"
		if activeCfg != nil && strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL)) != "" {
			base = strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL))
		} else if st.OpenAI != nil && st.OpenAI.BaseURL != nil && strings.TrimSpace(*st.OpenAI.BaseURL) != "" {
			base = strings.TrimSpace(*st.OpenAI.BaseURL)
		}
		compactionEnabled := true
		compactionThreshold := defaultOpenAICompactionThresholdTokens
		if activeCfg != nil {
			if activeCfg.CompactionEnabled != nil {
				compactionEnabled = *activeCfg.CompactionEnabled
			}
			if activeCfg.CompactionTrigger != nil && *activeCfg.CompactionTrigger >= minCompactionTriggerTokens {
				compactionThreshold = *activeCfg.CompactionTrigger
			}
		} else if st.OpenAI != nil {
			if st.OpenAI.CompactionEnabled != nil {
				compactionEnabled = *st.OpenAI.CompactionEnabled
			}
			if st.OpenAI.CompactionTrigger != nil && *st.OpenAI.CompactionTrigger >= minCompactionTriggerTokens {
				compactionThreshold = *st.OpenAI.CompactionTrigger
			}
		}
		key, err := f.Secrets.GetProviderKey("openai")
		if err != nil {
			return nil, err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("openai api key is not configured")
		}
		prof := openai_compat.Profile{
			ProviderID: "openai",
			APIType:    openai_compat.APIResponses,
			BaseURL:    base,
			ExtraHeaders: map[string]string{
				"Authorization": "Bearer " + key,
			},
			ModelInBody:               true,
			ModelInPath:               false,
			FileInputMode:             openai_compat.FileInputNativeSupported,
			UsageSemantics:            types.UsageSemanticsOpenAI,
			CompactionEnabled:         compactionEnabled,
			CompactionThresholdTokens: compactionThreshold,
		}
		if activeCfg != nil {
			prof.WebSearch = openAIWebSearchConfigWithPolicy(activeCfg.WebSearch, hostedPolicy, "openai-responses")
			prof.CodeExecution = openAICodeExecutionConfigWithPolicy(activeCfg.CodeExecution, hostedPolicy, "openai-responses")
		} else if st.OpenAI != nil {
			prof.WebSearch = openAIWebSearchConfigWithPolicy(st.OpenAI.WebSearch, hostedPolicy, "openai-responses")
			prof.CodeExecution = openAICodeExecutionConfigWithPolicy(st.OpenAI.CodeExecution, hostedPolicy, "openai-responses")
		}
		return openai_compat.New("openai", model, prof), nil

	case "compatible":
		if activeCfg != nil {
			return f.compatibleProviderFromConfig(*activeCfg, model, hostedPolicy)
		}
		prof, pid, err := compatibleProfile(st)
		if err != nil {
			return nil, err
		}
		needsSecretHeaders := compatibleProfileHasSecretHeaders(prof.Extra)
		needsAPIKey := strings.TrimSpace(prof.Auth.Type) == "api_key"
		sec := secrets.CompatibleSecrets{}
		if needsAPIKey || needsSecretHeaders {
			var err error
			sec, err = f.Secrets.GetCompatible(pid)
			if err != nil {
				return nil, err
			}
		}

		apiType := openai_compat.APIChatCompletions
		if strings.TrimSpace(prof.ApiType) == "responses" {
			apiType = openai_compat.APIResponses
		}

		extra := map[string]string{}
		// Non-secret extra headers
		for _, kv := range prof.Extra {
			k := strings.TrimSpace(kv.Key)
			if k == "" {
				continue
			}
			if kv.IsSecret {
				continue
			}
			extra[k] = kv.Value
		}
		// Secret extra headers
		for _, kv := range prof.Extra {
			if !kv.IsSecret {
				continue
			}
			k := strings.TrimSpace(kv.Key)
			if k == "" {
				continue
			}
			val := strings.TrimSpace(sec.SecretHeaders[k])
			if val == "" {
				continue
			}
			extra[k] = val
		}

		// Auth header
		if strings.TrimSpace(prof.Auth.Type) == "api_key" {
			key := strings.TrimSpace(sec.APIKey)
			if key == "" {
				return nil, errors.New("compatible profile api key is not configured")
			}
			hdr := strings.TrimSpace(prof.Auth.Header)
			if hdr == "" {
				hdr = "Authorization"
			}
			val := key
			if prof.Auth.Scheme != nil && strings.TrimSpace(*prof.Auth.Scheme) != "" {
				val = strings.TrimSpace(*prof.Auth.Scheme) + " " + key
			}
			extra[hdr] = val
		}

		var queryParams map[string]string
		if qp, ok := prof.Query.(map[string]any); ok {
			queryParams = map[string]string{}
			for k, v := range qp {
				ks := strings.TrimSpace(k)
				if ks == "" {
					continue
				}
				if vs, ok := v.(string); ok {
					queryParams[ks] = vs
				}
			}
		}

		var payloadOverrides map[string]any
		if po, ok := prof.Overrides.(map[string]any); ok {
			payloadOverrides = po
		}

		timeoutMs := anyToIntPtr(prof.TimeoutMs)

		modelInPath := anyToBool(prof.ModelInPath, false)
		modelInBody := anyToBool(prof.ModelInBody, true)

		profileID := pid
		profIDPtr := &profileID

		openProf := openai_compat.Profile{
			ProviderID:                "compatible",
			ProfileID:                 profIDPtr,
			APIType:                   apiType,
			BaseURL:                   prof.BaseURL,
			QueryParams:               queryParams,
			ExtraHeaders:              extra,
			PayloadOverrides:          payloadOverrides,
			TimeoutMs:                 timeoutMs,
			ReasoningEffort:           prof.ReasoningEffort,
			ModelInPath:               modelInPath,
			ModelInBody:               modelInBody,
			EndpointSuffix:            prof.EndpointSufx,
			FileInputMode:             compatibleFileInputMode(prof.FileInputMode, apiType),
			IncludeStreamUsage:        prof.IncludeStreamUsage,
			UsageSemantics:            compatibleUsageSemantics(prof.UsageSemantics),
			WebSearch:                 openAIWebSearchConfigWithPolicy(prof.WebSearch, hostedPolicy, "openai-responses"),
			CodeExecution:             openAICodeExecutionConfigWithPolicy(prof.CodeExecution, hostedPolicy, "openai-responses"),
			CompactionEnabled:         false,
			CompactionThresholdTokens: defaultOpenAICompactionThresholdTokens,
		}
		return openai_compat.New("compatible", model, openProf), nil

	case "anthropic":
		base := "https://api.anthropic.com"
		ver := "2023-06-01"
		compactionEnabled := true
		compactionInstruction := defaultBedrockCompactionInstruction
		compactionTriggerTokens := defaultCompactionTriggerTokens
		if activeCfg != nil {
			if strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL)) != "" {
				base = strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL))
			}
			if strings.TrimSpace(trimmedStringPtr(activeCfg.AnthropicVersion)) != "" {
				ver = strings.TrimSpace(trimmedStringPtr(activeCfg.AnthropicVersion))
			}
			if activeCfg.CompactionEnabled != nil {
				compactionEnabled = *activeCfg.CompactionEnabled
			}
			if strings.TrimSpace(trimmedStringPtr(activeCfg.CompactionInstruction)) != "" {
				compactionInstruction = strings.TrimSpace(trimmedStringPtr(activeCfg.CompactionInstruction))
			}
			if activeCfg.CompactionTrigger != nil && *activeCfg.CompactionTrigger >= minCompactionTriggerTokens {
				compactionTriggerTokens = *activeCfg.CompactionTrigger
			}
		} else if st.Anthropic != nil {
			if st.Anthropic.BaseURL != nil && strings.TrimSpace(*st.Anthropic.BaseURL) != "" {
				base = strings.TrimSpace(*st.Anthropic.BaseURL)
			}
			if st.Anthropic.AnthropicVersion != nil && strings.TrimSpace(*st.Anthropic.AnthropicVersion) != "" {
				ver = strings.TrimSpace(*st.Anthropic.AnthropicVersion)
			}
			if st.Anthropic.CompactionEnabled != nil {
				compactionEnabled = *st.Anthropic.CompactionEnabled
			}
			if st.Anthropic.CompactionInstruction != nil && strings.TrimSpace(*st.Anthropic.CompactionInstruction) != "" {
				compactionInstruction = strings.TrimSpace(*st.Anthropic.CompactionInstruction)
			}
			if st.Anthropic.CompactionTrigger != nil && *st.Anthropic.CompactionTrigger >= minCompactionTriggerTokens {
				compactionTriggerTokens = *st.Anthropic.CompactionTrigger
			}
		}
		key, err := f.Secrets.GetProviderKey("anthropic")
		if err != nil {
			return nil, err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("anthropic api key is not configured")
		}
		hdrs := map[string]string{"x-api-key": key}
		opts := anthropic.Options{
			ProviderID:            "anthropic",
			Model:                 model,
			BaseURL:               base,
			Version:               ver,
			Headers:               hdrs,
			CompactionEnabled:     compactionEnabled,
			CompactionInstruction: compactionInstruction,
			CompactionTrigger:     compactionTriggerTokens,
		}
		if activeCfg != nil {
			opts.WebSearch = anthropicWebSearchConfigWithPolicy(activeCfg.WebSearch, hostedPolicy, "anthropic-messages")
			opts.CodeExecution = anthropicCodeExecutionConfigWithPolicy(activeCfg.CodeExecution, hostedPolicy, "anthropic-messages")
		} else if st.Anthropic != nil {
			opts.WebSearch = anthropicWebSearchConfigWithPolicy(st.Anthropic.WebSearch, hostedPolicy, "anthropic-messages")
			opts.CodeExecution = anthropicCodeExecutionConfigWithPolicy(st.Anthropic.CodeExecution, hostedPolicy, "anthropic-messages")
		}
		return anthropic.NewWithOptions(opts), nil

	case "gemini":
		base := "https://generativelanguage.googleapis.com"
		if activeCfg != nil && strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL)) != "" {
			base = strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL))
		} else if st.Gemini != nil && st.Gemini.BaseURL != nil && strings.TrimSpace(*st.Gemini.BaseURL) != "" {
			base = strings.TrimSpace(*st.Gemini.BaseURL)
		}
		key, err := f.Secrets.GetProviderKey("gemini")
		if err != nil {
			return nil, err
		}
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("gemini api key is not configured")
		}
		hdrs := map[string]string{"x-goog-api-key": key}
		return gemini.New("gemini", model, base, hdrs), nil
	case "bedrock":
		region := "us-east-1"
		base := ""
		anthVer := "bedrock-2023-05-31"
		compactionEnabled := true
		compactionInstruction := defaultBedrockCompactionInstruction
		compactionTriggerTokens := defaultCompactionTriggerTokens
		if activeCfg != nil {
			if strings.TrimSpace(trimmedStringPtr(activeCfg.Region)) != "" {
				region = strings.TrimSpace(trimmedStringPtr(activeCfg.Region))
			}
			if strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL)) != "" {
				base = strings.TrimSpace(trimmedStringPtr(activeCfg.BaseURL))
			}
			if strings.TrimSpace(trimmedStringPtr(activeCfg.AnthropicVersion)) != "" {
				anthVer = strings.TrimSpace(trimmedStringPtr(activeCfg.AnthropicVersion))
			}
			if activeCfg.CompactionEnabled != nil {
				compactionEnabled = *activeCfg.CompactionEnabled
			}
			if strings.TrimSpace(trimmedStringPtr(activeCfg.CompactionInstruction)) != "" {
				compactionInstruction = strings.TrimSpace(trimmedStringPtr(activeCfg.CompactionInstruction))
			}
			if activeCfg.CompactionTrigger != nil && *activeCfg.CompactionTrigger >= minCompactionTriggerTokens {
				compactionTriggerTokens = *activeCfg.CompactionTrigger
			}
		} else if st.Bedrock != nil {
			if strings.TrimSpace(st.Bedrock.Region) != "" {
				region = strings.TrimSpace(st.Bedrock.Region)
			}
			if st.Bedrock.BaseURL != nil && strings.TrimSpace(*st.Bedrock.BaseURL) != "" {
				base = strings.TrimSpace(*st.Bedrock.BaseURL)
			}
			if st.Bedrock.AnthropicVersion != nil && strings.TrimSpace(*st.Bedrock.AnthropicVersion) != "" {
				anthVer = strings.TrimSpace(*st.Bedrock.AnthropicVersion)
			}
			if st.Bedrock.CompactionEnabled != nil {
				compactionEnabled = *st.Bedrock.CompactionEnabled
			}
			if st.Bedrock.CompactionInstruction != nil && strings.TrimSpace(*st.Bedrock.CompactionInstruction) != "" {
				compactionInstruction = strings.TrimSpace(*st.Bedrock.CompactionInstruction)
			}
			if st.Bedrock.CompactionTrigger != nil && *st.Bedrock.CompactionTrigger >= minCompactionTriggerTokens {
				compactionTriggerTokens = *st.Bedrock.CompactionTrigger
			}
		}

		sec, err := f.Secrets.GetBedrock()
		if err != nil {
			return nil, err
		}
		bedrockSurface := "invoke_stream"
		if activeCfg != nil {
			bedrockSurface = strings.ToLower(strings.TrimSpace(config.DeriveProviderAPISurface(*activeCfg)))
			switch bedrockSurface {
			case "responses", "chat_completions":
				compatibleCfg := *activeCfg
				compatibleCfg.ApiType = stringPointer(bedrockSurface)
				return f.bedrockCompatibleProviderFromConfig(compatibleCfg, model, region, sec, hostedPolicy)
			case "invoke_stream", "converse_stream":
			default:
				return nil, fmt.Errorf("unsupported Bedrock API surface %q", bedrockSurface)
			}
		}
		apiKey := strings.TrimSpace(sec.APIKey)
		accessKeyID := strings.TrimSpace(sec.AccessKeyID)
		secretAccessKey := strings.TrimSpace(sec.SecretAccessKey)
		bedrockAuthType := ""
		if activeCfg != nil {
			bedrockAuthType = strings.TrimSpace(trimmedStringPtr(activeCfg.AuthType))
		}
		if bedrockAuthType == "" {
			if apiKey != "" {
				bedrockAuthType = "api_key"
			} else if accessKeyID != "" && secretAccessKey != "" {
				bedrockAuthType = "cloud_credentials"
			} else {
				bedrockAuthType = "api_key"
			}
		}
		switch bedrockAuthType {
		case "none":
		case "cloud_credentials":
			if accessKeyID == "" || secretAccessKey == "" {
				return nil, errors.New("bedrock AWS credentials are not configured")
			}
		case "api_key":
			if apiKey == "" {
				return nil, errors.New("bedrock api key is not configured")
			}
		default:
			if apiKey != "" {
				bedrockAuthType = "api_key"
			} else if accessKeyID != "" && secretAccessKey != "" {
				bedrockAuthType = "cloud_credentials"
			} else {
				return nil, errors.New("bedrock authentication is not configured (set an API key or AWS credentials)")
			}
		}

		extra := map[string]string{}
		switch {
		case activeCfg != nil:
			extra = headerMap(activeCfg.ExtraHeaders, sec.SecretHeaders)
		case st.Bedrock != nil && len(st.Bedrock.Extra) > 0:
			// Non-secret extra headers
			for _, kv := range st.Bedrock.Extra {
				k := strings.TrimSpace(kv.Key)
				if k == "" {
					continue
				}
				if kv.IsSecret {
					continue
				}
				extra[k] = kv.Value
			}
			// Secret extra headers
			for _, kv := range st.Bedrock.Extra {
				if !kv.IsSecret {
					continue
				}
				k := strings.TrimSpace(kv.Key)
				if k == "" {
					continue
				}
				val := strings.TrimSpace(sec.SecretHeaders[k])
				if val == "" {
					continue
				}
				extra[k] = val
			}
		}

		creds := bedrock.Credentials{}
		switch bedrockAuthType {
		case "api_key":
			creds.APIKey = apiKey
		case "cloud_credentials":
			creds = bedrock.Credentials{
				AccessKeyID:     accessKeyID,
				SecretAccessKey: secretAccessKey,
				SessionToken:    strings.TrimSpace(sec.SessionToken),
			}
		default:
			return nil, errors.New("bedrock messages endpoint requires api key or AWS credentials")
		}
		// Bedrock "messages" uses the runtime InvokeModelWithResponseStream endpoint with
		// an Anthropic Messages body. This matches the last known-working path before
		// the query-card redesign and avoids the Mantle /v1/messages operation errors.
		base = bedrockRuntimeBaseURL(base)
		return bedrock.NewWithAPISurface("bedrock", model, region, base, anthVer, extra, creds, compactionEnabled, compactionInstruction, compactionTriggerTokens, bedrockSurface)
	default:
		return nil, fmt.Errorf("unknown providerId: %s", providerID)
	}
}

func providerConfigWithCapabilityControls(cfg config.ProviderConfig) config.ProviderConfig {
	applyEnabled := func(key string, current bool) bool {
		switch strings.ToLower(strings.TrimSpace(cfg.CapabilityControls[key])) {
		case "on":
			return true
		case "off", "auto":
			return false
		default:
			return current
		}
	}
	webEnabled := cfg.WebSearch != nil && cfg.WebSearch.Enabled != nil && *cfg.WebSearch.Enabled
	webEnabled = applyEnabled(string(capabilities.FeatureHostedWebSearch), webEnabled)
	if cfg.WebSearch == nil {
		cfg.WebSearch = &config.ProviderWebSearchConfig{}
	}
	cfg.WebSearch.Enabled = providerBoolPtr(webEnabled)
	codeEnabled := cfg.CodeExecution != nil && cfg.CodeExecution.Enabled != nil && *cfg.CodeExecution.Enabled
	codeEnabled = applyEnabled(string(capabilities.FeatureHostedCodeExecution), codeEnabled)
	if cfg.CodeExecution == nil {
		cfg.CodeExecution = &config.ProviderCodeExecutionConfig{}
	}
	cfg.CodeExecution.Enabled = providerBoolPtr(codeEnabled)
	return cfg
}

func capabilityAwareHostedToolPolicy(cfg config.ProviderConfig, model string, base HostedToolPolicy) HostedToolPolicy {
	disabled := make(map[string]bool, len(base.Disabled)+4)
	for id, value := range base.Disabled {
		disabled[id] = value
	}
	policy := HostedToolPolicy{Disabled: disabled}
	providerFamily := strings.ToLower(strings.TrimSpace(cfg.ProviderID))
	surface := config.DeriveProviderAPISurface(cfg)
	region := normalizedFactoryStringPtr(cfg.Region)
	controls := map[capabilities.FeatureKey]capabilities.ControlSetting{
		capabilities.FeatureHostedWebSearch:     controlSettingFromProviderConfig(cfg, capabilities.FeatureHostedWebSearch),
		capabilities.FeatureHostedCodeExecution: controlSettingFromProviderConfig(cfg, capabilities.FeatureHostedCodeExecution),
	}
	permissions := map[capabilities.FeatureKey]capabilities.PermissionDecision{
		capabilities.FeatureHostedWebSearch: {
			Granted: !base.Disables(hostedCapabilityID(providerFamily, surface, true)),
		},
		capabilities.FeatureHostedCodeExecution: {
			Granted: !base.Disables(hostedCapabilityID(providerFamily, surface, false)),
		},
	}
	report, err := capabilities.NewResolver().Resolve(capabilities.ResolveRequest{
		Target: capabilities.TargetDescriptor{
			ProviderFamily: providerFamily,
			APISurface:     surface,
			EndpointFamily: capabilityruntime.EndpointFamily(cfg),
			Model:          strings.TrimSpace(model),
			Region:         region,
			AdapterVersion: capabilities.DefaultAdapterVersion,
		},
		Fingerprint:     config.ProviderConfigFingerprint(cfg),
		Controls:        controls,
		Permissions:     permissions,
		OverridePresent: len(cfg.PayloadOverrides) > 0 || len(cfg.QueryParams) > 0,
	})
	if err != nil {
		// A target the capability resolver cannot classify is not allowed to
		// attach an external or executable hosted capability.
		disableHostedCapabilityIDs(policy.Disabled, providerFamily, surface, true, true)
		return policy
	}
	web := report.Features[capabilities.FeatureHostedWebSearch]
	code := report.Features[capabilities.FeatureHostedCodeExecution]
	disableHostedCapabilityIDs(policy.Disabled, providerFamily, surface, web.Effective != capabilities.EffectiveOn, code.Effective != capabilities.EffectiveOn)
	return policy
}

func controlSettingFromProviderConfig(cfg config.ProviderConfig, key capabilities.FeatureKey) capabilities.ControlSetting {
	if value := strings.ToLower(strings.TrimSpace(cfg.CapabilityControls[string(key)])); value != "" {
		return capabilities.ControlSetting(value)
	}
	switch key {
	case capabilities.FeatureHostedWebSearch:
		if cfg.WebSearch != nil && cfg.WebSearch.Enabled != nil && *cfg.WebSearch.Enabled {
			return capabilities.ControlOn
		}
	case capabilities.FeatureHostedCodeExecution:
		if cfg.CodeExecution != nil && cfg.CodeExecution.Enabled != nil && *cfg.CodeExecution.Enabled {
			return capabilities.ControlOn
		}
	}
	return capabilities.ControlOff
}

func hostedCapabilityID(providerFamily, surface string, web bool) string {
	adapterFamily := "openai-responses"
	tool := "code_interpreter"
	if providerFamily == "anthropic" && surface == "messages" {
		adapterFamily = "anthropic-messages"
		tool = "code_execution"
	}
	if web {
		tool = "web_search"
	}
	return adapterFamily + "/" + tool
}

func disableHostedCapabilityIDs(disabled map[string]bool, providerFamily, surface string, web, code bool) {
	if web {
		disabled[hostedCapabilityID(providerFamily, surface, true)] = true
	}
	if code {
		disabled[hostedCapabilityID(providerFamily, surface, false)] = true
	}
}

func normalizedFactoryStringPtr(value *string) *string {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	return &normalized
}

func providerBoolPtr(value bool) *bool { return &value }

func bedrockRuntimeBaseURL(base string) string {
	b := strings.TrimRight(strings.TrimSpace(base), "/")
	if b == "" {
		return ""
	}
	lower := strings.ToLower(b)
	if strings.Contains(lower, "bedrock-mantle.") {
		return ""
	}
	for _, suffix := range []string{"/anthropic/v1/messages", "/anthropic/v1", "/anthropic", "/v1/messages", "/v1"} {
		if strings.HasSuffix(lower, suffix) {
			b = strings.TrimRight(b[:len(b)-len(suffix)], "/")
			break
		}
	}
	if strings.TrimSpace(b) == "" {
		return ""
	}
	return b
}

func (f *Factory) compatibleProviderFromConfig(cfg config.ProviderConfig, model string, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	profileID := strings.TrimSpace(cfg.ID)
	if profileID == "" {
		return nil, errors.New("compatible profile id is required")
	}

	authType := strings.TrimSpace(trimmedStringPtr(cfg.AuthType))
	if authType == "" {
		authType = "api_key"
	}
	needsSecretHeaders := providerConfigHasSecretHeaders(cfg.ExtraHeaders)
	sec := secrets.CompatibleSecrets{}
	if authType == "api_key" || needsSecretHeaders {
		var err error
		sec, err = f.Secrets.GetCompatible(profileID)
		if err != nil {
			return nil, err
		}
	}

	extra := headerMap(cfg.ExtraHeaders, sec.SecretHeaders)
	if authType == "api_key" {
		key := strings.TrimSpace(sec.APIKey)
		if key == "" {
			return nil, errors.New("compatible profile api key is not configured")
		}
		addAPIKeyHeader(extra, trimmedStringPtr(cfg.AuthHeader), trimmedStringPtr(cfg.AuthScheme), key, "Authorization")
	}

	apiType := compatibleAPIType(trimmedStringPtr(cfg.ApiType))
	profIDPtr := &profileID
	openProf := openai_compat.Profile{
		ProviderID:                "compatible",
		ProfileID:                 profIDPtr,
		APIType:                   apiType,
		BaseURL:                   defaultString(trimmedStringPtr(cfg.BaseURL), "https://api.openai.com/v1"),
		QueryParams:               cfg.QueryParams,
		ExtraHeaders:              extra,
		PayloadOverrides:          cfg.PayloadOverrides,
		TimeoutMs:                 cfg.TimeoutMs,
		ReasoningEffort:           cfg.ReasoningEffort,
		ModelInPath:               boolPtrValue(cfg.ModelInPath, false),
		ModelInBody:               boolPtrValue(cfg.ModelInBody, true),
		EndpointSuffix:            cfg.EndpointSuffix,
		FileInputMode:             compatibleFileInputMode(trimmedStringPtr(cfg.FileInputMode), apiType),
		IncludeStreamUsage:        cfg.IncludeStreamUsage,
		UsageSemantics:            compatibleUsageSemantics(cfg.UsageSemantics),
		WebSearch:                 openAIWebSearchConfigWithPolicy(cfg.WebSearch, hostedPolicy, "openai-responses"),
		CodeExecution:             openAICodeExecutionConfigWithPolicy(cfg.CodeExecution, hostedPolicy, "openai-responses"),
		CompactionEnabled:         boolPtrValue(cfg.CompactionEnabled, false) && apiType == openai_compat.APIResponses,
		CompactionThresholdTokens: normalizeOpenAICompactionThreshold(cfg.CompactionTrigger),
	}
	return openai_compat.New("compatible", model, openProf), nil
}

func compatibleProfileHasSecretHeaders(headers []config.HeaderKV) bool {
	return providerConfigHasSecretHeaders(headers)
}

func providerConfigHasSecretHeaders(headers []config.HeaderKV) bool {
	for _, kv := range headers {
		if kv.IsSecret && strings.TrimSpace(kv.Key) != "" {
			return true
		}
	}
	return false
}

func normalizeOpenAICompactionThreshold(trigger *int) int {
	if trigger == nil || *trigger < minCompactionTriggerTokens {
		return defaultOpenAICompactionThresholdTokens
	}
	return *trigger
}

func (f *Factory) bedrockCompatibleProviderFromConfig(cfg config.ProviderConfig, model string, region string, sec secrets.BedrockSecrets, hostedPolicy HostedToolPolicy) (types.Provider, error) {
	authType := strings.TrimSpace(trimmedStringPtr(cfg.AuthType))
	if authType == "" {
		authType = "api_key"
	}
	if authType == "cloud_credentials" {
		return nil, errors.New("bedrock responses/chat_completions endpoints require API key auth")
	}

	apiKey := strings.TrimSpace(sec.APIKey)
	if apiKey == "" {
		return nil, errors.New("bedrock api key is not configured")
	}

	apiType := compatibleAPIType(trimmedStringPtr(cfg.ApiType))
	base := bedrockCompatibleBaseURL(apiType, region, trimmedStringPtr(cfg.BaseURL))

	extra := headerMap(cfg.ExtraHeaders, sec.SecretHeaders)
	addAPIKeyHeader(extra, trimmedStringPtr(cfg.AuthHeader), trimmedStringPtr(cfg.AuthScheme), apiKey, "Authorization")

	openProf := openai_compat.Profile{
		ProviderID:         "bedrock",
		APIType:            apiType,
		BaseURL:            base,
		QueryParams:        cfg.QueryParams,
		ExtraHeaders:       extra,
		PayloadOverrides:   cfg.PayloadOverrides,
		TimeoutMs:          cfg.TimeoutMs,
		ReasoningEffort:    cfg.ReasoningEffort,
		ModelInPath:        boolPtrValue(cfg.ModelInPath, false),
		ModelInBody:        boolPtrValue(cfg.ModelInBody, true),
		EndpointSuffix:     cfg.EndpointSuffix,
		FileInputMode:      compatibleFileInputMode(trimmedStringPtr(cfg.FileInputMode), apiType),
		IncludeStreamUsage: cfg.IncludeStreamUsage,
		UsageSemantics:     types.UsageSemanticsOpenAI,
		WebSearch:          openAIWebSearchConfigWithPolicy(cfg.WebSearch, hostedPolicy, "openai-responses"),
		CodeExecution:      openAICodeExecutionConfigWithPolicy(cfg.CodeExecution, hostedPolicy, "openai-responses"),
	}
	return openai_compat.New("bedrock", model, openProf), nil
}

func bedrockCompatibleBaseURL(apiType openai_compat.APIType, region, raw string) string {
	region = strings.TrimSpace(region)
	base := strings.TrimRight(strings.TrimSpace(raw), "/")
	if base == "" {
		if apiType == openai_compat.APIResponses {
			return fmt.Sprintf("https://bedrock-mantle.%s.api.aws/v1", region)
		}
		return fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com/v1", region)
	}

	// Only add /v1 to an exact, pathless AWS service endpoint. Explicit proxy
	// URLs and user-supplied paths remain opaque to the factory.
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil || parsed.Port() != "" {
		return base
	}
	expectedHost := fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	if apiType == openai_compat.APIResponses {
		expectedHost = fmt.Sprintf("bedrock-mantle.%s.api.aws", region)
	}
	if !strings.EqualFold(parsed.Hostname(), expectedHost) {
		return base
	}
	parsed.Path = "/v1"
	return parsed.String()
}

func compatibleProfile(st config.AppSettings) (config.CompatibleProfile, string, error) {
	pid := ""
	if st.Provider.ProfileID != nil {
		pid = strings.TrimSpace(*st.Provider.ProfileID)
	}
	if pid == "" {
		if len(st.CompatibleProfiles) == 0 {
			return config.CompatibleProfile{}, "", errors.New("no compatibleProfiles configured")
		}
		pid = strings.TrimSpace(st.CompatibleProfiles[0].ID)
	}
	for _, p := range st.CompatibleProfiles {
		if strings.TrimSpace(p.ID) == pid {
			return p, pid, nil
		}
	}
	return config.CompatibleProfile{}, "", errors.New("unknown compatible profileId")
}

func anyToIntPtr(v any) *int {
	switch t := v.(type) {
	case nil:
		return nil
	case float64:
		i := int(t)
		return &i
	case int:
		i := t
		return &i
	case json.Number:
		if n, err := t.Int64(); err == nil {
			i := int(n)
			return &i
		}
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		if n, err := json.Number(strings.TrimSpace(t)).Int64(); err == nil {
			i := int(n)
			return &i
		}
	}
	return nil
}

func anyToBool(v any, def bool) bool {
	switch t := v.(type) {
	case nil:
		return def
	case bool:
		return t
	case float64:
		return t != 0
	case int:
		return t != 0
	case string:
		lv := strings.ToLower(strings.TrimSpace(t))
		if lv == "" {
			return def
		}
		if lv == "true" || lv == "1" || lv == "yes" || lv == "y" {
			return true
		}
		if lv == "false" || lv == "0" || lv == "no" || lv == "n" {
			return false
		}
		return def
	default:
		return def
	}
}

func activeProviderConfig(st config.AppSettings) *config.ProviderConfig {
	if len(st.ProviderConfigs) == 0 {
		return nil
	}
	if st.ActiveProviderConfigID != nil {
		desired := strings.TrimSpace(*st.ActiveProviderConfigID)
		if desired != "" {
			for i := range st.ProviderConfigs {
				if strings.TrimSpace(st.ProviderConfigs[i].ID) == desired {
					return &st.ProviderConfigs[i]
				}
			}
		}
	}
	return &st.ProviderConfigs[0]
}

func providerConfigByID(st config.AppSettings, id string) *config.ProviderConfig {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	for i := range st.ProviderConfigs {
		if strings.TrimSpace(st.ProviderConfigs[i].ID) == id {
			return &st.ProviderConfigs[i]
		}
	}
	return nil
}

func providerIdentity(st config.AppSettings, activeCfg *config.ProviderConfig) (connectionID, profileID, surface string) {
	providerID := strings.ToLower(strings.TrimSpace(st.Provider.ProviderID))
	if activeCfg != nil {
		connectionID = strings.TrimSpace(activeCfg.ID)
		providerID = strings.ToLower(strings.TrimSpace(activeCfg.ProviderID))
		surface = config.DeriveProviderAPISurface(*activeCfg)
	}
	if connectionID == "" {
		connectionID = "legacy-default"
	}
	if activeCfg == nil && providerID == "compatible" && st.Provider.ProfileID != nil {
		profileID = strings.TrimSpace(*st.Provider.ProfileID)
		if profileID != "" {
			connectionID = ""
		}
	}
	if surface == "" {
		switch providerID {
		case "openai":
			surface = "responses"
		case "anthropic":
			surface = "messages"
		case "gemini":
			surface = "generate_content"
		case "bedrock":
			surface = "invoke_stream"
		case "compatible":
			surface = "chat_completions"
		}
	}
	return connectionID, profileID, surface
}

func (f *Factory) trackProvider(provider types.Provider, connectionID, profileID, fingerprint, plannedSurface, effectiveSurface string) types.Provider {
	if provider == nil || f.Usage == nil {
		return provider
	}
	return newTrackingProvider(provider, f.Usage, connectionID, profileID, fingerprint, plannedSurface, effectiveSurface)
}

func providerAPIType(st config.AppSettings, providerID string, activeCfg *config.ProviderConfig, legacyProfileID string, legacyProfile *config.CompatibleProfile) string {
	configured := ""
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	if providerID == "compatible" && activeCfg != nil {
		configured = strings.ToLower(strings.TrimSpace(trimmedStringPtr(activeCfg.ApiType)))
	} else if providerID == "bedrock" && activeCfg != nil {
		configured = strings.ToLower(strings.TrimSpace(trimmedStringPtr(activeCfg.ApiType)))
	} else if providerID == "compatible" && legacyProfile != nil {
		configured = strings.ToLower(strings.TrimSpace(legacyProfile.ApiType))
	} else if providerID == "compatible" {
		for _, profile := range st.CompatibleProfiles {
			if strings.TrimSpace(profile.ID) == strings.TrimSpace(legacyProfileID) {
				configured = strings.ToLower(strings.TrimSpace(profile.ApiType))
				break
			}
		}
	}
	if configured != "" {
		if providerID == "bedrock" && configured == "messages" {
			return "invoke_stream"
		}
		return configured
	}
	switch providerID {
	case "openai":
		return "responses"
	case "anthropic":
		return "messages"
	case "gemini":
		return "generate_content"
	case "bedrock":
		return "invoke_stream"
	case "compatible":
		return "chat_completions"
	default:
		return ""
	}
}

func trimmedStringPtr(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

func boolPtrValue(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

func defaultString(v string, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func compatibleAPIType(raw string) openai_compat.APIType {
	if strings.TrimSpace(raw) == "responses" {
		return openai_compat.APIResponses
	}
	return openai_compat.APIChatCompletions
}

func compatibleUsageSemantics(raw *string) types.UsageSemantics {
	if raw != nil && strings.EqualFold(strings.TrimSpace(*raw), string(types.UsageSemanticsOpenAI)) {
		return types.UsageSemanticsOpenAI
	}
	return types.UsageSemanticsGeneric
}

func compatibleFileInputMode(raw string, apiType openai_compat.APIType) openai_compat.FileInputMode {
	switch strings.TrimSpace(raw) {
	case string(openai_compat.FileInputSidecarOnly):
		return openai_compat.FileInputSidecarOnly
	case string(openai_compat.FileInputNativePDFOnly):
		return openai_compat.FileInputNativePDFOnly
	case string(openai_compat.FileInputNativeSupported):
		return openai_compat.FileInputNativeSupported
	default:
		return openai_compat.FileInputNativeSupported
	}
}

func headerMap(headers []config.HeaderKV, secretHeaders map[string]string) map[string]string {
	out := map[string]string{}
	for _, kv := range headers {
		key := strings.TrimSpace(kv.Key)
		if key == "" {
			continue
		}
		if kv.IsSecret {
			val := strings.TrimSpace(secretHeaders[key])
			if val == "" {
				continue
			}
			out[key] = val
			continue
		}
		out[key] = kv.Value
	}
	return out
}

func addAPIKeyHeader(headers map[string]string, header string, scheme string, apiKey string, defaultHeader string) {
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return
	}
	hdr := strings.TrimSpace(header)
	if hdr == "" {
		hdr = defaultHeader
	}
	val := key
	if strings.TrimSpace(scheme) != "" {
		val = strings.TrimSpace(scheme) + " " + key
	}
	headers[hdr] = val
}

func openAIWebSearchConfig(cfg *config.ProviderWebSearchConfig) *openai_compat.WebSearchConfig {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil
	}
	return &openai_compat.WebSearchConfig{
		Enabled:  true,
		ToolType: "web_search",
	}
}

func openAIWebSearchConfigWithPolicy(cfg *config.ProviderWebSearchConfig, policy HostedToolPolicy, adapterFamily string) *openai_compat.WebSearchConfig {
	if policy.Disables(strings.TrimSpace(adapterFamily) + "/web_search") {
		return nil
	}
	return openAIWebSearchConfig(cfg)
}

func openAICodeExecutionConfig(cfg *config.ProviderCodeExecutionConfig) *openai_compat.CodeExecutionConfig {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil
	}
	return &openai_compat.CodeExecutionConfig{
		Enabled:       true,
		ToolType:      trimmedStringPtr(cfg.ToolType),
		ContainerMode: trimmedStringPtr(cfg.ContainerMode),
		ContainerID:   trimmedStringPtr(cfg.ContainerID),
		MemoryLimit:   trimmedStringPtr(cfg.MemoryLimit),
	}
}

func openAICodeExecutionConfigWithPolicy(cfg *config.ProviderCodeExecutionConfig, policy HostedToolPolicy, adapterFamily string) *openai_compat.CodeExecutionConfig {
	if policy.Disables(strings.TrimSpace(adapterFamily) + "/code_interpreter") {
		return nil
	}
	return openAICodeExecutionConfig(cfg)
}

func anthropicWebSearchConfig(cfg *config.ProviderWebSearchConfig) *anthropic.WebSearchConfig {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil
	}
	out := &anthropic.WebSearchConfig{
		Enabled:        true,
		ToolType:       "web_search_20260209",
		AllowedCallers: []string{"direct"},
	}
	maxUses := 5
	out.MaxUses = &maxUses
	return out
}

func anthropicWebSearchConfigWithPolicy(cfg *config.ProviderWebSearchConfig, policy HostedToolPolicy, adapterFamily string) *anthropic.WebSearchConfig {
	if policy.Disables(strings.TrimSpace(adapterFamily) + "/web_search") {
		return nil
	}
	return anthropicWebSearchConfig(cfg)
}

func anthropicCodeExecutionConfig(cfg *config.ProviderCodeExecutionConfig) *anthropic.CodeExecutionConfig {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil
	}
	out := &anthropic.CodeExecutionConfig{
		Enabled:       true,
		ToolType:      trimmedStringPtr(cfg.ToolType),
		ContainerMode: trimmedStringPtr(cfg.ContainerMode),
		ContainerID:   trimmedStringPtr(cfg.ContainerID),
		MemoryLimit:   trimmedStringPtr(cfg.MemoryLimit),
	}
	for _, skill := range cfg.Skills {
		skillType := strings.TrimSpace(skill.Type)
		skillID := strings.TrimSpace(skill.SkillID)
		if skillType == "" || skillID == "" {
			continue
		}
		entry := anthropic.CodeExecutionSkill{
			Type:    skillType,
			SkillID: skillID,
		}
		if skill.Version != nil && strings.TrimSpace(*skill.Version) != "" {
			v := strings.TrimSpace(*skill.Version)
			entry.Version = &v
		}
		out.Skills = append(out.Skills, entry)
	}
	return out
}

func anthropicCodeExecutionConfigWithPolicy(cfg *config.ProviderCodeExecutionConfig, policy HostedToolPolicy, adapterFamily string) *anthropic.CodeExecutionConfig {
	if policy.Disables(strings.TrimSpace(adapterFamily) + "/code_execution") {
		return nil
	}
	return anthropicCodeExecutionConfig(cfg)
}
