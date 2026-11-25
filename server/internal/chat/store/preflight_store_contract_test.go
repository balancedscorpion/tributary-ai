package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// Keep the two durable implementations pinned to the complete public store
// contract. This catches capability/preflight additions that accidentally land
// in only one backend.
var _ Store = (*JSONStore)(nil)
var _ Store = (*MySQLStore)(nil)

func TestJSONRunPlanLifecyclePersistsTargetPolicyAndIdempotencyAcrossReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	st, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	initialTarget := RunTarget{ConnectionID: " openai-primary ", Model: " gpt-5 "}
	if _, err := st.SetThreadTarget(ctx, thread.ID, initialTarget); err != nil {
		t.Fatalf("SetThreadTarget: %v", err)
	}

	st, err = NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen after target save: %v", err)
	}
	gotThread, err := st.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread after target reopen: %v", err)
	}
	if gotThread.LastTarget == nil || *gotThread.LastTarget != (RunTarget{ConnectionID: "openai-primary", Model: "gpt-5"}) {
		t.Fatalf("LastTarget=%#v, want trimmed persisted target", gotThread.LastTarget)
	}

	createdAt := time.Now().UTC().Truncate(time.Millisecond)
	target := RunTarget{ConnectionID: "bedrock-prod", Model: "anthropic.claude-sonnet"}
	plan := preflightPlanForStoreTest("plan-json-lifecycle-1", thread.ID, target, "requirements-v1", "config-v1", createdAt)
	created, err := st.CreateRunPlan(ctx, plan)
	if err != nil {
		t.Fatalf("CreateRunPlan: %v", err)
	}
	// Returned plans must not alias the store's persisted slices/raw JSON.
	created.Choices[0].Options[0].Label = "mutated by caller"
	created.CapabilityReport[0] = '['
	persisted, err := st.GetRunPlan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("GetRunPlan: %v", err)
	}
	if persisted.Choices[0].Options[0].Label != "Extract text" || !json.Valid(persisted.CapabilityReport) {
		t.Fatalf("persisted plan was aliased by caller mutation: %#v / %s", persisted.Choices, persisted.CapabilityReport)
	}

	finalizeAt := createdAt.Add(time.Minute)
	req := finalizationRequestForStoreTest(plan, finalizeAt)
	req.ExpectedThreadPolicyFingerprint = AttachmentPoliciesFingerprint(nil)
	run, finalized, err := st.FinalizeRunPlanAndCreateRun(ctx, req)
	if err != nil {
		t.Fatalf("FinalizeRunPlanAndCreateRun: %v", err)
	}
	if finalized.Status != RunPlanStatusFinalized || finalized.RunID == nil || *finalized.RunID != run.ID {
		t.Fatalf("finalized plan/run mismatch: plan=%#v run=%#v", finalized, run)
	}
	if !run.CreatedAt.Equal(finalizeAt) || !run.UpdatedAt.Equal(finalizeAt) {
		t.Fatalf("run timestamps=%s/%s, want finalization time %s", run.CreatedAt, run.UpdatedAt, finalizeAt)
	}
	if run.ConnectionID != target.ConnectionID || run.PlanID != plan.ID || run.Model != target.Model {
		t.Fatalf("run target linkage=%#v, want plan target/link", run)
	}

	gotThread, err = st.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread after finalize: %v", err)
	}
	if gotThread.LastTarget == nil || *gotThread.LastTarget != target {
		t.Fatalf("finalized LastTarget=%#v, want %#v", gotThread.LastTarget, target)
	}
	if len(gotThread.AttachmentPolicies) != 1 {
		t.Fatalf("AttachmentPolicies=%#v, want one thread-scoped policy", gotThread.AttachmentPolicies)
	}
	policyID := gotThread.AttachmentPolicies[0].ID
	if policyID == "" || gotThread.AttachmentPolicies[0].Class != "office_docs_to_text" || gotThread.AttachmentPolicies[0].Action != "extract_text" {
		t.Fatalf("persisted policy=%#v, want explicit extraction policy", gotThread.AttachmentPolicies[0])
	}

	st, err = NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen after finalization: %v", err)
	}
	byRun, err := st.GetRunPlanForRun(ctx, run.ID)
	if err != nil || byRun.ID != plan.ID || byRun.RunID == nil || *byRun.RunID != run.ID {
		t.Fatalf("GetRunPlanForRun after reopen=%#v err=%v", byRun, err)
	}
	replayed, replayedPlan, err := st.FinalizeRunPlanAndCreateRun(ctx, req)
	if err != nil || replayed.ID != run.ID || replayedPlan.ID != plan.ID {
		t.Fatalf("idempotent finalized replay run=%#v plan=%#v err=%v", replayed, replayedPlan, err)
	}

	staleRequests := []struct {
		name   string
		mutate func(*FinalizeRunPlanRequest)
	}{
		{"thread", func(in *FinalizeRunPlanRequest) { in.ExpectedThreadID = "another-thread" }},
		{"target", func(in *FinalizeRunPlanRequest) { in.ExpectedTarget.Model = "another-model" }},
		{"requirements", func(in *FinalizeRunPlanRequest) { in.ExpectedRequirementsHash = "requirements-v2" }},
		{"fingerprint", func(in *FinalizeRunPlanRequest) { in.ExpectedConfigFingerprint = "config-v2" }},
		{"run target", func(in *FinalizeRunPlanRequest) { in.Run.ConnectionID = "another-connection" }},
	}
	for _, tc := range staleRequests {
		stale := req
		tc.mutate(&stale)
		if _, _, err := st.FinalizeRunPlanAndCreateRun(ctx, stale); !errors.Is(err, ErrPlanStale) {
			t.Fatalf("%s-divergent finalized replay err=%v, want ErrPlanStale", tc.name, err)
		}
	}
	runs, err := st.ListRuns(ctx, thread.ID, 10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns after stale replays=%#v err=%v, want exactly original run", runs, err)
	}

	// Draft finalization is bound to the exact thread policy set seen during
	// preflight. A policy deletion between preflight and finalization must fail,
	// even though the plan's target and routing fingerprint are still current.
	policyStalePlan := preflightPlanForStoreTest("plan-json-thread-policy-stale", thread.ID, target, "requirements-policy-stale", "config-v1", finalizeAt)
	policyStalePlan.ExpiresAt = finalizeAt.Add(24 * time.Hour)
	if _, err := st.CreateRunPlan(ctx, policyStalePlan); err != nil {
		t.Fatalf("CreateRunPlan thread-policy stale: %v", err)
	}
	policyStaleReq := finalizationRequestForStoreTest(policyStalePlan, finalizeAt.Add(time.Minute))
	policyStaleReq.ExpectedThreadPolicyFingerprint = AttachmentPoliciesFingerprint(gotThread.AttachmentPolicies)
	if _, err := st.DeleteThreadAttachmentPolicy(ctx, thread.ID, policyID); err != nil {
		t.Fatalf("DeleteThreadAttachmentPolicy before stale finalization: %v", err)
	}
	if _, _, err := st.FinalizeRunPlanAndCreateRun(ctx, policyStaleReq); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("thread-policy-stale finalization err=%v, want ErrPlanStale", err)
	}
	if policyStale, err := st.GetRunPlan(ctx, policyStalePlan.ID); err != nil || policyStale.Status != RunPlanStatusDraft || policyStale.RunID != nil {
		t.Fatalf("thread-policy-stale plan changed=%#v err=%v", policyStale, err)
	}

	// A later plan can recreate a revoked policy, and a subsequent selection of
	// the same class/action/surface must not accumulate duplicates.
	secondPlan := preflightPlanForStoreTest("plan-json-lifecycle-2", thread.ID, target, "requirements-v2", "config-v1", finalizeAt)
	if _, err := st.CreateRunPlan(ctx, secondPlan); err != nil {
		t.Fatalf("CreateRunPlan second: %v", err)
	}
	if _, _, err := st.FinalizeRunPlanAndCreateRun(ctx, finalizationRequestForStoreTest(secondPlan, finalizeAt.Add(time.Minute))); err != nil {
		t.Fatalf("FinalizeRunPlanAndCreateRun second: %v", err)
	}
	gotThread, err = st.GetThread(ctx, thread.ID)
	if err != nil || len(gotThread.AttachmentPolicies) != 1 {
		t.Fatalf("recreated policies=%#v err=%v, want one policy", gotThread.AttachmentPolicies, err)
	}
	recreatedPolicyID := gotThread.AttachmentPolicies[0].ID
	thirdPlan := preflightPlanForStoreTest("plan-json-lifecycle-3", thread.ID, target, "requirements-v3", "config-v1", finalizeAt)
	if _, err := st.CreateRunPlan(ctx, thirdPlan); err != nil {
		t.Fatalf("CreateRunPlan third: %v", err)
	}
	if _, _, err := st.FinalizeRunPlanAndCreateRun(ctx, finalizationRequestForStoreTest(thirdPlan, finalizeAt.Add(2*time.Minute))); err != nil {
		t.Fatalf("FinalizeRunPlanAndCreateRun third: %v", err)
	}
	gotThread, err = st.GetThread(ctx, thread.ID)
	if err != nil || len(gotThread.AttachmentPolicies) != 1 || gotThread.AttachmentPolicies[0].ID != recreatedPolicyID {
		t.Fatalf("idempotent policies=%#v err=%v, want one unchanged policy", gotThread.AttachmentPolicies, err)
	}
	if _, err := st.DeleteThreadAttachmentPolicy(ctx, thread.ID, recreatedPolicyID); err != nil {
		t.Fatalf("DeleteThreadAttachmentPolicy: %v", err)
	}
	st, err = NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen after policy deletion: %v", err)
	}
	gotThread, err = st.GetThread(ctx, thread.ID)
	if err != nil || len(gotThread.AttachmentPolicies) != 0 {
		t.Fatalf("policies after delete/reopen=%#v err=%v, want none", gotThread.AttachmentPolicies, err)
	}
	// A retry of an already finalized request is idempotent even if its thread
	// policy has since been revoked. It must return the original run, not create
	// a replacement or reinterpret the old plan against live state.
	replayed, replayedPlan, err = st.FinalizeRunPlanAndCreateRun(ctx, req)
	if err != nil || replayed.ID != run.ID || replayedPlan.ID != plan.ID {
		t.Fatalf("finalized replay after policy revocation run=%#v plan=%#v err=%v", replayed, replayedPlan, err)
	}

	expired := preflightPlanForStoreTest("plan-json-expired", thread.ID, target, "requirements-expired", "config-v1", createdAt.Add(-2*time.Hour))
	expired.ExpiresAt = createdAt.Add(-time.Hour)
	if _, err := st.CreateRunPlan(ctx, expired); err != nil {
		t.Fatalf("CreateRunPlan expired seed: %v", err)
	}
	deleted, err := st.DeleteExpiredRunPlans(ctx, finalizeAt.Add(2*time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpiredRunPlans=%d err=%v, want one draft", deleted, err)
	}
	if _, err := st.GetRunPlan(ctx, expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired draft GetRunPlan err=%v, want ErrNotFound", err)
	}
	if _, err := st.GetRunPlan(ctx, plan.ID); err != nil {
		t.Fatalf("finalized plan was removed by draft cleanup: %v", err)
	}
	invalidCreate := finalized
	invalidCreate.ID = "plan-invalid-finalized-create"
	if _, err := st.CreateRunPlan(ctx, invalidCreate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreateRunPlan(finalized) err=%v, want ErrInvalid", err)
	}
	invalidPath := preflightPlanForStoreTest("../plan-invalid-path", thread.ID, target, "requirements-path", "config-v1", createdAt)
	if _, err := st.CreateRunPlan(ctx, invalidPath); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CreateRunPlan(path separator) err=%v, want ErrInvalid", err)
	}
}

