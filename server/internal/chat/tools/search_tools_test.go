package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/search"
	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

func toolErrorMessage(t *testing.T, out Output) string {
	t.Helper()
	if out.OK {
		t.Fatalf("expected tool failure")
	}
	m, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected tool result payload: %#v", out.Result)
	}
	raw, ok := m["error"]
	if !ok {
		t.Fatalf("tool result missing error field: %#v", m)
	}
	msg, ok := raw.(string)
	if !ok {
		t.Fatalf("tool error is not string: %#v", raw)
	}
	return strings.TrimSpace(msg)
}

func requireThreadSearchResultMetadata(t *testing.T, out Output, toolName string, kind string) map[string]any {
	t.Helper()
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("%s result=%T, want map[string]any: %#v", toolName, out.Result, out.Result)
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
	}
	meta := out.ResultMetadata
	requireThreadSearchMetadataFields(t, meta, toolName, threadSearchMetadataCatalogName(toolName), kind)
	return meta
}

func requireThreadSearchMetadataFields(t *testing.T, meta map[string]any, toolName string, catalogName string, kind string) {
	t.Helper()
	if len(meta) == 0 {
		t.Fatalf("%s ResultMetadata is empty", toolName)
	}
	toolMeta, ok := ToolMetadataByName(catalogName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) missing", catalogName)
	}
	expected := map[string]any{
		"provider":          "thread-search",
		"product":           toolMeta.Presentation.Family,
		"family":            toolMeta.Presentation.Family,
		"tool_name":         toolName,
		"tool_set_id":       toolMeta.ToolSetID,
		"tool_set_name":     toolMeta.ToolSetName,
		"connector_id":      toolMeta.ConnectorID,
		"category":          string(toolMeta.Category),
		"origin":            string(toolMeta.Origin),
		"kind":              kind,
		"result_kind":       toolMeta.Presentation.ResultKind,
		"native_viewer":     toolMeta.Presentation.NativeViewer,
		"renderer_strategy": toolMeta.Presentation.RendererStrategy,
		"renderer_key":      toolMeta.Presentation.RendererKey,
		"readonly":          true,
	}
	for key, want := range expected {
		if got := meta[key]; got != want {
			t.Fatalf("%s ResultMetadata[%s]=%#v, want %#v; metadata=%#v", toolName, key, got, want, meta)
		}
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("%s ResultMetadata.tool_presentation=%T, want map[string]any; metadata=%#v", toolName, meta["tool_presentation"], meta)
	}
	if got := presentation["family"]; got != toolMeta.Presentation.Family {
		t.Fatalf("%s tool_presentation.family=%#v, want %#v; presentation=%#v", toolName, got, toolMeta.Presentation.Family, presentation)
	}
	if got := presentation["result_kind"]; got != toolMeta.Presentation.ResultKind {
		t.Fatalf("%s tool_presentation.result_kind=%#v, want %#v; presentation=%#v", toolName, got, toolMeta.Presentation.ResultKind, presentation)
	}
	if got := presentation["native_viewer"]; got != toolMeta.Presentation.NativeViewer {
		t.Fatalf("%s tool_presentation.native_viewer=%#v, want %#v; presentation=%#v", toolName, got, toolMeta.Presentation.NativeViewer, presentation)
	}
	if got := presentation["renderer_strategy"]; got != toolMeta.Presentation.RendererStrategy {
		t.Fatalf("%s tool_presentation.renderer_strategy=%#v, want %#v; presentation=%#v", toolName, got, toolMeta.Presentation.RendererStrategy, presentation)
	}
	if got := presentation["renderer_key"]; got != toolMeta.Presentation.RendererKey {
		t.Fatalf("%s tool_presentation.renderer_key=%#v, want %#v; presentation=%#v", toolName, got, toolMeta.Presentation.RendererKey, presentation)
	}
}

func threadSearchMetadataCatalogName(toolName string) string {
	if strings.TrimSpace(toolName) == "search_index" {
		return "search_threads"
	}
	return strings.TrimSpace(toolName)
}

