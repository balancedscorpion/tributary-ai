package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/runs"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func TestManagerStartRunFinalizedPlanReplayHasNoSideEffectsAfterPolicyRevocation(t *testing.T) {
	t.Parallel()

	var providerCalls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		providerCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"id\":\"req-replay\",\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	ctx := context.Background()
	manager, st := newStartRunManagerForSSE(t, t.TempDir(), srv.URL)
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	const (
		connectionID     = "default"
		model            = "test-model"
		requirementsHash = "requirements-replay"
	)
	apiType := "chat_completions"
	baseURL := srv.URL
	settings := manager.settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID: connectionID, ProviderID: "compatible", Model: model, ApiType: &apiType, BaseURL: &baseURL,
	}}
	settings.ActiveProviderConfigID = ptrStringForPlanTest(connectionID)
	if err := manager.settings.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}
	live := manager.settings.Get()
	fingerprint := strings.TrimSpace(*live.ProviderConfigs[0].ConfigFingerprint)
	connectionClass := "office"
	threadClass := "archive"
	now := time.Now().UTC()
	plan, err := st.CreateRunPlan(ctx, store.RunPlan{
		ID: "plan-manager-finalized-replay", ThreadID: thread.ID,
		Target:            store.RunTarget{ConnectionID: connectionID, Model: model},
		ConfigFingerprint: fingerprint, RequirementsHash: requirementsHash,
		ResultStatus: "choices_required", Status: store.RunPlanStatusDraft,
		Choices: []store.PlanChoice{
			{
				PlanIssue:     store.PlanIssue{IssueID: "connection-consent", Code: "fidelity_reduction"},
				Options:       []store.PlanChoiceOption{{ID: "text", PolicyClass: &connectionClass, APISurface: apiType}},
				AllowedScopes: []string{"connection"},
			},
			{
				PlanIssue:     store.PlanIssue{IssueID: "thread-consent", Code: "archive_extraction"},
				Options:       []store.PlanChoiceOption{{ID: "extract", PolicyClass: &threadClass, APISurface: apiType}},
				AllowedScopes: []string{"thread"},
			},
		},
		CapabilityReport: policyBoundCapabilityReport(t, live.ProviderConfigs[0], thread.AttachmentPolicies),
		CreatedAt:        now, ExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateRunPlan: %v", err)
	}
	request := StartRunRequest{
		ThreadID: thread.ID, Content: "run once", PlanID: plan.ID, Target: &plan.Target,
		RequirementsHash: requirementsHash, ConfigFingerprint: fingerprint,
		ChoiceSelections: []store.PlanChoiceSelection{
			{IssueID: "connection-consent", OptionID: "text", Scope: "connection"},
			{IssueID: "thread-consent", OptionID: "extract", Scope: "thread"},
		},
	}
	first, err := manager.StartRun(ctx, request)
	if err != nil {
		t.Fatalf("StartRun first: %v", err)
	}
	if _, err := waitForRunTerminalState(ctx, st, first.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	messagesBefore, err := st.ListMessages(ctx, store.ListMessagesParams{ThreadID: thread.ID, Limit: 100, IncludeHidden: true})
	if err != nil {
		t.Fatalf("ListMessages before replay: %v", err)
	}
	artifactsBefore, err := st.ListArtifacts(ctx, thread.ID, 100)
	if err != nil {
		t.Fatalf("ListArtifacts before replay: %v", err)
	}
	callsBefore := providerCalls.Load()
	if callsBefore == 0 {
		t.Fatal("initial run never reached provider")
	}

	connectionPolicies := manager.settings.Get().ProviderConfigs[0].AttachmentPolicies
	if len(connectionPolicies) != 1 {
		t.Fatalf("connection policies after first run = %#v", connectionPolicies)
	}
	if _, err := manager.settings.DeleteProviderAttachmentPolicy(connectionID, connectionPolicies[0].ID); err != nil {
		t.Fatalf("DeleteProviderAttachmentPolicy: %v", err)
	}
	threadAfter, err := st.GetThread(ctx, thread.ID)
	if err != nil || len(threadAfter.AttachmentPolicies) != 1 {
		t.Fatalf("thread policies after first run = %#v, err=%v", threadAfter.AttachmentPolicies, err)
	}
	if _, err := st.DeleteThreadAttachmentPolicy(ctx, thread.ID, threadAfter.AttachmentPolicies[0].ID); err != nil {
		t.Fatalf("DeleteThreadAttachmentPolicy: %v", err)
	}

	replayed, err := manager.StartRun(ctx, request)
	if err != nil {
		t.Fatalf("StartRun replay after policy revocation: %v", err)
	}
	if replayed.Run.ID != first.Run.ID {
		t.Fatalf("replayed run ID = %q, want original %q", replayed.Run.ID, first.Run.ID)
	}
	if providerCalls.Load() != callsBefore {
		t.Fatalf("provider calls changed on replay: before=%d after=%d", callsBefore, providerCalls.Load())
	}
	messagesAfter, err := st.ListMessages(ctx, store.ListMessagesParams{ThreadID: thread.ID, Limit: 100, IncludeHidden: true})
	if err != nil || len(messagesAfter) != len(messagesBefore) {
		t.Fatalf("messages after replay=%d err=%v, want unchanged %d", len(messagesAfter), err, len(messagesBefore))
	}
	artifactsAfter, err := st.ListArtifacts(ctx, thread.ID, 100)
	if err != nil || len(artifactsAfter) != len(artifactsBefore) {
		t.Fatalf("artifacts after replay=%d err=%v, want unchanged %d", len(artifactsAfter), err, len(artifactsBefore))
	}
	runsAfter, err := st.ListRuns(ctx, thread.ID, 10)
	if err != nil || len(runsAfter) != 1 || runsAfter[0].ID != first.Run.ID {
		t.Fatalf("runs after replay=%#v err=%v, want only original", runsAfter, err)
	}
}

func TestManagerFinalizedPlanReplayPreservesAnotherManagersLiveInitialLease(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan, err := repository.CreateRunPlan(ctx, store.RunPlan{
		ID: "plan-live-cross-manager-replay", ThreadID: thread.ID,
		Target:            store.RunTarget{ConnectionID: "connection-a", Model: "model-a"},
		ConfigFingerprint: "fingerprint-a", RequirementsHash: "requirements-a",
		ResultStatus: "clean", Status: store.RunPlanStatusDraft,
		CapabilityReport: json.RawMessage(`{"target":{"apiSurface":"chat_completions"},"features":{}}`),
		CreatedAt:        now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _, err := repository.FinalizeRunPlanAndCreateRun(ctx, store.FinalizeRunPlanRequest{
		PlanID: plan.ID, ExpectedThreadID: thread.ID, ExpectedTarget: plan.Target,
		ExpectedRequirementsHash: plan.RequirementsHash, ExpectedConfigFingerprint: plan.ConfigFingerprint,
		Run: store.CreateRunParams{
			ThreadID: thread.ID, ProviderID: "compatible", ConnectionID: plan.Target.ConnectionID,
			Model: plan.Target.Model, SystemPrompt: "system", MaxIterations: 4,
		},
		Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	live, claimed, err := repository.ClaimRunWake(
		ctx, run.ID, run.Revision, "manager-a-live-worker", now, now.Add(time.Minute),
	)
	if err != nil || !claimed || live.Status != store.RunStatusRunning || live.ResumeLeaseExpiresAt == nil {
		t.Fatalf("claim live initial execution: run=%#v claimed=%v err=%v", live, claimed, err)
	}

	managerB := &Manager{
		store: repository, longHorizon: repository,
		activeByThread: make(map[string]string), runs: make(map[string]*runState),
	}
	replayed, err := managerB.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID, Content: "same finalized request", PlanID: plan.ID, Target: &plan.Target,
		RequirementsHash: plan.RequirementsHash, ConfigFingerprint: plan.ConfigFingerprint,
	})
	if err != nil {
		t.Fatalf("cross-manager replay: %v", err)
	}
	if replayed.Run.ID != live.ID || replayed.Run.Status != store.RunStatusRunning ||
		replayed.Run.ResumeLeaseHolder != live.ResumeLeaseHolder || replayed.Run.Revision != live.Revision {
		t.Fatalf("replayed live run=%#v, want unchanged %#v", replayed.Run, live)
	}
	durable, err := repository.GetRun(ctx, live.ID)
	if err != nil || durable.Status != store.RunStatusRunning || durable.ResumeLeaseHolder != live.ResumeLeaseHolder ||
		durable.ResumeLeaseExpiresAt == nil || !durable.ResumeLeaseExpiresAt.Equal(*live.ResumeLeaseExpiresAt) || durable.Revision != live.Revision {
		t.Fatalf("durable live run was overwritten: run=%#v err=%v", durable, err)
	}
}

func TestManagerFinalizedPlanQueuedReplayUsesGraceThenParksStaleBoundary(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name       string
		runAge     time.Duration
		wantStatus string
	}{
		{name: "fresh handoff is not stolen", runAge: 0, wantStatus: store.RunStatusQueued},
		{name: "stale crash boundary is parked", runAge: finalizedQueuedRunReplayGrace + time.Second, wantStatus: store.RunStatusWaitingUser},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			repository, err := store.NewJSON(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			thread, err := repository.CreateThread(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			createdAt := now.Add(-testCase.runAge)
			plan, err := repository.CreateRunPlan(ctx, store.RunPlan{
				ID: "plan-queued-replay-" + strings.ReplaceAll(testCase.name, " ", "-"), ThreadID: thread.ID,
				Target:            store.RunTarget{ConnectionID: "connection-a", Model: "model-a"},
				ConfigFingerprint: "fingerprint-a", RequirementsHash: "requirements-a",
				ResultStatus: "clean", Status: store.RunPlanStatusDraft,
				CapabilityReport: json.RawMessage(`{"target":{"apiSurface":"chat_completions"},"features":{}}`),
				CreatedAt:        createdAt, ExpiresAt: now.Add(time.Minute),
			})
			if err != nil {
				t.Fatal(err)
			}
			run, _, err := repository.FinalizeRunPlanAndCreateRun(ctx, store.FinalizeRunPlanRequest{
				PlanID: plan.ID, ExpectedThreadID: thread.ID, ExpectedTarget: plan.Target,
				ExpectedRequirementsHash: plan.RequirementsHash, ExpectedConfigFingerprint: plan.ConfigFingerprint,
				Run: store.CreateRunParams{
					ThreadID: thread.ID, ProviderID: "compatible", ConnectionID: plan.Target.ConnectionID,
					Model: plan.Target.Model, SystemPrompt: "system", MaxIterations: 4,
				},
				Now: createdAt,
			})
			if err != nil || run.Status != store.RunStatusQueued {
				t.Fatalf("finalize queued run=%#v err=%v", run, err)
			}
			manager := &Manager{
				store: repository, longHorizon: repository,
				activeByThread: make(map[string]string), runs: make(map[string]*runState),
			}
			if testCase.runAge > 0 {
				manager.recoverLongHorizonRuns(ctx)
				parked, parkErr := repository.GetRun(ctx, run.ID)
				if parkErr != nil || parked.Status != store.RunStatusWaitingUser {
					t.Fatalf("periodic recovery did not park stale queue: run=%#v err=%v", parked, parkErr)
				}
			}
			replayed, err := manager.StartRun(ctx, StartRunRequest{
				ThreadID: thread.ID, Content: "same finalized request", PlanID: plan.ID, Target: &plan.Target,
				RequirementsHash: plan.RequirementsHash, ConfigFingerprint: plan.ConfigFingerprint,
			})
			if err != nil || replayed.Run.Status != testCase.wantStatus {
				t.Fatalf("replayed run=%#v err=%v, want %s", replayed.Run, err, testCase.wantStatus)
			}
			durable, err := repository.GetRun(ctx, run.ID)
			if err != nil || durable.Status != testCase.wantStatus {
				t.Fatalf("durable queued boundary=%#v err=%v, want %s", durable, err, testCase.wantStatus)
			}
		})
	}
}

func ptrStringForPlanTest(value string) *string { return &value }

type appendMessageFailStore struct {
	store.Store
	fail atomic.Bool
}

func (s *appendMessageFailStore) AppendMessage(ctx context.Context, threadID string, runID *string, message store.Message) (store.Message, error) {
	if s.fail.Load() {
		return store.Message{}, errors.New("injected initial message persistence failure")
	}
	return s.Store.AppendMessage(ctx, threadID, runID, message)
}

func TestManagerStartRunInitialMessageFailureTerminalizesFinalizedRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager, baseStore := newStartRunManagerForSSE(t, t.TempDir(), "http://127.0.0.1:1")
	thread, err := baseStore.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	const (
		connectionID     = "message-failure-connection"
		model            = "test-model"
		requirementsHash = "requirements-message-failure"
	)
	apiType := "chat_completions"
	baseURL := "http://127.0.0.1:1"
	settings := manager.settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID: connectionID, ProviderID: "compatible", Model: model, ApiType: &apiType, BaseURL: &baseURL,
	}}
	settings.ActiveProviderConfigID = ptrStringForPlanTest(connectionID)
	if err := manager.settings.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}
	live := manager.settings.Get()
	fingerprint := strings.TrimSpace(*live.ProviderConfigs[0].ConfigFingerprint)
	now := time.Now().UTC()
	plan, err := baseStore.CreateRunPlan(ctx, store.RunPlan{
		ID: "plan-initial-message-failure", ThreadID: thread.ID,
		Target:            store.RunTarget{ConnectionID: connectionID, Model: model},
		ConfigFingerprint: fingerprint, RequirementsHash: requirementsHash,
		ResultStatus: "clean", Status: store.RunPlanStatusDraft,
		CapabilityReport: policyBoundCapabilityReport(t, live.ProviderConfigs[0], thread.AttachmentPolicies),
		CreatedAt:        now, ExpiresAt: now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateRunPlan: %v", err)
	}
	failingStore := &appendMessageFailStore{Store: baseStore}
	failingStore.fail.Store(true)
	manager.store = failingStore
	request := StartRunRequest{
		ThreadID: thread.ID, Content: "must persist atomically enough to recover", PlanID: plan.ID, Target: &plan.Target,
		RequirementsHash: requirementsHash, ConfigFingerprint: fingerprint,
	}
	if _, err := manager.StartRun(ctx, request); err == nil || !strings.Contains(err.Error(), "injected initial message") {
		t.Fatalf("StartRun error=%v, want injected persistence failure", err)
	}
	finalized, err := baseStore.GetRunPlan(ctx, plan.ID)
	if err != nil || finalized.Status != store.RunPlanStatusFinalized || finalized.RunID == nil {
		t.Fatalf("finalized plan=%#v err=%v", finalized, err)
	}
	failed, err := baseStore.GetRun(ctx, *finalized.RunID)
	if err != nil || failed.Status != "failed" || failed.ErrorCode == nil || *failed.ErrorCode != "PERSISTENCE_ERROR" {
		t.Fatalf("run after message failure=%#v err=%v", failed, err)
	}
	messages, err := baseStore.ListMessages(ctx, store.ListMessagesParams{ThreadID: thread.ID, Limit: 10, IncludeHidden: true})
	if err != nil || len(messages) != 0 {
		t.Fatalf("messages after failed creation=%#v err=%v", messages, err)
	}

	failingStore.fail.Store(false)
	replayed, err := manager.StartRun(ctx, request)
	if err != nil || replayed.Run.ID != failed.ID || replayed.Run.Status != "failed" {
		t.Fatalf("replayed failed run=%#v err=%v", replayed.Run, err)
	}
	runsAfter, err := baseStore.ListRuns(ctx, thread.ID, 10)
	if err != nil || len(runsAfter) != 1 {
		t.Fatalf("runs after replay=%#v err=%v", runsAfter, err)
	}
}

