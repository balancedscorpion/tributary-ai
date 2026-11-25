package usage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ai-analyst-local/internal/chat/jsondb"
)

const (
	jsonRecordKind          = "ai_analyst.model_invocation"
	jsonRecordSchemaVersion = 2
)

type jsonInvocationRecord struct {
	Kind          string     `json:"kind"`
	SchemaVersion int        `json:"schema_version"`
	Invocation    Invocation `json:"invocation"`
}

type jsonInvocationEnvelope struct {
	Kind          string          `json:"kind"`
	SchemaVersion int             `json:"schema_version"`
	Invocation    json.RawMessage `json:"invocation"`
}

// JSONRepository stores usage globally under chatdb/usage/YYYY-MM-DD. Its
// retention policy is explicitly indefinite and records do not cascade when a
// thread is deleted.
type JSONRepository struct {
	cfgDir string
	now    func() time.Time

	mu        sync.RWMutex
	pathsByID map[string]string
	dropped   atomic.Int64
}

var _ Repository = (*JSONRepository)(nil)

func NewJSONRepository(cfgDir string) (*JSONRepository, error) {
	if strings.TrimSpace(cfgDir) == "" {
		return nil, errors.New("cfgDir is required")
	}
	repo := &JSONRepository{cfgDir: cfgDir, now: func() time.Time { return time.Now().UTC() }, pathsByID: make(map[string]string)}
	if err := os.MkdirAll(repo.usageDir(), 0o700); err != nil {
		return nil, err
	}
	if err := repo.rebuildIndex(); err != nil {
		return nil, err
	}
	return repo, nil
}

func (r *JSONRepository) Start(ctx context.Context, invocation Invocation) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	inv, err := prepareStart(ctx, invocation, r.now())
	if err != nil {
		return Invocation{}, err
	}
	path := r.invocationPath(inv)
	r.mu.Lock()
	defer r.mu.Unlock()
	if existingPath, exists := r.pathsByID[inv.ID]; exists {
		existing, readErr := readJSONInvocation(existingPath)
		if readErr == nil && startEquivalent(existing, inv) {
			return cloneInvocation(existing), nil
		}
		if readErr != nil {
			return Invocation{}, readErr
		}
		return Invocation{}, ErrConflict
	}
	if _, err := os.Stat(path); err == nil {
		existing, readErr := readJSONInvocation(path)
		if readErr == nil && startEquivalent(existing, inv) {
			r.pathsByID[inv.ID] = path
			return cloneInvocation(existing), nil
		}
		if readErr != nil {
			return Invocation{}, readErr
		}
		return Invocation{}, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		r.dropped.Add(1)
		return Invocation{}, err
	}
	if err := writeJSONInvocation(path, inv); err != nil {
		r.dropped.Add(1)
		return Invocation{}, err
	}
	r.pathsByID[inv.ID] = path
	return cloneInvocation(inv), nil
}

func (r *JSONRepository) Snapshot(ctx context.Context, id string, snapshot UsageSnapshot) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, path, err := r.getLocked(id)
	if err != nil {
		return Invocation{}, err
	}
	updated, err := applySnapshot(current, snapshot, r.now())
	if err != nil {
		return Invocation{}, err
	}
	if reflectInvocationJournalState(current, updated) {
		return cloneInvocation(current), nil
	}
	if err := writeJSONInvocation(path, updated); err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(updated), nil
}

func (r *JSONRepository) Finalize(ctx context.Context, id string, final Finalization) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, path, err := r.getLocked(id)
	if err != nil {
		return Invocation{}, err
	}
	updated, err := applyFinalization(current, final, r.now())
	if err != nil {
		return Invocation{}, err
	}
	if reflectInvocationJournalState(current, updated) {
		return cloneInvocation(current), nil
	}
	if err := writeJSONInvocation(path, updated); err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(updated), nil
}

func (r *JSONRepository) getLocked(id string) (Invocation, string, error) {
	path, ok := r.pathsByID[id]
	if !ok {
		return Invocation{}, "", ErrNotFound
	}
	inv, err := readJSONInvocation(path)
	if errors.Is(err, fs.ErrNotExist) {
		delete(r.pathsByID, id)
		return Invocation{}, "", ErrNotFound
	}
	if err != nil {
		return Invocation{}, "", err
	}
	return inv, path, nil
}

