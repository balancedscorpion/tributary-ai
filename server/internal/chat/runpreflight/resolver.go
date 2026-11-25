package runpreflight

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/providers/capabilityruntime"
	"ai-analyst-local/internal/chat/providers/discovery"
	"ai-analyst-local/internal/chat/runs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/permissions"
)

// Resolver adapts saved connection state and the shared capability authority to
// the provider-neutral runs.Resolver contract.
type Resolver struct {
	Settings     *config.Store
	Store        store.Store
	Capabilities *capabilityruntime.Service
}

func NewResolver(settings *config.Store, chatStore store.Store, permissionService *permissions.Service) *Resolver {
	return &Resolver{
		Settings:     settings,
		Store:        chatStore,
		Capabilities: capabilityruntime.NewService(chatStore, permissionService),
	}
}

func (r *Resolver) ResolveRunRequirements(ctx context.Context, req runs.ResolveRequest) (runs.Resolution, error) {
	if r == nil || r.Settings == nil || r.Store == nil || r.Capabilities == nil {
		return runs.Resolution{}, fmt.Errorf("run capability resolver is unavailable")
	}
	cfg, err := r.connectionConfig(req.Target)
	if err != nil {
		return runs.Resolution{}, err
	}
	connectionID := strings.TrimSpace(req.Target.ConnectionID)
	report, err := r.Capabilities.Resolve(ctx, capabilityruntime.ResolveInput{
		Config: cfg, ConnectionID: &connectionID, ThreadID: strings.TrimSpace(req.ThreadID),
	})
	if err != nil {
		return runs.Resolution{}, err
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return runs.Resolution{}, err
	}
	thread, err := r.Store.GetThread(ctx, strings.TrimSpace(req.ThreadID))
	if err != nil {
		return runs.Resolution{}, err
	}

	resolution := runs.Resolution{CapabilityReport: reportJSON, StaleEvidence: reportHasStaleEvidence(report)}
	resolution.Blockers, resolution.Notices = appendFeatureIssues(resolution.Blockers, resolution.Notices, report, req.Requirements)
	resolution.Blockers, resolution.Choices, resolution.Notices = r.appendAttachmentIssues(
		resolution.Blockers, resolution.Choices, resolution.Notices, cfg, thread, report, req.Requirements,
	)
	resolution.Blockers, resolution.Notices = appendModelLifecycleIssues(
		resolution.Blockers, resolution.Notices, cfg.ProviderID, cfg.Model,
	)
	resolution.Notices = r.appendContinuationNotice(ctx, resolution.Notices, thread, req.Target, report)
	// Bind the plan to the exact immutable objects that produced the decisions
	// above. Handler-side rereads could otherwise create a mixed-time snapshot.
	resolution.ConfigFingerprint = config.ProviderConfigFingerprint(cfg)
	resolution.ConnectionPolicyFingerprint = runs.ProviderConnectionPolicyFingerprint(cfg)
	resolution.ThreadPolicyFingerprint = store.AttachmentPoliciesFingerprint(thread.AttachmentPolicies)
	return resolution, nil
}

func (r *Resolver) connectionConfig(target store.RunTarget) (config.ProviderConfig, error) {
	connectionID := strings.TrimSpace(target.ConnectionID)
	model := strings.TrimSpace(target.Model)
	settings := r.Settings.Get()
	for _, cfg := range settings.ProviderConfigs {
		if strings.TrimSpace(cfg.ID) != connectionID {
			continue
		}
		cfg.Model = model
		return cfg, nil
	}
	return config.ProviderConfig{}, store.ErrNotFound
}

