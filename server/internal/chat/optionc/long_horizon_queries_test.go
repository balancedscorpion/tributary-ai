package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/engine"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/jsondb"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

type ambiguousSuspendRepository struct {
	store.LongHorizonRepository
	calls int
	ids   []string
}

type failOnceWaitingUserRepository struct {
	store.LongHorizonRepository
	remaining int
}

type failOnceGetRunStore struct {
	store.Store
	remaining int
}

func (failing *failOnceGetRunStore) GetRun(ctx context.Context, runID string) (store.Run, error) {
	if failing.remaining > 0 {
		failing.remaining--
		return store.Run{}, errors.New("transient get run failure")
	}
	return failing.Store.GetRun(ctx, runID)
}

func (repository *failOnceWaitingUserRepository) TransitionRunCAS(ctx context.Context, mutation store.RunStateMutation) (store.Run, bool, error) {
	if mutation.Status == store.RunStatusWaitingUser && repository.remaining > 0 {
		repository.remaining--
		return store.Run{}, false, errors.New("transient waiting_user transition failure")
	}
	return repository.LongHorizonRepository.TransitionRunCAS(ctx, mutation)
}

func (repository *ambiguousSuspendRepository) SuspendRun(ctx context.Context, mutation store.SuspendRunMutation) (store.ContinuationCheckpoint, store.Run, bool, error) {
	repository.calls++
	repository.ids = append(repository.ids, mutation.Checkpoint.ID)
	checkpoint, run, changed, err := repository.LongHorizonRepository.SuspendRun(ctx, mutation)
	if repository.calls == 1 && err == nil {
		return store.ContinuationCheckpoint{}, store.Run{}, false, errors.New("simulated response loss after commit")
	}
	return checkpoint, run, changed, err
}

func TestSuspendRunAtCheckpointRetriesAmbiguousCommitWithStableIdentity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	run, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision, ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusRunning,
	})
	if err != nil || !changed {
		t.Fatalf("move run to running changed=%v err=%v", changed, err)
	}
	ambiguous := &ambiguousSuspendRepository{LongHorizonRepository: repository}
	manager := &Manager{store: repository, longHorizon: ambiguous}
	content := "query submitted"
	point := engine.LoopControlPoint{
		Iteration:     2,
		History:       []types.ChatMessage{{Role: types.RoleAssistant, Content: &content}},
		ProviderState: map[string]any{"turn": 2},
	}
	snapshot := continuationExecutionSnapshot{
		Dispatch:            continuationDispatchIdentity{ConnectionID: run.ConnectionID, ProviderID: run.ProviderID, Model: run.Model},
		NextDispatchOrdinal: 2, SystemPromptBase: "system", SystemPrompt: "system",
		ToolDefinitions: []types.ToolDefinition{{Type: "function", Function: types.ToolFunctionDefinition{Name: "query_status", Parameters: map[string]any{"type": "object"}}}},
		QueryRowLimit:   100, MaxContextInputTokens: 1000, MaxContextHardTokens: 2000,
		TokenCharsPerToken: 4, EngineInputCap: 900, HardInputCap: 1900, RunInputCap: 900,
		EvidenceBudget: 4, RuntimeReference: time.Now().UTC(),
	}
	checkpoint, waiting, err := manager.suspendRunAtCheckpoint(ctx, run, point, []string{"job-1"}, snapshot, 0, 3, "")
	if err != nil {
		t.Fatalf("suspendRunAtCheckpoint: %v", err)
	}
	if ambiguous.calls != 2 || len(ambiguous.ids) != 2 || ambiguous.ids[0] == "" || ambiguous.ids[0] != ambiguous.ids[1] {
		t.Fatalf("ambiguous retry calls=%d ids=%#v", ambiguous.calls, ambiguous.ids)
	}
	if checkpoint.ID != ambiguous.ids[0] || waiting.Status != store.RunStatusWaitingExternal {
		t.Fatalf("checkpoint=%#v waiting=%#v", checkpoint, waiting)
	}
	latest, err := repository.LatestContinuationCheckpoint(ctx, run.ID)
	if err != nil || latest == nil || latest.ID != checkpoint.ID || latest.Sequence != checkpoint.Sequence {
		t.Fatalf("latest=%#v err=%v", latest, err)
	}
	restoredSnapshot, err := unmarshalContinuationExecutionSnapshot(latest.ExecutionSnapshotJSON, run)
	if err != nil || validateContinuationCheckpointDigests(restoredSnapshot, latest) != nil {
		t.Fatalf("persisted snapshot/digests are invalid: snapshot=%#v err=%v", restoredSnapshot, err)
	}
	corruptedCheckpoint := *latest
	corruptedCheckpoint.HistoryJSON = json.RawMessage(`[{"role":"assistant","content":"tampered"}]`)
	if err := validateContinuationCheckpointDigests(restoredSnapshot, &corruptedCheckpoint); err == nil {
		t.Fatal("tampered continuation history passed its integrity check")
	}
}

func TestTransitionRunToWaitingUserRetriesTransientStoreFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := repository.CreateThread(ctx, nil)
	run, _ := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	run, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision,
		ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusRunning,
	})
	if err != nil || !changed {
		t.Fatalf("move running changed=%v err=%v", changed, err)
	}
	now := time.Now().UTC()
	job := jobs.JobRecord{
		ID: "qj-waiting-user-retry", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-waiting-user-retry"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, job, 0); err != nil {
		t.Fatal(err)
	}
	obligationID, _ := store.QueryObligationIDForRoot(run.ID, job.ID)
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: job.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
	}); err != nil {
		t.Fatal(err)
	}
	failing := &failOnceWaitingUserRepository{LongHorizonRepository: repository, remaining: 1}
	manager := &Manager{store: repository, longHorizon: failing}
	if err := manager.transitionRunToWaitingUser(ctx, run.ID, 0, 3, ""); err != nil {
		t.Fatalf("transitionRunToWaitingUser: %v", err)
	}
	parked, err := repository.GetRun(ctx, run.ID)
	if err != nil || parked.Status != store.RunStatusWaitingUser || failing.remaining != 0 {
		t.Fatalf("query run was not durably parked after retry: run=%#v remaining=%d err=%v", parked, failing.remaining, err)
	}
}

func TestInitialExecutionVerificationFailureParksWithoutContinuationIncrement(t *testing.T) {
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
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	running, claimed, err := repository.ClaimRunWake(
		ctx, run.ID, run.Revision, "initial-verification-token", now, now.Add(time.Minute),
	)
	if err != nil || !claimed || running.Status != store.RunStatusRunning {
		t.Fatalf("claim initial execution=%v run=%#v err=%v", claimed, running, err)
	}
	manager := &Manager{store: repository, longHorizon: repository}
	if err := manager.parkClaimedInitialRun(ctx, run.ID, "initial-verification-token"); err != nil {
		t.Fatalf("park initial execution: %v", err)
	}
	parked, err := repository.GetRun(ctx, run.ID)
	if err != nil || parked.Status != store.RunStatusWaitingUser || parked.ContinuationCount != 0 ||
		parked.ResumeLeaseHolder != "" || parked.ResumeLeaseExpiresAt != nil || parked.WakeAt != nil {
		t.Fatalf("parked initial execution=%#v err=%v", parked, err)
	}
}

func TestInitialExecutionLeasePersistsStartedAndContextMetadata(t *testing.T) {
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
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	startedAt := time.Now().UTC().Add(-time.Second)
	fields := map[string]any{
		"started_at": startedAt, "context_tokens_before": 900,
		"context_summary_buckets": 2, "context_need_retrieval": true,
		"context_plan": json.RawMessage(`{"tokensBefore":900,"needRetrieval":true}`),
	}
	manager := &Manager{store: repository, longHorizon: repository}
	claimToken := "initial-metadata-token"
	if err := manager.transitionRunExecutionStarted(ctx, run.ID, claimToken, fields); err != nil {
		t.Fatalf("transitionRunExecutionStarted: %v", err)
	}
	started, err := repository.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if started.Status != store.RunStatusRunning || started.ResumeLeaseHolder != claimToken ||
		started.StartedAt == nil || !started.StartedAt.Equal(startedAt) ||
		started.ContextTokensBefore == nil || *started.ContextTokensBefore != 900 ||
		started.ContextSummaryBuckets == nil || *started.ContextSummaryBuckets != 2 ||
		started.ContextNeedRetrieval == nil || !*started.ContextNeedRetrieval ||
		len(started.ContextPlanJSON) == 0 {
		t.Fatalf("initial execution metadata was not durably fenced: %#v", started)
	}
}

