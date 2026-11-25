package queryadapters

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/snowflake"
	"ai-analyst-local/internal/trino"
)

type recordingProfileObserver struct {
	events    []string
	handles   []jobs.NativeHandle
	handleErr error
	phaseErr  error
}

func (o *recordingProfileObserver) PublishProfileHandle(handle jobs.NativeHandle) error {
	o.handles = append(o.handles, handle)
	o.events = append(o.events, "handle:"+handle.ID+":"+handle.Extra["next_uri"])
	return o.handleErr
}

func (o *recordingProfileObserver) PublishProfilePhase(phase string) error {
	o.events = append(o.events, "phase:"+phase)
	return o.phaseErr
}

var (
	_ jobs.ObservableQueryProfiler = (*Databricks)(nil)
	_ jobs.ProfileCanceller        = (*Databricks)(nil)
	_ jobs.ObservableQueryProfiler = (*Snowflake)(nil)
	_ jobs.ProfileCanceller        = (*Snowflake)(nil)
	_ jobs.ObservableQueryProfiler = (*Trino)(nil)
	_ jobs.ProfileCanceller        = (*Trino)(nil)
)

type fakeBigQueryClient struct {
	preflight    bigquery.QueryPreflight
	insertReq    bigquery.InsertQueryJobRequest
	insert       bigquery.QueryJobSnapshot
	get          bigquery.QueryJobSnapshot
	getErr       error
	pages        map[string]bigquery.QueryResultPage
	fetchTokens  []string
	fetchMaxRows []int64
}

func (f *fakeBigQueryClient) PreflightQuery(context.Context, bigquery.QueryPreflightRequest) (bigquery.QueryPreflight, error) {
	return f.preflight, nil
}
func (f *fakeBigQueryClient) InsertQueryJob(_ context.Context, req bigquery.InsertQueryJobRequest) (bigquery.QueryJobSnapshot, error) {
	f.insertReq = req
	f.insert.Handle = req.Handle
	return f.insert, nil
}
func (f *fakeBigQueryClient) GetQueryJob(context.Context, bigquery.QueryJobHandle) (bigquery.QueryJobSnapshot, error) {
	return f.get, f.getErr
}
func (f *fakeBigQueryClient) GetQueryResultPage(_ context.Context, _ bigquery.QueryJobHandle, token string, maxRows int64) (bigquery.QueryResultPage, error) {
	f.fetchTokens = append(f.fetchTokens, token)
	f.fetchMaxRows = append(f.fetchMaxRows, maxRows)
	return f.pages[token], nil
}
func (f *fakeBigQueryClient) RequestCancelQueryJob(_ context.Context, handle bigquery.QueryJobHandle) (bigquery.QueryCancelReceipt, error) {
	return bigquery.QueryCancelReceipt{Handle: handle, Accepted: true}, nil
}