func appendFeatureIssues(
	blockers []store.PlanIssue,
	notices []store.PlanIssue,
	report capabilities.CapabilityReport,
	requirements runs.Requirements,
) ([]store.PlanIssue, []store.PlanIssue) {
	required := []string{
		string(capabilities.FeatureTextStreaming),
		string(capabilities.FeatureSystemInstructions),
		string(capabilities.FeatureCustomTools),
	}
	required = append(required, requirements.Features...)
	seen := map[string]struct{}{}
	for _, raw := range required {
		featureKey := strings.TrimSpace(raw)
		if featureKey == "" || featureKey == string(capabilities.FeatureImages) || featureKey == string(capabilities.FeatureDocuments) {
			continue
		}
		if _, ok := seen[featureKey]; ok {
			continue
		}
		seen[featureKey] = struct{}{}
		feature, ok := report.Features[capabilities.FeatureKey(featureKey)]
		if !ok || feature.State == capabilities.StateUnknown {
			blockers = append(blockers, planIssue(
				"capability_unverified",
				fmt.Sprintf("%s is unverified for this provider connection and model.", featureLabel(featureKey, feature)),
				featureKey, "", "recheck",
			))
			continue
		}
		if feature.State == capabilities.StateUnsupported {
			blockers = append(blockers, planIssue(
				"feature_unavailable", unavailableText(featureKey, feature), featureKey, "", "switch_model",
			))
			continue
		}
		switch feature.Effective {
		case capabilities.EffectiveBlocked:
			blockers = append(blockers, planIssue(
				"hosted_tool_blocked",
				fmt.Sprintf("%s is configured but blocked by the current permission policy.", featureLabel(featureKey, feature)),
				featureKey, "", "open_permissions",
			))
		case capabilities.EffectiveOff:
			blockers = append(blockers, planIssue(
				"feature_disabled",
				fmt.Sprintf("%s is required for this run but is Off for the selected connection.", featureLabel(featureKey, feature)),
				featureKey, "", "open_permissions",
			))
		case capabilities.EffectiveUnavailable:
			blockers = append(blockers, planIssue(
				"feature_unavailable", unavailableText(featureKey, feature), featureKey, "", "switch_model",
			))
		}
	}
	return blockers, notices
}

func (r *Resolver) appendAttachmentIssues(
	blockers []store.PlanIssue,
	choices []store.PlanChoice,
	notices []store.PlanIssue,
	cfg config.ProviderConfig,
	thread store.Thread,
	report capabilities.CapabilityReport,
	requirements runs.Requirements,
) ([]store.PlanIssue, []store.PlanChoice, []store.PlanIssue) {
	surface := config.DeriveProviderAPISurface(cfg)
	documents := make([]runs.AttachmentRequirement, 0)
	nativeBudgetItems := make([]nativeAttachmentBudgetItem, 0, len(requirements.Attachments))
	for _, attachment := range requirements.Attachments {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.MIME)), "image/") {
			var native bool
			blockers, choices, notices, native = appendInputIssue(
				blockers, choices, notices, cfg, thread, report, attachment,
				capabilities.FeatureImages, "image", surface,
			)
			if native {
				nativeBudgetItems = append(nativeBudgetItems, nativeAttachmentBudgetItem{
					attachment: attachment,
					feature:    capabilities.FeatureImages,
				})
			}
			continue
		}
		documents = append(documents, attachment)
	}

	if len(documents) > 0 && strings.EqualFold(strings.TrimSpace(cfg.ProviderID), "bedrock") && surface == "invoke_stream" {
		policyClass := "bedrock_documents_converse"
		action := "bedrock_converse"
		if hasAttachmentPolicy(cfg.AttachmentPolicies, thread.AttachmentPolicies, policyClass, action, surface) {
			notices = append(notices, planIssue(
				"surface_switch",
				"Documents will use the Bedrock Converse API for this run. The saved attachment policy applies; Invoke-only compaction is skipped.",
				string(capabilities.FeatureDocuments), "", "",
			))
		} else if cfg.CompactionEnabled != nil && *cfg.CompactionEnabled {
			choices = append(choices, planChoice(
				"surface_switch",
				"These documents require the Bedrock Converse API. Native InvokeModel compaction is skipped for this run.",
				string(capabilities.FeatureDocuments), "", "switch_model",
				store.PlanChoiceOption{
					ID: "bedrock_converse", Label: "Use Converse for these documents",
					Consequence: "Documents keep native fidelity; Invoke-only compaction is skipped for this run.",
					PolicyClass: stringPtr(policyClass), APISurface: surface,
				},
				[]string{"run", "thread", "connection"},
			))
		} else {
			notices = append(notices, planIssue(
				"surface_switch",
				"Documents will use the Bedrock Converse API for this run.",
				string(capabilities.FeatureDocuments), "", "",
			))
		}
	}

	documentMaxCount := -1
	if feature, ok := report.Features[capabilities.FeatureDocuments]; ok && feature.Limits != nil && feature.Limits.MaxCount != nil && *feature.Limits.MaxCount >= 0 {
		documentMaxCount = *feature.Limits.MaxCount
	}
	nativeDocuments := make([]nativeAttachmentBudgetItem, 0, len(documents))
	for _, attachment := range documents {
		var native bool
		blockers, choices, notices, native = appendInputIssue(
			blockers, choices, notices, cfg, thread, report, attachment,
			capabilities.FeatureDocuments, "document", surface,
		)
		if native {
			nativeDocuments = append(nativeDocuments, nativeAttachmentBudgetItem{
				attachment: attachment,
				feature:    capabilities.FeatureDocuments,
			})
		}
	}
	if documentMaxCount >= 0 && len(nativeDocuments) > documentMaxCount {
		for _, item := range nativeDocuments[documentMaxCount:] {
			attachment := item.attachment
			class := "document_count_to_text"
			action := "send_extracted_text"
			if hasAttachmentPolicy(cfg.AttachmentPolicies, thread.AttachmentPolicies, class, action, surface) {
				notices = append(notices, reducedFidelityNotice(attachment, "The native document count limit is exceeded; the saved extracted-text policy applies."))
				continue
			}
			choices = append(choices, reducedFidelityChoice(attachment, class, surface, "The native document count limit is exceeded."))
		}
		nativeDocuments = nativeDocuments[:documentMaxCount]
	}
	nativeBudgetItems = append(nativeBudgetItems, nativeDocuments...)
	blockers = appendAggregateRequestBudgetIssue(blockers, nativeBudgetItems, report)
	return blockers, choices, notices
}

