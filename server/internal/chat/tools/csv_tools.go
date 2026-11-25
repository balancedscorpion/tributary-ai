package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"ai-analyst-local/internal/chat/types"
)

func csvResultMetadata(toolName, kind, path string, extra map[string]any) map[string]any {
	meta := map[string]any{
		"provider":     "local_files",
		"product":      "csv",
		"family":       "data-analysis",
		"kind":         kind,
		"tool_name":    toolName,
		"source_path":  path,
		"content_kind": "csv",
		"readonly":     true,
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		meta[key] = value
	}
	return toolCatalogResultMetadata(toolName, kind, meta)
}

func csvQueryMetadata(toolName, path string, fullData bool, extra map[string]any) map[string]any {
	meta := map[string]any{
		"provider":            "csv",
		"tool_name":           toolName,
		"source_path":         path,
		"full_data_requested": fullData,
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		meta[key] = value
	}
	return meta
}

func newDescribeDatasetTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "describe_dataset",
				Description: "Profile a CSV file to get column names, inferred types, row count, null counts, numeric statistics (min, max, mean, median, stddev), string cardinality, and sample values — without loading the entire file. Use this first when working with any CSV file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Relative path to the CSV file (e.g. \"data/sales.csv\").",
						},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Path string `json:"path"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			displayPath, err := normalizeRelPathForDisplay(in.Path, false)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if !isCSVPath(displayPath) {
				return JSONError("describe_dataset only supports .csv files."), nil
			}

			f, err := ps.OpenFile(pid, displayPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", strings.TrimSpace(in.Path))), nil
			}
			defer f.Close()

			profile, err := profileDataset(ctx, f, displayPath)
			if err != nil {
				return JSONError(fmt.Sprintf("Error profiling CSV: %v", err)), nil
			}

			return JSONOKWithMetadata(profile, csvResultMetadata("describe_dataset", "dataset_profile", displayPath, map[string]any{
				"content_kind": "csv_profile",
				"row_count":    profile.RowCount,
				"column_count": len(profile.Columns),
			})), nil
		},
	}
}

func newSampleRowsTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "sample_rows",
				Description: "Return a sample of rows from a CSV file. Use sequential mode (default) to read rows from a specific offset, or random mode for a statistically representative sample across the entire file.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Relative path to the CSV file.",
						},
						"count": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"maximum":     maxCSVTransformRows,
							"description": "Number of rows to return (default 10, max 1000).",
						},
						"offset": map[string]any{
							"type":        "integer",
							"minimum":     0,
							"description": "Number of rows to skip before reading (default 0). Only used in sequential mode.",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from previous sequential page. Include to fetch the next page.",
						},
						"random": map[string]any{
							"type":        "boolean",
							"description": "If true, use reservoir sampling for a random sample across the entire file (default false).",
						},
					},
					"required":             []string{"path"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Path      string `json:"path"`
				Count     int    `json:"count,omitempty"`
				FullData  bool   `json:"full_data,omitempty"`
				Offset    int    `json:"offset,omitempty"`
				PageToken string `json:"page_token,omitempty"`
				Random    bool   `json:"random,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			displayPath, err := normalizeRelPathForDisplay(in.Path, false)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if !isCSVPath(displayPath) {
				return JSONError("sample_rows only supports .csv files."), nil
			}

			count := clampPositiveInt(in.Count, 10, maxCSVTransformRows)
			offset := int64(in.Offset)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if in.Offset < 0 {
				return JSONError("offset must be >= 0"), nil
			}
			if offset < 0 {
				return JSONError("Invalid page_token"), nil
			}

			f, err := ps.OpenFile(pid, displayPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", strings.TrimSpace(in.Path))), nil
			}
			defer f.Close()

			// Read full file for BOM handling and delimiter detection
			allBytes, err := io.ReadAll(f)
			if err != nil {
				return JSONError(fmt.Sprintf("Error reading file: %v", err)), nil
			}
			allBytes = stripBOM(allBytes)
			seekReader := bytes.NewReader(allBytes)

			delim := detectDelimiter(seekReader)
			seekReader.Seek(0, io.SeekStart)

			cr := newCSVReader(seekReader, delim)

			header, err := cr.Read()
			if err != nil {
				return JSONError("Error reading CSV header."), nil
			}

			if in.Random {
				if strings.TrimSpace(in.PageToken) != "" || offset > 0 {
					return JSONError("page_token and offset are not supported when random=true"), nil
				}
				// Reservoir sampling
				rows, totalRows, err := reservoirSample(ctx, cr, count)
				if err != nil {
					return JSONError(fmt.Sprintf("Error sampling: %v", err)), nil
				}
				mapped := make([]map[string]any, len(rows))
				for i, r := range rows {
					mapped[i] = rowToMap(header, r)
				}
				result := map[string]any{
					"columns":    header,
					"rows":       mapped,
					"total_rows": totalRows,
					"page_size":  count,
					"offset":     0,
					"has_more":   false,
				}
				meta := map[string]any{
					"content_kind":    "csv_rows",
					"rows_returned":   len(mapped),
					"row_count_total": totalRows,
					"page_size":       count,
					"offset":          0,
					"has_more":        false,
					"sample_mode":     "random",
				}
				return Output{
					OK:             true,
					ResultFormat:   ResultFormatJSON,
					Result:         result,
					QueryMetadata:  csvQueryMetadata("sample_rows", displayPath, in.FullData, meta),
					ResultMetadata: csvResultMetadata("sample_rows", "csv_rows", displayPath, meta),
				}, nil
			}

			// Sequential mode: skip offset, read count
			skipped := int64(0)
			for skipped < offset {
				_, err := cr.Read()
				if err == io.EOF {
					result := map[string]any{
						"columns":    header,
						"rows":       []map[string]any{},
						"total_rows": skipped,
						"page_size":  count,
						"offset":     offset,
						"has_more":   false,
					}
					meta := map[string]any{
						"content_kind":    "csv_rows",
						"rows_returned":   0,
						"row_count_total": skipped,
						"page_size":       count,
						"offset":          offset,
						"has_more":        false,
						"sample_mode":     "sequential",
					}
					return Output{
						OK:             true,
						ResultFormat:   ResultFormatJSON,
						Result:         result,
						QueryMetadata:  csvQueryMetadata("sample_rows", displayPath, in.FullData, meta),
						ResultMetadata: csvResultMetadata("sample_rows", "csv_rows", displayPath, meta),
					}, nil
				}
				if err != nil {
					continue
				}
				skipped++
			}

			var rows []map[string]any
			var totalRows int64 = offset
			collected := 0
			for {
				record, err := cr.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					continue
				}
				totalRows++
				if collected < count+1 {
					rows = append(rows, rowToMap(header, record))
					collected++
				}
				if collected > count {
					break
				}
			}
			hasMore := len(rows) > count
			if hasMore {
				rows = rows[:count]
			}
			if rows == nil {
				rows = []map[string]any{}
			}
			returned := len(rows)
			if hasMore {
				returned = count
			}

			out := map[string]any{
				"columns":    header,
				"rows":       rows,
				"total_rows": totalRows,
				"page_size":  count,
				"offset":     offset,
				"has_more":   hasMore,
			}
			if hasMore {
				nextOffset := offset + int64(returned)
				if nextOffset < 0 || nextOffset < offset {
					return JSONError("offset out of range"), nil
				}
				out["next_page_token"] = encodePageOffsetToken(nextOffset)
			}
			meta := map[string]any{
				"content_kind":   "csv_rows",
				"rows_returned":  len(rows),
				"rows_processed": totalRows,
				"page_size":      count,
				"offset":         offset,
				"has_more":       hasMore,
				"sample_mode":    "sequential",
			}
			if !hasMore {
				meta["row_count_total"] = totalRows
			}
			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				Result:         out,
				QueryMetadata:  csvQueryMetadata("sample_rows", displayPath, in.FullData, meta),
				ResultMetadata: csvResultMetadata("sample_rows", "csv_rows", displayPath, meta),
			}, nil
		},
	}
}

func newFilterCSVTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "filter_csv",
				Description: "Filter rows in a CSV file by a column condition. Streams through the file and returns matching rows up to the limit, plus total match count.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Relative path to the CSV file.",
						},
						"column": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Column name to filter on.",
						},
						"operator": map[string]any{
							"type":        "string",
							"enum":        []string{"eq", "neq", "gt", "lt", "gte", "lte", "contains", "starts_with", "ends_with", "is_null", "not_null"},
							"description": "Filter operator: eq, neq, gt, lt, gte, lte, contains, starts_with, ends_with, is_null, not_null.",
						},
						"value": map[string]any{
							"type":        "string",
							"description": "Value to compare against (not required for is_null/not_null).",
						},
						"limit": map[string]any{
							"type":        "integer",
							"minimum":     1,
							"maximum":     maxCSVTransformRows,
							"description": "Max rows to return (default 100, max 1000).",
						},
						"page_token": map[string]any{
							"type":        "string",
							"description": "Opaque token returned from previous page. Include to fetch the next page.",
						},
					},
					"required":             []string{"path", "column", "operator"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Path      string `json:"path"`
				Column    string `json:"column"`
				Operator  string `json:"operator"`
				Value     string `json:"value,omitempty"`
				Limit     int    `json:"limit,omitempty"`
				FullData  bool   `json:"full_data,omitempty"`
				PageToken string `json:"page_token,omitempty"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			displayPath, err := normalizeRelPathForDisplay(in.Path, false)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if !isCSVPath(displayPath) {
				return JSONError("filter_csv only supports .csv files."), nil
			}

			column := strings.TrimSpace(in.Column)
			if column == "" {
				return JSONError("column is required"), nil
			}
			operator := strings.TrimSpace(in.Operator)
			validOps := map[string]bool{
				"eq": true, "neq": true, "gt": true, "lt": true,
				"gte": true, "lte": true, "contains": true,
				"starts_with": true, "ends_with": true,
				"is_null": true, "not_null": true,
			}
			if !validOps[operator] {
				return JSONError(fmt.Sprintf("Invalid operator %q. Use: eq, neq, gt, lt, gte, lte, contains, starts_with, ends_with, is_null, not_null.", operator)), nil
			}

			limit := clampPositiveInt(in.Limit, defaultCSVTransformRows, maxCSVTransformRows)
			offset := int64(0)
			if token, ok := parsePageOffsetToken(in.PageToken); ok {
				offset = token
			} else if strings.TrimSpace(in.PageToken) != "" {
				return JSONError("Invalid page_token"), nil
			}
			if offset < 0 {
				return JSONError("Invalid page_token"), nil
			}

			f, err := ps.OpenFile(pid, displayPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", strings.TrimSpace(in.Path))), nil
			}
			defer f.Close()

			allBytes, err := io.ReadAll(f)
			if err != nil {
				return JSONError(fmt.Sprintf("Error reading file: %v", err)), nil
			}
			allBytes = stripBOM(allBytes)
			seekReader := bytes.NewReader(allBytes)

			delim := detectDelimiter(seekReader)
			seekReader.Seek(0, io.SeekStart)

			cr := newCSVReader(seekReader, delim)

			header, err := cr.Read()
			if err != nil {
				return JSONError("Error reading CSV header."), nil
			}

			// Find column index
			colIdx := -1
			for i, h := range header {
				if strings.TrimSpace(h) == column {
					colIdx = i
					break
				}
			}
			if colIdx < 0 {
				return JSONError(fmt.Sprintf("Column %q not found. Available columns: %s", column, strings.Join(header, ", "))), nil
			}

			// Infer column type from first 100 non-empty values for comparison
			var typeSamples []string
			var allRecords [][]string
			var rowCount int64

			for {
				if rowCount%ctxCheckInterval == 0 && rowCount > 0 {
					select {
					case <-ctx.Done():
						return JSONError("Operation cancelled."), nil
					default:
					}
				}

				record, err := cr.Read()
				if err == io.EOF {
					break
				}
				if err != nil {
					continue
				}
				rowCount++
				allRecords = append(allRecords, record)

				if colIdx < len(record) && len(typeSamples) < 100 {
					v := strings.TrimSpace(record[colIdx])
					if v != "" {
						typeSamples = append(typeSamples, v)
					}
				}
			}

			colType := inferColumnType(typeSamples)

			var rows []map[string]any
			var totalMatches int64
			for _, record := range allRecords {
				val := ""
				if colIdx < len(record) {
					val = strings.TrimSpace(record[colIdx])
				}
				if !evalFilter(val, operator, in.Value, colType) {
					continue
				}
				totalMatches++
				if totalMatches <= offset {
					continue
				}
				if int64(len(rows)) >= int64(limit) {
					continue
				}
				rows = append(rows, rowToMap(header, record))
			}
			hasMore := totalMatches > offset+int64(limit)
			if rows == nil {
				rows = []map[string]any{}
			}
			out := map[string]any{
				"columns":       header,
				"rows":          rows,
				"total_matches": totalMatches,
				"page_size":     limit,
				"offset":        offset,
				"has_more":      hasMore,
			}
			if hasMore {
				nextOffset := offset + int64(len(rows))
				if nextOffset < 0 || nextOffset < offset {
					return JSONError("offset out of range"), nil
				}
				out["next_page_token"] = encodePageOffsetToken(nextOffset)
			}
			meta := map[string]any{
				"content_kind":    "csv_rows",
				"rows_returned":   len(rows),
				"total_matches":   totalMatches,
				"row_count_total": rowCount,
				"page_size":       limit,
				"offset":          offset,
				"has_more":        hasMore,
				"filter_column":   column,
				"filter_operator": operator,
			}
			return Output{
				OK:             true,
				ResultFormat:   ResultFormatJSON,
				Result:         out,
				QueryMetadata:  csvQueryMetadata("filter_csv", displayPath, in.FullData, meta),
				ResultMetadata: csvResultMetadata("filter_csv", "csv_filter", displayPath, meta),
			}, nil
		},
	}
}

func newAggregateCSVTool() Tool {
	return ExecutableTool{
		Def: types.ToolDefinition{
			Type: "function",
			Function: types.ToolFunctionDefinition{
				Name:        "aggregate_csv",
				Description: "Compute group-by aggregations on a CSV file in a single pass. Supports count, sum, avg, min, max, and count_distinct functions. Returns results with an optional group-by breakdown (up to 1000 groups).",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"path": map[string]any{
							"type":        "string",
							"minLength":   1,
							"description": "Relative path to the CSV file.",
						},
						"group_by": map[string]any{
							"type":        "array",
							"items":       map[string]any{"type": "string"},
							"description": "Column names to group by (optional; omit for overall aggregation).",
						},
						"metrics": map[string]any{
							"type":     "array",
							"minItems": 1,
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"column": map[string]any{
										"type":        "string",
										"minLength":   1,
										"description": "Column name to aggregate.",
									},
									"function": map[string]any{
										"type":        "string",
										"enum":        []string{"count", "sum", "avg", "min", "max", "count_distinct"},
										"description": "Aggregation function: count, sum, avg, min, max, count_distinct.",
									},
								},
								"required":             []string{"column", "function"},
								"additionalProperties": false,
							},
							"description": "Array of {column, function} metrics to compute.",
						},
					},
					"required":             []string{"path", "metrics"},
					"additionalProperties": false,
				},
			},
		},
		Exec: func(ctx context.Context, exec ExecContext, args json.RawMessage) (Output, error) {
			ps, pid, errOut, ok := requireProject(exec)
			if !ok {
				return errOut, nil
			}

			var in struct {
				Path    string       `json:"path"`
				GroupBy []string     `json:"group_by,omitempty"`
				Metrics []MetricSpec `json:"metrics"`
			}
			if err := decodeArgs(args, &in); err != nil {
				return JSONError("Invalid JSON arguments."), nil
			}
			displayPath, err := normalizeRelPathForDisplay(in.Path, false)
			if err != nil {
				return JSONError(err.Error()), nil
			}
			if !isCSVPath(displayPath) {
				return JSONError("aggregate_csv only supports .csv files."), nil
			}

			if len(in.Metrics) == 0 {
				return JSONError("At least one metric is required."), nil
			}

			validFns := map[string]bool{
				"count": true, "sum": true, "avg": true,
				"min": true, "max": true, "count_distinct": true,
			}
			for _, m := range in.Metrics {
				if strings.TrimSpace(m.Column) == "" {
					return JSONError("Each metric must have a column name."), nil
				}
				if !validFns[strings.TrimSpace(m.Function)] {
					return JSONError(fmt.Sprintf("Invalid function %q. Use: count, sum, avg, min, max, count_distinct.", m.Function)), nil
				}
			}

			f, err := ps.OpenFile(pid, displayPath)
			if err != nil {
				return JSONError(fmt.Sprintf("File not found: %s", strings.TrimSpace(in.Path))), nil
			}
			defer f.Close()

			results, err := aggregateCSV(ctx, f, in.GroupBy, in.Metrics)
			if err != nil {
				return JSONError(fmt.Sprintf("Aggregation error: %v", err)), nil
			}
			if results == nil {
				results = []map[string]any{}
			}

			result := map[string]any{
				"results":     results,
				"group_count": len(results),
			}
			meta := csvResultMetadata("aggregate_csv", "csv_aggregate", displayPath, map[string]any{
				"content_kind":   "csv_rows",
				"rows_returned":  len(results),
				"group_count":    len(results),
				"group_by":       normalizeDatasetColumns(in.GroupBy),
				"metrics_count":  len(in.Metrics),
				"result_payload": "aggregate_rows",
			})
			return JSONOKWithMetadata(result, meta), nil
		},
	}
}

// isCSVPath checks if a file path has a .csv extension.
func isCSVPath(p string) bool {
	return strings.HasSuffix(strings.ToLower(p), ".csv")
}