func TestBigQueryEstimatedProfileBudgetAndNativePagination(t *testing.T) {
	client := &fakeBigQueryClient{
		preflight: bigquery.QueryPreflight{ProjectID: "p", EstimatedBytesProcessed: 1234},
		insert:    bigquery.QueryJobSnapshot{State: "PENDING"},
		pages: map[string]bigquery.QueryResultPage{
			"":         {JobComplete: true, Rows: []map[string]any{{"n": 1}}, NextPageToken: "native-2"},
			"native-2": {JobComplete: true, Rows: []map[string]any{{"n": 2}}},
		},
	}
	adapter := newBigQueryForClient(client)
	profile, err := adapter.Profile(context.Background(), jobs.SubmitRequest{SQL: "select 1", ConnectionID: "p"})
	if err != nil || profile.EstimatedBytes == nil || *profile.EstimatedBytes != 1234 || profile.EstimateIsExact {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	submitted, err := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "p", IdempotencyKey: "idem",
		Budgets: jobs.Budgets{MaxScanBytes: 1000, HardExecution: 73 * time.Second},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if client.insertReq.MaximumBytesBilled != 1000 {
		t.Fatalf("maximumBytesBilled=%d want 1000", client.insertReq.MaximumBytesBilled)
	}
	if client.insertReq.JobTimeout != 73*time.Second {
		t.Fatalf("job timeout=%s want exactly 73s", client.insertReq.JobTimeout)
	}
	first, err := adapter.Fetch(context.Background(), submitted.Handle, jobs.FetchCursor{MaxRows: 1})
	if err != nil || first.NextPageToken == "" || len(first.Rows) != 1 {
		t.Fatalf("first page=%+v err=%v", first, err)
	}
	second, err := adapter.Fetch(context.Background(), submitted.Handle, jobs.FetchCursor{PageToken: first.NextPageToken, MaxRows: 1})
	if err != nil || len(second.Rows) != 1 || second.Rows[0]["n"] != 2 {
		t.Fatalf("second page=%+v err=%v", second, err)
	}
	if got := strings.Join(client.fetchTokens, ","); got != ",native-2" {
		t.Fatalf("provider page tokens=%q want empty,native-2", got)
	}
	for _, maxRows := range client.fetchMaxRows {
		if maxRows != bigQueryNativeResultPageRows {
			t.Fatalf("provider maxResults=%d want %d", maxRows, bigQueryNativeResultPageRows)
		}
	}
}

func TestBigQueryBytesBilledLimitErrorLabelsCapAndRequiredBytes(t *testing.T) {
	status := bigQueryStatus(bigquery.QueryJobSnapshot{
		State:    "DONE",
		Terminal: true,
		Error: &bigquery.BQError{
			Message: "Query exceeded limit for bytes billed: 213534213421. 213634712423 or higher required.",
			Reason:  "billingTierLimitExceeded",
		},
	})
	if status.State != jobs.ProviderStateFailed {
		t.Fatalf("state=%q want failed", status.State)
	}
	if !strings.Contains(status.Error, "authorized maximumBytesBilled cap was 213534213421 bytes") ||
		!strings.Contains(status.Error, "requires at least 213634712423 bytes") ||
		!strings.Contains(status.Error, "No rows were materialized") {
		t.Fatalf("error=%q", status.Error)
	}
	if status.Telemetry["maximum_bytes_billed"] != int64(213_534_213_421) ||
		status.Telemetry["minimum_bytes_required"] != int64(213_634_712_423) ||
		status.Telemetry["bytes_billed_limit_exceeded"] != true {
		t.Fatalf("telemetry=%#v", status.Telemetry)
	}
}

func TestBigQueryCompositeCursorResumesAfterByteClipWithoutSkippingRows(t *testing.T) {
	firstNativePage := []map[string]any{
		{"n": "first"},
		{"n": "second"},
		{"n": "third"},
	}
	client := &fakeBigQueryClient{pages: map[string]bigquery.QueryResultPage{
		"":         {JobComplete: true, Rows: firstNativePage, NextPageToken: "native-2"},
		"native-2": {JobComplete: true, Rows: []map[string]any{{"n": "fourth"}}},
	}}
	adapter := newBigQueryForClient(client)
	handle := jobs.NativeHandle{Provider: "bigquery", ID: "job", Extra: map[string]string{"project_id": "p"}}

	firstRowBytes := int64(len(`{"n":"first"}`))
	first, err := adapter.Fetch(context.Background(), handle, jobs.FetchCursor{MaxRows: 100, MaxBytes: firstRowBytes})
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(first.Rows) != 1 || first.Rows[0]["n"] != "first" || first.NextPageToken == "" {
		t.Fatalf("first page=%+v", first)
	}
	providerToken, offset, err := parseBigQueryResultCursor(first.NextPageToken)
	if err != nil || providerToken != "" || offset != 1 {
		t.Fatalf("first continuation provider=%q offset=%d err=%v", providerToken, offset, err)
	}

	second, err := adapter.Fetch(context.Background(), handle, jobs.FetchCursor{PageToken: first.NextPageToken, MaxRows: 100, MaxBytes: 1 << 20})
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(second.Rows) != 2 || second.Rows[0]["n"] != "second" || second.Rows[1]["n"] != "third" {
		t.Fatalf("second page=%+v", second)
	}
	providerToken, offset, err = parseBigQueryResultCursor(second.NextPageToken)
	if err != nil || providerToken != "native-2" || offset != 0 {
		t.Fatalf("second continuation provider=%q offset=%d err=%v", providerToken, offset, err)
	}

	third, err := adapter.Fetch(context.Background(), handle, jobs.FetchCursor{PageToken: second.NextPageToken, MaxRows: 100, MaxBytes: 1 << 20})
	if err != nil || len(third.Rows) != 1 || third.Rows[0]["n"] != "fourth" || third.NextPageToken != "" {
		t.Fatalf("third page=%+v err=%v", third, err)
	}
	if got := strings.Join(client.fetchTokens, ","); got != ",,native-2" {
		t.Fatalf("provider page tokens=%q want empty,empty,native-2", got)
	}
	for _, maxRows := range client.fetchMaxRows {
		if maxRows != bigQueryNativeResultPageRows {
			t.Fatalf("provider maxResults=%d want %d", maxRows, bigQueryNativeResultPageRows)
		}
	}
}

func TestBigQueryFetchAcceptsLegacyRawProviderPageToken(t *testing.T) {
	client := &fakeBigQueryClient{pages: map[string]bigquery.QueryResultPage{
		"legacy-native": {JobComplete: true, Rows: []map[string]any{{"n": 7}}},
	}}
	page, err := newBigQueryForClient(client).Fetch(context.Background(), jobs.NativeHandle{
		Provider: "bigquery", ID: "job", Extra: map[string]string{"project_id": "p"},
	}, jobs.FetchCursor{PageToken: "legacy-native", MaxRows: 10})
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["n"] != 7 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if got := strings.Join(client.fetchTokens, ","); got != "legacy-native" {
		t.Fatalf("provider page token=%q", got)
	}
}

type fakeDatabricksClient struct {
	candidates          []databricks.StatementCandidate
	snapshot            databricks.StatementSnapshot
	statementErr        error
	statementCalls      int
	chunks              map[int32]databricks.ResultChunk
	chunkErrs           map[int32]error
	chunkCalls          []int32
	submitReq           databricks.StatementSubmitRequest
	submitErr           error
	explain             databricks.ExplainQueryResult
	explainErr          error
	explainReq          databricks.ExplainQueryRequest
	explainObservations []databricks.ExplainObservation
	cancelled           []databricks.StatementHandle
	statementProfileErr error
}

func (f *fakeDatabricksClient) SubmitStatement(_ context.Context, req databricks.StatementSubmitRequest) (databricks.StatementSnapshot, error) {
	f.submitReq = req
	return f.snapshot, f.submitErr
}
func (f *fakeDatabricksClient) GetStatement(context.Context, databricks.StatementHandle) (databricks.StatementSnapshot, error) {
	f.statementCalls++
	return f.snapshot, f.statementErr
}
func (f *fakeDatabricksClient) GetStatementResultChunk(_ context.Context, _ databricks.StatementHandle, chunkIndex int32) (databricks.ResultChunk, error) {
	f.chunkCalls = append(f.chunkCalls, chunkIndex)
	if err := f.chunkErrs[chunkIndex]; err != nil {
		return databricks.ResultChunk{}, err
	}
	return f.chunks[chunkIndex], nil
}
func (f *fakeDatabricksClient) RequestCancelStatement(_ context.Context, handle databricks.StatementHandle) (databricks.StatementCancelReceipt, error) {
	f.cancelled = append(f.cancelled, handle)
	return databricks.StatementCancelReceipt{Handle: handle, Accepted: true}, nil
}
func (f *fakeDatabricksClient) ExplainQuery(_ context.Context, req databricks.ExplainQueryRequest) (databricks.ExplainQueryResult, error) {
	f.explainReq = req
	for _, observation := range f.explainObservations {
		if req.OnObservation != nil {
			if err := req.OnObservation(observation); err != nil {
				return f.explain, err
			}
		}
	}
	return f.explain, f.explainErr
}
func (f *fakeDatabricksClient) GetStatementProfile(context.Context, databricks.StatementHandle) (databricks.StatementProfile, error) {
	return databricks.StatementProfile{Statement: f.snapshot}, f.statementProfileErr
}
func (f *fakeDatabricksClient) FindStatementCandidates(context.Context, databricks.StatementReconcileRequest) ([]databricks.StatementCandidate, error) {
	return append([]databricks.StatementCandidate(nil), f.candidates...), nil
}
func (*fakeDatabricksClient) MaterializeStatementResult(context.Context, databricks.MaterializeStatementRequest) (databricks.MaterializedStatementResult, error) {
	return databricks.MaterializedStatementResult{}, nil
}

func TestDatabricksAmbiguousSubmissionRequiresSoleCandidate(t *testing.T) {
	client := &fakeDatabricksClient{
		candidates: []databricks.StatementCandidate{{StatementID: "a"}, {StatementID: "b"}},
		snapshot: databricks.StatementSnapshot{
			Handle: databricks.StatementHandle{Host: "https://dbx", WarehouseID: "w", StatementID: "a"}, State: "RUNNING",
		},
	}
	adapter := newDatabricksForClient(client)
	out, err := adapter.ReconcileSubmission(context.Background(), jobs.ReconcileRequest{
		IdempotencyKey: "idem", SQL: "select 1", ConnectionID: "https://dbx", SubmittedAt: time.Now(),
		Metadata: map[string]any{"host": "https://dbx", "warehouse_id": "w"},
	})
	if err != nil || out.Resolution != jobs.ReconcileUnknown {
		t.Fatalf("multiple candidates outcome=%+v err=%v", out, err)
	}
	client.candidates = client.candidates[:1]
	out, err = adapter.ReconcileSubmission(context.Background(), jobs.ReconcileRequest{
		IdempotencyKey: "idem", SQL: "select 1", ConnectionID: "https://dbx", SubmittedAt: time.Now(),
		Metadata: map[string]any{"host": "https://dbx", "warehouse_id": "w"},
	})
	if err != nil || out.Resolution != jobs.ReconcileFound || out.Handle.ID != "a" {
		t.Fatalf("sole candidate outcome=%+v err=%v", out, err)
	}
}

func TestDatabricksHardExecutionIsHonestlyMonitorEnforced(t *testing.T) {
	snapshot := databricks.StatementSnapshot{
		Handle: databricks.StatementHandle{Host: "https://dbx", WarehouseID: "w", StatementID: "stmt-1"},
		State:  "SUCCEEDED", Terminal: true, Succeeded: true, ResultAvailable: true,
	}
	client := &fakeDatabricksClient{
		snapshot: snapshot,
		explain:  databricks.ExplainQueryResult{Statement: snapshot, PlanText: "== Physical Plan =="},
	}
	adapter := newDatabricksForClient(client)
	profile, err := adapter.Profile(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://dbx", IdempotencyKey: "profile",
		Budgets:  jobs.Budgets{HardExecution: 45 * time.Minute},
		Metadata: map[string]any{"warehouse_id": "w"},
	})
	if err != nil || !profile.Valid {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if profile.ProviderDetails["provider_execution_cap_supported"] != false ||
		profile.ProviderDetails["hard_execution_enforcement"] != "durable_monitor_cancellation" {
		t.Fatalf("provider enforcement details=%#v", profile.ProviderDetails)
	}
	warnings := strings.Join(profile.Warnings, " ")
	if !strings.Contains(warnings, "no provider-side execution timeout") || !strings.Contains(warnings, "durable monitor cancellation") {
		t.Fatalf("profile warnings=%#v", profile.Warnings)
	}

	submitted, err := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://dbx", IdempotencyKey: "query",
		Budgets:  jobs.Budgets{HardExecution: 45 * time.Minute, MaxResultBytes: 32 << 20},
		Metadata: map[string]any{"warehouse_id": "w"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if submitted.Initial == nil || submitted.Initial.State != jobs.ProviderStateSucceeded {
		t.Fatalf("Databricks wait_timeout initial observation=%+v", submitted.Initial)
	}
	if client.submitReq.WaitTimeout != 300*time.Millisecond || client.submitReq.OnWaitTimeout != "CONTINUE" {
		t.Fatalf("statement wait controls=%+v; wait timeout is transport-only and must remain CONTINUE", client.submitReq)
	}
	if client.submitReq.ByteLimit != databricks.MaxInlineStatementByteLimit {
		t.Fatalf("statement byte_limit=%d, want provider maximum %d", client.submitReq.ByteLimit, databricks.MaxInlineStatementByteLimit)
	}

	_, err = adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://dbx", IdempotencyKey: "query-small",
		Budgets:  jobs.Budgets{MaxResultBytes: 1 << 20},
		Metadata: map[string]any{"warehouse_id": "w"},
	})
	if err != nil || client.submitReq.ByteLimit != 1<<20 {
		t.Fatalf("smaller statement byte_limit=%d err=%v, want 1MiB unchanged", client.submitReq.ByteLimit, err)
	}
}

