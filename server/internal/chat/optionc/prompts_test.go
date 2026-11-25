package optionc

import (
	"strings"
	"testing"
	"time"
)

func TestEmbedContextPlanningGuidanceIncludesHandoverThreadVerification(t *testing.T) {
	plan := RunContextPlan{
		NeedRetrieval:        true,
		NeedRetrievalTooling: true,
		PlanReason:           handoverCompactionMode,
		CompactionMode:       handoverCompactionMode,
		HandoverSummaryUsed:  true,
		HandoverSummaryChars: 512,
		TokensBefore:         12000,
		TokensAfter:          2400,
	}

	out := embedContextPlanningGuidance("base system prompt", "thread-abc", plan, true)
	mustContain := []string{
		"handover_thread_id=thread-abc",
		"When using list_messages, always pass thread_id=thread-abc.",
		"Verification tools: list_messages, search_threads, get_doc, get_artifact, rlm_exec.",
		"compaction_mode=handover_clean_context",
	}
	for _, pattern := range mustContain {
		if !strings.Contains(out, pattern) {
			t.Fatalf("output missing %q: %s", pattern, out)
		}
	}
}

func TestEmbedContextPlanningGuidanceSkipsHandoverHintsWhenInactive(t *testing.T) {
	plan := RunContextPlan{
		NeedRetrieval:        true,
		NeedRetrievalTooling: true,
		PlanReason:           "need_retrieval",
	}

	out := embedContextPlanningGuidance("base system prompt", "thread-abc", plan, true)
	mustNotContain := []string{
		"handover_thread_id=",
		"When using list_messages, always pass thread_id=",
	}
	for _, pattern := range mustNotContain {
		if strings.Contains(out, pattern) {
			t.Fatalf("unexpected %q in output: %s", pattern, out)
		}
	}
}

func TestEmbedContextPlanningGuidanceOmitsSearchToolsWhenDisabled(t *testing.T) {
	plan := RunContextPlan{
		NeedRetrieval:        true,
		NeedRetrievalTooling: true,
		PlanReason:           handoverCompactionMode,
		CompactionMode:       handoverCompactionMode,
		HandoverSummaryUsed:  true,
	}

	out := embedContextPlanningGuidance("base system prompt", "thread-abc", plan, false)
	if strings.Contains(out, "search_threads") || strings.Contains(out, "list_messages") || strings.Contains(out, "rlm_exec") {
		t.Fatalf("disabled Search Threads guidance should not name search tools: %s", out)
	}
	if !strings.Contains(out, "Search Threads is disabled for this run") {
		t.Fatalf("expected disabled Search Threads guidance: %s", out)
	}
}

func TestEmbedRuntimeTemporalGuidanceIncludesDeviceClockContext(t *testing.T) {
	now := time.Date(2026, time.February, 20, 14, 32, 0, 0, time.FixedZone("PST", -8*60*60))
	out := embedRuntimeTemporalGuidance("base system prompt", now)
	mustContain := []string{
		"base system prompt",
		"[RUNTIME CONTEXT - DEVICE CLOCK]",
		"Current date: Friday, February 20, 2026",
		"Current time: 14:32:00 PST (UTC-08:00)",
		"Current timezone: PST",
		"Current timestamp (ISO-8601): 2026-02-20T14:32:00-08:00",
		"Interpret relative dates like today/tomorrow/yesterday using this timestamp and timezone.",
	}
	for _, pattern := range mustContain {
		if !strings.Contains(out, pattern) {
			t.Fatalf("output missing %q: %s", pattern, out)
		}
	}
}

func TestFormatUTCOffsetSupportsHalfHourTimezones(t *testing.T) {
	if got := formatUTCOffset(5*60*60 + 30*60); got != "UTC+05:30" {
		t.Fatalf("formatUTCOffset(+5:30)=%q, want UTC+05:30", got)
	}
	if got := formatUTCOffset(-(3*60*60 + 30*60)); got != "UTC-03:30" {
		t.Fatalf("formatUTCOffset(-3:30)=%q, want UTC-03:30", got)
	}
}

