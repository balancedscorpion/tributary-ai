package snowflake

import (
	"bytes"
	"context"
	"crypto/sha256"
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

const (
	// DefaultAsyncStatementTimeout is a provider-side safety limit, not an HTTP
	// wait limit. Durable callers should normally supply their configured hard
	// execution budget explicitly.
	DefaultAsyncStatementTimeout = 6 * time.Hour
	maxAsyncStatementTimeout     = 7 * 24 * time.Hour
)

const (
	StatementStateSubmitted = "submitted"
	StatementStateQueued    = "queued"
	StatementStateRunning   = "running"
	StatementStateBlocked   = "blocked"
	StatementStateCompleted = "completed"
	StatementStateFailed    = "failed"
	StatementStateCancelled = "cancelled"
)

// AsyncQueryRequest contains only durable, non-secret query identity and
// scope. RequestID must be stable across an ambiguous-submit replay.
type AsyncQueryRequest struct {
	SQL                     string
	RequestID               string
	QueryTag                string
	AccountIdentifier       string
	Warehouse               string
	Database                string
	Schema                  string
	Role                    string
	User                    string
	Timeout                 time.Duration
	ClientResultChunkSizeMB int
	RowsPerResultSet        int64
	Retry                   bool
}

// StatementRequest identifies a previously submitted statement. The status
// URL may be relative or absolute, but absolute URLs must match the configured
// Snowflake account host.
type StatementRequest struct {
	StatementHandle    string
	StatementStatusURL string
	RequestID          string
	AccountIdentifier  string
	Warehouse          string
	Database           string
	Schema             string
	Role               string
	User               string
}

type PartitionRequest struct {
	StatementRequest
	Partition int
}

type ExplainRequest struct {
	SQL                     string
	RequestID               string
	QueryTag                string
	AccountIdentifier       string
	Warehouse               string
	Database                string
	Schema                  string
	Role                    string
	User                    string
	Timeout                 time.Duration
	ClientResultChunkSizeMB int
	PollInterval            time.Duration
	CleanupTimeout          time.Duration
	// OnObservation is runtime-only. A non-nil error means the accepted
	// handle/cursor was not durably persisted and the profile must abort.
	OnObservation func(ExplainObservation) error `json:"-"`
}

type ExplainObservation struct {
	Phase     string
	Statement StatementSnapshot
}

type StatementColumn struct {
	Name       string `json:"name"`
	Type       string `json:"type,omitempty"`
	Database   string `json:"database,omitempty"`
	Schema     string `json:"schema,omitempty"`
	Table      string `json:"table,omitempty"`
	ByteLength int64  `json:"byteLength,omitempty"`
	Length     int64  `json:"length,omitempty"`
	Precision  int64  `json:"precision,omitempty"`
	Scale      int64  `json:"scale,omitempty"`
	Nullable   bool   `json:"nullable,omitempty"`
}

type PartitionInfo struct {
	RowCount         int64 `json:"rowCount,omitempty"`
	CompressedSize   int64 `json:"compressedSize,omitempty"`
	UncompressedSize int64 `json:"uncompressedSize,omitempty"`
}

type ResultSetMetadata struct {
	NumRows       int64           `json:"numRows,omitempty"`
	Format        string          `json:"format,omitempty"`
	PartitionInfo []PartitionInfo `json:"partitionInfo,omitempty"`
}

// StatementSnapshot is the lossless provider observation used by the durable
// scheduler. HTTPStatus and ProviderState retain Snowflake protocol details;
// State is the normalized application lifecycle state.
type StatementSnapshot struct {
	HTTPStatus         int
	StatementHandle    string
	StatementStatusURL string
	RequestID          string
	Code               string
	SQLState           string
	Message            string
	State              string
	ProviderState      string
	Terminal           bool
	Succeeded          bool
	Throttled          bool
	Columns            []StatementColumn
	Rows               [][]any
	ResultMetadata     ResultSetMetadata
	RetryAfter         time.Duration
	Raw                json.RawMessage
}

type ResultPartition struct {
	HTTPStatus      int
	StatementHandle string
	RequestID       string
	Partition       int
	Columns         []StatementColumn
	Rows            [][]any
	ResultMetadata  ResultSetMetadata
	Raw             json.RawMessage
}

type ExplainProfile struct {
	ReadOnly           bool
	ExplainAttempted   bool
	ExplainSucceeded   bool
	Quality            string
	StatementHandle    string
	RequestID          string
	PartitionsTotal    int64
	PartitionsAssigned int64
	BytesAssigned      int64
	Operations         json.RawMessage
	Raw                json.RawMessage
	Warnings           []string
}

// CancelOutcome deliberately separates provider acceptance from confirmed
// terminal cancellation. Callers must poll/reconcile after Accepted=true.
type CancelOutcome struct {
	HTTPStatus      int
	StatementHandle string
	RequestID       string
	Code            string
	SQLState        string
	Message         string
	Accepted        bool
	Confirmed       bool
	ProviderState   string
	Raw             json.RawMessage
}

// APIError preserves enough protocol information for the durable scheduler to
// decide between retry, authentication recovery, result expiry, and terminal
// provider failure.
type APIError struct {
	HTTPStatus int
	Code       string
	SQLState   string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
	Body       json.RawMessage
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.HTTPStatus > 0 {
		return fmt.Sprintf("Snowflake API request failed (HTTP %d)", e.HTTPStatus)
	}
	return "Snowflake API request failed"
}

// RequestIDForIdempotencyKey derives a stable RFC 4122-shaped UUID from the
// application's durable idempotency key. Snowflake requires a UUID requestId,
// while the shared job service intentionally treats idempotency keys as opaque.
func RequestIDForIdempotencyKey(idempotencyKey string) (string, error) {
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if idempotencyKey == "" {
		return "", errors.New("idempotency key is required")
	}
	sum := sha256.Sum256([]byte("ai-analyst/snowflake/request-id/v1\x00" + idempotencyKey))
	bytes := append([]byte(nil), sum[:16]...)
	bytes[6] = (bytes[6] & 0x0f) | 0x50
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := fmt.Sprintf("%x", bytes)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}

// SubmitAsync submits exactly once. It never retries an ambiguous transport
// failure. To reconcile one, call it again with the same RequestID and Retry
// set to true, which adds Snowflake's retry=true parameter.
func (h *Handler) SubmitAsync(ctx context.Context, req AsyncQueryRequest) (StatementSnapshot, error) {
	req.SQL = strings.TrimSpace(req.SQL)
	if req.SQL == "" {
		return StatementSnapshot{}, errors.New("missing sql")
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.RequestID == "" {
		return StatementSnapshot{}, errors.New("request ID is required for durable Snowflake submission")
	}
	return h.submitAsync(ctx, req)
}

func (h *Handler) submitAsync(ctx context.Context, req AsyncQueryRequest) (StatementSnapshot, error) {
	payload := map[string]any{"statement": strings.TrimSpace(req.SQL)}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = DefaultAsyncStatementTimeout
	}
	if timeout > maxAsyncStatementTimeout {
		timeout = maxAsyncStatementTimeout
	}
	timeoutSeconds := int64((timeout + time.Second - 1) / time.Second)
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}
	payload["timeout"] = timeoutSeconds

	if value := strings.TrimSpace(req.Warehouse); value != "" {
		payload["warehouse"] = value
	}
	if value := strings.TrimSpace(req.Database); value != "" {
		payload["database"] = value
	}
	if value := strings.TrimSpace(req.Schema); value != "" {
		payload["schema"] = value
	}
	if value := strings.TrimSpace(req.Role); value != "" {
		payload["role"] = value
	}
	parameters := map[string]any{}
	if value := strings.TrimSpace(req.QueryTag); value != "" {
		parameters["query_tag"] = value
	}
	if req.ClientResultChunkSizeMB > 0 {
		parameters["client_result_chunk_size"] = req.ClientResultChunkSizeMB
	}
	if req.RowsPerResultSet > 0 {
		parameters["rows_per_resultset"] = req.RowsPerResultSet
	}
	if len(parameters) > 0 {
		payload["parameters"] = parameters
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return StatementSnapshot{}, err
	}

	query := url.Values{}
	query.Set("async", "true")
	query.Set("requestId", req.RequestID)
	if req.Retry {
		query.Set("retry", "true")
	}
	requestPath := "/api/v2/statements?" + query.Encode()
	overrides := authRequestOverridesFromAsync(req)
	resp, err := h.lifecycleRequest(ctx, overrides, req.RequestID, http.MethodPost, requestPath, body)
	if err != nil {
		return StatementSnapshot{}, err
	}
	return decodeStatementSnapshot(resp, req.RequestID)
}

