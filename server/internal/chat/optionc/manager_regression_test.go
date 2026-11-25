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
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/rlm"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/secrets"
)

func TestToUIMessageIncludesFileAttachments(t *testing.T) {
	content := `{"artifact_id":"tool_artifact","tool_name":"jira_read_attachment","tool_call_id":"call_1","ok":true,"result_format":"json","result":{"attachment_artifact_id":"file_artifact"}}`
	msg := types.ChatMessage{
		ID:         "msg_tool",
		Role:       types.RoleTool,
		Content:    &content,
		ToolCallID: "call_1",
		Name:       "jira_read_attachment",
		FileAttachments: []types.FileAttachment{{
			ArtifactID:       "file_artifact",
			FileName:         "report.xlsx",
			MimeType:         "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			FileKind:         "spreadsheet",
			Source:           externalToolFileArtifactSource,
			InlineDataBase64: "d29ya2Jvb2s=",
			PreviewAvailable: boolPtr(true),
		}},
	}

	ui := toUIMessage(msg)
	rawAttachments, ok := ui["file_attachments"].([]types.FileAttachment)
	if !ok || len(rawAttachments) != 1 {
		t.Fatalf("file_attachments=%#v, want one attachment", ui["file_attachments"])
	}
	if rawAttachments[0].ArtifactID != "file_artifact" {
		t.Fatalf("attachment artifact id=%q, want file_artifact", rawAttachments[0].ArtifactID)
	}
	if rawAttachments[0].InlineDataBase64 != "" {
		t.Fatalf("InlineDataBase64 leaked into UI snapshot")
	}

	encoded, err := json.Marshal(ui)
	if err != nil {
		t.Fatalf("Marshal UI message: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("Unmarshal UI message: %v", err)
	}
	attachments, ok := payload["file_attachments"].([]any)
	if !ok || len(attachments) != 1 {
		t.Fatalf("json file_attachments=%#v, want one attachment", payload["file_attachments"])
	}
	attachment, ok := attachments[0].(map[string]any)
	if !ok {
		t.Fatalf("json attachment=%#v, want object", attachments[0])
	}
	if got := attachment["artifactId"]; got != "file_artifact" {
		t.Fatalf("json attachment artifactId=%#v, want file_artifact", got)
	}
	if got := attachment["source"]; got != externalToolFileArtifactSource {
		t.Fatalf("json attachment source=%#v, want %q", got, externalToolFileArtifactSource)
	}
	if _, ok := attachment["inlineDataBase64"]; ok {
		t.Fatalf("json attachment leaked inlineDataBase64: %#v", attachment["inlineDataBase64"])
	}
}

func TestManagerExecToolRLMExecUpdatesPlanFromQueryMetadata(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	manager := &Manager{
		store: st,
		tools: tools.NewRegistry(&tools.RegistryOptions{
			RLM: rlm.NewRuntime(rlm.Options{
				Store: st,
			}),
		}),
		cfgDir: cfgDir,
	}
	plan := RunContextPlan{EvidenceBudget: 2}

	out, err := manager.execTool(
		ctx,
		thread,
		run,
		testProjectID(""),
		types.ToolCall{
			ID:       "tc_rlm_update",
			Name:     "rlm_exec",
			ArgsText: `{"code":"list_messages(\"` + thread.ID + `\")\nlist_messages(\"` + thread.ID + `\")","note":"evidence recovery"}`,
		},
		false,
		"shallow",
		0,
		&plan,
		nil,
		defaultChartEngineEnablement(),
		false,
		false,
		true,
		true,
		false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if plan.EvidenceBudget != 2 {
		t.Fatalf("plan.EvidenceBudget = %d, want 2", plan.EvidenceBudget)
	}
	if plan.EvidenceUsed != 2 {
		t.Fatalf("plan.EvidenceUsed = %d, want 2", plan.EvidenceUsed)
	}
	if !plan.EvidenceExhausted {
		t.Fatalf("plan.EvidenceExhausted = false, want true")
	}
	if out.Role != types.RoleTool {
		t.Fatalf("persisted tool result role = %q, want %q", out.Role, types.RoleTool)
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs)=%d, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.Role != types.RoleTool {
		t.Fatalf("message.Role = %q, want %q", msg.Role, types.RoleTool)
	}
	if msg.ContextSummary == "" {
		t.Fatalf("context summary missing from persisted tool message")
	}
	if len(msg.ContextPlan) == 0 {
		t.Fatalf("context plan missing from persisted tool message")
	}

	var persisted RunContextPlan
	if err := json.Unmarshal(msg.ContextPlan, &persisted); err != nil {
		t.Fatalf("unmarshal context_plan: %v", err)
	}
	if persisted.EvidenceBudget != 2 || persisted.EvidenceUsed != 2 || !persisted.EvidenceExhausted {
		t.Fatalf("persisted plan mismatch: %+v", persisted)
	}
	if len(msg.ReferencedArtifactIDs) == 0 {
		t.Fatalf("no referenced artifacts captured on rlm tool result")
	}
	if !strings.Contains(msg.ContextSummary, "Evidence budget:") {
		t.Fatalf("unexpected context summary: %q", msg.ContextSummary)
	}
	var outEnvelope map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &outEnvelope); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	artifactID, _ := outEnvelope["artifact_id"].(string)
	if strings.TrimSpace(artifactID) == "" {
		t.Fatalf("tool envelope artifact_id missing")
	}
	foundArtifactID := false
	for _, id := range msg.ReferencedArtifactIDs {
		if strings.TrimSpace(id) == artifactID {
			foundArtifactID = true
			break
		}
	}
	if !foundArtifactID {
		t.Fatalf("persisted refs missing tool artifact id: %#v", msg.ReferencedArtifactIDs)
	}
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("tool message content missing")
	}
	if out.Role != types.RoleTool || out.Content == nil || strings.TrimSpace(*out.Content) == "" {
		t.Fatalf("tool output envelope missing or malformed")
	}
}

func TestManagerUpdatePlanFromToolResultParsesMetadataAndExhaustion(t *testing.T) {
	manager := &Manager{}
	plan := RunContextPlan{EvidenceBudget: 2}
	out := tools.Output{
		QueryMetadata: map[string]any{
			"evidence_budget":           float64(4),
			"evidence_used":             float64(5),
			"evidence_budget_exhausted": true,
		},
	}
	manager.updatePlanFromToolResult(&plan, out, "rlm_exec")
	if plan.EvidenceBudget != 4 {
		t.Fatalf("plan.EvidenceBudget=%d, want 4", plan.EvidenceBudget)
	}
	if plan.EvidenceUsed != 5 {
		t.Fatalf("plan.EvidenceUsed=%d, want 5", plan.EvidenceUsed)
	}
	if !plan.EvidenceExhausted {
		t.Fatalf("plan.EvidenceExhausted=false, want true")
	}
	manager.updatePlanFromToolResult(&plan, tools.Output{}, "search_docs")
	if plan.EvidenceBudget != 4 || plan.EvidenceUsed != 5 {
		t.Fatalf("unsupported tool should not update evidence fields: %+v", plan)
	}
	if !plan.EvidenceExhausted {
		t.Fatalf("unsupported tool should not clear EvidenceExhausted")
	}
}

func TestManagerUpdatePlanFromToolResultParsesHeterogeneousNumericTypes(t *testing.T) {
	manager := &Manager{}
	plan := RunContextPlan{EvidenceBudget: 2}
	out := tools.Output{
		QueryMetadata: map[string]any{
			"evidence_budget":           int64(8),
			"evidence_used":             float32(1.9),
			"evidence_budget_exhausted": true,
		},
	}
	manager.updatePlanFromToolResult(&plan, out, "rlm_exec")

	if plan.EvidenceBudget != 8 {
		t.Fatalf("plan.EvidenceBudget=%d, want 8", plan.EvidenceBudget)
	}
	if plan.EvidenceUsed != 1 {
		t.Fatalf("plan.EvidenceUsed=%d, want 1", plan.EvidenceUsed)
	}
	if !plan.EvidenceExhausted {
		t.Fatalf("plan.EvidenceExhausted=false, want true")
	}
}

func TestManagerRunContextUpdateFieldsTelemetry(t *testing.T) {
	manager := &Manager{}
	fields := manager.runContextUpdateFields(RunContextPlan{
		TokensBefore:         1000,
		TokensAfter:          400,
		SummaryRounds:        2,
		ArtifactRefsLoaded:   5,
		EvidenceBudget:       7,
		EvidenceUsed:         3,
		EvidenceExhausted:    true,
		NeedRetrieval:        true,
		NeedRetrievalTooling: true,
		HardCapped:           true,
		OverflowCompacted:    true,
		SummarizedBuckets:    make([]ContextSummaryBucket, 0),
		RetrievalHints:       nil,
		PlanReason:           "runtime_context_overflow",
	})

	if fields["context_summary_buckets"] != 0 {
		t.Fatalf("context_summary_buckets = %#v, want 0", fields["context_summary_buckets"])
	}
	if fields["context_summary_rounds"] != 2 {
		t.Fatalf("context_summary_rounds = %#v, want 2", fields["context_summary_rounds"])
	}
	if fields["context_artifact_refs_loaded"] != 5 {
		t.Fatalf("context_artifact_refs_loaded = %#v, want 5", fields["context_artifact_refs_loaded"])
	}
	if fields["context_evidence_budget"] != 7 {
		t.Fatalf("context_evidence_budget = %#v, want 7", fields["context_evidence_budget"])
	}
	if fields["context_evidence_used"] != 3 {
		t.Fatalf("context_evidence_used = %#v, want 3", fields["context_evidence_used"])
	}
	if fields["context_evidence_exhausted"] != true {
		t.Fatalf("context_evidence_exhausted = %#v, want true", fields["context_evidence_exhausted"])
	}
	if fields["context_need_retrieval"] != true {
		t.Fatalf("context_need_retrieval = %#v, want true", fields["context_need_retrieval"])
	}
	if fields["context_need_retrieval_tooling"] != true {
		t.Fatalf("context_need_retrieval_tooling = %#v, want true", fields["context_need_retrieval_tooling"])
	}
	if fields["context_hard_capped"] != true {
		t.Fatalf("context_hard_capped = %#v, want true", fields["context_hard_capped"])
	}
	if fields["context_overflow_compacted"] != true {
		t.Fatalf("context_overflow_compacted = %#v, want true", fields["context_overflow_compacted"])
	}
	rawPlan, ok := fields["context_plan"].(json.RawMessage)
	if !ok {
		t.Fatalf("context_plan is missing or invalid type: %#v", fields["context_plan"])
	}
	var roundTrip RunContextPlan
	if err := json.Unmarshal(rawPlan, &roundTrip); err != nil {
		t.Fatalf("unmarshal context_plan: %v", err)
	}
	if roundTrip.NeedRetrieval != true || roundTrip.EvidenceBudget != 7 {
		t.Fatalf("round-tripped context plan mismatch: %+v", roundTrip)
	}
}

func TestManagerStartRunCompactsOnHardCapWithoutFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	manager.contextPlanner = NewContextPlanner(nil)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	large := strings.Repeat("x", 70000)
	for i := 0; i < 12; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			Role:    role,
			Content: &large,
		}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "compact this context",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}
	if run.ContextOverflowCompacted == nil || !*run.ContextOverflowCompacted {
		t.Fatalf("context_overflow_compacted = %#v, want true", run.ContextOverflowCompacted)
	}
	if run.ContextHardCapped == nil || !*run.ContextHardCapped {
		t.Fatalf("context_hard_capped = %#v, want true", run.ContextHardCapped)
	}
	if run.ContextTokensAfter == nil || *run.ContextTokensAfter > defaultHardContextTokenCap {
		t.Fatalf("context_tokens_after=%#v, want <= %d", run.ContextTokensAfter, defaultHardContextTokenCap)
	}
	if run.ContextPlanJSON == nil {
		t.Fatalf("run context_plan missing")
	}
	var persisted RunContextPlan
	if err := json.Unmarshal(run.ContextPlanJSON, &persisted); err != nil {
		t.Fatalf("unmarshal run context_plan: %v", err)
	}
	if !persisted.HardCapped || !persisted.OverflowCompacted {
		t.Fatalf("run context plan mismatch: %#v", persisted)
	}
	if !persisted.NeedRetrieval || !persisted.NeedRetrievalTooling {
		t.Fatalf("expected retrieval tooling to remain enabled, got %#v", persisted)
	}
	if persisted.CompactionMode != handoverCompactionMode {
		t.Fatalf("CompactionMode=%q, want %q", persisted.CompactionMode, handoverCompactionMode)
	}
	if persisted.PlanReason != handoverCompactionMode {
		t.Fatalf("PlanReason=%q, want %q", persisted.PlanReason, handoverCompactionMode)
	}
	if !persisted.HandoverSummaryUsed || persisted.HandoverSummaryChars <= 0 {
		t.Fatalf("handover summary telemetry missing: %#v", persisted)
	}
	if run.ErrorCode != nil {
		t.Fatalf("run error_code = %#v, want nil", run.ErrorCode)
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    64,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	foundRunUser := false
	for _, m := range msgs {
		if m.Role != types.RoleUser || m.RunID == nil || strings.TrimSpace(*m.RunID) != result.Run.ID {
			continue
		}
		foundRunUser = true
		if strings.TrimSpace(m.ContextSummary) == "" {
			t.Fatalf("run user message context_summary is empty")
		}
		if len(m.ContextPlan) == 0 {
			t.Fatalf("run user message context_plan missing")
		}
		var msgPlan RunContextPlan
		if err := json.Unmarshal(m.ContextPlan, &msgPlan); err != nil {
			t.Fatalf("unmarshal user message context_plan: %v", err)
		}
		if msgPlan.CompactionMode != handoverCompactionMode || msgPlan.PlanReason != handoverCompactionMode {
			t.Fatalf("user message plan missing handover mode: %#v", msgPlan)
		}
		if !msgPlan.NeedRetrieval || !msgPlan.NeedRetrievalTooling {
			t.Fatalf("user message plan missing retrieval flags: %#v", msgPlan)
		}
		break
	}
	if !foundRunUser {
		t.Fatalf("failed to locate run user message for run_id=%s", result.Run.ID)
	}
}

func TestManagerStartRunUsesActiveCheckpointPlusDeltaMessages(t *testing.T) {
	t.Parallel()

	var (
		mu               sync.Mutex
		capturedMessages []any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := body["messages"].([]any)
		mu.Lock()
		capturedMessages = append([]any(nil), raw...)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	oldRaw := "OLD_RAW_TRANSCRIPT_SHOULD_NOT_BE_REPLAYED"
	oldMsg, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role:    types.RoleUser,
		Content: &oldRaw,
	})
	if err != nil {
		t.Fatalf("AppendMessage old user: %v", err)
	}
	oldAssistant := "old assistant"
	oldAssistantMsg, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
		Role:    types.RoleAssistant,
		Content: &oldAssistant,
	})
	if err != nil {
		t.Fatalf("AppendMessage old assistant: %v", err)
	}

	checkpointSummary := "ACTIVE_CHECKPOINT_SUMMARY_MARKER"
	if _, err := manager.persistActiveContextCheckpoint(ctx, thread, store.Run{}, managerContextCheckpoint{
		ThreadID:           thread.ID,
		SourceMessageSeq:   oldAssistantMsg.Seq,
		SourceMessageID:    oldAssistantMsg.ID,
		ReplacementHistory: []types.ChatMessage{{Role: types.RoleAssistant, Content: &checkpointSummary}},
		Summary:            checkpointSummary,
		SummaryMessageIDs:  []string{oldMsg.ID, oldAssistantMsg.ID},
		TokenEstimate:      estimateStringTokenCount(checkpointSummary),
		CharsPerToken:      defaultTokenCharsPerToken,
		Plan: RunContextPlan{
			PlanReason:           "test_checkpoint",
			OverflowCompacted:    true,
			NeedRetrieval:        true,
			NeedRetrievalTooling: true,
		},
	}); err != nil {
		t.Fatalf("persistActiveContextCheckpoint: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "CURRENT_DELTA_USER_MARKER",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}

	mu.Lock()
	requestMessages := append([]any(nil), capturedMessages...)
	mu.Unlock()
	if !requestPayloadContainsMarker(requestMessages, checkpointSummary) {
		t.Fatalf("provider request missing active checkpoint replacement history")
	}
	if !requestPayloadContainsMarker(requestMessages, "CURRENT_DELTA_USER_MARKER") {
		t.Fatalf("provider request missing delta/current user message")
	}
	if requestPayloadContainsMarker(requestMessages, oldRaw) {
		t.Fatalf("provider request replayed raw messages older than checkpoint")
	}
}

func TestManagerStartRunReleasesThreadReservationOnProviderValidationError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	invalid := manager.settings.Get()
	invalid.Provider.Model = ""
	if err := manager.settings.Save(invalid); err != nil {
		t.Fatalf("settings.Save invalid: %v", err)
	}

	if _, err := manager.StartRun(ctx, StartRunRequest{ThreadID: thread.ID, Content: "first"}); err == nil || !strings.Contains(err.Error(), "no provider model is configured") {
		t.Fatalf("StartRun invalid provider err = %v, want no provider model is configured", err)
	}

	manager.mu.Lock()
	_, reserved := manager.activeByThread[thread.ID]
	manager.mu.Unlock()
	if reserved {
		t.Fatalf("thread reservation was not released after provider validation error")
	}

	valid := manager.settings.Get()
	valid.Provider.ProviderID = "compatible"
	if strings.TrimSpace(valid.Provider.Model) == "" {
		valid.Provider.Model = "test-model"
	}
	if err := manager.settings.Save(valid); err != nil {
		t.Fatalf("settings.Save valid: %v", err)
	}

	second, err := manager.StartRun(ctx, StartRunRequest{ThreadID: thread.ID, Content: "second"})
	if err != nil {
		t.Fatalf("StartRun second attempt: %v", err)
	}
	if _, err := waitForRunTerminalState(ctx, st, second.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState second attempt: %v", err)
	}
}

