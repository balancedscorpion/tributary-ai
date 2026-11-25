package snowflake

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sqlutil"
)

const (
	snowflakeResponseBodyLimit          = 8 << 20
	defaultSnowflakeUpstreamReqTimeout  = 15 * time.Second
	defaultSnowflakePollSleep           = 350 * time.Millisecond
	defaultSnowflakeStatementTimeoutSec = int64(DefaultAsyncStatementTimeout / time.Second)
)

// Handler exposes Snowflake SQL API-backed routes under /api/snowflake/*.
type Handler struct {
	Settings *config.Store
	Secrets  *secrets.Store

	httpc                  *http.Client
	upstreamRequestTimeout time.Duration

	// keypair_jwt token cache (see auth.go)
	tokenMu        sync.Mutex
	cachedJWT      string
	cachedJWTKey   string
	cachedJWTUntil time.Time

	metrics        *snowflakeMetrics
	sleepFn        func(context.Context, time.Duration) error
	nowFn          func() time.Time
	newRequestIDFn func() string
	logf           func(format string, args ...any)
}

func NewHandler(settings *config.Store, secretsStore *secrets.Store) *Handler {
	return &Handler{
		Settings: settings,
		Secrets:  secretsStore,
		httpc: &http.Client{
			Timeout: 0, // rely on request context timeouts
		},
		upstreamRequestTimeout: defaultSnowflakeUpstreamReqTimeout,
		metrics:                newSnowflakeMetrics(),
		sleepFn:                sleepWithContext,
		nowFn:                  time.Now,
		newRequestIDFn:         newSnowflakeRequestID,
		logf:                   log.Printf,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/status"):
		h.handleStatus(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/test"):
		h.handleTest(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/preflight"):
		h.handlePreflight(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/query"):
		h.handleQuery(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/databases"):
		h.handleDatabases(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/schemas"):
		h.handleSchemas(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/tables"):
		h.handleTables(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/snowflake/table-schema"):
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
	mode := "none"
	authErr := ""
	if m, _, err := h.statusAuthMode(); err == nil {
		mode = m
	} else {
		authErr = err.Error()
	}

	accountConfigured := strings.TrimSpace(settings.AccountIdentifier) != ""
	warehouseConfigured := strings.TrimSpace(settings.Warehouse) != ""
	databaseConfigured := strings.TrimSpace(settings.Database) != ""
	schemaConfigured := strings.TrimSpace(settings.Schema) != ""
	roleConfigured := strings.TrimSpace(settings.Role) != ""
	userConfigured := strings.TrimSpace(settings.User) != ""

	hostValid := false
	hostValidationMessage := ""
	normalizedAccount := settings.AccountIdentifier
	normalizedHost := ""
	if accountConfigured {
		account, host, err := normalizeSnowflakeAccountIdentifier(settings.AccountIdentifier)
		if err != nil {
			hostValidationMessage = err.Error()
		} else {
			hostValid = true
			normalizedAccount = account
			normalizedHost = host
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"auth_mode":                  mode,
		"account_configured":         accountConfigured,
		"warehouse_configured":       warehouseConfigured,
		"database_configured":        databaseConfigured,
		"schema_configured":          schemaConfigured,
		"role_configured":            roleConfigured,
		"user_configured":            userConfigured,
		"account_identifier":         nilIfEmpty(normalizedAccount),
		"host":                       nilIfEmpty(normalizedHost),
		"warehouse":                  nilIfEmpty(settings.Warehouse),
		"database":                   nilIfEmpty(settings.Database),
		"schema":                     nilIfEmpty(settings.Schema),
		"role":                       nilIfEmpty(settings.Role),
		"user":                       nilIfEmpty(settings.User),
		"account_valid":              hostValid,
		"account_validation_message": nilIfEmpty(hostValidationMessage),
		"auth_error":                 nilIfEmpty(authErr),
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
		AccountIdentifier  string `json:"account_identifier,omitempty"`
		SnowflakeAccountID string `json:"snowflakeAccountIdentifier,omitempty"`
		Warehouse          string `json:"warehouse,omitempty"`
		SnowflakeWarehouse string `json:"snowflakeWarehouse,omitempty"`
		Database           string `json:"database,omitempty"`
		SnowflakeDatabase  string `json:"snowflakeDatabase,omitempty"`
		Schema             string `json:"schema,omitempty"`
		SnowflakeSchema    string `json:"snowflakeSchema,omitempty"`
		Role               string `json:"role,omitempty"`
		SnowflakeRole      string `json:"snowflakeRole,omitempty"`
		User               string `json:"user,omitempty"`
		SnowflakeUser      string `json:"snowflakeUser,omitempty"`
	}
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, "Invalid JSON body")
		h.persistTestStatus("", "", "", "Invalid JSON body", false)
		return
	}

	overrides := authRequestOverrides{
		AccountIdentifier: firstNonEmpty(body.AccountIdentifier, body.SnowflakeAccountID),
		Warehouse:         firstNonEmpty(body.Warehouse, body.SnowflakeWarehouse),
		Database:          firstNonEmpty(body.Database, body.SnowflakeDatabase),
		Schema:            firstNonEmpty(body.Schema, body.SnowflakeSchema),
		Role:              firstNonEmpty(body.Role, body.SnowflakeRole),
		User:              firstNonEmpty(body.User, body.SnowflakeUser),
	}
	cfg, err := h.authConfig(overrides)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		h.persistTestStatus(overrides.AccountIdentifier, overrides.Warehouse, overrides.Database, err.Error(), false)
		return
	}

	result, err := h.executeSQLLifecycle(ctx, cfg, "SELECT 1", 10)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		h.persistTestStatus(cfg.AccountIdentifier, cfg.Warehouse, cfg.Database, err.Error(), false)
		return
	}

	msg := "Connected."
	h.persistTestStatus(cfg.AccountIdentifier, cfg.Warehouse, cfg.Database, msg, true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"message":      msg,
		"query_handle": nilIfEmpty(result.Handle),
		"rows_visible": len(result.Rows),
	})
}

func (h *Handler) persistTestStatus(accountIdentifier, warehouse, database, message string, ok bool) {
	if h == nil || h.Settings == nil {
		return
	}
	s := h.Settings.Get()

	if s.DataSources == nil {
		off := false
		s.DataSources = &config.DatasourceConnections{
			BigQuery:   &config.DatasourceConnectionState{Enabled: &off},
			Databricks: &config.DatasourceConnectionState{Enabled: &off},
			Trino:      &config.DatasourceConnectionState{Enabled: &off},
		}
	}

	now := h.now().UnixMilli()
	state := &config.DatasourceConnectionState{
		LastCheckedAt:    &now,
		LastCheckOk:      boolPtr(ok),
		LastCheckMessage: stringPtrOrNil(message),
		LastHost:         stringPtrOrNil(accountIdentifier),
		LastWarehouseID:  stringPtrOrNil(warehouse),
		LastProjectID:    stringPtrOrNil(database),
	}
	if !setSnowflakeDatasourceState(s.DataSources, state) {
		return
	}
	_ = h.Settings.Save(s)
}

func setSnowflakeDatasourceState(ds *config.DatasourceConnections, state *config.DatasourceConnectionState) bool {
	if ds == nil || state == nil {
		return false
	}
	rv := reflect.ValueOf(ds)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return false
	}
	field := rv.Elem().FieldByName("Snowflake")
	if !field.IsValid() || !field.CanSet() || field.Kind() != reflect.Pointer {
		return false
	}

	if field.IsNil() {
		if state.Enabled == nil {
			off := false
			state.Enabled = &off
		}
		field.Set(reflect.ValueOf(state))
		return true
	}

	if existing, ok := field.Interface().(*config.DatasourceConnectionState); ok && existing != nil {
		if state.Enabled == nil {
			state.Enabled = existing.Enabled
		}
		*existing = *state
		return true
	}

	field.Set(reflect.ValueOf(state))
	return true
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()

	var body struct {
		SQL                string `json:"sql"`
		AccountIdentifier  string `json:"account_identifier,omitempty"`
		SnowflakeAccountID string `json:"snowflakeAccountIdentifier,omitempty"`
		Warehouse          string `json:"warehouse,omitempty"`
		SnowflakeWarehouse string `json:"snowflakeWarehouse,omitempty"`
		Database           string `json:"database,omitempty"`
		SnowflakeDatabase  string `json:"snowflakeDatabase,omitempty"`
		Schema             string `json:"schema,omitempty"`
		SnowflakeSchema    string `json:"snowflakeSchema,omitempty"`
		Role               string `json:"role,omitempty"`
		SnowflakeRole      string `json:"snowflakeRole,omitempty"`
		User               string `json:"user,omitempty"`
		SnowflakeUser      string `json:"snowflakeUser,omitempty"`
	}
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writePreflightBlocking(w, "invalid_json", "Invalid JSON body")
		return
	}

	sqlText, errorCode, err := validateUserSQL(body.SQL)
	if err != nil {
		writePreflightBlocking(w, errorCode, err.Error())
		return
	}

	cfg, err := h.authConfig(authRequestOverrides{
		AccountIdentifier: firstNonEmpty(body.AccountIdentifier, body.SnowflakeAccountID),
		Warehouse:         firstNonEmpty(body.Warehouse, body.SnowflakeWarehouse),
		Database:          firstNonEmpty(body.Database, body.SnowflakeDatabase),
		Schema:            firstNonEmpty(body.Schema, body.SnowflakeSchema),
		Role:              firstNonEmpty(body.Role, body.SnowflakeRole),
		User:              firstNonEmpty(body.User, body.SnowflakeUser),
	})
	if err != nil {
		writePreflightBlocking(w, "auth_error", err.Error())
		return
	}

	_, err = h.executeSQLLifecycle(ctx, cfg, "EXPLAIN "+sqlText, 200)
	if err != nil {
		if isNonBlockingExplainError(err) {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":               true,
				"severity":         "warning",
				"readOnly":         true,
				"explainAttempted": true,
				"explainSucceeded": false,
				"warnings":         []string{strings.TrimSpace(err.Error())},
			})
			return
		}
		writePreflightBlocking(w, "preflight_explain_failed", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"severity":         "ok",
		"readOnly":         true,
		"explainAttempted": true,
		"explainSucceeded": true,
		"warnings":         []string{},
	})
}