func preflightPlanForStoreTest(id, threadID string, target RunTarget, requirementsHash, fingerprint string, createdAt time.Time) RunPlan {
	policyClass := "office_docs_to_text"
	return RunPlan{
		ID: id, ThreadID: threadID, Target: target,
		ConfigFingerprint: fingerprint, RequirementsHash: requirementsHash,
		ResultStatus: "choices_required", Status: RunPlanStatusDraft,
		Choices: []PlanChoice{{
			PlanIssue: PlanIssue{IssueID: id + "-choice", Code: "fidelity_reduction", Text: "Extract the document"},
			Options: []PlanChoiceOption{{
				ID: "extract_text", Label: "Extract text", Consequence: "Layout may change",
				PolicyClass: &policyClass, APISurface: "responses",
			}},
			AllowedScopes: []string{"run", "thread"},
		}},
		CapabilityReport: json.RawMessage(`{"target":{"apiSurface":"responses"},"features":{}}`),
		CreatedAt:        createdAt.UTC(),
		ExpiresAt:        createdAt.UTC().Add(30 * time.Minute),
	}
}

func finalizationRequestForStoreTest(plan RunPlan, now time.Time) FinalizeRunPlanRequest {
	return FinalizeRunPlanRequest{
		PlanID: plan.ID, ExpectedThreadID: plan.ThreadID, ExpectedTarget: plan.Target,
		ExpectedRequirementsHash: plan.RequirementsHash, ExpectedConfigFingerprint: plan.ConfigFingerprint,
		Selections: []PlanChoiceSelection{{IssueID: plan.Choices[0].IssueID, OptionID: "extract_text", Scope: "thread"}},
		Run: CreateRunParams{
			ThreadID: plan.ThreadID, ProviderID: "bedrock", ConnectionID: plan.Target.ConnectionID,
			Model: plan.Target.Model, SystemPrompt: "system", MaxIterations: 7,
		},
		Now: now.UTC(),
	}
}

