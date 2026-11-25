package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
)

func mustHighchartsCapabilityVersion(t *testing.T) string {
	t.Helper()
	snapshot, err := loadHighchartsCapabilitiesSnapshot()
	if err != nil {
		t.Fatalf("loadHighchartsCapabilitiesSnapshot: %v", err)
	}
	version, _ := snapshot["capability_version"].(string)
	version = strings.TrimSpace(version)
	if version == "" {
		t.Fatalf("capability_version is empty: %#v", snapshot)
	}
	return version
}

func TestDisplayTableToolUsesFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("data_tools.go")
	if err != nil {
		t.Fatalf("read data_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("display_table should set Output.ResultMetadata directly instead of nesting result_metadata in result maps")
	}
}

func TestDisplayTableToolSmallResultUsesFirstClassResultMetadata(t *testing.T) {
	out, err := newDisplayTableTool().Execute(context.Background(), ExecContext{}, mustJSONRaw(t, map[string]any{
		"title":     "Small table",
		"data_json": `[{"region":"EMEA","revenue":42}]`,
	}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", out.Result)
	}
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("result contains nested result_metadata: %#v", result)
	}
	meta := out.ResultMetadata
	toolMeta, ok := ToolMetadataByName("display_table")
	if !ok {
		t.Fatal("missing display_table metadata")
	}
	if meta["provider"] != "artifacts" || meta["tool_name"] != "display_table" || meta["kind"] != "table" {
		t.Fatalf("ResultMetadata=%#v, want display_table table metadata", meta)
	}
	if meta["result_kind"] != toolMeta.Presentation.ResultKind ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("renderer metadata=%#v, want presentation %#v", meta, toolMeta.Presentation)
	}
	if meta["row_count_total"] != 1 || meta["row_count_returned"] != 1 || meta["preview_only"] != false {
		t.Fatalf("row metadata=%#v, want one full row", meta)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", meta)
	}
}

func mustEchartsCapabilityVersion(t *testing.T) string {
	t.Helper()
	snapshot, err := loadEchartsCapabilitiesSnapshot()
	if err != nil {
		t.Fatalf("loadEchartsCapabilitiesSnapshot: %v", err)
	}
	version, _ := snapshot["capability_version"].(string)
	version = strings.TrimSpace(version)
	if version == "" {
		t.Fatalf("capability_version is empty: %#v", snapshot)
	}
	return version
}

func TestDisplayTableToolAcceptsValidHighchartsSpec(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustHighchartsCapabilityVersion(t)
	args := map[string]any{
		"data_json": `[ {"month":"2026-01","revenue":1200.5}, {"month":"2026-02","revenue":980.25} ]`,
		"title":     "Revenue by month",
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"chart":   map[string]any{"type": "line"},
				"tooltip": map[string]any{"formatter_id": "tooltip_currency_usd"},
				"series": []any{
					map[string]any{
						"type": "line",
						"name": "Revenue",
						"data": []any{1200.5, 980.25},
					},
				},
			},
		},
		"chart_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	rows, ok := result["rows"].([]any)
	if !ok || len(rows) != 2 {
		t.Fatalf("rows=%#v, want 2 rows", result["rows"])
	}
	if result["chart_engine"] != highchartsEngineName {
		t.Fatalf("chart_engine=%#v, want %q", result["chart_engine"], highchartsEngineName)
	}
	plotSpec, ok := result["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(plotSpec["schema_version"])); got != HighchartsPlotSpecVersion {
		t.Fatalf("plot_spec.schema_version=%q, want %q", got, HighchartsPlotSpecVersion)
	}
	if got := strings.TrimSpace(anyToString(result["chart_spec_version"])); got != version {
		t.Fatalf("chart_spec_version=%q, want %q", got, version)
	}
}

