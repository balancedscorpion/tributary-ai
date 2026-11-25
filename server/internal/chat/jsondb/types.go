package jsondb

import (
	"encoding/json"
	"time"

	"ai-analyst-local/internal/chat/types"
)

const SchemaVersion = 1

type Kind string

const (
	KindMeta        Kind = "ai_analyst.meta"
	KindThread      Kind = "ai_analyst.thread"
	KindRun         Kind = "ai_analyst.run"
	KindMessage     Kind = "ai_analyst.message"
	KindArtifact    Kind = "ai_analyst.artifact"
	KindProject     Kind = "ai_analyst.project"
	KindProjectFile Kind = "ai_analyst.project_file"
	KindJob         Kind = "ai_analyst.job"
	KindSearchDoc   Kind = "ai_analyst.search_doc"
)

type Meta struct {
	Kind          Kind      `json:"kind"`
	SchemaVersion int       `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type ThreadCounters struct {
	NextSeq         int64 `json:"next_seq"`
	MessageCount    int64 `json:"message_count"`
	ArtifactCount   int64 `json:"artifact_count"`
	RunCount        int64 `json:"run_count"`
	SummaryRunCount int64 `json:"summary_run_count"`
}

type Thread struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID        string  `json:"id"`
	Title     string  `json:"title"`
	Summary   *string `json:"summary,omitempty"`
	ProjectID *string `json:"project_id,omitempty"`
	Status    string  `json:"status"`

	CreatedAt          time.Time          `json:"created_at"`
	UpdatedAt          time.Time          `json:"updated_at"`
	LastMessageAt      *time.Time         `json:"last_message_at,omitempty"`
	LastRunID          *string            `json:"last_run_id,omitempty"`
	LastTarget         *RunTarget         `json:"last_target,omitempty"`
	AttachmentPolicies []AttachmentPolicy `json:"attachment_policies,omitempty"`

	Counters ThreadCounters `json:"counters"`
	Tags     []string       `json:"tags,omitempty"`
}

type RunTarget struct {
	ConnectionID string `json:"connectionId"`
	Model        string `json:"model"`
}

type AttachmentPolicy struct {
	ID         string `json:"id"`
	Class      string `json:"class"`
	Action     string `json:"action"`
	APISurface string `json:"apiSurface"`
	CreatedAt  int64  `json:"createdAt"`
}

type RunPurpose string

const (
	RunPurposeAnalysis RunPurpose = "analysis"
	RunPurposeSearch   RunPurpose = "search"
)

type Usage struct {
	RequestID    *string `json:"request_id,omitempty"`
	InputTokens  *int    `json:"input_tokens,omitempty"`
	OutputTokens *int    `json:"output_tokens,omitempty"`
	TotalTokens  *int    `json:"total_tokens,omitempty"`
}

type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Run struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID       string `json:"id"`
	ThreadID string `json:"thread_id"`
	Status   string `json:"status"`
	Revision int64  `json:"revision"`

	Purpose      RunPurpose `json:"purpose,omitempty"`
	ProviderID   string     `json:"provider_id"`
	Model        string     `json:"model"`
	ConnectionID string     `json:"connection_id,omitempty"`
	PlanID       string     `json:"plan_id,omitempty"`
	ProfileID    *string    `json:"profile_id,omitempty"`

	SystemPrompt           string     `json:"system_prompt"`
	MaxIterations          int        `json:"max_iterations"`
	ContinuationCount      int        `json:"continuation_count"`
	MaxAutoContinuations   int        `json:"max_auto_continuations"`
	LastCheckpointSequence int64      `json:"last_checkpoint_sequence"`
	WakeAt                 *time.Time `json:"wake_at,omitempty"`
	ResumeLeaseHolder      string     `json:"resume_lease_holder,omitempty"`
	ResumeLeaseExpiresAt   *time.Time `json:"resume_lease_expires_at,omitempty"`

	UserMessageID *string `json:"user_message_id,omitempty"`

	CreatedAt time.Time  `json:"created_at"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	UpdatedAt time.Time  `json:"updated_at"`

	Usage Usage     `json:"usage,omitempty"`
	Error *RunError `json:"error,omitempty"`

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
}

