package usage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

const (
	MaxQueryLimit      = 50_001
	DefaultQueryLimit  = 10_000
	MaxQueryRange      = 367 * 24 * time.Hour
	StaleInvocationAge = 10 * time.Minute
)

var (
	ErrNotFound = errors.New("usage invocation not found")
	ErrConflict = errors.New("usage invocation journal conflict")
	ErrLimit    = errors.New("usage query exceeds safety limit")
)

type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthUnavailable HealthStatus = "unavailable"
)

type ReportHealth struct {
	Status                 HealthStatus `json:"status"`
	Message                *string      `json:"message"`
	DroppedInvocationCount int64        `json:"dropped_invocation_count"`
	RunningInvocationCount int64        `json:"running_invocation_count"`
	StaleInvocationCount   int64        `json:"stale_invocation_count"`
	PendingInvocationCount int64        `json:"pending_invocation_count"`
	TerminalPendingCount   int64        `json:"terminal_pending_count"`
	QuarantinedRecordCount int64        `json:"quarantined_record_count"`
	OldestPendingAt        *time.Time   `json:"oldest_pending_at"`
}

type RetentionPolicy struct {
	Mode             string `json:"mode"`
	DeleteWithThread bool   `json:"delete_with_thread"`
}

var IndefiniteRetention = RetentionPolicy{Mode: "indefinite", DeleteWithThread: false}

type Repository interface {
	Start(ctx context.Context, invocation Invocation) (Invocation, error)
	Snapshot(ctx context.Context, id string, snapshot UsageSnapshot) (Invocation, error)
	Finalize(ctx context.Context, id string, final Finalization) (Invocation, error)
	Get(ctx context.Context, id string) (Invocation, error)
	List(ctx context.Context, query Query) ([]Invocation, error)
	SumByRun(ctx context.Context, runID string) (TokenTotals, error)
	EarliestTrackingDate(ctx context.Context) (*time.Time, error)
	Health(ctx context.Context, now time.Time) (ReportHealth, error)
}

func prepareStart(ctx context.Context, inv Invocation, now time.Time) (Invocation, error) {
	if attr, ok := AttributionFromContext(ctx); ok {
		applyAttribution(&inv, attr)
	}
	if err := normalizeInvocationAliases(&inv); err != nil {
		return Invocation{}, err
	}
	inv.ID = strings.TrimSpace(inv.ID)
	if inv.ID == "" {
		inv.ID = newUUID()
	}
	if err := validateID(inv.ID); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	inv.RunID = trimOptional(inv.RunID)
	inv.ThreadID = trimOptional(inv.ThreadID)
	inv.JobID = trimOptional(inv.JobID)
	inv.PlanID = trimOptional(inv.PlanID)
	inv.ConnectionID = trimOptional(inv.ConnectionID)
	inv.ProfileID = trimOptional(inv.ProfileID)
	inv.ConfigFingerprint = trimOptional(inv.ConfigFingerprint)
	inv.ResolvedModel = trimOptional(inv.ResolvedModel)
	inv.ProviderRequestID = nil
	inv.ErrorCode = nil
	inv.ErrorMessage = nil
	inv.Operation = Operation(strings.TrimSpace(string(inv.Operation)))
	if inv.Operation == "" {
		inv.Operation = OperationUnknown
	}
	inv.ProviderID = strings.ToLower(strings.TrimSpace(inv.ProviderID))
	inv.RequestedModel = strings.TrimSpace(inv.RequestedModel)
	if inv.ProviderID == "" {
		return Invocation{}, errors.New("provider_id is required")
	}
	if inv.RequestedModel == "" {
		return Invocation{}, errors.New("requested_model is required")
	}
	planned, err := canonicalSurfaceOptional(inv.ProviderID, inv.PlannedAPISurface)
	if err != nil {
		return Invocation{}, fmt.Errorf("planned_api_surface: %w", err)
	}
	inv.PlannedAPISurface = planned
	inv.EffectiveAPISurface = nil
	if err := validateInvocationStrings(inv); err != nil {
		return Invocation{}, err
	}
	if inv.StartedAt.IsZero() {
		inv.StartedAt = now
	}
	inv.StartedAt = canonicalUsageTime(inv.StartedAt)
	inv.UpdatedAt = inv.StartedAt
	inv.EndedAt = nil
	inv.Status = StatusRunning
	inv.SnapshotSequence = 0
	inv.InputTokens = nil
	inv.OutputTokens = nil
	inv.ProviderTotalTokens = nil
	inv.DerivedTotalTokens = nil
	inv.TotalTokens = nil
	inv.CachedInputTokens = nil
	inv.CacheWriteTokens = nil
	inv.ReasoningTokens = nil
	inv.LowerBoundMask = 0
	inv.UsageQuality = QualityMissing
	inv.TokenSemanticsProfile = TokenSemanticsRawCountersV1
	inv.UpstreamRequestCount = 0
	inv.SanitizedDetails = copyDetails(inv.SanitizedDetails)
	return cloneInvocation(inv), nil
}

