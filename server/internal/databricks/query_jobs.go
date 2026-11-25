package databricks

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
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/sqlutil"
)

type QueryTag struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type StatementHandle struct {
	Host        string `json:"host"`
	WarehouseID string `json:"warehouse_id,omitempty"`
	StatementID string `json:"statement_id"`
}

type StatementSubmitRequest struct {
	SQL           string
	Host          string
	WarehouseID   string
	Catalog       string
	Schema        string
	WaitTimeout   time.Duration
	OnWaitTimeout string
	Disposition   string
	Format        string
	RowLimit      int64
	ByteLimit     int64
	QueryTags     []QueryTag
}

// MaxInlineStatementByteLimit is the Statement Execution API ceiling for
// INLINE results. Databricks rejects larger explicit byte_limit values
// instead of clamping them, so every inline caller must normalize at the
// provider boundary.
const MaxInlineStatementByteLimit int64 = 25 << 20

// NormalizeStatementByteLimit keeps explicit INLINE requests inside the
// provider's accepted range. An unset/non-positive inline limit is made
// explicit so Databricks truncates at a known boundary instead of aborting an
// oversized inline result. EXTERNAL_LINKS has a different contract and is
// deliberately left unchanged.
func NormalizeStatementByteLimit(disposition string, limit int64) int64 {
	if !strings.EqualFold(strings.TrimSpace(disposition), "INLINE") {
		return limit
	}
	if limit <= 0 || limit > MaxInlineStatementByteLimit {
		return MaxInlineStatementByteLimit
	}
	return limit
}

type StatementError struct {
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
	SQLState  string `json:"sql_state,omitempty"`
}

type StatementColumn struct {
	Name          string `json:"name"`
	TypeName      string `json:"type_name,omitempty"`
	TypeText      string `json:"type_text,omitempty"`
	Position      int32  `json:"position,omitempty"`
	TypePrecision int32  `json:"type_precision,omitempty"`
	TypeScale     int32  `json:"type_scale,omitempty"`
}

type StatementChunkInfo struct {
	ChunkIndex int32 `json:"chunk_index"`
	RowOffset  int64 `json:"row_offset"`
	RowCount   int64 `json:"row_count"`
	ByteCount  int64 `json:"byte_count"`
}

type StatementManifest struct {
	Format          string               `json:"format,omitempty"`
	Columns         []StatementColumn    `json:"columns,omitempty"`
	TotalChunkCount int32                `json:"total_chunk_count"`
	TotalRowCount   int64                `json:"total_row_count"`
	TotalByteCount  int64                `json:"total_byte_count"`
	Truncated       bool                 `json:"truncated"`
	Chunks          []StatementChunkInfo `json:"chunks,omitempty"`
}

type StatementExternalLink struct {
	ChunkIndex   int32  `json:"chunk_index"`
	RowOffset    int64  `json:"row_offset"`
	RowCount     int64  `json:"row_count"`
	ByteCount    int64  `json:"byte_count"`
	ExternalLink string `json:"-"`
	Expiration   string `json:"expiration,omitempty"`
}

type ResultChunk struct {
	ChunkIndex            int32                   `json:"chunk_index"`
	RowOffset             int64                   `json:"row_offset"`
	RowCount              int64                   `json:"row_count"`
	ByteCount             int64                   `json:"byte_count"`
	DataArray             [][]any                 `json:"data_array,omitempty"`
	ExternalLinks         []StatementExternalLink `json:"external_links,omitempty"`
	NextChunkIndex        *int32                  `json:"next_chunk_index,omitempty"`
	NextChunkInternalLink string                  `json:"next_chunk_internal_link,omitempty"`
}

type StatementSnapshot struct {
	Handle          StatementHandle   `json:"handle"`
	State           string            `json:"state"`
	Terminal        bool              `json:"terminal"`
	Succeeded       bool              `json:"succeeded"`
	Cancelled       bool              `json:"cancelled"`
	Closed          bool              `json:"closed"`
	ResultAvailable bool              `json:"result_available"`
	Error           *StatementError   `json:"error,omitempty"`
	Manifest        StatementManifest `json:"manifest"`
	FirstChunk      ResultChunk       `json:"first_chunk"`
}

type StatementCancelReceipt struct {
	Handle   StatementHandle `json:"handle"`
	Accepted bool            `json:"accepted"`
}

type StatementAPIError struct {
	StatusCode int
	ErrorCode  string
	SQLState   string
	Message    string
	Ambiguous  bool
	RetryAfter time.Duration
	Cause      error
}

func (e *StatementAPIError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *StatementAPIError) Unwrap() error { return e.Cause }

func StatementHTTPStatus(err error) int {
	var statementErr *StatementAPIError
	if errors.As(err, &statementErr) {
		return statementErr.StatusCode
	}
	var he httpError
	if errors.As(err, &he) {
		return he.status
	}
	return 0
}

func StatementSQLSHA256(sql string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sql)))
	return hex.EncodeToString(sum[:])
}

