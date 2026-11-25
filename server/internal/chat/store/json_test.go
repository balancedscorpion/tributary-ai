package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/types"
)

func ptrToString(s string) *string {
	return &s
}

func TestJSONStoreUpdateThreadProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if err := st.UpdateThreadProject(ctx, thread.ID, "proj_123"); err != nil {
		t.Fatalf("UpdateThreadProject: %v", err)
	}
	updated, err := st.GetThread(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetThread: %v", err)
	}
	if updated.ProjectID == nil || *updated.ProjectID != "proj_123" {
		t.Fatalf("ProjectID=%v, want proj_123", updated.ProjectID)
	}
}

func TestJSONStoreAppendMessageConcurrentSeq(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			content := "message"
			_, err := st.AppendMessage(ctx, thread.ID, nil, Message{
				Role:    types.RoleUser,
				Content: &content,
			})
			if err != nil {
				t.Errorf("AppendMessage(%d): %v", idx, err)
			}
		}(i)
	}
	wg.Wait()

	msgs, err := st.ListMessages(ctx, ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    n + 10,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != n {
		t.Fatalf("expected %d messages, got %d", n, len(msgs))
	}
	for i := 0; i < n; i++ {
		want := int64(i + 1)
		if msgs[i].Seq != want {
			t.Fatalf("seq[%d] = %d, want %d", i, msgs[i].Seq, want)
		}
	}
}

