package queryadapters

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/chat/jobs"
)

const (
	bigQueryResultCursorPrefix = "bq1:"
	// Keep provider fetches coarse-grained and stable across resumptions. The
	// caller's row/byte ceilings are enforced locally; using those ceilings as
	// BigQuery maxResults would make a MaxRows=1 drain hammer the API one row at
	// a time and would make intra-page offsets depend on the next caller's limit.
	bigQueryNativeResultPageRows int64 = 1000
)

var bigQueryBytesBilledLimitPattern = regexp.MustCompile(
	`(?i)query exceeded limit for bytes billed:\s*([0-9]+)\.?\s+([0-9]+)\s+or higher required`,
)

type bigQueryClient interface {
	PreflightQuery(context.Context, bigquery.QueryPreflightRequest) (bigquery.QueryPreflight, error)
	InsertQueryJob(context.Context, bigquery.InsertQueryJobRequest) (bigquery.QueryJobSnapshot, error)
	GetQueryJob(context.Context, bigquery.QueryJobHandle) (bigquery.QueryJobSnapshot, error)
	GetQueryResultPage(context.Context, bigquery.QueryJobHandle, string, int64) (bigquery.QueryResultPage, error)
	RequestCancelQueryJob(context.Context, bigquery.QueryJobHandle) (bigquery.QueryCancelReceipt, error)
}

// BigQuery implements the durable provider contract with deterministic
// jobs.insert identities. Retrying Submit is safe because every attempt uses
// the same native job ID and adopts a matching 409 response via jobs.get.
type BigQuery struct {
	client bigQueryClient
}

func NewBigQuery(handler *bigquery.Handler) *BigQuery {
	if handler == nil {
		return nil
	}
	return &BigQuery{client: handler}
}

func newBigQueryForClient(client bigQueryClient) *BigQuery {
	return &BigQuery{client: client}
}

func (a *BigQuery) Provider() string { return "bigquery" }

func (a *BigQuery) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{
		IdempotentSubmit:   true,
		DetachedRepoll:     true,
		NativePagination:   true,
		QueuedStateVisible: true,
		CancelConfirmable:  true,
		ProgressSignals: []jobs.ProgressSignal{
			jobs.SignalBytesProcessed,
		},
	}
}

func (a *BigQuery) Profile(ctx context.Context, req jobs.SubmitRequest) (jobs.QueryProfile, error) {
	if a == nil || a.client == nil {
		return jobs.QueryProfile{}, errors.New("BigQuery query adapter is not configured")
	}
	projectID := firstNonBlank(metadataString(req.Metadata, "project_id"), req.ConnectionID)
	profile, err := a.client.PreflightQuery(ctx, bigquery.QueryPreflightRequest{
		ProjectID: projectID,
		Location:  metadataString(req.Metadata, "location", "job_location"),
		SQL:       req.SQL,
	})
	if err != nil {
		return jobs.QueryProfile{Valid: false, Error: err.Error()}, err
	}
	estimated := profile.EstimatedBytesProcessed
	warnings := make([]string, 0, len(profile.Warnings))
	for _, warning := range profile.Warnings {
		if message := strings.TrimSpace(warning.Message); message != "" {
			warnings = append(warnings, message)
		}
	}
	return jobs.QueryProfile{
		Valid:          true,
		EstimatedBytes: &estimated,
		// The dry run is trustworthy for approval, but it is not a stable
		// byte-perfect execution cap: table state and BigQuery's submission
		// estimate can move between these two requests.
		EstimateIsExact: false,
		Warnings:        warnings,
		ProviderDetails: map[string]any{
			"project_id":                profile.ProjectID,
			"location":                  profile.Location,
			"schema":                    profile.Schema,
			"estimated_bytes_processed": profile.EstimatedBytesProcessed,
		},
	}, nil
}

