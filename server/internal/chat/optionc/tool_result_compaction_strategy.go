package optionc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ai-analyst-local/internal/chat/tools"
	"ai-analyst-local/internal/chat/types"
)

const (
	defaultPersistenceReservedTokens = 24000
	defaultDispatchReservedTokens    = 4000
	defaultMinResultTokens           = 96
	maxBudgetCompactionIterations    = 8
	defaultModelResultRawBytes       = toolResultEnvelopeByteLimit
	defaultModelResultPreviewBytes   = toolResultPreviewBytes
)

type ToolResultCompactionStrategy interface {
	CompactForPersistence(input ToolResultPersistenceInput) ToolResultPersistenceOutput
	CompactHistory(input ToolResultHistoryInput) ToolResultHistoryOutput
}

type NoopToolResultCompactionStrategy struct{}

func (NoopToolResultCompactionStrategy) CompactForPersistence(input ToolResultPersistenceInput) ToolResultPersistenceOutput {
	return ToolResultPersistenceOutput{Result: input.Result}
}

func (NoopToolResultCompactionStrategy) CompactHistory(input ToolResultHistoryInput) ToolResultHistoryOutput {
	return ToolResultHistoryOutput{History: input.History}
}

type ToolResultPersistenceInput struct {
	ToolName          string
	Result            any
	QueryMetadata     map[string]any
	IncludeFullResult bool
	HardTokenBudget   int
	CharsPerToken     int
}

type ToolResultPersistenceOutput struct {
	Result    any
	Compacted bool
	Metadata  map[string]any
}

type ToolResultHistoryInput struct {
	History       []types.ChatMessage
	TokenBudget   int
	SystemPrompt  string
	ToolDefs      []types.ToolDefinition
	CharsPerToken int
}

type ToolResultHistoryOutput struct {
	History   []types.ChatMessage
	Compacted bool
}

type BudgetAwareToolResultCompactionConfig struct {
	PersistenceReservedTokens int
	DispatchReservedTokens    int
	MinResultTokens           int
}

type BudgetAwareToolResultCompactionStrategy struct {
	config BudgetAwareToolResultCompactionConfig
}

type ModelToolResultSanitizerConfig struct {
	MaxEnvelopeBytes  int
	MaxRawResultBytes int
	MaxPreviewBytes   int
}

type ModelToolResultSanitizerOutput struct {
	Envelope      map[string]any
	Compacted     bool
	ResultBytes   int
	EnvelopeBytes int
	SHA256        string
}

func NewBudgetAwareToolResultCompactionStrategy(cfg *BudgetAwareToolResultCompactionConfig) *BudgetAwareToolResultCompactionStrategy {
	config := BudgetAwareToolResultCompactionConfig{
		PersistenceReservedTokens: defaultPersistenceReservedTokens,
		DispatchReservedTokens:    defaultDispatchReservedTokens,
		MinResultTokens:           defaultMinResultTokens,
	}
	if cfg != nil {
		if cfg.PersistenceReservedTokens > 0 {
			config.PersistenceReservedTokens = cfg.PersistenceReservedTokens
		}
		if cfg.DispatchReservedTokens > 0 {
			config.DispatchReservedTokens = cfg.DispatchReservedTokens
		}
		if cfg.MinResultTokens > 0 {
			config.MinResultTokens = cfg.MinResultTokens
		}
	}
	if config.MinResultTokens <= 0 {
		config.MinResultTokens = defaultMinResultTokens
	}
	return &BudgetAwareToolResultCompactionStrategy{config: config}
}

