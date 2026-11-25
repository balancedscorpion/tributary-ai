package optionc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"ai-analyst-local/internal/chat/types"
)

func TestBudgetAwareToolResultCompactionStrategyPersistKeepsFullWhenBudgetAllows(t *testing.T) {
	strategy := NewBudgetAwareToolResultCompactionStrategy(&BudgetAwareToolResultCompactionConfig{
		PersistenceReservedTokens: 200,
		DispatchReservedTokens:    50,
		MinResultTokens:           32,
	})
	result := map[string]any{
		"rows": []any{
			map[string]any{"id": 1, "name": "alice"},
			map[string]any{"id": 2, "name": "bob"},
		},
		"has_more": false,
	}
	input := ToolResultPersistenceInput{
		ToolName:          "filter_csv",
		Result:            result,
		IncludeFullResult: true,
		HardTokenBudget:   4000,
		CharsPerToken:     4,
	}

	out := strategy.CompactForPersistence(input)
	if out.Compacted {
		t.Fatalf("expected full result to be kept, but got compaction metadata: %#v", out.Metadata)
	}
	gotJSON, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatalf("marshal compacted result: %v", err)
	}
	wantJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal original result: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("result changed unexpectedly:\n got=%s\nwant=%s", string(gotJSON), string(wantJSON))
	}
}

func TestBudgetAwareToolResultCompactionStrategyPersistCompactsWhenBudgetExceeded(t *testing.T) {
	strategy := NewBudgetAwareToolResultCompactionStrategy(&BudgetAwareToolResultCompactionConfig{
		PersistenceReservedTokens: 120,
		DispatchReservedTokens:    50,
		MinResultTokens:           24,
	})
	result := map[string]any{
		"rows":            makeWideRows(600, 4, 40),
		"has_more":        true,
		"next_page_token": "600",
	}
	input := ToolResultPersistenceInput{
		ToolName:          "filter_csv",
		Result:            result,
		IncludeFullResult: true,
		HardTokenBudget:   420,
		CharsPerToken:     4,
	}

	maxTokens, cpt := strategy.persistenceResultTokenBudget(input)
	out := strategy.CompactForPersistence(input)
	if !out.Compacted {
		t.Fatalf("expected compaction for oversized result")
	}
	if out.Metadata == nil {
		t.Fatalf("expected compaction metadata")
	}
	if estimateAnyTokenCount(out.Result, cpt) > maxTokens {
		t.Fatalf("compacted result still exceeds budget: got=%d max=%d", estimateAnyTokenCount(out.Result, cpt), maxTokens)
	}
}

func TestBudgetAwareToolResultCompactionStrategyDispatchCompactsLatestToolToRemainingBudget(t *testing.T) {
	strategy := NewBudgetAwareToolResultCompactionStrategy(&BudgetAwareToolResultCompactionConfig{
		PersistenceReservedTokens: 200,
		DispatchReservedTokens:    40,
		MinResultTokens:           24,
	})
	toolEnvelope := map[string]any{
		"artifact_id":   "artifact_1",
		"tool_name":     "filter_csv",
		"tool_call_id":  "tc_1",
		"ok":            true,
		"result_format": "json",
		"result": map[string]any{
			"rows":            makeWideRows(1000, 5, 48),
			"has_more":        true,
			"next_page_token": "1000",
			"offset":          0,
			"page_size":       1000,
		},
		"query_metadata": map[string]any{
			"provider":            "bigquery",
			"job_id":              "job_123",
			"query_obligation_id": "qob-0123456789abcdef0123456789abcdef",
		},
	}
	contentBytes, err := json.Marshal(toolEnvelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	content := string(contentBytes)

	history := []types.ChatMessage{
		{Role: types.RoleUser, Content: strPtr("Please analyze this CSV.")},
		{
			Role: types.RoleAssistant,
			ToolCalls: []types.ToolCall{
				{ID: "tc_1", Name: "filter_csv"},
			},
		},
		{
			Role:       types.RoleTool,
			Name:       "filter_csv",
			ToolCallID: "tc_1",
			Content:    &content,
		},
	}
	systemPrompt := "You are a data analyst."
	usedBefore := estimateRequestTokensWithTools(systemPrompt, history[:2], nil, 4)
	tokenBudget := usedBefore + 240

	result := strategy.CompactHistory(ToolResultHistoryInput{
		History:       history,
		TokenBudget:   tokenBudget,
		SystemPrompt:  systemPrompt,
		ToolDefs:      nil,
		CharsPerToken: 4,
	})
	if !result.Compacted {
		t.Fatalf("expected dispatch compaction")
	}
	after := estimateRequestTokensWithTools(systemPrompt, result.History, nil, 4)
	if after > tokenBudget {
		t.Fatalf("compacted history still exceeds budget: got=%d max=%d", after, tokenBudget)
	}

	toolMsg := result.History[len(result.History)-1]
	if toolMsg.Content == nil {
		t.Fatalf("tool content missing after compaction")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*toolMsg.Content), &env); err != nil {
		t.Fatalf("tool content is not valid JSON after compaction: %v", err)
	}
	qm, ok := env["query_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("query_metadata missing after compaction: %#v", env)
	}
	if strings.TrimSpace(asStringValue(qm["job_id"])) != "job_123" {
		t.Fatalf("query_metadata.job_id was not preserved: %#v", qm)
	}
	if got := strings.TrimSpace(asStringValue(qm["query_obligation_id"])); got != "qob-0123456789abcdef0123456789abcdef" {
		t.Fatalf("query_metadata.query_obligation_id=%q, want stable obligation identity", got)
	}
}

