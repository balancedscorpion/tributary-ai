package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/model"
	"ai-analyst-local/internal/chat/runs"
)

type Deps struct {
	Store model.Store
	Runs  *runs.Manager

	// MaxBodyBytes caps JSON request bodies. Default: 1MiB.
	MaxBodyBytes int64
}

type Handler struct {
	store        model.Store
	runs         *runs.Manager
	maxBodyBytes int64
}

func NewChatHandler(deps Deps) http.Handler {
	if deps.Store == nil {
		panic("chat/api.NewChatHandler: Store is required")
	}
	maxBody := deps.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = 1 << 20
	}
	h := &Handler{store: deps.Store, runs: deps.Runs, maxBodyBytes: maxBody}

	mux := http.NewServeMux()

	// Threads
	mux.HandleFunc("POST /api/chat/threads", h.handleCreateThread)
	mux.HandleFunc("GET /api/chat/threads", h.handleListThreads)
	mux.HandleFunc("GET /api/chat/threads/{threadID}", h.handleGetThread)

	// Messages
	mux.HandleFunc("POST /api/chat/threads/{threadID}/messages", h.handleCreateMessage)
	mux.HandleFunc("GET /api/chat/threads/{threadID}/messages", h.handleListMessages)

	// Runs
	mux.HandleFunc("POST /api/chat/threads/{threadID}/runs", h.handleCreateRun)
	mux.HandleFunc("GET /api/chat/threads/{threadID}/runs", h.handleListRuns)
	mux.HandleFunc("GET /api/chat/threads/{threadID}/runs/{runID}", h.handleGetRun)
	mux.HandleFunc("POST /api/chat/threads/{threadID}/runs/{runID}/cancel", h.handleCancelRun)
	mux.HandleFunc("GET /api/chat/threads/{threadID}/runs/{runID}/events", h.handleRunEvents)

	return mux
}

func (h *Handler) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Metadata map[string]any `json:"metadata,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_JSON", "message": err.Error()}})
		return
	}

	thread, err := h.store.CreateThread(r.Context(), model.CreateThreadParams{ID: model.NewID("thread"), Metadata: body.Metadata})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if thread.Object == "" {
		thread.Object = "chat.thread"
	}
	writeJSON(w, http.StatusOK, thread)
}

func (h *Handler) handleGetThread(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID"}})
		return
	}
	thread, err := h.store.GetThread(r.Context(), threadID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if thread.Object == "" {
		thread.Object = "chat.thread"
	}
	writeJSON(w, http.StatusOK, thread)
}

func (h *Handler) handleListThreads(w http.ResponseWriter, r *http.Request) {
	limit := parseLimit(r, 50, 1, 200)
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	threads, err := h.store.ListThreads(r.Context(), model.ListThreadsParams{Limit: limit, After: after})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	for i := range threads {
		if threads[i].Object == "" {
			threads[i].Object = "chat.thread"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": threads})
}

func (h *Handler) handleCreateMessage(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID"}})
		return
	}

	var body struct {
		Role     model.MessageRole `json:"role"`
		Content  string            `json:"content"`
		Metadata map[string]any    `json:"metadata,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_JSON", "message": err.Error()}})
		return
	}
	body.Content = strings.TrimSpace(body.Content)
	if !body.Role.Valid() {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "invalid role"}})
		return
	}
	if body.Content == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "content is required"}})
		return
	}

	// Validate thread exists.
	if _, err := h.store.GetThread(r.Context(), threadID); err != nil {
		h.writeStoreErr(w, err)
		return
	}

	msg, err := h.store.CreateMessage(r.Context(), model.CreateMessageParams{
		ID:       model.NewID("msg"),
		ThreadID: threadID,
		Role:     body.Role,
		Content:  body.Content,
		Metadata: body.Metadata,
	})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if msg.Object == "" {
		msg.Object = "chat.message"
	}
	writeJSON(w, http.StatusOK, msg)
}

func (h *Handler) handleListMessages(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID"}})
		return
	}
	limit := parseLimit(r, 100, 1, 500)
	after := strings.TrimSpace(r.URL.Query().Get("after"))

	msgs, err := h.store.ListMessages(r.Context(), model.ListMessagesParams{ThreadID: threadID, Limit: limit, After: after})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	for i := range msgs {
		if msgs[i].Object == "" {
			msgs[i].Object = "chat.message"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": msgs})
}

func (h *Handler) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": "RUNS_NOT_CONFIGURED", "message": "run manager not configured"}})
		return
	}

	threadID := strings.TrimSpace(r.PathValue("threadID"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID"}})
		return
	}

	var body struct {
		Instructions string         `json:"instructions,omitempty"`
		Metadata     map[string]any `json:"metadata,omitempty"`
	}
	if err := decodeJSONLimit(w, r, &body, h.maxBodyBytes); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_JSON", "message": err.Error()}})
		return
	}

	run, err := h.runs.CreateAndStart(r.Context(), runs.CreateAndStartParams{ThreadID: threadID, Instructions: body.Instructions, Metadata: body.Metadata})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if run.Object == "" {
		run.Object = "chat.run"
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) handleGetRun(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	runID := strings.TrimSpace(r.PathValue("runID"))
	if threadID == "" || runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID or runID"}})
		return
	}
	run, err := h.store.GetRun(r.Context(), runID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if run.ThreadID != threadID {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "run not found"}})
		return
	}
	if run.Object == "" {
		run.Object = "chat.run"
	}
	writeJSON(w, http.StatusOK, run)
}

