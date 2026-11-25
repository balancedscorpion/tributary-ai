package permissions

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	CategoryRead    = "read"
	CategoryWrite   = "write"
	CategoryExecute = "execute"

	EffectAllow = "allow"
	EffectAsk   = "ask"
	EffectDeny  = "deny"

	DecisionAllow = "allow"
	DecisionAsk   = "ask"
	DecisionDeny  = "deny"
)

type PermissionRule struct {
	ID         string                  `json:"id"`
	Category   string                  `json:"category"`
	Effect     string                  `json:"effect"`
	Scope      string                  `json:"scope"`
	Subject    store.PermissionSubject `json:"subject"`
	CreatedVia string                  `json:"created_via"`
	CreatedAt  time.Time               `json:"created_at"`
	LastUsedAt *time.Time              `json:"last_used_at,omitempty"`
	ExpiresAt  *time.Time              `json:"expires_at,omitempty"`
}

type EvaluationInput struct {
	ThreadID string                  `json:"thread_id,omitempty"`
	Tool     string                  `json:"tool,omitempty"`
	Category string                  `json:"category"`
	Subject  store.PermissionSubject `json:"subject"`
}

type EvaluationResult struct {
	Decision string          `json:"decision"`
	Rule     *PermissionRule `json:"rule,omitempty"`
	Reason   string          `json:"reason,omitempty"`
}

