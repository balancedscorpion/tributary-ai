package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/jobs"
	"ai-analyst-local/internal/chat/store"
	chattype "ai-analyst-local/internal/chat/types"
)

const (
	defaultChartArtifactMaxPoints   = 50000
	maxChartArtifactMaxPoints       = 200000
	defaultChartArtifactPreviewRows = 200
	maxChartArtifactPreviewRows     = 2000
	maxChartArtifactSourceBytes     = 256 << 20
	maxChartArtifactSpecBytes       = 8 << 20
	maxChartSpecBudgetAttempts      = 5
	minChartSpecBudgetPoints        = 1000
)

func newBuildChartArtifactTool() Tool {
	return ExecutableTool{
		Def: chattype.ToolDefinition{
			Type: "function",
			Function: chattype.ToolFunctionDefinition{
				Name: "build_chart_artifact",
				Description: "Build a chart-ready artifact from an existing data artifact. " +
					"Use this for scatter plots or large datasets so the backend transforms data instead of manually constructing large chart payloads.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"artifact_id": map[string]any{
									"type":        "string",
									"description": "Source artifact ID that contains dataset rows.",
								},
								"path": map[string]any{
									"type":        "string",
									"description": "Optional project-relative dataset path (CSV or JSON). Prefer this for uploaded files instead of read_file artifacts.",
								},
								"job_id": map[string]any{
									"type":        "string",
									"description": "Optional async job ID when loading from query registry.",
								},
								"owner_token": map[string]any{
									"type":        "string",
									"description": "Optional owner token for async query registry lookups.",
								},
							},
							"additionalProperties": false,
						},
						"chart": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"kind": map[string]any{
									"type": "string",
									"enum": []string{"scatter", "line", "bar"},
								},
								"engine": map[string]any{
									"type": "string",
									"enum": []string{"echarts", "highcharts"},
								},
								"x": map[string]any{
									"type": "string",
								},
								"y": map[string]any{
									"type": "string",
								},
								"color": map[string]any{
									"type": "string",
								},
								"size": map[string]any{
									"type": "string",
								},
							},
							"required":             []string{"kind", "x", "y"},
							"additionalProperties": false,
						},
						"transform": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"drop_nulls": map[string]any{
									"type": "boolean",
								},
								"coerce_numeric": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "string",
									},
								},
								"project": map[string]any{
									"type": "array",
									"items": map[string]any{
										"type": "string",
									},
								},
								"limit": map[string]any{
									"type": "integer",
								},
								"sort": map[string]any{
									"type": "object",
									"properties": map[string]any{
										"by": map[string]any{
											"type": "string",
										},
										"direction": map[string]any{
											"type": "string",
											"enum": []string{"asc", "desc"},
										},
									},
									"required":             []string{"by"},
									"additionalProperties": false,
								},
								"max_points": map[string]any{
									"type":        "integer",
									"description": "Maximum sampled points for scatter plots.",
								},
								"sample_method": map[string]any{
									"type": "string",
									"enum": []string{"stratified_reservoir", "reservoir"},
								},
								"preview_rows": map[string]any{
									"type":        "integer",
									"description": "How many sampled rows to return inline for immediate preview.",
								},
								"filters": map[string]any{
									"type":        "array",
									"description": "Optional row filters applied after loading but before sampling. AND across filters; OR within values.",
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
							"additionalProperties": false,
						},
						"title": map[string]any{
							"type": "string",
						},
					},
					"required":             []string{"source", "chart"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			if exec.Store == nil {
				return JSONError("store is not configured"), nil
			}
			var in chartArtifactInput
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}

			sourceArtifactID := strings.TrimSpace(in.Source.ArtifactID)
			sourcePath := strings.TrimSpace(in.Source.Path)
			if sourceArtifactID == "" && sourcePath == "" && strings.TrimSpace(in.Source.JobID) == "" {
				return JSONError("source.artifact_id, source.path, or source.job_id (with owner_token) is required"), nil
			}

			rows, sourceLabel, err := loadChartSourceRows(ctx, exec, in.Source)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if len(rows) == 0 {
				return JSONError("source dataset is empty"), nil
			}

			engine := strings.ToLower(strings.TrimSpace(in.Chart.Engine))
			if engine == "" {
				engine = echartsEngineName
			}
			kind := strings.ToLower(strings.TrimSpace(in.Chart.Kind))
			if kind == "" {
				kind = "scatter"
			}
			if kind != "scatter" && kind != "line" && kind != "bar" {
				return JSONError("chart.kind must be one of scatter, line, bar"), nil
			}
			xCol := strings.TrimSpace(in.Chart.X)
			yCol := strings.TrimSpace(in.Chart.Y)
			if xCol == "" || yCol == "" {
				return JSONError("chart.x and chart.y are required"), nil
			}

			transform := normalizeChartTransformInput(in.Transform)
			rows = applyChartProjection(rows, transform.Project, in.Chart)
			rows, err = ApplyChartFilters(rows, transform.Filters)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if len(rows) == 0 {
				return JSONError("no rows remain after filters"), nil
			}
			rows, err = applyChartCoercionAndFiltering(rows, in.Chart, transform)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if len(rows) == 0 {
				return JSONError("no rows remain after transforms"), nil
			}
			rows, err = applyChartSortAndLimit(rows, transform)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if len(rows) == 0 {
				return JSONError("no rows remain after transforms"), nil
			}

			budget := NewChartBudgetReport(kind, engine)
			budget.RowCountTotal = len(rows)
			budget.PointsRequested = len(rows)

			effectiveMaxPoints := transform.MaxPoints
			if effectiveMaxPoints <= 0 {
				effectiveMaxPoints = defaultChartArtifactMaxPoints
			}
			if effectiveMaxPoints > maxChartArtifactMaxPoints {
				effectiveMaxPoints = maxChartArtifactMaxPoints
			}

			sampledRows := rows
			downsampled := false
			var normalized *normalizedChartSpec
			var lastPlotSpecBytes int

			for attempt := 0; attempt < maxChartSpecBudgetAttempts; attempt++ {
				var budgetActions []string
				sampledRows, downsampled, budget.SeriesCount, budgetActions = applyChartMarkBudget(kind, rows, in.Chart, transform, effectiveMaxPoints)
				for _, action := range budgetActions {
					budget.AddAction(action)
				}
				budget.PointsReturned = len(sampledRows)
				if len(sampledRows) == 0 {
					out := structuredChartError(
						"sampling produced no rows",
						"CHART_SAMPLING_EMPTY",
						"Check chart mappings or reduce transform filters.",
						"tool_generation",
						budget,
					)
					return out, nil
				}

				switch engine {
				case echartsEngineName:
					if !exec.ChartEngines.Echarts {
						return JSONError("chart engine \"echarts\" is disabled for this run"), nil
					}
					rawSpec := buildRawEchartsSpec(kind, in.Chart, sampledRows, strings.TrimSpace(in.Title))
					spec, normErr := normalizeEchartsSpecInput(rawSpec, "")
					if normErr != nil {
						return JSONError("failed to normalize generated echarts spec: " + strings.TrimSpace(normErr.Error())), nil
					}
					normalized = spec
				case highchartsEngineName:
					if !exec.ChartEngines.Highcharts {
						return JSONError("chart engine \"highcharts\" is disabled for this run"), nil
					}
					rawSpec := buildRawHighchartsSpec(kind, in.Chart, sampledRows, strings.TrimSpace(in.Title))
					spec, normErr := normalizeChartSpecInput(rawSpec, "")
					if normErr != nil {
						return JSONError("failed to normalize generated highcharts spec: " + strings.TrimSpace(normErr.Error())), nil
					}
					normalized = spec
				default:
					return JSONError("chart.engine must be echarts or highcharts"), nil
				}

				plotSpecBytes, err := json.Marshal(normalized.PlotSpec)
				if err == nil {
					lastPlotSpecBytes = len(plotSpecBytes)
					budget.PlotSpecBytes = len(plotSpecBytes)
				}
				if lastPlotSpecBytes <= maxChartArtifactSpecBytes {
					break
				}
				budget.AddAction("spec_budget:reduce_max_points")
				if effectiveMaxPoints <= minChartSpecBudgetPoints {
					break
				}
				nextMaxPoints := effectiveMaxPoints / 2
				if nextMaxPoints < minChartSpecBudgetPoints {
					nextMaxPoints = minChartSpecBudgetPoints
				}
				if nextMaxPoints >= effectiveMaxPoints {
					break
				}
				effectiveMaxPoints = nextMaxPoints
			}

			if budget.PlotSpecBytes > maxChartArtifactSpecBytes {
				out := structuredChartError(
					"chart specification is too large after budget retries",
					"CHART_SPEC_TOO_LARGE",
					"Reduce category cardinality, lower max_points, or use a more aggregated chart intent.",
					"tool_generation",
					budget,
				)
				return out, nil
			}

			previewCount := minInt(transform.PreviewRows, len(sampledRows))
			previewRows := sampledRows[:previewCount]
			previewRowsAny := make([]any, 0, len(previewRows))
			for _, row := range previewRows {
				previewRowsAny = append(previewRowsAny, row)
			}
			if rowsPreviewBytes, err := json.Marshal(previewRowsAny); err == nil {
				budget.RowsPreviewBytes = len(rowsPreviewBytes)
			}

			title := strings.TrimSpace(in.Title)
			if title == "" {
				title = "Chart Data"
			}
			artifactID := largeOutputArtifactID()

			ratio := 1.0
			if len(sampledRows) > 0 {
				ratio = float64(len(rows)) / float64(len(sampledRows))
			}
			budget.PointsReturned = len(sampledRows)
			if len(sampledRows) < len(rows) {
				downsampled = true
			}
			if downsampled {
				budget.AddAction("downsample:applied")
			}

			resultMetadata := toolCatalogResultMetadata("build_chart_artifact", "chart", map[string]any{
				"preview_only":     true,
				"full_artifact_id": artifactID,
				"row_count_total":  len(rows),
				"downsampled":      downsampled,
				"downsample_ratio": roundFloat(ratio, 4),
				"points_sampled":   len(sampledRows),
				"chart_budget":     budget.AsMap(),
			})
			chartRebuild := map[string]any{
				"kind":   kind,
				"engine": engine,
				"x":      xCol,
				"y":      yCol,
			}
			if color := strings.TrimSpace(in.Chart.Color); color != "" {
				chartRebuild["color"] = color
			}
			if size := strings.TrimSpace(in.Chart.Size); size != "" {
				chartRebuild["size"] = size
			}
			resultMetadata["chart_rebuild"] = chartRebuild

			result := map[string]any{
				"artifact_id":        artifactID,
				"rows":               previewRowsAny,
				"rows_preview":       previewRowsAny,
				"row_count_total":    len(rows),
				"preview_only":       true,
				"full_artifact_id":   artifactID,
				"chart_engine":       engine,
				"plot_spec":          normalized.PlotSpec,
				"chart_spec_version": normalized.CapabilityVersion,
				"chart_validation":   map[string]any{"normalized": true},
				"chart_rebuild":      chartRebuild,
			}
			if strings.TrimSpace(normalized.Module) != "" {
				result["chart_module"] = normalized.Module
			}
			if len(normalized.Modules) > 0 {
				result["chart_modules"] = normalized.Modules
			}
			result["downsample_ratio"] = roundFloat(ratio, 4)
			result["chart_budget"] = resultMetadata["chart_budget"]
			if resultPayloadBytes, err := json.Marshal(result); err == nil {
				budget.ResultPayloadBytes = len(resultPayloadBytes)
				resultMetadata["chart_budget"] = budget.AsMap()
				result["chart_budget"] = resultMetadata["chart_budget"]
			}
			resultMetadata = addLargeOutputPreviewContractMetadata(result, resultMetadata, artifactID, len(rows), len(previewRowsAny), "json_rows")

			artifactMetadata := map[string]any{
				"row_count":          len(rows),
				"row_count_total":    len(rows),
				"source":             sourceLabel,
				"source_artifact_id": strings.TrimSpace(in.Source.ArtifactID),
				"generated_by":       "build_chart_artifact",
				"chart_engine":       engine,
				"plot_spec":          normalized.PlotSpec,
				"chart_spec_version": normalized.CapabilityVersion,
				"downsampled":        downsampled,
				"downsample_ratio":   roundFloat(ratio, 4),
				"points_sampled":     len(sampledRows),
				"chart_budget":       budget.AsMap(),
				"chart_rebuild":      chartRebuild,
				"result_metadata":    cloneOutputMetadataMap(resultMetadata),
			}
			if presentation, ok := resultMetadata["tool_presentation"].(map[string]any); ok && len(presentation) > 0 {
				artifactMetadata["tool_presentation"] = cloneOutputMetadataMap(presentation)
			}
			if strings.TrimSpace(normalized.Module) != "" {
				artifactMetadata["chart_module"] = normalized.Module
			}
			if len(normalized.Modules) > 0 {
				artifactMetadata["chart_modules"] = normalized.Modules
			}
			artifactPayload, payloadErr := newLargeRowsArtifactPayload(artifactID, title, rows, "build_chart_artifact", artifactMetadata)
			if payloadErr != nil {
				return JSONError("Failed to encode chart dataset for artifact persistence."), nil
			}

			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				Result:         result,
				ResultMetadata: cloneOutputMetadataMap(resultMetadata),
				Artifact:       artifactPayload,
			}, nil
		},
	}
}

