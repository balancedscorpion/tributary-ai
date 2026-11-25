package optionc

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestContextPlannerOrdersMessagesBySeqDeterministically(t *testing.T) {
	messages := []store.Message{
		{ID: "seq-3", Seq: 3, Role: types.RoleAssistant, Content: ptrToString("third")},
		{ID: "seq-2-b", Seq: 2, Role: types.RoleUser, Content: ptrToString("second-b")},
		{ID: "seq-1", Seq: 1, Role: types.RoleUser, Content: ptrToString("first")},
		{ID: "seq-2-a", Seq: 2, Role: types.RoleAssistant, Content: ptrToString("second-a")},
	}

	got := orderStoreMessagesBySeq(messages)
	gotIDs := idsFromMessages(got)
	wantIDs := []string{"seq-1", "seq-2-a", "seq-2-b", "seq-3"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("ordered ids=%#v, want %#v", gotIDs, wantIDs)
	}
	if !reflect.DeepEqual(idsFromMessages(messages), []string{"seq-3", "seq-2-b", "seq-1", "seq-2-a"}) {
		t.Fatalf("orderStoreMessagesBySeq mutated caller slice")
	}
}

func TestContextPlannerBuildFromCheckpointDeltaComposesReplacementHistoryWithOrderedDelta(t *testing.T) {
	checkpointSummary := "ACTIVE_CHECKPOINT_SUMMARY"
	checkpoint := &ContextCheckpointPayload{
		ReplacementHistory: []types.ChatMessage{
			{Role: types.RoleAssistant, Content: ptrToString(checkpointSummary)},
		},
		SourceMessageSeq:     10,
		SourceMessageID:      "checkpoint-source",
		Summary:              checkpointSummary,
		SummarySource:        ContextSummarySourceProvider,
		ReferencedMessageIDs: []string{"old-1", "old-2"},
		TokenEstimate:        12,
		CharsPerToken:        4,
	}
	oldRaw := "OLD_RAW_SHOULD_BE_EXCLUDED"
	delta := []store.Message{
		{ID: "assistant-12", Seq: 12, Role: types.RoleAssistant, Content: ptrToString("delta assistant")},
		{ID: "old-9", Seq: 9, Role: types.RoleUser, Content: ptrToString(oldRaw)},
		{ID: "user-11", Seq: 11, Role: types.RoleUser, Content: ptrToString("delta user")},
	}

	p := NewContextPlanner(func(ctx context.Context, text string, instruction string) (string, error) {
		t.Fatalf("BuildFromCheckpointDelta should not invoke summarizer")
		return "", nil
	})
	got, err := p.BuildFromCheckpointDelta(context.Background(), checkpoint, delta, "user-11", "hidden delta context", ContextPolicy{
		MaxInputTokens: 500,
		HardContextCap: 1000,
		CharsPerToken:  4,
	})
	if err != nil {
		t.Fatalf("BuildFromCheckpointDelta() error: %v", err)
	}

	if len(got.History) != 3 {
		t.Fatalf("history len=%d, want checkpoint plus two delta messages", len(got.History))
	}
	contents := historyContents(got.History)
	wantContents := []string{checkpointSummary, "delta user\n\nhidden delta context", "delta assistant"}
	if !reflect.DeepEqual(contents, wantContents) {
		t.Fatalf("history contents=%#v, want %#v", contents, wantContents)
	}
	if strings.Contains(strings.Join(contents, "\n"), oldRaw) {
		t.Fatalf("history included pre-checkpoint raw message")
	}
	if got.Plan.HistoryMode != ContextHistoryModeActiveCheckpointDelta {
		t.Fatalf("HistoryMode=%q, want %q", got.Plan.HistoryMode, ContextHistoryModeActiveCheckpointDelta)
	}
	if !got.Plan.ActiveCheckpointUsed || got.Plan.CheckpointSourceSeq != 10 || got.Plan.CheckpointSourceID != "checkpoint-source" {
		t.Fatalf("active checkpoint metadata missing: %#v", got.Plan)
	}
	if !reflect.DeepEqual(got.Plan.DeltaMessageIDs, []string{"user-11", "assistant-12"}) {
		t.Fatalf("delta ids=%#v, want ordered post-checkpoint ids", got.Plan.DeltaMessageIDs)
	}
	if !reflect.DeepEqual(idsFromMessages(got.DeltaMessages), []string{"user-11", "assistant-12"}) {
		t.Fatalf("result delta ids=%#v, want ordered post-checkpoint ids", idsFromMessages(got.DeltaMessages))
	}
	if got.ActiveCheckpoint == nil || got.Plan.ActiveCheckpoint == nil {
		t.Fatalf("expected active checkpoint to be carried in planner result and plan")
	}
}

func TestContextPlannerBuildLocalFromInputSkipsSubLLMSummarizerAndMarksFallback(t *testing.T) {
	calls := 0
	p := NewContextPlanner(func(ctx context.Context, text string, instruction string) (string, error) {
		calls++
		return "provider summary should not be used", nil
	})
	messages := []store.Message{
		{ID: "old-1", Seq: 1, Role: types.RoleUser, Content: ptrToString(strings.Repeat("old user context ", 800))},
		{ID: "old-2", Seq: 2, Role: types.RoleAssistant, Content: ptrToString(strings.Repeat("old assistant context ", 800))},
		{ID: "active", Seq: 3, Role: types.RoleUser, Content: ptrToString("continue from the compact handover")},
	}

	got, err := p.BuildLocalFromInput(context.Background(), ContextBuildInput{
		RawMessages:   messages,
		UserMessageID: "active",
		Policy: ContextPolicy{
			MaxInputTokens:     1000,
			HardContextCap:     2000,
			SummaryChunkTokens: 200,
			MaxSummaryDepth:    2,
			CharsPerToken:      4,
		},
	})
	if err != nil {
		t.Fatalf("BuildLocalFromInput() error: %v", err)
	}
	if calls != 0 {
		t.Fatalf("BuildLocalFromInput() called provider summarizer %d times", calls)
	}
	if !got.Plan.LocalFallbackUsed {
		t.Fatalf("expected deterministic local fallback metadata, got %#v", got.Plan)
	}
	if got.Plan.SummarySource != ContextSummarySourceDeterministicFallback {
		t.Fatalf("SummarySource=%q, want %q", got.Plan.SummarySource, ContextSummarySourceDeterministicFallback)
	}
	if got.Checkpoint == nil || got.Checkpoint.SummarySource != ContextSummarySourceDeterministicFallback {
		t.Fatalf("checkpoint summary source=%#v, want deterministic local fallback", got.Checkpoint)
	}
}

func historyContents(history []types.ChatMessage) []string {
	out := make([]string, 0, len(history))
	for _, msg := range history {
		out = append(out, safeContent(msg.Content))
	}
	return out
}
