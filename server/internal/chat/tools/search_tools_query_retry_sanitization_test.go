package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	retrievalCurrentQOB = "qob-11111111111111111111111111111111"
	retrievalStaleQOB   = "qob-22222222222222222222222222222222"
)

func TestSanitizeRetrievedQueryRetryValueScopesLegacyFieldsToLifecyclePayloads(t *testing.T) {
	business := map[string]any{
		"query_obligation_id":                   retrievalStaleQOB,
		"replacement_job_id":                    "business-123",
		"target_job_id":                         "customer-target",
		"job_id":                                "qj-monitor-me",
		"owner_token":                           "tok_monitor",
		"thread_id":                             "thread-observation",
		retrievalStaleQOB:                       "authority-in-key",
		"prefix" + retrievalStaleQOB + "suffix": "embedded-authority-in-key",
	}

	plain := sanitizeRetrievedQueryRetryValue(business, false, false).(map[string]any)
	if _, exists := plain["query_obligation_id"]; exists {
		t.Fatalf("stale query_obligation_id survived: %#v", plain)
	}
	if _, exists := plain[retrievalStaleQOB]; exists {
		t.Fatalf("stale qob object key survived: %#v", plain)
	}
	if _, exists := plain["prefix"+retrievalStaleQOB+"suffix"]; exists {
		t.Fatalf("embedded stale qob object key survived: %#v", plain)
	}
	for key, want := range map[string]any{
		"replacement_job_id": "business-123",
		"target_job_id":      "customer-target",
		"job_id":             "qj-monitor-me",
		"owner_token":        "tok_monitor",
		"thread_id":          "thread-observation",
	} {
		if got := plain[key]; got != want {
			t.Fatalf("plain[%s]=%#v, want %#v; payload=%#v", key, got, want, plain)
		}
	}

	lifecycle := sanitizeRetrievedQueryRetryValue(business, false, true).(map[string]any)
	for _, key := range []string{"query_obligation_id", "replacement_job_id", "target_job_id"} {
		if _, exists := lifecycle[key]; exists {
			t.Fatalf("lifecycle retry identity %q survived: %#v", key, lifecycle)
		}
	}
	for _, key := range []string{"job_id", "owner_token", "thread_id"} {
		if lifecycle[key] != business[key] {
			t.Fatalf("observation handle %q was removed: %#v", key, lifecycle)
		}
	}

	current := sanitizeRetrievedQueryRetryValue(business, true, true).(map[string]any)
	if got := current["query_obligation_id"]; got != retrievalStaleQOB {
		t.Fatalf("same-run canonical qob=%#v, want %q; payload=%#v", got, retrievalStaleQOB, current)
	}
	if _, exists := current["replacement_job_id"]; exists {
		t.Fatalf("legacy replacement id survived same-run sanitization: %#v", current)
	}
	if pasted := sanitizeRetrievedQueryRetryText("prefix"+retrievalCurrentQOB+"suffix", true, false); strings.Contains(pasted, retrievalCurrentQOB) {
		t.Fatalf("same-run arbitrary text preserved a non-canonical qob: %q", pasted)
	}
}

func TestSanitizeRetrievedQueryRetryTextRemovesHistoricalReplacementOwnershipError(t *testing.T) {
	raw := "The durable attempt failed: replacement target [qj-old] is not owned by the current run. " +
		"Business note: replacement target [business-123] remains under review."
	clean := sanitizeRetrievedQueryRetryText(raw, false, true)
	if strings.Contains(clean, "qj-old") || strings.Contains(clean, "is not owned by the current run") {
		t.Fatalf("historical replacement ownership target survived: %q", clean)
	}
	if !strings.Contains(clean, "replacement target [business-123] remains under review") {
		t.Fatalf("unrelated business replacement text was redacted: %q", clean)
	}
	plain := sanitizeRetrievedQueryRetryText(raw, false, false)
	if !strings.Contains(plain, "replacement target [qj-old] is not owned by the current run") {
		t.Fatalf("non-lifecycle text was unexpectedly redacted: %q", plain)
	}
}

