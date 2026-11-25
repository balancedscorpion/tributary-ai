package optionc

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

// The async query lifecycle persists a placeholder artifact under the
// deterministic artifact id at submit time; when get_query_results lands the
// completed dataset arrives under the SAME id. The insert conflicts, and the
// conflict handler must upgrade the stored record — otherwise
// /api/artifacts/:id serves the stale placeholder forever and the results
// explorer can never hydrate from storage after a missed SSE envelope or a
// reload.
func TestUpgradeQueryPlaceholderArtifactReplacesPlaceholder(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	m := &Manager{store: st}

	empty := ""
	runTool := "run_bigquery_query"
	okVal := true
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "artifact_async_abc",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Running",
		ToolName:    &runTool,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &empty,
		Metadata:    map[string]any{"placeholder": true, "async_query": true, "job_id": "job-1"},
	}); err != nil {
		t.Fatalf("InsertArtifact placeholder: %v", err)
	}

	rows := `[{"a":1}]`
	resultsTool := "get_query_results"
	incoming := store.Artifact{
		ID:          "artifact_async_abc",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Results",
		ToolName:    &resultsTool,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &rows,
		ByteSize:    int64(len(rows)),
		Metadata:    map[string]any{"tool_name": "get_query_results", "ok": true, "row_count": 1},
	}
	if !m.upgradeQueryPlaceholderArtifact(ctx, "run-1", incoming) {
		t.Fatalf("expected placeholder to be upgraded with completed results")
	}
	got, err := st.GetArtifact(ctx, "artifact_async_abc")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.ContentText == nil || *got.ContentText != rows {
		t.Fatalf("stored artifact still holds placeholder content: %#v", got.ContentText)
	}
	if got.ToolName == nil || *got.ToolName != "get_query_results" {
		t.Fatalf("stored artifact tool name not upgraded: %#v", got.ToolName)
	}

	// A second results insert for the same id (e.g. a paginated
	// get_query_results call) must NOT clobber the stored first page.
	page2 := `[{"a":2}]`
	secondPage := incoming
	secondPage.ContentText = &page2
	if m.upgradeQueryPlaceholderArtifact(ctx, "run-1", secondPage) {
		t.Fatalf("results artifact should not be replaced by a later results insert")
	}

	// Failed results envelopes never upgrade the placeholder.
	notOK := false
	failed := incoming
	failed.OK = &notOK
	if m.upgradeQueryPlaceholderArtifact(ctx, "run-1", failed) {
		t.Fatalf("failed results envelope should not upgrade anything")
	}
}

// Upgrade acceptance keys off the shared async results-tool predicate
// (isAsyncQueryResultsTool), not a hard-coded tool name: alias results tools
// upgrade placeholders too, and a stored results dataset under ANY results
// tool name blocks later replacements so the first page is never clobbered,
// even across alias names.
func TestUpgradeQueryPlaceholderArtifactAcceptsResultsToolAliases(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	m := &Manager{store: st}

	empty := ""
	runTool := "run_snowflake_query"
	okVal := true
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "art-alias",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Running",
		ToolName:    &runTool,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &empty,
		Metadata:    map[string]any{"placeholder": true, "async_query": true, "job_id": "job-2"},
	}); err != nil {
		t.Fatalf("InsertArtifact placeholder: %v", err)
	}

	rows := `[{"a":1}]`
	aliasTool := "fetch_query_results"
	incoming := store.Artifact{
		ID:          "art-alias",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Query Results",
		ToolName:    &aliasTool,
		OK:          &okVal,
		Storage:     "mysql",
		ContentText: &rows,
		ByteSize:    int64(len(rows)),
		Metadata:    map[string]any{"tool_name": aliasTool, "ok": true, "row_count": 1},
	}
	if !m.upgradeQueryPlaceholderArtifact(ctx, "run-1", incoming) {
		t.Fatalf("alias results tool should upgrade the placeholder")
	}

	page2 := `[{"a":2}]`
	canonical := "get_query_results"
	later := incoming
	later.ToolName = &canonical
	later.ContentText = &page2
	if m.upgradeQueryPlaceholderArtifact(ctx, "run-1", later) {
		t.Fatalf("stored results dataset must not be replaced across alias tool names")
	}
	got, err := st.GetArtifact(ctx, "art-alias")
	if err != nil {
		t.Fatalf("GetArtifact: %v", err)
	}
	if got.ContentText == nil || *got.ContentText != rows {
		t.Fatalf("stored first page was clobbered: %#v", got.ContentText)
	}
}

