package snowflake

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubmitAsyncUsesDurableRequestSemantics(t *testing.T) {
	t.Parallel()

	type observedRequest struct {
		RequestID string
		Async     string
		Retry     string
		HeaderID  string
		Body      map[string]any
	}
	var mu sync.Mutex
	var observed []observedRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/statements" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		mu.Lock()
		observed = append(observed, observedRequest{
			RequestID: r.URL.Query().Get("requestId"),
			Async:     r.URL.Query().Get("async"),
			Retry:     r.URL.Query().Get("retry"),
			HeaderID:  r.Header.Get("X-Request-ID"),
			Body:      body,
		})
		mu.Unlock()
		w.Header().Set("Location", "/api/v2/statements/stmt-1")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"statementHandle":    "stmt-1",
			"statementStatusUrl": "/api/v2/statements/stmt-1",
		})
	}))
	defer server.Close()

	h := newOAuthHandlerForServer(t, server)
	request := AsyncQueryRequest{
		SQL:                     "SELECT * FROM events",
		RequestID:               "a73b2dd7-cd91-4586-bf7a-214944690f86",
		QueryTag:                "query-job-42",
		AccountIdentifier:       "org-account",
		Warehouse:               "WH",
		Database:                "DB",
		Schema:                  "PUBLIC",
		Role:                    "ANALYST",
		Timeout:                 3*time.Hour + 500*time.Millisecond,
		ClientResultChunkSizeMB: 8,
		RowsPerResultSet:        2500,
	}
	first, err := h.SubmitAsync(context.Background(), request)
	if err != nil {
		t.Fatalf("SubmitAsync: %v", err)
	}
	if first.State != StatementStateRunning || first.Terminal || first.HTTPStatus != http.StatusAccepted {
		t.Fatalf("first snapshot=%+v, want active 202", first)
	}
	request.Retry = true
	second, err := h.SubmitAsync(context.Background(), request)
	if err != nil {
		t.Fatalf("SubmitAsync(replay): %v", err)
	}
	if second.StatementHandle != first.StatementHandle {
		t.Fatalf("replay handle=%q, want %q", second.StatementHandle, first.StatementHandle)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(observed) != 2 {
		t.Fatalf("submit calls=%d, want 2", len(observed))
	}
	for index, got := range observed {
		if got.Async != "true" {
			t.Errorf("call %d async=%q, want true", index, got.Async)
		}
		if got.RequestID != request.RequestID || got.HeaderID != request.RequestID {
			t.Errorf("call %d request ID query/header=%q/%q, want %q", index, got.RequestID, got.HeaderID, request.RequestID)
		}
		if gotTimeout := int64(numberToFloat(got.Body["timeout"])); gotTimeout != 10801 {
			t.Errorf("call %d timeout=%d, want ceil(3h+.5s)=10801", index, gotTimeout)
		}
		if got.Body["statement"] != request.SQL || got.Body["warehouse"] != "WH" || got.Body["database"] != "DB" || got.Body["schema"] != "PUBLIC" || got.Body["role"] != "ANALYST" {
			t.Errorf("call %d body=%+v, missing scoped statement fields", index, got.Body)
		}
		parameters, _ := got.Body["parameters"].(map[string]any)
		if parameters["query_tag"] != "query-job-42" || int64(numberToFloat(parameters["client_result_chunk_size"])) != 8 || int64(numberToFloat(parameters["rows_per_resultset"])) != 2500 {
			t.Errorf("call %d parameters=%+v", index, parameters)
		}
	}
	if observed[0].Retry != "" {
		t.Fatalf("initial retry=%q, want absent", observed[0].Retry)
	}
	if observed[1].Retry != "true" {
		t.Fatalf("replay retry=%q, want true", observed[1].Retry)
	}
}

func TestSubmitAsyncRequiresPersistedRequestID(t *testing.T) {
	t.Parallel()
	h := &Handler{}
	_, err := h.SubmitAsync(context.Background(), AsyncQueryRequest{SQL: "SELECT 1"})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "request id") {
		t.Fatalf("error=%v, want missing request ID", err)
	}
}

func TestRequestIDForIdempotencyKeyIsStableUUID(t *testing.T) {
	t.Parallel()
	first, err := RequestIDForIdempotencyKey("idem_owner-token-value")
	if err != nil {
		t.Fatalf("RequestIDForIdempotencyKey: %v", err)
	}
	second, err := RequestIDForIdempotencyKey("idem_owner-token-value")
	if err != nil {
		t.Fatalf("RequestIDForIdempotencyKey second call: %v", err)
	}
	if first != second || len(first) != 36 || first[8] != '-' || first[13] != '-' || first[18] != '-' || first[23] != '-' {
		t.Fatalf("derived request ID=%q second=%q, want stable UUID", first, second)
	}
	different, err := RequestIDForIdempotencyKey("idem_other")
	if err != nil {
		t.Fatalf("RequestIDForIdempotencyKey different key: %v", err)
	}
	if different == first {
		t.Fatalf("different idempotency keys produced the same request ID %q", first)
	}
}