type statementRequest struct {
	Statement     string     `json:"statement"`
	WarehouseID   string     `json:"warehouse_id"`
	WaitTimeout   string     `json:"wait_timeout"`
	OnWaitTimeout string     `json:"on_wait_timeout,omitempty"`
	Disposition   string     `json:"disposition"`
	Format        string     `json:"format"`
	Catalog       string     `json:"catalog,omitempty"`
	Schema        string     `json:"schema,omitempty"`
	RowLimit      int64      `json:"row_limit,omitempty"`
	ByteLimit     int64      `json:"byte_limit,omitempty"`
	QueryTags     []QueryTag `json:"query_tags,omitempty"`
}

type statementStatus struct {
	State string          `json:"state"`
	Error *statementError `json:"error,omitempty"`
}

type statementError struct {
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
	SQLState  string `json:"sql_state,omitempty"`
}

type statementSchema struct {
	Columns []statementColumn `json:"columns,omitempty"`
}

type statementColumn struct {
	Name          string `json:"name"`
	TypeName      string `json:"type_name,omitempty"`
	TypeText      string `json:"type_text,omitempty"`
	Position      int32  `json:"position,omitempty"`
	TypePrecision int32  `json:"type_precision,omitempty"`
	TypeScale     int32  `json:"type_scale,omitempty"`
}

type statementChunkInfo struct {
	ChunkIndex int32 `json:"chunk_index"`
	RowOffset  int64 `json:"row_offset"`
	RowCount   int64 `json:"row_count"`
	ByteCount  int64 `json:"byte_count"`
}

type statementManifest struct {
	Format          string               `json:"format,omitempty"`
	Schema          statementSchema      `json:"schema"`
	TotalChunkCount int32                `json:"total_chunk_count,omitempty"`
	TotalRowCount   int64                `json:"total_row_count,omitempty"`
	TotalByteCount  int64                `json:"total_byte_count,omitempty"`
	Truncated       bool                 `json:"truncated,omitempty"`
	Chunks          []statementChunkInfo `json:"chunks,omitempty"`
}

type statementExternalLink struct {
	ChunkIndex   int32  `json:"chunk_index,omitempty"`
	RowOffset    int64  `json:"row_offset,omitempty"`
	RowCount     int64  `json:"row_count,omitempty"`
	ByteCount    int64  `json:"byte_count,omitempty"`
	ExternalLink string `json:"external_link,omitempty"`
	Expiration   string `json:"expiration,omitempty"`
}

type statementResult struct {
	ChunkIndex            int32                   `json:"chunk_index,omitempty"`
	RowOffset             int64                   `json:"row_offset,omitempty"`
	RowCount              int64                   `json:"row_count,omitempty"`
	ByteCount             int64                   `json:"byte_count,omitempty"`
	DataArray             [][]any                 `json:"data_array,omitempty"`
	ExternalLinks         []statementExternalLink `json:"external_links,omitempty"`
	NextChunkIndex        *int32                  `json:"next_chunk_index,omitempty"`
	NextChunkInternalLink string                  `json:"next_chunk_internal_link,omitempty"`
}

type statementResponse struct {
	StatementID string            `json:"statement_id"`
	Status      statementStatus   `json:"status"`
	Manifest    statementManifest `json:"manifest"`
	Result      statementResult   `json:"result"`
}

func (h *Handler) SubmitStatement(ctx context.Context, req StatementSubmitRequest) (StatementSnapshot, error) {
	req.Host = strings.TrimSpace(req.Host)
	req.WarehouseID = strings.TrimSpace(req.WarehouseID)
	req.SQL = strings.TrimSpace(req.SQL)
	if req.Host == "" {
		return StatementSnapshot{}, errors.New("missing databricks host")
	}
	if req.WarehouseID == "" {
		return StatementSnapshot{}, errors.New("missing databricks warehouse_id")
	}
	if req.SQL == "" {
		return StatementSnapshot{}, errors.New("missing sql")
	}
	disposition := strings.ToUpper(strings.TrimSpace(req.Disposition))
	if disposition == "" {
		disposition = "INLINE"
	}
	format := strings.ToUpper(strings.TrimSpace(req.Format))
	if format == "" {
		format = "JSON_ARRAY"
	}
	onWaitTimeout := strings.ToUpper(strings.TrimSpace(req.OnWaitTimeout))
	if onWaitTimeout == "" {
		onWaitTimeout = "CONTINUE"
	}
	payload := statementRequest{
		Statement:     req.SQL,
		WarehouseID:   req.WarehouseID,
		WaitTimeout:   databricksWaitTimeout(req.WaitTimeout),
		OnWaitTimeout: onWaitTimeout,
		Disposition:   disposition,
		Format:        format,
		Catalog:       strings.TrimSpace(req.Catalog),
		Schema:        strings.TrimSpace(req.Schema),
		RowLimit:      req.RowLimit,
		ByteLimit:     NormalizeStatementByteLimit(disposition, req.ByteLimit),
		QueryTags:     cleanQueryTags(req.QueryTags),
	}
	var response statementResponse
	if err := h.doStatementAPI(ctx, req.Host, http.MethodPost, "/api/2.0/sql/statements", payload, &response); err != nil {
		return StatementSnapshot{}, err
	}
	return snapshotFromStatementResponse(response, StatementHandle{Host: req.Host, WarehouseID: req.WarehouseID}), nil
}

