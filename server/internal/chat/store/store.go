package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/types"
)

type Thread struct {
	ID                 string             `json:"id"`
	Title              string             `json:"title"`
	Summary            *string            `json:"summary,omitempty"`
	ArtifactCount      int64              `json:"-"`
	RunCount           int64              `json:"-"`
	SummaryRunCount    int64              `json:"-"`
	ProjectID          *string            `json:"project_id,omitempty"`
	Status             string             `json:"status"`
	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	LastMessageAt      *time.Time         `json:"last_message_at,omitempty"`
	LastRunID          *string            `json:"last_run_id,omitempty"`
	LastTarget         *RunTarget         `json:"last_target,omitempty"`
	AttachmentPolicies []AttachmentPolicy `json:"attachment_policies,omitempty"`
}

// RunTarget identifies a saved provider connection and model. ConnectionID is
// intentionally distinct from ProviderID because a provider family may have
// multiple independently configured connections.
type RunTarget struct {
	ConnectionID string `json:"connectionId"`
	Model        string `json:"model"`
}

// AttachmentPolicy records explicit consent to an attachment transformation.
// Class + Action + APISurface is its idempotency key within a scope.
type AttachmentPolicy struct {
	ID         string `json:"id"`
	Class      string `json:"class"`
	Action     string `json:"action"`
	APISurface string `json:"apiSurface"`
	CreatedAt  int64  `json:"createdAt"`
}

// AttachmentPoliciesFingerprint hashes only the authorization identity used
// by preflight. It deliberately excludes record IDs and timestamps so an
// idempotently recreated equivalent policy has the same meaning.
func AttachmentPoliciesFingerprint(policies []AttachmentPolicy) string {
	type material struct {
		Class      string `json:"class"`
		Action     string `json:"action"`
		APISurface string `json:"apiSurface"`
	}
	normalized := make([]material, 0, len(policies))
	for _, policy := range policies {
		normalized = append(normalized, material{
			Class: strings.TrimSpace(policy.Class), Action: strings.TrimSpace(policy.Action), APISurface: strings.TrimSpace(policy.APISurface),
		})
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Class != normalized[j].Class {
			return normalized[i].Class < normalized[j].Class
		}
		if normalized[i].Action != normalized[j].Action {
			return normalized[i].Action < normalized[j].Action
		}
		return normalized[i].APISurface < normalized[j].APISurface
	})
	raw, _ := json.Marshal(struct {
		SchemaVersion int        `json:"schemaVersion"`
		Policies      []material `json:"policies"`
	}{1, normalized})
	sum := sha256.Sum256(raw)
	return "thread-policy-v1:" + hex.EncodeToString(sum[:])
}

type Run struct {
	ID                     string     `json:"id"`
	ThreadID               string     `json:"thread_id"`
	Status                 string     `json:"status"`
	Revision               int64      `json:"revision"`
	Purpose                string     `json:"purpose,omitempty"`
	ProviderID             string     `json:"provider_id"`
	Model                  string     `json:"model"`
	ConnectionID           string     `json:"connection_id,omitempty"`
	PlanID                 string     `json:"plan_id,omitempty"`
	ProfileID              *string    `json:"profile_id,omitempty"`
	SystemPrompt           string     `json:"system_prompt"`
	MaxIterations          int        `json:"max_iterations"`
	ContinuationCount      int        `json:"continuation_count"`
	MaxAutoContinuations   int        `json:"max_auto_continuations"`
	LastCheckpointSequence int64      `json:"last_checkpoint_sequence"`
	WakeAt                 *time.Time `json:"wake_at,omitempty"`
	ResumeLeaseHolder      string     `json:"resume_lease_holder,omitempty"`
	ResumeLeaseExpiresAt   *time.Time `json:"resume_lease_expires_at,omitempty"`

	RequestID    *string `json:"request_id,omitempty"`
	InputTokens  *int    `json:"input_tokens,omitempty"`
	OutputTokens *int    `json:"output_tokens,omitempty"`
	TotalTokens  *int    `json:"total_tokens,omitempty"`

	ErrorCode    *string `json:"error_code,omitempty"`
	ErrorMessage *string `json:"error_message,omitempty"`

	ContextTokensBefore       *int            `json:"context_tokens_before,omitempty"`
	ContextTokensAfter        *int            `json:"context_tokens_after,omitempty"`
	ContextSummaryBuckets     *int            `json:"context_summary_buckets,omitempty"`
	ContextSummaryRounds      *int            `json:"context_summary_rounds,omitempty"`
	ContextArtifactRefsLoaded *int            `json:"context_artifact_refs_loaded,omitempty"`
	ContextNeedRetrieval      *bool           `json:"context_need_retrieval,omitempty"`
	ContextNeedTooling        *bool           `json:"context_need_retrieval_tooling,omitempty"`
	ContextHardCapped         *bool           `json:"context_hard_capped,omitempty"`
	ContextOverflowCompacted  *bool           `json:"context_overflow_compacted,omitempty"`
	ContextPlanJSON           json.RawMessage `json:"context_plan,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`
}