func TestPollAsyncMapsSnowflakeLifecycleStatuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		body          map[string]any
		wantState     string
		wantTerminal  bool
		wantSucceeded bool
		wantThrottled bool
		wantRetry     time.Duration
	}{
		{
			name: "completed 200", status: http.StatusOK,
			body:      map[string]any{"statementHandle": "done", "status": "SUCCESS", "data": [][]any{{"ok"}}},
			wantState: StatementStateCompleted, wantTerminal: true, wantSucceeded: true,
		},
		{
			name: "active 202", status: http.StatusAccepted,
			body:      map[string]any{"statementHandle": "active", "statementStatusUrl": "/api/v2/statements/active"},
			wantState: StatementStateRunning,
		},
		{
			name: "throttled but active 429", status: http.StatusTooManyRequests,
			body:      map[string]any{"statementHandle": "limited", "statementStatusUrl": "/api/v2/statements/limited", "code": "333334", "message": "query is still running"},
			wantState: StatementStateRunning, wantThrottled: true, wantRetry: 7 * time.Second,
		},
		{
			name: "terminal SQL failure 422", status: http.StatusUnprocessableEntity,
			body:      map[string]any{"statementHandle": "bad", "code": "001003", "sqlState": "42000", "message": "syntax error"},
			wantState: StatementStateFailed, wantTerminal: true,
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/v2/statements/")
		for _, test := range tests {
			handle := strings.TrimSpace(anyString(test.body["statementHandle"]))
			if name != handle {
				continue
			}
			if test.status == http.StatusTooManyRequests {
				w.Header().Set("Retry-After", "7")
			}
			w.WriteHeader(test.status)
			_ = json.NewEncoder(w).Encode(test.body)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handle := strings.TrimSpace(anyString(test.body["statementHandle"]))
			snapshot, err := h.PollAsync(context.Background(), StatementRequest{
				StatementHandle:   handle,
				RequestID:         "rid-status",
				AccountIdentifier: "org-account",
			})
			if err != nil {
				t.Fatalf("PollAsync: %v", err)
			}
			if snapshot.State != test.wantState || snapshot.Terminal != test.wantTerminal || snapshot.Succeeded != test.wantSucceeded || snapshot.Throttled != test.wantThrottled {
				t.Fatalf("snapshot=%+v", snapshot)
			}
			if snapshot.RetryAfter != test.wantRetry {
				t.Fatalf("RetryAfter=%s, want %s", snapshot.RetryAfter, test.wantRetry)
			}
			if test.status == http.StatusUnprocessableEntity && (snapshot.Code != "001003" || snapshot.SQLState != "42000") {
				t.Fatalf("422 details=%+v", snapshot)
			}
		})
	}
}

