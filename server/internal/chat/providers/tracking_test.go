package providers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
)

type trackingScriptedProvider struct {
	events []types.StreamEvent
	err    error
}

type trackingContextProvider struct{}

type trackingTelemetryContextProvider struct{}

type trackingIncompleteCumulativeProvider struct{}

type trackingDispatchGateProvider struct {
	beforeDispatch func()
	dispatched     atomic.Int64
}

func (p *trackingDispatchGateProvider) ID() string    { return "openai" }
func (p *trackingDispatchGateProvider) Model() string { return "gpt-test" }
func (p *trackingDispatchGateProvider) StreamChat(_ context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	if p.beforeDispatch != nil {
		p.beforeDispatch()
	}
	if err := types.NotifyUsageDispatch(req, "responses"); err != nil {
		return nil, err
	}
	p.dispatched.Add(1)
	stream := make(chan types.StreamEvent)
	close(stream)
	return stream, nil
}

func (trackingTelemetryContextProvider) ID() string    { return "anthropic" }
func (trackingTelemetryContextProvider) Model() string { return "requested-model" }
func (trackingTelemetryContextProvider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	types.NotifyUsageDispatch(req, "messages")
	input, cached, output, derived := 10, 4, 2, 16
	types.NotifyUsageSnapshot(req, types.ResponseMeta{
		InputTokens: &input, OutputTokens: &output, CachedInputTokens: &cached, DerivedTotalTokens: &derived,
		UsageQuality: types.UsageQualityDerived, UsageSemantics: types.UsageSemanticsAnthropic,
	})
	stream := make(chan types.StreamEvent)
	go func() {
		defer close(stream)
		<-ctx.Done()
	}()
	return stream, nil
}

func (trackingIncompleteCumulativeProvider) ID() string    { return "anthropic" }
func (trackingIncompleteCumulativeProvider) Model() string { return "requested-model" }
func (trackingIncompleteCumulativeProvider) StreamChat(_ context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	if err := types.NotifyUsageDispatch(req, "messages"); err != nil {
		return nil, err
	}
	inputFirst, outputFirst, derivedFirst := 10, 2, 12
	_ = types.NotifyUsageSnapshot(req, types.ResponseMeta{
		InputTokens: &inputFirst, OutputTokens: &outputFirst, DerivedTotalTokens: &derivedFirst,
		UsageQuality: types.UsageQualityDerived, UsageSemantics: types.UsageSemanticsAnthropic,
	})
	if err := types.NotifyUsageDispatch(req, "messages"); err != nil {
		return nil, err
	}
	inputCumulative, upstream := 30, 2
	_ = types.NotifyUsageSnapshot(req, types.ResponseMeta{
		InputTokens: &inputCumulative, UsageQuality: types.UsageQualityDerived, UsageSemantics: types.UsageSemanticsAnthropic,
	})
	stream := make(chan types.StreamEvent, 1)
	stream <- types.StreamEvent{Event: types.EventFinal, Data: types.FinalData{Answer: "ok", Meta: types.ResponseMeta{
		InputTokens: &inputCumulative, UsageQuality: types.UsageQualityDerived, UsageSemantics: types.UsageSemanticsAnthropic,
		UpstreamRequestCount: &upstream,
	}}}
	close(stream)
	return stream, nil
}

func (trackingContextProvider) ID() string    { return "test-provider" }
func (trackingContextProvider) Model() string { return "requested-model" }
func (trackingContextProvider) StreamChat(ctx context.Context, _ types.ChatRequest) (<-chan types.StreamEvent, error) {
	stream := make(chan types.StreamEvent)
	go func() {
		defer close(stream)
		<-ctx.Done()
	}()
	return stream, nil
}

func (p trackingScriptedProvider) ID() string    { return "test-provider" }
func (p trackingScriptedProvider) Model() string { return "requested-model" }
func (p trackingScriptedProvider) StreamChat(ctx context.Context, _ types.ChatRequest) (<-chan types.StreamEvent, error) {
	if p.err != nil {
		return nil, p.err
	}
	stream := make(chan types.StreamEvent, len(p.events))
	for _, event := range p.events {
		stream <- event
	}
	close(stream)
	return stream, nil
}

