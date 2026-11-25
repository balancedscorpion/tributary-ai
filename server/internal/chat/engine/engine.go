package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
)

type Callbacks struct {
	OnAssistantStart       func()
	OnTextDelta            func(delta string)
	OnThinkingStart        func(traceID string)
	OnThinkingDelta        func(delta string)
	OnThinkingEnd          func(summary *string)
	OnCompactionStart      func()
	OnCompactionDelta      func(delta string)
	OnCompactionEnd        func()
	OnLocalProgress        func(progress LocalProgressEvent)
	OnProviderToolProgress func(progress types.ProviderToolProgressData)
	OnPlanUpdate           func(update types.PlanUpdateData)
	OnAssistantFinal       func(message types.ChatMessage, meta *types.ResponseMeta)
	OnToolResult           func(message types.ChatMessage)
	OnError                func(message string)
}

const (
	LocalPhaseContextCompacting    = "context_compacting"
	LocalPhaseToolResultCompacting = "tool_result_compacting"

	LocalProgressStatusStarted   = "started"
	LocalProgressStatusCompleted = "completed"

	nativeImageEstimateTokens = 1024
	nativeFileEstimateTokens  = 2048
)

type LocalProgressEvent struct {
	Phase         string
	Status        string
	Iteration     int
	ToolCallCount int
	TokenBudget   int
	TokensBefore  int
	TokensAfter   int
	Compacted     bool
}

// LoopControlPoint is an exact, immutable view of the agent loop at a safe
// suspension boundary. AfterToolBatch is invoked only after every tool result
// in the batch has been appended and emitted. BeforeAssistantFinal is invoked
// before a candidate final assistant message is appended or persisted.
//
// Callers must treat History, ToolCalls, ToolResults, and ProviderState as
// read-only. The engine clones them before invoking a control callback so a
// continuation checkpoint cannot be changed by later loop work.
type LoopControlPoint struct {
	Iteration     int
	History       []types.ChatMessage
	ProviderState map[string]any
	ToolCalls     []types.ToolCall
	ToolResults   []types.ChatMessage
	Candidate     *types.ChatMessage
}

// LoopSuspension describes why orchestration intentionally stopped model
// execution without completing or failing the task. DependencyIDs normally
// contains durable external job IDs that must wake the continuation.
type LoopSuspension struct {
	Reason        string
	DependencyIDs []string
}

// LoopSuspendedError is a non-terminal control result. The returned history
// is the exact checkpoint represented by Point.
type LoopSuspendedError struct {
	Suspension LoopSuspension
	Point      LoopControlPoint
}

func (e *LoopSuspendedError) Error() string {
	reason := strings.TrimSpace(e.Suspension.Reason)
	if reason == "" {
		reason = "external work is still running"
	}
	return "agent loop suspended: " + reason
}

// MaxIterationsError is deliberately typed so orchestration can schedule an
// alternative continuation instead of persisting a synthetic failure answer.
type MaxIterationsError struct {
	Limit               int
	LastToolCalls       []types.ToolCall
	LastToolCallSummary string
	// Point is the exact safe boundary at the end of the exhausted segment.
	// It includes provider-native continuation state; callers must resume from
	// it rather than from message history alone.
	Point LoopControlPoint
}

func (e *MaxIterationsError) Error() string {
	if strings.TrimSpace(e.LastToolCallSummary) == "" {
		return fmt.Sprintf("agent loop exhausted its %d tool iterations", e.Limit)
	}
	return fmt.Sprintf("agent loop exhausted its %d tool iterations; last tool calls: %s", e.Limit, e.LastToolCallSummary)
}

// LoopCheckpointError means a safe continuation boundary was reached, but the
// exact provider/tool state could not be copied into a durable checkpoint. The
// engine deliberately refuses to suspend with a shallow, mutable snapshot.
type LoopCheckpointError struct {
	Cause error
}