const (
	RunPlanStatusDraft     = "draft"
	RunPlanStatusFinalized = "finalized"
)

// PlanIssue uses IssueID as its selection key. Codes may repeat when multiple
// attachments require the same decision.
type PlanIssue struct {
	IssueID      string          `json:"issueId"`
	Code         string          `json:"code"`
	Text         string          `json:"text"`
	Feature      *string         `json:"feature"`
	AttachmentID *string         `json:"attachmentId"`
	Fix          json.RawMessage `json:"fix"`
}

type PlanChoiceOption struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Consequence string  `json:"consequence"`
	PolicyClass *string `json:"policyClass"`
	APISurface  string  `json:"apiSurface,omitempty"`
}

type PlanChoice struct {
	PlanIssue
	Options       []PlanChoiceOption `json:"options"`
	AllowedScopes []string           `json:"allowedScopes"`
}

type PlanChoiceSelection struct {
	IssueID    string    `json:"issueId"`
	OptionID   string    `json:"optionId"`
	Scope      string    `json:"scope"`
	SelectedAt time.Time `json:"selectedAt"`
}

// RunPlan persists the exact preflight result consumed at run creation.
// CapabilityReport is raw JSON so newer registry fields survive older stores.
type RunPlan struct {
	ID                string                `json:"planId"`
	ThreadID          string                `json:"threadId"`
	RunID             *string               `json:"runId,omitempty"`
	Target            RunTarget             `json:"target"`
	ConfigFingerprint string                `json:"configFingerprint,omitempty"`
	RequirementsHash  string                `json:"requirementsHash"`
	ResultStatus      string                `json:"resultStatus"`
	Status            string                `json:"status"`
	Blockers          []PlanIssue           `json:"blockers"`
	Choices           []PlanChoice          `json:"choices"`
	Notices           []PlanIssue           `json:"notices"`
	StaleEvidence     bool                  `json:"staleEvidence"`
	CapabilityReport  json.RawMessage       `json:"plan"`
	Selections        []PlanChoiceSelection `json:"choiceSelections,omitempty"`
	CreatedAt         time.Time             `json:"createdAt"`
	ExpiresAt         time.Time             `json:"expiresAt"`
	FinalizedAt       *time.Time            `json:"finalizedAt,omitempty"`
}

type CreateRunParams struct {
	ThreadID      string
	ProviderID    string
	ConnectionID  string
	Model         string
	ProfileID     *string
	SystemPrompt  string
	MaxIterations int
}

// FinalizeRunPlanRequest is revalidated under the store transaction/lock.
type FinalizeRunPlanRequest struct {
	PlanID                          string
	ExpectedThreadID                string
	ExpectedTarget                  RunTarget
	ExpectedRequirementsHash        string
	ExpectedConfigFingerprint       string
	ExpectedThreadPolicyFingerprint string
	Selections                      []PlanChoiceSelection
	Run                             CreateRunParams
	Now                             time.Time
}

