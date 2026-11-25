package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	chatstore "ai-analyst-local/internal/chat/store"
)

type budgetGateAdapter struct {
	profiles atomic.Int64
	submits  atomic.Int64
}

// lifecycleOnlyObligationStore keeps profile/replay unit tests focused on the
// registry ordering contract. Production tests use the real chat store and
// assert the obligation ledger separately.
type lifecycleOnlyObligationStore struct {
	*jobs.MemoryJobStore
	bindErr error
}

func (s *lifecycleOnlyObligationStore) BindQueryObligation(context.Context, string) error {
	return s.bindErr
}

func newLifecycleOnlyObligationStore() *lifecycleOnlyObligationStore {
	return &lifecycleOnlyObligationStore{MemoryJobStore: jobs.NewMemoryJobStore()}
}

func TestManagedRootBindingFailureReturnsDurableAlternativeIdentity(t *testing.T) {
	store := newLifecycleOnlyObligationStore()
	store.bindErr = errors.New("obligation ledger temporarily unavailable")
	adapter := &budgetGateAdapter{}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: store})
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs: registry, ThreadID: "thread", RunID: "run", ToolCallID: "root-bind-failure",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 1", SubmittedSQL: "select 1",
		Adapter: adapter, Budgets: jobs.Budgets{MaxScanBytes: 1000},
	})
	if err != nil || out.OK {
		t.Fatalf("binding failure output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	jobID := strings.TrimSpace(anyToString(result["job_id"]))
	ownerToken := strings.TrimSpace(anyToString(result["owner_token"]))
	obligationID := strings.TrimSpace(anyToString(result["query_obligation_id"]))
	if jobID == "" || ownerToken == "" || chatstore.ValidateQueryObligationID(obligationID) != nil {
		t.Fatalf("failed root lost durable alternative identity: %#v", result)
	}
	if result["replacement_required"] != true || result["remote_submitted"] != false {
		t.Fatalf("failed root result=%#v want local replacement obligation", result)
	}
	job, ok := registry.Snapshot(jobID, jobs.JobOwner{ThreadID: "thread", RunID: "run", Handle: ownerToken})
	if !ok || job.Status != jobs.JobStatusCancelled || job.CancelOutcome != jobs.CancelOutcomeBeforeSubmit {
		t.Fatalf("durable failed root=%#v", job)
	}
	if adapter.profiles.Load() != 0 || adapter.submits.Load() != 0 {
		t.Fatalf("provider work after bind failure: profiles=%d submits=%d", adapter.profiles.Load(), adapter.submits.Load())
	}
}

func waitForManagedRegistryTerminal(t *testing.T, registry *jobs.Registry, jobID string, owner jobs.JobOwner) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, ok := registry.Snapshot(jobID, owner)
		if ok {
			switch snapshot.Status {
			case jobs.JobStatusCompleted, jobs.JobStatusFailed, jobs.JobStatusCancelled, jobs.JobStatusExpired, jobs.JobStatusOrphaned:
				// Terminal persistence is synchronous immediately after the
				// in-memory transition; leave it time to release its durable lease
				// before TempDir cleanup.
				time.Sleep(50 * time.Millisecond)
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("managed job %s did not reach terminal state", jobID)
}

func (*budgetGateAdapter) Provider() string { return "bigquery" }
func (*budgetGateAdapter) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{IdempotentSubmit: true}
}
func (a *budgetGateAdapter) Submit(context.Context, jobs.SubmitRequest) (jobs.SubmitResult, error) {
	a.submits.Add(1)
	return jobs.SubmitResult{Handle: jobs.NativeHandle{Provider: "bigquery", ID: "unexpected"}}, nil
}
func (*budgetGateAdapter) Poll(context.Context, jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	return jobs.StatusSnapshot{State: jobs.ProviderStateSucceeded}, nil
}
func (*budgetGateAdapter) Fetch(context.Context, jobs.NativeHandle, jobs.FetchCursor) (jobs.ResultPage, error) {
	return jobs.ResultPage{}, nil
}
func (*budgetGateAdapter) Cancel(context.Context, jobs.NativeHandle) error { return nil }
func (*budgetGateAdapter) ConfirmCancel(context.Context, jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	return false, jobs.StatusSnapshot{}, nil
}
func (*budgetGateAdapter) Adopt(context.Context, jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	return jobs.AdoptOutcomeAdopted, jobs.StatusSnapshot{}, nil
}
func (a *budgetGateAdapter) Profile(context.Context, jobs.SubmitRequest) (jobs.QueryProfile, error) {
	a.profiles.Add(1)
	estimated := int64(101)
	return jobs.QueryProfile{Valid: true, EstimatedBytes: &estimated, EstimateIsExact: true}, nil
}

type profileContractAdapter struct {
	profile    jobs.QueryProfile
	profileErr error
	started    chan struct{}
	release    chan struct{}
	startOnce  sync.Once
	profiles   atomic.Int64
	submits    atomic.Int64
}

func (*profileContractAdapter) Provider() string { return "bigquery" }
func (*profileContractAdapter) Capabilities() jobs.ProviderCapabilities {
	return jobs.ProviderCapabilities{IdempotentSubmit: true}
}
func (a *profileContractAdapter) Submit(context.Context, jobs.SubmitRequest) (jobs.SubmitResult, error) {
	a.submits.Add(1)
	return jobs.SubmitResult{Handle: jobs.NativeHandle{Provider: "bigquery", ID: "profile-contract-job"}}, nil
}
func (*profileContractAdapter) Poll(context.Context, jobs.NativeHandle) (jobs.StatusSnapshot, error) {
	return jobs.StatusSnapshot{State: jobs.ProviderStateSucceeded}, nil
}
func (*profileContractAdapter) Fetch(context.Context, jobs.NativeHandle, jobs.FetchCursor) (jobs.ResultPage, error) {
	return jobs.ResultPage{}, nil
}
func (*profileContractAdapter) Cancel(context.Context, jobs.NativeHandle) error { return nil }
func (*profileContractAdapter) ConfirmCancel(context.Context, jobs.NativeHandle) (bool, jobs.StatusSnapshot, error) {
	return false, jobs.StatusSnapshot{}, nil
}
func (*profileContractAdapter) Adopt(context.Context, jobs.NativeHandle) (jobs.AdoptOutcome, jobs.StatusSnapshot, error) {
	return jobs.AdoptOutcomeAdopted, jobs.StatusSnapshot{}, nil
}
func (a *profileContractAdapter) Profile(ctx context.Context, _ jobs.SubmitRequest) (jobs.QueryProfile, error) {
	a.profiles.Add(1)
	if a.started != nil {
		a.startOnce.Do(func() { close(a.started) })
	}
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return jobs.QueryProfile{}, ctx.Err()
		}
	}
	return a.profile, a.profileErr
}

func TestManagedBigQueryBudgetGateRunsBeforeSubmit(t *testing.T) {
	adapter := &budgetGateAdapter{}
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs: jobs.NewRegistry(jobs.RegistryOptions{}), ThreadID: "thread", RunID: "run",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 1", SubmittedSQL: "select 1",
		Adapter: adapter, Budgets: jobs.Budgets{MaxScanBytes: 100, MaxResultRows: 10, MaxResultBytes: 1024},
		RequestedLimit: 10, PageSize: 10, ConfiguredLimit: 10,
	})
	if err != nil {
		t.Fatalf("launchManagedQuery: %v", err)
	}
	if out.OK || asMap(t, out.Result)["budget_exceeded"] != true {
		t.Fatalf("output=%+v, want pre-submit budget rejection", out)
	}
	if adapter.submits.Load() != 0 {
		t.Fatalf("submits=%d, budget gate must run before submit", adapter.submits.Load())
	}
}

func TestManagedProfileRejectionFormatsTypedDurableDiagnostics(t *testing.T) {
	estimated := int64(2048)
	tests := []struct {
		name    string
		status  jobs.JobStatus
		outcome jobs.ProfileOutcome
		code    string
	}{
		{"invalid", jobs.JobStatusFailed, jobs.ProfileOutcomeInvalid, "QUERY_PROFILE_INVALID"},
		{"over budget", jobs.JobStatusFailed, jobs.ProfileOutcomeOverBudget, "QUERY_PROFILE_OVER_BUDGET"},
		{"approval denied", jobs.JobStatusFailed, jobs.ProfileOutcomeApprovalDenied, "QUERY_SCAN_APPROVAL_NOT_GRANTED"},
		{"timed out", jobs.JobStatusFailed, jobs.ProfileOutcomeTimedOut, "QUERY_PROFILE_TIMED_OUT"},
		{"errored", jobs.JobStatusFailed, jobs.ProfileOutcomeErrored, "QUERY_PROFILE_ERRORED"},
		{"indeterminate", jobs.JobStatusOrphaned, jobs.ProfileOutcomeIndeterminate, "QUERY_PROFILE_INDETERMINATE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &jobs.Job{
				ID: "qj-original", Provider: "bigquery", Status: tc.status,
				Owner:       jobs.JobOwner{ThreadID: "thread", RunID: "run", Handle: "qown-original"},
				SubmittedAt: time.Now().UTC(), Error: "provider profile diagnostic",
				Profile:        &jobs.QueryProfile{Valid: false, Error: "provider profile diagnostic", EstimatedBytes: &estimated, EstimateIsExact: true},
				ProfileOutcome: tc.outcome, ProfileDispatched: true,
				Budgets: jobs.Budgets{MaxScanBytes: 1024},
				// Every real managed launch persists this marker before provider
				// work; only marked jobs may surface an obligation handle.
				Metadata: map[string]any{jobs.QueryObligationManagedMetadataKey: true},
			}
			out := formatManagedQueryLaunch(context.Background(), ExecContext{
				Jobs: jobs.NewRegistry(jobs.RegistryOptions{}), ThreadID: "thread", RunID: "run",
			}, managedQueryLaunchRequest{
				ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select * from large_table",
			}, "bigquery", nil, job, jobs.ErrProfileRejected)
			result := asMap(t, out.Result)
			if out.OK || result["job_id"] != job.ID || result["owner_token"] != job.Owner.Handle {
				t.Fatalf("output=%+v, want original durable identity on typed rejection", out)
			}
			if result["profile_outcome"] != string(tc.outcome) || result["remote_submitted"] != false || result["replacement_required"] != true {
				t.Fatalf("result=%#v, want rejection telemetry", result)
			}
			plan := asMap(t, result["alternative_plan"])
			obligationID, obligationErr := chatstore.QueryObligationIDForRoot(job.Owner.RunID, job.ID)
			if obligationErr != nil || plan["query_obligation_id"] != obligationID {
				t.Fatalf("alternative_plan=%#v obligation_err=%v, want stable obligation %q", plan, obligationErr, obligationID)
			}
			if _, leaked := plan["replaces_job_id"]; leaked {
				t.Fatalf("alternative_plan leaked internal predecessor identity: %#v", plan)
			}
			diagnostic := asMap(t, out.ResultMetadata["diagnostic"])
			if diagnostic["code"] != tc.code {
				t.Fatalf("diagnostic=%#v, want code %s", diagnostic, tc.code)
			}
		})
	}
}