func (e *LoopCheckpointError) Error() string {
	if e == nil || e.Cause == nil {
		return "agent loop checkpoint is not serializable"
	}
	return "agent loop checkpoint is not serializable: " + e.Cause.Error()
}

func (e *LoopCheckpointError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type LoopControl func(point LoopControlPoint) *LoopSuspension

type Params struct {
	ProviderFactory func() (types.Provider, error)
	// DispatchOrdinal is shared across outer context-recovery loops. The first
	// provider call is chat (ordinal zero); every later call is a follow-up even
	// when the manager restarts RunAgentLoop after a context overflow.
	DispatchOrdinal *int

	SystemPrompt  string
	Model         string
	History       []types.ChatMessage
	Tools         []types.ToolDefinition
	ProviderState map[string]any
	RuntimePlan   *types.RuntimePlanPolicy

	ExecuteTool func(call types.ToolCall) (types.ChatMessage, error)
	// AfterToolBatch may suspend at the durable boundary after a complete tool
	// batch. BeforeAssistantFinal is a completion barrier: it runs before the
	// final candidate is appended or sent to OnAssistantFinal.
	AfterToolBatch       LoopControl
	BeforeAssistantFinal LoopControl

	Callbacks      *Callbacks
	MaxIterations  int
	MaxInputTokens int
	CompactHistory func(history []types.ChatMessage, tokenBudget int, systemPrompt string, tools []types.ToolDefinition) ([]types.ChatMessage, bool)
	TokenEstimator func(systemPrompt string, history []types.ChatMessage, tools []types.ToolDefinition) int
}

var ErrPromptContextTooLarge = errors.New("prompt context exceeds hard cap")

var providerStreamIdleTimeout = 5 * time.Minute

func safeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "\"[unserializable]\""
	}
	return string(b)
}

func summarizeToolCalls(calls []types.ToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, fmt.Sprintf("%s(%s)", c.Name, safeJSON(c.Args)))
	}
	joined := strings.Join(parts, ", ")
	if len(joined) > 600 {
		return joined[:600] + "..."
	}
	return joined
}

func parseCompactionDeltaData(data any) (types.CompactionDeltaData, bool) {
	switch d := data.(type) {
	case types.CompactionDeltaData:
		return d, true
	case *types.CompactionDeltaData:
		if d == nil {
			return types.CompactionDeltaData{}, false
		}
		return *d, true
	case map[string]any:
		if raw, ok := d["content"]; ok {
			switch content := raw.(type) {
			case string:
				return types.CompactionDeltaData{Content: content}, true
			case fmt.Stringer:
				return types.CompactionDeltaData{Content: content.String()}, true
			case nil:
				return types.CompactionDeltaData{}, true
			default:
				return types.CompactionDeltaData{Content: fmt.Sprintf("%v", content)}, true
			}
		}
	case map[string]string:
		if content, ok := d["content"]; ok {
			return types.CompactionDeltaData{Content: content}, true
		}
	}

	var parsed types.CompactionDeltaData
	b, err := json.Marshal(data)
	if err != nil {
		return types.CompactionDeltaData{}, false
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return types.CompactionDeltaData{}, false
	}
	return parsed, true
}

func cloneProviderState(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	b, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func cloneRawJSON(in json.RawMessage) json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(in))
	copy(out, in)
	return out
}

func providerStreamTimeoutMessage(timeout time.Duration) string {
	if timeout <= 0 {
		return "Provider stream timed out without new data."
	}
	return fmt.Sprintf("Provider stream timed out after %s without new data.", timeout.Round(time.Second))
}

func cloneChatMessages(in []types.ChatMessage) []types.ChatMessage {
	if len(in) == 0 {
		return nil
	}
	encoded, err := json.Marshal(in)
	if err == nil {
		var out []types.ChatMessage
		if json.Unmarshal(encoded, &out) == nil {
			return out
		}
	}
	return append([]types.ChatMessage(nil), in...)
}

