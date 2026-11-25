package providers

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/providers/capabilities"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
)

const usagePersistenceTimeout = 3 * time.Second

// trackingProvider is the common accounting seam for every logical provider
// invocation. Adapter telemetry reports actual upstream dispatches and partial
// counters without entering serialized provider payloads.
type trackingProvider struct {
	inner               types.Provider
	repository          usage.Repository
	connectionID        string
	profileID           string
	configFingerprint   string
	plannedAPISurface   string
	effectiveAPISurface string
}

func newTrackingProvider(inner types.Provider, repository usage.Repository, connectionID, profileID, configFingerprint, plannedAPISurface, effectiveAPISurface string) types.Provider {
	if inner == nil || repository == nil {
		return inner
	}
	return &trackingProvider{
		inner: inner, repository: repository, connectionID: strings.TrimSpace(connectionID), profileID: strings.TrimSpace(profileID),
		configFingerprint: strings.TrimSpace(configFingerprint), plannedAPISurface: strings.TrimSpace(plannedAPISurface),
		effectiveAPISurface: strings.TrimSpace(effectiveAPISurface),
	}
}

func (p *trackingProvider) ID() string    { return p.inner.ID() }
func (p *trackingProvider) Model() string { return p.inner.Model() }

func (p *trackingProvider) Capabilities() capabilities.ModelCapabilities {
	if reporter, ok := p.inner.(capabilities.Reporter); ok {
		return reporter.Capabilities()
	}
	return capabilities.Normalize(capabilities.ModelCapabilities{ProviderID: p.ID(), Model: p.Model()})
}

func (p *trackingProvider) StreamChat(ctx context.Context, request types.ChatRequest) (<-chan types.StreamEvent, error) {
	requestedModel := strings.TrimSpace(request.Model)
	if requestedModel == "" {
		requestedModel = strings.TrimSpace(p.inner.Model())
	}
	identity := usage.ProviderIdentity{
		ProviderID: strings.TrimSpace(p.inner.ID()), ConnectionID: p.connectionID, ProfileID: p.profileID,
		ConfigFingerprint: p.configFingerprint, PlannedAPISurface: p.plannedAPISurface, RequestedModel: requestedModel,
	}
	if attribution, ok := usage.AttributionFromContext(ctx); ok {
		if identity.ConnectionID == "" && identity.ProfileID == "" {
			identity.ConnectionID = attribution.ConnectionID
			identity.ProfileID = attribution.ProfileID
		}
		identityMatchesAttribution := (identity.ConnectionID == "" || identity.ConnectionID == attribution.ConnectionID) &&
			(identity.ProfileID == "" || identity.ProfileID == attribution.ProfileID)
		if identity.ConfigFingerprint == "" && identityMatchesAttribution {
			identity.ConfigFingerprint = attribution.ConfigFingerprint
		}
		if identity.PlannedAPISurface == "" && identityMatchesAttribution {
			identity.PlannedAPISurface = attribution.PlannedAPISurface
		}
	}
	if identity.PlannedAPISurface == "" {
		identity.PlannedAPISurface = p.effectiveAPISurface
	}

	invocation := usage.NewInvocation(ctx, identity)
	persistCtx, cancel := context.WithTimeout(context.Background(), usagePersistenceTimeout)
	started, err := p.repository.Start(persistCtx, invocation)
	cancel()
	if err != nil {
		// The local journal must exist before the adapter is allowed to reach an
		// upstream dispatch boundary.
		return nil, usage.ErrAccountingUnavailable
	}

	state := &invocationTelemetryState{
		repository: p.repository, invocationID: started.ID,
		quality: usage.QualityMissing, semantics: usage.TokenSemanticsRawCountersV1,
	}
	previousObserver := request.UsageObserver
	request.UsageObserver = func(event types.UsageTelemetry) error {
		if previousObserver != nil {
			if err := previousObserver(event); err != nil {
				return err
			}
		}
		return state.observe(event)
	}

	innerStream, err := p.inner.StreamChat(ctx, request)
	if err != nil {
		p.finalize(started.ID, state.finalization(nil, terminalStatusForContext(ctx, usage.StatusFailed), categorizedProviderError("PROVIDER_ERROR")))
		return nil, err
	}

	out := make(chan types.StreamEvent, 64)
	go func() {
		defer close(out)
		var finishOnce sync.Once
		finish := func(final usage.Finalization) {
			finishOnce.Do(func() {
				if final.EndedAt.IsZero() {
					final.EndedAt = time.Now().UTC()
				}
				p.finalize(started.ID, final)
			})
		}

		for event := range innerStream {
			if meta, status, code, terminal := terminalEvent(event, ctx); terminal {
				finish(state.finalization(meta, status, code))
			}
			select {
			case out <- event:
			case <-ctx.Done():
				status := terminalStatusForContext(ctx, usage.StatusCancelled)
				finish(state.finalization(nil, status, terminalContextCode(ctx, status)))
				return
			}
		}

		status := terminalStatusForContext(ctx, usage.StatusInterrupted)
		code := "STREAM_ENDED"
		if ctx.Err() != nil {
			code = terminalContextCode(ctx, status)
		}
		finish(state.finalization(nil, status, code))
	}()
	return out, nil
}