type RequestInput struct {
	ThreadID   string                  `json:"thread_id"`
	RunID      string                  `json:"run_id"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
	Tool       string                  `json:"tool"`
	Category   string                  `json:"category"`
	Operation  string                  `json:"operation,omitempty"`
	Subject    store.PermissionSubject `json:"subject"`
	Summary    string                  `json:"summary"`
	Preview    map[string]any          `json:"preview,omitempty"`
	Options    []string                `json:"options,omitempty"`
	ExpiresAt  *time.Time              `json:"expires_at,omitempty"`
}

type ApproveOptions struct {
	Scope   string
	Subject *store.PermissionSubject
}

type LocalLocationTestResult struct {
	Subject  store.PermissionSubject `json:"subject"`
	Kind     string                  `json:"kind"`
	Readable bool                    `json:"readable"`
}

type RuleFilter struct {
	Category string
	Scope    string
}

type ApprovalFilter = store.ApprovalFilter

type AuditRecord struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	RuleID     string                  `json:"rule_id,omitempty"`
	ApprovalID string                  `json:"approval_id,omitempty"`
	ThreadID   string                  `json:"thread_id,omitempty"`
	RunID      string                  `json:"run_id,omitempty"`
	Category   string                  `json:"category,omitempty"`
	Effect     string                  `json:"effect,omitempty"`
	Scope      string                  `json:"scope,omitempty"`
	Subject    store.PermissionSubject `json:"subject,omitempty"`
	CreatedAt  time.Time               `json:"created_at"`
}

type Service struct {
	cfgDir string
	store  store.Store

	mu      sync.Mutex
	waiters map[string][]chan store.ApprovalRequest
}

type rulesEnvelope struct {
	Kind          string           `json:"kind"`
	SchemaVersion int              `json:"schema_version"`
	Rules         []PermissionRule `json:"rules"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

func NewService(cfgDir string, st store.Store) (*Service, error) {
	if strings.TrimSpace(cfgDir) == "" {
		return nil, errors.New("cfgDir is required")
	}
	if st == nil {
		return nil, errors.New("store is required")
	}
	s := &Service{cfgDir: cfgDir, store: st, waiters: map[string][]chan store.ApprovalRequest{}}
	if err := os.MkdirAll(s.permissionsDir(), 0o700); err != nil {
		return nil, err
	}
	if _, err := s.readRules(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) Evaluate(ctx context.Context, input EvaluationInput) (EvaluationResult, error) {
	if err := ctx.Err(); err != nil {
		return EvaluationResult{}, err
	}
	category := normalizeCategory(input.Category)
	subject := normalizeSubject(input.Subject)
	if subject.Tool == "" {
		subject.Tool = strings.TrimSpace(input.Tool)
	}
	if subject.Kind == "" {
		subject.Kind = "tool"
	}
	rules, err := s.readRules()
	if err != nil {
		return EvaluationResult{}, err
	}
	now := time.Now().UTC()
	threadScope := "thread:" + strings.TrimSpace(input.ThreadID)
	var threadMatch *PermissionRule
	var globalMatch *PermissionRule
	for i := range rules {
		rule := rules[i]
		if rule.ExpiresAt != nil && !rule.ExpiresAt.After(now) {
			continue
		}
		if normalizeCategory(rule.Category) != category {
			continue
		}
		if !subjectMatches(rule.Subject, subject) {
			continue
		}
		effect := normalizeEffect(rule.Effect)
		if effect == EffectDeny {
			_ = s.markRuleUsed(rule.ID, now)
			return EvaluationResult{Decision: DecisionDeny, Rule: &rule, Reason: "explicit deny rule"}, nil
		}
		switch strings.TrimSpace(rule.Scope) {
		case threadScope:
			if threadMatch == nil {
				r := rule
				threadMatch = &r
			}
		case "global":
			if globalMatch == nil {
				r := rule
				globalMatch = &r
			}
		}
	}
	if threadMatch != nil {
		_ = s.markRuleUsed(threadMatch.ID, now)
		return EvaluationResult{Decision: decisionForEffect(threadMatch.Effect), Rule: threadMatch}, nil
	}
	if globalMatch != nil {
		_ = s.markRuleUsed(globalMatch.ID, now)
		return EvaluationResult{Decision: decisionForEffect(globalMatch.Effect), Rule: globalMatch}, nil
	}
	return EvaluationResult{Decision: defaultDecision(category, subject), Reason: "default"}, nil
}

func (s *Service) Request(ctx context.Context, input RequestInput) (store.ApprovalRequest, error) {
	approval, _, err := s.RequestOrReuse(ctx, input)
	return approval, err
}

func (s *Service) RequestOrReuse(ctx context.Context, input RequestInput) (store.ApprovalRequest, bool, error) {
	if err := ctx.Err(); err != nil {
		return store.ApprovalRequest{}, false, err
	}
	expiresAt := time.Now().UTC().Add(10 * time.Minute)
	if input.ExpiresAt != nil && !input.ExpiresAt.IsZero() {
		expiresAt = input.ExpiresAt.UTC()
	}
	req := store.ApprovalRequest{
		ID:         "apr_" + types.NewUUID(),
		ThreadID:   strings.TrimSpace(input.ThreadID),
		RunID:      strings.TrimSpace(input.RunID),
		ToolCallID: strings.TrimSpace(input.ToolCallID),
		Tool:       strings.TrimSpace(input.Tool),
		Category:   normalizeCategory(input.Category),
		Operation:  strings.TrimSpace(input.Operation),
		Subject:    normalizeSubject(input.Subject),
		Summary:    strings.TrimSpace(input.Summary),
		Preview:    cloneMap(input.Preview),
		Options:    normalizeOptions(input.Options),
		Status:     "pending",
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
	}
	if req.Subject.Kind == "" {
		req.Subject.Kind = "tool"
	}
	if req.Subject.Tool == "" {
		req.Subject.Tool = req.Tool
	}
	if req.Summary == "" {
		req.Summary = approvalSummary(req)
	}
	s.mu.Lock()
	existing, ok, err := s.findMatchingPendingApproval(ctx, req)
	if err != nil {
		s.mu.Unlock()
		return store.ApprovalRequest{}, false, err
	}
	if ok {
		s.mu.Unlock()
		return existing, true, nil
	}
	created, err := s.store.CreateApprovalRequest(ctx, req)
	if err != nil {
		s.mu.Unlock()
		return store.ApprovalRequest{}, false, err
	}
	s.mu.Unlock()
	_ = s.audit(AuditRecord{
		Type:       "approval_requested",
		ApprovalID: created.ID,
		ThreadID:   created.ThreadID,
		RunID:      created.RunID,
		Category:   created.Category,
		Subject:    created.Subject,
		CreatedAt:  time.Now().UTC(),
	})
	return created, false, nil
}

func (s *Service) findMatchingPendingApproval(ctx context.Context, req store.ApprovalRequest) (store.ApprovalRequest, bool, error) {
	if err := s.store.ExpireStaleApprovalRequests(ctx, time.Now().UTC()); err != nil {
		return store.ApprovalRequest{}, false, err
	}
	pending, err := s.store.ListApprovalRequests(ctx, store.ApprovalFilter{
		ThreadID: req.ThreadID,
		RunID:    req.RunID,
		Status:   "pending",
	})
	if err != nil {
		return store.ApprovalRequest{}, false, err
	}
	for _, approval := range pending {
		if samePendingApprovalRequest(req, approval) {
			return approval, true, nil
		}
	}
	return store.ApprovalRequest{}, false, nil
}

func (s *Service) Await(ctx context.Context, approvalID string, timeout time.Duration) (store.ApprovalRequest, error) {
	id := strings.TrimSpace(approvalID)
	if id == "" {
		return store.ApprovalRequest{}, errors.New("approvalId is required")
	}
	current, err := s.store.GetApprovalRequest(ctx, id)
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	if current.Status != "pending" {
		return current, nil
	}
	ch := make(chan store.ApprovalRequest, 1)
	s.mu.Lock()
	s.waiters[id] = append(s.waiters[id], ch)
	s.mu.Unlock()
	defer s.removeWaiter(id, ch)

	var timer <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(timeout)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
		return store.ApprovalRequest{}, ctx.Err()
	case <-timer:
		return current, context.DeadlineExceeded
	case updated := <-ch:
		return updated, nil
	}
}

func (s *Service) Approve(ctx context.Context, approvalID string, scope string) (store.ApprovalRequest, error) {
	return s.ApproveWithOptions(ctx, approvalID, ApproveOptions{Scope: scope})
}

func (s *Service) ApproveWithOptions(ctx context.Context, approvalID string, opts ApproveOptions) (store.ApprovalRequest, error) {
	scope := normalizeScope(opts.Scope)
	current, err := s.store.GetApprovalRequest(ctx, approvalID)
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	if strings.TrimSpace(strings.ToLower(current.Status)) != "pending" {
		return current, store.ErrConflict
	}
	if !approvalScopeOffered(current.Options, scope) {
		return current, store.ErrConflict
	}
	decisionSubject, err := approvalDecisionSubject(current, opts.Subject)
	if err != nil {
		return current, err
	}
	decision := store.ApprovalDecision{Action: "approve", Scope: scope, DecidedAt: time.Now().UTC()}
	if decisionSubject != nil {
		decision.Subject = decisionSubject
	}
	approval, err := s.store.DecideApprovalRequest(ctx, approvalID, decision, "approved")
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	if strings.TrimSpace(strings.ToLower(approval.Status)) != "approved" {
		return approval, store.ErrConflict
	}
	if approval.Decision != nil && approval.Decision.Action == "approve" {
		ruleSubject := approval.Subject
		if approval.Decision.Subject != nil {
			ruleSubject = *approval.Decision.Subject
		}
		if scope == "thread" || scope == "always" {
			ruleScope := "global"
			if scope == "thread" {
				ruleScope = "thread:" + strings.TrimSpace(approval.ThreadID)
			}
			_, _ = s.CreateRule(ctx, PermissionRule{
				Category:   approval.Category,
				Effect:     EffectAllow,
				Scope:      ruleScope,
				Subject:    ruleSubject,
				CreatedVia: "approval:" + approval.ID,
			})
		}
		_ = s.audit(AuditRecord{
			Type:       "approval_approved",
			ApprovalID: approval.ID,
			ThreadID:   approval.ThreadID,
			RunID:      approval.RunID,
			Category:   approval.Category,
			Scope:      scope,
			Subject:    ruleSubject,
			CreatedAt:  time.Now().UTC(),
		})
	}
	s.notify(approval.ID, approval)
	return approval, nil
}

func approvalDecisionSubject(current store.ApprovalRequest, override *store.PermissionSubject) (*store.PermissionSubject, error) {
	if override == nil {
		return nil, nil
	}
	currentSubject := normalizeSubject(current.Subject)
	if currentSubject.Kind != "local_location" {
		return nil, errors.New("approval subject override is only allowed for local file requests")
	}
	selected := normalizeSubject(*override)
	if selected.Kind == "" {
		selected.Kind = "local_location"
	}
	if selected.Kind != "local_location" {
		return nil, errors.New("approval subject override must be a local location")
	}
	root, err := cleanLocalPath(selected.LocalRoot)
	if err != nil {
		return nil, fmt.Errorf("approval subject local_root is invalid: %w", err)
	}
	localKind := normalizeLocalKind(selected.LocalKind)
	if localKind == "" {
		localKind = normalizeLocalKind(currentSubject.LocalKind)
	}
	if localKind == "" {
		if info, statErr := os.Stat(root); statErr == nil && info != nil && !info.IsDir() {
			localKind = "file"
		} else {
			localKind = "folder"
		}
	}
	excludes := selected.Excludes
	if len(excludes) == 0 {
		excludes = currentSubject.Excludes
	}
	if len(excludes) == 0 {
		excludes = store.DefaultSensitiveLocalPermissionExcludes()
	}
	out := store.PermissionSubject{
		Kind:      "local_location",
		LocalRoot: root,
		LocalKind: localKind,
		Excludes:  normalizeOptions(excludes),
	}
	return &out, nil
}

func approvalScopeOffered(options []string, scope string) bool {
	allowed := normalizeOptions(options)
	if len(allowed) == 0 {
		allowed = []string{"once"}
	}
	scope = normalizeScope(scope)
	for _, option := range allowed {
		if option == scope {
			return true
		}
	}
	return false
}

func (s *Service) Reject(ctx context.Context, approvalID string) (store.ApprovalRequest, error) {
	current, err := s.store.GetApprovalRequest(ctx, approvalID)
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	if strings.TrimSpace(strings.ToLower(current.Status)) != "pending" {
		return current, store.ErrConflict
	}
	decision := store.ApprovalDecision{Action: "reject", Scope: "once", DecidedAt: time.Now().UTC()}
	approval, err := s.store.DecideApprovalRequest(ctx, approvalID, decision, "rejected")
	if err != nil {
		return store.ApprovalRequest{}, err
	}
	if strings.TrimSpace(strings.ToLower(approval.Status)) != "rejected" {
		return approval, store.ErrConflict
	}
	_ = s.audit(AuditRecord{
		Type:       "approval_rejected",
		ApprovalID: approval.ID,
		ThreadID:   approval.ThreadID,
		RunID:      approval.RunID,
		Category:   approval.Category,
		Subject:    approval.Subject,
		CreatedAt:  time.Now().UTC(),
	})
	s.notify(approval.ID, approval)
	return approval, nil
}

func (s *Service) CancelRunApprovalRequests(ctx context.Context, runID string) ([]store.ApprovalRequest, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, errors.New("runId is required")
	}
	pending, err := s.store.ListApprovalRequests(ctx, store.ApprovalFilter{
		RunID:  runID,
		Status: "pending",
		Limit:  1000,
	})
	if err != nil {
		return nil, err
	}
	if err := s.store.CancelRunApprovalRequests(ctx, runID); err != nil {
		return nil, err
	}
	canceled := []store.ApprovalRequest{}
	for _, approval := range pending {
		updated, err := s.store.GetApprovalRequest(ctx, approval.ID)
		if err != nil {
			continue
		}
		if strings.TrimSpace(strings.ToLower(updated.Status)) != "canceled" {
			continue
		}
		_ = s.audit(AuditRecord{
			Type:       "approval_canceled",
			ApprovalID: updated.ID,
			ThreadID:   updated.ThreadID,
			RunID:      updated.RunID,
			Category:   updated.Category,
			Subject:    updated.Subject,
			CreatedAt:  time.Now().UTC(),
		})
		s.notify(updated.ID, updated)
		canceled = append(canceled, updated)
	}
	return canceled, nil
}

