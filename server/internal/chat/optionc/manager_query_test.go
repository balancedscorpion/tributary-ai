package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/providers"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/secrets"
)

func TestManagerQueryJobStatusAndCancelValidation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager, st, queryJobs := newQueryManagerHarness(t)
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	queryJobs.jobs["job-1"] = map[string]any{
		"job_id":           "job-1",
		"thread_id":        thread.ID,
		"owner_token":      "owner-1",
		"status":           "running",
		"warehouse_job_id": "bq-job-warehouse-1",
		"url":              "https://console.cloud.google.com/bigquery?project=proj-1&j=bq%3AUS%3Abq-job-warehouse-1&page=queryresults",
	}

	got, err := manager.GetQueryJobStatus(ctx, "job-1", thread.ID, "owner-1")
	if err != nil {
		t.Fatalf("GetQueryJobStatus: %v", err)
	}
	if strings.TrimSpace(anyString(got["status"])) != "running" {
		t.Fatalf("status=%q, want running", anyString(got["status"]))
	}
	if strings.TrimSpace(anyString(got["provider_job_id"])) != "bq-job-warehouse-1" {
		t.Fatalf("provider_job_id=%q, want bq-job-warehouse-1", anyString(got["provider_job_id"]))
	}
	if strings.TrimSpace(anyString(got["provider_job_url"])) != "https://console.cloud.google.com/bigquery?project=proj-1&j=bq%3AUS%3Abq-job-warehouse-1&page=queryresults" {
		t.Fatalf("provider_job_url=%q, want BigQuery console URL", anyString(got["provider_job_url"]))
	}
	if strings.TrimSpace(anyString(got["warehouse_job_id"])) != "bq-job-warehouse-1" {
		t.Fatalf("warehouse_job_id=%q, want bq-job-warehouse-1", anyString(got["warehouse_job_id"]))
	}
	if strings.TrimSpace(anyString(got["url"])) != "https://console.cloud.google.com/bigquery?project=proj-1&j=bq%3AUS%3Abq-job-warehouse-1&page=queryresults" {
		t.Fatalf("url=%q, want BigQuery console URL", anyString(got["url"]))
	}

	queryJobs.jobs["job-trino"] = map[string]any{
		"job_id":      "job-trino",
		"thread_id":   thread.ID,
		"owner_token": "owner-1",
		"provider":    "trino",
		"status":      "running",
		"query_id":    "tri-query-1",
		"next_uri":    "https://trino.example/v1/statement/queued/1",
	}
	trinoStatus, err := manager.GetQueryJobStatus(ctx, "job-trino", thread.ID, "owner-1")
	if err != nil {
		t.Fatalf("GetQueryJobStatus(trino): %v", err)
	}
	if strings.TrimSpace(anyString(trinoStatus["provider_job_id"])) != "tri-query-1" {
		t.Fatalf("provider_job_id=%q, want tri-query-1", anyString(trinoStatus["provider_job_id"]))
	}
	if strings.TrimSpace(anyString(trinoStatus["provider_job_url"])) != "https://trino.example/v1/statement/queued/1" {
		t.Fatalf("provider_job_url=%q, want Trino next URI", anyString(trinoStatus["provider_job_url"]))
	}

	cancelled, err := manager.CancelQueryJob(ctx, "job-1", thread.ID, "owner-1")
	if err != nil {
		t.Fatalf("CancelQueryJob: %v", err)
	}
	if strings.TrimSpace(anyString(cancelled["status"])) != "cancelled" {
		t.Fatalf("cancel status=%q, want cancelled", anyString(cancelled["status"]))
	}
}

