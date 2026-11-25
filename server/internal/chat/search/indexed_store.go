package search

import (
	"context"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/store"
)

type IndexedStore struct {
	base store.Store
	svc  *Service

	mu      sync.Mutex
	pending map[string]struct{}
}

var _ store.Store = (*IndexedStore)(nil)

func NewIndexedStore(base store.Store, svc *Service) *IndexedStore {
	return &IndexedStore{
		base:    base,
		svc:     svc,
		pending: map[string]struct{}{},
	}
}

func (s *IndexedStore) queueSync(threadID string) {
	tid := strings.TrimSpace(threadID)
	if tid == "" || s.svc == nil {
		return
	}
	s.mu.Lock()
	if _, ok := s.pending[tid]; ok {
		s.mu.Unlock()
		return
	}
	s.pending[tid] = struct{}{}
	s.mu.Unlock()

	go func() {
		defer func() {
			s.mu.Lock()
			delete(s.pending, tid)
			s.mu.Unlock()
		}()
		_ = s.svc.SyncThread(context.Background(), tid)
	}()
}

func (s *IndexedStore) Ping(ctx context.Context) error {
	return s.base.Ping(ctx)
}

func (s *IndexedStore) CreateThread(ctx context.Context, projectID *string) (store.Thread, error) {
	out, err := s.base.CreateThread(ctx, projectID)
	if err == nil {
		s.queueSync(out.ID)
	}
	return out, err
}

func (s *IndexedStore) ListThreads(ctx context.Context, status string, limit int) ([]store.Thread, error) {
	return s.base.ListThreads(ctx, status, limit)
}

func (s *IndexedStore) GetThread(ctx context.Context, threadID string) (store.Thread, error) {
	return s.base.GetThread(ctx, threadID)
}

func (s *IndexedStore) ArchiveThread(ctx context.Context, threadID string) error {
	err := s.base.ArchiveThread(ctx, threadID)
	if err == nil {
		s.queueSync(threadID)
	}
	return err
}

func (s *IndexedStore) DeleteThread(ctx context.Context, threadID string) error {
	err := s.base.DeleteThread(ctx, threadID)
	if err == nil && s.svc != nil {
		_ = s.svc.RemoveThread(threadID)
	}
	return err
}

func (s *IndexedStore) UpdateThreadTitleSummary(ctx context.Context, threadID string, title *string, summary *string) error {
	err := s.base.UpdateThreadTitleSummary(ctx, threadID, title, summary)
	if err == nil {
		s.queueSync(threadID)
	}
	return err
}

func (s *IndexedStore) UpdateThreadProject(ctx context.Context, threadID string, projectID string) error {
	err := s.base.UpdateThreadProject(ctx, threadID, projectID)
	if err == nil {
		s.queueSync(threadID)
	}
	return err
}

func (s *IndexedStore) IncrementThreadRunCount(ctx context.Context, threadID string) error {
	err := s.base.IncrementThreadRunCount(ctx, threadID)
	if err == nil {
		s.queueSync(threadID)
	}
	return err
}

func (s *IndexedStore) SetThreadTarget(ctx context.Context, threadID string, target store.RunTarget) (store.Thread, error) {
	out, err := s.base.SetThreadTarget(ctx, threadID, target)
	if err == nil {
		s.queueSync(threadID)
	}
	return out, err
}

func (s *IndexedStore) DeleteThreadAttachmentPolicy(ctx context.Context, threadID string, policyID string) (store.Thread, error) {
	out, err := s.base.DeleteThreadAttachmentPolicy(ctx, threadID, policyID)
	if err == nil {
		s.queueSync(threadID)
	}
	return out, err
}

func (s *IndexedStore) CreateRun(ctx context.Context, threadID string, providerID string, model string, profileID *string, systemPrompt string, maxIterations int) (store.Run, error) {
	out, err := s.base.CreateRun(ctx, threadID, providerID, model, profileID, systemPrompt, maxIterations)
	if err == nil {
		s.queueSync(threadID)
	}
	return out, err
}

func (s *IndexedStore) CreateRunPlan(ctx context.Context, plan store.RunPlan) (store.RunPlan, error) {
	return s.base.CreateRunPlan(ctx, plan)
}

func (s *IndexedStore) GetRunPlan(ctx context.Context, planID string) (store.RunPlan, error) {
	return s.base.GetRunPlan(ctx, planID)
}

func (s *IndexedStore) GetRunPlanForRun(ctx context.Context, runID string) (store.RunPlan, error) {
	return s.base.GetRunPlanForRun(ctx, runID)
}

func (s *IndexedStore) FinalizeRunPlanAndCreateRun(ctx context.Context, req store.FinalizeRunPlanRequest) (store.Run, store.RunPlan, error) {
	run, plan, err := s.base.FinalizeRunPlanAndCreateRun(ctx, req)
	if err == nil {
		s.queueSync(run.ThreadID)
	}
	return run, plan, err
}

