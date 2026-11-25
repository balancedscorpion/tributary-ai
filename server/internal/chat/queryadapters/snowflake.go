package queryadapters

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/snowflake"
)

type snowflakeClient interface {
	SubmitAsync(context.Context, snowflake.AsyncQueryRequest) (snowflake.StatementSnapshot, error)
	PollAsync(context.Context, snowflake.StatementRequest) (snowflake.StatementSnapshot, error)
	FetchPartition(context.Context, snowflake.PartitionRequest) (snowflake.ResultPartition, error)
	CancelAsync(context.Context, snowflake.StatementRequest) (snowflake.CancelOutcome, error)
	ExplainQuery(context.Context, snowflake.ExplainRequest) (snowflake.ExplainProfile, error)
}

// Snowflake implements the SQL API async protocol. Request IDs are derived
// from the durable idempotency key; only replays of that exact ID use
// retry=true, so a lost response can be recovered without duplicate cost.
type Snowflake struct {
	client snowflakeClient

	mu       sync.Mutex
	attempts map[string]int
}

func NewSnowflake(handler *snowflake.Handler) *Snowflake {
	if handler == nil {
		return nil
	}
	return &Snowflake{client: handler, attempts: map[string]int{}}
}

func newSnowflakeForClient(client snowflakeClient) *Snowflake {
	return &Snowflake{client: client, attempts: map[string]int{}}
}

func (a *Snowflake) Provider() string { return "snowflake" }

func (a *Snowflake) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{
		IdempotentSubmit:   true,
		DetachedRepoll:     true,
		NativePagination:   true,
		QueuedStateVisible: true,
		CancelConfirmable:  true,
		ProfileBound:       60 * time.Second,
		// Snowflake SQL API status responses expose state but no defensible
		// monotonic progress counter. Query History enrichment remains raw
		// telemetry and must not be advertised as a progress signal.
		ProgressSignals: nil,
	}
}

func (a *Snowflake) Profile(ctx context.Context, req jobs.SubmitRequest) (jobs.QueryProfile, error) {
	return a.ProfileWithObserver(ctx, req, nil)
}

func (a *Snowflake) ProfileWithObserver(ctx context.Context, req jobs.SubmitRequest, obs jobs.ProfileObserver) (jobs.QueryProfile, error) {
	if a == nil || a.client == nil {
		return jobs.QueryProfile{}, errors.New("Snowflake query adapter is not configured")
	}
	requestID, err := snowflake.RequestIDForIdempotencyKey(req.IdempotencyKey + ":profile")
	if err != nil {
		return jobs.QueryProfile{}, err
	}
	providerTimeout := snowflakeProfileTimeout(ctx, req)
	var onObservation func(snowflake.ExplainObservation) error
	if obs != nil {
		onObservation = func(native snowflake.ExplainObservation) error {
			handle := snowflakeNativeHandle(native.Statement, req)
			if handle.Extra == nil {
				handle.Extra = map[string]string{}
			}
			handle.Extra["provider_timeout_ms"] = strconv.FormatInt(providerTimeout.Milliseconds(), 10)
			if err := obs.PublishProfileHandle(handle); err != nil {
				return err
			}
			return obs.PublishProfilePhase(native.Phase)
		}
	}
	profile, err := a.client.ExplainQuery(ctx, snowflake.ExplainRequest{
		SQL:                     req.SQL,
		RequestID:               requestID,
		QueryTag:                snowflakeQueryTag(req.IdempotencyKey, "profile"),
		AccountIdentifier:       firstNonBlank(metadataString(req.Metadata, "account_identifier", "account"), req.ConnectionID),
		Warehouse:               sessionString(req.Session, req.Metadata, "warehouse"),
		Database:                sessionString(req.Session, req.Metadata, "database"),
		Schema:                  sessionString(req.Session, req.Metadata, "schema"),
		Role:                    sessionString(req.Session, req.Metadata, "role"),
		User:                    sessionString(req.Session, req.Metadata, "user"),
		Timeout:                 providerTimeout,
		ClientResultChunkSizeMB: 1,
		PollInterval:            500 * time.Millisecond,
		OnObservation:           onObservation,
	})
	if err != nil {
		err = withSnowflakeRetryAfter(err)
		return jobs.QueryProfile{Valid: false, Error: err.Error()}, err
	}
	estimated := profile.BytesAssigned
	result := jobs.QueryProfile{
		Valid:           profile.ReadOnly && profile.ExplainSucceeded,
		EstimateIsExact: false,
		Warnings:        append([]string(nil), profile.Warnings...),
		ProviderDetails: map[string]any{
			"quality":             profile.Quality,
			"partitions_total":    profile.PartitionsTotal,
			"partitions_assigned": profile.PartitionsAssigned,
			"bytes_assigned":      profile.BytesAssigned,
			"operations":          profile.Operations,
			"statement_handle":    profile.StatementHandle,
			"explain_request_id":  profile.RequestID,
		},
	}
	if estimated > 0 {
		result.EstimatedBytes = &estimated
	}
	if !result.Valid {
		result.Error = "Snowflake EXPLAIN did not produce a valid optimizer profile"
		return result, errors.New(result.Error)
	}
	return result, nil
}

