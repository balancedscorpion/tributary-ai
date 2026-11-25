package tools

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/projects"
)

// --- Unit Tests ---

func TestDetectDelimiter(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected rune
	}{
		{"comma", "a,b,c\n1,2,3\n4,5,6\n", ','},
		{"tab", "a\tb\tc\n1\t2\t3\n4\t5\t6\n", '\t'},
		{"semicolon", "a;b;c\n1;2;3\n4;5;6\n", ';'},
		{"pipe", "a|b|c\n1|2|3\n4|5|6\n", '|'},
		{"empty", "", ','},
		{"single_column", "a\n1\n2\n3\n", ','}, // fallback
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := bytes.NewReader([]byte(tt.content))
			got := detectDelimiter(r)
			if got != tt.expected {
				t.Errorf("detectDelimiter() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestInferColumnType(t *testing.T) {
	tests := []struct {
		name     string
		values   []string
		expected ColumnType
	}{
		{"numbers", []string{"1", "2.5", "3", "-4.0"}, ColTypeNumber},
		{"formatted_numbers", []string{"$1,000", "$2,500.50", "$300"}, ColTypeNumber},
		{"percentage", []string{"10%", "20%", "30%"}, ColTypeNumber},
		{"booleans", []string{"true", "false", "true"}, ColTypeBoolean},
		{"yes_no_booleans", []string{"yes", "no", "yes", "no"}, ColTypeBoolean},
		{"dates_iso", []string{"2024-01-01", "2024-02-15", "2024-12-31"}, ColTypeDate},
		{"dates_us", []string{"01/15/2024", "02/28/2024"}, ColTypeDate},
		{"dates_datetime", []string{"2024-01-01T10:30:00", "2024-02-15 14:00"}, ColTypeDate},
		{"strings", []string{"hello", "world", "foo"}, ColTypeString},
		{"mixed_num_str", []string{"1", "abc", "3"}, ColTypeString},
		{"empty", []string{}, ColTypeString},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := inferColumnType(tt.values)
			if got != tt.expected {
				t.Errorf("inferColumnType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestCSVToolSchemasExposeClosedBoundedInputs(t *testing.T) {
	for _, tool := range []Tool{
		newDescribeDatasetTool(),
		newSampleRowsTool(),
		newFilterCSVTool(),
		newAggregateCSVTool(),
	} {
		def := tool.Definition()
		if got, ok := def.Function.Parameters["additionalProperties"].(bool); !ok || got {
			t.Fatalf("%s additionalProperties=%#v, want false", def.Function.Name, def.Function.Parameters["additionalProperties"])
		}
		properties := toolDefinitionProperties(t, def.Function.Parameters)
		if _, ok := properties["full_data"]; ok {
			t.Fatalf("%s schema should not advertise hidden full_data", def.Function.Name)
		}
	}

	sampleProperties := toolDefinitionProperties(t, newSampleRowsTool().Definition().Function.Parameters)
	requireIntegerRangeSchema(t, sampleProperties["count"], "sample_rows.count", 1, maxCSVTransformRows)
	requireIntegerRangeSchema(t, sampleProperties["offset"], "sample_rows.offset", 0, nil)

	filterProperties := toolDefinitionProperties(t, newFilterCSVTool().Definition().Function.Parameters)
	requireIntegerRangeSchema(t, filterProperties["limit"], "filter_csv.limit", 1, maxCSVTransformRows)
	requireStringEnumSchema(t, filterProperties["operator"], "filter_csv.operator", []string{"eq", "neq", "gt", "lt", "gte", "lte", "contains", "starts_with", "ends_with", "is_null", "not_null"})

	aggregateProperties := toolDefinitionProperties(t, newAggregateCSVTool().Definition().Function.Parameters)
	metrics, ok := aggregateProperties["metrics"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate_csv.metrics schema=%T, want object", aggregateProperties["metrics"])
	}
	if metrics["minItems"] != 1 {
		t.Fatalf("aggregate_csv.metrics.minItems=%#v, want 1", metrics["minItems"])
	}
	items, ok := metrics["items"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate_csv.metrics.items=%T, want object", metrics["items"])
	}
	if got, ok := items["additionalProperties"].(bool); !ok || got {
		t.Fatalf("aggregate_csv.metrics.items.additionalProperties=%#v, want false", items["additionalProperties"])
	}
	itemProperties, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("aggregate_csv.metrics.items.properties=%T, want object", items["properties"])
	}
	requireStringEnumSchema(t, itemProperties["function"], "aggregate_csv.metrics.function", []string{"count", "sum", "avg", "min", "max", "count_distinct"})
}

func requireIntegerRangeSchema(t *testing.T, raw any, path string, minimum int, maximum any) {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s schema=%T, want object", path, raw)
	}
	if schema["type"] != "integer" || schema["minimum"] != minimum {
		t.Fatalf("%s schema=%#v, want integer minimum %d", path, schema, minimum)
	}
	if maximum != nil && schema["maximum"] != maximum {
		t.Fatalf("%s maximum=%#v, want %#v; schema=%#v", path, schema["maximum"], maximum, schema)
	}
}

func requireStringEnumSchema(t *testing.T, raw any, path string, want []string) {
	t.Helper()
	schema, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("%s schema=%T, want object", path, raw)
	}
	if schema["type"] != "string" {
		t.Fatalf("%s type=%#v, want string", path, schema["type"])
	}
	got, ok := schema["enum"].([]string)
	if !ok {
		t.Fatalf("%s enum=%T, want []string; schema=%#v", path, schema["enum"], schema)
	}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s enum=%v, want %v", path, got, want)
	}
}

func TestParseNumber(t *testing.T) {
	tests := []struct {
		input    string
		expected float64
		wantErr  bool
	}{
		{"123", 123, false},
		{"-45.6", -45.6, false},
		{"$1,000", 1000, false},
		{"$1,234.56", 1234.56, false},
		{"50%", 50, false},
		{"abc", 0, true},
		{"", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseNumber(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseNumber(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err == nil && got != tt.expected {
				t.Errorf("parseNumber(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestProfileDataset(t *testing.T) {
	csvContent := "name,age,city,active\nAlice,30,NYC,true\nBob,25,LA,false\n,40,NYC,true\nCharlie,,SF,false\nDave,35,,true\n"

	ctx := context.Background()
	r := bytes.NewReader([]byte(csvContent))

	profile, err := profileDataset(ctx, r, "test.csv")
	if err != nil {
		t.Fatalf("profileDataset() error: %v", err)
	}

	if profile.RowCount != 5 {
		t.Errorf("RowCount = %d, want 5", profile.RowCount)
	}
	if profile.Path != "test.csv" {
		t.Errorf("Path = %q, want %q", profile.Path, "test.csv")
	}
	if len(profile.Columns) != 4 {
		t.Fatalf("len(Columns) = %d, want 4", len(profile.Columns))
	}

	// Check name column
	nameCol := profile.Columns[0]
	if nameCol.Name != "name" {
		t.Errorf("Columns[0].Name = %q, want %q", nameCol.Name, "name")
	}
	if nameCol.Type != ColTypeString {
		t.Errorf("Columns[0].Type = %q, want %q", nameCol.Type, ColTypeString)
	}
	if nameCol.NullCount != 1 {
		t.Errorf("Columns[0].NullCount = %d, want 1", nameCol.NullCount)
	}

	// Check age column (numeric)
	ageCol := profile.Columns[1]
	if ageCol.Type != ColTypeNumber {
		t.Errorf("Columns[1].Type = %q, want %q", ageCol.Type, ColTypeNumber)
	}
	if ageCol.NullCount != 1 {
		t.Errorf("Columns[1].NullCount = %d, want 1", ageCol.NullCount)
	}
	if ageCol.Mean == nil {
		t.Fatal("Columns[1].Mean is nil")
	}
	// Mean of 30, 25, 40, 35 = 32.5
	if math.Abs(*ageCol.Mean-32.5) > 0.01 {
		t.Errorf("Columns[1].Mean = %v, want ~32.5", *ageCol.Mean)
	}

	// Check active column (boolean)
	activeCol := profile.Columns[3]
	if activeCol.Type != ColTypeBoolean {
		t.Errorf("Columns[3].Type = %q, want %q", activeCol.Type, ColTypeBoolean)
	}
}

func TestProfileDatasetBOM(t *testing.T) {
	bom := "\xEF\xBB\xBFname,value\nfoo,1\nbar,2\n"
	ctx := context.Background()
	r := bytes.NewReader([]byte(bom))

	profile, err := profileDataset(ctx, r, "bom.csv")
	if err != nil {
		t.Fatalf("profileDataset() with BOM error: %v", err)
	}
	if profile.Columns[0].Name != "name" {
		t.Errorf("BOM not stripped: Columns[0].Name = %q, want %q", profile.Columns[0].Name, "name")
	}
}

func TestProfileDatasetEmpty(t *testing.T) {
	csvContent := "a,b,c\n"
	ctx := context.Background()
	r := bytes.NewReader([]byte(csvContent))

	profile, err := profileDataset(ctx, r, "empty.csv")
	if err != nil {
		t.Fatalf("profileDataset() error: %v", err)
	}
	if profile.RowCount != 0 {
		t.Errorf("RowCount = %d, want 0", profile.RowCount)
	}
	if len(profile.Columns) != 3 {
		t.Errorf("len(Columns) = %d, want 3", len(profile.Columns))
	}
}

func TestReservoirSampling(t *testing.T) {
	// Build a CSV with 10K rows
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	w.Write([]string{"id", "value"})
	for i := 0; i < 10000; i++ {
		w.Write([]string{fmt.Sprintf("%d", i), fmt.Sprintf("%d", i*10)})
	}
	w.Flush()

	// We won't include the header in the reader for reservoirSample
	allBytes := buf.Bytes()
	cr := csv.NewReader(bytes.NewReader(allBytes))
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true

	// Skip header
	cr.Read()

	ctx := context.Background()
	rows, total, err := reservoirSample(ctx, cr, 100)
	if err != nil {
		t.Fatalf("reservoirSample() error: %v", err)
	}
	if total != 10000 {
		t.Errorf("total = %d, want 10000", total)
	}
	if len(rows) != 100 {
		t.Errorf("len(rows) = %d, want 100", len(rows))
	}

	// Check that we got different IDs (not all from the start)
	ids := make(map[string]bool)
	for _, r := range rows {
		ids[r[0]] = true
	}
	// With 100 samples from 10K, probability of all being from first 200 is negligible
	hasLargeID := false
	for id := range ids {
		var num int
		fmt.Sscanf(id, "%d", &num)
		if num > 1000 {
			hasLargeID = true
			break
		}
	}
	if !hasLargeID {
		t.Error("reservoir sample appears biased toward start of file")
	}
}

func TestFilterEval(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		op       string
		cmpVal   string
		colType  ColumnType
		expected bool
	}{
		{"eq_str", "hello", "eq", "hello", ColTypeString, true},
		{"eq_str_ci", "Hello", "eq", "hello", ColTypeString, true},
		{"eq_num", "42", "eq", "42", ColTypeNumber, true},
		{"neq_str", "hello", "neq", "world", ColTypeString, true},
		{"gt_num", "50", "gt", "30", ColTypeNumber, true},
		{"gt_num_false", "10", "gt", "30", ColTypeNumber, false},
		{"lt_num", "10", "lt", "30", ColTypeNumber, true},
		{"gte_num", "30", "gte", "30", ColTypeNumber, true},
		{"lte_num", "30", "lte", "30", ColTypeNumber, true},
		{"contains", "hello world", "contains", "world", ColTypeString, true},
		{"contains_ci", "Hello World", "contains", "world", ColTypeString, true},
		{"starts_with", "hello world", "starts_with", "hello", ColTypeString, true},
		{"ends_with", "hello world", "ends_with", "world", ColTypeString, true},
		{"is_null_empty", "", "is_null", "", ColTypeString, true},
		{"is_null_notempty", "abc", "is_null", "", ColTypeString, false},
		{"not_null", "abc", "not_null", "", ColTypeString, true},
		{"not_null_empty", "", "not_null", "", ColTypeString, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := evalFilter(tt.value, tt.op, tt.cmpVal, tt.colType)
			if got != tt.expected {
				t.Errorf("evalFilter(%q, %q, %q, %q) = %v, want %v",
					tt.value, tt.op, tt.cmpVal, tt.colType, got, tt.expected)
			}
		})
	}
}

func TestAggregation(t *testing.T) {
	csvContent := "dept,salary,name\nEng,100,Alice\nEng,120,Bob\nSales,80,Charlie\nSales,90,Dave\nEng,110,Eve\n"

	ctx := context.Background()

	t.Run("ungrouped", func(t *testing.T) {
		r := bytes.NewReader([]byte(csvContent))
		results, err := aggregateCSV(ctx, r, nil, []MetricSpec{
			{Column: "salary", Function: "sum"},
			{Column: "salary", Function: "avg"},
			{Column: "salary", Function: "count"},
			{Column: "salary", Function: "min"},
			{Column: "salary", Function: "max"},
		})
		if err != nil {
			t.Fatalf("aggregateCSV() error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("len(results) = %d, want 1", len(results))
		}
		row := results[0]
		if row["sum(salary)"] != 500.0 {
			t.Errorf("sum(salary) = %v, want 500", row["sum(salary)"])
		}
		if row["count(salary)"] != int64(5) {
			t.Errorf("count(salary) = %v, want 5", row["count(salary)"])
		}
		if row["min(salary)"] != 80.0 {
			t.Errorf("min(salary) = %v, want 80", row["min(salary)"])
		}
		if row["max(salary)"] != 120.0 {
			t.Errorf("max(salary) = %v, want 120", row["max(salary)"])
		}
	})

	t.Run("grouped", func(t *testing.T) {
		r := bytes.NewReader([]byte(csvContent))
		results, err := aggregateCSV(ctx, r, []string{"dept"}, []MetricSpec{
			{Column: "salary", Function: "sum"},
			{Column: "salary", Function: "avg"},
			{Column: "name", Function: "count_distinct"},
		})
		if err != nil {
			t.Fatalf("aggregateCSV() error: %v", err)
		}
		if len(results) != 2 {
			t.Fatalf("len(results) = %d, want 2", len(results))
		}

		// Find Eng group
		var engRow map[string]any
		for _, row := range results {
			if row["dept"] == "Eng" {
				engRow = row
				break
			}
		}
		if engRow == nil {
			t.Fatal("Eng group not found")
		}
		if engRow["sum(salary)"] != 330.0 {
			t.Errorf("Eng sum(salary) = %v, want 330", engRow["sum(salary)"])
		}
		if engRow["count_distinct(name)"] != int64(3) {
			t.Errorf("Eng count_distinct(name) = %v, want 3", engRow["count_distinct(name)"])
		}
	})

	t.Run("group_cap", func(t *testing.T) {
		// Build CSV with > 1000 unique groups
		var buf bytes.Buffer
		buf.WriteString("id,val\n")
		for i := 0; i < 1100; i++ {
			buf.WriteString(fmt.Sprintf("g%d,1\n", i))
		}
		r := bytes.NewReader(buf.Bytes())
		results, err := aggregateCSV(ctx, r, []string{"id"}, []MetricSpec{
			{Column: "val", Function: "sum"},
		})
		if err != nil {
			t.Fatalf("aggregateCSV() error: %v", err)
		}
		if len(results) != 1000 {
			t.Errorf("group count = %d, want 1000 (capped)", len(results))
		}
	})

	t.Run("invalid_column", func(t *testing.T) {
		r := bytes.NewReader([]byte(csvContent))
		_, err := aggregateCSV(ctx, r, nil, []MetricSpec{
			{Column: "nonexistent", Function: "sum"},
		})
		if err == nil {
			t.Fatal("expected error for nonexistent column")
		}
	})
}

// --- Integration Tests using projects.Store ---

func setupTestProject(t *testing.T, files map[string]string) (*projects.Store, string) {
	t.Helper()
	dir := t.TempDir()
	ps, err := projects.NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	pid := "test-project"
	for name, content := range files {
		if err := ps.PutFile(pid, name, []byte(content)); err != nil {
			t.Fatalf("PutFile(%q): %v", name, err)
		}
	}
	return ps, pid
}

func execTool(t *testing.T, tool Tool, ps *projects.Store, pid string, args any) Output {
	t.Helper()
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	exec := ExecContext{
		ProjectID: pid,
		Projects:  ps,
	}

	out, err := tool.Execute(context.Background(), exec, json.RawMessage(b))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return out
}

func requireCSVResultMetadata(t *testing.T, out Output, kind string) map[string]any {
	t.Helper()
	if result, ok := out.Result.(map[string]any); ok {
		if _, nested := result["result_metadata"]; nested {
			t.Fatalf("result should not contain nested result_metadata: %#v", result)
		}
	}
	meta := out.ResultMetadata
	if meta["provider"] != "local_files" || meta["product"] != "csv" || meta["family"] != "data-analysis" || meta["kind"] != kind || meta["readonly"] != true {
		t.Fatalf("ResultMetadata=%#v, want CSV %s metadata", meta, kind)
	}
	toolName, _ := meta["tool_name"].(string)
	toolMeta, ok := ToolMetadataByName(toolName)
	if !ok {
		t.Fatalf("missing metadata for CSV tool %q", toolName)
	}
	if meta["tool_set_id"] != toolMeta.ToolSetID ||
		meta["category"] != string(toolMeta.Category) ||
		meta["origin"] != string(toolMeta.Origin) ||
		meta["native_viewer"] != toolMeta.Presentation.NativeViewer ||
		meta["renderer_strategy"] != toolMeta.Presentation.RendererStrategy ||
		meta["renderer_key"] != toolMeta.Presentation.RendererKey {
		t.Fatalf("ResultMetadata=%#v, want CSV catalog presentation %#v", meta, toolMeta.Presentation)
	}
	if _, ok := meta["tool_presentation"].(map[string]any); !ok {
		t.Fatalf("ResultMetadata missing tool_presentation: %#v", meta)
	}
	return meta
}

func TestCSVToolsUseFirstClassResultMetadata(t *testing.T) {
	raw, err := os.ReadFile("csv_tools.go")
	if err != nil {
		t.Fatalf("read csv_tools.go: %v", err)
	}
	if strings.Contains(string(raw), `"result_metadata"`) {
		t.Fatal("CSV tools should set Output.ResultMetadata directly instead of nesting result_metadata in result maps")
	}
}

func TestDescribeDatasetTool(t *testing.T) {
	ps, pid := setupTestProject(t, map[string]string{
		"data.csv": "name,age,score\nAlice,30,95.5\nBob,25,88.0\nCharlie,35,92.3\n",
	})

	tool := newDescribeDatasetTool()
	out := execTool(t, tool, ps, pid, map[string]any{"path": "data.csv"})

	if !out.OK {
		t.Fatalf("expected OK, got error: %v", out.Result)
	}

	// Marshal and unmarshal to check structure
	b, _ := json.Marshal(out.Result)
	var profile DatasetProfile
	if err := json.Unmarshal(b, &profile); err != nil {
		t.Fatalf("unmarshal profile: %v", err)
	}

	if profile.RowCount != 3 {
		t.Errorf("RowCount = %d, want 3", profile.RowCount)
	}
	if len(profile.Columns) != 3 {
		t.Errorf("len(Columns) = %d, want 3", len(profile.Columns))
	}
	if profile.Columns[0].Name != "name" {
		t.Errorf("first column = %q, want %q", profile.Columns[0].Name, "name")
	}
	meta := requireCSVResultMetadata(t, out, "dataset_profile")
	if meta["content_kind"] != "csv_profile" || meta["row_count"] != int64(3) || meta["column_count"] != 3 {
		t.Fatalf("ResultMetadata=%#v, want profile counts", meta)
	}
}

func TestDescribeDatasetToolNotCSV(t *testing.T) {
	ps, pid := setupTestProject(t, map[string]string{
		"data.txt": "hello world",
	})

	tool := newDescribeDatasetTool()
	out := execTool(t, tool, ps, pid, map[string]any{"path": "data.txt"})

	if out.OK {
		t.Error("expected error for non-CSV file")
	}
}

func TestSampleRowsToolSequential(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("id,value\n")
	for i := 1; i <= 100; i++ {
		buf.WriteString(fmt.Sprintf("%d,%d\n", i, i*10))
	}

	ps, pid := setupTestProject(t, map[string]string{
		"big.csv": buf.String(),
	})

	tool := newSampleRowsTool()

	// Test basic sample
	out := execTool(t, tool, ps, pid, map[string]any{
		"path":  "big.csv",
		"count": 5,
	})
	if !out.OK {
		t.Fatalf("expected OK: %v", out.Result)
	}

	b, _ := json.Marshal(out.Result)
	var result map[string]any
	json.Unmarshal(b, &result)

	rows := result["rows"].([]any)
	if len(rows) != 5 {
		t.Errorf("got %d rows, want 5", len(rows))
	}

	// Test with offset
	out2 := execTool(t, tool, ps, pid, map[string]any{
		"path":   "big.csv",
		"count":  3,
		"offset": 10,
	})
	if !out2.OK {
		t.Fatalf("expected OK: %v", out2.Result)
	}

	b2, _ := json.Marshal(out2.Result)
	var result2 map[string]any
	json.Unmarshal(b2, &result2)

	rows2 := result2["rows"].([]any)
	if len(rows2) != 3 {
		t.Errorf("got %d rows, want 3", len(rows2))
	}
	// First row at offset 10 should be id=11
	firstRow := rows2[0].(map[string]any)
	if firstRow["id"] != "11" {
		t.Errorf("first row id = %v, want %q", firstRow["id"], "11")
	}
}

func TestSampleRowsToolRandom(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("id,value\n")
	for i := 1; i <= 1000; i++ {
		buf.WriteString(fmt.Sprintf("%d,%d\n", i, i*10))
	}

	ps, pid := setupTestProject(t, map[string]string{
		"big.csv": buf.String(),
	})

	tool := newSampleRowsTool()
	out := execTool(t, tool, ps, pid, map[string]any{
		"path":   "big.csv",
		"count":  50,
		"random": true,
	})
	if !out.OK {
		t.Fatalf("expected OK: %v", out.Result)
	}

	b, _ := json.Marshal(out.Result)
	var result map[string]any
	json.Unmarshal(b, &result)

	totalRows := result["total_rows"].(float64)
	if totalRows != 1000 {
		t.Errorf("total_rows = %v, want 1000", totalRows)
	}

	rows := result["rows"].([]any)
	if len(rows) != 50 {
		t.Errorf("got %d rows, want 50", len(rows))
	}
}

func TestSampleRowsToolFullDataDoesNotIncreaseDefaultCount(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("id,value\n")
	for i := 1; i <= 200; i++ {
		buf.WriteString(fmt.Sprintf("%d,%d\n", i, i*10))
	}

	ps, pid := setupTestProject(t, map[string]string{
		"big.csv": buf.String(),
	})

	tool := newSampleRowsTool()
	out := execTool(t, tool, ps, pid, map[string]any{
		"path":      "big.csv",
		"full_data": true,
	})
	if !out.OK {
		t.Fatalf("expected OK: %v", out.Result)
	}
	b, _ := json.Marshal(out.Result)
	var result map[string]any
	json.Unmarshal(b, &result)

	rows := result["rows"].([]any)
	if len(rows) != 10 {
		t.Fatalf("len(rows)=%d, want default 10", len(rows))
	}
	if out.QueryMetadata["full_data_requested"] != true {
		t.Fatalf("query metadata full_data_requested=%#v, want true", out.QueryMetadata["full_data_requested"])
	}
	meta := requireCSVResultMetadata(t, out, "csv_rows")
	if meta["content_kind"] != "csv_rows" || meta["rows_returned"] != 10 || meta["sample_mode"] != "sequential" {
		t.Fatalf("ResultMetadata=%#v, want sequential row metadata", meta)
	}
	if out.QueryMetadata["provider"] != "csv" || out.QueryMetadata["tool_name"] != "sample_rows" {
		t.Fatalf("QueryMetadata=%#v, want sample_rows CSV metadata", out.QueryMetadata)
	}
}

func TestFilterCSVTool(t *testing.T) {
	csvContent := "name,age,city\nAlice,30,NYC\nBob,25,LA\nCharlie,40,NYC\nDave,35,SF\nEve,28,NYC\n"

	ps, pid := setupTestProject(t, map[string]string{
		"people.csv": csvContent,
	})

	tool := newFilterCSVTool()

	t.Run("eq", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path":     "people.csv",
			"column":   "city",
			"operator": "eq",
			"value":    "NYC",
		})
		if !out.OK {
			t.Fatalf("expected OK: %v", out.Result)
		}
		b, _ := json.Marshal(out.Result)
		var result map[string]any
		json.Unmarshal(b, &result)

		total := result["total_matches"].(float64)
		if total != 3 {
			t.Errorf("total_matches = %v, want 3", total)
		}
	})

	t.Run("gt_numeric", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path":     "people.csv",
			"column":   "age",
			"operator": "gt",
			"value":    "30",
		})
		if !out.OK {
			t.Fatalf("expected OK: %v", out.Result)
		}
		b, _ := json.Marshal(out.Result)
		var result map[string]any
		json.Unmarshal(b, &result)

		total := result["total_matches"].(float64)
		if total != 2 { // Charlie=40, Dave=35
			t.Errorf("total_matches = %v, want 2", total)
		}
	})

	t.Run("contains", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path":     "people.csv",
			"column":   "name",
			"operator": "contains",
			"value":    "li",
		})
		if !out.OK {
			t.Fatalf("expected OK: %v", out.Result)
		}
		b, _ := json.Marshal(out.Result)
		var result map[string]any
		json.Unmarshal(b, &result)

		total := result["total_matches"].(float64)
		if total != 2 { // Alice, Charlie
			t.Errorf("total_matches = %v, want 2", total)
		}
	})

	t.Run("invalid_column", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path":     "people.csv",
			"column":   "nonexistent",
			"operator": "eq",
			"value":    "x",
		})
		if out.OK {
			t.Error("expected error for nonexistent column")
		}
	})

	t.Run("invalid_operator", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path":     "people.csv",
			"column":   "name",
			"operator": "LIKE",
			"value":    "x",
		})
		if out.OK {
			t.Error("expected error for invalid operator")
		}
	})
}