func (s *Service) ListApprovals(ctx context.Context, filter ApprovalFilter) ([]store.ApprovalRequest, error) {
	_ = s.store.ExpireStaleApprovalRequests(ctx, time.Now().UTC())
	return s.store.ListApprovalRequests(ctx, filter)
}

func (s *Service) GetApproval(ctx context.Context, approvalID string) (store.ApprovalRequest, error) {
	return s.store.GetApprovalRequest(ctx, approvalID)
}

func (s *Service) ListRules(ctx context.Context, filter RuleFilter) ([]PermissionRule, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rules, err := s.readRules()
	if err != nil {
		return nil, err
	}
	category := normalizeCategory(filter.Category)
	scope := strings.TrimSpace(filter.Scope)
	now := time.Now().UTC()
	out := []PermissionRule{}
	for _, rule := range rules {
		if rule.ExpiresAt != nil && !rule.ExpiresAt.After(now) {
			continue
		}
		if category != "" && normalizeCategory(rule.Category) != category {
			continue
		}
		if scope != "" && strings.TrimSpace(rule.Scope) != scope {
			continue
		}
		out = append(out, rule)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (s *Service) CreateRule(ctx context.Context, input PermissionRule) (PermissionRule, error) {
	if err := ctx.Err(); err != nil {
		return PermissionRule{}, err
	}
	rule := normalizeRule(input)
	if rule.ID == "" {
		rule.ID = "perm_" + types.NewUUID()
	}
	if rule.CreatedAt.IsZero() {
		rule.CreatedAt = time.Now().UTC()
	}
	if rule.CreatedVia == "" {
		rule.CreatedVia = "settings"
	}
	if err := validateRule(rule); err != nil {
		return PermissionRule{}, err
	}
	rules, err := s.readRules()
	if err != nil {
		return PermissionRule{}, err
	}
	for _, existing := range rules {
		if existing.ID == rule.ID {
			return PermissionRule{}, store.ErrConflict
		}
	}
	rules = append(rules, rule)
	if err := s.writeRules(rules); err != nil {
		return PermissionRule{}, err
	}
	_ = s.audit(AuditRecord{
		Type:      "rule_created",
		RuleID:    rule.ID,
		Category:  rule.Category,
		Effect:    rule.Effect,
		Scope:     rule.Scope,
		Subject:   rule.Subject,
		CreatedAt: time.Now().UTC(),
	})
	return rule, nil
}

func (s *Service) UpdateRule(ctx context.Context, id string, input PermissionRule) (PermissionRule, error) {
	if err := ctx.Err(); err != nil {
		return PermissionRule{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return PermissionRule{}, errors.New("rule id is required")
	}
	rules, err := s.readRules()
	if err != nil {
		return PermissionRule{}, err
	}
	for i := range rules {
		if rules[i].ID != id {
			continue
		}
		updated := normalizeRule(rules[i])
		updated.ID = id
		if strings.TrimSpace(input.Category) != "" {
			updated.Category = normalizeCategory(input.Category)
		}
		if strings.TrimSpace(input.Effect) != "" {
			updated.Effect = normalizeEffect(input.Effect)
		}
		if strings.TrimSpace(input.Scope) != "" {
			updated.Scope = strings.TrimSpace(input.Scope)
		}
		if subjectPatchProvided(input.Subject) {
			updated.Subject = normalizeSubject(input.Subject)
		}
		if strings.TrimSpace(input.CreatedVia) != "" {
			updated.CreatedVia = strings.TrimSpace(input.CreatedVia)
		}
		if !input.CreatedAt.IsZero() {
			updated.CreatedAt = input.CreatedAt.UTC()
		}
		if input.LastUsedAt != nil {
			t := input.LastUsedAt.UTC()
			updated.LastUsedAt = &t
		}
		if input.ExpiresAt != nil {
			t := input.ExpiresAt.UTC()
			updated.ExpiresAt = &t
		}
		if err := validateRule(updated); err != nil {
			return PermissionRule{}, err
		}
		rules[i] = updated
		if err := s.writeRules(rules); err != nil {
			return PermissionRule{}, err
		}
		_ = s.audit(AuditRecord{Type: "rule_updated", RuleID: id, Category: updated.Category, Effect: updated.Effect, Scope: updated.Scope, Subject: updated.Subject, CreatedAt: time.Now().UTC()})
		return updated, nil
	}
	return PermissionRule{}, store.ErrNotFound
}

func (s *Service) DeleteRule(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("rule id is required")
	}
	rules, err := s.readRules()
	if err != nil {
		return err
	}
	out := rules[:0]
	var removed *PermissionRule
	for i := range rules {
		if rules[i].ID == id {
			r := rules[i]
			removed = &r
			continue
		}
		out = append(out, rules[i])
	}
	if removed == nil {
		return store.ErrNotFound
	}
	if err := s.writeRules(out); err != nil {
		return err
	}
	_ = s.audit(AuditRecord{Type: "rule_deleted", RuleID: id, Category: removed.Category, Effect: removed.Effect, Scope: removed.Scope, Subject: removed.Subject, CreatedAt: time.Now().UTC()})
	return nil
}

func (s *Service) ListAudit(ctx context.Context, limit int) ([]AuditRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	f, err := os.Open(s.auditPath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []AuditRecord{}, nil
		}
		return nil, err
	}
	defer f.Close()
	records := []AuditRecord{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &rec); err == nil {
			records = append(records, rec)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].CreatedAt.After(records[j].CreatedAt) })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *Service) TestLocalLocation(ctx context.Context, subject store.PermissionSubject) (LocalLocationTestResult, error) {
	if err := ctx.Err(); err != nil {
		return LocalLocationTestResult{}, err
	}
	subject = normalizeSubject(subject)
	if subject.Kind == "" {
		subject.Kind = "local_location"
	}
	if subject.Kind != "local_location" {
		return LocalLocationTestResult{}, errors.New("subject.kind must be local_location")
	}
	root, err := cleanLocalPath(subject.LocalRoot)
	if err != nil {
		return LocalLocationTestResult{}, fmt.Errorf("subject.local_root is invalid: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return LocalLocationTestResult{}, err
	}
	localKind := normalizeLocalKind(subject.LocalKind)
	if localKind == "" {
		if info.IsDir() {
			localKind = "folder"
		} else {
			localKind = "file"
		}
	}
	switch localKind {
	case "folder":
		if !info.IsDir() {
			return LocalLocationTestResult{}, errors.New("subject.local_root must be a directory")
		}
		if _, err := os.ReadDir(root); err != nil {
			return LocalLocationTestResult{}, fmt.Errorf("subject.local_root is not readable: %w", err)
		}
	case "file":
		if info.IsDir() {
			return LocalLocationTestResult{}, errors.New("subject.local_root must be a file")
		}
		f, err := os.Open(root)
		if err != nil {
			return LocalLocationTestResult{}, fmt.Errorf("subject.local_root is not readable: %w", err)
		}
		_ = f.Close()
	default:
		return LocalLocationTestResult{}, errors.New("subject.local_kind must be folder or file")
	}
	if len(subject.Excludes) == 0 {
		subject.Excludes = store.DefaultSensitiveLocalPermissionExcludes()
	}
	subject.LocalRoot = root
	subject.LocalKind = localKind
	return LocalLocationTestResult{
		Subject:  subject,
		Kind:     localKind,
		Readable: true,
	}, nil
}

func (s *Service) permissionsDir() string {
	return filepath.Join(s.cfgDir, "permissions")
}

func (s *Service) rulesPath() string {
	return filepath.Join(s.permissionsDir(), "rules.json")
}

func (s *Service) auditPath() string {
	return filepath.Join(s.permissionsDir(), "audit.jsonl")
}

func (s *Service) readRules() ([]PermissionRule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readRulesLocked()
}

func (s *Service) readRulesLocked() ([]PermissionRule, error) {
	path := s.rulesPath()
	b, err := atomicfile.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			env := rulesEnvelope{
				Kind:          "ai_analyst.permission_rules",
				SchemaVersion: 1,
				Rules:         []PermissionRule{},
				UpdatedAt:     time.Now().UTC(),
			}
			if err := s.writeRulesLocked(env.Rules); err != nil {
				return nil, err
			}
			return []PermissionRule{}, nil
		}
		return nil, err
	}
	var env rulesEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return nil, err
	}
	out := make([]PermissionRule, 0, len(env.Rules))
	for _, raw := range env.Rules {
		rule := normalizeRule(raw)
		if err := validateRule(rule); err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

func (s *Service) writeRules(rules []PermissionRule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeRulesLocked(rules)
}

func (s *Service) writeRulesLocked(rules []PermissionRule) error {
	env := rulesEnvelope{
		Kind:          "ai_analyst.permission_rules",
		SchemaVersion: 1,
		Rules:         append([]PermissionRule(nil), rules...),
		UpdatedAt:     time.Now().UTC(),
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.permissionsDir(), 0o700); err != nil {
		return err
	}
	return atomicfile.WriteFile(s.rulesPath(), append(b, '\n'), 0o600)
}

func (s *Service) markRuleUsed(id string, now time.Time) error {
	if id == "" {
		return nil
	}
	rules, err := s.readRules()
	if err != nil {
		return err
	}
	for i := range rules {
		if rules[i].ID == id {
			rules[i].LastUsedAt = &now
			return s.writeRules(rules)
		}
	}
	return nil
}

func (s *Service) audit(record AuditRecord) error {
	if strings.TrimSpace(record.ID) == "" {
		record.ID = "aud_" + types.NewUUID()
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	if err := os.MkdirAll(s.permissionsDir(), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.auditPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(b, '\n')); err != nil {
		return err
	}
	return nil
}

func (s *Service) notify(id string, approval store.ApprovalRequest) {
	s.mu.Lock()
	waiters := append([]chan store.ApprovalRequest(nil), s.waiters[id]...)
	delete(s.waiters, id)
	s.mu.Unlock()
	for _, ch := range waiters {
		select {
		case ch <- approval:
		default:
		}
	}
}

func (s *Service) removeWaiter(id string, ch chan store.ApprovalRequest) {
	s.mu.Lock()
	defer s.mu.Unlock()
	waiters := s.waiters[id]
	out := waiters[:0]
	for _, waiter := range waiters {
		if waiter != ch {
			out = append(out, waiter)
		}
	}
	if len(out) == 0 {
		delete(s.waiters, id)
		return
	}
	s.waiters[id] = out
}

func normalizeRule(in PermissionRule) PermissionRule {
	out := in
	out.ID = strings.TrimSpace(out.ID)
	out.Category = normalizeCategory(out.Category)
	out.Effect = normalizeEffect(out.Effect)
	out.Scope = strings.TrimSpace(out.Scope)
	if out.Scope == "" {
		out.Scope = "global"
	}
	out.Subject = normalizeSubject(out.Subject)
	if out.Subject.Kind == "local_location" && strings.TrimSpace(out.Subject.LocalRoot) != "" {
		if clean, err := cleanLocalPath(out.Subject.LocalRoot); err == nil {
			out.Subject.LocalRoot = clean
		}
		if out.Subject.LocalKind == "" {
			out.Subject.LocalKind = "folder"
		}
	}
	if out.Category == CategoryRead && out.Subject.Kind == "local_location" && len(out.Subject.Excludes) == 0 {
		out.Subject.Excludes = normalizeOptions(store.DefaultSensitiveLocalPermissionExcludes())
	}
	out.CreatedVia = strings.TrimSpace(out.CreatedVia)
	if !out.CreatedAt.IsZero() {
		out.CreatedAt = out.CreatedAt.UTC()
	}
	if out.LastUsedAt != nil {
		t := out.LastUsedAt.UTC()
		out.LastUsedAt = &t
	}
	if out.ExpiresAt != nil {
		t := out.ExpiresAt.UTC()
		out.ExpiresAt = &t
	}
	return out
}

func validateRule(rule PermissionRule) error {
	if rule.ID == "" {
		return errors.New("rule.id is required")
	}
	if containsPathSeparator(rule.ID) {
		return errors.New("rule.id is invalid")
	}
	switch rule.Category {
	case CategoryRead, CategoryWrite, CategoryExecute:
	default:
		return errors.New("rule.category must be read, write, or execute")
	}
	switch rule.Effect {
	case EffectAllow, EffectAsk, EffectDeny:
	default:
		return errors.New("rule.effect must be allow, ask, or deny")
	}
	if rule.Scope != "global" && !strings.HasPrefix(rule.Scope, "thread:") {
		return errors.New("rule.scope must be global or thread:<id>")
	}
	if rule.Subject.Kind == "" {
		return errors.New("rule.subject.kind is required")
	}
	if rule.Subject.Kind == "local_location" {
		if strings.TrimSpace(rule.Subject.LocalRoot) == "" {
			return errors.New("rule.subject.local_root is required for local locations")
		}
		if _, err := cleanLocalPath(rule.Subject.LocalRoot); err != nil {
			return fmt.Errorf("rule.subject.local_root is invalid: %w", err)
		}
		if kind := normalizeLocalKind(rule.Subject.LocalKind); kind != "" && kind != rule.Subject.LocalKind {
			return errors.New("rule.subject.local_kind must be folder or file")
		}
	}
	if rule.Subject.Kind == "tool_set" && strings.TrimSpace(rule.Subject.ToolSetID) == "" {
		return errors.New("rule.subject.tool_set_id is required for tool sets")
	}
	if rule.CreatedAt.IsZero() {
		return errors.New("rule.created_at is required")
	}
	return nil
}

func subjectPatchProvided(subject store.PermissionSubject) bool {
	return strings.TrimSpace(subject.Kind) != "" ||
		strings.TrimSpace(subject.Tool) != "" ||
		strings.TrimSpace(subject.Connector) != "" ||
		strings.TrimSpace(subject.ToolSetID) != "" ||
		strings.TrimSpace(subject.LocalRoot) != "" ||
		strings.TrimSpace(subject.LocalKind) != "" ||
		strings.TrimSpace(subject.Repository) != "" ||
		strings.TrimSpace(subject.RemoteKind) != "" ||
		strings.TrimSpace(subject.RemoteID) != "" ||
		strings.TrimSpace(subject.Operation) != "" ||
		len(subject.Excludes) > 0
}

func subjectMatches(ruleSubject store.PermissionSubject, input store.PermissionSubject) bool {
	rule := normalizeSubject(ruleSubject)
	in := normalizeSubject(input)
	if rule.Kind != "" && in.Kind != "" && rule.Kind != in.Kind {
		return false
	}
	if rule.Tool != "" && rule.Tool != in.Tool {
		return false
	}
	if rule.Connector != "" && rule.Connector != in.Connector {
		return false
	}
	if rule.ToolSetID != "" && rule.ToolSetID != in.ToolSetID {
		return false
	}
	if rule.Repository != "" && rule.Repository != in.Repository {
		return false
	}
	if rule.RemoteKind != "" && rule.RemoteKind != in.RemoteKind {
		return false
	}
	if rule.RemoteID != "" && rule.RemoteID != in.RemoteID {
		return false
	}
	if rule.Operation != "" && rule.Operation != in.Operation {
		return false
	}
	if rule.LocalRoot != "" {
		return localRootSubjectMatches(rule.LocalRoot, in.LocalRoot, rule.LocalKind, in.LocalKind, effectiveLocalExcludes(rule.Excludes, in.Excludes))
	}
	return true
}

func samePendingApprovalRequest(a store.ApprovalRequest, b store.ApprovalRequest) bool {
	if strings.TrimSpace(a.ThreadID) != strings.TrimSpace(b.ThreadID) ||
		strings.TrimSpace(a.RunID) != strings.TrimSpace(b.RunID) ||
		strings.TrimSpace(a.ToolCallID) != strings.TrimSpace(b.ToolCallID) ||
		strings.TrimSpace(a.Tool) != strings.TrimSpace(b.Tool) ||
		normalizeCategory(a.Category) != normalizeCategory(b.Category) ||
		strings.TrimSpace(strings.ToLower(a.Operation)) != strings.TrimSpace(strings.ToLower(b.Operation)) {
		return false
	}
	return sameNormalizedSubject(a.Subject, b.Subject)
}

func sameNormalizedSubject(a store.PermissionSubject, b store.PermissionSubject) bool {
	a = normalizeSubject(a)
	b = normalizeSubject(b)
	return a.Kind == b.Kind &&
		a.Tool == b.Tool &&
		a.Connector == b.Connector &&
		a.ToolSetID == b.ToolSetID &&
		a.LocalRoot == b.LocalRoot &&
		effectiveLocalKind(a.LocalKind) == effectiveLocalKind(b.LocalKind) &&
		a.Repository == b.Repository &&
		a.RemoteKind == b.RemoteKind &&
		a.RemoteID == b.RemoteID &&
		a.Operation == b.Operation &&
		sameStringSlice(a.Excludes, b.Excludes)
}

func sameStringSlice(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func localRootContains(ruleRoot string, inputRoot string) bool {
	return localRootSubjectMatches(ruleRoot, inputRoot, "folder", "folder", nil)
}

func localRootSubjectMatches(ruleRoot string, inputRoot string, ruleKind string, inputKind string, excludes []string) bool {
	ruleClean, err := cleanLocalPath(ruleRoot)
	if err != nil {
		return false
	}
	inputClean, err := cleanLocalPath(inputRoot)
	if err != nil {
		return false
	}
	if inputClean == ruleClean {
		return true
	}
	if effectiveLocalKind(ruleKind) == "file" {
		return false
	}
	rel, err := filepath.Rel(ruleClean, inputClean)
	if err != nil {
		return false
	}
	if rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return false
	}
	return !localPathExcludedByPatterns(filepath.ToSlash(rel), excludes)
}

func localPathExcludedByPatterns(rel string, excludes []string) bool {
	return store.LocalPathExcludedByPermissionPatterns(rel, excludes)
}

func effectiveLocalExcludes(ruleExcludes []string, inputExcludes []string) []string {
	if len(ruleExcludes) == 0 {
		return normalizeOptions(inputExcludes)
	}
	return normalizeOptions(append(append([]string(nil), ruleExcludes...), inputExcludes...))
}

func cleanLocalPath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("path is required")
	}
	if trimmed == "~" || strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && strings.TrimSpace(home) != "" {
			if trimmed == "~" {
				trimmed = home
			} else {
				trimmed = filepath.Join(home, strings.TrimPrefix(trimmed, "~/"))
			}
		}
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return "", err
	}
	if evaluated, err := filepath.EvalSymlinks(abs); err == nil {
		abs = evaluated
	}
	return filepath.Clean(abs), nil
}