func (p *trackingProvider) finalize(id string, final usage.Finalization) {
	ctx, cancel := context.WithTimeout(context.Background(), usagePersistenceTimeout)
	defer cancel()
	if _, err := p.repository.Finalize(ctx, id, final); err != nil && !errors.Is(err, usage.ErrNotFound) {
		log.Printf("usage: terminal accounting update failed id=%s code=ACCOUNTING_FINALIZE_FAILED", id)
	}
}

type invocationTelemetryState struct {
	mu                                                                             sync.Mutex
	repository                                                                     usage.Repository
	invocationID                                                                   string
	sequence                                                                       int64
	upstreamRequestCount                                                           int64
	effectiveAPISurface                                                            string
	resolvedModel                                                                  *string
	requestID                                                                      *string
	input, output, providerTotal, derivedTotal, cachedInput, cacheWrite, reasoning *int64
	quality                                                                        usage.Quality
	semantics                                                                      usage.TokenSemanticsProfile
	incompleteMask                                                                 usage.CounterMask
	details                                                                        map[string]int64
}

func (s *invocationTelemetryState) observe(event types.UsageTelemetry) error {
	s.mu.Lock()
	previousUpstreamCount := s.upstreamRequestCount
	previousSequence := s.sequence
	previousSurface := s.effectiveAPISurface
	if event.Kind == types.UsageTelemetryDispatch {
		increment := int64(event.UpstreamRequestCount)
		if increment <= 0 {
			increment = 1
		}
		s.upstreamRequestCount += increment
	} else {
		s.observeCounterLocked(&s.input, intPointer64(event.InputTokens), usage.CounterInput)
		s.observeCounterLocked(&s.output, intPointer64(event.OutputTokens), usage.CounterOutput)
		s.observeCounterLocked(&s.providerTotal, intPointer64(event.ProviderTotalTokens), usage.CounterProviderTotal)
		s.observeCounterLocked(&s.derivedTotal, intPointer64(event.DerivedTotalTokens), usage.CounterDerivedTotal)
		s.observeCounterLocked(&s.cachedInput, intPointer64(event.CachedInputTokens), usage.CounterCachedInput)
		s.observeCounterLocked(&s.cacheWrite, intPointer64(event.CacheWriteTokens), usage.CounterCacheWrite)
		s.observeCounterLocked(&s.reasoning, intPointer64(event.ReasoningTokens), usage.CounterReasoning)
		s.resolvedModel = nonEmptyPointer(event.ResolvedModel)
		s.requestID = nonEmptyPointer(event.RequestID)
		s.quality = usageQuality(event.UsageQuality, s.hasUsageLocked())
		if semantics := tokenSemanticsProfile(event.UsageSemantics); semantics != "" {
			s.semantics = semantics
		}
		s.details = sanitizedUsageDetails(event.SanitizedDetails)
	}
	if surface := strings.TrimSpace(event.EffectiveAPISurface); surface != "" {
		s.effectiveAPISurface = surface
	}
	s.sequence++
	snapshot := s.snapshotLocked()
	ctx, cancel := context.WithTimeout(context.Background(), usagePersistenceTimeout)
	defer cancel()
	if _, err := s.repository.Snapshot(ctx, s.invocationID, snapshot); err != nil {
		log.Printf("usage: progress accounting update failed id=%s code=ACCOUNTING_SNAPSHOT_FAILED", s.invocationID)
		if event.Kind == types.UsageTelemetryDispatch {
			s.upstreamRequestCount = previousUpstreamCount
			s.sequence = previousSequence
			s.effectiveAPISurface = previousSurface
		}
		s.mu.Unlock()
		return usage.ErrAccountingUnavailable
	}
	s.mu.Unlock()
	return nil
}