func (h *Handler) handleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// The request context bounds only how long this legacy synchronous route
	// waits. It is not a provider cancellation signal; durable query execution
	// uses SubmitAsync/PollAsync instead.
	ctx := r.Context()

	var body struct {
		SQL                string `json:"sql"`
		AccountIdentifier  string `json:"account_identifier,omitempty"`
		SnowflakeAccountID string `json:"snowflakeAccountIdentifier,omitempty"`
		Warehouse          string `json:"warehouse,omitempty"`
		SnowflakeWarehouse string `json:"snowflakeWarehouse,omitempty"`
		Database           string `json:"database,omitempty"`
		SnowflakeDatabase  string `json:"snowflakeDatabase,omitempty"`
		Schema             string `json:"schema,omitempty"`
		SnowflakeSchema    string `json:"snowflakeSchema,omitempty"`
		Role               string `json:"role,omitempty"`
		SnowflakeRole      string `json:"snowflakeRole,omitempty"`
		User               string `json:"user,omitempty"`
		SnowflakeUser      string `json:"snowflakeUser,omitempty"`
	}
	if err := decodeJSONLimit(r.Body, &body, 1<<20); err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	sqlText, _, err := validateUserSQL(body.SQL)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}

	cfg, err := h.authConfig(authRequestOverrides{
		AccountIdentifier: firstNonEmpty(body.AccountIdentifier, body.SnowflakeAccountID),
		Warehouse:         firstNonEmpty(body.Warehouse, body.SnowflakeWarehouse),
		Database:          firstNonEmpty(body.Database, body.SnowflakeDatabase),
		Schema:            firstNonEmpty(body.Schema, body.SnowflakeSchema),
		Role:              firstNonEmpty(body.Role, body.SnowflakeRole),
		User:              firstNonEmpty(body.User, body.SnowflakeUser),
	})
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	result, err := h.executeSQLLifecycle(ctx, cfg, sqlText, h.queryRowLimit())
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
			if strings.TrimSpace(qerr.Handle) != "" {
				payload["query_metadata"] = map[string]any{
					"provider":               "snowflake",
					"snowflake_query_handle": strings.TrimSpace(qerr.Handle),
					"job_id":                 strings.TrimSpace(qerr.Handle),
					"request_id":             nilIfEmpty(strings.TrimSpace(qerr.RequestID)),
					"schema":                 qerr.Schema,
				}
			}
			writeJSON(w, qerr.StatusCode, payload)
			return
		}
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"rows": result.Rows,
		"query_metadata": map[string]any{
			"provider":               "snowflake",
			"duration_ms":            result.DurationMs,
			"snowflake_query_handle": result.Handle,
			"job_id":                 result.Handle,
			"request_id":             nilIfEmpty(result.RequestID),
			"status":                 result.TerminalStatus,
			"schema":                 result.Schema,
		},
	})
}