func (h *Handler) PollAsync(ctx context.Context, req StatementRequest) (StatementSnapshot, error) {
	requestPath, err := h.statementRequestPath(req, false, 0)
	if err != nil {
		return StatementSnapshot{}, err
	}
	resp, err := h.lifecycleRequest(ctx, authRequestOverridesFromStatement(req), strings.TrimSpace(req.RequestID), http.MethodGet, requestPath, nil)
	if err != nil {
		return StatementSnapshot{}, err
	}
	snapshot, err := decodeStatementSnapshot(resp, strings.TrimSpace(req.RequestID))
	if snapshot.StatementHandle == "" {
		snapshot.StatementHandle = strings.TrimSpace(req.StatementHandle)
	}
	return snapshot, err
}

func (h *Handler) FetchPartition(ctx context.Context, req PartitionRequest) (ResultPartition, error) {
	if req.Partition < 0 {
		return ResultPartition{}, errors.New("partition must be non-negative")
	}
	requestPath, err := h.statementRequestPath(req.StatementRequest, true, req.Partition)
	if err != nil {
		return ResultPartition{}, err
	}
	resp, err := h.lifecycleRequest(ctx, authRequestOverridesFromStatement(req.StatementRequest), strings.TrimSpace(req.RequestID), http.MethodGet, requestPath, nil)
	if err != nil {
		return ResultPartition{}, err
	}
	snapshot, err := decodeStatementSnapshot(resp, strings.TrimSpace(req.RequestID))
	if err != nil {
		return ResultPartition{}, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return ResultPartition{}, apiErrorFromResponse(resp)
	}
	handle := snapshot.StatementHandle
	if handle == "" {
		handle = strings.TrimSpace(req.StatementHandle)
	}
	return ResultPartition{
		HTTPStatus:      resp.Status,
		StatementHandle: handle,
		RequestID:       strings.TrimSpace(req.RequestID),
		Partition:       req.Partition,
		Columns:         snapshot.Columns,
		Rows:            snapshot.Rows,
		ResultMetadata:  snapshot.ResultMetadata,
		Raw:             append(json.RawMessage(nil), resp.Body...),
	}, nil
}