type chartArtifactInput struct {
	Source    chartArtifactSourceInput    `json:"source"`
	Chart     chartArtifactChartInput     `json:"chart"`
	Transform chartArtifactTransformInput `json:"transform,omitempty"`
	Title     string                      `json:"title,omitempty"`
}

type chartArtifactSourceInput struct {
	ArtifactID string `json:"artifact_id,omitempty"`
	Path       string `json:"path,omitempty"`
	JobID      string `json:"job_id,omitempty"`
	OwnerToken string `json:"owner_token,omitempty"`
}

type chartArtifactChartInput struct {
	Kind   string `json:"kind"`
	Engine string `json:"engine,omitempty"`
	X      string `json:"x"`
	Y      string `json:"y"`
	Color  string `json:"color,omitempty"`
	Size   string `json:"size,omitempty"`
}

// ChartRebuildInput describes a lightweight chart intent used for
// regenerating a plot spec from already-filtered rows.
type ChartRebuildInput struct {
	Kind   string `json:"kind,omitempty"`
	Engine string `json:"engine,omitempty"`
	X      string `json:"x,omitempty"`
	Y      string `json:"y,omitempty"`
	Color  string `json:"color,omitempty"`
	Size   string `json:"size,omitempty"`
}

// ChartRebuildResult contains normalized chart output for filtered-row rebuilds.
type ChartRebuildResult struct {
	PlotSpec          map[string]any
	Engine            string
	CapabilityVersion string
	Module            string
	Modules           []string
}

type chartArtifactSortInput struct {
	By        string `json:"by"`
	Direction string `json:"direction,omitempty"`
}

type chartArtifactTransformInput struct {
	DropNulls     bool                       `json:"drop_nulls,omitempty"`
	CoerceNumeric []string                   `json:"coerce_numeric,omitempty"`
	Project       []string                   `json:"project,omitempty"`
	Limit         int                        `json:"limit,omitempty"`
	Sort          *chartArtifactSortInput    `json:"sort,omitempty"`
	MaxPoints     int                        `json:"max_points,omitempty"`
	SampleMethod  string                     `json:"sample_method,omitempty"`
	PreviewRows   int                        `json:"preview_rows,omitempty"`
	Filters       []ChartArtifactFilterInput `json:"filters,omitempty"`
}

