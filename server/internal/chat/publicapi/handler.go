package publicapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/store"
)

func New(deps Deps) http.Handler {
	if deps.Store == nil {
		panic("chat/publicapi.New: Store is required")
	}
	if deps.Manager == nil {
		panic("chat/publicapi.New: Manager is required")
	}
	if deps.Settings == nil {
		panic("chat/publicapi.New: Settings is required")
	}
	if strings.TrimSpace(deps.CfgDir) == "" {
		panic("chat/publicapi.New: CfgDir is required")
	}
	maxBody := deps.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 20 << 20
	}
	h := &Handler{
		store:        deps.Store,
		manager:      deps.Manager,
		settings:     deps.Settings,
		cfgDir:       strings.TrimSpace(deps.CfgDir),
		maxBodyBytes: maxBody,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/external/v1/runs", h.requireScopes(h.handleCreateRun, ScopeRunsCreate))
	mux.HandleFunc("GET /api/external/v1/runs/{runId}", h.requireScopes(h.handleGetRun, ScopeRunsRead))
	mux.HandleFunc("GET /api/external/v1/runs/{runId}/events", h.requireScopes(h.handleRunEvents, ScopeRunsRead))
	mux.HandleFunc("GET /api/external/v1/runs/{runId}/result", h.requireScopes(h.handleRunResult, ScopeRunsRead))
	mux.HandleFunc("POST /api/external/v1/runs/{runId}/cancel", h.requireScopes(h.handleCancelRun, ScopeRunsCancel))
	mux.HandleFunc("GET /api/external/v1/threads/{threadId}/messages", h.requireScopes(h.handleListMessages, ScopeThreadsRead))
	mux.HandleFunc("POST /api/external/v1/threads/{threadId}/attachments", h.requireScopes(h.handlePostAttachment, ScopeAttachmentsWrite))
	mux.HandleFunc("GET /api/external/v1/artifacts/{artifactId}", h.requireScopes(h.handleGetArtifact, ScopeArtifactsRead))
	mux.HandleFunc("GET /api/external/v1/artifacts/{artifactId}/rows", h.requireScopes(h.handleGetArtifactRows, ScopeArtifactsRead))
	mux.HandleFunc("GET /api/external/v1/queries/{jobId}/status", h.requireScopes(h.handleGetQueryStatus, ScopeQueriesRead))
	mux.HandleFunc("POST /api/external/v1/queries/{jobId}/cancel", h.requireScopes(h.handleCancelQuery, ScopeQueriesCancel))
	mux.HandleFunc("GET /api/external/v1/status", h.requireScopes(h.handleStatus))
	return mux
}

func (h *Handler) requireScopes(next http.HandlerFunc, scopes ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth, err := authenticateAPIKey(h.settings, r.Header.Get("Authorization"), scopes...)
		if err != nil {
			writeError(w, err)
			return
		}
		r = r.WithContext(withAuthInfo(r.Context(), auth))
		next(w, r)
	}
}

func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object":  "api.status",
		"ok":      true,
		"version": "v1",
	})
}