func applySnapshot(inv Invocation, snapshot UsageSnapshot, now time.Time) (Invocation, error) {
	if inv.Status.Terminal() {
		return Invocation{}, ErrConflict
	}
	if snapshot.Sequence <= 0 {
		return Invocation{}, errors.New("snapshot sequence must be positive")
	}
	if snapshot.Sequence < inv.SnapshotSequence {
		return cloneInvocation(inv), nil
	}
	if snapshot.Sequence == inv.SnapshotSequence {
		if snapshotMatches(inv, snapshot) {
			return cloneInvocation(inv), nil
		}
		return Invocation{}, ErrConflict
	}
	updated, err := applyCumulative(inv, snapshot.ResolvedModel, firstStringPointer(snapshot.EffectiveAPISurface, snapshot.APIType),
		snapshot.InputTokens, snapshot.OutputTokens, snapshot.ProviderTotalTokens, snapshot.DerivedTotalTokens, snapshot.TotalTokens,
		snapshot.CachedInputTokens, snapshot.CacheWriteTokens, snapshot.ReasoningTokens, snapshot.LowerBoundMask,
		snapshot.UsageQuality, snapshot.TokenSemanticsProfile, snapshot.UpstreamRequestCount,
		snapshot.ProviderRequestID, snapshot.SanitizedDetails)
	if err != nil {
		return Invocation{}, err
	}
	observedAt := snapshot.ObservedAt
	if observedAt.IsZero() {
		observedAt = now
	}
	observedAt = canonicalUsageTime(observedAt)
	if observedAt.Before(inv.StartedAt) || observedAt.Before(inv.UpdatedAt) {
		return Invocation{}, errors.New("snapshot observed_at cannot precede durable invocation time")
	}
	updated.UpdatedAt = observedAt
	updated.SnapshotSequence = snapshot.Sequence
	return cloneInvocation(updated), nil
}

func applyFinalization(inv Invocation, final Finalization, now time.Time) (Invocation, error) {
	if inv.Status.Terminal() {
		if finalizationMatches(inv, final) {
			return cloneInvocation(inv), nil
		}
		return Invocation{}, ErrConflict
	}
	if !final.Status.Terminal() {
		return Invocation{}, fmt.Errorf("terminal status is required, got %q", final.Status)
	}
	sequence := final.Sequence
	if sequence == 0 {
		sequence = inv.SnapshotSequence + 1
	}
	if sequence <= inv.SnapshotSequence {
		return Invocation{}, ErrConflict
	}
	updated, err := applyCumulative(inv, final.ResolvedModel, firstStringPointer(final.EffectiveAPISurface, final.APIType),
		final.InputTokens, final.OutputTokens, final.ProviderTotalTokens, final.DerivedTotalTokens, final.TotalTokens,
		final.CachedInputTokens, final.CacheWriteTokens, final.ReasoningTokens, final.LowerBoundMask,
		final.UsageQuality, final.TokenSemanticsProfile, final.UpstreamRequestCount,
		final.ProviderRequestID, final.SanitizedDetails)
	if err != nil {
		return Invocation{}, err
	}
	endedAt := final.EndedAt
	if endedAt.IsZero() {
		endedAt = now
	}
	endedAt = canonicalUsageTime(endedAt)
	if endedAt.Before(inv.StartedAt) || endedAt.Before(inv.UpdatedAt) {
		return Invocation{}, errors.New("ended_at cannot precede durable invocation time")
	}
	updated.Status = final.Status
	updated.EndedAt = &endedAt
	updated.UpdatedAt = endedAt
	updated.SnapshotSequence = sequence
	updated.ErrorCode = categorizedError(final.Status, final.ErrorCode)
	updated.ErrorMessage = nil
	return cloneInvocation(updated), nil
}