type runPlanScannerFunc func(dest ...any) error

func (fn runPlanScannerFunc) Scan(dest ...any) error { return fn(dest...) }

func TestScanMySQLRunPlanMatchesMigrationColumnOrderAndJSONFields(t *testing.T) {
	createdAt := time.Date(2026, 7, 11, 10, 0, 0, 123000000, time.UTC)
	expiresAt := createdAt.Add(15 * time.Minute)
	finalizedAt := createdAt.Add(time.Minute)
	plan, err := scanMySQLRunPlan(runPlanScannerFunc(func(dest ...any) error {
		if len(dest) != 18 {
			return fmt.Errorf("scan destinations=%d, want 18", len(dest))
		}
		*dest[0].(*string) = "plan-mysql-scan"
		*dest[1].(*string) = "thread-mysql-scan"
		*dest[2].(*sql.NullString) = sql.NullString{String: "run-mysql-scan", Valid: true}
		*dest[3].(*string) = "connection-mysql-scan"
		*dest[4].(*string) = "model-mysql-scan"
		*dest[5].(*sql.NullString) = sql.NullString{String: "fingerprint-mysql-scan", Valid: true}
		*dest[6].(*string) = "requirements-mysql-scan"
		*dest[7].(*string) = "choices_required"
		*dest[8].(*string) = RunPlanStatusFinalized
		*dest[9].(*string) = `[]`
		*dest[10].(*string) = `[{"issueId":"issue-mysql-scan","code":"fidelity_reduction","text":"Extract","options":[{"id":"extract_text","label":"Extract","consequence":"Layout changes","apiSurface":"responses"}],"allowedScopes":["thread"]}]`
		*dest[11].(*string) = `[{"issueId":"notice-mysql-scan","code":"stale_evidence","text":"Recheck"}]`
		*dest[12].(*bool) = true
		*dest[13].(*string) = `{"target":{"apiSurface":"responses"},"features":{"documents":{"state":"conditional"}}}`
		*dest[14].(*sql.NullString) = sql.NullString{String: `[{"issueId":"issue-mysql-scan","optionId":"extract_text","scope":"thread","selectedAt":"2026-07-11T10:01:00.123Z"}]`, Valid: true}
		*dest[15].(*time.Time) = createdAt
		*dest[16].(*time.Time) = expiresAt
		*dest[17].(*sql.NullTime) = sql.NullTime{Time: finalizedAt, Valid: true}
		return nil
	}))
	if err != nil {
		t.Fatalf("scanMySQLRunPlan: %v", err)
	}
	if plan.ID != "plan-mysql-scan" || plan.ThreadID != "thread-mysql-scan" || plan.RunID == nil || *plan.RunID != "run-mysql-scan" {
		t.Fatalf("plan identity/linkage=%#v", plan)
	}
	if plan.Target != (RunTarget{ConnectionID: "connection-mysql-scan", Model: "model-mysql-scan"}) || plan.ConfigFingerprint != "fingerprint-mysql-scan" {
		t.Fatalf("plan target/fingerprint=%#v/%q", plan.Target, plan.ConfigFingerprint)
	}
	if len(plan.Choices) != 1 || plan.Choices[0].IssueID != "issue-mysql-scan" || len(plan.Notices) != 1 || plan.Notices[0].IssueID != "notice-mysql-scan" {
		t.Fatalf("plan issue JSON fields=%#v/%#v", plan.Choices, plan.Notices)
	}
	if len(plan.Selections) != 1 || plan.Selections[0].OptionID != "extract_text" || !plan.StaleEvidence {
		t.Fatalf("plan selections/stale evidence=%#v/%v", plan.Selections, plan.StaleEvidence)
	}
	if !json.Valid(plan.CapabilityReport) || plan.FinalizedAt == nil || !plan.FinalizedAt.Equal(finalizedAt) {
		t.Fatalf("plan capability/finalization=%s/%#v", plan.CapabilityReport, plan.FinalizedAt)
	}
}

