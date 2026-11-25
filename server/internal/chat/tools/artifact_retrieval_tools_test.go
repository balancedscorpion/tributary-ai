package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
	"ai-analyst-local/internal/chat/store"
)

func setupArtifactRetrievalToolTest(t *testing.T, content string, typ string) (context.Context, store.Store, string) {
	t.Helper()
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if typ == "" {
		typ = "data"
	}
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "retrieval-artifact",
		ThreadID:    thread.ID,
		Type:        typ,
		Title:       "Retrieval Artifact",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	return ctx, st, cfgDir
}

func requireArtifactRetrievalResultMetadata(t *testing.T, out Output, toolName, kind, artifactType string) map[string]any {
	t.Helper()
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("%s result=%T, want map[string]any", toolName, out.Result)
	}
	if _, nested := result["result_metadata"]; nested {
		t.Fatalf("%s result should not contain nested result_metadata: %#v", toolName, result)
	}
	meta := out.ResultMetadata
	if meta == nil {
		t.Fatalf("%s ResultMetadata is nil", toolName)
	}
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("ToolMetadataByName(%q) not found", toolName)
	}
	required := map[string]any{
		"provider":          "artifact",
		"product":           toolMeta.Presentation.Family,
		"family":            toolMeta.Presentation.Family,
		"tool_name":         toolMeta.Name,
		"tool_set_id":       toolMeta.ToolSetID,
		"tool_set_name":     toolMeta.ToolSetName,
		"category":          string(toolMeta.Category),
		"origin":            string(toolMeta.Origin),
		"kind":              kind,
		"result_kind":       toolMeta.Presentation.ResultKind,
		"native_viewer":     toolMeta.Presentation.NativeViewer,
		"renderer_strategy": toolMeta.Presentation.RendererStrategy,
		"renderer_key":      toolMeta.Presentation.RendererKey,
		"readonly":          true,
		"artifact_id":       "retrieval-artifact",
		"title":             "Retrieval Artifact",
		"type":              artifactType,
		"storage":           "mysql",
	}
	for key, want := range required {
		if got := meta[key]; got != want {
			t.Fatalf("%s ResultMetadata[%s]=%#v, want %#v; metadata=%#v", toolName, key, got, want, meta)
		}
	}
	presentation, ok := meta["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("%s ResultMetadata.tool_presentation=%T, want map[string]any; metadata=%#v", toolName, meta["tool_presentation"], meta)
	}
	if got := strings.TrimSpace(fmt.Sprint(presentation["native_viewer"])); got != toolMeta.Presentation.NativeViewer {
		t.Fatalf("%s tool_presentation.native_viewer=%q, want %q; presentation=%#v", toolName, got, toolMeta.Presentation.NativeViewer, presentation)
	}
	if got := strings.TrimSpace(fmt.Sprint(presentation["renderer_strategy"])); got != toolMeta.Presentation.RendererStrategy {
		t.Fatalf("%s tool_presentation.renderer_strategy=%q, want %q; presentation=%#v", toolName, got, toolMeta.Presentation.RendererStrategy, presentation)
	}
	if got := strings.TrimSpace(fmt.Sprint(presentation["renderer_key"])); got != toolMeta.Presentation.RendererKey {
		t.Fatalf("%s tool_presentation.renderer_key=%q, want %q; presentation=%#v", toolName, got, toolMeta.Presentation.RendererKey, presentation)
	}
	return meta
}

func TestGetArtifactToolPagesContent(t *testing.T) {
	ctx, st, cfgDir := setupArtifactRetrievalToolTest(t, "abcdefghij", "data")
	tool := newGetArtifactTool(st, cfgDir)

	out, err := tool.Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
		"max_bytes":   4,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK, got %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	if result["content"] != "abcd" {
		t.Fatalf("content=%#v, want abcd", result["content"])
	}
	if result["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", result["has_more"])
	}

	nextOut, err := tool.Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
		"max_bytes":   4,
		"page_token":  result["next_page_token"],
	}))
	if err != nil {
		t.Fatalf("Execute page 2: %v", err)
	}
	next := nextOut.Result.(map[string]any)
	if next["content"] != "efgh" {
		t.Fatalf("page 2 content=%#v, want efgh", next["content"])
	}
	if got := intFromAnyDataTool(next["offset"]); got != 4 {
		t.Fatalf("page 2 offset=%d, want 4", got)
	}
}

