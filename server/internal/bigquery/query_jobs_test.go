package bigquery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type bqRewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t bqRewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func newBigQueryLifecycleTestHandler(t *testing.T, fn http.HandlerFunc) (*Handler, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(fn)
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	handler := NewHandler(nil, nil)
	handler.httpc = &http.Client{Transport: bqRewriteTransport{target: target, base: http.DefaultTransport}}
	handler.cachedToken = "test-token"
	handler.cachedUntil = time.Now().Add(time.Hour)
	handler.cachedKey = handler.tokenCacheKey()
	return handler, server
}

func TestPreflightQueryReturnsEstimateSchemaAndLocation(t *testing.T) {
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/bigquery/v2/projects/project-a/queries" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("authorization = %q", got)
		}
		var request bqQueryRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if !request.DryRun || request.UseLegacySQL || request.Query != "SELECT 1" || request.Location != "EU" {
			t.Fatalf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"totalBytesProcessed": "123456",
			"jobReference":        map[string]any{"projectId": "project-a", "location": "EU"},
			"schema":              map[string]any{"fields": []map[string]any{{"name": "answer", "type": "INTEGER"}}},
		})
	})

	profile, err := handler.PreflightQuery(context.Background(), QueryPreflightRequest{
		ProjectID: "project-a",
		Location:  "EU",
		SQL:       "SELECT 1",
	})
	if err != nil {
		t.Fatalf("PreflightQuery: %v", err)
	}
	if profile.EstimatedBytesProcessed != 123456 || profile.EstimatedBytesText != "123456" || profile.Location != "EU" {
		t.Fatalf("profile = %#v", profile)
	}
	if len(profile.Schema) != 1 || profile.Schema[0]["name"] != "answer" {
		t.Fatalf("schema = %#v", profile.Schema)
	}
}

func TestInsertQueryJobCarriesDeterministicIdentityLabelsAndBudgets(t *testing.T) {
	useCache := false
	handle := QueryJobHandle{ProjectID: "project-a", JobID: DeterministicQueryJobID("app-job-7", 2), Location: "EU"}
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/bigquery/v2/projects/project-a/jobs" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		var request bqJob
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.JobReference.JobID != handle.JobID || request.JobReference.Location != "EU" {
			t.Fatalf("job reference = %#v", request.JobReference)
		}
		if request.Configuration.Query.Query != "SELECT count(*) FROM dataset.table" {
			t.Fatalf("query = %q", request.Configuration.Query.Query)
		}
		if request.Configuration.Query.MaximumBytesBilled != "107374182400" {
			t.Fatalf("maximumBytesBilled = %q", request.Configuration.Query.MaximumBytesBilled)
		}
		if request.Configuration.JobTimeoutMs != "21600000" {
			t.Fatalf("jobTimeoutMs = %q", request.Configuration.JobTimeoutMs)
		}
		if request.Configuration.Query.UseQueryCache == nil || *request.Configuration.Query.UseQueryCache {
			t.Fatalf("useQueryCache = %#v", request.Configuration.Query.UseQueryCache)
		}
		if request.Configuration.Labels["app_job"] != "app-job-7" {
			t.Fatalf("labels = %#v", request.Configuration.Labels)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jobReference":  map[string]any{"projectId": "project-a", "jobId": handle.JobID, "location": "EU"},
			"configuration": request.Configuration,
			"status":        map[string]any{"state": "PENDING"},
		})
	})

	snapshot, err := handler.InsertQueryJob(context.Background(), InsertQueryJobRequest{
		Handle:             handle,
		SQL:                "SELECT count(*) FROM dataset.table",
		MaximumBytesBilled: 100 << 30,
		JobTimeout:         6 * time.Hour,
		Priority:           "INTERACTIVE",
		UseQueryCache:      &useCache,
		Labels:             map[string]string{"app_job": "app-job-7"},
	})
	if err != nil {
		t.Fatalf("InsertQueryJob: %v", err)
	}
	if snapshot.Handle != handle || snapshot.State != "PENDING" || snapshot.Terminal {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.SQLSHA256 != QuerySQLSHA256("SELECT count(*) FROM dataset.table") {
		t.Fatalf("SQLSHA256 = %q", snapshot.SQLSHA256)
	}
	if DeterministicQueryJobID("app-job-7", 2) != handle.JobID || DeterministicQueryJobID("app-job-7", 3) == handle.JobID {
		t.Fatal("deterministic job ID is not stable per attempt")
	}
}

