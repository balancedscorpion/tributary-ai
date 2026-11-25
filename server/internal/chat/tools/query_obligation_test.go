package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	chatstore "ai-analyst-local/internal/chat/store"
)

func TestRunQueryToolsAdvertiseOnlyStrictObligationAndRejectInvalidRetryIDs(t *testing.T) {
	toolsUnderTest := []Tool{
		newRunBigQueryQueryTool(),
		newRunDatabricksQueryTool(),
		newRunTrinoQueryTool(),
		newRunSnowflakeQueryTool(),
	}
	invalidFields := []string{
		`"query_obligation_id":null`,
		`"query_obligation_id":""`,
		`"query_obligation_id":"qob-00000000000000000000000000000000"`,
		`"query_obligation_id":"statement-native-123"`,
		`"query_obligation_id":"qj-managed-but-wrong-namespace"`,
		`"replaces_job_id":null`,
		`"replaces_job_id":""`,
		`"replaces_job_id":"qj-00000-00000"`,
		`"replaces_job_id":"statement-native-123"`,
	}
	for _, tool := range toolsUnderTest {
		definition := tool.Definition()
		properties, _ := definition.Function.Parameters["properties"].(map[string]any)
		if _, advertised := properties["replaces_job_id"]; advertised {
			t.Fatalf("%s still advertises replaces_job_id", definition.Function.Name)
		}
		obligationSchema, ok := properties["query_obligation_id"].(map[string]any)
		if !ok || obligationSchema["pattern"] != "^qob-[0-9a-f]{32}$" {
			t.Fatalf("%s obligation schema=%#v", definition.Function.Name, obligationSchema)
		}
		for _, field := range invalidFields {
			args := json.RawMessage(`{"sql":"select 1",` + field + `}`)
			out, err := tool.Execute(context.Background(), ExecContext{}, args)
			if err != nil {
				t.Fatalf("%s %s returned transport error: %v", definition.Function.Name, field, err)
			}
			result := asMap(t, out.Result)
			if out.OK || result["replacement_preflight_failed"] != true || result["replacement_wait_required"] != true {
				t.Fatalf("%s accepted/collapsed %s: output=%+v", definition.Function.Name, field, out)
			}
			if _, minted := result["job_id"]; minted {
				t.Fatalf("%s minted a new job for malformed retry %s: %#v", definition.Function.Name, field, result)
			}
		}
	}
}

func rejectSeededValidationQuery(t *testing.T, registry *jobs.Registry, repository chatstore.LongHorizonRepository, thread chatstore.Thread, run chatstore.Run, job *jobs.Job) {
	t.Helper()
	rejected, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected",
		"detail": "The original filter was incorrect.",
	}))
	if err != nil || !rejected.OK {
		t.Fatalf("reject original output=%+v err=%v", rejected, err)
	}
}

func TestManagedLaunchWithQueryObligationIDTargetsActiveAttempt(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	rejectSeededValidationQuery(t, registry, repository, thread, run, job)
	obligationID, obligationErr := chatstore.QueryObligationIDForRoot(run.ID, job.ID)
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}

	adapter := &budgetGateAdapter{}
	replacementRegistry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs: replacementRegistry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "obligation-call",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 2", SubmittedSQL: "select 2",
		Adapter: adapter, QueryObligationID: obligationID,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil || !out.OK {
		t.Fatalf("obligation replacement output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if _, leaked := result["replaces_job_id"]; leaked || result["query_obligation_id"] != obligationID {
		t.Fatalf("obligation replacement result=%#v", result)
	}
	if _, leaked := out.QueryMetadata[jobs.QueryObligationManagedMetadataKey]; leaked {
		t.Fatalf("internal obligation marker leaked in query metadata: %#v", out.QueryMetadata)
	}
	newJobID := strings.TrimSpace(anyToString(result["job_id"]))
	ownerToken := strings.TrimSpace(anyToString(result["owner_token"]))
	snapshot, ok := replacementRegistry.Snapshot(newJobID, jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: ownerToken})
	if !ok || snapshot.ReplacesJobID != job.ID {
		t.Fatalf("durable job=%#v, want canonical replaces_job_id=%s", snapshot, job.ID)
	}
	if adapter.submits.Load() != 1 {
		t.Fatalf("submits=%d, want exactly one", adapter.submits.Load())
	}
	waitForManagedRegistryTerminal(t, replacementRegistry, newJobID, jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: ownerToken})
}