// End-to-end through persistToolResult: the submit-time placeholder and the
// completed get_query_results dataset must land under the SAME deterministic
// artifact id (query_metadata.artifact_id), the conflict upgrades the stored
// placeholder in place, and a later paginated call keeps the first page.
func TestPersistToolResultReplacesPlaceholderForGetQueryResults(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewJSON(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	m := &Manager{store: st}

	const artifactID = "art-0123456789abcdef01234567"
	queryMeta := func(extra map[string]any) map[string]any {
		meta := map[string]any{
			"artifact_id":      artifactID,
			"full_artifact_id": artifactID,
			"job_id":           "job-1",
			"thread_id":        thread.ID,
			"owner_token":      "tok",
			"provider":         "bigquery",
		}
		for key, value := range extra {
			meta[key] = value
		}
		return meta
	}

	// Submit: the async run tool persists the running placeholder.
	m.persistToolResult(ctx, thread, store.Run{}, types.ToolCall{ID: "call-1", Name: "run_bigquery_query"}, tools.Output{
		OK:            true,
		ResultFormat:  tools.ResultFormatJSON,
		Result:        map[string]any{"status": "running", "job_id": "job-1"},
		QueryMetadata: queryMeta(map[string]any{"status": "running"}),
	}, RunContextPlan{})

	placeholder, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("placeholder artifact not stored under lifecycle id: %v", err)
	}
	if placeholder.Metadata["placeholder"] != true {
		t.Fatalf("submit-time artifact should be a placeholder: %#v", placeholder.Metadata)
	}

	// Completion: get_query_results page 1 resolves to the same id and
	// upgrades the placeholder.
	resultRows := []map[string]any{{"a": 1.0}}
	resultMeta := map[string]any{
		"provider":           "query-lifecycle",
		"product":            "query",
		"family":             "query-lifecycle",
		"integration":        "query-lifecycle",
		"tool_name":          "get_query_results",
		"kind":               "query_results",
		"result_kind":        "query",
		"renderer_strategy":  "artifact_type",
		"renderer_key":       "query",
		"native_viewer":      "query",
		"content_kind":       "json_rows",
		"full_artifact_id":   artifactID,
		"row_count_total":    1,
		"rows_returned":      1,
		"rows_preview_count": 1,
		"generated_by":       "get_query_results",
		"retrieval":          tools.LargeArtifactRetrievalHints(artifactID, "json_rows"),
		"tool_presentation":  queryLifecyclePresentationForTest(),
	}
	resultArtifact, err := tools.NewLargeRowsArtifactPayload(artifactID, "Query Results", resultRows, "get_query_results", queryMeta(map[string]any{
		"status":          "completed",
		"rows_returned":   1,
		"row_count_total": 1,
		"content_kind":    "json_rows",
		"generated_by":    "get_query_results",
		"result_metadata": resultMeta,
	}))
	if err != nil {
		t.Fatalf("NewLargeRowsArtifactPayload: %v", err)
	}
	resultsMsg := m.persistToolResult(ctx, thread, store.Run{}, types.ToolCall{ID: "call-2", Name: "get_query_results"}, tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows":               []any{map[string]any{"a": 1.0}},
			"page_size":          1,
			"offset":             0,
			"has_more":           true,
			"full_artifact_id":   artifactID,
			"row_count_total":    1,
			"rows_preview_count": 1,
			"retrieval":          tools.LargeArtifactRetrievalHints(artifactID, "json_rows"),
		},
		QueryMetadata:  queryMeta(map[string]any{"status": "completed", "rows_returned": 1, "row_count_total": 1, "has_more": true}),
		ResultMetadata: resultMeta,
		Artifact:       resultArtifact,
	}, RunContextPlan{})
	if resultsMsg.Content == nil {
		t.Fatalf("results message has no content")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*resultsMsg.Content), &env); err != nil {
		t.Fatalf("unmarshal results envelope: %v", err)
	}
	if env["artifact_id"] != artifactID {
		t.Fatalf("results envelope artifact_id = %v, want %s", env["artifact_id"], artifactID)
	}

	upgraded, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact after results: %v", err)
	}
	if upgraded.ToolName == nil || *upgraded.ToolName != "get_query_results" {
		t.Fatalf("artifact not upgraded with results: tool=%#v", upgraded.ToolName)
	}
	if upgraded.ContentText == nil || !strings.Contains(*upgraded.ContentText, `"a":1`) {
		t.Fatalf("upgraded artifact missing first-page rows: %#v", upgraded.ContentText)
	}
	if upgraded.Metadata["placeholder"] == true || upgraded.Metadata["async_query"] == true {
		t.Fatalf("upgraded artifact kept placeholder flags: %#v", upgraded.Metadata)
	}
	requireArtifactMetadataValue(t, upgraded.Metadata, "full_artifact_id", artifactID)
	requireArtifactMetadataValue(t, upgraded.Metadata, "row_count_total", 1)
	requireArtifactMetadataValue(t, upgraded.Metadata, "generated_by", "get_query_results")
	requireArtifactMetadataValue(t, upgraded.Metadata, "content_kind", "json_rows")
	queryMetadata := requireArtifactMetadataMap(t, upgraded.Metadata, "query_metadata")
	requireArtifactMetadataValue(t, queryMetadata, "job_id", "job-1")
	requireArtifactMetadataValue(t, queryMetadata, "artifact_id", artifactID)
	requireArtifactMetadataValue(t, queryMetadata, "full_artifact_id", artifactID)
	requireArtifactMetadataValue(t, queryMetadata, "status", "completed")
	persistedResultMetadata := requireArtifactMetadataMap(t, upgraded.Metadata, "result_metadata")
	requireArtifactMetadataValue(t, persistedResultMetadata, "integration", "query-lifecycle")
	requireArtifactMetadataValue(t, persistedResultMetadata, "renderer_key", "query")
	requireArtifactMetadataValue(t, persistedResultMetadata, "native_viewer", "query")
	presentation := requireArtifactMetadataMap(t, persistedResultMetadata, "tool_presentation")
	requireArtifactMetadataValue(t, presentation, "family", "query-lifecycle")
	requireArtifactMetadataValue(t, presentation, "renderer_key", "query")
	requireArtifactMetadataValue(t, presentation, "native_viewer", "query")
	retrieval := requireArtifactMetadataMap(t, upgraded.Metadata, "retrieval")
	requireArtifactMetadataValue(t, retrieval, "artifact_id", artifactID)

	// Pagination: page 2 resolves to the same id but must not clobber the
	// stored first page.
	m.persistToolResult(ctx, thread, store.Run{}, types.ToolCall{ID: "call-3", Name: "get_query_results"}, tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"rows":      []any{map[string]any{"a": 2.0}},
			"page_size": 1,
			"offset":    1,
			"has_more":  false,
		},
		QueryMetadata: queryMeta(map[string]any{"rows_returned": 1, "offset": 1}),
	}, RunContextPlan{})

	final, err := st.GetArtifact(ctx, artifactID)
	if err != nil {
		t.Fatalf("GetArtifact after page 2: %v", err)
	}
	if final.ContentText == nil || !strings.Contains(*final.ContentText, `"a":1`) {
		t.Fatalf("first page was clobbered by a paginated call: %#v", final.ContentText)
	}
	if strings.Contains(*final.ContentText, `"a":2`) {
		t.Fatalf("paginated rows must not replace the stored first page: %#v", final.ContentText)
	}
}

