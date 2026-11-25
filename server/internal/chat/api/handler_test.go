package api_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/api"
	"ai-analyst-local/internal/chat/model"
	"ai-analyst-local/internal/chat/runs"
)

type memStore struct {
	mu sync.Mutex

	threads map[string]model.Thread
	// threadID -> messages ordered by CreatedAt then insertion order
	messages map[string][]model.Message

	runs         map[string]model.Run
	runsByThread map[string][]string

	events map[string][]model.RunEvent
}

func newMemStore() *memStore {
	return &memStore{
		threads:      make(map[string]model.Thread),
		messages:     make(map[string][]model.Message),
		runs:         make(map[string]model.Run),
		runsByThread: make(map[string][]string),
		events:       make(map[string][]model.RunEvent),
	}
}

func (m *memStore) CreateThread(ctx context.Context, p model.CreateThreadParams) (model.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(p.ID) == "" {
		return model.Thread{}, model.ErrInvalid
	}
	if _, ok := m.threads[p.ID]; ok {
		return model.Thread{}, model.ErrConflict
	}
	t := model.Thread{ID: p.ID, Object: "chat.thread", CreatedAt: time.Now().UTC(), Metadata: p.Metadata}
	m.threads[p.ID] = t
	return t, nil
}

func (m *memStore) GetThread(ctx context.Context, threadID string) (model.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.threads[threadID]
	if !ok {
		return model.Thread{}, model.ErrNotFound
	}
	return t, nil
}

func (m *memStore) ListThreads(ctx context.Context, p model.ListThreadsParams) ([]model.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]model.Thread, 0, len(m.threads))
	for _, t := range m.threads {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[:p.Limit]
	}
	return out, nil
}

func (m *memStore) CreateMessage(ctx context.Context, p model.CreateMessageParams) (model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.ThreadID) == "" {
		return model.Message{}, model.ErrInvalid
	}
	if _, ok := m.threads[p.ThreadID]; !ok {
		return model.Message{}, model.ErrNotFound
	}
	if !p.Role.Valid() {
		return model.Message{}, model.ErrInvalid
	}
	msg := model.Message{ID: p.ID, Object: "chat.message", ThreadID: p.ThreadID, Role: p.Role, Content: p.Content, CreatedAt: time.Now().UTC(), Metadata: p.Metadata}
	m.messages[p.ThreadID] = append(m.messages[p.ThreadID], msg)
	return msg, nil
}

func (m *memStore) ListMessages(ctx context.Context, p model.ListMessagesParams) ([]model.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	msgs := m.messages[p.ThreadID]
	out := append([]model.Message(nil), msgs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[len(out)-p.Limit:]
	}
	return out, nil
}

func (m *memStore) CreateRun(ctx context.Context, p model.CreateRunParams) (model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.ThreadID) == "" {
		return model.Run{}, model.ErrInvalid
	}
	if _, ok := m.threads[p.ThreadID]; !ok {
		return model.Run{}, model.ErrNotFound
	}
	if _, ok := m.runs[p.ID]; ok {
		return model.Run{}, model.ErrConflict
	}
	r := model.Run{ID: p.ID, Object: "chat.run", ThreadID: p.ThreadID, Status: model.RunStatusQueued, CreatedAt: time.Now().UTC(), Instructions: p.Instructions, Metadata: p.Metadata}
	m.runs[p.ID] = r
	m.runsByThread[p.ThreadID] = append(m.runsByThread[p.ThreadID], p.ID)
	return r, nil
}

func (m *memStore) GetRun(ctx context.Context, runID string) (model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return model.Run{}, model.ErrNotFound
	}
	return r, nil
}

