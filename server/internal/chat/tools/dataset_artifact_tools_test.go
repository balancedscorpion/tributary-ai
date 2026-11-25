package tools

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
)

func setupDatasetArtifactToolTest(t *testing.T) (context.Context, ExecContext) {
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
	rows := []map[string]any{
		{"id": 1, "item": "Alpha", "segment": "a", "amount": 10, "note": "first sale"},
		{"id": 2, "item": "Beta", "segment": "b", "amount": 20, "note": "repeat customer"},
		{"id": 3, "item": "Gamma", "segment": "b", "amount": 30, "note": "expansion sale"},
		{"id": 4, "item": "Delta", "segment": "c", "amount": nil, "note": "missing amount"},
		{"id": 5, "item": "Alphanumeric", "segment": "a", "amount": 40, "note": "alpha followup"},
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("Marshal rows: %v", err)
	}
	content := string(b)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "dataset-artifact",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Dataset Artifact",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		Metadata: map[string]any{
			"row_count": len(rows),
		},
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	return ctx, ExecContext{
		Store:    st,
		ThreadID: thread.ID,
		CfgDir:   cfgDir,
	}
}

func requireDatasetArtifactResultMetadata(t *testing.T, out Output, kind string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("result should not contain nested result_metadata: %#v", result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != "artifact" || meta["product"] != "dataset" || meta["family"] != "data-analysis" || meta["kind"] != kind || meta["readonly"] != true {
		t.Fatalf("ResultMetadata=%#v, want dataset artifact %s metadata", meta, kind)
	}
	if meta["artifact_id"] != "dataset-artifact" || meta["source_artifact_id"] != "dataset-artifact" {
		t.Fatalf("ResultMetadata artifact refs=%#v, want dataset-artifact", meta)
	}
	toolName, _ := meta["tool_name"].(string)
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("missing metadata for dataset artifact tool %q", toolName)
	}
	if meta["tool_set_id"] != toolMeta.ToolSetID ||
		meta["category"] != string(toolMeta.Category) ||
		meta["origin"] != string(toolMeta.Origin) ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("ResultMetadata=%#v, want dataset catalog presentation %#v", meta, toolMeta.Presentation)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", meta)
	}
	return meta
}

func TestDatasetArtifactToolsUseFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("dataset_artifact_tools.go")
	if err != nil {
		t.Fatalf("read dataset_artifact_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("dataset artifact tools should set Output.ResultMetadata directly instead of nesting result_metadata in result maps")
	}
}