func (h *Handler) CancelAsync(ctx context.Context, req StatementRequest) (CancelOutcome, error) {
	handle := strings.TrimSpace(req.StatementHandle)
	if handle == "" {
		return CancelOutcome{}, errors.New("statement handle is required")
	}
	query := url.Values{}
	if requestID := strings.TrimSpace(req.RequestID); requestID != "" {
		query.Set("requestId", requestID)
	}
	requestPath := "/api/v2/statements/" + url.PathEscape(handle) + "/cancel"
	if encoded := query.Encode(); encoded != "" {
		requestPath += "?" + encoded
	}
	resp, err := h.lifecycleRequest(ctx, authRequestOverridesFromStatement(req), strings.TrimSpace(req.RequestID), http.MethodPost, requestPath, nil)
	if err != nil {
		return CancelOutcome{}, err
	}
	snapshot, decodeErr := decodeStatementSnapshot(resp, strings.TrimSpace(req.RequestID))
	outcome := CancelOutcome{
		HTTPStatus:      resp.Status,
		StatementHandle: handle,
		RequestID:       strings.TrimSpace(req.RequestID),
		Code:            snapshot.Code,
		SQLState:        snapshot.SQLState,
		Message:         snapshot.Message,
		Accepted:        resp.Status >= 200 && resp.Status < 300,
		Confirmed:       snapshot.State == StatementStateCancelled,
		ProviderState:   snapshot.ProviderState,
		Raw:             append(json.RawMessage(nil), resp.Body...),
	}
	if decodeErr != nil && resp.Status >= 200 && resp.Status < 300 && len(bytes.TrimSpace(resp.Body)) == 0 {
		decodeErr = nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return outcome, apiErrorFromResponse(resp)
	}
	return outcome, decodeErr
}