func TestThreadSearchResultMetadataUsesCatalogPresentation(t *testing.T) {
	cases := []struct {
		toolName    string
		catalogName string
		kind        string
	}{
		{"search_threads", "search_threads", "search_index"},
		{"search_docs", "search_docs", "search_index"},
		{"search_index", "search_threads", "search_index"},
		{"get_doc", "get_doc", "search_document"},
		{"get_artifact", "get_artifact", "artifact_chunk"},
		{"list_messages", "list_messages", "thread_messages"},
	}

	for _, tc := range cases {
		t.Run(tc.toolName, func(t *testing.T) {
			meta := threadSearchResultMetadata(tc.toolName, tc.kind, map[string]any{"count": 1})
			requireThreadSearchMetadataFields(t, meta, tc.toolName, tc.catalogName, tc.kind)
			if got := meta["count"]; got != 1 {
				t.Fatalf("count=%#v, want 1; metadata=%#v", got, meta)
			}
			if tc.toolName == "search_index" && meta["canonical_tool_name"] != "search_threads" {
				t.Fatalf("search_index canonical_tool_name=%#v, want search_threads; metadata=%#v", meta["canonical_tool_name"], meta)
			}
		})
	}
}

func newSearchFixtureWithMessage(t *testing.T, ctx context.Context, cfgDir string, projectID string, content string) (store.Store, *search.Service, store.Thread) {
	t.Helper()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	var projectRef *string
	if strings.TrimSpace(projectID) != "" {
		projectRef = &projectID
	}
	thread, err := st.CreateThread(ctx, projectRef)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-4.1", nil, "sys", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		ID:      types.NewUUID(),
		Role:    types.RoleUser,
		Content: &content,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	svc, err := search.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	if err := svc.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	return st, svc, thread
}

func TestListMessagesToolValidationRejectsInvalidArgs(t *testing.T) {
	ctx := context.Background()
	tool := newListMessagesTool(nil)

	tests := []struct {
		name       string
		args       string
		errorMatch string
	}{
		{
			name:       "invalid order_by",
			args:       `{"thread_id":"thread-1","order_by":"random"}`,
			errorMatch: "order_by must be one of: seq_desc, seq_asc",
		},
		{
			name:       "before_seq_relation",
			args:       `{"thread_id":"thread-1","before_seq":1,"after_seq":2}`,
			errorMatch: "before_seq must be greater than after_seq",
		},
		{
			name:       "invalid before_ts",
			args:       `{"thread_id":"thread-1","before_ts":"not-a-timestamp"}`,
			errorMatch: "before_ts must be RFC3339 timestamp",
		},
		{
			name:       "max result cap exceeded",
			args:       `{"thread_id":"thread-1","limit":101}`,
			errorMatch: "limit must be <= 100",
		},
		{
			name:       "content cap exceeded",
			args:       `{"thread_id":"thread-1","max_chars_per_result":4000}`,
			errorMatch: "max_chars_per_result must be <= 3000",
		},
		{
			name:       "non-string artifact_id values rejected",
			args:       `{"thread_id":"thread-1","artifact_ids":["artifact-1",42]}`,
			errorMatch: "Invalid JSON arguments.",
		},
		{
			name:       "strict unknown field rejection",
			args:       `{"thread_id":"thread-1","unexpected_field":"oops"}`,
			errorMatch: "Invalid JSON arguments.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("tool.Execute returned error: %v", err)
			}
			if got := toolErrorMessage(t, out); !strings.Contains(got, tc.errorMatch) {
				t.Fatalf("error = %q, want substring %q", got, tc.errorMatch)
			}
		})
	}
}