func SanitizeModelToolResultEnvelope(input map[string]any, cfg *ModelToolResultSanitizerConfig) ModelToolResultSanitizerOutput {
	config := normalizeModelToolResultSanitizerConfig(cfg)
	env := shallowCopyStringAnyMap(input)
	if env == nil {
		env = map[string]any{}
	}

	result := env["result"]
	resultFormat := strings.ToLower(modelToolResultString(env["result_format"]))
	resultBytes := modelToolResultBytes(result, resultFormat)
	resultHash := sha256.Sum256(resultBytes)
	resultHashHex := hex.EncodeToString(resultHash[:])
	contentKind := modelToolResultContentKind(env, result, resultFormat)

	resultMetadata := ensureMapValue(env["result_metadata"])
	resultMetadata["result_size_bytes"] = len(resultBytes)
	resultMetadata["sha256"] = resultHashHex
	resultMetadata["result_sha256"] = resultHashHex
	resultMetadata["content_kind"] = contentKind
	addModelToolResultCounts(resultMetadata, result)

	if retrieval := modelToolResultRetrievalHints(env, result, resultMetadata, contentKind); len(retrieval) > 0 {
		env["retrieval"] = retrieval
		resultMetadata["retrieval"] = retrieval
	}
	env["result_metadata"] = resultMetadata

	envelopeBytes := modelToolResultEnvelopeSize(env)
	if len(resultBytes) <= config.MaxRawResultBytes && envelopeBytes <= config.MaxEnvelopeBytes {
		return ModelToolResultSanitizerOutput{
			Envelope:      env,
			Compacted:     false,
			ResultBytes:   len(resultBytes),
			EnvelopeBytes: envelopeBytes,
			SHA256:        resultHashHex,
		}
	}

	resultMetadata["model_context_compacted"] = true
	resultMetadata["compacted"] = true
	resultMetadata["compaction_stage"] = "model_context_sanitizer"
	resultMetadata["compaction_strategy"] = "head_tail_preview"
	resultMetadata["result_size_bytes_before_compaction"] = len(resultBytes)
	resultMetadata["max_envelope_bytes"] = config.MaxEnvelopeBytes
	resultMetadata["max_raw_result_bytes"] = config.MaxRawResultBytes
	resultMetadata["max_preview_bytes"] = config.MaxPreviewBytes

	previewBudget := min(config.MaxPreviewBytes, max(0, config.MaxEnvelopeBytes/2))
	applyPreview := func(maxPreviewBytes int) {
		previewResult, previewMetadata := compactedModelToolResultValue(resultBytes, contentKind, maxPreviewBytes)
		env["result"] = previewResult
		for key, value := range previewMetadata {
			resultMetadata[key] = value
		}
		env["result_metadata"] = resultMetadata
	}

	for {
		applyPreview(previewBudget)
		envelopeBytes = modelToolResultEnvelopeSize(env)
		if envelopeBytes <= config.MaxEnvelopeBytes {
			return ModelToolResultSanitizerOutput{
				Envelope:      env,
				Compacted:     true,
				ResultBytes:   len(resultBytes),
				EnvelopeBytes: envelopeBytes,
				SHA256:        resultHashHex,
			}
		}
		if previewBudget == 0 {
			break
		}
		nextBudget := previewBudget / 2
		if nextBudget == previewBudget {
			nextBudget = 0
		}
		previewBudget = nextBudget
	}

	if qm, ok := env["query_metadata"].(map[string]any); ok && len(qm) > 0 {
		compactedQM := compactQueryMetadata(qm)
		env["query_metadata"] = compactedQM
		resultMetadata["query_metadata_compacted"] = true
		env["result_metadata"] = resultMetadata
		envelopeBytes = modelToolResultEnvelopeSize(env)
		if envelopeBytes <= config.MaxEnvelopeBytes {
			return ModelToolResultSanitizerOutput{
				Envelope:      env,
				Compacted:     true,
				ResultBytes:   len(resultBytes),
				EnvelopeBytes: envelopeBytes,
				SHA256:        resultHashHex,
			}
		}
		identityQM := compactQueryIdentityMetadata(qm)
		if len(identityQM) > 0 {
			env["query_metadata"] = identityQM
			resultMetadata["query_metadata_identity_only"] = true
		} else {
			delete(env, "query_metadata")
			resultMetadata["query_metadata_omitted"] = true
		}
	}

	for _, key := range []string{"plot_spec", "chart_budget", "chart_modules", "chart_module"} {
		if _, ok := env[key]; !ok {
			continue
		}
		delete(env, key)
		resultMetadata["omitted_"+key] = true
		env["result_metadata"] = resultMetadata
		envelopeBytes = modelToolResultEnvelopeSize(env)
		if envelopeBytes <= config.MaxEnvelopeBytes {
			return ModelToolResultSanitizerOutput{
				Envelope:      env,
				Compacted:     true,
				ResultBytes:   len(resultBytes),
				EnvelopeBytes: envelopeBytes,
				SHA256:        resultHashHex,
			}
		}
	}

	applyPreview(0)
	envelopeBytes = modelToolResultEnvelopeSize(env)
	return ModelToolResultSanitizerOutput{
		Envelope:      env,
		Compacted:     true,
		ResultBytes:   len(resultBytes),
		EnvelopeBytes: envelopeBytes,
		SHA256:        resultHashHex,
	}
}