func (h *Handler) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var body createRunRequest
	if err := decodeJSONLimit(r.Body, &body, h.maxBodyBytes); err != nil {
		writeError(w, invalidRequest(err.Error()))
		return
	}
	req, err := normalizeCreateRunRequest(body)
	if err != nil {
		writeError(w, err)
		return
	}

	var thread store.Thread
	if strings.TrimSpace(req.ThreadID) == "" {
		if !authInfoFromContext(r.Context()).canCreateThread(req.ProjectIDOverride) {
			writeError(w, permissionDenied("API key is not allowed to create threads in this project scope"))
			return
		}
		thread, err = h.store.CreateThread(r.Context(), req.ProjectIDOverride)
		if err != nil {
			h.writeStoreErr(w, err)
			return
		}
		req.ThreadID = thread.ID
	} else {
		thread, err = h.store.GetThread(r.Context(), req.ThreadID)
		if err != nil {
			h.writeStoreErr(w, err)
			return
		}
		if !authInfoFromContext(r.Context()).canAccessThread(thread) {
			writeError(w, permissionDenied("API key is not allowed to access this thread"))
			return
		}
	}

	res, err := h.manager.StartRun(r.Context(), req)
	if err != nil {
		var conflict *optionc.ActiveRunConflictError
		if errors.As(err, &conflict) {
			payload := errorEnvelope("run_active", conflict.Error(), "")
			if runID := conflict.ActiveRunID(); runID != "" {
				payload["active_run_id"] = runID
			}
			writeJSON(w, http.StatusConflict, payload)
			return
		}
		h.writeStoreErr(w, err)
		return
	}
	if strings.TrimSpace(res.Thread.ID) != "" {
		thread = res.Thread
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":     "run.create_response",
		"thread":     publicThreadDTO(thread),
		"run":        publicRunDTO(res.Run),
		"events_url": "/api/external/v1/runs/" + res.Run.ID + "/events",
		"result_url": "/api/external/v1/runs/" + res.Run.ID + "/result",
	})
}

func (h *Handler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.getRunFromRequest(r)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicRunDTO(run))
}

func (h *Handler) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.getRunFromRequest(r)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if err := h.manager.Cancel(run.ID); err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if updated, err := h.store.GetRun(r.Context(), run.ID); err == nil {
		run = updated
	}
	writeJSON(w, http.StatusOK, publicRunDTO(run))
}

func (h *Handler) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	run, err := h.getRunFromRequest(r)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, apiError(http.StatusInternalServerError, "streaming_not_supported", "streaming not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_ = writeSSEComment(w, "connected")
	flusher.Flush()

	ch, cancel, err := h.manager.Subscribe(run.ID)
	if err != nil {
		_ = writeSSE(w, EventError, PublicEvent{
			Object:    "run.event",
			RunID:     run.ID,
			ThreadID:  run.ThreadID,
			Type:      EventError,
			CreatedAt: time.Now().UnixMilli(),
			Data:      map[string]any{"error": map[string]any{"code": "stream_error", "message": err.Error()}},
		})
		flusher.Flush()
		return
	}
	defer cancel()

	after := parseInt64(r.URL.Query().Get("after"), 0)
	fallbackSequence := int64(0)
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_ = writeSSEComment(w, "ping")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			sequence := ev.Seq
			if sequence <= 0 {
				fallbackSequence++
				sequence = fallbackSequence
			}
			if after > 0 && sequence <= after {
				continue
			}
			if latest, err := h.store.GetRun(r.Context(), run.ID); err == nil {
				run = latest
			}
			publicEvent, err := mapSSEEvent(run, ev, sequence)
			if err != nil {
				publicEvent = PublicEvent{
					Object:    "run.event",
					RunID:     run.ID,
					ThreadID:  run.ThreadID,
					Sequence:  sequence,
					Type:      EventError,
					CreatedAt: time.Now().UnixMilli(),
					Data:      map[string]any{"error": map[string]any{"code": "stream_error", "message": err.Error()}},
				}
			}
			if err := writeSSE(w, publicEvent.Type, publicEvent); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (h *Handler) handleRunResult(w http.ResponseWriter, r *http.Request) {
	run, err := h.getRunFromRequest(r)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if parseBool(r.URL.Query().Get("wait")) {
		timeoutMs := parseInt64(r.URL.Query().Get("timeout_ms"), 30000)
		if timeoutMs < 0 {
			timeoutMs = 0
		}
		if timeoutMs > 30000 {
			timeoutMs = 30000
		}
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		for !runTerminal(run.Status) && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			if latest, err := h.store.GetRun(r.Context(), run.ID); err == nil {
				run = latest
			}
		}
	}
	if !runTerminal(run.Status) {
		writeError(w, apiError(http.StatusConflict, "run_not_terminal", "run is not terminal"))
		return
	}
	result, err := h.buildRunResult(
		r.Context(),
		run,
		parseBoolDefault(r.URL.Query().Get("include_artifacts"), true),
		parseBoolDefault(r.URL.Query().Get("include_tool_results"), true),
	)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) handleListMessages(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadId"))
	if threadID == "" {
		writeError(w, invalidRequest("missing threadId"))
		return
	}
	thread, err := h.store.GetThread(r.Context(), threadID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if !authInfoFromContext(r.Context()).canAccessThread(thread) {
		writeError(w, permissionDenied("API key is not allowed to access this thread"))
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), 100, 1, 500)
	var afterSeq *int64
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		v := parseInt64(raw, -1)
		if v >= 0 {
			afterSeq = &v
		}
	}
	msgs, err := h.store.ListMessages(r.Context(), store.ListMessagesParams{
		ThreadID: threadID,
		Limit:    limit,
		AfterSeq: afterSeq,
		OrderBy:  "seq_asc",
	})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, publicMessageDTO(msg))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": out, "has_more": len(out) == limit})
}