type nativeAttachmentBudgetItem struct {
	attachment runs.AttachmentRequirement
	feature    capabilities.FeatureKey
}

func appendInputIssue(
	blockers []store.PlanIssue,
	choices []store.PlanChoice,
	notices []store.PlanIssue,
	cfg config.ProviderConfig,
	thread store.Thread,
	report capabilities.CapabilityReport,
	attachment runs.AttachmentRequirement,
	key capabilities.FeatureKey,
	kind string,
	surface string,
) ([]store.PlanIssue, []store.PlanChoice, []store.PlanIssue, bool) {
	feature, ok := report.Features[key]
	if !ok || feature.State == capabilities.StateUnknown {
		blockers = append(blockers, planIssue(
			"capability_unverified",
			fmt.Sprintf("%s support is unverified for %s.", displayKind(kind), attachmentLabel(attachment)),
			string(key), attachment.ID, "recheck",
		))
		return blockers, choices, notices, false
	}
	if feature.State == capabilities.StateUnsupported || feature.Effective == capabilities.EffectiveUnavailable {
		blockers = append(blockers, planIssue(
			"feature_unavailable",
			fmt.Sprintf("This provider target cannot send %s natively: %s.", kind, attachmentLabel(attachment)),
			string(key), attachment.ID, "switch_model",
		))
		return blockers, choices, notices, false
	}
	if feature.Effective == capabilities.EffectiveBlocked || feature.Effective == capabilities.EffectiveOff {
		blockers = append(blockers, planIssue(
			"feature_disabled",
			fmt.Sprintf("%s input is not enabled for %s.", displayKind(kind), attachmentLabel(attachment)),
			string(key), attachment.ID, "open_permissions",
		))
		return blockers, choices, notices, false
	}

	if key == capabilities.FeatureDocuments && requiresAnthropicHostedExecution(cfg, attachment.MIME) {
		hosted := report.Features[capabilities.FeatureHostedCodeExecution]
		if hosted.State == capabilities.StateUnsupported || hosted.State == capabilities.StateUnknown || hosted.Effective != capabilities.EffectiveOn {
			blockers = append(blockers, planIssue(
				"hosted_tool_blocked",
				fmt.Sprintf("%s requires Anthropic hosted code execution, but that capability is not both enabled and permitted.", attachmentLabel(attachment)),
				string(capabilities.FeatureHostedCodeExecution), attachment.ID, "open_permissions",
			))
			return blockers, choices, notices, false
		}
	}
	if key == capabilities.FeatureDocuments && hasConditionCode(feature, "native_with_reduced_fallback", "openai_shape_required") {
		class := "native_rejection_to_text"
		action := "allow_extracted_fallback"
		if hasAttachmentPolicy(cfg.AttachmentPolicies, thread.AttachmentPolicies, class, action, surface) {
			notices = append(notices, reducedFidelityNotice(
				attachment,
				"If native file input is rejected, the saved extracted-text fallback policy applies.",
			))
		} else {
			choices = append(choices, planChoice(
				"fidelity_reduction",
				fmt.Sprintf("If native file input is rejected for %s, continuing requires an extracted-text fallback.", attachmentLabel(attachment)),
				string(capabilities.FeatureDocuments), attachment.ID, "remove_attachment",
				store.PlanChoiceOption{
					ID: action, Label: "Allow extracted-text fallback",
					Consequence: "Native input is tried first. If rejected, layout, images, and precise citations may be lost.",
					PolicyClass: stringPtr(class), APISurface: surface,
				},
				[]string{"run", "thread", "connection"},
			))
		}
	}

	if feature.Limits == nil {
		return blockers, choices, notices, true
	}
	if len(feature.Limits.MIMETypes) > 0 && !containsMIME(feature.Limits.MIMETypes, attachment.MIME) {
		if key == capabilities.FeatureImages {
			blockers = append(blockers, planIssue(
				"feature_unavailable",
				fmt.Sprintf("The image type %s is not available on this target.", fallbackMIME(attachment.MIME)),
				string(key), attachment.ID, "remove_attachment",
			))
			return blockers, choices, notices, false
		}
		class := fidelityClass(attachment, false)
		if hasAttachmentPolicy(cfg.AttachmentPolicies, thread.AttachmentPolicies, class, "send_extracted_text", surface) {
			notices = append(notices, reducedFidelityNotice(attachment, "This format is not native; the saved extracted-text policy applies."))
		} else {
			choices = append(choices, reducedFidelityChoice(attachment, class, surface, "This format is not supported as a native document."))
		}
		// The selected or saved fallback is decisive for this attachment. It
		// must not also produce an independent native-size decision, and the
		// source bytes will not contribute to a native request budget.
		return blockers, choices, notices, false
	}
	if feature.Limits.MaxBytes != nil && attachment.SizeBytes > *feature.Limits.MaxBytes {
		if key == capabilities.FeatureImages {
			blockers = append(blockers, planIssue(
				"attachment_too_large",
				fmt.Sprintf("%s exceeds this target's native image size limit.", attachmentLabel(attachment)),
				string(key), attachment.ID, "remove_attachment",
			))
			return blockers, choices, notices, false
		}
		class := fidelityClass(attachment, true)
		if hasAttachmentPolicy(cfg.AttachmentPolicies, thread.AttachmentPolicies, class, "send_extracted_text", surface) {
			notices = append(notices, reducedFidelityNotice(attachment, "The file exceeds the native size limit; the saved extracted-text policy applies."))
		} else {
			choices = append(choices, reducedFidelityChoice(attachment, class, surface, "The file exceeds the native document size limit."))
		}
		return blockers, choices, notices, false
	}
	return blockers, choices, notices, true
}