func TestManagerStartRunReturnsActiveRunConflictError(t *testing.T) {
	t.Parallel()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, "http://127.0.0.1:1")
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	const activeRunID = "run-active-123"
	manager.mu.Lock()
	manager.activeByThread[thread.ID] = activeRunID
	manager.mu.Unlock()

	_, err = manager.StartRun(ctx, StartRunRequest{ThreadID: thread.ID, Content: "next"})
	if err == nil {
		t.Fatalf("StartRun err = nil, want conflict")
	}
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("StartRun err = %v, want store.ErrConflict", err)
	}
	var conflictErr *ActiveRunConflictError
	if !errors.As(err, &conflictErr) {
		t.Fatalf("StartRun err = %T, want *ActiveRunConflictError", err)
	}
	if got := conflictErr.ActiveRunID(); got != activeRunID {
		t.Fatalf("ActiveRunID = %q, want %q", got, activeRunID)
	}
}

func TestManagerStartRunAnthropicCompactionGuardrailUsesHandoverHistory(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var capturedMessages []any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if raw, ok := body["messages"].([]any); ok {
			capturedMessages = append([]any(nil), raw...)
		} else {
			capturedMessages = nil
		}
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0,\"total_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newAnthropicStartRunManagerForSSE(t, cfgDir, srv.URL, 70000)
	// Keep planner deterministic for this regression.
	manager.contextPlanner = NewContextPlanner(nil)

	ctx := context.Background()
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	large := strings.Repeat("x", 30000)
	for i := 0; i < 10; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			Role:    role,
			Content: &large,
		}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "what should I do next?",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}
	if run.ContextPlanJSON == nil {
		t.Fatalf("run context_plan missing")
	}
	var persisted RunContextPlan
	if err := json.Unmarshal(run.ContextPlanJSON, &persisted); err != nil {
		t.Fatalf("unmarshal run context_plan: %v", err)
	}
	if persisted.CompactionMode != handoverCompactionMode {
		t.Fatalf("CompactionMode=%q, want %q", persisted.CompactionMode, handoverCompactionMode)
	}

	mu.Lock()
	msgCount := len(capturedMessages)
	firstRole := ""
	secondRole := ""
	if msgCount > 0 {
		if first, ok := capturedMessages[0].(map[string]any); ok {
			firstRole, _ = first["role"].(string)
		}
	}
	if msgCount > 1 {
		if second, ok := capturedMessages[1].(map[string]any); ok {
			secondRole, _ = second["role"].(string)
		}
	}
	mu.Unlock()

	if msgCount != 2 {
		t.Fatalf("expected compact handover history of 2 messages, got %d", msgCount)
	}
	if firstRole != "assistant" || secondRole != "user" {
		t.Fatalf("unexpected compact message roles: first=%q second=%q", firstRole, secondRole)
	}
}

func TestCompactionPlanningTokenCap(t *testing.T) {
	t.Parallel()

	settings := config.DefaultSettings()

	if cap, ok := compactionPlanningTokenCap(settings, "anthropic"); !ok || cap != 170000 {
		t.Fatalf("anthropic cap = (%d, %t), want (170000, true)", cap, ok)
	}

	if settings.Anthropic == nil {
		t.Fatalf("default anthropic config missing")
	}
	disabled := false
	settings.Anthropic.CompactionEnabled = &disabled
	if cap, ok := compactionPlanningTokenCap(settings, "anthropic"); ok || cap != 0 {
		t.Fatalf("anthropic disabled cap = (%d, %t), want (0, false)", cap, ok)
	}

	if settings.Bedrock == nil {
		t.Fatalf("default bedrock config missing")
	}
	enabled := true
	settings.Bedrock.CompactionEnabled = &enabled
	trigger := 62000
	settings.Bedrock.CompactionTrigger = &trigger
	if cap, ok := compactionPlanningTokenCap(settings, "bedrock"); !ok || cap != 62000 {
		t.Fatalf("bedrock cap = (%d, %t), want (62000, true)", cap, ok)
	}
}

func TestManagerStartRunSanitizesReferencedIDsAcrossPersistAndUpdate(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	threadA, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread A: %v", err)
	}
	threadB, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread B: %v", err)
	}

	existingMsgText := "existing message"
	validMsg, err := st.AppendMessage(ctx, threadA.ID, nil, store.Message{
		Role:    types.RoleUser,
		Content: &existingMsgText,
	})
	if err != nil {
		t.Fatalf("AppendMessage valid: %v", err)
	}
	otherMsg, err := st.AppendMessage(ctx, threadB.ID, nil, store.Message{
		Role:    types.RoleAssistant,
		Content: &existingMsgText,
	})
	if err != nil {
		t.Fatalf("AppendMessage other thread: %v", err)
	}

	artifactBody := "artifact body"
	validArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    threadA.ID,
		Type:        "code",
		Title:       "valid",
		Storage:     "mysql",
		ContentText: &artifactBody,
		ByteSize:    int64(len(artifactBody)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact valid: %v", err)
	}
	otherArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    threadB.ID,
		Type:        "code",
		Title:       "other",
		Storage:     "mysql",
		ContentText: &artifactBody,
		ByteSize:    int64(len(artifactBody)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact other: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: threadA.ID,
		Content:  "Use references",
		ReferencedArtifactIDs: []string{
			validArtifact.ID,
			otherArtifact.ID,
			"missing-artifact-id",
			validArtifact.ID,
		},
		ReferencedMessageIDs: []string{
			validMsg.ID,
			otherMsg.ID,
			"missing-message-id",
			validMsg.ID,
		},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	findRunUserMessage := func() store.Message {
		msgs, listErr := st.ListMessages(ctx, store.ListMessagesParams{
			ThreadID: threadA.ID,
			Limit:    64,
		})
		if listErr != nil {
			t.Fatalf("ListMessages: %v", listErr)
		}
		for _, msg := range msgs {
			if msg.Role != types.RoleUser || msg.RunID == nil || strings.TrimSpace(*msg.RunID) != result.Run.ID {
				continue
			}
			return msg
		}
		t.Fatalf("run user message not found for run %s", result.Run.ID)
		return store.Message{}
	}

	assertSanitizedRefs := func(msg store.Message) {
		if len(msg.ReferencedArtifactIDs) != 1 || msg.ReferencedArtifactIDs[0] != validArtifact.ID {
			t.Fatalf("referenced artifact ids=%#v, want [%q]", msg.ReferencedArtifactIDs, validArtifact.ID)
		}
		if len(msg.ReferencedMessageIDs) != 1 || msg.ReferencedMessageIDs[0] != validMsg.ID {
			t.Fatalf("referenced message ids=%#v, want [%q]", msg.ReferencedMessageIDs, validMsg.ID)
		}
	}

	initialRunUser := findRunUserMessage()
	assertSanitizedRefs(initialRunUser)

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}

	finalRunUser := findRunUserMessage()
	if finalRunUser.ID != initialRunUser.ID {
		t.Fatalf("run user message id changed: got %q want %q", finalRunUser.ID, initialRunUser.ID)
	}
	assertSanitizedRefs(finalRunUser)
}

func TestManagerStartRunPersistsImageAttachmentsAsArtifacts(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	artifactID := "img-artifact-1"
	dataURI := "data:image/png;base64,AAAA"
	sizeBytes := int64(4)
	width := 1
	height := 1
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "Describe this image",
		ImageAttachments: []types.ImageAttachment{
			{
				ArtifactID: artifactID,
				FileName:   "diagram.png",
				MimeType:   "image/png",
				SizeBytes:  &sizeBytes,
				DataURI:    dataURI,
				Width:      &width,
				Height:     &height,
				Source:     "upload",
			},
		},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    64,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}

	var runUserMessage store.Message
	foundRunUserMessage := false
	for i := range msgs {
		msg := msgs[i]
		if msg.Role != types.RoleUser || msg.RunID == nil || strings.TrimSpace(*msg.RunID) != result.Run.ID {
			continue
		}
		runUserMessage = msg
		foundRunUserMessage = true
		break
	}
	if !foundRunUserMessage {
		t.Fatalf("run user message not found for run %s", result.Run.ID)
	}
	if len(runUserMessage.ReferencedArtifactIDs) != 1 || runUserMessage.ReferencedArtifactIDs[0] != artifactID {
		t.Fatalf("referenced_artifact_ids=%#v, want [%q]", runUserMessage.ReferencedArtifactIDs, artifactID)
	}

	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.Type != "image" {
		t.Fatalf("artifact.Type=%q, want image", artifact.Type)
	}
	if artifact.Title != "diagram.png" {
		t.Fatalf("artifact.Title=%q, want diagram.png", artifact.Title)
	}
	if artifact.ToolName == nil || strings.TrimSpace(*artifact.ToolName) != "image_attachment" {
		t.Fatalf("artifact.ToolName=%v, want image_attachment", artifact.ToolName)
	}

	var persistedContent string
	switch strings.TrimSpace(artifact.Storage) {
	case "mysql":
		if artifact.ContentText == nil {
			t.Fatalf("artifact.ContentText is nil for mysql storage")
		}
		persistedContent = *artifact.ContentText
	case "file":
		if artifact.ContentPath == nil || strings.TrimSpace(*artifact.ContentPath) == "" {
			t.Fatalf("artifact.ContentPath missing for file storage")
		}
		fullPath := filepath.Join(cfgDir, filepath.FromSlash(strings.TrimSpace(*artifact.ContentPath)))
		b, readErr := os.ReadFile(fullPath)
		if readErr != nil {
			t.Fatalf("ReadFile(%s): %v", fullPath, readErr)
		}
		persistedContent = string(b)
	default:
		t.Fatalf("unexpected artifact storage %q", artifact.Storage)
	}
	if strings.TrimSpace(persistedContent) != dataURI {
		t.Fatalf("artifact content mismatch: got %q", persistedContent)
	}

	if artifact.Metadata == nil {
		t.Fatalf("artifact.Metadata is nil")
	}
	if mimeType, _ := artifact.Metadata["mimeType"].(string); mimeType != "image/png" {
		t.Fatalf("artifact metadata mimeType=%v, want image/png", artifact.Metadata["mimeType"])
	}
	if source, _ := artifact.Metadata["source"].(string); source != "upload" {
		t.Fatalf("artifact metadata source=%v, want upload", artifact.Metadata["source"])
	}
}

func TestEstimateRequestTokensWithToolsUsesNativeImageCost(t *testing.T) {
	t.Parallel()

	largeDataURI := "data:image/png;base64," + strings.Repeat("A", 120000)
	content := "what is in this image?"
	history := []types.ChatMessage{
		{
			Role:    types.RoleUser,
			Content: &content,
			ImageAttachments: []types.ImageAttachment{
				{
					FileName: "large.png",
					MimeType: "image/png",
					DataURI:  largeDataURI,
				},
			},
		},
	}

	got := estimateRequestTokensWithTools("system", history, nil, 4)
	if got > nativeImageEstimateTokens+200 {
		t.Fatalf("token estimate=%d, want native image placeholder cost rather than base64 text", got)
	}
	if got < nativeImageEstimateTokens {
		t.Fatalf("token estimate=%d, want at least native image placeholder cost", got)
	}
}

func TestManagerHydrateReferencedImageAttachments(t *testing.T) {
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

	imageData := "data:image/png;base64,AAAA"
	imageArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "image",
		Title:       "example.png",
		Storage:     "mysql",
		ContentText: &imageData,
		ByteSize:    int64(len(imageData)),
		Metadata: map[string]any{
			"mimeType":  "image/png",
			"width":     1200,
			"height":    800,
			"source":    "upload",
			"sizeBytes": 4096,
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact image: %v", err)
	}

	codeData := "package main"
	codeArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "main.go",
		Storage:     "mysql",
		ContentText: &codeData,
		ByteSize:    int64(len(codeData)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact code: %v", err)
	}

	fileRelPath := "uploads/photo.png"
	fileFullPath := filepath.Join(cfgDir, filepath.FromSlash(fileRelPath))
	if err := os.MkdirAll(filepath.Dir(fileFullPath), 0o700); err != nil {
		t.Fatalf("MkdirAll file artifact dir: %v", err)
	}
	fileBytes := []byte("png bytes")
	if err := os.WriteFile(fileFullPath, fileBytes, 0o600); err != nil {
		t.Fatalf("WriteFile file artifact: %v", err)
	}
	fileArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "photo.png",
		Storage:     "file",
		ContentPath: &fileRelPath,
		ByteSize:    int64(len(fileBytes)),
		Metadata: map[string]any{
			"mimeType":  "image/png",
			"source":    "chat_upload",
			"sizeBytes": len(fileBytes),
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact file image: %v", err)
	}

	manager := &Manager{
		store:  st,
		cfgDir: cfgDir,
	}
	attachments := manager.hydrateReferencedImageAttachments(ctx, thread.ID, []string{codeArtifact.ID, imageArtifact.ID, fileArtifact.ID}, nil)
	if len(attachments) != 2 {
		t.Fatalf("len(attachments)=%d, want 2", len(attachments))
	}
	attachment := attachments[0]
	if attachment.ArtifactID != imageArtifact.ID {
		t.Fatalf("ArtifactID=%q, want %q", attachment.ArtifactID, imageArtifact.ID)
	}
	if attachment.DataURI != imageData {
		t.Fatalf("DataURI=%q, want %q", attachment.DataURI, imageData)
	}
	if attachment.FileName != "example.png" {
		t.Fatalf("FileName=%q, want example.png", attachment.FileName)
	}
	if attachment.MimeType != "image/png" {
		t.Fatalf("MimeType=%q, want image/png", attachment.MimeType)
	}
	if attachment.Width == nil || *attachment.Width != 1200 {
		t.Fatalf("Width=%v, want 1200", attachment.Width)
	}
	if attachment.Height == nil || *attachment.Height != 800 {
		t.Fatalf("Height=%v, want 800", attachment.Height)
	}
	if attachment.SizeBytes == nil || *attachment.SizeBytes != 4096 {
		t.Fatalf("SizeBytes=%v, want 4096", attachment.SizeBytes)
	}
	if attachment.Source != "upload" {
		t.Fatalf("Source=%q, want upload", attachment.Source)
	}
	fileAttachment := attachments[1]
	if fileAttachment.ArtifactID != fileArtifact.ID {
		t.Fatalf("file ArtifactID=%q, want %q", fileAttachment.ArtifactID, fileArtifact.ID)
	}
	if !strings.HasPrefix(fileAttachment.DataURI, "data:image/png;base64,") {
		t.Fatalf("file DataURI=%q, want PNG data URI", fileAttachment.DataURI)
	}
	if fileAttachment.MimeType != "image/png" {
		t.Fatalf("file MimeType=%q, want image/png", fileAttachment.MimeType)
	}
	if fileAttachment.SizeBytes == nil || *fileAttachment.SizeBytes != int64(len(fileBytes)) {
		t.Fatalf("file SizeBytes=%v, want %d", fileAttachment.SizeBytes, len(fileBytes))
	}
}

