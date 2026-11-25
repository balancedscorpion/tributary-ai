package optionc

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"ai-analyst-local/internal/chat/tools"
)

func normalizeSearchDepth(depth string) string {
	switch strings.TrimSpace(strings.ToLower(depth)) {
	case "off":
		return "off"
	case "deep":
		return "deep"
	default:
		return "shallow"
	}
}

func searchSystemPrompt(depth string) string {
	mode := normalizeSearchDepth(depth)
	base := strings.Join([]string{
		"You are the Search assistant.",
		"Goal: answer the user query using retrieval-grounded evidence.",
		"Keep answers concise and include evidence IDs (doc_id / artifact_id / message_id) for factual claims.",
		"When tools are available, prefer search-first retrieval and only fetch full content when necessary.",
		"If a context summary is present, treat it as an approximate guide, then query search_threads/list_messages/get_doc/get_artifact when precision is required.",
		"rlm_exec runs in Starlark only; use it for bounded retrieval loops and avoid Python file APIs.",
		"For async query tools, persist and reuse the returned job_id and owner token; poll status with the same thread context and owner token.",
		"When async status is pending/running, keep status-check outputs internal and avoid noisy user-facing progress spam.",
		"If evidence is insufficient, say what is missing and provide best-effort guidance.",
	}, "\n")
	switch mode {
	case "off":
		return strings.Join([]string{
			base,
			"Depth mode is OFF: do not call tools unless absolutely required.",
		}, "\n")
	case "deep":
		return strings.Join([]string{
			base,
			"Depth mode is DEEP: use the rlm_exec tool to iteratively retrieve/summarize large context.",
			"Keep Starlark short and deterministic. Allowed helpers include search/get_doc/list_messages/get_artifact/prompt helpers and finish().",
			"Avoid Python-style calls in rlm_exec: open(), read_file(), f\"...\"/f'...', dir() with no argument, and search(..., max_results=). Use limit= instead.",
			"Prefer short Starlark snippets and call finish(text, evidence) when done.",
		}, "\n")
	default:
		return strings.Join([]string{
			base,
			"Depth mode is SHALLOW: use search_threads/get_doc/get_artifact/list_messages as needed.",
		}, "\n")
	}
}

