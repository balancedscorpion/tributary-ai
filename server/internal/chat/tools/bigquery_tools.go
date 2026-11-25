package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"

	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/queryadapters"
	"ai-analyst-local/internal/chat/types"
)

func defaultBQProjectID(h *bigquery.Handler) string {
	if h == nil || h.Settings == nil {
		return ""
	}
	s := h.Settings.Get()
	if s.GoogleProjectID != nil {
		pid := strings.TrimSpace(*s.GoogleProjectID)
		if pid != "" {
			return pid
		}
	}
	if s.DataSources == nil || s.DataSources.BigQuery == nil || s.DataSources.BigQuery.LastProjectID == nil {
		return ""
	}
	return strings.TrimSpace(*s.DataSources.BigQuery.LastProjectID)
}

// bigQueryScanBudgets resolves the two policy levels for a launch. approved
// == 0 means no configured ceiling: estimates above the automatic limit pause
// for a one-time user approval at any size. A positive approved value is a
// hard cap — estimates above it are rejected without asking.
func bigQueryScanBudgets(h *bigquery.Handler) (automatic int64, approved int64) {
	automatic = defaultBigQueryScanBytes
	if h != nil && h.Settings != nil {
		settings := h.Settings.Get()
		if settings.DatabaseQueryPolicy != nil && settings.DatabaseQueryPolicy.BigQueryMaximumBytesBilled > 0 {
			automatic = settings.DatabaseQueryPolicy.BigQueryMaximumBytesBilled
		}
		if settings.DataSources != nil && settings.DataSources.BigQuery != nil {
			state := settings.DataSources.BigQuery
			if state.AutomaticScanBytes != nil && *state.AutomaticScanBytes > 0 {
				automatic = *state.AutomaticScanBytes
			}
			if state.ApprovedScanBytes != nil && *state.ApprovedScanBytes > 0 {
				approved = *state.ApprovedScanBytes
			}
		}
	}
	if approved > 0 && approved < automatic {
		approved = automatic
	}
	return automatic, approved
}

func callLocalDatasourceHandler(h http.Handler, ctx context.Context, method string, targetPath string, body any) (int, []byte, error) {
	if h == nil {
		return 0, nil, errors.New("datasource handler is not configured")
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

const localDatasourceResponseLimit int64 = 8 << 20

func readLocalDatasourceResponse(reader io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(reader, localDatasourceResponseLimit+1))
	if err != nil {
		return nil, fmt.Errorf("failed to read datasource handler response: %w", err)
	}
	if int64(len(b)) > localDatasourceResponseLimit {
		return nil, fmt.Errorf("datasource handler response exceeded %d bytes", localDatasourceResponseLimit)
	}
	return b, nil
}

func callBigQuery(h *bigquery.Handler, fake http.Handler, ctx context.Context, method string, targetPath string, body any) (int, []byte, error) {
	if fake != nil {
		return callLocalDatasourceHandler(fake, ctx, method, targetPath, body)
	}
	if h == nil {
		return 0, nil, errors.New("bigquery handler is not configured")
	}

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		bodyReader = bytes.NewReader(b)
	}

	// Use an absolute URL so Query parsing works consistently.
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

func parseJSONBody(b []byte) (any, error) {
	if len(bytes.TrimSpace(b)) == 0 {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return v, nil
}

func bqErrorMessage(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("BigQuery API error (HTTP %d)", status)
	}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if strings.TrimSpace(parsed.Error.Message) != "" {
			return strings.TrimSpace(parsed.Error.Message)
		}
		if strings.TrimSpace(parsed.Message) != "" {
			return strings.TrimSpace(parsed.Message)
		}
	}
	return text
}

func bigqueryJobConsoleURL(projectID, location, jobID string) string {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	jobID = strings.TrimSpace(jobID)
	if projectID == "" || jobID == "" {
		return ""
	}
	jobRef := jobID
	if location != "" {
		jobRef = "bq:" + location + ":" + jobID
	}
	return fmt.Sprintf(
		"https://console.cloud.google.com/bigquery?project=%s&j=%s&page=queryresults",
		url.QueryEscape(projectID),
		url.QueryEscape(jobRef),
	)
}

