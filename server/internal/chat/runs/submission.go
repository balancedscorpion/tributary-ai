package runs

import (
	"strings"
	"time"

	"ai-analyst-local/internal/chat/store"
)

type ConnectionPolicyWrite struct {
	Class      string
	Action     string
	APISurface string
}

// ValidateSubmission performs the cross-store validation phase before any
// connection-scoped consent is written. The chat store repeats these checks
// under its transaction/lock when it finalizes the plan and creates the run.
func ValidateSubmission(plan store.RunPlan, target store.RunTarget, requirementsHash, fingerprint string, selections []store.PlanChoiceSelection, now time.Time) ([]ConnectionPolicyWrite, error) {
	target = NormalizeTarget(target)
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(plan.ThreadID) == "" ||
		plan.Target != target || strings.TrimSpace(plan.RequirementsHash) != strings.TrimSpace(requirementsHash) ||
		strings.TrimSpace(plan.ConfigFingerprint) != strings.TrimSpace(fingerprint) || len(plan.Blockers) > 0 || plan.ResultStatus == "blocked" {
		return nil, store.ErrPlanStale
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if plan.Status == store.RunPlanStatusDraft {
		if !now.Before(plan.ExpiresAt) {
			return nil, store.ErrPlanStale
		}
	} else if plan.Status == store.RunPlanStatusFinalized {
		if !equalSelections(plan.Selections, selections) {
			return nil, store.ErrPlanStale
		}
	} else {
		return nil, store.ErrPlanStale
	}

	choices := make(map[string]store.PlanChoice, len(plan.Choices))
	for _, choice := range plan.Choices {
		choices[strings.TrimSpace(choice.IssueID)] = choice
	}
	seen := map[string]struct{}{}
	policies := make([]ConnectionPolicyWrite, 0)
	for _, selection := range selections {
		issueID := strings.TrimSpace(selection.IssueID)
		optionID := strings.TrimSpace(selection.OptionID)
		scope := strings.ToLower(strings.TrimSpace(selection.Scope))
		choice, ok := choices[issueID]
		if !ok || optionID == "" || !containsNormalized(choice.AllowedScopes, scope) {
			return nil, store.ErrPlanStale
		}
		if _, duplicate := seen[issueID]; duplicate {
			return nil, store.ErrPlanStale
		}
		seen[issueID] = struct{}{}
		var option *store.PlanChoiceOption
		for i := range choice.Options {
			if strings.TrimSpace(choice.Options[i].ID) == optionID {
				option = &choice.Options[i]
				break
			}
		}
		if option == nil {
			return nil, store.ErrPlanStale
		}
		if scope == "connection" && option.PolicyClass != nil && strings.TrimSpace(*option.PolicyClass) != "" {
			policies = append(policies, ConnectionPolicyWrite{
				Class: strings.TrimSpace(*option.PolicyClass), Action: optionID, APISurface: strings.TrimSpace(option.APISurface),
			})
		}
	}
	if len(seen) != len(choices) {
		return nil, store.ErrPlanStale
	}
	return policies, nil
}

func containsNormalized(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), wanted) {
			return true
		}
	}
	return false
}

func equalSelections(a, b []store.PlanChoiceSelection) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]store.PlanChoiceSelection, len(a))
	for _, item := range a {
		index[strings.TrimSpace(item.IssueID)] = item
	}
	for _, item := range b {
		other, ok := index[strings.TrimSpace(item.IssueID)]
		if !ok || strings.TrimSpace(other.OptionID) != strings.TrimSpace(item.OptionID) ||
			!strings.EqualFold(strings.TrimSpace(other.Scope), strings.TrimSpace(item.Scope)) {
			return false
		}
	}
	return true
}
