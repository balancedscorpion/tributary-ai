package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/atomicfile"
	"ai-analyst-local/internal/chat/jsondb"
	"ai-analyst-local/internal/chat/types"
)

const (
	contextCheckpointKind           = "ai_analyst.context_checkpoint"
	contextCheckpointSchemaVersion  = 1
	runEventKind                    = "ai_analyst.run_event"
	runEventSchemaVersion           = 1
	approvalRequestKind             = "ai_analyst.approval_request"
	approvalRequestSchemaVersion    = 1
	localSearchJobKind              = "ai_analyst.local_search_job"
	localSearchJobSchemaVersion     = 1
	runPlanKind                     = "ai_analyst.run_plan"
	runPlanSchemaVersion            = 1
	capabilityEvidenceKind          = "ai_analyst.provider_capability_evidence"
	capabilityEvidenceSchemaVersion = 1
)

type runPlanRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	RunPlan
}

type capabilityEvidenceRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`
	ProviderCapabilityEvidence
}

type contextCheckpointRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`

	ID                    string              `json:"id"`
	ThreadID              string              `json:"thread_id"`
	RunID                 *string             `json:"run_id,omitempty"`
	SourceMessageSeq      int64               `json:"source_message_seq"`
	SourceMessageID       string              `json:"source_message_id"`
	ReplacementHistory    []types.ChatMessage `json:"replacement_history"`
	Summary               string              `json:"summary,omitempty"`
	SummaryMessageIDs     []string            `json:"summary_message_ids,omitempty"`
	ReferencedArtifactIDs []string            `json:"referenced_artifact_ids,omitempty"`
	TokenEstimate         int                 `json:"token_estimate,omitempty"`
	CharsPerToken         int                 `json:"chars_per_token,omitempty"`
	Model                 string              `json:"model,omitempty"`
	ProviderID            string              `json:"provider_id,omitempty"`
	PlanJSON              json.RawMessage     `json:"plan,omitempty"`
	CreatedAt             time.Time           `json:"created_at"`
	UpdatedAt             time.Time           `json:"updated_at"`
}

type runEventRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`

	RunID     string          `json:"run_id"`
	ThreadID  string          `json:"thread_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

type approvalRequestRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`

	ID         string            `json:"id"`
	ThreadID   string            `json:"thread_id"`
	RunID      string            `json:"run_id"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
	Tool       string            `json:"tool"`
	Category   string            `json:"category"`
	Operation  string            `json:"operation,omitempty"`
	Subject    PermissionSubject `json:"subject"`
	Summary    string            `json:"summary"`
	Preview    map[string]any    `json:"preview,omitempty"`
	Options    []string          `json:"options,omitempty"`
	Status     string            `json:"status"`
	Decision   *ApprovalDecision `json:"decision,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	ExpiresAt  time.Time         `json:"expires_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

