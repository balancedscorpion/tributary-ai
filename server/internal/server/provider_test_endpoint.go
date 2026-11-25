package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/providers/capabilities"
	chatstore "ai-analyst-local/internal/chat/store"
	chattypes "ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
)

const providerConnectionTestPrompt = "hi"

const (
	providerSystemInstructionProbeExpectedMarker = "SYSTEM_INSTRUCTIONS_CAPABILITY_VERIFIED_8A1C"
	providerSystemInstructionProbeUserMarker     = "SYSTEM_INSTRUCTIONS_CAPABILITY_IGNORED_2F7B"
	providerProbeFinalTextLimit                  = 8 << 10
)

const (
	providerCheckTierQuick      = "quick"
	providerCheckTierCapability = "capability"

	providerProbeVerdictVerified    = "verified"
	providerProbeVerdictUnsupported = "unsupported"
	providerProbeVerdictFailed      = "failed"
	providerProbeVerdictSkipped     = "skipped"
)

var providerCapabilityProbeSet = map[string]capabilities.FeatureKey{
	"text_streaming":              capabilities.FeatureTextStreaming,
	"system_instructions":         capabilities.FeatureSystemInstructions,
	"custom_tools":                capabilities.FeatureCustomTools,
	"images":                      capabilities.FeatureImages,
	"documents":                   capabilities.FeatureDocuments,
	"hosted_web_search":           capabilities.FeatureHostedWebSearch,
	"hosted_code_execution":       capabilities.FeatureHostedCodeExecution,
	"thinking_state_preservation": capabilities.FeatureThinkingStatePreservation,
	"structured_output":           capabilities.FeatureStructuredOutput,
	"native_compaction":           capabilities.FeatureNativeCompaction,
	"citations":                   capabilities.FeatureCitations,
	"cache_reporting":             capabilities.FeatureCacheReporting,
}

type providerConnectionTestRequest struct {
	Config              *config.ProviderConfig `json:"config,omitempty"`
	ProviderConfig      *config.ProviderConfig `json:"providerConfig,omitempty"`
	ConnectionID        string                 `json:"connectionId,omitempty"`
	Model               string                 `json:"model,omitempty"`
	Tier                string                 `json:"tier,omitempty"`
	Probes              []string               `json:"probes,omitempty"`
	ConsentAcknowledged bool                   `json:"consentAcknowledged,omitempty"`
	Endpoint            string                 `json:"endpoint,omitempty"`
}

func handleProviderConnectionTest(w http.ResponseWriter, r *http.Request, opts HandlerOpts) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if opts.Settings == nil || opts.Secrets == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"ok":      false,
			"message": "Settings or secrets store is unavailable.",
		})
		return
	}

	var body providerConnectionTestRequest
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": err.Error(), "error": err.Error()})
		return
	}

	tier := strings.ToLower(strings.TrimSpace(body.Tier))
	if tier != "" {
		handleTieredProviderConnectionTest(w, r, opts, body, tier)
		return
	}

	cfg := body.Config
	if cfg == nil {
		cfg = body.ProviderConfig
	}
	if cfg == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "provider config is required", "error": "provider config is required"})
		return
	}

	result := runProviderConnectionTest(r.Context(), opts, *cfg, strings.TrimSpace(body.Endpoint))
	status := http.StatusOK
	if !result.OK && result.Invalid {
		status = http.StatusBadRequest
	}
	writeJSON(w, status, result)
}

type providerProbeResult struct {
	Probe      string `json:"probe"`
	Verdict    string `json:"verdict"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message"`
	RequestID  string `json:"requestId,omitempty"`
	DurationMS int64  `json:"durationMs"`
}

type providerTieredCheckResult struct {
	OK          bool                  `json:"ok"`
	Fingerprint string                `json:"fingerprint"`
	Results     []providerProbeResult `json:"results"`
	Message     string                `json:"message,omitempty"`
	Endpoint    string                `json:"endpoint,omitempty"`
	ProviderID  string                `json:"providerId,omitempty"`
	Model       string                `json:"model,omitempty"`
}

