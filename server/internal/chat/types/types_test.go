package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolResultEnvelopeIncludesPlotSpec(t *testing.T) {
	raw := `{
		"artifact_id":"artifact-1",
		"tool_name":"display_table",
		"ok":true,
		"result_format":"json",
		"result":{"rows":[{"x":1}]},
		"plot_spec":{
			"schema_version":"highcharts-plot-spec.v1",
			"module":"core",
			"options":{"series":[{"type":"line","data":[1,2]}]}
		},
		"chart_engine":"highcharts",
		"chart_spec_version":"hc-cap-v1-abc",
		"chart_modules":["drilldown","treemap"]
	}`
	env, ok := ParseToolResultEnvelope(raw)
	if !ok {
		t.Fatalf("ParseToolResultEnvelope returned not-ok")
	}
	if strings.TrimSpace(env.ArtifactID) != "artifact-1" {
		t.Fatalf("ArtifactID=%q", env.ArtifactID)
	}
	if strings.TrimSpace(env.ChartEngine) != "highcharts" {
		t.Fatalf("ChartEngine=%q, want highcharts", env.ChartEngine)
	}
	if strings.TrimSpace(env.ChartSpecVersion) != "hc-cap-v1-abc" {
		t.Fatalf("ChartSpecVersion=%q", env.ChartSpecVersion)
	}
	if len(env.ChartModules) != 2 || env.ChartModules[0] != "drilldown" || env.ChartModules[1] != "treemap" {
		t.Fatalf("ChartModules=%#v, want [drilldown treemap]", env.ChartModules)
	}
	if env.PlotSpec == nil || len(env.PlotSpec) == 0 {
		t.Fatalf("PlotSpec missing: %#v", env)
	}
}

func TestCompactToolResultPayloadPreservesPlotSpec(t *testing.T) {
	raw := `{
		"artifact_id":"artifact-1",
		"tool_name":"display_table",
		"ok":true,
		"result_format":"json",
		"result":{"rows":[{"x":1}]},
		"plot_spec":{
			"schema_version":"highcharts-plot-spec.v1",
			"module":"core",
			"options":{"series":[{"type":"line","data":[1,2]}]}
		},
		"chart_engine":"highcharts",
		"chart_spec_version":"hc-cap-v1-abc",
		"chart_modules":["drilldown","treemap"]
	}`

	compact, compacted := CompactToolResultPayload(raw)
	if !compacted {
		t.Fatalf("expected payload to be recognized as tool envelope")
	}
	env, ok := ParseToolResultEnvelope(compact)
	if !ok {
		t.Fatalf("ParseToolResultEnvelope(compact) returned not-ok")
	}
	if env.PlotSpec == nil || len(env.PlotSpec) == 0 {
		t.Fatalf("PlotSpec missing after compaction: %s", compact)
	}
	if strings.TrimSpace(env.ChartEngine) != "highcharts" {
		t.Fatalf("ChartEngine=%q, want highcharts", env.ChartEngine)
	}
	if strings.TrimSpace(env.ChartSpecVersion) != "hc-cap-v1-abc" {
		t.Fatalf("ChartSpecVersion=%q", env.ChartSpecVersion)
	}
	if len(env.ChartModules) != 2 || env.ChartModules[0] != "drilldown" || env.ChartModules[1] != "treemap" {
		t.Fatalf("ChartModules=%#v, want [drilldown treemap]", env.ChartModules)
	}
}

func TestCompactToolResultPayloadPreservesToolPresentation(t *testing.T) {
	raw := `{
		"artifact_id":"artifact-1",
		"tool_name":"sharepoint_search",
		"ok":true,
		"result_format":"json",
		"result":{"items":[{"name":"Roadmap.pdf","size":12345}]},
		"result_metadata":{
			"integration":"sharepoint",
			"renderer_strategy":"family",
			"tool_presentation":{"native_viewer":"connector_inspector"}
		},
		"tool_presentation":{
			"family":"sharepoint",
			"result_kind":"connector",
			"renderer_strategy":"family",
			"renderer_key":"sharepoint",
			"native_viewer":"connector_inspector"
		}
	}`

	compact, compacted := CompactToolResultPayload(raw)
	if !compacted {
		t.Fatalf("expected payload to be recognized as tool envelope")
	}
	env, ok := ParseToolResultEnvelope(compact)
	if !ok {
		t.Fatalf("ParseToolResultEnvelope(compact) returned not-ok")
	}
	if env.ToolPresentation == nil || len(env.ToolPresentation) == 0 {
		t.Fatalf("ToolPresentation missing after compaction: %s", compact)
	}
	if got, _ := env.ToolPresentation["native_viewer"].(string); got != "connector_inspector" {
		t.Fatalf("ToolPresentation native_viewer=%q, want connector_inspector", got)
	}
	if env.ResultMetadata == nil {
		t.Fatalf("ResultMetadata missing after compaction: %s", compact)
	}
}

