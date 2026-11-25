package runpreflight

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/runs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/config"
)

func TestUnsupportedDocumentFormatProducesOneDecisiveFallback(t *testing.T) {
	t.Parallel()
	maxBytes := int64(100)
	budget := int64(150)
	report := capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureDocuments: {
			State:     capabilities.StateConditional,
			Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{
				MIMETypes:          []string{"application/pdf"},
				MaxBytes:           &maxBytes,
				RequestBudgetBytes: &budget,
			},
		},
	}}
	attachment := runs.AttachmentRequirement{ID: "archive", MIME: "application/zip", SizeBytes: 200}

	blockers, choices, notices := (&Resolver{}).appendAttachmentIssues(
		nil, nil, nil,
		config.ProviderConfig{ProviderID: "compatible"}, store.Thread{}, report,
		runs.Requirements{Attachments: []runs.AttachmentRequirement{attachment}},
	)

	if len(blockers) != 0 {
		t.Fatalf("unsupported document blockers = %#v, want none", blockers)
	}
	if len(notices) != 0 {
		t.Fatalf("unsupported document notices = %#v, want none", notices)
	}
	if len(choices) != 1 {
		t.Fatalf("unsupported and oversize document choices = %#v, want one decisive choice", choices)
	}
	if choices[0].Code != "fidelity_reduction" || choices[0].AttachmentID == nil || *choices[0].AttachmentID != attachment.ID {
		t.Fatalf("fallback choice = %#v", choices[0])
	}
	if len(choices[0].Options) != 1 || choices[0].Options[0].PolicyClass == nil || *choices[0].Options[0].PolicyClass != "unsupported_documents_to_text" {
		t.Fatalf("fallback option = %#v, want unsupported format policy", choices[0].Options)
	}
}

func TestUnsupportedOversizeSixthDocumentDoesNotAlsoGetCountFallback(t *testing.T) {
	t.Parallel()
	maxBytes := int64(100)
	maxCount := 5
	budget := int64(10_000)
	report := capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureDocuments: {
			State:     capabilities.StateConditional,
			Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{
				MIMETypes:          []string{"application/pdf"},
				MaxBytes:           &maxBytes,
				MaxCount:           &maxCount,
				RequestBudgetBytes: &budget,
			},
		},
	}}
	attachments := []runs.AttachmentRequirement{
		{ID: "doc-1", MIME: "application/pdf", SizeBytes: 50},
		{ID: "doc-2", MIME: "application/pdf", SizeBytes: 50},
		{ID: "doc-3", MIME: "application/pdf", SizeBytes: 50},
		{ID: "doc-4", MIME: "application/pdf", SizeBytes: 50},
		{ID: "doc-5", MIME: "application/pdf", SizeBytes: 50},
		{ID: "doc-6", MIME: "application/zip", SizeBytes: 200},
	}

	blockers, choices, notices := (&Resolver{}).appendAttachmentIssues(
		nil, nil, nil,
		config.ProviderConfig{ProviderID: "compatible"}, store.Thread{}, report,
		runs.Requirements{Attachments: attachments},
	)

	if len(blockers) != 0 || len(notices) != 0 {
		t.Fatalf("six-document blockers/notices = %#v / %#v", blockers, notices)
	}
	if len(choices) != 1 {
		t.Fatalf("sixth unsupported/oversize document choices = %#v, want one", choices)
	}
	choice := choices[0]
	if choice.AttachmentID == nil || *choice.AttachmentID != "doc-6" || len(choice.Options) != 1 || choice.Options[0].PolicyClass == nil {
		t.Fatalf("sixth document fallback = %#v", choice)
	}
	if got := *choice.Options[0].PolicyClass; got != "unsupported_documents_to_text" {
		t.Fatalf("sixth document policy class = %q, want unsupported_documents_to_text", got)
	}
}

