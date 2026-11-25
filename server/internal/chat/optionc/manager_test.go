package optionc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	arxivapi "ai-analyst-local/internal/arxiv"
	"ai-analyst-local/internal/atlassian"
	"ai-analyst-local/internal/bigquery"
	"ai-analyst-local/internal/browsercontrol"
	"ai-analyst-local/internal/chat/fileartifact"
	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/rlm"
	"ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
	"ai-analyst-local/internal/databricks"
	"ai-analyst-local/internal/permissions"
	"ai-analyst-local/internal/secrets"
	"ai-analyst-local/internal/sharepoint"
	slackapi "ai-analyst-local/internal/slack"
	"ai-analyst-local/internal/sources"
	"ai-analyst-local/internal/trino"
)

func testProjectID(value string) *string {
	v := value
	return &v
}

type requireApprovalOutcome struct {
	applied bool
	allowed bool
	out     tools.Output
}

type requireApprovalArgsOutcome struct {
	applied bool
	allowed bool
	args    json.RawMessage
	out     tools.Output
}

func TestScanApprovalPreviewCeilingCopy(t *testing.T) {
	withCeiling := scanApprovalPreview("BigQuery", jobs.ScanApprovalRequest{
		EstimatedBytes:      250 << 30,
		AutomaticScanBytes:  100 << 30,
		ApprovedScanBytes:   500 << 30,
		AuthorizedScanBytes: 525 << 29, // 262.5 GiB: estimate + 5%
	})
	if withCeiling["approved_scan_bytes"] != int64(500<<30) {
		t.Fatalf("preview=%+v", withCeiling)
	}
	if withCeiling["authorized_scan_bytes"] != int64(525<<29) {
		t.Fatalf("preview=%+v", withCeiling)
	}
	desc, _ := withCeiling["description"].(string)
	if !strings.Contains(desc, "up to 262.50 GiB") ||
		!strings.Contains(desc, "5% estimate allowance") ||
		!strings.Contains(desc, "500.00 GiB hard ceiling") {
		t.Fatalf("description=%q", desc)
	}

	// No configured ceiling: the key is omitted (the card renders no bogus
	// "up to 0 GiB" row) and the copy never cites a 0.00 GiB ceiling.
	noCeiling := scanApprovalPreview("BigQuery", jobs.ScanApprovalRequest{
		EstimatedBytes:      250 << 30,
		AutomaticScanBytes:  100 << 30,
		AuthorizedScanBytes: 525 << 29,
	})
	if _, present := noCeiling["approved_scan_bytes"]; present {
		t.Fatalf("no-ceiling preview must omit approved_scan_bytes: %+v", noCeiling)
	}
	desc, _ = noCeiling["description"].(string)
	if strings.Contains(desc, "ceiling") ||
		!strings.Contains(desc, "authorizes up to 262.50 GiB") ||
		!strings.Contains(desc, "up to 5% allowance") {
		t.Fatalf("description=%q", desc)
	}
}

func TestRequestQueryScanApprovalIsOneTimeAndCarriesEstimate(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	done := make(chan struct {
		decision jobs.ScanApprovalDecision
		err      error
	}, 1)
	go func() {
		decision, requestErr := manager.requestQueryScanApproval(
			ctx,
			thread,
			run,
			types.ToolCall{ID: "tc_scan_approval", Name: "run_bigquery_query"},
			"run_bigquery_query",
			jobs.ScanApprovalRequest{
				JobID:               "qj-bigquery-test",
				Provider:            "bigquery",
				ConnectionID:        "analytics-prod",
				SQLPreview:          "SELECT * FROM analytics.events",
				EstimatedBytes:      250 << 30,
				AutomaticScanBytes:  100 << 30,
				ApprovedScanBytes:   500 << 30,
				AuthorizedScanBytes: 525 << 29,
			},
			nil,
		)
		done <- struct {
			decision jobs.ScanApprovalDecision
			err      error
		}{decision: decision, err: requestErr}
	}()

	var approval store.ApprovalRequest
	deadline := time.After(time.Second)
	for approval.ID == "" {
		select {
		case <-deadline:
			t.Fatal("scan approval was not created")
		case <-time.After(5 * time.Millisecond):
			pending, listErr := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{
				ThreadID: thread.ID,
				RunID:    run.ID,
				Status:   "pending",
			})
			if listErr != nil {
				t.Fatalf("ListApprovals: %v", listErr)
			}
			if len(pending) > 0 {
				approval = pending[0]
			}
		}
	}
	if len(approval.Options) != 1 || approval.Options[0] != "once" {
		t.Fatalf("options=%v want [once]", approval.Options)
	}
	estimatedBytes := int64(0)
	switch value := approval.Preview["estimated_bytes"].(type) {
	case int64:
		estimatedBytes = value
	case float64:
		estimatedBytes = int64(value)
	}
	if approval.Operation != "query_scan_budget" ||
		approval.Subject.Connector != "bigquery" ||
		estimatedBytes != int64(250<<30) {
		t.Fatalf("approval=%+v", approval)
	}
	authorizedBytes := int64(0)
	switch value := approval.Preview["authorized_scan_bytes"].(type) {
	case int64:
		authorizedBytes = value
	case float64:
		authorizedBytes = int64(value)
	}
	if authorizedBytes != int64(525<<29) {
		t.Fatalf("authorized bytes=%d want %d", authorizedBytes, int64(525<<29))
	}
	if _, err := permissionSvc.Approve(ctx, approval.ID, "once"); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	select {
	case result := <-done:
		if result.err != nil || !result.decision.Approved || result.decision.ApprovalID != approval.ID {
			t.Fatalf("decision=%+v err=%v", result.decision, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("scan approval did not resume")
	}
}

func startRequireApprovalForTest(t *testing.T, fn func() (bool, bool, json.RawMessage, tools.Output)) <-chan requireApprovalOutcome {
	t.Helper()
	done := make(chan requireApprovalOutcome, 1)
	go func() {
		applied, allowed, _, out := fn()
		done <- requireApprovalOutcome{applied: applied, allowed: allowed, out: out}
	}()
	return done
}

func waitForPendingApprovalForTest(t *testing.T, ctx context.Context, svc *permissions.Service, threadID, runID string, done <-chan requireApprovalOutcome) store.ApprovalRequest {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case outcome := <-done:
			t.Fatalf("requireToolApproval returned before approval decision: applied=%v allowed=%v out=%#v", outcome.applied, outcome.allowed, outcome.out.Result)
		case <-ticker.C:
			pending, err := svc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: threadID, RunID: runID, Status: "pending"})
			if err != nil {
				t.Fatalf("ListApprovals: %v", err)
			}
			if len(pending) == 1 {
				return pending[0]
			}
			if len(pending) > 1 {
				t.Fatalf("pending approvals=%d, want 1: %#v", len(pending), pending)
			}
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		}
	}
}

func waitForPendingApprovalForArgsTest(t *testing.T, ctx context.Context, svc *permissions.Service, threadID, runID string, done <-chan requireApprovalArgsOutcome) store.ApprovalRequest {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case outcome := <-done:
			t.Fatalf("requireToolApproval returned before approval decision: applied=%v allowed=%v out=%#v", outcome.applied, outcome.allowed, outcome.out.Result)
		case <-ticker.C:
			pending, err := svc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: threadID, RunID: runID, Status: "pending"})
			if err != nil {
				t.Fatalf("ListApprovals: %v", err)
			}
			if len(pending) == 1 {
				return pending[0]
			}
			if len(pending) > 1 {
				t.Fatalf("pending approvals=%d, want 1: %#v", len(pending), pending)
			}
		case <-deadline:
			t.Fatal("timed out waiting for pending approval")
		}
	}
}

func rejectApprovalAndWaitForTest(t *testing.T, ctx context.Context, svc *permissions.Service, approvalID string, done <-chan requireApprovalOutcome) requireApprovalOutcome {
	t.Helper()
	if _, err := svc.Reject(ctx, approvalID); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	return waitForRequireApprovalOutcomeForTest(t, done)
}

func waitForRequireApprovalOutcomeForTest(t *testing.T, done <-chan requireApprovalOutcome) requireApprovalOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for requireToolApproval result")
	}
	return requireApprovalOutcome{}
}

func TestRuntimeTimeFromClientContextUsesUTCOffset(t *testing.T) {
	offset := -8 * 60
	got := runtimeTimeFromClientContext(StartRunRequest{
		ClientNowISO:           "2026-02-20T22:32:00Z",
		ClientUTCOffsetMinutes: &offset,
	})
	if got.Format(time.RFC3339) != "2026-02-20T14:32:00-08:00" {
		t.Fatalf("runtimeTimeFromClientContext offset=%q, want 2026-02-20T14:32:00-08:00", got.Format(time.RFC3339))
	}
}

func waitForRequireApprovalArgsOutcomeForTest(t *testing.T, done <-chan requireApprovalArgsOutcome) requireApprovalArgsOutcome {
	t.Helper()
	select {
	case outcome := <-done:
		return outcome
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for requireToolApproval outcome")
		return requireApprovalArgsOutcome{}
	}
}

func canonicalManagerTestPath(path string) string {
	clean := filepath.Clean(path)
	if evaluated, err := filepath.EvalSymlinks(clean); err == nil {
		return filepath.Clean(evaluated)
	}
	return clean
}

func TestRuntimeTimeFromClientContextUsesTimezoneWhenAvailable(t *testing.T) {
	got := runtimeTimeFromClientContext(StartRunRequest{
		ClientNowISO:   "2026-02-20T22:32:00Z",
		ClientTimezone: "UTC",
	})
	if got.Format(time.RFC3339) != "2026-02-20T22:32:00Z" {
		t.Fatalf("runtimeTimeFromClientContext timezone=%q, want 2026-02-20T22:32:00Z", got.Format(time.RFC3339))
	}
	if got.Location().String() != "UTC" {
		t.Fatalf("runtimeTimeFromClientContext location=%q, want UTC", got.Location().String())
	}
}

func TestManagerCancelBroadcastsCanceledApprovals(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	approval, err := permissionSvc.Request(ctx, permissions.RequestInput{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "rlm_exec",
		Category: permissions.CategoryExecute,
		Subject:  store.PermissionSubject{Kind: "tool", Tool: "rlm_exec"},
		Summary:  "Allow execution",
	})
	if err != nil {
		t.Fatalf("Request approval: %v", err)
	}
	manager := &Manager{
		store:       st,
		permissions: permissionSvc,
		bufferSize:  16,
		runs: map[string]*runState{
			run.ID: {
				threadID: thread.ID,
				subs:     map[int]chan SSEEvent{},
				doneCh:   make(chan struct{}),
			},
		},
		activeByThread: map[string]string{thread.ID: run.ID},
	}
	ch, cancelSub, err := manager.Subscribe(run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancelSub()

	if err := manager.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case ev := <-ch:
		if ev.Event != "approval" {
			t.Fatalf("event=%q want approval", ev.Event)
		}
		var payload struct {
			Approval struct {
				ID     string `json:"id"`
				Status string `json:"status"`
				State  string `json:"state"`
			} `json:"approval"`
		}
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("unmarshal approval event: %v", err)
		}
		if payload.Approval.ID != approval.ID {
			t.Fatalf("approval id=%q want %q", payload.Approval.ID, approval.ID)
		}
		if payload.Approval.Status != "canceled" || payload.Approval.State != "canceled" {
			t.Fatalf("approval status/state=%q/%q want canceled/canceled", payload.Approval.Status, payload.Approval.State)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled approval event")
	}

	stored, err := st.GetApprovalRequest(ctx, approval.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest: %v", err)
	}
	if stored.Status != "canceled" {
		t.Fatalf("stored approval status=%q want canceled", stored.Status)
	}
}

func TestHostedToolPolicyForRunEvaluatesPersistentPermissions(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}

	policy, err := manager.hostedToolPolicyForRun(ctx, "thread-hosted")
	if err != nil {
		t.Fatalf("hostedToolPolicyForRun default: %v", err)
	}
	if policy.Disables("openai-responses/web_search") {
		t.Fatal("read hosted web search should be allowed by default")
	}
	if !policy.Disables("openai-responses/code_interpreter") || !policy.Disables("anthropic-messages/code_execution") {
		t.Fatalf("execute hosted tools should be disabled until explicitly allowed: %#v", policy.Disabled)
	}

	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "tool",
			Tool:      "openai-responses/web_search",
			ToolSetID: "provider-hosted",
			Connector: "openai-responses",
			Operation: "webSearch",
		},
	}); err != nil {
		t.Fatalf("CreateRule stale generic hosted web allow: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectDeny,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "provider_hosted_tool",
			Tool:      "openai-responses/web_search",
			ToolSetID: "provider-hosted",
			Connector: "openai-responses",
			Operation: "webSearch",
		},
	}); err != nil {
		t.Fatalf("CreateRule hosted web deny: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryExecute,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject:  store.PermissionSubject{Kind: "tool_set", ToolSetID: "provider-hosted"},
	}); err != nil {
		t.Fatalf("CreateRule hosted execute allow: %v", err)
	}

	policy, err = manager.hostedToolPolicyForRun(ctx, "thread-hosted")
	if err != nil {
		t.Fatalf("hostedToolPolicyForRun with rules: %v", err)
	}
	if policy.Disables("openai-responses/web_search") {
		t.Fatalf("legacy hosted web deny overrode the Web Search connection: %#v", policy.Disabled)
	}
	if policy.Disables("anthropic-messages/web_search") {
		t.Fatalf("OpenAI hosted web deny should not disable Anthropic hosted web search: %#v", policy.Disabled)
	}
	if policy.Disables("openai-responses/code_interpreter") || policy.Disables("anthropic-messages/code_execution") {
		t.Fatalf("provider-hosted execute tool-set allow should enable code tools: %#v", policy.Disabled)
	}

	scopedPolicy, err := manager.hostedToolPolicyForRun(ctx, "thread-hosted", newRunConnectionScope([]string{"python-sandbox"}))
	if err != nil {
		t.Fatalf("hostedToolPolicyForRun with connection scope: %v", err)
	}
	if scopedPolicy.Disables("openai-responses/web_search") || scopedPolicy.Disables("anthropic-messages/web_search") {
		t.Fatalf("legacy client scope overrode the server-side Web Search connection: %#v", scopedPolicy.Disabled)
	}
	if scopedPolicy.Disables("openai-responses/code_interpreter") || scopedPolicy.Disables("anthropic-messages/code_execution") {
		t.Fatalf("armed Python Sandbox should retain allowed code tools: %#v", scopedPolicy.Disabled)
	}
}

func TestHostedToolPolicyAppliesConnectionScopeWithoutPermissionService(t *testing.T) {
	manager := &Manager{}
	policy, err := manager.hostedToolPolicyForRun(
		context.Background(),
		"thread-hosted-scope",
		newRunConnectionScope([]string{"web-search"}),
	)
	if err != nil {
		t.Fatalf("hostedToolPolicyForRun: %v", err)
	}
	if policy.Disables("openai-responses/web_search") || policy.Disables("anthropic-messages/web_search") {
		t.Fatalf("armed Web Search should remain available: %#v", policy.Disabled)
	}
	if !policy.Disables("openai-responses/code_interpreter") || !policy.Disables("anthropic-messages/code_execution") {
		t.Fatalf("unarmed Python Sandbox should be disabled: %#v", policy.Disabled)
	}
}

func TestRequireToolApprovalWaitsUntilDecision(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		permissions:  permissionSvc,
		approvalWait: 5 * time.Millisecond,
	}

	done := startRequireApprovalForTest(t, func() (bool, bool, json.RawMessage, tools.Output) {
		return manager.requireToolApproval(
			ctx,
			thread,
			run,
			types.ToolCall{ID: "tc_rlm", Name: "rlm_exec"},
			"rlm_exec",
			json.RawMessage(`{"code":"print('hello')"}`),
			"",
			nil,
		)
	})
	approval := waitForPendingApprovalForTest(t, ctx, permissionSvc, thread.ID, run.ID, done)
	select {
	case outcome := <-done:
		t.Fatalf("requireToolApproval returned before decision despite approvalWait: applied=%v allowed=%v out=%#v", outcome.applied, outcome.allowed, outcome.out.Result)
	case <-time.After(20 * time.Millisecond):
	}
	if approval.Status != "pending" {
		t.Fatalf("stored approval status=%q want pending", approval.Status)
	}
	if containsStringForTest(approval.Options, "always") {
		t.Fatalf("broad execute approval options=%v should not include always", approval.Options)
	}
	outcome := rejectApprovalAndWaitForTest(t, ctx, permissionSvc, approval.ID, done)
	if outcome.applied || outcome.allowed {
		t.Fatalf("after reject applied=%v allowed=%v, want both false", outcome.applied, outcome.allowed)
	}
	payload, ok := outcome.out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", outcome.out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
}

func TestRequireToolApprovalOffersAlwaysForScopedExecute(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		permissions:  permissionSvc,
		approvalWait: 5 * time.Millisecond,
	}

	done := startRequireApprovalForTest(t, func() (bool, bool, json.RawMessage, tools.Output) {
		return manager.requireToolApproval(
			ctx,
			thread,
			run,
			types.ToolCall{ID: "tc_clone", Name: tools.GitHubToolCloneRepository},
			tools.GitHubToolCloneRepository,
			json.RawMessage(`{"repository":"owner/repo"}`),
			"",
			nil,
		)
	})
	approval := waitForPendingApprovalForTest(t, ctx, permissionSvc, thread.ID, run.ID, done)
	defer rejectApprovalAndWaitForTest(t, ctx, permissionSvc, approval.ID, done)
	if !containsStringForTest(approval.Options, "always") {
		t.Fatalf("scoped execute approval options=%v should include always", approval.Options)
	}
	if approval.Subject.Kind != "repository" || approval.Subject.Repository != "owner/repo" {
		t.Fatalf("approval subject=%#v, want repository owner/repo", approval.Subject)
	}
	if approval.Subject.RemoteKind != "github_repository" || approval.Subject.RemoteID != "owner/repo" {
		t.Fatalf("approval remote target=%#v/%#v, want github_repository owner/repo", approval.Subject.RemoteKind, approval.Subject.RemoteID)
	}
}

func TestRequireToolApprovalDoesNotOfferAlwaysForUnscopedBrowserExecute(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		permissions:  permissionSvc,
		approvalWait: 5 * time.Millisecond,
	}

	done := startRequireApprovalForTest(t, func() (bool, bool, json.RawMessage, tools.Output) {
		return manager.requireToolApproval(
			ctx,
			thread,
			run,
			types.ToolCall{ID: "tc_browser_click", Name: tools.BrowserControlToolClick},
			tools.BrowserControlToolClick,
			json.RawMessage(`{"selector":"button.primary"}`),
			"",
			nil,
		)
	})
	approval := waitForPendingApprovalForTest(t, ctx, permissionSvc, thread.ID, run.ID, done)
	defer rejectApprovalAndWaitForTest(t, ctx, permissionSvc, approval.ID, done)
	if containsStringForTest(approval.Options, "always") {
		t.Fatalf("unscoped browser execute approval options=%v should not include always", approval.Options)
	}
	if approval.Subject.Kind != "tool" ||
		approval.Subject.Tool != tools.BrowserControlToolClick ||
		approval.Subject.Connector != "browser" ||
		approval.Subject.Operation != "click" {
		t.Fatalf("approval subject=%#v, want browser click tool subject", approval.Subject)
	}
}

func TestRequireToolApprovalOffersAlwaysForBrowserNavigateOrigin(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		permissions:  permissionSvc,
		approvalWait: 5 * time.Millisecond,
	}

	done := startRequireApprovalForTest(t, func() (bool, bool, json.RawMessage, tools.Output) {
		return manager.requireToolApproval(
			ctx,
			thread,
			run,
			types.ToolCall{ID: "tc_browser_navigate", Name: tools.BrowserControlToolNavigate},
			tools.BrowserControlToolNavigate,
			json.RawMessage(`{"url":"https://Example.com:8443/path?q=1"}`),
			"",
			nil,
		)
	})
	approval := waitForPendingApprovalForTest(t, ctx, permissionSvc, thread.ID, run.ID, done)
	defer rejectApprovalAndWaitForTest(t, ctx, permissionSvc, approval.ID, done)
	if !containsStringForTest(approval.Options, "always") {
		t.Fatalf("origin-scoped browser navigate approval options=%v should include always", approval.Options)
	}
	if approval.Subject.Kind != "tool" ||
		approval.Subject.Tool != tools.BrowserControlToolNavigate ||
		approval.Subject.Connector != "browser" ||
		approval.Subject.Operation != "navigate" ||
		approval.Subject.RemoteKind != "browser_origin" ||
		approval.Subject.RemoteID != "https://example.com:8443" {
		t.Fatalf("approval subject=%#v, want browser origin subject", approval.Subject)
	}
}