func bigQueryCompletedSubmitMetadata(sql, projectID string, submit *bigquery.SubmitResult, description string) map[string]any {
	md := map[string]any{}
	if submit != nil {
		md = cloneStringAnyMap(submit.Metadata)
		if md == nil {
			md = map[string]any{}
		}
		if strings.TrimSpace(projectID) == "" {
			projectID = strings.TrimSpace(submit.ProjectID)
		}
	}
	projectID = strings.TrimSpace(projectID)
	sql = strings.TrimSpace(sql)
	if _, ok := md["provider"]; !ok {
		md["provider"] = "bigquery"
	}
	if sql != "" {
		md["sql"] = sql
		md["sql_preview"] = asyncSQLPreview(sql)
	}
	if projectID != "" {
		md["project_id"] = projectID
	}
	if submit != nil {
		if jobID := strings.TrimSpace(submit.JobID); jobID != "" {
			md["job_id"] = jobID
			md["warehouse_job_id"] = jobID
			md["provider_job_id"] = jobID
			if jobURL := bigqueryJobConsoleURL(projectID, submit.JobLocation, jobID); jobURL != "" {
				md["provider_job_url"] = jobURL
			}
		}
		if location := strings.TrimSpace(submit.JobLocation); location != "" {
			md["job_location"] = location
			md["warehouse_location"] = location
		}
		if len(submit.Schema) > 0 {
			md["schema"] = submit.Schema
		}
	}
	if description = strings.TrimSpace(description); description != "" {
		md["description"] = description
	}
	return md
}

func newRunBigQueryQueryTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "run_bigquery_query",
				Description: "Profile and start a durable read-only Google BigQuery query. It returns a managed job; use check_query_status and get_query_results rather than re-running SQL. To continue failed work, pass the exact query_obligation_id returned by the prior attempt.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"sql": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Standard SQL query to execute.",
						},
						"description": map[string]any{
							"type":        "string",
							"maxLength":   300,
							"description": "One-sentence plain-English summary of what this query answers. Keep it brief (max 300 characters).",
						},
						"project_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "GCP Project ID (optional)",
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
				ProjectID         string          `json:"project_id,omitempty"`
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
			if exec.BigQuery == nil && exec.BigQueryHTTP == nil {
				return JSONError("BigQuery is not configured."), nil
			}
			sql := normalizeQuerySQLInput(in.SQL)
			if sql == "" {
				return JSONError("Missing sql"), nil
			}
			description := normalizeQueryDescription(in.Description)
			pid := strings.TrimSpace(in.ProjectID)
			if pid == "" {
				pid = defaultBQProjectID(exec.BigQuery)
			}
			if pid == "" {
				return JSONError("No project ID configured"), nil
			}

			requestedLimit, pageSize, isCapped, effectiveConfiguredLimit := resolveQueryPageSize(in.Limit, exec.DatabaseQueryRowLimit)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset > 0 && exec.Jobs != nil {
				return JSONError("run_bigquery_query never re-executes SQL for pagination. Call get_query_results with the original job_id, owner_token, and page_token."), nil
			}

			queryFetchSize := pageSize
			if offset == 0 {
				queryFetchSize = effectiveConfiguredLimit
			}
			querySQL := makeBQPagedQuery(sql, queryFetchSize+1, offset)

			if offset == 0 && exec.Jobs != nil {
				if exec.BigQuery == nil {
					return JSONError("BigQuery managed queries require the native BigQuery handler."), nil
				}
				automaticScanBytes, approvedScanBytes := bigQueryScanBudgets(exec.BigQuery)
				return launchManagedQuery(ctx, exec, managedQueryLaunchRequest{
					ToolName:          "run_bigquery_query",
					Provider:          "bigquery",
					ConnectionID:      pid,
					SQL:               sql,
					SubmittedSQL:      querySQL,
					Description:       description,
					Adapter:           queryadapters.NewBigQuery(exec.BigQuery),
					Metadata:          map[string]any{"project_id": pid},
					Budgets:           jobsScanBudgets(effectiveConfiguredLimit, automaticScanBytes, approvedScanBytes),
					RequestedLimit:    requestedLimit,
					PageSize:          pageSize,
					ConfiguredLimit:   effectiveConfiguredLimit,
					LimitCapped:       isCapped,
					FullData:          in.FullData,
					ReplacesJobID:     replacesJobID,
					QueryObligationID: queryObligationID,
				})
			}

			var m map[string]any

			if offset == 0 {
				// Compatibility preflight for installations without the durable
				// managed-job registry.
				st, b, err := callBigQuery(exec.BigQuery, exec.BigQueryHTTP, ctx, http.MethodPost, "/api/bigquery/preflight", map[string]any{
					"project_id": pid,
					"sql":        sql,
				})
				_ = st
				if err != nil {
					return JSONError(err.Error()), nil
				}
				v, err := parseJSONBody(b)
				if err != nil {
					return JSONError("BigQuery preflight failed"), nil
				}
				m, _ = v.(map[string]any)
				ok, _ := m["ok"].(bool)
				if ok != true {
					msg := "BigQuery preflight failed"
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
							"instructions":     "Preflight failed; rewrite the SQL query and call run_bigquery_query again. Do not show this preflight error in the UI.",
						},
					}, nil
				}
			}

			// Existing synchronous compatibility path remains unchanged.
			var bqProgress bigquery.QueryProgressFn
			if exec.OnProgress != nil {
				bqProgress = func(phase string, meta map[string]any) {
					progress := make(map[string]any, len(meta)+1)
					for key, value := range meta {
						progress[key] = value
					}
					progress["phase"] = phase
					exec.OnProgress(progress)
				}
			}
			result, err := exec.BigQuery.RunQueryWithProgress(ctx, pid, querySQL, bqProgress)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			qm := map[string]any{
				"provider":              "bigquery",
				"sql":                   sql,
				"sql_preview":           asyncSQLPreview(sql),
				"duration_ms":           result.DurationMs,
				"total_bytes_processed": result.TotalBytesProcessed,
				"job_id":                result.JobID,
				"job_location":          result.JobLocation,
				"cache_hit":             result.CacheHit,
			}
			if description != "" {
				qm["description"] = description
			}
			if result.Schema != nil {
				qm["schema"] = result.Schema
			}
			qm["full_data_requested"] = in.FullData
			return formatBigQuerySuccessOutput(result.Rows, pageSize, offset, requestedLimit, isCapped, effectiveConfiguredLimit, in.FullData, qm), nil
		},
	}
}