func (h *Handler) handleDatabases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	cfg, err := h.authConfig(authRequestOverrides{
		AccountIdentifier: strings.TrimSpace(r.URL.Query().Get("account_identifier")),
		User:              strings.TrimSpace(r.URL.Query().Get("user")),
	})
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	result, err := h.executeSQLLifecycle(ctx, cfg, "SHOW DATABASES", 1000)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	databases := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(rowValueCaseInsensitive(row, "name"))
		if name == "" {
			name = strings.TrimSpace(firstMapValue(row))
		}
		if name == "" {
			continue
		}
		databases = append(databases, map[string]any{"name": name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": databases})
}

func (h *Handler) handleSchemas(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	overrides := authRequestOverrides{
		AccountIdentifier: strings.TrimSpace(r.URL.Query().Get("account_identifier")),
		Database:          strings.TrimSpace(r.URL.Query().Get("database_name")),
		User:              strings.TrimSpace(r.URL.Query().Get("user")),
	}
	cfg, err := h.authConfig(overrides)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	databaseName := strings.TrimSpace(r.URL.Query().Get("database_name"))
	if databaseName == "" {
		databaseName = strings.TrimSpace(cfg.Database)
	}
	if databaseName == "" {
		writeSnowflakeError(w, http.StatusBadRequest, "Missing database_name")
		return
	}
	dbExpr, err := quoteIdentifier(databaseName)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.executeSQLLifecycle(ctx, cfg, "SHOW SCHEMAS IN DATABASE "+dbExpr, 2000)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	schemas := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(rowValueCaseInsensitive(row, "name"))
		if name == "" {
			name = strings.TrimSpace(firstMapValue(row))
		}
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

	overrides := authRequestOverrides{
		AccountIdentifier: strings.TrimSpace(r.URL.Query().Get("account_identifier")),
		Database:          strings.TrimSpace(r.URL.Query().Get("database_name")),
		Schema:            strings.TrimSpace(r.URL.Query().Get("schema_name")),
		User:              strings.TrimSpace(r.URL.Query().Get("user")),
	}
	cfg, err := h.authConfig(overrides)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	databaseName := strings.TrimSpace(r.URL.Query().Get("database_name"))
	schemaName := strings.TrimSpace(r.URL.Query().Get("schema_name"))
	if databaseName == "" {
		databaseName = strings.TrimSpace(cfg.Database)
	}
	if schemaName == "" {
		schemaName = strings.TrimSpace(cfg.Schema)
	}
	if databaseName == "" || schemaName == "" {
		writeSnowflakeError(w, http.StatusBadRequest, "Missing database_name or schema_name")
		return
	}
	dbExpr, err := quoteIdentifier(databaseName)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}
	schExpr, err := quoteIdentifier(schemaName)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.executeSQLLifecycle(ctx, cfg, "SHOW TABLES IN SCHEMA "+dbExpr+"."+schExpr, 5000)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	tables := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(rowValueCaseInsensitive(row, "name"))
		if name == "" {
			name = strings.TrimSpace(firstMapValue(row))
		}
		if name == "" {
			continue
		}
		tables = append(tables, map[string]any{
			"name": name,
			"type": nilIfEmpty(strings.TrimSpace(rowValueCaseInsensitive(row, "kind"))),
		})
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

	q := r.URL.Query()
	databaseName := strings.TrimSpace(q.Get("database_name"))
	schemaName := strings.TrimSpace(q.Get("schema_name"))
	tableName := strings.TrimSpace(q.Get("table_name"))
	fullName := strings.TrimSpace(q.Get("full_name"))
	if fullName != "" && (databaseName == "" || schemaName == "" || tableName == "") {
		parts := strings.Split(fullName, ".")
		if len(parts) == 3 {
			databaseName = strings.TrimSpace(parts[0])
			schemaName = strings.TrimSpace(parts[1])
			tableName = strings.TrimSpace(parts[2])
		}
	}

	overrides := authRequestOverrides{
		AccountIdentifier: strings.TrimSpace(q.Get("account_identifier")),
		Database:          databaseName,
		Schema:            schemaName,
		User:              strings.TrimSpace(q.Get("user")),
	}
	cfg, err := h.authConfig(overrides)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	if databaseName == "" {
		databaseName = strings.TrimSpace(cfg.Database)
	}
	if schemaName == "" {
		schemaName = strings.TrimSpace(cfg.Schema)
	}
	if databaseName == "" || schemaName == "" || tableName == "" {
		writeSnowflakeError(w, http.StatusBadRequest, "Missing database_name, schema_name, or table_name")
		return
	}

	dbExpr, err := quoteIdentifier(databaseName)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}
	schExpr, err := quoteIdentifier(schemaName)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tblExpr, err := quoteIdentifier(tableName)
	if err != nil {
		writeSnowflakeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.executeSQLLifecycle(ctx, cfg, "DESCRIBE TABLE "+dbExpr+"."+schExpr+"."+tblExpr, 1000)
	if err != nil {
		writeSnowflakeError(w, httpStatusForErr(err), err.Error())
		return
	}

	schemaRows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		name := strings.TrimSpace(rowValueCaseInsensitive(row, "name"))
		if name == "" {
			name = strings.TrimSpace(firstMapValue(row))
		}
		if name == "" {
			continue
		}
		schemaRows = append(schemaRows, map[string]any{
			"name":    name,
			"type":    nilIfEmpty(strings.TrimSpace(rowValueCaseInsensitive(row, "type"))),
			"comment": nilIfEmpty(strings.TrimSpace(rowValueCaseInsensitive(row, "comment"))),
		})
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"table":  databaseName + "." + schemaName + "." + tableName,
		"schema": schemaRows,
	})
}