func TestDocumentCountFallbackAppliesToSixthNativeDocument(t *testing.T) {
	t.Parallel()
	maxBytes := int64(100)
	maxCount := 5
	budget := int64(10_000)
	report := capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureDocuments: {
			State:     capabilities.StateSupported,
			Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{
				MIMETypes:          []string{"application/pdf"},
				MaxBytes:           &maxBytes,
				MaxCount:           &maxCount,
				RequestBudgetBytes: &budget,
			},
		},
	}}
	attachments := make([]runs.AttachmentRequirement, 0, 6)
	for index := 1; index <= 6; index++ {
		attachments = append(attachments, runs.AttachmentRequirement{
			ID: fmt.Sprintf("doc-%d", index), MIME: "application/pdf", SizeBytes: 50,
		})
	}

	blockers, choices, notices := (&Resolver{}).appendAttachmentIssues(
		nil, nil, nil,
		config.ProviderConfig{ProviderID: "compatible"}, store.Thread{}, report,
		runs.Requirements{Attachments: attachments},
	)

	if len(blockers) != 0 || len(notices) != 0 || len(choices) != 1 {
		t.Fatalf("six native documents resolution = blockers %#v choices %#v notices %#v", blockers, choices, notices)
	}
	choice := choices[0]
	if choice.AttachmentID == nil || *choice.AttachmentID != "doc-6" || len(choice.Options) != 1 || choice.Options[0].PolicyClass == nil || *choice.Options[0].PolicyClass != "document_count_to_text" {
		t.Fatalf("sixth native document fallback = %#v", choice)
	}
}

func TestAggregateRequestBudgetUsesEncodedBytesAcrossAttachmentKinds(t *testing.T) {
	t.Parallel()
	budget := int64(20 << 20)
	report := capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureImages: {
			State: capabilities.StateSupported, Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{RequestBudgetBytes: &budget},
		},
		capabilities.FeatureDocuments: {
			State: capabilities.StateSupported, Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{RequestBudgetBytes: &budget},
		},
	}}
	// The raw total is only 16 MiB, but the padded base64 representation is
	// about 21.3 MiB and therefore cannot fit in the declared request budget.
	attachments := []runs.AttachmentRequirement{
		{ID: "b-document", MIME: "application/pdf", SizeBytes: 8 << 20},
		{ID: "a-image", MIME: "image/jpeg", SizeBytes: 8 << 20},
	}

	blockers, choices, notices := (&Resolver{}).appendAttachmentIssues(
		nil, nil, nil,
		config.ProviderConfig{ProviderID: "gemini"}, store.Thread{}, report,
		runs.Requirements{Attachments: attachments},
	)

	if len(choices) != 0 || len(notices) != 0 {
		t.Fatalf("aggregate budget choices/notices = %#v / %#v", choices, notices)
	}
	if len(blockers) != 1 {
		t.Fatalf("aggregate budget blockers = %#v, want one", blockers)
	}
	issue := blockers[0]
	if issue.Code != "request_attachment_budget_exceeded" {
		t.Fatalf("aggregate issue code = %q", issue.Code)
	}
	if issue.AttachmentID == nil || *issue.AttachmentID != "b-document" {
		t.Fatalf("aggregate issue attachment = %#v, want deterministic crossing attachment", issue.AttachmentID)
	}
	var fix map[string]string
	if err := json.Unmarshal(issue.Fix, &fix); err != nil || fix["kind"] != "remove_attachment" {
		t.Fatalf("aggregate issue fix = %s, err=%v", issue.Fix, err)
	}
	if !containsString(StableIssueCodes(), "request_attachment_budget_exceeded") {
		t.Fatalf("aggregate issue code missing from stable contract: %#v", StableIssueCodes())
	}
}

func TestAggregateRequestBudgetCrossingIsInputOrderIndependent(t *testing.T) {
	t.Parallel()
	budget := int64(20 << 20)
	report := capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureDocuments: {
			State: capabilities.StateSupported, Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{RequestBudgetBytes: &budget},
		},
	}}
	left := runs.AttachmentRequirement{ID: "a", MIME: "application/pdf", SizeBytes: 8 << 20}
	right := runs.AttachmentRequirement{ID: "b", MIME: "application/pdf", SizeBytes: 8 << 20}

	resolve := func(attachments []runs.AttachmentRequirement) *string {
		blockers, _, _ := (&Resolver{}).appendAttachmentIssues(
			nil, nil, nil,
			config.ProviderConfig{ProviderID: "gemini"}, store.Thread{}, report,
			runs.Requirements{Attachments: attachments},
		)
		if len(blockers) != 1 || blockers[0].Code != "request_attachment_budget_exceeded" {
			t.Fatalf("aggregate blockers = %#v", blockers)
		}
		return blockers[0].AttachmentID
	}

	forward := resolve([]runs.AttachmentRequirement{left, right})
	reverse := resolve([]runs.AttachmentRequirement{right, left})
	if forward == nil || reverse == nil || *forward != "b" || *reverse != *forward {
		t.Fatalf("crossing attachments forward=%v reverse=%v, want b", forward, reverse)
	}
}