func TestFilterCSVToolFullDataDoesNotIncreaseDefaultLimit(t *testing.T) {
	var buf strings.Builder
	buf.WriteString("id,city\n")
	for i := 1; i <= 300; i++ {
		buf.WriteString(fmt.Sprintf("%d,NYC\n", i))
	}

	ps, pid := setupTestProject(t, map[string]string{
		"people.csv": buf.String(),
	})
	tool := newFilterCSVTool()
	out := execTool(t, tool, ps, pid, map[string]any{
		"path":      "people.csv",
		"column":    "city",
		"operator":  "eq",
		"value":     "NYC",
		"full_data": true,
	})
	if !out.OK {
		t.Fatalf("expected OK: %v", out.Result)
	}
	b, _ := json.Marshal(out.Result)
	var result map[string]any
	json.Unmarshal(b, &result)
	rows := result["rows"].([]any)
	if len(rows) != defaultCSVTransformRows {
		t.Fatalf("len(rows)=%d, want default %d", len(rows), defaultCSVTransformRows)
	}
	if out.QueryMetadata["full_data_requested"] != true {
		t.Fatalf("query metadata full_data_requested=%#v, want true", out.QueryMetadata["full_data_requested"])
	}
	meta := requireCSVResultMetadata(t, out, "csv_filter")
	if meta["content_kind"] != "csv_rows" || meta["rows_returned"] != defaultCSVTransformRows || meta["total_matches"] != int64(300) {
		t.Fatalf("ResultMetadata=%#v, want bounded filter metadata", meta)
	}
	if out.QueryMetadata["provider"] != "csv" || out.QueryMetadata["tool_name"] != "filter_csv" {
		t.Fatalf("QueryMetadata=%#v, want filter_csv CSV metadata", out.QueryMetadata)
	}
}