func TestDisplayTableToolAcceptsStringifiedHighchartsSpec(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustHighchartsCapabilityVersion(t)
	stringifiedSpec := `{"schema_version":"highcharts-plot-spec.v1","module":"core","options":{"chart":{"type":"line"},"series":[{"type":"line","name":"Revenue","data":[1200.5,980.25]}]}}`
	args := map[string]any{
		"data_json":          `[ {"month":"2026-01","revenue":1200.5}, {"month":"2026-02","revenue":980.25} ]`,
		"title":              "Revenue by month",
		"chart_spec":         stringifiedSpec,
		"chart_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	if result["chart_engine"] != highchartsEngineName {
		t.Fatalf("chart_engine=%#v, want %q", result["chart_engine"], highchartsEngineName)
	}
	plotSpec, ok := result["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(plotSpec["schema_version"])); got != HighchartsPlotSpecVersion {
		t.Fatalf("plot_spec.schema_version=%q, want %q", got, HighchartsPlotSpecVersion)
	}
}

func TestDisplayTableToolAcceptsValidHighchartsSpecV2Maps(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustHighchartsCapabilityVersion(t)
	args := map[string]any{
		"data_json": `[ {"region":"A","value":12}, {"region":"B","value":7} ]`,
		"title":     "Map Coverage",
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v2",
			"module":         "maps",
			"modules":        []any{"drilldown", "heatmap"},
			"options": map[string]any{
				"title": map[string]any{"text": "Coverage by Region"},
				"series": []any{
					map[string]any{
						"type": "map",
						"name": "Coverage",
						"mapData": map[string]any{
							"type":     "FeatureCollection",
							"features": []any{},
						},
						"data": []any{
							map[string]any{"hc-key": "a", "value": 12, "drilldown": "a_detail"},
							map[string]any{"hc-key": "b", "value": 7},
						},
					},
				},
			},
			"extensions": map[string]any{
				"drilldown_async": map[string]any{
					"loader_id": "drilldown_embedded_series",
					"series_by_id": map[string]any{
						"a_detail": map[string]any{
							"type": "column",
							"name": "A Detail",
							"data": []any{6, 6},
						},
					},
					"max_depth": 3,
				},
			},
		},
		"chart_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}

	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	plotSpec, ok := result["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(plotSpec["schema_version"])); got != HighchartsPlotSpecVersionV2 {
		t.Fatalf("plot_spec.schema_version=%q, want %q", got, HighchartsPlotSpecVersionV2)
	}
	if got := strings.TrimSpace(anyToString(result["chart_module"])); got != "maps" {
		t.Fatalf("chart_module=%q, want maps", got)
	}
	switch modules := result["chart_modules"].(type) {
	case []any:
		if len(modules) == 0 {
			t.Fatalf("chart_modules=%#v, want non-empty array", result["chart_modules"])
		}
	case []string:
		if len(modules) == 0 {
			t.Fatalf("chart_modules=%#v, want non-empty array", result["chart_modules"])
		}
	default:
		t.Fatalf("chart_modules=%#v, want non-empty array", result["chart_modules"])
	}
}

func TestDisplayTableToolRejectsUnsupportedFormatterID(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"month":"2026-01","revenue":1200.5} ]`,
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"tooltip": map[string]any{"formatter_id": "alert_hack"},
				"series":  []any{map[string]any{"type": "line", "data": []any{1}}},
			},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(msg, "unsupported tooltip.formatter_id") {
		t.Fatalf("error %q does not contain expected text", msg)
	}
}

func TestDisplayTableToolRejectsStaleChartSpecVersion(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"month":"2026-01","revenue":1200.5} ]`,
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"series": []any{map[string]any{"type": "line", "data": []any{1}}},
			},
		},
		"chart_spec_version": "hc-cap-v1-stale",
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(msg, "chart_spec_version mismatch") {
		t.Fatalf("error %q does not contain expected text", msg)
	}
}