func cloneToolCalls(in []types.ToolCall) []types.ToolCall {
	if len(in) == 0 {
		return nil
	}
	encoded, err := json.Marshal(in)
	if err == nil {
		var out []types.ToolCall
		if json.Unmarshal(encoded, &out) == nil {
			return out
		}
	}
	return append([]types.ToolCall(nil), in...)
}

func cloneControlProviderState(in map[string]any) (map[string]any, error) {
	if len(in) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("provider state: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("provider state: %w", err)
	}
	return out, nil
}

func cloneControlMessages(in []types.ChatMessage) ([]types.ChatMessage, error) {
	if len(in) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("chat messages: %w", err)
	}
	var out []types.ChatMessage
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("chat messages: %w", err)
	}
	return out, nil
}

func cloneControlToolCalls(in []types.ToolCall) ([]types.ToolCall, error) {
	if len(in) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("tool calls: %w", err)
	}
	var out []types.ToolCall
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("tool calls: %w", err)
	}
	return out, nil
}

func applyLoopControl(
	control LoopControl,
	iteration int,
	history []types.ChatMessage,
	providerState map[string]any,
	toolCalls []types.ToolCall,
	toolResults []types.ChatMessage,
	candidate *types.ChatMessage,
) (*LoopSuspendedError, error) {
	if control == nil {
		return nil, nil
	}
	point, err := makeLoopControlPoint(iteration, history, providerState, toolCalls, toolResults, candidate)
	if err != nil {
		return nil, err
	}
	suspension := control(point)
	if suspension == nil {
		return nil, nil
	}
	copySuspension := *suspension
	copySuspension.DependencyIDs = append([]string(nil), suspension.DependencyIDs...)
	return &LoopSuspendedError{Suspension: copySuspension, Point: point}, nil
}

func makeLoopControlPoint(
	iteration int,
	history []types.ChatMessage,
	providerState map[string]any,
	toolCalls []types.ToolCall,
	toolResults []types.ChatMessage,
	candidate *types.ChatMessage,
) (LoopControlPoint, error) {
	clonedHistory, err := cloneControlMessages(history)
	if err != nil {
		return LoopControlPoint{}, &LoopCheckpointError{Cause: err}
	}
	clonedProviderState, err := cloneControlProviderState(providerState)
	if err != nil {
		return LoopControlPoint{}, &LoopCheckpointError{Cause: err}
	}
	clonedToolCalls, err := cloneControlToolCalls(toolCalls)
	if err != nil {
		return LoopControlPoint{}, &LoopCheckpointError{Cause: err}
	}
	clonedToolResults, err := cloneControlMessages(toolResults)
	if err != nil {
		return LoopControlPoint{}, &LoopCheckpointError{Cause: err}
	}
	point := LoopControlPoint{
		Iteration:     iteration,
		History:       clonedHistory,
		ProviderState: clonedProviderState,
		ToolCalls:     clonedToolCalls,
		ToolResults:   clonedToolResults,
	}
	if candidate != nil {
		cloned, cloneErr := cloneControlMessages([]types.ChatMessage{*candidate})
		if cloneErr != nil {
			return LoopControlPoint{}, &LoopCheckpointError{Cause: fmt.Errorf("assistant candidate: %w", cloneErr)}
		}
		if len(cloned) == 1 {
			point.Candidate = &cloned[0]
		}
	}
	return point, nil
}

