package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

const invocationSelectColumns = `id, run_id, thread_id, job_id, plan_id, operation,
	provider_id, connection_id, profile_id, config_fingerprint, planned_api_surface, effective_api_surface, requested_model, resolved_model,
	status, started_at, ended_at, updated_at, snapshot_sequence,
	input_tokens, output_tokens, provider_total_tokens, derived_total_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens,
	lower_bound_mask, usage_quality, token_semantics_profile,
	upstream_request_count, provider_request_id, sanitized_details, error_code`

type MySQLRepository struct {
	db      *sql.DB
	now     func() time.Time
	dropped atomic.Int64
}

var _ Repository = (*MySQLRepository)(nil)

func NewMySQLRepository(db *sql.DB) *MySQLRepository {
	return &MySQLRepository{db: db, now: func() time.Time { return time.Now().UTC() }}
}

func (r *MySQLRepository) Start(ctx context.Context, invocation Invocation) (Invocation, error) {
	if r == nil || r.db == nil {
		return Invocation{}, errors.New("nil usage database")
	}
	inv, err := prepareStart(ctx, invocation, r.now())
	if err != nil {
		return Invocation{}, err
	}
	details, err := inv.MarshalDetailsJSON()
	if err != nil {
		return Invocation{}, err
	}
	_, err = r.db.ExecContext(ctx, `INSERT INTO model_invocations (
		id, run_id, thread_id, job_id, plan_id, operation,
		provider_id, connection_id, profile_id, config_fingerprint, planned_api_surface, effective_api_surface, requested_model, resolved_model,
		status, started_at, ended_at, updated_at, snapshot_sequence,
		input_tokens, output_tokens, provider_total_tokens, derived_total_tokens, cached_input_tokens, cache_write_tokens, reasoning_tokens,
		lower_bound_mask, usage_quality, token_semantics_profile,
		upstream_request_count, provider_request_id, sanitized_details, error_code
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, NULL, ?, 0, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 0, ?, ?, 0, NULL, ?, NULL)`,
		inv.ID, inv.RunID, inv.ThreadID, inv.JobID, inv.PlanID, inv.Operation,
		inv.ProviderID, inv.ConnectionID, inv.ProfileID, inv.ConfigFingerprint, inv.PlannedAPISurface, inv.RequestedModel, inv.ResolvedModel,
		inv.Status, inv.StartedAt, inv.UpdatedAt, inv.UsageQuality, inv.TokenSemanticsProfile, details)
	if err != nil {
		if isMySQLDuplicate(err) {
			existing, getErr := r.Get(ctx, inv.ID)
			if getErr == nil && startEquivalent(existing, inv) {
				return existing, nil
			}
			return Invocation{}, ErrConflict
		}
		r.dropped.Add(1)
		return Invocation{}, err
	}
	return cloneInvocation(inv), nil
}

func (r *MySQLRepository) Snapshot(ctx context.Context, id string, snapshot UsageSnapshot) (Invocation, error) {
	if r == nil || r.db == nil {
		return Invocation{}, errors.New("nil usage database")
	}
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return Invocation{}, err
	}
	updated, err := applySnapshot(current, snapshot, r.now())
	if err != nil {
		return Invocation{}, err
	}
	if reflectInvocationJournalState(current, updated) {
		return current, nil
	}
	rows, err := r.persistJournalUpdate(ctx, updated, id, current.SnapshotSequence)
	if err != nil {
		return Invocation{}, err
	}
	if rows == 0 {
		return r.resolveJournalRace(ctx, id, updated)
	}
	return cloneInvocation(updated), nil
}

func (r *MySQLRepository) Finalize(ctx context.Context, id string, final Finalization) (Invocation, error) {
	if r == nil || r.db == nil {
		return Invocation{}, errors.New("nil usage database")
	}
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return Invocation{}, err
	}
	updated, err := applyFinalization(current, final, r.now())
	if err != nil {
		return Invocation{}, err
	}
	if reflectInvocationJournalState(current, updated) {
		return current, nil
	}
	rows, err := r.persistJournalUpdate(ctx, updated, id, current.SnapshotSequence)
	if err != nil {
		return Invocation{}, err
	}
	if rows == 0 {
		return r.resolveJournalRace(ctx, id, updated)
	}
	return cloneInvocation(updated), nil
}

