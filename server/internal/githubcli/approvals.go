package githubcli

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	operationCloneRepository   = "github_clone_repository_execute"
	operationRefreshRepository = "github_refresh_repository_execute"
	operationPullRepository    = "github_pull_repository_execute"
	operationRawGHCommand      = "github_raw_gh_command_execute"
)

const (
	OperationCloneRepository   = operationCloneRepository
	OperationRefreshRepository = operationRefreshRepository
	OperationPullRepository    = operationPullRepository
	OperationRawGHCommand      = operationRawGHCommand
)

const (
	approvalDecisionApproved = "approved"
	approvalDecisionRejected = "rejected"
)

type approvalResolution struct {
	Status   string
	Approval PendingApproval
	Result   map[string]any
	Error    string
	Message  string
}

func (m *Manager) QueueApproval(input PendingApproval, operation string, args map[string]any) PendingApproval {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := strings.TrimSpace(input.ID)
	if id == "" {
		id = fmt.Sprintf("gh-%d", time.Now().UnixNano())
	}
	createdAt := strings.TrimSpace(input.CreatedAt)
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	input.ID = id
	input.CreatedAt = createdAt
	m.approvals[id] = approvalAction{
		PendingApproval: cloneApproval(input),
		Operation:       strings.TrimSpace(operation),
		Args:            cloneMap(args),
		Decision:        make(chan approvalDecision, 1),
	}
	return input
}

func (m *Manager) ListApprovals() []PendingApproval {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]PendingApproval, 0, len(m.approvals))
	for _, approval := range m.approvals {
		out = append(out, cloneApproval(approval.PendingApproval))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (m *Manager) Approve(ctx context.Context, id string) (map[string]any, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	action, ok := m.approvals[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("unknown approval id: %s", id)
	}
	if action.Waiting && action.Decision != nil {
		delete(m.approvals, id)
		m.mu.Unlock()
		signalApprovalDecision(action, approvalDecision{Action: approvalDecisionApproved})
		return approvalResolution{
			Status:   approvalDecisionApproved,
			Approval: cloneApproval(action.PendingApproval),
			Message:  "Approval accepted. The active chat run is continuing the operation.",
		}.Map(), nil
	}
	m.mu.Unlock()
	result, err := m.executeApprovalOperation(ctx, action)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	delete(m.approvals, id)
	m.mu.Unlock()
	return approvalResolution{
		Status:   approvalDecisionApproved,
		Approval: cloneApproval(action.PendingApproval),
		Result:   result,
	}.Map(), nil
}

func (m *Manager) Reject(id string) (PendingApproval, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	action, ok := m.approvals[id]
	if !ok {
		m.mu.Unlock()
		return PendingApproval{}, fmt.Errorf("unknown approval id: %s", id)
	}
	delete(m.approvals, id)
	m.mu.Unlock()
	signalApprovalDecision(action, approvalDecision{Action: approvalDecisionRejected})
	return cloneApproval(action.PendingApproval), nil
}

func (m *Manager) AwaitApproval(ctx context.Context, id string, timeout time.Duration) (map[string]any, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("approval id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	action, ok := m.approvals[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("unknown approval id: %s", id)
	}
	if action.Decision == nil {
		action.Decision = make(chan approvalDecision, 1)
	}
	action.Waiting = true
	m.approvals[id] = action
	m.mu.Unlock()

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}

	select {
	case decision := <-action.Decision:
		if strings.TrimSpace(decision.Action) == approvalDecisionRejected {
			return approvalResolution{
				Status:   approvalDecisionRejected,
				Approval: cloneApproval(action.PendingApproval),
				Message:  "Approval was rejected. The requested operation was not run.",
			}.Map(), nil
		}
		result, err := m.executeApprovalOperation(ctx, action)
		if err != nil {
			return approvalResolution{
				Status:   "error",
				Approval: cloneApproval(action.PendingApproval),
				Error:    err.Error(),
				Message:  "Approval was accepted, but the requested operation failed.",
			}.Map(), nil
		}
		return approvalResolution{
			Status:   approvalDecisionApproved,
			Approval: cloneApproval(action.PendingApproval),
			Result:   result,
		}.Map(), nil
	case <-timer:
		m.clearApprovalWaiter(id, action.Decision)
		return approvalResolution{
			Status:   "pending",
			Approval: cloneApproval(action.PendingApproval),
			Message:  "Approval is still pending. The requested operation has not run yet.",
		}.Map(), nil
	case <-ctx.Done():
		m.clearApprovalWaiter(id, action.Decision)
		return nil, ctx.Err()
	}
}

func (m *Manager) executeApprovalOperation(ctx context.Context, action approvalAction) (map[string]any, error) {
	switch strings.TrimSpace(action.Operation) {
	case operationCloneRepository:
		return m.executeCloneRepository(ctx, action.Args)
	case operationRefreshRepository, operationPullRepository:
		return m.executeRefreshRepositories(ctx, action.Args)
	case operationRawGHCommand:
		return m.executeRawGHCommand(ctx, action.Args)
	default:
		return nil, fmt.Errorf("unknown GitHub approval operation: %s", action.Operation)
	}
}

func (m *Manager) RunApprovedOperation(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
	return m.executeApprovalOperation(ctx, approvalAction{
		Operation: strings.TrimSpace(operation),
		Args:      cloneMap(args),
	})
}

func (m *Manager) clearApprovalWaiter(id string, decision chan approvalDecision) {
	m.mu.Lock()
	defer m.mu.Unlock()
	action, ok := m.approvals[id]
	if !ok || action.Decision != decision {
		return
	}
	action.Waiting = false
	m.approvals[id] = action
}

func signalApprovalDecision(action approvalAction, decision approvalDecision) {
	if action.Decision == nil {
		return
	}
	select {
	case action.Decision <- decision:
	default:
	}
}

func (r approvalResolution) Map() map[string]any {
	status := strings.TrimSpace(r.Status)
	if status == "" {
		status = "pending"
	}
	out := map[string]any{
		"approval":        cloneApproval(r.Approval),
		"status":          status,
		"approval_status": status,
	}
	if len(r.Result) > 0 {
		out["result"] = cloneMap(r.Result)
	}
	if strings.TrimSpace(r.Error) != "" {
		out["error"] = strings.TrimSpace(r.Error)
	}
	if strings.TrimSpace(r.Message) != "" {
		out["message"] = strings.TrimSpace(r.Message)
	}
	return out
}

func cloneApproval(input PendingApproval) PendingApproval {
	input.Target = cloneMap(input.Target)
	input.Content = cloneMap(input.Content)
	return input
}

func cloneMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = cloneAny(value)
	}
	return out
}

func cloneAny(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return cloneMap(v)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = cloneAny(item)
		}
		return out
	case []string:
		return append([]string(nil), v...)
	default:
		return value
	}
}
