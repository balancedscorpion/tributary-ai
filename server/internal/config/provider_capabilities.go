package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/providers/capabilities"
)

type providerFingerprintHeader struct {
	Key      string `json:"key"`
	Value    string `json:"value,omitempty"`
	IsSecret bool   `json:"isSecret"`
}

type providerFingerprintMaterial struct {
	SchemaVersion      int                         `json:"schemaVersion"`
	AdapterVersion     string                      `json:"adapterVersion"`
	ProviderID         string                      `json:"providerId"`
	APISurface         string                      `json:"apiSurface"`
	Model              string                      `json:"model"`
	BaseURL            string                      `json:"baseUrl"`
	AuthType           string                      `json:"authType"`
	AuthHeader         string                      `json:"authHeader"`
	AuthScheme         string                      `json:"authScheme"`
	AWSCredentialID    string                      `json:"awsCredentialId"`
	GCPCredentialID    string                      `json:"gcpCredentialId"`
	Region             string                      `json:"region"`
	AnthropicVersion   string                      `json:"anthropicVersion"`
	EndpointSuffix     string                      `json:"endpointSuffix"`
	ModelInPath        *bool                       `json:"modelInPath"`
	ModelInBody        *bool                       `json:"modelInBody"`
	FileInputMode      string                      `json:"fileInputMode"`
	IncludeStreamUsage *bool                       `json:"includeStreamUsage"`
	UsageSemantics     string                      `json:"usageSemantics"`
	Headers            []providerFingerprintHeader `json:"headers"`
	QueryParams        map[string]string           `json:"queryParams"`
	PayloadOverrides   map[string]any              `json:"payloadOverrides"`
}

type providerPlanPolicyAttachment struct {
	Class      string `json:"class"`
	Action     string `json:"action"`
	APISurface string `json:"apiSurface"`
}

// ProviderPlanPolicyFingerprint tracks mutable connection controls and saved
// consent separately from the evidence/routing fingerprint.
func ProviderPlanPolicyFingerprint(cfg ProviderConfig) string {
	controls := make(map[string]string, len(cfg.CapabilityControls))
	for key, value := range cfg.CapabilityControls {
		key = strings.TrimSpace(key)
		if key != "" {
			controls[key] = strings.ToLower(strings.TrimSpace(value))
		}
	}
	policies := make([]providerPlanPolicyAttachment, 0, len(cfg.AttachmentPolicies))
	for _, policy := range cfg.AttachmentPolicies {
		policies = append(policies, providerPlanPolicyAttachment{
			Class: strings.TrimSpace(policy.Class), Action: strings.TrimSpace(policy.Action), APISurface: strings.TrimSpace(policy.APISurface),
		})
	}
	sort.Slice(policies, func(i, j int) bool {
		if policies[i].Class != policies[j].Class {
			return policies[i].Class < policies[j].Class
		}
		if policies[i].Action != policies[j].Action {
			return policies[i].Action < policies[j].Action
		}
		return policies[i].APISurface < policies[j].APISurface
	})
	material := struct {
		SchemaVersion     int                            `json:"schemaVersion"`
		Controls          map[string]string              `json:"controls"`
		WebSearch         *ProviderWebSearchConfig       `json:"webSearch"`
		CodeExecution     *ProviderCodeExecutionConfig   `json:"codeExecution"`
		CompactionEnabled *bool                          `json:"compactionEnabled"`
		Policies          []providerPlanPolicyAttachment `json:"policies"`
	}{1, controls, cfg.WebSearch, cfg.CodeExecution, cfg.CompactionEnabled, policies}
	raw, _ := json.Marshal(material)
	sum := sha256.Sum256(raw)
	return "connection-policy-v1:" + hex.EncodeToString(sum[:])
}

// DeriveProviderAPISurface maps legacy providerId/apiType settings to the
// canonical runtime surface. An explicit apiSurface always wins so Bedrock
// Invoke and Converse can be represented independently.
func DeriveProviderAPISurface(cfg ProviderConfig) string {
	if value := strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.APISurface))); value != "" {
		return value
	}
	providerID := strings.ToLower(strings.TrimSpace(cfg.ProviderID))
	apiType := strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.ApiType)))
	switch providerID {
	case "openai":
		return "responses"
	case "anthropic":
		return "messages"
	case "gemini":
		return "generate_content"
	case "bedrock":
		switch apiType {
		case "responses":
			return "responses"
		case "chat_completions":
			return "chat_completions"
		default:
			return "invoke_stream"
		}
	case "compatible":
		if apiType == "responses" {
			return "responses"
		}
		return "chat_completions"
	default:
		return apiType
	}
}