func (s *invocationTelemetryState) observeCounterLocked(destination **int64, observed *int64, bit usage.CounterMask) {
	if observed != nil {
		*destination = observed
		s.incompleteMask &^= bit
		return
	}
	// Adapter telemetry is cumulative across an invocation. If a later
	// successful request omits a counter seen on an earlier request, retain the
	// known amount only as a lower bound; it must never become an exact total.
	if *destination != nil {
		s.incompleteMask |= bit
	}
}

func (s *invocationTelemetryState) snapshotLocked() usage.UsageSnapshot {
	mask := s.presentMaskLocked()
	quality := s.quality
	if mask != 0 {
		quality = usage.QualityLegacyPartial
	}
	return usage.UsageSnapshot{
		Sequence: s.sequence, ObservedAt: time.Now().UTC(), ResolvedModel: copyStringPointer(s.resolvedModel),
		EffectiveAPISurface: stringPointer(s.effectiveAPISurface), InputTokens: copyInt64Pointer(s.input), OutputTokens: copyInt64Pointer(s.output),
		ProviderTotalTokens: copyInt64Pointer(s.providerTotal), DerivedTotalTokens: copyInt64Pointer(s.derivedTotal),
		CachedInputTokens: copyInt64Pointer(s.cachedInput), CacheWriteTokens: copyInt64Pointer(s.cacheWrite), ReasoningTokens: copyInt64Pointer(s.reasoning),
		LowerBoundMask: mask, UsageQuality: quality, TokenSemanticsProfile: s.semantics,
		UpstreamRequestCount: s.upstreamRequestCount, ProviderRequestID: copyStringPointer(s.requestID), SanitizedDetails: copyInt64Map(s.details),
	}
}

func (s *invocationTelemetryState) finalization(meta *types.ResponseMeta, status usage.Status, errorCode string) usage.Finalization {
	s.mu.Lock()
	defer s.mu.Unlock()
	if meta != nil {
		mergeMetaIntoTelemetryState(s, *meta)
	}
	s.sequence++
	mask := s.incompleteMask
	quality := s.quality
	if status != usage.StatusCompleted {
		mask |= s.presentMaskLocked()
	}
	if mask != 0 {
		quality = usage.QualityLegacyPartial
	}
	return usage.Finalization{
		Sequence: s.sequence, Status: status, EndedAt: time.Now().UTC(), ResolvedModel: copyStringPointer(s.resolvedModel),
		EffectiveAPISurface: stringPointer(s.effectiveAPISurface), InputTokens: copyInt64Pointer(s.input), OutputTokens: copyInt64Pointer(s.output),
		ProviderTotalTokens: copyInt64Pointer(s.providerTotal), DerivedTotalTokens: copyInt64Pointer(s.derivedTotal),
		CachedInputTokens: copyInt64Pointer(s.cachedInput), CacheWriteTokens: copyInt64Pointer(s.cacheWrite), ReasoningTokens: copyInt64Pointer(s.reasoning),
		LowerBoundMask: mask, UsageQuality: quality, TokenSemanticsProfile: s.semantics,
		UpstreamRequestCount: s.upstreamRequestCount, ProviderRequestID: copyStringPointer(s.requestID),
		SanitizedDetails: copyInt64Map(s.details), ErrorCode: stringPointer(categorizedProviderError(errorCode)),
	}
}