func TestQueryJobPayloadFreezesTerminalDurations(t *testing.T) {
	t.Parallel()
	submitted := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)
	started := submitted.Add(2 * time.Minute)
	completed := started.Add(7 * time.Minute)

	payload := queryJobPayloadFromRegistry(&jobs.Job{
		ID:          "job-terminal-duration",
		Provider:    "bigquery",
		Status:      jobs.JobStatusCompleted,
		Owner:       jobs.JobOwner{ThreadID: "thread-1", Handle: "owner-1"},
		SubmittedAt: submitted,
		StartedAt:   &started,
		CompletedAt: &completed,
		LastAccess:  completed,
		Metadata: map[string]any{
			jobs.QueryObligationManagedMetadataKey: true,
		},
	})
	if _, leaked := payload[jobs.QueryObligationManagedMetadataKey]; leaked {
		t.Fatalf("queryJobPayloadFromRegistry leaked internal marker: %#v", payload)
	}

	if got := payload["elapsed_ms"]; got != int64((9 * time.Minute).Milliseconds()) {
		t.Fatalf("elapsed_ms=%#v, want frozen 9m", got)
	}
	if got := payload["queued_elapsed_ms"]; got != int64((2 * time.Minute).Milliseconds()) {
		t.Fatalf("queued_elapsed_ms=%#v, want 2m", got)
	}
	if got := payload["execution_elapsed_ms"]; got != int64((7 * time.Minute).Milliseconds()) {
		t.Fatalf("execution_elapsed_ms=%#v, want frozen 7m", got)
	}
}

func TestManagerQueryJobStatusAuthFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager, st, queryJobs := newQueryManagerHarness(t)
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	queryJobs.jobs["job-2"] = map[string]any{
		"job_id":      "job-2",
		"thread_id":   thread.ID,
		"owner_token": "owner-2",
		"status":      "running",
	}

	_, err = manager.GetQueryJobStatus(ctx, "job-2", thread.ID, "wrong-owner")
	if !errors.Is(err, ErrQueryJobUnauthorized) {
		t.Fatalf("wrong owner err=%v, want ErrQueryJobUnauthorized", err)
	}

	_, err = manager.GetQueryJobStatus(ctx, "job-2", "other-thread", "owner-2")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing thread err=%v, want store.ErrNotFound", err)
	}

	otherThread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread(other): %v", err)
	}
	_, err = manager.GetQueryJobStatus(ctx, "job-2", otherThread.ID, "owner-2")
	if !errors.Is(err, ErrQueryJobThreadScope) {
		t.Fatalf("wrong thread err=%v, want ErrQueryJobThreadScope", err)
	}
}