func TestCompactQueryMetadataRejectsInvalidQueryObligationIDs(t *testing.T) {
	for _, obligationID := range []string{
		"qob-00000000000000000000000000000000",
		"qob_0123456789abcdef0123456789abcdef",
		"qob-0123456789ABCDEF0123456789ABCDEF",
		"qob-short",
	} {
		compacted := compactQueryMetadata(map[string]any{
			"query_obligation_id": obligationID,
		})
		if got, exists := compacted["query_obligation_id"]; exists {
			t.Fatalf("invalid query obligation %q survived compaction as %#v", obligationID, got)
		}
	}
}

func TestSanitizeModelToolResultEnvelopeCompactsHugeTextWithBoundedHeadTail(t *testing.T) {
	hugeText := "TEXT_SENTINEL_BEGIN" + strings.Repeat("x", 200_000) + "TEXT_SENTINEL_END"
	env := map[string]any{
		"artifact_id":   "artifact-text",
		"tool_name":     "read_file",
		"tool_call_id":  "tc-text",
		"ok":            true,
		"result_format": "text",
		"result":        hugeText,
	}

	out := SanitizeModelToolResultEnvelope(env, &ModelToolResultSanitizerConfig{
		MaxEnvelopeBytes:  4096,
		MaxRawResultBytes: 1024,
		MaxPreviewBytes:   2048,
	})
	if !out.Compacted {
		t.Fatalf("expected huge text result to be compacted")
	}
	encoded, err := json.Marshal(out.Envelope)
	if err != nil {
		t.Fatalf("marshal sanitized envelope: %v", err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("sanitized envelope exceeded cap: got=%d max=4096", len(encoded))
	}
	if strings.Contains(string(encoded), hugeText) {
		t.Fatalf("sanitized envelope included the raw huge text sentinel")
	}

	result, preview := requireCompactedModelPreview(t, out.Envelope)
	if result["compacted"] != true {
		t.Fatalf("result.compacted=%#v, want true", result["compacted"])
	}
	assertPreviewPartBounded(t, preview, "head", 1024)
	assertPreviewPartBounded(t, preview, "tail", 1024)

	meta, ok := out.Envelope["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing: %#v", out.Envelope["result_metadata"])
	}
	if meta["model_context_compacted"] != true {
		t.Fatalf("model_context_compacted=%#v, want true", meta["model_context_compacted"])
	}
	sum := sha256.Sum256([]byte(hugeText))
	if got, want := strings.TrimSpace(asStringValue(meta["sha256"])), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("sha256=%q, want %q", got, want)
	}
	if got := intFromAnyToolResultTest(meta["result_size_bytes"]); got != len([]byte(hugeText)) {
		t.Fatalf("result_size_bytes=%d, want %d", got, len([]byte(hugeText)))
	}
	if got := intFromAnyToolResultTest(meta["result_preview_bytes"]); got > 2048 {
		t.Fatalf("result_preview_bytes=%d, want <= 2048", got)
	}
	if retrieval, ok := out.Envelope["retrieval"].(map[string]any); !ok || len(retrieval) == 0 {
		t.Fatalf("retrieval hints missing: %#v", out.Envelope["retrieval"])
	}
}