func TestClaimedRunMetadataCannotOverwriteConcurrentCancellation(t *testing.T) {
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
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	claimed, ok, err := repository.ClaimRunWake(ctx, run.ID, run.Revision, "cancel-race-token", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("ClaimRunWake ok=%v run=%#v err=%v", ok, claimed, err)
	}
	manager := &Manager{store: repository, longHorizon: repository}
	changed, err := manager.cancelRunDurably(ctx, run.ID)
	if err != nil || !changed {
		t.Fatalf("cancelRunDurably changed=%v err=%v", changed, err)
	}
	if err := repository.UpdateClaimedRunMetadata(ctx, run.ID, "cancel-race-token", map[string]any{
		"started_at": now, "context_tokens_before": 50,
	}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale claimed metadata err=%v, want ErrConflict", err)
	}
	cancelled, err := repository.GetRun(ctx, run.ID)
	if err != nil || cancelled.Status != store.RunStatusCancelled || cancelled.StartedAt != nil ||
		cancelled.ContextTokensBefore != nil || cancelled.ResumeLeaseHolder != "" {
		t.Fatalf("stale metadata overwrote cancellation: run=%#v err=%v", cancelled, err)
	}
}

func TestClaimedContinuationStartVerificationRetriesWithoutTerminalFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, claimed, claimToken := seedClaimedContinuation(t)
	now := time.Now().UTC()
	job := jobs.JobRecord{
		ID: "qj-claimed-start-retry", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: claimed.ThreadID, RunID: claimed.ID, Handle: "qown-claimed-start-retry"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, job, 0); err != nil {
		t.Fatal(err)
	}
	obligationID, _ := store.QueryObligationIDForRoot(claimed.ID, job.ID)
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: claimed.ID, JobID: job.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
	}); err != nil {
		t.Fatal(err)
	}
	failing := &failOnceGetRunStore{Store: repository, remaining: 1}
	manager := &Manager{store: failing, longHorizon: repository}
	if err := manager.transitionRunExecutionStarted(ctx, claimed.ID, claimToken, nil); err != nil {
		t.Fatalf("transitionRunExecutionStarted: %v", err)
	}
	current, err := repository.GetRun(ctx, claimed.ID)
	if err != nil || current.Status != store.RunStatusResuming || current.ResumeLeaseHolder != claimToken || failing.remaining != 0 {
		t.Fatalf("claimed continuation was terminalized after transient read: run=%#v remaining=%d err=%v", current, failing.remaining, err)
	}
}

func TestContinuationExecutionSnapshotRoundTripsAndRejectsCorruption(t *testing.T) {
	t.Parallel()
	run := store.Run{ID: "run-1", ThreadID: "thread-1", ConnectionID: "connection-1", ProviderID: "anthropic", Model: "claude"}
	snapshot := continuationExecutionSnapshot{
		RunID: run.ID, ThreadID: run.ThreadID,
		Dispatch:            continuationDispatchIdentity{ConnectionID: run.ConnectionID, ProviderID: run.ProviderID, Model: run.Model, ConfigFingerprint: "fingerprint"},
		NextDispatchOrdinal: 7, SystemPromptBase: "base", SystemPrompt: "composite",
		ToolDefinitions: []types.ToolDefinition{{Type: "function", Function: types.ToolFunctionDefinition{Name: "query_status", Parameters: map[string]any{"type": "object"}}}},
		ToolScope:       continuationToolScope{Datasources: map[string]bool{"bigquery": true}, ThreadSearch: true},
		ProjectID:       "project-1", SearchMode: true, SearchDepth: "deep", QueryRowLimit: 2500,
		MaxContextInputTokens: 1000, MaxContextHardTokens: 2000, TokenCharsPerToken: 4,
		EngineInputCap: 800, HardInputCap: 1800, RunInputCap: 800, EvidenceBudget: 8, EvidenceUsed: 2,
		RuntimeReference: time.Now().UTC(), RuntimePlan: &types.RuntimePlanPolicy{PlanID: "plan-1"},
	}
	raw, err := marshalContinuationExecutionSnapshot(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	restored, err := unmarshalContinuationExecutionSnapshot(raw, run)
	if err != nil {
		t.Fatalf("unmarshal snapshot: %v", err)
	}
	if restored.NextDispatchOrdinal != 7 || restored.SystemPrompt != "composite" ||
		len(restored.ToolDefinitions) != 1 || !restored.ToolScope.Datasources["bigquery"] || restored.ProjectID != "project-1" {
		t.Fatalf("restored snapshot=%#v", restored)
	}
	var corrupted map[string]any
	if err := json.Unmarshal(raw, &corrupted); err != nil {
		t.Fatal(err)
	}
	corrupted["next_dispatch_ordinal"] = float64(8)
	corruptedRaw, _ := json.Marshal(corrupted)
	if _, err := unmarshalContinuationExecutionSnapshot(corruptedRaw, run); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("corrupt snapshot err=%v, want integrity failure", err)
	}
}