func TestFetchPartitionPreservesMetadataAndExpiry(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/statements/partitioned" {
			http.NotFound(w, r)
			return
		}
		switch r.URL.Query().Get("partition") {
		case "1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "partitioned",
				"status":          "SUCCESS",
				"resultSetMetaData": map[string]any{
					"numRows": 12,
					"format":  "jsonv2",
					"rowType": []map[string]any{{"name": "ID", "type": "fixed", "precision": 38, "scale": 0}},
					"partitionInfo": []map[string]any{
						{"rowCount": 4, "compressedSize": 100, "uncompressedSize": 400},
						{"rowCount": 8, "compressedSize": 200, "uncompressedSize": 800},
					},
				},
				"data": [][]any{{"5"}, {"6"}},
			})
		case "2":
			w.WriteHeader(http.StatusGone)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "333333", "message": "result expired"})
		default:
			http.Error(w, "bad partition", http.StatusBadRequest)
		}
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	partition, err := h.FetchPartition(context.Background(), PartitionRequest{
		StatementRequest: StatementRequest{StatementHandle: "partitioned", RequestID: "rid-part", AccountIdentifier: "org-account"},
		Partition:        1,
	})
	if err != nil {
		t.Fatalf("FetchPartition: %v", err)
	}
	if partition.Partition != 1 || len(partition.Rows) != 2 || len(partition.Columns) != 1 {
		t.Fatalf("partition=%+v", partition)
	}
	if partition.ResultMetadata.NumRows != 12 || len(partition.ResultMetadata.PartitionInfo) != 2 || partition.ResultMetadata.PartitionInfo[1].UncompressedSize != 800 {
		t.Fatalf("metadata=%+v", partition.ResultMetadata)
	}

	_, err = h.FetchPartition(context.Background(), PartitionRequest{
		StatementRequest: StatementRequest{StatementHandle: "partitioned", RequestID: "rid-part", AccountIdentifier: "org-account"},
		Partition:        2,
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusGone || apiErr.Message != "result expired" {
		t.Fatalf("expiry error=%#v", err)
	}
}

func TestCancelAsyncSeparatesAcceptanceFromConfirmation(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.RequestURI())
		mu.Unlock()
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/cancel") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.URL.Path, "confirmed") {
			_ = json.NewEncoder(w).Encode(map[string]any{"statementHandle": "confirmed", "status": "CANCELLED", "message": "cancelled"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "000604", "message": "cancel request accepted"})
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	accepted, err := h.CancelAsync(context.Background(), StatementRequest{StatementHandle: "running", RequestID: "rid-cancel", AccountIdentifier: "org-account"})
	if err != nil {
		t.Fatalf("CancelAsync accepted: %v", err)
	}
	if !accepted.Accepted || accepted.Confirmed {
		t.Fatalf("accepted outcome=%+v, want accepted but unconfirmed", accepted)
	}
	confirmed, err := h.CancelAsync(context.Background(), StatementRequest{StatementHandle: "confirmed", RequestID: "rid-cancel", AccountIdentifier: "org-account"})
	if err != nil {
		t.Fatalf("CancelAsync confirmed: %v", err)
	}
	if !confirmed.Accepted || !confirmed.Confirmed {
		t.Fatalf("confirmed outcome=%+v", confirmed)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, path := range paths {
		if !strings.Contains(path, "/api/v2/statements/") || !strings.Contains(path, "/cancel?requestId=rid-cancel") {
			t.Fatalf("unexpected cancellation request %q", path)
		}
	}
}

func TestExplainQueryParsesJSONProfile(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var submittedSQL string
	polls := 0
	var cancels atomic.Int32
	var observations []string
	plan := `[{"GlobalStats":{"partitionsTotal":120,"partitionsAssigned":"9","bytesAssigned":987654},"Operations":[{"id":0,"operation":"TableScan"}]}]`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			submittedSQL = strings.TrimSpace(anyString(body["statement"]))
			mu.Unlock()
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"statementHandle": "explain-1", "statementStatusUrl": "/api/v2/statements/explain-1"})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/explain-1":
			mu.Lock()
			polls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "explain-1",
				"status":          "SUCCESS",
				"resultSetMetaData": map[string]any{
					"rowType": []map[string]any{{"name": "content", "type": "text"}},
				},
				"data": [][]any{{plan}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements/explain-1/cancel":
			cancels.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	profile, err := h.ExplainQuery(context.Background(), ExplainRequest{
		SQL:               "SELECT * FROM events",
		RequestID:         "rid-explain",
		QueryTag:          "job-explain",
		AccountIdentifier: "org-account",
		PollInterval:      time.Millisecond,
		OnObservation: func(observation ExplainObservation) error {
			observations = append(observations, observation.Phase+":"+observation.Statement.State)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ExplainQuery: %v", err)
	}
	if !profile.ReadOnly || !profile.ExplainAttempted || !profile.ExplainSucceeded || profile.Quality != "optimizer_estimate" {
		t.Fatalf("profile flags=%+v", profile)
	}
	if profile.PartitionsTotal != 120 || profile.PartitionsAssigned != 9 || profile.BytesAssigned != 987654 || len(profile.Operations) == 0 || len(profile.Raw) == 0 {
		t.Fatalf("profile estimates=%+v", profile)
	}
	if got := strings.Join(observations, ","); got != "explain_json:running,explain_json:completed" {
		t.Fatalf("observations=%q", got)
	}
	if cancels.Load() != 0 {
		t.Fatalf("normal terminal profile issued %d cancellations", cancels.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if submittedSQL != "EXPLAIN USING JSON SELECT * FROM events" || polls != 1 {
		t.Fatalf("submittedSQL=%q polls=%d", submittedSQL, polls)
	}
}

func TestExplainQueryHonorsRetryAfterBetweenNativePolls(t *testing.T) {
	plan := `[{"GlobalStats":{"partitionsTotal":1,"partitionsAssigned":1,"bytesAssigned":10},"Operations":[]}]`
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "explain-retry-after", "statementStatusUrl": "/api/v2/statements/explain-retry-after",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/explain-retry-after":
			polls++
			if polls == 1 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"statementHandle": "explain-retry-after", "statementStatusUrl": "/api/v2/statements/explain-retry-after",
				})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle": "explain-retry-after",
				"status":          "SUCCESS",
				"resultSetMetaData": map[string]any{
					"rowType": []map[string]any{{"name": "content", "type": "text"}},
				},
				"data": [][]any{{plan}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)
	var waits []time.Duration
	h.sleepFn = func(ctx context.Context, wait time.Duration) error {
		waits = append(waits, wait)
		return nil
	}

	profile, err := h.ExplainQuery(context.Background(), ExplainRequest{
		SQL: "SELECT 1", RequestID: "rid-retry-after", AccountIdentifier: "org-account", PollInterval: time.Millisecond,
	})
	if err != nil || !profile.ExplainSucceeded {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if len(waits) != 2 || waits[0] != time.Millisecond || waits[1] != 7*time.Second {
		t.Fatalf("native poll waits=%v, want [1ms 7s]", waits)
	}
}

func TestExplainQueryObservationFailureCancelsActiveStatement(t *testing.T) {
	sentinel := errors.New("durable observation rejected")
	var polls atomic.Int32
	var cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle":    "explain-observer-failed",
				"statementStatusUrl": "/api/v2/statements/explain-observer-failed",
			})
		case r.Method == http.MethodGet:
			polls.Add(1)
			http.NotFound(w, r)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements/explain-observer-failed/cancel":
			cancels.Add(1)
			w.WriteHeader(http.StatusOK)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	_, err := h.ExplainQuery(context.Background(), ExplainRequest{
		SQL:               "SELECT 1",
		RequestID:         "rid-observer-failed",
		AccountIdentifier: "org-account",
		OnObservation: func(observation ExplainObservation) error {
			if observation.Phase != "explain_json" || observation.Statement.StatementHandle != "explain-observer-failed" {
				t.Fatalf("observation=%+v", observation)
			}
			return sentinel
		},
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ExplainQuery error=%v, want observer error", err)
	}
	if polls.Load() != 0 || cancels.Load() != 1 {
		t.Fatalf("polls/cancels=%d/%d, want 0/1", polls.Load(), cancels.Load())
	}
}

func TestExplainQueryCancellationUsesFreshBoundedCleanupContext(t *testing.T) {
	var cancels atomic.Int32
	pollStarted := make(chan struct{})
	cancelStarted := make(chan struct{})
	cancelDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements":
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"statementHandle":    "explain-timeout",
				"statementStatusUrl": "/api/v2/statements/explain-timeout",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v2/statements/explain-timeout":
			close(pollStarted)
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements/explain-timeout/cancel":
			cancels.Add(1)
			close(cancelStarted)
			<-r.Context().Done()
			close(cancelDone)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := h.ExplainQuery(ctx, ExplainRequest{
			SQL:               "SELECT 1",
			RequestID:         "rid-explain-timeout",
			AccountIdentifier: "org-account",
			PollInterval:      time.Millisecond,
			CleanupTimeout:    200 * time.Millisecond,
		})
		done <- err
	}()
	select {
	case <-pollStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("profile poll did not start")
	}
	started := time.Now()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("ExplainQuery error=%v, want context cancellation", err)
	}
	select {
	case <-cancelStarted:
	default:
		t.Fatal("cleanup cancellation was not attempted with a fresh context")
	}
	select {
	case <-cancelDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cleanup request did not stop at its bounded deadline")
	}
	if cancels.Load() != 1 || time.Since(started) > 2*time.Second {
		t.Fatalf("cancels=%d elapsed=%s", cancels.Load(), time.Since(started))
	}
}

