package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/chat/usage"
)

type scriptedProvider struct {
	id       string
	model    string
	events   []types.StreamEvent
	requests *[]types.ChatRequest
}

func (p *scriptedProvider) ID() string    { return p.id }
func (p *scriptedProvider) Model() string { return p.model }

func (p *scriptedProvider) StreamChat(ctx context.Context, req types.ChatRequest) (<-chan types.StreamEvent, error) {
	if p.requests != nil {
		snapshot := req
		snapshot.Messages = append([]types.ChatMessage(nil), req.Messages...)
		snapshot.Tools = append([]types.ToolDefinition(nil), req.Tools...)
		snapshot.ProviderState = cloneProviderState(req.ProviderState)
		*p.requests = append(*p.requests, snapshot)
	}
	ch := make(chan types.StreamEvent, len(p.events))
	go func() {
		defer close(ch)
		for _, ev := range p.events {
			select {
			case ch <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

type hangingProvider struct {
	id    string
	model string
}

type operationCapturingProvider struct{ operation usage.Operation }

func (p *operationCapturingProvider) ID() string    { return "mock" }
func (p *operationCapturingProvider) Model() string { return "model" }
func (p *operationCapturingProvider) StreamChat(ctx context.Context, _ types.ChatRequest) (<-chan types.StreamEvent, error) {
	if attr, ok := usage.AttributionFromContext(ctx); ok {
		p.operation = attr.Operation
	}
	stream := make(chan types.StreamEvent, 1)
	stream <- types.StreamEvent{Event: types.EventFinal, Data: types.FinalData{Answer: "done", Meta: types.ResponseMeta{ProviderID: "mock", Model: "model"}}}
	close(stream)
	return stream, nil
}

func TestRunAgentLoopOuterRecoveryRetainsToolFollowupAttribution(t *testing.T) {
	provider := &operationCapturingProvider{}
	dispatchOrdinal := 1 // an earlier outer loop already dispatched the chat call
	ctx := usage.WithAttribution(context.Background(), usage.Attribution{Operation: usage.OperationChat})
	_, err := RunAgentLoop(ctx, Params{
		ProviderFactory: func() (types.Provider, error) { return provider, nil },
		DispatchOrdinal: &dispatchOrdinal,
		Model:           "model", History: []types.ChatMessage{{Role: types.RoleUser, Content: ptr("continue")}}, MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if provider.operation != usage.OperationToolFollowup || dispatchOrdinal != 2 {
		t.Fatalf("operation/ordinal=%q/%d", provider.operation, dispatchOrdinal)
	}
}

func (p *hangingProvider) ID() string    { return p.id }
func (p *hangingProvider) Model() string { return p.model }

func (p *hangingProvider) StreamChat(ctx context.Context, _ types.ChatRequest) (<-chan types.StreamEvent, error) {
	ch := make(chan types.StreamEvent)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

func TestRunAgentLoop_ToolThenFinal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	call := types.ToolCall{
		ID:   "call_1",
		Name: "list_files",
		Args: map[string]any{"path": "/"},
	}

	prov1 := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventTextDelta, Data: types.TextDeltaData{Content: "Working..."}},
			{Event: types.EventToolCalls, Data: types.ToolCallsData{
				ToolCalls: []types.ToolCall{call},
				Meta:      types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}
	prov2 := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventFinal, Data: types.FinalData{
				Answer: "Done",
				Meta:   types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}

	iter := 0
	factory := func() (types.Provider, error) {
		iter++
		if iter == 1 {
			return prov1, nil
		}
		return prov2, nil
	}

	execTool := func(c types.ToolCall) (types.ChatMessage, error) {
		if c.ID != call.ID || c.Name != call.Name {
			t.Fatalf("unexpected tool call: %#v", c)
		}
		return types.ChatMessage{
			Role:       types.RoleTool,
			ToolCallID: c.ID,
			Name:       c.Name,
			Content:    ptr(`{"ok":true}`),
		}, nil
	}

	history := []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hi")}}

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: factory,
		SystemPrompt:    "",
		Model:           "m",
		History:         history,
		Tools:           nil,
		ExecuteTool:     execTool,
		MaxIterations:   4,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if len(out) != 4 {
		t.Fatalf("expected 4 messages, got %d: %#v", len(out), out)
	}

	if out[1].Role != types.RoleAssistant || out[1].Content == nil || *out[1].Content != "Working..." {
		t.Fatalf("unexpected assistant msg: %#v", out[1])
	}
	if len(out[1].ToolCalls) != 1 || out[1].ToolCalls[0].ID != call.ID {
		t.Fatalf("unexpected tool calls in assistant msg: %#v", out[1].ToolCalls)
	}

	if out[2].Role != types.RoleTool || out[2].ToolCallID != call.ID || out[2].Name != call.Name {
		t.Fatalf("unexpected tool msg: %#v", out[2])
	}

	if out[3].Role != types.RoleAssistant || out[3].Content == nil || *out[3].Content != "Done" {
		t.Fatalf("unexpected final assistant msg: %#v", out[3])
	}
}

func TestRunAgentLoop_ReplaysToolReturnedArxivPDFFileAttachment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	call := types.ToolCall{
		ID:   "call_read_1",
		Name: "arxiv_read_paper",
		Args: map[string]any{
			"paper":      "2301.12345",
			"start_page": 1,
			"max_pages":  2,
		},
	}

	var requests []types.ChatRequest
	prov1 := &scriptedProvider{
		id:       "mock",
		model:    "m",
		requests: &requests,
		events: []types.StreamEvent{
			{Event: types.EventToolCalls, Data: types.ToolCallsData{
				ToolCalls: []types.ToolCall{call},
				Meta:      types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}
	prov2 := &scriptedProvider{
		id:       "mock",
		model:    "m",
		requests: &requests,
		events: []types.StreamEvent{
			{Event: types.EventFinal, Data: types.FinalData{
				Answer: "Done",
				Meta:   types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}

	iter := 0
	factory := func() (types.Provider, error) {
		iter++
		if iter == 1 {
			return prov1, nil
		}
		return prov2, nil
	}

	pdfSize := int64(1024)
	execTool := func(c types.ToolCall) (types.ChatMessage, error) {
		if c.ID != call.ID || c.Name != call.Name {
			t.Fatalf("unexpected tool call: %#v", c)
		}
		return types.ChatMessage{
			Role:       types.RoleTool,
			ToolCallID: c.ID,
			Name:       c.Name,
			Content:    ptr(`{"ok":true,"paper_pages":{"arxiv_id":"2301.12345","start_page":1,"end_page":2}}`),
			FileAttachments: []types.FileAttachment{{
				ArtifactID:       "artifact-pdf",
				FileName:         "2301.12345.pdf",
				MimeType:         "application/pdf",
				NormalizedMIME:   "application/pdf",
				InlineDataBase64: "JVBERi0xLjQ=",
				SizeBytes:        &pdfSize,
				Source:           "external_tool_file",
				Capabilities: map[string]bool{
					"model_file_input":  true,
					"native_file_input": true,
				},
			}},
		}, nil
	}

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: factory,
		SystemPrompt:    "",
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("read this paper")}},
		Tools:           nil,
		ExecuteTool:     execTool,
		MaxIterations:   4,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if len(requests) < 2 {
		t.Fatalf("provider requests=%d, want at least 2", len(requests))
	}
	if got := toolFileAttachmentCount(requests[0].Messages, call.ID); got != 0 {
		t.Fatalf("first provider request unexpectedly had %d tool file attachments", got)
	}
	secondTool := findToolMessage(requests[1].Messages, call.ID)
	if secondTool == nil {
		t.Fatalf("second provider request missing tool message: %#v", requests[1].Messages)
	}
	assertArxivPDFAttachment(t, *secondTool)

	outTool := findToolMessage(out, call.ID)
	if outTool == nil {
		t.Fatalf("output history missing tool message: %#v", out)
	}
	assertArxivPDFAttachment(t, *outTool)
}

func TestRunAgentLoop_TimesOutIdleProviderAfterTool(t *testing.T) {
	oldTimeout := providerStreamIdleTimeout
	providerStreamIdleTimeout = 20 * time.Millisecond
	t.Cleanup(func() { providerStreamIdleTimeout = oldTimeout })

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	call := types.ToolCall{
		ID:   "call_1",
		Name: "query",
		Args: map[string]any{"sql": "select 1"},
	}

	prov1 := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventToolCalls, Data: types.ToolCallsData{
				ToolCalls: []types.ToolCall{call},
				Meta:      types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}
	prov2 := &hangingProvider{id: "mock", model: "m"}

	iter := 0
	factory := func() (types.Provider, error) {
		iter++
		if iter == 1 {
			return prov1, nil
		}
		return prov2, nil
	}

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: factory,
		SystemPrompt:    "",
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("run query")}},
		ExecuteTool: func(c types.ToolCall) (types.ChatMessage, error) {
			return types.ChatMessage{
				Role:       types.RoleTool,
				ToolCallID: c.ID,
				Name:       c.Name,
				Content:    ptr(`{"ok":true}`),
			}, nil
		},
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected timeout assistant message")
	}
	last := out[len(out)-1]
	if last.Role != types.RoleAssistant || last.Content == nil {
		t.Fatalf("unexpected last message: %#v", last)
	}
	if !strings.Contains(*last.Content, "Provider stream timed out") {
		t.Fatalf("last message = %q, want provider timeout", *last.Content)
	}
}

func TestRunAgentLoop_MaxIterationsStops(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	call := types.ToolCall{
		ID:   "call_1",
		Name: "list_files",
		Args: map[string]any{"path": "/"},
	}

	prov := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventToolCalls, Data: types.ToolCallsData{
				ToolCalls: []types.ToolCall{call},
				Meta: types.ResponseMeta{
					ProviderID:    "mock",
					Model:         "m",
					ProviderState: map[string]any{"cursor": "resume-token"},
				},
			}},
		},
	}

	factory := func() (types.Provider, error) { return prov, nil }
	execTool := func(types.ToolCall) (types.ChatMessage, error) {
		return types.ChatMessage{Role: types.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: ptr(`{}`)}, nil
	}

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: factory,
		SystemPrompt:    "",
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hi")}},
		Tools:           nil,
		ExecuteTool:     execTool,
		MaxIterations:   2,
	})
	var exhausted *MaxIterationsError
	if !errors.As(err, &exhausted) {
		t.Fatalf("RunAgentLoop error=%v, want MaxIterationsError", err)
	}
	if exhausted.Limit != 2 || !strings.Contains(exhausted.LastToolCallSummary, "list_files") {
		t.Fatalf("unexpected exhaustion detail: %#v", exhausted)
	}
	if len(exhausted.LastToolCalls) != 1 || exhausted.LastToolCalls[0].Name != "list_files" {
		t.Fatalf("typed last tool calls = %#v, want list_files", exhausted.LastToolCalls)
	}
	if got := exhausted.Point.ProviderState["cursor"]; got != "resume-token" {
		t.Fatalf("checkpoint provider state cursor=%v", got)
	}
	if len(exhausted.Point.History) != len(out) || len(exhausted.Point.ToolResults) != 1 {
		t.Fatalf("checkpoint does not match exhausted boundary: %#v", exhausted.Point)
	}
	last := out[len(out)-1]
	if last.Role != types.RoleTool {
		t.Fatalf("unexpected last msg: %#v", last)
	}
}

func TestRunAgentLoop_SuspendsAtExactToolBatchBoundary(t *testing.T) {
	call := types.ToolCall{ID: "call_query", Name: "run_bigquery_query", Args: map[string]any{"sql": "select 1"}}
	prov := &scriptedProvider{
		id: "mock", model: "m",
		events: []types.StreamEvent{{Event: types.EventToolCalls, Data: types.ToolCallsData{
			ToolCalls: []types.ToolCall{call},
			Meta:      types.ResponseMeta{ProviderID: "mock", Model: "m", ProviderState: map[string]any{"cursor": "next"}},
		}}},
	}
	assistantFinals := 0
	out, err := RunAgentLoop(context.Background(), Params{
		ProviderFactory: func() (types.Provider, error) { return prov, nil },
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("run it")}},
		ExecuteTool: func(types.ToolCall) (types.ChatMessage, error) {
			return types.ChatMessage{Role: types.RoleTool, ToolCallID: call.ID, Name: call.Name, Content: ptr(`{"job_id":"job-1","status":"running"}`)}, nil
		},
		AfterToolBatch: func(point LoopControlPoint) *LoopSuspension {
			if len(point.History) != 3 || len(point.ToolCalls) != 1 || len(point.ToolResults) != 1 {
				t.Fatalf("unexpected checkpoint: %#v", point)
			}
			if got := point.ProviderState["cursor"]; got != "next" {
				t.Fatalf("provider state cursor=%v", got)
			}
			return &LoopSuspension{Reason: "query_running", DependencyIDs: []string{"job-1"}}
		},
		Callbacks: &Callbacks{OnAssistantFinal: func(types.ChatMessage, *types.ResponseMeta) {
			assistantFinals++
		}},
		MaxIterations: 4,
	})
	var suspended *LoopSuspendedError
	if !errors.As(err, &suspended) {
		t.Fatalf("RunAgentLoop error=%v, want LoopSuspendedError", err)
	}
	if len(out) != 3 || len(suspended.Point.History) != 3 {
		t.Fatalf("history len=%d checkpoint=%d", len(out), len(suspended.Point.History))
	}
	if assistantFinals != 1 {
		t.Fatalf("assistant final callbacks=%d, want persisted tool-call turn only", assistantFinals)
	}
}