// appendAggregateRequestBudgetIssue applies the smallest declared request-wide
// budget to all attachments that preflight has determined will still be sent
// natively. Attachment payloads are base64-encoded in provider JSON, so the
// encoded size is the minimum conservative estimate of their request cost.
func appendAggregateRequestBudgetIssue(
	blockers []store.PlanIssue,
	items []nativeAttachmentBudgetItem,
	report capabilities.CapabilityReport,
) []store.PlanIssue {
	var budget int64
	for _, item := range items {
		feature, ok := report.Features[item.feature]
		if !ok || feature.Limits == nil || feature.Limits.RequestBudgetBytes == nil {
			continue
		}
		candidate := *feature.Limits.RequestBudgetBytes
		if candidate > 0 && (budget == 0 || candidate < budget) {
			budget = candidate
		}
	}
	if budget <= 0 || len(items) == 0 {
		return blockers
	}

	ordered := append([]nativeAttachmentBudgetItem(nil), items...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i].attachment, ordered[j].attachment
		if left.ID != right.ID {
			return left.ID < right.ID
		}
		if left.MIME != right.MIME {
			return left.MIME < right.MIME
		}
		if left.SizeBytes != right.SizeBytes {
			return left.SizeBytes < right.SizeBytes
		}
		return ordered[i].feature < ordered[j].feature
	})

	var encodedTotal int64
	for _, item := range ordered {
		encodedBytes, exceeds := base64EncodedSizeWithinBudget(item.attachment.SizeBytes, budget-encodedTotal)
		if exceeds {
			return append(blockers, planIssue(
				"request_attachment_budget_exceeded",
				fmt.Sprintf(
					"The encoded native attachment payload exceeds this target's %d-byte request budget at %s. Remove or reduce one or more attachments, then preflight again.",
					budget, attachmentLabel(item.attachment),
				),
				string(item.feature), item.attachment.ID, "remove_attachment",
			))
		}
		encodedTotal += encodedBytes
	}
	return blockers
}

