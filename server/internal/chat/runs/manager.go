package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ai-analyst-local/internal/chat/model"
)

const (
	EventRunStarted         = "run_started"
	EventAssistantStarted   = "assistant_started"
	EventTextDelta          = "text_delta"
	EventToolCallStarted    = "tool_call_started"
	EventToolCallCompleted  = "tool_call_completed"
	EventAssistantCompleted = "assistant_completed"
	EventRunCompleted       = "run_completed"
	EventRunFailed          = "run_failed"
	EventRunCancelled       = "run_cancelled"
	EventError              = "error"
)

type EmitFunc func(ctx context.Context, eventType string, payload any) error

type Runner interface {
	// Run executes a run and can optionally emit streaming events (e.g. text_delta).
	// The returned string is the final assistant message content.
	//
	// If the runner emits text deltas and returns an empty string, the manager will
	// assemble the final content from the streamed deltas.
	Run(ctx context.Context, req RunRequest, emit EmitFunc) (string, error)
}

type RunRequest struct {
	Run          model.Run
	Thread       model.Thread
	Messages     []model.Message
	Instructions string
}

type EventEnvelope struct {
	RunID     string    `json:"run_id"`
	ThreadID  string    `json:"thread_id"`
	Seq       int64     `json:"seq"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"created_at"`
	Payload   any       `json:"payload,omitempty"`
}

type Options struct {
	// BufferSize controls how many recent events are kept in-memory per run
	// for catch-up subscriptions. Default: 1024.
	BufferSize int

	Clock Clock
}

type Clock interface {
	Now() time.Time
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }

type Manager struct {
	store  model.Store
	runner Runner
	clock  Clock

	mu   sync.Mutex
	runs map[string]*runState

	bufferSize int
}

type runState struct {
	mu        sync.Mutex
	nextSeq   int64
	buffer    []model.RunEvent
	subs      map[int]chan model.RunEvent
	nextSubID int
	done      bool
	doneCh    chan struct{}
	cancel    context.CancelFunc
}

func NewManager(st model.Store, runner Runner, opts Options) *Manager {
	if st == nil {
		panic("runs.NewManager: store is required")
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1024
	}
	clk := opts.Clock
	if clk == nil {
		clk = realClock{}
	}
	return &Manager{
		store:      st,
		runner:     runner,
		clock:      clk,
		runs:       make(map[string]*runState),
		bufferSize: opts.BufferSize,
	}
}

type CreateAndStartParams struct {
	ThreadID     string
	Instructions string
	Metadata     map[string]any
}

func (m *Manager) CreateAndStart(ctx context.Context, p CreateAndStartParams) (model.Run, error) {
	threadID := strings.TrimSpace(p.ThreadID)
	if threadID == "" {
		return model.Run{}, fmt.Errorf("thread_id: %w", model.ErrInvalid)
	}
	// Ensure thread exists.
	thread, err := m.store.GetThread(ctx, threadID)
	if err != nil {
		return model.Run{}, err
	}

	runID := model.NewID("run")
	now := m.clock.Now()
	run, err := m.store.CreateRun(ctx, model.CreateRunParams{
		ID:           runID,
		ThreadID:     thread.ID,
		Instructions: strings.TrimSpace(p.Instructions),
		Metadata:     p.Metadata,
	})
	if err != nil {
		return model.Run{}, err
	}
	if run.Object == "" {
		run.Object = "chat.run"
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = now
	}
	if run.Status == "" {
		run.Status = model.RunStatusQueued
	}

	st := m.ensureRunState(run.ID)

	runCtx, cancel := context.WithCancel(context.Background())
	st.mu.Lock()
	st.cancel = cancel
	st.mu.Unlock()

	go m.executeRun(runCtx, thread, run)
	return run, nil
}

func (m *Manager) Cancel(ctx context.Context, runID string) (model.Run, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return model.Run{}, fmt.Errorf("run_id: %w", model.ErrInvalid)
	}
	st := m.getRunState(runID)
	if st != nil {
		st.mu.Lock()
		cancel := st.cancel
		st.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	}
	// Best-effort state update; the running loop will also handle context cancellation.
	now := m.clock.Now()
	status := model.RunStatusCancelled
	run, err := m.store.UpdateRun(ctx, runID, model.UpdateRunPatch{Status: &status, CompletedAt: &now})
	if err != nil {
		return model.Run{}, err
	}
	_ = m.emit(ctx, runID, run.ThreadID, EventRunCancelled, map[string]any{"run": run})
	m.markDone(runID)
	return run, nil
}