func TestRunAgentLoop_CompletionBarrierPreventsPrematureFinal(t *testing.T) {
	prov := &scriptedProvider{
		id: "mock", model: "m",
		events: []types.StreamEvent{
			{Event: types.EventTextDelta, Data: types.TextDeltaData{Content: "I gave up waiting"}},
			{Event: types.EventFinal, Data: types.FinalData{
				Meta: types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}
	assistantFinals := 0
	streamedText := ""
	out, err := RunAgentLoop(context.Background(), Params{
		ProviderFactory: func() (types.Provider, error) { return prov, nil },
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("finish the analysis")}},
		BeforeAssistantFinal: func(point LoopControlPoint) *LoopSuspension {
			if point.Candidate == nil || point.Candidate.Content == nil || *point.Candidate.Content != "I gave up waiting" {
				t.Fatalf("unexpected candidate: %#v", point.Candidate)
			}
			return &LoopSuspension{Reason: "required_query_running", DependencyIDs: []string{"job-1"}}
		},
		Callbacks: &Callbacks{
			OnTextDelta: func(delta string) { streamedText += delta },
			OnAssistantFinal: func(types.ChatMessage, *types.ResponseMeta) {
				assistantFinals++
			},
		},
		MaxIterations: 2,
	})
	var suspended *LoopSuspendedError
	if !errors.As(err, &suspended) {
		t.Fatalf("RunAgentLoop error=%v, want LoopSuspendedError", err)
	}
	if len(out) != 1 || assistantFinals != 0 || streamedText != "" {
		t.Fatalf("premature final escaped barrier: history=%#v callbacks=%d streamed=%q", out, assistantFinals, streamedText)
	}
}

func TestRunAgentLoop_CompletionBarrierSuspendsAllProviderFailureFinals(t *testing.T) {
	tests := []struct {
		name    string
		factory func() (types.Provider, error)
	}{
		{
			name: "provider factory error",
			factory: func() (types.Provider, error) {
				return nil, errors.New("provider factory unavailable")
			},
		},
		{
			name: "stream startup error",
			factory: func() (types.Provider, error) {
				return startupErrorProvider{err: errors.New("provider request failed")}, nil
			},
		},
		{
			name: "provider event error",
			factory: func() (types.Provider, error) {
				return &scriptedProvider{id: "mock", model: "m", events: []types.StreamEvent{{
					Event: types.EventError,
					Data:  types.ErrorData{Code: "RATE_LIMIT", Message: "try again later", Retryable: true},
				}}}, nil
			},
		},
		{
			name: "unexpected stream eof",
			factory: func() (types.Provider, error) {
				return &scriptedProvider{id: "mock", model: "m"}, nil
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assistantFinals, errorCallbacks := 0, 0
			barrierCalls := 0
			out, err := RunAgentLoop(context.Background(), Params{
				ProviderFactory: test.factory,
				Model:           "m",
				History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("finish the required query")}},
				BeforeAssistantFinal: func(point LoopControlPoint) *LoopSuspension {
					barrierCalls++
					if point.Candidate == nil || point.Candidate.Content == nil || !strings.HasPrefix(*point.Candidate.Content, "Error:") {
						t.Fatalf("unexpected provider-failure candidate: %#v", point.Candidate)
					}
					return &LoopSuspension{Reason: "required_query_running", DependencyIDs: []string{"qj-required"}}
				},
				Callbacks: &Callbacks{
					OnAssistantFinal: func(types.ChatMessage, *types.ResponseMeta) { assistantFinals++ },
					OnError:          func(string) { errorCallbacks++ },
				},
				MaxIterations: 1,
			})
			var suspended *LoopSuspendedError
			if !errors.As(err, &suspended) {
				t.Fatalf("RunAgentLoop error=%v, want LoopSuspendedError", err)
			}
			if suspended.Suspension.Reason != "required_query_running" || len(suspended.Suspension.DependencyIDs) != 1 {
				t.Fatalf("suspension=%#v", suspended.Suspension)
			}
			if barrierCalls != 1 || assistantFinals != 0 || errorCallbacks != 0 || len(out) != 1 {
				t.Fatalf("failure escaped barrier: barrier=%d finals=%d errors=%d history=%#v", barrierCalls, assistantFinals, errorCallbacks, out)
			}
		})
	}
}

