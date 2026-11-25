package usage

import (
	"context"
	"errors"
	"io/fs"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

var ErrAccountingUnavailable = errors.New("usage accounting is temporarily unavailable")
var ErrReconciliationDeferred = errors.New("usage journal reconciliation deferred")

// Recorder composes the local write-ahead journal with the configured primary
// repository. Journal durability gates dispatch; primary failures do not lose
// data because pending records are replayed idempotently.
type Recorder struct {
	primary       Repository
	journal       *Journal
	primaryFailed atomic.Bool
}

var _ Repository = (*Recorder)(nil)

func NewRecorder(cfgDir string, primary Repository) (*Recorder, error) {
	if primary == nil {
		return nil, errors.New("primary usage repository is required")
	}
	journal, err := NewJournal(cfgDir)
	if err != nil {
		return nil, err
	}
	recorder := &Recorder{primary: primary, journal: journal}
	if _, err := journal.recoverInterrupted(time.Now().UTC()); err != nil {
		return nil, err
	}
	return recorder, nil
}

func (r *Recorder) Start(ctx context.Context, invocation Invocation) (Invocation, error) {
	if r == nil || r.journal == nil {
		return Invocation{}, ErrAccountingUnavailable
	}
	local, err := r.journal.Begin(ctx, invocation)
	if err != nil {
		return Invocation{}, ErrAccountingUnavailable
	}
	if _, err := r.primary.Start(ctx, local); err != nil {
		r.primaryFailed.Store(true)
		return local, nil
	}
	return local, nil
}

func (r *Recorder) Snapshot(ctx context.Context, id string, snapshot UsageSnapshot) (Invocation, error) {
	local, err := r.journal.Snapshot(ctx, id, snapshot)
	if err != nil {
		return Invocation{}, err
	}
	if _, err := r.primary.Snapshot(ctx, id, snapshot); err != nil {
		if errors.Is(err, ErrNotFound) {
			err = r.replayRecord(ctx, journalRecord{Invocation: local})
		}
		if err != nil {
			r.primaryFailed.Store(true)
			return local, nil
		}
	}
	return local, nil
}

func (r *Recorder) Finalize(ctx context.Context, id string, final Finalization) (Invocation, error) {
	local, err := r.journal.Finalize(ctx, id, final)
	if err != nil {
		return Invocation{}, err
	}
	if _, err := r.primary.Finalize(ctx, id, final); err != nil {
		if errors.Is(err, ErrNotFound) {
			err = r.replayRecord(ctx, journalRecord{Invocation: local})
		}
		if err != nil {
			r.primaryFailed.Store(true)
			return local, nil
		}
	}
	if err := r.journal.Remove(id); err != nil {
		return local, err
	}
	return local, nil
}

func (r *Recorder) Get(ctx context.Context, id string) (Invocation, error) {
	if r != nil && r.journal != nil {
		r.journal.mu.Lock()
		record, err := r.journal.readLocked(id)
		r.journal.mu.Unlock()
		if err == nil {
			return cloneInvocation(record.Invocation), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Invocation{}, err
		}
	}
	return r.primary.Get(ctx, id)
}

func (r *Recorder) List(ctx context.Context, query Query) ([]Invocation, error) {
	if r == nil || r.primary == nil || r.journal == nil {
		return nil, errors.New("nil usage recorder")
	}
	if err := validateQuery(query); err != nil {
		return nil, err
	}
	primaryQuery := query
	primaryQuery.Limit = MaxQueryLimit
	primaryRows, err := r.primary.List(ctx, primaryQuery)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]Invocation, len(primaryRows))
	for _, invocation := range primaryRows {
		byID[invocation.ID] = invocation
	}
	records, err := r.journal.pendingRecords(time.Now().UTC(), -1, false)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		// The local journal is the source of truth until replay succeeds. Remove
		// any older primary version before applying the caller's query to it.
		delete(byID, record.Invocation.ID)
		if invocationMatches(record.Invocation, query) {
			byID[record.Invocation.ID] = cloneInvocation(record.Invocation)
		}
	}
	out := make([]Invocation, 0, len(byID))
	for _, invocation := range byID {
		if invocationMatches(invocation, query) {
			out = append(out, cloneInvocation(invocation))
		}
	}
	field := queryRangeField(query)
	sort.Slice(out, func(i, j int) bool {
		iTime, _ := invocationRangeTime(out[i], field)
		jTime, _ := invocationRangeTime(out[j], field)
		if iTime.Equal(jTime) {
			return out[i].ID < out[j].ID
		}
		return iTime.Before(jTime)
	})
	if limit := effectiveQueryLimit(query); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *Recorder) SumByRun(ctx context.Context, runID string) (TokenTotals, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return TokenTotals{}, errors.New("run_id is required")
	}
	invocations, err := r.List(ctx, Query{RunID: runID, Limit: MaxQueryLimit})
	if err != nil {
		return TokenTotals{}, err
	}
	if len(invocations) == MaxQueryLimit {
		return TokenTotals{}, ErrLimit
	}
	var totals TokenTotals
	for _, invocation := range invocations {
		totals.AddInvocation(invocation)
	}
	return totals, nil
}

