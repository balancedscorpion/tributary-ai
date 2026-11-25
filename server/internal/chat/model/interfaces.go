package model

import (
	"context"
	"time"
)

type CreateThreadParams struct {
	ID       string
	Metadata map[string]any
}

type ListThreadsParams struct {
	Limit int
	After string
}

type CreateMessageParams struct {
	ID       string
	ThreadID string
	Role     MessageRole
	Content  string
	Metadata map[string]any
}

type ListMessagesParams struct {
	ThreadID string
	Limit    int
	After    string
}

type CreateRunParams struct {
	ID           string
	ThreadID     string
	Instructions string
	Metadata     map[string]any
}

type ListRunsParams struct {
	ThreadID string
	Limit    int
	After    string
}

type UpdateRunPatch struct {
	Status      *RunStatus
	StartedAt   *time.Time
	CompletedAt *time.Time
	Error       *RunError
}

type ListRunEventsParams struct {
	RunID    string
	AfterSeq int64
	Limit    int
}

// Store abstracts thread/message/run persistence for the HTTP API and run manager.
//
// A concrete implementation will live in the chat store layer.
// This interface is designed for unit testing and handler composition.
//
// NOTE: This is intentionally separate from any SQL store implementation types.
//
// Errors should use ErrNotFound/ErrConflict/ErrInvalid/ErrForbidden when applicable.
//
// Returned objects should be fully populated for JSON responses.
//
// Implementations may ignore cursor params for now.
//
// All timestamps should be in UTC.
//
// RunEvent.Data should be the JSON-encoded SSE payload for the event.
//
// Thread/Object fields are optional; the API layer will default them if empty.
//
// Similarly for Message/Object and Run/Object.
//
// Limit values are advisory.
//
// List* should return results in ascending chronological order.
type Store interface {
	// Threads
	CreateThread(ctx context.Context, p CreateThreadParams) (Thread, error)
	GetThread(ctx context.Context, threadID string) (Thread, error)
	ListThreads(ctx context.Context, p ListThreadsParams) ([]Thread, error)

	// Messages
	CreateMessage(ctx context.Context, p CreateMessageParams) (Message, error)
	ListMessages(ctx context.Context, p ListMessagesParams) ([]Message, error)

	// Runs
	CreateRun(ctx context.Context, p CreateRunParams) (Run, error)
	GetRun(ctx context.Context, runID string) (Run, error)
	ListRuns(ctx context.Context, p ListRunsParams) ([]Run, error)
	UpdateRun(ctx context.Context, runID string, patch UpdateRunPatch) (Run, error)

	// Run events
	AppendRunEvent(ctx context.Context, ev RunEvent) error
	ListRunEvents(ctx context.Context, p ListRunEventsParams) ([]RunEvent, error)
}