func TestTrackingProviderPersistsTerminalUsageWithAttribution(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	input, output, total := 11, 7, 18
	cached, reasoning, upstream := 3, 2, 2
	requestID := "request-1"
	resolved := "resolved-model-2026-07-01"
	inner := trackingScriptedProvider{events: []types.StreamEvent{{
		Event: types.EventFinal,
		Data: types.FinalData{Answer: "ok", Meta: types.ResponseMeta{
			ProviderID:           "test-provider",
			Model:                "requested-model",
			ResolvedModel:        &resolved,
			RequestID:            &requestID,
			InputTokens:          &input,
			OutputTokens:         &output,
			TotalTokens:          &total,
			CachedInputTokens:    &cached,
			ReasoningTokens:      &reasoning,
			UsageQuality:         types.UsageQualityProviderReported,
			UsageDetails:         map[string]int{"accepted_prediction_tokens": 4},
			UpstreamRequestCount: &upstream,
		}},
	}}}

	provider := newTrackingProvider(inner, repository, "factory-config", "", "fingerprint-1", "responses", "responses")
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{
		RunID:        "run-1",
		ThreadID:     "thread-1",
		Operation:    usage.OperationChat,
		ConnectionID: "run-config",
	})
	stream, err := provider.StreamChat(ctx, types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range stream {
	}

	records, err := repository.List(context.Background(), usage.Query{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d, want 1", len(records))
	}
	got := records[0]
	if got.Status != usage.StatusCompleted || got.UsageQuality != usage.QualityProviderReported {
		t.Fatalf("status/quality=%s/%s", got.Status, got.UsageQuality)
	}
	if got.RunID == nil || *got.RunID != "run-1" || got.ThreadID == nil || *got.ThreadID != "thread-1" {
		t.Fatalf("attribution=%#v/%#v", got.RunID, got.ThreadID)
	}
	if got.ConnectionID == nil || *got.ConnectionID != "factory-config" {
		t.Fatalf("connection_id=%#v, want actual factory-config", got.ConnectionID)
	}
	if got.APIType == nil || *got.APIType != "responses" {
		t.Fatalf("api_type=%#v, want responses", got.APIType)
	}
	if got.ResolvedModel == nil || *got.ResolvedModel != resolved || got.ProviderRequestID == nil || *got.ProviderRequestID != requestID {
		t.Fatalf("resolved/request=%#v/%#v", got.ResolvedModel, got.ProviderRequestID)
	}
	if got.InputTokens == nil || *got.InputTokens != 11 || got.OutputTokens == nil || *got.OutputTokens != 7 || got.TotalTokens == nil || *got.TotalTokens != 18 {
		t.Fatalf("tokens=%#v/%#v/%#v", got.InputTokens, got.OutputTokens, got.TotalTokens)
	}
	if got.CachedInputTokens == nil || *got.CachedInputTokens != 3 || got.ReasoningTokens == nil || *got.ReasoningTokens != 2 {
		t.Fatalf("cached/reasoning=%#v/%#v", got.CachedInputTokens, got.ReasoningTokens)
	}
	if got.UpstreamRequestCount != 2 || got.SanitizedDetails["accepted_prediction_tokens"] != 4 {
		t.Fatalf("upstream/details=%d/%#v", got.UpstreamRequestCount, got.SanitizedDetails)
	}
	if got.EndedAt == nil || got.EndedAt.Before(got.StartedAt) {
		t.Fatalf("terminal observation timestamp=%v started=%v", got.EndedAt, got.StartedAt)
	}
}

func TestTrackingProviderNeverBorrowsFingerprintAcrossConnectionIdentity(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	provider := newTrackingProvider(trackingScriptedProvider{events: []types.StreamEvent{{
		Event: types.EventFinal, Data: types.FinalData{Answer: "ok", Meta: types.ResponseMeta{}},
	}}}, repository, "connection-b", "", "", "", "responses")
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{
		RunID: "run-a", ConnectionID: "connection-a", ConfigFingerprint: "fingerprint-a", PlannedAPISurface: "chat_completions", Operation: usage.OperationChat,
	})
	stream, err := provider.StreamChat(ctx, types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range stream {
	}
	records, err := repository.List(context.Background(), usage.Query{Limit: 10})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	if records[0].ConnectionID == nil || *records[0].ConnectionID != "connection-b" || records[0].ConfigFingerprint != nil {
		t.Fatalf("cross-target provenance contamination: %#v", records[0])
	}
	if records[0].PlannedAPISurface == nil || *records[0].PlannedAPISurface != "responses" {
		t.Fatalf("cross-target planned surface contamination: %#v", records[0].PlannedAPISurface)
	}
}

func TestTrackingProviderPersistsLegacyProfileIdentity(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	effectiveAPIType := "responses"
	upstream := 1
	provider := newTrackingProvider(trackingScriptedProvider{events: []types.StreamEvent{{
		Event: types.EventFinal,
		Data:  types.FinalData{Answer: "ok", Meta: types.ResponseMeta{APIType: &effectiveAPIType, UpstreamRequestCount: &upstream}},
	}}}, repository, "", "legacy-profile", "", "chat_completions", "chat_completions")
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{ConnectionID: "parent-config"})
	stream, err := provider.StreamChat(ctx, types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range stream {
	}
	records, err := repository.List(context.Background(), usage.Query{})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	got := records[0]
	if got.ProfileID == nil || *got.ProfileID != "legacy-profile" || got.ConnectionID != nil {
		t.Fatalf("legacy selection identity=%#v", got)
	}
	if got.APIType == nil || *got.APIType != effectiveAPIType {
		t.Fatalf("legacy api_type=%#v", got.APIType)
	}
}

func TestTrackingProviderRecordsSynchronousFailure(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	provider := newTrackingProvider(trackingScriptedProvider{err: errors.New("dial failed")}, repository, "", "", "", "responses", "responses")
	if _, err := provider.StreamChat(context.Background(), types.ChatRequest{Model: "requested-model"}); err == nil {
		t.Fatal("StreamChat error=nil, want failure")
	}
	records, err := repository.List(context.Background(), usage.Query{})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	if records[0].Status != usage.StatusFailed || records[0].UsageQuality != usage.QualityMissing {
		t.Fatalf("status/quality=%s/%s", records[0].Status, records[0].UsageQuality)
	}
	if records[0].UpstreamRequestCount != 0 || records[0].ResolvedModel != nil {
		t.Fatalf("pre-dispatch failure fabricated request/model: %#v", records[0])
	}
	if records[0].EffectiveAPISurface != nil {
		t.Fatalf("pre-dispatch failure fabricated effective surface: %#v", records[0].EffectiveAPISurface)
	}
}

func TestTrackingProviderPreservesCancellationLowerBound(t *testing.T) {
	primary, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	recorder, err := usage.NewRecorder(t.TempDir(), primary)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	provider := newTrackingProvider(trackingTelemetryContextProvider{}, recorder, "connection-a", "", "fingerprint-a", "messages", "messages")
	attributed := usage.WithAttribution(context.Background(), usage.Attribution{RunID: "run-cancel", Operation: usage.OperationChat})
	ctx, cancel := context.WithCancel(attributed)
	stream, err := provider.StreamChat(ctx, types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	cancel()
	for range stream {
	}
	records, err := primary.List(context.Background(), usage.Query{})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	got := records[0]
	if got.Status != usage.StatusCancelled || got.InputTokens == nil || *got.InputTokens != 10 ||
		got.DerivedTotalTokens == nil || *got.DerivedTotalTokens != 16 || got.UpstreamRequestCount != 1 ||
		!got.LowerBoundMask.Has(usage.CounterInput) || !got.LowerBoundMask.Has(usage.CounterDerivedTotal) {
		t.Fatalf("cancelled lower-bound invocation=%#v", got)
	}
	if got.TokenSemanticsProfile != usage.TokenSemanticsAnthropicV1 {
		t.Fatalf("token semantics profile=%q, want anthropic_v1", got.TokenSemanticsProfile)
	}
	totals, err := primary.SumByRun(context.Background(), "run-cancel")
	if err != nil || totals.InvocationCount != 1 || totals.CallsWithDerivedTotalTokens != 0 ||
		totals.DerivedTotalTokens != 0 || totals.LowerBoundDerivedTotalTokens != 16 {
		t.Fatalf("unexpected rollup=%#v err=%v", totals, err)
	}
}

func TestTrackingProviderLaterMissingCumulativeCounterRemainsLowerBound(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	provider := newTrackingProvider(trackingIncompleteCumulativeProvider{}, repository, "connection-a", "", "fingerprint-a", "messages", "messages")
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{RunID: "run-incomplete", Operation: usage.OperationChat})
	stream, err := provider.StreamChat(ctx, types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range stream {
	}
	records, err := repository.List(context.Background(), usage.Query{Limit: 10})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%#v err=%v", records, err)
	}
	got := records[0]
	if got.Status != usage.StatusCompleted || got.InputTokens == nil || *got.InputTokens != 30 || got.LowerBoundMask.Has(usage.CounterInput) {
		t.Fatalf("complete cumulative input was not exact: %#v", got)
	}
	if got.OutputTokens == nil || *got.OutputTokens != 2 || !got.LowerBoundMask.Has(usage.CounterOutput) ||
		got.DerivedTotalTokens == nil || *got.DerivedTotalTokens != 12 || !got.LowerBoundMask.Has(usage.CounterDerivedTotal) ||
		got.UsageQuality != usage.QualityLegacyPartial {
		t.Fatalf("missing later-attempt counters were presented as exact: %#v", got)
	}
	if got.TokenSemanticsProfile != usage.TokenSemanticsAnthropicV1 || got.UpstreamRequestCount != 2 {
		t.Fatalf("semantics/upstream provenance = %q/%d", got.TokenSemanticsProfile, got.UpstreamRequestCount)
	}
	totals, err := repository.SumByRun(context.Background(), "run-incomplete")
	if err != nil || totals.OutputTokens != 0 || totals.CallsWithOutputTokens != 0 || totals.DerivedTotalTokens != 0 || totals.CallsWithDerivedTotalTokens != 0 ||
		totals.LowerBoundOutputTokens != 2 || totals.LowerBoundDerivedTotalTokens != 12 {
		t.Fatalf("rollup promoted lower-bound counters: %#v err=%v", totals, err)
	}
}

type countingTrackingProvider struct{ calls atomic.Int64 }

func (p *countingTrackingProvider) ID() string    { return "openai" }
func (p *countingTrackingProvider) Model() string { return "gpt-test" }
func (p *countingTrackingProvider) StreamChat(context.Context, types.ChatRequest) (<-chan types.StreamEvent, error) {
	p.calls.Add(1)
	stream := make(chan types.StreamEvent)
	close(stream)
	return stream, nil
}

func TestTrackingProviderFailsClosedBeforeDispatchWhenJournalBeginFails(t *testing.T) {
	cfgDir := t.TempDir()
	primary, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	recorder, err := usage.NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	pending := filepath.Join(cfgDir, "chatdb", "usage-journal", "pending")
	if err := os.RemoveAll(pending); err != nil {
		t.Fatalf("remove pending: %v", err)
	}
	if err := os.WriteFile(pending, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("replace pending: %v", err)
	}
	inner := &countingTrackingProvider{}
	provider := newTrackingProvider(inner, recorder, "connection-a", "", "fingerprint-a", "responses", "responses")
	if _, err := provider.StreamChat(context.Background(), types.ChatRequest{Model: "gpt-test"}); !errors.Is(err, usage.ErrAccountingUnavailable) {
		t.Fatalf("StreamChat err=%v", err)
	}
	if inner.calls.Load() != 0 {
		t.Fatalf("provider dispatched %d times", inner.calls.Load())
	}
}

func TestTrackingProviderFailsClosedWhenJournalBreaksAtDispatchBoundary(t *testing.T) {
	cfgDir := t.TempDir()
	primary, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	recorder, err := usage.NewRecorder(cfgDir, primary)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	pending := filepath.Join(cfgDir, "chatdb", "usage-journal", "pending")
	inner := &trackingDispatchGateProvider{beforeDispatch: func() {
		if err := os.RemoveAll(pending); err != nil {
			t.Errorf("remove pending: %v", err)
			return
		}
		if err := os.WriteFile(pending, []byte("not a directory"), 0o600); err != nil {
			t.Errorf("replace pending: %v", err)
		}
	}}
	provider := newTrackingProvider(inner, recorder, "connection-a", "", "fingerprint-a", "responses", "responses")
	if _, err := provider.StreamChat(context.Background(), types.ChatRequest{Model: "gpt-test"}); !errors.Is(err, usage.ErrAccountingUnavailable) {
		t.Fatalf("StreamChat err=%v", err)
	}
	if inner.dispatched.Load() != 0 {
		t.Fatalf("provider crossed upstream boundary %d times", inner.dispatched.Load())
	}
}

func TestTrackingProviderRecordsUnexpectedStreamClose(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	provider := newTrackingProvider(trackingScriptedProvider{events: []types.StreamEvent{{
		Event: types.EventTextDelta,
		Data:  types.TextDeltaData{Content: "partial"},
	}}}, repository, "", "", "", "responses", "responses")
	stream, err := provider.StreamChat(context.Background(), types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	for range stream {
	}
	records, err := repository.List(context.Background(), usage.Query{})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	if records[0].Status != usage.StatusInterrupted || records[0].UsageQuality != usage.QualityMissing {
		t.Fatalf("status/quality=%s/%s", records[0].Status, records[0].UsageQuality)
	}
}

func TestTrackingProviderClassifiesWatchdogTimeoutAsFailed(t *testing.T) {
	repository, err := usage.NewJSONRepository(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONRepository: %v", err)
	}
	provider := newTrackingProvider(trackingContextProvider{}, repository, "", "", "", "responses", "responses")
	ctx, cancel := context.WithCancelCause(context.Background())
	stream, err := provider.StreamChat(ctx, types.ChatRequest{Model: "requested-model"})
	if err != nil {
		t.Fatalf("StreamChat: %v", err)
	}
	cancel(usage.ErrInvocationWatchdogTimeout)
	for range stream {
	}
	records, err := repository.List(context.Background(), usage.Query{})
	if err != nil || len(records) != 1 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	if records[0].Status != usage.StatusFailed || records[0].ErrorCode == nil || *records[0].ErrorCode != "STREAM_TIMEOUT" {
		t.Fatalf("watchdog lifecycle=%#v", records[0])
	}
}
