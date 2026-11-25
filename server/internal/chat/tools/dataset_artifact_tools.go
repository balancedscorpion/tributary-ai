package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"ai-analyst-local/internal/chat/store"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultDatasetArtifactSampleRows = 5
	maxDatasetArtifactSampleRows     = 25
	defaultDatasetArtifactRowsLimit  = 25
	maxDatasetArtifactRowsLimit      = 50
)

func datasetArtifactResultMetadata(toolName, kind string, artifact store.Artifact, extra map[string]any) map[string]any {
	meta := map[string]any{
		"provider":           "artifact",
		"product":            "dataset",
		"family":             "data-analysis",
		"kind":               kind,
		"tool_name":          toolName,
		"artifact_id":        artifact.ID,
		"source_artifact_id": artifact.ID,
		"artifact_type":      artifact.Type,
		"content_kind":       "json_rows",
		"readonly":           true,
	}
	if strings.TrimSpace(artifact.Title) != "" {
		meta["artifact_title"] = artifact.Title
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		meta[key] = value
	}
	return toolCatalogResultMetadata(toolName, kind, meta)
}

func newInspectDatasetArtifactTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "inspect_dataset_artifact",
				Description: "Inspect a persisted dataset artifact without loading every row into the model context. Returns row count, columns, inferred types, null counts, numeric stats, cardinality, sample values, and a small row sample. Use this first when query result rows are compacted.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Dataset artifact ID from a previous tool-result envelope.",
						},
						"sample_rows": map[string]any{
							"type":        "integer",
							"description": "Number of example rows to return (default 5, max 25).",
						},
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID string `json:"artifact_id"`
				SampleRows int    `json:"sample_rows,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifact, rows, errOut, ok := requireDatasetArtifactRows(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			sampleRows := clampPositiveInt(in.SampleRows, defaultDatasetArtifactSampleRows, maxDatasetArtifactSampleRows)
			profile := profileDatasetRows(ctx, rows)
			if profile.err != nil {
				return JSONError(profile.err.Error()), nil
			}
			out := map[string]any{
				"artifact_id":      artifact.ID,
				"title":            artifact.Title,
				"type":             artifact.Type,
				"byte_size":        artifact.ByteSize,
				"row_count":        len(rows),
				"column_count":     len(profile.columns),
				"columns":          profile.columns,
				"sample_rows":      sampleDatasetRows(rows, sampleRows),
				"sample_row_count": minInt(sampleRows, len(rows)),
				"usage":            "Use get_dataset_rows for focused pages, search_dataset_rows for text matches, and aggregate_dataset for totals or group-bys. Keep row pages small enough to read.",
			}
			if artifact.Metadata != nil {
				out["metadata"] = artifact.Metadata
			}
			return JSONOKWithMetadata(out, datasetArtifactResultMetadata("inspect_dataset_artifact", "dataset_profile", artifact, map[string]any{
				"content_kind":     "dataset_profile",
				"row_count":        len(rows),
				"column_count":     len(profile.columns),
				"sample_row_count": minInt(sampleRows, len(rows)),
			})), nil
		},
	}
}

func newGetDatasetRowsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "get_dataset_rows",
				Description: "Read a focused, paged slice of rows from a persisted dataset artifact. Use this instead of asking for full inline data when query results are compacted.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Dataset artifact ID from a previous tool-result envelope.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Rows to return for this page (default 25, max 50).",
						},
						"offset": map[string]any{
							"type":        "integer",
							"description": "Zero-based row offset after filters and sorting. Ignored when page_token is supplied.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned by a previous get_dataset_rows call.",
						},
						"columns": map[string]any{
							"type":        "array",
							"description": "Optional column projection.",
							"items":       map[string]any{"type": "string"},
						},
						"filters": datasetFilterParameterSchema(),
						"sort":    datasetSortParameterSchema(),
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID string                     `json:"artifact_id"`
				Limit      int                        `json:"limit,omitempty"`
				Offset     int                        `json:"offset,omitempty"`
				PageToken  string                     `json:"page_token,omitempty"`
				Columns    []string                   `json:"columns,omitempty"`
				Filters    []ChartArtifactFilterInput `json:"filters,omitempty"`
				Sort       *chartArtifactSortInput    `json:"sort,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifact, rows, errOut, ok := requireDatasetArtifactRows(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			filtered, err := ApplyChartFilters(rows, in.Filters)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			filtered, err = sortDatasetRows(filtered, in.Sort)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limit := clampPositiveInt(in.Limit, defaultDatasetArtifactRowsLimit, maxDatasetArtifactRowsLimit)
			offset := int64(in.Offset)
			if token, tokenOK := parsePageOffsetToken(in.PageToken); tokenOK {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if in.Offset < 0 || offset < 0 {
				return JSONError("offset must be >= 0"), nil
			}
			if offset > int64(len(filtered)) {
				return JSONError("offset out of range"), nil
			}

			start := int(offset)
			end := start + limit
			if end > len(filtered) {
				end = len(filtered)
			}
			window := filtered[start:end]
			columns := normalizeDatasetColumns(in.Columns)
			missingColumns := missingDatasetColumns(rows, columns)
			projected := projectDatasetRows(window, columns)
			hasMore := end < len(filtered)
			out := map[string]any{
				"artifact_id":        artifact.ID,
				"rows":               projected,
				"row_count":          len(rows),
				"filtered_row_count": len(filtered),
				"rows_returned":      len(projected),
				"page_size":          limit,
				"offset":             offset,
				"has_more":           hasMore,
			}
			if len(columns) > 0 {
				out["columns"] = columns
			}
			if len(missingColumns) > 0 {
				out["missing_columns"] = missingColumns
			}
			if hasMore {
				out["next_page_token"] = encodePageOffsetToken(int64(end))
			}
			return JSONOKWithMetadata(out, datasetArtifactResultMetadata("get_dataset_rows", "dataset_rows", artifact, map[string]any{
				"row_count_total":    len(rows),
				"filtered_row_count": len(filtered),
				"rows_returned":      len(projected),
				"page_size":          limit,
				"offset":             offset,
				"has_more":           hasMore,
			})), nil
		},
	}
}

func newAggregateDatasetTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "aggregate_dataset",
				Description: "Compute compact aggregations over a persisted dataset artifact. Prefer this for totals, averages, counts, distinct counts, and group-bys instead of reading many raw rows.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Dataset artifact ID from a previous tool-result envelope.",
						},
						"group_by": map[string]any{
							"type":        "array",
							"description": "Columns to group by. Omit for an overall aggregation.",
							"items":       map[string]any{"type": "string"},
						},
						"metrics": map[string]any{
							"type":        "array",
							"description": "Aggregation metrics. For count, column may be omitted to count rows.",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"column": map[string]any{
										"type":        "string",
										"description": "Column to aggregate. Optional only for count.",
									},
									"function": map[string]any{
										"type":        "string",
										"enum":        []string{"count", "sum", "avg", "min", "max", "count_distinct"},
										"description": "Aggregation function.",
									},
								},
								"required":             []string{"function"},
								"additionalProperties": false,
							},
						},
						"filters": datasetFilterParameterSchema(),
						"sort":    datasetSortParameterSchema(),
						"limit": map[string]any{
							"type":        "integer",
							"description": "Maximum aggregate rows to return after sorting (default 50, max 50).",
						},
					},
					"required":             []string{"artifact_id"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID string                     `json:"artifact_id"`
				GroupBy    []string                   `json:"group_by,omitempty"`
				Metrics    []MetricSpec               `json:"metrics,omitempty"`
				Filters    []ChartArtifactFilterInput `json:"filters,omitempty"`
				Sort       *chartArtifactSortInput    `json:"sort,omitempty"`
				Limit      int                        `json:"limit,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			artifact, rows, errOut, ok := requireDatasetArtifactRows(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			filtered, err := ApplyChartFilters(rows, in.Filters)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			metrics := normalizeDatasetMetrics(in.Metrics)
			aggregated, groupCapped, err := aggregateDatasetRows(ctx, filtered, normalizeDatasetColumns(in.GroupBy), metrics)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			aggregated, err = sortDatasetRows(aggregated, in.Sort)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			limit := clampPositiveInt(in.Limit, maxDatasetArtifactRowsLimit, maxDatasetArtifactRowsLimit)
			hasMore := len(aggregated) > limit
			returned := aggregated
			if hasMore {
				returned = aggregated[:limit]
			}
			out := map[string]any{
				"artifact_id":        artifact.ID,
				"rows":               returned,
				"row_count":          len(rows),
				"filtered_row_count": len(filtered),
				"group_count":        len(aggregated),
				"groups_returned":    len(returned),
				"group_by":           normalizeDatasetColumns(in.GroupBy),
				"metrics":            metrics,
				"group_capped":       groupCapped,
				"has_more":           hasMore,
			}
			if hasMore {
				out["note"] = "Aggregate output was limited. Add filters, sort by a metric, or run a more focused aggregation."
			}
			return JSONOKWithMetadata(out, datasetArtifactResultMetadata("aggregate_dataset", "dataset_aggregate", artifact, map[string]any{
				"row_count_total":    len(rows),
				"filtered_row_count": len(filtered),
				"rows_returned":      len(returned),
				"group_count":        len(aggregated),
				"groups_returned":    len(returned),
				"group_capped":       groupCapped,
				"has_more":           hasMore,
			})), nil
		},
	}
}

func newSearchDatasetRowsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "search_dataset_rows",
				Description: "Search text across rows in a persisted dataset artifact and return a compact page of matching rows. Use this when compacted query previews are missing a specific value or category.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"artifact_id": map[string]any{
							"type":        "string",
							"description": "Dataset artifact ID from a previous tool-result envelope.",
						},
						"query": map[string]any{
							"type":        "string",
							"description": "Text to search for.",
						},
						"columns": map[string]any{
							"type":        "array",
							"description": "Columns to search and return. Omit to search all columns.",
							"items":       map[string]any{"type": "string"},
						},
						"case_sensitive": map[string]any{
							"type":        "boolean",
							"description": "Whether matching is case sensitive. Default false.",
						},
						"limit": map[string]any{
							"type":        "integer",
							"description": "Matching rows to return (default 25, max 50).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned by a previous search_dataset_rows call.",
						},
						"filters": datasetFilterParameterSchema(),
					},
					"required":             []string{"artifact_id", "query"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			var in struct {
				ArtifactID    string                     `json:"artifact_id"`
				Query         string                     `json:"query"`
				Columns       []string                   `json:"columns,omitempty"`
				CaseSensitive bool                       `json:"case_sensitive,omitempty"`
				Limit         int                        `json:"limit,omitempty"`
				PageToken     string                     `json:"page_token,omitempty"`
				Filters       []ChartArtifactFilterInput `json:"filters,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			query := strings.TrimSpace(in.Query)
			if query == "" {
				return JSONError("query is required"), nil
			}
			artifact, rows, errOut, ok := requireDatasetArtifactRows(ctx, exec, in.ArtifactID)
			if !ok {
				return errOut, nil
			}
			startIndex := int64(0)
			if token, tokenOK := parsePageOffsetToken(in.PageToken); tokenOK {
				startIndex = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if startIndex < 0 || startIndex > int64(len(rows)) {
				return JSONError("page_token out of range"), nil
			}

			columns := normalizeDatasetColumns(in.Columns)
			limit := clampPositiveInt(in.Limit, defaultDatasetArtifactRowsLimit, maxDatasetArtifactRowsLimit)
			matches, totalMatches, nextToken := searchDatasetRows(rows, query, columns, in.Filters, in.CaseSensitive, int(startIndex), limit)
			out := map[string]any{
				"artifact_id":      artifact.ID,
				"query":            query,
				"rows":             matches,
				"row_count":        len(rows),
				"total_matches":    totalMatches,
				"matches_returned": len(matches),
				"offset":           startIndex,
				"has_more":         nextToken != "",
			}
			if len(columns) > 0 {
				out["columns"] = columns
			}
			if nextToken != "" {
				out["next_page_token"] = nextToken
			}
			return JSONOKWithMetadata(out, datasetArtifactResultMetadata("search_dataset_rows", "dataset_search", artifact, map[string]any{
				"row_count_total": len(rows),
				"total_matches":   totalMatches,
				"rows_returned":   len(matches),
				"offset":          startIndex,
				"has_more":        nextToken != "",
				"query":           query,
			})), nil
		},
	}
}