func normalizeSubject(in store.PermissionSubject) store.PermissionSubject {
	return store.PermissionSubject{
		Kind:       strings.TrimSpace(strings.ToLower(in.Kind)),
		Tool:       strings.TrimSpace(in.Tool),
		Connector:  strings.TrimSpace(strings.ToLower(in.Connector)),
		ToolSetID:  strings.TrimSpace(strings.ToLower(in.ToolSetID)),
		LocalRoot:  strings.TrimSpace(in.LocalRoot),
		LocalKind:  normalizeLocalKind(in.LocalKind),
		Repository: strings.TrimSpace(strings.ToLower(in.Repository)),
		RemoteKind: strings.TrimSpace(strings.ToLower(in.RemoteKind)),
		RemoteID:   strings.TrimSpace(in.RemoteID),
		Operation:  strings.TrimSpace(strings.ToLower(in.Operation)),
		Excludes:   normalizeOptions(in.Excludes),
	}
}

func normalizeLocalKind(kind string) string {
	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "folder":
		return "folder"
	case "file":
		return "file"
	default:
		return ""
	}
}

func effectiveLocalKind(kind string) string {
	if normalized := normalizeLocalKind(kind); normalized != "" {
		return normalized
	}
	return "folder"
}

func normalizeCategory(category string) string {
	switch strings.TrimSpace(strings.ToLower(category)) {
	case CategoryWrite:
		return CategoryWrite
	case CategoryExecute:
		return CategoryExecute
	default:
		return CategoryRead
	}
}