func TestRequireToolApprovalReusesPendingApprovalWithoutDuplicateBroadcast(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		store:        st,
		permissions:  permissionSvc,
		approvalWait: 5 * time.Millisecond,
		bufferSize:   16,
		runs: map[string]*runState{
			run.ID: {
				threadID: thread.ID,
				subs:     map[int]chan SSEEvent{},
				doneCh:   make(chan struct{}),
			},
		},
		activeByThread: map[string]string{thread.ID: run.ID},
	}
	ch, cancelSub, err := manager.Subscribe(run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancelSub()

	args := json.RawMessage(`{"repository":"owner/repo"}`)
	call := types.ToolCall{ID: "tc_clone_once", Name: tools.GitHubToolCloneRepository}
	firstDone := startRequireApprovalForTest(t, func() (bool, bool, json.RawMessage, tools.Output) {
		return manager.requireToolApproval(ctx, thread, run, call, tools.GitHubToolCloneRepository, args, "", nil)
	})
	firstApproval := waitForPendingApprovalForTest(t, ctx, permissionSvc, thread.ID, run.ID, firstDone)
	firstApprovalID := firstApproval.ID

	select {
	case ev := <-ch:
		if ev.Event != "approval" {
			t.Fatalf("first event=%q, want approval", ev.Event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first approval broadcast")
	}

	secondDone := startRequireApprovalForTest(t, func() (bool, bool, json.RawMessage, tools.Output) {
		return manager.requireToolApproval(ctx, thread, run, call, tools.GitHubToolCloneRepository, args, "", nil)
	})
	select {
	case ev := <-ch:
		t.Fatalf("unexpected duplicate approval broadcast: %#v", ev)
	case <-time.After(25 * time.Millisecond):
	}

	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending approvals=%d, want 1", len(pending))
	}
	if pending[0].ID != firstApprovalID {
		t.Fatalf("pending approval ID=%q, want reused %q", pending[0].ID, firstApprovalID)
	}
	firstOutcome := rejectApprovalAndWaitForTest(t, ctx, permissionSvc, firstApprovalID, firstDone)
	secondOutcome := waitForRequireApprovalOutcomeForTest(t, secondDone)
	for label, outcome := range map[string]requireApprovalOutcome{"first": firstOutcome, "second": secondOutcome} {
		if outcome.applied || outcome.allowed {
			t.Fatalf("%s outcome applied=%v allowed=%v, want both false", label, outcome.applied, outcome.allowed)
		}
		payload, ok := outcome.out.Result.(map[string]any)
		if !ok || payload["status"] != "approval_denied" {
			t.Fatalf("%s outcome result=%#v, want approval_denied", label, outcome.out.Result)
		}
	}
}

func TestRequireToolApprovalDoesNotPromptForSelectedLocalRoot(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast","location_hint":"Documents"}`),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want Local Computer scope failure", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalUsesSingleSavedLocalGrant(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: root,
		},
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search_saved", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast"}`),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want saved grant ignored", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalPrefersWholeComputerGrant(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	wholeComputerRoot := string(os.PathSeparator)
	if volume := filepath.VolumeName(os.TempDir()); volume != "" {
		wholeComputerRoot = volume + string(os.PathSeparator)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: wholeComputerRoot,
			LocalKind: "folder",
		},
	}); err != nil {
		t.Fatalf("CreateRule whole computer: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: t.TempDir(),
			LocalKind: "folder",
		},
	}); err != nil {
		t.Fatalf("CreateRule folder: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search_whole_computer", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast"}`),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want saved whole-computer grant ignored", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalUsesLocalComputerSelectedFolderWithoutSavedRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{root},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search_local_computer", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast"}`),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer settings to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	wantRoot := canonicalManagerTestPath(root)
	if args["local_root"] != wantRoot || args["local_kind"] != "folder" {
		t.Fatalf("approved args=%#v, want local_root %q from Local Computer settings", args, wantRoot)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want Local Computer settings to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalUsesLocalComputerWholeComputerWithoutSavedRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeWholeComputer
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:    &enabled,
		AccessMode: &mode,
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search_local_computer_whole", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast"}`),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer whole-computer settings to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	wantRoot := canonicalManagerTestPath(localComputerWholeComputerRoot())
	if args["local_root"] != wantRoot || args["local_kind"] != "folder" {
		t.Fatalf("approved args=%#v, want whole-computer local_root %q", args, wantRoot)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want Local Computer whole-computer settings to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalUsesLocalComputerSelectedFoldersForRootlessSearch(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rootA := t.TempDir()
	rootB := t.TempDir()
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{rootA, rootB},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search_local_computer_multi", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast"}`),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer selected folders to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	if _, ok := args["local_root"]; ok {
		t.Fatalf("approved args=%#v, want multi-root search without single local_root", args)
	}
	rawRoots, ok := args["local_roots"].([]any)
	if !ok || len(rawRoots) != 2 {
		t.Fatalf("local_roots=%#v, want two configured folders", args["local_roots"])
	}
	gotRoots := map[string]bool{}
	for _, raw := range rawRoots {
		root, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("local_roots entry=%#v, want object", raw)
		}
		gotRoots[fmt.Sprint(root["local_root"])] = true
		if root["local_kind"] != "folder" || strings.TrimSpace(fmt.Sprint(root["grant_id"])) == "" {
			t.Fatalf("local_roots entry=%#v, want folder with grant_id", root)
		}
	}
	if !gotRoots[canonicalManagerTestPath(rootA)] || !gotRoots[canonicalManagerTestPath(rootB)] {
		t.Fatalf("local_roots=%#v, want %q and %q", rawRoots, canonicalManagerTestPath(rootA), canonicalManagerTestPath(rootB))
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want Local Computer selected folders to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalLocalComputerRootlessSearchWithoutConfiguredScopeDoesNotPrompt(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_file_search_no_local_computer_scope", Name: "file_search"},
		"file_search",
		json.RawMessage(`{"query":"Q3 forecast"}`),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want Local Computer scope failure", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalLocalComputerReadOutsideScopeDoesNotPrompt(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	allowedRoot := t.TempDir()
	outsideRoot := t.TempDir()
	outsideFile := filepath.Join(outsideRoot, "outside.txt")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{allowedRoot},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_read_file_outside_scope", Name: "local_read_file"},
		"local_read_file",
		mustJSONForPermissionTest(map[string]any{"path": outsideFile}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want Local Computer outside-scope failure", applied, allowed)
	}
	payload := requireLocalComputerScopeRequiredForPermissionTest(t, out)
	if payload["tool"] != "local_read_file" {
		t.Fatalf("payload=%#v, want local_read_file tool", payload)
	}
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalLocalComputerPDFReadOutsideScopeDoesNotPrompt(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	allowedRoot := t.TempDir()
	outsideRoot := t.TempDir()
	outsidePDF := filepath.Join(outsideRoot, "outside.pdf")
	if err := os.WriteFile(outsidePDF, []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{allowedRoot},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_read_pdf_page_outside_scope", Name: "local_read_pdf_page"},
		"local_read_pdf_page",
		mustJSONForPermissionTest(map[string]any{"path": outsidePDF, "page_number": 1}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want Local Computer PDF outside-scope failure", applied, allowed)
	}
	payload := requireLocalComputerScopeRequiredForPermissionTest(t, out)
	if payload["tool"] != "local_read_pdf_page" {
		t.Fatalf("payload=%#v, want local_read_pdf_page tool", payload)
	}
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalLocalComputerRelativeReadNoMatchDoesNotPrompt(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rootA := t.TempDir()
	rootB := t.TempDir()
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{rootA, rootB},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_read_file_relative_missing", Name: "local_read_file"},
		"local_read_file",
		mustJSONForPermissionTest(map[string]any{"path": "docs/missing.txt"}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want Local Computer no-match failure", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalMapsRelativeLocalComputerPDFPathAcrossSelectedFolders(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rootA := t.TempDir()
	rootB := t.TempDir()
	filePath := filepath.Join(rootB, "docs", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{rootA, rootB},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_read_pdf_page_relative", Name: "local_read_pdf_page"},
		"local_read_pdf_page",
		mustJSONForPermissionTest(map[string]any{"path": "docs/report.pdf", "page_number": 1}),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want matching Local Computer folder to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	wantGrantID := localComputerPermissionGrantID(canonicalManagerTestPath(rootB))
	if args["local_root"] != canonicalManagerTestPath(rootB) || args["local_kind"] != "folder" || args["grant_id"] != wantGrantID || args["path"] != "docs/report.pdf" {
		t.Fatalf("approved args=%#v, want rootB grant and relative PDF path", args)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want relative Local Computer PDF read to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalMapsRelativeLocalComputerOpenFileAcrossSelectedFolders(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rootA := t.TempDir()
	rootB := t.TempDir()
	filePath := filepath.Join(rootB, "docs", "report.pdf")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("%PDF-1.7\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{rootA, rootB},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_open_file_relative", Name: "local_open_file"},
		"local_open_file",
		mustJSONForPermissionTest(map[string]any{"path": "docs/report.pdf"}),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want matching Local Computer folder to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	wantGrantID := localComputerPermissionGrantID(canonicalManagerTestPath(rootB))
	if args["local_root"] != canonicalManagerTestPath(rootB) || args["local_kind"] != "folder" || args["grant_id"] != wantGrantID || args["path"] != "docs/report.pdf" {
		t.Fatalf("approved args=%#v, want rootB grant and relative open-file path", args)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want covered Local Computer open-file call to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalMapsAbsoluteLocalComputerReadPath(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	filePath := filepath.Join(root, "docs", "report.txt")
	if err := os.WriteFile(filePath, []byte("forecast"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{root},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_read_file_path", Name: "local_read_file"},
		"local_read_file",
		mustJSONForPermissionTest(map[string]any{"path": filePath}),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer path to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	if args["local_root"] != canonicalManagerTestPath(root) || args["local_kind"] != "folder" || args["path"] != "docs/report.txt" {
		t.Fatalf("approved args=%#v, want local_root and relative path", args)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want Local Computer path to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalMapsAbsoluteLocalComputerFolderPath(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	folderPath := filepath.Join(root, "docs")
	if err := os.MkdirAll(folderPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{root},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_list_folder_path", Name: "local_list_folder"},
		"local_list_folder",
		mustJSONForPermissionTest(map[string]any{"path": folderPath}),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer folder path to allow", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	if args["local_root"] != canonicalManagerTestPath(root) || args["local_kind"] != "folder" || args["path"] != "docs" {
		t.Fatalf("approved args=%#v, want local_root and relative folder path", args)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want Local Computer folder path to skip prompt", len(pending))
	}
}

func TestRequireToolApprovalLocalComputerBypassesGenericToolAsk(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	filePath := filepath.Join(root, "reports", "summary.txt")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filePath, []byte("forecast"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{root},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAsk,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind: "tool",
			Tool: "local_read_file",
		},
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	applied, allowed, approvedArgs, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_local_read_file_tool_ask", Name: "local_read_file"},
		"local_read_file",
		mustJSONForPermissionTest(map[string]any{"path": filePath}),
		"",
		nil,
	)
	if !applied || !allowed {
		t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer grant to bypass generic tool ask", applied, allowed, out.Result)
	}
	var args map[string]any
	if err := json.Unmarshal(approvedArgs, &args); err != nil {
		t.Fatalf("approved args JSON: %v", err)
	}
	if args["local_root"] != canonicalManagerTestPath(root) || args["local_kind"] != "folder" || args["path"] != "reports/summary.txt" {
		t.Fatalf("approved args=%#v, want configured local_root and relative path", args)
	}
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want configured Local Computer folder to skip prompt despite tool ask", len(pending))
	}
}

func TestEmbedLocalComputerAccessGuidanceListsGrantedFolders(t *testing.T) {
	root := canonicalManagerTestPath(t.TempDir())
	prompt := embedLocalComputerAccessGuidance("Base prompt.", []localComputerPermissionGrant{{
		Root: root,
		Kind: "folder",
	}})
	for _, want := range []string{
		"Base prompt.",
		"Local file access:",
		"is configured",
		"Do not ask the user for permission again",
		"exact PDF page questions",
		"avoid whole-document extraction",
		"local_read_pdf_page",
		"1-based page number",
		"page_count and method metadata",
		root,
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestLocalReadPermissionGrantsForRunUsesLocalComputerSettingsOnly(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	savedRuleRoot := t.TempDir()
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: savedRuleRoot,
			LocalKind: "folder",
		},
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	configuredRoot := t.TempDir()
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := config.AppSettings{
		LocalComputer: &config.LocalComputerSettings{
			Enabled:         &enabled,
			AccessMode:      &mode,
			SelectedFolders: []string{configuredRoot},
		},
	}

	manager := &Manager{permissions: permissionSvc}
	grants := manager.localReadPermissionGrantsForRun(ctx, "", "", settings)
	if len(grants) != 1 || grants[0].Root != canonicalManagerTestPath(configuredRoot) || grants[0].Kind != "folder" {
		t.Fatalf("grants=%#v, want configured Local Computer folder only", grants)
	}
	prompt := embedLocalComputerAccessGuidance("Base prompt.", grants)
	for _, want := range []string{
		"Local file access:",
		"is configured",
		"Locate the PDF through configured local access",
		"local_read_pdf_page",
		canonicalManagerTestPath(configuredRoot),
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, canonicalManagerTestPath(savedRuleRoot)) {
		t.Fatalf("prompt includes saved rule root %q, want only settings roots:\n%s", savedRuleRoot, prompt)
	}
}

func TestRequireToolApprovalIgnoresSavedLocalRuleForLocalComputerRead(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	filePath := filepath.Join(root, "docs", "saved-report.txt")
	if err := os.WriteFile(filePath, []byte("forecast"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: root,
			LocalKind: "folder",
		},
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_saved_local_read_file_path", Name: "local_read_file"},
		"local_read_file",
		mustJSONForPermissionTest(map[string]any{"path": filePath}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want saved local rule ignored for Local Computer tool", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalResolvesLocalComputerSyntheticGrantID(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "reports"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "reports", "summary.txt"), []byte("forecast"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{root},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{permissions: permissionSvc, settings: settingsStore}
	grantID := localComputerPermissionGrantID(canonicalManagerTestPath(root))
	for _, tc := range []struct {
		name string
		args map[string]any
	}{
		{
			name: tools.LocalSearchCommandToolName,
			args: map[string]any{"grant_id": grantID, "program": "auto", "query": "forecast"},
		},
		{
			name: "local_search_in_grant",
			args: map[string]any{"grant_id": grantID, "query": "forecast"},
		},
		{
			name: "local_list_grant_folder",
			args: map[string]any{"grant_id": grantID, "relative_path": "reports"},
		},
		{
			name: "local_read_grant_file",
			args: map[string]any{"grant_id": grantID, "file_ref": "reports/summary.txt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			applied, allowed, approvedArgs, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_" + tc.name, Name: tc.name},
				tc.name,
				mustJSONForPermissionTest(tc.args),
				"",
				nil,
			)
			if !applied || !allowed {
				t.Fatalf("applied=%v allowed=%v out=%#v, want Local Computer synthetic grant id allow", applied, allowed, out.Result)
			}
			var args map[string]any
			if err := json.Unmarshal(approvedArgs, &args); err != nil {
				t.Fatalf("approved args JSON: %v", err)
			}
			if args["grant_id"] != grantID || args["local_root"] != canonicalManagerTestPath(root) || args["local_kind"] != "folder" {
				t.Fatalf("approved args=%#v, want synthetic grant id and configured local_root", args)
			}
			pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: thread.ID, RunID: run.ID, Status: "pending"})
			if err != nil {
				t.Fatalf("ListApprovals: %v", err)
			}
			if len(pending) != 0 {
				t.Fatalf("pending approvals=%d, want Local Computer synthetic grant id to skip prompt", len(pending))
			}
		})
	}
}

func TestRequireToolApprovalIgnoresSavedRuleInternalGrantID(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	rule, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: root,
		},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_grant_search", Name: "local_search_in_grant"},
		"local_search_in_grant",
		mustJSONForPermissionTest(map[string]any{"grant_id": rule.ID, "query": "Q3"}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want saved rule grant ignored", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)

	applied, allowed, _, out = manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_grant_list", Name: "local_list_grant_folder"},
		"local_list_grant_folder",
		mustJSONForPermissionTest(map[string]any{"grant_id": rule.ID, "relative_path": "reports"}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("list applied=%v allowed=%v, want saved rule grant ignored", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalIgnoresSavedRuleInternalFileGrantID(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	root := t.TempDir()
	filePath := filepath.Join(root, "forecast.txt")
	if err := os.WriteFile(filePath, []byte("Q3"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	rule, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: filePath,
			LocalKind: "file",
		},
	})
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_grant_file_read", Name: "local_read_grant_file"},
		"local_read_grant_file",
		mustJSONForPermissionTest(map[string]any{"grant_id": rule.ID, "file_ref": "forecast.txt"}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want saved file grant ignored", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestRequireToolApprovalDeniesUnknownInternalGrantID(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_grant_search_unknown", Name: "local_search_in_grant"},
		"local_search_in_grant",
		mustJSONForPermissionTest(map[string]any{"grant_id": "perm_missing", "query": "Q3"}),
		"",
		nil,
	)
	if applied || allowed {
		t.Fatalf("applied=%v allowed=%v, want denied unknown grant", applied, allowed)
	}
	requireLocalComputerScopeRequiredForPermissionTest(t, out)
	requireNoPendingApprovalsForPermissionTest(t, ctx, permissionSvc, thread.ID, run.ID)
}

func TestManagerExecTool_SearchModeBlocksNonSearchTool(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store:  st,
		tools:  tools.NewRegistry(nil),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_blocked",
		Name: "list_files",
	}, true, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content, got nil")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	if env["tool_name"] != "list_files" {
		t.Fatalf("tool_name = %#v, want list_files", env["tool_name"])
	}
	okVal, ok := env["ok"].(bool)
	if !ok || okVal {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	errMsg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(errMsg, "tool is not allowed in search mode") {
		t.Fatalf("error = %q, want blocked tool message", errMsg)
	}
	resultMetadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing from envelope: %#v", env["result_metadata"])
	}
	diagnostic, ok := resultMetadata["diagnostic"].(map[string]any)
	if !ok {
		t.Fatalf("diagnostic missing from error result: %#v", resultMetadata["diagnostic"])
	}
	if strings.TrimSpace(fmt.Sprint(diagnostic["why"])) == "" {
		t.Fatalf("diagnostic why missing: %#v", diagnostic)
	}
	if required, _ := resultMetadata["model_commentary_required"].(bool); !required {
		t.Fatalf("model_commentary_required=%#v, want true", resultMetadata["model_commentary_required"])
	}
}

func TestManagerTestToolRunsReadToolAgainstProject(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-tool-test"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	manager := &Manager{
		tools:    tools.NewRegistry(nil),
		projects: ps,
		cfgDir:   cfgDir,
	}

	result, err := manager.TestTool(ctx, ToolTestRequest{
		ToolName:  "list_files",
		ProjectID: projectID,
	})
	if err != nil {
		t.Fatalf("TestTool: %v", err)
	}
	if result.ToolName != "list_files" || result.Mode != tools.ToolTestModeSampleCall {
		t.Fatalf("result metadata=%#v", result)
	}
	if result.Presentation.Family != "local-files" ||
		result.Presentation.ResultKind != "repository" ||
		result.Presentation.RendererStrategy != "family" ||
		result.Presentation.RendererKey != "local-files" {
		t.Fatalf("presentation=%#v, want local-files repository presentation", result.Presentation)
	}
	if result.Output.ResultMetadata["integration"] != "local-files" ||
		result.Output.ResultMetadata["renderer_strategy"] != "family" ||
		result.Output.ResultMetadata["renderer_key"] != "local-files" {
		t.Fatalf("result_metadata=%#v, want canonical local-files metadata", result.Output.ResultMetadata)
	}
	if _, ok := result.Output.ResultMetadata["tool_presentation"]; !ok {
		t.Fatalf("tool_presentation missing from result_metadata: %#v", result.Output.ResultMetadata)
	}
	if !result.Output.OK {
		t.Fatalf("expected OK output, got %#v", result.Output.Result)
	}
	if len(result.Args) != 0 {
		t.Fatalf("args=%#v want empty prepared args", result.Args)
	}
	payload, ok := result.Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("output result=%T want object", result.Output.Result)
	}
	if payload["total_count"] == 0 {
		t.Fatalf("total_count=%#v want files listed", payload["total_count"])
	}
}

func TestManagerTestToolUsesLocalComputerSettingsGrant(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	settingsStore, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "readme.md"), []byte("nested note target"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	otherRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(otherRoot, "unrelated.txt"), []byte("other granted folder"), 0o644); err != nil {
		t.Fatalf("WriteFile otherRoot: %v", err)
	}
	enabled := true
	mode := config.LocalComputerAccessModeSelectedFolders
	settings := settingsStore.Get()
	settings.LocalComputer = &config.LocalComputerSettings{
		Enabled:         &enabled,
		AccessMode:      &mode,
		SelectedFolders: []string{root},
	}
	if err := settingsStore.Save(settings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: otherRoot,
			LocalKind: "folder",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{
		store:       st,
		settings:    settingsStore,
		permissions: permissionSvc,
		tools:       tools.NewRegistry(nil),
		cfgDir:      cfgDir,
	}
	canonicalRoot := canonicalManagerTestPath(root)
	grantID := localComputerPermissionGrantID(canonicalRoot)

	for _, tc := range []struct {
		name             string
		args             map[string]any
		wantPath         string
		wantGrant        bool
		wantSearch       bool
		wantRoots        bool
		allowOutputError bool
	}{
		{
			name:     "local_list_folder",
			args:     map[string]any{"path": filepath.Join(root, "notes")},
			wantPath: "notes",
		},
		{
			name:     "local_read_file",
			args:     map[string]any{"path": filepath.Join(root, "notes", "readme.md")},
			wantPath: "notes/readme.md",
		},
		{
			name:             "file_search",
			args:             map[string]any{"query": "nested note"},
			allowOutputError: true,
		},
		{
			name:       "local_search_in_grant",
			args:       map[string]any{"grant_id": grantID, "query": "nested note"},
			wantGrant:  true,
			wantSearch: true,
		},
		{
			name:      "local_read_grant_file",
			args:      map[string]any{"grant_id": grantID, "file_ref": "notes/readme.md"},
			wantPath:  "notes/readme.md",
			wantGrant: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := manager.TestTool(ctx, ToolTestRequest{
				ToolName: tc.name,
				ThreadID: thread.ID,
				Args:     mustJSONForPermissionTest(tc.args),
			})
			if err != nil {
				t.Fatalf("TestTool: %T %v", err, err)
			}
			if !result.Output.OK && !tc.allowOutputError {
				t.Fatalf("Output=%#v, want OK", result.Output.Result)
			}
			if tc.wantRoots {
				roots, ok := result.Args["local_roots"].([]any)
				if !ok {
					t.Fatalf("args=%#v, want injected local_roots", result.Args)
				}
				found := false
				for _, raw := range roots {
					rootMap, ok := raw.(map[string]any)
					if ok && rootMap["local_root"] == canonicalRoot && rootMap["local_kind"] == "folder" {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("local_roots=%#v, want %q", roots, canonicalRoot)
				}
			} else if result.Args["local_root"] != canonicalRoot || result.Args["local_kind"] != "folder" {
				t.Fatalf("args=%#v, want injected local_root=%q local_kind=folder", result.Args, canonicalRoot)
			}
			if tc.wantGrant && result.Args["grant_id"] != grantID {
				t.Fatalf("args=%#v, want grant_id=%q", result.Args, grantID)
			}
			if tc.wantPath != "" && result.Args["path"] != tc.wantPath && result.Args["file_ref"] != tc.wantPath {
				t.Fatalf("args=%#v, want relative path or file_ref %q", result.Args, tc.wantPath)
			}
			if tc.wantSearch {
				payload, ok := result.Output.Result.(map[string]any)
				if !ok || payload["total_count"] == 0 {
					t.Fatalf("search result=%#v, want at least one match", result.Output.Result)
				}
			}
		})
	}
}

func TestManagerTestToolRunsDisplayTableAgainstSourceArtifact(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := "display_table"
	rows := `[{"name":"A","value":10},{"name":"B","value":20}]`
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact-display-source",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Rows",
		ToolName:    &toolName,
		Storage:     "mysql",
		ContentText: &rows,
		ByteSize:    int64(len(rows)),
		Metadata: map[string]any{
			"content_kind":          "json_rows",
			"contains_full_content": true,
			"row_count_total":       2,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	manager := &Manager{
		tools:  tools.NewRegistry(nil),
		store:  st,
		cfgDir: cfgDir,
	}

	result, err := manager.TestTool(ctx, ToolTestRequest{
		ToolName: "display_table",
		Args:     json.RawMessage(`{"source":{"artifact_id":"artifact-display-source"},"title":"Rows"}`),
	})
	if err != nil {
		t.Fatalf("TestTool: %v", err)
	}
	if !result.Output.OK {
		t.Fatalf("expected OK output, got %#v", result.Output.Result)
	}
	source, ok := result.Args["source"].(map[string]any)
	if !ok || source["artifact_id"] != "artifact-display-source" {
		t.Fatalf("args=%#v want source artifact", result.Args)
	}
}

func TestBuildArtifactAddsCanonicalToolPresentationMetadata(t *testing.T) {
	out := tools.JSONOK(map[string]any{"items": []any{}})
	_, _, _, _, metadata := buildArtifact("sharepoint_search", out, `{"items":[]}`)
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing: %#v", metadata["result_metadata"])
	}
	if resultMetadata["integration"] != "sharepoint" ||
		resultMetadata["product"] != "sharepoint" ||
		resultMetadata["kind"] != "connector" ||
		resultMetadata["renderer_strategy"] != "family" ||
		resultMetadata["renderer_key"] != "sharepoint" {
		t.Fatalf("result_metadata=%#v, want SharePoint connector presentation metadata", resultMetadata)
	}
	if _, ok := resultMetadata["tool_presentation"]; !ok {
		t.Fatalf("tool_presentation missing from result_metadata: %#v", resultMetadata)
	}
	if _, ok := metadata["tool_presentation"]; ok {
		t.Fatalf("buildArtifact should keep tool_presentation inside result_metadata until persistence adds top-level metadata: %#v", metadata)
	}
}

func TestPersistToolResultAddsToolArgsAndPresentationMetadata(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "tool-test", "test-provider", nil, "test", 1)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}
	call := types.ToolCall{
		ID:   "call-slack-list",
		Name: tools.SlackToolListConversations,
		Args: map[string]any{"limit": float64(5)},
	}
	msg := manager.persistToolResult(ctx, thread, run, call, tools.JSONOK(map[string]any{"channels": []any{}}), RunContextPlan{})
	if msg.Content == nil {
		t.Fatal("persisted tool message has nil content")
	}
	env, ok := types.ParseToolResultEnvelope(*msg.Content)
	if !ok {
		t.Fatalf("tool result envelope not parseable: %s", *msg.Content)
	}
	if env.ResultMetadata["integration"] != "slack" || env.ResultMetadata["renderer_key"] != "slack" {
		t.Fatalf("result_metadata=%#v, want Slack canonical metadata", env.ResultMetadata)
	}
	if env.ToolArgs["limit"] != float64(5) {
		t.Fatalf("envelope tool_args=%#v, want live call args", env.ToolArgs)
	}
	artifact, err := st.GetArtifact(ctx, env.ArtifactID)
	if err != nil {
		t.Fatalf("GetArtifact(%s): %v", env.ArtifactID, err)
	}
	toolArgs, ok := artifact.Metadata["tool_args"].(map[string]any)
	if !ok || toolArgs["limit"] != float64(5) {
		t.Fatalf("tool_args=%#v, want persisted call args", artifact.Metadata["tool_args"])
	}
	if _, ok := artifact.Metadata["tool_presentation"]; !ok {
		t.Fatalf("tool_presentation missing from artifact metadata: %#v", artifact.Metadata)
	}
}

func TestManagerTestToolInjectsDryRunForExecuteTool(t *testing.T) {
	ctx := context.Background()
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	var cancelCalls atomic.Int32
	job, err := reg.Register(
		jobs.JobOwner{ThreadID: "thread-1", RunID: "run-1", Handle: "tok_owner"},
		"bigquery",
		"select 1",
		"select 1",
		nil,
		nil,
		func(ctx context.Context) error {
			cancelCalls.Add(1)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	manager := &Manager{
		tools:     tools.NewRegistry(nil),
		queryJobs: reg,
		cfgDir:    t.TempDir(),
	}

	result, err := manager.TestTool(ctx, ToolTestRequest{
		ToolName: "cancel_query",
		ThreadID: "thread-1",
		Args: json.RawMessage(fmt.Sprintf(
			`{"job_id":%q,"owner_token":"tok_owner","reason":"wrong_query","dry_run":false}`,
			job.ID,
		)),
	})
	if err != nil {
		t.Fatalf("TestTool: %v", err)
	}
	if result.Mode != tools.ToolTestModeDryRun || !result.RequiresApproval {
		t.Fatalf("test metadata=%#v", result)
	}
	if result.Args["dry_run"] != true {
		t.Fatalf("prepared dry_run=%#v want true", result.Args["dry_run"])
	}
	if !result.Output.OK {
		t.Fatalf("expected OK dry-run output, got %#v", result.Output.Result)
	}
	payload, ok := result.Output.Result.(map[string]any)
	if !ok {
		t.Fatalf("output result=%T want object", result.Output.Result)
	}
	if payload["dry_run"] != true || payload["would_cancel"] != true {
		t.Fatalf("dry-run payload=%#v", payload)
	}
	if cancelCalls.Load() != 0 {
		t.Fatalf("cancel callback called %d times during dry-run", cancelCalls.Load())
	}
	snap, ok := reg.Snapshot(job.ID, jobs.JobOwner{ThreadID: "thread-1", Handle: "tok_owner"})
	if !ok {
		t.Fatal("snapshot missing")
	}
	if snap.Status == jobs.JobStatusCancelled {
		t.Fatal("tool test dry-run cancelled the job")
	}
}

func TestManagerTestToolSeedsSyntheticQueryLifecycleJobs(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		check    func(t *testing.T, result ToolTestResult)
	}{
		{
			name:     "status",
			toolName: "check_query_status",
			check: func(t *testing.T, result ToolTestResult) {
				t.Helper()
				payload := result.Output.Result.(map[string]any)
				if strings.TrimSpace(fmt.Sprint(payload["status"])) == "" {
					t.Fatalf("status payload missing status: %#v", payload)
				}
			},
		},
		{
			name:     "results",
			toolName: "get_query_results",
			check: func(t *testing.T, result ToolTestResult) {
				t.Helper()
				payload := result.Output.Result.(map[string]any)
				rows, _ := payload["rows"].([]any)
				if len(rows) != 1 {
					t.Fatalf("rows=%#v, want one synthetic row", payload["rows"])
				}
			},
		},
		{
			name:     "cancel",
			toolName: "cancel_query",
			check: func(t *testing.T, result ToolTestResult) {
				t.Helper()
				if result.Args["dry_run"] != true {
					t.Fatalf("dry_run arg=%#v, want true", result.Args["dry_run"])
				}
				payload := result.Output.Result.(map[string]any)
				if payload["dry_run"] != true || payload["would_cancel"] != true {
					t.Fatalf("cancel dry-run payload=%#v", payload)
				}
			},
		},
		{
			name:     "validate",
			toolName: "validate_query_result",
			check: func(t *testing.T, result ToolTestResult) {
				t.Helper()
				payload := result.Output.Result.(map[string]any)
				if payload["validation_state"] != string(jobs.ValidationValidated) {
					t.Fatalf("validation payload=%#v, want validated", payload)
				}
				if result.Args["verdict"] != string(jobs.ValidationValidated) {
					t.Fatalf("validation args=%#v, want validated verdict", result.Args)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := &Manager{
				tools:  tools.NewRegistry(nil),
				cfgDir: t.TempDir(),
			}
			result, err := manager.TestTool(context.Background(), ToolTestRequest{
				ToolName: tc.toolName,
			})
			if err != nil {
				t.Fatalf("TestTool: %v", err)
			}
			if !result.Output.OK {
				t.Fatalf("expected OK output, got %#v", result.Output.Result)
			}
			if strings.TrimSpace(fmt.Sprint(result.Args["job_id"])) == "" || result.Args["owner_token"] != "tool_test_owner" {
				t.Fatalf("synthetic args missing job handle: %#v", result.Args)
			}
			tc.check(t, result)
		})
	}
}

func TestManagerTestToolSeedsSyntheticArxivAuditFixtures(t *testing.T) {
	manager := &Manager{
		tools: tools.NewRegistry(&tools.RegistryOptions{
			Arxiv: arxivapi.NewManager(arxivapi.Options{CfgDir: t.TempDir()}),
		}),
		cfgDir: t.TempDir(),
	}
	cases := []struct {
		name     string
		toolName string
		schema   string
	}{
		{
			name:     "screening report",
			toolName: tools.ArxivToolScreeningReport,
			schema:   "arxiv_research_audit.v1",
		},
		{
			name:     "recommendations",
			toolName: tools.ArxivToolRecommendations,
			schema:   "arxiv_recommendations.v1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := manager.TestTool(context.Background(), ToolTestRequest{
				ToolName: tc.toolName,
			})
			if err != nil {
				t.Fatalf("TestTool: %v", err)
			}
			if result.Mode != tools.ToolTestModeSampleCall || result.RequiresApproval {
				t.Fatalf("test metadata=%#v, want sample call without approval", result)
			}
			if !result.Output.OK {
				t.Fatalf("expected OK output, got %#v", result.Output.Result)
			}
			payload, ok := result.Output.Result.(map[string]any)
			if !ok {
				t.Fatalf("output result=%T want object", result.Output.Result)
			}
			if payload["schema"] != tc.schema {
				t.Fatalf("schema=%#v want %s payload=%#v", payload["schema"], tc.schema, payload)
			}
			if len(result.Args) == 0 {
				t.Fatalf("synthetic args missing for %s", tc.toolName)
			}
		})
	}
}

func TestManagerTestToolPreflightCoversEverySupportedTool(t *testing.T) {
	ctx := context.Background()
	h := newManagerToolTestHarness(t, ctx)

	for _, meta := range tools.AllToolMetadata() {
		meta := meta
		t.Run(meta.Name, func(t *testing.T) {
			result, err := h.manager.TestTool(ctx, ToolTestRequest{
				ToolName:  meta.Name,
				ProjectID: h.projectID,
				ThreadID:  h.threadID,
				Args:      managerToolTestArgs(meta, h),
			})
			if err != nil {
				var permissionErr *ToolTestPermissionError
				if errors.As(err, &permissionErr) {
					if permissionErr.ToolName != meta.Name {
						t.Fatalf("permission tool_name=%q, want %q", permissionErr.ToolName, meta.Name)
					}
					if !managerToolTestPermissionCategoryAllowed(meta, permissionErr.Category) {
						t.Fatalf("permission category=%q, want %q or documented source-read boundary", permissionErr.Category, meta.Category)
					}
					if strings.TrimSpace(permissionErr.Subject.Kind) == "" {
						t.Fatalf("permission subject has empty kind: %#v", permissionErr.Subject)
					}
					return
				}
				var inputErr *tools.MissingToolTestInputError
				if errors.As(err, &inputErr) {
					t.Fatalf("tool test harness missed documented input for %s: %#v", meta.Name, inputErr)
				}
				t.Fatalf("TestTool(%s) unexpected error: %T %v", meta.Name, err, err)
			}

			requireManagerToolTestResultMetadata(t, result, meta)
		})
	}
}

func TestManagerTestToolExecutionMetadataCoversEverySupportedTool(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	h := newManagerToolTestHarnessWithOptions(t, ctx, managerToolTestHarnessOptions{
		EnablePermissions: false,
	})

	for _, meta := range tools.AllToolMetadata() {
		meta := meta
		t.Run(meta.Name, func(t *testing.T) {
			result, err := h.manager.TestTool(ctx, ToolTestRequest{
				ToolName:  meta.Name,
				ProjectID: h.projectID,
				ThreadID:  h.threadID,
				Args:      managerToolTestArgs(meta, h),
			})
			if err != nil {
				var permissionErr *ToolTestPermissionError
				if errors.As(err, &permissionErr) {
					t.Fatalf("execution for %s stopped at permission preflight: %#v", meta.Name, permissionErr)
				}
				var inputErr *tools.MissingToolTestInputError
				if errors.As(err, &inputErr) {
					t.Fatalf("execution harness missed documented input for %s: %#v", meta.Name, inputErr)
				}
				t.Fatalf("TestTool(%s) unexpected error: %T %v", meta.Name, err, err)
			}
			requireManagerToolTestResultMetadata(t, result, meta)
		})
	}
}

func requireManagerToolTestResultMetadata(t *testing.T, result ToolTestResult, meta tools.ToolMetadata) {
	t.Helper()
	if result.ToolName != meta.Name ||
		result.Mode != meta.Test.Mode ||
		result.Category != meta.Category ||
		result.Origin != meta.Origin ||
		result.ToolSetID != meta.ToolSetID ||
		result.ConnectorID != meta.ConnectorID {
		t.Fatalf("result metadata=%#v, want tool metadata %#v", result, meta)
	}
	if result.Presentation.Family != meta.Presentation.Family ||
		result.Presentation.ResultKind != meta.Presentation.ResultKind ||
		result.Presentation.RendererStrategy != meta.Presentation.RendererStrategy ||
		result.Presentation.RendererKey != meta.Presentation.RendererKey ||
		result.Presentation.NativeViewer != meta.Presentation.NativeViewer {
		t.Fatalf("result presentation=%#v, want %#v", result.Presentation, meta.Presentation)
	}
	if result.Output.ResultMetadata["tool_name"] != meta.Name ||
		result.Output.ResultMetadata["tool_set_id"] != meta.ToolSetID ||
		result.Output.ResultMetadata["renderer_strategy"] != meta.Presentation.RendererStrategy ||
		result.Output.ResultMetadata["renderer_key"] != meta.Presentation.RendererKey ||
		result.Output.ResultMetadata["native_viewer"] != meta.Presentation.NativeViewer {
		t.Fatalf("result metadata=%#v, want canonical presentation for %s", result.Output.ResultMetadata, meta.Name)
	}
	if _, ok := result.Output.ResultMetadata["tool_presentation"]; !ok {
		t.Fatalf("result metadata missing tool_presentation: %#v", result.Output.ResultMetadata)
	}
}

func managerToolTestPermissionCategoryAllowed(meta tools.ToolMetadata, category string) bool {
	if category == string(meta.Category) {
		return true
	}
	if category == permissions.CategoryRead {
		switch meta.Presentation.FileCapability {
		case "remote_file_upload":
			return true
		}
		if meta.Presentation.PromotesNativeInput {
			return true
		}
	}
	return false
}

type managerToolTestHarness struct {
	manager        *Manager
	projectID      string
	threadID       string
	runID          string
	artifactID     string
	localGrantID   string
	localGrantRoot string
}

type managerToolTestHarnessOptions struct {
	EnablePermissions  bool
	DenyConnectorReads bool
}

func newManagerToolTestHarness(t *testing.T, ctx context.Context) managerToolTestHarness {
	return newManagerToolTestHarnessWithOptions(t, ctx, managerToolTestHarnessOptions{
		EnablePermissions:  true,
		DenyConnectorReads: true,
	})
}

func newManagerToolTestHarnessWithOptions(t *testing.T, ctx context.Context, opts managerToolTestHarnessOptions) managerToolTestHarness {
	t.Helper()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	settings, err := config.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	sec, err := secrets.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("secrets.NewStore: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-manager-tool-test"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile README: %v", err)
	}
	if err := ps.PutFile(projectID, "data.csv", []byte("name,value\nA,1\nB,2\n")); err != nil {
		t.Fatalf("PutFile data.csv: %v", err)
	}
	localGrantRoot := filepath.Join(cfgDir, "local-grant-fixture")
	if err := os.MkdirAll(filepath.Join(localGrantRoot, "reports"), 0o700); err != nil {
		t.Fatalf("mkdir local grant fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localGrantRoot, "reports", "summary.txt"), []byte("release notes\nQ3 forecast\n"), 0o600); err != nil {
		t.Fatalf("write local grant fixture: %v", err)
	}
	searchSvc, err := search.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	var permissionSvc *permissions.Service
	localGrantID := "perm_tool_test_local_grant"
	if opts.EnablePermissions {
		permissionSvc, err = permissions.NewService(cfgDir, st)
		if err != nil {
			t.Fatalf("permissions.NewService: %v", err)
		}
		if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
			ID:       localGrantID,
			Category: permissions.CategoryRead,
			Effect:   permissions.EffectAllow,
			Scope:    "global",
			Subject: store.PermissionSubject{
				Kind:      "local_location",
				LocalRoot: localGrantRoot,
			},
			CreatedVia: "test",
		}); err != nil {
			t.Fatalf("Create local grant rule: %v", err)
		}
		if opts.DenyConnectorReads {
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "connector"},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("Create connector deny rule: %v", err)
			}
		}
	}
	thread, err := st.CreateThread(ctx, &projectID)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "tool-test", "synthetic", nil, "Tool test matrix fixture.", 1)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	artifactID := "artifact-manager-tool-test"
	content := `[{"name":"A","value":1},{"name":"B","value":2}]`
	toolName := "display_table"
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          artifactID,
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Tool test rows",
		ToolName:    &toolName,
		Storage:     "json",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		Metadata: map[string]any{
			"content_kind":          "json_rows",
			"contains_full_content": true,
			"row_count_total":       2,
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	rlmRuntime := rlm.NewRuntime(rlm.Options{Search: searchSvc, Store: st, CfgDir: cfgDir})
	registry := tools.NewRegistry(&tools.RegistryOptions{
		Search:     searchSvc,
		Store:      st,
		CfgDir:     cfgDir,
		RLM:        rlmRuntime,
		Atlassian:  atlassian.NewManager(cfgDir, nil),
		Browser:    browsercontrol.NewManager(cfgDir, nil),
		SharePoint: sharepoint.NewManager(cfgDir, nil),
		GitHub:     &struct{}{},
		Slack:      slackapi.NewManager(nil, nil),
		Arxiv:      arxivapi.NewManager(arxivapi.Options{CfgDir: cfgDir}),
	})
	manager := &Manager{
		store:       st,
		settings:    settings,
		tools:       registry,
		projects:    ps,
		bigquery:    bigquery.NewHandler(settings, sec),
		databricks:  databricks.NewHandler(settings, sec),
		trino:       trino.NewHandler(settings, sec),
		snowflake:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		cfgDir:      cfgDir,
		queryJobs:   jobs.NewRegistry(jobs.RegistryOptions{}),
		permissions: permissionSvc,
	}
	return managerToolTestHarness{
		manager:        manager,
		projectID:      projectID,
		threadID:       thread.ID,
		runID:          run.ID,
		artifactID:     artifactID,
		localGrantID:   localGrantID,
		localGrantRoot: localGrantRoot,
	}
}

func managerToolTestArgs(meta tools.ToolMetadata, h managerToolTestHarness) json.RawMessage {
	args := map[string]any{}
	if len(permissionMetaTestArgs(meta.Name)) > 0 {
		_ = json.Unmarshal(permissionMetaTestArgs(meta.Name), &args)
	}
	for _, name := range meta.Test.RequiredArguments {
		if _, ok := args[name]; !ok {
			args[name] = managerToolTestValue(name, h)
		}
	}
	if len(meta.Test.RequiredArgumentGroups) > 0 {
		for _, name := range meta.Test.RequiredArgumentGroups[0] {
			if _, ok := args[name]; !ok {
				args[name] = managerToolTestValue(name, h)
			}
		}
	}
	switch meta.Name {
	case "local_list_grant_folder":
		args["local_root"] = h.localGrantRoot
		args["relative_path"] = "reports"
	case "local_search_in_grant":
		args["local_root"] = h.localGrantRoot
		args["relative_path"] = "reports"
	case "local_read_grant_file":
		args["local_root"] = h.localGrantRoot
	}
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return b
}

func managerToolTestValue(name string, h managerToolTestHarness) any {
	switch name {
	case "args":
		return []string{"issue", "list", "--repo", "owner/repo"}
	case "artifact_id", "doc_id":
		return h.artifactID
	case "authors", "categories", "ids", "labels", "components", "metrics", "relatedIssueKeys":
		return []string{"test-" + name}
	case "assigneeAccountId":
		return "account-123"
	case "attachmentId":
		return "attachment-123"
	case "catalog_name":
		return "hive"
	case "channel_id":
		return "C123"
	case "chart":
		return map[string]any{"kind": "bar", "x": "name", "y": "value"}
	case "clearDescription", "recursive":
		return true
	case "code":
		return "return []"
	case "column":
		return "name"
	case "comment":
		return "hello"
	case "commentId":
		return "10000"
	case "contentBase64":
		return "aGVsbG8="
	case "contentText":
		return "hello"
	case "data_json":
		return `[{"name":"A","value":1}]`
	case "database_name":
		return "analytics"
	case "dataset_id":
		return "dataset"
	case "description":
		return "Description"
	case "fields":
		return map[string]any{"summary": "Updated summary"}
	case "file_id":
		return "F123"
	case "file_ref":
		return "reports/summary.txt"
	case "fileName":
		return "note.txt"
	case "full_name":
		return "analytics.public.events"
	case "grant_id":
		return h.localGrantID
	case "html":
		return "<!doctype html><html><body>Tool test</body></html>"
	case "index", "number":
		return 1
	case "issueKey":
		return "PROJ-1"
	case "issueType":
		return "Task"
	case "item_url", "url":
		return "https://example.com/item"
	case "jql":
		return "project = PROJ"
	case "key":
		return "Enter"
	case "local_path", "path":
		return "data.csv"
	case "operator":
		return "contains"
	case "pageId":
		return "12345"
	case "paper", "arxiv_id":
		return "2301.12345"
	case "priorityName":
		return "Medium"
	case "projectKey":
		return "PROJ"
	case "projectOrBoard":
		return "PROJ"
	case "project_id":
		return "test-project"
	case "q", "query":
		return "test"
	case "repository":
		return "owner/repo"
	case "run_id":
		return h.runID
	case "schema_name":
		return "public"
	case "selector":
		return "button"
	case "server_relative_url":
		return "/sites/team/Shared Documents/file.docx"
	case "source":
		return map[string]any{"artifact_id": h.artifactID}
	case "spaceKey":
		return "SPACE"
	case "sql":
		return "select 1 as value"
	case "summary":
		return "Updated summary"
	case "table_id":
		return "events"
	case "text":
		return "hello"
	case "thread_id":
		return h.threadID
	case "thread_ts":
		return "171234.567"
	case "title":
		return "Tool test"
	case "user_id":
		return "U123"
	case "value":
		return "A"
	default:
		return "test-" + name
	}
}

func TestManagerTestToolRejectsMissingRequiredArguments(t *testing.T) {
	manager := &Manager{
		tools:  tools.NewRegistry(nil),
		cfgDir: t.TempDir(),
	}
	_, err := manager.TestTool(context.Background(), ToolTestRequest{
		ToolName: "read_file",
	})
	if err == nil {
		t.Fatal("expected missing input error")
	}
	var inputErr *tools.MissingToolTestInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error=%T %v, want MissingToolTestInputError", err, err)
	}
	if inputErr.ToolName != "read_file" || !containsStringForTest(inputErr.MissingArguments, "path") {
		t.Fatalf("missing input error=%#v", inputErr)
	}
}

func TestManagerTestToolRejectsMissingRequiredContext(t *testing.T) {
	manager := &Manager{
		tools:  tools.NewRegistry(nil),
		cfgDir: t.TempDir(),
	}
	_, err := manager.TestTool(context.Background(), ToolTestRequest{
		ToolName: "list_files",
	})
	if err == nil {
		t.Fatal("expected missing context error")
	}
	var inputErr *tools.MissingToolTestInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error=%T %v, want MissingToolTestInputError", err, err)
	}
	if inputErr.ToolName != "list_files" || !containsStringForTest(inputErr.MissingContext, "project") {
		t.Fatalf("missing context error=%#v", inputErr)
	}
	if !containsStringForTest(inputErr.RequiredContext, "project") {
		t.Fatalf("required context missing project: %#v", inputErr)
	}
}