func TestManagerHostedToolPolicyCannotExceedFinalizedPlanAndKeepsLiveRevocations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	report, err := json.Marshal(capabilities.CapabilityReport{Features: map[capabilities.FeatureKey]capabilities.FeatureResolution{
		capabilities.FeatureHostedWebSearch:     {Effective: capabilities.EffectiveOn},
		capabilities.FeatureHostedCodeExecution: {Effective: capabilities.EffectiveOff},
	}})
	if err != nil {
		t.Fatalf("marshal capability report: %v", err)
	}
	now := time.Now().UTC()
	plan, err := st.CreateRunPlan(ctx, store.RunPlan{
		ID: "plan-hosted-tool-ceiling", ThreadID: thread.ID,
		Target:            store.RunTarget{ConnectionID: "openai-primary", Model: "gpt-test"},
		ConfigFingerprint: "config-v1", RequirementsHash: "requirements-v1",
		ResultStatus: "clean", Status: store.RunPlanStatusDraft, CapabilityReport: report,
		CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateRunPlan: %v", err)
	}
	run, _, err := st.FinalizeRunPlanAndCreateRun(ctx, store.FinalizeRunPlanRequest{
		PlanID: plan.ID, ExpectedThreadID: thread.ID, ExpectedTarget: plan.Target,
		ExpectedRequirementsHash: plan.RequirementsHash, ExpectedConfigFingerprint: plan.ConfigFingerprint,
		Run: store.CreateRunParams{
			ThreadID: thread.ID, ProviderID: "openai", ConnectionID: plan.Target.ConnectionID,
			Model: plan.Target.Model, SystemPrompt: "system", MaxIterations: 1,
		},
		Now: now,
	})
	if err != nil {
		t.Fatalf("FinalizeRunPlanAndCreateRun: %v", err)
	}

	manager := &Manager{store: st}
	liveRevokedWeb := "openai-responses/web_search"
	policy, err := manager.applyHostedToolPlanCeiling(ctx, run, providers.HostedToolPolicy{
		Disabled: map[string]bool{liveRevokedWeb: true},
	})
	if err != nil {
		t.Fatalf("applyHostedToolPlanCeiling: %v", err)
	}
	if !policy.Disabled[liveRevokedWeb] {
		t.Fatal("live web-search revocation was lost")
	}
	for _, capability := range tools.ProviderHostedToolCapabilities() {
		if capability.SettingsKey == "codeExecution" && !policy.Disabled[capability.ID] {
			t.Fatalf("post-preflight grant enabled code capability %q absent from plan", capability.ID)
		}
	}
}