func TestManagerQueryJobStatusResolvesIdentityInRequestingRun(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	manager, st, queryJobs := newQueryManagerHarness(t)
	repository, ok := st.(store.LongHorizonRepository)
	if !ok {
		t.Fatal("query manager harness store does not implement LongHorizonRepository")
	}
	manager.longHorizon = repository
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	ownerRun, err := st.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun owner: %v", err)
	}
	attachedRun, err := st.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun attached: %v", err)
	}
	unattachedRun, err := st.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 20)
	if err != nil {
		t.Fatalf("CreateRun unattached: %v", err)
	}

	now := time.Now().UTC()
	record := jobs.JobRecord{
		ID:          "job-cross-run-status",
		Provider:    "databricks",
		Status:      jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: ownerRun.ID, Handle: "owner-cross-run"},
		SubmittedAt: now,
		LastAccess:  now,
		Metadata:    map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("UpsertQueryJobCAS: %v", err)
	}
	ownerObligation, err := store.QueryObligationIDForRoot(ownerRun.ID, record.ID)
	if err != nil {
		t.Fatalf("owner QueryObligationIDForRoot: %v", err)
	}
	attachedObligation, err := store.QueryObligationIDForRoot(attachedRun.ID, record.ID)
	if err != nil {
		t.Fatalf("attached QueryObligationIDForRoot: %v", err)
	}
	for _, dependency := range []store.RunQueryDependency{
		{RunID: ownerRun.ID, JobID: record.ID, RequiredForCompletion: true, ObligationID: ownerObligation},
		// Simulate a live dependency written by a pre-obligation version. Status
		// must derive and persist its run-scoped identity before returning it.
		{RunID: attachedRun.ID, JobID: record.ID, RequiredForCompletion: true},
	} {
		if _, err := repository.UpsertRunQueryDependency(ctx, dependency); err != nil {
			t.Fatalf("UpsertRunQueryDependency(%s): %v", dependency.RunID, err)
		}
	}
	queryJobs.jobs[record.ID] = map[string]any{
		"job_id":                               record.ID,
		"thread_id":                            thread.ID,
		"owner_token":                          record.Owner.Handle,
		"status":                               "running",
		"run_id":                               ownerRun.ID,
		"query_obligation_id":                  ownerObligation,
		jobs.QueryObligationManagedMetadataKey: true,
		"nested": map[string]any{
			"query_obligation_id":                  ownerObligation,
			"run_id":                               ownerRun.ID,
			"replaces_job_id":                      "qj-old-private-target",
			jobs.QueryObligationManagedMetadataKey: true,
		},
	}

	ownerStatus, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, ownerRun.ID, ownerObligation,
	)
	if err != nil {
		t.Fatalf("owner GetQueryJobStatusForRun: %v", err)
	}
	if got := strings.TrimSpace(anyString(ownerStatus["query_obligation_id"])); got != ownerObligation {
		t.Fatalf("owner query_obligation_id=%q want=%q", got, ownerObligation)
	}

	attachedStatus, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, attachedRun.ID, "",
	)
	if err != nil {
		t.Fatalf("attached GetQueryJobStatusForRun: %v", err)
	}
	if got := strings.TrimSpace(anyString(attachedStatus["run_id"])); got != attachedRun.ID {
		t.Fatalf("attached run_id=%q want=%q", got, attachedRun.ID)
	}
	if got := strings.TrimSpace(anyString(attachedStatus["query_obligation_id"])); got != attachedObligation {
		t.Fatalf("attached query_obligation_id=%q want=%q", got, attachedObligation)
	}
	if attachedObligation == ownerObligation {
		t.Fatalf("run-scoped obligations unexpectedly match: %q", attachedObligation)
	}
	attachedRaw, attachedMarshalErr := json.Marshal(attachedStatus)
	if attachedMarshalErr != nil || strings.Contains(string(attachedRaw), ownerObligation) || strings.Contains(string(attachedRaw), ownerRun.ID) || strings.Contains(string(attachedRaw), "qj-old-private-target") {
		t.Fatalf("attached status leaked nested owner-run identity: err=%v payload=%s", attachedMarshalErr, attachedRaw)
	}
	attachedDependencies, err := repository.ListRunQueryDependencies(ctx, attachedRun.ID)
	if err != nil || len(attachedDependencies) != 1 || attachedDependencies[0].ObligationID != attachedObligation {
		t.Fatalf("legacy obligation was not durably hydrated: dependencies=%#v err=%v", attachedDependencies, err)
	}
	if _, leaked := attachedStatus[jobs.QueryObligationManagedMetadataKey]; leaked {
		t.Fatalf("status leaked internal metadata marker: %#v", attachedStatus)
	}

	if _, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, attachedRun.ID, ownerObligation,
	); !errors.Is(err, ErrQueryJobRunScope) {
		t.Fatalf("mismatched obligation err=%v want ErrQueryJobRunScope", err)
	}
	if _, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, unattachedRun.ID, "",
	); !errors.Is(err, ErrQueryJobRunScope) {
		t.Fatalf("unattached run err=%v want ErrQueryJobRunScope", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, store.RunQueryDependency{
		RunID: unattachedRun.ID, JobID: record.ID, RequiredForCompletion: true,
	}); err != nil {
		t.Fatalf("UpsertRunQueryDependency legacy detached: %v", err)
	}
	if err := repository.DetachRunQueryDependencies(ctx, unattachedRun.ID, time.Now().UTC()); err != nil {
		t.Fatalf("DetachRunQueryDependencies legacy: %v", err)
	}
	legacyDetachedStatus, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, unattachedRun.ID, "",
	)
	if err != nil {
		t.Fatalf("legacy detached GetQueryJobStatusForRun: %v", err)
	}
	if got := strings.TrimSpace(anyString(legacyDetachedStatus["query_obligation_id"])); got != "" {
		t.Fatalf("legacy detached status advertised inactive query_obligation_id=%q", got)
	}
	if _, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, attachedRun.ID, "qob-00000000000000000000000000000000",
	); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("invalid obligation err=%v want store.ErrConflict", err)
	}
	if err := repository.DetachRunQueryDependencies(ctx, ownerRun.ID, time.Now().UTC()); err != nil {
		t.Fatalf("DetachRunQueryDependencies: %v", err)
	}
	if _, err := manager.GetQueryJobStatusForRun(
		ctx, record.ID, thread.ID, record.Owner.Handle, ownerRun.ID, ownerObligation,
	); err != nil {
		t.Fatalf("detached completed-run dependency should remain pollable: %v", err)
	}

	legacyStatus, err := manager.GetQueryJobStatus(ctx, record.ID, thread.ID, record.Owner.Handle)
	if err != nil {
		t.Fatalf("legacy GetQueryJobStatus: %v", err)
	}
	if _, exists := legacyStatus["query_obligation_id"]; exists {
		t.Fatalf("legacy unscoped status echoed obligation: %#v", legacyStatus)
	}
	if _, exists := legacyStatus["run_id"]; exists {
		t.Fatalf("legacy unscoped status echoed run identity: %#v", legacyStatus)
	}
}

