package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestContextPlannerWithinBudget(t *testing.T) {
	messages := []store.Message{
		{
			ID:      "m1",
			Seq:     1,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("A", 200)),
		},
		{
			ID:      "m2",
			Seq:     2,
			Role:    types.RoleAssistant,
			Content: ptrToString(strings.Repeat("B", 200)),
		},
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "m2", "", ContextPolicy{
		MaxInputTokens:  500,
		HardContextCap:  4000,
		BaseWindowTurns: 12,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if got.Plan.NeedRetrieval {
		t.Fatalf("unexpected NeedRetrieval=%v", got.Plan.NeedRetrieval)
	}
	if got.Plan.HardCapped {
		t.Fatalf("unexpected HardCapped=%v", got.Plan.HardCapped)
	}
	if len(got.History) != len(messages) {
		t.Fatalf("history len=%d, want %d", len(got.History), len(messages))
	}
	if got.Plan.TokensAfter <= 0 {
		t.Fatalf("TokensAfter=%d, expected > 0", got.Plan.TokensAfter)
	}
}

func TestContextPlannerBuildLocalSkipsSubLLMSummarizer(t *testing.T) {
	calls := 0
	p := NewContextPlanner(func(ctx context.Context, text string, instruction string) (string, error) {
		calls++
		return "provider summary should not be used", nil
	})

	messages := []store.Message{
		{
			ID:      "old1",
			Seq:     1,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("old user context ", 800)),
		},
		{
			ID:      "old2",
			Seq:     2,
			Role:    types.RoleAssistant,
			Content: ptrToString(strings.Repeat("old assistant context ", 800)),
		},
		{
			ID:      "active",
			Seq:     3,
			Role:    types.RoleUser,
			Content: ptrToString("continue from the compact handover"),
		},
	}

	got, err := p.BuildLocal(context.Background(), messages, "active", "", ContextPolicy{
		MaxInputTokens:     1000,
		HardContextCap:     2000,
		SummaryChunkTokens: 200,
		MaxSummaryDepth:    2,
		CharsPerToken:      4,
	})
	if err != nil {
		t.Fatalf("BuildLocal() error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("BuildLocal() called provider summarizer %d times", calls)
	}
	if !got.Plan.HandoverSummaryUsed {
		t.Fatalf("BuildLocal() did not build handover summary for oversized context")
	}
	if !got.Plan.LocalFallbackUsed {
		t.Fatalf("BuildLocal() did not mark deterministic local fallback metadata: %#v", got.Plan)
	}
	if got.Plan.SummarySource != ContextSummarySourceDeterministicFallback {
		t.Fatalf("BuildLocal() SummarySource=%q, want %q", got.Plan.SummarySource, ContextSummarySourceDeterministicFallback)
	}
	if len(got.History) != 2 {
		t.Fatalf("BuildLocal() history len=%d, want compact handover history", len(got.History))
	}
}

func TestContextPlannerSummarizesWhenOverBudget(t *testing.T) {
	messages := make([]store.Message, 0, 12)
	for i := 0; i < 12; i++ {
		messages = append(messages, store.Message{
			ID:      fmt.Sprintf("m%d", i+1),
			Seq:     int64(i + 1),
			Role:    types.RoleUser,
			Content: ptrToString("old context chunk " + strings.Repeat("x", 500)),
		})
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "m12", "", ContextPolicy{
		MaxInputTokens:     200,
		HardContextCap:     4000,
		BaseWindowTurns:    4,
		SummaryChunkTokens: 80,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(got.Plan.SummarizedBuckets) == 0 {
		t.Fatalf("expected summarized buckets > 0, got %d", len(got.Plan.SummarizedBuckets))
	}
	if len(got.Plan.RetrievalHints) == 0 {
		t.Fatalf("expected retrieval hints")
	}
	if got.Plan.CompactionMode != handoverCompactionMode {
		t.Fatalf("CompactionMode=%q, want %q", got.Plan.CompactionMode, handoverCompactionMode)
	}
	if !got.Plan.HandoverSummaryUsed {
		t.Fatalf("expected HandoverSummaryUsed=true")
	}
	if got.Plan.HandoverSummaryChars <= 0 {
		t.Fatalf("HandoverSummaryChars=%d, want > 0", got.Plan.HandoverSummaryChars)
	}
	if !got.Plan.NeedRetrieval || !got.Plan.NeedRetrievalTooling {
		t.Fatalf("expected retrieval tooling enabled, got %#v", got.Plan)
	}
	if got.Plan.PlanReason != handoverCompactionMode {
		t.Fatalf("PlanReason=%q, want %q", got.Plan.PlanReason, handoverCompactionMode)
	}
	if len(got.History) != 2 {
		t.Fatalf("history len=%d, want 2", len(got.History))
	}
	if got.Checkpoint == nil {
		t.Fatalf("expected checkpoint payload")
	}
	if got.Plan.Checkpoint == nil {
		t.Fatalf("expected checkpoint payload on plan")
	}
	if got.Checkpoint.SourceMessageID != "m12" || got.Checkpoint.SourceMessageSeq != 12 {
		t.Fatalf("checkpoint source=(%q,%d), want (m12,12)", got.Checkpoint.SourceMessageID, got.Checkpoint.SourceMessageSeq)
	}
	if got.Checkpoint.Summary != got.SummaryForUser {
		t.Fatalf("checkpoint summary=%q, want SummaryForUser=%q", got.Checkpoint.Summary, got.SummaryForUser)
	}
	if got.Checkpoint.SummarySource == "" {
		t.Fatalf("checkpoint missing summary source metadata")
	}
	if got.Checkpoint.TokenEstimate != got.Plan.TokensAfter {
		t.Fatalf("checkpoint token estimate=%d, want plan TokensAfter=%d", got.Checkpoint.TokenEstimate, got.Plan.TokensAfter)
	}
	if got.Checkpoint.TokenEstimate > 200 {
		t.Fatalf("checkpoint token estimate=%d, want <= policy max 200", got.Checkpoint.TokenEstimate)
	}
	if !reflect.DeepEqual(got.Checkpoint.ReplacementHistory, got.History) {
		t.Fatalf("checkpoint replacement history does not match returned history")
	}
	if !reflect.DeepEqual(got.Plan.Checkpoint, got.Checkpoint) {
		t.Fatalf("plan checkpoint=%#v, want result checkpoint=%#v", got.Plan.Checkpoint, got.Checkpoint)
	}
	if got.History[0].Role != types.RoleAssistant || strings.TrimSpace(safeContent(got.History[0].Content)) == "" {
		t.Fatalf("expected first history message to be non-empty assistant handover summary, got %#v", got.History[0])
	}
	if got.History[1].Role != types.RoleUser {
		t.Fatalf("expected second history message role=user, got %#v", got.History[1].Role)
	}
}

func TestContextPlannerHandoverWhenPastRetrievalLimitInsideSoftBudget(t *testing.T) {
	messages := make([]store.Message, 0, 6)
	for i := 0; i < 6; i++ {
		messages = append(messages, store.Message{
			ID:      fmt.Sprintf("m%d", i+1),
			Seq:     int64(i + 1),
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("near-edge context ", 40)),
		})
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "m6", "", ContextPolicy{
		MaxInputTokens:     2000,
		HardContextCap:     4000,
		BaseWindowTurns:    12,
		SummaryChunkTokens: 200,
		NeedRetrievalLimit: 300,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if got.Plan.TokensBefore > 2000 {
		t.Fatalf("test setup exceeded soft budget: tokens_before=%d", got.Plan.TokensBefore)
	}
	if got.Plan.TokensBefore <= 300 {
		t.Fatalf("test setup did not cross retrieval limit: tokens_before=%d", got.Plan.TokensBefore)
	}
	if got.Plan.CompactionMode != handoverCompactionMode {
		t.Fatalf("CompactionMode=%q, want %q", got.Plan.CompactionMode, handoverCompactionMode)
	}
	if got.Plan.TokensAfter > 2000 {
		t.Fatalf("TokensAfter=%d, want <= soft budget", got.Plan.TokensAfter)
	}
	if !got.Plan.OverflowCompacted || !got.Plan.NeedRetrieval || !got.Plan.NeedRetrievalTooling {
		t.Fatalf("expected compacted retrieval handover, got %#v", got.Plan)
	}
	if len(got.History) != 2 {
		t.Fatalf("history len=%d, want compact handover history", len(got.History))
	}
	if got.Checkpoint == nil || got.Plan.Checkpoint == nil {
		t.Fatalf("expected durable checkpoint payload")
	}
}

func TestBuildContextCheckpointPayloadBoundsAndOrdersDeterministically(t *testing.T) {
	summaryMessages := []store.Message{
		{
			ID:                    "later",
			Seq:                   2,
			Role:                  types.RoleAssistant,
			Content:               ptrToString("later"),
			ReferencedArtifactIDs: []string{"art-b", "art-a"},
		},
		{
			ID:                    "first",
			Seq:                   1,
			Role:                  types.RoleUser,
			Content:               ptrToString("first"),
			ReferencedArtifactIDs: []string{"art-a", "art-c"},
		},
		{
			ID:                    "first",
			Seq:                   1,
			Role:                  types.RoleUser,
			Content:               ptrToString("duplicate id"),
			ReferencedArtifactIDs: []string{"art-c"},
		},
	}
	source := store.Message{
		ID:                    "source",
		Seq:                   3,
		Role:                  types.RoleUser,
		Content:               ptrToString("source"),
		ReferencedArtifactIDs: []string{"art-source", "art-b"},
	}
	replacement := []types.ChatMessage{
		{Role: types.RoleAssistant, Content: ptrToString(strings.Repeat("summary ", 200))},
	}
	for i := 0; i < 9; i++ {
		replacement = append(replacement, types.ChatMessage{
			Role:    types.RoleUser,
			Content: ptrToString(fmt.Sprintf("tail-%d %s", i, strings.Repeat("x", 80))),
		})
	}
	policy := ContextPolicy{
		MaxInputTokens: 80,
		HardContextCap: 80,
		CharsPerToken:  2,
	}

	got := buildContextCheckpointPayload(source, strings.Repeat("handover ", 200), summaryMessages, replacement, policy)
	again := buildContextCheckpointPayload(source, strings.Repeat("handover ", 200), summaryMessages, replacement, policy)
	if got == nil {
		t.Fatalf("expected checkpoint payload")
	}
	if !reflect.DeepEqual(got, again) {
		t.Fatalf("checkpoint helper is not deterministic:\nfirst=%#v\nagain=%#v", got, again)
	}
	if got.SourceMessageID != "source" || got.SourceMessageSeq != 3 {
		t.Fatalf("source=(%q,%d), want (source,3)", got.SourceMessageID, got.SourceMessageSeq)
	}
	if len(got.ReplacementHistory) != maxCheckpointHistoryMessages {
		t.Fatalf("replacement history len=%d, want %d", len(got.ReplacementHistory), maxCheckpointHistoryMessages)
	}
	if got.TokenEstimate > policy.MaxInputTokens {
		t.Fatalf("token estimate=%d, want <= %d", got.TokenEstimate, policy.MaxInputTokens)
	}
	if got.TokenEstimate != estimateHistoryTokens(got.ReplacementHistory, policy.CharsPerToken) {
		t.Fatalf("token estimate=%d, want recalculated %d", got.TokenEstimate, estimateHistoryTokens(got.ReplacementHistory, policy.CharsPerToken))
	}
	if !reflect.DeepEqual(got.ReferencedMessageIDs, []string{"first", "later"}) {
		t.Fatalf("referenced message ids=%#v, want ordered unique ids", got.ReferencedMessageIDs)
	}
	if !reflect.DeepEqual(got.ReferencedArtifactIDs, []string{"art-a", "art-c", "art-b", "art-source"}) {
		t.Fatalf("referenced artifact ids=%#v, want ordered unique ids", got.ReferencedArtifactIDs)
	}
	if got.CharsPerToken != policy.CharsPerToken {
		t.Fatalf("chars per token=%d, want %d", got.CharsPerToken, policy.CharsPerToken)
	}
}

func TestContextPlannerFallsBackWhenUserMessageMissing(t *testing.T) {
	messages := make([]store.Message, 0, 6)
	for i := 0; i < 6; i++ {
		messages = append(messages, store.Message{
			ID:      fmt.Sprintf("x%d", i+1),
			Seq:     int64(i + 1),
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("c", 450)),
		})
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "missing-user-id", "hidden", ContextPolicy{
		MaxInputTokens:  100,
		HardContextCap:  8000,
		BaseWindowTurns: 3,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if got.Plan.CompactionMode != "" {
		t.Fatalf("CompactionMode=%q, want empty fallback mode", got.Plan.CompactionMode)
	}
	if len(got.History) < 3 {
		t.Fatalf("expected legacy fallback history length >= 3, got %d", len(got.History))
	}
	if got.History[len(got.History)-1].Role != types.RoleUser {
		t.Fatalf("expected last history message role=user, got %#v", got.History[len(got.History)-1].Role)
	}
}

func TestContextPlannerSubLLMFailureFallsBackToDeterministicSummary(t *testing.T) {
	messages := []store.Message{
		{
			ID:      "u1",
			Seq:     1,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("a", 1200)),
		},
		{
			ID:      "a1",
			Seq:     2,
			Role:    types.RoleAssistant,
			Content: ptrToString(strings.Repeat("b", 1200)),
		},
		{
			ID:      "u2",
			Seq:     3,
			Role:    types.RoleUser,
			Content: ptrToString("current request"),
		},
	}

	p := NewContextPlanner(func(ctx context.Context, text string, instruction string) (string, error) {
		return "", errors.New("sub-llm unavailable")
	})
	got, err := p.Build(context.Background(), messages, "u2", "", ContextPolicy{
		MaxInputTokens:  120,
		HardContextCap:  8000,
		BaseWindowTurns: 2,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if len(got.History) != 2 {
		t.Fatalf("history len=%d, want 2", len(got.History))
	}
	if strings.TrimSpace(safeContent(got.History[0].Content)) == "" {
		t.Fatalf("expected non-empty deterministic handover summary fallback")
	}
	if got.Plan.CompactionMode != handoverCompactionMode || got.Plan.HandoverSummaryChars <= 0 {
		t.Fatalf("expected handover metadata despite summarizer failure, got %#v", got.Plan)
	}
}

func TestContextPlannerHardCapEnforced(t *testing.T) {
	messages := make([]store.Message, 0, 6)
	for i := 0; i < 6; i++ {
		messages = append(messages, store.Message{
			ID:      fmt.Sprintf("h%d", i),
			Seq:     int64(i + 1),
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("z", 400)),
		})
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "h6", "", ContextPolicy{
		MaxInputTokens:  4000,
		HardContextCap:  10,
		BaseWindowTurns: 2,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if !got.Plan.HardCapped {
		t.Fatalf("expected HardCapped=true, got plan=%+v (tokens_before=%d tokens_after=%d)", got.Plan, got.Plan.TokensBefore, got.Plan.TokensAfter)
	}
	if !got.Plan.NeedRetrieval {
		t.Fatalf("expected NeedRetrieval=true")
	}
}

func TestContextPlannerRequiresNeedRetrievalToolingOnHardCap(t *testing.T) {
	messages := []store.Message{
		{
			ID:      "old1",
			Seq:     1,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("x", 2400)),
		},
		{
			ID:      "old2",
			Seq:     2,
			Role:    types.RoleAssistant,
			Content: ptrToString(strings.Repeat("y", 2400)),
		},
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "old2", "", ContextPolicy{
		MaxInputTokens:  100,
		HardContextCap:  10,
		BaseWindowTurns: 1,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	if !got.Plan.HardCapped {
		t.Fatalf("expected HardCapped=true")
	}
	if !got.Plan.NeedRetrievalTooling {
		t.Fatalf("expected NeedRetrievalTooling=true, got %#v", got.Plan)
	}
	if !got.Plan.NeedRetrieval {
		t.Fatalf("expected NeedRetrieval=true")
	}
}

func TestContextPlannerHonorsCharsPerTokenForTokenCounts(t *testing.T) {
	messages := []store.Message{
		{
			ID:      "m1",
			Seq:     1,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("A", 200)),
		},
		{
			ID:      "m2",
			Seq:     2,
			Role:    types.RoleAssistant,
			Content: ptrToString(strings.Repeat("B", 200)),
		},
	}

	p := NewContextPlanner(nil)
	got, err := p.Build(context.Background(), messages, "m2", "", ContextPolicy{
		MaxInputTokens:  500,
		HardContextCap:  4000,
		BaseWindowTurns: 12,
		CharsPerToken:   2,
	})
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	wantBefore := estimateMessagesTokens(messages, 2)
	if got.Plan.TokensBefore != wantBefore {
		t.Fatalf("TokensBefore=%d, want %d", got.Plan.TokensBefore, wantBefore)
	}
	wantAfter := estimateHistoryTokens(got.History, 2)
	if got.Plan.TokensAfter != wantAfter {
		t.Fatalf("TokensAfter=%d, want %d", got.Plan.TokensAfter, wantAfter)
	}
	defaultBefore := estimateMessagesTokens(messages)
	if got.Plan.TokensBefore == defaultBefore {
		t.Fatalf("expected custom chars-per-token to change TokensBefore (default=%d custom=%d)", defaultBefore, got.Plan.TokensBefore)
	}
}

func TestContextPlannerHonorsCharsPerTokenForBudgeting(t *testing.T) {
	messages := []store.Message{
		{
			ID:      "m1",
			Seq:     1,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("a", 120)),
		},
		{
			ID:      "m2",
			Seq:     2,
			Role:    types.RoleAssistant,
			Content: ptrToString(strings.Repeat("b", 120)),
		},
		{
			ID:      "m3",
			Seq:     3,
			Role:    types.RoleUser,
			Content: ptrToString(strings.Repeat("c", 120)),
		},
	}

	p := NewContextPlanner(nil)
	defaultPlan, err := p.Build(context.Background(), messages, "m3", "", ContextPolicy{
		MaxInputTokens:  100,
		HardContextCap:  4000,
		BaseWindowTurns: 12,
	})
	if err != nil {
		t.Fatalf("default Build() error: %v", err)
	}
	if defaultPlan.Plan.PlanReason != "within_budget" {
		t.Fatalf("default plan reason=%q, want within_budget", defaultPlan.Plan.PlanReason)
	}
	if len(defaultPlan.History) != 3 {
		t.Fatalf("default history len=%d, want 3", len(defaultPlan.History))
	}

	customPlan, err := p.Build(context.Background(), messages, "m3", "", ContextPolicy{
		MaxInputTokens:  100,
		HardContextCap:  4000,
		BaseWindowTurns: 12,
		CharsPerToken:   2,
	})
	if err != nil {
		t.Fatalf("custom Build() error: %v", err)
	}
	if customPlan.Plan.TokensBefore <= defaultPlan.Plan.TokensBefore {
		t.Fatalf("expected higher TokensBefore with lower chars-per-token: default=%d custom=%d", defaultPlan.Plan.TokensBefore, customPlan.Plan.TokensBefore)
	}
	if customPlan.Plan.PlanReason == "within_budget" {
		t.Fatalf("expected custom plan to exceed budget, got reason=%q", customPlan.Plan.PlanReason)
	}
	if customPlan.Plan.CompactionMode != handoverCompactionMode {
		t.Fatalf("custom compaction mode=%q, want %q", customPlan.Plan.CompactionMode, handoverCompactionMode)
	}
	if len(customPlan.History) != 2 {
		t.Fatalf("custom history len=%d, want 2", len(customPlan.History))
	}
}

func TestCompactToolResultForModelCompactsLargeJSONEnvelope(t *testing.T) {
	rows := make([]any, 0, 80)
	for i := 0; i < 80; i++ {
		rows = append(rows, map[string]any{"id": i + 1})
	}
	env := map[string]any{
		"artifact_id":   "artifact-large",
		"tool_name":     "run_bigquery_query",
		"tool_call_id":  "tc-large",
		"ok":            true,
		"result_format": "json",
		"result": map[string]any{
			"rows": rows,
		},
	}
	encoded, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	raw := string(encoded)
	compacted := compactToolResultForModel(&raw)
	if compacted == nil || strings.TrimSpace(*compacted) == "" {
		t.Fatalf("compactToolResultForModel returned empty content")
	}
	parsed, ok := types.ParseToolResultEnvelope(*compacted)
	if !ok {
		t.Fatalf("failed to parse compacted tool result envelope: %s", *compacted)
	}
	resultMap, ok := parsed.Result.(map[string]any)
	if !ok {
		t.Fatalf("parsed result type=%T, want map[string]any", parsed.Result)
	}
	rowsOut, ok := resultMap["rows"].([]any)
	if !ok {
		t.Fatalf("parsed rows type=%T, want []any", resultMap["rows"])
	}
	if len(rowsOut) >= len(rows) {
		t.Fatalf("expected compacted rows length < %d, got %d", len(rows), len(rowsOut))
	}
}

func TestCompactToolResultForModelLeavesTextEnvelopeUnchanged(t *testing.T) {
	raw := `{"artifact_id":"artifact-text","tool_name":"read_file","ok":true,"result_format":"text","result":"` + strings.Repeat("x", 6000) + `"}`
	out := compactToolResultForModel(&raw)
	if out == nil {
		t.Fatalf("compactToolResultForModel returned nil")
	}
	if strings.TrimSpace(*out) != strings.TrimSpace(raw) {
		t.Fatalf("expected text envelope to remain unchanged")
	}
}

func ptrToString(s string) *string {
	return &s
}