func normalizeModelToolResultSanitizerConfig(cfg *ModelToolResultSanitizerConfig) ModelToolResultSanitizerConfig {
	config := ModelToolResultSanitizerConfig{
		MaxEnvelopeBytes:  defaultModelResultRawBytes,
		MaxRawResultBytes: defaultModelResultRawBytes,
		MaxPreviewBytes:   defaultModelResultPreviewBytes,
	}
	if cfg != nil {
		if cfg.MaxEnvelopeBytes > 0 {
			config.MaxEnvelopeBytes = cfg.MaxEnvelopeBytes
		}
		if cfg.MaxRawResultBytes > 0 {
			config.MaxRawResultBytes = cfg.MaxRawResultBytes
		}
		if cfg.MaxPreviewBytes > 0 {
			config.MaxPreviewBytes = cfg.MaxPreviewBytes
		}
	}
	if config.MaxEnvelopeBytes <= 0 {
		config.MaxEnvelopeBytes = defaultModelResultRawBytes
	}
	if config.MaxRawResultBytes <= 0 {
		config.MaxRawResultBytes = config.MaxEnvelopeBytes
	}
	if config.MaxRawResultBytes > config.MaxEnvelopeBytes {
		config.MaxRawResultBytes = config.MaxEnvelopeBytes
	}
	if config.MaxPreviewBytes < 0 {
		config.MaxPreviewBytes = 0
	}
	if config.MaxPreviewBytes > config.MaxEnvelopeBytes {
		config.MaxPreviewBytes = config.MaxEnvelopeBytes
	}
	return config
}

func modelToolResultEnvelopeSize(env map[string]any) int {
	if env == nil {
		return 0
	}
	b, err := json.Marshal(env)
	if err != nil {
		return len(marshalToString(env))
	}
	return len(b)
}

func modelToolResultBytes(result any, resultFormat string) []byte {
	if strings.EqualFold(strings.TrimSpace(resultFormat), string(tools.ResultFormatText)) {
		if s, ok := result.(string); ok {
			return []byte(s)
		}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return []byte(asStringValue(result))
	}
	return b
}

func modelToolResultString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(asStringValue(value))
}

func modelToolResultContentKind(env map[string]any, result any, resultFormat string) string {
	if resultMap, ok := result.(map[string]any); ok {
		if contentKind := modelToolResultString(resultMap["content_kind"]); contentKind != "" {
			return contentKind
		}
	}
	if resultMetadata, ok := env["result_metadata"].(map[string]any); ok {
		if contentKind := modelToolResultString(resultMetadata["content_kind"]); contentKind != "" {
			return contentKind
		}
	}
	switch strings.TrimSpace(strings.ToLower(resultFormat)) {
	case string(tools.ResultFormatText), "text/plain":
		return "text"
	case string(tools.ResultFormatJSON), "application/json":
		if resultMap, ok := result.(map[string]any); ok {
			if _, hasRows := resultMap["rows"]; hasRows {
				return "json_rows"
			}
		}
		return "json"
	default:
		if _, ok := result.(string); ok {
			return "text"
		}
		return "json"
	}
}

func modelToolResultRetrievalHints(env map[string]any, result any, resultMetadata map[string]any, contentKind string) map[string]any {
	if retrieval, ok := env["retrieval"].(map[string]any); ok && len(retrieval) > 0 {
		return shallowCopyStringAnyMap(retrieval)
	}
	if resultMap, ok := result.(map[string]any); ok {
		if retrieval, ok := resultMap["retrieval"].(map[string]any); ok && len(retrieval) > 0 {
			return shallowCopyStringAnyMap(retrieval)
		}
	}
	if retrieval, ok := resultMetadata["retrieval"].(map[string]any); ok && len(retrieval) > 0 {
		return shallowCopyStringAnyMap(retrieval)
	}
	artifactID := modelToolResultArtifactID(env, result, resultMetadata)
	if artifactID == "" {
		return nil
	}
	return tools.LargeArtifactRetrievalHints(artifactID, contentKind)
}

func modelToolResultArtifactID(env map[string]any, result any, resultMetadata map[string]any) string {
	for _, raw := range []any{
		env["full_artifact_id"],
		env["artifact_id"],
		resultMetadata["full_artifact_id"],
		resultMetadata["artifact_id"],
	} {
		if id := modelToolResultString(raw); id != "" {
			return id
		}
	}
	if resultMap, ok := result.(map[string]any); ok {
		for _, raw := range []any{resultMap["full_artifact_id"], resultMap["artifact_id"]} {
			if id := modelToolResultString(raw); id != "" {
				return id
			}
		}
	}
	return ""
}