func TestManagerStartRunStaleLiveFingerprintDoesNotPersistConnectionPolicy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, "http://127.0.0.1:1")

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	const (
		connectionID     = "compatible-work"
		model            = "test-model"
		requirementsHash = "requirements-v1"
		planID           = "plan-stale-live-fingerprint"
	)
	apiType := "chat_completions"
	originalBaseURL := "https://first.example.test/v1"
	settings := manager.settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID:          connectionID,
		DisplayName: "Compatible work",
		ProviderID:  "compatible",
		Model:       model,
		ApiType:     &apiType,
		BaseURL:     &originalBaseURL,
	}}
	activeConnectionID := connectionID
	settings.ActiveProviderConfigID = &activeConnectionID
	if err := manager.settings.Save(settings); err != nil {
		t.Fatalf("settings.Save initial connection: %v", err)
	}

	initial := manager.settings.Get()
	if len(initial.ProviderConfigs) != 1 || initial.ProviderConfigs[0].ConfigFingerprint == nil {
		t.Fatalf("initial provider config missing fingerprint: %#v", initial.ProviderConfigs)
	}
	originalFingerprint := strings.TrimSpace(*initial.ProviderConfigs[0].ConfigFingerprint)
	if originalFingerprint == "" {
		t.Fatal("initial provider config fingerprint is empty")
	}

	policyClass := "office"
	now := time.Now().UTC()
	plan, err := st.CreateRunPlan(ctx, store.RunPlan{
		ID:                planID,
		ThreadID:          thread.ID,
		Target:            store.RunTarget{ConnectionID: connectionID, Model: model},
		ConfigFingerprint: originalFingerprint,
		RequirementsHash:  requirementsHash,
		ResultStatus:      "choices",
		Status:            store.RunPlanStatusDraft,
		Choices: []store.PlanChoice{{
			PlanIssue: store.PlanIssue{IssueID: "office-fallback", Code: "fidelity_reduction", Text: "Use extracted text."},
			Options: []store.PlanChoiceOption{{
				ID: "text", Label: "Use extracted text", Consequence: "Native layout is not preserved.",
				PolicyClass: &policyClass, APISurface: "chat_completions",
			}},
			AllowedScopes: []string{"connection"},
		}},
		CapabilityReport: policyBoundCapabilityReport(t, initial.ProviderConfigs[0], thread.AttachmentPolicies),
		CreatedAt:        now,
		ExpiresAt:        now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateRunPlan: %v", err)
	}

	changed := manager.settings.Get()
	changedBaseURL := "https://changed.example.test/v1"
	changed.ProviderConfigs[0].BaseURL = &changedBaseURL
	if err := manager.settings.Save(changed); err != nil {
		t.Fatalf("settings.Save changed connection: %v", err)
	}
	live := manager.settings.Get()
	if live.ProviderConfigs[0].ConfigFingerprint == nil || strings.TrimSpace(*live.ProviderConfigs[0].ConfigFingerprint) == originalFingerprint {
		t.Fatalf("connection fingerprint did not change: %#v", live.ProviderConfigs[0].ConfigFingerprint)
	}

	selection := store.PlanChoiceSelection{IssueID: "office-fallback", OptionID: "text", Scope: "connection"}
	_, err = manager.StartRun(ctx, StartRunRequest{
		ThreadID:          thread.ID,
		Content:           "summarize the attachment",
		PlanID:            plan.ID,
		Target:            &plan.Target,
		RequirementsHash:  requirementsHash,
		ConfigFingerprint: originalFingerprint,
		ChoiceSelections:  []store.PlanChoiceSelection{selection},
	})
	if !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("StartRun error = %v, want store.ErrPlanStale", err)
	}

	after := manager.settings.Get()
	if got := after.ProviderConfigs[0].AttachmentPolicies; len(got) != 0 {
		t.Fatalf("stale plan persisted connection policy: %#v", got)
	}
	persistedPlan, err := st.GetRunPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("GetRunPlan: %v", err)
	}
	if persistedPlan.Status != store.RunPlanStatusDraft || persistedPlan.RunID != nil || len(persistedPlan.Selections) != 0 {
		t.Fatalf("stale plan was finalized: %#v", persistedPlan)
	}
	runs, err := st.ListRuns(ctx, thread.ID, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("stale plan created runs: %#v", runs)
	}
}