func TestSanitizeModelToolResultEnvelopeCompactsHugeJSONWithoutRawResult(t *testing.T) {
	hugeJSONSentinel := "JSON_SENTINEL_BEGIN" + strings.Repeat("0123456789", 20_000) + "JSON_SENTINEL_END"
	result := map[string]any{
		"payload": hugeJSONSentinel,
		"rows": []any{
			map[string]any{"id": 1, "value": "alpha"},
			map[string]any{"id": 2, "value": "beta"},
		},
	}
	env := map[string]any{
		"artifact_id":   "artifact-json",
		"tool_name":     "run_bigquery_query",
		"tool_call_id":  "tc-json",
		"ok":            true,
		"result_format": "json",
		"result":        result,
		"query_metadata": map[string]any{
			"provider":            "bigquery",
			"job_id":              "qj-1234567890abcdef1234567890abcdef",
			"query_obligation_id": "qob-0123456789abcdef0123456789abcdef",
			"debug_payload":       strings.Repeat("metadata", 20_000),
		},
	}

	out := SanitizeModelToolResultEnvelope(env, &ModelToolResultSanitizerConfig{
		MaxEnvelopeBytes:  4096,
		MaxRawResultBytes: 1024,
		MaxPreviewBytes:   1536,
	})
	if !out.Compacted {
		t.Fatalf("expected huge JSON result to be compacted")
	}
	encoded, err := json.Marshal(out.Envelope)
	if err != nil {
		t.Fatalf("marshal sanitized envelope: %v", err)
	}
	if len(encoded) > 4096 {
		t.Fatalf("sanitized JSON envelope exceeded cap: got=%d max=4096", len(encoded))
	}
	if strings.Contains(string(encoded), hugeJSONSentinel) {
		t.Fatalf("sanitized envelope included the raw huge JSON sentinel")
	}

	resultMap, preview := requireCompactedModelPreview(t, out.Envelope)
	if _, exists := resultMap["payload"]; exists {
		t.Fatalf("compacted result still exposes raw payload field: %#v", resultMap)
	}
	assertPreviewPartBounded(t, preview, "head", 768)
	assertPreviewPartBounded(t, preview, "tail", 768)

	meta, ok := out.Envelope["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing: %#v", out.Envelope["result_metadata"])
	}
	if got := intFromAnyToolResultTest(meta["result_key_count"]); got != 2 {
		t.Fatalf("result_key_count=%d, want 2", got)
	}
	counts, ok := meta["result_counts"].(map[string]any)
	if !ok {
		t.Fatalf("result_counts missing: %#v", meta["result_counts"])
	}
	if got := intFromAnyToolResultTest(counts["rows"]); got != 2 {
		t.Fatalf("result_counts.rows=%d, want 2", got)
	}
	if strings.TrimSpace(asStringValue(meta["sha256"])) == "" {
		t.Fatalf("sha256 metadata missing: %#v", meta)
	}
	qm, ok := out.Envelope["query_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("query_metadata missing after sanitizer compaction: %#v", out.Envelope)
	}
	if got := strings.TrimSpace(asStringValue(qm["query_obligation_id"])); got != "qob-0123456789abcdef0123456789abcdef" {
		t.Fatalf("query_metadata.query_obligation_id=%q after sanitizer compaction", got)
	}
	retrieval, ok := out.Envelope["retrieval"].(map[string]any)
	if !ok {
		t.Fatalf("retrieval hints missing: %#v", out.Envelope["retrieval"])
	}
	if strings.TrimSpace(asStringValue(retrieval["artifact_id"])) != "artifact-json" {
		t.Fatalf("retrieval.artifact_id=%#v, want artifact-json", retrieval["artifact_id"])
	}
}