func datasetFilterParameterSchema() map[string]any {
	return map[string]any{
		"type":        "array",
		"description": "Optional row filters. AND across filters; OR within values.",
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
					"description": "OR-list of values.",
				},
				"value": map[string]any{
					"type":        "string",
					"description": "Single comparison value.",
				},
			},
			"required":             []string{"column", "operator"},
			"additionalProperties": false,
		},
	}
}

func datasetSortParameterSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "Optional sort before paging.",
		"properties": map[string]any{
			"by": map[string]any{
				"type":        "string",
				"description": "Column or aggregate output field to sort by.",
			},
			"direction": map[string]any{
				"type": "string",
				"enum": []string{"asc", "desc"},
			},
		},
		"required":             []string{"by"},
		"additionalProperties": false,
	}
}

func requireDatasetArtifactRows(ctx context.Context, exec ExecContext, artifactID string) (store.Artifact, []map[string]any, Output, bool) {
	if exec.Store == nil {
		return store.Artifact{}, nil, JSONError("store is not configured"), false
	}
	id := strings.TrimSpace(artifactID)
	if id == "" {
		return store.Artifact{}, nil, JSONError("artifact_id is required"), false
	}
	artifact, err := exec.Store.GetArtifact(ctx, id)
	if err != nil {
		return store.Artifact{}, nil, JSONError(err.Error()), false
	}
	rows, err := loadRowsFromArtifact(ctx, exec, id)
	if err != nil {
		return store.Artifact{}, nil, JSONError(err.Error()), false
	}
	if len(rows) == 0 {
		return store.Artifact{}, nil, JSONError("dataset artifact has no rows"), false
	}
	return artifact, rows, Output{}, true
}

