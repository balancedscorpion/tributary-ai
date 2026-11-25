package tools

import (
	"strings"
	"testing"
)

func mustCapabilitiesContext(t *testing.T) highchartsCapabilitiesContext {
	t.Helper()
	snapshot, err := loadHighchartsCapabilitiesSnapshot()
	if err != nil {
		t.Fatalf("loadHighchartsCapabilitiesSnapshot: %v", err)
	}
	return buildHighchartsCapabilitiesContext(snapshot)
}

func TestNormalizeHighchartsPlotSpecV2MapsWithExtensions(t *testing.T) {
	ctx := mustCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "highcharts-plot-spec.v2",
		"module":         "maps",
		"modules":        []any{"drilldown", "sunburst"},
		"options": map[string]any{
			"title": map[string]any{"text": "Coverage"},
			"series": []any{
				map[string]any{
					"type": "map",
					"mapData": map[string]any{
						"type":     "FeatureCollection",
						"features": []any{},
					},
					"data": []any{map[string]any{"hc-key": "a", "value": 1, "drilldown": "a_detail"}},
				},
			},
		},
		"extensions": map[string]any{
			"drilldown_async": map[string]any{
				"loader_id": "drilldown_embedded_series",
				"series_by_id": map[string]any{
					"a_detail": map[string]any{
						"type": "column",
						"data": []any{1, 2, 3},
					},
				},
				"max_depth": 4,
			},
		},
	}

	normalized, module, modules, err := normalizeHighchartsPlotSpec(raw, ctx)
	if err != nil {
		t.Fatalf("normalizeHighchartsPlotSpec returned error: %v", err)
	}
	if module != "maps" {
		t.Fatalf("module=%q, want maps", module)
	}
	if got := strings.TrimSpace(anyToString(normalized["schema_version"])); got != HighchartsPlotSpecVersionV2 {
		t.Fatalf("schema_version=%q, want %q", got, HighchartsPlotSpecVersionV2)
	}
	if len(modules) == 0 {
		t.Fatalf("modules not resolved: %#v", modules)
	}
	if ext, ok := normalized["extensions"].(map[string]any); !ok || len(ext) == 0 {
		t.Fatalf("extensions missing from normalized spec: %#v", normalized)
	}
}

func TestNormalizeHighchartsPlotSpecV2RejectsUnsupportedExtensionModule(t *testing.T) {
	ctx := mustCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "highcharts-plot-spec.v2",
		"module":         "core",
		"modules":        []any{"ad-indicator"},
		"options": map[string]any{
			"series": []any{map[string]any{"type": "line", "data": []any{1, 2}}},
		},
	}

	_, _, _, err := normalizeHighchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected unsupported extension module error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "modules") {
		t.Fatalf("error %q does not mention modules", err)
	}
}

func TestNormalizeHighchartsPlotSpecV2RejectsExternalMapURL(t *testing.T) {
	ctx := mustCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "highcharts-plot-spec.v2",
		"module":         "maps",
		"options": map[string]any{
			"series": []any{
				map[string]any{
					"type":    "map",
					"mapData": "https://code.highcharts.com/map.geo.json",
					"data":    []any{map[string]any{"hc-key": "a", "value": 1}},
				},
			},
		},
	}

	_, _, _, err := normalizeHighchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected map URL validation error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "map") {
		t.Fatalf("error %q does not mention map policy", err)
	}
}

func TestNormalizeHighchartsPlotSpecV2RejectsUnknownDrilldownLoader(t *testing.T) {
	ctx := mustCapabilitiesContext(t)
	raw := map[string]any{
		"schema_version": "highcharts-plot-spec.v2",
		"module":         "core",
		"options": map[string]any{
			"series": []any{map[string]any{"type": "line", "data": []any{1, 2}}},
		},
		"extensions": map[string]any{
			"drilldown_async": map[string]any{
				"loader_id": "custom_loader",
				"series_by_id": map[string]any{
					"foo": map[string]any{"type": "line", "data": []any{3, 4}},
				},
			},
		},
	}

	_, _, _, err := normalizeHighchartsPlotSpec(raw, ctx)
	if err == nil {
		t.Fatalf("expected unknown drilldown loader error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "loader") {
		t.Fatalf("error %q does not mention loader", err)
	}
}