type ContextCheckpoint struct {
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

type ListArtifactsPageParams struct {
	ThreadID        string
	Type            string
	Limit           int
	BeforeCreatedAt *time.Time
	BeforeID        string
}

type ArtifactPage struct {
	Artifacts []Artifact
	HasMore   bool
}

type RunEvent struct {
	RunID     string          `json:"run_id"`
	ThreadID  string          `json:"thread_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	Data      json.RawMessage `json:"data"`
	CreatedAt time.Time       `json:"created_at"`
}

// ProviderCapabilityEvidence is the durable result of a provider capability
// probe. Its natural key prevents old fingerprints from being mistaken for
// current evidence while retaining them for audit/history.
type ProviderCapabilityEvidence struct {
	ConnectionID string    `json:"connectionId"`
	Model        string    `json:"model"`
	Fingerprint  string    `json:"fingerprint"`
	Probe        string    `json:"probe"`
	Verdict      string    `json:"verdict"`
	Reason       string    `json:"reason,omitempty"`
	Message      string    `json:"message"`
	RequestID    string    `json:"requestId,omitempty"`
	DurationMS   int64     `json:"durationMs"`
	VerifiedAt   time.Time `json:"verifiedAt"`
}

type ProviderCapabilityEvidenceFilter struct {
	ConnectionID string
	Model        string
	Fingerprint  string
	Probe        string
	Limit        int
}

type PermissionSubject struct {
	Kind       string   `json:"kind"`
	Tool       string   `json:"tool,omitempty"`
	Connector  string   `json:"connector,omitempty"`
	ToolSetID  string   `json:"tool_set_id,omitempty"`
	LocalRoot  string   `json:"local_root,omitempty"`
	LocalKind  string   `json:"local_kind,omitempty"`
	Repository string   `json:"repository,omitempty"`
	RemoteKind string   `json:"remote_kind,omitempty"`
	RemoteID   string   `json:"remote_id,omitempty"`
	Operation  string   `json:"operation,omitempty"`
	Excludes   []string `json:"excludes,omitempty"`
}

func DefaultSensitiveLocalPermissionExcludes() []string {
	return []string{
		".env",
		".env.*",
		"**/.env",
		"**/.env.*",
		".ssh/**",
		"**/.ssh/**",
		".aws/**",
		"**/.aws/**",
		".azure/**",
		"**/.azure/**",
		".config/gcloud/**",
		"**/.config/gcloud/**",
		".config/gh/**",
		"**/.config/gh/**",
		".docker/config.json",
		"**/.docker/config.json",
		".kube/**",
		"**/.kube/**",
		".gnupg/**",
		"**/.gnupg/**",
		".netrc",
		"**/.netrc",
		".npmrc",
		"**/.npmrc",
		".pypirc",
		"**/.pypirc",
		".git-credentials",
		"**/.git-credentials",
		"id_rsa",
		"id_dsa",
		"id_ecdsa",
		"id_ed25519",
		"*.pem",
		"*.key",
		"*.p12",
		"*.pfx",
		"credentials",
		"credentials/**",
		"credentials.json",
		"**/credentials",
		"**/credentials/**",
		"**/credentials.json",
		"token.json",
		"**/token.json",
		"*-service-account.json",
		"**/*-service-account.json",
	}
}

func LocalPathExcludedByPermissionPatterns(rel string, excludes []string) bool {
	rel = normalizeLocalExcludePath(rel)
	if rel == "" || rel == "." || len(excludes) == 0 {
		return false
	}
	for _, pattern := range excludes {
		if LocalPathMatchesPermissionExclude(rel, pattern) {
			return true
		}
	}
	return false
}

func LocalPathMatchesPermissionExclude(rel string, rawPattern string) bool {
	rel = normalizeLocalExcludePath(rel)
	pattern := normalizeLocalExcludePath(rawPattern)
	if rel == "" || rel == "." || pattern == "" || pattern == "." {
		return false
	}
	base := path.Base(rel)
	if rel == pattern || base == pattern {
		return true
	}
	if strings.HasPrefix(pattern, "**/") {
		tail := strings.TrimPrefix(pattern, "**/")
		if strings.HasSuffix(tail, "/**") {
			return localPathMatchesExcludedDir(rel, strings.TrimSuffix(tail, "/**"))
		}
		if rel == tail || strings.HasSuffix(rel, "/"+tail) {
			return true
		}
		if ok, _ := path.Match(tail, base); ok {
			return true
		}
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		return localPathMatchesExcludedDir(rel, strings.TrimSuffix(pattern, "/**"))
	}
	if ok, _ := path.Match(pattern, rel); ok {
		return true
	}
	if ok, _ := path.Match(pattern, base); ok {
		return true
	}
	return false
}

func normalizeLocalExcludePath(raw string) string {
	value := strings.TrimSpace(strings.ToLower(raw))
	value = strings.ReplaceAll(value, "\\", "/")
	value = strings.TrimPrefix(value, "./")
	value = strings.TrimPrefix(value, "/")
	value = strings.TrimSuffix(value, "/")
	return value
}

func localPathMatchesExcludedDir(rel string, dir string) bool {
	dir = normalizeLocalExcludePath(dir)
	if dir == "" || dir == "." {
		return false
	}
	return rel == dir ||
		strings.HasPrefix(rel, dir+"/") ||
		strings.HasSuffix(rel, "/"+dir) ||
		strings.Contains(rel, "/"+dir+"/")
}

type ApprovalDecision struct {
	Action    string             `json:"action"`
	Scope     string             `json:"scope"`
	Subject   *PermissionSubject `json:"subject,omitempty"`
	DecidedAt time.Time          `json:"decided_at"`
}

type ApprovalRequest struct {
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

type ApprovalFilter struct {
	ThreadID string
	RunID    string
	Status   string
	Limit    int
}

type LocalSearchJob struct {
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

type LocalSearchJobFilter struct {
	ThreadID string
	RunID    string
	Limit    int
}

type ListRunEventsParams struct {
	RunID    string
	AfterSeq int64
	Limit    int
}

type RunEventStore interface {
	AppendRunEvent(ctx context.Context, ev RunEvent) (RunEvent, error)
	ListRunEvents(ctx context.Context, p ListRunEventsParams) ([]RunEvent, error)
	LatestRunEventSeq(ctx context.Context, runID string) (int64, error)
}

type ListMessagesParams struct {
	ThreadID      string
	Limit         int
	BeforeSeq     *int64
	AfterSeq      *int64
	ArtifactIDs   []string
	MessageIDs    []string
	BeforeTS      *time.Time
	AfterTS       *time.Time
	IncludeHidden bool
	OrderBy       string
}

type Message struct {
	ID                    string           `json:"id"`
	ThreadID              string           `json:"thread_id"`
	RunID                 *string          `json:"run_id,omitempty"`
	Seq                   int64            `json:"seq"`
	Role                  types.Role       `json:"role"`
	Content               *string          `json:"content"`
	ProviderPayload       json.RawMessage  `json:"provider_payload,omitempty"`
	ReferencedMessageIDs  []string         `json:"referenced_message_ids,omitempty"`
	ReferencedThreadIDs   []string         `json:"referenced_thread_ids,omitempty"`
	ReferencedArtifactIDs []string         `json:"referenced_artifact_ids,omitempty"`
	ContextSummary        string           `json:"context_summary,omitempty"`
	ContextPlan           json.RawMessage  `json:"context_plan,omitempty"`
	HiddenContext         *string          `json:"hidden_context,omitempty"`
	Thinking              *string          `json:"thinking,omitempty"`
	ThinkingComplete      bool             `json:"thinkingComplete"`
	ToolCalls             []types.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID            *string          `json:"tool_call_id,omitempty"`
	Name                  *string          `json:"name,omitempty"`
	UIHidden              bool             `json:"ui_hidden"`
	CreatedAt             time.Time        `json:"created_at"`
	UpdatedAt             time.Time        `json:"updated_at"`
}

type Artifact struct {
	ID          string         `json:"id"`
	ThreadID    string         `json:"thread_id"`
	MessageID   *string        `json:"message_id,omitempty"`
	Type        string         `json:"type"`
	Title       string         `json:"title"`
	Language    *string        `json:"language,omitempty"`
	ToolName    *string        `json:"tool_name,omitempty"`
	ToolCallID  *string        `json:"tool_call_id,omitempty"`
	OK          *bool          `json:"ok,omitempty"`
	Storage     string         `json:"content_storage"`
	ContentText *string        `json:"content_text,omitempty"`
	ContentPath *string        `json:"content_path,omitempty"`
	ByteSize    int64          `json:"byte_size"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	CreatedAt   time.Time      `json:"created_at"`
}

type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	RootRelPath string    `json:"root_rel_path"`
	FileCount   int       `json:"file_count"`
	TotalBytes  int64     `json:"total_bytes"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type ProjectFileStat struct {
	Path     string `json:"path"`
	ByteSize int64  `json:"byte_size"`
	SHA256   []byte `json:"sha256,omitempty"`
}

type Job struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	ThreadID  string    `json:"thread_id"`
	RunID     *string   `json:"run_id,omitempty"`
	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	NotBefore time.Time `json:"not_before"`
	LastError *string   `json:"last_error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store interface {
	Ping(ctx context.Context) error

	CreateThread(ctx context.Context, projectID *string) (Thread, error)
	ListThreads(ctx context.Context, status string, limit int) ([]Thread, error)
	GetThread(ctx context.Context, threadID string) (Thread, error)
	ArchiveThread(ctx context.Context, threadID string) error
	DeleteThread(ctx context.Context, threadID string) error
	UpdateThreadTitleSummary(ctx context.Context, threadID string, title *string, summary *string) error
	UpdateThreadProject(ctx context.Context, threadID string, projectID string) error
	IncrementThreadRunCount(ctx context.Context, threadID string) error
	SetThreadTarget(ctx context.Context, threadID string, target RunTarget) (Thread, error)
	DeleteThreadAttachmentPolicy(ctx context.Context, threadID string, policyID string) (Thread, error)

	CreateRun(ctx context.Context, threadID string, providerID string, model string, profileID *string, systemPrompt string, maxIterations int) (Run, error)
	CreateRunPlan(ctx context.Context, plan RunPlan) (RunPlan, error)
	GetRunPlan(ctx context.Context, planID string) (RunPlan, error)
	GetRunPlanForRun(ctx context.Context, runID string) (RunPlan, error)
	FinalizeRunPlanAndCreateRun(ctx context.Context, req FinalizeRunPlanRequest) (Run, RunPlan, error)
	DeleteExpiredRunPlans(ctx context.Context, now time.Time) (int64, error)
	GetRun(ctx context.Context, runID string) (Run, error)
	ListRuns(ctx context.Context, threadID string, limit int) ([]Run, error)
	UpdateRunStatus(ctx context.Context, runID string, status string, fields map[string]any) error
	AppendRunEvent(ctx context.Context, ev RunEvent) (RunEvent, error)
	ListRunEvents(ctx context.Context, p ListRunEventsParams) ([]RunEvent, error)
	LatestRunEventSeq(ctx context.Context, runID string) (int64, error)
	UpsertProviderCapabilityEvidence(ctx context.Context, evidence ProviderCapabilityEvidence) (ProviderCapabilityEvidence, error)
	ListProviderCapabilityEvidence(ctx context.Context, filter ProviderCapabilityEvidenceFilter) ([]ProviderCapabilityEvidence, error)

	CreateApprovalRequest(ctx context.Context, approval ApprovalRequest) (ApprovalRequest, error)
	GetApprovalRequest(ctx context.Context, approvalID string) (ApprovalRequest, error)
	ListApprovalRequests(ctx context.Context, filter ApprovalFilter) ([]ApprovalRequest, error)
	DecideApprovalRequest(ctx context.Context, approvalID string, decision ApprovalDecision, status string) (ApprovalRequest, error)
	CancelRunApprovalRequests(ctx context.Context, runID string) error
	ExpireStaleApprovalRequests(ctx context.Context, now time.Time) error

	UpsertLocalSearchJob(ctx context.Context, job LocalSearchJob) (LocalSearchJob, error)
	GetLocalSearchJob(ctx context.Context, jobID string) (LocalSearchJob, error)
	ListLocalSearchJobs(ctx context.Context, filter LocalSearchJobFilter) ([]LocalSearchJob, error)

	GetActiveContextCheckpoint(ctx context.Context, threadID string) (*ContextCheckpoint, error)
	UpsertActiveContextCheckpoint(ctx context.Context, checkpoint ContextCheckpoint) (ContextCheckpoint, error)
	ListContextCheckpoints(ctx context.Context, threadID string, limit int) ([]ContextCheckpoint, error)

	AppendMessage(ctx context.Context, threadID string, runID *string, msg Message) (Message, error)
	UpdateMessage(ctx context.Context, messageID string, fields map[string]any) error
	ListMessages(ctx context.Context, p ListMessagesParams) ([]Message, error)

	InsertArtifact(ctx context.Context, a Artifact) (Artifact, error)
	// ReplaceArtifact overwrites an existing artifact record in place (same
	// ID, same thread). Used to upgrade async-query placeholder artifacts to
	// their completed results once get_query_results lands; returns
	// ErrNotFound when the artifact does not exist.
	ReplaceArtifact(ctx context.Context, a Artifact) (Artifact, error)
	GetArtifact(ctx context.Context, artifactID string) (Artifact, error)
	DeleteArtifact(ctx context.Context, artifactID string) error
	// ListArtifacts returns artifacts for a thread when threadID is set. An
	// empty threadID lists artifacts across all threads, newest records bounded
	// by limit.
	ListArtifacts(ctx context.Context, threadID string, limit int) ([]Artifact, error)
	// ListFileArtifacts is the same listing shape filtered to first-class file
	// artifacts so file audit surfaces are not skewed by data/code/image output.
	ListFileArtifacts(ctx context.Context, threadID string, limit int) ([]Artifact, error)

	CreateProject(ctx context.Context, name string, rootRelPath string) (Project, error)
	ListProjects(ctx context.Context, limit int) ([]Project, error)
	GetProject(ctx context.Context, projectID string) (Project, error)
	UpsertProjectFile(ctx context.Context, projectID string, path string, byteSize int64, sha256 []byte) error
	ReplaceProjectFiles(ctx context.Context, projectID string, files []ProjectFileStat) (Project, error)
	FinalizeProject(ctx context.Context, projectID string) (Project, error)

	EnqueueJob(ctx context.Context, kind string, threadID string, runID *string) (Job, error)
	ClaimNextJob(ctx context.Context, now time.Time) (*Job, error)
	CompleteJob(ctx context.Context, jobID string) error
	FailJob(ctx context.Context, jobID string, errMsg string, notBefore time.Time) error
}
