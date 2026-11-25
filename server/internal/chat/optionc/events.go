package optionc

import (
	"bytes"
	"fmt"
	"io"
	"strings"
)

const (
	EventLocalProgress = "local_progress"
	EventRunDiagnostic = "run_diagnostic"
	EventRunTerminal   = "run_terminal"

	LocalPhaseContextPlanning      = "context_planning"
	LocalPhaseContextCompacting    = "context_compacting"
	LocalPhaseToolResultCompacting = "tool_result_compacting"

	LocalProgressStatusStarted   = "started"
	LocalProgressStatusCompleted = "completed"
	LocalProgressStatusFailed    = "failed"
)

type LocalProgressPayload struct {
	Phase         string `json:"phase"`
	Status        string `json:"status"`
	Timestamp     int64  `json:"timestamp"`
	Iteration     int    `json:"iteration,omitempty"`
	ToolCallCount int    `json:"tool_call_count,omitempty"`
	TokenBudget   int    `json:"token_budget,omitempty"`
	TokensBefore  int    `json:"tokens_before,omitempty"`
	TokensAfter   int    `json:"tokens_after,omitempty"`
	Compacted     bool   `json:"compacted"`
}

type RunDiagnosticPayload struct {
	Code       string `json:"code,omitempty"`
	Message    string `json:"message"`
	Visibility string `json:"visibility,omitempty"`
	Timestamp  int64  `json:"timestamp"`
}

type RunTerminalPayload struct {
	RunID     string `json:"run_id"`
	Status    string `json:"status"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Timestamp int64  `json:"timestamp"`
}

// SSEEvent is a single Server-Sent Events message.
type SSEEvent struct {
	Event string
	Data  []byte // JSON payload; nil/empty means "{}"
	Seq   int64  // Monotonic in-memory sequence assigned by Manager.broadcast.
}

// WriteSSETo serializes the event using the SSE wire format. It is named
// explicitly instead of WriteTo so it is not mistaken for io.WriterTo,
// whose contract also requires a byte count.
func (e SSEEvent) WriteSSETo(w io.Writer) error {
	if e.Seq > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", e.Seq); err != nil {
			return err
		}
	}
	if strings.TrimSpace(e.Event) != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", e.Event); err != nil {
			return err
		}
	}
	data := e.Data
	if len(bytes.TrimSpace(data)) == 0 {
		data = []byte("{}")
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err := io.WriteString(w, "\n")
	return err
}