type datasetRowProfile struct {
	columns []ColumnProfile
	err     error
}

func profileDatasetRows(ctx context.Context, rows []map[string]any) datasetRowProfile {
	columnNames := datasetColumnNames(rows)
	type colAcc struct {
		nullCount   int64
		welf        welford
		reservoir   []float64
		uniques     map[string]struct{}
		cardCapped  bool
		samples     []string
		sampleSet   map[string]struct{}
		typeSamples []string
		minStr      string
		maxStr      string
		hasStr      bool
	}
	cols := make(map[string]*colAcc, len(columnNames))
	for _, col := range columnNames {
		cols[col] = &colAcc{
			uniques:   map[string]struct{}{},
			sampleSet: map[string]struct{}{},
		}
	}
	rng := rand.New(rand.NewSource(1))
	for rowIndex, row := range rows {
		if rowIndex%ctxCheckInterval == 0 && rowIndex > 0 {
			select {
			case <-ctx.Done():
				return datasetRowProfile{err: ctx.Err()}
			default:
			}
		}
		for _, col := range columnNames {
			acc := cols[col]
			raw, exists := row[col]
			value := datasetValueString(raw)
			if !exists || strings.TrimSpace(value) == "" {
				acc.nullCount++
				continue
			}
			if len(acc.typeSamples) < 100 {
				acc.typeSamples = append(acc.typeSamples, value)
			}
			if _, seen := acc.sampleSet[value]; !seen && len(acc.samples) < maxSampleDisplay {
				acc.samples = append(acc.samples, value)
				acc.sampleSet[value] = struct{}{}
			}
			if !acc.cardCapped {
				acc.uniques[value] = struct{}{}
				if len(acc.uniques) > cardinalityCap {
					acc.cardCapped = true
				}
			}
			if num, err := parseNumber(value); err == nil {
				acc.welf.update(num)
				if int64(len(acc.reservoir)) < reservoirK {
					acc.reservoir = append(acc.reservoir, num)
				} else {
					j := rng.Int63n(acc.welf.n)
					if j < reservoirK {
						acc.reservoir[j] = num
					}
				}
			}
			if !acc.hasStr {
				acc.minStr = value
				acc.maxStr = value
				acc.hasStr = true
			} else {
				if value < acc.minStr {
					acc.minStr = value
				}
				if value > acc.maxStr {
					acc.maxStr = value
				}
			}
		}
	}

	profiles := make([]ColumnProfile, 0, len(columnNames))
	for _, col := range columnNames {
		acc := cols[col]
		colType := inferColumnType(acc.typeSamples)
		p := ColumnProfile{
			Name:         col,
			Type:         colType,
			NullCount:    acc.nullCount,
			SampleValues: acc.samples,
		}
		if colType == ColTypeNumber && acc.welf.n > 0 {
			p.Min = roundNum(acc.welf.min)
			p.Max = roundNum(acc.welf.max)
			mean := roundNum(acc.welf.mean)
			p.Mean = &mean
			sd := roundNum(acc.welf.stddev())
			p.Stddev = &sd
			if len(acc.reservoir) > 0 {
				sort.Float64s(acc.reservoir)
				mid := len(acc.reservoir) / 2
				var median float64
				if len(acc.reservoir)%2 == 0 {
					median = (acc.reservoir[mid-1] + acc.reservoir[mid]) / 2
				} else {
					median = acc.reservoir[mid]
				}
				median = roundNum(median)
				p.Median = &median
			}
		} else if acc.hasStr {
			p.Min = acc.minStr
			p.Max = acc.maxStr
		}
		if acc.cardCapped {
			p.Cardinality = ">10000"
		} else {
			p.Cardinality = int64(len(acc.uniques))
		}
		profiles = append(profiles, p)
	}
	return datasetRowProfile{columns: profiles}
}

