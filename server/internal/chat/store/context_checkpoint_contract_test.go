package store

import (
	"context"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/types"
)

type activeContextCheckpointStore interface {
	GetActiveContextCheckpoint(ctx context.Context, threadID string) (*ContextCheckpoint, error)
	UpsertActiveContextCheckpoint(ctx context.Context, checkpoint ContextCheckpoint) (ContextCheckpoint, error)
}

var _ activeContextCheckpointStore = (*JSONStore)(nil)
var _ activeContextCheckpointStore = (*MySQLStore)(nil)
var _ RunEventStore = (*JSONStore)(nil)
var _ RunEventStore = (*MySQLStore)(nil)

func TestJSONStoreActiveContextCheckpointPersistsAcrossReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	dir := t.TempDir()
	st, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "anthropic", "claude-opus-4-6", nil, "system", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	rawContent := "raw transcript remains visible"
	raw, err := st.AppendMessage(ctx, thread.ID, &run.ID, Message{
		Role:    types.RoleUser,
		Content: &rawContent,
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	if got, err := st.GetActiveContextCheckpoint(ctx, thread.ID); err != nil || got != nil {
		t.Fatalf("empty GetActiveContextCheckpoint = (%#v, %v), want nil checkpoint and nil error", got, err)
	}

	summary := "durable active checkpoint summary"
	replacementUser := "continue from checkpoint"
	checkpoint := ContextCheckpoint{
		ID:               "checkpoint-json-1",
		ThreadID:         thread.ID,
		RunID:            &run.ID,
		SourceMessageSeq: raw.Seq,
		SourceMessageID:  raw.ID,
		ReplacementHistory: []types.ChatMessage{
			{Role: types.RoleAssistant, Content: &summary},
			{Role: types.RoleUser, Content: &replacementUser},
		},
		Summary:               summary,
		SummaryMessageIDs:     []string{raw.ID},
		ReferencedArtifactIDs: []string{"artifact-1"},
		TokenEstimate:         42,
		CharsPerToken:         3,
		Model:                 "claude-opus-4-6",
		ProviderID:            "anthropic",
		CreatedAt:             time.Now().UTC(),
	}
	persisted, err := st.UpsertActiveContextCheckpoint(ctx, checkpoint)
	if err != nil {
		t.Fatalf("UpsertActiveContextCheckpoint: %v", err)
	}
	if persisted.ID != checkpoint.ID {
		t.Fatalf("persisted checkpoint ID=%q, want %q", persisted.ID, checkpoint.ID)
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen NewJSON: %v", err)
	}
	got, err := reopened.GetActiveContextCheckpoint(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetActiveContextCheckpoint after reopen: %v", err)
	}
	if got == nil {
		t.Fatalf("GetActiveContextCheckpoint returned nil checkpoint")
	}
	if got.ID != checkpoint.ID || got.ThreadID != thread.ID || got.SourceMessageSeq != raw.Seq || got.SourceMessageID != raw.ID {
		t.Fatalf("checkpoint identity/source mismatch: %#v", got)
	}
	if got.RunID == nil || *got.RunID != run.ID {
		t.Fatalf("checkpoint RunID=%#v, want %q", got.RunID, run.ID)
	}
	if got.Summary != summary || got.TokenEstimate != 42 || got.CharsPerToken != 3 {
		t.Fatalf("checkpoint summary/token metadata mismatch: %#v", got)
	}
	if len(got.ReplacementHistory) != 2 {
		t.Fatalf("replacement history len=%d, want 2", len(got.ReplacementHistory))
	}
	if got.ReplacementHistory[0].Role != types.RoleAssistant || got.ReplacementHistory[0].Content == nil || *got.ReplacementHistory[0].Content != summary {
		t.Fatalf("replacement history[0]=%#v, want assistant summary", got.ReplacementHistory[0])
	}
	if got.ReplacementHistory[1].Role != types.RoleUser || got.ReplacementHistory[1].Content == nil || *got.ReplacementHistory[1].Content != replacementUser {
		t.Fatalf("replacement history[1]=%#v, want user delta", got.ReplacementHistory[1])
	}

	msgs, err := reopened.ListMessages(ctx, ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("ListMessages after checkpoint reopen: %v", err)
	}
	foundRaw := false
	for _, msg := range msgs {
		if msg.ID == raw.ID && msg.Content != nil && *msg.Content == rawContent && !msg.UIHidden {
			foundRaw = true
			break
		}
	}
	if !foundRaw {
		t.Fatalf("raw message was not preserved as visible history after checkpoint write")
	}
}