func handleTieredProviderConnectionTest(w http.ResponseWriter, r *http.Request, opts HandlerOpts, body providerConnectionTestRequest, tier string) {
	if tier != providerCheckTierQuick && tier != providerCheckTierCapability {
		writeProviderCheckError(w, http.StatusBadRequest, "tier must be quick or capability")
		return
	}

	cfg, savedConnectionID, status, err := resolveProviderCheckConfig(opts, body)
	if err != nil {
		writeProviderCheckError(w, status, err.Error())
		return
	}
	if err := validateProviderCheckConfig(cfg); err != nil {
		writeProviderCheckError(w, http.StatusBadRequest, err.Error())
		return
	}

	fingerprint := config.ProviderConfigFingerprint(cfg)
	if tier == providerCheckTierQuick {
		result := runQuickProviderCheck(r.Context(), opts, cfg, fingerprint, strings.TrimSpace(body.Endpoint))
		if savedConnectionID != "" && opts.ChatStore != nil {
			if err := persistProviderProbeEvidence(r.Context(), opts.ChatStore, savedConnectionID, cfg.Model, fingerprint, result.Results); err != nil {
				result.OK = false
				result.Message = "Connection check completed, but its evidence could not be persisted: " + err.Error()
				writeJSON(w, http.StatusInternalServerError, result)
				return
			}
		}
		writeJSON(w, http.StatusOK, result)
		return
	}

	probes, err := normalizeProviderCapabilityProbes(body.Probes)
	if err != nil {
		writeProviderCheckError(w, http.StatusBadRequest, err.Error())
		return
	}
	report, err := resolveProviderCapabilityReport(
		r.Context(), cfg, optionalNonEmptyString(savedConnectionID), opts.Permissions, opts.ChatStore,
	)
	if err != nil {
		writeProviderCheckError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateExplicitConsentProbes(cfg, report, probes, body.ConsentAcknowledged); err != nil {
		writeProviderCheckError(w, http.StatusBadRequest, err.Error())
		return
	}

	result := runCapabilityProviderChecks(r.Context(), opts, cfg, fingerprint, strings.TrimSpace(body.Endpoint), probes, report)
	if savedConnectionID != "" && opts.ChatStore != nil {
		if err := persistProviderProbeEvidence(r.Context(), opts.ChatStore, savedConnectionID, cfg.Model, fingerprint, result.Results); err != nil {
			result.OK = false
			result.Message = "Capability probes completed, but their evidence could not be persisted: " + err.Error()
			writeJSON(w, http.StatusInternalServerError, result)
			return
		}
	}
	writeJSON(w, http.StatusOK, result)
}

func resolveProviderCheckConfig(opts HandlerOpts, body providerConnectionTestRequest) (config.ProviderConfig, string, int, error) {
	direct := body.Config
	if direct == nil {
		direct = body.ProviderConfig
	}
	connectionID := strings.TrimSpace(body.ConnectionID)
	if direct != nil && connectionID != "" {
		return config.ProviderConfig{}, "", http.StatusBadRequest, errors.New("provide either config or connectionId, not both")
	}

	var cfg config.ProviderConfig
	if connectionID != "" {
		found := false
		for _, candidate := range opts.Settings.Get().ProviderConfigs {
			if strings.TrimSpace(candidate.ID) != connectionID {
				continue
			}
			cfg = candidate
			found = true
			break
		}
		if !found {
			return config.ProviderConfig{}, "", http.StatusNotFound, errors.New("provider connection not found")
		}
	} else if direct != nil {
		cfg = *direct
	} else {
		return config.ProviderConfig{}, "", http.StatusBadRequest, errors.New("provider config or connectionId is required")
	}
	if model := strings.TrimSpace(body.Model); model != "" {
		cfg.Model = model
	}
	return cfg, connectionID, http.StatusOK, nil
}

func validateProviderCheckConfig(cfg config.ProviderConfig) error {
	if strings.TrimSpace(cfg.ID) == "" {
		return errors.New("provider config id is required")
	}
	if strings.TrimSpace(cfg.ProviderID) == "" {
		return errors.New("provider id is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return errors.New("model is required")
	}
	return nil
}

func normalizeProviderCapabilityProbes(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, errors.New("probes must contain at least one capability probe")
	}
	seen := make(map[string]struct{}, len(raw))
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		probe := strings.ToLower(strings.TrimSpace(value))
		if _, ok := providerCapabilityProbeSet[probe]; !ok {
			return nil, fmt.Errorf("unknown capability probe: %s", probe)
		}
		if _, ok := seen[probe]; ok {
			continue
		}
		seen[probe] = struct{}{}
		out = append(out, probe)
	}
	if len(out) == 0 {
		return nil, errors.New("probes must contain at least one capability probe")
	}
	return out, nil
}

func validateExplicitConsentProbes(cfg config.ProviderConfig, report capabilities.CapabilityReport, probes []string, consentAcknowledged bool) error {
	for _, probe := range probes {
		if probe != string(capabilities.FeatureHostedWebSearch) && probe != string(capabilities.FeatureHostedCodeExecution) {
			continue
		}
		label := strings.ReplaceAll(probe, "_", " ")
		if strings.EqualFold(strings.TrimSpace(cfg.ProviderID), "bedrock") {
			return fmt.Errorf("%s capability probes are unavailable on Bedrock", label)
		}
		if !consentAcknowledged {
			return fmt.Errorf("%s capability probes require consentAcknowledged=true", label)
		}
		feature, ok := report.Features[providerCapabilityProbeSet[probe]]
		if !ok || feature.Control == nil || feature.Control.Setting != capabilities.ControlOn {
			return fmt.Errorf("%s capability probe requires its control to be On", label)
		}
		if feature.Permission == nil || !feature.Permission.Required || !feature.Permission.Granted {
			return fmt.Errorf("%s capability probe requires an explicit permission grant", label)
		}
		if feature.Effective != capabilities.EffectiveOn {
			return fmt.Errorf("%s capability probe is not effective for this exact target", label)
		}
	}
	return nil
}

