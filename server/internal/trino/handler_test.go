package trino

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestHandleQueryFollowsNextURIChain(t *testing.T) {
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/statement":
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("X-Trino-User"); got != "alice" {
				t.Fatalf("X-Trino-User = %q, want alice", got)
			}
			if got := r.Header.Get("X-Trino-Time-Zone"); got != "UTC" {
				t.Fatalf("X-Trino-Time-Zone = %q, want UTC", got)
			}
			writeJSONTest(w, map[string]any{
				"id":      "q1",
				"columns": []map[string]any{{"name": "value", "type": "bigint"}},
				"data":    [][]any{{1}},
				"nextUri": serverURL + "/next1",
			})
		case "/next1":
			writeJSONTest(w, map[string]any{
				"id":      "q1",
				"data":    [][]any{{2}},
				"nextUri": serverURL + "/next2",
			})
		case "/next2":
			writeJSONTest(w, map[string]any{
				"id":   "q1",
				"data": [][]any{{3}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, ts.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/trino/query", strings.NewReader(`{"sql":"SELECT 1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var out struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Rows) != 3 {
		t.Fatalf("rows len = %d, want 3", len(out.Rows))
	}
}

func TestHandleQueryReturnsPartialRowsOnLaterError(t *testing.T) {
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/statement":
			writeJSONTest(w, map[string]any{
				"id":      "q2",
				"columns": []map[string]any{{"name": "value", "type": "bigint"}},
				"data":    [][]any{{10}},
				"nextUri": serverURL + "/next-error",
			})
		case "/next-error":
			writeJSONTest(w, map[string]any{
				"id":    "q2",
				"error": map[string]any{"message": "boom"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, ts.URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/trino/query", strings.NewReader(`{"sql":"SELECT 1"}`))
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502, body=%s", rec.Code, rec.Body.String())
	}

	var out struct {
		PartialRows []map[string]any `json:"partial_rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.PartialRows) != 1 {
		t.Fatalf("partial_rows len = %d, want 1", len(out.PartialRows))
	}
}

func TestSubmitQueryCompletesWithEmptyResultSet(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/statement" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		writeJSONTest(w, map[string]any{
			"id": "q-empty",
		})
	}))
	defer ts.Close()

	h := newQueryTestHandler(t, ts.URL)
	result, err := h.SubmitQuery(context.Background(), "SELECT 1", "", "", "", 0)
	if err != nil {
		t.Fatalf("SubmitQuery: %v", err)
	}
	if result == nil {
		t.Fatalf("SubmitQuery returned nil result")
	}
	if !result.Completed {
		t.Fatalf("Completed = false, want true")
	}
	if result.QueryID != "q-empty" {
		t.Fatalf("QueryID = %q, want q-empty", result.QueryID)
	}
	if len(result.Rows) != 0 {
		t.Fatalf("rows len = %d, want 0", len(result.Rows))
	}
	if got := strings.TrimSpace(firstString(result.Metadata["state"])); got != "FINISHED" {
		t.Fatalf("metadata.state = %q, want FINISHED", got)
	}
	if got := strings.TrimSpace(firstString(result.Metadata["terminal_status"])); got != "completed" {
		t.Fatalf("metadata.terminal_status = %q, want completed", got)
	}
}

func TestCancelQueryFallsBackToQueryEndpoint(t *testing.T) {
	var mu sync.Mutex
	deletePaths := make([]string, 0, 2)
	var serverURL string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/next-delete":
			mu.Lock()
			deletePaths = append(deletePaths, r.URL.Path)
			mu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/query/q-cancel":
			mu.Lock()
			deletePaths = append(deletePaths, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/v1/statement":
			writeJSONTest(w, map[string]any{"id": "unused"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	serverURL = ts.URL

	h := newQueryTestHandler(t, serverURL)
	if err := h.CancelQuery(context.Background(), "q-cancel", serverURL+"/next-delete", "", "", ""); err != nil {
		t.Fatalf("CancelQuery: %v", err)
	}

	mu.Lock()
	paths := append([]string(nil), deletePaths...)
	mu.Unlock()
	if len(paths) != 2 {
		t.Fatalf("DELETE paths len = %d, want 2, got %v", len(paths), paths)
	}
	if paths[0] != "/next-delete" {
		t.Fatalf("first DELETE path = %q, want /next-delete", paths[0])
	}
	if paths[1] != "/v1/query/q-cancel" {
		t.Fatalf("second DELETE path = %q, want /v1/query/q-cancel", paths[1])
	}
}

func TestPollQueryAppliesPerRequestTimeout(t *testing.T) {
	h := newQueryTestHandler(t, "http://trino.local")
	h.upstreamRequestTimeout = 25 * time.Millisecond
	h.httpc = &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}

	_, err := h.PollQuery(
		context.Background(),
		"q-timeout",
		"https://trino.local/v1/statement/queued/1",
		"",
		"",
		"",
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PollQuery error = %v, want deadline exceeded", err)
	}
}

func newQueryTestHandler(t *testing.T, host string) *Handler {
	t.Helper()
	dir := t.TempDir()
	settings, err := config.NewStore(dir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	cfg := settings.Get()
	cfg.TrinoHost = testStrPtr(host)
	cfg.TrinoUser = testStrPtr("alice")
	if err := settings.Save(cfg); err != nil {
		t.Fatalf("settings.Save: %v", err)
	}

	sec, err := secrets.NewStore(dir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	if err := sec.Init("passphrase"); err != nil {
		t.Fatalf("secrets.Init: %v", err)
	}
	if err := sec.Unlock("passphrase"); err != nil {
		t.Fatalf("secrets.Unlock: %v", err)
	}
	if err := sec.SetTrinoNone(); err != nil {
		t.Fatalf("SetTrinoNone: %v", err)
	}

	return NewHandler(settings, sec)
}

func writeJSONTest(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func testStrPtr(v string) *string {
	s := v
	return &s
}

func firstString(v any) string {
	s, _ := v.(string)
	return s
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
