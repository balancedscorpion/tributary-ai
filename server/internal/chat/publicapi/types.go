package publicapi

import (
	"context"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

type RunManager interface {
	StartRun(ctx context.Context, req optionc.StartRunRequest) (optionc.StartRunResult, error)
	Subscribe(runID string) (<-chan optionc.SSEEvent, func(), error)
	Cancel(runID string) error
	GetQueryJobStatus(ctx context.Context, jobID, threadID, ownerToken string) (map[string]any, error)
	CancelQueryJob(ctx context.Context, jobID, threadID, ownerToken string) (map[string]any, error)
}

type Deps struct {
	Store    store.Store
	Manager  RunManager
	Settings *config.Store
	CfgDir   string

	MaxBodyBytes int64
}

type Handler struct {
	store        store.Store
	manager      RunManager
	settings     *config.Store
	cfgDir       string
	maxBodyBytes int64
}

type createRunRequest struct {
	ThreadID       string                `json:"thread_id,omitempty"`
	ProjectID      string                `json:"project_id,omitempty"`
	Input          createRunInput        `json:"input"`
	Purpose        string                `json:"purpose,omitempty"`
	SearchDepth    string                `json:"search_depth,omitempty"`
	MaxIterations  *int                  `json:"max_iterations,omitempty"`
	DataSources    map[string]any        `json:"data_sources,omitempty"`
	RuntimeContext runtimeContextRequest `json:"runtime_context,omitempty"`
	Stream         bool                  `json:"stream,omitempty"`
}

type createRunInput struct {
	Content               string                  `json:"content,omitempty"`
	HiddenContext         string                  `json:"hidden_context,omitempty"`
	ReferencedArtifactIDs []string                `json:"referenced_artifact_ids,omitempty"`
	ReferencedMessageIDs  []string                `json:"referenced_message_ids,omitempty"`
	ReferencedThreadIDs   []string                `json:"referenced_thread_ids,omitempty"`
	ImageAttachments      []types.ImageAttachment `json:"image_attachments,omitempty"`
	FileAttachments       []types.FileAttachment  `json:"file_attachments,omitempty"`
}

type runtimeContextRequest struct {
	NowISO           string `json:"now_iso,omitempty"`
	Timezone         string `json:"timezone,omitempty"`
	UTCOffsetMinutes *int   `json:"utc_offset_minutes,omitempty"`
}