func TestRecoveredRunReplaysExactSnapshotWithoutInitialReplanning(t *testing.T) {
	var plannerCalls atomic.Int64
	requestBody := make(chan []byte, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body json.RawMessage
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		select {
		case requestBody <- append([]byte(nil), body...):
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_exact_replay\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"done\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer server.Close()

	cfgDir := t.TempDir()
	manager, baseStore := newAnthropicStartRunManagerForSSE(t, cfgDir, server.URL, 100000)
	repository, ok := baseStore.(store.LongHorizonRepository)
	if !ok {
		t.Fatal("test store does not implement LongHorizonRepository")
	}
	manager.longHorizon = repository
	manager.contextPlanner = NewContextPlanner(func(context.Context, string, string) (string, error) {
		plannerCalls.Add(1)
		return "SHOULD-NOT-BE-CALLED", nil
	})

	ctx := context.Background()
	thread, err := baseStore.CreateThread(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := baseStore.CreateRun(ctx, thread.ID, "anthropic", "claude-opus-4-6", nil, "MUTABLE ORIGINAL PROMPT", 12)
	if err != nil {
		t.Fatal(err)
	}
	run, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision, ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusRunning,
	})
	if err != nil || !changed {
		t.Fatalf("move running changed=%v err=%v", changed, err)
	}
	dispatch, err := manager.providers.SnapshotConnection(manager.settings.Get(), "", run.Model, "", "")
	if err != nil {
		t.Fatal(err)
	}
	exactContent := "EXACT-CHECKPOINT-HISTORY"
	point := engine.LoopControlPoint{
		Iteration:     6,
		History:       []types.ChatMessage{{Role: types.RoleUser, Content: &exactContent}},
		ProviderState: map[string]any{"exact_state": "preserved"},
	}
	snapshot := continuationExecutionSnapshot{
		Dispatch: continuationDispatchIdentity{
			ConnectionID: dispatch.ConnectionID, LegacyProfileID: dispatch.LegacyProfileID,
			ProviderID: dispatch.ProviderID, Model: dispatch.Model,
			ConfigFingerprint: dispatch.ConfigFingerprint, PlannedAPISurface: dispatch.PlannedAPISurface,
		},
		NextDispatchOrdinal: 9,
		SystemPromptBase:    "EXACT-SYSTEM-BASE",
		SystemPrompt:        "EXACT-COMPOSITE-SYSTEM-PROMPT",
		ToolDefinitions: []types.ToolDefinition{{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name: "exact_snapshot_tool", Description: "checkpointed tool only",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			},
		}},
		ToolScope:   continuationToolScope{Datasources: map[string]bool{}},
		SearchDepth: "off", QueryRowLimit: 2500, ContextPlan: RunContextPlan{PlanReason: "exact_checkpoint"},
		MaxContextInputTokens: 100000, MaxContextHardTokens: 120000, TokenCharsPerToken: 4,
		EngineInputCap: 99000, HardInputCap: 119000, RunInputCap: 99000,
		EvidenceBudget: 8, RuntimeReference: time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC),
	}
	checkpoint, waiting, err := manager.suspendRunAtCheckpoint(ctx, run, point, nil, snapshot, 0, 3, "")
	if err != nil {
		t.Fatalf("suspend: %v", err)
	}
	mutableContent := "MUTABLE-POST-CHECKPOINT-HISTORY-MUST-NOT-REPLAY"
	if _, err := baseStore.AppendMessage(ctx, thread.ID, &run.ID, store.Message{Role: types.RoleUser, Content: &mutableContent}); err != nil {
		t.Fatal(err)
	}
	claimed, outcome, token, err := manager.claimRunContinuation(ctx, waiting.ID)
	if err != nil || outcome != continuationClaimAcquired {
		t.Fatalf("claim outcome=%s token=%q err=%v", outcome, token, err)
	}
	if err := manager.launchRecoveredRun(ctx, claimed, &checkpoint, token); err != nil {
		t.Fatalf("launchRecoveredRun: %v", err)
	}
	terminal, err := waitForRunTerminalState(ctx, baseStore, run.ID)
	if err != nil || terminal.Status != store.RunStatusCompleted {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
	if plannerCalls.Load() != 0 {
		t.Fatalf("initial context planner calls=%d, want 0", plannerCalls.Load())
	}
	select {
	case raw := <-requestBody:
		payload := string(raw)
		for _, marker := range []string{"EXACT-COMPOSITE-SYSTEM-PROMPT", exactContent, "exact_snapshot_tool"} {
			if !strings.Contains(payload, marker) {
				t.Fatalf("recovered provider request missing %q: %s", marker, payload)
			}
		}
		for _, marker := range []string{mutableContent, "MUTABLE ORIGINAL PROMPT", "SHOULD-NOT-BE-CALLED"} {
			if strings.Contains(payload, marker) {
				t.Fatalf("recovered provider request unexpectedly contains %q: %s", marker, payload)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered run did not dispatch provider request")
	}
}

func TestQueryContinuationIdentityNoticeUsesAuthoritativeRepairedCapability(t *testing.T) {
	t.Parallel()
	const obligationID = "qob-1234567890abcdef1234567890abcdef"
	notice := queryContinuationIdentityNotice([]queryDependencyRef{
		{
			ID: "qj-repaired-1234567890abcdef", Owner: jobs.JobOwner{Handle: "owner-repaired"},
			Status: jobs.JobStatusRunning, ValidationState: store.QueryValidationUnvalidated,
			ObligationID: obligationID,
		},
		{ID: "00000-00000", Owner: jobs.JobOwner{Handle: "owner-stale"}, ObligationID: obligationID},
	})
	for _, want := range []string{"qj-repaired-1234567890abcdef", "owner-repaired", obligationID, "authoritative current query capabilities"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("identity notice %q does not contain %q", notice, want)
		}
	}
	for _, stale := range []string{"00000-00000", "owner-stale"} {
		if strings.Contains(notice, stale) {
			t.Fatalf("identity notice exposed stale capability %q: %s", stale, notice)
		}
	}
}

type renewingRepository struct {
	store.LongHorizonRepository
	renewals atomic.Int64
	reject   bool
}

func (repository *renewingRepository) RenewRunWakeLease(ctx context.Context, runID, claimant string, ttl time.Duration) (bool, error) {
	repository.renewals.Add(1)
	if repository.reject {
		return false, nil
	}
	return repository.LongHorizonRepository.RenewRunWakeLease(ctx, runID, claimant, ttl)
}

func seedClaimedContinuation(t *testing.T) (*store.JSONStore, store.Run, string) {
	t.Helper()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	run, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision, ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusRunning,
	})
	if err != nil || !changed {
		t.Fatalf("move running changed=%v err=%v", changed, err)
	}
	wakeAt := time.Now().UTC().Add(-time.Second)
	_, run, changed, err = repository.SuspendRun(ctx, store.SuspendRunMutation{
		Checkpoint: store.ContinuationCheckpoint{
			ID: "checkpoint-heartbeat", RunID: run.ID, ThreadID: run.ThreadID,
			HistoryJSON:           json.RawMessage(`[{"role":"assistant","content":"waiting"}]`),
			ExecutionSnapshotJSON: json.RawMessage(`{"version":1}`), CreatedAt: time.Now().UTC(),
		},
		ExpectedRevision: run.Revision, ExpectedStatuses: []string{store.RunStatusRunning},
		ContinuationCount: 0, MaxAutoContinuations: 3, WakeAt: wakeAt,
	})
	if err != nil || !changed {
		t.Fatalf("suspend changed=%v err=%v", changed, err)
	}
	manager := &Manager{store: repository, longHorizon: repository, resumeClaimant: "test"}
	claimed, outcome, token, err := manager.claimRunContinuation(ctx, run.ID)
	if err != nil || outcome != continuationClaimAcquired || token == "" {
		t.Fatalf("claim outcome=%s token=%q run=%#v err=%v", outcome, token, claimed, err)
	}
	return repository, claimed, token
}

func TestRunWakeLeaseHeartbeatRenewsWhileExecutionWaits(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	counting := &renewingRepository{LongHorizonRepository: repository}
	ctx, cancel := context.WithCancel(context.Background())
	guard := newRunWakeLeaseGuard(counting, claimed.ID, 45*time.Millisecond, cancel)
	guard.Hold(token, claimed.ResumeLeaseExpiresAt)
	time.Sleep(125 * time.Millisecond)
	guard.Stop()
	defer cancel()
	if guard.Lost() || counting.renewals.Load() < 2 || ctx.Err() != nil {
		t.Fatalf("lost=%v renewals=%d ctx=%v", guard.Lost(), counting.renewals.Load(), ctx.Err())
	}
}

func TestRunWakeHeartbeatSelfDisarmsAfterNewerMessageCommits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, thread, waiting, _, job := seedFutureRunContinuation(t, t.TempDir())
	dueAt := time.Now().UTC().Add(-time.Second)
	waiting, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: waiting.ID, ExpectedRevision: waiting.Revision,
		ExpectedStatuses: []string{store.RunStatusWaitingExternal},
		Status:           store.RunStatusWaitingExternal, WakeAt: &dueAt,
	})
	if err != nil || !changed {
		t.Fatalf("make wake due changed=%v err=%v", changed, err)
	}
	manager := &Manager{store: repository, longHorizon: repository, resumeClaimant: "heartbeat-old-worker"}
	claimed, outcome, claimToken, err := manager.claimRunContinuation(ctx, waiting.ID)
	if err != nil || outcome != continuationClaimAcquired || claimToken == "" {
		t.Fatalf("claim outcome=%s token=%q run=%#v err=%v", outcome, claimToken, claimed, err)
	}

	counting := &renewingRepository{LongHorizonRepository: repository}
	executionCtx, cancelExecution := context.WithCancel(context.Background())
	defer cancelExecution()
	guard := newRunWakeLeaseGuard(counting, claimed.ID, 80*time.Millisecond, cancelExecution)
	defer guard.Stop()
	var validationChecks atomic.Int64
	guard.SetSupersessionValidator(func(validationCtx context.Context) (bool, error) {
		validationChecks.Add(1)
		return manager.validateRunContinuationGeneration(validationCtx, claimed.ID)
	})
	guard.Hold(claimToken, claimed.ResumeLeaseExpiresAt)

	// Wait for a complete clean heartbeat. The worker has now passed its prior
	// generation check, matching the crash race this guard must close.
	deadline := time.Now().Add(2 * time.Second)
	for counting.renewals.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if counting.renewals.Load() == 0 || validationChecks.Load() == 0 {
		t.Fatalf("initial heartbeat checks=%d renewals=%d", validationChecks.Load(), counting.renewals.Load())
	}

	// Commit the newer durable generation but deliberately skip the eager
	// StartRun disarm, simulating a process crash immediately after AppendMessage.
	newRun, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	newContent := "new direction committed after the stale worker's pre-dispatch check"
	if _, err := repository.AppendMessage(ctx, thread.ID, &newRun.ID, store.Message{Role: types.RoleUser, Content: &newContent}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-executionCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not cancel the superseded resumed worker")
	}
	disarmed, err := repository.GetRun(ctx, claimed.ID)
	if err != nil || !guard.Lost() || disarmed.Status != store.RunStatusWaitingUser ||
		disarmed.WakeAt != nil || disarmed.ResumeLeaseHolder != "" || disarmed.ResumeLeaseExpiresAt != nil {
		t.Fatalf("heartbeat fencing lost=%v run=%#v err=%v", guard.Lost(), disarmed, err)
	}
	preserved, found, err := repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found || preserved.Status != jobs.JobStatusRunning {
		t.Fatalf("warehouse job was disturbed by heartbeat supersession: job=%#v found=%v err=%v", preserved, found, err)
	}
}

