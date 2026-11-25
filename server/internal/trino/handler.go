package trino

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sqlutil"
)

const trinoResponseBodyLimit = 8 << 20
const defaultTrinoUpstreamRequestTimeout = 15 * time.Second

type Handler struct {
	Settings *config.Store
	Secrets  *secrets.Store

	httpc                  *http.Client
	upstreamRequestTimeout time.Duration
}

func NewHandler(settings *config.Store, secretsStore *secrets.Store) *Handler {
	return &Handler{
		Settings: settings,
		Secrets:  secretsStore,
		httpc: &http.Client{
			Timeout: 0, // rely on request context timeouts
		},
		upstreamRequestTimeout: defaultTrinoUpstreamRequestTimeout,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/trino/status"):
		h.handleStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/test"):
		h.handleTest(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/preflight"):
		h.handlePreflight(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/query"):
		h.handleQuery(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/catalogs"):
		h.handleCatalogs(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/schemas"):
		h.handleSchemas(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/tables"):
		h.handleTables(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/trino/table-schema"):
		h.handleTableSchema(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	settings := h.settingsSnapshot()
	authMode := "none"
	userConfigured := settings.User != ""
	authErr := ""
	if mode, sec, err := h.statusAuthMode(); err == nil {
		authMode = mode
		switch mode {
		case "basic":
			userConfigured = sec != nil && strings.TrimSpace(sec.Username) != ""
		case "jwt", "none":
			userConfigured = settings.User != ""
		}
	} else {
		authErr = err.Error()
	}

	hostConfigured := settings.Host != ""
	hostValid := false
	hostValidationMessage := ""
	normalizedHost := settings.Host
	if hostConfigured {
		norm, err := normalizeTrinoHost(settings.Host, authMode)
		if err != nil {
			hostValidationMessage = err.Error()
		} else {
			hostValid = true
			normalizedHost = norm
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":                authMode,
		"host_configured":          hostConfigured,
		"catalog_configured":       settings.Catalog != "",
		"schema_configured":        settings.Schema != "",
		"user_configured":          userConfigured,
		"host":                     nilIfEmpty(normalizedHost),
		"catalog":                  nilIfEmpty(settings.Catalog),
		"schema":                   nilIfEmpty(settings.Schema),
		"user":                     nilIfEmpty(settings.User),
		"source":                   nilIfEmpty(firstNonEmpty(settings.Source, defaultTrinoSource)),
		"host_valid":               hostValid,
		"host_validation_message":  nilIfEmpty(hostValidationMessage),
		"host_validation_required": true,
		"auth_error":               nilIfEmpty(authErr),
	})
}

func (h *Handler) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	var body struct {
		Host      string `json:"host,omitempty"`
		Catalog   string `json:"catalog,omitempty"`
		Schema    string `json:"schema,omitempty"`
		Source    string `json:"source,omitempty"`
		TrinoHost string `json:"trinoHost,omitempty"`
		TrinoCat  string `json:"trinoCatalog,omitempty"`
		TrinoSch  string `json:"trinoSchema,omitempty"`
		TrinoSrc  string `json:"trinoSource,omitempty"`
	}
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeTrinoError(w, http.StatusBadRequest, "Invalid JSON body")
		h.persistTestStatus("", "", "", "Invalid JSON body", false)
		return
	}

	overrides := authRequestOverrides{
		Host:    firstNonEmpty(body.Host, body.TrinoHost),
		Catalog: firstNonEmpty(body.Catalog, body.TrinoCat),
		Schema:  firstNonEmpty(body.Schema, body.TrinoSch),
		Source:  firstNonEmpty(body.Source, body.TrinoSrc),
	}
	cfg, err := h.authConfig(overrides)
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		h.persistTestStatus(overrides.Host, overrides.Catalog, overrides.Schema, err.Error(), false)
		return
	}

	result, err := h.executeSQL(ctx, cfg, "SELECT 1", 10)
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		h.persistTestStatus(cfg.BaseURL, cfg.Catalog, cfg.Schema, err.Error(), false)
		return
	}

	message := "Connected."
	h.persistTestStatus(cfg.BaseURL, cfg.Catalog, cfg.Schema, message, true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"message":      message,
		"query_id":     nilIfEmpty(result.QueryID),
		"rows_visible": len(result.Rows),
	})
}

func (h *Handler) persistTestStatus(host, catalog, schema, message string, ok bool) {
	if h == nil || h.Settings == nil {
		return
	}
	s := h.Settings.Get()

	normHost := strings.TrimSpace(host)
	cat := strings.TrimSpace(catalog)
	sch := strings.TrimSpace(schema)
	msg := strings.TrimSpace(message)

	if s.DataSources == nil {
		off := false
		s.DataSources = &config.DatasourceConnections{
			BigQuery:   &config.DatasourceConnectionState{Enabled: &off},
			Databricks: &config.DatasourceConnectionState{Enabled: &off},
			Trino:      &config.DatasourceConnectionState{Enabled: &off},
		}
	}
	if s.DataSources.Trino == nil {
		off := false
		s.DataSources.Trino = &config.DatasourceConnectionState{Enabled: &off}
	} else if s.DataSources.Trino.Enabled == nil {
		off := false
		s.DataSources.Trino.Enabled = &off
	}

	ds := s.DataSources.Trino
	now := time.Now().UnixMilli()
	ds.LastCheckedAt = &now
	ds.LastCheckOk = &ok
	if msg == "" {
		ds.LastCheckMessage = nil
	} else {
		ds.LastCheckMessage = &msg
	}
	if normHost == "" {
		ds.LastHost = nil
	} else {
		ds.LastHost = &normHost
	}
	if cat == "" {
		ds.LastProjectID = nil
	} else {
		ds.LastProjectID = &cat
	}
	if sch == "" {
		ds.LastWarehouseID = nil
	} else {
		ds.LastWarehouseID = &sch
	}

	_ = h.Settings.Save(s)
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	var body struct {
		SQL       string `json:"sql"`
		Catalog   string `json:"catalog,omitempty"`
		Schema    string `json:"schema,omitempty"`
		Host      string `json:"host,omitempty"`
		TrinoHost string `json:"trinoHost,omitempty"`
		TrinoCat  string `json:"trinoCatalog,omitempty"`
		TrinoSch  string `json:"trinoSchema,omitempty"`
	}
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Invalid JSON body",
			},
		})
		return
	}

	sqlText := strings.TrimSpace(body.SQL)
	if sqlText == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Missing sql",
			},
		})
		return
	}
	if !sqlutil.IsReadOnlySQL(sqlText) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Only read-only SELECT/WITH queries are allowed",
			},
		})
		return
	}

	cfg, err := h.authConfig(authRequestOverrides{
		Host:    firstNonEmpty(body.Host, body.TrinoHost),
		Catalog: firstNonEmpty(body.Catalog, body.TrinoCat),
		Schema:  firstNonEmpty(body.Schema, body.TrinoSch),
	})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": err.Error(),
			},
		})
		return
	}

	result, err := h.executeSQL(ctx, cfg, "EXPLAIN "+sqlText, 200)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": err.Error(),
			},
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"query_id":     nilIfEmpty(result.QueryID),
		"rows_visible": len(result.Rows),
	})
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// The request context bounds only this legacy synchronous wait. It is not
	// a provider cancellation signal; durable execution uses the typed
	// SubmitAsync/PollAsync lifecycle.
	ctx := r.Context()

	var body struct {
		SQL       string `json:"sql"`
		Catalog   string `json:"catalog,omitempty"`
		Schema    string `json:"schema,omitempty"`
		Host      string `json:"host,omitempty"`
		Source    string `json:"source,omitempty"`
		TrinoHost string `json:"trinoHost,omitempty"`
	}
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeTrinoError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	sqlText := strings.TrimSpace(body.SQL)
	if sqlText == "" {
		writeTrinoError(w, http.StatusBadRequest, "Missing sql")
		return
	}
	if !sqlutil.IsReadOnlySQL(sqlText) {
		writeTrinoError(w, http.StatusBadRequest, "Only read-only SELECT/WITH queries are allowed")
		return
	}

	cfg, err := h.authConfig(authRequestOverrides{
		Host:    firstNonEmpty(body.Host, body.TrinoHost),
		Catalog: body.Catalog,
		Schema:  body.Schema,
		Source:  body.Source,
	})
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	result, err := h.executeSQL(ctx, cfg, sqlText, h.queryRowLimit())
	if err != nil {
		if qerr, ok := err.(queryExecutionError); ok {
			payload := map[string]any{
				"ok": false,
				"error": map[string]any{
					"message": qerr.Error(),
				},
			}
			if len(qerr.PartialRows) > 0 {
				payload["partial_rows"] = qerr.PartialRows
			}
			if qerr.QueryID != "" {
				payload["query_metadata"] = map[string]any{
					"provider": "trino",
					"query_id": qerr.QueryID,
					"schema":   qerr.Schema,
				}
			}
			writeJSON(w, qerr.StatusCode, payload)
			return
		}
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rows": result.Rows,
		"query_metadata": map[string]any{
			"provider":    "trino",
			"duration_ms": result.DurationMs,
			"query_id":    result.QueryID,
			"info_uri":    nilIfEmpty(result.InfoURI),
			"schema":      result.Schema,
		},
	})
}