func TestDatabricksTerminalInlineResultHandsOffToNextChunkWithoutStatementRefetch(t *testing.T) {
	next := int32(1)
	snapshot := databricks.StatementSnapshot{
		Handle: databricks.StatementHandle{Host: "https://dbx", WarehouseID: "w", StatementID: "stmt-pages"},
		State:  "SUCCEEDED", Terminal: true, Succeeded: true, ResultAvailable: true,
		Manifest: databricks.StatementManifest{
			Columns:         []databricks.StatementColumn{{Name: "n", TypeName: "INT"}},
			TotalChunkCount: 2,
			TotalRowCount:   2,
			TotalByteCount:  16,
			Truncated:       true,
			Chunks: []databricks.StatementChunkInfo{
				{ChunkIndex: 0, RowCount: 1},
				{ChunkIndex: 1, RowOffset: 1, RowCount: 1},
			},
		},
		FirstChunk: databricks.ResultChunk{
			ChunkIndex:     0,
			DataArray:      [][]any{{1}},
			RowCount:       1,
			NextChunkIndex: &next,
		},
	}
	client := &fakeDatabricksClient{
		snapshot: snapshot,
		chunks: map[int32]databricks.ResultChunk{
			1: {ChunkIndex: 1, RowOffset: 1, RowCount: 1, DataArray: [][]any{{2}}},
		},
		// Any redundant statement GET would reproduce the production 400.
		statementErr: &databricks.StatementAPIError{StatusCode: http.StatusBadRequest, Message: "result already consumed"},
	}
	adapter := newDatabricksForClient(client)
	result, err := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1 union all select 2", ConnectionID: "https://dbx", IdempotencyKey: "paged",
		Budgets:  jobs.Budgets{MaxResultRows: 100, MaxResultBytes: 1 << 20},
		Metadata: map[string]any{"warehouse_id": "w"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if result.Initial == nil || len(result.Initial.DrainedPages) != 1 {
		t.Fatalf("terminal observation did not retain inline page: %+v", result.Initial)
	}
	first := result.Initial.DrainedPages[0]
	if len(first.Rows) != 1 || first.Rows[0]["n"] != 1 || first.NextPageToken != "dbx:1:0" {
		t.Fatalf("inline page=%+v", first)
	}
	if first.Truncated {
		t.Fatalf("intermediate page must not inherit manifest.truncated: %+v", first)
	}
	if result.Handle.Extra["result_manifest"] == "" {
		t.Fatalf("result manifest context missing from durable handle: %+v", result.Handle)
	}

	last, err := adapter.Fetch(context.Background(), result.Handle, jobs.FetchCursor{
		PageToken: first.NextPageToken, MaxRows: 100, MaxBytes: 1 << 20,
	})
	if err != nil {
		t.Fatalf("continuation fetch: %v", err)
	}
	if client.statementCalls != 0 || len(client.chunkCalls) != 1 || client.chunkCalls[0] != 1 {
		t.Fatalf("statement calls=%d chunk calls=%v; continuation must request chunk 1 directly", client.statementCalls, client.chunkCalls)
	}
	if len(last.Rows) != 1 || last.Rows[0]["n"] != 2 || last.NextPageToken != "" || !last.Truncated {
		t.Fatalf("last page=%+v", last)
	}
}

func TestDatabricksTerminalWithoutInlineChunkFetchesFirstManifestChunkDirectly(t *testing.T) {
	snapshot := databricks.StatementSnapshot{
		Handle: databricks.StatementHandle{Host: "https://dbx", WarehouseID: "w", StatementID: "stmt-no-inline"},
		State:  "SUCCEEDED", Terminal: true, Succeeded: true, ResultAvailable: true,
		Manifest: databricks.StatementManifest{
			Columns:       []databricks.StatementColumn{{Name: "n", TypeName: "INT"}},
			TotalRowCount: 1,
			Chunks:        []databricks.StatementChunkInfo{{ChunkIndex: 4, RowCount: 1}},
		},
		// Databricks omitted result.data_array entirely; the first index must be
		// recovered from manifest.chunks rather than a zero-value FirstChunk.
		FirstChunk: databricks.ResultChunk{},
	}
	client := &fakeDatabricksClient{
		snapshot: snapshot,
		chunks: map[int32]databricks.ResultChunk{
			4: {ChunkIndex: 4, RowCount: 1, DataArray: [][]any{{9}}},
		},
		statementErr: &databricks.StatementAPIError{StatusCode: http.StatusBadRequest, Message: "second statement GET rejected"},
	}
	adapter := newDatabricksForClient(client)
	result, err := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 9", ConnectionID: "https://dbx", IdempotencyKey: "no-inline",
		Metadata: map[string]any{"warehouse_id": "w"},
	})
	if err != nil || result.Initial == nil || len(result.Initial.DrainedPages) != 0 {
		t.Fatalf("submit result=%+v err=%v", result, err)
	}
	page, err := adapter.Fetch(context.Background(), result.Handle, jobs.FetchCursor{MaxRows: 10, MaxBytes: 1 << 20})
	if err != nil || len(page.Rows) != 1 || page.Rows[0]["n"] != 9 {
		t.Fatalf("first direct chunk page=%+v err=%v", page, err)
	}
	if client.statementCalls != 0 || len(client.chunkCalls) != 1 || client.chunkCalls[0] != 4 {
		t.Fatalf("statement calls=%d chunk calls=%v", client.statementCalls, client.chunkCalls)
	}
}