func mergeMetaIntoTelemetryState(state *invocationTelemetryState, meta types.ResponseMeta) {
	state.observeCounterLocked(&state.input, intPointer64(meta.InputTokens), usage.CounterInput)
	state.observeCounterLocked(&state.output, intPointer64(meta.OutputTokens), usage.CounterOutput)
	providerTotal := intPointer64(meta.ProviderTotalTokens)
	if value := intPointer64(meta.ProviderTotalTokens); value != nil {
		providerTotal = value
	} else if meta.DerivedTotalTokens == nil && meta.UsageQuality != types.UsageQualityDerived {
		if value := intPointer64(meta.TotalTokens); value != nil {
			providerTotal = value
		}
	}
	state.observeCounterLocked(&state.providerTotal, providerTotal, usage.CounterProviderTotal)
	derivedTotal := intPointer64(meta.DerivedTotalTokens)
	if value := intPointer64(meta.DerivedTotalTokens); value != nil {
		derivedTotal = value
	} else if meta.ProviderTotalTokens == nil && meta.UsageQuality == types.UsageQualityDerived {
		if value := intPointer64(meta.TotalTokens); value != nil {
			derivedTotal = value
		}
	}
	state.observeCounterLocked(&state.derivedTotal, derivedTotal, usage.CounterDerivedTotal)
	state.observeCounterLocked(&state.cachedInput, intPointer64(meta.CachedInputTokens), usage.CounterCachedInput)
	state.observeCounterLocked(&state.cacheWrite, intPointer64(meta.CacheWriteTokens), usage.CounterCacheWrite)
	state.observeCounterLocked(&state.reasoning, intPointer64(meta.ReasoningTokens), usage.CounterReasoning)
	if value := nonEmptyPointer(meta.ResolvedModel); value != nil {
		state.resolvedModel = value
	}
	if value := nonEmptyPointer(meta.RequestID); value != nil {
		state.requestID = value
	}
	if meta.UpstreamRequestCount != nil && int64(*meta.UpstreamRequestCount) > state.upstreamRequestCount {
		state.upstreamRequestCount = int64(*meta.UpstreamRequestCount)
	}
	if state.upstreamRequestCount > 0 && meta.APIType != nil && strings.TrimSpace(*meta.APIType) != "" {
		state.effectiveAPISurface = strings.TrimSpace(*meta.APIType)
	}
	if semantics := tokenSemanticsProfile(meta.UsageSemantics); semantics != "" {
		state.semantics = semantics
	}
	if details := sanitizedUsageDetails(meta.UsageDetails); details != nil {
		state.details = details
	}
	state.quality = usageQuality(meta.UsageQuality, state.hasUsageLocked())
}

func tokenSemanticsProfile(value types.UsageSemantics) usage.TokenSemanticsProfile {
	switch value {
	case types.UsageSemanticsGeneric:
		return usage.TokenSemanticsGenericV1
	case types.UsageSemanticsOpenAI:
		return usage.TokenSemanticsOpenAIV1
	case types.UsageSemanticsAnthropic:
		return usage.TokenSemanticsAnthropicV1
	case types.UsageSemanticsBedrockConverse:
		return usage.TokenSemanticsBedrockConverseV1
	case types.UsageSemanticsGemini:
		return usage.TokenSemanticsGeminiV1
	default:
		return ""
	}
}