func snowflakeProfileTimeout(ctx context.Context, req jobs.SubmitRequest) time.Duration {
	const maximum = 60 * time.Second
	bound := maximum
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < bound {
			bound = remaining
		}
	}
	configured := snowflakeEffectiveTimeout(req.Budgets.HardExecution, req.Metadata)
	if configured > 0 && configured < bound {
		return configured
	}
	return bound
}

func (a *Snowflake) CancelProfile(ctx context.Context, handle jobs.NativeHandle) error {
	return a.Cancel(ctx, handle)
}

func (a *Snowflake) Submit(ctx context.Context, req jobs.SubmitRequest) (jobs.SubmitResult, error) {
	if a == nil || a.client == nil {
		return jobs.SubmitResult{}, jobs.SubmitNotDelivered(errors.New("Snowflake query adapter is not configured"))
	}
	requestID, err := snowflake.RequestIDForIdempotencyKey(req.IdempotencyKey)
	if err != nil {
		return jobs.SubmitResult{}, jobs.SubmitNotDelivered(err)
	}
	retry := a.nextAttempt(requestID) > 1
	timeout := snowflakeEffectiveTimeout(req.Budgets.HardExecution, req.Metadata)
	snapshot, err := a.client.SubmitAsync(ctx, snowflake.AsyncQueryRequest{
		SQL:                     req.SQL,
		RequestID:               requestID,
		QueryTag:                snowflakeQueryTag(req.IdempotencyKey, "query"),
		AccountIdentifier:       firstNonBlank(metadataString(req.Metadata, "account_identifier", "account"), req.ConnectionID),
		Warehouse:               sessionString(req.Session, req.Metadata, "warehouse"),
		Database:                sessionString(req.Session, req.Metadata, "database"),
		Schema:                  sessionString(req.Session, req.Metadata, "schema"),
		Role:                    sessionString(req.Session, req.Metadata, "role"),
		User:                    sessionString(req.Session, req.Metadata, "user"),
		Timeout:                 timeout,
		ClientResultChunkSizeMB: snowflakeChunkSizeMB(req.Budgets.MaxResultBytes),
		RowsPerResultSet:        int64(req.Budgets.MaxResultRows),
		Retry:                   retry,
	})
	if err != nil {
		return jobs.SubmitResult{}, withSnowflakeRetryAfter(err)
	}
	if strings.TrimSpace(snapshot.StatementHandle) == "" {
		if snapshot.Terminal {
			return jobs.SubmitResult{}, jobs.WithRetryAfter(
				fmt.Errorf("Snowflake submission reached terminal state %s without a statement handle: %s", snapshot.State, snapshot.Message),
				snapshot.RetryAfter,
			)
		}
		return jobs.SubmitResult{}, jobs.WithRetryAfter(
			errors.New("Snowflake accepted the statement but returned no statement handle"),
			snapshot.RetryAfter,
		)
	}
	handle := snowflakeNativeHandle(snapshot, req)
	initial := snowflakeStatus(snapshot)
	return jobs.SubmitResult{Handle: handle, Initial: &initial}, nil
}

func (a *Snowflake) Poll(ctx context.Context, handle jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	request, err := parseSnowflakeHandle(handle)
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	snapshot, err := a.client.PollAsync(ctx, request)
	if err != nil {
		return jobs.StatusSnapshot{}, withSnowflakeRetryAfter(err)
	}
	return snowflakeStatus(snapshot), nil
}

