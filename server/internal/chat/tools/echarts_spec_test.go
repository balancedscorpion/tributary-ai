package tools

import (
	"sort"
	"strings"
	"testing"
)

func mustEchartsCapabilitiesContext(t *testing.T) echartsCapabilitiesContext {
	t.Helper()
	snapshot, err := loadEchartsCapabilitiesSnapshot()
	if err != nil {
		t.Fatalf("loadEchartsCapabilitiesSnapshot: %v", err)
	}
	return buildEchartsCapabilitiesContext(snapshot)
}

func seriesTemplateForType(seriesType string) map[string]any {
	switch seriesType {
	case "graph":
		return map[string]any{
			"type": "graph",
			"nodes": []any{
				map[string]any{"id": "n1", "name": "A"},
				map[string]any{"id": "n2", "name": "B"},
			},
			"links": []any{
				map[string]any{"source": "n1", "target": "n2"},
			},
		}
	case "tree":
		return map[string]any{
			"type": "tree",
			"data": []any{
				map[string]any{"name": "root", "value": 1},
			},
		}
	case "sankey":
		return map[string]any{
			"type": "sankey",
			"data": []any{
				map[string]any{"name": "A"},
				map[string]any{"name": "B"},
			},
			"links": []any{
				map[string]any{"source": "A", "target": "B", "value": 1},
			},
		}
	case "custom":
		return map[string]any{
			"type":           "custom",
			"render_item_id": "custom_render_rect",
			"data":           []any{[]any{0, 1}, []any{1, 2}},
		}
	case "scatter", "effectscatter":
		return map[string]any{
			"type": seriesType,
			"data": []any{
				[]any{1, 2},
				[]any{3, 4},
			},
		}
	default:
		return map[string]any{
			"type": seriesType,
			"data": []any{1, 2},
		}
	}
}

func TestNormalizeEchartsPlotSpecAcceptsAllSeriesTypesFromSnapshot(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	allowed := make([]string, 0, len(ctx.SupportedSeriesByModule["all"]))
	for seriesType := range ctx.SupportedSeriesByModule["all"] {
		allowed = append(allowed, seriesType)
	}
	sort.Strings(allowed)
	if len(allowed) == 0 {
		t.Fatalf("supported series for module all is empty")
	}

	for _, seriesType := range allowed {
		seriesType := seriesType
		t.Run(seriesType, func(t *testing.T) {
			raw := map[string]any{
				"schema_version": "echarts-plot-spec.v1",
				"module":         "all",
				"options": map[string]any{
					"title": map[string]any{"text": "Coverage"},
					"series": []any{
						seriesTemplateForType(seriesType),
					},
				},
			}

			normalized, module, _, err := normalizeEchartsPlotSpec(raw, ctx)
			if err != nil {
				t.Fatalf("normalizeEchartsPlotSpec returned error: %v", err)
			}
			if module != "all" {
				t.Fatalf("module=%q, want all", module)
			}
			if got := strings.TrimSpace(anyToString(normalized["schema_version"])); got != EchartsPlotSpecVersionV1 {
				t.Fatalf("schema_version=%q, want %q", got, EchartsPlotSpecVersionV1)
			}
		})
	}
}

func TestNormalizeEchartsPlotSpecRejectsUnsupportedSeriesType(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "core",
		"options": map[string]any{
			"series": []any{
				map[string]any{
					"type": "map",
					"data": []any{1, 2},
				},
			},
		},
	}

	_, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected unsupported series type error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "series") {
		t.Fatalf("error %q does not mention series", err)
	}
}

func TestNormalizeEchartsPlotSpecRejectsUnknownTooltipFormatterID(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"tooltip": map[string]any{
				"formatter_id": "not_allowed",
			},
			"series": []any{
				map[string]any{
					"type": "line",
					"data": []any{1, 2},
				},
			},
		},
	}

	_, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected unsupported formatter id error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "formatter") {
		t.Fatalf("error %q does not mention formatter", err)
	}
}

func TestNormalizeEchartsPlotSpecRejectsUnknownDrilldownLoader(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"series": []any{
				map[string]any{
					"type": "line",
					"data": []any{1, 2},
				},
			},
		},
		"extensions": map[string]any{
			"drilldown_async": map[string]any{
				"loader_id": "custom_loader",
				"series_by_id": map[string]any{
					"detail": map[string]any{
						"type": "line",
						"data": []any{3, 4},
					},
				},
			},
		},
	}

	_, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected unknown drilldown loader error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "loader") {
		t.Fatalf("error %q does not mention loader", err)
	}
}

func TestNormalizeEchartsPlotSpecCustomSeriesRequiresRenderItemID(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"series": []any{
				map[string]any{
					"type": "custom",
					"data": []any{[]any{0, 1}},
				},
			},
		},
	}

	_, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected missing render_item_id error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "render_item_id") {
		t.Fatalf("error %q does not mention render_item_id", err)
	}
}

func TestNormalizeEchartsPlotSpecRejectsCustomRenderItemFunctionPayload(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"series": []any{
				map[string]any{
					"type":       "custom",
					"renderItem": "function(params, api){ return {}; }",
					"data":       []any{[]any{0, 1}},
				},
			},
		},
	}

	_, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected renderItem rejection error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "executable") && !strings.Contains(strings.ToLower(err.Error()), "renderitem") {
		t.Fatalf("error %q does not mention executable/renderItem policy", err)
	}
}

