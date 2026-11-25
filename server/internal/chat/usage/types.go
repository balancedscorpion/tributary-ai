// Package usage records and aggregates model invocation usage.
//
// An Invocation is one logical provider call. A provider adapter may issue
// multiple upstream HTTP requests while satisfying that call; the distinction
// is retained by UpstreamRequestCount.
package usage

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

const ReportSchemaVersion = 2

type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled, StatusInterrupted:
		return true
	default:
		return false
	}
}

type Quality string

const (
	QualityProviderReported Quality = "provider_reported"
	QualityDerived          Quality = "derived"
	QualityMissing          Quality = "missing"
	QualityLegacyPartial    Quality = "legacy_partial"
)

type Operation string

const (
	OperationUnknown          Operation = "unknown"
	OperationChat             Operation = "chat"
	OperationToolFollowup     Operation = "tool_followup"
	OperationContextSummary   Operation = "context_summary"
	OperationThreadTitle      Operation = "thread_title"
	OperationThreadSummary    Operation = "thread_summary"
	OperationLocalSearchAgent Operation = "local_search_agent"
	OperationRLMSubLLM        Operation = "rlm_sub_llm"
	OperationProviderTest     Operation = "provider_test"
	OperationCapabilityProbe  Operation = "capability_probe"
	OperationImageGeneration  Operation = "image_generation"
)

// APISurface is a provider protocol, not a provider or SDK name.
type APISurface string

const (
	APISurfaceResponses       APISurface = "responses"
	APISurfaceChatCompletions APISurface = "chat_completions"
	APISurfaceMessages        APISurface = "messages"
	APISurfaceGenerateContent APISurface = "generate_content"
	APISurfaceInvokeStream    APISurface = "invoke_stream"
	APISurfaceConverseStream  APISurface = "converse_stream"
	APISurfaceImages          APISurface = "images"
)

// TokenSemanticsProfile describes how raw counters must be interpreted. Raw
// counters are never netted against cache or reasoning dimensions.
type TokenSemanticsProfile string

const (
	TokenSemanticsRawCountersV1     TokenSemanticsProfile = "raw_counters_v1"
	TokenSemanticsGenericV1         TokenSemanticsProfile = "generic_v1"
	TokenSemanticsOpenAIV1          TokenSemanticsProfile = "openai_v1"
	TokenSemanticsOpenAIImagesV1    TokenSemanticsProfile = "openai_images_v1"
	TokenSemanticsAnthropicV1       TokenSemanticsProfile = "anthropic_v1"
	TokenSemanticsBedrockConverseV1 TokenSemanticsProfile = "bedrock_converse_v1"
	TokenSemanticsGeminiV1          TokenSemanticsProfile = "gemini_v1"
)

// CounterMask identifies counters that are lower bounds rather than exact
// values. Lower-bound counters are retained but excluded from exact rollups.
type CounterMask uint64

const (
	CounterInput CounterMask = 1 << iota
	CounterOutput
	CounterProviderTotal
	CounterDerivedTotal
	CounterCachedInput
	CounterCacheWrite
	CounterReasoning
)

const counterMaskAll = CounterInput | CounterOutput | CounterProviderTotal | CounterDerivedTotal | CounterCachedInput | CounterCacheWrite | CounterReasoning

func (m CounterMask) Has(counter CounterMask) bool { return m&counter != 0 }