func TestRunWakeHeartbeatToleratesTransientSupersessionCheckErrorsUntilExpiry(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	counting := &renewingRepository{LongHorizonRepository: repository}
	executionCtx, cancelExecution := context.WithCancel(context.Background())
	defer cancelExecution()
	guard := newRunWakeLeaseGuard(counting, claimed.ID, 80*time.Millisecond, cancelExecution)
	defer guard.Stop()
	var checks atomic.Int64
	guard.SetSupersessionValidator(func(context.Context) (bool, error) {
		if checks.Add(1) <= 2 {
			return false, errors.New("transient generation-store failure")
		}
		return false, nil
	})
	shortExpiry := time.Now().UTC().Add(250 * time.Millisecond)
	guard.Hold(token, &shortExpiry)

	deadline := time.Now().Add(2 * time.Second)
	for counting.renewals.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if checks.Load() < 3 || counting.renewals.Load() == 0 {
		t.Fatalf("validator did not recover: checks=%d renewals=%d", checks.Load(), counting.renewals.Load())
	}
	if guard.Lost() || executionCtx.Err() != nil {
		t.Fatalf("transient validation errors cancelled a live lease: lost=%v ctx=%v", guard.Lost(), executionCtx.Err())
	}
}

func TestRunWakeHeartbeatStopsAfterPersistentSupersessionCheckErrorsReachExpiry(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	counting := &renewingRepository{LongHorizonRepository: repository}
	executionCtx, cancelExecution := context.WithCancel(context.Background())
	defer cancelExecution()
	guard := newRunWakeLeaseGuard(counting, claimed.ID, 40*time.Millisecond, cancelExecution)
	defer guard.Stop()
	guard.SetSupersessionValidator(func(context.Context) (bool, error) {
		return false, errors.New("persistent generation-store failure")
	})
	started := time.Now()
	shortExpiry := time.Now().UTC().Add(100 * time.Millisecond)
	guard.Hold(token, &shortExpiry)
	select {
	case <-executionCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("persistent validation errors did not fence execution after lease expiry")
	}
	if elapsed := time.Since(started); elapsed < 70*time.Millisecond {
		t.Fatalf("transient-error tolerance ended before lease expiry: %s", elapsed)
	}
	if !guard.Lost() || counting.renewals.Load() != 0 {
		t.Fatalf("persistent validation result lost=%v renewals=%d", guard.Lost(), counting.renewals.Load())
	}
}

func TestClaimRunContinuationReportsHeldElsewhereWithoutDoubleCounting(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	manager := &Manager{store: repository, longHorizon: repository, resumeClaimant: "competitor"}
	observed, outcome, competitorToken, err := manager.claimRunContinuation(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != continuationClaimHeldElsewhere || competitorToken != "" || observed.ResumeLeaseHolder != token ||
		observed.ContinuationCount != claimed.ContinuationCount {
		t.Fatalf("outcome=%s token=%q observed=%#v claimed=%#v", outcome, competitorToken, observed, claimed)
	}
}

func TestClaimRunContinuationReportsCapReached(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	wakeAt := time.Now().UTC().Add(-time.Second)
	_, waiting, changed, err := repository.SuspendRun(context.Background(), store.SuspendRunMutation{
		Checkpoint: store.ContinuationCheckpoint{
			ID: "checkpoint-cap", RunID: claimed.ID, ThreadID: claimed.ThreadID,
			HistoryJSON:           json.RawMessage(`[{"role":"assistant","content":"still waiting"}]`),
			ExecutionSnapshotJSON: json.RawMessage(`{"version":1}`), CreatedAt: time.Now().UTC(),
		},
		ExpectedRevision: claimed.Revision, ExpectedStatuses: []string{store.RunStatusResuming},
		ExpectedResumeLeaseHolder: token, ContinuationCount: 3, MaxAutoContinuations: 3, WakeAt: wakeAt,
	})
	if err != nil || !changed {
		t.Fatalf("second suspend changed=%v waiting=%#v err=%v", changed, waiting, err)
	}
	manager := &Manager{store: repository, longHorizon: repository, resumeClaimant: "cap"}
	observed, outcome, claimToken, err := manager.claimRunContinuation(context.Background(), claimed.ID)
	if err != nil || outcome != continuationClaimCapReached || claimToken != "" || observed.Status != store.RunStatusWaitingUser || observed.ContinuationCount != 3 {
		t.Fatalf("outcome=%s token=%q observed=%#v err=%v", outcome, claimToken, observed, err)
	}
}

func TestRequeueClaimedRunNeverRevokesAnotherHolder(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	manager := &Manager{store: repository, longHorizon: repository}
	if err := manager.requeueClaimedRun(context.Background(), claimed.ID, "wrong-"+token); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong-holder requeue err=%v, want ErrConflict", err)
	}
	current, err := repository.GetRun(context.Background(), claimed.ID)
	if err != nil || current.Status != store.RunStatusResuming || current.ResumeLeaseHolder != token {
		t.Fatalf("winner was disturbed: run=%#v err=%v", current, err)
	}
}

func TestRunWakeLeaseLossOnlyCancelsStaleLocalExecution(t *testing.T) {
	t.Parallel()
	repository, claimed, token := seedClaimedContinuation(t)
	rejecting := &renewingRepository{LongHorizonRepository: repository, reject: true}
	ctx, cancel := context.WithCancel(context.Background())
	guard := newRunWakeLeaseGuard(rejecting, claimed.ID, 30*time.Millisecond, cancel)
	guard.Hold(token, claimed.ResumeLeaseExpiresAt)
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		guard.Stop()
		t.Fatal("lease loss did not cancel local execution")
	}
	guard.Stop()
	current, err := repository.GetRun(context.Background(), claimed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !guard.Lost() || current.Status != store.RunStatusResuming || current.ResumeLeaseHolder != token {
		t.Fatalf("lost=%v durable run=%#v", guard.Lost(), current)
	}
}

func TestManagerCancelWithoutLocalStateIsDurableAndPreservesQueryJob(t *testing.T) {
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
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	job := jobs.JobRecord{
		ID: "job-preserved-after-cancel", Provider: "bigquery", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "owner"},
		SubmittedAt: time.Now().UTC(), LastAccess: time.Now().UTC(),
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, job, 0); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{store: repository, longHorizon: repository, runs: map[string]*runState{}}
	if err := manager.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	cancelled, err := repository.GetRun(ctx, run.ID)
	if err != nil || cancelled.Status != store.RunStatusCancelled {
		t.Fatalf("cancelled run=%#v err=%v", cancelled, err)
	}
	if changed, err := manager.transitionRunTerminal(ctx, run.ID, store.RunStatusCompleted, "", map[string]any{"ended_at": time.Now().UTC()}); err != nil || changed {
		t.Fatalf("stale completion changed=%v err=%v", changed, err)
	}
	preserved, found, err := repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found || preserved.Status != jobs.JobStatusRunning {
		t.Fatalf("preserved job=%#v found=%v err=%v", preserved, found, err)
	}
}