func TestManagedProfileReplayKeepsOriginalIdentityAndProfilesExactlyOnce(t *testing.T) {
	adapter := &profileContractAdapter{
		profile: jobs.QueryProfile{Valid: false, Error: "unknown column"},
	}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: newLifecycleOnlyObligationStore()})
	exec := ExecContext{
		Jobs: registry, ThreadID: "thread", RunID: "run", ToolCallID: "same-call",
	}
	req := managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select missing from t",
		SubmittedSQL: "select missing from t", Adapter: adapter,
		Budgets: jobs.Budgets{MaxScanBytes: 1000},
	}
	first, err := launchManagedQuery(context.Background(), exec, req)
	if err != nil || first.OK {
		t.Fatalf("first output=%+v err=%v", first, err)
	}
	second, err := launchManagedQuery(context.Background(), exec, req)
	if err != nil || second.OK {
		t.Fatalf("replay output=%+v err=%v", second, err)
	}
	firstResult, secondResult := asMap(t, first.Result), asMap(t, second.Result)
	if firstResult["job_id"] != secondResult["job_id"] || firstResult["owner_token"] != secondResult["owner_token"] {
		t.Fatalf("replay changed durable identity: first=%#v second=%#v", firstResult, secondResult)
	}
	if adapter.profiles.Load() != 1 || adapter.submits.Load() != 0 {
		t.Fatalf("profiles=%d submits=%d, want one profile and no submit", adapter.profiles.Load(), adapter.submits.Load())
	}

	exec.ToolCallID = "fresh-call"
	third, err := launchManagedQuery(context.Background(), exec, req)
	if err != nil || third.OK {
		t.Fatalf("fresh output=%+v err=%v", third, err)
	}
	thirdResult := asMap(t, third.Result)
	if thirdResult["job_id"] == firstResult["job_id"] || adapter.profiles.Load() != 2 {
		t.Fatalf("fresh attempt did not create/profile a new job: first=%#v third=%#v profiles=%d", firstResult, thirdResult, adapter.profiles.Load())
	}
}

func TestManagedAcceptedProfileSubmitsExactlyOnce(t *testing.T) {
	estimated := int64(100)
	adapter := &profileContractAdapter{profile: jobs.QueryProfile{
		Valid: true, EstimatedBytes: &estimated, EstimateIsExact: true,
	}}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: newLifecycleOnlyObligationStore()})
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs: registry, ThreadID: "thread", RunID: "run", ToolCallID: "accepted-call",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 1", SubmittedSQL: "select 1",
		Adapter: adapter, Budgets: jobs.Budgets{MaxScanBytes: 1000},
	})
	if err != nil || !out.OK {
		t.Fatalf("accepted output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["profile_outcome"] != string(jobs.ProfileOutcomeAccepted) || result["remote_submitted"] != true {
		t.Fatalf("accepted result=%#v", result)
	}
	if adapter.profiles.Load() != 1 || adapter.submits.Load() != 1 {
		t.Fatalf("profiles=%d submits=%d, want exactly one each", adapter.profiles.Load(), adapter.submits.Load())
	}
}

func TestManagedLaunchKeepsRecoverableStatesInsteadOfRequestingReplacement(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name   string
		status jobs.JobStatus
		handle jobs.NativeHandle
		err    error
		want   string
	}{
		{
			name: "submitted handle with monitor handoff recovery", status: jobs.JobStatusSubmitted,
			handle: jobs.NativeHandle{Provider: "databricks", ID: "stmt-live"}, err: jobs.ErrDriverLeaseUnavailable,
			want: "running",
		},
		{
			name: "ambiguous submission", status: jobs.JobStatusSubmissionUnknown,
			err: jobs.ErrSubmissionUnknown, want: string(jobs.JobStatusSubmissionUnknown),
		},
		{
			name: "reserved driver recovery", status: jobs.JobStatusReserved,
			err: jobs.ErrDriverLeaseUnavailable, want: string(jobs.JobStatusReserved),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			job := &jobs.Job{
				ID: "qj-recoverable", Provider: "databricks", Status: tc.status, Handle: tc.handle,
				Owner:       jobs.JobOwner{ThreadID: "thread", RunID: "run", Handle: "qown-live"},
				SubmittedAt: now, SubmitDispatched: tc.status != jobs.JobStatusReserved,
			}
			out := formatManagedQueryLaunch(context.Background(), ExecContext{Jobs: jobs.NewRegistry(jobs.RegistryOptions{})}, managedQueryLaunchRequest{
				ToolName: "run_databricks_query", Provider: "databricks", SQL: "select 1",
			}, "databricks", nil, job, tc.err)
			result := asMap(t, out.Result)
			if !out.OK || result["status"] != tc.want || result["replacement_required"] == true {
				t.Fatalf("recoverable output=%+v", out)
			}
			if result["monitor_recovery_pending"] != true {
				t.Fatalf("recoverable result=%#v, want monitor_recovery_pending", result)
			}
			if tc.status == jobs.JobStatusSubmissionUnknown && result["submission_uncertain"] != true {
				t.Fatalf("ambiguous submission result=%#v, want submission_uncertain", result)
			}
		})
	}

	failed := &jobs.Job{
		ID: "qj-failed", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: "thread", RunID: "run", Handle: "qown-failed"},
		SubmittedAt: now, SubmitDispatched: true, Error: "provider rejected request",
	}
	out := formatManagedQueryLaunch(context.Background(), ExecContext{Jobs: jobs.NewRegistry(jobs.RegistryOptions{})}, managedQueryLaunchRequest{
		ToolName: "run_databricks_query", Provider: "databricks", SQL: "select broken",
	}, "databricks", nil, failed, nil)
	failedResult := asMap(t, out.Result)
	if out.OK || failedResult["replacement_required"] != true || failedResult["submission_uncertain"] != false {
		t.Fatalf("terminal failed output=%+v, want replacement", out)
	}

	cancelled := &jobs.Job{
		ID: "qj-cancelled-replay", Provider: "databricks", Status: jobs.JobStatusCancelled,
		Owner: jobs.JobOwner{ThreadID: "thread", RunID: "run", Handle: "qown-cancelled"}, SubmittedAt: now,
	}
	out = formatManagedQueryLaunch(context.Background(), ExecContext{Jobs: jobs.NewRegistry(jobs.RegistryOptions{})}, managedQueryLaunchRequest{
		ToolName: "run_databricks_query", Provider: "databricks", SQL: "select cancelled",
	}, "databricks", nil, cancelled, nil)
	cancelledResult := asMap(t, out.Result)
	if out.OK || cancelledResult["replacement_required"] != true || !strings.Contains(strings.ToLower(fmt.Sprint(cancelledResult["error"])), "cancelled") {
		t.Fatalf("terminal cancelled replay output=%+v, want safe replacement without panic", out)
	}
}

func TestManagedBigQueryBytesBilledFailureRequestsReprofiledReplacement(t *testing.T) {
	job := &jobs.Job{
		ID:       "qj-budget-drift",
		Provider: "bigquery",
		Status:   jobs.JobStatusFailed,
		Owner: jobs.JobOwner{
			ThreadID: "thread",
			RunID:    "run",
			Handle:   "qown-budget-drift",
		},
		SubmittedAt:      time.Now().UTC(),
		SubmitDispatched: true,
		Error:            "BigQuery rejected the query because the authorized maximumBytesBilled cap was 213534213421 bytes, while the current provider estimate requires at least 213634712423 bytes.",
		Metadata: map[string]any{
			jobs.QueryObligationManagedMetadataKey: true,
		},
	}
	out := formatManagedQueryLaunch(context.Background(), ExecContext{
		Jobs: jobs.NewRegistry(jobs.RegistryOptions{}),
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query",
		Provider: "bigquery",
		SQL:      "select * from large_table",
	}, "bigquery", nil, job, nil)
	result := asMap(t, out.Result)
	if out.OK || result["retryable"] != true || result["replacement_required"] != true {
		t.Fatalf("output=%+v, want retryable linked replacement", out)
	}
	if result["rewrite_required"] != false || result["scan_budget_reprofile_required"] != true {
		t.Fatalf("result=%#v, want same-SQL re-profile path", result)
	}
	if instructions := fmt.Sprint(result["instructions"]); !strings.Contains(instructions, "same SQL") ||
		!strings.Contains(instructions, "supplemental approval") ||
		!strings.Contains(instructions, "Do not abandon") {
		t.Fatalf("instructions=%q", instructions)
	}
	diagnostic := asMap(t, out.ResultMetadata["diagnostic"])
	if diagnostic["code"] != "QUERY_SCAN_BUDGET_ESTIMATE_CHANGED" || diagnostic["kind"] != "budget" {
		t.Fatalf("diagnostic=%#v", diagnostic)
	}
}

func TestManagedProfilingReplayReturnsOriginalPendingJobWithoutReprofile(t *testing.T) {
	estimated := int64(100)
	adapter := &profileContractAdapter{
		profile: jobs.QueryProfile{Valid: true, EstimatedBytes: &estimated},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: newLifecycleOnlyObligationStore()})
	exec := ExecContext{Jobs: registry, ThreadID: "thread", RunID: "run", ToolCallID: "pending-call"}
	req := managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 1", SubmittedSQL: "select 1",
		Adapter: adapter, Budgets: jobs.Budgets{MaxScanBytes: 1000},
	}
	firstDone := make(chan Output, 1)
	go func() {
		out, _ := launchManagedQuery(context.Background(), exec, req)
		firstDone <- out
	}()
	select {
	case <-adapter.started:
	case <-time.After(time.Second):
		t.Fatal("profile did not start")
	}
	pending, err := launchManagedQuery(context.Background(), exec, req)
	if err != nil || !pending.OK {
		t.Fatalf("pending replay output=%+v err=%v", pending, err)
	}
	pendingResult := asMap(t, pending.Result)
	if pendingResult["status"] != string(jobs.JobStatusProfiling) || pendingResult["profile_pending"] != true {
		t.Fatalf("pending replay=%#v", pendingResult)
	}
	if adapter.profiles.Load() != 1 || adapter.submits.Load() != 0 {
		t.Fatalf("pending replay profiles=%d submits=%d", adapter.profiles.Load(), adapter.submits.Load())
	}
	close(adapter.release)
	select {
	case first := <-firstDone:
		firstResult := asMap(t, first.Result)
		if pendingResult["job_id"] != firstResult["job_id"] || pendingResult["owner_token"] != firstResult["owner_token"] {
			t.Fatalf("pending replay changed identity: pending=%#v first=%#v", pendingResult, firstResult)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("original profile did not finish")
	}
}