func TestRunAgentLoop_ProviderFailureStillEmitsWhenCompletionBarrierAllows(t *testing.T) {
	provider := &scriptedProvider{id: "mock", model: "m", events: []types.StreamEvent{{
		Event: types.EventError,
		Data:  types.ErrorData{Code: "UPSTREAM", Message: "temporary provider failure", Retryable: true},
	}}}
	assistantFinals, errorCallbacks, barrierCalls := 0, 0, 0
	out, err := RunAgentLoop(context.Background(), Params{
		ProviderFactory: func() (types.Provider, error) { return provider, nil },
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hello")}},
		BeforeAssistantFinal: func(point LoopControlPoint) *LoopSuspension {
			barrierCalls++
			return nil
		},
		Callbacks: &Callbacks{
			OnAssistantFinal: func(types.ChatMessage, *types.ResponseMeta) { assistantFinals++ },
			OnError:          func(string) { errorCallbacks++ },
		},
		MaxIterations: 1,
	})
	if err != nil || barrierCalls != 1 || assistantFinals != 1 || errorCallbacks != 1 || len(out) != 2 ||
		out[1].Content == nil || !strings.Contains(*out[1].Content, "temporary provider failure") {
		t.Fatalf("allowed provider failure behavior changed: err=%v barrier=%d finals=%d errors=%d history=%#v", err, barrierCalls, assistantFinals, errorCallbacks, out)
	}
}