func TestCompactToolMessageToTokenBudgetFallsBackWhenEnvelopeCannotFit(t *testing.T) {
	toolEnvelope := map[string]any{
		"artifact_id":   "artifact_oversized",
		"tool_name":     "read_file",
		"tool_call_id":  "tc_oversized",
		"ok":            true,
		"result_format": "json",
		"result": map[string]any{
			"content":  strings.Repeat("x", 200_000),
			"has_more": true,
		},
		"query_metadata": map[string]any{
			"provider":            "databricks",
			"job_id":              "qj-fedcba0987654321fedcba0987654321",
			"query_obligation_id": "qob-fedcba9876543210fedcba9876543210",
			"debug_payload":       strings.Repeat("y", 200_000),
		},
	}
	contentBytes, err := json.Marshal(toolEnvelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	content := string(contentBytes)
	msg := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       "read_file",
		ToolCallID: "tc_oversized",
		Content:    &content,
	}

	compacted, changed := compactToolMessageWithTimeout(t, msg, 12, 4, "dispatch")
	if !changed {
		t.Fatalf("expected oversized envelope to be compacted")
	}
	if compacted.Content == nil {
		t.Fatalf("expected compacted content")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*compacted.Content), &env); err != nil {
		t.Fatalf("compacted content is not valid JSON: %v\n%s", err, *compacted.Content)
	}
	if strings.TrimSpace(asStringValue(env["tool_call_id"])) != "tc_oversized" {
		t.Fatalf("tool_call_id not preserved: %#v", env)
	}
	metadata, ok := env["result_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("result_metadata missing after fallback: %#v", env)
	}
	if strings.TrimSpace(asStringValue(metadata["compaction_fallback"])) != "minimal_envelope" {
		t.Fatalf("expected minimal envelope fallback metadata, got %#v", metadata)
	}
	qm, ok := env["query_metadata"].(map[string]any)
	if !ok {
		t.Fatalf("query_metadata missing from minimal envelope: %#v", env)
	}
	if got := strings.TrimSpace(asStringValue(qm["query_obligation_id"])); got != "qob-fedcba9876543210fedcba9876543210" {
		t.Fatalf("minimal envelope query_obligation_id=%q, want stable obligation identity", got)
	}
}

func TestCompactToolMessageToTokenBudgetLowBudgetHugeTextEnvelopeConverges(t *testing.T) {
	toolEnvelope := map[string]any{
		"artifact_id":   "artifact_low_text",
		"tool_name":     "read_file",
		"tool_call_id":  "tc_low_text",
		"ok":            true,
		"result_format": "text",
		"result":        strings.Repeat("large text payload\n", 20_000),
	}
	contentBytes, err := json.Marshal(toolEnvelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	content := string(contentBytes)
	msg := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       "read_file",
		ToolCallID: "tc_low_text",
		Content:    &content,
	}

	compacted, changed := compactToolMessageWithTimeout(t, msg, 20, 4, "dispatch")
	if !changed {
		t.Fatalf("expected low-budget text envelope to compact")
	}
	if compacted.Content == nil {
		t.Fatalf("expected compacted content")
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*compacted.Content), &env); err != nil {
		t.Fatalf("compacted text envelope is not valid JSON: %v\n%s", err, *compacted.Content)
	}
	if strings.TrimSpace(asStringValue(env["artifact_id"])) != "artifact_low_text" {
		t.Fatalf("artifact_id not preserved: %#v", env)
	}
	metadata, ok := env["result_metadata"].(map[string]any)
	if !ok || metadata["compacted"] != true {
		t.Fatalf("result_metadata missing compaction marker: %#v", env)
	}
	if len(*compacted.Content) >= len(content) {
		t.Fatalf("text envelope did not get smaller: before=%d after=%d", len(content), len(*compacted.Content))
	}
}