// base64EncodedSizeWithinBudget returns the standard padded base64 size if it
// fits in the remaining request budget. Comparing groups before multiplication
// keeps the calculation safe for every int64 input.
func base64EncodedSizeWithinBudget(rawBytes, remaining int64) (int64, bool) {
	if rawBytes <= 0 {
		return 0, false
	}
	if remaining <= 0 {
		return 0, true
	}
	groups := rawBytes / 3
	if rawBytes%3 != 0 {
		groups++
	}
	if groups > remaining/4 {
		return 0, true
	}
	encoded := groups * 4
	return encoded, false
}

func (r *Resolver) appendContinuationNotice(
	ctx context.Context,
	notices []store.PlanIssue,
	thread store.Thread,
	target store.RunTarget,
	report capabilities.CapabilityReport,
) []store.PlanIssue {
	runsForThread, err := r.Store.ListRuns(ctx, thread.ID, 1)
	if err != nil || len(runsForThread) == 0 {
		return notices
	}
	previous := runsForThread[0]
	if strings.TrimSpace(previous.ConnectionID) == strings.TrimSpace(target.ConnectionID) && strings.TrimSpace(previous.Model) == strings.TrimSpace(target.Model) {
		return notices
	}
	if targetCanRestoreNativeContinuation(report) && r.hasExactContinuationState(ctx, thread.ID, target, report.Target.ProviderFamily) {
		return append(notices, planIssue(
			"continuation_restore",
			"The provider target changed, but provider-native continuation state saved for this exact connection and model will be restored. Portable conversation content is also preserved.",
			string(capabilities.FeatureSessionContinuation), "", "",
		))
	}
	return append(notices, planIssue(
		"continuation_loss",
		"The provider target changed. Provider-native continuation state from the previous target is not used for this run; portable conversation content is preserved.",
		string(capabilities.FeatureSessionContinuation), "", "",
	))
}

func targetCanRestoreNativeContinuation(report capabilities.CapabilityReport) bool {
	feature, ok := report.Features[capabilities.FeatureSessionContinuation]
	if !ok || (feature.State != capabilities.StateSupported && feature.State != capabilities.StateConditional) {
		return false
	}
	if feature.Effective != capabilities.EffectiveOn || feature.Mode == nil {
		return false
	}
	return *feature.Mode == capabilities.ModeNative
}

func (r *Resolver) hasExactContinuationState(ctx context.Context, threadID string, target store.RunTarget, providerID string) bool {
	messages, err := r.Store.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: strings.TrimSpace(threadID),
		Limit:    500,
		OrderBy:  "seq_desc",
	})
	if err == nil {
		for _, message := range messages {
			if message.Role == "assistant" && providerPayloadHasExactContinuationState(message.ProviderPayload, target, providerID) {
				return true
			}
		}
	}

	// Active checkpoints can retain native state beyond the ordinary history
	// window, and are part of the same execution path used by optionc.
	checkpoint, checkpointErr := r.Store.GetActiveContextCheckpoint(ctx, strings.TrimSpace(threadID))
	if checkpointErr != nil || checkpoint == nil {
		return false
	}
	for idx := len(checkpoint.ReplacementHistory) - 1; idx >= 0; idx-- {
		message := checkpoint.ReplacementHistory[idx]
		if message.Role == "assistant" && providerPayloadHasExactContinuationState(message.ProviderPayload, target, providerID) {
			return true
		}
	}
	return false
}