// ExplainQuery validates the read-only statement and obtains Snowflake's JSON
// optimizer plan. If local observation stops while the EXPLAIN is active, it
// requests best-effort cancellation under a fresh bounded cleanup context.
func (h *Handler) ExplainQuery(ctx context.Context, req ExplainRequest) (ExplainProfile, error) {
	sqlText, _, err := validateUserSQL(req.SQL)
	if err != nil {
		return ExplainProfile{ReadOnly: false, ExplainAttempted: false}, err
	}
	requestID := strings.TrimSpace(req.RequestID)
	if requestID == "" {
		requestID = h.newRequestID()
	}
	profile := ExplainProfile{
		ReadOnly:         true,
		ExplainAttempted: true,
		RequestID:        requestID,
		Quality:          "unavailable",
	}
	snapshot, err := h.submitAsync(ctx, AsyncQueryRequest{
		SQL:                     "EXPLAIN USING JSON " + sqlText,
		RequestID:               requestID,
		QueryTag:                strings.TrimSpace(req.QueryTag),
		AccountIdentifier:       req.AccountIdentifier,
		Warehouse:               req.Warehouse,
		Database:                req.Database,
		Schema:                  req.Schema,
		Role:                    req.Role,
		User:                    req.User,
		Timeout:                 req.Timeout,
		ClientResultChunkSizeMB: req.ClientResultChunkSizeMB,
	})
	if err != nil {
		return profile, err
	}
	if strings.TrimSpace(snapshot.StatementHandle) == "" {
		return profile, &APIError{
			HTTPStatus: snapshot.HTTPStatus,
			Code:       snapshot.Code,
			SQLState:   snapshot.SQLState,
			Message:    firstNonEmpty(snapshot.Message, "Snowflake accepted EXPLAIN without a statement handle"),
			Retryable:  snapshot.Throttled,
			RetryAfter: snapshot.RetryAfter,
			Body:       snapshot.Raw,
		}
	}
	profile.StatementHandle = snapshot.StatementHandle
	if err := publishSnowflakeExplainObservation(req, snapshot); err != nil {
		h.cancelActiveExplain(req, snapshot)
		return profile, fmt.Errorf("publish Snowflake EXPLAIN observation: %w", err)
	}
	if err := ctx.Err(); err != nil {
		h.cancelActiveExplain(req, snapshot)
		return profile, err
	}
	pollInterval := req.PollInterval
	if pollInterval <= 0 {
		pollInterval = defaultSnowflakePollSleep
	}
	for !snapshot.Terminal {
		wait := pollInterval
		if snapshot.RetryAfter > wait {
			wait = snapshot.RetryAfter
		}
		if err := h.sleep(ctx, wait); err != nil {
			h.cancelActiveExplain(req, snapshot)
			return profile, err
		}
		next, pollErr := h.PollAsync(ctx, StatementRequest{
			StatementHandle:    snapshot.StatementHandle,
			StatementStatusURL: snapshot.StatementStatusURL,
			RequestID:          requestID,
			AccountIdentifier:  req.AccountIdentifier,
			Warehouse:          req.Warehouse,
			Database:           req.Database,
			Schema:             req.Schema,
			Role:               req.Role,
			User:               req.User,
		})
		if pollErr != nil {
			h.cancelActiveExplain(req, snapshot)
			return profile, pollErr
		}
		snapshot = next
		if err := publishSnowflakeExplainObservation(req, snapshot); err != nil {
			h.cancelActiveExplain(req, snapshot)
			return profile, fmt.Errorf("publish Snowflake EXPLAIN observation: %w", err)
		}
		if err := ctx.Err(); err != nil {
			h.cancelActiveExplain(req, snapshot)
			return profile, err
		}
	}
	if !snapshot.Succeeded {
		return profile, &APIError{
			HTTPStatus: snapshot.HTTPStatus,
			Code:       snapshot.Code,
			SQLState:   snapshot.SQLState,
			Message:    firstNonEmpty(snapshot.Message, "Snowflake EXPLAIN failed"),
			Body:       snapshot.Raw,
		}
	}
	rows := snapshot.Rows
	if len(rows) == 0 && len(snapshot.ResultMetadata.PartitionInfo) > 0 {
		partition, fetchErr := h.FetchPartition(ctx, PartitionRequest{
			StatementRequest: StatementRequest{
				StatementHandle:    snapshot.StatementHandle,
				StatementStatusURL: snapshot.StatementStatusURL,
				RequestID:          requestID,
				AccountIdentifier:  req.AccountIdentifier,
				Warehouse:          req.Warehouse,
				Database:           req.Database,
				Schema:             req.Schema,
				Role:               req.Role,
				User:               req.User,
			},
			Partition: 0,
		})
		if fetchErr != nil {
			return profile, fetchErr
		}
		rows = partition.Rows
	}
	rawPlan, operations, total, assigned, bytesAssigned, parseErr := parseExplainRows(rows)
	if parseErr != nil {
		profile.Warnings = append(profile.Warnings, parseErr.Error())
		return profile, parseErr
	}
	profile.ExplainSucceeded = true
	profile.Raw = rawPlan
	profile.Operations = operations
	profile.PartitionsTotal = total
	profile.PartitionsAssigned = assigned
	profile.BytesAssigned = bytesAssigned
	if total > 0 || assigned > 0 || bytesAssigned > 0 {
		profile.Quality = "optimizer_estimate"
	} else {
		profile.Quality = "plan_only"
		profile.Warnings = append(profile.Warnings, "Snowflake EXPLAIN did not provide scan estimates")
	}
	return profile, nil
}