func TestListMessagesToolOrdersAndTruncates(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 1; i <= 4; i++ {
		content := fmt.Sprintf("message-%d %s", i, strings.Repeat("x", 32))
		_, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			Role:     types.RoleUser,
			Content:  &content,
			UIHidden: i == 3,
		})
		if err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	tool := newListMessagesTool(st)
	args := fmt.Sprintf(`{"thread_id":"%s","order_by":"seq_desc","limit":2,"max_chars_per_result":6}`, thread.ID)
	out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(args))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected failure-free output")
	}

	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	rawMessages, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("result.messages missing or invalid: %#v", payload["messages"])
	}
	if len(rawMessages) != 2 {
		t.Fatalf("len(messages)=%d, want 2", len(rawMessages))
	}

	seenSeqs := make([]int64, 0, len(rawMessages))
	for _, row := range rawMessages {
		msg, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("message row type mismatch: %#v", row)
		}
		seqRaw, ok := msg["seq"].(float64)
		if !ok {
			t.Fatalf("message seq type mismatch: %#v", msg["seq"])
		}
		seenSeqs = append(seenSeqs, int64(seqRaw))

		contentRaw, ok := msg["content"].(string)
		if !ok {
			t.Fatalf("message content type mismatch: %#v", msg["content"])
		}
		if len([]rune(contentRaw)) > 6 {
			t.Fatalf("message content too long: %q", contentRaw)
		}
	}

	if len(seenSeqs) != 2 || seenSeqs[0] != 4 || seenSeqs[1] != 2 {
		t.Fatalf("message sequences=%v, want [4,2] (skip hidden seq 3)", seenSeqs)
	}
	meta := requireThreadSearchResultMetadata(t, out, "list_messages", "thread_messages")
	if got := meta["thread_id"]; got != thread.ID {
		t.Fatalf("ResultMetadata.thread_id=%#v, want %q; metadata=%#v", got, thread.ID, meta)
	}
	if got := meta["count"]; got != 2 {
		t.Fatalf("ResultMetadata.count=%#v, want 2; metadata=%#v", got, meta)
	}
	if got := meta["limit"]; got != 2 {
		t.Fatalf("ResultMetadata.limit=%#v, want 2; metadata=%#v", got, meta)
	}
	if got := meta["max_chars_per_result"]; got != 6 {
		t.Fatalf("ResultMetadata.max_chars_per_result=%#v, want 6; metadata=%#v", got, meta)
	}
}

func TestListMessagesToolPaginatesWithStableDescendingCursor(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 1; i <= 5; i++ {
		content := fmt.Sprintf("message-%d", i)
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{Role: types.RoleUser, Content: &content}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	tool := newListMessagesTool(st)
	first, err := tool.Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "order_by": "seq_desc", "limit": 2,
	}))
	if err != nil || !first.OK {
		t.Fatalf("first page output=%+v err=%v", first, err)
	}
	if got := listMessageResultSeqs(t, first); !equalInt64s(got, []int64{5, 4}) {
		t.Fatalf("first page seqs=%v, want [5 4]", got)
	}
	firstResult := asMap(t, first.Result)
	firstToken := strings.TrimSpace(anyToString(firstResult["next_page_token"]))
	if firstResult["has_more"] != true || firstToken == "" {
		t.Fatalf("first page cursor fields=%#v", firstResult)
	}
	conflict, err := tool.Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "page_token": firstToken, "before_seq": 4,
	}))
	if err != nil || conflict.OK || !strings.Contains(toolErrorMessage(t, conflict), "page_token cannot be combined with before_seq") {
		t.Fatalf("page token conflict output=%+v err=%v", conflict, err)
	}

	// A new newest message must not shift an older-page continuation.
	newest := "message-6"
	if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{Role: types.RoleUser, Content: &newest}); err != nil {
		t.Fatalf("AppendMessage(newest): %v", err)
	}
	second, err := tool.Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "page_token": firstToken,
	}))
	if err != nil || !second.OK {
		t.Fatalf("second page output=%+v err=%v", second, err)
	}
	if got := listMessageResultSeqs(t, second); !equalInt64s(got, []int64{3, 2}) {
		t.Fatalf("second page seqs=%v, want [3 2]", got)
	}
	secondResult := asMap(t, second.Result)
	secondToken := strings.TrimSpace(anyToString(secondResult["next_page_token"]))
	if secondResult["has_more"] != true || secondToken == "" {
		t.Fatalf("second page cursor fields=%#v", secondResult)
	}

	third, err := tool.Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "page_token": secondToken,
	}))
	if err != nil || !third.OK {
		t.Fatalf("third page output=%+v err=%v", third, err)
	}
	if got := listMessageResultSeqs(t, third); !equalInt64s(got, []int64{1}) {
		t.Fatalf("third page seqs=%v, want [1]", got)
	}
	thirdResult := asMap(t, third.Result)
	if thirdResult["has_more"] != false || strings.TrimSpace(anyToString(thirdResult["next_page_token"])) != "" {
		t.Fatalf("terminal page cursor fields=%#v", thirdResult)
	}
}

