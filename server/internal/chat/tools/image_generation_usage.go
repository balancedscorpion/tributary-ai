package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/usage"
	"ai-analyst-local/internal/config"
)

const imageUsagePersistenceTimeout = 3 * time.Second

type imageUsageCounters struct {
	input, output, providerTotal, derivedTotal *int64
	quality                                    usage.Quality
	details                                    map[string]int64
}

// imageUsageTracker gives direct Images API calls the same durable lifecycle
// as streamed chat providers: start before egress, snapshot every dispatch,
// and finalize exactly one logical invocation after all retries.
type imageUsageTracker struct {
	repository usage.Repository
	id         string
	model      string
	sequence   int64
	upstream   int64
	status     usage.Status
	errorCode  string
	requestID  string
	counters   imageUsageCounters
}

func beginImageUsage(ctx context.Context, exec ExecContext, provider config.ProviderConfig, model string) (*imageUsageTracker, error) {
	if exec.Usage == nil {
		return nil, usage.ErrAccountingUnavailable
	}
	providerFamily := strings.ToLower(strings.TrimSpace(provider.ProviderID))
	if providerFamily == "" {
		providerFamily = "compatible"
	}
	attr, _ := usage.AttributionFromContext(ctx)
	if attr.RunID == "" {
		attr.RunID = strings.TrimSpace(exec.RunID)
	}
	if attr.ThreadID == "" {
		attr.ThreadID = strings.TrimSpace(exec.ThreadID)
	}
	attr.Operation = usage.OperationImageGeneration
	attr.PlannedAPISurface = string(usage.APISurfaceImages)
	accountingCtx := usage.WithAttribution(ctx, attr)
	invocation := usage.NewInvocation(accountingCtx, usage.ProviderIdentity{
		ProviderID:        providerFamily,
		ConnectionID:      strings.TrimSpace(provider.ID),
		PlannedAPISurface: string(usage.APISurfaceImages),
		RequestedModel:    strings.TrimSpace(model),
		ResolvedModel:     strings.TrimSpace(model),
	})
	persistCtx, cancel := context.WithTimeout(accountingCtx, imageUsagePersistenceTimeout)
	started, err := exec.Usage.Start(persistCtx, invocation)
	cancel()
	if err != nil {
		return nil, usage.ErrAccountingUnavailable
	}
	return &imageUsageTracker{
		repository: exec.Usage,
		id:         started.ID,
		model:      strings.TrimSpace(model),
		sequence:   started.SnapshotSequence,
		status:     usage.StatusFailed,
		errorCode:  "PROVIDER_ERROR",
		counters: imageUsageCounters{
			quality: usage.QualityMissing,
			details: map[string]int64{},
		},
	}, nil
}

func (t *imageUsageTracker) invocationID() string {
	if t == nil {
		return ""
	}
	return t.id
}

// dispatch is deliberately persisted before http.Do. If the durable journal
// cannot record the attempt, the caller must not send the billable request.
func (t *imageUsageTracker) dispatch(ctx context.Context) error {
	if t == nil || t.repository == nil {
		return usage.ErrAccountingUnavailable
	}
	nextSequence := t.sequence + 1
	nextUpstream := t.upstream + 1
	surface := string(usage.APISurfaceImages)
	model := t.model
	persistCtx, cancel := context.WithTimeout(ctx, imageUsagePersistenceTimeout)
	updated, err := t.repository.Snapshot(persistCtx, t.id, usage.UsageSnapshot{
		Sequence: nextSequence, ObservedAt: time.Now().UTC(), ResolvedModel: &model,
		EffectiveAPISurface: &surface, UsageQuality: usage.QualityMissing,
		TokenSemanticsProfile: usage.TokenSemanticsOpenAIImagesV1,
		UpstreamRequestCount:  nextUpstream,
	})
	cancel()
	if err != nil {
		return usage.ErrAccountingUnavailable
	}
	t.sequence = updated.SnapshotSequence
	t.upstream = updated.UpstreamRequestCount
	return nil
}