func (r *Recorder) EarliestTrackingDate(ctx context.Context) (*time.Time, error) {
	earliest, err := r.primary.EarliestTrackingDate(ctx)
	if err != nil {
		return nil, err
	}
	records, err := r.journal.pendingRecords(time.Now().UTC(), -1, false)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		startedAt := record.Invocation.StartedAt.UTC()
		if earliest == nil || startedAt.Before(*earliest) {
			copy := startedAt
			earliest = &copy
		}
	}
	return earliest, nil
}

func (r *Recorder) Health(ctx context.Context, now time.Time) (ReportHealth, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	local := r.journal.health(now)
	primary, err := r.primary.Health(ctx, now)
	if err != nil {
		if local.Status != HealthUnavailable {
			local.Status = HealthDegraded
		}
		local.Message = optionalString("primary usage storage unavailable; local accounting journal remains active")
		return local, nil
	}
	if primary.RunningInvocationCount > local.RunningInvocationCount {
		local.RunningInvocationCount = primary.RunningInvocationCount
	}
	if primary.StaleInvocationCount > local.StaleInvocationCount {
		local.StaleInvocationCount = primary.StaleInvocationCount
	}
	if local.PendingInvocationCount == 0 && local.QuarantinedRecordCount == 0 && local.DroppedInvocationCount == 0 && primary.StaleInvocationCount == 0 {
		r.primaryFailed.Store(false)
	}
	if primary.Status == HealthUnavailable && local.Status != HealthUnavailable {
		local.Status = HealthDegraded
		local.Message = optionalString("primary usage storage unavailable; local accounting journal remains active")
	} else if primary.StaleInvocationCount > 0 || local.Status == HealthDegraded || r.primaryFailed.Load() {
		local.Status = HealthDegraded
		local.Message = optionalString("usage accounting is durable but primary reporting is catching up")
	}
	return local, nil
}

func (r *Recorder) Reconcile(ctx context.Context) (int, error) {
	records, err := r.journal.pending(time.Now().UTC(), maxJournalReplayBatch)
	if err != nil {
		return 0, err
	}
	replayed := 0
	replayFailed := false
	for _, record := range records {
		if err := contextErr(ctx); err != nil {
			return replayed, err
		}
		if err := r.replayRecord(ctx, record); err != nil {
			replayFailed = true
			r.primaryFailed.Store(true)
			_ = r.journal.markRetry(record.Invocation.ID, time.Now().UTC())
			continue
		}
		if record.Invocation.Status.Terminal() {
			if err := r.journal.Remove(record.Invocation.ID); err != nil {
				return replayed, err
			}
		}
		replayed++
	}
	if replayed > 0 && !replayFailed {
		r.primaryFailed.Store(false)
	}
	if replayFailed {
		return replayed, ErrReconciliationDeferred
	}
	if remaining := r.journal.health(time.Now().UTC()).PendingInvocationCount; remaining == 0 {
		r.primaryFailed.Store(false)
	}
	return replayed, nil
}

// ReconcileAll drains every eligible startup record before legacy primary rows
// are touched. NewRecorder has already terminalized pre-restart running records
// and cleared their retry deferrals, so a short batch is a true drain boundary.
func (r *Recorder) ReconcileAll(ctx context.Context) (int, error) {
	total := 0
	for {
		replayed, err := r.Reconcile(ctx)
		total += replayed
		if err != nil {
			return total, err
		}
		if replayed < maxJournalReplayBatch {
			return total, nil
		}
	}
}