func TestDisplayTableToolRejectsMapURLSources(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"region":"A","value":12} ]`,
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v2",
			"module":         "maps",
			"options": map[string]any{
				"series": []any{
					map[string]any{
						"type":    "map",
						"name":    "Unsafe",
						"mapData": "https://example.com/world.geo.json",
						"data":    []any{map[string]any{"hc-key": "a", "value": 12}},
					},
				},
			},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(strings.ToLower(msg), "map") {
		t.Fatalf("error %q does not mention map policy", msg)
	}
}

func TestDisplayTableToolBackwardsCompatibleWithoutChartSpec(t *testing.T) {
	tool := newDisplayTableTool()
	rawArgs := []byte(`{"data_json":"[{\"name\":\"A\",\"value\":1}]"}`)

	out, err := tool.Execute(context.Background(), ExecContext{}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	rows, ok := result["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("rows=%#v, want one row", result["rows"])
	}
	if _, ok := result["plot_spec"]; ok {
		t.Fatalf("plot_spec should not be present without chart_spec input: %#v", result)
	}
}

func TestDisplayTableToolRejectsMissingAndConflictingDataSources(t *testing.T) {
	tool := newDisplayTableTool()

	t.Run("rejects missing source and data_json", func(t *testing.T) {
		out, err := tool.Execute(context.Background(), ExecContext{}, []byte(`{"title":"No data"}`))
		if err != nil {
			t.Fatalf("Execute returned unexpected error: %v", err)
		}
		msg := toolErrorMessage(t, out)
		if !strings.Contains(strings.ToLower(msg), "exactly one data source") {
			t.Fatalf("error %q does not mention one-of data source requirement", msg)
		}
	})

	t.Run("rejects both source and data_json", func(t *testing.T) {
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

		rows := []map[string]any{{"name": "A", "value": 1}}
		b, err := json.Marshal(rows)
		if err != nil {
			t.Fatalf("json.Marshal rows: %v", err)
		}
		content := string(b)
		if _, err := st.InsertArtifact(ctx, store.Artifact{
			ID:          "src-display-table",
			ThreadID:    thread.ID,
			Type:        "data",
			Title:       "Rows",
			Storage:     "mysql",
			ContentText: &content,
			ByteSize:    int64(len(content)),
		}); err != nil {
			t.Fatalf("InsertArtifact: %v", err)
		}

		args := map[string]any{
			"data_json": `[{"name":"A","value":1}]`,
			"source": map[string]any{
				"artifact_id": "src-display-table",
			},
		}
		rawArgs, err := json.Marshal(args)
		if err != nil {
			t.Fatalf("json.Marshal args: %v", err)
		}

		out, err := tool.Execute(ctx, ExecContext{
			Store:    st,
			ThreadID: thread.ID,
			CfgDir:   cfgDir,
		}, rawArgs)
		if err != nil {
			t.Fatalf("Execute returned unexpected error: %v", err)
		}
		msg := toolErrorMessage(t, out)
		if !strings.Contains(strings.ToLower(msg), "exactly one data source") {
			t.Fatalf("error %q does not mention one-of data source requirement", msg)
		}
	})
}

func TestDisplayTableToolAcceptsValidEchartsSpec(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustEchartsCapabilityVersion(t)
	args := map[string]any{
		"data_json": `[ {"month":"2026-01","count":120}, {"month":"2026-02","count":98} ]`,
		"title":     "Events by month",
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"title": map[string]any{"text": "Monthly Events"},
				"tooltip": map[string]any{
					"trigger":      "axis",
					"formatter_id": "echarts_tooltip_compact",
				},
				"xAxis": map[string]any{"type": "category", "data": []any{"2026-01", "2026-02"}},
				"yAxis": map[string]any{"type": "value"},
				"series": []any{
					map[string]any{
						"type": "bar",
						"name": "Events",
						"data": []any{120, 98},
					},
				},
			},
		},
		"echarts_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	if result["chart_engine"] != echartsEngineName {
		t.Fatalf("chart_engine=%#v, want %q", result["chart_engine"], echartsEngineName)
	}
	plotSpec, ok := result["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(plotSpec["schema_version"])); got != EchartsPlotSpecVersionV1 {
		t.Fatalf("plot_spec.schema_version=%q, want %q", got, EchartsPlotSpecVersionV1)
	}
}

func TestDisplayTableToolAcceptsSourceArtifactWithAdvancedEchartsSpec(t *testing.T) {
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
		{"x": 0, "y": 2},
		{"x": 1, "y": 4},
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(b)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "src-echarts-advanced",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	version := mustEchartsCapabilityVersion(t)
	args := map[string]any{
		"source": map[string]any{
			"artifact_id": "src-echarts-advanced",
		},
		"title": "Custom Series",
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "all",
			"options": map[string]any{
				"xAxis": map[string]any{"type": "value"},
				"yAxis": map[string]any{"type": "value"},
				"series": []any{
					map[string]any{
						"type":           "custom",
						"render_item_id": "custom_render_rect",
						"data":           []any{[]any{0, 2}, []any{1, 4}},
					},
				},
			},
		},
		"echarts_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	tool := newDisplayTableTool()
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", out.Result)
	}
	if got := strings.TrimSpace(anyToString(result["chart_engine"])); got != "echarts" {
		t.Fatalf("chart_engine=%q, want echarts", got)
	}
	plotSpec, ok := result["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", result)
	}
	if got := strings.TrimSpace(anyToString(plotSpec["schema_version"])); got != EchartsPlotSpecVersionV1 {
		t.Fatalf("plot_spec.schema_version=%q, want %q", got, EchartsPlotSpecVersionV1)
	}
}

func TestDisplayTableToolSourceBackedLargePreviewContract(t *testing.T) {
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

	rows := make([]map[string]any, 0, 450)
	for i := 0; i < 450; i++ {
		rows = append(rows, map[string]any{
			"category": fmt.Sprintf("c-%03d", i+1),
			"value":    i + 1,
		})
	}
	b, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(b)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "src-preview-large",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Rows",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	version := mustEchartsCapabilityVersion(t)
	args := map[string]any{
		"source": map[string]any{
			"artifact_id": "src-preview-large",
		},
		"title": "Preview Rows",
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"xAxis":   map[string]any{"type": "category", "data": []any{"A", "B", "C"}},
				"yAxis":   map[string]any{"type": "value"},
				"tooltip": map[string]any{"formatter_id": "echarts_tooltip_compact"},
				"series":  []any{map[string]any{"type": "bar", "name": "Value", "data": []any{1, 2, 3}}},
			},
		},
		"echarts_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	tool := newDisplayTableTool()
	out, err := tool.Execute(ctx, ExecContext{
		Store:        st,
		ThreadID:     thread.ID,
		CfgDir:       cfgDir,
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	if out.Artifact == nil {
		t.Fatalf("expected out.Artifact for source-backed preview result")
	}

	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", out.Result)
	}
	if got := result["preview_only"]; got != true {
		t.Fatalf("preview_only=%#v, want true", got)
	}
	if got := intFromAnyDataTool(result["row_count_total"]); got != 450 {
		t.Fatalf("row_count_total=%d, want 450", got)
	}
	rowsPreview, ok := result["rows"].([]any)
	if !ok {
		t.Fatalf("rows preview missing: %#v", result["rows"])
	}
	if len(rowsPreview) != displayTableSourcePreviewRows {
		t.Fatalf("len(rows)=%d, want %d", len(rowsPreview), displayTableSourcePreviewRows)
	}
	fullArtifactID := strings.TrimSpace(anyToString(result["full_artifact_id"]))
	if fullArtifactID == "" {
		t.Fatalf("full_artifact_id missing in result: %#v", result)
	}
	retrieval, ok := result["retrieval"].(map[string]any)
	if !ok {
		t.Fatalf("retrieval hints missing: %#v", result["retrieval"])
	}
	if got := strings.TrimSpace(anyToString(retrieval["artifact_id"])); got != fullArtifactID {
		t.Fatalf("retrieval.artifact_id=%q, want %q", got, fullArtifactID)
	}
	if out.Artifact.ID != fullArtifactID {
		t.Fatalf("artifact payload id=%q, want full_artifact_id %q", out.Artifact.ID, fullArtifactID)
	}
	if out.Artifact.Metadata == nil {
		t.Fatalf("artifact metadata missing")
	}
	if _, ok := out.Artifact.Metadata["retrieval"].(map[string]any); !ok {
		t.Fatalf("artifact retrieval hints missing: %#v", out.Artifact.Metadata)
	}

	fullRows := []map[string]any{}
	if err := json.Unmarshal([]byte(out.Artifact.Content), &fullRows); err != nil {
		t.Fatalf("parse artifact payload JSON: %v", err)
	}
	if len(fullRows) != 450 {
		t.Fatalf("len(full artifact rows)=%d, want 450", len(fullRows))
	}

	chartBudget, ok := result["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("chart_budget missing: %#v", result["chart_budget"])
	}
	if got := intFromAnyDataTool(chartBudget["points_requested"]); got != 450 {
		t.Fatalf("chart_budget.points_requested=%d, want 450", got)
	}
	if got := intFromAnyDataTool(chartBudget["points_returned"]); got != displayTableSourcePreviewRows {
		t.Fatalf("chart_budget.points_returned=%d, want %d", got, displayTableSourcePreviewRows)
	}
}

func TestDisplayTableToolAcceptsEchartsSpecModuleAllCustomSeries(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustEchartsCapabilityVersion(t)
	args := map[string]any{
		"data_json": `[ {"x":0,"y":2}, {"x":1,"y":4} ]`,
		"title":     "Custom Rect Series",
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "all",
			"options": map[string]any{
				"xAxis": map[string]any{"type": "value"},
				"yAxis": map[string]any{"type": "value"},
				"series": []any{
					map[string]any{
						"type":           "custom",
						"render_item_id": "custom_render_rect",
						"data":           []any{[]any{0, 2}, []any{1, 4}},
					},
				},
			},
		},
		"echarts_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}

	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %#v", out.Result)
	}
	if got := strings.TrimSpace(anyToString(result["chart_module"])); got != "all" {
		t.Fatalf("chart_module=%q, want all", got)
	}
}

func TestDisplayTableToolRejectsEchartsCustomSeriesWithoutRenderItemID(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"x":0,"y":2} ]`,
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "all",
			"options": map[string]any{
				"series": []any{
					map[string]any{
						"type": "custom",
						"data": []any{[]any{0, 2}},
					},
				},
			},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(strings.ToLower(msg), "render_item_id") {
		t.Fatalf("error %q does not mention render_item_id requirement", msg)
	}
}