func TestRunAgentLoop_RefusesUnserializableContinuationCheckpoint(t *testing.T) {
	prov := &scriptedProvider{
		id: "mock", model: "m",
		events: []types.StreamEvent{{Event: types.EventFinal, Data: types.FinalData{
			Answer: "candidate",
			Meta: types.ResponseMeta{
				ProviderID:    "mock",
				Model:         "m",
				ProviderState: map[string]any{"not_json": func() {}},
			},
		}}},
	}
	barrierCalled := false
	out, err := RunAgentLoop(context.Background(), Params{
		ProviderFactory: func() (types.Provider, error) { return prov, nil },
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("continue")}},
		BeforeAssistantFinal: func(LoopControlPoint) *LoopSuspension {
			barrierCalled = true
			return &LoopSuspension{Reason: "wait"}
		},
		MaxIterations: 1,
	})
	var checkpointErr *LoopCheckpointError
	if !errors.As(err, &checkpointErr) {
		t.Fatalf("RunAgentLoop error=%v, want LoopCheckpointError", err)
	}
	if barrierCalled {
		t.Fatal("barrier received an unsafe checkpoint")
	}
	if len(out) != 1 {
		t.Fatalf("candidate escaped before safe checkpoint: %#v", out)
	}
}

func TestRunAgentLoop_CompactionCallbacks(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	prov := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventCompactionStart, Data: map[string]any{}},
			{Event: types.EventCompactionDelta, Data: types.CompactionDeltaData{Content: "typed"}},
			{Event: types.EventCompactionDelta, Data: map[string]any{"content": "mapped"}},
			{Event: types.EventCompactionEnd, Data: map[string]any{}},
			{Event: types.EventFinal, Data: types.FinalData{
				Answer: "Done",
				Meta:   types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}

	started := 0
	ended := 0
	deltas := make([]string, 0, 2)

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: func() (types.Provider, error) { return prov, nil },
		SystemPrompt:    "",
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("hi")}},
		Callbacks: &Callbacks{
			OnCompactionStart: func() {
				started++
			},
			OnCompactionDelta: func(delta string) {
				deltas = append(deltas, delta)
			},
			OnCompactionEnd: func() {
				ended++
			},
		},
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if len(out) < 2 {
		t.Fatalf("expected final assistant message, got %#v", out)
	}
	if out[len(out)-1].Content == nil || *out[len(out)-1].Content != "Done" {
		t.Fatalf("unexpected final assistant message: %#v", out[len(out)-1])
	}
	if started != 1 {
		t.Fatalf("compaction start callbacks=%d, want 1", started)
	}
	if ended != 1 {
		t.Fatalf("compaction end callbacks=%d, want 1", ended)
	}
	if len(deltas) != 2 {
		t.Fatalf("compaction delta callbacks=%d, want 2", len(deltas))
	}
	if deltas[0] != "typed" || deltas[1] != "mapped" {
		t.Fatalf("unexpected compaction deltas: %#v", deltas)
	}
}