func (h *Handler) handleGetArtifact(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeError(w, invalidRequest("missing artifactId"))
		return
	}
	artifact, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if err := h.requireArtifactAccess(r, artifact); err != nil {
		writeError(w, err)
		return
	}
	content, err := h.readArtifactContent(artifact, 8<<20)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, publicArtifactDTO(artifact, content))
}

func (h *Handler) handleGetArtifactRows(w http.ResponseWriter, r *http.Request) {
	artifactID := strings.TrimSpace(r.PathValue("artifactId"))
	if artifactID == "" {
		writeError(w, invalidRequest("missing artifactId"))
		return
	}
	offset := int(parseInt64(r.URL.Query().Get("offset"), 0))
	if offset < 0 {
		writeError(w, invalidRequest("offset must be non-negative"))
		return
	}
	limit := parseInt(r.URL.Query().Get("limit"), defaultRowsLimit, 1, maxRowsLimit)
	columns := parseColumns(r.URL.Query().Get("columns"))
	artifact, err := h.store.GetArtifact(r.Context(), artifactID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if err := h.requireArtifactAccess(r, artifact); err != nil {
		writeError(w, err)
		return
	}
	raw, err := h.readArtifactContent(artifact, maxArtifactReadBytes)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	rows, err := parseRows(raw)
	if err != nil {
		writeError(w, invalidRequest(err.Error()))
		return
	}
	total := len(rows)
	if offset > total {
		writeError(w, invalidRequest("offset out of range"))
		return
	}
	end := offset + limit
	if end > total {
		end = total
	}
	selected := projectRows(rows[offset:end], columns)
	selected = clampRowsByBytes(selected, maxArtifactReplyBytes)
	hasMore := end < total || len(selected) < end-offset
	nextOffset := offset + len(selected)
	writeJSON(w, http.StatusOK, map[string]any{
		"object":            "artifact.rows",
		"artifact_id":       artifact.ID,
		"rows":              selected,
		"offset":            offset,
		"next_offset":       nextOffset,
		"limit":             limit,
		"rows_returned":     len(selected),
		"row_count_total":   total,
		"has_more":          hasMore,
		"requested_columns": columns,
	})
}

func (h *Handler) handleGetQueryStatus(w http.ResponseWriter, r *http.Request) {
	h.handleQueryJobOp(w, r, false)
}

func (h *Handler) handleCancelQuery(w http.ResponseWriter, r *http.Request) {
	h.handleQueryJobOp(w, r, true)
}

func (h *Handler) handleQueryJobOp(w http.ResponseWriter, r *http.Request, cancel bool) {
	jobID := strings.TrimSpace(r.PathValue("jobId"))
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	if threadID == "" {
		threadID = strings.TrimSpace(r.URL.Query().Get("threadId"))
	}
	ownerToken := strings.TrimSpace(r.Header.Get("x-query-owner-token"))
	runID := strings.TrimSpace(r.URL.Query().Get("run_id"))
	if runID == "" {
		runID = strings.TrimSpace(r.URL.Query().Get("runId"))
	}
	queryObligationID := strings.TrimSpace(r.URL.Query().Get("query_obligation_id"))
	if queryObligationID == "" {
		queryObligationID = strings.TrimSpace(r.URL.Query().Get("queryObligationId"))
	}
	if jobID == "" || threadID == "" {
		writeError(w, invalidRequest("jobId and thread_id are required"))
		return
	}
	if ownerToken == "" {
		writeError(w, apiError(http.StatusUnauthorized, "query_owner_token_required", "x-query-owner-token is required"))
		return
	}
	thread, err := h.store.GetThread(r.Context(), threadID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if !authInfoFromContext(r.Context()).canAccessThread(thread) {
		writeError(w, permissionDenied("API key is not allowed to access this thread"))
		return
	}
	var payload map[string]any
	if cancel {
		payload, err = h.manager.CancelQueryJob(r.Context(), jobID, threadID, ownerToken)
	} else {
		if scoped, ok := h.manager.(interface {
			GetQueryJobStatusForRun(context.Context, string, string, string, string, string) (map[string]any, error)
		}); ok {
			payload, err = scoped.GetQueryJobStatusForRun(
				r.Context(), jobID, threadID, ownerToken, runID, queryObligationID,
			)
		} else if runID != "" || queryObligationID != "" {
			err = invalidRequest("run-scoped query status is unavailable")
		} else {
			payload, err = h.manager.GetQueryJobStatus(r.Context(), jobID, threadID, ownerToken)
		}
	}
	if err != nil {
		h.writeQueryJobErr(w, err)
		return
	}
	delete(payload, "owner_token")
	delete(payload, "ownerToken")
	payload["object"] = "query.status"
	writeJSON(w, http.StatusOK, payload)
}

func (h *Handler) getRunFromRequest(r *http.Request) (store.Run, error) {
	runID := strings.TrimSpace(r.PathValue("runId"))
	if runID == "" {
		return store.Run{}, invalidRequest("missing runId")
	}
	run, err := h.store.GetRun(r.Context(), runID)
	if err != nil {
		return store.Run{}, err
	}
	thread, err := h.store.GetThread(r.Context(), run.ThreadID)
	if err != nil {
		return store.Run{}, err
	}
	if !authInfoFromContext(r.Context()).canAccessThread(thread) {
		return store.Run{}, permissionDenied("API key is not allowed to access this run")
	}
	return run, nil
}

func (h *Handler) requireArtifactAccess(r *http.Request, artifact store.Artifact) error {
	thread, err := h.store.GetThread(r.Context(), artifact.ThreadID)
	if err != nil {
		return err
	}
	if !authInfoFromContext(r.Context()).canAccessThread(thread) {
		return permissionDenied("API key is not allowed to access this artifact")
	}
	return nil
}

func (h *Handler) buildRunResult(ctx context.Context, run store.Run, includeArtifacts bool, includeToolResults bool) (map[string]any, error) {
	thread, err := h.store.GetThread(ctx, run.ThreadID)
	if err != nil {
		return nil, err
	}
	allMessages, err := h.store.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: run.ThreadID,
		Limit:    2000,
		OrderBy:  "seq_asc",
	})
	if err != nil {
		return nil, err
	}
	messages := make([]store.Message, 0)
	messageIDs := map[string]struct{}{}
	artifactIDs := map[string]struct{}{}
	var answerParts []string
	for _, msg := range allMessages {
		if msg.RunID == nil || strings.TrimSpace(*msg.RunID) != run.ID {
			continue
		}
		messages = append(messages, msg)
		messageIDs[msg.ID] = struct{}{}
		for _, artifactID := range msg.ReferencedArtifactIDs {
			if strings.TrimSpace(artifactID) != "" {
				artifactIDs[strings.TrimSpace(artifactID)] = struct{}{}
			}
		}
		if msg.Role == "assistant" && msg.Content != nil && strings.TrimSpace(*msg.Content) != "" {
			answerParts = append(answerParts, strings.TrimSpace(*msg.Content))
		}
		if msg.Role == "tool" && msg.Content != nil {
			if tr, ok := parseToolResultEnvelope(*msg.Content); ok {
				if artifactID := strings.TrimSpace(stringValue(tr["artifact_id"])); artifactID != "" {
					artifactIDs[artifactID] = struct{}{}
				}
			}
		}
	}
	messageDTOs := make([]map[string]any, 0, len(messages))
	assistantDTOs := make([]map[string]any, 0)
	toolResults := make([]map[string]any, 0)
	for _, msg := range messages {
		dto := publicRunResultMessageDTO(msg)
		messageDTOs = append(messageDTOs, dto)
		if msg.Role == "assistant" {
			assistantDTOs = append(assistantDTOs, dto)
		}
		if tr, ok := dto["tool_result"].(map[string]any); ok {
			if includeToolResults {
				toolResults = append(toolResults, tr)
			}
		}
	}

	artifacts := []map[string]any{}
	if includeArtifacts {
		list, err := h.store.ListArtifacts(ctx, run.ThreadID, 2000)
		if err != nil {
			return nil, err
		}
		sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
		for _, artifact := range list {
			include := false
			if _, ok := artifactIDs[artifact.ID]; ok {
				include = true
			}
			if artifact.MessageID != nil {
				if _, ok := messageIDs[*artifact.MessageID]; ok {
					include = true
				}
			}
			if !include {
				continue
			}
			content, truncated, err := h.readArtifactContentPreview(artifact, maxRunResultArtifactPreviewBytes)
			if err != nil {
				return nil, err
			}
			artifacts = append(artifacts, publicArtifactPreviewDTO(artifact, content, truncated))
		}
	}
	usage := map[string]any{}
	if run.RequestID != nil {
		usage["request_id"] = *run.RequestID
	}
	if run.InputTokens != nil {
		usage["input_tokens"] = *run.InputTokens
	}
	if run.OutputTokens != nil {
		usage["output_tokens"] = *run.OutputTokens
	}
	if run.TotalTokens != nil {
		usage["total_tokens"] = *run.TotalTokens
	}
	return map[string]any{
		"object":             "run.result",
		"run":                publicRunDTO(run),
		"thread":             publicThreadDTO(thread),
		"messages":           messageDTOs,
		"assistant_messages": assistantDTOs,
		"tool_results":       toolResults,
		"artifacts":          artifacts,
		"answer":             strings.Join(answerParts, "\n\n"),
		"error":              publicRunError(run),
		"usage":              usage,
	}, nil
}

