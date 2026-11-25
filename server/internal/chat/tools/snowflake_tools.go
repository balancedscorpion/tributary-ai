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
	"sync"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/queryadapters"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/snowflake"
)

func callSnowflake(h http.Handler, ctx context.Context, method string, targetPath string, body any) (int, []byte, error) {
	if h == nil {
		return 0, nil, errors.New("snowflake handler is not configured")
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
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, b, nil
}

func resolveSnowflakeHandler(exec ExecContext) http.Handler {
	return exec.SnowflakeHTTP
}

func snowflakeErrorMessage(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("Snowflake API error (HTTP %d)", status)
	}
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		if msg := strings.TrimSpace(parsed.Error.Message); msg != "" {
			return msg
		}
		if msg := strings.TrimSpace(parsed.Message); msg != "" {
			return msg
		}
	}
	return text
}

type snowflakeQueryOutcome struct {
	Rows      []map[string]any
	Schema    []map[string]any
	Metadata  map[string]any
	Completed bool
	Failed    bool
	State     string
	Message   string
	QueryID   string
	NextURI   string
	QueryURL  string
}

func snowflakeFailureState(state string) bool {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		return false
	}
	return strings.Contains(state, "fail") || strings.Contains(state, "error") || strings.Contains(state, "abort")
}

func snowflakeTerminalState(state string) bool {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		return false
	}
	return strings.Contains(state, "finish") || strings.Contains(state, "done") || strings.Contains(state, "success") || strings.Contains(state, "complete") || strings.Contains(state, "cancel")
}

func parseSnowflakeQueryOutcome(v any) snowflakeQueryOutcome {
	out := snowflakeQueryOutcome{
		Metadata: map[string]any{},
	}
	m := mapStringAny(v)
	if m == nil {
		return out
	}

	if meta := mapStringAny(m["metadata"]); meta != nil {
		out.Metadata = cloneStringAnyMap(meta)
	} else if queryMeta := mapStringAny(m["query_metadata"]); queryMeta != nil {
		out.Metadata = cloneStringAnyMap(queryMeta)
	} else if queryMeta := mapStringAny(m["queryMetadata"]); queryMeta != nil {
		out.Metadata = cloneStringAnyMap(queryMeta)
	}
	if out.Metadata == nil {
		out.Metadata = map[string]any{}
	}

	out.Rows = stringMapSlice(m["rows"])
	if out.Rows == nil {
		if data := asSlice(m["data"]); len(data) > 0 {
			out.Rows = stringMapSlice(data)
		}
	}
	out.Schema = stringMapSlice(m["schema"])
	if out.Schema == nil {
		out.Schema = stringMapSlice(m["columns"])
	}
	if len(out.Schema) == 0 {
		out.Schema = stringMapSlice(out.Metadata["schema"])
	}

	out.QueryID = firstNonEmpty(
		firstStringMapValue(m, "query_id", "queryId", "statement_id", "statementId", "snowflake_query_handle", "job_id", "id"),
		firstStringMapValue(out.Metadata, "query_id", "queryId", "statement_id", "statementId", "snowflake_query_handle", "job_id", "provider_job_id", "id"),
	)
	out.NextURI = firstNonEmpty(
		firstStringMapValue(m, "next_uri", "nextUri", "next_page_uri", "nextPageUri"),
		firstStringMapValue(out.Metadata, "next_uri", "nextUri", "next_page_uri", "nextPageUri"),
	)
	out.QueryURL = firstNonEmpty(
		firstStringMapValue(m, "query_url", "queryUrl", "query_history_url", "queryHistoryURL", "url"),
		firstStringMapValue(out.Metadata, "query_url", "queryUrl", "query_history_url", "queryHistoryURL", "url"),
	)

	out.State = firstNonEmpty(
		firstStringMapValue(m, "state", "status"),
		firstStringMapValue(out.Metadata, "state", "status"),
	)

	if done, ok := boolFromAny(m["completed"]); ok {
		out.Completed = done
	}
	if !out.Completed {
		if done, ok := boolFromAny(m["done"]); ok {
			out.Completed = done
		}
	}
	if !out.Completed {
		if done, ok := boolFromAny(m["job_complete"]); ok {
			out.Completed = done
		}
	}
	if !out.Completed && snowflakeTerminalState(out.State) && !snowflakeFailureState(out.State) {
		out.Completed = true
	}
	if !out.Completed && !out.Failed {
		// The Snowflake handler can return completed rows + query_metadata without top-level status.
		if _, hasRows := m["rows"]; hasRows && strings.TrimSpace(out.NextURI) == "" {
			out.Completed = true
		}
	}

	out.Message = strings.TrimSpace(extractErrMessage(m))
	if out.Message == "" {
		out.Message = firstNonEmpty(firstStringMapValue(m, "error"), firstStringMapValue(m, "message"))
	}
	if out.Message == "" {
		out.Message = firstNonEmpty(firstStringMapValue(out.Metadata, "error"), firstStringMapValue(out.Metadata, "message"))
	}

	out.Failed = snowflakeFailureState(out.State)
	if !out.Failed {
		if okv, ok := boolFromAny(m["ok"]); ok && !okv {
			out.Failed = true
		}
	}
	if out.Failed && strings.TrimSpace(out.Message) == "" {
		out.Message = "Snowflake query failed"
	}

	if out.Completed && out.Metadata["terminal_status"] == nil {
		out.Metadata["terminal_status"] = "completed"
	}
	if out.Failed && out.Metadata["terminal_status"] == nil {
		out.Metadata["terminal_status"] = "failed"
	}
	if out.QueryID != "" {
		out.Metadata["query_id"] = out.QueryID
	}
	if out.NextURI != "" {
		out.Metadata["next_uri"] = out.NextURI
	}
	if out.QueryURL != "" {
		out.Metadata["query_url"] = out.QueryURL
	}
	if out.State != "" {
		out.Metadata["state"] = out.State
	}
	return out
}

