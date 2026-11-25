package capabilities

import "strings"

type catalogFeature struct {
	State      SupportState
	Mode       *CapabilityMode
	Conditions []CapabilityExplanation
	Reasons    []CapabilityExplanation
	Limits     *CapabilityLimits
	Detail     map[string]any
}

type catalogEntry struct {
	ID                    string
	ProviderFamily        string
	APISurface            string
	EndpointFamilyPattern string
	ModelPattern          string
	RegionPattern         string
	AdapterVersionPattern string
	LastVerifiedAt        string
	Sources               []string
	Features              map[FeatureKey]catalogFeature
}

func curatedCatalog() []catalogEntry {
	return []catalogEntry{
		openAIResponsesCatalog(),
		anthropicMessagesCatalog(),
		geminiGenerateContentCatalog(),
		bedrockInvokeCatalog(),
		bedrockConverseCatalog(),
		bedrockChatCatalog(),
		bedrockResponsesCatalog(),
		groqChatCatalog(),
		togetherChatCatalog(),
		fireworksChatCatalog(),
		openRouterChatCatalog(),
		compatibleChatCatalog(),
		azureOpenAIResponsesCatalog(),
		compatibleResponsesCatalog(),
	}
}

func catalogBase(id, family, surface string, sources ...string) catalogEntry {
	features := make(map[FeatureKey]catalogFeature, len(featureRegistry))
	for _, definition := range featureRegistry {
		features[definition.Key] = catalogFeature{
			State: StateUnknown,
			Reasons: []CapabilityExplanation{{
				Code: "not_cataloged",
				Text: "This capability has not been verified for the exact target.",
			}},
		}
	}
	return catalogEntry{
		ID:                    id,
		ProviderFamily:        family,
		APISurface:            surface,
		EndpointFamilyPattern: "*",
		ModelPattern:          "*",
		RegionPattern:         "*",
		AdapterVersionPattern: DefaultAdapterVersion,
		LastVerifiedAt:        "2026-07-11",
		Sources:               append([]string(nil), sources...),
		Features:              features,
	}
}

func (entry catalogEntry) supported(keys ...FeatureKey) catalogEntry {
	for _, key := range keys {
		entry.Features[key] = catalogFeature{State: StateSupported, Mode: modePtr(ModeNative)}
	}
	return entry
}

func (entry catalogEntry) unsupported(reasonCode, reasonText string, keys ...FeatureKey) catalogEntry {
	for _, key := range keys {
		entry.Features[key] = catalogFeature{
			State:   StateUnsupported,
			Reasons: []CapabilityExplanation{{Code: reasonCode, Text: reasonText}},
		}
	}
	return entry
}

func (entry catalogEntry) conditional(reasonCode, reasonText string, keys ...FeatureKey) catalogEntry {
	for _, key := range keys {
		entry.Features[key] = catalogFeature{
			State: StateConditional,
			Mode:  modePtr(ModeNative),
			Conditions: []CapabilityExplanation{{
				Code: reasonCode,
				Text: reasonText,
			}},
		}
	}
	return entry
}

func (entry catalogEntry) unknown(reasonCode, reasonText string, keys ...FeatureKey) catalogEntry {
	for _, key := range keys {
		entry.Features[key] = catalogFeature{
			State: StateUnknown,
			Reasons: []CapabilityExplanation{{
				Code: reasonCode,
				Text: reasonText,
			}},
		}
	}
	return entry
}

func (entry catalogEntry) set(key FeatureKey, feature catalogFeature) catalogEntry {
	entry.Features[key] = feature
	return entry
}