// observe durably journals the provider's cumulative counters as soon as the
// response is decoded. Finalization is still deferred so the artifact outcome
// can determine the terminal status, but a process crash between those two
// points cannot discard usage for the already-billed request.
func (t *imageUsageTracker) observe(raw map[string]any, requestID string, generatedImage bool) error {
	if t == nil || t.repository == nil {
		return usage.ErrAccountingUnavailable
	}
	t.requestID = strings.TrimSpace(requestID)
	t.counters = parseImageUsage(raw)
	if generatedImage {
		t.counters.details["generated_images"] = 1
	}
	nextSequence := t.sequence + 1
	surface := string(usage.APISurfaceImages)
	model := t.model
	snapshot := usage.UsageSnapshot{
		Sequence: nextSequence, ObservedAt: time.Now().UTC(), ResolvedModel: &model,
		EffectiveAPISurface: &surface,
		InputTokens:         t.counters.input, OutputTokens: t.counters.output,
		ProviderTotalTokens: t.counters.providerTotal, DerivedTotalTokens: t.counters.derivedTotal,
		UsageQuality: t.counters.quality, TokenSemanticsProfile: usage.TokenSemanticsOpenAIImagesV1,
		UpstreamRequestCount: t.upstream, SanitizedDetails: t.counters.details,
	}
	if t.requestID != "" {
		snapshot.ProviderRequestID = &t.requestID
	}
	persistCtx, cancel := context.WithTimeout(context.Background(), imageUsagePersistenceTimeout)
	updated, err := t.repository.Snapshot(persistCtx, t.id, snapshot)
	cancel()
	if err != nil {
		return usage.ErrAccountingUnavailable
	}
	t.sequence = updated.SnapshotSequence
	return nil
}

func (t *imageUsageTracker) succeed() {
	if t == nil {
		return
	}
	t.status = usage.StatusCompleted
	t.errorCode = ""
}

func (t *imageUsageTracker) fail(code string) {
	if t == nil {
		return
	}
	t.status = usage.StatusFailed
	t.errorCode = strings.TrimSpace(code)
}

func (t *imageUsageTracker) cancel() {
	if t == nil {
		return
	}
	t.status = usage.StatusCancelled
	t.errorCode = "ABORTED"
}

func (t *imageUsageTracker) finish(ctx context.Context) {
	if t == nil || t.repository == nil || t.id == "" {
		return
	}
	if t.status != usage.StatusCompleted && ctx != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			t.cancel()
		} else if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.fail("TIMEOUT")
		}
	}
	final := imageUsageFinalization(t.sequence+1, t.status, time.Now().UTC(), t.model, t.requestID, t.upstream, t.errorCode, t.counters)
	persistCtx, cancel := context.WithTimeout(context.Background(), imageUsagePersistenceTimeout)
	defer cancel()
	if _, err := t.repository.Finalize(persistCtx, t.id, final); err != nil {
		log.Printf("usage: image generation terminal accounting failed id=%s code=ACCOUNTING_FINALIZE_FAILED", t.id)
	}
}

func imageUsageFinalization(sequence int64, status usage.Status, endedAt time.Time, model, requestID string, upstream int64, errorCode string, counters imageUsageCounters) usage.Finalization {
	surface := string(usage.APISurfaceImages)
	model = strings.TrimSpace(model)
	requestID = strings.TrimSpace(requestID)
	final := usage.Finalization{
		Sequence: sequence, Status: status, EndedAt: endedAt.UTC(),
		ResolvedModel: &model, EffectiveAPISurface: &surface,
		InputTokens: counters.input, OutputTokens: counters.output,
		ProviderTotalTokens: counters.providerTotal, DerivedTotalTokens: counters.derivedTotal,
		UsageQuality: counters.quality, TokenSemanticsProfile: usage.TokenSemanticsOpenAIImagesV1,
		UpstreamRequestCount: upstream, SanitizedDetails: counters.details,
	}
	if requestID != "" {
		final.ProviderRequestID = &requestID
	}
	if errorCode != "" {
		final.ErrorCode = &errorCode
	}
	return final
}