func addModelToolResultCounts(metadata map[string]any, result any) {
	if metadata == nil {
		return
	}
	switch v := result.(type) {
	case string:
		metadata["result_char_count"] = len([]rune(v))
		metadata["result_line_count"] = strings.Count(v, "\n") + 1
	case []any:
		metadata["result_item_count"] = len(v)
	case []string:
		metadata["result_item_count"] = len(v)
	case map[string]any:
		metadata["result_key_count"] = len(v)
		counts := map[string]any{}
		for _, key := range []string{"rows", "results", "messages", "evidence", "items"} {
			if count := countAnySlice(v[key]); count > 0 {
				counts[key] = count
			}
		}
		if len(counts) > 0 {
			metadata["result_counts"] = counts
		}
	}
}

func compactedModelToolResultValue(resultBytes []byte, contentKind string, maxPreviewBytes int) (map[string]any, map[string]any) {
	preview, previewMetadata := headTailPreview(resultBytes, maxPreviewBytes)
	result := map[string]any{
		"compacted":    true,
		"content_kind": strings.TrimSpace(contentKind),
		"preview":      preview,
		"note":         "Tool result compacted for model context. Use the retrieval hints to fetch focused content.",
	}
	if result["content_kind"] == "" {
		result["content_kind"] = "content"
	}
	return result, previewMetadata
}

func headTailPreview(raw []byte, maxPreviewBytes int) (map[string]any, map[string]any) {
	if maxPreviewBytes < 0 {
		maxPreviewBytes = 0
	}
	if len(raw) == 0 || maxPreviewBytes == 0 {
		preview := map[string]any{
			"head":          "",
			"tail":          "",
			"head_bytes":    0,
			"tail_bytes":    0,
			"omitted_bytes": len(raw),
			"source_bytes":  len(raw),
		}
		return preview, map[string]any{
			"result_preview_head_bytes": 0,
			"result_preview_tail_bytes": 0,
			"result_preview_bytes":      0,
			"result_omitted_bytes":      len(raw),
		}
	}

	if len(raw) <= maxPreviewBytes {
		head := sanitizePreviewBytes(raw)
		headBytes := len([]byte(head))
		preview := map[string]any{
			"head":          head,
			"tail":          "",
			"head_bytes":    headBytes,
			"tail_bytes":    0,
			"omitted_bytes": 0,
			"source_bytes":  len(raw),
		}
		return preview, map[string]any{
			"result_preview_head_bytes": headBytes,
			"result_preview_tail_bytes": 0,
			"result_preview_bytes":      headBytes,
			"result_omitted_bytes":      0,
		}
	}

	headBudget := maxPreviewBytes / 2
	tailBudget := maxPreviewBytes - headBudget
	head := sanitizePreviewBytes(raw[:headBudget])
	tail := sanitizePreviewBytes(raw[len(raw)-tailBudget:])
	headBytes := len([]byte(head))
	tailBytes := len([]byte(tail))
	omittedBytes := len(raw) - headBudget - tailBudget
	if omittedBytes < 0 {
		omittedBytes = 0
	}
	preview := map[string]any{
		"head":          head,
		"tail":          tail,
		"head_bytes":    headBytes,
		"tail_bytes":    tailBytes,
		"omitted_bytes": omittedBytes,
		"source_bytes":  len(raw),
	}
	return preview, map[string]any{
		"result_preview_head_bytes": headBytes,
		"result_preview_tail_bytes": tailBytes,
		"result_preview_bytes":      headBytes + tailBytes,
		"result_omitted_bytes":      omittedBytes,
	}
}

func sanitizePreviewBytes(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	return strings.ToValidUTF8(string(raw), "?")
}

func (s *BudgetAwareToolResultCompactionStrategy) CompactForPersistence(input ToolResultPersistenceInput) ToolResultPersistenceOutput {
	out := ToolResultPersistenceOutput{
		Result: input.Result,
	}
	maxTokens, cpt := s.persistenceResultTokenBudget(input)
	originalTokens := estimateAnyTokenCount(input.Result, cpt)
	if maxTokens <= 0 || originalTokens <= maxTokens {
		return out
	}
	compacted := compactValueToTokenBudget(input.Result, maxTokens, cpt)
	compactedTokens := estimateAnyTokenCount(compacted, cpt)
	metadata := map[string]any{
		"compacted":            true,
		"compaction_stage":     "persistence",
		"compaction_strategy":  "budget_aware",
		"hard_token_budget":    input.HardTokenBudget,
		"result_token_budget":  maxTokens,
		"chars_per_token":      cpt,
		"result_tokens_before": originalTokens,
		"result_tokens_after":  compactedTokens,
		"full_data_requested":  input.IncludeFullResult,
		"persistence_reserved": s.config.PersistenceReservedTokens,
	}
	if input.QueryMetadata != nil {
		if provider := strings.TrimSpace(asStringValue(input.QueryMetadata["provider"])); provider != "" {
			metadata["provider"] = provider
		}
	}
	out.Result = compacted
	out.Compacted = true
	out.Metadata = metadata
	return out
}