func TestManagerStartRunTreatsUploadedImageFileAsNativeImageInput(t *testing.T) {
	t.Parallel()

	var (
		mu               sync.Mutex
		capturedMessages []any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := body["messages"].([]any)
		mu.Lock()
		capturedMessages = append([]any(nil), raw...)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	fileRelPath := filepath.ToSlash(filepath.Join("thread_uploads", thread.ID, "uploaded.png"))
	fileFullPath := filepath.Join(cfgDir, filepath.FromSlash(fileRelPath))
	if err := os.MkdirAll(filepath.Dir(fileFullPath), 0o700); err != nil {
		t.Fatalf("MkdirAll file artifact dir: %v", err)
	}
	fileBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(fileFullPath, fileBytes, 0o600); err != nil {
		t.Fatalf("WriteFile file artifact: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	fileArtifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "uploaded.png",
		ToolName:    &toolName,
		Storage:     "file",
		ContentPath: &fileRelPath,
		ByteSize:    int64(len(fileBytes)),
		Metadata: map[string]any{
			"mimeType":         "image/png",
			"detectedMimeType": "image/png",
			"fileKind":         "image",
			"source":           types.ChatUploadAttachmentSource,
			"previewAvailable": true,
			"previewText":      "image metadata fallback should not be sent",
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact file image: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "describe the uploaded image",
		FileAttachments: []types.FileAttachment{{
			ArtifactID: fileArtifact.ID,
		}},
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}

	mu.Lock()
	requestMessages := append([]any(nil), capturedMessages...)
	mu.Unlock()
	raw, err := json.Marshal(requestMessages)
	if err != nil {
		t.Fatalf("marshal request messages: %v", err)
	}
	serialized := string(raw)
	if !strings.Contains(serialized, `"type":"image_url"`) || !strings.Contains(serialized, "data:image/png;base64,") {
		t.Fatalf("provider request did not include native image input: %s", serialized)
	}
	if !strings.Contains(serialized, "native_image_input=true") {
		t.Fatalf("provider request missing native image context marker: %s", serialized)
	}
	if strings.Contains(serialized, "image metadata fallback should not be sent") {
		t.Fatalf("provider request included file preview despite native image input: %s", serialized)
	}
}

func TestManagerStartRunAssistantFinalOmitsAlreadyStreamedContent(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"table\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" table table table\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "Repeat table",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	sawTextDelta := false
	sawAssistantFinal := false
	assistantFinalID := ""
	assistantFinalSeq := int64(0)
	for ev := range ch {
		switch ev.Event {
		case "text_delta":
			sawTextDelta = true
		case "assistant_final":
			sawAssistantFinal = true
			var payload map[string]any
			if err := json.Unmarshal(ev.Data, &payload); err != nil {
				t.Fatalf("assistant_final payload decode: %v", err)
			}
			msg, ok := payload["message"].(map[string]any)
			if !ok {
				t.Fatalf("assistant_final message missing: %#v", payload)
			}
			content, ok := msg["content"]
			if !ok {
				t.Fatalf("assistant_final message.content missing: %#v", msg)
			}
			if content != nil {
				t.Fatalf("assistant_final content=%#v, want nil when text_delta already streamed", content)
			}
			id, ok := msg["id"].(string)
			if !ok || strings.TrimSpace(id) == "" {
				t.Fatalf("assistant_final message.id missing: %#v", msg)
			}
			assistantFinalID = strings.TrimSpace(id)
			seqRaw, ok := msg["seq"].(float64)
			if !ok || int64(seqRaw) <= 0 {
				t.Fatalf("assistant_final message.seq missing or invalid: %#v", msg["seq"])
			}
			assistantFinalSeq = int64(seqRaw)
		}
	}
	if !sawTextDelta {
		t.Fatalf("expected at least one text_delta event")
	}
	if !sawAssistantFinal {
		t.Fatalf("expected assistant_final event")
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	assistantText := ""
	storedAssistantID := ""
	storedAssistantSeq := int64(0)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != types.RoleAssistant || msgs[i].Content == nil {
			continue
		}
		assistantText = *msgs[i].Content
		storedAssistantID = strings.TrimSpace(msgs[i].ID)
		storedAssistantSeq = msgs[i].Seq
		break
	}
	if strings.TrimSpace(assistantText) != "table table table table" {
		t.Fatalf("stored assistant text = %q, want %q", assistantText, "table table table table")
	}
	if storedAssistantID == "" {
		t.Fatalf("stored assistant id missing")
	}
	if storedAssistantSeq <= 0 {
		t.Fatalf("stored assistant seq=%d, want > 0", storedAssistantSeq)
	}
	if assistantFinalID != storedAssistantID {
		t.Fatalf("assistant_final id=%q, want stored id %q", assistantFinalID, storedAssistantID)
	}
	if assistantFinalSeq != storedAssistantSeq {
		t.Fatalf("assistant_final seq=%d, want stored seq %d", assistantFinalSeq, storedAssistantSeq)
	}
}

func TestManagerStartRunWaitsForApprovalBeforeFinal(t *testing.T) {
	var mu sync.Mutex
	requestCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		mu.Lock()
		requestCount++
		n := requestCount
		mu.Unlock()

		if n == 1 {
			_, _ = w.Write([]byte("data: {\"id\":\"req_approval_1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_clone_1\",\"function\":{\"name\":\"github_clone_repository\",\"arguments\":\"{\\\"repository\\\":\\\"owner/repo\\\"}\"}}]}}]}\n\n"))
		} else {
			_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"approval handled\"}}]}\n\n"))
		}
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	manager.permissions = permissionSvc
	manager.tools = tools.NewRegistry(&tools.RegistryOptions{GitHub: struct{}{}})
	settings := manager.settings.Get()
	enabled := true
	settings.GitHub.Enabled = &enabled
	settings.GitHub.ReadTools = &enabled
	if err := manager.settings.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "Cancel the query",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	var approvalID string
	deadline := time.After(2 * time.Second)
	for approvalID == "" {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("SSE subscription closed before approval")
			}
			if ev.Event == "final" {
				t.Fatalf("saw %s before approval was resolved", ev.Event)
			}
			if ev.Event != "approval" {
				continue
			}
			var payload struct {
				Approval struct {
					ID     string `json:"id"`
					Status string `json:"status"`
				} `json:"approval"`
			}
			if err := json.Unmarshal(ev.Data, &payload); err != nil {
				t.Fatalf("approval payload decode: %v", err)
			}
			if payload.Approval.Status == "pending" {
				approvalID = strings.TrimSpace(payload.Approval.ID)
			}
		case <-deadline:
			t.Fatal("timed out waiting for approval event")
		}
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: result.Run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != approvalID {
		t.Fatalf("pending approvals=%#v, want only %s", pending, approvalID)
	}

	noFinalTimer := time.After(75 * time.Millisecond)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("SSE subscription closed while approval was pending")
			}
			if ev.Event == "final" {
				t.Fatalf("saw %s while approval %s was pending", ev.Event, approvalID)
			}
		case <-noFinalTimer:
			goto rejectApproval
		}
	}

rejectApproval:
	if _, err := permissionSvc.Reject(ctx, approvalID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	sawFinal := false
	for ev := range ch {
		if ev.Event == "final" {
			sawFinal = true
			break
		}
	}
	if !sawFinal {
		t.Fatal("subscription closed without final after approval rejection")
	}
	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run status=%q, want completed", run.Status)
	}
}

func TestManagerStartRunTextDeltaPreservesWhitespaceOnlyChunks(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\" \"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"world\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "Say hello world",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	streamed := strings.Builder{}
	for ev := range ch {
		if ev.Event != "text_delta" {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("text_delta payload decode: %v", err)
		}
		content, _ := payload["content"].(string)
		streamed.WriteString(content)
	}

	if streamed.String() != "Hello world" {
		t.Fatalf("streamed text=%q, want %q", streamed.String(), "Hello world")
	}
}

func TestManagerStartRunEnsuresThinkingEndBeforeErrorWhenProviderOmitsIt(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_thinking_error\"}}\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"working\"}}\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: error\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"error\",\"error\":{\"message\":\"forced stream failure\"}}\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newAnthropicStartRunManagerForSSE(t, cfgDir, srv.URL, 170000)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "trigger thinking error path",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if _, err := waitForRunTerminalState(ctx, st, result.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	events := collectEventNames(ch)
	thinkingEndIdx := firstEventIndex(events, "thinking_end")
	errorIdx := firstEventIndex(events, "error")
	if thinkingEndIdx < 0 {
		t.Fatalf("events missing thinking_end: %#v", events)
	}
	if errorIdx < 0 {
		t.Fatalf("events missing error: %#v", events)
	}
	if thinkingEndIdx > errorIdx {
		t.Fatalf("thinking_end must precede error; events=%#v", events)
	}
}

func TestManagerStartRunEnsuresThinkingEndBeforeFinalOnCancel(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_cancel\"}}\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"still working\"}}\n\n"))
		f.Flush()

		<-r.Context().Done()
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newAnthropicStartRunManagerForSSE(t, cfgDir, srv.URL, 170000)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "cancel while thinking",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	events := make([]string, 0, 32)
	sawThinkingStart := false
	cancelIssued := false
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto DONE
			}
			events = append(events, ev.Event)
			if ev.Event == "thinking_start" {
				sawThinkingStart = true
				if !cancelIssued {
					if err := manager.Cancel(result.Run.ID); err != nil {
						t.Fatalf("Cancel: %v", err)
					}
					cancelIssued = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for cancellation stream events; events=%#v", events)
		}
	}

DONE:
	if !sawThinkingStart {
		t.Fatalf("expected thinking_start before cancellation; events=%#v", events)
	}
	if !cancelIssued {
		t.Fatalf("expected cancel to be issued after thinking_start; events=%#v", events)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run.Status=%q, want cancelled", run.Status)
	}

	thinkingEndIdx := firstEventIndex(events, "thinking_end")
	finalIdx := firstEventIndex(events, "final")
	if thinkingEndIdx < 0 {
		t.Fatalf("events missing thinking_end: %#v", events)
	}
	if finalIdx < 0 {
		t.Fatalf("events missing final: %#v", events)
	}
	if thinkingEndIdx > finalIdx {
		t.Fatalf("thinking_end must precede final on cancel; events=%#v", events)
	}
}

func TestManagerStartRunDoesNotDuplicateThinkingEndWhenProviderAlreadySentIt(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		f, _ := w.(http.Flusher)

		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_done\"}}\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"done\"}}\n\n"))
		f.Flush()

		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		f.Flush()
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newAnthropicStartRunManagerForSSE(t, cfgDir, srv.URL, 170000)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "complete after thinking",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	if _, err := waitForRunTerminalState(ctx, st, result.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	events := collectEventNames(ch)
	if got := countEvent(events, "thinking_end"); got != 1 {
		t.Fatalf("thinking_end count=%d, want 1; events=%#v", got, events)
	}
	thinkingEndIdx := firstEventIndex(events, "thinking_end")
	finalIdx := firstEventIndex(events, "final")
	if thinkingEndIdx < 0 || finalIdx < 0 {
		t.Fatalf("events missing thinking_end/final: %#v", events)
	}
	if thinkingEndIdx > finalIdx {
		t.Fatalf("thinking_end must precede final; events=%#v", events)
	}
}