func TestRunAgentLoop_DefersDispatchCompactionUntilToolBatchComplete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	call1 := types.ToolCall{
		ID:   "call_1",
		Name: "read_file",
		Args: map[string]any{"path": "a"},
	}
	call2 := types.ToolCall{
		ID:   "call_2",
		Name: "read_file",
		Args: map[string]any{"path": "b"},
	}

	var requests []types.ChatRequest
	prov1 := &scriptedProvider{
		id:       "mock",
		model:    "m",
		requests: &requests,
		events: []types.StreamEvent{
			{Event: types.EventToolCalls, Data: types.ToolCallsData{
				ToolCalls: []types.ToolCall{call1, call2},
				Meta:      types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}
	prov2 := &scriptedProvider{
		id:       "mock",
		model:    "m",
		requests: &requests,
		events: []types.StreamEvent{
			{Event: types.EventFinal, Data: types.FinalData{
				Answer: "Done",
				Meta:   types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}

	iter := 0
	factory := func() (types.Provider, error) {
		iter++
		if iter == 1 {
			return prov1, nil
		}
		return prov2, nil
	}

	const compactedContent = `{"compacted":true}`
	observedToolCounts := []int{}
	progress := []LocalProgressEvent{}
	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: factory,
		SystemPrompt:    "",
		Model:           "m",
		History:         []types.ChatMessage{{Role: types.RoleUser, Content: ptr("use both files")}},
		ExecuteTool: func(c types.ToolCall) (types.ChatMessage, error) {
			return types.ChatMessage{
				Role:       types.RoleTool,
				ToolCallID: c.ID,
				Name:       c.Name,
				Content:    ptr(`{"payload":"large"}`),
			}, nil
		},
		CompactHistory: func(history []types.ChatMessage, _ int, _ string, _ []types.ToolDefinition) ([]types.ChatMessage, bool) {
			toolCount := 0
			allCompacted := true
			for _, msg := range history {
				if msg.Role != types.RoleTool {
					continue
				}
				toolCount++
				if msg.Content == nil || *msg.Content != compactedContent {
					allCompacted = false
				}
			}
			observedToolCounts = append(observedToolCounts, toolCount)
			if toolCount != 2 || allCompacted {
				return history, false
			}
			next := append([]types.ChatMessage(nil), history...)
			for i := range next {
				if next[i].Role == types.RoleTool {
					content := compactedContent
					next[i].Content = &content
				}
			}
			return next, true
		},
		Callbacks: &Callbacks{
			OnLocalProgress: func(ev LocalProgressEvent) {
				progress = append(progress, ev)
			},
		},
		MaxIterations: 4,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if len(out) == 0 || out[len(out)-1].Content == nil || *out[len(out)-1].Content != "Done" {
		t.Fatalf("unexpected output history: %#v", out)
	}
	for _, count := range observedToolCounts {
		if count == 1 {
			t.Fatalf("CompactHistory saw partial multi-tool batch; observations=%v", observedToolCounts)
		}
	}
	if len(requests) < 2 {
		t.Fatalf("provider requests=%d, want at least 2", len(requests))
	}
	compactedToolsInSecondRequest := 0
	for _, msg := range requests[1].Messages {
		if msg.Role == types.RoleTool && msg.Content != nil && *msg.Content == compactedContent {
			compactedToolsInSecondRequest++
		}
	}
	if compactedToolsInSecondRequest != 2 {
		t.Fatalf("second provider request compacted tool results=%d, want 2; messages=%#v", compactedToolsInSecondRequest, requests[1].Messages)
	}
	if len(progress) != 2 {
		t.Fatalf("local progress events=%#v, want start and completion", progress)
	}
	if progress[0].Phase != LocalPhaseToolResultCompacting || progress[0].Status != LocalProgressStatusStarted {
		t.Fatalf("progress[0]=%#v, want tool_result_compacting started", progress[0])
	}
	if progress[1].Phase != LocalPhaseToolResultCompacting || progress[1].Status != LocalProgressStatusCompleted || !progress[1].Compacted {
		t.Fatalf("progress[1]=%#v, want compacted tool_result_compacting completion", progress[1])
	}
}

func TestRunAgentLoop_EmitsContextCompactionProgressBeforeProviderRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	providerCalls := 0
	prov := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventFinal, Data: types.FinalData{
				Answer: "Done",
				Meta:   types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}

	initialContent := "this context is too large"
	compactedContent := "fits"
	progress := []LocalProgressEvent{}
	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: func() (types.Provider, error) {
			providerCalls++
			return prov, nil
		},
		SystemPrompt: "",
		Model:        "m",
		History: []types.ChatMessage{
			{Role: types.RoleUser, Content: &initialContent},
		},
		MaxInputTokens: 10,
		CompactHistory: func(history []types.ChatMessage, _ int, _ string, _ []types.ToolDefinition) ([]types.ChatMessage, bool) {
			next := append([]types.ChatMessage(nil), history...)
			if len(next) == 0 || next[0].Content == nil || *next[0].Content == compactedContent {
				return next, false
			}
			next[0].Content = &compactedContent
			return next, true
		},
		TokenEstimator: func(_ string, history []types.ChatMessage, _ []types.ToolDefinition) int {
			total := 0
			for _, msg := range history {
				if msg.Content != nil {
					total += len(*msg.Content)
				}
			}
			return total
		},
		Callbacks: &Callbacks{
			OnLocalProgress: func(ev LocalProgressEvent) {
				progress = append(progress, ev)
			},
		},
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls=%d, want 1", providerCalls)
	}
	if len(out) < 2 || out[0].Content == nil || *out[0].Content != compactedContent {
		t.Fatalf("expected compacted history before provider request, got %#v", out)
	}
	if len(progress) != 2 {
		t.Fatalf("local progress events=%#v, want start and completion", progress)
	}
	if progress[0].Phase != LocalPhaseContextCompacting || progress[0].Status != LocalProgressStatusStarted {
		t.Fatalf("progress[0]=%#v, want context_compacting started", progress[0])
	}
	if progress[1].Phase != LocalPhaseContextCompacting || progress[1].Status != LocalProgressStatusCompleted || !progress[1].Compacted {
		t.Fatalf("progress[1]=%#v, want compacted context_compacting completion", progress[1])
	}
	if progress[1].TokensAfter >= progress[1].TokensBefore {
		t.Fatalf("progress tokens before/after = %d/%d, want reduction", progress[1].TokensBefore, progress[1].TokensAfter)
	}
}

func TestRunAgentLoop_CompactionSameLengthReducedContentIsEffective(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	providerCalls := 0
	prov := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{Event: types.EventFinal, Data: types.FinalData{
				Answer: "Done",
				Meta:   types.ResponseMeta{ProviderID: "mock", Model: "m"},
			}},
		},
	}

	initialContent := "this-message-is-way-too-large-for-budget"
	compactedContent := "fits"

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: func() (types.Provider, error) {
			providerCalls++
			return prov, nil
		},
		SystemPrompt: "",
		Model:        "m",
		History: []types.ChatMessage{
			{Role: types.RoleUser, Content: &initialContent},
		},
		MaxInputTokens: 10,
		CompactHistory: func(history []types.ChatMessage, _ int, _ string, _ []types.ToolDefinition) ([]types.ChatMessage, bool) {
			out := append([]types.ChatMessage(nil), history...)
			if len(out) == 0 || out[0].Content == nil {
				return out, false
			}
			if *out[0].Content == compactedContent {
				return out, false
			}
			out[0].Content = &compactedContent
			return out, true
		},
		TokenEstimator: func(_ string, history []types.ChatMessage, _ []types.ToolDefinition) int {
			total := 0
			for _, msg := range history {
				if msg.Content != nil {
					total += len(*msg.Content)
				}
			}
			return total
		},
		MaxIterations: 2,
	})
	if err != nil {
		t.Fatalf("RunAgentLoop error: %v", err)
	}
	if providerCalls != 1 {
		t.Fatalf("provider calls=%d, want 1", providerCalls)
	}
	if len(out) < 2 {
		t.Fatalf("expected compacted history plus final assistant message, got %#v", out)
	}
	if out[0].Content == nil || *out[0].Content != compactedContent {
		t.Fatalf("compacted message content=%#v, want %q", out[0].Content, compactedContent)
	}
	if out[len(out)-1].Content == nil || *out[len(out)-1].Content != "Done" {
		t.Fatalf("unexpected final message: %#v", out[len(out)-1])
	}
}