func (s *IndexedStore) DeleteExpiredRunPlans(ctx context.Context, now time.Time) (int64, error) {
	return s.base.DeleteExpiredRunPlans(ctx, now)
}

func (s *IndexedStore) GetRun(ctx context.Context, runID string) (store.Run, error) {
	return s.base.GetRun(ctx, runID)
}

func (s *IndexedStore) ListRuns(ctx context.Context, threadID string, limit int) ([]store.Run, error) {
	return s.base.ListRuns(ctx, threadID, limit)
}

func (s *IndexedStore) UpdateRunStatus(ctx context.Context, runID string, status string, fields map[string]any) error {
	err := s.base.UpdateRunStatus(ctx, runID, status, fields)
	if err == nil {
		if run, getErr := s.base.GetRun(ctx, runID); getErr == nil {
			s.queueSync(run.ThreadID)
		}
	}
	return err
}

func (s *IndexedStore) AppendRunEvent(ctx context.Context, ev store.RunEvent) (store.RunEvent, error) {
	return s.base.AppendRunEvent(ctx, ev)
}

func (s *IndexedStore) ListRunEvents(ctx context.Context, p store.ListRunEventsParams) ([]store.RunEvent, error) {
	return s.base.ListRunEvents(ctx, p)
}

func (s *IndexedStore) LatestRunEventSeq(ctx context.Context, runID string) (int64, error) {
	return s.base.LatestRunEventSeq(ctx, runID)
}

func (s *IndexedStore) UpsertProviderCapabilityEvidence(ctx context.Context, evidence store.ProviderCapabilityEvidence) (store.ProviderCapabilityEvidence, error) {
	return s.base.UpsertProviderCapabilityEvidence(ctx, evidence)
}

func (s *IndexedStore) ListProviderCapabilityEvidence(ctx context.Context, filter store.ProviderCapabilityEvidenceFilter) ([]store.ProviderCapabilityEvidence, error) {
	return s.base.ListProviderCapabilityEvidence(ctx, filter)
}

func (s *IndexedStore) CreateApprovalRequest(ctx context.Context, approval store.ApprovalRequest) (store.ApprovalRequest, error) {
	return s.base.CreateApprovalRequest(ctx, approval)
}

func (s *IndexedStore) GetApprovalRequest(ctx context.Context, approvalID string) (store.ApprovalRequest, error) {
	return s.base.GetApprovalRequest(ctx, approvalID)
}

func (s *IndexedStore) ListApprovalRequests(ctx context.Context, filter store.ApprovalFilter) ([]store.ApprovalRequest, error) {
	return s.base.ListApprovalRequests(ctx, filter)
}

func (s *IndexedStore) DecideApprovalRequest(ctx context.Context, approvalID string, decision store.ApprovalDecision, status string) (store.ApprovalRequest, error) {
	return s.base.DecideApprovalRequest(ctx, approvalID, decision, status)
}

func (s *IndexedStore) CancelRunApprovalRequests(ctx context.Context, runID string) error {
	return s.base.CancelRunApprovalRequests(ctx, runID)
}

func (s *IndexedStore) ExpireStaleApprovalRequests(ctx context.Context, now time.Time) error {
	return s.base.ExpireStaleApprovalRequests(ctx, now)
}

func (s *IndexedStore) UpsertLocalSearchJob(ctx context.Context, job store.LocalSearchJob) (store.LocalSearchJob, error) {
	return s.base.UpsertLocalSearchJob(ctx, job)
}

func (s *IndexedStore) GetLocalSearchJob(ctx context.Context, jobID string) (store.LocalSearchJob, error) {
	return s.base.GetLocalSearchJob(ctx, jobID)
}

func (s *IndexedStore) ListLocalSearchJobs(ctx context.Context, filter store.LocalSearchJobFilter) ([]store.LocalSearchJob, error) {
	return s.base.ListLocalSearchJobs(ctx, filter)
}

func (s *IndexedStore) GetActiveContextCheckpoint(ctx context.Context, threadID string) (*store.ContextCheckpoint, error) {
	return s.base.GetActiveContextCheckpoint(ctx, threadID)
}

func (s *IndexedStore) UpsertActiveContextCheckpoint(ctx context.Context, checkpoint store.ContextCheckpoint) (store.ContextCheckpoint, error) {
	return s.base.UpsertActiveContextCheckpoint(ctx, checkpoint)
}

func (s *IndexedStore) ListContextCheckpoints(ctx context.Context, threadID string, limit int) ([]store.ContextCheckpoint, error) {
	return s.base.ListContextCheckpoints(ctx, threadID, limit)
}

func (s *IndexedStore) AppendMessage(ctx context.Context, threadID string, runID *string, msg store.Message) (store.Message, error) {
	out, err := s.base.AppendMessage(ctx, threadID, runID, msg)
	if err == nil {
		s.queueSync(threadID)
	}
	return out, err
}

