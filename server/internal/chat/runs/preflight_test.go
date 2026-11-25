package runs_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/runs"
	"ai-analyst-local/internal/chat/store"
)

type fixedResolver struct{}

func (fixedResolver) ResolveRunRequirements(context.Context, runs.ResolveRequest) (runs.Resolution, error) {
	policyClass := "office_docs_to_text"
	attachmentA := "artifact-a"
	attachmentB := "artifact-b"
	return runs.Resolution{
		Choices: []store.PlanChoice{
			{
				PlanIssue:     store.PlanIssue{Code: "fidelity_reduction", Text: "Extract the first file", AttachmentID: &attachmentA},
				Options:       []store.PlanChoiceOption{{ID: "extract_a", Label: "Extract", Consequence: "Formatting may change", PolicyClass: &policyClass, APISurface: "messages"}},
				AllowedScopes: []string{"run", "thread"},
			},
			{
				PlanIssue:     store.PlanIssue{Code: "fidelity_reduction", Text: "Extract the second file", AttachmentID: &attachmentB},
				Options:       []store.PlanChoiceOption{{ID: "extract_b", Label: "Extract", Consequence: "Formatting may change", PolicyClass: &policyClass, APISurface: "messages"}},
				AllowedScopes: []string{"run", "thread"},
			},
		},
		CapabilityReport:            json.RawMessage(`{"target":{"apiSurface":"messages"},"features":{}}`),
		ConfigFingerprint:           "sha256:config-a",
		ConnectionPolicyFingerprint: "connection-policy-v1:test",
		ThreadPolicyFingerprint:     "thread-policy-v1:test",
	}, nil
}

func TestPreflightPersistsStableIssueIDsAndFinalizesIdempotently(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := store.RunTarget{ConnectionID: "anthropic-work", Model: "claude-sonnet"}
	requirements := runs.Requirements{
		Features: []string{"documents", "custom_tools", "documents"},
		Attachments: []runs.AttachmentRequirement{
			{ID: "artifact-b", MIME: "application/pdf", SizeBytes: 200},
			{ID: "artifact-a", MIME: "application/pdf", SizeBytes: 100},
		},
	}
	service := runs.NewService(st, fixedResolver{})
	plan, err := service.Preflight(ctx, runs.PreflightRequest{
		ThreadID: thread.ID, Target: target, Requirements: requirements,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.ResultStatus != "choices_required" || len(plan.Choices) != 2 {
		t.Fatalf("plan status/choices = %q/%d", plan.ResultStatus, len(plan.Choices))
	}
	if connection, thread := runs.PlanPolicyFingerprints(plan); connection != "connection-policy-v1:test" || thread != "thread-policy-v1:test" {
		t.Fatalf("plan policy fingerprints=%q/%q", connection, thread)
	}
	if plan.Choices[0].IssueID == "" || plan.Choices[1].IssueID == "" || plan.Choices[0].IssueID == plan.Choices[1].IssueID {
		t.Fatalf("issue IDs must be stable and unique: %#v", plan.Choices)
	}
	persisted, err := st.GetRunPlan(ctx, plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Choices[0].IssueID != plan.Choices[0].IssueID || persisted.Choices[1].IssueID != plan.Choices[1].IssueID {
		t.Fatalf("persisted issue IDs changed: got %#v want %#v", persisted.Choices, plan.Choices)
	}

	selections := []store.PlanChoiceSelection{
		{IssueID: plan.Choices[0].IssueID, OptionID: "extract_a", Scope: "thread"},
		{IssueID: plan.Choices[1].IssueID, OptionID: "extract_b", Scope: "thread"},
	}
	finalize := store.FinalizeRunPlanRequest{
		PlanID: plan.ID, ExpectedThreadID: thread.ID, ExpectedTarget: target,
		ExpectedRequirementsHash: plan.RequirementsHash, ExpectedConfigFingerprint: "sha256:config-a",
		Selections: selections,
		Run: store.CreateRunParams{
			ThreadID: thread.ID, ProviderID: "anthropic", ConnectionID: target.ConnectionID,
			Model: target.Model, SystemPrompt: "You are helpful.", MaxIterations: 20,
		},
		Now: time.Now().UTC(),
	}
	run, finalized, err := st.FinalizeRunPlanAndCreateRun(ctx, finalize)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.Status != store.RunPlanStatusFinalized || finalized.RunID == nil || *finalized.RunID != run.ID {
		t.Fatalf("finalized plan=%#v run=%#v", finalized, run)
	}
	gotThread, err := st.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotThread.LastTarget == nil || *gotThread.LastTarget != target {
		t.Fatalf("last target=%#v, want %#v", gotThread.LastTarget, target)
	}
	if len(gotThread.AttachmentPolicies) != 2 {
		t.Fatalf("thread policies=%#v, want two explicit choices", gotThread.AttachmentPolicies)
	}

	retriedRun, _, err := st.FinalizeRunPlanAndCreateRun(ctx, finalize)
	if err != nil || retriedRun.ID != run.ID {
		t.Fatalf("idempotent retry run=%#v err=%v", retriedRun, err)
	}
	divergent := finalize
	divergent.Selections = append([]store.PlanChoiceSelection(nil), selections...)
	divergent.Selections[0].OptionID = "different"
	if _, _, err := st.FinalizeRunPlanAndCreateRun(ctx, divergent); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("divergent finalized retry err=%v, want ErrPlanStale", err)
	}
	byRun, err := st.GetRunPlanForRun(ctx, run.ID)
	if err != nil || byRun.ID != plan.ID {
		t.Fatalf("GetRunPlanForRun=%#v err=%v", byRun, err)
	}
}

func TestExpiredOrDivergentPlanNeverCreatesRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := st.CreateThread(ctx, nil)
	now := time.Now().UTC()
	plan := store.RunPlan{
		ID: "expired-plan", ThreadID: thread.ID,
		Target:            store.RunTarget{ConnectionID: "conn", Model: "model"},
		ConfigFingerprint: "fingerprint", RequirementsHash: runs.RequirementsHash(runs.Requirements{}),
		ResultStatus: "clean", Status: store.RunPlanStatusDraft,
		CapabilityReport: json.RawMessage(`{"features":{}}`),
		CreatedAt:        now.Add(-20 * time.Minute), ExpiresAt: now.Add(-5 * time.Minute),
	}
	if _, err := st.CreateRunPlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	_, _, err = st.FinalizeRunPlanAndCreateRun(ctx, store.FinalizeRunPlanRequest{
		PlanID: plan.ID, ExpectedThreadID: thread.ID, ExpectedTarget: plan.Target,
		ExpectedRequirementsHash: plan.RequirementsHash, ExpectedConfigFingerprint: plan.ConfigFingerprint,
		Run: store.CreateRunParams{ThreadID: thread.ID, ProviderID: "anthropic", ConnectionID: "conn", Model: "model", SystemPrompt: "prompt"},
		Now: now,
	})
	if !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("expired finalize err=%v, want ErrPlanStale", err)
	}
	deleted, err := st.DeleteExpiredRunPlans(ctx, now)
	if err != nil || deleted != 1 {
		t.Fatalf("DeleteExpiredRunPlans=%d err=%v", deleted, err)
	}
	if _, err := st.GetRunPlan(ctx, plan.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired plan still present: %v", err)
	}
}