func TestManagerTestToolRejectsMissingAutomaticConnectionContext(t *testing.T) {
	manager := &Manager{
		tools:  tools.NewRegistry(nil),
		cfgDir: t.TempDir(),
	}
	_, err := manager.TestTool(context.Background(), ToolTestRequest{
		ToolName: tools.GitHubToolCloneRepository,
		Args:     json.RawMessage(`{"repository":"owner/repo"}`),
	})
	if err == nil {
		t.Fatal("expected missing connection context error")
	}
	var inputErr *tools.MissingToolTestInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error=%T %v, want MissingToolTestInputError", err, err)
	}
	if inputErr.ToolName != tools.GitHubToolCloneRepository || !containsStringForTest(inputErr.MissingContext, "connection:github") {
		t.Fatalf("missing context error=%#v", inputErr)
	}
	if !containsStringForTest(inputErr.RequiredContext, "connection:github") {
		t.Fatalf("required context missing connection: %#v", inputErr)
	}
}

func TestManagerTestToolRejectsMissingAutomaticBrowserContext(t *testing.T) {
	manager := &Manager{
		tools:  tools.NewRegistry(nil),
		cfgDir: t.TempDir(),
	}
	_, err := manager.TestTool(context.Background(), ToolTestRequest{
		ToolName: tools.BrowserControlToolNavigate,
		Args:     json.RawMessage(`{"url":"https://example.com"}`),
	})
	if err == nil {
		t.Fatal("expected missing browser context error")
	}
	var inputErr *tools.MissingToolTestInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error=%T %v, want MissingToolTestInputError", err, err)
	}
	if inputErr.ToolName != tools.BrowserControlToolNavigate ||
		!containsStringForTest(inputErr.MissingContext, "connection:browser") ||
		!containsStringForTest(inputErr.MissingContext, "browser") {
		t.Fatalf("missing context error=%#v", inputErr)
	}
}