func applyCumulative(inv Invocation, resolvedModel, effectiveSurface *string,
	input, output, providerTotal, derivedTotal, legacyTotal, cachedInput, cacheWrite, reasoning *int64,
	lowerBoundMask CounterMask, quality Quality, semantics TokenSemanticsProfile, upstreamCount int64,
	providerRequestID *string, details map[string]int64) (Invocation, error) {
	if legacyTotal != nil {
		if providerTotal != nil || derivedTotal != nil {
			return Invocation{}, errors.New("total_tokens alias cannot be combined with canonical total counters")
		}
		if quality == QualityDerived {
			derivedTotal = legacyTotal
		} else {
			providerTotal = legacyTotal
		}
	}
	values := []struct {
		name  string
		value *int64
	}{
		{"input_tokens", input}, {"output_tokens", output}, {"provider_total_tokens", providerTotal},
		{"derived_total_tokens", derivedTotal}, {"cached_input_tokens", cachedInput},
		{"cache_write_tokens", cacheWrite}, {"reasoning_tokens", reasoning},
	}
	for _, item := range values {
		if item.value != nil && *item.value < 0 {
			return Invocation{}, fmt.Errorf("%s cannot be negative", item.name)
		}
	}
	if lowerBoundMask&^counterMaskAll != 0 {
		return Invocation{}, errors.New("lower_bound_mask contains unknown counters")
	}
	payloadMask := counterPayloadMask(input, output, providerTotal, derivedTotal, cachedInput, cacheWrite, reasoning)
	if lowerBoundMask&^payloadMask != 0 {
		return Invocation{}, errors.New("lower_bound_mask marks a counter absent from the journal event")
	}
	if upstreamCount < 0 {
		return Invocation{}, errors.New("upstream_request_count cannot be negative")
	}
	if upstreamCount == 0 && inv.UpstreamRequestCount > 0 {
		upstreamCount = inv.UpstreamRequestCount
	}
	if upstreamCount < inv.UpstreamRequestCount {
		return Invocation{}, errors.New("upstream_request_count cannot decrease")
	}
	if quality != "" && !validQuality(quality) {
		return Invocation{}, fmt.Errorf("invalid usage quality %q", quality)
	}
	if semantics != "" && !validTokenSemanticsProfile(semantics) {
		return Invocation{}, fmt.Errorf("unsupported token semantics profile %q", semantics)
	}
	updated := cloneInvocation(inv)
	if value := trimOptional(resolvedModel); value != nil {
		updated.ResolvedModel = value
	}
	if effectiveSurface != nil {
		surface, err := canonicalSurfaceOptional(inv.ProviderID, effectiveSurface)
		if err != nil {
			return Invocation{}, fmt.Errorf("effective_api_surface: %w", err)
		}
		updated.EffectiveAPISurface = surface
	}
	mergeCounter := func(name string, dst **int64, value *int64, bit CounterMask) error {
		if value == nil {
			return nil
		}
		if *dst != nil && *value < **dst {
			return fmt.Errorf("%s cannot decrease in a cumulative journal event", name)
		}
		*dst = copyInt64(value)
		updated.LowerBoundMask &^= bit
		updated.LowerBoundMask |= lowerBoundMask & bit
		return nil
	}
	for _, merge := range []struct {
		name  string
		dst   **int64
		value *int64
		bit   CounterMask
	}{
		{"input_tokens", &updated.InputTokens, input, CounterInput},
		{"output_tokens", &updated.OutputTokens, output, CounterOutput},
		{"provider_total_tokens", &updated.ProviderTotalTokens, providerTotal, CounterProviderTotal},
		{"derived_total_tokens", &updated.DerivedTotalTokens, derivedTotal, CounterDerivedTotal},
		{"cached_input_tokens", &updated.CachedInputTokens, cachedInput, CounterCachedInput},
		{"cache_write_tokens", &updated.CacheWriteTokens, cacheWrite, CounterCacheWrite},
		{"reasoning_tokens", &updated.ReasoningTokens, reasoning, CounterReasoning},
	} {
		if err := mergeCounter(merge.name, merge.dst, merge.value, merge.bit); err != nil {
			return Invocation{}, err
		}
	}
	if quality != "" {
		updated.UsageQuality = quality
	} else {
		updated.UsageQuality = inferQuality(updated)
	}
	if updated.UsageQuality == QualityLegacyPartial && updated.LowerBoundMask == 0 {
		updated.LowerBoundMask = presentCounterMask(updated)
	}
	if semantics != "" {
		updated.TokenSemanticsProfile = semantics
	}
	if updated.TokenSemanticsProfile == "" {
		updated.TokenSemanticsProfile = TokenSemanticsRawCountersV1
	}
	updated.UpstreamRequestCount = upstreamCount
	if value := trimOptional(providerRequestID); value != nil {
		updated.ProviderRequestID = value
	}
	if details != nil {
		updated.SanitizedDetails = copyDetails(details)
	}
	if err := validateInvocationStrings(updated); err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(updated), nil
}

