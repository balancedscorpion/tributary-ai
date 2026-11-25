package optionc

import (
	"context"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/usage"
)

func TestRefreshRunUsageRollupIncludesBackgroundCallsAndClearsPartialFields(t *testing.T) {
	ctx := context.Background()
	chatStore, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON store: %v", err)
	}
	usageRepository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	thread, err := chatStore.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := chatStore.CreateRun(ctx, thread.ID, "openai", "gpt-test", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if err := chatStore.UpdateRunStatus(ctx, run.ID, "completed", map[string]any{
		"input_tokens": 10, "output_tokens": 2, "total_tokens": 12,
	}); err != nil {
		t.Fatalf("complete run: %v", err)
	}
	attrCtx := usage.WithAttribution(ctx, usage.Attribution{RunID: run.ID, Operation: usage.OperationChat})
	seed := func(input, output, total *int64, quality usage.Quality) {
		t.Helper()
		invocation, err := usageRepository.Start(attrCtx, usage.Invocation{ProviderID: "openai", RequestedModel: "gpt-test"})
		if err != nil {
			t.Fatalf("Start invocation: %v", err)
		}
		if _, err := usageRepository.Finalize(ctx, invocation.ID, usage.Finalization{
			Status: usage.StatusCompleted, InputTokens: input, OutputTokens: output, TotalTokens: total, UsageQuality: quality,
		}); err != nil {
			t.Fatalf("Finalize invocation: %v", err)
		}
	}
	input, output, total := int64(10), int64(2), int64(12)
	seed(&input, &output, &total, usage.QualityProviderReported)
	refreshRunUsageRollup(ctx, chatStore, usageRepository, run.ID)

	input2, output2, total2 := int64(4), int64(1), int64(5)
	seed(&input2, &output2, &total2, usage.QualityProviderReported)
	refreshRunUsageRollup(ctx, chatStore, usageRepository, run.ID)
	got, err := chatStore.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if got.InputTokens == nil || *got.InputTokens != 14 || got.OutputTokens == nil || *got.OutputTokens != 3 || got.TotalTokens == nil || *got.TotalTokens != 17 {
		t.Fatalf("background-inclusive rollup = %#v", got)
	}

	seed(nil, nil, nil, usage.QualityMissing)
	refreshRunUsageRollup(ctx, chatStore, usageRepository, run.ID)
	got, err = chatStore.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun after partial call: %v", err)
	}
	if got.InputTokens != nil || got.OutputTokens != nil || got.TotalTokens != nil {
		t.Fatalf("partial rollup must clear exact legacy fields: %#v", got)
	}
}
