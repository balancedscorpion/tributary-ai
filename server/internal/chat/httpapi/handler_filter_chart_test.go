package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ai-analyst-local/internal/chat/store"
)

func setupFilterChartTestStore(t *testing.T) (store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}

	rows := []map[string]any{
		{"name": "Alice", "age": 30, "city": "NYC"},
		{"name": "Bob", "age": 25, "city": "LA"},
		{"name": "Carol", "age": 35, "city": "NYC"},
		{"name": "Dave", "age": 28, "city": "Chicago"},
		{"name": "Eve", "age": 40, "city": "LA"},
		{"name": "Frank", "age": 22, "city": "NYC"},
		{"name": "Grace", "age": 33, "city": "Chicago"},
		{"name": "Hank", "age": 45, "city": "LA"},
		{"name": "Ivy", "age": 29, "city": "NYC"},
		{"name": "Jack", "age": 31, "city": "Chicago"},
	}
	contentBytes, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(contentBytes)
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "filter-chart-1",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "People",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}

	return st, cfgDir, "filter-chart-1"
}

func postFilterChart(h *Handler, artifactID string, body any) *httptest.ResponseRecorder {
	bodyBytes, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/api/artifacts/"+artifactID+"/filter-chart", bytes.NewReader(bodyBytes))
	req.SetPathValue("artifactId", artifactID)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.handleFilterChart(rec, req)
	return rec
}

func TestFilterChartWithEqFilter(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{
			{Column: "city", Operator: "eq", Value: "NYC"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}

	// NYC rows: Alice, Carol, Frank, Ivy = 4
	total := int(payload["row_count_total"].(float64))
	if total != 4 {
		t.Fatalf("row_count_total=%d, want 4", total)
	}
	returned := int(payload["rows_returned"].(float64))
	if returned != 4 {
		t.Fatalf("rows_returned=%d, want 4", returned)
	}
	filtersApplied := int(payload["filters_applied"].(float64))
	if filtersApplied != 1 {
		t.Fatalf("filters_applied=%d, want 1", filtersApplied)
	}

	rows, ok := payload["rows"].([]any)
	if !ok {
		t.Fatalf("rows is not an array")
	}
	for _, r := range rows {
		row := r.(map[string]any)
		if row["city"] != "NYC" {
			t.Fatalf("expected city=NYC, got %v", row["city"])
		}
	}
}

func TestFilterChartEmptyFilters(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}

	total := int(payload["row_count_total"].(float64))
	if total != 10 {
		t.Fatalf("row_count_total=%d, want 10", total)
	}
	returned := int(payload["rows_returned"].(float64))
	if returned != 10 {
		t.Fatalf("rows_returned=%d, want 10", returned)
	}
}

func TestFilterChartNonexistentArtifact(t *testing.T) {
	st, cfgDir, _ := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, "nonexistent-artifact", filterChartRequest{
		Filters: []filterChartParam{},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d, want 404, body=%s", rec.Code, rec.Body.String())
	}
}

func TestFilterChartInvalidOperator(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{
			{Column: "city", Operator: "badop", Value: "NYC"},
		},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "invalid filter operator") {
		t.Fatalf("expected invalid operator error, body=%s", rec.Body.String())
	}
}

func TestFilterChartPagination(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{},
		Offset:  2,
		Limit:   3,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}

	total := int(payload["row_count_total"].(float64))
	if total != 10 {
		t.Fatalf("row_count_total=%d, want 10", total)
	}
	returned := int(payload["rows_returned"].(float64))
	if returned != 3 {
		t.Fatalf("rows_returned=%d, want 3", returned)
	}
	offset := int(payload["offset"].(float64))
	if offset != 2 {
		t.Fatalf("offset=%d, want 2", offset)
	}
	limit := int(payload["limit"].(float64))
	if limit != 3 {
		t.Fatalf("limit=%d, want 3", limit)
	}
	if payload["has_more"] != true {
		t.Fatalf("has_more=%v, want true", payload["has_more"])
	}

	// Verify the rows are the correct slice (offset=2, limit=3 means rows 2,3,4)
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) != 3 {
		t.Fatalf("rows=%#v, want length 3", payload["rows"])
	}
	first := rows[0].(map[string]any)
	if first["name"] != "Carol" {
		t.Fatalf("first row name=%v, want Carol", first["name"])
	}
}

func TestFilterChartMultipleFilters(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	// Filter: city=NYC AND age > 29 -> Alice(30), Carol(35), Ivy(29 excluded) = 2
	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{
			{Column: "city", Operator: "eq", Value: "NYC"},
			{Column: "age", Operator: "gt", Value: "29"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}

	total := int(payload["row_count_total"].(float64))
	if total != 2 {
		t.Fatalf("row_count_total=%d, want 2", total)
	}
	filtersApplied := int(payload["filters_applied"].(float64))
	if filtersApplied != 2 {
		t.Fatalf("filters_applied=%d, want 2", filtersApplied)
	}
}

func TestFilterChartMissingColumn(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartTestStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{
			{Column: "", Operator: "eq", Value: "NYC"},
		},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400, body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "filter column is required") {
		t.Fatalf("expected column validation error, body=%s", rec.Body.String())
	}
}