func openAIResponsesCatalog() catalogEntry {
	entry := catalogBase(
		"openai.responses.repository.v1",
		"openai",
		"responses",
		"https://developers.openai.com/api/docs/guides/function-calling",
		"https://developers.openai.com/api/docs/guides/tools-web-search",
		"https://developers.openai.com/api/docs/guides/file-inputs",
	)
	entry.EndpointFamilyPattern = "openai-api"
	entry = entry.supported(
		FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools, FeatureToolChoice,
		FeatureToolStatePreservation, FeatureImages, FeatureSessionContinuation,
		FeatureCacheReporting, FeatureRichUsage,
	)
	entry = entry.conditional("model_dependent", "Availability depends on the selected OpenAI model.",
		FeatureHostedWebSearch, FeatureHostedCodeExecution, FeatureReasoningRequestControl,
		FeatureNativeCompaction, FeaturePromptCaching, FeatureContextLimits,
	)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.conditional("native_with_reduced_fallback", "Native file input is attempted first; rejection can require an explicit extracted-text fallback.", FeatureDocuments)
	entry = entry.set(FeatureCitations, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeEmulated),
		Conditions: []CapabilityExplanation{{Code: "flattened_annotations", Text: "Source links are surfaced, but citation offsets are not preserved."}},
	})
	entry = entry.unsupported("adapter_gap", "The current Responses adapter does not expose this capability.",
		FeatureTextNonStreaming, FeatureStrictToolSchemas, FeatureHostedComputerUse,
		FeatureProviderGeneratedFiles, FeatureThinkingDisplay, FeatureThinkingStatePreservation,
		FeatureStructuredOutput, FeatureTokenCounting,
		FeatureSafetyControls,
	)
	return entry
}

func anthropicMessagesCatalog() catalogEntry {
	entry := catalogBase(
		"anthropic.messages.repository.v1",
		"anthropic",
		"messages",
		"https://platform.claude.com/docs/en/agents-and-tools/tool-use/overview",
		"https://platform.claude.com/docs/en/agents-and-tools/tool-use/web-search-tool",
		"https://platform.claude.com/docs/en/build-with-claude/files",
	)
	entry.EndpointFamilyPattern = "anthropic-api"
	entry = entry.supported(
		FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools, FeatureImages,
		FeatureToolStatePreservation, FeatureSessionContinuation, FeatureCacheReporting, FeatureRichUsage,
	)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.conditional("model_and_tool_version_dependent", "The selected Claude model and hosted-tool version must support this capability.", FeatureHostedWebSearch, FeatureHostedCodeExecution)
	entry = entry.conditional("format_dependent", "PDF and text-like files are native; some office files require the Anthropic Files API and explicitly permitted hosted code execution.", FeatureDocuments)
	entry = entry.conditional("provider_emitted", "Thinking is displayed only when the provider emits thinking blocks.", FeatureThinkingDisplay)
	entry = entry.set(FeatureNativeCompaction, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeReduced),
		Conditions: []CapabilityExplanation{{Code: "compaction_state_not_preserved", Text: "Compaction can run, but the streamed compaction block is not replayed on the next request."}},
	})
	entry = entry.conditional("heuristic_limits", "Context and output limits are currently selected with model-name heuristics.", FeatureContextLimits)
	entry = entry.unsupported("adapter_gap", "The current Anthropic adapter does not expose this capability.",
		FeatureTextNonStreaming, FeatureToolChoice, FeatureStrictToolSchemas,
		FeatureHostedComputerUse, FeatureProviderGeneratedFiles, FeatureReasoningRequestControl,
		FeatureThinkingStatePreservation, FeatureStructuredOutput, FeatureCitations,
		FeaturePromptCaching, FeatureTokenCounting,
		FeatureSafetyControls,
	)
	return entry
}