func TestAggregateCSVTool(t *testing.T) {
	csvContent := "dept,salary,name\nEng,100,Alice\nEng,120,Bob\nSales,80,Charlie\nSales,90,Dave\nEng,110,Eve\n"

	ps, pid := setupTestProject(t, map[string]string{
		"employees.csv": csvContent,
	})

	tool := newAggregateCSVTool()

	t.Run("ungrouped_sum", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path": "employees.csv",
			"metrics": []map[string]any{
				{"column": "salary", "function": "sum"},
			},
		})
		if !out.OK {
			t.Fatalf("expected OK: %v", out.Result)
		}
		b, _ := json.Marshal(out.Result)
		var result map[string]any
		json.Unmarshal(b, &result)

		groupCount := result["group_count"].(float64)
		if groupCount != 1 {
			t.Errorf("group_count = %v, want 1", groupCount)
		}

		results := result["results"].([]any)
		row := results[0].(map[string]any)
		if row["sum(salary)"] != 500.0 {
			t.Errorf("sum(salary) = %v, want 500", row["sum(salary)"])
		}
		meta := requireCSVResultMetadata(t, out, "csv_aggregate")
		if meta["content_kind"] != "csv_rows" || meta["rows_returned"] != 1 || meta["group_count"] != 1 {
			t.Fatalf("ResultMetadata=%#v, want aggregate metadata", meta)
		}
	})

	t.Run("grouped", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path":     "employees.csv",
			"group_by": []string{"dept"},
			"metrics": []map[string]any{
				{"column": "salary", "function": "avg"},
				{"column": "salary", "function": "count"},
			},
		})
		if !out.OK {
			t.Fatalf("expected OK: %v", out.Result)
		}
		b, _ := json.Marshal(out.Result)
		var result map[string]any
		json.Unmarshal(b, &result)

		groupCount := result["group_count"].(float64)
		if groupCount != 2 {
			t.Errorf("group_count = %v, want 2", groupCount)
		}
	})

	t.Run("invalid_function", func(t *testing.T) {
		out := execTool(t, tool, ps, pid, map[string]any{
			"path": "employees.csv",
			"metrics": []map[string]any{
				{"column": "salary", "function": "median"},
			},
		})
		if out.OK {
			t.Error("expected error for invalid function")
		}
	})
}