func TestManagedReplacementPreflightBlocksNonReplaceableTargetBeforeSubmit(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	adapter := &budgetGateAdapter{}
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "replacement-call",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 2", SubmittedSQL: "select 2",
		Adapter: adapter, ReplacesJobID: job.ID,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil {
		t.Fatalf("launchManagedQuery: %v", err)
	}
	result := asMap(t, out.Result)
	if out.OK || result["replacement_preflight_failed"] != true {
		t.Fatalf("output=%+v, want replacement preflight failure", out)
	}
	if adapter.submits.Load() != 0 {
		t.Fatalf("submits=%d, invalid replacement must fail before submit", adapter.submits.Load())
	}
}

func TestManagedReplacementLinkFlowsIntoDurableJob(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	rejected, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected",
		"detail": "The original filter was incorrect.",
	}))
	if err != nil || !rejected.OK {
		t.Fatalf("reject original output=%+v err=%v", rejected, err)
	}

	adapter := &budgetGateAdapter{}
	replacementRegistry := jobs.NewRegistry(jobs.RegistryOptions{Store: repository})
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs: replacementRegistry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "replacement-call",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 2", SubmittedSQL: "select 2",
		Adapter: adapter, ReplacesJobID: job.ID,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil || !out.OK {
		t.Fatalf("replacement output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	newJobID := strings.TrimSpace(anyToString(result["job_id"]))
	ownerToken := strings.TrimSpace(anyToString(result["owner_token"]))
	if _, leaked := result["replaces_job_id"]; leaked || newJobID == "" || ownerToken == "" {
		t.Fatalf("replacement result=%#v", result)
	}
	snapshot, ok := replacementRegistry.Snapshot(newJobID, jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: ownerToken})
	if !ok || snapshot.ReplacesJobID != job.ID {
		t.Fatalf("replacement durable job=%#v, want replaces_job_id=%s", snapshot, job.ID)
	}
	if adapter.submits.Load() != 1 {
		t.Fatalf("submits=%d, want exactly one", adapter.submits.Load())
	}
	waitForManagedRegistryTerminal(t, replacementRegistry, newJobID, jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: ownerToken})
}

func TestManagedReplacementRejectsProviderNativeHandle(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	rejected, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected", "detail": "replace native alias",
	}))
	if err != nil || !rejected.OK || strings.TrimSpace(job.Handle.ID) == "" {
		t.Fatalf("reject original output=%+v err=%v job=%#v", rejected, err, job)
	}
	dependencies, err := repository.ListRunQueryDependencies(context.Background(), run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].JobID != job.ID {
		t.Fatalf("seed dependency=%#v err=%v job=%s", dependencies, err, job.ID)
	}

	adapter := &budgetGateAdapter{}
	out, err := launchManagedQuery(context.Background(), ExecContext{
		Jobs:        jobs.NewRegistry(jobs.RegistryOptions{Store: jobs.NewMemoryJobStore()}),
		LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, ToolCallID: "native-alias-replacement",
	}, managedQueryLaunchRequest{
		ToolName: "run_bigquery_query", Provider: "bigquery", SQL: "select 2", SubmittedSQL: "select 2",
		Adapter: adapter, ReplacesJobID: job.Handle.ID,
		Budgets: jobs.Budgets{MaxScanBytes: 1000, MaxResultRows: 10, MaxResultBytes: 1024},
	})
	if err != nil {
		t.Fatalf("native handle replacement: %v", err)
	}
	result := asMap(t, out.Result)
	if out.OK || result["replacement_preflight_failed"] != true || result["replacement_wait_required"] != true {
		t.Fatalf("native handle replacement output=%+v, want fail-closed preflight", out)
	}
	if adapter.submits.Load() != 0 {
		t.Fatalf("submits=%d, native handle must never reach provider", adapter.submits.Load())
	}
}

func TestManagedReplacementActiveTargetRequiresWait(t *testing.T) {
	_, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	resolution, err := resolveManagedQueryReplacementTarget(context.Background(), repository, thread.ID, run.ID, job.ID)
	if err == nil || !resolution.Active || resolution.Record.ID != job.ID {
		t.Fatalf("active resolution=%#v err=%v", resolution, err)
	}
	out := formatManagedQueryReplacementPreflightFailure(resolution)
	result := asMap(t, out.Result)
	if out.OK || result["replacement_wait_required"] != true || result["job_id"] != job.ID {
		t.Fatalf("active preflight output=%+v", out)
	}
	for _, key := range []string{"attempted_retry_target", "replacement_target_job_id", "candidate_job_ids"} {
		if _, leaked := result[key]; leaked {
			t.Fatalf("active preflight exposed %s: %#v", key, result)
		}
	}
}

func TestManagedReplacementPreflightDoesNotExposeUnverifiedCandidates(t *testing.T) {
	resolution := managedQueryReplacementResolution{
		SelectionRequired: true,
		CandidateJobIDs:   []string{"qj-private-candidate-a", "qj-private-candidate-b"},
	}
	out := formatManagedQueryReplacementPreflightFailure(resolution)
	result := asMap(t, out.Result)
	if out.OK || result["replacement_wait_required"] != true {
		t.Fatalf("selection preflight output=%+v", out)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	for _, forbidden := range []string{"attempted_retry_target", "replacement_target_job_id", "candidate_job_ids", "qj-private-candidate"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("selection preflight exposed %q: %s", forbidden, raw)
		}
	}
}

func TestManagedReplacementResolverNeverCrossesRunBoundary(t *testing.T) {
	ctx := context.Background()
	repository, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	oldRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun old: %v", err)
	}
	currentRun, err := repository.CreateRun(ctx, thread.ID, "analysis", "model", nil, "prompt", 8)
	if err != nil {
		t.Fatalf("CreateRun current: %v", err)
	}
	now := time.Now().UTC()
	old := jobs.JobRecord{
		ID: "qj-old-run", Provider: "databricks", Status: jobs.JobStatusFailed,
		Handle:      jobs.NativeHandle{Provider: "databricks", ID: "statement-old-run"},
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: oldRun.ID, Handle: "qown-old"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationRejected,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, old, 0); err != nil {
		t.Fatalf("persist old job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: oldRun.ID, JobID: old.ID, RequiredForCompletion: true, ValidationState: chatstore.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("persist old dependency: %v", err)
	}
	resolution, err := resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, old.ID)
	if err == nil || resolution.Active || strings.TrimSpace(resolution.Record.ID) != "" {
		t.Fatalf("cross-run resolution for %q=%#v err=%v", old.ID, resolution, err)
	}
	if nativeResolution, nativeErr := resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, old.Handle.ID); nativeErr == nil || nativeResolution.Record.ID != "" {
		t.Fatalf("native cross-run resolution=%#v err=%v, want rejection", nativeResolution, nativeErr)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: currentRun.ID, JobID: old.ID, RequiredForCompletion: true, ValidationState: chatstore.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("attach old job to current run: %v", err)
	}
	resolution, err = resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, old.ID)
	if err != nil || resolution.Active || resolution.Record.ID != old.ID {
		t.Fatalf("attached cross-run resolution for %q=%#v err=%v", old.ID, resolution, err)
	}
	if nativeResolution, nativeErr := resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, old.Handle.ID); nativeErr == nil || nativeResolution.Record.ID != "" {
		t.Fatalf("attached native alias resolution=%#v err=%v, want rejection", nativeResolution, nativeErr)
	}

	completedRejected := jobs.JobRecord{
		ID: "qj-old-run-completed-rejected", Provider: "databricks", Status: jobs.JobStatusCompleted,
		Handle:      jobs.NativeHandle{Provider: "databricks", ID: "statement-old-run-completed-rejected"},
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: oldRun.ID, Handle: "qown-old-completed-rejected"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationRejected,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, completedRejected, 0); err != nil {
		t.Fatalf("persist completed rejected old-run job: %v", err)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: currentRun.ID, JobID: completedRejected.ID, RequiredForCompletion: true, ValidationState: chatstore.QueryValidationRejected,
	}); err != nil {
		t.Fatalf("attach completed rejected job to current run: %v", err)
	}
	resolution, err = resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, completedRejected.ID)
	if err != nil || resolution.Active || resolution.Record.ID != completedRejected.ID {
		t.Fatalf("completed rejected cross-run resolution=%#v err=%v", resolution, err)
	}
	if nativeResolution, nativeErr := resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, completedRejected.Handle.ID); nativeErr == nil || nativeResolution.Record.ID != "" {
		t.Fatalf("completed rejected native alias=%#v err=%v, want rejection", nativeResolution, nativeErr)
	}

	active := jobs.JobRecord{
		ID: "qj-old-run-active", Provider: "databricks", Status: jobs.JobStatusRunning,
		Handle:      jobs.NativeHandle{Provider: "databricks", ID: "statement-old-run-active"},
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: oldRun.ID, Handle: "qown-old-active"},
		SubmittedAt: now, LastAccess: now, ValidationState: jobs.ValidationUnvalidated,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, active, 0); err != nil {
		t.Fatalf("persist active old-run job: %v", err)
	}
	resolution, err = resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, active.ID)
	if err == nil || !resolution.Active || resolution.Record.ID != active.ID {
		t.Fatalf("cross-run live resolution=%#v err=%v", resolution, err)
	}
	currentObligationID, obligationErr := chatstore.QueryObligationIDForRoot(currentRun.ID, active.ID)
	if obligationErr != nil {
		t.Fatalf("derive current-run observation obligation: %v", obligationErr)
	}
	if _, err := repository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: currentRun.ID, JobID: active.ID, RequiredForCompletion: true,
		ValidationState: chatstore.QueryValidationUnvalidated, ObligationID: currentObligationID,
	}); err != nil {
		t.Fatalf("attach active old-run job to current run: %v", err)
	}
	resolution, err = resolveManagedQueryRetryTarget(ctx, repository, thread.ID, currentRun.ID, currentObligationID, "")
	if err == nil || !resolution.Active || resolution.ObligationID != currentObligationID {
		t.Fatalf("cross-run attached live retry resolution=%#v err=%v", resolution, err)
	}
	out := formatManagedQueryReplacementPreflightFailure(resolution)
	result := asMap(t, out.Result)
	if result["replacement_wait_required"] != true || result["job_id"] != active.ID {
		t.Fatalf("cross-run live preflight=%#v", result)
	}
	if result["run_id"] != currentRun.ID || result["thread_id"] != thread.ID || result["query_obligation_id"] != currentObligationID {
		t.Fatalf("cross-run live preflight mixed owner and observer scope: %#v", result)
	}
	if result["run_id"] == oldRun.ID {
		t.Fatalf("cross-run live preflight exposed prior owner run as qob scope: %#v", result)
	}
	if nativeResolution, nativeErr := resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, currentRun.ID, active.Handle.ID); nativeErr == nil || nativeResolution.Record.ID != "" {
		t.Fatalf("cross-run live native alias=%#v err=%v, want rejection", nativeResolution, nativeErr)
	}
}