func TestManagerStartRunOversizedUserInputContinuesWithoutContextOverflow(t *testing.T) {
	t.Parallel()

	var (
		mu               sync.Mutex
		providerCalls    int
		capturedMessages []any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rawMessages, _ := body["messages"].([]any)
		mu.Lock()
		providerCalls++
		capturedMessages = append([]any(nil), rawMessages...)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_oversized\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0,\"total_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"continued\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newAnthropicStartRunManagerForSSE(t, cfgDir, srv.URL, 70000)
	manager.contextPlanner = NewContextPlanner(nil)

	ctx := context.Background()
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	hugeUserMsg := "RAW_OVERSIZED_USER_INPUT_START\n" + strings.Repeat("x", 260000) + "\nRAW_OVERSIZED_USER_INPUT_END"
	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  hugeUserMsg,
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}
	if run.ErrorCode != nil && strings.EqualFold(strings.TrimSpace(*run.ErrorCode), "CONTEXT_OVERFLOW") {
		t.Fatalf("run.ErrorCode = %#v, want no user-facing CONTEXT_OVERFLOW", run.ErrorCode)
	}
	if run.ContextOverflowCompacted == nil || !*run.ContextOverflowCompacted {
		t.Fatalf("context_overflow_compacted = %#v, want true", run.ContextOverflowCompacted)
	}
	if run.ContextPlanJSON == nil {
		t.Fatalf("run context_plan missing")
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()
	for ev := range ch {
		if ev.Event != "error" {
			continue
		}
		if strings.Contains(strings.ToUpper(string(ev.Data)), "CONTEXT_OVERFLOW") ||
			strings.Contains(strings.ToLower(string(ev.Data)), "context exceeded") ||
			strings.Contains(strings.ToLower(string(ev.Data)), "context token budget") {
			t.Fatalf("saw user-facing context overflow SSE error: %s", string(ev.Data))
		}
	}

	mu.Lock()
	calls := providerCalls
	requestMessages := append([]any(nil), capturedMessages...)
	mu.Unlock()
	if calls == 0 {
		t.Fatalf("provider was not called; oversized input should be compacted/previewed and retried")
	}
	if requestPayloadContainsMarker(requestMessages, "RAW_OVERSIZED_USER_INPUT_END") {
		t.Fatalf("provider request included the full oversized user input instead of a bounded preview/retrieval path")
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	foundRaw := false
	for _, msg := range msgs {
		if msg.Role == types.RoleUser && msg.Content != nil && *msg.Content == hugeUserMsg {
			foundRaw = true
			break
		}
	}
	if !foundRaw {
		t.Fatalf("raw oversized user message was not preserved in visible thread history")
	}
}

func TestManagerStartRunRetryReusesCompactedEngineHistory(t *testing.T) {
	t.Parallel()

	const compactedMarker = "retry-compacted-history-marker"

	var (
		mu               sync.Mutex
		capturedMessages = make([][]any, 0, 2)
		requestCount     int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		raw, _ := body["messages"].([]any)
		snapshot := append([]any(nil), raw...)

		mu.Lock()
		capturedMessages = append(capturedMessages, snapshot)
		requestCount = len(capturedMessages)
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		switch requestCount {
		case 1:
			_, _ = w.Write([]byte("data: {\"id\":\"req_1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_retry_1\",\"function\":{\"name\":\"list_files\",\"arguments\":\"{\\\"path\\\":\\\".\\\"}\"}}]}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			_, _ = w.Write([]byte("data: {\"id\":\"req_2\",\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	manager.toolCompaction = &retryMarkerCompactionStrategy{
		marker:         compactedMarker,
		overflowTokens: 1024,
	}
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "retry with compaction",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}

	mu.Lock()
	requests := append([][]any(nil), capturedMessages...)
	mu.Unlock()

	if len(requests) < 2 {
		t.Fatalf("expected at least 2 provider requests, got %d", len(requests))
	}
	if requestPayloadContainsMarker(requests[0], compactedMarker) {
		t.Fatalf("first provider request unexpectedly already contains compacted marker")
	}
	if !requestPayloadContainsMarker(requests[1], compactedMarker) {
		t.Fatalf("second provider request missing compacted marker; compacted history was not reused across retry")
	}
}

func TestManagerStartRunPropagatesLocalToolResultCompactionProgress(t *testing.T) {
	t.Parallel()

	var (
		mu           sync.Mutex
		requestCount int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		mu.Lock()
		requestCount++
		currentRequest := requestCount
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		switch currentRequest {
		case 1:
			_, _ = w.Write([]byte("data: {\"id\":\"req_1\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_local_1\",\"function\":{\"name\":\"list_files\",\"arguments\":\"{\\\"path\\\":\\\".\\\"}\"}},{\"index\":1,\"id\":\"call_local_2\",\"function\":{\"name\":\"list_files\",\"arguments\":\"{\\\"path\\\":\\\"server\\\"}\"}}]}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		default:
			_, _ = w.Write([]byte("data: {\"id\":\"req_2\",\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "list two paths",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	sawStarted := false
	sawCompleted := false
	for ev := range ch {
		if ev.Event != EventLocalProgress {
			continue
		}
		var payload LocalProgressPayload
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("local_progress payload decode: %v", err)
		}
		if payload.Phase != LocalPhaseToolResultCompacting {
			continue
		}
		if payload.ToolCallCount != 2 {
			t.Fatalf("tool_result_compacting tool_call_count=%d, want 2", payload.ToolCallCount)
		}
		switch payload.Status {
		case LocalProgressStatusStarted:
			sawStarted = true
		case LocalProgressStatusCompleted:
			sawCompleted = true
		}
	}
	if !sawStarted || !sawCompleted {
		t.Fatalf("local tool result compaction progress started=%v completed=%v", sawStarted, sawCompleted)
	}
}

func TestManagerStartRunInjectsRuntimeTemporalContextInSystemPrompt(t *testing.T) {
	t.Parallel()

	var (
		mu        sync.Mutex
		messages  []any
		seenCalls int
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rawMessages, _ := body["messages"].([]any)

		mu.Lock()
		messages = append([]any(nil), rawMessages...)
		seenCalls++
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"req_time\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)

	ctx := context.Background()
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "what date is it?",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status = %q, want completed", run.Status)
	}

	mu.Lock()
	snapshot := append([]any(nil), messages...)
	callCount := seenCalls
	mu.Unlock()
	if callCount < 1 {
		t.Fatalf("expected at least one provider request, got %d", callCount)
	}

	foundRuntimeContext := false
	foundTimestampLine := false
	foundRelativeDateRule := false
	for _, raw := range snapshot {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "system" {
			continue
		}
		content, _ := msg["content"].(string)
		if strings.Contains(content, "[RUNTIME CONTEXT - DEVICE CLOCK]") {
			foundRuntimeContext = true
		}
		if strings.Contains(content, "Current timestamp (ISO-8601):") {
			foundTimestampLine = true
		}
		if strings.Contains(content, "Interpret relative dates like today/tomorrow/yesterday") {
			foundRelativeDateRule = true
		}
	}

	if !foundRuntimeContext {
		t.Fatalf("system prompt missing runtime context marker")
	}
	if !foundTimestampLine {
		t.Fatalf("system prompt missing ISO timestamp guidance")
	}
	if !foundRelativeDateRule {
		t.Fatalf("system prompt missing relative-date guidance")
	}
}

func TestManagerPersistToolResultCapturesReferencedArtifactsAndContextPlan(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{
		ID:   "tc_persist_refs",
		Name: "list_messages",
		Args: map[string]any{
			"artifact_ids": []string{"artifact-a", "artifact-a", "artifact-b"},
			"thread_id":    thread.ID,
		},
	}
	out := tools.Output{
		OK:            true,
		ResultFormat:  tools.ResultFormatJSON,
		QueryMetadata: map[string]any{"evidence_budget": 1},
		Result: map[string]any{
			"artifact_ids":       []any{"artifact-c", "artifact-c"},
			"result_artifact_id": "artifact-result",
		},
	}
	plan := RunContextPlan{
		EvidenceBudget:       1,
		EvidenceUsed:         1,
		NeedRetrieval:        true,
		NeedRetrievalTooling: true,
		HardCapped:           true,
		OverflowCompacted:    true,
	}
	msg := manager.persistToolResult(ctx, thread, run, call, out, plan)
	if msg.Role != types.RoleTool {
		t.Fatalf("persisted msg role = %q, want %q", msg.Role, types.RoleTool)
	}
	if strings.TrimSpace(msg.ToolCallID) != call.ID {
		t.Fatalf("persisted tool_call_id=%q, want %q", msg.ToolCallID, call.ID)
	}
	if strings.TrimSpace(msg.ID) == "" {
		t.Fatalf("persistToolResult return id missing")
	}
	if msg.Seq == nil || *msg.Seq <= 0 {
		t.Fatalf("persistToolResult return seq invalid: %#v", msg.Seq)
	}
	uiMsg := toUIMessage(msg)
	if got, _ := uiMsg["id"].(string); got != msg.ID {
		t.Fatalf("toUIMessage id=%#v, want %q", uiMsg["id"], msg.ID)
	}
	if got, ok := uiMsg["seq"].(int64); !ok || got != *msg.Seq {
		t.Fatalf("toUIMessage seq=%#v, want %d", uiMsg["seq"], *msg.Seq)
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{ThreadID: thread.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs)=%d, want 1", len(msgs))
	}
	stored := msgs[0]
	if stored.ID != msg.ID {
		t.Fatalf("stored id=%q, want %q", stored.ID, msg.ID)
	}
	if stored.Seq != *msg.Seq {
		t.Fatalf("stored seq=%d, want %d", stored.Seq, *msg.Seq)
	}
	if stored.ContextPlan == nil {
		t.Fatalf("stored ContextPlan is missing")
	}
	if strings.TrimSpace(stored.ContextSummary) == "" {
		t.Fatalf("stored ContextSummary is empty")
	}
	if len(stored.ReferencedArtifactIDs) != 5 {
		t.Fatalf("len(referenced_artifact_ids)=%d, want 5", len(stored.ReferencedArtifactIDs))
	}
	want := map[string]bool{
		"artifact-a":      true,
		"artifact-b":      true,
		thread.ID:         true,
		"artifact-result": true,
		"artifact-c":      true,
	}
	for _, id := range stored.ReferencedArtifactIDs {
		delete(want, id)
	}
	if len(want) != 0 {
		t.Fatalf("unexpected referenced artifacts: missing=%#v", want)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid persisted tool envelope JSON: %v", err)
	}
	if _, ok := env["query_metadata"]; !ok {
		t.Fatalf("query metadata missing from persisted tool envelope: %#v", env)
	}
	qm, ok := env["query_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("query_metadata has unexpected type %#v", env["query_metadata"])
	}
	evid, ok := qm["evidence_budget"].(float64)
	if !ok {
		t.Fatalf("expected evidence_budget in query_metadata, got %#v", qm)
	}
	if evid != 1 {
		t.Fatalf("query_metadata.evidence_budget=%v, want 1", evid)
	}

	var persistedPlan RunContextPlan
	if err := json.Unmarshal(stored.ContextPlan, &persistedPlan); err != nil {
		t.Fatalf("unmarshal ContextPlan: %v", err)
	}
	if persistedPlan.HardCapped != true || persistedPlan.OverflowCompacted != true {
		t.Fatalf("stored plan mismatch: %+v", persistedPlan)
	}
}

func TestAttachThreadProjectFromGitHubCloneResult(t *testing.T) {
	cases := []struct {
		name          string
		result        map[string]any
		current       string
		existing      string
		wantAttached  string
		wantProjectID string
		wantEvent     bool
	}{
		{
			name:          "direct_project_id",
			result:        map[string]any{"project_id": "project-direct"},
			wantAttached:  "project-direct",
			wantProjectID: "project-direct",
			wantEvent:     true,
		},
		{
			name:          "nested_project_id",
			result:        map[string]any{"result": map[string]any{"project_id": "project-nested"}},
			wantAttached:  "project-nested",
			wantProjectID: "project-nested",
			wantEvent:     true,
		},
		{
			name:      "active_project_not_overridden",
			result:    map[string]any{"project_id": "project-new"},
			current:   "project-active",
			wantEvent: false,
		},
		{
			name:          "thread_project_not_overridden",
			result:        map[string]any{"project_id": "project-new"},
			existing:      "project-existing",
			wantProjectID: "project-existing",
			wantEvent:     false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("NewJSON: %v", err)
			}
			var projectID *string
			if tc.existing != "" {
				projectID = testProjectID(tc.existing)
			}
			thread, err := st.CreateThread(ctx, projectID)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-5.5", nil, "test system prompt", 10)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			manager := &Manager{
				store:      st,
				bufferSize: 16,
				runs: map[string]*runState{
					run.ID: {
						threadID: thread.ID,
						subs:     map[int]chan SSEEvent{},
						doneCh:   make(chan struct{}),
					},
				},
				activeByThread: map[string]string{thread.ID: run.ID},
			}
			ch, cancelSub, err := manager.Subscribe(run.ID)
			if err != nil {
				t.Fatalf("Subscribe: %v", err)
			}
			defer cancelSub()

			got := manager.attachThreadProjectFromToolResult(ctx, thread, run, tools.GitHubToolCloneRepository, tc.current, tools.JSONOK(tc.result))
			if got != tc.wantAttached {
				t.Fatalf("attached project=%q, want %q", got, tc.wantAttached)
			}
			updated, err := st.GetThread(ctx, thread.ID)
			if err != nil {
				t.Fatalf("GetThread: %v", err)
			}
			gotProjectID := ""
			if updated.ProjectID != nil {
				gotProjectID = strings.TrimSpace(*updated.ProjectID)
			}
			if gotProjectID != tc.wantProjectID {
				t.Fatalf("thread project=%q, want %q", gotProjectID, tc.wantProjectID)
			}

			if tc.wantEvent {
				select {
				case ev := <-ch:
					if ev.Event != "thread_updated" {
						t.Fatalf("event=%q, want thread_updated", ev.Event)
					}
					var payload map[string]any
					if err := json.Unmarshal(ev.Data, &payload); err != nil {
						t.Fatalf("thread_updated JSON: %v", err)
					}
					if payload["project_id"] != tc.wantAttached {
						t.Fatalf("event project_id=%#v, want %q", payload["project_id"], tc.wantAttached)
					}
				case <-time.After(time.Second):
					t.Fatal("timed out waiting for thread_updated event")
				}
				select {
				case ev := <-ch:
					t.Fatalf("unexpected second thread_updated event: %#v", ev)
				case <-time.After(25 * time.Millisecond):
				}
			} else {
				select {
				case ev := <-ch:
					t.Fatalf("unexpected thread_updated event: %#v", ev)
				case <-time.After(25 * time.Millisecond):
				}
			}
		})
	}
}

func TestManagerPersistToolResultCarriesPlotSpecToEnvelopeAndArtifact(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	call := types.ToolCall{
		ID:   "tc_plot_spec",
		Name: "display_table",
		Args: map[string]any{
			"data_json": "[{\"month\":\"2026-01\",\"revenue\":100}]",
		},
	}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows": []any{
				map[string]any{"month": "2026-01", "revenue": 100.0},
				map[string]any{"month": "2026-02", "revenue": 125.0},
			},
			"chart_engine":       "highcharts",
			"chart_spec_version": "hc-cap-v1-test",
			"chart_module":       "core",
			"chart_modules":      []any{"drilldown", "treemap"},
			"plot_spec": map[string]any{
				"schema_version": "highcharts-plot-spec.v1",
				"module":         "core",
				"options": map[string]any{
					"series": []any{
						map[string]any{"type": "line", "data": []any{100.0, 125.0}},
					},
				},
			},
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted tool message content is empty")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid persisted envelope: %v", err)
	}
	if env["chart_engine"] != "highcharts" {
		t.Fatalf("envelope chart_engine=%#v, want highcharts", env["chart_engine"])
	}
	if env["chart_spec_version"] != "hc-cap-v1-test" {
		t.Fatalf("envelope chart_spec_version=%#v, want hc-cap-v1-test", env["chart_spec_version"])
	}
	if modules, ok := env["chart_modules"].([]any); !ok || len(modules) != 2 {
		t.Fatalf("envelope chart_modules=%#v, want two entries", env["chart_modules"])
	}
	if _, ok := env["plot_spec"].(map[string]any); !ok {
		t.Fatalf("envelope missing plot_spec: %#v", env)
	}

	artifactID, _ := env["artifact_id"].(string)
	if strings.TrimSpace(artifactID) == "" {
		t.Fatalf("envelope missing artifact_id: %#v", env)
	}
	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.Metadata == nil {
		t.Fatalf("artifact.Metadata is nil")
	}
	if artifact.Metadata["chart_engine"] != "highcharts" {
		t.Fatalf("artifact metadata chart_engine=%#v, want highcharts", artifact.Metadata["chart_engine"])
	}
	if artifact.Metadata["chart_spec_version"] != "hc-cap-v1-test" {
		t.Fatalf("artifact metadata chart_spec_version=%#v, want hc-cap-v1-test", artifact.Metadata["chart_spec_version"])
	}
	if modules, ok := artifact.Metadata["chart_modules"].([]any); !ok || len(modules) != 2 {
		t.Fatalf("artifact metadata chart_modules=%#v, want two entries", artifact.Metadata["chart_modules"])
	}
	if _, ok := artifact.Metadata["plot_spec"].(map[string]any); !ok {
		t.Fatalf("artifact metadata missing plot_spec: %#v", artifact.Metadata)
	}
}

func TestManagerPersistToolResultCarriesChartBudgetAndSSEBytes(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	call := types.ToolCall{
		ID:   "tc-chart-budget",
		Name: "build_chart_artifact",
		Args: map[string]any{
			"source": map[string]any{"artifact_id": "source-1"},
		},
	}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows": []any{
				map[string]any{"x": 1, "y": 2},
				map[string]any{"x": 2, "y": 3},
			},
			"chart_engine": "echarts",
			"plot_spec": map[string]any{
				"schema_version": "echarts-plot-spec.v1",
				"module":         "core",
				"options": map[string]any{
					"xAxis":  map[string]any{"type": "category", "data": []any{"a", "b"}},
					"yAxis":  map[string]any{"type": "value"},
					"series": []any{map[string]any{"type": "line", "data": []any{2, 3}}},
				},
			},
			"chart_budget": map[string]any{
				"schema_version":   "chart-budget.v1",
				"chart_kind":       "line",
				"chart_engine":     "echarts",
				"row_count_total":  2000,
				"points_requested": 2000,
				"points_returned":  200,
				"plot_spec_bytes":  20480,
				"budget_actions":   []any{"downsample:line_lttb"},
			},
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted tool message content is empty")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid persisted envelope: %v", err)
	}
	chartBudget, ok := env["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("envelope chart_budget missing: %#v", env["chart_budget"])
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", chartBudget["schema_version"])); got != "chart-budget.v1" {
		t.Fatalf("envelope chart_budget.schema_version=%q, want chart-budget.v1", got)
	}

	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing from envelope: %#v", env["result_metadata"])
	}
	sseEventBytes := int(numberFromAny(resultMetadata["sse_event_bytes"]))
	if sseEventBytes <= 0 {
		t.Fatalf("result_metadata.sse_event_bytes=%d, want > 0", sseEventBytes)
	}
	metaBudget, ok := resultMetadata["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata.chart_budget missing: %#v", resultMetadata["chart_budget"])
	}
	if got := int(numberFromAny(metaBudget["sse_event_bytes"])); got != sseEventBytes {
		t.Fatalf("result_metadata.chart_budget.sse_event_bytes=%d, want %d", got, sseEventBytes)
	}

	artifactID, _ := env["artifact_id"].(string)
	if strings.TrimSpace(artifactID) == "" {
		t.Fatalf("envelope artifact_id missing")
	}
	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.Metadata == nil {
		t.Fatalf("artifact metadata missing")
	}
	artifactBudget, ok := artifact.Metadata["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata chart_budget missing: %#v", artifact.Metadata["chart_budget"])
	}
	if got := int(numberFromAny(artifactBudget["points_returned"])); got != 200 {
		t.Fatalf("artifact metadata chart_budget.points_returned=%d, want 200", got)
	}
}

func TestManagerPersistToolResultCompactsLargeEnvelopeAndPreservesArtifactRows(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	rows := make([]any, 0, 120)
	for i := 0; i < 120; i++ {
		rows = append(rows, map[string]any{
			"id":    i + 1,
			"value": fmt.Sprintf("v-%03d", i+1),
		})
	}
	call := types.ToolCall{
		ID:   "tc-large-envelope",
		Name: "run_bigquery_query",
		Args: map[string]any{"sql": "select * from table"},
	}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows": rows,
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted tool message content is empty")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid persisted envelope: %v", err)
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing from envelope: %#v", env["result_metadata"])
	}
	if resultMetadata["model_context_compacted"] != true {
		t.Fatalf("model_context_compacted=%#v, want true", resultMetadata["model_context_compacted"])
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", resultMetadata["model_context_compaction_stage"])); got != "envelope" {
		t.Fatalf("model_context_compaction_stage=%q, want envelope", got)
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", resultMetadata["model_context_compaction_reason"])); got != "row_count_threshold" {
		t.Fatalf("model_context_compaction_reason=%q, want row_count_threshold", got)
	}
	before := int(numberFromAny(resultMetadata["result_size_bytes_before_compaction"]))
	after := int(numberFromAny(resultMetadata["result_size_bytes_after_compaction"]))
	if before <= after {
		t.Fatalf("expected result_size_bytes_before_compaction > after, got before=%d after=%d", before, after)
	}

	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result type=%T, want map[string]any", env["result"])
	}
	rowsCompacted, ok := result["rows"].([]any)
	if !ok {
		t.Fatalf("envelope result.rows type=%T, want []any", result["rows"])
	}
	if len(rowsCompacted) >= len(rows) {
		t.Fatalf("expected compacted rows to be smaller than %d, got %d", len(rows), len(rowsCompacted))
	}

	artifactID, _ := env["artifact_id"].(string)
	if strings.TrimSpace(artifactID) == "" {
		t.Fatalf("envelope artifact_id missing")
	}
	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.ContentText == nil || strings.TrimSpace(*artifact.ContentText) == "" {
		t.Fatalf("artifact content missing")
	}
	var artifactRows []map[string]any
	if err := json.Unmarshal([]byte(*artifact.ContentText), &artifactRows); err != nil {
		t.Fatalf("unmarshal artifact content rows: %v", err)
	}
	if len(artifactRows) != len(rows) {
		t.Fatalf("artifact rows=%d, want %d", len(artifactRows), len(rows))
	}
}

func TestManagerPersistToolResultKeepsSmallEnvelopeUnchanged(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	call := types.ToolCall{ID: "tc-small-envelope", Name: "run_bigquery_query"}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows": []any{
				map[string]any{"id": 1, "value": "a"},
				map[string]any{"id": 2, "value": "b"},
			},
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted tool message content is empty")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid persisted envelope: %v", err)
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing from envelope: %#v", env["result_metadata"])
	}
	if _, exists := resultMetadata["model_context_compacted"]; exists {
		t.Fatalf("expected no envelope compaction metadata for small result, got %#v", resultMetadata["model_context_compacted"])
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result type=%T, want map[string]any", env["result"])
	}
	rowsOut, ok := result["rows"].([]any)
	if !ok {
		t.Fatalf("envelope rows type=%T, want []any", result["rows"])
	}
	if len(rowsOut) != 2 {
		t.Fatalf("len(result.rows)=%d, want 2", len(rowsOut))
	}
}

func TestManagerPersistToolResultUsesArtifactPayloadContent(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	fullRows := []map[string]any{
		{"x": 1, "y": 2},
		{"x": 2, "y": 4},
		{"x": 3, "y": 6},
		{"x": 4, "y": 8},
		{"x": 5, "y": 10},
	}
	fullBlob, err := json.Marshal(fullRows)
	if err != nil {
		t.Fatalf("json.Marshal fullRows: %v", err)
	}

	call := types.ToolCall{
		ID:   "tc_chart_artifact_payload",
		Name: "build_chart_artifact",
		Args: map[string]any{
			"source": map[string]any{"artifact_id": "source-1"},
		},
	}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"artifact_id":      "chart-full-id",
			"rows":             []any{map[string]any{"x": 1, "y": 2}, map[string]any{"x": 2, "y": 4}},
			"preview_only":     true,
			"full_artifact_id": "chart-full-id",
			"row_count_total":  5,
			"downsample_ratio": 2.5,
		},
		Artifact: &tools.ArtifactPayload{
			ID:      "chart-full-id",
			Type:    "data",
			Title:   "Full Chart Dataset",
			Content: string(fullBlob),
			Metadata: map[string]any{
				"row_count_total": 5,
				"result_metadata": map[string]any{
					"kind":           "payload_rows",
					"payload_marker": "payload-specific",
				},
			},
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted message content is empty")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid envelope JSON: %v", err)
	}
	if env["artifact_id"] != "chart-full-id" {
		t.Fatalf("envelope artifact_id=%#v, want chart-full-id", env["artifact_id"])
	}
	if env["preview_only"] != true {
		t.Fatalf("envelope preview_only=%#v, want true", env["preview_only"])
	}
	if env["full_artifact_id"] != "chart-full-id" {
		t.Fatalf("envelope full_artifact_id=%#v, want chart-full-id", env["full_artifact_id"])
	}

	artifact, err := st.GetArtifact(ctx, "chart-full-id")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.ContentText == nil {
		t.Fatalf("artifact ContentText is nil")
	}
	var persistedRows []map[string]any
	if err := json.Unmarshal([]byte(*artifact.ContentText), &persistedRows); err != nil {
		t.Fatalf("parse persisted artifact rows: %v", err)
	}
	if len(persistedRows) != len(fullRows) {
		t.Fatalf("persisted row count=%d, want %d", len(persistedRows), len(fullRows))
	}
	if artifact.Metadata == nil {
		t.Fatalf("artifact.Metadata is nil")
	}
	if got := artifact.Metadata["row_count_total"]; got != float64(5) && got != 5 {
		t.Fatalf("artifact metadata row_count_total=%#v, want 5", got)
	}
	resultMetadata, ok := artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact result_metadata=%T, want object: %#v", artifact.Metadata["result_metadata"], artifact.Metadata)
	}
	if resultMetadata["payload_marker"] != "payload-specific" || resultMetadata["kind"] != "payload_rows" {
		t.Fatalf("artifact result_metadata lost payload fields: %#v", resultMetadata)
	}
	if resultMetadata["renderer_strategy"] != "artifact_type" ||
		resultMetadata["renderer_key"] != "chart" ||
		resultMetadata["native_viewer"] != "chart" {
		t.Fatalf("artifact result_metadata lost canonical renderer fields: %#v", resultMetadata)
	}
	if _, ok := resultMetadata["tool_presentation"]; !ok {
		t.Fatalf("artifact result_metadata missing tool_presentation after payload merge: %#v", resultMetadata)
	}
}

