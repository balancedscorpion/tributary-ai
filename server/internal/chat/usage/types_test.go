package usage

import (
	"errors"
	"testing"
	"time"
)

func TestTokenTotalsPreservesPerCounterCoverage(t *testing.T) {
	input, output, total := int64(8), int64(2), int64(10)
	cached, reasoning := int64(3), int64(1)
	totalOnly := int64(5)
	var totals TokenTotals
	totals.AddInvocation(Invocation{
		InputTokens: &input, OutputTokens: &output, TotalTokens: &total, CachedInputTokens: &cached,
		UsageQuality: QualityProviderReported, Status: StatusCompleted,
	})
	totals.AddInvocation(Invocation{
		TotalTokens: &totalOnly, ReasoningTokens: &reasoning, UsageQuality: QualityProviderReported, Status: StatusCompleted,
	})
	totals.AddInvocation(Invocation{
		UsageQuality: QualityMissing, Status: StatusFailed,
	})

	if totals.InvocationCount != 3 || totals.CallsWithInputTokens != 1 || totals.CallsWithOutputTokens != 1 || totals.CallsWithTotalTokens != 2 {
		t.Fatalf("counter coverage = %#v", totals)
	}
	if totals.CallsWithUsage != 2 || totals.MissingUsageCalls != 1 {
		t.Fatalf("total-token coverage = %#v", totals)
	}
	if totals.CallsWithCachedInput != 1 || totals.CallsWithCacheWrite != 0 || totals.CallsWithReasoning != 1 {
		t.Fatalf("extended counter coverage = %#v", totals)
	}
	if totals.InputTokens != 8 || totals.OutputTokens != 2 || totals.TotalTokens != 15 || totals.FailedCalls != 1 {
		t.Fatalf("known sums = %#v", totals)
	}
}

func TestTokenTotalsKeepsProviderDerivedAndLowerBoundsSeparate(t *testing.T) {
	providerTotal, derivedTotal := int64(31), int64(30)
	lowerProvider := int64(7)
	input, output := int64(9), int64(4)
	var totals TokenTotals
	totals.AddInvocation(Invocation{
		ProviderTotalTokens: &providerTotal, DerivedTotalTokens: &derivedTotal,
		UsageQuality: QualityProviderReported, Status: StatusCompleted,
	})
	totals.AddInvocation(Invocation{
		ProviderTotalTokens: &lowerProvider, LowerBoundMask: CounterProviderTotal,
		UsageQuality: QualityLegacyPartial, Status: StatusInterrupted,
	})
	// Generic raw input/output counters provide a safe floor, but must not be
	// promoted to an exact derived total without declared token semantics.
	totals.AddInvocation(Invocation{
		InputTokens: &input, OutputTokens: &output,
		UsageQuality: QualityProviderReported, Status: StatusCompleted,
	})

	if totals.ProviderTotalTokens != 31 || totals.DerivedTotalTokens != 30 || totals.TotalTokens != 31 {
		t.Fatalf("exact total separation = %#v", totals)
	}
	if totals.CallsWithProviderTotalTokens != 1 || totals.CallsWithDerivedTotalTokens != 1 || totals.CallsWithTotalTokens != 1 {
		t.Fatalf("exact total coverage = %#v", totals)
	}
	if totals.LowerBoundProviderTotalTokens != 7 || totals.CallsWithLowerBoundProviderTotal != 1 || totals.LowerBoundTotalTokens != 20 || totals.CallsWithLowerBoundTotal != 2 || totals.LowerBoundCalls != 2 || totals.LegacyPartialCalls != 1 {
		t.Fatalf("lower-bound accounting = %#v", totals)
	}
	if totals.MissingUsageCalls != 0 || totals.InputTokens != 9 || totals.OutputTokens != 4 {
		t.Fatalf("missing/partial accounting = %#v", totals)
	}
}

func TestCanonicalAPISurfaceDisambiguatesBedrock(t *testing.T) {
	tests := []struct {
		provider string
		raw      string
		want     APISurface
	}{
		{"bedrock", "messages", APISurfaceInvokeStream},
		{"bedrock", "bedrock_native", APISurfaceInvokeStream},
		{"bedrock", "converse", APISurfaceConverseStream},
		{"anthropic", "messages", APISurfaceMessages},
		{"openai", "chat-completions", APISurfaceChatCompletions},
		{"openai", "images_api", APISurfaceImages},
		{"gemini", "stream_generate_content", APISurfaceGenerateContent},
	}
	for _, test := range tests {
		got, err := CanonicalAPISurface(test.provider, test.raw)
		if err != nil || got != test.want {
			t.Errorf("CanonicalAPISurface(%q, %q) = %q, %v; want %q", test.provider, test.raw, got, err, test.want)
		}
	}
	if _, err := CanonicalAPISurface("openai", "made_up_surface"); err == nil {
		t.Fatal("CanonicalAPISurface accepted an unknown surface")
	}
}

func TestQuerySafetyLimits(t *testing.T) {
	if err := validateQuery(Query{Limit: MaxQueryLimit + 1}); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized limit error = %v, want ErrLimit", err)
	}
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := validateQuery(Query{From: from, To: from.Add(MaxQueryRange + time.Second)}); !errors.Is(err, ErrLimit) {
		t.Fatalf("oversized range error = %v, want ErrLimit", err)
	}
}