func TestManagedReplacementResolverRepairsMissingCurrentRunDependency(t *testing.T) {
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
	record := jobs.JobRecord{
		ID: "qj-missing-dependency", Provider: "databricks", Status: jobs.JobStatusFailed,
		Owner:       jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown-repair"},
		SubmittedAt: now, LastAccess: now, CompletedAt: &now, ValidationState: jobs.ValidationRejected,
	}
	if _, err := repository.UpsertQueryJobCAS(ctx, record, 0); err != nil {
		t.Fatalf("persist query job: %v", err)
	}

	resolution, err := resolveManagedQueryReplacementTarget(ctx, repository, thread.ID, run.ID, record.ID)
	if err != nil || resolution.Record.ID != record.ID {
		t.Fatalf("resolution=%#v err=%v", resolution, err)
	}
	dependencies, err := repository.ListRunQueryDependencies(ctx, run.ID)
	if err != nil || len(dependencies) != 1 {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if dependencies[0].JobID != record.ID || !dependencies[0].RequiredForCompletion || dependencies[0].Detached {
		t.Fatalf("repaired dependency=%#v", dependencies[0])
	}
}

type failingDependencyListRepository struct {
	chatstore.LongHorizonRepository
}

func (failingDependencyListRepository) ListRunQueryDependencies(context.Context, string) ([]chatstore.RunQueryDependency, error) {
	return nil, fmt.Errorf("temporary dependency ledger failure")
}

func TestManagedReplacementResolverWaitsOnTransientLedgerFailure(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	rejected, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected", "detail": "retry ledger read",
	}))
	if err != nil || !rejected.OK {
		t.Fatalf("reject original output=%+v err=%v", rejected, err)
	}

	resolution, err := resolveManagedQueryReplacementTarget(
		context.Background(), failingDependencyListRepository{repository}, thread.ID, run.ID, job.ID,
	)
	if err == nil || !resolution.Retryable {
		t.Fatalf("resolution=%#v err=%v", resolution, err)
	}
	out := formatManagedQueryReplacementPreflightFailure(resolution)
	result := asMap(t, out.Result)
	if out.OK || result["replacement_wait_required"] != true || result["replacement_preflight_retry_required"] != true {
		t.Fatalf("retryable preflight output=%+v", out)
	}
}

type conflictOnceValidationRepository struct {
	chatstore.LongHorizonRepository
	failFirst       bool
	attempts        atomic.Int64
	monitorReleased chan struct{}
	releaseOnce     sync.Once
}

type validationBoundaryFailureRepository struct {
	chatstore.LongHorizonRepository
	failDependencyUpserts int
	failEvidenceEvents    int
}

func (r *validationBoundaryFailureRepository) UpsertRunQueryDependency(ctx context.Context, dependency chatstore.RunQueryDependency) (chatstore.RunQueryDependency, error) {
	if r.failDependencyUpserts > 0 {
		r.failDependencyUpserts--
		return chatstore.RunQueryDependency{}, errors.New("injected validation dependency failure")
	}
	return r.LongHorizonRepository.UpsertRunQueryDependency(ctx, dependency)
}

func (r *validationBoundaryFailureRepository) AppendQueryJobEvent(ctx context.Context, event chatstore.QueryJobEvent) (chatstore.QueryJobEvent, error) {
	if event.Type == "validation_evidence" && r.failEvidenceEvents > 0 {
		r.failEvidenceEvents--
		return chatstore.QueryJobEvent{}, errors.New("injected validation evidence failure")
	}
	return r.LongHorizonRepository.AppendQueryJobEvent(ctx, event)
}

func (r *conflictOnceValidationRepository) ReleaseMonitorLease(ctx context.Context, jobID, holder string) error {
	err := r.LongHorizonRepository.ReleaseMonitorLease(ctx, jobID, holder)
	r.releaseOnce.Do(func() { close(r.monitorReleased) })
	return err
}

func (r *conflictOnceValidationRepository) UpdateQueryJobValidation(ctx context.Context, jobID string, state jobs.ValidationState, detail string, expectedRevision int64) (int64, error) {
	if r.attempts.Add(1) == 1 && r.failFirst {
		return 0, jobs.ErrRevisionConflict
	}
	return r.LongHorizonRepository.UpdateQueryJobValidation(ctx, jobID, state, detail, expectedRevision)
}

func seedDurableValidationQuery(t *testing.T, failFirstValidation bool, extraMetadata map[string]any) (*jobs.Registry, *conflictOnceValidationRepository, chatstore.Thread, chatstore.Run, *jobs.Job) {
	return seedDurableValidationQueryWithResult(t, failFirstValidation, extraMetadata, &jobs.QueryResult{
		Rows:   []map[string]any{{"n": 1}},
		Schema: []map[string]any{{"name": "n", "type": "INTEGER"}},
		Metadata: map[string]any{
			"terminal_status":   "completed",
			"materialized_rows": 1,
		},
	})
}

func seedDurableValidationQueryWithResult(t *testing.T, failFirstValidation bool, extraMetadata map[string]any, queryResult *jobs.QueryResult) (*jobs.Registry, *conflictOnceValidationRepository, chatstore.Thread, chatstore.Run, *jobs.Job) {
	t.Helper()
	ctx := context.Background()
	repository, err := chatstore.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := repository.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := repository.CreateRun(ctx, thread.ID, "anthropic", "claude", nil, "system", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	validationRepository := &conflictOnceValidationRepository{
		LongHorizonRepository: repository,
		failFirst:             failFirstValidation,
		monitorReleased:       make(chan struct{}),
	}
	registry := jobs.NewRegistry(jobs.RegistryOptions{Store: validationRepository})
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown_validation"}
	metadata := map[string]any{"provider_job_id": "bq-validation-1"}
	for key, value := range extraMetadata {
		metadata[key] = value
	}
	job, err := launchPreSubmittedCompatibility(
		ctx,
		registry,
		owner,
		"bigquery",
		"select 1 as n",
		"select 1 as n",
		metadata,
		func(context.Context) (bool, *jobs.QueryResult, error) {
			return true, queryResult, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("launch durable query: %v", err)
	}
	waitForStatus(t, registry, job.ID, thread.ID, owner.Handle, jobs.JobStatusCompleted)
	select {
	case <-validationRepository.monitorReleased:
	case <-time.After(time.Second):
		t.Fatal("managed query monitor did not release its durable lease")
	}
	if _, err := validationRepository.UpsertRunQueryDependency(ctx, chatstore.RunQueryDependency{
		RunID: run.ID, JobID: job.ID, RequiredForCompletion: true, ValidationState: chatstore.QueryValidationUnvalidated,
	}); err != nil {
		t.Fatalf("UpsertRunQueryDependency: %v", err)
	}
	snapshot, ok := registry.Snapshot(job.ID, owner)
	if !ok {
		t.Fatal("completed validation job missing")
	}
	return registry, validationRepository, thread, run, snapshot
}

func TestValidateQueryResultDurableCASDependencyAndStatus(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, true, nil)
	exec := ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID, DatabaseQueryRowLimit: 100,
	}
	args := minimalSuccessfulQueryValidationArgs(job)
	args["detail"] = "Schema and row count match the control total."
	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || !out.OK {
		t.Fatalf("validate output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["validation_state"] != chatstore.QueryValidationValidated || result["replacement_required"] != false {
		t.Fatalf("validation result=%#v", result)
	}
	publicRaw, marshalErr := json.Marshal(out)
	if marshalErr != nil || strings.Contains(string(publicRaw), "query_obligation_id") {
		t.Fatalf("validated result advertised a retired retry handle: err=%v payload=%s", marshalErr, publicRaw)
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "validate_query_result", "query_validation")
	if meta["category"] != string(ToolCategoryRead) || meta["readonly"] != true || meta["warehouse_readonly"] != true || meta["internal_validation_write"] != true {
		t.Fatalf("validation permission/result metadata=%#v", meta)
	}
	toolMeta, ok := ToolMetadataByName("validate_query_result")
	if !ok || toolMeta.Test.RequiresApproval {
		t.Fatalf("validation tool metadata=%#v, want internal read permission without approval", toolMeta)
	}
	if repository.attempts.Load() < 2 {
		t.Fatalf("CAS attempts=%d, want conflict retry", repository.attempts.Load())
	}
	record, found, err := repository.GetQueryJob(context.Background(), job.ID)
	if err != nil || !found || record.ValidationState != jobs.ValidationValidated || record.ValidationDetail != "Schema and row count match the control total." {
		t.Fatalf("durable record=%#v found=%v err=%v", record, found, err)
	}
	dependencies, err := repository.ListRunQueryDependencies(context.Background(), run.ID)
	if err != nil || len(dependencies) != 1 || dependencies[0].ValidationState != chatstore.QueryValidationValidated {
		t.Fatalf("dependencies=%#v err=%v", dependencies, err)
	}
	if obligationID := managedQueryObligationID(context.Background(), repository, run.ID, job, nil); obligationID != "" {
		t.Fatalf("validated dependency still exposed retry handle %q", obligationID)
	}
	events, err := repository.ListQueryJobEvents(context.Background(), job.ID, 0, 100)
	if err != nil {
		t.Fatalf("ListQueryJobEvents: %v", err)
	}
	foundEvidence := false
	for _, event := range events {
		if event.Type == "validation_evidence" {
			foundEvidence = true
		}
	}
	if !foundEvidence {
		t.Fatalf("validation evidence event missing: %#v", events)
	}

	statusOut, err := newCheckQueryStatusTool().Execute(context.Background(), exec, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle,
	}))
	if err != nil || asMap(t, statusOut.Result)["validation_state"] != chatstore.QueryValidationValidated {
		t.Fatalf("status output=%+v err=%v", statusOut, err)
	}
	resultsOut, err := newGetQueryResultsTool().Execute(context.Background(), exec, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle,
	}))
	if err != nil || !resultsOut.OK {
		t.Fatalf("get results=%+v err=%v", resultsOut, err)
	}
	results := asMap(t, resultsOut.Result)
	if results["validation_state"] != chatstore.QueryValidationValidated || results["retrieval_validates"] != false {
		t.Fatalf("retrieval validation fields=%#v", results)
	}
}