func (a *Snowflake) Fetch(ctx context.Context, handle jobs.NativeHandle, cursor jobs.FetchCursor) (jobs.ResultPage, error) {
	request, err := parseSnowflakeHandle(handle)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	snapshot, err := a.client.PollAsync(ctx, request)
	if err != nil {
		return jobs.ResultPage{}, withSnowflakeRetryAfter(err)
	}
	if !snapshot.Terminal || !snapshot.Succeeded {
		return jobs.ResultPage{}, errors.New("Snowflake results requested before successful completion")
	}
	partition, rowOffset, first, err := snowflakePartitionCursor(cursor.PageToken)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	columns := snapshot.Columns
	rows := snapshot.Rows
	if !first || (len(rows) == 0 && len(snapshot.ResultMetadata.PartitionInfo) > 0) {
		fetched, fetchErr := a.client.FetchPartition(ctx, snowflake.PartitionRequest{
			StatementRequest: request,
			Partition:        partition,
		})
		if fetchErr != nil {
			return jobs.ResultPage{}, withSnowflakeRetryAfter(fetchErr)
		}
		columns = fetched.Columns
		rows = fetched.Rows
	}
	mappedRows := snowflakeRows(columns, rows)
	if rowOffset < 0 || rowOffset > len(mappedRows) {
		return jobs.ResultPage{}, errors.New("Snowflake result page token row offset is out of range")
	}
	page := boundedResultPage(mappedRows[rowOffset:], snowflakeSchema(columns), cursor.MaxRows, cursor.MaxBytes)
	totalPartitions := len(snapshot.ResultMetadata.PartitionInfo)
	nextPartition := partition + 1
	if first {
		nextPartition = 1
	}
	consumed := len(page.Rows)
	if rowOffset+consumed < len(mappedRows) {
		if consumed == 0 {
			return jobs.ResultPage{}, errors.New("Snowflake result page byte limit is too small for one row")
		}
		page.NextPageToken = fmt.Sprintf("sf:%d:%d", partition, rowOffset+consumed)
	} else if nextPartition < totalPartitions {
		page.NextPageToken = fmt.Sprintf("sf:%d:0", nextPartition)
	}
	page.Metadata = map[string]any{
		"partition":        partition,
		"partition_count":  totalPartitions,
		"total_rows":       snapshot.ResultMetadata.NumRows,
		"result_format":    snapshot.ResultMetadata.Format,
		"statement_handle": snapshot.StatementHandle,
	}
	return page, nil
}

func (a *Snowflake) Cancel(ctx context.Context, handle jobs.NativeHandle) error {
	request, err := parseSnowflakeHandle(handle)
	if err != nil {
		return err
	}
	outcome, err := a.client.CancelAsync(ctx, request)
	if err != nil {
		return withSnowflakeRetryAfter(err)
	}
	if !outcome.Accepted {
		return errors.New("Snowflake did not accept the cancellation request")
	}
	return nil
}

func (a *Snowflake) ConfirmCancel(ctx context.Context, handle jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	status, err := a.Poll(ctx, handle)
	if err != nil {
		return false, jobs.StatusSnapshot{}, err
	}
	return status.State == jobs.ProviderStateCancelled, status, nil
}

func (a *Snowflake) Adopt(ctx context.Context, handle jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	status, err := a.Poll(ctx, handle)
	if err != nil {
		var apiErr *snowflake.APIError
		if errors.As(err, &apiErr) && apiErr.HTTPStatus == 404 {
			return jobs.AdoptOutcomeOrphaned, jobs.StatusSnapshot{}, nil
		}
		return "", jobs.StatusSnapshot{}, err
	}
	if providerTerminal(status.State) {
		return jobs.AdoptOutcomeTerminal, status, nil
	}
	return jobs.AdoptOutcomeAdopted, status, nil
}

