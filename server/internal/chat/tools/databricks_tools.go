package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/queryadapters"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/databricks"
)

const databricksMetadataPageTokenPrefix = "dbx-metadata:"

type databricksMetadataPageTokenEnvelope struct {
	Version   int    `json:"v"`
	Resource  string `json:"resource"`
	Catalog   string `json:"catalog,omitempty"`
	Schema    string `json:"schema,omitempty"`
	Workspace string `json:"workspace,omitempty"`
	Cursor    string `json:"cursor"`
	Checksum  string `json:"checksum"`
}

type databricksMetadataPageTokenError struct {
	reason  string
	message string
}

func (e *databricksMetadataPageTokenError) Error() string {
	if e == nil || strings.TrimSpace(e.message) == "" {
		return "invalid or expired Databricks metadata page_token; restart this listing from its first page"
	}
	return e.message
}

func newDatabricksMetadataPageTokenError(reason, message string) error {
	return &databricksMetadataPageTokenError{
		reason:  strings.TrimSpace(reason),
		message: strings.TrimSpace(message),
	}
}

func databricksMetadataPageTokenChecksum(envelope databricksMetadataPageTokenEnvelope) string {
	canonical := struct {
		Version   int    `json:"v"`
		Resource  string `json:"resource"`
		Catalog   string `json:"catalog,omitempty"`
		Schema    string `json:"schema,omitempty"`
		Workspace string `json:"workspace,omitempty"`
		Cursor    string `json:"cursor"`
	}{
		Version:   envelope.Version,
		Resource:  envelope.Resource,
		Catalog:   envelope.Catalog,
		Schema:    envelope.Schema,
		Workspace: envelope.Workspace,
		Cursor:    envelope.Cursor,
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(payload)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func databricksMetadataWorkspaceScope(exec ExecContext) (string, error) {
	if exec.Databricks == nil {
		// Adapter/fake handlers do not expose connection settings. Their tokens
		// still receive an integrity checksum and scope validation for the
		// catalog/schema/resource tuple.
		return "", nil
	}
	host, err := exec.Databricks.NormalizedHost()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(host))))
	return hex.EncodeToString(digest[:16]), nil
}

func encodeDatabricksMetadataPageToken(resource, catalog, schema, workspace, cursor string) string {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return ""
	}
	envelope := databricksMetadataPageTokenEnvelope{
		Version:   2,
		Resource:  strings.TrimSpace(strings.ToLower(resource)),
		Catalog:   strings.TrimSpace(catalog),
		Schema:    strings.TrimSpace(schema),
		Workspace: strings.TrimSpace(workspace),
		Cursor:    cursor,
	}
	envelope.Checksum = databricksMetadataPageTokenChecksum(envelope)
	if envelope.Checksum == "" {
		return ""
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return ""
	}
	return databricksMetadataPageTokenPrefix + base64.RawURLEncoding.EncodeToString(payload)
}