// This tiny driver models the MySQL matched-row behavior that motivated the
// SetThreadTarget existence fix: a valid idempotent UPDATE may report zero
// affected rows even though the thread exists.
type idempotentTargetTestDriver struct{}

type idempotentTargetTestConn struct{}

func (idempotentTargetTestDriver) Open(string) (driver.Conn, error) {
	return idempotentTargetTestConn{}, nil
}

func (idempotentTargetTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}

func (idempotentTargetTestConn) Close() error { return nil }

func (idempotentTargetTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not supported")
}

func (idempotentTargetTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(strings.ToLower(query), "update threads set last_target_connection_id") || len(args) != 4 || namedString(args, 0) != "conn-idempotent" || namedString(args, 1) != "model-idempotent" || namedString(args, 3) != "thread-idempotent" {
		return nil, fmt.Errorf("unexpected target update: %s args=%#v", query, args)
	}
	return approvalStoreTestResult(0), nil
}

func (idempotentTargetTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(strings.ToLower(query), "from threads t where t.id=?") || len(args) != 1 || namedString(args, 0) != "thread-idempotent" {
		return nil, fmt.Errorf("unexpected thread query: %s args=%#v", query, args)
	}
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	return approvalRows(
		[]string{"id", "title", "summary", "run_count", "summary_run_count", "project_id", "status", "created_at", "updated_at", "last_message_at", "last_run_id", "last_target_connection_id", "last_target_model", "attachment_policies", "artifact_count"},
		[][]driver.Value{{"thread-idempotent", "Thread", nil, int64(0), int64(0), nil, "active", now, now, nil, nil, "conn-idempotent", "model-idempotent", []byte(`[]`), int64(0)}},
	), nil
}