func (h *Handler) GetStatement(ctx context.Context, handle StatementHandle) (StatementSnapshot, error) {
	handle, err := normalizeStatementHandle(handle)
	if err != nil {
		return StatementSnapshot{}, err
	}
	path := "/api/2.0/sql/statements/" + url.PathEscape(handle.StatementID)
	var response statementResponse
	if err := h.doStatementAPI(ctx, handle.Host, http.MethodGet, path, nil, &response); err != nil {
		return StatementSnapshot{}, err
	}
	return snapshotFromStatementResponse(response, handle), nil
}

func (h *Handler) GetStatementResultChunk(ctx context.Context, handle StatementHandle, chunkIndex int32) (ResultChunk, error) {
	handle, err := normalizeStatementHandle(handle)
	if err != nil {
		return ResultChunk{}, err
	}
	if chunkIndex < 0 {
		return ResultChunk{}, errors.New("chunk_index must be non-negative")
	}
	path := fmt.Sprintf("/api/2.0/sql/statements/%s/result/chunks/%d", url.PathEscape(handle.StatementID), chunkIndex)
	var response statementResult
	if err := h.doStatementAPI(ctx, handle.Host, http.MethodGet, path, nil, &response); err != nil {
		return ResultChunk{}, err
	}
	return exportResultChunk(response), nil
}

func (h *Handler) RequestCancelStatement(ctx context.Context, handle StatementHandle) (StatementCancelReceipt, error) {
	handle, err := normalizeStatementHandle(handle)
	if err != nil {
		return StatementCancelReceipt{}, err
	}
	path := "/api/2.0/sql/statements/" + url.PathEscape(handle.StatementID) + "/cancel"
	if err := h.doStatementAPI(ctx, handle.Host, http.MethodPost, path, nil, nil); err != nil {
		return StatementCancelReceipt{}, err
	}
	return StatementCancelReceipt{Handle: handle, Accepted: true}, nil
}

type ExplainQueryRequest struct {
	StatementSubmitRequest
	Mode           string
	PollInterval   time.Duration
	CleanupTimeout time.Duration
	// OnObservation is runtime-only. Returning an error means the caller did
	// not durably persist this accepted statement observation; ExplainQuery
	// aborts immediately and cleans up any still-active statement.
	OnObservation func(ExplainObservation) error `json:"-"`
}

type ExplainObservation struct {
	Phase     string
	Statement StatementSnapshot
}

type ExplainQueryResult struct {
	Statement StatementSnapshot `json:"statement"`
	PlanRows  []map[string]any  `json:"plan_rows,omitempty"`
	PlanText  string            `json:"plan_text,omitempty"`
}

const (
	defaultExplainPollInterval   = 500 * time.Millisecond
	maximumExplainWaitTimeout    = 50 * time.Second
	maximumExplainCleanupTimeout = 5 * time.Second
)

// ExplainQuery submits a bounded, independently identifiable Statement
// Execution request and follows it to a terminal state. EXPLAIN uses
// on_wait_timeout=CANCEL so a process crash cannot leave the profile running
// beyond Databricks' maximum 50-second server wait window. If local polling
// stops after a handle is known, the handler also requests best-effort
// cancellation under a fresh, bounded cleanup context.
func (h *Handler) ExplainQuery(ctx context.Context, req ExplainQueryRequest) (ExplainQueryResult, error) {
	sql := strings.TrimSpace(req.SQL)
	if sql == "" {
		return ExplainQueryResult{}, errors.New("missing sql")
	}
	if !sqlutil.IsReadOnlySQL(sql) {
		return ExplainQueryResult{}, errors.New("only read-only SELECT/WITH queries can be explained")
	}
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "FORMATTED"
	}
	switch mode {
	case "FORMATTED", "EXTENDED", "CODEGEN", "COST":
	default:
		return ExplainQueryResult{}, fmt.Errorf("unsupported EXPLAIN mode %q", mode)
	}
	req.SQL = "EXPLAIN " + mode + "\n" + sql
	req.WaitTimeout = databricksExplainWaitTimeout(ctx)
	req.OnWaitTimeout = "CANCEL"
	snapshot, err := h.SubmitStatement(ctx, req.StatementSubmitRequest)
	if err != nil {
		return ExplainQueryResult{}, err
	}
	if strings.TrimSpace(snapshot.Handle.StatementID) == "" {
		return ExplainQueryResult{Statement: snapshot}, errors.New("Databricks accepted EXPLAIN without a statement_id")
	}
	phase := "explain_" + strings.ToLower(mode)
	if err := publishDatabricksExplainObservation(req, phase, snapshot); err != nil {
		h.cancelActiveExplain(req, snapshot)
		return ExplainQueryResult{Statement: snapshot}, fmt.Errorf("publish Databricks EXPLAIN observation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		h.cancelActiveExplain(req, snapshot)
		return ExplainQueryResult{Statement: snapshot}, err
	}
	pollInterval := req.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultExplainPollInterval
	}
	for !snapshot.Terminal {
		if err := waitForDatabricksExplainPoll(ctx, pollInterval); err != nil {
			h.cancelActiveExplain(req, snapshot)
			return ExplainQueryResult{Statement: snapshot}, err
		}
		next, pollErr := h.GetStatement(ctx, snapshot.Handle)
		if pollErr != nil {
			h.cancelActiveExplain(req, snapshot)
			return ExplainQueryResult{Statement: snapshot}, pollErr
		}
		snapshot = next
		if err := publishDatabricksExplainObservation(req, phase, snapshot); err != nil {
			h.cancelActiveExplain(req, snapshot)
			return ExplainQueryResult{Statement: snapshot}, fmt.Errorf("publish Databricks EXPLAIN observation: %w", err)
		}
		if err := ctx.Err(); err != nil {
			h.cancelActiveExplain(req, snapshot)
			return ExplainQueryResult{Statement: snapshot}, err
		}
	}

	result := ExplainQueryResult{Statement: snapshot}
	if !snapshot.Succeeded || !snapshot.ResultAvailable {
		return result, nil
	}
	result.PlanRows = rowsFromChunk(snapshot.Manifest.Columns, snapshot.FirstChunk)
	parts := make([]string, 0, len(result.PlanRows))
	for _, row := range result.PlanRows {
		for _, value := range row {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				parts = append(parts, text)
				break
			}
		}
	}
	result.PlanText = strings.Join(parts, "\n")
	return result, nil
}