func TestInsertQueryJobDoesNotRetryAmbiguousPOST(t *testing.T) {
	var calls atomic.Int32
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "11")
		http.Error(w, `{"error":{"message":"backend unavailable"}}`, http.StatusServiceUnavailable)
	})
	_, err := handler.InsertQueryJob(context.Background(), InsertQueryJobRequest{
		Handle: QueryJobHandle{ProjectID: "p", JobID: "stable-id", Location: "US"},
		SQL:    "SELECT 1",
	})
	if err == nil {
		t.Fatal("InsertQueryJob returned nil error")
	}
	if calls.Load() != 1 {
		t.Fatalf("POST calls = %d, want 1", calls.Load())
	}
	var apiErr *QueryAPIError
	if !errors.As(err, &apiErr) || !apiErr.Ambiguous || apiErr.StatusCode != http.StatusServiceUnavailable || apiErr.RetryAfter() != 11*time.Second {
		t.Fatalf("error = %#v", err)
	}
}

func TestInsertQueryJobConflictIsDefinitiveAndInspectable(t *testing.T) {
	var calls atomic.Int32
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":{"message":"Already Exists"}}`, http.StatusConflict)
	})
	_, err := handler.InsertQueryJob(context.Background(), InsertQueryJobRequest{
		Handle: QueryJobHandle{ProjectID: "p", JobID: "stable-id"},
		SQL:    "SELECT 1",
	})
	if QueryHTTPStatus(err) != http.StatusConflict || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d err=%v", QueryHTTPStatus(err), calls.Load(), err)
	}
	var apiErr *QueryAPIError
	if !errors.As(err, &apiErr) || apiErr.Ambiguous {
		t.Fatalf("conflict should be definitive: %#v", err)
	}
}

func TestGetQueryJobUsesErrorResultNotWarningsAndParsesTelemetry(t *testing.T) {
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/bigquery/v2/projects/p/jobs/j" || r.URL.Query().Get("location") != "EU" {
			t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"jobReference": map[string]any{"projectId": "p", "jobId": "j", "location": "EU"},
			"configuration": map[string]any{
				"labels": map[string]any{"app_job": "a"},
				"query":  map[string]any{"query": "SELECT 1", "useLegacySql": false},
			},
			"status": map[string]any{
				"state":  "DONE",
				"errors": []map[string]any{{"reason": "resourcesExceeded", "message": "nonfatal warning"}},
			},
			"statistics": map[string]any{
				"creationTime": "10", "startTime": "20", "endTime": "30", "reservation_id": "res-a",
				"query": map[string]any{
					"totalBytesProcessed": "1000", "totalBytesBilled": "2048", "totalSlotMs": "77", "cacheHit": true,
					"statementType": "SELECT", "totalPartitionsProcessed": "4",
					"timeline":         []map[string]any{{"elapsedMs": "5", "completedUnits": "3", "pendingUnits": "2", "activeUnits": "1", "totalSlotMs": "7"}},
					"queryPlan":        []map[string]any{{"id": "1", "name": "S1", "status": "COMPLETE", "recordsRead": "8", "recordsWritten": "2", "shuffleOutputBytesSpilled": "9", "slotMs": "11"}},
					"referencedTables": []map[string]any{{"projectId": "data-p", "datasetId": "analytics", "tableId": "events"}},
					"schema":           map[string]any{"fields": []map[string]any{{"name": "x", "type": "INTEGER"}}},
				},
			},
		})
	})

	snapshot, err := handler.GetQueryJob(context.Background(), QueryJobHandle{ProjectID: "p", JobID: "j", Location: "EU"})
	if err != nil {
		t.Fatalf("GetQueryJob: %v", err)
	}
	if !snapshot.Terminal || !snapshot.Succeeded || snapshot.Error != nil || len(snapshot.Warnings) != 1 {
		t.Fatalf("snapshot status = %#v", snapshot)
	}
	stats := snapshot.Statistics
	if stats.TotalBytesProcessed != 1000 || stats.TotalBytesBilled != 2048 || stats.TotalSlotMs != 77 || !stats.CacheHit {
		t.Fatalf("statistics = %#v", stats)
	}
	if len(stats.Timeline) != 1 || stats.Timeline[0].CompletedUnits != 3 || len(stats.QueryPlan) != 1 || stats.QueryPlan[0].ShuffleBytesSpilled != 9 {
		t.Fatalf("telemetry = %#v", stats)
	}
	if len(stats.ReferencedTables) != 1 || stats.ReferencedTables[0].TableID != "events" {
		t.Fatalf("referenced tables = %#v", stats.ReferencedTables)
	}
	if snapshot.SQLSHA256 != QuerySQLSHA256("SELECT 1") || snapshot.Labels["app_job"] != "a" {
		t.Fatalf("identity = hash:%q labels:%#v", snapshot.SQLSHA256, snapshot.Labels)
	}
	if !snapshot.MatchesSubmission(QuerySQLSHA256("SELECT 1"), map[string]string{"app_job": "a"}) || snapshot.MatchesSubmission(QuerySQLSHA256("SELECT 2"), nil) {
		t.Fatalf("submission identity matching is incorrect: %#v", snapshot)
	}
}

