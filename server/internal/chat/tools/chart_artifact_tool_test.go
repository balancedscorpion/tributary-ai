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

func TestBuildChartArtifactToolUsesFirstClassResultMetadata(t *testing.T) {
	meta := toolCatalogResultMetadata("build_chart_artifact", "chart", map[string]any{"preview_only": true})
	if meta["tool_name"] != "build_chart_artifact" ||
		meta["native_viewer"] != "chart" ||
		meta["renderer_strategy"] != "artifact_type" ||
		meta["renderer_key"] != "chart" {
		t.Fatalf("metadata=%#v, want first-class chart renderer metadata", meta)
	}
}

func TestBuildChartArtifactToolBuildsPreviewAndArtifactPayload(t *testing.T) {
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

	sourceRows := make([]map[string]any, 0, 1000)
	for i := 0; i < 1000; i++ {
		sourceRows = append(sourceRows, map[string]any{
			"x":       i,
			"y":       i * 2,
			"segment": fmt.Sprintf("g%d", i%7),
			"weight":  (i % 19) + 1,
		})
	}
	b, err := json.Marshal(sourceRows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	sourceContent := string(b)
	_, err = st.InsertArtifact(ctx, store.Artifact{
		ID:          "source-artifact",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Source",
		Storage:     "mysql",
		ContentText: &sourceContent,
		ByteSize:    int64(len(sourceContent)),
	})
	if err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{
			"artifact_id": "source-artifact",
		},
		"chart": map[string]any{
			"kind":   "scatter",
			"engine": "echarts",
			"x":      "x",
			"y":      "y",
			"color":  "segment",
			"size":   "weight",
		},
		"transform": map[string]any{
			"drop_nulls":     true,
			"coerce_numeric": []string{"x", "y", "weight"},
			"max_points":     120,
			"sample_method":  "stratified_reservoir",
			"preview_rows":   25,
		},
		"title": "Scatter Preview",
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		RunID:        "run-chart",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	if out.Artifact == nil {
		t.Fatalf("out.Artifact is nil")
	}

	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	if got := strings.TrimSpace(anyToString(result["chart_engine"])); got != "echarts" {
		t.Fatalf("chart_engine=%q, want echarts", got)
	}
	if got := strings.TrimSpace(anyToString(result["full_artifact_id"])); got == "" {
		t.Fatalf("full_artifact_id missing in result: %#v", result)
	}
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("result contains nested result_metadata: %#v", result)
	}
	if out.ResultMetadata["preview_only"] != true || out.ResultMetadata["full_artifact_id"] != result["full_artifact_id"] {
		t.Fatalf("ResultMetadata=%#v, want preview metadata for artifact %v", out.ResultMetadata, result["full_artifact_id"])
	}
	if _, ok := out.ResultMetadata["chart_budget"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata.chart_budget missing: %#v", out.ResultMetadata["chart_budget"])
	}
	toolMeta, ok := ToolMetadataByName("build_chart_artifact")
	if !ok {
		t.Fatal("missing build_chart_artifact metadata")
	}
	if out.ResultMetadata["tool_name"] != "build_chart_artifact" ||
		out.ResultMetadata["kind"] != "chart" ||
		out.ResultMetadata["tool_set_id"] != "artifacts" ||
		out.ResultMetadata["category"] != "read" ||
		out.ResultMetadata["origin"] != "builtin" ||
		out.ResultMetadata["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		out.ResultMetadata["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		out.ResultMetadata["renderer_key"] != toolMeta.Presentation.RendererKey ||
		out.ResultMetadata["file_capability"] != toolMeta.Presentation.FileCapability {
		t.Fatalf("ResultMetadata=%#v, want build_chart_artifact catalog metadata", out.ResultMetadata)
	}
	if _, ok := out.ResultMetadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", out.ResultMetadata)
	}
	total, ok := result["row_count_total"].(int)
	if !ok {
		if f, okf := result["row_count_total"].(float64); okf {
			total = int(f)
			ok = true
		}
	}
	if !ok || total != 1000 {
		t.Fatalf("row_count_total=%#v, want 1000", result["row_count_total"])
	}
	rowsPreview, ok := result["rows"].([]any)
	if !ok {
		t.Fatalf("rows preview missing: %#v", result["rows"])
	}
	if len(rowsPreview) == 0 || len(rowsPreview) > 25 {
		t.Fatalf("len(rows preview)=%d, want 1..25", len(rowsPreview))
	}
	if _, ok := result["plot_spec"].(map[string]any); !ok {
		t.Fatalf("plot_spec missing: %#v", result)
	}
	if ratio, ok := result["downsample_ratio"].(float64); !ok || ratio <= 1 {
		t.Fatalf("downsample_ratio=%#v, want >1", result["downsample_ratio"])
	}

	payloadRows := []map[string]any{}
	if err := json.Unmarshal([]byte(out.Artifact.Content), &payloadRows); err != nil {
		t.Fatalf("artifact payload JSON parse: %v", err)
	}
	if len(payloadRows) != 1000 {
		t.Fatalf("len(artifact payload rows)=%d, want 1000", len(payloadRows))
	}
	if out.Artifact.Type != "data" {
		t.Fatalf("artifact type=%q, want data", out.Artifact.Type)
	}
	if out.Artifact.Metadata == nil {
		t.Fatalf("artifact metadata is nil")
	}
	if out.Artifact.Metadata["row_count_total"] != 1000 {
		t.Fatalf("artifact metadata row_count_total=%#v, want 1000", out.Artifact.Metadata["row_count_total"])
	}
	artifactResultMeta, ok := out.Artifact.Metadata["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.result_metadata=%T, want object: %#v", out.Artifact.Metadata["result_metadata"], out.Artifact.Metadata)
	}
	if artifactResultMeta["renderer_key"] != "chart" || artifactResultMeta["native_viewer"] != "chart" {
		t.Fatalf("artifact result_metadata=%#v, want build_chart_artifact native chart metadata", artifactResultMeta)
	}
	artifactPresentation, ok := out.Artifact.Metadata["tool_presentation"].(map[string]any)
	if !ok {
		t.Fatalf("artifact metadata.tool_presentation=%T, want object: %#v", out.Artifact.Metadata["tool_presentation"], out.Artifact.Metadata)
	}
	if artifactPresentation["renderer_key"] != "chart" || artifactPresentation["native_viewer"] != "chart" {
		t.Fatalf("artifact tool_presentation=%#v, want build_chart_artifact chart presentation", artifactPresentation)
	}

	resultBlob, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if len(resultBlob) > 250000 {
		t.Fatalf("result envelope too large: %d bytes", len(resultBlob))
	}
}