func TestManagerPersistToolResultAttachesLocalOpenFileArtifact(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	localRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localRoot, "reports"), 0o700); err != nil {
		t.Fatalf("MkdirAll local reports: %v", err)
	}
	const body = "%PDF-1.4\n% local native attachment\n"
	if err := os.WriteFile(filepath.Join(localRoot, "reports", "briefing.pdf"), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile local pdf: %v", err)
	}

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-5.5", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	reg := tools.NewRegistry(&tools.RegistryOptions{Store: st, CfgDir: cfgDir})
	tool, ok := reg.Get(tools.LocalOpenFileToolName)
	if !ok {
		t.Fatal("local_open_file tool missing from registry")
	}
	out, err := tool.Execute(ctx, tools.ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		RunID:    run.ID,
		CfgDir:   cfgDir,
	}, mustJSONForPermissionTest(map[string]any{
		"local_root": localRoot,
		"path":       "reports/briefing.pdf",
	}))
	if err != nil {
		t.Fatalf("Execute local_open_file: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{ID: "call_local_open", Name: tools.LocalOpenFileToolName, Args: map[string]any{
		"path": "reports/briefing.pdf",
	}}
	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if len(msg.FileAttachments) != 1 {
		t.Fatalf("FileAttachments len=%d, want 1 native local file attachment: %#v", len(msg.FileAttachments), msg.FileAttachments)
	}
	attachment := msg.FileAttachments[0]
	artifactID := firstStringMapValue(anyMap(out.Result), "attachment_artifact_id", "attachmentArtifactId", "file_artifact_id")
	if artifactID == "" {
		t.Fatalf("local_open_file result missing file artifact id: %#v", out.Result)
	}
	if attachment.ArtifactID != artifactID {
		t.Fatalf("attachment artifact=%q, want local_open_file artifact %q", attachment.ArtifactID, artifactID)
	}
	if attachment.InlineDataBase64 == "" {
		t.Fatalf("attachment missing inline native file bytes: %#v", attachment)
	}
	if attachment.MimeType != "application/pdf" && attachment.NormalizedMIME != "application/pdf" {
		t.Fatalf("attachment MIME=%q normalized=%q, want PDF", attachment.MimeType, attachment.NormalizedMIME)
	}
	if attachment.Source != "external_tool" {
		t.Fatalf("attachment Source=%q, want external_tool", attachment.Source)
	}
	if !attachment.Capabilities["native_file_input"] || !attachment.Capabilities["model_file_input"] {
		t.Fatalf("attachment capabilities=%#v, want native/model file input", attachment.Capabilities)
	}

	storedMessages, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID:   thread.ID,
		MessageIDs: []string{msg.ID},
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ListMessages tool result: %v", err)
	}
	if len(storedMessages) != 1 {
		t.Fatalf("stored messages len=%d, want 1", len(storedMessages))
	}
	if !containsStringForTest(storedMessages[0].ReferencedArtifactIDs, artifactID) {
		t.Fatalf("stored refs=%#v, want local opened file artifact %q", storedMessages[0].ReferencedArtifactIDs, artifactID)
	}
	history := manager.buildLegacyHistory(ctx, thread.ID, storedMessages, "", "")
	if len(history) != 1 {
		t.Fatalf("history len=%d, want 1", len(history))
	}
	if len(history[0].FileAttachments) != 1 {
		t.Fatalf("history FileAttachments len=%d, want 1: %#v", len(history[0].FileAttachments), history[0].FileAttachments)
	}
	if history[0].FileAttachments[0].ArtifactID != artifactID || history[0].FileAttachments[0].InlineDataBase64 == "" {
		t.Fatalf("history attachment=%#v, want hydrated local opened file", history[0].FileAttachments[0])
	}
}

func TestManagerLocalOpenFilePromotesArtifactToNativeFileAttachment(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	localRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(localRoot, "reports"), 0o700); err != nil {
		t.Fatalf("MkdirAll local reports: %v", err)
	}
	localPath := filepath.Join(localRoot, "reports", "summary.md")
	const localBody = "# Local Summary\n\nRevenue rose 42 percent.\n"
	if err := os.WriteFile(localPath, []byte(localBody), 0o600); err != nil {
		t.Fatalf("WriteFile local summary: %v", err)
	}

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-5.5", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	manager := &Manager{
		store:  st,
		cfgDir: cfgDir,
		tools:  tools.NewRegistry(nil),
	}
	call := types.ToolCall{
		ID:   "call_local_open",
		Name: tools.LocalOpenFileToolName,
		Args: map[string]any{
			"local_root": localRoot,
			"path":       "reports/summary.md",
		},
	}
	msg, err := manager.execTool(
		ctx,
		thread,
		run,
		nil,
		call,
		false,
		"off",
		0,
		&RunContextPlan{},
		nil,
		chartEngineEnablement{},
		false,
		false,
		false,
		true,
		false,
		atlassianEnablement{},
		false,
		false,
		slackToolAccess{},
	)
	if err != nil {
		t.Fatalf("execTool local_open_file: %v", err)
	}
	if len(msg.FileAttachments) != 1 {
		t.Fatalf("FileAttachments len=%d, want 1: %#v", len(msg.FileAttachments), msg.FileAttachments)
	}
	attachment := msg.FileAttachments[0]
	if attachment.ArtifactID == "" {
		t.Fatalf("local-open attachment missing artifact id: %#v", attachment)
	}
	if attachment.InlineDataBase64 == "" {
		t.Fatalf("local-open attachment missing inline data for provider-native input: %#v", attachment)
	}
	if attachment.FileName != "summary.md" {
		t.Fatalf("FileName=%q, want summary.md", attachment.FileName)
	}
	if attachment.Source != externalToolFileArtifactSource {
		t.Fatalf("Source=%q, want %q", attachment.Source, externalToolFileArtifactSource)
	}
	if native := fileAttachmentNativeInputAvailability(config.AppSettings{}, "openai", nil, []types.FileAttachment{attachment}); !native[attachment.ArtifactID] {
		t.Fatalf("local-open attachment not available as native file input: %#v", native)
	}

	if msg.Content == nil {
		t.Fatalf("message content is nil")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid tool result envelope: %v", err)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result missing map: %#v", env["result"])
	}
	if got := firstStringMapValue(result, "attachment_artifact_id", "attachmentArtifactId", "file_artifact_id"); got != attachment.ArtifactID {
		t.Fatalf("result attachment artifact id=%q, want %q", got, attachment.ArtifactID)
	}
	if native, ok := firstBoolMapValue(result, "native_file_input"); !ok || !native {
		t.Fatalf("result.native_file_input=%v/%v, want true", native, ok)
	}

	artifact, err := st.GetArtifact(ctx, attachment.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact local-open file: %v", err)
	}
	if artifact.Type != "file" || artifact.Storage != "file" || artifact.ContentPath == nil {
		t.Fatalf("artifact=%#v, want file artifact stored on disk", artifact)
	}
	if got := string(manager.readArtifactBytes(artifact)); got != localBody {
		t.Fatalf("promoted local artifact bytes=%q, want exact local file body", got)
	}
	if source := stringMapValue(artifact.Metadata, "local_source"); source != "local_file_snapshot" {
		t.Fatalf("artifact local_source=%q, want local_file_snapshot", source)
	}

	storedMessages, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID:   thread.ID,
		MessageIDs: []string{msg.ID},
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ListMessages tool result: %v", err)
	}
	if len(storedMessages) != 1 {
		t.Fatalf("stored messages len=%d, want 1", len(storedMessages))
	}
	if !containsStringForTest(storedMessages[0].ReferencedArtifactIDs, attachment.ArtifactID) {
		t.Fatalf("tool result refs=%#v, want local file artifact %q", storedMessages[0].ReferencedArtifactIDs, attachment.ArtifactID)
	}
	history := manager.buildLegacyHistory(ctx, thread.ID, storedMessages, "", "")
	if len(history) != 1 || len(history[0].FileAttachments) != 1 {
		t.Fatalf("history attachments=%#v, want one hydrated local file attachment", history)
	}
	if history[0].FileAttachments[0].InlineDataBase64 == "" {
		t.Fatalf("history attachment missing inline data: %#v", history[0].FileAttachments[0])
	}

	ui := toUIMessage(msg)
	rawAttachments, ok := ui["file_attachments"].([]types.FileAttachment)
	if !ok || len(rawAttachments) != 1 {
		t.Fatalf("UI file_attachments=%#v, want one rendered file attachment", ui["file_attachments"])
	}
	if rawAttachments[0].ArtifactID != attachment.ArtifactID {
		t.Fatalf("UI attachment artifact id=%q, want %q", rawAttachments[0].ArtifactID, attachment.ArtifactID)
	}
	if rawAttachments[0].InlineDataBase64 != "" {
		t.Fatalf("UI attachment leaked inline provider bytes")
	}
}

func TestManagerPersistToolResultPromotesManagedExternalDownload(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "bedrock", "anthropic.claude-3-5-sonnet", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	managedRel := filepath.ToSlash(filepath.Join("atlassian", "downloads", "jira", "ABC-1", "10001", "revenue.csv"))
	managedFull := filepath.Join(cfgDir, filepath.FromSlash(managedRel))
	if err := os.MkdirAll(filepath.Dir(managedFull), 0o700); err != nil {
		t.Fatalf("MkdirAll managed download: %v", err)
	}
	if err := os.WriteFile(managedFull, []byte("region,revenue\nEMEA,42\n"), 0o600); err != nil {
		t.Fatalf("WriteFile managed download: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{
		ID:   "call_jira_attachment",
		Name: "jira_read_attachment",
		Args: map[string]any{"issueKey": "ABC-1", "attachmentId": "10001"},
	}
	const jiraSourceURL = "https://jira.example.com/rest/api/3/attachment/content/10001"
	out := tools.JSONOK(map[string]any{
		"issueKey":     "ABC-1",
		"attachmentId": "10001",
		"fileName":     "revenue.csv",
		"mimeType":     "text/csv",
		"local_path":   managedRel,
		"attachment": map[string]any{
			"id":          "10001",
			"content_url": jiraSourceURL,
		},
		"download": map[string]any{
			"local_path": managedRel,
			"fileName":   "revenue.csv",
			"source":     "atlassian_managed_download",
		},
	})

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if len(msg.FileAttachments) != 1 {
		t.Fatalf("FileAttachments len=%d, want 1: %#v", len(msg.FileAttachments), msg.FileAttachments)
	}
	attachment := msg.FileAttachments[0]
	if attachment.ArtifactID == "" {
		t.Fatalf("promoted attachment missing artifact id: %#v", attachment)
	}
	if attachment.InlineDataBase64 == "" {
		t.Fatalf("promoted attachment missing inline data: %#v", attachment)
	}
	if attachment.FileName != "revenue.csv" {
		t.Fatalf("FileName=%q, want revenue.csv", attachment.FileName)
	}
	if attachment.Source != externalToolFileArtifactSource {
		t.Fatalf("Source=%q, want %q", attachment.Source, externalToolFileArtifactSource)
	}

	var env map[string]any
	if msg.Content == nil {
		t.Fatalf("message content is nil")
	}
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid tool result envelope: %v", err)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result missing map: %#v", env["result"])
	}
	if got := firstStringMapValue(result, "attachment_artifact_id", "attachmentArtifactId"); got != attachment.ArtifactID {
		t.Fatalf("attachment_artifact_id=%q, want %q", got, attachment.ArtifactID)
	}
	if native, ok := firstBoolMapValue(result, "native_file_input"); !ok || !native {
		t.Fatalf("native_file_input=%v/%v, want true", native, ok)
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object", env["result_metadata"])
	}
	if got := firstStringMapValue(resultMetadata, "attachment_artifact_id", "attachmentArtifactId", "file_artifact_id"); got != attachment.ArtifactID {
		t.Fatalf("result_metadata attachment artifact id=%q, want %q", got, attachment.ArtifactID)
	}
	if native, ok := firstBoolMapValue(resultMetadata, "native_file_input"); !ok || !native {
		t.Fatalf("result_metadata.native_file_input=%v/%v, want true", native, ok)
	}
	assertExternalFileMetadataRemoteLocators(t, resultMetadata, jiraSourceURL, "", "10001")

	artifact, err := st.GetArtifact(ctx, attachment.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact promoted file: %v", err)
	}
	if artifact.Type != "file" {
		t.Fatalf("artifact Type=%q, want file", artifact.Type)
	}
	if artifact.ContentPath == nil || strings.HasPrefix(filepath.ToSlash(*artifact.ContentPath), "atlassian/downloads/") {
		t.Fatalf("artifact ContentPath=%v, want copied tool_files path", artifact.ContentPath)
	}
	if got := stringMapValue(artifact.Metadata, "managedLocalPath", "managed_local_path"); got != managedRel {
		t.Fatalf("managedLocalPath=%q, want %q", got, managedRel)
	}
	if external, ok := boolMapValue(artifact.Metadata, "externalToolFile", "external_tool_file"); !ok || !external {
		t.Fatalf("externalToolFile=%v/%v, want true", external, ok)
	}
	assertPromotedExternalArtifactMetadata(t, artifact, "jira_read_attachment", "atlassian", "jira", "remote_file_download")
	assertPromotedExternalArtifactToolArg(t, artifact, "attachmentId", "10001")
	assertPromotedExternalArtifactRemoteLocators(t, artifact, jiraSourceURL, "", "10001")
	if extraction := fileAttachmentExtractionMap(artifact); extraction == nil {
		t.Fatalf("promoted file missing extraction metadata: %#v", artifact.Metadata)
	}
	if got := string(manager.readArtifactBytes(artifact)); !strings.Contains(got, "EMEA,42") {
		t.Fatalf("promoted artifact bytes=%q, want copied managed download content", got)
	}

	storedMessages, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID:   thread.ID,
		MessageIDs: []string{msg.ID},
		Limit:      1,
	})
	if err != nil {
		t.Fatalf("ListMessages tool result: %v", err)
	}
	if len(storedMessages) != 1 {
		t.Fatalf("stored messages len=%d, want 1", len(storedMessages))
	}
	stored := storedMessages[0]
	if !containsStringForTest(stored.ReferencedArtifactIDs, attachment.ArtifactID) {
		t.Fatalf("tool result refs=%#v, want promoted artifact %q", stored.ReferencedArtifactIDs, attachment.ArtifactID)
	}
	history := manager.buildLegacyHistory(ctx, thread.ID, storedMessages, "", "")
	if len(history) != 1 {
		t.Fatalf("history len=%d, want 1", len(history))
	}
	if len(history[0].FileAttachments) != 1 {
		t.Fatalf("history FileAttachments len=%d, want 1: %#v", len(history[0].FileAttachments), history[0].FileAttachments)
	}
	historyAttachment := history[0].FileAttachments[0]
	if historyAttachment.ArtifactID != attachment.ArtifactID {
		t.Fatalf("history attachment artifact=%q, want %q", historyAttachment.ArtifactID, attachment.ArtifactID)
	}
	if historyAttachment.InlineDataBase64 == "" {
		t.Fatalf("history attachment missing inline data: %#v", historyAttachment)
	}
}

func TestManagerPersistToolResultPromotesSlackManagedDownload(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-5.5", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	managedRel := filepath.ToSlash(filepath.Join("slack", "downloads", "F123", "pipeline.csv"))
	managedFull := filepath.Join(cfgDir, filepath.FromSlash(managedRel))
	if err := os.MkdirAll(filepath.Dir(managedFull), 0o700); err != nil {
		t.Fatalf("MkdirAll managed slack download: %v", err)
	}
	if err := os.WriteFile(managedFull, []byte("step,status\nbuild,green\n"), 0o600); err != nil {
		t.Fatalf("WriteFile managed slack download: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{
		ID:   "call_slack_download",
		Name: tools.SlackToolDownloadFile,
		Args: map[string]any{"file_id": "F123", "channel_id": "C456"},
	}
	const slackSourceURL = "https://example.slack.com/files/F123/pipeline.csv"
	descriptor := map[string]any{
		"local_path":          managedRel,
		"localPath":           managedRel,
		"local_relative_path": managedRel,
		"localRelativePath":   managedRel,
		"file_name":           "pipeline.csv",
		"fileName":            "pipeline.csv",
		"mime_type":           "text/csv",
		"mimeType":            "text/csv",
		"size_bytes":          24,
		"sizeBytes":           24,
		"source":              "slack_managed_download",
	}
	out := tools.JSONOK(map[string]any{
		"ok":                  true,
		"file_id":             "F123",
		"channel_id":          "C456",
		"file":                map[string]any{"id": "F123", "name": "pipeline.csv", "mimetype": "text/csv", "permalink": slackSourceURL, "url_private_download": "https://files.slack.com/files-pri/T123-F123/download/pipeline.csv"},
		"file_name":           descriptor["file_name"],
		"fileName":            descriptor["fileName"],
		"mime_type":           descriptor["mime_type"],
		"mimeType":            descriptor["mimeType"],
		"size_bytes":          descriptor["size_bytes"],
		"sizeBytes":           descriptor["sizeBytes"],
		"local_path":          descriptor["local_path"],
		"localPath":           descriptor["localPath"],
		"local_relative_path": descriptor["local_relative_path"],
		"localRelativePath":   descriptor["localRelativePath"],
		"download":            descriptor,
		"source":              "slack_managed_download",
	})

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if len(msg.FileAttachments) != 1 {
		t.Fatalf("FileAttachments len=%d, want 1: %#v", len(msg.FileAttachments), msg.FileAttachments)
	}
	attachment := msg.FileAttachments[0]
	if attachment.ArtifactID == "" {
		t.Fatalf("promoted slack attachment missing artifact id: %#v", attachment)
	}
	if attachment.FileName != "pipeline.csv" {
		t.Fatalf("FileName=%q, want pipeline.csv", attachment.FileName)
	}
	if attachment.Source != externalToolFileArtifactSource {
		t.Fatalf("Source=%q, want %q", attachment.Source, externalToolFileArtifactSource)
	}

	var env map[string]any
	if msg.Content == nil {
		t.Fatalf("message content is nil")
	}
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid tool result envelope: %v", err)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result missing map: %#v", env["result"])
	}
	if got := firstStringMapValue(result, "attachment_artifact_id", "attachmentArtifactId"); got != attachment.ArtifactID {
		t.Fatalf("attachment_artifact_id=%q, want %q", got, attachment.ArtifactID)
	}
	if native, ok := firstBoolMapValue(result, "native_file_input"); !ok || !native {
		t.Fatalf("native_file_input=%v/%v, want true", native, ok)
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object", env["result_metadata"])
	}
	assertExternalFileMetadataRemoteLocators(t, resultMetadata, slackSourceURL, "", "F123")

	artifact, err := st.GetArtifact(ctx, attachment.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact promoted slack file: %v", err)
	}
	if artifact.Type != "file" {
		t.Fatalf("artifact Type=%q, want file", artifact.Type)
	}
	if got := stringMapValue(artifact.Metadata, "managedLocalPath", "managed_local_path"); got != managedRel {
		t.Fatalf("managedLocalPath=%q, want %q", got, managedRel)
	}
	if external, ok := boolMapValue(artifact.Metadata, "externalToolFile", "external_tool_file"); !ok || !external {
		t.Fatalf("externalToolFile=%v/%v, want true", external, ok)
	}
	assertPromotedExternalArtifactMetadata(t, artifact, tools.SlackToolDownloadFile, "slack", "slack", "remote_file_download")
	assertPromotedExternalArtifactToolArg(t, artifact, "file_id", "F123")
	assertPromotedExternalArtifactRemoteLocators(t, artifact, slackSourceURL, "", "F123")
	if got := string(manager.readArtifactBytes(artifact)); !strings.Contains(got, "build,green") {
		t.Fatalf("promoted artifact bytes=%q, want copied managed download content", got)
	}
}

