package optionc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func TestManagerFutureRunsUseCheckpointPlusDeltaAndKeepRawMessagesVisible(t *testing.T) {
	var (
		mu               sync.Mutex
		requests         [][]any
		summarizerCalls  int
		summarizerInputs []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			http.NotFound(w, r)
			return
		}

		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		rawMessages, _ := body["messages"].([]any)
		mu.Lock()
		requests = append(requests, append([]any(nil), rawMessages...))
		mu.Unlock()

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_checkpoint\",\"usage\":{\"input_tokens\":1,\"output_tokens\":0,\"total_tokens\":1}}}\n\n"))
		_, _ = w.Write([]byte("event: content_block_delta\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n"))
		_, _ = w.Write([]byte("event: message_stop\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_stop\"}\n\n"))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	}))
	defer srv.Close()

	cfgDir := t.TempDir()
	manager, st := newAnthropicStartRunManagerForSSE(t, cfgDir, srv.URL, 70000)
	manager.contextPlanner = NewContextPlanner(func(ctx context.Context, text string, instruction string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		summarizerCalls++
		summarizerInputs = append(summarizerInputs, text)
		return "ACTIVE-CONTEXT-CHECKPOINT-SUMMARY-1", nil
	})

	ctx := context.Background()
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	const rawMarker = "RAW_TRANSCRIPT_MARKER_SHOULD_NOT_BE_REPLAYED"
	largeRawMessage := rawMarker + "\n" + strings.Repeat("old transcript content ", 1600)
	for i := 0; i < 8; i++ {
		role := types.RoleUser
		if i%2 == 1 {
			role = types.RoleAssistant
		}
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			Role:    role,
			Content: &largeRawMessage,
		}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	first, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "first request that should create the checkpoint",
	})
	if err != nil {
		t.Fatalf("StartRun first: %v", err)
	}
	firstRun, err := waitForRunTerminalState(ctx, st, first.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState first: %v", err)
	}
	if firstRun.Status != "completed" {
		t.Fatalf("first run status=%q, want completed", firstRun.Status)
	}
	mu.Lock()
	summarizerCallsAfterFirstRun := summarizerCalls
	mu.Unlock()
	if summarizerCallsAfterFirstRun == 0 {
		t.Fatalf("first run did not summarize raw historical transcript")
	}

	second, err := manager.StartRun(ctx, StartRunRequest{
		ThreadID: thread.ID,
		Content:  "delta follow-up after checkpoint",
	})
	if err != nil {
		t.Fatalf("StartRun second: %v", err)
	}
	secondRun, err := waitForRunTerminalState(ctx, st, second.Run.ID)
	if err != nil {
		t.Fatalf("waitForRunTerminalState second: %v", err)
	}
	if secondRun.Status != "completed" {
		t.Fatalf("second run status=%q, want completed", secondRun.Status)
	}
	if secondRun.ErrorCode != nil && strings.EqualFold(strings.TrimSpace(*secondRun.ErrorCode), "CONTEXT_OVERFLOW") {
		t.Fatalf("second run surfaced CONTEXT_OVERFLOW: %#v", secondRun.ErrorCode)
	}

	mu.Lock()
	callCount := summarizerCalls
	inputs := append([]string(nil), summarizerInputs...)
	capturedRequests := append([][]any(nil), requests...)
	mu.Unlock()

	if callCount != summarizerCallsAfterFirstRun {
		t.Fatalf("summarizer calls changed from %d to %d; future runs should reuse the active checkpoint plus delta messages instead of compacting raw history again", summarizerCallsAfterFirstRun, callCount)
	}
	if len(inputs) == 0 || !strings.Contains(strings.Join(inputs, "\n"), rawMarker) {
		t.Fatalf("initial compaction input did not include raw historical transcript marker")
	}
	if len(capturedRequests) < 2 {
		t.Fatalf("provider requests=%d, want at least 2", len(capturedRequests))
	}
	secondRequest := capturedRequests[len(capturedRequests)-1]
	if !requestPayloadContainsMarker(secondRequest, "ACTIVE-CONTEXT-CHECKPOINT-SUMMARY-1") {
		t.Fatalf("second request missing active checkpoint summary")
	}
	if !requestPayloadContainsMarker(secondRequest, "delta follow-up after checkpoint") {
		t.Fatalf("second request missing post-checkpoint delta user message")
	}
	if requestPayloadContainsMarker(secondRequest, rawMarker) {
		t.Fatalf("second request replayed raw pre-checkpoint transcript marker instead of using checkpoint plus deltas")
	}

	msgs, err := st.ListMessages(ctx, store.ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    50,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	foundRaw := false
	for _, msg := range msgs {
		if msg.UIHidden {
			continue
		}
		if msg.Content != nil && strings.Contains(*msg.Content, rawMarker) {
			foundRaw = true
			break
		}
	}
	if !foundRaw {
		t.Fatalf("raw pre-checkpoint transcript is not visible in thread history")
	}
}