// ChartArtifactFilterInput describes a single row filter predicate.
type ChartArtifactFilterInput struct {
	Column   string   `json:"column"`
	Operator string   `json:"operator"`
	Values   []string `json:"values,omitempty"`
	Value    string   `json:"value,omitempty"`
}

func normalizeChartTransformInput(in chartArtifactTransformInput) chartArtifactTransformInput {
	out := in
	if out.MaxPoints <= 0 {
		out.MaxPoints = defaultChartArtifactMaxPoints
	}
	if out.MaxPoints > maxChartArtifactMaxPoints {
		out.MaxPoints = maxChartArtifactMaxPoints
	}
	if out.PreviewRows <= 0 {
		out.PreviewRows = defaultChartArtifactPreviewRows
	}
	if out.PreviewRows > maxChartArtifactPreviewRows {
		out.PreviewRows = maxChartArtifactPreviewRows
	}
	out.SampleMethod = strings.ToLower(strings.TrimSpace(out.SampleMethod))
	if out.SampleMethod == "" {
		out.SampleMethod = "stratified_reservoir"
	}
	return out
}

// ApplyChartFilters applies row-level filter predicates. AND across filters; OR within a single filter's Values.
func ApplyChartFilters(rows []map[string]any, filters []ChartArtifactFilterInput) ([]map[string]any, error) {
	if len(filters) == 0 {
		return rows, nil
	}
	validOps := map[string]bool{
		"eq": true, "neq": true, "gt": true, "lt": true,
		"gte": true, "lte": true, "contains": true,
		"starts_with": true, "ends_with": true,
		"is_null": true, "not_null": true,
	}
	for _, f := range filters {
		if strings.TrimSpace(f.Column) == "" {
			return nil, fmt.Errorf("filter column is required")
		}
		op := strings.TrimSpace(f.Operator)
		if !validOps[op] {
			return nil, fmt.Errorf("invalid filter operator %q", op)
		}
	}
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		if chartFilterRowMatches(row, filters) {
			out = append(out, row)
		}
	}
	return out, nil
}

func chartFilterRowMatches(row map[string]any, filters []ChartArtifactFilterInput) bool {
	for _, f := range filters {
		col := strings.TrimSpace(f.Column)
		op := strings.TrimSpace(f.Operator)
		cellRaw := row[col]
		cellStr := strings.TrimSpace(fmt.Sprintf("%v", cellRaw))
		if cellRaw == nil {
			cellStr = ""
		}
		// Infer column type for this cell
		colType := ColTypeString
		if _, err := parseNumber(cellStr); err == nil && cellStr != "" {
			colType = ColTypeNumber
		}
		if len(f.Values) > 0 {
			// OR logic: match if cell matches ANY value
			anyMatch := false
			for _, v := range f.Values {
				if evalFilter(cellStr, op, strings.TrimSpace(v), colType) {
					anyMatch = true
					break
				}
			}
			if !anyMatch {
				return false
			}
		} else {
			// Single value comparison
			if !evalFilter(cellStr, op, strings.TrimSpace(f.Value), colType) {
				return false
			}
		}
	}
	return true
}

func loadChartSourceRows(ctx context.Context, exec ExecContext, source chartArtifactSourceInput) ([]map[string]any, string, error) {
	if artifactID := strings.TrimSpace(source.ArtifactID); artifactID != "" {
		rows, err := loadRowsFromArtifact(ctx, exec, artifactID)
		return rows, "artifact", err
	}
	if sourcePath := strings.TrimSpace(source.Path); sourcePath != "" {
		rows, err := loadRowsFromProjectPath(exec, sourcePath)
		return rows, "path", err
	}
	jobID := strings.TrimSpace(source.JobID)
	ownerToken := strings.TrimSpace(source.OwnerToken)
	if jobID == "" || ownerToken == "" {
		return nil, "", fmt.Errorf("source.job_id and source.owner_token are required when source.artifact_id and source.path are not provided")
	}
	if exec.Jobs == nil {
		return nil, "", fmt.Errorf("async query registry is not configured")
	}
	owner := jobs.JobOwner{
		ThreadID: exec.ThreadID,
		RunID:    exec.RunID,
		Handle:   ownerToken,
	}
	result, err := exec.Jobs.Results(jobID, owner)
	if err != nil {
		return nil, "", err
	}
	if result == nil || len(result.Rows) == 0 {
		return nil, "", fmt.Errorf("query result is empty")
	}
	rows := make([]map[string]any, 0, len(result.Rows))
	for _, row := range result.Rows {
		if row == nil {
			continue
		}
		rows = append(rows, cloneRowMap(row))
	}
	return rows, "job", nil
}

func loadRowsFromArtifact(ctx context.Context, exec ExecContext, artifactID string) ([]map[string]any, error) {
	artifact, err := exec.Store.GetArtifact(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	if artifact.ByteSize > 0 && artifact.ByteSize > maxChartArtifactSourceBytes {
		return nil, fmt.Errorf("source artifact %s exceeds maximum size limit", artifactID)
	}
	raw := ""
	switch strings.TrimSpace(strings.ToLower(artifact.Storage)) {
	case "file":
		if artifact.ContentPath == nil || strings.TrimSpace(*artifact.ContentPath) == "" {
			return nil, fmt.Errorf("artifact %s has no content path", artifactID)
		}
		txt, readErr := readRelativeArtifactFile(exec.CfgDir, strings.TrimSpace(*artifact.ContentPath), maxChartArtifactSourceBytes)
		if readErr != nil {
			return nil, readErr
		}
		raw = txt
	default:
		if artifact.ContentText != nil {
			raw = *artifact.ContentText
			if int64(len(raw)) > maxChartArtifactSourceBytes {
				return nil, fmt.Errorf("source artifact %s exceeds maximum size limit", artifactID)
			}
		}
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("artifact %s content is empty", artifactID)
	}

	if isReadFileArtifact(artifact) {
		if wrapped, ok := parseReadFileArtifactEnvelope(raw); ok {
			if wrapped.Path != "" {
				if rows, pathErr := loadRowsFromProjectPath(exec, wrapped.Path); pathErr == nil {
					return rows, nil
				} else if wrapped.HasMore {
					return nil, fmt.Errorf("source artifact %s is a paged read_file chunk and full file load failed: %v", artifactID, pathErr)
				}
			}

			content := strings.TrimSpace(wrapped.Content)
			if content == "" {
				return nil, fmt.Errorf("artifact %s content is empty", artifactID)
			}
			if wrapped.HasMore {
				return nil, fmt.Errorf("source artifact %s is a paged read_file chunk; use source.path or a dataset artifact", artifactID)
			}
			rows, parseErr := parseDatasetRows(content)
			if parseErr != nil {
				return nil, parseErr
			}
			return rows, nil
		}
	}

	rows, parseErr := parseDatasetRows(raw)
	if parseErr != nil {
		return nil, parseErr
	}
	return rows, nil
}

func parseDatasetRows(raw string) ([]map[string]any, error) {
	return parseDatasetRowsWithDepth(raw, 0)
}

func parseDatasetRowsWithDepth(raw string, depth int) ([]map[string]any, error) {
	if depth > 4 {
		return nil, fmt.Errorf("source dataset nesting is too deep")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("source dataset is empty")
	}

	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		rows, parseErr := parseDatasetRowsFromParsed(parsed, depth)
		if parseErr == nil {
			return rows, nil
		}
		// JSON parsed but shape was not row-oriented; try CSV as a fallback.
		if csvRows, csvErr := parseCSVRows(raw); csvErr == nil {
			return csvRows, nil
		}
		return nil, parseErr
	}

	rows, err := parseCSVRows(raw)
	if err != nil {
		return nil, fmt.Errorf("failed to parse source dataset JSON or CSV")
	}
	return rows, nil
}