func TestManagerPersistToolResultPromotesSharePointManagedDownload(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		result   func(string) map[string]any
	}{
		{
			name:     "read",
			toolName: tools.SharePointToolRead,
			result: func(managedRel string) map[string]any {
				file := sharePointManagedDownloadDescriptorForTest(managedRel)
				return map[string]any{
					"ok":                true,
					"status":            200,
					"url":               "https://contoso.sharepoint.com/_api/web/GetFileByServerRelativeUrl(...)/$value",
					"itemUrl":           "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv",
					"siteUrl":           "https://contoso.sharepoint.com/sites/team",
					"serverRelativeUrl": "/sites/team/Shared Documents/forecast.csv",
					"metadata":          map[string]any{"name": "forecast.csv"},
					"file":              file,
					"text":              "quarter,forecast\nQ1,120\n",
					"extraction":        "text_preview",
					"model_file_input":  true,
					"native_file_input": true,
					"errors":            []any{},
				}
			},
		},
		{
			name:     "summarize",
			toolName: tools.SharePointToolSummarize,
			result: func(managedRel string) map[string]any {
				file := sharePointManagedDownloadDescriptorForTest(managedRel)
				return map[string]any{
					"ok":                true,
					"status":            200,
					"url":               "https://contoso.sharepoint.com/_api/web/GetFileByServerRelativeUrl(...)/$value",
					"itemUrl":           "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv",
					"siteUrl":           "https://contoso.sharepoint.com/sites/team",
					"serverRelativeUrl": "/sites/team/Shared Documents/forecast.csv",
					"metadata":          map[string]any{"name": "forecast.csv"},
					"file":              file,
					"text":              "quarter,forecast\nQ1,120\n",
					"extraction":        "text_preview",
					"model_file_input":  true,
					"native_file_input": true,
					"summary_requested": true,
					"errors":            []any{},
				}
			},
		},
		{
			name:     "download",
			toolName: tools.SharePointToolDownload,
			result: func(managedRel string) map[string]any {
				file := sharePointManagedDownloadDescriptorForTest(managedRel)
				return map[string]any{
					"ok":                true,
					"status":            200,
					"url":               "https://contoso.sharepoint.com/_api/web/GetFileByServerRelativeUrl(...)/$value",
					"itemUrl":           "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv",
					"siteUrl":           "https://contoso.sharepoint.com/sites/team",
					"serverRelativeUrl": "/sites/team/Shared Documents/forecast.csv",
					"metadata":          map[string]any{"name": "forecast.csv"},
					"file":              file,
					"download":          file,
					"model_file_input":  true,
					"native_file_input": true,
					"errors":            []any{},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("NewJSON: %v", err)
			}
			thread, err := st.CreateThread(ctx, nil)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-5.5", nil, "test system prompt", 10)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}

			managedRel := filepath.ToSlash(filepath.Join("sharepoint", "downloads", "contoso.sharepoint.com", "abc123", tc.name+"-forecast.csv"))
			managedFull := filepath.Join(cfgDir, filepath.FromSlash(managedRel))
			if err := os.MkdirAll(filepath.Dir(managedFull), 0o700); err != nil {
				t.Fatalf("MkdirAll managed sharepoint download: %v", err)
			}
			if err := os.WriteFile(managedFull, []byte("quarter,forecast\nQ1,120\n"), 0o600); err != nil {
				t.Fatalf("WriteFile managed sharepoint download: %v", err)
			}

			manager := &Manager{store: st, cfgDir: cfgDir}
			call := types.ToolCall{
				ID:   "call_sharepoint_" + tc.name,
				Name: tc.toolName,
				Args: map[string]any{"server_relative_url": "/sites/team/Shared Documents/forecast.csv"},
			}
			msg := manager.persistToolResult(ctx, thread, run, call, tools.JSONOK(tc.result(managedRel)), RunContextPlan{})
			if len(msg.FileAttachments) != 1 {
				t.Fatalf("FileAttachments len=%d, want 1: %#v", len(msg.FileAttachments), msg.FileAttachments)
			}
			attachment := msg.FileAttachments[0]
			if attachment.ArtifactID == "" {
				t.Fatalf("promoted sharepoint attachment missing artifact id: %#v", attachment)
			}
			if attachment.FileName != "forecast.csv" {
				t.Fatalf("FileName=%q, want forecast.csv", attachment.FileName)
			}
			if attachment.Source != externalToolFileArtifactSource {
				t.Fatalf("Source=%q, want %q", attachment.Source, externalToolFileArtifactSource)
			}

			var env map[string]any
			if msg.Content == nil {
				t.Fatalf("message content is nil")
			}
			if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
				t.Fatalf("invalid tool result envelope: %v", err)
			}
			result, ok := env["result"].(map[string]any)
			if !ok {
				t.Fatalf("envelope result missing map: %#v", env["result"])
			}
			if got := firstStringMapValue(result, "attachment_artifact_id", "attachmentArtifactId"); got != attachment.ArtifactID {
				t.Fatalf("attachment_artifact_id=%q, want %q", got, attachment.ArtifactID)
			}
			if native, ok := firstBoolMapValue(result, "native_file_input"); !ok || !native {
				t.Fatalf("native_file_input=%v/%v, want true", native, ok)
			}
			resultMetadata, ok := env["result_metadata"].(map[string]any)
			if !ok {
				t.Fatalf("result_metadata=%T, want object", env["result_metadata"])
			}
			assertExternalFileEnvelopeMetadata(t, resultMetadata, attachment.ArtifactID, tc.toolName, "sharepoint", "sharepoint", "remote_file_download")
			assertExternalFileMetadataRemoteLocators(t, resultMetadata, "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv", "/sites/team/Shared Documents/forecast.csv", "")

			artifact, err := st.GetArtifact(ctx, attachment.ArtifactID)
			if err != nil {
				t.Fatalf("GetArtifact promoted sharepoint file: %v", err)
			}
			if artifact.Type != "file" {
				t.Fatalf("artifact Type=%q, want file", artifact.Type)
			}
			if got := stringMapValue(artifact.Metadata, "managedLocalPath", "managed_local_path"); got != managedRel {
				t.Fatalf("managedLocalPath=%q, want %q", got, managedRel)
			}
			if external, ok := boolMapValue(artifact.Metadata, "externalToolFile", "external_tool_file"); !ok || !external {
				t.Fatalf("externalToolFile=%v/%v, want true", external, ok)
			}
			assertPromotedExternalArtifactMetadata(t, artifact, tc.toolName, "sharepoint", "sharepoint", "remote_file_download")
			assertPromotedExternalArtifactToolArg(t, artifact, "server_relative_url", "/sites/team/Shared Documents/forecast.csv")
			assertPromotedExternalArtifactRemoteLocators(t, artifact, "https://contoso.sharepoint.com/sites/team/Shared%20Documents/forecast.csv", "/sites/team/Shared Documents/forecast.csv", "")
			if got := string(manager.readArtifactBytes(artifact)); !strings.Contains(got, "Q1,120") {
				t.Fatalf("promoted artifact bytes=%q, want copied managed download content", got)
			}
		})
	}
}

func TestManagerPersistToolResultPromotesArxivPDFFile(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-5.5", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	managedRel := filepath.ToSlash(filepath.Join("arxiv", "papers", "2301.12345", "paper.pdf"))
	managedFull := filepath.Join(cfgDir, filepath.FromSlash(managedRel))
	if err := os.MkdirAll(filepath.Dir(managedFull), 0o700); err != nil {
		t.Fatalf("MkdirAll managed arxiv pdf: %v", err)
	}
	const pdfBytes = "%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n<<>>\n%%EOF\n"
	if err := os.WriteFile(managedFull, []byte(pdfBytes), 0o600); err != nil {
		t.Fatalf("WriteFile managed arxiv pdf: %v", err)
	}

	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{
		ID:   "call_arxiv_download",
		Name: "arxiv_download_paper",
		Args: map[string]any{"arxiv_id": "2301.12345"},
	}
	out := tools.JSONOK(map[string]any{
		"paper": map[string]any{
			"paper_key": "2301.12345",
			"arxiv_id":  "2301.12345",
			"title":     "Sparse Attention Models",
			"abs_url":   "https://arxiv.org/abs/2301.12345",
			"pdf_url":   "https://arxiv.org/pdf/2301.12345",
		},
		"pdf_file": map[string]any{
			"local_path":        managedRel,
			"file_name":         "2301.12345.pdf",
			"mime_type":         "application/pdf",
			"size_bytes":        len(pdfBytes),
			"model_file_input":  true,
			"native_file_input": true,
		},
	})

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if len(msg.FileAttachments) != 1 {
		t.Fatalf("FileAttachments len=%d, want 1: %#v", len(msg.FileAttachments), msg.FileAttachments)
	}
	attachment := msg.FileAttachments[0]
	if attachment.ArtifactID == "" {
		t.Fatalf("promoted arxiv attachment missing artifact id: %#v", attachment)
	}
	if attachment.FileName != "2301.12345.pdf" {
		t.Fatalf("FileName=%q, want 2301.12345.pdf", attachment.FileName)
	}
	if attachment.InlineDataBase64 == "" {
		t.Fatalf("promoted arxiv pdf missing inline data: %#v", attachment)
	}
	if attachment.MimeType != "application/pdf" && attachment.NormalizedMIME != "application/pdf" {
		t.Fatalf("attachment MIME=%q normalized=%q, want application/pdf", attachment.MimeType, attachment.NormalizedMIME)
	}
	if attachment.Source != externalToolFileArtifactSource {
		t.Fatalf("Source=%q, want %q", attachment.Source, externalToolFileArtifactSource)
	}

	var env map[string]any
	if msg.Content == nil {
		t.Fatalf("message content is nil")
	}
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid tool result envelope: %v", err)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result missing map: %#v", env["result"])
	}
	if got := firstStringMapValue(result, "attachment_artifact_id", "attachmentArtifactId"); got != attachment.ArtifactID {
		t.Fatalf("attachment_artifact_id=%q, want %q", got, attachment.ArtifactID)
	}
	if native, ok := firstBoolMapValue(result, "native_file_input"); !ok || !native {
		t.Fatalf("native_file_input=%v/%v, want true", native, ok)
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata=%T, want object", env["result_metadata"])
	}
	assertExternalFileMetadataRemoteLocators(t, resultMetadata, "https://arxiv.org/pdf/2301.12345", "", "2301.12345")

	artifact, err := st.GetArtifact(ctx, attachment.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact promoted arxiv pdf: %v", err)
	}
	if artifact.Type != "file" {
		t.Fatalf("artifact Type=%q, want file", artifact.Type)
	}
	if got := stringMapValue(artifact.Metadata, "managedLocalPath", "managed_local_path"); got != managedRel {
		t.Fatalf("managedLocalPath=%q, want %q", got, managedRel)
	}
	assertPromotedExternalArtifactMetadata(t, artifact, "arxiv_download_paper", "arxiv", "arxiv", "remote_file_download")
	assertPromotedExternalArtifactToolArg(t, artifact, "arxiv_id", "2301.12345")
	assertPromotedExternalArtifactRemoteLocators(t, artifact, "https://arxiv.org/pdf/2301.12345", "", "2301.12345")
	if got := string(manager.readArtifactBytes(artifact)); got != pdfBytes {
		t.Fatalf("promoted arxiv artifact bytes=%q, want copied PDF", got)
	}
}

func sharePointManagedDownloadDescriptorForTest(managedRel string) map[string]any {
	return map[string]any{
		"local_path":          managedRel,
		"localPath":           managedRel,
		"local_relative_path": managedRel,
		"localRelativePath":   managedRel,
		"file_name":           "forecast.csv",
		"fileName":            "forecast.csv",
		"mime_type":           "text/csv",
		"mimeType":            "text/csv",
		"size_bytes":          24,
		"sizeBytes":           24,
		"source":              "sharepoint_managed_download",
		"model_file_input":    true,
		"native_file_input":   true,
	}
}

func assertPromotedExternalArtifactRemoteLocators(t *testing.T, artifact store.Artifact, wantURL string, wantPath string, wantRef string) {
	t.Helper()
	assertExternalFileMetadataRemoteLocators(t, artifact.Metadata, wantURL, wantPath, wantRef)
	resultMetadata, ok := artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact result_metadata=%T, want object: %#v", artifact.Metadata["result_metadata"], artifact.Metadata)
	}
	assertExternalFileMetadataRemoteLocators(t, resultMetadata, wantURL, wantPath, wantRef)
}

func assertExternalFileMetadataRemoteLocators(t *testing.T, metadata map[string]any, wantURL string, wantPath string, wantRef string) {
	t.Helper()
	if metadata == nil {
		t.Fatalf("metadata is nil, want remote locators")
	}
	if wantURL != "" {
		if got := firstStringMapValue(metadata, "source_url", "remote_url"); got != wantURL {
			t.Fatalf("metadata source/remote url=%q, want %q; metadata=%#v", got, wantURL, metadata)
		}
	}
	if wantPath != "" {
		if got := firstStringMapValue(metadata, "remote_path"); got != wantPath {
			t.Fatalf("metadata remote_path=%q, want %q; metadata=%#v", got, wantPath, metadata)
		}
	}
	if wantRef != "" {
		if got := firstStringMapValue(metadata, "remote_id", "remote_ref"); got != wantRef {
			t.Fatalf("metadata remote ref=%q, want %q; metadata=%#v", got, wantRef, metadata)
		}
	}
}

func assertPromotedExternalArtifactMetadata(t *testing.T, artifact store.Artifact, toolName string, integration string, family string, fileCapability string) {
	t.Helper()
	if artifact.Metadata == nil {
		t.Fatalf("artifact %s metadata is nil", artifact.ID)
	}
	resultMetadata, ok := artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact result_metadata=%T, want object: %#v", artifact.Metadata["result_metadata"], artifact.Metadata)
	}
	if resultMetadata["source_tool_name"] != toolName {
		t.Fatalf("result_metadata.source_tool_name=%#v, want %q", resultMetadata["source_tool_name"], toolName)
	}
	if resultMetadata["external_tool_file"] != true {
		t.Fatalf("result_metadata.external_tool_file=%#v, want true", resultMetadata["external_tool_file"])
	}
	if resultMetadata["integration"] != integration || resultMetadata["family"] != family {
		t.Fatalf("result_metadata integration/family=%#v/%#v, want %q/%q", resultMetadata["integration"], resultMetadata["family"], integration, family)
	}
	if resultMetadata["native_viewer"] != "file" || resultMetadata["file_capability"] != fileCapability {
		t.Fatalf("result_metadata viewer/capability=%#v/%#v, want file/%s", resultMetadata["native_viewer"], resultMetadata["file_capability"], fileCapability)
	}
	if _, ok := resultMetadata["tool_presentation"]; !ok {
		t.Fatalf("result_metadata.tool_presentation missing: %#v", resultMetadata)
	}
	presentation := artifact.Metadata["tool_presentation"]
	if presentation == nil {
		t.Fatalf("artifact tool_presentation missing: %#v", artifact.Metadata)
	}
	if got := presentationStringField(presentation, "family"); got != family {
		t.Fatalf("artifact tool_presentation.family=%q, want %q: %#v", got, family, presentation)
	}
	if got := presentationStringField(presentation, "native_viewer"); got != "file" {
		t.Fatalf("artifact tool_presentation.native_viewer=%q, want file: %#v", got, presentation)
	}
	if got := presentationStringField(presentation, "file_capability"); got != fileCapability {
		t.Fatalf("artifact tool_presentation.file_capability=%q, want %q: %#v", got, fileCapability, presentation)
	}
}