func publishDatabricksExplainObservation(req ExplainQueryRequest, phase string, snapshot StatementSnapshot) error {
	if req.OnObservation == nil {
		return nil
	}
	return req.OnObservation(ExplainObservation{Phase: phase, Statement: snapshot})
}

func databricksExplainWaitTimeout(ctx context.Context) time.Duration {
	wait := maximumExplainWaitTimeout
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining > 0 && remaining < wait {
			wait = remaining
		}
	}
	return wait
}

func waitForDatabricksExplainPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (h *Handler) cancelActiveExplain(req ExplainQueryRequest, snapshot StatementSnapshot) {
	if snapshot.Terminal || strings.TrimSpace(snapshot.Handle.StatementID) == "" {
		return
	}
	timeout := boundedDatabricksExplainCleanupTimeout(req.CleanupTimeout)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = h.RequestCancelStatement(cleanupCtx, snapshot.Handle)
}

func boundedDatabricksExplainCleanupTimeout(requested time.Duration) time.Duration {
	if requested <= 0 || requested > maximumExplainCleanupTimeout {
		return maximumExplainCleanupTimeout
	}
	return requested
}

type QueryHistoryFilter struct {
	Statuses       []string
	UserIDs        []int64
	WarehouseIDs   []string
	StatementIDs   []string
	StartTimeMs    int64
	EndTimeMs      int64
	IncludeMetrics bool
	MaxResults     int
	PageToken      string
}

type QueryTagMap map[string]string

func (m *QueryTagMap) UnmarshalJSON(data []byte) error {
	var object map[string]string
	if json.Unmarshal(data, &object) == nil {
		*m = object
		return nil
	}
	var list []QueryTag
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	out := make(map[string]string, len(list))
	for _, tag := range list {
		out[strings.TrimSpace(tag.Key)] = strings.TrimSpace(tag.Value)
	}
	*m = out
	return nil
}

type QueryHistoryMetrics struct {
	CompilationTimeMs                 int64           `json:"compilation_time_ms,omitempty"`
	ExecutionTimeMs                   int64           `json:"execution_time_ms,omitempty"`
	TotalTimeMs                       int64           `json:"total_time_ms,omitempty"`
	TaskTotalTimeMs                   int64           `json:"task_total_time_ms,omitempty"`
	ProjectedRemainingTaskTotalTimeMs int64           `json:"projected_remaining_task_total_time_ms,omitempty"`
	ProjectedRemainingWallclockTimeMs int64           `json:"projected_remaining_wallclock_time_ms,omitempty"`
	RemainingTaskCount                int64           `json:"remaining_task_count,omitempty"`
	WorkToBeDone                      int64           `json:"work_to_be_done,omitempty"`
	RowsReadCount                     int64           `json:"rows_read_count,omitempty"`
	RowsProducedCount                 int64           `json:"rows_produced_count,omitempty"`
	ReadBytes                         int64           `json:"read_bytes,omitempty"`
	ReadCacheBytes                    int64           `json:"read_cache_bytes,omitempty"`
	ReadFilesBytes                    int64           `json:"read_files_bytes,omitempty"`
	ReadRemoteBytes                   int64           `json:"read_remote_bytes,omitempty"`
	ReadFilesCount                    int64           `json:"read_files_count,omitempty"`
	ReadPartitionsCount               int64           `json:"read_partitions_count,omitempty"`
	PrunedBytes                       int64           `json:"pruned_bytes,omitempty"`
	PrunedFilesCount                  int64           `json:"pruned_files_count,omitempty"`
	SpillToDiskBytes                  int64           `json:"spill_to_disk_bytes,omitempty"`
	RunnableTasks                     int64           `json:"runnable_tasks,omitempty"`
	ResultFromCache                   bool            `json:"result_from_cache,omitempty"`
	ProvisioningQueueStartTimestamp   int64           `json:"provisioning_queue_start_timestamp,omitempty"`
	OverloadingQueueStartTimestamp    int64           `json:"overloading_queue_start_timestamp,omitempty"`
	TaskTimeOverTimeRange             json.RawMessage `json:"task_time_over_time_range,omitempty"`
}