func writeProviderCheckError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "message": message, "error": message})
}

func runQuickProviderCheck(ctx context.Context, opts HandlerOpts, cfg config.ProviderConfig, fingerprint, endpoint string) providerTieredCheckResult {
	ctx = usage.WithAttribution(ctx, usage.Attribution{
		Operation: usage.OperationProviderTest, ConnectionID: strings.TrimSpace(cfg.ID),
		ConfigFingerprint: fingerprint, PlannedAPISurface: config.DeriveProviderAPISurface(cfg),
	})
	result := newProviderTieredCheckResult(cfg, fingerprint, endpoint)
	startedAt := time.Now()
	provider, cleanup, err := createProviderProbeProvider(opts, cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		probe := failedProviderProbe("transport", "provider_unavailable", err.Error(), "", startedAt)
		result.Results = []providerProbeResult{probe}
		result.Message = probe.Message
		return result
	}
	result.ProviderID = provider.ID()
	result.Model = provider.Model()
	probe := runProviderTransportProbe(ctx, provider, "transport")
	result.Results = []providerProbeResult{probe}
	result.OK = probe.Verdict == providerProbeVerdictVerified
	result.Message = probe.Message
	return result
}

func runCapabilityProviderChecks(ctx context.Context, opts HandlerOpts, cfg config.ProviderConfig, fingerprint, endpoint string, probes []string, report capabilities.CapabilityReport) providerTieredCheckResult {
	ctx = usage.WithAttribution(ctx, usage.Attribution{
		Operation: usage.OperationCapabilityProbe, ConnectionID: strings.TrimSpace(cfg.ID),
		ConfigFingerprint: fingerprint, PlannedAPISurface: config.DeriveProviderAPISurface(cfg),
	})
	result := newProviderTieredCheckResult(cfg, fingerprint, endpoint)
	var provider chattypes.Provider
	var cleanup func()
	var providerErr error
	providerAttempted := false
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	getProvider := func() (chattypes.Provider, error) {
		if !providerAttempted {
			providerAttempted = true
			provider, cleanup, providerErr = createProviderProbeProvider(opts, cfg)
			if providerErr == nil {
				result.ProviderID = provider.ID()
				result.Model = provider.Model()
			}
		}
		return provider, providerErr
	}

	result.Results = make([]providerProbeResult, 0, len(probes))
	for _, probe := range probes {
		switch probe {
		case string(capabilities.FeatureTextStreaming), string(capabilities.FeatureSystemInstructions), string(capabilities.FeatureCustomTools), string(capabilities.FeatureImages), string(capabilities.FeatureDocuments):
			if unsupported := catalogUnsupportedProbeResult(probe, report); unsupported != nil {
				result.Results = append(result.Results, *unsupported)
				continue
			}
			startedAt := time.Now()
			p, err := getProvider()
			if err != nil {
				result.Results = append(result.Results, failedProviderProbe(probe, "provider_unavailable", err.Error(), "", startedAt))
				continue
			}
			var probeResult providerProbeResult
			switch probe {
			case string(capabilities.FeatureTextStreaming):
				probeResult = runProviderTransportProbe(ctx, p, probe)
			case string(capabilities.FeatureSystemInstructions):
				probeResult = runProviderSystemInstructionsProbe(ctx, p)
			case string(capabilities.FeatureCustomTools):
				probeResult = runProviderCustomToolsProbe(ctx, p)
			case string(capabilities.FeatureImages):
				probeResult = runProviderAttachmentProbe(ctx, p, probe, providerProbeImageAttachment())
			case string(capabilities.FeatureDocuments):
				probeResult = runProviderAttachmentProbe(ctx, p, probe, providerProbeDocumentAttachment())
			}
			result.Results = append(result.Results, probeResult)
		default:
			result.Results = append(result.Results, providerProbeResult{
				Probe: probe, Verdict: providerProbeVerdictSkipped, Reason: "probe_not_implemented",
				Message: "This reserved capability probe is not implemented yet.", DurationMS: 0,
			})
		}
	}

	result.OK = len(result.Results) == len(probes)
	for _, probe := range result.Results {
		if probe.Verdict != providerProbeVerdictVerified && probe.Verdict != providerProbeVerdictUnsupported {
			result.OK = false
			break
		}
	}
	if result.OK {
		result.Message = "Capability checks completed with conclusive results."
	} else {
		result.Message = "One or more capability checks failed or could not be run."
	}
	return result
}

func newProviderTieredCheckResult(cfg config.ProviderConfig, fingerprint, endpoint string) providerTieredCheckResult {
	return providerTieredCheckResult{
		OK: false, Fingerprint: fingerprint, Results: []providerProbeResult{}, Endpoint: endpoint,
		ProviderID: strings.TrimSpace(cfg.ProviderID), Model: strings.TrimSpace(cfg.Model),
	}
}