func assertExternalFileEnvelopeMetadata(t *testing.T, resultMetadata map[string]any, artifactID string, toolName string, integration string, family string, fileCapability string) {
	t.Helper()
	if got := resultMetadata["tool_name"]; got != toolName {
		t.Fatalf("result_metadata.tool_name=%#v, want %q; metadata=%#v", got, toolName, resultMetadata)
	}
	if got := resultMetadata["integration"]; got != integration {
		t.Fatalf("result_metadata.integration=%#v, want %q; metadata=%#v", got, integration, resultMetadata)
	}
	if got := resultMetadata["family"]; got != family {
		t.Fatalf("result_metadata.family=%#v, want %q; metadata=%#v", got, family, resultMetadata)
	}
	if got := resultMetadata["native_viewer"]; got != "file" {
		t.Fatalf("result_metadata.native_viewer=%#v, want file; metadata=%#v", got, resultMetadata)
	}
	if got := resultMetadata["renderer_strategy"]; got != "artifact_type" {
		t.Fatalf("result_metadata.renderer_strategy=%#v, want artifact_type; metadata=%#v", got, resultMetadata)
	}
	if got := resultMetadata["renderer_key"]; got != "file" {
		t.Fatalf("result_metadata.renderer_key=%#v, want file; metadata=%#v", got, resultMetadata)
	}
	if got := resultMetadata["file_capability"]; got != fileCapability {
		t.Fatalf("result_metadata.file_capability=%#v, want %q; metadata=%#v", got, fileCapability, resultMetadata)
	}
	if got := resultMetadata["external_tool_file"]; got != true {
		t.Fatalf("result_metadata.external_tool_file=%#v, want true; metadata=%#v", got, resultMetadata)
	}
	if native, ok := firstBoolMapValue(resultMetadata, "native_file_input"); !ok || !native {
		t.Fatalf("result_metadata.native_file_input=%v/%v, want true; metadata=%#v", native, ok, resultMetadata)
	}
	if model, ok := firstBoolMapValue(resultMetadata, "model_file_input"); !ok || !model {
		t.Fatalf("result_metadata.model_file_input=%v/%v, want true; metadata=%#v", model, ok, resultMetadata)
	}
	if got := firstStringMapValue(resultMetadata, "attachment_artifact_id", "attachmentArtifactId"); got != artifactID {
		t.Fatalf("result_metadata.attachment_artifact_id=%q, want %q; metadata=%#v", got, artifactID, resultMetadata)
	}
	if got := firstStringMapValue(resultMetadata, "file_artifact_id"); got != artifactID {
		t.Fatalf("result_metadata.file_artifact_id=%q, want %q; metadata=%#v", got, artifactID, resultMetadata)
	}
	presentation, ok := resultMetadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata.tool_presentation=%T, want object: %#v", resultMetadata["tool_presentation"], resultMetadata)
	}
	if got := firstStringMapValue(presentation, "family"); got != family {
		t.Fatalf("result_metadata.tool_presentation.family=%q, want %q; presentation=%#v", got, family, presentation)
	}
	if got := firstStringMapValue(presentation, "native_viewer"); got != "file" {
		t.Fatalf("result_metadata.tool_presentation.native_viewer=%q, want file; presentation=%#v", got, presentation)
	}
	if got := firstStringMapValue(presentation, "renderer_key"); got != "file" {
		t.Fatalf("result_metadata.tool_presentation.renderer_key=%q, want file; presentation=%#v", got, presentation)
	}
	if produces, ok := firstBoolMapValue(presentation, "produces_file_artifact"); !ok || !produces {
		t.Fatalf("result_metadata.tool_presentation.produces_file_artifact=%v/%v, want true; presentation=%#v", produces, ok, presentation)
	}
	if promotes, ok := firstBoolMapValue(presentation, "promotes_native_input"); !ok || !promotes {
		t.Fatalf("result_metadata.tool_presentation.promotes_native_input=%v/%v, want true; presentation=%#v", promotes, ok, presentation)
	}
}

func assertPromotedExternalArtifactToolArg(t *testing.T, artifact store.Artifact, key string, want any) {
	t.Helper()
	args, ok := artifact.Metadata["tool_args"].(map[string]any)
	if !ok {
		t.Fatalf("artifact tool_args=%T, want object: %#v", artifact.Metadata["tool_args"], artifact.Metadata)
	}
	if got := args[key]; got != want {
		t.Fatalf("artifact tool_args[%q]=%#v, want %#v; args=%#v", key, got, want, args)
	}
}

func presentationStringField(value any, field string) string {
	switch typed := value.(type) {
	case map[string]any:
		if v, ok := typed[field].(string); ok {
			return strings.TrimSpace(v)
		}
	case tools.ToolPresentationSpec:
		switch field {
		case "family":
			return strings.TrimSpace(typed.Family)
		case "native_viewer":
			return strings.TrimSpace(typed.NativeViewer)
		case "file_capability":
			return strings.TrimSpace(typed.FileCapability)
		}
	}
	return ""
}

func containsStringForTest(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestManagerPersistToolResultInteractiveHTMLOmitsRawHTMLFromEnvelope(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	html := `<div id="secret-html-marker"><script>window.secret = true</script></div>`
	language := "html"
	call := types.ToolCall{
		ID:   "tc_interactive_html",
		Name: "create_interactive_data_artifact",
	}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"title":                   "Interactive Dataset",
			"artifact_type":           "interactive_data_html",
			"language":                "html",
			"render_mode":             "sandboxed_html",
			"sandbox":                 "allow-scripts",
			"network":                 "blocked",
			"inline_preview":          true,
			"byte_size":               len([]byte(html)),
			"data_source_artifact_id": "source-data",
			"data_source_row_count":   42,
			"data_source_columns":     []any{"region", "revenue"},
			"data_access":             map[string]any{"mode": "parent_bridge", "max_page_size": 100},
			"html":                    html,
			"content":                 html,
			"srcdoc":                  html,
		},
		Artifact: &tools.ArtifactPayload{
			Type:     "code",
			Title:    "Interactive Dataset",
			Language: &language,
			Content:  html,
			Metadata: map[string]any{
				"render_mode":             "sandboxed_html",
				"artifact_kind":           "interactive_data_html",
				"sandbox":                 "allow-scripts",
				"network":                 "blocked",
				"inline_preview":          true,
				"data_source_artifact_id": "source-data",
				"data_source_row_count":   42,
				"data_source_columns":     []any{"region", "revenue"},
				"data_access":             map[string]any{"mode": "parent_bridge", "max_page_size": 100},
			},
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted message content is empty")
	}
	if strings.Contains(*msg.Content, "secret-html-marker") {
		t.Fatalf("tool result envelope leaked raw HTML: %s", *msg.Content)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid envelope JSON: %v", err)
	}
	if env["inline_preview"] != true {
		t.Fatalf("envelope inline_preview=%#v, want true", env["inline_preview"])
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("envelope result type=%T, want map[string]any", env["result"])
	}
	for _, key := range []string{"html", "content", "srcdoc"} {
		if _, exists := result[key]; exists {
			t.Fatalf("envelope result leaked %q: %#v", key, result)
		}
	}
	if result["inline_preview"] != true {
		t.Fatalf("result inline_preview=%#v, want true", result["inline_preview"])
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing: %#v", env["result_metadata"])
	}
	if resultMetadata["inline_preview"] != true {
		t.Fatalf("result_metadata.inline_preview=%#v, want true", resultMetadata["inline_preview"])
	}

	artifactID, _ := env["artifact_id"].(string)
	if strings.TrimSpace(artifactID) == "" {
		t.Fatalf("envelope artifact_id missing")
	}
	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.ContentText == nil || !strings.Contains(*artifact.ContentText, "secret-html-marker") {
		t.Fatalf("artifact content missing raw HTML")
	}
	if artifact.Metadata["inline_preview"] != true {
		t.Fatalf("artifact metadata inline_preview=%#v, want true", artifact.Metadata["inline_preview"])
	}
	if artifact.Metadata["data_source_artifact_id"] != "source-data" {
		t.Fatalf("artifact metadata data_source_artifact_id=%#v, want source-data", artifact.Metadata["data_source_artifact_id"])
	}
	if got := int(numberFromAny(artifact.Metadata["data_source_row_count"])); got != 42 {
		t.Fatalf("artifact metadata data_source_row_count=%d, want 42", got)
	}
	if _, ok := artifact.Metadata["data_access"].(map[string]any); !ok {
		t.Fatalf("artifact metadata data_access missing: %#v", artifact.Metadata["data_access"])
	}
}

func TestManagerPersistToolResultDisplayTableCarriesPreviewMetadata(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "test-model", nil, "test system prompt", 10)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}

	fullRows := make([]map[string]any, 0, 300)
	for i := 0; i < 300; i++ {
		fullRows = append(fullRows, map[string]any{
			"category": fmt.Sprintf("c-%03d", i+1),
			"value":    i + 1,
		})
	}
	fullBlob, err := json.Marshal(fullRows)
	if err != nil {
		t.Fatalf("json.Marshal fullRows: %v", err)
	}

	previewRows := []any{
		map[string]any{"category": "c-001", "value": 1},
		map[string]any{"category": "c-002", "value": 2},
	}
	call := types.ToolCall{
		ID:   "tc_display_table_preview",
		Name: "display_table",
		Args: map[string]any{
			"source": map[string]any{"artifact_id": "source-preview"},
		},
	}
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows":             previewRows,
			"preview_only":     true,
			"full_artifact_id": "table-full-id",
			"row_count_total":  300,
		},
		Artifact: &tools.ArtifactPayload{
			ID:      "table-full-id",
			Type:    "data",
			Title:   "Preview Table",
			Content: string(fullBlob),
			Metadata: map[string]any{
				"row_count_total": 300,
			},
		},
	}

	msg := manager.persistToolResult(ctx, thread, run, call, out, RunContextPlan{})
	if msg.Content == nil || strings.TrimSpace(*msg.Content) == "" {
		t.Fatalf("persisted message content is empty")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*msg.Content), &env); err != nil {
		t.Fatalf("invalid envelope JSON: %v", err)
	}
	if env["preview_only"] != true {
		t.Fatalf("envelope preview_only=%#v, want true", env["preview_only"])
	}
	if env["full_artifact_id"] != "table-full-id" {
		t.Fatalf("envelope full_artifact_id=%#v, want table-full-id", env["full_artifact_id"])
	}
	if got := int(numberFromAny(env["row_count_total"])); got != 300 {
		t.Fatalf("envelope row_count_total=%d, want 300", got)
	}

	artifact, err := st.GetArtifact(ctx, "table-full-id")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if artifact.Metadata == nil {
		t.Fatalf("artifact.Metadata is nil")
	}
	if artifact.Metadata["preview_only"] != true {
		t.Fatalf("artifact metadata preview_only=%#v, want true", artifact.Metadata["preview_only"])
	}
	if artifact.Metadata["partial"] != true {
		t.Fatalf("artifact metadata partial=%#v, want true", artifact.Metadata["partial"])
	}
	if artifact.Metadata["full_artifact_id"] != "table-full-id" {
		t.Fatalf("artifact metadata full_artifact_id=%#v, want table-full-id", artifact.Metadata["full_artifact_id"])
	}
	if got := int(numberFromAny(artifact.Metadata["row_count_total"])); got != 300 {
		t.Fatalf("artifact metadata row_count_total=%d, want 300", got)
	}
}

func TestManagerExtractToolResultReferencedArtifactIDs(t *testing.T) {
	t.Run("rlm_exec", func(t *testing.T) {
		out := tools.Output{
			Result: map[string]any{
				"id": "op-1",
				"evidence": []any{
					map[string]any{"artifact_id": "artifact-a", "message_id": "m1", "doc_id": "d1"},
					map[string]any{"artifactId": "artifact-b", "message_id": "m2", "id": "e2"},
				},
			},
		}
		got := extractToolResultReferencedArtifactIDs("rlm_exec", out)
		if len(got) != 4 {
			t.Fatalf("rlm_exec artifact refs=%#v", got)
		}
		expected := map[string]bool{
			"artifact-a": true,
			"m1":         true,
			"d1":         true,
			"e2":         true,
		}
		for id := range expected {
			found := false
			for _, gotID := range got {
				if strings.TrimSpace(gotID) == id {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing referenced id %q in %#v", id, got)
			}
		}
	})

	t.Run("list_messages", func(t *testing.T) {
		out := tools.Output{
			Result: map[string]any{
				"messages": []any{
					map[string]any{"artifact_id": "artifact-a", "message_id": "m1"},
					map[string]any{"artifactId": "artifact-b", "id": "m2"},
				},
				"artifact_ids": []any{"artifact-c", "artifact-c", "artifact-a"},
			},
		}
		got := extractToolResultReferencedArtifactIDs("list_messages", out)
		want := map[string]bool{"artifact-a": true, "m1": true, "artifact-b": true, "m2": true, "artifact-c": true}
		for id := range want {
			found := false
			for _, gotID := range got {
				if strings.TrimSpace(gotID) == id {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("missing referenced id %q in %#v", id, got)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("got=%#v want size=%d", got, len(want))
		}
	})
}

func TestManagerExtractToolReferencedArtifactIDs(t *testing.T) {
	ids := extractToolReferencedArtifactIDs("list_messages", map[string]any{
		"thread_id":    "thread-1",
		"artifact_ids": []any{"artifact-a", "artifact-a", "artifact-b"},
		"message_ids":  []string{"m1", "m2"},
		"thread_ids":   []string{"thread-1", "thread-2"},
		"project_id":   "project-1",
	})
	want := []string{"thread-1", "artifact-a", "artifact-b", "m1", "m2", "thread-2"}
	if len(ids) != len(want) {
		t.Fatalf("extracted=%#v, want=%#v", ids, want)
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Fatalf("missing %q in %#v", id, ids)
		}
	}

	fallback := extractToolReferencedArtifactIDs("list_messages", map[string]any{"project_id": "project-1", "projectId": "project-2"})
	if len(fallback) != 1 {
		t.Fatalf("fallback extraction should return 1 id, got %#v", fallback)
	}
	if fallback[0] != "project-1" && fallback[0] != "project-2" {
		t.Fatalf("unexpected fallback extraction %#v", fallback)
	}

	camelCaseIDs := extractToolReferencedArtifactIDs("list_messages", map[string]any{
		"threadIds":   []string{"thread-x", "thread-x"},
		"artifactIds": []any{"artifact-x", 77},
		"messageIds":  []any{"msg-x", "msg-y"},
	})
	wantCamel := map[string]bool{"thread-x": true, "artifact-x": true, "msg-x": true, "msg-y": true}
	for _, id := range camelCaseIDs {
		delete(wantCamel, id)
	}
	if len(wantCamel) != 0 {
		t.Fatalf("missing camelCase fallback refs in %#v", camelCaseIDs)
	}

	displayTableIDs := extractToolReferencedArtifactIDs("display_table", map[string]any{
		"source": map[string]any{
			"artifact_id": "artifact-display-source",
		},
	})
	if len(displayTableIDs) != 1 || displayTableIDs[0] != "artifact-display-source" {
		t.Fatalf("display_table source refs=%#v, want [artifact-display-source]", displayTableIDs)
	}

	chartArtifactIDs := extractToolReferencedArtifactIDs("build_chart_artifact", map[string]any{
		"source": map[string]any{
			"artifact_id": "artifact-chart-source",
		},
	})
	if len(chartArtifactIDs) != 1 || chartArtifactIDs[0] != "artifact-chart-source" {
		t.Fatalf("build_chart_artifact source refs=%#v, want [artifact-chart-source]", chartArtifactIDs)
	}
}

func TestManagerSubscribe_BacklogBeforeLive_AfterRegistration(t *testing.T) {
	const backlogLen = 4096
	runID := "run-subscribe-order"

	manager, st := newManagerWithBufferedRun(runID, backlogLen)
	subscribeCh := make(chan subscribeResult, 1)
	go func() {
		ch, cancel, err := manager.Subscribe(runID)
		subscribeCh <- subscribeResult{ch: ch, cancel: cancel, err: err}
	}()

	if err := waitForSubscriberRegistered(st, 2*time.Second); err != nil {
		t.Fatalf("waitForSubscriberRegistered: %v", err)
	}
	if _, err := waitForReplayInProgressOrComplete(st, backlogLen, 2*time.Second); err != nil {
		t.Fatalf("waitForReplayInProgressOrComplete: %v", err)
	}
	manager.broadcast(runID, SSEEvent{Event: "live"})

	sub := <-subscribeCh
	if sub.err != nil {
		t.Fatalf("Subscribe: %v", sub.err)
	}
	defer sub.cancel()

	for i := 0; i < backlogLen; i++ {
		ev := mustReceiveEvent(t, sub.ch, 2*time.Second)
		want := backlogEventName(i)
		if ev.Event != want {
			t.Fatalf("event[%d]=%q, want %q", i, ev.Event, want)
		}
	}
	if ev := mustReceiveEvent(t, sub.ch, 2*time.Second); ev.Event != "live" {
		t.Fatalf("live event ordering mismatch: got %q, want %q", ev.Event, "live")
	}
}

func TestManagerSubscribe_BacklogOrderStress(t *testing.T) {
	const iterations = 24
	const backlogLen = 512

	for i := 0; i < iterations; i++ {
		runID := fmt.Sprintf("run-subscribe-stress-%d", i)
		manager, st := newManagerWithBufferedRun(runID, backlogLen)
		subscribeCh := make(chan subscribeResult, 1)
		go func() {
			ch, cancel, err := manager.Subscribe(runID)
			subscribeCh <- subscribeResult{ch: ch, cancel: cancel, err: err}
		}()

		if err := waitForSubscriberRegistered(st, 2*time.Second); err != nil {
			t.Fatalf("iteration %d waitForSubscriberRegistered: %v", i, err)
		}
		if _, err := waitForReplayInProgressOrComplete(st, backlogLen, 2*time.Second); err != nil {
			t.Fatalf("iteration %d waitForReplayInProgressOrComplete: %v", i, err)
		}
		manager.broadcast(runID, SSEEvent{Event: "live"})

		sub := <-subscribeCh
		if sub.err != nil {
			t.Fatalf("iteration %d Subscribe: %v", i, sub.err)
		}

		for j := 0; j < backlogLen; j++ {
			ev := mustReceiveEvent(t, sub.ch, 2*time.Second)
			want := backlogEventName(j)
			if ev.Event != want {
				sub.cancel()
				t.Fatalf("iteration %d event[%d]=%q, want %q", i, j, ev.Event, want)
			}
		}
		if ev := mustReceiveEvent(t, sub.ch, 2*time.Second); ev.Event != "live" {
			sub.cancel()
			t.Fatalf("iteration %d live event ordering mismatch: got %q, want %q", i, ev.Event, "live")
		}
		sub.cancel()
	}
}

func TestManagerSubscribe_FinishDuringReplay_NoPanic(t *testing.T) {
	const iterations = 24
	const backlogLen = 4096

	for i := 0; i < iterations; i++ {
		runID := fmt.Sprintf("run-subscribe-finish-%d", i)
		manager, st := newManagerWithBufferedRun(runID, backlogLen)
		outcomeCh := make(chan subscribeOutcome, 1)
		go func() {
			out := subscribeOutcome{}
			defer func() {
				if r := recover(); r != nil {
					out.panicValue = r
				}
				outcomeCh <- out
			}()
			ch, cancel, err := manager.Subscribe(runID)
			out.err = err
			_ = cancel
			out.ch = ch
		}()

		if err := waitForSubscriberRegistered(st, 2*time.Second); err != nil {
			t.Fatalf("iteration %d waitForSubscriberRegistered: %v", i, err)
		}
		manager.finish(runID)

		select {
		case out := <-outcomeCh:
			if out.err != nil {
				t.Fatalf("iteration %d Subscribe: %v", i, out.err)
			}
			if out.panicValue != nil {
				t.Fatalf("iteration %d Subscribe panicked: %v", i, out.panicValue)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d timed out waiting for Subscribe result", i)
		}
	}
}

func TestManagerDelayedFinishCleanupPreservesRecoveredRunState(t *testing.T) {
	t.Parallel()
	const runID = "run-recovered-before-delayed-cleanup"
	finished := &runState{subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{})}
	recovered := &runState{subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{})}
	manager := &Manager{runs: map[string]*runState{runID: recovered}}

	manager.removeRunStateIfCurrent(runID, finished)
	if current := manager.runs[runID]; current != recovered {
		t.Fatalf("stale cleanup removed recovered run state: got=%p want=%p", current, recovered)
	}

	manager.removeRunStateIfCurrent(runID, recovered)
	if _, exists := manager.runs[runID]; exists {
		t.Fatal("current finished run state was not removed")
	}
}

func TestSubscribeRejectsStaleDoneStateForDurablyNonterminalRun(t *testing.T) {
	t.Parallel()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.CreateRun(context.Background(), thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatal(err)
	}
	done := &runState{
		threadID: thread.ID, subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{}), done: true,
		buffer: []SSEEvent{{Event: "old_owner_event", Seq: 1, Data: []byte(`{}`)}},
	}
	manager := &Manager{store: repository, runs: map[string]*runState{run.ID: done}}
	if _, _, err := manager.Subscribe(run.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Subscribe stale done state err=%v, want durable-tail fallback", err)
	}
}