func (h *Handler) handleCatalogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cfg, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}
	result, err := h.executeSQL(ctx, cfg, "SHOW CATALOGS", 1000)
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	catalogs := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(firstColumnString(row, result.Columns))
		if name == "" {
			continue
		}
		catalogs = append(catalogs, map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"catalogs": catalogs})
}

func (h *Handler) handleSchemas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cfg, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	catalogName := strings.TrimSpace(r.URL.Query().Get("catalog_name"))
	if catalogName == "" {
		catalogName = strings.TrimSpace(cfg.Catalog)
	}
	if catalogName == "" {
		writeTrinoError(w, http.StatusBadRequest, "Missing catalog_name")
		return
	}
	catalogExpr, err := quoteIdentifier(catalogName)
	if err != nil {
		writeTrinoError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.executeSQL(ctx, cfg, "SHOW SCHEMAS FROM "+catalogExpr, 2000)
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}
	schemas := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(firstColumnString(row, result.Columns))
		if name == "" {
			continue
		}
		schemas = append(schemas, map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"schemas": schemas})
}

func (h *Handler) handleTables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cfg, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	catalogName := strings.TrimSpace(r.URL.Query().Get("catalog_name"))
	schemaName := strings.TrimSpace(r.URL.Query().Get("schema_name"))
	if catalogName == "" {
		catalogName = strings.TrimSpace(cfg.Catalog)
	}
	if schemaName == "" {
		schemaName = strings.TrimSpace(cfg.Schema)
	}
	if catalogName == "" || schemaName == "" {
		writeTrinoError(w, http.StatusBadRequest, "Missing catalog_name or schema_name")
		return
	}
	catalogExpr, err := quoteIdentifier(catalogName)
	if err != nil {
		writeTrinoError(w, http.StatusBadRequest, err.Error())
		return
	}
	schemaExpr, err := quoteIdentifier(schemaName)
	if err != nil {
		writeTrinoError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.executeSQL(ctx, cfg, "SHOW TABLES FROM "+catalogExpr+"."+schemaExpr, 5000)
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}
	tables := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(firstColumnString(row, result.Columns))
		if name == "" {
			continue
		}
		tables = append(tables, map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tables": tables})
}