func parseDatasetRowsFromParsed(parsed any, depth int) ([]map[string]any, error) {
	switch v := parsed.(type) {
	case []any:
		out := make([]map[string]any, 0, len(v))
		for idx, row := range v {
			m, ok := row.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("source dataset row %d is not an object", idx)
			}
			out = append(out, cloneRowMap(m))
		}
		return out, nil
	case map[string]any:
		if rowsRaw, ok := v["rows"]; ok {
			switch rows := rowsRaw.(type) {
			case []any:
				out := make([]map[string]any, 0, len(rows))
				for idx, row := range rows {
					m, ok := row.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("source dataset row %d is not an object", idx)
					}
					out = append(out, cloneRowMap(m))
				}
				return out, nil
			case string:
				return parseDatasetRowsWithDepth(rows, depth+1)
			default:
				return nil, fmt.Errorf("source dataset rows field is not an array")
			}
		}
		if dataJSON, ok := mapStringValue(v, "data_json"); ok {
			return parseDatasetRowsWithDepth(dataJSON, depth+1)
		}
		if content, ok := mapStringValue(v, "content"); ok {
			return parseDatasetRowsWithDepth(content, depth+1)
		}
		return nil, fmt.Errorf("source dataset must be an array of objects")
	default:
		return nil, fmt.Errorf("source dataset must be an array of objects")
	}
}

func parseCSVRows(raw string) ([]map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("source CSV dataset is empty")
	}

	data := stripBOM([]byte(raw))
	reader := bytes.NewReader(data)
	delim := detectDelimiter(reader)
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to parse source CSV")
	}
	cr := newCSVReader(reader, delim)

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("failed to parse source CSV header")
	}
	if len(header) == 0 {
		return nil, fmt.Errorf("source CSV header is empty")
	}

	rows := make([]map[string]any, 0, 1024)
	for {
		record, readErr := cr.Read()
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			continue
		}
		rows = append(rows, rowToMap(header, record))
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("source CSV has no data rows")
	}
	return rows, nil
}

func mapStringValue(mm map[string]any, keys ...string) (string, bool) {
	for _, key := range keys {
		raw, ok := mm[strings.TrimSpace(key)]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				continue
			}
			return v, true
		default:
			s := strings.TrimSpace(fmt.Sprintf("%v", v))
			if s == "" {
				continue
			}
			return s, true
		}
	}
	return "", false
}

func loadRowsFromProjectPath(exec ExecContext, sourcePath string) ([]map[string]any, error) {
	if exec.Projects == nil {
		return nil, fmt.Errorf("project files are not available for source.path")
	}
	projectID := strings.TrimSpace(exec.ProjectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required for source.path")
	}
	displayPath, err := normalizeRelPathForDisplay(sourcePath, false)
	if err != nil {
		return nil, err
	}

	f, err := exec.Projects.OpenFile(projectID, displayPath)
	if err != nil {
		return nil, fmt.Errorf("file not found: %s", strings.TrimSpace(sourcePath))
	}
	defer f.Close()

	b, err := io.ReadAll(io.LimitReader(f, maxChartArtifactSourceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("failed reading source.path file: %v", err)
	}
	if int64(len(b)) > maxChartArtifactSourceBytes {
		return nil, fmt.Errorf("source path %s exceeds maximum size limit", displayPath)
	}

	text := strings.TrimSpace(string(stripBOM(b)))
	if text == "" {
		return nil, fmt.Errorf("source path %s is empty", displayPath)
	}
	if isCSVPath(displayPath) {
		rows, parseErr := parseCSVRows(text)
		if parseErr != nil {
			return nil, parseErr
		}
		return rows, nil
	}
	return parseDatasetRows(text)
}

type readFileArtifactEnvelope struct {
	Path    string
	Content string
	HasMore bool
}

func parseReadFileArtifactEnvelope(raw string) (readFileArtifactEnvelope, bool) {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return readFileArtifactEnvelope{}, false
	}
	content, ok := parsed["content"].(string)
	if !ok {
		return readFileArtifactEnvelope{}, false
	}
	envelope := readFileArtifactEnvelope{
		Content: content,
	}
	if path, ok := parsed["path"].(string); ok {
		envelope.Path = strings.TrimSpace(path)
	}
	if hasMore, ok := parsed["has_more"].(bool); ok {
		envelope.HasMore = hasMore
	}
	return envelope, true
}

func isReadFileArtifact(artifact store.Artifact) bool {
	if artifact.ToolName != nil && strings.EqualFold(strings.TrimSpace(*artifact.ToolName), "read_file") {
		return true
	}
	if artifact.Metadata == nil {
		return false
	}
	toolName, ok := mapStringValue(artifact.Metadata, "tool_name")
	if !ok {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(toolName), "read_file")
}

func applyChartProjection(rows []map[string]any, project []string, chart chartArtifactChartInput) []map[string]any {
	required := map[string]struct{}{}
	addRequired := func(key string) {
		key = strings.TrimSpace(key)
		if key == "" {
			return
		}
		required[key] = struct{}{}
	}
	addRequired(chart.X)
	addRequired(chart.Y)
	addRequired(chart.Color)
	addRequired(chart.Size)

	if len(project) == 0 {
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, cloneRowMap(row))
		}
		return out
	}

	ordered := make([]string, 0, len(project)+len(required))
	seen := map[string]struct{}{}
	for _, item := range project {
		key := strings.TrimSpace(item)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ordered = append(ordered, key)
	}
	for key := range required {
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		ordered = append(ordered, key)
	}

	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := map[string]any{}
		for _, key := range ordered {
			if value, ok := row[key]; ok {
				next[key] = value
			}
		}
		out = append(out, next)
	}
	return out
}

func applyChartCoercionAndFiltering(rows []map[string]any, chart chartArtifactChartInput, transform chartArtifactTransformInput) ([]map[string]any, error) {
	numericKeys := map[string]struct{}{}
	for _, key := range transform.CoerceNumeric {
		key = strings.TrimSpace(key)
		if key != "" {
			numericKeys[key] = struct{}{}
		}
	}
	if strings.EqualFold(strings.TrimSpace(chart.Kind), "scatter") {
		if strings.TrimSpace(chart.X) != "" {
			numericKeys[strings.TrimSpace(chart.X)] = struct{}{}
		}
		if strings.TrimSpace(chart.Y) != "" {
			numericKeys[strings.TrimSpace(chart.Y)] = struct{}{}
		}
		if strings.TrimSpace(chart.Size) != "" {
			numericKeys[strings.TrimSpace(chart.Size)] = struct{}{}
		}
	}

	xKey := strings.TrimSpace(chart.X)
	yKey := strings.TrimSpace(chart.Y)
	sizeKey := strings.TrimSpace(chart.Size)
	dropNulls := transform.DropNulls

	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		next := cloneRowMap(row)
		skip := false
		for key := range numericKeys {
			value, exists := next[key]
			if !exists {
				if dropNulls && (key == xKey || key == yKey || key == sizeKey) {
					skip = true
					break
				}
				continue
			}
			number, ok := coerceToFiniteNumber(value)
			if ok {
				next[key] = number
				continue
			}
			if dropNulls && (key == xKey || key == yKey || key == sizeKey) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		if dropNulls {
			if isMissingValue(next[xKey]) || isMissingValue(next[yKey]) {
				continue
			}
			if sizeKey != "" && isMissingValue(next[sizeKey]) {
				continue
			}
		}
		out = append(out, next)
	}
	return out, nil
}