func TestManagerStartRunUsesExactTargetModelForLiveFingerprint(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, "http://127.0.0.1:1")

	validThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread valid target: %v", err)
	}
	staleThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread stale target: %v", err)
	}

	const (
		connectionID     = "compatible-multi-model"
		savedModel       = "default-model"
		selectedModel    = "discovered-model"
		requirementsHash = "requirements-exact-target"
	)
	apiType := "chat_completions"
	baseURL := "http://127.0.0.1:1"
	settings := manager.settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID:          connectionID,
		DisplayName: "Compatible multi-model",
		ProviderID:  "compatible",
		Model:       savedModel,
		ApiType:     &apiType,
		BaseURL:     &baseURL,
	}}
	activeConnectionID := connectionID
	settings.ActiveProviderConfigID = &activeConnectionID
	if err := manager.settings.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	live := manager.settings.Get()
	if got := live.ProviderConfigs[0].Model; got != savedModel {
		t.Fatalf("saved default model = %q, want %q", got, savedModel)
	}
	exactTargetConfig := live.ProviderConfigs[0]
	exactTargetConfig.Model = selectedModel
	exactTargetFingerprint := config.ProviderConfigFingerprint(exactTargetConfig)
	if exactTargetFingerprint == strings.TrimSpace(*live.ProviderConfigs[0].ConfigFingerprint) {
		t.Fatal("test setup did not produce a model-specific target fingerprint")
	}

	createPlan := func(threadID, planID string) store.RunPlan {
		t.Helper()
		now := time.Now().UTC()
		plan, createErr := st.CreateRunPlan(ctx, store.RunPlan{
			ID:                planID,
			ThreadID:          threadID,
			Target:            store.RunTarget{ConnectionID: connectionID, Model: selectedModel},
			ConfigFingerprint: exactTargetFingerprint,
			RequirementsHash:  requirementsHash,
			ResultStatus:      "ready",
			Status:            store.RunPlanStatusDraft,
			CapabilityReport:  policyBoundCapabilityReport(t, exactTargetConfig, nil),
			CreatedAt:         now,
			ExpiresAt:         now.Add(5 * time.Minute),
		})
		if createErr != nil {
			t.Fatalf("CreateRunPlan(%s): %v", planID, createErr)
		}
		return plan
	}
	validPlan := createPlan(validThread.ID, "plan-exact-target-valid")
	stalePlan := createPlan(staleThread.ID, "plan-exact-target-stale")

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID:          validThread.ID,
		Content:           "use the selected discovered model",
		PlanID:            validPlan.ID,
		Target:            &validPlan.Target,
		RequirementsHash:  requirementsHash,
		ConfigFingerprint: exactTargetFingerprint,
	})
	if err != nil {
		t.Fatalf("StartRun non-default target: %v", err)
	}
	if result.Run.Model != selectedModel || result.Run.ConnectionID != connectionID || result.Run.PlanID != validPlan.ID {
		t.Fatalf("finalized run target = %#v, want %s/%s plan %s", result.Run, connectionID, selectedModel, validPlan.ID)
	}
	finalized, err := st.GetRunPlan(ctx, validPlan.ID)
	if err != nil {
		t.Fatalf("GetRunPlan valid: %v", err)
	}
	if finalized.Status != store.RunPlanStatusFinalized || finalized.RunID == nil || *finalized.RunID != result.Run.ID {
		t.Fatalf("non-default target plan was not finalized: %#v", finalized)
	}
	if _, err := waitForRunTerminalState(ctx, st, result.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}

	changed := manager.settings.Get()
	changedBaseURL := "https://routing-changed.example.test/v1"
	changed.ProviderConfigs[0].BaseURL = &changedBaseURL
	if err := manager.settings.Save(changed); err != nil {
		t.Fatalf("settings.Save routing change: %v", err)
	}
	if changedFingerprint, ok := providerFingerprintForRunTarget(manager.settings.Get(), connectionID, selectedModel); !ok || changedFingerprint == exactTargetFingerprint {
		t.Fatalf("routing change fingerprint = %q/%v, want a changed exact-target fingerprint", changedFingerprint, ok)
	}

	_, err = manager.StartRun(ctx, StartRunRequest{
		ThreadID:          staleThread.ID,
		Content:           "this plan must be re-preflighted",
		PlanID:            stalePlan.ID,
		Target:            &stalePlan.Target,
		RequirementsHash:  requirementsHash,
		ConfigFingerprint: exactTargetFingerprint,
	})
	if !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("StartRun after routing change error = %v, want store.ErrPlanStale", err)
	}
	persistedStalePlan, err := st.GetRunPlan(ctx, stalePlan.ID)
	if err != nil {
		t.Fatalf("GetRunPlan stale: %v", err)
	}
	if persistedStalePlan.Status != store.RunPlanStatusDraft || persistedStalePlan.RunID != nil {
		t.Fatalf("routing-stale plan was finalized: %#v", persistedStalePlan)
	}
}