func setupFilterChartSalesStore(t *testing.T) (store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	rows := []map[string]any{
		{"month": "2026-01", "region": "US", "revenue": 100},
		{"month": "2026-02", "region": "US", "revenue": 130},
		{"month": "2026-01", "region": "EU", "revenue": 90},
		{"month": "2026-02", "region": "EU", "revenue": 95},
	}
	contentBytes, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(contentBytes)
	meta := map[string]any{
		"plot_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "all",
			"options": map[string]any{
				"xAxis": map[string]any{"type": "category", "data": []any{"2026-01", "2026-02"}},
				"yAxis": map[string]any{"type": "value"},
				"series": []any{
					map[string]any{"type": "line", "name": "US", "data": []any{100, 130}},
					map[string]any{"type": "line", "name": "EU", "data": []any{90, 95}},
				},
			},
		},
		"chart_engine": "echarts",
	}
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "filter-chart-sales",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Sales",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		Metadata:    meta,
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	return st, cfgDir, "filter-chart-sales"
}

func TestFilterChartRebuildsPlotSpecFromChartInput(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartSalesStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{
			{Column: "region", Operator: "eq", Value: "US"},
		},
		Chart: &chartRebuildInput{
			Kind:   "line",
			Engine: "echarts",
			X:      "month",
			Y:      "revenue",
			Color:  "region",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	if got := int(payload["row_count_total"].(float64)); got != 2 {
		t.Fatalf("row_count_total=%d, want 2", got)
	}

	plotSpec, ok := payload["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", payload["plot_spec"])
	}
	options, ok := plotSpec["options"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec.options missing: %#v", plotSpec)
	}
	series, ok := options["series"].([]any)
	if !ok || len(series) != 1 {
		t.Fatalf("series=%#v, want single US series", options["series"])
	}
	first, _ := series[0].(map[string]any)
	if name := strings.TrimSpace(anyToStringTest(first["name"])); name != "US" {
		t.Fatalf("series name=%q, want US", name)
	}
	data, ok := first["data"].([]any)
	if !ok || len(data) != 2 {
		t.Fatalf("series data=%#v, want two points", first["data"])
	}
	if v := int(data[0].(float64)); v != 100 {
		t.Fatalf("data[0]=%d, want 100", v)
	}
	if v := int(data[1].(float64)); v != 130 {
		t.Fatalf("data[1]=%d, want 130", v)
	}
}

func setupFilterChartHistogramStore(t *testing.T) (store.Store, string, string) {
	t.Helper()
	ctx := context.Background()
	cfgDir := t.TempDir()
	st, err := store.NewJSON(cfgDir)
	if err != nil {
		t.Fatalf("NewJSON: %v", err)
	}
	thread, err := st.CreateThread(ctx, nil)
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	rows := []map[string]any{
		{"Age": 5, "Pclass": "1", "Sex": "female"},
		{"Age": 15, "Pclass": "1", "Sex": "female"},
		{"Age": 24, "Pclass": "3", "Sex": "male"},
		{"Age": 36, "Pclass": "3", "Sex": "male"},
	}
	contentBytes, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("json.Marshal rows: %v", err)
	}
	content := string(contentBytes)
	meta := map[string]any{
		"plot_spec": map[string]any{
			"schema_version": "echarts-plot-spec.v1",
			"module":         "all",
			"options": map[string]any{
				"xAxis": map[string]any{
					"type": "category",
					"name": "Age Group",
					"data": []any{"0-10", "10-20", "20-30", "30-40"},
				},
				"yAxis": map[string]any{"type": "value", "name": "Passenger Count"},
				"series": []any{
					map[string]any{
						"type": "bar",
						"name": "Passengers",
						"data": []any{10, 10, 10, 10},
					},
				},
			},
		},
		"chart_engine": "echarts",
	}
	if _, err := st.InsertArtifact(ctx, store.Artifact{
		ID:          "filter-chart-histogram",
		ThreadID:    thread.ID,
		Type:        "data",
		Title:       "Histogram",
		Storage:     "mysql",
		ContentText: &content,
		ByteSize:    int64(len(content)),
		Metadata:    meta,
	}); err != nil {
		t.Fatalf("InsertArtifact: %v", err)
	}
	return st, cfgDir, "filter-chart-histogram"
}

func TestFilterChartFallbackRebuildsHistogramSeries(t *testing.T) {
	st, cfgDir, artifactID := setupFilterChartHistogramStore(t)
	h := &Handler{store: st, cfgDir: cfgDir, maxBodyBytes: 1 << 20}

	rec := postFilterChart(h, artifactID, filterChartRequest{
		Filters: []filterChartParam{
			{Column: "Pclass", Operator: "eq", Value: "1"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal response: %v", err)
	}
	plotSpec, ok := payload["plot_spec"].(map[string]any)
	if !ok {
		t.Fatalf("plot_spec missing: %#v", payload["plot_spec"])
	}
	options, _ := plotSpec["options"].(map[string]any)
	series, _ := options["series"].([]any)
	first, _ := series[0].(map[string]any)
	data, _ := first["data"].([]any)
	if len(data) != 4 {
		t.Fatalf("series data len=%d, want 4", len(data))
	}
	if int(data[0].(float64)) != 1 || int(data[1].(float64)) != 1 || int(data[2].(float64)) != 0 || int(data[3].(float64)) != 0 {
		t.Fatalf("histogram counts=%#v, want [1 1 0 0]", data)
	}
}

func anyToStringTest(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return ""
	}
}