func normalizeEffect(effect string) string {
	switch strings.TrimSpace(strings.ToLower(effect)) {
	case EffectAllow:
		return EffectAllow
	case EffectDeny:
		return EffectDeny
	default:
		return EffectAsk
	}
}

func normalizeScope(scope string) string {
	switch strings.TrimSpace(strings.ToLower(scope)) {
	case "thread":
		return "thread"
	case "always":
		return "always"
	default:
		return "once"
	}
}

func decisionForEffect(effect string) string {
	switch normalizeEffect(effect) {
	case EffectAllow:
		return DecisionAllow
	case EffectDeny:
		return DecisionDeny
	default:
		return DecisionAsk
	}
}

func defaultDecision(category string, subject store.PermissionSubject) string {
	if normalizeCategory(category) == CategoryRead {
		if normalizeSubject(subject).Kind == "local_location" {
			return DecisionAsk
		}
		return DecisionAllow
	}
	return DecisionAsk
}

func normalizeOptions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		v := strings.TrimSpace(strings.ToLower(raw))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func approvalSummary(req store.ApprovalRequest) string {
	category := normalizeCategory(req.Category)
	tool := strings.TrimSpace(req.Tool)
	if tool == "" {
		tool = "tool"
	}
	return fmt.Sprintf("Allow %s access for %s", category, tool)
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for k, v := range in {
			out[k] = v
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func containsPathSeparator(v string) bool {
	s := strings.TrimSpace(v)
	return s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, "\\")
}