func catalogUnsupportedProbeResult(probe string, report capabilities.CapabilityReport) *providerProbeResult {
	feature, ok := report.Features[providerCapabilityProbeSet[probe]]
	if !ok || feature.State != capabilities.StateUnsupported {
		return nil
	}
	reason := "catalog_unsupported"
	message := "The exact provider target reports this capability as unsupported."
	if len(feature.Reasons) > 0 {
		if value := strings.TrimSpace(feature.Reasons[0].Code); value != "" {
			reason = value
		}
		if value := strings.TrimSpace(feature.Reasons[0].Text); value != "" {
			message = value
		}
	}
	return &providerProbeResult{
		Probe: probe, Verdict: providerProbeVerdictUnsupported, Reason: reason, Message: message, DurationMS: 0,
	}
}

func createProviderProbeProvider(opts HandlerOpts, cfg config.ProviderConfig) (chattypes.Provider, func(), error) {
	settings := opts.Settings.Get()
	cfg.IsDefault = boolPtrServer(true)
	settings.ProviderConfigs = []config.ProviderConfig{cfg}
	activeID := strings.TrimSpace(cfg.ID)
	settings.ActiveProviderConfigID = &activeID
	settings.Provider = config.ProviderSelection{ProviderID: strings.TrimSpace(cfg.ProviderID), Model: strings.TrimSpace(cfg.Model)}
	if strings.EqualFold(strings.TrimSpace(cfg.ProviderID), "compatible") {
		settings.Provider.ProfileID = &activeID
	}

	tmpDir, err := os.MkdirTemp("", "provider-capability-test-*")
	if err != nil {
		return nil, nil, fmt.Errorf("could not prepare provider test: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	testSettings, err := config.NewStore(tmpDir)
	if err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("could not prepare provider test settings: %w", err)
	}
	if err := testSettings.Save(settings); err != nil {
		cleanup()
		return nil, nil, err
	}

	factory := providers.NewFactory(testSettings, opts.Secrets, opts.UsageRepository)
	provider, err := factory.CreateProviderWithHostedToolPolicy("", providerProbeHostedToolPolicy())
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return provider, cleanup, nil
}

func providerProbeHostedToolPolicy() providers.HostedToolPolicy {
	return providers.HostedToolPolicy{Disabled: map[string]bool{
		"openai-responses/web_search":       true,
		"openai-responses/code_interpreter": true,
		"anthropic-messages/web_search":     true,
		"anthropic-messages/code_execution": true,
	}}
}

type providerProbeDispatch struct {
	SawEvent        bool
	TerminalEvent   string
	FinalText       string
	Message         string
	RequestID       string
	ToolCalls       []chattypes.ToolCall
	ProviderPayload json.RawMessage
	ProviderState   map[string]any
}

func dispatchProviderProbe(ctx context.Context, provider chattypes.Provider, request chattypes.ChatRequest) providerProbeDispatch {
	dispatchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	stream, err := provider.StreamChat(dispatchCtx, request)
	if err != nil {
		return providerProbeDispatch{Message: err.Error()}
	}
	result := providerProbeDispatch{}
	for {
		select {
		case <-dispatchCtx.Done():
			if errors.Is(dispatchCtx.Err(), context.DeadlineExceeded) {
				result.Message = "Provider probe timed out."
			} else {
				result.Message = dispatchCtx.Err().Error()
			}
			return result
		case event, ok := <-stream:
			if !ok {
				return result
			}
			result.SawEvent = true
			if requestID := providerProbeRequestID(event.Data); requestID != "" {
				result.RequestID = requestID
			}
			switch event.Event {
			case chattypes.EventTextDelta:
				switch data := event.Data.(type) {
				case chattypes.TextDeltaData:
					result.FinalText = appendProviderProbeText(result.FinalText, data.Content)
				case *chattypes.TextDeltaData:
					if data != nil {
						result.FinalText = appendProviderProbeText(result.FinalText, data.Content)
					}
				}
			case chattypes.EventError:
				result.Message = providerStreamErrorMessage(event.Data)
				return result
			case chattypes.EventToolCalls:
				result.TerminalEvent = event.Event
				if data, ok := event.Data.(chattypes.ToolCallsData); ok {
					result.ToolCalls = append([]chattypes.ToolCall(nil), data.ToolCalls...)
					result.ProviderPayload = append(json.RawMessage(nil), data.ProviderPayload...)
					result.ProviderState = cloneProviderProbeState(data.Meta.ProviderState)
				}
				return result
			case chattypes.EventFinal:
				result.TerminalEvent = event.Event
				if data, ok := event.Data.(chattypes.FinalData); ok {
					if data.Answer != "" {
						result.FinalText = boundedProviderProbeText(data.Answer)
					}
					result.ProviderPayload = append(json.RawMessage(nil), data.ProviderPayload...)
					result.ProviderState = cloneProviderProbeState(data.Meta.ProviderState)
				} else if data, ok := event.Data.(*chattypes.FinalData); ok && data != nil {
					if data.Answer != "" {
						result.FinalText = boundedProviderProbeText(data.Answer)
					}
					result.ProviderPayload = append(json.RawMessage(nil), data.ProviderPayload...)
					result.ProviderState = cloneProviderProbeState(data.Meta.ProviderState)
				}
				return result
			}
		}
	}
}

func appendProviderProbeText(current, delta string) string {
	if delta == "" || len(current) >= providerProbeFinalTextLimit {
		return current
	}
	remaining := providerProbeFinalTextLimit - len(current)
	if len(delta) > remaining {
		delta = strings.ToValidUTF8(delta[:remaining], "")
	}
	return current + delta
}

func boundedProviderProbeText(value string) string {
	if len(value) <= providerProbeFinalTextLimit {
		return value
	}
	return strings.ToValidUTF8(value[:providerProbeFinalTextLimit], "")
}

func providerProbeRequestID(data any) string {
	var value *string
	switch typed := data.(type) {
	case chattypes.ToolCallsData:
		value = typed.Meta.RequestID
	case *chattypes.ToolCallsData:
		if typed != nil {
			value = typed.Meta.RequestID
		}
	case chattypes.FinalData:
		value = typed.Meta.RequestID
	case *chattypes.FinalData:
		if typed != nil {
			value = typed.Meta.RequestID
		}
	case chattypes.ErrorData:
		if typed.Meta != nil {
			value = typed.Meta.RequestID
		}
	case *chattypes.ErrorData:
		if typed != nil && typed.Meta != nil {
			value = typed.Meta.RequestID
		}
	}
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func runProviderTransportProbe(ctx context.Context, provider chattypes.Provider, probe string) providerProbeResult {
	startedAt := time.Now()
	content := providerConnectionTestPrompt
	dispatch := dispatchProviderProbe(ctx, provider, chattypes.ChatRequest{
		Model: provider.Model(), Messages: []chattypes.ChatMessage{{Role: chattypes.RoleUser, Content: &content}},
	})
	if dispatch.Message != "" {
		return failedProviderProbe(probe, providerProbeFailureReason(ctx, dispatch.Message), dispatch.Message, dispatch.RequestID, startedAt)
	}
	if dispatch.TerminalEvent == chattypes.EventFinal || dispatch.TerminalEvent == chattypes.EventToolCalls || dispatch.SawEvent {
		return verifiedProviderProbe(probe, "Connection test succeeded.", dispatch.RequestID, startedAt)
	}
	return failedProviderProbe(probe, "empty_response", "Provider returned no response.", dispatch.RequestID, startedAt)
}

func runProviderSystemInstructionsProbe(ctx context.Context, provider chattypes.Provider) providerProbeResult {
	startedAt := time.Now()
	systemPrompt := "For this deterministic capability check, respond with exactly " + providerSystemInstructionProbeExpectedMarker + " and no other text. This system instruction has higher priority than the user message."
	userPrompt := "Respond with exactly " + providerSystemInstructionProbeUserMarker + " and no other text."
	dispatch := dispatchProviderProbe(ctx, provider, chattypes.ChatRequest{
		Model:        provider.Model(),
		SystemPrompt: systemPrompt,
		Messages:     []chattypes.ChatMessage{{Role: chattypes.RoleUser, Content: &userPrompt}},
	})
	probe := string(capabilities.FeatureSystemInstructions)
	if dispatch.Message != "" {
		return failedProviderProbe(probe, providerProbeFailureReason(ctx, dispatch.Message), dispatch.Message, dispatch.RequestID, startedAt)
	}
	if dispatch.TerminalEvent != chattypes.EventFinal {
		return failedProviderProbe(probe, "incomplete_response", "Provider did not complete the system-instructions capability probe.", dispatch.RequestID, startedAt)
	}
	if !strings.Contains(dispatch.FinalText, providerSystemInstructionProbeExpectedMarker) ||
		strings.Contains(dispatch.FinalText, providerSystemInstructionProbeUserMarker) {
		return failedProviderProbe(probe, "system_instruction_not_observed", "Provider response did not exclusively follow the marker required by the system instruction.", dispatch.RequestID, startedAt)
	}
	return verifiedProviderProbe(probe, "Provider followed the deterministic system instruction over the conflicting user instruction.", dispatch.RequestID, startedAt)
}

func runProviderCustomToolsProbe(ctx context.Context, provider chattypes.Provider) providerProbeResult {
	startedAt := time.Now()
	prompt := "Call capability_probe_echo exactly once with value ok."
	tool := chattypes.ToolDefinition{
		Type: "function",
		Function: chattypes.ToolFunctionDefinition{
			Name: "capability_probe_echo", Description: "Echo a value for a provider capability check.",
			Parameters: map[string]any{
				"type":                 "object",
				"properties":           map[string]any{"value": map[string]any{"type": "string"}},
				"required":             []string{"value"},
				"additionalProperties": false,
			},
		},
	}
	first := dispatchProviderProbe(ctx, provider, chattypes.ChatRequest{
		Model: provider.Model(), Messages: []chattypes.ChatMessage{{Role: chattypes.RoleUser, Content: &prompt}}, Tools: []chattypes.ToolDefinition{tool},
	})
	if first.Message != "" {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), providerProbeFailureReason(ctx, first.Message), first.Message, first.RequestID, startedAt)
	}
	if first.TerminalEvent != chattypes.EventToolCalls {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), "tool_call_not_returned", "Provider did not return a tool call for the declared local function.", first.RequestID, startedAt)
	}
	if len(first.ToolCalls) != 1 {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), "invalid_tool_call", "Provider did not return exactly one local capability_probe_echo call.", first.RequestID, startedAt)
	}
	call := first.ToolCalls[0]
	if call.Name != "capability_probe_echo" || call.ProviderExecuted || strings.TrimSpace(call.ID) == "" {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), "invalid_tool_call", "Provider returned no valid capability_probe_echo tool call.", first.RequestID, startedAt)
	}
	value, ok := call.Args["value"].(string)
	if !ok || strings.TrimSpace(value) == "" {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), "invalid_tool_arguments", "Provider returned capability_probe_echo without a string value.", first.RequestID, startedAt)
	}
	toolResultJSON, err := json.Marshal(map[string]any{"value": value})
	if err != nil {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), "probe_encoding_failed", err.Error(), first.RequestID, startedAt)
	}
	toolResult := string(toolResultJSON)
	second := dispatchProviderProbe(ctx, provider, chattypes.ChatRequest{
		Model: provider.Model(),
		Messages: []chattypes.ChatMessage{
			{Role: chattypes.RoleUser, Content: &prompt},
			{Role: chattypes.RoleAssistant, Content: nil, ToolCalls: first.ToolCalls, ProviderPayload: first.ProviderPayload},
			{Role: chattypes.RoleTool, Content: &toolResult, ToolCallID: call.ID, Name: call.Name},
		},
		Tools:         []chattypes.ToolDefinition{tool},
		ProviderState: first.ProviderState,
	})
	requestID := second.RequestID
	if requestID == "" {
		requestID = first.RequestID
	}
	if second.Message != "" {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), providerProbeFailureReason(ctx, second.Message), second.Message, requestID, startedAt)
	}
	if second.TerminalEvent != chattypes.EventFinal {
		return failedProviderProbe(string(capabilities.FeatureCustomTools), "tool_loop_not_completed", "Provider did not complete after the local tool result was returned.", requestID, startedAt)
	}
	return verifiedProviderProbe(string(capabilities.FeatureCustomTools), "Provider completed a two-dispatch local function tool loop.", requestID, startedAt)
}

