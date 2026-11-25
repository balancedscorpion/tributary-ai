package capabilities

// FeatureKey is deliberately open-ended. Callers must render unknown keys from
// the metadata carried by FeatureResolution rather than treating this as a
// closed enum.
type FeatureKey string

const (
	FeatureTextStreaming             FeatureKey = "text_streaming"
	FeatureTextNonStreaming          FeatureKey = "text_nonstreaming"
	FeatureSystemInstructions        FeatureKey = "system_instructions"
	FeatureCustomTools               FeatureKey = "custom_tools"
	FeatureToolChoice                FeatureKey = "tool_choice"
	FeatureStrictToolSchemas         FeatureKey = "strict_tool_schemas"
	FeatureParallelTools             FeatureKey = "parallel_tools"
	FeatureToolStatePreservation     FeatureKey = "tool_state_preservation"
	FeatureImages                    FeatureKey = "images"
	FeatureDocuments                 FeatureKey = "documents"
	FeatureHostedWebSearch           FeatureKey = "hosted_web_search"
	FeatureHostedCodeExecution       FeatureKey = "hosted_code_execution"
	FeatureHostedComputerUse         FeatureKey = "hosted_computer_use"
	FeatureProviderGeneratedFiles    FeatureKey = "provider_generated_files"
	FeatureReasoningRequestControl   FeatureKey = "reasoning_request_control"
	FeatureThinkingDisplay           FeatureKey = "thinking_display"
	FeatureThinkingStatePreservation FeatureKey = "thinking_state_preservation"
	FeatureStructuredOutput          FeatureKey = "structured_output"
	FeatureCitations                 FeatureKey = "citations"
	FeatureNativeCompaction          FeatureKey = "native_compaction"
	FeatureSessionContinuation       FeatureKey = "session_continuation"
	FeaturePromptCaching             FeatureKey = "prompt_caching"
	FeatureCacheReporting            FeatureKey = "cache_reporting"
	FeatureTokenCounting             FeatureKey = "token_counting"
	FeatureContextLimits             FeatureKey = "context_limits"
	FeatureRichUsage                 FeatureKey = "rich_usage"
	FeatureSafetyControls            FeatureKey = "safety_controls"
)

type CapabilityClass string

const (
	CapabilityClassStandard        CapabilityClass = "standard"
	CapabilityClassExplicitConsent CapabilityClass = "explicit_consent"
)

type RiskTag string

const (
	RiskBillable       RiskTag = "billable"
	RiskExternalEgress RiskTag = "external_egress"
	RiskCodeExecution  RiskTag = "code_execution"
)

type FeatureMeta struct {
	Label    string `json:"label"`
	Group    string `json:"group"`
	Headline bool   `json:"headline"`
	Order    int    `json:"order"`
}

type FeatureDefinition struct {
	Key          FeatureKey
	Meta         FeatureMeta
	Class        CapabilityClass
	RiskTags     []RiskTag
	Controllable bool
}

var featureRegistry = []FeatureDefinition{
	feature(FeatureTextStreaming, "Streaming responses", "core", true, false),
	feature(FeatureTextNonStreaming, "Non-streaming responses", "core", false, false),
	feature(FeatureSystemInstructions, "System instructions", "core", false, false),
	feature(FeatureCustomTools, "Custom tools", "tools", true, false),
	feature(FeatureToolChoice, "Tool choice", "tools", false, false),
	feature(FeatureStrictToolSchemas, "Strict tool schemas", "tools", false, true),
	feature(FeatureParallelTools, "Parallel tool calls", "tools", false, true),
	feature(FeatureToolStatePreservation, "Tool state preservation", "tools", false, false),
	feature(FeatureImages, "Images", "inputs", true, false),
	feature(FeatureDocuments, "Documents", "inputs", true, false),
	explicitFeature(FeatureHostedWebSearch, "Hosted web search", "hosted", true, RiskBillable, RiskExternalEgress),
	explicitFeature(FeatureHostedCodeExecution, "Hosted code execution", "hosted", true, RiskBillable, RiskCodeExecution),
	explicitFeature(FeatureHostedComputerUse, "Hosted computer use", "hosted", false, RiskExternalEgress, RiskCodeExecution),
	feature(FeatureProviderGeneratedFiles, "Provider-generated files", "hosted", false, false),
	feature(FeatureReasoningRequestControl, "Reasoning controls", "reasoning", false, true),
	feature(FeatureThinkingDisplay, "Thinking display", "reasoning", false, true),
	feature(FeatureThinkingStatePreservation, "Thinking state preservation", "reasoning", false, false),
	feature(FeatureStructuredOutput, "Structured output", "output", true, true),
	feature(FeatureCitations, "Citations", "output", true, true),
	feature(FeatureNativeCompaction, "Native compaction", "state", true, true),
	feature(FeatureSessionContinuation, "Session continuation", "state", false, false),
	feature(FeaturePromptCaching, "Prompt caching", "state", false, true),
	feature(FeatureCacheReporting, "Cache reporting", "state", false, false),
	feature(FeatureTokenCounting, "Token counting", "ops", false, false),
	feature(FeatureContextLimits, "Context limits", "ops", false, false),
	feature(FeatureRichUsage, "Detailed usage", "ops", false, false),
	feature(FeatureSafetyControls, "Safety controls", "safety", false, true),
}

func feature(key FeatureKey, label, group string, headline, controllable bool) FeatureDefinition {
	return FeatureDefinition{
		Key:          key,
		Meta:         FeatureMeta{Label: label, Group: group, Headline: headline},
		Class:        CapabilityClassStandard,
		Controllable: controllable,
	}
}

func explicitFeature(key FeatureKey, label, group string, headline bool, risks ...RiskTag) FeatureDefinition {
	return FeatureDefinition{
		Key:          key,
		Meta:         FeatureMeta{Label: label, Group: group, Headline: headline},
		Class:        CapabilityClassExplicitConsent,
		RiskTags:     append([]RiskTag(nil), risks...),
		Controllable: true,
	}
}

// FeatureRegistry returns a defensive, stably ordered copy of the registry.
func FeatureRegistry() []FeatureDefinition {
	out := make([]FeatureDefinition, len(featureRegistry))
	for i, definition := range featureRegistry {
		definition.Meta.Order = i
		definition.RiskTags = append([]RiskTag(nil), definition.RiskTags...)
		out[i] = definition
	}
	return out
}

func featureDefinition(key FeatureKey) (FeatureDefinition, bool) {
	for i, definition := range featureRegistry {
		if definition.Key == key {
			definition.Meta.Order = i
			definition.RiskTags = append([]RiskTag(nil), definition.RiskTags...)
			return definition, true
		}
	}
	return FeatureDefinition{}, false
}

func isCoreFeature(key FeatureKey) bool {
	definition, ok := featureDefinition(key)
	return ok && definition.Meta.Group == "core"
}
