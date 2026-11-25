package usage

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONRepositoryLifecyclePersistsGlobalDatePartition(t *testing.T) {
	ctx := WithAttribution(context.Background(), Attribution{
		RunID:            "run-1",
		ThreadID:         "thread-1",
		Operation:        OperationChat,
		ProviderConfigID: "profile-1",
		APIType:          "responses",
	})
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	startedAt := time.Date(2026, 7, 10, 23, 59, 0, 0, time.UTC)
	repo.now = func() time.Time { return startedAt }
	created, err := repo.Start(ctx, Invocation{
		ID:             "invocation-1",
		ProviderID:     "openai",
		RequestedModel: "gpt-5",
		SanitizedDetails: map[string]int64{
			"numeric_at_start":  1,
			"unsafe detail key": 2,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if created.Status != StatusRunning || created.UsageQuality != QualityMissing {
		t.Fatalf("created lifecycle = %q/%q", created.Status, created.UsageQuality)
	}
	if created.InputTokens != nil || created.SanitizedDetails["numeric_at_start"] != 1 || len(created.SanitizedDetails) != 1 {
		t.Fatalf("start persisted usage before completion: %#v", created)
	}
	if stringValue(created.RunID) != "run-1" || stringValue(created.ThreadID) != "thread-1" {
		t.Fatalf("created attribution = %#v", created)
	}
	wantPath := filepath.Join(repo.cfgDir, "chatdb", "usage", "2026-07-10", "invocation-1.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Fatalf("global date-partitioned record missing at %s: %v", wantPath, err)
	}
	retried, err := repo.Start(ctx, Invocation{ID: created.ID, ProviderID: "openai", RequestedModel: "gpt-5"})
	if err != nil || retried.ID != created.ID {
		t.Fatalf("idempotent duplicate Start = %#v, %v", retried, err)
	}
	if _, err := repo.Start(ctx, Invocation{ID: created.ID, ProviderID: "openai", RequestedModel: "different-model"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched duplicate Start error = %v, want ErrConflict", err)
	}

	endedAt := startedAt.Add(2 * time.Minute)
	repo.now = func() time.Time { return endedAt }
	input, output, total, cached, reasoning := int64(100), int64(25), int64(125), int64(40), int64(5)
	requestID := "req-123"
	resolvedModel := "gpt-5-2026-06-01"
	finalized, err := repo.Finalize(ctx, created.ID, Finalization{
		Status:               StatusCompleted,
		ResolvedModel:        &resolvedModel,
		InputTokens:          &input,
		OutputTokens:         &output,
		TotalTokens:          &total,
		CachedInputTokens:    &cached,
		ReasoningTokens:      &reasoning,
		UsageQuality:         QualityProviderReported,
		UpstreamRequestCount: 2,
		ProviderRequestID:    &requestID,
		SanitizedDetails: map[string]int64{
			"accepted_prediction_tokens": 3,
		},
	})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if finalized.EndedAt == nil || !finalized.EndedAt.Equal(endedAt) {
		t.Fatalf("EndedAt = %v, want %v", finalized.EndedAt, endedAt)
	}
	if finalized.TotalTokens == nil || *finalized.TotalTokens != 125 || finalized.UpstreamRequestCount != 2 {
		t.Fatalf("finalized usage = %#v", finalized)
	}

	// Finalization is exactly-once/idempotent: a later attempt cannot rewrite
	// the first terminal accounting result.
	differentTotal := int64(999)
	if _, err := repo.Finalize(ctx, created.ID, Finalization{Status: StatusFailed, TotalTokens: &differentTotal}); !errors.Is(err, ErrConflict) {
		t.Fatalf("mismatched Finalize error = %v, want ErrConflict", err)
	}

	reopened, err := NewJSONRepository(repo.cfgDir)
	if err != nil {
		t.Fatalf("reopen NewJSONRepository: %v", err)
	}
	got, err := reopened.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("reopened Get: %v", err)
	}
	if got.DisplayModel() != resolvedModel || got.SanitizedDetails["accepted_prediction_tokens"] != 3 {
		t.Fatalf("reopened record = %#v", got)
	}
	totals, err := reopened.SumByRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("SumByRun: %v", err)
	}
	if totals.InvocationCount != 1 || totals.TotalTokens != 125 || totals.ProviderReportedCalls != 1 || totals.CallsWithUsage != 1 {
		t.Fatalf("SumByRun totals = %#v", totals)
	}
	earliest, err := reopened.EarliestTrackingDate(ctx)
	if err != nil {
		t.Fatalf("EarliestTrackingDate: %v", err)
	}
	if earliest == nil || !earliest.Equal(startedAt) {
		t.Fatalf("earliest = %v, want %v", earliest, startedAt)
	}
}

func TestJSONRepositoryListRangeFiltersAndMissingUsage(t *testing.T) {
	ctx := context.Background()
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID: "inv-a", RunID: stringPointer("run-a"), Operation: OperationChat,
		ProviderID: "openai", RequestedModel: "gpt-5", StartedAt: base.Add(time.Hour),
	}, Finalization{Status: StatusCompleted, EndedAt: base.Add(2 * time.Hour), TotalTokens: int64Pointer(10), UsageQuality: QualityProviderReported})
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID: "inv-b", RunID: stringPointer("run-a"), Operation: OperationToolFollowup,
		ProviderID: "anthropic", RequestedModel: "claude", StartedAt: base.Add(24 * time.Hour),
	}, Finalization{Status: StatusFailed, EndedAt: base.Add(25 * time.Hour), UsageQuality: QualityMissing})
	createFinalizedJSONInvocation(t, repo, Invocation{
		ID: "inv-c", RunID: stringPointer("run-c"), Operation: OperationChat,
		ProviderID: "openai", RequestedModel: "gpt-5", StartedAt: base.Add(48 * time.Hour),
	}, Finalization{Status: StatusCancelled, EndedAt: base.Add(49 * time.Hour), TotalTokens: int64Pointer(7), UsageQuality: QualityDerived})

	listed, err := repo.List(ctx, Query{
		From:       base.Add(24 * time.Hour),
		To:         base.Add(48 * time.Hour),
		RangeField: RangeByEndedAt,
		ProviderID: "anthropic",
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "inv-b" {
		t.Fatalf("filtered List = %#v, want inv-b", listed)
	}

	totals, err := repo.SumByRun(ctx, "run-a")
	if err != nil {
		t.Fatalf("SumByRun: %v", err)
	}
	if totals.InvocationCount != 2 || totals.TotalTokens != 10 || totals.MissingUsageCalls != 1 || totals.FailedCalls != 1 {
		t.Fatalf("run-a totals = %#v", totals)
	}
}

func TestJSONRepositoryRejectsInvalidFinalizationWithoutChangingRecord(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	started := time.Date(2026, 7, 1, 2, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return started }
	created, err := repo.Start(context.Background(), Invocation{ID: "invalid-final", ProviderID: "openai", RequestedModel: "gpt-5"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	negative := int64(-1)
	if _, err := repo.Finalize(context.Background(), created.ID, Finalization{Status: StatusCompleted, TotalTokens: &negative}); err == nil {
		t.Fatal("Finalize accepted a negative token count")
	}
	got, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != StatusRunning || got.EndedAt != nil {
		t.Fatalf("invalid Finalize changed record: %#v", got)
	}
}

func TestJSONRepositorySnapshotJournalPreservesPartialUsage(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	started := time.Date(2026, 7, 11, 8, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return started }
	created, err := repo.Start(context.Background(), Invocation{
		ID: "journal-partial", ProviderID: "bedrock", ConnectionID: stringPointer("bedrock-prod"),
		PlanID: stringPointer("plan-1"), ConfigFingerprint: stringPointer("sha256:abc"),
		PlannedAPISurface: stringPointer("messages"), RequestedModel: "claude-sonnet",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := stringValue(created.PlannedAPISurface); got != string(APISurfaceInvokeStream) {
		t.Fatalf("planned surface = %q", got)
	}
	input := int64(10)
	snapshot := UsageSnapshot{Sequence: 1, ObservedAt: started.Add(time.Second), InputTokens: &input,
		UsageQuality: QualityLegacyPartial, UpstreamRequestCount: 1}
	first, err := repo.Snapshot(context.Background(), created.ID, snapshot)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if first.InputTokens == nil || *first.InputTokens != 10 || !first.LowerBoundMask.Has(CounterInput) {
		t.Fatalf("first snapshot = %#v", first)
	}
	retried, err := repo.Snapshot(context.Background(), created.ID, snapshot)
	if err != nil || retried.SnapshotSequence != 1 {
		t.Fatalf("idempotent snapshot = %#v, %v", retried, err)
	}
	different := int64(11)
	conflicting := snapshot
	conflicting.InputTokens = &different
	if _, err := repo.Snapshot(context.Background(), created.ID, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting snapshot error = %v, want ErrConflict", err)
	}

	providerTotal := int64(18)
	second, err := repo.Snapshot(context.Background(), created.ID, UsageSnapshot{
		Sequence: 2, ObservedAt: started.Add(2 * time.Second), InputTokens: &different,
		ProviderTotalTokens: &providerTotal, LowerBoundMask: CounterInput | CounterProviderTotal,
		UsageQuality: QualityLegacyPartial, UpstreamRequestCount: 2,
	})
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	if second.UpstreamRequestCount != 2 || second.ProviderTotalTokens == nil || *second.ProviderTotalTokens != 18 {
		t.Fatalf("second snapshot = %#v", second)
	}
	// A lower sequence is a safe no-op even when its payload is stale.
	stale, err := repo.Snapshot(context.Background(), created.ID, snapshot)
	if err != nil || stale.SnapshotSequence != 2 || *stale.InputTokens != 11 {
		t.Fatalf("stale snapshot = %#v, %v", stale, err)
	}
	decreased := int64(9)
	if _, err := repo.Snapshot(context.Background(), created.ID, UsageSnapshot{
		Sequence: 3, ObservedAt: started.Add(2500 * time.Millisecond), InputTokens: &decreased,
		UsageQuality: QualityLegacyPartial, UpstreamRequestCount: 2,
	}); err == nil {
		t.Fatal("Snapshot accepted a decreasing cumulative counter")
	}

	rawCode := "request cancelled: user@example.com"
	rawMessage := "secret prompt must never be persisted"
	final := Finalization{Sequence: 3, Status: StatusInterrupted, EndedAt: started.Add(3 * time.Second),
		UsageQuality: QualityLegacyPartial, ErrorCode: &rawCode, ErrorMessage: &rawMessage}
	finished, err := repo.Finalize(context.Background(), created.ID, final)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if finished.InputTokens == nil || *finished.InputTokens != 11 || finished.UpstreamRequestCount != 2 || !finished.LowerBoundMask.Has(CounterProviderTotal) {
		t.Fatalf("partial usage was not preserved: %#v", finished)
	}
	if finished.ErrorCode == nil || *finished.ErrorCode != "ABORTED" || finished.ErrorMessage != nil {
		t.Fatalf("error sanitization = %#v/%#v", finished.ErrorCode, finished.ErrorMessage)
	}
	if _, err := repo.Finalize(context.Background(), created.ID, final); err != nil {
		t.Fatalf("idempotent Finalize: %v", err)
	}

	contents, err := os.ReadFile(filepath.Join(repo.cfgDir, "chatdb", "usage", "2026-07-11", "journal-partial.json"))
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	text := string(contents)
	for _, forbidden := range []string{"provider_config_id", "\"api_type\":", "\"total_tokens\":", "error_message", rawMessage, "user@example.com"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("durable ledger leaked %q: %s", forbidden, text)
		}
	}
}

func TestJSONRepositoryPreservesExplicitZeroAndDoesNotDeriveTotals(t *testing.T) {
	repo, err := NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	started := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	repo.now = func() time.Time { return started }
	created, err := repo.Start(context.Background(), Invocation{ID: "zero-vs-nil", ProviderID: "openai", RequestedModel: "gpt-5"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	zero, output := int64(0), int64(4)
	finished, err := repo.Finalize(context.Background(), created.ID, Finalization{Status: StatusCompleted, EndedAt: started.Add(time.Second),
		InputTokens: &zero, OutputTokens: &output, UsageQuality: QualityProviderReported, UpstreamRequestCount: 1})
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if finished.InputTokens == nil || *finished.InputTokens != 0 || finished.ProviderTotalTokens != nil || finished.DerivedTotalTokens != nil || finished.TotalTokens != nil {
		t.Fatalf("zero/nil or total semantics lost: %#v", finished)
	}
	totals, err := repo.SumByRun(context.Background(), "missing-run")
	if err != nil || totals.InvocationCount != 0 {
		t.Fatalf("empty SumByRun = %#v, %v", totals, err)
	}
}

func TestJSONRepositoryUpgradeScrubsLegacyRawErrorsAndMarksPartialFailure(t *testing.T) {
	cfgDir := t.TempDir()
	started := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	ended := started.Add(time.Second)
	path := filepath.Join(cfgDir, "chatdb", "usage", "2026-07-11", "legacy-error.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	legacy := map[string]any{
		"kind": jsonRecordKind, "schema_version": 1,
		"invocation": map[string]any{
			"id": "legacy-error", "operation": "chat", "provider_id": "compatible", "provider_config_id": "legacy-default", "profile_id": "legacy-profile",
			"api_type": "responses", "requested_model": "gpt-test", "status": "failed",
			"started_at": started, "ended_at": ended, "updated_at": ended,
			"input_tokens": 9, "total_tokens": 12, "usage_quality": "provider_reported", "upstream_request_count": 1,
			"error_code": "PROVIDER_ERROR", "error_message": "secret prompt user@example.com",
		},
	}
	raw, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	repo, err := NewJSONRepository(cfgDir)
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	inv, err := repo.Get(context.Background(), "legacy-error")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if inv.UsageQuality != QualityLegacyPartial || !inv.LowerBoundMask.Has(CounterInput) || !inv.LowerBoundMask.Has(CounterProviderTotal) {
		t.Fatalf("legacy failed counters were not lower-bound: %#v", inv)
	}
	if inv.ConnectionID != nil || inv.ProfileID == nil || *inv.ProfileID != "legacy-profile" || inv.EffectiveAPISurface != nil {
		t.Fatalf("legacy dual identity/effective surface was not canonicalized: %#v", inv)
	}
	upgraded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(upgraded)
	for _, forbidden := range []string{"error_message", "secret prompt", "user@example.com", "provider_config_id", "connection_id"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("upgraded file retained %q: %s", forbidden, text)
		}
	}
	var envelope map[string]any
	if err := json.Unmarshal(upgraded, &envelope); err != nil {
		t.Fatalf("decode upgraded file: %v", err)
	}
	if version, ok := envelope["schema_version"].(float64); !ok || version != jsonRecordSchemaVersion {
		t.Fatalf("legacy file was not rewritten to schema v2: %s", text)
	}
}

func createFinalizedJSONInvocation(t *testing.T, repo *JSONRepository, inv Invocation, final Finalization) Invocation {
	t.Helper()
	repo.now = func() time.Time { return inv.StartedAt }
	created, err := repo.Start(context.Background(), inv)
	if err != nil {
		t.Fatalf("Start(%s): %v", inv.ID, err)
	}
	repo.now = func() time.Time { return final.EndedAt }
	finalized, err := repo.Finalize(context.Background(), created.ID, final)
	if err != nil {
		t.Fatalf("Finalize(%s): %v", inv.ID, err)
	}
	return finalized
}

func stringPointer(value string) *string { return &value }
func int64Pointer(value int64) *int64    { return &value }