func TestCompactToolMessageToTokenBudgetConvergesWithinFeasibleBudget(t *testing.T) {
	toolEnvelope := map[string]any{
		"artifact_id":   "artifact_1",
		"tool_name":     "filter_csv",
		"tool_call_id":  "tc_1",
		"ok":            true,
		"result_format": "json",
		"result": map[string]any{
			"rows":            makeWideRows(2500, 8, 96),
			"has_more":        true,
			"next_page_token": "2500",
		},
	}
	contentBytes, err := json.Marshal(toolEnvelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	content := string(contentBytes)
	msg := types.ChatMessage{
		Role:       types.RoleTool,
		Name:       "filter_csv",
		ToolCallID: "tc_1",
		Content:    &content,
	}

	const maxTokens = 180
	compacted, changed := compactToolMessageToTokenBudget(msg, maxTokens, 4, "dispatch")
	if !changed {
		t.Fatalf("expected oversized envelope to be compacted")
	}
	if compacted.Content == nil {
		t.Fatalf("expected compacted content")
	}
	if got := estimateStringTokenCount(*compacted.Content, 4); got > maxTokens {
		t.Fatalf("compacted content exceeds feasible budget: got=%d max=%d content=%s", got, maxTokens, *compacted.Content)
	}
	var env map[string]any
	if err := json.Unmarshal([]byte(*compacted.Content), &env); err != nil {
		t.Fatalf("compacted content is not valid JSON: %v", err)
	}
	result, ok := env["result"].(map[string]any)
	if !ok {
		t.Fatalf("result missing after compaction: %#v", env)
	}
	if nav, ok := result["navigation"].(map[string]any); ok {
		if strings.TrimSpace(asStringValue(nav["next_page_token"])) != "2500" {
			t.Fatalf("navigation next_page_token not preserved: %#v", nav)
		}
	}
}

func TestCompactValueToTokenBudgetReturnsMinimalWhenPreviewCannotFit(t *testing.T) {
	value := map[string]any{
		"payload": strings.Repeat("z", 100_000),
	}
	compacted := compactValueToTokenBudget(value, 1, 4)
	if got := estimateAnyTokenCount(compacted, 4); got > 1 {
		t.Fatalf("minimal fallback exceeded tiny budget: got=%d value=%#v", got, compacted)
	}
}

func TestCompactValueToTokenBudgetLowBudgetNoProgressConverges(t *testing.T) {
	done := make(chan any, 1)
	go func() {
		done <- compactValueToTokenBudget(strings.Repeat("x", 1<<20), 1, 4)
	}()

	select {
	case got := <-done:
		if tokens := estimateAnyTokenCount(got, 4); tokens > 1 {
			t.Fatalf("fallback exceeded tiny budget: got=%d value=%#v", tokens, got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("compactValueToTokenBudget did not converge for no-progress low-budget input")
	}
}

func compactToolMessageWithTimeout(t *testing.T, msg types.ChatMessage, maxTokens int, charsPerToken int, stage string) (types.ChatMessage, bool) {
	t.Helper()
	type result struct {
		msg     types.ChatMessage
		changed bool
	}
	done := make(chan result, 1)
	go func() {
		compacted, changed := compactToolMessageToTokenBudget(msg, maxTokens, charsPerToken, stage)
		done <- result{msg: compacted, changed: changed}
	}()
	select {
	case got := <-done:
		return got.msg, got.changed
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("compactToolMessageToTokenBudget did not converge")
		return types.ChatMessage{}, false
	}
}

func requireCompactedModelPreview(t *testing.T, envelope map[string]any) (map[string]any, map[string]any) {
	t.Helper()
	result, ok := envelope["result"].(map[string]any)
	if !ok {
		t.Fatalf("result type=%T, want map[string]any", envelope["result"])
	}
	preview, ok := result["preview"].(map[string]any)
	if !ok {
		t.Fatalf("result.preview type=%T, want map[string]any", result["preview"])
	}
	return result, preview
}

func assertPreviewPartBounded(t *testing.T, preview map[string]any, key string, maxBytes int) {
	t.Helper()
	value, ok := preview[key].(string)
	if !ok {
		t.Fatalf("preview.%s type=%T, want string", key, preview[key])
	}
	if got := len([]byte(value)); got > maxBytes {
		t.Fatalf("preview.%s bytes=%d, want <= %d", key, got, maxBytes)
	}
}

func intFromAnyToolResultTest(v any) int {
	switch typed := v.(type) {
	case int:
		return typed
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	case json.Number:
		i, _ := typed.Int64()
		return int(i)
	default:
		n, _ := strconv.Atoi(strings.TrimSpace(asStringValue(v)))
		return n
	}
}

func makeWideRows(rowCount int, cols int, width int) []any {
	rows := make([]any, 0, rowCount)
	for i := 0; i < rowCount; i++ {
		row := make(map[string]any, cols+1)
		row["row"] = i
		for c := 0; c < cols; c++ {
			key := "c" + strconv.Itoa(c)
			row[key] = strings.Repeat("x", width)
		}
		rows = append(rows, row)
	}
	return rows
}

func strPtr(v string) *string { return &v }