func geminiGenerateContentCatalog() catalogEntry {
	entry := catalogBase(
		"gemini.generate-content.repository.v1",
		"gemini",
		"generate_content",
		"https://ai.google.dev/api/generate-content",
		"https://ai.google.dev/gemini-api/docs/generate-content/function-calling",
		"https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures",
	)
	entry.EndpointFamilyPattern = "gemini-developer"
	entry = entry.supported(
		FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools,
		FeatureToolStatePreservation, FeatureThinkingStatePreservation, FeatureCacheReporting, FeatureRichUsage,
	)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.set(FeatureImages, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeNative),
		Conditions: []CapabilityExplanation{
			{Code: "verified_image_mime_types", Text: "The first-party FileAttachment path natively supports PNG, JPEG, WEBP, HEIC, and HEIF."},
			{Code: "gif_unverified", Text: "GIF is forwarded but has not yet been capability-verified."},
		},
		Limits: &CapabilityLimits{
			MIMETypes:          []string{"image/png", "image/jpeg", "image/webp", "image/heic", "image/heif", "image/gif"},
			RequestBudgetBytes: int64Ptr(20 << 20),
		},
	})
	entry = entry.set(FeatureDocuments, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeNative),
		Conditions: []CapabilityExplanation{{Code: "inline_formats_only", Text: "PDF and text/code-like formats are sent inline within the request budget."}},
		Limits:     &CapabilityLimits{RequestBudgetBytes: int64Ptr(20 << 20)},
	})
	entry = entry.conditional("heuristic_limits", "Context and output limits are currently selected with model-name heuristics.", FeatureContextLimits)
	entry = entry.unsupported("adapter_gap", "Gemini supports this upstream, but the current GenerateContent adapter does not expose it safely.",
		FeatureTextNonStreaming, FeatureToolChoice, FeatureStrictToolSchemas,
		FeatureHostedWebSearch, FeatureHostedCodeExecution,
		FeatureHostedComputerUse, FeatureProviderGeneratedFiles, FeatureReasoningRequestControl,
		FeatureThinkingDisplay, FeatureStructuredOutput,
		FeatureCitations, FeatureNativeCompaction, FeatureSessionContinuation, FeaturePromptCaching,
		FeatureTokenCounting, FeatureSafetyControls,
	)
	return entry
}

func bedrockInvokeCatalog() catalogEntry {
	entry := catalogBase(
		"bedrock.claude-invoke.repository.v1",
		"bedrock",
		"invoke_stream",
		"https://docs.aws.amazon.com/bedrock/latest/userguide/model-parameters-anthropic-claude-messages.html",
		"https://docs.aws.amazon.com/bedrock/latest/userguide/claude-messages-compaction.html",
		"https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_DocumentBlock.html",
	)
	entry.EndpointFamilyPattern = "bedrock-runtime"
	entry = entry.supported(FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools, FeatureCacheReporting, FeatureRichUsage)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.conditional("legacy_path_only", "Only the legacy inline-image path is reliably native in the current product flow.", FeatureImages)
	entry = entry.set(FeatureDocuments, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeNative),
		Conditions: []CapabilityExplanation{{Code: "requires_converse_switch", Text: "Documents require an explicit switch from InvokeModel to Converse for this run."}},
		Reasons:    []CapabilityExplanation{{Code: "requires_converse_switch", Text: "InvokeModel does not carry the repository's document blocks; the adapter changes API surface."}},
	})
	entry = bedrockHostedToolsUnsupported(entry)
	entry = entry.conditional("provider_emitted", "Thinking text may be displayed, but signatures and opaque state are discarded.", FeatureThinkingDisplay)
	entry = entry.set(FeatureNativeCompaction, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeReduced),
		Conditions: []CapabilityExplanation{{Code: "compaction_state_not_preserved", Text: "Compaction requests and events work on selected models, but their opaque state is not replayed."}},
	})
	entry = entry.conditional("heuristic_limits", "Context and output limits are currently selected with Claude model-name heuristics.", FeatureContextLimits)
	entry = entry.unsupported("adapter_gap", "The current Bedrock Invoke adapter does not expose this capability.",
		FeatureTextNonStreaming, FeatureToolChoice, FeatureStrictToolSchemas,
		FeatureToolStatePreservation, FeatureProviderGeneratedFiles, FeatureReasoningRequestControl,
		FeatureThinkingStatePreservation, FeatureStructuredOutput, FeatureCitations,
		FeatureSessionContinuation, FeaturePromptCaching,
		FeatureTokenCounting, FeatureSafetyControls,
	)
	return entry
}