func inferQuality(inv Invocation) Quality {
	if inv.ProviderTotalTokens != nil {
		return QualityProviderReported
	}
	if inv.DerivedTotalTokens != nil || inv.InputTokens != nil || inv.OutputTokens != nil ||
		inv.CachedInputTokens != nil || inv.CacheWriteTokens != nil || inv.ReasoningTokens != nil {
		return QualityDerived
	}
	return QualityMissing
}

func presentCounterMask(inv Invocation) CounterMask {
	var mask CounterMask
	for _, item := range []struct {
		value *int64
		bit   CounterMask
	}{
		{inv.InputTokens, CounterInput}, {inv.OutputTokens, CounterOutput},
		{inv.ProviderTotalTokens, CounterProviderTotal}, {inv.DerivedTotalTokens, CounterDerivedTotal},
		{inv.CachedInputTokens, CounterCachedInput}, {inv.CacheWriteTokens, CounterCacheWrite},
		{inv.ReasoningTokens, CounterReasoning},
	} {
		if item.value != nil {
			mask |= item.bit
		}
	}
	return mask
}

func snapshotMatches(inv Invocation, snapshot UsageSnapshot) bool {
	if snapshot.Sequence != inv.SnapshotSequence {
		return false
	}
	if !snapshot.ObservedAt.IsZero() && !canonicalUsageTime(snapshot.ObservedAt).Equal(inv.UpdatedAt) {
		return false
	}
	if !journalMaskMatches(inv, snapshot.LowerBoundMask, snapshot.UsageQuality,
		snapshot.InputTokens, snapshot.OutputTokens, snapshot.ProviderTotalTokens, snapshot.DerivedTotalTokens,
		snapshot.TotalTokens, snapshot.CachedInputTokens, snapshot.CacheWriteTokens, snapshot.ReasoningTokens) ||
		(snapshot.UsageQuality != "" && snapshot.UsageQuality != inv.UsageQuality) ||
		(snapshot.TokenSemanticsProfile != "" && snapshot.TokenSemanticsProfile != inv.TokenSemanticsProfile) ||
		(snapshot.SanitizedDetails != nil && !reflect.DeepEqual(copyDetails(snapshot.SanitizedDetails), inv.SanitizedDetails)) {
		return false
	}
	return optionalFieldsMatch(inv, snapshot.ResolvedModel, firstStringPointer(snapshot.EffectiveAPISurface, snapshot.APIType),
		snapshot.InputTokens, snapshot.OutputTokens, snapshot.ProviderTotalTokens, snapshot.DerivedTotalTokens,
		snapshot.TotalTokens, snapshot.CachedInputTokens, snapshot.CacheWriteTokens, snapshot.ReasoningTokens,
		snapshot.UpstreamRequestCount, snapshot.ProviderRequestID)
}

func finalizationMatches(inv Invocation, final Finalization) bool {
	if final.Status != inv.Status || (final.Sequence > 0 && final.Sequence != inv.SnapshotSequence) {
		return false
	}
	if !final.EndedAt.IsZero() && (inv.EndedAt == nil || !canonicalUsageTime(final.EndedAt).Equal(*inv.EndedAt)) {
		return false
	}
	if categorizedError(final.Status, final.ErrorCode) != nil && !stringPointersEqual(categorizedError(final.Status, final.ErrorCode), inv.ErrorCode) {
		return false
	}
	if !journalMaskMatches(inv, final.LowerBoundMask, final.UsageQuality,
		final.InputTokens, final.OutputTokens, final.ProviderTotalTokens, final.DerivedTotalTokens,
		final.TotalTokens, final.CachedInputTokens, final.CacheWriteTokens, final.ReasoningTokens) ||
		(final.UsageQuality != "" && final.UsageQuality != inv.UsageQuality) ||
		(final.TokenSemanticsProfile != "" && final.TokenSemanticsProfile != inv.TokenSemanticsProfile) ||
		(final.SanitizedDetails != nil && !reflect.DeepEqual(copyDetails(final.SanitizedDetails), inv.SanitizedDetails)) {
		return false
	}
	return optionalFieldsMatch(inv, final.ResolvedModel, firstStringPointer(final.EffectiveAPISurface, final.APIType),
		final.InputTokens, final.OutputTokens, final.ProviderTotalTokens, final.DerivedTotalTokens,
		final.TotalTokens, final.CachedInputTokens, final.CacheWriteTokens, final.ReasoningTokens,
		final.UpstreamRequestCount, final.ProviderRequestID)
}

func counterPayloadMask(input, output, providerTotal, derivedTotal, cached, cacheWrite, reasoning *int64) CounterMask {
	var mask CounterMask
	for _, item := range []struct {
		value *int64
		bit   CounterMask
	}{
		{input, CounterInput}, {output, CounterOutput}, {providerTotal, CounterProviderTotal},
		{derivedTotal, CounterDerivedTotal}, {cached, CounterCachedInput}, {cacheWrite, CounterCacheWrite},
		{reasoning, CounterReasoning},
	} {
		if item.value != nil {
			mask |= item.bit
		}
	}
	return mask
}