func queryLifecyclePresentationForTest() map[string]any {
	return map[string]any{
		"family":            "query-lifecycle",
		"result_kind":       "query",
		"renderer_strategy": "artifact_type",
		"renderer_key":      "query",
		"native_viewer":     "query",
		"artifact_types":    []string{"data"},
		"source_refs":       []string{"remote_id"},
		"native_links":      []string{"warehouse_job"},
	}
}

func requireArtifactMetadataMap(t *testing.T, metadata map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := metadata[key].(map[string]any)
	if !ok {
		t.Fatalf("metadata[%q]=%T, want object: %#v", key, metadata[key], metadata)
	}
	return value
}

func requireArtifactMetadataValue(t *testing.T, metadata map[string]any, key string, want any) {
	t.Helper()
	if got := metadata[key]; !artifactMetadataValueEqual(got, want) {
		t.Fatalf("metadata[%q]=%#v, want %#v; metadata=%#v", key, got, want, metadata)
	}
}

func artifactMetadataValueEqual(got any, want any) bool {
	if got == want {
		return true
	}
	gotNumber, gotOK := artifactMetadataNumber(got)
	wantNumber, wantOK := artifactMetadataNumber(want)
	return gotOK && wantOK && gotNumber == wantNumber
}

func artifactMetadataNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int8:
		return float64(typed), true
	case int16:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint8:
		return float64(typed), true
	case uint16:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	default:
		return 0, false
	}
}