// Query lifecycle implementation

type snowflakeStatementColumn struct {
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

type snowflakePartitionInfo struct {
	RowCount         int64 `json:"rowCount,omitempty"`
	CompressedSize   int64 `json:"compressedSize,omitempty"`
	UncompressedSize int64 `json:"uncompressedSize,omitempty"`
}

type snowflakeResultSetMetaData struct {
	NumRows       int64                      `json:"numRows,omitempty"`
	Format        string                     `json:"format,omitempty"`
	RowType       []snowflakeStatementColumn `json:"rowType,omitempty"`
	PartitionInfo []snowflakePartitionInfo   `json:"partitionInfo,omitempty"`
}

type snowflakeStatementResponse struct {
	Code               string                      `json:"code,omitempty"`
	SQLState           string                      `json:"sqlState,omitempty"`
	Message            string                      `json:"message,omitempty"`
	Success            bool                        `json:"success,omitempty"`
	StatementHandle    string                      `json:"statementHandle,omitempty"`
	StatementStatusURL string                      `json:"statementStatusUrl,omitempty"`
	Status             string                      `json:"status,omitempty"`
	StatementStatus    string                      `json:"statementStatus,omitempty"`
	Data               any                         `json:"data,omitempty"`
	ResultSetMetaData  *snowflakeResultSetMetaData `json:"resultSetMetaData,omitempty"`
	RowType            []snowflakeStatementColumn  `json:"rowType,omitempty"`
	PartitionInfo      []snowflakePartitionInfo    `json:"partitionInfo,omitempty"`
}

type snowflakeQueryResult struct {
	Rows           []map[string]any
	Schema         []map[string]any
	Handle         string
	RequestID      string
	DurationMs     int64
	TerminalStatus string
}

type queryExecutionError struct {
	StatusCode  int
	Message     string
	Handle      string
	RequestID   string
	Schema      []map[string]any
	PartialRows []map[string]any
}

func (e queryExecutionError) Error() string { return e.Message }

func (h *Handler) executeSQLLifecycle(ctx context.Context, cfg snowflakeAuthConfig, sqlText string, maxRows int) (snowflakeQueryResult, error) {
	start := h.now()
	requestID := strings.TrimSpace(cfg.RequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(h.newRequestID())
	}
	cfg.RequestID = requestID

	h.emitQueryLog("", "", requestID, "submit", "start")
	resp, err := h.submitStatement(ctx, cfg, sqlText)
	if err != nil {
		h.observeSubmitLatency(start)
		return snowflakeQueryResult{}, err
	}

	handle := strings.TrimSpace(resp.StatementHandle)
	if handle == "" {
		if rows := statementRows(resp.Data); len(rows) > 0 {
			handle = "adhoc"
		} else {
			h.observeSubmitLatency(start)
			return snowflakeQueryResult{}, errors.New("Snowflake statement submission failed: missing statement handle")
		}
	}
	jobID := handle
	h.emitQueryLog(handle, jobID, requestID, "submit", "accepted")

	columns := statementColumns(resp)
	rows := appendRowsLimited(nil, columns, statementRows(resp.Data), maxRows)
	inlineConsumed := len(statementRows(resp.Data)) > 0
	partitionCount := statementPartitionCount(resp, inlineConsumed)
	status := normalizeStatementStatus(resp)

	if isStatementFailure(status) {
		h.metrics.IncCounter("snowflake_terminal_total")
		h.observeSubmitLatency(start)
		h.emitQueryLog(handle, jobID, requestID, "terminal", "failed")
		return snowflakeQueryResult{}, queryExecutionError{
			StatusCode:  http.StatusBadGateway,
			Message:     statementErrorMessage(resp),
			Handle:      handle,
			RequestID:   requestID,
			Schema:      statementSchema(columns),
			PartialRows: rows,
		}
	}

	for !isStatementSuccess(status) {
		if ctx.Err() != nil {
			h.metrics.IncCounter("snowflake_terminal_total")
			h.observeSubmitLatency(start)
			h.emitQueryLog(handle, jobID, requestID, "wait", "interrupted")
			return snowflakeQueryResult{}, ctx.Err()
		}

		h.metrics.IncCounter("snowflake_poll_attempt_total")
		h.emitQueryLog(handle, jobID, requestID, "poll", "attempt")
		if err := h.sleep(ctx, defaultSnowflakePollSleep); err != nil {
			h.metrics.IncCounter("snowflake_terminal_total")
			h.observeSubmitLatency(start)
			h.emitQueryLog(handle, jobID, requestID, "wait", "interrupted")
			return snowflakeQueryResult{}, err
		}

		polled, err := h.pollStatement(ctx, cfg, handle)
		if err != nil {
			h.observeSubmitLatency(start)
			return snowflakeQueryResult{}, err
		}
		if len(columns) == 0 {
			columns = statementColumns(polled)
		}
		if !inlineConsumed {
			rows = appendRowsLimited(rows, columns, statementRows(polled.Data), maxRows)
			if len(statementRows(polled.Data)) > 0 {
				inlineConsumed = true
			}
		}
		pc := statementPartitionCount(polled, inlineConsumed)
		if pc > partitionCount {
			partitionCount = pc
		}

		status = normalizeStatementStatus(polled)
		if isStatementFailure(status) {
			h.metrics.IncCounter("snowflake_terminal_total")
			h.observeSubmitLatency(start)
			h.emitQueryLog(handle, jobID, requestID, "terminal", "failed")
			return snowflakeQueryResult{}, queryExecutionError{
				StatusCode:  http.StatusBadGateway,
				Message:     statementErrorMessage(polled),
				Handle:      handle,
				RequestID:   requestID,
				Schema:      statementSchema(columns),
				PartialRows: rows,
			}
		}
		if !isStatementSuccess(status) && status == "" {
			// Empty status without failure details can happen transiently while polling.
			status = "RUNNING"
		}
	}

	for partition := 0; partition < partitionCount; partition++ {
		if maxRows > 0 && len(rows) >= maxRows {
			break
		}
		if partition == 0 && inlineConsumed {
			continue
		}
		h.emitQueryLog(handle, jobID, requestID, "partition", "fetch")
		partResp, err := h.fetchPartition(ctx, cfg, handle, partition)
		if err != nil {
			var herr httpError
			if errors.As(err, &herr) && (herr.status == http.StatusNotFound || herr.status == http.StatusGone) {
				h.metrics.IncCounter("snowflake_terminal_total")
				h.observeSubmitLatency(start)
				h.emitQueryLog(handle, jobID, requestID, "terminal", "failed")
				return snowflakeQueryResult{}, queryExecutionError{
					StatusCode:  http.StatusGone,
					Message:     "Snowflake result partition expired (HTTP 404/410). Re-run the query.",
					Handle:      handle,
					RequestID:   requestID,
					Schema:      statementSchema(columns),
					PartialRows: rows,
				}
			}
			h.metrics.IncCounter("snowflake_terminal_total")
			h.observeSubmitLatency(start)
			h.emitQueryLog(handle, jobID, requestID, "terminal", "failed")
			return snowflakeQueryResult{}, queryExecutionError{
				StatusCode:  httpStatusForErr(err),
				Message:     err.Error(),
				Handle:      handle,
				RequestID:   requestID,
				Schema:      statementSchema(columns),
				PartialRows: rows,
			}
		}
		if len(columns) == 0 {
			columns = statementColumns(partResp)
		}
		rows = appendRowsLimited(rows, columns, statementRows(partResp.Data), maxRows)
	}

	h.metrics.IncCounter("snowflake_terminal_total")
	h.observeSubmitLatency(start)
	h.emitQueryLog(handle, jobID, requestID, "terminal", "completed")
	return snowflakeQueryResult{
		Rows:           rows,
		Schema:         statementSchema(columns),
		Handle:         handle,
		RequestID:      requestID,
		DurationMs:     h.now().Sub(start).Milliseconds(),
		TerminalStatus: "completed",
	}, nil
}

func (h *Handler) submitStatement(ctx context.Context, cfg snowflakeAuthConfig, sqlText string) (snowflakeStatementResponse, error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return snowflakeStatementResponse{}, errors.New("missing sql")
	}

	payload := map[string]any{
		"statement": sqlText,
		"timeout":   defaultSnowflakeStatementTimeoutSec,
	}
	if strings.TrimSpace(cfg.Warehouse) != "" {
		payload["warehouse"] = strings.TrimSpace(cfg.Warehouse)
	}
	if strings.TrimSpace(cfg.Database) != "" {
		payload["database"] = strings.TrimSpace(cfg.Database)
	}
	if strings.TrimSpace(cfg.Schema) != "" {
		payload["schema"] = strings.TrimSpace(cfg.Schema)
	}
	if strings.TrimSpace(cfg.Role) != "" {
		payload["role"] = strings.TrimSpace(cfg.Role)
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return snowflakeStatementResponse{}, err
	}

	q := url.Values{}
	q.Set("async", "true")
	if strings.TrimSpace(cfg.RequestID) != "" {
		q.Set("requestId", strings.TrimSpace(cfg.RequestID))
	}
	path := "/api/v2/statements?" + q.Encode()
	allowRetry := strings.TrimSpace(cfg.RequestID) != ""
	respBody, _, err := h.doSnowflakeRaw(ctx, cfg, http.MethodPost, path, body, allowRetry)
	if err != nil {
		return snowflakeStatementResponse{}, err
	}

	var resp snowflakeStatementResponse
	if len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return snowflakeStatementResponse{}, httpError{status: http.StatusBadGateway, msg: "Invalid JSON from Snowflake"}
		}
	}
	return resp, nil
}