func parseImageUsage(raw map[string]any) imageUsageCounters {
	counters := imageUsageCounters{
		quality: usage.QualityMissing,
		details: map[string]int64{},
	}
	if len(raw) == 0 {
		return counters
	}
	counters.input = imageUsageInt(raw["input_tokens"])
	counters.output = imageUsageInt(raw["output_tokens"])
	counters.providerTotal = imageUsageInt(raw["total_tokens"])
	if counters.providerTotal != nil {
		counters.quality = usage.QualityProviderReported
	} else if counters.input != nil && counters.output != nil {
		derived := *counters.input + *counters.output
		counters.derivedTotal = &derived
		counters.quality = usage.QualityDerived
	} else if counters.input != nil || counters.output != nil {
		counters.quality = usage.QualityProviderReported
	}
	copyDetails := func(prefix string, value any) {
		detailMap, ok := imageUsageMap(value)
		if !ok {
			return
		}
		for _, name := range []string{"text_tokens", "image_tokens"} {
			if amount := imageUsageInt(detailMap[name]); amount != nil {
				counters.details[prefix+"_"+name] = *amount
			}
		}
	}
	copyDetails("input", raw["input_tokens_details"])
	copyDetails("output", raw["output_tokens_details"])
	return counters
}

func imageUsageInt(value any) *int64 {
	var number int64
	switch typed := value.(type) {
	case int:
		number = int64(typed)
	case int32:
		number = int64(typed)
	case int64:
		number = typed
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return nil
		}
		number = int64(typed)
	case uint64:
		if typed > math.MaxInt64 {
			return nil
		}
		number = int64(typed)
	case float64:
		if typed < 0 || typed > math.MaxInt64 || math.Trunc(typed) != typed {
			return nil
		}
		number = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return nil
		}
		number = parsed
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil
		}
		number = parsed
	default:
		return nil
	}
	if number < 0 {
		return nil
	}
	return &number
}

func imageUsageMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[string]int64:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out, true
	default:
		return nil, false
	}
}

type imageArtifactPager interface {
	ListArtifactsPage(context.Context, store.ListArtifactsPageParams) (store.ArtifactPage, error)
}

// BackfillImageGenerationUsage imports exact usage retained by older generated
// image artifacts. IDs are deterministic, so repeated startups are safe; new
// artifacts carry their live invocation ID and are simply recognized.
func BackfillImageGenerationUsage(ctx context.Context, chatStore store.Store, repository usage.Repository, settings config.AppSettings) (int, error) {
	if chatStore == nil || repository == nil {
		return 0, nil
	}
	providerFamilies := make(map[string]string, len(settings.ProviderConfigs))
	for _, provider := range settings.ProviderConfigs {
		if id := strings.TrimSpace(provider.ID); id != "" {
			providerFamilies[id] = strings.ToLower(strings.TrimSpace(provider.ProviderID))
		}
	}
	pager, ok := chatStore.(imageArtifactPager)
	if !ok {
		artifacts, err := chatStore.ListArtifacts(ctx, "", 100_000)
		if err != nil {
			return 0, err
		}
		return backfillImageArtifacts(ctx, artifacts, repository, providerFamilies)
	}
	imported := 0
	var backfillErrors []error
	var beforeCreatedAt *time.Time
	beforeID := ""
	for {
		page, err := pager.ListArtifactsPage(ctx, store.ListArtifactsPageParams{
			Type: "image", Limit: 250, BeforeCreatedAt: beforeCreatedAt, BeforeID: beforeID,
		})
		if err != nil {
			return imported, err
		}
		count, err := backfillImageArtifacts(ctx, page.Artifacts, repository, providerFamilies)
		imported += count
		if err != nil {
			backfillErrors = append(backfillErrors, err)
		}
		if !page.HasMore || len(page.Artifacts) == 0 {
			return imported, errors.Join(backfillErrors...)
		}
		last := page.Artifacts[len(page.Artifacts)-1]
		createdAt := last.CreatedAt.UTC()
		beforeCreatedAt = &createdAt
		beforeID = last.ID
	}
}

func backfillImageArtifacts(ctx context.Context, artifacts []store.Artifact, repository usage.Repository, providerFamilies map[string]string) (int, error) {
	imported := 0
	var backfillErrors []error
	for _, artifact := range artifacts {
		if err := contextError(ctx); err != nil {
			return imported, err
		}
		changed, err := backfillImageArtifact(ctx, artifact, repository, providerFamilies)
		if err != nil {
			backfillErrors = append(backfillErrors, err)
			continue
		}
		if changed {
			imported++
		}
	}
	return imported, errors.Join(backfillErrors...)
}