func TestEmbedChartingGuidanceOmitsDisabledEngineTools(t *testing.T) {
	out := embedChartingGuidance("base", chartEngineEnablement{Echarts: true, Highcharts: false, ChartProgramPipeline: true})
	if strings.Contains(out, "get_highcharts_capabilities") {
		t.Fatalf("unexpected highcharts guidance in output: %s", out)
	}
	if !strings.Contains(out, "get_echarts_capabilities") {
		t.Fatalf("expected echarts guidance in output: %s", out)
	}
	if !strings.Contains(out, "module \"all\"") {
		t.Fatalf("expected module \"all\" guidance in output: %s", out)
	}
}

func TestEmbedChartingGuidanceIncludesBothEngines(t *testing.T) {
	out := embedChartingGuidance("base", chartEngineEnablement{Echarts: true, Highcharts: true, ChartProgramPipeline: true})
	if !strings.Contains(out, "get_echarts_capabilities") || !strings.Contains(out, "get_highcharts_capabilities") {
		t.Fatalf("expected both chart engine guidance entries: %s", out)
	}
	if !strings.Contains(strings.ToLower(out), "prefer echarts") {
		t.Fatalf("expected ECharts preference guidance when both engines are enabled: %s", out)
	}
	if !strings.Contains(out, "module \"all\"") {
		t.Fatalf("expected module \"all\" guidance in output: %s", out)
	}
}

func TestEmbedChartingGuidanceDualAxisGridPadding(t *testing.T) {
	const dualAxisGuidance = "When using dual y-axes in ECharts, set grid.right to at least 60"

	// Present when ECharts only
	echartsOnly := embedChartingGuidance("base", chartEngineEnablement{Echarts: true, Highcharts: false, ChartProgramPipeline: true})
	if !strings.Contains(echartsOnly, dualAxisGuidance) {
		t.Fatalf("expected dual-axis guidance when ECharts enabled: %s", echartsOnly)
	}

	// Present when both engines enabled
	both := embedChartingGuidance("base", chartEngineEnablement{Echarts: true, Highcharts: true, ChartProgramPipeline: true})
	if !strings.Contains(both, dualAxisGuidance) {
		t.Fatalf("expected dual-axis guidance when both engines enabled: %s", both)
	}

	// Absent when only Highcharts
	highchartsOnly := embedChartingGuidance("base", chartEngineEnablement{Echarts: false, Highcharts: true, ChartProgramPipeline: true})
	if strings.Contains(highchartsOnly, dualAxisGuidance) {
		t.Fatalf("unexpected dual-axis guidance when only Highcharts enabled: %s", highchartsOnly)
	}

	// Absent when neither enabled
	neither := embedChartingGuidance("base", chartEngineEnablement{Echarts: false, Highcharts: false, ChartProgramPipeline: true})
	if strings.Contains(neither, dualAxisGuidance) {
		t.Fatalf("unexpected dual-axis guidance when no engines enabled: %s", neither)
	}
}

func TestEmbedChartingGuidanceRespectsChartProgramPipelineFlag(t *testing.T) {
	enabled := embedChartingGuidance("base", chartEngineEnablement{Echarts: true, Highcharts: false, ChartProgramPipeline: true})
	if !strings.Contains(enabled, "build_chart_artifact") {
		t.Fatalf("expected build_chart_artifact guidance when pipeline is enabled: %s", enabled)
	}
	if !strings.Contains(enabled, "display_table with source.artifact_id or source.path") {
		t.Fatalf("expected source-backed display_table guidance when pipeline is enabled: %s", enabled)
	}
	if !strings.Contains(enabled, "source.path") {
		t.Fatalf("expected source.path guidance when pipeline is enabled: %s", enabled)
	}
	if !strings.Contains(enabled, "Do not pass read_file artifact IDs") {
		t.Fatalf("expected read_file-artifact avoidance guidance when pipeline is enabled: %s", enabled)
	}
	if !strings.Contains(enabled, "Do not request full inline row payloads for charting") {
		t.Fatalf("expected inline-row avoidance guidance when pipeline is enabled: %s", enabled)
	}

	disabled := embedChartingGuidance("base", chartEngineEnablement{Echarts: true, Highcharts: false, ChartProgramPipeline: false})
	if strings.Contains(disabled, "use display_table with source.artifact_id or source.path") {
		t.Fatalf("unexpected source-backed display_table guidance when pipeline is disabled: %s", disabled)
	}
	if strings.Contains(disabled, "Use build_chart_artifact with source.artifact_id plus chart intent") {
		t.Fatalf("unexpected build_chart_artifact usage guidance when pipeline is disabled: %s", disabled)
	}
	if !strings.Contains(disabled, "Chart program pipeline is disabled") {
		t.Fatalf("expected disabled-pipeline guidance when pipeline is disabled: %s", disabled)
	}
}