func datasetColumnNames(rows []map[string]any) []string {
	seen := map[string]struct{}{}
	for _, row := range rows {
		for key := range row {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			seen[key] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for key := range seen {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func sampleDatasetRows(rows []map[string]any, limit int) []map[string]any {
	limit = clampPositiveInt(limit, defaultDatasetArtifactSampleRows, maxDatasetArtifactSampleRows)
	if limit > len(rows) {
		limit = len(rows)
	}
	out := make([]map[string]any, 0, limit)
	for i := 0; i < limit; i++ {
		out = append(out, cloneRowMap(rows[i]))
	}
	return out
}

func normalizeDatasetColumns(columns []string) []string {
	out := make([]string, 0, len(columns))
	seen := map[string]struct{}{}
	for _, col := range columns {
		col = strings.TrimSpace(col)
		if col == "" {
			continue
		}
		if _, ok := seen[col]; ok {
			continue
		}
		seen[col] = struct{}{}
		out = append(out, col)
	}
	return out
}

func missingDatasetColumns(rows []map[string]any, columns []string) []string {
	if len(columns) == 0 {
		return nil
	}
	available := map[string]struct{}{}
	for _, col := range datasetColumnNames(rows) {
		available[col] = struct{}{}
	}
	missing := make([]string, 0)
	for _, col := range columns {
		if _, ok := available[col]; !ok {
			missing = append(missing, col)
		}
	}
	return missing
}

func projectDatasetRows(rows []map[string]any, columns []string) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	if len(columns) == 0 {
		for _, row := range rows {
			out = append(out, cloneRowMap(row))
		}
		return out
	}
	for _, row := range rows {
		next := map[string]any{}
		for _, col := range columns {
			if value, ok := row[col]; ok {
				next[col] = value
			}
		}
		out = append(out, next)
	}
	return out
}

func sortDatasetRows(rows []map[string]any, sortInput *chartArtifactSortInput) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, cloneRowMap(row))
	}
	if sortInput == nil {
		return out, nil
	}
	sortKey := strings.TrimSpace(sortInput.By)
	if sortKey == "" {
		return nil, fmt.Errorf("sort.by is required when sort is provided")
	}
	desc := strings.EqualFold(strings.TrimSpace(sortInput.Direction), "desc")
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
	return out, nil
}

