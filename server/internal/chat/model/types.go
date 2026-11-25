package model

import (
	"encoding/json"
	"time"
)

type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

func (r MessageRole) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

type RunStatus string

const (
	RunStatusQueued    RunStatus = "queued"
	RunStatusRunning   RunStatus = "running"
	RunStatusCompleted RunStatus = "completed"
	RunStatusFailed    RunStatus = "failed"
	RunStatusCancelled RunStatus = "cancelled"
)

func (s RunStatus) Terminal() bool {
	switch s {
	case RunStatusCompleted, RunStatusFailed, RunStatusCancelled:
		return true
	default:
		return false
	}
}

type Thread struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	CreatedAt time.Time      `json:"created_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type Message struct {
	ID        string         `json:"id"`
	Object    string         `json:"object"`
	ThreadID  string         `json:"thread_id"`
	Role      MessageRole    `json:"role"`
	Content   string         `json:"content"`
	CreatedAt time.Time      `json:"created_at"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type RunError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Run struct {
	ID           string         `json:"id"`
	Object       string         `json:"object"`
	ThreadID     string         `json:"thread_id"`
	Status       RunStatus      `json:"status"`
	CreatedAt    time.Time      `json:"created_at"`
	StartedAt    *time.Time     `json:"started_at,omitempty"`
	CompletedAt  *time.Time     `json:"completed_at,omitempty"`
	Instructions string         `json:"instructions,omitempty"`
	Error        *RunError      `json:"error,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type RunEvent struct {
	RunID     string          `json:"run_id"`
	ThreadID  string          `json:"thread_id"`
	Seq       int64           `json:"seq"`
	Type      string          `json:"type"`
	CreatedAt time.Time       `json:"created_at"`
	Data      json.RawMessage `json:"data"`
}