func (a *BigQuery) Submit(ctx context.Context, req jobs.SubmitRequest) (jobs.SubmitResult, error) {
	if a == nil || a.client == nil {
		return jobs.SubmitResult{}, jobs.SubmitNotDelivered(errors.New("BigQuery query adapter is not configured"))
	}
	projectID := firstNonBlank(metadataString(req.Metadata, "project_id"), req.ConnectionID)
	location := metadataString(req.Metadata, "location", "job_location")
	jobID := bigquery.DeterministicQueryJobID(req.IdempotencyKey, 0)
	native := bigquery.QueryJobHandle{ProjectID: projectID, JobID: jobID, Location: location}
	labels := bigQuerySubmissionLabels(req.IdempotencyKey, req.SQL)
	useQueryCache := true
	if value := strings.TrimSpace(req.Session["use_query_cache"]); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			useQueryCache = parsed
		}
	}
	snapshot, err := a.client.InsertQueryJob(ctx, bigquery.InsertQueryJobRequest{
		Handle:             native,
		SQL:                req.SQL,
		MaximumBytesBilled: req.Budgets.MaxScanBytes,
		JobTimeout:         req.Budgets.HardExecution,
		Priority:           sessionString(req.Session, req.Metadata, "priority"),
		UseQueryCache:      &useQueryCache,
		Labels:             labels,
	})
	if err != nil {
		if bigquery.QueryHTTPStatus(err) != http.StatusConflict {
			return jobs.SubmitResult{}, err
		}
		// A deterministic ID conflict is adoption only after both the SQL hash
		// and durable labels match. Never attach to an unrelated native job.
		snapshot, err = a.client.GetQueryJob(ctx, native)
		if err != nil {
			return jobs.SubmitResult{}, err
		}
		if !snapshot.MatchesSubmission(bigquery.QuerySQLSHA256(req.SQL), labels) {
			return jobs.SubmitResult{}, fmt.Errorf("BigQuery deterministic job ID %s belongs to a different submission", jobID)
		}
	}
	if snapshot.Handle.ProjectID != "" {
		native = snapshot.Handle
	}
	return jobs.SubmitResult{Handle: bigQueryNativeHandle(native, labels, req.SQL)}, nil
}

func (a *BigQuery) Poll(ctx context.Context, handle jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	native, err := parseBigQueryHandle(handle)
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	snapshot, err := a.client.GetQueryJob(ctx, native)
	if err != nil {
		return jobs.StatusSnapshot{}, err
	}
	return bigQueryStatus(snapshot), nil
}

func (a *BigQuery) Fetch(ctx context.Context, handle jobs.NativeHandle, cursor jobs.FetchCursor) (jobs.ResultPage, error) {
	native, err := parseBigQueryHandle(handle)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	providerPageToken, rowOffset, err := parseBigQueryResultCursor(cursor.PageToken)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	page, err := a.client.GetQueryResultPage(ctx, native, providerPageToken, bigQueryNativeResultPageRows)
	if err != nil {
		return jobs.ResultPage{}, err
	}
	if !page.JobComplete {
		return jobs.ResultPage{}, errors.New("BigQuery results are not available before job completion")
	}
	if rowOffset > len(page.Rows) {
		return jobs.ResultPage{}, errors.New("BigQuery result page token row offset is out of range")
	}
	out := boundedResultPage(page.Rows[rowOffset:], page.Schema, cursor.MaxRows, cursor.MaxBytes)
	consumedOffset := rowOffset + len(out.Rows)
	switch {
	case consumedOffset < len(page.Rows):
		// A local row/byte ceiling clipped this native page. Re-fetch the same
		// provider page after restart and resume at the first uncommitted row.
		out.NextPageToken = encodeBigQueryResultCursor(providerPageToken, consumedOffset)
	case strings.TrimSpace(page.NextPageToken) != "":
		// Advance the provider cursor only after every row in this page has
		// been admitted. Offset zero makes the next fetch restart-safe too.
		out.NextPageToken = encodeBigQueryResultCursor(page.NextPageToken, 0)
	}
	out.Metadata = map[string]any{
		"total_rows":            page.TotalRows,
		"total_bytes_processed": page.TotalBytesProcessed,
		"cache_hit":             page.CacheHit,
		"warnings":              page.Warnings,
	}
	return out, nil
}

func encodeBigQueryResultCursor(providerPageToken string, rowOffset int) string {
	encodedProviderToken := base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSpace(providerPageToken)))
	return bigQueryResultCursorPrefix + strconv.Itoa(rowOffset) + ":" + encodedProviderToken
}