func (h *Handler) pollStatement(ctx context.Context, cfg snowflakeAuthConfig, handle string) (snowflakeStatementResponse, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return snowflakeStatementResponse{}, errors.New("statement handle is required")
	}
	path := "/api/v2/statements/" + url.PathEscape(handle)
	if strings.TrimSpace(cfg.RequestID) != "" {
		path += "?requestId=" + url.QueryEscape(strings.TrimSpace(cfg.RequestID))
	}
	respBody, _, err := h.doSnowflakeRaw(ctx, cfg, http.MethodGet, path, nil, true)
	if err != nil {
		return snowflakeStatementResponse{}, err
	}
	var resp snowflakeStatementResponse
	if len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return snowflakeStatementResponse{}, httpError{status: http.StatusBadGateway, msg: "Invalid JSON from Snowflake"}
		}
	}
	if strings.TrimSpace(resp.StatementHandle) == "" {
		resp.StatementHandle = handle
	}
	return resp, nil
}

func (h *Handler) fetchPartition(ctx context.Context, cfg snowflakeAuthConfig, handle string, partition int) (snowflakeStatementResponse, error) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return snowflakeStatementResponse{}, errors.New("statement handle is required")
	}
	q := url.Values{}
	q.Set("partition", strconv.Itoa(partition))
	if strings.TrimSpace(cfg.RequestID) != "" {
		q.Set("requestId", strings.TrimSpace(cfg.RequestID))
	}
	path := "/api/v2/statements/" + url.PathEscape(handle) + "?" + q.Encode()
	respBody, _, err := h.doSnowflakeRaw(ctx, cfg, http.MethodGet, path, nil, true)
	if err != nil {
		return snowflakeStatementResponse{}, err
	}
	var resp snowflakeStatementResponse
	if len(bytes.TrimSpace(respBody)) > 0 {
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return snowflakeStatementResponse{}, httpError{status: http.StatusBadGateway, msg: "Invalid JSON from Snowflake"}
		}
	}
	if strings.TrimSpace(resp.StatementHandle) == "" {
		resp.StatementHandle = handle
	}
	return resp, nil
}