func embedRuntimeTemporalGuidance(systemPrompt string, now time.Time) string {
	base := strings.TrimSpace(systemPrompt)
	if now.IsZero() {
		now = time.Now().In(time.Local)
	}
	localNow := now
	zoneName, zoneOffsetSeconds := localNow.Zone()
	zoneName = strings.TrimSpace(zoneName)
	if zoneName == "" {
		zoneName = "local"
	}
	locationName := strings.TrimSpace(localNow.Location().String())
	if locationName == "" {
		locationName = zoneName
	}

	lines := []string{
		"[RUNTIME CONTEXT - DEVICE CLOCK]",
		"Current date: " + localNow.Format("Monday, January 2, 2006"),
		fmt.Sprintf("Current time: %s %s (%s)", localNow.Format("15:04:05"), zoneName, formatUTCOffset(zoneOffsetSeconds)),
		"Current timezone: " + locationName,
		"Current timestamp (ISO-8601): " + localNow.Format(time.RFC3339),
		"Use this as authoritative for temporal reasoning.",
		"Interpret relative dates like today/tomorrow/yesterday using this timestamp and timezone.",
	}
	if base == "" {
		return strings.Join(lines, "\n")
	}
	out := append([]string{base, ""}, lines...)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func formatUTCOffset(totalSeconds int) string {
	sign := "+"
	if totalSeconds < 0 {
		sign = "-"
		totalSeconds = -totalSeconds
	}
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	return fmt.Sprintf("UTC%s%02d:%02d", sign, hours, minutes)
}

func embedContextPlanningGuidance(systemPrompt string, threadID string, plan RunContextPlan, threadSearchEnabled bool) string {
	base := strings.TrimSpace(systemPrompt)
	needRetrieval := plan.NeedRetrieval || plan.NeedRetrievalTooling || plan.HardCapped
	if !needRetrieval && len(plan.SummarizedBuckets) == 0 && len(plan.RetrievalHints) == 0 {
		return base
	}

	var lines []string
	if base != "" {
		lines = append(lines, base)
		lines = append(lines, "")
	}
	lines = append(lines, "Context planning:")
	lines = append(lines, fmt.Sprintf("- plan_reason=%s, need_retrieval=%t, hard_capped=%t", strings.TrimSpace(plan.PlanReason), plan.NeedRetrieval || plan.NeedRetrievalTooling, plan.HardCapped))
	lines = append(lines, fmt.Sprintf("- kept_tokens=%d, planned_tokens=%d, summary_buckets=%d, summary_rounds=%d", plan.TokensBefore, plan.TokensAfter, len(plan.SummarizedBuckets), plan.SummaryRounds))
	if strings.TrimSpace(plan.CompactionMode) != "" {
		lines = append(lines, "- compaction_mode="+strings.TrimSpace(plan.CompactionMode))
	}
	if plan.HandoverSummaryUsed {
		lines = append(lines, fmt.Sprintf("- handover_summary_chars=%d", plan.HandoverSummaryChars))
	}

	if plan.HardCapped {
		lines = append(lines, "Use compact summary first, then retrieve additional evidence only when needed.")
	}
	if plan.HardCapped && plan.OverflowCompacted {
		lines = append(lines, "Near-capacity handoff is active: keep context compact, stay retrieval-first, and prefer shallow search before loading extra context.")
	}
	if plan.NeedRetrieval || plan.NeedRetrievalTooling {
		if threadSearchEnabled {
			lines = append(lines, "If factual confidence is low, retrieve evidence before answering and cite evidence IDs.")
			lines = append(lines, "Preferred evidence tools: rlm_exec, search_threads, list_messages, get_doc, get_artifact.")
		} else {
			lines = append(lines, "Search Threads is disabled for this run; use available context only, and ask the user to enable Search Threads when exact prior-chat evidence is required.")
		}
	}
	if strings.EqualFold(strings.TrimSpace(plan.CompactionMode), handoverCompactionMode) || plan.HandoverSummaryUsed || strings.EqualFold(strings.TrimSpace(plan.PlanReason), handoverCompactionMode) {
		lines = append(lines, "Handover-first compaction is active: treat the handover summary as compressed memory and verify key facts before final output.")
		if threadSearchEnabled {
			if tid := strings.TrimSpace(threadID); tid != "" {
				lines = append(lines, "- handover_thread_id="+tid)
				lines = append(lines, "When using list_messages, always pass thread_id="+tid+".")
			}
			lines = append(lines, "Verification tools: list_messages, search_threads, get_doc, get_artifact, rlm_exec.")
		} else if tid := strings.TrimSpace(threadID); tid != "" {
			lines = append(lines, "- handover_thread_id="+tid)
		}
	}

	if len(plan.RetrievalHints) > 0 {
		for i, hint := range plan.RetrievalHints {
			if i >= 3 {
				break
			}
			b := hint
			if b.QueryHint != "" {
				lines = append(lines, "- hint: "+b.QueryHint)
			}
			if len(b.CandidateMessageIDs) > 0 {
				if encoded, err := json.Marshal(b.CandidateMessageIDs); err == nil {
					lines = append(lines, "- candidate_message_ids: "+string(encoded))
				}
			}
			if len(b.CandidateArtifactIDs) > 0 {
				if encoded, err := json.Marshal(b.CandidateArtifactIDs); err == nil {
					lines = append(lines, "- candidate_artifact_ids: "+string(encoded))
				}
			}
			if b.MinScore > 0 {
				lines = append(lines, fmt.Sprintf("- min_score=%.2f", b.MinScore))
			}
			if strings.TrimSpace(b.OrderBy) != "" {
				lines = append(lines, "- order_by="+strings.TrimSpace(b.OrderBy))
			}
		}
	}
	if trace, err := compactContextPlanTrace(plan); err == nil && strings.TrimSpace(trace) != "" {
		lines = append(lines, "context_plan_trace: "+trace)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func embedRemoteQueryLimitGuidance(systemPrompt string, rowLimit int) string {
	base := strings.TrimSpace(systemPrompt)
	if rowLimit <= 0 {
		return base
	}
	families := datasourcePromptFamilies()
	remoteLabel := "Remote"
	if len(families) > 0 {
		remoteLabel = strings.Join(families, "/")
	}
	lines := []string{
		"Remote query output governance:",
		fmt.Sprintf("- %s query tools are capped at %d rows per page.", remoteLabel, rowLimit),
		"Use page_token pagination when you need to fetch more rows.",
		"When a query result is compacted, use its artifact_id with inspect_dataset_artifact, get_dataset_rows, search_dataset_rows, or aggregate_dataset instead of requesting a larger inline payload.",
		"Prefer aggregate_dataset for totals/group-bys and keep get_dataset_rows/search_dataset_rows pages to 50 rows or fewer.",
		"Do not ask for more rows in a single call than the configured per-page cap.",
	}
	lines = append([]string{base, ""}, lines...)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func embedAsyncQueryGuidance(systemPrompt string) string {
	base := strings.TrimSpace(systemPrompt)
	runTools := tools.AsyncQueryRunToolNames()
	runToolText := "run_bigquery_query or run_databricks_query or run_trino_query or run_snowflake_query"
	if len(runTools) > 0 {
		runToolText = strings.Join(runTools, " or ")
	}
	lines := []string{
		"Async query execution:",
		"Treat a query as durable external work: model turns, stream disconnects, polling errors, and ordinary elapsed time do not cancel it.",
		"Managed query tools durably reserve a job and automatically run the provider's EXPLAIN/dry-run/profile capability before submission when available. Do not invoke a second profile or duplicate query when status=profiling; retain the original job_id and owner_token and let orchestration continue it.",
		"Use returned query_profile and profile_outcome telemetry to assess scan/partition estimates, join shape, filters, warnings, and configured cost limits.",
		fmt.Sprintf("When %s returns any durable job_id, including status=profiling or ok=false, retain that exact job_id and owner_token.", runToolText),
		fmt.Sprintf("When calling %s, include a brief `description` argument (one sentence, max 300 characters) describing what the query answers.", runToolText),
		"Do useful independent work after submission. Do not busy-poll: the task orchestrator can suspend this model turn and resume it when the query reaches a meaningful state.",
		"Never cancel a query merely because it is slow, a status request failed, the current turn is ending, or the tool-iteration limit is near.",
		"Request cancellation only when the user asks, a hard configured budget is exceeded, the SQL is proven wrong, or provider telemetry confirms a non-recovering pathology.",
		"Only call get_query_results(job_id, owner_token) after status is completed.",
		"Treat tool-result envelopes as context previews; use paging and artifact retrieval for additional rows.",
		"If result rows are compacted or too dense to read, use the returned artifact_id with inspect_dataset_artifact, get_dataset_rows, search_dataset_rows, or aggregate_dataset.",
		"Prefer aggregate_dataset for totals/group-bys; use get_dataset_rows/search_dataset_rows only for focused pages of 50 rows or fewer.",
		"Do not request inline full-data payloads to bypass compaction safeguards.",
		"If profiling reports invalid, correct the SQL using catalog/schema evidence. If it reports over_budget, prune partitions/columns, aggregate or sample earlier, or obtain explicit approval for an appropriate configured budget. If it times out, errors, or is indeterminate, gather cheaper metadata or run a bounded preliminary query before replacing it.",
		"If a query is cancelled, failed, expired, lost, or profile-rejected, inspect its typed diagnostics and execute a safe alternative plan: correct and replace the query, split it into bounded stages, use a cheaper aggregate/sample, or ask for a material decision only when no safe path remains.",
		"When launching a corrected replacement, pass the exact stable query_obligation_id (qob-...) returned by the query tools; it always targets the obligation's current attempt, even after further replacements. Never substitute a job_id, statement_id, query_id, provider_job_id, next_uri, profile handle, nil, or zero identifier, and never detach or supersede unrelated query obligations.",
		"Do not give up on the user's task just because one query attempt did not complete.",
		"After results arrive, validate schema, row counts, null/range sanity, and key aggregates before relying on them in the final answer.",
		"Do not call get_query_results before completion.",
		"Do not finish the task while a required query or required result validation is unresolved; allow orchestration to wait and continue instead.",
	}
	lines = append([]string{base, ""}, lines...)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func embedToolErrorCommentaryGuidance(systemPrompt string) string {
	base := strings.TrimSpace(systemPrompt)
	lines := []string{
		"Tool error reporting:",
		"- Every tool result with ok=false is sent back to you as context.",
		"- When a tool fails, inspect result_metadata.diagnostic when present.",
		"- Before finishing the response, report the failed tool, the reported error, and the likely cause in plain language.",
		"- If multiple tools failed, cover each failed tool once; do not imply a failed operation succeeded.",
		"- If a retry or reconnect is appropriate, state that as the next step.",
	}
	if base == "" {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return strings.TrimSpace(strings.Join(append([]string{base, ""}, lines...), "\n"))
}

func embedChartingGuidance(systemPrompt string, enabled chartEngineEnablement) string {
	base := strings.TrimSpace(systemPrompt)
	lines := []string{"Charting guidance:"}
	if enabled.ChartProgramPipeline {
		lines = append(lines,
			"For advanced ECharts/Highcharts specs on large datasets, use display_table with source.artifact_id or source.path instead of constructing large data_json payloads.",
			"Use build_chart_artifact with source.artifact_id plus chart intent and transform options for intent-based scatter/line/bar charts; avoid manually listing large point arrays.",
			"For uploaded CSV files, call build_chart_artifact with source.path using the project-relative file path.",
			"Do not pass read_file artifact IDs into build_chart_artifact for CSV charting; use source.path or a row-based artifact (query/sample/filter results).",
			"Do not request full inline row payloads for charting; use artifact references and paging instead.",
			"If a chart tool result returns code DATA_TOO_LARGE_FOR_DISPLAY_TABLE, retry display_table with source.artifact_id or source.path for advanced specs; use build_chart_artifact for intent-based charts.",
		)
		if enabled.ChartDimensionFilters {
			lines = append(lines,
				"When the user asks to filter, subset, or focus a chart or table on specific dimension values (e.g. \"show only US\" or \"filter to Q3\"), use transform.filters on build_chart_artifact or the top-level filters parameter on display_table. Each filter object takes {column, operator, value} for single-value comparisons or {column, operator, values} for OR-matching multiple values. Multiple filter objects are AND-combined. Supported operators: eq, neq, gt, lt, gte, lte, contains, starts_with, ends_with, is_null, not_null.",
				"When dimension filtering is requested, prefer one chart/table artifact with reusable dimensions over creating separate charts per filter value unless the user explicitly asks for separate charts.",
			)
		} else {
			lines = append(lines,
				"Dimension-filter tool parameters are disabled in this run. Do not use transform.filters or display_table.filters.",
			)
		}
	} else {
		lines = append(lines,
			"Chart program pipeline is disabled; use display_table directly for chart rendering.",
		)
	}

	switch {
	case enabled.Echarts && enabled.Highcharts:
		lines = append(lines,
			"Prefer ECharts for advanced chart requests unless the user explicitly asks for Highcharts.",
			"Call get_echarts_capabilities before generating ECharts specs, then use display_table with echarts_spec.",
			"Default ECharts specs to module \"all\" unless the user asks for a narrower profile.",
			"Call get_highcharts_capabilities before generating Highcharts specs, then use display_table with chart_spec.",
			"Use exactly one chart spec field per display_table call (echarts_spec or chart_spec).",
			"Never generate executable JavaScript callbacks in chart specs. Use callback IDs only (including custom series render_item_id).",
			"When using dual y-axes in ECharts, set grid.right to at least 60 to prevent axis-name clipping.",
		)
	case enabled.Echarts:
		lines = append(lines,
			"Call get_echarts_capabilities before advanced chart generation, then use display_table with echarts_spec.",
			"Default ECharts specs to module \"all\" unless the user asks for a narrower profile.",
			"Do not generate executable JavaScript callbacks in chart specs. Use callback IDs only (including custom series render_item_id).",
			"When using dual y-axes in ECharts, set grid.right to at least 60 to prevent axis-name clipping.",
		)
	case enabled.Highcharts:
		lines = append(lines,
			"Call get_highcharts_capabilities before advanced chart generation, then use display_table with chart_spec.",
			"Do not generate executable JavaScript callbacks in chart specs. Use callback IDs only.",
		)
	default:
		lines = append(lines,
			"All advanced chart engines are disabled for this run.",
			"Use display_table without chart specs for table-only artifacts.",
		)
	}

	if base == "" {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return strings.TrimSpace(strings.Join(append([]string{base, ""}, lines...), "\n"))
}

func embedThreadTransferGuidance(systemPrompt string) string {
	base := strings.TrimSpace(systemPrompt)
	lines := []string{
		"Thread transfer guidance:",
		"If the user asks to export, share, or download the current thread, call export_thread instead of recreating the conversation manually.",
		"If export_thread is not available in the current run, explain the limitation instead of fabricating an export.",
	}
	if base == "" {
		return strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return strings.TrimSpace(strings.Join(append([]string{base, ""}, lines...), "\n"))
}

func embedAnthropicToolingGuidance(systemPrompt string, profile anthropicToolProfile) string {
	base := strings.TrimSpace(systemPrompt)
	families := make([]string, 0, 4)
	families = append(families, "file")
	if profile.includeCSV {
		families = append(families, "csv")
	}
	if profile.includeSearch {
		families = append(families, "search")
	}
	for _, capability := range tools.DatasourceCapabilities() {
		name := strings.TrimSpace(strings.ToLower(capability.Name))
		if name == "" {
			continue
		}
		if profile.datasourceEnabled[name] {
			families = append(families, name)
		}
	}
	line := "Anthropic tool scope for this run: " + strings.Join(families, ", ")
	if len(families) == 0 {
		line = "No tool families enabled for this run; answer from context only."
	}
	notes := []string{
		line,
		"Prefer available lightweight tool schemas and avoid broad capability exploration.",
	}

	if base == "" {
		return strings.TrimSpace(strings.Join(notes, "\n"))
	}
	out := append([]string{base, "", "Tooling guidance:"}, notes...)
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func datasourcePromptFamilies() []string {
	families := make([]string, 0, 3)
	for _, capability := range tools.DatasourceCapabilities() {
		name := strings.TrimSpace(strings.ToLower(capability.Name))
		if name == "" {
			continue
		}
		switch name {
		case "bigquery":
			families = append(families, "BigQuery")
		case "databricks":
			families = append(families, "Databricks")
		case "trino":
			families = append(families, "Trino")
		case "snowflake":
			families = append(families, "Snowflake")
		default:
			families = append(families, strings.ToUpper(name[:1])+name[1:])
		}
	}
	return families
}

func compactContextPlanTrace(plan RunContextPlan) (string, error) {
	compact := struct {
		PlanReason         string `json:"plan_reason"`
		NeedRetrieval      bool   `json:"need_retrieval"`
		NeedRetrievalTool  bool   `json:"need_retrieval_tooling"`
		HardCapped         bool   `json:"hard_capped"`
		OverflowCompacted  bool   `json:"overflow_compacted"`
		CompactionMode     string `json:"compaction_mode,omitempty"`
		HandoverUsed       bool   `json:"handover_summary_used,omitempty"`
		HandoverSummaryLen int    `json:"handover_summary_chars,omitempty"`
		TokensBefore       int    `json:"tokens_before"`
		TokensAfter        int    `json:"tokens_after"`
		SummaryBuckets     int    `json:"summary_buckets"`
		SummaryRounds      int    `json:"summary_rounds"`
		ArtifactRefsLoaded int    `json:"artifact_refs_loaded"`
		EvidenceBudget     int    `json:"evidence_budget"`
		EvidenceUsed       int    `json:"evidence_used"`
		EvidenceExhausted  bool   `json:"evidence_exhausted"`
		NeedSeqOrder       bool   `json:"need_seq_order"`
	}{}
	compact.PlanReason = strings.TrimSpace(plan.PlanReason)
	compact.NeedRetrieval = plan.NeedRetrieval
	compact.NeedRetrievalTool = plan.NeedRetrievalTooling
	compact.HardCapped = plan.HardCapped
	compact.OverflowCompacted = plan.OverflowCompacted
	compact.CompactionMode = strings.TrimSpace(plan.CompactionMode)
	compact.HandoverUsed = plan.HandoverSummaryUsed
	compact.HandoverSummaryLen = plan.HandoverSummaryChars
	compact.TokensBefore = plan.TokensBefore
	compact.TokensAfter = plan.TokensAfter
	compact.SummaryBuckets = len(plan.SummarizedBuckets)
	compact.SummaryRounds = plan.SummaryRounds
	compact.ArtifactRefsLoaded = plan.ArtifactRefsLoaded
	compact.EvidenceBudget = plan.EvidenceBudget
	compact.EvidenceUsed = plan.EvidenceUsed
	compact.EvidenceExhausted = plan.EvidenceExhausted
	b, err := json.Marshal(compact)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