func backfillImageArtifact(ctx context.Context, artifact store.Artifact, repository usage.Repository, providerFamilies map[string]string) (bool, error) {
	toolName := ""
	if artifact.ToolName != nil {
		toolName = strings.TrimSpace(*artifact.ToolName)
	}
	if toolName == "" {
		toolName = imageUsageString(artifact.Metadata["tool_name"])
	}
	if !strings.EqualFold(toolName, generateImageToolName) {
		return false, nil
	}
	resultMetadata, _ := imageUsageMap(artifact.Metadata["result_metadata"])
	rawUsage, _ := imageUsageMap(resultMetadata["usage"])
	counters := parseImageUsage(rawUsage)
	if len(rawUsage) > 0 && counters.input == nil && counters.output == nil && counters.providerTotal == nil && counters.derivedTotal == nil {
		// The artifact proves that an image was generated even when an older or
		// provider-specific usage payload cannot be decoded. Import the invocation
		// as missing-quality instead of silently omitting it or retrying this
		// permanent data-shape mismatch forever.
		counters.details["usage_payload_unrecognized"] = 1
	}
	counters.details["generated_images"] = 1
	connectionID := firstNonEmptyImage(
		imageUsageString(artifact.Metadata["connection_id"]), imageUsageString(resultMetadata["connection_id"]),
		imageUsageString(artifact.Metadata["provider_id"]), imageUsageString(resultMetadata["provider_id"]),
	)
	providerFamily := firstNonEmptyImage(
		imageUsageString(artifact.Metadata["provider_family"]), imageUsageString(resultMetadata["provider_family"]),
		providerFamilies[connectionID], "compatible",
	)
	model := firstNonEmptyImage(imageUsageString(artifact.Metadata["model"]), imageUsageString(resultMetadata["model"]), "unknown-image-model")
	invocationID := firstNonEmptyImage(
		imageUsageString(artifact.Metadata["usage_invocation_id"]), imageUsageString(resultMetadata["usage_invocation_id"]),
		"image-artifact-"+strings.TrimSpace(artifact.ID),
	)
	existing, err := repository.Get(ctx, invocationID)
	if err == nil && existing.Status.Terminal() {
		return false, nil
	}
	if err != nil && !errors.Is(err, usage.ErrNotFound) {
		return false, err
	}
	endedAt := artifact.CreatedAt.UTC()
	if parsed := imageUsageTime(artifact.Metadata["created_at"]); !parsed.IsZero() {
		endedAt = parsed
	}
	startedAt := endedAt
	if latency := imageUsageInt(resultMetadata["latency_ms"]); latency != nil && *latency > 0 {
		startedAt = endedAt.Add(-time.Duration(*latency) * time.Millisecond)
	}
	if errors.Is(err, usage.ErrNotFound) {
		attr := usage.Attribution{
			RunID:    firstNonEmptyImage(imageUsageString(artifact.Metadata["run_id"]), imageUsageString(resultMetadata["run_id"])),
			ThreadID: strings.TrimSpace(artifact.ThreadID), Operation: usage.OperationImageGeneration,
			ConnectionID: connectionID, PlannedAPISurface: string(usage.APISurfaceImages),
		}
		invocation := usage.NewInvocation(usage.WithAttribution(ctx, attr), usage.ProviderIdentity{
			ProviderID: providerFamily, ConnectionID: connectionID,
			PlannedAPISurface: string(usage.APISurfaceImages), RequestedModel: model, ResolvedModel: model,
		})
		invocation.ID = invocationID
		invocation.StartedAt = startedAt
		existing, err = repository.Start(ctx, invocation)
		if err != nil {
			return false, fmt.Errorf("backfill image usage %s: %w", artifact.ID, err)
		}
	}
	requestID := firstNonEmptyImage(imageUsageString(artifact.Metadata["request_id"]), imageUsageString(resultMetadata["request_id"]))
	final := imageUsageFinalization(existing.SnapshotSequence+1, usage.StatusCompleted, endedAt, model, requestID, 1, "", counters)
	if _, err := repository.Finalize(ctx, existing.ID, final); err != nil {
		return false, fmt.Errorf("backfill image usage %s: %w", artifact.ID, err)
	}
	return true, nil
}

func imageUsageString(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func imageUsageTime(value any) time.Time {
	text := imageUsageString(value)
	if text == "" {
		return time.Time{}
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return time.Time{}
	}
	return parsed.UTC()
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