func publishSnowflakeExplainObservation(req ExplainRequest, snapshot StatementSnapshot) error {
	if req.OnObservation == nil {
		return nil
	}
	return req.OnObservation(ExplainObservation{Phase: "explain_json", Statement: snapshot})
}

const maximumExplainCleanupTimeout = 5 * time.Second

func (h *Handler) cancelActiveExplain(req ExplainRequest, snapshot StatementSnapshot) {
	if snapshot.Terminal || strings.TrimSpace(snapshot.StatementHandle) == "" {
		return
	}
	timeout := boundedSnowflakeExplainCleanupTimeout(req.CleanupTimeout)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = h.CancelAsync(cleanupCtx, StatementRequest{
		StatementHandle:    snapshot.StatementHandle,
		StatementStatusURL: snapshot.StatementStatusURL,
		RequestID:          snapshot.RequestID,
		AccountIdentifier:  req.AccountIdentifier,
		Warehouse:          req.Warehouse,
		Database:           req.Database,
		Schema:             req.Schema,
		Role:               req.Role,
		User:               req.User,
	})
}

func boundedSnowflakeExplainCleanupTimeout(requested time.Duration) time.Duration {
	if requested <= 0 || requested > maximumExplainCleanupTimeout {
		return maximumExplainCleanupTimeout
	}
	return requested
}

type lifecycleHTTPResponse struct {
	Status int
	Header http.Header
	Body   []byte
}

func (h *Handler) lifecycleRequest(ctx context.Context, overrides authRequestOverrides, requestID, method, requestPath string, body []byte) (lifecycleHTTPResponse, error) {
	for attempt := 0; attempt < 2; attempt++ {
		cfg, err := h.authConfig(overrides)
		if err != nil {
			return lifecycleHTTPResponse{}, err
		}
		cfg.RequestID = strings.TrimSpace(requestID)
		validatedPath, err := validateSnowflakeRequestURL(cfg, requestPath)
		if err != nil {
			return lifecycleHTTPResponse{}, err
		}
		resp, err := h.doSnowflakeLifecycleRaw(ctx, cfg, method, validatedPath, body)
		if err != nil {
			return lifecycleHTTPResponse{}, err
		}
		if resp.Status == http.StatusUnauthorized && cfg.Mode == snowflakeAuthModeKeypairJWT && attempt == 0 {
			h.invalidateCachedJWT()
			continue
		}
		return resp, nil
	}
	return lifecycleHTTPResponse{}, errors.New("Snowflake authentication retry failed")
}