func TestResolveToolArtifactIDDeterministicAsync(t *testing.T) {
	call := types.ToolCall{Name: "run_bigquery_query"}
	out := tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		QueryMetadata: map[string]any{
			"job_id": "job-abc",
		},
	}
	idA := resolveToolArtifactID(call, out)
	idB := resolveToolArtifactID(call, out)
	if strings.TrimSpace(idA) == "" || strings.TrimSpace(idB) == "" {
		t.Fatalf("deterministic artifact ids should be non-empty: %q %q", idA, idB)
	}
	if idA != idB {
		t.Fatalf("deterministic artifact ids differ: %q vs %q", idA, idB)
	}

	out.QueryMetadata["artifact_id"] = "artifact-explicit"
	if got := resolveToolArtifactID(call, out); got != "artifact-explicit" {
		t.Fatalf("explicit artifact id=%q, want artifact-explicit", got)
	}
}

// get_query_results is part of the async query lifecycle: the completed
// dataset must resolve to query_metadata.artifact_id (the deterministic id
// the placeholder was stored under at submit time) so the insert conflicts
// with the placeholder and upgrades it, instead of landing under a fresh
// random UUID that leaves the running card stale forever.
func TestResolveToolArtifactIDGetQueryResultsUsesLifecycleArtifactID(t *testing.T) {
	call := types.ToolCall{Name: "get_query_results"}
	out := tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows":     []any{map[string]any{"a": 1.0}},
			"has_more": false,
		},
		QueryMetadata: map[string]any{
			"artifact_id": "art-lifecycle",
			"job_id":      "job-abc",
		},
	}
	if got := resolveToolArtifactID(call, out); got != "art-lifecycle" {
		t.Fatalf("completed results artifact id=%q, want art-lifecycle", got)
	}

	// Paginated follow-ups carry the same lifecycle id and must resolve to it
	// (the conflict-upgrade path keeps the stored first page).
	out.QueryMetadata["offset"] = 100
	if got := resolveToolArtifactID(call, out); got != "art-lifecycle" {
		t.Fatalf("paginated results artifact id=%q, want art-lifecycle", got)
	}

	// Pending retries (no lifecycle id in metadata yet) fall back to a stable
	// job-derived id so repeated polls do not litter unique hidden artifacts.
	pending := tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"job_id":    "job-abc",
			"status":    "running",
			"retryable": true,
		},
	}
	idA := resolveToolArtifactID(call, pending)
	idB := resolveToolArtifactID(call, pending)
	if strings.TrimSpace(idA) == "" || idA != idB {
		t.Fatalf("pending results ids should be stable and non-empty: %q vs %q", idA, idB)
	}
}