func TestManagerTestToolRejectsMissingDatasourceConnectionContext(t *testing.T) {
	manager := &Manager{
		tools:  tools.NewRegistry(nil),
		cfgDir: t.TempDir(),
	}
	_, err := manager.TestTool(context.Background(), ToolTestRequest{
		ToolName: "run_bigquery_query",
		Args:     json.RawMessage(`{"sql":"select 1"}`),
	})
	if err == nil {
		t.Fatal("expected missing datasource connection context error")
	}
	var inputErr *tools.MissingToolTestInputError
	if !errors.As(err, &inputErr) {
		t.Fatalf("error=%T %v, want MissingToolTestInputError", err, err)
	}
	if inputErr.ToolName != "run_bigquery_query" || !containsStringForTest(inputErr.MissingContext, "connection:bigquery") {
		t.Fatalf("missing context error=%#v", inputErr)
	}
}

func TestManagerTestToolRequiresPermissionForLocalReadWhenPermissionsEnabled(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	projectID := "project-local-test-permission"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	projectRoot, err := ps.FilesDir(projectID)
	if err != nil {
		t.Fatalf("FilesDir: %v", err)
	}
	manager := &Manager{
		tools:       tools.NewRegistry(nil),
		projects:    ps,
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}

	_, err = manager.TestTool(ctx, ToolTestRequest{
		ToolName:  "read_file",
		ProjectID: projectID,
		Args:      json.RawMessage(`{"path":"README.md"}`),
	})
	if err == nil {
		t.Fatal("expected permission error")
	}
	var permissionErr *ToolTestPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error=%T %v, want ToolTestPermissionError", err, err)
	}
	if permissionErr.Decision != permissions.DecisionAsk || permissionErr.Subject.Kind != "local_location" {
		t.Fatalf("permission error=%#v, want local ask", permissionErr)
	}

	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectAllow,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: projectRoot},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	result, err := manager.TestTool(ctx, ToolTestRequest{
		ToolName:  "read_file",
		ProjectID: projectID,
		Args:      json.RawMessage(`{"path":"README.md"}`),
	})
	if err != nil {
		t.Fatalf("TestTool after allow rule: %v", err)
	}
	if !result.Output.OK {
		t.Fatalf("expected allowed tool test to run, got %#v", result.Output.Result)
	}
}

func TestManagerTestToolBroadLocalAllowDoesNotCoverSensitiveChild(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	projectID := "project-local-sensitive-permission"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile README: %v", err)
	}
	if err := ps.PutFile(projectID, ".env", []byte("SECRET_TOKEN=value")); err != nil {
		t.Fatalf("PutFile .env: %v", err)
	}
	projectRoot, err := ps.FilesDir(projectID)
	if err != nil {
		t.Fatalf("FilesDir: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "local_location",
			LocalRoot: projectRoot,
			Excludes:  sensitiveLocalPermissionExcludes(),
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{
		tools:       tools.NewRegistry(nil),
		projects:    ps,
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}

	result, err := manager.TestTool(ctx, ToolTestRequest{
		ToolName:  "read_file",
		ProjectID: projectID,
		Args:      json.RawMessage(`{"path":"README.md"}`),
	})
	if err != nil {
		t.Fatalf("TestTool README: %v", err)
	}
	if !result.Output.OK {
		t.Fatalf("expected README read to run under broad local allow, got %#v", result.Output.Result)
	}

	_, err = manager.TestTool(ctx, ToolTestRequest{
		ToolName:  "read_file",
		ProjectID: projectID,
		Args:      json.RawMessage(`{"path":".env"}`),
	})
	if err == nil {
		t.Fatal("expected sensitive child to require its own approval")
	}
	var permissionErr *ToolTestPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error=%T %v, want ToolTestPermissionError", err, err)
	}
	if permissionErr.Decision != permissions.DecisionAsk || permissionErr.Subject.Kind != "local_location" {
		t.Fatalf("permission error=%#v, want local ask", permissionErr)
	}
	if !strings.HasSuffix(filepath.ToSlash(permissionErr.Subject.LocalRoot), "/.env") {
		t.Fatalf("permission subject local root=%q, want .env target", permissionErr.Subject.LocalRoot)
	}
}

func TestManagerTestToolBlocksDeniedExecutePermission(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryExecute,
		Effect:   permissions.EffectDeny,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "tool",
			Tool:      "cancel_query",
			Operation: "cancel_query",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	manager := &Manager{
		tools:       tools.NewRegistry(nil),
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}

	_, err = manager.TestTool(ctx, ToolTestRequest{
		ToolName: "cancel_query",
		Args:     json.RawMessage(`{"job_id":"job_test","owner_token":"tok_test"}`),
	})
	if err == nil {
		t.Fatal("expected denied permission error")
	}
	var permissionErr *ToolTestPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error=%T %v, want ToolTestPermissionError", err, err)
	}
	if permissionErr.Decision != permissions.DecisionDeny || permissionErr.Category != permissions.CategoryExecute {
		t.Fatalf("permission error=%#v, want execute deny", permissionErr)
	}
}

func TestManagerTestToolBlocksDeniedQueryLifecycleConnectorPermissions(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	for _, category := range []string{permissions.CategoryRead, permissions.CategoryExecute} {
		if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
			Category:   category,
			Effect:     permissions.EffectDeny,
			Scope:      "global",
			Subject:    store.PermissionSubject{Kind: "connector", Connector: "bigquery"},
			CreatedVia: "test",
		}); err != nil {
			t.Fatalf("CreateRule(%s): %v", category, err)
		}
	}
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	manager := &Manager{
		tools:       tools.NewRegistry(nil),
		queryJobs:   reg,
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}
	cases := []struct {
		name     string
		toolName string
		category string
	}{
		{name: "status", toolName: "check_query_status", category: permissions.CategoryRead},
		{name: "results", toolName: "get_query_results", category: permissions.CategoryRead},
		{name: "cancel", toolName: "cancel_query", category: permissions.CategoryExecute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			owner := jobs.JobOwner{ThreadID: "thread-query-perms", RunID: "run-1", Handle: "tok_" + tc.name}
			job, err := reg.Register(owner, "bigquery", "select 1", "select 1", nil, nil, nil)
			if err != nil {
				t.Fatalf("Register: %v", err)
			}
			_, err = manager.TestTool(ctx, ToolTestRequest{
				ToolName: tc.toolName,
				ThreadID: owner.ThreadID,
				Args: json.RawMessage(fmt.Sprintf(
					`{"job_id":%q,"owner_token":%q}`,
					job.ID,
					owner.Handle,
				)),
			})
			if err == nil {
				t.Fatal("expected denied permission error")
			}
			var permissionErr *ToolTestPermissionError
			if !errors.As(err, &permissionErr) {
				t.Fatalf("error=%T %v, want ToolTestPermissionError", err, err)
			}
			if permissionErr.Decision != permissions.DecisionDeny || permissionErr.Category != tc.category {
				t.Fatalf("permission error=%#v, want %s deny", permissionErr, tc.category)
			}
			if permissionErr.Subject.Kind != "connector" || permissionErr.Subject.Connector != "bigquery" {
				t.Fatalf("permission subject=%#v, want bigquery connector", permissionErr.Subject)
			}
		})
	}
}