func (s *BudgetAwareToolResultCompactionStrategy) CompactHistory(input ToolResultHistoryInput) ToolResultHistoryOutput {
	if len(input.History) == 0 || input.TokenBudget <= 0 {
		return ToolResultHistoryOutput{History: input.History}
	}
	cpt := input.CharsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	out := append([]types.ChatMessage(nil), input.History...)
	latestToolIdx := -1
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == types.RoleTool {
			latestToolIdx = i
			break
		}
	}
	if latestToolIdx < 0 || out[latestToolIdx].Content == nil {
		return ToolResultHistoryOutput{History: out}
	}

	usedBefore := estimateRequestTokensWithTools(
		input.SystemPrompt,
		out[:latestToolIdx],
		input.ToolDefs,
		cpt,
	)
	remainingTokens := input.TokenBudget - usedBefore - s.config.DispatchReservedTokens
	if remainingTokens < s.config.MinResultTokens {
		remainingTokens = s.config.MinResultTokens
	}
	compactedMsg, changed := compactToolMessageToTokenBudget(out[latestToolIdx], remainingTokens, cpt, "dispatch")
	if !changed {
		return ToolResultHistoryOutput{History: out}
	}
	out[latestToolIdx] = compactedMsg
	return ToolResultHistoryOutput{
		History:   out,
		Compacted: true,
	}
}

func (s *BudgetAwareToolResultCompactionStrategy) persistenceResultTokenBudget(input ToolResultPersistenceInput) (maxTokens int, charsPerToken int) {
	charsPerToken = input.CharsPerToken
	if charsPerToken <= 0 {
		charsPerToken = defaultTokenCharsPerToken
	}
	hardBudget := input.HardTokenBudget
	if hardBudget <= 0 {
		hardBudget = defaultHardContextTokenCap
	}
	maxTokens = hardBudget - s.config.PersistenceReservedTokens
	if maxTokens < s.config.MinResultTokens {
		maxTokens = s.config.MinResultTokens
	}
	return maxTokens, charsPerToken
}