func TestLargeCSVNoOOM(t *testing.T) {
	// Generate a 100K-row CSV file and ensure profiling completes without issues
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "large.csv")

	f, err := os.Create(csvPath)
	if err != nil {
		t.Fatalf("create file: %v", err)
	}

	w := csv.NewWriter(f)
	w.Write([]string{"id", "value", "category"})
	for i := 0; i < 100000; i++ {
		cat := "A"
		if i%3 == 1 {
			cat = "B"
		} else if i%3 == 2 {
			cat = "C"
		}
		w.Write([]string{fmt.Sprintf("%d", i), fmt.Sprintf("%.2f", float64(i)*1.5), cat})
	}
	w.Flush()
	f.Close()

	// Read file content
	data, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	ctx := context.Background()
	r := bytes.NewReader(data)
	profile, err := profileDataset(ctx, r, "large.csv")
	if err != nil {
		t.Fatalf("profileDataset: %v", err)
	}

	if profile.RowCount != 100000 {
		t.Errorf("RowCount = %d, want 100000", profile.RowCount)
	}
	if len(profile.Columns) != 3 {
		t.Errorf("len(Columns) = %d, want 3", len(profile.Columns))
	}

	// Check that value column is numeric with stats
	valCol := profile.Columns[1]
	if valCol.Type != ColTypeNumber {
		t.Errorf("value column type = %q, want %q", valCol.Type, ColTypeNumber)
	}
	if valCol.Mean == nil {
		t.Error("value column Mean is nil")
	}
	if valCol.Median == nil {
		t.Error("value column Median is nil")
	}

	// Category should have cardinality 3
	catCol := profile.Columns[2]
	switch v := catCol.Cardinality.(type) {
	case int64:
		if v != 3 {
			t.Errorf("category cardinality = %d, want 3", v)
		}
	case float64:
		if v != 3 {
			t.Errorf("category cardinality = %v, want 3", v)
		}
	default:
		t.Errorf("unexpected cardinality type: %T", catCol.Cardinality)
	}
}