func bedrockConverseCatalog() catalogEntry {
	entry := catalogBase(
		"bedrock.converse.repository.v1",
		"bedrock",
		"converse_stream",
		"https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference-call.html",
		"https://docs.aws.amazon.com/bedrock/latest/APIReference/API_runtime_DocumentBlock.html",
		"https://docs.aws.amazon.com/bedrock/latest/userguide/conversation-inference-supported-models-features.html",
	)
	entry.EndpointFamilyPattern = "bedrock-runtime"
	entry = entry.supported(FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools, FeatureCacheReporting, FeatureRichUsage)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.conditional("legacy_path_only", "Only the legacy inline-image path is reliably native in the current product flow.", FeatureImages)
	entry = entry.set(FeatureDocuments, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeNative),
		Conditions: []CapabilityExplanation{
			{Code: "max_documents_5", Text: "A request can contain at most 5 document blocks."},
			{Code: "max_document_bytes_4718592", Text: "Each native document is capped at 4.5 MiB by the adapter."},
			{Code: "format_dependent", Text: "PDF, TXT, Markdown, HTML, CSV, DOC/DOCX, and XLS/XLSX are native; unsupported formats require explicit extracted-text fallback."},
		},
		Limits: &CapabilityLimits{
			MIMETypes: []string{"application/pdf", "text/plain", "text/markdown", "text/html", "text/csv", "application/msword", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", "application/vnd.ms-excel", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
			MaxBytes:  int64Ptr((9 * 1024 * 1024) / 2), MaxCount: intPtr(5), RequestBudgetBytes: int64Ptr(20 << 20),
		},
	})
	entry = bedrockHostedToolsUnsupported(entry)
	entry = entry.conditional("provider_emitted", "Reasoning text may be displayed, but signatures and opaque state are discarded.", FeatureThinkingDisplay)
	entry = entry.conditional("heuristic_limits", "Context and output limits are currently selected with Claude model-name heuristics.", FeatureContextLimits)
	entry = entry.unsupported("adapter_gap", "The current Bedrock Converse adapter does not expose this capability.",
		FeatureTextNonStreaming, FeatureToolChoice, FeatureStrictToolSchemas,
		FeatureToolStatePreservation, FeatureProviderGeneratedFiles, FeatureReasoningRequestControl,
		FeatureThinkingStatePreservation, FeatureStructuredOutput, FeatureCitations,
		FeatureSessionContinuation, FeaturePromptCaching,
		FeatureTokenCounting, FeatureSafetyControls,
	)
	entry = entry.unsupported("not_on_converse", "Claude compaction is available through InvokeModel, not the Converse path used by this adapter.", FeatureNativeCompaction)
	return entry
}

func bedrockChatCatalog() catalogEntry {
	entry := compatibleChatCatalog()
	entry.ID = "bedrock.chat-completions.repository.v1"
	entry.ProviderFamily = "bedrock"
	entry.APISurface = "chat_completions"
	entry.EndpointFamilyPattern = "bedrock-runtime"
	entry.Sources = []string{
		"https://docs.aws.amazon.com/bedrock/latest/userguide/inference-chat-completions.html",
		"https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html",
	}
	entry = entry.conditional("model_api_dependent", "The selected Bedrock model must support the OpenAI-compatible Chat Completions API.",
		FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools, FeatureToolChoice,
		FeatureToolStatePreservation, FeatureImages, FeatureContextLimits, FeatureCacheReporting, FeatureRichUsage,
	)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.set(FeatureDocuments, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeReduced),
		Conditions: []CapabilityExplanation{{Code: "nonstandard_file_part", Text: "The adapter sends a nonstandard Chat file part whose support depends on the selected Bedrock model."}},
	})
	entry = entry.conditional("model_api_dependent", "A static reasoning-effort field is sent when configured; support depends on the selected Bedrock model.", FeatureReasoningRequestControl)
	entry = bedrockHostedToolsUnsupported(entry)
	return entry
}

func bedrockResponsesCatalog() catalogEntry {
	entry := compatibleResponsesCatalog()
	entry.ID = "bedrock.mantle-responses.repository.v1"
	entry.ProviderFamily = "bedrock"
	entry.APISurface = "responses"
	entry.EndpointFamilyPattern = "bedrock-mantle"
	entry.Sources = []string{
		"https://docs.aws.amazon.com/bedrock/latest/userguide/bedrock-mantle.html",
		"https://docs.aws.amazon.com/bedrock/latest/userguide/endpoints.html",
	}
	entry = entry.conditional("model_api_dependent", "The selected Bedrock model must support the Mantle Responses API.",
		FeatureTextStreaming, FeatureSystemInstructions, FeatureCustomTools, FeatureToolChoice,
		FeatureToolStatePreservation, FeatureImages, FeatureReasoningRequestControl,
		FeatureNativeCompaction, FeatureSessionContinuation, FeatureContextLimits, FeatureCacheReporting, FeatureRichUsage,
	)
	entry = entry.conditional("serial_execution", "The provider may return parallel calls, but the app executes them serially.", FeatureParallelTools)
	entry = entry.set(FeatureDocuments, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeNative),
		Conditions: []CapabilityExplanation{{Code: "openai_shape_required", Text: "Native OpenAI-shaped file input is attempted; a rejection may require explicit extracted-text fallback."}},
	})
	entry = entry.set(FeatureCitations, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeEmulated),
		Conditions: []CapabilityExplanation{{Code: "openai_annotations_required", Text: "Only OpenAI-shaped URL annotations are recognized, and offsets are flattened."}},
	})
	entry = bedrockHostedToolsUnsupported(entry)
	return entry
}