func compactToolMessageToTokenBudget(msg types.ChatMessage, maxTokens int, charsPerToken int, stage string) (types.ChatMessage, bool) {
	if msg.Content == nil || maxTokens <= 0 {
		return msg, false
	}
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	raw := strings.TrimSpace(*msg.Content)
	if raw == "" {
		return msg, false
	}
	if estimateStringTokenCount(raw, cpt) <= maxTokens {
		return msg, false
	}

	var env map[string]any
	if err := json.Unmarshal([]byte(raw), &env); err != nil || len(env) == 0 {
		preview := compactStringToTokenBudget(raw, maxTokens, cpt)
		if preview == raw {
			return msg, false
		}
		msg.Content = ptrString(preview)
		return msg, true
	}

	originalResult := env["result"]
	resultMetadata := ensureMapValue(env["result_metadata"])
	nonResultEnvelope := shallowCopyStringAnyMap(env)
	delete(nonResultEnvelope, "result")
	baseTokens := estimateAnyTokenCount(nonResultEnvelope, cpt)
	resultBudget := maxTokens - baseTokens - 4
	if resultBudget < 1 {
		resultBudget = 1
	}
	compactedResult := compactValueToTokenBudget(originalResult, resultBudget, cpt)
	env["result"] = compactedResult
	resultMetadata["compacted"] = true
	resultMetadata["compaction_stage"] = stage
	resultMetadata["compaction_strategy"] = "budget_aware"
	resultMetadata["result_token_budget"] = resultBudget
	resultMetadata["result_tokens_before"] = estimateAnyTokenCount(originalResult, cpt)
	env["result_metadata"] = resultMetadata

	// If query metadata itself is very large, compact that too while preserving
	// key job identifiers and navigation hints.
	for estimateAnyTokenCount(env, cpt) > maxTokens {
		changed := false
		if qm, ok := env["query_metadata"].(map[string]any); ok && len(qm) > 0 {
			compactedQM := compactQueryMetadata(qm)
			if estimateAnyTokenCount(compactedQM, cpt) < estimateAnyTokenCount(qm, cpt) {
				env["query_metadata"] = compactedQM
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	prevEnvelopeTokens := estimateAnyTokenCount(env, cpt)
	for i := 0; i < maxBudgetCompactionIterations && prevEnvelopeTokens > maxTokens; i++ {
		currentResult := env["result"]
		currentResultTokens := estimateAnyTokenCount(currentResult, cpt)
		nextBudget := max(1, currentResultTokens/2)
		nextResult := compactValueToTokenBudget(currentResult, nextBudget, cpt)
		if estimateAnyTokenCount(nextResult, cpt) >= currentResultTokens {
			nextResult = minimalToolResultValueForBudget(originalResult, nextBudget, cpt)
		}
		env["result"] = nextResult
		nextEnvelopeTokens := estimateAnyTokenCount(env, cpt)
		if nextEnvelopeTokens >= prevEnvelopeTokens {
			break
		}
		prevEnvelopeTokens = nextEnvelopeTokens
	}
	if estimateAnyTokenCount(env, cpt) > maxTokens {
		env = minimalToolResultEnvelope(env, originalResult, maxTokens, cpt, stage)
	} else {
		resultMetadata["result_tokens_after"] = estimateAnyTokenCount(env["result"], cpt)
		env["result_metadata"] = resultMetadata
	}

	encoded, err := json.Marshal(env)
	if err != nil {
		return msg, false
	}
	nextContent := string(encoded)
	if strings.TrimSpace(nextContent) == raw {
		return msg, false
	}
	msg.Content = &nextContent
	return msg, true
}

func compactQueryMetadata(in map[string]any) map[string]any {
	out := map[string]any{}
	priorityKeys := []string{
		"provider",
		"job_id",
		"query_obligation_id",
		"provider_job_id",
		"provider_job_url",
		"warehouse_job_id",
		"job_location",
		"location",
		"statement_id",
		"query_id",
		"project_id",
		"host",
		"url",
		"account_identifier",
		"owner_token",
		"thread_id",
		"artifact_id",
		"status",
		"has_more",
		"next_page_token",
		"offset",
		"page_size",
		"rows_returned",
		"effective_limit",
		"requested_limit",
		"configured_limit",
		"limit_capped",
	}
	for _, key := range priorityKeys {
		if raw, ok := in[key]; ok {
			if key == "query_obligation_id" {
				if obligationID := normalizeCompactionQueryObligationID(raw); obligationID != "" {
					out[key] = obligationID
				}
				continue
			}
			out[key] = raw
		}
	}
	if len(out) == 0 {
		fallback := compactToolResultPayload(in).(map[string]any)
		delete(fallback, "query_obligation_id")
		return fallback
	}
	out["...truncated"] = true
	out["compaction_hint"] = "Query metadata trimmed for token budget; use status or result paging tools for full details."
	return out
}

func compactQueryIdentityMetadata(value any) map[string]any {
	obligationID := compactQueryObligationID(value)
	if obligationID == "" {
		return nil
	}
	out := map[string]any{"query_obligation_id": obligationID}
	if m, ok := value.(map[string]any); ok {
		for _, key := range []string{"provider", "job_id", "thread_id", "run_id", "status"} {
			if raw, exists := m[key]; exists {
				out[key] = raw
			}
		}
		if qm, ok := m["query_metadata"].(map[string]any); ok {
			for _, key := range []string{"provider", "job_id", "thread_id", "run_id", "status"} {
				if _, exists := out[key]; exists {
					continue
				}
				if raw, exists := qm[key]; exists {
					out[key] = raw
				}
			}
		}
	}
	return out
}

func compactQueryObligationID(value any) string {
	m, ok := value.(map[string]any)
	if !ok || len(m) == 0 {
		return ""
	}
	if obligationID := normalizeCompactionQueryObligationID(m["query_obligation_id"]); obligationID != "" {
		return obligationID
	}
	for _, key := range []string{"query_metadata", "result"} {
		if nested, ok := m[key].(map[string]any); ok {
			if obligationID := normalizeCompactionQueryObligationID(nested["query_obligation_id"]); obligationID != "" {
				return obligationID
			}
		}
	}
	return ""
}

func normalizeCompactionQueryObligationID(value any) string {
	obligationID, ok := value.(string)
	if !ok {
		return ""
	}
	obligationID = strings.TrimSpace(obligationID)
	if len(obligationID) != len("qob-")+32 || !strings.HasPrefix(obligationID, "qob-") {
		return ""
	}
	allZero := true
	for _, ch := range obligationID[len("qob-"):] {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
		if ch != '0' {
			allZero = false
		}
	}
	if allZero {
		return ""
	}
	return obligationID
}

func compactValueToTokenBudget(value any, maxTokens int, charsPerToken int) any {
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	if maxTokens <= 0 {
		return minimalToolResultValueForBudget(value, maxTokens, cpt)
	}
	if estimateAnyTokenCount(value, cpt) <= maxTokens {
		return value
	}

	compacted := compactToolResultPayload(value)
	if estimateAnyTokenCount(compacted, cpt) <= maxTokens {
		return compacted
	}

	preview := map[string]any{
		"...truncated": true,
	}
	if nav := extractNavigationHints(value); len(nav) > 0 {
		preview["navigation"] = nav
	}
	serialized := marshalToString(value)
	originalTokens := estimateAnyTokenCount(value, cpt)
	candidates := make([]any, 0, maxBudgetCompactionIterations+5)
	addPreviewCandidate := func(charBudget int) {
		candidate := shallowCopyStringAnyMap(preview)
		candidate["preview"] = truncateForToolPreview(serialized, max(0, charBudget))
		candidate["original_tokens"] = originalTokens
		candidate["returned_tokens"] = estimateAnyTokenCount(candidate, cpt)
		candidates = append(candidates, candidate)
	}

	charBudget := max(0, maxTokens*cpt-256)
	seenBudgets := map[int]bool{}
	for i := 0; i < maxBudgetCompactionIterations; i++ {
		if !seenBudgets[charBudget] {
			addPreviewCandidate(charBudget)
			seenBudgets[charBudget] = true
		}
		if charBudget == 0 {
			break
		}
		nextBudget := charBudget / 2
		if nextBudget == charBudget {
			break
		}
		charBudget = nextBudget
	}

	withOriginalTokens := shallowCopyStringAnyMap(preview)
	withOriginalTokens["original_tokens"] = originalTokens
	candidates = append(candidates, withOriginalTokens)
	candidates = append(candidates, preview)
	if _, hasNavigation := preview["navigation"]; hasNavigation {
		candidates = append(candidates, map[string]any{"...truncated": true})
	}
	candidates = append(candidates, minimalToolResultValueForBudget(value, maxTokens, cpt))

	best := candidates[0]
	bestTokens := estimateAnyTokenCount(best, cpt)
	for _, candidate := range candidates {
		tokens := estimateAnyTokenCount(candidate, cpt)
		if tokens <= maxTokens {
			return candidate
		}
		if tokens < bestTokens {
			best = candidate
			bestTokens = tokens
		}
	}
	return best
}

func compactStringToTokenBudget(raw string, maxTokens int, charsPerToken int) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || maxTokens <= 0 {
		return ""
	}
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	if estimateStringTokenCount(raw, cpt) <= maxTokens {
		return raw
	}
	suffix := "... [truncated]"
	maxChars := maxTokens * cpt
	if maxChars <= len(suffix) {
		if estimateStringTokenCount("...", cpt) <= maxTokens {
			return "..."
		}
		return ""
	}
	charBudget := maxChars - len(suffix)
	for i := 0; i < maxBudgetCompactionIterations; i++ {
		candidate := truncateForToolPreview(raw, charBudget)
		if estimateStringTokenCount(candidate, cpt) <= maxTokens {
			return candidate
		}
		if charBudget == 0 {
			break
		}
		nextBudget := charBudget / 2
		if nextBudget == charBudget {
			break
		}
		charBudget = nextBudget
	}
	if estimateStringTokenCount("...", cpt) <= maxTokens {
		return "..."
	}
	return ""
}

func minimalToolResultValue(value any, charsPerToken int) map[string]any {
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	out := map[string]any{
		"...truncated": true,
		"note":         "Result omitted because the tool output exceeded the model context budget.",
	}
	if nav := extractNavigationHints(value); len(nav) > 0 {
		out["navigation"] = nav
	}
	if obligationID := compactQueryObligationID(value); obligationID != "" {
		out["query_obligation_id"] = obligationID
	}
	out["original_tokens"] = estimateAnyTokenCount(value, cpt)
	return out
}

func minimalToolResultValueForBudget(value any, maxTokens int, charsPerToken int) any {
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	minimal := minimalToolResultValue(value, cpt)
	if maxTokens > 0 && estimateAnyTokenCount(minimal, cpt) <= maxTokens {
		return minimal
	}
	truncatedOnly := map[string]any{"...truncated": true}
	if obligationID := compactQueryObligationID(value); obligationID != "" {
		truncatedOnly["query_obligation_id"] = obligationID
		if maxTokens <= 0 || estimateAnyTokenCount(truncatedOnly, cpt) <= maxTokens {
			return truncatedOnly
		}
		// The obligation identity is an invariant, even when an unrealistically
		// small token budget cannot represent the complete minimal value.
		return truncatedOnly
	}
	if maxTokens > 0 && estimateAnyTokenCount(truncatedOnly, cpt) <= maxTokens {
		return truncatedOnly
	}
	if maxTokens > 0 && estimateAnyTokenCount("...", cpt) <= maxTokens {
		return "..."
	}
	return ""
}

func minimalToolResultEnvelope(env map[string]any, originalResult any, maxTokens int, charsPerToken int, stage string) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"artifact_id", "full_artifact_id", "tool_name", "tool_call_id", "ok", "result_format"} {
		if raw, ok := env[key]; ok {
			out[key] = raw
		}
	}
	identityQM := compactQueryIdentityMetadata(env)
	if len(identityQM) == 0 {
		identityQM = compactQueryIdentityMetadata(originalResult)
	}
	if len(identityQM) > 0 {
		out["query_metadata"] = identityQM
	}
	out["result"] = minimalToolResultValue(originalResult, charsPerToken)
	out["result_metadata"] = map[string]any{
		"compacted":             true,
		"compaction_stage":      stage,
		"compaction_strategy":   "budget_aware",
		"compaction_fallback":   "minimal_envelope",
		"result_token_budget":   maxTokens,
		"result_tokens_before":  estimateAnyTokenCount(originalResult, charsPerToken),
		"result_tokens_after":   estimateAnyTokenCount(out["result"], charsPerToken),
		"envelope_token_budget": maxTokens,
	}
	if estimateAnyTokenCount(out, charsPerToken) <= maxTokens {
		return out
	}
	out["result"] = map[string]any{"...truncated": true}
	out["result_metadata"] = map[string]any{
		"compacted":             true,
		"compaction_stage":      stage,
		"compaction_strategy":   "budget_aware",
		"compaction_fallback":   "minimal_envelope",
		"envelope_token_budget": maxTokens,
	}
	if estimateAnyTokenCount(out, charsPerToken) <= maxTokens {
		return out
	}
	for _, key := range []string{"full_artifact_id", "result_format", "ok"} {
		delete(out, key)
		if estimateAnyTokenCount(out, charsPerToken) <= maxTokens {
			return out
		}
	}
	return out
}