func formatBigQuerySuccessOutput(rowsIn []map[string]any, pageSize int, offset int64, requestedLimit int, isCapped bool, effectiveConfiguredLimit int, fullData bool, metadata map[string]any) Output {
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
	qm["full_data_requested"] = fullData
	qm["status"] = string(jobs.JobStatusCompleted)
	qm["requested_limit"] = requestedLimit
	qm["effective_limit"] = pageSize
	qm["configured_limit"] = effectiveConfiguredLimit
	qm["limit_capped"] = isCapped
	qm["rows_returned"] = len(rows)
	qm["rows_preview_count"] = len(rows)
	qm["page_size"] = pageSize
	qm["offset"] = offset
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
	resultMetadata := datasourceCompletedQueryResultMetadata("run_bigquery_query", "bigquery", qm)
	if offset == 0 {
		artifactID := firstStringMapValue(qm, "artifact_id", "full_artifact_id")
		if artifactID == "" {
			artifactID = largeOutputArtifactID()
		}
		qm["artifact_id"] = artifactID
		qm["full_artifact_id"] = artifactID
		resultMetadata = datasourceCompletedQueryResultMetadata("run_bigquery_query", "bigquery", qm)
		resultMetadata = addRowsArtifactReferenceMetadata(out, resultMetadata, artifactID, len(fullRows), len(rows), "json_rows", len(fullRows) > len(rows) || truncatedToConfiguredLimit)
		payload, payloadErr := newLargeRowsArtifactPayload(artifactID, "Query Results", fullRows, "run_bigquery_query", qm)
		if payloadErr != nil {
			return JSONError("Failed to encode query results for artifact persistence.")
		}
		artifactPayload = payload
	}

	return Output{OK: true, ResultFormat: ResultFormatJSON, Result: out, ResultMetadata: resultMetadata, QueryMetadata: qm, Artifact: artifactPayload}
}

