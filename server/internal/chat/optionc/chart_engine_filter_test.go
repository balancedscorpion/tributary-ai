package optionc

import (
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

func chartToolNames(defs []types.ToolDefinition) []string {
	out := make([]string, 0, len(defs))
	for _, def := range defs {
		if def.Function.Name == "" {
			continue
		}
		out = append(out, def.Function.Name)
	}
	return out
}

func hasTool(defs []types.ToolDefinition, name string) bool {
	for _, def := range defs {
		if def.Function.Name == name {
			return true
		}
	}
	return false
}

func findToolDefinition(defs []types.ToolDefinition, name string) (types.ToolDefinition, bool) {
	for _, def := range defs {
		if def.Function.Name == name {
			return def, true
		}
	}
	return types.ToolDefinition{}, false
}

func TestChartEngineEnablementFromSettingsDefaults(t *testing.T) {
	got := chartEngineEnablementFromSettings(config.AppSettings{})
	if !got.Echarts || got.Highcharts || !got.ChartProgramPipeline || got.ChartDimensionFilters {
		t.Fatalf("defaults = %#v, want echarts=true highcharts=false chart_program_pipeline=true chart_dimension_filters=false", got)
	}
}

func TestFilterToolDefinitionsByChartEnginesShapesDisplayTable(t *testing.T) {
	registry := tools.NewRegistry(nil)
	defs := registry.AnalysisCoreDefinitions()

	filtered := filterToolDefinitionsByChartEngines(defs, chartEngineEnablement{
		Echarts:    true,
		Highcharts: false,
	})
	if hasTool(filtered, toolGetHighchartsCapabilities) {
		t.Fatalf("%s should be filtered out when highcharts is disabled", toolGetHighchartsCapabilities)
	}
	if !hasTool(filtered, toolGetEchartsCapabilities) {
		t.Fatalf("%s should be present when echarts is enabled", toolGetEchartsCapabilities)
	}

	displayDef, ok := findToolDefinition(filtered, toolDisplayTable)
	if !ok {
		t.Fatalf("missing %s in filtered definitions: %#v", toolDisplayTable, chartToolNames(filtered))
	}
	if !containsAll(displayDef.Function.Description, "exactly one data source", "source (artifact/path/job)") {
		t.Fatalf("display_table description missing source contract guidance: %q", displayDef.Function.Description)
	}
	props, _ := displayDef.Function.Parameters["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("display_table properties missing")
	}
	if _, exists := props["chart_spec"]; exists {
		t.Fatalf("chart_spec should be removed when highcharts is disabled")
	}
	if _, exists := props["echarts_spec"]; !exists {
		t.Fatalf("echarts_spec should remain when echarts is enabled")
	}
	if _, exists := props["filters"]; exists {
		t.Fatalf("filters should be removed when dimension filters are disabled")
	}
}

func TestFilterToolDefinitionsByChartEnginesBothOff(t *testing.T) {
	registry := tools.NewRegistry(nil)
	defs := registry.AnalysisCoreDefinitions()
	filtered := filterToolDefinitionsByChartEngines(defs, chartEngineEnablement{})

	if hasTool(filtered, toolGetHighchartsCapabilities) || hasTool(filtered, toolGetEchartsCapabilities) {
		t.Fatalf("chart capability tools must be removed when both engines are disabled")
	}
	displayDef, ok := findToolDefinition(filtered, toolDisplayTable)
	if !ok {
		t.Fatalf("missing %s in filtered definitions", toolDisplayTable)
	}
	if !containsAll(displayDef.Function.Description, "exactly one data source", "source (artifact/path/job)") {
		t.Fatalf("display_table description missing source contract guidance: %q", displayDef.Function.Description)
	}
	props, _ := displayDef.Function.Parameters["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("display_table properties missing")
	}
	if _, exists := props["chart_spec"]; exists {
		t.Fatalf("chart_spec should be removed in table-only mode")
	}
	if _, exists := props["echarts_spec"]; exists {
		t.Fatalf("echarts_spec should be removed in table-only mode")
	}
	if _, exists := props["filters"]; exists {
		t.Fatalf("filters should be removed in table-only mode")
	}
}

func TestFilterToolDefinitionsByChartEnginesRespectsChartProgramPipelineFlag(t *testing.T) {
	registry := tools.NewRegistry(nil)
	defs := registry.AnalysisCoreDefinitions()
	filtered := filterToolDefinitionsByChartEngines(defs, chartEngineEnablement{
		Echarts:              true,
		Highcharts:           false,
		ChartProgramPipeline: false,
	})

	if hasTool(filtered, toolBuildChartArtifact) {
		t.Fatalf("%s should be filtered out when chart program pipeline is disabled", toolBuildChartArtifact)
	}
	if !hasTool(filtered, toolDisplayTable) {
		t.Fatalf("%s should still be available when chart program pipeline is disabled", toolDisplayTable)
	}
}

func TestFilterToolDefinitionsByChartEnginesKeepsFilterParamsWhenEnabled(t *testing.T) {
	registry := tools.NewRegistry(nil)
	defs := registry.AnalysisCoreDefinitions()
	filtered := filterToolDefinitionsByChartEngines(defs, chartEngineEnablement{
		Echarts:               true,
		Highcharts:            false,
		ChartProgramPipeline:  true,
		ChartDimensionFilters: true,
	})

	displayDef, ok := findToolDefinition(filtered, toolDisplayTable)
	if !ok {
		t.Fatalf("missing %s in filtered definitions", toolDisplayTable)
	}
	displayProps, _ := displayDef.Function.Parameters["properties"].(map[string]any)
	if displayProps == nil {
		t.Fatalf("display_table properties missing")
	}
	if _, exists := displayProps["filters"]; !exists {
		t.Fatalf("filters should remain when dimension filters are enabled")
	}

	buildDef, ok := findToolDefinition(filtered, toolBuildChartArtifact)
	if !ok {
		t.Fatalf("missing %s in filtered definitions", toolBuildChartArtifact)
	}
	buildProps, _ := buildDef.Function.Parameters["properties"].(map[string]any)
	if buildProps == nil {
		t.Fatalf("build_chart_artifact properties missing")
	}
	transformProps, _ := buildProps["transform"].(map[string]any)
	if transformProps == nil {
		t.Fatalf("build_chart_artifact.transform properties missing")
	}
	transformFields, _ := transformProps["properties"].(map[string]any)
	if transformFields == nil {
		t.Fatalf("build_chart_artifact.transform.properties missing")
	}
	if _, exists := transformFields["filters"]; !exists {
		t.Fatalf("build_chart_artifact.transform.filters should remain when dimension filters are enabled")
	}
}

func TestFilterToolDefinitionsByChartEnginesRemovesBuildChartArtifactFiltersWhenDisabled(t *testing.T) {
	registry := tools.NewRegistry(nil)
	defs := registry.AnalysisCoreDefinitions()
	filtered := filterToolDefinitionsByChartEngines(defs, chartEngineEnablement{
		Echarts:               true,
		Highcharts:            false,
		ChartProgramPipeline:  true,
		ChartDimensionFilters: false,
	})

	buildDef, ok := findToolDefinition(filtered, toolBuildChartArtifact)
	if !ok {
		t.Fatalf("missing %s in filtered definitions", toolBuildChartArtifact)
	}
	buildProps, _ := buildDef.Function.Parameters["properties"].(map[string]any)
	if buildProps == nil {
		t.Fatalf("build_chart_artifact properties missing")
	}
	transformProps, _ := buildProps["transform"].(map[string]any)
	if transformProps == nil {
		t.Fatalf("build_chart_artifact.transform properties missing")
	}
	transformFields, _ := transformProps["properties"].(map[string]any)
	if transformFields == nil {
		t.Fatalf("build_chart_artifact.transform.properties missing")
	}
	if _, exists := transformFields["filters"]; exists {
		t.Fatalf("build_chart_artifact.transform.filters should be removed when dimension filters are disabled")
	}
}

func containsAll(text string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