func seedFutureRunContinuation(
	t *testing.T,
	cfgDir string,
) (*store.JSONStore, store.Thread, store.Run, store.ContinuationCheckpoint, jobs.JobRecord) {
	t.Helper()
	ctx := context.Background()
	repository, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	content := "start a durable warehouse query"
	userMessage, err := repository.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		ID: types.NewUUID(), Role: types.RoleUser, Content: &content,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: run.ID, ExpectedRevision: run.Revision,
		ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusRunning,
	})
	if err != nil || !changed {
		t.Fatalf("move running changed=%v err=%v", changed, err)
	}
	history, err := json.Marshal([]types.ChatMessage{{ID: userMessage.ID, Role: types.RoleUser, Content: &content}})
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Now().UTC()
	checkpoint, waiting, changed, err := repository.SuspendRun(ctx, store.SuspendRunMutation{
		Checkpoint: store.ContinuationCheckpoint{
			ID: "checkpoint-future-" + run.ID, RunID: run.ID, ThreadID: thread.ID,
			HistoryJSON: history, ExecutionSnapshotJSON: json.RawMessage(`{"version":1}`),
			DependencyIDs: []string{"job-future-" + run.ID}, CreatedAt: createdAt,
		},
		ExpectedRevision: run.Revision, ExpectedStatuses: []string{store.RunStatusRunning},
		ContinuationCount: 0, MaxAutoContinuations: 3, WakeAt: createdAt.Add(time.Hour),
	})
	if err != nil || !changed {
		t.Fatalf("suspend future continuation changed=%v err=%v", changed, err)
	}
	job := jobs.JobRecord{
		ID: "job-future-" + run.ID, Provider: "bigquery", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "owner-" + run.ID},
		SubmittedAt: createdAt, LastAccess: createdAt,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, job, 0); err != nil {
		t.Fatal(err)
	}
	return repository, thread, waiting, checkpoint, job
}

func TestDurableQueryWaitErrorDefersToRecoveryWithoutFailingRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _, waiting, checkpoint, _ := seedFutureRunContinuation(t, t.TempDir())
	manager := &Manager{store: repository, longHorizon: repository}
	beforeWake := *waiting.WakeAt
	beforeRevision := waiting.Revision

	if !manager.deferSuspendedQueryWaitError(ctx, waiting.ID, errors.New("transient wake claim failure")) {
		t.Fatal("durable non-cancellation wait error was not deferred to recovery")
	}
	unchanged, err := repository.GetRun(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != store.RunStatusWaitingExternal || unchanged.Revision != beforeRevision ||
		unchanged.WakeAt == nil || !unchanged.WakeAt.Equal(beforeWake) || unchanged.ErrorCode != nil {
		t.Fatalf("deferred wait error mutated durable run: before=%#v after=%#v", waiting, unchanged)
	}
	latest, err := repository.LatestContinuationCheckpoint(ctx, waiting.ID)
	if err != nil || latest == nil || latest.ID != checkpoint.ID {
		t.Fatalf("deferred wait error disturbed checkpoint: latest=%#v err=%v", latest, err)
	}

	cancelledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if manager.deferSuspendedQueryWaitError(cancelledCtx, waiting.ID, errors.New("wrapped local error")) ||
		manager.deferSuspendedQueryWaitError(ctx, waiting.ID, context.Canceled) {
		t.Fatal("explicit cancellation was incorrectly deferred as a transient wait error")
	}
}

func TestRecoveryQuarantinesExpiredInitialExecutionWithQueryDependency(t *testing.T) {
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
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	running, claimed, err := repository.ClaimRunWake(
		ctx, run.ID, run.Revision, "dead-initial-worker", now.Add(-2*time.Minute), now.Add(-time.Minute),
	)
	if err != nil || !claimed || running.Status != store.RunStatusRunning {
		t.Fatalf("seed expired initial lease claimed=%v run=%#v err=%v", claimed, running, err)
	}
	job := jobs.JobRecord{
		ID: "qj-crash-before-first-checkpoint", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "owner-crash-window"},
		ValidationState: jobs.ValidationUnvalidated, SubmittedAt: now.Add(-90 * time.Second), LastAccess: now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, job, 0); err != nil {
		t.Fatal(err)
	}
	obligationID, err := store.QueryObligationIDForRoot(run.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: job.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	manager := &Manager{
		store: repository, longHorizon: repository, resumeClaimant: "restart-worker",
		activeByThread: make(map[string]string), runs: make(map[string]*runState),
	}
	manager.activeByThread[thread.ID] = run.ID
	manager.recoverLongHorizonRuns(ctx)
	stillActive, err := repository.GetRun(ctx, run.ID)
	if err != nil || stillActive.Status != store.RunStatusRunning || stillActive.ResumeLeaseHolder != "dead-initial-worker" {
		t.Fatalf("same-process active run was stolen: run=%#v err=%v", stillActive, err)
	}
	delete(manager.activeByThread, thread.ID)
	manager.recoverLongHorizonRuns(ctx)

	quarantined, err := repository.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if quarantined.Status != store.RunStatusWaitingUser || quarantined.ContinuationCount != 0 ||
		quarantined.ResumeLeaseHolder != "" || quarantined.ResumeLeaseExpiresAt != nil {
		t.Fatalf("recovered crash-window run=%#v", quarantined)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].Detached ||
		!dependencies[0].RequiredForCompletion || dependencies[0].JobID != job.ID {
		t.Fatalf("recovery disturbed warehouse obligation=%#v err=%v", dependencies, err)
	}
}

func TestRecoveryParksLegacyRunningRowsWithoutStarvingLaterWake(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()
	repository, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacyRunIDs := make([]string, 0, longHorizonRecoveryBatch)
	for index := 0; index < longHorizonRecoveryBatch; index++ {
		run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
		if err != nil {
			t.Fatal(err)
		}
		run, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
			RunID: run.ID, ExpectedRevision: run.Revision,
			ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusRunning,
		})
		if err != nil || !changed {
			t.Fatalf("move legacy run %d to running changed=%v err=%v", index, changed, err)
		}
		path, err := jsondb.RunPath(cfgDir, thread.ID, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		var record jsondb.Run
		if err := jsondb.ReadJSONFileStrict(path, &record); err != nil {
			t.Fatal(err)
		}
		record.UpdatedAt = time.Now().UTC().Add(-time.Hour)
		if err := jsondb.WriteJSONFile(path, record); err != nil {
			t.Fatal(err)
		}
		legacyRunIDs = append(legacyRunIDs, run.ID)
	}

	actionable, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(-time.Minute)
	actionable, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: actionable.ID, ExpectedRevision: actionable.Revision,
		ExpectedStatuses: []string{store.RunStatusQueued}, Status: store.RunStatusWaitingExternal,
		WakeAt: &dueAt,
	})
	if err != nil || !changed {
		t.Fatalf("seed actionable wake changed=%v err=%v", changed, err)
	}

	manager := &Manager{
		store: repository, longHorizon: repository, resumeClaimant: "recovery-worker",
		activeByThread: make(map[string]string), runs: make(map[string]*runState),
	}
	manager.recoverLongHorizonRuns(ctx)
	stillWaiting, err := repository.GetRun(ctx, actionable.ID)
	if err != nil || stillWaiting.Status != store.RunStatusWaitingExternal {
		t.Fatalf("first bounded scan unexpectedly reached later wake: run=%#v err=%v", stillWaiting, err)
	}
	for _, runID := range legacyRunIDs {
		parked, err := repository.GetRun(ctx, runID)
		if err != nil || parked.Status != store.RunStatusWaitingUser {
			t.Fatalf("legacy run %s was not durably parked: run=%#v err=%v", runID, parked, err)
		}
	}

	manager.recoverLongHorizonRuns(ctx)
	recovered, err := repository.GetRun(ctx, actionable.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Status != store.RunStatusWaitingUser {
		t.Fatalf("later actionable wake remained starved: run=%#v", recovered)
	}
}