func extractNavigationHints(value any) map[string]any {
	m, ok := value.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	keys := []string{
		"has_more",
		"next_page_token",
		"offset",
		"page_size",
		"rows_returned",
		"total_rows",
		"total_matches",
		"effective_limit",
		"requested_limit",
		"configured_limit",
		"limit_capped",
	}
	out := map[string]any{}
	for _, key := range keys {
		if raw, exists := m[key]; exists {
			out[key] = raw
		}
	}
	return out
}

func estimateAnyTokenCount(value any, charsPerToken int) int {
	cpt := charsPerToken
	if cpt <= 0 {
		cpt = defaultTokenCharsPerToken
	}
	b, err := json.Marshal(value)
	if err != nil {
		return estimateStringTokenCount(marshalToString(value), cpt)
	}
	return estimateStringTokenCount(string(b), cpt)
}

func marshalToString(value any) string {
	b, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(asStringValue(value))
	}
	return strings.TrimSpace(string(b))
}

func ensureMapValue(raw any) map[string]any {
	if m, ok := raw.(map[string]any); ok {
		return shallowCopyStringAnyMap(m)
	}
	return map[string]any{}
}

func shallowCopyStringAnyMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func asStringValue(v any) string {
	switch typed := v.(type) {
	case string:
		return typed
	case fmtStringer:
		return typed.String()
	case int:
		return strconv.Itoa(typed)
	case int8:
		return strconv.FormatInt(int64(typed), 10)
	case int16:
		return strconv.FormatInt(int64(typed), 10)
	case int32:
		return strconv.FormatInt(int64(typed), 10)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint:
		return strconv.FormatUint(uint64(typed), 10)
	case uint8:
		return strconv.FormatUint(uint64(typed), 10)
	case uint16:
		return strconv.FormatUint(uint64(typed), 10)
	case uint32:
		return strconv.FormatUint(uint64(typed), 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", v))
	}
}

type fmtStringer interface {
	String() string
}