func (r *JSONRepository) Get(ctx context.Context, id string) (Invocation, error) {
	if err := contextErr(ctx); err != nil {
		return Invocation{}, err
	}
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	r.mu.RLock()
	path, ok := r.pathsByID[id]
	r.mu.RUnlock()
	if !ok {
		return Invocation{}, ErrNotFound
	}
	inv, err := readJSONInvocation(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Invocation{}, ErrNotFound
	}
	if err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(inv), nil
}

func (r *JSONRepository) List(ctx context.Context, query Query) ([]Invocation, error) {
	if err := validateQuery(query); err != nil {
		return nil, err
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	paths := make([]string, 0, len(r.pathsByID))
	for _, path := range r.pathsByID {
		paths = append(paths, path)
	}
	r.mu.RUnlock()
	out := make([]Invocation, 0, min(len(paths), effectiveQueryLimit(query)))
	for _, path := range paths {
		if err := contextErr(ctx); err != nil {
			return nil, err
		}
		inv, err := readJSONInvocation(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if invocationMatches(inv, query) {
			out = append(out, cloneInvocation(inv))
		}
	}
	field := queryRangeField(query)
	sort.Slice(out, func(i, j int) bool {
		ti, _ := invocationRangeTime(out[i], field)
		tj, _ := invocationRangeTime(out[j], field)
		if ti.Equal(tj) {
			return out[i].ID < out[j].ID
		}
		return ti.Before(tj)
	})
	if limit := effectiveQueryLimit(query); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *JSONRepository) SumByRun(ctx context.Context, runID string) (TokenTotals, error) {
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
	for _, inv := range invocations {
		totals.AddInvocation(inv)
	}
	return totals, nil
}

func (r *JSONRepository) EarliestTrackingDate(ctx context.Context) (*time.Time, error) {
	invocations, err := r.List(ctx, Query{RangeField: RangeByStartedAt, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(invocations) == 0 {
		return nil, nil
	}
	earliest := invocations[0].StartedAt.UTC()
	return &earliest, nil
}

func (r *JSONRepository) Health(ctx context.Context, now time.Time) (ReportHealth, error) {
	if now.IsZero() {
		now = r.now()
	}
	invocations, err := r.List(ctx, Query{Status: StatusRunning, Limit: MaxQueryLimit})
	if err != nil {
		return ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage ledger could not be read")}, err
	}
	health := ReportHealth{Status: HealthHealthy, DroppedInvocationCount: r.dropped.Load(), RunningInvocationCount: int64(len(invocations))}
	staleBefore := now.UTC().Add(-StaleInvocationAge)
	for _, inv := range invocations {
		if inv.UpdatedAt.Before(staleBefore) {
			health.StaleInvocationCount++
		}
	}
	if health.DroppedInvocationCount > 0 || health.StaleInvocationCount > 0 {
		health.Status = HealthDegraded
		health.Message = messagePointer("usage tracking has dropped or stale invocations")
	}
	if len(invocations) == MaxQueryLimit {
		health.Status = HealthDegraded
		health.Message = messagePointer("usage ledger running-invocation count reached its safety limit")
	}
	return health, nil
}

func (r *JSONRepository) usageDir() string { return filepath.Join(jsondb.RootDir(r.cfgDir), "usage") }
func (r *JSONRepository) invocationPath(inv Invocation) string {
	return filepath.Join(r.usageDir(), inv.StartedAt.UTC().Format("2006-01-02"), inv.ID+".json")
}

func (r *JSONRepository) rebuildIndex() error {
	return filepath.WalkDir(r.usageDir(), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			return nil
		}
		inv, schemaVersion, err := decodeJSONInvocation(path)
		if err != nil {
			return fmt.Errorf("read usage record %s: %w", path, err)
		}
		if schemaVersion == 1 {
			// Rewrite through the v2 durable encoder so legacy raw error_message
			// data is physically scrubbed, not merely hidden after decoding.
			if err := writeJSONInvocation(path, inv); err != nil {
				return fmt.Errorf("upgrade usage record %s: %w", path, err)
			}
		}
		if existing, ok := r.pathsByID[inv.ID]; ok {
			return fmt.Errorf("duplicate usage invocation id %q in %s and %s", inv.ID, existing, path)
		}
		r.pathsByID[inv.ID] = path
		return nil
	})
}

func writeJSONInvocation(path string, inv Invocation) error {
	inv = durableInvocation(inv)
	record := jsonInvocationRecord{Kind: jsonRecordKind, SchemaVersion: jsonRecordSchemaVersion, Invocation: inv}
	return jsondb.WriteJSONFile(path, record)
}

func readJSONInvocation(path string) (Invocation, error) {
	inv, _, err := decodeJSONInvocation(path)
	return inv, err
}

func decodeJSONInvocation(path string) (Invocation, int, error) {
	var envelope jsonInvocationEnvelope
	if err := jsondb.ReadJSONFileStrict(path, &envelope); err != nil {
		return Invocation{}, 0, err
	}
	if envelope.Kind != jsonRecordKind {
		return Invocation{}, 0, fmt.Errorf("unexpected usage record kind %q", envelope.Kind)
	}
	var inv Invocation
	switch envelope.SchemaVersion {
	case jsonRecordSchemaVersion:
		if err := json.Unmarshal(envelope.Invocation, &inv); err != nil {
			return Invocation{}, 0, err
		}
	case 1:
		var legacy legacyJSONInvocation
		if err := json.Unmarshal(envelope.Invocation, &legacy); err != nil {
			return Invocation{}, 0, err
		}
		inv = legacy.toInvocation()
	default:
		return Invocation{}, 0, fmt.Errorf("unsupported usage record schema version %d", envelope.SchemaVersion)
	}
	if err := normalizeLoadedInvocation(&inv); err != nil {
		return Invocation{}, 0, err
	}
	return cloneInvocation(inv), envelope.SchemaVersion, nil
}

type legacyJSONInvocation struct {
	ID                   string           `json:"id"`
	RunID                *string          `json:"run_id,omitempty"`
	ThreadID             *string          `json:"thread_id,omitempty"`
	JobID                *string          `json:"job_id,omitempty"`
	Operation            Operation        `json:"operation"`
	ProviderID           string           `json:"provider_id"`
	ProviderConfigID     *string          `json:"provider_config_id,omitempty"`
	ProfileID            *string          `json:"profile_id,omitempty"`
	APIType              *string          `json:"api_type,omitempty"`
	RequestedModel       string           `json:"requested_model"`
	ResolvedModel        *string          `json:"resolved_model,omitempty"`
	Status               Status           `json:"status"`
	StartedAt            time.Time        `json:"started_at"`
	EndedAt              *time.Time       `json:"ended_at,omitempty"`
	UpdatedAt            time.Time        `json:"updated_at"`
	InputTokens          *int64           `json:"input_tokens,omitempty"`
	OutputTokens         *int64           `json:"output_tokens,omitempty"`
	TotalTokens          *int64           `json:"total_tokens,omitempty"`
	CachedInputTokens    *int64           `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens     *int64           `json:"cache_write_tokens,omitempty"`
	ReasoningTokens      *int64           `json:"reasoning_tokens,omitempty"`
	UsageQuality         Quality          `json:"usage_quality"`
	UpstreamRequestCount int64            `json:"upstream_request_count"`
	ProviderRequestID    *string          `json:"provider_request_id,omitempty"`
	SanitizedDetails     map[string]int64 `json:"sanitized_details,omitempty"`
	ErrorCode            *string          `json:"error_code,omitempty"`
	ErrorMessage         *string          `json:"error_message,omitempty"`
}

func (legacy legacyJSONInvocation) toInvocation() Invocation {
	connectionID := legacy.ProviderConfigID
	if trimOptional(legacy.ProfileID) != nil {
		// Historical compatible-provider records used the synthetic
		// "legacy-default" connection alongside the real profile identity. In
		// v2, profile identity is authoritative and mutually exclusive.
		connectionID = nil
	}
	var effectiveSurface *string
	if legacy.Status == StatusCompleted {
		effectiveSurface = legacy.APIType
	}
	inv := Invocation{ID: legacy.ID, RunID: legacy.RunID, ThreadID: legacy.ThreadID, JobID: legacy.JobID, Operation: legacy.Operation,
		ProviderID: legacy.ProviderID, ConnectionID: connectionID, ProfileID: legacy.ProfileID,
		PlannedAPISurface: legacy.APIType, EffectiveAPISurface: effectiveSurface, RequestedModel: legacy.RequestedModel, ResolvedModel: legacy.ResolvedModel,
		Status: legacy.Status, StartedAt: legacy.StartedAt, EndedAt: legacy.EndedAt, UpdatedAt: legacy.UpdatedAt,
		InputTokens: legacy.InputTokens, OutputTokens: legacy.OutputTokens, CachedInputTokens: legacy.CachedInputTokens,
		CacheWriteTokens: legacy.CacheWriteTokens, ReasoningTokens: legacy.ReasoningTokens, UsageQuality: legacy.UsageQuality,
		TokenSemanticsProfile: TokenSemanticsRawCountersV1, UpstreamRequestCount: legacy.UpstreamRequestCount,
		ProviderRequestID: legacy.ProviderRequestID, SanitizedDetails: legacy.SanitizedDetails, ErrorCode: legacy.ErrorCode}
	if legacy.UsageQuality == QualityDerived {
		inv.DerivedTotalTokens = legacy.TotalTokens
	} else {
		inv.ProviderTotalTokens = legacy.TotalTokens
	}
	if legacy.Status.Terminal() {
		inv.SnapshotSequence = 1
		if legacy.Status != StatusCompleted || legacy.UsageQuality == QualityLegacyPartial {
			inv.LowerBoundMask = presentCounterMask(inv)
			if inv.LowerBoundMask != 0 {
				inv.UsageQuality = QualityLegacyPartial
			}
		}
	}
	return inv
}

func invocationMatches(inv Invocation, query Query) bool {
	when, hasTime := invocationRangeTime(inv, queryRangeField(query))
	if (!query.From.IsZero() || !query.To.IsZero()) && !hasTime {
		return false
	}
	if !query.From.IsZero() && when.Before(query.From.UTC()) {
		return false
	}
	if !query.To.IsZero() && !when.Before(query.To.UTC()) {
		return false
	}
	checks := []struct{ filter, value string }{
		{query.RunID, stringValue(inv.RunID)}, {query.ThreadID, stringValue(inv.ThreadID)}, {query.JobID, stringValue(inv.JobID)},
		{query.PlanID, stringValue(inv.PlanID)}, {query.ProviderID, inv.ProviderID}, {queryConnectionID(query), stringValue(inv.ConnectionID)},
		{query.ProfileID, stringValue(inv.ProfileID)}, {query.PlannedAPISurface, stringValue(inv.PlannedAPISurface)},
		{query.EffectiveAPISurface, stringValue(inv.EffectiveAPISurface)}, {query.ResolvedModel, inv.DisplayModel()},
		{query.RequestedModel, inv.RequestedModel},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.filter) != "" && strings.TrimSpace(check.filter) != check.value {
			return false
		}
	}
	if query.Operation != "" && inv.Operation != query.Operation {
		return false
	}
	if query.Status != "" && inv.Status != query.Status {
		return false
	}
	if query.UsageQuality != "" && inv.UsageQuality != query.UsageQuality {
		return false
	}
	return true
}

func invocationRangeTime(inv Invocation, field RangeField) (time.Time, bool) {
	if field == RangeByEndedAt {
		if inv.EndedAt == nil {
			return time.Time{}, false
		}
		return inv.EndedAt.UTC(), true
	}
	return inv.StartedAt.UTC(), !inv.StartedAt.IsZero()
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func messagePointer(message string) *string { return &message }

func reflectInvocationJournalState(a, b Invocation) bool {
	return a.Status == b.Status && a.SnapshotSequence == b.SnapshotSequence && a.UpdatedAt.Equal(b.UpdatedAt)
}
