package publicapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/optionc"
	"ai-analyst-local/internal/chat/store"
)

const (
	EventRunStarted         = "run_started"
	EventAssistantStarted   = "assistant_started"
	EventTextDelta          = "text_delta"
	EventThinkingStart      = "thinking_start"
	EventThinkingDelta      = "thinking_delta"
	EventThinkingEnd        = "thinking_end"
	EventCompactionStart    = "compaction_start"
	EventCompactionDelta    = "compaction_delta"
	EventCompactionEnd      = "compaction_end"
	EventToolCalls          = "tool_calls"
	EventToolProgress       = "tool_progress"
	EventToolResult         = "tool_result"
	EventArtifactCreated    = "artifact_created"
	EventAssistantCompleted = "assistant_completed"
	EventRunCompleted       = "run_completed"
	EventRunFailed          = "run_failed"
	EventRunCancelled       = "run_cancelled"
	EventError              = "error"
)

type PublicEvent struct {
	Object    string         `json:"object"`
	RunID     string         `json:"run_id"`
	ThreadID  string         `json:"thread_id"`
	Sequence  int64          `json:"sequence"`
	Type      string         `json:"type"`
	CreatedAt int64          `json:"created_at"`
	Data      map[string]any `json:"data"`
}

func mapSSEEvent(run store.Run, internal optionc.SSEEvent, sequence int64) (PublicEvent, error) {
	payload, err := decodeEventPayload(internal.Data)
	if err != nil {
		return PublicEvent{}, err
	}
	eventType := strings.TrimSpace(internal.Event)
	outType := eventType
	data := payload
	switch eventType {
	case "run_started":
		outType = EventRunStarted
		data = map[string]any{"run": publicRunDTO(run)}
	case "assistant_start":
		outType = EventAssistantStarted
	case "text_delta":
		outType = EventTextDelta
	case "thinking_start":
		outType = EventThinkingStart
		renameField(data, "traceId", "trace_id")
	case "thinking_delta":
		outType = EventThinkingDelta
	case "thinking_end":
		outType = EventThinkingEnd
	case "compaction_start":
		outType = EventCompactionStart
	case "compaction_delta":
		outType = EventCompactionDelta
	case "compaction_end":
		outType = EventCompactionEnd
	case "tool_calls":
		outType = EventToolCalls
		if v, ok := data["toolCalls"]; ok {
			data["tool_calls"] = v
			delete(data, "toolCalls")
		}
	case "tool_progress":
		outType = EventToolProgress
	case "tool_result":
		outType = EventToolResult
		if msg, ok := data["message"].(map[string]any); ok {
			publicMessage := publicMessageFromMap(msg)
			data["message"] = publicMessage
			if toolResult, ok := publicMessage["tool_result"]; ok {
				data["tool_result"] = toolResult
			}
		}
	case "artifact":
		outType = EventArtifactCreated
		if _, ok := data["artifact"]; !ok {
			data = map[string]any{"artifact": data}
		}
	case "assistant_final":
		outType = EventAssistantCompleted
		if msg, ok := data["message"].(map[string]any); ok {
			data["message"] = publicMessageFromMap(msg)
		}
	case "final":
		switch strings.TrimSpace(strings.ToLower(run.Status)) {
		case "cancelled":
			outType = EventRunCancelled
		case "failed":
			outType = EventRunFailed
		default:
			outType = EventRunCompleted
		}
		data = map[string]any{"run": publicRunDTO(run)}
		if errObj := publicRunError(run); errObj != nil {
			data["error"] = errObj
		}
	case "error":
		outType = EventError
		if _, ok := data["error"]; !ok {
			message := strings.TrimSpace(stringValue(data["message"]))
			if message == "" {
				message = strings.TrimSpace(stringValue(data["error"]))
			}
			if message == "" {
				message = "unknown error"
			}
			data = map[string]any{"error": map[string]any{"code": "stream_error", "message": message}}
		}
	}
	return PublicEvent{
		Object:    "run.event",
		RunID:     run.ID,
		ThreadID:  run.ThreadID,
		Sequence:  sequence,
		Type:      outType,
		CreatedAt: time.Now().UnixMilli(),
		Data:      data,
	}, nil
}

func publicRunError(run store.Run) map[string]any {
	if run.ErrorCode == nil && run.ErrorMessage == nil {
		return nil
	}
	errObj := map[string]any{}
	if run.ErrorCode != nil {
		errObj["code"] = *run.ErrorCode
	}
	if run.ErrorMessage != nil {
		errObj["message"] = *run.ErrorMessage
	}
	return errObj
}

func decodeEventPayload(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, err
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return payload, nil
}

func renameField(m map[string]any, from string, to string) {
	if value, ok := m[from]; ok {
		m[to] = value
		delete(m, from)
	}
}

func writeSSE(w io.Writer, eventType string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if strings.TrimSpace(eventType) != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", eventType); err != nil {
			return err
		}
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return err
		}
	}
	_, err = io.WriteString(w, "\n")
	return err
}

func writeSSEComment(w io.Writer, comment string) error {
	comment = strings.ReplaceAll(comment, "\n", " ")
	_, err := fmt.Fprintf(w, ": %s\n\n", comment)
	return err
}