func applyChartSortAndLimit(rows []map[string]any, transform chartArtifactTransformInput) ([]map[string]any, error) {
	out := rows
	if transform.Sort != nil {
		sortKey := strings.TrimSpace(transform.Sort.By)
		if sortKey == "" {
			return nil, fmt.Errorf("transform.sort.by is required when sort is provided")
		}
		direction := strings.ToLower(strings.TrimSpace(transform.Sort.Direction))
		desc := direction == "desc"
		sort.SliceStable(out, func(i, j int) bool {
			left := out[i][sortKey]
			right := out[j][sortKey]
			ln, lok := coerceToFiniteNumber(left)
			rn, rok := coerceToFiniteNumber(right)
			if lok && rok {
				if desc {
					return ln > rn
				}
				return ln < rn
			}
			ls := strings.TrimSpace(fmt.Sprintf("%v", left))
			rs := strings.TrimSpace(fmt.Sprintf("%v", right))
			if desc {
				return ls > rs
			}
			return ls < rs
		})
	}
	if transform.Limit > 0 && transform.Limit < len(out) {
		out = out[:transform.Limit]
	}
	return out, nil
}

type indexedChartRow struct {
	Index  int
	Series string
	Row    map[string]any
}

func applyChartMarkBudget(kind string, rows []map[string]any, chart chartArtifactChartInput, transform chartArtifactTransformInput, maxPoints int) ([]map[string]any, bool, int, []string) {
	if len(rows) == 0 {
		return nil, false, 0, nil
	}
	if maxPoints <= 0 {
		maxPoints = transform.MaxPoints
	}
	if maxPoints <= 0 {
		maxPoints = defaultChartArtifactMaxPoints
	}
	if maxPoints > maxChartArtifactMaxPoints {
		maxPoints = maxChartArtifactMaxPoints
	}
	orderedSeries, grouped := groupChartRowsBySeries(rows, strings.TrimSpace(chart.Color))
	seriesCount := len(orderedSeries)
	if seriesCount <= 0 {
		seriesCount = 1
	}
	if len(rows) <= maxPoints {
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, cloneRowMap(row))
		}
		return out, false, seriesCount, nil
	}

	switch strings.TrimSpace(strings.ToLower(kind)) {
	case "scatter":
		var sampled []map[string]any
		action := "downsample:scatter_stratified_reservoir"
		if strings.EqualFold(transform.SampleMethod, "reservoir") {
			sampled = reservoirSampleRows(rows, maxPoints, rand.New(rand.NewSource(1)))
			action = "downsample:scatter_reservoir"
		} else {
			sampled = stratifiedReservoirSampleRows(rows, strings.TrimSpace(chart.Color), maxPoints, rand.New(rand.NewSource(1)))
		}
		return sampled, len(sampled) < len(rows), seriesCount, []string{action}
	case "line":
		quotas := distributeSeriesBudgets(orderedSeries, grouped, maxPoints)
		sampled, usedLTTB := reduceLineRowsBySeries(orderedSeries, grouped, quotas, strings.TrimSpace(chart.X), strings.TrimSpace(chart.Y))
		action := "downsample:line_stride"
		if usedLTTB {
			action = "downsample:line_lttb"
		}
		return sampled, len(sampled) < len(rows), seriesCount, []string{action}
	case "bar":
		quotas := distributeSeriesBudgets(orderedSeries, grouped, maxPoints)
		sampled := reduceBarRowsBySeries(
			orderedSeries,
			grouped,
			quotas,
			strings.TrimSpace(chart.X),
			strings.TrimSpace(chart.Y),
			strings.TrimSpace(chart.Color),
		)
		return sampled, len(sampled) < len(rows), seriesCount, []string{"downsample:bar_topk_other"}
	default:
		sampled := reservoirSampleRows(rows, maxPoints, rand.New(rand.NewSource(1)))
		return sampled, len(sampled) < len(rows), seriesCount, []string{"downsample:reservoir"}
	}
}

func groupChartRowsBySeries(rows []map[string]any, colorKey string) ([]string, map[string][]indexedChartRow) {
	const defaultSeries = "__default__"
	colorKey = strings.TrimSpace(colorKey)
	grouped := map[string][]indexedChartRow{}
	for idx, row := range rows {
		series := defaultSeries
		if colorKey != "" {
			series = strings.TrimSpace(fmt.Sprintf("%v", row[colorKey]))
			if series == "" {
				series = "(empty)"
			}
		}
		grouped[series] = append(grouped[series], indexedChartRow{
			Index:  idx,
			Series: series,
			Row:    row,
		})
	}
	ordered := make([]string, 0, len(grouped))
	for series := range grouped {
		ordered = append(ordered, series)
	}
	sort.Strings(ordered)
	return ordered, grouped
}

func distributeSeriesBudgets(orderedSeries []string, grouped map[string][]indexedChartRow, maxPoints int) map[string]int {
	out := map[string]int{}
	if len(orderedSeries) == 0 || maxPoints <= 0 {
		return out
	}
	base := maxPoints / len(orderedSeries)
	remainder := maxPoints % len(orderedSeries)
	total := 0
	for idx, series := range orderedSeries {
		quota := base
		if idx < remainder {
			quota++
		}
		if quota <= 0 && idx < maxPoints {
			quota = 1
		}
		groupLen := len(grouped[series])
		if groupLen > 0 && quota > groupLen {
			quota = groupLen
		}
		out[series] = quota
		total += quota
	}
	remaining := maxPoints - total
	for remaining > 0 {
		progress := false
		for _, series := range orderedSeries {
			groupLen := len(grouped[series])
			quota := out[series]
			if quota >= groupLen {
				continue
			}
			out[series] = quota + 1
			remaining--
			progress = true
			if remaining <= 0 {
				break
			}
		}
		if !progress {
			break
		}
	}
	return out
}

func reduceLineRowsBySeries(orderedSeries []string, grouped map[string][]indexedChartRow, quotas map[string]int, xKey string, yKey string) ([]map[string]any, bool) {
	selected := make([]indexedChartRow, 0)
	usedLTTB := false
	for _, series := range orderedSeries {
		group := grouped[series]
		if len(group) == 0 {
			continue
		}
		quota := quotas[series]
		if quota <= 0 {
			continue
		}
		if len(group) <= quota {
			selected = append(selected, group...)
			continue
		}
		if canUseLTTBForSeries(group, xKey, yKey) {
			usedLTTB = true
			selected = append(selected, lttbDecimateSeries(group, xKey, yKey, quota)...)
			continue
		}
		selected = append(selected, strideDecimateSeries(group, quota)...)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Index == selected[j].Index {
			return selected[i].Series < selected[j].Series
		}
		return selected[i].Index < selected[j].Index
	})
	return indexedRowsToMaps(selected), usedLTTB
}

func reduceBarRowsBySeries(orderedSeries []string, grouped map[string][]indexedChartRow, quotas map[string]int, xKey string, yKey string, colorKey string) []map[string]any {
	selected := make([]indexedChartRow, 0)
	for _, series := range orderedSeries {
		group := grouped[series]
		if len(group) == 0 {
			continue
		}
		quota := quotas[series]
		if quota <= 0 {
			continue
		}
		if len(group) <= quota {
			selected = append(selected, group...)
			continue
		}
		selected = append(selected, reduceBarSeriesGroup(group, quota, xKey, yKey, colorKey, series)...)
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Index == selected[j].Index {
			return selected[i].Series < selected[j].Series
		}
		return selected[i].Index < selected[j].Index
	})
	return indexedRowsToMaps(selected)
}