func TestDatabricksClosedAndBadResultCursorsRequireRecovery(t *testing.T) {
	closed := databricks.StatementSnapshot{
		Handle: databricks.StatementHandle{Host: "https://dbx", WarehouseID: "w", StatementID: "stmt-closed"},
		State:  "CLOSED", Terminal: true, Succeeded: true, Closed: true,
	}
	client := &fakeDatabricksClient{snapshot: closed}
	adapter := newDatabricksForClient(client)
	handle := jobs.NativeHandle{
		Provider: "databricks", ID: "stmt-closed", Location: "https://dbx",
		Extra: map[string]string{"host": "https://dbx", "warehouse_id": "w"},
	}
	if _, err := adapter.Poll(context.Background(), handle); !jobs.IsPermanentObservationLoss(err) {
		t.Fatalf("closed Poll error=%v want permanent observation loss", err)
	}
	if outcome, _, err := adapter.Adopt(context.Background(), handle); err != nil || outcome != jobs.AdoptOutcomeOrphaned {
		t.Fatalf("closed Adopt outcome=%q err=%v", outcome, err)
	}

	manifest := databricks.StatementManifest{Columns: []databricks.StatementColumn{{Name: "n"}}, TotalRowCount: 2}
	handle = databricksHandleWithResultManifest(handle, manifest, 0)
	client.chunkErrs = map[int32]error{
		1: &databricks.StatementAPIError{StatusCode: http.StatusBadRequest, Message: "result chunk is no longer available"},
	}
	_, err := adapter.Fetch(context.Background(), handle, jobs.FetchCursor{PageToken: "dbx:1:0"})
	if !jobs.IsPermanentObservationLoss(err) {
		t.Fatalf("bad result cursor error=%v want permanent observation loss", err)
	}
	if client.statementCalls != 0 {
		t.Fatalf("bad continuation performed %d statement GETs", client.statementCalls)
	}
}

func TestDatabricksObservableProfilePublishesDurableHandleBeforePhase(t *testing.T) {
	snapshot := databricks.StatementSnapshot{
		Handle: databricks.StatementHandle{Host: "https://dbx", WarehouseID: "wh-profile", StatementID: "profile-stmt"},
		State:  "SUCCEEDED", Terminal: true, Succeeded: true, ResultAvailable: true,
	}
	client := &fakeDatabricksClient{
		explain: databricks.ExplainQueryResult{Statement: snapshot, PlanText: "plan"},
		explainObservations: []databricks.ExplainObservation{{
			Phase: "explain_cost", Statement: snapshot,
		}},
	}
	adapter := newDatabricksForClient(client)
	observer := &recordingProfileObserver{}
	req := jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://dbx", IdempotencyKey: "profile-idem",
		Session:  map[string]string{"catalog": "main", "schema": "analytics"},
		Metadata: map[string]any{"warehouse_id": "wh-profile"},
	}
	profile, err := adapter.ProfileWithObserver(context.Background(), req, observer)
	if err != nil || !profile.Valid {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if got := strings.Join(observer.events, ","); got != "handle:profile-stmt:,phase:explain_cost" {
		t.Fatalf("publication order=%q", got)
	}
	handle := observer.handles[0]
	if handle.Provider != "databricks" || handle.Location != "https://dbx" || handle.Extra["warehouse_id"] != "wh-profile" || handle.Extra["catalog"] != "main" {
		t.Fatalf("durable handle=%+v", handle)
	}
	if client.explainReq.WaitTimeout != 50*time.Second || client.explainReq.OnWaitTimeout != "CANCEL" {
		t.Fatalf("profile controls=%+v", client.explainReq.StatementSubmitRequest)
	}
	if adapter.Capabilities().ProfileBound != 50*time.Second {
		t.Fatalf("ProfileBound=%s", adapter.Capabilities().ProfileBound)
	}
	if err := adapter.CancelProfile(context.Background(), handle); err != nil || len(client.cancelled) != 1 || client.cancelled[0].StatementID != "profile-stmt" {
		t.Fatalf("CancelProfile cancelled=%+v err=%v", client.cancelled, err)
	}
}

