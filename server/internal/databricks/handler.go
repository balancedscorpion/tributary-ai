package databricks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sqlutil"
)

type Handler struct {
	Settings *config.Store
	Secrets  *secrets.Store

	httpc *http.Client

	tokenMu     sync.Mutex
	cachedToken string
	cachedUntil time.Time
	cachedKey   string
	refreshing  bool
	refreshWait chan struct{}

	oauthMu      sync.Mutex
	oauthFlows   map[string]*oauthPendingFlow
	oauthServers map[string][]*http.Server
}

const (
	databricksCatalogListPageSize = 100
	databricksSchemaListPageSize  = 100
	databricksTableListPageSize   = 50
	databricksResponseBodyLimit   = 8 << 20
)

func NewHandler(settings *config.Store, secretsStore *secrets.Store) *Handler {
	return &Handler{
		Settings: settings,
		Secrets:  secretsStore,
		httpc: &http.Client{
			Timeout: 0, // rely on request context timeouts
		},
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/databricks/oauth/start"):
		h.handleOAuthStart(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/oauth/complete"):
		h.handleOAuthComplete(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/oauth/callback"):
		h.handleOAuthCallback(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/oauth/result"):
		h.handleOAuthResult(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/status"):
		h.handleStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/test"):
		h.handleTest(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/preflight"):
		h.handlePreflight(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/query"):
		h.handleQuery(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/catalogs"):
		h.handleCatalogs(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/schemas"):
		h.handleSchemas(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/tables"):
		h.handleTables(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/databricks/table-schema"):
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

	rawHost := strings.TrimSpace(h.hostFromSettingsRaw())
	rawWarehouse := strings.TrimSpace(h.warehouseIDFromSettings())
	hostConfigured := rawHost != ""
	warehouseConfigured := rawWarehouse != ""

	host := rawHost
	hostValid := false
	hostValidationMessage := ""
	if rawHost != "" {
		normalized, err := normalizeGCPDatabricksHost(rawHost)
		if err != nil {
			hostValidationMessage = err.Error()
		} else {
			hostValid = true
			host = normalized
		}
	}

	authMode := "none"
	if h.Secrets != nil {
		if sec, err := h.Secrets.GetDatabricks(); err == nil {
			mode := strings.TrimSpace(strings.ToLower(sec.Mode))
			refreshTokenSet := strings.TrimSpace(sec.RefreshToken) != ""
			patSet := strings.TrimSpace(sec.PAT) != ""
			if mode == "" {
				switch {
				case refreshTokenSet:
					mode = "refresh_token"
				case patSet:
					mode = "pat"
				default:
					mode = "none"
				}
			}
			switch mode {
			case "refresh_token", "pat":
				authMode = mode
			default:
				authMode = "none"
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":                authMode,
		"host_configured":          hostConfigured,
		"warehouse_configured":     warehouseConfigured,
		"host":                     nilIfEmpty(host),
		"warehouse_id":             nilIfEmpty(rawWarehouse),
		"host_valid":               hostValid,
		"host_validation_message":  nilIfEmpty(hostValidationMessage),
		"host_validation_required": true,
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
		Host                  string `json:"host,omitempty"`
		WarehouseID           string `json:"warehouse_id,omitempty"`
		DatabricksHost        string `json:"databricksHost,omitempty"`
		DatabricksWarehouseID string `json:"databricksWarehouseId,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeDatabricksError(w, http.StatusBadRequest, "Invalid JSON body")
		h.persistTestStatus(context.Background(), "", "", "Invalid JSON body", false)
		return
	}

	rawHost := strings.TrimSpace(body.Host)
	if rawHost == "" {
		rawHost = strings.TrimSpace(body.DatabricksHost)
	}
	var host string
	var err error
	if rawHost != "" {
		host, err = normalizeGCPDatabricksHost(rawHost)
		if err != nil {
			writeDatabricksError(w, http.StatusBadRequest, err.Error())
			h.persistTestStatus(context.Background(), rawHost, "", err.Error(), false)
			return
		}
	} else {
		host, err = h.normalizedHostFromSettings()
		if err != nil {
			writeDatabricksError(w, httpStatusForErr(err), err.Error())
			h.persistTestStatus(context.Background(), "", "", err.Error(), false)
			return
		}
	}

	warehouseID := strings.TrimSpace(body.WarehouseID)
	if warehouseID == "" {
		warehouseID = strings.TrimSpace(body.DatabricksWarehouseID)
	}
	if warehouseID == "" {
		warehouseID = strings.TrimSpace(h.warehouseIDFromSettings())
	}
	if warehouseID == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing databricksWarehouseId in settings")
		h.persistTestStatus(context.Background(), host, "", "Missing databricksWarehouseId in settings", false)
		return
	}

	var wh struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	p := "/api/2.0/sql/warehouses/" + url.PathEscape(warehouseID)
	if err := h.doDatabricks(ctx, host, http.MethodGet, p, nil, &wh); err != nil {
		writeJSON(w, httpStatusForErr(err), map[string]any{
			"ok":              false,
			"warehouse_name":  nilIfEmpty(strings.TrimSpace(wh.Name)),
			"warehouse_state": nilIfEmpty(strings.TrimSpace(wh.State)),
			"message":         err.Error(),
		})
		h.persistTestStatus(context.Background(), host, warehouseID, err.Error(), false)
		return
	}

	state := strings.TrimSpace(wh.State)
	msg := "Connected."
	if state != "" && !strings.EqualFold(state, "RUNNING") {
		msg = "Connected, but warehouse is not RUNNING."
	}
	h.persistTestStatus(context.Background(), host, warehouseID, msg, true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"warehouse_name":  nilIfEmpty(strings.TrimSpace(wh.Name)),
		"warehouse_state": nilIfEmpty(state),
		"message":         msg,
	})
}

func (h *Handler) persistTestStatus(ctx context.Context, host, warehouseID, message string, ok bool) {
	if h.Settings == nil {
		return
	}
	s := h.Settings.Get()

	normHost := strings.TrimSpace(host)
	wh := strings.TrimSpace(warehouseID)
	msg := strings.TrimSpace(message)

	if s.DataSources == nil {
		off := false
		s.DataSources = &config.DatasourceConnections{
			BigQuery: &config.DatasourceConnectionState{
				Enabled: &off,
			},
			Databricks: &config.DatasourceConnectionState{
				Enabled: &off,
			},
		}
	}
	if s.DataSources.Databricks == nil {
		off := false
		s.DataSources.Databricks = &config.DatasourceConnectionState{
			Enabled: &off,
		}
	} else if s.DataSources.Databricks.Enabled == nil {
		off := false
		s.DataSources.Databricks.Enabled = &off
	}

	ds := s.DataSources.Databricks
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
	if wh == "" {
		ds.LastWarehouseID = nil
	} else {
		ds.LastWarehouseID = &wh
	}

	_ = h.Settings.Save(s)
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		SQL string `json:"sql"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Invalid JSON body",
			},
		})
		return
	}
	sql := strings.TrimSpace(body.SQL)
	if sql == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Missing sql",
			},
		})
		return
	}
	if !sqlutil.IsReadOnlySQL(sql) {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": false,
			"error": map[string]any{
				"message": "Only read-only SELECT/WITH queries are allowed",
			},
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()

	var body struct {
		SQL     string `json:"sql"`
		Catalog string `json:"catalog,omitempty"`
		Schema  string `json:"schema,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, 1<<20); err != nil {
		writeDatabricksError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	sql := strings.TrimSpace(body.SQL)
	if sql == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing sql")
		return
	}
	if !sqlutil.IsReadOnlySQL(sql) {
		writeDatabricksError(w, http.StatusBadRequest, "Only read-only SELECT/WITH queries are allowed")
		return
	}

	host, err := h.normalizedHostFromSettings()
	if err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	warehouseID := strings.TrimSpace(h.warehouseIDFromSettings())
	if warehouseID == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing databricksWarehouseId in settings")
		return
	}

	result, err := h.runQuery(ctx, host, warehouseID, sql, strings.TrimSpace(body.Catalog), strings.TrimSpace(body.Schema))
	if err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": result.Rows,
		"query_metadata": map[string]any{
			"provider":     "databricks",
			"duration_ms":  result.DurationMs,
			"statement_id": result.StatementID,
			"schema":       result.Schema,
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

	host, err := h.normalizedHostFromSettings()
	if err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}

	params := url.Values{}
	params.Set("max_results", fmt.Sprintf("%d", databricksCatalogListPageSize))
	if pageToken := r.URL.Query().Get("page_token"); pageToken != "" {
		params.Set("page_token", pageToken)
	}
	var out struct {
		Catalogs []struct {
			Name    string `json:"name"`
			Comment string `json:"comment,omitempty"`
		} `json:"catalogs"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := h.doDatabricks(ctx, host, http.MethodGet, "/api/2.1/unity-catalog/catalogs?"+params.Encode(), nil, &out); err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	catalogs := make([]map[string]any, 0, len(out.Catalogs))
	for _, c := range out.Catalogs {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		catalogs = append(catalogs, map[string]any{
			"name":    name,
			"comment": nilIfEmpty(strings.TrimSpace(c.Comment)),
		})
	}
	result := map[string]any{
		"catalogs":    catalogs,
		"has_more":    out.NextPageToken != "",
		"max_results": databricksCatalogListPageSize,
	}
	if out.NextPageToken != "" {
		result["next_page_token"] = out.NextPageToken
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleSchemas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	host, err := h.normalizedHostFromSettings()
	if err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	catalogName := strings.TrimSpace(r.URL.Query().Get("catalog_name"))
	if catalogName == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing catalog_name")
		return
	}

	params := url.Values{}
	params.Set("catalog_name", catalogName)
	params.Set("max_results", fmt.Sprintf("%d", databricksSchemaListPageSize))
	if pageToken := r.URL.Query().Get("page_token"); pageToken != "" {
		params.Set("page_token", pageToken)
	}
	p := "/api/2.1/unity-catalog/schemas?" + params.Encode()
	var out struct {
		Schemas []struct {
			Name    string `json:"name"`
			Comment string `json:"comment,omitempty"`
		} `json:"schemas"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := h.doDatabricks(ctx, host, http.MethodGet, p, nil, &out); err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	schemas := make([]map[string]any, 0, len(out.Schemas))
	for _, s := range out.Schemas {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			continue
		}
		schemas = append(schemas, map[string]any{
			"name":    name,
			"comment": nilIfEmpty(strings.TrimSpace(s.Comment)),
		})
	}
	result := map[string]any{
		"schemas":     schemas,
		"has_more":    out.NextPageToken != "",
		"max_results": databricksSchemaListPageSize,
	}
	if out.NextPageToken != "" {
		result["next_page_token"] = out.NextPageToken
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleTables(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	host, err := h.normalizedHostFromSettings()
	if err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	catalogName := strings.TrimSpace(r.URL.Query().Get("catalog_name"))
	schemaName := strings.TrimSpace(r.URL.Query().Get("schema_name"))
	if catalogName == "" || schemaName == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing catalog_name or schema_name")
		return
	}

	params := url.Values{}
	params.Set("catalog_name", catalogName)
	params.Set("schema_name", schemaName)
	params.Set("max_results", fmt.Sprintf("%d", databricksTableListPageSize))
	params.Set("omit_columns", "true")
	params.Set("omit_properties", "true")
	params.Set("omit_username", "true")
	if pageToken := r.URL.Query().Get("page_token"); pageToken != "" {
		params.Set("page_token", pageToken)
	}
	p := "/api/2.1/unity-catalog/tables?" + params.Encode()
	var out struct {
		Tables []struct {
			Name      string `json:"name"`
			TableType string `json:"table_type,omitempty"`
		} `json:"tables"`
		NextPageToken string `json:"next_page_token"`
	}
	if err := h.doDatabricks(ctx, host, http.MethodGet, p, nil, &out); err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	tables := make([]map[string]any, 0, len(out.Tables))
	for _, t := range out.Tables {
		name := strings.TrimSpace(t.Name)
		if name == "" {
			continue
		}
		tables = append(tables, map[string]any{
			"name": name,
			"type": nilIfEmpty(strings.TrimSpace(t.TableType)),
		})
	}
	result := map[string]any{
		"tables":      tables,
		"has_more":    out.NextPageToken != "",
		"max_results": databricksTableListPageSize,
	}
	if out.NextPageToken != "" {
		result["next_page_token"] = out.NextPageToken
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleTableSchema(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	host, err := h.normalizedHostFromSettings()
	if err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	fullName := strings.TrimSpace(r.URL.Query().Get("full_name"))
	if fullName == "" {
		writeDatabricksError(w, http.StatusBadRequest, "Missing full_name")
		return
	}

	p := "/api/2.1/unity-catalog/tables/" + url.PathEscape(fullName)
	var out struct {
		Columns []struct {
			Name     string `json:"name"`
			TypeText string `json:"type_text,omitempty"`
			Comment  string `json:"comment,omitempty"`
		} `json:"columns"`
	}
	if err := h.doDatabricks(ctx, host, http.MethodGet, p, nil, &out); err != nil {
		writeDatabricksError(w, httpStatusForErr(err), err.Error())
		return
	}
	schema := make([]map[string]any, 0, len(out.Columns))
	for _, c := range out.Columns {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		schema = append(schema, map[string]any{
			"name":    name,
			"type":    nilIfEmpty(strings.TrimSpace(c.TypeText)),
			"comment": nilIfEmpty(strings.TrimSpace(c.Comment)),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"table": fullName, "schema": schema})
}

type queryResult struct {
	Rows        []map[string]any
	DurationMs  int64
	StatementID string
	Schema      []map[string]any
}

type QueryResult struct {
	Rows     []map[string]any `json:"rows,omitempty"`
	Schema   []map[string]any `json:"schema,omitempty"`
	Metadata map[string]any   `json:"metadata,omitempty"`
}

type SubmitResult struct {
	Completed bool             `json:"completed"`
	Rows      []map[string]any `json:"rows,omitempty"`
	Schema    []map[string]any `json:"schema,omitempty"`
	Metadata  map[string]any   `json:"metadata,omitempty"`

	StatementID string `json:"statement_id,omitempty"`
	Host        string `json:"host,omitempty"`
}

const (
	defaultAdaptiveWaitFirst  = 300 * time.Millisecond
	defaultAdaptiveWaitSecond = 800 * time.Millisecond
)

func (h *Handler) SubmitQuery(ctx context.Context, host, warehouseID, sql, catalog, schema string, initialWait time.Duration) (*SubmitResult, error) {
	host = strings.TrimSpace(host)
	warehouseID = strings.TrimSpace(warehouseID)
	sql = strings.TrimSpace(sql)
	catalog = strings.TrimSpace(catalog)
	schema = strings.TrimSpace(schema)
	if host == "" {
		return nil, errors.New("missing databricks host")
	}
	if warehouseID == "" {
		return nil, errors.New("missing databricks warehouse_id")
	}
	if sql == "" {
		return nil, errors.New("missing sql")
	}

	firstWait := initialWait
	if firstWait <= 0 {
		firstWait = defaultAdaptiveWaitFirst
	}
	if firstWait < 50*time.Millisecond {
		firstWait = 50 * time.Millisecond
	}
	snapshot, err := h.SubmitStatement(ctx, StatementSubmitRequest{
		SQL:           sql,
		Host:          host,
		WarehouseID:   warehouseID,
		Catalog:       catalog,
		Schema:        schema,
		WaitTimeout:   0,
		OnWaitTimeout: "CONTINUE",
		Disposition:   "INLINE",
		Format:        "JSON_ARRAY",
	})
	if err != nil {
		return nil, err
	}
	statementID := strings.TrimSpace(snapshot.Handle.StatementID)
	state := snapshot.State
	switch state {
	case "SUCCEEDED":
		materialized, materializeErr := h.MaterializeStatementResult(ctx, MaterializeStatementRequest{
			Handle:    snapshot.Handle,
			Snapshot:  &snapshot,
			MaxRows:   2501,
			MaxBytes:  32 << 20,
			MaxChunks: 10_000,
		})
		if materializeErr != nil {
			return nil, materializeErr
		}
		return &SubmitResult{
			Completed:   true,
			Rows:        materialized.Rows,
			Schema:      materialized.Schema,
			StatementID: statementID,
			Host:        host,
			Metadata: map[string]any{
				"provider":        "databricks",
				"statement_id":    statementID,
				"host":            host,
				"warehouse_id":    warehouseID,
				"state":           "SUCCEEDED",
				"terminal_status": "completed",
			},
		}, nil
	case "CLOSED":
		return &SubmitResult{
			Completed:   true,
			StatementID: statementID,
			Host:        host,
			Metadata: map[string]any{
				"provider":           "databricks",
				"statement_id":       statementID,
				"host":               host,
				"warehouse_id":       warehouseID,
				"state":              "CLOSED",
				"terminal_status":    "completed",
				"result_unavailable": true,
			},
		}, nil
	case "PENDING", "RUNNING":
		if statementID == "" {
			return nil, errors.New("Databricks submission failed: missing statement_id")
		}
	default:
		if snapshot.Error != nil && strings.TrimSpace(snapshot.Error.Message) != "" {
			return nil, errors.New(strings.TrimSpace(snapshot.Error.Message))
		}
		if state == "" {
			state = "UNKNOWN"
		}
		return nil, fmt.Errorf("Databricks query failed (state=%s)", state)
	}

	done, quickResult, err := h.waitForStatement(ctx, host, statementID, firstWait)
	if err != nil {
		return nil, err
	}
	if !done {
		done, quickResult, err = h.waitForStatement(ctx, host, statementID, defaultAdaptiveWaitSecond)
		if err != nil {
			return nil, err
		}
	}
	if done && quickResult != nil {
		md := cloneMap(quickResult.Metadata)
		if md == nil {
			md = map[string]any{}
		}
		md["warehouse_id"] = warehouseID
		return &SubmitResult{
			Completed:   true,
			Rows:        quickResult.Rows,
			Schema:      quickResult.Schema,
			StatementID: statementID,
			Host:        host,
			Metadata:    md,
		}, nil
	}

	return &SubmitResult{
		Completed:   false,
		StatementID: statementID,
		Host:        host,
		Metadata: map[string]any{
			"provider":        "databricks",
			"statement_id":    statementID,
			"host":            host,
			"warehouse_id":    warehouseID,
			"state":           "RUNNING",
			"terminal_status": "",
		},
	}, nil
}

func (h *Handler) waitForStatement(ctx context.Context, host, statementID string, wait time.Duration) (bool, *QueryResult, error) {
	if wait <= 0 {
		return false, nil, nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return false, nil, ctx.Err()
			}
			return false, nil, nil
		}
		done, result, err := h.PollStatement(waitCtx, host, statementID)
		if err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return false, nil, nil
			}
			return false, nil, err
		}
		if done {
			return true, result, nil
		}
		if err := sleepWithContext(waitCtx, 120*time.Millisecond); err != nil {
			if waitCtx.Err() != nil && ctx.Err() == nil {
				return false, nil, nil
			}
			return false, nil, err
		}
	}
}

func (h *Handler) PollStatement(ctx context.Context, host, statementID string) (bool, *QueryResult, error) {
	host = strings.TrimSpace(host)
	statementID = strings.TrimSpace(statementID)
	if host == "" || statementID == "" {
		return false, nil, errors.New("host and statement_id are required")
	}
	snapshot, err := h.GetStatement(ctx, StatementHandle{Host: host, StatementID: statementID})
	if err != nil {
		return false, nil, err
	}
	state := snapshot.State
	switch state {
	case "PENDING", "RUNNING":
		return false, nil, nil
	case "SUCCEEDED":
		materialized, materializeErr := h.MaterializeStatementResult(ctx, MaterializeStatementRequest{
			Handle:    snapshot.Handle,
			Snapshot:  &snapshot,
			MaxRows:   2501,
			MaxBytes:  32 << 20,
			MaxChunks: 10_000,
		})
		if materializeErr != nil {
			return true, nil, materializeErr
		}
		return true, &QueryResult{
			Rows:   materialized.Rows,
			Schema: materialized.Schema,
			Metadata: map[string]any{
				"provider":        "databricks",
				"statement_id":    statementID,
				"host":            host,
				"state":           state,
				"terminal_status": "completed",
			},
		}, nil
	case "CLOSED":
		return true, &QueryResult{
			Metadata: map[string]any{
				"provider":           "databricks",
				"statement_id":       statementID,
				"host":               host,
				"state":              state,
				"terminal_status":    "completed",
				"result_unavailable": true,
			},
		}, nil
	case "CANCELED", "CANCELLED":
		return true, &QueryResult{
			Metadata: map[string]any{
				"provider":        "databricks",
				"statement_id":    statementID,
				"host":            host,
				"state":           state,
				"terminal_status": "cancelled",
				"error":           "Databricks query cancelled",
			},
		}, nil
	default:
		message := fmt.Sprintf("Databricks query failed (state=%s)", state)
		if snapshot.Error != nil && strings.TrimSpace(snapshot.Error.Message) != "" {
			message = strings.TrimSpace(snapshot.Error.Message)
		}
		if state == "" {
			state = "UNKNOWN"
		}
		return true, &QueryResult{
			Metadata: map[string]any{
				"provider":        "databricks",
				"statement_id":    statementID,
				"host":            host,
				"state":           state,
				"terminal_status": "failed",
				"error":           message,
			},
		}, nil
	}
}

func (h *Handler) CancelStatement(ctx context.Context, host, statementID string) error {
	host = strings.TrimSpace(host)
	statementID = strings.TrimSpace(statementID)
	if host == "" || statementID == "" {
		return errors.New("host and statement_id are required")
	}
	_, err := h.RequestCancelStatement(ctx, StatementHandle{Host: host, StatementID: statementID})
	return err
}

func (h *Handler) runQuery(ctx context.Context, host, warehouseID, sql, catalog, schema string) (queryResult, error) {
	start := time.Now()
	payload := map[string]any{
		"statement":    sql,
		"warehouse_id": warehouseID,
		"wait_timeout": "30s",
		"disposition":  "INLINE",
		"format":       "JSON_ARRAY",
	}
	if catalog != "" {
		payload["catalog"] = catalog
	}
	if schema != "" {
		payload["schema"] = schema
	}

	var stmt statementResponse
	if err := h.doDatabricks(ctx, host, http.MethodPost, "/api/2.0/sql/statements", payload, &stmt); err != nil {
		return queryResult{}, err
	}

	for {
		state := strings.TrimSpace(strings.ToUpper(stmt.Status.State))
		switch state {
		case "SUCCEEDED":
			durationMs := time.Since(start).Milliseconds()
			schemaCols := make([]map[string]any, 0, len(stmt.Manifest.Schema.Columns))
			for _, c := range stmt.Manifest.Schema.Columns {
				schemaCols = append(schemaCols, map[string]any{"name": strings.TrimSpace(c.Name)})
			}
			return queryResult{
				Rows:        statementToRows(stmt),
				DurationMs:  durationMs,
				StatementID: strings.TrimSpace(stmt.StatementID),
				Schema:      schemaCols,
			}, nil
		case "PENDING", "RUNNING":
			if strings.TrimSpace(stmt.StatementID) == "" {
				return queryResult{}, errors.New("Databricks query polling failed: missing statement_id")
			}
			p := "/api/2.0/sql/statements/" + url.PathEscape(strings.TrimSpace(stmt.StatementID))
			if err := sleepWithContext(ctx, 1*time.Second); err != nil {
				return queryResult{}, err
			}
			if err := h.doDatabricks(ctx, host, http.MethodGet, p, nil, &stmt); err != nil {
				return queryResult{}, err
			}
		default:
			if stmt.Status.Error != nil && strings.TrimSpace(stmt.Status.Error.Message) != "" {
				return queryResult{}, errors.New(strings.TrimSpace(stmt.Status.Error.Message))
			}
			if state == "" {
				state = "UNKNOWN"
			}
			return queryResult{}, fmt.Errorf("Databricks query failed (state=%s)", state)
		}
	}
}

// QueryProgressFn is called during query execution to report progress.
type QueryProgressPhase string

const (
	queryProgressPhaseSubmitted QueryProgressPhase = "submitted"
	queryProgressPhasePolling   QueryProgressPhase = "polling"
	queryProgressPhaseComplete  QueryProgressPhase = "complete"
)

type QueryProgressFn func(phase string, meta map[string]any)

// RunQueryWithProgress mirrors runQuery but accepts an optional progress callback
// that emits metadata as the query executes.
func (h *Handler) RunQueryWithProgress(ctx context.Context, host, warehouseID, sql, catalog, schema string, onProgress QueryProgressFn) (queryResult, error) {
	start := time.Now()
	payload := map[string]any{
		"statement":    sql,
		"warehouse_id": warehouseID,
		"wait_timeout": "30s",
		"disposition":  "INLINE",
		"format":       "JSON_ARRAY",
	}
	if catalog != "" {
		payload["catalog"] = catalog
	}
	if schema != "" {
		payload["schema"] = schema
	}

	var stmt statementResponse
	if err := h.doDatabricks(ctx, host, http.MethodPost, "/api/2.0/sql/statements", payload, &stmt); err != nil {
		return queryResult{}, err
	}

	// PROGRESS EVENT: "submitted" — statement_id available immediately.
	stmtID := strings.TrimSpace(stmt.StatementID)
	if onProgress != nil && stmtID != "" {
		onProgress(string(queryProgressPhaseSubmitted), map[string]any{
			"provider":     "databricks",
			"state":        strings.TrimSpace(strings.ToUpper(stmt.Status.State)),
			"statement_id": stmtID,
			"warehouse_id": warehouseID,
			"host":         host,
			"url":          databricksQueryHistoryURL(host, stmtID),
		})
	}

	for {
		state := strings.TrimSpace(strings.ToUpper(stmt.Status.State))
		switch state {
		case "SUCCEEDED":
			if onProgress != nil {
				onProgress(string(queryProgressPhaseComplete), map[string]any{
					"provider":     "databricks",
					"state":        state,
					"statement_id": strings.TrimSpace(stmt.StatementID),
					"warehouse_id": warehouseID,
					"host":         host,
					"url":          databricksQueryHistoryURL(host, strings.TrimSpace(stmt.StatementID)),
				})
			}
			durationMs := time.Since(start).Milliseconds()
			schemaCols := make([]map[string]any, 0, len(stmt.Manifest.Schema.Columns))
			for _, c := range stmt.Manifest.Schema.Columns {
				schemaCols = append(schemaCols, map[string]any{"name": strings.TrimSpace(c.Name)})
			}
			return queryResult{
				Rows:        statementToRows(stmt),
				DurationMs:  durationMs,
				StatementID: strings.TrimSpace(stmt.StatementID),
				Schema:      schemaCols,
			}, nil
		case "PENDING", "RUNNING":
			// PROGRESS EVENT: "polling" — state update.
			if onProgress != nil {
				onProgress(string(queryProgressPhasePolling), map[string]any{
					"provider": "databricks",
					"state":    state,
				})
			}
			if strings.TrimSpace(stmt.StatementID) == "" {
				return queryResult{}, errors.New("Databricks query polling failed: missing statement_id")
			}
			p := "/api/2.0/sql/statements/" + url.PathEscape(strings.TrimSpace(stmt.StatementID))
			if err := sleepWithContext(ctx, 1*time.Second); err != nil {
				return queryResult{}, err
			}
			if err := h.doDatabricks(ctx, host, http.MethodGet, p, nil, &stmt); err != nil {
				return queryResult{}, err
			}
		default:
			if stmt.Status.Error != nil && strings.TrimSpace(stmt.Status.Error.Message) != "" {
				return queryResult{}, errors.New(strings.TrimSpace(stmt.Status.Error.Message))
			}
			if state == "" {
				state = "UNKNOWN"
			}
			return queryResult{}, fmt.Errorf("Databricks query failed (state=%s)", state)
		}
	}
}

func databricksQueryHistoryURL(host, statementID string) string {
	h := strings.TrimRight(strings.TrimSpace(host), "/")
	if !strings.HasPrefix(h, "https://") {
		h = "https://" + h
	}
	return fmt.Sprintf("%s/sql/history?filter=%%7B%%22statementId%%22%%3A%%22%s%%22%%7D", h, url.QueryEscape(statementID))
}

func statementToRows(resp statementResponse) []map[string]any {
	data := resp.Result.DataArray
	if len(data) == 0 {
		return []map[string]any{}
	}
	cols := make([]string, 0, len(resp.Manifest.Schema.Columns))
	for _, c := range resp.Manifest.Schema.Columns {
		n := strings.TrimSpace(c.Name)
		if n == "" {
			n = fmt.Sprintf("col_%d", len(cols))
		}
		cols = append(cols, n)
	}
	if len(cols) == 0 && len(data) > 0 {
		for i := range data[0] {
			cols = append(cols, fmt.Sprintf("col_%d", i))
		}
	}

	rows := make([]map[string]any, 0, len(data))
	for _, row := range data {
		obj := map[string]any{}
		for i := range row {
			colName := fmt.Sprintf("col_%d", i)
			if i < len(cols) && strings.TrimSpace(cols[i]) != "" {
				colName = cols[i]
			}
			obj[colName] = row[i]
		}
		rows = append(rows, obj)
	}
	return rows
}

func statementSchemaToMaps(resp statementResponse) []map[string]any {
	out := make([]map[string]any, 0, len(resp.Manifest.Schema.Columns))
	for _, column := range resp.Manifest.Schema.Columns {
		name := strings.TrimSpace(column.Name)
		if name == "" {
			continue
		}
		col := map[string]any{"name": name}
		if t := strings.TrimSpace(column.TypeName); t != "" {
			col["type"] = t
		} else if t := strings.TrimSpace(column.TypeText); t != "" {
			col["type"] = t
		}
		out = append(out, col)
	}
	return out
}

func cloneMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func (h *Handler) doDatabricks(ctx context.Context, host, method, path string, body any, out any) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return httpError{status: http.StatusBadRequest, msg: "Databricks host is not configured"}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	var bodyBytes []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return httpError{status: http.StatusInternalServerError, msg: "failed to encode Databricks request", cause: err}
		}
		bodyBytes = b
	}

	idempotent := method == http.MethodGet || method == http.MethodHead
	authRetried := false
	const maxAttempts = 3
	for attempt := 0; attempt < maxAttempts; attempt++ {
		token, err := h.accessToken(ctx, host)
		if err != nil {
			if ctx.Err() != nil {
				return databricksUpstreamError(ctx, "Databricks authentication was interrupted", err)
			}
			return err
		}

		var bodyReader io.Reader
		if bodyBytes != nil {
			bodyReader = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(host, "/")+path, bodyReader)
		if err != nil {
			return httpError{status: http.StatusInternalServerError, msg: "failed to create Databricks request", cause: err}
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := h.httpc.Do(req)
		if err != nil {
			upstreamErr := databricksUpstreamError(ctx, fmt.Sprintf("failed to reach Databricks API: %v", err), err)
			if idempotent && ctx.Err() == nil && attempt+1 < maxAttempts {
				if waitErr := sleepWithContext(ctx, databricksMetadataRetryDelay(attempt, "")); waitErr != nil {
					return databricksUpstreamError(ctx, "Databricks API retry was interrupted", waitErr)
				}
				continue
			}
			return upstreamErr
		}
		respBody, readErr := readBoundedResponse(resp.Body, databricksResponseBodyLimit)
		_ = resp.Body.Close()
		if readErr != nil {
			upstreamErr := databricksUpstreamError(ctx, "failed to read Databricks API response", readErr)
			if idempotent && ctx.Err() == nil && attempt+1 < maxAttempts {
				if waitErr := sleepWithContext(ctx, databricksMetadataRetryDelay(attempt, "")); waitErr != nil {
					return databricksUpstreamError(ctx, "Databricks API retry was interrupted", waitErr)
				}
				continue
			}
			return upstreamErr
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			msg := databricksErrorMessage(resp.StatusCode, respBody)
			if resp.StatusCode == http.StatusUnauthorized && !authRetried && attempt+1 < maxAttempts {
				authRetried = true
				h.invalidateCachedToken()
				continue
			}
			if idempotent && retryableDatabricksMetadataStatus(resp.StatusCode) && attempt+1 < maxAttempts {
				if waitErr := sleepWithContext(ctx, databricksMetadataRetryDelay(attempt, resp.Header.Get("Retry-After"))); waitErr != nil {
					return databricksUpstreamError(ctx, "Databricks API retry was interrupted", waitErr)
				}
				continue
			}
			return httpError{status: resp.StatusCode, msg: msg}
		}

		if out == nil {
			return nil
		}
		if len(bytes.TrimSpace(respBody)) == 0 {
			emptyErr := httpError{
				status: http.StatusBadGateway,
				msg:    "Databricks API returned an empty response",
			}
			if idempotent && attempt+1 < maxAttempts {
				if waitErr := sleepWithContext(ctx, databricksMetadataRetryDelay(attempt, "")); waitErr != nil {
					return databricksUpstreamError(ctx, "Databricks API retry was interrupted", waitErr)
				}
				continue
			}
			return emptyErr
		}
		if err := json.Unmarshal(respBody, out); err != nil {
			malformedErr := httpError{
				status: http.StatusBadGateway,
				msg:    "Databricks API returned malformed JSON",
				cause:  err,
			}
			if idempotent && attempt+1 < maxAttempts {
				if waitErr := sleepWithContext(ctx, databricksMetadataRetryDelay(attempt, "")); waitErr != nil {
					return databricksUpstreamError(ctx, "Databricks API retry was interrupted", waitErr)
				}
				continue
			}
			return malformedErr
		}
		return nil
	}
	return httpError{status: http.StatusBadGateway, msg: "Databricks request failed"}
}

func retryableDatabricksMetadataStatus(status int) bool {
	return status == http.StatusRequestTimeout ||
		status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests ||
		(status >= http.StatusInternalServerError && status <= 599)
}

func databricksMetadataRetryDelay(attempt int, retryAfterHeader string) time.Duration {
	delay := 200 * time.Millisecond
	if attempt > 0 {
		delay = 600 * time.Millisecond
	}
	if retryAfter := parseStatementRetryAfter(retryAfterHeader); retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

func databricksUpstreamError(ctx context.Context, message string, cause error) httpError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(cause, context.DeadlineExceeded) {
		return httpError{status: http.StatusGatewayTimeout, msg: "Databricks API request timed out", cause: cause}
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(cause, context.Canceled) {
		return httpError{status: http.StatusRequestTimeout, msg: "Databricks API request was cancelled", cause: cause}
	}
	return httpError{status: http.StatusBadGateway, msg: message, cause: cause}
}

// NormalizedHost returns the normalized Databricks host from settings.
func (h *Handler) NormalizedHost() (string, error) {
	return h.normalizedHostFromSettings()
}

// WarehouseID returns the Databricks warehouse ID from settings.
func (h *Handler) WarehouseID() string {
	return strings.TrimSpace(h.warehouseIDFromSettings())
}

func (h *Handler) hostFromSettingsRaw() string {
	if h.Settings == nil {
		return ""
	}
	s := h.Settings.Get()
	if s.DatabricksHost == nil {
		return ""
	}
	return strings.TrimSpace(*s.DatabricksHost)
}

func (h *Handler) normalizedHostFromSettings() (string, error) {
	raw := strings.TrimSpace(h.hostFromSettingsRaw())
	if raw == "" {
		return "", httpError{status: http.StatusBadRequest, msg: "Missing databricksHost in settings"}
	}
	normalized, err := normalizeGCPDatabricksHost(raw)
	if err != nil {
		return "", httpError{status: http.StatusBadRequest, msg: err.Error()}
	}
	return normalized, nil
}

func (h *Handler) warehouseIDFromSettings() string {
	if h.Settings == nil {
		return ""
	}
	s := h.Settings.Get()
	if s.DatabricksWarehouseID == nil {
		return ""
	}
	return strings.TrimSpace(*s.DatabricksWarehouseID)
}

func (h *Handler) oauthClientIDFromSettings() string {
	if h.Settings == nil {
		return ""
	}
	s := h.Settings.Get()
	if s.DatabricksOAuthClientID == nil {
		return ""
	}
	return strings.TrimSpace(*s.DatabricksOAuthClientID)
}

func normalizeGCPDatabricksHost(raw string) (string, error) {
	host := strings.TrimSpace(raw)
	if host == "" {
		return "", errors.New("Databricks host is required")
	}
	if !strings.Contains(host, "://") {
		host = "https://" + host
	}

	u, err := url.Parse(host)
	if err != nil {
		return "", errors.New("Invalid Databricks host URL")
	}
	if strings.ToLower(strings.TrimSpace(u.Scheme)) != "https" {
		return "", errors.New("Databricks host must use https")
	}
	if u.User != nil {
		return "", errors.New("Databricks host must not include user info")
	}
	if strings.TrimSpace(u.Path) != "" && strings.TrimSpace(u.Path) != "/" {
		return "", errors.New("Databricks host must not include a path")
	}
	if strings.TrimSpace(u.RawQuery) != "" || strings.TrimSpace(u.Fragment) != "" {
		return "", errors.New("Databricks host must not include query or fragment")
	}
	if strings.TrimSpace(u.Port()) != "" {
		return "", errors.New("Databricks host must not include a custom port")
	}

	hostname := strings.ToLower(strings.TrimSpace(u.Hostname()))
	if !strings.HasSuffix(hostname, ".gcp.databricks.com") {
		return "", errors.New("Databricks host must be a GCP workspace domain (*.gcp.databricks.com)")
	}
	return "https://" + hostname, nil
}

type httpError struct {
	status int
	msg    string
	cause  error
}

func (e httpError) Error() string { return e.msg }
func (e httpError) Unwrap() error { return e.cause }

func httpStatusForErr(err error) int {
	var he httpError
	if errors.As(err, &he) && he.status != 0 {
		return he.status
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	if errors.Is(err, context.Canceled) {
		return http.StatusRequestTimeout
	}
	return http.StatusBadRequest
}

func databricksErrorMessage(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("Databricks API error (HTTP %d)", status)
	}
	var parsed struct {
		Error any    `json:"error"`
		Msg   string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := strings.TrimSpace(parsed.Msg); msg != "" {
			return msg
		}
		switch v := parsed.Error.(type) {
		case string:
			if msg := strings.TrimSpace(v); msg != "" {
				return msg
			}
		case map[string]any:
			if msg, ok := v["message"].(string); ok && strings.TrimSpace(msg) != "" {
				return strings.TrimSpace(msg)
			}
		}
	}
	return text
}

func decodeJSONLimit(w http.ResponseWriter, r *http.Request, dst any, limit int64) error {
	if limit <= 0 {
		limit = 1 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("unexpected additional JSON values")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeDatabricksError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"message": msg,
		},
	})
}

func nilIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
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
