package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/types"
)

const (
	displayTableChartGuardMaxRows  = 10000
	displayTableChartGuardMaxBytes = 8 << 20
	displayTableSourcePreviewRows  = 200
	displayTableSourcePreviewCap   = 2000
)

func displayTableResultMetadata(rowCountTotal, rowCountReturned int, title, chartEngine string, previewOnly bool) map[string]any {
	meta := map[string]any{
		"provider":           "artifacts",
		"product":            "artifacts",
		"family":             "artifacts",
		"kind":               "table",
		"tool_name":          "display_table",
		"readonly":           true,
		"row_count_total":    rowCountTotal,
		"row_count_returned": rowCountReturned,
		"preview_only":       previewOnly,
	}
	if title = strings.TrimSpace(title); title != "" {
		meta["title"] = title
	}
	if chartEngine = strings.TrimSpace(chartEngine); chartEngine != "" {
		meta["chart_engine"] = chartEngine
	}
	if toolMeta, ok := ToolMetadataByName("display_table"); ok {
		meta["tool_set_id"] = toolMeta.ToolSetID
		meta["tool_set_name"] = toolMeta.ToolSetName
		meta["connector_id"] = toolMeta.ConnectorID
		meta["category"] = string(toolMeta.Category)
		meta["origin"] = string(toolMeta.Origin)
		meta["readonly"] = toolMeta.Category == ToolCategoryRead
		presentation := displayTablePresentationMetadata(toolMeta.Presentation)
		if len(presentation) > 0 {
			meta["tool_presentation"] = presentation
			if v := firstStringMapValue(presentation, "family"); v != "" {
				meta["family"] = v
				meta["product"] = v
			}
			if v := firstStringMapValue(presentation, "result_kind"); v != "" {
				meta["result_kind"] = v
			}
			if v := firstStringMapValue(presentation, "native_viewer"); v != "" {
				meta["native_viewer"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_strategy"); v != "" {
				meta["renderer_strategy"] = v
			}
			if v := firstStringMapValue(presentation, "renderer_key"); v != "" {
				meta["renderer_key"] = v
			}
			if v := firstStringMapValue(presentation, "file_capability"); v != "" {
				meta["file_capability"] = v
			}
		}
	}
	return meta
}

func displayTablePresentationMetadata(p ToolPresentationSpec) map[string]any {
	out := map[string]any{}
	putChartCapabilityPresentationString(out, "family", p.Family)
	putChartCapabilityPresentationString(out, "result_kind", p.ResultKind)
	putChartCapabilityPresentationString(out, "renderer_strategy", p.RendererStrategy)
	putChartCapabilityPresentationString(out, "renderer_key", p.RendererKey)
	putChartCapabilityPresentationString(out, "native_viewer", p.NativeViewer)
	putChartCapabilityPresentationStrings(out, "artifact_types", p.ArtifactTypes)
	putChartCapabilityPresentationString(out, "file_capability", p.FileCapability)
	putChartCapabilityPresentationStrings(out, "file_kinds", p.FileKinds)
	putChartCapabilityPresentationStrings(out, "preview_modes", p.PreviewModes)
	putChartCapabilityPresentationStrings(out, "source_refs", p.SourceRefs)
	if p.ProducesFileArtifact {
		out["produces_file_artifact"] = true
	}
	if p.PromotesNativeInput {
		out["promotes_native_input"] = true
	}
	putChartCapabilityPresentationStrings(out, "native_links", p.NativeLinks)
	return out
}

func newDisplayTableTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "display_table",
				Description: "Render structured data as an interactive table and chart in the chat UI. Use this when you have generated lists, statistics, or datasets that should be presented clearly to the user. Provide exactly one data source: inline data_json (JSON array of objects) or source (artifact/path/job). Optional chart_spec enables Highcharts; optional echarts_spec enables Apache ECharts.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"data_json": map[string]any{
							"type":        "string",
							"description": "A JSON string representing an array of objects (rows). Example: \"[{\\\"name\\\": \\\"A\\\", \\\"value\\\": 10}, ...]\"",
						},
						"source": displayTableSourceParameterSchema(),
						"title": map[string]any{
							"type":        "string",
							"description": "A short title describing the data.",
						},
						"chart_spec": highchartsChartSpecParameterSchema(),
						"chart_spec_version": map[string]any{
							"type":        "string",
							"description": "Optional capability_version from get_highcharts_capabilities. When provided, it must match the current capability version.",
						},
						"echarts_spec": echartsChartSpecParameterSchema(),
						"echarts_spec_version": map[string]any{
							"type":        "string",
							"description": "Optional capability_version from get_echarts_capabilities. When provided, it must match the current capability version.",
						},
						"filters": map[string]any{
							"type":        "array",
							"description": "Optional row filters applied before display. AND across filters; OR within values.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"column": map[string]any{
										"type":        "string",
										"description": "Column name to filter on.",
									},
									"operator": map[string]any{
										"type":        "string",
										"enum":        []string{"eq", "neq", "gt", "lt", "gte", "lte", "contains", "starts_with", "ends_with", "is_null", "not_null"},
										"description": "Comparison operator.",
									},
									"values": map[string]any{
										"type":        "array",
										"items":       map[string]any{"type": "string"},
										"description": "OR-list of values; row matches if cell matches ANY value.",
									},
									"value": map[string]any{
										"type":        "string",
										"description": "Single comparison value.",
									},
								},
								"required":             []string{"column", "operator"},
								"additionalProperties": false,
							},
						},
					},
					"required":             []string{},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				DataJSON           string                     `json:"data_json"`
				Source             chartArtifactSourceInput   `json:"source,omitempty"`
				Title              string                     `json:"title,omitempty"`
				ChartSpec          any                        `json:"chart_spec,omitempty"`
				ChartSpecVersion   string                     `json:"chart_spec_version,omitempty"`
				EchartsSpec        any                        `json:"echarts_spec,omitempty"`
				EchartsSpecVersion string                     `json:"echarts_spec_version,omitempty"`
				Filters            []ChartArtifactFilterInput `json:"filters,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}

			raw := strings.TrimSpace(in.DataJSON)
			hasInlineData := raw != ""
			hasSource := displayTableSourceProvided(in.Source)
			if hasInlineData == hasSource {
				return JSONError("Specify exactly one data source: data_json or source."), nil
			}

			var (
				rows []map[string]any
				err  error
			)
			if hasInlineData {
				rows, err = parseDisplayTableInlineRows(raw)
				if err != nil {
					return JSONError(err.Error()), nil
				}
			} else {
				rows, _, err = loadChartSourceRows(ctx, exec, in.Source)
				if err != nil {
					return JSONError(err.Error()), nil
				}
			}
			if len(rows) == 0 {
				return JSONError("The provided data array is empty."), nil
			}

			if len(in.Filters) > 0 {
				rows, err = ApplyChartFilters(rows, in.Filters)
				if err != nil {
					return JSONError(err.Error()), nil
				}
				if len(rows) == 0 {
					return JSONError("no rows remain after filters"), nil
				}
			}

			if in.ChartSpec != nil && in.EchartsSpec != nil {
				return JSONError("Specify only one chart spec field: chart_spec or echarts_spec."), nil
			}
			if in.ChartSpec != nil && !exec.ChartEngines.Highcharts {
				return JSONError("chart engine \"highcharts\" is disabled for this run"), nil
			}
			if in.EchartsSpec != nil && !exec.ChartEngines.Echarts {
				return JSONError("chart engine \"echarts\" is disabled for this run"), nil
			}
			chartRequested := in.ChartSpec != nil || in.EchartsSpec != nil
			chartEngine := ""
			if in.ChartSpec != nil {
				chartEngine = highchartsEngineName
			}
			if in.EchartsSpec != nil {
				chartEngine = echartsEngineName
			}
			var budget *ChartBudgetReport
			if chartRequested {
				budget = NewChartBudgetReport("table", chartEngine)
				budget.RowCountTotal = len(rows)
				budget.PointsRequested = len(rows)
				budget.SeriesCount = 1
				violatesRowGuard := hasInlineData && len(rows) > displayTableChartGuardMaxRows
				violatesByteGuard := hasInlineData && len(raw) > displayTableChartGuardMaxBytes
				if violatesRowGuard || violatesByteGuard {
					budget.AddAction("guardrail:display_table_chart_payload_limit")
					if violatesRowGuard {
						budget.AddAction("guardrail:max_rows")
					}
					if violatesByteGuard {
						budget.AddAction("guardrail:max_data_json_bytes")
					}
					out := structuredChartError(
						"display_table chart payload is too large",
						"DATA_TOO_LARGE_FOR_DISPLAY_TABLE",
						"Retry display_table with source.artifact_id or source.path for advanced chart specs, or use build_chart_artifact for intent-based charts.",
						"tool_generation",
						budget,
					)
					return out, nil
				}
			}

			previewLimit := minInt(displayTableSourcePreviewRows, displayTableSourcePreviewCap)
			previewRows := rows
			previewOnly := false
			if hasSource && len(rows) > previewLimit {
				previewRows = rows[:previewLimit]
				previewOnly = true
			}
			resultRows := make([]any, 0, len(previewRows))
			for _, row := range previewRows {
				resultRows = append(resultRows, cloneRowMap(row))
			}
			if budget != nil {
				budget.PointsReturned = len(resultRows)
				if rowsBytes, marshalErr := json.Marshal(resultRows); marshalErr == nil {
					budget.RowsPreviewBytes = len(rowsBytes)
				}
				if previewOnly {
					budget.AddAction("preview:source_rows")
				}
			}

			fullArtifactID := ""
			if previewOnly {
				fullArtifactID = largeOutputArtifactID()
			}

			var (
				normalized     *normalizedChartSpec
				resolvedEngine string
			)
			if in.ChartSpec != nil {
				rawSpec, err := normalizeDisplayTableChartSpec(in.ChartSpec)
				if err != nil {
					return JSONError("Invalid chart_spec: " + strings.TrimSpace(err.Error())), nil
				}
				spec, err := normalizeChartSpecInput(rawSpec, in.ChartSpecVersion)
				if err != nil {
					return JSONError("Invalid chart_spec: " + strings.TrimSpace(err.Error())), nil
				}
				normalized = spec
				resolvedEngine = highchartsEngineName
			}
			if in.EchartsSpec != nil {
				rawSpec, err := normalizeDisplayTableChartSpec(in.EchartsSpec)
				if err != nil {
					return JSONError("Invalid echarts_spec: " + strings.TrimSpace(err.Error())), nil
				}
				spec, err := normalizeEchartsSpecInput(rawSpec, in.EchartsSpecVersion)
				if err != nil {
					return JSONError("Invalid echarts_spec: " + strings.TrimSpace(err.Error())), nil
				}
				normalized = spec
				resolvedEngine = echartsEngineName
			}

			result := map[string]any{
				"rows": resultRows,
			}
			if title := strings.TrimSpace(in.Title); title != "" {
				result["title"] = title
			}
			if normalized != nil {
				result["chart_engine"] = resolvedEngine
				result["plot_spec"] = normalized.PlotSpec
				result["chart_spec_version"] = normalized.CapabilityVersion
				if strings.TrimSpace(normalized.Module) != "" {
					result["chart_module"] = normalized.Module
				}
				if len(normalized.Modules) > 0 {
					result["chart_modules"] = normalized.Modules
				}
				result["chart_validation"] = map[string]any{"normalized": true}
			}
			resultMeta := displayTableResultMetadata(len(rows), len(resultRows), in.Title, resolvedEngine, previewOnly)
			if budget != nil {
				if resolvedEngine != "" {
					budget.ChartEngine = resolvedEngine
				}
				if normalized != nil {
					if plotSpecBytes, err := json.Marshal(normalized.PlotSpec); err == nil {
						budget.PlotSpecBytes = len(plotSpecBytes)
					}
				}
				resultMeta["chart_budget"] = budget.AsMap()
				result["chart_budget"] = resultMeta["chart_budget"]
				if b, err := json.Marshal(result); err == nil {
					budget.ResultPayloadBytes = len(b)
					resultMeta["chart_budget"] = budget.AsMap()
					result["chart_budget"] = resultMeta["chart_budget"]
				}
			}

			var artifactPayload *ArtifactPayload
			if previewOnly {
				resultMeta = addLargeOutputPreviewContractMetadata(result, resultMeta, fullArtifactID, len(rows), len(resultRows), "json_rows")
				artifactTitle := strings.TrimSpace(in.Title)
				if artifactTitle == "" {
					artifactTitle = "Table"
				}
				payload, payloadErr := newLargeRowsArtifactPayload(fullArtifactID, artifactTitle, rows, "display_table", map[string]any{
					"row_count_total":  len(rows),
					"preview_only":     true,
					"partial":          true,
					"full_artifact_id": fullArtifactID,
				})
				if payloadErr != nil {
					return JSONError("Failed to encode source dataset for artifact persistence."), nil
				}
				artifactPayload = payload
			}

			// Return structured dataset payload so UI can render table and optional chart artifact.
			if artifactPayload != nil {
				return Output{
					OK:             true,
					ResultFormat:   ResultFormatJSON,
					Result:         result,
					ResultMetadata: cloneOutputMetadataMap(resultMeta),
					Artifact:       artifactPayload,
				}, nil
			}
			return JSONOKWithMetadata(result, resultMeta), nil
		},
	}
}

func parseDisplayTableInlineRows(raw string) ([]map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("data_json is required")
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("Failed to parse data_json. Ensure it is a valid JSON string.")
	}
	rowsAny, ok := parsed.([]any)
	if !ok {
		return nil, fmt.Errorf("The provided data_json is not a valid JSON array.")
	}
	if len(rowsAny) == 0 {
		return nil, fmt.Errorf("The provided data array is empty.")
	}

	rows := make([]map[string]any, 0, len(rowsAny))
	for _, row := range rowsAny {
		if row == nil {
			return nil, fmt.Errorf("The provided data_json must be an array of objects.")
		}
		mm, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("The provided data_json must be an array of objects.")
		}
		rows = append(rows, cloneRowMap(mm))
	}
	return rows, nil
}

func displayTableSourceProvided(source chartArtifactSourceInput) bool {
	return strings.TrimSpace(source.ArtifactID) != "" ||
		strings.TrimSpace(source.Path) != "" ||
		strings.TrimSpace(source.JobID) != "" ||
		strings.TrimSpace(source.OwnerToken) != ""
}

func displayTableSourceParameterSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional source pointer for dataset rows. Use this instead of inline data_json for large datasets.",
		"properties": map[string]any{
			"artifact_id": map[string]any{
				"type":        "string",
				"description": "Artifact ID that contains dataset rows.",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "Project-relative dataset path (CSV or JSON).",
			},
			"job_id": map[string]any{
				"type":        "string",
				"description": "Async query job ID.",
			},
			"owner_token": map[string]any{
				"type":        "string",
				"description": "Owner token required with source.job_id.",
			},
		},
		"additionalProperties": false,
	}
}

func normalizeDisplayTableChartSpec(raw any) (map[string]any, error) {
	switch v := raw.(type) {
	case map[string]any:
		return v, nil
	case string:
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return nil, fmt.Errorf("must be an object or a JSON object string")
		}
		var parsed any
		if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
			return nil, err
		}
		spec, ok := parsed.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("must be an object or a JSON object string")
		}
		return spec, nil
	default:
		return nil, fmt.Errorf("must be an object or a JSON object string")
	}
}

func highchartsChartSpecParameterSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional Highcharts chart specification object. Supports schema_version highcharts-plot-spec.v1 or highcharts-plot-spec.v2.",
		"properties": map[string]any{
			"schema_version": map[string]any{
				"type": "string",
				"enum": []string{HighchartsPlotSpecVersionV1, HighchartsPlotSpecVersionV2},
			},
			"module": map[string]any{
				"type": "string",
				"enum": []string{"core", "stock", "maps", "gantt"},
			},
			"modules": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"options": map[string]any{
				"type":                 "object",
				"additionalProperties": true,
			},
			"extensions": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"drilldown_async": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"loader_id": map[string]any{"type": "string"},
							"series_by_id": map[string]any{
								"type":                 "object",
								"additionalProperties": true,
							},
							"max_depth": map[string]any{"type": "integer"},
						},
						"required":             []string{"loader_id", "series_by_id"},
						"additionalProperties": false,
					},
				},
				"additionalProperties": false,
			},
		},
		"required":             []string{"schema_version", "module", "options"},
		"additionalProperties": false,
	}
}

func echartsChartSpecParameterSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional Apache ECharts chart specification object. Supports schema_version echarts-plot-spec.v1.",
		"properties": map[string]any{
			"schema_version": map[string]any{
				"type": "string",
				"enum": []string{EchartsPlotSpecVersionV1},
			},
			"module": map[string]any{
				"type": "string",
				"enum": []string{"core", "dataviz", "graph", "all"},
			},
			"modules": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
			},
			"options": map[string]any{
				"type":                 "object",
				"additionalProperties": true,
			},
			"extensions": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"drilldown_async": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"loader_id": map[string]any{"type": "string"},
							"series_by_id": map[string]any{
								"type":                 "object",
								"additionalProperties": true,
							},
							"max_depth": map[string]any{"type": "integer"},
						},
						"required":             []string{"loader_id", "series_by_id"},
						"additionalProperties": false,
					},
				},
				"additionalProperties": false,
			},
		},
		"required":             []string{"schema_version", "module", "options"},
		"additionalProperties": false,
	}
}

func supportedHighchartsSeriesTypes() []string {
	dedupe := map[string]struct{}{}
	for _, byType := range allowedHighchartsSeriesByModule {
		for seriesType := range byType {
			dedupe[seriesType] = struct{}{}
		}
	}
	out := make([]string, 0, len(dedupe))
	for seriesType := range dedupe {
		out = append(out, seriesType)
	}
	sort.Strings(out)
	return out
}

func itoa(i int) string {
	// Small helper to avoid pulling in strconv for a single use.
	if i == 0 {
		return "0"
	}
	neg := false
	if i < 0 {
		neg = true
		i = -i
	}
	var buf [32]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + (i % 10))
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