func journalMaskMatches(inv Invocation, supplied CounterMask, quality Quality,
	input, output, providerTotal, derivedTotal, legacyTotal, cached, cacheWrite, reasoning *int64) bool {
	if legacyTotal != nil {
		if quality == QualityDerived {
			derivedTotal = legacyTotal
		} else {
			providerTotal = legacyTotal
		}
	}
	present := counterPayloadMask(input, output, providerTotal, derivedTotal, cached, cacheWrite, reasoning)
	if quality == QualityLegacyPartial && supplied == 0 {
		supplied = present
	}
	return inv.LowerBoundMask&present == supplied&present
}

func optionalFieldsMatch(inv Invocation, resolved, surface *string,
	input, output, providerTotal, derivedTotal, legacyTotal, cached, cacheWrite, reasoning *int64,
	upstream int64, requestID *string) bool {
	if resolved != nil && !stringPointersEqual(trimOptional(resolved), inv.ResolvedModel) {
		return false
	}
	if surface != nil {
		canonical, err := canonicalSurfaceOptional(inv.ProviderID, surface)
		if err != nil || !stringPointersEqual(canonical, inv.EffectiveAPISurface) {
			return false
		}
	}
	checks := []struct{ supplied, stored *int64 }{
		{input, inv.InputTokens}, {output, inv.OutputTokens}, {providerTotal, inv.ProviderTotalTokens},
		{derivedTotal, inv.DerivedTotalTokens}, {cached, inv.CachedInputTokens},
		{cacheWrite, inv.CacheWriteTokens}, {reasoning, inv.ReasoningTokens},
	}
	for _, check := range checks {
		if check.supplied != nil && !intPointersEqual(check.supplied, check.stored) {
			return false
		}
	}
	if legacyTotal != nil && !intPointersEqual(legacyTotal, inv.TotalTokens) {
		return false
	}
	if upstream != 0 && upstream != inv.UpstreamRequestCount {
		return false
	}
	if requestID != nil && !stringPointersEqual(trimOptional(requestID), inv.ProviderRequestID) {
		return false
	}
	return true
}

func normalizeInvocationAliases(inv *Invocation) error {
	if inv == nil {
		return nil
	}
	if err := mergeStringAlias(&inv.ConnectionID, inv.ProviderConfigID, "connection_id", "provider_config_id"); err != nil {
		return err
	}
	if err := mergeStringAlias(&inv.PlannedAPISurface, inv.APIType, "planned_api_surface", "api_type"); err != nil {
		return err
	}
	if inv.TotalTokens != nil {
		if inv.ProviderTotalTokens != nil || inv.DerivedTotalTokens != nil {
			return errors.New("total_tokens alias cannot be combined with canonical total counters")
		}
		if inv.UsageQuality == QualityDerived {
			inv.DerivedTotalTokens = copyInt64(inv.TotalTokens)
		} else {
			inv.ProviderTotalTokens = copyInt64(inv.TotalTokens)
		}
	}
	inv.ProviderConfigID = nil
	inv.APIType = nil
	inv.TotalTokens = nil
	inv.ErrorMessage = nil
	return nil
}

func mergeStringAlias(canonical **string, alias *string, canonicalName, aliasName string) error {
	a := trimOptional(alias)
	c := trimOptional(*canonical)
	if a != nil && c != nil && *a != *c {
		return fmt.Errorf("%s and %s disagree", canonicalName, aliasName)
	}
	if c == nil {
		c = a
	}
	*canonical = c
	return nil
}

func startEquivalent(a, b Invocation) bool {
	a, b = cloneInvocation(a), cloneInvocation(b)
	return a.ID == b.ID && a.Operation == b.Operation && a.ProviderID == b.ProviderID && a.RequestedModel == b.RequestedModel &&
		stringPointersEqual(a.RunID, b.RunID) && stringPointersEqual(a.ThreadID, b.ThreadID) && stringPointersEqual(a.JobID, b.JobID) &&
		stringPointersEqual(a.PlanID, b.PlanID) && stringPointersEqual(a.ConnectionID, b.ConnectionID) && stringPointersEqual(a.ProfileID, b.ProfileID) &&
		stringPointersEqual(a.ConfigFingerprint, b.ConfigFingerprint) && stringPointersEqual(a.PlannedAPISurface, b.PlannedAPISurface)
}