func TestDatabricksObservableProfilePropagatesFenceFailure(t *testing.T) {
	sentinel := errors.New("profile fence lost")
	snapshot := databricks.StatementSnapshot{Handle: databricks.StatementHandle{Host: "https://dbx", StatementID: "profile-stmt"}}
	client := &fakeDatabricksClient{
		explain:             databricks.ExplainQueryResult{Statement: snapshot},
		explainObservations: []databricks.ExplainObservation{{Phase: "explain_cost", Statement: snapshot}},
	}
	observer := &recordingProfileObserver{handleErr: sentinel}
	_, err := newDatabricksForClient(client).ProfileWithObserver(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://dbx", IdempotencyKey: "idem",
	}, observer)
	if !errors.Is(err, sentinel) || len(observer.events) != 1 || !strings.HasPrefix(observer.events[0], "handle:") {
		t.Fatalf("events=%v err=%v", observer.events, err)
	}
}

type fakeSnowflakeClient struct {
	requests            []snowflake.AsyncQueryRequest
	explainReq          snowflake.ExplainRequest
	explainObservations []snowflake.ExplainObservation
	cancelled           []snowflake.StatementRequest
	pollSnapshot        snowflake.StatementSnapshot
	pollErr             error
	submitOverride      *snowflake.StatementSnapshot
	submitOverrideErr   error
}

func (f *fakeSnowflakeClient) SubmitAsync(_ context.Context, req snowflake.AsyncQueryRequest) (snowflake.StatementSnapshot, error) {
	f.requests = append(f.requests, req)
	if f.submitOverride != nil || f.submitOverrideErr != nil {
		if f.submitOverride == nil {
			return snowflake.StatementSnapshot{}, f.submitOverrideErr
		}
		return *f.submitOverride, f.submitOverrideErr
	}
	if len(f.requests) == 1 {
		return snowflake.StatementSnapshot{}, errors.New("lost response")
	}
	return snowflake.StatementSnapshot{StatementHandle: "sf-1", RequestID: req.RequestID}, nil
}
func (f *fakeSnowflakeClient) PollAsync(context.Context, snowflake.StatementRequest) (snowflake.StatementSnapshot, error) {
	return f.pollSnapshot, f.pollErr
}
func (*fakeSnowflakeClient) FetchPartition(context.Context, snowflake.PartitionRequest) (snowflake.ResultPartition, error) {
	return snowflake.ResultPartition{}, nil
}
func (f *fakeSnowflakeClient) CancelAsync(_ context.Context, req snowflake.StatementRequest) (snowflake.CancelOutcome, error) {
	f.cancelled = append(f.cancelled, req)
	return snowflake.CancelOutcome{Accepted: true}, nil
}
func (f *fakeSnowflakeClient) ExplainQuery(_ context.Context, req snowflake.ExplainRequest) (snowflake.ExplainProfile, error) {
	f.explainReq = req
	for _, observation := range f.explainObservations {
		if req.OnObservation != nil {
			if err := req.OnObservation(observation); err != nil {
				return snowflake.ExplainProfile{}, err
			}
		}
	}
	return snowflake.ExplainProfile{ReadOnly: true, ExplainSucceeded: true}, nil
}

func TestSnowflakeReplayUsesSameRequestIDAndRetry(t *testing.T) {
	client := &fakeSnowflakeClient{}
	adapter := newSnowflakeForClient(client)
	req := jobs.SubmitRequest{
		SQL: "select 1", IdempotencyKey: "durable-id",
		Budgets:  jobs.Budgets{HardExecution: 2 * time.Hour},
		Metadata: map[string]any{"provider_timeout_ms": (30 * time.Minute).Milliseconds()},
	}
	if _, err := adapter.Submit(context.Background(), req); err == nil {
		t.Fatal("first submit should simulate a lost response")
	}
	result, err := adapter.Submit(context.Background(), req)
	if err != nil || result.Handle.ID != "sf-1" {
		t.Fatalf("replay result=%+v err=%v", result, err)
	}
	if len(client.requests) != 2 || client.requests[0].Retry || !client.requests[1].Retry || client.requests[0].RequestID != client.requests[1].RequestID {
		t.Fatalf("requests=%+v", client.requests)
	}
	if client.requests[0].Timeout != 30*time.Minute || client.requests[1].Timeout != client.requests[0].Timeout {
		t.Fatalf("replay timeouts=%s,%s want stable shorter explicit 30m", client.requests[0].Timeout, client.requests[1].Timeout)
	}
}

func TestSnowflakeHardBudgetCapsLongerExplicitTimeoutAndReconcileIsStable(t *testing.T) {
	client := &fakeSnowflakeClient{}
	adapter := newSnowflakeForClient(client)
	metadata := map[string]any{
		"provider_timeout_ms":      (4 * time.Hour).Milliseconds(),
		"hard_execution_budget_ms": (3 * time.Hour).Milliseconds(),
	}
	_, submitErr := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", IdempotencyKey: "reconcile-id", Metadata: metadata,
		Budgets: jobs.Budgets{HardExecution: 45 * time.Minute},
	})
	if submitErr == nil {
		t.Fatal("first submit should simulate an ambiguous lost response")
	}
	outcome, err := adapter.ReconcileSubmission(context.Background(), jobs.ReconcileRequest{
		SQL: "select 1", IdempotencyKey: "reconcile-id", Metadata: metadata,
		Budgets: jobs.Budgets{HardExecution: 45 * time.Minute},
	})
	if err != nil || outcome.Resolution != jobs.ReconcileFound || outcome.Handle.ID != "sf-1" {
		t.Fatalf("reconcile outcome=%+v err=%v", outcome, err)
	}
	if len(client.requests) != 2 || client.requests[0].Timeout != 45*time.Minute || client.requests[1].Timeout != 45*time.Minute {
		t.Fatalf("submit/reconcile timeouts=%+v, want stable hard cap 45m", client.requests)
	}
	if !client.requests[1].Retry || client.requests[0].RequestID != client.requests[1].RequestID {
		t.Fatalf("reconcile did not use stable request identity: %+v", client.requests)
	}
	if outcome.Handle.Extra["provider_timeout_ms"] != "2700000" {
		t.Fatalf("reconciled handle did not persist effective timeout: %+v", outcome.Handle)
	}
}