type QueryHistoryEntry struct {
	QueryID            string              `json:"query_id"`
	QueryText          string              `json:"query_text"`
	Status             string              `json:"status"`
	WarehouseID        string              `json:"warehouse_id"`
	QueryStartTimeMs   int64               `json:"query_start_time_ms"`
	QueryEndTimeMs     int64               `json:"query_end_time_ms"`
	ExecutionEndTimeMs int64               `json:"execution_end_time_ms"`
	IsFinal            bool                `json:"is_final"`
	QueryTags          QueryTagMap         `json:"query_tags,omitempty"`
	LookupKey          string              `json:"lookup_key,omitempty"`
	RowsProduced       int64               `json:"rows_produced,omitempty"`
	UserID             int64               `json:"user_id,omitempty"`
	UserName           string              `json:"user_name,omitempty"`
	ClientApplication  string              `json:"client_application,omitempty"`
	Metrics            QueryHistoryMetrics `json:"metrics,omitempty"`
}

type QueryHistoryPage struct {
	Entries       []QueryHistoryEntry `json:"entries"`
	HasNextPage   bool                `json:"has_next_page"`
	NextPageToken string              `json:"next_page_token,omitempty"`
}

type queryHistoryRequest struct {
	FilterBy       queryHistoryFilterBy `json:"filter_by,omitempty"`
	IncludeMetrics bool                 `json:"include_metrics,omitempty"`
	MaxResults     int                  `json:"max_results,omitempty"`
	PageToken      string               `json:"page_token,omitempty"`
}

type queryHistoryFilterBy struct {
	Statuses     []string               `json:"statuses,omitempty"`
	UserIDs      []int64                `json:"user_ids,omitempty"`
	WarehouseIDs []string               `json:"warehouse_ids,omitempty"`
	StatementIDs []string               `json:"statement_ids,omitempty"`
	StartRange   *queryHistoryTimeRange `json:"query_start_time_range,omitempty"`
}

type queryHistoryTimeRange struct {
	StartTimeMs int64 `json:"start_time_ms,omitempty"`
	EndTimeMs   int64 `json:"end_time_ms,omitempty"`
}

func (h *Handler) ListQueryHistory(ctx context.Context, host string, filter QueryHistoryFilter) (QueryHistoryPage, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return QueryHistoryPage{}, errors.New("missing databricks host")
	}
	request := queryHistoryRequest{
		FilterBy: queryHistoryFilterBy{
			Statuses:     cleanStrings(filter.Statuses),
			UserIDs:      append([]int64(nil), filter.UserIDs...),
			WarehouseIDs: cleanStrings(filter.WarehouseIDs),
			StatementIDs: cleanStrings(filter.StatementIDs),
		},
		IncludeMetrics: filter.IncludeMetrics,
		MaxResults:     filter.MaxResults,
		PageToken:      strings.TrimSpace(filter.PageToken),
	}
	if filter.StartTimeMs > 0 || filter.EndTimeMs > 0 {
		request.FilterBy.StartRange = &queryHistoryTimeRange{StartTimeMs: filter.StartTimeMs, EndTimeMs: filter.EndTimeMs}
	}
	var response struct {
		Entries       []QueryHistoryEntry `json:"res"`
		HasNextPage   bool                `json:"has_next_page"`
		NextPageToken string              `json:"next_page_token"`
	}
	if err := h.doStatementAPI(ctx, host, http.MethodGet, "/api/2.0/sql/history/queries", request, &response); err != nil {
		return QueryHistoryPage{}, err
	}
	return QueryHistoryPage{Entries: response.Entries, HasNextPage: response.HasNextPage, NextPageToken: strings.TrimSpace(response.NextPageToken)}, nil
}

type StatementReconcileRequest struct {
	Host               string
	WarehouseID        string
	SubmittedSQLSHA256 string
	RequiredTags       map[string]string
	StartTimeMs        int64
	EndTimeMs          int64
	MaxPages           int
}

type StatementCandidate struct {
	StatementID string            `json:"statement_id"`
	SQLSHA256   string            `json:"sql_sha256"`
	Query       QueryHistoryEntry `json:"query"`
}

// FindStatementCandidates deliberately returns every exact match. Choosing one
// when multiple statements match would conceal duplicate cost and is left to
// the durable supervisor's explicit ambiguity state.
func (h *Handler) FindStatementCandidates(ctx context.Context, req StatementReconcileRequest) ([]StatementCandidate, error) {
	maxPages := req.MaxPages
	if maxPages <= 0 {
		maxPages = 1
	}
	filter := QueryHistoryFilter{
		WarehouseIDs:   []string{strings.TrimSpace(req.WarehouseID)},
		StartTimeMs:    req.StartTimeMs,
		EndTimeMs:      req.EndTimeMs,
		IncludeMetrics: true,
		MaxResults:     100,
	}
	wantedHash := strings.ToLower(strings.TrimSpace(req.SubmittedSQLSHA256))
	wantedTags := cloneTags(req.RequiredTags)
	var candidates []StatementCandidate
	for pageNumber := 0; pageNumber < maxPages; pageNumber++ {
		page, err := h.ListQueryHistory(ctx, req.Host, filter)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Entries {
			if warehouseID := strings.TrimSpace(req.WarehouseID); warehouseID != "" && strings.TrimSpace(entry.WarehouseID) != warehouseID {
				continue
			}
			if req.StartTimeMs > 0 && entry.QueryStartTimeMs < req.StartTimeMs {
				continue
			}
			if req.EndTimeMs > 0 && entry.QueryStartTimeMs > req.EndTimeMs {
				continue
			}
			entryHash := StatementSQLSHA256(entry.QueryText)
			if wantedHash != "" && !strings.EqualFold(entryHash, wantedHash) {
				continue
			}
			if !queryTagsContain(entry.QueryTags, wantedTags) {
				continue
			}
			if statementID := strings.TrimSpace(entry.QueryID); statementID != "" {
				candidates = append(candidates, StatementCandidate{StatementID: statementID, SQLSHA256: entryHash, Query: entry})
			}
		}
		if !page.HasNextPage || strings.TrimSpace(page.NextPageToken) == "" {
			break
		}
		filter.PageToken = page.NextPageToken
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Query.QueryStartTimeMs == candidates[j].Query.QueryStartTimeMs {
			return candidates[i].StatementID < candidates[j].StatementID
		}
		return candidates[i].Query.QueryStartTimeMs < candidates[j].Query.QueryStartTimeMs
	})
	return candidates, nil
}