func TestBuildChartArtifactToolRespectsEngineEnablement(t *testing.T) {
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

	sourceRows := []map[string]any{
		{"x": 1, "y": 2},
		{"x": 2, "y": 4},
	}
	b, err := json.Marshal(sourceRows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(b)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "src-hc",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Source",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"artifact_id": "src-hc"},
		"chart": map[string]any{
			"kind":   "scatter",
			"engine": "highcharts",
			"x":      "x",
			"y":      "y",
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		RunID:        "run-disabled",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: false},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("expected error when highcharts engine is disabled")
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(msg, "disabled") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

func TestBuildChartArtifactToolAcceptsReadFileCSVArtifact(t *testing.T) {
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

	readFilePayload := map[string]any{
		"path":           "tiny.csv",
		"offset":         0,
		"max_bytes":      262144,
		"bytes_returned": 33,
		"has_more":       false,
		"content":        "x,y,segment\n1,2,A\n3,4,B\n5,6,A\n",
	}
	payloadJSON, err := json.Marshal(readFilePayload)
	if err != nil {
		t.Fatalf("json.Marshal payload: %v", err)
	}
	content := string(payloadJSON)
	toolName := "read_file"
	okVal := true
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "read-file-src",
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "read_file result",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		ToolName:    &toolName,
		OK:          &okVal,
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"artifact_id": "read-file-src"},
		"chart": map[string]any{
			"kind":   "scatter",
			"engine": "echarts",
			"x":      "x",
			"y":      "y",
			"color":  "segment",
		},
		"transform": map[string]any{
			"drop_nulls":   true,
			"max_points":   100,
			"preview_rows": 5,
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		RunID:        "run-read-file",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	if got := intFromAny(result["row_count_total"]); got != 3 {
		t.Fatalf("row_count_total=%d, want 3", got)
	}
}

func TestBuildChartArtifactToolLoadsChunkedReadFileArtifactFromProjectPath(t *testing.T) {
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

	ps, err := projects.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	projectID := "chart-project"
	fullCSV := "x,y,segment\n1,2,A\n2,4,A\n3,6,B\n4,8,B\n5,10,C\n"
	if err := ps.PutFile(projectID, "full.csv", []byte(fullCSV)); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	readFilePayload := map[string]any{
		"path":           "full.csv",
		"offset":         0,
		"max_bytes":      20,
		"bytes_returned": 20,
		"has_more":       true,
		"content":        "x,y,segment\n1,2,A\n2,4,",
	}
	payloadJSON, err := json.Marshal(readFilePayload)
	if err != nil {
		t.Fatalf("json.Marshal payload: %v", err)
	}
	content := string(payloadJSON)
	toolName := "read_file"
	okVal := true
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "chunked-read-file",
		ThreadID:    thread.ID,
		Type:        "code",
		Title:       "read_file result",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		ToolName:    &toolName,
		OK:          &okVal,
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"artifact_id": "chunked-read-file"},
		"chart": map[string]any{
			"kind":   "scatter",
			"engine": "echarts",
			"x":      "x",
			"y":      "y",
			"color":  "segment",
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		Projects:     ps,
		ProjectID:    projectID,
		ThreadID:     thread.ID,
		RunID:        "run-chunked-read-file",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	if got := intFromAny(result["row_count_total"]); got != 5 {
		t.Fatalf("row_count_total=%d, want 5 (full project file)", got)
	}
}

func TestBuildChartArtifactToolSourcePath(t *testing.T) {
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

	ps, err := projects.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	projectID := "path-project"
	csv := "x,y\n1,2\n2,3\n3,4\n"
	if err := ps.PutFile(projectID, "points.csv", []byte(csv)); err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"path": "points.csv"},
		"chart": map[string]any{
			"kind":   "scatter",
			"engine": "echarts",
			"x":      "x",
			"y":      "y",
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		Projects:     ps,
		ProjectID:    projectID,
		ThreadID:     thread.ID,
		RunID:        "run-source-path",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	if got := intFromAny(result["row_count_total"]); got != 3 {
		t.Fatalf("row_count_total=%d, want 3", got)
	}
}

func TestBuildChartArtifactToolLineAppliesMarkBudgetAcrossSeries(t *testing.T) {
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

	rows := make([]map[string]any, 0, 1200)
	for i := 0; i < 1200; i++ {
		rows = append(rows, map[string]any{
			"x":      i,
			"y":      (i % 97) + (i / 10),
			"series": fmt.Sprintf("s%d", i%4),
		})
	}
	blob, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(blob)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "line-source",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Line Source",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"artifact_id": "line-source"},
		"chart": map[string]any{
			"kind":   "line",
			"engine": "echarts",
			"x":      "x",
			"y":      "y",
			"color":  "series",
		},
		"transform": map[string]any{
			"max_points":   120,
			"preview_rows": 120,
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		RunID:        "run-line-budget",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	if got := intFromAny(result["row_count_total"]); got != 1200 {
		t.Fatalf("row_count_total=%d, want 1200", got)
	}
	chartBudget, ok := result["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("chart_budget missing: %#v", result["chart_budget"])
	}
	if got := intFromAny(chartBudget["points_requested"]); got != 1200 {
		t.Fatalf("chart_budget.points_requested=%d, want 1200", got)
	}
	if got := intFromAny(chartBudget["points_returned"]); got <= 0 || got > 120 {
		t.Fatalf("chart_budget.points_returned=%d, want 1..120", got)
	}
	if got := intFromAny(chartBudget["series_count"]); got != 4 {
		t.Fatalf("chart_budget.series_count=%d, want 4", got)
	}
	actionsRaw, ok := chartBudget["budget_actions"].([]any)
	if !ok {
		t.Fatalf("chart_budget.budget_actions=%#v, want []any", chartBudget["budget_actions"])
	}
	hasLineAction := false
	for _, item := range actionsRaw {
		action := strings.TrimSpace(strings.ToLower(anyToString(item)))
		if strings.Contains(action, "line_lttb") || strings.Contains(action, "line_stride") {
			hasLineAction = true
			break
		}
	}
	if !hasLineAction {
		t.Fatalf("chart_budget.budget_actions=%#v, want line downsample action", chartBudget["budget_actions"])
	}
	if got := intFromAny(chartBudget["plot_spec_bytes"]); got <= 0 || got > maxChartArtifactSpecBytes {
		t.Fatalf("chart_budget.plot_spec_bytes=%d, want 1..%d", got, maxChartArtifactSpecBytes)
	}
}

func TestBuildChartArtifactToolBarAppliesTopKAndOtherBucket(t *testing.T) {
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

	rows := make([]map[string]any, 0, 350)
	for i := 0; i < 350; i++ {
		rows = append(rows, map[string]any{
			"category": fmt.Sprintf("cat-%03d", i),
			"value":    350 - i,
		})
	}
	blob, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(blob)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "bar-source",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Bar Source",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"artifact_id": "bar-source"},
		"chart": map[string]any{
			"kind":   "bar",
			"engine": "echarts",
			"x":      "category",
			"y":      "value",
		},
		"transform": map[string]any{
			"max_points":   40,
			"preview_rows": 80,
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		RunID:        "run-bar-budget",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected OK result, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	previewRows, ok := result["rows"].([]any)
	if !ok || len(previewRows) == 0 {
		t.Fatalf("rows preview missing: %#v", result["rows"])
	}
	hasOther := false
	for _, item := range previewRows {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(anyToString(row["category"])), "Other") {
			hasOther = true
			break
		}
	}
	if !hasOther {
		t.Fatalf("expected reduced bar preview to include Other bucket: %#v", previewRows)
	}
	chartBudget, ok := result["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("chart_budget missing: %#v", result["chart_budget"])
	}
	if got := intFromAny(chartBudget["points_returned"]); got <= 0 || got > 40 {
		t.Fatalf("chart_budget.points_returned=%d, want 1..40", got)
	}
	actionsRaw, ok := chartBudget["budget_actions"].([]any)
	if !ok {
		t.Fatalf("chart_budget.budget_actions=%#v, want []any", chartBudget["budget_actions"])
	}
	hasBarAction := false
	for _, item := range actionsRaw {
		if strings.Contains(strings.ToLower(strings.TrimSpace(anyToString(item))), "bar_topk_other") {
			hasBarAction = true
			break
		}
	}
	if !hasBarAction {
		t.Fatalf("chart_budget.budget_actions=%#v, want bar_topk_other", chartBudget["budget_actions"])
	}
}

func TestBuildChartArtifactToolReturnsStructuredErrorForOversizedSpec(t *testing.T) {
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

	longLabel := strings.Repeat("category-label-", 700)
	rows := make([]map[string]any, 0, 1200)
	for i := 0; i < 1200; i++ {
		rows = append(rows, map[string]any{
			"x": fmt.Sprintf("%s-%d", longLabel, i),
			"y": i + 1,
		})
	}
	blob, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(blob)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "oversized-spec-source",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Oversized Spec Source",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	tool := newBuildChartArtifactTool()
	args := map[string]any{
		"source": map[string]any{"artifact_id": "oversized-spec-source"},
		"chart": map[string]any{
			"kind":   "bar",
			"engine": "echarts",
			"x":      "x",
			"y":      "y",
		},
		"transform": map[string]any{
			"max_points": 50000,
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		RunID:        "run-oversized-spec",
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out.OK {
		t.Fatalf("expected CHART_SPEC_TOO_LARGE error")
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type %T", out.Result)
	}
	if got := strings.TrimSpace(anyToString(result["code"])); got != "CHART_SPEC_TOO_LARGE" {
		t.Fatalf("code=%q, want CHART_SPEC_TOO_LARGE", got)
	}
	if got := strings.TrimSpace(anyToString(result["failure_stage"])); got != "tool_generation" {
		t.Fatalf("failure_stage=%q, want tool_generation", got)
	}
	chartBudget, ok := result["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("chart_budget missing: %#v", result["chart_budget"])
	}
	if got := intFromAny(chartBudget["plot_spec_bytes"]); got <= maxChartArtifactSpecBytes {
		t.Fatalf("chart_budget.plot_spec_bytes=%d, want > %d", got, maxChartArtifactSpecBytes)
	}
	if got := strings.TrimSpace(anyToString(chartBudget["failure_code"])); got != "CHART_SPEC_TOO_LARGE" {
		t.Fatalf("chart_budget.failure_code=%q, want CHART_SPEC_TOO_LARGE", got)
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func TestApplyChartFilters(t *testing.T) {
	rows := []map[string]any{
		{"region": "US", "category": "A", "revenue": "100"},
		{"region": "EU", "category": "B", "revenue": "200"},
		{"region": "US", "category": "B", "revenue": "300"},
		{"region": "EU", "category": "A", "revenue": "150"},
		{"region": "", "category": "C", "revenue": "50"},
	}

	t.Run("empty filters passthrough", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != len(rows) {
			t.Fatalf("expected %d rows, got %d", len(rows), len(result))
		}
	})

	t.Run("single eq filter", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "region", Operator: "eq", Value: "US"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(result))
		}
		for _, r := range result {
			if r["region"] != "US" {
				t.Errorf("expected region=US, got %v", r["region"])
			}
		}
	})

	t.Run("numeric gt filter", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "revenue", Operator: "gt", Value: "150"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(result))
		}
	})

	t.Run("multi-value OR filter", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "category", Operator: "eq", Values: []string{"A", "C"}},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 3 {
			t.Fatalf("expected 3 rows, got %d", len(result))
		}
	})

	t.Run("compound AND filters", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "region", Operator: "eq", Value: "US"},
			{Column: "category", Operator: "eq", Value: "B"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 row, got %d", len(result))
		}
		if result[0]["revenue"] != "300" {
			t.Errorf("expected revenue=300, got %v", result[0]["revenue"])
		}
	})

	t.Run("is_null filter", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "region", Operator: "is_null"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 1 {
			t.Fatalf("expected 1 row (empty region), got %d", len(result))
		}
	})

	t.Run("not_null filter", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "region", Operator: "not_null"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 4 {
			t.Fatalf("expected 4 rows, got %d", len(result))
		}
	})

	t.Run("all rows filtered out", func(t *testing.T) {
		result, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "region", Operator: "eq", Value: "NONEXISTENT"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result) != 0 {
			t.Fatalf("expected 0 rows, got %d", len(result))
		}
	})

	t.Run("invalid operator", func(t *testing.T) {
		_, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "region", Operator: "invalid_op", Value: "US"},
		})
		if err == nil {
			t.Fatal("expected error for invalid operator")
		}
	})

	t.Run("missing column name", func(t *testing.T) {
		_, err := ApplyChartFilters(rows, []ChartArtifactFilterInput{
			{Column: "", Operator: "eq", Value: "US"},
		})
		if err == nil {
			t.Fatal("expected error for missing column")
		}
	})
}