func TestQueryLifecyclePermissionMetaFallsBackWhenJobSnapshotMissing(t *testing.T) {
	manager := &Manager{}
	got, ok := manager.queryLifecyclePermissionMetaForManagerTool(
		"thread-query-missing",
		"check_query_status",
		json.RawMessage(`{"job_id":"job_missing","owner_token":"tok_missing","provider":"trino"}`),
	)
	if !ok {
		t.Fatal("expected query lifecycle fallback permission metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "tool" ||
		got.Connector != "trino" ||
		got.Operation != "check_query_status" ||
		got.RemoteKind != "query_job" ||
		got.RemoteID != "job_missing" {
		t.Fatalf("metadata=%#v, want trino read lifecycle metadata", got)
	}
	if !strings.Contains(got.Summary, "Trino query job_missing") {
		t.Fatalf("summary=%q, want provider and job id", got.Summary)
	}

	metas := manager.permissionMetasForTool(
		context.Background(),
		"thread-query-missing",
		"cancel_query",
		json.RawMessage(`{"job_id":"job_missing","owner_token":"tok_missing"}`),
		"",
	)
	if len(metas) != 1 {
		t.Fatalf("metas=%#v, want one fallback meta", metas)
	}
	if metas[0].Category != permissions.CategoryExecute ||
		metas[0].Operation != "cancel_query" ||
		metas[0].ToolSetID != "query-lifecycle" {
		t.Fatalf("fallback meta=%#v, want execute query-lifecycle metadata", metas[0])
	}
	if strings.TrimSpace(metas[0].Connector) != "" {
		t.Fatalf("fallback connector=%q, want empty when provider is unknown", metas[0].Connector)
	}
}

func TestSourceBackedArtifactToolsScopeQueryJobPermissions(t *testing.T) {
	ctx := context.Background()
	reg := jobs.NewRegistry(jobs.RegistryOptions{})
	owner := jobs.JobOwner{ThreadID: "thread-query-source", RunID: "run-1", Handle: "tok_source"}
	job, err := reg.Register(owner, "bigquery", "select 1", "select 1", nil, nil, nil)
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	manager := &Manager{queryJobs: reg}
	cases := []struct {
		toolName string
		args     json.RawMessage
	}{
		{
			toolName: "display_table",
			args: mustJSONForPermissionTest(map[string]any{
				"source": map[string]any{
					"job_id":      job.ID,
					"owner_token": owner.Handle,
				},
			}),
		},
		{
			toolName: "build_chart_artifact",
			args: mustJSONForPermissionTest(map[string]any{
				"source": map[string]any{
					"job_id":      job.ID,
					"owner_token": owner.Handle,
				},
				"chart": map[string]any{"kind": "bar", "x": "region", "y": "revenue"},
			}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			metas := manager.permissionMetasForTool(ctx, owner.ThreadID, tc.toolName, tc.args, "")
			if len(metas) < 1 {
				t.Fatalf("permission metas empty, want query job source metadata")
			}
			meta := metas[0]
			if meta.Category != permissions.CategoryRead ||
				meta.SubjectKind != "tool" ||
				meta.Connector != "bigquery" ||
				meta.Operation != tc.toolName+"_source_query_job_read" ||
				meta.RemoteKind != "query_job" ||
				meta.RemoteID != job.ID {
				t.Fatalf("metadata=%#v, want scoped bigquery query_job source read", meta)
			}
			if !strings.Contains(meta.Summary, "BigQuery query "+job.ID) {
				t.Fatalf("summary=%q, want provider and job id", meta.Summary)
			}
		})
	}
}

func TestConnectedWritePermissionMetaCarriesRemoteResourceTargets(t *testing.T) {
	cases := []struct {
		name       string
		toolName   string
		args       json.RawMessage
		remoteKind string
		remoteID   string
	}{
		{
			name:       "slack_channel_message",
			toolName:   tools.SlackToolPostMessage,
			args:       json.RawMessage(`{"channel_id":"C123","text":"hello"}`),
			remoteKind: "slack_channel",
			remoteID:   "C123",
		},
		{
			name:       "slack_thread_message",
			toolName:   tools.SlackToolPostMessage,
			args:       json.RawMessage(`{"channel_id":"C123","thread_ts":"171234.567","text":"hello"}`),
			remoteKind: "slack_thread",
			remoteID:   "C123/171234.567",
		},
		{
			name:       "slack_upload",
			toolName:   tools.SlackToolUploadFile,
			args:       json.RawMessage(`{"channel_id":"C123","fileName":"note.txt","contentText":"hello"}`),
			remoteKind: "slack_channel",
			remoteID:   "C123",
		},
		{
			name:       "jira_comment",
			toolName:   "jira_add_comment",
			args:       json.RawMessage(`{"issueKey":"proj-1","comment":"hello"}`),
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
		},
		{
			name:       "jira_reply",
			toolName:   "jira_reply_to_comment",
			args:       json.RawMessage(`{"issueKey":"PROJ-1","commentId":"10000","comment":"hello"}`),
			remoteKind: "jira_comment",
			remoteID:   "PROJ-1/10000",
		},
		{
			name:       "jira_related_tickets",
			toolName:   "jira_add_related_tickets",
			args:       json.RawMessage(`{"issueKey":"proj-1","relatedIssueKeys":["PROJ-2"]}`),
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
		},
		{
			name:       "jira_upload_attachment",
			toolName:   "jira_upload_attachment",
			args:       json.RawMessage(`{"issueKey":"proj-1","fileName":"note.txt","contentText":"hello"}`),
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
		},
		{
			name:       "jira_update_issue",
			toolName:   "jira_update_issue",
			args:       json.RawMessage(`{"issueKey":"proj-1","summary":"Updated summary"}`),
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
		},
		{
			name:       "jira_create",
			toolName:   "jira_create_issue",
			args:       json.RawMessage(`{"projectKey":"proj","summary":"New issue"}`),
			remoteKind: "jira_project",
			remoteID:   "PROJ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := permissionMetaForManagerTool(tc.toolName, tc.args, "")
			if !ok {
				t.Fatalf("permissionMetaForManagerTool(%s) returned ok=false", tc.toolName)
			}
			if got.RemoteKind != tc.remoteKind || got.RemoteID != tc.remoteID {
				t.Fatalf("remote target=%s/%s, want %s/%s; metadata=%#v", got.RemoteKind, got.RemoteID, tc.remoteKind, tc.remoteID, got)
			}
			subject := managerPermissionSubject(got, tc.toolName)
			if subject.RemoteKind != tc.remoteKind || subject.RemoteID != tc.remoteID {
				t.Fatalf("subject remote target=%s/%s, want %s/%s; subject=%#v", subject.RemoteKind, subject.RemoteID, tc.remoteKind, tc.remoteID, subject)
			}
		})
	}
}

func TestConnectedReadPermissionMetaCarriesRemoteResourceTargets(t *testing.T) {
	cases := []struct {
		name       string
		toolName   string
		args       json.RawMessage
		connector  string
		toolSetID  string
		remoteKind string
		remoteID   string
	}{
		{
			name:       "slack_messages",
			toolName:   tools.SlackToolGetMessages,
			args:       json.RawMessage(`{"channel_id":"C123"}`),
			connector:  "slack",
			toolSetID:  "slack",
			remoteKind: "slack_channel",
			remoteID:   "C123",
		},
		{
			name:       "slack_thread",
			toolName:   tools.SlackToolGetThreadReplies,
			args:       json.RawMessage(`{"channel_id":"C123","thread_ts":"171234.567"}`),
			connector:  "slack",
			toolSetID:  "slack",
			remoteKind: "slack_thread",
			remoteID:   "C123/171234.567",
		},
		{
			name:       "slack_user",
			toolName:   tools.SlackToolGetUser,
			args:       json.RawMessage(`{"user_id":"U123"}`),
			connector:  "slack",
			toolSetID:  "slack",
			remoteKind: "slack_user",
			remoteID:   "U123",
		},
		{
			name:       "slack_file",
			toolName:   tools.SlackToolDownloadFile,
			args:       json.RawMessage(`{"file_id":"FABC","channel_id":"C123"}`),
			connector:  "slack",
			toolSetID:  "slack",
			remoteKind: "slack_file",
			remoteID:   "FABC",
		},
		{
			name:       "github_pull_requests",
			toolName:   tools.GitHubToolListPullRequests,
			args:       json.RawMessage(`{"repository":"Owner/Repo"}`),
			connector:  "github",
			toolSetID:  "github",
			remoteKind: "github_repository",
			remoteID:   "owner/repo",
		},
		{
			name:       "github_pull_request",
			toolName:   tools.GitHubToolGetPullRequest,
			args:       json.RawMessage(`{"repository":"Owner/Repo","number":12}`),
			connector:  "github",
			toolSetID:  "github",
			remoteKind: "github_pull_request",
			remoteID:   "owner/repo#12",
		},
		{
			name:       "github_issue",
			toolName:   tools.GitHubToolGetIssue,
			args:       json.RawMessage(`{"repository":"Owner/Repo","number":34}`),
			connector:  "github",
			toolSetID:  "github",
			remoteKind: "github_issue",
			remoteID:   "owner/repo#34",
		},
		{
			name:       "github_workflow_run",
			toolName:   tools.GitHubToolGetWorkflowRunJobs,
			args:       json.RawMessage(`{"repository":"Owner/Repo","run_id":"98765"}`),
			connector:  "github",
			toolSetID:  "github",
			remoteKind: "github_workflow_run",
			remoteID:   "owner/repo/98765",
		},
		{
			name:       "bigquery_project",
			toolName:   "list_bq_datasets",
			args:       json.RawMessage(`{"project_id":"analytics-prod"}`),
			connector:  "bigquery",
			toolSetID:  "bigquery",
			remoteKind: "bigquery_project",
			remoteID:   "analytics-prod",
		},
		{
			name:       "bigquery_run_project",
			toolName:   "run_bigquery_query",
			args:       json.RawMessage(`{"project_id":"analytics-prod","sql":"select 1"}`),
			connector:  "bigquery",
			toolSetID:  "bigquery",
			remoteKind: "bigquery_project",
			remoteID:   "analytics-prod",
		},
		{
			name:       "bigquery_dataset",
			toolName:   "list_bq_tables",
			args:       json.RawMessage(`{"project_id":"analytics-prod","dataset_id":"finance"}`),
			connector:  "bigquery",
			toolSetID:  "bigquery",
			remoteKind: "bigquery_dataset",
			remoteID:   "analytics-prod.finance",
		},
		{
			name:       "bigquery_table",
			toolName:   "get_bq_table_schema",
			args:       json.RawMessage(`{"project_id":"analytics-prod","dataset_id":"finance","table_id":"invoices"}`),
			connector:  "bigquery",
			toolSetID:  "bigquery",
			remoteKind: "bigquery_table",
			remoteID:   "analytics-prod.finance.invoices",
		},
		{
			name:       "databricks_catalog",
			toolName:   "list_databricks_schemas",
			args:       json.RawMessage(`{"catalog_name":"hive_metastore"}`),
			connector:  "databricks",
			toolSetID:  "databricks",
			remoteKind: "databricks_catalog",
			remoteID:   "hive_metastore",
		},
		{
			name:       "databricks_run_catalog",
			toolName:   "run_databricks_query",
			args:       json.RawMessage(`{"catalog":"hive_metastore","sql":"select 1"}`),
			connector:  "databricks",
			toolSetID:  "databricks",
			remoteKind: "databricks_catalog",
			remoteID:   "hive_metastore",
		},
		{
			name:       "databricks_run_schema",
			toolName:   "run_databricks_query",
			args:       json.RawMessage(`{"catalog":"hive_metastore","schema":"sales","sql":"select 1"}`),
			connector:  "databricks",
			toolSetID:  "databricks",
			remoteKind: "databricks_schema",
			remoteID:   "hive_metastore.sales",
		},
		{
			name:       "databricks_schema",
			toolName:   "list_databricks_tables",
			args:       json.RawMessage(`{"catalog_name":"hive_metastore","schema_name":"sales"}`),
			connector:  "databricks",
			toolSetID:  "databricks",
			remoteKind: "databricks_schema",
			remoteID:   "hive_metastore.sales",
		},
		{
			name:       "databricks_table",
			toolName:   "describe_databricks_table",
			args:       json.RawMessage(`{"full_name":"hive_metastore.sales.orders"}`),
			connector:  "databricks",
			toolSetID:  "databricks",
			remoteKind: "databricks_table",
			remoteID:   "hive_metastore.sales.orders",
		},
		{
			name:       "trino_catalog",
			toolName:   "list_trino_schemas",
			args:       json.RawMessage(`{"catalog_name":"hive"}`),
			connector:  "trino",
			toolSetID:  "trino",
			remoteKind: "trino_catalog",
			remoteID:   "hive",
		},
		{
			name:       "trino_run_catalog",
			toolName:   "run_trino_query",
			args:       json.RawMessage(`{"catalog":"hive","sql":"select 1"}`),
			connector:  "trino",
			toolSetID:  "trino",
			remoteKind: "trino_catalog",
			remoteID:   "hive",
		},
		{
			name:       "trino_run_schema",
			toolName:   "run_trino_query",
			args:       json.RawMessage(`{"catalog":"hive","schema":"sales","sql":"select 1"}`),
			connector:  "trino",
			toolSetID:  "trino",
			remoteKind: "trino_schema",
			remoteID:   "hive.sales",
		},
		{
			name:       "trino_schema",
			toolName:   "list_trino_tables",
			args:       json.RawMessage(`{"catalog_name":"hive","schema_name":"sales"}`),
			connector:  "trino",
			toolSetID:  "trino",
			remoteKind: "trino_schema",
			remoteID:   "hive.sales",
		},
		{
			name:       "trino_table",
			toolName:   "describe_trino_table",
			args:       json.RawMessage(`{"full_name":"hive.sales.orders"}`),
			connector:  "trino",
			toolSetID:  "trino",
			remoteKind: "trino_table",
			remoteID:   "hive.sales.orders",
		},
		{
			name:       "snowflake_database",
			toolName:   "list_snowflake_schemas",
			args:       json.RawMessage(`{"database_name":"ANALYTICS"}`),
			connector:  "snowflake",
			toolSetID:  "snowflake",
			remoteKind: "snowflake_database",
			remoteID:   "ANALYTICS",
		},
		{
			name:       "snowflake_run_database",
			toolName:   "run_snowflake_query",
			args:       json.RawMessage(`{"database":"ANALYTICS","sql":"select 1"}`),
			connector:  "snowflake",
			toolSetID:  "snowflake",
			remoteKind: "snowflake_database",
			remoteID:   "ANALYTICS",
		},
		{
			name:       "snowflake_run_schema",
			toolName:   "run_snowflake_query",
			args:       json.RawMessage(`{"database":"ANALYTICS","schema":"SALES","sql":"select 1"}`),
			connector:  "snowflake",
			toolSetID:  "snowflake",
			remoteKind: "snowflake_schema",
			remoteID:   "ANALYTICS.SALES",
		},
		{
			name:       "snowflake_schema",
			toolName:   "list_snowflake_tables",
			args:       json.RawMessage(`{"database_name":"ANALYTICS","schema_name":"SALES"}`),
			connector:  "snowflake",
			toolSetID:  "snowflake",
			remoteKind: "snowflake_schema",
			remoteID:   "ANALYTICS.SALES",
		},
		{
			name:       "snowflake_table",
			toolName:   "describe_snowflake_table",
			args:       json.RawMessage(`{"full_name":"ANALYTICS.SALES.ORDERS"}`),
			connector:  "snowflake",
			toolSetID:  "snowflake",
			remoteKind: "snowflake_table",
			remoteID:   "ANALYTICS.SALES.ORDERS",
		},
		{
			name:       "jira_issue",
			toolName:   "jira_get_issue",
			args:       json.RawMessage(`{"issueKey":"proj-1"}`),
			connector:  "atlassian",
			toolSetID:  "atlassian",
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
		},
		{
			name:       "jira_attachment",
			toolName:   "jira_read_attachment",
			args:       json.RawMessage(`{"issueKey":"PROJ-1","attachmentId":"10001"}`),
			connector:  "atlassian",
			toolSetID:  "atlassian",
			remoteKind: "jira_attachment",
			remoteID:   "10001",
		},
		{
			name:       "confluence_page",
			toolName:   "confluence_open_page",
			args:       json.RawMessage(`{"pageId":"98765"}`),
			connector:  "atlassian",
			toolSetID:  "atlassian",
			remoteKind: "confluence_page",
			remoteID:   "98765",
		},
		{
			name:       "confluence_search_space",
			toolName:   "confluence_search",
			args:       json.RawMessage(`{"query":"roadmap","spaceKey":"ENG"}`),
			connector:  "atlassian",
			toolSetID:  "atlassian",
			remoteKind: "confluence_space",
			remoteID:   "ENG",
		},
		{
			name:       "confluence_rest",
			toolName:   "confluence_rest_get",
			args:       json.RawMessage(`{"path":"/wiki/api/v2/pages/1"}`),
			connector:  "atlassian",
			toolSetID:  "atlassian",
			remoteKind: "confluence_rest_path",
			remoteID:   "/wiki/api/v2/pages/1",
		},
		{
			name:       "sharepoint_file",
			toolName:   tools.SharePointToolRead,
			args:       json.RawMessage(`{"server_relative_url":"/sites/team/Shared Documents/File.docx"}`),
			connector:  "sharepoint",
			toolSetID:  "sharepoint",
			remoteKind: "sharepoint_file",
			remoteID:   "/sites/team/Shared Documents/File.docx",
		},
		{
			name:       "sharepoint_download",
			toolName:   tools.SharePointToolDownload,
			args:       json.RawMessage(`{"item_url":"https://contoso.sharepoint.com/sites/team/Shared%20Documents/File.docx"}`),
			connector:  "sharepoint",
			toolSetID:  "sharepoint",
			remoteKind: "sharepoint_file",
			remoteID:   "https://contoso.sharepoint.com/sites/team/Shared%20Documents/File.docx",
		},
		{
			name:       "sharepoint_summarize",
			toolName:   tools.SharePointToolSummarize,
			args:       json.RawMessage(`{"server_relative_url":"/sites/team/Shared Documents/Summary.docx"}`),
			connector:  "sharepoint",
			toolSetID:  "sharepoint",
			remoteKind: "sharepoint_file",
			remoteID:   "/sites/team/Shared Documents/Summary.docx",
		},
		{
			name:       "arxiv_paper",
			toolName:   tools.ArxivToolDownloadPaper,
			args:       json.RawMessage(`{"arxiv_id":"2301.12345"}`),
			connector:  "arxiv",
			toolSetID:  "arxiv",
			remoteKind: "arxiv_paper",
			remoteID:   "2301.12345",
		},
		{
			name:       "arxiv_search_exact_id",
			toolName:   tools.ArxivToolSearch,
			args:       json.RawMessage(`{"ids":["2301.12345"]}`),
			connector:  "arxiv",
			toolSetID:  "arxiv",
			remoteKind: "arxiv_paper",
			remoteID:   "2301.12345",
		},
		{
			name:       "arxiv_search_exact_id_set",
			toolName:   tools.ArxivToolSearch,
			args:       json.RawMessage(`{"ids":["2402.22222","2301.11111"]}`),
			connector:  "arxiv",
			toolSetID:  "arxiv",
			remoteKind: "arxiv_paper_set",
			remoteID:   "2301.11111,2402.22222",
		},
		{
			name:       "arxiv_paper_text",
			toolName:   tools.ArxivToolGetPaperText,
			args:       json.RawMessage(`{"paper":"2301.12345"}`),
			connector:  "arxiv",
			toolSetID:  "arxiv",
			remoteKind: "arxiv_paper",
			remoteID:   "2301.12345",
		},
		{
			name:       "arxiv_paper_pages",
			toolName:   tools.ArxivToolReadPaper,
			args:       json.RawMessage(`{"paper":"2301.12345"}`),
			connector:  "arxiv",
			toolSetID:  "arxiv",
			remoteKind: "arxiv_paper",
			remoteID:   "2301.12345",
		},
		{
			name:       "thread_search_doc",
			toolName:   "get_doc",
			args:       json.RawMessage(`{"doc_id":"doc-123"}`),
			connector:  "thread-search",
			toolSetID:  "thread-search",
			remoteKind: "search_doc",
			remoteID:   "doc-123",
		},
		{
			name:       "thread_search_messages",
			toolName:   "list_messages",
			args:       json.RawMessage(`{"thread_id":"thread-123"}`),
			connector:  "thread-search",
			toolSetID:  "thread-search",
			remoteKind: "thread",
			remoteID:   "thread-123",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := permissionMetaForManagerTool(tc.toolName, tc.args, "")
			if !ok {
				t.Fatalf("permissionMetaForManagerTool(%s) returned ok=false", tc.toolName)
			}
			if got.Category != permissions.CategoryRead ||
				got.SubjectKind != "tool" ||
				got.Connector != tc.connector ||
				got.ToolSetID != tc.toolSetID ||
				got.Operation != tc.toolName ||
				got.RemoteKind != tc.remoteKind ||
				got.RemoteID != tc.remoteID {
				t.Fatalf("metadata=%#v, want scoped connected read target", got)
			}
			subject := managerPermissionSubject(got, tc.toolName)
			if subject.RemoteKind != tc.remoteKind || subject.RemoteID != tc.remoteID {
				t.Fatalf("subject remote target=%s/%s, want %s/%s; subject=%#v", subject.RemoteKind, subject.RemoteID, tc.remoteKind, tc.remoteID, subject)
			}
		})
	}
}

func TestUploadArtifactSourcePermissionMetaScopesArtifactSource(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		args      json.RawMessage
		connector string
	}{
		{
			name:      "slack_upload",
			toolName:  tools.SlackToolUploadFile,
			args:      json.RawMessage(`{"channel_id":"C123","artifact_id":"artifact-source-1"}`),
			connector: "slack",
		},
		{
			name:      "jira_upload",
			toolName:  "jira_upload_attachment",
			args:      json.RawMessage(`{"issueKey":"PROJ-1","artifact_id":"artifact-source-1"}`),
			connector: "atlassian",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metas := permissionMetasForManagerTool(tc.toolName, tc.args, "")
			if len(metas) != 2 {
				t.Fatalf("metas=%#v, want artifact-source read meta plus write meta", metas)
			}
			readMeta := metas[0]
			if readMeta.Category != permissions.CategoryRead ||
				readMeta.SubjectKind != "tool" ||
				readMeta.Connector != tc.connector ||
				readMeta.Operation != tc.toolName+"_artifact_source_read" ||
				readMeta.RemoteKind != "artifact" ||
				readMeta.RemoteID != "artifact-source-1" {
				t.Fatalf("read meta=%#v, want scoped artifact source read", readMeta)
			}
			subject := managerPermissionSubject(readMeta, tc.toolName)
			if subject.Tool != tc.toolName ||
				subject.Connector != tc.connector ||
				subject.RemoteKind != "artifact" ||
				subject.RemoteID != "artifact-source-1" {
				t.Fatalf("subject=%#v, want upload tool artifact source subject", subject)
			}
			if metas[1].Category != permissions.CategoryWrite {
				t.Fatalf("write meta=%#v, want write target after source read", metas[1])
			}
		})
	}
}