func (h *Handler) cancelStatement(cfg snowflakeAuthConfig, handle string) {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	h.metrics.IncCounter("snowflake_cancel_attempt_total")
	h.emitQueryLog(handle, handle, strings.TrimSpace(cfg.RequestID), "cancel", "attempt")
	path := "/api/v2/statements/" + url.PathEscape(handle) + "/cancel"
	if strings.TrimSpace(cfg.RequestID) != "" {
		path += "?requestId=" + url.QueryEscape(strings.TrimSpace(cfg.RequestID))
	}
	_, _, _ = h.doSnowflakeRaw(ctx, cfg, http.MethodPost, path, nil, false)
}

func (h *Handler) doSnowflakeRaw(ctx context.Context, cfg snowflakeAuthConfig, method, requestPath string, body []byte, allowRetry bool) ([]byte, int, error) {
	client := h.httpc
	if client == nil {
		client = &http.Client{Timeout: 0}
	}

	attempts := 1
	if allowRetry {
		attempts = 4
	}

	for attempt := 0; attempt < attempts; attempt++ {
		requestURL := requestPath
		// Snowflake requires retry=true when replaying an idempotent statement
		// submission. Reusing requestId alone is not the documented replay
		// protocol and can create ambiguous duplicate work.
		if attempt > 0 && method == http.MethodPost && strings.Contains(requestURL, "/api/v2/statements?") && !strings.Contains(requestURL, "/cancel") {
			if parsed, parseErr := url.Parse(requestURL); parseErr == nil {
				query := parsed.Query()
				query.Set("retry", "true")
				parsed.RawQuery = query.Encode()
				requestURL = parsed.String()
			}
		}
		if !strings.HasPrefix(requestURL, "http://") && !strings.HasPrefix(requestURL, "https://") {
			if !strings.HasPrefix(requestURL, "/") {
				requestURL = "/" + requestURL
			}
			requestURL = strings.TrimRight(cfg.BaseURL, "/") + requestURL
		}

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
			return nil, 0, err
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
			cancelReq()
			if reqCtx.Err() != nil {
				if ctx.Err() != nil {
					return nil, 0, ctx.Err()
				}
				return nil, 0, reqCtx.Err()
			}
			if allowRetry && attempt+1 < attempts && isTransientNetErr(err) {
				if serr := h.sleep(ctx, retryAfterDuration("", attempt)); serr != nil {
					return nil, 0, serr
				}
				continue
			}
			return nil, 0, httpError{status: http.StatusBadGateway, msg: fmt.Sprintf("failed to reach Snowflake API: %v", err)}
		}

		payload, readErr := readLimitedBody(resp.Body, snowflakeResponseBodyLimit)
		_ = resp.Body.Close()
		cancelReq()
		if readErr != nil {
			return nil, 0, readErr
		}

		status := resp.StatusCode
		if status >= 200 && status < 300 {
			return payload, status, nil
		}

		msg := snowflakeHTTPMessage(status, payload)
		if allowRetry && retryableStatus(status) && attempt+1 < attempts {
			if serr := h.sleep(ctx, retryAfterDuration(resp.Header.Get("Retry-After"), attempt)); serr != nil {
				return nil, 0, serr
			}
			continue
		}
		return payload, status, httpError{status: status, msg: msg}
	}

	return nil, 0, httpError{status: http.StatusBadGateway, msg: "Snowflake request failed"}
}