func TestAggregateRequestBudgetAllowsEncodedPayloadBelowLimit(t *testing.T) {
	t.Parallel()
	budget := int64(20 << 20)
	report := capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureDocuments: {
			State: capabilities.StateSupported, Effective: capabilities.EffectiveOn,
			Limits: &capabilities.CapabilityLimits{RequestBudgetBytes: &budget},
		},
	}}
	blockers, _, _ := (&Resolver{}).appendAttachmentIssues(
		nil, nil, nil,
		config.ProviderConfig{ProviderID: "gemini"}, store.Thread{}, report,
		runs.Requirements{Attachments: []runs.AttachmentRequirement{
			{ID: "a", MIME: "application/pdf", SizeBytes: 7 << 20},
			{ID: "b", MIME: "application/pdf", SizeBytes: 7 << 20},
		}},
	)
	if issueWithCode(blockers, "request_attachment_budget_exceeded") != nil {
		t.Fatalf("payload below encoded budget was blocked: %#v", blockers)
	}
}

func TestBedrockDocumentSurfaceSwitchChoiceAndSavedPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chatStore, settingsStore, thread := resolverFixture(t)
	apiType := "messages"
	compaction := true
	cfg := config.ProviderConfig{
		ID: "bedrock-main", DisplayName: "Bedrock", ProviderID: "bedrock",
		Model: "anthropic.claude-sonnet-4-20250514-v1:0", ApiType: &apiType,
		CompactionEnabled: &compaction,
	}
	saveResolverConfigs(t, settingsStore, cfg)
	resolver := NewResolver(settingsStore, chatStore, nil)
	req := runs.ResolveRequest{
		ThreadID: thread.ID,
		Target:   store.RunTarget{ConnectionID: cfg.ID, Model: cfg.Model},
		Requirements: runs.Requirements{Attachments: []runs.AttachmentRequirement{{
			ID: "file-pdf", MIME: "application/pdf", SizeBytes: 1024,
		}}},
	}
	resolution, err := resolver.ResolveRunRequirements(ctx, req)
	if err != nil {
		t.Fatalf("ResolveRunRequirements: %v", err)
	}
	choice := choiceWithCode(resolution.Choices, "surface_switch")
	if choice == nil {
		t.Fatalf("surface switch choice missing: %#v", resolution)
	}
	if len(choice.Options) != 1 || choice.Options[0].ID != "bedrock_converse" || choice.Options[0].PolicyClass == nil {
		t.Fatalf("surface switch option = %#v", choice.Options)
	}
	if len(resolution.Blockers) != 0 {
		t.Fatalf("unexpected blockers: %#v", resolution.Blockers)
	}

	cfg.AttachmentPolicies = []config.ProviderAttachmentPolicy{{
		ID: "policy-one", Class: "bedrock_documents_converse", Action: "bedrock_converse",
		APISurface: "invoke_stream", CreatedAt: 1,
	}}
	saveResolverConfigs(t, settingsStore, cfg)
	covered, err := resolver.ResolveRunRequirements(ctx, req)
	if err != nil {
		t.Fatalf("ResolveRunRequirements with policy: %v", err)
	}
	if choiceWithCode(covered.Choices, "surface_switch") != nil {
		t.Fatalf("saved policy did not remove choice: %#v", covered.Choices)
	}
	if issueWithCode(covered.Notices, "surface_switch") == nil {
		t.Fatalf("saved policy did not retain surface notice: %#v", covered.Notices)
	}
}