func TestMySQLSetThreadTargetTreatsZeroAffectedRowsAsIdempotent(t *testing.T) {
	db, err := sql.Open("idempotent-target-store-test", "")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer db.Close()
	thread, err := NewMySQL(db).SetThreadTarget(context.Background(), "thread-idempotent", RunTarget{
		ConnectionID: "conn-idempotent", Model: "model-idempotent",
	})
	if err != nil {
		t.Fatalf("SetThreadTarget: %v", err)
	}
	if thread.LastTarget == nil || *thread.LastTarget != (RunTarget{ConnectionID: "conn-idempotent", Model: "model-idempotent"}) {
		t.Fatalf("LastTarget=%#v, want existing idempotent target", thread.LastTarget)
	}
}

func TestApplyThreadRuntimeFieldsRejectsCorruptAttachmentPolicies(t *testing.T) {
	thread := Thread{ID: "thread-corrupt-policy"}
	err := applyThreadRuntimeFields(&thread, sql.NullString{}, sql.NullString{}, []byte(`{"not":"an array"}`))
	if err == nil || !strings.Contains(err.Error(), "decode thread attachment policies") {
		t.Fatalf("applyThreadRuntimeFields err=%v, want explicit policy decode failure", err)
	}
}

type capabilityEvidenceTestDriver struct{}