func TestListMessagesToolPaginatesAscendingWithoutGaps(t *testing.T) {
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
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{Role: types.RoleUser, Content: &content}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}
	tool := newListMessagesTool(st)
	first, err := tool.Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "order_by": "seq_asc", "limit": 2,
	}))
	if err != nil || !first.OK {
		t.Fatalf("first ascending page output=%+v err=%v", first, err)
	}
	if got := listMessageResultSeqs(t, first); !equalInt64s(got, []int64{1, 2}) {
		t.Fatalf("first ascending page seqs=%v, want [1 2]", got)
	}
	token := strings.TrimSpace(anyToString(asMap(t, first.Result)["next_page_token"]))
	second, err := tool.Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "page_token": token,
	}))
	if err != nil || !second.OK {
		t.Fatalf("second ascending page output=%+v err=%v", second, err)
	}
	if got := listMessageResultSeqs(t, second); !equalInt64s(got, []int64{3}) {
		t.Fatalf("second ascending page seqs=%v, want [3]", got)
	}
	if asMap(t, second.Result)["has_more"] != false {
		t.Fatalf("second ascending page should be terminal: %#v", second.Result)
	}
}

func TestListMessagesToolDoesNotReportFalsePositiveHasMore(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 1; i <= 2; i++ {
		content := fmt.Sprintf("message-%d", i)
		if _, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{Role: types.RoleUser, Content: &content}); err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}
	out, err := newListMessagesTool(st).Execute(ctx, ExecContext{}, mustJSON(t, map[string]any{
		"thread_id": thread.ID, "limit": 2,
	}))
	if err != nil || !out.OK {
		t.Fatalf("exact page output=%+v err=%v", out, err)
	}
	result := asMap(t, out.Result)
	if result["has_more"] != false || strings.TrimSpace(anyToString(result["next_page_token"])) != "" {
		t.Fatalf("exact page advertised a continuation: %#v", result)
	}
	meta := requireThreadSearchResultMetadata(t, out, "list_messages", "thread_messages")
	if meta["truncation"] != false || meta["has_more"] != false {
		t.Fatalf("exact page metadata=%#v", meta)
	}
}

func listMessageResultSeqs(t *testing.T, out Output) []int64 {
	t.Helper()
	result := asMap(t, out.Result)
	rows, ok := result["messages"].([]any)
	if !ok {
		t.Fatalf("messages type=%T value=%#v", result["messages"], result["messages"])
	}
	seqs := make([]int64, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("message row type=%T value=%#v", raw, raw)
		}
		seq, ok := row["seq"].(float64)
		if !ok {
			t.Fatalf("message seq type=%T value=%#v", row["seq"], row["seq"])
		}
		seqs = append(seqs, int64(seq))
	}
	return seqs
}

func equalInt64s(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestListMessagesToolSupportsSeqAscOrder(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	for i := 1; i <= 4; i++ {
		content := fmt.Sprintf("asc-message-%d", i)
		_, err := st.AppendMessage(ctx, thread.ID, nil, store.Message{
			Role:    types.RoleUser,
			Content: &content,
		})
		if err != nil {
			t.Fatalf("AppendMessage(%d): %v", i, err)
		}
	}

	tool := newListMessagesTool(st)
	args := fmt.Sprintf(`{"thread_id":"%s","order_by":"seq_asc","limit":3}`, thread.ID)
	out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(args))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected failure-free output")
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	rawMessages, ok := payload["messages"].([]any)
	if !ok {
		t.Fatalf("result.messages missing or invalid: %#v", payload["messages"])
	}
	if len(rawMessages) != 3 {
		t.Fatalf("len(messages)=%d, want 3", len(rawMessages))
	}
	seenSeqs := make([]int64, 0, len(rawMessages))
	for _, row := range rawMessages {
		msg, ok := row.(map[string]any)
		if !ok {
			t.Fatalf("message row type mismatch: %#v", row)
		}
		seqRaw, ok := msg["seq"].(float64)
		if !ok {
			t.Fatalf("message seq type mismatch: %#v", msg["seq"])
		}
		seenSeqs = append(seenSeqs, int64(seqRaw))
	}
	if len(seenSeqs) != 3 || seenSeqs[0] != 1 || seenSeqs[1] != 2 || seenSeqs[2] != 3 {
		t.Fatalf("message sequences=%v, want [1,2,3]", seenSeqs)
	}
}

