package trino

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/sqlutil"
)

// StatementColumn is a column descriptor returned by Trino's statement API.
// TypeSignature is retained because complex types cannot always be faithfully
// represented by the display Type string alone.
type StatementColumn struct {
	Name          string          `json:"name"`
	Type          string          `json:"type,omitempty"`
	TypeSignature json.RawMessage `json:"typeSignature,omitempty"`
}

// Internal aliases keep the established synchronous handler implementation
// source-compatible while exposing the richer lifecycle types.
type trinoStatementColumn = StatementColumn

// QueryError is a terminal query error embedded in an otherwise successful
// Trino protocol response. It is intentionally distinct from an HTTP or
// monitoring error.
type QueryError struct {
	Message       string          `json:"message,omitempty"`
	ErrorCode     int             `json:"errorCode,omitempty"`
	ErrorName     string          `json:"errorName,omitempty"`
	ErrorType     string          `json:"errorType,omitempty"`
	ErrorLocation json.RawMessage `json:"errorLocation,omitempty"`
	FailureInfo   json.RawMessage `json:"failureInfo,omitempty"`
}

func (e *QueryError) Error() string { return statementErrorMessage(e) }

type trinoStatementError = QueryError

type QueryWarningCode struct {
	Code int    `json:"code,omitempty"`
	Name string `json:"name,omitempty"`
}

type QueryWarning struct {
	WarningCode QueryWarningCode `json:"warningCode,omitempty"`
	Message     string           `json:"message,omitempty"`
}

// QueryStats mirrors the progress fields emitted by current Trino statement
// responses. Raw retains fields introduced by newer coordinators, allowing
// callers to persist telemetry without losing forward-compatible data.
type QueryStats struct {
	State              string   `json:"state,omitempty"`
	Queued             bool     `json:"queued,omitempty"`
	Scheduled          bool     `json:"scheduled,omitempty"`
	ProgressPercentage *float64 `json:"progressPercentage,omitempty"`
	RunningPercentage  *float64 `json:"runningPercentage,omitempty"`

	Nodes           int `json:"nodes,omitempty"`
	TotalSplits     int `json:"totalSplits,omitempty"`
	QueuedSplits    int `json:"queuedSplits,omitempty"`
	RunningSplits   int `json:"runningSplits,omitempty"`
	CompletedSplits int `json:"completedSplits,omitempty"`

	PlanningTimeMillis      int64 `json:"planningTimeMillis,omitempty"`
	AnalysisTimeMillis      int64 `json:"analysisTimeMillis,omitempty"`
	CPUTimeMillis           int64 `json:"cpuTimeMillis,omitempty"`
	WallTimeMillis          int64 `json:"wallTimeMillis,omitempty"`
	QueuedTimeMillis        int64 `json:"queuedTimeMillis,omitempty"`
	ElapsedTimeMillis       int64 `json:"elapsedTimeMillis,omitempty"`
	FinishingTimeMillis     int64 `json:"finishingTimeMillis,omitempty"`
	PhysicalInputTimeMillis int64 `json:"physicalInputTimeMillis,omitempty"`

	ProcessedRows             int64 `json:"processedRows,omitempty"`
	ProcessedBytes            int64 `json:"processedBytes,omitempty"`
	PhysicalInputRows         int64 `json:"physicalInputRows,omitempty"`
	PhysicalInputBytes        int64 `json:"physicalInputBytes,omitempty"`
	PhysicalWrittenRows       int64 `json:"physicalWrittenRows,omitempty"`
	PhysicalWrittenBytes      int64 `json:"physicalWrittenBytes,omitempty"`
	InternalNetworkInputRows  int64 `json:"internalNetworkInputRows,omitempty"`
	InternalNetworkInputBytes int64 `json:"internalNetworkInputBytes,omitempty"`
	PeakMemoryBytes           int64 `json:"peakMemoryBytes,omitempty"`
	SpilledBytes              int64 `json:"spilledBytes,omitempty"`

	RootStage json.RawMessage `json:"rootStage,omitempty"`
	Raw       json.RawMessage `json:"-"`
}

