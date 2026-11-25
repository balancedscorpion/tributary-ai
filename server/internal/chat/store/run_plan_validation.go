package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/types"
)

func validateRunPlan(plan RunPlan) error {
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.ThreadID) == "" {
		return fmt.Errorf("%w: planId and threadId are required", ErrInvalid)
	}
	if containsPathSeparator(plan.ID) {
		return fmt.Errorf("%w: planId contains a path separator", ErrInvalid)
	}
	if strings.TrimSpace(plan.Target.ConnectionID) == "" || strings.TrimSpace(plan.Target.Model) == "" {
		return fmt.Errorf("%w: plan target is required", ErrInvalid)
	}
	if strings.TrimSpace(plan.RequirementsHash) == "" {
		return fmt.Errorf("%w: requirementsHash is required", ErrInvalid)
	}
	if plan.Status != RunPlanStatusDraft && plan.Status != RunPlanStatusFinalized {
		return fmt.Errorf("%w: invalid plan status", ErrInvalid)
	}
	if plan.CreatedAt.IsZero() || plan.ExpiresAt.IsZero() || !plan.ExpiresAt.After(plan.CreatedAt) {
		return fmt.Errorf("%w: invalid plan lifetime", ErrInvalid)
	}
	switch plan.Status {
	case RunPlanStatusDraft:
		if plan.RunID != nil || plan.FinalizedAt != nil || len(plan.Selections) != 0 {
			return fmt.Errorf("%w: draft plan contains finalized fields", ErrInvalid)
		}
	case RunPlanStatusFinalized:
		if plan.RunID == nil || strings.TrimSpace(*plan.RunID) == "" || plan.FinalizedAt == nil ||
			plan.FinalizedAt.Before(plan.CreatedAt) || !plan.FinalizedAt.Before(plan.ExpiresAt) {
			return fmt.Errorf("%w: finalized plan fields or timestamp are invalid", ErrInvalid)
		}
	}
	if len(plan.CapabilityReport) == 0 || !json.Valid(plan.CapabilityReport) {
		return fmt.Errorf("%w: plan capability report must be valid JSON", ErrInvalid)
	}
	issueIDs := map[string]struct{}{}
	for _, issue := range append(append([]PlanIssue(nil), plan.Blockers...), plan.Notices...) {
		if err := validatePlanIssue(issue, issueIDs); err != nil {
			return err
		}
	}
	for _, choice := range plan.Choices {
		if err := validatePlanIssue(choice.PlanIssue, issueIDs); err != nil {
			return err
		}
		if len(choice.Options) == 0 || len(choice.AllowedScopes) == 0 {
			return fmt.Errorf("%w: choice %s has no options or scopes", ErrInvalid, choice.IssueID)
		}
	}
	return nil
}

// validateNewRunPlan keeps CreateRunPlan's contract unambiguous across the
// JSON and MySQL stores. Finalized plans are produced only by the atomic
// FinalizeRunPlanAndCreateRun operation; accepting one here would make the
// MySQL implementation silently discard its run/finalization fields.
func validateNewRunPlan(plan RunPlan) error {
	if err := validateRunPlan(plan); err != nil {
		return err
	}
	if plan.Status != RunPlanStatusDraft || plan.RunID != nil || plan.FinalizedAt != nil || len(plan.Selections) != 0 {
		return fmt.Errorf("%w: new run plans must be unfinalized drafts", ErrInvalid)
	}
	return nil
}

func validatePlanIssue(issue PlanIssue, seen map[string]struct{}) error {
	id := strings.TrimSpace(issue.IssueID)
	if id == "" || strings.TrimSpace(issue.Code) == "" {
		return fmt.Errorf("%w: plan issues require issueId and code", ErrInvalid)
	}
	if _, ok := seen[id]; ok {
		return fmt.Errorf("%w: duplicate plan issueId %s", ErrInvalid, id)
	}
	seen[id] = struct{}{}
	return nil
}