func (a *Snowflake) ReconcileSubmission(ctx context.Context, req jobs.ReconcileRequest) (jobs.ReconcileOutcome, error) {
	requestID, err := snowflake.RequestIDForIdempotencyKey(req.IdempotencyKey)
	if err != nil {
		return jobs.ReconcileOutcome{}, err
	}
	// Snowflake defines retry=true with the same requestId as its native
	// ambiguous-submit reconciliation operation. It is not a blind retry and
	// cannot create a second statement for this durable identity.
	snapshot, err := a.client.SubmitAsync(ctx, snowflake.AsyncQueryRequest{
		SQL:                     req.SQL,
		RequestID:               requestID,
		QueryTag:                snowflakeQueryTag(req.IdempotencyKey, "query"),
		AccountIdentifier:       firstNonBlank(metadataString(req.Metadata, "account_identifier", "account"), req.ConnectionID),
		Warehouse:               metadataString(req.Metadata, "warehouse"),
		Database:                metadataString(req.Metadata, "database"),
		Schema:                  metadataString(req.Metadata, "schema"),
		Role:                    metadataString(req.Metadata, "role"),
		User:                    metadataString(req.Metadata, "user"),
		Timeout:                 snowflakeEffectiveTimeout(req.Budgets.HardExecution, req.Metadata),
		ClientResultChunkSizeMB: snowflakeChunkSizeMB(metadataInt64(req.Metadata, "max_result_bytes")),
		RowsPerResultSet:        metadataInt64(req.Metadata, "max_result_rows"),
		Retry:                   true,
	})
	if err != nil {
		return jobs.ReconcileOutcome{}, withSnowflakeRetryAfter(err)
	}
	if strings.TrimSpace(snapshot.StatementHandle) == "" {
		if snapshot.RetryAfter > 0 {
			return jobs.ReconcileOutcome{Resolution: jobs.ReconcileUnknown}, jobs.WithRetryAfter(
				errors.New("Snowflake reconciliation was throttled before a statement handle was returned"),
				snapshot.RetryAfter,
			)
		}
		return jobs.ReconcileOutcome{Resolution: jobs.ReconcileUnknown}, nil
	}
	handle := snowflakeNativeHandle(snapshot, jobs.SubmitRequest{
		SQL:            req.SQL,
		ConnectionID:   req.ConnectionID,
		IdempotencyKey: req.IdempotencyKey,
		Budgets:        req.Budgets,
		Metadata:       req.Metadata,
	})
	status := snowflakeStatus(snapshot)
	out := jobs.ReconcileOutcome{Resolution: jobs.ReconcileFound, Handle: handle, RetryAfter: snapshot.RetryAfter}
	if providerTerminal(status.State) {
		out.Snapshot = &status
	}
	return out, nil
}

func (a *Snowflake) nextAttempt(requestID string) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.attempts == nil {
		a.attempts = map[string]int{}
	}
	a.attempts[requestID]++
	return a.attempts[requestID]
}

func snowflakeQueryTag(idempotencyKey, kind string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(idempotencyKey)))
	return "dbstream:" + strings.TrimSpace(kind) + ":" + hex.EncodeToString(sum[:16])
}

func explicitSnowflakeTimeout(metadata map[string]any) time.Duration {
	millis := metadataInt64(metadata, "provider_timeout_ms", "statement_timeout_ms")
	if millis <= 0 {
		return 0
	}
	return time.Duration(millis) * time.Millisecond
}

// snowflakeEffectiveTimeout maps the durable hard budget to Snowflake's SQL
// API statement timeout. A deliberately shorter explicit provider timeout is
// preserved; a longer explicit value can never weaken the job's hard budget.
// During reconciliation hardExecution may be unavailable on older durable
// records, so the persisted budget metadata is the fallback.
func snowflakeEffectiveTimeout(hardExecution time.Duration, metadata map[string]any) time.Duration {
	if hardExecution <= 0 {
		millis := metadataInt64(metadata,
			"hard_execution_ms",
			"hard_execution_budget_ms",
			"hard_execution_timeout_ms",
		)
		if millis > 0 {
			hardExecution = time.Duration(millis) * time.Millisecond
		}
	}
	explicit := explicitSnowflakeTimeout(metadata)
	if hardExecution <= 0 {
		return explicit
	}
	if explicit > 0 && explicit < hardExecution {
		return explicit
	}
	return hardExecution
}

func snowflakeChunkSizeMB(maxBytes int64) int {
	if maxBytes <= 0 {
		return 16
	}
	megabytes := int((maxBytes + (1 << 20) - 1) >> 20)
	if megabytes < 1 {
		return 1
	}
	if megabytes > 16 {
		return 16
	}
	return megabytes
}

func snowflakeNativeHandle(snapshot snowflake.StatementSnapshot, req jobs.SubmitRequest) jobs.NativeHandle {
	timeout := snowflakeEffectiveTimeout(req.Budgets.HardExecution, req.Metadata)
	extra := map[string]string{
		"statement_status_url": snapshot.StatementStatusURL,
		"request_id":           snapshot.RequestID,
		"account_identifier":   firstNonBlank(metadataString(req.Metadata, "account_identifier", "account"), req.ConnectionID),
		"warehouse":            sessionString(req.Session, req.Metadata, "warehouse"),
		"database":             sessionString(req.Session, req.Metadata, "database"),
		"schema":               sessionString(req.Session, req.Metadata, "schema"),
		"role":                 sessionString(req.Session, req.Metadata, "role"),
		"user":                 sessionString(req.Session, req.Metadata, "user"),
	}
	if timeout > 0 {
		extra["provider_timeout_ms"] = strconv.FormatInt(timeout.Milliseconds(), 10)
	}
	return jobs.NativeHandle{
		Provider: "snowflake",
		ID:       snapshot.StatementHandle,
		Location: extra["account_identifier"],
		Extra:    extra,
	}
}