func (r *MySQLRepository) persistJournalUpdate(ctx context.Context, inv Invocation, id string, priorSequence int64) (int64, error) {
	details, err := inv.MarshalDetailsJSON()
	if err != nil {
		return 0, err
	}
	statement := `UPDATE model_invocations SET
		resolved_model=?, effective_api_surface=?, status=?, ended_at=?, updated_at=?, snapshot_sequence=?,
		input_tokens=?, output_tokens=?, provider_total_tokens=?, derived_total_tokens=?, cached_input_tokens=?, cache_write_tokens=?, reasoning_tokens=?,
		lower_bound_mask=?, usage_quality=?, token_semantics_profile=?, upstream_request_count=?, provider_request_id=?, sanitized_details=?, error_code=?
		WHERE id=? AND status=? AND snapshot_sequence=?`
	result, err := r.db.ExecContext(ctx, statement,
		inv.ResolvedModel, inv.EffectiveAPISurface, inv.Status, inv.EndedAt, inv.UpdatedAt, inv.SnapshotSequence,
		inv.InputTokens, inv.OutputTokens, inv.ProviderTotalTokens, inv.DerivedTotalTokens, inv.CachedInputTokens, inv.CacheWriteTokens, inv.ReasoningTokens,
		uint64(inv.LowerBoundMask), inv.UsageQuality, inv.TokenSemanticsProfile, inv.UpstreamRequestCount,
		inv.ProviderRequestID, details, inv.ErrorCode, id, StatusRunning, priorSequence)
	if err != nil {
		return 0, err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return rows, nil
}

func (r *MySQLRepository) resolveJournalRace(ctx context.Context, id string, expected Invocation) (Invocation, error) {
	winner, err := r.Get(ctx, id)
	if err != nil {
		return Invocation{}, err
	}
	if journalEquivalent(winner, expected) {
		return winner, nil
	}
	return Invocation{}, ErrConflict
}

func (r *MySQLRepository) Get(ctx context.Context, id string) (Invocation, error) {
	if r == nil || r.db == nil {
		return Invocation{}, errors.New("nil usage database")
	}
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Invocation{}, fmt.Errorf("id: %w", err)
	}
	inv, err := scanInvocation(r.db.QueryRowContext(ctx, `SELECT `+invocationSelectColumns+` FROM model_invocations WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Invocation{}, ErrNotFound
	}
	if err != nil {
		return Invocation{}, err
	}
	return inv, nil
}

func (r *MySQLRepository) List(ctx context.Context, query Query) ([]Invocation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil usage database")
	}
	if err := validateQuery(query); err != nil {
		return nil, err
	}
	rangeColumn := string(queryRangeField(query))
	where := make([]string, 0, 18)
	args := make([]any, 0, 20)
	if !query.From.IsZero() {
		where = append(where, rangeColumn+` >= ?`)
		args = append(args, query.From.UTC())
	}
	if !query.To.IsZero() {
		where = append(where, rangeColumn+` < ?`)
		args = append(args, query.To.UTC())
	}
	add := func(column, value string) {
		if value = strings.TrimSpace(value); value != "" {
			where = append(where, column+` = ?`)
			args = append(args, value)
		}
	}
	add("run_id", query.RunID)
	add("thread_id", query.ThreadID)
	add("job_id", query.JobID)
	add("plan_id", query.PlanID)
	add("operation", string(query.Operation))
	add("provider_id", query.ProviderID)
	add("connection_id", queryConnectionID(query))
	add("profile_id", query.ProfileID)
	add("planned_api_surface", query.PlannedAPISurface)
	add("effective_api_surface", query.EffectiveAPISurface)
	if value := strings.TrimSpace(query.ResolvedModel); value != "" {
		where = append(where, `COALESCE(NULLIF(resolved_model, ''), requested_model) = ?`)
		args = append(args, value)
	}
	add("requested_model", query.RequestedModel)
	add("status", string(query.Status))
	add("usage_quality", string(query.UsageQuality))
	statement := `SELECT ` + invocationSelectColumns + ` FROM model_invocations`
	if len(where) > 0 {
		statement += ` WHERE ` + strings.Join(where, ` AND `)
	}
	statement += ` ORDER BY ` + rangeColumn + ` ASC, id ASC LIMIT ?`
	args = append(args, effectiveQueryLimit(query))
	rows, err := r.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Invocation, 0)
	for rows.Next() {
		inv, err := scanInvocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (r *MySQLRepository) SumByRun(ctx context.Context, runID string) (TokenTotals, error) {
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

func (r *MySQLRepository) EarliestTrackingDate(ctx context.Context) (*time.Time, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("nil usage database")
	}
	var earliest sql.NullTime
	if err := r.db.QueryRowContext(ctx, `SELECT MIN(started_at) FROM model_invocations`).Scan(&earliest); err != nil {
		return nil, err
	}
	if !earliest.Valid {
		return nil, nil
	}
	value := earliest.Time.UTC()
	return &value, nil
}

func (r *MySQLRepository) Health(ctx context.Context, now time.Time) (ReportHealth, error) {
	if r == nil || r.db == nil {
		return ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage reporting is unavailable")}, errors.New("nil usage database")
	}
	if now.IsZero() {
		now = r.now()
	}
	var running, stale int64
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN updated_at < ? THEN 1 ELSE 0 END), 0) FROM model_invocations WHERE status=?`, now.UTC().Add(-StaleInvocationAge), StatusRunning).Scan(&running, &stale)
	if err != nil {
		return ReportHealth{Status: HealthUnavailable, Message: messagePointer("usage ledger health query failed"), DroppedInvocationCount: r.dropped.Load()}, err
	}
	health := ReportHealth{Status: HealthHealthy, DroppedInvocationCount: r.dropped.Load(), RunningInvocationCount: running, StaleInvocationCount: stale}
	if health.DroppedInvocationCount > 0 || health.StaleInvocationCount > 0 {
		health.Status = HealthDegraded
		health.Message = messagePointer("usage tracking has dropped or stale invocations")
	}
	return health, nil
}

type invocationScanner interface{ Scan(dest ...any) error }

func scanInvocation(scanner invocationScanner) (Invocation, error) {
	var inv Invocation
	var runID, threadID, jobID, planID sql.NullString
	var connectionID, profileID, fingerprint, plannedSurface, effectiveSurface, resolvedModel sql.NullString
	var endedAt sql.NullTime
	var input, output, providerTotal, derivedTotal, cachedInput, cacheWrite, reasoning sql.NullInt64
	var providerRequestID, detailsJSON, errorCode sql.NullString
	var lowerBoundMask uint64
	if err := scanner.Scan(
		&inv.ID, &runID, &threadID, &jobID, &planID, &inv.Operation,
		&inv.ProviderID, &connectionID, &profileID, &fingerprint, &plannedSurface, &effectiveSurface, &inv.RequestedModel, &resolvedModel,
		&inv.Status, &inv.StartedAt, &endedAt, &inv.UpdatedAt, &inv.SnapshotSequence,
		&input, &output, &providerTotal, &derivedTotal, &cachedInput, &cacheWrite, &reasoning,
		&lowerBoundMask, &inv.UsageQuality, &inv.TokenSemanticsProfile,
		&inv.UpstreamRequestCount, &providerRequestID, &detailsJSON, &errorCode,
	); err != nil {
		return Invocation{}, err
	}
	inv.RunID = nullStringPtr(runID)
	inv.ThreadID = nullStringPtr(threadID)
	inv.JobID = nullStringPtr(jobID)
	inv.PlanID = nullStringPtr(planID)
	inv.ConnectionID = nullStringPtr(connectionID)
	inv.ProfileID = nullStringPtr(profileID)
	inv.ConfigFingerprint = nullStringPtr(fingerprint)
	inv.PlannedAPISurface = nullStringPtr(plannedSurface)
	inv.EffectiveAPISurface = nullStringPtr(effectiveSurface)
	inv.ResolvedModel = nullStringPtr(resolvedModel)
	inv.ProviderRequestID = nullStringPtr(providerRequestID)
	inv.ErrorCode = nullStringPtr(errorCode)
	inv.InputTokens = nullInt64Ptr(input)
	inv.OutputTokens = nullInt64Ptr(output)
	inv.ProviderTotalTokens = nullInt64Ptr(providerTotal)
	inv.DerivedTotalTokens = nullInt64Ptr(derivedTotal)
	inv.CachedInputTokens = nullInt64Ptr(cachedInput)
	inv.CacheWriteTokens = nullInt64Ptr(cacheWrite)
	inv.ReasoningTokens = nullInt64Ptr(reasoning)
	inv.LowerBoundMask = CounterMask(lowerBoundMask)
	if endedAt.Valid {
		value := endedAt.Time.UTC()
		inv.EndedAt = &value
	}
	if detailsJSON.Valid && strings.TrimSpace(detailsJSON.String) != "" {
		if err := json.Unmarshal([]byte(detailsJSON.String), &inv.SanitizedDetails); err != nil {
			return Invocation{}, fmt.Errorf("decode sanitized usage details: %w", err)
		}
	}
	if err := normalizeLoadedInvocation(&inv); err != nil {
		return Invocation{}, err
	}
	return cloneInvocation(inv), nil
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
func nullInt64Ptr(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func isMySQLDuplicate(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return strings.Contains(strings.ToLower(err.Error()), "duplicate entry")
}

func journalEquivalent(a, b Invocation) bool {
	a, b = durableInvocation(a), durableInvocation(b)
	a.ProviderConfigID, a.APIType, a.TotalTokens, a.ErrorMessage = nil, nil, nil, nil
	b.ProviderConfigID, b.APIType, b.TotalTokens, b.ErrorMessage = nil, nil, nil, nil
	return reflect.DeepEqual(a, b)
}
