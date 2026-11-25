package optionc

import (
	"context"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/usage"
)

func TestRunUsageRollupSumsEveryProviderInvocation(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{
		RunID: "run-usage", ThreadID: "thread-usage", Operation: usage.OperationChat,
	})
	seedInvocation := func(status usage.Status, input, output int64) {
		t.Helper()
		started, err := repository.Start(ctx, usage.Invocation{
			ProviderID: "openai", RequestedModel: "gpt-test", StartedAt: time.Now().UTC().Add(-time.Minute),
		})
		if err != nil {
			t.Fatalf("Start: %v", err)
		}
		total := input + output
		if _, err := repository.Finalize(context.Background(), started.ID, usage.Finalization{
			Status: status, InputTokens: &input, OutputTokens: &output, TotalTokens: &total,
			UsageQuality: usage.QualityProviderReported, UpstreamRequestCount: 1,
		}); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
	}
	seedInvocation(usage.StatusCompleted, 10, 2)
	seedInvocation(usage.StatusFailed, 20, 3)

	manager := &Manager{usage: repository}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		fields := manager.withRunUsageRollup(context.Background(), "run-usage", status, map[string]any{
			"input_tokens": 999,
		})
		if fields["input_tokens"] != int64(30) || fields["output_tokens"] != int64(5) || fields["total_tokens"] != int64(35) {
			t.Fatalf("status=%s fields=%#v", status, fields)
		}
	}

	running := manager.withRunUsageRollup(context.Background(), "run-usage", "running", map[string]any{"input_tokens": 999})
	if running["input_tokens"] != 999 {
		t.Fatalf("running fields=%#v, want unchanged", running)
	}
}

func TestRunUsageRollupDoesNotTurnMissingUsageIntoZero(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{RunID: "run-missing", Operation: usage.OperationChat})
	started, err := repository.Start(ctx, usage.Invocation{ProviderID: "compatible", RequestedModel: "unknown-usage"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := repository.Finalize(context.Background(), started.ID, usage.Finalization{
		Status: usage.StatusCompleted, UsageQuality: usage.QualityMissing, UpstreamRequestCount: 1,
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	manager := &Manager{usage: repository}
	fields := manager.withRunUsageRollup(context.Background(), "run-missing", "completed", map[string]any{
		"ended_at": time.Now().UTC(), "input_tokens": 99, "output_tokens": 99, "total_tokens": 198,
	})
	if _, exists := fields["input_tokens"]; exists {
		t.Fatalf("fields=%#v, missing usage must remove partial fallback", fields)
	}
	if _, exists := fields["output_tokens"]; exists {
		t.Fatalf("fields=%#v, missing usage must remove partial fallback", fields)
	}
	if _, exists := fields["total_tokens"]; exists {
		t.Fatalf("fields=%#v, missing usage must remove partial fallback", fields)
	}
}

func TestRunUsageRollupOnlyWritesCountersWithCompleteCoverage(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{RunID: "run-partial", Operation: usage.OperationChat})
	for index, total := range []int64{12, 8} {
		started, err := repository.Start(ctx, usage.Invocation{ProviderID: "compatible", RequestedModel: "partial"})
		if err != nil {
			t.Fatalf("Start %d: %v", index, err)
		}
		final := usage.Finalization{Status: usage.StatusCompleted, TotalTokens: &total, UsageQuality: usage.QualityProviderReported}
		if index == 0 {
			input, output := int64(10), int64(2)
			final.InputTokens = &input
			final.OutputTokens = &output
		}
		if _, err := repository.Finalize(context.Background(), started.ID, final); err != nil {
			t.Fatalf("Finalize %d: %v", index, err)
		}
	}
	fields := (&Manager{usage: repository}).withRunUsageRollup(context.Background(), "run-partial", "completed", map[string]any{
		"input_tokens": 10, "output_tokens": 2, "total_tokens": 12,
	})
	if _, ok := fields["input_tokens"]; ok {
		t.Fatalf("partial input coverage was persisted: %#v", fields)
	}
	if _, ok := fields["output_tokens"]; ok {
		t.Fatalf("partial output coverage was persisted: %#v", fields)
	}
	if fields["total_tokens"] != int64(20) {
		t.Fatalf("complete total coverage = %#v, want 20", fields)
	}
}