func (r *Recorder) replayRecord(ctx context.Context, record journalRecord) error {
	inv := cloneInvocation(record.Invocation)
	inv.ProviderConfigID = nil
	inv.APIType = nil
	inv.TotalTokens = nil
	inv.ErrorMessage = nil
	if _, err := r.primary.Start(ctx, inv); err != nil && !errors.Is(err, ErrConflict) {
		return err
	}
	if inv.Status.Terminal() {
		final := finalizationFromInvocation(inv, inv.Status, endedAtOr(inv, time.Now().UTC()), errorCodeValue(inv.ErrorCode))
		final.Sequence = inv.SnapshotSequence
		_, err := r.primary.Finalize(ctx, inv.ID, final)
		return r.verifyReplayConflict(ctx, inv, err)
	}
	if inv.SnapshotSequence > 0 {
		_, err := r.primary.Snapshot(ctx, inv.ID, snapshotFromInvocation(inv))
		return r.verifyReplayConflict(ctx, inv, err)
	}
	return nil
}

func (r *Recorder) verifyReplayConflict(ctx context.Context, desired Invocation, err error) error {
	if !errors.Is(err, ErrConflict) {
		return err
	}
	existing, getErr := r.primary.Get(ctx, desired.ID)
	if getErr != nil {
		return err
	}
	if invocationCovers(existing, desired) {
		return nil
	}
	return ErrConflict
}

func invocationCovers(existing, desired Invocation) bool {
	if desired.Status.Terminal() {
		return terminalInvocationEqual(existing, desired)
	}
	if existing.ID != desired.ID || existing.ProviderID != desired.ProviderID || existing.RequestedModel != desired.RequestedModel ||
		existing.Status != desired.Status || !stringPointersEqual(existing.RunID, desired.RunID) || !stringPointersEqual(existing.ThreadID, desired.ThreadID) ||
		!stringPointersEqual(existing.JobID, desired.JobID) || !stringPointersEqual(existing.ConnectionID, desired.ConnectionID) || !stringPointersEqual(existing.ProfileID, desired.ProfileID) ||
		!stringPointersEqual(existing.PlanID, desired.PlanID) || !stringPointersEqual(existing.ConfigFingerprint, desired.ConfigFingerprint) ||
		existing.Operation != desired.Operation || existing.SnapshotSequence < desired.SnapshotSequence ||
		existing.UpstreamRequestCount < desired.UpstreamRequestCount {
		return false
	}
	if desired.Status.Terminal() && existing.Status != desired.Status {
		return false
	}
	for _, counter := range []struct {
		existing *int64
		desired  *int64
		bit      CounterMask
	}{
		{existing.InputTokens, desired.InputTokens, CounterInput},
		{existing.OutputTokens, desired.OutputTokens, CounterOutput},
		{existing.ProviderTotalTokens, desired.ProviderTotalTokens, CounterProviderTotal},
		{existing.DerivedTotalTokens, desired.DerivedTotalTokens, CounterDerivedTotal},
		{existing.CachedInputTokens, desired.CachedInputTokens, CounterCachedInput},
		{existing.CacheWriteTokens, desired.CacheWriteTokens, CounterCacheWrite},
		{existing.ReasoningTokens, desired.ReasoningTokens, CounterReasoning},
	} {
		if counter.desired == nil {
			continue
		}
		if counter.existing == nil || *counter.existing < *counter.desired {
			return false
		}
		if !desired.LowerBoundMask.Has(counter.bit) && existing.LowerBoundMask.Has(counter.bit) {
			return false
		}
	}
	return true
}