func submitSnowflakeQuery(ctx context.Context, exec ExecContext, sql, database, schema string, initialWait time.Duration) (snowflakeQueryOutcome, error) {
	h := resolveSnowflakeHandler(exec)
	st, b, err := callSnowflake(h, ctx, http.MethodPost, "/api/snowflake/query", map[string]any{
		"sql":             sql,
		"database":        database,
		"schema":          schema,
		"initial_wait_ms": int(initialWait / time.Millisecond),
		"wait_timeout":    initialWait.String(),
	})
	if err != nil {
		return snowflakeQueryOutcome{}, err
	}
	if st < 200 || st >= 300 {
		return snowflakeQueryOutcome{}, errors.New(snowflakeErrorMessage(st, b))
	}
	v, err := parseJSONBody(b)
	if err != nil {
		return snowflakeQueryOutcome{}, errors.New("Snowflake query request failed")
	}
	parsed := parseSnowflakeQueryOutcome(v)
	if parsed.Failed {
		return snowflakeQueryOutcome{}, errors.New(firstNonEmpty(parsed.Message, "Snowflake query failed"))
	}
	return parsed, nil
}

type snowflakePollState struct {
	mu       sync.Mutex
	queryID  string
	nextURI  string
	queryURL string
	database string
	schema   string
}

