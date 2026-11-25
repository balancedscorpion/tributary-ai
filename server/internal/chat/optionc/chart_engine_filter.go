package optionc

import (
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
	"ai-analyst-local/internal/config"
)

const (
	toolGetHighchartsCapabilities = "get_highcharts_capabilities"
	toolGetEchartsCapabilities    = "get_echarts_capabilities"
	toolBuildChartArtifact        = "build_chart_artifact"
	toolDisplayTable              = "display_table"
)

type chartEngineEnablement struct {
	Echarts               bool
	Highcharts            bool
	ChartProgramPipeline  bool
	ChartDimensionFilters bool
}

func defaultChartEngineEnablement() chartEngineEnablement {
	return chartEngineEnablement{
		Echarts:               true,
		Highcharts:            false,
		ChartProgramPipeline:  true,
		ChartDimensionFilters: false,
	}
}

func chartEngineEnablementFromSettings(settings config.AppSettings) chartEngineEnablement {
	out := defaultChartEngineEnablement()
	if settings.FeatureFlags == nil {
		return out
	}
	if settings.FeatureFlags.ChartEngines != nil {
		out.Echarts = settings.FeatureFlags.ChartEngines.Echarts
		out.Highcharts = settings.FeatureFlags.ChartEngines.Highcharts
	}
	if settings.FeatureFlags.ChartProgramPipeline != nil {
		out.ChartProgramPipeline = *settings.FeatureFlags.ChartProgramPipeline
	}
	out.ChartDimensionFilters = settings.FeatureFlags.ChartDimensionFilters
	return out
}

func (c chartEngineEnablement) toToolFlags() tools.ChartEngineFlags {
	return tools.ChartEngineFlags{
		Echarts:    c.Echarts,
		Highcharts: c.Highcharts,
	}
}

func isChartToolAllowedForRun(toolName string, enabled chartEngineEnablement) bool {
	switch strings.TrimSpace(toolName) {
	case toolGetHighchartsCapabilities:
		return enabled.Highcharts
	case toolGetEchartsCapabilities:
		return enabled.Echarts
	case toolBuildChartArtifact:
		return enabled.ChartProgramPipeline
	default:
		return true
	}
}

func filterToolDefinitionsByChartEngines(toolDefs []types.ToolDefinition, enabled chartEngineEnablement) []types.ToolDefinition {
	if len(toolDefs) == 0 {
		return nil
	}
	out := make([]types.ToolDefinition, 0, len(toolDefs))
	for _, def := range toolDefs {
		name := strings.TrimSpace(def.Function.Name)
		if name == "" {
			continue
		}
		if !isChartToolAllowedForRun(name, enabled) {
			continue
		}
		if name == toolDisplayTable {
			def = shapeDisplayTableDefinitionForChartEngines(def, enabled)
		}
		if name == toolBuildChartArtifact {
			def = shapeBuildChartArtifactDefinitionForFeatures(def, enabled)
		}
		out = append(out, def)
	}
	return out
}

func shapeDisplayTableDefinitionForChartEngines(def types.ToolDefinition, enabled chartEngineEnablement) types.ToolDefinition {
	params := cloneJSONMap(def.Function.Parameters)
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return def
	}

	if !enabled.Highcharts {
		delete(props, "chart_spec")
		delete(props, "chart_spec_version")
	}
	if !enabled.Echarts {
		delete(props, "echarts_spec")
		delete(props, "echarts_spec_version")
	}
	if !enabled.ChartDimensionFilters {
		delete(props, "filters")
	}

	params["properties"] = props
	def.Function.Parameters = params

	switch {
	case enabled.Echarts && enabled.Highcharts:
		def.Function.Description = "Render structured data as an interactive table and chart in the chat UI. Provide exactly one data source: data_json (inline JSON array of objects) or source (artifact/path/job). Optional echarts_spec or chart_spec enables ECharts or Highcharts plotting."
	case enabled.Echarts:
		def.Function.Description = "Render structured data as an interactive table and chart in the chat UI. Provide exactly one data source: data_json (inline JSON array of objects) or source (artifact/path/job). Optional echarts_spec enables ECharts plotting."
	case enabled.Highcharts:
		def.Function.Description = "Render structured data as an interactive table and chart in the chat UI. Provide exactly one data source: data_json (inline JSON array of objects) or source (artifact/path/job). Optional chart_spec enables Highcharts plotting."
	default:
		def.Function.Description = "Render structured data as an interactive table in the chat UI. Provide exactly one data source: data_json (inline JSON array of objects) or source (artifact/path/job)."
	}
	return def
}

func shapeBuildChartArtifactDefinitionForFeatures(def types.ToolDefinition, enabled chartEngineEnablement) types.ToolDefinition {
	if enabled.ChartDimensionFilters {
		return def
	}
	params := cloneJSONMap(def.Function.Parameters)
	props, _ := params["properties"].(map[string]any)
	if props == nil {
		return def
	}
	transform, _ := props["transform"].(map[string]any)
	if transform == nil {
		return def
	}
	transformProps, _ := transform["properties"].(map[string]any)
	if transformProps == nil {
		return def
	}
	delete(transformProps, "filters")
	transform["properties"] = transformProps
	props["transform"] = transform
	params["properties"] = props
	def.Function.Parameters = params
	return def
}

func cloneJSONMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		out := make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		out = make(map[string]any, len(in))
		for key, value := range in {
			out[key] = value
		}
	}
	return out
}
