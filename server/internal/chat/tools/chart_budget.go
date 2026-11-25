package tools

import (
	"encoding/json"
	"strings"
)

const chartBudgetSchemaVersion = "chart-budget.v1"

type ChartBudgetReport struct {
	SchemaVersion      string   `json:"schema_version"`
	ChartKind          string   `json:"chart_kind,omitempty"`
	ChartEngine        string   `json:"chart_engine,omitempty"`
	RowCountTotal      int      `json:"row_count_total,omitempty"`
	PointsRequested    int      `json:"points_requested,omitempty"`
	PointsReturned     int      `json:"points_returned,omitempty"`
	SeriesCount        int      `json:"series_count,omitempty"`
	PlotSpecBytes      int      `json:"plot_spec_bytes,omitempty"`
	RowsPreviewBytes   int      `json:"rows_preview_bytes,omitempty"`
	ResultPayloadBytes int      `json:"result_payload_bytes,omitempty"`
	SSEEventBytes      int      `json:"sse_event_bytes,omitempty"`
	BudgetActions      []string `json:"budget_actions,omitempty"`
	FailureStage       string   `json:"failure_stage,omitempty"`
	FailureCode        string   `json:"failure_code,omitempty"`
	FailureMessage     string   `json:"failure_message,omitempty"`
}

func NewChartBudgetReport(kind string, engine string) *ChartBudgetReport {
	return &ChartBudgetReport{
		SchemaVersion: chartBudgetSchemaVersion,
		ChartKind:     strings.TrimSpace(strings.ToLower(kind)),
		ChartEngine:   strings.TrimSpace(strings.ToLower(engine)),
	}
}

func (r *ChartBudgetReport) AddAction(action string) {
	if r == nil {
		return
	}
	action = strings.TrimSpace(action)
	if action == "" {
		return
	}
	for _, existing := range r.BudgetActions {
		if strings.EqualFold(existing, action) {
			return
		}
	}
	r.BudgetActions = append(r.BudgetActions, action)
}

func (r *ChartBudgetReport) SetFailure(stage string, code string, message string) {
	if r == nil {
		return
	}
	r.FailureStage = strings.TrimSpace(stage)
	r.FailureCode = strings.TrimSpace(code)
	r.FailureMessage = strings.TrimSpace(message)
}

func (r *ChartBudgetReport) AsMap() map[string]any {
	if r == nil {
		return nil
	}
	if strings.TrimSpace(r.SchemaVersion) == "" {
		r.SchemaVersion = chartBudgetSchemaVersion
	}
	b, err := json.Marshal(r)
	if err != nil {
		return map[string]any{
			"schema_version": chartBudgetSchemaVersion,
		}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{
			"schema_version": chartBudgetSchemaVersion,
		}
	}
	if strings.TrimSpace(anyToString(out["schema_version"])) == "" {
		out["schema_version"] = chartBudgetSchemaVersion
	}
	return out
}

func cloneChartBudgetMap(raw map[string]any) map[string]any {
	if raw == nil {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		out := make(map[string]any, len(raw))
		for key, value := range raw {
			out[key] = value
		}
		if _, ok := out["schema_version"]; !ok {
			out["schema_version"] = chartBudgetSchemaVersion
		}
		return out
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		out = make(map[string]any, len(raw))
		for key, value := range raw {
			out[key] = value
		}
	}
	if strings.TrimSpace(anyToString(out["schema_version"])) == "" {
		out["schema_version"] = chartBudgetSchemaVersion
	}
	return out
}

func structuredChartError(msg string, code string, hint string, stage string, budget *ChartBudgetReport) Output {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "unknown error"
	}
	result := map[string]any{
		"error": msg,
	}
	if code = strings.TrimSpace(code); code != "" {
		result["code"] = code
	}
	if hint = strings.TrimSpace(hint); hint != "" {
		result["hint"] = hint
	}
	if stage = strings.TrimSpace(stage); stage != "" {
		result["failure_stage"] = stage
	}
	if budget != nil {
		budget.SetFailure(stage, code, msg)
		result["chart_budget"] = budget.AsMap()
	}
	return Output{OK: false, ResultFormat: ResultFormatJSON, Result: result}
}