func parseSnowflakeHandle(handle jobs.NativeHandle) (snowflake.StatementRequest, error) {
	request := snowflake.StatementRequest{
		StatementHandle:    strings.TrimSpace(handle.ID),
		StatementStatusURL: handle.Extra["statement_status_url"],
		RequestID:          handle.Extra["request_id"],
		AccountIdentifier:  firstNonBlank(handle.Extra["account_identifier"], handle.Location),
		Warehouse:          handle.Extra["warehouse"],
		Database:           handle.Extra["database"],
		Schema:             handle.Extra["schema"],
		Role:               handle.Extra["role"],
		User:               handle.Extra["user"],
	}
	if request.StatementHandle == "" {
		return snowflake.StatementRequest{}, errors.New("Snowflake native handle is missing statement_handle")
	}
	return request, nil
}

func snowflakeStatus(snapshot snowflake.StatementSnapshot) jobs.StatusSnapshot {
	state := jobs.ProviderStateUnknown
	switch snapshot.State {
	case snowflake.StatementStateSubmitted:
		state = jobs.ProviderStatePending
	case snowflake.StatementStateQueued, snowflake.StatementStateBlocked:
		state = jobs.ProviderStateQueued
	case snowflake.StatementStateRunning:
		state = jobs.ProviderStateRunning
	case snowflake.StatementStateCompleted:
		state = jobs.ProviderStateSucceeded
	case snowflake.StatementStateFailed:
		state = jobs.ProviderStateFailed
	case snowflake.StatementStateCancelled:
		state = jobs.ProviderStateCancelled
	}
	status := jobs.StatusSnapshot{
		State:      state,
		RawState:   firstNonBlank(snapshot.ProviderState, snapshot.State),
		Error:      strings.TrimSpace(snapshot.Message),
		RetryAfter: snapshot.RetryAfter,
		Telemetry: map[string]any{
			"http_status":          snapshot.HTTPStatus,
			"code":                 snapshot.Code,
			"sql_state":            snapshot.SQLState,
			"throttled":            snapshot.Throttled,
			"retry_after_ms":       snapshot.RetryAfter.Milliseconds(),
			"statement_handle":     snapshot.StatementHandle,
			"statement_status_url": snapshot.StatementStatusURL,
			"result_metadata":      snapshot.ResultMetadata,
		},
	}
	return status
}

func withSnowflakeRetryAfter(err error) error {
	return jobs.WithRetryAfter(err, snowflakeRetryAfter(err))
}

func snowflakeRetryAfter(err error) time.Duration {
	var apiErr *snowflake.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return apiErr.RetryAfter
	}
	return 0
}

func snowflakePartitionCursor(token string) (partition int, rowOffset int, first bool, err error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, 0, true, nil
	}
	if !strings.HasPrefix(token, "sf:") {
		return 0, 0, false, errors.New("invalid Snowflake result page token")
	}
	parts := strings.Split(strings.TrimPrefix(token, "sf:"), ":")
	if len(parts) < 1 || len(parts) > 2 {
		return 0, 0, false, errors.New("invalid Snowflake result page token")
	}
	partition, err = strconv.Atoi(parts[0])
	if err != nil || partition < 0 {
		return 0, 0, false, errors.New("invalid Snowflake result page token")
	}
	if len(parts) == 2 {
		rowOffset, err = strconv.Atoi(parts[1])
		if err != nil || rowOffset < 0 {
			return 0, 0, false, errors.New("invalid Snowflake result page token")
		}
	}
	return partition, rowOffset, false, nil
}

func snowflakeRows(columns []snowflake.StatementColumn, data [][]any) []map[string]any {
	rows := make([]map[string]any, 0, len(data))
	for _, values := range data {
		row := make(map[string]any, len(values))
		for index, value := range values {
			name := fmt.Sprintf("col_%d", index)
			if index < len(columns) && strings.TrimSpace(columns[index].Name) != "" {
				name = strings.TrimSpace(columns[index].Name)
			}
			row[name] = value
		}
		rows = append(rows, row)
	}
	return rows
}

func snowflakeSchema(columns []snowflake.StatementColumn) []map[string]any {
	schema := make([]map[string]any, 0, len(columns))
	for _, column := range columns {
		item := map[string]any{
			"name":     column.Name,
			"type":     column.Type,
			"nullable": column.Nullable,
		}
		if column.Precision > 0 {
			item["precision"] = column.Precision
		}
		if column.Scale > 0 {
			item["scale"] = column.Scale
		}
		schema = append(schema, item)
	}
	return schema
}