type MessageUI struct {
	Hidden                bool     `json:"hidden"`
	ReferencedMessageIDs  []string `json:"referenced_message_ids,omitempty"`
	ReferencedThreadIDs   []string `json:"referenced_thread_ids,omitempty"`
	ReferencedArtifactIDs []string `json:"referenced_artifact_ids,omitempty"`
	HiddenContext         *string  `json:"hidden_context,omitempty"`
}

type Message struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID       string  `json:"id"`
	ThreadID string  `json:"thread_id"`
	RunID    *string `json:"run_id,omitempty"`
	Seq      int64   `json:"seq"`

	ReferencedMessageIDs  []string        `json:"referenced_message_ids,omitempty"`
	ReferencedThreadIDs   []string        `json:"referenced_thread_ids,omitempty"`
	ReferencedArtifactIDs []string        `json:"referenced_artifact_ids,omitempty"`
	ContextSummary        string          `json:"context_summary,omitempty"`
	ContextPlan           json.RawMessage `json:"context_plan,omitempty"`
	HiddenContext         *string         `json:"hidden_context,omitempty"`

	Role    types.Role `json:"role"`
	Content *string    `json:"content,omitempty"`

	ProviderPayload json.RawMessage `json:"provider_payload,omitempty"`

	Thinking         *string          `json:"thinking,omitempty"`
	ThinkingComplete bool             `json:"thinking_complete"`
	ToolCalls        []types.ToolCall `json:"tool_calls,omitempty"`

	ToolCallID *string `json:"tool_call_id,omitempty"`
	Name       *string `json:"name,omitempty"`

	UI MessageUI `json:"ui"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ArtifactContent struct {
	Storage  string  `json:"storage"`
	Text     *string `json:"text,omitempty"`
	Path     *string `json:"path,omitempty"`
	ByteSize int64   `json:"byte_size"`
	SHA256   *string `json:"sha256,omitempty"`
}

type Artifact struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID        string  `json:"id"`
	ThreadID  string  `json:"thread_id"`
	MessageID *string `json:"message_id,omitempty"`

	Type     string  `json:"type"`
	Title    string  `json:"title"`
	Language *string `json:"language,omitempty"`

	ToolName   *string `json:"tool_name,omitempty"`
	ToolCallID *string `json:"tool_call_id,omitempty"`
	OK         *bool   `json:"ok,omitempty"`

	Content   ArtifactContent `json:"content"`
	Metadata  map[string]any  `json:"metadata,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
}

type Project struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID          string `json:"id"`
	Name        string `json:"name"`
	RootRelPath string `json:"root_rel_path"`
	FileCount   int    `json:"file_count"`
	TotalBytes  int64  `json:"total_bytes"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ProjectFile struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ProjectID string    `json:"project_id"`
	Path      string    `json:"path"`
	ByteSize  int64     `json:"byte_size"`
	SHA256    *string   `json:"sha256,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Job struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID       string  `json:"id"`
	KindName string  `json:"kind_name"`
	ThreadID string  `json:"thread_id"`
	RunID    *string `json:"run_id,omitempty"`

	Status    string    `json:"status"`
	Attempts  int       `json:"attempts"`
	NotBefore time.Time `json:"not_before"`
	LastError *string   `json:"last_error,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SearchDoc struct {
	Kind          Kind `json:"kind"`
	SchemaVersion int  `json:"schema_version"`

	ID      string `json:"id"`
	DocKind string `json:"doc_kind"`

	ThreadID   string  `json:"thread_id"`
	RunID      *string `json:"run_id,omitempty"`
	MessageID  *string `json:"message_id,omitempty"`
	ArtifactID *string `json:"artifact_id,omitempty"`

	Status    string  `json:"status,omitempty"`
	ProjectID *string `json:"project_id,omitempty"`

	Title string `json:"title"`
	Text  string `json:"text"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	Tags []string `json:"tags,omitempty"`
}