func validateQuery(query Query) error {
	if !query.From.IsZero() && !query.To.IsZero() && !query.From.Before(query.To) {
		return errors.New("usage query from must precede to")
	}
	if !query.From.IsZero() && !query.To.IsZero() && query.To.Sub(query.From) > MaxQueryRange {
		return ErrLimit
	}
	if query.Limit < 0 || query.Limit > MaxQueryLimit {
		return ErrLimit
	}
	if query.RangeField != "" && query.RangeField != RangeByStartedAt && query.RangeField != RangeByEndedAt {
		return fmt.Errorf("invalid usage range field %q", query.RangeField)
	}
	if query.ConnectionID != "" && query.ProviderConfigID != "" && strings.TrimSpace(query.ConnectionID) != strings.TrimSpace(query.ProviderConfigID) {
		return errors.New("connection_id and provider_config_id filters disagree")
	}
	return nil
}

func effectiveQueryLimit(query Query) int {
	if query.Limit > 0 {
		return query.Limit
	}
	return DefaultQueryLimit
}

func queryConnectionID(query Query) string {
	return firstNonEmpty(query.ConnectionID, query.ProviderConfigID)
}
func queryRangeField(query Query) RangeField {
	if query.RangeField == "" {
		return RangeByStartedAt
	}
	return query.RangeField
}

func validQuality(quality Quality) bool {
	switch quality {
	case QualityProviderReported, QualityDerived, QualityMissing, QualityLegacyPartial:
		return true
	default:
		return false
	}
}

// CanonicalAPISurface normalizes known provider aliases to the finite reporting
// vocabulary. In particular, Bedrock's Anthropic invoke stream is not reported
// as Anthropic Messages.
func CanonicalAPISurface(providerID, raw string) (APISurface, error) {
	providerID = strings.ToLower(strings.TrimSpace(providerID))
	value := strings.ToLower(strings.TrimSpace(raw))
	value = strings.NewReplacer("-", "_", " ", "_").Replace(value)
	if providerID == "bedrock" || providerID == "aws_bedrock" {
		switch value {
		case "messages", "bedrock_native", "native", "invoke_model", "invoke_stream", "invoke_model_with_response_stream":
			return APISurfaceInvokeStream, nil
		case "converse", "converse_stream":
			return APISurfaceConverseStream, nil
		}
	}
	switch value {
	case "responses", "response":
		return APISurfaceResponses, nil
	case "chat_completions", "chat_completion":
		return APISurfaceChatCompletions, nil
	case "messages", "anthropic_messages":
		return APISurfaceMessages, nil
	case "generate_content", "stream_generate_content":
		return APISurfaceGenerateContent, nil
	case "invoke_stream", "invoke_model_with_response_stream":
		return APISurfaceInvokeStream, nil
	case "converse_stream":
		return APISurfaceConverseStream, nil
	case "images", "images_api", "image_api", "image_generations":
		return APISurfaceImages, nil
	default:
		return "", fmt.Errorf("unsupported API surface %q", raw)
	}
}

func canonicalSurfaceOptional(providerID string, value *string) (*string, error) {
	value = trimOptional(value)
	if value == nil {
		return nil, nil
	}
	canonical, err := CanonicalAPISurface(providerID, *value)
	if err != nil {
		return nil, err
	}
	result := string(canonical)
	return &result, nil
}

func categorizedError(status Status, raw *string) *string {
	if status == StatusRunning || status == StatusCompleted {
		return nil
	}
	value := strings.ToUpper(strings.TrimSpace(stringValue(raw)))
	allowed := map[string]bool{
		"PROVIDER_ERROR": true, "AUTH_ERROR": true, "RATE_LIMIT": true, "INVALID_REQUEST": true,
		"TIMEOUT": true, "STREAM_TIMEOUT": true, "ABORTED": true, "STREAM_ENDED": true,
		"PROCESS_RESTART": true, "PERSISTENCE_ERROR": true, "UNKNOWN": true,
	}
	if allowed[value] {
		return optionalString(value)
	}
	switch {
	case strings.Contains(value, "AUTH"), strings.Contains(value, "UNAUTHORIZED"), strings.Contains(value, "FORBIDDEN"), strings.Contains(value, "CREDENTIAL"):
		value = "AUTH_ERROR"
	case strings.Contains(value, "RATE"), strings.Contains(value, "QUOTA"), strings.Contains(value, "429"):
		value = "RATE_LIMIT"
	case strings.Contains(value, "WATCHDOG"), strings.Contains(value, "STREAM") && strings.Contains(value, "TIMEOUT"):
		value = "STREAM_TIMEOUT"
	case strings.Contains(value, "TIMEOUT"), strings.Contains(value, "DEADLINE"):
		value = "TIMEOUT"
	case strings.Contains(value, "CANCEL"), strings.Contains(value, "ABORT"):
		value = "ABORTED"
	case strings.Contains(value, "INVALID"), strings.Contains(value, "BAD_REQUEST"), strings.Contains(value, "400"):
		value = "INVALID_REQUEST"
	case strings.Contains(value, "EOF"), strings.Contains(value, "STREAM_ENDED"):
		value = "STREAM_ENDED"
	case strings.Contains(value, "PERSIST"):
		value = "PERSISTENCE_ERROR"
	case strings.Contains(value, "PROVIDER"), value != "":
		value = "PROVIDER_ERROR"
	case status == StatusCancelled:
		value = "ABORTED"
	case status == StatusInterrupted:
		value = "STREAM_ENDED"
	default:
		value = "UNKNOWN"
	}
	return &value
}