func TestManagedLegacyReplacesJobIDForwardResolvesSupersededTarget(t *testing.T) {
	ctx := context.Background()
	repository, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	persistJob := func(id, replaces string) {
		t.Helper()
		if _, err := repository.UpsertQueryJobCAS(ctx, jobs.JobRecord{
			ID: id, Provider: "bigquery", Status: jobs.JobStatusFailed,
			Owner:         jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-" + id},
			ReplacesJobID: replaces, ValidationState: jobs.ValidationRejected,
			SubmittedAt: now, LastAccess: now, CompletedAt: &now,
		}, 0); err != nil {
			t.Fatalf("persist %s: %v", id, err)
		}
	}
	persistJob("qj-first", "")
	persistJob("qj-second", "qj-first")
	if _, err := repository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: run.ID, JobID: "qj-first", RequiredForCompletion: true, ValidationState: chatstore.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("seed first dependency: %v", err)
	}
	if _, err := repository.ReplaceRunQueryDependency(ctx, run.ID, "qj-first", chatstore.RunQueryDependency{
		RunID: run.ID, JobID: "qj-second", RequiredForCompletion: true, ValidationState: chatstore.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("supersede first attempt: %v", err)
	}
	wantObligation, obligationErr := chatstore.QueryObligationIDForRoot(run.ID, "qj-first")
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}
	resolution, err := resolveManagedQueryRetryTarget(ctx, repository, thread.ID, run.ID, wantObligation, "")
	if err != nil || resolution.Record.ID != "qj-second" || resolution.ObligationID != wantObligation {
		t.Fatalf("pre-launch obligation resolution=%#v err=%v", resolution, err)
	}

	// A stale legacy pointer at the superseded first attempt must
	// forward-resolve to the chain's active attempt instead of dead-ending.
	adapter := &budgetGateAdapter{}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	out, err := launchManagedQuery(ctx, ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "stale-pointer-call",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 3", SubmittedSQL: "select 3",
		Adapter: adapter, ReplacesJobID: "qj-first",
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil || !out.OK {
		t.Fatalf("stale-pointer replacement output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if _, leaked := result["replaces_job_id"]; leaked || result["query_obligation_id"] != wantObligation {
		t.Fatalf("result=%#v, want obligation-only public retry identity", result)
	}
	if adapter.submits.Load() != 1 {
		t.Fatalf("submits=%d, want exactly one", adapter.submits.Load())
	}

	// After the swap, the same stable handle follows the newly-bound attempt.
	newJobID := strings.TrimSpace(anyToString(result["job_id"]))
	resolution, err = resolveManagedQueryRetryTarget(ctx, repository, thread.ID, run.ID, wantObligation, "")
	if err == nil || !resolution.Active || resolution.Record.ID != newJobID || resolution.ObligationID != wantObligation {
		t.Fatalf("post-launch obligation resolution=%#v err=%v", resolution, err)
	}
	waitForManagedRegistryTerminal(t, registry, newJobID, jobs.JobOwner{
		ThreadID: thread.ID, RunID: run.ID, Handle: strings.TrimSpace(anyToString(result["owner_token"])),
	})
}

func TestManagedRetryTargetMismatchFailsClosed(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	rejectSeededValidationQuery(t, registry, repository, thread, run, job)
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := repository.UpsertQueryJobCAS(ctx, jobs.JobRecord{
		ID: "qj-unrelated", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-unrelated"},
		ValidationState: jobs.ValidationRejected,
		SubmittedAt:     now, LastAccess: now, CompletedAt: &now,
	}, 0); err != nil {
		t.Fatalf("persist unrelated job: %v", err)
	}

	adapter := &budgetGateAdapter{}
	obligationID, obligationErr := chatstore.QueryObligationIDForRoot(run.ID, job.ID)
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}
	out, err := launchManagedQuery(ctx, ExecContext{
		Jobs:        jobs.NewRegistry(jobs.RegistryOptions{Store: jobs.NewMemoryJobStore()}),
		LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "mismatch-call",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 4", SubmittedSQL: "select 4",
		Adapter:           adapter,
		QueryObligationID: obligationID,
		ReplacesJobID:     "qj-unrelated",
		Budgets:           jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil {
		t.Fatalf("launchManagedQuery: %v", err)
	}
	result := asMap(t, out.Result)
	if out.OK || result["replacement_preflight_failed"] != true {
		t.Fatalf("output=%+v, want fail-closed preflight on disagreeing retry targets", out)
	}
	if raw, marshalErr := json.Marshal(out); marshalErr != nil || strings.Contains(string(raw), "qj-unrelated") || strings.Contains(string(raw), obligationID) {
		t.Fatalf("mismatch preflight echoed an unverified target: err=%v payload=%s", marshalErr, raw)
	}
	if adapter.submits.Load() != 0 {
		t.Fatalf("submits=%d, mismatched retry targets must never reach the provider", adapter.submits.Load())
	}
}

func TestManagedReplacementUsesConcreteBindingAndBlocksDuplicateRetry(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	rejectSeededValidationQuery(t, registry, repository, thread, run, job)
	ctx := context.Background()
	obligationID, obligationErr := chatstore.QueryObligationIDForRoot(run.ID, job.ID)
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}

	winnerAdapter := &budgetGateAdapter{}
	winnerExec := ExecContext{
		Jobs:        jobs.NewRegistry(jobs.RegistryOptions{Store: repository}),
		LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "winner-call",
	}
	req := managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 5", SubmittedSQL: "select 5",
		Adapter: winnerAdapter, QueryObligationID: obligationID,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	}
	winner, err := launchManagedQuery(ctx, winnerExec, req)
	if err != nil || !winner.OK {
		t.Fatalf("winner output=%+v err=%v", winner, err)
	}
	if winnerAdapter.submits.Load() != 1 {
		t.Fatalf("winner submits=%d, want exactly one", winnerAdapter.submits.Load())
	}

	loserAdapter := &budgetGateAdapter{}
	loserReq := req
	loserReq.Adapter = loserAdapter
	loser, err := launchManagedQuery(ctx, ExecContext{
		Jobs:        jobs.NewRegistry(jobs.RegistryOptions{Store: repository}),
		LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "loser-call",
	}, loserReq)
	if err != nil {
		t.Fatalf("duplicate retry: %v", err)
	}
	loserResult := asMap(t, loser.Result)
	if loser.OK || loserResult["replacement_wait_required"] != true {
		t.Fatalf("duplicate output=%+v, want wait on already-bound successor", loser)
	}
	if loserAdapter.submits.Load() != 0 || loserAdapter.profiles.Load() != 0 {
		t.Fatalf("duplicate submits=%d profiles=%d, want zero provider work", loserAdapter.submits.Load(), loserAdapter.profiles.Load())
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil {
		t.Fatalf("list dependencies: %v", err)
	}
	active := 0
	for _, dependency := range dependencies {
		if dependency.ObligationID == obligationID && dependency.RequiredForCompletion && !dependency.Detached {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active attempts for %s=%d, want one", obligationID, active)
	}
	winnerResult := asMap(t, winner.Result)
	waitForManagedRegistryTerminal(t, winnerExec.Jobs, strings.TrimSpace(anyToString(winnerResult["job_id"])), jobs.JobOwner{
		ThreadID: thread.ID, RunID: run.ID, Handle: strings.TrimSpace(anyToString(winnerResult["owner_token"])),
	})
}

// The exact deleted-run edge: a managed job whose durable run was deleted
// (blank RunID tombstone) observed from a NEWER run via check_query_status.
// AttachOwner must not re-home it and no fabricated current-run obligation
// handle may appear anywhere in the status output.
func TestCheckQueryStatusNeverFabricatesObligationForDeletedRunJob(t *testing.T) {
	ctx := context.Background()
	repository, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	currentRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	tombstoned := jobs.JobRecord{
		ID: "qj-tombstone", Provider: "bigquery", Status: jobs.JobStatusRunning,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: "", Handle: "qown-tombstone"},
		SubmittedAt: now, LastAccess: now,
		Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, tombstoned, 0); err != nil {
		t.Fatalf("persist tombstoned job: %v", err)
	}

	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	out, err := newCheckQueryStatusTool().Execute(ctx, ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: currentRun.ID,
	}, mustJSON(t, map[string]any{"job_id": tombstoned.ID, "owner_token": tombstoned.Owner.Handle}))
	if err != nil || !out.OK {
		t.Fatalf("check_query_status output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["job_id"] != tombstoned.ID {
		t.Fatalf("status did not find the tombstoned job: %#v", result)
	}
	if value, fabricated := result["query_obligation_id"]; fabricated {
		t.Fatalf("status fabricated obligation %v for a deleted-run job", value)
	}
	snapshot, ok := registry.Snapshot(tombstoned.ID, jobs.JobOwner{ThreadID: thread.ID, RunID: currentRun.ID, Handle: tombstoned.Owner.Handle})
	if !ok || snapshot.Owner.RunID != "" {
		t.Fatalf("tombstoned job re-homed: ok=%v owner=%#v", ok, snapshot.Owner)
	}
	fabricated, err := chatstore.QueryObligationIDForRoot(currentRun.ID, tombstoned.ID)
	if err != nil {
		t.Fatalf("derive fabricated candidate: %v", err)
	}
	if raw, marshalErr := json.Marshal(out.Result); marshalErr == nil && strings.Contains(string(raw), fabricated) {
		t.Fatalf("status output leaked a current-run obligation for a deleted-run job: %s", raw)
	}
}

// Intact cross-run isolation (NOT a tombstone): run-old owns a live managed
// job with a valid ledger obligation. A later run-new in the same thread may
// observe it with the owner token, but no run-old obligation handle may leak
// into run-new's status or results output, while run-old keeps its handle.
func TestQueryStatusAndResultsNeverLeakOtherRunObligation(t *testing.T) {
	ctx := context.Background()
	repository, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	runOld, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun old: %v", err)
	}
	runNew, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun new: %v", err)
	}

	adapter := &budgetGateAdapter{}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	launched, err := launchManagedQuery(ctx, ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: runOld.ID, ToolCallID: "cross-run-launch",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 1", SubmittedSQL: "select 1",
		Adapter: adapter,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil || !launched.OK {
		t.Fatalf("launch output=%+v err=%v", launched, err)
	}
	launchResult := asMap(t, launched.Result)
	rootJobID := strings.TrimSpace(anyToString(launchResult["job_id"]))
	rootOwnerToken := strings.TrimSpace(anyToString(launchResult["owner_token"]))
	waitForManagedRegistryTerminal(t, registry, rootJobID, jobs.JobOwner{ThreadID: thread.ID, RunID: runOld.ID, Handle: rootOwnerToken})
	wantObligation, obligationErr := chatstore.QueryObligationIDForRoot(runOld.ID, rootJobID)
	if obligationErr != nil {
		t.Fatalf("derive owning obligation: %v", obligationErr)
	}
	root, ok := registry.Snapshot(rootJobID, jobs.JobOwner{ThreadID: thread.ID, RunID: runOld.ID, Handle: rootOwnerToken})
	if !ok {
		t.Fatalf("snapshot root job")
	}
	rejectSeededValidationQuery(t, registry, repository, thread, runOld, root)

	// Observe the replacement rather than the root: replacement job metadata
	// deliberately carries the owning run's qob, which is the leak-prone case.
	replacement, err := launchManagedQuery(ctx, ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: runOld.ID, ToolCallID: "cross-run-replacement",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 2", SubmittedSQL: "select 2",
		Adapter: adapter, QueryObligationID: wantObligation,
		Metadata: map[string]any{
			"nested": map[string]any{"query_obligation_id": wantObligation},
		},
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil || !replacement.OK {
		t.Fatalf("replacement output=%+v err=%v", replacement, err)
	}
	replacementResult := asMap(t, replacement.Result)
	jobID := strings.TrimSpace(anyToString(replacementResult["job_id"]))
	ownerToken := strings.TrimSpace(anyToString(replacementResult["owner_token"]))
	waitForManagedRegistryTerminal(t, registry, jobID, jobs.JobOwner{ThreadID: thread.ID, RunID: runOld.ID, Handle: ownerToken})
	replacementSnapshot, ok := registry.Snapshot(jobID, jobs.JobOwner{ThreadID: thread.ID, RunID: runOld.ID, Handle: ownerToken})
	if !ok || replacementSnapshot.Metadata["query_obligation_id"] != wantObligation {
		t.Fatalf("replacement did not retain owning-run obligation in durable metadata: %#v", replacementSnapshot)
	}

	statusFor := func(runID string) Output {
		t.Helper()
		out, statusErr := newCheckQueryStatusTool().Execute(ctx, ExecContext{
			Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: runID,
		}, mustJSON(t, map[string]any{"job_id": jobID, "owner_token": ownerToken}))
		if statusErr != nil || !out.OK {
			t.Fatalf("check_query_status run=%s output=%+v err=%v", runID, out, statusErr)
		}
		return out
	}

	oldStatusOut := statusFor(runOld.ID)
	oldStatus := asMap(t, oldStatusOut.Result)
	if oldStatus["query_obligation_id"] != wantObligation {
		t.Fatalf("owning run status obligation=%v, want ledger-verified %q", oldStatus["query_obligation_id"], wantObligation)
	}

	newStatus := statusFor(runNew.ID)
	if raw, marshalErr := json.Marshal(newStatus); marshalErr != nil || strings.Contains(string(raw), "qob-") {
		t.Fatalf("run-new status leaked an obligation handle: err=%v payload=%s", marshalErr, raw)
	}

	results, resultsErr := newGetQueryResultsTool().Execute(ctx, ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: runNew.ID, DatabaseQueryRowLimit: 100,
	}, mustJSON(t, map[string]any{"job_id": jobID, "owner_token": ownerToken}))
	if resultsErr != nil {
		t.Fatalf("get_query_results: %v", resultsErr)
	}
	if raw, marshalErr := json.Marshal(results.Result); marshalErr != nil || strings.Contains(string(raw), "qob-") {
		t.Fatalf("run-new results leaked an obligation handle: err=%v payload=%s", marshalErr, raw)
	}
	if raw, marshalErr := json.Marshal(results.QueryMetadata); marshalErr == nil && strings.Contains(string(raw), "qob-") {
		t.Fatalf("run-new result metadata leaked an obligation handle: %s", raw)
	}
	if raw, marshalErr := json.Marshal(results.ResultMetadata); marshalErr == nil && strings.Contains(string(raw), "qob-") {
		t.Fatalf("run-new result presentation metadata leaked an obligation handle: %s", raw)
	}
	if results.Artifact == nil {
		t.Fatal("get_query_results did not produce its deterministic artifact")
	}
	if raw, marshalErr := json.Marshal(results.Artifact.Metadata); marshalErr != nil || strings.Contains(string(raw), "qob-") {
		t.Fatalf("deterministic artifact metadata leaked an obligation handle: err=%v payload=%s", marshalErr, raw)
	}
	if strings.Contains(results.Artifact.Content, "qob-") {
		t.Fatalf("deterministic artifact content leaked an obligation handle: %s", results.Artifact.Content)
	}

	// If the later run rejects the same completed result, it gets its own
	// ledger-scoped obligation. Instructions and plans must use that verified
	// handle, never the stale handle retained in the job's owning-run metadata.
	validated, validationErr := newValidateQueryResultTool().Execute(ctx, ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: runNew.ID,
	}, mustJSON(t, map[string]any{
		"job_id": jobID, "owner_token": ownerToken, "verdict": "rejected", "detail": "later-run correction required",
	}))
	if validationErr != nil || !validated.OK {
		t.Fatalf("later-run validation output=%+v err=%v", validated, validationErr)
	}
	validationResult := asMap(t, validated.Result)
	newObligation := strings.TrimSpace(anyToString(validationResult["query_obligation_id"]))
	if chatstore.ValidateQueryObligationID(newObligation) != nil || newObligation == wantObligation {
		t.Fatalf("later-run obligation=%q, old=%q output=%#v", newObligation, wantObligation, validationResult)
	}
	if raw, marshalErr := json.Marshal(validated); marshalErr != nil || strings.Contains(string(raw), wantObligation) || !strings.Contains(string(raw), newObligation) {
		t.Fatalf("later-run validation used stale retry identity: err=%v payload=%s", marshalErr, raw)
	}

	// The owning run still resolves its own handle after the cross-run reads.
	if again := asMap(t, statusFor(runOld.ID).Result); again["query_obligation_id"] != wantObligation {
		t.Fatalf("owning run lost its obligation after cross-run observation: %v", again["query_obligation_id"])
	}
}