var capabilityEvidenceTestStates sync.Map

type capabilityEvidenceTestState struct {
	mu        sync.Mutex
	evidence  *ProviderCapabilityEvidence
	lastQuery string
	lastArgs  []driver.NamedValue
}

type capabilityEvidenceTestConn struct{ state *capabilityEvidenceTestState }

func (capabilityEvidenceTestDriver) Open(name string) (driver.Conn, error) {
	value, ok := capabilityEvidenceTestStates.Load(name)
	if !ok {
		return nil, fmt.Errorf("capability evidence state %q not found", name)
	}
	return capabilityEvidenceTestConn{state: value.(*capabilityEvidenceTestState)}, nil
}

func (capabilityEvidenceTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare not supported")
}

func (capabilityEvidenceTestConn) Close() error { return nil }

func (capabilityEvidenceTestConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions not supported")
}

func (c capabilityEvidenceTestConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(strings.ToLower(query), "insert into provider_capability_evidence") || len(args) != 10 {
		return nil, fmt.Errorf("unexpected capability evidence exec: %s args=%#v", query, args)
	}
	evidence := ProviderCapabilityEvidence{
		ConnectionID: namedString(args, 0), Model: namedString(args, 1), Fingerprint: namedString(args, 2), Probe: namedString(args, 3),
		Verdict: namedString(args, 4), Reason: namedString(args, 5), Message: namedString(args, 6), RequestID: namedString(args, 7),
		DurationMS: namedInt64(args, 8), VerifiedAt: namedTime(args, 9),
	}
	c.state.mu.Lock()
	if c.state.evidence == nil || !c.state.evidence.VerifiedAt.After(evidence.VerifiedAt) {
		c.state.evidence = &evidence
	}
	c.state.mu.Unlock()
	return approvalStoreTestResult(1), nil
}

func (c capabilityEvidenceTestConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q := strings.ToLower(query)
	if !strings.Contains(q, "from provider_capability_evidence") {
		return nil, fmt.Errorf("unexpected capability evidence query: %s", query)
	}
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	c.state.lastQuery = query
	c.state.lastArgs = append([]driver.NamedValue(nil), args...)
	columns := []string{"connection_id", "model", "fingerprint", "probe", "verdict", "reason", "message", "request_id", "duration_ms", "verified_at"}
	if c.state.evidence == nil || !capabilityEvidenceMatchesQuery(*c.state.evidence, q, args) {
		return approvalRows(columns, nil), nil
	}
	evidence := *c.state.evidence
	return approvalRows(columns, [][]driver.Value{{
		evidence.ConnectionID, evidence.Model, evidence.Fingerprint, evidence.Probe, evidence.Verdict,
		emptyStringAsNil(evidence.Reason), evidence.Message, emptyStringAsNil(evidence.RequestID), evidence.DurationMS, evidence.VerifiedAt,
	}}), nil
}

func capabilityEvidenceMatchesQuery(evidence ProviderCapabilityEvidence, query string, args []driver.NamedValue) bool {
	if len(args) < 2 || namedString(args, 0) != evidence.ConnectionID {
		return false
	}
	idx := 1
	for _, item := range []struct {
		clause string
		value  string
	}{{"model=?", evidence.Model}, {"fingerprint=?", evidence.Fingerprint}, {"probe=?", evidence.Probe}} {
		if strings.Contains(query, item.clause) {
			if idx >= len(args)-1 || namedString(args, idx) != item.value {
				return false
			}
			idx++
		}
	}
	return idx == len(args)-1
}