func TestSnowflakeObservableProfilePublishesDurableHandleBeforePhase(t *testing.T) {
	snapshot := snowflake.StatementSnapshot{
		StatementHandle: "sf-profile", StatementStatusURL: "/api/v2/statements/sf-profile", RequestID: "rid-profile",
		State: snowflake.StatementStateRunning,
	}
	client := &fakeSnowflakeClient{explainObservations: []snowflake.ExplainObservation{{Phase: "explain_json", Statement: snapshot}}}
	adapter := newSnowflakeForClient(client)
	observer := &recordingProfileObserver{}
	req := jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "org-account", IdempotencyKey: "profile-idem",
		Session: map[string]string{"warehouse": "WH", "database": "DB", "schema": "PUBLIC", "role": "ANALYST"},
	}
	profile, err := adapter.ProfileWithObserver(context.Background(), req, observer)
	if err != nil || !profile.Valid {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	if got := strings.Join(observer.events, ","); got != "handle:sf-profile:,phase:explain_json" {
		t.Fatalf("publication order=%q", got)
	}
	handle := observer.handles[0]
	if handle.Provider != "snowflake" || handle.Location != "org-account" || handle.Extra["statement_status_url"] != "/api/v2/statements/sf-profile" || handle.Extra["request_id"] != "rid-profile" || handle.Extra["warehouse"] != "WH" {
		t.Fatalf("durable handle=%+v", handle)
	}
	if client.explainReq.Timeout != 60*time.Second || handle.Extra["provider_timeout_ms"] != "60000" || adapter.Capabilities().ProfileBound != 60*time.Second {
		t.Fatalf("profile provider cap=%s durable=%q capabilities=%s", client.explainReq.Timeout, handle.Extra["provider_timeout_ms"], adapter.Capabilities().ProfileBound)
	}
	if err := adapter.CancelProfile(context.Background(), handle); err != nil || len(client.cancelled) != 1 || client.cancelled[0].StatementHandle != "sf-profile" {
		t.Fatalf("CancelProfile cancelled=%+v err=%v", client.cancelled, err)
	}
}

func TestSnowflakeSubmitCarriesInitialRetryAfterObservation(t *testing.T) {
	snapshot := snowflake.StatementSnapshot{
		StatementHandle: "sf-throttled", RequestID: "rid", State: snowflake.StatementStateRunning,
		RetryAfter: 7 * time.Second,
	}
	client := &fakeSnowflakeClient{submitOverride: &snapshot}
	result, err := newSnowflakeForClient(client).Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", IdempotencyKey: "throttled",
	})
	if err != nil || result.Initial == nil || result.Initial.RetryAfter != 7*time.Second || result.Initial.State != jobs.ProviderStateRunning {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type fakeTrinoClient struct {
	submit              trino.AsyncQueryPage
	poll                trino.AsyncQueryPage
	pollErr             error
	submitReq           trino.AsyncQueryRequest
	pollReq             trino.AsyncPollRequest
	explainReq          trino.ExplainRequest
	explainObservations []trino.ExplainObservation
	cancelled           []trino.AsyncPollRequest
}

func (f *fakeTrinoClient) SubmitAsync(_ context.Context, req trino.AsyncQueryRequest) (trino.AsyncQueryPage, error) {
	f.submitReq = req
	return f.submit, nil
}
func (f *fakeTrinoClient) PollAsync(_ context.Context, req trino.AsyncPollRequest) (trino.AsyncQueryPage, error) {
	f.pollReq = req
	return f.poll, f.pollErr
}

func TestTrinoExpiredCursorIsPermanentObservationLoss(t *testing.T) {
	client := &fakeTrinoClient{pollErr: trino.UpstreamError{
		StatusCode: http.StatusGone,
		Message:    "query cursor expired",
	}}
	adapter := newTrinoForClient(client)
	_, err := adapter.Poll(context.Background(), jobs.NativeHandle{
		Provider: "trino",
		ID:       "q-expired",
		Location: "https://trino.test",
		Extra: map[string]string{
			"host":     "https://trino.test",
			"next_uri": "https://trino.test/v1/statement/q-expired/1",
		},
	})
	if !jobs.IsPermanentObservationLoss(err) {
		t.Fatalf("Poll error=%v, want permanent observation loss", err)
	}
}
func (f *fakeTrinoClient) CancelAsync(_ context.Context, req trino.AsyncPollRequest) (trino.CancelOutcome, error) {
	f.cancelled = append(f.cancelled, req)
	return trino.CancelOutcome{Accepted: true}, nil
}
func (f *fakeTrinoClient) ExplainQuery(_ context.Context, req trino.ExplainRequest) (trino.ExplainProfile, error) {
	f.explainReq = req
	for _, observation := range f.explainObservations {
		if req.OnObservation != nil {
			if err := req.OnObservation(observation); err != nil {
				return trino.ExplainProfile{}, err
			}
		}
	}
	return trino.ExplainProfile{Validated: true}, nil
}

func TestTrinoObservableProfilePublishesEveryCurrentCursorBeforePhase(t *testing.T) {
	first := trino.AsyncQueryPage{
		QueryID: "trino-profile", RequestURI: "https://trino.test/v1/statement", NextURI: "https://trino.test/cursor/one",
		ProtocolState: trino.ProtocolState{SessionProperties: map[string]string{"retry_policy": "TASK"}},
	}
	second := first
	second.RequestURI = first.NextURI
	second.NextURI = "https://trino.test/cursor/two"
	client := &fakeTrinoClient{explainObservations: []trino.ExplainObservation{
		{Phase: "explain_distributed", Page: first},
		{Phase: "explain_distributed", Page: second},
	}}
	adapter := newTrinoForClient(client)
	observer := &recordingProfileObserver{}
	req := jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://trino.test", IdempotencyKey: "profile-idem",
		Session: map[string]string{"catalog": "iceberg", "schema": "analytics"},
	}
	profile, err := adapter.ProfileWithObserver(context.Background(), req, observer)
	if err != nil || !profile.Valid {
		t.Fatalf("profile=%+v err=%v", profile, err)
	}
	want := "handle:trino-profile:https://trino.test/cursor/one,phase:explain_distributed," +
		"handle:trino-profile:https://trino.test/cursor/two,phase:explain_distributed"
	if got := strings.Join(observer.events, ","); got != want {
		t.Fatalf("publication order=%q want %q", got, want)
	}
	latest := observer.handles[len(observer.handles)-1]
	if latest.Provider != "trino" || latest.Location != "https://trino.test" || latest.Extra["next_uri"] != second.NextURI || latest.Extra["catalog"] != "iceberg" || !strings.Contains(latest.Extra["protocol_state"], "retry_policy") {
		t.Fatalf("latest durable handle=%+v", latest)
	}
	if adapter.Capabilities().ProfileBound != 45*time.Second {
		t.Fatalf("ProfileBound=%s", adapter.Capabilities().ProfileBound)
	}
	for _, property := range []string{"query_max_run_time", "query_max_execution_time"} {
		if got := client.explainReq.SessionProperties[property]; got != "45000ms" {
			t.Fatalf("profile provider cap %s=%q want 45000ms", property, got)
		}
	}
	if err := adapter.CancelProfile(context.Background(), latest); err != nil || len(client.cancelled) != 1 || client.cancelled[0].NextURI != second.NextURI {
		t.Fatalf("CancelProfile cancelled=%+v err=%v", client.cancelled, err)
	}
}