// Attribution is carried outside provider payloads. ConnectionID is the
// canonical identity. ProfileID is retained only for legacy compatible-provider
// profiles. ProviderConfigID and APIType are source-compatibility aliases.
type Attribution struct {
	RunID     string    `json:"run_id,omitempty"`
	ThreadID  string    `json:"thread_id,omitempty"`
	JobID     string    `json:"job_id,omitempty"`
	PlanID    string    `json:"plan_id,omitempty"`
	Operation Operation `json:"operation,omitempty"`

	ConnectionID      string `json:"connection_id,omitempty"`
	ProfileID         string `json:"profile_id,omitempty"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
	PlannedAPISurface string `json:"planned_api_surface,omitempty"`

	// Deprecated compatibility aliases. They are normalized into canonical
	// fields and are never serialized by the ledger.
	ProviderConfigID string `json:"-"`
	APIType          string `json:"-"`
}

// ProviderIdentity is the non-secret provider selection captured at dispatch.
type ProviderIdentity struct {
	ProviderID        string `json:"provider_id"`
	ConnectionID      string `json:"connection_id,omitempty"`
	ProfileID         string `json:"profile_id,omitempty"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
	PlannedAPISurface string `json:"planned_api_surface,omitempty"`
	RequestedModel    string `json:"requested_model"`
	ResolvedModel     string `json:"resolved_model,omitempty"`
	ProviderConfigID  string `json:"-"`
	APIType           string `json:"-"`
}

// Invocation is the durable ledger record. Pointer counters distinguish an
// absent counter from an explicit provider-reported zero.
type Invocation struct {
	ID string `json:"id"`

	RunID     *string   `json:"run_id,omitempty"`
	ThreadID  *string   `json:"thread_id,omitempty"`
	JobID     *string   `json:"job_id,omitempty"`
	PlanID    *string   `json:"plan_id,omitempty"`
	Operation Operation `json:"operation"`

	ProviderID          string  `json:"provider_id"`
	ConnectionID        *string `json:"connection_id,omitempty"`
	ProfileID           *string `json:"profile_id,omitempty"`
	ConfigFingerprint   *string `json:"config_fingerprint,omitempty"`
	PlannedAPISurface   *string `json:"planned_api_surface,omitempty"`
	EffectiveAPISurface *string `json:"effective_api_surface,omitempty"`
	RequestedModel      string  `json:"requested_model"`
	ResolvedModel       *string `json:"resolved_model,omitempty"`

	Status           Status     `json:"status"`
	StartedAt        time.Time  `json:"started_at"`
	EndedAt          *time.Time `json:"ended_at,omitempty"`
	UpdatedAt        time.Time  `json:"updated_at"`
	SnapshotSequence int64      `json:"snapshot_sequence"`

	InputTokens           *int64                `json:"input_tokens,omitempty"`
	OutputTokens          *int64                `json:"output_tokens,omitempty"`
	ProviderTotalTokens   *int64                `json:"provider_total_tokens,omitempty"`
	DerivedTotalTokens    *int64                `json:"derived_total_tokens,omitempty"`
	CachedInputTokens     *int64                `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens      *int64                `json:"cache_write_tokens,omitempty"`
	ReasoningTokens       *int64                `json:"reasoning_tokens,omitempty"`
	LowerBoundMask        CounterMask           `json:"lower_bound_mask"`
	UsageQuality          Quality               `json:"usage_quality"`
	TokenSemanticsProfile TokenSemanticsProfile `json:"token_semantics_profile"`

	UpstreamRequestCount int64            `json:"upstream_request_count"`
	ProviderRequestID    *string          `json:"provider_request_id,omitempty"`
	SanitizedDetails     map[string]int64 `json:"sanitized_details,omitempty"`
	ErrorCode            *string          `json:"error_code,omitempty"`

	// Deprecated compatibility aliases. cloneInvocation hydrates them for old
	// callers, but schema v2 never writes them.
	ProviderConfigID *string `json:"-"`
	APIType          *string `json:"-"`
	TotalTokens      *int64  `json:"-"`
	ErrorMessage     *string `json:"-"`
}

func (i Invocation) DisplayModel() string {
	if i.ResolvedModel != nil && *i.ResolvedModel != "" {
		return *i.ResolvedModel
	}
	return i.RequestedModel
}

// NewInvocation creates an unpersisted invocation and copies context
// attribution. Repositories normalize and validate the result at Start.
func NewInvocation(ctx context.Context, identity ProviderIdentity) Invocation {
	connectionID := firstNonEmpty(identity.ConnectionID, identity.ProviderConfigID)
	plannedSurface := firstNonEmpty(identity.PlannedAPISurface, identity.APIType)
	inv := Invocation{
		ProviderID:        identity.ProviderID,
		ConnectionID:      optionalString(connectionID),
		ProfileID:         optionalString(identity.ProfileID),
		ConfigFingerprint: optionalString(identity.ConfigFingerprint),
		PlannedAPISurface: optionalString(plannedSurface),
		RequestedModel:    identity.RequestedModel,
		ResolvedModel:     optionalString(identity.ResolvedModel),
	}
	if attr, ok := AttributionFromContext(ctx); ok {
		applyAttribution(&inv, attr)
	}
	return hydrateCompatibility(inv)
}

// StartRequest is the stable journal payload for a start event.
type StartRequest struct {
	Invocation Invocation `json:"invocation"`
}

// UsageSnapshot is a cumulative journal event. Sequence must increase for a
// running invocation; retrying an identical sequence and payload is safe.
type UsageSnapshot struct {
	Sequence   int64     `json:"sequence"`
	ObservedAt time.Time `json:"observed_at"`

	ResolvedModel         *string               `json:"resolved_model,omitempty"`
	EffectiveAPISurface   *string               `json:"effective_api_surface,omitempty"`
	InputTokens           *int64                `json:"input_tokens,omitempty"`
	OutputTokens          *int64                `json:"output_tokens,omitempty"`
	ProviderTotalTokens   *int64                `json:"provider_total_tokens,omitempty"`
	DerivedTotalTokens    *int64                `json:"derived_total_tokens,omitempty"`
	CachedInputTokens     *int64                `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens      *int64                `json:"cache_write_tokens,omitempty"`
	ReasoningTokens       *int64                `json:"reasoning_tokens,omitempty"`
	LowerBoundMask        CounterMask           `json:"lower_bound_mask"`
	UsageQuality          Quality               `json:"usage_quality,omitempty"`
	TokenSemanticsProfile TokenSemanticsProfile `json:"token_semantics_profile,omitempty"`
	UpstreamRequestCount  int64                 `json:"upstream_request_count"`
	ProviderRequestID     *string               `json:"provider_request_id,omitempty"`
	SanitizedDetails      map[string]int64      `json:"sanitized_details,omitempty"`

	APIType     *string `json:"-"`
	TotalTokens *int64  `json:"-"`
}