func TestManagerJiraRemoteResourceRuleCanOverrideConnectorRule(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name       string
		toolName   string
		operation  string
		remoteKind string
		remoteID   string
		allowArgs  json.RawMessage
		denyArgs   json.RawMessage
	}{
		{
			name:       "comment_issue",
			toolName:   "jira_add_comment",
			operation:  "jira_add_comment_execute",
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
			allowArgs:  json.RawMessage(`{"issueKey":"PROJ-1","comment":"hello"}`),
			denyArgs:   json.RawMessage(`{"issueKey":"PROJ-2","comment":"hello"}`),
		},
		{
			name:       "comment_reply",
			toolName:   "jira_reply_to_comment",
			operation:  "jira_reply_to_comment_execute",
			remoteKind: "jira_comment",
			remoteID:   "PROJ-1/10000",
			allowArgs:  json.RawMessage(`{"issueKey":"PROJ-1","commentId":"10000","comment":"hello"}`),
			denyArgs:   json.RawMessage(`{"issueKey":"PROJ-1","commentId":"20000","comment":"hello"}`),
		},
		{
			name:       "update_issue",
			toolName:   "jira_update_issue",
			operation:  "jira_update_issue_execute",
			remoteKind: "jira_issue",
			remoteID:   "PROJ-1",
			allowArgs:  json.RawMessage(`{"issueKey":"PROJ-1","summary":"Updated summary"}`),
			denyArgs:   json.RawMessage(`{"issueKey":"PROJ-2","summary":"Updated summary"}`),
		},
		{
			name:       "create_project_issue",
			toolName:   "jira_create_issue",
			operation:  "jira_create_issue_execute",
			remoteKind: "jira_project",
			remoteID:   "PROJ",
			allowArgs:  json.RawMessage(`{"projectKey":"PROJ","summary":"New issue"}`),
			denyArgs:   json.RawMessage(`{"projectKey":"OTHER","summary":"New issue"}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category: permissions.CategoryWrite,
				Effect:   permissions.EffectAllow,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:       "tool",
					Tool:       tc.toolName,
					Connector:  "atlassian",
					Operation:  tc.operation,
					RemoteKind: tc.remoteKind,
					RemoteID:   tc.remoteID,
				},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("Create remote allow rule: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryWrite,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "connector", Connector: "atlassian"},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("Create connector deny rule: %v", err)
			}
			manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}

			_, subject, evaluation, ok, err := manager.evaluateManagerToolPermission(
				ctx,
				"thread-jira-remote-resource-rule",
				tc.toolName,
				tc.allowArgs,
				"",
			)
			if err != nil {
				t.Fatalf("evaluateManagerToolPermission allow: %v", err)
			}
			if !ok {
				t.Fatal("evaluateManagerToolPermission returned ok=false, want remote-resource evaluation")
			}
			if evaluation.Decision != permissions.DecisionAllow {
				t.Fatalf("decision=%q, want allow from remote-resource rule", evaluation.Decision)
			}
			if subject.RemoteKind != tc.remoteKind || subject.RemoteID != tc.remoteID {
				t.Fatalf("subject=%#v, want %s/%s remote subject", subject, tc.remoteKind, tc.remoteID)
			}

			_, subject, evaluation, ok, err = manager.evaluateManagerToolPermission(
				ctx,
				"thread-jira-remote-resource-rule",
				tc.toolName,
				tc.denyArgs,
				"",
			)
			if err != nil {
				t.Fatalf("evaluateManagerToolPermission deny: %v", err)
			}
			if !ok {
				t.Fatal("evaluateManagerToolPermission returned ok=false for other remote target, want connector evaluation")
			}
			if evaluation.Decision != permissions.DecisionDeny {
				t.Fatalf("other target decision=%q, want deny from connector rule", evaluation.Decision)
			}
			if subject.Kind != "connector" || subject.Connector != "atlassian" {
				t.Fatalf("other target subject=%#v, want atlassian connector subject", subject)
			}
		})
	}
}

func TestManagerConnectedReadRemoteResourceRuleCanOverrideConnectorRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       tools.SlackToolDownloadFile,
			Connector:  "slack",
			Operation:  tools.SlackToolDownloadFile,
			RemoteKind: "slack_file",
			RemoteID:   "FABC",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create remote allow rule: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "slack"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create connector deny rule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}

	_, subject, evaluation, ok, err := manager.evaluateManagerToolPermission(
		ctx,
		"thread-connected-read-remote-resource-rule",
		tools.SlackToolDownloadFile,
		json.RawMessage(`{"file_id":"FABC","channel_id":"C123"}`),
		"",
	)
	if err != nil {
		t.Fatalf("evaluateManagerToolPermission allow: %v", err)
	}
	if !ok {
		t.Fatal("evaluateManagerToolPermission returned ok=false, want remote-resource evaluation")
	}
	if evaluation.Decision != permissions.DecisionAllow {
		t.Fatalf("decision=%q, want allow from remote-resource rule", evaluation.Decision)
	}
	if subject.RemoteKind != "slack_file" || subject.RemoteID != "FABC" {
		t.Fatalf("subject=%#v, want slack_file/FABC remote subject", subject)
	}

	_, subject, evaluation, ok, err = manager.evaluateManagerToolPermission(
		ctx,
		"thread-connected-read-remote-resource-rule",
		tools.SlackToolDownloadFile,
		json.RawMessage(`{"file_id":"FXYZ","channel_id":"C123"}`),
		"",
	)
	if err != nil {
		t.Fatalf("evaluateManagerToolPermission deny: %v", err)
	}
	if !ok {
		t.Fatal("evaluateManagerToolPermission returned ok=false for other remote target, want connector evaluation")
	}
	if evaluation.Decision != permissions.DecisionDeny {
		t.Fatalf("other target decision=%q, want deny from connector rule", evaluation.Decision)
	}
	if subject.Kind != "connector" || subject.Connector != "slack" {
		t.Fatalf("other target subject=%#v, want slack connector subject", subject)
	}
}

func TestManagerPermissionUsesToolSetRuleBeforeConnectorRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryExecute,
		Effect:     permissions.EffectAllow,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "github"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create connector rule: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryExecute,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "tool_set", ToolSetID: "github", Connector: "github"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create tool-set rule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}

	_, subject, evaluation, ok, err := manager.evaluateManagerToolPermission(
		ctx,
		"thread-tool-set-rule",
		tools.GitHubToolCloneRepository,
		json.RawMessage(`{"repository":"owner/repo"}`),
		"",
	)
	if err != nil {
		t.Fatalf("evaluateManagerToolPermission: %v", err)
	}
	if !ok {
		t.Fatal("evaluateManagerToolPermission returned ok=false, want tool-set evaluation")
	}
	if evaluation.Decision != permissions.DecisionDeny {
		t.Fatalf("decision=%q, want deny from tool-set rule", evaluation.Decision)
	}
	if subject.Kind != "tool_set" || subject.ToolSetID != "github" || subject.Connector != "github" {
		t.Fatalf("subject=%#v, want github tool_set subject", subject)
	}
}

func TestManagerRemoteResourceRuleCanOverrideConnectorRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryWrite,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       tools.SlackToolPostMessage,
			Connector:  "slack",
			Operation:  "slack_post_message_execute",
			RemoteKind: "slack_channel",
			RemoteID:   "C123",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create remote allow rule: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryWrite,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "slack"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create connector deny rule: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}

	_, subject, evaluation, ok, err := manager.evaluateManagerToolPermission(
		ctx,
		"thread-remote-resource-rule",
		tools.SlackToolPostMessage,
		json.RawMessage(`{"channel_id":"C123","text":"hello"}`),
		"",
	)
	if err != nil {
		t.Fatalf("evaluateManagerToolPermission: %v", err)
	}
	if !ok {
		t.Fatal("evaluateManagerToolPermission returned ok=false, want remote-resource evaluation")
	}
	if evaluation.Decision != permissions.DecisionAllow {
		t.Fatalf("decision=%q, want allow from remote-resource rule", evaluation.Decision)
	}
	if subject.RemoteKind != "slack_channel" || subject.RemoteID != "C123" {
		t.Fatalf("subject=%#v, want slack channel remote subject", subject)
	}

	_, subject, evaluation, ok, err = manager.evaluateManagerToolPermission(
		ctx,
		"thread-remote-resource-rule",
		tools.SlackToolPostMessage,
		json.RawMessage(`{"channel_id":"C999","text":"hello"}`),
		"",
	)
	if err != nil {
		t.Fatalf("evaluateManagerToolPermission other channel: %v", err)
	}
	if !ok {
		t.Fatal("evaluateManagerToolPermission returned ok=false for other channel, want connector evaluation")
	}
	if evaluation.Decision != permissions.DecisionDeny {
		t.Fatalf("other channel decision=%q, want deny from connector rule", evaluation.Decision)
	}
	if subject.Kind != "connector" || subject.Connector != "slack" {
		t.Fatalf("other channel subject=%#v, want slack connector subject", subject)
	}
}

func TestManagerToolSetAllowDoesNotBypassLocalLocationAsk(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	projectID := "project-tool-set-local-guard"
	if err := ps.PutFile(projectID, "README.md", []byte("hello")); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectAllow,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "tool_set", ToolSetID: "local-files"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("Create tool-set rule: %v", err)
	}
	manager := &Manager{
		tools:       tools.NewRegistry(nil),
		projects:    ps,
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}

	_, err = manager.TestTool(ctx, ToolTestRequest{
		ToolName:  "read_file",
		ProjectID: projectID,
		Args:      json.RawMessage(`{"path":"README.md"}`),
	})
	if err == nil {
		t.Fatal("expected local-location permission ask despite tool-set allow")
	}
	var permissionErr *ToolTestPermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("error=%T %v, want ToolTestPermissionError", err, err)
	}
	if permissionErr.Decision != permissions.DecisionAsk || permissionErr.Subject.Kind != "local_location" {
		t.Fatalf("permission error=%#v, want local_location ask", permissionErr)
	}
}

func TestPermissionMetaCoversWriteAndExecuteTools(t *testing.T) {
	for _, meta := range tools.AllToolMetadata() {
		if meta.Category != tools.ToolCategoryWrite && meta.Category != tools.ToolCategoryExecute {
			continue
		}
		got, ok := permissionMetaForManagerTool(meta.Name, permissionMetaTestArgs(meta.Name), "")
		if !ok {
			t.Fatalf("%s category=%s is missing manager permission metadata", meta.Name, meta.Category)
		}
		if got.Category != string(meta.Category) {
			t.Fatalf("%s permission category=%q, want %q", meta.Name, got.Category, meta.Category)
		}
		if got.SubjectKind == "" {
			t.Fatalf("%s permission subject kind is empty", meta.Name)
		}
		if got.Operation == "" {
			t.Fatalf("%s permission operation is empty", meta.Name)
		}
		switch meta.Name {
		case tools.GitHubToolCloneRepository, tools.GitHubToolPullRepository, tools.GitHubToolRefreshRepository:
			if got.Category != permissions.CategoryExecute {
				t.Fatalf("%s category=%q, want execute", meta.Name, got.Category)
			}
			if got.Repository != "owner/repo" || got.RemoteKind != "github_repository" || got.RemoteID != "owner/repo" {
				t.Fatalf("%s repository target=%#v/%#v/%#v, want owner/repo github_repository owner/repo", meta.Name, got.Repository, got.RemoteKind, got.RemoteID)
			}
		case tools.GitHubToolRunGHCommand:
			if got.Repository != "owner/repo" || got.RemoteKind != "github_repository" || got.RemoteID != "owner/repo" {
				t.Fatalf("%s repository target=%#v/%#v/%#v, want owner/repo github_repository owner/repo", meta.Name, got.Repository, got.RemoteKind, got.RemoteID)
			}
		}
	}
}

func TestPermissionMetasForWriteAndExecuteToolsCarryProductScope(t *testing.T) {
	for _, productMeta := range tools.AllToolMetadata() {
		if productMeta.Category != tools.ToolCategoryWrite && productMeta.Category != tools.ToolCategoryExecute {
			continue
		}
		metas := permissionMetasForManagerTool(productMeta.Name, permissionMetaTestArgs(productMeta.Name), "")
		if len(metas) == 0 {
			t.Fatalf("%s category=%s is missing enriched manager permission metadata", productMeta.Name, productMeta.Category)
		}
		hasPrimaryRiskMeta := false
		for _, got := range metas {
			if strings.TrimSpace(got.SubjectKind) == "" {
				t.Fatalf("%s enriched permission subject kind is empty: %#v", productMeta.Name, got)
			}
			if got.SubjectKind != "local_location" && strings.TrimSpace(got.Operation) == "" {
				t.Fatalf("%s enriched permission operation is empty: %#v", productMeta.Name, got)
			}
			if got.ToolSetID != productMeta.ToolSetID {
				t.Fatalf("%s enriched tool_set_id=%q, want %q in %#v", productMeta.Name, got.ToolSetID, productMeta.ToolSetID, got)
			}
			if productMeta.ConnectorID != "" && got.Connector != productMeta.ConnectorID {
				t.Fatalf("%s enriched connector=%q, want %q in %#v", productMeta.Name, got.Connector, productMeta.ConnectorID, got)
			}
			if got.Category == string(productMeta.Category) {
				hasPrimaryRiskMeta = true
			}
		}
		if !hasPrimaryRiskMeta {
			t.Fatalf("%s enriched metas=%#v, want at least one %s permission", productMeta.Name, metas, productMeta.Category)
		}
	}
}

func TestPermissionMetaCoversReadTools(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "projects", "project-reads", "files")
	for _, meta := range tools.AllToolMetadata() {
		if meta.Category != tools.ToolCategoryRead {
			continue
		}
		metas := permissionMetasForManagerTool(meta.Name, permissionMetaTestArgs(meta.Name), projectRoot)
		if len(metas) == 0 {
			t.Fatalf("%s category=read is missing manager permission metadata", meta.Name)
		}
		for _, got := range metas {
			if got.Category != permissions.CategoryRead {
				t.Fatalf("%s permission category=%q, want read", meta.Name, got.Category)
			}
			if got.SubjectKind == "" {
				t.Fatalf("%s permission subject kind is empty", meta.Name)
			}
			if got.Operation == "" {
				t.Fatalf("%s permission operation is empty", meta.Name)
			}
			if got.SubjectKind != "local_location" {
				if got.ToolSetID != meta.ToolSetID {
					t.Fatalf("%s tool_set_id=%q, want %q", meta.Name, got.ToolSetID, meta.ToolSetID)
				}
				if meta.ConnectorID != "" && got.Connector != meta.ConnectorID {
					t.Fatalf("%s connector=%q, want %q", meta.Name, got.Connector, meta.ConnectorID)
				}
			}
			if meta.Origin == tools.ToolOriginConnected && got.SubjectKind != "local_location" && got.Connector == "" {
				t.Fatalf("%s connected read tool has no connector permission metadata", meta.Name)
			}
		}
	}
}

func TestPermissionMetaScopesArtifactReadToolsToRemoteTarget(t *testing.T) {
	cases := []struct {
		name        string
		toolName    string
		args        json.RawMessage
		toolSetID   string
		connector   string
		remoteKind  string
		remoteID    string
		operation   string
		summaryHint string
	}{
		{
			name:        "inspect_attachment",
			toolName:    "inspect_attachment",
			args:        json.RawMessage(`{"artifact_id":"att-1"}`),
			toolSetID:   "attachments",
			remoteKind:  "attachment",
			remoteID:    "att-1",
			operation:   "inspect_attachment",
			summaryHint: "attachment att-1",
		},
		{
			name:        "read_attachment",
			toolName:    "read_attachment",
			args:        json.RawMessage(`{"artifact_id":"att-2"}`),
			toolSetID:   "attachments",
			remoteKind:  "attachment",
			remoteID:    "att-2",
			operation:   "read_attachment",
			summaryHint: "attachment att-2",
		},
		{
			name:        "search_attachment",
			toolName:    "search_attachment",
			args:        json.RawMessage(`{"artifact_id":"att-3","query":"needle"}`),
			toolSetID:   "attachments",
			remoteKind:  "attachment",
			remoteID:    "att-3",
			operation:   "search_attachment",
			summaryHint: "attachment att-3",
		},
		{
			name:        "artifact_chunk",
			toolName:    "get_artifact_chunk",
			args:        json.RawMessage(`{"artifact_id":"artifact-1"}`),
			toolSetID:   "artifacts",
			remoteKind:  "artifact",
			remoteID:    "artifact-1",
			operation:   "get_artifact_chunk",
			summaryHint: "artifact artifact-1",
		},
		{
			name:        "json_artifact",
			toolName:    "get_json_artifact",
			args:        json.RawMessage(`{"artifact_id":"artifact-2"}`),
			toolSetID:   "artifacts",
			remoteKind:  "artifact",
			remoteID:    "artifact-2",
			operation:   "get_json_artifact",
			summaryHint: "artifact artifact-2",
		},
		{
			name:        "search_artifact_content",
			toolName:    "search_artifact_content",
			args:        json.RawMessage(`{"artifact_id":"artifact-search","query":"needle"}`),
			toolSetID:   "artifacts",
			remoteKind:  "artifact",
			remoteID:    "artifact-search",
			operation:   "search_artifact_content",
			summaryHint: "artifact artifact-search",
		},
		{
			name:        "interactive_data_source",
			toolName:    "create_interactive_data_artifact",
			args:        json.RawMessage(`{"title":"Dashboard","source":{"artifact_id":"dataset-interactive"},"html":"<div>ok</div>"}`),
			toolSetID:   "artifacts",
			remoteKind:  "dataset_artifact",
			remoteID:    "dataset-interactive",
			operation:   "create_interactive_data_artifact",
			summaryHint: "dataset_artifact dataset-interactive",
		},
		{
			name:        "inspect_dataset",
			toolName:    "inspect_dataset_artifact",
			args:        json.RawMessage(`{"artifact_id":"dataset-1"}`),
			toolSetID:   "data-analysis",
			remoteKind:  "dataset_artifact",
			remoteID:    "dataset-1",
			operation:   "inspect_dataset_artifact",
			summaryHint: "dataset_artifact dataset-1",
		},
		{
			name:        "dataset_rows",
			toolName:    "get_dataset_rows",
			args:        json.RawMessage(`{"artifact_id":"dataset-2"}`),
			toolSetID:   "data-analysis",
			remoteKind:  "dataset_artifact",
			remoteID:    "dataset-2",
			operation:   "get_dataset_rows",
			summaryHint: "dataset_artifact dataset-2",
		},
		{
			name:        "aggregate_dataset",
			toolName:    "aggregate_dataset",
			args:        json.RawMessage(`{"artifact_id":"dataset-3","metrics":[{"column":"revenue","op":"sum"}]}`),
			toolSetID:   "data-analysis",
			remoteKind:  "dataset_artifact",
			remoteID:    "dataset-3",
			operation:   "aggregate_dataset",
			summaryHint: "dataset_artifact dataset-3",
		},
		{
			name:        "search_dataset",
			toolName:    "search_dataset_rows",
			args:        json.RawMessage(`{"artifact_id":"dataset-4","query":"EMEA"}`),
			toolSetID:   "data-analysis",
			remoteKind:  "dataset_artifact",
			remoteID:    "dataset-4",
			operation:   "search_dataset_rows",
			summaryHint: "dataset_artifact dataset-4",
		},
		{
			name:        "thread_artifact",
			toolName:    "get_artifact",
			args:        json.RawMessage(`{"artifact_id":"thread-artifact-1"}`),
			toolSetID:   "thread-search",
			connector:   "thread-search",
			remoteKind:  "artifact",
			remoteID:    "thread-artifact-1",
			operation:   "get_artifact",
			summaryHint: "artifact thread-artifact-1",
		},
		{
			name:        "camel_case_artifact_id",
			toolName:    "get_artifact_chunk",
			args:        json.RawMessage(`{"artifactId":"artifact-camel"}`),
			toolSetID:   "artifacts",
			remoteKind:  "artifact",
			remoteID:    "artifact-camel",
			operation:   "get_artifact_chunk",
			summaryHint: "artifact artifact-camel",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := permissionMetaForManagerTool(tc.toolName, tc.args, "")
			if !ok {
				t.Fatalf("%s missing permission metadata", tc.toolName)
			}
			if got.Category != permissions.CategoryRead ||
				got.SubjectKind != "tool" ||
				got.ToolSetID != tc.toolSetID ||
				got.Connector != tc.connector ||
				got.Operation != tc.operation ||
				got.RemoteKind != tc.remoteKind ||
				got.RemoteID != tc.remoteID {
				t.Fatalf("metadata=%#v, want scoped %s/%s read target", got, tc.remoteKind, tc.remoteID)
			}
			if !strings.Contains(got.Summary, tc.summaryHint) {
				t.Fatalf("summary=%q, want it to mention %q", got.Summary, tc.summaryHint)
			}
			subject := managerPermissionSubject(got, tc.toolName)
			if subject.Kind != "tool" ||
				subject.Tool != tc.toolName ||
				subject.ToolSetID != tc.toolSetID ||
				subject.Connector != tc.connector ||
				subject.Operation != tc.operation ||
				subject.RemoteKind != tc.remoteKind ||
				subject.RemoteID != tc.remoteID {
				t.Fatalf("subject=%#v, want scoped %s/%s tool subject", subject, tc.remoteKind, tc.remoteID)
			}
		})
	}
}

func TestRequireToolApprovalBlocksDeniedInteractiveDatasetArtifactRead(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	const datasetID = "dataset-interactive-deny"
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectDeny,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:       "tool",
			Tool:       "create_interactive_data_artifact",
			ToolSetID:  "artifacts",
			Operation:  "create_interactive_data_artifact",
			RemoteKind: "dataset_artifact",
			RemoteID:   datasetID,
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
	args := json.RawMessage(`{"title":"Dashboard","source":{"artifact_id":"` + datasetID + `"},"html":"<div>ok</div>"}`)
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_interactive_data", Name: "create_interactive_data_artifact"},
		"create_interactive_data_artifact",
		args,
		"",
		nil,
	)
	if applied {
		t.Fatal("approvalApplied=true for denied dataset artifact source, want false")
	}
	if allowed {
		t.Fatal("allowed=true for denied dataset artifact source, want false")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
	if payload["category"] != permissions.CategoryRead {
		t.Fatalf("category=%#v, want read", payload["category"])
	}
}

func TestPermissionMetaCoversLocalProjectReadTools(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "projects", "project-1", "files")
	cases := []struct {
		toolName      string
		args          map[string]any
		wantLocalRoot string
	}{
		{"list_files", map[string]any{"path": "src"}, filepath.Join(projectRoot, "src")},
		{"read_file", map[string]any{"path": "src/App.tsx"}, filepath.Join(projectRoot, "src", "App.tsx")},
		{"search_files", map[string]any{"query": "needle"}, projectRoot},
		{"list_git_repositories", map[string]any{}, projectRoot},
		{"git_list_refs", map[string]any{"repo_path": "repo"}, filepath.Join(projectRoot, "repo")},
		{"git_log", map[string]any{"repo_path": "repo", "path": "pkg"}, filepath.Join(projectRoot, "repo", "pkg")},
		{"git_search_history", map[string]any{"repo_path": "repo", "query": "needle"}, filepath.Join(projectRoot, "repo")},
		{"git_grep", map[string]any{"repo_path": "repo", "path": "pkg", "query": "needle"}, filepath.Join(projectRoot, "repo", "pkg")},
		{"git_show_commit", map[string]any{"repo_path": "repo", "commit": "abc123"}, filepath.Join(projectRoot, "repo")},
		{"git_show_file_at_ref", map[string]any{"repo_path": "repo", "path": "README.md"}, filepath.Join(projectRoot, "repo", "README.md")},
		{"describe_dataset", map[string]any{"path": "data/sales.csv"}, filepath.Join(projectRoot, "data", "sales.csv")},
		{"sample_rows", map[string]any{"path": "data/sales.csv"}, filepath.Join(projectRoot, "data", "sales.csv")},
		{"filter_csv", map[string]any{"path": "data/sales.csv", "column": "region", "operator": "eq"}, filepath.Join(projectRoot, "data", "sales.csv")},
		{"aggregate_csv", map[string]any{"path": "data/sales.csv", "metrics": []map[string]any{{"column": "revenue", "op": "sum"}}}, filepath.Join(projectRoot, "data", "sales.csv")},
		{"search_threads", map[string]any{"query": "needle"}, projectRoot},
		{"display_table", map[string]any{"source": map[string]any{"path": "data/sales.csv"}}, filepath.Join(projectRoot, "data", "sales.csv")},
		{"build_chart_artifact", map[string]any{"source": map[string]any{"path": "data/sales.csv"}, "chart": map[string]any{"kind": "bar", "x": "region", "y": "revenue"}}, filepath.Join(projectRoot, "data", "sales.csv")},
	}
	for _, tc := range cases {
		b, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatalf("%s json.Marshal: %v", tc.toolName, err)
		}
		got, ok := permissionMetaForManagerTool(tc.toolName, json.RawMessage(b), projectRoot)
		if !ok {
			t.Fatalf("%s missing local read permission metadata", tc.toolName)
		}
		if got.Category != permissions.CategoryRead {
			t.Fatalf("%s category=%q, want read", tc.toolName, got.Category)
		}
		if got.SubjectKind != "local_location" {
			t.Fatalf("%s subject kind=%q, want local_location", tc.toolName, got.SubjectKind)
		}
		if filepath.Clean(got.LocalRoot) != filepath.Clean(tc.wantLocalRoot) {
			t.Fatalf("%s local root=%q, want %q", tc.toolName, got.LocalRoot, tc.wantLocalRoot)
		}
		if got.Operation == "" || got.Summary == "" {
			t.Fatalf("%s metadata missing operation/summary: %#v", tc.toolName, got)
		}
		if !containsStringForTest(got.Excludes, ".env") || !containsStringForTest(got.Excludes, "**/.ssh/**") {
			t.Fatalf("%s local read excludes=%v, want sensitive local exclusion patterns", tc.toolName, got.Excludes)
		}
	}
}

func TestPermissionMetaCoversGitHubSourceCloneReadTools(t *testing.T) {
	ctx := context.Background()
	sourceStore, err := sources.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("sources.NewStore: %v", err)
	}
	worktree := filepath.Join(t.TempDir(), "sources", "github", "worktree")
	sourceID := sources.GitHubSourceID("acme/widget")
	if _, err := sourceStore.Upsert(ctx, sources.Source{
		ID:         sourceID,
		Kind:       sources.KindGitHubClone,
		Repository: "acme/widget",
		Owner:      "acme",
		Repo:       "widget",
		Worktree:   worktree,
	}); err != nil {
		t.Fatalf("source Upsert: %v", err)
	}
	m := &Manager{sources: sourceStore}
	cases := []struct {
		toolName      string
		args          map[string]any
		wantLocalRoot string
	}{
		{"list_git_repositories", map[string]any{}, worktree},
		{"git_list_refs", map[string]any{}, worktree},
		{"git_log", map[string]any{"path": "pkg"}, filepath.Join(worktree, "pkg")},
		{"git_grep", map[string]any{"repo_path": sourceID, "path": "pkg", "query": "needle"}, filepath.Join(worktree, "pkg")},
		{"git_show_file_at_ref", map[string]any{"repo_path": "acme/widget", "path": "README.md"}, filepath.Join(worktree, "README.md")},
	}
	for _, tc := range cases {
		b, err := json.Marshal(tc.args)
		if err != nil {
			t.Fatalf("%s json.Marshal: %v", tc.toolName, err)
		}
		got, ok := m.gitSourceReadPermissionMetaForManagerTool(ctx, tc.toolName, json.RawMessage(b))
		if !ok {
			t.Fatalf("%s missing source clone permission metadata", tc.toolName)
		}
		if got.Category != permissions.CategoryRead ||
			got.SubjectKind != "local_location" ||
			filepath.Clean(got.LocalRoot) != filepath.Clean(tc.wantLocalRoot) ||
			got.Operation != tc.toolName {
			t.Fatalf("%s metadata=%#v, want read local_location %s", tc.toolName, got, tc.wantLocalRoot)
		}
	}
}

func TestPermissionMetaCoversMachineLocalFileSearch(t *testing.T) {
	localRoot := filepath.Join(t.TempDir(), "Documents")
	got, ok := permissionMetaForManagerTool("file_search", mustJSONForPermissionTest(map[string]any{
		"local_root": localRoot,
		"path":       "Reports",
		"query":      "needle",
	}), "")
	if !ok {
		t.Fatal("file_search missing local machine permission metadata")
	}
	wantRoot := filepath.Join(localRoot, "Reports")
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		filepath.Clean(got.LocalRoot) != filepath.Clean(wantRoot) ||
		got.Operation != "file_search" {
		t.Fatalf("permission metadata=%#v, want read local_location %s", got, wantRoot)
	}
	if !containsStringForTest(got.Excludes, ".env") || !containsStringForTest(got.Excludes, "**/.ssh/**") {
		t.Fatalf("file_search local excludes=%v, want sensitive local exclusion patterns", got.Excludes)
	}

	got, ok = permissionMetaForManagerTool("file_search", mustJSONForPermissionTest(map[string]any{
		"query": "needle",
	}), "")
	if !ok {
		t.Fatal("file_search without local_root should create local-location approval metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		got.LocalRoot != "" ||
		got.Operation != "file_search" ||
		!strings.Contains(got.Summary, "needle") {
		t.Fatalf("rootless file_search metadata=%#v, want read local_location approval request with query summary", got)
	}

	got, ok = permissionMetaForManagerTool("local_list_folder", mustJSONForPermissionTest(map[string]any{
		"local_root": localRoot,
		"path":       "Reports",
	}), "")
	if !ok {
		t.Fatal("local_list_folder missing local machine permission metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		filepath.Clean(got.LocalRoot) != filepath.Clean(wantRoot) ||
		got.Operation != "local_list_folder" ||
		got.LocalKind != "folder" {
		t.Fatalf("local_list_folder metadata=%#v, want read local_location folder list %s", got, wantRoot)
	}
	if !containsStringForTest(got.Excludes, ".env") || !containsStringForTest(got.Excludes, "**/.ssh/**") {
		t.Fatalf("local_list_folder local excludes=%v, want sensitive local exclusion patterns", got.Excludes)
	}

	got, ok = permissionMetaForManagerTool("local_list_folder", mustJSONForPermissionTest(map[string]any{
		"path": "Reports",
	}), "")
	if !ok {
		t.Fatal("local_list_folder without local_root should create local-location approval metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		got.LocalRoot != "" ||
		got.Operation != "local_list_folder" ||
		got.LocalKind != "folder" ||
		!strings.Contains(got.Summary, "Reports") {
		t.Fatalf("rootless local_list_folder metadata=%#v, want read local_location folder approval request", got)
	}

	got, ok = permissionMetaForManagerTool("local_read_file", mustJSONForPermissionTest(map[string]any{
		"local_root": localRoot,
		"path":       "Reports/summary.txt",
	}), "")
	if !ok {
		t.Fatal("local_read_file missing local machine permission metadata")
	}
	wantFile := filepath.Join(localRoot, "Reports", "summary.txt")
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		filepath.Clean(got.LocalRoot) != filepath.Clean(wantFile) ||
		got.Operation != "local_read_file" {
		t.Fatalf("permission metadata=%#v, want read local_location %s", got, wantFile)
	}
	if !containsStringForTest(got.Excludes, ".env") || !containsStringForTest(got.Excludes, "**/.ssh/**") {
		t.Fatalf("local_read_file local excludes=%v, want sensitive local exclusion patterns", got.Excludes)
	}

	got, ok = permissionMetaForManagerTool("local_read_file", mustJSONForPermissionTest(map[string]any{
		"path": "Reports/summary.txt",
	}), "")
	if !ok {
		t.Fatal("local_read_file without local_root should create local-location approval metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		got.LocalRoot != "" ||
		got.Operation != "local_read_file" ||
		!strings.Contains(got.Summary, "Reports/summary.txt") {
		t.Fatalf("rootless local_read_file metadata=%#v, want read local_location approval request with path summary", got)
	}

	got, ok = permissionMetaForManagerTool("local_open_file", mustJSONForPermissionTest(map[string]any{
		"local_root": localRoot,
		"path":       "Reports/source.pdf",
	}), "")
	if !ok {
		t.Fatal("local_open_file missing local machine permission metadata")
	}
	wantOpenFile := filepath.Join(localRoot, "Reports", "source.pdf")
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		filepath.Clean(got.LocalRoot) != filepath.Clean(wantOpenFile) ||
		got.Operation != "local_open_file" ||
		got.LocalKind != "file" {
		t.Fatalf("local_open_file metadata=%#v, want read local_location file %s", got, wantOpenFile)
	}

	got, ok = permissionMetaForManagerTool("local_open_file", mustJSONForPermissionTest(map[string]any{
		"path": "Reports/source.pdf",
	}), "")
	if !ok {
		t.Fatal("local_open_file without local_root should create local-location approval metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		got.LocalRoot != "" ||
		got.Operation != "local_open_file" ||
		got.LocalKind != "file" ||
		!strings.Contains(got.Summary, "Reports/source.pdf") {
		t.Fatalf("rootless local_open_file metadata=%#v, want read local_location file approval request", got)
	}

	got, ok = permissionMetaForManagerTool("local_read_pdf_page", mustJSONForPermissionTest(map[string]any{
		"local_root":  localRoot,
		"path":        "Reports/source.pdf",
		"page_number": 27,
	}), "")
	if !ok {
		t.Fatal("local_read_pdf_page missing local machine permission metadata")
	}
	wantPDF := filepath.Join(localRoot, "Reports", "source.pdf")
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		filepath.Clean(got.LocalRoot) != filepath.Clean(wantPDF) ||
		got.Operation != "local_read_pdf_page" ||
		got.LocalKind != "file" {
		t.Fatalf("local_read_pdf_page metadata=%#v, want read local_location file %s", got, wantPDF)
	}

	got, ok = permissionMetaForManagerTool("local_read_pdf_page", mustJSONForPermissionTest(map[string]any{
		"path":        "Reports/source.pdf",
		"page_number": 27,
	}), "")
	if !ok {
		t.Fatal("local_read_pdf_page without local_root should create local-location approval metadata")
	}
	if got.Category != permissions.CategoryRead ||
		got.SubjectKind != "local_location" ||
		got.LocalRoot != "" ||
		got.Operation != "local_read_pdf_page" ||
		got.LocalKind != "file" ||
		!strings.Contains(got.Summary, "Reports/source.pdf") {
		t.Fatalf("rootless local_read_pdf_page metadata=%#v, want read local_location approval request with path summary", got)
	}
}

func TestPermissionMetaResolvesOmittedGitRepoPathForSingleVisibleRepo(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-git-permission"
	projectRoot, err := ps.EnsureFilesDir(projectID)
	if err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "repo", ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo .git: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(projectRoot, "repo", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir repo pkg: %v", err)
	}
	manager := &Manager{projects: ps}

	cases := []struct {
		toolName      string
		args          json.RawMessage
		wantLocalRoot string
	}{
		{"git_log", json.RawMessage(`{"path":"pkg"}`), filepath.Join(projectRoot, "repo", "pkg")},
		{"git_grep", json.RawMessage(`{"path":"pkg","query":"needle"}`), filepath.Join(projectRoot, "repo", "pkg")},
		{"git_show_file_at_ref", json.RawMessage(`{"path":"README.md"}`), filepath.Join(projectRoot, "repo", "README.md")},
		{"git_search_history", json.RawMessage(`{"query":"needle"}`), filepath.Join(projectRoot, "repo")},
	}
	for _, tc := range cases {
		metas := manager.permissionMetasForTool(ctx, "", tc.toolName, tc.args, projectID)
		if len(metas) != 1 {
			t.Fatalf("%s metas=%#v, want one local read meta", tc.toolName, metas)
		}
		if metas[0].SubjectKind != "local_location" || filepath.Clean(metas[0].LocalRoot) != filepath.Clean(tc.wantLocalRoot) {
			t.Fatalf("%s local meta=%#v, want root %q", tc.toolName, metas[0], tc.wantLocalRoot)
		}
	}
}

func TestPermissionMetaSkipsOmittedGitRepoPathWhenMultipleReposVisible(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-git-permission-multiple"
	projectRoot, err := ps.EnsureFilesDir(projectID)
	if err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	for _, repo := range []string{"repo-a", "repo-b"} {
		if err := os.MkdirAll(filepath.Join(projectRoot, repo, ".git"), 0o755); err != nil {
			t.Fatalf("mkdir %s .git: %v", repo, err)
		}
	}
	manager := &Manager{projects: ps}

	metas := manager.permissionMetasForTool(ctx, "", "git_grep", json.RawMessage(`{"path":"pkg","query":"needle"}`), projectID)
	if len(metas) != 0 {
		t.Fatalf("metas=%#v, want no misleading local read meta without repo_path", metas)
	}
}

func TestPermissionMetaResolvesOmittedGitHubRepositoryForSingleVisibleRepo(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-github-permission"
	projectRoot, err := ps.EnsureFilesDir(projectID)
	if err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	writeGitHubRepoForPermissionTest(t, projectRoot, "repo", "Owner/Repo")
	manager := &Manager{projects: ps}

	readCases := []struct {
		name       string
		toolName   string
		args       json.RawMessage
		remoteKind string
		remoteID   string
	}{
		{
			name:       "pull_requests",
			toolName:   tools.GitHubToolListPullRequests,
			args:       json.RawMessage(`{"state":"open"}`),
			remoteKind: "github_repository",
			remoteID:   "owner/repo",
		},
		{
			name:       "pull_request",
			toolName:   tools.GitHubToolGetPullRequest,
			args:       json.RawMessage(`{"number":12}`),
			remoteKind: "github_pull_request",
			remoteID:   "owner/repo#12",
		},
		{
			name:       "issue",
			toolName:   tools.GitHubToolGetIssue,
			args:       json.RawMessage(`{"number":34}`),
			remoteKind: "github_issue",
			remoteID:   "owner/repo#34",
		},
		{
			name:       "workflow_run",
			toolName:   tools.GitHubToolGetWorkflowRunJobs,
			args:       json.RawMessage(`{"run_id":"98765"}`),
			remoteKind: "github_workflow_run",
			remoteID:   "owner/repo/98765",
		},
	}
	for _, tc := range readCases {
		t.Run(tc.name, func(t *testing.T) {
			metas := manager.permissionMetasForTool(ctx, "", tc.toolName, tc.args, projectID)
			if len(metas) != 1 {
				t.Fatalf("%s metas=%#v, want one GitHub read meta", tc.toolName, metas)
			}
			got := metas[0]
			if got.Category != permissions.CategoryRead ||
				got.Connector != "github" ||
				got.ToolSetID != "github" ||
				got.Operation != tc.toolName ||
				got.RemoteKind != tc.remoteKind ||
				got.RemoteID != tc.remoteID {
				t.Fatalf("%s meta=%#v, want %s/%s", tc.toolName, got, tc.remoteKind, tc.remoteID)
			}
		})
	}

	for _, tc := range []struct {
		toolName  string
		operation string
	}{
		{tools.GitHubToolPullRepository, "pull_repository"},
		{tools.GitHubToolRefreshRepository, "refresh_repository"},
	} {
		t.Run(tc.toolName, func(t *testing.T) {
			metas := manager.permissionMetasForTool(ctx, "", tc.toolName, json.RawMessage(`{}`), projectID)
			if len(metas) != 1 {
				t.Fatalf("%s metas=%#v, want one GitHub execute meta", tc.toolName, metas)
			}
			got := metas[0]
			if got.Category != permissions.CategoryExecute ||
				got.SubjectKind != "repository" ||
				got.Connector != "github" ||
				got.Repository != "owner/repo" ||
				got.RemoteKind != "github_repository" ||
				got.RemoteID != "owner/repo" ||
				got.Operation != tc.operation {
				t.Fatalf("%s meta=%#v, want repository execute for owner/repo", tc.toolName, got)
			}
		})
	}
}

func TestPermissionMetaDoesNotInventOmittedGitHubRepositoryWhenMultipleReposVisible(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-github-permission-multiple"
	projectRoot, err := ps.EnsureFilesDir(projectID)
	if err != nil {
		t.Fatalf("EnsureFilesDir: %v", err)
	}
	writeGitHubRepoForPermissionTest(t, projectRoot, "repo-a", "Owner/RepoA")
	writeGitHubRepoForPermissionTest(t, projectRoot, "repo-b", "Owner/RepoB")
	manager := &Manager{projects: ps}

	metas := manager.permissionMetasForTool(ctx, "", tools.GitHubToolGetIssue, json.RawMessage(`{"number":34}`), projectID)
	if len(metas) != 1 {
		t.Fatalf("read metas=%#v, want broad GitHub tool read fallback", metas)
	}
	if got := metas[0]; got.Category != permissions.CategoryRead ||
		got.SubjectKind != "tool" ||
		got.Connector != "github" ||
		got.RemoteKind != "" ||
		got.RemoteID != "" {
		t.Fatalf("read meta=%#v, want broad GitHub tool read fallback without remote target", got)
	}

	metas = manager.permissionMetasForTool(ctx, "", tools.GitHubToolPullRepository, json.RawMessage(`{}`), projectID)
	if len(metas) != 1 {
		t.Fatalf("execute metas=%#v, want broad GitHub tool execute fallback", metas)
	}
	if got := metas[0]; got.Category != permissions.CategoryExecute ||
		got.SubjectKind != "tool" ||
		got.Connector != "github" ||
		got.Repository != "" {
		t.Fatalf("execute meta=%#v, want broad GitHub tool execute fallback without repository", got)
	}
}

func writeGitHubRepoForPermissionTest(t *testing.T, projectRoot string, repoPath string, fullName string) {
	t.Helper()
	configDir := filepath.Join(projectRoot, filepath.FromSlash(repoPath), ".git")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", configDir, err)
	}
	config := fmt.Sprintf("[remote \"origin\"]\n\turl = https://github.com/%s.git\n", fullName)
	if err := os.WriteFile(filepath.Join(configDir, "config"), []byte(config), 0o600); err != nil {
		t.Fatalf("write git config for %s: %v", repoPath, err)
	}
}

func TestPermissionMetaSearchThreadsIncludeFilesFalseUsesThreadSearchPermission(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "projects", "project-1", "files")
	got, ok := permissionMetaForManagerTool("search_threads", json.RawMessage(`{"query":"needle","include_files":false}`), projectRoot)
	if !ok {
		t.Fatal("search_threads include_files=false missing permission metadata")
	}
	if got.SubjectKind != "tool" || got.Connector != "thread-search" || got.Category != permissions.CategoryRead {
		t.Fatalf("permission metadata=%#v, want thread-search tool read permission", got)
	}
	if got.LocalRoot != "" {
		t.Fatalf("local root=%q, want empty when include_files=false", got.LocalRoot)
	}
}

func TestPermissionMetasSkipChatUploadAttachmentReadApproval(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := types.ThreadFileAttachmentToolName
	contentPath := "thread_uploads/" + thread.ID + "/notes.txt"
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "notes.txt",
		ToolName:    &toolName,
		Storage:     "file",
		ContentPath: &contentPath,
		Metadata: map[string]any{
			"source": types.ChatUploadAttachmentSource,
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}
	for _, toolName := range []string{"inspect_attachment", "read_attachment", "search_attachment"} {
		args := json.RawMessage(fmt.Sprintf(`{"artifact_id":%q,"query":"needle"}`, artifact.ID))
		if metas := manager.permissionMetasForTool(ctx, thread.ID, toolName, args, ""); len(metas) != 0 {
			t.Fatalf("%s metas=%#v, want chat-upload attachment reads to be implicitly approved", toolName, metas)
		}
	}
}

func TestPermissionMetasKeepExternalAttachmentReadApproval(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	contentPath := "external/downloads/file.txt"
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "file.txt",
		Storage:     "file",
		ContentPath: &contentPath,
		Metadata: map[string]any{
			"source": fileartifact.SourceExternalTool,
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	manager := &Manager{store: st, cfgDir: cfgDir}
	metas := manager.permissionMetasForTool(ctx, thread.ID, "read_attachment", json.RawMessage(fmt.Sprintf(`{"artifact_id":%q}`, artifact.ID)), "")
	if len(metas) != 1 {
		t.Fatalf("metas=%#v, want external attachment read approval metadata", metas)
	}
	if got := metas[0]; got.Category != permissions.CategoryRead || got.RemoteKind != "attachment" || got.RemoteID != artifact.ID {
		t.Fatalf("meta=%#v, want attachment read metadata scoped to external artifact", got)
	}
}

func TestRequireToolApprovalBlocksDeniedLocalRead(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	projectID := "project-local-deny"
	if err := ps.PutFile(projectID, "src/App.tsx", []byte("hello")); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	projectRoot, err := ps.FilesDir(projectID)
	if err != nil {
		t.Fatalf("FilesDir: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: projectRoot},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	thread, err := st.CreateThread(ctx, &projectID)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		projects:    ps,
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_read", Name: "read_file"},
		"read_file",
		json.RawMessage(`{"path":"src/App.tsx"}`),
		projectID,
		nil,
	)
	if applied {
		t.Fatal("approvalApplied=true for denied rule, want false")
	}
	if allowed {
		t.Fatal("allowed=true for denied local read rule, want false")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
}

func TestRequireToolApprovalBlocksDeniedSearchThreadsFileSearch(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	projectID := "project-search-threads-deny"
	if err := ps.PutFile(projectID, "notes.txt", []byte("needle")); err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	projectRoot, err := ps.FilesDir(projectID)
	if err != nil {
		t.Fatalf("FilesDir: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: projectRoot},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}

	thread, err := st.CreateThread(ctx, &projectID)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{
		projects:    ps,
		permissions: permissionSvc,
		cfgDir:      cfgDir,
	}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_search_threads", Name: "search_threads"},
		"search_threads",
		json.RawMessage(`{"query":"needle"}`),
		projectID,
		nil,
	)
	if applied {
		t.Fatal("approvalApplied=true for denied rule, want false")
	}
	if allowed {
		t.Fatal("allowed=true for denied search_threads file search, want false")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
}

func TestRequireToolApprovalBlocksDeniedUploadProjectFileRead(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		args     json.RawMessage
	}{
		{
			name:     "slack",
			toolName: tools.SlackToolUploadFile,
			args:     json.RawMessage(`{"channel_id":"C123","path":"docs/report.txt","fileName":"report.txt"}`),
		},
		{
			name:     "jira",
			toolName: "jira_upload_attachment",
			args:     json.RawMessage(`{"issueKey":"PROJ-1","path":"docs/report.txt","fileName":"report.txt"}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			ps, err := projects.NewStore(cfgDir)
			if err != nil {
				t.Fatalf("projects.NewStore: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			projectID := "project-upload-deny-" + tc.name
			if err := ps.PutFile(projectID, "docs/report.txt", []byte("report")); err != nil {
				t.Fatalf("PutFile: %v", err)
			}
			projectRoot, err := ps.FilesDir(projectID)
			if err != nil {
				t.Fatalf("FilesDir: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: projectRoot},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule local deny: %v", err)
			}
			thread, err := st.CreateThread(ctx, &projectID)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			manager := &Manager{
				projects:    ps,
				permissions: permissionSvc,
				cfgDir:      cfgDir,
			}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_upload_" + tc.name, Name: tc.toolName},
				tc.toolName,
				tc.args,
				projectID,
				nil,
			)
			if applied {
				t.Fatal("approvalApplied=true for denied local upload source, want false")
			}
			if allowed {
				t.Fatal("allowed=true for denied local upload source, want false")
			}
			payload, ok := out.Result.(map[string]any)
			if !ok {
				t.Fatalf("approval output result=%T, want object", out.Result)
			}
			if payload["status"] != "approval_denied" {
				t.Fatalf("status=%#v, want approval_denied", payload["status"])
			}
			if payload["category"] != permissions.CategoryRead {
				t.Fatalf("category=%#v, want read local source denial", payload["category"])
			}
		})
	}
}