func TestTrinoInitialAndTerminalPagesCarryAdvancingCursor(t *testing.T) {
	client := &fakeTrinoClient{
		submit: trino.AsyncQueryPage{
			QueryID: "q1", RequestURI: "https://trino.test/v1/statement", NextURI: "https://trino.test/next/1",
			Rows: []map[string]any{{"n": 0}}, Columns: []trino.StatementColumn{{Name: "n", Type: "bigint"}},
		},
		poll: trino.AsyncQueryPage{
			QueryID: "q1", RequestURI: "https://trino.test/next/1", Completed: true,
			Rows: []map[string]any{{"n": 1}}, Columns: []trino.StatementColumn{{Name: "n", Type: "bigint"}},
		},
	}
	adapter := newTrinoForClient(client)
	result, err := adapter.Submit(context.Background(), jobs.SubmitRequest{SQL: "select n", IdempotencyKey: "x"})
	if err != nil || result.Initial == nil || len(result.Initial.DrainedPages) != 1 {
		t.Fatalf("submit result=%+v err=%v", result, err)
	}
	if result.Initial.DrainedPages[0].Rows[0]["n"] != 0 || result.Initial.UpdatedHandle.Extra["next_uri"] == "" {
		t.Fatalf("initial observation=%+v", result.Initial)
	}
	terminal, err := adapter.Poll(context.Background(), *result.Initial.UpdatedHandle)
	if err != nil || terminal.State != jobs.ProviderStateSucceeded || len(terminal.DrainedPages) != 1 || terminal.DrainedPages[0].Rows[0]["n"] != 1 {
		t.Fatalf("terminal observation=%+v err=%v", terminal, err)
	}
	if terminal.UpdatedHandle.Extra["next_uri"] != "" {
		t.Fatalf("terminal cursor retained stale next_uri: %+v", terminal.UpdatedHandle)
	}
}

func TestTrinoProviderSafetyCapsPropagateAndPersistAcrossPoll(t *testing.T) {
	client := &fakeTrinoClient{
		submit: trino.AsyncQueryPage{
			QueryID: "q-caps", RequestURI: "https://trino.test/v1/statement", NextURI: "https://trino.test/next/1",
			ProtocolState: trino.ProtocolState{SessionProperties: map[string]string{"resource_overcommit": "false"}},
		},
		poll: trino.AsyncQueryPage{QueryID: "q-caps", RequestURI: "https://trino.test/next/1", Completed: true},
	}
	adapter := newTrinoForClient(client)
	result, err := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://trino.test", IdempotencyKey: "caps",
		Budgets: jobs.Budgets{HardExecution: 90 * time.Minute, MaxScanBytes: 12345},
		Session: map[string]string{"join_distribution_type": "BROADCAST"},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	wantDuration := "5400000ms"
	for _, property := range []string{"query_max_run_time", "query_max_execution_time"} {
		if got := client.submitReq.SessionProperties[property]; got != wantDuration {
			t.Fatalf("%s=%q want %q", property, got, wantDuration)
		}
	}
	if got := client.submitReq.SessionProperties["query_max_scan_physical_bytes"]; got != "12345B" {
		t.Fatalf("scan cap=%q want 12345B", got)
	}
	if client.submitReq.SessionProperties["join_distribution_type"] != "BROADCAST" {
		t.Fatalf("other session property was lost: %#v", client.submitReq.SessionProperties)
	}

	if _, err := adapter.Poll(context.Background(), result.Handle); err != nil {
		t.Fatalf("poll: %v", err)
	}
	for _, property := range []string{"query_max_run_time", "query_max_execution_time"} {
		if client.pollReq.SessionProperties[property] != wantDuration {
			t.Fatalf("poll lost %s: %#v", property, client.pollReq.SessionProperties)
		}
	}
	if client.pollReq.SessionProperties["query_max_scan_physical_bytes"] != "12345B" ||
		client.pollReq.ProtocolState.SessionProperties["resource_overcommit"] != "false" {
		t.Fatalf("poll lost cap or protocol state: session=%#v protocol=%#v", client.pollReq.SessionProperties, client.pollReq.ProtocolState)
	}
}