func (h *Handler) doSnowflakeLifecycleRaw(ctx context.Context, cfg snowflakeAuthConfig, method, requestPath string, body []byte) (lifecycleHTTPResponse, error) {
	client := h.httpc
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	reqCtx := ctx
	cancel := func() {}
	if timeout := h.requestTimeout(); timeout > 0 {
		reqCtx, cancel = context.WithTimeout(ctx, timeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, method, requestPath, bodyReader)
	if err != nil {
		return lifecycleHTTPResponse{}, err
	}
	for key, value := range h.snowflakeHeaders(cfg) {
		if strings.TrimSpace(value) != "" {
			req.Header.Set(key, value)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		if reqCtx.Err() != nil {
			if ctx.Err() != nil {
				return lifecycleHTTPResponse{}, ctx.Err()
			}
			return lifecycleHTTPResponse{}, reqCtx.Err()
		}
		return lifecycleHTTPResponse{}, &APIError{HTTPStatus: http.StatusBadGateway, Message: fmt.Sprintf("failed to reach Snowflake API: %v", err), Retryable: true}
	}
	defer resp.Body.Close()
	payload, err := readLimitedBody(resp.Body, snowflakeResponseBodyLimit)
	if err != nil {
		return lifecycleHTTPResponse{}, err
	}
	return lifecycleHTTPResponse{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: payload}, nil
}

func (h *Handler) statementRequestPath(req StatementRequest, partition bool, partitionNumber int) (string, error) {
	handle := strings.TrimSpace(req.StatementHandle)
	statusURL := strings.TrimSpace(req.StatementStatusURL)
	var parsed *url.URL
	var err error
	if statusURL != "" {
		parsed, err = url.Parse(statusURL)
		if err != nil {
			return "", fmt.Errorf("invalid statement status URL: %w", err)
		}
	} else {
		if handle == "" {
			return "", errors.New("statement handle or status URL is required")
		}
		parsed = &url.URL{Path: "/api/v2/statements/" + url.PathEscape(handle)}
	}
	query := parsed.Query()
	if requestID := strings.TrimSpace(req.RequestID); requestID != "" {
		query.Set("requestId", requestID)
	}
	if partition {
		if handle == "" {
			return "", errors.New("statement handle is required to fetch a partition")
		}
		parsed.Path = "/api/v2/statements/" + url.PathEscape(handle)
		query.Set("partition", strconv.Itoa(partitionNumber))
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func validateSnowflakeRequestURL(cfg snowflakeAuthConfig, requestPath string) (string, error) {
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "" {
		return "", errors.New("Snowflake request URL is required")
	}
	parsed, err := url.Parse(requestPath)
	if err != nil {
		return "", err
	}
	if parsed.IsAbs() {
		base, err := url.Parse(cfg.BaseURL)
		if err != nil {
			return "", err
		}
		if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), base.Hostname()) || effectivePort(parsed) != effectivePort(base) {
			return "", errors.New("statement status URL does not match the configured Snowflake account host")
		}
		return parsed.String(), nil
	}
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}
	return strings.TrimRight(cfg.BaseURL, "/") + parsed.String(), nil
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}

func (h *Handler) invalidateCachedJWT() {
	if h == nil {
		return
	}
	h.tokenMu.Lock()
	h.cachedJWT = ""
	h.cachedJWTKey = ""
	h.cachedJWTUntil = time.Time{}
	h.tokenMu.Unlock()
}

func decodeStatementSnapshot(resp lifecycleHTTPResponse, requestID string) (StatementSnapshot, error) {
	var decoded snowflakeStatementResponse
	if len(bytes.TrimSpace(resp.Body)) > 0 {
		if err := json.Unmarshal(resp.Body, &decoded); err != nil {
			return StatementSnapshot{}, &APIError{HTTPStatus: http.StatusBadGateway, Message: "Invalid JSON from Snowflake", Body: append(json.RawMessage(nil), resp.Body...)}
		}
	}
	providerState := strings.ToUpper(strings.TrimSpace(firstNonEmpty(decoded.StatementStatus, decoded.Status)))
	state, terminal, succeeded := normalizeAsyncStatementState(resp.Status, providerState, decoded)
	statusURL := strings.TrimSpace(decoded.StatementStatusURL)
	if statusURL == "" {
		statusURL = strings.TrimSpace(resp.Header.Get("Location"))
	}
	snapshot := StatementSnapshot{
		HTTPStatus:         resp.Status,
		StatementHandle:    strings.TrimSpace(decoded.StatementHandle),
		StatementStatusURL: statusURL,
		RequestID:          strings.TrimSpace(requestID),
		Code:               strings.TrimSpace(decoded.Code),
		SQLState:           strings.TrimSpace(decoded.SQLState),
		Message:            strings.TrimSpace(decoded.Message),
		State:              state,
		ProviderState:      providerState,
		Terminal:           terminal,
		Succeeded:          succeeded,
		Throttled:          resp.Status == http.StatusTooManyRequests,
		Columns:            exportedStatementColumns(statementColumns(decoded)),
		Rows:               statementRows(decoded.Data),
		ResultMetadata:     exportedResultMetadata(decoded),
		RetryAfter:         retryAfterHeader(resp.Header.Get("Retry-After")),
		Raw:                append(json.RawMessage(nil), resp.Body...),
	}
	if resp.Status == http.StatusUnprocessableEntity {
		return snapshot, nil
	}
	if resp.Status == http.StatusTooManyRequests {
		return snapshot, nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return snapshot, apiErrorFromResponse(resp)
	}
	return snapshot, nil
}

func normalizeAsyncStatementState(httpStatus int, providerState string, decoded snowflakeStatementResponse) (string, bool, bool) {
	switch providerState {
	case "SUCCESS", "SUCCEEDED", "DONE", "COMPLETE", "COMPLETED", "FINISHED":
		return StatementStateCompleted, true, true
	case "FAILED", "FAILED_WITH_ERROR", "ABORTED", "ERROR":
		return StatementStateFailed, true, false
	case "CANCELLED", "CANCELED":
		return StatementStateCancelled, true, false
	case "QUEUED", "QUEUED_OVERLOAD", "RESUMING_WAREHOUSE":
		return StatementStateQueued, false, false
	case "BLOCKED":
		return StatementStateBlocked, false, false
	case "RUNNING":
		return StatementStateRunning, false, false
	}
	if httpStatus == http.StatusUnprocessableEntity {
		return StatementStateFailed, true, false
	}
	if httpStatus == http.StatusAccepted || httpStatus == http.StatusTooManyRequests || strings.TrimSpace(decoded.StatementStatusURL) != "" {
		return StatementStateRunning, false, false
	}
	if httpStatus >= 200 && httpStatus < 300 {
		return StatementStateCompleted, true, true
	}
	return StatementStateSubmitted, false, false
}

func apiErrorFromResponse(resp lifecycleHTTPResponse) *APIError {
	var decoded snowflakeStatementResponse
	_ = json.Unmarshal(resp.Body, &decoded)
	message := strings.TrimSpace(decoded.Message)
	if message == "" {
		message = snowflakeHTTPMessage(resp.Status, resp.Body)
	}
	return &APIError{
		HTTPStatus: resp.Status,
		Code:       strings.TrimSpace(decoded.Code),
		SQLState:   strings.TrimSpace(decoded.SQLState),
		Message:    message,
		Retryable:  retryableStatus(resp.Status),
		RetryAfter: retryAfterHeader(resp.Header.Get("Retry-After")),
		Body:       append(json.RawMessage(nil), resp.Body...),
	}
}

func retryAfterHeader(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if duration := time.Until(when); duration > 0 {
			return duration
		}
	}
	return 0
}