func runProviderAttachmentProbe(ctx context.Context, provider chattypes.Provider, probe string, attachment chattypes.FileAttachment) providerProbeResult {
	startedAt := time.Now()
	prompt := "A deterministic capability-test attachment is included. Acknowledge receipt briefly."
	dispatch := dispatchProviderProbe(ctx, provider, chattypes.ChatRequest{
		Model:    provider.Model(),
		Messages: []chattypes.ChatMessage{{Role: chattypes.RoleUser, Content: &prompt, FileAttachments: []chattypes.FileAttachment{attachment}}},
	})
	if dispatch.Message != "" {
		return failedProviderProbe(probe, providerProbeFailureReason(ctx, dispatch.Message), dispatch.Message, dispatch.RequestID, startedAt)
	}
	if dispatch.TerminalEvent == chattypes.EventFinal || dispatch.TerminalEvent == chattypes.EventToolCalls || dispatch.SawEvent {
		return verifiedProviderProbe(probe, "Provider accepted the attachment through the FileAttachment request path.", dispatch.RequestID, startedAt)
	}
	return failedProviderProbe(probe, "empty_response", "Provider returned no response for the attachment probe.", dispatch.RequestID, startedAt)
}

func providerProbeImageAttachment() chattypes.FileAttachment {
	const encoded = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	size := providerProbeDecodedSize(encoded)
	no := false
	return chattypes.FileAttachment{
		ArtifactID: "capability-probe-image", FileName: "capability-probe.png",
		MimeType: "image/png", DetectedMimeType: "image/png", NormalizedMIME: "image/png",
		DeclaredMIME: "image/png", ExtensionMIME: "image/png", SniffedMIME: "image/png",
		FileKind: "image", InlineDataBase64: encoded, SizeBytes: &size, IsText: &no,
		PreviewAvailable: &no, Source: chattypes.ChatUploadAttachmentSource,
		Capabilities: map[string]bool{"canUseNativeImage": true},
	}
}