func TestGetArtifactChunkToolPagesContentWithFirstClassResultMetadata(t *testing.T) {
	ctx, st, cfgDir := setupArtifactRetrievalToolTest(t, "abcdefghij", "data")
	tool := newGetArtifactChunkTool(st, cfgDir)

	out, err := tool.Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
		"max_bytes":   4,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK, got %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	if result["content"] != "abcd" {
		t.Fatalf("content=%#v, want abcd", result["content"])
	}
	if result["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", result["has_more"])
	}
	meta := requireArtifactRetrievalResultMetadata(t, out, "get_artifact_chunk", "artifact_chunk", "data")
	if meta["offset"] != int64(0) || meta["count"] != 4 || meta["max_bytes"] != int64(4) || meta["truncation"] != true {
		t.Fatalf("chunk metadata=%#v, want offset/count/max_bytes/truncation", meta)
	}

	nextOut, err := tool.Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
		"max_bytes":   4,
		"page_token":  result["next_page_token"],
	}))
	if err != nil {
		t.Fatalf("Execute page 2: %v", err)
	}
	next := nextOut.Result.(map[string]any)
	if next["content"] != "efgh" {
		t.Fatalf("page 2 content=%#v, want efgh", next["content"])
	}
	if got := intFromAnyDataTool(next["offset"]); got != 4 {
		t.Fatalf("page 2 offset=%d, want 4", got)
	}
	nextMeta := requireArtifactRetrievalResultMetadata(t, nextOut, "get_artifact_chunk", "artifact_chunk", "data")
	if nextMeta["offset"] != int64(4) || nextMeta["count"] != 4 {
		t.Fatalf("page 2 metadata=%#v, want offset/count", nextMeta)
	}
}

func TestGetArtifactChunkImageOmittedUsesFirstClassResultMetadata(t *testing.T) {
	ctx, st, cfgDir := setupArtifactRetrievalToolTest(t, "data:image/png;base64,abc123", "image")
	out, err := newGetArtifactChunkTool(st, cfgDir).Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK, got %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	if result["image_content_omitted"] != true || result["content"] != "" {
		t.Fatalf("image omitted result=%#v, want omitted empty content", result)
	}
	meta := requireArtifactRetrievalResultMetadata(t, out, "get_artifact_chunk", "artifact_chunk", "image")
	if meta["image_content_omitted"] != true || meta["count"] != 0 || meta["content_size"] != 0 {
		t.Fatalf("image omitted metadata=%#v, want omitted zero-count metadata", meta)
	}
}