func authRequestOverridesFromAsync(req AsyncQueryRequest) authRequestOverrides {
	return authRequestOverrides{
		AccountIdentifier: req.AccountIdentifier,
		Warehouse:         req.Warehouse,
		Database:          req.Database,
		Schema:            req.Schema,
		Role:              req.Role,
		User:              req.User,
	}
}

func authRequestOverridesFromStatement(req StatementRequest) authRequestOverrides {
	return authRequestOverrides{
		AccountIdentifier: req.AccountIdentifier,
		Warehouse:         req.Warehouse,
		Database:          req.Database,
		Schema:            req.Schema,
		Role:              req.Role,
		User:              req.User,
	}
}

func exportedStatementColumns(columns []snowflakeStatementColumn) []StatementColumn {
	out := make([]StatementColumn, 0, len(columns))
	for _, column := range columns {
		out = append(out, StatementColumn{
			Name:       column.Name,
			Type:       column.Type,
			Database:   column.Database,
			Schema:     column.Schema,
			Table:      column.Table,
			ByteLength: column.ByteLength,
			Length:     column.Length,
			Precision:  column.Precision,
			Scale:      column.Scale,
			Nullable:   column.Nullable,
		})
	}
	return out
}

func exportedResultMetadata(resp snowflakeStatementResponse) ResultSetMetadata {
	metadata := ResultSetMetadata{}
	var partitions []snowflakePartitionInfo
	if resp.ResultSetMetaData != nil {
		metadata.NumRows = resp.ResultSetMetaData.NumRows
		metadata.Format = resp.ResultSetMetaData.Format
		partitions = resp.ResultSetMetaData.PartitionInfo
	} else {
		partitions = resp.PartitionInfo
	}
	metadata.PartitionInfo = make([]PartitionInfo, 0, len(partitions))
	for _, partition := range partitions {
		metadata.PartitionInfo = append(metadata.PartitionInfo, PartitionInfo{
			RowCount:         partition.RowCount,
			CompressedSize:   partition.CompressedSize,
			UncompressedSize: partition.UncompressedSize,
		})
	}
	return metadata
}

