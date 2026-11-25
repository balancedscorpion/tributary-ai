package tools

import (
	"context"
	"testing"
)

func TestGetHighchartsCapabilitiesToolReturnsSnapshot(t *testing.T) {
	tool := newGetHighchartsCapabilitiesTool()
	out, err := tool.Execute(context.Background(), ExecContext{}, []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute returned unexpected error: %v", err)
	}
	if !out.OK {
		t.Fatalf("expected success, got: %s", toolErrorMessage(t, out))
	}
	requireChartCapabilityResultMetadata(t, out, "get_highcharts_capabilities", "highcharts")
	payload, ok := out.Result.(map[string]any)
	if !ok {
		t.Fatalf("unexpected payload type: %#v", out.Result)
	}

	if got := anyToString(payload["schema_version"]); got != "highcharts-capabilities.v2" {
		t.Fatalf("schema_version=%q, want highcharts-capabilities.v2", got)
	}
	if got := anyToString(payload["chart_spec_version"]); got != HighchartsPlotSpecVersionV2 {
		t.Fatalf("chart_spec_version=%q, want %q", got, HighchartsPlotSpecVersionV2)
	}
	if got := anyToString(payload["capability_version"]); got == "" {
		t.Fatalf("capability_version is empty: %#v", payload)
	}
	if got := anyToString(payload["highcharts_version"]); got == "" {
		t.Fatalf("highcharts_version is empty: %#v", payload)
	}
	if versions, ok := payload["chart_spec_versions"].([]any); !ok || len(versions) < 2 {
		t.Fatalf("chart_spec_versions=%#v, want v1+v2", payload["chart_spec_versions"])
	}
	if constructors, ok := payload["constructors"].(map[string]any); !ok || len(constructors) < 4 {
		t.Fatalf("constructors=%#v, want core/stock/maps/gantt", payload["constructors"])
	}
	callbackKinds, ok := payload["callback_ids_by_kind"].(map[string]any)
	if !ok || len(callbackKinds) == 0 {
		t.Fatalf("callback_ids_by_kind=%#v, want non-empty object", payload["callback_ids_by_kind"])
	}
}