func TestQueryStatusPlaceholderUsesCanonicalArtifactAndPollingIdentity(t *testing.T) {
	call := types.ToolCall{Name: "check_query_status"}
	threadID := "thread-status-artifact"
	jobID := "qj-status-artifact"
	ownerToken := "tok_status_artifact"
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"job_id": jobID,
			"status": "running",
		},
		ResultMetadata: map[string]any{
			"job_id":      jobID,
			"thread_id":   threadID,
			"owner_token": ownerToken,
			"status":      "running",
		},
	}
	wantID := tools.DeterministicAsyncArtifactID(threadID, jobID)
	if got := resolveToolArtifactIDForThread(call, out, threadID); got != wantID {
		t.Fatalf("status artifact id=%q, want canonical %q", got, wantID)
	}
	_, _, _, _, metadata := buildArtifact(call.Name, out, `{"job_id":"`+jobID+`","status":"running"}`)
	if metadata["thread_id"] != threadID || metadata["owner_token"] != ownerToken || metadata["job_id"] != jobID {
		t.Fatalf("status placeholder lost polling identity: %#v", metadata)
	}
}

func TestResolveToolArtifactIDIgnoresSourceArtifactIDsForRetrievalResults(t *testing.T) {
	call := types.ToolCall{Name: "get_artifact_chunk"}
	out := tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"id":          "source-artifact",
			"artifact_id": "source-artifact",
		},
	}
	if got := resolveToolArtifactID(call, out); got != "" {
		t.Fatalf("retrieval source artifact id should not become result artifact id, got %q", got)
	}

	out.Result = map[string]any{"result_artifact_id": "result-artifact"}
	if got := resolveToolArtifactID(call, out); got != "result-artifact" {
		t.Fatalf("explicit result artifact id=%q, want result-artifact", got)
	}
}

func TestShouldSuppressToolUIAsyncStatusPending(t *testing.T) {
	pending := shouldSuppressToolUI("get_query_status", tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result:       map[string]any{"status": "running"},
	})
	if !pending {
		t.Fatalf("pending query status should be suppressed")
	}

	complete := shouldSuppressToolUI("get_query_status", tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result:       map[string]any{"status": "completed"},
	})
	if complete {
		t.Fatalf("completed query status should not be suppressed")
	}

	metaHidden := shouldSuppressToolUI("search_docs", tools.Output{
		ResultFormat:  tools.ResultFormatJSON,
		Result:        map[string]any{"status": "completed"},
		QueryMetadata: map[string]any{"ui_hidden": true},
	})
	if !metaHidden {
		t.Fatalf("metadata ui_hidden should suppress tool UI")
	}
}

func TestShouldSuppressToolUIShowsDurableProfileRejection(t *testing.T) {
	durable := shouldSuppressToolUI("run_bigquery_query", tools.Output{
		OK:           false,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"job_id": "qj-profile-invalid", "preflight_failed": true,
			"profile_outcome": "invalid", "remote_submitted": false,
		},
		QueryMetadata: map[string]any{"job_id": "qj-profile-invalid"},
	})
	if durable {
		t.Fatal("durable profile rejection must remain visible in the query inspector")
	}
	legacy := shouldSuppressToolUI("run_bigquery_query", tools.Output{
		OK: false, ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{"preflight_failed": true},
	})
	if !legacy {
		t.Fatal("legacy preflight failure without a durable job should remain suppressed")
	}
}

func TestShouldSuppressToolUIGetQueryResultsRetryable(t *testing.T) {
	pending := shouldSuppressToolUI("get_query_results", tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"status":      "running",
			"retryable":   true,
			"ui_suppress": true,
		},
	})
	if !pending {
		t.Fatalf("retryable get_query_results should be suppressed")
	}

	nonTerminal := shouldSuppressToolUI("get_query_results", tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"status": "submitted",
		},
	})
	if !nonTerminal {
		t.Fatalf("non-terminal get_query_results should be suppressed")
	}

	completed := shouldSuppressToolUI("get_query_results", tools.Output{
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"status": "completed",
			"rows":   []any{map[string]any{"n": 1}},
		},
	})
	if completed {
		t.Fatalf("completed get_query_results should not be suppressed")
	}
}