func (h *Handler) handleTableSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cfg, err := h.authConfig(authRequestOverrides{})
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	q := r.URL.Query()
	catalogName := strings.TrimSpace(q.Get("catalog_name"))
	schemaName := strings.TrimSpace(q.Get("schema_name"))
	tableName := strings.TrimSpace(q.Get("table_name"))
	fullName := strings.TrimSpace(q.Get("full_name"))
	if fullName != "" && (catalogName == "" || schemaName == "" || tableName == "") {
		parts := strings.Split(fullName, ".")
		if len(parts) == 3 {
			catalogName = strings.TrimSpace(parts[0])
			schemaName = strings.TrimSpace(parts[1])
			tableName = strings.TrimSpace(parts[2])
		}
	}
	if catalogName == "" {
		catalogName = strings.TrimSpace(cfg.Catalog)
	}
	if schemaName == "" {
		schemaName = strings.TrimSpace(cfg.Schema)
	}
	if catalogName == "" || schemaName == "" || tableName == "" {
		writeTrinoError(w, http.StatusBadRequest, "Missing catalog_name, schema_name, or table_name")
		return
	}

	catalogExpr, err := quoteIdentifier(catalogName)
	if err != nil {
		writeTrinoError(w, http.StatusBadRequest, err.Error())
		return
	}
	schemaExpr, err := quoteIdentifier(schemaName)
	if err != nil {
		writeTrinoError(w, http.StatusBadRequest, err.Error())
		return
	}
	tableExpr, err := quoteIdentifier(tableName)
	if err != nil {
		writeTrinoError(w, http.StatusBadRequest, err.Error())
		return
	}

	sqlText := "DESCRIBE " + catalogExpr + "." + schemaExpr + "." + tableExpr
	result, err := h.executeSQL(ctx, cfg, sqlText, 1000)
	if err != nil {
		writeTrinoError(w, httpStatusForErr(err), err.Error())
		return
	}

	schema := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(rowValueCaseInsensitive(row, "column"))
		if name == "" {
			name = strings.TrimSpace(firstColumnString(row, result.Columns))
		}
		if name == "" {
			continue
		}
		typ := strings.TrimSpace(rowValueCaseInsensitive(row, "type"))
		comment := strings.TrimSpace(rowValueCaseInsensitive(row, "comment"))
		schema = append(schema, map[string]any{
			"name":    name,
			"type":    nilIfEmpty(typ),
			"comment": nilIfEmpty(comment),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"table":  catalogName + "." + schemaName + "." + tableName,
		"schema": schema,
	})
}

type trinoStatementResponse struct {
	ID               string                 `json:"id"`
	InfoURI          string                 `json:"infoUri,omitempty"`
	NextURI          string                 `json:"nextUri,omitempty"`
	PartialCancelURI string                 `json:"partialCancelUri,omitempty"`
	Columns          []trinoStatementColumn `json:"columns,omitempty"`
	Data             [][]any                `json:"data,omitempty"`
	Error            *trinoStatementError   `json:"error,omitempty"`
	Stats            QueryStats             `json:"stats,omitempty"`
	Warnings         []QueryWarning         `json:"warnings,omitempty"`
}