func normalizeDatasetMetrics(metrics []MetricSpec) []MetricSpec {
	if len(metrics) == 0 {
		return []MetricSpec{{Function: "count"}}
	}
	out := make([]MetricSpec, 0, len(metrics))
	for _, metric := range metrics {
		fn := strings.TrimSpace(strings.ToLower(metric.Function))
		col := strings.TrimSpace(metric.Column)
		if fn == "" {
			continue
		}
		out = append(out, MetricSpec{Column: col, Function: fn})
	}
	if len(out) == 0 {
		return []MetricSpec{{Function: "count"}}
	}
	return out
}

func aggregateDatasetRows(ctx context.Context, rows []map[string]any, groupBy []string, metrics []MetricSpec) ([]map[string]any, bool, error) {
	available := map[string]struct{}{}
	for _, col := range datasetColumnNames(rows) {
		available[col] = struct{}{}
	}
	for _, col := range groupBy {
		if _, ok := available[col]; !ok {
			return nil, false, fmt.Errorf("group_by column %q not found", col)
		}
	}
	validFns := map[string]bool{
		"count":          true,
		"sum":            true,
		"avg":            true,
		"min":            true,
		"max":            true,
		"count_distinct": true,
	}
	for _, metric := range metrics {
		if !validFns[metric.Function] {
			return nil, false, fmt.Errorf("invalid metric function %q", metric.Function)
		}
		if metric.Function != "count" && strings.TrimSpace(metric.Column) == "" {
			return nil, false, fmt.Errorf("metric column is required for %s", metric.Function)
		}
		if strings.TrimSpace(metric.Column) != "" {
			if _, ok := available[metric.Column]; !ok {
				return nil, false, fmt.Errorf("metric column %q not found", metric.Column)
			}
		}
	}

	type datasetGroupAcc struct {
		groupValues []string
		metrics     []metricAcc
	}
	groups := map[string]*datasetGroupAcc{}
	order := []string{}
	groupCapped := false

	for rowIndex, row := range rows {
		if rowIndex%ctxCheckInterval == 0 && rowIndex > 0 {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			default:
			}
		}
		groupValues := make([]string, 0, len(groupBy))
		for _, col := range groupBy {
			groupValues = append(groupValues, datasetValueString(row[col]))
		}
		key := strings.Join(groupValues, "\x00")
		if len(groupBy) == 0 {
			key = "__all__"
		}
		acc, ok := groups[key]
		if !ok {
			if len(groups) >= maxGroupCount {
				groupCapped = true
				continue
			}
			acc = &datasetGroupAcc{
				groupValues: append([]string(nil), groupValues...),
				metrics:     make([]metricAcc, len(metrics)),
			}
			for i, metric := range metrics {
				if metric.Function == "count_distinct" {
					acc.metrics[i].uniques = map[string]struct{}{}
				}
			}
			groups[key] = acc
			order = append(order, key)
		}
		for i, metric := range metrics {
			value := ""
			if strings.TrimSpace(metric.Column) != "" {
				value = datasetValueString(row[metric.Column])
			}
			ma := &acc.metrics[i]
			switch metric.Function {
			case "count":
				if strings.TrimSpace(metric.Column) == "" || strings.TrimSpace(value) != "" {
					ma.count++
				}
			case "sum", "avg":
				if num, err := parseNumber(value); err == nil {
					ma.count++
					ma.sum += num
				}
			case "min":
				if num, err := parseNumber(value); err == nil {
					if !ma.hasVal || num < ma.min {
						ma.min = num
						ma.hasVal = true
					}
					ma.count++
				}
			case "max":
				if num, err := parseNumber(value); err == nil {
					if !ma.hasVal || num > ma.max {
						ma.max = num
						ma.hasVal = true
					}
					ma.count++
				}
			case "count_distinct":
				if strings.TrimSpace(value) != "" {
					ma.uniques[value] = struct{}{}
					ma.count++
				}
			}
		}
	}

	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		acc := groups[key]
		row := map[string]any{}
		for i, col := range groupBy {
			if i < len(acc.groupValues) {
				row[col] = acc.groupValues[i]
			}
		}
		for i, metric := range metrics {
			ma := acc.metrics[i]
			label := datasetMetricLabel(metric)
			switch metric.Function {
			case "count":
				row[label] = ma.count
			case "sum":
				row[label] = roundNum(ma.sum)
			case "avg":
				if ma.count > 0 {
					row[label] = roundNum(ma.sum / float64(ma.count))
				} else {
					row[label] = nil
				}
			case "min":
				if ma.hasVal {
					row[label] = roundNum(ma.min)
				} else {
					row[label] = nil
				}
			case "max":
				if ma.hasVal {
					row[label] = roundNum(ma.max)
				} else {
					row[label] = nil
				}
			case "count_distinct":
				row[label] = int64(len(ma.uniques))
			}
		}
		out = append(out, row)
	}
	return out, groupCapped, nil
}