func validatePlanFinalization(plan RunPlan, req FinalizeRunPlanRequest) ([]PlanChoiceSelection, []AttachmentPolicy, error) {
	now := req.Now.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if err := validatePlanRequestIdentity(plan, req); err != nil {
		return nil, nil, ErrPlanStale
	}
	if plan.Status != RunPlanStatusDraft || now.Before(plan.CreatedAt) || !now.Before(plan.ExpiresAt) || len(plan.Blockers) > 0 || plan.ResultStatus == "blocked" {
		return nil, nil, ErrPlanStale
	}

	choiceByID := make(map[string]PlanChoice, len(plan.Choices))
	for _, choice := range plan.Choices {
		choiceByID[choice.IssueID] = choice
	}
	selectionByID := make(map[string]PlanChoiceSelection, len(req.Selections))
	policies := make([]AttachmentPolicy, 0)
	for _, selection := range req.Selections {
		selection.IssueID = strings.TrimSpace(selection.IssueID)
		selection.OptionID = strings.TrimSpace(selection.OptionID)
		selection.Scope = strings.ToLower(strings.TrimSpace(selection.Scope))
		choice, ok := choiceByID[selection.IssueID]
		if !ok || selection.OptionID == "" || selection.Scope == "" {
			return nil, nil, ErrPlanStale
		}
		if _, duplicate := selectionByID[selection.IssueID]; duplicate {
			return nil, nil, ErrPlanStale
		}
		if !containsString(choice.AllowedScopes, selection.Scope) {
			return nil, nil, ErrPlanStale
		}
		var selectedOption *PlanChoiceOption
		for i := range choice.Options {
			if strings.TrimSpace(choice.Options[i].ID) == selection.OptionID {
				selectedOption = &choice.Options[i]
				break
			}
		}
		if selectedOption == nil {
			return nil, nil, ErrPlanStale
		}
		selection.SelectedAt = now
		selectionByID[selection.IssueID] = selection
		if selection.Scope == "thread" && selectedOption.PolicyClass != nil && strings.TrimSpace(*selectedOption.PolicyClass) != "" {
			policies = append(policies, AttachmentPolicy{
				ID:         types.NewUUID(),
				Class:      strings.TrimSpace(*selectedOption.PolicyClass),
				Action:     selection.OptionID,
				APISurface: strings.TrimSpace(selectedOption.APISurface),
				CreatedAt:  now.UnixMilli(),
			})
		}
	}
	if len(selectionByID) != len(choiceByID) {
		return nil, nil, ErrPlanStale
	}
	selections := make([]PlanChoiceSelection, 0, len(selectionByID))
	for _, selection := range selectionByID {
		selections = append(selections, selection)
	}
	sort.Slice(selections, func(i, j int) bool { return selections[i].IssueID < selections[j].IssueID })
	return selections, policies, nil
}

// validateFinalizedPlanReplay permits an idempotent retry only when it is the
// same finalization request. Matching selections alone is insufficient: a
// stale caller must not be able to replay a plan against another thread,
// target, requirements set, or provider configuration.
func validateFinalizedPlanReplay(plan RunPlan, req FinalizeRunPlanRequest) error {
	if plan.Status != RunPlanStatusFinalized || plan.RunID == nil || strings.TrimSpace(*plan.RunID) == "" {
		return ErrPlanStale
	}
	if err := validatePlanRequestIdentity(plan, req); err != nil || !sameSelections(plan.Selections, req.Selections) {
		return ErrPlanStale
	}
	return nil
}

func validatePlanRequestIdentity(plan RunPlan, req FinalizeRunPlanRequest) error {
	if strings.TrimSpace(plan.ID) != strings.TrimSpace(req.PlanID) ||
		strings.TrimSpace(plan.ThreadID) != strings.TrimSpace(req.ExpectedThreadID) ||
		!sameTarget(plan.Target, req.ExpectedTarget) ||
		strings.TrimSpace(plan.RequirementsHash) != strings.TrimSpace(req.ExpectedRequirementsHash) ||
		strings.TrimSpace(plan.ConfigFingerprint) != strings.TrimSpace(req.ExpectedConfigFingerprint) ||
		strings.TrimSpace(req.Run.ThreadID) != strings.TrimSpace(plan.ThreadID) ||
		strings.TrimSpace(req.Run.ConnectionID) != strings.TrimSpace(plan.Target.ConnectionID) ||
		strings.TrimSpace(req.Run.Model) != strings.TrimSpace(plan.Target.Model) {
		return ErrPlanStale
	}
	return nil
}

func sameTarget(a, b RunTarget) bool {
	return strings.TrimSpace(a.ConnectionID) == strings.TrimSpace(b.ConnectionID) &&
		strings.TrimSpace(a.Model) == strings.TrimSpace(b.Model)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(wanted)) {
			return true
		}
	}
	return false
}

func sameSelections(a, b []PlanChoiceSelection) bool {
	if len(a) != len(b) {
		return false
	}
	normalize := func(in []PlanChoiceSelection) []string {
		out := make([]string, 0, len(in))
		for _, item := range in {
			out = append(out, strings.TrimSpace(item.IssueID)+"\x00"+strings.TrimSpace(item.OptionID)+"\x00"+strings.ToLower(strings.TrimSpace(item.Scope)))
		}
		sort.Strings(out)
		return out
	}
	aa, bb := normalize(a), normalize(b)
	for i := range aa {
		if aa[i] != bb[i] {
			return false
		}
	}
	return true
}

func stalePlanError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrPlanStale) {
		return ErrPlanStale
	}
	return err
}