// ProviderConfigFingerprint hashes capability-relevant routing configuration.
// It deliberately excludes identity, labels, timestamps, capability controls,
// and attachment policies: those are preferences, not evidence inputs.
func ProviderConfigFingerprint(cfg ProviderConfig) string {
	headers := make([]providerFingerprintHeader, 0, len(cfg.ExtraHeaders))
	for _, header := range cfg.ExtraHeaders {
		entry := providerFingerprintHeader{
			Key:      strings.ToLower(strings.TrimSpace(header.Key)),
			IsSecret: header.IsSecret,
		}
		if !header.IsSecret {
			entry.Value = strings.TrimSpace(header.Value)
		}
		headers = append(headers, entry)
	}
	sort.Slice(headers, func(i, j int) bool {
		if headers[i].Key != headers[j].Key {
			return headers[i].Key < headers[j].Key
		}
		if headers[i].IsSecret != headers[j].IsSecret {
			return !headers[i].IsSecret
		}
		return headers[i].Value < headers[j].Value
	})

	material := providerFingerprintMaterial{
		SchemaVersion:      1,
		AdapterVersion:     capabilities.DefaultAdapterVersion,
		ProviderID:         strings.ToLower(strings.TrimSpace(cfg.ProviderID)),
		APISurface:         DeriveProviderAPISurface(cfg),
		Model:              strings.TrimSpace(cfg.Model),
		BaseURL:            strings.TrimRight(strings.TrimSpace(ptrStringValue(cfg.BaseURL)), "/"),
		AuthType:           strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.AuthType))),
		AuthHeader:         strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.AuthHeader))),
		AuthScheme:         strings.TrimSpace(ptrStringValue(cfg.AuthScheme)),
		Region:             strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.Region))),
		AnthropicVersion:   strings.TrimSpace(ptrStringValue(cfg.AnthropicVersion)),
		EndpointSuffix:     strings.TrimSpace(ptrStringValue(cfg.EndpointSuffix)),
		ModelInPath:        cloneBoolPtr(cfg.ModelInPath),
		ModelInBody:        cloneBoolPtr(cfg.ModelInBody),
		FileInputMode:      strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.FileInputMode))),
		IncludeStreamUsage: cloneBoolPtr(cfg.IncludeStreamUsage),
		UsageSemantics:     strings.ToLower(strings.TrimSpace(ptrStringValue(cfg.UsageSemantics))),
		Headers:            headers,
		QueryParams:        normalizedStringMap(cfg.QueryParams),
		PayloadOverrides:   cloneMapAny(cfg.PayloadOverrides),
	}
	if cfg.CloudCredentialRefs != nil {
		material.AWSCredentialID = strings.TrimSpace(ptrStringValue(cfg.CloudCredentialRefs.AWSCredentialID))
		material.GCPCredentialID = strings.TrimSpace(ptrStringValue(cfg.CloudCredentialRefs.GCPCredentialID))
	}
	raw, err := json.Marshal(material)
	if err != nil {
		// ProviderConfig's payload overrides originate in JSON and should always
		// marshal. Keep the function total if a programmatic caller supplies an
		// unsupported value such as a channel.
		raw = []byte("unmarshalable-provider-config")
	}
	sum := sha256.Sum256(raw)
	return "provider-capability-v1:" + hex.EncodeToString(sum[:])
}

func normalizeProviderCapabilityMetadata(cfg ProviderConfig) ProviderConfig {
	surface := DeriveProviderAPISurface(cfg)
	if surface != "" {
		cfg.APISurface = &surface
	}
	if cfg.CapabilityControls != nil {
		controls := make(map[string]string, len(cfg.CapabilityControls))
		for rawKey, rawValue := range cfg.CapabilityControls {
			key := strings.TrimSpace(rawKey)
			value := strings.ToLower(strings.TrimSpace(rawValue))
			if key == "" || (value != "auto" && value != "on" && value != "off") {
				continue
			}
			controls[key] = value
		}
		// A non-nil controls map marks a config created or saved by the new
		// capability editor. Materialize the explicit-consent defaults so even an
		// empty map survives JSON's omitempty round trip and can never be mistaken
		// for a legacy OpenAI/Anthropic config on a later load.
		switch strings.ToLower(strings.TrimSpace(cfg.ProviderID)) {
		case "openai", "anthropic":
			if _, ok := controls[string(capabilities.FeatureHostedWebSearch)]; !ok {
				controls[string(capabilities.FeatureHostedWebSearch)] = string(capabilities.ControlOff)
			}
			if _, ok := controls[string(capabilities.FeatureHostedCodeExecution)]; !ok {
				controls[string(capabilities.FeatureHostedCodeExecution)] = string(capabilities.ControlOff)
			}
		}
		if len(controls) == 0 {
			cfg.CapabilityControls = nil
		} else {
			cfg.CapabilityControls = controls
		}
	}
	if cfg.AttachmentPolicies != nil {
		policies := make([]ProviderAttachmentPolicy, 0, len(cfg.AttachmentPolicies))
		seen := map[string]struct{}{}
		for _, policy := range cfg.AttachmentPolicies {
			policy.ID = strings.TrimSpace(policy.ID)
			policy.Class = strings.ToLower(strings.TrimSpace(policy.Class))
			policy.Action = strings.ToLower(strings.TrimSpace(policy.Action))
			policy.APISurface = strings.ToLower(strings.TrimSpace(policy.APISurface))
			if policy.Class == "" || policy.Action == "" || policy.APISurface == "" {
				continue
			}
			key := policy.Class + "\x00" + policy.Action + "\x00" + policy.APISurface
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			policies = append(policies, policy)
		}
		if len(policies) == 0 {
			cfg.AttachmentPolicies = nil
		} else {
			cfg.AttachmentPolicies = policies
		}
	}
	fingerprint := ProviderConfigFingerprint(cfg)
	cfg.ConfigFingerprint = &fingerprint
	return cfg
}

func normalizedStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}