func TestDisplayTableToolRejectsEchartsRenderItemFunctionPayload(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"x":0,"y":2} ]`,
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "all",
			"options": map[string]any{
				"series": []any{
					map[string]any{
						"type":       "custom",
						"renderItem": "function(params, api){ return {}; }",
						"data":       []any{[]any{0, 2}},
					},
				},
			},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(strings.ToLower(msg), "executable") && !strings.Contains(strings.ToLower(msg), "renderitem") {
		t.Fatalf("error %q does not mention executable/renderItem policy", msg)
	}
}

func TestDisplayTableToolRejectsDisabledChartEngine(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"month":"2026-01","revenue":1200.5} ]`,
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"series": []any{map[string]any{"type": "line", "data": []any{1}}},
			},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: false},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(msg, "disabled") {
		t.Fatalf("error %q does not mention disabled engine", msg)
	}
}

func TestDisplayTableToolRejectsMultipleChartSpecs(t *testing.T) {
	tool := newDisplayTableTool()
	args := map[string]any{
		"data_json": `[ {"month":"2026-01","revenue":1200.5} ]`,
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"series": []any{map[string]any{"type": "line", "data": []any{1}}},
			},
		},
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"series": []any{map[string]any{"type": "bar", "data": []any{1}}},
			},
		},
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true, Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	msg := toolErrorMessage(t, out)
	if !strings.Contains(strings.ToLower(msg), "only one chart spec") {
		t.Fatalf("error %q does not mention chart spec exclusivity", msg)
	}
}