func makeBQPagedQuery(sql string, limit int, offset int64) string {
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

func newListBQDatasetsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_bq_datasets",
				Description: "List all BigQuery datasets in the project. Use this to find available datasets.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"project_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "GCP Project ID (optional)",
						},
					},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.BigQuery == nil && exec.BigQueryHTTP == nil {
				return JSONError("BigQuery is not configured."), nil
			}
			var in struct {
				ProjectID string `json:"project_id,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			pid := strings.TrimSpace(in.ProjectID)
			if pid == "" {
				pid = defaultBQProjectID(exec.BigQuery)
			}
			if pid == "" {
				return JSONError("No project ID configured"), nil
			}

			u := "/api/bigquery/datasets?project_id=" + url.QueryEscape(pid)
			st, b, err := callBigQuery(exec.BigQuery, exec.BigQueryHTTP, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(bqErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("BigQuery datasets request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			datasets, _ := m["datasets"].([]any)
			if datasets == nil {
				datasets = []any{}
			}
			return datasourceMetadataOutput("list_bq_datasets", "bigquery", "dataset_list", map[string]any{"datasets": datasets}, map[string]any{
				"project_id": pid,
				"count":      len(datasets),
			}), nil
		},
	}
}

func newListBQTablesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_bq_tables",
				Description: "List all tables in a specific BigQuery dataset.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"dataset_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "The Dataset ID",
						},
						"project_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "GCP Project ID (optional)",
						},
					},
					"required":             []string{"dataset_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.BigQuery == nil && exec.BigQueryHTTP == nil {
				return JSONError("BigQuery is not configured."), nil
			}
			var in struct {
				DatasetID string `json:"dataset_id"`
				ProjectID string `json:"project_id,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			did := strings.TrimSpace(in.DatasetID)
			if did == "" {
				return JSONError("dataset_id is required"), nil
			}
			pid := strings.TrimSpace(in.ProjectID)
			if pid == "" {
				pid = defaultBQProjectID(exec.BigQuery)
			}
			if pid == "" {
				return JSONError("No project ID configured"), nil
			}

			u := "/api/bigquery/tables?project_id=" + url.QueryEscape(pid) + "&dataset_id=" + url.QueryEscape(did)
			st, b, err := callBigQuery(exec.BigQuery, exec.BigQueryHTTP, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(bqErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("BigQuery tables request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			tables, _ := m["tables"].([]any)
			if tables == nil {
				tables = []any{}
			}
			return datasourceMetadataOutput("list_bq_tables", "bigquery", "table_list", map[string]any{"tables": tables}, map[string]any{
				"project_id": pid,
				"dataset_id": did,
				"count":      len(tables),
			}), nil
		},
	}
}

func newGetBQTableSchemaTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_bq_table_schema",
				Description: "Get the schema (columns and types) of a BigQuery table. Use this before writing queries to ensure column names are correct.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"dataset_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "BigQuery dataset ID.",
						},
						"table_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "BigQuery table ID.",
						},
						"project_id": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "GCP Project ID (optional).",
						},
					},
					"required":             []string{"dataset_id", "table_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.BigQuery == nil && exec.BigQueryHTTP == nil {
				return JSONError("BigQuery is not configured."), nil
			}
			var in struct {
				DatasetID string `json:"dataset_id"`
				TableID   string `json:"table_id"`
				ProjectID string `json:"project_id,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			did := strings.TrimSpace(in.DatasetID)
			tid := strings.TrimSpace(in.TableID)
			if did == "" || tid == "" {
				return JSONError("dataset_id and table_id are required"), nil
			}
			pid := strings.TrimSpace(in.ProjectID)
			if pid == "" {
				pid = defaultBQProjectID(exec.BigQuery)
			}
			if pid == "" {
				return JSONError("No project ID configured"), nil
			}

			u := "/api/bigquery/schema?project_id=" + url.QueryEscape(pid) + "&dataset_id=" + url.QueryEscape(did) + "&table_id=" + url.QueryEscape(tid)
			st, b, err := callBigQuery(exec.BigQuery, exec.BigQueryHTTP, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(bqErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("BigQuery schema request failed"), nil
			}
			m, _ := v.(map[string]any)
			if m == nil {
				m = map[string]any{}
			}
			schema, _ := m["schema"].([]any)
			if schema == nil {
				schema = []any{}
			}
			return datasourceMetadataOutput("get_bq_table_schema", "bigquery", "table_schema", map[string]any{"schema": schema}, map[string]any{
				"project_id": pid,
				"dataset_id": did,
				"table_id":   tid,
				"count":      len(schema),
			}), nil
		},
	}
}

func extractErrMessage(m map[string]any) string {
	if m == nil {
		return ""
	}
	// Handle BigQuery handler preflight shape: {"ok":false,"error":{"message":"..."}}.
	if errObj, ok := m["error"]; ok {
		switch v := errObj.(type) {
		case string:
			return strings.TrimSpace(v)
		case map[string]any:
			if msg, ok := v["message"].(string); ok {
				return strings.TrimSpace(msg)
			}
		}
	}
	if msg, ok := m["message"].(string); ok {
		return strings.TrimSpace(msg)
	}
	return ""
}
