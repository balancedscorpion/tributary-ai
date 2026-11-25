package optionc

import (
	"encoding/json"
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

type ToolLoopGuard interface {
	BeforeCall(call types.ToolCall) (tools.Output, bool)
	RecordToolResult(call types.ToolCall, msg types.ChatMessage)
	MarkLatestDispatchCompacted(history []types.ChatMessage)
}

type ToolLoopGuardFactory interface {
	NewRunGuard() ToolLoopGuard
}

type NoopToolLoopGuardFactory struct{}

func (NoopToolLoopGuardFactory) NewRunGuard() ToolLoopGuard {
	return noopToolLoopGuard{}
}

type noopToolLoopGuard struct{}

func (noopToolLoopGuard) BeforeCall(call types.ToolCall) (tools.Output, bool) {
	_ = call
	return tools.Output{}, false
}

func (noopToolLoopGuard) RecordToolResult(call types.ToolCall, msg types.ChatMessage) {
	_, _ = call, msg
}

func (noopToolLoopGuard) MarkLatestDispatchCompacted(history []types.ChatMessage) {
	_ = history
}

type MonotonicToolLoopGuardConfig struct {
	// MaxNoProgressRepeats is how many consecutive identical calls with an
	// identical progress marker are tolerated before the guard blocks the
	// signature for the rest of the run. The default of 2 is deliberately
	// tight: for paging/read tools a repeat with no marker movement is
	// almost always a stuck model, and the one legitimate repeat-forever
	// case — async query status polling — is exempted explicitly via
	// isActiveAsyncQueryPollEnvelope rather than by loosening this number.
	MaxNoProgressRepeats int
}

type MonotonicToolLoopGuardFactory struct {
	config MonotonicToolLoopGuardConfig
}

func NewMonotonicToolLoopGuardFactory(cfg *MonotonicToolLoopGuardConfig) *MonotonicToolLoopGuardFactory {
	config := MonotonicToolLoopGuardConfig{
		MaxNoProgressRepeats: 2,
	}
	if cfg != nil && cfg.MaxNoProgressRepeats > 0 {
		config.MaxNoProgressRepeats = cfg.MaxNoProgressRepeats
	}
	return &MonotonicToolLoopGuardFactory{config: config}
}

func (f *MonotonicToolLoopGuardFactory) NewRunGuard() ToolLoopGuard {
	if f == nil {
		return noopToolLoopGuard{}
	}
	return &monotonicToolLoopGuard{
		maxNoProgressRepeats: maxInt(1, f.config.MaxNoProgressRepeats),
		entries:              map[string]*loopEntry{},
		callIDToSignature:    map[string]string{},
	}
}

type monotonicToolLoopGuard struct {
	maxNoProgressRepeats int
	entries              map[string]*loopEntry
	callIDToSignature    map[string]string
}

type loopEntry struct {
	toolName             string
	attempts             int
	stalledCount         int
	lastProgressMarker   string
	blockSameCallThisRun bool
	lastCompactionReason string
}

func (g *monotonicToolLoopGuard) BeforeCall(call types.ToolCall) (tools.Output, bool) {
	if g == nil {
		return tools.Output{}, false
	}
	sig := toolCallSignature(call)
	entry := g.entries[sig]
	if entry == nil {
		return tools.Output{}, false
	}
	if entry.blockSameCallThisRun {
		return loopGuardBlockedOutput(call, entry, "repeated identical call after compaction"), true
	}
	if entry.stalledCount >= g.maxNoProgressRepeats {
		return loopGuardBlockedOutput(call, entry, "repeated identical call without progress"), true
	}
	return tools.Output{}, false
}

func (g *monotonicToolLoopGuard) RecordToolResult(call types.ToolCall, msg types.ChatMessage) {
	if g == nil {
		return
	}
	sig := toolCallSignature(call)
	entry := g.entries[sig]
	if entry == nil {
		entry = &loopEntry{toolName: strings.TrimSpace(call.Name)}
		g.entries[sig] = entry
	}
	entry.attempts++
	callID := strings.TrimSpace(call.ID)
	if callID != "" {
		g.callIDToSignature[callID] = sig
	}

	env := parseToolEnvelope(msg.Content)
	if env == nil {
		return
	}
	if resultMeta, ok := env["result_metadata"].(map[string]any); ok {
		if parseBoolValue(resultMeta["compacted"]) {
			entry.blockSameCallThisRun = true
			entry.lastCompactionReason = strings.TrimSpace(asStringValue(resultMeta["compaction_stage"]))
		}
	}
	// Async-query polling tools legitimately repeat the same call with
	// identical arguments (and near-identical envelopes) until the job
	// reaches a terminal state. Counting those repeats as "no progress"
	// blocked check_query_status / get_query_results after
	// MaxNoProgressRepeats polls, so longer-running queries could never be
	// retrieved and the UI stayed in its running state forever. Skip stall
	// accounting while the poll reports an active job; once the job is
	// terminal, normal accounting resumes (so truly identical
	// post-completion repeats are still caught).
	if isActiveAsyncQueryPollEnvelope(call.Name, env) {
		return
	}
	progress := progressMarkerFromEnvelope(env)
	if strings.TrimSpace(progress) == "" {
		return
	}
	if progress == entry.lastProgressMarker {
		entry.stalledCount++
	} else {
		entry.stalledCount = 0
		entry.lastProgressMarker = progress
	}
}

// isActiveAsyncQueryPollEnvelope reports whether the tool result is a status
// poll for an async query job that is still in flight (or an explicitly
// retryable get_query_results call made before completion).
//
// State detection mirrors isPendingQueryJobState: providers surface job state
// under different keys and casings (the generic registry uses lowercase
// `status: running`; BigQuery-style payloads report uppercase `state:
// PENDING|RUNNING`; Databricks statement payloads report `state:
// PENDING|RUNNING` too), so we check the common key set case-insensitively
// in both `result` and `query_metadata`.
func isActiveAsyncQueryPollEnvelope(toolName string, env map[string]any) bool {
	name := strings.TrimSpace(strings.ToLower(toolName))
	if !isAsyncQueryResultsTool(name) && !isAsyncQueryStatusTool(name) {
		return false
	}
	result, _ := env["result"].(map[string]any)
	if result == nil {
		return false
	}
	if parseBoolValue(result["retryable"]) {
		return true
	}
	state := firstStringMapValue(result, "state", "status", "phase", "job_state", "job_status")
	if strings.TrimSpace(state) == "" {
		if qm, ok := env["query_metadata"].(map[string]any); ok {
			state = firstStringMapValue(qm, "state", "status", "phase", "job_state", "job_status")
		}
	}
	switch strings.TrimSpace(strings.ToLower(state)) {
	case "reserved", "profiling", "submission_unknown", "submitted", "running", "cancel_requested", "cancelling",
		"pending", "queued", "queueing", "in_progress", "processing", "polling", "started", "unknown":
		return true
	default:
		return false
	}
}

func (g *monotonicToolLoopGuard) MarkLatestDispatchCompacted(history []types.ChatMessage) {
	if g == nil || len(history) == 0 {
		return
	}
	for i := len(history) - 1; i >= 0; i-- {
		msg := history[i]
		if msg.Role != types.RoleTool {
			continue
		}
		callID := strings.TrimSpace(msg.ToolCallID)
		if callID == "" {
			return
		}
		sig := strings.TrimSpace(g.callIDToSignature[callID])
		if sig == "" {
			return
		}
		entry := g.entries[sig]
		if entry == nil {
			entry = &loopEntry{toolName: strings.TrimSpace(msg.Name)}
			g.entries[sig] = entry
		}
		entry.blockSameCallThisRun = true
		if strings.TrimSpace(entry.lastCompactionReason) == "" {
			entry.lastCompactionReason = "dispatch"
		}
		return
	}
}

func toolCallSignature(call types.ToolCall) string {
	name := strings.TrimSpace(strings.ToLower(call.Name))
	argsJSON := ""
	if len(call.Args) > 0 {
		if b, err := json.Marshal(call.Args); err == nil {
			argsJSON = string(b)
		}
	}
	if argsJSON == "" && strings.TrimSpace(call.ArgsText) != "" {
		argsJSON = strings.TrimSpace(call.ArgsText)
	}
	return name + "::" + argsJSON
}

func parseToolEnvelope(content *string) map[string]any {
	if content == nil {
		return nil
	}
	raw := strings.TrimSpace(*content)
	if raw == "" {
		return nil
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		return nil
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func progressMarkerFromEnvelope(env map[string]any) string {
	if len(env) == 0 {
		return ""
	}
	parts := make([]string, 0, 8)
	toolName := strings.TrimSpace(asStringValue(env["tool_name"]))
	if toolName != "" {
		parts = append(parts, "tool="+toolName)
	}
	if result, ok := env["result"].(map[string]any); ok {
		for _, key := range []string{"next_page_token", "offset", "has_more", "rows_returned", "total_rows", "total_matches", "bytes_returned"} {
			if v, exists := result[key]; exists {
				parts = append(parts, key+"="+strings.TrimSpace(asStringValue(v)))
			}
		}
	}
	if qm, ok := env["query_metadata"].(map[string]any); ok {
		for _, key := range []string{"next_page_token", "offset", "has_more", "rows_returned", "job_id"} {
			if v, exists := qm[key]; exists {
				parts = append(parts, "qm."+key+"="+strings.TrimSpace(asStringValue(v)))
			}
		}
	}
	return strings.Join(parts, "|")
}

func loopGuardBlockedOutput(call types.ToolCall, entry *loopEntry, reason string) tools.Output {
	toolName := strings.TrimSpace(call.Name)
	hints := []string{
		"Do not retry the same tool call with identical arguments in this run.",
		"Request a bounded chunk that fits remaining context (for example lower limit/max_bytes).",
		"If paging is supported, use next_page_token from the previous response.",
		"Summarize incrementally and only keep compact findings in working context.",
	}
	recommendation := map[string]any{
		"tool_name": toolName,
	}
	if rec := suggestedArgsForTool(call); len(rec) > 0 {
		recommendation["suggested_args"] = rec
	}
	attempts := 0
	stalled := 0
	compaction := ""
	if entry != nil {
		attempts = entry.attempts
		stalled = entry.stalledCount
		compaction = strings.TrimSpace(entry.lastCompactionReason)
	}
	return tools.Output{
		OK:           false,
		ResultFormat: tools.ResultFormatJSON,
		Result: map[string]any{
			"error":                "loop guard blocked repeated tool call that is unlikely to make progress",
			"loop_guard_triggered": true,
			"strategy":             "monotonic_progress",
			"reason":               strings.TrimSpace(reason),
			"tool_name":            toolName,
			"attempts":             attempts,
			"stalled_count":        stalled,
			"compaction_reason":    compaction,
			"instructions":         "Adjust the request to a smaller bounded scope or advance pagination. Repeating the identical call is disabled for this run.",
			"hints":                hints,
			"recommendation":       recommendation,
		},
		QueryMetadata: map[string]any{
			"loop_guard_triggered": true,
			"loop_guard_strategy":  "monotonic_progress",
			"loop_guard_reason":    strings.TrimSpace(reason),
		},
	}
}

func suggestedArgsForTool(call types.ToolCall) map[string]any {
	toolName := strings.TrimSpace(strings.ToLower(call.Name))
	if len(call.Args) == 0 {
		return nil
	}
	args := shallowCopyStringAnyMap(call.Args)
	switch toolName {
	case "read_file":
		if _, ok := args["max_bytes"]; !ok {
			args["max_bytes"] = 32768
		} else {
			v := toInt(args["max_bytes"])
			if v <= 0 || v > 32768 {
				args["max_bytes"] = 32768
			}
		}
	case "sample_rows":
		if _, ok := args["count"]; !ok {
			args["count"] = 100
		} else {
			v := toInt(args["count"])
			if v <= 0 || v > 100 {
				args["count"] = 100
			}
		}
	case "filter_csv":
		if _, ok := args["limit"]; !ok {
			args["limit"] = 100
		} else {
			v := toInt(args["limit"])
			if v <= 0 || v > 100 {
				args["limit"] = 100
			}
		}
	case "run_bigquery_query", "run_databricks_query", "run_trino_query", "run_snowflake_query":
		if _, ok := args["limit"]; !ok {
			args["limit"] = 200
		} else {
			v := toInt(args["limit"])
			if v <= 0 || v > 200 {
				args["limit"] = 200
			}
		}
	default:
		return nil
	}
	return args
}