func TestRunAgentLoop_OversizedSingleMessageExitsDeterministically(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	providerCalls := 0
	large := "this-message-is-too-large"

	out, err := RunAgentLoop(ctx, Params{
		ProviderFactory: func() (types.Provider, error) {
			providerCalls++
			return &scriptedProvider{id: "mock", model: "m"}, nil
		},
		SystemPrompt: "",
		Model:        "m",
		History: []types.ChatMessage{
			{Role: types.RoleUser, Content: &large},
		},
		MaxInputTokens: 8,
		CompactHistory: func(history []types.ChatMessage, _ int, _ string, _ []types.ToolDefinition) ([]types.ChatMessage, bool) {
			return history, false
		},
		TokenEstimator: func(_ string, history []types.ChatMessage, _ []types.ToolDefinition) int {
			total := 0
			for _, msg := range history {
				if msg.Content != nil {
					total += len(*msg.Content)
				}
			}
			return total
		},
		MaxIterations: 2,
	})
	if err == nil || !errors.Is(err, ErrPromptContextTooLarge) {
		t.Fatalf("RunAgentLoop err=%v, want ErrPromptContextTooLarge", err)
	}
	if providerCalls != 0 {
		t.Fatalf("provider calls=%d, want 0", providerCalls)
	}
	if len(out) != 1 || out[0].Role != types.RoleUser {
		t.Fatalf("unexpected terminal history: %#v", out)
	}
}