func validateID(id string) error {
	v := strings.TrimSpace(id)
	if v == "" {
		return errors.New("empty")
	}
	if strings.ContainsAny(v, `/\\`) || v == "." || v == ".." {
		return errors.New("invalid path component")
	}
	if len(v) > 96 {
		return errors.New("too long")
	}
	return nil
}

func validateInvocationStrings(inv Invocation) error {
	if inv.ConnectionID != nil && inv.ProfileID != nil {
		return errors.New("connection_id and profile_id are mutually exclusive")
	}
	values := []struct {
		name, value string
		limit       int
	}{
		{"run_id", stringValue(inv.RunID), 36}, {"thread_id", stringValue(inv.ThreadID), 36}, {"job_id", stringValue(inv.JobID), 96},
		{"plan_id", stringValue(inv.PlanID), 36}, {"operation", string(inv.Operation), 64}, {"provider_id", inv.ProviderID, 64},
		{"connection_id", stringValue(inv.ConnectionID), 200}, {"profile_id", stringValue(inv.ProfileID), 200},
		{"config_fingerprint", stringValue(inv.ConfigFingerprint), 128}, {"planned_api_surface", stringValue(inv.PlannedAPISurface), 64},
		{"effective_api_surface", stringValue(inv.EffectiveAPISurface), 64}, {"requested_model", inv.RequestedModel, 255},
		{"resolved_model", stringValue(inv.ResolvedModel), 255}, {"provider_request_id", stringValue(inv.ProviderRequestID), 255},
		{"error_code", stringValue(inv.ErrorCode), 32},
	}
	for _, item := range values {
		if len(item.value) > item.limit {
			return fmt.Errorf("%s exceeds %d bytes", item.name, item.limit)
		}
	}
	return nil
}

func validateStoredInvocation(inv Invocation) error {
	if err := normalizeInvocationAliases(&inv); err != nil {
		return err
	}
	if err := validateID(inv.ID); err != nil {
		return fmt.Errorf("invalid usage invocation id: %w", err)
	}
	if strings.TrimSpace(inv.ProviderID) == "" || strings.TrimSpace(inv.RequestedModel) == "" {
		return errors.New("invalid usage invocation provider identity")
	}
	if inv.StartedAt.IsZero() || inv.UpdatedAt.IsZero() {
		return errors.New("invalid usage invocation timestamps")
	}
	if inv.Operation == "" {
		return errors.New("invalid empty usage operation")
	}
	if inv.Status != StatusRunning && !inv.Status.Terminal() {
		return fmt.Errorf("invalid usage invocation status %q", inv.Status)
	}
	if inv.Status == StatusRunning && inv.EndedAt != nil {
		return errors.New("running usage invocation has ended_at")
	}
	if inv.Status.Terminal() && inv.EndedAt == nil {
		return errors.New("terminal usage invocation is missing ended_at")
	}
	if inv.EndedAt != nil && inv.EndedAt.Before(inv.StartedAt) {
		return errors.New("usage invocation ended_at precedes started_at")
	}
	if !validQuality(inv.UsageQuality) {
		return fmt.Errorf("invalid usage quality %q", inv.UsageQuality)
	}
	if !validTokenSemanticsProfile(inv.TokenSemanticsProfile) {
		return fmt.Errorf("unsupported token semantics profile %q", inv.TokenSemanticsProfile)
	}
	if inv.SnapshotSequence < 0 || inv.UpstreamRequestCount < 0 {
		return errors.New("invalid negative usage sequence/count")
	}
	if inv.LowerBoundMask&^counterMaskAll != 0 {
		return errors.New("invalid lower_bound_mask")
	}
	if err := validateInvocationStrings(inv); err != nil {
		return err
	}
	for _, value := range []*int64{inv.InputTokens, inv.OutputTokens, inv.ProviderTotalTokens, inv.DerivedTotalTokens, inv.CachedInputTokens, inv.CacheWriteTokens, inv.ReasoningTokens} {
		if value != nil && *value < 0 {
			return errors.New("invalid negative usage counter")
		}
	}
	return nil
}

