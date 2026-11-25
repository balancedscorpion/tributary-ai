package rlm

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	chattypes "ai-analyst-local/internal/chat/types"
)

func TestExecWorkspaceAndFinish(t *testing.T) {
	rt := NewRuntime(Options{
		MaxPromptBytes: 64,
		MaxSubCalls:    4,
	})
	code := strings.Join([]string{
		`set_prompt("hello")`,
		`append_prompt(" world")`,
		`add_evidence("doc:1", "seed")`,
		`finish("done", ["doc:1", "artifact:2"])`,
	}, "\n")
	res, err := rt.Exec(context.Background(), "run-1", ExecRequest{Code: code})
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if !res.OK || !res.Done {
		t.Fatalf("expected successful finished exec, got %+v", res)
	}
	if res.FinalAnswer == nil || *res.FinalAnswer != "done" {
		t.Fatalf("expected final answer 'done', got %+v", res.FinalAnswer)
	}
	if res.PromptLen != len("hello world") {
		t.Fatalf("unexpected prompt len: %d", res.PromptLen)
	}
	if len(res.Evidence) != 2 {
		t.Fatalf("expected 2 evidence ids, got %+v", res.Evidence)
	}
}

func TestExecStepLimitEnforced(t *testing.T) {
	rt := NewRuntime(Options{
		MaxExecutionSteps: 64,
	})
	res, err := rt.Exec(context.Background(), "run-2", ExecRequest{
		Code: "while True:\n  pass",
	})
	if err != nil {
		t.Fatalf("exec returned unexpected error: %v", err)
	}
	if res.OK {
		t.Fatalf("expected step-limited execution to fail, got %+v", res)
	}
	if strings.TrimSpace(res.Error) == "" {
		t.Fatalf("expected an execution error message")
	}
}

func TestExecRejectsUnsupportedStarlarkPatterns(t *testing.T) {
	rt := NewRuntime(Options{})
	tests := []struct {
		name       string
		code       string
		errorMatch string
	}{
		{
			name:       "open call blocked",
			code:       `open("file.txt")`,
			errorMatch: "open() is not supported in rlm_exec",
		},
		{
			name:       "read_file call blocked",
			code:       `read_file("foo")`,
			errorMatch: "read_file() is not available in rlm_exec",
		},
		{
			name:       "f-string blocked",
			code:       `x = f"hello {1}"`,
			errorMatch: "f-strings are not valid Starlark syntax",
		},
		{
			name:       "dir without args blocked",
			code:       `dir()`,
			errorMatch: "dir() requires an argument in this runtime",
		},
		{
			name:       "max_results blocked",
			code:       `search(query="x", max_results=2)`,
			errorMatch: "search() does not accept max_results",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := rt.Exec(context.Background(), "run-rlm-hardening", ExecRequest{Code: tc.code})
			if err != nil {
				t.Fatalf("Exec returned unexpected error: %v", err)
			}
			if res.OK {
				t.Fatalf("expected execution to fail for %q", tc.name)
			}
			if !strings.Contains(res.Error, tc.errorMatch) {
				t.Fatalf("error %q does not contain expected text %q", res.Error, tc.errorMatch)
			}
		})
	}
}

func TestListMessagesCursorPagination(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 1; i <= 3; i++ {
		content := fmt.Sprintf("message-%d", i)
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{Role: chattypes.RoleUser, Content: &content}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	rt := NewRuntime(Options{Store: st})
	code := fmt.Sprintf(strings.Join([]string{
		`first = list_messages(thread_id=%[1]q, limit=2, with_page_info=True)`,
		`second = list_messages(thread_id=%[1]q, page_token=first["next_page_token"])`,
		`print(first["messages"][0]["seq"], first["messages"][1]["seq"], second["messages"][0]["seq"], second["has_more"])`,
	}, "\n"), thread.ID)
	res, err := rt.Exec(ctx, "run-list-messages-cursor", ExecRequest{Code: code, EvidenceBudget: 2})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !res.OK {
		t.Fatalf("cursor exec failed: %+v", res)
	}
	if !strings.Contains(res.StdoutPrefix, "3 2 1 False") {
		t.Fatalf("cursor stdout=%q, want descending pages without gaps", res.StdoutPrefix)
	}
	if res.EvidenceUsed != 2 {
		t.Fatalf("EvidenceUsed=%d, want 2 successful reads", res.EvidenceUsed)
	}
}

func TestInvalidListMessagesWindowDoesNotConsumeEvidenceBudget(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	rt := NewRuntime(Options{Store: st})
	res, err := rt.Exec(ctx, "run-invalid-list-messages-window", ExecRequest{
		Code:           fmt.Sprintf(`list_messages(thread_id=%q, before_seq=1, after_seq=2)`, thread.ID),
		EvidenceBudget: 1,
	})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.OK || !strings.Contains(res.Error, "before_seq must be greater than after_seq") {
		t.Fatalf("invalid window result=%+v", res)
	}
	if res.EvidenceUsed != 0 || res.EvidenceBudgetExhausted {
		t.Fatalf("invalid call consumed evidence budget: %+v", res)
	}
}