func TestCrossRunValidationAnchorsDependencyBeforeVerdictMutation(t *testing.T) {
	t.Run("anchor failure leaves verdict unchanged", func(t *testing.T) {
		registry, repository, thread, _, job := seedDurableValidationQuery(t, false, nil)
		baseStore, ok := repository.LongHorizonRepository.(chatstore.Store)
		if !ok {
			t.Fatal("validation repository does not expose chat store")
		}
		currentRun, err := baseStore.CreateRun(context.Background(), thread.ID, "anthropic", "claude", nil, "system", 8)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		failing := &validationBoundaryFailureRepository{LongHorizonRepository: repository, failDependencyUpserts: 1}
		out, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
			Jobs: registry, LongHorizon: failing, ThreadID: thread.ID, RunID: currentRun.ID,
		}, mustJSON(t, map[string]any{
			"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected",
			"detail": "The cross-run result used the wrong business filter.",
		}))
		if err != nil || out.OK || !strings.Contains(strings.ToLower(anyToString(asMap(t, out.Result)["error"])), "dependency") {
			t.Fatalf("anchor failure output=%+v err=%v", out, err)
		}
		record, found, readErr := repository.GetQueryJob(context.Background(), job.ID)
		if readErr != nil || !found || record.ValidationState != jobs.ValidationUnvalidated {
			t.Fatalf("verdict mutated before anchor: record=%#v found=%v err=%v", record, found, readErr)
		}
		dependencies, _ := repository.ListRunQueryDependencies(context.Background(), currentRun.ID)
		if len(dependencies) != 0 {
			t.Fatalf("failed anchor unexpectedly persisted dependency: %#v", dependencies)
		}
	})

	t.Run("evidence failure retains pre-verdict anchor", func(t *testing.T) {
		registry, repository, thread, _, job := seedDurableValidationQuery(t, false, nil)
		baseStore := repository.LongHorizonRepository.(chatstore.Store)
		currentRun, err := baseStore.CreateRun(context.Background(), thread.ID, "anthropic", "claude", nil, "system", 8)
		if err != nil {
			t.Fatalf("CreateRun: %v", err)
		}
		failing := &validationBoundaryFailureRepository{LongHorizonRepository: repository, failEvidenceEvents: 1}
		out, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
			Jobs: registry, LongHorizon: failing, ThreadID: thread.ID, RunID: currentRun.ID,
		}, mustJSON(t, map[string]any{
			"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected",
			"detail": "The cross-run result used the wrong business filter.",
		}))
		if err != nil || out.OK || !strings.Contains(strings.ToLower(anyToString(asMap(t, out.Result)["error"])), "evidence") {
			t.Fatalf("evidence failure output=%+v err=%v", out, err)
		}
		record, found, readErr := repository.GetQueryJob(context.Background(), job.ID)
		if readErr != nil || !found || record.ValidationState != jobs.ValidationRejected {
			t.Fatalf("rejected verdict was not durable: record=%#v found=%v err=%v", record, found, readErr)
		}
		dependencies, depErr := repository.ListRunQueryDependencies(context.Background(), currentRun.ID)
		if depErr != nil || len(dependencies) != 1 || dependencies[0].JobID != job.ID ||
			dependencies[0].ValidationState != chatstore.QueryValidationUnvalidated {
			t.Fatalf("pre-verdict anchor=%#v err=%v", dependencies, depErr)
		}
	})
}

func TestValidateQueryResultValidatedRequiresStructuredChecks(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	args := minimalSuccessfulQueryValidationArgs(job)
	delete(args, "checks")

	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || out.OK {
		t.Fatalf("empty-check validation output=%+v err=%v", out, err)
	}
	errorText := strings.ToLower(anyToString(asMap(t, out.Result)["error"]))
	if !strings.Contains(errorText, "checks cannot be empty") {
		t.Fatalf("empty-check validation error=%q", errorText)
	}
	assertQueryRemainsUnvalidated(t, repository, job.ID)
}

func TestValidateQueryResultValidatedRequiresEveryCheckToPass(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	args := minimalSuccessfulQueryValidationArgs(job)
	checks := args["checks"].([]map[string]any)
	checks[1]["passed"] = false
	checks[1]["detail"] = "Expected schema column was absent."

	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || out.OK {
		t.Fatalf("failed-check validation output=%+v err=%v", out, err)
	}
	errorText := strings.ToLower(anyToString(asMap(t, out.Result)["error"]))
	if !strings.Contains(errorText, "did not pass") || !strings.Contains(errorText, "rejected verdict") {
		t.Fatalf("failed-check validation error=%q", errorText)
	}
	assertQueryRemainsUnvalidated(t, repository, job.ID)
}

func TestValidateQueryResultValidatedDerivesMechanicalEvidence(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	args := minimalSuccessfulQueryValidationArgs(job)
	args["checks"] = []map[string]any{
		{"category": "business_logic", "name": "metric matches the requested definition", "passed": true},
	}
	delete(args, "evidence")

	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || !out.OK {
		t.Fatalf("server-derived evidence validation output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	recorded, ok := result["evidence"].([]queryValidationEvidence)
	if !ok {
		t.Fatalf("recorded evidence type=%T value=%#v", result["evidence"], result["evidence"])
	}
	byCategory := make(map[string]queryValidationEvidence, len(recorded))
	for _, item := range recorded {
		byCategory[item.Category] = item
	}
	availability := byCategory[queryValidationResultAvailability]
	if availability.ResultAvailable == nil || !*availability.ResultAvailable || availability.Source != "managed_query_registry" {
		t.Fatalf("derived availability evidence=%#v", availability)
	}
	schema := byCategory[queryValidationSchemaShape]
	if schema.ColumnCount == nil || *schema.ColumnCount != 1 || len(schema.Columns) != 1 || schema.Columns[0] != "n" {
		t.Fatalf("derived schema evidence=%#v", schema)
	}
	rows := byCategory[queryValidationRowCountMaterialization]
	if rows.RowCount == nil || *rows.RowCount != 1 || rows.MaterializationState == "" {
		t.Fatalf("derived row evidence=%#v", rows)
	}
}

func TestValidateQueryResultAllowsMultipleSemanticEvidencePerCategory(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	args := minimalSuccessfulQueryValidationArgs(job)
	args["checks"] = []map[string]any{
		{"category": "reconciliation", "name": "grand total reconciles", "passed": true},
		{"category": "reconciliation", "name": "segment totals reconcile", "passed": true},
	}
	args["evidence"] = []map[string]any{
		{
			"category": "reconciliation", "name": "grand total", "observed": "result total equals the control total",
			"expected": "control total", "source": "get_query_results",
		},
		{
			"category": "reconciliation", "name": "segment totals", "observed": "segments sum to the result total",
			"expected": "sum of segments equals total", "source": "get_query_results",
		},
	}

	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || !out.OK {
		t.Fatalf("multiple reconciliation evidence output=%+v err=%v", out, err)
	}
	recorded, ok := asMap(t, out.Result)["evidence"].([]queryValidationEvidence)
	if !ok {
		t.Fatalf("recorded evidence type=%T", asMap(t, out.Result)["evidence"])
	}
	names := map[string]bool{}
	for _, item := range recorded {
		if item.Category == "reconciliation" {
			names[item.Name] = true
		}
	}
	if !names["grand total"] || !names["segment totals"] || len(names) != 2 {
		t.Fatalf("reconciliation evidence=%#v, want both distinct observations", recorded)
	}
}

func TestValidateQueryResultRejectsDuplicateMechanicalEvidence(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	args := minimalSuccessfulQueryValidationArgs(job)
	evidence := args["evidence"].([]map[string]any)
	args["evidence"] = append(evidence, map[string]any{
		"category": "result_availability", "name": "second retrieval observation", "observed": "completed result returned",
		"source": "get_query_results", "result_available": true,
	})

	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || out.OK {
		t.Fatalf("duplicate mechanical evidence output=%+v err=%v", out, err)
	}
	errorText := strings.ToLower(anyToString(asMap(t, out.Result)["error"]))
	if !strings.Contains(errorText, "mechanical evidence category result_availability appears more than once") {
		t.Fatalf("duplicate mechanical evidence error=%q", errorText)
	}
	assertQueryRemainsUnvalidated(t, repository, job.ID)
}

func TestValidateQueryResultRecoveredMaterializationIgnoresStaleTransientErrors(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, map[string]any{
		"result_fetch_error":    "old transient fetch failure",
		"materialization_error": "old transient finalize failure",
	})
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	args := minimalSuccessfulQueryValidationArgs(job)
	args["checks"] = []map[string]any{
		{"category": "business_logic", "name": "metric matches the requested definition", "passed": true},
	}
	delete(args, "evidence")

	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
	if err != nil || !out.OK {
		t.Fatalf("recovered materialization validation output=%+v err=%v", out, err)
	}
}

func TestValidateQueryResultValidatedRejectsConflictingMechanicalEvidence(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(map[string]any)
		wantError string
	}{
		{
			name: "weak availability evidence",
			mutate: func(args map[string]any) {
				evidence := args["evidence"].([]map[string]any)
				delete(evidence[0], "result_available")
			},
			wantError: "result_available=true",
		},
		{
			name: "weak schema evidence",
			mutate: func(args map[string]any) {
				evidence := args["evidence"].([]map[string]any)
				delete(evidence[1], "columns")
			},
			wantError: "columns contains 0 names",
		},
		{
			name: "weak row materialization evidence",
			mutate: func(args map[string]any) {
				evidence := args["evidence"].([]map[string]any)
				delete(evidence[2], "materialization_state")
			},
			wantError: "must include materialization_state",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
			exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
			args := minimalSuccessfulQueryValidationArgs(job)
			tt.mutate(args)

			out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, args))
			if err != nil || out.OK {
				t.Fatalf("validation output=%+v err=%v", out, err)
			}
			errorText := strings.ToLower(anyToString(asMap(t, out.Result)["error"]))
			if !strings.Contains(errorText, strings.ToLower(tt.wantError)) {
				t.Fatalf("validation error=%q, want %q", errorText, tt.wantError)
			}
			assertQueryRemainsUnvalidated(t, repository, job.ID)
		})
	}
}