type trinoQueryResult struct {
	Rows       []map[string]any
	Columns    []trinoStatementColumn
	Schema     []map[string]any
	QueryID    string
	InfoURI    string
	DurationMs int64
}

type queryExecutionError struct {
	StatusCode  int
	Message     string
	QueryID     string
	Schema      []map[string]any
	PartialRows []map[string]any
}

func (e queryExecutionError) Error() string {
	return e.Message
}

type QueryResult struct {
	Rows     []map[string]any `json:"rows,omitempty"`
	Schema   []map[string]any `json:"schema,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
}

type AsyncQueryResult struct {
	Completed bool             `json:"completed"`
	Rows      []map[string]any `json:"rows,omitempty"`
	Schema    []map[string]any `json:"schema,omitempty"`
	Metadata  map[string]any   `json:"metadata,omitempty"`

	QueryID   string `json:"query_id,omitempty"`
	NextURI   string `json:"next_uri,omitempty"`
	QueryURL  string `json:"query_url,omitempty"`
	CancelURI string `json:"cancel_uri,omitempty"`
}

const (
	defaultAdaptiveWaitFirst  = 300 * time.Millisecond
	defaultAdaptiveWaitSecond = 800 * time.Millisecond
)

type trinoAsyncState struct {
	QueryID   string
	InfoURI   string
	NextURI   string
	CancelURI string
	Columns   []trinoStatementColumn
	Rows      []map[string]any
}

func (h *Handler) SubmitQuery(ctx context.Context, sqlText, catalog, schema, source string, initialWait time.Duration) (*AsyncQueryResult, error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return nil, errors.New("missing sql")
	}
	cfg, err := h.authConfig(authRequestOverrides{
		Catalog: catalog,
		Schema:  schema,
		Source:  source,
	})
	if err != nil {
		return nil, err
	}
	statementURL := strings.TrimRight(cfg.BaseURL, "/") + "/v1/statement"
	headers := h.trinoHeaders(cfg)
	resp, err := h.doTrinoStatement(ctx, http.MethodPost, statementURL, headers, []byte(sqlText))
	if err != nil {
		return nil, err
	}
	state, failed, failMsg := applyTrinoAsyncResponse(trinoAsyncState{}, resp)
	if failed {
		return nil, errors.New(failMsg)
	}
	if strings.TrimSpace(state.NextURI) == "" {
		return &AsyncQueryResult{
			Completed: true,
			Rows:      state.Rows,
			Schema:    trinoSchema(state.Columns),
			Metadata:  asyncResultMetadata(state, true),
			QueryID:   state.QueryID,
			NextURI:   "",
			QueryURL:  firstNonEmpty(state.InfoURI, state.NextURI),
			CancelURI: state.CancelURI,
		}, nil
	}

	firstWait := initialWait
	if firstWait <= 0 {
		firstWait = defaultAdaptiveWaitFirst
	}
	if firstWait < 50*time.Millisecond {
		firstWait = 50 * time.Millisecond
	}
	done, progressed, err := h.waitForQuery(ctx, cfg, state, firstWait)
	if err != nil {
		return nil, err
	}
	state = progressed
	if done {
		return &AsyncQueryResult{
			Completed: true,
			Rows:      state.Rows,
			Schema:    trinoSchema(state.Columns),
			Metadata:  asyncResultMetadata(state, true),
			QueryID:   state.QueryID,
			NextURI:   "",
			QueryURL:  firstNonEmpty(state.InfoURI, state.NextURI),
			CancelURI: state.CancelURI,
		}, nil
	}

	done, progressed, err = h.waitForQuery(ctx, cfg, state, defaultAdaptiveWaitSecond)
	if err != nil {
		return nil, err
	}
	state = progressed
	if done {
		return &AsyncQueryResult{
			Completed: true,
			Rows:      state.Rows,
			Schema:    trinoSchema(state.Columns),
			Metadata:  asyncResultMetadata(state, true),
			QueryID:   state.QueryID,
			NextURI:   "",
			QueryURL:  firstNonEmpty(state.InfoURI, state.NextURI),
			CancelURI: state.CancelURI,
		}, nil
	}

	return &AsyncQueryResult{
		Completed: false,
		Rows:      state.Rows,
		Schema:    trinoSchema(state.Columns),
		Metadata:  asyncResultMetadata(state, false),
		QueryID:   state.QueryID,
		NextURI:   state.NextURI,
		QueryURL:  firstNonEmpty(state.InfoURI, state.NextURI),
		CancelURI: state.CancelURI,
	}, nil
}

func (h *Handler) PollQuery(ctx context.Context, queryID, nextURI, catalog, schema, source string) (*AsyncQueryResult, error) {
	nextURI = strings.TrimSpace(nextURI)
	if nextURI == "" {
		return nil, errors.New("next_uri is required")
	}
	cfg, err := h.authConfig(authRequestOverrides{
		Catalog: catalog,
		Schema:  schema,
		Source:  source,
	})
	if err != nil {
		return nil, err
	}
	headers := h.trinoHeaders(cfg)
	resp, err := h.doTrinoStatement(ctx, http.MethodGet, nextURI, headers, nil)
	if err != nil {
		return nil, err
	}
	initial := trinoAsyncState{
		QueryID: strings.TrimSpace(queryID),
		NextURI: nextURI,
	}
	state, failed, failMsg := applyTrinoAsyncResponse(initial, resp)
	if failed {
		return nil, errors.New(failMsg)
	}
	completed := strings.TrimSpace(state.NextURI) == ""
	return &AsyncQueryResult{
		Completed: completed,
		Rows:      state.Rows,
		Schema:    trinoSchema(state.Columns),
		Metadata:  asyncResultMetadata(state, completed),
		QueryID:   state.QueryID,
		NextURI:   state.NextURI,
		QueryURL:  firstNonEmpty(state.InfoURI, state.NextURI),
		CancelURI: state.CancelURI,
	}, nil
}

func (h *Handler) CancelQuery(ctx context.Context, queryID, nextURI, catalog, schema, source string) error {
	cfg, err := h.authConfig(authRequestOverrides{
		Catalog: catalog,
		Schema:  schema,
		Source:  source,
	})
	if err != nil {
		return err
	}
	return h.cancelQueryWithContext(ctx, cfg, strings.TrimSpace(nextURI), strings.TrimSpace(queryID))
}

func (h *Handler) waitForQuery(ctx context.Context, cfg trinoAuthConfig, state trinoAsyncState, wait time.Duration) (bool, trinoAsyncState, error) {
	if wait <= 0 || strings.TrimSpace(state.NextURI) == "" {
		return false, state, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	headers := h.trinoHeaders(cfg)
	for strings.TrimSpace(state.NextURI) != "" {
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return false, state, ctx.Err()
			}
			return false, state, nil
		}
		resp, err := h.doTrinoStatement(waitCtx, http.MethodGet, state.NextURI, headers, nil)
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return false, state, nil
			}
			return false, state, err
		}
		nextState, failed, failMsg := applyTrinoAsyncResponse(state, resp)
		state = nextState
		if failed {
			return false, state, errors.New(failMsg)
		}
		if strings.TrimSpace(state.NextURI) == "" {
			return true, state, nil
		}
		if err := sleepWithContext(waitCtx, 120*time.Millisecond); err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return false, state, nil
			}
			return false, state, err
		}
	}
	return true, state, nil
}

func applyTrinoAsyncResponse(state trinoAsyncState, resp trinoStatementResponse) (trinoAsyncState, bool, string) {
	if strings.TrimSpace(state.QueryID) == "" {
		state.QueryID = strings.TrimSpace(resp.ID)
	}
	if strings.TrimSpace(state.InfoURI) == "" {
		state.InfoURI = strings.TrimSpace(resp.InfoURI)
	}
	if len(state.Columns) == 0 && len(resp.Columns) > 0 {
		state.Columns = append(state.Columns, resp.Columns...)
	}
	state.Rows = appendRowsLimited(state.Rows, state.Columns, resp.Data, 0)
	state.NextURI = strings.TrimSpace(resp.NextURI)
	// Full-query cancellation is DELETE on the current nextUri. The
	// partialCancelUri only cancels a leaf stage and must not escape as the
	// normal cancellation handle.
	state.CancelURI = strings.TrimSpace(resp.NextURI)
	if resp.Error != nil {
		return state, true, statementErrorMessage(resp.Error)
	}
	return state, false, ""
}

func asyncResultMetadata(state trinoAsyncState, completed bool) map[string]any {
	queryURL := firstNonEmpty(state.InfoURI, state.NextURI)
	out := map[string]any{
		"provider":         "trino",
		"query_id":         strings.TrimSpace(state.QueryID),
		"next_uri":         strings.TrimSpace(state.NextURI),
		"provider_job_id":  strings.TrimSpace(state.QueryID),
		"provider_job_url": strings.TrimSpace(queryURL),
		"url":              strings.TrimSpace(queryURL),
		"state":            "RUNNING",
		"terminal_status":  "",
	}
	if completed {
		out["state"] = "FINISHED"
		out["terminal_status"] = "completed"
		out["next_uri"] = ""
	}
	if strings.TrimSpace(state.CancelURI) != "" {
		out["cancel_uri"] = strings.TrimSpace(state.CancelURI)
	}
	if schema := trinoSchema(state.Columns); len(schema) > 0 {
		out["schema"] = schema
	}
	return out
}

func (h *Handler) executeSQL(ctx context.Context, cfg trinoAuthConfig, sqlText string, maxRows int) (trinoQueryResult, error) {
	start := time.Now()
	statementURL := strings.TrimRight(cfg.BaseURL, "/") + "/v1/statement"
	headers := h.trinoHeaders(cfg)

	resp, err := h.doTrinoStatement(ctx, http.MethodPost, statementURL, headers, []byte(strings.TrimSpace(sqlText)))
	if err != nil {
		return trinoQueryResult{}, err
	}

	queryID := strings.TrimSpace(resp.ID)
	infoURI := strings.TrimSpace(resp.InfoURI)
	currentNextURI := strings.TrimSpace(resp.NextURI)
	columns := make([]trinoStatementColumn, 0, len(resp.Columns))
	rows := make([]map[string]any, 0)

	if len(resp.Columns) > 0 {
		columns = append(columns, resp.Columns...)
	}
	rows = appendRowsLimited(rows, columns, resp.Data, maxRows)
	if resp.Error != nil {
		message := statementErrorMessage(resp.Error)
		return trinoQueryResult{}, queryExecutionError{
			StatusCode:  http.StatusBadGateway,
			Message:     message,
			QueryID:     queryID,
			Schema:      trinoSchema(columns),
			PartialRows: rows,
		}
	}

	for currentNextURI != "" {
		if ctx.Err() != nil {
			return trinoQueryResult{}, ctx.Err()
		}

		nextResp, err := h.doTrinoStatement(ctx, http.MethodGet, currentNextURI, headers, nil)
		if err != nil {
			return trinoQueryResult{}, err
		}
		if queryID == "" {
			queryID = strings.TrimSpace(nextResp.ID)
		}
		if infoURI == "" {
			infoURI = strings.TrimSpace(nextResp.InfoURI)
		}
		if len(columns) == 0 && len(nextResp.Columns) > 0 {
			columns = append(columns, nextResp.Columns...)
		}
		rows = appendRowsLimited(rows, columns, nextResp.Data, maxRows)
		if nextResp.Error != nil {
			message := statementErrorMessage(nextResp.Error)
			return trinoQueryResult{}, queryExecutionError{
				StatusCode:  http.StatusBadGateway,
				Message:     message,
				QueryID:     queryID,
				Schema:      trinoSchema(columns),
				PartialRows: rows,
			}
		}
		currentNextURI = strings.TrimSpace(nextResp.NextURI)
	}

	return trinoQueryResult{
		Rows:       rows,
		Columns:    columns,
		Schema:     trinoSchema(columns),
		QueryID:    queryID,
		InfoURI:    infoURI,
		DurationMs: time.Since(start).Milliseconds(),
	}, nil
}

func (h *Handler) cancelQuery(cfg trinoAuthConfig, cancelURI, queryID string) {
	cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	headers := h.trinoHeaders(cfg)

	cancelURI = strings.TrimSpace(cancelURI)
	if cancelURI != "" {
		_, _, err := h.doTrinoRaw(cancelCtx, http.MethodDelete, cancelURI, headers, nil, false)
		if err == nil {
			return
		}
	}

	queryID = strings.TrimSpace(queryID)
	if queryID == "" {
		return
	}
	fallback := strings.TrimRight(cfg.BaseURL, "/") + "/v1/query/" + url.PathEscape(queryID)
	_, _, _ = h.doTrinoRaw(cancelCtx, http.MethodDelete, fallback, headers, nil, false)
}

func (h *Handler) cancelQueryWithContext(ctx context.Context, cfg trinoAuthConfig, cancelURI, queryID string) error {
	headers := h.trinoHeaders(cfg)
	cancelURI = strings.TrimSpace(cancelURI)
	queryID = strings.TrimSpace(queryID)

	var firstErr error
	if cancelURI != "" {
		if _, _, err := h.doTrinoRaw(ctx, http.MethodDelete, cancelURI, headers, nil, false); err == nil {
			return nil
		} else {
			firstErr = err
		}
	}

	if queryID != "" {
		fallback := strings.TrimRight(cfg.BaseURL, "/") + "/v1/query/" + url.PathEscape(queryID)
		if _, _, err := h.doTrinoRaw(ctx, http.MethodDelete, fallback, headers, nil, false); err == nil {
			return nil
		} else if firstErr == nil {
			firstErr = err
		}
	}

	if firstErr != nil {
		return firstErr
	}
	return errors.New("query_id or next_uri is required")
}

func (h *Handler) doTrinoStatement(ctx context.Context, method, requestURL string, headers map[string]string, body []byte) (trinoStatementResponse, error) {
	out, _, err := h.doTrinoStatementWithHeaders(ctx, method, requestURL, headers, body, true)
	return out, err
}

func (h *Handler) doTrinoStatementWithHeaders(ctx context.Context, method, requestURL string, headers map[string]string, body []byte, allowRetry bool) (trinoStatementResponse, http.Header, error) {
	respBody, _, responseHeaders, err := h.doTrinoRawResponse(ctx, method, requestURL, headers, body, allowRetry)
	if err != nil {
		return trinoStatementResponse{}, responseHeaders, err
	}
	if len(respBody) == 0 {
		return trinoStatementResponse{}, responseHeaders, nil
	}
	var out trinoStatementResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return trinoStatementResponse{}, responseHeaders, httpError{status: http.StatusBadGateway, msg: "Invalid JSON from Trino"}
	}
	return out, responseHeaders, nil
}

func (h *Handler) doTrinoRaw(ctx context.Context, method, requestURL string, headers map[string]string, body []byte, allowRetry bool) ([]byte, int, error) {
	payload, status, _, err := h.doTrinoRawResponse(ctx, method, requestURL, headers, body, allowRetry)
	return payload, status, err
}

func (h *Handler) doTrinoRawResponse(ctx context.Context, method, requestURL string, headers map[string]string, body []byte, allowRetry bool) ([]byte, int, http.Header, error) {
	client := h.httpc
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	attempts := 1
	if allowRetry {
		attempts = 4
	}
	for attempt := 0; attempt < attempts; attempt++ {
		var bodyReader io.Reader
		if body != nil {
			bodyReader = bytes.NewReader(body)
		}
		reqCtx := ctx
		cancelReq := func() {}
		if timeout := h.requestTimeout(); timeout > 0 {
			reqCtx, cancelReq = context.WithTimeout(ctx, timeout)
		}
		req, err := http.NewRequestWithContext(reqCtx, method, requestURL, bodyReader)
		if err != nil {
			cancelReq()
			return nil, 0, nil, err
		}
		for key, value := range headers {
			if strings.TrimSpace(value) == "" {
				continue
			}
			req.Header.Set(key, value)
		}
		if body != nil {
			req.Header.Set("Content-Type", "text/plain; charset=utf-8")
		}

		resp, err := client.Do(req)
		if err != nil {
			if reqCtx.Err() != nil {
				cancelReq()
				if ctx.Err() != nil {
					return nil, 0, nil, ctx.Err()
				}
				return nil, 0, nil, reqCtx.Err()
			}
			cancelReq()
			return nil, 0, nil, UpstreamError{StatusCode: http.StatusBadGateway, Message: fmt.Sprintf("failed to reach Trino: %v", err), Retryable: true}
		}
		responseHeaders := resp.Header.Clone()
		payload, readErr := io.ReadAll(io.LimitReader(resp.Body, trinoResponseBodyLimit))
		_ = resp.Body.Close()
		if readErr != nil {
			if reqCtx.Err() != nil {
				cancelReq()
				if ctx.Err() != nil {
					return nil, 0, responseHeaders, ctx.Err()
				}
				return nil, 0, responseHeaders, reqCtx.Err()
			}
			cancelReq()
			return nil, 0, responseHeaders, readErr
		}
		cancelReq()

		status := resp.StatusCode
		if status >= 200 && status < 300 {
			return payload, status, responseHeaders, nil
		}
		msg := trinoHTTPMessage(status, payload)
		retryAfter := retryAfterDuration(resp.Header.Get("Retry-After"), attempt)
		if allowRetry && retryableStatus(status) && attempt+1 < attempts {
			if err := sleepWithContext(ctx, retryAfter); err != nil {
				return nil, 0, responseHeaders, err
			}
			continue
		}
		return payload, status, responseHeaders, UpstreamError{
			StatusCode: status,
			Message:    msg,
			Retryable:  retryableStatus(status),
			RetryAfter: retryAfter,
		}
	}
	return nil, 0, nil, UpstreamError{StatusCode: http.StatusBadGateway, Message: "Trino request failed", Retryable: true}
}

func (h *Handler) requestTimeout() time.Duration {
	if h == nil || h.upstreamRequestTimeout <= 0 {
		return defaultTrinoUpstreamRequestTimeout
	}
	return h.upstreamRequestTimeout
}

func (h *Handler) trinoHeaders(cfg trinoAuthConfig) map[string]string {
	headers := map[string]string{
		"X-Trino-User":      cfg.User,
		"X-Trino-Time-Zone": "UTC",
		"X-Trino-Source":    firstNonEmpty(cfg.Source, defaultTrinoSource),
	}
	if cfg.Authorization != "" {
		headers["Authorization"] = cfg.Authorization
	}
	if strings.TrimSpace(cfg.Catalog) != "" {
		headers["X-Trino-Catalog"] = strings.TrimSpace(cfg.Catalog)
	}
	if strings.TrimSpace(cfg.Schema) != "" {
		headers["X-Trino-Schema"] = strings.TrimSpace(cfg.Schema)
	}
	return headers
}

func appendRowsLimited(existing []map[string]any, columns []trinoStatementColumn, data [][]any, maxRows int) []map[string]any {
	if len(data) == 0 {
		return existing
	}
	for _, raw := range data {
		if maxRows > 0 && len(existing) >= maxRows {
			return existing
		}
		row := map[string]any{}
		for i, value := range raw {
			key := fmt.Sprintf("col_%d", i)
			if i < len(columns) {
				name := strings.TrimSpace(columns[i].Name)
				if name != "" {
					key = name
				}
			}
			row[key] = value
		}
		existing = append(existing, row)
	}
	return existing
}

func trinoSchema(columns []trinoStatementColumn) []map[string]any {
	out := make([]map[string]any, 0, len(columns))
	for _, col := range columns {
		name := strings.TrimSpace(col.Name)
		if name == "" {
			continue
		}
		entry := map[string]any{"name": name}
		if typ := strings.TrimSpace(col.Type); typ != "" {
			entry["type"] = typ
		}
		out = append(out, entry)
	}
	return out
}

func statementErrorMessage(e *trinoStatementError) string {
	if e == nil {
		return "Trino query failed"
	}
	message := strings.TrimSpace(e.Message)
	if message == "" {
		message = "Trino query failed"
	}
	if name := strings.TrimSpace(e.ErrorName); name != "" {
		return name + ": " + message
	}
	return message
}

func trinoHTTPMessage(status int, body []byte) string {
	if len(body) > 0 {
		var parsed struct {
			Message string               `json:"message"`
			Error   *trinoStatementError `json:"error"`
		}
		if err := json.Unmarshal(body, &parsed); err == nil {
			if parsed.Error != nil {
				if msg := statementErrorMessage(parsed.Error); strings.TrimSpace(msg) != "" {
					return msg
				}
			}
			if msg := strings.TrimSpace(parsed.Message); msg != "" {
				return msg
			}
		}
		if msg := strings.TrimSpace(string(body)); msg != "" {
			return msg
		}
	}
	return fmt.Sprintf("Trino request failed (HTTP %d)", status)
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryAfterDuration(headerValue string, attempt int) time.Duration {
	fallback := time.Duration(attempt+1) * 250 * time.Millisecond
	raw := strings.TrimSpace(headerValue)
	if raw == "" {
		return fallback
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(raw); err == nil {
		d := time.Until(when)
		if d > 0 {
			return d
		}
	}
	return fallback
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h *Handler) queryRowLimit() int {
	if h == nil || h.Settings == nil {
		return config.DefaultDatabaseQueryRowLimit
	}
	s := h.Settings.Get()
	if s.DatabaseQueryRowLimit == nil {
		return config.DefaultDatabaseQueryRowLimit
	}
	limit := *s.DatabaseQueryRowLimit
	if limit < 1 {
		return config.DefaultDatabaseQueryRowLimit
	}
	if limit > config.MaxDatabaseQueryRowLimit {
		return config.MaxDatabaseQueryRowLimit
	}
	return limit
}

func firstColumnString(row map[string]any, columns []trinoStatementColumn) string {
	if len(columns) > 0 {
		key := strings.TrimSpace(columns[0].Name)
		if key != "" {
			if value, ok := row[key]; ok {
				return fmt.Sprintf("%v", value)
			}
		}
	}
	for _, value := range row {
		return fmt.Sprintf("%v", value)
	}
	return ""
}

func rowValueCaseInsensitive(row map[string]any, key string) string {
	want := strings.ToLower(strings.TrimSpace(key))
	for k, value := range row {
		if strings.ToLower(strings.TrimSpace(k)) == want {
			return fmt.Sprintf("%v", value)
		}
	}
	return ""
}

func quoteIdentifier(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("identifier is required")
	}
	if strings.ContainsAny(trimmed, ";\n\r\t") {
		return "", errors.New("identifier contains invalid characters")
	}
	return `"` + strings.ReplaceAll(trimmed, `"`, `""`) + `"`, nil
}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

func writeTrinoError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]any{
			"message": strings.TrimSpace(message),
		},
	})
}

func httpStatusForErr(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var herr httpError
	if errors.As(err, &herr) {
		if herr.status >= 400 {
			return herr.status
		}
	}
	var upstream UpstreamError
	if errors.As(err, &upstream) && upstream.StatusCode >= 400 {
		return upstream.StatusCode
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout
	}
	return http.StatusInternalServerError
}

func decodeJSONLimit(r io.Reader, dst any, limit int64) error {
	if limit <= 0 {
		limit = 1 << 20
	}
	dec := json.NewDecoder(io.LimitReader(r, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected additional JSON content")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(payload)
}

func nilIfEmpty(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}