func TestRequireToolApprovalAllowsUploadProjectFileWhenReadAndWriteAllowed(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		connector string
		args      json.RawMessage
	}{
		{
			name:      "slack",
			toolName:  tools.SlackToolUploadFile,
			connector: "slack",
			args:      json.RawMessage(`{"channel_id":"C123","path":"docs/report.txt","fileName":"report.txt"}`),
		},
		{
			name:      "jira",
			toolName:  "jira_upload_attachment",
			connector: "atlassian",
			args:      json.RawMessage(`{"issueKey":"PROJ-1","path":"docs/report.txt","fileName":"report.txt"}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			ps, err := projects.NewStore(cfgDir)
			if err != nil {
				t.Fatalf("projects.NewStore: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			projectID := "project-upload-allow-" + tc.name
			if err := ps.PutFile(projectID, "docs/report.txt", []byte("report")); err != nil {
				t.Fatalf("PutFile: %v", err)
			}
			projectRoot, err := ps.FilesDir(projectID)
			if err != nil {
				t.Fatalf("FilesDir: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectAllow,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: filepath.Join(projectRoot, "docs", "report.txt")},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule local allow: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category: permissions.CategoryWrite,
				Effect:   permissions.EffectAllow,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:      "tool",
					Tool:      tc.toolName,
					Connector: tc.connector,
				},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule write allow: %v", err)
			}
			thread, err := st.CreateThread(ctx, &projectID)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			manager := &Manager{
				projects:    ps,
				permissions: permissionSvc,
				cfgDir:      cfgDir,
			}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_upload_" + tc.name, Name: tc.toolName},
				tc.toolName,
				tc.args,
				projectID,
				nil,
			)
			if !applied {
				t.Fatal("approvalApplied=false, want true when local read and external write are both allowed")
			}
			if !allowed {
				t.Fatalf("allowed=false output=%#v", out.Result)
			}
		})
	}
}

func TestRequireToolApprovalBlocksDeniedUploadManagedLocalFileRead(t *testing.T) {
	cases := []struct {
		name        string
		toolName    string
		relative    string
		absoluteArg bool
		args        func(string) json.RawMessage
	}{
		{
			name:     "slack",
			toolName: tools.SlackToolUploadFile,
			relative: "slack/downloads/F123/report.txt",
			args: func(localPath string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"channel_id": "C123",
					"local_path": localPath,
					"fileName":   "report.txt",
				})
			},
		},
		{
			name:        "jira",
			toolName:    "jira_upload_attachment",
			relative:    "atlassian/downloads/PROJ-1/report.txt",
			absoluteArg: true,
			args: func(localPath string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"issueKey":   "PROJ-1",
					"local_path": localPath,
					"fileName":   "report.txt",
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			localFile := filepath.Join(cfgDir, filepath.FromSlash(tc.relative))
			if err := os.MkdirAll(filepath.Dir(localFile), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(localFile, []byte("report"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: localFile},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule local deny: %v", err)
			}
			thread, err := st.CreateThread(ctx, nil)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			localArg := tc.relative
			if tc.absoluteArg {
				localArg = localFile
			}
			manager := &Manager{
				permissions: permissionSvc,
				cfgDir:      cfgDir,
			}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_upload_local_" + tc.name, Name: tc.toolName},
				tc.toolName,
				tc.args(localArg),
				"",
				nil,
			)
			if applied {
				t.Fatal("approvalApplied=true for denied managed local upload source, want false")
			}
			if allowed {
				t.Fatal("allowed=true for denied managed local upload source, want false")
			}
			payload, ok := out.Result.(map[string]any)
			if !ok {
				t.Fatalf("approval output result=%T, want object", out.Result)
			}
			if payload["status"] != "approval_denied" {
				t.Fatalf("status=%#v, want approval_denied", payload["status"])
			}
			if payload["category"] != permissions.CategoryRead {
				t.Fatalf("category=%#v, want read local source denial", payload["category"])
			}
		})
	}
}

func TestRequireToolApprovalAllowsUploadManagedLocalFileWhenReadAndWriteAllowed(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		connector string
		relative  string
		args      func(string) json.RawMessage
	}{
		{
			name:      "slack",
			toolName:  tools.SlackToolUploadFile,
			connector: "slack",
			relative:  "slack/downloads/F123/report.txt",
			args: func(localPath string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"channel_id": "C123",
					"localPath":  localPath,
					"fileName":   "report.txt",
				})
			},
		},
		{
			name:      "jira",
			toolName:  "jira_upload_attachment",
			connector: "atlassian",
			relative:  "atlassian/downloads/PROJ-1/report.txt",
			args: func(localPath string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"issueKey":  "PROJ-1",
					"localPath": localPath,
					"fileName":  "report.txt",
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			localFile := filepath.Join(cfgDir, filepath.FromSlash(tc.relative))
			if err := os.MkdirAll(filepath.Dir(localFile), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(localFile, []byte("report"), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectAllow,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: localFile},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule local allow: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category: permissions.CategoryWrite,
				Effect:   permissions.EffectAllow,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:      "tool",
					Tool:      tc.toolName,
					Connector: tc.connector,
				},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule write allow: %v", err)
			}
			thread, err := st.CreateThread(ctx, nil)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			manager := &Manager{
				permissions: permissionSvc,
				cfgDir:      cfgDir,
			}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_upload_local_allow_" + tc.name, Name: tc.toolName},
				tc.toolName,
				tc.args(tc.relative),
				"",
				nil,
			)
			if !applied {
				t.Fatal("approvalApplied=false, want true when managed local read and external write are both allowed")
			}
			if !allowed {
				t.Fatalf("allowed=false output=%#v", out.Result)
			}
		})
	}
}

func TestRequireToolApprovalBlocksDeniedArtifactBackedLocalRead(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		args     func(string) json.RawMessage
	}{
		{
			name:     "table",
			toolName: "display_table",
			args: func(artifactID string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"source": map[string]any{"artifact_id": artifactID},
				})
			},
		},
		{
			name:     "chart",
			toolName: "build_chart_artifact",
			args: func(artifactID string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"source": map[string]any{"artifact_id": artifactID},
					"chart":  map[string]any{"kind": "bar", "x": "region", "y": "revenue"},
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			ps, err := projects.NewStore(cfgDir)
			if err != nil {
				t.Fatalf("projects.NewStore: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			projectID := "project-artifact-deny-" + tc.name
			if err := ps.PutFile(projectID, "data/sales.csv", []byte("region,revenue\nEMEA,10\n")); err != nil {
				t.Fatalf("PutFile: %v", err)
			}
			projectRoot, err := ps.FilesDir(projectID)
			if err != nil {
				t.Fatalf("FilesDir: %v", err)
			}
			localFile := filepath.Join(projectRoot, "data", "sales.csv")
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: localFile},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule local deny: %v", err)
			}
			thread, err := st.CreateThread(ctx, &projectID)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			artifactID := "artifact-read-file-" + tc.name
			insertReadFileArtifactForPermissionTest(t, ctx, st, thread.ID, artifactID, "data/sales.csv")
			manager := &Manager{
				projects:    ps,
				store:       st,
				permissions: permissionSvc,
				cfgDir:      cfgDir,
			}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_artifact_" + tc.name, Name: tc.toolName},
				tc.toolName,
				tc.args(artifactID),
				projectID,
				nil,
			)
			if applied {
				t.Fatal("approvalApplied=true for denied artifact-backed local read, want false")
			}
			if allowed {
				t.Fatal("allowed=true for denied artifact-backed local read, want false")
			}
			payload, ok := out.Result.(map[string]any)
			if !ok {
				t.Fatalf("approval output result=%T, want object", out.Result)
			}
			if payload["status"] != "approval_denied" {
				t.Fatalf("status=%#v, want approval_denied", payload["status"])
			}
			if payload["category"] != permissions.CategoryRead {
				t.Fatalf("category=%#v, want read local artifact source denial", payload["category"])
			}
		})
	}
}

func TestRequireToolApprovalAllowsArtifactBackedLocalReadWhenFileAndToolAllowed(t *testing.T) {
	cases := []struct {
		name     string
		toolName string
		args     func(string) json.RawMessage
	}{
		{
			name:     "table",
			toolName: "display_table",
			args: func(artifactID string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"source": map[string]any{"artifact_id": artifactID},
				})
			},
		},
		{
			name:     "chart",
			toolName: "build_chart_artifact",
			args: func(artifactID string) json.RawMessage {
				return mustJSONForPermissionTest(map[string]any{
					"source": map[string]any{"artifact_id": artifactID},
					"chart":  map[string]any{"kind": "bar", "x": "region", "y": "revenue"},
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			ps, err := projects.NewStore(cfgDir)
			if err != nil {
				t.Fatalf("projects.NewStore: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			projectID := "project-artifact-allow-" + tc.name
			if err := ps.PutFile(projectID, "data/sales.csv", []byte("region,revenue\nEMEA,10\n")); err != nil {
				t.Fatalf("PutFile: %v", err)
			}
			projectRoot, err := ps.FilesDir(projectID)
			if err != nil {
				t.Fatalf("FilesDir: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectAllow,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "local_location", LocalRoot: filepath.Join(projectRoot, "data", "sales.csv")},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule local allow: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectAllow,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "tool", Tool: tc.toolName},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule tool allow: %v", err)
			}
			thread, err := st.CreateThread(ctx, &projectID)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			artifactID := "artifact-read-file-allow-" + tc.name
			insertReadFileArtifactForPermissionTest(t, ctx, st, thread.ID, artifactID, "data/sales.csv")
			manager := &Manager{
				projects:    ps,
				store:       st,
				permissions: permissionSvc,
				cfgDir:      cfgDir,
			}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_artifact_allow_" + tc.name, Name: tc.toolName},
				tc.toolName,
				tc.args(artifactID),
				projectID,
				nil,
			)
			if !applied {
				t.Fatal("approvalApplied=false, want true when artifact local read and tool read are both allowed")
			}
			if !allowed {
				t.Fatalf("allowed=false output=%#v", out.Result)
			}
		})
	}
}

func TestRequireToolApprovalBlocksDeniedConnectedReadTool(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryRead,
		Effect:   permissions.EffectDeny,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "tool",
			Tool:      tools.GitHubToolListRepositories,
			Connector: "github",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_read_github", Name: tools.GitHubToolListRepositories},
		tools.GitHubToolListRepositories,
		json.RawMessage(`{}`),
		"",
		nil,
	)
	if applied {
		t.Fatal("approvalApplied=true for denied connected read rule, want false")
	}
	if allowed {
		t.Fatal("allowed=true for denied connected read rule, want false")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
}

func TestRequireToolApprovalBlocksDeniedConnectedReadConnector(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "github"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_read_github", Name: tools.GitHubToolListRepositories},
		tools.GitHubToolListRepositories,
		json.RawMessage(`{}`),
		"",
		nil,
	)
	if applied {
		t.Fatal("approvalApplied=true for denied connector read rule, want false")
	}
	if allowed {
		t.Fatal("allowed=true for denied connector read rule, want false")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
}

func TestRequireToolApprovalBlocksDeniedDatasourceReadConnectors(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		connector string
	}{
		{"bigquery", "list_bq_datasets", "bigquery"},
		{"databricks", "list_databricks_catalogs", "databricks"},
		{"trino", "list_trino_catalogs", "trino"},
		{"snowflake", "list_snowflake_databases", "snowflake"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "connector", Connector: tc.connector},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule: %v", err)
			}
			thread, err := st.CreateThread(ctx, nil)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_read_" + tc.connector, Name: tc.toolName},
				tc.toolName,
				json.RawMessage(`{}`),
				"",
				nil,
			)
			if applied {
				t.Fatal("approvalApplied=true for denied connector read rule, want false")
			}
			if allowed {
				t.Fatal("allowed=true for denied datasource read rule, want false")
			}
			payload, ok := out.Result.(map[string]any)
			if !ok {
				t.Fatalf("approval output result=%T, want object", out.Result)
			}
			if payload["status"] != "approval_denied" {
				t.Fatalf("status=%#v, want approval_denied", payload["status"])
			}
		})
	}
}

func TestRequireToolApprovalToolRuleOverridesConnectorRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectAllow,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "tool", Tool: tools.GitHubToolListRepositories, Connector: "github"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule tool allow: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryRead,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "github"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule connector deny: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_read_github", Name: tools.GitHubToolListRepositories},
		tools.GitHubToolListRepositories,
		json.RawMessage(`{}`),
		"",
		nil,
	)
	if !applied {
		t.Fatal("approvalApplied=false, want true for explicit tool allow")
	}
	if !allowed {
		t.Fatalf("allowed=false output=%#v", out.Result)
	}
}

func TestRequireToolApprovalDatasourceToolRuleOverridesConnectorRule(t *testing.T) {
	cases := []struct {
		name      string
		toolName  string
		connector string
	}{
		{"bigquery", "list_bq_datasets", "bigquery"},
		{"databricks", "list_databricks_catalogs", "databricks"},
		{"trino", "list_trino_catalogs", "trino"},
		{"snowflake", "list_snowflake_databases", "snowflake"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cfgDir := t.TempDir()
			st, err := store.NewJSON(cfgDir)
			if err != nil {
				t.Fatalf("store.NewJSON: %v", err)
			}
			permissionSvc, err := permissions.NewService(cfgDir, st)
			if err != nil {
				t.Fatalf("permissions.NewService: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category: permissions.CategoryRead,
				Effect:   permissions.EffectAllow,
				Scope:    "global",
				Subject: store.PermissionSubject{
					Kind:      "tool",
					Tool:      tc.toolName,
					Connector: tc.connector,
				},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule tool allow: %v", err)
			}
			if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
				Category:   permissions.CategoryRead,
				Effect:     permissions.EffectDeny,
				Scope:      "global",
				Subject:    store.PermissionSubject{Kind: "connector", Connector: tc.connector},
				CreatedVia: "test",
			}); err != nil {
				t.Fatalf("CreateRule connector deny: %v", err)
			}
			thread, err := st.CreateThread(ctx, nil)
			if err != nil {
				t.Fatalf("CreateThread: %v", err)
			}
			run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
			if err != nil {
				t.Fatalf("CreateRun: %v", err)
			}
			manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
			applied, allowed, _, out := manager.requireToolApproval(
				ctx,
				thread,
				run,
				types.ToolCall{ID: "tc_read_" + tc.connector, Name: tc.toolName},
				tc.toolName,
				json.RawMessage(`{}`),
				"",
				nil,
			)
			if !applied {
				t.Fatal("approvalApplied=false, want true for explicit datasource tool allow")
			}
			if !allowed {
				t.Fatalf("allowed=false output=%#v", out.Result)
			}
		})
	}
}

func TestRequireToolApprovalBlocksDeniedThreadSearchExecuteConnector(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryExecute,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "thread-search"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule connector deny: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_rlm", Name: "rlm_exec"},
		"rlm_exec",
		json.RawMessage(`{"code":"return []"}`),
		"",
		nil,
	)
	if applied {
		t.Fatal("approvalApplied=true for denied connector execute rule, want false")
	}
	if allowed {
		t.Fatal("allowed=true for denied rlm_exec connector rule, want false")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("approval output result=%T, want object", out.Result)
	}
	if payload["status"] != "approval_denied" {
		t.Fatalf("status=%#v, want approval_denied", payload["status"])
	}
}

func TestRequireToolApprovalThreadSearchExecuteToolRuleOverridesConnectorRule(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("store.NewJSON: %v", err)
	}
	permissionSvc, err := permissions.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("permissions.NewService: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category: permissions.CategoryExecute,
		Effect:   permissions.EffectAllow,
		Scope:    "global",
		Subject: store.PermissionSubject{
			Kind:      "tool",
			Tool:      "rlm_exec",
			Connector: "thread-search",
		},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule tool allow: %v", err)
	}
	if _, err := permissionSvc.CreateRule(ctx, permissions.PermissionRule{
		Category:   permissions.CategoryExecute,
		Effect:     permissions.EffectDeny,
		Scope:      "global",
		Subject:    store.PermissionSubject{Kind: "connector", Connector: "thread-search"},
		CreatedVia: "test",
	}); err != nil {
		t.Fatalf("CreateRule connector deny: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "system", 4)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	manager := &Manager{permissions: permissionSvc, cfgDir: cfgDir}
	applied, allowed, _, out := manager.requireToolApproval(
		ctx,
		thread,
		run,
		types.ToolCall{ID: "tc_rlm", Name: "rlm_exec"},
		"rlm_exec",
		json.RawMessage(`{"code":"return []"}`),
		"",
		nil,
	)
	if !applied {
		t.Fatal("approvalApplied=false, want true for explicit rlm_exec allow")
	}
	if !allowed {
		t.Fatalf("allowed=false output=%#v", out.Result)
	}
}

func permissionMetaTestArgs(toolName string) json.RawMessage {
	args := map[string]any{}
	switch toolName {
	case "file_search":
		args["local_root"] = filepath.Join(os.TempDir(), "codex-local-search-test")
		args["query"] = "needle"
	case "local_list_folder":
		args["local_root"] = filepath.Join(os.TempDir(), "codex-local-list-test")
	case "local_list_grant_folder":
		args["grant_id"] = "perm_local"
		args["relative_path"] = "reports"
	case tools.LocalSearchCommandToolName:
		args["grant_id"] = "perm_local"
		args["program"] = "auto"
		args["query"] = "needle"
	case "local_search_in_grant":
		args["grant_id"] = "perm_local"
		args["query"] = "needle"
	case "local_read_file":
		args["local_root"] = filepath.Join(os.TempDir(), "codex-local-read-test")
		args["path"] = "README.md"
	case "local_open_file":
		args["local_root"] = filepath.Join(os.TempDir(), "codex-local-open-test")
		args["path"] = "README.md"
	case "local_read_grant_file":
		args["grant_id"] = "perm_local"
		args["file_ref"] = "reports/summary.txt"
	case "rlm_exec":
		args["code"] = "return []"
	case "cancel_query":
		args["job_id"] = "job_test"
		args["owner_token"] = "tok_test"
	case tools.BrowserControlToolNavigate:
		args["url"] = "http://127.0.0.1:5173/"
	case tools.BrowserControlToolType:
		args["value"] = "hello"
	case tools.BrowserControlToolPress:
		args["key"] = "Enter"
	case tools.GitHubToolCloneRepository, tools.GitHubToolPullRepository, tools.GitHubToolRefreshRepository:
		args["repository"] = "owner/repo"
	case tools.GitHubToolRunGHCommand:
		args["args"] = []string{"issue", "edit", "1", "--title", "Updated", "--repo", "owner/repo"}
	case tools.SlackToolPostMessage:
		args["channel_id"] = "C123"
		args["text"] = "hello"
	case tools.SlackToolUploadFile:
		args["channel_id"] = "C123"
		args["fileName"] = "note.txt"
		args["contentText"] = "hello"
	case "jira_add_comment":
		args["issueKey"] = "PROJ-1"
		args["comment"] = "hello"
	case "jira_reply_to_comment":
		args["issueKey"] = "PROJ-1"
		args["commentId"] = "10000"
		args["comment"] = "hello"
	case "jira_add_related_tickets":
		args["issueKey"] = "PROJ-1"
		args["relatedIssueKeys"] = []string{"PROJ-2"}
	case "jira_upload_attachment":
		args["issueKey"] = "PROJ-1"
		args["fileName"] = "note.txt"
		args["contentText"] = "hello"
	case "jira_update_issue":
		args["issueKey"] = "PROJ-1"
		args["summary"] = "Updated summary"
	case "jira_create_issue":
		args["projectKey"] = "PROJ"
		args["summary"] = "New issue"
	}
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return b
}

func mustJSONForPermissionTest(value any) json.RawMessage {
	b, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return b
}

func requireLocalComputerScopeRequiredForPermissionTest(t *testing.T, out tools.Output) map[string]any {
	t.Helper()
	if out.OK {
		t.Fatalf("out.OK=true, want Local Computer scope failure: %#v", out.Result)
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("out.Result=%T, want object", out.Result)
	}
	if payload["code"] != "LOCAL_COMPUTER_SCOPE_REQUIRED" || payload["status"] != "local_computer_scope_required" {
		t.Fatalf("payload=%#v, want LOCAL_COMPUTER_SCOPE_REQUIRED", payload)
	}
	if !strings.Contains(fmt.Sprint(payload["message"]), "Settings > Connections > Local Computer") {
		t.Fatalf("payload message=%#v, want Local Computer settings guidance", payload["message"])
	}
	if out.ResultMetadata["kind"] != "local_computer_scope_required" {
		t.Fatalf("ResultMetadata=%#v, want local_computer_scope_required", out.ResultMetadata)
	}
	return payload
}

func requireNoPendingApprovalsForPermissionTest(t *testing.T, ctx context.Context, permissionSvc *permissions.Service, threadID string, runID string) {
	t.Helper()
	pending, err := permissionSvc.ListApprovals(ctx, permissions.ApprovalFilter{ThreadID: threadID, RunID: runID, Status: "pending"})
	if err != nil {
		t.Fatalf("ListApprovals: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending approvals=%d, want none: %#v", len(pending), pending)
	}
}

func insertReadFileArtifactForPermissionTest(t *testing.T, ctx context.Context, st store.Store, threadID string, artifactID string, sourcePath string) {
	t.Helper()
	toolName := "read_file"
	content := fmt.Sprintf(`{"path":%q,"content":"region,revenue\nEMEA,10\n","has_more":true}`, sourcePath)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          artifactID,
		ThreadID:    threadID,
		Type:        "code",
		Title:       filepath.Base(sourcePath),
		ToolName:    &toolName,
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		Metadata: map[string]any{
			"tool_name": "read_file",
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
}

func TestBuildArtifactCarriesToolErrorDiagnosticMetadata(t *testing.T) {
	out := enrichToolErrorOutput("github_list_repositories", types.ToolCall{
		ID:       "tc_error",
		Name:     "github_list_repositories",
		ArgsText: `{"owner":"missing"}`,
	}, tools.JSONError("404 repository not found"))

	_, _, _, _, metadata := buildArtifact("github_list_repositories", out, `{"error":"404 repository not found"}`)
	resultMetadata, ok := metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact result_metadata missing: %#v", metadata["result_metadata"])
	}
	diagnostic, ok := resultMetadata["diagnostic"].(map[string]any)
	if !ok {
		t.Fatalf("artifact diagnostic missing: %#v", resultMetadata["diagnostic"])
	}
	if strings.TrimSpace(fmt.Sprint(diagnostic["why"])) == "" {
		t.Fatalf("artifact diagnostic why missing: %#v", diagnostic)
	}
	if strings.TrimSpace(fmt.Sprint(diagnostic["reported"])) == "" {
		t.Fatalf("artifact diagnostic reported missing: %#v", diagnostic)
	}
	if required, _ := resultMetadata["model_commentary_required"].(bool); !required {
		t.Fatalf("model_commentary_required=%#v, want true", resultMetadata["model_commentary_required"])
	}
}

func TestManagerExecTool_SearchModeAllowsSearchTool(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	searchSvc, err := search.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("new search service: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store: st,
		tools: tools.NewRegistry(&tools.RegistryOptions{
			Search: searchSvc,
			Store:  st,
		}),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:       "tc_allowed",
		Name:     "search_docs",
		ArgsText: `{"q":"hello"}`,
	}, true, "deep", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content, got nil")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	if env["tool_name"] != "search_docs" {
		t.Fatalf("tool_name = %#v, want search_docs", env["tool_name"])
	}
	okVal, ok := env["ok"].(bool)
	if !ok || !okVal {
		t.Fatalf("ok = %#v, want true", env["ok"])
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	if _, ok := res["results"]; !ok {
		t.Fatalf("search result missing `results`: %#v", res)
	}
}

func TestManagerExecTool_DatabricksToolBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store:  st,
		tools:  tools.NewRegistry(nil),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_blocked_db",
		Name: "run_databricks_query",
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content, got nil")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	okVal, ok := env["ok"].(bool)
	if !ok || okVal {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	errMsg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(errMsg, "tool is disabled for this run") {
		t.Fatalf("error = %q, want disabled-tool message", errMsg)
	}
}

func TestManagerExecTool_BigQueryToolBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store:  st,
		tools:  tools.NewRegistry(nil),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_blocked_bq",
		Name: "run_bigquery_query",
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	okVal, ok := env["ok"].(bool)
	if !ok || okVal {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	errMsg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(errMsg, "tool is disabled for this run") {
		t.Fatalf("error = %q, want disabled-tool message", errMsg)
	}
}

func TestManagerExecTool_TrinoToolBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store:  st,
		tools:  tools.NewRegistry(nil),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_blocked_trino",
		Name: "run_trino_query",
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	okVal, ok := env["ok"].(bool)
	if !ok || okVal {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	errMsg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(errMsg, "tool is disabled for this run") {
		t.Fatalf("error = %q, want disabled-tool message", errMsg)
	}
}

// Regression: the arXiv gate in execTool must honor the run-scoped
// enabledArxiv parameter, not global settings. With arXiv globally enabled
// but the run's connection scope excluding it, the call must be blocked.
func TestManagerExecTool_ArxivToolBlockedWhenRunScopeExcludesArxiv(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}

	settingsStore, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	arxivEnabled := true
	globalSettings := settingsStore.Get()
	globalSettings.Arxiv = &config.ArxivSettings{Enabled: &arxivEnabled}
	if err := settingsStore.Save(globalSettings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{
		store:    st,
		tools:    tools.NewRegistry(nil),
		settings: settingsStore,
		cfgDir:   cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_blocked_arxiv_scope",
		Name: "arxiv_search",
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	okVal, ok := env["ok"].(bool)
	if !ok || okVal {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	errMsg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(errMsg, "tool is disabled for this run") {
		t.Fatalf("error = %q, want disabled-tool message", errMsg)
	}
}

// Regression: generate_image remains registered for stable metadata, so a
// provider can technically emit a stray call even when the tool definition was
// omitted. The run-scoped connection gate must block that call before the
// billable image tool executes.
func TestManagerExecTool_ImageGenerationBlockedWhenRunScopeExcludesConnection(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}

	settingsStore, err := config.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("config.NewStore: %v", err)
	}
	imageEnabled := true
	globalSettings := settingsStore.Get()
	globalSettings.ImageGeneration = &config.ImageGenerationSettings{Enabled: &imageEnabled}
	if err := settingsStore.Save(globalSettings); err != nil {
		t.Fatalf("settings Save: %v", err)
	}

	manager := &Manager{
		store: st,
		tools: tools.NewRegistry(&tools.RegistryOptions{
			Settings: settingsStore,
		}),
		settings: settingsStore,
		cfgDir:   cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:       "tc_blocked_image_scope",
		Name:     "generate_image",
		ArgsText: `{"prompt":"A lighthouse at dusk"}`,
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	errMsg, _ := res["error"].(string)
	if !strings.Contains(errMsg, "tool is disabled for this run") {
		t.Fatalf("error = %q, want disabled-tool message", errMsg)
	}
}

func TestManagerExecTool_SnowflakeToolBlockedWhenDisabled(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run := store.Run{ID: types.NewUUID()}
	manager := &Manager{
		store:  st,
		tools:  tools.NewRegistry(nil),
		cfgDir: cfgDir,
	}

	out, err := manager.execTool(ctx, thread, run, testProjectID(""), types.ToolCall{
		ID:   "tc_blocked_snowflake",
		Name: "run_snowflake_query",
	}, false, "shallow", 0, nil, nil, defaultChartEngineEnablement(), false, false, true, true, false, atlassianEnablement{}, false, false, slackToolAccess{})
	if err != nil {
		t.Fatalf("execTool: %v", err)
	}
	if out.Content == nil {
		t.Fatalf("expected tool envelope content")
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(*out.Content), &env); err != nil {
		t.Fatalf("invalid tool envelope JSON: %v", err)
	}
	okVal, ok := env["ok"].(bool)
	if !ok || okVal {
		t.Fatalf("ok = %#v, want false", env["ok"])
	}
	res, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", env["result"])
	}
	errMsg, ok := res["error"].(string)
	if !ok {
		t.Fatalf("expected error message, got %#v", res["error"])
	}
	if !strings.Contains(errMsg, "tool is disabled for this run") {
		t.Fatalf("error = %q, want disabled-tool message", errMsg)
	}
}