func RunAgentLoop(ctx context.Context, p Params) ([]types.ChatMessage, error) {
	maxIters := p.MaxIterations
	if maxIters <= 0 {
		maxIters = 200
	}
	estimateTokens := p.TokenEstimator
	if estimateTokens == nil {
		estimateTokens = func(systemPrompt string, history []types.ChatMessage, tools []types.ToolDefinition) int {
			return estimateRequestTokensWithTools(systemPrompt, history, tools, charsPerTokenForModel(p.Model))
		}
	}

	history := append([]types.ChatMessage(nil), p.History...)
	providerState := cloneProviderState(p.ProviderState)
	var lastToolCallsSummary *string
	var lastToolCalls []types.ToolCall
	var lastToolResults []types.ChatMessage

	cb := p.Callbacks
	emitLocalProgress := func(progress LocalProgressEvent) {
		if cb != nil && cb.OnLocalProgress != nil {
			cb.OnLocalProgress(progress)
		}
	}
	compactHistory := func(iter int, phase string, toolCallCount int, announce bool) {
		if p.CompactHistory == nil {
			return
		}
		tokenBudget := maxInputTokensAllowed(p.MaxInputTokens)
		tokensBefore := estimateTokens(p.SystemPrompt, history, p.Tools)
		shouldAnnounce := announce || tokensBefore > tokenBudget
		if shouldAnnounce {
			emitLocalProgress(LocalProgressEvent{
				Phase:         phase,
				Status:        LocalProgressStatusStarted,
				Iteration:     iter,
				ToolCallCount: toolCallCount,
				TokenBudget:   tokenBudget,
				TokensBefore:  tokensBefore,
			})
		}

		compacted, didCompact := p.CompactHistory(history, tokenBudget, p.SystemPrompt, p.Tools)
		appliedCompaction := didCompact && compacted != nil
		if appliedCompaction {
			history = compacted
		}

		tokensAfter := estimateTokens(p.SystemPrompt, history, p.Tools)
		if appliedCompaction && !shouldAnnounce {
			emitLocalProgress(LocalProgressEvent{
				Phase:         phase,
				Status:        LocalProgressStatusStarted,
				Iteration:     iter,
				ToolCallCount: toolCallCount,
				TokenBudget:   tokenBudget,
				TokensBefore:  tokensBefore,
			})
		}
		if shouldAnnounce || appliedCompaction {
			emitLocalProgress(LocalProgressEvent{
				Phase:         phase,
				Status:        LocalProgressStatusCompleted,
				Iteration:     iter,
				ToolCallCount: toolCallCount,
				TokenBudget:   tokenBudget,
				TokensBefore:  tokensBefore,
				TokensAfter:   tokensAfter,
				Compacted:     appliedCompaction,
			})
		}
	}
	emitSyntheticAssistantError := func(iter int, msg string, meta *types.ResponseMeta, emitStart bool) error {
		assistantMsg := types.ChatMessage{Role: types.RoleAssistant, Content: ptr(msg)}
		suspended, checkpointErr := applyLoopControl(
			p.BeforeAssistantFinal,
			iter,
			history,
			providerState,
			nil,
			nil,
			&assistantMsg,
		)
		if checkpointErr != nil {
			return checkpointErr
		}
		if suspended != nil {
			// Provider failures are candidate terminal messages too. Never
			// expose/persist them (or fire OnError) while a durable external
			// dependency still requires continuation.
			return suspended
		}
		if emitStart && cb != nil && cb.OnAssistantStart != nil {
			cb.OnAssistantStart()
		}
		history = append(history, assistantMsg)
		if cb != nil && cb.OnAssistantFinal != nil {
			cb.OnAssistantFinal(assistantMsg, meta)
		}
		if cb != nil && cb.OnError != nil {
			cb.OnError(msg)
		}
		return nil
	}

	for iter := 0; iter < maxIters; iter++ {
		if ctx.Err() != nil {
			return history, ctx.Err()
		}
		compactHistory(iter, LocalPhaseContextCompacting, 0, false)
		if estimateTokens(p.SystemPrompt, history, p.Tools) > maxInputTokensAllowed(p.MaxInputTokens) {
			return history, ErrPromptContextTooLarge
		}

		provider, err := p.ProviderFactory()
		if err != nil {
			msg := "Error: " + err.Error()
			return history, emitSyntheticAssistantError(iter, msg, nil, true)
		}

		req := types.ChatRequest{
			Model:         p.Model,
			SystemPrompt:  p.SystemPrompt,
			Messages:      history,
			Tools:         p.Tools,
			ProviderState: cloneProviderState(providerState),
			RuntimePlan:   p.RuntimePlan,
		}

		if cb != nil && cb.OnAssistantStart != nil {
			cb.OnAssistantStart()
		}

		dispatchOrdinal := iter
		if p.DispatchOrdinal != nil {
			dispatchOrdinal = *p.DispatchOrdinal
			*p.DispatchOrdinal = *p.DispatchOrdinal + 1
		}
		dispatchCtx := ctx
		if attr, ok := usage.AttributionFromContext(ctx); ok && attr.Operation == usage.OperationChat && dispatchOrdinal > 0 {
			attr.Operation = usage.OperationToolFollowup
			dispatchCtx = usage.WithAttribution(ctx, attr)
		}
		streamCtx := dispatchCtx
		streamCancel := func() {}
		var streamIdleTimedOut atomic.Bool
		var streamIdleTimer *time.Timer
		if providerStreamIdleTimeout > 0 {
			var cancel context.CancelCauseFunc
			streamCtx, cancel = context.WithCancelCause(dispatchCtx)
			streamCancel = func() { cancel(context.Canceled) }
			streamIdleTimer = time.AfterFunc(providerStreamIdleTimeout, func() {
				streamIdleTimedOut.Store(true)
				cancel(usage.ErrInvocationWatchdogTimeout)
			})
		}

		stopStreamWatchdog := func() {
			if streamIdleTimer != nil {
				streamIdleTimer.Stop()
			}
			streamCancel()
		}
		resetStreamWatchdog := func() {
			if streamIdleTimer != nil {
				streamIdleTimer.Reset(providerStreamIdleTimeout)
			}
		}

		streamCh, err := provider.StreamChat(streamCtx, req)
		if err != nil {
			stopStreamWatchdog()
			if isProviderContextOverflowError("", err.Error()) {
				return history, ErrPromptContextTooLarge
			}
			msg := "Error: " + err.Error()
			return history, emitSyntheticAssistantError(iter, msg, nil, false)
		}

		accumText := ""
		pendingTextDeltas := make([]string, 0, 8)
		flushPendingTextDeltas := func() {
			if cb != nil && cb.OnTextDelta != nil {
				for _, delta := range pendingTextDeltas {
					cb.OnTextDelta(delta)
				}
			}
			pendingTextDeltas = pendingTextDeltas[:0]
		}
		var accumToolCalls []types.ToolCall
		var terminal *types.StreamEvent
		var lastMeta *types.ResponseMeta

		for ev := range streamCh {
			resetStreamWatchdog()
			if ctx.Err() != nil {
				// Preserve partial assistant output already streamed.
				if accumText != "" {
					assistantMsg := types.ChatMessage{Role: types.RoleAssistant, Content: ptr(accumText)}
					history = append(history, assistantMsg)
					if cb != nil && cb.OnAssistantFinal != nil {
						cb.OnAssistantFinal(assistantMsg, lastMeta)
					}
				}
				stopStreamWatchdog()
				return history, ctx.Err()
			}

			switch ev.Event {
			case types.EventThinkingStart:
				if cb != nil && cb.OnThinkingStart != nil {
					if d, ok := ev.Data.(types.ThinkingStartData); ok {
						cb.OnThinkingStart(d.TraceID)
					}
				}
			case types.EventThinkingDelta:
				if cb != nil && cb.OnThinkingDelta != nil {
					if d, ok := ev.Data.(types.ThinkingDeltaData); ok {
						cb.OnThinkingDelta(d.Content)
					}
				}
			case types.EventThinkingEnd:
				if cb != nil && cb.OnThinkingEnd != nil {
					if d, ok := ev.Data.(types.ThinkingEndData); ok {
						cb.OnThinkingEnd(d.Summary)
					} else {
						cb.OnThinkingEnd(nil)
					}
				}
			case types.EventCompactionStart:
				if cb != nil && cb.OnCompactionStart != nil {
					cb.OnCompactionStart()
				}
			case types.EventCompactionDelta:
				if cb != nil && cb.OnCompactionDelta != nil {
					if d, ok := parseCompactionDeltaData(ev.Data); ok {
						cb.OnCompactionDelta(d.Content)
					}
				}
			case types.EventCompactionEnd:
				if cb != nil && cb.OnCompactionEnd != nil {
					cb.OnCompactionEnd()
				}
			case types.EventTextDelta:
				if d, ok := ev.Data.(types.TextDeltaData); ok {
					accumText += d.Content
					if p.BeforeAssistantFinal != nil {
						pendingTextDeltas = append(pendingTextDeltas, d.Content)
					} else if cb != nil && cb.OnTextDelta != nil {
						cb.OnTextDelta(d.Content)
					}
				}
			case types.EventProviderToolProgress:
				if d, ok := ev.Data.(types.ProviderToolProgressData); ok {
					if cb != nil && cb.OnProviderToolProgress != nil {
						cb.OnProviderToolProgress(d)
					}
				}
			case types.EventPlanUpdate:
				if d, ok := ev.Data.(types.PlanUpdateData); ok && cb != nil && cb.OnPlanUpdate != nil {
					cb.OnPlanUpdate(d)
				}
			case types.EventToolCalls:
				if d, ok := ev.Data.(types.ToolCallsData); ok {
					accumToolCalls = d.ToolCalls
					lastToolCalls = cloneToolCalls(d.ToolCalls)
					s := summarizeToolCalls(accumToolCalls)
					if strings.TrimSpace(s) != "" {
						lastToolCallsSummary = ptr(s)
					}
					meta := d.Meta
					lastMeta = &meta
					providerState = cloneProviderState(meta.ProviderState)
				}
				terminal = &ev
				goto TERMINAL
			case types.EventFinal:
				if d, ok := ev.Data.(types.FinalData); ok {
					meta := d.Meta
					lastMeta = &meta
					providerState = cloneProviderState(meta.ProviderState)
				}
				terminal = &ev
				goto TERMINAL
			case types.EventError:
				if d, ok := ev.Data.(types.ErrorData); ok {
					if d.Meta != nil {
						lastMeta = d.Meta
					}
				}
				terminal = &ev
				goto TERMINAL
			}
		}

	TERMINAL:
		stopStreamWatchdog()
		if terminal == nil {
			msg := "Error: Provider stream ended unexpectedly."
			if streamIdleTimedOut.Load() {
				msg = "Error: " + providerStreamTimeoutMessage(providerStreamIdleTimeout)
			}
			return history, emitSyntheticAssistantError(iter, msg, lastMeta, false)
		}

		switch terminal.Event {
		case types.EventError:
			d, _ := terminal.Data.(types.ErrorData)
			if isProviderContextOverflowError(d.Code, d.Message) {
				return history, ErrPromptContextTooLarge
			}
			msg := "Error: " + d.Message
			if streamIdleTimedOut.Load() {
				msg = "Error: " + providerStreamTimeoutMessage(providerStreamIdleTimeout)
			}
			return history, emitSyntheticAssistantError(iter, msg, lastMeta, false)
		case types.EventFinal:
			d, _ := terminal.Data.(types.FinalData)
			answer := d.Answer
			if answer == "" {
				answer = accumText
			}
			assistantMsg := types.ChatMessage{
				Role:            types.RoleAssistant,
				ProviderPayload: cloneRawJSON(d.ProviderPayload),
			}
			if strings.TrimSpace(answer) != "" {
				assistantMsg.Content = ptr(answer)
			}
			if len(d.ToolCalls) > 0 {
				assistantMsg.ToolCalls = d.ToolCalls
				lastToolCalls = cloneToolCalls(d.ToolCalls)
				if s := summarizeToolCalls(d.ToolCalls); strings.TrimSpace(s) != "" {
					lastToolCallsSummary = ptr(s)
				}
			}
			suspended, checkpointErr := applyLoopControl(
				p.BeforeAssistantFinal,
				iter,
				history,
				providerState,
				d.ToolCalls,
				d.ToolResults,
				&assistantMsg,
			)
			if checkpointErr != nil {
				return history, checkpointErr
			}
			if suspended != nil {
				// Completion-barrier text must never reach the user before the
				// required external work is validated. The exact text remains in
				// the returned control point for audit/recovery, but is discarded
				// from this streamed turn while suspended.
				pendingTextDeltas = pendingTextDeltas[:0]
				return history, suspended
			}
			flushPendingTextDeltas()
			if strings.TrimSpace(answer) == "" && len(d.ToolCalls) == 0 && len(d.ToolResults) == 0 {
				// Some turns (for example lightweight status polling loops) can end with no assistant text.
				// Do not synthesize placeholder content.
				return history, nil
			}
			history = append(history, assistantMsg)
			if cb != nil && cb.OnAssistantFinal != nil {
				cb.OnAssistantFinal(assistantMsg, lastMeta)
			}
			for _, toolMsg := range d.ToolResults {
				if toolMsg.Role != types.RoleTool {
					toolMsg.Role = types.RoleTool
				}
				history = append(history, toolMsg)
				if cb != nil && cb.OnToolResult != nil {
					cb.OnToolResult(toolMsg)
				}
			}
			return history, nil
		case types.EventToolCalls:
			d, _ := terminal.Data.(types.ToolCallsData)
			// A tool-call turn is not an assistant completion attempt. Its
			// commentary may be shown once the provider has committed to the
			// tool batch; the post-batch barrier runs after all results persist.
			flushPendingTextDeltas()
			// Append assistant message with tool calls.
			assistantMsg := types.ChatMessage{
				Role:            types.RoleAssistant,
				ProviderPayload: cloneRawJSON(d.ProviderPayload),
			}
			if accumText != "" {
				assistantMsg.Content = ptr(accumText)
			}
			assistantMsg.ToolCalls = accumToolCalls
			history = append(history, assistantMsg)
			if cb != nil && cb.OnAssistantFinal != nil {
				cb.OnAssistantFinal(assistantMsg, lastMeta)
			}

			batchResults := make([]types.ChatMessage, 0, len(accumToolCalls))
			for _, call := range accumToolCalls {
				if ctx.Err() != nil {
					return history, ctx.Err()
				}
				toolMsg, err := p.ExecuteTool(call)
				if err != nil {
					toolMsg = types.ChatMessage{Role: types.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: ptr("{\"error\":\"failed to execute tool\"}")}
				}
				history = append(history, toolMsg)
				batchResults = append(batchResults, toolMsg)
				if cb != nil && cb.OnToolResult != nil {
					cb.OnToolResult(toolMsg)
				}
			}
			lastToolResults = append([]types.ChatMessage(nil), batchResults...)
			if len(accumToolCalls) > 0 {
				compactHistory(iter, LocalPhaseToolResultCompacting, len(accumToolCalls), true)
			}
			suspended, checkpointErr := applyLoopControl(
				p.AfterToolBatch,
				iter,
				history,
				providerState,
				accumToolCalls,
				batchResults,
				nil,
			)
			if checkpointErr != nil {
				return history, checkpointErr
			}
			if suspended != nil {
				return history, suspended
			}
		}
	}

	lastCalls := ""
	if lastToolCallsSummary != nil {
		lastCalls = *lastToolCallsSummary
	}
	point, checkpointErr := makeLoopControlPoint(
		maxIters,
		history,
		providerState,
		lastToolCalls,
		lastToolResults,
		nil,
	)
	if checkpointErr != nil {
		return history, checkpointErr
	}
	return history, &MaxIterationsError{
		Limit:               maxIters,
		LastToolCalls:       cloneToolCalls(lastToolCalls),
		LastToolCallSummary: lastCalls,
		Point:               point,
	}
}