func TestRunAgentLoopProviderContextOverflowReturnsRecoverableError(t *testing.T) {
	prov := &scriptedProvider{
		id:    "mock",
		model: "m",
		events: []types.StreamEvent{
			{
				Event: types.EventError,
				Data: types.ErrorData{
					Code:      "CONTEXT_OVERFLOW",
					Message:   "maximum context length exceeded",
					Retryable: false,
				},
			},
		},
	}
	errorCallbacks := 0
	out, err := RunAgentLoop(context.Background(), Params{
		ProviderFactory: func() (types.Provider, error) { return prov, nil },
		Model:           "m",
		History: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("hello")},
		},
		MaxIterations:  1,
		MaxInputTokens: 1000,
		Callbacks: &Callbacks{
			OnError: func(message string) {
				errorCallbacks++
			},
		},
	})
	if !errors.Is(err, ErrPromptContextTooLarge) {
		t.Fatalf("RunAgentLoop err=%v, want ErrPromptContextTooLarge", err)
	}
	if len(out) != 1 {
		t.Fatalf("history len=%d, want original history only", len(out))
	}
	if errorCallbacks != 0 {
		t.Fatalf("OnError callbacks=%d, want 0 for recoverable context pressure", errorCallbacks)
	}
}

func TestEstimateRequestTokensWithToolsUsesNativeImageCost(t *testing.T) {
	largeDataURI := "data:image/png;base64," + strings.Repeat("A", 120000)
	content := "describe this"
	history := []types.ChatMessage{
		{
			Role:    types.RoleUser,
			Content: &content,
			ImageAttachments: []types.ImageAttachment{
				{
					FileName: "large.png",
					MimeType: "image/png",
					DataURI:  largeDataURI,
				},
			},
		},
	}

	got := estimateRequestTokensWithTools("system", history, nil, 4)
	if got > nativeImageEstimateTokens+200 {
		t.Fatalf("token estimate=%d, want native image placeholder cost rather than base64 text", got)
	}
	if got < nativeImageEstimateTokens {
		t.Fatalf("token estimate=%d, want at least native image placeholder cost", got)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	}()))
}