func TestBroadcastPersistsWithoutLocalSubscriberAndAllocatesAfterRecoveryHistory(t *testing.T) {
	t.Parallel()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.CreateRun(context.Background(), thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AppendRunEvent(context.Background(), store.RunEvent{
		RunID: run.ID, ThreadID: thread.ID, Type: "before_recovery", Data: json.RawMessage(`{"old":true}`),
	}); err != nil {
		t.Fatal(err)
	}
	state := &runState{
		threadID: thread.ID, subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{}), durableEvents: true,
	}
	manager := &Manager{store: repository, runs: map[string]*runState{run.ID: state}, bufferSize: 16}
	manager.broadcast(run.ID, SSEEvent{Event: "assistant_delta", Data: []byte(`{"delta":"resumed"}`)})
	events, err := repository.ListRunEvents(context.Background(), store.ListRunEventsParams{RunID: run.ID, Limit: 10})
	if err != nil || len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 || events[1].Type != "assistant_delta" {
		t.Fatalf("durable events=%#v err=%v", events, err)
	}
}

type failOnceAppendRunEventStore struct {
	store.Store
	mu       sync.Mutex
	failed   bool
	failWith error
}

func (s *failOnceAppendRunEventStore) AppendRunEvent(ctx context.Context, event store.RunEvent) (store.RunEvent, error) {
	s.mu.Lock()
	if !s.failed {
		s.failed = true
		err := s.failWith
		s.mu.Unlock()
		return store.RunEvent{}, err
	}
	s.mu.Unlock()
	return s.Store.AppendRunEvent(ctx, event)
}

func TestBroadcastPersistenceFailureDoesNotAdvanceReconnectCursor(t *testing.T) {
	t.Parallel()
	repository, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := repository.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	run, err := repository.CreateRun(context.Background(), thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatal(err)
	}
	failingStore := &failOnceAppendRunEventStore{Store: repository, failWith: errors.New("transient event-store failure")}
	state := &runState{
		threadID: thread.ID, subs: make(map[int]chan SSEEvent), doneCh: make(chan struct{}), durableEvents: true,
	}
	manager := &Manager{store: failingStore, runs: map[string]*runState{run.ID: state}, bufferSize: 16}
	stream, cancel, err := manager.Subscribe(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	manager.broadcast(run.ID, SSEEvent{Event: "assistant_delta", Data: []byte(`{"delta":"not-durable"}`)})
	first := <-stream
	if first.Seq != 0 {
		t.Fatalf("unpersisted event seq=%d, want 0 so Last-Event-ID does not advance", first.Seq)
	}
	manager.broadcast(run.ID, SSEEvent{Event: "assistant_delta", Data: []byte(`{"delta":"durable"}`)})
	second := <-stream
	if second.Seq != 1 {
		t.Fatalf("first durable event seq=%d, want 1", second.Seq)
	}
	events, err := repository.ListRunEvents(context.Background(), store.ListRunEventsParams{RunID: run.ID, AfterSeq: first.Seq, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Seq != second.Seq || !strings.Contains(string(events[0].Data), "durable") {
		t.Fatalf("events after reconnect cursor %d = %#v, want the next durable event", first.Seq, events)
	}
}

func TestThinkingEnd_FlushesPendingThinkingDelta(t *testing.T) {
	manager := &Manager{deltaInterval: time.Hour}
	events := make([]SSEEvent, 0, 2)
	emit := func(event string, payload any) {
		var b []byte
		if payload != nil {
			if jb, err := json.Marshal(payload); err == nil {
				b = jb
			}
		}
		events = append(events, SSEEvent{Event: event, Data: b})
	}

	pushThinkingDelta, flushThinkingDeltas := manager.coalesceDeltas("run-thinking-order", "thinking_delta", emit)
	pushThinkingDelta("pending-thought")
	emitThinkingEnd(flushThinkingDeltas, emit, nil)

	if len(events) < 2 {
		t.Fatalf("events=%d, want >=2", len(events))
	}
	if events[0].Event != "thinking_delta" {
		t.Fatalf("events[0]=%q, want %q", events[0].Event, "thinking_delta")
	}
	var delta map[string]any
	if err := json.Unmarshal(events[0].Data, &delta); err != nil {
		t.Fatalf("thinking_delta payload decode: %v", err)
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", delta["content"])); got != "pending-thought" {
		t.Fatalf("thinking_delta content=%q, want %q", got, "pending-thought")
	}
	if events[1].Event != "thinking_end" {
		t.Fatalf("events[1]=%q, want %q", events[1].Event, "thinking_end")
	}
}

func TestCompactionEnd_FlushesPendingCompactionDelta(t *testing.T) {
	manager := &Manager{deltaInterval: time.Hour}
	events := make([]SSEEvent, 0, 2)
	emit := func(event string, payload any) {
		var b []byte
		if payload != nil {
			if jb, err := json.Marshal(payload); err == nil {
				b = jb
			}
		}
		events = append(events, SSEEvent{Event: event, Data: b})
	}

	pushCompactionDelta, flushCompactionDeltas := manager.coalesceDeltas("run-compaction-order", "compaction_delta", emit)
	pushCompactionDelta("pending-summary")
	emitCompactionEnd(flushCompactionDeltas, emit)

	if len(events) < 2 {
		t.Fatalf("events=%d, want >=2", len(events))
	}
	if events[0].Event != "compaction_delta" {
		t.Fatalf("events[0]=%q, want %q", events[0].Event, "compaction_delta")
	}
	var delta map[string]any
	if err := json.Unmarshal(events[0].Data, &delta); err != nil {
		t.Fatalf("compaction_delta payload decode: %v", err)
	}
	if got := strings.TrimSpace(fmt.Sprintf("%v", delta["content"])); got != "pending-summary" {
		t.Fatalf("compaction_delta content=%q, want %q", got, "pending-summary")
	}
	if events[1].Event != "compaction_end" {
		t.Fatalf("events[1]=%q, want %q", events[1].Event, "compaction_end")
	}
}

type retryMarkerCompactionStrategy struct {
	marker         string
	overflowTokens int
}

func (s *retryMarkerCompactionStrategy) CompactForPersistence(input ToolResultPersistenceInput) ToolResultPersistenceOutput {
	return ToolResultPersistenceOutput{Result: input.Result}
}

func (s *retryMarkerCompactionStrategy) CompactHistory(input ToolResultHistoryInput) ToolResultHistoryOutput {
	history := append([]types.ChatMessage(nil), input.History...)
	latestToolIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == types.RoleTool {
			latestToolIdx = i
			break
		}
	}
	if latestToolIdx < 0 {
		return ToolResultHistoryOutput{History: history}
	}
	if history[latestToolIdx].Content != nil && strings.Contains(*history[latestToolIdx].Content, s.marker) {
		return ToolResultHistoryOutput{History: history}
	}

	cpt := input.CharsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	targetTokens := input.TokenBudget + maxInt(1, s.overflowTokens)
	if targetTokens < 1 {
		targetTokens = 1
	}
	targetChars := targetTokens * cpt
	if targetChars < len(s.marker)+64 {
		targetChars = len(s.marker) + 64
	}

	blob := strings.Repeat("x", targetChars)
	compacted := fmt.Sprintf("{\"marker\":\"%s\",\"blob\":\"%s\"}", s.marker, blob)
	history[latestToolIdx].Content = &compacted
	return ToolResultHistoryOutput{
		History:   history,
		Compacted: true,
	}
}

func requestPayloadContainsMarker(messages []any, marker string) bool {
	for _, raw := range messages {
		msg, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		content, ok := msg["content"]
		if !ok {
			continue
		}
		switch typed := content.(type) {
		case string:
			if strings.Contains(typed, marker) {
				return true
			}
		case []any:
			for _, part := range typed {
				block, ok := part.(map[string]any)
				if !ok {
					continue
				}
				text, _ := block["text"].(string)
				if strings.Contains(text, marker) {
					return true
				}
			}
		}
	}
	return false
}

type subscribeResult struct {
	ch     <-chan SSEEvent
	cancel func()
	err    error
}

type subscribeOutcome struct {
	ch         <-chan SSEEvent
	err        error
	panicValue any
}

func newManagerWithBufferedRun(runID string, backlogLen int) (*Manager, *runState) {
	st := &runState{
		threadID: "thread-subscribe-test",
		subs:     make(map[int]chan SSEEvent),
		doneCh:   make(chan struct{}),
		buffer:   make([]SSEEvent, 0, backlogLen),
	}
	for i := 0; i < backlogLen; i++ {
		st.buffer = append(st.buffer, SSEEvent{Event: backlogEventName(i)})
	}

	manager := &Manager{
		bufferSize:     backlogLen + 1024,
		runs:           map[string]*runState{runID: st},
		activeByThread: make(map[string]string),
	}
	return manager, st
}

func backlogEventName(i int) string {
	return fmt.Sprintf("backlog_%06d", i)
}

func waitForSubscriberRegistered(st *runState, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		ready := len(st.subs) > 0
		st.mu.Unlock()
		if ready {
			return nil
		}
		time.Sleep(100 * time.Microsecond)
	}
	return context.DeadlineExceeded
}

func waitForReplayInProgressOrComplete(st *runState, backlogLen int, timeout time.Duration) (bool, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st.mu.Lock()
		queued := -1
		for _, ch := range st.subs {
			queued = len(ch)
			break
		}
		st.mu.Unlock()

		if queued < 0 {
			time.Sleep(100 * time.Microsecond)
			continue
		}
		if queued > 0 && queued < backlogLen {
			return true, nil
		}
		if queued >= backlogLen {
			return false, nil
		}
		time.Sleep(100 * time.Microsecond)
	}
	return false, context.DeadlineExceeded
}

func mustReceiveEvent(t *testing.T, ch <-chan SSEEvent, timeout time.Duration) SSEEvent {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("stream closed before expected event")
		}
		return ev
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for stream event")
		return SSEEvent{}
	}
}

func collectEventNames(ch <-chan SSEEvent) []string {
	events := make([]string, 0, 32)
	for ev := range ch {
		events = append(events, ev.Event)
	}
	return events
}

func firstEventIndex(events []string, target string) int {
	for i, event := range events {
		if event == target {
			return i
		}
	}
	return -1
}

func countEvent(events []string, target string) int {
	count := 0
	for _, event := range events {
		if event == target {
			count++
		}
	}
	return count
}

func waitForRunTerminalState(ctx context.Context, st store.Store, runID string) (store.Run, error) {
	timeoutAt := time.Now().Add(5 * time.Second)
	for {
		run, err := st.GetRun(ctx, runID)
		if err != nil {
			return run, err
		}
		if run.Status == "completed" || run.Status == "failed" || run.Status == "cancelled" {
			return run, nil
		}
		if time.Now().After(timeoutAt) {
			return run, context.DeadlineExceeded
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func newStartRunManagerForSSE(t *testing.T, cfgDir string, providerBaseURL string) (*Manager, store.Store) {
	t.Helper()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}

	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("test-pass"); err != nil && !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("test-pass"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := sec.SetCompatibleAPIKey("default", "test-key"); err != nil {
		t.Fatalf("SetCompatibleAPIKey: %v", err)
	}

	settings := cfg.Get()
	if len(settings.CompatibleProfiles) == 0 {
		t.Fatalf("expected at least one compatible profile")
	}
	settings.Provider.ProviderID = "compatible"
	settings.Provider.Model = "test-model"
	settings.Provider.ProfileID = &settings.CompatibleProfiles[0].ID
	settings.CompatibleProfiles[0].BaseURL = providerBaseURL
	if err := cfg.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}

	manager := NewManager(st, cfg, providers.NewFactory(cfg, sec), tools.NewRegistry(nil), ps, nil, nil, nil, cfgDir, nil)
	t.Cleanup(func() {
		waitForManagerRunsDone(t, manager, 2*time.Second)
	})
	return manager, st
}

func newAnthropicStartRunManagerForSSE(t *testing.T, cfgDir string, providerBaseURL string, compactionTrigger int) (*Manager, store.Store) {
	t.Helper()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}

	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("test-pass"); err != nil && !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("test-pass"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := sec.SetProviderKey("anthropic", "test-key"); err != nil {
		t.Fatalf("SetProviderKey anthropic: %v", err)
	}

	settings := cfg.Get()
	settings.Provider.ProviderID = "anthropic"
	settings.Provider.Model = "claude-opus-4-6"
	settings.Provider.ProfileID = nil

	if settings.Anthropic == nil {
		settings.Anthropic = &config.AnthropicConfig{}
	}
	baseURL := providerBaseURL
	enabled := true
	settings.Anthropic.BaseURL = &baseURL
	settings.Anthropic.CompactionEnabled = &enabled
	settings.Anthropic.CompactionTrigger = &compactionTrigger

	if err := cfg.Save(settings); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}

	manager := NewManager(st, cfg, providers.NewFactory(cfg, sec), tools.NewRegistry(nil), ps, nil, nil, nil, cfgDir, nil)
	t.Cleanup(func() {
		waitForManagerRunsDone(t, manager, 2*time.Second)
	})
	return manager, st
}

func waitForManagerRunsDone(t *testing.T, manager *Manager, timeout time.Duration) {
	t.Helper()
	if manager == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for {
		manager.mu.Lock()
		pending := make([]chan struct{}, 0, len(manager.runs))
		for _, st := range manager.runs {
			if st == nil {
				continue
			}
			st.mu.Lock()
			done := st.done
			ch := st.doneCh
			st.mu.Unlock()
			if !done && ch != nil {
				pending = append(pending, ch)
			}
		}
		manager.mu.Unlock()
		if len(pending) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %d manager run(s) to finish", len(pending))
		}
		wait := 10 * time.Millisecond
		if remaining := time.Until(deadline); remaining < wait {
			wait = remaining
		}
		select {
		case <-pending[0]:
		case <-time.After(wait):
		}
	}
}

func mapContainsAnyKey(m map[string]any, expected map[string]bool) bool {
	for k := range expected {
		if _, ok := m[k]; ok {
			return true
		}
	}
	return false
}

func numberFromAny(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case uint:
		return float64(x)
	case uint32:
		return float64(x)
	case uint64:
		return float64(x)
	default:
		return 0
	}
}