type SnapshotRequest struct {
	InvocationID string        `json:"invocation_id"`
	Snapshot     UsageSnapshot `json:"snapshot"`
}

// Finalization is the terminal cumulative journal event. ErrorMessage remains
// source-compatible but is deliberately discarded; only categorized ErrorCode
// may be persisted.
type Finalization struct {
	Sequence int64     `json:"sequence"`
	Status   Status    `json:"status"`
	EndedAt  time.Time `json:"ended_at"`

	ResolvedModel         *string               `json:"resolved_model,omitempty"`
	EffectiveAPISurface   *string               `json:"effective_api_surface,omitempty"`
	InputTokens           *int64                `json:"input_tokens,omitempty"`
	OutputTokens          *int64                `json:"output_tokens,omitempty"`
	ProviderTotalTokens   *int64                `json:"provider_total_tokens,omitempty"`
	DerivedTotalTokens    *int64                `json:"derived_total_tokens,omitempty"`
	CachedInputTokens     *int64                `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens      *int64                `json:"cache_write_tokens,omitempty"`
	ReasoningTokens       *int64                `json:"reasoning_tokens,omitempty"`
	LowerBoundMask        CounterMask           `json:"lower_bound_mask"`
	UsageQuality          Quality               `json:"usage_quality"`
	TokenSemanticsProfile TokenSemanticsProfile `json:"token_semantics_profile,omitempty"`
	UpstreamRequestCount  int64                 `json:"upstream_request_count"`
	ProviderRequestID     *string               `json:"provider_request_id,omitempty"`
	SanitizedDetails      map[string]int64      `json:"sanitized_details,omitempty"`
	ErrorCode             *string               `json:"error_code,omitempty"`

	APIType      *string `json:"-"`
	TotalTokens  *int64  `json:"-"`
	ErrorMessage *string `json:"-"`
}

type FinalizeRequest struct {
	InvocationID string       `json:"invocation_id"`
	Finalization Finalization `json:"finalization"`
}

type RangeField string

const (
	RangeByStartedAt RangeField = "started_at"
	RangeByEndedAt   RangeField = "ended_at"
)

// Query uses a half-open range [From, To). A zero endpoint is unbounded.
type Query struct {
	From       time.Time
	To         time.Time
	RangeField RangeField

	RunID               string
	ThreadID            string
	JobID               string
	PlanID              string
	Operation           Operation
	ProviderID          string
	ConnectionID        string
	ProfileID           string
	PlannedAPISurface   string
	EffectiveAPISurface string
	ResolvedModel       string
	RequestedModel      string
	Status              Status
	UsageQuality        Quality
	Limit               int

	ProviderConfigID string // Deprecated alias for ConnectionID.
}

// TokenTotals contains exact sums and explicit coverage. Lower-bound values are
// retained separately and never added to the exact sums.
type TokenTotals struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	ProviderTotalTokens int64 `json:"provider_total_tokens"`
	DerivedTotalTokens  int64 `json:"derived_total_tokens"`
	CachedInputTokens   int64 `json:"cached_input_tokens"`
	CacheWriteTokens    int64 `json:"cache_write_tokens"`
	ReasoningTokens     int64 `json:"reasoning_tokens"`
	TotalTokens         int64 `json:"total_tokens"` // Deprecated preferred-total rollup.

	LowerBoundInputTokens         int64 `json:"lower_bound_input_tokens"`
	LowerBoundOutputTokens        int64 `json:"lower_bound_output_tokens"`
	LowerBoundProviderTotalTokens int64 `json:"lower_bound_provider_total_tokens"`
	LowerBoundDerivedTotalTokens  int64 `json:"lower_bound_derived_total_tokens"`
	LowerBoundTotalTokens         int64 `json:"lower_bound_total_tokens"`
	LowerBoundCachedInputTokens   int64 `json:"lower_bound_cached_input_tokens"`
	LowerBoundCacheWriteTokens    int64 `json:"lower_bound_cache_write_tokens"`
	LowerBoundReasoningTokens     int64 `json:"lower_bound_reasoning_tokens"`

	InvocationCount                  int64 `json:"invocation_count"`
	UpstreamRequestCount             int64 `json:"upstream_request_count"`
	CallsWithUsage                   int64 `json:"calls_with_usage"`
	CallsWithInputTokens             int64 `json:"calls_with_input_tokens"`
	CallsWithOutputTokens            int64 `json:"calls_with_output_tokens"`
	CallsWithProviderTotalTokens     int64 `json:"calls_with_provider_total_tokens"`
	CallsWithDerivedTotalTokens      int64 `json:"calls_with_derived_total_tokens"`
	CallsWithTotalTokens             int64 `json:"calls_with_total_tokens"`
	CallsWithCachedInput             int64 `json:"calls_with_cached_input_tokens"`
	CallsWithCacheWrite              int64 `json:"calls_with_cache_write_tokens"`
	CallsWithReasoning               int64 `json:"calls_with_reasoning_tokens"`
	CallsWithLowerBoundInput         int64 `json:"calls_with_lower_bound_input_tokens"`
	CallsWithLowerBoundOutput        int64 `json:"calls_with_lower_bound_output_tokens"`
	CallsWithLowerBoundProviderTotal int64 `json:"calls_with_lower_bound_provider_total_tokens"`
	CallsWithLowerBoundDerivedTotal  int64 `json:"calls_with_lower_bound_derived_total_tokens"`
	CallsWithLowerBoundTotal         int64 `json:"calls_with_lower_bound_total_tokens"`
	CallsWithLowerBoundCachedInput   int64 `json:"calls_with_lower_bound_cached_input_tokens"`
	CallsWithLowerBoundCacheWrite    int64 `json:"calls_with_lower_bound_cache_write_tokens"`
	CallsWithLowerBoundReasoning     int64 `json:"calls_with_lower_bound_reasoning_tokens"`
	LowerBoundCalls                  int64 `json:"lower_bound_calls"`
	ProviderReportedCalls            int64 `json:"provider_reported_calls"`
	DerivedCalls                     int64 `json:"derived_calls"`
	LegacyPartialCalls               int64 `json:"legacy_partial_calls"`
	MissingUsageCalls                int64 `json:"missing_usage_calls"`
	FailedCalls                      int64 `json:"failed_calls"`
	CancelledCalls                   int64 `json:"cancelled_calls"`
	InterruptedCalls                 int64 `json:"interrupted_calls"`
}

func (t *TokenTotals) AddInvocation(inv Invocation) {
	if t == nil {
		return
	}
	_ = normalizeInvocationAliases(&inv)
	inv = cloneInvocation(inv)
	t.InvocationCount++
	t.UpstreamRequestCount += inv.UpstreamRequestCount
	lowerBound := false
	add := func(dst, lower *int64, coverage, lowerCoverage *int64, src *int64, bit CounterMask) bool {
		if src == nil {
			return false
		}
		if inv.LowerBoundMask.Has(bit) {
			*lower += *src
			*lowerCoverage++
			lowerBound = true
			return false
		}
		*dst += *src
		*coverage++
		return true
	}
	add(&t.InputTokens, &t.LowerBoundInputTokens, &t.CallsWithInputTokens, &t.CallsWithLowerBoundInput, inv.InputTokens, CounterInput)
	add(&t.OutputTokens, &t.LowerBoundOutputTokens, &t.CallsWithOutputTokens, &t.CallsWithLowerBoundOutput, inv.OutputTokens, CounterOutput)
	providerExact := add(&t.ProviderTotalTokens, &t.LowerBoundProviderTotalTokens, &t.CallsWithProviderTotalTokens, &t.CallsWithLowerBoundProviderTotal, inv.ProviderTotalTokens, CounterProviderTotal)
	derivedExact := add(&t.DerivedTotalTokens, &t.LowerBoundDerivedTotalTokens, &t.CallsWithDerivedTotalTokens, &t.CallsWithLowerBoundDerivedTotal, inv.DerivedTotalTokens, CounterDerivedTotal)
	providerLower := inv.ProviderTotalTokens != nil && inv.LowerBoundMask.Has(CounterProviderTotal)
	derivedLower := inv.DerivedTotalTokens != nil && inv.LowerBoundMask.Has(CounterDerivedTotal)
	add(&t.CachedInputTokens, &t.LowerBoundCachedInputTokens, &t.CallsWithCachedInput, &t.CallsWithLowerBoundCachedInput, inv.CachedInputTokens, CounterCachedInput)
	add(&t.CacheWriteTokens, &t.LowerBoundCacheWriteTokens, &t.CallsWithCacheWrite, &t.CallsWithLowerBoundCacheWrite, inv.CacheWriteTokens, CounterCacheWrite)
	add(&t.ReasoningTokens, &t.LowerBoundReasoningTokens, &t.CallsWithReasoning, &t.CallsWithLowerBoundReasoning, inv.ReasoningTokens, CounterReasoning)
	if providerExact {
		t.TotalTokens += *inv.ProviderTotalTokens
		t.CallsWithTotalTokens++
		t.CallsWithUsage++
	} else if derivedExact {
		t.TotalTokens += *inv.DerivedTotalTokens
		t.CallsWithTotalTokens++
		t.CallsWithUsage++
	} else if providerLower {
		t.LowerBoundTotalTokens += *inv.ProviderTotalTokens
		t.CallsWithLowerBoundTotal++
	} else if derivedLower {
		t.LowerBoundTotalTokens += *inv.DerivedTotalTokens
		t.CallsWithLowerBoundTotal++
	} else if inv.InputTokens != nil && inv.OutputTokens != nil &&
		!inv.LowerBoundMask.Has(CounterInput) && !inv.LowerBoundMask.Has(CounterOutput) {
		// Generic compatible endpoints sometimes omit total_tokens even when
		// they provide exact raw input/output counters. Without declared token
		// semantics we cannot call their sum exact (cache or reasoning may be
		// disjoint), but it is always a useful floor instead of "no usage".
		t.LowerBoundTotalTokens += *inv.InputTokens + *inv.OutputTokens
		t.CallsWithLowerBoundTotal++
		lowerBound = true
	} else {
		t.MissingUsageCalls++
	}
	if lowerBound {
		t.LowerBoundCalls++
	}
	switch inv.UsageQuality {
	case QualityProviderReported:
		t.ProviderReportedCalls++
	case QualityDerived:
		t.DerivedCalls++
	case QualityLegacyPartial:
		t.LegacyPartialCalls++
	}
	switch inv.Status {
	case StatusFailed:
		t.FailedCalls++
	case StatusCancelled:
		t.CancelledCalls++
	case StatusInterrupted:
		t.InterruptedCalls++
	}
}

func (i Invocation) MarshalDetailsJSON() ([]byte, error) {
	if len(i.SanitizedDetails) == 0 {
		return nil, nil
	}
	return json.Marshal(i.SanitizedDetails)
}

func copyDetails(details map[string]int64) map[string]int64 {
	if len(details) == 0 {
		return nil
	}
	out := make(map[string]int64, len(details))
	for key, value := range details {
		key = strings.TrimSpace(key)
		if safeDetailKey(key) && value >= 0 {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func safeDetailKey(key string) bool {
	if key == "" || len(key) > 64 {
		return false
	}
	for _, char := range key {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func cloneInvocation(inv Invocation) Invocation {
	inv.RunID = trimOptional(inv.RunID)
	inv.ThreadID = trimOptional(inv.ThreadID)
	inv.JobID = trimOptional(inv.JobID)
	inv.PlanID = trimOptional(inv.PlanID)
	inv.ConnectionID = trimOptional(inv.ConnectionID)
	inv.ProfileID = trimOptional(inv.ProfileID)
	inv.ConfigFingerprint = trimOptional(inv.ConfigFingerprint)
	inv.PlannedAPISurface = trimOptional(inv.PlannedAPISurface)
	inv.EffectiveAPISurface = trimOptional(inv.EffectiveAPISurface)
	inv.ResolvedModel = trimOptional(inv.ResolvedModel)
	inv.ProviderRequestID = trimOptional(inv.ProviderRequestID)
	inv.ErrorCode = trimOptional(inv.ErrorCode)
	inv.InputTokens = copyInt64(inv.InputTokens)
	inv.OutputTokens = copyInt64(inv.OutputTokens)
	inv.ProviderTotalTokens = copyInt64(inv.ProviderTotalTokens)
	inv.DerivedTotalTokens = copyInt64(inv.DerivedTotalTokens)
	inv.CachedInputTokens = copyInt64(inv.CachedInputTokens)
	inv.CacheWriteTokens = copyInt64(inv.CacheWriteTokens)
	inv.ReasoningTokens = copyInt64(inv.ReasoningTokens)
	inv.SanitizedDetails = copyDetails(inv.SanitizedDetails)
	if inv.EndedAt != nil {
		endedAt := inv.EndedAt.UTC()
		inv.EndedAt = &endedAt
	}
	return hydrateCompatibility(inv)
}

func hydrateCompatibility(inv Invocation) Invocation {
	inv.ProviderConfigID = copyString(inv.ConnectionID)
	if inv.EffectiveAPISurface != nil {
		inv.APIType = copyString(inv.EffectiveAPISurface)
	} else {
		inv.APIType = copyString(inv.PlannedAPISurface)
	}
	if inv.ProviderTotalTokens != nil {
		inv.TotalTokens = copyInt64(inv.ProviderTotalTokens)
	} else {
		inv.TotalTokens = copyInt64(inv.DerivedTotalTokens)
	}
	inv.ErrorMessage = nil
	return inv
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