func TestCompactToolResultPayloadDoesNotBypassOnFullDataRequested(t *testing.T) {
	raw := `{
		"artifact_id":"artifact-2",
		"tool_name":"run_bigquery_query",
		"ok":true,
		"result_format":"json",
		"query_metadata":{"full_data":true},
		"result":{"rows":[{"x":0},{"x":1},{"x":2},{"x":3},{"x":4},{"x":5},{"x":6},{"x":7},{"x":8},{"x":9},{"x":10},{"x":11}]}
	}`

	compact, compacted := CompactToolResultPayload(raw)
	if !compacted {
		t.Fatalf("expected payload to be recognized as tool envelope")
	}
	env, ok := ParseToolResultEnvelope(compact)
	if !ok {
		t.Fatalf("ParseToolResultEnvelope(compact) returned not-ok")
	}
	resultMap, ok := env.Result.(map[string]any)
	if !ok {
		t.Fatalf("compacted result type=%T, want map[string]any", env.Result)
	}
	rowsAny, ok := resultMap["rows"].([]any)
	if !ok {
		t.Fatalf("compacted rows type=%T, want []any", resultMap["rows"])
	}
	if len(rowsAny) >= 12 {
		t.Fatalf("expected compacted rows length < 12, got %d", len(rowsAny))
	}
	encoded, err := json.Marshal(resultMap)
	if err != nil {
		t.Fatalf("marshal compacted result: %v", err)
	}
	if strings.Contains(string(encoded), `"x":11`) {
		t.Fatalf("expected high-index rows to be compacted out: %s", string(encoded))
	}
}

func TestNormalizeResponseMetaUsagePreservesMissingAndMarksDerivedTotals(t *testing.T) {
	missing := NormalizeResponseMetaUsage(ResponseMeta{ProviderID: "test", Model: "model"})
	if missing.InputTokens != nil || missing.OutputTokens != nil || missing.TotalTokens != nil {
		t.Fatalf("missing usage was converted to a value: %#v", missing)
	}
	if missing.UsageQuality != UsageQualityMissing {
		t.Fatalf("missing usage quality = %q, want %q", missing.UsageQuality, UsageQualityMissing)
	}

	input, output := 7, 3
	generic := NormalizeResponseMetaUsage(ResponseMeta{InputTokens: &input, OutputTokens: &output})
	if generic.TotalTokens != nil || generic.DerivedTotalTokens != nil {
		t.Fatalf("generic usage unexpectedly derived a total: %#v", generic)
	}
	derived := NormalizeResponseMetaUsageForSemantics(ResponseMeta{InputTokens: &input, OutputTokens: &output}, UsageSemanticsOpenAI)
	if derived.TotalTokens == nil || *derived.TotalTokens != 10 || derived.ProviderTotalTokens != nil || derived.DerivedTotalTokens == nil || *derived.DerivedTotalTokens != 10 {
		t.Fatalf("derived total = %#v, want 10", derived.TotalTokens)
	}
	if derived.UsageQuality != UsageQualityDerived {
		t.Fatalf("derived usage quality = %q, want %q", derived.UsageQuality, UsageQualityDerived)
	}

	reportedTotal := 12
	reported := NormalizeResponseMetaUsageForSemantics(ResponseMeta{InputTokens: &input, OutputTokens: &output, TotalTokens: &reportedTotal}, UsageSemanticsOpenAI)
	if reported.TotalTokens == nil || *reported.TotalTokens != 12 || reported.ProviderTotalTokens == nil || *reported.ProviderTotalTokens != 12 || reported.DerivedTotalTokens == nil || *reported.DerivedTotalTokens != 10 || reported.UsageQuality != UsageQualityProviderReported {
		t.Fatalf("provider total was not preserved: %#v", reported)
	}

	zero := 0
	for _, semantics := range []UsageSemantics{UsageSemanticsAnthropic, UsageSemanticsBedrockConverse, UsageSemanticsGemini} {
		meta := NormalizeResponseMetaUsageForSemantics(ResponseMeta{
			InputTokens: &input, OutputTokens: &output, CachedInputTokens: &zero, CacheWriteTokens: &zero, ReasoningTokens: &zero,
		}, semantics)
		if meta.ProviderTotalTokens != nil || meta.DerivedTotalTokens == nil || meta.UsageQuality != UsageQualityDerived {
			t.Fatalf("%s derived usage was promoted to provider total: %#v", semantics, meta)
		}
	}
}