func TestGetArtifactToolOmitsImageDataByDefault(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	imageData := "data:image/png;base64,AAAA"
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "image",
		Title:       "Screenshot",
		Storage:     "mysql",
		ContentText: &imageData,
		ByteSize:    int64(len(imageData)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newGetArtifactTool(st, t.TempDir())
	args := fmt.Sprintf(`{"artifact_id":"%s"}`, artifact.ID)
	out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(args))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}

	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	content, _ := payload["content"].(string)
	if content != "" {
		t.Fatalf("content = %q, want empty (image data omitted)", content)
	}
	if omitted, _ := payload["image_content_omitted"].(bool); !omitted {
		t.Fatalf("image_content_omitted = %#v, want true", payload["image_content_omitted"])
	}
	if note, _ := payload["content_note"].(string); !strings.Contains(note, "include_image_data=true") {
		t.Fatalf("content_note = %q, want include_image_data guidance", note)
	}
	meta := requireThreadSearchResultMetadata(t, out, "get_artifact", "artifact_chunk")
	if got := meta["artifact_id"]; got != artifact.ID {
		t.Fatalf("ResultMetadata.artifact_id=%#v, want %q; metadata=%#v", got, artifact.ID, meta)
	}
	if got := meta["image_content_omitted"]; got != true {
		t.Fatalf("ResultMetadata.image_content_omitted=%#v, want true; metadata=%#v", got, meta)
	}
	if got := meta["bytes_returned"]; got != 0 {
		t.Fatalf("ResultMetadata.bytes_returned=%#v, want 0; metadata=%#v", got, meta)
	}
}

func TestGetArtifactToolIncludesImageDataWhenRequested(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	imageData := "data:image/png;base64,BBBB"
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "image",
		Title:       "Screenshot",
		Storage:     "mysql",
		ContentText: &imageData,
		ByteSize:    int64(len(imageData)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newGetArtifactTool(st, t.TempDir())
	args := fmt.Sprintf(`{"artifact_id":"%s","include_image_data":true}`, artifact.ID)
	out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(args))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}

	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result payload: %#v", out.Result)
	}
	content, _ := payload["content"].(string)
	if content != imageData {
		t.Fatalf("content = %q, want %q", content, imageData)
	}
	if _, exists := payload["image_content_omitted"]; exists {
		t.Fatalf("image_content_omitted should not be present when include_image_data=true")
	}
	meta := requireThreadSearchResultMetadata(t, out, "get_artifact", "artifact_chunk")
	if got := meta["artifact_id"]; got != artifact.ID {
		t.Fatalf("ResultMetadata.artifact_id=%#v, want %q; metadata=%#v", got, artifact.ID, meta)
	}
	if got := meta["content_size"]; got != len(imageData) {
		t.Fatalf("ResultMetadata.content_size=%#v, want %d; metadata=%#v", got, len(imageData), meta)
	}
	if got := meta["truncation"]; got != false {
		t.Fatalf("ResultMetadata.truncation=%#v, want false; metadata=%#v", got, meta)
	}
}

func TestSearchDocsToolUsesFirstClassResultMetadata(t *testing.T) {
	ctx := context.Background()
	_, svc, _ := newSearchFixtureWithMessage(t, ctx, t.TempDir(), "", "alpha metadata appears in indexed history")

	tool := newSearchDocsTool(svc)
	out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(`{"q":"alpha metadata","limit":3,"max_chars_per_result":12}`))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %#v", out.Result)
	}
	results, ok := payload["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results type mismatch: %#v", payload["results"])
	}
	if len(results) == 0 {
		t.Fatal("expected search_docs results, got none")
	}
	meta := requireThreadSearchResultMetadata(t, out, "search_docs", "search_index")
	if got := meta["query"]; got != "alpha metadata" {
		t.Fatalf("ResultMetadata.query=%#v, want alpha metadata; metadata=%#v", got, meta)
	}
	if got := meta["count"]; got != len(results) {
		t.Fatalf("ResultMetadata.count=%#v, want %d; metadata=%#v", got, len(results), meta)
	}
	if got := meta["include_files"]; got != false {
		t.Fatalf("ResultMetadata.include_files=%#v, want false; metadata=%#v", got, meta)
	}
	if got := meta["max_chars_per_result"]; got != 12 {
		t.Fatalf("ResultMetadata.max_chars_per_result=%#v, want 12; metadata=%#v", got, meta)
	}
}