func TestNewUserRunDisarmsOlderFutureContinuationWithoutCancellingWarehouseJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, thread, waiting, _, job := seedFutureRunContinuation(t, t.TempDir())
	newRun, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	content := "use the latest direction instead"
	newUserMessage, err := repository.AppendMessage(ctx, thread.ID, &newRun.ID, store.Message{Role: types.RoleUser, Content: &content})
	if err != nil {
		t.Fatal(err)
	}
	manager := &Manager{store: repository, longHorizon: repository}
	if err := manager.disarmPriorContinuationsForNewRun(ctx, newRun, newUserMessage.Seq); err != nil {
		t.Fatalf("disarm prior continuation: %v", err)
	}
	disarmed, err := repository.GetRun(ctx, waiting.ID)
	if err != nil || disarmed.Status != store.RunStatusWaitingUser || disarmed.WakeAt != nil ||
		disarmed.ResumeLeaseHolder != "" || disarmed.ResumeLeaseExpiresAt != nil {
		t.Fatalf("disarmed run=%#v err=%v", disarmed, err)
	}
	recoverable, err := repository.ListRecoverableRuns(ctx, time.Now().UTC().Add(2*time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range recoverable {
		if candidate.ID == waiting.ID {
			t.Fatalf("superseded continuation remained recoverable: %#v", candidate)
		}
	}
	preserved, found, err := repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found || preserved.Status != jobs.JobStatusRunning {
		t.Fatalf("warehouse job was disturbed: job=%#v found=%v err=%v", preserved, found, err)
	}
}

func TestFinalizedPlanSameRunReplayCannotDisarmItsOwnContinuation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, _, waiting, _, _ := seedFutureRunContinuation(t, t.TempDir())
	manager := &Manager{store: repository, longHorizon: repository}
	// A finalized plan returns its original run and appends no user message. Even
	// if a caller redundantly invokes the eager pass, identity exclusion keeps
	// that same run's checkpoint armed.
	if err := manager.disarmPriorContinuationsForNewRun(ctx, waiting, 1<<62); err != nil {
		t.Fatal(err)
	}
	unchanged, err := repository.GetRun(ctx, waiting.ID)
	if err != nil || unchanged.Status != store.RunStatusWaitingExternal || unchanged.WakeAt == nil {
		t.Fatalf("same-run replay changed continuation: run=%#v err=%v", unchanged, err)
	}
}

func TestRecoveryDisarmsFutureCheckpointAfterCrashAndNewerUserRun(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfgDir := t.TempDir()
	_, thread, waiting, checkpoint, job := seedFutureRunContinuation(t, cfgDir)

	// Simulate a restart, then a crash window in which the newer run and user
	// message commit before StartRun can perform its eager disarm pass.
	reopened, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	newRun, err := reopened.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	newContent := "new direction after restart"
	if _, err := reopened.AppendMessage(ctx, thread.ID, &newRun.ID, store.Message{Role: types.RoleUser, Content: &newContent}); err != nil {
		t.Fatal(err)
	}
	current, err := reopened.GetRun(ctx, waiting.ID)
	if err != nil {
		t.Fatal(err)
	}
	dueAt := time.Now().UTC().Add(-time.Second)
	current, changed, err := reopened.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: current.ID, ExpectedRevision: current.Revision,
		ExpectedStatuses: []string{store.RunStatusWaitingExternal},
		Status:           store.RunStatusWaitingExternal, WakeAt: &dueAt,
	})
	if err != nil || !changed {
		t.Fatalf("make old wake due changed=%v err=%v", changed, err)
	}
	manager := &Manager{store: reopened, longHorizon: reopened, resumeClaimant: "recovery"}
	manager.recoverLongHorizonRuns(ctx)

	disarmed, err := reopened.GetRun(ctx, waiting.ID)
	if err != nil || disarmed.Status != store.RunStatusWaitingUser || disarmed.WakeAt != nil ||
		disarmed.ResumeLeaseHolder != "" || disarmed.ContinuationCount != current.ContinuationCount {
		t.Fatalf("recovered stale continuation=%#v err=%v", disarmed, err)
	}
	latest, err := reopened.LatestContinuationCheckpoint(ctx, waiting.ID)
	if err != nil || latest == nil || latest.ID != checkpoint.ID {
		t.Fatalf("checkpoint changed during supersession: latest=%#v err=%v", latest, err)
	}
	preserved, found, err := reopened.GetQueryJob(ctx, job.ID)
	if err != nil || !found || preserved.Status != jobs.JobStatusRunning {
		t.Fatalf("warehouse job was disturbed by recovery fencing: job=%#v found=%v err=%v", preserved, found, err)
	}
}

func TestNewerUserRunRevokesClaimedLeaseAndBlocksStaleRecoveredLaunch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, thread, waiting, checkpoint, job := seedFutureRunContinuation(t, t.TempDir())
	dueAt := time.Now().UTC().Add(-time.Second)
	waiting, changed, err := repository.TransitionRunCAS(ctx, store.RunStateMutation{
		RunID: waiting.ID, ExpectedRevision: waiting.Revision,
		ExpectedStatuses: []string{store.RunStatusWaitingExternal},
		Status:           store.RunStatusWaitingExternal, WakeAt: &dueAt,
	})
	if err != nil || !changed {
		t.Fatalf("make wake due changed=%v err=%v", changed, err)
	}
	claimingManager := &Manager{store: repository, longHorizon: repository, resumeClaimant: "old-worker"}
	claimed, outcome, claimToken, err := claimingManager.claimRunContinuation(ctx, waiting.ID)
	if err != nil || outcome != continuationClaimAcquired || claimToken == "" {
		t.Fatalf("claim outcome=%s token=%q run=%#v err=%v", outcome, claimToken, claimed, err)
	}

	newRun, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 12)
	if err != nil {
		t.Fatal(err)
	}
	newContent := "supersede the old model continuation"
	newUserMessage, err := repository.AppendMessage(ctx, thread.ID, &newRun.ID, store.Message{Role: types.RoleUser, Content: &newContent})
	if err != nil {
		t.Fatal(err)
	}
	fencingManager := &Manager{store: repository, longHorizon: repository, resumeClaimant: "new-worker"}
	if err := fencingManager.disarmPriorContinuationsForNewRun(ctx, newRun, newUserMessage.Seq); err != nil {
		t.Fatal(err)
	}
	disarmed, err := repository.GetRun(ctx, claimed.ID)
	if err != nil || disarmed.Status != store.RunStatusWaitingUser || disarmed.ResumeLeaseHolder != "" ||
		disarmed.ResumeLeaseExpiresAt != nil {
		t.Fatalf("claimed run lease was not revoked: run=%#v err=%v", disarmed, err)
	}
	if renewed, err := repository.RenewRunWakeLease(ctx, claimed.ID, claimToken, time.Minute); err != nil || renewed {
		t.Fatalf("stale lease renewed=%v err=%v", renewed, err)
	}
	if err := claimingManager.launchRecoveredRun(ctx, claimed, &checkpoint, claimToken); !errors.Is(err, errContinuationSuperseded) {
		t.Fatalf("stale recovered launch err=%v, want errContinuationSuperseded", err)
	}
	preserved, found, err := repository.GetQueryJob(ctx, job.ID)
	if err != nil || !found || preserved.Status != jobs.JobStatusRunning {
		t.Fatalf("warehouse job was disturbed by lease revocation: job=%#v found=%v err=%v", preserved, found, err)
	}
}