func TestEmbedChartingGuidanceDimensionFilterGuidanceFlag(t *testing.T) {
	enabled := embedChartingGuidance("base", chartEngineEnablement{
		Echarts:               true,
		Highcharts:            false,
		ChartProgramPipeline:  true,
		ChartDimensionFilters: true,
	})
	if !strings.Contains(enabled, "use transform.filters on build_chart_artifact") {
		t.Fatalf("expected transform.filters guidance when dimension filters are enabled: %s", enabled)
	}

	disabled := embedChartingGuidance("base", chartEngineEnablement{
		Echarts:               true,
		Highcharts:            false,
		ChartProgramPipeline:  true,
		ChartDimensionFilters: false,
	})
	if strings.Contains(disabled, "use transform.filters on build_chart_artifact") {
		t.Fatalf("unexpected transform.filters guidance when dimension filters are disabled: %s", disabled)
	}
	if !strings.Contains(disabled, "Dimension-filter tool parameters are disabled in this run") {
		t.Fatalf("expected disabled guidance for dimension filters: %s", disabled)
	}
}

func TestEmbedAsyncQueryGuidanceIncludesPreviewContract(t *testing.T) {
	out := embedAsyncQueryGuidance("base")
	if !strings.Contains(out, "tool-result envelopes as context previews") {
		t.Fatalf("expected preview-contract guidance in async query prompt: %s", out)
	}
	if !strings.Contains(out, "Do not request inline full-data payloads to bypass compaction safeguards") {
		t.Fatalf("expected inline full-data prohibition in async query prompt: %s", out)
	}
	for _, toolName := range []string{"inspect_dataset_artifact", "get_dataset_rows", "search_dataset_rows", "aggregate_dataset"} {
		if !strings.Contains(out, toolName) {
			t.Fatalf("expected dataset artifact guidance for %s in async query prompt: %s", toolName, out)
		}
	}
	for _, want := range []string{
		"dry-run/profile",
		"automatically run",
		"status=profiling",
		"any durable job_id",
		"invalid",
		"over_budget",
		"times out",
		"indeterminate",
		"Do not busy-poll",
		"Never cancel a query merely because it is slow",
		"execute a safe alternative plan",
		"pass the exact stable query_obligation_id (qob-...)",
		"targets the obligation's current attempt",
		"Never substitute a job_id, statement_id",
		"Do not give up on the user's task",
		"validate schema, row counts",
		"Do not finish the task while a required query",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected long-horizon guidance %q in async query prompt: %s", want, out)
		}
	}
	if strings.Contains(out, "Before finishing, cancel any unresolved running queries") {
		t.Fatalf("async query prompt still asks the model to cancel work at run completion: %s", out)
	}
}

func TestEmbedToolErrorCommentaryGuidanceRequiresWhy(t *testing.T) {
	out := embedToolErrorCommentaryGuidance("base")
	for _, want := range []string{"Tool error reporting", "ok=false", "reported error", "likely cause"} {
		if !strings.Contains(out, want) {
			t.Fatalf("embedToolErrorCommentaryGuidance missing %q:\n%s", want, out)
		}
	}
}

func TestEmbedAtlassianGuidanceIncludesFullJiraCapabilityScope(t *testing.T) {
	out := embedAtlassianGuidance("base", atlassianEnablement{Jira: true})
	for _, pattern := range []string{
		"jira_add_related_tickets",
		"jira_get_issue_link_types",
		"jira_update_issue",
		"jira_find_users",
		"mentions array",
		"accountId",
		"jira_get_issue_attachments",
		"jira_read_attachment",
		"jira_upload_attachment",
		"components",
		"target project/board",
	} {
		if !strings.Contains(out, pattern) {
			t.Fatalf("Atlassian guidance missing %q: %s", pattern, out)
		}
	}
}