func (m *Manager) Subscribe(runID string, afterSeq int64) (<-chan model.RunEvent, func(), error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return nil, nil, fmt.Errorf("run_id: %w", model.ErrInvalid)
	}
	st := m.getRunState(runID)
	if st == nil {
		return nil, nil, model.ErrNotFound
	}

	st.mu.Lock()
	backlog := make([]model.RunEvent, 0, len(st.buffer))
	for _, ev := range st.buffer {
		if ev.Seq > afterSeq {
			backlog = append(backlog, ev)
		}
	}
	done := st.done
	subID := -1
	var ch chan model.RunEvent
	if !done {
		subID = st.nextSubID
		st.nextSubID++
		if st.subs == nil {
			st.subs = make(map[int]chan model.RunEvent)
		}
		ch = make(chan model.RunEvent, len(backlog)+64)
		st.subs[subID] = ch
	} else {
		ch = make(chan model.RunEvent, len(backlog)+1)
	}
	st.mu.Unlock()

	// Deliver backlog.
	for i := range backlog {
		ch <- backlog[i]
	}
	if done {
		close(ch)
		return ch, func() {}, nil
	}

	cancel := func() {
		st.mu.Lock()
		c := st.subs[subID]
		delete(st.subs, subID)
		st.mu.Unlock()
		if c != nil {
			close(c)
		}
	}
	return ch, cancel, nil
}

func (m *Manager) getRunState(runID string) *runState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.runs[runID]
}

func (m *Manager) ensureRunState(runID string) *runState {
	m.mu.Lock()
	defer m.mu.Unlock()
	if st := m.runs[runID]; st != nil {
		return st
	}
	st := &runState{doneCh: make(chan struct{}), subs: make(map[int]chan model.RunEvent)}
	m.runs[runID] = st
	return st
}

func (m *Manager) markDone(runID string) {
	st := m.getRunState(runID)
	if st == nil {
		return
	}
	st.mu.Lock()
	if st.done {
		st.mu.Unlock()
		return
	}
	st.done = true
	for id, ch := range st.subs {
		close(ch)
		delete(st.subs, id)
	}
	close(st.doneCh)
	st.mu.Unlock()
}

func (m *Manager) nextSeq(runID string) int64 {
	st := m.ensureRunState(runID)
	st.mu.Lock()
	st.nextSeq++
	seq := st.nextSeq
	st.mu.Unlock()
	return seq
}

func (m *Manager) broadcast(runID string, ev model.RunEvent) {
	st := m.ensureRunState(runID)

	st.mu.Lock()
	if m.bufferSize > 0 {
		if len(st.buffer) < m.bufferSize {
			st.buffer = append(st.buffer, ev)
		} else if len(st.buffer) == 0 {
			st.buffer = append(st.buffer, ev)
		} else {
			copy(st.buffer, st.buffer[1:])
			st.buffer[len(st.buffer)-1] = ev
		}
	}

	for id, ch := range st.subs {
		select {
		case ch <- ev:
		default:
			// Slow consumer: detach.
			close(ch)
			delete(st.subs, id)
		}
	}
	st.mu.Unlock()
}

func (m *Manager) emit(ctx context.Context, runID string, threadID string, eventType string, payload any) error {
	seq := m.nextSeq(runID)
	now := m.clock.Now()
	env := EventEnvelope{
		RunID:     runID,
		ThreadID:  threadID,
		Seq:       seq,
		Type:      eventType,
		CreatedAt: now,
		Payload:   payload,
	}
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	ev := model.RunEvent{
		RunID:     runID,
		ThreadID:  threadID,
		Seq:       seq,
		Type:      eventType,
		CreatedAt: now,
		Data:      b,
	}
	// Persist first; still broadcast even if persistence fails.
	perr := m.store.AppendRunEvent(ctx, ev)
	m.broadcast(runID, ev)
	if perr != nil {
		return perr
	}
	return nil
}

