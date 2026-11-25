package engine

import (
	"context"
	"errors"
	"testing"

	"ai-analyst-local/internal/chat/types"
)

type startupErrorProvider struct {
	err error
}

func (p startupErrorProvider) ID() string    { return "startup-error" }
func (p startupErrorProvider) Model() string { return "test-model" }

func (p startupErrorProvider) StreamChat(context.Context, types.ChatRequest) (<-chan types.StreamEvent, error) {
	return nil, p.err
}

func TestRunAgentLoopProviderStartupContextOverflowReturnsRecoverableError(t *testing.T) {
	errorCallbacks := 0
	finalCallbacks := 0
	out, err := RunAgentLoop(context.Background(), Params{
		ProviderFactory: func() (types.Provider, error) {
			return startupErrorProvider{err: errors.New("provider request body exceeds byte limit")}, nil
		},
		Model: "test-model",
		History: []types.ChatMessage{
			{Role: types.RoleUser, Content: ptr("hello")},
		},
		MaxIterations:  1,
		MaxInputTokens: 1000,
		Callbacks: &Callbacks{
			OnError: func(string) {
				errorCallbacks++
			},
			OnAssistantFinal: func(types.ChatMessage, *types.ResponseMeta) {
				finalCallbacks++
			},
		},
	})
	if !errors.Is(err, ErrPromptContextTooLarge) {
		t.Fatalf("RunAgentLoop err=%v, want ErrPromptContextTooLarge", err)
	}
	if len(out) != 1 || out[0].Role != types.RoleUser {
		t.Fatalf("history=%#v, want original user history only", out)
	}
	if errorCallbacks != 0 {
		t.Fatalf("OnError callbacks=%d, want 0 for recoverable context pressure", errorCallbacks)
	}
	if finalCallbacks != 0 {
		t.Fatalf("OnAssistantFinal callbacks=%d, want 0 for recoverable context pressure", finalCallbacks)
	}
}