func (s *IndexedStore) UpdateMessage(ctx context.Context, messageID string, fields map[string]any) error {
	err := s.base.UpdateMessage(ctx, messageID, fields)
	if err == nil && s.svc != nil {
		// Best effort: infer thread by scanning indexed docs for this message.
		docID := "message:" + strings.TrimSpace(messageID)
		if doc, ok := s.svc.GetDoc(docID); ok {
			s.queueSync(doc.ThreadID)
		}
	}
	return err
}

func (s *IndexedStore) ListMessages(ctx context.Context, p store.ListMessagesParams) ([]store.Message, error) {
	return s.base.ListMessages(ctx, p)
}

func (s *IndexedStore) InsertArtifact(ctx context.Context, a store.Artifact) (store.Artifact, error) {
	out, err := s.base.InsertArtifact(ctx, a)
	if err == nil {
		s.queueSync(out.ThreadID)
	}
	return out, err
}

func (s *IndexedStore) ReplaceArtifact(ctx context.Context, a store.Artifact) (store.Artifact, error) {
	out, err := s.base.ReplaceArtifact(ctx, a)
	if err == nil {
		s.queueSync(out.ThreadID)
	}
	return out, err
}

func (s *IndexedStore) GetArtifact(ctx context.Context, artifactID string) (store.Artifact, error) {
	return s.base.GetArtifact(ctx, artifactID)
}

func (s *IndexedStore) DeleteArtifact(ctx context.Context, artifactID string) error {
	var threadID string
	if artifact, err := s.base.GetArtifact(ctx, artifactID); err == nil {
		threadID = artifact.ThreadID
	}
	err := s.base.DeleteArtifact(ctx, artifactID)
	if err == nil {
		s.queueSync(threadID)
	}
	return err
}

func (s *IndexedStore) ListArtifacts(ctx context.Context, threadID string, limit int) ([]store.Artifact, error) {
	return s.base.ListArtifacts(ctx, threadID, limit)
}

func (s *IndexedStore) ListArtifactsPage(ctx context.Context, p store.ListArtifactsPageParams) (store.ArtifactPage, error) {
	type artifactPager interface {
		ListArtifactsPage(context.Context, store.ListArtifactsPageParams) (store.ArtifactPage, error)
	}
	if pager, ok := s.base.(artifactPager); ok {
		return pager.ListArtifactsPage(ctx, p)
	}
	var (
		artifacts []store.Artifact
		err       error
	)
	if strings.EqualFold(strings.TrimSpace(p.Type), "file") {
		artifacts, err = s.base.ListFileArtifacts(ctx, p.ThreadID, p.Limit)
	} else {
		artifacts, err = s.base.ListArtifacts(ctx, p.ThreadID, p.Limit)
	}
	if err != nil {
		return store.ArtifactPage{}, err
	}
	return store.ArtifactPage{Artifacts: artifacts, HasMore: false}, nil
}

func (s *IndexedStore) ListFileArtifacts(ctx context.Context, threadID string, limit int) ([]store.Artifact, error) {
	return s.base.ListFileArtifacts(ctx, threadID, limit)
}

func (s *IndexedStore) CreateProject(ctx context.Context, name string, rootRelPath string) (store.Project, error) {
	return s.base.CreateProject(ctx, name, rootRelPath)
}

func (s *IndexedStore) ListProjects(ctx context.Context, limit int) ([]store.Project, error) {
	return s.base.ListProjects(ctx, limit)
}

func (s *IndexedStore) GetProject(ctx context.Context, projectID string) (store.Project, error) {
	return s.base.GetProject(ctx, projectID)
}

func (s *IndexedStore) UpsertProjectFile(ctx context.Context, projectID string, path string, byteSize int64, sha256 []byte) error {
	return s.base.UpsertProjectFile(ctx, projectID, path, byteSize, sha256)
}

func (s *IndexedStore) ReplaceProjectFiles(ctx context.Context, projectID string, files []store.ProjectFileStat) (store.Project, error) {
	return s.base.ReplaceProjectFiles(ctx, projectID, files)
}

func (s *IndexedStore) FinalizeProject(ctx context.Context, projectID string) (store.Project, error) {
	return s.base.FinalizeProject(ctx, projectID)
}

func (s *IndexedStore) EnqueueJob(ctx context.Context, kind string, threadID string, runID *string) (store.Job, error) {
	return s.base.EnqueueJob(ctx, kind, threadID, runID)
}

func (s *IndexedStore) ClaimNextJob(ctx context.Context, now time.Time) (*store.Job, error) {
	return s.base.ClaimNextJob(ctx, now)
}

func (s *IndexedStore) CompleteJob(ctx context.Context, jobID string) error {
	return s.base.CompleteJob(ctx, jobID)
}

func (s *IndexedStore) FailJob(ctx context.Context, jobID string, errMsg string, notBefore time.Time) error {
	return s.base.FailJob(ctx, jobID, errMsg, notBefore)
}