type StatementProfile struct {
	Statement StatementSnapshot  `json:"statement"`
	History   *QueryHistoryEntry `json:"history,omitempty"`
}

func (h *Handler) GetStatementProfile(ctx context.Context, handle StatementHandle) (StatementProfile, error) {
	snapshot, err := h.GetStatement(ctx, handle)
	if err != nil {
		return StatementProfile{}, err
	}
	page, err := h.ListQueryHistory(ctx, handle.Host, QueryHistoryFilter{
		WarehouseIDs:   cleanStrings([]string{handle.WarehouseID}),
		StatementIDs:   []string{handle.StatementID},
		IncludeMetrics: true,
		MaxResults:     10,
	})
	if err != nil {
		return StatementProfile{Statement: snapshot}, err
	}
	profile := StatementProfile{Statement: snapshot}
	for i := range page.Entries {
		if strings.TrimSpace(page.Entries[i].QueryID) == handle.StatementID {
			entry := page.Entries[i]
			profile.History = &entry
			break
		}
	}
	return profile, nil
}

type MaterializeStatementRequest struct {
	Handle    StatementHandle
	Snapshot  *StatementSnapshot
	MaxRows   int64
	MaxBytes  int64
	MaxChunks int
}

type MaterializedStatementResult struct {
	Rows              []map[string]any `json:"rows,omitempty"`
	Schema            []map[string]any `json:"schema,omitempty"`
	BytesRead         int64            `json:"bytes_read"`
	ChunksRead        int              `json:"chunks_read"`
	ProviderTruncated bool             `json:"provider_truncated"`
	LocallyTruncated  bool             `json:"locally_truncated"`
	ResultAvailable   bool             `json:"result_available"`
	NextChunkIndex    *int32           `json:"next_chunk_index,omitempty"`
}

// MaterializeStatementResult follows native result chunks. It never modifies
// or re-executes SQL. MaxRows/MaxBytes are caller policy inputs; zero means no
// local limit. MaxChunks defaults to a safety bound when omitted.
func (h *Handler) MaterializeStatementResult(ctx context.Context, req MaterializeStatementRequest) (MaterializedStatementResult, error) {
	var snapshot StatementSnapshot
	if req.Snapshot != nil {
		snapshot = *req.Snapshot
	} else {
		var err error
		snapshot, err = h.GetStatement(ctx, req.Handle)
		if err != nil {
			return MaterializedStatementResult{}, err
		}
	}
	if !snapshot.Succeeded || !snapshot.ResultAvailable {
		return MaterializedStatementResult{ProviderTruncated: snapshot.Manifest.Truncated, ResultAvailable: false}, nil
	}
	maxChunks := req.MaxChunks
	if maxChunks <= 0 {
		maxChunks = 10_000
	}
	result := MaterializedStatementResult{
		Schema:            statementColumnsToMaps(snapshot.Manifest.Columns),
		ProviderTruncated: snapshot.Manifest.Truncated,
		ResultAvailable:   true,
	}
	chunk := snapshot.FirstChunk
	if len(chunk.DataArray) == 0 && len(chunk.ExternalLinks) == 0 && snapshot.Manifest.TotalRowCount > 0 {
		firstIndex := int32(0)
		if len(snapshot.Manifest.Chunks) > 0 {
			firstIndex = snapshot.Manifest.Chunks[0].ChunkIndex
		}
		var err error
		chunk, err = h.GetStatementResultChunk(ctx, snapshot.Handle, firstIndex)
		if err != nil {
			return MaterializedStatementResult{}, err
		}
	}
	seen := map[int32]struct{}{}
	for {
		if result.ChunksRead >= maxChunks {
			return MaterializedStatementResult{}, errors.New("Databricks result exceeded maximum chunk count")
		}
		if _, duplicate := seen[chunk.ChunkIndex]; duplicate {
			return MaterializedStatementResult{}, fmt.Errorf("Databricks result chunk cycle at index %d", chunk.ChunkIndex)
		}
		seen[chunk.ChunkIndex] = struct{}{}
		if chunk.RowOffset != int64(len(result.Rows)) {
			return MaterializedStatementResult{}, fmt.Errorf("Databricks result chunk %d has row offset %d, want %d", chunk.ChunkIndex, chunk.RowOffset, len(result.Rows))
		}
		result.ChunksRead++
		for _, row := range rowsFromChunk(snapshot.Manifest.Columns, chunk) {
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
		next, hasNext, nextErr := nextStatementChunkIndex(chunk, snapshot.Manifest)
		if nextErr != nil {
			return MaterializedStatementResult{}, nextErr
		}
		if !hasNext {
			return result, nil
		}
		result.NextChunkIndex = &next
		var err error
		chunk, err = h.GetStatementResultChunk(ctx, snapshot.Handle, next)
		if err != nil {
			return result, err
		}
		result.NextChunkIndex = nil
	}
}

func nextStatementChunkIndex(chunk ResultChunk, manifest StatementManifest) (int32, bool, error) {
	if chunk.NextChunkIndex != nil {
		return *chunk.NextChunkIndex, true, nil
	}
	if link := strings.TrimSpace(chunk.NextChunkInternalLink); link != "" {
		parsed, err := url.Parse(link)
		if err != nil {
			return 0, false, fmt.Errorf("invalid Databricks next chunk link: %w", err)
		}
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) == 0 {
			return 0, false, errors.New("invalid Databricks next chunk link")
		}
		index, err := strconv.ParseInt(segments[len(segments)-1], 10, 32)
		if err != nil || index < 0 {
			return 0, false, errors.New("invalid Databricks next chunk index")
		}
		return int32(index), true, nil
	}
	for index, item := range manifest.Chunks {
		if item.ChunkIndex == chunk.ChunkIndex && index+1 < len(manifest.Chunks) {
			return manifest.Chunks[index+1].ChunkIndex, true, nil
		}
	}
	return 0, false, nil
}