func TestManagerStartRunFinalizationFailureCompensatesOnlyNewConnectionPolicies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, "http://127.0.0.1:1")

	planThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread plan owner: %v", err)
	}
	submissionThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread submission: %v", err)
	}

	const (
		connectionID     = "compatible-compensation"
		model            = "test-model"
		requirementsHash = "requirements-compensation"
		planID           = "plan-compensation"
	)
	apiType := "chat_completions"
	baseURL := "https://compensation.example.test/v1"
	settings := manager.settings.Get()
	settings.ProviderConfigs = []config.ProviderConfig{{
		ID:          connectionID,
		DisplayName: "Compatible compensation",
		ProviderID:  "compatible",
		Model:       model,
		ApiType:     &apiType,
		BaseURL:     &baseURL,
	}}
	activeConnectionID := connectionID
	settings.ActiveProviderConfigID = &activeConnectionID
	if err := manager.settings.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	preexisting, err := manager.settings.UpsertProviderAttachmentPolicy(connectionID, config.ProviderAttachmentPolicy{
		Class: "office", Action: "text", APISurface: "chat_completions",
	})
	if err != nil {
		t.Fatalf("UpsertProviderAttachmentPolicy pre-existing: %v", err)
	}
	live := manager.settings.Get()
	fingerprint := strings.TrimSpace(*live.ProviderConfigs[0].ConfigFingerprint)

	officeClass := "office"
	archiveClass := "archive"
	now := time.Now().UTC()
	plan, err := st.CreateRunPlan(ctx, store.RunPlan{
		ID:                planID,
		ThreadID:          planThread.ID,
		Target:            store.RunTarget{ConnectionID: connectionID, Model: model},
		ConfigFingerprint: fingerprint,
		RequirementsHash:  requirementsHash,
		ResultStatus:      "choices",
		Status:            store.RunPlanStatusDraft,
		Choices: []store.PlanChoice{
			{
				PlanIssue: store.PlanIssue{IssueID: "office-fallback", Code: "fidelity_reduction", Text: "Use extracted text."},
				Options: []store.PlanChoiceOption{{
					ID: "text", Label: "Use extracted text", Consequence: "Native layout is not preserved.",
					PolicyClass: &officeClass, APISurface: "chat_completions",
				}},
				AllowedScopes: []string{"connection"},
			},
			{
				PlanIssue: store.PlanIssue{IssueID: "archive-fallback", Code: "archive_extraction", Text: "Extract the archive."},
				Options: []store.PlanChoiceOption{{
					ID: "extract", Label: "Extract archive", Consequence: "Archive contents are expanded.",
					PolicyClass: &archiveClass, APISurface: "chat_completions",
				}},
				AllowedScopes: []string{"connection"},
			},
		},
		CapabilityReport: policyBoundCapabilityReport(t, live.ProviderConfigs[0], planThread.AttachmentPolicies),
		CreatedAt:        now,
		ExpiresAt:        now.Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateRunPlan: %v", err)
	}

	selections := []store.PlanChoiceSelection{
		{IssueID: "office-fallback", OptionID: "text", Scope: "connection"},
		{IssueID: "archive-fallback", OptionID: "extract", Scope: "connection"},
	}
	// Submission validation intentionally runs before the store's atomic
	// thread-identity check. Using another real thread forces finalization to
	// fail after policy upserts without an artificial store test double.
	_, err = manager.StartRun(ctx, StartRunRequest{
		ThreadID:          submissionThread.ID,
		Content:           "summarize the attachment",
		PlanID:            plan.ID,
		Target:            &plan.Target,
		RequirementsHash:  requirementsHash,
		ConfigFingerprint: fingerprint,
		ChoiceSelections:  selections,
	})
	if !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("StartRun error = %v, want store.ErrPlanStale", err)
	}

	policies := manager.settings.Get().ProviderConfigs[0].AttachmentPolicies
	if len(policies) != 1 || policies[0] != preexisting {
		t.Fatalf("compensated policies = %#v, want only pre-existing %#v", policies, preexisting)
	}
	persistedPlan, err := st.GetRunPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("GetRunPlan: %v", err)
	}
	if persistedPlan.Status != store.RunPlanStatusDraft || persistedPlan.RunID != nil {
		t.Fatalf("failed finalization changed plan: %#v", persistedPlan)
	}
	runs, err := st.ListRuns(ctx, submissionThread.ID, 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Fatalf("failed finalization created runs: %#v", runs)
	}
}

