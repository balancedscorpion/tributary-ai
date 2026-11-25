package tools

import (
	"context"
	"encoding/json"

	"ai-analyst-local/internal/chat/types"
)

func newGetHighchartsCapabilitiesTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_highcharts_capabilities",
				Description: "Return the currently supported Highcharts plotting capabilities, including capability_version, chart spec versions, constructors, module catalog, supported series types, allowed option paths, and callback IDs.",
				Parameters: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{},
					"required":             []string{},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			snapshot, err := loadHighchartsCapabilitiesSnapshot()
			if err != nil {
				return JSONError("Failed to load Highcharts capabilities snapshot."), nil
			}
			return chartCapabilityOutput("get_highcharts_capabilities", "highcharts", snapshot), nil
		},
	}
}