// buildArtifact must persist async query handles as explicit placeholder
// records (placeholder/async_query metadata + job identity) so clients and
// the conflict-upgrade path can recognise them.
func TestBuildArtifactStampsAsyncQueryPlaceholderMetadata(t *testing.T) {
	out := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"status":      "running",
			"job_id":      "job-9",
			"provider":    "bigquery",
			"owner_token": "tok",
		},
		QueryMetadata: map[string]any{
			"job_id":      "job-9",
			"thread_id":   "thread-1",
			"owner_token": "tok",
			"provider":    "bigquery",
			"status":      "running",
			"description": "count users",
		},
	}
	typ, title, _, content, metadata := buildArtifact("run_bigquery_query", out, `{"status":"running"}`)
	if typ != "data" {
		t.Fatalf("type = %q, want data", typ)
	}
	if title != "Query Running" {
		t.Fatalf("title = %q, want Query Running", title)
	}
	if content != "" {
		t.Fatalf("placeholder content should be empty, got %q", content)
	}
	if metadata["placeholder"] != true || metadata["async_query"] != true {
		t.Fatalf("placeholder flags missing: %#v", metadata)
	}
	if metadata["job_id"] != "job-9" || metadata["thread_id"] != "thread-1" || metadata["owner_token"] != "tok" {
		t.Fatalf("job identity missing from metadata: %#v", metadata)
	}

	// Completed get_query_results outputs keep producing dataset artifacts.
	resultsOut := tools.Output{
		OK:           true,
		ResultFormat: tools.ResultFormatJSON,
		Result:       map[string]any{"rows": []any{map[string]any{"a": 1.0}}},
	}
	rTyp, _, _, rContent, rMeta := buildArtifact("get_query_results", resultsOut, "")
	if rTyp != "data" {
		t.Fatalf("results type = %q, want data", rTyp)
	}
	if rContent == "" {
		t.Fatalf("results content should hold the dataset rows")
	}
	if rMeta["placeholder"] == true {
		t.Fatalf("results artifact must not be a placeholder: %#v", rMeta)
	}
}