func (m *Manager) executeRun(ctx context.Context, thread model.Thread, run model.Run) {
	// Transition to running.
	startedAt := m.clock.Now()
	st := model.RunStatusRunning
	run, err := m.store.UpdateRun(ctx, run.ID, model.UpdateRunPatch{Status: &st, StartedAt: &startedAt})
	if err != nil {
		_ = m.emit(context.Background(), run.ID, thread.ID, EventError, map[string]any{
			"code":    "STORE_ERROR",
			"message": err.Error(),
		})
		m.markDone(run.ID)
		return
	}
	_ = m.emit(ctx, run.ID, thread.ID, EventRunStarted, map[string]any{"run": run})

	assistantMsgID := model.NewID("msg")
	_ = m.emit(ctx, run.ID, thread.ID, EventAssistantStarted, map[string]any{"assistant_message_id": assistantMsgID})

	msgs, err := m.store.ListMessages(ctx, model.ListMessagesParams{ThreadID: thread.ID, Limit: 1000})
	if err != nil {
		_ = m.failRun(ctx, thread, run.ID, err)
		return
	}

	var sb strings.Builder
	emit := func(ctx context.Context, eventType string, payload any) error {
		if eventType == EventTextDelta {
			if delta, ok := extractDelta(payload); ok {
				sb.WriteString(delta)
			}
		}
		return m.emit(ctx, run.ID, thread.ID, eventType, payload)
	}

	if m.runner == nil {
		_ = emit(ctx, EventError, map[string]any{
			"code":    "NO_RUNNER",
			"message": "no runner configured",
		})
		_ = m.failRun(ctx, thread, run.ID, errors.New("no runner configured"))
		return
	}

	final, rerr := m.runner.Run(ctx, RunRequest{Run: run, Thread: thread, Messages: msgs, Instructions: run.Instructions}, emit)
	if rerr != nil {
		if errors.Is(rerr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			_ = m.cancelRun(ctx, thread, run.ID)
			return
		}
		_ = emit(ctx, EventError, map[string]any{
			"code":    "RUNNER_ERROR",
			"message": rerr.Error(),
		})
		_ = m.failRun(ctx, thread, run.ID, rerr)
		return
	}
	if strings.TrimSpace(final) == "" {
		final = sb.String()
	}

	msg, err := m.store.CreateMessage(ctx, model.CreateMessageParams{
		ID:       assistantMsgID,
		ThreadID: thread.ID,
		Role:     model.RoleAssistant,
		Content:  final,
	})
	if err != nil {
		_ = m.failRun(ctx, thread, run.ID, err)
		return
	}
	if msg.Object == "" {
		msg.Object = "chat.message"
	}

	_ = m.emit(ctx, run.ID, thread.ID, EventAssistantCompleted, map[string]any{"message": msg})

	completedAt := m.clock.Now()
	stDone := model.RunStatusCompleted
	run, err = m.store.UpdateRun(ctx, run.ID, model.UpdateRunPatch{Status: &stDone, CompletedAt: &completedAt})
	if err != nil {
		_ = m.emit(ctx, run.ID, thread.ID, EventError, map[string]any{
			"code":    "STORE_ERROR",
			"message": err.Error(),
		})
		m.markDone(run.ID)
		return
	}
	_ = m.emit(ctx, run.ID, thread.ID, EventRunCompleted, map[string]any{"run": run})
	m.markDone(run.ID)
}

func (m *Manager) cancelRun(ctx context.Context, thread model.Thread, runID string) error {
	now := m.clock.Now()
	st := model.RunStatusCancelled
	run, err := m.store.UpdateRun(ctx, runID, model.UpdateRunPatch{Status: &st, CompletedAt: &now})
	if err != nil {
		return err
	}
	_ = m.emit(ctx, runID, thread.ID, EventRunCancelled, map[string]any{"run": run})
	m.markDone(runID)
	return nil
}

func (m *Manager) failRun(ctx context.Context, thread model.Thread, runID string, cause error) error {
	now := m.clock.Now()
	st := model.RunStatusFailed
	errObj := &model.RunError{Code: "FAILED", Message: cause.Error()}
	run, err := m.store.UpdateRun(ctx, runID, model.UpdateRunPatch{Status: &st, CompletedAt: &now, Error: errObj})
	if err != nil {
		return err
	}
	_ = m.emit(ctx, runID, thread.ID, EventRunFailed, map[string]any{"run": run})
	m.markDone(runID)
	return nil
}

func extractDelta(payload any) (string, bool) {
	switch v := payload.(type) {
	case string:
		if v == "" {
			return "", false
		}
		return v, true
	case map[string]any:
		if raw, ok := v["delta"]; ok {
			if s, ok := raw.(string); ok {
				if s == "" {
					return "", false
				}
				return s, true
			}
		}
	case struct{ Delta string }:
		if v.Delta == "" {
			return "", false
		}
		return v.Delta, true
	case *struct{ Delta string }:
		if v == nil || v.Delta == "" {
			return "", false
		}
		return v.Delta, true
	default:
		// Try JSON roundtrip for common case of typed payloads.
		b, err := json.Marshal(payload)
		if err != nil {
			return "", false
		}
		var tmp struct {
			Delta string `json:"delta"`
		}
		if err := json.Unmarshal(b, &tmp); err != nil {
			return "", false
		}
		if tmp.Delta == "" {
			return "", false
		}
		return tmp.Delta, true
	}
	return "", false
}