func (s *QueryStats) UnmarshalJSON(data []byte) error {
	type plain QueryStats
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*s = QueryStats(decoded)
	s.Raw = append(s.Raw[:0], data...)
	return nil
}

// ProtocolState is the mutable Trino client protocol state that must be
// persisted with a durable nextUri. Authentication is deliberately excluded.
type ProtocolState struct {
	Catalog            string            `json:"catalog,omitempty"`
	Schema             string            `json:"schema,omitempty"`
	Path               string            `json:"path,omitempty"`
	SessionProperties  map[string]string `json:"session_properties,omitempty"`
	Roles              map[string]string `json:"roles,omitempty"`
	OriginalRoles      map[string]string `json:"original_roles,omitempty"`
	PreparedStatements map[string]string `json:"prepared_statements,omitempty"`
	TransactionID      string            `json:"transaction_id,omitempty"`
	AuthorizationUser  string            `json:"authorization_user,omitempty"`
	OriginalUser       string            `json:"original_user,omitempty"`
}

// ProtocolStateDelta records exactly what the coordinator changed on a
// response. ProtocolState on AsyncQueryPage is the post-merge state to use on
// the next request.
type ProtocolStateDelta struct {
	SetCatalog                    string            `json:"set_catalog,omitempty"`
	SetSchema                     string            `json:"set_schema,omitempty"`
	SetPath                       string            `json:"set_path,omitempty"`
	SetSession                    map[string]string `json:"set_session,omitempty"`
	ClearSession                  []string          `json:"clear_session,omitempty"`
	SetRoles                      map[string]string `json:"set_roles,omitempty"`
	SetOriginalRoles              map[string]string `json:"set_original_roles,omitempty"`
	AddedPreparedStatements       map[string]string `json:"added_prepared_statements,omitempty"`
	DeallocatedPreparedStatements []string          `json:"deallocated_prepared_statements,omitempty"`
	StartedTransactionID          string            `json:"started_transaction_id,omitempty"`
	ClearTransactionID            bool              `json:"clear_transaction_id,omitempty"`
	SetAuthorizationUser          string            `json:"set_authorization_user,omitempty"`
	ResetAuthorizationUser        bool              `json:"reset_authorization_user,omitempty"`
}

type AsyncQueryRequest struct {
	SQL     string
	Host    string
	Catalog string
	Schema  string
	Source  string

	TraceToken        string
	ClientTags        []string
	SessionProperties map[string]string
	ProtocolState     ProtocolState
}

type AsyncPollRequest struct {
	QueryID string
	NextURI string
	Host    string
	Catalog string
	Schema  string
	Source  string

	TraceToken        string
	ClientTags        []string
	SessionProperties map[string]string
	ProtocolState     ProtocolState
	Columns           []StatementColumn
}

// AsyncQueryPage represents exactly one statement-protocol response. Rows are
// never accumulated across nextUri requests, which makes the request URI a
// stable durable chunk boundary.
type AsyncQueryPage struct {
	Completed bool `json:"completed"`

	QueryID          string `json:"query_id,omitempty"`
	InfoURI          string `json:"info_uri,omitempty"`
	RequestURI       string `json:"request_uri,omitempty"`
	NextURI          string `json:"next_uri,omitempty"`
	PartialCancelURI string `json:"partial_cancel_uri,omitempty"`

	Columns  []StatementColumn `json:"columns,omitempty"`
	Rows     []map[string]any  `json:"rows,omitempty"`
	Stats    QueryStats        `json:"stats,omitempty"`
	Warnings []QueryWarning    `json:"warnings,omitempty"`
	Error    *QueryError       `json:"error,omitempty"`

	ProtocolState      ProtocolState      `json:"protocol_state,omitempty"`
	ProtocolStateDelta ProtocolStateDelta `json:"protocol_state_delta,omitempty"`
}