func TestLegacyWaitExpiryDoesNotCancelProviderStatement(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	cancelCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			mu.Lock()
			cancelCalls++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "CANCELLED"})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v2/statements" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if got := int64(numberToFloat(body["timeout"])); got != int64(DefaultAsyncStatementTimeout/time.Second) {
				t.Errorf("legacy provider timeout=%d, want %d", got, int64(DefaultAsyncStatementTimeout/time.Second))
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"statementHandle": "long-running", "statementStatusUrl": "/api/v2/statements/long-running"})
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	h := newOAuthHandlerForServer(t, server)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	cfg, err := h.authConfig(authRequestOverrides{AccountIdentifier: "org-account"})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	_, err = h.executeSQLLifecycle(ctx, cfg, "SELECT COUNT(*) FROM huge_table", 10)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeSQLLifecycle error=%v, want deadline exceeded", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if cancelCalls != 0 {
		t.Fatalf("provider cancellation calls=%d, want 0", cancelCalls)
	}
}

func TestPollAsyncRejectsForeignStatusURL(t *testing.T) {
	t.Parallel()
	h, _, sec := newAuthTestHandler(t)
	if err := sec.SetSnowflakeOAuthToken("tok"); err != nil {
		t.Fatalf("SetSnowflakeOAuthToken: %v", err)
	}
	_, err := h.PollAsync(context.Background(), StatementRequest{
		StatementHandle:    "stmt",
		StatementStatusURL: "https://attacker.example/api/v2/statements/stmt",
		RequestID:          "rid",
		AccountIdentifier:  "org-account",
	})
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "does not match") {
		t.Fatalf("error=%v, want foreign-host rejection", err)
	}
}