type reserveFailingRepository struct {
	chatstore.LongHorizonRepository
	fail atomic.Bool
}

func (r *reserveFailingRepository) ReserveQueryCapacity(ctx context.Context, record jobs.JobRecord, limits jobs.CapacityLimits) (bool, int64, error) {
	if r.fail.Load() {
		return false, 0, errors.New("simulated durable reservation outage")
	}
	return r.LongHorizonRepository.ReserveQueryCapacity(ctx, record, limits)
}

func (r *reserveFailingRepository) UpsertQueryJobCAS(ctx context.Context, record jobs.JobRecord, expectedRevision int64) (int64, error) {
	if r.fail.Load() {
		return 0, errors.New("simulated durable reservation outage")
	}
	return r.LongHorizonRepository.UpsertQueryJobCAS(ctx, record, expectedRevision)
}

// A retry that resolved a valid obligation but fails BEFORE any durable
// reservation must keep that obligation as the task's only identity: no new
// job, no provider work, and a retryable wait result carrying the same
// verified handle.
func TestManagedRetryLaunchFailureBeforeReservationPreservesObligation(t *testing.T) {
	ctx := context.Background()
	repository, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	now := time.Now().UTC()
	failed := jobs.JobRecord{
		ID: "qj-outage-root", Provider: "bigquery", Status: jobs.JobStatusFailed,
		Owner:           jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-outage"},
		ValidationState: jobs.ValidationRejected,
		SubmittedAt:     now, LastAccess: now, CompletedAt: &now,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, failed, 0); err != nil {
		t.Fatalf("persist failed root: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: run.ID, JobID: failed.ID, RequiredForCompletion: true, ValidationState: chatstore.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("seed dependency: %v", err)
	}
	obligationID, obligationErr := chatstore.QueryObligationIDForRoot(run.ID, failed.ID)
	if obligationErr != nil {
		t.Fatalf("derive obligation: %v", obligationErr)
	}

	failing := &reserveFailingRepository{LongHorizonRepository: repository}
	failing.fail.Store(true)
	adapter := &budgetGateAdapter{}
	out, err := launchManagedQuery(ctx, ExecContext{
		Jobs:        jobs.NewRegistry(jobs.RegistryOptions{Store: failing}),
		LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "outage-retry",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 2", SubmittedSQL: "select 2",
		Adapter: adapter, QueryObligationID: obligationID,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil {
		t.Fatalf("launchManagedQuery: %v", err)
	}
	result := asMap(t, out.Result)
	if out.OK || result["query_obligation_id"] != obligationID ||
		result["replacement_wait_required"] != true || result["replacement_preflight_retry_required"] != true {
		t.Fatalf("output=%+v, want obligation-preserving deferred retry", out)
	}
	if _, minted := result["job_id"]; minted {
		t.Fatalf("deferred retry minted a job identity: %#v", result)
	}
	for _, key := range []string{"attempted_retry_target", "replacement_target_job_id", "candidate_job_ids"} {
		if _, leaked := result[key]; leaked {
			t.Fatalf("deferred retry exposed %s: %#v", key, result)
		}
	}
	if adapter.submits.Load() != 0 || adapter.profiles.Load() != 0 {
		t.Fatalf("submits=%d profiles=%d, want zero provider work", adapter.submits.Load(), adapter.profiles.Load())
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != failed.ID || dependencies[0].Detached {
		t.Fatalf("ledger changed by failed retry: %#v err=%v", dependencies, err)
	}
}

func TestManagedQueryObligationMetadataFallbackNeverCrossesRunBoundary(t *testing.T) {
	obligationID := "qob-11111111111111111111111111111111"
	job := &jobs.Job{
		ID:    "qj-repoless-replacement",
		Owner: jobs.JobOwner{ThreadID: "thread", RunID: "run-old", Handle: "qown-repoless"},
		Metadata: map[string]any{
			jobs.QueryObligationManagedMetadataKey: true,
			"query_obligation_id":                  obligationID,
		},
		ReplacesJobID: "qj-repoless-root",
	}
	if got := managedQueryObligationID(context.Background(), nil, "run-old", job, nil); got != obligationID {
		t.Fatalf("owning-run fallback=%q, want %q", got, obligationID)
	}
	if got := managedQueryObligationID(context.Background(), nil, "run-new", job, nil); got != "" {
		t.Fatalf("cross-run repo-less fallback leaked %q", got)
	}
	if got := managedQueryObligationID(context.Background(), nil, "", job, nil); got != "" {
		t.Fatalf("unscoped repo-less fallback leaked %q", got)
	}
}