func (s *snowflakePollState) snapshot() snowflakePollState {
	if s == nil {
		return snowflakePollState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return snowflakePollState{
		queryID:  strings.TrimSpace(s.queryID),
		nextURI:  strings.TrimSpace(s.nextURI),
		queryURL: strings.TrimSpace(s.queryURL),
		database: strings.TrimSpace(s.database),
		schema:   strings.TrimSpace(s.schema),
	}
}

func (s *snowflakePollState) update(queryID, nextURI, queryURL string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queryID = strings.TrimSpace(queryID)
	nextURI = strings.TrimSpace(nextURI)
	queryURL = strings.TrimSpace(queryURL)
	if queryID != "" {
		s.queryID = queryID
	}
	s.nextURI = nextURI
	if queryURL != "" {
		s.queryURL = queryURL
	}
}

func snowflakePollBody(snapshot *snowflakePollState) map[string]any {
	body := map[string]any{}
	if snapshot.queryID != "" {
		body["query_id"] = snapshot.queryID
	}
	if snapshot.nextURI != "" {
		body["next_uri"] = snapshot.nextURI
	}
	if snapshot.database != "" {
		body["database"] = snapshot.database
	}
	if snapshot.schema != "" {
		body["schema"] = snapshot.schema
	}
	return body
}

func snowflakePollOnce(ctx context.Context, exec ExecContext, state *snowflakePollState) (snowflakeQueryOutcome, bool, error) {
	h := resolveSnowflakeHandler(exec)
	snapshot := state.snapshot()
	st, b, err := callSnowflake(h, ctx, http.MethodPost, "/api/snowflake/query", snowflakePollBody(&snapshot))
	if err != nil {
		return snowflakeQueryOutcome{}, false, err
	}
	if st < 200 || st >= 300 {
		return snowflakeQueryOutcome{}, false, errors.New(snowflakeErrorMessage(st, b))
	}
	v, err := parseJSONBody(b)
	if err != nil {
		return snowflakeQueryOutcome{}, false, errors.New("Snowflake query poll failed")
	}
	parsed := parseSnowflakeQueryOutcome(v)
	state.update(parsed.QueryID, parsed.NextURI, firstNonEmpty(parsed.QueryURL, parsed.NextURI))
	if parsed.Failed {
		return parsed, true, errors.New(firstNonEmpty(parsed.Message, "Snowflake query failed"))
	}
	if parsed.Completed {
		return parsed, true, nil
	}
	if strings.TrimSpace(parsed.NextURI) == "" && !strings.EqualFold(strings.TrimSpace(parsed.State), "running") {
		parsed.Completed = true
		return parsed, true, nil
	}
	return parsed, false, nil
}

func cancelSnowflakeQuery(ctx context.Context, exec ExecContext, state *snowflakePollState) error {
	h := resolveSnowflakeHandler(exec)
	if h == nil {
		return errors.New("snowflake handler is not configured")
	}
	snapshot := state.snapshot()
	body := snowflakePollBody(&snapshot)
	st, b, err := callSnowflake(h, ctx, http.MethodDelete, "/api/snowflake/query", body)
	if err != nil {
		return err
	}
	if st >= 200 && st < 300 {
		return nil
	}

	st2, b2, err2 := callSnowflake(h, ctx, http.MethodPost, "/api/snowflake/cancel", body)
	if err2 == nil && st2 >= 200 && st2 < 300 {
		return nil
	}
	if err2 != nil {
		return err2
	}
	return errors.New(firstNonEmpty(snowflakeErrorMessage(st, b), snowflakeErrorMessage(st2, b2), "Snowflake query cancel failed"))
}

func makeSnowflakePagedQuery(sql string, limit int, offset int64) string {
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

func formatSnowflakeSuccessOutput(rowsIn []map[string]any, pageSize int, offset int64, requestedLimit int, isCapped bool, effectiveConfiguredLimit int, fullData bool, metadata map[string]any) Output {
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
	qm["provider"] = "snowflake"
	qm["full_data_requested"] = fullData
	qm["requested_limit"] = requestedLimit
	qm["effective_limit"] = pageSize
	qm["configured_limit"] = effectiveConfiguredLimit
	qm["limit_capped"] = isCapped
	qm["rows_returned"] = len(rows)
	qm["rows_preview_count"] = len(rows)
	qm["status"] = string(jobs.JobStatusCompleted)
	qm["rows_processed"] = offset + int64(len(rows))
	qm["row_count"] = offset + int64(len(rows))
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
	resultMetadata := datasourceCompletedQueryResultMetadata("run_snowflake_query", "snowflake", qm)
	if offset == 0 {
		artifactID := firstStringMapValue(qm, "artifact_id", "full_artifact_id")
		if artifactID == "" {
			artifactID = largeOutputArtifactID()
		}
		qm["artifact_id"] = artifactID
		qm["full_artifact_id"] = artifactID
		resultMetadata = datasourceCompletedQueryResultMetadata("run_snowflake_query", "snowflake", qm)
		resultMetadata = addRowsArtifactReferenceMetadata(out, resultMetadata, artifactID, len(fullRows), len(rows), "json_rows", len(fullRows) > len(rows) || truncatedToConfiguredLimit)
		payload, payloadErr := newLargeRowsArtifactPayload(artifactID, "Query Results", fullRows, "run_snowflake_query", qm)
		if payloadErr != nil {
			return JSONError("Failed to encode query results for artifact persistence.")
		}
		artifactPayload = payload
	}

	return Output{OK: true, ResultFormat: ResultFormatJSON, Result: out, ResultMetadata: resultMetadata, QueryMetadata: qm, Artifact: artifactPayload}
}

func newRunSnowflakeQueryTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "run_snowflake_query",
				Description: "Profile and start a durable read-only Snowflake query. It returns a managed job; use check_query_status and get_query_results rather than re-running SQL. To continue failed work, pass the exact query_obligation_id returned by the prior attempt.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"sql": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Snowflake SQL query to execute.",
						},
						"description": map[string]any{
							"type":        "string",
							"maxLength":   300,
							"description": "One-sentence plain-English summary of what this query answers. Keep it brief (max 300 characters).",
						},
						"database": map[string]any{
							"type":        "string",
							"description": "Snowflake database name (optional).",
						},
						"schema": map[string]any{
							"type":        "string",
							"description": "Snowflake schema name (optional).",
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
				Database          string          `json:"database,omitempty"`
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
			handler := resolveSnowflakeHandler(exec)
			if handler == nil {
				return JSONError("Snowflake is not configured."), nil
			}
			sql := normalizeQuerySQLInput(in.SQL)
			if sql == "" {
				return JSONError("Missing sql"), nil
			}
			description := normalizeQueryDescription(in.Description)
			database := strings.TrimSpace(in.Database)
			schema := strings.TrimSpace(in.Schema)
			requestedLimit, pageSize, isCapped, effectiveConfiguredLimit := resolveQueryPageSize(in.Limit, exec.DatabaseQueryRowLimit)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset > 0 && exec.Jobs != nil {
				return JSONError("run_snowflake_query never re-executes SQL for pagination. Call get_query_results with the original job_id, owner_token, and page_token."), nil
			}
			if offset == 0 && exec.Jobs != nil {
				native, ok := handler.(*snowflake.Handler)
				if ok && native != nil {
					querySQL := makeSnowflakePagedQuery(sql, effectiveConfiguredLimit+1, 0)
					return launchManagedQuery(ctx, exec, managedQueryLaunchRequest{
						ToolName:          "run_snowflake_query",
						Provider:          "snowflake",
						ConnectionID:      "",
						SQL:               sql,
						SubmittedSQL:      querySQL,
						Description:       description,
						Adapter:           queryadapters.NewSnowflake(native),
						Session:           map[string]string{"database": database, "schema": schema},
						Metadata:          map[string]any{"database": database, "schema": schema},
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
			}

			if offset == 0 {
				st, b, err := callSnowflake(handler, ctx, http.MethodPost, "/api/snowflake/preflight", map[string]any{
					"sql":      sql,
					"database": database,
					"schema":   schema,
				})
				_ = st
				if err != nil {
					return JSONError(err.Error()), nil
				}
				v, err := parseJSONBody(b)
				if err != nil {
					return JSONError("Snowflake preflight failed"), nil
				}
				m := mapStringAny(v)
				ok, _ := m["ok"].(bool)
				if ok != true {
					msg := "Snowflake preflight failed"
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
							"instructions":     "Preflight failed; rewrite the SQL query and call run_snowflake_query again. Do not show this preflight error in the UI.",
						},
					}, nil
				}
			}

			querySQL := makeSnowflakePagedQuery(sql, pageSize+1, offset)

			if offset == 0 && exec.Jobs != nil {
				submittedAt := time.Now()
				parsed, err := submitSnowflakeQuery(ctx, exec, querySQL, database, schema, 300*time.Millisecond)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				if parsed.Completed {
					sqlPreview := asyncSQLPreview(sql)
					owner := jobs.JobOwner{
						ThreadID: exec.ThreadID,
						RunID:    exec.RunID,
					}
					completedRows := rowsFromMapSlice(parsed.Rows)
					completedSchema := rowsFromMapSlice(parsed.Schema)
					completedMeta := cloneStringAnyMap(parsed.Metadata)
					if completedMeta == nil {
						completedMeta = map[string]any{}
					}
					completedMeta["provider"] = "snowflake"
					completedMeta["sql"] = sql
					completedMeta["sql_preview"] = sqlPreview
					if description != "" {
						completedMeta["description"] = description
					}
					completedMeta["full_data_requested"] = in.FullData
					completedMeta["requested_limit"] = requestedLimit
					completedMeta["effective_limit"] = pageSize
					completedMeta["configured_limit"] = effectiveConfiguredLimit
					completedMeta["limit_capped"] = isCapped
					completedMeta["database"] = database
					completedMeta["schema"] = schema

					providerJobID := firstNonEmpty(parsed.QueryID)
					if providerJobID != "" {
						completedMeta["query_id"] = providerJobID
						completedMeta["provider_job_id"] = providerJobID
					}
					providerJobURL := firstNonEmpty(parsed.QueryURL, parsed.NextURI)
					if providerJobURL != "" {
						completedMeta["provider_job_url"] = providerJobURL
						completedMeta["url"] = providerJobURL
					}

					job, err := launchPreSubmittedCompatibility(ctx, exec.Jobs,
						owner,
						"snowflake",
						sql,
						sqlPreview,
						cloneStringAnyMap(completedMeta),
						func(context.Context) (bool, *jobs.QueryResult, error) {
							return true, &jobs.QueryResult{
								Rows:     rowsFromMapSlice(completedRows),
								Schema:   rowsFromMapSlice(completedSchema),
								Metadata: cloneStringAnyMap(completedMeta),
							}, nil
						},
						nil,
					)
					if err != nil {
						if errors.Is(err, jobs.ErrConcurrencyLimit) {
							return JSONError("Too many concurrent async queries. Wait for a running query to finish and retry."), nil
						}
						return JSONError(err.Error()), nil
					}

					ownerToken := strings.TrimSpace(job.Owner.Handle)
					artifactID := deterministicAsyncArtifactID(exec.ThreadID, job.ID)
					if providerJobID == "" {
						providerJobID = job.ID
					}
					result := map[string]any{
						"status":      "running",
						"job_id":      job.ID,
						"provider":    "snowflake",
						"sql":         sql,
						"sql_preview": sqlPreview,
						"owner_token": ownerToken,
						"thread_id":   exec.ThreadID,
						"artifact_id": artifactID,
						"message":     "Query submitted to Snowflake. Use check_query_status to monitor progress.",
					}
					if description != "" {
						result["description"] = description
					}
					qm := map[string]any{
						"provider":            "snowflake",
						"job_id":              job.ID,
						"sql":                 sql,
						"sql_preview":         sqlPreview,
						"query_id":            parsed.QueryID,
						"next_uri":            parsed.NextURI,
						"provider_job_id":     providerJobID,
						"provider_job_url":    providerJobURL,
						"url":                 providerJobURL,
						"owner_token":         ownerToken,
						"thread_id":           exec.ThreadID,
						"artifact_id":         artifactID,
						"status":              "running",
						"full_data_requested": in.FullData,
						"requested_limit":     requestedLimit,
						"effective_limit":     pageSize,
						"configured_limit":    effectiveConfiguredLimit,
						"limit_capped":        isCapped,
						"adaptive_wait_ms":    0,
						"submitted_at_ms":     submittedAt.UnixMilli(),
						"database":            database,
						"schema":              schema,
					}
					if description != "" {
						qm["description"] = description
					}
					return Output{OK: true, ResultFormat: ResultFormatJSON, Result: result, ResultMetadata: datasourceAsyncQueryResultMetadata("run_snowflake_query", "snowflake", qm), QueryMetadata: qm}, nil
				}

				state := &snowflakePollState{
					queryID:  strings.TrimSpace(parsed.QueryID),
					nextURI:  strings.TrimSpace(parsed.NextURI),
					queryURL: firstNonEmpty(parsed.QueryURL, parsed.NextURI),
					database: database,
					schema:   schema,
				}
				if state.queryID == "" && state.nextURI == "" {
					return JSONError("Snowflake async submit failed: missing query reference"), nil
				}

				providerJobID := firstNonEmpty(state.queryID, state.nextURI)
				providerJobURL := firstNonEmpty(state.queryURL, state.nextURI)
				if exec.OnProgress != nil {
					exec.OnProgress(map[string]any{
						"provider": "snowflake",
						"phase":    "submitted",
						"state":    "RUNNING",
						"query_id": state.queryID,
						"next_uri": state.nextURI,
						"url":      providerJobURL,
						"database": database,
						"schema":   schema,
					})
				}

				sqlPreview := asyncSQLPreview(sql)
				owner := jobs.JobOwner{
					ThreadID: exec.ThreadID,
					RunID:    exec.RunID,
				}
				collectedRows := rowsFromMapSlice(parsed.Rows)
				collectedSchema := rowsFromMapSlice(parsed.Schema)
				var collectedMu sync.Mutex
				jobMeta := map[string]any{
					"provider":            "snowflake",
					"sql":                 sql,
					"sql_preview":         sqlPreview,
					"query_id":            state.queryID,
					"next_uri":            state.nextURI,
					"provider_job_id":     providerJobID,
					"provider_job_url":    providerJobURL,
					"url":                 providerJobURL,
					"database":            database,
					"schema":              schema,
					"submitted_at_ms":     submittedAt.UnixMilli(),
					"full_data_requested": in.FullData,
					"requested_limit":     requestedLimit,
					"effective_limit":     pageSize,
					"configured_limit":    effectiveConfiguredLimit,
					"limit_capped":        isCapped,
				}
				if description != "" {
					jobMeta["description"] = description
				}
				job, err := launchPreSubmittedCompatibility(ctx, exec.Jobs,
					owner,
					"snowflake",
					sql,
					sqlPreview,
					jobMeta,
					func(pollCtx context.Context) (bool, *jobs.QueryResult, error) {
						polled, done, pollErr := snowflakePollOnce(pollCtx, exec, state)
						if pollErr != nil {
							return false, nil, pollErr
						}
						collectedMu.Lock()
						if len(polled.Rows) > 0 {
							collectedRows = append(collectedRows, rowsFromMapSlice(polled.Rows)...)
						}
						if len(collectedSchema) == 0 && len(polled.Schema) > 0 {
							collectedSchema = rowsFromMapSlice(polled.Schema)
						}
						finalRows := rowsFromMapSlice(collectedRows)
						finalSchema := rowsFromMapSlice(collectedSchema)
						collectedMu.Unlock()
						if !done {
							return false, nil, nil
						}
						meta := cloneStringAnyMap(polled.Metadata)
						if meta == nil {
							meta = map[string]any{}
						}
						meta["provider"] = "snowflake"
						stateSnap := state.snapshot()
						if stateSnap.queryID != "" {
							meta["query_id"] = stateSnap.queryID
							meta["provider_job_id"] = stateSnap.queryID
						}
						if stateSnap.queryURL != "" {
							meta["provider_job_url"] = stateSnap.queryURL
							meta["url"] = stateSnap.queryURL
						} else if stateSnap.nextURI != "" {
							meta["provider_job_url"] = stateSnap.nextURI
							meta["url"] = stateSnap.nextURI
						}
						if stateSnap.nextURI != "" {
							meta["next_uri"] = stateSnap.nextURI
						} else {
							meta["next_uri"] = ""
						}
						return true, &jobs.QueryResult{
							Rows:     finalRows,
							Schema:   finalSchema,
							Metadata: meta,
						}, nil
					},
					func(cancelCtx context.Context) error {
						return cancelSnowflakeQuery(cancelCtx, exec, state)
					},
				)
				if err != nil {
					if errors.Is(err, jobs.ErrConcurrencyLimit) {
						return JSONError("Too many concurrent async queries. Wait for a running query to finish and retry."), nil
					}
					return JSONError(err.Error()), nil
				}

				ownerToken := strings.TrimSpace(job.Owner.Handle)
				artifactID := deterministicAsyncArtifactID(exec.ThreadID, job.ID)
				stateSnap := state.snapshot()
				result := map[string]any{
					"status":      "running",
					"job_id":      job.ID,
					"provider":    "snowflake",
					"sql":         sql,
					"sql_preview": sqlPreview,
					"owner_token": ownerToken,
					"thread_id":   exec.ThreadID,
					"artifact_id": artifactID,
					"message":     "Query submitted to Snowflake. Use check_query_status to monitor progress.",
				}
				if description != "" {
					result["description"] = description
				}
				qm := map[string]any{
					"provider":            "snowflake",
					"job_id":              job.ID,
					"sql":                 sql,
					"sql_preview":         sqlPreview,
					"query_id":            stateSnap.queryID,
					"next_uri":            stateSnap.nextURI,
					"provider_job_id":     providerJobID,
					"provider_job_url":    providerJobURL,
					"url":                 providerJobURL,
					"owner_token":         ownerToken,
					"thread_id":           exec.ThreadID,
					"artifact_id":         artifactID,
					"status":              "running",
					"full_data_requested": in.FullData,
					"requested_limit":     requestedLimit,
					"effective_limit":     pageSize,
					"configured_limit":    effectiveConfiguredLimit,
					"limit_capped":        isCapped,
					"adaptive_wait_ms":    1100,
					"submitted_at_ms":     submittedAt.UnixMilli(),
					"database":            database,
					"schema":              schema,
				}
				if description != "" {
					qm["description"] = description
				}
				return Output{OK: true, ResultFormat: ResultFormatJSON, Result: result, ResultMetadata: datasourceAsyncQueryResultMetadata("run_snowflake_query", "snowflake", qm), QueryMetadata: qm}, nil
			}

			parsed, err := submitSnowflakeQuery(ctx, exec, querySQL, database, schema, 300*time.Millisecond)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			state := &snowflakePollState{
				queryID:  strings.TrimSpace(parsed.QueryID),
				nextURI:  strings.TrimSpace(parsed.NextURI),
				queryURL: firstNonEmpty(parsed.QueryURL, parsed.NextURI),
				database: database,
				schema:   schema,
			}

			const maxSyncPollSteps = 32
			steps := 0
			collectedRows := rowsFromMapSlice(parsed.Rows)
			collectedSchema := rowsFromMapSlice(parsed.Schema)
			for !parsed.Completed && !parsed.Failed && strings.TrimSpace(state.snapshot().nextURI) != "" && steps < maxSyncPollSteps {
				steps++
				polled, done, pollErr := snowflakePollOnce(ctx, exec, state)
				if pollErr != nil {
					return JSONError(pollErr.Error()), nil
				}
				parsed = polled
				collectedRows = append(collectedRows, rowsFromMapSlice(polled.Rows)...)
				if len(collectedSchema) == 0 && len(polled.Schema) > 0 {
					collectedSchema = rowsFromMapSlice(polled.Schema)
				}
				if done {
					break
				}
			}
			if parsed.Failed {
				return JSONError(firstNonEmpty(parsed.Message, "Snowflake query failed")), nil
			}
			if !parsed.Completed && strings.TrimSpace(parsed.NextURI) != "" {
				return JSONError("Snowflake query did not complete in the current call. Retry or use async query status tools."), nil
			}

			rows := collectedRows
			meta := cloneStringAnyMap(parsed.Metadata)
			if meta == nil {
				meta = map[string]any{}
			}
			meta["provider"] = "snowflake"
			meta["sql"] = sql
			meta["sql_preview"] = asyncSQLPreview(sql)
			if description != "" {
				meta["description"] = description
			}
			meta["query_id"] = firstNonEmpty(parsed.QueryID, state.snapshot().queryID)
			meta["next_uri"] = firstNonEmpty(parsed.NextURI, state.snapshot().nextURI)
			meta["provider_job_id"] = firstNonEmpty(parsed.QueryID, state.snapshot().queryID)
			meta["provider_job_url"] = firstNonEmpty(parsed.QueryURL, state.snapshot().queryURL, parsed.NextURI, state.snapshot().nextURI)
			meta["url"] = firstNonEmpty(parsed.QueryURL, state.snapshot().queryURL, parsed.NextURI, state.snapshot().nextURI)
			if len(collectedSchema) > 0 {
				meta["schema"] = collectedSchema
			}
			meta["full_data_requested"] = in.FullData
			return formatSnowflakeSuccessOutput(rows, pageSize, offset, requestedLimit, isCapped, effectiveConfiguredLimit, in.FullData, meta), nil
		},
	}
}

func newListSnowflakeDatabasesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_snowflake_databases",
				Description: "List databases available in Snowflake.",
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveSnowflakeHandler(exec)
			if handler == nil {
				return JSONError("Snowflake is not configured."), nil
			}
			var in struct{}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			st, b, err := callSnowflake(handler, ctx, http.MethodGet, "/api/snowflake/databases", nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(snowflakeErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Snowflake databases request failed"), nil
			}
			m := mapStringAny(v)
			databases := asSlice(m["databases"])
			if databases == nil {
				databases = []any{}
			}
			return datasourceMetadataOutput("list_snowflake_databases", "snowflake", "database_list", map[string]any{"databases": databases}, map[string]any{
				"count": len(databases),
			}), nil
		},
	}
}

func newListSnowflakeSchemasTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_snowflake_schemas",
				Description: "List schemas for a Snowflake database.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"database_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Snowflake database name.",
						},
					},
					"required":             []string{"database_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveSnowflakeHandler(exec)
			if handler == nil {
				return JSONError("Snowflake is not configured."), nil
			}
			var in struct {
				DatabaseName string `json:"database_name"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			database := strings.TrimSpace(in.DatabaseName)
			if database == "" {
				return JSONError("database_name is required"), nil
			}
			u := "/api/snowflake/schemas?database_name=" + url.QueryEscape(database)
			st, b, err := callSnowflake(handler, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(snowflakeErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Snowflake schemas request failed"), nil
			}
			m := mapStringAny(v)
			schemas := asSlice(m["schemas"])
			if schemas == nil {
				schemas = []any{}
			}
			return datasourceMetadataOutput("list_snowflake_schemas", "snowflake", "schema_list", map[string]any{"schemas": schemas}, map[string]any{
				"database_name": database,
				"count":         len(schemas),
			}), nil
		},
	}
}

func newListSnowflakeTablesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_snowflake_tables",
				Description: "List tables for a Snowflake database and schema.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"database_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Snowflake database name.",
						},
						"schema_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Snowflake schema name.",
						},
					},
					"required":             []string{"database_name", "schema_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveSnowflakeHandler(exec)
			if handler == nil {
				return JSONError("Snowflake is not configured."), nil
			}
			var in struct {
				DatabaseName string `json:"database_name"`
				SchemaName   string `json:"schema_name"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			database := strings.TrimSpace(in.DatabaseName)
			schema := strings.TrimSpace(in.SchemaName)
			if database == "" || schema == "" {
				return JSONError("database_name and schema_name are required"), nil
			}
			u := "/api/snowflake/tables?database_name=" + url.QueryEscape(database) + "&schema_name=" + url.QueryEscape(schema)
			st, b, err := callSnowflake(handler, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(snowflakeErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Snowflake tables request failed"), nil
			}
			m := mapStringAny(v)
			tables := asSlice(m["tables"])
			if tables == nil {
				tables = []any{}
			}
			return datasourceMetadataOutput("list_snowflake_tables", "snowflake", "table_list", map[string]any{"tables": tables}, map[string]any{
				"database_name": database,
				"schema_name":   schema,
				"count":         len(tables),
			}), nil
		},
	}
}

func newDescribeSnowflakeTableTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "describe_snowflake_table",
				Description: "Describe a Snowflake table by full name (database.schema.table).",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"full_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Fully qualified table name as database.schema.table.",
						},
					},
					"required":             []string{"full_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveSnowflakeHandler(exec)
			if handler == nil {
				return JSONError("Snowflake is not configured."), nil
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
			u := "/api/snowflake/table-schema?full_name=" + url.QueryEscape(fullName)
			st, b, err := callSnowflake(handler, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(snowflakeErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Snowflake table schema request failed"), nil
			}
			m := mapStringAny(v)
			schema := asSlice(m["schema"])
			if schema == nil {
				schema = asSlice(m["columns"])
			}
			if schema == nil {
				schema = []any{}
			}
			return datasourceMetadataOutput("describe_snowflake_table", "snowflake", "table_schema", map[string]any{"schema": schema}, map[string]any{
				"table": fullName,
				"count": len(schema),
			}), nil
		},
	}
}