func TestMySQLCapabilityEvidenceUpsertAndExactFilterContract(t *testing.T) {
	name := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	state := &capabilityEvidenceTestState{}
	capabilityEvidenceTestStates.Store(name, state)
	db, err := sql.Open("capability-evidence-store-test", name)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
		capabilityEvidenceTestStates.Delete(name)
	})
	st := NewMySQL(db)
	verifiedAt := time.Date(2026, 7, 11, 13, 14, 15, 123000000, time.UTC)
	base := ProviderCapabilityEvidence{
		ConnectionID: " conn-evidence ", Model: " Model/Case-Sensitive ", Fingerprint: " SHA256:ABC ", Probe: " tool_round_trip ",
		Verdict: " VERIFIED ", Reason: " supported ", Message: "first", RequestID: " request-1 ", DurationMS: 42, VerifiedAt: verifiedAt,
	}
	created, err := st.UpsertProviderCapabilityEvidence(context.Background(), base)
	if err != nil {
		t.Fatalf("UpsertProviderCapabilityEvidence: %v", err)
	}
	if created.ConnectionID != "conn-evidence" || created.Model != "Model/Case-Sensitive" || created.Verdict != "verified" || created.RequestID != "request-1" {
		t.Fatalf("normalized created evidence=%#v", created)
	}
	created.Message = "updated"
	created.DurationMS = 9
	created.VerifiedAt = verifiedAt.Add(time.Minute)
	if _, err := st.UpsertProviderCapabilityEvidence(context.Background(), created); err != nil {
		t.Fatalf("UpsertProviderCapabilityEvidence update: %v", err)
	}
	older := created
	older.Message = "late stale completion"
	older.VerifiedAt = verifiedAt.Add(30 * time.Second)
	kept, err := st.UpsertProviderCapabilityEvidence(context.Background(), older)
	if err != nil {
		t.Fatalf("UpsertProviderCapabilityEvidence older: %v", err)
	}
	if kept.Message != "updated" || !kept.VerifiedAt.Equal(created.VerifiedAt) {
		t.Fatalf("older MySQL evidence replaced newer evidence: %#v", kept)
	}
	got, err := st.ListProviderCapabilityEvidence(context.Background(), ProviderCapabilityEvidenceFilter{
		ConnectionID: created.ConnectionID, Model: created.Model, Fingerprint: created.Fingerprint, Probe: created.Probe, Limit: 7,
	})
	if err != nil {
		t.Fatalf("ListProviderCapabilityEvidence: %v", err)
	}
	if len(got) != 1 || got[0].Message != "updated" || got[0].DurationMS != 9 || !got[0].VerifiedAt.Equal(created.VerifiedAt) {
		t.Fatalf("listed evidence=%#v, want updated exact-key row", got)
	}
	state.mu.Lock()
	query := strings.ToLower(state.lastQuery)
	args := append([]driver.NamedValue(nil), state.lastArgs...)
	state.mu.Unlock()
	if !strings.Contains(query, "connection_id=?") || !strings.Contains(query, "model=?") || !strings.Contains(query, "fingerprint=?") || !strings.Contains(query, "probe=?") || !strings.Contains(query, "order by verified_at desc limit ?") {
		t.Fatalf("evidence filter query missing exact predicates/order/limit: %s", query)
	}
	if len(args) != 5 || namedInt64(args, 4) != 7 {
		t.Fatalf("evidence filter args=%#v, want four keys and limit 7", args)
	}
	wrongCase, err := st.ListProviderCapabilityEvidence(context.Background(), ProviderCapabilityEvidenceFilter{
		ConnectionID: created.ConnectionID, Model: strings.ToLower(created.Model), Fingerprint: created.Fingerprint,
	})
	if err != nil || len(wrongCase) != 0 {
		t.Fatalf("case-divergent model evidence=%#v err=%v, want exact-key miss", wrongCase, err)
	}
}

func init() {
	sql.Register("idempotent-target-store-test", idempotentTargetTestDriver{})
	sql.Register("capability-evidence-store-test", capabilityEvidenceTestDriver{})
}