func TestGetDocToolUsesFirstClassResultMetadata(t *testing.T) {
	ctx := context.Background()
	_, svc, thread := newSearchFixtureWithMessage(t, ctx, t.TempDir(), "", "needle document text is long enough to truncate")
	_, results, err := svc.Search(ctx, search.Query{Text: "needle document", Limit: 5, Status: "*", Intent: "keyword"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected indexed results, got none")
	}

	tool := newGetDocTool(svc)
	args := fmt.Sprintf(`{"doc_id":"%s","max_chars":8}`, results[0].DocID)
	out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(args))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %#v", out.Result)
	}
	text, _ := payload["text"].(string)
	if len([]rune(text)) > 8 {
		t.Fatalf("text len=%d, want <= 8: %q", len([]rune(text)), text)
	}
	meta := requireThreadSearchResultMetadata(t, out, "get_doc", "search_document")
	if got := meta["doc_id"]; got != results[0].DocID {
		t.Fatalf("ResultMetadata.doc_id=%#v, want %q; metadata=%#v", got, results[0].DocID, meta)
	}
	if got := meta["thread_id"]; got != thread.ID {
		t.Fatalf("ResultMetadata.thread_id=%#v, want %q; metadata=%#v", got, thread.ID, meta)
	}
	if got := meta["max_chars"]; got != 8 {
		t.Fatalf("ResultMetadata.max_chars=%#v, want 8; metadata=%#v", got, meta)
	}
	if got := meta["truncation"]; got != true {
		t.Fatalf("ResultMetadata.truncation=%#v, want true; metadata=%#v", got, meta)
	}
}

func TestSearchDocsToolValidationRejectsInvalidArgs(t *testing.T) {
	ctx := context.Background()
	tool := newSearchDocsTool(nil)

	tests := []struct {
		name       string
		args       string
		errorMatch string
	}{
		{
			name:       "invalid order_by",
			args:       `{"q":"query","order_by":"bad"}`,
			errorMatch: "order_by must be one of: score_desc, updated_at_desc, updated_at_asc",
		},
		{
			name:       "invalid limit",
			args:       `{"q":"query","limit":51}`,
			errorMatch: "limit must be <= 50",
		},
		{
			name:       "min_score too small",
			args:       `{"q":"query","min_score":-0.1}`,
			errorMatch: "min_score must be >= 0",
		},
		{
			name:       "min_score too large",
			args:       `{"q":"query","min_score":1.2}`,
			errorMatch: "min_score must be <= 1",
		},
		{
			name:       "invalid before_ts",
			args:       `{"q":"query","before_ts":"2025-99-99T00:00:00Z"}`,
			errorMatch: "before_ts must be RFC3339 timestamp",
		},
		{
			name:       "invalid max_chars_per_result",
			args:       `{"q":"query","max_chars_per_result":600}`,
			errorMatch: "max_chars_per_result must be <= 500",
		},
		{
			name:       "strict unknown field rejection",
			args:       `{"q":"query","bogus":"x"}`,
			errorMatch: "Invalid JSON arguments.",
		},
		{
			name:       "non-string artifact filter rejected",
			args:       `{"q":"query","artifact_ids":["a",1]}`,
			errorMatch: "Invalid JSON arguments.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := tool.Execute(ctx, ExecContext{}, json.RawMessage(tc.args))
			if err != nil {
				t.Fatalf("tool.Execute returned error: %v", err)
			}
			if got := toolErrorMessage(t, out); !strings.Contains(got, tc.errorMatch) {
				t.Fatalf("error = %q, want substring %q", got, tc.errorMatch)
			}
		})
	}
}