func parseExplainRows(rows [][]any) (json.RawMessage, json.RawMessage, int64, int64, int64, error) {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, nil, 0, 0, 0, errors.New("Snowflake EXPLAIN returned no JSON plan")
	}
	var raw []byte
	switch value := rows[0][0].(type) {
	case string:
		raw = []byte(strings.TrimSpace(value))
	case json.RawMessage:
		raw = append([]byte(nil), value...)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, nil, 0, 0, 0, fmt.Errorf("encode Snowflake EXPLAIN JSON: %w", err)
		}
		raw = encoded
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil, 0, 0, 0, errors.New("Snowflake EXPLAIN returned an empty JSON plan")
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, nil, 0, 0, 0, fmt.Errorf("invalid Snowflake EXPLAIN JSON: %w", err)
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return nil, nil, 0, 0, 0, err
	}
	var total, assigned, bytesAssigned int64
	var operations json.RawMessage
	walkExplainJSON(decoded, func(key string, value any) {
		switch normalizedJSONKey(key) {
		case "partitionstotal":
			if number, ok := flexibleInt64(value); ok {
				total = number
			}
		case "partitionsassigned":
			if number, ok := flexibleInt64(value); ok {
				assigned = number
			}
		case "bytesassigned":
			if number, ok := flexibleInt64(value); ok {
				bytesAssigned = number
			}
		case "operations":
			if operations == nil {
				if encoded, err := json.Marshal(value); err == nil {
					operations = encoded
				}
			}
		}
	})
	return canonical, operations, total, assigned, bytesAssigned, nil
}

func walkExplainJSON(value any, visit func(string, any)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			visit(key, child)
			walkExplainJSON(child, visit)
		}
	case []any:
		for _, child := range typed {
			walkExplainJSON(child, visit)
		}
	}
}

func normalizedJSONKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, "-", "")
	return value
}

func flexibleInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		result, err := typed.Int64()
		return result, err == nil
	case float64:
		return int64(typed), true
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	case string:
		result, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return result, err == nil
	default:
		return 0, false
	}
}