func decodeDatabricksMetadataPageToken(token, resource, catalog, schema, workspace string) (string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return "", nil
	}
	if !strings.HasPrefix(token, databricksMetadataPageTokenPrefix) {
		return "", newDatabricksMetadataPageTokenError("invalid_format", "invalid or expired Databricks metadata page_token; restart this listing from its first page")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, databricksMetadataPageTokenPrefix))
	if err != nil {
		return "", newDatabricksMetadataPageTokenError("invalid_encoding", "invalid or expired Databricks metadata page_token; restart this listing from its first page")
	}
	var envelope databricksMetadataPageTokenEnvelope
	if err := json.Unmarshal(decoded, &envelope); err != nil {
		return "", newDatabricksMetadataPageTokenError("invalid_payload", "invalid or expired Databricks metadata page_token; restart this listing from its first page")
	}
	if envelope.Version != 2 {
		return "", newDatabricksMetadataPageTokenError("unsupported_version", "expired Databricks metadata page_token version; restart this listing from its first page")
	}
	wantChecksum := databricksMetadataPageTokenChecksum(envelope)
	if wantChecksum == "" || subtle.ConstantTimeCompare([]byte(strings.TrimSpace(envelope.Checksum)), []byte(wantChecksum)) != 1 {
		return "", newDatabricksMetadataPageTokenError("integrity_check_failed", "corrupted Databricks metadata page_token; restart this listing from its first page")
	}
	if strings.TrimSpace(strings.ToLower(envelope.Resource)) != strings.TrimSpace(strings.ToLower(resource)) ||
		strings.TrimSpace(envelope.Catalog) != strings.TrimSpace(catalog) ||
		strings.TrimSpace(envelope.Schema) != strings.TrimSpace(schema) {
		return "", newDatabricksMetadataPageTokenError("listing_scope_mismatch", "mismatched Databricks metadata page_token; restart this listing from its first page")
	}
	if strings.TrimSpace(envelope.Workspace) != strings.TrimSpace(workspace) {
		return "", newDatabricksMetadataPageTokenError("workspace_mismatch", "Databricks workspace changed since this page_token was issued; restart this listing from its first page")
	}
	if strings.TrimSpace(envelope.Cursor) == "" {
		return "", newDatabricksMetadataPageTokenError("missing_cursor", "invalid Databricks metadata page_token; restart this listing from its first page")
	}
	return strings.TrimSpace(envelope.Cursor), nil
}

func databricksMetadataPageTokenRestartOutput(err error) Output {
	reason := "invalid_or_expired"
	message := "invalid or expired Databricks metadata page_token; restart this listing from its first page"
	var tokenErr *databricksMetadataPageTokenError
	if errors.As(err, &tokenErr) {
		if strings.TrimSpace(tokenErr.reason) != "" {
			reason = strings.TrimSpace(tokenErr.reason)
		}
		message = tokenErr.Error()
	} else if err != nil && strings.TrimSpace(err.Error()) != "" {
		message = strings.TrimSpace(err.Error())
	}
	return Output{
		OK:           false,
		ResultFormat: ResultFormatJSON,
		Result: map[string]any{
			"error":                   message,
			"status":                  "restart_required",
			"reason":                  reason,
			"page_token_invalid":      true,
			"restart_required":        true,
			"restart_from_first_page": true,
		},
		ResultMetadata: map[string]any{
			"provider":         "databricks",
			"kind":             "metadata_page_token_error",
			"status":           "restart_required",
			"reason":           reason,
			"restart_required": true,
		},
	}
}

func databricksProviderRejectedMetadataPageToken(status int, message string) bool {
	if status != http.StatusBadRequest && status != http.StatusUnprocessableEntity {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(message))
	if lower == "" {
		return false
	}
	if strings.Contains(lower, "input byte array has wrong") ||
		strings.Contains(lower, "illegal base64") ||
		strings.Contains(lower, "invalid base64") {
		return true
	}
	mentionsToken := strings.Contains(lower, "page_token") || strings.Contains(lower, "page token")
	mentionsInvalidity := strings.Contains(lower, "invalid") || strings.Contains(lower, "expired") ||
		strings.Contains(lower, "malformed") || strings.Contains(lower, "decode")
	return mentionsToken && mentionsInvalidity
}