func reduceBarSeriesGroup(group []indexedChartRow, quota int, xKey string, yKey string, colorKey string, seriesKey string) []indexedChartRow {
	if len(group) == 0 || quota <= 0 {
		return nil
	}
	if len(group) <= quota {
		out := make([]indexedChartRow, 0, len(group))
		out = append(out, group...)
		return out
	}

	ranked := append([]indexedChartRow(nil), group...)
	sort.SliceStable(ranked, func(i, j int) bool {
		leftY, _ := coerceToFiniteNumber(ranked[i].Row[yKey])
		rightY, _ := coerceToFiniteNumber(ranked[j].Row[yKey])
		leftAbs := math.Abs(leftY)
		rightAbs := math.Abs(rightY)
		if leftAbs != rightAbs {
			return leftAbs > rightAbs
		}
		leftX := strings.TrimSpace(fmt.Sprintf("%v", ranked[i].Row[xKey]))
		rightX := strings.TrimSpace(fmt.Sprintf("%v", ranked[j].Row[xKey]))
		if leftX != rightX {
			return leftX < rightX
		}
		return ranked[i].Index < ranked[j].Index
	})

	topCount := quota - 1
	if quota == 1 {
		topCount = 0
	}
	if topCount > len(ranked) {
		topCount = len(ranked)
	}
	selectedSet := map[int]struct{}{}
	for i := 0; i < topCount; i++ {
		selectedSet[ranked[i].Index] = struct{}{}
	}

	selected := make([]indexedChartRow, 0, quota)
	dropped := make([]indexedChartRow, 0, len(group)-topCount)
	for _, row := range group {
		if _, ok := selectedSet[row.Index]; ok {
			selected = append(selected, row)
			continue
		}
		dropped = append(dropped, row)
	}
	if len(dropped) > 0 {
		selected = append(selected, buildOtherBarRow(dropped, xKey, yKey, colorKey, seriesKey))
	}
	return selected
}

func buildOtherBarRow(dropped []indexedChartRow, xKey string, yKey string, colorKey string, seriesKey string) indexedChartRow {
	sum := 0.0
	numericCount := 0
	maxIndex := 0
	if len(dropped) > 0 {
		maxIndex = dropped[len(dropped)-1].Index
	}
	for _, row := range dropped {
		if row.Index > maxIndex {
			maxIndex = row.Index
		}
		if y, ok := coerceToFiniteNumber(row.Row[yKey]); ok {
			sum += y
			numericCount++
		}
	}
	if numericCount == 0 {
		sum = float64(len(dropped))
	}
	next := map[string]any{
		xKey: "Other",
		yKey: roundFloat(sum, 6),
	}
	if strings.TrimSpace(colorKey) != "" && strings.TrimSpace(seriesKey) != "" && seriesKey != "__default__" {
		next[colorKey] = seriesKey
	}
	return indexedChartRow{
		Index:  maxIndex + 1,
		Series: seriesKey,
		Row:    next,
	}
}

func canUseLTTBForSeries(series []indexedChartRow, xKey string, yKey string) bool {
	if len(series) < 3 {
		return false
	}
	for _, row := range series {
		if _, ok := coerceLTTBXValue(row.Row[xKey]); !ok {
			return false
		}
		if _, ok := coerceToFiniteNumber(row.Row[yKey]); !ok {
			return false
		}
	}
	return true
}

func lttbDecimateSeries(series []indexedChartRow, xKey string, yKey string, threshold int) []indexedChartRow {
	if len(series) == 0 || threshold <= 0 {
		return nil
	}
	if threshold >= len(series) {
		out := make([]indexedChartRow, 0, len(series))
		out = append(out, series...)
		return out
	}
	if threshold == 1 {
		return []indexedChartRow{series[0]}
	}
	if threshold == 2 {
		return []indexedChartRow{series[0], series[len(series)-1]}
	}

	type point struct {
		x float64
		y float64
	}
	points := make([]point, 0, len(series))
	for _, row := range series {
		xv, _ := coerceLTTBXValue(row.Row[xKey])
		yv, _ := coerceToFiniteNumber(row.Row[yKey])
		points = append(points, point{x: xv, y: yv})
	}

	sampled := make([]indexedChartRow, 0, threshold)
	sampled = append(sampled, series[0])
	every := float64(len(series)-2) / float64(threshold-2)
	a := 0

	for i := 0; i < threshold-2; i++ {
		avgRangeStart := int(math.Floor(float64(i+1)*every)) + 1
		avgRangeEnd := int(math.Floor(float64(i+2)*every)) + 1
		if avgRangeEnd > len(series) {
			avgRangeEnd = len(series)
		}
		if avgRangeStart >= avgRangeEnd {
			avgRangeStart = minInt(len(series)-1, avgRangeStart)
			avgRangeEnd = minInt(len(series), avgRangeStart+1)
		}

		avgX := 0.0
		avgY := 0.0
		avgRangeLength := max(1, avgRangeEnd-avgRangeStart)
		for j := avgRangeStart; j < avgRangeEnd; j++ {
			avgX += points[j].x
			avgY += points[j].y
		}
		avgX /= float64(avgRangeLength)
		avgY /= float64(avgRangeLength)

		rangeOffs := int(math.Floor(float64(i)*every)) + 1
		rangeTo := int(math.Floor(float64(i+1)*every)) + 1
		if rangeTo > len(series)-1 {
			rangeTo = len(series) - 1
		}
		if rangeOffs >= rangeTo {
			rangeOffs = minInt(len(series)-2, rangeOffs)
			rangeTo = minInt(len(series)-1, rangeOffs+1)
		}
		if rangeTo <= rangeOffs {
			rangeTo = minInt(len(series)-1, rangeOffs+1)
		}

		pointA := points[a]
		maxArea := -1.0
		nextA := rangeOffs
		for j := rangeOffs; j < rangeTo; j++ {
			area := math.Abs((pointA.x-avgX)*(points[j].y-pointA.y) - (pointA.x-points[j].x)*(avgY-pointA.y))
			if area > maxArea {
				maxArea = area
				nextA = j
			}
		}
		sampled = append(sampled, series[nextA])
		a = nextA
	}

	sampled = append(sampled, series[len(series)-1])
	if len(sampled) > threshold {
		sampled = sampled[:threshold]
	}
	return sampled
}

func strideDecimateSeries(series []indexedChartRow, maxPoints int) []indexedChartRow {
	if len(series) == 0 || maxPoints <= 0 {
		return nil
	}
	if maxPoints >= len(series) {
		out := make([]indexedChartRow, 0, len(series))
		out = append(out, series...)
		return out
	}
	if maxPoints == 1 {
		return []indexedChartRow{series[0]}
	}
	out := make([]indexedChartRow, 0, maxPoints)
	for i := 0; i < maxPoints; i++ {
		idx := i * (len(series) - 1) / (maxPoints - 1)
		out = append(out, series[idx])
	}
	return out
}

func indexedRowsToMaps(rows []indexedChartRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, cloneRowMap(row.Row))
	}
	return out
}

func coerceLTTBXValue(v any) (float64, bool) {
	if n, ok := coerceToFiniteNumber(v); ok {
		return n, true
	}
	switch x := v.(type) {
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, false
		}
		layouts := []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02 15:04:05",
			"2006-01-02T15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, s); err == nil {
				return float64(parsed.UnixNano()) / float64(time.Second), true
			}
		}
	case time.Time:
		return float64(x.UnixNano()) / float64(time.Second), true
	}
	return 0, false
}