func compatibleChatCatalog() catalogEntry {
	entry := catalogBase(
		"compatible.chat-completions.repository.v1",
		"compatible",
		"chat_completions",
		"https://platform.openai.com/docs/api-reference/chat",
	)
	entry = entry.supported(FeatureTextStreaming)
	entry = entry.unknown(
		"endpoint_model_unverified",
		"This capability has not been verified for the exact compatible endpoint and selected model.",
		FeatureSystemInstructions, FeatureCustomTools, FeatureToolChoice, FeatureParallelTools,
		FeatureToolStatePreservation, FeatureImages, FeatureDocuments,
		FeatureReasoningRequestControl, FeatureContextLimits,
	)
	entry = entry.conditional("usage_reporting_opt_in", "Detailed usage is available only when this exact endpoint supports streamed usage and the connection explicitly enables it.", FeatureCacheReporting, FeatureRichUsage)
	entry = entry.unsupported("adapter_gap", "The Chat Completions product integration does not expose this capability.",
		FeatureTextNonStreaming, FeatureStrictToolSchemas, FeatureHostedWebSearch,
		FeatureHostedCodeExecution, FeatureHostedComputerUse, FeatureProviderGeneratedFiles,
		FeatureThinkingDisplay, FeatureThinkingStatePreservation, FeatureStructuredOutput,
		FeatureCitations, FeatureNativeCompaction, FeatureSessionContinuation, FeaturePromptCaching,
		FeatureTokenCounting, FeatureSafetyControls,
	)
	return entry
}

func compatibleResponsesCatalog() catalogEntry {
	entry := catalogBase(
		"compatible.responses.repository.v1",
		"compatible",
		"responses",
		"https://platform.openai.com/docs/api-reference/responses",
	)
	entry = entry.supported(FeatureTextStreaming)
	entry = entry.unknown(
		"endpoint_model_unverified",
		"This capability has not been verified for the exact compatible endpoint and selected model.",
		FeatureSystemInstructions, FeatureCustomTools, FeatureToolChoice, FeatureParallelTools,
		FeatureToolStatePreservation, FeatureImages, FeatureDocuments, FeatureHostedWebSearch,
		FeatureHostedCodeExecution, FeatureReasoningRequestControl, FeatureCitations,
		FeatureNativeCompaction, FeatureSessionContinuation, FeatureContextLimits,
	)
	entry = entry.conditional("usage_reporting_shape", "Detailed usage is available when the endpoint returns OpenAI-shaped usage metadata for this exact model.", FeatureCacheReporting, FeatureRichUsage)
	entry = entry.unsupported("adapter_gap", "The Responses-compatible product integration does not expose this capability.",
		FeatureTextNonStreaming, FeatureStrictToolSchemas, FeatureHostedComputerUse,
		FeatureProviderGeneratedFiles, FeatureThinkingDisplay, FeatureThinkingStatePreservation,
		FeatureStructuredOutput, FeaturePromptCaching, FeatureTokenCounting,
		FeatureSafetyControls,
	)
	return entry
}

func azureOpenAIResponsesCatalog() catalogEntry {
	entry := compatibleResponsesCatalog()
	entry.ID = "compatible.azure-openai-responses.repository.v1"
	entry.EndpointFamilyPattern = "azure-openai"
	entry.Sources = []string{
		"https://learn.microsoft.com/en-us/azure/foundry/openai/how-to/web-search",
		"https://learn.microsoft.com/en-us/azure/ai-services/openai/how-to/responses",
	}
	entry = entry.conditional(
		"model_dependent",
		"The selected Azure OpenAI deployment must support Responses web search, and the subscription must permit it.",
		FeatureHostedWebSearch,
	)
	return entry
}