func providerPayloadHasExactContinuationState(raw json.RawMessage, target store.RunTarget, providerID string) bool {
	if len(raw) == 0 {
		return false
	}
	var payload struct {
		ProviderID    string         `json:"provider_id"`
		State         map[string]any `json:"state"`
		TargetBinding struct {
			ConnectionID string `json:"connection_id"`
			Model        string `json:"model"`
		} `json:"target_binding"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || len(payload.State) == 0 {
		return false
	}
	if expected := strings.TrimSpace(providerID); expected != "" && strings.TrimSpace(payload.ProviderID) != "" && strings.TrimSpace(payload.ProviderID) != expected {
		return false
	}
	return strings.TrimSpace(payload.TargetBinding.ConnectionID) != "" &&
		strings.TrimSpace(payload.TargetBinding.Model) != "" &&
		strings.TrimSpace(payload.TargetBinding.ConnectionID) == strings.TrimSpace(target.ConnectionID) &&
		strings.TrimSpace(payload.TargetBinding.Model) == strings.TrimSpace(target.Model)
}

func appendModelLifecycleIssues(blockers, notices []store.PlanIssue, providerID, model string) ([]store.PlanIssue, []store.PlanIssue) {
	metadata := discovery.ResolveLifecycle(providerID, model)
	replacement := ""
	if metadata.Replacement != nil {
		replacement = strings.TrimSpace(*metadata.Replacement)
	}
	switch metadata.Lifecycle {
	case discovery.LifecycleShutdown:
		text := fmt.Sprintf("Model %s is shut down and cannot start a new run.", strings.TrimSpace(model))
		if replacement != "" {
			text += " Suggested replacement: " + replacement + "."
		}
		blockers = append(blockers, planIssue("model_shutdown", text, "", "", "switch_model"))
	case discovery.LifecycleDeprecated:
		text := fmt.Sprintf("Model %s is deprecated.", strings.TrimSpace(model))
		if replacement != "" {
			text += " Consider switching to " + replacement + "."
		}
		notices = append(notices, planIssue("model_deprecated", text, "", "", "switch_model"))
	}
	return blockers, notices
}

func reportHasStaleEvidence(report capabilities.CapabilityReport) bool {
	for _, feature := range report.Features {
		for _, evidence := range feature.Evidence {
			if evidence.Stale {
				return true
			}
		}
	}
	return false
}

func planIssue(code, text, feature, attachmentID, fixKind string) store.PlanIssue {
	issue := store.PlanIssue{Code: code, Text: text}
	if strings.TrimSpace(feature) != "" {
		issue.Feature = stringPtr(strings.TrimSpace(feature))
	}
	if strings.TrimSpace(attachmentID) != "" {
		issue.AttachmentID = stringPtr(strings.TrimSpace(attachmentID))
	}
	if strings.TrimSpace(fixKind) != "" {
		issue.Fix, _ = json.Marshal(map[string]string{"kind": strings.TrimSpace(fixKind)})
	}
	return issue
}

func planChoice(
	code, text, feature, attachmentID, fixKind string,
	option store.PlanChoiceOption,
	allowedScopes []string,
) store.PlanChoice {
	return store.PlanChoice{
		PlanIssue:     planIssue(code, text, feature, attachmentID, fixKind),
		Options:       []store.PlanChoiceOption{option},
		AllowedScopes: append([]string(nil), allowedScopes...),
	}
}

func reducedFidelityChoice(attachment runs.AttachmentRequirement, class, surface, reason string) store.PlanChoice {
	return planChoice(
		"fidelity_reduction",
		fmt.Sprintf("%s Sending extracted text instead reduces layout, visual, and citation fidelity for %s.", reason, attachmentLabel(attachment)),
		string(capabilities.FeatureDocuments), attachment.ID, "remove_attachment",
		store.PlanChoiceOption{
			ID: "send_extracted_text", Label: "Send extracted text",
			Consequence: "The model receives readable text, but layout, images, and precise citations may be lost.",
			PolicyClass: stringPtr(class), APISurface: surface,
		},
		[]string{"run", "thread", "connection"},
	)
}

func reducedFidelityNotice(attachment runs.AttachmentRequirement, reason string) store.PlanIssue {
	return planIssue(
		"fidelity_reduction", reason+" Reduced fidelity remains recorded for "+attachmentLabel(attachment)+".",
		string(capabilities.FeatureDocuments), attachment.ID, "",
	)
}

func hasAttachmentPolicy(
	connection []config.ProviderAttachmentPolicy,
	thread []store.AttachmentPolicy,
	class, action, surface string,
) bool {
	for _, policy := range connection {
		if policyMatches(policy.Class, policy.Action, policy.APISurface, class, action, surface) {
			return true
		}
	}
	for _, policy := range thread {
		if policyMatches(policy.Class, policy.Action, policy.APISurface, class, action, surface) {
			return true
		}
	}
	return false
}

func policyMatches(policyClass, policyAction, policySurface, class, action, surface string) bool {
	return strings.EqualFold(strings.TrimSpace(policyClass), strings.TrimSpace(class)) &&
		strings.EqualFold(strings.TrimSpace(policyAction), strings.TrimSpace(action)) &&
		strings.EqualFold(strings.TrimSpace(policySurface), strings.TrimSpace(surface))
}

func containsMIME(values []string, wanted string) bool {
	wanted = strings.ToLower(strings.TrimSpace(wanted))
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func hasConditionCode(feature capabilities.FeatureResolution, codes ...string) bool {
	for _, condition := range feature.Conditions {
		for _, code := range codes {
			if strings.EqualFold(strings.TrimSpace(condition.Code), strings.TrimSpace(code)) {
				return true
			}
		}
	}
	return false
}

func requiresAnthropicHostedExecution(cfg config.ProviderConfig, mime string) bool {
	if !strings.EqualFold(strings.TrimSpace(cfg.ProviderID), "anthropic") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint":
		return true
	default:
		return false
	}
}

func fidelityClass(attachment runs.AttachmentRequirement, oversize bool) string {
	if oversize {
		if strings.EqualFold(strings.TrimSpace(attachment.MIME), "application/pdf") {
			return "oversize_pdf_to_text"
		}
		return "oversize_document_to_text"
	}
	if requiresOfficeFallback(attachment.MIME) {
		return "office_docs_to_text"
	}
	return "unsupported_documents_to_text"
}

func requiresOfficeFallback(mime string) bool {
	lower := strings.ToLower(strings.TrimSpace(mime))
	return strings.Contains(lower, "officedocument") || strings.Contains(lower, "msword") ||
		strings.Contains(lower, "ms-excel") || strings.Contains(lower, "ms-powerpoint")
}

func featureLabel(key string, feature capabilities.FeatureResolution) string {
	if label := strings.TrimSpace(feature.Meta.Label); label != "" {
		return label
	}
	return strings.ReplaceAll(key, "_", " ")
}

func displayKind(kind string) string {
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "Attachment"
	}
	return strings.ToUpper(kind[:1]) + kind[1:]
}

func unavailableText(key string, feature capabilities.FeatureResolution) string {
	if len(feature.Reasons) > 0 && strings.TrimSpace(feature.Reasons[0].Text) != "" {
		return featureLabel(key, feature) + " is not available: " + strings.TrimSpace(feature.Reasons[0].Text)
	}
	return featureLabel(key, feature) + " is not available for this provider target."
}

func attachmentLabel(attachment runs.AttachmentRequirement) string {
	if id := strings.TrimSpace(attachment.ID); id != "" {
		return id
	}
	return fallbackMIME(attachment.MIME)
}

func fallbackMIME(mime string) string {
	if value := strings.TrimSpace(mime); value != "" {
		return value
	}
	return "this attachment"
}

func stringPtr(value string) *string {
	return &value
}

// StableIssueCodes returns the issue codes emitted by this resolver. Keeping
// this list sorted makes contract snapshots and UI fixture generation stable.
func StableIssueCodes() []string {
	codes := []string{
		"attachment_too_large", "capability_unverified", "continuation_loss", "continuation_restore", "feature_disabled",
		"feature_unavailable", "fidelity_reduction", "hosted_tool_blocked", "model_deprecated",
		"model_shutdown", "request_attachment_budget_exceeded", "surface_switch",
	}
	sort.Strings(codes)
	return codes
}