func (h *Handler) handleListRuns(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	if threadID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID"}})
		return
	}
	limit := parseLimit(r, 50, 1, 200)
	after := strings.TrimSpace(r.URL.Query().Get("after"))
	runsList, err := h.store.ListRuns(r.Context(), model.ListRunsParams{ThreadID: threadID, Limit: limit, After: after})
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	for i := range runsList {
		if runsList[i].Object == "" {
			runsList[i].Object = "chat.run"
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": runsList})
}

func (h *Handler) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	if h.runs == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": map[string]any{"code": "RUNS_NOT_CONFIGURED", "message": "run manager not configured"}})
		return
	}
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	runID := strings.TrimSpace(r.PathValue("runID"))
	if threadID == "" || runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID or runID"}})
		return
	}
	run, err := h.store.GetRun(r.Context(), runID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if run.ThreadID != threadID {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "run not found"}})
		return
	}
	updated, err := h.runs.Cancel(r.Context(), runID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if updated.Object == "" {
		updated.Object = "chat.run"
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *Handler) handleRunEvents(w http.ResponseWriter, r *http.Request) {
	threadID := strings.TrimSpace(r.PathValue("threadID"))
	runID := strings.TrimSpace(r.PathValue("runID"))
	if threadID == "" || runID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": "missing threadID or runID"}})
		return
	}

	ctx := r.Context()

	run, err := h.store.GetRun(ctx, runID)
	if err != nil {
		h.writeStoreErr(w, err)
		return
	}
	if run.ThreadID != threadID {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "run not found"}})
		return
	}

	after := int64(0)
	if v := strings.TrimSpace(r.URL.Query().Get("after")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			after = n
		}
	}

	f, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	_ = writeSSEComment(w, "connected")
	f.Flush()

	lastSeq := after
	for {
		events, err := h.store.ListRunEvents(ctx, model.ListRunEventsParams{RunID: runID, AfterSeq: lastSeq, Limit: 500})
		if err != nil {
			h.writeSSEError(w, f, err)
			return
		}
		if len(events) == 0 {
			break
		}
		for i := range events {
			if err := writeSSE(w, events[i].Type, events[i].Data); err != nil {
				return
			}
			f.Flush()
			lastSeq = events[i].Seq
		}
		if len(events) < 500 {
			break
		}
	}

	// If the run is already terminal and there are no live events to follow, end the stream.
	run, _ = h.store.GetRun(ctx, runID)
	if run.Status.Terminal() {
		return
	}

	if h.runs == nil {
		h.writeSSEError(w, f, errors.New("run manager not configured"))
		return
	}

	ch, cancel, err := h.runs.Subscribe(runID, lastSeq)
	if err != nil {
		// No live channel; if the run isn't terminal, surface a stream error.
		run, _ = h.store.GetRun(ctx, runID)
		if !run.Status.Terminal() {
			h.writeSSEError(w, f, err)
		}
		return
	}
	defer cancel()

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			_ = writeSSEComment(w, "ping")
			f.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, ev.Type, ev.Data); err != nil {
				return
			}
			f.Flush()
			lastSeq = ev.Seq
		}
	}
}

func (h *Handler) writeSSEError(w http.ResponseWriter, f http.Flusher, err error) {
	// Best-effort: send a structured error event then end the stream.
	payload := []byte(`{"type":"error","payload":{"code":"STREAM_ERROR","message":"` + escapeJSONString(err.Error()) + `"}}`)
	_ = writeSSE(w, runs.EventError, payload)
	f.Flush()
}

func escapeJSONString(s string) string {
	// Minimal escaping for embedding strings into a JSON literal.
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}

func (h *Handler) writeStoreErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, model.ErrNotFound):
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]any{"code": "NOT_FOUND", "message": "not found"}})
	case errors.Is(err, model.ErrConflict):
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{"code": "CONFLICT", "message": err.Error()}})
	case errors.Is(err, model.ErrForbidden):
		writeJSON(w, http.StatusForbidden, map[string]any{"error": map[string]any{"code": "FORBIDDEN", "message": err.Error()}})
	case errors.Is(err, model.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]any{"code": "INVALID_REQUEST", "message": err.Error()}})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "INTERNAL", "message": err.Error()}})
	}
}

func parseLimit(r *http.Request, def int, min int, max int) int {
	q := strings.TrimSpace(r.URL.Query().Get("limit"))
	if q == "" {
		return def
	}
	n, err := strconv.Atoi(q)
	if err != nil {
		return def
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// Ensure we compile even if a store implementation does not set created timestamps.
func normalizeTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}