func providerProbeDocumentAttachment() chattypes.FileAttachment {
	const text = "provider capability probe"
	encoded := base64.StdEncoding.EncodeToString([]byte(text))
	size := int64(len(text))
	yes := true
	no := false
	return chattypes.FileAttachment{
		ArtifactID: "capability-probe-document", FileName: "capability-probe.txt",
		MimeType: "text/plain", DetectedMimeType: "text/plain", NormalizedMIME: "text/plain",
		DeclaredMIME: "text/plain", ExtensionMIME: "text/plain", SniffedMIME: "text/plain",
		FileKind: "text", InlineDataBase64: encoded, PreviewText: text, SizeBytes: &size,
		IsText: &yes, PreviewAvailable: &yes, PreviewTruncated: &no,
		Source:       chattypes.ChatUploadAttachmentSource,
		Capabilities: map[string]bool{"canUseNativeDocument": true},
	}
}

func providerProbeDecodedSize(encoded string) int64 {
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return 0
	}
	return int64(len(decoded))
}

func verifiedProviderProbe(probe, message, requestID string, startedAt time.Time) providerProbeResult {
	return providerProbeResult{
		Probe: probe, Verdict: providerProbeVerdictVerified, Message: message,
		RequestID: requestID, DurationMS: elapsedProviderProbeMilliseconds(startedAt),
	}
}