func findToolMessage(messages []types.ChatMessage, toolCallID string) *types.ChatMessage {
	for i := range messages {
		if messages[i].Role == types.RoleTool && messages[i].ToolCallID == toolCallID {
			return &messages[i]
		}
	}
	return nil
}

func toolFileAttachmentCount(messages []types.ChatMessage, toolCallID string) int {
	msg := findToolMessage(messages, toolCallID)
	if msg == nil {
		return 0
	}
	return len(msg.FileAttachments)
}

func assertArxivPDFAttachment(t *testing.T, msg types.ChatMessage) {
	t.Helper()
	if len(msg.FileAttachments) != 1 {
		t.Fatalf("tool file attachments=%#v, want exactly one", msg.FileAttachments)
	}
	file := msg.FileAttachments[0]
	if file.ArtifactID != "artifact-pdf" {
		t.Fatalf("ArtifactID=%q, want artifact-pdf", file.ArtifactID)
	}
	if file.FileName != "2301.12345.pdf" {
		t.Fatalf("FileName=%q, want 2301.12345.pdf", file.FileName)
	}
	if file.MimeType != "application/pdf" || file.NormalizedMIME != "application/pdf" {
		t.Fatalf("PDF MIME metadata not preserved: %#v", file)
	}
	if file.InlineDataBase64 != "JVBERi0xLjQ=" {
		t.Fatalf("InlineDataBase64=%q, want PDF payload", file.InlineDataBase64)
	}
	if file.Source != "external_tool_file" {
		t.Fatalf("Source=%q, want external_tool_file", file.Source)
	}
	if !file.Capabilities["model_file_input"] || !file.Capabilities["native_file_input"] {
		t.Fatalf("native/model file capabilities not preserved: %#v", file.Capabilities)
	}
}