func TestValidateQueryResultRejectedRequiresReplacementWithoutCancelling(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	out, err := newValidateQueryResultTool().Execute(context.Background(), exec, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "rejected",
		"detail": "The date filter used the wrong timezone boundary.",
		"checks": []map[string]any{
			{"category": "filters", "name": "timezone_boundary", "passed": false, "detail": "Filter used the wrong boundary."},
		},
	}))
	if err != nil || !out.OK {
		t.Fatalf("reject output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["validation_state"] != chatstore.QueryValidationRejected || result["replacement_required"] != true {
		t.Fatalf("rejection result=%#v", result)
	}
	if !strings.Contains(strings.ToLower(anyToString(result["instructions"])), "replacement") {
		t.Fatalf("rejection lacks replacement plan: %#v", result)
	}
	if obligationID := strings.TrimSpace(anyToString(result["query_obligation_id"])); chatstore.ValidateQueryObligationID(obligationID) != nil {
		t.Fatalf("rejected result lost its active retry handle: %#v", result)
	}
	plan := asMap(t, result["alternative_plan"])
	if plan["action"] != "profile_and_run_replacement_query" || plan["cancel_completed_query"] != false {
		t.Fatalf("rejection alternative plan=%#v", plan)
	}
	snapshot, ok := registry.Snapshot(job.ID, jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: job.Owner.Handle})
	if !ok || snapshot.Status != jobs.JobStatusCompleted {
		t.Fatalf("rejection cancelled or mutated execution: %#v", snapshot)
	}
}

func minimalSuccessfulQueryValidationArgs(job *jobs.Job) map[string]any {
	materializationState := "retrievable"
	if job != nil {
		switch job.Materialization {
		case jobs.MaterializationComplete, jobs.MaterializationTruncated:
			materializationState = string(job.Materialization)
		}
	}
	return map[string]any{
		"job_id":      job.ID,
		"owner_token": job.Owner.Handle,
		"verdict":     "validated",
		"detail":      "Retrieved result availability, schema shape, and materialized row count all match.",
		"checks": []map[string]any{
			{"category": "result_availability", "name": "result is retrievable", "passed": true},
			{"category": "schema_shape", "name": "schema matches expected shape", "passed": true},
			{"category": "row_count_materialization", "name": "row count matches materialized result", "passed": true},
		},
		"evidence": []map[string]any{
			{
				"category": "result_availability", "name": "retrieval", "observed": "completed result returned",
				"source": "get_query_results", "result_available": true,
			},
			{
				"category": "schema_shape", "name": "retrieved schema", "observed": "one INTEGER column named n",
				"source": "get_query_results.schema", "column_count": 1, "columns": []string{"n"},
			},
			{
				"category": "row_count_materialization", "name": "materialized row count", "observed": "one stored row",
				"source": "get_query_results.result_counts", "row_count": 1, "materialization_state": materializationState,
			},
		},
	}
}

func assertQueryRemainsUnvalidated(t *testing.T, repository chatstore.LongHorizonRepository, jobID string) {
	t.Helper()
	record, ok, err := repository.GetQueryJob(context.Background(), jobID)
	if err != nil || !ok || record.ValidationState != jobs.ValidationUnvalidated {
		t.Fatalf("query validation mutated after rejected contract: record=%#v ok=%v err=%v", record, ok, err)
	}
}

func TestValidateQueryResultRejectsWrongOwner(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	tool := newValidateQueryResultTool()
	out, err := tool.Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": "qown_wrong", "verdict": "validated", "detail": "looks fine",
	}))
	if err != nil || out.OK || !strings.Contains(strings.ToLower(anyToString(asMap(t, out.Result)["error"])), "owner") {
		t.Fatalf("wrong owner output=%+v err=%v", out, err)
	}
}

func TestValidateQueryResultRequiresMaterializedOrRetrievableResult(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQueryWithResult(t, false, map[string]any{
		"materialization_error": "durable result fetch failed",
		"result_fetch_error":    "provider result fetch failed",
	}, &jobs.QueryResult{})
	out, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle, "verdict": "validated",
		"detail": "The returned row looks plausible.",
		"checks": []map[string]any{{
			"category": "business_logic", "name": "result answers the request", "passed": true,
		}},
	}))
	if err != nil || out.OK {
		t.Fatalf("unretrievable validation output=%+v err=%v", out, err)
	}
	if !strings.Contains(strings.ToLower(anyToString(asMap(t, out.Result)["error"])), "neither durably materialized nor safely retrievable") {
		t.Fatalf("unretrievable validation error=%#v", out.Result)
	}
	record, ok, readErr := repository.GetQueryJob(context.Background(), job.ID)
	if readErr != nil || !ok || record.ValidationState != jobs.ValidationUnvalidated {
		t.Fatalf("durable validation changed despite failed precondition: record=%#v ok=%v err=%v", record, ok, readErr)
	}
}

func TestValidateQueryResultRequiresCompletedExecution(t *testing.T) {
	registry, repository, thread, run, _ := seedDurableValidationQuery(t, false, nil)
	owner := jobs.JobOwner{ThreadID: thread.ID, RunID: run.ID, Handle: "qown_submitted_validation"}
	job, registerErr := registry.Register(owner, "bigquery", "select sleep", "select sleep", nil, nil, nil)
	if registerErr != nil {
		t.Fatalf("Register: %v", registerErr)
	}
	out, err := newValidateQueryResultTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": owner.Handle, "verdict": "validated",
		"detail": "Premature validation attempt.",
	}))
	if err != nil || out.OK {
		t.Fatalf("submitted validation output=%+v err=%v", out, err)
	}
	if !strings.Contains(strings.ToLower(anyToString(asMap(t, out.Result)["error"])), "requires completed execution") {
		t.Fatalf("submitted validation error=%#v", out.Result)
	}
	snapshot, ok := registry.Snapshot(job.ID, owner)
	if !ok || snapshot.Status != jobs.JobStatusSubmitted {
		t.Fatalf("validation mutated submitted job: %#v", snapshot)
	}
}

func TestGetQueryResultsNeverAutoValidatesDurableResult(t *testing.T) {
	registry, repository, thread, run, job := seedDurableValidationQuery(t, false, nil)
	exec := ExecContext{Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID}
	statusOut, statusErr := newCheckQueryStatusTool().Execute(context.Background(), exec, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle,
	}))
	if statusErr != nil || !statusOut.OK {
		t.Fatalf("status output=%+v err=%v", statusOut, statusErr)
	}
	statusResult := asMap(t, statusOut.Result)
	if statusResult["validation_state"] != chatstore.QueryValidationUnvalidated || statusResult["validation_required"] != true {
		t.Fatalf("status unexpectedly validated result: %#v", statusResult)
	}
	out, err := newGetQueryResultsTool().Execute(context.Background(), ExecContext{
		Jobs: registry, LongHorizon: repository, ThreadID: thread.ID, RunID: run.ID,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": job.Owner.Handle,
	}))
	if err != nil || !out.OK {
		t.Fatalf("get results output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["validation_state"] != chatstore.QueryValidationUnvalidated || result["retrieval_validates"] != false || result["validation_required"] != true {
		t.Fatalf("retrieval validation fields=%#v", result)
	}
	record, ok, readErr := repository.GetQueryJob(context.Background(), job.ID)
	if readErr != nil || !ok || record.ValidationState != jobs.ValidationUnvalidated || record.ValidationDetail != "" {
		t.Fatalf("retrieval mutated durable validation: record=%#v ok=%v err=%v", record, ok, readErr)
	}
	dependencies, dependencyErr := repository.ListRunQueryDependencies(context.Background(), run.ID)
	if dependencyErr != nil || len(dependencies) != 1 || dependencies[0].ValidationState != chatstore.QueryValidationUnvalidated {
		t.Fatalf("retrieval mutated dependency validation: dependencies=%#v err=%v", dependencies, dependencyErr)
	}
}

func requireQueryLifecycleResultMetadata(t *testing.T, out Output, toolName string, kind string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("result should not contain nested result_metadata: %#v", result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != "query-lifecycle" || meta["product"] != "query" || meta["family"] != "query-lifecycle" || meta["tool_name"] != toolName || meta["kind"] != kind {
		t.Fatalf("ResultMetadata=%#v, want %s %s metadata", meta, toolName, kind)
	}
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("missing metadata for %s", toolName)
	}
	if meta["tool_set_id"] != toolMeta.ToolSetID ||
		meta["category"] != string(toolMeta.Category) ||
		meta["origin"] != string(toolMeta.Origin) ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("ResultMetadata=%#v, want query lifecycle catalog metadata %#v", meta, toolMeta)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", meta)
	}
	return meta
}

func requireQueryLifecycleProviderLinks(t *testing.T, meta map[string]any, providerJobID string) {
	t.Helper()
	if meta["provider_job_id"] != providerJobID ||
		meta["warehouse_job_id"] != providerJobID ||
		meta["provider_job_url"] != "https://warehouse.example/jobs/"+providerJobID ||
		meta["statement_id"] != "stmt_"+providerJobID {
		t.Fatalf("ResultMetadata provider links=%#v, want provider job link fields for %s", meta, providerJobID)
	}
}