func TestTrinoProviderCapsNeverWeakenStricterSessionOrProtocolLimits(t *testing.T) {
	client := &fakeTrinoClient{submit: trino.AsyncQueryPage{
		QueryID: "q-strict", RequestURI: "https://trino.test/v1/statement", NextURI: "https://trino.test/next/1",
	}}
	adapter := newTrinoForClient(client)
	_, err := adapter.Submit(context.Background(), jobs.SubmitRequest{
		SQL: "select 1", ConnectionID: "https://trino.test", IdempotencyKey: "strict",
		Budgets: jobs.Budgets{HardExecution: 90 * time.Minute, MaxScanBytes: 1 << 30},
		Session: map[string]string{
			"query_max_run_time":            "30m",
			"query_max_execution_time":      "4h",
			"query_max_scan_physical_bytes": "64MB",
			"retry_policy":                  "TASK",
		},
		Metadata: map[string]any{
			"protocol_state": trino.ProtocolState{SessionProperties: map[string]string{
				"query_max_execution_time": "15m",
				"resource_overcommit":      "false",
			}},
		},
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if got := client.submitReq.SessionProperties["query_max_run_time"]; got != "30m" {
		t.Fatalf("stricter run cap weakened to %q", got)
	}
	if _, exists := client.submitReq.SessionProperties["query_max_execution_time"]; exists {
		t.Fatalf("protocol's stricter 15m execution cap was overridden: %#v", client.submitReq.SessionProperties)
	}
	if got := client.submitReq.ProtocolState.SessionProperties["query_max_execution_time"]; got != "15m" {
		t.Fatalf("protocol execution cap=%q want 15m", got)
	}
	if got := client.submitReq.SessionProperties["query_max_scan_physical_bytes"]; got != "64MB" {
		t.Fatalf("stricter scan cap weakened to %q", got)
	}
	if client.submitReq.SessionProperties["retry_policy"] != "TASK" ||
		client.submitReq.ProtocolState.SessionProperties["resource_overcommit"] != "false" {
		t.Fatalf("unrelated session/protocol state lost: request=%#v", client.submitReq)
	}
}

func TestProviderCapabilitiesOnlyDeclareEmittedSignals(t *testing.T) {
	tests := []struct {
		name                  string
		capabilities          jobs.ProviderCapabilities
		wantSignals           []jobs.ProgressSignal
		wantQueuedVisible     bool
		wantCancelConfirmable bool
	}{
		{name: "bigquery", capabilities: newBigQueryForClient(&fakeBigQueryClient{}).Capabilities(), wantSignals: []jobs.ProgressSignal{jobs.SignalBytesProcessed}, wantQueuedVisible: true, wantCancelConfirmable: true},
		{name: "databricks", capabilities: newDatabricksForClient(&fakeDatabricksClient{}).Capabilities(), wantSignals: []jobs.ProgressSignal{jobs.SignalBytesProcessed, jobs.SignalRowsProcessed}, wantQueuedVisible: false, wantCancelConfirmable: true},
		{name: "snowflake", capabilities: newSnowflakeForClient(&fakeSnowflakeClient{}).Capabilities(), wantSignals: nil, wantQueuedVisible: true, wantCancelConfirmable: true},
		{name: "trino", capabilities: newTrinoForClient(&fakeTrinoClient{}).Capabilities(), wantSignals: []jobs.ProgressSignal{jobs.SignalBytesProcessed, jobs.SignalRowsProcessed, jobs.SignalSplits, jobs.SignalNativePercent}, wantQueuedVisible: true, wantCancelConfirmable: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.capabilities.QueuedStateVisible != tt.wantQueuedVisible || tt.capabilities.CancelConfirmable != tt.wantCancelConfirmable {
				t.Fatalf("capabilities=%+v", tt.capabilities)
			}
			if len(tt.capabilities.ProgressSignals) != len(tt.wantSignals) {
				t.Fatalf("progress signals=%v want %v", tt.capabilities.ProgressSignals, tt.wantSignals)
			}
			for index := range tt.wantSignals {
				if tt.capabilities.ProgressSignals[index] != tt.wantSignals[index] {
					t.Fatalf("progress signals=%v want %v", tt.capabilities.ProgressSignals, tt.wantSignals)
				}
			}
			for _, signal := range tt.capabilities.ProgressSignals {
				if signal == jobs.SignalStageInfo {
					t.Fatalf("%s advertises unextractable stage_info", tt.name)
				}
			}
		})
	}
}

func TestProviderRetryAfterHintsPropagateToSchedulerContract(t *testing.T) {
	t.Run("bigquery typed API error hint", func(t *testing.T) {
		client := &fakeBigQueryClient{getErr: &bigquery.QueryAPIError{
			StatusCode: http.StatusServiceUnavailable, Message: "retry later", RetryAfterDelay: 5 * time.Second,
		}}
		_, err := newBigQueryForClient(client).Poll(context.Background(), jobs.NativeHandle{
			Provider: "bigquery", ID: "job", Extra: map[string]string{"project_id": "p"},
		})
		if got := jobs.RetryAfterFromError(err); got != 5*time.Second {
			t.Fatalf("RetryAfterFromError=%s err=%v", got, err)
		}
	})

	t.Run("databricks successful observation with history backpressure", func(t *testing.T) {
		client := &fakeDatabricksClient{
			snapshot: databricks.StatementSnapshot{
				Handle: databricks.StatementHandle{Host: "https://dbx", StatementID: "stmt"},
				State:  "RUNNING",
			},
			statementProfileErr: &databricks.StatementAPIError{StatusCode: http.StatusTooManyRequests, Message: "slow down", RetryAfter: 3 * time.Second},
		}
		status, err := newDatabricksForClient(client).Poll(context.Background(), jobs.NativeHandle{
			Provider: "databricks", ID: "stmt", Location: "https://dbx", Extra: map[string]string{"host": "https://dbx"},
		})
		if err != nil || status.RetryAfter != 3*time.Second {
			t.Fatalf("status=%+v err=%v", status, err)
		}
	})

	t.Run("snowflake successful throttled snapshot", func(t *testing.T) {
		client := &fakeSnowflakeClient{pollSnapshot: snowflake.StatementSnapshot{
			StatementHandle: "sf", State: snowflake.StatementStateRunning, RetryAfter: 7 * time.Second,
		}}
		status, err := newSnowflakeForClient(client).Poll(context.Background(), jobs.NativeHandle{Provider: "snowflake", ID: "sf"})
		if err != nil || status.RetryAfter != 7*time.Second {
			t.Fatalf("status=%+v err=%v", status, err)
		}
	})

	t.Run("snowflake error hint", func(t *testing.T) {
		client := &fakeSnowflakeClient{pollErr: &snowflake.APIError{
			HTTPStatus: http.StatusServiceUnavailable, Message: "retry later", RetryAfter: 9 * time.Second,
		}}
		_, err := newSnowflakeForClient(client).Poll(context.Background(), jobs.NativeHandle{Provider: "snowflake", ID: "sf"})
		if got := jobs.RetryAfterFromError(err); got != 9*time.Second {
			t.Fatalf("RetryAfterFromError=%s err=%v", got, err)
		}
	})

	t.Run("trino error hint", func(t *testing.T) {
		client := &fakeTrinoClient{pollErr: trino.UpstreamError{
			StatusCode: http.StatusServiceUnavailable, Message: "retry later", RetryAfter: 4 * time.Second,
		}}
		_, err := newTrinoForClient(client).Poll(context.Background(), jobs.NativeHandle{
			Provider: "trino", ID: "q", Location: "https://trino.test", Extra: map[string]string{
				"host": "https://trino.test", "next_uri": "https://trino.test/cursor",
			},
		})
		if got := jobs.RetryAfterFromError(err); got != 4*time.Second {
			t.Fatalf("RetryAfterFromError=%s err=%v", got, err)
		}
	})
}