func stratifiedReservoirSampleRows(rows []map[string]any, key string, maxPoints int, rng *rand.Rand) []map[string]any {
	key = strings.TrimSpace(key)
	if key == "" || maxPoints <= 0 || len(rows) <= maxPoints {
		return reservoirSampleRows(rows, maxPoints, rng)
	}

	groups := map[string][]map[string]any{}
	for _, row := range rows {
		groupValue := strings.TrimSpace(fmt.Sprintf("%v", row[key]))
		if groupValue == "" {
			groupValue = "(empty)"
		}
		groups[groupValue] = append(groups[groupValue], row)
	}
	if len(groups) == 0 {
		return reservoirSampleRows(rows, maxPoints, rng)
	}

	type alloc struct {
		key     string
		weight  float64
		desired int
	}
	keys := make([]string, 0, len(groups))
	for groupKey := range groups {
		keys = append(keys, groupKey)
	}
	sort.Strings(keys)

	n := len(rows)
	allocs := make([]alloc, 0, len(keys))
	totalAssigned := 0
	for _, groupKey := range keys {
		group := groups[groupKey]
		weight := float64(len(group)) / float64(n)
		desired := int(math.Floor(weight * float64(maxPoints)))
		if desired <= 0 {
			desired = 1
		}
		if desired > len(group) {
			desired = len(group)
		}
		totalAssigned += desired
		allocs = append(allocs, alloc{
			key:     groupKey,
			weight:  weight,
			desired: desired,
		})
	}

	for totalAssigned > maxPoints {
		reduced := false
		for i := range allocs {
			if allocs[i].desired <= 1 {
				continue
			}
			allocs[i].desired--
			totalAssigned--
			reduced = true
			if totalAssigned <= maxPoints {
				break
			}
		}
		if !reduced {
			break
		}
	}
	for totalAssigned < maxPoints {
		expanded := false
		for i := range allocs {
			groupSize := len(groups[allocs[i].key])
			if allocs[i].desired >= groupSize {
				continue
			}
			allocs[i].desired++
			totalAssigned++
			expanded = true
			if totalAssigned >= maxPoints {
				break
			}
		}
		if !expanded {
			break
		}
	}

	sampled := make([]map[string]any, 0, minInt(maxPoints, len(rows)))
	for _, item := range allocs {
		group := groups[item.key]
		part := reservoirSampleRows(group, item.desired, rng)
		sampled = append(sampled, part...)
	}
	if len(sampled) > maxPoints {
		sampled = sampled[:maxPoints]
	}
	return sampled
}

func reservoirSampleRows(rows []map[string]any, maxPoints int, rng *rand.Rand) []map[string]any {
	if maxPoints <= 0 || len(rows) <= maxPoints {
		out := make([]map[string]any, 0, len(rows))
		for _, row := range rows {
			out = append(out, row)
		}
		return out
	}
	if rng == nil {
		rng = rand.New(rand.NewSource(time.Now().UnixNano()))
	}
	reservoir := make([]map[string]any, 0, maxPoints)
	for i, row := range rows {
		if i < maxPoints {
			reservoir = append(reservoir, row)
			continue
		}
		j := rng.Intn(i + 1)
		if j < maxPoints {
			reservoir[j] = row
		}
	}
	return reservoir
}

// BuildChartPlotSpec regenerates a normalized plot spec for filtered rows.
func BuildChartPlotSpec(rows []map[string]any, input ChartRebuildInput, title string) (ChartRebuildResult, error) {
	if len(rows) == 0 {
		return ChartRebuildResult{}, fmt.Errorf("cannot rebuild chart: rows are empty")
	}
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind == "" {
		kind = "scatter"
	}
	if kind != "scatter" && kind != "line" && kind != "bar" {
		return ChartRebuildResult{}, fmt.Errorf("chart.kind must be one of scatter, line, bar")
	}

	engine := strings.ToLower(strings.TrimSpace(input.Engine))
	if engine == "" {
		engine = echartsEngineName
	}
	if engine != echartsEngineName && engine != highchartsEngineName {
		return ChartRebuildResult{}, fmt.Errorf("chart.engine must be echarts or highcharts")
	}

	chart := chartArtifactChartInput{
		Kind:   kind,
		Engine: engine,
		X:      strings.TrimSpace(input.X),
		Y:      strings.TrimSpace(input.Y),
		Color:  strings.TrimSpace(input.Color),
		Size:   strings.TrimSpace(input.Size),
	}
	if chart.X == "" || chart.Y == "" {
		return ChartRebuildResult{}, fmt.Errorf("chart.x and chart.y are required")
	}
	if strings.TrimSpace(title) == "" {
		title = "Filtered Chart"
	}

	var normalized *normalizedChartSpec
	switch engine {
	case echartsEngineName:
		raw := buildRawEchartsSpec(kind, chart, rows, title)
		spec, err := normalizeEchartsSpecInput(raw, "")
		if err != nil {
			return ChartRebuildResult{}, fmt.Errorf("failed to normalize generated echarts spec: %w", err)
		}
		normalized = spec
	case highchartsEngineName:
		raw := buildRawHighchartsSpec(kind, chart, rows, title)
		spec, err := normalizeChartSpecInput(raw, "")
		if err != nil {
			return ChartRebuildResult{}, fmt.Errorf("failed to normalize generated highcharts spec: %w", err)
		}
		normalized = spec
	}

	return ChartRebuildResult{
		PlotSpec:          normalized.PlotSpec,
		Engine:            engine,
		CapabilityVersion: normalized.CapabilityVersion,
		Module:            normalized.Module,
		Modules:           append([]string(nil), normalized.Modules...),
	}, nil
}

func buildRawEchartsSpec(kind string, chart chartArtifactChartInput, rows []map[string]any, title string) map[string]any {
	kind = strings.ToLower(strings.TrimSpace(kind))
	xKey := strings.TrimSpace(chart.X)
	yKey := strings.TrimSpace(chart.Y)
	colorKey := strings.TrimSpace(chart.Color)
	sizeKey := strings.TrimSpace(chart.Size)

	options := map[string]any{
		"title": map[string]any{
			"text": title,
		},
		"tooltip": map[string]any{
			"formatter_id": "echarts_tooltip_compact",
		},
		"grid": map[string]any{
			"containLabel": true,
			"right":        60,
		},
	}

	switch kind {
	case "scatter":
		options["xAxis"] = map[string]any{"type": "value", "name": xKey}
		options["yAxis"] = map[string]any{"type": "value", "name": yKey}
		series := buildEchartsScatterSeries(rows, xKey, yKey, colorKey, sizeKey)
		options["series"] = series
		if len(series) > 1 {
			options["legend"] = map[string]any{"show": true}
		}
	case "bar", "line":
		if colorKey != "" {
			categories, grouped := buildCategoricalSeriesByColor(rows, xKey, yKey, colorKey)
			series := make([]any, 0, len(grouped))
			for _, group := range grouped {
				series = append(series, map[string]any{
					"type": kind,
					"name": group.Name,
					"data": group.Data,
				})
			}
			options["xAxis"] = map[string]any{"type": "category", "name": xKey, "data": categories}
			options["yAxis"] = map[string]any{"type": "value", "name": yKey}
			options["series"] = series
			if len(series) > 1 {
				options["legend"] = map[string]any{"show": true}
			}
			break
		}
		xValues := make([]any, 0, len(rows))
		yValues := make([]any, 0, len(rows))
		for _, row := range rows {
			xValues = append(xValues, row[xKey])
			yValues = append(yValues, row[yKey])
		}
		options["xAxis"] = map[string]any{"type": "category", "name": xKey, "data": xValues}
		options["yAxis"] = map[string]any{"type": "value", "name": yKey}
		options["series"] = []any{
			map[string]any{
				"type": kind,
				"name": yKey,
				"data": yValues,
			},
		}
	}

	return map[string]any{
		"schema_version": EchartsPlotSpecVersionV1,
		"module":         "all",
		"options":        options,
	}
}