func TestCheckQueryStatusUnknownForMismatchedOwner(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	tool := newCheckQueryStatusTool()

	out, err := tool.Execute(context.Background(), ExecContext{
		Jobs:     reg,
		ThreadID: "thread-1",
		RunID:    "run-1",
	}, mustJSON(t, map[string]any{
		"job_id":      "missing-job",
		"owner_token": "tok_missing",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	result := asMap(t, out.Result)
	if result["status"] != "unknown" {
		t.Fatalf("status=%v want=unknown", result["status"])
	}
	if result["ui_suppress"] != true {
		t.Fatalf("ui_suppress=%v want=true", result["ui_suppress"])
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "check_query_status", "query_status")
	if meta["status"] != "unknown" || meta["found"] != false || meta["job_id"] != "missing-job" {
		t.Fatalf("ResultMetadata=%#v, want unknown not-found status", meta)
	}
}

func TestCheckQueryStatusIncludesProviderJobLinks(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		map[string]any{
			"provider_job_id":  "bq_status",
			"warehouse_job_id": "bq_status",
			"provider_job_url": "https://warehouse.example/jobs/bq_status",
			"statement_id":     "stmt_bq_status",
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	tool := newCheckQueryStatusTool()
	out, err := tool.Execute(context.Background(), ExecContext{
		Jobs:     reg,
		ThreadID: "thread-1",
		RunID:    "run-1",
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
	}))
	if err != nil {
		t.Fatalf("execute check_query_status: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "check_query_status", "query_status")
	requireQueryLifecycleProviderLinks(t, meta, "bq_status")
}

func TestCheckQueryStatusMakesBigQueryBudgetDriftRecoverable(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	owner := jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"}
	job, err := reg.Register(
		owner,
		"bigquery",
		"select * from large_table",
		"select * from large_table",
		nil,
		func(context.Context) (bool, *jobs.QueryResult, error) {
			return false, nil, errors.New(
				"BigQuery rejected the query because the authorized maximumBytesBilled cap was 213534213421 bytes, while the current provider estimate requires at least 213634712423 bytes.",
			)
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	waitForManagedRegistryTerminal(t, reg, job.ID, owner)

	out, err := newCheckQueryStatusTool().Execute(context.Background(), ExecContext{
		Jobs:     reg,
		ThreadID: owner.ThreadID,
		RunID:    owner.RunID,
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": owner.Handle,
	}))
	if err != nil || !out.OK {
		t.Fatalf("status output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["status"] != string(jobs.JobStatusFailed) ||
		result["retryable"] != true ||
		result["replacement_required"] != true ||
		result["rewrite_required"] != false ||
		result["scan_budget_reprofile_required"] != true {
		t.Fatalf("status result=%#v", result)
	}
	if instructions := fmt.Sprint(result["instructions"]); !strings.Contains(instructions, "same SQL") ||
		!strings.Contains(instructions, "fresh estimate") ||
		!strings.Contains(instructions, "do not retry the failed provider job ID") {
		t.Fatalf("instructions=%q", instructions)
	}
}

func TestQueryJobLifecycleDetailsIncludesProfilingTelemetry(t *testing.T) {
	estimated := int64(4096)
	job := &jobs.Job{
		ID: "qj-profiling", Status: jobs.JobStatusProfiling,
		SubmittedAt: time.Now().Add(-time.Second), LastAccess: time.Now(),
		Profile:        &jobs.QueryProfile{Valid: true, EstimatedBytes: &estimated, EstimateIsExact: true},
		ProfileOutcome: jobs.ProfileOutcomeNone,
		ProfileHandle:  jobs.NativeHandle{Provider: "databricks", ID: "profile-statement"},
		ProfilePhase:   "explaining", ProfileDispatched: true, SubmitDispatched: false,
		Metadata: map[string]any{"result_cursor": "internal-provider-cursor"},
	}
	details := queryJobLifecycleDetails(job)
	if details["active"] != true || details["remote_submitted"] != false || details["profile_dispatched"] != true {
		t.Fatalf("profiling details=%#v", details)
	}
	if details["profile_phase"] != "explaining" || details["query_profile"] != job.Profile || details["profile_handle"] == nil {
		t.Fatalf("profiling telemetry missing: %#v", details)
	}
	if _, exposed := details["result_cursor"]; exposed {
		t.Fatalf("lifecycle details exposed an internal provider cursor: %#v", details)
	}
	if !strings.Contains(strings.ToLower(queryStatusMessage(jobs.JobStatusProfiling, 1000)), "profiling") {
		t.Fatalf("profiling status message missing")
	}
}

func TestGetQueryResultsReturnsCompletedRows(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		map[string]any{
			"provider":         "bigquery",
			"provider_job_id":  "bq_results",
			"warehouse_job_id": "bq_results",
			"provider_job_url": "https://warehouse.example/jobs/bq_results",
			"statement_id":     "stmt_bq_results",
			// Legacy records may retain a consumable provider cursor. The
			// result tool must ignore it and page only through these rows.
			"result_cursor": "provider-page-must-not-be-replayed",
		},
		func(ctx context.Context) (bool, *jobs.QueryResult, error) {
			return true, &jobs.QueryResult{
				Rows: []map[string]any{
					{"n": 1},
					{"n": 2},
				},
				Schema: []map[string]any{
					{"name": "n", "type": "INTEGER"},
				},
				Metadata: map[string]any{
					"terminal_status": "completed",
					"state":           "DONE",
				},
			}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	waitForStatus(t, reg, job.ID, "thread-1", "tok_owner", jobs.JobStatusCompleted)

	getTool := newGetQueryResultsTool()
	out, err := getTool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
		"limit":       1,
	}))
	if err != nil {
		t.Fatalf("execute get_query_results: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	result := asMap(t, out.Result)
	rows, _ := result["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows=%d want=1", len(rows))
	}
	nextPageToken, _ := result["next_page_token"].(string)
	if !strings.HasPrefix(nextPageToken, "result-offset:") {
		t.Fatalf("next_page_token=%q, want a job-bound durable offset token", nextPageToken)
	}
	publicMetadata := map[string]map[string]any{
		"query metadata":  out.QueryMetadata,
		"result metadata": out.ResultMetadata,
	}
	if out.Artifact == nil {
		t.Fatal("expected a durable result artifact")
	}
	publicMetadata["artifact metadata"] = out.Artifact.Metadata
	for label, metadata := range publicMetadata {
		if _, exposed := metadata["result_cursor"]; exposed {
			t.Fatalf("%s exposed an internal provider cursor: %#v", label, metadata)
		}
	}
	secondOut, err := getTool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
		"limit":       1,
		"page_token":  nextPageToken,
	}))
	if err != nil || !secondOut.OK {
		t.Fatalf("second durable page output=%+v err=%v", secondOut, err)
	}
	secondResult := asMap(t, secondOut.Result)
	secondRows, _ := secondResult["rows"].([]any)
	if len(secondRows) != 1 || asMap(t, secondRows[0])["n"] != 2 {
		t.Fatalf("second rows=%#v, want n=2", secondRows)
	}
	if secondResult["has_more"] != false || strings.TrimSpace(anyToString(secondResult["next_page_token"])) != "" {
		t.Fatalf("legacy provider cursor was exposed after durable rows: %#v", secondResult)
	}
	legacyOut, legacyErr := getTool.Execute(context.Background(), ExecContext{
		Jobs: reg, ThreadID: "thread-1", RunID: "run-1", DatabaseQueryRowLimit: 1000,
	}, mustJSON(t, map[string]any{
		"job_id": job.ID, "owner_token": "tok_owner", "limit": 1,
		"page_token": "native:Y2h1bmstaW5kZXg6MQ",
	}))
	if legacyErr != nil || legacyOut.OK || !strings.Contains(strings.ToLower(anyToString(asMap(t, legacyOut.Result)["error"])), "invalid or expired") {
		t.Fatalf("legacy provider token output=%+v err=%v", legacyOut, legacyErr)
	}
	if out.QueryMetadata["artifact_id"] == "" {
		t.Fatalf("expected deterministic artifact_id in metadata")
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "get_query_results", "query_results")
	requireQueryLifecycleProviderLinks(t, meta, "bq_results")
}

func TestGetQueryResultsPersistsFullArtifactWhenResultIsPreviewed(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	allRows := make([]map[string]any, 0, 100)
	for i := 0; i < 100; i++ {
		allRows = append(allRows, map[string]any{"n": i, "label": "row"})
	}
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select * from sample",
		"select * from sample",
		map[string]any{
			"provider_job_id":  "bq_pending",
			"warehouse_job_id": "bq_pending",
			"provider_job_url": "https://warehouse.example/jobs/bq_pending",
			"statement_id":     "stmt_bq_pending",
		},
		func(ctx context.Context) (bool, *jobs.QueryResult, error) {
			return true, &jobs.QueryResult{
				Rows: allRows,
				Schema: []map[string]any{
					{"name": "n", "type": "INTEGER"},
					{"name": "label", "type": "STRING"},
				},
				Metadata: map[string]any{
					"terminal_status": "completed",
					"state":           "DONE",
				},
			}, nil
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	waitForStatus(t, reg, job.ID, "thread-1", "tok_owner", jobs.JobStatusCompleted)

	getTool := newGetQueryResultsTool()
	out, err := getTool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
		"limit":       10,
	}))
	if err != nil {
		t.Fatalf("execute get_query_results: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output")
	}

	result := asMap(t, out.Result)
	rows, _ := result["rows"].([]any)
	if len(rows) != 10 {
		t.Fatalf("preview rows=%d want=10", len(rows))
	}
	if result["preview_only"] != true {
		t.Fatalf("preview_only=%#v want=true", result["preview_only"])
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("result should not contain nested result_metadata: %#v", result)
	}
	if got := result["row_count_total"]; got != 100 {
		t.Fatalf("row_count_total=%#v want=100", got)
	}

	artifactID, _ := out.QueryMetadata["artifact_id"].(string)
	if artifactID == "" {
		t.Fatalf("expected deterministic artifact_id in metadata")
	}
	if out.Artifact == nil {
		t.Fatalf("expected full artifact payload")
	}
	if out.Artifact.ID != artifactID {
		t.Fatalf("artifact ID=%q want %q", out.Artifact.ID, artifactID)
	}
	var persistedRows []map[string]any
	if err := json.Unmarshal([]byte(out.Artifact.Content), &persistedRows); err != nil {
		t.Fatalf("unmarshal artifact content: %v", err)
	}
	if len(persistedRows) != 100 {
		t.Fatalf("artifact rows=%d want=100", len(persistedRows))
	}
	if got := out.Artifact.Metadata["row_count_total"]; got != 100 {
		t.Fatalf("artifact row_count_total=%#v want=100", got)
	}
	if got := out.Artifact.Metadata["full_artifact_id"]; got != artifactID {
		t.Fatalf("artifact full_artifact_id=%#v want %q", got, artifactID)
	}
	if got := out.ResultMetadata["full_artifact_id"]; got != artifactID {
		t.Fatalf("ResultMetadata full_artifact_id=%#v want %q", got, artifactID)
	}
	if got := out.ResultMetadata["content_kind"]; got != "json_rows" {
		t.Fatalf("ResultMetadata content_kind=%#v want json_rows", got)
	}
	if got := out.ResultMetadata["kind"]; got != "query_results" {
		t.Fatalf("ResultMetadata kind=%#v want query_results", got)
	}
	if retrieval, ok := out.ResultMetadata["retrieval"].(map[string]any); !ok || retrieval["content_kind"] != "json_rows" {
		t.Fatalf("ResultMetadata retrieval=%#v want json_rows retrieval", out.ResultMetadata["retrieval"])
	}
	artifactResultMeta, ok := out.Artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.result_metadata=%T, want object: %#v", out.Artifact.Metadata["result_metadata"], out.Artifact.Metadata)
	}
	if artifactResultMeta["renderer_key"] != "query" || artifactResultMeta["native_viewer"] != "query" {
		t.Fatalf("artifact result_metadata=%#v, want native query metadata", artifactResultMeta)
	}
	if _, ok := out.Artifact.Metadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("artifact metadata missing tool_presentation: %#v", out.Artifact.Metadata)
	}
}