func TestListMessagesSanitizesRetryAuthorityBySourceRun(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	staleRun, err := st.CreateRun(ctx, thread.ID, "openai", "gpt", nil, "sys", 8)
	if err != nil {
		t.Fatalf("CreateRun(stale): %v", err)
	}
	currentRun, err := st.CreateRun(ctx, thread.ID, "openai", "gpt", nil, "sys", 8)
	if err != nil {
		t.Fatalf("CreateRun(current): %v", err)
	}
	toolName := "run_databricks_query"
	appendLifecycleMessage := func(runID, qob string) {
		t.Helper()
		content := fmt.Sprintf(`{"query_metadata":{"query_obligation_id":%q,"queryObligationId":%q,"replaces_job_id":"qj-old","job_id":"qj-monitor","owner_token":"tok-monitor"},"instructions":"retry query_obligation_id=%s; replaces_job_id=qj-old"}`, qob, qob, qob)
		if _, appendErr := st.AppendMessage(ctx, thread.ID, &runID, store.Message{
			Role:    types.RoleTool,
			Content: &content,
			Name:    &toolName,
		}); appendErr != nil {
			t.Fatalf("AppendMessage(%s): %v", runID, appendErr)
		}
	}
	appendLifecycleMessage(staleRun.ID, retrievalStaleQOB)
	appendLifecycleMessage(currentRun.ID, retrievalCurrentQOB)

	out, err := newListMessagesTool(st).Execute(ctx, ExecContext{RunID: currentRun.ID}, mustJSONRaw(t, map[string]any{
		"thread_id": thread.ID,
		"order_by":  "seq_asc",
		"limit":     10,
	}))
	if err != nil || !out.OK {
		t.Fatalf("list_messages err=%v output=%#v", err, out.Result)
	}
	result := out.Result.(map[string]any)
	rows := result["messages"].([]any)
	if len(rows) != 2 {
		t.Fatalf("messages=%d, want 2: %#v", len(rows), rows)
	}
	for _, raw := range rows {
		row := raw.(map[string]any)
		runID := strings.TrimSpace(fmt.Sprint(row["run_id"]))
		var payload map[string]any
		if err := json.Unmarshal([]byte(row["content"].(string)), &payload); err != nil {
			t.Fatalf("decode row %s: %v; content=%q", runID, err, row["content"])
		}
		queryMetadata := payload["query_metadata"].(map[string]any)
		if queryMetadata["job_id"] != "qj-monitor" || queryMetadata["owner_token"] != "tok-monitor" {
			t.Fatalf("row %s lost polling handles: %#v", runID, queryMetadata)
		}
		for _, key := range []string{"queryObligationId", "replaces_job_id"} {
			if _, exists := queryMetadata[key]; exists {
				t.Fatalf("row %s retained legacy identity %q: %#v", runID, key, queryMetadata)
			}
		}
		content := row["content"].(string)
		if runID == currentRun.ID {
			if queryMetadata["query_obligation_id"] != retrievalCurrentQOB || !strings.Contains(content, retrievalCurrentQOB) {
				t.Fatalf("current-run qob was not preserved: %s", content)
			}
			if instructions := strings.TrimSpace(fmt.Sprint(payload["instructions"])); strings.Contains(instructions, retrievalCurrentQOB) {
				t.Fatalf("current-run arbitrary instruction text retained qob: %q", instructions)
			}
		} else {
			if _, exists := queryMetadata["query_obligation_id"]; exists || strings.Contains(content, retrievalStaleQOB) {
				t.Fatalf("stale-run qob survived retrieval: %s", content)
			}
		}
		if strings.Contains(content, "replaces_job_id") {
			t.Fatalf("legacy labelled replacement target survived: %s", content)
		}
	}
}

func TestGetArtifactAlwaysStripsRetryAuthorityButKeepsPollingHandles(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := "get_query_results"
	content := fmt.Sprintf(`{"query_metadata":{"query_obligation_id":%q,"replaces_job_id":"qj-old","job_id":"qj-monitor","owner_token":"tok-monitor"}}`, retrievalCurrentQOB)
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query lifecycle artifact",
		ToolName:    &toolName,
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		Metadata: map[string]any{
			"query_metadata": map[string]any{
				"query_obligation_id": retrievalCurrentQOB,
				"replaces_job_id":     "qj-old",
				"job_id":              "qj-monitor",
				"owner_token":         "tok-monitor",
			},
		},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	out, err := newGetArtifactTool(st, t.TempDir()).Execute(ctx, ExecContext{RunID: "same-run-does-not-authorize-artifact"}, mustJSONRaw(t, map[string]any{
		"artifact_id": artifact.ID,
	}))
	if err != nil || !out.OK {
		t.Fatalf("get_artifact err=%v output=%#v", err, out.Result)
	}
	result := out.Result.(map[string]any)
	metadata := result["metadata"].(map[string]any)["query_metadata"].(map[string]any)
	for _, key := range []string{"query_obligation_id", "replaces_job_id"} {
		if _, exists := metadata[key]; exists {
			t.Fatalf("artifact metadata retained %q: %#v", key, metadata)
		}
	}
	if metadata["job_id"] != "qj-monitor" || metadata["owner_token"] != "tok-monitor" {
		t.Fatalf("artifact metadata lost polling handles: %#v", metadata)
	}
	var contentResult map[string]any
	if err := json.Unmarshal([]byte(result["content"].(string)), &contentResult); err != nil {
		t.Fatalf("decode artifact content: %v; content=%q", err, result["content"])
	}
	contentMetadata := contentResult["query_metadata"].(map[string]any)
	if _, exists := contentMetadata["query_obligation_id"]; exists {
		t.Fatalf("artifact content retained qob: %#v", contentMetadata)
	}
	if _, exists := contentMetadata["replaces_job_id"]; exists {
		t.Fatalf("artifact content retained replacement target: %#v", contentMetadata)
	}
	if contentMetadata["job_id"] != "qj-monitor" || contentMetadata["owner_token"] != "tok-monitor" {
		t.Fatalf("artifact content lost polling handles: %#v", contentMetadata)
	}
}