func TestAnthropicOfficeDocumentCannotBroadenHostedExecutionPermission(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chatStore, settingsStore, thread := resolverFixture(t)
	apiType := "messages"
	off := false
	cfg := config.ProviderConfig{
		ID: "anthropic-main", DisplayName: "Anthropic", ProviderID: "anthropic",
		Model: "claude-sonnet-4-20250514", ApiType: &apiType,
		CodeExecution: &config.ProviderCodeExecutionConfig{Enabled: &off},
	}
	saveResolverConfigs(t, settingsStore, cfg)
	resolver := NewResolver(settingsStore, chatStore, nil)
	resolution, err := resolver.ResolveRunRequirements(ctx, runs.ResolveRequest{
		ThreadID: thread.ID,
		Target:   store.RunTarget{ConnectionID: cfg.ID, Model: cfg.Model},
		Requirements: runs.Requirements{Attachments: []runs.AttachmentRequirement{{
			ID: "file-docx", MIME: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", SizeBytes: 1024,
		}}},
	})
	if err != nil {
		t.Fatalf("ResolveRunRequirements: %v", err)
	}
	blocker := issueWithCode(resolution.Blockers, "hosted_tool_blocked")
	if blocker == nil || blocker.Feature == nil || *blocker.Feature != string(capabilities.FeatureHostedCodeExecution) {
		t.Fatalf("hosted execution blocker missing: %#v", resolution.Blockers)
	}
}

func TestShutdownModelIsBlocked(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chatStore, settingsStore, thread := resolverFixture(t)
	apiType := "generate_content"
	cfg := config.ProviderConfig{
		ID: "gemini-main", DisplayName: "Gemini", ProviderID: "gemini",
		Model: "gemini-2.0-flash", ApiType: &apiType,
	}
	saveResolverConfigs(t, settingsStore, cfg)
	resolver := NewResolver(settingsStore, chatStore, nil)
	resolution, err := resolver.ResolveRunRequirements(ctx, runs.ResolveRequest{
		ThreadID: thread.ID,
		Target:   store.RunTarget{ConnectionID: cfg.ID, Model: cfg.Model},
	})
	if err != nil {
		t.Fatalf("ResolveRunRequirements: %v", err)
	}
	if issueWithCode(resolution.Blockers, "model_shutdown") == nil {
		t.Fatalf("shutdown model blocker missing: %#v", resolution.Blockers)
	}
}

func TestContinuationRestoreRequiresExactBoundStateAndNativeCapability(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chatStore, settingsStore, thread := resolverFixture(t)
	apiType := "responses"
	cfg := config.ProviderConfig{
		ID: "openai-work", DisplayName: "OpenAI Work", ProviderID: "openai",
		Model: "gpt-5", ApiType: &apiType,
	}
	saveResolverConfigs(t, settingsStore, cfg)

	payload := continuationProviderPayload(t, "openai", "resp-work-new", cfg.ID, cfg.Model)
	if _, err := chatStore.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role: "assistant", Content: stringPtr("Portable answer from the earlier OpenAI target."), ProviderPayload: payload,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	// The newest run used another target, so preflight must distinguish a
	// restorable return from ordinary continuation loss.
	if _, err := chatStore.CreateRun(ctx, thread.ID, "openai", "gpt-other", nil, "system", 4); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	resolution, err := NewResolver(settingsStore, chatStore, nil).ResolveRunRequirements(ctx, runs.ResolveRequest{
		ThreadID: thread.ID,
		Target:   store.RunTarget{ConnectionID: cfg.ID, Model: cfg.Model},
	})
	if err != nil {
		t.Fatalf("ResolveRunRequirements: %v", err)
	}
	if issueWithCode(resolution.Notices, "continuation_restore") == nil {
		t.Fatalf("exact restoration notice missing: %#v", resolution.Notices)
	}
	if issueWithCode(resolution.Notices, "continuation_loss") != nil {
		t.Fatalf("loss notice emitted despite exact native state: %#v", resolution.Notices)
	}
	if !containsString(StableIssueCodes(), "continuation_restore") {
		t.Fatalf("continuation_restore missing from stable issue codes: %#v", StableIssueCodes())
	}
}