func TestBuildChartPlotSpecGroupsLineSeriesByColor(t *testing.T) {
	rows := []map[string]any{
		{"month": "2026-01", "region": "US", "revenue": 100},
		{"month": "2026-02", "region": "US", "revenue": 130},
		{"month": "2026-01", "region": "EU", "revenue": 90},
		{"month": "2026-02", "region": "EU", "revenue": 95},
	}

	out, err := BuildChartPlotSpec(rows, ChartRebuildInput{
		Kind:   "line",
		Engine: "echarts",
		X:      "month",
		Y:      "revenue",
		Color:  "region",
	}, "Revenue")
	if err != nil {
		t.Fatalf("BuildChartPlotSpec: %v", err)
	}
	if out.Engine != "echarts" {
		t.Fatalf("engine=%q, want echarts", out.Engine)
	}

	options, ok := out.PlotSpec["options"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec.options missing: %#v", out.PlotSpec)
	}
	seriesRaw, ok := options["series"].([]any)
	if !ok || len(seriesRaw) != 2 {
		t.Fatalf("series=%#v, want two grouped series", options["series"])
	}

	names := map[string]struct{}{}
	for _, item := range seriesRaw {
		series, _ := item.(map[string]any)
		names[strings.TrimSpace(anyToString(series["name"]))] = struct{}{}
	}
	if _, ok := names["US"]; !ok {
		t.Fatalf("series names=%#v, missing US", names)
	}
	if _, ok := names["EU"]; !ok {
		t.Fatalf("series names=%#v, missing EU", names)
	}
}