func TestManagerAutoCancelActiveQueryJobsForThread(t *testing.T) {
	t.Parallel()
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

	queryJobs := jobs.NewRegistry(jobs.RegistryOptions{})
	manager := NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(nil),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		&Options{QueryJobs: queryJobs},
	)

	job, err := queryJobs.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		nil,
		func(ctx context.Context) (bool, *jobs.QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	cancelled := manager.cancelActiveQueryJobsForThread("thread-1")
	if len(cancelled) != 1 || strings.TrimSpace(cancelled[0]) != job.ID {
		t.Fatalf("cancelled=%v want=[%s]", cancelled, job.ID)
	}

	snap, ok := queryJobs.Snapshot(job.ID, jobs.JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
	if !ok {
		t.Fatalf("snapshot missing")
	}
	if snap.Status != jobs.JobStatusCancelled {
		t.Fatalf("status=%s want=%s", snap.Status, jobs.JobStatusCancelled)
	}
}

func TestManagerQueryJobStatusMaterializesCompletedRegistryResultArtifact(t *testing.T) {
	t.Parallel()
	cfgDir := t.TempDir()
	ctx := context.Background()

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
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	queryJobs := jobs.NewRegistry(jobs.RegistryOptions{})
	manager := NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(nil),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		&Options{QueryJobs: queryJobs},
	)

	job, err := queryJobs.Register(
		jobs.JobOwner{ThreadID: thread.ID, RunID: "run-1", Handle: "owner-1"},
		"bigquery",
		"select 1 as n union all select 2 as n",
		"select 1 as n union all select 2 as n",
		map[string]any{
			"project_id":                           "proj-1",
			"full_data_requested":                  true,
			jobs.QueryObligationManagedMetadataKey: true,
		},
		func(context.Context) (bool, *jobs.QueryResult, error) {
			return true, &jobs.QueryResult{
				Rows: []map[string]any{
					{"n": 1.0},
					{"n": 2.0},
				},
				Schema: []map[string]any{{"name": "n", "type": "INT64"}},
				Metadata: map[string]any{
					"bytes_processed":                      128.0,
					jobs.QueryObligationManagedMetadataKey: true,
				},
			}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	ownerToken := strings.TrimSpace(job.Owner.Handle)
	artifactID := tools.DeterministicAsyncArtifactID(thread.ID, job.ID)
	if artifactID == "" {
		t.Fatalf("deterministic artifact id is empty")
	}
	toolName := "run_bigquery_query"
	okVal := true
	empty := ""
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Running",
		ToolName:    &toolName,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &empty,
		Metadata: map[string]any{
			"placeholder": true,
			"async_query": true,
			"job_id":      job.ID,
			"thread_id":   thread.ID,
			"owner_token": ownerToken,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact placeholder: %v", err)
	}

	waitForRegistryStatus(t, queryJobs, job.ID, jobs.JobOwner{ThreadID: thread.ID, Handle: ownerToken}, jobs.JobStatusCompleted)
	status, err := manager.GetQueryJobStatus(ctx, job.ID, thread.ID, ownerToken)
	if err != nil {
		t.Fatalf("GetQueryJobStatus: %v", err)
	}
	if got := strings.TrimSpace(anyString(status["status"])); got != string(jobs.JobStatusCompleted) {
		t.Fatalf("status=%q want completed", got)
	}
	if got := strings.TrimSpace(anyString(status["artifact_id"])); got != artifactID {
		t.Fatalf("artifact_id=%q want %q", got, artifactID)
	}
	if ready, _ := status["results_ready"].(bool); !ready {
		t.Fatalf("results_ready=%#v want true", status["results_ready"])
	}

	artifact, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact materialized: %v", err)
	}
	if artifact.ToolName == nil || *artifact.ToolName != "get_query_results" {
		t.Fatalf("tool_name=%v want get_query_results", artifact.ToolName)
	}
	if parseBoolValue(artifact.Metadata["placeholder"]) || parseBoolValue(artifact.Metadata["async_query"]) {
		t.Fatalf("materialized artifact still marked placeholder: %#v", artifact.Metadata)
	}
	if artifact.Metadata["generated_by"] != "get_query_results" {
		t.Fatalf("generated_by=%#v want get_query_results", artifact.Metadata["generated_by"])
	}
	if got, ok := firstIntMapValue(artifact.Metadata, "row_count_total"); !ok || got != 2 {
		t.Fatalf("row_count_total=%v,%v want 2,true", got, ok)
	}
	queryMetadata, ok := artifact.Metadata["query_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("query_metadata missing: %#v", artifact.Metadata["query_metadata"])
	}
	if queryMetadata["job_id"] != job.ID || queryMetadata["artifact_id"] != artifactID {
		t.Fatalf("query_metadata=%#v, want job/artifact ids", queryMetadata)
	}
	if _, leaked := artifact.Metadata[jobs.QueryObligationManagedMetadataKey]; leaked {
		t.Fatalf("artifact metadata leaked internal marker: %#v", artifact.Metadata)
	}
	if _, leaked := queryMetadata[jobs.QueryObligationManagedMetadataKey]; leaked {
		t.Fatalf("artifact query metadata leaked internal marker: %#v", queryMetadata)
	}
	resultMetadata, ok := artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing: %#v", artifact.Metadata["result_metadata"])
	}
	if resultMetadata["integration"] != "query-lifecycle" ||
		resultMetadata["renderer_key"] != "query" ||
		resultMetadata["native_viewer"] != "query" {
		t.Fatalf("result_metadata=%#v, want canonical query metadata", resultMetadata)
	}
	if _, ok := resultMetadata["tool_presentation"]; !ok {
		t.Fatalf("result_metadata.tool_presentation missing: %#v", resultMetadata)
	}
	if _, ok := artifact.Metadata["tool_presentation"]; !ok {
		t.Fatalf("tool_presentation missing from artifact metadata: %#v", artifact.Metadata)
	}
	if artifact.ContentText == nil {
		t.Fatalf("materialized artifact content_text is nil")
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(*artifact.ContentText), &rows); err != nil {
		t.Fatalf("materialized rows JSON: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows len=%d want 2", len(rows))
	}
}