func TestGetArtifactRedactsQueryObligationAcrossPageBoundary(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	content := "prefix-" + retrievalStaleQOB + "-suffix"
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Paged artifact",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	tool := newGetArtifactTool(st, t.TempDir())
	var rebuilt strings.Builder
	pageToken := ""
	for page := 0; page < 10; page++ {
		args := map[string]any{"artifact_id": artifact.ID, "max_bytes": 20}
		if pageToken != "" {
			args["page_token"] = pageToken
		}
		out, execErr := tool.Execute(ctx, ExecContext{RunID: "another-run"}, mustJSONRaw(t, args))
		if execErr != nil || !out.OK {
			t.Fatalf("page %d err=%v output=%#v", page, execErr, out.Result)
		}
		result := out.Result.(map[string]any)
		chunk := result["content"].(string)
		if strings.Contains(chunk, retrievalStaleQOB) {
			t.Fatalf("page %d leaked complete qob: %q", page, chunk)
		}
		rebuilt.WriteString(chunk)
		hasMore, _ := result["has_more"].(bool)
		if !hasMore {
			break
		}
		pageToken = strings.TrimSpace(fmt.Sprint(result["next_page_token"]))
		if pageToken == "" {
			t.Fatalf("page %d has_more without next_page_token: %#v", page, result)
		}
	}
	if strings.Contains(rebuilt.String(), retrievalStaleQOB) {
		t.Fatalf("adjacent pages reconstruct stale qob: %q", rebuilt.String())
	}
	if !strings.Contains(rebuilt.String(), strings.Repeat("*", len(retrievalStaleQOB))) {
		t.Fatalf("rebuilt content lacks length-preserving redaction: %q", rebuilt.String())
	}
}

func TestGetJSONArtifactCannotRecoverStaleQueryRetryAuthority(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	toolName := "get_query_results"
	content := fmt.Sprintf(`{"query_metadata":{"query_obligation_id":%q,"replaces_job_id":"qj-old","job_id":"qj-monitor","owner_token":"tok-monitor"}}`, retrievalStaleQOB)
	artifact, err := st.InsertArtifact(ctx, store.Artifact{
		ThreadID: thread.ID, Type: "data", Title: "JSON lifecycle artifact", ToolName: &toolName,
		Storage: "mysql", ContentText: &content, ByteSize: int64(len(content)),
		Metadata: map[string]any{"query_obligation_id": retrievalStaleQOB, "replaces_job_id": "qj-old", "job_id": "qj-monitor"},
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	out, err := newGetJSONArtifactTool(st, t.TempDir()).Execute(ctx, ExecContext{RunID: "new-run"}, mustJSONRaw(t, map[string]any{
		"artifact_id": artifact.ID,
		"path":        "query_metadata",
	}))
	if err != nil || !out.OK {
		t.Fatalf("get_json_artifact err=%v output=%#v", err, out.Result)
	}
	result := out.Result.(map[string]any)
	value := result["value"].(map[string]any)
	for _, key := range []string{"query_obligation_id", "replaces_job_id"} {
		if _, exists := value[key]; exists {
			t.Fatalf("get_json_artifact recovered %q: %#v", key, result)
		}
	}
	if value["job_id"] != "qj-monitor" || value["owner_token"] != "tok-monitor" {
		t.Fatalf("get_json_artifact lost polling handles: %#v", value)
	}
	descriptorMetadata := result["metadata"].(map[string]any)
	if _, exists := descriptorMetadata["query_obligation_id"]; exists {
		t.Fatalf("descriptor metadata recovered qob: %#v", descriptorMetadata)
	}
}