func terminalInvocationEqual(existing, desired Invocation) bool {
	if !existing.Status.Terminal() || existing.ID != desired.ID || existing.ProviderID != desired.ProviderID ||
		existing.RequestedModel != desired.RequestedModel || existing.Operation != desired.Operation || existing.Status != desired.Status ||
		existing.SnapshotSequence != desired.SnapshotSequence || existing.UpstreamRequestCount != desired.UpstreamRequestCount ||
		existing.LowerBoundMask != desired.LowerBoundMask || existing.UsageQuality != desired.UsageQuality ||
		existing.TokenSemanticsProfile != desired.TokenSemanticsProfile || !canonicalUsageTime(existing.StartedAt).Equal(canonicalUsageTime(desired.StartedAt)) ||
		!canonicalUsageTime(existing.UpdatedAt).Equal(canonicalUsageTime(desired.UpdatedAt)) || !timePointersEqual(existing.EndedAt, desired.EndedAt) ||
		!stringPointersEqual(existing.RunID, desired.RunID) || !stringPointersEqual(existing.ThreadID, desired.ThreadID) ||
		!stringPointersEqual(existing.JobID, desired.JobID) || !stringPointersEqual(existing.PlanID, desired.PlanID) ||
		!stringPointersEqual(existing.ConnectionID, desired.ConnectionID) || !stringPointersEqual(existing.ProfileID, desired.ProfileID) ||
		!stringPointersEqual(existing.ConfigFingerprint, desired.ConfigFingerprint) ||
		!stringPointersEqual(existing.PlannedAPISurface, desired.PlannedAPISurface) ||
		!stringPointersEqual(existing.EffectiveAPISurface, desired.EffectiveAPISurface) ||
		!stringPointersEqual(existing.ResolvedModel, desired.ResolvedModel) ||
		!stringPointersEqual(existing.ProviderRequestID, desired.ProviderRequestID) ||
		!stringPointersEqual(existing.ErrorCode, desired.ErrorCode) || !detailsEqual(existing.SanitizedDetails, desired.SanitizedDetails) {
		return false
	}
	for _, pair := range [][2]*int64{
		{existing.InputTokens, desired.InputTokens}, {existing.OutputTokens, desired.OutputTokens},
		{existing.ProviderTotalTokens, desired.ProviderTotalTokens}, {existing.DerivedTotalTokens, desired.DerivedTotalTokens},
		{existing.CachedInputTokens, desired.CachedInputTokens}, {existing.CacheWriteTokens, desired.CacheWriteTokens},
		{existing.ReasoningTokens, desired.ReasoningTokens},
	} {
		if !int64PointersEqual(pair[0], pair[1]) {
			return false
		}
	}
	return true
}

func timePointersEqual(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return canonicalUsageTime(*left).Equal(canonicalUsageTime(*right))
}

func int64PointersEqual(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func detailsEqual(left, right map[string]int64) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if other, ok := right[key]; !ok || other != value {
			return false
		}
	}
	return true
}

func StartReconciler(ctx context.Context, recorder *Recorder, interval time.Duration) {
	if recorder == nil {
		return
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	go func() {
		_, _ = recorder.Reconcile(ctx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = recorder.Reconcile(ctx)
			}
		}
	}()
}

func snapshotFromInvocation(inv Invocation) UsageSnapshot {
	return UsageSnapshot{
		Sequence: inv.SnapshotSequence, ObservedAt: inv.UpdatedAt,
		ResolvedModel: inv.ResolvedModel, EffectiveAPISurface: inv.EffectiveAPISurface,
		InputTokens: inv.InputTokens, OutputTokens: inv.OutputTokens,
		ProviderTotalTokens: inv.ProviderTotalTokens, DerivedTotalTokens: inv.DerivedTotalTokens,
		CachedInputTokens: inv.CachedInputTokens, CacheWriteTokens: inv.CacheWriteTokens, ReasoningTokens: inv.ReasoningTokens,
		LowerBoundMask: inv.LowerBoundMask, UsageQuality: inv.UsageQuality, TokenSemanticsProfile: inv.TokenSemanticsProfile,
		UpstreamRequestCount: inv.UpstreamRequestCount, ProviderRequestID: inv.ProviderRequestID, SanitizedDetails: inv.SanitizedDetails,
	}
}

func finalizationFromInvocation(inv Invocation, status Status, endedAt time.Time, errorCode string) Finalization {
	mask := inv.LowerBoundMask
	quality := inv.UsageQuality
	if status != StatusCompleted {
		mask |= presentCounterMask(inv)
		if mask != 0 {
			quality = QualityLegacyPartial
		}
	}
	sequence := inv.SnapshotSequence + 1
	return Finalization{
		Sequence: sequence, Status: status, EndedAt: endedAt.UTC(), ResolvedModel: inv.ResolvedModel,
		EffectiveAPISurface: inv.EffectiveAPISurface, InputTokens: inv.InputTokens, OutputTokens: inv.OutputTokens,
		ProviderTotalTokens: inv.ProviderTotalTokens, DerivedTotalTokens: inv.DerivedTotalTokens,
		CachedInputTokens: inv.CachedInputTokens, CacheWriteTokens: inv.CacheWriteTokens, ReasoningTokens: inv.ReasoningTokens,
		LowerBoundMask: mask, UsageQuality: quality, TokenSemanticsProfile: inv.TokenSemanticsProfile,
		UpstreamRequestCount: inv.UpstreamRequestCount, ProviderRequestID: inv.ProviderRequestID,
		SanitizedDetails: inv.SanitizedDetails, ErrorCode: optionalString(errorCode),
	}
}

func endedAtOr(inv Invocation, fallback time.Time) time.Time {
	if inv.EndedAt != nil {
		return inv.EndedAt.UTC()
	}
	return fallback.UTC()
}

func errorCodeValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