func TestArtifactRetrievalToolsSearchAndReadJSON(t *testing.T) {
	rows := []map[string]any{
		{"id": 1, "name": "Alpha", "segment": "a"},
		{"id": 2, "name": "Beta", "segment": "b"},
		{"id": 3, "name": "Alpine", "segment": "a"},
	}
	blob, err := json.Marshal(map[string]any{"rows": rows})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	ctx, st, cfgDir := setupArtifactRetrievalToolTest(t, string(blob), "data")

	searchOut, err := newSearchArtifactContentTool(st, cfgDir).Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
		"query":       "alp",
		"limit":       1,
	}))
	if err != nil {
		t.Fatalf("search Execute: %v", err)
	}
	if !searchOut.OK {
		t.Fatalf("expected search OK, got %s", toolErrorMessage(t, searchOut))
	}
	searchResult := searchOut.Result.(map[string]any)
	matches := searchResult["matches"].([]map[string]any)
	if len(matches) != 1 || !strings.Contains(strings.ToLower(matches[0]["snippet"].(string)), "alpha") {
		t.Fatalf("matches=%#v, want alpha snippet", matches)
	}
	searchMeta := requireArtifactRetrievalResultMetadata(t, searchOut, "search_artifact_content", "artifact_search", "data")
	if searchMeta["query"] != "alp" || searchMeta["count"] != 1 || searchMeta["offset"] != int64(0) || searchMeta["truncation"] != false {
		t.Fatalf("search metadata=%#v, want query/count/offset/truncation", searchMeta)
	}

	jsonOut, err := newGetJSONArtifactTool(st, cfgDir).Execute(ctx, ExecContext{}, mustJSONRaw(t, map[string]any{
		"artifact_id": "retrieval-artifact",
		"path":        "rows",
		"limit":       2,
		"columns":     []string{"name"},
	}))
	if err != nil {
		t.Fatalf("json Execute: %v", err)
	}
	if !jsonOut.OK {
		t.Fatalf("expected JSON OK, got %s", toolErrorMessage(t, jsonOut))
	}
	jsonResult := jsonOut.Result.(map[string]any)
	if jsonResult["kind"] != "array" {
		t.Fatalf("kind=%#v, want array", jsonResult["kind"])
	}
	if jsonResult["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", jsonResult["has_more"])
	}
	items := jsonResult["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items=%d, want 2", len(items))
	}
	first := items[0].(map[string]any)
	if first["name"] != "Alpha" {
		t.Fatalf("first item=%#v, want Alpha", first)
	}
	if _, ok := first["segment"]; ok {
		t.Fatalf("projected item unexpectedly includes segment: %#v", first)
	}
	jsonMeta := requireArtifactRetrievalResultMetadata(t, jsonOut, "get_json_artifact", "json_artifact", "data")
	if jsonMeta["path"] != "rows" || jsonMeta["count"] != 2 || jsonMeta["selection_kind"] != "array" || jsonMeta["has_more"] != true || jsonMeta["truncation"] != true {
		t.Fatalf("json metadata=%#v, want paged JSON selection metadata", jsonMeta)
	}
}

func TestSearchFilesDefaultsToPagedLimit(t *testing.T) {
	ctx := context.Background()
	cfgDir := t.TempDir()
	ps, err := projects.NewStore(cfgDir)
	if err != nil {
		t.Fatalf("projects.NewStore: %v", err)
	}
	projectID := "project-search-default"
	var body strings.Builder
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&body, "needle line %02d\n", i+1)
	}
	if err := ps.PutFile(projectID, "notes.txt", []byte(body.String())); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	tool := newSearchFilesTool()
	out, err := tool.Execute(ctx, ExecContext{ProjectID: projectID, Projects: ps}, mustJSONRaw(t, map[string]any{
		"query": "needle",
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK, got %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	if got := intFromAnyDataTool(result["page_size"]); got != defaultSearchMatches {
		t.Fatalf("page_size=%d, want %d", got, defaultSearchMatches)
	}
	if result["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", result["has_more"])
	}
	matches := result["matches"].(map[string][]string)
	if got := len(matches["notes.txt"]); got != defaultSearchMatches {
		t.Fatalf("matches returned=%d, want %d", got, defaultSearchMatches)
	}

	nextOut, err := tool.Execute(ctx, ExecContext{ProjectID: projectID, Projects: ps}, mustJSONRaw(t, map[string]any{
		"query":      "needle",
		"page_token": result["next_page_token"],
	}))
	if err != nil {
		t.Fatalf("Execute page 2: %v", err)
	}
	next := nextOut.Result.(map[string]any)
	nextMatches := next["matches"].(map[string][]string)
	if got := len(nextMatches["notes.txt"]); got != 5 {
		t.Fatalf("page 2 matches=%d, want 5", got)
	}
}