func (s *invocationTelemetryState) hasUsageLocked() bool {
	return s.input != nil || s.output != nil || s.providerTotal != nil || s.derivedTotal != nil || s.cachedInput != nil || s.cacheWrite != nil || s.reasoning != nil
}

func (s *invocationTelemetryState) presentMaskLocked() usage.CounterMask {
	var mask usage.CounterMask
	if s.input != nil {
		mask |= usage.CounterInput
	}
	if s.output != nil {
		mask |= usage.CounterOutput
	}
	if s.providerTotal != nil {
		mask |= usage.CounterProviderTotal
	}
	if s.derivedTotal != nil {
		mask |= usage.CounterDerivedTotal
	}
	if s.cachedInput != nil {
		mask |= usage.CounterCachedInput
	}
	if s.cacheWrite != nil {
		mask |= usage.CounterCacheWrite
	}
	if s.reasoning != nil {
		mask |= usage.CounterReasoning
	}
	return mask
}

func terminalEvent(event types.StreamEvent, ctx context.Context) (*types.ResponseMeta, usage.Status, string, bool) {
	switch data := event.Data.(type) {
	case types.FinalData:
		if event.Event == types.EventFinal {
			meta := data.Meta
			return &meta, usage.StatusCompleted, "", true
		}
	case types.ToolCallsData:
		if event.Event == types.EventToolCalls {
			meta := data.Meta
			return &meta, usage.StatusCompleted, "", true
		}
	case types.ErrorData:
		if event.Event == types.EventError {
			fallback := usage.StatusFailed
			if strings.EqualFold(strings.TrimSpace(data.Code), "STREAM_ENDED") {
				fallback = usage.StatusInterrupted
			}
			status := terminalStatusForContext(ctx, fallback)
			return data.Meta, status, categorizedProviderError(data.Code), true
		}
	}
	return nil, "", "", false
}

func terminalStatusForContext(ctx context.Context, fallback usage.Status) usage.Status {
	if ctx == nil || ctx.Err() == nil {
		return fallback
	}
	if errors.Is(context.Cause(ctx), usage.ErrInvocationWatchdogTimeout) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return usage.StatusFailed
	}
	return usage.StatusCancelled
}

func terminalContextCode(ctx context.Context, status usage.Status) string {
	if ctx != nil && errors.Is(context.Cause(ctx), usage.ErrInvocationWatchdogTimeout) {
		return "STREAM_TIMEOUT"
	}
	if status == usage.StatusFailed && ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	if ctx != nil && ctx.Err() != nil {
		return "ABORTED"
	}
	return "STREAM_ENDED"
}

func categorizedProviderError(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	for _, allowed := range []string{"ABORTED", "AUTH_ERROR", "BAD_REQUEST", "CONTEXT_LENGTH_EXCEEDED", "HTTP_ERROR", "PROVIDER_ERROR", "RATE_LIMITED", "REQUEST_TOO_LARGE", "STREAM_ENDED", "STREAM_TIMEOUT", "TIMEOUT"} {
		if value == allowed {
			return allowed
		}
	}
	if value == "" {
		return ""
	}
	return "PROVIDER_ERROR"
}

func usageQuality(value types.UsageQuality, present bool) usage.Quality {
	switch value {
	case types.UsageQualityProviderReported:
		return usage.QualityProviderReported
	case types.UsageQualityDerived:
		return usage.QualityDerived
	}
	if present {
		return usage.QualityDerived
	}
	return usage.QualityMissing
}

func sanitizedUsageDetails(values map[string]int) map[string]int64 {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int64, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key != "" && len(key) <= 64 && value >= 0 {
			out[key] = int64(value)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func intPointer64(value *int) *int64 {
	if value == nil || *value < 0 {
		return nil
	}
	converted := int64(*value)
	return &converted
}

func nonEmptyPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func stringPointer(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func copyStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyInt64Map(values map[string]int64) map[string]int64 {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]int64, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