func (h *Handler) requestTimeout() time.Duration {
	if h == nil || h.upstreamRequestTimeout <= 0 {
		return defaultSnowflakeUpstreamReqTimeout
	}
	return h.upstreamRequestTimeout
}

func (h *Handler) snowflakeHeaders(cfg snowflakeAuthConfig) map[string]string {
	headers := map[string]string{
		"Authorization": cfg.Authorization,
		"Accept":        "application/json",
		"User-Agent":    "ai-analyst/snowflake-v1",
	}
	if strings.TrimSpace(cfg.TokenType) != "" {
		headers["X-Snowflake-Authorization-Token-Type"] = strings.TrimSpace(cfg.TokenType)
	}
	if strings.TrimSpace(cfg.RequestID) != "" {
		headers["X-Request-ID"] = strings.TrimSpace(cfg.RequestID)
	}
	return headers
}

func statementRows(data any) [][]any {
	switch v := data.(type) {
	case nil:
		return nil
	case [][]any:
		return v
	case []any:
		rows := make([][]any, 0, len(v))
		for _, rawRow := range v {
			switch row := rawRow.(type) {
			case []any:
				rows = append(rows, row)
			case map[string]any:
				keys := make([]string, 0, len(row))
				for key := range row {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				values := make([]any, 0, len(keys))
				for _, key := range keys {
					values = append(values, row[key])
				}
				rows = append(rows, values)
			default:
				rows = append(rows, []any{row})
			}
		}
		return rows
	default:
		return nil
	}
}

func statementColumns(resp snowflakeStatementResponse) []snowflakeStatementColumn {
	if resp.ResultSetMetaData != nil && len(resp.ResultSetMetaData.RowType) > 0 {
		return append([]snowflakeStatementColumn(nil), resp.ResultSetMetaData.RowType...)
	}
	if len(resp.RowType) > 0 {
		return append([]snowflakeStatementColumn(nil), resp.RowType...)
	}
	return nil
}

func statementPartitionCount(resp snowflakeStatementResponse, inline bool) int {
	if resp.ResultSetMetaData != nil && len(resp.ResultSetMetaData.PartitionInfo) > 0 {
		return len(resp.ResultSetMetaData.PartitionInfo)
	}
	if len(resp.PartitionInfo) > 0 {
		return len(resp.PartitionInfo)
	}
	if inline {
		return 1
	}
	return 0
}

func normalizeStatementStatus(resp snowflakeStatementResponse) string {
	status := strings.ToUpper(strings.TrimSpace(firstNonEmpty(resp.StatementStatus, resp.Status)))
	if status != "" {
		return status
	}
	if resp.Success {
		return "SUCCESS"
	}
	if len(statementRows(resp.Data)) > 0 {
		return "SUCCESS"
	}
	if strings.TrimSpace(resp.StatementStatusURL) != "" {
		return "RUNNING"
	}
	return ""
}

func isStatementSuccess(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "SUCCESS", "SUCCEEDED", "DONE", "COMPLETE", "COMPLETED", "FINISHED":
		return true
	default:
		return false
	}
}

func isStatementFailure(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FAILED", "FAILED_WITH_ERROR", "ABORTED", "CANCELLED", "CANCELED", "ERROR":
		return true
	default:
		return false
	}
}

func statementErrorMessage(resp snowflakeStatementResponse) string {
	msg := strings.TrimSpace(resp.Message)
	if msg == "" {
		if code := strings.TrimSpace(resp.Code); code != "" {
			msg = "Snowflake request failed (code=" + code + ")"
		} else {
			msg = "Snowflake query failed"
		}
	}
	return msg
}