type CancelOutcome struct {
	Accepted   bool   `json:"accepted"`
	Confirmed  bool   `json:"confirmed"`
	QueryID    string `json:"query_id,omitempty"`
	RequestURI string `json:"request_uri,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type ExplainRequest struct {
	SQL     string
	Host    string
	Catalog string
	Schema  string
	Source  string

	TraceToken        string
	ClientTags        []string
	SessionProperties map[string]string
	ProtocolState     ProtocolState
	CleanupTimeout    time.Duration
	// OnObservation is runtime-only. A non-nil error means the accepted
	// query/cursor was not durably persisted and the current phase must abort.
	OnObservation func(ExplainObservation) error `json:"-"`
}

type ExplainObservation struct {
	Phase string
	Page  AsyncQueryPage
}

type ExplainPhase struct {
	QueryID string          `json:"query_id,omitempty"`
	Text    string          `json:"text,omitempty"`
	JSON    json.RawMessage `json:"json,omitempty"`
	Stats   QueryStats      `json:"stats,omitempty"`
}

type ExplainProfile struct {
	Validated   bool         `json:"validated"`
	Validate    ExplainPhase `json:"validate"`
	IO          ExplainPhase `json:"io"`
	Distributed ExplainPhase `json:"distributed"`

	EstimateQuality string        `json:"estimate_quality"`
	Warnings        []string      `json:"warnings,omitempty"`
	ProtocolState   ProtocolState `json:"protocol_state,omitempty"`
}

// UpstreamError describes statement-protocol transport failures. Query errors
// remain embedded in AsyncQueryPage so monitoring outages cannot be confused
// with provider execution failures.
type UpstreamError struct {
	StatusCode int
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e UpstreamError) Error() string { return e.Message }

func (h *Handler) SubmitAsync(ctx context.Context, req AsyncQueryRequest) (AsyncQueryPage, error) {
	sqlText := strings.TrimSpace(req.SQL)
	if sqlText == "" {
		return AsyncQueryPage{}, errors.New("missing sql")
	}
	cfg, err := h.authConfig(authRequestOverrides{
		Host:    req.Host,
		Catalog: req.Catalog,
		Schema:  req.Schema,
		Source:  req.Source,
	})
	if err != nil {
		return AsyncQueryPage{}, err
	}
	protocolState := protocolStateWithOriginalUser(req.ProtocolState, cfg.User)
	headers, err := h.lifecycleHeaders(cfg, lifecycleRequestControls{
		Catalog:           req.Catalog,
		Schema:            req.Schema,
		Source:            req.Source,
		TraceToken:        req.TraceToken,
		ClientTags:        req.ClientTags,
		SessionProperties: req.SessionProperties,
		ProtocolState:     protocolState,
	})
	if err != nil {
		return AsyncQueryPage{}, err
	}
	statementURL := strings.TrimRight(cfg.BaseURL, "/") + "/v1/statement"
	// POST is intentionally attempted once. Trino has no idempotency key for
	// statement submission, so an automatic retry can duplicate execution.
	resp, responseHeaders, err := h.doTrinoStatementWithHeaders(
		ctx, http.MethodPost, statementURL, headers, []byte(sqlText), false,
	)
	if err != nil {
		return AsyncQueryPage{}, err
	}
	return asyncPageFromResponse(statementURL, nil, protocolState, resp, responseHeaders), nil
}

func (h *Handler) PollAsync(ctx context.Context, req AsyncPollRequest) (AsyncQueryPage, error) {
	nextURI := strings.TrimSpace(req.NextURI)
	if nextURI == "" {
		return AsyncQueryPage{}, errors.New("next_uri is required")
	}
	cfg, err := h.authConfig(authRequestOverrides{
		Host:    req.Host,
		Catalog: req.Catalog,
		Schema:  req.Schema,
		Source:  req.Source,
	})
	if err != nil {
		return AsyncQueryPage{}, err
	}
	if err := validateNextURI(nextURI, cfg.BaseURL); err != nil {
		return AsyncQueryPage{}, err
	}
	protocolState := protocolStateWithOriginalUser(req.ProtocolState, cfg.User)
	headers, err := h.lifecycleHeaders(cfg, lifecycleRequestControls{
		Catalog:           req.Catalog,
		Schema:            req.Schema,
		Source:            req.Source,
		TraceToken:        req.TraceToken,
		ClientTags:        req.ClientTags,
		SessionProperties: req.SessionProperties,
		ProtocolState:     protocolState,
	})
	if err != nil {
		return AsyncQueryPage{}, err
	}
	// Repeating the same nextUri GET is safe and is the recovery mechanism for
	// a crash after receipt but before durable commit. Keep each registry poll
	// to one HTTP attempt, though: Retry-After is part of the durable scheduler
	// contract, so sleeping and retrying inside this call would hide the pacing
	// deadline from lease handoff/restart recovery.
	resp, responseHeaders, err := h.doTrinoStatementWithHeaders(
		ctx, http.MethodGet, nextURI, headers, nil, false,
	)
	if err != nil {
		return AsyncQueryPage{}, err
	}
	page := asyncPageFromResponse(nextURI, req.Columns, protocolState, resp, responseHeaders)
	if page.QueryID == "" {
		page.QueryID = strings.TrimSpace(req.QueryID)
	}
	return page, nil
}

// CancelAsync requests full-query cancellation using the current nextUri. It
// never uses partialCancelUri, which only cancels a leaf stage.
func (h *Handler) CancelAsync(ctx context.Context, req AsyncPollRequest) (CancelOutcome, error) {
	nextURI := strings.TrimSpace(req.NextURI)
	if nextURI == "" {
		return CancelOutcome{}, errors.New("next_uri is required for full query cancellation")
	}
	cfg, err := h.authConfig(authRequestOverrides{
		Host:    req.Host,
		Catalog: req.Catalog,
		Schema:  req.Schema,
		Source:  req.Source,
	})
	if err != nil {
		return CancelOutcome{}, err
	}
	if err := validateNextURI(nextURI, cfg.BaseURL); err != nil {
		return CancelOutcome{}, err
	}
	headers, err := h.lifecycleHeaders(cfg, lifecycleRequestControls{
		Catalog:           req.Catalog,
		Schema:            req.Schema,
		Source:            req.Source,
		TraceToken:        req.TraceToken,
		ClientTags:        req.ClientTags,
		SessionProperties: req.SessionProperties,
		ProtocolState:     req.ProtocolState,
	})
	if err != nil {
		return CancelOutcome{}, err
	}
	_, status, _, err := h.doTrinoRawResponse(ctx, http.MethodDelete, nextURI, headers, nil, false)
	if err != nil {
		return CancelOutcome{}, err
	}
	return CancelOutcome{
		Accepted:   true,
		Confirmed:  false,
		QueryID:    strings.TrimSpace(req.QueryID),
		RequestURI: nextURI,
		HTTPStatus: status,
	}, nil
}

// ExplainQuery validates the statement and captures both IO and distributed
// JSON plans. It never uses EXPLAIN ANALYZE because preflight must not execute
// the user's query.
func (h *Handler) ExplainQuery(ctx context.Context, req ExplainRequest) (ExplainProfile, error) {
	sqlText := strings.TrimSpace(req.SQL)
	if sqlText == "" {
		return ExplainProfile{}, errors.New("missing sql")
	}
	if !sqlutil.IsReadOnlySQL(sqlText) {
		return ExplainProfile{}, errors.New("only read-only SELECT/WITH queries are allowed")
	}

	profile := ExplainProfile{EstimateQuality: "connector_dependent"}
	state := cloneProtocolState(req.ProtocolState)
	phases := []struct {
		name  string
		phase string
		sql   string
		dst   *ExplainPhase
	}{
		{name: "validate", phase: "explain_validate", sql: "EXPLAIN (TYPE VALIDATE) " + sqlText, dst: &profile.Validate},
		{name: "io", phase: "explain_io", sql: "EXPLAIN (TYPE IO, FORMAT JSON) " + sqlText, dst: &profile.IO},
		{name: "distributed", phase: "explain_distributed", sql: "EXPLAIN (TYPE DISTRIBUTED, FORMAT JSON) " + sqlText, dst: &profile.Distributed},
	}

	for _, phase := range phases {
		result, nextState, err := h.executeExplainStatement(ctx, req, state, phase.phase, phase.sql)
		if err != nil {
			return profile, fmt.Errorf("Trino EXPLAIN %s: %w", phase.name, err)
		}
		*phase.dst = result
		state = nextState
	}
	profile.Validated = true
	profile.ProtocolState = state
	if len(profile.IO.JSON) == 0 && len(profile.Distributed.JSON) == 0 {
		profile.Warnings = append(profile.Warnings, "Trino did not return machine-readable plan estimates")
	} else {
		profile.Warnings = append(profile.Warnings, "Trino plan estimates depend on connector statistics and may be unavailable or incomplete")
	}
	return profile, nil
}

func (h *Handler) executeExplainStatement(ctx context.Context, req ExplainRequest, state ProtocolState, phase, sqlText string) (ExplainPhase, ProtocolState, error) {
	page, err := h.SubmitAsync(ctx, AsyncQueryRequest{
		SQL:               sqlText,
		Host:              req.Host,
		Catalog:           req.Catalog,
		Schema:            req.Schema,
		Source:            req.Source,
		TraceToken:        req.TraceToken,
		ClientTags:        req.ClientTags,
		SessionProperties: req.SessionProperties,
		ProtocolState:     state,
	})
	if err != nil {
		return ExplainPhase{}, state, err
	}
	if strings.TrimSpace(page.QueryID) == "" {
		h.cancelActiveExplain(req, page)
		return ExplainPhase{}, state, errors.New("Trino accepted EXPLAIN without a query id")
	}
	if err := publishTrinoExplainObservation(req, phase, page); err != nil {
		h.cancelActiveExplain(req, page)
		return ExplainPhase{}, state, fmt.Errorf("publish Trino %s observation: %w", phase, err)
	}
	rows := append([]map[string]any(nil), page.Rows...)
	columns := append([]StatementColumn(nil), page.Columns...)
	latestStats := page.Stats
	state = page.ProtocolState
	queryID := page.QueryID
	if err := ctx.Err(); err != nil {
		h.cancelActiveExplain(req, page)
		return ExplainPhase{}, state, err
	}
	for !page.Completed && page.Error == nil {
		if strings.TrimSpace(page.NextURI) == "" {
			break
		}
		next, pollErr := h.PollAsync(ctx, AsyncPollRequest{
			QueryID:           queryID,
			NextURI:           page.NextURI,
			Host:              req.Host,
			Catalog:           req.Catalog,
			Schema:            req.Schema,
			Source:            req.Source,
			TraceToken:        req.TraceToken,
			ClientTags:        req.ClientTags,
			SessionProperties: req.SessionProperties,
			ProtocolState:     state,
			Columns:           columns,
		})
		if pollErr != nil {
			h.cancelActiveExplain(req, page)
			return ExplainPhase{}, state, pollErr
		}
		page = next
		if err := publishTrinoExplainObservation(req, phase, page); err != nil {
			h.cancelActiveExplain(req, page)
			return ExplainPhase{}, state, fmt.Errorf("publish Trino %s observation: %w", phase, err)
		}
		if err := ctx.Err(); err != nil {
			h.cancelActiveExplain(req, page)
			return ExplainPhase{}, state, err
		}
		if queryID == "" {
			queryID = page.QueryID
		}
		rows = append(rows, page.Rows...)
		if len(columns) == 0 && len(page.Columns) > 0 {
			columns = append(columns, page.Columns...)
		}
		latestStats = page.Stats
		state = page.ProtocolState
	}
	if page.Error != nil {
		return ExplainPhase{}, state, page.Error
	}
	text, rawJSON := explainPayload(rows, columns)
	return ExplainPhase{QueryID: queryID, Text: text, JSON: rawJSON, Stats: latestStats}, state, nil
}

func publishTrinoExplainObservation(req ExplainRequest, phase string, page AsyncQueryPage) error {
	if req.OnObservation == nil {
		return nil
	}
	return req.OnObservation(ExplainObservation{Phase: phase, Page: page})
}

const maximumExplainCleanupTimeout = 5 * time.Second

func (h *Handler) cancelActiveExplain(req ExplainRequest, page AsyncQueryPage) {
	if page.Completed || page.Error != nil || strings.TrimSpace(page.NextURI) == "" {
		return
	}
	timeout := boundedTrinoExplainCleanupTimeout(req.CleanupTimeout)
	cleanupCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, _ = h.CancelAsync(cleanupCtx, AsyncPollRequest{
		QueryID:           page.QueryID,
		NextURI:           page.NextURI,
		Host:              req.Host,
		Catalog:           req.Catalog,
		Schema:            req.Schema,
		Source:            req.Source,
		TraceToken:        req.TraceToken,
		ClientTags:        req.ClientTags,
		SessionProperties: req.SessionProperties,
		ProtocolState:     page.ProtocolState,
		Columns:           page.Columns,
	})
}

func boundedTrinoExplainCleanupTimeout(requested time.Duration) time.Duration {
	if requested <= 0 || requested > maximumExplainCleanupTimeout {
		return maximumExplainCleanupTimeout
	}
	return requested
}

func explainPayload(rows []map[string]any, columns []StatementColumn) (string, json.RawMessage) {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		var value any
		if len(columns) > 0 {
			value = row[columns[0].Name]
		}
		if value == nil {
			keys := make([]string, 0, len(row))
			for key := range row {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if len(keys) > 0 {
				value = row[keys[0]]
			}
		}
		switch typed := value.(type) {
		case string:
			parts = append(parts, typed)
		case nil:
			continue
		default:
			encoded, err := json.Marshal(typed)
			if err == nil {
				parts = append(parts, string(encoded))
			}
		}
	}
	text := strings.Join(parts, "\n")
	trimmed := strings.TrimSpace(text)
	if json.Valid([]byte(trimmed)) {
		return text, json.RawMessage(append([]byte(nil), []byte(trimmed)...))
	}
	return text, nil
}

func asyncPageFromResponse(requestURI string, priorColumns []StatementColumn, priorState ProtocolState, resp trinoStatementResponse, headers http.Header) AsyncQueryPage {
	columns := append([]StatementColumn(nil), resp.Columns...)
	if len(columns) == 0 {
		columns = append(columns, priorColumns...)
	}
	state, delta := applyProtocolResponseHeaders(priorState, headers)
	page := AsyncQueryPage{
		QueryID:            strings.TrimSpace(resp.ID),
		InfoURI:            strings.TrimSpace(resp.InfoURI),
		RequestURI:         strings.TrimSpace(requestURI),
		NextURI:            strings.TrimSpace(resp.NextURI),
		PartialCancelURI:   strings.TrimSpace(resp.PartialCancelURI),
		Columns:            columns,
		Rows:               appendRowsLimited(nil, columns, resp.Data, 0),
		Stats:              resp.Stats,
		Warnings:           append([]QueryWarning(nil), resp.Warnings...),
		Error:              resp.Error,
		ProtocolState:      state,
		ProtocolStateDelta: delta,
	}
	page.Completed = page.Error == nil && page.NextURI == ""
	return page
}

type lifecycleRequestControls struct {
	Catalog           string
	Schema            string
	Source            string
	TraceToken        string
	ClientTags        []string
	SessionProperties map[string]string
	ProtocolState     ProtocolState
}

func (h *Handler) lifecycleHeaders(cfg trinoAuthConfig, controls lifecycleRequestControls) (map[string]string, error) {
	headers := h.trinoHeaders(cfg)
	state := controls.ProtocolState
	if catalog := firstNonEmpty(state.Catalog, controls.Catalog, cfg.Catalog); catalog != "" {
		headers["X-Trino-Catalog"] = catalog
	}
	if schema := firstNonEmpty(state.Schema, controls.Schema, cfg.Schema); schema != "" {
		headers["X-Trino-Schema"] = schema
	}
	if path := strings.TrimSpace(state.Path); path != "" {
		if err := validateHeaderComponent("path", path, false); err != nil {
			return nil, err
		}
		headers["X-Trino-Path"] = path
	}
	if source := firstNonEmpty(controls.Source, cfg.Source, defaultTrinoSource); source != "" {
		headers["X-Trino-Source"] = source
	}
	if trace := strings.TrimSpace(controls.TraceToken); trace != "" {
		if err := validateHeaderComponent("trace token", trace, false); err != nil {
			return nil, err
		}
		headers["X-Trino-Trace-Token"] = trace
	}
	if tags, err := encodeHeaderList(controls.ClientTags); err != nil {
		return nil, fmt.Errorf("client tags: %w", err)
	} else if tags != "" {
		headers["X-Trino-Client-Tags"] = tags
	}

	session := cloneStringMap(state.SessionProperties)
	for key, value := range controls.SessionProperties {
		session[key] = value
	}
	if value, err := encodeAssignments(session); err != nil {
		return nil, fmt.Errorf("session properties: %w", err)
	} else if value != "" {
		headers["X-Trino-Session"] = value
	}
	if value, err := encodeAssignments(state.Roles); err != nil {
		return nil, fmt.Errorf("roles: %w", err)
	} else if value != "" {
		headers["X-Trino-Role"] = value
	}
	if value, err := encodeAssignments(state.OriginalRoles); err != nil {
		return nil, fmt.Errorf("original roles: %w", err)
	} else if value != "" {
		headers["X-Trino-Original-Roles"] = value
	}
	if value, err := encodeAssignments(state.PreparedStatements); err != nil {
		return nil, fmt.Errorf("prepared statements: %w", err)
	} else if value != "" {
		headers["X-Trino-Prepared-Statement"] = value
	}
	if txn := strings.TrimSpace(state.TransactionID); txn != "" {
		if err := validateHeaderComponent("transaction id", txn, false); err != nil {
			return nil, err
		}
		headers["X-Trino-Transaction-Id"] = txn
	}
	if user := strings.TrimSpace(state.AuthorizationUser); user != "" {
		if err := validateHeaderComponent("authorization user", user, false); err != nil {
			return nil, err
		}
		original := firstNonEmpty(state.OriginalUser, cfg.User)
		if err := validateHeaderComponent("original user", original, false); err != nil {
			return nil, err
		}
		headers["X-Trino-User"] = user
		headers["X-Trino-Original-User"] = original
	}
	headers["X-Trino-Client-Capabilities"] = "PATH,SESSION_AUTHORIZATION"
	return headers, nil
}

func applyProtocolResponseHeaders(prior ProtocolState, headers http.Header) (ProtocolState, ProtocolStateDelta) {
	state := cloneProtocolState(prior)
	delta := ProtocolStateDelta{}
	if value := decodeHeaderValue(headers.Get("X-Trino-Set-Catalog")); value != "" {
		delta.SetCatalog = value
		state.Catalog = value
	}
	if value := decodeHeaderValue(headers.Get("X-Trino-Set-Schema")); value != "" {
		delta.SetSchema = value
		state.Schema = value
	}
	if value := decodeHeaderValue(headers.Get("X-Trino-Set-Path")); value != "" {
		delta.SetPath = value
		state.Path = value
	}
	delta.SetSession = parseHeaderAssignments(headers.Values("X-Trino-Set-Session"))
	for key, value := range delta.SetSession {
		state.SessionProperties[key] = value
	}
	delta.ClearSession = parseHeaderNames(headers.Values("X-Trino-Clear-Session"))
	for _, key := range delta.ClearSession {
		delete(state.SessionProperties, key)
	}
	delta.SetRoles = parseHeaderAssignments(headers.Values("X-Trino-Set-Role"))
	for key, value := range delta.SetRoles {
		state.Roles[key] = value
	}
	delta.SetOriginalRoles = parseHeaderAssignments(headers.Values("X-Trino-Set-Original-Roles"))
	for key, value := range delta.SetOriginalRoles {
		state.OriginalRoles[key] = value
	}
	delta.AddedPreparedStatements = parseHeaderAssignments(headers.Values("X-Trino-Added-Prepare"))
	for key, value := range delta.AddedPreparedStatements {
		state.PreparedStatements[key] = value
	}
	delta.DeallocatedPreparedStatements = parseHeaderNames(headers.Values("X-Trino-Deallocated-Prepare"))
	for _, key := range delta.DeallocatedPreparedStatements {
		delete(state.PreparedStatements, key)
	}
	if value := decodeHeaderValue(headers.Get("X-Trino-Started-Transaction-Id")); value != "" {
		delta.StartedTransactionID = value
		state.TransactionID = value
	}
	if headerPresent(headers, "X-Trino-Clear-Transaction-Id") {
		delta.ClearTransactionID = true
		state.TransactionID = ""
	}
	if value := decodeHeaderValue(headers.Get("X-Trino-Set-Authorization-User")); value != "" {
		delta.SetAuthorizationUser = value
		state.AuthorizationUser = value
	}
	if headerPresent(headers, "X-Trino-Reset-Authorization-User") {
		delta.ResetAuthorizationUser = true
		state.AuthorizationUser = ""
		state.OriginalUser = ""
	}
	return state, delta
}

func cloneProtocolState(in ProtocolState) ProtocolState {
	return ProtocolState{
		Catalog:            in.Catalog,
		Schema:             in.Schema,
		Path:               in.Path,
		SessionProperties:  cloneStringMap(in.SessionProperties),
		Roles:              cloneStringMap(in.Roles),
		OriginalRoles:      cloneStringMap(in.OriginalRoles),
		PreparedStatements: cloneStringMap(in.PreparedStatements),
		TransactionID:      in.TransactionID,
		AuthorizationUser:  in.AuthorizationUser,
		OriginalUser:       in.OriginalUser,
	}
}

func protocolStateWithOriginalUser(in ProtocolState, configuredUser string) ProtocolState {
	out := cloneProtocolState(in)
	if strings.TrimSpace(out.OriginalUser) == "" {
		out.OriginalUser = strings.TrimSpace(configuredUser)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func encodeHeaderList(values []string) (string, error) {
	clean := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		if err := validateHeaderComponent("value", value, true); err != nil {
			return "", err
		}
		seen[value] = true
		clean = append(clean, value)
	}
	sort.Strings(clean)
	return strings.Join(clean, ","), nil
}

func encodeAssignments(values map[string]string) (string, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	encoded := make([]string, 0, len(keys))
	for _, key := range keys {
		cleanKey := strings.TrimSpace(key)
		if cleanKey == "" {
			return "", errors.New("empty name")
		}
		if err := validateHeaderComponent("name", cleanKey, false); err != nil {
			return "", err
		}
		value := values[key]
		if strings.ContainsAny(value, "\r\n") {
			return "", errors.New("value contains a newline")
		}
		encoded = append(encoded, urlEncodeHeaderValue(cleanKey)+"="+urlEncodeHeaderValue(value))
	}
	return strings.Join(encoded, ","), nil
}

func validateHeaderComponent(label, value string, disallowComma bool) error {
	if strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s contains a newline", label)
	}
	if disallowComma && strings.ContainsAny(value, ",=") {
		return fmt.Errorf("%s contains a delimiter", label)
	}
	return nil
}

func urlEncodeHeaderValue(value string) string {
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func parseHeaderAssignments(values []string) map[string]string {
	out := map[string]string{}
	for _, item := range splitHeaderValues(values) {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		key = decodeHeaderValue(key)
		if key == "" {
			continue
		}
		out[key] = decodeHeaderValue(value)
	}
	return out
}

func parseHeaderNames(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, item := range splitHeaderValues(values) {
		name := decodeHeaderValue(item)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func splitHeaderValues(values []string) []string {
	out := make([]string, 0)
	for _, value := range values {
		for _, item := range strings.Split(value, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out = append(out, item)
			}
		}
	}
	return out
}

func decodeHeaderValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(value)
	if err != nil {
		return value
	}
	return strings.TrimSpace(decoded)
}

func validateNextURI(nextURI, baseURL string) error {
	next, err := url.Parse(strings.TrimSpace(nextURI))
	if err != nil || next.Scheme == "" || next.Host == "" {
		return errors.New("next_uri must be an absolute URL")
	}
	base, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return errors.New("configured Trino host is invalid")
	}
	if !strings.EqualFold(next.Scheme, base.Scheme) || !strings.EqualFold(next.Host, base.Host) {
		return errors.New("next_uri origin does not match the configured Trino host")
	}
	return nil
}

func headerPresent(headers http.Header, name string) bool {
	_, ok := headers[http.CanonicalHeaderKey(name)]
	return ok
}

// RetryAfterSeconds is useful to adapter packages that persist retry cadence
// without depending on the concrete UpstreamError type.
func RetryAfterSeconds(err error) (int64, bool) {
	var upstream UpstreamError
	if !errors.As(err, &upstream) {
		return 0, false
	}
	return int64(upstream.RetryAfter / time.Second), true
}