func TestMalformedCSV(t *testing.T) {
	// CSV with inconsistent field counts and bad quoting
	csvContent := "a,b,c\n1,2,3\n4,5\n6,7,8,9\n\"unclosed\n10,11,12\n"

	ctx := context.Background()
	r := bytes.NewReader([]byte(csvContent))

	profile, err := profileDataset(ctx, r, "malformed.csv")
	if err != nil {
		t.Fatalf("profileDataset should handle malformed CSV: %v", err)
	}
	// Should still get some rows
	if profile.RowCount == 0 {
		t.Error("expected some rows from malformed CSV")
	}
}

func TestSingleColumnCSV(t *testing.T) {
	csvContent := "name\nAlice\nBob\nCharlie\n"

	ctx := context.Background()
	r := bytes.NewReader([]byte(csvContent))

	profile, err := profileDataset(ctx, r, "single.csv")
	if err != nil {
		t.Fatalf("profileDataset: %v", err)
	}
	if len(profile.Columns) != 1 {
		t.Errorf("len(Columns) = %d, want 1", len(profile.Columns))
	}
	if profile.RowCount != 3 {
		t.Errorf("RowCount = %d, want 3", profile.RowCount)
	}
}

func TestWelford(t *testing.T) {
	w := &welford{}
	values := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	for _, v := range values {
		w.update(v)
	}

	if w.n != 8 {
		t.Errorf("n = %d, want 8", w.n)
	}
	expectedMean := 5.0
	if math.Abs(w.mean-expectedMean) > 0.001 {
		t.Errorf("mean = %v, want %v", w.mean, expectedMean)
	}
	if w.min != 2 {
		t.Errorf("min = %v, want 2", w.min)
	}
	if w.max != 9 {
		t.Errorf("max = %v, want 9", w.max)
	}
	// Sample variance (Bessel's correction, n-1): 32/7 ≈ 4.5714
	expectedVar := 32.0 / 7.0
	if math.Abs(w.variance()-expectedVar) > 0.001 {
		t.Errorf("variance = %v, want %v", w.variance(), expectedVar)
	}
	// Sample stddev
	expectedSD := math.Sqrt(expectedVar)
	if math.Abs(w.stddev()-expectedSD) > 0.001 {
		t.Errorf("stddev = %v, want %v", w.stddev(), expectedSD)
	}
}