type localSearchJobRecord struct {
	Kind          string `json:"kind"`
	SchemaVersion int    `json:"schema_version"`

	ID          string         `json:"id"`
	ThreadID    string         `json:"thread_id"`
	RunID       string         `json:"run_id"`
	ToolCallID  string         `json:"tool_call_id,omitempty"`
	Status      string         `json:"status"`
	Query       string         `json:"query"`
	GrantID     string         `json:"grant_id,omitempty"`
	LocalRoot   string         `json:"local_root,omitempty"`
	LocalKind   string         `json:"local_kind,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	CompletedAt *time.Time     `json:"completed_at,omitempty"`
	FilesCount  int            `json:"files_count,omitempty"`
	Error       string         `json:"error,omitempty"`
	Result      map[string]any `json:"result,omitempty"`
}

type JSONStore struct {
	cfgDir string

	mu            sync.RWMutex
	evidenceMu    sync.Mutex
	longHorizonMu *jsonLongHorizonMutex

	threadLocks  map[string]*sync.Mutex
	projectLocks map[string]*sync.Mutex
	runToThread  map[string]string
	artToThread  map[string]string
	msgToThread  map[string]string
	planToThread map[string]string
}

func NewJSON(cfgDir string) (*JSONStore, error) {
	if strings.TrimSpace(cfgDir) == "" {
		return nil, errors.New("cfgDir is required")
	}
	s := &JSONStore{
		cfgDir:       cfgDir,
		threadLocks:  map[string]*sync.Mutex{},
		projectLocks: map[string]*sync.Mutex{},
		runToThread:  map[string]string{},
		artToThread:  map[string]string{},
		msgToThread:  map[string]string{},
		planToThread: map[string]string{},
	}
	if err := jsondb.EnsureBaseDirs(cfgDir); err != nil {
		return nil, err
	}
	longHorizonMu, err := newJSONLongHorizonMutex(cfgDir)
	if err != nil {
		return nil, err
	}
	s.longHorizonMu = longHorizonMu
	if err := os.MkdirAll(s.projectsDir(), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.jobsDir(), 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.capabilityEvidenceDir(), 0o700); err != nil {
		return nil, err
	}
	if err := s.initMeta(); err != nil {
		return nil, err
	}
	if err := s.recoverQueryTransactions(); err != nil {
		return nil, err
	}
	if err := s.reconcileContinuationCheckpoints(); err != nil {
		return nil, err
	}
	if err := s.reconcileQueryCapacityReservations(); err != nil {
		return nil, err
	}
	if err := s.rebuildIndexes(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *JSONStore) Ping(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

func (s *JSONStore) CreateThread(ctx context.Context, projectID *string) (Thread, error) {
	select {
	case <-ctx.Done():
		return Thread{}, ctx.Err()
	default:
	}
	id := types.NewUUID()
	now := nowUTC()
	title := "New Thread"

	t := jsondb.Thread{
		Kind:          jsondb.KindThread,
		SchemaVersion: jsondb.SchemaVersion,
		ID:            id,
		Title:         title,
		ProjectID:     trimPtr(projectID),
		Status:        "active",
		CreatedAt:     now,
		UpdatedAt:     now,
		Counters: jsondb.ThreadCounters{
			NextSeq: 1,
		},
	}
	if err := validateThreadRecord(t); err != nil {
		return Thread{}, err
	}
	if err := s.ensureThreadDirs(id); err != nil {
		return Thread{}, err
	}
	p, err := jsondb.ThreadPath(s.cfgDir, id)
	if err != nil {
		return Thread{}, err
	}
	if err := jsondb.WriteJSONFile(p, t); err != nil {
		return Thread{}, err
	}
	s.threadMu(id)
	return threadFromRecord(t), nil
}

func (s *JSONStore) ListThreads(ctx context.Context, status string, limit int) ([]Thread, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	st := strings.TrimSpace(status)
	if st == "" {
		st = "active"
	}
	if limit <= 0 {
		limit = 50
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out := make([]Thread, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		t, err := s.readThread(entry.Name())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if strings.TrimSpace(t.Status) != st {
			continue
		}
		out = append(out, threadFromRecord(t))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) GetThread(ctx context.Context, threadID string) (Thread, error) {
	select {
	case <-ctx.Done():
		return Thread{}, ctx.Err()
	default:
	}
	t, err := s.readThread(threadID)
	if err != nil {
		return Thread{}, err
	}
	return threadFromRecord(t), nil
}

func (s *JSONStore) ArchiveThread(ctx context.Context, threadID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()

	t, err := s.readThread(id)
	if err != nil {
		return err
	}
	t.Status = "archived"
	t.UpdatedAt = nowUTC()
	return s.writeThread(t)
}

func (s *JSONStore) DeleteThread(ctx context.Context, threadID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()

	dir, err := s.threadDir(id)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(dir); errors.Is(statErr, fs.ErrNotExist) {
		return ErrNotFound
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	s.mu.Lock()
	for runID, tid := range s.runToThread {
		if tid == id {
			delete(s.runToThread, runID)
		}
	}
	for artID, tid := range s.artToThread {
		if tid == id {
			delete(s.artToThread, artID)
		}
	}
	for msgID, tid := range s.msgToThread {
		if tid == id {
			delete(s.msgToThread, msgID)
		}
	}
	for planID, tid := range s.planToThread {
		if tid == id {
			delete(s.planToThread, planID)
		}
	}
	delete(s.threadLocks, id)
	s.mu.Unlock()
	return nil
}

func (s *JSONStore) UpdateThreadTitleSummary(ctx context.Context, threadID string, title *string, summary *string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()

	t, err := s.readThread(id)
	if err != nil {
		return err
	}
	if title != nil {
		next := strings.TrimSpace(*title)
		t.Title = next
	}
	if summary != nil {
		sm := *summary
		t.Summary = &sm
		t.Counters.SummaryRunCount = t.Counters.RunCount
	}
	t.UpdatedAt = nowUTC()
	return s.writeThread(t)
}

func (s *JSONStore) UpdateThreadProject(ctx context.Context, threadID string, projectID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return errors.New("projectId is required")
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()

	t, err := s.readThread(id)
	if err != nil {
		return err
	}
	t.ProjectID = &pid
	t.UpdatedAt = nowUTC()
	return s.writeThread(t)
}

func (s *JSONStore) IncrementThreadRunCount(ctx context.Context, threadID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	if id == "" {
		return errors.New("threadId is required")
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()

	t, err := s.readThread(id)
	if err != nil {
		return err
	}
	t.Counters.RunCount++
	return s.writeThread(t)
}

func (s *JSONStore) SetThreadTarget(ctx context.Context, threadID string, target RunTarget) (Thread, error) {
	select {
	case <-ctx.Done():
		return Thread{}, ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	target.ConnectionID = strings.TrimSpace(target.ConnectionID)
	target.Model = strings.TrimSpace(target.Model)
	if id == "" || target.ConnectionID == "" || target.Model == "" {
		return Thread{}, fmt.Errorf("%w: threadId, connectionId, and model are required", ErrInvalid)
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()
	t, err := s.readThread(id)
	if err != nil {
		return Thread{}, err
	}
	t.LastTarget = &jsondb.RunTarget{ConnectionID: target.ConnectionID, Model: target.Model}
	t.UpdatedAt = nowUTC()
	if err := s.writeThread(t); err != nil {
		return Thread{}, err
	}
	return threadFromRecord(t), nil
}

func (s *JSONStore) DeleteThreadAttachmentPolicy(ctx context.Context, threadID string, policyID string) (Thread, error) {
	select {
	case <-ctx.Done():
		return Thread{}, ctx.Err()
	default:
	}
	id := strings.TrimSpace(threadID)
	policyID = strings.TrimSpace(policyID)
	if id == "" || policyID == "" {
		return Thread{}, fmt.Errorf("%w: threadId and policyId are required", ErrInvalid)
	}
	lock := s.threadMu(id)
	lock.Lock()
	defer lock.Unlock()
	t, err := s.readThread(id)
	if err != nil {
		return Thread{}, err
	}
	next := make([]jsondb.AttachmentPolicy, 0, len(t.AttachmentPolicies))
	removed := false
	for _, policy := range t.AttachmentPolicies {
		if strings.TrimSpace(policy.ID) == policyID {
			removed = true
			continue
		}
		next = append(next, policy)
	}
	if !removed {
		return Thread{}, ErrNotFound
	}
	t.AttachmentPolicies = next
	t.UpdatedAt = nowUTC()
	if err := s.writeThread(t); err != nil {
		return Thread{}, err
	}
	return threadFromRecord(t), nil
}

func (s *JSONStore) CreateRun(ctx context.Context, threadID string, providerID string, model string, profileID *string, systemPrompt string, maxIterations int) (Run, error) {
	select {
	case <-ctx.Done():
		return Run{}, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return Run{}, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return Run{}, err
	}
	id := types.NewUUID()
	now := nowUTC()
	if maxIterations <= 0 {
		maxIterations = 200
	}
	r := jsondb.Run{
		Kind:                 jsondb.KindRun,
		SchemaVersion:        jsondb.SchemaVersion,
		ID:                   id,
		ThreadID:             tid,
		Status:               "queued",
		Purpose:              jsondb.RunPurposeAnalysis,
		ProviderID:           strings.TrimSpace(providerID),
		Model:                strings.TrimSpace(model),
		ProfileID:            trimPtr(profileID),
		SystemPrompt:         systemPrompt,
		MaxIterations:        maxIterations,
		MaxAutoContinuations: 3,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
	if err := validateRunRecord(r); err != nil {
		return Run{}, err
	}
	p, err := jsondb.RunPath(s.cfgDir, tid, id)
	if err != nil {
		return Run{}, err
	}
	if err := jsondb.WriteJSONFile(p, r); err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	s.runToThread[id] = tid
	s.mu.Unlock()
	return runFromRecord(r), nil
}

func (s *JSONStore) CreateRunPlan(ctx context.Context, plan RunPlan) (RunPlan, error) {
	select {
	case <-ctx.Done():
		return RunPlan{}, ctx.Err()
	default:
	}
	if err := validateNewRunPlan(plan); err != nil {
		return RunPlan{}, err
	}
	lock := s.threadMu(plan.ThreadID)
	lock.Lock()
	defer lock.Unlock()
	if _, err := s.readThread(plan.ThreadID); err != nil {
		return RunPlan{}, err
	}
	if err := os.MkdirAll(s.runPlansDir(plan.ThreadID), 0o700); err != nil {
		return RunPlan{}, err
	}
	p := s.runPlanPath(plan.ThreadID, plan.ID)
	if _, err := os.Stat(p); err == nil {
		return RunPlan{}, ErrConflict
	} else if !errors.Is(err, fs.ErrNotExist) {
		return RunPlan{}, err
	}
	rec := runPlanRecord{Kind: runPlanKind, SchemaVersion: runPlanSchemaVersion, RunPlan: cloneRunPlan(plan)}
	if err := jsondb.WriteJSONFile(p, rec); err != nil {
		return RunPlan{}, err
	}
	s.mu.Lock()
	s.planToThread[plan.ID] = plan.ThreadID
	s.mu.Unlock()
	return cloneRunPlan(plan), nil
}

func (s *JSONStore) GetRunPlan(ctx context.Context, planID string) (RunPlan, error) {
	select {
	case <-ctx.Done():
		return RunPlan{}, ctx.Err()
	default:
	}
	rec, _, err := s.readRunPlan(planID)
	if err != nil {
		return RunPlan{}, err
	}
	return cloneRunPlan(rec.RunPlan), nil
}

func (s *JSONStore) GetRunPlanForRun(ctx context.Context, runID string) (RunPlan, error) {
	run, err := s.GetRun(ctx, runID)
	if err != nil {
		return RunPlan{}, err
	}
	if strings.TrimSpace(run.PlanID) == "" {
		return RunPlan{}, ErrNotFound
	}
	return s.GetRunPlan(ctx, run.PlanID)
}

func (s *JSONStore) FinalizeRunPlanAndCreateRun(ctx context.Context, req FinalizeRunPlanRequest) (Run, RunPlan, error) {
	select {
	case <-ctx.Done():
		return Run{}, RunPlan{}, ctx.Err()
	default:
	}
	planID := strings.TrimSpace(req.PlanID)
	threadID := strings.TrimSpace(req.ExpectedThreadID)
	if planID == "" || threadID == "" {
		return Run{}, RunPlan{}, ErrPlanStale
	}
	lock := s.threadMu(threadID)
	lock.Lock()
	defer lock.Unlock()

	rec, planPath, err := s.readRunPlan(planID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Run{}, RunPlan{}, ErrPlanStale
		}
		return Run{}, RunPlan{}, err
	}
	plan := rec.RunPlan
	if plan.Status == RunPlanStatusFinalized {
		if err := validateFinalizedPlanReplay(plan, req); err != nil {
			return Run{}, RunPlan{}, ErrPlanStale
		}
		run, err := s.GetRun(ctx, *plan.RunID)
		return run, cloneRunPlan(plan), err
	}
	selections, policies, err := validatePlanFinalization(plan, req)
	if err != nil {
		return Run{}, RunPlan{}, stalePlanError(err)
	}
	now := req.Now.UTC()
	if now.IsZero() {
		now = nowUTC()
	}

	thread, err := s.readThread(threadID)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	if expected := strings.TrimSpace(req.ExpectedThreadPolicyFingerprint); expected != "" &&
		AttachmentPoliciesFingerprint(threadFromRecord(thread).AttachmentPolicies) != expected {
		return Run{}, RunPlan{}, ErrPlanStale
	}
	previousThread := thread
	runRec, err := newJSONRunRecord(req.Run, planID, now)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	runPath, err := jsondb.RunPath(s.cfgDir, threadID, runRec.ID)
	if err != nil {
		return Run{}, RunPlan{}, err
	}
	if err := jsondb.WriteJSONFile(runPath, runRec); err != nil {
		return Run{}, RunPlan{}, err
	}

	thread.LastTarget = &jsondb.RunTarget{ConnectionID: plan.Target.ConnectionID, Model: plan.Target.Model}
	for _, policy := range policies {
		thread.AttachmentPolicies = appendJSONPolicyIdempotent(thread.AttachmentPolicies, policy)
	}
	thread.UpdatedAt = now
	if err := s.writeThread(thread); err != nil {
		_ = os.Remove(runPath)
		return Run{}, RunPlan{}, err
	}

	finalizedAt := now
	plan.Status = RunPlanStatusFinalized
	plan.Selections = selections
	plan.RunID = &runRec.ID
	plan.FinalizedAt = &finalizedAt
	rec.RunPlan = plan
	if err := jsondb.WriteJSONFile(planPath, rec); err != nil {
		_ = s.writeThread(previousThread)
		_ = os.Remove(runPath)
		return Run{}, RunPlan{}, err
	}
	s.mu.Lock()
	s.runToThread[runRec.ID] = threadID
	s.mu.Unlock()
	return runFromRecord(runRec), cloneRunPlan(plan), nil
}

func (s *JSONStore) DeleteExpiredRunPlans(ctx context.Context, now time.Time) (int64, error) {
	if now.IsZero() {
		now = nowUTC()
	}
	s.mu.RLock()
	index := make(map[string]string, len(s.planToThread))
	for planID, threadID := range s.planToThread {
		index[planID] = threadID
	}
	s.mu.RUnlock()
	var deleted int64
	for planID, threadID := range index {
		select {
		case <-ctx.Done():
			return deleted, ctx.Err()
		default:
		}
		lock := s.threadMu(threadID)
		lock.Lock()
		rec, p, err := s.readRunPlan(planID)
		if err == nil && rec.Status == RunPlanStatusDraft && !now.Before(rec.ExpiresAt) {
			if err = os.Remove(p); err == nil || errors.Is(err, fs.ErrNotExist) {
				deleted++
				s.mu.Lock()
				delete(s.planToThread, planID)
				s.mu.Unlock()
			}
		}
		lock.Unlock()
		if err != nil && !errors.Is(err, ErrNotFound) {
			return deleted, err
		}
	}
	return deleted, nil
}

func (s *JSONStore) UpsertProviderCapabilityEvidence(ctx context.Context, evidence ProviderCapabilityEvidence) (ProviderCapabilityEvidence, error) {
	select {
	case <-ctx.Done():
		return ProviderCapabilityEvidence{}, ctx.Err()
	default:
	}
	prepared, err := prepareProviderCapabilityEvidence(evidence)
	if err != nil {
		return ProviderCapabilityEvidence{}, err
	}
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	path := s.capabilityEvidencePath(prepared)
	var existing capabilityEvidenceRecord
	if err := readJSONLoose(path, &existing); err == nil &&
		existing.Kind == capabilityEvidenceKind && existing.SchemaVersion == capabilityEvidenceSchemaVersion {
		current, err := validateStoredProviderCapabilityEvidence(existing.ProviderCapabilityEvidence)
		if err == nil && current.VerifiedAt.After(prepared.VerifiedAt) {
			return current, nil
		}
	}
	rec := capabilityEvidenceRecord{
		Kind: capabilityEvidenceKind, SchemaVersion: capabilityEvidenceSchemaVersion,
		ProviderCapabilityEvidence: prepared,
	}
	if err := jsondb.WriteJSONFile(path, rec); err != nil {
		return ProviderCapabilityEvidence{}, err
	}
	return prepared, nil
}

func (s *JSONStore) ListProviderCapabilityEvidence(ctx context.Context, filter ProviderCapabilityEvidenceFilter) ([]ProviderCapabilityEvidence, error) {
	filter, err := normalizeProviderCapabilityEvidenceFilter(filter)
	if err != nil {
		return nil, err
	}
	s.evidenceMu.Lock()
	defer s.evidenceMu.Unlock()
	entries, err := os.ReadDir(s.capabilityEvidenceDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out := make([]ProviderCapabilityEvidence, 0)
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var rec capabilityEvidenceRecord
		if err := readJSONLoose(filepath.Join(s.capabilityEvidenceDir(), entry.Name()), &rec); err != nil {
			return nil, err
		}
		if rec.Kind != capabilityEvidenceKind || rec.SchemaVersion != capabilityEvidenceSchemaVersion {
			return nil, errors.New("provider capability evidence record kind or schema mismatch")
		}
		evidence, err := validateStoredProviderCapabilityEvidence(rec.ProviderCapabilityEvidence)
		if err != nil {
			return nil, err
		}
		if evidence.ConnectionID != filter.ConnectionID ||
			(filter.Model != "" && evidence.Model != filter.Model) ||
			(filter.Fingerprint != "" && evidence.Fingerprint != filter.Fingerprint) ||
			(filter.Probe != "" && evidence.Probe != filter.Probe) {
			continue
		}
		out = append(out, evidence)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].VerifiedAt.After(out[j].VerifiedAt) })
	if len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (s *JSONStore) GetRun(ctx context.Context, runID string) (Run, error) {
	select {
	case <-ctx.Done():
		return Run{}, ctx.Err()
	default:
	}
	r, err := s.readRun(runID)
	if err != nil {
		return Run{}, err
	}
	return runFromRecord(r), nil
}

func (s *JSONStore) ListRuns(ctx context.Context, threadID string, limit int) ([]Run, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	runsDir, err := jsondb.RunsDir(s.cfgDir, tid)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(runsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	recs := make([]jsondb.Run, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var rec jsondb.Run
		if err := readJSONLoose(filepath.Join(runsDir, entry.Name()), &rec); err != nil {
			return nil, err
		}
		if err := validateRunRecord(rec); err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].CreatedAt.Before(recs[j].CreatedAt) })
	if len(recs) > limit {
		recs = recs[len(recs)-limit:]
	}
	out := make([]Run, 0, len(recs))
	for _, rec := range recs {
		out = append(out, runFromRecord(rec))
	}
	return out, nil
}

func (s *JSONStore) UpdateRunStatus(ctx context.Context, runID string, status string, fields map[string]any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	s.longHorizonMu.Lock()
	defer s.longHorizonMu.Unlock()
	id := strings.TrimSpace(runID)
	if id == "" {
		return errors.New("runId is required")
	}
	st := strings.TrimSpace(status)
	if st == "" {
		return errors.New("status is required")
	}
	rec, p, err := s.readRunWithPath(id)
	if err != nil {
		return err
	}
	rec.Status = st
	rec.Revision++
	for key, val := range fields {
		switch key {
		case "profile_id":
			if v, ok := anyString(val); ok {
				rec.ProfileID = trimPtr(&v)
			}
		case "request_id":
			if v, ok := anyString(val); ok {
				rec.Usage.RequestID = &v
			}
		case "input_tokens":
			if val == nil {
				rec.Usage.InputTokens = nil
				break
			}
			if v, ok := anyInt(val); ok {
				rec.Usage.InputTokens = &v
			}
		case "output_tokens":
			if val == nil {
				rec.Usage.OutputTokens = nil
				break
			}
			if v, ok := anyInt(val); ok {
				rec.Usage.OutputTokens = &v
			}
		case "total_tokens":
			if val == nil {
				rec.Usage.TotalTokens = nil
				break
			}
			if v, ok := anyInt(val); ok {
				rec.Usage.TotalTokens = &v
			}
		case "error_code":
			v, ok := anyString(val)
			if ok {
				if rec.Error == nil {
					rec.Error = &jsondb.RunError{}
				}
				rec.Error.Code = v
			}
		case "error_message":
			v, ok := anyString(val)
			if ok {
				if rec.Error == nil {
					rec.Error = &jsondb.RunError{}
				}
				rec.Error.Message = v
			}
		case "started_at":
			if v, ok := anyTime(val); ok {
				t := v.UTC()
				rec.StartedAt = &t
			}
		case "ended_at":
			if v, ok := anyTime(val); ok {
				t := v.UTC()
				rec.EndedAt = &t
			}
		case "purpose":
			if v, ok := anyString(val); ok {
				rec.Purpose = jsondb.RunPurpose(strings.TrimSpace(v))
			}
		case "context_tokens_before":
			if v, ok := anyInt(val); ok {
				rec.ContextTokensBefore = &v
			}
		case "context_tokens_after":
			if v, ok := anyInt(val); ok {
				rec.ContextTokensAfter = &v
			}
		case "context_summary_buckets":
			if v, ok := anyInt(val); ok {
				rec.ContextSummaryBuckets = &v
			}
		case "context_summary_rounds":
			if v, ok := anyInt(val); ok {
				rec.ContextSummaryRounds = &v
			}
		case "context_artifact_refs_loaded":
			if v, ok := anyInt(val); ok {
				rec.ContextArtifactRefsLoaded = &v
			}
		case "context_need_retrieval":
			if v, ok := anyBool(val); ok {
				rec.ContextNeedRetrieval = &v
			}
		case "context_need_retrieval_tooling":
			if v, ok := anyBool(val); ok {
				rec.ContextNeedTooling = &v
			}
		case "context_hard_capped":
			if v, ok := anyBool(val); ok {
				rec.ContextHardCapped = &v
			}
		case "context_overflow_compacted":
			if v, ok := anyBool(val); ok {
				rec.ContextOverflowCompacted = &v
			}
		case "context_plan":
			if b, ok := val.([]byte); ok {
				rec.ContextPlanJSON = cloneRawJSON(b)
			} else if raw, ok := val.(json.RawMessage); ok {
				rec.ContextPlanJSON = cloneRawJSON(raw)
			} else if s, ok := val.(string); ok {
				rec.ContextPlanJSON = cloneRawJSON(json.RawMessage(strings.TrimSpace(s)))
			} else if val != nil {
				if b, err := json.Marshal(val); err == nil {
					rec.ContextPlanJSON = b
				}
			}
		}
	}
	rec.UpdatedAt = nowUTC()
	if err := validateRunRecord(rec); err != nil {
		return err
	}
	return jsondb.WriteJSONFile(p, rec)
}

func (s *JSONStore) AppendRunEvent(ctx context.Context, ev RunEvent) (RunEvent, error) {
	select {
	case <-ctx.Done():
		return RunEvent{}, ctx.Err()
	default:
	}
	runID := strings.TrimSpace(ev.RunID)
	if runID == "" {
		return RunEvent{}, errors.New("runId is required")
	}
	if ev.Seq < 0 {
		return RunEvent{}, errors.New("runEvent.seq must be >= 0")
	}
	run, err := s.readRun(runID)
	if err != nil {
		return RunEvent{}, err
	}
	lock := s.threadMu(run.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	run, err = s.readRun(runID)
	if err != nil {
		return RunEvent{}, err
	}
	tid := strings.TrimSpace(ev.ThreadID)
	if tid == "" {
		tid = run.ThreadID
	} else if tid != run.ThreadID {
		return RunEvent{}, errors.New("runEvent.thread_id does not match run")
	}
	ev.RunID = run.ID
	ev.ThreadID = tid
	latest, err := s.latestRunEventSeqLocked(run.ThreadID, run.ID)
	if err != nil {
		return RunEvent{}, err
	}
	if ev.Seq == 0 {
		ev.Seq = latest + 1
	} else if ev.Seq <= latest {
		return RunEvent{}, ErrConflict
	}
	prepared, err := prepareRunEvent(ev)
	if err != nil {
		return RunEvent{}, err
	}
	rec := runEventToRecord(prepared)
	if err := validateRunEventRecord(rec); err != nil {
		return RunEvent{}, err
	}
	path := s.runEventPath(rec.ThreadID, rec.RunID, rec.Seq)
	if _, err := os.Stat(path); err == nil {
		return RunEvent{}, ErrConflict
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return RunEvent{}, err
	}
	if err := jsondb.WriteJSONFile(path, rec); err != nil {
		return RunEvent{}, err
	}
	return runEventFromRecord(rec), nil
}

func (s *JSONStore) ListRunEvents(ctx context.Context, p ListRunEventsParams) ([]RunEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	runID := strings.TrimSpace(p.RunID)
	if runID == "" {
		return nil, errors.New("runId is required")
	}
	run, err := s.readRun(runID)
	if err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 500
	}
	entries, err := os.ReadDir(s.runEventsDir(run.ThreadID, run.ID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []RunEvent{}, nil
		}
		return nil, err
	}
	type row struct {
		seq  int64
		path string
	}
	rows := make([]row, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		seq, ok := parseRunEventFileName(entry.Name())
		if !ok || seq <= p.AfterSeq {
			continue
		}
		rows = append(rows, row{seq: seq, path: filepath.Join(s.runEventsDir(run.ThreadID, run.ID), entry.Name())})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	out := make([]RunEvent, 0, len(rows))
	for _, row := range rows {
		rec, err := s.readRunEventFile(row.path)
		if err != nil {
			return nil, err
		}
		out = append(out, runEventFromRecord(rec))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *JSONStore) LatestRunEventSeq(ctx context.Context, runID string) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return 0, errors.New("runId is required")
	}
	run, err := s.readRun(runID)
	if err != nil {
		return 0, err
	}
	lock := s.threadMu(run.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	run, err = s.readRun(runID)
	if err != nil {
		return 0, err
	}
	return s.latestRunEventSeqLocked(run.ThreadID, run.ID)
}

func (s *JSONStore) CreateApprovalRequest(ctx context.Context, approval ApprovalRequest) (ApprovalRequest, error) {
	select {
	case <-ctx.Done():
		return ApprovalRequest{}, ctx.Err()
	default:
	}
	prepared, err := prepareApprovalRequest(approval)
	if err != nil {
		return ApprovalRequest{}, err
	}
	if _, err := s.readThread(prepared.ThreadID); err != nil {
		return ApprovalRequest{}, err
	}
	if strings.TrimSpace(prepared.RunID) != "" {
		run, err := s.readRun(prepared.RunID)
		if err != nil {
			return ApprovalRequest{}, err
		}
		if run.ThreadID != prepared.ThreadID {
			return ApprovalRequest{}, errors.New("approval run_id does not match thread_id")
		}
	}
	lock := s.threadMu(prepared.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	if err := os.MkdirAll(s.approvalsDir(prepared.ThreadID), 0o700); err != nil {
		return ApprovalRequest{}, err
	}
	path := s.approvalPath(prepared.ThreadID, prepared.ID)
	if _, err := os.Stat(path); err == nil {
		return ApprovalRequest{}, ErrConflict
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return ApprovalRequest{}, err
	}
	rec := approvalRequestToRecord(prepared)
	if err := validateApprovalRequestRecord(rec); err != nil {
		return ApprovalRequest{}, err
	}
	if err := jsondb.WriteJSONFile(path, rec); err != nil {
		return ApprovalRequest{}, err
	}
	return approvalRequestFromRecord(rec), nil
}

func (s *JSONStore) GetApprovalRequest(ctx context.Context, approvalID string) (ApprovalRequest, error) {
	select {
	case <-ctx.Done():
		return ApprovalRequest{}, ctx.Err()
	default:
	}
	rec, _, err := s.readApprovalRequest(approvalID)
	if err != nil {
		return ApprovalRequest{}, err
	}
	return approvalRequestFromRecord(rec), nil
}

func (s *JSONStore) ListApprovalRequests(ctx context.Context, filter ApprovalFilter) ([]ApprovalRequest, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 200
	}
	threadIDs := []string{}
	if tid := strings.TrimSpace(filter.ThreadID); tid != "" {
		threadIDs = append(threadIDs, tid)
	} else {
		entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []ApprovalRequest{}, nil
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				threadIDs = append(threadIDs, entry.Name())
			}
		}
	}
	status := strings.TrimSpace(strings.ToLower(filter.Status))
	runID := strings.TrimSpace(filter.RunID)
	out := []ApprovalRequest{}
	for _, tid := range threadIDs {
		entries, err := os.ReadDir(s.approvalsDir(tid))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			rec, err := s.readApprovalRequestFile(filepath.Join(s.approvalsDir(tid), entry.Name()))
			if err != nil {
				return nil, err
			}
			if status != "" && strings.TrimSpace(strings.ToLower(rec.Status)) != status {
				continue
			}
			if runID != "" && strings.TrimSpace(rec.RunID) != runID {
				continue
			}
			out = append(out, approvalRequestFromRecord(rec))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) DecideApprovalRequest(ctx context.Context, approvalID string, decision ApprovalDecision, status string) (ApprovalRequest, error) {
	select {
	case <-ctx.Done():
		return ApprovalRequest{}, ctx.Err()
	default:
	}
	rec, path, err := s.readApprovalRequest(approvalID)
	if err != nil {
		return ApprovalRequest{}, err
	}
	lock := s.threadMu(rec.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	rec, err = s.readApprovalRequestFile(path)
	if err != nil {
		return ApprovalRequest{}, err
	}
	st := strings.TrimSpace(strings.ToLower(status))
	switch st {
	case "approved", "rejected", "expired", "canceled":
	default:
		return ApprovalRequest{}, errors.New("approval status must be approved, rejected, expired, or canceled")
	}
	if strings.TrimSpace(strings.ToLower(rec.Status)) != "pending" {
		return approvalRequestFromRecord(rec), nil
	}
	now := nowUTC()
	decision.Action = strings.TrimSpace(strings.ToLower(decision.Action))
	if decision.Scope == "" {
		decision.Scope = "once"
	}
	decision.Scope = strings.TrimSpace(strings.ToLower(decision.Scope))
	if decision.Subject != nil {
		subject := clonePermissionSubject(*decision.Subject)
		decision.Subject = &subject
	}
	if decision.DecidedAt.IsZero() {
		decision.DecidedAt = now
	}
	rec.Status = st
	rec.Decision = &decision
	rec.UpdatedAt = now
	if err := validateApprovalRequestRecord(rec); err != nil {
		return ApprovalRequest{}, err
	}
	if err := jsondb.WriteJSONFile(path, rec); err != nil {
		return ApprovalRequest{}, err
	}
	return approvalRequestFromRecord(rec), nil
}

func (s *JSONStore) CancelRunApprovalRequests(ctx context.Context, runID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	rid := strings.TrimSpace(runID)
	if rid == "" {
		return errors.New("runId is required")
	}
	run, err := s.readRun(rid)
	if err != nil {
		return err
	}
	lock := s.threadMu(run.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	return s.updateApprovalRequestsLocked(run.ThreadID, func(rec *approvalRequestRecord) bool {
		return strings.TrimSpace(rec.RunID) == rid && strings.TrimSpace(strings.ToLower(rec.Status)) == "pending"
	}, func(rec *approvalRequestRecord) {
		now := nowUTC()
		rec.Status = "canceled"
		rec.Decision = &ApprovalDecision{Action: "cancel", Scope: "once", DecidedAt: now}
		rec.UpdatedAt = now
	})
}

func (s *JSONStore) ExpireStaleApprovalRequests(ctx context.Context, now time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if now.IsZero() {
		now = nowUTC()
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		tid := entry.Name()
		lock := s.threadMu(tid)
		lock.Lock()
		err := s.updateApprovalRequestsLocked(tid, func(rec *approvalRequestRecord) bool {
			return strings.TrimSpace(strings.ToLower(rec.Status)) == "pending" && !rec.ExpiresAt.IsZero() && !rec.ExpiresAt.After(now)
		}, func(rec *approvalRequestRecord) {
			rec.Status = "expired"
			rec.Decision = &ApprovalDecision{Action: "expire", Scope: "once", DecidedAt: now}
			rec.UpdatedAt = now
		})
		lock.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONStore) UpsertLocalSearchJob(ctx context.Context, job LocalSearchJob) (LocalSearchJob, error) {
	select {
	case <-ctx.Done():
		return LocalSearchJob{}, ctx.Err()
	default:
	}
	prepared, err := prepareLocalSearchJob(job)
	if err != nil {
		return LocalSearchJob{}, err
	}
	if _, err := s.readThread(prepared.ThreadID); err != nil {
		return LocalSearchJob{}, err
	}
	if strings.TrimSpace(prepared.RunID) != "" {
		run, err := s.readRun(prepared.RunID)
		if err != nil {
			return LocalSearchJob{}, err
		}
		if run.ThreadID != prepared.ThreadID {
			return LocalSearchJob{}, errors.New("local_search_job run_id does not match thread_id")
		}
	}
	lock := s.threadMu(prepared.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	if err := os.MkdirAll(s.localSearchJobsDir(prepared.ThreadID), 0o700); err != nil {
		return LocalSearchJob{}, err
	}
	rec := localSearchJobToRecord(prepared)
	if err := validateLocalSearchJobRecord(rec); err != nil {
		return LocalSearchJob{}, err
	}
	if err := jsondb.WriteJSONFile(s.localSearchJobPath(prepared.ThreadID, prepared.ID), rec); err != nil {
		return LocalSearchJob{}, err
	}
	return localSearchJobFromRecord(rec), nil
}

func (s *JSONStore) GetLocalSearchJob(ctx context.Context, jobID string) (LocalSearchJob, error) {
	select {
	case <-ctx.Done():
		return LocalSearchJob{}, ctx.Err()
	default:
	}
	rec, _, err := s.readLocalSearchJob(jobID)
	if err != nil {
		return LocalSearchJob{}, err
	}
	return localSearchJobFromRecord(rec), nil
}

func (s *JSONStore) ListLocalSearchJobs(ctx context.Context, filter LocalSearchJobFilter) ([]LocalSearchJob, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	threadIDs := []string{}
	if tid := strings.TrimSpace(filter.ThreadID); tid != "" {
		threadIDs = append(threadIDs, tid)
	} else {
		entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []LocalSearchJob{}, nil
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() {
				threadIDs = append(threadIDs, entry.Name())
			}
		}
	}
	runID := strings.TrimSpace(filter.RunID)
	out := []LocalSearchJob{}
	for _, tid := range threadIDs {
		entries, err := os.ReadDir(s.localSearchJobsDir(tid))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			rec, err := s.readLocalSearchJobFile(filepath.Join(s.localSearchJobsDir(tid), entry.Name()))
			if err != nil {
				return nil, err
			}
			if runID != "" && strings.TrimSpace(rec.RunID) != runID {
				continue
			}
			out = append(out, localSearchJobFromRecord(rec))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) GetActiveContextCheckpoint(ctx context.Context, threadID string) (*ContextCheckpoint, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return nil, err
	}
	rec, err := s.readContextCheckpointFile(s.contextCheckpointActivePath(tid))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	cp := contextCheckpointFromRecord(rec)
	return &cp, nil
}

func (s *JSONStore) UpsertActiveContextCheckpoint(ctx context.Context, checkpoint ContextCheckpoint) (ContextCheckpoint, error) {
	select {
	case <-ctx.Done():
		return ContextCheckpoint{}, ctx.Err()
	default:
	}
	cp, err := prepareContextCheckpoint(checkpoint)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	lock := s.threadMu(cp.ThreadID)
	lock.Lock()
	defer lock.Unlock()

	if _, err := s.readThread(cp.ThreadID); err != nil {
		return ContextCheckpoint{}, err
	}
	rec := contextCheckpointToRecord(cp)
	if err := validateContextCheckpointRecord(rec); err != nil {
		return ContextCheckpoint{}, err
	}
	active, activeErr := s.readContextCheckpointFile(s.contextCheckpointActivePath(cp.ThreadID))
	if activeErr != nil && !errors.Is(activeErr, fs.ErrNotExist) {
		return ContextCheckpoint{}, activeErr
	}
	if activeErr == nil && active.ID == rec.ID && active.SourceMessageSeq > rec.SourceMessageSeq {
		return contextCheckpointFromRecord(active), nil
	}
	if err := jsondb.WriteJSONFile(s.contextCheckpointHistoryPath(cp.ThreadID, cp.ID), rec); err != nil {
		return ContextCheckpoint{}, err
	}
	if activeErr == nil && active.SourceMessageSeq > rec.SourceMessageSeq {
		return contextCheckpointFromRecord(active), nil
	}
	if err := jsondb.WriteJSONFile(s.contextCheckpointActivePath(cp.ThreadID), rec); err != nil {
		return ContextCheckpoint{}, err
	}
	return contextCheckpointFromRecord(rec), nil
}

func (s *JSONStore) ListContextCheckpoints(ctx context.Context, threadID string, limit int) ([]ContextCheckpoint, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	dir := s.contextCheckpointsDir(tid)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		active, err := s.GetActiveContextCheckpoint(ctx, tid)
		if err != nil || active == nil {
			return nil, err
		}
		return []ContextCheckpoint{*active}, nil
	}
	recs := make([]contextCheckpointRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := s.readContextCheckpointFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	if len(recs) == 0 {
		active, err := s.GetActiveContextCheckpoint(ctx, tid)
		if err != nil || active == nil {
			return nil, err
		}
		return []ContextCheckpoint{*active}, nil
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].ID > recs[j].ID
		}
		return recs[i].CreatedAt.After(recs[j].CreatedAt)
	})
	if len(recs) > limit {
		recs = recs[:limit]
	}
	out := make([]ContextCheckpoint, 0, len(recs))
	for _, rec := range recs {
		out = append(out, contextCheckpointFromRecord(rec))
	}
	return out, nil
}

func (s *JSONStore) AppendMessage(ctx context.Context, threadID string, runID *string, msg Message) (Message, error) {
	select {
	case <-ctx.Done():
		return Message{}, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return Message{}, errors.New("threadId is required")
	}
	lock := s.threadMu(tid)
	lock.Lock()
	defer lock.Unlock()

	thread, err := s.readThread(tid)
	if err != nil {
		return Message{}, err
	}
	if runID != nil && strings.TrimSpace(*runID) != "" {
		if _, err := s.readRun(strings.TrimSpace(*runID)); err != nil {
			return Message{}, err
		}
	}
	role := strings.TrimSpace(string(msg.Role))
	if role == "" {
		return Message{}, errors.New("role is required")
	}
	id := strings.TrimSpace(msg.ID)
	if id == "" {
		id = types.NewUUID()
	}
	seq := thread.Counters.NextSeq
	if seq <= 0 {
		seq = 1
	}
	now := nowUTC()
	threadPrev := thread
	thread.Counters.NextSeq = seq + 1
	thread.Counters.MessageCount++
	thread.UpdatedAt = now
	thread.LastMessageAt = &now

	if err := s.writeThread(thread); err != nil {
		return Message{}, err
	}

	rec := jsondb.Message{
		Kind:                  jsondb.KindMessage,
		SchemaVersion:         jsondb.SchemaVersion,
		ID:                    id,
		ThreadID:              tid,
		RunID:                 trimPtr(runID),
		Seq:                   seq,
		ReferencedMessageIDs:  dedupeStrings(msg.ReferencedMessageIDs),
		ReferencedThreadIDs:   dedupeStrings(msg.ReferencedThreadIDs),
		ReferencedArtifactIDs: dedupeStrings(msg.ReferencedArtifactIDs),
		ContextSummary:        strings.TrimSpace(msg.ContextSummary),
		ContextPlan:           cloneRawJSON(msg.ContextPlan),
		HiddenContext:         msg.HiddenContext,
		Role:                  msg.Role,
		Content:               msg.Content,
		ProviderPayload:       cloneRawJSON(msg.ProviderPayload),
		Thinking:              msg.Thinking,
		ThinkingComplete:      msg.ThinkingComplete,
		ToolCalls:             msg.ToolCalls,
		ToolCallID:            trimPtr(msg.ToolCallID),
		Name:                  trimPtr(msg.Name),
		UI: jsondb.MessageUI{
			Hidden:                msg.UIHidden,
			ReferencedMessageIDs:  dedupeStrings(msg.ReferencedMessageIDs),
			ReferencedThreadIDs:   dedupeStrings(msg.ReferencedThreadIDs),
			ReferencedArtifactIDs: dedupeStrings(msg.ReferencedArtifactIDs),
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := validateMessageRecord(rec); err != nil {
		_ = s.writeThread(threadPrev)
		return Message{}, err
	}
	p, err := jsondb.MessagePath(s.cfgDir, tid, seq, id)
	if err != nil {
		_ = s.writeThread(threadPrev)
		return Message{}, err
	}
	if err := jsondb.WriteJSONFile(p, rec); err != nil {
		_ = s.writeThread(threadPrev)
		return Message{}, err
	}
	s.mu.Lock()
	s.msgToThread[id] = tid
	s.mu.Unlock()

	msg.ID = id
	msg.ThreadID = tid
	msg.RunID = trimPtr(runID)
	msg.Seq = seq
	msg.CreatedAt = now
	msg.UpdatedAt = now
	return msg, nil
}

func (s *JSONStore) UpdateMessage(ctx context.Context, messageID string, fields map[string]any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	rec, p, err := s.readMessageWithPath(messageID)
	if err != nil {
		return err
	}
	for key, val := range fields {
		switch key {
		case "content":
			if v, ok := anyString(val); ok {
				rec.Content = &v
			}
		case "referenced_artifact_ids":
			if v, ok := anyStringSlice(val); ok {
				rec.ReferencedArtifactIDs = dedupeStrings(v)
				rec.UI.ReferencedArtifactIDs = dedupeStrings(v)
			}
		case "referenced_message_ids":
			if v, ok := anyStringSlice(val); ok {
				rec.ReferencedMessageIDs = dedupeStrings(v)
				rec.UI.ReferencedMessageIDs = dedupeStrings(v)
			}
		case "referenced_thread_ids":
			if v, ok := anyStringSlice(val); ok {
				rec.ReferencedThreadIDs = dedupeStrings(v)
				rec.UI.ReferencedThreadIDs = dedupeStrings(v)
			}
		case "context_summary":
			if v, ok := anyString(val); ok {
				rec.ContextSummary = strings.TrimSpace(v)
			}
		case "context_plan":
			if v, ok := val.([]byte); ok {
				rec.ContextPlan = cloneRawJSON(v)
			} else if v, ok := val.(string); ok {
				rec.ContextPlan = cloneRawJSON(json.RawMessage(strings.TrimSpace(v)))
			} else if v, ok := val.(json.RawMessage); ok {
				rec.ContextPlan = cloneRawJSON(v)
			} else {
				if b, err := json.Marshal(v); err == nil {
					rec.ContextPlan = b
				}
			}
		case "hidden_context":
			if v, ok := anyString(val); ok {
				rec.HiddenContext = &v
			}
		case "thinking":
			if v, ok := anyString(val); ok {
				rec.Thinking = &v
			}
		case "provider_payload":
			switch v := val.(type) {
			case []byte:
				rec.ProviderPayload = cloneRawJSON(v)
			case json.RawMessage:
				rec.ProviderPayload = cloneRawJSON(v)
			case string:
				rec.ProviderPayload = cloneRawJSON(json.RawMessage(strings.TrimSpace(v)))
			default:
				if b, err := json.Marshal(v); err == nil {
					rec.ProviderPayload = cloneRawJSON(b)
				}
			}
		case "thinking_complete":
			if v, ok := anyBool(val); ok {
				rec.ThinkingComplete = v
			}
		case "tool_calls":
			if v, ok := val.([]types.ToolCall); ok {
				rec.ToolCalls = v
			} else if arr, ok := val.([]any); ok {
				b, _ := json.Marshal(arr)
				var calls []types.ToolCall
				if err := json.Unmarshal(b, &calls); err == nil {
					rec.ToolCalls = calls
				}
			}
		case "tool_call_id":
			if v, ok := anyString(val); ok {
				rec.ToolCallID = &v
			}
		case "name":
			if v, ok := anyString(val); ok {
				rec.Name = &v
			}
		case "ui_hidden":
			if v, ok := anyBool(val); ok {
				rec.UI.Hidden = v
			}
		}
	}
	rec.UpdatedAt = nowUTC()
	if err := validateMessageRecord(rec); err != nil {
		return err
	}
	return jsondb.WriteJSONFile(p, rec)
}

func (s *JSONStore) ListMessages(ctx context.Context, p ListMessagesParams) ([]Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(p.ThreadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return nil, err
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	includeHidden := p.IncludeHidden
	orderBy := strings.ToLower(strings.TrimSpace(p.OrderBy))
	if orderBy == "" {
		orderBy = "seq_asc"
	}
	messageIDs := dedupeStrings(p.MessageIDs)
	artifactIDs := dedupeStrings(p.ArtifactIDs)
	beforeSeq := p.BeforeSeq
	afterSeq := p.AfterSeq
	beforeTS := p.BeforeTS
	afterTS := p.AfterTS
	beforeSeqSet := beforeSeq
	afterSeqSet := afterSeq

	beforeSeen := make(map[string]struct{}, len(messageIDs))
	for _, id := range messageIDs {
		beforeSeen[id] = struct{}{}
	}
	msgDir, err := jsondb.MessagesDir(s.cfgDir, tid)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(msgDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	type row struct {
		seq  int64
		path string
	}
	rows := make([]row, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		seq, _, ok := parseMessageFileName(entry.Name())
		if !ok {
			continue
		}
		if beforeSeqSet != nil && seq >= *beforeSeqSet {
			continue
		}
		if afterSeqSet != nil && seq <= *afterSeqSet {
			continue
		}
		rows = append(rows, row{seq: seq, path: filepath.Join(msgDir, entry.Name())})
	}
	switch orderBy {
	case "seq_desc":
		sort.Slice(rows, func(i, j int) bool { return rows[i].seq > rows[j].seq })
	default:
		sort.Slice(rows, func(i, j int) bool { return rows[i].seq < rows[j].seq })
	}
	out := make([]Message, 0, len(rows))
	for _, r := range rows {
		rec, err := s.readMessageFile(r.path)
		if err != nil {
			return nil, err
		}
		m := messageFromRecord(rec)
		if !includeHidden && m.UIHidden {
			continue
		}
		if len(messageIDs) > 0 {
			if _, ok := beforeSeen[m.ID]; !ok {
				continue
			}
		}
		if len(artifactIDs) > 0 {
			if !hasAnyOverlap(m.ReferencedArtifactIDs, artifactIDs) {
				continue
			}
		}
		if beforeTS != nil && !m.CreatedAt.IsZero() && m.CreatedAt.After(*beforeTS) {
			continue
		}
		if afterTS != nil && !m.CreatedAt.IsZero() && m.CreatedAt.Before(*afterTS) {
			continue
		}
		out = append(out, m)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

func (s *JSONStore) InsertArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	select {
	case <-ctx.Done():
		return Artifact{}, ctx.Err()
	default:
	}
	if strings.TrimSpace(a.ID) == "" {
		a.ID = types.NewUUID()
	}
	tid := strings.TrimSpace(a.ThreadID)
	if tid == "" {
		return Artifact{}, errors.New("threadId is required")
	}
	if strings.TrimSpace(a.Type) == "" {
		return Artifact{}, errors.New("artifact type is required")
	}
	if strings.TrimSpace(a.Title) == "" {
		a.Title = "Artifact"
	}
	if strings.TrimSpace(a.Storage) == "" {
		a.Storage = "mysql"
	}
	lock := s.threadMu(tid)
	lock.Lock()
	defer lock.Unlock()
	thread, err := s.readThread(tid)
	if err != nil {
		return Artifact{}, err
	}
	if a.MessageID != nil && strings.TrimSpace(*a.MessageID) != "" {
		msgRec, _, err := s.readMessageWithPath(*a.MessageID)
		if err != nil {
			return Artifact{}, err
		}
		if msgRec.ThreadID != tid {
			return Artifact{}, errors.New("artifact message/thread mismatch")
		}
	}
	now := nowUTC()
	rec := jsondb.Artifact{
		Kind:          jsondb.KindArtifact,
		SchemaVersion: jsondb.SchemaVersion,
		ID:            a.ID,
		ThreadID:      tid,
		MessageID:     trimPtr(a.MessageID),
		Type:          strings.TrimSpace(a.Type),
		Title:         a.Title,
		Language:      trimPtr(a.Language),
		ToolName:      trimPtr(a.ToolName),
		ToolCallID:    trimPtr(a.ToolCallID),
		OK:            a.OK,
		Content: jsondb.ArtifactContent{
			Storage:  storageToContentStorage(a.Storage),
			Text:     a.ContentText,
			Path:     trimPtr(a.ContentPath),
			ByteSize: a.ByteSize,
		},
		Metadata:  a.Metadata,
		CreatedAt: now,
	}
	if err := validateArtifactRecord(rec); err != nil {
		return Artifact{}, err
	}
	p, err := jsondb.ArtifactPath(s.cfgDir, tid, a.ID)
	if err != nil {
		return Artifact{}, err
	}
	if _, err := os.Stat(p); err == nil {
		return Artifact{}, ErrConflict
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Artifact{}, err
	}
	if err := jsondb.WriteJSONFile(p, rec); err != nil {
		return Artifact{}, err
	}
	thread.Counters.ArtifactCount++
	if err := s.writeThread(thread); err != nil {
		return Artifact{}, err
	}
	s.mu.Lock()
	s.artToThread[a.ID] = tid
	s.mu.Unlock()
	out := artifactFromRecord(rec)
	return out, nil
}

func (s *JSONStore) ReplaceArtifact(ctx context.Context, a Artifact) (Artifact, error) {
	select {
	case <-ctx.Done():
		return Artifact{}, ctx.Err()
	default:
	}
	id := strings.TrimSpace(a.ID)
	if id == "" {
		return Artifact{}, errors.New("artifactId is required")
	}
	tid := strings.TrimSpace(a.ThreadID)
	if tid == "" {
		return Artifact{}, errors.New("threadId is required")
	}
	if strings.TrimSpace(a.Type) == "" {
		return Artifact{}, errors.New("artifact type is required")
	}
	if strings.TrimSpace(a.Title) == "" {
		a.Title = "Artifact"
	}
	if strings.TrimSpace(a.Storage) == "" {
		a.Storage = "mysql"
	}
	lock := s.threadMu(tid)
	lock.Lock()
	defer lock.Unlock()
	existing, err := s.readArtifact(id)
	if err != nil {
		return Artifact{}, err
	}
	if existing.ThreadID != tid {
		return Artifact{}, errors.New("artifact thread mismatch")
	}
	rec := jsondb.Artifact{
		Kind:          jsondb.KindArtifact,
		SchemaVersion: jsondb.SchemaVersion,
		ID:            id,
		ThreadID:      tid,
		MessageID:     trimPtr(a.MessageID),
		Type:          strings.TrimSpace(a.Type),
		Title:         a.Title,
		Language:      trimPtr(a.Language),
		ToolName:      trimPtr(a.ToolName),
		ToolCallID:    trimPtr(a.ToolCallID),
		OK:            a.OK,
		Content: jsondb.ArtifactContent{
			Storage:  storageToContentStorage(a.Storage),
			Text:     a.ContentText,
			Path:     trimPtr(a.ContentPath),
			ByteSize: a.ByteSize,
		},
		Metadata:  a.Metadata,
		CreatedAt: existing.CreatedAt,
	}
	if err := validateArtifactRecord(rec); err != nil {
		return Artifact{}, err
	}
	p, err := jsondb.ArtifactPath(s.cfgDir, tid, id)
	if err != nil {
		return Artifact{}, err
	}
	if err := jsondb.WriteJSONFile(p, rec); err != nil {
		return Artifact{}, err
	}
	return artifactFromRecord(rec), nil
}

func (s *JSONStore) GetArtifact(ctx context.Context, artifactID string) (Artifact, error) {
	select {
	case <-ctx.Done():
		return Artifact{}, ctx.Err()
	default:
	}
	rec, err := s.readArtifact(artifactID)
	if err != nil {
		return Artifact{}, err
	}
	return artifactFromRecord(rec), nil
}

func (s *JSONStore) DeleteArtifact(ctx context.Context, artifactID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return errors.New("artifactId is required")
	}
	rec, err := s.readArtifact(id)
	if err != nil {
		return err
	}
	tid := strings.TrimSpace(rec.ThreadID)
	lock := s.threadMu(tid)
	lock.Lock()
	defer lock.Unlock()

	rec, err = s.readArtifact(id)
	if err != nil {
		return err
	}
	p, err := jsondb.ArtifactPath(s.cfgDir, tid, id)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	if thread, err := s.readThread(tid); err == nil {
		if thread.Counters.ArtifactCount > 0 {
			thread.Counters.ArtifactCount--
			if writeErr := s.writeThread(thread); writeErr != nil {
				return writeErr
			}
		}
	}
	s.mu.Lock()
	delete(s.artToThread, id)
	s.mu.Unlock()
	return nil
}

func (s *JSONStore) ListArtifacts(ctx context.Context, threadID string, limit int) ([]Artifact, error) {
	return s.listArtifacts(ctx, threadID, limit, "")
}

func (s *JSONStore) ListFileArtifacts(ctx context.Context, threadID string, limit int) ([]Artifact, error) {
	return s.listArtifacts(ctx, threadID, limit, "file")
}

func (s *JSONStore) ListArtifactsPage(ctx context.Context, p ListArtifactsPageParams) (ArtifactPage, error) {
	select {
	case <-ctx.Done():
		return ArtifactPage{}, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(p.ThreadID)
	limit := p.Limit
	if limit <= 0 {
		limit = 100
	}
	var recs []jsondb.Artifact
	if tid != "" {
		var err error
		recs, err = s.listArtifactRecordsForThread(tid)
		if err != nil {
			return ArtifactPage{}, err
		}
	} else {
		entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return ArtifactPage{}, nil
			}
			return ArtifactPage{}, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			threadRecs, err := s.listArtifactRecordsForThread(entry.Name())
			if err != nil {
				return ArtifactPage{}, err
			}
			recs = append(recs, threadRecs...)
		}
	}
	typeFilter := strings.TrimSpace(p.Type)
	if typeFilter != "" {
		filtered := recs[:0]
		for _, rec := range recs {
			if strings.EqualFold(strings.TrimSpace(rec.Type), typeFilter) {
				filtered = append(filtered, rec)
			}
		}
		recs = filtered
	}
	sort.SliceStable(recs, func(i, j int) bool {
		if !recs[i].CreatedAt.Equal(recs[j].CreatedAt) {
			return recs[i].CreatedAt.After(recs[j].CreatedAt)
		}
		return recs[i].ID > recs[j].ID
	})
	if p.BeforeCreatedAt != nil {
		before := p.BeforeCreatedAt.UTC()
		beforeID := strings.TrimSpace(p.BeforeID)
		filtered := recs[:0]
		for _, rec := range recs {
			recCreatedAt := rec.CreatedAt.UTC()
			if recCreatedAt.Before(before) || (recCreatedAt.Equal(before) && strings.TrimSpace(rec.ID) < beforeID) {
				filtered = append(filtered, rec)
			}
		}
		recs = filtered
	}
	hasMore := len(recs) > limit
	if hasMore {
		recs = recs[:limit]
	}
	out := make([]Artifact, 0, len(recs))
	for _, rec := range recs {
		out = append(out, artifactFromRecord(rec))
	}
	return ArtifactPage{Artifacts: out, HasMore: hasMore}, nil
}

func (s *JSONStore) listArtifacts(ctx context.Context, threadID string, limit int, typeFilter string) ([]Artifact, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	tid := strings.TrimSpace(threadID)
	if limit <= 0 {
		limit = 100
	}
	var recs []jsondb.Artifact
	if tid != "" {
		var err error
		recs, err = s.listArtifactRecordsForThread(tid)
		if err != nil {
			return nil, err
		}
	} else {
		entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			threadRecs, err := s.listArtifactRecordsForThread(entry.Name())
			if err != nil {
				return nil, err
			}
			recs = append(recs, threadRecs...)
		}
	}
	sort.Slice(recs, func(i, j int) bool { return recs[i].CreatedAt.Before(recs[j].CreatedAt) })
	if strings.TrimSpace(typeFilter) != "" {
		filtered := recs[:0]
		for _, rec := range recs {
			if strings.EqualFold(strings.TrimSpace(rec.Type), strings.TrimSpace(typeFilter)) {
				filtered = append(filtered, rec)
			}
		}
		recs = filtered
	}
	if len(recs) > limit {
		recs = recs[len(recs)-limit:]
	}
	out := make([]Artifact, 0, len(recs))
	for _, rec := range recs {
		out = append(out, artifactFromRecord(rec))
	}
	return out, nil
}

func (s *JSONStore) listArtifactRecordsForThread(threadID string) ([]jsondb.Artifact, error) {
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return nil, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return nil, err
	}
	artDir, err := jsondb.ArtifactsDir(s.cfgDir, tid)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(artDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	recs := make([]jsondb.Artifact, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var rec jsondb.Artifact
		if err := readJSONLoose(filepath.Join(artDir, entry.Name()), &rec); err != nil {
			return nil, err
		}
		if err := validateArtifactRecord(rec); err != nil {
			return nil, err
		}
		recs = append(recs, rec)
	}
	return recs, nil
}

func (s *JSONStore) CreateProject(ctx context.Context, name string, rootRelPath string) (Project, error) {
	select {
	case <-ctx.Done():
		return Project{}, ctx.Err()
	default:
	}
	id := types.NewUUID()
	now := nowUTC()
	nm := strings.TrimSpace(name)
	if nm == "" {
		nm = "Project"
	}
	root := strings.TrimSpace(rootRelPath)
	if root == "" {
		return Project{}, errors.New("rootRelPath is required")
	}
	p := jsondb.Project{
		Kind:          jsondb.KindProject,
		SchemaVersion: jsondb.SchemaVersion,
		ID:            id,
		Name:          nm,
		RootRelPath:   root,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := validateProjectRecord(p); err != nil {
		return Project{}, err
	}
	if err := os.MkdirAll(s.projectDir(id), 0o700); err != nil {
		return Project{}, err
	}
	if err := jsondb.WriteJSONFile(s.projectPath(id), p); err != nil {
		return Project{}, err
	}
	if err := jsondb.WriteJSONFile(s.projectFilesPath(id), projectFilesEnvelope{Files: []jsondb.ProjectFile{}}); err != nil {
		return Project{}, err
	}
	return projectFromRecord(p), nil
}

func (s *JSONStore) ListProjects(ctx context.Context, limit int) ([]Project, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if limit <= 0 {
		limit = 50
	}
	entries, err := os.ReadDir(s.projectsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	out := make([]Project, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		rec, err := s.readProject(entry.Name())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, projectFromRecord(rec))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *JSONStore) GetProject(ctx context.Context, projectID string) (Project, error) {
	select {
	case <-ctx.Done():
		return Project{}, ctx.Err()
	default:
	}
	rec, err := s.readProject(projectID)
	if err != nil {
		return Project{}, err
	}
	return projectFromRecord(rec), nil
}

func (s *JSONStore) UpsertProjectFile(ctx context.Context, projectID string, path string, byteSize int64, sha256Bytes []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return errors.New("projectId is required")
	}
	lock := s.projectMu(pid)
	lock.Lock()
	defer lock.Unlock()

	pth := strings.TrimSpace(path)
	if pth == "" {
		return errors.New("path is required")
	}
	project, err := s.readProject(pid)
	if err != nil {
		return err
	}
	env, err := s.readProjectFiles(pid)
	if err != nil {
		return err
	}
	now := nowUTC()
	shaHex := ""
	if len(sha256Bytes) > 0 {
		shaHex = hex.EncodeToString(sha256Bytes)
	}
	updated := false
	for idx := range env.Files {
		if env.Files[idx].Path == pth {
			env.Files[idx].ByteSize = byteSize
			env.Files[idx].UpdatedAt = now
			if shaHex == "" {
				env.Files[idx].SHA256 = nil
			} else {
				env.Files[idx].SHA256 = &shaHex
			}
			updated = true
			break
		}
	}
	if !updated {
		rec := jsondb.ProjectFile{
			Kind:          jsondb.KindProjectFile,
			SchemaVersion: jsondb.SchemaVersion,
			ProjectID:     pid,
			Path:          pth,
			ByteSize:      byteSize,
			UpdatedAt:     now,
		}
		if shaHex != "" {
			rec.SHA256 = &shaHex
		}
		env.Files = append(env.Files, rec)
	}
	if err := jsondb.WriteJSONFile(s.projectFilesPath(pid), env); err != nil {
		return err
	}
	project.UpdatedAt = now
	return jsondb.WriteJSONFile(s.projectPath(pid), project)
}

func (s *JSONStore) ReplaceProjectFiles(ctx context.Context, projectID string, files []ProjectFileStat) (Project, error) {
	select {
	case <-ctx.Done():
		return Project{}, ctx.Err()
	default:
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return Project{}, errors.New("projectId is required")
	}
	lock := s.projectMu(pid)
	lock.Lock()
	defer lock.Unlock()

	project, err := s.readProject(pid)
	if err != nil {
		return Project{}, err
	}

	now := nowUTC()
	byPath := make(map[string]jsondb.ProjectFile, len(files))
	for _, f := range files {
		pth := strings.TrimSpace(f.Path)
		if pth == "" {
			continue
		}
		size := f.ByteSize
		if size < 0 {
			size = 0
		}
		rec := jsondb.ProjectFile{
			Kind:          jsondb.KindProjectFile,
			SchemaVersion: jsondb.SchemaVersion,
			ProjectID:     pid,
			Path:          pth,
			ByteSize:      size,
			UpdatedAt:     now,
		}
		if len(f.SHA256) > 0 {
			shaHex := hex.EncodeToString(f.SHA256)
			rec.SHA256 = &shaHex
		}
		byPath[pth] = rec
	}

	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	env := projectFilesEnvelope{Files: make([]jsondb.ProjectFile, 0, len(paths))}
	total := int64(0)
	for _, p := range paths {
		rec := byPath[p]
		total += rec.ByteSize
		env.Files = append(env.Files, rec)
	}

	project.FileCount = len(env.Files)
	project.TotalBytes = total
	project.UpdatedAt = now
	if err := jsondb.WriteJSONFile(s.projectFilesPath(pid), env); err != nil {
		return Project{}, err
	}
	if err := jsondb.WriteJSONFile(s.projectPath(pid), project); err != nil {
		return Project{}, err
	}
	return projectFromRecord(project), nil
}

func (s *JSONStore) FinalizeProject(ctx context.Context, projectID string) (Project, error) {
	select {
	case <-ctx.Done():
		return Project{}, ctx.Err()
	default:
	}
	pid := strings.TrimSpace(projectID)
	if pid == "" {
		return Project{}, errors.New("projectId is required")
	}
	lock := s.projectMu(pid)
	lock.Lock()
	defer lock.Unlock()

	project, err := s.readProject(pid)
	if err != nil {
		return Project{}, err
	}
	env, err := s.readProjectFiles(pid)
	if err != nil {
		return Project{}, err
	}
	total := int64(0)
	for _, f := range env.Files {
		total += f.ByteSize
	}
	project.FileCount = len(env.Files)
	project.TotalBytes = total
	project.UpdatedAt = nowUTC()
	if err := jsondb.WriteJSONFile(s.projectPath(pid), project); err != nil {
		return Project{}, err
	}
	return projectFromRecord(project), nil
}

func (s *JSONStore) EnqueueJob(ctx context.Context, kind string, threadID string, runID *string) (Job, error) {
	select {
	case <-ctx.Done():
		return Job{}, ctx.Err()
	default:
	}
	k := strings.TrimSpace(kind)
	if k == "" {
		return Job{}, errors.New("kind is required")
	}
	tid := strings.TrimSpace(threadID)
	if tid == "" {
		return Job{}, errors.New("threadId is required")
	}
	if _, err := s.readThread(tid); err != nil {
		return Job{}, err
	}
	id := types.NewUUID()
	now := nowUTC()
	rec := jsondb.Job{
		Kind:          jsondb.KindJob,
		SchemaVersion: jsondb.SchemaVersion,
		ID:            id,
		KindName:      k,
		ThreadID:      tid,
		RunID:         trimPtr(runID),
		Status:        "queued",
		Attempts:      0,
		NotBefore:     now,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := validateJobRecord(rec); err != nil {
		return Job{}, err
	}
	if err := jsondb.WriteJSONFile(s.jobPath(id), rec); err != nil {
		return Job{}, err
	}
	return jobFromRecord(rec), nil
}

func (s *JSONStore) ClaimNextJob(ctx context.Context, now time.Time) (*Job, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	entries, err := os.ReadDir(s.jobsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var chosen *jsondb.Job
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		rec, err := s.readJob(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			continue
		}
		if rec.Status != "queued" {
			continue
		}
		if rec.NotBefore.After(now) {
			continue
		}
		if chosen == nil || rec.CreatedAt.Before(chosen.CreatedAt) {
			c := rec
			chosen = &c
		}
	}
	if chosen == nil {
		return nil, nil
	}
	chosen.Attempts++
	chosen.Status = "running"
	chosen.UpdatedAt = nowUTC()
	if err := jsondb.WriteJSONFile(s.jobPath(chosen.ID), chosen); err != nil {
		return nil, err
	}
	out := jobFromRecord(*chosen)
	return &out, nil
}

func (s *JSONStore) CompleteJob(ctx context.Context, jobID string) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(jobID)
	if id == "" {
		return errors.New("jobId is required")
	}
	rec, err := s.readJob(id)
	if err != nil {
		return err
	}
	rec.Status = "completed"
	rec.UpdatedAt = nowUTC()
	return jsondb.WriteJSONFile(s.jobPath(id), rec)
}

func (s *JSONStore) FailJob(ctx context.Context, jobID string, errMsg string, notBefore time.Time) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	id := strings.TrimSpace(jobID)
	if id == "" {
		return errors.New("jobId is required")
	}
	rec, err := s.readJob(id)
	if err != nil {
		return err
	}
	msg := strings.TrimSpace(errMsg)
	if msg == "" {
		msg = "unknown error"
	}
	rec.Status = "queued"
	rec.NotBefore = notBefore.UTC()
	rec.LastError = &msg
	rec.UpdatedAt = nowUTC()
	return jsondb.WriteJSONFile(s.jobPath(id), rec)
}

func (s *JSONStore) rebuildIndexes(ctx context.Context) error {
	threadsDir := jsondb.ThreadsDir(s.cfgDir)
	entries, err := os.ReadDir(threadsDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	runToThread := map[string]string{}
	artToThread := map[string]string{}
	msgToThread := map[string]string{}
	planToThread := map[string]string{}
	for _, entry := range entries {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if !entry.IsDir() {
			continue
		}
		threadID := entry.Name()
		runsDir, err := jsondb.RunsDir(s.cfgDir, threadID)
		if err == nil {
			runEntries, _ := os.ReadDir(runsDir)
			for _, re := range runEntries {
				if re.IsDir() || !strings.HasSuffix(re.Name(), ".json") {
					continue
				}
				runID := strings.TrimSuffix(re.Name(), ".json")
				runToThread[runID] = threadID
			}
		}
		artsDir, err := jsondb.ArtifactsDir(s.cfgDir, threadID)
		if err == nil {
			artEntries, _ := os.ReadDir(artsDir)
			for _, ae := range artEntries {
				if ae.IsDir() || !strings.HasSuffix(ae.Name(), ".json") {
					continue
				}
				artID := strings.TrimSuffix(ae.Name(), ".json")
				artToThread[artID] = threadID
			}
		}
		msgDir, err := jsondb.MessagesDir(s.cfgDir, threadID)
		if err == nil {
			msgEntries, _ := os.ReadDir(msgDir)
			for _, me := range msgEntries {
				if me.IsDir() || !strings.HasSuffix(me.Name(), ".json") {
					continue
				}
				_, msgID, ok := parseMessageFileName(me.Name())
				if !ok {
					continue
				}
				msgToThread[msgID] = threadID
			}
		}
		planEntries, _ := os.ReadDir(s.runPlansDir(threadID))
		for _, pe := range planEntries {
			if pe.IsDir() || !strings.HasSuffix(pe.Name(), ".json") {
				continue
			}
			planID := strings.TrimSuffix(pe.Name(), ".json")
			planToThread[planID] = threadID
		}
	}
	s.mu.Lock()
	s.runToThread = runToThread
	s.artToThread = artToThread
	s.msgToThread = msgToThread
	s.planToThread = planToThread
	s.mu.Unlock()
	return nil
}

func (s *JSONStore) initMeta() error {
	p := jsondb.MetaPath(s.cfgDir)
	if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
		now := nowUTC()
		meta := jsondb.Meta{
			Kind:          jsondb.KindMeta,
			SchemaVersion: jsondb.SchemaVersion,
			CreatedAt:     now,
			UpdatedAt:     now,
		}
		return jsondb.WriteJSONFile(p, meta)
	}
	var meta jsondb.Meta
	if err := jsondb.ReadJSONFileStrict(p, &meta); err != nil {
		return err
	}
	if meta.Kind != jsondb.KindMeta {
		return errors.New("chatdb meta kind mismatch")
	}
	if meta.SchemaVersion != jsondb.SchemaVersion {
		return fmt.Errorf("chatdb schema version mismatch: got=%d expected=%d", meta.SchemaVersion, jsondb.SchemaVersion)
	}
	return nil
}

func (s *JSONStore) readThread(threadID string) (jsondb.Thread, error) {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return jsondb.Thread{}, errors.New("threadId is required")
	}
	p, err := jsondb.ThreadPath(s.cfgDir, id)
	if err != nil {
		return jsondb.Thread{}, err
	}
	var rec jsondb.Thread
	if err := readJSONLoose(p, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsondb.Thread{}, ErrNotFound
		}
		return jsondb.Thread{}, err
	}
	if err := validateThreadRecord(rec); err != nil {
		return jsondb.Thread{}, err
	}
	return rec, nil
}

func (s *JSONStore) writeThread(rec jsondb.Thread) error {
	if err := validateThreadRecord(rec); err != nil {
		return err
	}
	p, err := jsondb.ThreadPath(s.cfgDir, rec.ID)
	if err != nil {
		return err
	}
	return jsondb.WriteJSONFile(p, rec)
}

func (s *JSONStore) readRun(runID string) (jsondb.Run, error) {
	rec, _, err := s.readRunWithPath(runID)
	return rec, err
}

func (s *JSONStore) readRunWithPath(runID string) (jsondb.Run, string, error) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return jsondb.Run{}, "", errors.New("runId is required")
	}
	threadID, ok := s.runThread(id)
	if !ok {
		return jsondb.Run{}, "", ErrNotFound
	}
	p, err := jsondb.RunPath(s.cfgDir, threadID, id)
	if err != nil {
		return jsondb.Run{}, "", err
	}
	var rec jsondb.Run
	if err := readJSONLoose(p, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsondb.Run{}, "", ErrNotFound
		}
		return jsondb.Run{}, "", err
	}
	if err := validateRunRecord(rec); err != nil {
		return jsondb.Run{}, "", err
	}
	return rec, p, nil
}

func (s *JSONStore) readRunPlan(planID string) (runPlanRecord, string, error) {
	id := strings.TrimSpace(planID)
	if id == "" {
		return runPlanRecord{}, "", fmt.Errorf("%w: planId is required", ErrInvalid)
	}
	s.mu.RLock()
	threadID := s.planToThread[id]
	s.mu.RUnlock()
	if threadID == "" {
		return runPlanRecord{}, "", ErrNotFound
	}
	p := s.runPlanPath(threadID, id)
	var rec runPlanRecord
	if err := readJSONLoose(p, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return runPlanRecord{}, "", ErrNotFound
		}
		return runPlanRecord{}, "", err
	}
	if rec.Kind != runPlanKind || rec.SchemaVersion != runPlanSchemaVersion {
		return runPlanRecord{}, "", errors.New("run plan record kind or schema mismatch")
	}
	if err := validateRunPlan(rec.RunPlan); err != nil {
		return runPlanRecord{}, "", err
	}
	return rec, p, nil
}

func (s *JSONStore) readMessageWithPath(messageID string) (jsondb.Message, string, error) {
	id := strings.TrimSpace(messageID)
	if id == "" {
		return jsondb.Message{}, "", errors.New("messageId is required")
	}
	threadID, p, ok := s.messagePathByID(id)
	if !ok {
		return jsondb.Message{}, "", ErrNotFound
	}
	var rec jsondb.Message
	if err := readJSONLoose(p, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsondb.Message{}, "", ErrNotFound
		}
		return jsondb.Message{}, "", err
	}
	if err := validateMessageRecord(rec); err != nil {
		return jsondb.Message{}, "", err
	}
	s.mu.Lock()
	s.msgToThread[id] = threadID
	s.mu.Unlock()
	return rec, p, nil
}

func (s *JSONStore) readMessageFile(path string) (jsondb.Message, error) {
	var rec jsondb.Message
	if err := readJSONLoose(path, &rec); err != nil {
		return jsondb.Message{}, err
	}
	if err := validateMessageRecord(rec); err != nil {
		return jsondb.Message{}, err
	}
	return rec, nil
}

func (s *JSONStore) readArtifact(artifactID string) (jsondb.Artifact, error) {
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return jsondb.Artifact{}, errors.New("artifactId is required")
	}
	threadID, ok := s.artThread(id)
	if !ok {
		return jsondb.Artifact{}, ErrNotFound
	}
	p, err := jsondb.ArtifactPath(s.cfgDir, threadID, id)
	if err != nil {
		return jsondb.Artifact{}, err
	}
	var rec jsondb.Artifact
	if err := readJSONLoose(p, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsondb.Artifact{}, ErrNotFound
		}
		return jsondb.Artifact{}, err
	}
	if err := validateArtifactRecord(rec); err != nil {
		return jsondb.Artifact{}, err
	}
	return rec, nil
}

func (s *JSONStore) readProject(projectID string) (jsondb.Project, error) {
	id := strings.TrimSpace(projectID)
	if id == "" {
		return jsondb.Project{}, errors.New("projectId is required")
	}
	var rec jsondb.Project
	if err := readJSONLoose(s.projectPath(id), &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsondb.Project{}, ErrNotFound
		}
		return jsondb.Project{}, err
	}
	if err := validateProjectRecord(rec); err != nil {
		return jsondb.Project{}, err
	}
	return rec, nil
}

func (s *JSONStore) readProjectFiles(projectID string) (projectFilesEnvelope, error) {
	id := strings.TrimSpace(projectID)
	if id == "" {
		return projectFilesEnvelope{}, errors.New("projectId is required")
	}
	var env projectFilesEnvelope
	if err := readJSONLoose(s.projectFilesPath(id), &env); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return projectFilesEnvelope{Files: []jsondb.ProjectFile{}}, nil
		}
		return projectFilesEnvelope{}, err
	}
	if env.Files == nil {
		env.Files = []jsondb.ProjectFile{}
	}
	return env, nil
}

func (s *JSONStore) readJob(jobID string) (jsondb.Job, error) {
	id := strings.TrimSpace(jobID)
	if id == "" {
		return jsondb.Job{}, errors.New("jobId is required")
	}
	var rec jsondb.Job
	if err := readJSONLoose(s.jobPath(id), &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return jsondb.Job{}, ErrNotFound
		}
		return jsondb.Job{}, err
	}
	if err := validateJobRecord(rec); err != nil {
		return jsondb.Job{}, err
	}
	return rec, nil
}

func (s *JSONStore) readContextCheckpointFile(path string) (contextCheckpointRecord, error) {
	var rec contextCheckpointRecord
	if err := readJSONLoose(path, &rec); err != nil {
		return contextCheckpointRecord{}, err
	}
	if err := validateContextCheckpointRecord(rec); err != nil {
		return contextCheckpointRecord{}, err
	}
	return rec, nil
}

func (s *JSONStore) readRunEventFile(path string) (runEventRecord, error) {
	var rec runEventRecord
	if err := readJSONLoose(path, &rec); err != nil {
		return runEventRecord{}, err
	}
	if err := validateRunEventRecord(rec); err != nil {
		return runEventRecord{}, err
	}
	return rec, nil
}

func (s *JSONStore) readApprovalRequest(approvalID string) (approvalRequestRecord, string, error) {
	id := strings.TrimSpace(approvalID)
	if id == "" {
		return approvalRequestRecord{}, "", errors.New("approvalId is required")
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return approvalRequestRecord{}, "", ErrNotFound
		}
		return approvalRequestRecord{}, "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := s.approvalPath(entry.Name(), id)
		rec, err := s.readApprovalRequestFile(path)
		if err == nil {
			return rec, path, nil
		}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotFound) {
			continue
		}
		return approvalRequestRecord{}, "", err
	}
	return approvalRequestRecord{}, "", ErrNotFound
}

func (s *JSONStore) readApprovalRequestFile(path string) (approvalRequestRecord, error) {
	var rec approvalRequestRecord
	if err := readJSONLoose(path, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return approvalRequestRecord{}, ErrNotFound
		}
		return approvalRequestRecord{}, err
	}
	if err := validateApprovalRequestRecord(rec); err != nil {
		return approvalRequestRecord{}, err
	}
	return rec, nil
}

func (s *JSONStore) readLocalSearchJob(jobID string) (localSearchJobRecord, string, error) {
	id := strings.TrimSpace(jobID)
	if id == "" {
		return localSearchJobRecord{}, "", errors.New("jobId is required")
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return localSearchJobRecord{}, "", ErrNotFound
		}
		return localSearchJobRecord{}, "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := s.localSearchJobPath(entry.Name(), id)
		rec, err := s.readLocalSearchJobFile(path)
		if err == nil {
			return rec, path, nil
		}
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, ErrNotFound) {
			continue
		}
		return localSearchJobRecord{}, "", err
	}
	return localSearchJobRecord{}, "", ErrNotFound
}

func (s *JSONStore) readLocalSearchJobFile(path string) (localSearchJobRecord, error) {
	var rec localSearchJobRecord
	if err := readJSONLoose(path, &rec); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return localSearchJobRecord{}, ErrNotFound
		}
		return localSearchJobRecord{}, err
	}
	if err := validateLocalSearchJobRecord(rec); err != nil {
		return localSearchJobRecord{}, err
	}
	return rec, nil
}

func (s *JSONStore) updateApprovalRequestsLocked(threadID string, match func(*approvalRequestRecord) bool, update func(*approvalRequestRecord)) error {
	dir := s.approvalsDir(threadID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		rec, err := s.readApprovalRequestFile(path)
		if err != nil {
			return err
		}
		if match != nil && !match(&rec) {
			continue
		}
		if update != nil {
			update(&rec)
		}
		if err := validateApprovalRequestRecord(rec); err != nil {
			return err
		}
		if err := jsondb.WriteJSONFile(path, rec); err != nil {
			return err
		}
	}
	return nil
}

func (s *JSONStore) latestRunEventSeqLocked(threadID string, runID string) (int64, error) {
	entries, err := os.ReadDir(s.runEventsDir(threadID, runID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	var latest int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		seq, ok := parseRunEventFileName(entry.Name())
		if !ok {
			continue
		}
		if seq > latest {
			latest = seq
		}
	}
	return latest, nil
}

func (s *JSONStore) runThread(runID string) (string, bool) {
	id := strings.TrimSpace(runID)
	if id == "" {
		return "", false
	}
	s.mu.RLock()
	tid, ok := s.runToThread[id]
	s.mu.RUnlock()
	if ok {
		return tid, true
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p, err := jsondb.RunPath(s.cfgDir, entry.Name(), id)
		if err != nil {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			s.mu.Lock()
			s.runToThread[id] = entry.Name()
			s.mu.Unlock()
			return entry.Name(), true
		}
	}
	return "", false
}

func (s *JSONStore) artThread(artifactID string) (string, bool) {
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return "", false
	}
	s.mu.RLock()
	tid, ok := s.artToThread[id]
	s.mu.RUnlock()
	if ok {
		return tid, true
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		p, err := jsondb.ArtifactPath(s.cfgDir, entry.Name(), id)
		if err != nil {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			s.mu.Lock()
			s.artToThread[id] = entry.Name()
			s.mu.Unlock()
			return entry.Name(), true
		}
	}
	return "", false
}

func (s *JSONStore) messagePathByID(messageID string) (string, string, bool) {
	id := strings.TrimSpace(messageID)
	if id == "" {
		return "", "", false
	}
	s.mu.RLock()
	tid, ok := s.msgToThread[id]
	s.mu.RUnlock()
	if ok {
		msgDir, err := jsondb.MessagesDir(s.cfgDir, tid)
		if err == nil {
			matches, _ := filepath.Glob(filepath.Join(msgDir, "*_"+id+".json"))
			if len(matches) > 0 {
				return tid, matches[0], true
			}
		}
	}
	entries, err := os.ReadDir(jsondb.ThreadsDir(s.cfgDir))
	if err != nil {
		return "", "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		msgDir, err := jsondb.MessagesDir(s.cfgDir, entry.Name())
		if err != nil {
			continue
		}
		matches, _ := filepath.Glob(filepath.Join(msgDir, "*_"+id+".json"))
		if len(matches) > 0 {
			s.mu.Lock()
			s.msgToThread[id] = entry.Name()
			s.mu.Unlock()
			return entry.Name(), matches[0], true
		}
	}
	return "", "", false
}

func (s *JSONStore) ensureThreadDirs(threadID string) error {
	runDir, err := jsondb.RunsDir(s.cfgDir, threadID)
	if err != nil {
		return err
	}
	msgDir, err := jsondb.MessagesDir(s.cfgDir, threadID)
	if err != nil {
		return err
	}
	artDir, err := jsondb.ArtifactsDir(s.cfgDir, threadID)
	if err != nil {
		return err
	}
	runEventsDir := filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "run_events")
	approvalsDir := filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "approvals")
	localSearchJobsDir := filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "local_search_jobs")
	runPlansDir := filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "run_plans")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(msgDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(artDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(runEventsDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(approvalsDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(localSearchJobsDir, 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(runPlansDir, 0o700); err != nil {
		return err
	}
	return nil
}

func (s *JSONStore) threadDir(threadID string) (string, error) {
	id := strings.TrimSpace(threadID)
	if id == "" {
		return "", errors.New("threadId is required")
	}
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), id), nil
}

func (s *JSONStore) threadMu(threadID string) *sync.Mutex {
	id := strings.TrimSpace(threadID)
	s.mu.Lock()
	defer s.mu.Unlock()
	lk := s.threadLocks[id]
	if lk == nil {
		lk = &sync.Mutex{}
		s.threadLocks[id] = lk
	}
	return lk
}

func (s *JSONStore) projectMu(projectID string) *sync.Mutex {
	id := strings.TrimSpace(projectID)
	s.mu.Lock()
	defer s.mu.Unlock()
	lk := s.projectLocks[id]
	if lk == nil {
		lk = &sync.Mutex{}
		s.projectLocks[id] = lk
	}
	return lk
}

func (s *JSONStore) projectsDir() string {
	return filepath.Join(jsondb.RootDir(s.cfgDir), "projects")
}

func (s *JSONStore) projectDir(projectID string) string {
	return filepath.Join(s.projectsDir(), strings.TrimSpace(projectID))
}

func (s *JSONStore) projectPath(projectID string) string {
	return filepath.Join(s.projectDir(projectID), "project.json")
}

func (s *JSONStore) projectFilesPath(projectID string) string {
	return filepath.Join(s.projectDir(projectID), "files.json")
}

func (s *JSONStore) contextCheckpointActivePath(threadID string) string {
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "context_checkpoint.json")
}

func (s *JSONStore) contextCheckpointsDir(threadID string) string {
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "context_checkpoints")
}

func (s *JSONStore) contextCheckpointHistoryPath(threadID string, checkpointID string) string {
	return filepath.Join(s.contextCheckpointsDir(threadID), strings.TrimSpace(checkpointID)+".json")
}

func (s *JSONStore) runEventsDir(threadID string, runID string) string {
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "run_events", strings.TrimSpace(runID))
}

func (s *JSONStore) runPlansDir(threadID string) string {
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "run_plans")
}

func (s *JSONStore) runPlanPath(threadID string, planID string) string {
	id := strings.ReplaceAll(strings.TrimSpace(planID), string(filepath.Separator), "_")
	return filepath.Join(s.runPlansDir(threadID), id+".json")
}

func (s *JSONStore) runEventPath(threadID string, runID string, seq int64) string {
	return filepath.Join(s.runEventsDir(threadID, runID), fmt.Sprintf("%020d.json", seq))
}

func (s *JSONStore) approvalsDir(threadID string) string {
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "approvals")
}

func (s *JSONStore) approvalPath(threadID string, approvalID string) string {
	id := strings.TrimSpace(approvalID)
	id = strings.ReplaceAll(id, string(filepath.Separator), "_")
	return filepath.Join(s.approvalsDir(threadID), id+".json")
}

func (s *JSONStore) localSearchJobsDir(threadID string) string {
	return filepath.Join(jsondb.ThreadsDir(s.cfgDir), strings.TrimSpace(threadID), "local_search_jobs")
}

func (s *JSONStore) localSearchJobPath(threadID string, jobID string) string {
	id := strings.TrimSpace(jobID)
	id = strings.ReplaceAll(id, string(filepath.Separator), "_")
	return filepath.Join(s.localSearchJobsDir(threadID), id+".json")
}

func (s *JSONStore) jobsDir() string {
	return filepath.Join(jsondb.RootDir(s.cfgDir), "jobs")
}

func (s *JSONStore) capabilityEvidenceDir() string {
	return filepath.Join(jsondb.RootDir(s.cfgDir), "provider_capability_evidence")
}

func (s *JSONStore) capabilityEvidencePath(evidence ProviderCapabilityEvidence) string {
	key := strings.Join([]string{evidence.ConnectionID, evidence.Model, evidence.Fingerprint, evidence.Probe}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(s.capabilityEvidenceDir(), hex.EncodeToString(sum[:])+".json")
}

func (s *JSONStore) jobPath(jobID string) string {
	return filepath.Join(s.jobsDir(), strings.TrimSpace(jobID)+".json")
}

func readJSONLoose(path string, dst any) error {
	// atomicfile.ReadFile retries the transient access-denied errors Windows
	// returns while the same file is being atomically replaced.
	b, err := atomicfile.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dst)
}

func parseMessageFileName(name string) (seq int64, messageID string, ok bool) {
	raw := strings.TrimSpace(name)
	if raw == "" || !strings.HasSuffix(raw, ".json") {
		return 0, "", false
	}
	base := strings.TrimSuffix(raw, ".json")
	parts := strings.SplitN(base, "_", 2)
	if len(parts) != 2 {
		return 0, "", false
	}
	v, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", false
	}
	id := strings.TrimSpace(parts[1])
	if id == "" {
		return 0, "", false
	}
	return v, id, true
}

func parseRunEventFileName(name string) (seq int64, ok bool) {
	raw := strings.TrimSpace(name)
	if raw == "" || !strings.HasSuffix(raw, ".json") {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSuffix(raw, ".json"), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func validateThreadRecord(t jsondb.Thread) error {
	if strings.TrimSpace(t.ID) == "" {
		return errors.New("thread.id is required")
	}
	if strings.TrimSpace(t.Title) == "" {
		return errors.New("thread.title is required")
	}
	if strings.TrimSpace(t.Status) == "" {
		return errors.New("thread.status is required")
	}
	if t.Kind == "" {
		return errors.New("thread.kind is required")
	}
	if t.SchemaVersion <= 0 {
		return errors.New("thread.schema_version is required")
	}
	if t.Counters.NextSeq <= 0 {
		t.Counters.NextSeq = 1
	}
	return nil
}

func validateRunRecord(r jsondb.Run) error {
	if strings.TrimSpace(r.ID) == "" {
		return errors.New("run.id is required")
	}
	if strings.TrimSpace(r.ThreadID) == "" {
		return errors.New("run.thread_id is required")
	}
	if strings.TrimSpace(r.Status) == "" {
		return errors.New("run.status is required")
	}
	if strings.TrimSpace(r.ProviderID) == "" {
		return errors.New("run.provider_id is required")
	}
	if strings.TrimSpace(r.Model) == "" {
		return errors.New("run.model is required")
	}
	if strings.TrimSpace(r.SystemPrompt) == "" {
		return errors.New("run.system_prompt is required")
	}
	if r.MaxIterations <= 0 {
		return errors.New("run.max_iterations must be > 0")
	}
	if r.Kind == "" {
		return errors.New("run.kind is required")
	}
	if r.SchemaVersion <= 0 {
		return errors.New("run.schema_version is required")
	}
	return nil
}

func validateRunEventRecord(ev runEventRecord) error {
	if strings.TrimSpace(ev.RunID) == "" {
		return errors.New("run_event.run_id is required")
	}
	if containsPathSeparator(ev.RunID) {
		return errors.New("run_event.run_id is invalid")
	}
	if strings.TrimSpace(ev.ThreadID) == "" {
		return errors.New("run_event.thread_id is required")
	}
	if containsPathSeparator(ev.ThreadID) {
		return errors.New("run_event.thread_id is invalid")
	}
	if ev.Seq <= 0 {
		return errors.New("run_event.seq must be > 0")
	}
	if strings.TrimSpace(ev.Type) == "" {
		return errors.New("run_event.type is required")
	}
	if ev.Kind != runEventKind {
		return errors.New("run_event.kind mismatch")
	}
	if ev.SchemaVersion <= 0 {
		return errors.New("run_event.schema_version is required")
	}
	if len(ev.Data) == 0 || !json.Valid(ev.Data) {
		return errors.New("run_event.data must be valid JSON")
	}
	return nil
}

func validateApprovalRequestRecord(a approvalRequestRecord) error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("approval_request.id is required")
	}
	if containsPathSeparator(a.ID) {
		return errors.New("approval_request.id is invalid")
	}
	if strings.TrimSpace(a.ThreadID) == "" {
		return errors.New("approval_request.thread_id is required")
	}
	if containsPathSeparator(a.ThreadID) {
		return errors.New("approval_request.thread_id is invalid")
	}
	if strings.TrimSpace(a.RunID) == "" {
		return errors.New("approval_request.run_id is required")
	}
	if containsPathSeparator(a.RunID) {
		return errors.New("approval_request.run_id is invalid")
	}
	if strings.TrimSpace(a.ToolCallID) != "" && containsPathSeparator(a.ToolCallID) {
		return errors.New("approval_request.tool_call_id is invalid")
	}
	if strings.TrimSpace(a.Tool) == "" {
		return errors.New("approval_request.tool is required")
	}
	switch strings.TrimSpace(strings.ToLower(a.Category)) {
	case "read", "write", "execute":
	default:
		return errors.New("approval_request.category must be read, write, or execute")
	}
	switch strings.TrimSpace(strings.ToLower(a.Status)) {
	case "pending", "approved", "rejected", "expired", "canceled":
	default:
		return errors.New("approval_request.status is invalid")
	}
	if strings.TrimSpace(a.Subject.Kind) == "" {
		return errors.New("approval_request.subject.kind is required")
	}
	if strings.TrimSpace(a.Summary) == "" {
		return errors.New("approval_request.summary is required")
	}
	if a.CreatedAt.IsZero() {
		return errors.New("approval_request.created_at is required")
	}
	if a.ExpiresAt.IsZero() {
		return errors.New("approval_request.expires_at is required")
	}
	if a.UpdatedAt.IsZero() {
		return errors.New("approval_request.updated_at is required")
	}
	if a.Kind != approvalRequestKind {
		return errors.New("approval_request.kind mismatch")
	}
	if a.SchemaVersion <= 0 {
		return errors.New("approval_request.schema_version is required")
	}
	if a.Decision != nil {
		switch strings.TrimSpace(strings.ToLower(a.Decision.Action)) {
		case "approve", "reject", "expire", "cancel":
		default:
			return errors.New("approval_request.decision.action is invalid")
		}
		switch strings.TrimSpace(strings.ToLower(a.Decision.Scope)) {
		case "once", "thread", "always":
		default:
			return errors.New("approval_request.decision.scope is invalid")
		}
		if a.Decision.DecidedAt.IsZero() {
			return errors.New("approval_request.decision.decided_at is required")
		}
	}
	if _, err := json.Marshal(a.Preview); err != nil {
		return fmt.Errorf("approval_request.preview must be JSON-serializable: %w", err)
	}
	return nil
}

func validateLocalSearchJobRecord(job localSearchJobRecord) error {
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("local_search_job.id is required")
	}
	if containsPathSeparator(job.ID) {
		return errors.New("local_search_job.id is invalid")
	}
	if strings.TrimSpace(job.ThreadID) == "" {
		return errors.New("local_search_job.thread_id is required")
	}
	if containsPathSeparator(job.ThreadID) {
		return errors.New("local_search_job.thread_id is invalid")
	}
	if strings.TrimSpace(job.RunID) == "" {
		return errors.New("local_search_job.run_id is required")
	}
	if containsPathSeparator(job.RunID) {
		return errors.New("local_search_job.run_id is invalid")
	}
	if strings.TrimSpace(job.ToolCallID) != "" && containsPathSeparator(job.ToolCallID) {
		return errors.New("local_search_job.tool_call_id is invalid")
	}
	switch strings.TrimSpace(strings.ToLower(job.Status)) {
	case "running", "completed", "failed", "canceled":
	default:
		return errors.New("local_search_job.status is invalid")
	}
	if strings.TrimSpace(job.Query) == "" {
		return errors.New("local_search_job.query is required")
	}
	if job.CreatedAt.IsZero() {
		return errors.New("local_search_job.created_at is required")
	}
	if job.UpdatedAt.IsZero() {
		return errors.New("local_search_job.updated_at is required")
	}
	if job.CompletedAt != nil && job.CompletedAt.IsZero() {
		return errors.New("local_search_job.completed_at is invalid")
	}
	if job.FilesCount < 0 {
		return errors.New("local_search_job.files_count is invalid")
	}
	if job.Kind != localSearchJobKind {
		return errors.New("local_search_job.kind mismatch")
	}
	if job.SchemaVersion <= 0 {
		return errors.New("local_search_job.schema_version is required")
	}
	if _, err := json.Marshal(job.Result); err != nil {
		return fmt.Errorf("local_search_job.result must be JSON-serializable: %w", err)
	}
	return nil
}

func validateMessageRecord(m jsondb.Message) error {
	if strings.TrimSpace(m.ID) == "" {
		return errors.New("message.id is required")
	}
	if strings.TrimSpace(m.ThreadID) == "" {
		return errors.New("message.thread_id is required")
	}
	if m.Seq <= 0 {
		return errors.New("message.seq must be > 0")
	}
	if strings.TrimSpace(string(m.Role)) == "" {
		return errors.New("message.role is required")
	}
	if m.Kind == "" {
		return errors.New("message.kind is required")
	}
	if m.SchemaVersion <= 0 {
		return errors.New("message.schema_version is required")
	}
	return nil
}

func validateArtifactRecord(a jsondb.Artifact) error {
	if strings.TrimSpace(a.ID) == "" {
		return errors.New("artifact.id is required")
	}
	if strings.TrimSpace(a.ThreadID) == "" {
		return errors.New("artifact.thread_id is required")
	}
	if strings.TrimSpace(a.Type) == "" {
		return errors.New("artifact.type is required")
	}
	if strings.TrimSpace(a.Title) == "" {
		return errors.New("artifact.title is required")
	}
	if strings.TrimSpace(a.Content.Storage) == "" {
		return errors.New("artifact.content.storage is required")
	}
	if a.Kind == "" {
		return errors.New("artifact.kind is required")
	}
	if a.SchemaVersion <= 0 {
		return errors.New("artifact.schema_version is required")
	}
	return nil
}

func validateProjectRecord(p jsondb.Project) error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("project.id is required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("project.name is required")
	}
	if strings.TrimSpace(p.RootRelPath) == "" {
		return errors.New("project.root_rel_path is required")
	}
	if p.Kind == "" {
		return errors.New("project.kind is required")
	}
	if p.SchemaVersion <= 0 {
		return errors.New("project.schema_version is required")
	}
	return nil
}

func validateJobRecord(j jsondb.Job) error {
	if strings.TrimSpace(j.ID) == "" {
		return errors.New("job.id is required")
	}
	if strings.TrimSpace(j.KindName) == "" {
		return errors.New("job.kind_name is required")
	}
	if strings.TrimSpace(j.ThreadID) == "" {
		return errors.New("job.thread_id is required")
	}
	if strings.TrimSpace(j.Status) == "" {
		return errors.New("job.status is required")
	}
	if j.Kind == "" {
		return errors.New("job.kind is required")
	}
	if j.SchemaVersion <= 0 {
		return errors.New("job.schema_version is required")
	}
	return nil
}

func validateContextCheckpointRecord(cp contextCheckpointRecord) error {
	if strings.TrimSpace(cp.ID) == "" {
		return errors.New("context_checkpoint.id is required")
	}
	if containsPathSeparator(cp.ID) {
		return errors.New("context_checkpoint.id is invalid")
	}
	if strings.TrimSpace(cp.ThreadID) == "" {
		return errors.New("context_checkpoint.thread_id is required")
	}
	if containsPathSeparator(cp.ThreadID) {
		return errors.New("context_checkpoint.thread_id is invalid")
	}
	if cp.SourceMessageSeq < 0 {
		return errors.New("context_checkpoint.source_message_seq must be >= 0")
	}
	if strings.TrimSpace(cp.Kind) == "" {
		return errors.New("context_checkpoint.kind is required")
	}
	if cp.Kind != contextCheckpointKind {
		return errors.New("context_checkpoint.kind mismatch")
	}
	if cp.SchemaVersion <= 0 {
		return errors.New("context_checkpoint.schema_version is required")
	}
	if _, err := json.Marshal(cp.ReplacementHistory); err != nil {
		return fmt.Errorf("context_checkpoint.replacement_history must be JSON-serializable: %w", err)
	}
	if len(cp.PlanJSON) > 0 && !json.Valid(cp.PlanJSON) {
		return errors.New("context_checkpoint.plan must be valid JSON")
	}
	return nil
}

func threadFromRecord(t jsondb.Thread) Thread {
	out := Thread{
		ID:              t.ID,
		Title:           t.Title,
		Summary:         t.Summary,
		ArtifactCount:   t.Counters.ArtifactCount,
		RunCount:        t.Counters.RunCount,
		SummaryRunCount: t.Counters.SummaryRunCount,
		ProjectID:       t.ProjectID,
		Status:          t.Status,
		CreatedAt:       t.CreatedAt,
		UpdatedAt:       t.UpdatedAt,
		LastMessageAt:   t.LastMessageAt,
		LastRunID:       t.LastRunID,
	}
	if t.LastTarget != nil {
		out.LastTarget = &RunTarget{ConnectionID: t.LastTarget.ConnectionID, Model: t.LastTarget.Model}
	}
	for _, policy := range t.AttachmentPolicies {
		out.AttachmentPolicies = append(out.AttachmentPolicies, AttachmentPolicy{
			ID: policy.ID, Class: policy.Class, Action: policy.Action, APISurface: policy.APISurface, CreatedAt: policy.CreatedAt,
		})
	}
	return out
}

func runFromRecord(r jsondb.Run) Run {
	out := Run{
		ID:                        r.ID,
		ThreadID:                  r.ThreadID,
		Status:                    r.Status,
		Revision:                  r.Revision,
		Purpose:                   string(r.Purpose),
		ProviderID:                r.ProviderID,
		Model:                     r.Model,
		ConnectionID:              r.ConnectionID,
		PlanID:                    r.PlanID,
		ProfileID:                 r.ProfileID,
		SystemPrompt:              r.SystemPrompt,
		MaxIterations:             r.MaxIterations,
		ContinuationCount:         r.ContinuationCount,
		MaxAutoContinuations:      r.MaxAutoContinuations,
		LastCheckpointSequence:    r.LastCheckpointSequence,
		WakeAt:                    r.WakeAt,
		ResumeLeaseHolder:         r.ResumeLeaseHolder,
		ResumeLeaseExpiresAt:      r.ResumeLeaseExpiresAt,
		RequestID:                 r.Usage.RequestID,
		InputTokens:               r.Usage.InputTokens,
		OutputTokens:              r.Usage.OutputTokens,
		TotalTokens:               r.Usage.TotalTokens,
		ContextTokensBefore:       r.ContextTokensBefore,
		ContextTokensAfter:        r.ContextTokensAfter,
		ContextSummaryBuckets:     r.ContextSummaryBuckets,
		ContextSummaryRounds:      r.ContextSummaryRounds,
		ContextArtifactRefsLoaded: r.ContextArtifactRefsLoaded,
		ContextNeedRetrieval:      r.ContextNeedRetrieval,
		ContextNeedTooling:        r.ContextNeedTooling,
		ContextHardCapped:         r.ContextHardCapped,
		ContextOverflowCompacted:  r.ContextOverflowCompacted,
		ContextPlanJSON:           r.ContextPlanJSON,
		CreatedAt:                 r.CreatedAt,
		StartedAt:                 r.StartedAt,
		EndedAt:                   r.EndedAt,
		UpdatedAt:                 r.UpdatedAt,
	}
	if r.Error != nil {
		if strings.TrimSpace(r.Error.Code) != "" {
			code := strings.TrimSpace(r.Error.Code)
			out.ErrorCode = &code
		}
		if strings.TrimSpace(r.Error.Message) != "" {
			msg := strings.TrimSpace(r.Error.Message)
			out.ErrorMessage = &msg
		}
	}
	if out.MaxAutoContinuations <= 0 {
		out.MaxAutoContinuations = 3
	}
	return out
}

func newJSONRunRecord(params CreateRunParams, planID string, now time.Time) (jsondb.Run, error) {
	now = now.UTC()
	if now.IsZero() {
		now = nowUTC()
	}
	maxIterations := params.MaxIterations
	if maxIterations <= 0 {
		maxIterations = 200
	}
	r := jsondb.Run{
		Kind: jsondb.KindRun, SchemaVersion: jsondb.SchemaVersion,
		ID: types.NewUUID(), ThreadID: strings.TrimSpace(params.ThreadID), Status: "queued",
		Purpose: jsondb.RunPurposeAnalysis, ProviderID: strings.TrimSpace(params.ProviderID),
		ConnectionID: strings.TrimSpace(params.ConnectionID), Model: strings.TrimSpace(params.Model),
		PlanID: strings.TrimSpace(planID), ProfileID: trimPtr(params.ProfileID),
		SystemPrompt: params.SystemPrompt, MaxIterations: maxIterations,
		MaxAutoContinuations: 3,
		CreatedAt:            now, UpdatedAt: now,
	}
	if err := validateRunRecord(r); err != nil {
		return jsondb.Run{}, err
	}
	return r, nil
}

func appendJSONPolicyIdempotent(existing []jsondb.AttachmentPolicy, policy AttachmentPolicy) []jsondb.AttachmentPolicy {
	for _, current := range existing {
		if strings.EqualFold(strings.TrimSpace(current.Class), strings.TrimSpace(policy.Class)) &&
			strings.EqualFold(strings.TrimSpace(current.Action), strings.TrimSpace(policy.Action)) &&
			strings.EqualFold(strings.TrimSpace(current.APISurface), strings.TrimSpace(policy.APISurface)) {
			return existing
		}
	}
	return append(existing, jsondb.AttachmentPolicy{
		ID: policy.ID, Class: policy.Class, Action: policy.Action, APISurface: policy.APISurface, CreatedAt: policy.CreatedAt,
	})
}

func cloneRunPlan(in RunPlan) RunPlan {
	b, _ := json.Marshal(in)
	var out RunPlan
	_ = json.Unmarshal(b, &out)
	return out
}

func runEventFromRecord(ev runEventRecord) RunEvent {
	return RunEvent{
		RunID:     strings.TrimSpace(ev.RunID),
		ThreadID:  strings.TrimSpace(ev.ThreadID),
		Seq:       ev.Seq,
		Type:      strings.TrimSpace(ev.Type),
		Data:      cloneRawJSON(ev.Data),
		CreatedAt: ev.CreatedAt,
	}
}

func runEventToRecord(ev RunEvent) runEventRecord {
	return runEventRecord{
		Kind:          runEventKind,
		SchemaVersion: runEventSchemaVersion,
		RunID:         strings.TrimSpace(ev.RunID),
		ThreadID:      strings.TrimSpace(ev.ThreadID),
		Seq:           ev.Seq,
		Type:          strings.TrimSpace(ev.Type),
		Data:          cloneRawJSON(ev.Data),
		CreatedAt:     ev.CreatedAt,
	}
}

func approvalRequestFromRecord(a approvalRequestRecord) ApprovalRequest {
	return ApprovalRequest{
		ID:         strings.TrimSpace(a.ID),
		ThreadID:   strings.TrimSpace(a.ThreadID),
		RunID:      strings.TrimSpace(a.RunID),
		ToolCallID: strings.TrimSpace(a.ToolCallID),
		Tool:       strings.TrimSpace(a.Tool),
		Category:   strings.TrimSpace(strings.ToLower(a.Category)),
		Operation:  strings.TrimSpace(a.Operation),
		Subject:    clonePermissionSubject(a.Subject),
		Summary:    strings.TrimSpace(a.Summary),
		Preview:    cloneMapAny(a.Preview),
		Options:    dedupeStrings(a.Options),
		Status:     strings.TrimSpace(strings.ToLower(a.Status)),
		Decision:   cloneApprovalDecision(a.Decision),
		CreatedAt:  a.CreatedAt,
		ExpiresAt:  a.ExpiresAt,
		UpdatedAt:  a.UpdatedAt,
	}
}

func approvalRequestToRecord(a ApprovalRequest) approvalRequestRecord {
	return approvalRequestRecord{
		Kind:          approvalRequestKind,
		SchemaVersion: approvalRequestSchemaVersion,
		ID:            strings.TrimSpace(a.ID),
		ThreadID:      strings.TrimSpace(a.ThreadID),
		RunID:         strings.TrimSpace(a.RunID),
		ToolCallID:    strings.TrimSpace(a.ToolCallID),
		Tool:          strings.TrimSpace(a.Tool),
		Category:      strings.TrimSpace(strings.ToLower(a.Category)),
		Operation:     strings.TrimSpace(a.Operation),
		Subject:       clonePermissionSubject(a.Subject),
		Summary:       strings.TrimSpace(a.Summary),
		Preview:       cloneMapAny(a.Preview),
		Options:       dedupeStrings(a.Options),
		Status:        strings.TrimSpace(strings.ToLower(a.Status)),
		Decision:      cloneApprovalDecision(a.Decision),
		CreatedAt:     a.CreatedAt,
		ExpiresAt:     a.ExpiresAt,
		UpdatedAt:     a.UpdatedAt,
	}
}

func localSearchJobFromRecord(job localSearchJobRecord) LocalSearchJob {
	return LocalSearchJob{
		ID:          strings.TrimSpace(job.ID),
		ThreadID:    strings.TrimSpace(job.ThreadID),
		RunID:       strings.TrimSpace(job.RunID),
		ToolCallID:  strings.TrimSpace(job.ToolCallID),
		Status:      strings.TrimSpace(strings.ToLower(job.Status)),
		Query:       strings.TrimSpace(job.Query),
		GrantID:     strings.TrimSpace(job.GrantID),
		LocalRoot:   strings.TrimSpace(job.LocalRoot),
		LocalKind:   strings.TrimSpace(strings.ToLower(job.LocalKind)),
		CreatedAt:   job.CreatedAt,
		UpdatedAt:   job.UpdatedAt,
		CompletedAt: cloneTimePtr(job.CompletedAt),
		FilesCount:  job.FilesCount,
		Error:       strings.TrimSpace(job.Error),
		Result:      cloneMapAny(job.Result),
	}
}

func localSearchJobToRecord(job LocalSearchJob) localSearchJobRecord {
	return localSearchJobRecord{
		Kind:          localSearchJobKind,
		SchemaVersion: localSearchJobSchemaVersion,
		ID:            strings.TrimSpace(job.ID),
		ThreadID:      strings.TrimSpace(job.ThreadID),
		RunID:         strings.TrimSpace(job.RunID),
		ToolCallID:    strings.TrimSpace(job.ToolCallID),
		Status:        strings.TrimSpace(strings.ToLower(job.Status)),
		Query:         strings.TrimSpace(job.Query),
		GrantID:       strings.TrimSpace(job.GrantID),
		LocalRoot:     strings.TrimSpace(job.LocalRoot),
		LocalKind:     strings.TrimSpace(strings.ToLower(job.LocalKind)),
		CreatedAt:     job.CreatedAt,
		UpdatedAt:     job.UpdatedAt,
		CompletedAt:   cloneTimePtr(job.CompletedAt),
		FilesCount:    job.FilesCount,
		Error:         strings.TrimSpace(job.Error),
		Result:        cloneMapAny(job.Result),
	}
}

func messageFromRecord(m jsondb.Message) Message {
	return Message{
		ID:                    m.ID,
		ThreadID:              m.ThreadID,
		RunID:                 m.RunID,
		Seq:                   m.Seq,
		ReferencedMessageIDs:  append([]string(nil), m.ReferencedMessageIDs...),
		ReferencedThreadIDs:   append([]string(nil), m.ReferencedThreadIDs...),
		ReferencedArtifactIDs: append([]string(nil), m.ReferencedArtifactIDs...),
		ContextSummary:        strings.TrimSpace(m.ContextSummary),
		ContextPlan:           cloneRawJSON(m.ContextPlan),
		HiddenContext:         cloneStringPtr(m.HiddenContext),
		Role:                  m.Role,
		Content:               m.Content,
		ProviderPayload:       cloneRawJSON(m.ProviderPayload),
		Thinking:              m.Thinking,
		ThinkingComplete:      m.ThinkingComplete,
		ToolCalls:             m.ToolCalls,
		ToolCallID:            m.ToolCallID,
		Name:                  m.Name,
		UIHidden:              m.UI.Hidden,
		CreatedAt:             m.CreatedAt,
		UpdatedAt:             m.UpdatedAt,
	}
}

func artifactFromRecord(a jsondb.Artifact) Artifact {
	return Artifact{
		ID:          a.ID,
		ThreadID:    a.ThreadID,
		MessageID:   a.MessageID,
		Type:        a.Type,
		Title:       a.Title,
		Language:    a.Language,
		ToolName:    a.ToolName,
		ToolCallID:  a.ToolCallID,
		OK:          a.OK,
		Storage:     contentStorageToStorage(a.Content.Storage),
		ContentText: a.Content.Text,
		ContentPath: a.Content.Path,
		ByteSize:    a.Content.ByteSize,
		Metadata:    a.Metadata,
		CreatedAt:   a.CreatedAt,
	}
}

func projectFromRecord(p jsondb.Project) Project {
	return Project{
		ID:          p.ID,
		Name:        p.Name,
		RootRelPath: p.RootRelPath,
		FileCount:   p.FileCount,
		TotalBytes:  p.TotalBytes,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
	}
}

func jobFromRecord(j jsondb.Job) Job {
	return Job{
		ID:        j.ID,
		Kind:      j.KindName,
		ThreadID:  j.ThreadID,
		RunID:     j.RunID,
		Status:    j.Status,
		Attempts:  j.Attempts,
		NotBefore: j.NotBefore,
		LastError: j.LastError,
		CreatedAt: j.CreatedAt,
		UpdatedAt: j.UpdatedAt,
	}
}

func contextCheckpointFromRecord(cp contextCheckpointRecord) ContextCheckpoint {
	return ContextCheckpoint{
		ID:                    cp.ID,
		ThreadID:              cp.ThreadID,
		RunID:                 cloneStringPtr(cp.RunID),
		SourceMessageSeq:      cp.SourceMessageSeq,
		SourceMessageID:       strings.TrimSpace(cp.SourceMessageID),
		ReplacementHistory:    cloneChatMessages(cp.ReplacementHistory),
		Summary:               cp.Summary,
		SummaryMessageIDs:     append([]string(nil), cp.SummaryMessageIDs...),
		ReferencedArtifactIDs: append([]string(nil), cp.ReferencedArtifactIDs...),
		TokenEstimate:         cp.TokenEstimate,
		CharsPerToken:         cp.CharsPerToken,
		Model:                 strings.TrimSpace(cp.Model),
		ProviderID:            strings.TrimSpace(cp.ProviderID),
		PlanJSON:              cloneRawJSON(cp.PlanJSON),
		CreatedAt:             cp.CreatedAt,
		UpdatedAt:             cp.UpdatedAt,
	}
}

func contextCheckpointToRecord(cp ContextCheckpoint) contextCheckpointRecord {
	return contextCheckpointRecord{
		Kind:                  contextCheckpointKind,
		SchemaVersion:         contextCheckpointSchemaVersion,
		ID:                    cp.ID,
		ThreadID:              cp.ThreadID,
		RunID:                 cloneStringPtr(cp.RunID),
		SourceMessageSeq:      cp.SourceMessageSeq,
		SourceMessageID:       strings.TrimSpace(cp.SourceMessageID),
		ReplacementHistory:    cloneChatMessages(cp.ReplacementHistory),
		Summary:               cp.Summary,
		SummaryMessageIDs:     dedupeStrings(cp.SummaryMessageIDs),
		ReferencedArtifactIDs: dedupeStrings(cp.ReferencedArtifactIDs),
		TokenEstimate:         cp.TokenEstimate,
		CharsPerToken:         cp.CharsPerToken,
		Model:                 strings.TrimSpace(cp.Model),
		ProviderID:            strings.TrimSpace(cp.ProviderID),
		PlanJSON:              cloneRawJSON(cp.PlanJSON),
		CreatedAt:             cp.CreatedAt,
		UpdatedAt:             cp.UpdatedAt,
	}
}

func storageToContentStorage(storage string) string {
	switch strings.TrimSpace(storage) {
	case "file":
		return "file"
	default:
		return "inline"
	}
}

func contentStorageToStorage(storage string) string {
	switch strings.TrimSpace(storage) {
	case "file":
		return "file"
	default:
		return "mysql"
	}
}

func trimPtr(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		return nil
	}
	out := s
	return &out
}

func cloneStringPtr(v *string) *string {
	if v == nil {
		return nil
	}
	c := strings.TrimSpace(*v)
	if c == "" {
		return nil
	}
	out := c
	return &out
}

func containsPathSeparator(v string) bool {
	s := strings.TrimSpace(v)
	return s == "." || s == ".." || strings.Contains(s, "/") || strings.Contains(s, "\\")
}

func cloneRawJSON(v []byte) json.RawMessage {
	if len(v) == 0 {
		return nil
	}
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err == nil {
		out := make([]byte, compact.Len())
		copy(out, compact.Bytes())
		return out
	}
	out := make([]byte, len(trimmed))
	copy(out, trimmed)
	return out
}

func compactValidRawJSON(v []byte) (json.RawMessage, error) {
	if len(v) == 0 {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(v)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if !json.Valid(trimmed) {
		return nil, errors.New("invalid JSON")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, trimmed); err != nil {
		return nil, err
	}
	out := make([]byte, compact.Len())
	copy(out, compact.Bytes())
	return out, nil
}

func cloneChatMessages(in []types.ChatMessage) []types.ChatMessage {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil
	}
	var out []types.ChatMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func prepareRunEvent(in RunEvent) (RunEvent, error) {
	ev := in
	ev.RunID = strings.TrimSpace(ev.RunID)
	if ev.RunID == "" {
		return RunEvent{}, errors.New("runId is required")
	}
	ev.ThreadID = strings.TrimSpace(ev.ThreadID)
	if ev.ThreadID == "" {
		return RunEvent{}, errors.New("threadId is required")
	}
	ev.Type = strings.TrimSpace(ev.Type)
	if ev.Type == "" {
		return RunEvent{}, errors.New("runEvent.type is required")
	}
	if ev.Seq <= 0 {
		return RunEvent{}, errors.New("runEvent.seq must be > 0")
	}
	data, err := compactValidRawJSON(ev.Data)
	if err != nil {
		return RunEvent{}, fmt.Errorf("runEvent.data must be JSON-serializable: %w", err)
	}
	if len(data) == 0 {
		data = json.RawMessage(`{}`)
	}
	ev.Data = data
	if ev.CreatedAt.IsZero() {
		ev.CreatedAt = nowUTC()
	} else {
		ev.CreatedAt = ev.CreatedAt.UTC()
	}
	rec := runEventToRecord(ev)
	if err := validateRunEventRecord(rec); err != nil {
		return RunEvent{}, err
	}
	return ev, nil
}

func prepareApprovalRequest(in ApprovalRequest) (ApprovalRequest, error) {
	a := in
	a.ID = strings.TrimSpace(a.ID)
	if a.ID == "" {
		a.ID = "apr_" + types.NewUUID()
	}
	a.ThreadID = strings.TrimSpace(a.ThreadID)
	if a.ThreadID == "" {
		return ApprovalRequest{}, errors.New("threadId is required")
	}
	a.RunID = strings.TrimSpace(a.RunID)
	if a.RunID == "" {
		return ApprovalRequest{}, errors.New("runId is required")
	}
	a.ToolCallID = strings.TrimSpace(a.ToolCallID)
	a.Tool = strings.TrimSpace(a.Tool)
	if a.Tool == "" {
		return ApprovalRequest{}, errors.New("approval tool is required")
	}
	a.Category = strings.TrimSpace(strings.ToLower(a.Category))
	if a.Category == "" {
		a.Category = "read"
	}
	a.Operation = strings.TrimSpace(a.Operation)
	a.Subject = clonePermissionSubject(a.Subject)
	if strings.TrimSpace(a.Subject.Kind) == "" {
		a.Subject.Kind = "tool"
	}
	a.Summary = strings.TrimSpace(a.Summary)
	if a.Summary == "" {
		return ApprovalRequest{}, errors.New("approval summary is required")
	}
	a.Preview = cloneMapAny(a.Preview)
	a.Options = dedupeStrings(a.Options)
	if len(a.Options) == 0 {
		a.Options = []string{"once", "thread", "always"}
	}
	a.Status = strings.TrimSpace(strings.ToLower(a.Status))
	if a.Status == "" {
		a.Status = "pending"
	}
	now := nowUTC()
	if a.CreatedAt.IsZero() {
		a.CreatedAt = now
	} else {
		a.CreatedAt = a.CreatedAt.UTC()
	}
	if a.ExpiresAt.IsZero() {
		a.ExpiresAt = a.CreatedAt.Add(10 * time.Minute)
	} else {
		a.ExpiresAt = a.ExpiresAt.UTC()
	}
	if a.UpdatedAt.IsZero() {
		a.UpdatedAt = a.CreatedAt
	} else {
		a.UpdatedAt = a.UpdatedAt.UTC()
	}
	a.Decision = cloneApprovalDecision(a.Decision)
	rec := approvalRequestToRecord(a)
	if err := validateApprovalRequestRecord(rec); err != nil {
		return ApprovalRequest{}, err
	}
	return a, nil
}

func prepareLocalSearchJob(in LocalSearchJob) (LocalSearchJob, error) {
	job := in
	job.ID = strings.TrimSpace(job.ID)
	if job.ID == "" {
		job.ID = "local_search_" + types.NewUUID()
	}
	job.ThreadID = strings.TrimSpace(job.ThreadID)
	if job.ThreadID == "" {
		return LocalSearchJob{}, errors.New("threadId is required")
	}
	job.RunID = strings.TrimSpace(job.RunID)
	if job.RunID == "" {
		return LocalSearchJob{}, errors.New("runId is required")
	}
	job.ToolCallID = strings.TrimSpace(job.ToolCallID)
	job.Status = strings.TrimSpace(strings.ToLower(job.Status))
	if job.Status == "" {
		job.Status = "running"
	}
	job.Query = strings.TrimSpace(job.Query)
	if job.Query == "" {
		return LocalSearchJob{}, errors.New("local search job query is required")
	}
	job.GrantID = strings.TrimSpace(job.GrantID)
	job.LocalRoot = strings.TrimSpace(job.LocalRoot)
	job.LocalKind = strings.TrimSpace(strings.ToLower(job.LocalKind))
	if job.LocalKind == "" && job.LocalRoot != "" {
		job.LocalKind = "folder"
	}
	now := nowUTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	} else {
		job.CreatedAt = job.CreatedAt.UTC()
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	} else {
		job.UpdatedAt = job.UpdatedAt.UTC()
	}
	if job.CompletedAt != nil {
		completed := job.CompletedAt.UTC()
		job.CompletedAt = &completed
	}
	if job.FilesCount < 0 {
		job.FilesCount = 0
	}
	job.Error = strings.TrimSpace(job.Error)
	job.Result = cloneMapAny(job.Result)
	rec := localSearchJobToRecord(job)
	if err := validateLocalSearchJobRecord(rec); err != nil {
		return LocalSearchJob{}, err
	}
	return job, nil
}

func prepareContextCheckpoint(in ContextCheckpoint) (ContextCheckpoint, error) {
	cp := in
	cp.ID = strings.TrimSpace(cp.ID)
	if cp.ID == "" {
		cp.ID = types.NewUUID()
	}
	cp.ThreadID = strings.TrimSpace(cp.ThreadID)
	if cp.ThreadID == "" {
		return ContextCheckpoint{}, errors.New("threadId is required")
	}
	cp.RunID = trimPtr(cp.RunID)
	cp.SourceMessageID = strings.TrimSpace(cp.SourceMessageID)
	cp.SummaryMessageIDs = dedupeStrings(cp.SummaryMessageIDs)
	cp.ReferencedArtifactIDs = dedupeStrings(cp.ReferencedArtifactIDs)
	cp.Model = strings.TrimSpace(cp.Model)
	cp.ProviderID = strings.TrimSpace(cp.ProviderID)
	if cp.SourceMessageSeq < 0 {
		return ContextCheckpoint{}, errors.New("sourceMessageSeq must be >= 0")
	}
	if cp.TokenEstimate < 0 {
		cp.TokenEstimate = 0
	}
	if cp.CharsPerToken < 0 {
		cp.CharsPerToken = 0
	}
	if cp.ReplacementHistory == nil {
		cp.ReplacementHistory = []types.ChatMessage{}
	}
	history, err := cloneChatMessagesForCheckpoint(cp.ReplacementHistory)
	if err != nil {
		return ContextCheckpoint{}, err
	}
	cp.ReplacementHistory = history
	plan, err := compactValidRawJSON(cp.PlanJSON)
	if err != nil {
		return ContextCheckpoint{}, fmt.Errorf("plan must be JSON-serializable: %w", err)
	}
	cp.PlanJSON = plan
	now := nowUTC()
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = now
	} else {
		cp.CreatedAt = cp.CreatedAt.UTC()
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = now
	} else {
		cp.UpdatedAt = cp.UpdatedAt.UTC()
	}
	rec := contextCheckpointToRecord(cp)
	if err := validateContextCheckpointRecord(rec); err != nil {
		return ContextCheckpoint{}, err
	}
	return cp, nil
}

func cloneChatMessagesForCheckpoint(in []types.ChatMessage) ([]types.ChatMessage, error) {
	if in == nil {
		return []types.ChatMessage{}, nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("replacementHistory must be JSON-serializable: %w", err)
	}
	var out []types.ChatMessage
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = []types.ChatMessage{}
	}
	return out, nil
}

func anyStringSlice(v any) ([]string, bool) {
	switch t := v.(type) {
	case []string:
		out := make([]string, 0, len(t))
		for _, raw := range t {
			s := strings.TrimSpace(raw)
			if s == "" {
				continue
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	case []any:
		out := make([]string, 0, len(t))
		for _, raw := range t {
			s, ok := raw.(string)
			if !ok {
				continue
			}
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, false
		}
		return out, true
	default:
		return nil, false
	}
}

func hasAnyOverlap(needles []string, haystack []string) bool {
	if len(needles) == 0 || len(haystack) == 0 {
		return false
	}
	m := map[string]struct{}{}
	for _, v := range haystack {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		m[v] = struct{}{}
	}
	for _, n := range needles {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		if _, ok := m[n]; ok {
			return true
		}
	}
	return false
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		s := strings.TrimSpace(raw)
		if s == "" {
			continue
		}
		if _, exists := seen[s]; exists {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func clonePermissionSubject(in PermissionSubject) PermissionSubject {
	return PermissionSubject{
		Kind:       strings.TrimSpace(in.Kind),
		Tool:       strings.TrimSpace(in.Tool),
		Connector:  strings.TrimSpace(in.Connector),
		ToolSetID:  strings.TrimSpace(in.ToolSetID),
		LocalRoot:  strings.TrimSpace(in.LocalRoot),
		LocalKind:  strings.TrimSpace(in.LocalKind),
		Repository: strings.TrimSpace(in.Repository),
		RemoteKind: strings.TrimSpace(in.RemoteKind),
		RemoteID:   strings.TrimSpace(in.RemoteID),
		Operation:  strings.TrimSpace(in.Operation),
		Excludes:   dedupeStrings(in.Excludes),
	}
}

func cloneApprovalDecision(in *ApprovalDecision) *ApprovalDecision {
	if in == nil {
		return nil
	}
	out := *in
	out.Action = strings.TrimSpace(strings.ToLower(out.Action))
	out.Scope = strings.TrimSpace(strings.ToLower(out.Scope))
	if out.Subject != nil {
		subject := clonePermissionSubject(*out.Subject)
		out.Subject = &subject
	}
	if !out.DecidedAt.IsZero() {
		out.DecidedAt = out.DecidedAt.UTC()
	}
	return &out
}

func cloneTimePtr(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := in.UTC()
	return &out
}

func cloneMapAny(in map[string]any) map[string]any {
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

func anyString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return "", false
		}
		return s, true
	default:
		return "", false
	}
}

func anyInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int32:
		return int(t), true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	default:
		return 0, false
	}
}

func anyBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	default:
		return false, false
	}
}

func anyTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, true
	case string:
		tt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(t))
		if err != nil {
			return time.Time{}, false
		}
		return tt, true
	default:
		return time.Time{}, false
	}
}

type projectFilesEnvelope struct {
	Files []jsondb.ProjectFile `json:"files"`
}