func TestDisplayTableToolRejectsLargeChartedPayloads(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustHighchartsCapabilityVersion(t)
	rows := make([]map[string]any, 0, 10001)
	for i := 0; i < 10001; i++ {
		rows = append(rows, map[string]any{
			"month": fmt.Sprintf("2026-%02d", (i%12)+1),
			"value": i + 1,
		})
	}
	rowsJSON, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	args := map[string]any{
		"data_json": string(rowsJSON),
		"chart_spec": map[string]any{
			"schema_version": "highcharts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"chart":   map[string]any{"type": "line"},
				"tooltip": map[string]any{"formatter_id": "tooltip_compact"},
				"series": []any{
					map[string]any{"type": "line", "name": "Value", "data": []any{1, 2, 3}},
				},
			},
		},
		"chart_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Highcharts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if out.OK {
		t.Fatalf("expected structured guardrail error")
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", out.Result)
	}
	if got := strings.TrimSpace(anyToString(result["code"])); got != "DATA_TOO_LARGE_FOR_DISPLAY_TABLE" {
		t.Fatalf("code=%q, want DATA_TOO_LARGE_FOR_DISPLAY_TABLE", got)
	}
	if got := strings.TrimSpace(anyToString(result["failure_stage"])); got != "tool_generation" {
		t.Fatalf("failure_stage=%q, want tool_generation", got)
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(anyToString(result["hint"]))), "build_chart_artifact") {
		t.Fatalf("hint=%q, want build_chart_artifact guidance", strings.TrimSpace(anyToString(result["hint"])))
	}
	chartBudget, ok := result["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("chart_budget missing: %#v", result["chart_budget"])
	}
	if got := strings.TrimSpace(anyToString(chartBudget["schema_version"])); got != "chart-budget.v1" {
		t.Fatalf("chart_budget.schema_version=%q, want chart-budget.v1", got)
	}
	if got := intFromAnyDataTool(chartBudget["row_count_total"]); got != 10001 {
		t.Fatalf("chart_budget.row_count_total=%d, want 10001", got)
	}
	if got := strings.TrimSpace(anyToString(chartBudget["failure_code"])); got != "DATA_TOO_LARGE_FOR_DISPLAY_TABLE" {
		t.Fatalf("chart_budget.failure_code=%q, want DATA_TOO_LARGE_FOR_DISPLAY_TABLE", got)
	}
}