func datasetMetricLabel(metric MetricSpec) string {
	fn := strings.TrimSpace(metric.Function)
	col := strings.TrimSpace(metric.Column)
	if col == "" {
		return fn + "(*)"
	}
	return fn + "(" + col + ")"
}

func searchDatasetRows(rows []map[string]any, query string, columns []string, filters []ChartArtifactFilterInput, caseSensitive bool, startIndex int, limit int) ([]map[string]any, int, string) {
	needle := query
	if !caseSensitive {
		needle = strings.ToLower(needle)
	}
	searchColumns := columns
	if len(searchColumns) == 0 {
		searchColumns = datasetColumnNames(rows)
	}
	matches := make([]map[string]any, 0, limit)
	totalMatches := 0
	nextToken := ""
	for idx, row := range rows {
		if len(filters) > 0 && !chartFilterRowMatches(row, filters) {
			continue
		}
		if !datasetRowContains(row, searchColumns, needle, caseSensitive) {
			continue
		}
		totalMatches++
		if idx < startIndex {
			continue
		}
		if len(matches) >= limit {
			if nextToken == "" {
				nextToken = encodePageOffsetToken(int64(idx))
			}
			continue
		}
		projected := projectDatasetRows([]map[string]any{row}, columns)[0]
		projected["_row_index"] = idx
		matches = append(matches, projected)
	}
	return matches, totalMatches, nextToken
}

func datasetRowContains(row map[string]any, columns []string, needle string, caseSensitive bool) bool {
	for _, col := range columns {
		value := datasetValueString(row[col])
		if !caseSensitive {
			value = strings.ToLower(value)
		}
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func datasetValueString(value any) string {
	if value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case fmt.Stringer:
		return strings.TrimSpace(v.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}