func TestQueryDependenciesForRunPreservesPerJobOwnerTokens(t *testing.T) {
	registry := jobs.NewRegistry(jobs.RegistryOptions{})
	for _, tc := range []struct {
		runID string
		token string
	}{
		{runID: "run-target", token: "tok_a"},
		{runID: "run-target", token: "tok_b"},
		{runID: "run-other", token: "tok_c"},
	} {
		if _, err := registry.Register(
			jobs.JobOwner{ThreadID: "thread-1", RunID: tc.runID, Handle: tc.token},
			"test", "select 1", "select 1", nil, nil, nil,
		); err != nil {
			t.Fatalf("Register(%s): %v", tc.token, err)
		}
	}

	manager := &Manager{queryJobs: registry}
	refs := manager.queryDependenciesForRun("thread-1", "run-target")
	if len(refs) != 2 {
		t.Fatalf("dependency count=%d, want 2: %#v", len(refs), refs)
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		seen[ref.Owner.Handle] = true
	}
	if !seen["tok_a"] || !seen["tok_b"] {
		t.Fatalf("per-job owner tokens were not preserved: %#v", refs)
	}
}

func TestWaitForQueryDependencyWakeSupportsDifferentOwners(t *testing.T) {
	registry := jobs.NewRegistry(jobs.RegistryOptions{})
	release := make(chan struct{})
	refs := make([]queryDependencyRef, 0, 2)
	for _, token := range []string{"tok_a", "tok_b"} {
		owner := jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: token}
		job, err := registry.Register(owner, "test", "select 1", "select 1", nil, func(context.Context) (bool, *jobs.QueryResult, error) {
			select {
			case <-release:
				return true, &jobs.QueryResult{}, nil
			default:
				return false, nil, nil
			}
		}, nil)
		if err != nil {
			t.Fatalf("Register(%s): %v", token, err)
		}
		refs = append(refs, queryDependencyRef{ID: job.ID, Owner: owner})
	}

	manager := &Manager{queryJobs: registry}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := manager.waitForQueryDependencyWake(ctx, refs)
		result <- err
	}()
	close(release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("waitForQueryDependencyWake: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("multi-owner dependency wait did not complete")
	}
}

func TestQueryDependenciesForRunRecoversUnregisteredDurableJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	record := jobs.JobRecord{
		ID:              "qj-unregistered-after-launch",
		Provider:        "bigquery",
		Status:          jobs.JobStatusRunning,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "owner-1"},
		ValidationState: jobs.ValidationUnvalidated,
		SubmittedAt:     time.Now().UTC(),
		LastAccess:      time.Now().UTC(),
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("UpsertQueryJobCAS: %v", err)
	}

	manager := &Manager{longHorizon: repository}
	refs := manager.queryDependenciesForRun(thread.ID, run.ID)
	if len(refs) != 1 || refs[0].ID != record.ID || refs[0].Owner.Handle != "owner-1" {
		t.Fatalf("recovered dependencies=%#v, want durable unregistered job", refs)
	}
}

func TestFailedQueryAttemptCreatesDurableReplacementObligation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{longHorizon: repository}
	call := types.ToolCall{ID: "call-profile-failed", Name: "run_bigquery_query", Args: map[string]any{"sql": "select * from huge_table"}}
	out := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"error": "estimated scan exceeds budget", "rewrite_required": true,
	}}

	jobID, err := manager.persistFailedQueryAttemptObligation(ctx, thread, run, call, call.Name, &out)
	if err != nil || jobID == "" {
		t.Fatalf("persistFailedQueryAttemptObligation job=%q err=%v", jobID, err)
	}
	record, found, err := repository.GetQueryJob(ctx, jobID)
	if err != nil || !found || record.Status != jobs.JobStatusFailed || record.SubmitDispatched || record.ValidationState != jobs.ValidationRejected {
		t.Fatalf("attempt record=%#v found=%v err=%v", record, found, err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != jobID || dependencies[0].ValidationState != store.QueryValidationRejected {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	result := out.Result.(map[string]any)
	if result["job_id"] != jobID || result["replacement_required"] != true {
		t.Fatalf("enriched failure=%#v", result)
	}
	firstOwner := record.Owner.Handle
	jobIDAgain, err := manager.persistFailedQueryAttemptObligation(ctx, thread, run, call, call.Name, &out)
	if err != nil || jobIDAgain != jobID {
		t.Fatalf("idempotent replay job=%q err=%v", jobIDAgain, err)
	}
	replayed, _, _ := repository.GetQueryJob(ctx, jobID)
	if replayed.Owner.Handle != firstOwner {
		t.Fatalf("owner token changed on replay: %q -> %q", firstOwner, replayed.Owner.Handle)
	}
}

func TestInvalidReplacementPreflightDoesNotMintSecondObligation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	out := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"error":                        "replacement target is not owned by this run",
		"replacement_preflight_failed": true,
		"replacement_wait_required":    true,
		"attempted_retry_target":       "native-or-foreign-id",
	}}

	if !queryOutputRequiresReplacementWait(out) {
		t.Fatal("invalid/foreign replacement must preserve the original unfinished obligation")
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 0 {
		t.Fatalf("invalid retry minted unrelated recovery work: dependencies=%#v err=%v", dependencies, err)
	}
}

func TestActiveReplacementPreflightWaitDoesNotRequestSyntheticRecovery(t *testing.T) {
	out := tools.Output{OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
		"replacement_wait_required": true,
		"job_id":                    "qj-still-running",
		"replacement_target_job_id": "qj-still-running",
	}}
	if !queryOutputRequiresReplacementWait(out) {
		t.Fatal("active replacement target must keep the original dependency instead of creating a synthetic replacement")
	}
}

func TestReplacementWaitAttachesForeignRunJobAndHealsStaleFault(t *testing.T) {
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	oldRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun old: %v", err)
	}
	currentRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun current: %v", err)
	}
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-foreign-live-wait", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: oldRun.ID, Handle: "qown-foreign-live"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist live query: %v", err)
	}
	manager := &Manager{longHorizon: repository}
	manager.setQueryDependencyFaultFor(currentRun.ID, "job:"+record.ID, errors.New("earlier dependency persistence failed"))
	manager.handleAsyncQueryReplacementWait(ctx, thread.ID, currentRun.ID, tools.Output{
		OK: false, ResultFormat: tools.ResultFormatJSON, Result: map[string]any{
			"replacement_wait_required": true, "job_id": record.ID,
		},
	})

	dependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != record.ID || !dependencies[0].RequiredForCompletion {
		t.Fatalf("wait dependencies=%#v err=%v", dependencies, err)
	}
	refs := manager.queryDependenciesForRun(thread.ID, currentRun.ID)
	if len(refs) != 1 || refs[0].ID != record.ID || refs[0].Owner.RunID != oldRun.ID {
		t.Fatalf("durably repaired dependency did not clear stale fault: refs=%#v", refs)
	}
}

func TestQueryDependencyFaultsAreIndependentCompletionBarriers(t *testing.T) {
	manager := &Manager{}
	manager.setQueryDependencyFaultFor("run-faults", "job:qj-one", errors.New("first write failed"))
	manager.setQueryDependencyFaultFor("run-faults", "job:qj-two", errors.New("second write failed"))

	refs := manager.queryDependenciesForRun("thread-faults", "run-faults")
	if len(refs) != 2 {
		t.Fatalf("independent persistence faults collapsed: %#v", refs)
	}
	manager.setQueryDependencyFaultFor("run-faults", "job:qj-one", nil)
	refs = manager.queryDependenciesForRun("thread-faults", "run-faults")
	if len(refs) != 1 {
		t.Fatalf("healing one persistence fault erased another: %#v", refs)
	}
	manager.setQueryDependencyFaultFor("run-faults", "job:qj-two", nil)
	if refs = manager.queryDependenciesForRun("thread-faults", "run-faults"); len(refs) != 0 {
		t.Fatalf("healed faults still block completion: %#v", refs)
	}
}