func TestSearchThreadsToolSearchesHistoryAndCurrentProjectFiles(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	projectID := "project-1"

	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	if err := ps.PutFile(projectID, "notes/revenue.txt", []byte("alpha revenue appears in a project file")); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	thread, err := st.CreateThread(ctx, &projectID)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	run, err := st.CreateRun(ctx, thread.ID, "openai", "gpt-4.1", nil, "sys", 8)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	content := "Please remember alpha revenue from this conversation."
	if _, err := st.AppendMessage(ctx, thread.ID, &run.ID, store.Message{
		ID:      types.NewUUID(),
		Role:    types.RoleUser,
		Content: &content,
	}); err != nil {
		t.Fatalf("AppendMessage: %v", err)
	}
	if err := st.ArchiveThread(ctx, thread.ID); err != nil {
		t.Fatalf("ArchiveThread: %v", err)
	}

	svc, err := search.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	if err := svc.Rebuild(ctx); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	tool := newSearchThreadsTool(svc)
	out, err := tool.Execute(ctx, ExecContext{
		ProjectID: projectID,
		Projects:  ps,
	}, json.RawMessage(`{"query":"alpha revenue","limit":5,"file_limit":5}`))
	if err != nil {
		t.Fatalf("tool.Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK output, got %#v", out.Result)
	}
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %#v", out.Result)
	}

	results, ok := payload["results"].([]map[string]any)
	if !ok {
		t.Fatalf("results type mismatch: %#v", payload["results"])
	}
	if len(results) == 0 {
		t.Fatalf("expected conversation search results, got none")
	}

	fileSearch, ok := payload["file_search"].(map[string]any)
	if !ok {
		t.Fatalf("file_search missing or invalid: %#v", payload["file_search"])
	}
	if available, _ := fileSearch["available"].(bool); !available {
		t.Fatalf("expected file search available, got %#v", fileSearch)
	}
	matches, ok := fileSearch["matches"].([]map[string]any)
	if !ok {
		t.Fatalf("file_search.matches type mismatch: %#v", fileSearch["matches"])
	}
	if len(matches) == 0 {
		t.Fatalf("expected project file matches, got none")
	}
	if got, _ := matches[0]["path"].(string); got != "notes/revenue.txt" {
		t.Fatalf("first file match path = %q, want notes/revenue.txt", got)
	}
	meta := requireThreadSearchResultMetadata(t, out, "search_threads", "search_index")
	if got := meta["query"]; got != "alpha revenue" {
		t.Fatalf("ResultMetadata.query=%#v, want alpha revenue; metadata=%#v", got, meta)
	}
	if got := meta["count"]; got != len(results) {
		t.Fatalf("ResultMetadata.count=%#v, want %d; metadata=%#v", got, len(results), meta)
	}
	if got := meta["include_files"]; got != true {
		t.Fatalf("ResultMetadata.include_files=%#v, want true; metadata=%#v", got, meta)
	}
	if got := meta["file_count"]; got != len(matches) {
		t.Fatalf("ResultMetadata.file_count=%#v, want %d; metadata=%#v", got, len(matches), meta)
	}
	if got := meta["project_id"]; got != projectID {
		t.Fatalf("ResultMetadata.project_id=%#v, want %q; metadata=%#v", got, projectID, meta)
	}
}

func TestRegistryExposesSearchThreadsByDefaultAndKeepsSearchDocsAlias(t *testing.T) {
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	svc, err := search.NewService(cfgDir, st)
	if err != nil {
		t.Fatalf("search.NewService: %v", err)
	}
	reg := NewRegistry(&RegistryOptions{Search: svc, Store: st, CfgDir: cfgDir})

	defaultDefs := reg.Definitions()
	if toolDefsContain(defaultDefs, "search_threads") {
		t.Fatalf("default definitions should not include connection-scoped search_threads")
	}
	for _, name := range []string{"get_artifact_chunk", "search_artifact_content", "get_json_artifact"} {
		if !toolDefsContain(defaultDefs, name) {
			t.Fatalf("default definitions missing %s", name)
		}
	}
	if toolDefsContain(defaultDefs, "search_docs") {
		t.Fatalf("default definitions should not advertise legacy search_docs alias")
	}
	threadSearchDefs := reg.ThreadSearchToolDefinitions(true)
	if !toolDefsContain(threadSearchDefs, "search_threads") {
		t.Fatalf("thread search definitions should include search_threads")
	}
	if defs := reg.ThreadSearchToolDefinitions(false); len(defs) != 0 {
		t.Fatalf("disabled thread search definitions = %#v, want none", defs)
	}

	searchDefs := reg.DefinitionsForSearch("shallow")
	if !toolDefsContain(searchDefs, "search_threads") {
		t.Fatalf("search definitions should include search_threads, got %#v", searchDefs)
	}
	if toolDefsContain(searchDefs, "search_docs") {
		t.Fatalf("search definitions should not advertise legacy search_docs alias")
	}
	if !reg.AllowedForSearch("search_threads", "shallow") || !reg.AllowedForSearch("search_docs", "shallow") {
		t.Fatalf("search_threads and search_docs should both be allowed in search mode")
	}
	for _, name := range []string{"get_artifact_chunk", "search_artifact_content", "get_json_artifact"} {
		if !reg.AllowedForSearch(name, "shallow") {
			t.Fatalf("%s should be allowed in shallow search mode", name)
		}
	}
}

func toolDefsContain(defs []types.ToolDefinition, name string) bool {
	for _, def := range defs {
		if strings.TrimSpace(def.Function.Name) == name {
			return true
		}
	}
	return false
}
