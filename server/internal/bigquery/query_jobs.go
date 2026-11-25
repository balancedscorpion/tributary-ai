package bigquery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// QueryJobHandle is the complete native identity of a BigQuery job. Location
// is part of the identity for regional jobs and must be persisted with JobID.
type QueryJobHandle struct {
	ProjectID string `json:"project_id"`
	JobID     string `json:"job_id"`
	Location  string `json:"location,omitempty"`
}

// BQError is the stable subset of BigQuery's ErrorProto used by the durable
// query supervisor. Status.Errors can contain warnings; only ErrorResult is
// authoritative for terminal failure.
type BQError struct {
	Message  string `json:"message,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Location string `json:"location,omitempty"`
}

type QueryPreflightRequest struct {
	ProjectID string
	Location  string
	SQL       string
}

type QueryPreflight struct {
	ProjectID               string           `json:"project_id"`
	Location                string           `json:"location,omitempty"`
	EstimatedBytesProcessed int64            `json:"estimated_bytes_processed"`
	EstimatedBytesText      string           `json:"estimated_bytes_processed_text,omitempty"`
	Schema                  []map[string]any `json:"schema,omitempty"`
	Warnings                []BQError        `json:"warnings,omitempty"`
}

type InsertQueryJobRequest struct {
	Handle             QueryJobHandle
	SQL                string
	MaximumBytesBilled int64
	JobTimeout         time.Duration
	Priority           string
	UseQueryCache      *bool
	Labels             map[string]string
}

type QueryTimelineSample struct {
	ElapsedMs              int64 `json:"elapsed_ms"`
	TotalSlotMs            int64 `json:"total_slot_ms"`
	PendingUnits           int64 `json:"pending_units"`
	CompletedUnits         int64 `json:"completed_units"`
	ActiveUnits            int64 `json:"active_units"`
	EstimatedRunnableUnits int64 `json:"estimated_runnable_units"`
}

type QueryStage struct {
	ID                      int64   `json:"id"`
	Name                    string  `json:"name,omitempty"`
	Status                  string  `json:"status,omitempty"`
	StartMs                 int64   `json:"start_ms"`
	EndMs                   int64   `json:"end_ms"`
	InputStages             []int64 `json:"input_stages,omitempty"`
	ParallelInputs          int64   `json:"parallel_inputs"`
	CompletedParallelInputs int64   `json:"completed_parallel_inputs"`
	RecordsRead             int64   `json:"records_read"`
	RecordsWritten          int64   `json:"records_written"`
	ShuffleOutputBytes      int64   `json:"shuffle_output_bytes"`
	ShuffleBytesSpilled     int64   `json:"shuffle_output_bytes_spilled"`
	SlotMs                  int64   `json:"slot_ms"`
}

type QueryTableReference struct {
	ProjectID string `json:"project_id,omitempty"`
	DatasetID string `json:"dataset_id,omitempty"`
	TableID   string `json:"table_id,omitempty"`
}

type QueryJobStatistics struct {
	CreationTimeMs         int64                 `json:"creation_time_ms"`
	StartTimeMs            int64                 `json:"start_time_ms"`
	EndTimeMs              int64                 `json:"end_time_ms"`
	TotalBytesProcessed    int64                 `json:"total_bytes_processed"`
	TotalBytesBilled       int64                 `json:"total_bytes_billed"`
	EstimatedBytes         int64                 `json:"estimated_bytes_processed"`
	TotalSlotMs            int64                 `json:"total_slot_ms"`
	CacheHit               bool                  `json:"cache_hit"`
	StatementType          string                `json:"statement_type,omitempty"`
	TotalPartitions        int64                 `json:"total_partitions_processed"`
	ReservationID          string                `json:"reservation_id,omitempty"`
	QuotaDeferments        []string              `json:"quota_deferments,omitempty"`
	Timeline               []QueryTimelineSample `json:"timeline,omitempty"`
	QueryPlan              []QueryStage          `json:"query_plan,omitempty"`
	ReferencedTables       []QueryTableReference `json:"referenced_tables,omitempty"`
	BytesProcessedAccuracy string                `json:"total_bytes_processed_accuracy,omitempty"`
}

type QueryJobSnapshot struct {
	Handle          QueryJobHandle     `json:"handle"`
	State           string             `json:"state"`
	Terminal        bool               `json:"terminal"`
	Succeeded       bool               `json:"succeeded"`
	Cancelled       bool               `json:"cancelled"`
	ResultAvailable bool               `json:"result_available"`
	Error           *BQError           `json:"error,omitempty"`
	Warnings        []BQError          `json:"warnings,omitempty"`
	Labels          map[string]string  `json:"labels,omitempty"`
	SQL             string             `json:"-"`
	SQLSHA256       string             `json:"sql_sha256,omitempty"`
	Statistics      QueryJobStatistics `json:"statistics"`
	Schema          []map[string]any   `json:"schema,omitempty"`
}

// MatchesSubmission verifies that a jobs.get response belongs to the durable
// submission being reconciled. Empty expectations are ignored, allowing an
// adapter to require the strongest identity signals it persisted.
func (s QueryJobSnapshot) MatchesSubmission(sqlSHA256 string, labels map[string]string) bool {
	if expected := strings.TrimSpace(sqlSHA256); expected != "" && !strings.EqualFold(expected, s.SQLSHA256) {
		return false
	}
	for key, expected := range labels {
		if s.Labels[strings.TrimSpace(key)] != strings.TrimSpace(expected) {
			return false
		}
	}
	return true
}

type QueryResultPage struct {
	Handle              QueryJobHandle   `json:"handle"`
	JobComplete         bool             `json:"job_complete"`
	Rows                []map[string]any `json:"rows,omitempty"`
	Schema              []map[string]any `json:"schema,omitempty"`
	NextPageToken       string           `json:"next_page_token,omitempty"`
	TotalRows           int64            `json:"total_rows"`
	TotalBytesProcessed int64            `json:"total_bytes_processed"`
	CacheHit            bool             `json:"cache_hit"`
	Warnings            []BQError        `json:"warnings,omitempty"`
}

type MaterializeQueryRequest struct {
	Handle   QueryJobHandle
	MaxRows  int64
	MaxBytes int64
	MaxPages int
}

type MaterializedQueryResult struct {
	Rows                []map[string]any `json:"rows,omitempty"`
	Schema              []map[string]any `json:"schema,omitempty"`
	BytesRead           int64            `json:"bytes_read"`
	PagesRead           int              `json:"pages_read"`
	TotalRows           int64            `json:"total_rows"`
	TotalBytesProcessed int64            `json:"total_bytes_processed"`
	CacheHit            bool             `json:"cache_hit"`
	LocallyTruncated    bool             `json:"locally_truncated"`
}

type QueryCancelReceipt struct {
	Handle   QueryJobHandle    `json:"handle"`
	Accepted bool              `json:"accepted"`
	Job      *QueryJobSnapshot `json:"job,omitempty"`
}

// QueryAPIError exposes enough transport information for an adapter to decide
// whether submission needs reconciliation. Ambiguous means the server may have
// accepted the request even though no usable response reached the caller.
type QueryAPIError struct {
	StatusCode      int
	Message         string
	Ambiguous       bool
	RetryAfterDelay time.Duration
	Cause           error
}

func (e *QueryAPIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *QueryAPIError) Unwrap() error { return e.Cause }

// RetryAfter implements the pacing-hint contract consumed by the durable job
// scheduler. It intentionally uses a method (rather than requiring adapters
// to know this concrete error type) so wrapped errors retain the hint.
func (e *QueryAPIError) RetryAfter() time.Duration {
	if e == nil || e.RetryAfterDelay < 0 {
		return 0
	}
	return e.RetryAfterDelay
}

func QueryHTTPStatus(err error) int {
	var qe *QueryAPIError
	if errors.As(err, &qe) {
		return qe.StatusCode
	}
	var he httpError
	if errors.As(err, &he) {
		return he.status
	}
	return 0
}

// DeterministicQueryJobID derives a stable BigQuery-safe job ID from the
// durable app job ID and attempt. Retrying the same attempt must reuse this ID.
func DeterministicQueryJobID(appJobID string, attempt int) string {
	seed := strings.TrimSpace(appJobID) + ":" + strconv.Itoa(attempt)
	sum := sha256.Sum256([]byte(seed))
	return "dbstream_" + hex.EncodeToString(sum[:16])
}

func QuerySQLSHA256(sql string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	return hex.EncodeToString(sum[:])
}

func (h *Handler) PreflightQuery(ctx context.Context, req QueryPreflightRequest) (QueryPreflight, error) {
	projectID := strings.TrimSpace(req.ProjectID)
	sql := strings.TrimSpace(req.SQL)
	location := strings.TrimSpace(req.Location)
	if projectID == "" {
		return QueryPreflight{}, errors.New("missing project_id")
	}
	if sql == "" {
		return QueryPreflight{}, errors.New("missing sql")
	}

	upURL := fmt.Sprintf("https://bigquery.googleapis.com/bigquery/v2/projects/%s/queries", url.PathEscape(projectID))
	request := bqQueryRequest{
		Query:        sql,
		UseLegacySQL: false,
		TimeoutMs:    10_000,
		DryRun:       true,
		Location:     location,
	}
	var response bqQueryResponse
	if err := h.doBQ(ctx, http.MethodPost, upURL, request, &response); err != nil {
		return QueryPreflight{}, err
	}
	if len(response.Errors) > 0 {
		message := strings.TrimSpace(response.Errors[0].Message)
		if message == "" {
			message = "BigQuery preflight failed"
		}
		return QueryPreflight{}, errors.New(message)
	}
	if response.JobReference.Location != "" {
		location = strings.TrimSpace(response.JobReference.Location)
	}
	return QueryPreflight{
		ProjectID:               projectID,
		Location:                location,
		EstimatedBytesProcessed: parseBQInt64(response.TotalBytesProcessed),
		EstimatedBytesText:      strings.TrimSpace(response.TotalBytesProcessed),
		Schema:                  bqSchemaToMaps(response.Schema.Fields),
		Warnings:                exportBQErrors(response.Errors),
	}, nil
}

func (h *Handler) InsertQueryJob(ctx context.Context, req InsertQueryJobRequest) (QueryJobSnapshot, error) {
	handle, err := normalizeQueryJobHandle(req.Handle)
	if err != nil {
		return QueryJobSnapshot{}, err
	}
	sql := strings.TrimSpace(req.SQL)
	if sql == "" {
		return QueryJobSnapshot{}, errors.New("missing sql")
	}

	useCache := req.UseQueryCache
	configuration := bqJobConfiguration{
		Query: bqJobQueryConfiguration{
			Query:         sql,
			UseLegacySQL:  false,
			UseQueryCache: useCache,
			Priority:      strings.TrimSpace(req.Priority),
		},
		Labels: cloneStringMap(req.Labels),
	}
	if req.MaximumBytesBilled > 0 {
		configuration.Query.MaximumBytesBilled = strconv.FormatInt(req.MaximumBytesBilled, 10)
	}
	if req.JobTimeout > 0 {
		configuration.JobTimeoutMs = strconv.FormatInt(req.JobTimeout.Milliseconds(), 10)
	}

	request := bqJob{
		JobReference: bqJobReference{
			ProjectID: handle.ProjectID,
			JobID:     handle.JobID,
			Location:  handle.Location,
		},
		Configuration: configuration,
	}
	upURL := fmt.Sprintf("https://bigquery.googleapis.com/bigquery/v2/projects/%s/jobs", url.PathEscape(handle.ProjectID))
	var response bqJob
	// Intentionally single-attempt. A retry with a different or absent identity
	// can duplicate query cost; callers reconcile this exact handle instead.
	if err := h.doBQOnce(ctx, http.MethodPost, upURL, request, &response); err != nil {
		return QueryJobSnapshot{}, err
	}
	return snapshotFromBQJob(response, handle), nil
}

func (h *Handler) GetQueryJob(ctx context.Context, handle QueryJobHandle) (QueryJobSnapshot, error) {
	handle, err := normalizeQueryJobHandle(handle)
	if err != nil {
		return QueryJobSnapshot{}, err
	}
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/jobs/%s",
		url.PathEscape(handle.ProjectID), url.PathEscape(handle.JobID),
	)
	if handle.Location != "" {
		upURL += "?location=" + url.QueryEscape(handle.Location)
	}
	var response bqJob
	if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &response); err != nil {
		return QueryJobSnapshot{}, err
	}
	return snapshotFromBQJob(response, handle), nil
}

func (h *Handler) GetQueryResultPage(ctx context.Context, handle QueryJobHandle, pageToken string, maxResults int64) (QueryResultPage, error) {
	handle, err := normalizeQueryJobHandle(handle)
	if err != nil {
		return QueryResultPage{}, err
	}
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/queries/%s",
		url.PathEscape(handle.ProjectID), url.PathEscape(handle.JobID),
	)
	values := url.Values{}
	if handle.Location != "" {
		values.Set("location", handle.Location)
	}
	if token := strings.TrimSpace(pageToken); token != "" {
		values.Set("pageToken", token)
	}
	if maxResults > 0 {
		values.Set("maxResults", strconv.FormatInt(maxResults, 10))
	}
	values.Set("timeoutMs", "0")
	upURL += "?" + values.Encode()

	var response bqQueryResponse
	if err := h.doBQ(ctx, http.MethodGet, upURL, nil, &response); err != nil {
		return QueryResultPage{}, err
	}
	return QueryResultPage{
		Handle:              handle,
		JobComplete:         response.JobComplete,
		Rows:                queryResponseToRows(response),
		Schema:              bqSchemaToMaps(response.Schema.Fields),
		NextPageToken:       strings.TrimSpace(response.PageToken),
		TotalRows:           parseBQInt64(response.TotalRows),
		TotalBytesProcessed: parseBQInt64(response.TotalBytesProcessed),
		CacheHit:            response.CacheHit,
		Warnings:            exportBQErrors(response.Errors),
	}, nil
}

// MaterializeQueryResult follows BigQuery's native page tokens and never
// rewrites or re-executes SQL. MaxRows and MaxBytes are caller policy inputs;
// zero means no local limit. Fetching can be restarted safely from page one
// because callers commit the assembled artifact atomically.
func (h *Handler) MaterializeQueryResult(ctx context.Context, req MaterializeQueryRequest) (MaterializedQueryResult, error) {
	if _, err := normalizeQueryJobHandle(req.Handle); err != nil {
		return MaterializedQueryResult{}, err
	}
	maxPages := req.MaxPages
	if maxPages <= 0 {
		maxPages = 10_000
	}
	result := MaterializedQueryResult{}
	pageToken := ""
	seenTokens := map[string]struct{}{}
	for result.PagesRead < maxPages {
		pageSize := int64(1000)
		if req.MaxRows > 0 {
			remaining := req.MaxRows - int64(len(result.Rows))
			if remaining <= 0 {
				result.LocallyTruncated = true
				return result, nil
			}
			if remaining+1 < pageSize {
				pageSize = remaining + 1
			}
		}
		page, err := h.GetQueryResultPage(ctx, req.Handle, pageToken, pageSize)
		if err != nil {
			return result, err
		}
		if !page.JobComplete {
			return result, errors.New("BigQuery result requested before job completion")
		}
		result.PagesRead++
		if len(result.Schema) == 0 && len(page.Schema) > 0 {
			result.Schema = page.Schema
		}
		result.TotalRows = page.TotalRows
		result.TotalBytesProcessed = page.TotalBytesProcessed
		result.CacheHit = page.CacheHit
		for _, row := range page.Rows {
			encoded, _ := json.Marshal(row)
			rowBytes := int64(len(encoded))
			if req.MaxRows > 0 && int64(len(result.Rows)) >= req.MaxRows {
				result.LocallyTruncated = true
				return result, nil
			}
			if req.MaxBytes > 0 && result.BytesRead+rowBytes > req.MaxBytes {
				result.LocallyTruncated = true
				return result, nil
			}
			result.Rows = append(result.Rows, row)
			result.BytesRead += rowBytes
		}
		next := strings.TrimSpace(page.NextPageToken)
		if next == "" {
			return result, nil
		}
		if _, duplicate := seenTokens[next]; duplicate {
			return result, errors.New("BigQuery result page token cycle")
		}
		seenTokens[next] = struct{}{}
		pageToken = next
	}
	return result, errors.New("BigQuery result exceeded maximum page count")
}

func (h *Handler) RequestCancelQueryJob(ctx context.Context, handle QueryJobHandle) (QueryCancelReceipt, error) {
	handle, err := normalizeQueryJobHandle(handle)
	if err != nil {
		return QueryCancelReceipt{}, err
	}
	upURL := fmt.Sprintf(
		"https://bigquery.googleapis.com/bigquery/v2/projects/%s/jobs/%s/cancel",
		url.PathEscape(handle.ProjectID), url.PathEscape(handle.JobID),
	)
	if handle.Location != "" {
		upURL += "?location=" + url.QueryEscape(handle.Location)
	}
	var response struct {
		Job *bqJob `json:"job,omitempty"`
	}
	if err := h.doBQOnce(ctx, http.MethodPost, upURL, nil, &response); err != nil {
		return QueryCancelReceipt{}, err
	}
	receipt := QueryCancelReceipt{Handle: handle, Accepted: true}
	if response.Job != nil {
		snapshot := snapshotFromBQJob(*response.Job, handle)
		receipt.Job = &snapshot
	}
	return receipt, nil
}

func legacyBQSubmitResult(snapshot QueryJobSnapshot, materialized MaterializedQueryResult) *SubmitResult {
	return &SubmitResult{
		Completed:   true,
		Rows:        materialized.Rows,
		Schema:      materialized.Schema,
		JobID:       snapshot.Handle.JobID,
		JobLocation: snapshot.Handle.Location,
		ProjectID:   snapshot.Handle.ProjectID,
		Metadata: map[string]any{
			"provider":              "bigquery",
			"duration_ms":           int64(0),
			"total_bytes_processed": strconv.FormatInt(snapshot.Statistics.TotalBytesProcessed, 10),
			"total_bytes_billed":    strconv.FormatInt(snapshot.Statistics.TotalBytesBilled, 10),
			"total_slot_ms":         snapshot.Statistics.TotalSlotMs,
			"job_id":                snapshot.Handle.JobID,
			"job_location":          snapshot.Handle.Location,
			"cache_hit":             snapshot.Statistics.CacheHit,
			"state":                 "DONE",
			"terminal_status":       "completed",
		},
	}
}

func (h *Handler) runQueryLifecycle(ctx context.Context, projectID, sql string, onProgress QueryProgressFn) (queryResult, error) {
	started := time.Now()
	submitted, err := h.SubmitQuery(ctx, projectID, sql, defaultAdaptiveWaitFirst)
	if err != nil {
		return queryResult{}, err
	}
	if submitted == nil || strings.TrimSpace(submitted.JobID) == "" {
		return queryResult{}, errors.New("BigQuery submission failed: missing job_id")
	}
	if onProgress != nil {
		state := "RUNNING"
		if submitted.Completed {
			state = "DONE"
		}
		onProgress(string(queryProgressPhaseSubmitted), map[string]any{
			"provider":   "bigquery",
			"state":      state,
			"job_id":     submitted.JobID,
			"location":   submitted.JobLocation,
			"project_id": submitted.ProjectID,
			"url":        bigqueryConsoleURL(submitted.ProjectID, submitted.JobLocation, submitted.JobID),
		})
	}

	rows := submitted.Rows
	schema := submitted.Schema
	metadata := submitted.Metadata
	for !submitted.Completed {
		if err := sleepWithContext(ctx, time.Second); err != nil {
			return queryResult{}, err
		}
		done, polled, pollErr := h.PollJob(ctx, submitted.ProjectID, submitted.JobID, submitted.JobLocation)
		if pollErr != nil {
			return queryResult{}, pollErr
		}
		if onProgress != nil {
			state := "RUNNING"
			meta := map[string]any{
				"provider":   "bigquery",
				"state":      state,
				"job_id":     submitted.JobID,
				"location":   submitted.JobLocation,
				"project_id": submitted.ProjectID,
				"url":        bigqueryConsoleURL(submitted.ProjectID, submitted.JobLocation, submitted.JobID),
			}
			if polled != nil {
				if rawState := legacyMetadataString(polled.Metadata, "state"); rawState != "" {
					meta["state"] = rawState
				}
				if processed := legacyMetadataString(polled.Metadata, "total_bytes_processed"); processed != "" {
					meta["bytes_processed"] = processed
				}
				if slotMs, ok := polled.Metadata["total_slot_ms"]; ok {
					meta["total_slot_ms"] = slotMs
				}
			}
			onProgress(string(queryProgressPhasePolling), meta)
		}
		if !done {
			continue
		}
		if polled == nil {
			return queryResult{}, errors.New("BigQuery completed without a result snapshot")
		}
		if terminal := legacyMetadataString(polled.Metadata, "terminal_status"); terminal != "" && terminal != "completed" {
			message := legacyMetadataString(polled.Metadata, "error")
			if message == "" {
				message = "BigQuery query " + terminal
			}
			return queryResult{}, errors.New(message)
		}
		rows = polled.Rows
		schema = polled.Schema
		metadata = polled.Metadata
		submitted.Completed = true
	}

	if onProgress != nil {
		onProgress(string(queryProgressPhaseComplete), map[string]any{
			"provider":        "bigquery",
			"state":           "DONE",
			"job_id":          submitted.JobID,
			"location":        submitted.JobLocation,
			"project_id":      submitted.ProjectID,
			"url":             bigqueryConsoleURL(submitted.ProjectID, submitted.JobLocation, submitted.JobID),
			"bytes_processed": legacyMetadataString(metadata, "total_bytes_processed"),
		})
	}
	cacheHit, _ := metadata["cache_hit"].(bool)
	return queryResult{
		Rows:                rows,
		DurationMs:          time.Since(started).Milliseconds(),
		TotalBytesProcessed: legacyMetadataString(metadata, "total_bytes_processed"),
		JobID:               submitted.JobID,
		JobLocation:         submitted.JobLocation,
		Schema:              schema,
		CacheHit:            cacheHit,
	}, nil
}

func legacyMetadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func normalizeQueryJobHandle(handle QueryJobHandle) (QueryJobHandle, error) {
	handle.ProjectID = strings.TrimSpace(handle.ProjectID)
	handle.JobID = strings.TrimSpace(handle.JobID)
	handle.Location = strings.TrimSpace(handle.Location)
	if handle.ProjectID == "" || handle.JobID == "" {
		return QueryJobHandle{}, errors.New("project_id and job_id are required")
	}
	return handle, nil
}

type bqJobReference struct {
	ProjectID string `json:"projectId,omitempty"`
	JobID     string `json:"jobId,omitempty"`
	Location  string `json:"location,omitempty"`
}

type bqJobConfiguration struct {
	Query        bqJobQueryConfiguration `json:"query"`
	DryRun       bool                    `json:"dryRun,omitempty"`
	JobTimeoutMs string                  `json:"jobTimeoutMs,omitempty"`
	Labels       map[string]string       `json:"labels,omitempty"`
	Reservation  string                  `json:"reservation,omitempty"`
}

type bqJobQueryConfiguration struct {
	Query              string `json:"query"`
	Priority           string `json:"priority,omitempty"`
	UseQueryCache      *bool  `json:"useQueryCache,omitempty"`
	MaximumBytesBilled string `json:"maximumBytesBilled,omitempty"`
	UseLegacySQL       bool   `json:"useLegacySql"`
}

type bqJobStatus struct {
	State       string         `json:"state,omitempty"`
	ErrorResult *bqErrorProto  `json:"errorResult,omitempty"`
	Errors      []bqErrorProto `json:"errors,omitempty"`
}

type bqJob struct {
	JobReference  bqJobReference     `json:"jobReference"`
	Configuration bqJobConfiguration `json:"configuration"`
	Status        bqJobStatus        `json:"status"`
	Statistics    bqStatistics       `json:"statistics"`
}

type bqStatistics struct {
	CreationTime        string            `json:"creationTime,omitempty"`
	StartTime           string            `json:"startTime,omitempty"`
	EndTime             string            `json:"endTime,omitempty"`
	TotalBytesProcessed string            `json:"totalBytesProcessed,omitempty"`
	TotalSlotMs         string            `json:"totalSlotMs,omitempty"`
	ReservationID       string            `json:"reservation_id,omitempty"`
	QuotaDeferments     []string          `json:"quotaDeferments,omitempty"`
	Query               bqQueryStatistics `json:"query,omitempty"`
}

type bqQueryStatistics struct {
	EstimatedBytesProcessed     string                `json:"estimatedBytesProcessed,omitempty"`
	TotalBytesProcessed         string                `json:"totalBytesProcessed,omitempty"`
	TotalBytesProcessedAccuracy string                `json:"totalBytesProcessedAccuracy,omitempty"`
	TotalBytesBilled            string                `json:"totalBytesBilled,omitempty"`
	TotalSlotMs                 string                `json:"totalSlotMs,omitempty"`
	TotalPartitionsProcessed    string                `json:"totalPartitionsProcessed,omitempty"`
	CacheHit                    bool                  `json:"cacheHit,omitempty"`
	StatementType               string                `json:"statementType,omitempty"`
	Timeline                    []bqRawTimeline       `json:"timeline,omitempty"`
	QueryPlan                   []bqRawStage          `json:"queryPlan,omitempty"`
	ReferencedTables            []bqRawTableReference `json:"referencedTables,omitempty"`
	Schema                      struct {
		Fields []bqTableField `json:"fields,omitempty"`
	} `json:"schema,omitempty"`
}

type bqRawTableReference struct {
	ProjectID string `json:"projectId,omitempty"`
	DatasetID string `json:"datasetId,omitempty"`
	TableID   string `json:"tableId,omitempty"`
}

type bqRawTimeline struct {
	ElapsedMs              string `json:"elapsedMs,omitempty"`
	TotalSlotMs            string `json:"totalSlotMs,omitempty"`
	PendingUnits           string `json:"pendingUnits,omitempty"`
	CompletedUnits         string `json:"completedUnits,omitempty"`
	ActiveUnits            string `json:"activeUnits,omitempty"`
	EstimatedRunnableUnits string `json:"estimatedRunnableUnits,omitempty"`
}

type bqRawStage struct {
	ID                      string   `json:"id,omitempty"`
	Name                    string   `json:"name,omitempty"`
	Status                  string   `json:"status,omitempty"`
	StartMs                 string   `json:"startMs,omitempty"`
	EndMs                   string   `json:"endMs,omitempty"`
	InputStages             []string `json:"inputStages,omitempty"`
	ParallelInputs          string   `json:"parallelInputs,omitempty"`
	CompletedParallelInputs string   `json:"completedParallelInputs,omitempty"`
	RecordsRead             string   `json:"recordsRead,omitempty"`
	RecordsWritten          string   `json:"recordsWritten,omitempty"`
	ShuffleOutputBytes      string   `json:"shuffleOutputBytes,omitempty"`
	ShuffleBytesSpilled     string   `json:"shuffleOutputBytesSpilled,omitempty"`
	SlotMs                  string   `json:"slotMs,omitempty"`
}

func snapshotFromBQJob(job bqJob, fallback QueryJobHandle) QueryJobSnapshot {
	handle := QueryJobHandle{
		ProjectID: strings.TrimSpace(job.JobReference.ProjectID),
		JobID:     strings.TrimSpace(job.JobReference.JobID),
		Location:  strings.TrimSpace(job.JobReference.Location),
	}
	if handle.ProjectID == "" {
		handle.ProjectID = fallback.ProjectID
	}
	if handle.JobID == "" {
		handle.JobID = fallback.JobID
	}
	if handle.Location == "" {
		handle.Location = fallback.Location
	}
	state := strings.ToUpper(strings.TrimSpace(job.Status.State))
	snapshot := QueryJobSnapshot{
		Handle:     handle,
		State:      state,
		Labels:     cloneStringMap(job.Configuration.Labels),
		SQL:        strings.TrimSpace(job.Configuration.Query.Query),
		Warnings:   exportBQErrors(job.Status.Errors),
		Statistics: exportBQStatistics(job.Statistics),
		Schema:     bqSchemaToMaps(job.Statistics.Query.Schema.Fields),
	}
	if snapshot.SQL != "" {
		snapshot.SQLSHA256 = QuerySQLSHA256(snapshot.SQL)
	}
	if job.Status.ErrorResult != nil {
		exported := exportBQError(*job.Status.ErrorResult)
		snapshot.Error = &exported
	}
	if state == "DONE" {
		snapshot.Terminal = true
		if snapshot.Error == nil {
			snapshot.Succeeded = true
			snapshot.ResultAvailable = true
		} else {
			reason := strings.ToLower(strings.TrimSpace(snapshot.Error.Reason))
			snapshot.Cancelled = reason == "stopped" || strings.Contains(reason, "cancel")
		}
	}
	return snapshot
}

func exportBQStatistics(raw bqStatistics) QueryJobStatistics {
	totalProcessed := raw.Query.TotalBytesProcessed
	if totalProcessed == "" {
		totalProcessed = raw.TotalBytesProcessed
	}
	totalSlot := raw.Query.TotalSlotMs
	if totalSlot == "" {
		totalSlot = raw.TotalSlotMs
	}
	out := QueryJobStatistics{
		CreationTimeMs:         parseBQInt64(raw.CreationTime),
		StartTimeMs:            parseBQInt64(raw.StartTime),
		EndTimeMs:              parseBQInt64(raw.EndTime),
		TotalBytesProcessed:    parseBQInt64(totalProcessed),
		TotalBytesBilled:       parseBQInt64(raw.Query.TotalBytesBilled),
		EstimatedBytes:         parseBQInt64(raw.Query.EstimatedBytesProcessed),
		TotalSlotMs:            parseBQInt64(totalSlot),
		CacheHit:               raw.Query.CacheHit,
		StatementType:          strings.TrimSpace(raw.Query.StatementType),
		TotalPartitions:        parseBQInt64(raw.Query.TotalPartitionsProcessed),
		ReservationID:          strings.TrimSpace(raw.ReservationID),
		QuotaDeferments:        append([]string(nil), raw.QuotaDeferments...),
		BytesProcessedAccuracy: strings.TrimSpace(raw.Query.TotalBytesProcessedAccuracy),
	}
	for _, sample := range raw.Query.Timeline {
		out.Timeline = append(out.Timeline, QueryTimelineSample{
			ElapsedMs:              parseBQInt64(sample.ElapsedMs),
			TotalSlotMs:            parseBQInt64(sample.TotalSlotMs),
			PendingUnits:           parseBQInt64(sample.PendingUnits),
			CompletedUnits:         parseBQInt64(sample.CompletedUnits),
			ActiveUnits:            parseBQInt64(sample.ActiveUnits),
			EstimatedRunnableUnits: parseBQInt64(sample.EstimatedRunnableUnits),
		})
	}
	for _, stage := range raw.Query.QueryPlan {
		inputs := make([]int64, 0, len(stage.InputStages))
		for _, input := range stage.InputStages {
			inputs = append(inputs, parseBQInt64(input))
		}
		out.QueryPlan = append(out.QueryPlan, QueryStage{
			ID:                      parseBQInt64(stage.ID),
			Name:                    strings.TrimSpace(stage.Name),
			Status:                  strings.TrimSpace(stage.Status),
			StartMs:                 parseBQInt64(stage.StartMs),
			EndMs:                   parseBQInt64(stage.EndMs),
			InputStages:             inputs,
			ParallelInputs:          parseBQInt64(stage.ParallelInputs),
			CompletedParallelInputs: parseBQInt64(stage.CompletedParallelInputs),
			RecordsRead:             parseBQInt64(stage.RecordsRead),
			RecordsWritten:          parseBQInt64(stage.RecordsWritten),
			ShuffleOutputBytes:      parseBQInt64(stage.ShuffleOutputBytes),
			ShuffleBytesSpilled:     parseBQInt64(stage.ShuffleBytesSpilled),
			SlotMs:                  parseBQInt64(stage.SlotMs),
		})
	}
	for _, table := range raw.Query.ReferencedTables {
		out.ReferencedTables = append(out.ReferencedTables, QueryTableReference{
			ProjectID: strings.TrimSpace(table.ProjectID),
			DatasetID: strings.TrimSpace(table.DatasetID),
			TableID:   strings.TrimSpace(table.TableID),
		})
	}
	return out
}

func exportBQErrors(in []bqErrorProto) []BQError {
	if len(in) == 0 {
		return nil
	}
	out := make([]BQError, 0, len(in))
	for _, item := range in {
		out = append(out, exportBQError(item))
	}
	return out
}

func exportBQError(in bqErrorProto) BQError {
	return BQError{
		Message:  strings.TrimSpace(in.Message),
		Reason:   strings.TrimSpace(in.Reason),
		Location: strings.TrimSpace(in.Location),
	}
}

func parseBQInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return parsed
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (h *Handler) doBQOnce(ctx context.Context, method, upstreamURL string, body any, out any) error {
	var bodyBytes []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = encoded
	}
	token, err := h.accessToken(ctx)
	if err != nil {
		return err
	}
	var reader io.Reader
	if bodyBytes != nil {
		reader = bytes.NewReader(bodyBytes)
	}
	request, err := http.NewRequestWithContext(ctx, method, upstreamURL, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.httpc.Do(request)
	if err != nil {
		return &QueryAPIError{
			Message:   fmt.Sprintf("failed to reach BigQuery API: %v", err),
			Ambiguous: method == http.MethodPost,
			Cause:     err,
		}
	}
	defer response.Body.Close()
	responseBody, _ := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		message := strings.TrimSpace(string(responseBody))
		var parsed struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(responseBody, &parsed) == nil && strings.TrimSpace(parsed.Error.Message) != "" {
			message = strings.TrimSpace(parsed.Error.Message)
		}
		if message == "" {
			message = fmt.Sprintf("BigQuery API error (HTTP %d)", response.StatusCode)
		}
		if response.StatusCode == http.StatusUnauthorized {
			h.invalidateCachedToken()
		}
		return &QueryAPIError{
			StatusCode:      response.StatusCode,
			Message:         message,
			Ambiguous:       method == http.MethodPost && (response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500),
			RetryAfterDelay: parseRetryAfter(response.Header.Get("Retry-After")),
		}
	}
	if out == nil || len(bytes.TrimSpace(responseBody)) == 0 {
		return nil
	}
	return json.Unmarshal(responseBody, out)
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}