func maxInputTokensAllowed(v int) int {
	if v <= 0 {
		return 200000
	}
	return v
}

func estimateRequestTokensWithTools(systemPrompt string, history []types.ChatMessage, toolDefs []types.ToolDefinition, charsPerToken int) int {
	total := estimateStringTokenCount(systemPrompt, charsPerToken)
	for _, msg := range history {
		total += estimateMessageTokenCost(msg, charsPerToken)
	}
	total += estimateToolOverheadTokens(toolDefs, charsPerToken)
	return max(1, total)
}

func estimateToolOverheadTokens(toolDefs []types.ToolDefinition, charsPerToken int) int {
	if len(toolDefs) == 0 {
		return 0
	}
	const perToolBaseOverheadChars = 64
	totalChars := 0
	for _, def := range toolDefs {
		totalChars += perToolBaseOverheadChars
		totalChars += len(strings.TrimSpace(def.Type))
		tf := def.Function
		totalChars += len(strings.TrimSpace(tf.Name))
		totalChars += len(strings.TrimSpace(tf.Description))
		if params, err := json.Marshal(tf.Parameters); err == nil {
			totalChars += len(params)
		}
	}
	return max(1, (totalChars+(charsPerToken-1))/max(1, charsPerToken))
}

func estimateMessageTokenCost(msg types.ChatMessage, charsPerToken int) int {
	tokens := 0
	if msg.Content != nil {
		tokens += estimateStringTokenCount(*msg.Content, charsPerToken)
	}
	for _, attachment := range msg.ImageAttachments {
		if strings.TrimSpace(attachment.DataURI) != "" {
			tokens += nativeImageEstimateTokens
		}
	}
	for _, attachment := range msg.FileAttachments {
		if strings.TrimSpace(attachment.InlineDataBase64) != "" {
			tokens += nativeFileEstimateTokens
		}
	}
	if strings.TrimSpace(msg.ToolCallID) != "" {
		tokens += estimateStringTokenCount(msg.ToolCallID, charsPerToken)
	}
	if strings.TrimSpace(msg.Name) != "" {
		tokens += estimateStringTokenCount(msg.Name, charsPerToken)
	}
	if len(msg.ToolCalls) > 0 {
		if encoded, err := json.Marshal(msg.ToolCalls); err == nil {
			tokens += estimateStringTokenCount(string(encoded), charsPerToken)
			return tokens
		}
	}
	return tokens
}

func estimateRequestTokens(history []types.ChatMessage, systemPrompt string) int {
	total := estimateStringTokenCount(systemPrompt, 4)
	for _, msg := range history {
		if msg.Content != nil {
			total += estimateStringTokenCount(*msg.Content, 4)
		}
	}
	return total
}

func estimateStringTokenCount(s string, charsPerToken int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	return ((len(s) + max(0, charsPerToken-1)) / max(1, charsPerToken))
}

func charsPerTokenForModel(model string) int {
	m := strings.ToLower(strings.TrimSpace(model))
	if strings.Contains(m, "claude-") {
		return 3
	}
	return 4
}

func isProviderContextOverflowError(code string, message string) bool {
	if strings.EqualFold(strings.TrimSpace(code), "CONTEXT_OVERFLOW") {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	for _, needle := range []string{
		"context length exceeded",
		"context_length_exceeded",
		"context window",
		"maximum context length",
		"input is too long",
		"too many tokens",
		"provider request body exceeds byte limit",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func ptr(s string) *string { return &s }
