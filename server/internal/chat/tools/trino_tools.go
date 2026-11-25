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
	"ai-analyst-local/internal/trino"
)

func callTrino(h http.Handler, ctx context.Context, method string, targetPath string, body any) (int, []byte, error) {
	if h == nil {
		return 0, nil, errors.New("trino handler is not configured")
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

func resolveTrinoHandler(exec ExecContext) http.Handler {
	if exec.TrinoHTTP != nil {
		return exec.TrinoHTTP
	}
	if exec.Trino != nil {
		return exec.Trino
	}
	return nil
}

func trinoOutcomeFromAsyncResult(result *trino.AsyncQueryResult) trinoQueryOutcome {
	if result == nil {
		return trinoQueryOutcome{Metadata: map[string]any{}}
	}
	metadata := cloneStringAnyMap(result.Metadata)
	if metadata == nil {
		metadata = map[string]any{}
	}
	out := trinoQueryOutcome{
		Rows:      rowsFromMapSlice(result.Rows),
		Schema:    rowsFromMapSlice(result.Schema),
		Metadata:  metadata,
		Completed: result.Completed,
		QueryID:   strings.TrimSpace(result.QueryID),
		NextURI:   strings.TrimSpace(result.NextURI),
		QueryURL:  strings.TrimSpace(result.QueryURL),
	}
	if out.QueryURL == "" {
		out.QueryURL = firstStringMapValue(metadata, "provider_job_url", "query_url", "url")
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
	out.State = firstStringMapValue(out.Metadata, "state", "status")
	if out.State == "" {
		if out.Completed {
			out.State = "FINISHED"
		} else {
			out.State = "RUNNING"
		}
	}
	if out.Completed && out.Metadata["terminal_status"] == nil {
		out.Metadata["terminal_status"] = "completed"
	}
	return out
}

func submitTrinoQuery(ctx context.Context, exec ExecContext, sql, catalog, schema string, initialWait time.Duration) (trinoQueryOutcome, error) {
	if exec.Trino != nil {
		result, err := exec.Trino.SubmitQuery(ctx, sql, catalog, schema, "", initialWait)
		if err != nil {
			return trinoQueryOutcome{}, err
		}
		return trinoOutcomeFromAsyncResult(result), nil
	}
	handler := resolveTrinoHandler(exec)
	st, b, err := callTrino(handler, ctx, http.MethodPost, "/api/trino/query", map[string]any{
		"sql":             sql,
		"catalog":         catalog,
		"schema":          schema,
		"initial_wait_ms": int(initialWait / time.Millisecond),
		"wait_timeout":    initialWait.String(),
	})
	if err != nil {
		return trinoQueryOutcome{}, err
	}
	if st < 200 || st >= 300 {
		return trinoQueryOutcome{}, errors.New(trinoErrorMessage(st, b))
	}
	v, err := parseJSONBody(b)
	if err != nil {
		return trinoQueryOutcome{}, errors.New("Trino query request failed")
	}
	parsed := parseTrinoQueryOutcome(v)
	if parsed.Failed {
		return trinoQueryOutcome{}, errors.New(firstNonEmpty(parsed.Message, "Trino query failed"))
	}
	return parsed, nil
}

func trinoErrorMessage(status int, body []byte) string {
	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Sprintf("Trino API error (HTTP %d)", status)
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

type trinoQueryOutcome struct {
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

type trinoPollState struct {
	mu       sync.Mutex
	queryID  string
	nextURI  string
	queryURL string
	catalog  string
	schema   string
}

func (s *trinoPollState) snapshot() trinoPollState {
	if s == nil {
		return trinoPollState{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return trinoPollState{
		queryID:  strings.TrimSpace(s.queryID),
		nextURI:  strings.TrimSpace(s.nextURI),
		queryURL: strings.TrimSpace(s.queryURL),
		catalog:  strings.TrimSpace(s.catalog),
		schema:   strings.TrimSpace(s.schema),
	}
}

func (s *trinoPollState) update(queryID, nextURI, queryURL string) {
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

func mapStringAny(raw any) map[string]any {
	m, _ := raw.(map[string]any)
	return m
}

func asSlice(raw any) []any {
	v, _ := raw.([]any)
	return v
}

func firstStringMapValue(m map[string]any, keys ...string) string {
	if m == nil {
		return ""
	}
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		raw, ok := m[key]
		if !ok {
			continue
		}
		switch typed := raw.(type) {
		case string:
			if v := strings.TrimSpace(typed); v != "" {
				return v
			}
		case fmt.Stringer:
			if v := strings.TrimSpace(typed.String()); v != "" {
				return v
			}
		default:
			if v := strings.TrimSpace(fmt.Sprint(typed)); v != "" && v != "<nil>" {
				return v
			}
		}
	}
	return ""
}

func stringMapSlice(raw any) []map[string]any {
	items := asSlice(raw)
	if len(items) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		m := mapStringAny(item)
		if m == nil {
			continue
		}
		out = append(out, cloneStringAnyMap(m))
	}
	return out
}

func rowsFromMapSlice(raw []map[string]any) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, row := range raw {
		out = append(out, cloneStringAnyMap(row))
	}
	return out
}

func boolFromAny(raw any) (bool, bool) {
	switch typed := raw.(type) {
	case bool:
		return typed, true
	case string:
		v := strings.TrimSpace(strings.ToLower(typed))
		if v == "true" || v == "1" || v == "yes" {
			return true, true
		}
		if v == "false" || v == "0" || v == "no" {
			return false, true
		}
	}
	return false, false
}

func trinoFailureState(state string) bool {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		return false
	}
	return strings.Contains(state, "fail") || strings.Contains(state, "error")
}

func trinoTerminalState(state string) bool {
	state = strings.TrimSpace(strings.ToLower(state))
	if state == "" {
		return false
	}
	return strings.Contains(state, "finish") || strings.Contains(state, "done") || strings.Contains(state, "success") || strings.Contains(state, "complete")
}

func parseTrinoQueryOutcome(v any) trinoQueryOutcome {
	out := trinoQueryOutcome{
		Metadata: map[string]any{},
	}
	m := mapStringAny(v)
	if m == nil {
		return out
	}

	if meta := mapStringAny(m["metadata"]); meta != nil {
		out.Metadata = cloneStringAnyMap(meta)
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

	out.QueryID = firstStringMapValue(m, "query_id", "queryId", "id")
	if out.QueryID == "" {
		out.QueryID = firstStringMapValue(out.Metadata, "query_id", "queryId", "id")
	}
	out.NextURI = firstStringMapValue(m, "next_uri", "nextUri")
	if out.NextURI == "" {
		out.NextURI = firstStringMapValue(out.Metadata, "next_uri", "nextUri")
	}
	out.QueryURL = firstStringMapValue(m, "query_url", "queryUrl", "url")
	if out.QueryURL == "" {
		out.QueryURL = firstStringMapValue(out.Metadata, "query_url", "queryUrl", "url")
	}

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
	if !out.Completed && trinoTerminalState(out.State) {
		out.Completed = true
	}

	out.Message = strings.TrimSpace(extractErrMessage(m))
	if out.Message == "" {
		out.Message = firstNonEmpty(firstStringMapValue(m, "error"), firstStringMapValue(m, "message"))
	}
	if out.Message == "" {
		out.Message = firstNonEmpty(firstStringMapValue(out.Metadata, "error"), firstStringMapValue(out.Metadata, "message"))
	}

	out.Failed = trinoFailureState(out.State)
	if !out.Failed && strings.TrimSpace(out.Message) != "" && strings.EqualFold(strings.TrimSpace(out.State), "failed") {
		out.Failed = true
	}
	if !out.Failed && strings.EqualFold(strings.TrimSpace(firstStringMapValue(m, "ok")), "false") {
		out.Failed = true
	}
	if !out.Failed {
		if okv, ok := boolFromAny(m["ok"]); ok && !okv {
			out.Failed = true
		}
	}

	if out.Failed && strings.TrimSpace(out.Message) == "" {
		out.Message = "Trino query failed"
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

func makeTrinoPagedQuery(sql string, limit int, offset int64) string {
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

func trinoPollBody(snapshot *trinoPollState) map[string]any {
	body := map[string]any{}
	if snapshot.queryID != "" {
		body["query_id"] = snapshot.queryID
	}
	if snapshot.nextURI != "" {
		body["next_uri"] = snapshot.nextURI
	}
	if snapshot.catalog != "" {
		body["catalog"] = snapshot.catalog
	}
	if snapshot.schema != "" {
		body["schema"] = snapshot.schema
	}
	return body
}

func trinoPollOnce(ctx context.Context, exec ExecContext, state *trinoPollState) (trinoQueryOutcome, bool, error) {
	if exec.Trino != nil {
		snapshot := state.snapshot()
		result, err := exec.Trino.PollQuery(ctx, snapshot.queryID, snapshot.nextURI, snapshot.catalog, snapshot.schema, "")
		if err != nil {
			return trinoQueryOutcome{}, false, err
		}
		parsed := trinoOutcomeFromAsyncResult(result)
		state.update(parsed.QueryID, parsed.NextURI, firstNonEmpty(parsed.QueryURL, parsed.NextURI))
		if parsed.Completed {
			return parsed, true, nil
		}
		if strings.TrimSpace(parsed.NextURI) == "" {
			parsed.Completed = true
			return parsed, true, nil
		}
		return parsed, false, nil
	}
	h := resolveTrinoHandler(exec)
	snapshot := state.snapshot()
	st, b, err := callTrino(h, ctx, http.MethodPost, "/api/trino/query", trinoPollBody(&snapshot))
	if err != nil {
		return trinoQueryOutcome{}, false, err
	}
	if st < 200 || st >= 300 {
		return trinoQueryOutcome{}, false, errors.New(trinoErrorMessage(st, b))
	}
	v, err := parseJSONBody(b)
	if err != nil {
		return trinoQueryOutcome{}, false, errors.New("Trino query poll failed")
	}
	parsed := parseTrinoQueryOutcome(v)
	state.update(parsed.QueryID, parsed.NextURI, firstNonEmpty(parsed.QueryURL, parsed.NextURI))
	if parsed.Failed {
		return parsed, true, errors.New(firstNonEmpty(parsed.Message, "Trino query failed"))
	}
	if parsed.Completed {
		return parsed, true, nil
	}
	// Empty-result terminal responses may have no explicit completed status.
	if strings.TrimSpace(parsed.NextURI) == "" && !strings.EqualFold(strings.TrimSpace(parsed.State), "running") {
		parsed.Completed = true
		return parsed, true, nil
	}
	return parsed, false, nil
}

func cancelTrinoQuery(ctx context.Context, exec ExecContext, state *trinoPollState) error {
	if exec.Trino != nil {
		snapshot := state.snapshot()
		return exec.Trino.CancelQuery(ctx, snapshot.queryID, snapshot.nextURI, snapshot.catalog, snapshot.schema, "")
	}
	h := resolveTrinoHandler(exec)
	if h == nil {
		return errors.New("trino handler is not configured")
	}
	snapshot := state.snapshot()
	body := trinoPollBody(&snapshot)
	st, b, err := callTrino(h, ctx, http.MethodDelete, "/api/trino/query", body)
	if err != nil {
		return err
	}
	if st >= 200 && st < 300 {
		return nil
	}

	// Fallback path for handlers that expose explicit cancel endpoint.
	st2, b2, err2 := callTrino(h, ctx, http.MethodPost, "/api/trino/cancel", body)
	if err2 == nil && st2 >= 200 && st2 < 300 {
		return nil
	}
	if err2 != nil {
		return err2
	}
	return errors.New(firstNonEmpty(trinoErrorMessage(st, b), trinoErrorMessage(st2, b2), "Trino query cancel failed"))
}

func formatTrinoSuccessOutput(rowsIn []map[string]any, pageSize int, offset int64, requestedLimit int, isCapped bool, effectiveConfiguredLimit int, fullData bool, metadata map[string]any) Output {
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
	qm["provider"] = "trino"
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
	resultMetadata := datasourceCompletedQueryResultMetadata("run_trino_query", "trino", qm)
	if offset == 0 {
		artifactID := firstStringMapValue(qm, "artifact_id", "full_artifact_id")
		if artifactID == "" {
			artifactID = largeOutputArtifactID()
		}
		qm["artifact_id"] = artifactID
		qm["full_artifact_id"] = artifactID
		resultMetadata = datasourceCompletedQueryResultMetadata("run_trino_query", "trino", qm)
		resultMetadata = addRowsArtifactReferenceMetadata(out, resultMetadata, artifactID, len(fullRows), len(rows), "json_rows", len(fullRows) > len(rows) || truncatedToConfiguredLimit)
		payload, payloadErr := newLargeRowsArtifactPayload(artifactID, "Query Results", fullRows, "run_trino_query", qm)
		if payloadErr != nil {
			return JSONError("Failed to encode query results for artifact persistence.")
		}
		artifactPayload = payload
	}

	return Output{OK: true, ResultFormat: ResultFormatJSON, Result: out, ResultMetadata: resultMetadata, QueryMetadata: qm, Artifact: artifactPayload}
}

func newRunTrinoQueryTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "run_trino_query",
				Description: "Profile and start a durable read-only Trino query whose forward-only result stream is materialized safely. Use check_query_status and get_query_results rather than re-running SQL. To continue failed work, pass the exact query_obligation_id returned by the prior attempt.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"sql": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Trino SQL query to execute.",
						},
						"description": map[string]any{
							"type":        "string",
							"maxLength":   300,
							"description": "One-sentence plain-English summary of what this query answers. Keep it brief (max 300 characters).",
						},
						"catalog": map[string]any{
							"type":        "string",
							"description": "Trino catalog name (optional).",
						},
						"schema": map[string]any{
							"type":        "string",
							"description": "Trino schema name (optional).",
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
			handler := resolveTrinoHandler(exec)
			if handler == nil {
				return JSONError("Trino is not configured."), nil
			}
			sql := normalizeQuerySQLInput(in.SQL)
			if sql == "" {
				return JSONError("Missing sql"), nil
			}
			description := normalizeQueryDescription(in.Description)
			catalog := strings.TrimSpace(in.Catalog)
			schema := strings.TrimSpace(in.Schema)
			requestedLimit, pageSize, isCapped, effectiveConfiguredLimit := resolveQueryPageSize(in.Limit, exec.DatabaseQueryRowLimit)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset > 0 && exec.Jobs != nil {
				return JSONError("run_trino_query never re-executes SQL for pagination. Trino results are durably materialized; call get_query_results with the original job_id and owner_token."), nil
			}
			if offset == 0 && exec.Jobs != nil && exec.Trino != nil {
				querySQL := makeTrinoPagedQuery(sql, effectiveConfiguredLimit+1, 0)
				return launchManagedQuery(ctx, exec, managedQueryLaunchRequest{
					ToolName:          "run_trino_query",
					Provider:          "trino",
					ConnectionID:      "",
					SQL:               sql,
					SubmittedSQL:      querySQL,
					Description:       description,
					Adapter:           queryadapters.NewTrino(exec.Trino),
					Session:           map[string]string{"catalog": catalog, "schema": schema},
					Metadata:          map[string]any{"catalog": catalog, "schema": schema, "source": "ai-analyst-long-horizon", "client_tags": []string{"ai-analyst", "long-horizon"}},
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

			if offset == 0 {
				st, b, err := callTrino(handler, ctx, http.MethodPost, "/api/trino/preflight", map[string]any{
					"sql":     sql,
					"catalog": catalog,
					"schema":  schema,
				})
				_ = st
				if err != nil {
					return JSONError(err.Error()), nil
				}
				v, err := parseJSONBody(b)
				if err != nil {
					return JSONError("Trino preflight failed"), nil
				}
				m := mapStringAny(v)
				ok, _ := m["ok"].(bool)
				if ok != true {
					msg := "Trino preflight failed"
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
							"instructions":     "Preflight failed; rewrite the SQL query and call run_trino_query again. Do not show this preflight error in the UI.",
						},
					}, nil
				}
			}

			querySQL := makeTrinoPagedQuery(sql, pageSize+1, offset)

			if offset == 0 && exec.Jobs != nil {
				submittedAt := time.Now()
				parsed, err := submitTrinoQuery(ctx, exec, querySQL, catalog, schema, 300*time.Millisecond)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				if parsed.Completed {
					meta := cloneStringAnyMap(parsed.Metadata)
					if meta == nil {
						meta = map[string]any{}
					}
					meta["provider"] = "trino"
					meta["sql"] = sql
					meta["sql_preview"] = asyncSQLPreview(sql)
					if description != "" {
						meta["description"] = description
					}
					meta["full_data_requested"] = in.FullData
					return formatTrinoSuccessOutput(rowsFromMapSlice(parsed.Rows), pageSize, offset, requestedLimit, isCapped, effectiveConfiguredLimit, in.FullData, meta), nil
				}

				state := &trinoPollState{
					queryID:  strings.TrimSpace(parsed.QueryID),
					nextURI:  strings.TrimSpace(parsed.NextURI),
					queryURL: firstNonEmpty(parsed.QueryURL, parsed.NextURI),
					catalog:  catalog,
					schema:   schema,
				}
				if state.queryID == "" && state.nextURI == "" {
					return JSONError("Trino async submit failed: missing query reference"), nil
				}

				providerJobID := firstNonEmpty(state.queryID, state.nextURI)
				providerJobURL := firstNonEmpty(state.queryURL, state.nextURI)
				if exec.OnProgress != nil {
					exec.OnProgress(map[string]any{
						"provider": "trino",
						"phase":    "submitted",
						"state":    "RUNNING",
						"query_id": state.queryID,
						"next_uri": state.nextURI,
						"url":      providerJobURL,
						"catalog":  catalog,
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
					"provider":            "trino",
					"sql":                 sql,
					"sql_preview":         sqlPreview,
					"query_id":            state.queryID,
					"next_uri":            state.nextURI,
					"provider_job_id":     providerJobID,
					"provider_job_url":    providerJobURL,
					"url":                 providerJobURL,
					"catalog":             catalog,
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
					"trino",
					sql,
					sqlPreview,
					jobMeta,
					func(pollCtx context.Context) (bool, *jobs.QueryResult, error) {
						polled, done, pollErr := trinoPollOnce(pollCtx, exec, state)
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
						meta["provider"] = "trino"
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
						return cancelTrinoQuery(cancelCtx, exec, state)
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
					"provider":    "trino",
					"sql":         sql,
					"sql_preview": sqlPreview,
					"owner_token": ownerToken,
					"thread_id":   exec.ThreadID,
					"artifact_id": artifactID,
					"message":     "Query submitted to Trino. Use check_query_status to monitor progress.",
				}
				if description != "" {
					result["description"] = description
				}
				qm := map[string]any{
					"provider":            "trino",
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
					"catalog":             catalog,
					"schema":              schema,
				}
				if description != "" {
					qm["description"] = description
				}
				return Output{OK: true, ResultFormat: ResultFormatJSON, Result: result, ResultMetadata: datasourceAsyncQueryResultMetadata("run_trino_query", "trino", qm), QueryMetadata: qm}, nil
			}

			parsed, err := submitTrinoQuery(ctx, exec, querySQL, catalog, schema, 300*time.Millisecond)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			state := &trinoPollState{
				queryID:  strings.TrimSpace(parsed.QueryID),
				nextURI:  strings.TrimSpace(parsed.NextURI),
				queryURL: firstNonEmpty(parsed.QueryURL, parsed.NextURI),
				catalog:  catalog,
				schema:   schema,
			}

			// Follow the next_uri chain for sync calls when available.
			const maxSyncPollSteps = 32
			steps := 0
			collectedRows := rowsFromMapSlice(parsed.Rows)
			collectedSchema := rowsFromMapSlice(parsed.Schema)
			for !parsed.Completed && !parsed.Failed && strings.TrimSpace(state.snapshot().nextURI) != "" && steps < maxSyncPollSteps {
				steps++
				polled, done, pollErr := trinoPollOnce(ctx, exec, state)
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
				return JSONError(firstNonEmpty(parsed.Message, "Trino query failed")), nil
			}
			if !parsed.Completed && strings.TrimSpace(parsed.NextURI) != "" {
				return JSONError("Trino query did not complete in the current call. Retry or use async query status tools."), nil
			}

			rows := collectedRows
			meta := cloneStringAnyMap(parsed.Metadata)
			if meta == nil {
				meta = map[string]any{}
			}
			meta["provider"] = "trino"
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
			return formatTrinoSuccessOutput(rows, pageSize, offset, requestedLimit, isCapped, effectiveConfiguredLimit, in.FullData, meta), nil
		},
	}
}

func newListTrinoCatalogsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_trino_catalogs",
				Description: "List catalogs available in Trino.",
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveTrinoHandler(exec)
			if handler == nil {
				return JSONError("Trino is not configured."), nil
			}
			var in struct{}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			st, b, err := callTrino(handler, ctx, http.MethodGet, "/api/trino/catalogs", nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(trinoErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Trino catalogs request failed"), nil
			}
			m := mapStringAny(v)
			catalogs := asSlice(m["catalogs"])
			if catalogs == nil {
				catalogs = []any{}
			}
			return datasourceMetadataOutput("list_trino_catalogs", "trino", "catalog_list", map[string]any{"catalogs": catalogs}, map[string]any{
				"count": len(catalogs),
			}), nil
		},
	}
}

func newListTrinoSchemasTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_trino_schemas",
				Description: "List schemas for a Trino catalog.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"catalog_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Trino catalog name.",
						},
					},
					"required":             []string{"catalog_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveTrinoHandler(exec)
			if handler == nil {
				return JSONError("Trino is not configured."), nil
			}
			var in struct {
				CatalogName string `json:"catalog_name"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			catalog := strings.TrimSpace(in.CatalogName)
			if catalog == "" {
				return JSONError("catalog_name is required"), nil
			}
			u := "/api/trino/schemas?catalog_name=" + url.QueryEscape(catalog)
			st, b, err := callTrino(handler, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(trinoErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Trino schemas request failed"), nil
			}
			m := mapStringAny(v)
			schemas := asSlice(m["schemas"])
			if schemas == nil {
				schemas = []any{}
			}
			return datasourceMetadataOutput("list_trino_schemas", "trino", "schema_list", map[string]any{"schemas": schemas}, map[string]any{
				"catalog_name": catalog,
				"count":        len(schemas),
			}), nil
		},
	}
}

func newListTrinoTablesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "list_trino_tables",
				Description: "List tables for a Trino catalog and schema.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"catalog_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Trino catalog name.",
						},
						"schema_name": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Trino schema name.",
						},
					},
					"required":             []string{"catalog_name", "schema_name"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			handler := resolveTrinoHandler(exec)
			if handler == nil {
				return JSONError("Trino is not configured."), nil
			}
			var in struct {
				CatalogName string `json:"catalog_name"`
				SchemaName  string `json:"schema_name"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			catalog := strings.TrimSpace(in.CatalogName)
			schema := strings.TrimSpace(in.SchemaName)
			if catalog == "" || schema == "" {
				return JSONError("catalog_name and schema_name are required"), nil
			}
			u := "/api/trino/tables?catalog_name=" + url.QueryEscape(catalog) + "&schema_name=" + url.QueryEscape(schema)
			st, b, err := callTrino(handler, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(trinoErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Trino tables request failed"), nil
			}
			m := mapStringAny(v)
			tables := asSlice(m["tables"])
			if tables == nil {
				tables = []any{}
			}
			return datasourceMetadataOutput("list_trino_tables", "trino", "table_list", map[string]any{"tables": tables}, map[string]any{
				"catalog_name": catalog,
				"schema_name":  schema,
				"count":        len(tables),
			}), nil
		},
	}
}

func newDescribeTrinoTableTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "describe_trino_table",
				Description: "Describe a Trino table by full name (catalog.schema.table).",
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
			handler := resolveTrinoHandler(exec)
			if handler == nil {
				return JSONError("Trino is not configured."), nil
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
			u := "/api/trino/table-schema?full_name=" + url.QueryEscape(fullName)
			st, b, err := callTrino(handler, ctx, http.MethodGet, u, nil)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if st < 200 || st >= 300 {
				return JSONError(trinoErrorMessage(st, b)), nil
			}
			v, err := parseJSONBody(b)
			if err != nil {
				return JSONError("Trino table schema request failed"), nil
			}
			m := mapStringAny(v)
			schema := asSlice(m["schema"])
			if schema == nil {
				schema = asSlice(m["columns"])
			}
			if schema == nil {
				schema = []any{}
			}
			return datasourceMetadataOutput("describe_trino_table", "trino", "table_schema", map[string]any{"schema": schema}, map[string]any{
				"table": fullName,
				"count": len(schema),
			}), nil
		},
	}
}