func groqChatCatalog() catalogEntry {
	return compatiblePresetChatCatalog(
		"compatible.groq-chat.repository.v1",
		"groq",
		"The selected Groq model must support local tool use.",
		false,
		"https://console.groq.com/docs/openai",
		"https://console.groq.com/docs/tool-use/local-tool-calling",
	)
}

func togetherChatCatalog() catalogEntry {
	return compatiblePresetChatCatalog(
		"compatible.together-chat.repository.v1",
		"together",
		"The selected Together AI model must support function calling.",
		true,
		"https://docs.together.ai/docs/inference/openai-compatibility",
		"https://docs.together.ai/docs/inference/function-calling/overview",
	)
}

func fireworksChatCatalog() catalogEntry {
	return compatiblePresetChatCatalog(
		"compatible.fireworks-chat.repository.v1",
		"fireworks",
		"The selected Fireworks AI model must support function calling.",
		true,
		"https://docs.fireworks.ai/tools-sdks/openai-compatibility",
		"https://docs.fireworks.ai/guides/function-calling",
	)
}

func openRouterChatCatalog() catalogEntry {
	return compatiblePresetChatCatalog(
		"compatible.openrouter-chat.repository.v1",
		"openrouter",
		"Both the selected OpenRouter model and routed provider must support tool calling.",
		false,
		"https://openrouter.ai/docs/guides/features/tool-calling",
		"https://openrouter.ai/docs/api/reference/overview",
	)
}

func compatiblePresetChatCatalog(id, endpointFamily, toolCondition string, imagesVerified bool, sources ...string) catalogEntry {
	entry := compatibleChatCatalog()
	entry.ID = id
	entry.EndpointFamilyPattern = endpointFamily
	entry.Sources = append([]string(nil), sources...)
	entry = entry.supported(FeatureSystemInstructions)
	entry = entry.conditional("model_dependent", toolCondition,
		FeatureCustomTools, FeatureToolChoice, FeatureToolStatePreservation,
	)
	entry = entry.set(FeatureParallelTools, catalogFeature{
		State: StateConditional, Mode: modePtr(ModeNative),
		Conditions: []CapabilityExplanation{
			{Code: "model_dependent", Text: toolCondition},
			{Code: "serial_execution", Text: "The provider may return parallel calls, but the app executes them serially."},
		},
	})
	if imagesVerified {
		entry = entry.conditional("model_dependent", "Standard image URL input is available only on supported vision models.", FeatureImages)
	}
	return entry
}

func bedrockHostedToolsUnsupported(entry catalogEntry) catalogEntry {
	return entry.unsupported(
		"not_on_bedrock",
		"Anthropic hosted web search and hosted code execution are not available through Amazon Bedrock, and this product does not wire Bedrock-native hosted alternatives.",
		FeatureHostedWebSearch, FeatureHostedCodeExecution, FeatureHostedComputerUse,
	)
}

func findCatalogEntry(target TargetDescriptor) (catalogEntry, bool) {
	for _, entry := range curatedCatalog() {
		if !strings.EqualFold(entry.ProviderFamily, target.ProviderFamily) || entry.APISurface != target.APISurface {
			continue
		}
		if !catalogPatternMatches(entry.EndpointFamilyPattern, target.EndpointFamily) ||
			!catalogPatternMatches(entry.ModelPattern, target.Model) ||
			!catalogPatternMatches(entry.RegionPattern, stringValue(target.Region)) ||
			!catalogPatternMatches(entry.AdapterVersionPattern, target.AdapterVersion) {
			continue
		}
		return entry, true
	}
	return catalogEntry{}, false
}

func catalogPatternMatches(pattern, value string) bool {
	pattern = strings.TrimSpace(strings.ToLower(pattern))
	value = strings.TrimSpace(strings.ToLower(value))
	if pattern == "" || pattern == "*" {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(value, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == value
}

func modePtr(value CapabilityMode) *CapabilityMode { return &value }
func intPtr(value int) *int                        { return &value }
func int64Ptr(value int64) *int64                  { return &value }

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