func snapshotFromStatementResponse(response statementResponse, fallback StatementHandle) StatementSnapshot {
	handle := fallback
	if id := strings.TrimSpace(response.StatementID); id != "" {
		handle.StatementID = id
	}
	state := strings.ToUpper(strings.TrimSpace(response.Status.State))
	snapshot := StatementSnapshot{
		Handle:     handle,
		State:      state,
		Manifest:   exportStatementManifest(response.Manifest),
		FirstChunk: exportResultChunk(response.Result),
	}
	if response.Status.Error != nil {
		snapshot.Error = &StatementError{
			ErrorCode: strings.TrimSpace(response.Status.Error.ErrorCode),
			Message:   strings.TrimSpace(response.Status.Error.Message),
			SQLState:  strings.TrimSpace(response.Status.Error.SQLState),
		}
	}
	switch state {
	case "SUCCEEDED":
		snapshot.Terminal = true
		snapshot.Succeeded = true
		snapshot.ResultAvailable = true
	case "FAILED":
		snapshot.Terminal = true
	case "CANCELED", "CANCELLED":
		snapshot.Terminal = true
		snapshot.Cancelled = true
	case "CLOSED":
		snapshot.Terminal = true
		snapshot.Succeeded = true
		snapshot.Closed = true
		snapshot.ResultAvailable = false
	}
	return snapshot
}

func exportStatementManifest(raw statementManifest) StatementManifest {
	out := StatementManifest{
		Format:          strings.TrimSpace(raw.Format),
		TotalChunkCount: raw.TotalChunkCount,
		TotalRowCount:   raw.TotalRowCount,
		TotalByteCount:  raw.TotalByteCount,
		Truncated:       raw.Truncated,
	}
	for _, column := range raw.Schema.Columns {
		out.Columns = append(out.Columns, StatementColumn{
			Name:          strings.TrimSpace(column.Name),
			TypeName:      strings.TrimSpace(column.TypeName),
			TypeText:      strings.TrimSpace(column.TypeText),
			Position:      column.Position,
			TypePrecision: column.TypePrecision,
			TypeScale:     column.TypeScale,
		})
	}
	for _, chunk := range raw.Chunks {
		out.Chunks = append(out.Chunks, StatementChunkInfo(chunk))
	}
	return out
}

func exportResultChunk(raw statementResult) ResultChunk {
	out := ResultChunk{
		ChunkIndex:            raw.ChunkIndex,
		RowOffset:             raw.RowOffset,
		RowCount:              raw.RowCount,
		ByteCount:             raw.ByteCount,
		DataArray:             raw.DataArray,
		NextChunkIndex:        raw.NextChunkIndex,
		NextChunkInternalLink: strings.TrimSpace(raw.NextChunkInternalLink),
	}
	for _, link := range raw.ExternalLinks {
		out.ExternalLinks = append(out.ExternalLinks, StatementExternalLink{
			ChunkIndex:   link.ChunkIndex,
			RowOffset:    link.RowOffset,
			RowCount:     link.RowCount,
			ByteCount:    link.ByteCount,
			ExternalLink: link.ExternalLink,
			Expiration:   strings.TrimSpace(link.Expiration),
		})
	}
	return out
}

func rowsFromChunk(columns []StatementColumn, chunk ResultChunk) []map[string]any {
	names := make([]string, 0, len(columns))
	for index, column := range columns {
		name := strings.TrimSpace(column.Name)
		if name == "" {
			name = fmt.Sprintf("col_%d", index)
		}
		names = append(names, name)
	}
	rows := make([]map[string]any, 0, len(chunk.DataArray))
	for _, values := range chunk.DataArray {
		row := make(map[string]any, len(values))
		for index, value := range values {
			name := fmt.Sprintf("col_%d", index)
			if index < len(names) {
				name = names[index]
			}
			row[name] = value
		}
		rows = append(rows, row)
	}
	return rows
}

