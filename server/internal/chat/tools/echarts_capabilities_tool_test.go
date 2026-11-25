package tools

import (
	"context"
	"testing"
)

func TestGetEchartsCapabilitiesToolReturnsSnapshot(t *testing.T) {
	tool := newGetEchartsCapabilitiesTool()
	out, err := tool.Execute(context.Background(), ExecContext{}, []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got: %s", toolErrorMessage(t, out))
	}
	requireChartCapabilityResultMetadata(t, out, "get_echarts_capabilities", "echarts")
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %#v", out.Result)
	}

	if got := anyToString(payload["schema_version"]); got != "echarts-capabilities.v1" {
		t.Fatalf("schema_version=%q, want echarts-capabilities.v1", got)
	}
	if got := anyToString(payload["chart_spec_version"]); got != EchartsPlotSpecVersionV1 {
		t.Fatalf("chart_spec_version=%q, want %q", got, EchartsPlotSpecVersionV1)
	}
	if got := anyToString(payload["capability_version"]); got == "" {
		t.Fatalf("capability_version is empty: %#v", payload)
	}
	if got := anyToString(payload["echarts_version"]); got == "" {
		t.Fatalf("echarts_version is empty: %#v", payload)
	}
	if modules, ok := payload["supported_modules"].([]any); !ok || len(modules) == 0 {
		t.Fatalf("supported_modules=%#v, want non-empty list", payload["supported_modules"])
	}
	if chartExports, ok := payload["supported_chart_exports"].([]any); !ok || len(chartExports) == 0 {
		t.Fatalf("supported_chart_exports=%#v, want non-empty list", payload["supported_chart_exports"])
	}
	if seriesGlobal, ok := payload["supported_series_types_global"].([]any); !ok || len(seriesGlobal) == 0 {
		t.Fatalf("supported_series_types_global=%#v, want non-empty list", payload["supported_series_types_global"])
	}
	callbackKinds, ok := payload["callback_ids_by_kind"].(map[string]any)
	if !ok || len(callbackKinds) == 0 {
		t.Fatalf("callback_ids_by_kind=%#v, want non-empty object", payload["callback_ids_by_kind"])
	}
	if _, ok := callbackKinds["series.custom.render_item"]; !ok {
		t.Fatalf("callback_ids_by_kind missing series.custom.render_item: %#v", callbackKinds)
	}
}