func buildEchartsScatterSeries(rows []map[string]any, xKey string, yKey string, colorKey string, sizeKey string) []any {
	if colorKey == "" {
		points := make([]any, 0, len(rows))
		for _, row := range rows {
			xv, xok := coerceToFiniteNumber(row[xKey])
			yv, yok := coerceToFiniteNumber(row[yKey])
			if !xok || !yok {
				continue
			}
			if sizeKey != "" {
				if sv, sok := coerceToFiniteNumber(row[sizeKey]); sok {
					points = append(points, map[string]any{
						"value":      []any{xv, yv},
						"symbolSize": sv,
					})
					continue
				}
			}
			points = append(points, []any{xv, yv})
		}
		return []any{
			map[string]any{
				"type": "scatter",
				"name": yKey,
				"data": points,
			},
		}
	}

	grouped := map[string][]any{}
	order := make([]string, 0)
	for _, row := range rows {
		xv, xok := coerceToFiniteNumber(row[xKey])
		yv, yok := coerceToFiniteNumber(row[yKey])
		if !xok || !yok {
			continue
		}
		group := strings.TrimSpace(fmt.Sprintf("%v", row[colorKey]))
		if group == "" {
			group = "(empty)"
		}
		if _, exists := grouped[group]; !exists {
			order = append(order, group)
		}
		if sizeKey != "" {
			if sv, sok := coerceToFiniteNumber(row[sizeKey]); sok {
				grouped[group] = append(grouped[group], map[string]any{
					"value":      []any{xv, yv},
					"symbolSize": sv,
				})
				continue
			}
		}
		grouped[group] = append(grouped[group], []any{xv, yv})
	}
	sort.Strings(order)
	series := make([]any, 0, len(order))
	for _, group := range order {
		series = append(series, map[string]any{
			"type": "scatter",
			"name": group,
			"data": grouped[group],
		})
	}
	return series
}

type categoricalSeriesGroup struct {
	Name string
	Data []any
}

func buildCategoricalSeriesByColor(rows []map[string]any, xKey string, yKey string, colorKey string) ([]any, []categoricalSeriesGroup) {
	xOrder := make([]string, 0, len(rows))
	xValues := map[string]any{}
	groupSums := map[string]map[string]float64{}
	groupNames := make([]string, 0)

	for _, row := range rows {
		xLabel := strings.TrimSpace(fmt.Sprintf("%v", row[xKey]))
		if xLabel == "" {
			continue
		}
		if _, ok := xValues[xLabel]; !ok {
			xValues[xLabel] = row[xKey]
			xOrder = append(xOrder, xLabel)
		}

		group := strings.TrimSpace(fmt.Sprintf("%v", row[colorKey]))
		if group == "" {
			group = "(empty)"
		}
		if _, ok := groupSums[group]; !ok {
			groupSums[group] = map[string]float64{}
			groupNames = append(groupNames, group)
		}
		if yVal, ok := coerceToFiniteNumber(row[yKey]); ok {
			groupSums[group][xLabel] += yVal
		}
	}

	sort.Strings(groupNames)
	categories := make([]any, 0, len(xOrder))
	for _, label := range xOrder {
		categories = append(categories, xValues[label])
	}

	series := make([]categoricalSeriesGroup, 0, len(groupNames))
	for _, group := range groupNames {
		data := make([]any, 0, len(xOrder))
		for _, label := range xOrder {
			if value, ok := groupSums[group][label]; ok {
				data = append(data, value)
			} else {
				data = append(data, nil)
			}
		}
		series = append(series, categoricalSeriesGroup{
			Name: group,
			Data: data,
		})
	}
	return categories, series
}

func buildRawHighchartsSpec(kind string, chart chartArtifactChartInput, rows []map[string]any, title string) map[string]any {
	kind = strings.ToLower(strings.TrimSpace(kind))
	xKey := strings.TrimSpace(chart.X)
	yKey := strings.TrimSpace(chart.Y)
	colorKey := strings.TrimSpace(chart.Color)
	sizeKey := strings.TrimSpace(chart.Size)

	options := map[string]any{
		"chart": map[string]any{
			"type": kind,
		},
		"title": map[string]any{
			"text": title,
		},
		"tooltip": map[string]any{
			"formatter_id": "tooltip_compact",
		},
	}

	switch kind {
	case "scatter":
		options["xAxis"] = map[string]any{
			"title": map[string]any{"text": xKey},
		}
		options["yAxis"] = map[string]any{
			"title": map[string]any{"text": yKey},
		}
		options["series"] = buildHighchartsScatterSeries(rows, xKey, yKey, colorKey, sizeKey)
	case "bar", "line":
		if colorKey != "" {
			categories, grouped := buildCategoricalSeriesByColor(rows, xKey, yKey, colorKey)
			series := make([]any, 0, len(grouped))
			for _, group := range grouped {
				series = append(series, map[string]any{
					"type": kind,
					"name": group.Name,
					"data": group.Data,
				})
			}
			options["xAxis"] = map[string]any{
				"categories": categories,
				"title":      map[string]any{"text": xKey},
			}
			options["yAxis"] = map[string]any{
				"title": map[string]any{"text": yKey},
			}
			options["series"] = series
			break
		}
		categories := make([]any, 0, len(rows))
		values := make([]any, 0, len(rows))
		for _, row := range rows {
			categories = append(categories, row[xKey])
			values = append(values, row[yKey])
		}
		options["xAxis"] = map[string]any{
			"categories": categories,
			"title":      map[string]any{"text": xKey},
		}
		options["yAxis"] = map[string]any{
			"title": map[string]any{"text": yKey},
		}
		options["series"] = []any{
			map[string]any{
				"type": kind,
				"name": yKey,
				"data": values,
			},
		}
	}

	return map[string]any{
		"schema_version": HighchartsPlotSpecVersionV2,
		"module":         "core",
		"options":        options,
	}
}

func buildHighchartsScatterSeries(rows []map[string]any, xKey string, yKey string, colorKey string, sizeKey string) []any {
	seriesType := "scatter"
	if sizeKey != "" {
		seriesType = "bubble"
	}
	if colorKey == "" {
		points := make([]any, 0, len(rows))
		for _, row := range rows {
			xv, xok := coerceToFiniteNumber(row[xKey])
			yv, yok := coerceToFiniteNumber(row[yKey])
			if !xok || !yok {
				continue
			}
			if seriesType == "bubble" {
				if sv, sok := coerceToFiniteNumber(row[sizeKey]); sok {
					points = append(points, []any{xv, yv, sv})
				}
				continue
			}
			points = append(points, []any{xv, yv})
		}
		return []any{
			map[string]any{
				"type": seriesType,
				"name": yKey,
				"data": points,
			},
		}
	}

	grouped := map[string][]any{}
	order := make([]string, 0)
	for _, row := range rows {
		xv, xok := coerceToFiniteNumber(row[xKey])
		yv, yok := coerceToFiniteNumber(row[yKey])
		if !xok || !yok {
			continue
		}
		group := strings.TrimSpace(fmt.Sprintf("%v", row[colorKey]))
		if group == "" {
			group = "(empty)"
		}
		if _, exists := grouped[group]; !exists {
			order = append(order, group)
		}
		if seriesType == "bubble" {
			if sv, sok := coerceToFiniteNumber(row[sizeKey]); sok {
				grouped[group] = append(grouped[group], []any{xv, yv, sv})
			}
			continue
		}
		grouped[group] = append(grouped[group], []any{xv, yv})
	}
	sort.Strings(order)
	series := make([]any, 0, len(order))
	for _, group := range order {
		series = append(series, map[string]any{
			"type": seriesType,
			"name": group,
			"data": grouped[group],
		})
	}
	return series
}

func coerceToFiniteNumber(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case float32:
		n := float64(v)
		if math.IsNaN(n) || math.IsInf(n, 0) {
			return 0, false
		}
		return n, true
	case int:
		return float64(v), true
	case int8:
		return float64(v), true
	case int16:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	case uint:
		return float64(v), true
	case uint8:
		return float64(v), true
	case uint16:
		return float64(v), true
	case uint32:
		return float64(v), true
	case uint64:
		return float64(v), true
	case json.Number:
		if f, err := v.Float64(); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return 0, false
		}
		f, err := strconv.ParseFloat(s, 64)
		if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
			return f, true
		}
	}
	return 0, false
}

func isMissingValue(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func roundFloat(value float64, places int) float64 {
	if places < 0 {
		return value
	}
	factor := math.Pow(10, float64(places))
	return math.Round(value*factor) / factor
}

func cloneRowMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src))
	for key, value := range src {
		out[key] = value
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