func TestReplacementWaitWithoutJobIdentityFailsClosed(t *testing.T) {
	manager := &Manager{}
	manager.handleAsyncQueryReplacementWait(context.Background(), "thread-wait", "run-wait", tools.Output{
		OK: true,
		Result: map[string]any{
			"replacement_wait_required": true,
		},
	})
	refs := manager.queryDependenciesForRun("thread-wait", "run-wait")
	if len(refs) != 1 || !strings.HasPrefix(refs[0].ID, "dependency-fault-") {
		t.Fatalf("wait-required output without job identity did not block completion: %#v", refs)
	}
}

func TestPersistRunQueryWaitDependencyRejectsDetachedReplay(t *testing.T) {
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	ownerRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun owner: %v", err)
	}
	waitingRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun waiting: %v", err)
	}
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-detached-wait", Provider: "databricks", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: ownerRun.ID, Handle: "qown-detached-wait"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist job: %v", err)
	}
	obligationID, err := store.QueryObligationIDForRoot(waitingRun.ID, record.ID)
	if err != nil {
		t.Fatalf("derive obligation: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: waitingRun.ID, JobID: record.ID, RequiredForCompletion: true,
		ValidationState: store.QueryValidationUnvalidated, ObligationID: obligationID,
	}); err != nil {
		t.Fatalf("seed wait dependency: %v", err)
	}
	if err := repository.DetachRunQueryDependencies(ctx, waitingRun.ID, now.Add(time.Second)); err != nil {
		t.Fatalf("detach wait dependency: %v", err)
	}
	manager := &Manager{longHorizon: repository}
	if err := manager.persistRunQueryWaitDependency(ctx, thread.ID, waitingRun.ID, record.ID); err == nil {
		t.Fatal("detached replay was accepted as an active completion barrier")
	}
}

func TestPersistRunQueryDependencyReplacesAttachedSameThreadForeignRunJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	oldRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun old: %v", err)
	}
	currentRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun current: %v", err)
	}
	now := time.Now().UTC()
	old := jobs.JobRecord{
		ID: "qj-attached-foreign-terminal", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: oldRun.ID, Handle: "qown-attached-foreign"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationRejected,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, old, 0); err != nil {
		t.Fatalf("persist old job: %v", err)
	}
	for _, runID := range []string{oldRun.ID, currentRun.ID} {
		if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
			RunID: runID, JobID: old.ID, RequiredForCompletion: true, ValidationState: store.QueryValidationRejected,
		}); err != nil {
			t.Fatalf("attach old job to %s: %v", runID, err)
		}
	}
	replacement := jobs.JobRecord{
		ID: "qj-current-run-replacement", Provider: "databricks", Status: jobs.JobStatusReserved,
		Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: currentRun.ID, Handle: "qown-current-replacement"},
		ReplacesJobID: old.ID, SubmittedAt: now.Add(time.Second), LastAccess: now.Add(time.Second),
		ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, replacement, 0); err != nil {
		t.Fatalf("persist replacement job: %v", err)
	}
	manager := &Manager{longHorizon: repository}
	if err := manager.persistRunQueryDependency(ctx, currentRun.ID, replacement.ID, old.ID); err != nil {
		t.Fatalf("persist foreign-run replacement dependency: %v", err)
	}

	currentDependencies, err := repository.ListRunQueryDependencies(ctx, currentRun.ID)
	if err != nil || len(currentDependencies) != 2 {
		t.Fatalf("current dependencies=%#v err=%v", currentDependencies, err)
	}
	currentByID := map[string]store.RunQueryDependency{}
	for _, dependency := range currentDependencies {
		currentByID[dependency.JobID] = dependency
	}
	if !currentByID[old.ID].Detached || currentByID[old.ID].ReplacementJobID != replacement.ID {
		t.Fatalf("current old dependency=%#v, want detached and linked", currentByID[old.ID])
	}
	if currentByID[replacement.ID].Detached || !currentByID[replacement.ID].RequiredForCompletion {
		t.Fatalf("current replacement dependency=%#v, want attached and required", currentByID[replacement.ID])
	}
	oldDependencies, err := repository.ListRunQueryDependencies(ctx, oldRun.ID)
	if err != nil || len(oldDependencies) != 1 || oldDependencies[0].Detached || oldDependencies[0].ReplacementJobID != "" {
		t.Fatalf("old-run dependencies changed: %#v err=%v", oldDependencies, err)
	}
	oldAfter, ok, err := repository.GetQueryJob(ctx, old.ID)
	if err != nil || !ok || oldAfter.Owner.RunID != oldRun.ID {
		t.Fatalf("old job ownership changed: %#v ok=%v err=%v", oldAfter, ok, err)
	}

	unattached := old
	unattached.ID = "qj-unattached-foreign-terminal"
	unattached.Owner.Handle = "qown-unattached-foreign"
	if _, err := repository.UpsertQueryJobCAS(ctx, unattached, 0); err != nil {
		t.Fatalf("persist unattached old job: %v", err)
	}
	unattachedReplacement := replacement
	unattachedReplacement.ID = "qj-unattached-foreign-replacement"
	unattachedReplacement.Owner.Handle = "qown-unattached-replacement"
	unattachedReplacement.ReplacesJobID = unattached.ID
	if _, err := repository.UpsertQueryJobCAS(ctx, unattachedReplacement, 0); err != nil {
		t.Fatalf("persist unattached replacement: %v", err)
	}
	if err := manager.persistRunQueryDependency(ctx, currentRun.ID, unattachedReplacement.ID, unattached.ID); err == nil {
		t.Fatal("unattached foreign-run target unexpectedly passed the manager ownership gate")
	}
}

func TestPersistRunQueryDependencyAttachesProfileRejectedDurableJob(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID: "qj-profile-invalid", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-profile"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now,
		ValidationState: jobs.ValidationUnvalidated,
		Profile:         &jobs.QueryProfile{Valid: false, Error: "unknown column"},
		ProfileOutcome:  jobs.ProfileOutcomeInvalid, ProfileDispatched: true,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("UpsertQueryJobCAS: %v", err)
	}
	manager := &Manager{longHorizon: repository}
	if err := manager.persistRunQueryDependency(ctx, run.ID, record.ID, ""); err != nil {
		t.Fatalf("persistRunQueryDependency: %v", err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if dependencies[0].JobID != record.ID || dependencies[0].ValidationState != store.QueryValidationRejected || !dependencies[0].RequiredForCompletion {
		t.Fatalf("dependency=%#v, want exact durable rejected job", dependencies[0])
	}
}

func TestPersistRunQueryDependencyPreservesFailedReplacementLink(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	old := jobs.JobRecord{
		ID: "qj-old-invalid", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-old"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now,
		ValidationState: jobs.ValidationRejected, ProfileOutcome: jobs.ProfileOutcomeInvalid,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, old, 0); err != nil {
		t.Fatalf("persist old job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: run.ID, JobID: old.ID, RequiredForCompletion: true, ValidationState: store.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("persist old dependency: %v", err)
	}
	replacement := jobs.JobRecord{
		ID: "qj-replacement-invalid", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-new"},
		ReplacesJobID: old.ID, SubmittedAt: now.Add(time.Second), LastAccess: now.Add(time.Second), CompletedAt: &now,
		ValidationState: jobs.ValidationUnvalidated, ProfileOutcome: jobs.ProfileOutcomeInvalid,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, replacement, 0); err != nil {
		t.Fatalf("persist replacement job: %v", err)
	}
	manager := &Manager{longHorizon: repository}
	if err := manager.persistRunQueryDependency(ctx, run.ID, replacement.ID, old.ID); err != nil {
		t.Fatalf("persist replacement dependency: %v", err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 2 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	byID := map[string]store.RunQueryDependency{}
	for _, dependency := range dependencies {
		byID[dependency.JobID] = dependency
	}
	if !byID[old.ID].Detached || byID[old.ID].ReplacementJobID != replacement.ID {
		t.Fatalf("old dependency=%#v, want detached and linked", byID[old.ID])
	}
	if byID[replacement.ID].ValidationState != store.QueryValidationRejected || !byID[replacement.ID].RequiredForCompletion {
		t.Fatalf("replacement dependency=%#v, want required rejected replacement", byID[replacement.ID])
	}
}