func TestInspectDatasetArtifactToolProfilesRows(t *testing.T) {
	ctx, exec := setupDatasetArtifactToolTest(t)
	tool := newInspectDatasetArtifactTool()
	args := mustJSONRaw(t, map[string]any{
		"artifact_id": "dataset-artifact",
		"sample_rows": 2,
	})

	out, err := tool.Execute(ctx, exec, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	if got := result["row_count"].(int); got != 5 {
		t.Fatalf("row_count=%d, want 5", got)
	}
	if got := result["column_count"].(int); got != 5 {
		t.Fatalf("column_count=%d, want 5", got)
	}
	columns := result["columns"].([]ColumnProfile)
	amount := findColumnProfile(t, columns, "amount")
	if amount.Type != ColTypeNumber {
		t.Fatalf("amount type=%q, want number", amount.Type)
	}
	if amount.NullCount != 1 {
		t.Fatalf("amount null_count=%d, want 1", amount.NullCount)
	}
	if amount.Mean == nil || *amount.Mean != 25 {
		t.Fatalf("amount mean=%v, want 25", amount.Mean)
	}
	samples := result["sample_rows"].([]map[string]any)
	if len(samples) != 2 {
		t.Fatalf("sample row count=%d, want 2", len(samples))
	}
	meta := requireDatasetArtifactResultMetadata(t, out, "dataset_profile")
	if meta["content_kind"] != "dataset_profile" || meta["row_count"] != 5 || meta["column_count"] != 5 {
		t.Fatalf("ResultMetadata=%#v, want dataset profile counts", meta)
	}
}

func TestGetDatasetRowsToolFiltersSortsProjectsAndPages(t *testing.T) {
	ctx, exec := setupDatasetArtifactToolTest(t)
	tool := newGetDatasetRowsTool()
	args := mustJSONRaw(t, map[string]any{
		"artifact_id": "dataset-artifact",
		"limit":       1,
		"columns":     []string{"item", "amount"},
		"filters": []map[string]any{
			{"column": "segment", "operator": "eq", "value": "b"},
		},
		"sort": map[string]any{"by": "amount", "direction": "desc"},
	})

	out, err := tool.Execute(ctx, exec, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	rows := result["rows"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("rows returned=%d, want 1", len(rows))
	}
	if rows[0]["item"] != "Gamma" {
		t.Fatalf("first item=%#v, want Gamma", rows[0]["item"])
	}
	if _, ok := rows[0]["segment"]; ok {
		t.Fatalf("projected row unexpectedly includes segment: %#v", rows[0])
	}
	if result["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", result["has_more"])
	}
	meta := requireDatasetArtifactResultMetadata(t, out, "dataset_rows")
	if meta["content_kind"] != "json_rows" || meta["rows_returned"] != 1 || meta["filtered_row_count"] != 2 || meta["has_more"] != true {
		t.Fatalf("ResultMetadata=%#v, want paged row metadata", meta)
	}
	token := result["next_page_token"].(string)
	nextArgs := mustJSONRaw(t, map[string]any{
		"artifact_id": "dataset-artifact",
		"page_token":  token,
		"columns":     []string{"item", "amount"},
		"filters": []map[string]any{
			{"column": "segment", "operator": "eq", "value": "b"},
		},
		"sort": map[string]any{"by": "amount", "direction": "desc"},
	})
	nextOut, err := tool.Execute(ctx, exec, nextArgs)
	if err != nil {
		t.Fatalf("Execute page 2: %v", err)
	}
	nextRows := nextOut.Result.(map[string]any)["rows"].([]map[string]any)
	if len(nextRows) != 1 || nextRows[0]["item"] != "Beta" {
		t.Fatalf("page 2 rows=%#v, want Beta", nextRows)
	}
}

func TestAggregateDatasetToolGroupsAndSorts(t *testing.T) {
	ctx, exec := setupDatasetArtifactToolTest(t)
	tool := newAggregateDatasetTool()
	args := mustJSONRaw(t, map[string]any{
		"artifact_id": "dataset-artifact",
		"group_by":    []string{"segment"},
		"metrics": []map[string]any{
			{"function": "count"},
			{"column": "amount", "function": "sum"},
			{"column": "item", "function": "count_distinct"},
		},
		"sort": map[string]any{"by": "sum(amount)", "direction": "desc"},
	})

	out, err := tool.Execute(ctx, exec, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	rows := result["rows"].([]map[string]any)
	if len(rows) != 3 {
		t.Fatalf("aggregate rows=%d, want 3", len(rows))
	}
	if rows[0]["segment"] != "a" {
		t.Fatalf("top segment=%#v, want a", rows[0]["segment"])
	}
	if rows[0]["sum(amount)"] != 50.0 {
		t.Fatalf("top sum=%#v, want 50", rows[0]["sum(amount)"])
	}
	if rows[0]["count(*)"] != int64(2) {
		t.Fatalf("top count=%#v, want 2", rows[0]["count(*)"])
	}
	meta := requireDatasetArtifactResultMetadata(t, out, "dataset_aggregate")
	if meta["content_kind"] != "json_rows" || meta["rows_returned"] != 3 || meta["group_count"] != 3 {
		t.Fatalf("ResultMetadata=%#v, want aggregate metadata", meta)
	}
}

func TestSearchDatasetRowsToolPagesMatches(t *testing.T) {
	ctx, exec := setupDatasetArtifactToolTest(t)
	tool := newSearchDatasetRowsTool()
	args := mustJSONRaw(t, map[string]any{
		"artifact_id": "dataset-artifact",
		"query":       "alp",
		"columns":     []string{"item"},
		"limit":       1,
	})

	out, err := tool.Execute(ctx, exec, args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result := out.Result.(map[string]any)
	if result["total_matches"] != 2 {
		t.Fatalf("total_matches=%#v, want 2", result["total_matches"])
	}
	rows := result["rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["item"] != "Alpha" {
		t.Fatalf("first search rows=%#v, want Alpha", rows)
	}
	if result["has_more"] != true {
		t.Fatalf("has_more=%#v, want true", result["has_more"])
	}
	meta := requireDatasetArtifactResultMetadata(t, out, "dataset_search")
	if meta["content_kind"] != "json_rows" || meta["rows_returned"] != 1 || meta["total_matches"] != 2 || meta["query"] != "alp" {
		t.Fatalf("ResultMetadata=%#v, want search metadata", meta)
	}
	nextOut, err := tool.Execute(ctx, exec, mustJSONRaw(t, map[string]any{
		"artifact_id": "dataset-artifact",
		"query":       "alp",
		"columns":     []string{"item"},
		"page_token":  result["next_page_token"].(string),
	}))
	if err != nil {
		t.Fatalf("Execute page 2: %v", err)
	}
	nextRows := nextOut.Result.(map[string]any)["rows"].([]map[string]any)
	if len(nextRows) != 1 || nextRows[0]["item"] != "Alphanumeric" {
		t.Fatalf("second search rows=%#v, want Alphanumeric", nextRows)
	}
}

func mustJSONRaw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal args: %v", err)
	}
	return b
}

func findColumnProfile(t *testing.T, columns []ColumnProfile, name string) ColumnProfile {
	t.Helper()
	for _, column := range columns {
		if column.Name == name {
			return column
		}
	}
	t.Fatalf("column %q not found in %#v", name, columns)
	return ColumnProfile{}
}