func TestNormalizeEchartsPlotSpecLineDataNormalizationDropsInvalidPoints(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"xAxis": map[string]any{"type": "category"},
			"yAxis": map[string]any{"type": "value"},
			"series": []any{
				map[string]any{
					"type":      "line",
					"stack":     "total",
					"areaStyle": map[string]any{},
					"data": []any{
						map[string]any{"x": "2024-01", "y": "10"},
						map[string]any{"value": []any{"2024-02", "11"}},
						[]any{"2024-03", "12"},
						map[string]any{"value": "13"},
						map[string]any{"value": "not-a-number"},
						map[string]any{"x": "2024-04"},
						"14",
						map[string]any{"value": nil},
						[]any{"2024-05", "NaN"},
					},
				},
			},
		},
	}

	normalized, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err != nil {
		t.Fatalf("normalizeEchartsPlotSpec returned error: %v", err)
	}

	options, ok := normalized["options"].(map[string]any)
	if !ok {
		t.Fatalf("normalized options missing: %#v", normalized)
	}
	seriesList, ok := options["series"].([]any)
	if !ok || len(seriesList) != 1 {
		t.Fatalf("normalized series missing: %#v", options["series"])
	}
	series, ok := seriesList[0].(map[string]any)
	if !ok {
		t.Fatalf("normalized series[0] is not an object: %#v", seriesList[0])
	}
	data, ok := series["data"].([]any)
	if !ok {
		t.Fatalf("normalized series data missing: %#v", series["data"])
	}
	if len(data) != 6 {
		t.Fatalf("expected 6 normalized points, got %d (%#v)", len(data), data)
	}

	firstPoint, ok := data[0].(map[string]any)
	if !ok {
		t.Fatalf("expected first point object, got %#v", data[0])
	}
	valueTuple, ok := firstPoint["value"].([]any)
	if !ok || len(valueTuple) != 2 {
		t.Fatalf("expected first point value tuple, got %#v", firstPoint["value"])
	}
	if valueTuple[0] != "2024-01" {
		t.Fatalf("expected first x label to remain string category, got %#v", valueTuple[0])
	}
	if numeric, ok := coerceToFiniteNumber(valueTuple[1]); !ok || numeric != 10 {
		t.Fatalf("expected first y value to be normalized to numeric 10, got %#v", valueTuple[1])
	}
}

func TestNormalizeEchartsPlotSpecScatterDataNormalizationDropsInvalidPoints(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"xAxis": map[string]any{"type": "value"},
			"yAxis": map[string]any{"type": "value"},
			"series": []any{
				map[string]any{
					"type": "scatter",
					"data": []any{
						[]any{"1.5", "2"},
						[]any{"bad", "3"},
						map[string]any{"x": "4", "y": "5"},
						map[string]any{"value": []any{"6", "7", "8"}},
						map[string]any{"value": []any{"x", "7"}},
						map[string]any{"value": []any{"8"}},
					},
				},
			},
		},
	}

	normalized, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err != nil {
		t.Fatalf("normalizeEchartsPlotSpec returned error: %v", err)
	}

	options := normalized["options"].(map[string]any)
	series := options["series"].([]any)[0].(map[string]any)
	data := series["data"].([]any)
	if len(data) != 3 {
		t.Fatalf("expected 3 normalized scatter points, got %d (%#v)", len(data), data)
	}

	for idx, point := range data {
		switch typed := point.(type) {
		case []any:
			if len(typed) < 2 {
				t.Fatalf("data[%d] tuple is too short: %#v", idx, typed)
			}
			if _, ok := coerceToFiniteNumber(typed[0]); !ok {
				t.Fatalf("data[%d] x is not numeric: %#v", idx, typed[0])
			}
			if _, ok := coerceToFiniteNumber(typed[1]); !ok {
				t.Fatalf("data[%d] y is not numeric: %#v", idx, typed[1])
			}
		case map[string]any:
			valueTuple, ok := typed["value"].([]any)
			if !ok || len(valueTuple) < 2 {
				t.Fatalf("data[%d].value tuple invalid: %#v", idx, typed["value"])
			}
			if _, ok := coerceToFiniteNumber(valueTuple[0]); !ok {
				t.Fatalf("data[%d].value[0] x is not numeric: %#v", idx, valueTuple[0])
			}
			if _, ok := coerceToFiniteNumber(valueTuple[1]); !ok {
				t.Fatalf("data[%d].value[1] y is not numeric: %#v", idx, valueTuple[1])
			}
		default:
			t.Fatalf("unexpected data[%d] point shape: %#v", idx, point)
		}
	}
}

func TestNormalizeEchartsPlotSpecRejectsWhenNoPointsSurviveNormalization(t *testing.T) {
	ctx := mustEchartsCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "echarts-plot-spec.v1",
		"module":         "all",
		"options": map[string]any{
			"series": []any{
				map[string]any{
					"type": "line",
					"data": []any{
						map[string]any{"value": "not-a-number"},
						[]any{"bucket", "nan"},
						map[string]any{"x": "bucket-only"},
					},
				},
			},
		},
	}

	_, _, _, err := normalizeEchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected no-valid-points normalization error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no valid points after normalization") {
		t.Fatalf("unexpected error: %v", err)
	}
}