func TestGetQueryJobMapsErrorResultStates(t *testing.T) {
	tests := []struct {
		name      string
		state     string
		errorBody any
		terminal  bool
		succeeded bool
		cancelled bool
	}{
		{name: "pending", state: "PENDING"},
		{name: "running", state: "RUNNING"},
		{name: "success", state: "DONE", terminal: true, succeeded: true},
		{name: "failed", state: "DONE", errorBody: map[string]any{"reason": "invalidQuery", "message": "bad sql"}, terminal: true},
		{name: "cancelled", state: "DONE", errorBody: map[string]any{"reason": "stopped", "message": "stopped"}, terminal: true, cancelled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
				status := map[string]any{"state": test.state}
				if test.errorBody != nil {
					status["errorResult"] = test.errorBody
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"status": status})
			})
			snapshot, err := handler.GetQueryJob(context.Background(), QueryJobHandle{ProjectID: "p", JobID: "j"})
			if err != nil {
				t.Fatalf("GetQueryJob: %v", err)
			}
			if snapshot.Terminal != test.terminal || snapshot.Succeeded != test.succeeded || snapshot.Cancelled != test.cancelled {
				t.Fatalf("snapshot = %#v", snapshot)
			}
		})
	}
}

func TestGetQueryResultPageUsesNativePageTokensWithoutResubmit(t *testing.T) {
	var methods []string
	var tokens []string
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		token := r.URL.Query().Get("pageToken")
		tokens = append(tokens, token)
		if r.URL.Path != "/bigquery/v2/projects/p/queries/j" || r.URL.Query().Get("location") != "EU" || r.URL.Query().Get("maxResults") != "2" {
			t.Fatalf("request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		rows := []map[string]any{{"f": []map[string]any{{"v": "1"}}}}
		response := map[string]any{
			"jobComplete": true,
			"totalRows":   "2",
			"schema":      map[string]any{"fields": []map[string]any{{"name": "x", "type": "INTEGER"}}},
			"rows":        rows,
		}
		if token == "" {
			response["pageToken"] = "native-next"
		} else if token != "native-next" {
			t.Fatalf("unexpected page token %q", token)
		}
		_ = json.NewEncoder(w).Encode(response)
	})
	handle := QueryJobHandle{ProjectID: "p", JobID: "j", Location: "EU"}
	first, err := handler.GetQueryResultPage(context.Background(), handle, "", 2)
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	second, err := handler.GetQueryResultPage(context.Background(), handle, first.NextPageToken, 2)
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if strings.Join(methods, ",") != "GET,GET" || strings.Join(tokens, ",") != ",native-next" {
		t.Fatalf("methods=%v tokens=%v", methods, tokens)
	}
	if len(first.Rows) != 1 || first.Rows[0]["x"] != int64(1) || len(second.Rows) != 1 {
		t.Fatalf("pages = %#v %#v", first, second)
	}
}

func TestRequestCancelQueryJobReturnsReceiptWithoutClaimingConfirmation(t *testing.T) {
	handler, _ := newBigQueryLifecycleTestHandler(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/bigquery/v2/projects/p/jobs/j/cancel" || r.URL.Query().Get("location") != "EU" {
			t.Fatalf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"job": map[string]any{
				"jobReference": map[string]any{"projectId": "p", "jobId": "j", "location": "EU"},
				"status":       map[string]any{"state": "RUNNING"},
			},
		})
	})
	receipt, err := handler.RequestCancelQueryJob(context.Background(), QueryJobHandle{ProjectID: "p", JobID: "j", Location: "EU"})
	if err != nil {
		t.Fatalf("RequestCancelQueryJob: %v", err)
	}
	if !receipt.Accepted || receipt.Job == nil || receipt.Job.Terminal || receipt.Job.State != "RUNNING" {
		t.Fatalf("receipt = %#v", receipt)
	}
}
