package optionc

import (
	"strings"

	"ai-analyst-local/internal/chat/store"
)

// ActiveRunConflictError indicates a thread already has an active run.
// It unwraps to store.ErrConflict so existing conflict handling still works.
type ActiveRunConflictError struct {
	RunID string
}

func (e *ActiveRunConflictError) Error() string {
	return "another run is active for this thread"
}

func (e *ActiveRunConflictError) Unwrap() error {
	return store.ErrConflict
}

func (e *ActiveRunConflictError) ActiveRunID() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.RunID)
}
