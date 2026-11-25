package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestQueryEndpoints_StatusAndCancelSuccess(t *testing.T) {
	t.Parallel()

	handler, threadID, _, _, _ := newQueryEndpointHandler(t)
	ownerToken := "owner-token-1"

	statusReq := httptest.NewRequest(http.MethodGet, "/api/queries/job-1/status?thread_id="+threadID, nil)
	statusReq.SetPathValue("jobId", "job-1")
	statusReq.Header.Set("x-query-owner-token", ownerToken)
	statusRec := httptest.NewRecorder()
	handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", statusRec.Code, strings.TrimSpace(statusRec.Body.String()))
	}
	var statusBody map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &statusBody); err != nil {
		t.Fatalf("decode status response: %v", err)
	}
	if got := strings.TrimSpace(toString(statusBody["status"])); got != "running" {
		t.Fatalf("status=%q, want running", got)
	}
	if got := strings.TrimSpace(toString(statusBody["provider_job_id"])); got != "stmt-1" {
		t.Fatalf("provider_job_id=%q, want stmt-1", got)
	}
	if got := strings.TrimSpace(toString(statusBody["provider_job_url"])); got != "https://dbc.example.com/sql/history/stmt-1" {
		t.Fatalf("provider_job_url=%q, want query history url", got)
	}
	if got := strings.TrimSpace(toString(statusBody["statement_id"])); got != "stmt-1" {
		t.Fatalf("statement_id=%q, want stmt-1", got)
	}
	if got := strings.TrimSpace(toString(statusBody["url"])); got != "https://dbc.example.com/sql/history/stmt-1" {
		t.Fatalf("url=%q, want query history url", got)
	}

	cancelReq := httptest.NewRequest(http.MethodPost, "/api/queries/job-1/cancel?thread_id="+threadID, nil)
	cancelReq.SetPathValue("jobId", "job-1")
	cancelReq.Header.Set("x-query-owner-token", ownerToken)
	cancelRec := httptest.NewRecorder()
	handler.ServeHTTP(cancelRec, cancelReq)
	if cancelRec.Code != http.StatusOK {
		t.Fatalf("cancel code=%d body=%s", cancelRec.Code, strings.TrimSpace(cancelRec.Body.String()))
	}
	var cancelBody map[string]any
	if err := json.Unmarshal(cancelRec.Body.Bytes(), &cancelBody); err != nil {
		t.Fatalf("decode cancel response: %v", err)
	}
	okVal, _ := cancelBody["ok"].(bool)
	if !okVal {
		t.Fatalf("cancel ok=false, want true")
	}
	if got := strings.TrimSpace(toString(cancelBody["status"])); got != "cancelled" {
		t.Fatalf("cancel status=%q, want cancelled", got)
	}
}

func TestQueryEndpoints_StatusUsesRequestingRunDependencyIdentity(t *testing.T) {
	t.Parallel()

	handler, threadID, _, repository, queryJobs := newQueryEndpointHandler(t)
	ctx := context.Background()
	run, err := repository.CreateRun(ctx, threadID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID:          "job-1",
		Provider:    "databricks",
		Status:      jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: threadID, RunID: run.ID, Handle: "owner-token-1"},
		SubmittedAt: now,
		LastAccess:  now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("UpsertQueryJobCAS: %v", err)
	}
	obligationID, err := store.QueryObligationIDForRoot(run.ID, record.ID)
	if err != nil {
		t.Fatalf("QueryObligationIDForRoot: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID:                 run.ID,
		JobID:                 record.ID,
		RequiredForCompletion: true,
		ObligationID:          obligationID,
	}); err != nil {
		t.Fatalf("UpsertRunQueryDependency: %v", err)
	}
	// The registry's identity is deliberately wrong; the HTTP response must
	// use the requesting run's durable dependency instead.
	queryJobs.jobs[record.ID]["run_id"] = "wrong-run"
	queryJobs.jobs[record.ID]["query_obligation_id"] = "qob-11111111111111111111111111111111"

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/queries/job-1/status?thread_id="+threadID+"&run_id="+run.ID+"&query_obligation_id="+obligationID,
		nil,
	)
	req.SetPathValue("jobId", record.ID)
	req.Header.Set("x-query-owner-token", record.Owner.Handle)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := strings.TrimSpace(toString(body["run_id"])); got != run.ID {
		t.Fatalf("run_id=%q want=%q", got, run.ID)
	}
	if got := strings.TrimSpace(toString(body["query_obligation_id"])); got != obligationID {
		t.Fatalf("query_obligation_id=%q want=%q", got, obligationID)
	}
}

func TestQueryEndpoints_StatusAuthAndThreadFailures(t *testing.T) {
	t.Parallel()

	handler, threadID, otherThreadID, _, _ := newQueryEndpointHandler(t)

	t.Run("missing owner token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/queries/job-1/status?thread_id="+threadID, nil)
		req.SetPathValue("jobId", "job-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("wrong owner token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/queries/job-1/status?thread_id="+threadID, nil)
		req.SetPathValue("jobId", "job-1")
		req.Header.Set("x-query-owner-token", "wrong-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("wrong thread context", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/queries/job-1/status?thread_id="+otherThreadID, nil)
		req.SetPathValue("jobId", "job-1")
		req.Header.Set("x-query-owner-token", "owner-token-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	})

	t.Run("cancel missing owner token", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/queries/job-1/cancel?thread_id="+threadID, nil)
		req.SetPathValue("jobId", "job-1")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("code=%d body=%s", rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	})
}

type fakeQueryJobsRegistry struct {
	mu   sync.Mutex
	jobs map[string]map[string]any
}

func (f *fakeQueryJobsRegistry) Status(_ context.Context, jobID, _ string, _ string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneAnyMap(m), nil
}

func (f *fakeQueryJobsRegistry) Cancel(_ context.Context, jobID, _ string, _ string) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	m["status"] = "cancelled"
	return cloneAnyMap(m), nil
}

func newQueryEndpointHandler(t *testing.T) (http.Handler, string, string, *store.JSONStore, *fakeQueryJobsRegistry) {
	t.Helper()
	cfgDir := t.TempDir()

	cfg, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	thread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	otherThread, err := st.CreateThread(context.Background(), nil)
	if err != nil {
		t.Fatalf("CreateThread(other): %v", err)
	}
	queryJobs := &fakeQueryJobsRegistry{
		jobs: map[string]map[string]any{
			"job-1": {
				"job_id":       "job-1",
				"thread_id":    thread.ID,
				"owner_token":  "owner-token-1",
				"status":       "running",
				"statement_id": "stmt-1",
				"url":          "https://dbc.example.com/sql/history/stmt-1",
			},
		},
	}

	manager := optionc.NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(nil),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		&optionc.Options{QueryJobs: queryJobs, LongHorizon: st},
	)
	handler := New(Deps{
		Store:    st,
		Manager:  manager,
		Projects: ps,
		CfgDir:   cfgDir,
	})
	return handler, thread.ID, otherThread.ID, st, queryJobs
}

func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func toString(v any) string {
	s, _ := v.(string)
	return s
}