func TestRequirementsHashIsCanonical(t *testing.T) {
	a := runs.Requirements{
		Features:    []string{"documents", "custom_tools", "documents"},
		Attachments: []runs.AttachmentRequirement{{ID: "b", MIME: "APPLICATION/PDF", SizeBytes: 2}, {ID: "a", MIME: "image/PNG", SizeBytes: 1}},
	}
	b := runs.Requirements{
		Features:    []string{"custom_tools", "documents"},
		Attachments: []runs.AttachmentRequirement{{ID: "a", MIME: "image/png", SizeBytes: 1}, {ID: "b", MIME: "application/pdf", SizeBytes: 2}},
	}
	if runs.RequirementsHash(a) != runs.RequirementsHash(b) {
		t.Fatalf("canonical requirements hashes differ: %s vs %s", runs.RequirementsHash(a), runs.RequirementsHash(b))
	}
}

func TestValidateSubmissionReturnsOnlyExplicitConnectionPolicies(t *testing.T) {
	policyClass := "office_docs_to_text"
	now := time.Now().UTC()
	target := store.RunTarget{ConnectionID: "conn", Model: "model"}
	plan := store.RunPlan{
		ID: "plan", ThreadID: "thread", Target: target, RequirementsHash: "hash", ConfigFingerprint: "fingerprint",
		Status: store.RunPlanStatusDraft, ResultStatus: "choices_required", ExpiresAt: now.Add(time.Minute),
		Choices: []store.PlanChoice{{
			PlanIssue:     store.PlanIssue{IssueID: "issue-1", Code: "fidelity_reduction"},
			Options:       []store.PlanChoiceOption{{ID: "extract", PolicyClass: &policyClass, APISurface: "converse_stream"}},
			AllowedScopes: []string{"run", "thread", "connection"},
		}},
	}
	policies, err := runs.ValidateSubmission(plan, target, "hash", "fingerprint", []store.PlanChoiceSelection{{
		IssueID: "issue-1", OptionID: "extract", Scope: "connection",
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(policies) != 1 || policies[0].Class != policyClass || policies[0].Action != "extract" || policies[0].APISurface != "converse_stream" {
		t.Fatalf("connection policies=%#v", policies)
	}
	if _, err := runs.ValidateSubmission(plan, target, "different", "fingerprint", []store.PlanChoiceSelection{{
		IssueID: "issue-1", OptionID: "extract", Scope: "connection",
	}}, now); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("divergent hash err=%v, want ErrPlanStale", err)
	}
}