func (m *memStore) ListRuns(ctx context.Context, p model.ListRunsParams) ([]model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := m.runsByThread[p.ThreadID]
	out := make([]model.Run, 0, len(ids))
	for _, id := range ids {
		if r, ok := m.runs[id]; ok {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[:p.Limit]
	}
	return out, nil
}

func (m *memStore) UpdateRun(ctx context.Context, runID string, patch model.UpdateRunPatch) (model.Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[runID]
	if !ok {
		return model.Run{}, model.ErrNotFound
	}
	if patch.Status != nil {
		r.Status = *patch.Status
	}
	if patch.StartedAt != nil {
		r.StartedAt = patch.StartedAt
	}
	if patch.CompletedAt != nil {
		r.CompletedAt = patch.CompletedAt
	}
	if patch.Error != nil {
		r.Error = patch.Error
	}
	m.runs[runID] = r
	return r, nil
}

func (m *memStore) AppendRunEvent(ctx context.Context, ev model.RunEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events[ev.RunID] = append(m.events[ev.RunID], ev)
	return nil
}

func (m *memStore) ListRunEvents(ctx context.Context, p model.ListRunEventsParams) ([]model.RunEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ev := m.events[p.RunID]
	out := make([]model.RunEvent, 0, len(ev))
	for _, e := range ev {
		if e.Seq > p.AfterSeq {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[:p.Limit]
	}
	return out, nil
}

type funcRunner func(ctx context.Context, req runs.RunRequest, emit runs.EmitFunc) (string, error)

func (f funcRunner) Run(ctx context.Context, req runs.RunRequest, emit runs.EmitFunc) (string, error) {
	return f(ctx, req, emit)
}

func TestChatRunSSE(t *testing.T) {
	st := newMemStore()
	runner := funcRunner(func(ctx context.Context, req runs.RunRequest, emit runs.EmitFunc) (string, error) {
		_ = emit(ctx, runs.EventTextDelta, map[string]any{"delta": "hi"})
		_ = emit(ctx, runs.EventTextDelta, map[string]any{"delta": "!"})
		return "", nil
	})
	mgr := runs.NewManager(st, runner, runs.Options{BufferSize: 128})
	h := api.NewChatHandler(api.Deps{Store: st, Runs: mgr})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create thread.
	thread := struct {
		ID string `json:"id"`
	}{}
	res := mustJSON(t, http.MethodPost, srv.URL+"/api/chat/threads", map[string]any{})
	if err := json.NewDecoder(res.Body).Decode(&thread); err != nil {
		t.Fatalf("decode thread: %v", err)
	}
	_ = res.Body.Close()
	if thread.ID == "" {
		t.Fatalf("missing thread id")
	}

	// Add user message.
	msgRes := mustJSON(t, http.MethodPost, srv.URL+"/api/chat/threads/"+thread.ID+"/messages", map[string]any{"role": "user", "content": "hello"})
	_ = msgRes.Body.Close()
	if msgRes.StatusCode != http.StatusOK {
		t.Fatalf("create message status: %d", msgRes.StatusCode)
	}

	// Create run.
	run := struct {
		ID string `json:"id"`
	}{}
	runRes := mustJSON(t, http.MethodPost, srv.URL+"/api/chat/threads/"+thread.ID+"/runs", map[string]any{})
	if err := json.NewDecoder(runRes.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	_ = runRes.Body.Close()
	if run.ID == "" {
		t.Fatalf("missing run id")
	}

	// Subscribe to events.
	client := &http.Client{Timeout: 5 * time.Second}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/chat/threads/"+thread.ID+"/runs/"+run.ID+"/events?after=0", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("sse status=%d body=%s", resp.StatusCode, string(b))
	}

	seen := readSSEEventNames(t, resp.Body, 50)

	// Basic ordering constraints.
	mustContainInOrder(t, seen, []string{runs.EventRunStarted, runs.EventAssistantStarted, runs.EventTextDelta, runs.EventAssistantCompleted, runs.EventRunCompleted})
}

func mustJSON(t *testing.T, method string, url string, body any) *http.Response {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(b))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	return resp
}

func readSSEEventNames(t *testing.T, r io.Reader, maxEvents int) []string {
	t.Helper()
	br := bufio.NewReader(r)
	var (
		event string
		data  []string
		out   []string
	)
	for len(out) < maxEvents {
		line, err := br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return out
			}
			t.Fatalf("read sse: %v", err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// flush event
			if event != "" {
				out = append(out, event)
				// stop once terminal event observed
				if event == runs.EventRunCompleted || event == runs.EventRunFailed || event == runs.EventRunCancelled {
					return out
				}
			}
			event = ""
			data = data[:0]
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		if strings.HasPrefix(line, "event:") {
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			continue
		}
	}
	return out
}

func mustContainInOrder(t *testing.T, seen []string, want []string) {
	t.Helper()
	i := 0
	for _, s := range seen {
		if i < len(want) && s == want[i] {
			i++
		}
	}
	if i != len(want) {
		t.Fatalf("missing sequence; seen=%v want=%v", seen, want)
	}
}

// Keep go test output small but validate we didn't accidentally break JSON encoding.
func TestListThreadsLimitParsing(t *testing.T) {
	st := newMemStore()
	h := api.NewChatHandler(api.Deps{Store: st})
	srv := httptest.NewServer(h)
	defer srv.Close()

	// Create a couple threads.
	for i := 0; i < 3; i++ {
		res := mustJSON(t, http.MethodPost, srv.URL+"/api/chat/threads", map[string]any{})
		_ = res.Body.Close()
	}

	resp, err := http.Get(srv.URL + "/api/chat/threads?limit=" + strconv.Itoa(2))
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(b))
	}
	var out struct {
		Object string         `json:"object"`
		Data   []model.Thread `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Data) != 2 {
		t.Fatalf("expected 2 threads, got %d", len(out.Data))
	}
}