func waitForRegistryStatus(t *testing.T, reg *jobs.Registry, jobID string, owner jobs.JobOwner, want jobs.JobStatus) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap, ok := reg.Snapshot(jobID, owner)
		if ok && snap.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	snap, _ := reg.Snapshot(jobID, owner)
	if snap == nil {
		t.Fatalf("job %s did not reach %s; snapshot missing", jobID, want)
	}
	t.Fatalf("job %s status=%s want %s", jobID, snap.Status, want)
}

type fakeQueryRegistry struct {
	jobs map[string]map[string]any
}

func (f *fakeQueryRegistry) Status(_ context.Context, jobID, _ string, _ string) (map[string]any, error) {
	v, ok := f.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	return cloneAny(v), nil
}

func (f *fakeQueryRegistry) Cancel(_ context.Context, jobID, _ string, _ string) (map[string]any, error) {
	v, ok := f.jobs[jobID]
	if !ok {
		return nil, store.ErrNotFound
	}
	v["status"] = "cancelled"
	return cloneAny(v), nil
}

func newQueryManagerHarness(t *testing.T) (*Manager, store.Store, *fakeQueryRegistry) {
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
	registry := &fakeQueryRegistry{jobs: map[string]map[string]any{}}
	manager := NewManager(
		st,
		cfg,
		providers.NewFactory(cfg, sec),
		tools.NewRegistry(nil),
		ps,
		nil,
		nil,
		nil,
		cfgDir,
		&Options{QueryJobs: registry},
	)
	return manager, st, registry
}

func cloneAny(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func anyString(v any) string {
	s, _ := v.(string)
	return s
}