// parseBigQueryResultCursor accepts both composite cursors emitted by this
// adapter and legacy raw BigQuery page tokens persisted before composite
// cursors were introduced.
func parseBigQueryResultCursor(cursor string) (providerPageToken string, rowOffset int, err error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return "", 0, nil
	}
	if !strings.HasPrefix(cursor, bigQueryResultCursorPrefix) {
		return cursor, 0, nil
	}
	offsetText, encodedProviderToken, ok := strings.Cut(strings.TrimPrefix(cursor, bigQueryResultCursorPrefix), ":")
	if !ok {
		return "", 0, errors.New("invalid BigQuery result page token")
	}
	rowOffset, err = strconv.Atoi(offsetText)
	if err != nil || rowOffset < 0 {
		return "", 0, errors.New("invalid BigQuery result page token")
	}
	decodedProviderToken, err := base64.RawURLEncoding.DecodeString(encodedProviderToken)
	if err != nil {
		return "", 0, errors.New("invalid BigQuery result page token")
	}
	return string(decodedProviderToken), rowOffset, nil
}

func (a *BigQuery) Cancel(ctx context.Context, handle jobs.NativeHandle) error {
	native, err := parseBigQueryHandle(handle)
	if err != nil {
		return err
	}
	receipt, err := a.client.RequestCancelQueryJob(ctx, native)
	if err != nil {
		return err
	}
	if !receipt.Accepted {
		return errors.New("BigQuery did not accept the cancellation request")
	}
	return nil
}

func (a *BigQuery) ConfirmCancel(ctx context.Context, handle jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	snapshot, err := a.Poll(ctx, handle)
	if err != nil {
		return false, jobs.StatusSnapshot{}, err
	}
	return snapshot.State == jobs.ProviderStateCancelled, snapshot, nil
}

func (a *BigQuery) Adopt(ctx context.Context, handle jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	snapshot, err := a.Poll(ctx, handle)
	if err != nil {
		if bigquery.QueryHTTPStatus(err) == http.StatusNotFound {
			return jobs.AdoptOutcomeOrphaned, jobs.StatusSnapshot{}, nil
		}
		return "", jobs.StatusSnapshot{}, err
	}
	if providerTerminal(snapshot.State) {
		return jobs.AdoptOutcomeTerminal, snapshot, nil
	}
	return jobs.AdoptOutcomeAdopted, snapshot, nil
}

func (a *BigQuery) ReconcileSubmission(ctx context.Context, req jobs.ReconcileRequest) (jobs.ReconcileOutcome, error) {
	projectID := firstNonBlank(metadataString(req.Metadata, "project_id"), req.ConnectionID)
	native := bigquery.QueryJobHandle{
		ProjectID: projectID,
		JobID:     bigquery.DeterministicQueryJobID(req.IdempotencyKey, 0),
		Location:  metadataString(req.Metadata, "location", "job_location"),
	}
	snapshot, err := a.client.GetQueryJob(ctx, native)
	if err != nil {
		if bigquery.QueryHTTPStatus(err) == http.StatusNotFound {
			return jobs.ReconcileOutcome{Resolution: jobs.ReconcileUnknown}, nil
		}
		return jobs.ReconcileOutcome{}, err
	}
	labels := bigQuerySubmissionLabels(req.IdempotencyKey, req.SQL)
	if !snapshot.MatchesSubmission(bigquery.QuerySQLSHA256(req.SQL), labels) {
		return jobs.ReconcileOutcome{Resolution: jobs.ReconcileUnknown}, nil
	}
	handle := bigQueryNativeHandle(snapshot.Handle, labels, req.SQL)
	status := bigQueryStatus(snapshot)
	out := jobs.ReconcileOutcome{Resolution: jobs.ReconcileFound, Handle: handle}
	if providerTerminal(status.State) {
		out.Snapshot = &status
	}
	return out, nil
}

func bigQuerySubmissionLabels(idempotencyKey, sql string) map[string]string {
	idemHash := sha256.Sum256([]byte(strings.TrimSpace(idempotencyKey)))
	sqlHash := bigquery.QuerySQLSHA256(sql)
	return map[string]string{
		"dbstream_idem": hex.EncodeToString(idemHash[:16]),
		"dbstream_sql":  sqlHash[:32],
	}
}

func bigQueryNativeHandle(native bigquery.QueryJobHandle, labels map[string]string, sql string) jobs.NativeHandle {
	extra := map[string]string{
		"project_id": native.ProjectID,
		"sql_sha256": bigquery.QuerySQLSHA256(sql),
	}
	_ = putJSON(extra, "labels", labels)
	return jobs.NativeHandle{Provider: "bigquery", ID: native.JobID, Location: native.Location, Extra: extra}
}