func TestDisplayTableToolAllowsSmallChartedPayloadsAndEmitsBudget(t *testing.T) {
	tool := newDisplayTableTool()
	version := mustEchartsCapabilityVersion(t)
	rows := make([]map[string]any, 0, 50)
	for i := 0; i < 50; i++ {
		rows = append(rows, map[string]any{
			"category": fmt.Sprintf("c-%d", i),
			"value":    i + 1,
		})
	}
	rowsJSON, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	args := map[string]any{
		"data_json": string(rowsJSON),
		"echarts_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "core",
			"options": map[string]any{
				"xAxis":   map[string]any{"type": "category", "data": []any{"A", "B", "C"}},
				"yAxis":   map[string]any{"type": "value"},
				"tooltip": map[string]any{"formatter_id": "echarts_tooltip_compact"},
				"series":  []any{map[string]any{"type": "bar", "name": "Value", "data": []any{1, 2, 3}}},
			},
		},
		"echarts_spec_version": version,
	}
	rawArgs, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal args: %v", err)
	}

	out, err := tool.Execute(context.Background(), ExecContext{
		ChartEngines: ChartEngineFlags{Echarts: true},
	}, rawArgs)
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got error: %s", toolErrorMessage(t, out))
	}
	result, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected result type: %T", out.Result)
	}
	chartBudget, ok := result["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("result.chart_budget missing: %#v", result["chart_budget"])
	}
	if got := strings.TrimSpace(anyToString(chartBudget["failure_stage"])); got != "" {
		t.Fatalf("chart_budget.failure_stage=%q, want empty", got)
	}
	if got := intFromAnyDataTool(chartBudget["points_returned"]); got != 50 {
		t.Fatalf("chart_budget.points_returned=%d, want 50", got)
	}
	if _, ok := result["result_metadata"]; ok {
		t.Fatalf("result contains nested result_metadata: %#v", result)
	}
	metaBudget, ok := out.ResultMetadata["chart_budget"].(map[string]any)
	if !ok {
		t.Fatalf("ResultMetadata.chart_budget missing: %#v", out.ResultMetadata["chart_budget"])
	}
	if got := intFromAnyDataTool(metaBudget["result_payload_bytes"]); got <= 0 {
		t.Fatalf("ResultMetadata.chart_budget.result_payload_bytes=%d, want > 0", got)
	}
	toolMeta, ok := ToolMetadataByName("display_table")
	if !ok {
		t.Fatal("missing display_table metadata")
	}
	if out.ResultMetadata["tool_name"] != "display_table" ||
		out.ResultMetadata["kind"] != "table" ||
		out.ResultMetadata["tool_set_id"] != "artifacts" ||
		out.ResultMetadata["category"] != "read" ||
		out.ResultMetadata["origin"] != "builtin" ||
		out.ResultMetadata["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		out.ResultMetadata["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		out.ResultMetadata["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("ResultMetadata=%#v, want charted display_table catalog metadata", out.ResultMetadata)
	}
	if _, ok := out.ResultMetadata["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", out.ResultMetadata)
	}
}