func policyBoundCapabilityReport(t *testing.T, cfg config.ProviderConfig, threadPolicies []store.AttachmentPolicy) []byte {
	t.Helper()
	report, err := runs.BindPlanPolicyFingerprints(
		[]byte(`{"schemaVersion":1}`),
		runs.ProviderConnectionPolicyFingerprint(cfg),
		store.AttachmentPoliciesFingerprint(threadPolicies),
	)
	if err != nil {
		t.Fatalf("BindPlanPolicyFingerprint: %v", err)
	}
	return report
}

func TestManagerPersistToolResultStampsPlanIDOnMainAndExternalFileArtifacts(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-test", nil, "test", 1)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	run.PlanID = "  plan-with-artifacts  "

	managedRel := filepath.ToSlash(filepath.Join("arxiv", "papers", "test", "paper.pdf"))
	managedFull := filepath.Join(cfgDir, filepath.FromSlash(managedRel))
	if err := os.MkdirAll(filepath.Dir(managedFull), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	const fileContent = "%PDF-1.4\n%%EOF\n"
	if err := os.WriteFile(managedFull, []byte(fileContent), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{ID: "call-plan-artifacts", Name: "arxiv_download_paper", Args: map[string]any{"arxiv_id": "test"}}
	out := tools.JSONOK(map[string]any{
		"paper": map[string]any{"arxiv_id": "test", "pdf_url": "https://arxiv.org/pdf/test"},
		"pdf_file": map[string]any{
			"local_path": managedRel,
			"file_name":  "paper.pdf",
			"mime_type":  "application/pdf",
			"size_bytes": len(fileContent),
		},
	})

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil {
		t.Fatal("persisted tool message has nil content")
	}
	envelope, ok := types.ParseToolResultEnvelope(*msg.Content)
	if !ok || strings.TrimSpace(envelope.ArtifactID) == "" {
		t.Fatalf("tool result envelope missing main artifact: %q", *msg.Content)
	}
	mainArtifact, err := st.GetArtifact(ctx, envelope.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact main: %v", err)
	}
	if got := mainArtifact.Metadata["plan_id"]; got != "plan-with-artifacts" {
		t.Fatalf("main artifact plan_id = %#v, want %q", got, "plan-with-artifacts")
	}

	if len(msg.FileAttachments) != 1 || strings.TrimSpace(msg.FileAttachments[0].ArtifactID) == "" {
		t.Fatalf("external file attachments = %#v, want one persisted artifact", msg.FileAttachments)
	}
	externalArtifact, err := st.GetArtifact(ctx, msg.FileAttachments[0].ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact external file: %v", err)
	}
	if got := externalArtifact.Metadata["plan_id"]; got != "plan-with-artifacts" {
		t.Fatalf("external file artifact plan_id = %#v, want %q", got, "plan-with-artifacts")
	}
}