func TestJSONStoreDeleteThreadCascade(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	content := "hello"
	msg, err := st.AppendMessage(ctx, thread.ID, &run.ID, Message{
		Role:    types.RoleUser,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	_, err = st.InsertArtifact(ctx, Artifact{
		ThreadID:    thread.ID,
		MessageID:   &msg.ID,
		Type:        "code",
		Title:       "artifact",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	if err := st.DeleteThread(ctx, thread.ID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	if _, err := st.GetThread(ctx, thread.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetThread err = %v, want ErrNotFound", err)
	}
	if _, err := st.GetRun(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRun err = %v, want ErrNotFound", err)
	}
	if _, err := st.ListMessages(ctx, ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    10,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListMessages err = %v, want ErrNotFound", err)
	}
}

func TestJSONStoreApprovalRequestsSurviveReopen(t *testing.T) {
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
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	created, err := st.CreateApprovalRequest(ctx, ApprovalRequest{
		ThreadID: thread.ID,
		RunID:    run.ID,
		Tool:     "rlm_exec",
		Category: "execute",
		Subject:  PermissionSubject{Kind: "tool_set", ToolSetID: "thread-search", Connector: "thread-search"},
		Summary:  "Allow execution",
	})
	if err != nil {
		t.Fatalf("CreateApprovalRequest: %v", err)
	}
	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen NewJSON: %v", err)
	}
	got, err := reopened.GetApprovalRequest(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetApprovalRequest: %v", err)
	}
	if got.ID != created.ID || got.Status != "pending" || got.ThreadID != thread.ID {
		t.Fatalf("approval mismatch got=%#v created=%#v", got, created)
	}
	if got.Subject.Kind != "tool_set" || got.Subject.ToolSetID != "thread-search" || got.Subject.Connector != "thread-search" {
		t.Fatalf("approval subject = %#v, want persisted tool_set subject", got.Subject)
	}
	decided, err := reopened.DecideApprovalRequest(ctx, created.ID, ApprovalDecision{Action: "reject", Scope: "once", DecidedAt: time.Now().UTC()}, "rejected")
	if err != nil {
		t.Fatalf("DecideApprovalRequest: %v", err)
	}
	if decided.Status != "rejected" {
		t.Fatalf("decided.Status = %q, want rejected", decided.Status)
	}
}

func TestJSONStoreLocalSearchJobsSurviveReopen(t *testing.T) {
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
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	created, err := st.UpsertLocalSearchJob(ctx, LocalSearchJob{
		ID:         "local_search_job_test",
		ThreadID:   thread.ID,
		RunID:      run.ID,
		ToolCallID: "call_1",
		Status:     "running",
		Query:      "find the forecast workbook",
		GrantID:    "rule_local",
		LocalRoot:  "/Users/test/Documents",
		LocalKind:  "folder",
		Result: map[string]any{
			"files_count": 0,
		},
	})
	if err != nil {
		t.Fatalf("UpsertLocalSearchJob running: %v", err)
	}
	completedAt := time.Now().UTC()
	created.Status = "completed"
	created.UpdatedAt = completedAt
	created.CompletedAt = &completedAt
	created.FilesCount = 2
	created.Result = map[string]any{
		"agent_summary": "ranked two files",
		"files_count":   2,
		"files": []any{
			map[string]any{"path": "forecast.xlsx"},
			map[string]any{"path": "forecast-notes.md"},
		},
	}
	if _, err := st.UpsertLocalSearchJob(ctx, created); err != nil {
		t.Fatalf("UpsertLocalSearchJob completed: %v", err)
	}
	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("reopen NewJSON: %v", err)
	}
	got, err := reopened.GetLocalSearchJob(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetLocalSearchJob after reopen: %v", err)
	}
	if got.ID != created.ID || got.ThreadID != thread.ID || got.RunID != run.ID || got.Status != "completed" {
		t.Fatalf("job identity/status mismatch after reopen: %#v", got)
	}
	if got.CompletedAt == nil || got.FilesCount != 2 {
		t.Fatalf("job completion metadata mismatch: %#v", got)
	}
	if got.Result["agent_summary"] != "ranked two files" {
		t.Fatalf("job result not persisted: %#v", got.Result)
	}
	listed, err := reopened.ListLocalSearchJobs(ctx, LocalSearchJobFilter{ThreadID: thread.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListLocalSearchJobs: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("ListLocalSearchJobs=%#v, want created job", listed)
	}
}

func TestJSONStoreGetArtifactMetadata(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	content := "body"
	msg, err := st.AppendMessage(ctx, thread.ID, nil, Message{
		Role:    types.RoleAssistant,
		Content: &content,
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	ok := true
	lang := "json"
	tool := "search_docs"
	toolCallID := "tc_1"
	path := "artifacts/a1.json"
	meta := map[string]any{"score": 1}
	created, err := st.InsertArtifact(ctx, Artifact{
		ThreadID:    thread.ID,
		MessageID:   &msg.ID,
		Type:        "data",
		Title:       "Search Results",
		Language:    &lang,
		ToolName:    &tool,
		ToolCallID:  &toolCallID,
		OK:          &ok,
		Storage:     "file",
		ContentPath: &path,
		ByteSize:    123,
		Metadata:    meta,
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	got, err := st.GetArtifact(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.Storage != "file" {
		t.Fatalf("storage = %q, want file", got.Storage)
	}
	if got.ContentPath == nil || *got.ContentPath != path {
		t.Fatalf("content path mismatch: %#v", got.ContentPath)
	}
	if got.Metadata["score"] != float64(1) && got.Metadata["score"] != 1 {
		t.Fatalf("metadata score mismatch: %#v", got.Metadata["score"])
	}
}

func TestJSONStoreInsertArtifactRejectsDuplicateID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	originalContent := "original"
	created, err := st.InsertArtifact(ctx, Artifact{
		ID:          "same-artifact",
		ThreadID:    thread.ID,
		Type:        "file",
		Title:       "Original upload",
		Storage:     "mysql",
		ContentText: &originalContent,
		ByteSize:    int64(len(originalContent)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact original: %v", err)
	}

	replacementContent := "replacement"
	_, err = st.InsertArtifact(ctx, Artifact{
		ID:          created.ID,
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "Tool result",
		Storage:     "mysql",
		ContentText: &replacementContent,
		ByteSize:    int64(len(replacementContent)),
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate InsertArtifact err=%v, want ErrConflict", err)
	}

	got, err := st.GetArtifact(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.Type != "file" || got.Title != "Original upload" || got.ContentText == nil || *got.ContentText != originalContent {
		t.Fatalf("duplicate insert changed original artifact: %#v", got)
	}
}

func TestJSONStoreReplaceArtifactOverwritesInPlace(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	placeholderContent := ""
	runTool := "run_bigquery_query"
	created, err := st.InsertArtifact(ctx, Artifact{
		ID:          "art-async-1",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Running",
		ToolName:    &runTool,
		Storage:     "mysql",
		ContentText: &placeholderContent,
		Metadata:    map[string]any{"placeholder": true, "async_query": true, "job_id": "job-1"},
	})
	if err != nil {
		t.Fatalf("InsertArtifact placeholder: %v", err)
	}

	rows := `[{"a":1},{"a":2}]`
	resultsTool := "get_query_results"
	okVal := true
	replaced, err := st.ReplaceArtifact(ctx, Artifact{
		ID:          created.ID,
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Results",
		ToolName:    &resultsTool,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &rows,
		ByteSize:    int64(len(rows)),
		Metadata:    map[string]any{"placeholder": false, "row_count": 2},
	})
	if err != nil {
		t.Fatalf("ReplaceArtifact: %v", err)
	}
	if replaced.CreatedAt.IsZero() {
		t.Fatalf("ReplaceArtifact should preserve created_at")
	}

	got, err := st.GetArtifact(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetArtifact after replace: %v", err)
	}
	if got.ContentText == nil || *got.ContentText != rows {
		t.Fatalf("content not replaced: %#v", got.ContentText)
	}
	if got.ToolName == nil || *got.ToolName != resultsTool {
		t.Fatalf("tool name not replaced: %#v", got.ToolName)
	}
	if got.Metadata["placeholder"] != false {
		t.Fatalf("placeholder flag should be false after replace: %#v", got.Metadata)
	}

	// Replacing a missing artifact must report not-found, never create one.
	if _, err := st.ReplaceArtifact(ctx, Artifact{
		ID:          "does-not-exist",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "x",
		ContentText: &rows,
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReplaceArtifact missing err=%v, want ErrNotFound", err)
	}
}

func TestJSONStoreJobsClaimAndRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	job, err := st.EnqueueJob(ctx, "thread_title", thread.ID, nil)
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	claimed, err := st.ClaimNextJob(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("ClaimNextJob: %v", err)
	}
	if claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claimed job mismatch: %#v", claimed)
	}
	if claimed.Status != "running" {
		t.Fatalf("claimed status = %q, want running", claimed.Status)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("claimed attempts = %d, want 1", claimed.Attempts)
	}

	notBefore := time.Now().UTC().Add(time.Hour)
	if err := st.FailJob(ctx, job.ID, "retry", notBefore); err != nil {
		t.Fatalf("FailJob: %v", err)
	}
	none, err := st.ClaimNextJob(ctx, notBefore.Add(-time.Minute))
	if err != nil {
		t.Fatalf("ClaimNextJob immediate: %v", err)
	}
	if none != nil {
		t.Fatalf("expected nil claim before not_before, got %#v", none)
	}
	later, err := st.ClaimNextJob(ctx, notBefore.Add(50*time.Millisecond))
	if err != nil {
		t.Fatalf("ClaimNextJob after delay: %v", err)
	}
	if later == nil || later.ID != job.ID {
		t.Fatalf("expected retry claim for %s, got %#v", job.ID, later)
	}
	if later.Attempts != 2 {
		t.Fatalf("retry attempts = %d, want 2", later.Attempts)
	}
}

func TestJSONStoreUpsertProjectFileConcurrentNoLostUpdates(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	project, err := st.CreateProject(ctx, "load-test", "projects")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	const totalFiles = 3000
	const workers = 8

	var (
		mu       sync.Mutex
		firstErr error
	)
	paths := make(chan int, workers*2)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range paths {
				p := fmt.Sprintf("src/file_%04d.txt", n)
				if err := st.UpsertProjectFile(ctx, project.ID, p, int64(n), nil); err != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = err
					}
					mu.Unlock()
				}
			}
		}()
	}
	for i := 1; i <= totalFiles; i++ {
		paths <- i
	}
	close(paths)
	wg.Wait()

	if firstErr != nil {
		t.Fatalf("UpsertProjectFile: %v", firstErr)
	}

	finalized, err := st.FinalizeProject(ctx, project.ID)
	if err != nil {
		t.Fatalf("FinalizeProject: %v", err)
	}
	if finalized.FileCount != totalFiles {
		t.Fatalf("file_count = %d, want %d", finalized.FileCount, totalFiles)
	}

	// 1 + ... + totalFiles
	wantTotalBytes := int64(totalFiles*(totalFiles+1)) / 2
	if finalized.TotalBytes != wantTotalBytes {
		t.Fatalf("total_bytes = %d, want %d", finalized.TotalBytes, wantTotalBytes)
	}
}

func TestJSONStoreMessageContextMetadataRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	content := "assistant message"
	initialMsg, err := st.AppendMessage(ctx, thread.ID, nil, Message{
		Role:                  types.RoleAssistant,
		Content:               &content,
		ReferencedMessageIDs:  []string{"message-a", "message-b", "message-a"},
		ReferencedThreadIDs:   []string{"thread-a", "thread-b", "thread-a"},
		ReferencedArtifactIDs: []string{"artifact-a", "artifact-b", "artifact-a"},
		ContextSummary:        "initial summary",
		ContextPlan:           json.RawMessage(`{"need_retrieval":false}`),
		HiddenContext:         ptrToString("seed context"),
	})
	if err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}

	msgs, err := st.ListMessages(ctx, ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs)=%d, want 1", len(msgs))
	}
	msg := msgs[0]
	if msg.ID != initialMsg.ID {
		t.Fatalf("message ID mismatch: %q != %q", msg.ID, initialMsg.ID)
	}
	if len(msg.ReferencedArtifactIDs) != 2 {
		t.Fatalf("len(referenced_artifact_ids)=%d, want 2", len(msg.ReferencedArtifactIDs))
	}
	if len(msg.ReferencedMessageIDs) != 2 {
		t.Fatalf("len(referenced_message_ids)=%d, want 2", len(msg.ReferencedMessageIDs))
	}
	if len(msg.ReferencedThreadIDs) != 2 {
		t.Fatalf("len(referenced_thread_ids)=%d, want 2", len(msg.ReferencedThreadIDs))
	}
	if strings.TrimSpace(msg.ContextSummary) != "initial summary" {
		t.Fatalf("ContextSummary=%q, want %q", msg.ContextSummary, "initial summary")
	}
	if strings.TrimSpace(string(msg.ContextPlan)) != `{"need_retrieval":false}` {
		t.Fatalf("ContextPlan=%q, want %q", string(msg.ContextPlan), `{"need_retrieval":false}`)
	}
	if msg.HiddenContext == nil || strings.TrimSpace(*msg.HiddenContext) != "seed context" {
		t.Fatalf("HiddenContext=%#v, want %q", msg.HiddenContext, "seed context")
	}

	if err := st.UpdateMessage(ctx, msg.ID, map[string]any{
		"context_summary":         "updated summary",
		"referenced_message_ids":  []string{"message-c", "message-c", "message-d"},
		"referenced_thread_ids":   []string{"thread-c", "thread-c", "thread-d"},
		"referenced_artifact_ids": []string{"artifact-c", "artifact-c", "artifact-d"},
		"context_plan":            json.RawMessage(`{"need_retrieval_tooling":true}`),
		"hidden_context":          "updated context",
	}); err != nil {
		t.Fatalf("UpdateMessage: %v", err)
	}

	msgs, err = st.ListMessages(ctx, ListMessagesParams{
		ThreadID: thread.ID,
		Limit:    20,
	})
	if err != nil {
		t.Fatalf("ListMessages after update: %v", err)
	}
	msg = msgs[0]
	if strings.TrimSpace(msg.ContextSummary) != "updated summary" {
		t.Fatalf("ContextSummary=%q, want %q", msg.ContextSummary, "updated summary")
	}
	if len(msg.ReferencedArtifactIDs) != 2 {
		t.Fatalf("len(referenced_artifact_ids)=%d, want 2", len(msg.ReferencedArtifactIDs))
	}
	if len(msg.ReferencedMessageIDs) != 2 {
		t.Fatalf("len(referenced_message_ids)=%d, want 2", len(msg.ReferencedMessageIDs))
	}
	if len(msg.ReferencedThreadIDs) != 2 {
		t.Fatalf("len(referenced_thread_ids)=%d, want 2", len(msg.ReferencedThreadIDs))
	}
	if strings.TrimSpace(string(msg.ContextPlan)) != `{"need_retrieval_tooling":true}` {
		t.Fatalf("ContextPlan=%q, want %q", string(msg.ContextPlan), `{"need_retrieval_tooling":true}`)
	}
	if msg.HiddenContext == nil || strings.TrimSpace(*msg.HiddenContext) != "updated context" {
		t.Fatalf("HiddenContext=%#v, want %q", msg.HiddenContext, "updated context")
	}
}

func TestJSONStoreContextCheckpointActiveRoundTripAndRestart(t *testing.T) {
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
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model-a", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	content := "compacted context"
	cp, err := st.UpsertActiveContextCheckpoint(ctx, ContextCheckpoint{
		ThreadID:              thread.ID,
		RunID:                 &run.ID,
		SourceMessageSeq:      12,
		SourceMessageID:       "message-12",
		ReplacementHistory:    []types.ChatMessage{{Role: types.RoleSystem, Content: &content}},
		Summary:               "summary",
		SummaryMessageIDs:     []string{"m1", "m1", "m2"},
		ReferencedArtifactIDs: []string{"a1", "a1", "a2"},
		TokenEstimate:         123,
		CharsPerToken:         4,
		Model:                 "model-a",
		ProviderID:            "compatible",
		PlanJSON:              json.RawMessage(`{"need_retrieval":true}`),
	})
	if err != nil {
		t.Fatalf("UpsertActiveContextCheckpoint: %v", err)
	}
	if strings.TrimSpace(cp.ID) == "" {
		t.Fatal("checkpoint ID was not assigned")
	}
	if _, err := os.Stat(st.contextCheckpointActivePath(thread.ID)); err != nil {
		t.Fatalf("active checkpoint file missing: %v", err)
	}
	if _, err := os.Stat(st.contextCheckpointHistoryPath(thread.ID, cp.ID)); err != nil {
		t.Fatalf("checkpoint history file missing: %v", err)
	}

	got, err := st.GetActiveContextCheckpoint(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetActiveContextCheckpoint: %v", err)
	}
	if got == nil {
		t.Fatal("active checkpoint is nil")
	}
	if got.ID != cp.ID || got.ThreadID != thread.ID || got.RunID == nil || *got.RunID != run.ID {
		t.Fatalf("checkpoint identity mismatch: %#v", got)
	}
	if got.SourceMessageSeq != 12 || got.SourceMessageID != "message-12" {
		t.Fatalf("source mismatch: seq=%d id=%q", got.SourceMessageSeq, got.SourceMessageID)
	}
	if len(got.ReplacementHistory) != 1 || got.ReplacementHistory[0].Content == nil || *got.ReplacementHistory[0].Content != content {
		t.Fatalf("replacement history mismatch: %#v", got.ReplacementHistory)
	}
	if len(got.SummaryMessageIDs) != 2 || len(got.ReferencedArtifactIDs) != 2 {
		t.Fatalf("deduped references mismatch: summary=%#v artifacts=%#v", got.SummaryMessageIDs, got.ReferencedArtifactIDs)
	}
	if strings.TrimSpace(string(got.PlanJSON)) != `{"need_retrieval":true}` {
		t.Fatalf("PlanJSON=%q", string(got.PlanJSON))
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("NewJSON reopen: %v", err)
	}
	afterRestart, err := reopened.GetActiveContextCheckpoint(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetActiveContextCheckpoint after restart: %v", err)
	}
	if afterRestart == nil || afterRestart.ID != cp.ID {
		t.Fatalf("restart checkpoint mismatch: %#v", afterRestart)
	}
}

func TestJSONStoreListContextCheckpointsNewestFirst(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	content := "checkpoint"
	first, err := st.UpsertActiveContextCheckpoint(ctx, ContextCheckpoint{
		ID:                 "cp-first",
		ThreadID:           thread.ID,
		SourceMessageSeq:   1,
		SourceMessageID:    "m1",
		ReplacementHistory: []types.ChatMessage{{Role: types.RoleSystem, Content: &content}},
		CreatedAt:          time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Upsert first: %v", err)
	}
	second, err := st.UpsertActiveContextCheckpoint(ctx, ContextCheckpoint{
		ID:                 "cp-second",
		ThreadID:           thread.ID,
		SourceMessageSeq:   2,
		SourceMessageID:    "m2",
		ReplacementHistory: []types.ChatMessage{{Role: types.RoleSystem, Content: &content}},
		CreatedAt:          time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Upsert second: %v", err)
	}
	if first.ID == second.ID {
		t.Fatal("checkpoint IDs should differ")
	}

	active, err := st.GetActiveContextCheckpoint(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetActiveContextCheckpoint: %v", err)
	}
	if active == nil || active.ID != second.ID {
		t.Fatalf("active checkpoint=%#v, want %s", active, second.ID)
	}

	list, err := st.ListContextCheckpoints(ctx, thread.ID, 10)
	if err != nil {
		t.Fatalf("ListContextCheckpoints: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list)=%d, want 2", len(list))
	}
	if list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("list order=%#v, want newest first", []string{list[0].ID, list[1].ID})
	}
}

func TestJSONStoreActiveContextCheckpointMonotonicActivation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	content := "checkpoint"
	newer, err := st.UpsertActiveContextCheckpoint(ctx, ContextCheckpoint{
		ID:                 "cp-newer",
		ThreadID:           thread.ID,
		SourceMessageSeq:   10,
		SourceMessageID:    "m10",
		ReplacementHistory: []types.ChatMessage{{Role: types.RoleSystem, Content: &content}},
		CreatedAt:          time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Upsert newer: %v", err)
	}
	if newer.ID != "cp-newer" {
		t.Fatalf("newer ID=%q", newer.ID)
	}

	stale, err := st.UpsertActiveContextCheckpoint(ctx, ContextCheckpoint{
		ID:                 "cp-stale",
		ThreadID:           thread.ID,
		SourceMessageSeq:   9,
		SourceMessageID:    "m9",
		ReplacementHistory: []types.ChatMessage{{Role: types.RoleSystem, Content: &content}},
		CreatedAt:          time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 5, 22, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Upsert stale: %v", err)
	}
	if stale.ID != newer.ID || stale.SourceMessageSeq != newer.SourceMessageSeq {
		t.Fatalf("stale upsert returned %#v, want active checkpoint %#v", stale, newer)
	}
	active, err := st.GetActiveContextCheckpoint(ctx, thread.ID)
	if err != nil {
		t.Fatalf("GetActiveContextCheckpoint: %v", err)
	}
	if active == nil || active.ID != newer.ID || active.SourceMessageSeq != 10 {
		t.Fatalf("active checkpoint=%#v, want newer", active)
	}
	if _, err := os.Stat(st.contextCheckpointHistoryPath(thread.ID, "cp-stale")); err != nil {
		t.Fatalf("stale checkpoint history missing: %v", err)
	}

	advanced, err := st.UpsertActiveContextCheckpoint(ctx, ContextCheckpoint{
		ID:                 "cp-advanced",
		ThreadID:           thread.ID,
		SourceMessageSeq:   11,
		SourceMessageID:    "m11",
		ReplacementHistory: []types.ChatMessage{{Role: types.RoleSystem, Content: &content}},
		CreatedAt:          time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
		UpdatedAt:          time.Date(2026, 5, 22, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Upsert advanced: %v", err)
	}
	if advanced.ID != "cp-advanced" || advanced.SourceMessageSeq != 11 {
		t.Fatalf("advanced checkpoint=%#v", advanced)
	}
}

func TestJSONStoreRunEventsAppendListLatestAndRestart(t *testing.T) {
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
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	first, err := st.AppendRunEvent(ctx, RunEvent{
		RunID: run.ID,
		Type:  "text_delta",
		Data:  json.RawMessage(`{"content":"one"}`),
	})
	if err != nil {
		t.Fatalf("AppendRunEvent first: %v", err)
	}
	second, err := st.AppendRunEvent(ctx, RunEvent{
		RunID: run.ID,
		Type:  "final",
		Data:  json.RawMessage(`{"answer":"done"}`),
	})
	if err != nil {
		t.Fatalf("AppendRunEvent second: %v", err)
	}
	if first.Seq != 1 || second.Seq != 2 {
		t.Fatalf("event seqs=(%d,%d), want (1,2)", first.Seq, second.Seq)
	}
	if first.ThreadID != thread.ID || second.ThreadID != thread.ID {
		t.Fatalf("event thread IDs=(%q,%q), want %q", first.ThreadID, second.ThreadID, thread.ID)
	}
	latest, err := st.LatestRunEventSeq(ctx, run.ID)
	if err != nil {
		t.Fatalf("LatestRunEventSeq: %v", err)
	}
	if latest != 2 {
		t.Fatalf("latest seq=%d, want 2", latest)
	}

	events, err := st.ListRunEvents(ctx, ListRunEventsParams{RunID: run.ID, AfterSeq: 1, Limit: 10})
	if err != nil {
		t.Fatalf("ListRunEvents: %v", err)
	}
	if len(events) != 1 || events[0].Seq != 2 || events[0].Type != "final" {
		t.Fatalf("events after seq 1=%#v, want final seq 2", events)
	}
	if strings.TrimSpace(string(events[0].Data)) != `{"answer":"done"}` {
		t.Fatalf("event data=%q", string(events[0].Data))
	}

	reopened, err := NewJSON(dir)
	if err != nil {
		t.Fatalf("NewJSON reopen: %v", err)
	}
	latest, err = reopened.LatestRunEventSeq(ctx, run.ID)
	if err != nil {
		t.Fatalf("LatestRunEventSeq after reopen: %v", err)
	}
	if latest != 2 {
		t.Fatalf("latest seq after reopen=%d, want 2", latest)
	}
	events, err = reopened.ListRunEvents(ctx, ListRunEventsParams{RunID: run.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListRunEvents after reopen: %v", err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events after reopen=%#v, want seqs 1,2", events)
	}
}

func TestJSONStoreRunEventsRejectDuplicateSeq(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, err := NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "compatible", "model", nil, "prompt", 12)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.AppendRunEvent(ctx, RunEvent{RunID: run.ID, Seq: 7, Type: "text_delta", Data: json.RawMessage(`{"content":"one"}`)}); err != nil {
		t.Fatalf("AppendRunEvent first: %v", err)
	}
	if _, err := st.AppendRunEvent(ctx, RunEvent{RunID: run.ID, Seq: 7, Type: "text_delta", Data: json.RawMessage(`{"content":"two"}`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate AppendRunEvent err=%v, want ErrConflict", err)
	}
	if _, err := st.AppendRunEvent(ctx, RunEvent{RunID: run.ID, Seq: 6, Type: "text_delta", Data: json.RawMessage(`{"content":"old"}`)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("lower-seq AppendRunEvent err=%v, want ErrConflict", err)
	}
}