func statementColumnsToMaps(columns []StatementColumn) []map[string]any {
	out := make([]map[string]any, 0, len(columns))
	for _, column := range columns {
		if strings.TrimSpace(column.Name) == "" {
			continue
		}
		item := map[string]any{"name": column.Name}
		if column.TypeName != "" {
			item["type"] = column.TypeName
		} else if column.TypeText != "" {
			item["type"] = column.TypeText
		}
		out = append(out, item)
	}
	return out
}

func normalizeStatementHandle(handle StatementHandle) (StatementHandle, error) {
	handle.Host = strings.TrimRight(strings.TrimSpace(handle.Host), "/")
	handle.WarehouseID = strings.TrimSpace(handle.WarehouseID)
	handle.StatementID = strings.TrimSpace(handle.StatementID)
	if handle.Host == "" || handle.StatementID == "" {
		return StatementHandle{}, errors.New("host and statement_id are required")
	}
	return handle, nil
}

func databricksWaitTimeout(wait time.Duration) string {
	if wait <= 0 {
		return "0s"
	}
	seconds := int64((wait + time.Second - 1) / time.Second)
	if seconds < 5 {
		seconds = 5
	}
	if seconds > 50 {
		seconds = 50
	}
	return strconv.FormatInt(seconds, 10) + "s"
}

func cleanQueryTags(tags []QueryTag) []QueryTag {
	seen := map[string]struct{}{}
	out := make([]QueryTag, 0, len(tags))
	for _, tag := range tags {
		tag.Key = strings.TrimSpace(tag.Key)
		tag.Value = strings.TrimSpace(tag.Value)
		if tag.Key == "" {
			continue
		}
		if _, duplicate := seen[tag.Key]; duplicate {
			continue
		}
		seen[tag.Key] = struct{}{}
		out = append(out, tag)
	}
	return out
}

func cleanStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func cloneTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	out := make(map[string]string, len(tags))
	for key, value := range tags {
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

func queryTagsContain(got QueryTagMap, required map[string]string) bool {
	for key, wanted := range required {
		if gotValue, ok := got[key]; !ok || gotValue != wanted {
			return false
		}
	}
	return true
}

func (h *Handler) doStatementAPI(ctx context.Context, host, method, path string, body any, out any) error {
	host = strings.TrimRight(strings.TrimSpace(host), "/")
	if host == "" {
		return &StatementAPIError{StatusCode: http.StatusBadRequest, Message: "Databricks host is not configured"}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var bodyBytes []byte
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		bodyBytes = encoded
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := h.accessToken(ctx, host)
		if err != nil {
			return err
		}
		var reader io.Reader
		if bodyBytes != nil {
			reader = bytes.NewReader(bodyBytes)
		}
		request, err := http.NewRequestWithContext(ctx, method, host+path, reader)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := h.httpc.Do(request)
		if err != nil {
			return statementTransportError(ctx, method, "failed to reach Databricks API", err)
		}
		responseBody, readErr := readBoundedResponse(response.Body, 32<<20)
		_ = response.Body.Close()
		if readErr != nil {
			return statementTransportError(ctx, method, "failed to read Databricks API response", readErr)
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			var envelope struct {
				ErrorCode string `json:"error_code"`
				Message   string `json:"message"`
				SQLState  string `json:"sql_state"`
			}
			_ = json.Unmarshal(responseBody, &envelope)
			if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
				h.invalidateCachedToken()
				continue
			}
			message := strings.TrimSpace(envelope.Message)
			if message == "" {
				message = databricksErrorMessage(response.StatusCode, responseBody)
			}
			return &StatementAPIError{
				StatusCode: response.StatusCode,
				ErrorCode:  strings.TrimSpace(envelope.ErrorCode),
				SQLState:   strings.TrimSpace(envelope.SQLState),
				Message:    message,
				Ambiguous:  method == http.MethodPost && response.StatusCode >= 500,
				RetryAfter: parseStatementRetryAfter(response.Header.Get("Retry-After")),
			}
		}
		if out == nil {
			return nil
		}
		trimmed := bytes.TrimSpace(responseBody)
		if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
			return &StatementAPIError{
				StatusCode: http.StatusBadGateway,
				Message:    "Databricks API returned an empty response",
				Ambiguous:  method == http.MethodPost,
				Cause:      errors.New("empty Databricks API response body"),
			}
		}
		if decodeErr := json.Unmarshal(responseBody, out); decodeErr != nil {
			return &StatementAPIError{
				StatusCode: http.StatusBadGateway,
				Message:    "Databricks API returned malformed JSON",
				Ambiguous:  method == http.MethodPost,
				Cause:      decodeErr,
			}
		}
		return nil
	}
	return errors.New("Databricks request failed")
}

func statementTransportError(ctx context.Context, method, message string, cause error) *StatementAPIError {
	status := http.StatusBadGateway
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
		message = "Databricks API request timed out"
	}
	return &StatementAPIError{
		StatusCode: status,
		Message:    message,
		Ambiguous:  method == http.MethodPost,
		Cause:      cause,
	}
}

func readBoundedResponse(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("Databricks response exceeded %d bytes", limit)
	}
	return data, nil
}

func parseStatementRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
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