func statementSchema(columns []snowflakeStatementColumn) []map[string]any {
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

func appendRowsLimited(existing []map[string]any, columns []snowflakeStatementColumn, data [][]any, maxRows int) []map[string]any {
	if len(data) == 0 {
		return existing
	}
	if existing == nil {
		existing = make([]map[string]any, 0, len(data))
	}
	for _, raw := range data {
		if maxRows > 0 && len(existing) >= maxRows {
			return existing
		}
		row := map[string]any{}
		for i, value := range raw {
			name := fmt.Sprintf("col_%d", i)
			if i < len(columns) {
				if candidate := strings.TrimSpace(columns[i].Name); candidate != "" {
					name = candidate
				}
			}
			row[name] = value
		}
		existing = append(existing, row)
	}
	return existing
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

func firstMapValue(row map[string]any) string {
	if len(row) == 0 {
		return ""
	}
	keys := make([]string, 0, len(row))
	for key := range row {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return fmt.Sprintf("%v", row[keys[0]])
}

func snowflakeHTTPMessage(status int, payload []byte) string {
	if len(payload) > 0 {
		var parsed map[string]any
		if err := json.Unmarshal(payload, &parsed); err == nil {
			if msg := strings.TrimSpace(anyString(parsed["message"])); msg != "" {
				return msg
			}
			if data, ok := parsed["data"].(map[string]any); ok {
				if msg := strings.TrimSpace(anyString(data["message"])); msg != "" {
					return msg
				}
				if msg := strings.TrimSpace(anyString(data["errorMessage"])); msg != "" {
					return msg
				}
			}
		}
		if msg := strings.TrimSpace(string(payload)); msg != "" {
			return msg
		}
	}
	return fmt.Sprintf("Snowflake API request failed (HTTP %d)", status)
}

func anyString(v any) string {
	s, _ := v.(string)
	return s
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
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

func readLimitedBody(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = snowflakeResponseBodyLimit
	}
	payload, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > limit {
		return nil, httpError{status: http.StatusBadGateway, msg: "Snowflake response exceeded maximum size"}
	}
	return payload, nil
}

func isTransientNetErr(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return netErr.Timeout() || netErr.Temporary()
	}
	return false
}

func validateUserSQL(sqlText string) (string, string, error) {
	sqlText = strings.TrimSpace(sqlText)
	if sqlText == "" {
		return "", "missing_sql", errors.New("Missing sql")
	}
	keyword := firstSQLKeyword(sqlText)
	if keyword == "PUT" || keyword == "GET" {
		return "", "unsupported_command", errors.New("Snowflake SQL API does not support PUT/GET commands")
	}
	if !sqlutil.IsReadOnlySQL(sqlText) {
		return "", "read_only_required", errors.New("Only read-only SELECT/WITH queries are allowed")
	}
	return sqlText, "", nil
}

func firstSQLKeyword(sqlText string) string {
	s := strings.TrimSpace(sqlText)
	for {
		s = strings.TrimSpace(s)
		switch {
		case strings.HasPrefix(s, "--"):
			idx := strings.IndexByte(s, '\n')
			if idx < 0 {
				return ""
			}
			s = s[idx+1:]
		case strings.HasPrefix(s, "/*"):
			idx := strings.Index(s, "*/")
			if idx < 0 {
				return ""
			}
			s = s[idx+2:]
		default:
			goto done
		}
	}
done:
	if s == "" {
		return ""
	}
	end := 0
	for end < len(s) {
		c := s[end]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || c == '_' {
			end++
			continue
		}
		break
	}
	if end == 0 {
		return ""
	}
	return strings.ToUpper(strings.TrimSpace(s[:end]))
}

func isNonBlockingExplainError(err error) bool {
	if err == nil {
		return false
	}
	var qerr queryExecutionError
	if errors.As(err, &qerr) {
		return true
	}
	var herr httpError
	if errors.As(err, &herr) {
		if herr.status >= 400 && herr.status < 500 {
			return true
		}
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "explain") || strings.Contains(msg, "permission") || strings.Contains(msg, "not authorized") || strings.Contains(msg, "unsupported") {
		return true
	}
	return false
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

func writePreflightBlocking(w http.ResponseWriter, errorCode, message string) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"ok":        false,
		"severity":  "error",
		"errorCode": strings.TrimSpace(errorCode),
		"message":   strings.TrimSpace(message),
	})
}

func writeSnowflakeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]any{
			"message": strings.TrimSpace(message),
		},
	})
}

type httpError struct {
	status int
	msg    string
}

func (e httpError) Error() string { return e.msg }

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
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func boolPtr(v bool) *bool {
	b := v
	return &b
}

func stringPtrOrNil(v string) *string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return nil
	}
	return &trimmed
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

func newSnowflakeRequestID() string {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		return fmt.Sprintf("00000000-0000-4000-8000-%012x", uint64(time.Now().UnixNano())&0xffffffffffff)
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(buf)
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func (h *Handler) now() time.Time {
	if h != nil && h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now()
}

func (h *Handler) sleep(ctx context.Context, d time.Duration) error {
	if h != nil && h.sleepFn != nil {
		return h.sleepFn(ctx, d)
	}
	return sleepWithContext(ctx, d)
}

func (h *Handler) newRequestID() string {
	if h != nil && h.newRequestIDFn != nil {
		return h.newRequestIDFn()
	}
	return newSnowflakeRequestID()
}

func (h *Handler) emitQueryLog(handle, jobID, requestID, event, status string) {
	if h == nil {
		return
	}
	provider := "snowflake"
	handle = strings.TrimSpace(handle)
	jobID = strings.TrimSpace(jobID)
	requestID = strings.TrimSpace(requestID)
	event = strings.TrimSpace(event)
	status = strings.TrimSpace(status)
	if h.logf != nil {
		h.logf("provider=%s snowflake_query_handle=%s job_id=%s request_id=%s event=%s status=%s", provider, handle, jobID, requestID, event, status)
	}
}

func (h *Handler) observeSubmitLatency(start time.Time) {
	if h == nil || h.metrics == nil {
		return
	}
	h.metrics.ObserveHistogram("snowflake_submit_latency_ms", float64(h.now().Sub(start).Milliseconds()))
}

type snowflakeMetrics struct {
	mu        sync.Mutex
	counters  map[string]int64
	histogram map[string][]float64
}

func newSnowflakeMetrics() *snowflakeMetrics {
	return &snowflakeMetrics{
		counters:  map[string]int64{},
		histogram: map[string][]float64{},
	}
}

func (m *snowflakeMetrics) IncCounter(name string) {
	if m == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counters[name]++
}

func (m *snowflakeMetrics) ObserveHistogram(name string, value float64) {
	if m == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.histogram[name] = append(m.histogram[name], value)
}

func (m *snowflakeMetrics) Counter(name string) int64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.counters[strings.TrimSpace(name)]
}

func (m *snowflakeMetrics) HistogramCount(name string) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.histogram[strings.TrimSpace(name)])
}