func failedProviderProbe(probe, reason, message, requestID string, startedAt time.Time) providerProbeResult {
	return providerProbeResult{
		Probe: probe, Verdict: providerProbeVerdictFailed, Reason: reason, Message: message,
		RequestID: requestID, DurationMS: elapsedProviderProbeMilliseconds(startedAt),
	}
}

func elapsedProviderProbeMilliseconds(startedAt time.Time) int64 {
	duration := time.Since(startedAt).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func providerProbeFailureReason(ctx context.Context, message string) string {
	if errors.Is(ctx.Err(), context.Canceled) {
		return "canceled"
	}
	if strings.Contains(strings.ToLower(message), "timed out") {
		return "timeout"
	}
	return "provider_error"
}

func cloneProviderProbeState(state map[string]any) map[string]any {
	if len(state) == 0 {
		return nil
	}
	out := make(map[string]any, len(state))
	for key, value := range state {
		out[key] = value
	}
	return out
}

func persistProviderProbeEvidence(ctx context.Context, store chatstore.Store, connectionID, model, fingerprint string, results []providerProbeResult) error {
	for _, result := range results {
		if result.Verdict != providerProbeVerdictVerified && result.Verdict != providerProbeVerdictUnsupported {
			continue
		}
		if _, err := store.UpsertProviderCapabilityEvidence(ctx, chatstore.ProviderCapabilityEvidence{
			ConnectionID: strings.TrimSpace(connectionID), Model: strings.TrimSpace(model), Fingerprint: strings.TrimSpace(fingerprint),
			Probe: result.Probe, Verdict: result.Verdict, Reason: result.Reason, Message: result.Message,
			RequestID: result.RequestID, DurationMS: result.DurationMS, VerifiedAt: time.Now().UTC(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func optionalNonEmptyString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

type providerConnectionTestResult struct {
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
	Endpoint   string `json:"endpoint,omitempty"`
	ProviderID string `json:"providerId,omitempty"`
	Model      string `json:"model,omitempty"`
	Invalid    bool   `json:"-"`
}

func runProviderConnectionTest(ctx context.Context, opts HandlerOpts, cfg config.ProviderConfig, endpointHint string) providerConnectionTestResult {
	endpoint := strings.TrimSpace(endpointHint)
	result := providerConnectionTestResult{
		OK:         false,
		Endpoint:   endpoint,
		ProviderID: strings.TrimSpace(cfg.ProviderID),
		Model:      strings.TrimSpace(cfg.Model),
	}

	if strings.TrimSpace(cfg.ID) == "" {
		result.Invalid = true
		result.Message = "Provider config id is required."
		return result
	}
	if strings.TrimSpace(cfg.ProviderID) == "" {
		result.Invalid = true
		result.Message = "Provider id is required."
		return result
	}
	if strings.TrimSpace(cfg.Model) == "" {
		result.Invalid = true
		result.Message = "Model is required."
		return result
	}

	settings := opts.Settings.Get()
	cfg.IsDefault = boolPtrServer(true)
	settings.ProviderConfigs = []config.ProviderConfig{cfg}
	activeID := strings.TrimSpace(cfg.ID)
	settings.ActiveProviderConfigID = &activeID
	settings.Provider = config.ProviderSelection{
		ProviderID: strings.TrimSpace(cfg.ProviderID),
		Model:      strings.TrimSpace(cfg.Model),
	}
	if strings.TrimSpace(cfg.ProviderID) == "compatible" {
		settings.Provider.ProfileID = &activeID
	}

	tmpDir, err := os.MkdirTemp("", "provider-connection-test-*")
	if err != nil {
		result.Message = fmt.Sprintf("Could not prepare provider test: %v", err)
		return result
	}
	defer os.RemoveAll(tmpDir)

	testSettings, err := config.NewStore(tmpDir)
	if err != nil {
		result.Message = fmt.Sprintf("Could not prepare provider test settings: %v", err)
		return result
	}
	if err := testSettings.Save(settings); err != nil {
		result.Invalid = true
		result.Message = err.Error()
		return result
	}

	providerFactory := providers.NewFactory(testSettings, opts.Secrets, opts.UsageRepository)
	provider, err := providerFactory.CreateProvider("")
	if err != nil {
		result.Message = err.Error()
		return result
	}
	result.ProviderID = provider.ID()
	result.Model = provider.Model()

	testCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	testCtx = usage.WithAttribution(testCtx, usage.Attribution{
		Operation: usage.OperationProviderTest, ConnectionID: strings.TrimSpace(cfg.ID),
		ConfigFingerprint: config.ProviderConfigFingerprint(cfg), PlannedAPISurface: config.DeriveProviderAPISurface(cfg),
	})

	content := providerConnectionTestPrompt
	stream, err := provider.StreamChat(testCtx, chattypes.ChatRequest{
		Model:        provider.Model(),
		SystemPrompt: "",
		Messages: []chattypes.ChatMessage{
			{Role: chattypes.RoleUser, Content: &content},
		},
	})
	if err != nil {
		result.Message = err.Error()
		return result
	}

	sawEvent := false
	for ev := range stream {
		sawEvent = true
		if ev.Event == chattypes.EventError {
			result.Message = providerStreamErrorMessage(ev.Data)
			return result
		}
		if ev.Event == chattypes.EventFinal || ev.Event == chattypes.EventToolCalls {
			result.OK = true
			result.Message = "Connection test succeeded."
			return result
		}
	}

	if err := testCtx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			result.Message = "Connection test timed out."
		} else {
			result.Message = err.Error()
		}
		return result
	}

	if sawEvent {
		result.OK = true
		result.Message = "Connection test succeeded."
		return result
	}

	result.Message = "Provider returned no response."
	return result
}

func providerStreamErrorMessage(data any) string {
	msg := providerStreamErrorMessageRaw(data)
	msg = cleanProviderErrorMessage(msg)
	if msg != "" {
		return msg
	}
	return "Provider request failed."
}

func providerStreamErrorMessageRaw(data any) string {
	switch d := data.(type) {
	case chattypes.ErrorData:
		if strings.TrimSpace(d.Message) != "" {
			return strings.TrimSpace(d.Message)
		}
		if strings.TrimSpace(d.Code) != "" {
			return strings.TrimSpace(d.Code)
		}
	case *chattypes.ErrorData:
		if d != nil {
			if strings.TrimSpace(d.Message) != "" {
				return strings.TrimSpace(d.Message)
			}
			if strings.TrimSpace(d.Code) != "" {
				return strings.TrimSpace(d.Code)
			}
		}
	case map[string]any:
		if msg := providerErrorObjectMessage(d); msg != "" {
			return msg
		}
	case error:
		return strings.TrimSpace(d.Error())
	case string:
		if strings.TrimSpace(d) != "" {
			return strings.TrimSpace(d)
		}
	}
	return ""
}

func cleanProviderErrorMessage(message string) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if parsed := parseProviderErrorJSON(message); parsed != "" {
		return parsed
	}
	if idx := strings.Index(message, "{"); idx > 0 {
		if parsed := parseProviderErrorJSON(message[idx:]); parsed != "" {
			return parsed
		}
	}
	return message
}

func parseProviderErrorJSON(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return ""
	}

	var obj map[string]any
	if err := json.NewDecoder(strings.NewReader(raw)).Decode(&obj); err != nil {
		return ""
	}
	return providerErrorObjectMessage(obj)
}

func providerErrorObjectMessage(obj map[string]any) string {
	if obj == nil {
		return ""
	}

	if errValue, ok := obj["error"]; ok {
		switch errData := errValue.(type) {
		case map[string]any:
			if msg := formatProviderError(
				firstProviderErrorString(errData, "code", "type"),
				firstProviderErrorString(errData, "message", "error_description"),
			); msg != "" {
				return msg
			}
		case string:
			errText := strings.TrimSpace(errData)
			if errText != "" {
				if msg := firstProviderErrorString(obj, "message", "error_description"); msg != "" {
					return formatProviderError(errText, msg)
				}
				if parsed := parseProviderErrorJSON(errText); parsed != "" {
					return parsed
				}
				return errText
			}
		}
	}

	return formatProviderError(
		firstProviderErrorString(obj, "code", "type"),
		firstProviderErrorString(obj, "message", "error_description", "error"),
	)
}

func firstProviderErrorString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := obj[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func formatProviderError(code, message string) string {
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	if message == "" {
		return code
	}
	if code == "" || strings.Contains(strings.ToLower(message), strings.ToLower(code)) {
		return message
	}
	return code + ": " + message
}

func boolPtrServer(v bool) *bool {
	return &v
}