func validTokenSemanticsProfile(profile TokenSemanticsProfile) bool {
	switch profile {
	case TokenSemanticsRawCountersV1, TokenSemanticsGenericV1, TokenSemanticsOpenAIV1, TokenSemanticsOpenAIImagesV1,
		TokenSemanticsAnthropicV1, TokenSemanticsBedrockConverseV1, TokenSemanticsGeminiV1:
		return true
	default:
		return false
	}
}

func normalizeLoadedInvocation(inv *Invocation) error {
	if inv == nil {
		return errors.New("nil usage invocation")
	}
	if err := normalizeInvocationAliases(inv); err != nil {
		return err
	}
	inv.ProviderID = strings.ToLower(strings.TrimSpace(inv.ProviderID))
	inv.RequestedModel = strings.TrimSpace(inv.RequestedModel)
	inv.RunID = trimOptional(inv.RunID)
	inv.ThreadID = trimOptional(inv.ThreadID)
	inv.JobID = trimOptional(inv.JobID)
	inv.PlanID = trimOptional(inv.PlanID)
	inv.ConnectionID = trimOptional(inv.ConnectionID)
	inv.ProfileID = trimOptional(inv.ProfileID)
	inv.ConfigFingerprint = trimOptional(inv.ConfigFingerprint)
	inv.ResolvedModel = trimOptional(inv.ResolvedModel)
	inv.ProviderRequestID = trimOptional(inv.ProviderRequestID)
	planned, err := canonicalSurfaceOptional(inv.ProviderID, inv.PlannedAPISurface)
	if err != nil {
		return fmt.Errorf("planned_api_surface: %w", err)
	}
	effective, err := canonicalSurfaceOptional(inv.ProviderID, inv.EffectiveAPISurface)
	if err != nil {
		return fmt.Errorf("effective_api_surface: %w", err)
	}
	inv.PlannedAPISurface, inv.EffectiveAPISurface = planned, effective
	if inv.UsageQuality == "" {
		inv.UsageQuality = inferQuality(*inv)
	}
	if inv.UsageQuality == QualityLegacyPartial && inv.LowerBoundMask == 0 {
		inv.LowerBoundMask = presentCounterMask(*inv)
	}
	if inv.TokenSemanticsProfile == "" {
		inv.TokenSemanticsProfile = TokenSemanticsRawCountersV1
	}
	inv.ErrorCode = categorizedError(inv.Status, inv.ErrorCode)
	inv.ErrorMessage = nil
	inv.StartedAt = canonicalUsageTime(inv.StartedAt)
	inv.UpdatedAt = canonicalUsageTime(inv.UpdatedAt)
	if inv.EndedAt != nil {
		ended := canonicalUsageTime(*inv.EndedAt)
		inv.EndedAt = &ended
	}
	inv.SanitizedDetails = copyDetails(inv.SanitizedDetails)
	if err := validateStoredInvocation(*inv); err != nil {
		return err
	}
	*inv = cloneInvocation(*inv)
	return nil
}

func canonicalUsageTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Millisecond)
}

func durableInvocation(inv Invocation) Invocation {
	inv = cloneInvocation(inv)
	inv.ProviderConfigID = nil
	inv.APIType = nil
	inv.TotalTokens = nil
	inv.ErrorMessage = nil
	return inv
}

func optionalString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}
func trimOptional(value *string) *string {
	if value == nil {
		return nil
	}
	return optionalString(*value)
}
func copyString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}
func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func firstStringPointer(values ...*string) *string {
	for _, value := range values {
		if trimmed := trimOptional(value); trimmed != nil {
			return trimmed
		}
	}
	return nil
}
func stringPointersEqual(a, b *string) bool {
	return stringValue(a) == stringValue(b) && (a == nil) == (b == nil)
}
func intPointersEqual(a, b *int64) bool { return reflect.DeepEqual(a, b) }

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", time.Now().UTC().UnixNano()&0xffffffffffff)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	buf := make([]byte, 36)
	hex.Encode(buf[0:8], b[0:4])
	buf[8] = '-'
	hex.Encode(buf[9:13], b[4:6])
	buf[13] = '-'
	hex.Encode(buf[14:18], b[6:8])
	buf[18] = '-'
	hex.Encode(buf[19:23], b[8:10])
	buf[23] = '-'
	hex.Encode(buf[24:36], b[10:16])
	return string(buf)
}