func TestDisplayTableToolDefinitionIncludesStructuredChartSpecSchema(t *testing.T) {
	def := newDisplayTableTool().Definition()
	params := def.Function.Parameters
	props, ok := params["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters.properties type = %T, want map[string]any", params["properties"])
	}
	chartSpec, ok := props["chart_spec"].(map[string]any)
	if !ok {
		t.Fatalf("chart_spec schema missing: %#v", props["chart_spec"])
	}
	if chartSpec["type"] != "object" {
		t.Fatalf("chart_spec.type=%#v, want object", chartSpec["type"])
	}
	chartSpecProps, ok := chartSpec["properties"].(map[string]any)
	if !ok {
		t.Fatalf("chart_spec.properties type = %T, want map[string]any", chartSpec["properties"])
	}
	options, ok := chartSpecProps["options"].(map[string]any)
	if !ok {
		t.Fatalf("chart_spec.options schema missing: %#v", chartSpecProps["options"])
	}
	if options["type"] != "object" {
		t.Fatalf("chart_spec.options.type=%#v, want object", options["type"])
	}
	if ap, ok := options["additionalProperties"].(bool); !ok || !ap {
		t.Fatalf("chart_spec.options.additionalProperties=%#v, want true", options["additionalProperties"])
	}

	echartsSpec, ok := props["echarts_spec"].(map[string]any)
	if !ok {
		t.Fatalf("echarts_spec schema missing: %#v", props["echarts_spec"])
	}
	if echartsSpec["type"] != "object" {
		t.Fatalf("echarts_spec.type=%#v, want object", echartsSpec["type"])
	}
	echartsSpecProps, ok := echartsSpec["properties"].(map[string]any)
	if !ok {
		t.Fatalf("echarts_spec.properties type = %T, want map[string]any", echartsSpec["properties"])
	}
	moduleSchema, ok := echartsSpecProps["module"].(map[string]any)
	if !ok {
		t.Fatalf("echarts_spec.module schema missing: %#v", echartsSpecProps["module"])
	}
	rawEnum, ok := moduleSchema["enum"].([]string)
	if !ok {
		rawAny, okAny := moduleSchema["enum"].([]any)
		if !okAny {
			t.Fatalf("echarts_spec.module.enum type = %T, want []string or []any", moduleSchema["enum"])
		}
		rawEnum = make([]string, 0, len(rawAny))
		for _, entry := range rawAny {
			rawEnum = append(rawEnum, strings.TrimSpace(anyToString(entry)))
		}
	}
	if !slicesContains(rawEnum, "all") {
		t.Fatalf("echarts_spec.module.enum=%#v, want to include \"all\"", rawEnum)
	}

	sourceSchema, ok := props["source"].(map[string]any)
	if !ok {
		t.Fatalf("source schema missing: %#v", props["source"])
	}
	if sourceSchema["type"] != "object" {
		t.Fatalf("source.type=%#v, want object", sourceSchema["type"])
	}
	if ap, ok := sourceSchema["additionalProperties"].(bool); !ok || ap {
		t.Fatalf("source.additionalProperties=%#v, want false", sourceSchema["additionalProperties"])
	}
}

func slicesContains(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(needle)) {
			return true
		}
	}
	return false
}

func intFromAnyDataTool(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float32:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}