func TestContinuationLossForUnboundOrMismatchedState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chatStore, settingsStore, thread := resolverFixture(t)
	apiType := "responses"
	cfg := config.ProviderConfig{
		ID: "openai-work", DisplayName: "OpenAI Work", ProviderID: "openai",
		Model: "gpt-5", ApiType: &apiType,
	}
	saveResolverConfigs(t, settingsStore, cfg)

	for _, payload := range []json.RawMessage{
		continuationProviderPayload(t, "openai", "resp-legacy", "", ""),
		continuationProviderPayload(t, "openai", "resp-other", "openai-personal", cfg.Model),
	} {
		if _, err := chatStore.AppendMessage(ctx, thread.ID, nil, store.Message{Role: "assistant", ProviderPayload: payload}); err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
	}
	if _, err := chatStore.CreateRun(ctx, thread.ID, "openai", "gpt-other", nil, "system", 4); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	resolution, err := NewResolver(settingsStore, chatStore, nil).ResolveRunRequirements(ctx, runs.ResolveRequest{
		ThreadID: thread.ID,
		Target:   store.RunTarget{ConnectionID: cfg.ID, Model: cfg.Model},
	})
	if err != nil {
		t.Fatalf("ResolveRunRequirements: %v", err)
	}
	if issueWithCode(resolution.Notices, "continuation_restore") != nil {
		t.Fatalf("unbound/mismatched state advertised as restorable: %#v", resolution.Notices)
	}
	if issueWithCode(resolution.Notices, "continuation_loss") == nil {
		t.Fatalf("continuation loss notice missing: %#v", resolution.Notices)
	}
}

func TestContinuationLossWhenTargetCapabilityCannotUseNativeState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	chatStore, settingsStore, thread := resolverFixture(t)
	apiType := "generate_content"
	cfg := config.ProviderConfig{
		ID: "gemini-main", DisplayName: "Gemini", ProviderID: "gemini",
		Model: "gemini-3.5-flash", ApiType: &apiType,
	}
	saveResolverConfigs(t, settingsStore, cfg)
	if _, err := chatStore.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role: "assistant", ProviderPayload: continuationProviderPayload(t, "gemini", "synthetic-state", cfg.ID, cfg.Model),
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if _, err := chatStore.CreateRun(ctx, thread.ID, "gemini", "gemini-other", nil, "system", 4); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	resolution, err := NewResolver(settingsStore, chatStore, nil).ResolveRunRequirements(ctx, runs.ResolveRequest{
		ThreadID: thread.ID,
		Target:   store.RunTarget{ConnectionID: cfg.ID, Model: cfg.Model},
	})
	if err != nil {
		t.Fatalf("ResolveRunRequirements: %v", err)
	}
	if issueWithCode(resolution.Notices, "continuation_restore") != nil || issueWithCode(resolution.Notices, "continuation_loss") == nil {
		t.Fatalf("unusable native continuation capability notices = %#v", resolution.Notices)
	}
}

func continuationProviderPayload(t *testing.T, providerID, stateID, connectionID, model string) json.RawMessage {
	t.Helper()
	payload := map[string]any{
		"format":      "opaque_test_v1",
		"provider_id": providerID,
		"state":       map[string]any{"previous_response_id": stateID},
	}
	if connectionID != "" || model != "" {
		payload["target_binding"] = map[string]any{"connection_id": connectionID, "model": model}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func resolverFixture(t *testing.T) (*store.JSONStore, *config.Store, store.Thread) {
	t.Helper()
	chatStore, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	thread, err := chatStore.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	return chatStore, settingsStore, thread
}

func saveResolverConfigs(t *testing.T, settingsStore *config.Store, configs ...config.ProviderConfig) {
	t.Helper()
	settings := settingsStore.Get()
	settings.ProviderConfigs = configs
	if len(configs) > 0 {
		activeID := configs[0].ID
		settings.ActiveProviderConfigID = &activeID
		settings.Provider.ProviderID = configs[0].ProviderID
		settings.Provider.Model = configs[0].Model
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("Save settings: %v", err)
	}
}

func choiceWithCode(choices []store.PlanChoice, code string) *store.PlanChoice {
	for i := range choices {
		if choices[i].Code == code {
			return &choices[i]
		}
	}
	return nil
}

func issueWithCode(issues []store.PlanIssue, code string) *store.PlanIssue {
	for i := range issues {
		if issues[i].Code == code {
			return &issues[i]
		}
	}
	return nil
}