func TestGetQueryResultsReturnsRetryableStatusWhileRunning(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		map[string]any{
			"provider_job_id":  "bq_pending",
			"warehouse_job_id": "bq_pending",
			"provider_job_url": "https://warehouse.example/jobs/bq_pending",
			"statement_id":     "stmt_bq_pending",
		},
		func(ctx context.Context) (bool, *jobs.QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	tool := newGetQueryResultsTool()
	out, err := tool.Execute(context.Background(), ExecContext{
		Jobs:                  reg,
		ThreadID:              "thread-1",
		RunID:                 "run-1",
		DatabaseQueryRowLimit: 1000,
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
	}))
	if err != nil {
		t.Fatalf("execute get_query_results: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output while job is running")
	}
	result := asMap(t, out.Result)
	if result["status"] == "completed" {
		t.Fatalf("status=%v want non-completed", result["status"])
	}
	if result["retryable"] != true {
		t.Fatalf("retryable=%v want=true", result["retryable"])
	}
	if result["ui_suppress"] != true {
		t.Fatalf("ui_suppress=%v want=true", result["ui_suppress"])
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "get_query_results", "query_pending")
	if meta["retryable"] != true || meta["job_id"] != job.ID || meta["query_provider"] != "bigquery" {
		t.Fatalf("ResultMetadata=%#v, want pending bigquery metadata", meta)
	}
	requireQueryLifecycleProviderLinks(t, meta, "bq_pending")

	_ = reg.Cancel(job.ID, jobs.JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
}

func TestCancelQueryToolCancelsRunningJob(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"databricks",
		"select 1",
		"select 1",
		map[string]any{
			"provider_job_id":  "db_cancel",
			"warehouse_job_id": "db_cancel",
			"provider_job_url": "https://warehouse.example/jobs/db_cancel",
			"statement_id":     "stmt_db_cancel",
		},
		func(ctx context.Context) (bool, *jobs.QueryResult, error) {
			<-ctx.Done()
			return false, nil, ctx.Err()
		},
		nil,
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	cancelTool := newCancelQueryTool()
	out, err := cancelTool.Execute(context.Background(), ExecContext{
		Jobs:     reg,
		ThreadID: "thread-1",
		RunID:    "run-1",
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
		"reason":      "wrong_query",
	}))
	if err != nil {
		t.Fatalf("execute cancel_query: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output")
	}
	snap, ok := reg.Snapshot(job.ID, jobs.JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
	if !ok {
		t.Fatalf("snapshot missing")
	}
	if snap.Status != jobs.JobStatusCancelled {
		t.Fatalf("status=%s want=%s", snap.Status, jobs.JobStatusCancelled)
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "cancel_query", "cancel_query")
	if meta["readonly"] != false || meta["status"] != string(jobs.JobStatusCancelled) || meta["job_id"] != job.ID || meta["cancel_reason"] != "wrong_query" {
		t.Fatalf("ResultMetadata=%#v, want cancel metadata", meta)
	}
	requireQueryLifecycleProviderLinks(t, meta, "db_cancel")
}

func TestCancelQueryToolDryRunDoesNotCancelRunningJob(t *testing.T) {
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	var cancelCalls atomic.Int32
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"databricks",
		"select 1",
		"select 1",
		map[string]any{
			"provider_job_id":  "db_dry_run",
			"warehouse_job_id": "db_dry_run",
			"provider_job_url": "https://warehouse.example/jobs/db_dry_run",
			"statement_id":     "stmt_db_dry_run",
		},
		nil,
		func(ctx context.Context) error {
			cancelCalls.Add(1)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	cancelTool := newCancelQueryTool()
	out, err := cancelTool.Execute(context.Background(), ExecContext{
		Jobs:     reg,
		ThreadID: "thread-1",
		RunID:    "run-1",
	}, mustJSON(t, map[string]any{
		"job_id":      job.ID,
		"owner_token": "tok_owner",
		"reason":      "no_longer_needed",
		"dry_run":     true,
	}))
	if err != nil {
		t.Fatalf("execute cancel_query dry_run: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK dry-run output")
	}
	result := asMap(t, out.Result)
	if result["dry_run"] != true {
		t.Fatalf("dry_run=%#v want=true", result["dry_run"])
	}
	if result["would_cancel"] != true {
		t.Fatalf("would_cancel=%#v want=true", result["would_cancel"])
	}
	meta := requireQueryLifecycleResultMetadata(t, out, "cancel_query", "cancel_dry_run")
	if meta["readonly"] != false || meta["mode"] != "dry_run" || meta["would_cancel"] != true || meta["query_provider"] != "databricks" || meta["cancel_reason"] != "no_longer_needed" {
		t.Fatalf("ResultMetadata=%#v, want cancel dry-run metadata", meta)
	}
	requireQueryLifecycleProviderLinks(t, meta, "db_dry_run")
	if cancelCalls.Load() != 0 {
		t.Fatalf("cancel callback called %d times during dry-run", cancelCalls.Load())
	}
	snap, ok := reg.Snapshot(job.ID, jobs.JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
	if !ok {
		t.Fatalf("snapshot missing")
	}
	if snap.Status == jobs.JobStatusCancelled {
		t.Fatalf("dry-run cancelled job")
	}
}

func TestCancelQueryToolRequiresExplicitReason(t *testing.T) {
	tool := newCancelQueryTool()
	out, err := tool.Execute(context.Background(), ExecContext{
		Jobs:     jobs.NewRegistry(jobs.RegistryOptions{}),
		ThreadID: "thread-1",
		RunID:    "run-1",
	}, mustJSON(t, map[string]any{
		"job_id":      "qj-example",
		"owner_token": "tok_owner",
	}))
	if err != nil {
		t.Fatalf("execute cancel_query: %v", err)
	}
	if out.OK || !strings.Contains(strings.ToLower(fmt.Sprint(out.Result)), "reason") {
		t.Fatalf("output=%#v, want an explicit reason error", out)
	}
}

func TestQueryLifecycleJobMetadataCarriesCanonicalPollingIdentity(t *testing.T) {
	job := &jobs.Job{
		ID:       "qj-metadata-identity",
		Provider: "databricks",
		Status:   jobs.JobStatusRunning,
		Owner: jobs.JobOwner{
			ThreadID: "thread-metadata-identity",
			Handle:   "tok_metadata_identity",
		},
	}
	metadata := queryLifecycleResultMetadataForJob("check_query_status", "query_status", job, "running", nil)
	wantArtifactID := DeterministicAsyncArtifactID(job.Owner.ThreadID, job.ID)
	if metadata["job_id"] != job.ID || metadata["thread_id"] != job.Owner.ThreadID ||
		metadata["owner_token"] != job.Owner.Handle || metadata["artifact_id"] != wantArtifactID {
		t.Fatalf("lifecycle metadata lost canonical polling identity: %#v", metadata)
	}
}

func TestManagedResultOffsetTokenIsBoundToJobAndProvider(t *testing.T) {
	token := encodeManagedResultOffsetToken("qj-results-a", "Databricks", 25)
	if token == "" {
		t.Fatal("encodeManagedResultOffsetToken returned an empty token")
	}
	if offset, ok := decodeManagedResultOffsetToken(token, "qj-results-a", "databricks"); !ok || offset != 25 {
		t.Fatalf("same-job token decoded as offset=%d ok=%v", offset, ok)
	}
	if offset, ok := decodeManagedResultOffsetToken(token, "qj-results-b", "databricks"); ok || offset != 0 {
		t.Fatalf("cross-job token decoded as offset=%d ok=%v", offset, ok)
	}
	if offset, ok := decodeManagedResultOffsetToken(token, "qj-results-a", "bigquery"); ok || offset != 0 {
		t.Fatalf("cross-provider token decoded as offset=%d ok=%v", offset, ok)
	}
	for _, legacy := range []string{"25", "native:Y2h1bmstaW5kZXg6MQ"} {
		if offset, ok := decodeManagedResultOffsetToken(legacy, "qj-results-a", "databricks"); ok || offset != 0 {
			t.Fatalf("legacy unbound token %q decoded as offset=%d ok=%v", legacy, offset, ok)
		}
	}
}

func TestManagedQueryRetryTargetFallbackIsExecutable(t *testing.T) {
	clause := managedQueryRetryTargetClause(map[string]any{
		"job_id":      "qj-ledger-read-failed",
		"owner_token": "tok_ledger_read_failed",
	})
	if !strings.Contains(clause, "check_query_status") || !strings.Contains(clause, "job_id") ||
		!strings.Contains(clause, "owner_token") || strings.Contains(clause, "exact query_obligation_id returned for this durable attempt") {
		t.Fatalf("fallback retry clause is not executable: %q", clause)
	}
}

func waitForStatus(t *testing.T, reg *jobs.Registry, jobID, threadID, ownerToken string, want jobs.JobStatus) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		snap, ok := reg.Snapshot(jobID, jobs.JobOwner{ThreadID: threadID, Handle: ownerToken})
		if ok && snap.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job status did not reach %s", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return json.RawMessage(b)
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("result type=%T want map[string]any", v)
	}
	return m
}