func parseBigQueryHandle(handle jobs.NativeHandle) (bigquery.QueryJobHandle, error) {
	native := bigquery.QueryJobHandle{
		ProjectID: strings.TrimSpace(handle.Extra["project_id"]),
		JobID:     strings.TrimSpace(handle.ID),
		Location:  strings.TrimSpace(handle.Location),
	}
	if native.ProjectID == "" || native.JobID == "" {
		return bigquery.QueryJobHandle{}, errors.New("BigQuery native handle is missing project_id or job_id")
	}
	return native, nil
}

func bigQueryStatus(snapshot bigquery.QueryJobSnapshot) jobs.StatusSnapshot {
	state := jobs.ProviderStateUnknown
	switch {
	case snapshot.Cancelled:
		state = jobs.ProviderStateCancelled
	case snapshot.Terminal && snapshot.Succeeded:
		state = jobs.ProviderStateSucceeded
	case snapshot.Terminal:
		state = jobs.ProviderStateFailed
	case strings.EqualFold(snapshot.State, "PENDING"):
		state = jobs.ProviderStateQueued
	case strings.EqualFold(snapshot.State, "RUNNING"):
		state = jobs.ProviderStateRunning
	}
	processed := snapshot.Statistics.TotalBytesProcessed
	// Query-plan stages can be rewritten between observations, so the maximum
	// recordsWritten value is useful display telemetry but is not declared as a
	// monotonic ProgressSignal for stall detection.
	rows := int64(0)
	for _, stage := range snapshot.Statistics.QueryPlan {
		if stage.RecordsWritten > rows {
			rows = stage.RecordsWritten
		}
	}
	status := jobs.StatusSnapshot{
		State:          state,
		RawState:       snapshot.State,
		BytesProcessed: &processed,
		RowsProcessed:  &rows,
		Telemetry: map[string]any{
			"total_bytes_billed":       snapshot.Statistics.TotalBytesBilled,
			"total_slot_ms":            snapshot.Statistics.TotalSlotMs,
			"cache_hit":                snapshot.Statistics.CacheHit,
			"creation_time_ms":         snapshot.Statistics.CreationTimeMs,
			"start_time_ms":            snapshot.Statistics.StartTimeMs,
			"end_time_ms":              snapshot.Statistics.EndTimeMs,
			"statement_type":           snapshot.Statistics.StatementType,
			"timeline":                 snapshot.Statistics.Timeline,
			"query_plan":               snapshot.Statistics.QueryPlan,
			"referenced_tables":        snapshot.Statistics.ReferencedTables,
			"quota_deferments":         snapshot.Statistics.QuotaDeferments,
			"result_available":         snapshot.ResultAvailable,
			"provider_warning_details": snapshot.Warnings,
		},
	}
	if snapshot.Error != nil {
		status.Error = firstNonBlank(snapshot.Error.Message, snapshot.Error.Reason, "BigQuery query failed")
		if normalized, authorizedCap, requiredBytes, ok := normalizeBigQueryBytesBilledLimitError(status.Error); ok {
			status.Error = normalized
			status.Telemetry["bytes_billed_limit_exceeded"] = true
			status.Telemetry["maximum_bytes_billed"] = authorizedCap
			status.Telemetry["minimum_bytes_required"] = requiredBytes
		}
	}
	return status
}

func normalizeBigQueryBytesBilledLimitError(message string) (normalized string, authorizedCap, requiredBytes int64, ok bool) {
	match := bigQueryBytesBilledLimitPattern.FindStringSubmatch(strings.TrimSpace(message))
	if len(match) != 3 {
		return "", 0, 0, false
	}
	authorizedCap, capErr := strconv.ParseInt(match[1], 10, 64)
	requiredBytes, requiredErr := strconv.ParseInt(match[2], 10, 64)
	if capErr != nil || requiredErr != nil || authorizedCap <= 0 || requiredBytes <= 0 {
		return "", 0, 0, false
	}
	return fmt.Sprintf(
		"BigQuery rejected the query before execution because the authorized maximumBytesBilled cap was %d bytes, while the current provider estimate requires at least %d bytes. No rows were materialized.",
		authorizedCap,
		requiredBytes,
	), authorizedCap, requiredBytes, true
}

func providerTerminal(state jobs.ProviderState) bool {
	switch state {
	case jobs.ProviderStateSucceeded, jobs.ProviderStateFailed, jobs.ProviderStateCancelled:
		return true
	default:
		return false
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