func (h *Handler) writeStoreErr(w http.ResponseWriter, err error) {
	if err == nil {
		writeError(w, apiError(http.StatusInternalServerError, "internal_error", "internal error"))
		return
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		writeError(w, apiErr)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, apiError(http.StatusNotFound, "not_found", "not found"))
	case errors.Is(err, store.ErrConflict):
		writeError(w, apiError(http.StatusConflict, "conflict", err.Error()))
	default:
		writeError(w, apiError(http.StatusInternalServerError, "internal_error", err.Error()))
	}
}

func (h *Handler) writeQueryJobErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, optionc.ErrQueryJobsUnavailable):
		writeError(w, apiError(http.StatusServiceUnavailable, "query_jobs_unavailable", err.Error()))
	case errors.Is(err, optionc.ErrQueryJobUnauthorized), errors.Is(err, optionc.ErrQueryJobThreadScope), errors.Is(err, optionc.ErrQueryJobRunScope):
		writeError(w, permissionDenied("query job access denied"))
	default:
		h.writeStoreErr(w, err)
	}
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

func parseInt(raw string, def int, min int, max int) int {
	n := int(parseInt64(raw, int64(def)))
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

func parseInt64(raw string, def int64) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return def
	}
	return n
}

func parseBool(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func parseBoolDefault(raw string, def bool) bool {
	if strings.TrimSpace(raw) == "" {
		return def
	}
	return parseBool(raw)
}

func runTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "cancelled":
		return true
	default:
		return false
	}
}

func parseColumns(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
