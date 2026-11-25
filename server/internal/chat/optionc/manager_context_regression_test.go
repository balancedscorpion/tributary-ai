package optionc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestToolResultPersistenceContextSurvivesRunCancellation(t *testing.T) {
	type contextKey string
	parent := context.WithValue(context.Background(), contextKey("request"), "kept")
	cancelled, cancel := context.WithCancel(parent)
	cancel()

	persistCtx, release := toolResultPersistenceContext(cancelled)
	defer release()
	if err := persistCtx.Err(); err != nil {
		t.Fatalf("persistence context inherited cancellation: %v", err)
	}
	if got := persistCtx.Value(contextKey("request")); got != "kept" {
		t.Fatalf("persistence context lost request value: got %v", got)
	}
	deadline, ok := persistCtx.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 5*time.Second {
		t.Fatalf("persistence context deadline = %v, want a live deadline within 5s", deadline)
	}
}

func TestManagerStartRunRecoversProviderContextOverflowWithoutUserFacingContextError(t *testing.T) {
	var (
		mu          sync.Mutex
		requests    int
		payloads    [][]any
		firstStatus = http.StatusRequestEntityTooLarge
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rawMessages, _ := body["messages"].([]any)

		mu.Lock()
		requests++
		current := requests
		payloads = append(payloads, append([]any(nil), rawMessages...))
		mu.Unlock()

		if current == 1 {
			w.WriteHeader(firstStatus)
			_, _ = w.Write([]byte(`{"error":{"message":"maximum context length exceeded"}}`))
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"ctx_retry\",\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "recover after provider context overflow",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}
	if run.ErrorCode != nil {
		t.Fatalf("run.ErrorCode=%#v, want nil after successful context recovery", run.ErrorCode)
	}
	if run.ContextOverflowCompacted == nil || !*run.ContextOverflowCompacted {
		t.Fatalf("context_overflow_compacted=%#v, want true", run.ContextOverflowCompacted)
	}

	ch, cancel, err := manager.Subscribe(result.Run.ID)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	for ev := range ch {
		if ev.Event == "error" {
			t.Fatalf("recovered provider context overflow emitted user-facing error: %s", string(ev.Data))
		}
	}

	mu.Lock()
	requestCount := requests
	capturedPayloads := append([][]any(nil), payloads...)
	mu.Unlock()
	if requestCount < 2 {
		t.Fatalf("provider requests=%d, want retry after context overflow", requestCount)
	}
	if len(capturedPayloads) < 2 {
		t.Fatalf("captured payloads=%d, want first attempt and retry", len(capturedPayloads))
	}
}

func TestManagerStartRunReleasesActiveRunAfterCompletedTerminal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"done\",\"choices\":[{\"delta\":{\"content\":\"done\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "complete normally",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("run.Status=%q, want completed", run.Status)
	}
	assertThreadReservationReleased(t, manager, thread.ID)

	second, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "start after completed terminal",
	})
	if err != nil {
		t.Fatalf("StartRun after terminal: %v", err)
	}
	if _, err := waitForRunTerminalState(ctx, st, second.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState second: %v", err)
	}
}

func TestManagerCancelReleasesActiveRun(t *testing.T) {
	var (
		mu       sync.Mutex
		requests int
		started  = make(chan struct{})
		once     sync.Once
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		requests++
		current := requests
		mu.Unlock()

		if current > 1 {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("data: {\"id\":\"after_cancel\",\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n"))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "cancel running request",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatalf("provider request did not start")
	}
	if err := manager.Cancel(result.Run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run.Status=%q, want cancelled", run.Status)
	}
	assertThreadReservationReleased(t, manager, thread.ID)

	second, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "start after cancel",
	})
	if err != nil {
		t.Fatalf("StartRun after cancel: %v", err)
	}
	if _, err := waitForRunTerminalState(ctx, st, second.Run.ID); err != nil {
		t.Fatalf("waitForRunTerminalState second: %v", err)
	}
}

func TestManagerCancelDuringContextPlanningCompactionReleasesActiveRun(t *testing.T) {
	t.Skip("TODO: cancellation during context-planning compaction currently reaches failed status after the running transition sees a cancelled context; unskip when that path is handled as cancelled.")

	var (
		mu            sync.Mutex
		providerCalls int
		summarizing   = make(chan struct{})
		once          sync.Once
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		providerCalls++
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"id\":\"unexpected\",\"choices\":[{\"delta\":{\"content\":\"unexpected\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	manager.contextPlanner = NewContextPlanner(func(ctx context.Context, text string, instruction string) (string, error) {
		once.Do(func() { close(summarizing) })
		<-ctx.Done()
		return "", ctx.Err()
	})
	ctx := context.Background()

	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	large := strings.Repeat("historical context ", 24000)
	for i := 0; i < 4; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			Role:    role,
			Content: &large,
		}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	result, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "cancel while compacting context",
	})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	select {
	case <-summarizing:
	case <-time.After(2 * time.Second):
		t.Fatalf("context summarizer was not reached")
	}
	if err := manager.Cancel(result.Run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "cancelled" {
		t.Fatalf("run.Status=%q, want cancelled", run.Status)
	}
	assertThreadReservationReleased(t, manager, thread.ID)

	mu.Lock()
	calls := providerCalls
	mu.Unlock()
	if calls != 0 {
		t.Fatalf("provider calls=%d, want 0 after cancellation during context compaction", calls)
	}
}

func TestManagerStartRunProviderErrorMarksRunFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `{"error":{"message":"provider exploded"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newStartRunManagerForSSE(t, cfgDir, srv.URL)
	ctx := context.Background()
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	result, err := manager.StartRun(ctx, StartRunRequest{ThreadID: thread.ID, Content: "fail provider"})
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	run, err := waitForRunTerminalState(ctx, st, result.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState: %v", err)
	}
	if run.Status != "failed" {
		t.Fatalf("run.Status=%q, want failed for provider error", run.Status)
	}
}

func TestManagerSubscribeLateAfterRestartReplaysDurableEventsTODO(t *testing.T) {
	t.Skip("TODO: Manager.Subscribe is still live/in-memory only; late replay is currently handled at the HTTP SSE boundary and should move behind Manager once replay ownership is consolidated.")
}

func assertThreadReservationReleased(t *testing.T, manager *Manager, threadID string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		activeRunID := strings.TrimSpace(manager.activeByThread[threadID])
		manager.mu.Unlock()
		if activeRunID == "" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}

	manager.mu.Lock()
	activeRunID := strings.TrimSpace(manager.activeByThread[threadID])
	manager.mu.Unlock()
	t.Fatalf("thread %s still reserved by active run %q", threadID, activeRunID)
}