func callDatabricks(h *databricks.Handler, fake http.Handler, ctx context.Context, method string, targetPath string, body any) (int, []byte, error) {
	if fake != nil {
		return callLocalDatasourceHandler(fake, ctx, method, targetPath, body)
	}
	if h == nil {
		return 0, nil, errors.New("databricks handler is not configured")
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	req := httptest.NewRequest(method, "http://local"+targetPath, bodyReader)
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	resp := rr.Result()
	defer resp.Body.Close()
	b, err := readLocalDatasourceResponse(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
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

func cloneStringAnyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
}

func databricksProgressMeta(phase string, meta map[string]any, offset int64, pageSize int) map[string]any {
	out := cloneStringAnyMap(meta)
	out["phase"] = phase
	out["offset"] = offset
	out["page_size"] = pageSize
	out["rows_processed"] = offset
	return out
}

func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func inferDatabricksTypeFromValue(v any) string {
	switch typed := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return "number"
	case string:
		return "string"
	case []any, []map[string]any:
		return "array"
	case map[string]any:
		return "object"
	default:
		if typed == nil {
			return "null"
		}
		return strings.TrimSpace(fmt.Sprintf("%T", typed))
	}
}

func inferDatabricksColumnTypes(rows []any) map[string]string {
	colTypes := map[string]string{}
	for _, r := range rows {
		m, ok := r.(map[string]any)
		if !ok {
			continue
		}
		for name, raw := range m {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if _, ok := colTypes[name]; ok {
				continue
			}
			t := inferDatabricksTypeFromValue(raw)
			if t != "" && t != "null" {
				colTypes[name] = t
			}
		}
	}
	return colTypes
}

func enrichDatabricksSchema(raw []map[string]any, rows []any) []map[string]any {
	if len(raw) == 0 {
		return []map[string]any{}
	}

	inferredTypes := inferDatabricksColumnTypes(rows)
	out := make([]map[string]any, 0, len(raw))
	for _, col := range raw {
		if col == nil {
			continue
		}

		name := strings.TrimSpace(asString(col["name"]))
		if name == "" {
			continue
		}

		t := firstNonEmpty(
			asString(col["type"]),
			asString(col["type_name"]),
			asString(col["type_text"]),
			asString(col["typeName"]),
			inferredTypes[name],
		)

		item := cloneStringAnyMap(col)
		item["name"] = name
		if strings.TrimSpace(asString(item["type"])) == "" && t != "" {
			item["type"] = t
		}
		out = append(out, item)
	}

	return out
}

func databricksQueryHistoryURL(host, statementID string) string {
	h := strings.TrimRight(strings.TrimSpace(host), "/")
	if !strings.HasPrefix(h, "https://") {
		h = "https://" + h
	}
	return fmt.Sprintf("%s/sql/history?filter=%%7B%%22statementId%%22%%3A%%22%s%%22%%7D", h, url.QueryEscape(strings.TrimSpace(statementID)))
}

func newRunDatabricksQueryTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "run_databricks_query",
				Description: "Profile and start a durable read-only Databricks SQL Warehouse query. It returns a managed job; use check_query_status and get_query_results rather than re-running SQL. To continue failed work, pass the exact query_obligation_id returned by the prior attempt.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"sql": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Databricks SQL query to execute.",
						},
						"description": map[string]any{
							"type":        "string",
							"maxLength":   300,
							"description": "One-sentence plain-English summary of what this query answers. Keep it brief (max 300 characters).",
						},
						"catalog": map[string]any{
							"type":        "string",
							"description": "Unity catalog name (optional).",
						},
						"schema": map[string]any{
							"type":        "string",
							"description": "Schema name (optional).",
						},
						"limit": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"maximum":     maxQueryPageSize,
							"description": "Maximum rows to return for this page. The platform enforces a run-level row cap (default 2500).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from previous page. Include to fetch the next page.",
						},
						"query_obligation_id": managedQueryObligationIDSchema(),
					},
					"required":             []string{"sql"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				Description       string          `json:"description,omitempty"`
				SQL               string          `json:"sql"`
				Catalog           string          `json:"catalog,omitempty"`
				Schema            string          `json:"schema,omitempty"`
				Limit             int             `json:"limit,omitempty"`
				FullData          bool            `json:"full_data,omitempty"`
				PageToken         string          `json:"page_token,omitempty"`
				ReplacesJobID     json.RawMessage `json:"replaces_job_id,omitempty"`
				QueryObligationID json.RawMessage `json:"query_obligation_id,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			replacesJobID, err := decodeOptionalManagedReplacementJobID(in.ReplacesJobID)
			if err != nil {
				return invalidManagedQueryRetryInput(err), nil
			}
			queryObligationID, err := decodeOptionalQueryObligationID(in.QueryObligationID)
			if err != nil {
				return invalidManagedQueryRetryInput(err), nil
			}
			if exec.Databricks == nil && exec.DatabricksHTTP == nil {
				return JSONError("Databricks is not configured."), nil
			}
			sql := normalizeQuerySQLInput(in.SQL)
			if sql == "" {
				return JSONError("Missing sql"), nil
			}
			description := normalizeQueryDescription(in.Description)
			requestedLimit, pageSize, isCapped, effectiveConfiguredLimit := resolveQueryPageSize(in.Limit, exec.DatabaseQueryRowLimit)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset > 0 && exec.Jobs != nil {
				return JSONError("run_databricks_query never re-executes SQL for pagination. Call get_query_results with the original job_id, owner_token, and page_token."), nil
			}

			queryFetchSize := pageSize
			if offset == 0 {
				queryFetchSize = effectiveConfiguredLimit
			}
			querySQL := makeDatabricksPagedQuery(sql, queryFetchSize+1, offset)
			host := ""
			warehouseID := ""
			if exec.Databricks != nil && exec.Jobs != nil {
				var err error
				host, err = exec.Databricks.NormalizedHost()
				if err != nil {
					return JSONError(err.Error()), nil
				}
				warehouseID = exec.Databricks.WarehouseID()
			}
			if offset == 0 && exec.Jobs != nil {
				if exec.Databricks == nil {
					return JSONError("Databricks managed queries require the native Databricks handler."), nil
				}
				if warehouseID == "" {
					return JSONError("Missing databricksWarehouseId in settings"), nil
				}
				catalog := strings.TrimSpace(in.Catalog)
				schema := strings.TrimSpace(in.Schema)
				return launchManagedQuery(ctx, exec, managedQueryLaunchRequest{
					ToolName:     "run_databricks_query",
					Provider:     "databricks",
					ConnectionID: host,
					SQL:          sql,
					SubmittedSQL: querySQL,
					Description:  description,
					Adapter:      queryadapters.NewDatabricks(exec.Databricks),
					Session:      map[string]string{"catalog": catalog, "schema": schema},
					Metadata: map[string]any{
						"host": host, "warehouse_id": warehouseID, "catalog": catalog, "schema": schema,
						"provider_max_result_bytes": databricks.MaxInlineStatementByteLimit,
					},
					Budgets:           jobsBudgets(effectiveConfiguredLimit, 0),
					RequestedLimit:    requestedLimit,
					PageSize:          pageSize,
					ConfiguredLimit:   effectiveConfiguredLimit,
					LimitCapped:       isCapped,
					FullData:          in.FullData,
					ReplacesJobID:     replacesJobID,
					QueryObligationID: queryObligationID,
				})
			}

			var v any
			var m map[string]any

			if offset == 0 {
				// Preflight only for initial page.
				st, b, err := callDatabricks(exec.Databricks, exec.DatabricksHTTP, ctx, http.MethodPost, "/api/databricks/preflight", map[string]any{
					"sql": sql,
				})
				_ = st
				if err != nil {
					return JSONError(err.Error()), nil
				}
				v, err = parseJSONBody(b)
				if err != nil {
					return JSONError("Databricks preflight failed"), nil
				}
				m, _ = v.(map[string]any)
				ok, _ := m["ok"].(bool)
				if ok != true {
					msg := "Databricks preflight failed"
					if em := extractErrMessage(m); em != "" {
						msg = em
					}
					return Output{
						OK:           false,
						ResultFormat: ResultFormatJSON,
						Result: map[string]any{
							"error":            msg,
							"preflight_failed": true,
							"rewrite_required": true,
							"instructions":     "Preflight failed; rewrite the SQL query and call run_databricks_query again. Do not show this preflight error in the UI.",
						},
					}, nil
				}
			}

			// Compatibility query path for installations without the managed
			// registry.
			if exec.Databricks == nil {
				return JSONError("Databricks native handler is not configured."), nil
			}
			if host == "" {
				var err error
				host, err = exec.Databricks.NormalizedHost()
				if err != nil {
					return JSONError(err.Error()), nil
				}
				warehouseID = exec.Databricks.WarehouseID()
			}
			if warehouseID == "" {
				return JSONError("Missing databricksWarehouseId in settings"), nil
			}
			var dbProgress databricks.QueryProgressFn
			if exec.OnProgress != nil {
				dbProgress = func(phase string, meta map[string]any) {
					if strings.TrimSpace(phase) == "complete" {
						return
					}
					exec.OnProgress(databricksProgressMeta(phase, meta, offset, pageSize))
				}
			}
			result, err := exec.Databricks.RunQueryWithProgress(ctx, host, warehouseID, querySQL, strings.TrimSpace(in.Catalog), strings.TrimSpace(in.Schema), dbProgress)
			if err != nil {
				return JSONError(err.Error()), nil
			}

			rowsForSchema := make([]any, 0, len(result.Rows))
			for _, row := range result.Rows {
				rowsForSchema = append(rowsForSchema, row)
			}
			schema := enrichDatabricksSchema(result.Schema, rowsForSchema)

			if exec.OnProgress != nil {
				rowsReturned := len(result.Rows)
				if rowsReturned > pageSize {
					rowsReturned = pageSize
				}
				rowsProcessed := offset + int64(rowsReturned)
				hasMore := len(result.Rows) > pageSize
				exec.OnProgress(map[string]any{
					"provider":       "databricks",
					"phase":          "complete",
					"state":          "SUCCEEDED",
					"statement_id":   result.StatementID,
					"warehouse_id":   warehouseID,
					"host":           host,
					"url":            databricksQueryHistoryURL(host, result.StatementID),
					"offset":         offset,
					"page_size":      pageSize,
					"rows_returned":  rowsReturned,
					"rows_processed": rowsProcessed,
					"row_count":      rowsProcessed,
					"has_more":       hasMore,
				})
			}

			qm := map[string]any{
				"provider":     "databricks",
				"sql":          sql,
				"sql_preview":  asyncSQLPreview(sql),
				"duration_ms":  result.DurationMs,
				"statement_id": result.StatementID,
				"url":          databricksQueryHistoryURL(host, result.StatementID),
			}
			if description != "" {
				qm["description"] = description
			}
			if len(schema) > 0 {
				qm["schema"] = schema
			}
			qm["full_data_requested"] = in.FullData
			return formatDatabricksSuccessOutput(result.Rows, pageSize, offset, requestedLimit, isCapped, effectiveConfiguredLimit, in.FullData, qm), nil
		},
	}
}

func formatDatabricksSuccessOutput(rowsIn []map[string]any, pageSize int, offset int64, requestedLimit int, isCapped bool, effectiveConfiguredLimit int, fullData bool, metadata map[string]any) Output {
	fullRows := rowsIn
	truncatedToConfiguredLimit := false
	if offset == 0 && effectiveConfiguredLimit > 0 && len(fullRows) > effectiveConfiguredLimit {
		fullRows = fullRows[:effectiveConfiguredLimit]
		truncatedToConfiguredLimit = true
	}

	rows := make([]any, 0, len(fullRows))
	for _, row := range fullRows {
		rows = append(rows, row)
	}
	hasMore := len(rows) > pageSize
	if hasMore {
		rows = rows[:pageSize]
	}
	nextOffset := offset + int64(pageSize)
	out := map[string]any{
		"rows":      rows,
		"page_size": pageSize,
		"offset":    offset,
		"has_more":  hasMore,
	}
	if hasMore && nextOffset >= offset {
		out["next_page_token"] = encodePageOffsetToken(nextOffset)
	}

	qm := cloneStringAnyMap(metadata)
	if qm == nil {
		qm = map[string]any{}
	}
	rowsReturned := len(rows)
	rowsProcessed := offset + int64(rowsReturned)
	qm["full_data_requested"] = fullData
	qm["requested_limit"] = requestedLimit
	qm["effective_limit"] = pageSize
	qm["configured_limit"] = effectiveConfiguredLimit
	qm["limit_capped"] = isCapped
	qm["rows_returned"] = rowsReturned
	qm["status"] = string(jobs.JobStatusCompleted)
	qm["rows_preview_count"] = rowsReturned
	qm["rows_processed"] = rowsProcessed
	qm["row_count"] = rowsProcessed
	qm["offset"] = offset
	qm["page_offset"] = offset
	qm["page_size"] = pageSize
	qm["has_more"] = hasMore
	if offset == 0 {
		qm["row_count_total"] = len(fullRows)
		qm["result_counts"] = map[string]any{
			"rows": len(fullRows),
		}
		if truncatedToConfiguredLimit {
			qm["truncated_to_configured_limit"] = true
		}
	}
	if hasMore && nextOffset >= offset {
		qm["next_page_token"] = encodePageOffsetToken(nextOffset)
	}

	var artifactPayload *ArtifactPayload
	resultMetadata := datasourceCompletedQueryResultMetadata("run_databricks_query", "databricks", qm)
	if offset == 0 {
		artifactID := firstStringMapValue(qm, "artifact_id", "full_artifact_id")
		if artifactID == "" {
			artifactID = largeOutputArtifactID()
		}
		qm["artifact_id"] = artifactID
		qm["full_artifact_id"] = artifactID
		resultMetadata = datasourceCompletedQueryResultMetadata("run_databricks_query", "databricks", qm)
		resultMetadata = addRowsArtifactReferenceMetadata(out, resultMetadata, artifactID, len(fullRows), len(rows), "json_rows", len(fullRows) > len(rows) || truncatedToConfiguredLimit)
		payload, payloadErr := newLargeRowsArtifactPayload(artifactID, "Query Results", fullRows, "run_databricks_query", qm)
		if payloadErr != nil {
			return JSONError("Failed to encode query results for artifact persistence.")
		}
		artifactPayload = payload
	}

	return Output{OK: true, ResultFormat: ResultFormatJSON, Result: out, ResultMetadata: resultMetadata, QueryMetadata: qm, Artifact: artifactPayload}
}

func makeDatabricksPagedQuery(sql string, limit int, offset int64) string {
	clean := strings.TrimSpace(sql)
	if clean == "" {
		return clean
	}
	clean = strings.TrimSuffix(clean, ";")
	clean = strings.TrimSpace(clean)
	if limit <= 0 {
		return clean
	}
	if offset <= 0 {
		return fmt.Sprintf("SELECT * FROM (%s) AS _query LIMIT %d", clean, limit)
	}
	return fmt.Sprintf("SELECT * FROM (%s) AS _query LIMIT %d OFFSET %d", clean, limit, offset)
}

func newListDatabricksCatalogsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_databricks_catalogs",
				Description: "List one bounded page of Unity Catalog catalogs in Databricks. When next_page_token is returned, copy it unchanged as page_token to continue that same listing.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque next_page_token returned by the previous call.",
						},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Databricks == nil && exec.DatabricksHTTP == nil {
				return JSONError("Databricks is not configured."), nil
			}
			workspaceScope, scopeErr := databricksMetadataWorkspaceScope(exec)
			if scopeErr != nil {
				return JSONError(scopeErr.Error()), nil
			}
			var in struct {
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			providerPageToken, tokenErr := decodeDatabricksMetadataPageToken(in.PageToken, "catalogs", "", "", workspaceScope)
			if tokenErr != nil {
				return databricksMetadataPageTokenRestartOutput(tokenErr), nil
			}
			params := url.Values{}
			if providerPageToken != "" {
				params.Set("page_token", providerPageToken)
			}
			target := "/api/databricks/catalogs"
			if encoded := params.Encode(); encoded != "" {
				target += "?" + encoded
			}
			st, b, err := callDatabricks(exec.Databricks, exec.DatabricksHTTP, ctx, http.MethodGet, target, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				message := databricksErrorMessage(st, b)
				if providerPageToken != "" && databricksProviderRejectedMetadataPageToken(st, message) {
					return databricksMetadataPageTokenRestartOutput(newDatabricksMetadataPageTokenError("provider_rejected", "Databricks rejected an expired or invalid metadata page_token; restart this listing from its first page")), nil
				}
				return JSONError(message), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Databricks catalogs request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			catalogs, _ := m["catalogs"].([]any)
			if catalogs == nil {
				catalogs = []any{}
			}
			nextPageToken := encodeDatabricksMetadataPageToken("catalogs", "", "", workspaceScope, asString(m["next_page_token"]))
			hasMore := nextPageToken != ""
			result := map[string]any{"catalogs": catalogs, "has_more": hasMore}
			metadata := map[string]any{"count": len(catalogs), "has_more": hasMore}
			if nextPageToken != "" {
				result["next_page_token"] = nextPageToken
			}
			return datasourceMetadataOutput("list_databricks_catalogs", "databricks", "catalog_list", result, metadata), nil
		},
	}
}

func newListDatabricksSchemasTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_databricks_schemas",
				Description: "List one bounded page of schemas in a Databricks Unity Catalog catalog. When next_page_token is returned, copy it unchanged as page_token to continue that same catalog listing.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"catalog_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Databricks Unity Catalog catalog name.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque next_page_token returned by the previous call.",
						},
					},
					"required":             []string{"catalog_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Databricks == nil && exec.DatabricksHTTP == nil {
				return JSONError("Databricks is not configured."), nil
			}
			workspaceScope, scopeErr := databricksMetadataWorkspaceScope(exec)
			if scopeErr != nil {
				return JSONError(scopeErr.Error()), nil
			}
			var in struct {
				CatalogName string `json:"catalog_name"`
				PageToken   string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			catalog := strings.TrimSpace(in.CatalogName)
			if catalog == "" {
				return JSONError("catalog_name is required"), nil
			}

			providerPageToken, tokenErr := decodeDatabricksMetadataPageToken(in.PageToken, "schemas", catalog, "", workspaceScope)
			if tokenErr != nil {
				return databricksMetadataPageTokenRestartOutput(tokenErr), nil
			}
			params := url.Values{}
			params.Set("catalog_name", catalog)
			if providerPageToken != "" {
				params.Set("page_token", providerPageToken)
			}
			u := "/api/databricks/schemas?" + params.Encode()
			st, b, err := callDatabricks(exec.Databricks, exec.DatabricksHTTP, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				message := databricksErrorMessage(st, b)
				if providerPageToken != "" && databricksProviderRejectedMetadataPageToken(st, message) {
					return databricksMetadataPageTokenRestartOutput(newDatabricksMetadataPageTokenError("provider_rejected", "Databricks rejected an expired or invalid metadata page_token; restart this listing from its first page")), nil
				}
				return JSONError(message), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Databricks schemas request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			schemas, _ := m["schemas"].([]any)
			if schemas == nil {
				schemas = []any{}
			}
			nextPageToken := encodeDatabricksMetadataPageToken("schemas", catalog, "", workspaceScope, asString(m["next_page_token"]))
			hasMore := nextPageToken != ""
			result := map[string]any{"schemas": schemas, "has_more": hasMore}
			metadata := map[string]any{
				"catalog_name": catalog,
				"count":        len(schemas),
				"has_more":     hasMore,
			}
			if nextPageToken != "" {
				result["next_page_token"] = nextPageToken
			}
			return datasourceMetadataOutput("list_databricks_schemas", "databricks", "schema_list", result, metadata), nil
		},
	}
}

func newListDatabricksTablesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_databricks_tables",
				Description: "List one bounded page of tables in a Databricks catalog and schema. When next_page_token is returned, copy it unchanged as page_token to continue that same catalog/schema listing.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"catalog_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Databricks Unity Catalog catalog name.",
						},
						"schema_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Databricks schema name.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque next_page_token returned by the previous call.",
						},
					},
					"required":             []string{"catalog_name", "schema_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Databricks == nil && exec.DatabricksHTTP == nil {
				return JSONError("Databricks is not configured."), nil
			}
			workspaceScope, scopeErr := databricksMetadataWorkspaceScope(exec)
			if scopeErr != nil {
				return JSONError(scopeErr.Error()), nil
			}
			var in struct {
				CatalogName string `json:"catalog_name"`
				SchemaName  string `json:"schema_name"`
				PageToken   string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			catalog := strings.TrimSpace(in.CatalogName)
			schema := strings.TrimSpace(in.SchemaName)
			if catalog == "" || schema == "" {
				return JSONError("catalog_name and schema_name are required"), nil
			}

			providerPageToken, tokenErr := decodeDatabricksMetadataPageToken(in.PageToken, "tables", catalog, schema, workspaceScope)
			if tokenErr != nil {
				return databricksMetadataPageTokenRestartOutput(tokenErr), nil
			}
			params := url.Values{}
			params.Set("catalog_name", catalog)
			params.Set("schema_name", schema)
			if providerPageToken != "" {
				params.Set("page_token", providerPageToken)
			}
			u := "/api/databricks/tables?" + params.Encode()
			st, b, err := callDatabricks(exec.Databricks, exec.DatabricksHTTP, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				message := databricksErrorMessage(st, b)
				if providerPageToken != "" && databricksProviderRejectedMetadataPageToken(st, message) {
					return databricksMetadataPageTokenRestartOutput(newDatabricksMetadataPageTokenError("provider_rejected", "Databricks rejected an expired or invalid metadata page_token; restart this listing from its first page")), nil
				}
				return JSONError(message), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Databricks tables request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			tables, _ := m["tables"].([]any)
			if tables == nil {
				tables = []any{}
			}
			nextPageToken := encodeDatabricksMetadataPageToken("tables", catalog, schema, workspaceScope, asString(m["next_page_token"]))
			hasMore := nextPageToken != ""
			result := map[string]any{"tables": tables, "has_more": hasMore}
			metadata := map[string]any{
				"catalog_name": catalog,
				"schema_name":  schema,
				"count":        len(tables),
				"has_more":     hasMore,
			}
			if nextPageToken != "" {
				result["next_page_token"] = nextPageToken
			}
			return datasourceMetadataOutput("list_databricks_tables", "databricks", "table_list", result, metadata), nil
		},
	}
}

func newDescribeDatabricksTableTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "describe_databricks_table",
				Description: "Describe a Databricks table schema by full name (catalog.schema.table).",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"full_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Fully qualified table name as catalog.schema.table.",
						},
					},
					"required":             []string{"full_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Databricks == nil && exec.DatabricksHTTP == nil {
				return JSONError("Databricks is not configured."), nil
			}
			var in struct {
				FullName string `json:"full_name"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			fullName := strings.TrimSpace(in.FullName)
			if fullName == "" {
				return JSONError("full_name is required"), nil
			}

			u := "/api/databricks/table-schema?full_name=" + url.QueryEscape(fullName)
			st, b, err := callDatabricks(exec.Databricks, exec.DatabricksHTTP, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(databricksErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Databricks table schema request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			schema, _ := m["schema"].([]any)
			if schema == nil {
				if cols, ok := m["columns"].([]any); ok {
					schema = cols
				}
			}
			if schema == nil {
				schema = []any{}
			}
			return datasourceMetadataOutput("describe_databricks_table", "databricks", "table_schema", map[string]any{"table": fullName, "schema": schema}, map[string]any{
				"table": fullName,
				"count": len(schema),
			}), nil
		},
	}
}
